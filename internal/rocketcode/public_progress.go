package rocketcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
)

// PublicProgressKind identifies display-only root activity, never replay input.
type PublicProgressKind string

// Public progress kinds distinguish text, tools, and delegated activity.
const (
	PublicProgressText       PublicProgressKind = "text"
	PublicProgressTool       PublicProgressKind = "tool"
	PublicProgressDelegation PublicProgressKind = "delegation"
)

// PublicProgressState is a fixed public lifecycle label, not diagnostic text.
type PublicProgressState string

// Public progress states describe execution and review outcomes.
const (
	PublicProgressWorking   PublicProgressState = "working"
	PublicProgressReview    PublicProgressState = "review"
	PublicProgressCompleted PublicProgressState = "completed"
	PublicProgressBlocked   PublicProgressState = "blocked"
	PublicProgressFailed    PublicProgressState = "failed"
	PublicProgressStopped   PublicProgressState = "stopped"
)

// PublicProgress contains only explicitly public text and producer identity.
// ID is a response/item or call identity, qualified by ParentID when present.
// Slice order preserves the producer's original item/call order.
type PublicProgress struct {
	ID       string              `json:"id"`
	ParentID string              `json:"parent_id,omitempty"`
	Kind     PublicProgressKind  `json:"kind"`
	State    PublicProgressState `json:"state"`
	Text     string              `json:"text,omitempty"`
	Agent    string              `json:"agent,omitempty"`
	Model    string              `json:"model,omitempty"`
}

type publicProgressTrace struct {
	Type     string         `json:"type"`
	Progress PublicProgress `json:"progress"`
}

// PublicProgressFromTrace decodes only recognized public records. Legacy,
// malformed, and unknown future records remain stored but are never projected.
func PublicProgressFromTrace(traces []json.RawMessage) []PublicProgress {
	var progress []PublicProgress

	for _, raw := range traces {
		var trace publicProgressTrace
		if json.Unmarshal(raw, &trace) != nil || trace.Type != "rocketcode_public_progress" || trace.Progress.ID == "" {
			continue
		}

		if !slices.Contains([]PublicProgressKind{PublicProgressText, PublicProgressTool, PublicProgressDelegation}, trace.Progress.Kind) ||
			!slices.Contains([]PublicProgressState{PublicProgressWorking, PublicProgressReview, PublicProgressCompleted, PublicProgressBlocked, PublicProgressFailed, PublicProgressStopped}, trace.Progress.State) {
			continue
		}

		progress = append(progress, trace.Progress)
	}

	return progress
}

// turnObservations owns trace merging and all persistence through final closure.
// Workers submit immutable snapshots; replay stays on the root goroutine.
type turnObservations struct {
	journal Journal

	mu     sync.Mutex
	turnID string
	trace  []json.RawMessage
	closed bool
	err    error
}

func (o *turnObservations) observe(ctx context.Context, progress *PublicProgress) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.observeLocked(ctx, progress)
}

// finishCall retains canonical child attribution and a guardrail's blocked outcome.
func (o *turnObservations) finishCall(ctx context.Context, progress *PublicProgress, state PublicProgressState) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	snapshot := *progress
	for _, item := range PublicProgressFromTrace(o.trace) {
		if item.ID == progress.ID && item.ParentID == progress.ParentID {
			snapshot = item
			break
		}
	}

	if snapshot.State != PublicProgressBlocked {
		snapshot.State = state
	}

	return o.observeLocked(ctx, &snapshot)
}

func (o *turnObservations) observeLocked(ctx context.Context, progress *PublicProgress) error {
	if o.closed {
		return nil // A worker finishing after terminal closure cannot revive the turn.
	}

	if o.err != nil {
		return o.err
	}
	// Public records contain only strings and structs; JSON encoding cannot fail.
	snapshot := *progress
	raw, _ := json.Marshal(publicProgressTrace{Type: "rocketcode_public_progress", Progress: snapshot})
	trace := slices.Clone(o.trace)

	index := slices.IndexFunc(trace, func(raw json.RawMessage) bool {
		items := PublicProgressFromTrace([]json.RawMessage{raw})
		return len(items) == 1 && items[0].ID == snapshot.ID && items[0].ParentID == snapshot.ParentID
	})
	if index < 0 {
		trace = append(trace, raw)
	} else {
		if PublicProgressFromTrace([]json.RawMessage{trace[index]})[0] == snapshot {
			return nil
		}

		trace[index] = raw
	}
	// Ponytail: one synchronous write per changed public event. Coalescing needs measurements.
	if err := o.journal.SaveTrace(ctx, o.turnID, trace); err != nil {
		o.err = progressPersistenceError{err: err}
		return o.err
	}

	o.trace = trace

	return nil
}

// replaceResponse atomically replaces one attempt's public text, retaining other
// observations and the response's original position. Empty progress removes it.
func (o *turnObservations) replaceResponse(ctx context.Context, parentID string, progress []PublicProgress) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.closed {
		return nil
	}

	if o.err != nil {
		return o.err
	}

	belongs := func(raw json.RawMessage) bool {
		items := PublicProgressFromTrace([]json.RawMessage{raw})
		return len(items) == 1 && items[0].Kind == PublicProgressText && items[0].ParentID == parentID
	}
	index := slices.IndexFunc(o.trace, belongs)

	trace := slices.DeleteFunc(slices.Clone(o.trace), belongs)
	if index < 0 {
		index = len(trace)
	}

	var records []json.RawMessage

	for _, item := range progress {
		raw, _ := json.Marshal(publicProgressTrace{Type: "rocketcode_public_progress", Progress: item})
		records = append(records, raw)
	}

	trace = slices.Insert(trace, index, records...)
	if slices.EqualFunc(trace, o.trace, func(a, b json.RawMessage) bool { return bytes.Equal(a, b) }) {
		return nil
	}

	if err := o.journal.SaveTrace(ctx, o.turnID, trace); err != nil {
		o.err = progressPersistenceError{err: err}
		return o.err
	}

	o.trace = trace

	return nil
}

func (o *turnObservations) close() {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
}

// progressPersistenceError must bypass provider retry and tool-result conversion.
type progressPersistenceError struct {
	err error
}

func (e progressPersistenceError) Error() string {
	return fmt.Sprintf("persist public progress: %v", e.err)
}

func (e progressPersistenceError) Unwrap() error {
	return e.err
}
