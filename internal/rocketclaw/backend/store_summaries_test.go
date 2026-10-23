package backend

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestSessionSummaryProjectionAndIncrementalAppend(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	header := provenanceHeader(&promptProvenance{origin: "Web", media: "Text", principal: "alice", additionalInstructions: defaultReplyInstruction})
	body, err := json.Marshal(header + "\n\n" + header + "\n\nbody")
	require.NoError(t, err)
	headerJSON, err := json.Marshal(header)
	require.NoError(t, err)

	for _, tc := range []struct{ name, raw, want string }{
		{"envelope", `{"type":"message","role":"user","prompt_header":` + string(headerJSON) + `,"content":` + string(body) + `}`, header + "\n\nbody"},
		{"legacy", `{"type":"message","role":"user","content":` + string(body) + `}`, header + "\n\n" + header + "\n\nbody"},
		{"multipart", `{"type":"message","role":"user","content":[{"type":"input_text","text":"part-a"},{"type":"input_text","text":"part-b\u0000"}]}`, "part-apart-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := &harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(2, 123456789).UTC(), ReplayInput: []json.RawMessage{json.RawMessage(tc.raw)}}
			insertSessionEntryWithoutSummary(t, service, tc.name, entry)
			require.NoError(t, service.UpsertThread(tc.name, ThreadState{Agent: "main"}))
			require.Equal(t, tc.want, sidebarSummaries(t, service)[tc.name].LastMessage)
			// Following appends use only the stored summary.
			for _, message := range []struct{ role, text, want string }{
				{"assistant", "answer\x00", "answer"},
				{"assistant", "\x00", "answer"},
				{"user", " \x00\n", "answer"},
				{"user", "latest user", "latest user"},
				{"developer", "not a visible message", "latest user"},
				{"user", " \n", "latest user"},
				{"assistant", " \n", "latest user"},
			} {
				replay := replayInputForMessage(message.role, message.text)
				_, err := service.AppendEntryID(t.Context(), tc.name, &harness.SessionEntry{Timestamp: time.Unix(1, 987654321).UTC(), ReplayInput: replay})
				require.NoError(t, err)
				require.Equal(t, message.want, sidebarSummaries(t, service)[tc.name].LastMessage)
			}
			// A poisoned old row detects accidental history reads on initialized append/list paths.
			_, err = service.db.ExecContext(t.Context(), `UPDATE entries SET entry_json = '{"timestamp":false}' WHERE conversation_id = $1`, tc.name)
			require.NoError(t, err)
			_, err = service.AppendEntryID(t.Context(), tc.name, &harness.SessionEntry{Timestamp: time.Unix(1, 987654321).UTC()})
			require.NoError(t, err)
			require.Equal(t, &protocol.SessionSummary{ConversationID: tc.name, LastMessage: "latest user", LastUpdated: time.Unix(1, 987654000).UTC()}, sidebarSummaries(t, service)[tc.name])
		})
	}
}

// sidebarSummaries returns the summary of each chat the sidebar lists, by conversation ID.
func sidebarSummaries(t *testing.T, s *SessionService) map[string]*protocol.SessionSummary {
	t.Helper()

	summaries := map[string]*protocol.SessionSummary{}

	for row, err := range s.sidebarSessions(t.Context(), "", nil) {
		require.NoError(t, err)

		summaries[row.Conversation.ID] = row.Summary
	}

	return summaries
}

func TestConversationSummaryInitializationRollsBackOnFailure(t *testing.T) {
	t.Parallel()

	// Decoding is shared by every creation path; each path's writes and transaction start fail on their own lines.
	for _, tc := range []struct{ creation, failure, message string }{
		{"upsert", "orphan decode", "parse session summary history"},
		{"create", "replay decode", "decode session summary replay input"},
		{"upsert", "summary write", "write session summary"},
		{"external MCP", "summary write", "write session summary"},
		{"upsert", "database unavailable", "database is closed"},
		{"create", "database unavailable", "database is closed"},
		{"external MCP", "database unavailable", "database is closed"},
	} {
		t.Run(tc.creation+"/"+tc.failure, func(t *testing.T) {
			workspace := t.TempDir()
			service := newTestSessionServiceAt(t, workspace)
			insertSessionEntryWithoutSummary(t, service, "history", testSessionEntry("orphan preview", "answer"))

			inspection, err := sql.Open("pgx", testStoreDSN(workspace))

			require.NoError(t, err)
			defer func() { require.NoError(t, inspection.Close()) }()

			switch tc.failure {
			case "orphan decode":
				_, err = inspection.ExecContext(t.Context(), `UPDATE entries SET entry_json = '{"timestamp":false}' WHERE conversation_id = 'history'`)
				require.NoError(t, err)
			case "replay decode":
				_, err = inspection.ExecContext(t.Context(), `UPDATE entries SET entry_json = '{"replay_input":[{"type":"compaction","content":42}]}' WHERE conversation_id = 'history'`)
				require.NoError(t, err)
			case "summary write":
				// A real database write rejection must roll back the earlier record/binding inserts.
				_, err = inspection.ExecContext(t.Context(), `ALTER TABLE conversations ADD CONSTRAINT reject_summary CHECK (conversation_id <> 'history' OR preview IS NULL)`)
				require.NoError(t, err)
			case "database unavailable":
				require.NoError(t, service.Stop())
			}

			var before string
			require.NoError(t, inspection.QueryRowContext(t.Context(), `SELECT entry_json FROM entries WHERE conversation_id = 'history'`).Scan(&before))

			switch tc.creation {
			case "upsert":
				err = service.UpsertThread("history", ThreadState{Agent: "main"})
			case "create":
				runtime := &Runtime{Sessions: service}
				err = runtime.CreateConversation(t.Context(), protocol.Conversation{ID: "history", Agent: "main"})
			case "external MCP":
				err = service.RegisterExternalMCPConversation("public", "main", &ExternalMCPSessionState{PrivateConversationID: "private", ManagedConversationID: "history", Agent: "main"})
			}

			require.ErrorContains(t, err, tc.message)

			// The preview and binding live on the rolled-back rows.
			var recorded bool
			require.NoError(t, inspection.QueryRowContext(t.Context(), `SELECT count(*) > 0 FROM conversations`).Scan(&recorded))
			require.False(t, recorded)

			var after string
			require.NoError(t, inspection.QueryRowContext(t.Context(), `SELECT entry_json FROM entries WHERE conversation_id = 'history'`).Scan(&after))
			require.Equal(t, before, after, "failed initialization must preserve the orphan history")
		})
	}
}

func TestSidebarSessionsNeverDecodeHistoryForInitializedConversations(t *testing.T) {
	t.Parallel()

	// CreateConversation initializes summaries through the same stateDAO path as External MCP registration.
	for _, creation := range []string{"upsert", "external MCP"} {
		t.Run(creation, func(t *testing.T) {
			workspace := t.TempDir()
			service := newTestSessionServiceAt(t, workspace)
			insertSessionEntryWithoutSummary(t, service, "history", testSessionEntryAt(time.Unix(2, 123456789).UTC(), "orphan preview"))

			for _, id := range []string{"empty", "history"} {
				switch creation {
				case "upsert":
					require.NoError(t, service.UpsertThread(id, ThreadState{Agent: "main"}))
				case "external MCP":
					require.NoError(t, service.RegisterExternalMCPConversation("public-"+id, "main", &ExternalMCPSessionState{PrivateConversationID: "private-" + id, ManagedConversationID: id, Agent: "main"}))
				}
			}

			_, err := service.db.ExecContext(t.Context(), `UPDATE entries SET entry_json = '{"timestamp":false}'`)
			require.NoError(t, err)

			// Pin the listing pool's session setting; the lock uses a separate connection.
			service.db.SetMaxOpenConns(1)
			_, err = service.db.ExecContext(t.Context(), `SET lock_timeout = '10ms'`)
			require.NoError(t, err)
			blocker, err := sql.Open("pgx", testStoreDSN(workspace))

			require.NoError(t, err)
			defer func() { require.NoError(t, blocker.Close()) }()

			transaction, err := blocker.BeginTx(t.Context(), nil)

			require.NoError(t, err)
			defer func() { require.NoError(t, transaction.Rollback()) }()

			// Checking entry presence must not decode replay or wait for history writers.
			_, err = transaction.ExecContext(t.Context(), `LOCK TABLE entries IN EXCLUSIVE MODE`)
			require.NoError(t, err)

			var rows []SidebarSession

			for row, err := range service.sidebarSessions(t.Context(), "", nil) {
				require.NoError(t, err, "sidebar enumeration must not decode replay history")

				rows = append(rows, row)
			}

			origin, kind := ChatOriginFacts{ConversationID: "history"}, KindWebChat
			if creation == "external MCP" {
				origin.ExternalConversationID, origin.Binding = "public-history", ExternalMCPSessionState{Agent: "main", ManagedConversationID: "history"}
				kind = KindSlackThread
			}

			require.Equal(t, []SidebarSession{
				{Conversation: protocol.Conversation{ID: "history", Agent: "main"}, Kind: kind, Summary: &protocol.SessionSummary{ConversationID: "history", LastMessage: "assistant", LastUpdated: time.Unix(2, 123456000).UTC()}, Tags: []string{}, Origin: origin},
			}, rows)
		})
	}
}

// Whether a chat has history is stored on its row (saveSessionSummary), so listing reads no entry.
func TestSidebarSessionsReadNoHistory(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	service.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := service.db.ExecContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT 'history-only-' || n, '{}', '' FROM generate_series(1, 4096) n`)
	require.NoError(t, err)

	for _, id := range []string{"visible", "empty"} {
		require.NoError(t, service.UpsertThread(id, ThreadState{Agent: "main"}))
	}

	for range 2 {
		_, err = service.AppendEntryID(ctx, "visible", testSessionEntry("question", "answer"))
		require.NoError(t, err)
	}

	_, err = service.db.ExecContext(ctx, `ANALYZE entries; ANALYZE conversations`)
	require.NoError(t, err)
	_, err = service.db.ExecContext(ctx, `SELECT pg_stat_force_next_flush()`)
	require.NoError(t, err)

	const historyReads = `SELECT pg_stat_get_tuples_returned('entries'::regclass)
    + COALESCE(SUM(pg_stat_get_tuples_returned(indexrelid)), 0)
FROM pg_index WHERE indrelid = 'entries'::regclass`

	var before, after int64
	require.NoError(t, service.db.QueryRowContext(ctx, historyReads).Scan(&before))

	var ids []string

	for row, err := range service.sidebarSessions(ctx, "", nil) {
		require.NoError(t, err)

		ids = append(ids, row.Conversation.ID)
	}

	require.Equal(t, []string{"visible"}, ids)

	_, err = service.db.ExecContext(ctx, `SELECT pg_stat_force_next_flush()`)
	require.NoError(t, err)
	require.NoError(t, service.db.QueryRowContext(ctx, historyReads).Scan(&after))
	require.Zero(t, after-before, "the sidebar reads no entry")
}

func TestSidebarSessionPinsAndNames(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 123456000, time.UTC)

	for _, id := range []string{"a", "Z", "ä"} {
		require.NoError(t, service.UpsertThread(id, ThreadState{Agent: "main"}))
		_, err := service.AppendEntryID(ctx, id, testSessionEntryAt(stamp, "original"))
		require.NoError(t, err)
	}

	for _, id := range []string{"a", "ä"} {
		updated, err := service.UpdateConversationDetails(ctx, id, new(true), new("Release notes"))
		require.NoError(t, err)
		require.True(t, updated)
	}

	updated, err := service.UpdateConversationDetails(ctx, "a", nil, new("Renamed"))
	require.NoError(t, err)
	require.True(t, updated)
	require.NoError(t, service.UpsertThread("a", ThreadState{Agent: "main"}))

	var ids []string

	for row, err := range service.sidebarSessions(ctx, "", nil) {
		require.NoError(t, err)

		ids = append(ids, row.Conversation.ID)
		require.Equal(t, row.Conversation.ID != "Z", row.Pinned)
		require.Equal(t, stamp, row.Summary.LastUpdated)

		if row.Conversation.ID == "a" {
			require.Equal(t, "Renamed", row.Name)
		}
	}

	require.Equal(t, []string{"a", "ä", "Z"}, ids)

	_, err = service.UpdateConversationDetails(ctx, "ä", new(false), nil)
	require.NoError(t, err)
	_, err = service.UpdateConversationDetails(ctx, "a", nil, new(""))
	require.NoError(t, err)

	for row, err := range service.sidebarSessions(ctx, "", nil) {
		require.NoError(t, err)

		switch row.Conversation.ID {
		case "a":
			require.True(t, row.Pinned)
			require.Empty(t, row.Name)
		case "ä":
			require.False(t, row.Pinned)
			require.Equal(t, "Release notes", row.Name)
		}
	}

	updated, err = service.UpdateConversationDetails(ctx, "missing", new(true), nil)
	require.NoError(t, err)
	require.False(t, updated)
}

func TestSidebarSessionsListFirstTurnBeforeHistory(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, service.UpsertThread("new", ThreadState{Agent: "main"}))
	seedActiveTurn(t, service, "new", "first-turn", nil)
	updated, err := service.UpdateConversationDetails(ctx, "new", nil, new("Draft"))
	require.NoError(t, err)
	require.True(t, updated)

	only := func() SidebarSession {
		var got []SidebarSession

		for row, err := range service.sidebarSessions(ctx, "", nil) {
			require.NoError(t, err)

			got = append(got, row)
		}

		require.Len(t, got, 1)

		return got[0]
	}

	row := only()
	require.Equal(t, "Draft", row.Name)
	require.True(t, row.Running)
	require.Equal(t, "seed", row.Summary.LastMessage)
	require.WithinDuration(t, time.Now(), row.Summary.LastUpdated, time.Minute)

	_, err = service.finishTurn(ctx, "first-turn", &turnFinish{store: newSessionStore("new", service), outbound: protocol.NewOutboundMessage("new", ""), entries: []harness.SessionEntry{*testSessionEntry("question", "answer")}})
	require.NoError(t, err)
	require.Equal(t, "answer", only().Summary.LastMessage)
	require.NoError(t, service.closeTurn(ctx, "first-turn"))

	row = only()
	require.Equal(t, "Draft", row.Name)
	require.False(t, row.Running)
	require.Equal(t, "answer", row.Summary.LastMessage)
}

func TestSidebarSessionsOrderMembershipAndCompleteness(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	recordTestConversations(t, service, "main", map[string]ConversationKind{"cron:private": KindCronRun, "one-off-cron:private": KindOneOffCronRun, "private": KindExternalMCPRun})

	for _, id := range []string{"a", "Z", "ä", "empty", "missing", "zero", "cron:private", "one-off-cron:private", "private"} {
		require.NoError(t, service.UpsertThread(id, ThreadState{Agent: "main"}))

		if id != "zero" {
			_, err := service.toggleSessionTag(t.Context(), id, "customer", []string{"customer", "internal"})
			require.NoError(t, err)
		}
	}

	require.NoError(t, service.UpsertExternalMCPSession("external", &ExternalMCPSessionState{PrivateConversationID: "private", ManagedConversationID: "not-recorded"}))

	stamp := time.Unix(2, 123456789).UTC()
	for _, id := range []string{"a", "a", "Z", "ä", "cron:private", "one-off-cron:private", "private", "history-only"} {
		_, err := service.AppendEntryID(t.Context(), id, testSessionEntryAt(stamp, id+"\x00full"))
		require.NoError(t, err)
	}

	for _, turnID := range []string{"active-first", "active-second"} {
		seedActiveTurn(t, service, "a", turnID, nil)
	}

	_, err := service.AppendEntryID(t.Context(), "zero", &harness.SessionEntry{})
	require.NoError(t, err)
	insertSessionEntryWithoutSummary(t, service, "missing", testSessionEntry("old", "answer"))
	// A summary missing since before migration 052, which still stored that the chat has entries.
	_, err = service.db.ExecContext(t.Context(), `UPDATE conversations SET preview = NULL, last_updated = NULL, has_entries = TRUE WHERE conversation_id = 'missing'`)
	require.NoError(t, err)
	// Enumeration must not parse replay even for missing summaries.
	_, err = service.db.ExecContext(t.Context(), `UPDATE entries SET entry_json = '{"timestamp":false}'`)
	require.NoError(t, err)

	var got []SidebarSession

	for row, err := range service.sidebarSessions(t.Context(), "", nil) {
		require.NoError(t, err)

		got = append(got, row)
	}

	want := make([]SidebarSession, 0, 5)

	for _, id := range []string{"Z", "a", "ä", "missing", "zero"} {
		row := SidebarSession{Conversation: protocol.Conversation{ID: id, Agent: "main"}, Kind: KindWebChat, Running: id == "a", Origin: ChatOriginFacts{ConversationID: id}}

		row.Tags = []string{"customer"}
		if id == "zero" {
			row.Tags = []string{}
		}

		if id == "zero" {
			row.Summary = &protocol.SessionSummary{ConversationID: id}
		} else if id != "missing" {
			row.Summary = &protocol.SessionSummary{ConversationID: id, LastUpdated: stamp.Truncate(time.Microsecond), LastMessage: "assistant"}
		}

		want = append(want, row)
	}

	require.Equal(t, want, got)
	_, err = service.toggleSessionTag(t.Context(), "a", "internal", []string{"customer", "internal"})
	require.NoError(t, err)

	for row, err := range service.sidebarSessions(t.Context(), "", nil) {
		require.NoError(t, err)

		if row.Conversation.ID == "a" {
			require.Equal(t, []string{"internal"}, row.Tags)
			row.Tags = got[1].Tags
			require.Equal(t, got[1], row, "tag-only changes do not affect sidebar activity")
		}
	}

	_, found, err := service.Thread("empty")
	require.NoError(t, err)
	require.True(t, found, "hiding empty history must preserve the conversation")
	_, err = service.AppendEntryID(t.Context(), "empty", testSessionEntryAt(stamp, "first message"))
	require.NoError(t, err)

	// A running turn keeps a cleared chat listed.
	for _, turnID := range []string{"active-first", "active-second"} {
		endTestTurn(t, service, "a", turnID, protocol.TerminalStopped)
	}

	_, err = service.DeleteSession(t.Context(), "a")
	require.NoError(t, err)

	var remaining []string

	for row, err := range service.sidebarSessions(t.Context(), "", nil) {
		require.NoError(t, err)

		remaining = append(remaining, row.Conversation.ID)
	}

	require.Equal(t, []string{"Z", "empty", "ä", "missing", "zero"}, remaining)

	// PostgreSQL accepts infinity timestamps, but Go time.Time cannot represent them.
	// A bad stored row stops enumeration with a scan error before any row.
	_, err = service.db.ExecContext(t.Context(), `UPDATE conversations SET last_updated = '-infinity' WHERE conversation_id = 'zero'`)
	require.NoError(t, err)

	next, stop := iter.Pull2(service.sidebarSessions(t.Context(), "", nil))
	defer stop()

	_, err, ok := next()
	require.True(t, ok)
	require.ErrorContains(t, err, "scan sidebar session")

	_, _, ok = next()
	require.False(t, ok)
	require.Zero(t, service.db.Stats().InUse)
}

func TestSidebarSessionsPreviewAllocationBudget(t *testing.T) {
	result := testing.Benchmark(BenchmarkSidebarSessions)
	require.Positive(t, result.N)
	require.Less(t, result.AllocedBytesPerOp(), int64(7<<20))
	require.Less(t, result.AllocsPerOp(), int64(1200)) // Includes race-detector overhead.
}

func BenchmarkSidebarSessions(b *testing.B) {
	service, err := NewSessionService(b.TempDir())
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, service.Stop()) })

	previews := make([]string, 32)
	for i := range previews {
		id := fmt.Sprintf("sidebar-%02d", i)
		previews[i] = strings.Repeat(id, 8192)
		require.NoError(b, service.UpsertThread(id, ThreadState{Agent: "main"}))
		_, err := service.AppendEntryID(b.Context(), id, testSessionEntry("question", previews[i]))
		require.NoError(b, err)
	}

	b.ReportAllocs()

	for b.Loop() {
		var rows []SidebarSession

		for row, err := range service.sidebarSessions(b.Context(), "", nil) {
			require.NoError(b, err)

			rows = append(rows, row)
		}

		require.Len(b, rows, 32)

		for i := range rows {
			row := &rows[i]
			require.Equal(b, fmt.Sprintf("sidebar-%02d", i), row.Conversation.ID)
			require.Equal(b, previews[i], row.Summary.LastMessage)
		}
	}
}

func TestSidebarSessionsCloseRowsOnEarlyStopAndCancellation(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	for _, id := range []string{"a", "b"} {
		require.NoError(t, service.UpsertThread(id, ThreadState{Agent: "main"}))
		_, err := service.AppendEntryID(t.Context(), id, testSessionEntry(strings.Repeat(id, 16384), ""))
		require.NoError(t, err)
	}

	before := service.db.Stats().InUse

	next, stop := iter.Pull2(service.sidebarSessions(t.Context(), "", nil))
	defer stop()

	row, err, ok := next()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "a", row.Conversation.ID)
	stop()
	require.Equal(t, before, service.db.Stats().InUse)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// Cancellation before the first read must also report failure, not an empty list.
	next, stop = iter.Pull2(service.sidebarSessions(ctx, "", nil))
	defer stop()

	row, err, ok = next()
	require.True(t, ok)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, row)

	_, _, ok = next()
	require.False(t, ok)
	require.Equal(t, before, service.db.Stats().InUse)
}

func TestSidebarSessionsCancelPendingDatabaseRow(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()

	service := newTestSessionServiceAt(t, workspace)
	for _, id := range []string{"a", "b"} {
		require.NoError(t, service.UpsertThread(id, ThreadState{Agent: "main"}))
		_, err := service.AppendEntryID(t.Context(), id, testSessionEntry(id, ""))
		require.NoError(t, err)
	}

	dsn, err := url.Parse(testStoreDSN(workspace))
	require.NoError(t, err)

	upstream := dsn.Host
	listener, err := net.Listen("tcp", "127.0.0.1:0")

	require.NoError(t, err)
	defer func() { require.NoError(t, listener.Close()) }()

	dsn.Host = listener.Addr().String()

	require.NoError(t, service.db.Close())
	service.db, err = sql.Open("pgx", dsn.String())
	require.NoError(t, err)

	tail := make(chan struct{})
	cancelled := make(chan struct{})

	var proxy errgroup.Group
	proxy.Go(func() error {
		client, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("accept database proxy: %w", err)
		}
		defer func() { _ = client.Close() }()

		database, err := net.Dial("tcp", upstream)
		if err != nil {
			return fmt.Errorf("dial test database: %w", err)
		}

		defer func() { _ = database.Close() }()
		// pgx sends PostgreSQL cancellation over a separate short connection.
		proxy.Go(func() error {
			client, err := listener.Accept()
			if err != nil {
				return fmt.Errorf("accept cancellation: %w", err)
			}
			defer func() { _ = client.Close() }()

			database, err := net.Dial("tcp", upstream)
			if err != nil {
				return fmt.Errorf("dial cancellation: %w", err)
			}

			defer func() { _ = database.Close() }()

			if _, err := io.CopyN(database, client, 16); err != nil {
				return fmt.Errorf("forward cancellation: %w", err)
			}

			close(cancelled)

			_, err = io.Copy(client, database)
			if err != nil {
				return fmt.Errorf("read cancellation response: %w", err)
			}

			return nil
		})

		var forwarding errgroup.Group
		forwarding.Go(func() error {
			_, err := io.Copy(database, client)
			if errors.Is(err, net.ErrClosed) {
				return nil
			}

			if err != nil {
				return fmt.Errorf("forward query: %w", err)
			}

			return nil
		})
		// Gate the real PostgreSQL wire after its first DataRow, independently
		// of SQL execution/planner buffering and of the Go iterator consumer.
		for rows := 0; ; {
			var header [5]byte
			if _, err := io.ReadFull(database, header[:]); err != nil {
				return fmt.Errorf("read database message: %w", err)
			}

			body := make([]byte, int(binary.BigEndian.Uint32(header[1:]))-4)
			if _, err := io.ReadFull(database, body); err != nil {
				return fmt.Errorf("read database payload: %w", err)
			}

			if header[0] == 'D' {
				rows++
				if rows == 2 {
					close(tail)
					<-cancelled
				}
			}

			if _, err := client.Write(append(header[:], body...)); err != nil {
				return fmt.Errorf("forward database message: %w", err)
			}

			if rows == 2 && header[0] == 'Z' {
				if err := client.Close(); err != nil {
					return fmt.Errorf("close database proxy: %w", err)
				}

				return forwarding.Wait()
			}
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var reading errgroup.Group
	reading.Go(func() error {
		for _, err := range service.sidebarSessions(ctx, "", nil) {
			if err != nil {
				return err
			}
		}

		return nil
	})
	<-tail
	cancel()
	require.ErrorIs(t, reading.Wait(), context.Canceled)
	require.NoError(t, proxy.Wait())
	require.Zero(t, service.db.Stats().InUse)
}

func insertSessionEntryWithoutSummary(t *testing.T, service *SessionService, conversationID string, entry *harness.SessionEntry) {
	t.Helper()

	data, err := json.Marshal(entry)
	require.NoError(t, err)

	data = removeSessionEntryNUL(data)
	_, err = service.db.ExecContext(t.Context(), `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ($1, $2, $3)`, conversationID, string(data), entry.Timestamp.UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
}

// Saving an entry writes its conversation's preview and time in the same transaction, and a
// failed save leaves both as they were.
func TestEntryPreviewCommitsWithItsEntry(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, service.UpsertThread("chat", ThreadState{Agent: "main"}))
	_, err := service.AppendEntryID(ctx, "chat", testSessionEntry("question", "first"))
	require.NoError(t, err)

	preview := func(db stateStoreDB) string {
		var (
			text string
			at   time.Time
		)
		require.NoError(t, db.QueryRowContext(ctx, `SELECT convert_from(preview, 'UTF8'), last_updated FROM conversations WHERE conversation_id = 'chat'`).Scan(&text, &at))

		return text + " " + at.UTC().Format(time.RFC3339)
	}

	before := preview(service.db)
	require.Equal(t, "first 1970-01-01T00:00:01Z", before)

	second := testSessionEntry("question", "second")
	second.Timestamp = time.Unix(2, 0).UTC()
	tx, err := service.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = appendSessionEntryDB(ctx, tx, "chat", second)
	require.NoError(t, err)
	require.Equal(t, "second 1970-01-01T00:00:02Z", preview(tx), "the saving transaction sees its preview")
	require.NoError(t, tx.Rollback())
	require.Equal(t, before, preview(service.db), "a rolled-back save leaves the preview")

	// A rejected preview write fails the save, so neither the entry nor its preview lands.
	_, err = service.db.ExecContext(ctx, `ALTER TABLE conversations ADD CONSTRAINT reject_preview CHECK (preview <> 'third') NOT VALID`)
	require.NoError(t, err)
	_, err = service.AppendEntryID(ctx, "chat", testSessionEntry("question", "third"))
	require.ErrorContains(t, err, "write session summary")
	require.Equal(t, before, preview(service.db), "a failed save leaves the preview")

	entries, err := service.ObserveEntries(ctx, "chat")
	require.NoError(t, err)
	require.Len(t, entries, 1)

	_, err = service.AppendEntryID(ctx, "chat", second)
	require.NoError(t, err)
	require.Equal(t, "second 1970-01-01T00:00:02Z", preview(service.db))
}

// The entry facts a chat's row stores for the sidebar and its origin follow the writes that change
// them: a run attached with entries older than the chat's own, a run reporting after them, and
// clearing the chat's history.
func TestChatEntryFactsFollowWrites(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"early": KindCronRun, "late": KindCronRun})

	for _, id := range []string{"attached", "own"} {
		require.NoError(t, s.UpsertThread(id, ThreadState{Agent: "main"}))
	}

	facts := func(id string) (hasEntries bool, creatingSource string) {
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT has_entries FROM conversations WHERE conversation_id = $1`, id).Scan(&hasEntries))

		for origin, err := range s.ChatOriginFacts(ctx, id) {
			require.NoError(t, err)

			creatingSource = origin.CreatingSource
		}

		return hasEntries, creatingSource
	}

	for _, id := range []string{"early", "own", "attached"} {
		_, err := s.AppendEntryID(ctx, id, testSessionEntry("question", "answer"))
		require.NoError(t, err)
	}

	hasEntries, source := facts("attached")
	require.True(t, hasEntries)
	require.Empty(t, source, "the chat's own entry came first")

	for run, chat := range map[string]string{"early": "attached", "late": "own"} {
		inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "report", true)
		inbound.ConversationID, inbound.SyncDestination = run, chat
		require.NoError(t, startTurnDB(ctx, s.db, "turn-"+run, run, inbound))
	}

	_, err := s.AppendEntryID(ctx, "late", testSessionEntry("report", "done"))
	require.NoError(t, err)

	hasEntries, source = facts("attached")
	require.True(t, hasEntries)
	require.Equal(t, "early", source, "the attached run's older entry is now the chat's first")

	hasEntries, source = facts("own")
	require.True(t, hasEntries)
	require.Empty(t, source, "a run reporting later leaves the chat's own entry first")

	_, err = s.DeleteSession(ctx, "attached")
	require.NoError(t, err)

	hasEntries, source = facts("attached")
	require.False(t, hasEntries)
	require.Equal(t, "early", source, "clearing the chat keeps its run's entry, as undo does")
}
