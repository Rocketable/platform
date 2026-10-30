package harness

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

func TestTaskPublicLifecycleKeepsChildContentPrivate(t *testing.T) {
	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{"child": testAgent("child")}})
	factory.childSessions = &mockChildSessions{
		WriteSubagentFunc: func(context.Context, *SubagentWrite) (bool, error) { return false, nil },
		AwaitSubagentFunc: func(context.Context, string, string) (string, ReplayAttribution, error) {
			return "CHILD_PRIVATE_SENTINEL", ReplayAttribution{Agent: "child", Model: "canonical-provider/canonical-model"}, nil
		},
	}
	sink := recordingJournal()
	l := emptyTestLooper()
	l.Journal = sink
	l.observations = &turnObservations{journal: sink, turnID: "test-turn"}
	l.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}
	l.Tools = map[string]looperTool{"task": factory.taskTool()}

	outputs, _, err := l.dispatchToolCalls(t.Context(), responseWithFunctionCalls("parent-response", []responses.ResponseFunctionToolCall{
		testFunctionCall("item-A", "A", "task", `{"subagent_type":"child","prompt":"do it","description":"PRIVATE_DESCRIPTION"}`),
		testFunctionCall("item-B", "B", "task", `{"subagent_type":"child","prompt":"do it","description":"PRIVATE_DESCRIPTION"}`),
	}), nil, nil)
	require.NoError(t, err)
	require.Len(t, outputs, 2)
	require.Contains(t, outputs[0].Result.Output, "CHILD_PRIVATE_SENTINEL")

	writes := sink.SaveTraceCalls()
	progress := PublicProgressFromTrace(writes[len(writes)-1].Trace)
	require.Len(t, progress, 2)

	for i, item := range progress {
		require.Equal(t, []string{"A", "B"}[i], item.ID)
		require.Equal(t, PublicProgressDelegation, item.Kind)
		require.Equal(t, PublicProgressCompleted, item.State)
		require.Equal(t, "child", item.Agent)
		require.Equal(t, "canonical-provider/canonical-model", item.Model)
	}

	for _, write := range writes {
		raw := marshalJSON(t, write.Trace)
		for _, sentinel := range []string{"CHILD_PRIVATE_SENTINEL", "PRIVATE_DESCRIPTION"} {
			require.NotContains(t, raw, sentinel)
		}
	}
}

func TestTaskPublicLifecycleFailureAndCancellation(t *testing.T) {
	for _, outcome := range []string{"failed", "stopped", "persistence"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := make(chan struct{})
				release := make(chan struct{})
				joined := false

				var start sync.Once

				wait := func(ctx context.Context) error {
					start.Do(func() { close(started) })

					select {
					case <-release:
						joined = true
						return errors.New("CHILD_FAILURE_PRIVATE_SENTINEL")
					case <-ctx.Done():
						joined = true
						return ctx.Err()
					}
				}
				factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{"child": testAgent("child")}})
				factory.childSessions = &mockChildSessions{
					WriteSubagentFunc: func(context.Context, *SubagentWrite) (bool, error) { return false, nil },
					AwaitSubagentFunc: func(ctx context.Context, _, _ string) (string, ReplayAttribution, error) {
						return "", ReplayAttribution{}, wait(ctx)
					},
				}

				sink := recordingJournal()
				l := emptyTestLooper()
				l.observations = &turnObservations{journal: sink, turnID: "parent-turn"}
				l.Permissions = PermissionSet{Buckets: []PermissionBucket{
					{Name: "task", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}},
					{Name: "fast", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}},
				}}
				l.Tools = map[string]looperTool{"task": factory.taskTool(), "fast": {Call: func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
					<-started
					return TextToolResult("ok"), nil
				}}}
				errDisk := errors.New("disk sentinel")

				if outcome == "persistence" {
					sink.SaveTraceFunc = func(_ context.Context, _ string, trace []json.RawMessage) error {
						for _, item := range PublicProgressFromTrace(trace) {
							if item.ID == "fast" && item.State == PublicProgressCompleted {
								return errDisk
							}
						}

						return nil
					}
				}

				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()

				var (
					outputs []dispatchedToolOutput
					err     error
				)

				done := make(chan struct{})
				go func() {
					defer close(done)

					outputs, _, err = l.dispatchToolCalls(ctx, responseWithFunctionCalls("response", []responses.ResponseFunctionToolCall{
						testFunctionCall("child-item", "child-call", "task", `{"subagent_type":"child","prompt":"PRIVATE_PROMPT"}`),
						testFunctionCall("fast-item", "fast", "fast", `{}`),
					}), nil, nil)
				}()

				synctest.Wait()

				switch outcome {
				case "failed":
					close(release)
				case "stopped":
					cancel()
				}

				<-done
				synctest.Wait()
				require.True(t, joined, "dispatch must join the child before returning")

				if outcome == "persistence" {
					require.ErrorIs(t, err, errDisk)
					require.Empty(t, outputs, "local persistence errors must not become tool results")
				} else {
					progress := PublicProgressFromTrace(l.observations.trace)
					require.Len(t, progress, 2)

					state := PublicProgressFailed
					if outcome == "stopped" {
						state = PublicProgressStopped
					}

					require.Equal(t, state, progress[0].State)
					require.Equal(t, PublicProgressDelegation, progress[0].Kind)
					require.Equal(t, "child", progress[0].Agent)

					if outcome == "stopped" {
						require.ErrorIs(t, err, context.Canceled)
					} else {
						require.NoError(t, err)
						require.Contains(t, outputs[0].Result.Output, "CHILD_FAILURE_PRIVATE_SENTINEL")
					}
				}

				for _, write := range sink.SaveTraceCalls() {
					require.NotContains(t, marshalJSON(t, write.Trace), "PRIVATE")
				}
			})
		})
	}
}

func TestTaskTool(t *testing.T) {
	t.Run("rejects unknown subagent", func(t *testing.T) {
		factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{}})

		_, err := factory.runTask(context.Background(), testTaskParams("", "", "missing"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1, observations: &turnObservations{journal: InertJournal{}}, progress: &PublicProgress{}})

		require.EqualError(t, err, "unknown agent type: missing is not a valid agent type")
	})

	t.Run("rejects delegation when recursion budget is exhausted", func(t *testing.T) {
		remaining := 0
		factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{
			"review": testAgent("review"),
		}})
		factory.recursionRemaining = &remaining

		_, err := factory.runTask(context.Background(), testTaskParams("Review", "check this", "review"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1, observations: &turnObservations{journal: InertJournal{}}, progress: &PublicProgress{}})

		require.EqualError(t, err, "maxRecursion limit reached: task delegation is unavailable")
	})
}

func TestTaskToolPermissionDefaults(t *testing.T) {
	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{}})

	t.Run("startup agent denies tools by default", func(t *testing.T) {
		tools, _ := factory.assembleTools(nil)

		require.Empty(t, tools)
	})

	t.Run("tasked subagent denies tools by default", func(t *testing.T) {
		agent := testAgentWithPermission(PermissionSet{Buckets: nil})
		agent.Name = "plain"

		tools, _ := factory.assembleTools(agent)

		require.Empty(t, tools)
	})

	t.Run("tasked subagent can allow individual tools", func(t *testing.T) {
		agent := testAgentWithPermission(permissionSetForActions(map[string]PermissionAction{"read": permissionAllow, "task": permissionAllow}))
		agent.Name = "reader"

		tools, hosts := factory.assembleTools(agent)

		require.Contains(t, tools, "execute")
		require.Contains(t, tools, "task")
		require.NotContains(t, tools, "read")
		require.NotContains(t, tools, "bash")
		require.Contains(t, hosts, "read")
	})

	t.Run("recursion budget hides task", func(t *testing.T) {
		remaining := 0
		factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{}})
		factory.recursionRemaining = &remaining
		agent := testAgentWithPermission(permissionSetForActions(map[string]PermissionAction{"task": permissionAllow}))
		agent.Name = "main"

		tools, _ := factory.assembleTools(agent)

		require.NotContains(t, tools, "task")
	})

	t.Run("startup agent can deny individual tools", func(t *testing.T) {
		agent := testAgentWithPermission(permissionSetForActions(map[string]PermissionAction{"bash": permissionDeny}))
		agent.Name = "main"

		tools, _ := factory.assembleTools(agent)

		require.NotContains(t, tools, "bash")
		require.NotContains(t, tools, "read")
	})

	t.Run("startup agent exposes execute for edit allow", func(t *testing.T) {
		agent := testAgentWithPermission(permissionSetForActions(map[string]PermissionAction{"edit": permissionAllow}))
		agent.Name = "main"

		tools, hosts := factory.assembleTools(agent)

		require.Contains(t, tools, "execute")
		require.NotContains(t, tools, "read")
		require.NotContains(t, tools, "bash")
		// edit allow makes read permission actionable (inheritance).
		require.Contains(t, hosts, "read")
	})

	t.Run("startup agent can allow hosted websearch", func(t *testing.T) {
		factory.baseTools["websearch"] = webSearchTool()
		agent := testAgentWithPermission(permissionSetForActions(map[string]PermissionAction{"websearch": permissionAllow}))
		agent.Name = "main"

		tools, _ := factory.assembleTools(agent)

		require.Contains(t, tools, "websearch")
		require.Equal(t, "web_search", *tools["websearch"].Hosted.GetType())
	})

	t.Run("specific allow keeps tool visible after wildcard deny", func(t *testing.T) {
		agent := testAgentWithPermission(PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{
			{Pattern: "*", Action: permissionDeny},
			{Pattern: "reviewer", Action: permissionAllow},
		}}}})
		agent.Name = "main"

		tools, _ := factory.assembleTools(agent)

		require.Contains(t, tools, "task")
	})

	t.Run("specific skill allow keeps skill tool visible", func(t *testing.T) {
		agent := testAgentWithPermission(PermissionSet{Buckets: []PermissionBucket{{Name: "skill", Rules: []PermissionRule{
			{Pattern: "*", Action: permissionDeny},
			{Pattern: "docs-helper", Action: permissionAllow},
		}}}})
		agent.Name = "main"

		tools, _ := factory.assembleTools(agent)

		require.Contains(t, tools, "skill")
	})
}

func TestBashPermissionGrantsSessionShellTempReadAndGlob(t *testing.T) {
	shellTemp := shellTempConfig{tmpRelDir: ".rocketclaw/.rocketcode/tmp/session-a", tmpDir: ""}
	agent := testAgentWithPermission(permissionSetForActions(map[string]PermissionAction{"bash": permissionAllow}))
	permissions := shellTemp.effectivePermissions(agent.Permission)
	loop := emptyTestLooper()
	loop.Permissions = permissions

	readTool := testPermissionReadTool(func(raw json.RawMessage) ([]string, error) {
		var params readToolParams
		if err := decodeToolParams(raw, &params); err != nil {
			return nil, err
		}

		return []string{rootedPathSubject(cmp.Or(params.FilePath, params.Filename))}, nil
	})
	globTool := testPermissionReadTool(func(raw json.RawMessage) ([]string, error) {
		var params globToolParams
		if err := decodeToolParams(raw, &params); err != nil {
			return nil, err
		}

		path := params.Path
		if path == "" {
			path = "."
		}

		return []string{rootedPathSubject(path)}, nil
	})
	globTool.Permission = "glob"

	decision, err := loop.permissionDecision("read", &readTool, json.RawMessage(`{"filePath":".rocketclaw/.rocketcode/tmp/session-a/out.txt"}`))
	require.NoError(t, err)
	require.False(t, decision.denied)

	decision, err = loop.permissionDecision("glob", &globTool, json.RawMessage(`{"pattern":"*","path":".rocketclaw/.rocketcode/tmp/session-a"}`))
	require.NoError(t, err)
	require.False(t, decision.denied)

	decision, err = loop.permissionDecision("read", &readTool, json.RawMessage(`{"filePath":".rocketclaw/.rocketcode/tmp/session-b/out.txt"}`))
	require.NoError(t, err)
	require.True(t, decision.denied)

	decision, err = loop.permissionDecision("glob", &globTool, json.RawMessage(`{"pattern":"*","path":".rocketclaw/.rocketcode/tmp/session-b"}`))
	require.NoError(t, err)
	require.True(t, decision.denied)
}

func TestTaskToolDescriptionFiltersDeniedSubagents(t *testing.T) {
	agents := Agents{Items: map[string]Agent{
		"builder":  testAgentWithDescription("builder", "Build things"),
		"helper":   testAgentWithDescription("helper", "Help everywhere"),
		"reviewer": testAgentWithDescription("reviewer", "Review changes"),
		"main":     testAgentWithDescription("main", "Default agent"),
	}}

	t.Run("no active agent lists no subagents", func(t *testing.T) {
		factory := testTaskFactory(mockResponses(), agents)

		description := factory.taskDescription()

		require.Contains(t, description, "No agents are currently available.")
		require.NotContains(t, description, "- builder: Build things")
		require.NotContains(t, description, "- helper: Help everywhere")
		require.NotContains(t, description, "- reviewer: Review changes")
		require.NotContains(t, description, "Default agent")
	})

	t.Run("active agent hides denied subagents", func(t *testing.T) {
		factory := testTaskFactory(mockResponses(), agents)
		factory.agent = testAgentWithPermission(PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{
			{Pattern: "*", Action: permissionDeny},
			{Pattern: "reviewer", Action: permissionAllow},
		}}}})
		factory.agent.Name = "main"

		description := factory.taskDescription()

		require.NotContains(t, description, "- builder: Build things")
		require.NotContains(t, description, "- helper: Help everywhere")
		require.Contains(t, description, "- reviewer: Review changes")
	})

	t.Run("active agent can allow default agent as subagent", func(t *testing.T) {
		factory := testTaskFactory(mockResponses(), agents)
		factory.agent = testAgentWithPermission(PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "main", Action: permissionAllow}}}}})
		factory.agent.Name = "main"

		description := factory.taskDescription()

		require.Contains(t, description, "- main: Default agent")
	})
}

func TestTaskToolDescriptionUsesOpenCodeGuidance(t *testing.T) {
	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{}})

	description := factory.taskDescription()

	require.Contains(t, description, "Launch a new agent to handle complex, multistep tasks autonomously.")
	require.Contains(t, description, "When NOT to use the Task tool:")
	require.Contains(t, description, "Launch multiple agents concurrently whenever possible")
}

func TestLooperRunsTaskToolCall(t *testing.T) {
	mock := mockResponses(
		responseWithFunctionCalls("parent-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "task", `{"description":"Review","prompt":"look","subagent_type":"review"}`)}),
		responseWithMessage("parent-final", "parent done"),
	)
	factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
		"review": testAgent("review"),
	}})
	factory.childSessions = answeringSessions(map[string][]SessionEntry{}, false)
	looper := testLooper(mock)
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "review", Action: permissionAllow}}}}}
	looper.Tools = map[string]looperTool{"task": factory.taskTool()}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "start", output)

	close(input)

	interrupts := make(chan os.Signal, 1)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession(), interrupts)

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("parent done")}, collectResponses(output))
	require.Len(t, newParams(mock), 2)
	encoded := marshalJSON(t, newParams(mock)[1].Input.OfInputItemList)
	require.Contains(t, encoded, "look: done")
	require.Contains(t, encoded, `\u003ctask_result\u003e`)
}

func TestChildRunsAppendUnderToolCallKeys(t *testing.T) {
	allow := `{"risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"ok"}`
	mock := mockResponses(
		responseWithFunctionCalls("root", []responses.ResponseFunctionToolCall{
			testFunctionCall("tool-1", "call-probe", "probe", `{}`),
			testFunctionCall("tool-2", "call-task", "task", `{"description":"Review","prompt":"look","subagent_type":"review"}`),
		}),
		responseWithMessage("probe-review", allow),
		responseWithMessage("probe-nested-review", allow),
		responseWithMessage("root-final", "done"),
	)
	auto := PermissionBucket{Name: "probe", Rules: []PermissionRule{{Pattern: "*", Action: permissionAuto}}}
	factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
		"review": {Name: "review", Model: "gpt-5.4", Permission: PermissionSet{Buckets: []PermissionBucket{auto}}},
	}})
	sessions := answeringSessions(map[string][]SessionEntry{}, false)
	factory.childSessions = sessions
	factory.autoApprovePermissions = true
	probe := testLooperTool("probe")
	probe.Permission = "probe"
	probe.Subjects = func(json.RawMessage) ([]string, error) { return []string{"top"}, nil }
	probe.Call = func(ctx context.Context, _ json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
		return TextToolResult("probed"), CheckNestedPermission(ctx, "probe", "probe", "nested", map[string]any{})
	}
	factory.baseTools["probe"] = probe
	looper := testLooper(mock)
	looper.agent = Agent{Name: "main"}
	looper.AutoApprovePermissions = true
	looper.PermissionReviewer = factory
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{auto, {Name: "task", Rules: []PermissionRule{{Pattern: "review", Action: permissionAllow}}}}}
	looper.Tools = map[string]looperTool{"task": factory.taskTool(), "probe": probe}
	looper.ParallelToolCalls = 1
	output := make(chan ChatResponse, 20)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "start", output)

	close(input)

	require.NoError(t, looper.Loop(context.Background(), input, emptySession(), discardSession(), make(chan os.Signal, 1)))
	require.Equal(t, []ChatResponse{assistantMessage("done")}, collectResponses(output))

	calls := sessions.AppendChildEntryCalls()
	got := make([]string, 0, len(calls))

	for _, call := range calls {
		got = append(got, call.Key.Path+" "+call.Entry.Agent)
	}

	review, task := calls[0].Key, sessions.WriteSubagentCalls()[0].Write.Key
	require.Equal(t, [][3]string{{"", "call-probe", review.Turn}, {"", "call-task", review.Turn}}, [][3]string{
		{review.Parent, review.Call, review.Turn}, {task.Parent, task.Call, task.Turn},
	}, "a review and a subagent record the call of the turn that started them")
	require.NotEmpty(t, review.Turn)
	require.Regexp(t, `^/call-probe-review-[0-9a-f]{8}$`, review.Path, "a review's key adds its reviewed call key's hash to its call ID")
	require.Regexp(t, `^/call-task-[0-9a-f]{8}$`, task.Path, "a task's key adds its tool call key's hash to its call ID")
	require.Equal(t, []string{review.Path + " guardian", review.Path + " guardian"}, got, "a review inside a running call joins its call's review history")
}

// A reused call ID gets its own permission review, a resumed turn's review keeps its own, and
// neither shares the history of the reviewed task's subagent. A denied call's review is saved too.
func TestPermissionReviewKeyIsUniquePerToolCall(t *testing.T) {
	call := responseWithFunctionCalls("root", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call_0", "task", `{"description":"Review","prompt":"look","subagent_type":"child","command":""}`)})
	allowed := []*responses.Response{call, responseWithMessage("review", `{"risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"ok"}`), responseWithMessage("root-final", "done")}
	denied := []*responses.Response{call, responseWithMessage("review", `{"risk_level":"critical","user_authorization":"unknown","outcome":"deny","rationale":"no"}`), responseWithMessage("root-final", "done")}
	saved := map[string][]SessionEntry{}
	rows := make([]PublicProgress, 0, 3)

	for i, turnID := range []string{"turn-1", "turn-1", "turn-2"} {
		mock := mockResponses([][]*responses.Response{allowed, allowed, denied}[i]...)
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{"child": testAgent("child")}})
		factory.childSessions = answeringSessions(saved, false)
		factory.autoApprovePermissions = true
		looper := testLooper(mock)
		looper.agent = Agent{Name: "main"}
		looper.AutoApprovePermissions = true
		looper.PermissionReviewer = factory
		looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "*", Action: permissionAuto}}}}}
		looper.Tools = map[string]looperTool{"task": factory.taskTool()}
		output := make(chan ChatResponse, 20)

		input := make(chan PromptInput, 1)
		input <- PromptInput{TurnID: turnID, Role: PromptInputRoleUser, Texts: []string{"start"}, Responses: output}

		close(input)

		require.NoError(t, looper.Loop(t.Context(), input, emptySession(), discardSession(), make(chan os.Signal, 1)))

		progress := PublicProgressFromTrace(looper.observations.trace)
		rows = append(rows, progress[slices.IndexFunc(progress, func(item PublicProgress) bool { return item.ID == "call_0" })])
	}

	require.Equal(t, taskKey("turn-1", "call_0"), rows[0].SubagentKey)
	require.Len(t, saved["/"+reviewKey("turn-1", "call_0")], 2, "both runs of turn-1 review under one key")
	require.Len(t, saved["/"+reviewKey("turn-2", "call_0")], 1)
	require.Equal(t, "guardian", saved["/"+reviewKey("turn-2", "call_0")][0].Agent)
	require.Equal(t, ReviewTurn(CallKey("turn-2", "call_0")), saved["/"+reviewKey("turn-2", "call_0")][0].TurnID, "a review saves its answer under the turn named after the call it checks")
	require.Equal(t, PublicProgressBlocked, rows[2].State)
	require.Len(t, saved, 2, "the reviews of both turns")
}

// Like OpenCode's per-subagent session ID, a reused call ID starts a new subagent; a resumed call keeps its own.
func TestTaskSubagentKeyIsUniquePerToolCall(t *testing.T) {
	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{"child": testAgent("child")}})
	sessions := answeringSessions(map[string][]SessionEntry{}, false)
	factory.childSessions = sessions
	task := factory.taskTool()

	keys := make([]string, 0, 3)

	for _, turnID := range []string{"turn-1", "turn-1", "turn-2"} {
		parent := testLooper(mockResponses())
		parent.observations.turnID = turnID
		result, err := task.Call(withToolCallContext(t.Context(), parent, nil, "call_0"), json.RawMessage(`{"subagent_type":"child","prompt":"look","description":"Review","command":""}`), nil, toolCallMetadata{callID: "call_0", observations: parent.observations, progress: &PublicProgress{ID: "call_0"}})
		require.NoError(t, err)
		require.Equal(t, "<task_result>\nlook: done\n</task_result>", result.Output)

		progress := PublicProgressFromTrace(parent.observations.trace)
		keys = append(keys, progress[len(progress)-1].SubagentKey)
	}

	require.Equal(t, taskKey("turn-1", "call_0"), keys[0])
	require.Equal(t, keys[0], keys[1], "a resumed call keeps its subagent")
	require.Equal(t, taskKey("turn-2", "call_0"), keys[2])
	require.NotEqual(t, keys[0], keys[2])

	for i, call := range sessions.WriteSubagentCalls() {
		require.Equal(t, keys[i], call.Write.Key.Path)
	}

	// Under a child history, the row names the new subagent under that history, and the subagent
	// starts under it.
	factory.childKey = ChildKey{Path: "/parent"}
	parent := testLooper(mockResponses())
	parent.observations.turnID = "turn-3"
	_, err := task.Call(withToolCallContext(t.Context(), parent, nil, "call_0"), json.RawMessage(`{"subagent_type":"child","prompt":"look","description":"Review","command":""}`), nil, toolCallMetadata{callID: "call_0", observations: parent.observations, progress: &PublicProgress{ID: "call_0"}})
	require.NoError(t, err)

	progress := PublicProgressFromTrace(parent.observations.trace)
	require.Equal(t, taskKey("turn-3", "call_0"), progress[len(progress)-1].SubagentKey)
	require.Equal(t, ChildKey{Path: "/parent" + taskKey("turn-3", "call_0"), Parent: "/parent", Turn: "turn-3", Call: "call_0"}, sessions.WriteSubagentCalls()[3].Write.Key)
}

// taskKey is the subagent key of a new root task call keyed turnID + "/call/" + callID.
func taskKey(turnID, callID string) string {
	sum := sha256.Sum256([]byte(turnID + "/call/" + callID))
	return "/" + callID + "-" + hex.EncodeToString(sum[:4])
}

// reviewKey is the permission review key segment of a root call keyed turnID + "/call/" + callID.
func reviewKey(turnID, callID string) string {
	sum := sha256.Sum256([]byte(turnID + "/call/" + callID))
	return callID + "-review-" + hex.EncodeToString(sum[:4])
}

func testTaskFactory(client responsesAPI, agents Agents) *toolFactory {
	var bashTool looperTool

	bashTool.Permission = "bash"

	var readTool looperTool

	readTool.Permission = "read"

	var factory toolFactory

	factory.resolver = testResolverForResponsesAPI(client)
	factory.autoApproverModel = defaultOpenAIModel
	factory.agents = agents
	factory.skills = Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}
	factory.baseTools = map[string]looperTool{
		"bash": bashTool,
		"read": readTool,
	}
	factory.childSessions = InertChildSessions{}
	factory.backgroundJobs = InertBackgroundJobs{}

	return &factory
}

func testAgent(name string) Agent {
	var agent Agent

	agent.Name = name
	agent.Model = "gpt-5.4"

	return agent
}

func testAgentWithDescription(name, description string) Agent {
	agent := testAgent(name)
	agent.Description = description

	return agent
}

func testTaskParams(description, prompt, subagentType string) *taskParams {
	var params taskParams

	params.Description = description
	params.Prompt = prompt
	params.SubagentType = subagentType

	return &params
}

func testPermissionReadTool(subjects func(json.RawMessage) ([]string, error)) looperTool {
	var tool looperTool

	tool.Permission = "read"
	tool.Subjects = subjects

	return tool
}

func drainBufferedResponses(output <-chan ChatResponse) []ChatResponse {
	var items []ChatResponse

	for {
		select {
		case item := <-output:
			items = append(items, item)
		default:
			return items
		}
	}
}

func permissionSetForActions(actions map[string]PermissionAction) PermissionSet {
	buckets := make([]PermissionBucket, 0, len(actions))
	for name, action := range actions {
		buckets = append(buckets, PermissionBucket{Name: name, Rules: []PermissionRule{{Pattern: "*", Action: action}}})
	}

	return PermissionSet{Buckets: buckets}
}
