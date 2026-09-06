package main

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

type clockworkTestBridge struct {
	received chan *protocol.Broadcast
	status   protocol.BroadcastStatus
}

func (b *clockworkTestBridge) HandleBroadcast(_ context.Context, broadcast *protocol.Broadcast) protocol.BroadcastAcknowledgement {
	b.received <- broadcast

	return protocol.BroadcastAcknowledgement{Status: b.status}
}

type blockingClockworkTestBridge struct {
	received chan *protocol.Broadcast
	release  chan struct{}
}

func (b *blockingClockworkTestBridge) HandleBroadcast(ctx context.Context, broadcast *protocol.Broadcast) protocol.BroadcastAcknowledgement {
	b.received <- broadcast

	select {
	case <-b.release:
		return protocol.BroadcastAcknowledgement{Status: protocol.BroadcastHandled}
	case <-ctx.Done():
		return protocol.BroadcastAcknowledgement{Status: protocol.BroadcastFailed, Err: ctx.Err()}
	}
}

func TestClockworkBuffersBroadcastBeforeBridges(t *testing.T) {
	channels := protocol.NewChannels()
	clockwork := newClockwork(channels)
	clockwork.pendingEnabled = true
	message := protocol.NewOutboundMessage(protocol.SourceSystem, "conversation", "buffered")
	clockwork.dispatch(&protocol.Broadcast{Message: message, Delivery: message})
	require.Len(t, clockwork.pending, 1)
	require.Equal(t, "buffered", clockwork.pending[0].Message.Text)
}

func TestClockworkRegisterBridgeDuplicate(t *testing.T) {
	channels := protocol.NewChannels()
	clockwork := newClockwork(channels)
	bridge := &clockworkTestBridge{received: make(chan *protocol.Broadcast, 1), status: protocol.BroadcastHandled}
	unregister, err := clockwork.registerBridge(protocol.BridgeSlack, bridge)
	require.NoError(t, err)
	_, err = clockwork.registerBridge(protocol.BridgeSlack, bridge)
	require.Error(t, err)
	unregister()
}

func TestDropBroadcastBridge(t *testing.T) {
	ack := (dropBroadcastBridge{}).HandleBroadcast(t.Context(), &protocol.Broadcast{Message: protocol.NewOutboundMessage(protocol.SourceSystem, "c", "x")})
	require.Equal(t, protocol.BroadcastDropped, ack.Status)
}

func TestDispatchMarksDeliveryWhenNoBridges(t *testing.T) {
	channels := protocol.NewChannels()
	clockwork := newClockwork(channels)
	delivery := protocol.NewOutboundMessage(protocol.SourceSystem, "conversation", "solo")
	clockwork.dispatch(&protocol.Broadcast{Message: delivery, Delivery: delivery})
	require.NoError(t, delivery.WaitDelivered(t.Context()))
}

func TestCloseBridgesFailsPending(t *testing.T) {
	channels := protocol.NewChannels()
	clockwork := newClockwork(channels)
	clockwork.pendingEnabled = true
	delivery := protocol.NewOutboundMessage(protocol.SourceSystem, "conversation", "pending")
	clockwork.pending = []protocol.Broadcast{{Delivery: delivery}}
	clockwork.closeBridges()
	require.ErrorIs(t, delivery.WaitDelivered(t.Context()), context.Canceled)
}

func TestRegisteredBridgeEnqueueAfterCloseFailsBroadcast(t *testing.T) {
	bridge := &registeredBridge{id: protocol.BridgeSlack, handler: &clockworkTestBridge{received: make(chan *protocol.Broadcast, 1), status: protocol.BroadcastHandled}}
	bridge.cond = sync.NewCond(&bridge.mu)
	bridge.close()

	delivery := protocol.NewOutboundMessage(protocol.SourceSystem, "conversation", "late")
	relay := make(chan protocol.BroadcastReply, 1)
	bridge.enqueue(&protocol.Broadcast{Delivery: delivery, RelayResponse: relay})
	require.ErrorIs(t, delivery.WaitDelivered(t.Context()), context.Canceled)
	require.ErrorIs(t, (<-relay).Err, context.Canceled)
}

func TestFailBroadcastMarksDeliveryAndRelay(t *testing.T) {
	delivery := protocol.NewOutboundMessage(protocol.SourceSystem, "conversation", "cron")
	relay := make(chan protocol.BroadcastReply, 1)
	failBroadcast(&protocol.Broadcast{Delivery: delivery, RelayResponse: relay})
	require.ErrorIs(t, delivery.WaitDelivered(t.Context()), context.Canceled)
	require.ErrorIs(t, (<-relay).Err, context.Canceled)
}

func TestClockworkBroadcastsExcludeSenderAndAcknowledge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := protocol.NewChannels()
		clockwork := newClockwork(channels)
		slack := &clockworkTestBridge{received: make(chan *protocol.Broadcast), status: protocol.BroadcastHandled}
		mcp := &clockworkTestBridge{received: make(chan *protocol.Broadcast), status: protocol.BroadcastDropped}
		failed := &clockworkTestBridge{received: make(chan *protocol.Broadcast), status: protocol.BroadcastFailed}
		unregisterSlack, err := clockwork.registerBridge(protocol.BridgeSlack, slack)
		require.NoError(t, err)
		unregisterMCP, err := clockwork.registerBridge(protocol.BridgeExternalMCP, mcp)
		require.NoError(t, err)
		unregisterFailed, err := clockwork.registerBridge(protocol.BridgeID("failed"), failed)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		go clockwork.run(ctx)

		channels.Broadcasts <- protocol.Broadcast{Sender: protocol.BridgeSlack, Message: protocol.NewOutboundMessage(protocol.SourceSlack, "conversation", "reply")}

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
		require.Equal(t, protocol.BroadcastDropped, acknowledgement.Status)
		require.NoError(t, acknowledgement.Err)
		require.Equal(t, protocol.BroadcastFailed, (<-failedBroadcast.Acknowledgement).Status)

		unregisterSlack()
		unregisterMCP()
		unregisterFailed()
		cancel()
		synctest.Wait()
	})
}

func TestClockworkNoSenderBroadcastReachesAllBridges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := protocol.NewChannels()
		clockwork := newClockwork(channels)
		slack := &clockworkTestBridge{received: make(chan *protocol.Broadcast), status: protocol.BroadcastHandled}
		mcp := &clockworkTestBridge{received: make(chan *protocol.Broadcast), status: protocol.BroadcastDropped}
		unregisterSlack, err := clockwork.registerBridge(protocol.BridgeSlack, slack)
		require.NoError(t, err)
		unregisterMCP, err := clockwork.registerBridge(protocol.BridgeExternalMCP, mcp)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		go clockwork.run(ctx)

		channels.Broadcasts <- protocol.Broadcast{Message: protocol.NewOutboundMessage(protocol.SourceSystem, "conversation", "cron")}

		synctest.Wait()

		slackBroadcast := <-slack.received
		mcpBroadcast := <-mcp.received

		synctest.Wait()
		require.Equal(t, protocol.BroadcastHandled, (<-slackBroadcast.Acknowledgement).Status)
		require.Equal(t, protocol.BroadcastDropped, (<-mcpBroadcast.Acknowledgement).Status)

		unregisterSlack()
		unregisterMCP()
		cancel()
		synctest.Wait()
	})
}

func TestClockworkSlowBridgeDoesNotBlockAnotherBridge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		channels := protocol.NewChannels()
		clockwork := newClockwork(channels)
		slow := &blockingClockworkTestBridge{received: make(chan *protocol.Broadcast, 1), release: make(chan struct{})}
		fast := &clockworkTestBridge{received: make(chan *protocol.Broadcast), status: protocol.BroadcastHandled}
		unregisterSlow, err := clockwork.registerBridge(protocol.BridgeExternalMCP, slow)
		require.NoError(t, err)
		unregisterFast, err := clockwork.registerBridge(protocol.BridgeSlack, fast)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		go clockwork.run(ctx)

		channels.Broadcasts <- protocol.Broadcast{Message: protocol.NewOutboundMessage(protocol.SourceSystem, "conversation", "first")}

		channels.Broadcasts <- protocol.Broadcast{Message: protocol.NewOutboundMessage(protocol.SourceSystem, "conversation", "second")}

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
		channels := protocol.NewChannels()
		clockwork := newClockwork(channels)
		firstBridge := &clockworkTestBridge{received: make(chan *protocol.Broadcast), status: protocol.BroadcastHandled}
		unregister, err := clockwork.registerBridge(protocol.BridgeSlack, firstBridge)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		go clockwork.run(ctx)

		synctest.Wait()

		unregister()

		channels.Broadcasts <- protocol.Broadcast{Message: protocol.NewOutboundMessage(protocol.SourceSystem, "conversation", "missed")}

		synctest.Wait()

		select {
		case <-firstBridge.received:
			t.Fatal("unregistered bridge received a broadcast")
		default:
		}

		reconnected := &clockworkTestBridge{received: make(chan *protocol.Broadcast), status: protocol.BroadcastHandled}
		unregister, err = clockwork.registerBridge(protocol.BridgeSlack, reconnected)
		require.NoError(t, err)

		channels.Broadcasts <- protocol.Broadcast{Message: protocol.NewOutboundMessage(protocol.SourceSystem, "conversation", "later")}

		synctest.Wait()

		got := <-reconnected.received
		require.Equal(t, "later", got.Message.Text)

		unregister()
		cancel()
		synctest.Wait()
	})
}
