// Package events defines the shared rocketclaw event bus.
package events

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"sync"
	"time"
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

	minimumWaitAfterHumanInteraction time.Duration
	inboundHumans                    []*InboundMessage
	lastHumanMessage                 time.Time
	stopTicker                       chan struct{}
	inboundAutos                     []*InboundMessage
	inboundPending                   int
	outbound                         []*OutboundMessage
	outboundPending                  int
	observers                        map[*Observer]struct{}
}

// Observer receives non-consuming inbound and outbound bus events.
type Observer struct {
	bus   *Bus
	queue []ObservedMessage
}

// Config controls event bus behavior.
type Config struct {
	MinimumWaitAfterHumanInteraction time.Duration
}

// New constructs an event bus.
func New(configs ...Config) *Bus {
	b := new(Bus)

	b.cond = sync.NewCond(&b.mu)
	if len(configs) > 0 {
		b.minimumWaitAfterHumanInteraction = configs[0].MinimumWaitAfterHumanInteraction
	}

	if b.minimumWaitAfterHumanInteraction > 0 {
		b.stopTicker = make(chan struct{})
		ticker := time.NewTicker(b.minimumWaitAfterHumanInteraction)

		go func() {
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					b.cond.Broadcast()
				case <-b.stopTicker:
					return
				}
			}
		}()
	}

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

// WaitInboundDequeued waits for accepted inbound work to leave the bus queues.
func (b *Bus) WaitInboundDequeued(ctx context.Context, logger *slog.Logger) error {
	stop := b.notifyOnContext(ctx)
	defer stop()

	b.mu.Lock()

	for {
		humanQueueLen := len(b.inboundHumans)
		autoQueueLen := len(b.inboundAutos)
		inboundPending := b.inboundPending
		inboundClosed := b.inboundClosed
		busClosed := b.closed
		idle := humanQueueLen == 0 && autoQueueLen == 0 && inboundPending == 0
		b.mu.Unlock()

		logger.Info("inbound queue handoff wait state", "human_queue_len", humanQueueLen, "auto_queue_len", autoQueueLen, "inbound_pending", inboundPending, "inbound_closed", inboundClosed, "bus_closed", busClosed)

		if idle {
			return nil
		}

		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for inbound idle: %w", err)
		}

		b.mu.Lock()
		if len(b.inboundHumans) == 0 && len(b.inboundAutos) == 0 && b.inboundPending == 0 {
			continue
		}

		b.cond.Wait()
	}
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

	if msg != nil {
		b.outboundPending++
		msg.deliveryNotify = func(error) {
			b.mu.Lock()
			b.outboundPending--
			b.cond.Broadcast()
			b.mu.Unlock()
		}
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

// WaitOutboundIdle waits until outbound work is queued nowhere and delivered everywhere.
func (b *Bus) WaitOutboundIdle(ctx context.Context, logger *slog.Logger) error {
	stop := b.notifyOnContext(ctx)
	defer stop()

	b.mu.Lock()

	for {
		outboundQueueLen := len(b.outbound)
		outboundPending := b.outboundPending
		busClosed := b.closed
		idle := outboundQueueLen == 0 && outboundPending == 0
		args := []any{"outbound_queue_len", outboundQueueLen, "outbound_pending", outboundPending, "bus_closed", busClosed}

		if len(b.outbound) > 0 && b.outbound[0] != nil {
			queued := b.outbound[0]
			targets := append([]OutputTarget(nil), queued.Targets...)
			args = append(args, "next_source", queued.Source, "next_targets", targets, "next_conversation_id", queued.ConversationID, "next_turn_id", queued.TurnID, "next_sequence", queued.Sequence, "next_progress", queued.PostProgressText, "next_complete", queued.Complete)
		}
		b.mu.Unlock()

		logger.Info("outbound drain wait state", args...)

		if idle {
			return nil
		}

		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for outbound idle: %w", err)
		}

		b.mu.Lock()
		if len(b.outbound) == 0 && b.outboundPending == 0 {
			continue
		}

		b.cond.Wait()
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
			b.inboundPending--
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
		if b.stopTicker != nil {
			close(b.stopTicker)
		}

		b.cond.Broadcast()
		b.mu.Unlock()
	})
}

func (b *Bus) dequeueInbound(ctx context.Context) (*InboundMessage, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	minimumWaitAfterHumanInteraction := b.minimumWaitAfterHumanInteraction
	for {
		if b.closed || ctx.Err() != nil {
			return nil, false
		}

		if len(b.inboundHumans) > 0 {
			msg := b.inboundHumans[0]
			b.inboundHumans = b.inboundHumans[1:]
			b.inboundPending++
			b.lastHumanMessage = time.Now()

			return msg, true
		}

		if len(b.inboundAutos) > 0 && (b.inboundClosed || minimumWaitAfterHumanInteraction <= 0 || time.Since(b.lastHumanMessage) >= minimumWaitAfterHumanInteraction) {
			msg := b.inboundAutos[0]
			b.inboundAutos = b.inboundAutos[1:]
			b.inboundPending++

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
