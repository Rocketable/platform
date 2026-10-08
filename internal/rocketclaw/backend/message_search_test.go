package backend

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

type messageSearchTestRow struct {
	EntryID                int64
	ReplayIndex, PartIndex int
	Role, Text, TextLower  string
}

func messageSearchTestRows(t *testing.T, s *SessionService, conversationID string) []messageSearchTestRow {
	t.Helper()

	// Turn rows have no entry and read as entry 0.
	rows, err := queryRows(t.Context(), s.db, `SELECT COALESCE(entry_id, 0), replay_index, part_index, role, text, text_lower FROM message_search
WHERE conversation_id = $1 ORDER BY entry_id NULLS FIRST, replay_index, part_index`, "message search rows", func(row rowScanner) (messageSearchTestRow, error) {
		var r messageSearchTestRow
		if err := row.Scan(&r.EntryID, &r.ReplayIndex, &r.PartIndex, &r.Role, &r.Text, &r.TextLower); err != nil {
			return r, fmt.Errorf("scan message search row: %w", err)
		}

		return r, nil
	}, conversationID)
	require.NoError(t, err)

	return rows
}

func uploadTestAttachment(t *testing.T, s *SessionService, conversationID, id string) string {
	t.Helper()

	require.NoError(t, s.SaveAttachment(t.Context(), conversationID, &protocol.OutboundAttachment{ID: id, Name: "notes.txt", MIMEType: "text/plain", Data: []byte("notes")}, true))

	return fmt.Sprintf(`attachment:%s "notes.txt" (workspace path "artifacts/uploads/%s/notes.txt")`, id, id)
}

func testReplayEntry(items ...string) *harness.SessionEntry {
	entry := &harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC()}
	for _, item := range items {
		entry.ReplayInput = append(entry.ReplayInput, json.RawMessage(item))
	}

	return entry
}

func jsonString(text string) string {
	data, _ := json.Marshal(text) // Encoding a string cannot fail.
	return string(data)
}

func TestMessageSearchIndexesSavedEntries(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	reference := uploadTestAttachment(t, s, "web-chat", "file-1")

	id, err := s.AppendEntryID(ctx, "web-chat", testReplayEntry(
		`{"type":"message","role":"user","prompt_header":"[Web]","content":"[Web]\n\nHello ÀB"}`,
		`{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"First PART","annotations":[]},{"type":"output_text","text":"  ","annotations":[]},{"type":"output_text","text":"Third","annotations":[]}]}`,
		`{"type":"message","role":"user","content":`+jsonString("look attachment:nope\n\n"+reference)+`}`,
		`{"type":"function_call","call_id":"call","name":"Read","arguments":"{\"path\":\"secret\"}"}`,
		`{"type":"function_call_output","call_id":"call","output":"tool secret"}`,
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking secret"}]}`,
		`{"type":"message","role":"developer","content":"developer secret"}`,
		`{"type":"compaction","id":"cmp_1","encrypted_content":"sealed"}`,
		`{"type":"message","role":"assistant","content":"nul\u0000 Reply"}`,
		`{"type":"message","role":"assistant","content":"\u0000 "}`,
	))
	require.NoError(t, err)
	require.Equal(t, []messageSearchTestRow{
		{id, 0, 0, "user", "Hello ÀB", "hello àb"},
		{id, 1, 0, "assistant", "First PART", "first part"},
		{id, 1, 2, "assistant", "Third", "third"},
		{id, 2, 0, "user", "look attachment:nope", "look attachment:nope"},
		{id, 8, 0, "assistant", "nul Reply", "nul reply"},
	}, messageSearchTestRows(t, s, "web-chat"))

	_, err = s.DeleteSession(ctx, "web-chat")
	require.NoError(t, err)
	require.Empty(t, messageSearchTestRows(t, s, "web-chat"), "deleted entries cascade")

	// Like the summary projection, a save fails on output History cannot decode.
	_, err = s.AppendEntryID(ctx, "web-chat", testReplayEntry(`{"type":"message","id":"msg_2","role":"assistant","content":[{"type":"output_text","text":"ok"},{"type":"refusal","refusal":"no","text":7}]}`))
	require.ErrorContains(t, err, "decode")
	entries, err := s.ObserveEntries(ctx, "web-chat")
	require.NoError(t, err)
	require.Empty(t, entries)

	for _, conversationID := range []string{"cron:daily", "one-off-cron:once"} {
		_, err := s.AppendEntryID(ctx, conversationID, testSessionEntry("hidden prompt", "hidden answer"))
		require.NoError(t, err)
		require.Empty(t, messageSearchTestRows(t, s, conversationID), conversationID)
	}

	// A cron handoff's Web chat has a '/' from the job path, like a Delegation History.
	const handoff = "web:cron:cron/daily.md:1"
	require.NoError(t, s.UpsertThread(handoff, ThreadState{Agent: "main", CreatedBy: ThreadCreatedByCron}))
	handoffID, err := s.AppendEntryID(ctx, handoff, testSessionEntry("handoff prompt", "handoff answer"))
	require.NoError(t, err)
	_, err = s.AppendEntryID(ctx, handoff+"/call-1", testSessionEntry("handoff prompt", "delegated answer"))
	require.NoError(t, err)
	hits, _, err := s.SearchMessages(ctx, "handoff", nil)
	require.NoError(t, err)
	require.Equal(t, []MessageSearchHit{
		{ConversationID: handoff, Role: "user", Text: "handoff prompt", MessageID: fmt.Sprintf("%d:0", handoffID)},
		{ConversationID: handoff, Role: "assistant", Text: "handoff answer", MessageID: fmt.Sprintf("%d:1", handoffID)},
	}, hits, "the Delegation History stays out of search")
}

func TestMessageSearchIndexesForkedAttachments(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, (&Runtime{Sessions: s}).CreateConversation(ctx, protocol.Conversation{ID: "source", Agent: "main"}))
	reference := uploadTestAttachment(t, s, "source", "source-file")
	_, err := s.AppendEntryID(ctx, "source", testReplayEntry(`{"type":"message","role":"user","content":`+jsonString("read this\n\n"+reference)+`}`))
	require.NoError(t, err)

	_, err = s.ForkConversation(ctx, "source", protocol.Conversation{ID: "fork", Agent: "main"}, "")
	require.NoError(t, err)

	rows := messageSearchTestRows(t, s, "fork")
	require.Len(t, rows, 1)
	require.Equal(t, "read this", rows[0].Text, "the copied attachment is visible inside the fork transaction")
}

func TestMessageSearchIndexesExternalMCPManagedCopyOnce(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	reference := uploadTestAttachment(t, s, "private-x", "private-file")
	prefix := []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"Prefix prompt"}`)}

	_, err := s.appendExternalMCPEntry(ctx, "private-x", "managed-y", testReplayEntry(
		`{"type":"message","role":"user","content":`+jsonString("from mcp\n\n"+reference)+`}`,
		`{"type":"message","role":"assistant","content":"mcp answer"}`,
	), prefix)
	require.NoError(t, err)
	require.Empty(t, messageSearchTestRows(t, s, "private-x"))

	entries, err := s.ObserveEntries(ctx, "managed-y")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, []messageSearchTestRow{
		{entries[0].ID, 0, 0, "user", "Prefix prompt", "prefix prompt"},
		{entries[0].ID, 1, 0, "user", "from mcp", "from mcp"},
		{entries[0].ID, 2, 0, "assistant", "mcp answer", "mcp answer"},
	}, messageSearchTestRows(t, s, "managed-y"))
}

func TestMessageSearchIndexesSyncedCopies(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	manager, _ := newTestBridgeManager(t, config.NewLockedConfig(&config.Config{Workspace: t.TempDir()}), s, make(chan *protocol.OutboundMessage, 4))
	rt := &Runtime{Sessions: s, threads: manager}

	require.NoError(t, s.UpsertThread("cron:daily", ThreadState{Agent: "job", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, rt.CreateConversation(ctx, protocol.Conversation{ID: "web-dest", Agent: "main"}))

	reference := uploadTestAttachment(t, s, "cron:daily", "cron-file")
	_, err := s.AppendEntryID(ctx, "cron:daily", testReplayEntry(
		`{"type":"message","role":"user","content":`+jsonString("cron report\n\n"+reference)+`}`,
		`{"type":"message","role":"assistant","content":"cron answer"}`,
	))
	require.NoError(t, err)
	// History hides an entry whose source row is gone and whose producer was never recorded.
	_, err = s.db.ExecContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('cron:daily', '{"sync_source_entry_id":-1,"replay_input":[{"type":"message","role":"user","content":"orphan prompt"}]}', '')`)
	require.NoError(t, err)
	require.Empty(t, messageSearchTestRows(t, s, "cron:daily"))

	require.NoError(t, rt.SyncConversation(ctx, "cron:daily", "web-dest"))

	entries, err := s.ObserveEntries(ctx, "web-dest")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, []messageSearchTestRow{
		{entries[0].ID, 0, 0, "user", "cron report", "cron report"},
		{entries[0].ID, 1, 0, "assistant", "cron answer", "cron answer"},
	}, messageSearchTestRows(t, s, "web-dest"))

	require.NoError(t, rt.SyncConversation(ctx, "cron:daily", "web-dest"))
	require.Len(t, messageSearchTestRows(t, s, "web-dest"), 2, "a repeated Sync copies nothing new")
}

func TestMessageSearchFollowsCommittedRevert(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))

	id, err := s.AppendEntryID(ctx, "main", testReplayEntry(
		`{"type":"message","role":"user","content":"keep prompt"}`,
		`{"type":"message","role":"assistant","content":"keep answer"}`,
		`{"type":"message","role":"user","content":"drop prompt"}`,
		`{"type":"message","role":"assistant","content":"drop answer"}`,
	))
	require.NoError(t, err)
	_, err = s.AppendEntryID(ctx, "main", testSessionEntry("later prompt", "later answer"))
	require.NoError(t, err)

	_, _, err = stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:2", id))
	require.NoError(t, err)
	require.Len(t, messageSearchTestRows(t, s, "main"), 6, "a staged undo hides rows at query time")

	bridge := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	rt := &Runtime{Sessions: s, threads: newThreadBridgeManager(nil, s, bridge.log, func(Config) directBridge { return bridge })}
	require.NoError(t, rt.StashQueueItem(ctx, "main", &protocol.ThreadQueueItem{ID: "replacement", Source: protocol.SourceWeb, Principal: "alice", Kind: protocol.InboundKindHeld, Message: "edited"}))

	require.Equal(t, []messageSearchTestRow{
		{id, 0, 0, "user", "keep prompt", "keep prompt"},
		{id, 1, 0, "assistant", "keep answer", "keep answer"},
	}, messageSearchTestRows(t, s, "main"))
}

func TestMessageSearchIndexesEndedTurns(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	reference := uploadTestAttachment(t, s, "web-chat", "turn-file")
	turn := &testCheckpoint{TurnID: "turn-1", ConversationKey: "web-chat", ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":` + jsonString("stopped question\n\n"+reference) + `}`),
		json.RawMessage(`{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Partial answer","annotations":[]}]}`),
	}, OutputTrace: []json.RawMessage{json.RawMessage(`{"type":"response.output_text.delta","item_id":"msg_2","content_index":0,"delta":"progress only"}`)}}
	require.NoError(t, upsertTestTurn(ctx, s, turn))
	require.Empty(t, messageSearchTestRows(t, s, "web-chat"), "a running turn is not searchable yet")

	endTestTurn(t, s, "web-chat", "turn-1", protocol.TerminalStopped)

	transcript, err := s.ObserveTranscript(ctx, "web-chat", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, transcript, 1)
	require.Equal(t, protocol.TerminalStopped, transcript[0].Terminal)
	require.Equal(t, []messageSearchTestRow{
		{0, 0, 0, "user", "stopped question", "stopped question"},
		{0, 1, 0, "assistant", "Partial answer", "partial answer"},
	}, messageSearchTestRows(t, s, "web-chat"), "History shows the stopped turn's record, not its progress log")

	// A runner arriving after the stop rewrites the record before its own finish.
	turn.ReplayInput = append(turn.ReplayInput, json.RawMessage(`{"type":"message","role":"assistant","content":"Late answer"}`))
	require.NoError(t, upsertTestTurn(ctx, s, turn))
	_, err = s.finishTurn(ctx, "turn-1", &turnFinish{store: newSessionStore("web-chat", s), outbound: protocol.NewOutboundMessage("web-chat", "late")})
	require.NoError(t, err)
	require.Equal(t, []messageSearchTestRow{
		{0, 0, 0, "user", "stopped question", "stopped question"},
		{0, 1, 0, "assistant", "Partial answer", "partial answer"},
		{0, 2, 0, "assistant", "Late answer", "late answer"},
	}, messageSearchTestRows(t, s, "web-chat"))

	entry := testReplayEntry(`{"type":"message","role":"user","content":"saved question"}`)
	entry.TurnID = "turn-1"
	id, err := s.AppendEntryID(ctx, "web-chat", entry)
	require.NoError(t, err)
	require.Equal(t, []messageSearchTestRow{{id, 0, 0, "user", "saved question", "saved question"}}, messageSearchTestRows(t, s, "web-chat"), "the turn's saved entry hides it")

	require.NoError(t, upsertTestTurn(ctx, s, &testCheckpoint{TurnID: "turn-2", ConversationKey: "web-chat", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"failed question"}`)}}))
	endTestTurn(t, s, "web-chat", "turn-2", protocol.TerminalFailed)
	require.Equal(t, []messageSearchTestRow{
		{0, 0, 0, "user", "failed question", "failed question"},
		{id, 0, 0, "user", "saved question", "saved question"},
	}, messageSearchTestRows(t, s, "web-chat"))

	_, err = s.DeleteSession(ctx, "web-chat")
	require.NoError(t, err)
	require.Empty(t, messageSearchTestRows(t, s, "web-chat"), "turn rows cascade with their turn")
}

func TestMessageSearchSkipsUndecodableTurnRecords(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()

	var logs bytes.Buffer

	s.log = slog.New(slog.NewTextHandler(&logs, nil))

	for turnID, step := range map[string]string{
		"bad-item":   `{"record":{"replay_input":[{"type":"message","role":"user","content":"attachment:none"},{"type":"message","id":"msg_2","role":"assistant","content":[{"type":"output_text","text":"ok"},{"type":"refusal","refusal":"no","text":7}]}]}}`,
		"bad-record": `{"record":{"replay_input":7}}`,
	} {
		seedActiveTurn(t, s, "web-chat", turnID, nil)
		require.NoError(t, s.SaveTurnStep(ctx, "web-chat", turnID, json.RawMessage(step)))
		endTestTurn(t, s, "web-chat", turnID, protocol.TerminalFailed)
	}

	failed, err := queryStrings(ctx, s.db, `SELECT id FROM active_turns WHERE terminal = $1 ORDER BY id`, "failed turns", protocol.TerminalFailed)
	require.NoError(t, err)
	require.Equal(t, []string{"bad-item", "bad-record"}, failed)
	require.Empty(t, messageSearchTestRows(t, s, "web-chat"))
	require.Equal(t, 2, strings.Count(logs.String(), "\n"), logs.String())
}

func TestFinishTurnLocksHistoryBeforeTurnRow(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	seedActiveTurn(t, s, "web-chat", "turn-1", nil)

	tx, err := s.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	require.NoError(t, lockSessionHistory(ctx, tx, "web-chat"))

	var holder int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holder))

	var group errgroup.Group
	group.Go(func() error {
		_, err := s.finishTurn(ctx, "turn-1", &turnFinish{store: newSessionStore("web-chat", s), outbound: protocol.NewOutboundMessage("web-chat", ""), terminal: protocol.TerminalStopped})
		return err
	})
	require.Eventually(t, func() bool {
		var blocked bool
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, holder).Scan(&blocked))

		return blocked
	}, 5*time.Second, time.Millisecond)

	// Like commitRevertDB, the history lock holder still reaches the turn row.
	_, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout = '1s'; UPDATE active_turns SET updated_at_unix_ns = 0 WHERE id = 'turn-1'`)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.NoError(t, group.Wait())
}

// unindexMessageSearch returns the database to its state just after the migration:
// history saved, no index rows, no markers.
func unindexMessageSearch(t *testing.T, s *SessionService) {
	t.Helper()

	_, err := s.db.ExecContext(t.Context(), `TRUNCATE message_search, message_search_indexed`)
	require.NoError(t, err)
}

func messageSearchMarkers(t *testing.T, s *SessionService) []string {
	t.Helper()

	markers, err := queryStrings(t.Context(), s.db, `SELECT conversation_id FROM message_search_indexed ORDER BY conversation_id COLLATE "C"`, "message search markers")
	require.NoError(t, err)

	return markers
}

func TestMessageSearchBackfillIndexesVisibleChats(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()

	var logs bytes.Buffer

	s.log = slog.New(slog.NewTextHandler(&logs, nil))
	manager, _ := newTestBridgeManager(t, config.NewLockedConfig(&config.Config{Workspace: t.TempDir()}), s, make(chan *protocol.OutboundMessage, 4))
	rt := &Runtime{Sessions: s, threads: manager}

	require.NoError(t, s.UpsertThread("cron:daily", ThreadState{Agent: "job", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, s.UpsertThread("one-off-cron:once", ThreadState{Agent: "job", CreatedBy: ThreadCreatedByCron}))
	reference := uploadTestAttachment(t, s, "cron:daily", "cron-file")
	_, err := s.AppendEntryID(ctx, "cron:daily", testReplayEntry(`{"type":"message","role":"user","content":`+jsonString("cron report\n\n"+reference)+`}`))
	require.NoError(t, err)
	_, err = s.AppendEntryID(ctx, "one-off-cron:once", testSessionEntry("once prompt", "once answer"))
	require.NoError(t, err)

	require.NoError(t, rt.CreateConversation(ctx, protocol.Conversation{ID: "saved", Agent: "main"}))
	require.NoError(t, rt.SyncConversation(ctx, "cron:daily", "saved"))
	_, err = s.AppendEntryID(ctx, "saved", testSessionEntry("old question", "old answer"))
	require.NoError(t, err)
	seedActiveTurn(t, s, "saved", "stopped-turn", testReplayEntry(`{"type":"message","role":"user","content":"stopped question"}`))
	endTestTurn(t, s, "saved", "stopped-turn", protocol.TerminalStopped)
	_, err = s.AppendEntryID(ctx, "saved/call-1", testSessionEntry("delegated prompt", "delegated answer"))
	require.NoError(t, err)

	require.NoError(t, s.UpsertThread("quiet", ThreadState{Agent: "main"}))
	_, err = s.AppendEntryID(ctx, "quiet", testReplayEntry(`{"type":"function_call","call_id":"call","name":"Read","arguments":"{}"}`, `{"type":"message","role":"assistant","content":" "}`))
	require.NoError(t, err)

	require.NoError(t, s.UpsertThread("damaged", ThreadState{Agent: "main"}))
	_, err = s.AppendEntryID(ctx, "damaged", testSessionEntry("intact question", "intact answer"))
	require.NoError(t, err)

	require.NoError(t, s.RegisterExternalMCPConversation("public", "main", &ExternalMCPSessionState{PrivateConversationID: "private", ManagedConversationID: "managed", Agent: "main"}))
	require.NoError(t, s.UpsertThread("private", ThreadState{Agent: "main"}))
	_, err = s.appendExternalMCPEntry(ctx, "private", "managed", testSessionEntry("mcp question", "mcp answer"), nil)
	require.NoError(t, err)

	want := map[string][]messageSearchTestRow{}
	for _, id := range []string{"saved", "quiet", "damaged", "managed"} {
		want[id] = messageSearchTestRows(t, s, id)
	}

	require.Len(t, want["saved"], 4, "the stopped turn, the synced report, and the saved entry")
	require.Equal(t, "cron report", want["saved"][1].Text, "a synced entry's attachments belong to its producer")
	require.Empty(t, want["quiet"])
	unindexMessageSearch(t, s)

	damaged := make([]int64, 2)
	for i, entry := range []string{
		`{"replay_input":[{"type":"message","id":"msg_2","role":"assistant","content":[{"type":"output_text","text":"lost"},{"type":"refusal","refusal":"no","text":7}]}]}`,
		`{"replay_input":7}`,
	} {
		require.NoError(t, s.db.QueryRowContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('damaged', $1, '') RETURNING id`, entry).Scan(&damaged[i]))
	}

	logs.Reset()
	require.NoError(t, s.backfillMessageSearch(ctx))

	for id, rows := range want {
		require.Equal(t, rows, messageSearchTestRows(t, s, id), id)
	}

	for _, id := range []string{"cron:daily", "one-off-cron:once", "private", "saved/call-1"} {
		require.Empty(t, messageSearchTestRows(t, s, id), id)
	}

	require.Equal(t, []string{"damaged", "managed", "quiet", "saved"}, messageSearchMarkers(t, s), "hidden chats and Delegation Histories are never discovered")
	require.Equal(t, 2, strings.Count(logs.String(), "\n"), logs.String())

	for _, id := range damaged {
		require.Contains(t, logs.String(), fmt.Sprintf("conversation_id=damaged entry_id=%d", id))
	}

	require.NoError(t, s.backfillMessageSearch(ctx))
	require.Equal(t, want["saved"], messageSearchTestRows(t, s, "saved"), "a marked chat is not indexed again")
	require.Equal(t, 2, strings.Count(logs.String(), "\n"), logs.String())
}

func TestMessageSearchBackfillResumesAfterCancel(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	want := map[string][]messageSearchTestRow{}

	for _, id := range []string{"resume-a", "resume-b"} {
		require.NoError(t, s.UpsertThread(id, ThreadState{Agent: "main"}))
		_, err := s.AppendEntryID(ctx, id, testSessionEntry(id+" question", id+" answer"))
		require.NoError(t, err)
		want[id] = messageSearchTestRows(t, s, id)
	}

	unindexMessageSearch(t, s)

	tx, err := s.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	require.NoError(t, lockSessionHistory(ctx, tx, "resume-b"))

	var holder int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holder))

	backfillCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var group errgroup.Group
	group.Go(func() error { return s.backfillMessageSearch(backfillCtx) })
	// Cancel only once the backfill, past resume-a, waits on resume-b's history lock.
	require.Eventually(t, func() bool {
		var blocked bool
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, holder).Scan(&blocked))

		return blocked
	}, 5*time.Second, time.Millisecond)
	cancel()
	require.Error(t, group.Wait())
	require.NoError(t, tx.Rollback())
	require.Equal(t, []string{"resume-a"}, messageSearchMarkers(t, s))
	require.Empty(t, messageSearchTestRows(t, s, "resume-b"))

	require.NoError(t, s.backfillMessageSearch(ctx))
	require.Equal(t, []string{"resume-a", "resume-b"}, messageSearchMarkers(t, s))

	for id, rows := range want {
		require.Equal(t, rows, messageSearchTestRows(t, s, id), id)
	}
}

func TestMessageSearchBackfillOverlapsMutation(t *testing.T) {
	for _, action := range []string{"append", "undo", "delete", "stop"} {
		t.Run(action, func(t *testing.T) {
			s := newTestSessionService(t)
			ctx := t.Context()
			id, turnID := "overlap-"+action, "overlap-turn-"+action
			require.NoError(t, s.UpsertThread(id, ThreadState{Agent: "main"}))
			first, err := s.AppendEntryID(ctx, id, testReplayEntry(
				`{"type":"message","role":"user","content":"keep prompt"}`,
				`{"type":"message","role":"assistant","content":"keep answer"}`,
				`{"type":"message","role":"user","content":"drop prompt"}`,
				`{"type":"message","role":"assistant","content":"drop answer"}`,
			))
			require.NoError(t, err)
			_, err = s.AppendEntryID(ctx, id, testSessionEntry("later question", "later answer"))
			require.NoError(t, err)
			seedActiveTurn(t, s, id, turnID, testReplayEntry(`{"type":"message","role":"user","content":"stopped question"}`))

			marker, _, err := stageRevertDB(ctx, s.db, id, fmt.Sprintf("%d:2", first))
			require.NoError(t, err)

			if action != "undo" {
				_, err = s.db.ExecContext(ctx, `UPDATE managed_conversations SET revert_message_id = '' WHERE conversation_id = $1`, id)
				require.NoError(t, err)
			}

			unindexMessageSearch(t, s)

			tx, err := s.db.BeginTx(ctx, nil)
			require.NoError(t, err)

			defer func() { _ = tx.Rollback() }()

			require.NoError(t, lockSessionHistory(ctx, tx, id))

			var group errgroup.Group
			group.Go(func() error { return s.backfillMessageSearch(ctx) })
			group.Go(func() error {
				switch action {
				case "append":
					_, err := s.AppendEntryID(ctx, id, testSessionEntry("new question", "new answer"))
					return err
				case "undo":
					tx, err := s.db.BeginTx(ctx, nil)
					if err != nil {
						return fmt.Errorf("begin undo commit: %w", err)
					}

					defer func() { _ = tx.Rollback() }()

					if err := lockSessionHistory(ctx, tx, id); err != nil {
						return err
					}

					if err := commitRevertDB(ctx, tx, id, marker); err != nil {
						return err
					}

					return tx.Commit()
				case "delete":
					_, err := s.DeleteSession(ctx, id)
					return err
				}

				_, err := s.finishTurn(ctx, turnID, &turnFinish{store: newSessionStore(id, s), outbound: protocol.NewOutboundMessage(id, ""), terminal: protocol.TerminalStopped})

				return err
			})
			require.Eventually(t, func() bool {
				var waiting int
				require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND classid = 87901 AND objid = (hashtext($1)::bigint & 4294967295)::oid AND objsubid = 2 AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, id).Scan(&waiting))

				return waiting == 2
			}, 5*time.Second, time.Millisecond)
			require.NoError(t, tx.Commit())
			require.NoError(t, group.Wait())
			require.Equal(t, []string{id}, messageSearchMarkers(t, s))

			rows := messageSearchTestRows(t, s, id)

			switch action {
			case "append":
				require.Len(t, rows, 8)
			case "undo":
				require.Equal(t, []messageSearchTestRow{{first, 0, 0, "user", "keep prompt", "keep prompt"}, {first, 1, 0, "assistant", "keep answer", "keep answer"}}, rows)
			case "delete":
				require.Empty(t, rows)
			case "stop":
				require.Len(t, rows, 7)
			}

			// A fresh projection of the chat matches what the overlap left behind.
			unindexMessageSearch(t, s)
			require.NoError(t, s.backfillMessageSearch(ctx))
			require.Equal(t, rows, messageSearchTestRows(t, s, id))
		})
	}
}

func TestMessageSearchMarksOnlyNewEmptyChats(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	rt := &Runtime{Sessions: s}

	for _, tc := range []struct {
		name   string
		create func(id string) error
	}{
		{"create", func(id string) error { return rt.CreateConversation(ctx, protocol.Conversation{ID: id, Agent: "main"}) }},
		{"upsert", func(id string) error { return s.UpsertThread(id, ThreadState{Agent: "main"}) }},
		{"external", func(id string) error {
			return s.RegisterExternalMCPConversation("public-"+id, "main", &ExternalMCPSessionState{PrivateConversationID: "private-" + id, ManagedConversationID: id, Agent: "main"})
		}},
	} {
		require.NoError(t, tc.create(tc.name+"-new"))
		// History saved by an older binary before the chat's managed row.
		insertSessionEntryWithoutSummary(t, s, tc.name+"-orphan", testSessionEntry("old question", "old answer"))
		require.NoError(t, tc.create(tc.name+"-orphan"))

		if tc.name != "external" {
			_, err := s.db.ExecContext(ctx, `INSERT INTO managed_conversations (conversation_id, agent, created_by) VALUES ($1, 'main', '')`, tc.name+"-existing")
			require.NoError(t, err)
			require.NoError(t, tc.create(tc.name+"-existing"))
			require.NoError(t, tc.create(tc.name+"-new"))
		}
	}

	_, err := s.AppendEntryID(ctx, "create-new", testSessionEntry("source question", "source answer"))
	require.NoError(t, err)
	_, err = s.ForkConversation(ctx, "create-new", protocol.Conversation{ID: "fork", Agent: "main"}, "")
	require.NoError(t, err)
	require.Len(t, messageSearchTestRows(t, s, "fork"), 2)

	// Committing an undo on a chat the backfill has not reached leaves it unmarked.
	id, err := s.AppendEntryID(ctx, "upsert-existing", testSessionEntry("undo question", "undo answer"))
	require.NoError(t, err)
	marker, _, err := stageRevertDB(ctx, s.db, "upsert-existing", fmt.Sprintf("%d:0", id))
	require.NoError(t, err)

	tx, err := s.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	require.NoError(t, lockSessionHistory(ctx, tx, "upsert-existing"))
	require.NoError(t, commitRevertDB(ctx, tx, "upsert-existing", marker))
	require.NoError(t, tx.Commit())

	require.Equal(t, []string{"create-new", "external-new", "fork", "upsert-new"}, messageSearchMarkers(t, s))
}

func searchTestTexts(t *testing.T, s *SessionService, needle string, prefixes ...string) []string {
	t.Helper()

	hits, _, err := s.SearchMessages(t.Context(), needle, prefixes)
	require.NoError(t, err)

	texts := make([]string, len(hits))
	for i, hit := range hits {
		texts[i] = hit.Text
	}

	return texts
}

func TestSearchMessagesMatchesLowercasedSubstrings(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("web-chat", ThreadState{Agent: "main"}))

	texts := []string{"Alan said hi", "alanine levels", "100% done", "1000 done", "snake_case", "snakeXcase", `C:\path`, "C:/path",
		"ping <@U123>", "ping <!subteam^S9>", "\u212Aelvin", "KELVIN", "İstanbul", "Istanbul"}
	items := make([]string, len(texts))

	for i, text := range texts {
		items[i] = `{"type":"message","role":"user","content":` + jsonString(text) + `}`
	}

	id, err := s.AppendEntryID(ctx, "web-chat", testReplayEntry(items...))
	require.NoError(t, err)

	hits, complete, err := s.SearchMessages(ctx, "alan", nil)
	require.NoError(t, err)
	require.True(t, complete)
	require.Equal(t, []MessageSearchHit{
		{ConversationID: "web-chat", Role: "user", Text: "Alan said hi", MessageID: fmt.Sprintf("%d:0", id)},
		{ConversationID: "web-chat", Role: "user", Text: "alanine levels", MessageID: fmt.Sprintf("%d:1", id)},
	}, hits, "substring, not whole-word, matches")

	for _, tc := range []struct {
		needle   string
		prefixes []string
		want     []string
	}{
		{"0%", nil, []string{"100% done"}},
		{"e_c", nil, []string{"snake_case"}},
		{`:\p`, nil, []string{`C:\path`}},
		{"zzz", []string{"<@u123"}, []string{"ping <@U123>"}},
		{"zzz", []string{"<@u123", "<!subteam^s9"}, []string{"ping <@U123>", "ping <!subteam^S9>"}},
		{strings.ToLower("\u212A"), nil, []string{"snake_case", "snakeXcase", "\u212Aelvin", "KELVIN"}},
		{strings.ToLower("\u212AELVIN"), nil, []string{"\u212Aelvin", "KELVIN"}},
		{strings.ToLower("İST"), nil, []string{"İstanbul", "Istanbul"}},
	} {
		require.Equal(t, tc.want, searchTestTexts(t, s, tc.needle, tc.prefixes...), "%q %q", tc.needle, tc.prefixes)
	}
}

func TestSearchMessagesShowsWhatHistoryShows(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	// Hidden chats an older binary left unmarked never make search incomplete.
	_, err := s.db.ExecContext(ctx, `INSERT INTO managed_conversations (conversation_id, agent, created_by) VALUES ('cron:old', 'job', ''), ('one-off-cron:old', 'job', '')`)
	require.NoError(t, err)
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))

	first, err := s.AppendEntryID(ctx, "main", testReplayEntry(
		`{"type":"message","role":"user","content":"needle 1"}`,
		`{"type":"message","role":"assistant","content":"needle 2"}`,
		`{"type":"message","role":"user","content":"needle 3"}`,
		`{"type":"message","role":"assistant","content":"needle 4"}`,
	))
	require.NoError(t, err)
	second, err := s.AppendEntryID(ctx, "main", testSessionEntry("needle 5", "needle 6"))
	require.NoError(t, err)
	seedActiveTurn(t, s, "main", "stopped", testReplayEntry(`{"type":"message","role":"user","content":"needle 7"}`))
	endTestTurn(t, s, "main", "stopped", protocol.TerminalStopped)
	third, err := s.AppendEntryID(ctx, "main", testSessionEntry("needle 8", "needle 9"))
	require.NoError(t, err)

	require.NoError(t, s.UpsertThread("cron:daily", ThreadState{Agent: "job", CreatedBy: ThreadCreatedByCron}))
	cron, err := s.AppendEntryID(ctx, "cron:daily", testSessionEntry("needle cron", "answer"))
	require.NoError(t, err)
	// No write path indexes a cron run; this row proves the query hides it anyway.
	_, err = s.db.ExecContext(ctx, `INSERT INTO message_search (conversation_id, entry_id, replay_index, part_index, role, text, text_lower) VALUES ('cron:daily', $1, 0, 0, 'user', 'needle cron', 'needle cron')`, cron)
	require.NoError(t, err)
	require.NoError(t, s.RegisterExternalMCPConversation("public", "main", &ExternalMCPSessionState{PrivateConversationID: "private", ManagedConversationID: "managed", Agent: "main"}))
	require.NoError(t, s.UpsertThread("private", ThreadState{Agent: "main"}))
	_, err = s.AppendEntryID(ctx, "private", testSessionEntry("needle private", "answer"))
	require.NoError(t, err)
	require.NotEmpty(t, messageSearchTestRows(t, s, "private"))

	all := []MessageSearchHit{
		{"main", "user", "needle 1", fmt.Sprintf("%d:0", first)},
		{"main", "assistant", "needle 2", fmt.Sprintf("%d:1", first)},
		{"main", "user", "needle 3", fmt.Sprintf("%d:2", first)},
		{"main", "assistant", "needle 4", fmt.Sprintf("%d:3", first)},
		{"main", "user", "needle 5", fmt.Sprintf("%d:0", second)},
		{"main", "assistant", "needle 6", fmt.Sprintf("%d:1", second)},
		{"main", "user", "needle 7", ""},
		{"main", "user", "needle 8", fmt.Sprintf("%d:0", third)},
		{"main", "assistant", "needle 9", fmt.Sprintf("%d:1", third)},
	}
	hits, complete, err := s.SearchMessages(ctx, "needle", nil)
	require.NoError(t, err)
	require.True(t, complete)
	require.Equal(t, all, hits, "the stopped turn sits after its anchor entry")

	_, _, err = stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:0", third))
	require.NoError(t, err)
	hits, _, err = s.SearchMessages(ctx, "needle", nil)
	require.NoError(t, err)
	require.Equal(t, all[:7], hits, "an undo at a turn's first message hides the whole entry")

	_, err = s.db.ExecContext(ctx, `UPDATE managed_conversations SET revert_message_id = '' WHERE conversation_id = 'main'`)
	require.NoError(t, err)
	hits, _, err = s.SearchMessages(ctx, "needle", nil)
	require.NoError(t, err)
	require.Equal(t, all, hits, "Redo returns the messages")

	_, _, err = stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:2", first))
	require.NoError(t, err)
	hits, _, err = s.SearchMessages(ctx, "needle", nil)
	require.NoError(t, err)
	require.Equal(t, all[:2], hits, "the undo hides later messages and the stopped turn anchored after it")

	// A chat saved by an older binary before its marker.
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_conversations (conversation_id, agent, created_by) VALUES ('legacy', 'main', '')`)
	require.NoError(t, err)
	hits, complete, err = s.SearchMessages(ctx, "needle", nil)
	require.NoError(t, err)
	require.False(t, complete)
	require.Equal(t, all[:2], hits)

	require.NoError(t, s.backfillMessageSearch(ctx))
	_, complete, err = s.SearchMessages(ctx, "nothing matches", nil)
	require.NoError(t, err)
	require.True(t, complete)
}

func TestSearchMessagesMatchesGoReference(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	rng := rand.New(rand.NewPCG(8, 10))
	alphabet := []string{"a", "b", "A", "B", " ", "%", "_", `\`, "\u212A", "k", "İ", "i", "ß", "é", "<@U1>"}
	item := func(role string) string {
		var text strings.Builder
		for range 1 + rng.IntN(10) {
			text.WriteString(alphabet[rng.IntN(len(alphabet))])
		}

		return `{"type":"message","role":"` + role + `","content":` + jsonString(text.String()) + `}`
	}

	// Chat IDs sort the same in Go and in any database collation.
	var want []MessageSearchHit

	for chat := range 10 {
		id := fmt.Sprintf("chat%d", chat)
		require.NoError(t, s.UpsertThread(id, ThreadState{Agent: "main"}))

		prompts := make([]string, 8)

		for turn := range 8 {
			items := make([]string, 2+rng.IntN(7))
			for i := range items {
				items[i] = item([]string{"user", "assistant"}[i%2])
			}

			entryID, err := s.AppendEntryID(ctx, id, testReplayEntry(items...))
			require.NoError(t, err)

			prompts[turn] = fmt.Sprintf("%d:%d", entryID, 2*rng.IntN((len(items)+1)/2))

			if rng.IntN(3) == 0 {
				turnID := fmt.Sprintf("%s-turn%d", id, turn)
				seedActiveTurn(t, s, id, turnID, testReplayEntry(item("user"), item("assistant")))
				endTestTurn(t, s, id, turnID, protocol.TerminalStopped)
			}
		}

		if chat%3 == 0 {
			_, _, err := stageRevertDB(ctx, s.db, id, prompts[rng.IntN(len(prompts))])
			require.NoError(t, err)
		}

		// History, not the index, decides what the reference sees.
		transcript, err := s.ObserveTranscript(ctx, id, 0, 0, nil)
		require.NoError(t, err)

		for _, entry := range transcript {
			for i, raw := range entry.Entry.ReplayInput {
				var message struct{ Role, Content string }
				require.NoError(t, json.Unmarshal(raw, &message))

				if strings.TrimSpace(message.Content) == "" {
					continue
				}

				hit := MessageSearchHit{ConversationID: id, Role: message.Role, Text: message.Content}
				if entry.ID != 0 {
					hit.MessageID = fmt.Sprintf("%d:%d", entry.ID, i)
				}

				want = append(want, hit)
			}
		}
	}

	require.Greater(t, len(want), 300)

	for _, id := range []string{"cron:daily", "private"} {
		require.NoError(t, s.UpsertThread(id, ThreadState{Agent: "main"}))
		_, err := s.AppendEntryID(ctx, id, testReplayEntry(item("user"), item("assistant"), item("user")))
		require.NoError(t, err)
	}

	require.NoError(t, s.RegisterExternalMCPConversation("public", "main", &ExternalMCPSessionState{PrivateConversationID: "private", ManagedConversationID: "managed", Agent: "main"}))

	type search struct {
		needle   string
		prefixes []string
	}

	searches := slices.Grow([]search{{"a", nil}, {"%", nil}, {"_", nil}, {`\`, nil}, {"k", nil}, {"ab", nil}, {"i̇", nil}, {"%_", nil}, {"zzz", []string{"<@u1"}}, {"b", []string{"<@u1"}}}, 60)

	for range 60 {
		text := []rune(strings.ToLower(want[rng.IntN(len(want))].Text))
		start := rng.IntN(len(text))
		searches = append(searches, search{needle: string(text[start:min(len(text), start+1+rng.IntN(4))])})
	}

	// A few hundred rows fit in a page or two, where the planner rightly skips
	// the index; this transaction runs the same statement through the index.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `SET LOCAL enable_seqscan = off`)
	require.NoError(t, err)

	plan, err := queryStrings(ctx, tx, `EXPLAIN `+searchMessagesSQL, "message search plan", pgx.QueryExecModeExec, "", []string{"%ab %"})
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan, "\n"), "message_search_text")

	escape := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

	for _, search := range searches {
		var reference []MessageSearchHit

		for _, hit := range want {
			text := strings.ToLower(hit.Text)
			if strings.Contains(text, search.needle) || slices.ContainsFunc(search.prefixes, func(prefix string) bool { return strings.Contains(text, prefix) }) {
				reference = append(reference, hit)
			}
		}

		hits, complete, err := s.SearchMessages(ctx, search.needle, search.prefixes)
		require.NoError(t, err)
		require.True(t, complete)
		require.Equal(t, reference, hits, "%q %q", search.needle, search.prefixes)

		patterns := []string{"%" + escape.Replace(search.needle) + "%"}
		for _, prefix := range search.prefixes {
			patterns = append(patterns, "%"+escape.Replace(prefix)+"%")
		}

		var raw []byte
		require.NoError(t, tx.QueryRowContext(ctx, searchMessagesSQL, pgx.QueryExecModeExec, "", patterns).Scan(&complete, &raw))
		require.NoError(t, json.Unmarshal(raw, &hits))
		require.Equal(t, reference, hits, "index scan %q %q", search.needle, search.prefixes)
	}
}
