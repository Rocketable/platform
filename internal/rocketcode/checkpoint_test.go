package rocketcode

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInertCheckpointSink(t *testing.T) {
	sink := InertCheckpointSink{}
	ctx := context.Background()

	checkpoint := ActiveTurnCheckpoint{TurnID: "turn-1"}
	require.NoError(t, sink.StartActiveTurn(ctx, &checkpoint))
	require.NoError(t, sink.RecordProviderResponse(ctx, &checkpoint))
	require.NoError(t, sink.RecordCompletedToolOutput(ctx, &checkpoint))
	require.NoError(t, sink.RecordRecoveredReplay(ctx, &checkpoint))
	require.NoError(t, sink.ClearCompletedTurn(ctx, "turn-1"))
}
