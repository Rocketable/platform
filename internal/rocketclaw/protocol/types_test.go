package protocol

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWaitDeliveredPrefersDeliveryOverCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	errFrontend := errors.New("frontend failed")

	for range 100 {
		delivered := NewOutboundMessage("conversation", "answer")
		delivered.MarkDelivered(nil)
		require.NoError(t, delivered.WaitDelivered(ctx), "a delivered final is not reported as failed during shutdown")

		failed := NewOutboundMessage("conversation", "answer")
		failed.MarkDelivered(errFrontend)
		require.ErrorIs(t, failed.WaitDelivered(ctx), errFrontend)
	}

	require.ErrorIs(t, NewOutboundMessage("conversation", "answer").WaitDelivered(ctx), context.Canceled)
}
