package main

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"golang.org/x/sync/errgroup"
)

type clockworkBridge interface {
	HandleBroadcast(context.Context, *protocol.Broadcast) protocol.BroadcastAcknowledgement
}

type dropBroadcastBridge struct{}

func (dropBroadcastBridge) HandleBroadcast(_ context.Context, _ *protocol.Broadcast) protocol.BroadcastAcknowledgement {
	return protocol.BroadcastAcknowledgement{Status: protocol.BroadcastDropped}
}

type clockwork struct {
	channels protocol.Channels

	mu             sync.Mutex
	bridges        map[protocol.BridgeID]*registeredBridge
	started        bool
	done           chan struct{}
	registerCh     chan bridgeRegistrationRequest
	pending        []protocol.Broadcast
	pendingEnabled bool
}

type bridgeRegistrationRequest struct {
	bridge *registeredBridge
	ready  chan struct{}
}

type registeredBridge struct {
	id      protocol.BridgeID
	handler clockworkBridge

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []*protocol.Broadcast
	closed bool
}

func newClockwork(channels protocol.Channels) *clockwork {
	return &clockwork{
		channels:   channels,
		bridges:    make(map[protocol.BridgeID]*registeredBridge),
		done:       make(chan struct{}),
		registerCh: make(chan bridgeRegistrationRequest),
	}
}

func (c *clockwork) registerBridge(id protocol.BridgeID, handler clockworkBridge) (func(), error) {
	bridge := &registeredBridge{id: id, handler: handler}
	bridge.cond = sync.NewCond(&bridge.mu)

	c.mu.Lock()
	if _, exists := c.bridges[id]; exists {
		c.mu.Unlock()
		return nil, errors.New("bridge already registered")
	}

	c.bridges[id] = bridge
	started := c.started
	done := c.done
	c.mu.Unlock()

	if started {
		request := bridgeRegistrationRequest{bridge: bridge, ready: make(chan struct{}, 1)}
		select {
		case c.registerCh <- request:
			<-request.ready
		case <-done:
			c.removeBridge(bridge)
			bridge.close()

			return nil, context.Canceled
		}
	}

	return func() {
		c.removeBridge(bridge)
		bridge.close()
	}, nil
}

func (c *clockwork) removeBridge(bridge *registeredBridge) {
	c.mu.Lock()
	if c.bridges[bridge.id] == bridge {
		delete(c.bridges, bridge.id)
	}
	c.mu.Unlock()
}

func (c *clockwork) run(ctx context.Context) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()

		return errors.New("clockwork already running")
	}

	c.started = true
	c.pendingEnabled = len(c.bridges) == 0
	bridges := slices.Collect(maps.Values(c.bridges))
	c.mu.Unlock()

	group, groupCtx := errgroup.WithContext(ctx)
	startBridge := func(bridge *registeredBridge) {
		group.Go(func() error {
			bridge.run(groupCtx)

			return nil
		})
	}

	for _, bridge := range bridges {
		startBridge(bridge)
	}

	group.Go(func() error {
		for {
			select {
			case <-groupCtx.Done():
				c.closeBridges()

				return nil
			case broadcast := <-c.channels.Broadcasts:
				c.dispatch(&broadcast)
			case registration := <-c.registerCh:
				startBridge(registration.bridge)
				c.mu.Lock()
				pending := c.pending
				c.pending = nil
				c.pendingEnabled = false
				c.mu.Unlock()

				for i := range pending {
					c.dispatch(&pending[i])
				}

				close(registration.ready)
			}
		}
	})

	err := group.Wait()

	c.closeBridges()
	close(c.done)

	return err
}

func (c *clockwork) dispatch(broadcast *protocol.Broadcast) {
	c.mu.Lock()

	bridgeCount := len(c.bridges)
	pendingEnabled := c.pendingEnabled

	bridges := make([]*registeredBridge, 0, bridgeCount)
	for id, bridge := range c.bridges {
		if broadcast.Sender == "" || id != broadcast.Sender {
			bridges = append(bridges, bridge)
		}
	}
	c.mu.Unlock()

	if len(bridges) == 0 && bridgeCount == 0 && pendingEnabled {
		c.mu.Lock()
		c.pending = append(c.pending, *broadcast)
		c.mu.Unlock()

		return
	}

	if len(bridges) == 0 && broadcast.Delivery != nil && broadcast.Message.Response == nil {
		broadcast.Delivery.MarkDelivered(nil)
	}

	for _, bridge := range bridges {
		clone := broadcast.Clone()
		bridge.enqueue(&clone)
	}
}

func (c *clockwork) closeBridges() {
	c.mu.Lock()
	pending := c.pending
	c.pending = nil
	bridges := slices.Collect(maps.Values(c.bridges))
	c.mu.Unlock()

	for _, bridge := range bridges {
		bridge.close()
	}

	for i := range pending {
		failBroadcast(&pending[i])
	}
}

func (b *registeredBridge) enqueue(broadcast *protocol.Broadcast) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		failBroadcast(broadcast)

		return
	}

	b.queue = append(b.queue, broadcast)
	b.cond.Signal()
	b.mu.Unlock()
}

func (b *registeredBridge) close() {
	b.mu.Lock()

	b.closed = true
	for _, broadcast := range b.queue {
		failBroadcast(broadcast)
	}

	b.queue = nil
	b.cond.Broadcast()
	b.mu.Unlock()
}

func failBroadcast(broadcast *protocol.Broadcast) {
	if broadcast.Delivery != nil {
		broadcast.Delivery.MarkDelivered(context.Canceled)
	}

	if broadcast.RelayResponse != nil {
		broadcast.RelayResponse <- protocol.BroadcastReply{Err: context.Canceled}
	}
}

func (b *registeredBridge) run(ctx context.Context) {
	stopWake := context.AfterFunc(ctx, func() {
		b.mu.Lock()
		b.cond.Broadcast()
		b.mu.Unlock()
	})
	defer stopWake()

	for {
		b.mu.Lock()
		for len(b.queue) == 0 && !b.closed && ctx.Err() == nil {
			b.cond.Wait()
		}

		if b.closed || ctx.Err() != nil {
			b.queue = nil
			b.mu.Unlock()

			return
		}

		broadcast := b.queue[0]
		clear(b.queue[:1])
		b.queue = b.queue[1:]
		b.mu.Unlock()

		acknowledgement := b.handler.HandleBroadcast(ctx, broadcast)
		broadcast.Acknowledgement <- acknowledgement
	}
}
