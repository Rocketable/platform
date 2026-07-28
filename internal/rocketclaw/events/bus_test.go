package events

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBusRoutesOutbound(t *testing.T) {
	bus := New()
	message := NewOutboundMessage(SourceSystem, "slack-thread:C1:1.2", "answer", OutputTargetSlack)
	require.NoError(t, bus.PublishOutbound(t.Context(), message))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	for got := range bus.Outbound(ctx) {
		require.Same(t, message, got)
		cancel()
	}
}

func TestBusPublishOutboundCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	bus := New()
	message := NewOutboundMessage(SourceSystem, "slack-thread:C1:1.2", "answer", OutputTargetSlack)
	err := bus.PublishOutbound(ctx, message)
	require.ErrorIs(t, err, context.Canceled, "PublishOutbound() error = %v, want context.Canceled", err)
}
