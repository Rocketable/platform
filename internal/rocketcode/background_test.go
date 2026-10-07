package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

// todayExecuteParameters and todayTaskParameters are the pre-background definitions.
const (
	todayExecuteParameters = `{"additionalProperties":false,"properties":{"code":{"description":"Starlark source defining def main() that returns a string (or JSON-encodable value). Starlark, not Python. Parsed before any host tool runs; a codemode.star error means the wrapper failed and nothing ran. execute code is a JSON string — JSON still wraps it in \"...\". Inside that string, bash(command=...) takes r'''...''' only, not Starlark \"...\" or '...'. r\"...\" is raw but single-line; a real newline needs r'''...'''. Example execute argument: {\"code\":\"def main():\\n    return bash(command=r'''grep -nE 'architecture|loop' FILE''')\\n\"}. $ is valid inside a closed Starlark string, not interpolation. bash(...) is text-like: use str(result) before find/split. Failed wrapper output is not evidence; fix and rerun. Concurrency: gather([lambda: …], concurrency=16) — one list of zero-arg lambdas, not varargs.","type":"string"}},"required":["code"],"type":"object"}`
	todayTaskParameters    = `{"additionalProperties":false,"properties":{"command":{"type":"string"},"description":{"type":"string"},"prompt":{"type":"string"},"subagent_type":{"type":"string"}},"required":["command","description","prompt","subagent_type"],"type":"object"}`
	backgroundPermissions  = `{rocketclaw: {allow_background: allow}, task: allow, read: allow, probe: auto}`
)

func enabledBackgroundJobs() *mockBackgroundJobs {
	return &mockBackgroundJobs{EnabledFunc: func() bool { return true }, DrainNotesFunc: func(context.Context, string) []PromptInput { return nil }}
}

func TestBackgroundSchemasFollowPermission(t *testing.T) {
	for _, tc := range []struct {
		name, permissions string
		jobs              BackgroundJobs
		offered           bool
	}{
		{name: "rocketclaw allow", permissions: `{rocketclaw: allow, task: allow, read: allow}`, jobs: enabledBackgroundJobs()},
		{name: "rocketclaw wildcard", permissions: `{rocketclaw: {'*': allow}, task: allow, read: allow}`, jobs: enabledBackgroundJobs()},
		{name: "exact deny", permissions: `{rocketclaw: {allow_background: deny}, task: allow, read: allow}`, jobs: enabledBackgroundJobs()},
		{name: "allowed without registry", permissions: backgroundPermissions, jobs: InertBackgroundJobs{}},
		{name: "allowed", permissions: backgroundPermissions, jobs: enabledBackgroundJobs(), offered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{"child": testAgent("child")}})
			factory.backgroundJobs = tc.jobs
			model := factory.toolsFor(&Agent{Name: "main", Permission: parsePermissionYAML(t, tc.permissions)})

			require.Equal(t, executeDescription(), model[executeToolName].Definition.Description.Value)

			if !tc.offered {
				require.JSONEq(t, todayExecuteParameters, string(marshalParameters(t, model[executeToolName].Definition.Parameters)))
				require.JSONEq(t, todayTaskParameters, string(marshalParameters(t, model["task"].Definition.Parameters)))

				return
			}

			var execute, task struct {
				Properties map[string]struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				} `json:"properties"`
				Required []string `json:"required"`
			}

			require.NoError(t, json.Unmarshal(marshalParameters(t, model[executeToolName].Definition.Parameters), &execute))
			require.NoError(t, json.Unmarshal(marshalParameters(t, model["task"].Definition.Parameters), &task))
			require.Equal(t, []string{"background", "code", "description"}, execute.Required)
			require.Equal(t, []string{"background", "command", "continue", "description", "prompt", "subagent_type"}, task.Required)
			require.Equal(t, "boolean", execute.Properties["background"].Type)
			require.Equal(t, backgroundExecuteParam, execute.Properties["background"].Description)
			require.Contains(t, execute.Properties["background"].Description, "result_id lasts 7 days")
			require.Equal(t, backgroundLabelParam, execute.Properties["description"].Description)
			require.Equal(t, "boolean", task.Properties["background"].Type)
			require.Equal(t, backgroundTaskParam, task.Properties["background"].Description)
			require.Equal(t, backgroundContinueParam, task.Properties["continue"].Description)
		})
	}
}

func TestBackgroundRequestsRejectedWhenNotOffered(t *testing.T) {
	executeArgs := `{"code":"def main():\n    return probe()\n","description":"","background":true}`
	taskArgs := `{"subagent_type":"missing","prompt":"look","description":"Review","command":"","background":true,"continue":""}`

	for _, tc := range []struct {
		name, permissions, tool, args string
		jobs                          BackgroundJobs
		want                          error
	}{
		{name: "denied execute", permissions: `{rocketclaw: allow, task: allow, probe: auto}`, tool: executeToolName, args: executeArgs, jobs: enabledBackgroundJobs(), want: errBackgroundUnavailable},
		{name: "denied task", permissions: `{rocketclaw: allow, task: allow, probe: auto}`, tool: "task", args: taskArgs, jobs: enabledBackgroundJobs(), want: errBackgroundUnavailable},
		{name: "unavailable execute", permissions: `{rocketclaw: {allow_background: allow}, task: allow, probe: auto}`, tool: executeToolName, args: executeArgs, jobs: InertBackgroundJobs{}, want: errBackgroundUnavailable},
		{name: "unavailable task", permissions: `{rocketclaw: {allow_background: allow}, task: allow, probe: auto}`, tool: "task", args: taskArgs, jobs: InertBackgroundJobs{}, want: errBackgroundUnavailable},
		{name: "denied continue", permissions: `{rocketclaw: allow, task: allow, probe: auto}`, tool: "task", args: strings.Replace(strings.Replace(taskArgs, `"continue":""`, `"continue":"/call-0"`, 1), `"background":true`, `"background":false`, 1), jobs: enabledBackgroundJobs(), want: errBackgroundUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probed := false
			loop := backgroundTestLooper(t, tc.permissions, tc.jobs, func(context.Context) (ToolResult, error) {
				probed = true
				return TextToolResult("probed"), nil
			})
			ctx := withToolCallContext(t.Context(), loop, nil, "call-1")

			_, err := loop.Tools[tc.tool].Call(ctx, json.RawMessage(tc.args), nil, toolCallMetadata{callID: "call-1", observations: loop.observations, progress: &PublicProgress{}})

			require.ErrorIs(t, err, tc.want)
			require.False(t, probed)
		})
	}
}

func TestBackgroundCallReturnsJobWhileWorkRuns(t *testing.T) {
	for _, tc := range []struct {
		name, tool, args string
		job              BackgroundJob
		// run starts the work in the background; false is a resumed call whose job already left the turn.
		run bool
	}{
		{
			name: "started in background", tool: executeToolName, run: true,
			args: `{"code":"def main():\n    return probe() + probe()\n","description":"Run tests","background":true}`,
			job:  BackgroundJob{ID: "turn/call/call-1", Kind: BackgroundKindExecute, Label: "Run tests", Agent: "main", CallID: "call-1", Detached: true},
		},
		{
			name: "moved", tool: executeToolName, run: true,
			args: `{"code":"def main():\n    return probe() + probe()\n","description":"Run tests","background":false}`,
			job:  BackgroundJob{ID: "turn/call/call-1", Kind: BackgroundKindExecute, Label: "Run tests", Agent: "main", CallID: "call-1"},
		},
		{
			name: "resumed", tool: executeToolName,
			args: `{"code":"def main():\n    return probe() + probe()\n","description":"Run tests","background":false}`,
			job:  BackgroundJob{ID: "turn/call/call-1", Kind: BackgroundKindExecute, Label: "Run tests", Agent: "main", CallID: "call-1"},
		},
		{
			name: "moved task", tool: "task",
			args: `{"subagent_type":"child","prompt":"look","description":"Review","command":"","background":false,"continue":""}`,
			job:  BackgroundJob{ID: "turn/call/call-1", Kind: BackgroundKindTask, Label: "Review", Agent: "main", SubagentKey: taskKey("turn", "call-1"), CallID: "call-1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var (
					work     errgroup.Group
					finished string
					errWork  error
					detached []bool
				)

				release := make(chan struct{})
				jobs := enabledBackgroundJobs()
				jobs.RunFunc = func(ctx context.Context, job *BackgroundJob, w BackgroundWork) (BackgroundResult, error) {
					if tc.run {
						work.Go(func() error {
							finished, errWork = w.RunBackground(context.WithoutCancel(ctx))

							return nil
						})
					}

					if !job.Detached {
						synctest.Wait() // The attached call blocks in its first probe before it moves.
					}

					return BackgroundResult{Moved: true}, nil
				}

				loop := backgroundTestLooper(t, backgroundPermissions, jobs, func(ctx context.Context) (ToolResult, error) {
					detached = append(detached, backgroundDetached(ctx))

					<-release

					return TextToolResult("probed"), nil
				})
				turnReview := []responses.ResponseInputItemUnionParam{testInputMessage(responses.EasyInputMessageRoleUser, "run the tests", "")}
				loop.permissionReviewInput = turnReview
				output := make(chan ChatResponse, 20)

				outputs, _, err := loop.dispatchToolCalls(t.Context(), responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{testFunctionCall("item", "call-1", tc.tool, tc.args)}), nil, output)
				require.NoError(t, err)

				text := fmt.Sprintf(backgroundExecuteResult, tc.job.ID)
				if tc.tool == "task" {
					text = fmt.Sprintf(taskContinueID, tc.job.SubagentKey) + fmt.Sprintf(backgroundTaskResult, tc.job.ID)
				}

				require.Equal(t, text, outputs[0].Result.Output)
				require.Len(t, jobs.RunCalls(), 1)
				require.Equal(t, tc.job, *jobs.RunCalls()[0].Job)
				require.Equal(t, PublicProgressBackground, PublicProgressFromTrace(loop.observations.trace)[0].State)

				// The turn moves on and closes; the job keeps its own review context and output sink.
				loop.permissionReviewInput = nil

				close(output)

				nested := 0

				for item := range output {
					if item.Tool != nil && item.Tool.Name == executeNestedToolPrefix+"probe" {
						nested++
					}
				}

				close(release)
				require.NoError(t, work.Wait())
				require.NoError(t, errWork)

				if !tc.run {
					require.Empty(t, detached, "a resumed background call does not rerun its work")
					return
				}

				require.Equal(t, "probedprobed", finished)
				require.Equal(t, []bool{tc.job.Detached, true}, detached, "the script sees when its job leaves the turn")
				require.Equal(t, map[bool]int{true: 0, false: 1}[tc.job.Detached], nested, "only attached work reaches the turn")

				for _, request := range reviewedRequests(loop.PermissionReviewer.(*mockPermissionReviewer)) {
					require.Equal(t, turnReview, request.ReviewContext)
					require.Equal(t, reviewKey("turn", "call-1"), request.Review, "a script's host calls, even after its turn, review under the script's call")
				}
			})
		})
	}
}

func TestBackgroundForegroundCallKeepsTurnCancellation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{name: "interrupt stops the script", cause: errTurnInterrupted},
		{name: "shutdown lets bash finish", cause: ErrShutdown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var (
					seen     error
					finished bool
				)

				release := make(chan struct{})
				jobs := enabledBackgroundJobs()
				jobs.RunFunc = InertBackgroundJobs{}.Run
				loop := backgroundTestLooper(t, backgroundPermissions, jobs, func(ctx context.Context) (ToolResult, error) {
					select {
					case <-ctx.Done():
						seen = context.Cause(ctx)

						return ToolResult{}, fmt.Errorf("probe: %w", seen)
					case <-release:
						finished = true

						return TextToolResult("probed"), nil
					}
				})

				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)

				var dispatch errgroup.Group

				dispatch.Go(func() error {
					_, _, err := loop.dispatchToolCalls(ctx, responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{testFunctionCall("item", "call-1", executeToolName, `{"code":"def main():\n    return probe()\n","description":"Build","background":false}`)}), nil, nil)
					return err
				})

				synctest.Wait()
				cancel(tc.cause)
				synctest.Wait()
				close(release)

				// Either cause stops the resumable script itself; only an interrupt reaches its host call.
				require.ErrorIs(t, dispatch.Wait(), tc.cause)
				require.Len(t, jobs.RunCalls(), 1)
				require.Equal(t, PublicProgressStopped, PublicProgressFromTrace(loop.observations.trace)[0].State)

				require.Equal(t, tc.cause == ErrShutdown, finished, "a non-resumable host call finishes through shutdown")

				if tc.cause == errTurnInterrupted {
					require.ErrorIs(t, seen, errTurnInterrupted)
				}
			})
		})
	}
}

// Both calls' progress records the subagent, so both rows open it.
func TestTaskContinueRunsSavedSubagent(t *testing.T) {
	approve := `{"approved":true,"reason":""}`
	saved := map[string][]SessionEntry{}
	mock := mockResponses(
		responseWithMessage("gate-1", approve), responseWithMessage("first", "tests started"), responseWithMessage("gate-2", approve),
		responseWithMessage("gate-3", approve), responseWithMessage("second", "all tests pass"), responseWithMessage("gate-4", approve),
	)
	jobs := enabledBackgroundJobs()
	jobs.RunFunc = InertBackgroundJobs{}.Run
	child := testAgent("child")
	child.Guardrail = "safety"
	task := continueTestTask(t, mock, jobs, saved, &child)
	key := taskKey("turn-call-1", "call-1")

	got, progress, err := task("call-1", "run the tests", "")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf(taskContinueID, key)+"<task_result>\ntests started\n</task_result>", got)
	require.Len(t, saved[key], 3, "the guardrail runs of a call save under its subagent's key")
	require.Equal(t, key, progress[0].SubagentKey)

	got, progress, err = task("call-2", "report the results", key)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf(taskContinueID, key)+"<task_result>\nall tests pass\n</task_result>", got)
	require.Equal(t, key, jobs.RunCalls()[1].Job.SubagentKey, "the registry sees which subagent runs")
	require.Equal(t, key, progress[0].SubagentKey, "a continue call's row opens the subagent it continued")

	input, err := json.Marshal(newParams(mock)[4].Input)
	require.NoError(t, err)
	require.Contains(t, string(input), "run the tests")
	require.Contains(t, string(input), "tests started")
	require.Contains(t, string(input), "report the results")
	require.NotContains(t, string(input), "Current Action", "guardrail turns are not the subagent's history")
	require.Len(t, saved[key], 6, "the continue call's guardrail runs save under the subagent it continues")
}

func TestTaskContinueRejections(t *testing.T) {
	errBusy := errors.New("subagent /call-1 is still running")

	for _, tc := range []struct {
		name, id string
		want     error
		text     string
	}{
		{name: "busy", id: "/call-1", want: errBusy},
		{name: "not a direct child", id: "/call-1/call-2", text: fmt.Sprintf(continueNotChild, "/call-1/call-2")},
		{name: "foreign key", id: "conversation-2/call-1", text: fmt.Sprintf(continueNotChild, "conversation-2/call-1")},
		{name: "another conversation's subagent", id: "/call-7", text: fmt.Sprintf(continueWithoutTurn, "/call-7")},
		{name: "another agent's subagent", id: "/call-5", text: fmt.Sprintf(continueOtherAgent, "/call-5", "reviewer")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turn := SessionEntry{Version: 1, Type: "turn", TurnID: "turn/call/call-1/task", Agent: "child", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"look"}`)}}
			other := turn
			other.Agent = "reviewer"
			saved := map[string][]SessionEntry{"/call-1": {turn}, "/call-1/call-2": {turn}, "/call-5": {other}}
			jobs := enabledBackgroundJobs()
			jobs.RunFunc = func(ctx context.Context, job *BackgroundJob, work BackgroundWork) (BackgroundResult, error) {
				if job.SubagentKey == "/call-1" {
					return BackgroundResult{}, errBusy // The registry owns the busy check.
				}

				return InertBackgroundJobs{}.Run(ctx, job, work)
			}
			mock := mockResponses()

			_, _, err := continueTestTask(t, mock, jobs, saved, new(testAgent("child")))("call-3", "more", tc.id)

			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			} else {
				require.ErrorContains(t, err, tc.text)
			}

			require.Empty(t, newParams(mock))
		})
	}
}

func TestSubagentDrainsItsOwnNotes(t *testing.T) {
	mock := mockResponses(responseWithMessage("wait", "waiting for tests"), responseWithMessage("after", "ship it"))
	jobs := enabledBackgroundJobs()
	jobs.RunFunc = InertBackgroundJobs{}.Run
	notes := []PromptInput{{ID: "turn-call-1/call/call-x", Role: PromptInputRoleUser, Text: "tests finished: 12 passed"}}
	jobs.DrainNotesFunc = func(_ context.Context, subagentKey string) []PromptInput {
		if subagentKey != taskKey("turn-call-1", "call-1") {
			return nil
		}

		drained := notes
		notes = nil

		return drained
	}

	got, _, err := continueTestTask(t, mock, jobs, map[string][]SessionEntry{}, new(testAgent("child")))("call-1", "run the tests", "")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf(taskContinueID, taskKey("turn-call-1", "call-1"))+"<task_result>\nship it\n</task_result>", got)

	input, err := json.Marshal(newParams(mock)[1].Input)
	require.NoError(t, err)
	require.Contains(t, string(input), "tests finished: 12 passed", "the note enters the subagent's running turn")
	require.NotEmpty(t, jobs.DrainNotesCalls())

	for _, call := range jobs.DrainNotesCalls() {
		require.Equal(t, taskKey("turn-call-1", "call-1"), call.SubagentKey)
	}
}

// savedChildSessions keeps child entries per key, like the store-backed Delegation History.
func savedChildSessions(saved map[string][]SessionEntry) *mockChildSessions {
	var mu sync.Mutex

	return &mockChildSessions{
		AppendChildEntryFunc: func(_ context.Context, key string, entry *SessionEntry) error {
			mu.Lock()
			defer mu.Unlock()

			saved[key] = append(saved[key], *entry)

			return nil
		},
		ChildEntriesFunc: func(_ context.Context, key string) ([]SessionEntry, error) {
			mu.Lock()
			defer mu.Unlock()

			return slices.Clone(saved[key]), nil
		},
	}
}

// continueTestTask calls the task tool of a main agent allowed to continue its subagents, in a
// turn of its own, and returns the call's output and public progress.
func continueTestTask(t *testing.T, mock *mockResponsesAPI, jobs BackgroundJobs, saved map[string][]SessionEntry, child *Agent) func(callID, prompt, continueID string) (string, []PublicProgress, error) {
	t.Helper()

	factory := testTaskFactory(mock, Agents{Items: map[string]Agent{"child": *child, "safety": testAgent("safety")}})
	factory.backgroundJobs = jobs
	factory.childSessions = savedChildSessions(saved)
	task := factory.toolsFor(&Agent{Name: "main", Permission: parsePermissionYAML(t, backgroundPermissions)})["task"]

	return func(callID, prompt, continueID string) (string, []PublicProgress, error) {
		parent := testLooper(mockResponses())
		parent.observations.turnID = "turn-" + callID
		args := fmt.Sprintf(`{"subagent_type":"child","prompt":%q,"description":"Review","command":"","background":false,"continue":%q}`, prompt, continueID)
		result, err := task.Call(withToolCallContext(t.Context(), parent, nil, callID), json.RawMessage(args), nil, toolCallMetadata{callID: callID, observations: parent.observations, progress: &PublicProgress{ID: callID}})

		return result.Output, PublicProgressFromTrace(parent.observations.trace), err
	}
}

// backgroundTestLooper builds a main agent whose execute script can call an auto-reviewed probe host tool.
func backgroundTestLooper(t *testing.T, permissions string, jobs BackgroundJobs, probe func(context.Context) (ToolResult, error)) *looper {
	t.Helper()

	probeTool := testLooperTool("probe")
	probeTool.Permission = "probe"
	probeTool.codeModeOnly = true
	probeTool.Call = func(ctx context.Context, _ json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
		return probe(ctx)
	}

	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{"child": testAgent("child")}})
	factory.baseTools = map[string]looperTool{"probe": probeTool}
	factory.backgroundJobs = jobs
	agent := Agent{Name: "main", Permission: parsePermissionYAML(t, permissions)}
	loop := testLooper(mockResponses())
	loop.agent = agent
	loop.Permissions = agent.Permission
	loop.Diagnostics = true
	loop.AutoApprovePermissions = true
	loop.PermissionReviewer = permissionReviewerWith(permissionReviewDecision{RiskLevel: permissionReviewRiskLevelLow, UserAuthorization: permissionReviewUserAuthorizationUnknown, Outcome: permissionReviewOutcomeAllow, Rationale: "ok"})
	loop.observations = &turnObservations{journal: InertJournal{}, turnID: "turn"}
	loop.Tools, loop.CodeModeHosts = factory.assembleTools(&agent)

	return loop
}

func marshalParameters(t *testing.T, parameters map[string]any) []byte {
	t.Helper()

	raw, err := json.Marshal(parameters)
	require.NoError(t, err)

	return raw
}
