package events

import (
	"bytes"
	"context"
	"iter"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBusWaitsLogQueuedState(t *testing.T) {
	bus := New()
	defer bus.Close()

	var logs bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&logs, nil))

	require.NoError(t, bus.PublishInbound(context.Background(), NewMainInboundMessage(SourceSlack, InboundKindPrompt, "slack", "inbound", true)))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, bus.WaitInboundDequeued(ctx, logger), context.Canceled)

	outbound := NewMainOutboundMessage(SourceSystem, "outbound")
	outbound.ConversationID = "main"
	outbound.TurnID = "turn-1"
	outbound.Sequence = 7
	require.NoError(t, bus.PublishOutbound(context.Background(), outbound))
	require.ErrorIs(t, bus.WaitOutboundIdle(ctx, logger), context.Canceled)

	logOutput := logs.String()
	require.Contains(t, logOutput, "inbound queue handoff wait state")
	require.Contains(t, logOutput, "human_queue_len=1")
	require.Contains(t, logOutput, "outbound drain wait state")
	require.Contains(t, logOutput, "outbound_queue_len=1")
	require.Contains(t, logOutput, "outbound_pending=1")
	require.Contains(t, logOutput, "next_conversation_id=main")
	require.Contains(t, logOutput, "next_turn_id=turn-1")
	require.Contains(t, logOutput, "next_sequence=7")
}

func TestStopInboundKeepsAcceptedMessages(t *testing.T) {
	bus := New(Config{MinimumWaitAfterHumanInteraction: time.Hour})
	defer bus.Close()

	require.NoError(t, bus.PublishInbound(context.Background(), NewMainInboundMessage("test", InboundKindPrompt, "", "human", true)))
	require.NoError(t, bus.PublishInbound(context.Background(), NewMainInboundMessage("test", InboundKindPrompt, "", "auto", false)))
	require.Equal(t, "human", requireInboundMessage(t, bus, 100*time.Millisecond).Text)

	bus.StopInbound()
	require.ErrorIs(t, bus.PublishInbound(context.Background(), NewMainInboundMessage("test", InboundKindPrompt, "", "late", true)), ErrBusClosed)
	require.Equal(t, "auto", requireInboundMessage(t, bus, 100*time.Millisecond).Text)
	require.NoError(t, bus.WaitInboundDequeued(context.Background(), slog.New(slog.DiscardHandler)))

	iterCtx, cancelIter := context.WithCancel(context.Background())
	defer cancelIter()

	result := make(chan *InboundMessage, 1)

	go func() {
		defer close(result)

		for msg := range bus.Inbound(iterCtx) {
			result <- msg
			return
		}
	}()

	select {
	case msg, ok := <-result:
		if ok {
			t.Fatalf("bus.Inbound yielded %v after StopInbound and drain", msg)
		}
	case <-time.After(time.Second):
		cancelIter()
		<-result
		t.Fatal("bus.Inbound did not return after StopInbound and drain")
	}
}

func TestWaitOutboundIdleWaitsForDelivery(t *testing.T) {
	bus := New()
	defer bus.Close()

	outbound := NewMainOutboundMessage(SourceSystem, "hello")
	require.NoError(t, bus.PublishOutbound(context.Background(), outbound))

	done := make(chan error, 1)

	go func() { done <- bus.WaitOutboundIdle(context.Background(), slog.New(slog.DiscardHandler)) }()

	require.Equal(t, outbound, requireOutboundMessage(t, bus, time.Second))
	outbound.MarkDelivered(nil)
	require.NoError(t, <-done)
}

func TestObserveDoesNotConsumeInboundOrOutbound(t *testing.T) {
	bus := New()
	defer bus.Close()

	observeCtx := t.Context()

	observed := bus.Observe(observeCtx)

	inbound := NewMainInboundMessage(SourceSlack, InboundKindPrompt, "slack", "hello", true)
	outbound := NewMainOutboundMessage(SourceSystem, "answer")

	require.NoError(t, bus.PublishInbound(context.Background(), inbound))
	require.NoError(t, bus.PublishOutbound(context.Background(), outbound))

	require.Equal(t, inbound, requireInboundMessage(t, bus, time.Second))
	require.Equal(t, outbound, requireOutboundMessage(t, bus, time.Second))
	messages := requireObservedMessages(t, observed, 2, time.Second)
	require.Equal(t, ObservedMessage{Inbound: inbound}, messages[0])
	require.Equal(t, ObservedMessage{Outbound: outbound}, messages[1])
}

func TestObserveStopsAfterClose(t *testing.T) {
	bus := New()
	observed := bus.Observe(context.Background())
	bus.Close()

	var messages []ObservedMessage
	for msg := range observed {
		messages = append(messages, msg)
	}

	require.Empty(t, messages)
}

func TestCompleteResponseWithAttachmentsClonesAttachments(t *testing.T) {
	msg := NewMainInboundMessage(SourceExternalMCP, InboundKindPrompt, "", "hello", true)
	resultCh := msg.EnableResponseWait()
	attachments := []OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}

	msg.CompleteResponseWithAttachments("answer", attachments, nil)
	attachments[0].Data[0] = 'R'

	result := <-resultCh
	require.Equal(t, "answer", result.Text)
	require.Equal(t, []OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}, result.Attachments)
}

func TestBusCanceledOperations(t *testing.T) {
	bus := New()
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, bus.PublishInbound(ctx, NewMainInboundMessage("test", InboundKindPrompt, "", "inbound", true)), context.Canceled)
	require.ErrorIs(t, bus.PublishOutbound(ctx, NewMainOutboundMessage(SourceSystem, "outbound")), context.Canceled)

	require.NoError(t, bus.PublishInbound(context.Background(), NewMainInboundMessage("test", InboundKindPrompt, "", "inbound", true)))
	require.ErrorIs(t, bus.WaitInboundDequeued(ctx, slog.New(slog.DiscardHandler)), context.Canceled)

	require.NoError(t, bus.PublishOutbound(context.Background(), NewMainOutboundMessage(SourceSystem, "pending")))
	require.ErrorIs(t, bus.WaitOutboundIdle(ctx, slog.New(slog.DiscardHandler)), context.Canceled)
}

func TestPublishOutboundAfterCloseReturnsErrBusClosed(t *testing.T) {
	bus := New()
	bus.Close()

	require.ErrorIs(t, bus.PublishOutbound(context.Background(), NewMainOutboundMessage(SourceSystem, "late")), ErrBusClosed)
}

func TestBusCloseStopsInboundPublishAndIterator(t *testing.T) {
	bus := New()
	bus.Close()

	require.ErrorIs(t, bus.PublishInbound(context.Background(), NewMainInboundMessage("test", InboundKindPrompt, "", "late", true)), ErrBusClosed)

	var inbound []*InboundMessage
	for msg := range bus.Inbound(context.Background()) {
		inbound = append(inbound, msg)
	}

	require.Empty(t, inbound)
}

func requireInboundMessage(t *testing.T, bus *Bus, timeout time.Duration) *InboundMessage {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for msg := range bus.Inbound(ctx) {
		return msg
	}

	t.Fatalf("timed out waiting for inbound message after %v", timeout)

	return nil
}

func requireOutboundMessage(t *testing.T, bus *Bus, timeout time.Duration) *OutboundMessage {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for msg := range bus.Outbound(ctx) {
		return msg
	}

	t.Fatalf("timed out waiting for outbound message after %v", timeout)

	return nil
}

func requireObservedMessages(t *testing.T, observed iter.Seq[ObservedMessage], count int, timeout time.Duration) []ObservedMessage {
	t.Helper()

	result := make(chan []ObservedMessage, 1)

	go func() {
		messages := make([]ObservedMessage, 0, count)
		for msg := range observed {
			messages = append(messages, msg)
			if len(messages) == count {
				result <- messages
				return
			}
		}
	}()

	select {
	case messages := <-result:
		return messages
	case <-time.After(timeout):
		t.Fatal("timed out waiting for observed messages")
	}

	return nil
}
