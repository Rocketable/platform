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

// Bus routes outbound text events between components.
type Bus struct {
	mu        sync.Mutex
	cond      *sync.Cond
	closed    bool
	closeOnce sync.Once

	outbound []*OutboundMessage
}

// New constructs an event bus.
func New() *Bus {
	b := new(Bus)

	b.cond = sync.NewCond(&b.mu)

	return b
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
	b.cond.Broadcast()

	return nil
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

func (b *Bus) notifyOnContext(ctx context.Context) func() {
	stop := context.AfterFunc(ctx, func() {
		b.mu.Lock()
		b.cond.Broadcast()
		b.mu.Unlock()
	})

	return func() { stop() }
}
