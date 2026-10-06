package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestRevertQueueAdmissionAndRetry(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	id, err := s.AppendEntryID(ctx, "main", testSessionEntry("abandoned", "answer"))
	require.NoError(t, err)

	marker := fmt.Sprintf("%d:0", id)
	_, _, err = stageRevertDB(ctx, s.db, "main", marker)
	require.NoError(t, err)

	bridge := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, s, bridge.log, func(Config) directBridge { return bridge })
	rt := &Runtime{Sessions: s, threads: manager}
	item := protocol.ThreadQueueItem{ID: "replacement", Source: protocol.SourceWeb, Principal: "alice", Kind: protocol.InboundKindHeld, Message: "edited"}
	require.NoError(t, rt.StashQueueItem(ctx, "main", &item))
	current, _, _, err := s.RevertState(ctx, "main")
	require.NoError(t, err)
	require.Empty(t, current, "STASH acceptance must commit the branch")

	_, _, err = stageRevertDB(ctx, s.db, "main", marker)
	require.Error(t, err, "accepted branch must delete the abandoned request")
	require.NoError(t, rt.StashQueueItem(ctx, "main", &item))

	queue, err := s.ThreadQueueForConversation("main")
	require.NoError(t, err)
	require.Len(t, queue, 1)

	item.Message = "conflicting"
	require.ErrorContains(t, rt.StashQueueItem(ctx, "main", &item), "different content")

	bridge.historyMutation = true
	item.ID = "racing"
	require.ErrorContains(t, rt.StashQueueItem(ctx, "main", &item), "busy")

	bridge.historyMutation = false
	bridge.waiting = []bridgeRequest{{queueItemID: "replacement", completion: &turnCompletion{done: make(chan struct{})}}}
	_, err = rt.DeleteQueueItem(context.Background(), "main", "replacement")
	require.NoError(t, err)
}

func TestRevertSettlesAndPreservesWaitingWork(t *testing.T) {
	newBridge, s, requests, finals := newResumeTestBridges(t, true)
	b := newBridge()
	b.config.ConversationID = "main"

	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	id, err := s.AppendEntryID(t.Context(), "main", testSessionEntry("replace", "abandoned"))
	require.NoError(t, err)

	manager := newThreadBridgeManager(b.runtime, s, b.log, func(Config) directBridge { return b })
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager, Cfg: b.runtime}
	runTestBridge(t, b)

	active := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "running"}, true)
	active.ConversationID, active.Metadata["web_message_id"], active.Metadata[protocol.InboundPrincipalMetadataKey] = "main", "active", "alice"

	done := make(chan error, 1)
	go func() { done <- rt.RunTurn(t.Context(), active) }()

	<-requests
	require.NoError(t, s.BeginGoal("main", "continue forever", "", 0, "", ""))

	for _, id := range []string{"queued-1", "queued-2"} {
		require.NoError(t, s.PutThreadQueueItem(id, &protocol.ThreadQueueItem{ID: id, ConversationID: "main", Message: id, Source: protocol.SourceWeb, Position: 10}))
	}

	steer := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "waiting steer"}, true)
	steer.ConversationID, steer.Metadata["web_message_id"], steer.Metadata[protocol.InboundPrincipalMetadataKey] = "main", "steer", "alice"
	fresh, err := rt.admitWeb(t.Context(), b, &bridgeRequest{inbound: steer}, protocol.GoalRequest{})
	require.NoError(t, err)
	require.True(t, fresh)

	completion := &turnCompletion{done: make(chan struct{})}
	require.NoError(t, b.enqueue(t.Context(), &bridgeRequest{inbound: steer, queueItemID: "steer", completion: completion}, "test steer"))

	channel := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindEnqueue, &protocol.InboundContent{Text: "waiting channel"}, true)
	channel.ConversationID, channel.Metadata["web_message_id"] = "main", "channel"
	channelDone := &turnCompletion{done: make(chan struct{})}
	channelRequest := bridgeRequest{inbound: channel, completion: channelDone}
	fresh, err = rt.admitWeb(t.Context(), b, &channelRequest, protocol.GoalRequest{})
	require.NoError(t, err)
	require.True(t, fresh)

	channelRequest.queueItemID = "channel"
	require.NoError(t, b.enqueue(t.Context(), &channelRequest, "waiting channel"))
	marker, prompt, err := rt.StageRevert(t.Context(), "main", fmt.Sprintf("%d:0", id))
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d:0", id), marker)
	require.Equal(t, "replace", prompt)
	require.NoError(t, <-done)
	require.Equal(t, "main", readFinal(t, finals).ConversationID)

	queue, err := s.ThreadQueueForConversation("main")
	require.NoError(t, err)
	require.Equal(t, "steer", queue[0].ID, "an unconsumed steer keeps its place ahead of later work")
	require.Len(t, queue, 4)
	require.Equal(t, "channel", queue[1].ID, "channel requests follow uninjected steers")
	require.Empty(t, requests, "staging must start no successor")

	select {
	case <-completion.done:
		t.Fatal("uninjected steer was falsely reported complete")
	default:
	}

	visible, err := rt.QueueItems("main")
	require.NoError(t, err)
	require.Empty(t, visible)
	_, err = rt.PopQueueItem(t.Context(), "main", "queued-1")
	require.Error(t, err, "queue mutations cannot release a hidden suffix")
	_, err = rt.PromoteQueueItem(t.Context(), "main", "queued-1")
	require.Error(t, err)
	require.NoError(t, rt.ClearRevert(t.Context(), "main"))

	goal, found, err := s.Goal("main")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, GoalStatusStopped, goal.Status, "Redo does not restart a stopped goal")
	readFinal(t, finals)
	readFinal(t, finals)
	require.Contains(t, <-requests, "waiting steer")
	require.Contains(t, <-requests, "waiting channel")

	select {
	case <-completion.done:
		require.NoError(t, completion.err)
	case <-time.After(10 * time.Second):
		t.Fatal("preserved steer completion did not return on Redo")
	}

	<-channelDone.done
	require.NoError(t, channelDone.err)
}

func TestRevertWaitsForDeliverySettlement(t *testing.T) {
	for _, errDelivery := range []error{nil, errors.New("delivery failed")} {
		t.Run(fmt.Sprint(errDelivery), func(t *testing.T) {
			newBridge, s, _, _ := newResumeTestBridges(t, false)
			b := newBridge()
			b.config.ConversationID = "main"

			require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
			id, err := s.AppendEntryID(t.Context(), "main", testSessionEntry("old", "answer"))
			require.NoError(t, err)

			finals := make(chan *protocol.OutboundMessage, 1)
			b.bus = &outboundPublisherMock{PublishOutboundFunc: func(_ context.Context, message *protocol.OutboundMessage) error {
				if message.Complete {
					finals <- message
				} else {
					message.MarkDelivered(nil)
				}

				return nil
			}}
			manager := newThreadBridgeManager(b.runtime, s, b.log, func(Config) directBridge { return b })
			manager.bridges["main"] = b
			rt := &Runtime{Sessions: s, threads: manager, Cfg: b.runtime}
			runTestBridge(t, b)

			inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindSteer, "new", true)
			inbound.ConversationID = "main"

			turnDone, staged := make(chan error, 1), make(chan error, 1)
			go func() { turnDone <- rt.RunTurn(t.Context(), inbound) }()

			final := readFinal(t, finals)
			go func() { _, _, err := rt.StageRevert(t.Context(), "main", fmt.Sprintf("%d:0", id)); staged <- err }()

			require.Eventually(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.historyMutation }, time.Second, time.Millisecond)

			select {
			case <-staged:
				t.Fatal("staged before delivery settled")
			default:
			}

			final.MarkDelivered(errDelivery)

			errTurn, errStage := <-turnDone, <-staged
			marker, _, _, err := s.RevertState(t.Context(), "main")
			require.NoError(t, err)

			if errDelivery == nil {
				require.NoError(t, errTurn)
				require.NoError(t, errStage)
				require.Equal(t, fmt.Sprintf("%d:0", id), marker)
			} else {
				require.ErrorIs(t, errTurn, errDelivery)
				require.ErrorIs(t, errStage, errDelivery)
				require.Empty(t, marker)
			}
		})
	}
}

func TestRevertStorageLifecycle(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	_, err := s.db.ExecContext(ctx, `INSERT INTO managed_conversations (conversation_id, agent, created_by) VALUES ('main', 'planner', '')`)
	require.NoError(t, err)
	first, err := s.AppendEntryID(ctx, "main", testSessionEntry("first", "reply"))
	require.NoError(t, err)
	second, err := s.AppendEntryID(ctx, "main", testSessionEntry("second", "reply"))
	require.NoError(t, err)
	marker, eligible, previous, err := s.RevertState(ctx, "main")
	require.NoError(t, err)
	require.Empty(t, marker)
	require.True(t, eligible)
	require.Equal(t, fmt.Sprintf("%d:0", second), previous)

	for _, invalid := range []string{"bad", "1:delivery", "01:0", "1:-1", fmt.Sprintf("%d:1", first), fmt.Sprintf("%d:99", first)} {
		_, _, err := stageRevertDB(ctx, s.db, "main", invalid)
		require.Error(t, err)
	}

	marker, text, err := stageRevertDB(ctx, s.db, "main", "")
	require.NoError(t, err)
	require.Equal(t, "second", text)
	require.Equal(t, fmt.Sprintf("%d:0", second), marker)
	marker, text, err = stageRevertDB(ctx, s.db, "main", "")
	require.NoError(t, err)
	require.Equal(t, "first", text)
	require.Equal(t, fmt.Sprintf("%d:0", first), marker)

	for _, stale := range []string{fmt.Sprintf("%d:0", second), marker} {
		_, _, err := stageRevertDB(ctx, s.db, "main", stale)
		require.ErrorContains(t, err, "not visible")
	}

	entries, err := s.ObserveEntries(ctx, "main")
	require.NoError(t, err)
	require.Empty(t, entries)

	prune, err := shouldPruneThreadConversation(ctx, s.db, "main", time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.False(t, prune, "a first-message cutoff must not let retention discard the stored suffix")

	_, eligible, previous, err = s.RevertState(ctx, "main")
	require.NoError(t, err)
	require.True(t, eligible)
	require.Empty(t, previous)

	_, text, err = stageRevertDB(ctx, s.db, "main", "")
	require.NoError(t, err)
	require.Empty(t, text, "Undo at the beginning does not replace the draft")

	tx, err := s.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, lockSessionHistory(ctx, tx, "main"))
	require.NoError(t, commitRevertDB(ctx, tx, "main", marker))
	require.NoError(t, tx.Rollback())

	marker, _, _, err = s.RevertState(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d:0", first), marker, "failed admission preserves the marker and suffix")

	_, err = s.db.ExecContext(ctx, `UPDATE managed_conversations SET revert_message_id = '' WHERE conversation_id = 'main'`)
	require.NoError(t, err)
	entries, err = s.ObserveEntries(ctx, "main")
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestRevertEligibility(t *testing.T) {
	s := newTestSessionService(t)

	ctx := t.Context()
	for _, id := range []string{"slack-thread:C1:1.1", "cron:job:1", "web:cron:job:1", "child/call", "external", "private", "copied", "valid"} {
		_, err := s.db.ExecContext(ctx, `INSERT INTO managed_conversations (conversation_id, agent, created_by) VALUES ($1, 'planner', '')`, id)
		require.NoError(t, err)
		_, err = s.AppendEntryID(ctx, id, testSessionEntry("request", "reply"))
		require.NoError(t, err)
	}

	require.NoError(t, s.UpsertExternalMCPSession("binding", &ExternalMCPSessionState{ManagedConversationID: "external", PrivateConversationID: "private", Agent: "planner"}))
	_, err := s.db.ExecContext(ctx, `UPDATE session_entries SET entry_json = (entry_json::jsonb || '{"sync_source_conversation_id":"cron:job:1"}'::jsonb)::json WHERE conversation_id = 'copied'`)
	require.NoError(t, err)

	for _, id := range []string{"slack-thread:C1:1.1", "cron:job:1", "web:cron:job:1", "child/call", "external", "private", "copied", "missing"} {
		_, allowed, _, err := s.RevertState(ctx, id)
		require.NoError(t, err)
		require.False(t, allowed, id)
		_, _, err = stageRevertDB(ctx, s.db, id, "")
		require.Error(t, err, id)
	}

	_, err = s.db.ExecContext(ctx, `UPDATE managed_conversations SET created_by = $1 WHERE conversation_id = 'valid'`, ThreadCreatedByCron)
	require.NoError(t, err)
	_, allowed, _, err := s.RevertState(ctx, "valid")
	require.NoError(t, err)
	require.False(t, allowed)

	for _, source := range []string{"slack-thread:C1:1.1", "private"} {
		_, err := s.db.ExecContext(ctx, `UPDATE session_entries SET entry_json = (entry_json::jsonb || jsonb_build_object('sync_source_conversation_id', $1::text))::json WHERE conversation_id = 'copied'`, source)
		require.NoError(t, err)
		_, allowed, _, err := s.RevertState(ctx, "copied")
		require.NoError(t, err)
		require.False(t, allowed, "copied routing facts cannot become pure Web")
	}
}

func TestRevertPausesScheduledClaims(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	_, err := s.db.ExecContext(ctx, `INSERT INTO managed_conversations (conversation_id, agent, created_by) VALUES ('main', 'planner', '')`)
	require.NoError(t, err)
	id, err := s.AppendEntryID(ctx, "main", testSessionEntry("first", "reply"))
	require.NoError(t, err)
	_, _, err = stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:0", id))
	require.NoError(t, err)

	now := time.Now().UTC()
	scheduled := protocol.ScheduledMessageState{ConversationID: "main", Agent: "planner", Message: "later", DueAt: now, Recurring: true, Interval: time.Hour}
	require.NoError(t, s.PutScheduledMessage("later", &scheduled))
	_, claimed, err := s.ClaimScheduledMessage("later", "main", now, now)
	require.NoError(t, err)
	require.False(t, claimed, "a staged suffix must not advance recurrence or start automatic work")

	stored, err := s.ScheduledMessagesForConversation("main")
	require.NoError(t, err)
	require.Equal(t, scheduled, stored["later"])

	_, err = s.db.ExecContext(ctx, `UPDATE managed_conversations SET revert_message_id = '' WHERE conversation_id = 'main'`)
	require.NoError(t, err)
	advanced, claimed, err := s.ClaimScheduledMessage("later", "main", now, now)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, now.Add(time.Hour), advanced.DueAt)
	_, claimed, err = s.ClaimScheduledMessage("later", "main", now, now)
	require.NoError(t, err)
	require.False(t, claimed, "clearing the gate must not duplicate the original scheduled claim")
}

// OpenCode V2 packages/core/src/session/session.ts reconciles an accepted ID
// before preparing or committing a new branch, including after completion.
func TestRevertProviderPrefixAndCompletedRetry(t *testing.T) {
	newBridge, s, requests, finals := newResumeTestBridges(t, false)
	b := newBridge()
	b.config.ConversationID = "main"

	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))

	entry := testSessionEntry("retained", "retained answer")
	entry.ReplayInput = append(entry.ReplayInput, testSessionEntry("abandoned", "abandoned answer").ReplayInput...)
	entry.ReplayInput = append(entry.ReplayInput, json.RawMessage(`{"type":"compaction","content":"abandoned summary"}`))
	id, err := s.AppendEntryID(t.Context(), "main", entry)
	require.NoError(t, err)

	manager := newThreadBridgeManager(b.runtime, s, b.log, func(Config) directBridge { return b })
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager, Cfg: b.runtime}
	runTestBridge(t, b)
	_, _, err = rt.StageRevert(t.Context(), "main", fmt.Sprintf("%d:2", id))
	require.NoError(t, err)

	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "replacement"}, true)
	inbound.ConversationID, inbound.Metadata["web_message_id"], inbound.Metadata[protocol.InboundPrincipalMetadataKey] = "main", "replacement", "alice"
	require.NoError(t, rt.RunTurn(t.Context(), inbound))
	readFinal(t, finals)

	request := <-requests
	require.Contains(t, request, "retained answer")
	require.Contains(t, request, "replacement")
	require.Contains(t, request, "alice")
	require.NotContains(t, request, "abandoned")
	entries, err := s.ObserveEntries(t.Context(), "main")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	marker := fmt.Sprintf("%d:0", entries[1].ID)
	_, _, err = rt.StageRevert(t.Context(), "main", marker)
	require.NoError(t, err)
	require.NoError(t, rt.RunTurn(t.Context(), inbound))
	current, _, _, err := s.RevertState(t.Context(), "main")
	require.NoError(t, err)
	require.Equal(t, marker, current, "completed retry must not commit a later cutoff")
	require.Empty(t, requests)

	inbound.Attachments = []protocol.InboundAttachment{{Name: "new.png", MIMEType: "image/png", Data: []byte("new")}}
	require.ErrorContains(t, rt.RunTurn(t.Context(), inbound), "different content")
}

func TestRevertCompletedFallbackAndPreparationFailure(t *testing.T) {
	newBridge, s, requests, finals := newResumeTestBridges(t, false)
	b := newBridge()
	b.config.ConversationID = "main"

	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	id, err := s.AppendEntryID(t.Context(), "main", testSessionEntry("earlier", "answer"))
	require.NoError(t, err)

	manager := newThreadBridgeManager(b.runtime, s, b.log, func(Config) directBridge { return b })
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager, Cfg: b.runtime}
	runTestBridge(t, b)

	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "", AttachmentPresence: protocol.AttachmentPresenceUnsupported}, true)
	inbound.ConversationID, inbound.Metadata["web_message_id"], inbound.Metadata[protocol.InboundPrincipalMetadataKey] = "main", "fallback", "alice"
	require.NoError(t, rt.RunTurn(t.Context(), inbound))
	readFinal(t, finals)
	require.Empty(t, requests)
	active, err := s.HasActiveTurn(t.Context(), "main")
	require.NoError(t, err)
	require.False(t, active)

	marker := fmt.Sprintf("%d:0", id)
	_, _, err = rt.StageRevert(t.Context(), "main", marker)
	require.NoError(t, err)
	require.NoError(t, rt.RunTurn(t.Context(), inbound))
	inbound.AttachmentPresence = protocol.AttachmentPresenceImages
	require.ErrorContains(t, rt.RunTurn(t.Context(), inbound), "different content")

	workflow := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "$workflow missing"}, true)
	workflow.ConversationID, workflow.Metadata["web_message_id"] = "main", "workflow"
	require.ErrorContains(t, rt.RunTurn(t.Context(), workflow), "not configured")
	current, _, _, err := s.RevertState(t.Context(), "main")
	require.NoError(t, err)
	require.Equal(t, marker, current)

	root, err := os.OpenRoot(b.runtime.Workspace)

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	workflowDir := filepath.Join(b.runtime.RuntimeDirName(), "workflows")
	require.NoError(t, root.MkdirAll(workflowDir, 0o700))
	definition := filepath.Join(workflowDir, "audit.star")
	require.NoError(t, root.WriteFile(definition, []byte("meta = {\"name\": \"audit\", \"description\": \"Audit\", \"phases\": []}\ndef main(args): return \"workflow completed\"\n"), 0o600))

	workflow.Text = "$workflow audit"
	workflow.Metadata[protocol.InboundRawTextMetadataKey] = workflow.Text
	workflow.Workflow = protocol.WorkflowInvocation{Name: "audit"}
	prepared := bridgeRequest{inbound: workflow, completion: &turnCompletion{done: make(chan struct{})}}
	fresh, err := rt.admitWeb(t.Context(), b, &prepared, protocol.GoalRequest{})
	require.NoError(t, err)
	require.True(t, fresh)

	prepared.queueItemID = workflow.Metadata["web_message_id"]

	require.NoError(t, root.Remove(definition), "execution must use the admitted preparation")
	require.NoError(t, b.enqueue(t.Context(), &prepared, "prepared workflow"))
	<-prepared.completion.done
	require.NoError(t, prepared.completion.err)
	require.Equal(t, "workflow completed", readFinal(t, finals).Text)
	workflowMarker, restored, err := rt.StageRevert(t.Context(), "main", "")
	require.NoError(t, err)
	require.Equal(t, "$workflow audit", restored)
	require.NoError(t, rt.RunTurn(t.Context(), workflow), "completed workflow retries reconcile before preparation")
	current, _, _, err = s.RevertState(t.Context(), "main")
	require.NoError(t, err)
	require.Equal(t, workflowMarker, current, "retry cannot commit the newly staged cutoff")

	workflow.Text = "$workflow audit changed"
	workflow.Metadata[protocol.InboundRawTextMetadataKey] = workflow.Text
	require.ErrorContains(t, rt.RunTurn(t.Context(), workflow), "different content")

	restarted := newBridge()
	restarted.config.ConversationID = "main"
	require.NoError(t, restarted.pickLaterWork(t.Context(), false))
	admitted, err := restarted.activateInbound(t.Context(), &bridgeRequest{inbound: inbound})
	require.NoError(t, err)
	require.False(t, admitted, "restarted activation must honor the durable gate")
	require.Empty(t, restarted.requestCh)
	require.Empty(t, requests)
}

func TestRevertHistorySnapshot(t *testing.T) {
	s := newTestSessionService(t)
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	id, err := s.AppendEntryID(t.Context(), "main", testSessionEntry("first", "answer"))
	require.NoError(t, err)
	view, err := s.ObserveHistory(t.Context(), "main", "", 0, 0, 1, HistoryInventory{})
	require.NoError(t, err)
	require.True(t, view.Eligible)
	require.NotEmpty(t, view.Predecessor)

	marker := fmt.Sprintf("%d:0", id)
	_, _, err = stageRevertDB(t.Context(), s.db, "main", marker)
	require.NoError(t, err)
	view, err = s.ObserveHistory(t.Context(), "main", "", 0, 0, 1, view.Inventory)
	require.NoError(t, err)
	require.Equal(t, marker, view.Inventory.Marker)
	require.True(t, view.Reset)
	require.True(t, view.Eligible)
	require.Empty(t, view.Predecessor)
	require.Empty(t, view.Entries)
	require.Zero(t, view.Inventory.From)
}

func TestRevertAdmissionRollbackAndConcurrentIdentity(t *testing.T) {
	s := newTestSessionService(t)
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	id, err := s.AppendEntryID(t.Context(), "main", testSessionEntry("old", "answer"))
	require.NoError(t, err)

	marker := fmt.Sprintf("%d:0", id)
	_, _, err = stageRevertDB(t.Context(), s.db, "main", marker)
	require.NoError(t, err)

	b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, s, b.log, func(Config) directBridge { return b })
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager}
	_, err = s.db.ExecContext(t.Context(), `CREATE FUNCTION reject_admission() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'admission failed'; END $$;
        CREATE TRIGGER reject_admission BEFORE INSERT ON thread_queue FOR EACH ROW EXECUTE FUNCTION reject_admission()`)
	require.NoError(t, err)

	item := protocol.ThreadQueueItem{ID: "new", Kind: protocol.InboundKindHeld, Source: protocol.SourceWeb, Principal: "alice", Message: "edited"}
	require.ErrorContains(t, rt.StashQueueItem(t.Context(), "main", &item), "admission failed")
	current, _, _, err := s.RevertState(t.Context(), "main")
	require.NoError(t, err)
	require.Equal(t, marker, current)

	var raw string
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT entry_json::text FROM session_entries WHERE id = $1`, id).Scan(&raw))
	require.Contains(t, raw, "answer")
	_, err = s.db.ExecContext(t.Context(), `DROP TRIGGER reject_admission ON thread_queue; DROP FUNCTION reject_admission()`)
	require.NoError(t, err)

	var admissions errgroup.Group
	for range 2 {
		admissions.Go(func() error { candidate := item; return rt.StashQueueItem(t.Context(), "main", &candidate) })
	}

	require.NoError(t, admissions.Wait())

	queue, err := s.ThreadQueueForConversation("main")
	require.NoError(t, err)
	require.Len(t, queue, 1)
	require.Equal(t, "new", queue[0].ID)
	require.NoError(t, s.UpsertThread("other", ThreadState{Agent: "main"}))
	other, err := s.AppendEntryID(t.Context(), "other", testSessionEntry("other history", "kept"))
	require.NoError(t, err)
	_, _, err = stageRevertDB(t.Context(), s.db, "other", fmt.Sprintf("%d:0", other))
	require.NoError(t, err)

	otherBridge := &Bridge{config: Config{ConversationID: "other", SessionService: s}, log: b.log, requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager.bridges["other"] = otherBridge

	require.ErrorContains(t, rt.StashQueueItem(t.Context(), "other", &item), "another conversation")
	current, _, _, err = s.RevertState(t.Context(), "other")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d:0", other), current)
	_, _, err = stageRevertDB(t.Context(), s.db, "main", marker)
	require.Error(t, err)
}

func TestRevertGoalIdentityAndPrompt(t *testing.T) {
	s := newTestSessionService(t)
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))

	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindPrompt, &protocol.InboundContent{Text: "objective"}, true)
	inbound.ConversationID, inbound.GoalTurn, inbound.GoalAction = "main", true, protocol.GoalActionKickoff
	inbound.Metadata["web_message_id"], inbound.Metadata["web_goal"], inbound.Metadata["web_goal_prompt"] = "goal", `{"Objective":"objective","MaxTurns":3}`, "$goal max-turns=3 objective"
	require.NoError(t, startTurnDB(t.Context(), s.db, "goal-turn", "main", inbound))

	replay := testSessionEntry("objective with generated goal instructions", "answer")
	replay.TurnID = "goal-turn"
	replay.ReplayInput[0] = json.RawMessage(`{"type":"message","role":"user","input_id":"goal","content":"objective with generated goal instructions"}`)
	_, err := s.finishTurn(t.Context(), "goal-turn", &turnFinish{store: newSessionStore("main", s), entries: []rocketcode.SessionEntry{*replay}, outbound: &protocol.OutboundMessage{ConversationID: "main"}})
	require.NoError(t, err)
	require.NoError(t, s.closeTurn(t.Context(), "goal-turn"))
	_, prompt, err := stageRevertDB(t.Context(), s.db, "main", "")
	require.NoError(t, err)
	require.Equal(t, "$goal max-turns=3 objective", prompt)
	accepted, err := reconcileWebDB(t.Context(), s.db, inbound)
	require.NoError(t, err)
	require.True(t, accepted)

	inbound.Metadata["web_goal"] = `{"Objective":"objective","MaxTurns":5}`
	_, err = reconcileWebDB(t.Context(), s.db, inbound)
	require.ErrorContains(t, err, "different content")
}

func TestRevertBusyAdmissionAndRecordedDrain(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))

	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "waiting"}, true)
	inbound.ConversationID, inbound.Metadata["web_message_id"] = "main", "waiting"
	b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{}), historyMutation: true}
	manager := newThreadBridgeManager(nil, s, b.log, func(Config) directBridge { return b })
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager}

	require.ErrorContains(t, b.enqueue(ctx, &bridgeRequest{inbound: inbound}, "fresh racing input"), "busy")
	require.ErrorContains(t, rt.ClearRevert(ctx, "main"), "busy")
	_, _, err := rt.StageRevert(ctx, "main", "bad")
	require.Error(t, err)
	_, err = rt.PromoteQueueItem(ctx, "main", "waiting")
	require.ErrorContains(t, err, "busy")
	require.Empty(t, b.drainSteers(ctx, rocketcode.TurnPhaseFinalAnswer))

	completion := &turnCompletion{done: make(chan struct{})}
	request := bridgeRequest{inbound: inbound, queueItemID: "waiting", completion: completion}
	activated, err := b.activateInbound(ctx, &request)
	require.NoError(t, err)
	require.False(t, activated)
	require.NoError(t, b.enqueue(ctx, &request, "already accepted input"))

	queue, err := s.ThreadQueueForConversation("main")
	require.NoError(t, err)
	require.Len(t, queue, 1, "activation and enqueue must not create two owners")

	entry := testSessionEntry("waiting", "answer")
	entry.TurnID = "drained-turn"
	entry.ReplayInput[0] = json.RawMessage(`{"type":"message","role":"user","input_id":"waiting","content":"waiting"}`)
	require.NoError(t, startTurnDB(ctx, s.db, entry.TurnID, "main", inbound))
	_, err = s.finishTurn(ctx, entry.TurnID, &turnFinish{store: newSessionStore("main", s), entries: []rocketcode.SessionEntry{*entry}, outbound: &protocol.OutboundMessage{ConversationID: "main"}})
	require.NoError(t, err)
	require.NoError(t, s.closeTurn(ctx, entry.TurnID))
	entries, err := s.ObserveEntries(ctx, "main")
	require.NoError(t, err)

	id := entries[0].ID
	// The drain cursor moved before replay was saved. Saved identity, not that
	// cursor, now settles this input without creating another waiting row.
	b.waiting = nil
	b.steers, b.steersRead = []bridgeRequest{request}, 1
	require.NoError(t, b.settleSteers(ctx, nil))
	<-completion.done
	require.NoError(t, completion.err)
	require.Empty(t, b.waiting)

	queue, err = s.ThreadQueueForConversation("main")
	require.NoError(t, err)
	require.Empty(t, queue, "the recorded input no longer has a queue owner")

	_, _, err = rt.StageRevert(ctx, "main", fmt.Sprintf("%d:0", id))
	require.ErrorContains(t, err, "busy")

	b.historyMutation = false
	_, _, err = rt.StageRevert(ctx, "main", fmt.Sprintf("%d:0", id))
	require.NoError(t, err)
	marker, prompt, err := rt.StageRevert(ctx, "main", "")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d:0", id), marker)
	require.Empty(t, prompt, "Undo at the beginning is a no-op")
}

func TestRevertMarkerGatesDoNotReadReplay(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	// Deliberately unreadable replay makes any full-state predecessor scan fail.
	_, err := s.db.ExecContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('main', '{"type":"turn","replay_input":{}}', '')`)
	require.NoError(t, err)

	b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, s, b.log, func(Config) directBridge { return b })
	manager.bridges["main"] = b
	queue, err := manager.queueItems("main")
	require.NoError(t, err)
	require.Empty(t, queue)
	require.NoError(t, b.pickLaterWork(ctx, false))

	inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "new", true)
	activated, err := b.activateInbound(ctx, &bridgeRequest{inbound: inbound})
	require.NoError(t, err)
	require.True(t, activated, "activation's locked marker check must not parse old replay")

	var boundary int64
	require.NoError(t, s.db.QueryRowContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('main', '{"type":"turn","replay_input":[{"type":"message","role":"user","content":"selected"}]}', '') RETURNING id`).Scan(&boundary))
	_, err = s.db.ExecContext(ctx, `UPDATE managed_conversations SET revert_message_id = $1 WHERE conversation_id = 'main'`, fmt.Sprintf("%d:0", boundary))
	require.NoError(t, err)
	queue, err = manager.queueItems("main")
	require.NoError(t, err)
	require.Empty(t, queue)

	activated, err = b.activateInbound(ctx, &bridgeRequest{inbound: inbound})
	require.NoError(t, err)
	require.False(t, activated)
}

func TestRevertPreservationFailureCompletesOwners(t *testing.T) {
	for _, path := range []string{"steer", "channel", "handle", "activation", "enqueue"} {
		t.Run(path, func(t *testing.T) {
			s := newTestSessionService(t)
			ctx := t.Context()
			require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
			id, err := s.AppendEntryID(ctx, "main", testSessionEntry("old", "answer"))
			require.NoError(t, err)

			inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "waiting"}, true)
			inbound.ConversationID, inbound.Metadata["web_message_id"] = "main", "waiting"
			completion := &turnCompletion{done: make(chan struct{})}
			request := bridgeRequest{inbound: inbound, queueItemID: "waiting", completion: completion}
			require.NoError(t, s.PutThreadQueueItem("waiting", &protocol.ThreadQueueItem{ID: "waiting", ConversationID: "main", Source: protocol.SourceWeb, Kind: protocol.InboundKindEnqueue, Message: inbound.Text, Inbound: inbound}))
			_, err = s.db.ExecContext(ctx, `CREATE FUNCTION reject_preservation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'preservation failed'; END $$;
				CREATE TRIGGER reject_preservation BEFORE UPDATE ON thread_queue FOR EACH ROW EXECUTE FUNCTION reject_preservation()`)
			require.NoError(t, err)

			b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{}), historyMutation: path != "channel"}
			manager := newThreadBridgeManager(nil, s, b.log, func(Config) directBridge { return b })
			manager.bridges["main"] = b
			rt := &Runtime{Sessions: s, threads: manager}

			switch path {
			case "steer":
				b.steers = []bridgeRequest{request}
				err = b.settleSteers(ctx, nil)
			case "channel":
				b.requestCh <- request

				_, _, err = rt.StageRevert(ctx, "main", fmt.Sprintf("%d:0", id))
			case "handle":
				b.handle(ctx, &request)

				err = completion.err
			case "activation":
				_, err = b.activateInbound(ctx, &request)
			case "enqueue":
				err = b.enqueue(ctx, &request, "waiting")
			}

			require.ErrorContains(t, err, "preservation failed")

			select {
			case <-completion.done:
				require.ErrorContains(t, completion.err, "preservation failed")
			default:
				t.Fatal("failed preservation orphaned its completion")
			}

			require.Empty(t, b.waiting)

			_, err = s.db.ExecContext(ctx, `DROP TRIGGER reject_preservation ON thread_queue`)
			require.NoError(t, err)
			queue, err := s.ThreadQueueForConversation("main")
			require.NoError(t, err)
			require.Len(t, queue, 1)
			require.Equal(t, "waiting", queue[0].ID)
			require.Equal(t, inbound.Text, queue[0].Inbound.Text)

			b.historyMutation = true
			recovered := bridgeRequest{inbound: queue[0].Inbound, queueItemID: queue[0].ID}
			require.NoError(t, b.enqueue(ctx, &recovered, "recovery"))
			require.Len(t, b.waiting, 1)
			require.NoError(t, b.settleSteers(ctx, nil), "teardown must not complete the failed owner twice")

			queue, err = s.ThreadQueueForConversation("main")
			require.NoError(t, err)
			require.Len(t, queue, 1)
		})
	}
}

func TestRevertBranchCancelsPreservedOwners(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	id, err := s.AppendEntryID(ctx, "main", testSessionEntry("old", "answer"))
	require.NoError(t, err)
	_, _, err = stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:0", id))
	require.NoError(t, err)

	b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, s, b.log, func(Config) directBridge { return b })
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager}

	for _, id := range []string{"withdraw", "discard"} {
		inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: id}, true)
		inbound.ConversationID, inbound.Metadata["web_message_id"] = "main", id
		request := bridgeRequest{inbound: inbound, queueItemID: id, completion: &turnCompletion{done: make(chan struct{})}}

		b.mu.Lock()
		err = b.preserveRevertRequestLocked(ctx, &request)
		b.mu.Unlock()
		require.NoError(t, err)
	}

	withdrawn, discarded := b.waiting[0].completion, b.waiting[1].completion
	changed, err := rt.DeleteQueueItem(ctx, "main", "discard")
	require.NoError(t, err)
	require.True(t, changed)
	<-discarded.done
	require.ErrorIs(t, discarded.err, context.Canceled)

	item := protocol.ThreadQueueItem{ID: "replacement", Source: protocol.SourceWeb, Kind: protocol.InboundKindHeld, Message: "edited"}
	require.NoError(t, rt.StashQueueItem(ctx, "main", &item))
	<-withdrawn.done
	require.ErrorIs(t, withdrawn.err, context.Canceled)
	require.Empty(t, b.waiting)

	queue, err := s.ThreadQueueForConversation("main")
	require.NoError(t, err)
	require.Len(t, queue, 1)
	require.Equal(t, "replacement", queue[0].ID)
}

func TestRevertLegacyQueuePromotionAndEnqueue(t *testing.T) {
	newBridge, s, requests, finals := newResumeTestBridges(t, false)
	b := newBridge()
	b.config.ConversationID = "main"

	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	manager := newThreadBridgeManager(b.runtime, s, b.log, func(Config) directBridge { return b })
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager, Cfg: b.runtime}
	runTestBridge(t, b)
	require.NoError(t, s.PutThreadQueueItem("legacy", &protocol.ThreadQueueItem{ID: "legacy", ConversationID: "main", Source: protocol.SourceWeb, Principal: "alice", Message: "legacy request", Kind: protocol.InboundKindEnqueue}))
	changed, err := rt.PromoteQueueItem(t.Context(), "main", "legacy")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "main", readFinal(t, finals).ConversationID)

	request := <-requests
	require.Contains(t, request, "legacy request")
	require.Contains(t, request, "alice")

	item := protocol.ThreadQueueItem{ID: "queued", Source: protocol.SourceWeb, Principal: "bob", Message: "new queue request", Kind: protocol.InboundKindEnqueue}
	require.NoError(t, rt.StashQueueItem(t.Context(), "main", &item))
	require.Equal(t, "main", readFinal(t, finals).ConversationID)
	require.Contains(t, <-requests, "new queue request")
}

func TestRevertAdmissionRollbackAtEveryWrite(t *testing.T) {
	for _, failure := range []struct{ table, operation string }{
		{"session_entries", "DELETE"}, {"session_entries", "UPDATE"},
		{"active_turns", "DELETE"}, {"turn_steps", "DELETE"},
		{"thread_queue", "DELETE"}, {"managed_conversations", "UPDATE"},
		{"session_summaries", "DELETE"}, {"session_summaries", "INSERT"},
		{"thread_queue", "INSERT"},
	} {
		t.Run(failure.table+"/"+failure.operation, func(t *testing.T) {
			s := newTestSessionService(t)
			ctx := t.Context()
			require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))

			entry := testSessionEntry("keep", "kept")
			entry.ReplayInput = append(entry.ReplayInput, testSessionEntry("old", "abandoned").ReplayInput...)
			entry.TurnID = "old-turn"
			id, err := s.AppendEntryID(ctx, "main", entry)
			require.NoError(t, err)
			_, err = s.AppendEntryID(ctx, "main", testSessionEntry("later", "later answer"))
			require.NoError(t, err)

			inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindSteer, "old", true)
			require.NoError(t, startTurnDB(ctx, s.db, "old-turn", "main", inbound))
			_, err = s.db.ExecContext(ctx, `UPDATE active_turns SET phase = 'done'; INSERT INTO turn_steps (conversation_id, key, value) VALUES ('main', 'old-turn/retry/1', '{}')`)
			require.NoError(t, err)
			require.NoError(t, s.PutThreadQueueItem("waiting", &protocol.ThreadQueueItem{ID: "waiting", ConversationID: "main", Source: protocol.SourceWeb, Message: "pending"}))

			marker := fmt.Sprintf("%d:2", id)
			_, _, err = stageRevertDB(ctx, s.db, "main", marker)
			require.NoError(t, err)

			const snapshot = `SELECT jsonb_build_array(
				(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM session_entries e),
				(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM active_turns a),
				(SELECT jsonb_agg(to_jsonb(j) ORDER BY key) FROM turn_steps j),
				(SELECT jsonb_agg(to_jsonb(q) ORDER BY queue_item_id) FROM thread_queue q),
				(SELECT jsonb_agg(to_jsonb(m) ORDER BY conversation_id) FROM managed_conversations m),
				(SELECT jsonb_agg(to_jsonb(s) ORDER BY conversation_id) FROM session_summaries s))::text`

			var before, after string
			require.NoError(t, s.db.QueryRowContext(ctx, snapshot).Scan(&before))
			_, err = s.db.ExecContext(ctx, `CREATE FUNCTION reject_branch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'branch write failed'; END $$`)
			require.NoError(t, err)
			_, err = s.db.ExecContext(ctx, fmt.Sprintf("CREATE TRIGGER reject_branch BEFORE %s ON %s FOR EACH ROW EXECUTE FUNCTION reject_branch()", failure.operation, failure.table))
			require.NoError(t, err)

			b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler)}
			rt := &Runtime{Sessions: s}
			replacement := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindHeld, &protocol.InboundContent{Text: "edited"}, true)
			replacement.ConversationID, replacement.Metadata["web_message_id"] = "main", "replacement"
			fresh, err := rt.admitWeb(ctx, b, &bridgeRequest{inbound: replacement}, protocol.GoalRequest{})
			require.ErrorContains(t, err, "branch write failed")
			require.False(t, fresh)
			require.NoError(t, s.db.QueryRowContext(ctx, snapshot).Scan(&after))
			require.JSONEq(t, before, after, "cutoff, suffix, journals, pending inputs and summary must all survive failed admission")
		})
	}
}

func TestRevertStorageFailuresAreNotEmptySuccess(t *testing.T) {
	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, s, b.log, func(Config) directBridge { return b })
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager}
	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "new"}, true)
	inbound.ConversationID, inbound.Metadata["web_message_id"] = "main", "new"

	require.NoError(t, s.Stop())
	marker, _, _, err := s.RevertState(ctx, "main")
	require.ErrorContains(t, err, "database is closed")
	require.Empty(t, marker)

	_, _, err = rt.StageRevert(ctx, "main", "1:0")
	require.ErrorContains(t, err, "database is closed")
	require.ErrorContains(t, rt.ClearRevert(ctx, "main"), "database is closed")
	require.ErrorContains(t, rt.RunTurn(ctx, inbound), "database is closed")
	require.ErrorContains(t, rt.StartGoal(ctx, inbound, protocol.GoalRequest{Objective: "new"}), "database is closed")
	_, err = rt.PopQueueItem(ctx, "main", "new")
	require.ErrorContains(t, err, "database is closed")
	_, err = rt.PromoteQueueItem(ctx, "main", "new")
	require.ErrorContains(t, err, "database is closed")
	require.ErrorContains(t, rt.StashQueueItem(ctx, "main", &protocol.ThreadQueueItem{Source: protocol.SourceWeb}), "database is closed")
	_, err = rt.mutateWebQueue(ctx, "main", "new", protocol.InboundKindSteer)
	require.ErrorContains(t, err, "database is closed")
	_, err = rt.admitWeb(ctx, b, &bridgeRequest{inbound: inbound}, protocol.GoalRequest{})
	require.ErrorContains(t, err, "database is closed")

	completion := &turnCompletion{done: make(chan struct{})}
	b.handle(ctx, &bridgeRequest{inbound: inbound, completion: completion})
	<-completion.done
	require.ErrorContains(t, completion.err, "database is closed")
	_, err = s.Delegations(ctx, "main", "", 0, 0)
	require.ErrorContains(t, err, "database is closed")
	_, _, err = stageRevertDB(ctx, s.db, "main", "1:0")
	require.ErrorContains(t, err, "database is closed")
	require.ErrorContains(t, s.closeTurn(ctx, "turn"), "database is closed")
	require.ErrorContains(t, s.PutThreadQueueItem("new", &protocol.ThreadQueueItem{}), "database is closed")
	require.Empty(t, b.requestCh, "storage failure must never wake execution")
}

func TestRevertStageFailurePreservesHistory(t *testing.T) {
	for _, unavailable := range []string{"conversation_goals", "active_turns", "managed_conversations update", "settled delivery"} {
		t.Run(unavailable, func(t *testing.T) {
			s := newTestSessionService(t)
			ctx := t.Context()
			require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
			id, err := s.AppendEntryID(ctx, "main", testSessionEntry("original", "answer"))
			require.NoError(t, err)

			b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
			manager := newThreadBridgeManager(nil, s, b.log, func(Config) directBridge { return b })
			manager.bridges["main"] = b
			rt := &Runtime{Sessions: s, threads: manager}

			switch unavailable {
			case "managed_conversations update":
				_, err = s.db.ExecContext(ctx, `CREATE FUNCTION reject_stage() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'stage failed'; END $$;
					CREATE TRIGGER reject_stage BEFORE UPDATE ON managed_conversations FOR EACH ROW EXECUTE FUNCTION reject_stage()`)
			case "settled delivery":
				err = startTurnDB(ctx, s.db, "delivering", "main", protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "running", true))
				require.NoError(t, err)
				_, err = s.finishTurn(ctx, "delivering", &turnFinish{store: newSessionStore("main", s), outbound: &protocol.OutboundMessage{ConversationID: "main"}})
			default:
				_, err = s.db.ExecContext(ctx, "ALTER TABLE "+unavailable+" RENAME TO unavailable_stage_table")
			}

			require.NoError(t, err)
			_, _, err = rt.StageRevert(ctx, "main", fmt.Sprintf("%d:0", id))
			require.Error(t, err)
			require.False(t, b.historyMutation, "failed staging must release the transient barrier")

			var marker string
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT revert_message_id FROM managed_conversations WHERE conversation_id = 'main'`).Scan(&marker))
			require.Empty(t, marker)

			var raw string
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT entry_json::text FROM session_entries WHERE id = $1`, id).Scan(&raw))
			require.Contains(t, raw, "answer")
		})
	}
}

func TestRevertCorruptHistoryCannotBeAdmitted(t *testing.T) {
	for _, corrupt := range []struct{ name, raw string }{
		{"trace", `{"type":"turn","output_trace":1,"replay_input":[{"role":"user","type":"message","content":"old"}]}`},
		{"replay", `{"type":"turn","replay_input":[{"role":"user","type":"message","input_id":"old","prompt_header":42,"content":"old"}]}`},
	} {
		t.Run(corrupt.name, func(t *testing.T) {
			s := newTestSessionService(t)
			ctx := t.Context()
			require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))

			var id int64
			require.NoError(t, s.db.QueryRowContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('main', $1, '') RETURNING id`, corrupt.raw).Scan(&id))
			_, _, err := stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:0", id))
			require.Error(t, err)

			_, err = s.ObserveHistory(ctx, "main", "", 0, 0, 0, HistoryInventory{})
			if corrupt.name == "trace" {
				require.Error(t, err)
			}

			inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "old"}, true)

			inbound.ConversationID, inbound.Metadata["web_message_id"] = "main", "old"
			if corrupt.name == "replay" {
				_, err = reconcileWebDB(ctx, s.db, inbound)
				require.Error(t, err)
			}
		})
	}
}

func TestRevertSavedAttachmentRetryIdentity(t *testing.T) {
	s := newTestSessionService(t)
	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "original", Attachments: []protocol.InboundAttachment{
		{Name: "photo.png", MIMEType: "image/png", Data: []byte("image bytes")},
		{Name: "notes.pdf", MIMEType: "application/pdf", Data: []byte("file bytes")},
	}}, true)
	inbound.ConversationID, inbound.Metadata["web_message_id"], inbound.Metadata[protocol.InboundPrincipalMetadataKey] = "main", "original", "alice"
	prompt := buildPrompt(inbound, nil)
	header, _, _ := strings.Cut(prompt, "\n\n")
	attachments := attachmentsFromInbound(inbound.Attachments)
	raw, err := json.Marshal(struct {
		Type    string            `json:"type"`
		Role    string            `json:"role"`
		Content []portableContent `json:"content"`
		Header  string            `json:"prompt_header"`
		InputID string            `json:"input_id"`
	}{Type: "message", Role: "user", Header: header, InputID: "original", Content: []portableContent{
		{Type: "input_text", Text: prompt}, {Type: "input_image", ImageURL: attachments[0].URL}, {Type: "input_file", FileData: attachments[1].URL, Filename: attachments[1].Filename},
	}})
	require.NoError(t, err)
	_, err = s.AppendEntryID(t.Context(), "main", &rocketcode.SessionEntry{Type: "turn", ReplayInput: []json.RawMessage{raw}})
	require.NoError(t, err)
	accepted, err := reconcileWebDB(t.Context(), s.db, inbound)
	require.NoError(t, err)
	require.True(t, accepted)

	inbound.Attachments[1].Data = []byte("different bytes")
	_, err = reconcileWebDB(t.Context(), s.db, inbound)
	require.ErrorContains(t, err, "different content")
}

func TestRevertRacingSteerDrainKeepsWaitingOwner(t *testing.T) {
	s := newTestSessionService(t)
	draining, release := make(chan struct{}), make(chan struct{})
	b := &Bridge{config: Config{ConversationID: "main", SessionService: s, SteerDrain: rocketcode.SteerDrain{Fn: func(context.Context, rocketcode.TurnPhase) []rocketcode.PromptInput {
		close(draining)
		<-release

		return []rocketcode.PromptInput{{ID: "external", Text: "already drained"}}
	}}}, log: slog.New(slog.DiscardHandler)}
	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "waiting"}, true)
	inbound.ConversationID, inbound.Metadata["web_message_id"] = "main", "waiting"
	completion := &turnCompletion{done: make(chan struct{})}
	b.steers = []bridgeRequest{{inbound: inbound, queueItemID: "waiting", completion: completion}}

	inputs := make(chan []rocketcode.PromptInput, 1)
	go func() { inputs <- b.drainSteers(t.Context(), rocketcode.TurnPhaseFinalAnswer) }()

	<-draining
	b.mu.Lock()
	b.historyMutation = true
	b.mu.Unlock()
	close(release)
	require.Equal(t, []rocketcode.PromptInput{{ID: "external", Text: "already drained"}}, <-inputs)
	require.Zero(t, b.steersRead, "a barrier installed during the external drain must not consume waiting Web inputs")
	require.NoError(t, b.settleSteers(t.Context(), nil))

	queue, err := s.ThreadQueueForConversation("main")
	require.NoError(t, err)
	require.Len(t, queue, 1)
	require.Equal(t, "waiting", queue[0].ID)
	require.Same(t, completion, b.waiting[0].completion)

	select {
	case <-completion.done:
		t.Fatal("an uninjected steer must not be reported executed")
	default:
	}
}
