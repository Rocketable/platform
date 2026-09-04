package backend

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func TestRuntimeJoinOriginatorAndSteers(t *testing.T) {
	rt := RuntimeFor()
	id := protocol.WebSessionConversationID("ops")
	live := rt.Subscribe(t.Context())

	origin := protocol.NewOutboundMessage(protocol.SourceSystem, id, "popped later", protocol.OutputTargetWeb)
	origin.Originator = true
	origin.Complete = true
	require.Equal(t, protocol.BroadcastHandled, rt.HandleBroadcast(t.Context(), &protocol.Broadcast{Message: origin, Delivery: origin}).Status)
	require.NoError(t, origin.WaitDelivered(t.Context()))

	got := <-live
	require.Equal(t, "popped later", got.Text)
	require.Equal(t, "user", got.Role)

	thought := protocol.NewOutboundMessage(protocol.SourceSystem, id, "", protocol.OutputTargetSlack)
	thought.ProgressText = "Read"
	require.Equal(t, protocol.BroadcastDropped, rt.HandleBroadcast(t.Context(), &protocol.Broadcast{Message: thought, Delivery: thought}).Status)

	got = <-live
	require.Equal(t, "thinking", got.Role)
	require.Equal(t, "Read", got.Text)

	rt.PushSteer(id, "steer me")
	require.Equal(t, []string{"steer me"}, rt.TakeSteers(id))
	require.Empty(t, rt.TakeSteers(id))
}

func TestRuntimeCreateThenRunTurnPrompt(t *testing.T) {
	rt := testConversationRuntime(t, new(completingTurnBridge))
	require.NoError(t, rt.CreateConversation("conv-1", []string{"main"}, nil))
	require.NoError(t, rt.RunTurn(t.Context(), &protocol.TurnRequest{ID: "conv-1", Kind: protocol.TurnPrompt, Text: "hello"}))

	listed, err := rt.ListConversations()
	require.NoError(t, err)
	require.Equal(t, []protocol.ConversationRecord{{ID: "conv-1", Agents: []string{"main"}}}, listed)
}

func TestRuntimeListConversationsExternalMCPPair(t *testing.T) {
	rt := testConversationRuntime(t, new(completingTurnBridge))
	xID := "external_mcp:planner:private"
	yID := protocol.SlackThreadConversationID("C1", "1.000")
	require.NoError(t, rt.Sessions.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{
		Agent: "planner", PrivateConversationID: xID, ManagedConversationID: yID, SlackChannel: "#ops",
	}))
	require.NoError(t, rt.CreateConversation(yID, []string{"main"}, []protocol.ConversationTag{protocol.ConversationUserFacing}))

	listed, err := rt.ListConversations()
	require.NoError(t, err)

	byID := map[string]protocol.ConversationRecord{}
	for _, rec := range listed {
		byID[rec.ID] = rec
	}

	require.NotContains(t, byID, xID)
	require.Contains(t, byID, yID)
	require.Equal(t, []string{"main"}, byID[yID].Agents)
	require.Contains(t, byID[yID].Tags, protocol.ConversationUserFacing)

	require.NoError(t, rt.CreateConversation(xID, []string{"planner"}, nil))
	listed, err = rt.ListConversations()
	require.NoError(t, err)

	byID = map[string]protocol.ConversationRecord{}
	for _, rec := range listed {
		byID[rec.ID] = rec
	}

	require.Contains(t, byID, xID)
	require.Equal(t, []string{"planner"}, byID[xID].Agents)
	require.NotContains(t, byID[xID].Tags, protocol.ConversationUserFacing)
}

func TestRuntimeListConversationsUserFacingFromCreatedBy(t *testing.T) {
	rt := testConversationRuntime(t, new(completingTurnBridge))
	slackID := protocol.SlackThreadConversationID("C1", "1.000")

	require.NoError(t, rt.CreateConversation("locked", []string{"planner"}, nil))
	require.NoError(t, rt.CreateConversation("visible", []string{"main"}, []protocol.ConversationTag{protocol.ConversationUserFacing}))
	require.NoError(t, rt.CreateConversation("cron-y", []string{"main"}, []protocol.ConversationTag{protocol.ConversationUserFacing, protocol.ConversationCron}))
	require.NoError(t, rt.CreateConversation(slackID, []string{"main"}, nil))

	listed, err := rt.ListConversations()
	require.NoError(t, err)

	byID := map[string]protocol.ConversationRecord{}
	for _, rec := range listed {
		byID[rec.ID] = rec
	}

	require.Equal(t, protocol.ConversationRecord{ID: "locked", Agents: []string{"planner"}}, byID["locked"])
	require.Equal(t, protocol.ConversationRecord{ID: "visible", Agents: []string{"main"}, Tags: []protocol.ConversationTag{protocol.ConversationUserFacing}}, byID["visible"])
	require.Equal(t, protocol.ConversationRecord{ID: "cron-y", Agents: []string{"main"}, Tags: []protocol.ConversationTag{protocol.ConversationCron, protocol.ConversationUserFacing}}, byID["cron-y"])
	require.Equal(t, protocol.ConversationRecord{ID: slackID, Agents: []string{"main"}}, byID[slackID])
}

func TestRuntimeRunTurnUnknownIDFails(t *testing.T) {
	rt := testConversationRuntime(t, new(completingTurnBridge))
	err := rt.RunTurn(t.Context(), &protocol.TurnRequest{ID: "missing", Kind: protocol.TurnPrompt, Text: "hello"})
	require.ErrorContains(t, err, "unknown conversation missing")
}

func TestRuntimeRunTurnsOnOneIDCompleteInOneOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bridge := new(recordingTurnBridge)
		rt := testConversationRuntime(t, bridge)
		require.NoError(t, rt.CreateConversation("conv-1", []string{"main"}, nil))

		errs := make(chan error, 2)
		go func() {
			errs <- rt.RunTurn(t.Context(), &protocol.TurnRequest{ID: "conv-1", Kind: protocol.TurnPrompt, Text: "a"})
		}()
		go func() {
			errs <- rt.RunTurn(t.Context(), &protocol.TurnRequest{ID: "conv-1", Kind: protocol.TurnPrompt, Text: "b"})
		}()

		require.NoError(t, <-errs)
		require.NoError(t, <-errs)
		require.ElementsMatch(t, []string{"a", "b"}, bridge.texts())
	})
}

func TestRuntimeEnqueueReturnsAfterPoppedTurn(t *testing.T) {
	bridge := new(enqueueTurnBridge)
	rt := testConversationRuntime(t, bridge)
	bridge.store = rt.Sessions
	require.NoError(t, rt.CreateConversation("conv-1", []string{"main"}, nil))
	require.NoError(t, rt.RunTurn(t.Context(), &protocol.TurnRequest{ID: "conv-1", Kind: protocol.TurnEnqueue, Text: "later"}))
	require.True(t, bridge.picked)
}

func TestRuntimeCancelEndsInFlightTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bridge := newBlockingTurnBridge()
		rt := testConversationRuntime(t, bridge)
		require.NoError(t, rt.CreateConversation("conv-1", []string{"main"}, nil))

		done := make(chan error, 1)
		go func() {
			done <- rt.RunTurn(t.Context(), &protocol.TurnRequest{ID: "conv-1", Kind: protocol.TurnPrompt, Text: "hang"})
		}()

		<-bridge.started
		synctest.Wait()
		require.NoError(t, rt.RunTurn(t.Context(), &protocol.TurnRequest{ID: "conv-1", Kind: protocol.TurnCancel}))
		require.Error(t, <-done)
	})
}

func TestRuntimeSteerInjectsIntoInFlightTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bridge := newBlockingTurnBridge()
		rt := testConversationRuntime(t, bridge)
		require.NoError(t, rt.CreateConversation("conv-1", []string{"main"}, nil))

		done := make(chan error, 1)
		go func() {
			done <- rt.RunTurn(t.Context(), &protocol.TurnRequest{ID: "conv-1", Kind: protocol.TurnPrompt, Text: "hang"})
		}()

		<-bridge.started
		synctest.Wait()
		require.NoError(t, rt.RunTurn(t.Context(), &protocol.TurnRequest{ID: "conv-1", Kind: protocol.TurnSteer, Text: "steer me"}))
		require.Equal(t, []string{"steer me"}, rt.TakeSteers("conv-1"))
		require.Len(t, bridge.submits, 1)
		require.Equal(t, "hang", bridge.submits[0].Text)
		bridge.complete()
		synctest.Wait()
		require.NoError(t, <-done)
	})
}

func TestRuntimeSteerWhenIdleDoesNotStartPrompt(t *testing.T) {
	bridge := new(recordingTurnBridge)
	rt := testConversationRuntime(t, bridge)
	require.NoError(t, rt.CreateConversation("conv-1", []string{"main"}, nil))
	require.NoError(t, rt.RunTurn(t.Context(), &protocol.TurnRequest{ID: "conv-1", Kind: protocol.TurnSteer, Text: "steer me"}))
	require.Empty(t, bridge.texts())
	require.Empty(t, rt.TakeSteers("conv-1"))
}

func TestRuntimeSyncWaitsThenCopies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bridge := newBlockingTurnBridge()
		rt := testConversationRuntime(t, bridge)
		require.NoError(t, rt.CreateConversation("src", []string{"main"}, nil))
		require.NoError(t, rt.CreateConversation("dst", []string{"main", "review"}, nil))
		_, err := rt.Sessions.AppendEntryID(t.Context(), "src", testSessionEntry("user", "copied"))
		require.NoError(t, err)

		done := make(chan error, 1)
		go func() {
			done <- rt.RunTurn(t.Context(), &protocol.TurnRequest{ID: "dst", Kind: protocol.TurnPrompt, Text: "busy"})
		}()

		<-bridge.started
		synctest.Wait()

		synced := make(chan error, 1)
		go func() {
			synced <- rt.SyncConversation(t.Context(), "src", "dst")
		}()

		synctest.Wait()

		select {
		case err := <-synced:
			t.Fatalf("sync returned before dst idle: %v", err)
		default:
		}

		bridge.complete()
		synctest.Wait()
		require.NoError(t, <-done)
		require.NoError(t, <-synced)

		entries, err := rt.Sessions.ObserveEntries(t.Context(), "dst", 0)
		require.NoError(t, err)
		require.NotEmpty(t, entries)
	})
}

func TestRuntimeSyncMissingDstFails(t *testing.T) {
	rt := testConversationRuntime(t, new(completingTurnBridge))
	require.NoError(t, rt.CreateConversation("src", []string{"main"}, nil))
	err := rt.SyncConversation(t.Context(), "src", "missing")
	require.ErrorContains(t, err, "unknown conversation missing")
}

func TestRuntimeSubscribeDeliversAfterSyncNotReplay(t *testing.T) {
	rt := testConversationRuntime(t, new(completingTurnBridge))
	require.NoError(t, rt.CreateConversation("src", []string{"main"}, nil))
	require.NoError(t, rt.CreateConversation("dst", []string{"main"}, nil))
	_, err := rt.Sessions.AppendEntryID(t.Context(), "src", testSessionEntry("user", "copied"))
	require.NoError(t, err)

	live := rt.Subscribe(t.Context())
	require.NoError(t, rt.SyncConversation(t.Context(), "src", "dst"))

	got := <-live
	require.Equal(t, "dst", got.ConversationID)
	require.Equal(t, "copied", got.Text)
	require.Equal(t, "assistant", got.Role)
	require.True(t, got.Complete)

	late := rt.Subscribe(t.Context())
	select {
	case <-late:
		t.Fatal("subscribe replayed history")
	default:
	}
}

func testConversationRuntime(t *testing.T, bridge directBridge) *Runtime {
	t.Helper()

	store := newWorkspaceSessionService(t)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	return &Runtime{Sessions: store, threads: manager, Channels: protocol.NewChannels()}
}

type completingTurnBridge struct {
	fakeDirectBridge
}

func (b *completingTurnBridge) Submit(ctx context.Context, msg *protocol.InboundMessage) error {
	if err := b.fakeDirectBridge.Submit(ctx, msg); err != nil {
		return err
	}

	msg.CompleteResponse("ok", nil)

	return nil
}

func (b *completingTurnBridge) SubmitWhenActive(ctx context.Context, msg *protocol.InboundMessage, activation protocol.ActivationHook) error {
	if err := activation(ctx, msg); err != nil {
		return err
	}

	return b.Submit(ctx, msg)
}

type recordingTurnBridge struct {
	completingTurnBridge
}

func (b *recordingTurnBridge) texts() []string {
	out := make([]string, 0, len(b.submits))
	for _, msg := range b.submits {
		out = append(out, msg.Text)
	}

	return out
}

type enqueueTurnBridge struct {
	fakeDirectBridge

	store  *SessionService
	picked bool
}

func (b *enqueueTurnBridge) PickLaterWork(context.Context) error {
	b.picked = true

	items, err := b.store.ThreadQueueForConversation("conv-1")
	if err != nil {
		return err
	}

	for i := range items {
		if waiter := b.store.TakeMCPWaiter(items[i].ID); waiter != nil {
			waiter.CompleteResponse("popped", nil)
		}

		if err := b.store.DeleteThreadQueueItem(items[i].ID); err != nil {
			return err
		}
	}

	return nil
}

type blockingTurnBridge struct {
	fakeDirectBridge

	mu      sync.Mutex
	active  *protocol.InboundMessage
	started chan struct{}
}

func newBlockingTurnBridge() *blockingTurnBridge {
	return &blockingTurnBridge{started: make(chan struct{})}
}

func (b *blockingTurnBridge) Submit(ctx context.Context, msg *protocol.InboundMessage) error {
	if err := b.fakeDirectBridge.Submit(ctx, msg); err != nil {
		return err
	}

	b.mu.Lock()
	b.active = msg
	started := b.started
	b.mu.Unlock()
	close(started)

	return nil
}

func (b *blockingTurnBridge) SubmitWhenActive(ctx context.Context, msg *protocol.InboundMessage, activation protocol.ActivationHook) error {
	if err := activation(ctx, msg); err != nil {
		return err
	}

	return b.Submit(ctx, msg)
}

func (b *blockingTurnBridge) Handling() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.active != nil
}

func (b *blockingTurnBridge) InterruptActiveTurn() *protocol.InboundMessage {
	b.mu.Lock()
	msg := b.active
	b.active = nil
	b.mu.Unlock()

	if msg != nil {
		msg.CompleteResponse("", context.Canceled)
	}

	return msg
}

func (b *blockingTurnBridge) complete() {
	b.mu.Lock()
	msg := b.active
	b.active = nil
	b.mu.Unlock()

	if msg != nil {
		msg.CompleteResponse("ok", nil)
	}
}

func TestRuntimeLaterWorkListDeleteReorder(t *testing.T) {
	rt := testConversationRuntime(t, new(completingTurnBridge))
	require.NoError(t, rt.CreateConversation("conv-1", []string{"main"}, nil))
	require.NoError(t, rt.Sessions.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: "conv-1", Message: "later", Position: 0}))
	require.NoError(t, rt.Sessions.PutThreadQueueItem("q2", &protocol.ThreadQueueItem{ID: "q2", ConversationID: "conv-1", Message: "also", Position: 1}))

	items, err := rt.ListLaterWork(t.Context(), "conv-1")
	require.NoError(t, err)
	require.Equal(t, []string{"q1", "q2"}, []string{items[0].ID, items[1].ID})

	require.NoError(t, rt.ReorderLaterWork(t.Context(), "conv-1", []string{"q2", "q1"}))
	items, err = rt.ListLaterWork(t.Context(), "conv-1")
	require.NoError(t, err)
	require.Equal(t, []string{"q2", "q1"}, []string{items[0].ID, items[1].ID})

	require.NoError(t, rt.DeleteLaterWork(t.Context(), "conv-1", "q2"))
	items, err = rt.ListLaterWork(t.Context(), "conv-1")
	require.NoError(t, err)
	require.Equal(t, []string{"q1"}, []string{items[0].ID})
	require.False(t, rt.ConversationBusy("conv-1"))
}

func TestRuntimeHasNoCronAPI(t *testing.T) {
	src, err := os.ReadFile("runtime.go")
	require.NoError(t, err)

	inStruct := false

	for line := range strings.SplitSeq(string(src), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "type Runtime struct") {
			inStruct = true
			continue
		}

		if inStruct {
			if trim == "}" {
				inStruct = false
				continue
			}

			fields := strings.Fields(trim)
			if len(fields) >= 2 && fields[0][0] >= 'A' && fields[0][0] <= 'Z' {
				require.NotEqual(t, "Cron", fields[0])
				require.NotContains(t, strings.ToLower(fields[1]), "cron")
			}

			continue
		}

		if !strings.HasPrefix(trim, "func (r *Runtime) ") {
			continue
		}

		name := strings.ToLower(trim)
		require.NotContains(t, name, "cron")
		require.NotContains(t, name, "slack")
		require.NotContains(t, name, "web")
		require.NotContains(t, name, "externalmcp")
	}
}
