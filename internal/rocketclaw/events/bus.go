// Package events defines the shared rocketclaw event bus.
package events

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
)

// ErrBusClosed reports that an event was published after the bus shut down.
var ErrBusClosed = errors.New("bus closed")

// Bus routes inbound and outbound text events between components.
type Bus struct {
	mu            sync.Mutex
	cond          *sync.Cond
	closed        bool
	inboundClosed bool
	closeOnce     sync.Once

	inboundHumans  []*InboundMessage
	inboundAutos   []*InboundMessage
	inboundPending []*InboundMessage
	outbound       []*OutboundMessage
	observers      map[*Observer]struct{}
}

// Observer receives non-consuming inbound and outbound bus events.
type Observer struct {
	bus   *Bus
	queue []ObservedMessage
}

// New constructs an event bus.
func New() *Bus {
	b := new(Bus)

	b.cond = sync.NewCond(&b.mu)

	return b
}

// PublishInbound publishes a text message into the shared input queue.
func (b *Bus) PublishInbound(ctx context.Context, msg *InboundMessage) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish to bus canceled: %w", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed || b.inboundClosed {
		return ErrBusClosed
	}

	if msg != nil && msg.Human {
		b.inboundHumans = append(b.inboundHumans, msg)
	} else {
		b.inboundAutos = append(b.inboundAutos, msg)
	}

	b.publishObservedLocked(ObservedMessage{Inbound: msg})

	b.cond.Broadcast()

	return nil
}

// StopInbound stops new inbound messages while allowing accepted messages to be dequeued.
func (b *Bus) StopInbound() {
	b.mu.Lock()
	b.inboundClosed = true
	b.cond.Broadcast()
	b.mu.Unlock()
}

// PublishOutbound publishes a text message to all output sinks.
func (b *Bus) PublishOutbound(ctx context.Context, msg *OutboundMessage) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish to bus canceled: %w", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return ErrBusClosed
	}

	b.outbound = append(b.outbound, msg)
	b.publishObservedLocked(ObservedMessage{Outbound: msg})
	b.cond.Broadcast()

	return nil
}

// Observe returns a non-consuming single-use iterator over inbound and outbound text events.
func (b *Bus) Observe(ctx context.Context) iter.Seq[ObservedMessage] {
	observer := &Observer{bus: b}

	b.mu.Lock()
	if b.observers == nil {
		b.observers = map[*Observer]struct{}{}
	}

	b.observers[observer] = struct{}{}
	b.mu.Unlock()

	return func(yield func(ObservedMessage) bool) {
		stop := b.notifyOnContext(ctx)
		defer stop()
		defer b.removeObserver(observer)

		for {
			msg, ok := observer.next(ctx)
			if !ok {
				return
			}

			if !yield(msg) {
				return
			}
		}
	}
}

// Inbound returns a single-use iterator over inbound text messages.
func (b *Bus) Inbound(ctx context.Context) iter.Seq[*InboundMessage] {
	return func(yield func(*InboundMessage) bool) {
		stop := b.notifyOnContext(ctx)
		defer stop()

		for {
			msg, ok := b.dequeueInbound(ctx)
			if !ok {
				return
			}

			keepGoing := yield(msg)

			b.mu.Lock()
			for i := range b.inboundPending {
				if b.inboundPending[i] == msg {
					b.inboundPending = append(b.inboundPending[:i], b.inboundPending[i+1:]...)
					break
				}
			}

			b.cond.Broadcast()
			b.mu.Unlock()

			if !keepGoing {
				return
			}
		}
	}
}

// Outbound returns a single-use iterator over outbound text messages.
func (b *Bus) Outbound(ctx context.Context) iter.Seq[*OutboundMessage] {
	return func(yield func(*OutboundMessage) bool) {
		stop := b.notifyOnContext(ctx)
		defer stop()

		for {
			msg, ok := b.dequeueOutbound(ctx)
			if !ok {
				return
			}

			if !yield(msg) {
				return
			}
		}
	}
}

// Close shuts down the bus and wakes all waiting consumers.
func (b *Bus) Close() {
	b.closeOnce.Do(func() {
		b.mu.Lock()

		b.closed = true
		b.cond.Broadcast()
		b.mu.Unlock()
	})
}

func (b *Bus) dequeueInbound(ctx context.Context) (*InboundMessage, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for {
		if b.closed || ctx.Err() != nil {
			return nil, false
		}

		if len(b.inboundHumans) > 0 {
			msg := b.inboundHumans[0]
			b.inboundHumans = b.inboundHumans[1:]
			b.inboundPending = append(b.inboundPending, msg)

			return msg, true
		}

		if len(b.inboundAutos) > 0 {
			msg := b.inboundAutos[0]
			b.inboundAutos = b.inboundAutos[1:]
			b.inboundPending = append(b.inboundPending, msg)

			return msg, true
		}

		if b.inboundClosed {
			return nil, false
		}

		b.cond.Wait()
	}
}

func (b *Bus) dequeueOutbound(ctx context.Context) (*OutboundMessage, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for {
		if b.closed || ctx.Err() != nil {
			return nil, false
		}

		if len(b.outbound) > 0 {
			msg := b.outbound[0]
			b.outbound = b.outbound[1:]
			b.cond.Broadcast()

			return msg, true
		}

		b.cond.Wait()
	}
}

func (b *Bus) publishObservedLocked(msg ObservedMessage) {
	for observer := range b.observers {
		observer.queue = append(observer.queue, msg)
	}
}

func (b *Bus) removeObserver(observer *Observer) {
	b.mu.Lock()
	delete(b.observers, observer)
	b.mu.Unlock()
}

func (o *Observer) next(ctx context.Context) (ObservedMessage, bool) {
	o.bus.mu.Lock()
	defer o.bus.mu.Unlock()

	for {
		if o.bus.closed || ctx.Err() != nil {
			return ObservedMessage{}, false
		}

		if len(o.queue) > 0 {
			msg := o.queue[0]
			o.queue = o.queue[1:]

			return msg, true
		}

		o.bus.cond.Wait()
	}
}

func (b *Bus) notifyOnContext(ctx context.Context) func() {
	stop := context.AfterFunc(ctx, func() {
		b.mu.Lock()
		b.cond.Broadcast()
		b.mu.Unlock()
	})

	return func() { stop() }
}
