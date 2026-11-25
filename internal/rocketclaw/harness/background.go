package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

const (
	backgroundExecuteResult = "The script is running in the background (job ID: %s). You will be notified automatically when it finishes: its result arrives as a note. Unless the user explicitly asks otherwise, DO NOT poll for completion, even if you need the final result to continue. Repeatedly sleeping and checking on it is polling, not useful work. Keep working on anything that does not depend on the result. If you have nothing else to do, end your response; you will be resumed automatically when the script finishes."
	backgroundTaskResult    = "The subagent is working in the background (job ID: %s). You will be notified automatically when it finishes: its result arrives as a note.\nDO NOT sleep, poll for progress, or duplicate this subagent's work; avoid working with the same files or topics it is using.\nWork on non-overlapping tasks, or briefly tell the user what you launched and end your response."
	backgroundExecuteParam  = "Run the script in the background and return immediately (useful for long builds and test runs). You will be notified when it completes. DO NOT poll for completion. false waits for the result. An oversized background result's result_id lasts 7 days, and load_execute_result can load it from later turns; a foreground result_id still expires at turn end."
	backgroundTaskParam     = "Run the subagent in the background and return immediately. You will be notified when it completes. DO NOT sleep, poll, or proactively check on its progress. false waits for the result."
	executeDescriptionParam = "A short 3-5 word label for the script, displayed to the user."
	backgroundContinueParam = "Continue a specific previous subagent by passing its continue ID. An empty string starts a new subagent."
	backgroundModeParam     = "How the prompt reaches the subagent if it is still running: steer adds it to the running turn at its next step, enqueue runs it as a new turn after the work ahead of it, and stop stops the running turn and runs it next. A subagent that is not running runs it as its next turn in every mode. Ignored when starting a new subagent."
	continueSteered         = "Steered: the prompt joins the subagent's running turn at its next step. That turn's result reaches you as it would have without the steer: as its job's note, as the result of the task call running it, or, for a turn the subagent started itself, as a note reporting its answer to you. DO NOT sleep or poll for it."
	taskContinueID          = "continue ID: %s (for resuming to continue this task if needed)\n\n"
	continueNotChild        = "continue ID %q is not a subagent you started; pass a continue ID from one of your own task results"
	unknownAgentType        = "unknown agent type: %s is not a valid agent type"
	TurnBoundRefused        = "Refused: %[1]s did nothing, because this call now runs as background work, after the turn it acts on. Nothing was shown to the user, and nobody will answer. This text is not %[1]s's result or the user's answer. Decide without it, or call %[1]s in the turn that receives this job's result."
)

var (
	errBackgroundUnavailable = errors.New("background mode and continue are not available to this agent; call again without them")
	errSubagentsUnavailable  = errors.New("task subagents are not available here")
)

var ErrBackgroundKilled = errors.New("background job killed")

type BackgroundStopError string

func (e BackgroundStopError) Error() string { return string(e) }

func (BackgroundStopError) Unwrap() error { return ErrBackgroundKilled }

type BackgroundKind string

const (
	BackgroundKindExecute BackgroundKind = "execute"
	BackgroundKindTask    BackgroundKind = "task"
)

type ContinueMode string

const (
	ContinueStop    ContinueMode = "stop"
	ContinueSteer   ContinueMode = "steer"
	ContinueEnqueue ContinueMode = "enqueue"
)

type BackgroundJob struct {
	ID          string
	Kind        BackgroundKind
	Label       string
	Agent       string
	SubagentKey string
	CallID      string
	Detached    bool
}

type BackgroundResult struct {
	Result ToolResult
	Moved  bool
}

type BackgroundWork interface {
	RunBackground(ctx context.Context) (ToolResult, error)
	Detach() bool
}

type BackgroundJobs interface {
	Enabled() bool
	Run(ctx context.Context, job *BackgroundJob, work BackgroundWork) (BackgroundResult, error)
}

type InertBackgroundJobs struct{}

func (InertBackgroundJobs) Enabled() bool { return false }

func (InertBackgroundJobs) Run(ctx context.Context, _ *BackgroundJob, work BackgroundWork) (BackgroundResult, error) {
	result, err := work.RunBackground(ctx)
	if err != nil {
		return BackgroundResult{}, fmt.Errorf("run background work: %w", err)
	}

	return BackgroundResult{Result: result}, nil
}

type backgroundParams struct {
	Background  bool         `json:"background"`
	Description string       `json:"description"`
	Continue    taskContinue `json:"continue"`
}

type taskContinue struct {
	SessionID string       `json:"session_id"`
	Mode      ContinueMode `json:"mode"`
}

type backgroundSinkKey struct{}

type backgroundSink struct {
	parent *backgroundSink
	output chan<- ChatResponse

	mu       sync.Mutex
	detached bool
	settled  bool
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

type backgroundWork struct {
	name     string
	call     func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error)
	raw      json.RawMessage
	metadata toolCallMetadata
	tc       toolCallContext
	items    chan ChatResponse
}

func (w *backgroundWork) Detach() bool {
	w.tc.sink.mu.Lock()
	defer w.tc.sink.mu.Unlock()

	w.tc.sink.detached = w.tc.sink.detached || !w.tc.sink.settled

	return w.tc.sink.detached
}

func (w *backgroundWork) RunBackground(ctx context.Context) (ToolResult, error) {
	ctx = context.WithValue(context.WithValue(ctx, toolCallContextKey{}, w.tc), backgroundSinkKey{}, w.tc.sink)

	var group errgroup.Group

	group.Go(func() error {
		w.tc.sink.forward(w.items)
		return nil
	})

	result, err := w.call(ctx, w.raw, w.items, w.metadata)
	close(w.items)

	_ = group.Wait()

	if _, rendered := errors.AsType[toolResultError](err); err == nil || rendered {
		return result, err
	}

	result = ToolCallResult(w.name, result, err)

	return result, toolResultError{result: result, err: err}
}

func (f *toolFactory) backgroundTool(agent *Agent, kind BackgroundKind, tool *looperTool) looperTool {
	call := tool.Call
	if !agent.Permission.Enabled("rocketclaw", AllowBackgroundSubject) || !f.backgroundJobs.Enabled() {
		tool.Call = func(ctx context.Context, raw json.RawMessage, output chan<- ChatResponse, metadata toolCallMetadata) (ToolResult, error) {
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

		job := BackgroundJob{ID: ToolCallKey(ctx), Kind: kind, Label: params.Description, Agent: agent.Name, CallID: metadata.callID, Detached: params.Background}
		continueLine, subagent := "", ""

		if kind == BackgroundKindTask {
			subagent = "/" + delegationName(metadata.callID, job.ID)

			if id := params.Continue.SessionID; id != "" {
				name, ok := strings.CutPrefix(id, f.childKey.Path+"/")
				if !ok || name == "" || strings.Contains(name, "/") {
					return ToolResult{}, fmt.Errorf(continueNotChild, id)
				}

				subagent = "/" + name
				key := ChildKey{Path: subagent, Turn: metadata.observations.turnID, Call: metadata.callID}.under(f.childKey.Path)

				var task taskParams
				if err := decodeToolParams(raw, &task); err != nil {
					return ToolResult{}, err
				}

				write, err := f.subagentWrite(ctx, &task, tool.Definition.Name == taskCloneToolName)
				if err != nil {
					return ToolResult{}, err
				}

				write.Key, write.CallKey, write.Mode = key, job.ID, params.Continue.Mode

				steered, err := f.childSessions.WriteSubagent(ctx, &write)
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
		work := &backgroundWork{name: tool.Definition.Name, call: call, raw: raw, metadata: metadata, items: make(chan ChatResponse, cap(output))}
		work.tc, _ = toolCallContextFrom(ctx)
		work.tc.output, work.tc.sink = work.items, &backgroundSink{parent: parent, output: output, detached: params.Background}

		result, err := f.backgroundJobs.Run(ctx, &job, work)
		if err != nil {
			return ToolResult{}, fmt.Errorf("run %s job: %w", kind, err)
		}

		if !result.Moved {
			result.Result.Output = continueLine + result.Result.Output

			return result.Result, nil
		}

		work.Detach()

		progress := *metadata.progress
		progress.SubagentKey = subagent

		if _, err := metadata.observations.finishCall(context.WithoutCancel(ctx), &progress, PublicProgressBackground); err != nil {
			return ToolResult{}, err
		}

		return TextToolResult(continueLine + fmt.Sprintf(text, job.ID)), nil
	}

	return *tool
}
