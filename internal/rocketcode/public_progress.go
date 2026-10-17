package rocketcode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
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
	// PublicProgressBackground means the call returned while its work runs as a Background Job.
	PublicProgressBackground PublicProgressState = "background"
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
	// SubagentKey is the child-session key of the subagent a task call runs or continues, as
	// OpenCode keeps a task part's child session ID in its metadata.
	SubagentKey string `json:"subagent_key,omitempty"`
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
			!slices.Contains([]PublicProgressState{PublicProgressWorking, PublicProgressReview, PublicProgressCompleted, PublicProgressBlocked, PublicProgressFailed, PublicProgressStopped, PublicProgressBackground}, trace.Progress.State) {
			continue
		}

		progress = append(progress, trace.Progress)
	}

	return progress
}

// codeModeCallStatus is the state of one call an Execute script made.
type codeModeCallStatus string

// A call fails with codeModeCallError, as does a bash command that exits non-zero.
const (
	codeModeCallRunning codeModeCallStatus = "running"
	codeModeCallOK      codeModeCallStatus = "ok"
	codeModeCallError   codeModeCallStatus = "error"
	codeModeCallDenied  codeModeCallStatus = "denied"
)

const (
	// codeModeCallTraceType records one call an Execute script made, and codeModeReviewTraceType
	// the approver's whole-script decision. Neither is a model item, so replay never reads them.
	codeModeCallTraceType   = "rocketcode_code_mode_call"
	codeModeReviewTraceType = "rocketcode_code_mode_review"
	// MaxCodeModeCalls bounds an Execute call's records, which History loads with the turn.
	// ponytail: dropped calls are not counted; counting them needs an overflow record per Execute call.
	MaxCodeModeCalls         = 200
	maxCodeModeCallArguments = 4 << 10
)

// CodeModeCall is a call an Execute script made, as saved on its own while its turn runs, then
// in the turn trace, and shown in history. It keeps no output beyond Summary. Key is the call's
// ToolCallKey; Outcome and Reason are set only when the approver ran. A review record, with no
// Name, holds the approver's decision on the whole script under its Execute call's key.
type CodeModeCall struct {
	Type         string                  `json:"type"`
	ParentCallID string                  `json:"parent_call_id"`
	Key          string                  `json:"key,omitempty"`
	Name         string                  `json:"name,omitempty"`
	Arguments    string                  `json:"arguments,omitempty"`
	Status       codeModeCallStatus      `json:"status,omitempty"`
	Summary      string                  `json:"summary,omitempty"`
	Outcome      permissionReviewOutcome `json:"outcome,omitempty"`
	Reason       string                  `json:"reason,omitempty"`
}

// CodeModeCallsFromTrace decodes a turn trace's Code Mode records in trace order.
func CodeModeCallsFromTrace(traces []json.RawMessage) []CodeModeCall {
	var calls []CodeModeCall

	for _, raw := range traces {
		var record CodeModeCall
		if json.Unmarshal(raw, &record) == nil && (record.Type == codeModeCallTraceType || record.Type == codeModeReviewTraceType) {
			calls = append(calls, record)
		}
	}

	return calls
}

// String renders the record on one line for an agent reading a session: the call, its status and
// output summary, and the approver's decision when it ran. A whole-script review has only the decision.
func (r *CodeModeCall) String() string {
	text := "[" + r.ParentCallID + "]"
	if r.Name != "" {
		text += " " + r.Name + " " + r.Arguments + " -> " + string(r.Status)
		if r.Summary != "" {
			text += ": " + r.Summary
		}
	}

	if r.Outcome != "" {
		text += " (approver " + string(r.Outcome) + ": " + r.Reason + ")"
	}

	return text
}

// codeModeCallRecord starts the record of the Code Mode call running under ctx.
func codeModeCallRecord(ctx context.Context, name string, raw json.RawMessage) CodeModeCall {
	tc, _ := toolCallContextFrom(ctx)

	return CodeModeCall{Type: codeModeCallTraceType, ParentCallID: tc.callID, Key: ToolCallKey(ctx), Name: name, Arguments: clipCodeModeArguments(string(raw)), Status: codeModeCallRunning}
}

// clipCodeModeArguments bounds a Code Mode call's arguments as shown in history.
func clipCodeModeArguments(arguments string) string {
	if len(arguments) > maxCodeModeCallArguments {
		arguments = strings.ToValidUTF8(arguments[:maxCodeModeCallArguments], "") + "…[truncated]"
	}

	return arguments
}

// finish sets the call's final status and summary. The summary is the last non-empty line of what the
// script received, or of the error, or the size of binary output; never the output itself.
func (r *CodeModeCall) finish(result ToolResult, errCall error) *CodeModeCall {
	text := attachmentOutputMessage(result)

	r.Status = codeModeCallOK
	if bash, ok := result.Data.(BashResult); ok && bash.ErrorCode != "" {
		r.Status = codeModeCallError
	}

	if errCall != nil {
		text, r.Status = errCall.Error(), codeModeCallError
		if _, ok := errors.AsType[permissionDeniedError](errCall); ok {
			r.Status = codeModeCallDenied
		}
	}

	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		r.Summary = fmt.Sprintf("%d KiB of binary output", (len(text)+1023)/1024)

		return r
	}

	text = strings.TrimRightFunc(text, unicode.IsSpace)
	line := text[strings.LastIndexByte(text, '\n')+1:]
	// read's output closes with a tag; the line before it tells how much was read.
	if line == "</content>" {
		text = strings.TrimRightFunc(strings.TrimSuffix(text, line), unicode.IsSpace)
		line = text[strings.LastIndexByte(text, '\n')+1:]
	}

	r.Summary = strings.TrimSpace(line)
	if len(r.Summary) > 200 {
		r.Summary = strings.ToValidUTF8(r.Summary[:200], "")
	}

	return r
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
	// reviews holds approver decisions by ToolCallKey until their Code Mode call's record finishes.
	reviews map[string]permissionReviewDecision
	// calls holds the turn's Code Mode records in the order they were first saved. The trace never
	// carries them while the turn runs, so its saves do not rewrite them.
	calls []CodeModeCall
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
	traced := PublicProgressFromTrace(o.trace)

	for i := range traced {
		if traced[i].ID == progress.ID && traced[i].ParentID == progress.ParentID {
			snapshot = traced[i]
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
		// A call whose work became a Background Job stays "background" for the rest of its turn.
		if existing := PublicProgressFromTrace([]json.RawMessage{trace[index]})[0]; existing == snapshot || existing.State == PublicProgressBackground {
			return nil
		}

		trace[index] = raw
	}

	return o.saveTraceLocked(ctx, trace)
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

	for i := range progress {
		raw, _ := json.Marshal(publicProgressTrace{Type: "rocketcode_public_progress", Progress: progress[i]})
		records = append(records, raw)
	}

	trace = slices.Insert(trace, index, records...)
	if slices.EqualFunc(trace, o.trace, func(a, b json.RawMessage) bool { return bytes.Equal(a, b) }) {
		return nil
	}

	return o.saveTraceLocked(ctx, trace)
}

// recordCodeModeCall saves a record on its own, as a new one or over the one with its key. A replay
// from the journal leaves a finished record unchanged, so a resumed script keeps what its first run
// recorded. A finished call takes the approver decision noted under its key.
func (o *turnObservations) recordCodeModeCall(ctx context.Context, record *CodeModeCall, replay bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if review, ok := o.reviews[record.Key]; ok && record.Status != codeModeCallRunning {
		delete(o.reviews, record.Key)
		record.Outcome, record.Reason = review.Outcome, review.Rationale
	}

	if o.closed {
		return nil
	}

	if o.err != nil {
		return o.err
	}

	index, calls := -1, 0

	for i := range o.calls {
		existing := &o.calls[i]
		if existing.Type != record.Type || existing.ParentCallID != record.ParentCallID {
			continue
		}

		calls++

		if existing.Key == record.Key {
			index = i
		}
	}

	if index >= 0 && replay && o.calls[index].Status != codeModeCallRunning {
		return nil
	}

	if index < 0 && record.Type == codeModeCallTraceType && calls >= MaxCodeModeCalls {
		return nil
	}

	// Records contain only strings; JSON encoding cannot fail.
	raw, _ := json.Marshal(record)
	// Like trace saves, record saves must outlive cancellation of the work they record.
	if err := o.journal.SaveCodeModeCall(context.WithoutCancel(ctx), o.turnID, record.Key, raw); err != nil {
		o.err = progressPersistenceError{err: err}
		return o.err
	}

	if index < 0 {
		o.calls = append(o.calls, *record)
	} else {
		o.calls[index] = *record
	}

	return nil
}

func (o *turnObservations) saveTraceLocked(ctx context.Context, trace []json.RawMessage) error {
	// Like step saves, trace saves must outlive cancellation of the work they record.
	if err := o.journal.SaveTrace(context.WithoutCancel(ctx), o.turnID, trace); err != nil {
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
