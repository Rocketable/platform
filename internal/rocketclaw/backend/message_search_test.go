package backend

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
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

	texts, err := queryRows(t.Context(), s.db, `WITH reporting AS (
    SELECT conversation_id FROM conversations WHERE reports_to = $1
), unreported AS (
    SELECT $1::text AS conversation_id FROM conversations WHERE conversation_id = $1 AND reports_to IS NOT NULL
), sources AS (
    SELECT $1::text AS conversation_id UNION SELECT conversation_id FROM reporting EXCEPT SELECT conversation_id FROM unreported
), shown AS (
    SELECT e.id, e.search_text FROM sources r JOIN entries e USING (conversation_id)
    UNION ALL SELECT 0, search_text FROM turns WHERE conversation_id = $1
)
SELECT id, search_text FROM shown WHERE search_text IS NOT NULL ORDER BY id`, "message search rows", func(row rowScanner) (messageSearchTestRow, error) {
		var r messageSearchTestRow
		if err := row.Scan(&r.EntryID, &r.Text); err != nil {
			return r, fmt.Errorf("scan message search text: %w", err)
		}

		return r, nil
	}, conversationID)
	require.NoError(t, err)

	var rows []messageSearchTestRow

	for _, text := range texts {
		for line := range strings.SplitSeq(text.Text, "\n") {
			fields := [4]string{}
			row := messageSearchTestRow{EntryID: text.EntryID}
			head, encoded, _ := strings.Cut(line, " [")
			_, err := fmt.Sscan(head, &row.ReplayIndex, &row.PartIndex)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal([]byte("["+encoded), &fields))
			row.Role, row.Text, row.TextLower = fields[0], fields[2], fields[3]
			rows = append(rows, row)
		}
	}

	return rows
}

func uploadTestAttachment(t *testing.T, s *SessionService, conversationID, id string) string {
	t.Helper()

	require.NoError(t, s.SaveAttachment(t.Context(), conversationID, "", &protocol.OutboundAttachment{ID: id, Name: "notes.txt", MIMEType: "text/plain", Data: []byte("notes")}, true))

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
	data, _ := json.Marshal(text)
	return string(data)
}

func TestMessageSearchIndexesSavedEntries(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"web-chat": KindWebChat})
	reference := uploadTestAttachment(t, s, "web-chat", "file-1")
	uploadTestAttachment(t, s, "web-chat", "file-2")

	easyReference := `attachment:file-2 "notes.txt" (path "/ws/.rocketclaw/uploads/web-chat/file-2/notes.txt")`

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
		`{"type":"message","role":"user","content":`+jsonString("see easy\n\n"+easyReference)+`}`,
	))
	require.NoError(t, err)
	require.Equal(t, []messageSearchTestRow{
		{id, 0, 0, "user", "Hello ÀB", "hello àb"},
		{id, 1, 0, "assistant", "First PART", "first part"},
		{id, 1, 2, "assistant", "Third", "third"},
		{id, 2, 0, "user", "look attachment:nope", "look attachment:nope"},
		{id, 8, 0, "assistant", "nul Reply", "nul reply"},
		{id, 10, 0, "user", "see easy", "see easy"},
	}, messageSearchTestRows(t, s, "web-chat"))

	_, err = s.DeleteSession(ctx, "web-chat")
	require.NoError(t, err)
	require.Empty(t, messageSearchTestRows(t, s, "web-chat"), "deleted entries cascade")

	_, err = s.AppendEntryID(ctx, "web-chat", testReplayEntry(`{"type":"message","id":"msg_2","role":"assistant","content":[{"type":"output_text","text":"ok"},{"type":"refusal","refusal":"no","text":7}]}`))
	require.ErrorContains(t, err, "decode")
	entries, err := s.ObserveEntries(ctx, "web-chat")
	require.NoError(t, err)
	require.Empty(t, entries)

	recordTestConversations(t, s, "job", map[string]ConversationKind{"cron:daily": KindRoutineRun, "one-off-cron:once": KindOneOffRoutineRun})

	for _, conversationID := range []string{"cron:daily", "one-off-cron:once"} {
		_, err := s.AppendEntryID(ctx, conversationID, testSessionEntry("hidden prompt", "hidden answer"))
		require.NoError(t, err)
	}

	const handoff = "web:cron:cron/daily.md:1"
	createWebChat(t, s, protocol.Conversation{ID: handoff, Agent: "main", CreatedBy: string(ThreadCreatedByRoutine)})
	recordTestConversations(t, s, "main", map[string]ConversationKind{handoff + "/call-1": KindSubagent})
	handoffID, err := s.AppendEntryID(ctx, handoff, testSessionEntry("handoff prompt", "handoff answer"))
	require.NoError(t, err)
	_, err = s.AppendEntryID(ctx, handoff+"/call-1", testSessionEntry("handoff prompt", "delegated answer"))
	require.NoError(t, err)
	hits, err := s.SearchMessages(ctx, "handoff", nil)
	require.NoError(t, err)
	require.Equal(t, []MessageSearchHit{
		{ConversationID: handoff, Role: "user", Text: "handoff prompt", MessageID: fmt.Sprintf("%d:0", handoffID)},
		{ConversationID: handoff, Role: "assistant", Text: "handoff answer", MessageID: fmt.Sprintf("%d:1", handoffID)},
	}, hits, "the Delegation History stays out of search")
	require.Empty(t, searchTestTexts(t, s, "hidden"), "search never reads a cron run")
}

func TestMessageSearchIndexesForkedAttachments(t *testing.T) {
	t.Parallel()

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

func TestMessageSearchIndexesBoundRunEntries(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	manager, _ := newTestBridgeManager(t, config.NewLockedConfig(&config.Config{Workspace: t.TempDir()}), s, make(chan *protocol.OutboundMessage, 4))
	rt := &Runtime{Sessions: s, threads: manager}

	recordTestConversations(t, s, "job", map[string]ConversationKind{"cron:daily": KindRoutineRun})
	require.NoError(t, rt.CreateConversation(ctx, protocol.Conversation{ID: "web-dest", Agent: "main"}))

	reference := uploadTestAttachment(t, s, "cron:daily", "cron-file")
	_, err := s.AppendEntryID(ctx, "cron:daily", testReplayEntry(
		`{"type":"message","role":"user","content":`+jsonString("cron report\n\n"+reference)+`}`,
		`{"type":"message","role":"assistant","content":"cron answer"}`,
	))
	require.NoError(t, err)
	require.Empty(t, messageSearchTestRows(t, s, "web-dest"))
	require.Empty(t, messageSearchTestRows(t, s, "cron:daily"), "search shows no unbound run, so its entries get no text until it is bound")

	bindTestRun(t, s, "cron:daily", "web-dest")

	entries, err := s.ObserveEntries(ctx, "web-dest")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, []messageSearchTestRow{
		{entries[0].ID, 0, 0, "user", "cron report", "cron report"},
		{entries[0].ID, 1, 0, "assistant", "cron answer", "cron answer"},
	}, messageSearchTestRows(t, s, "web-dest"))

	bindTestRun(t, s, "cron:daily", "web-dest")
	require.Len(t, messageSearchTestRows(t, s, "web-dest"), 2, "binding again indexes nothing new")

	recordTestConversations(t, s, "job", map[string]ConversationKind{"cron:broken": KindRoutineRun})
	_, err = s.db.ExecContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ('cron:broken', '{"replay_input":[{"type":"message","id":"msg_2","role":"assistant","content":[{"type":"output_text","text":"ok"},{"type":"refusal","refusal":"no","text":7}]}]}', '')`)
	require.NoError(t, err)
	_, _, err = (stateDAO{db: s.db}).bindRun(ctx, "cron:broken", "web-dest")
	require.ErrorContains(t, err, "decode")
}

func TestMessageSearchFindsContinuedRun(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	manager, _ := newTestBridgeManager(t, config.NewLockedConfig(&config.Config{Workspace: t.TempDir()}), s, make(chan *protocol.OutboundMessage, 4))
	rt := &Runtime{Sessions: s, threads: manager}
	recordTestConversations(t, s, "job", map[string]ConversationKind{"cron:daily": KindRoutineRun, "cron:other": KindRoutineRun})

	for _, id := range []string{"thread", "web:cron:daily", "web:cron:other"} {
		require.NoError(t, rt.CreateConversation(ctx, protocol.Conversation{ID: id, Agent: "main"}))
	}

	bindTestRun(t, s, "cron:daily", "thread")
	report, err := s.AppendEntryID(ctx, "cron:daily", testReplayEntry(`{"type":"message","role":"user","prompt_header":"[Cron principal=\"Cron Writer\"]","content":"Revenue dropped in Germany"}`))
	require.NoError(t, err)
	adopted, err := s.AppendEntryID(ctx, "cron:other", testSessionEntry("Germany prompt", "other answer"))
	require.NoError(t, err)

	for _, run := range []string{"cron:daily", "cron:other"} {
		_, err := rt.ContinueRun(ctx, run, "web:"+run, false)
		require.NoError(t, err)
	}

	require.Equal(t, map[string][]string{"thread": {"Cron Writer"}, "web:cron:daily": {"Cron Writer"}}, storedPrincipals(t, s))

	hits, err := s.SearchMessages(ctx, "germany", nil)
	require.NoError(t, err)
	require.Equal(t, []MessageSearchHit{
		{ConversationID: "thread", Role: "user", Text: "Revenue dropped in Germany", MessageID: fmt.Sprintf("%d:0", report)},
		{ConversationID: "web:cron:daily", Role: "user", Text: "Revenue dropped in Germany", MessageID: fmt.Sprintf("%d:0", report)},
		{ConversationID: "web:cron:other", Role: "user", Text: "Germany prompt", MessageID: fmt.Sprintf("%d:0", adopted)},
	}, hits)

	_, err = s.AppendEntryID(ctx, "cron:daily", testReplayEntry(`{"type":"message","role":"user","prompt_header":"[Cron principal=\"Ann\"]","content":"follow-up"}`))
	require.NoError(t, err)
	require.Equal(t, []string{"Ann", "Cron Writer"}, storedPrincipals(t, s)["web:cron:daily"], "a later run message reaches the chat that continues it")
}

func storedPrincipals(t *testing.T, s *SessionService) map[string][]string {
	t.Helper()

	principals := map[string][]string{}

	require.NoError(t, readRows(t.Context(), s.db, `WITH stored AS (
    SELECT c.conversation_id, p.principal FROM conversations c CROSS JOIN LATERAL unnest(c.principals) AS p(principal)
)
SELECT conversation_id, principal FROM stored ORDER BY conversation_id COLLATE "C", principal COLLATE "C"`, "stored principals", func(row rowScanner) error {
		var id, principal string
		if err := row.Scan(&id, &principal); err != nil {
			return fmt.Errorf("scan stored principal: %w", err)
		}

		principals[id] = append(principals[id], principal)

		return nil
	}))

	return principals
}

func TestMessagePrincipalsFollowHistory(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "job", map[string]ConversationKind{"cron:weekly": KindRoutineRun})
	require.NoError(t, (&Runtime{Sessions: s}).CreateConversation(ctx, protocol.Conversation{ID: "chat", Agent: "main"}))
	bindTestRun(t, s, "cron:weekly", "chat")
	_, err := s.AppendEntryID(ctx, "cron:weekly", testReplayEntry(`{"type":"message","role":"user","prompt_header":"[Cron principal=\"Cron Writer\"]","content":"report"}`))
	require.NoError(t, err)

	first, err := s.AppendEntryID(ctx, "chat", testReplayEntry(
		`{"type":"message","role":"user","prompt_header":"[Web principal=\"Ann\"]","content":"[Web principal=\"Ann\"]\n\nfirst"}`,
		`{"type":"message","role":"assistant","content":"reply"}`,
		`{"type":"message","role":"user","prompt_header":"[Web principal=\"Bob\"]","content":"[Web principal=\"Bob\"]\n\nsecond"}`,
		`{"type":"message","role":"assistant","content":"another reply"}`,
	))
	require.NoError(t, err)

	all := map[string][]string{"chat": {"Ann", "Bob", "Cron Writer"}}
	require.Equal(t, all, storedPrincipals(t, s))
	search, _ := searchTestSessions(t, s, noSlackLookups{}, "", SearchFields)
	require.Equal(t, all["chat"], search.Principals)

	_, _, err = stageRevertDB(ctx, s.db, "chat", fmt.Sprintf("%d:2", first), protocol.RevertBefore)
	require.NoError(t, err)
	require.Equal(t, all, storedPrincipals(t, s), "moving the current point keeps every branch's participants")

	_, err = s.AppendEntryID(ctx, "chat", testReplayEntry(`{"type":"message","role":"user","prompt_header":"[Web principal=\"Cid\"]","content":"[Web principal=\"Cid\"]\n\nother"}`))
	require.NoError(t, err)
	search, _ = searchTestSessions(t, s, noSlackLookups{}, "", SearchFields)
	require.Equal(t, []string{"Ann", "Bob", "Cid", "Cron Writer"}, search.Principals, "Bob wrote only on the branch left behind and stays a participant")

	_, err = s.DeleteSession(ctx, "chat")
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"chat": {"Cron Writer"}}, storedPrincipals(t, s), "a cleared chat keeps its run's principals")
}

func TestMessagePrincipalsCommitWithTheirWrite(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	bridge := &Bridge{config: Config{ConversationID: "chat", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	rt := &Runtime{Sessions: s, threads: newThreadBridgeManager(nil, s, bridge.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return bridge }})}
	recordTestConversations(t, s, "job", map[string]ConversationKind{"cron:daily": KindRoutineRun})
	require.NoError(t, rt.CreateConversation(ctx, protocol.Conversation{ID: "chat", Agent: "main"}))

	prompt := func(principal string) *harness.SessionEntry {
		header := `[Web principal=\"` + principal + `\"]`

		return testReplayEntry(`{"type":"message","role":"user","prompt_header":"` + header + `","content":"` + header + `\n\nhello"}`)
	}
	reject := func(table, column string) {
		statement := `DROP TRIGGER reject_write ON ` + table
		if column != "" {
			statement = `CREATE TRIGGER reject_write BEFORE UPDATE OF ` + column + ` ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION reject_write()`
		}

		_, err := s.db.ExecContext(ctx, statement)
		require.NoError(t, err)
	}
	inTx := func(write func(tx *sql.Tx) error) func() error {
		return func() error {
			tx, err := s.db.BeginTx(ctx, nil)
			require.NoError(t, err)

			defer func() { _ = tx.Rollback() }()

			return write(tx)
		}
	}

	_, err := s.db.ExecContext(ctx, `CREATE FUNCTION reject_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'write unavailable'; END $$`)
	require.NoError(t, err)
	_, err = s.AppendEntryID(ctx, "chat", prompt("Ann"))
	require.NoError(t, err)
	_, err = s.AppendEntryID(ctx, "cron:daily", prompt("Cron"))
	require.NoError(t, err)
	seedActiveTurn(t, s, "chat", "stopped", prompt("Bob"))
	_, err = s.finishTurn(ctx, "stopped", &turnFinish{conversationID: strings.TrimSpace("chat"), outbound: protocol.NewOutboundMessage("chat", ""), terminal: protocol.TerminalStopped})
	require.NoError(t, err)

	saved := testReplayEntry(`{"type":"message","role":"user","content":"saved"}`)
	saved.TurnID = "stopped"
	hideTurn := func() error { _, err := s.AppendEntryID(ctx, "chat", saved); return err }
	bind := inTx(func(tx *sql.Tx) error {
		_, _, err := (stateDAO{db: tx}).bindRun(ctx, "cron:daily", "chat")
		return err
	})

	for _, tc := range []struct {
		name, table, column string
		write               func() error
	}{
		{"append", "conversations", "principals", func() error { _, err := s.AppendEntryID(ctx, "chat", prompt("Dan")); return err }},
		{"hide turn", "conversations", "principals", hideTurn},
		{"hide turn text", "turns", "search_text", hideTurn},
		{"end turn", "conversations", "principals", func() error {
			seedActiveTurn(t, s, "chat", "failed", prompt("Eve"))
			_, err := s.finishTurn(ctx, "failed", &turnFinish{conversationID: strings.TrimSpace("chat"), outbound: protocol.NewOutboundMessage("chat", ""), terminal: protocol.TerminalFailed})

			return err
		}},
		{"bind", "conversations", "principals", bind},
		{"bind text", "entries", "search_text", bind},
		{"delete", "conversations", "principals", func() error { _, err := s.DeleteSession(ctx, "chat"); return err }},
		{"prune", "conversations", "principals", inTx(func(tx *sql.Tx) error { _, err := deleteOwnedHistory(ctx, tx, []string{"chat"}); return err })},
	} {
		reject(tc.table, tc.column)
		require.ErrorContains(t, tc.write(), "write unavailable", tc.name)
		reject(tc.table, "")
	}

	require.Equal(t, map[string][]string{"chat": {"Ann", "Bob"}}, storedPrincipals(t, s), "no failed write changed them")

	seedActiveTurn(t, s, "chat", "attached", testReplayEntry(`{"type":"message","role":"user","content":"see attachment:file"}`))
	_, err = s.db.ExecContext(ctx, `ALTER TABLE attachments RENAME TO unavailable_attachments`)
	require.NoError(t, err)
	_, err = s.finishTurn(ctx, "attached", &turnFinish{conversationID: strings.TrimSpace("chat"), outbound: protocol.NewOutboundMessage("chat", ""), terminal: protocol.TerminalStopped})
	require.ErrorContains(t, err, `relation "attachments" does not exist`)
	_, err = s.db.ExecContext(ctx, `ALTER TABLE unavailable_attachments RENAME TO attachments`)
	require.NoError(t, err)
}

func TestMessageSearchSkipsRunEntriesBeforeAttach(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "job", map[string]ConversationKind{"cron:daily": KindRoutineRun})
	require.NoError(t, (&Runtime{Sessions: s}).CreateConversation(ctx, protocol.Conversation{ID: "chat", Agent: "main"}))
	_, err := s.AppendEntryID(ctx, "cron:daily", testReplayEntry(`{"type":"compaction","id":"cmp_1","encrypted_content":"sealed"}`, `{"type":"message","role":"user","content":"early report"}`))
	require.NoError(t, err)

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "next", true)
	inbound.ConversationID, inbound.SyncDestination = "cron:daily", "chat"
	require.NoError(t, startTurnDB(ctx, s.db, "turn-1", "cron:daily", inbound))
	later, err := s.AppendEntryID(ctx, "cron:daily", testReplayEntry(`{"type":"message","role":"user","content":"later report"}`))
	require.NoError(t, err)

	hits, err := s.SearchMessages(ctx, "report", nil)
	require.NoError(t, err)
	require.Equal(t, []MessageSearchHit{{ConversationID: "chat", Role: "user", Text: "later report", MessageID: fmt.Sprintf("%d:0", later)}}, hits)
}

func TestMessageSearchReadsStoppedTurnText(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	createWebChat(t, s, protocol.Conversation{ID: "chat", Agent: "main"})
	seedActiveTurn(t, s, "chat", "stopped", testReplayEntry(
		`{"type":"message","role":"user","content":"find the needle"}`,
		`{"type":"function_call","call_id":"call","name":"Read","arguments":"{}"}`,
		`{"type":"function_call_output","call_id":"call","output":`+jsonString(strings.Repeat("tool output needle ", 1<<18))+`}`,
		`{"type":"message","role":"assistant","content":"needle found"}`,
	))
	endTestTurn(t, s, "chat", "stopped", protocol.TerminalStopped)

	var text string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT search_text FROM turns WHERE id = 'stopped'`).Scan(&text))
	require.Less(t, len(text), 200, "only the turn's messages are searchable")

	_, err := s.db.ExecContext(ctx, `UPDATE turns SET replay = '[7]', record = '7' WHERE id = 'stopped'`)
	require.NoError(t, err)

	hits, err := s.SearchMessages(ctx, "needle", nil)
	require.NoError(t, err)
	require.Equal(t, []MessageSearchHit{{ConversationID: "chat", Role: "user", Text: "find the needle"}, {ConversationID: "chat", Role: "assistant", Text: "needle found"}}, hits)
}

func bindTestRun(t *testing.T, s *SessionService, run, chat string) {
	t.Helper()

	tx, err := s.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	require.NoError(t, lockHistories(t.Context(), tx, []string{run, chat}))
	_, _, err = (stateDAO{db: tx}).bindRun(t.Context(), run, chat)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

func TestMessageSearchIndexesEndedTurns(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	createWebChat(t, s, protocol.Conversation{ID: "web-chat", Agent: "main"})
	reference := uploadTestAttachment(t, s, "web-chat", "turn-file")
	turn := &testCheckpoint{TurnID: "turn-1", ConversationKey: "web-chat", ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","prompt_header":"[Web principal=\"Stopped Writer\"]","content":` + jsonString("stopped question\n\n"+reference) + `}`),
		json.RawMessage(`{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Partial answer","annotations":[]}]}`),
	}, OutputTrace: []json.RawMessage{json.RawMessage(`{"type":"response.output_text.delta","item_id":"msg_2","content_index":0,"delta":"progress only"}`)}}
	require.NoError(t, upsertTestTurn(ctx, s, turn))
	require.Empty(t, messageSearchTestRows(t, s, "web-chat"), "a running turn is not searchable yet")

	endTestTurn(t, s, "web-chat", "turn-1", protocol.TerminalStopped)

	transcript := testHistoryEntries(t, s, "web-chat")
	require.Len(t, transcript, 1)
	require.Equal(t, protocol.TerminalStopped, transcript[0].Terminal)
	require.Equal(t, []messageSearchTestRow{
		{0, 0, 0, "user", "stopped question", "stopped question"},
		{0, 1, 0, "assistant", "Partial answer", "partial answer"},
	}, messageSearchTestRows(t, s, "web-chat"), "History shows the stopped turn's record, not its progress log")
	search, matches := searchTestSessions(t, s, noSlackLookups{}, `principal:"Stopped Writer"`, SearchFields)
	require.Empty(t, search.Principals, "a chat without saved history is not listed in the sidebar")
	require.Empty(t, matches)

	_, err := s.AppendEntryID(ctx, "web-chat", testReplayEntry())
	require.NoError(t, err)
	_, matches = searchTestSessions(t, s, noSlackLookups{}, `principal:"Stopped Writer"`, SearchFields)
	require.Len(t, matches, 1)

	turn.ReplayInput = append(turn.ReplayInput, json.RawMessage(`{"type":"message","role":"assistant","content":"Late answer"}`))
	require.NoError(t, upsertTestTurn(ctx, s, turn))
	_, err = s.finishTurn(ctx, "turn-1", &turnFinish{conversationID: strings.TrimSpace("web-chat"), outbound: protocol.NewOutboundMessage("web-chat", "late")})
	require.NoError(t, err)
	require.Equal(t, []messageSearchTestRow{
		{0, 0, 0, "user", "stopped question", "stopped question"},
		{0, 1, 0, "assistant", "Partial answer", "partial answer"},
	}, messageSearchTestRows(t, s, "web-chat"))

	entry := testReplayEntry(`{"type":"message","role":"user","content":"saved question"}`)
	entry.TurnID = "turn-1"
	id, err := s.AppendEntryID(ctx, "web-chat", entry)
	require.NoError(t, err)
	require.Equal(t, []messageSearchTestRow{{id, 0, 0, "user", "saved question", "saved question"}}, messageSearchTestRows(t, s, "web-chat"), "the turn's saved entry hides it")
	_, matches = searchTestSessions(t, s, noSlackLookups{}, `principal:"Stopped Writer"`, SearchFields)
	require.Empty(t, matches, "the saved entry replaces the stopped turn's principal too")

	require.NoError(t, upsertTestTurn(ctx, s, &testCheckpoint{TurnID: "turn-2", ConversationKey: "web-chat", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"failed question"}`)}}))
	endTestTurn(t, s, "web-chat", "turn-2", protocol.TerminalFailed)
	require.Equal(t, []messageSearchTestRow{
		{0, 0, 0, "user", "failed question", "failed question"},
		{id, 0, 0, "user", "saved question", "saved question"},
	}, messageSearchTestRows(t, s, "web-chat"))

	_, err = s.DeleteSession(ctx, "web-chat")
	require.NoError(t, err)
	require.Empty(t, messageSearchTestRows(t, s, "web-chat"), "turn rows cascade with their turn")
	search, _ = searchTestSessions(t, s, noSlackLookups{}, "", SearchFields)
	require.Empty(t, search.Principals)
}

func TestMessageSearchSkipsUndecodableTurnRecords(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"web-chat": KindWebChat})

	var logs bytes.Buffer

	s.log = slog.New(slog.NewTextHandler(&logs, nil))

	for turnID, replay := range map[string][]json.RawMessage{
		"bad-item":   {json.RawMessage(`{"type":"message","role":"user","content":"attachment:none"}`), json.RawMessage(`{"type":"message","id":"msg_2","role":"assistant","content":[{"type":"output_text","text":"ok"},{"type":"refusal","refusal":"no","text":7}]}`)},
		"bad-record": {json.RawMessage(`7`)},
	} {
		seedActiveTurn(t, s, "web-chat", turnID, &harness.SessionEntry{Version: 1, Type: "turn", TurnID: turnID, ReplayInput: replay})
		endTestTurn(t, s, "web-chat", turnID, protocol.TerminalFailed)
	}

	failed, err := queryStrings(ctx, s.db, `SELECT id FROM turns WHERE terminal = $1 ORDER BY id`, "failed turns", protocol.TerminalFailed)
	require.NoError(t, err)
	require.Equal(t, []string{"bad-item", "bad-record"}, failed)
	require.Empty(t, messageSearchTestRows(t, s, "web-chat"))
	require.Equal(t, 2, strings.Count(logs.String(), "\n"), logs.String())
}

func TestFinishTurnKeepsUnreadableStep(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	recordTestConversations(t, s, "main", map[string]ConversationKind{"web-chat": KindWebChat})
	seedActiveTurn(t, s, "web-chat", "turn-1", nil)
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO steps (conversation_id, key, value) VALUES ('web-chat', 'turn-1', '{"record":{"text":"\u0000"}}')`)
	require.NoError(t, err)

	require.ErrorContains(t, terminateTestTurn(t.Context(), s, "turn-1", protocol.TerminalStopped), "move turn record")
	require.Equal(t, []string{"turn-1"}, runningTestTurns(t, s))
	require.Equal(t, []string{"turn-1"}, testTurnStepKeys(t, s, "web-chat"))
}

func TestFinishTurnLocksHistoryBeforeTurnRow(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"web-chat": KindWebChat})
	seedActiveTurn(t, s, "web-chat", "turn-1", nil)

	tx, err := s.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	require.NoError(t, lockSessionHistory(ctx, tx, "web-chat"))

	var holder int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holder))

	var group errgroup.Group
	group.Go(func() error {
		_, err := s.finishTurn(ctx, "turn-1", &turnFinish{conversationID: strings.TrimSpace("web-chat"), outbound: protocol.NewOutboundMessage("web-chat", ""), terminal: protocol.TerminalStopped})
		return err
	})
	require.Eventually(t, func() bool {
		var blocked bool
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, holder).Scan(&blocked))

		return blocked
	}, 5*time.Second, time.Millisecond)

	_, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout = '1s'; UPDATE turns SET updated_at_unix_ns = 0 WHERE id = 'turn-1'`)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.NoError(t, group.Wait())
}

func searchTestTexts(t *testing.T, s *SessionService, needle string, prefixes ...string) []string {
	t.Helper()

	hits, err := s.SearchMessages(t.Context(), needle, prefixes)
	require.NoError(t, err)

	texts := make([]string, len(hits))
	for i, hit := range hits {
		texts[i] = hit.Text
	}

	return texts
}

func TestSearchMessagesMatchesLowercasedSubstrings(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	createWebChat(t, s, protocol.Conversation{ID: "web-chat", Agent: "main"})

	texts := []string{"Sam said hi", "sample levels", "100% done", "1000 done", "snake_case", "snakeXcase", `C:\path`, "C:/path",
		"ping <@U123>", "ping <!subteam^S9>", "\u212Aelvin", "KELVIN", "İstanbul", "Istanbul"}
	items := make([]string, len(texts))

	for i, text := range texts {
		items[i] = `{"type":"message","role":"user","content":` + jsonString(text) + `}`
	}

	id, err := s.AppendEntryID(ctx, "web-chat", testReplayEntry(items...))
	require.NoError(t, err)

	hits, err := s.SearchMessages(ctx, "sam", nil)
	require.NoError(t, err)
	require.Equal(t, []MessageSearchHit{
		{ConversationID: "web-chat", Role: "user", Text: "Sam said hi", MessageID: fmt.Sprintf("%d:0", id)},
		{ConversationID: "web-chat", Role: "user", Text: "sample levels", MessageID: fmt.Sprintf("%d:1", id)},
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

func TestMessageSearchDecodeAllocationBudget(t *testing.T) {
	lines := messageSearchDecodeFixture()

	var decoder messageSearchDecoder

	require.Less(t, testing.AllocsPerRun(10, func() {
		for i, line := range lines {
			replay, fields, err := decoder.decode(line)
			if err != nil || replay != i || fields[0] != "user" || !strings.HasPrefix(fields[2], "Sample") || !strings.EqualFold(fields[3], fields[2]) {
				t.Fatalf("decode line %d: replay=%d, error=%v", i, replay, err)
			}
		}
	}), float64(len(lines)*10))
}

func BenchmarkMessageSearchDecode(b *testing.B) {
	lines := messageSearchDecodeFixture()

	b.ReportAllocs()

	for b.Loop() {
		var decoder messageSearchDecoder
		for _, line := range lines {
			if _, _, err := decoder.decode(line); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func messageSearchDecodeFixture() []string {
	lines := make([]string, 24)
	for i := range lines {
		text := strings.Repeat("Sample text with \"quotes\", \\ and ☀\n", i+1)
		lines[i] = fmt.Sprintf("%d %d %s\n", i, i+1, searchJSON([4]string{"user", "", text, strings.ToLower(text)}))
	}

	return lines
}

func TestSearchMessagesReadEveryBranch(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	createWebChat(t, s, protocol.Conversation{ID: "main", Agent: "main"})

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

	recordTestConversations(t, s, "job", map[string]ConversationKind{"cron:daily": KindRoutineRun})
	_, err = s.AppendEntryID(ctx, "cron:daily", testSessionEntry("needle cron", "answer"))
	require.NoError(t, err)
	require.NoError(t, s.RegisterExternalMCPConversation("public", "main", &ExternalMCPSessionState{PrivateConversationID: "private", ManagedConversationID: "managed", Agent: "main"}))
	_, err = s.AppendEntryID(ctx, "private", testSessionEntry("needle private", "answer"))
	require.NoError(t, err)
	require.Empty(t, messageSearchTestRows(t, s, "private"), "search never shows a private External MCP conversation, so its entries get no text")

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
	hits, err := s.SearchMessages(ctx, "needle", nil)
	require.NoError(t, err)
	require.Equal(t, all, hits, "the stopped turn sits after its anchor entry")

	_, _, err = stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:0", third), protocol.RevertBefore)
	require.NoError(t, err)
	hits, err = s.SearchMessages(ctx, "needle", nil)
	require.NoError(t, err)
	require.Equal(t, all, hits, "moving the current point hides nothing from search")

	branch, err := s.AppendEntryID(ctx, "main", testReplayEntry(
		`{"type":"message","role":"user","content":"needle 10"}`,
		`{"type":"message","role":"assistant","content":"needle 11"}`,
		`{"type":"message","role":"user","content":"later"}`,
	))
	require.NoError(t, err)
	marker, _, err := stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:1", branch), protocol.RevertThrough)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d:2", branch), marker)

	hits, err = s.SearchMessages(ctx, "needle", nil)
	require.NoError(t, err)
	require.Equal(t, slices.Concat(all, []MessageSearchHit{
		{"main", "user", "needle 10", fmt.Sprintf("%d:0", branch)},
		{"main", "assistant", "needle 11", fmt.Sprintf("%d:1", branch)},
	}), hits, "a word only on the branch left behind still finds the chat")

	opened, err := s.ObserveHistory(ctx, "main", "", third, 0, 0, &HistoryInventory{})
	require.NoError(t, err)
	require.Equal(t, marker, opened.Inventory.Marker)
	require.Len(t, opened.Entries, 1)
	require.Equal(t, branch, opened.Entries[0].ID, "opening a hit on the other branch reads the current path")

	current, _, _, err := s.RevertState(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, marker, current, "searching and opening a hit leave the current point where it was")
	require.Equal(t, []string{"message:needle 1", "message:needle 2", "message:needle 3", "message:needle 4", "message:needle 5", "message:needle 6", "message:needle 10", "message:needle 11"}, observedEntryTexts(t, s, "main"))

	_, err = s.db.ExecContext(ctx, `UPDATE entries SET search_text = '0 0 ["needle"' WHERE id = $1`, first)
	require.NoError(t, err)
	_, err = s.SearchMessages(ctx, "needle", nil)
	require.ErrorContains(t, err, "decode message search text")
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestSearchMessagesMatchesGoReference(t *testing.T) {
	t.Parallel()

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

	var want []MessageSearchHit

	for chat := range 10 {
		id := fmt.Sprintf("chat%d", chat)
		createWebChat(t, s, protocol.Conversation{ID: id, Agent: "main"})

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

		for _, entry := range testHistoryEntries(t, s, id) {
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

		if chat%3 == 0 {
			_, _, err := stageRevertDB(ctx, s.db, id, prompts[rng.IntN(len(prompts))], protocol.RevertBefore)
			require.NoError(t, err)
		}
	}

	require.Greater(t, len(want), 300)

	recordTestConversations(t, s, "main", map[string]ConversationKind{"cron:daily": KindRoutineRun, "private": KindExternalMCPRun})

	for _, id := range []string{"cron:daily", "private"} {
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

	references := make([][]MessageSearchHit, len(searches))

	for i, search := range searches {
		for _, hit := range want {
			text := strings.ToLower(hit.Text)
			if strings.Contains(text, search.needle) || slices.ContainsFunc(search.prefixes, func(prefix string) bool { return strings.Contains(text, prefix) }) {
				references[i] = append(references[i], hit)
			}
		}

		hits, err := s.SearchMessages(ctx, search.needle, search.prefixes)
		require.NoError(t, err)
		require.Equal(t, references[i], hits, "%q %q", search.needle, search.prefixes)
	}

	s.db.SetMaxOpenConns(1)
	_, err := s.db.ExecContext(ctx, `ANALYZE conversations; SELECT gin_clean_pending_list('entries_search_text'::regclass); SET enable_seqscan = off`)
	require.NoError(t, err)

	plan, err := queryStrings(ctx, s.db, `EXPLAIN `+searchMessagesSQL, "message search plan", pgx.QueryExecModeExec, []string{"%ab %"})
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan, "\n"), "entries_search_text")

	for i, search := range searches {
		hits, err := s.SearchMessages(ctx, search.needle, search.prefixes)
		require.NoError(t, err)
		require.Equal(t, references[i], hits, "index scan %q %q", search.needle, search.prefixes)
	}
}
