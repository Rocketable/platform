package app

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/stretchr/testify/require"
)

type clockworkTestBridge struct {
	received chan *events.Broadcast
	status   events.BroadcastStatus
}

func (b *clockworkTestBridge) HandleBroadcast(_ context.Context, broadcast *events.Broadcast) events.BroadcastAcknowledgement {
	b.received <- broadcast

	return events.BroadcastAcknowledgement{Status: b.status}
}

type blockingClockworkTestBridge struct {
	received chan *events.Broadcast
	release  chan struct{}
}

func runClockwork(ctx context.Context, t *testing.T, clockwork *clockwork) {
	t.Helper()

	go func() {
		if err := clockwork.run(ctx); err != nil {
			t.Errorf("clockwork.run: %v", err)
		}
	}()
}

func (b *blockingClockworkTestBridge) HandleBroadcast(ctx context.Context, broadcast *events.Broadcast) events.BroadcastAcknowledgement {
	b.received <- broadcast

	select {
	case <-b.release:
		return events.BroadcastAcknowledgement{Status: events.BroadcastHandled}
	case <-ctx.Done():
		return events.BroadcastAcknowledgement{Status: events.BroadcastFailed, Err: ctx.Err()}
	}
}

func TestClockworkBuffersBroadcastBeforeBridges(t *testing.T) {
	channels := events.NewChannels()
	clockwork := newClockwork(channels)
	clockwork.pendingEnabled = true
	message := events.NewOutboundMessage(events.SourceSystem, "conversation", "buffered")
	clockwork.dispatch(&events.Broadcast{Message: message, Delivery: message})
	require.Len(t, clockwork.pending, 1)
	require.Equal(t, "buffered", clockwork.pending[0].Message.Text)
}

func TestClockworkRegisterBridgeDuplicate(t *testing.T) {
	channels := events.NewChannels()
	clockwork := newClockwork(channels)
	bridge := &clockworkTestBridge{received: make(chan *events.Broadcast, 1), status: events.BroadcastHandled}
	unregister, err := clockwork.registerBridge(events.BridgeSlack, bridge)
	require.NoError(t, err)
	_, err = clockwork.registerBridge(events.BridgeSlack, bridge)
	require.Error(t, err)
	unregister()
}

func TestClockworkRunRejectsSecondStart(t *testing.T) {
	channels := events.NewChannels()
	clockwork := newClockwork(channels)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- clockwork.run(ctx) }()
	// wait until started
	for {
		clockwork.mu.Lock()
		started := clockwork.started
		clockwork.mu.Unlock()

		if started {
			break
		}
	}

	require.Error(t, clockwork.run(ctx))
	cancel()
	require.NoError(t, <-errCh)
}

func TestDropBroadcastBridge(t *testing.T) {
	ack := (dropBroadcastBridge{}).HandleBroadcast(t.Context(), &events.Broadcast{Message: events.NewOutboundMessage(events.SourceSystem, "c", "x")})
	require.Equal(t, events.BroadcastDropped, ack.Status)
}

func TestDispatchMarksDeliveryWhenNoBridges(t *testing.T) {
	channels := events.NewChannels()
	clockwork := newClockwork(channels)
	delivery := events.NewOutboundMessage(events.SourceSystem, "conversation", "solo")
	clockwork.dispatch(&events.Broadcast{Message: delivery, Delivery: delivery})
	require.NoError(t, delivery.WaitDelivered(t.Context()))
}

func TestCloseBridgesFailsPending(t *testing.T) {
	channels := events.NewChannels()
	clockwork := newClockwork(channels)
	clockwork.pendingEnabled = true
	delivery := events.NewOutboundMessage(events.SourceSystem, "conversation", "pending")
	clockwork.pending = []events.Broadcast{{Delivery: delivery}}
	clockwork.closeBridges()
	require.ErrorIs(t, delivery.WaitDelivered(t.Context()), context.Canceled)
}

func TestRegisteredBridgeEnqueueAfterCloseFailsBroadcast(t *testing.T) {
	bridge := &registeredBridge{id: events.BridgeSlack, handler: &clockworkTestBridge{received: make(chan *events.Broadcast, 1), status: events.BroadcastHandled}}
	bridge.cond = sync.NewCond(&bridge.mu)
	bridge.close()

	delivery := events.NewOutboundMessage(events.SourceSystem, "conversation", "late")
	relay := make(chan events.BroadcastReply, 1)
	bridge.enqueue(&events.Broadcast{Delivery: delivery, RelayResponse: relay})
	require.ErrorIs(t, delivery.WaitDelivered(t.Context()), context.Canceled)
	require.ErrorIs(t, (<-relay).Err, context.Canceled)
}

func TestFailBroadcastMarksDeliveryAndRelay(t *testing.T) {
	delivery := events.NewOutboundMessage(events.SourceSystem, "conversation", "cron")
	relay := make(chan events.BroadcastReply, 1)
	failBroadcast(&events.Broadcast{Delivery: delivery, RelayResponse: relay})
	require.ErrorIs(t, delivery.WaitDelivered(t.Context()), context.Canceled)
	require.ErrorIs(t, (<-relay).Err, context.Canceled)
}

func TestClockworkBroadcastsExcludeSenderAndAcknowledge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		slack := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastHandled}
		mcp := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastDropped}
		failed := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastFailed}
		unregisterSlack, err := clockwork.registerBridge(events.BridgeSlack, slack)
		require.NoError(t, err)
		unregisterMCP, err := clockwork.registerBridge(events.BridgeExternalMCP, mcp)
		require.NoError(t, err)
		unregisterFailed, err := clockwork.registerBridge(events.BridgeID("failed"), failed)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork)

		channels.Broadcasts <- events.Broadcast{Sender: events.BridgeSlack, Message: events.NewOutboundMessage(events.SourceSlack, "conversation", "reply")}

		synctest.Wait()

		select {
		case <-slack.received:
			t.Fatal("sender received its own broadcast")
		default:
		}

		mcpBroadcast := <-mcp.received
		failedBroadcast := <-failed.received

		synctest.Wait()

		acknowledgement := <-mcpBroadcast.Acknowledgement
		require.Equal(t, events.BroadcastDropped, acknowledgement.Status)
		require.NoError(t, acknowledgement.Err)
		require.Equal(t, events.BroadcastFailed, (<-failedBroadcast.Acknowledgement).Status)

		unregisterSlack()
		unregisterMCP()
		unregisterFailed()
		cancel()
		synctest.Wait()
	})
}

func TestDropBroadcastBridgeCompletesDelivery(t *testing.T) {
	message := events.NewOutboundMessage(events.SourceSystem, "conversation", "cron")
	bridge := dropBroadcastBridge{}
	broadcast := &events.Broadcast{Message: message, Delivery: message}

	acknowledgement := bridge.HandleBroadcast(t.Context(), broadcast)
	require.Equal(t, events.BroadcastDropped, acknowledgement.Status)
}

func TestClockworkNoSenderBroadcastReachesAllBridges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		slack := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastHandled}
		mcp := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastDropped}
		unregisterSlack, err := clockwork.registerBridge(events.BridgeSlack, slack)
		require.NoError(t, err)
		unregisterMCP, err := clockwork.registerBridge(events.BridgeExternalMCP, mcp)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork)

		channels.Broadcasts <- events.Broadcast{Message: events.NewOutboundMessage(events.SourceSystem, "conversation", "cron")}

		synctest.Wait()

		slackBroadcast := <-slack.received
		mcpBroadcast := <-mcp.received

		synctest.Wait()
		require.Equal(t, events.BroadcastHandled, (<-slackBroadcast.Acknowledgement).Status)
		require.Equal(t, events.BroadcastDropped, (<-mcpBroadcast.Acknowledgement).Status)

		unregisterSlack()
		unregisterMCP()
		cancel()
		synctest.Wait()
	})
}

func TestClockworkSlowBridgeDoesNotBlockAnotherBridge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		slow := &blockingClockworkTestBridge{received: make(chan *events.Broadcast, 1), release: make(chan struct{})}
		fast := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastHandled}
		unregisterSlow, err := clockwork.registerBridge(events.BridgeExternalMCP, slow)
		require.NoError(t, err)
		unregisterFast, err := clockwork.registerBridge(events.BridgeSlack, fast)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork)

		channels.Broadcasts <- events.Broadcast{Message: events.NewOutboundMessage(events.SourceSystem, "conversation", "first")}

		channels.Broadcasts <- events.Broadcast{Message: events.NewOutboundMessage(events.SourceSystem, "conversation", "second")}

		synctest.Wait()

		first := <-fast.received
		second := <-fast.received

		require.Equal(t, "first", first.Message.Text)
		require.Equal(t, "second", second.Message.Text)

		close(slow.release)
		<-slow.received
		synctest.Wait()

		unregisterSlow()
		unregisterFast()
		cancel()
		synctest.Wait()
	})
}

func TestClockworkReconnectReceivesOnlyLaterBroadcasts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := events.NewChannels()
		clockwork := newClockwork(channels)
		firstBridge := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastHandled}
		unregister, err := clockwork.registerBridge(events.BridgeSlack, firstBridge)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		runClockwork(ctx, t, clockwork)
		synctest.Wait()

		unregister()

		channels.Broadcasts <- events.Broadcast{Message: events.NewOutboundMessage(events.SourceSystem, "conversation", "missed")}

		synctest.Wait()

		select {
		case <-firstBridge.received:
			t.Fatal("unregistered bridge received a broadcast")
		default:
		}

		reconnected := &clockworkTestBridge{received: make(chan *events.Broadcast), status: events.BroadcastHandled}
		unregister, err = clockwork.registerBridge(events.BridgeSlack, reconnected)
		require.NoError(t, err)

		channels.Broadcasts <- events.Broadcast{Message: events.NewOutboundMessage(events.SourceSystem, "conversation", "later")}

		synctest.Wait()

		got := <-reconnected.received
		require.Equal(t, "later", got.Message.Text)

		unregister()
		cancel()
		synctest.Wait()
	})
}
