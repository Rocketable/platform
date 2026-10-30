package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

// rejectShadowLifecycleWrite makes a real database write fail inside the tested
// transaction. Each test owns an isolated schema and removes the trigger to retry.
func rejectShadowLifecycleWrite(t *testing.T, store *SessionService, table, event string) {
	t.Helper()

	_, err := store.db.ExecContext(t.Context(), `CREATE FUNCTION reject_shadow_lifecycle_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'shadow lifecycle write unavailable'; END $$`)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `CREATE TRIGGER reject_shadow_lifecycle_write BEFORE `+event+` ON `+table+` FOR EACH STATEMENT EXECUTE FUNCTION reject_shadow_lifecycle_write()`)
	require.NoError(t, err)
}

func TestShadowLifecycleAdmissionRollback(t *testing.T) {
	for _, table := range []string{"conversations", "entries", "turns", "shadow_turns"} {
		t.Run(table, func(t *testing.T) {
			store, _ := newShadowTestStore(t)
			_, err := (stateDAO{db: store.db}).createConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}, conversationFacts{kind: KindWebChat})
			require.NoError(t, err)
			_, err = store.AppendEntryID(t.Context(), "parent", testSessionEntry("seed", "answer"))
			require.NoError(t, err)

			rejectShadowLifecycleWrite(t, store, table, "INSERT")
			parent := &Bridge{log: store.log, config: Config{ConversationID: "parent", Agent: "main", SessionService: store}}
			request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "next", true)}

			admitted, err := parent.activateInbound(t.Context(), &request)
			require.ErrorContains(t, err, "shadow lifecycle write unavailable")
			require.False(t, admitted)
			require.Empty(t, request.turnID)

			for _, table := range []string{"conversations WHERE shadow_parent IS NOT NULL", "shadow_turns", "turns"} {
				var count int

				require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&count))
				require.Zero(t, count, "failed seeding must not leave an attached or executable child")
			}

			var count int

			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM conversations`).Scan(&count))
			require.Equal(t, 1, count)

			_, err = store.db.ExecContext(t.Context(), `DROP TRIGGER reject_shadow_lifecycle_write ON `+table)
			require.NoError(t, err)

			admitted, err = parent.activateInbound(t.Context(), &request)
			require.NoError(t, err)
			require.True(t, admitted)
			children, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM conversations WHERE shadow_parent IS NOT NULL ORDER BY 1`, "retried children")
			require.NoError(t, err)
			require.Len(t, children, 2)

			for _, child := range children {
				entries, err := store.ObserveEntries(t.Context(), child)
				require.NoError(t, err)
				require.Len(t, entries, 1)
				require.Equal(t, testSessionEntry("seed", "answer").ReplayInput, entries[0].Entry.ReplayInput)
			}
		})
	}
}

func TestShadowLifecycleClearRollback(t *testing.T) {
	for _, step := range []struct{ table, event string }{
		{"entries", "DELETE"}, {"conversations", "UPDATE"}, {"steps", "DELETE"},
		{"shadow_turns", "DELETE"}, {"pending_attachment_deletions", "INSERT"},
	} {
		t.Run(step.table, func(t *testing.T) {
			store, _ := newShadowTestStore(t)
			require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}))
			children, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM conversations WHERE shadow_parent IS NOT NULL ORDER BY 1`, "clear children")
			require.NoError(t, err)

			for _, id := range append(children, "parent") {
				_, err := store.AppendEntryID(t.Context(), id, testSessionEntry("kept until commit", "answer"))
				require.NoError(t, err)
			}

			require.NoError(t, store.SaveAttachment(t.Context(), children[0], "", &protocol.OutboundAttachment{ID: "child-blob", Data: []byte("child bytes")}, false))
			rejectShadowLifecycleWrite(t, store, step.table, step.event)

			_, err = store.DeleteSession(t.Context(), "parent")
			require.ErrorContains(t, err, "shadow lifecycle write unavailable")

			for _, id := range append(children, "parent") {
				entries, err := store.ObserveEntries(t.Context(), id)
				require.NoError(t, err)
				require.Len(t, entries, 1, "clear failure preserves every family history")
			}

			blob, err := store.LoadAttachment(t.Context(), children[0], "child-blob", false)
			require.NoError(t, err)
			require.Equal(t, []byte("child bytes"), blob.Data)

			_, err = store.db.ExecContext(t.Context(), `DROP TRIGGER reject_shadow_lifecycle_write ON `+step.table)
			require.NoError(t, err)

			deleted, err := store.DeleteSession(t.Context(), "parent")
			require.NoError(t, err)
			require.EqualValues(t, 3, deleted)
			_, err = store.attachments.Get(t.Context(), "child-blob")
			require.ErrorIs(t, err, fs.ErrNotExist)
			bindings, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM conversations WHERE shadow_parent IS NOT NULL ORDER BY 1`, "retained children")
			require.NoError(t, err)
			require.Equal(t, children, bindings, "successful retry clears history, not family ownership")
		})
	}
}

func TestShadowLifecycleCaptureFailureAndRetry(t *testing.T) {
	store, _ := newShadowTestStore(t)
	parent := &Bridge{log: store.log, config: Config{ConversationID: "parent", Agent: "main", SessionService: store}}
	request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "start", true)}
	admitted, err := parent.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)

	snapshot := turnInputSnapshot{Inputs: []harness.PromptInput{{ID: "initial", Texts: []string{"prepared"}}}, Closed: true}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err = store.captureTurnInputs(ctx, "parent", request.turnID, &snapshot)
	require.ErrorIs(t, err, context.Canceled)
	persistence, matched := errors.AsType[inputPersistenceError](err)
	require.True(t, matched)
	require.Contains(t, persistence.Error(), "persist consumed input")

	snapshot.ContextEntries = []harness.SessionEntry{{ReplayInput: []json.RawMessage{json.RawMessage(`{`)}}}
	err = store.captureTurnInputs(t.Context(), "parent", request.turnID, &snapshot)
	require.ErrorContains(t, err, "encode canonical input")

	snapshot.ContextEntries = nil

	rejectShadowLifecycleWrite(t, store, "steps", "INSERT")

	err = store.captureTurnInputs(t.Context(), "parent", request.turnID, &snapshot)
	require.ErrorContains(t, err, "save canonical input and fan-out")

	var prepared int

	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns WHERE inputs <> '[]' OR input_closed`).Scan(&prepared))
	require.Zero(t, prepared, "failed capture must not expose a partial payload to either child")

	_, err = store.db.ExecContext(t.Context(), `DROP TRIGGER reject_shadow_lifecycle_write ON steps`)
	require.NoError(t, err)

	require.NoError(t, store.captureTurnInputs(t.Context(), "parent", request.turnID, &snapshot))
	children, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM conversations WHERE shadow_parent IS NOT NULL ORDER BY 1`, "captured children")
	require.NoError(t, err)

	for _, child := range children {
		turn, claimed, err := store.claimShadowTurn(t.Context(), child)
		require.NoError(t, err)
		require.True(t, claimed)
		mirrored, found, err := store.shadowInputs(t.Context(), child, turn.id)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, snapshot, mirrored)

		_, err = store.db.ExecContext(t.Context(), `UPDATE shadow_turns SET inputs = '{}' WHERE child_turn_id = $1`, turn.id)
		require.NoError(t, err)

		bridge := &Bridge{shadow: true, config: Config{ConversationID: child, SessionService: store}, activeTurnID: turn.id}
		_, err = bridge.Drain(t.Context(), "resp-1", harness.TurnPhaseFinalAnswer, nil)
		require.ErrorContains(t, err, "decode mirrored input")
		require.Nil(t, bridge.shadowInputWake, "failed durable read unregisters the waiter")
	}
}

func TestShadowLifecycleDefinitionFailureAndRetry(t *testing.T) {
	for _, failure := range []string{"invalid definition", "escaping directory", "unavailable workspace"} {
		t.Run(failure, func(t *testing.T) {
			store, root := newShadowTestStore(t)
			dir := filepath.Join(config.DefaultRuntimeDir, "agents", "main")

			switch failure {
			case "invalid definition":
				require.NoError(t, root.WriteFile(filepath.Join(dir, "a.md"), []byte("---\nmodel: [\n---\nBroken"), 0o600))
			case "escaping directory":
				require.NoError(t, root.Rename(dir, dir+"-saved"))
				require.NoError(t, root.Symlink("../../../../outside", dir))
			case "unavailable workspace":
				cfg := store.definitions.Clone()
				cfg.Workspace = filepath.Join(root.Name(), "missing")
				store.definitions = config.NewLockedConfig(cfg)
			}

			runtime := &Runtime{Sessions: store}

			err := runtime.CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"})
			if failure == "invalid definition" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}

			_, recorded, err := store.Thread("parent")
			require.NoError(t, err)
			require.Equal(t, failure == "invalid definition", recorded, "a malformed child cannot roll back its healthy parent")

			switch failure {
			case "invalid definition":
				require.NoError(t, root.WriteFile(filepath.Join(dir, "a.md"), []byte("---\nmodel: gpt-6.1-sol\n---\nFixed"), 0o600))
			case "escaping directory":
				require.NoError(t, root.Remove(dir))
				require.NoError(t, root.Rename(dir+"-saved", dir))
			case "unavailable workspace":
				cfg := store.definitions.Clone()
				cfg.Workspace = root.Name()
				store.definitions = config.NewLockedConfig(cfg)
			}

			require.NoError(t, runtime.CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}))

			if failure == "invalid definition" {
				_, err = store.reconcileShadows(t.Context(), store.db, "parent", "main")
				require.NoError(t, err)
			}

			bindings, err := queryStrings(t.Context(), store.db, `SELECT shadow_agent FROM conversations WHERE shadow_parent IS NOT NULL ORDER BY 1`, "repaired definitions")
			require.NoError(t, err)
			require.Equal(t, []string{"main/a", "main/z"}, bindings)
		})
	}
}

func TestShadowLifecycleRevertRollback(t *testing.T) {
	for _, step := range []struct{ table, event string }{
		{"entries", "DELETE"}, {"conversations", "UPDATE"},
		{"steps", "DELETE"},
	} {
		t.Run(step.table+" "+step.event, func(t *testing.T) {
			store, _ := newShadowTestStore(t)
			require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}))

			entry := testSessionEntry("discarded", "answer")
			entry.TurnID = "parent-turn"
			id, err := store.AppendEntryID(t.Context(), "parent", entry)
			require.NoError(t, err)
			children, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM conversations WHERE shadow_parent IS NOT NULL ORDER BY 1`, "revert children")
			require.NoError(t, err)

			for _, child := range children {
				entry.TurnID = child + "-turn"

				_, err := store.AppendEntryID(t.Context(), child, entry)
				require.NoError(t, err)
				_, err = store.db.ExecContext(t.Context(), `INSERT INTO shadow_turns (child_conversation_id, parent_source_conversation_id, parent_turn_id, child_turn_id, inbound_json) VALUES ($1, 'parent', 'parent-turn', $2, '{}')`, child, entry.TurnID)
				require.NoError(t, err)
			}

			marker, _, err := stageRevertDB(t.Context(), store.db, "parent", fmt.Sprintf("%d:0", id))
			require.NoError(t, err)
			rejectShadowLifecycleWrite(t, store, step.table, step.event)

			tx, err := store.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			err = commitRevertDB(t.Context(), tx, "parent", marker)
			require.ErrorContains(t, err, "shadow lifecycle write unavailable")
			require.NoError(t, tx.Rollback())

			var rows int

			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM entries`).Scan(&rows))
			require.Equal(t, 3, rows, "failed branch commit retains physical rows on both sides")

			_, err = store.db.ExecContext(t.Context(), `DROP TRIGGER reject_shadow_lifecycle_write ON `+step.table)
			require.NoError(t, err)

			tx, err = store.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			require.NoError(t, commitRevertDB(t.Context(), tx, "parent", marker))
			require.NoError(t, tx.Commit())
			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM entries`).Scan(&rows))
			require.Zero(t, rows)
			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns`).Scan(&rows))
			require.Zero(t, rows, "retry removes discarded manifests rather than reexecuting them")
		})
	}
}

func TestShadowLifecycleSettlementFailureReleasesFamily(t *testing.T) {
	for _, failure := range []string{"busy", "settlement failed", "canceled"} {
		t.Run(failure, func(t *testing.T) {
			store, _ := newShadowTestStore(t)
			require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}))
			children, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM conversations WHERE shadow_parent IS NOT NULL ORDER BY 1`, "settling children")
			require.NoError(t, err)

			manager := &threadBridgeManager{store: store, bridges: map[string]directBridge{}}

			for _, id := range append(children, "parent") {
				manager.bridges[id] = &Bridge{handling: true, log: store.log, config: Config{ConversationID: id, SessionService: store}}
			}

			store.historyWorkers = manager
			child := manager.bridges[children[1]].(*Bridge)
			errSettlement := errors.New("child settlement failed")

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			switch failure {
			case "busy":
				child.historyMutation = true
			case "settlement failed":
				child.settlement = &turnCompletion{done: make(chan struct{}), err: errSettlement}
				close(child.settlement.done)
			case "canceled":
				child.settlement = &turnCompletion{done: make(chan struct{})}

				cancel()
			}

			var workers []*Bridge

			if failure == "canceled" {
				workers, err = manager.settleHistory(ctx, append(children, "parent"))
			} else {
				_, _, workers, err = store.beginHistoryMutation(ctx, []string{"parent"}, false)
			}

			require.Error(t, err)

			switch failure {
			case "settlement failed":
				require.ErrorIs(t, err, errSettlement)
			case "canceled":
				require.ErrorIs(t, err, context.Canceled)
			}

			require.False(t, manager.bridges[children[0]].(*Bridge).historyMutation, "failure releases earlier family members")
			require.False(t, manager.bridges["parent"].(*Bridge).historyMutation)

			if failure == "busy" {
				require.True(t, child.historyMutation, "another mutation keeps its own admission flag")
			} else {
				require.False(t, child.historyMutation)
			}

			releaseHistory(workers)

			child.historyMutation, child.settlement = false, nil

			tx, ids, workers, err := store.beginHistoryMutation(t.Context(), []string{"parent"}, false)
			require.NoError(t, err, "failed settlement must not strand database locks or admission")
			require.ElementsMatch(t, append(children, "parent"), ids)
			require.NoError(t, tx.Commit())
			releaseHistory(workers)
		})
	}
}

func TestShadowLifecycleUnavailableStateDoesNotStartWork(t *testing.T) {
	store, _ := newShadowTestStore(t)
	parent := &Bridge{log: store.log, config: Config{ConversationID: "parent", Agent: "main", SessionService: store}}
	request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "start", true)}
	admitted, err := parent.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)
	children, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM conversations WHERE shadow_parent IS NOT NULL ORDER BY 1`, "unavailable children")
	require.NoError(t, err)

	child := &Bridge{shadow: true, log: store.log, config: Config{ConversationID: children[0], SessionService: store}, requestCh: make(chan bridgeRequest, 1)}
	parent.threads = &threadBridgeManager{store: store, bridges: map[string]directBridge{children[0]: child}}
	require.NoError(t, store.Stop())

	for _, operation := range []string{"claim", "capture", "drain", "wake", "notify", "recover stopped", "clear", "revert", "history"} {
		t.Run(operation, func(t *testing.T) {
			var err error

			switch operation {
			case "claim":
				_, claimed, errClaim := store.claimShadowTurn(t.Context(), children[0])
				require.False(t, claimed)

				err = errClaim
			case "capture":
				err = store.captureTurnInputs(t.Context(), "parent", request.turnID, &turnInputSnapshot{})
			case "drain":
				_, err = child.Drain(t.Context(), "resp-1", harness.TurnPhaseFinalAnswer, nil)
				require.Nil(t, child.shadowInputWake)
			case "wake":
				err = parent.startShadowWorkers(t.Context(), request.turnID)
			case "notify":
				err = parent.notifyShadowInputs(t.Context(), request.turnID)
			case "recover stopped":
				_, err = child.finishStoppedShadow(t.Context(), &request)
			case "clear":
				_, err = store.DeleteSession(t.Context(), "parent")
			case "revert":
				_, err = (&Runtime{Sessions: store}).settleShadowRevert(t.Context(), "parent", &ObservedSessionEntry{})
			case "history":
				_, err = store.ObserveHistory(t.Context(), "parent", "", 0, 0, 0, &HistoryInventory{})
			}

			require.ErrorContains(t, err, "database is closed")
			require.Empty(t, child.requestCh, "unavailable state must not wake a child or synthesize work")
		})
	}
}

func TestShadowLifecycleDeferredCommitFailureAndRetry(t *testing.T) {
	for _, operation := range []string{"capture", "claim", "clear", "revert", "finish"} {
		t.Run(operation, func(t *testing.T) {
			store, _ := newShadowTestStore(t)
			parent := &Bridge{log: store.log, config: Config{ConversationID: "parent", Agent: "main", SessionService: store}}
			request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "start", true)}
			admitted, err := parent.activateInbound(t.Context(), &request)
			require.NoError(t, err)
			require.True(t, admitted)

			snapshot := turnInputSnapshot{Inputs: []harness.PromptInput{{ID: "initial", Texts: []string{"prepared"}}}}
			require.NoError(t, store.captureTurnInputs(t.Context(), "parent", request.turnID, &snapshot))

			entry := testSessionEntry("start", "answer")
			entry.TurnID = request.turnID
			id, err := store.AppendEntryID(t.Context(), "parent", entry)
			require.NoError(t, err)

			var child string

			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT conversation_id FROM conversations WHERE shadow_parent IS NOT NULL ORDER BY 1 LIMIT 1`).Scan(&child))
			_, err = store.db.ExecContext(t.Context(), `CREATE FUNCTION reject_shadow_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'shadow commit unavailable'; END $$;
CREATE CONSTRAINT TRIGGER reject_shadow_commit AFTER UPDATE ON shadow_turns DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_shadow_commit()`)
			require.NoError(t, err)

			for _, rejected := range []bool{true, false} {
				switch operation {
				case "capture":
					snapshot.Closed = true
					err = store.captureTurnInputs(t.Context(), "parent", request.turnID, &snapshot)
				case "claim":
					_, _, err = store.claimShadowTurn(t.Context(), child)
				case "clear":
					_, err = store.DeleteSession(t.Context(), "parent")
				case "finish":
					_, err = store.finishTurn(t.Context(), request.turnID, &turnFinish{conversationID: strings.TrimSpace("parent"), outbound: protocol.NewOutboundMessage("parent", "answer")})
				case "revert":
					var workers []*Bridge

					workers, err = (&Runtime{Sessions: store, threads: &threadBridgeManager{store: store}}).settleShadowRevert(t.Context(), "parent", &ObservedSessionEntry{ID: id, Entry: *entry})
					releaseHistory(workers)
				}

				if !rejected {
					require.NoError(t, err, "retry after a failed commit must be possible")
					break
				}

				require.ErrorContains(t, err, "shadow commit unavailable")

				var changed int

				require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns WHERE input_closed OR terminal <> '' OR child_turn_id <> ''`).Scan(&changed))
				require.Zero(t, changed, "a commit failure must not publish closure, cancellation, or a child claim")
				active, err := store.HasActiveTurn(t.Context(), child)
				require.NoError(t, err)
				require.False(t, active, "failed claim must not leave an executable child turn")

				_, err = store.db.ExecContext(t.Context(), `DROP TRIGGER reject_shadow_commit ON shadow_turns`)
				require.NoError(t, err)
			}
		})
	}
}

func TestShadowLifecycleFinishRollback(t *testing.T) {
	for _, step := range []struct{ table, event string }{
		{"shadow_turns", "UPDATE"}, {"entries", "INSERT"}, {"turns", "DELETE"},
		{"turns", "UPDATE OF note"}, {"steps", "DELETE"},
	} {
		t.Run(step.table+" "+step.event, func(t *testing.T) {
			store, _ := newShadowTestStore(t)
			ctx := t.Context()
			parent := &Bridge{log: store.log, config: Config{ConversationID: "parent", Agent: "main", SessionService: store}}
			request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "start", true)}
			admitted, err := parent.activateInbound(ctx, &request)
			require.NoError(t, err)
			require.True(t, admitted)

			snapshot := turnInputSnapshot{Inputs: []harness.PromptInput{{ID: "initial", Texts: []string{"start"}}}}
			require.NoError(t, store.captureTurnInputs(ctx, "parent", request.turnID, &snapshot))

			entry := testSessionEntry("start", "answer")
			entry.TurnID = request.turnID
			finish := &turnFinish{conversationID: strings.TrimSpace("parent"), entries: []harness.SessionEntry{*entry}, outbound: protocol.NewOutboundMessage("parent", "answer")}

			rejectShadowLifecycleWrite(t, store, step.table, step.event)

			outbound, err := store.finishTurn(ctx, request.turnID, finish)
			require.ErrorContains(t, err, "shadow lifecycle write unavailable")
			_, persistence := errors.AsType[inputPersistenceError](err)
			require.True(t, persistence, "failed settlement stays resumable rather than becoming a delivered model error")
			require.Nil(t, outbound)

			var phase turnPhase
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT phase FROM turns WHERE id = $1`, request.turnID).Scan(&phase))
			require.Equal(t, turnRunning, phase)

			var closed, saved int
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM shadow_turns WHERE input_closed`).Scan(&closed))
			require.Zero(t, closed, "failed settlement cannot close the children's input window")
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM entries WHERE conversation_id = 'parent'`).Scan(&saved))
			require.Zero(t, saved, "history must not survive a rolled back settlement")

			canonical, found, err := loadTurnInputs(ctx, store.db, "parent", request.turnID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, snapshot, canonical, "retry retains the exact consumed payload")

			_, err = store.db.ExecContext(ctx, `DROP TRIGGER reject_shadow_lifecycle_write ON `+step.table)
			require.NoError(t, err)
			outbound, err = store.finishTurn(ctx, request.turnID, finish)
			require.NoError(t, err)
			require.Equal(t, "answer", outbound.Text)
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM shadow_turns WHERE input_closed`).Scan(&closed))
			require.Equal(t, 2, closed)

			entries, err := store.ObserveEntries(ctx, "parent")
			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.Equal(t, entry.ReplayInput, entries[0].Entry.ReplayInput)

			_, found, err = loadTurnInputs(ctx, store.db, "parent", request.turnID)
			require.NoError(t, err)
			require.False(t, found, "only committed settlement releases the canonical journal")
		})
	}
}

// A shadow's running-turn inbound, which its subagents act for, names the parent's person but not
// the parent's prompt principal.
func TestShadowTurnNamesParentPerson(t *testing.T) {
	store, _ := newShadowTestStore(t)
	parent := &Bridge{log: store.log, config: Config{ConversationID: "parent", Agent: "main", SessionService: store}}
	request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "start", true)}
	request.inbound.Metadata = map[string]string{protocol.InboundPrincipalMetadataKey: "Alice Smith", protocol.InboundPrincipalNameMetadataKey: "Alice Smith", protocol.InboundPrincipalEmailMetadataKey: "alice@example.com"}
	admitted, err := parent.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NoError(t, store.captureTurnInputs(t.Context(), "parent", request.turnID, &turnInputSnapshot{Inputs: []harness.PromptInput{{ID: "initial", Texts: []string{"start"}}}}))

	var childID string

	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT conversation_id FROM conversations WHERE shadow_parent IS NOT NULL ORDER BY 1 LIMIT 1`).Scan(&childID))
	_, claimed, err := store.claimShadowTurn(t.Context(), childID)
	require.NoError(t, err)
	require.True(t, claimed)

	var metadata string

	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT inbound_json->>'Metadata' FROM turns WHERE conversation_id = $1`, childID).Scan(&metadata))
	require.JSONEq(t, `{"rocketclaw_principal_name":"Alice Smith","rocketclaw_principal_email":"alice@example.com"}`, metadata)
}

func TestShadowLifecycleStopNotificationCancelsOwningTurn(t *testing.T) {
	store, _ := newShadowTestStore(t)
	parent := &Bridge{log: store.log, config: Config{ConversationID: "parent", Agent: "main", SessionService: store}}
	request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "start", true)}
	admitted, err := parent.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NoError(t, store.captureTurnInputs(t.Context(), "parent", request.turnID, &turnInputSnapshot{Inputs: []harness.PromptInput{{ID: "initial", Texts: []string{"start"}}}}))

	var childID string

	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT conversation_id FROM conversations WHERE shadow_parent IS NOT NULL ORDER BY 1 LIMIT 1`).Scan(&childID))
	turn, claimed, err := store.claimShadowTurn(t.Context(), childID)
	require.NoError(t, err)
	require.True(t, claimed)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	child := &Bridge{shadow: true, activeTurnID: turn.id, activeTurnCancel: cancel, activeTurnInterrupts: make(chan os.Signal, 1), shadowInputWake: make(chan struct{}, 1), requestCh: make(chan bridgeRequest, 1)}
	parent.threads = &threadBridgeManager{bridges: map[string]directBridge{childID: child}}
	_, err = store.db.ExecContext(t.Context(), `UPDATE shadow_turns SET terminal = $2 WHERE child_turn_id = $1`, turn.id, protocol.TerminalStopped)
	require.NoError(t, err)

	require.NoError(t, parent.notifyShadowInputs(t.Context(), request.turnID))
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.True(t, child.activeTurnInterrupted)
	require.Len(t, child.activeTurnInterrupts, 1)
	require.Equal(t, os.Interrupt, <-child.activeTurnInterrupts)
	require.Len(t, child.shadowInputWake, 1, "stop releases a final-answer input wait")
	require.Len(t, child.requestCh, 1, "local worker is notified to settle its durable stopped turn")
}
