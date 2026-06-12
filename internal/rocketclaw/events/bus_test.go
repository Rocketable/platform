package events

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStopInboundKeepsAcceptedMessages(t *testing.T) {
	bus := New(Config{MinimumWaitAfterHumanInteraction: time.Hour})
	defer bus.Close()

	require.NoError(t, bus.PublishInbound(context.Background(), NewMainInboundMessage("test", InboundKindPrompt, "", "human", true)))
	require.NoError(t, bus.PublishInbound(context.Background(), NewMainInboundMessage("test", InboundKindPrompt, "", "auto", false)))
	require.Equal(t, "human", requireInboundMessage(t, bus, 100*time.Millisecond).Text)

	bus.StopInbound()
	require.ErrorIs(t, bus.PublishInbound(context.Background(), NewMainInboundMessage("test", InboundKindPrompt, "", "late", true)), ErrBusClosed)
	require.Equal(t, "auto", requireInboundMessage(t, bus, 100*time.Millisecond).Text)
	require.NoError(t, bus.WaitInboundDequeued(context.Background()))

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

	go func() { done <- bus.WaitOutboundIdle(context.Background()) }()

	require.Equal(t, outbound, requireOutboundMessage(t, bus, time.Second))
	outbound.MarkDelivered(nil)
	require.NoError(t, <-done)
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

func TestAudioQueue(t *testing.T) {
	bus := New()
	defer bus.Close()

	chunk := &AudioChunk{SessionID: "s", SpeakerID: "u", Source: SourceDiscordVoice, RTPSequence: 1, Timestamp: 2, SSRC: 3, SampleRate: 48000, Channels: 2, Format: "opus", Data: []byte{1, 2, 3}}
	require.NoError(t, bus.PublishAudio(context.Background(), chunk))
	require.Equal(t, chunk, requireAudioChunk(t, bus, time.Second))
}

func TestBusCanceledOperations(t *testing.T) {
	bus := New()
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, bus.PublishInbound(ctx, NewMainInboundMessage("test", InboundKindPrompt, "", "inbound", true)), context.Canceled)
	require.ErrorIs(t, bus.PublishOutbound(ctx, NewMainOutboundMessage(SourceSystem, "outbound")), context.Canceled)
	require.ErrorIs(t, bus.PublishAudio(ctx, &AudioChunk{}), context.Canceled)

	require.NoError(t, bus.PublishInbound(context.Background(), NewMainInboundMessage("test", InboundKindPrompt, "", "inbound", true)))
	require.ErrorIs(t, bus.WaitInboundDequeued(ctx), context.Canceled)

	require.NoError(t, bus.PublishOutbound(context.Background(), NewMainOutboundMessage(SourceSystem, "pending")))
	require.ErrorIs(t, bus.WaitOutboundIdle(ctx), context.Canceled)
}

func TestPublishAudioAfterCloseReturnsErrBusClosed(t *testing.T) {
	bus := New()
	bus.Close()

	require.ErrorIs(t, bus.PublishOutbound(context.Background(), NewMainOutboundMessage(SourceSystem, "late")), ErrBusClosed)
	require.ErrorIs(t, bus.PublishAudio(context.Background(), &AudioChunk{}), ErrBusClosed)
}

func TestBusCloseStopsInboundPublishAndAudioIterator(t *testing.T) {
	bus := New()
	bus.Close()

	require.ErrorIs(t, bus.PublishInbound(context.Background(), NewMainInboundMessage("test", InboundKindPrompt, "", "late", true)), ErrBusClosed)

	var inbound []*InboundMessage
	for msg := range bus.Inbound(context.Background()) {
		inbound = append(inbound, msg)
	}

	require.Empty(t, inbound)

	var chunks []*AudioChunk
	for chunk := range bus.Audio(context.Background()) {
		chunks = append(chunks, chunk)
	}

	require.Empty(t, chunks)
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

func requireAudioChunk(t *testing.T, bus *Bus, timeout time.Duration) *AudioChunk {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for chunk := range bus.Audio(ctx) {
		return chunk
	}

	t.Fatalf("timed out waiting for audio chunk after %v", timeout)

	return nil
}
