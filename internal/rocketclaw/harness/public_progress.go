package harness

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
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
	codeModeCallRunning   codeModeCallStatus = "running"
	codeModeCallOK        codeModeCallStatus = "ok"
	codeModeCallError     codeModeCallStatus = "error"
	codeModeCallDenied    codeModeCallStatus = "denied"
	codeModeCallCancelled codeModeCallStatus = "cancelled"
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

	CreatedPullRequests   []string `json:"created_pull_requests,omitempty"`
	MentionedPullRequests []string `json:"mentioned_pull_requests,omitempty"`

	command string
}

func SettleStoppedTrace(ctx context.Context, journal Journal, turnID string, trace []json.RawMessage) error {
	for i := range trace {
		items := PublicProgressFromTrace(trace[i : i+1])
		if len(items) != 1 || items[0].Kind != PublicProgressTool && items[0].Kind != PublicProgressDelegation || items[0].State != PublicProgressReview && items[0].State != PublicProgressWorking {
			continue
		}

		var step callStep
		if _, err := loadStep(ctx, journal, CallKey(turnID, items[0].ID), &step); err != nil {
			return err
		}

		items[0].State = PublicProgressStopped
		if len(step.Output) > 0 {
			items[0].State = cmp.Or(step.State, PublicProgressCompleted)
		}

		trace[i], _ = json.Marshal(publicProgressTrace{Type: "rocketcode_public_progress", Progress: items[0]})
	}

	return nil
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
		if r.Summary != "" && r.Summary != string(r.Status) {
			text += ": " + r.Summary
		}
	}

	if r.Outcome != "" {
		text += " (approver " + string(r.Outcome) + ": " + r.Reason + ")"
	}

	return text
}

func (r *CodeModeCall) PullRequests() (created, mentioned []string) {
	var bash bashParams
	if len(r.CreatedPullRequests)+len(r.MentionedPullRequests) > 0 || r.Name != "bash" || r.Status != codeModeCallOK || !strings.Contains(r.Summary, "https://github.com/") || json.Unmarshal([]byte(r.Arguments), &bash) != nil {
		return r.CreatedPullRequests, r.MentionedPullRequests
	}

	return PullRequestsIn(bash.Command, r.Summary)
}

func codeModeCallRecord(ctx context.Context, name string, raw json.RawMessage) CodeModeCall {
	tc, _ := toolCallContextFrom(ctx)

	var bash bashParams
	if name == "bash" {
		_ = json.Unmarshal(raw, &bash)
	}

	return CodeModeCall{Type: codeModeCallTraceType, ParentCallID: tc.callID, Key: ToolCallKey(ctx), Name: name, Arguments: clipCodeModeArguments(string(raw)), Status: codeModeCallRunning, command: bash.Command}
}

func clipCodeModeArguments(arguments string) string {
	if len(arguments) > maxCodeModeCallArguments {
		arguments = strings.ToValidUTF8(arguments[:maxCodeModeCallArguments], "") + "…[truncated]"
	}

	return arguments
}

func (r *CodeModeCall) finish(result ToolResult, errCall error) *CodeModeCall {
	text, command := result.Output, r.command
	r.command = ""

	r.Status = codeModeCallOK
	if value, ok := hostCallValue(result); ok && codemode.IsFailure(value) {
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

	if command != "" && r.Status == codeModeCallOK && strings.Contains(text, "https://github.com/") {
		r.CreatedPullRequests, r.MentionedPullRequests = PullRequestsIn(command, text)
	}

	text = strings.TrimRightFunc(text, unicode.IsSpace)

	r.Summary = strings.TrimSpace(text[strings.LastIndexByte(text, '\n')+1:])
	if len(r.Summary) > 200 {
		r.Summary = strings.ToValidUTF8(r.Summary[:200], "")
	}

	return r
}

func (r *CodeModeCall) cancel() *CodeModeCall {
	r.command, r.Status, r.Summary = "", codeModeCallCancelled, "cancelled"

	return r
}

const (
	githubPullRequest   = `https://github\.com/([a-zA-Z\d][a-zA-Z\d-]*)/([\w.-]+)/pull/([1-9]\d*)`
	maxPullRequestLinks = 100
)

var (
	pullRequestMention = regexp.MustCompile(githubPullRequest + `\b`)
	pullRequestLine    = regexp.MustCompile(`(?m)^[ \t]*` + githubPullRequest + `(?:/(?:files|commits|checks))?/?(?:[?#]\S*)?[ \t\r]*$`)
	pullRequestNumber  = regexp.MustCompile(`(?:^|[\s#/=(])(\d+)`)
)

func PullRequestsIn(command, output string) (created, mentioned []string) {
	var (
		creates         int
		dryRun, listing bool
		named           []string
	)

	if file, err := syntax.NewParser().Parse(strings.NewReader(command), ""); err == nil {
		syntax.Walk(file, func(node syntax.Node) bool {
			switch node := node.(type) {
			case *syntax.CallExpr:
				switch name, arguments := ghCommand(node); name {
				case "pr create", "pr new":
					creates++
					dryRun = dryRun || slices.ContainsFunc(arguments, func(argument string) bool {
						return argument == "--dry-run" || strings.HasPrefix(argument, "--dry-run=")
					})

					return false
				case "pr list", "pr status", "search prs", "api":
					listing = true
				}
			case *syntax.Lit:
				named = appendPullRequestNumbers(named, node.Value)
			case *syntax.SglQuoted:
				named = appendPullRequestNumbers(named, node.Value)
			}

			return true
		})
	}

	if creates > 0 && !dryRun {
		for _, match := range pullRequestLine.FindAllStringSubmatch(output, -1) {
			url := "https://github.com/" + match[1] + "/" + match[2] + "/pull/" + match[3]
			if slices.Contains(created, url) || slices.Contains(named, match[3]) {
				continue
			}

			if created = append(created, url); len(created) > creates {
				created = nil
				break
			}
		}
	}

	created = created[:min(len(created), maxPullRequestLinks)]
	if listing {
		return created, nil
	}

	for _, url := range pullRequestMention.FindAllString(output, -1) {
		if len(mentioned) < maxPullRequestLinks && !slices.Contains(created, url) && !slices.Contains(mentioned, url) {
			mentioned = append(mentioned, url)
		}
	}

	return created, mentioned
}

func ghCommand(call *syntax.CallExpr) (name string, arguments []string) {
	fields := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		field, _ := expand.Literal(&expand.Config{}, word)
		fields = append(fields, field)
	}

	for len(fields) > 1 && (fields[0] == "env" || fields[0] == "nohup" || fields[0] == "timeout" || strings.Contains(fields[0], "=")) {
		if fields[0] == "timeout" {
			fields = fields[1:]
		}

		fields = fields[1:]
	}

	switch {
	case len(fields) < 2 || path.Base(fields[0]) != "gh":
		return "", nil
	case fields[1] == "api" || len(fields) == 2:
		return fields[1], fields[2:]
	default:
		return fields[1] + " " + fields[2], fields[3:]
	}
}

func appendPullRequestNumbers(numbers []string, text string) []string {
	for _, number := range pullRequestNumber.FindAllStringSubmatchIndex(text, -1) {
		if number[3] == len(text) || strings.IndexByte("\t\n\f\r );&|/", text[number[3]]) >= 0 {
			numbers = append(numbers, text[number[2]:number[3]])
		}
	}

	return numbers
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

	return o.observeLocked(ctx, progress, nil)
}

func (o *turnObservations) finishCall(ctx context.Context, progress *PublicProgress, state PublicProgressState) (PublicProgressState, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	err := o.observeLocked(ctx, progress, &state)

	return state, err
}

func (o *turnObservations) observeLocked(ctx context.Context, progress *PublicProgress, finalState *PublicProgressState) error {
	if o.closed {
		return nil
	}

	if o.err != nil {
		return o.err
	}

	snapshot := *progress
	index := len(o.trace)

	var existing PublicProgress

	for i := range o.trace {
		items := PublicProgressFromTrace(o.trace[i : i+1])
		if len(items) == 1 && items[0].ID == progress.ID && items[0].ParentID == progress.ParentID {
			existing, index = items[0], i
			break
		}
	}

	if finalState != nil {
		if index < len(o.trace) {
			snapshot = existing
		}

		snapshot.SubagentKey = cmp.Or(snapshot.SubagentKey, progress.SubagentKey)
		snapshot.State = *finalState
	}

	if index < len(o.trace) && (existing == snapshot || existing.State == PublicProgressBackground) {
		if finalState != nil {
			*finalState = existing.State
		}

		return nil
	}

	trace := make([]json.RawMessage, max(len(o.trace), index+1))
	copy(trace, o.trace)
	trace[index], _ = json.Marshal(publicProgressTrace{Type: "rocketcode_public_progress", Progress: snapshot})

	return o.saveTraceLocked(ctx, trace)
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

	var records []json.RawMessage

	for i := range progress {
		raw, _ := json.Marshal(publicProgressTrace{Type: "rocketcode_public_progress", Progress: progress[i]})
		records = append(records, raw)
	}

	trace := make([]json.RawMessage, 0, len(o.trace))
	for _, raw := range o.trace {
		if items := PublicProgressFromTrace([]json.RawMessage{raw}); len(items) == 1 && (items[0].Kind == PublicProgressText || items[0].Kind == PublicProgressThinking) && items[0].ParentID == parentID {
			trace, records = append(trace, records...), nil
		} else {
			trace = append(trace, raw)
		}
	}

	trace = append(trace, records...)
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
