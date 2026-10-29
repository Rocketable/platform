package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

// Model-facing background text follows OpenCode packages/core/src/tool/plugin/shell.ts
// (BACKGROUND_INSTRUCTION, backgroundResult, Input.background) and subagent.ts
// (backgroundResult, Input.background), adapted to Background Jobs and Completion Notes.
const (
	backgroundExecuteResult = "The script is running in the background (job ID: %s). You will be notified automatically when it finishes: its result arrives as a note. Unless the user explicitly asks otherwise, DO NOT poll for completion, even if you need the final result to continue. Repeatedly sleeping and checking on it is polling, not useful work. Keep working on anything that does not depend on the result. If you have nothing else to do, end your response; you will be resumed automatically when the script finishes."
	backgroundTaskResult    = "The subagent is working in the background (job ID: %s). You will be notified automatically when it finishes: its result arrives as a note.\nDO NOT sleep, poll for progress, or duplicate this subagent's work; avoid working with the same files or topics it is using.\nWork on non-overlapping tasks, or briefly tell the user what you launched and end your response."
	backgroundExecuteParam  = "Run the script in the background and return immediately (useful for long builds and test runs). You will be notified when it completes. DO NOT poll for completion. false waits for the result. An oversized background result's result_id lasts 7 days, and load_execute_result can load it from later turns; a foreground result_id still expires at turn end."
	backgroundTaskParam     = "Run the subagent in the background and return immediately. You will be notified when it completes. DO NOT sleep, poll, or proactively check on its progress. false waits for the result."
	executeDescriptionParam = "A short 3-5 word label for the script, displayed to the user."
	backgroundContinueParam = "Continue a specific previous subagent by passing its continue ID. An empty string starts a new subagent."
	backgroundModeParam     = "How the prompt reaches the subagent if it is still running: steer adds it to the running turn at its next step, enqueue runs it as a new turn after the work ahead of it, and stop stops the running turn and runs it next. A subagent that is not running runs it as its next turn in every mode. Ignored when starting a new subagent."
	continueSteered         = "Steered: the prompt joins the subagent's running turn at its next step. That turn's result reaches you as it would have without the steer: as its job's note, as the result of the task call running it, or, for a turn the subagent started itself, only in its history; continue the subagent again to get its answer. DO NOT sleep or poll for it."
	// taskContinueID follows the task_id line of OpenCode's former packages/opencode/src/tool/task.ts output.
	taskContinueID   = "continue ID: %s (for resuming to continue this task if needed)\n\n"
	continueNotChild = "continue ID %q is not a subagent you started; pass a continue ID from one of your own task results"
	unknownAgentType = "unknown agent type: %s is not a valid agent type"
	// TurnBoundRefused is what a turn-bound tool, named by its format argument, returns in background work.
	TurnBoundRefused = "Refused: %[1]s did nothing, because this call now runs as background work, after the turn it acts on. Nothing was shown to the user, and nobody will answer. This text is not %[1]s's result or the user's answer. Decide without it, or call %[1]s in the turn that receives this job's result."
)

var (
	errBackgroundUnavailable = errors.New("background mode and continue are not available to this agent; call again without them")
	errSubagentsUnavailable  = errors.New("task subagents are not available here")
)

// ErrBackgroundKilled is wrapped by the cancellation cause that stops a Background Job's work,
// such as a user stop or shutdown. Unlike other causes it also kills the job's running bash.
var ErrBackgroundKilled = errors.New("background job killed")

// BackgroundKind is the tool that started a Background Job.
type BackgroundKind string

// Background Job kinds.
const (
	BackgroundKindExecute BackgroundKind = "execute"
	BackgroundKindTask    BackgroundKind = "task"
)

// ContinueMode is how a task continue's prompt reaches a subagent session, as rocketclaw_sessions_write's mode.
type ContinueMode string

// Task continue modes.
const (
	// ContinueStop stops the subagent's running turn and runs the prompt next, ahead of its queued prompts.
	ContinueStop ContinueMode = "stop"
	// ContinueSteer joins the running turn at its next step, or else queues the prompt.
	ContinueSteer ContinueMode = "steer"
	// ContinueEnqueue runs the prompt as a new turn after the subagent's queued prompts.
	ContinueEnqueue ContinueMode = "enqueue"
)

// BackgroundJob describes one execute or task call of an agent allowed to run it in the background.
type BackgroundJob struct {
	// ID is the call's turn-scoped tool call key (ToolCallKey), stable across resume.
	ID   string
	Kind BackgroundKind
	// Label is execute's description or task's description.
	Label string
	Agent string
	// SubagentKey is the child-session key of the subagent a task call runs; empty for execute.
	SubagentKey string
	CallID      string
	// Detached reports that the call asked to start in the background.
	Detached bool
	// Prompt is a task call's prompt.
	Prompt string
}

// BackgroundResult is how a Background Job call ended for its turn.
type BackgroundResult struct {
	// Output is the finished work's output; empty when Moved.
	Output string
	// Moved reports that the work runs, or already ran, in the background.
	Moved bool
}

// BackgroundWork is the work of one execute or task call.
type BackgroundWork interface {
	RunBackground(ctx context.Context) (string, error)
	// Detach marks the work as running outside its turn, before Run reports the move. It reports
	// false, leaving the work attached, once the work stored its result for its turn.
	Detach() bool
}

// BackgroundJobs runs the execute and task calls of agents allowed to use background mode.
type BackgroundJobs interface {
	// Enabled reports whether background mode is offered at all.
	Enabled() bool
	// Run runs work and returns when it finishes attached to its call, or once it runs in the
	// background. Attached work stops with ctx and keeps its cancellation cause.
	Run(ctx context.Context, job *BackgroundJob, work BackgroundWork) (BackgroundResult, error)
}

// InertBackgroundJobs offers no background mode and runs work inline.
type InertBackgroundJobs struct{}

// Enabled reports false.
func (InertBackgroundJobs) Enabled() bool { return false }

// Run runs work inline; it never moves.
func (InertBackgroundJobs) Run(ctx context.Context, _ *BackgroundJob, work BackgroundWork) (BackgroundResult, error) {
	output, err := work.RunBackground(ctx)
	if err != nil {
		return BackgroundResult{}, fmt.Errorf("run background work: %w", err)
	}

	return BackgroundResult{Output: output}, nil
}

type backgroundParams struct {
	Background   bool         `json:"background"`
	Description  string       `json:"description"`
	Continue     taskContinue `json:"continue"`
	Prompt       string       `json:"prompt"`
	SubagentType string       `json:"subagent_type"`
}

// taskContinue is task's continue parameter; an empty SessionID starts a new subagent.
type taskContinue struct {
	SessionID string       `json:"session_id"`
	Mode      ContinueMode `json:"mode"`
}

type backgroundSinkKey struct{}

// backgroundSink owns one job's output. It forwards to the turn only while the job is attached,
// so work that outlives its call never sends on the closed turn output.
type backgroundSink struct {
	parent *backgroundSink // The job this one runs inside, if any.
	output chan<- ChatResponse

	mu       sync.Mutex
	detached bool
	settled  bool // The call chose where to store its result, so detached no longer changes.
}

func (s *backgroundSink) forward(items <-chan ChatResponse) {
	for item := range items {
		s.mu.Lock()
		if !s.detached {
			emitChatResponse(s.output, item)
		}
		s.mu.Unlock()
	}
}

// backgroundDetached reports whether ctx runs inside a job that left its turn, directly or through
// an enclosing job; turn-bound tools refuse to run there.
func backgroundDetached(ctx context.Context) bool {
	for s, _ := ctx.Value(backgroundSinkKey{}).(*backgroundSink); s != nil; s = s.parent {
		s.mu.Lock()
		detached := s.detached
		s.mu.Unlock()

		if detached {
			return true
		}
	}

	return false
}

// backgroundWork is one execute or task call. It keeps the tool call context captured when the
// call started, because the looper's per-batch state moves on once the job leaves the turn.
type backgroundWork struct {
	call     func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error)
	raw      json.RawMessage
	metadata toolCallMetadata
	tc       toolCallContext
	sink     *backgroundSink
	items    chan ChatResponse
}

func (w *backgroundWork) Detach() bool {
	w.sink.mu.Lock()
	defer w.sink.mu.Unlock()

	w.sink.detached = w.sink.detached || !w.sink.settled

	return w.sink.detached
}

func (w *backgroundWork) RunBackground(ctx context.Context) (string, error) {
	ctx = context.WithValue(context.WithValue(ctx, toolCallContextKey{}, w.tc), backgroundSinkKey{}, w.sink)

	var group errgroup.Group

	group.Go(func() error {
		w.sink.forward(w.items)
		return nil
	})

	result, err := w.call(ctx, w.raw, w.items, w.metadata)
	close(w.items)

	_ = group.Wait() // forward cannot fail.

	return result.Output, err
}

// backgroundTool offers background mode on an execute or task tool when agent may use it here.
// Otherwise the tool keeps its definition and only rejects background requests.
func (f *toolFactory) backgroundTool(agent *Agent, kind BackgroundKind, tool *looperTool) looperTool {
	call := tool.Call
	if agent == nil || !agent.Permission.Enabled("rocketclaw", AllowBackgroundSubject) || !f.backgroundJobs.Enabled() {
		tool.Call = func(ctx context.Context, raw json.RawMessage, output chan<- ChatResponse, metadata toolCallMetadata) (ToolResult, error) {
			// Malformed input is left to the call's own decoding error; continue counts as a string or an object.
			var params struct {
				Background bool `json:"background"`
				Continue   any  `json:"continue"`
			}
			if json.Unmarshal(raw, &params) == nil && (params.Background || params.Continue != nil && params.Continue != "") {
				return ToolResult{}, errBackgroundUnavailable
			}

			return call(ctx, raw, output, metadata)
		}

		return *tool
	}

	properties := tool.Definition.Parameters["properties"].(map[string]any)
	text := backgroundExecuteResult

	if kind == BackgroundKindExecute {
		properties["background"] = map[string]any{"type": "boolean", "description": backgroundExecuteParam}
	} else {
		properties["background"] = map[string]any{"type": "boolean", "description": backgroundTaskParam}
		properties["continue"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"session_id", "mode"}, "properties": map[string]any{
			"session_id": map[string]any{"type": "string", "description": backgroundContinueParam},
			"mode":       map[string]any{"type": "string", "enum": []ContinueMode{ContinueStop, ContinueSteer, ContinueEnqueue}, "description": backgroundModeParam},
		}}
		text = backgroundTaskResult
	}

	tool.Definition = *functionTool(tool.Definition.Name, tool.Definition.Description.Value, properties)
	tool.Call = func(ctx context.Context, raw json.RawMessage, output chan<- ChatResponse, metadata toolCallMetadata) (ToolResult, error) {
		var params backgroundParams
		if err := decodeToolParams(raw, &params); err != nil {
			return ToolResult{}, err
		}

		job := BackgroundJob{ID: ToolCallKey(ctx), Kind: kind, Label: params.Description, Agent: agent.Name, CallID: metadata.callID, Detached: params.Background, Prompt: params.Prompt}
		continueLine, subagent := "", ""

		if kind == BackgroundKindTask {
			subagent = "/" + delegationName(metadata.callID, job.ID)

			if id := params.Continue.SessionID; id != "" {
				name, ok := strings.CutPrefix(id, f.childKey.Path+"/")
				if !ok || name == "" || strings.Contains(name, "/") {
					return ToolResult{}, fmt.Errorf(continueNotChild, id)
				}

				// A continue writes an existing session; a new one starts as this call's.
				subagent = "/" + name
				key := ChildKey{Path: subagent, Turn: metadata.observations.turnID, Call: metadata.callID}.under(f.childKey.Path)

				// The named type takes the subagent over (OpenCode packages/core/src/tool/plugin/subagent.ts). A busy
				// subagent takes the prompt without its task call, so the type's guardrail reviews it here.
				child, ok := f.agents.Items[params.SubagentType]
				if !ok {
					return ToolResult{}, fmt.Errorf(unknownAgentType, params.SubagentType)
				}

				progress := *metadata.progress
				progress.Kind, progress.Agent, progress.Model, progress.SubagentKey = PublicProgressDelegation, child.Name, "", subagent

				if blocked, err := f.guardTask(ctx, &child, key, ChildRunStageDelegation, agent.Name, params.Prompt, "", &progress, metadata, output); err != nil || blocked != "" {
					return TextToolResult(fmt.Sprintf(taskContinueID, id) + blocked), err
				}

				steered, err := f.childSessions.WriteSubagent(ctx, &SubagentWrite{Key: key, CallKey: job.ID, Agent: child.Name, Delegator: agent.Name, Prompt: params.Prompt, Mode: params.Continue.Mode})
				if err != nil {
					return ToolResult{}, fmt.Errorf("write subagent: %w", err)
				}

				if steered {
					return TextToolResult(fmt.Sprintf(taskContinueID, id) + continueSteered), nil
				}
			}

			job.SubagentKey = f.childKey.Path + subagent
			continueLine = fmt.Sprintf(taskContinueID, job.SubagentKey)
		}

		parent, _ := ctx.Value(backgroundSinkKey{}).(*backgroundSink)
		work := &backgroundWork{call: call, raw: raw, metadata: metadata, sink: &backgroundSink{parent: parent, output: output, detached: params.Background}, items: make(chan ChatResponse, cap(output))}
		work.tc, _ = toolCallContextFrom(ctx)
		work.tc.output, work.tc.sink = work.items, work.sink

		result, err := f.backgroundJobs.Run(ctx, &job, work)
		if err != nil {
			return ToolResult{}, fmt.Errorf("run %s job: %w", kind, err)
		}

		if !result.Moved {
			return TextToolResult(continueLine + result.Output), nil
		}

		work.Detach()

		// The row names its subagent by its key under the history that holds the call.
		progress := *metadata.progress
		progress.SubagentKey = subagent

		if err := metadata.observations.finishCall(context.WithoutCancel(ctx), &progress, PublicProgressBackground); err != nil {
			return ToolResult{}, err
		}

		return TextToolResult(continueLine + fmt.Sprintf(text, job.ID)), nil
	}

	return *tool
}
