// Package events defines the shared rocketclaw event bus.
package events

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"time"
)

// ErrBusClosed reports that an event was published after the bus shut down.
var ErrBusClosed = errors.New("bus closed")

// Bus routes inbound text, outbound text, and audio events between components.
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
	audio                            []*AudioChunk
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
func (b *Bus) WaitInboundDequeued(ctx context.Context) error {
	stop := b.notifyOnContext(ctx)
	defer stop()

	b.mu.Lock()
	defer b.mu.Unlock()

	for len(b.inboundHumans) > 0 || len(b.inboundAutos) > 0 || b.inboundPending > 0 {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for inbound idle: %w", err)
		}

		b.cond.Wait()
	}

	return nil
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
	b.cond.Broadcast()

	return nil
}

// WaitOutboundIdle waits until outbound work is queued nowhere and delivered everywhere.
func (b *Bus) WaitOutboundIdle(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	stop := b.notifyOnContext(ctx)
	defer stop()

	b.mu.Lock()
	defer b.mu.Unlock()

	for {
		if len(b.outbound) == 0 && b.outboundPending == 0 {
			return nil
		}

		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for outbound idle: %w", err)
		}

		b.cond.Wait()
	}
}

// PublishAudio publishes an audio chunk into the voice pipeline.
func (b *Bus) PublishAudio(ctx context.Context, chunk *AudioChunk) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish to bus canceled: %w", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return ErrBusClosed
	}

	b.audio = append(b.audio, chunk)
	b.cond.Broadcast()

	return nil
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

// Audio returns a single-use iterator over inbound audio chunks.
func (b *Bus) Audio(ctx context.Context) iter.Seq[*AudioChunk] {
	return func(yield func(*AudioChunk) bool) {
		stop := b.notifyOnContext(ctx)
		defer stop()

		for {
			chunk, ok := b.dequeueAudio(ctx)
			if !ok {
				return
			}

			if !yield(chunk) {
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

func (b *Bus) dequeueAudio(ctx context.Context) (*AudioChunk, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for {
		if b.closed || ctx.Err() != nil {
			return nil, false
		}

		if len(b.audio) > 0 {
			chunk := b.audio[0]
			b.audio = b.audio[1:]

			return chunk, true
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
