package rocketcode

import "context"

// CheckpointSink persists active root-turn lifecycle checkpoints for embedders.
type CheckpointSink interface {
	StartActiveTurn(context.Context, *ActiveTurnCheckpoint) error
	RecordProviderResponse(context.Context, *ActiveTurnCheckpoint) error
	RecordCompletedToolOutput(context.Context, *ActiveTurnCheckpoint) error
	RecordRecoveredReplay(context.Context, *ActiveTurnCheckpoint) error
	ClearCompletedTurn(context.Context, string) error
}

// InertCheckpointSink ignores active root-turn lifecycle checkpoints.
type InertCheckpointSink struct{}

// StartActiveTurn ignores an active-turn start checkpoint.
func (InertCheckpointSink) StartActiveTurn(context.Context, *ActiveTurnCheckpoint) error {
	return nil
}

// RecordProviderResponse ignores a provider response checkpoint.
func (InertCheckpointSink) RecordProviderResponse(context.Context, *ActiveTurnCheckpoint) error {
	return nil
}

// RecordCompletedToolOutput ignores a completed tool-output checkpoint.
func (InertCheckpointSink) RecordCompletedToolOutput(context.Context, *ActiveTurnCheckpoint) error {
	return nil
}

// RecordRecoveredReplay ignores a recovered replay checkpoint.
func (InertCheckpointSink) RecordRecoveredReplay(context.Context, *ActiveTurnCheckpoint) error {
	return nil
}

// ClearCompletedTurn ignores completed-turn checkpoint cleanup.
func (InertCheckpointSink) ClearCompletedTurn(context.Context, string) error {
	return nil
}
