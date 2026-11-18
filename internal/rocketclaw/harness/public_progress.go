package harness

import (
	"bytes"
	"cmp"
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

type PublicProgressKind string

const (
	PublicProgressText       PublicProgressKind = "text"
	PublicProgressThinking   PublicProgressKind = "thinking"
	PublicProgressTool       PublicProgressKind = "tool"
	PublicProgressDelegation PublicProgressKind = "delegation"
)

type PublicProgressState string

const (
	PublicProgressWorking    PublicProgressState = "working"
	PublicProgressReview     PublicProgressState = "review"
	PublicProgressCompleted  PublicProgressState = "completed"
	PublicProgressBlocked    PublicProgressState = "blocked"
	PublicProgressFailed     PublicProgressState = "failed"
	PublicProgressStopped    PublicProgressState = "stopped"
	PublicProgressBackground PublicProgressState = "background"
)

type PublicProgress struct {
	ID          string              `json:"id"`
	ParentID    string              `json:"parent_id,omitempty"`
	Kind        PublicProgressKind  `json:"kind"`
	State       PublicProgressState `json:"state"`
	Text        string              `json:"text,omitempty"`
	Agent       string              `json:"agent,omitempty"`
	Model       string              `json:"model,omitempty"`
	SubagentKey string              `json:"subagent_key,omitempty"`
}

type publicProgressTrace struct {
	Type     string         `json:"type"`
	Progress PublicProgress `json:"progress"`
}

func PublicProgressFromTrace(traces []json.RawMessage) []PublicProgress {
	var progress []PublicProgress

	for _, raw := range traces {
		var trace publicProgressTrace
		if json.Unmarshal(raw, &trace) != nil || trace.Type != "rocketcode_public_progress" || trace.Progress.ID == "" {
			continue
		}

		if !slices.Contains([]PublicProgressKind{PublicProgressText, PublicProgressThinking, PublicProgressTool, PublicProgressDelegation}, trace.Progress.Kind) ||
			!slices.Contains([]PublicProgressState{PublicProgressWorking, PublicProgressReview, PublicProgressCompleted, PublicProgressBlocked, PublicProgressFailed, PublicProgressStopped, PublicProgressBackground}, trace.Progress.State) {
			continue
		}

		progress = append(progress, trace.Progress)
	}

	return progress
}

type codeModeCallStatus string

const (
	codeModeCallRunning codeModeCallStatus = "running"
	codeModeCallOK      codeModeCallStatus = "ok"
	codeModeCallError   codeModeCallStatus = "error"
	codeModeCallDenied  codeModeCallStatus = "denied"
)

const (
	codeModeCallTraceType    = "rocketcode_code_mode_call"
	codeModeReviewTraceType  = "rocketcode_code_mode_review"
	MaxCodeModeCalls         = 200
	maxCodeModeCallArguments = 4 << 10
)

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

func codeModeCallRecord(ctx context.Context, name string, raw json.RawMessage) CodeModeCall {
	tc, _ := toolCallContextFrom(ctx)

	return CodeModeCall{Type: codeModeCallTraceType, ParentCallID: tc.callID, Key: ToolCallKey(ctx), Name: name, Arguments: clipCodeModeArguments(string(raw)), Status: codeModeCallRunning}
}

func clipCodeModeArguments(arguments string) string {
	if len(arguments) > maxCodeModeCallArguments {
		arguments = strings.ToValidUTF8(arguments[:maxCodeModeCallArguments], "") + "…[truncated]"
	}

	return arguments
}

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

type turnObservations struct {
	journal Journal

	mu      sync.Mutex
	turnID  string
	trace   []json.RawMessage
	closed  bool
	err     error
	reviews map[string]permissionReviewDecision
	calls   []CodeModeCall
	saved   turnStep
}

func (o *turnObservations) observe(ctx context.Context, progress *PublicProgress) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.observeLocked(ctx, progress)
}

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

	snapshot.SubagentKey = cmp.Or(snapshot.SubagentKey, progress.SubagentKey)

	snapshot.State = state

	return o.observeLocked(ctx, &snapshot)
}

func (o *turnObservations) observeLocked(ctx context.Context, progress *PublicProgress) error {
	if o.closed {
		return nil
	}

	if o.err != nil {
		return o.err
	}

	snapshot := *progress
	raw, _ := json.Marshal(publicProgressTrace{Type: "rocketcode_public_progress", Progress: snapshot})
	trace := slices.Clone(o.trace)

	for i := range trace {
		items := PublicProgressFromTrace(trace[i : i+1])
		if len(items) != 1 || items[0].ID != snapshot.ID || items[0].ParentID != snapshot.ParentID {
			continue
		}

		if existing := items[0]; existing == snapshot || existing.State == PublicProgressBackground {
			return nil
		}

		trace[i] = raw

		return o.saveTraceLocked(ctx, trace)
	}

	return o.saveTraceLocked(ctx, append(trace, raw))
}

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
		return len(items) == 1 && (items[0].Kind == PublicProgressText || items[0].Kind == PublicProgressThinking) && items[0].ParentID == parentID
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

	raw, _ := json.Marshal(record)
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

type progressPersistenceError struct {
	err error
}

func (e progressPersistenceError) Error() string {
	return fmt.Sprintf("persist public progress: %v", e.err)
}

func (e progressPersistenceError) Unwrap() error {
	return e.err
}
