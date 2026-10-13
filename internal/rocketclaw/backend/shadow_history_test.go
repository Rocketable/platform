package backend

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

func TestShadowHistoryWaitingInventory(t *testing.T) {
	store, _ := newShadowTestStore(t)
	ctx := t.Context()
	runtime := &Runtime{Sessions: store}
	require.NoError(t, runtime.CreateConversation(ctx, protocol.Conversation{ID: "parent", Agent: "main"}))
	require.NoError(t, runtime.CreateConversation(ctx, protocol.Conversation{ID: "other", Agent: "main"}))
	_, err := store.db.ExecContext(ctx, `INSERT INTO shadow_turns
(child_conversation_id, parent_source_conversation_id, parent_turn_id, inbound_json)
SELECT child_conversation_id, parent_conversation_id, 'waiting', '{}' FROM shadow_bindings`)
	require.NoError(t, err)
	view, err := store.ObserveHistory(ctx, "parent", "", 0, 0, 0, &HistoryInventory{})
	require.NoError(t, err)
	require.Empty(t, view.Entries)
	require.NotEmpty(t, view.Inventory.Shadows)
	require.Len(t, view.Shadows, 2)

	for i, shadow := range view.Shadows {
		require.Equal(t, "parent", shadow.ParentConversationID)
		require.Equal(t, []string{"main/a", "main/z"}[i], shadow.ShadowAgent)
		require.Len(t, shadow.Turns, 1)
		require.Equal(t, "parent", shadow.Turns[0].ParentSourceConversationID)
		require.Equal(t, "waiting", shadow.Turns[0].ParentTurnID)
		require.Empty(t, shadow.Turns[0].ChildTurnID)
		require.False(t, shadow.Turns[0].Running)
		require.Empty(t, shadow.Turns[0].Terminal)
	}

	view.Inventory.Entries = map[string]string{}
	next, err := store.ObserveHistory(ctx, "parent", "", 0, 0, 0, &view.Inventory)
	require.NoError(t, err)
	require.False(t, next.Reset)
	require.Equal(t, view.Inventory.Shadows, next.Inventory.Shadows)

	child := view.Shadows[0].ChildConversationID
	for _, terminal := range []protocol.Terminal{protocol.TerminalFailed, protocol.TerminalStopped, protocol.TerminalComplete} {
		_, err := store.db.ExecContext(ctx, `UPDATE shadow_turns SET terminal = $2 WHERE child_conversation_id = $1`, child, terminal)
		require.NoError(t, err)
		next, err = store.ObserveHistory(ctx, "parent", "", 0, 0, 0, &view.Inventory)
		require.NoError(t, err)
		require.Empty(t, next.Entries, "a metadata-only change adds no parent messages")
		require.False(t, next.Reset)
		require.NotEqual(t, view.Inventory.Shadows, next.Inventory.Shadows)
		require.Len(t, next.Shadows[0].Turns, 1, "a silent or terminal-only turn retains its row")
		require.Equal(t, terminal, next.Shadows[0].Turns[0].Terminal)
		require.Empty(t, next.Shadows[1].Turns[0].Terminal, "another shadow remains waiting")
		view = next
		view.Inventory.Entries = map[string]string{}
	}
}

func TestShadowHistorySourcePagesAndChildRevision(t *testing.T) {
	store, _ := newShadowTestStore(t)
	ctx := t.Context()

	runtime := &Runtime{Sessions: store}
	for _, id := range []string{"parent", "producer", "other"} {
		require.NoError(t, runtime.CreateConversation(ctx, protocol.Conversation{ID: id, Agent: "main", CreatedBy: "alice"}))
	}

	first := testSessionEntry("same prompt", "first answer")
	first.TurnID = "T1"
	firstID, err := store.AppendEntryID(ctx, "parent", first)
	require.NoError(t, err)

	second := testSessionEntry("same prompt", "second answer")
	second.TurnID = "T2"
	secondID, err := store.AppendEntryID(ctx, "parent", second)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `INSERT INTO shadow_turns
(child_conversation_id, parent_source_conversation_id, parent_turn_id, inbound_json)
SELECT b.child_conversation_id, b.parent_conversation_id, v.turn_id, '{}'
FROM shadow_bindings b JOIN (VALUES ('parent', 'T1'), ('parent', 'T2'), ('producer', 'T1'), ('other', 'T1')) v(parent, turn_id)
ON b.parent_conversation_id = v.parent ORDER BY v.parent, v.turn_id, b.shadow_agent`)
	require.NoError(t, err)

	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "same prompt", false)
	inbound.SyncDestination = "parent"
	require.NoError(t, startTurnDB(ctx, store.db, "T1", "producer", inbound))
	view, err := store.ObserveHistory(ctx, "parent", "", 0, 0, 0, &HistoryInventory{})
	require.NoError(t, err)
	require.Len(t, view.Shadows, 4, "live producer bindings appear before history sync, unrelated bindings do not")

	for i := range 2 {
		require.Equal(t, "parent", view.Shadows[i].ParentConversationID)
		require.Equal(t, "T1", view.Shadows[i].Turns[0].ParentTurnID)
		require.Equal(t, "T2", view.Shadows[i].Turns[1].ParentTurnID)
		require.Less(t, view.Shadows[i].Turns[0].Order, view.Shadows[i].Turns[1].Order)
		require.Equal(t, "producer", view.Shadows[i+2].ParentConversationID)
		require.Equal(t, "producer", view.Shadows[i+2].Turns[0].ParentSourceConversationID)
		require.Equal(t, "T1", view.Shadows[i+2].Turns[0].ParentTurnID, "source scopes reused turn IDs")
	}

	_, err = store.appendExternalMCPEntry(ctx, "producer", "parent", first, nil)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `DELETE FROM session_entries WHERE conversation_id = 'producer'`)
	require.NoError(t, err)
	view, err = store.ObserveHistory(ctx, "parent", "producer", 0, 0, 0, &HistoryInventory{})
	require.NoError(t, err)
	require.Len(t, view.Shadows, 2)
	require.Equal(t, "producer", view.Shadows[0].ParentConversationID)
	require.Len(t, view.Shadows[0].Turns, 1, "sync projects the original correlation exactly once")

	page, err := store.ObserveHistory(ctx, "parent", "", firstID, secondID, 0, &HistoryInventory{})
	require.NoError(t, err)
	require.Len(t, page.Shadows, 2)
	require.Len(t, page.Shadows[0].Turns, 1)
	require.Equal(t, "T1", page.Shadows[0].Turns[0].ParentTurnID)

	tail, err := store.ObserveHistory(ctx, "parent", "", secondID, 0, 0, &HistoryInventory{})
	require.NoError(t, err)
	require.Len(t, tail.Shadows, 4)
	require.Len(t, tail.Shadows[0].Turns, 1)
	require.Equal(t, "T2", tail.Shadows[0].Turns[0].ParentTurnID)

	child := tail.Shadows[0].ChildConversationID
	require.NoError(t, startTurnDB(ctx, store.db, "child-T2", child, protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "same prompt", false)))
	_, err = store.db.ExecContext(ctx, `UPDATE shadow_turns SET child_turn_id = 'child-T2' WHERE child_conversation_id = $1 AND parent_turn_id = 'T2'`, child)
	require.NoError(t, err)
	running, err := store.ObserveHistory(ctx, "parent", "", secondID, 0, 0, &HistoryInventory{})
	require.NoError(t, err)
	require.True(t, running.Shadows[0].Turns[0].Running)
	require.NotEqual(t, tail.Inventory.Shadows, running.Inventory.Shadows)
	require.NoError(t, store.SaveTurnStep(ctx, child, "child-T2", json.RawMessage(`{"record":{"replay_input":[{"role":"assistant","content":"partial"}]}}`)))
	updated, err := store.ObserveHistory(ctx, "parent", "", secondID, 0, 0, &HistoryInventory{})
	require.NoError(t, err)
	require.NotEqual(t, running.Inventory.Shadows, updated.Inventory.Shadows)
	require.NotEqual(t, running.Shadows[0].Turns[0].Revision, updated.Shadows[0].Turns[0].Revision)
	require.Equal(t, running.Shadows[1].Turns[0].Revision, updated.Shadows[1].Turns[0].Revision)

	for _, terminal := range []protocol.Terminal{protocol.TerminalComplete, protocol.TerminalFailed, protocol.TerminalStopped} {
		if terminal != protocol.TerminalComplete {
			_, err = store.db.ExecContext(ctx, `DELETE FROM active_turns WHERE id = 'child-T2'`)
			require.NoError(t, err)
			require.NoError(t, startTurnDB(ctx, store.db, "child-T2", child, protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "same prompt", false)))
		}

		outbound := protocol.NewOutboundMessage(child, "")
		outbound.Terminal = terminal

		finish := turnFinish{store: newSessionStore(child, store), outbound: outbound}
		if terminal != protocol.TerminalComplete {
			finish.terminal = terminal
		}

		_, err = store.finishTurn(ctx, "child-T2", &finish)
		require.NoError(t, err)
		require.NoError(t, store.closeTurn(ctx, "child-T2"))
		settled, err := store.ObserveHistory(ctx, "parent", "", secondID, 0, 0, &HistoryInventory{})
		require.NoError(t, err)
		require.False(t, settled.Shadows[0].Turns[0].Running, string(terminal))
		require.Equal(t, terminal, settled.Shadows[0].Turns[0].Terminal)
		require.NotEqual(t, updated.Inventory.Shadows, settled.Inventory.Shadows)
	}

	_, err = store.db.ExecContext(ctx, `UPDATE managed_conversations SET revert_message_id = $1 WHERE conversation_id = 'parent'`, fmt.Sprintf("%d:1", secondID))
	require.NoError(t, err)
	cut, err := store.ObserveHistory(ctx, "parent", "", 0, 0, 0, &HistoryInventory{})
	require.NoError(t, err)
	require.Len(t, cut.Shadows, 2, "reverted producer output does not expose a binding")
	require.Len(t, cut.Shadows[0].Turns, 1, "a mid-turn cutoff hides the whole paired child turn")
	require.Equal(t, "T1", cut.Shadows[0].Turns[0].ParentTurnID)
}

func TestShadowHistoryIsAbsentFromOrdinaryEnumeration(t *testing.T) {
	store, _ := newShadowTestStore(t)
	ctx := t.Context()
	runtime := &Runtime{Sessions: store}
	require.NoError(t, runtime.CreateConversation(ctx, protocol.Conversation{ID: "parent", Agent: "main"}))
	children, err := queryStrings(ctx, store.db, `SELECT child_conversation_id FROM shadow_bindings`, "test children")
	require.NoError(t, err)

	for _, id := range append(children, "parent") {
		_, err := store.AppendEntryID(ctx, id, testSessionEntry("shared searchable input", "reply"))
		require.NoError(t, err)
	}

	var listed []string

	for session, err := range store.SidebarSessions(ctx) {
		require.NoError(t, err)

		listed = append(listed, session.Conversation.ID)
	}

	require.Equal(t, []string{"parent"}, listed)

	hits, _, err := store.SearchMessages(ctx, "searchable", nil)
	require.NoError(t, err)

	for _, hit := range hits {
		require.Equal(t, "parent", hit.ConversationID)
	}

	for facts, err := range store.ChatOriginFacts(ctx, "") {
		require.NoError(t, err)
		require.Equal(t, "parent", facts.ConversationID)
	}

	listedResult, err := listSessionsTool(store, &threadBridgeManager{slackLookups: noSlackLookups{}}).Call(ctx, json.RawMessage(`{"since":"1970-01-01T00:00:00Z","until":"","limit":100,"include_message_preview":false}`), nil)
	require.NoError(t, err)
	require.Contains(t, listedResult.Output, "parent\t")

	for _, child := range children {
		require.NotContains(t, listedResult.Output, child)
	}

	inspected, err := getSessionTool(store).Call(ctx, json.RawMessage(`{"conversation_id":"parent","limit":100,"before_entry_id":0}`), nil)
	require.NoError(t, err)

	for _, child := range children {
		require.Contains(t, inspected.Output, child, "parent inspection exposes the readable child bindings")
	}
}

func TestShadowPublicMutationsAreReadOnly(t *testing.T) {
	store, _ := newShadowTestStore(t)
	ctx := t.Context()
	manager, shutdown := newTestBridgeManager(t, store.definitions, store, make(chan *protocol.OutboundMessage, 16))
	t.Cleanup(func() { require.NoError(t, shutdown()) })

	runtime := &Runtime{Sessions: store, Cfg: store.definitions, threads: manager}
	require.NoError(t, runtime.CreateConversation(ctx, protocol.Conversation{ID: "parent", Agent: "main"}))

	var child string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT child_conversation_id FROM shadow_bindings ORDER BY shadow_agent LIMIT 1`).Scan(&child))
	_, err := store.AppendEntryID(ctx, child, testSessionEntry("retained input", "retained answer"))
	require.NoError(t, err)

	for _, id := range []string{child, child + "/task"} {
		for _, action := range []string{"fork", "delete", "upload", "stage", "redo", "prompt", "cancel", "goal", "pop", "promote", "delete queue", "reorder", "stash", "switch", "move background", "stop background", "answer"} {
			var err error

			switch action {
			case "fork":
				_, err = store.ForkConversation(ctx, id, protocol.Conversation{ID: "off-contract-fork", Agent: "main"}, "")
			case "delete":
				_, err = store.DeleteSession(ctx, id)
			case "upload":
				err = store.SaveAttachment(ctx, id, "", &protocol.OutboundAttachment{ID: "off-contract-upload", Data: []byte("data")}, true)
			case "stage":
				_, _, err = runtime.StageRevert(ctx, id, "")
			case "redo":
				err = runtime.ClearRevert(ctx, id)
			case "prompt", "cancel":
				inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "must not run", true)

				inbound.ConversationID = id
				if action == "cancel" {
					inbound.Kind = protocol.InboundKindCancel
				}

				err = runtime.RunTurn(ctx, inbound)
			case "goal":
				inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "must not run", true)
				inbound.ConversationID = id
				err = runtime.StartGoal(ctx, inbound, protocol.GoalRequest{Objective: "must not run"})
			case "pop":
				_, err = runtime.PopQueueItem(ctx, id, "item")
			case "promote":
				_, err = runtime.PromoteQueueItem(ctx, id, "item")
			case "delete queue":
				_, err = runtime.DeleteQueueItem(ctx, id, "item")
			case "reorder":
				err = runtime.ReorderQueueItems(id, []string{"item"})
			case "stash":
				err = runtime.StashQueueItem(ctx, id, &protocol.ThreadQueueItem{ID: "item", Source: protocol.SourceWeb})
			case "switch":
				_, err = runtime.SwitchConversationAgent(id, "main")
			case "move background":
				_, err = runtime.MoveToBackground(t.Context(), id)
			case "stop background":
				_, err = runtime.StopBackgroundJob(ctx, id, "job")
			case "answer":
				err = runtime.AnswerQuestion(ctx, id, "question", protocol.AskUserQuestionAnswer{})
			}

			require.Error(t, err, action)
			require.Contains(t, err.Error(), "read-only", action)
		}
	}

	entries, err := store.ObserveEntries(ctx, child)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	_, recorded, err := store.Thread("off-contract-fork")
	require.NoError(t, err)
	require.False(t, recorded)
}

func TestShadowHistoryChangeSignals(t *testing.T) {
	store, _ := newShadowTestStore(t)
	ctx := t.Context()

	runtime := &Runtime{Sessions: store}
	for _, id := range []string{"parent", "producer", "unrelated"} {
		require.NoError(t, runtime.CreateConversation(ctx, protocol.Conversation{ID: id, Agent: "main"}))
	}

	_, err := store.db.ExecContext(ctx, `UPDATE managed_conversations SET producer_inbound_json = '{"SyncDestination":"parent"}' WHERE conversation_id = 'producer'`)
	require.NoError(t, err)

	var child, producerChild string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT child_conversation_id FROM shadow_bindings WHERE parent_conversation_id = 'parent' AND shadow_agent = 'main/a'`).Scan(&child))
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT child_conversation_id FROM shadow_bindings WHERE parent_conversation_id = 'producer' AND shadow_agent = 'main/a'`).Scan(&producerChild))
	conn, err := store.db.Conn(ctx)

	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()

	var channel string
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT 'rocketclaw_transcript_' || md5(current_schema())`).Scan(&channel))
	_, err = conn.ExecContext(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize())
	require.NoError(t, err)

	for _, step := range []struct {
		name, query string
		args        []any
		want        []string
		rollback    bool
	}{
		{"child history", `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ($1, '{"type":"turn"}', '')`, []any{child}, []string{child, "parent"}, false},
		{"delegation history", `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ($1, '{"type":"turn"}', '')`, []any{child + "/task"}, []string{child + "/task", "parent"}, false},
		{"producer history", `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ($1, '{"type":"turn"}', '')`, []any{producerChild}, []string{producerChild, "producer", "parent"}, false},
		{"manifest admission", `INSERT INTO shadow_turns (child_conversation_id, parent_source_conversation_id, parent_turn_id, inbound_json) VALUES ($1, 'parent', 'T', '{}')`, []any{child}, []string{"parent"}, false},
		{"terminal state", `UPDATE shadow_turns SET terminal = 'failed' WHERE child_conversation_id = $1`, []any{child}, []string{"parent"}, false},
		{"checkpoint", `INSERT INTO turn_steps (conversation_id, key, value) VALUES ($1, 'T', '{}')`, []any{child}, []string{child, "parent"}, false},
		{"rollback", `UPDATE shadow_turns SET terminal = 'stopped' WHERE child_conversation_id = $1`, []any{child}, nil, true},
		{"binding removal", `DELETE FROM shadow_bindings WHERE child_conversation_id = $1`, []any{child}, []string{"parent"}, false},
	} {
		t.Run(step.name, func(t *testing.T) {
			tx, err := store.db.BeginTx(ctx, nil)
			require.NoError(t, err)
			_, err = tx.ExecContext(ctx, step.query, step.args...)
			require.NoError(t, err)

			if step.rollback {
				require.NoError(t, tx.Rollback())
			} else {
				require.NoError(t, tx.Commit())
			}

			_, err = store.db.ExecContext(ctx, `SELECT pg_notify($1, 'barrier')`, channel)
			require.NoError(t, err)

			var got []string

			require.NoError(t, conn.Raw(func(raw any) error {
				for {
					n, err := raw.(*stdlib.Conn).Conn().WaitForNotification(ctx)
					require.NoError(t, err)

					if n.Payload == "barrier" {
						break
					}

					var change protocol.ConversationChange
					require.NoError(t, json.Unmarshal([]byte(n.Payload), &change))
					require.NotEmpty(t, change.Revision)
					got = append(got, change.ConversationID)
				}

				return nil
			}))
			require.ElementsMatch(t, step.want, got, "notifications reach only the owning parent and its visible destination")
		})
	}
}
