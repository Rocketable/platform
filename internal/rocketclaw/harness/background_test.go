package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"testing/synctest"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

// todayExecuteParameters and todayTaskParameters are the definitions without background mode.
const (
	todayExecuteParameters = `{"additionalProperties":false,"properties":{"code":{"description":"Starlark source defining def main() that returns a string (or JSON-encodable value). Starlark, not Python. Parsed before any host tool runs; a codemode.star error means the wrapper failed and nothing ran. execute code is a JSON string — JSON still wraps it in \"...\". Inside that string, bash(command=...) takes r'''...''' only, not Starlark \"...\" or '...'. r\"...\" is raw but single-line; a real newline needs r'''...'''. Example execute argument: {\"code\":\"def main():\\n    return bash(command=r'''grep -nE 'architecture|loop' FILE''')\\n\"}. $ is valid inside a closed Starlark string, not interpolation. bash(...) is text-like: use str(result) before find/split. Failed wrapper output is not evidence; fix and rerun. Concurrency: gather([lambda: …], concurrency=16) — one list of zero-arg lambdas, not varargs.","type":"string"},"description":{"description":"A short 3-5 word label for the script, displayed to the user.","type":"string"}},"required":["code","description"],"type":"object"}`
	todayTaskParameters    = `{"additionalProperties":false,"properties":{"command":{"type":"string"},"description":{"type":"string"},"prompt":{"type":"string"},"subagent_type":{"type":"string"}},"required":["command","description","prompt","subagent_type"],"type":"object"}`
	backgroundPermissions  = `{rocketclaw: {allow_background: allow}, task: allow, read: allow, probe: auto}`
)

func enabledBackgroundJobs() *mockBackgroundJobs {
	return &mockBackgroundJobs{EnabledFunc: func() bool { return true }}
}

func TestBackgroundSchemasFollowPermission(t *testing.T) {
	for _, tc := range []struct {
		name, permissions string
		jobs              BackgroundJobs
		offered           bool
	}{
		{name: "absent", permissions: `{task: allow, read: allow}`, jobs: enabledBackgroundJobs()},
		{name: "exact deny", permissions: `{rocketclaw: {allow_background: deny}, task: allow, read: allow}`, jobs: enabledBackgroundJobs()},
		{name: "allowed without registry", permissions: backgroundPermissions, jobs: InertBackgroundJobs{}},
		{name: "allowed", permissions: backgroundPermissions, jobs: enabledBackgroundJobs(), offered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{"child": testAgent("child")}})
			factory.backgroundJobs = tc.jobs
			model, _ := factory.assembleTools(&Agent{Name: "main", Permission: parsePermissionYAML(t, tc.permissions)})

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
			require.Equal(t, executeDescriptionParam, execute.Properties["description"].Description)
			require.Equal(t, "boolean", task.Properties["background"].Type)
			require.Equal(t, backgroundTaskParam, task.Properties["background"].Description)

			var parameters struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}

			require.NoError(t, json.Unmarshal(marshalParameters(t, model["task"].Definition.Parameters), &parameters))
			require.JSONEq(t, fmt.Sprintf(`{"type":"object","additionalProperties":false,"required":["session_id","mode"],"properties":{"session_id":{"type":"string","description":%q},"mode":{"type":"string","enum":["stop","steer","enqueue"],"description":%q}}}`, backgroundContinueParam, backgroundModeParam), string(parameters.Properties["continue"]))
		})
	}
}

func TestSudoAgentSeesEveryTool(t *testing.T) {
	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{"child": testAgent("child")}})
	factory.backgroundJobs = enabledBackgroundJobs()
	result := LoadAgents(fstest.MapFS{"main.md": testMapFile("---\nmodel: gpt-5.4\nsudo: yes\n---\nPrompt\n")}, AgentLoadConfig{})
	require.Empty(t, result.Errors)

	agent := result.Agents.Items["main"]
	model, hosts := factory.assembleTools(&agent)

	require.Subset(t, slices.Collect(maps.Keys(model)), []string{"skill", "task", executeToolName, loadExecuteResultToolName})
	require.Subset(t, slices.Collect(maps.Keys(hosts)), []string{"bash", "read"})
	require.Contains(t, model[executeToolName].Definition.Parameters["properties"], "background")
	require.Contains(t, model["task"].Definition.Description.Value, "child")
	require.Contains(t, permissionPrompt(agent.Permission), "## Sudo Mode")
}

func TestBackgroundRequestsRejectedWhenNotOffered(t *testing.T) {
	executeArgs := `{"code":"def main():\n    return probe()\n","description":"","background":true}`
	taskArgs := `{"subagent_type":"missing","prompt":"look","description":"Review","command":"","background":true,"continue":""}`

	for _, tc := range []struct {
		name, permissions, tool, args string
		jobs                          BackgroundJobs
		want                          error
	}{
		{name: "denied execute", permissions: `{rocketclaw: {allow_background: deny}, task: allow, probe: auto}`, tool: executeToolName, args: executeArgs, jobs: enabledBackgroundJobs(), want: errBackgroundUnavailable},
		{name: "denied task", permissions: `{rocketclaw: {allow_background: deny}, task: allow, probe: auto}`, tool: "task", args: taskArgs, jobs: enabledBackgroundJobs(), want: errBackgroundUnavailable},
		{name: "unavailable execute", permissions: `{rocketclaw: {allow_background: allow}, task: allow, probe: auto}`, tool: executeToolName, args: executeArgs, jobs: InertBackgroundJobs{}, want: errBackgroundUnavailable},
		{name: "unavailable task", permissions: `{rocketclaw: {allow_background: allow}, task: allow, probe: auto}`, tool: "task", args: taskArgs, jobs: InertBackgroundJobs{}, want: errBackgroundUnavailable},
		{name: "denied continue", permissions: `{rocketclaw: {allow_background: deny}, task: allow, probe: auto}`, tool: "task", args: strings.Replace(strings.Replace(taskArgs, `"continue":""`, `"continue":{"session_id":"/call-0","mode":"steer"}`, 1), `"background":true`, `"background":false`, 1), jobs: enabledBackgroundJobs(), want: errBackgroundUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probed := false
			loop := backgroundTestLooper(t, tc.permissions, tc.jobs, func(context.Context) (ToolResult, error) {
				probed = true
				return TextToolResult("probed"), nil
			})
			ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: loop, observations: loop.observations, output: nil, callID: "call-1"})

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
			args: `{"subagent_type":"child","prompt":"look","description":"Review","command":"","background":false,"continue":{"session_id":"","mode":"enqueue"}}`,
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
				output := make(chan ChatResponse, 20)

				outputs, err := loop.dispatchToolCalls(t.Context(), responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{testFunctionCall("item", "call-1", tc.tool, tc.args)}), turnReview, nil, output)
				require.NoError(t, err)
				synctest.Wait() // A started script blocks in its first probe.

				text := fmt.Sprintf(backgroundExecuteResult, tc.job.ID)
				if tc.tool == "task" {
					text = fmt.Sprintf(taskContinueID, tc.job.SubagentKey) + fmt.Sprintf(backgroundTaskResult, tc.job.ID)
				}

				require.Equal(t, text, outputs[0].Param.Output.OfString.Value)
				require.Len(t, jobs.RunCalls(), 1)
				require.Equal(t, tc.job, *jobs.RunCalls()[0].Job)
				// A task's row names its subagent even when the call moves before the subagent reports.
				traced := PublicProgressFromTrace(loop.observations.trace)[0]
				require.Equal(t, PublicProgressBackground, traced.State)
				require.Equal(t, tc.job.SubagentKey, traced.SubagentKey)

				// The turn moves on and closes; the job keeps its own review context and output sink.
				loop.observations.close()
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
					require.Empty(t, loop.observations.calls)

					return
				}

				// The probe running at closure keeps its status; the script's later probe is not recorded.
				require.Equal(t, []CodeModeCall{{Type: codeModeCallTraceType, ParentCallID: "call-1", Key: "turn/call/call-1/host/0", Name: "probe", Arguments: "{}", Status: codeModeCallRunning}}, loop.observations.calls)

				require.Equal(t, "probedprobed", finished)
				require.Equal(t, []bool{tc.job.Detached, true}, detached, "the script sees when its job leaves the turn")
				require.Equal(t, map[bool]int{true: 0, false: 1}[tc.job.Detached], nested, "only attached work reaches the turn")

				turns := []string{}

				for _, request := range reviewedRequests(loop.PermissionReviewer.(*mockPermissionReviewer)) {
					require.Equal(t, turnReview, request.ReviewContext)
					require.Equal(t, ChildKey{Path: "/" + reviewKey("turn", "call-1"), Turn: "turn", Call: "call-1"}, request.Review, "a script's host calls, even after its turn, review under the script's call")
					turns = append(turns, request.Turn)
				}

				require.Contains(t, turns, ReviewTurn(loop.observations.calls[0].Key), "a host call's review turn comes from the key its record keeps")
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
					_, err := loop.dispatchToolCalls(ctx, responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{testFunctionCall("item", "call-1", executeToolName, `{"code":"def main():\n    return probe()\n","description":"Build","background":false}`)}), nil, nil, nil)
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

				record := CodeModeCall{Type: codeModeCallTraceType, ParentCallID: "call-1", Key: "turn/call/call-1/host/0", Name: "probe", Arguments: "{}", Status: codeModeCallOK, Summary: "probed", Outcome: permissionReviewOutcomeAllow, Reason: "ok"}

				if tc.cause == errTurnInterrupted {
					require.ErrorIs(t, seen, errTurnInterrupted)

					// A cancelled call does not keep showing as running.
					record.Status, record.Summary = codeModeCallError, "probe: "+errTurnInterrupted.Error()
				}

				require.Equal(t, []CodeModeCall{record}, loop.observations.calls)
			})
		})
	}
}

// A task call writes its prompt into its subagent's session and answers with the turn that runs it. A
// continue naming another type switches the subagent to it, as OpenCode packages/core/src/tool/plugin/subagent.ts
// does.
func TestTaskWritesSubagentSession(t *testing.T) {
	mock := mockResponses()
	jobs := enabledBackgroundJobs()
	jobs.RunFunc = InertBackgroundJobs{}.Run
	sessions := answeringSessions(map[string][]SessionEntry{}, false)
	task := continueTestTask(t, mock, jobs, sessions, new(testAgent("child")))
	key := taskKey("turn-call-1", "call-1")

	got, progress, err := task("call-1", "child", "run the tests", "")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf(taskContinueID, key)+"<task_result>\nrun the tests: done\n</task_result>", got)
	require.Equal(t, key, progress[0].SubagentKey)

	got, progress, err = task("call-2", "reviewer", "report the results", key)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf(taskContinueID, key)+"<task_result>\nreport the results: done\n</task_result>", got)
	require.Equal(t, key, jobs.RunCalls()[1].Job.SubagentKey, "the registry sees which subagent runs")
	require.Equal(t, []string{key, "reviewer", "openai/gpt-5.4"}, []string{progress[0].SubagentKey, progress[0].Agent, progress[0].Model}, "a continue call's row opens the subagent it continued")

	writes := sessions.WriteSubagentCalls()
	require.Equal(t, SubagentWrite{Key: ChildKey{Path: key, Turn: "turn-call-1", Call: "call-1"}, CallKey: "turn-call-1/call/call-1", Agent: "child", Delegator: "main", Prompt: "run the tests"}, *writes[0].Write)
	require.Equal(t, SubagentWrite{Key: ChildKey{Path: key, Turn: "turn-call-2", Call: "call-2"}, CallKey: "turn-call-2/call/call-2", Agent: "reviewer", Delegator: "main", Prompt: "report the results", Mode: ContinueEnqueue}, *writes[1].Write)
	require.Equal(t, "turn-call-2/call/call-2", sessions.AwaitSubagentCalls()[1].CallKey)
}

func TestTaskContinueRejections(t *testing.T) {
	for _, tc := range []struct {
		name, id, subagentType, text string
	}{
		{name: "not a direct child", id: "/call-1/call-2", subagentType: "child", text: fmt.Sprintf(continueNotChild, "/call-1/call-2")},
		{name: "foreign key", id: "conversation-2/call-1", subagentType: "child", text: fmt.Sprintf(continueNotChild, "conversation-2/call-1")},
		{name: "unknown type", id: "/call-1", subagentType: "missing", text: fmt.Sprintf(unknownAgentType, "missing")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := answeringSessions(map[string][]SessionEntry{}, false)
			mock := mockResponses()

			_, _, err := continueTestTask(t, mock, enabledBackgroundJobs(), sessions, new(testAgent("child")))("call-3", tc.subagentType, "more", tc.id)
			require.ErrorContains(t, err, tc.text)
			require.Empty(t, sessions.WriteSubagentCalls())
			require.Empty(t, newParams(mock))
		})
	}
}

// A steer that joins the subagent's running turn returns at once, without a job or a wait.
func TestTaskContinueSteerJoinsRunningTurn(t *testing.T) {
	jobs, sessions := enabledBackgroundJobs(), answeringSessions(map[string][]SessionEntry{}, true)

	got, _, err := continueTestTask(t, mockResponses(), jobs, sessions, new(testAgent("child")))("call-3", "child", "more", "/call-1")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf(taskContinueID, "/call-1")+continueSteered, got)
	require.Empty(t, jobs.RunCalls())
	require.Empty(t, sessions.AwaitSubagentCalls())
}

// A continue needs the task permission for the type it names, as starting a subagent of that type does.
func TestTaskContinueNeedsTaskPermission(t *testing.T) {
	jobs := enabledBackgroundJobs()
	loop := backgroundTestLooper(t, `{rocketclaw: {allow_background: allow}, task: {reviewer: allow}}`, jobs, nil)
	args := `{"subagent_type":"child","prompt":"more","description":"Review","command":"","background":false,"continue":{"session_id":"/call-1","mode":"steer"}}`

	outputs, err := loop.dispatchToolCalls(t.Context(), responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{testFunctionCall("item", "call-3", "task", args)}), nil, nil, make(chan ChatResponse, 20))
	require.NoError(t, err)
	require.Equal(t, `tool call denied: permission "task" has no matching allow rule for subject "child". Choose a different action.`, outputs[0].Param.Output.OfString.Value)
	require.Empty(t, jobs.RunCalls())
}

// A steer naming another type switches the running session to it from the turn's next step, as OpenCode
// packages/core/src/session/context.ts selects the session's agent at every step: the step already running
// keeps its type's permissions, and later steps and a resumed turn run as the new type. A steer naming the
// running type changes nothing.
func TestSteerSwitchesRunningSessionType(t *testing.T) {
	for _, tc := range []struct {
		name, steer, prompt, model string
		restart, probes            bool
	}{
		{name: "switch", steer: "reviewer", prompt: "REVIEWER PROMPT", model: "gpt-6.1-sol"},
		{name: "switch survives restart", steer: "reviewer", prompt: "REVIEWER PROMPT", model: "gpt-6.1-sol", restart: true},
		{name: "same type", steer: "child", prompt: "CHILD PROMPT", model: "gpt-5.4", probes: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })

			agents := LoadAgents(fstest.MapFS{
				"child.md":    {Data: []byte("---\nmodel: gpt-5.4\npermission: {probe: allow}\n---\nCHILD PROMPT")},
				"reviewer.md": {Data: []byte("---\nmodel: gpt-6.1-sol\npermission: {probe: deny}\n---\nREVIEWER PROMPT")},
			}, AgentLoadConfig{})
			require.Empty(t, agents.Errors)

			steers := []PromptInput{{ID: "steer-1", Role: PromptInputRoleUser, Texts: []string{"review it instead"}, Agent: tc.steer}}
			drain := &mockSteerDrain{DrainFunc: func(context.Context, string, TurnPhase, []string) ([]PromptInput, error) {
				drained := steers
				steers = nil

				return drained, nil
			}}

			var probedBy []string

			probe := testLooperTool("probe")
			probe.Permission = "probe"
			probe.Call = func(ctx context.Context, _ json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				agent, _ := ToolCallAgent(ctx)
				probedBy = append(probedBy, agent.Name)

				return TextToolResult("probed"), nil
			}

			var saved []SessionEntry

			journal := recordingJournal()
			run := func(mock *mockResponsesAPI) (string, error) {
				config := testConfig(dir)
				config.Journal = journal
				loop, err := NewWithModelResolver(testResolverForResponsesAPI(mock), config, root, agents.Agents, Skills{Items: map[string]Skill{}}, "child", nil)
				require.NoError(t, err)

				loop.PermissionReviewer.(*toolFactory).baseTools["probe"] = probe
				loop.Tools["probe"], loop.SteerDrain = probe, drain

				return loop.runPrompt(t.Context(), "turn-1", []string{"probe it"}, func(func(SessionEntry, error) bool) {}, &mockSessionWriter{AppendFunc: func(_ context.Context, entry *SessionEntry) error { saved = append(saved, *entry); return nil }}, inertChatObserver{})
			}
			call := responseWithFunctionCalls("probe", []responses.ResponseFunctionToolCall{testFunctionCall("item", "call-p", "probe", `{}`)})
			crashed, mock := mockResponses(call), mockResponses(call, responseWithMessage("answer", "reviewed"))

			if tc.restart {
				_, err := run(crashed)
				require.ErrorContains(t, err, "no mock response configured", "the process stops before the switched step answers")

				mock = mockResponses(responseWithMessage("answer", "reviewed"))
			}

			got, err := run(mock)
			require.NoError(t, err)
			require.Equal(t, "reviewed", got)

			// The crashed process sent the switched step too, which failed.
			requests := append(newParams(crashed), newParams(mock)...)
			probes := func(params *responses.ResponseNewParams) bool {
				return slices.ContainsFunc(params.Tools, func(tool responses.ToolUnionParam) bool {
					return tool.OfFunction != nil && tool.OfFunction.Name == "probe"
				})
			}

			require.Contains(t, requests[0].Instructions.Value, "CHILD PROMPT")
			require.Equal(t, "gpt-5.4", requests[0].Model)
			require.True(t, probes(&requests[0]))
			require.Equal(t, []string{"child"}, probedBy, "the step already running keeps its type")
			require.Contains(t, marshalJSON(t, requests[1].Input), "review it instead")

			for _, request := range requests[1:] {
				require.Contains(t, request.Instructions.Value, tc.prompt)
				require.Equal(t, tc.model, request.Model)
				require.Equal(t, tc.probes, probes(&request))
			}

			require.Len(t, saved, 1)
			require.Equal(t, tc.steer, saved[0].Agent, "the turn ends as the type its steer named")
		})
	}
}

// The parent's row names the type a steer switched the subagent to.
func TestSteerSwitchedSubagentRowNamesFinalType(t *testing.T) {
	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{"child": testAgent("child"), "reviewer": testAgent("reviewer")}})
	factory.childSessions = &mockChildSessions{
		WriteSubagentFunc: func(context.Context, *SubagentWrite) (bool, error) { return false, nil },
		AwaitSubagentFunc: func(context.Context, string, string) (string, ReplayAttribution, error) {
			return "checked", ReplayAttribution{Agent: "reviewer", Model: "openai/gpt-6.1-sol"}, nil
		},
	}

	parent := testLooper(mockResponses())
	got, err := factory.runTask(context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: parent, observations: parent.observations, output: nil, callID: "call-1"}), testTaskParams("Review", "check it", "child"), toolCallMetadata{callID: "call-1", observations: parent.observations, progress: &PublicProgress{ID: "call-1"}})
	require.NoError(t, err)
	require.Equal(t, "<task_result>\nchecked\n</task_result>", got)

	row := PublicProgressFromTrace(parent.observations.trace)[0]
	require.Equal(t, []string{"reviewer", "openai/gpt-6.1-sol"}, []string{row.Agent, row.Model})
}

// A steer switching a turn to a type on another provider converts the turn's items, and the saved history,
// as loadSession converts saved turns. The items the new provider adds name its model, so a steer switching
// back converts them in turn.
func TestSteerSwitchConvertsTurnForProvider(t *testing.T) {
	child, worker := testAgent("child"), testAgent("worker")
	child.Permission, worker.Permission, worker.Model = parsePermissionYAML(t, "{probe: allow}"), parsePermissionYAML(t, "{probe: allow}"), "work/model-b"
	step := func(id, secret string) *responses.Response {
		probe := responseWithFunctionCalls(id, []responses.ResponseFunctionToolCall{testFunctionCall(id+"-call", "call-"+id, "probe", `{}`)}).Output[0]
		return testResponse(id, []responses.ResponseOutputItemUnion{testReasoningOutputItem(id+"-reasoning", secret, id+" thought"), probe})
	}
	mock := mockResponses(step("first", "openai-secret"), step("switched", "work-secret"), responseWithMessage("back", "done"))
	factory := testTaskFactory(mock, Agents{Items: map[string]Agent{"child": child, "worker": worker}})
	resolve := testResolverForResponsesAPI(mock)
	factory.resolver = testModelResolverFunc(func(model string) (*openai.Client, ProviderOrigin, error) {
		api, work := strings.CutPrefix(model, "work/")

		client, origin, err := resolve(api)
		if work {
			origin.Provider = "work"
		}

		return client, origin, err
	})
	probe := testLooperTool("probe")
	probe.Permission = "probe"
	probe.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
		return TextToolResult("probed"), nil
	}
	factory.baseTools["probe"] = probe

	steers := [][]PromptInput{{{ID: "steer-1", Role: PromptInputRoleUser, Texts: []string{"work on it"}, Agent: "worker"}}, {{ID: "steer-2", Role: PromptInputRoleUser, Texts: []string{"back to you"}, Agent: "child"}}}
	history := SessionEntry{Version: 1, Type: "turn", TurnID: "turn-0", Agent: "child", Model: "openai/gpt-5.4", ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":"look"}`),
		json.RawMessage(`{"type":"reasoning","encrypted_content":"saved-secret","summary":[{"type":"summary_text","text":"saved thought"}]}`),
	}}

	loop, err := factory.newLooper(&child, "")
	require.NoError(t, err)

	loop.SteerDrain = &mockSteerDrain{DrainFunc: func(context.Context, string, TurnPhase, []string) ([]PromptInput, error) {
		if len(steers) == 0 {
			return nil, nil
		}

		drained := steers[0]
		steers = steers[1:]

		return drained, nil
	}}

	var saved []SessionEntry

	got, err := loop.runPrompt(t.Context(), "", []string{"probe it"}, func(yield func(SessionEntry, error) bool) { yield(history, nil) }, &mockSessionWriter{AppendFunc: func(_ context.Context, entry *SessionEntry) error { saved = append(saved, *entry); return nil }}, inertChatObserver{})
	require.NoError(t, err)
	require.Equal(t, "done", got)

	requests := newParams(mock)
	require.Len(t, requests, 3)
	require.Contains(t, marshalJSON(t, requests[0].Input), "saved-secret")
	require.Equal(t, "model-b", requests[1].Model)

	switched, back := marshalJSON(t, requests[1].Input), marshalJSON(t, requests[2].Input)
	for _, secret := range []string{"saved-secret", "openai-secret"} {
		require.NotContains(t, switched, secret)
	}

	require.Contains(t, switched, "saved thought")
	require.Contains(t, switched, "first thought")
	require.NotContains(t, back, "work-secret")
	require.Contains(t, back, "switched thought")

	entry := saved[0]
	thought := slices.IndexFunc(entry.ReplayInput, func(raw json.RawMessage) bool { return strings.Contains(string(raw), "switched thought") })
	require.Equal(t, "work/model-b", entry.AttributionAt(thought).Model, "the switched type's items name its model")
	require.Equal(t, []string{"child", "openai/gpt-5.4"}, []string{entry.Agent, entry.Model})
}

// answeringSessions is a ChildSessions that keeps child entries per key in saved, like the store, and
// whose subagent sessions answer each write's prompt as the type the write named; with steered every
// write joins a running turn instead.
func answeringSessions(saved map[string][]SessionEntry, steered bool) *mockChildSessions {
	var (
		mu     sync.Mutex
		writes = map[string]SubagentWrite{}
	)

	return &mockChildSessions{
		AppendChildEntryFunc: func(_ context.Context, key ChildKey, entry *SessionEntry) error {
			mu.Lock()
			defer mu.Unlock()

			saved[key.Path] = append(saved[key.Path], *entry)

			return nil
		},
		WriteSubagentFunc: func(_ context.Context, write *SubagentWrite) (bool, error) {
			mu.Lock()
			defer mu.Unlock()

			writes[write.Key.Path] = *write

			return steered, nil
		},
		AwaitSubagentFunc: func(_ context.Context, key, _ string) (string, ReplayAttribution, error) {
			mu.Lock()
			defer mu.Unlock()

			return writes[key].Prompt + ": done", ReplayAttribution{Agent: writes[key].Agent, Model: "openai/gpt-5.4"}, nil
		},
	}
}

// continueTestTask calls the task tool of a main agent allowed to continue its subagents, in a
// turn of its own, and returns the call's output and public progress.
func continueTestTask(t *testing.T, mock *mockResponsesAPI, jobs BackgroundJobs, sessions ChildSessions, child *Agent) func(callID, subagentType, prompt, continueID string) (string, []PublicProgress, error) {
	t.Helper()

	factory := testTaskFactory(mock, Agents{Items: map[string]Agent{"child": *child, "reviewer": testAgent("reviewer")}})
	factory.backgroundJobs = jobs
	factory.childSessions = sessions
	tools, _ := factory.assembleTools(&Agent{Name: "main", Permission: parsePermissionYAML(t, backgroundPermissions)})
	task := tools["task"]

	return func(callID, subagentType, prompt, continueID string) (string, []PublicProgress, error) {
		parent := testLooper(mockResponses())
		parent.observations.turnID = "turn-" + callID
		args := fmt.Sprintf(`{"subagent_type":%q,"prompt":%q,"description":"Review","command":"","background":false,"continue":{"session_id":%q,"mode":"enqueue"}}`, subagentType, prompt, continueID)
		result, err := task.Call(context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: parent, observations: parent.observations, output: nil, callID: callID}), json.RawMessage(args), nil, toolCallMetadata{callID: callID, observations: parent.observations, progress: &PublicProgress{ID: callID}})

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
