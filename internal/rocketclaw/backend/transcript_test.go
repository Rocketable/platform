package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

func TestObserveTranscript(t *testing.T) {
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
	producerID, err := s.AppendEntryID(ctx, "producer", saved)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp)
SELECT 'main', (entry_json::jsonb || jsonb_build_object('sync_source_entry_id', id, 'sync_source_conversation_id', conversation_id))::json, entry_timestamp
FROM session_entries WHERE id = $1`, producerID)
	require.NoError(t, err)
	withProducer, err := s.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, withProducer, 3)
	require.Equal(t, "turn:producer:turn-1", withProducer[2].Key)
	require.Equal(t, "producer", withProducer[2].SourceConversationID)
	require.True(t, withProducer[2].Synced)

	_, err = s.db.ExecContext(ctx, `DELETE FROM session_entries WHERE conversation_id = 'main' AND entry_json::jsonb ? 'sync_source_entry_id'`)
	require.NoError(t, err)
	require.NoError(t, clearTestTurn(ctx, s, checkpoint.TurnID))

	checkpoint.TurnID = "failed-turn"
	require.NoError(t, upsertTestTurn(ctx, s, checkpoint))
	require.NoError(t, terminateTestTurn(ctx, s, checkpoint.TurnID, protocol.TerminalFailed))
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

func TestTranscriptChanges(t *testing.T) {
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

	_, err = tx.ExecContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('rolled-back', '{"replay_input":["SECRET"]}', '')`)
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

	// The same transaction ID is opaque, shared by its row changes, and content-free.
	tx, err = s.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	var revision string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_current_xact_id()::text`).Scan(&revision))

	var sourceID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('source', '{"replay_input":["SECRET"]}', '') RETURNING id`).Scan(&sourceID))
	_, err = tx.ExecContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('destination', jsonb_build_object('sync_source_entry_id', $1::bigint)::json, '')`, sourceID)
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

	_, err = s.db.ExecContext(ctx, `UPDATE session_entries SET entry_json = (entry_json::jsonb || '{"response_id":"edited"}'::jsonb)::json WHERE conversation_id = 'destination'`)
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

	// Deleting a source invalidates both its history and surviving synced copies.
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
	s := newTestSessionService(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	for range s.Changes(ctx, "main") {
		t.Fatal("cancellation must end observation without yielding an error")
	}

	require.NoError(t, s.Stop())
	_, err := s.ObserveTranscript(t.Context(), "main", 0, 0, nil)
	require.ErrorContains(t, err, "database is closed")
	_, err = s.OriginPairs(t.Context(), "main")
	require.ErrorContains(t, err, "read origin metadata")

	journal := conversationJournal{store: s, conversationID: "main"}
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
	s := newTestSessionService(t)
	ctx := t.Context()
	journal := conversationJournal{store: s, conversationID: "main"}
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
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM active_turns WHERE id = $1)`, c.TurnID).Scan(&exists); err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	if !exists {
		inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "seed", true)
		if err := startTurnDB(ctx, s.db, c.TurnID, c.ConversationKey, inbound); err != nil {
			return fmt.Errorf("seed test turn: %w", err)
		}
	}

	record := harness.SessionEntry{Version: 1, Type: "turn", TurnID: c.TurnID, Agent: c.Agent, Model: c.DisplayModel, ReasoningEffort: c.ReasoningEffort, ReplayAttribution: c.ReplayAttribution, TokenUsage: c.TokenUsage, ReplayInput: c.ReplayInput, ResponseID: c.ResponseID}

	data, err := json.Marshal(struct {
		Record harness.SessionEntry `json:"record"`
	}{record})
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	journal := conversationJournal{store: s, conversationID: c.ConversationKey}
	if err := journal.Save(ctx, c.TurnID, data); err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	return journal.SaveTrace(ctx, c.TurnID, c.OutputTrace)
}

func clearTestTurn(ctx context.Context, s *SessionService, turnID string) error {
	_, err := s.db.ExecContext(ctx, `WITH turn AS (DELETE FROM active_turns WHERE id = $1 RETURNING conversation_id) DELETE FROM turn_steps WHERE key = $1 AND conversation_id IN (SELECT conversation_id FROM turn)`, turnID)
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	return nil
}

func terminateTestTurn(ctx context.Context, s *SessionService, turnID string, terminal protocol.Terminal) error {
	var conversationID string
	if err := s.db.QueryRowContext(ctx, `SELECT conversation_id FROM active_turns WHERE id = $1`, turnID).Scan(&conversationID); err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	if _, err := s.finishTurn(ctx, turnID, &turnFinish{store: newSessionStore(conversationID, s), outbound: protocol.NewOutboundMessage(conversationID, ""), terminal: terminal}); err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	return s.closeTurn(ctx, turnID)
}

func runningTestTurns(t *testing.T, s *SessionService) []string {
	t.Helper()

	ids, err := queryStrings(t.Context(), s.db, `SELECT id FROM active_turns WHERE phase <> $1 ORDER BY id`, "running test turns", turnDone)
	require.NoError(t, err)

	return ids
}

func TestTranscriptObservationReportsUnreadableStoredEntries(t *testing.T) {
	s := newTestSessionService(t)
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('main', json_build_object('type', $1::text, 'output_trace', 1), '')`, externalMCPOriginPairsEntryType)
	require.NoError(t, err)
	_, err = s.ObserveTranscript(t.Context(), "main", 0, 0, nil)
	require.ErrorContains(t, err, "decode transcript entry")
	_, err = s.OriginPairs(t.Context(), "main")
	require.ErrorContains(t, err, "decode origin metadata")
}
