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
	service := newTestSessionService(t)
	header := provenanceHeader(promptProvenance{origin: "Web", media: "Text", principal: "alice", additionalInstructions: defaultReplyInstruction})
	body, err := json.Marshal(header + "\n\n" + header + "\n\nbody")
	require.NoError(t, err)

	for _, tc := range []struct{ name, raw, want string }{
		{"envelope", `{"type":"message","role":"user","content":` + string(body) + `}`, header + "\n\nbody"},
		{"multipart", `{"type":"message","role":"user","content":[{"type":"input_text","text":"part-a"},{"type":"input_text","text":"part-b\u0000"}]}`, "part-apart-b\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := &harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(2, 123456789).UTC(), ReplayInput: []json.RawMessage{json.RawMessage(tc.raw)}}
			insertSessionEntryWithoutSummary(t, service, tc.name, entry)
			// First append initializes old history; following appends use only the stored summary.
			for _, role := range []string{"developer", "assistant", "user"} {
				text := "later message"
				if role == "user" {
					text = " \n"
				}

				replay, err := replayInputForMessage(role, text)
				require.NoError(t, err)
				_, err = service.AppendEntryID(t.Context(), tc.name, &harness.SessionEntry{Timestamp: time.Unix(1, 987654321).UTC(), ReplayInput: replay})
				require.NoError(t, err)
			}
			// A poisoned old row detects accidental history reads on initialized append/list paths.
			_, err := service.db.ExecContext(t.Context(), `UPDATE session_entries SET entry_json = '{' WHERE conversation_id = $1`, tc.name)
			require.NoError(t, err)
			_, err = service.AppendEntryID(t.Context(), tc.name, &harness.SessionEntry{Timestamp: time.Unix(1, 987654321).UTC()})
			require.NoError(t, err)
			summaries, err := service.ListSessions(t.Context(), []string{tc.name})
			require.NoError(t, err)
			require.Equal(t, []protocol.SessionSummary{{ConversationID: tc.name, LastUserMessage: tc.want, LastUpdated: time.Unix(1, 987654000).UTC()}}, summaries)
		})
	}
}

func TestSessionSummaryBackfillProgressFailureAndResume(t *testing.T) {
	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	insertSessionEntryWithoutSummary(t, service, "a-private", testSessionEntry("alpha\x00", "answer"))
	insertSessionEntryWithoutSummary(t, service, "b-empty", &harness.SessionEntry{})
	_, err := service.db.ExecContext(t.Context(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('c-bad', '{', 'invalid')`)
	require.NoError(t, err)

	var before string
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT json_agg(e ORDER BY id)::text FROM session_entries e`).Scan(&before))
	tx, err := service.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	require.NoError(t, lockSessionHistory(t.Context(), tx, "b-empty"))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var group errgroup.Group
	group.Go(func() error {
		if err := service.backfillSessionSummaries(ctx); err != nil {
			return fmt.Errorf("interrupted backfill: %w", err)
		}

		return nil
	})
	require.Eventually(t, func() bool {
		var committed bool

		err := service.db.QueryRowContext(t.Context(), `SELECT EXISTS (SELECT 1 FROM session_summaries WHERE conversation_id = 'a-private')`).Scan(&committed)
		require.NoError(t, err)

		return committed
	}, 5*time.Second, time.Millisecond)
	cancel()
	require.Error(t, group.Wait())
	require.NoError(t, tx.Rollback())
	require.NoError(t, service.Stop())
	service = newTestSessionServiceAt(t, workspace)
	summaries, err := service.ListSessions(t.Context(), []string{"a-private", "b-empty", "c-bad"})
	require.NoError(t, err)
	require.Equal(t, []protocol.SessionSummary{{ConversationID: "a-private", LastUserMessage: "alpha\x00", LastUpdated: time.Unix(1, 0).UTC()}}, summaries)
	require.ErrorContains(t, service.backfillSessionSummaries(t.Context()), "parse session summary history")

	var after string
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT json_agg(e ORDER BY id)::text FROM session_entries e`).Scan(&after))
	require.Equal(t, before, after, "interruption and restart must not mutate replay rows")
	summaries, err = service.ListSessions(t.Context(), []string{"a-private", "b-empty", "c-bad"})
	require.NoError(t, err)
	require.Equal(t, []protocol.SessionSummary{{ConversationID: "a-private", LastUserMessage: "alpha\x00", LastUpdated: time.Unix(1, 0).UTC()}, {ConversationID: "b-empty"}}, summaries)
	complete, err := service.sessionSummariesComplete(t.Context())
	require.NoError(t, err)
	require.False(t, complete)
	_, err = service.db.ExecContext(t.Context(), `UPDATE session_entries SET entry_json = '{}' WHERE conversation_id = 'c-bad'`)
	require.NoError(t, err)
	// The final invalid timestamp must leave the last valid timestamp in ID order.
	insertSessionEntryWithoutSummary(t, service, "c-bad", testSessionEntryAt(time.Unix(9, 123456789).UTC(), "nine"))
	insertSessionEntryWithoutSummary(t, service, "c-bad", testSessionEntryAt(time.Unix(4, 123456789).UTC(), "four"))
	_, err = service.db.ExecContext(t.Context(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('c-bad', '{}', 'invalid')`)
	require.NoError(t, err)
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT json_agg(e ORDER BY id)::text FROM session_entries e`).Scan(&before))
	require.NoError(t, service.Stop())
	service = newTestSessionServiceAt(t, workspace)
	require.NoError(t, service.backfillSessionSummaries(t.Context()))
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT json_agg(e ORDER BY id)::text FROM session_entries e`).Scan(&after))
	require.Equal(t, before, after, "resumed backfill must not mutate replay rows")
	complete, err = service.sessionSummariesComplete(t.Context())
	require.NoError(t, err)
	require.True(t, complete)
	summaries, err = service.ListSessions(t.Context(), []string{"c-bad"})
	require.NoError(t, err)
	require.Equal(t, []protocol.SessionSummary{{ConversationID: "c-bad", LastUserMessage: "four", LastUpdated: time.Unix(4, 123456000).UTC()}}, summaries)
	ids, err := managedConversationIDs(t.Context(), service.db)
	require.NoError(t, err)
	require.Empty(t, ids)
}

func TestSessionSummaryBackfillOverlapsMutation(t *testing.T) {
	for _, tc := range []struct {
		action string
		orphan bool
	}{
		{"append", true}, {"delete", true},
		{"upsert", false}, {"upsert", true},
		{"create", false}, {"create", true},
		{"external MCP", false}, {"external MCP", true},
	} {
		t.Run(fmt.Sprintf("%s/orphan=%t", tc.action, tc.orphan), func(t *testing.T) {
			service := newTestSessionService(t)

			id := t.Name()
			if tc.orphan {
				insertSessionEntryWithoutSummary(t, service, id, testSessionEntry("old", "answer"))
			}

			tx, err := service.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)

			defer func() { _ = tx.Rollback() }()

			require.NoError(t, lockSessionHistory(t.Context(), tx, id))

			var group errgroup.Group
			group.Go(func() error {
				if err := service.backfillSessionSummary(t.Context(), id); err != nil {
					return fmt.Errorf("backfill during mutation: %w", err)
				}

				return nil
			})
			group.Go(func() error {
				if tc.action == "delete" {
					_, err := service.DeleteSession(t.Context(), id)
					return err
				}

				_, err := service.AppendEntryID(t.Context(), id, testSessionEntry("new", "answer"))

				return err
			})

			wantWaiters := 2
			if tc.action == "delete" {
				wantWaiters++

				group.Go(func() error {
					_, err := service.AppendEntryID(t.Context(), id, testSessionEntryAt(time.Unix(3, 123456789).UTC(), "new 日本"))
					return err
				})
			}

			if tc.action != "append" && tc.action != "delete" {
				wantWaiters++

				group.Go(func() error {
					switch tc.action {
					case "upsert":
						return service.UpsertThread(id, ThreadState{Agent: "main"})
					case "create":
						runtime := &Runtime{Sessions: service}
						return runtime.CreateConversation(t.Context(), protocol.Conversation{ID: id, Agent: "main"})
					}

					return service.RegisterExternalMCPConversation("public", "main", &ExternalMCPSessionState{PrivateConversationID: "private", ManagedConversationID: id, Agent: "main"})
				})
			}

			require.Eventually(t, func() bool {
				var waiting int

				err := service.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND classid = 87901 AND objid = (hashtext($1)::bigint & 4294967295)::oid AND objsubid = 2 AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, id).Scan(&waiting)
				require.NoError(t, err)

				return waiting == wantWaiters
			}, 5*time.Second, time.Millisecond)
			require.NoError(t, tx.Commit())
			require.NoError(t, group.Wait())
			// A previously selected backfill candidate must also be harmless after mutation.
			require.NoError(t, service.backfillSessionSummary(t.Context(), id))
			summaries, err := service.ListSessions(t.Context(), []string{id})
			require.NoError(t, err)

			want := protocol.SessionSummary{ConversationID: id}
			entries, err := service.ObserveEntries(t.Context(), id)
			require.NoError(t, err)

			if tc.action == "delete" {
				require.LessOrEqual(t, len(entries), 1, "delete removes the old entry regardless of append order")
			} else {
				require.NotEmpty(t, entries)
			}

			if len(entries) > 0 {
				last := entries[len(entries)-1].Entry
				messages, err := replayInputMessages(last.ReplayInput)
				require.NoError(t, err)

				want.LastUserMessage, want.LastUpdated = messages[0].text, last.Timestamp.Truncate(time.Microsecond)
				if tc.action == "delete" {
					require.Equal(t, "new 日本", want.LastUserMessage)
				} else {
					require.Equal(t, "new", want.LastUserMessage)
				}
			}

			require.Equal(t, []protocol.SessionSummary{want}, summaries)
		})
	}
}

func TestSessionSummaryPairedAppendAndCleanup(t *testing.T) {
	service := newTestSessionService(t)
	session := &ExternalMCPSessionState{PrivateConversationID: "private", ManagedConversationID: "managed", Agent: "main"}
	require.NoError(t, service.RegisterExternalMCPConversation("public", "main", session))

	prefix, err := replayInputForMessage("user", "managed-only")
	require.NoError(t, err)
	_, err = service.appendExternalMCPEntry(t.Context(), "private", "managed", &harness.SessionEntry{Timestamp: time.Unix(1, 0).UTC()}, prefix)
	require.NoError(t, err)
	summaries, err := service.ListSessions(t.Context(), []string{"private", "managed"})
	require.NoError(t, err)
	require.Equal(t, []protocol.SessionSummary{{ConversationID: "managed", LastUserMessage: "managed-only", LastUpdated: time.Unix(1, 0).UTC()}, {ConversationID: "private", LastUpdated: time.Unix(1, 0).UTC()}}, summaries)

	var group errgroup.Group
	for i := range 25 {
		group.Go(func() error {
			_, err := service.appendExternalMCPEntry(t.Context(), "private", "managed", testSessionEntryAt(time.Unix(int64(25-i), 987654321).UTC(), fmt.Sprintf("paired %d 日本 %s", i, strings.Repeat("full preview ", 100))), prefix)
			return err
		})
	}

	require.NoError(t, group.Wait())
	summaries, err = service.ListSessions(t.Context(), []string{"private", "managed"})
	require.NoError(t, err)
	require.Len(t, summaries, 2)

	for _, summary := range summaries {
		entries, err := service.ObserveEntries(t.Context(), summary.ConversationID)
		require.NoError(t, err)
		require.Len(t, entries, 26)
		last := entries[len(entries)-1].Entry
		messages, err := replayInputMessages(last.ReplayInput)
		require.NoError(t, err)

		var preview string

		for _, message := range messages {
			if message.role == "user" {
				preview = message.text
			}
		}

		require.Equal(t, protocol.SessionSummary{ConversationID: summary.ConversationID, LastUserMessage: preview, LastUpdated: last.Timestamp.Truncate(time.Microsecond)}, summary)
	}
	// Fail the second write after the first history and summary have been written.
	_, err = service.db.ExecContext(t.Context(), `ALTER TABLE session_entries ADD CONSTRAINT reject_managed CHECK (conversation_id <> 'managed') NOT VALID`)
	require.NoError(t, err)
	_, err = service.appendExternalMCPEntry(t.Context(), "private", "managed", testSessionEntry("rolled back", "answer"), nil)
	require.Error(t, err)
	after, err := service.ListSessions(t.Context(), []string{"private", "managed"})
	require.NoError(t, err)
	require.Equal(t, summaries, after)
	entries, err := service.ObserveEntries(t.Context(), "private")
	require.NoError(t, err)
	require.Len(t, entries, 26)
	require.NoError(t, service.RemoveExternalMCPConversation("public"))
	require.NoError(t, service.backfillSessionSummary(t.Context(), "private"))
	after, err = service.ListSessions(t.Context(), []string{"private", "managed"})
	require.NoError(t, err)
	require.Empty(t, after)
}

func TestConversationSummaryInitializationRollsBackOnFailure(t *testing.T) {
	for _, creation := range []string{"upsert", "create", "external MCP"} {
		for _, failure := range []struct{ name, message string }{
			{"orphan decode", "parse session summary history"},
			{"summary write", "write session summary"},
			{"database unavailable", "database is closed"},
		} {
			t.Run(creation+"/"+failure.name, func(t *testing.T) {
				workspace := t.TempDir()
				service := newTestSessionServiceAt(t, workspace)
				insertSessionEntryWithoutSummary(t, service, "history", testSessionEntry("orphan preview", "answer"))

				inspection, err := sql.Open("pgx", testStoreDSN(workspace))

				require.NoError(t, err)
				defer func() { require.NoError(t, inspection.Close()) }()

				switch failure.name {
				case "orphan decode":
					_, err = inspection.ExecContext(t.Context(), `UPDATE session_entries SET entry_json = '{' WHERE conversation_id = 'history'`)
					require.NoError(t, err)
				case "summary write":
					// A real database write rejection must roll back the earlier record/binding inserts.
					_, err = inspection.ExecContext(t.Context(), `ALTER TABLE session_summaries ADD CONSTRAINT reject_summary CHECK (conversation_id <> 'history')`)
					require.NoError(t, err)
				case "database unavailable":
					require.NoError(t, service.Stop())
				}

				var before string
				require.NoError(t, inspection.QueryRowContext(t.Context(), `SELECT entry_json FROM session_entries WHERE conversation_id = 'history'`).Scan(&before))

				switch creation {
				case "upsert":
					err = service.UpsertThread("history", ThreadState{Agent: "main"})
				case "create":
					runtime := &Runtime{Sessions: service}
					err = runtime.CreateConversation(t.Context(), protocol.Conversation{ID: "history", Agent: "main"})
				case "external MCP":
					err = service.RegisterExternalMCPConversation("public", "main", &ExternalMCPSessionState{PrivateConversationID: "private", ManagedConversationID: "history", Agent: "main"})
				}

				require.ErrorContains(t, err, failure.message)

				var recorded, summarized, bound bool
				require.NoError(t, inspection.QueryRowContext(t.Context(), `SELECT
EXISTS (SELECT 1 FROM managed_conversations WHERE conversation_id = 'history'),
EXISTS (SELECT 1 FROM session_summaries WHERE conversation_id = 'history'),
EXISTS (SELECT 1 FROM external_mcp_sessions WHERE external_conversation_id = 'public')`).Scan(&recorded, &summarized, &bound))
				require.False(t, recorded)
				require.False(t, summarized)
				require.False(t, bound)

				var after string
				require.NoError(t, inspection.QueryRowContext(t.Context(), `SELECT entry_json FROM session_entries WHERE conversation_id = 'history'`).Scan(&after))
				require.Equal(t, before, after, "failed initialization must preserve the orphan history")
			})
		}
	}
}

func TestSidebarSessionsNeverReadHistoryForInitializedConversations(t *testing.T) {
	for _, creation := range []string{"backfill", "upsert", "create", "external MCP"} {
		t.Run(creation, func(t *testing.T) {
			workspace := t.TempDir()
			service := newTestSessionServiceAt(t, workspace)
			insertSessionEntryWithoutSummary(t, service, "history", testSessionEntryAt(time.Unix(2, 123456789).UTC(), "orphan preview"))

			for _, id := range []string{"empty", "history"} {
				switch creation {
				case "backfill":
					// A managed row from before summary initialization was introduced.
					_, err := service.db.ExecContext(t.Context(), `INSERT INTO managed_conversations (conversation_id, agent, created_by) VALUES ($1, 'main', '')`, id)
					require.NoError(t, err)
				case "upsert":
					require.NoError(t, service.UpsertThread(id, ThreadState{Agent: "main"}))
				case "create":
					runtime := &Runtime{Sessions: service}
					require.NoError(t, runtime.CreateConversation(t.Context(), protocol.Conversation{ID: id, Agent: "main"}))
				case "external MCP":
					require.NoError(t, service.RegisterExternalMCPConversation("public-"+id, "main", &ExternalMCPSessionState{PrivateConversationID: "private-" + id, ManagedConversationID: id, Agent: "main"}))
				}
			}

			if creation == "backfill" {
				complete, err := service.sessionSummariesComplete(t.Context())
				require.NoError(t, err)
				require.False(t, complete)
				require.NoError(t, service.backfillSessionSummaries(t.Context()))
				complete, err = service.sessionSummariesComplete(t.Context())
				require.NoError(t, err)
				require.True(t, complete)
			}

			// Pin the listing pool's session setting; the lock uses a separate connection.
			service.db.SetMaxOpenConns(1)
			_, err := service.db.ExecContext(t.Context(), `SET lock_timeout = '10ms'`)
			require.NoError(t, err)
			blocker, err := sql.Open("pgx", testStoreDSN(workspace))

			require.NoError(t, err)
			defer func() { require.NoError(t, blocker.Close()) }()

			transaction, err := blocker.BeginTx(t.Context(), nil)

			require.NoError(t, err)
			defer func() { require.NoError(t, transaction.Rollback()) }()

			_, err = transaction.ExecContext(t.Context(), `LOCK TABLE session_entries IN ACCESS EXCLUSIVE MODE`)
			require.NoError(t, err)

			var rows []SidebarSession

			for row, err := range service.SidebarSessions(t.Context()) {
				require.NoError(t, err, "sidebar enumeration must not access replay history")

				rows = append(rows, row)
			}

			require.Equal(t, []SidebarSession{
				{Conversation: protocol.Conversation{ID: "history", Agent: "main"}, Summary: &protocol.SessionSummary{ConversationID: "history", LastUserMessage: "orphan preview", LastUpdated: time.Unix(2, 123456000).UTC()}},
				{Conversation: protocol.Conversation{ID: "empty", Agent: "main"}, Summary: &protocol.SessionSummary{ConversationID: "empty"}},
			}, rows)
		})
	}
}

func TestSidebarSessionsOrderMembershipAndCompleteness(t *testing.T) {
	service := newTestSessionService(t)
	for _, id := range []string{"a", "Z", "ä", "empty", "missing", "zero", "cron:private", "one-off-cron:private", "private"} {
		require.NoError(t, service.UpsertThread(id, ThreadState{Agent: "main", Settled: id == "Z"}))
	}

	require.NoError(t, service.UpsertExternalMCPSession("external", &ExternalMCPSessionState{PrivateConversationID: "private", ManagedConversationID: "not-recorded"}))

	stamp := time.Unix(2, 123456789).UTC()
	for _, id := range []string{"a", "Z", "ä", "cron:private", "one-off-cron:private", "private", "history-only"} {
		_, err := service.AppendEntryID(t.Context(), id, testSessionEntryAt(stamp, id+"\x00full"))
		require.NoError(t, err)
	}

	_, err := service.AppendEntryID(t.Context(), "zero", &harness.SessionEntry{})
	require.NoError(t, err)
	insertSessionEntryWithoutSummary(t, service, "missing", testSessionEntry("old", "answer"))
	_, err = service.db.ExecContext(t.Context(), `DELETE FROM session_summaries WHERE conversation_id = 'missing'`)
	require.NoError(t, err)
	// Enumeration must not parse replay even for missing summaries.
	_, err = service.db.ExecContext(t.Context(), `UPDATE session_entries SET entry_json = '{'`)
	require.NoError(t, err)

	var got []SidebarSession

	for row, err := range service.SidebarSessions(t.Context()) {
		require.NoError(t, err)

		got = append(got, row)
	}

	want := make([]SidebarSession, 0, 6)

	for _, id := range []string{"Z", "a", "ä", "empty", "missing", "zero"} {
		row := SidebarSession{Conversation: protocol.Conversation{ID: id, Agent: "main", Settled: id == "Z"}}
		if id == "zero" || id == "empty" {
			row.Summary = &protocol.SessionSummary{ConversationID: id}
		} else if id != "empty" && id != "missing" {
			row.Summary = &protocol.SessionSummary{ConversationID: id, LastUpdated: stamp.Truncate(time.Microsecond), LastUserMessage: id + "\x00full"}
		}

		want = append(want, row)
	}

	require.Equal(t, want, got)
	_, err = service.DeleteSession(t.Context(), "a")
	require.NoError(t, err)

	var remaining []string

	for row, err := range service.SidebarSessions(t.Context()) {
		require.NoError(t, err)

		remaining = append(remaining, row.Conversation.ID)
		if row.Conversation.ID == "a" {
			require.Equal(t, &protocol.SessionSummary{ConversationID: "a"}, row.Summary)
		}
	}

	require.Equal(t, []string{"Z", "ä", "a", "empty", "missing", "zero"}, remaining)

	// PostgreSQL accepts infinity timestamps, but Go time.Time cannot represent them.
	// A bad stored row after a valid prefix must stop enumeration with a scan error.
	_, err = service.db.ExecContext(t.Context(), `UPDATE session_summaries SET last_updated = '-infinity' WHERE conversation_id = 'zero'`)
	require.NoError(t, err)

	next, stop := iter.Pull2(service.SidebarSessions(t.Context()))
	defer stop()

	for _, id := range remaining[:len(remaining)-1] {
		row, err, ok := next()
		require.True(t, ok)
		require.NoError(t, err)
		require.Equal(t, id, row.Conversation.ID)
	}

	_, err, ok := next()
	require.True(t, ok)
	require.ErrorContains(t, err, "scan sidebar session")

	_, _, ok = next()
	require.False(t, ok)
	require.Zero(t, service.db.Stats().InUse)
}

func TestSidebarSessionsCloseRowsOnEarlyStopAndCancellation(t *testing.T) {
	service := newTestSessionService(t)
	for _, id := range []string{"a", "b"} {
		require.NoError(t, service.UpsertThread(id, ThreadState{Agent: "main"}))
		_, err := service.AppendEntryID(t.Context(), id, testSessionEntry(strings.Repeat(id, 16384), ""))
		require.NoError(t, err)
	}

	before := service.db.Stats().InUse

	next, stop := iter.Pull2(service.SidebarSessions(t.Context()))
	defer stop()

	row, err, ok := next()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "a", row.Conversation.ID)
	require.Equal(t, before+1, service.db.Stats().InUse, "the first row arrives while the database cursor is still open")
	stop()
	require.Equal(t, before, service.db.Stats().InUse)
	ctx, cancel := context.WithCancel(t.Context())

	next, stop = iter.Pull2(service.SidebarSessions(ctx))
	defer stop()

	_, err, ok = next()
	require.NoError(t, err)
	require.True(t, ok)
	cancel()

	for {
		_, err, ok = next()
		if err != nil {
			require.ErrorIs(t, err, context.Canceled)
			break
		}

		if !ok {
			t.Fatal("cancelled enumeration completed successfully")
		}
	}

	stop()
	require.Equal(t, before, service.db.Stats().InUse)

	// Cancellation before the first read must also report failure, not an empty list.
	next, stop = iter.Pull2(service.SidebarSessions(ctx))
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
	workspace := t.TempDir()

	service := newTestSessionServiceAt(t, workspace)
	for _, id := range []string{"a", "b"} {
		require.NoError(t, service.UpsertThread(id, ThreadState{Agent: "main"}))
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

	next, stop := iter.Pull2(service.SidebarSessions(ctx))

	defer func() {
		cancel()
		stop()
	}()

	row, err, ok := next()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "a", row.Conversation.ID)
	<-tail

	var reading errgroup.Group
	reading.Go(func() error {
		_, err, _ := next()
		return err
	})
	cancel()
	require.ErrorIs(t, reading.Wait(), context.Canceled)
	stop()
	require.NoError(t, proxy.Wait())
	require.Zero(t, service.db.Stats().InUse)
}

func insertSessionEntryWithoutSummary(t *testing.T, service *SessionService, conversationID string, entry *harness.SessionEntry) {
	t.Helper()

	data, err := json.Marshal(entry)
	require.NoError(t, err)
	_, err = service.db.ExecContext(t.Context(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ($1, $2, $3)`, conversationID, string(data), entry.Timestamp.UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
}
