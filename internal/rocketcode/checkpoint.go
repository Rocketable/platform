package rocketcode

import (
	"context"
	"encoding/json"
)

// CheckpointSink persists active root-turn lifecycle checkpoints for embedders.
type CheckpointSink interface {
	StartActiveTurn(context.Context, *ActiveTurnCheckpoint) error
	RecordProviderResponse(context.Context, *ActiveTurnCheckpoint) error
	RecordCompletedToolOutput(context.Context, *ActiveTurnCheckpoint) error
	RecordRecoveredReplay(context.Context, *ActiveTurnCheckpoint) error
	RecordOutputTrace(context.Context, string, []json.RawMessage) error
	CloseActiveTurn(context.Context, string, PublicProgressState) error
	ClearCompletedTurn(context.Context, string) error
}

// InertCheckpointSink ignores active root-turn lifecycle checkpoints.
type InertCheckpointSink struct{}

// RecordOutputTrace ignores display-only progress.
func (InertCheckpointSink) RecordOutputTrace(context.Context, string, []json.RawMessage) error {
	return nil
}

// CloseActiveTurn ignores failed/stopped closure; an empty state preserves recovery.
func (InertCheckpointSink) CloseActiveTurn(context.Context, string, PublicProgressState) error {
	return nil
}

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
