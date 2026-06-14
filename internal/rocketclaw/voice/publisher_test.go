package voice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishTranscriptionRelaysBeforePublishingInboundForWebVoice(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	steps := make([]string, 0, 2)
	publisher := NewTranscriptionPublisher(bus, testLogger(), events.SourceWebVoice, nil, func(context.Context, string) (*events.InboundMessage, error) {
		steps = append(steps, "relay")
		return &events.InboundMessage{SlackReply: &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: ""}}, nil
	})

	published, err := publisher.PublishTranscription(context.Background(), "hello from browser voice", "browser-session-1")
	require.NoError(t, err)
	require.True(t, published)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	for msg := range bus.Inbound(ctx) {
		steps = append(steps, "publish")

		assert.Equal(t, events.SourceWebVoice, msg.Source)
		assert.Equal(t, "hello from browser voice", msg.Text)
		assert.NotContains(t, msg.Text, "Browser voice utterance:")
		assert.Equal(t, "browser-session-1", msg.WebSessionID)
		require.NotNil(t, msg.SlackReply)
		assert.Equal(t, "D123", msg.SlackReply.ChannelID)
		assert.Equal(t, "111.222", msg.SlackReply.MessageTS)

		break
	}

	require.Len(t, steps, 2, "expected inbound message after browser publishTranscription")
	assert.Equal(t, []string{"relay", "publish"}, steps)
}

func TestPublishTranscriptionStopsWhenWebRelayFails(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	publisher := NewTranscriptionPublisher(bus, testLogger(), events.SourceWebVoice, nil, func(context.Context, string) (*events.InboundMessage, error) {
		return nil, errors.New("relay failed")
	})

	_, err := publisher.PublishTranscription(context.Background(), "hello from browser voice", "browser-session-1")
	require.Error(t, err)

	requireNoInboundMessages(t, bus)
}

func TestPublishTranscriptionReportsInboundPublishError(t *testing.T) {
	bus := events.New()

	bus.StopInbound()
	defer bus.Close()

	publisher := NewTranscriptionPublisher(bus, testLogger(), events.SourceWebVoice, nil, func(context.Context, string) (*events.InboundMessage, error) {
		return nil, nil
	})

	published, err := publisher.PublishTranscription(context.Background(), "hello from browser voice", "browser-session-1")
	require.ErrorIs(t, err, events.ErrBusClosed)
	require.ErrorContains(t, err, "publish voice utterance to inbound bus")
	assert.False(t, published)
}

func TestPublishTranscriptionIgnoresBlankText(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	relayed := false
	publisher := NewTranscriptionPublisher(bus, testLogger(), events.SourceWebVoice, nil, func(context.Context, string) (*events.InboundMessage, error) {
		relayed = true
		return nil, nil
	})

	published, err := publisher.PublishTranscription(context.Background(), " \n\t ", "browser-session-1")
	require.NoError(t, err)
	assert.False(t, published)
	assert.False(t, relayed)
	requireNoInboundMessages(t, bus)
}
