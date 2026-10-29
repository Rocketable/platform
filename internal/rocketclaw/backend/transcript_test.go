package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// OpenCode V2 packages/core/src/session/projector.ts projects strictly before
// the staged user message; RocketClaw boundaries may lie inside a saved turn.
func TestRevertTranscriptPrefix(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed) VALUES ('main', 'planner', '', 'web_chat', TRUE)`)
	require.NoError(t, err)
	earlier, err := s.AppendEntryID(ctx, "main", testSessionEntry("earlier", "reply"))
	require.NoError(t, err)

	checkpoint := &testCheckpoint{TurnID: "boundary", ConversationKey: "main", ReplayInput: testSessionEntry("kept", "hidden checkpoint").ReplayInput}
	require.NoError(t, upsertTestTurn(ctx, s, checkpoint))
	require.NoError(t, terminateTestTurn(ctx, s, checkpoint.TurnID, protocol.TerminalStopped))

	entry := testSessionEntry("kept", "kept reply")
	entry.TurnID = checkpoint.TurnID
	entry.ReplayInput = append(entry.ReplayInput,
		json.RawMessage(`{"type":"function_call","name":"Task","call_id":"kept-child","arguments":"{}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"kept-child","output":"kept result"}`))
	entry.ReplayInput = append(entry.ReplayInput, testSessionEntry("replace me", "abandoned").ReplayInput...)
	entry.ReplayInput = append(entry.ReplayInput, json.RawMessage(`{"type":"function_call","name":"Task","call_id":"discarded-child","arguments":"{}"}`))
	entry.ResponseID = "hidden-response"
	entry.OutputTrace = []json.RawMessage{json.RawMessage(`{"text":"hidden fallback"}`)}
	entry.TokenUsage = &harness.TokenUsage{TotalTokens: 99}
	entry.ReplayAttribution = []harness.ReplayAttribution{{Start: 1, End: 6, Agent: "old"}, {Start: 4, End: 6, Agent: "hidden"}}
	boundary, err := s.AppendEntryID(ctx, "main", entry)
	require.NoError(t, err)
	_, err = s.AppendEntryID(ctx, "main", testSessionEntry("later", "abandoned later"))
	require.NoError(t, err)

	recordLegacyDelegations(t, s, "main", "kept-child", "discarded-child")

	for _, child := range []string{"main/kept-child", "main/discarded-child"} {
		_, err := s.AppendEntryID(ctx, child, testSessionEntry("child", "reply"))
		require.NoError(t, err)
	}

	_, err = s.db.ExecContext(ctx, `UPDATE conversations SET revert_message_id = $1 WHERE conversation_id = 'main'`, fmt.Sprintf("%d:4", boundary))
	require.NoError(t, err)
	replay, err := s.ObserveEntries(ctx, "main")
	require.NoError(t, err)
	require.Len(t, replay, 2)
	require.Equal(t, earlier, replay[0].ID)
	require.Equal(t, entry.ReplayInput[:4], replay[1].Entry.ReplayInput)
	require.Empty(t, replay[1].Entry.ResponseID)
	require.Empty(t, replay[1].Entry.OutputTrace)
	require.Nil(t, replay[1].Entry.TokenUsage)
	require.Equal(t, []harness.ReplayAttribution{{Start: 1, End: 4, Agent: "old"}}, replay[1].Entry.ReplayAttribution)

	transcript, err := s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, transcript, 2, "superseded checkpoint must not reappear behind the boundary")
	require.Equal(t, replay[1].Entry, transcript[1].Entry)

	children, err := s.Delegations(ctx, "main", "", 0, 0)
	require.NoError(t, err)
	require.Equal(t, []Delegation{{ID: "main/kept-child", Parent: "main", CallID: "kept-child", Legacy: true, Kind: KindSubagent}}, children)

	child, err := s.ObserveEntries(ctx, "main/discarded-child")
	require.NoError(t, err)
	require.Empty(t, child, "direct access cannot reveal a hidden child")

	childCheckpoint := &testCheckpoint{TurnID: "child-checkpoint", ConversationKey: "main/discarded-child", ReplayInput: testSessionEntry("child live", "child output").ReplayInput}
	require.NoError(t, upsertTestTurn(ctx, s, childCheckpoint))
	child, err = s.ObserveTranscript(ctx, "main/discarded-child", 0, 0, nil)
	require.NoError(t, err)
	require.Empty(t, child, "hidden child checkpoints must not bypass the prefix")

	var (
		physical harness.SessionEntry
		raw      string
	)
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT entry_json FROM entries WHERE id = $1`, boundary).Scan(&raw))
	require.NoError(t, json.Unmarshal([]byte(raw), &physical))
	require.Equal(t, *entry, physical, "staging must leave saved bytes intact")

	tx, err := s.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, lockSessionHistory(ctx, tx, "main"))
	require.NoError(t, commitRevertDB(ctx, tx, "main", fmt.Sprintf("%d:4", boundary)))
	require.NoError(t, tx.Commit())

	replay, err = s.ObserveEntries(ctx, "main")
	require.NoError(t, err)
	require.Len(t, replay, 2)
	require.Equal(t, entry.ReplayInput[:4], replay[1].Entry.ReplayInput)

	child, err = s.ObserveEntries(ctx, "main/discarded-child")
	require.NoError(t, err)
	require.Empty(t, child, "commit deletes discarded child history")
	child, err = s.ObserveTranscript(ctx, "main/discarded-child", 0, 0, nil)
	require.NoError(t, err)
	require.Empty(t, child, "discarded child checkpoints must not reappear after commit")
	child, err = s.ObserveEntries(ctx, "main/kept-child")
	require.NoError(t, err)
	require.Len(t, child, 1)
}

func TestObserveTranscript(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	checkpoint := &testCheckpoint{
		TurnID: "turn-1", ConversationKey: "main", Agent: "planner", Model: "provider-model", DisplayModel: "display-model",
		ReasoningEffort: new("high"), ReplayInput: testSessionEntry("hello", "partial").ReplayInput,
		ReplayAttribution: []harness.ReplayAttribution{{Start: 0, End: 1, Agent: "previous", Model: "previous-model", ReasoningEffort: new("low")}},
		OutputTrace:       []json.RawMessage{json.RawMessage(`{"id":"trace"}`)}, TokenUsage: &harness.TokenUsage{TotalTokens: 5}, ResponseID: "response-1",
	}
	require.NoError(t, upsertTestTurn(ctx, s, checkpoint))
	entries, err := s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Zero(t, entries[0].ID)
	require.True(t, entries[0].Active)
	require.Equal(t, "turn:main:turn-1", entries[0].Key)
	require.NotEmpty(t, entries[0].Revision)
	require.Equal(t, "turn-1", entries[0].Entry.TurnID)
	require.Len(t, entries[0].Entry.ReplayInput, len(checkpoint.ReplayInput))

	for i, raw := range checkpoint.ReplayInput {
		require.JSONEq(t, string(raw), string(entries[0].Entry.ReplayInput[i]))
	}

	require.Equal(t, checkpoint.ReplayAttribution, entries[0].Entry.ReplayAttribution)
	require.Equal(t, checkpoint.ReasoningEffort, entries[0].Entry.ReasoningEffort)
	require.Equal(t, checkpoint.DisplayModel, entries[0].Entry.Model)
	require.Equal(t, checkpoint.Agent, entries[0].Entry.Agent)
	require.Equal(t, checkpoint.TokenUsage, entries[0].Entry.TokenUsage)
	require.Len(t, entries[0].Entry.OutputTrace, 1)
	require.JSONEq(t, string(checkpoint.OutputTrace[0]), string(entries[0].Entry.OutputTrace[0]))
	require.Equal(t, checkpoint.ResponseID, entries[0].Entry.ResponseID)
	require.False(t, entries[0].Entry.Timestamp.IsZero())
	initialRevision := entries[0].Revision

	// Legacy saved rows must not hide a checkpoint merely because their text matches.
	legacy := testSessionEntry("hello", "partial")
	legacyID, err := s.AppendEntryID(ctx, "main", legacy)
	require.NoError(t, err)
	entries, err = s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.True(t, entries[0].Active)
	require.Equal(t, legacyID, entries[1].ID)

	saved := testSessionEntry("hello", "complete")
	saved.TurnID = checkpoint.TurnID
	tx, err := s.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	id, err := appendSessionEntryDB(ctx, tx, "main", saved)
	require.NoError(t, err)
	uncommitted, err := s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, uncommitted, 2)
	require.True(t, uncommitted[0].Active, "uncommitted saved state must not leak into the snapshot")
	require.NoError(t, tx.Commit())

	entries, err = s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, entries, 2, "save/clear overlap must not duplicate a logical turn")
	require.Equal(t, id, entries[1].ID)
	require.Equal(t, *saved, entries[1].Entry)
	require.Equal(t, "turn:main:turn-1", entries[1].Key)
	require.False(t, entries[1].Active)
	require.NotEqual(t, initialRevision, entries[1].Revision)
	// A producer's identical turn ID must not replace the destination's own turn.
	recordTestConversations(t, s, "main", map[string]ConversationKind{"main": KindWebChat, "producer": KindCronRun})
	_, err = s.db.ExecContext(ctx, `UPDATE conversations SET reports_to = 'main' WHERE conversation_id = 'producer'`)
	require.NoError(t, err)
	producerID, err := s.AppendEntryID(ctx, "producer", saved)
	require.NoError(t, err)
	withProducer, err := s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, withProducer, 3)
	require.Equal(t, "turn:producer:turn-1", withProducer[2].Key)
	require.Equal(t, "producer", withProducer[2].SourceConversationID)
	require.True(t, withProducer[2].Synced)

	// A running hidden run that reports here shows live after the history, under the key its saved entry takes.
	hidden := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "hidden", false)
	hidden.SyncDestination = "main"
	require.NoError(t, startTurnDB(ctx, s.db, "producer-live", "producer", hidden))
	live, err := s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, live, 4)
	require.Equal(t, "turn:producer:producer-live", live[3].Key)
	require.True(t, live[3].Active)
	require.True(t, live[3].Synced)
	require.Equal(t, "producer", live[3].SourceConversationID)

	page, err := s.ObserveTranscript(ctx, "main", 0, producerID+1000, nil)
	require.NoError(t, err)
	require.Len(t, page, 3, "older pages never show a live hidden turn")

	copied := testSessionEntry("hidden", "done")
	copied.TurnID = "producer-live"
	_, err = s.AppendEntryID(ctx, "producer", copied)
	require.NoError(t, err)
	synced, err := s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, synced, 4, "the run's saved entry replaces the live turn")
	require.Equal(t, live[3].Key, synced[3].Key)
	require.False(t, synced[3].Active)

	_, err = s.db.ExecContext(ctx, `DELETE FROM turns WHERE id = 'producer-live'`)
	require.NoError(t, err)

	_, err = s.db.ExecContext(ctx, `DELETE FROM entries WHERE conversation_id = 'producer'`)
	require.NoError(t, err)
	require.NoError(t, clearTestTurn(ctx, s, checkpoint.TurnID))

	checkpoint.TurnID = "failed-turn"
	require.NoError(t, upsertTestTurn(ctx, s, checkpoint))
	require.NoError(t, terminateTestTurn(ctx, s, checkpoint.TurnID, protocol.TerminalFailed))
	require.Empty(t, testTurnStepKeys(t, s, "main"), "a failed turn leaves no step")

	checkpoint.TurnID = "new-turn"
	require.NoError(t, upsertTestTurn(ctx, s, checkpoint))
	entries, err = s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, entries, 4)
	require.Equal(t, "failed-turn", entries[2].Entry.TurnID)
	require.Equal(t, protocol.TerminalFailed, entries[2].Terminal)
	require.False(t, entries[2].Active)
	require.Equal(t, "new-turn", entries[3].Entry.TurnID)
	require.True(t, entries[3].Active)

	require.Equal(t, []string{"new-turn"}, runningTestTurns(t, s))

	manifest := make(map[string]string, len(entries))
	for _, entry := range entries {
		manifest[entry.Key] = entry.Revision
	}

	unchanged, err := s.ObserveTranscript(ctx, "main", 0, 0, manifest)
	require.NoError(t, err)
	require.Len(t, unchanged, len(entries))

	for i, entry := range unchanged {
		require.Equal(t, harness.SessionEntry{}, entry.Entry, "unchanged payload must not be loaded")
		entries[i].Entry = harness.SessionEntry{}
		require.Equal(t, entries[i], entry, "all inventory metadata must survive")
	}

	checkpoint.ResponseID = "edited-response"
	require.NoError(t, upsertTestTurn(ctx, s, checkpoint))
	edited, err := s.ObserveTranscript(ctx, "main", 0, 0, manifest)
	require.NoError(t, err)
	require.Len(t, edited, 4)
	require.Equal(t, "edited-response", edited[3].Entry.ResponseID)
	require.NotEqual(t, manifest[edited[3].Key], edited[3].Revision)
	require.Equal(t, harness.SessionEntry{}, edited[2].Entry)
	require.NoError(t, clearTestTurn(ctx, s, checkpoint.TurnID))
	remaining, err := s.ObserveTranscript(ctx, "main", 0, 0, manifest)
	require.NoError(t, err)
	require.Len(t, remaining, 3)

	for _, entry := range remaining {
		require.NotEqual(t, "turn:main:new-turn", entry.Key)
		require.Equal(t, harness.SessionEntry{}, entry.Entry)
	}

	later := testSessionEntry("later", "complete")
	later.TurnID = "new-turn"
	laterID, err := s.AppendEntryID(ctx, "main", later)
	require.NoError(t, err)

	checkpoint.TurnID = "failed-turn"
	require.NoError(t, upsertTestTurn(ctx, s, checkpoint))
	afterCompletion, err := s.ObserveTranscript(ctx, "main", 0, 0, manifest)
	require.NoError(t, err)
	require.Len(t, afterCompletion, 4)
	require.Equal(t, "turn:main:failed-turn", afterCompletion[2].Key, "a later saved turn must not move the retained failure to the tail")
	require.Equal(t, laterID, afterCompletion[3].ID)
	require.Equal(t, *later, afterCompletion[3].Entry)

	require.Empty(t, runningTestTurns(t, s))

	empty, err := s.ObserveTranscript(ctx, "unknown", 0, 0, nil)
	require.NoError(t, err)
	require.Empty(t, empty)

	_, err = s.ObserveTranscript(ctx, " \t", 0, 0, nil)
	require.ErrorContains(t, err, "conversation ID is required")

	checkpoint.TurnID = "still-active"
	require.NoError(t, upsertTestTurn(ctx, s, checkpoint))
	deleted, err := s.DeleteSession(ctx, "main")
	require.NoError(t, err)
	require.EqualValues(t, 3, deleted)

	remaining, err = s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, remaining, 1, "history deletion removes retained terminal detail, not live recovery state")
	require.True(t, remaining[0].Active)
	require.Equal(t, "still-active", remaining[0].Entry.TurnID)
}

func TestTranscriptPages(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	ids := make([]int64, 5)
	appendRow := func(i int) {
		var err error

		ids[i], err = s.AppendEntryID(ctx, "main", testSessionEntry("prompt", "reply"))
		require.NoError(t, err)
	}
	keys := func(entries []ObservedSessionEntry) []string {
		keys := make([]string, 0, len(entries))
		for _, entry := range entries {
			keys = append(keys, entry.Key)
		}

		return keys
	}
	text := func(id int64) string { return strconv.FormatInt(id, 10) }

	appendRow(0)

	running := &testCheckpoint{TurnID: "running", ConversationKey: "main", ReplayInput: testSessionEntry("hello", "partial").ReplayInput}
	require.NoError(t, upsertTestTurn(ctx, s, running))
	appendRow(1)

	failed := &testCheckpoint{TurnID: "failed", ConversationKey: "main", ReplayInput: testSessionEntry("hello", "partial").ReplayInput}
	require.NoError(t, upsertTestTurn(ctx, s, failed))
	require.NoError(t, terminateTestTurn(ctx, s, failed.TurnID, protocol.TerminalFailed))

	for i := 2; i < len(ids); i++ {
		appendRow(i)
	}

	start, oldest, err := s.TranscriptPage(ctx, "main", 0, 2)
	require.NoError(t, err)
	require.Equal(t, [2]int64{ids[3], ids[0]}, [2]int64{start, oldest})
	tail, err := s.ObserveTranscript(ctx, "main", start, 0, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"turn:main:running", text(ids[3]), text(ids[4])}, keys(tail), "the tail keeps running turns even when they started earlier")

	start, _, err = s.TranscriptPage(ctx, "main", ids[3], 2)
	require.NoError(t, err)
	require.Equal(t, ids[1], start)
	page, err := s.ObserveTranscript(ctx, "main", start, ids[3], nil)
	require.NoError(t, err)
	require.Equal(t, []string{text(ids[1]), "turn:main:failed", text(ids[2])}, keys(page), "settled pages keep finished checkpoints in place")

	start, _, err = s.TranscriptPage(ctx, "main", ids[1], 2)
	require.NoError(t, err)
	require.Zero(t, start, "fewer remaining entries than the limit start at the beginning")
	page, err = s.ObserveTranscript(ctx, "main", start, ids[1], nil)
	require.NoError(t, err)
	require.Equal(t, []string{text(ids[0])}, keys(page))

	start, oldest, err = s.TranscriptPage(ctx, "unknown", 0, 50)
	require.NoError(t, err)
	require.Equal(t, [2]int64{0, 0}, [2]int64{start, oldest})

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	_, _, err = s.TranscriptPage(canceled, "main", 0, 50)
	require.ErrorContains(t, err, "read transcript page")
}

func TestTranscriptChangesReachSyncDestination(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()

	next, stop := iter.Pull2(s.Changes(ctx, "destination"))
	defer stop()

	_, err, _ := next()
	require.NoError(t, err, "initial refresh")

	recordTestConversations(t, s, "main", map[string]ConversationKind{"producer": KindCronRun, "destination": KindWebChat})

	hidden := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "hidden", false)
	hidden.SyncDestination = "destination"
	require.NoError(t, startTurnDB(ctx, s.db, "hidden-turn", "producer", hidden))
	require.NoError(t, conversationJournal{store: s, conversationID: "producer", log: slog.New(slog.DiscardHandler)}.SaveTrace(ctx, "hidden-turn", []json.RawMessage{json.RawMessage(`{"id":"progress"}`)}))

	for range 2 {
		change, err, ok := next()
		require.True(t, ok)
		require.NoError(t, err)
		require.Equal(t, "destination", change.ConversationID)
	}
}

func TestTranscriptChanges(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	conn, err := s.db.Conn(ctx)

	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()

	var channel string
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT 'rocketclaw_transcript_' || md5(current_schema())`).Scan(&channel))
	_, err = conn.ExecContext(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize())
	require.NoError(t, err)

	// An open transaction and a rolled-back one cannot emit transcript signals.
	tx, err := s.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ('rolled-back', '{"replay_input":["SECRET"]}', '')`)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `SELECT pg_notify($1, 'barrier')`, channel)
	require.NoError(t, err)
	require.NoError(t, conn.Raw(func(raw any) error {
		n, err := raw.(*stdlib.Conn).Conn().WaitForNotification(ctx)
		require.NoError(t, err)
		require.Equal(t, "barrier", n.Payload)

		return nil
	}))
	require.NoError(t, tx.Rollback())

	// The same transaction ID is opaque, shared by its row changes, and content-free. A hidden
	// run's changes also reach the chat it reports to.
	_, err = s.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, reports_to, kind, listed) VALUES ('destination', 'main', '', NULL, 'web_chat', TRUE), ('source', 'main', '', 'destination', 'cron_run', FALSE)`)
	require.NoError(t, err)

	tx, err = s.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	var revision string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_current_xact_id()::text`).Scan(&revision))

	var sourceID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ('source', '{"replay_input":["SECRET"]}', '') RETURNING id`).Scan(&sourceID))
	_, err = tx.ExecContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ('destination', jsonb_build_object('answer', $1::bigint)::json, '')`, sourceID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.NoError(t, conn.Raw(func(raw any) error {
		for _, id := range []string{"source", "destination"} {
			n, err := raw.(*stdlib.Conn).Conn().WaitForNotification(ctx)
			require.NoError(t, err)

			var change protocol.ConversationChange
			require.NoError(t, json.Unmarshal([]byte(n.Payload), &change))
			require.Equal(t, protocol.ConversationChange{ConversationID: id, Revision: revision}, change)
			require.JSONEq(t, `{"conversationId":"`+id+`","revision":"`+revision+`"}`, n.Payload)
		}

		return nil
	}))

	_, err = s.db.ExecContext(ctx, `UPDATE entries SET entry_json = (entry_json::jsonb || '{"response_id":"edited"}'::jsonb)::json WHERE conversation_id = 'destination'`)
	require.NoError(t, err)
	require.NoError(t, conn.Raw(func(raw any) error {
		n, err := raw.(*stdlib.Conn).Conn().WaitForNotification(ctx)
		require.NoError(t, err)

		var change protocol.ConversationChange
		require.NoError(t, json.Unmarshal([]byte(n.Payload), &change))
		require.Equal(t, "destination", change.ConversationID)
		require.NotEmpty(t, change.Revision)

		return nil
	}))

	// Deleting a run's history invalidates it and the chat it reports to.
	_, err = s.DeleteSession(ctx, "source")
	require.NoError(t, err)
	require.NoError(t, conn.Raw(func(raw any) error {
		for _, id := range []string{"source", "destination"} {
			n, err := raw.(*stdlib.Conn).Conn().WaitForNotification(ctx)
			require.NoError(t, err)

			var change protocol.ConversationChange
			require.NoError(t, json.Unmarshal([]byte(n.Payload), &change))
			require.Equal(t, id, change.ConversationID)
			require.NotEmpty(t, change.Revision)
		}

		return nil
	}))

	for change, err := range s.Changes(ctx, "main") {
		require.NoError(t, err)
		require.Equal(t, protocol.ConversationChange{ConversationID: "main"}, change)

		break
	}

	require.Equal(t, 1, s.db.Stats().InUse, "initial consumer stop releases the listener connection")

	// Registration precedes the initial yield, so an append from that yield is seen.
	ctxChanges, cancel := context.WithCancel(ctx)
	defer cancel()

	next, stop := iter.Pull2(s.Changes(ctxChanges, "main"))
	defer stop()

	change, err, ok := next()
	require.True(t, ok)
	require.NoError(t, err)
	require.Equal(t, protocol.ConversationChange{ConversationID: "main"}, change)
	other := newTestSessionService(t)
	_, err = other.AppendEntryID(ctx, "main", testSessionEntry("other schema", "SECRET"))
	require.NoError(t, err)
	_, err = s.AppendEntryID(ctx, "unrelated", testSessionEntry("other conversation", "SECRET"))
	require.NoError(t, err)
	tx, err = s.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	_, err = appendSessionEntryDB(ctx, tx, "main", testSessionEntry("hello", "SECRET"))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_current_xact_id()::text`).Scan(&revision))
	require.NoError(t, tx.Commit())

	change, err, ok = next()
	require.True(t, ok)
	require.NoError(t, err)
	require.Equal(t, "main", change.ConversationID)
	require.Equal(t, revision, change.Revision, "another schema's same-ID notification must not leak")

	checkpoint := &testCheckpoint{TurnID: "active", ConversationKey: "main"}
	require.NoError(t, upsertTestTurn(ctx, s, checkpoint))

	_, err, ok = next()
	require.True(t, ok)
	require.NoError(t, err)
	require.NoError(t, terminateTestTurn(ctx, s, checkpoint.TurnID, protocol.TerminalFailed))

	_, err, ok = next()
	require.True(t, ok)
	require.NoError(t, err)
	require.NoError(t, clearTestTurn(ctx, s, checkpoint.TurnID))

	_, err, ok = next()
	require.True(t, ok)
	require.NoError(t, err)
	cancel()

	_, err, ok = next()
	require.NoError(t, err)
	require.False(t, ok)
	stop()

	for change, err := range s.Changes(ctx, "main") {
		require.NoError(t, err)

		if change.Revision == "" {
			_, err := s.AppendEntryID(ctx, "main", testSessionEntry("after reopen", "public"))
			require.NoError(t, err)

			continue
		}

		break
	}
	// No pooled connection may retain LISTEN state after iteration ends.
	require.Equal(t, 1, s.db.Stats().InUse, "only the explicit test listener remains")

	pooled := make([]*sql.Conn, 0, s.db.Stats().Idle)
	defer func() {
		for _, conn := range pooled {
			require.NoError(t, conn.Close())
		}
	}()

	for range s.db.Stats().Idle {
		conn, err := s.db.Conn(ctx)
		require.NoError(t, err)

		pooled = append(pooled, conn)

		var listening bool
		require.NoError(t, conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_listening_channels())`).Scan(&listening))
		require.False(t, listening)
	}
}

func TestTranscriptChangesReportsListenerFailures(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"missing schema", "malformed signal", "disconnected"} {
		t.Run(scenario, func(t *testing.T) {
			s := newTestSessionService(t)
			ctx := t.Context()

			var channel string
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT 'rocketclaw_transcript_' || md5(current_schema())`).Scan(&channel))

			if scenario == "missing schema" {
				s.db.SetMaxOpenConns(1)
				_, err := s.db.ExecContext(ctx, `SET search_path = nonexistent`)
				require.NoError(t, err)
			}

			next, stop := iter.Pull2(s.Changes(ctx, "main"))
			defer stop()

			_, err, ok := next()
			require.True(t, ok)

			if scenario == "missing schema" {
				require.ErrorContains(t, err, "listen for transcript changes")
			} else {
				require.NoError(t, err)

				if scenario == "malformed signal" {
					_, err = s.db.ExecContext(ctx, `SELECT pg_notify($1, '{')`, channel)
					require.NoError(t, err)
				} else {
					var terminated bool
					require.NoError(t, s.db.QueryRowContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query = $1`, "LISTEN "+pgx.Identifier{channel}.Sanitize()).Scan(&terminated))
					require.True(t, terminated)
				}

				_, err, ok = next()
				require.True(t, ok)

				if scenario == "malformed signal" {
					require.ErrorContains(t, err, "decode transcript change")
				} else {
					require.ErrorContains(t, err, "wait for transcript change")
				}
			}

			_, err, ok = next()
			require.NoError(t, err)
			require.False(t, ok)
			require.Zero(t, s.db.Stats().InUse, "failed listeners must release their connection")
		})
	}
}

func TestTranscriptObservationReportsUnavailableStorage(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	for _, err := range s.Changes(ctx, "main") {
		require.NoError(t, err)
		t.Fatal("cancellation must end observation without yielding an error")
	}

	require.NoError(t, s.Stop())
	_, err := s.ObserveTranscript(t.Context(), "main", 0, 0, nil)
	require.ErrorContains(t, err, "database is closed")

	journal := conversationJournal{store: s, conversationID: "main", log: slog.New(slog.DiscardHandler)}
	require.ErrorContains(t, journal.SaveTrace(t.Context(), "turn", nil), "save turn trace")
	require.ErrorContains(t, journal.Save(t.Context(), "turn", json.RawMessage(`{}`)), "save turn step")
	_, _, err = journal.Load(t.Context(), "turn")
	require.ErrorContains(t, err, "load turn step")
	_, err = s.finishTurn(t.Context(), "turn", &turnFinish{store: newSessionStore("main", s), outbound: protocol.NewOutboundMessage("main", "")})
	require.ErrorContains(t, err, "begin turn finish")

	count := 0
	for _, err := range s.Changes(t.Context(), "main") {
		count++

		require.ErrorContains(t, err, "connect transcript listener")
	}

	require.Equal(t, 1, count, "an outage must not look like empty successful history")
}

func TestActiveTurnOutputTracePersistence(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()

	var logs lockedBuffer

	journal := conversationJournal{store: s, conversationID: "main", log: slog.New(slog.NewJSONHandler(&logs, nil))}
	checkpoint := &testCheckpoint{TurnID: "turn-1", ConversationKey: "main", ReplayInput: testSessionEntry("hello", "").ReplayInput,
		OutputTrace: []json.RawMessage{json.RawMessage(`{"type":"reasoning","text":"PRIVATE"}`)}}
	require.NoError(t, upsertTestTurn(ctx, s, checkpoint))

	next, stop := iter.Pull2(s.Changes(ctx, "main"))
	defer stop()

	_, err, ok := next()
	require.True(t, ok)
	require.NoError(t, err)

	trace := append(slices.Clone(checkpoint.OutputTrace), json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"response-1/item-1","kind":"text","state":"working","text":"hello","agent":"main","model":"model"}}`))
	require.NoError(t, journal.SaveTrace(ctx, checkpoint.TurnID+"/retry/1", trace), "retry turns report progress on the request row")
	require.NotContains(t, logs.String(), `"event":"operation_snapshot"`, "text is not a tool operation")

	change, err, ok := next()
	require.True(t, ok)
	require.NoError(t, err)
	require.NotEmpty(t, change.Revision)
	raw, err := json.Marshal(change)
	require.NoError(t, err)
	require.JSONEq(t, `{"conversationId":"main","revision":"`+change.Revision+`"}`, string(raw), "notifications contain metadata only")

	entries, err := s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.True(t, entries[0].Active)
	require.Equal(t, []harness.PublicProgress{{ID: "response-1/item-1", Kind: harness.PublicProgressText, State: harness.PublicProgressWorking, Text: "hello", Agent: "main", Model: "model"}}, harness.PublicProgressFromTrace(entries[0].Entry.OutputTrace))
	require.Len(t, entries[0].Entry.ReplayInput, len(checkpoint.ReplayInput), "the transcript reads the journaled turn record")

	require.NoError(t, journal.SaveTrace(ctx, checkpoint.TurnID, trace))
	unchanged, err := s.ObserveTranscript(ctx, "main", 0, 0, map[string]string{entries[0].Key: entries[0].Revision})
	require.NoError(t, err)
	require.Equal(t, harness.SessionEntry{}, unchanged[0].Entry)
	require.NoError(t, terminateTestTurn(ctx, s, checkpoint.TurnID, protocol.TerminalStopped))
	require.Empty(t, runningTestTurns(t, s), "a stopped turn never resumes")

	_, err, ok = next()
	require.True(t, ok)
	require.NoError(t, err)
	require.NoError(t, journal.SaveTrace(ctx, checkpoint.TurnID, nil))
	entries, err = s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Equal(t, protocol.TerminalStopped, entries[0].Terminal)
	require.Len(t, entries[0].Entry.OutputTrace, 2, "late writes cannot alter terminal progress")
	require.Len(t, entries[0].Entry.ReplayInput, len(checkpoint.ReplayInput), "a stopped turn keeps its record for the transcript")
	require.NoError(t, clearTestTurn(ctx, s, checkpoint.TurnID))
	require.NoError(t, journal.SaveTrace(ctx, checkpoint.TurnID, trace))
	entries, err = s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Empty(t, entries, "trace-only writes cannot recreate a cleared row")

	trace = []json.RawMessage{
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"call-a","kind":"tool","state":"working","text":"private-arguments"}}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"call-b","parent_id":"call-a","kind":"tool","state":"completed","text":"private-result"}}`),
	}
	require.NoError(t, journal.SaveTrace(ctx, checkpoint.TurnID, trace))
	require.Equal(t, 2, strings.Count(logs.String(), `"event":"operation_snapshot"`))

	for _, field := range []string{`"operation_id":"call-a"`, `"operation_id":"call-b"`, `"state":"working"`, `"state":"completed"`, `"boundary":"journal_observation"`, `"observation_elapsed_ms":`} {
		require.Contains(t, logs.String(), field)
	}

	require.NotContains(t, logs.String(), "private-")
	require.NotContains(t, logs.String(), `"duration_ms":`)
}

func TestCodeModeCallsSavedOnTheirOwn(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	journal := conversationJournal{store: s, conversationID: "main", log: slog.New(slog.DiscardHandler)}
	progress := json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"exec","kind":"tool","state":"working","text":"execute"}}`)
	require.NoError(t, upsertTestTurn(ctx, s, &testCheckpoint{TurnID: "turn-1", ConversationKey: "main", ReplayInput: testSessionEntry("hello", "").ReplayInput, OutputTrace: []json.RawMessage{progress}}))

	next, stop := iter.Pull2(s.Changes(ctx, "main"))
	defer stop()

	_, err, ok := next()
	require.True(t, ok)
	require.NoError(t, err)

	save := func(turnID, host, status string) {
		t.Helper()

		key := turnID + "/call/exec/host/" + host
		require.NoError(t, journal.SaveCodeModeCall(ctx, turnID, key, json.RawMessage(`{"type":"rocketcode_code_mode_call","parent_call_id":"exec","key":"`+key+`","name":"bash","status":"`+status+`"}`)))
	}
	calls := func(trace []json.RawMessage) []string {
		records := harness.CodeModeCallsFromTrace(trace)
		out := make([]string, 0, len(records))

		for _, call := range records {
			out = append(out, call.Key+" "+string(call.Status))
		}

		return out
	}

	save("turn-1", "10", "running")

	_, err, ok = next()
	require.True(t, ok, "a record save notifies the chat as a trace save does")
	require.NoError(t, err)

	before, err := s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	save("turn-1", "2", "running")
	save("turn-1", "10", "ok")
	save("turn-1/retry/1", "0", "ok")

	saved, err := journal.CodeModeCalls(ctx, "turn-1")
	require.NoError(t, err)
	require.Equal(t, []string{"turn-1/call/exec/host/10 ok", "turn-1/call/exec/host/2 running"}, calls(saved), "a turn reads its own records in the order first saved")

	entries, err := s.ObserveTranscript(ctx, "main", 0, 0, map[string]string{before[0].Key: before[0].Revision})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.JSONEq(t, string(progress), string(entries[0].Entry.OutputTrace[0]), "a record changes the turn's revision and follows its trace")
	require.Equal(t, []string{"turn-1/call/exec/host/10 ok", "turn-1/call/exec/host/2 running", "turn-1/retry/1/call/exec/host/0 ok"}, calls(entries[0].Entry.OutputTrace))

	var trace string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT output_trace_json FROM turns WHERE id = 'turn-1'`).Scan(&trace))
	require.JSONEq(t, `[`+string(progress)+`]`, trace, "records never enter the row's trace")

	require.NoError(t, terminateTestTurn(ctx, s, "turn-1", protocol.TerminalStopped))
	require.Empty(t, testTurnStepKeys(t, s, "main"), "a stopped turn leaves no step")
	save("turn-1", "3", "running")

	entries, err = s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, calls(entries[0].Entry.OutputTrace), 3, "a stopped turn keeps its records and takes no more")

	require.NoError(t, clearTestTurn(ctx, s, "turn-1"))
	save("turn-1", "4", "running")

	saved, err = journal.CodeModeCalls(ctx, "turn-1")
	require.NoError(t, err)
	require.Empty(t, saved, "a cleared row takes its records with it and cannot be recreated by one")
}

// A running turn's save writes its step and only its changed items, each once, and the live
// transcript and a resumed turn read the record those saves leave.
func TestJournalSavesTurnItemsOnce(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	journal := conversationJournal{store: s, conversationID: "main", log: slog.New(slog.DiscardHandler)}
	require.NoError(t, startTurnDB(ctx, s.db, "turn-1", "main", protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "seed", true)))

	item := func(text string) json.RawMessage {
		return json.RawMessage(`{"type":"message","role":"user","content":"` + text + `"}`)
	}
	save := func(responseID string, lists ...harness.TurnItems) {
		t.Helper()

		step, err := json.Marshal(map[string]harness.SessionEntry{"record": {Version: 1, Type: "turn", TurnID: "turn-1", ResponseID: responseID}})
		require.NoError(t, err)
		require.NoError(t, journal.SaveTurn(ctx, "turn-1", step, lists))
	}
	replay := func() []string {
		t.Helper()

		entries, err := s.ObserveTranscript(ctx, "main", 0, 0, nil)
		require.NoError(t, err)
		require.Len(t, entries, 1)

		items, err := journal.Items(ctx, "turn-1", harness.TurnReplayInput)
		require.NoError(t, err)
		require.Equal(t, entries[0].Entry.ReplayInput, items, "a resumed turn reads the replay the transcript shows")

		texts := make([]string, len(items))
		for i := range items {
			texts[i] = string(items[i])
		}

		return texts
	}

	save("", harness.TurnItems{List: harness.TurnReplayInput, Items: []json.RawMessage{item("a"), item("b <c>")}}, harness.TurnItems{List: harness.TurnTrace, Items: []json.RawMessage{json.RawMessage(`{"id":"p"}`)}})
	require.Equal(t, []string{string(item("a")), string(item("b <c>"))}, replay(), "items keep their bytes")

	save("resp-1", harness.TurnItems{List: harness.TurnReplayInput, From: 2, Items: []json.RawMessage{item("c")}}, harness.TurnItems{List: harness.TurnTrace, From: 1})
	require.Equal(t, []string{string(item("a")), string(item("b <c>")), string(item("c"))}, replay())
	require.Equal(t, []string{"turn-1", "turn-1/replay_input/0000000000", "turn-1/replay_input/0000000001", "turn-1/replay_input/0000000002", "turn-1/trace/0000000000"}, testTurnStepKeys(t, s, "main"))

	// Compaction rewrites the replay from its start; items past its new end go.
	save("resp-1", harness.TurnItems{List: harness.TurnReplayInput, Items: []json.RawMessage{item("summary")}}, harness.TurnItems{List: harness.TurnTrace})
	require.Equal(t, []string{string(item("summary"))}, replay())
	require.Equal(t, []string{"turn-1", "turn-1/replay_input/0000000000"}, testTurnStepKeys(t, s, "main"))
}

// testCheckpoint seeds a running turn as a taken request records it: the row
// holds live trace and the journal root step holds the turn's record.
type testCheckpoint struct {
	TurnID, ConversationKey, Agent, Model, DisplayModel string
	ReasoningEffort                                     *string
	ReplayInput                                         []json.RawMessage
	ReplayAttribution                                   []harness.ReplayAttribution
	OutputTrace                                         []json.RawMessage
	TokenUsage                                          *harness.TokenUsage
	ResponseID                                          string
}

func upsertTestTurn(ctx context.Context, s *SessionService, c *testCheckpoint) error {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) > 0 FROM turns WHERE id = $1`, c.TurnID).Scan(&exists); err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	if !exists {
		inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "seed", true)
		if err := startTurnDB(ctx, s.db, c.TurnID, c.ConversationKey, inbound); err != nil {
			return fmt.Errorf("seed test turn: %w", err)
		}
	}

	record := harness.SessionEntry{Version: 1, Type: "turn", TurnID: c.TurnID, Agent: c.Agent, Model: c.DisplayModel, ReasoningEffort: c.ReasoningEffort, ReplayAttribution: c.ReplayAttribution, TokenUsage: c.TokenUsage, ReplayInput: c.ReplayInput, ResponseID: c.ResponseID}
	if err := saveTestTurn(ctx, s, c.ConversationKey, c.TurnID, nil, &record); err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	return conversationJournal{store: s, conversationID: c.ConversationKey, log: slog.New(slog.DiscardHandler)}.SaveTrace(ctx, c.TurnID, c.OutputTrace)
}

// saveTestTurn journals a turn's record as the harness saves it: its step, and its replay input as the turn's list.
func saveTestTurn(ctx context.Context, s *SessionService, conversationID, turnID string, applied []string, record *harness.SessionEntry) error {
	header := *record
	header.ReplayInput = nil

	data, err := json.Marshal(struct {
		AppliedInputIDs []string             `json:"applied_input_ids,omitempty"`
		Record          harness.SessionEntry `json:"record"`
	}{applied, header})
	if err != nil {
		return fmt.Errorf("encode test turn step: %w", err)
	}

	journal := conversationJournal{store: s, conversationID: conversationID, log: slog.New(slog.DiscardHandler)}

	return journal.SaveTurn(ctx, turnID, data, []harness.TurnItems{{List: harness.TurnReplayInput, Items: record.ReplayInput}})
}

func clearTestTurn(ctx context.Context, s *SessionService, turnID string) error {
	_, err := s.db.ExecContext(ctx, `WITH turn AS (DELETE FROM turns WHERE id = $1 RETURNING conversation_id)
DELETE FROM steps s USING turn t WHERE s.conversation_id = t.conversation_id AND (s.key = $1 OR starts_with(s.key, $1 || '/'))`, turnID)
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	return nil
}

func terminateTestTurn(ctx context.Context, s *SessionService, turnID string, terminal protocol.Terminal) error {
	var conversationID string
	if err := s.db.QueryRowContext(ctx, `SELECT conversation_id FROM turns WHERE id = $1`, turnID).Scan(&conversationID); err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	if _, err := s.finishTurn(ctx, turnID, &turnFinish{store: newSessionStore(conversationID, s), outbound: protocol.NewOutboundMessage(conversationID, ""), terminal: terminal}); err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	return s.closeTurn(ctx, turnID)
}

func runningTestTurns(t *testing.T, s *SessionService) []string {
	t.Helper()

	ids, err := queryStrings(t.Context(), s.db, `SELECT id FROM turns WHERE phase NOT IN ($1, $2) AND takes_lane ORDER BY id`, "running test turns", turnWaiting, turnDone)
	require.NoError(t, err)

	return ids
}

func TestTranscriptObservationReportsUnreadableStoredEntries(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ('main', '{"type":"turn","output_trace":1}', '')`)
	require.NoError(t, err)
	_, err = s.ObserveTranscript(t.Context(), "main", 0, 0, nil)
	require.ErrorContains(t, err, "decode transcript entry")
}

// A failed or running turn's record past jsonb's limit fails History, and a failed one its delegations
// and undo's lookup, instead of being expanded in Postgres, as the jsonb records before it failed; a
// smaller one still reads.
func TestOversizedTurnRecordFailsReads(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()

	// The live conversations' turns are still running.
	for id, text := range map[string]string{"small": "hi", "large": strings.Repeat("x", 2000), "small-live": "hi", "live": strings.Repeat("x", 2000)} {
		require.NoError(t, s.UpsertThread(id, ThreadState{Agent: "main"}))
		seedActiveTurn(t, s, id, id, testReplayEntry(`{"type":"message","role":"user","content":"`+text+`"}`))

		if !strings.HasSuffix(id, "live") {
			endTestTurn(t, s, id, id, protocol.TerminalFailed)
		}
	}

	// A record past the real limit, 256 MB, is too heavy for a test; this one's limit is 1000 bytes.
	_, err := s.db.ExecContext(ctx, `DO $$ BEGIN EXECUTE replace(pg_get_functiondef('readable_replay(bigint)'::regprocedure), '268435455', '1000'); END $$`)
	require.NoError(t, err)

	for _, id := range []string{"small", "small-live"} {
		view, err := s.ObserveHistory(ctx, id, "", 0, 0, 0, &HistoryInventory{})
		require.NoError(t, err)
		require.Len(t, view.Entries, 1)
		require.Len(t, view.Entries[0].Entry.ReplayInput, 1)
	}

	// Each fails with the error jsonb's limit raised before.
	const jsonbLimit = "ERROR: string too long to represent as jsonb string (SQLSTATE 54000)"

	for _, id := range []string{"live", "large"} {
		_, err = s.ObserveHistory(ctx, id, "", 0, 0, 0, &HistoryInventory{})
		require.EqualError(t, err, "query transcript: "+jsonbLimit)
	}

	_, err = s.Delegations(ctx, "large", "", 0, 0)
	require.ErrorContains(t, err, jsonbLimit)
	_, _, err = revertInputDB(ctx, s.db, "large", "input")
	require.ErrorContains(t, err, jsonbLimit)
}

// finishTestRunTurn runs one turn of the hidden run reporting to chat that saves entry.
func finishTestRunTurn(t *testing.T, s *SessionService, run, chat, turnID string, entry *harness.SessionEntry) {
	t.Helper()

	inbound := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "work", true)
	inbound.ConversationID, inbound.SyncDestination = run, chat
	require.NoError(t, startTurnDB(t.Context(), s.db, turnID, run, inbound))
	_, err := s.finishTurn(t.Context(), turnID, &turnFinish{store: newSessionStore(run, s), entries: []harness.SessionEntry{*entry}, outbound: protocol.NewOutboundMessage(run, "")})
	require.NoError(t, err)
	require.NoError(t, s.closeTurn(t.Context(), turnID))
}

func observedEntryTexts(t *testing.T, s *SessionService, conversationID string) []string {
	t.Helper()

	entries, err := s.ObserveEntries(t.Context(), conversationID)
	require.NoError(t, err)

	var texts []string

	for i := range entries {
		for _, raw := range entries[i].Entry.ReplayInput {
			var item struct{ Type, Content string }
			require.NoError(t, json.Unmarshal(raw, &item))
			texts = append(texts, item.Type+":"+item.Content)
		}
	}

	return texts
}

// AE1, AE3: a hidden run's turns wait in its chat's lane. The chat and its agent see the run's
// history without its compaction; the run's agent sees only its own entries, compaction included.
// History, search, and undo give the run's messages the same IDs, and a failed run turn shows only live.
func TestHiddenRunViews(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()

	const chat, run = "ticket-123", "external_mcp:main:ticket-123"

	for _, id := range []string{chat, run} {
		require.NoError(t, s.UpsertThread(id, ThreadState{Agent: "main"}))
	}

	finishTestRunTurn(t, s, run, chat, "run-1", testSessionEntry("mcp question", "mcp answer"))
	_, err := s.AppendEntryID(ctx, chat, testSessionEntry("person follow-up", "person answer"))
	require.NoError(t, err)
	finishTestRunTurn(t, s, run, chat, "run-2", testReplayEntry(
		`{"type":"compaction","content":"summary of earlier work"}`,
		`{"type":"message","role":"user","content":"after compaction"}`,
		`{"type":"message","role":"assistant","content":"compacted answer"}`,
	))

	require.Equal(t, []string{"message:mcp question", "message:mcp answer", "compaction:summary of earlier work", "message:after compaction", "message:compacted answer"},
		observedEntryTexts(t, s, run), "the run's agent sees its own entries, compaction included, and not the person's")
	require.Equal(t, []string{"message:mcp question", "message:mcp answer", "message:person follow-up", "message:person answer", "message:after compaction", "message:compacted answer"},
		observedEntryTexts(t, s, chat), "the chat and its agent see the run's history without its compaction")

	var copies int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM entries WHERE conversation_id = $1`, chat).Scan(&copies))
	require.Equal(t, 1, copies, "nothing is copied into the chat")

	entries, err := s.ObserveEntries(ctx, chat)
	require.NoError(t, err)

	compacted := entries[2].ID
	hits, err := s.SearchMessages(ctx, "after compaction", nil)
	require.NoError(t, err)
	require.Equal(t, []MessageSearchHit{{ConversationID: chat, Role: "user", Text: "after compaction", MessageID: fmt.Sprintf("%d:0", compacted)}}, hits)

	_, eligible, predecessor, floor, err := revertStateDB(ctx, s.db, chat)
	require.NoError(t, err)
	require.True(t, eligible)
	require.Empty(t, predecessor, "undo keeps the run's messages")
	require.Equal(t, fmt.Sprintf("%d:1", compacted), floor, "undo addresses the run's message as history and search do")

	// A person's running turn holds the lane; the run's next turn waits behind it, on the chat's worker.
	require.NoError(t, startTurnDB(ctx, s.db, "person-turn", chat, protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "more", true)))

	waiting := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "next call", true)
	waiting.ConversationID, waiting.SyncDestination = run, chat
	require.NoError(t, startTurnDB(ctx, s.db, "run-3", run, waiting))

	head, ok, err := s.headTurn(ctx, chat)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "person-turn", head.id)

	lane, err := queryStrings(ctx, s.db, `SELECT id FROM (`+laneTurnSQL+`) lane`, "lane turns", chat)
	require.NoError(t, err)
	require.Equal(t, []string{"person-turn", "run-3"}, lane)

	_, ok, err = s.headTurn(ctx, run)
	require.NoError(t, err)
	require.False(t, ok, "the run has no lane of its own")

	live, err := s.ObserveTranscript(ctx, chat, 0, 0, nil)
	require.NoError(t, err)
	require.True(t, slices.ContainsFunc(live, func(entry ObservedSessionEntry) bool { return entry.Key == "turn:"+run+":run-3" && entry.Active }), "the running run turn shows live in its chat")

	endTestTurn(t, s, run, "run-3", protocol.TerminalFailed)

	ended, err := s.ObserveTranscript(ctx, chat, 0, 0, nil)
	require.NoError(t, err)
	require.False(t, slices.ContainsFunc(ended, func(entry ObservedSessionEntry) bool { return entry.Key == "turn:"+run+":run-3" }), "a failed run turn leaves its chat when it ends")
}

// Undo in a chat stops after a hidden run's entries, and a wipe keeps them as undo does, in the chat
// and in the run.
func TestUndoKeepsHiddenRunEntries(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()

	const chat, run = "chat", "one-off-cron:cron/remind.md:20260101T000000Z:run"

	for _, id := range []string{chat, run} {
		require.NoError(t, s.UpsertThread(id, ThreadState{Agent: "main"}))
	}

	_, err := s.AppendEntryID(ctx, chat, testSessionEntry("first question", "first answer"))
	require.NoError(t, err)
	finishTestRunTurn(t, s, run, chat, "run-1", testSessionEntry("reminder prompt", "reminder answer"))
	second, err := s.AppendEntryID(ctx, chat, testSessionEntry("second question", "second answer"))
	require.NoError(t, err)

	entries, err := s.ObserveEntries(ctx, chat)
	require.NoError(t, err)

	_, _, err = stageRevertDB(ctx, s.db, chat, fmt.Sprintf("%d:0", entries[1].ID))
	require.ErrorContains(t, err, "past a message")

	kept := []string{"message:first question", "message:first answer", "message:reminder prompt", "message:reminder answer"}
	tx, err := s.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	marker, _, err := stageRevertDB(ctx, tx, chat, fmt.Sprintf("%d:0", second))
	require.NoError(t, err)
	require.NoError(t, commitRevertDB(ctx, tx, chat, marker))
	require.NoError(t, tx.Commit())
	require.Equal(t, kept, observedEntryTexts(t, s, chat))

	_, err = s.AppendEntryID(ctx, chat, testSessionEntry("third question", "third answer"))
	require.NoError(t, err)
	deleted, err := s.DeleteSession(ctx, chat)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	require.Equal(t, kept, observedEntryTexts(t, s, chat))
	require.Equal(t, []string{"message:reminder prompt", "message:reminder answer"}, observedEntryTexts(t, s, run), "the run's next turn sees the same history")
}
