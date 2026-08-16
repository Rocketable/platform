package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestTaskRejectsEmptySubagentModelWithoutResolving(t *testing.T) {
	for _, model := range []string{"", "   "} {
		t.Run(model, func(t *testing.T) {
			calls := 0
			factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{
				"child": {Name: "child", Model: model},
			}})
			factory.resolver = testModelResolverFunc(func(string) (*openai.Client, ProviderOrigin, error) {
				calls++
				return nil, ProviderOrigin{}, errors.New("resolver called")
			})

			_, err := factory.runTask(context.Background(), testTaskParams("Child", "do it", "child"), toolCallMetadata{}, testTaskOutput())

			require.ErrorContains(t, err, "required non-empty string")
			require.Zero(t, calls)
		})
	}
}

func TestTaskResolvesSubagentModelIndependently(t *testing.T) {
	rootClient, rootRequests := testResolverClient(t, "wrong")
	workClient, workRequests := testResolverClient(t, "child answer")
	resolver := testModelResolverFunc(func(model string) (*openai.Client, ProviderOrigin, error) {
		if model == "work/gpt-child" {
			return workClient, ProviderOrigin{Provider: "work", Model: "api-child"}, nil
		}

		return rootClient, ProviderOrigin{Provider: "openai", Model: model}, nil
	})
	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{
		"child": {Name: "child", Model: "work/gpt-child"},
	}})
	factory.resolver = resolver

	got, err := factory.runTask(context.Background(), testTaskParams("Child", "do it", "child"), toolCallMetadata{}, testTaskOutput())

	require.NoError(t, err)
	require.Equal(t, "<task_result>\nchild answer\n</task_result>", got)
	require.Len(t, workRequests, 1)
	require.Contains(t, <-workRequests, `"model":"api-child"`)
	require.Empty(t, rootRequests)
}

func TestTaskResolvesGuardrailModelIndependently(t *testing.T) {
	rootClient, rootRequests := testResolverClient(t, "wrong")
	guardClient, guardRequests := testResolverClient(t, `{"approved":false,"reason":"blocked"}`)
	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{}})
	factory.resolver = testModelResolverFunc(func(model string) (*openai.Client, ProviderOrigin, error) {
		if model == "safety/guard" {
			return guardClient, ProviderOrigin{Provider: "safety", Model: "guard-api"}, nil
		}

		return rootClient, ProviderOrigin{Provider: "openai", Model: model}, nil
	})
	agent := Agent{Name: "guard", Model: "safety/guard"}

	decision := factory.runGuardrail(context.Background(), &agent, ChildRunStageDelegation, "review", "child", toolCallMetadata{}, testTaskOutput())

	require.False(t, decision.Approved)
	require.Equal(t, "blocked", decision.Reason)
	require.Len(t, guardRequests, 1)
	require.Contains(t, <-guardRequests, `"model":"guard-api"`)
	require.Empty(t, rootRequests)
}

func TestTaskTool(t *testing.T) {
	t.Run("applies child output schema", func(t *testing.T) {
		mock := mockResponses(responseWithTaskMessages())
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Model: "gpt-5.4", Prompt: "review carefully", OutputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"answer": map[string]any{"type": "string"}},
				"required":   []any{"answer"},
			}},
		}})

		got, err := factory.runTask(context.Background(), testTaskParams("Review", "check this", "review"), toolCallMetadata{}, testTaskOutput())

		require.NoError(t, err)
		require.Equal(t, "<task_result>\nsecond\n</task_result>", got)
		require.Len(t, mock.calls, 1)
		require.NotNil(t, mock.calls[0].Text.Format.OfJSONSchema)
		require.Equal(t, "agent_output", mock.calls[0].Text.Format.OfJSONSchema.Name)
		require.True(t, mock.calls[0].Text.Format.OfJSONSchema.Strict.Value)
		require.Equal(t, false, mock.calls[0].Text.Format.OfJSONSchema.Schema["additionalProperties"])
	})

	t.Run("returns last final child text wrapped in task result", func(t *testing.T) {
		mock := mockResponses(responseWithTaskMessages())
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Description: "", Model: "gpt-5.4", ReasoningEffort: "", Verbosity: "low", MaxRecursion: nil, Prompt: "review carefully", Location: "", Permission: PermissionSet{Buckets: nil}, Frontmatter: nil, FileMode: 0},
		}})

		got, err := factory.runTask(context.Background(), testTaskParams("Review", "check this", "review"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, testTaskOutput())

		require.NoError(t, err)
		require.Equal(t, "<task_result>\nsecond\n</task_result>", got)
		require.Len(t, mock.calls, 1)
		require.Equal(t, "review carefully", mock.calls[0].Instructions.Value)
		require.Equal(t, responses.ResponseTextConfigVerbosityLow, mock.calls[0].Text.Verbosity)
		require.Contains(t, marshalJSON(t, mock.calls[0].Input.OfInputItemList), "check this")
	})

	t.Run("returns empty task result when child has no final text", func(t *testing.T) {
		mock := mockResponses(testResponse("empty", nil))
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"empty": testAgent("empty"),
		}})

		got, err := factory.runTask(context.Background(), testTaskParams("Empty", "do it", "empty"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, testTaskOutput())

		require.NoError(t, err)
		require.Equal(t, "<task_result>\n\n</task_result>", got)
	})

	t.Run("rejects unknown subagent", func(t *testing.T) {
		factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{}})

		_, err := factory.runTask(context.Background(), testTaskParams("", "", "missing"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, testTaskOutput())

		require.EqualError(t, err, "unknown agent type: missing is not a valid agent type")
	})

	t.Run("rejects delegation when recursion budget is exhausted", func(t *testing.T) {
		remaining := 0
		factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{
			"review": testAgent("review"),
		}})
		factory.recursionRemaining = &remaining

		_, err := factory.runTask(context.Background(), testTaskParams("Review", "check this", "review"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, testTaskOutput())

		require.EqualError(t, err, "maxRecursion limit reached: task delegation is unavailable")
	})

	t.Run("allows any agent", func(t *testing.T) {
		mock := mockResponses(responseWithTaskMessages())
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"helper": testAgentWithPrompt("helper", "help carefully"),
		}})

		got, err := factory.runTask(context.Background(), testTaskParams("Help", "assist", "helper"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, testTaskOutput())

		require.NoError(t, err)
		require.Equal(t, "<task_result>\nsecond\n</task_result>", got)
	})

	t.Run("leaves subagent prompt shell commands literal by default", func(t *testing.T) {
		mock := mockResponses(responseWithTaskMessages())
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": testAgentWithPrompt("review", "review !`printf carefully`"),
		}})
		factory.systemPrompt = "base prompt"

		got, err := factory.runTask(context.Background(), testTaskParams("Review", "check this", "review"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, testTaskOutput())

		require.NoError(t, err)
		require.Equal(t, "<task_result>\nsecond\n</task_result>", got)
		require.Equal(t, "base prompt\n\nreview !`printf carefully`", mock.calls[0].Instructions.Value)
		require.Equal(t, "review !`printf carefully`", factory.agents.Items["review"].Prompt)
	})

	t.Run("expands subagent prompt when enabled without mutating loaded agent", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })

		require.NoError(t, root.WriteFile("MEMORY.md", []byte("carefully"), 0o644))
		shellTemp := testPromptShellTempConfig(t, root, dir)
		env, err := newPromptExpansionEnvironment(root, shellTemp, nil, DefaultShellCommand)
		require.NoError(t, err)

		mock := mockResponses(responseWithTaskMessages())
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": testAgentWithPrompt("review", "review !`cat MEMORY.md`"),
		}})
		factory.systemPrompt = "base prompt"
		factory.expandPromptShellCommands = testPromptExpansion(false, true, false)
		factory.promptExpansion = env

		got, err := factory.runTask(context.Background(), testTaskParams("Review", "check this", "review"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, testTaskOutput())

		require.NoError(t, err)
		require.Equal(t, "<task_result>\nsecond\n</task_result>", got)
		require.Equal(t, "base prompt\n\nreview carefully", mock.calls[0].Instructions.Value)
		require.Equal(t, "review !`cat MEMORY.md`", factory.agents.Items["review"].Prompt)
	})

	t.Run("primary expansion does not enable subagent expansion", func(t *testing.T) {
		mock := mockResponses(responseWithTaskMessages())
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": testAgentWithPrompt("review", "review !`printf carefully`"),
		}})
		factory.systemPrompt = "base prompt"
		factory.expandPromptShellCommands = testPromptExpansion(true, false, false)

		got, err := factory.runTask(context.Background(), testTaskParams("Review", "check this", "review"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, testTaskOutput())

		require.NoError(t, err)
		require.Equal(t, "<task_result>\nsecond\n</task_result>", got)
		require.Equal(t, "base prompt\n\nreview !`printf carefully`", mock.calls[0].Instructions.Value)
	})

	t.Run("parent context cancellation stops child", func(t *testing.T) {
		started := make(chan struct{})
		mock := mockResponseFunc(func(ctx context.Context, _ *responses.ResponseNewParams) (*responses.Response, error) {
			close(started)
			<-ctx.Done()

			return nil, ctx.Err()
		})
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"slow": testAgent("slow"),
		}})
		ctx, cancel := context.WithCancel(context.Background())

		var group errgroup.Group

		group.Go(func() error {
			_, err := factory.runTask(ctx, testTaskParams("Slow", "wait", "slow"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, testTaskOutput())
			return err
		})

		<-started
		cancel()
		require.ErrorIs(t, group.Wait(), context.Canceled)
	})

	t.Run("diagnostics mirrors subagent output with prefixes", func(t *testing.T) {
		mock := mockResponses(responseWithTaskMessages())
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": testAgentWithPrompt("review", "review carefully"),
		}})
		factory.diagnostics = true

		output := make(chan ChatResponse, 10)
		got, err := factory.runTask(context.Background(), testTaskParams("Review", "check this", "review"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, output)

		require.NoError(t, err)
		require.Equal(t, "<task_result>\nsecond\n</task_result>", got)

		diagnostics := drainBufferedResponses(output)

		require.Equal(t, []ChatResponse{
			subagentDiagnosticResponse(testReviewSubagentDiagnostic("delegation", 1, 1, "started: Review")),
			subagentDiagnosticResponse(testReviewSubagentDiagnostic("reasoning summary", 1, 1, "thinking")),
			subagentDiagnosticResponse(testReviewSubagentDiagnostic("assistant commentary", 1, 1, "commentary")),
			subagentDiagnosticResponse(testReviewSubagentDiagnostic("assistant message", 1, 1, "first")),
			subagentDiagnosticResponse(testReviewSubagentDiagnostic("assistant message", 1, 1, "second")),
			subagentDiagnosticResponse(testReviewSubagentDiagnostic("delegation", 1, 1, "finished")),
		}, diagnostics)
	})

	t.Run("guardrail approval allows child and response", func(t *testing.T) {
		mock := mockResponses(
			testResponse("delegation-gate", []responses.ResponseOutputItemUnion{
				testReasoningOutputItem("delegation-reasoning", "", "checking delegation"),
				testMessageOutputItem("delegation-commentary", "commentary", "verifying target"),
				testMessageOutputItem("delegation-final", "", `{"approved":true,"reason":"ok"}`),
			}),
			responseWithTaskMessages(),
			responseWithMessage("response-gate", `{"approved":true,"reason":""}`),
		)
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"main":      testAgent("main"),
			"review":    {Name: "review", Model: "gpt-5.4", Guardrail: "safety", Prompt: "review carefully"},
			"safety":    {Name: "safety", Model: "gpt-5.4", Guardrail: "recursive", Prompt: "guard carefully", OutputSchema: map[string]any{"type": "string"}},
			"recursive": testAgentWithPrompt("recursive", "recursive guard"),
		}})
		mainAgent := factory.agents.Items["main"]
		factory.agent = &mainAgent
		factory.diagnostics = true
		output := make(chan ChatResponse, 20)

		got, err := factory.runTask(context.Background(), testTaskParams("Review", "check this", "review"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, output)

		require.NoError(t, err)
		require.Equal(t, "<task_result>\nsecond\n</task_result>", got)
		require.Equal(t, []ChatResponse{
			subagentDiagnosticResponse(testGuardrailSubagentDiagnostic("review", "safety", ChildRunStageDelegation, "reasoning summary", 1, 1, "checking delegation")),
			subagentDiagnosticResponse(testGuardrailSubagentDiagnostic("review", "safety", ChildRunStageDelegation, "assistant commentary", 1, 1, "verifying target")),
			subagentDiagnosticResponse(testGuardrailResultDiagnostic(ChildRunStageDelegation, "approve: ok")),
			subagentDiagnosticResponse(testReviewSubagentDiagnostic("delegation", 1, 1, "started: Review")),
			subagentDiagnosticResponse(testGuardrailResultDiagnostic(ChildRunStageResponse, "approve")),
			subagentDiagnosticResponse(testReviewSubagentDiagnostic("reasoning summary", 1, 1, "thinking")),
			subagentDiagnosticResponse(testReviewSubagentDiagnostic("assistant commentary", 1, 1, "commentary")),
			subagentDiagnosticResponse(testReviewSubagentDiagnostic("assistant message", 1, 1, "first")),
			subagentDiagnosticResponse(testReviewSubagentDiagnostic("assistant message", 1, 1, "second")),
			subagentDiagnosticResponse(testReviewSubagentDiagnostic("delegation", 1, 1, "finished")),
		}, drainBufferedResponses(output))
		require.Len(t, mock.calls, 3)
		require.Equal(t, "guard carefully", mock.calls[0].Instructions.Value)
		require.NotNil(t, mock.calls[0].Text.Format.OfJSONSchema)
		require.Equal(t, "guardrail_decision", mock.calls[0].Text.Format.OfJSONSchema.Name)
		require.Contains(t, marshalJSON(t, mock.calls[0].Input.OfInputItemList), "Current Action: delegation")
		require.Contains(t, marshalJSON(t, mock.calls[0].Input.OfInputItemList), "The agent main wants to delegate to review:")
		require.Contains(t, marshalJSON(t, mock.calls[0].Input.OfInputItemList), "check this")
		require.Equal(t, "guard carefully", mock.calls[2].Instructions.Value)
		require.Contains(t, marshalJSON(t, mock.calls[2].Input.OfInputItemList), "Current Action: response")
		require.Contains(t, marshalJSON(t, mock.calls[2].Input.OfInputItemList), "And the response from review to main:")
		require.Contains(t, marshalJSON(t, mock.calls[2].Input.OfInputItemList), "second")
	})

	t.Run("guardrail rejection skips child", func(t *testing.T) {
		mock := mockResponses(responseWithMessage("delegation-gate", `{"approved":false,"reason":"too risky"}`))
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Model: "gpt-5.4", Guardrail: "safety", Prompt: "review carefully"},
			"safety": testAgentWithPrompt("safety", "guard carefully"),
		}})

		got, err := factory.runTask(context.Background(), testTaskParams("Review", "check this", "review"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, testTaskOutput())

		require.NoError(t, err)
		require.Equal(t, "<task_result>\ndelegation blocked: too risky\n</task_result>", got)
		require.Len(t, mock.calls, 1)
	})

	t.Run("guardrail response rejection bubbles reason", func(t *testing.T) {
		var childRunEvents []ChildRunEvent

		mock := mockResponses(
			responseWithMessage("delegation-gate", `{"approved":true,"reason":""}`),
			responseWithTaskMessages(),
			responseWithMessage("response-gate", `{"approved":false,"reason":"do not share"}`),
		)
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Model: "gpt-5.4", Guardrail: "safety", Prompt: "review carefully"},
			"safety": testAgentWithPrompt("safety", "guard carefully"),
		}})
		factory.diagnostics = true
		factory.childRunLogger = func(event *ChildRunEvent) {
			childRunEvents = append(childRunEvents, *event)
		}
		output := make(chan ChatResponse, 10)

		got, err := factory.runTask(context.Background(), testTaskParams("Review", "check this", "review"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, output)

		require.NoError(t, err)
		require.Equal(t, "<task_result>\ndelegation response blocked: do not share\n</task_result>", got)
		require.Equal(t, []ChatResponse{
			subagentDiagnosticResponse(testGuardrailResultDiagnostic(ChildRunStageDelegation, "approve")),
			subagentDiagnosticResponse(testReviewSubagentDiagnostic("delegation", 1, 1, "started: Review")),
			subagentDiagnosticResponse(testGuardrailResultDiagnostic(ChildRunStageResponse, "reject: do not share")),
		}, drainBufferedResponses(output))
		require.Equal(t, []ChildRunEvent{
			{Kind: ChildRunKindGuardrail, Stage: ChildRunStageDelegation, Agent: "safety", Item: assistantMessage(`{"approved":true,"reason":""}`)},
			{Kind: ChildRunKindGuardrail, Stage: ChildRunStageResponse, Agent: "safety", Item: assistantMessage(`{"approved":false,"reason":"do not share"}`)},
		}, childRunEvents)
	})

	t.Run("guardrail invalid JSON fails closed", func(t *testing.T) {
		mock := mockResponses(responseWithMessage("delegation-gate", `not json`))
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Model: "gpt-5.4", Guardrail: "safety", Prompt: "review carefully"},
			"safety": testAgentWithPrompt("safety", "guard carefully"),
		}})

		got, err := factory.runTask(context.Background(), testTaskParams("Review", "check this", "review"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, testTaskOutput())

		require.NoError(t, err)
		require.Equal(t, "<task_result>\ndelegation blocked: inter-agent guardrail returned invalid JSON\n</task_result>", got)
		require.Len(t, mock.calls, 1)
	})

	t.Run("guardrail tools follow its permissions", func(t *testing.T) {
		mock := mockResponses(
			responseWithMessage("delegation-gate", `{"approved":false,"reason":"stop"}`),
		)
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Model: "gpt-5.4", Guardrail: "safety", Prompt: "review carefully"},
			"safety": testAgentWithPermissionName("safety", PermissionSet{Buckets: []PermissionBucket{{Name: "read", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}),
		}})
		readTool := testLooperTool("read")
		readTool.Definition = *functionTool("read", "Read", map[string]any{})
		readTool.Permission = "read"
		factory.baseTools["read"] = readTool

		_, err := factory.runTask(context.Background(), testTaskParams("Review", "check this", "review"), toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, testTaskOutput())

		require.NoError(t, err)
		require.Len(t, mock.calls, 1)
		require.Contains(t, marshalJSON(t, mock.calls[0].Tools), `"name":"execute"`)
		require.NotContains(t, marshalJSON(t, mock.calls[0].Tools), `"name":"read"`)
	})
}

func TestTaskToolPermissionDefaults(t *testing.T) {
	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{}})

	t.Run("startup agent denies tools by default", func(t *testing.T) {
		tools := factory.toolsFor(nil)

		require.Empty(t, tools)
	})

	t.Run("tasked subagent denies tools by default", func(t *testing.T) {
		agent := testAgentWithPermission(PermissionSet{Buckets: nil})
		agent.Name = "plain"

		tools := factory.toolsFor(agent)

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

		tools := factory.toolsFor(agent)

		require.NotContains(t, tools, "task")
	})

	t.Run("startup agent can deny individual tools", func(t *testing.T) {
		agent := testAgentWithPermission(permissionSetForActions(map[string]PermissionAction{"bash": permissionDeny}))
		agent.Name = "main"

		tools := factory.toolsFor(agent)

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

		tools := factory.toolsFor(agent)

		require.Contains(t, tools, "websearch")
		require.Equal(t, "web_search", *tools["websearch"].Hosted.GetType())
	})

	t.Run("specific allow keeps tool visible after wildcard deny", func(t *testing.T) {
		agent := testAgentWithPermission(PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{
			{Pattern: "*", Action: permissionDeny},
			{Pattern: "reviewer", Action: permissionAllow},
		}}}})
		agent.Name = "main"

		tools := factory.toolsFor(agent)

		require.Contains(t, tools, "task")
	})

	t.Run("specific skill allow keeps skill tool visible", func(t *testing.T) {
		agent := testAgentWithPermission(PermissionSet{Buckets: []PermissionBucket{{Name: "skill", Rules: []PermissionRule{
			{Pattern: "*", Action: permissionDeny},
			{Pattern: "docs-helper", Action: permissionAllow},
		}}}})
		agent.Name = "main"

		tools := factory.toolsFor(agent)

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

		return []string{rootedPathSubject(readToolPath(params))}, nil
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
		responseWithMessage("child-final", "child answer"),
		responseWithMessage("parent-final", "parent done"),
	)
	factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
		"review": testAgent("review"),
	}})
	looper := testLooper(mock)
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "review", Action: permissionAllow}}}}}
	looper.Tools = map[string]looperTool{"task": factory.taskTool()}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "start", output)

	close(input)

	interrupts := make(chan os.Signal, 1)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession, interrupts)

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("parent done")}, collectResponses(output))
	require.Len(t, mock.calls, 3)
	encoded := marshalJSON(t, mock.calls[2].Input.OfInputItemList)
	require.Contains(t, encoded, "child answer")
	require.Contains(t, encoded, `\u003ctask_result\u003e`)
}

func TestLooperTaskMaxRecursion(t *testing.T) {
	t.Run("unlimited preserves nested delegation", func(t *testing.T) {
		mock := mockResponses(
			responseWithFunctionCalls("parent-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "task", `{"description":"Review","prompt":"look","subagent_type":"review"}`)}),
			responseWithFunctionCalls("child-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-2", "call-2", "task", `{"description":"Work","prompt":"go","subagent_type":"worker"}`)}),
			responseWithMessage("grandchild-final", "grandchild done"),
			responseWithMessage("child-final", "child done"),
			responseWithMessage("parent-final", "parent done"),
		)
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": testAgentWithPermissionName("review", PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "worker", Action: permissionAllow}}}}}),
			"worker": testAgent("worker"),
		}})
		looper := testLooper(mock)
		looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "review", Action: permissionAllow}}}}}
		looper.Tools = map[string]looperTool{"task": factory.taskTool()}
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- testPromptInput(PromptInputRoleUser, "start", output)

		close(input)

		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		require.Len(t, mock.calls, 5)
		require.Equal(t, []ChatResponse{assistantMessage("parent done")}, collectResponses(output))
		require.Contains(t, marshalJSON(t, mock.calls[3].Input.OfInputItemList), "grandchild done")
	})

	t.Run("one level blocks grandchild delegation", func(t *testing.T) {
		childLimit := 5
		remaining := 1
		mock := mockResponses(
			responseWithFunctionCalls("parent-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "task", `{"description":"Review","prompt":"look","subagent_type":"review"}`)}),
			responseWithFunctionCalls("child-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-2", "call-2", "task", `{"description":"Work","prompt":"go","subagent_type":"worker"}`)}),
			responseWithMessage("child-final", "child done"),
			responseWithMessage("parent-final", "parent done"),
		)
		reviewAgent := testAgentWithPermissionName("review", PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "worker", Action: permissionAllow}}}}})
		reviewAgent.MaxRecursion = &childLimit
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": reviewAgent,
			"worker": testAgent("worker"),
		}})
		factory.recursionRemaining = &remaining
		looper := testLooper(mock)
		looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "review", Action: permissionAllow}}}}}
		looper.Tools = map[string]looperTool{"task": factory.taskTool()}
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- testPromptInput(PromptInputRoleUser, "start", output)

		close(input)

		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		require.Equal(t, []ChatResponse{assistantMessage("parent done")}, collectResponses(output))
		require.Len(t, mock.calls, 4)
		require.Contains(t, marshalJSON(t, mock.calls[2].Input.OfInputItemList), "tool not found")
	})

	t.Run("siblings each receive remaining depth", func(t *testing.T) {
		remaining := 1
		mock := mockResponses(
			responseWithFunctionCalls("parent-tool", []responses.ResponseFunctionToolCall{
				testFunctionCall("tool-1", "call-1", "task", `{"description":"Review first","prompt":"look","subagent_type":"review"}`),
				testFunctionCall("tool-2", "call-2", "task", `{"description":"Review second","prompt":"look","subagent_type":"review"}`),
			}),
			responseWithMessage("child-first", "child one"),
			responseWithMessage("child-second", "child two"),
			responseWithMessage("parent-final", "parent done"),
		)
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": testAgent("review"),
		}})
		factory.recursionRemaining = &remaining
		looper := testLooper(mock)
		looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "review", Action: permissionAllow}}}}}
		looper.Tools = map[string]looperTool{"task": factory.taskTool()}
		looper.ParallelToolCalls = 1
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- testPromptInput(PromptInputRoleUser, "start", output)

		close(input)

		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		require.Equal(t, []ChatResponse{assistantMessage("parent done")}, collectResponses(output))
		require.Len(t, mock.calls, 4)
	})
}

func TestLooperNumbersSiblingTaskDiagnostics(t *testing.T) {
	mock := mockResponses(
		responseWithFunctionCalls("parent-tool", []responses.ResponseFunctionToolCall{
			testFunctionCall("tool-1", "call-1", "task", `{"description":"Review first","prompt":"look","subagent_type":"review"}`),
			testFunctionCall("tool-2", "call-2", "task", `{"description":"Review second","prompt":"look","subagent_type":"review"}`),
		}),
		responseWithMessage("child-first", "child one"),
		responseWithMessage("child-second", "child two"),
		responseWithMessage("parent-final", "parent done"),
	)
	factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
		"review": testAgent("review"),
	}})
	factory.diagnostics = true
	looper := testLooper(mock)
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "review", Action: permissionAllow}}}}}
	looper.Tools = map[string]looperTool{"task": factory.taskTool()}
	looper.ParallelToolCalls = 1
	output := make(chan ChatResponse, 20)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "start", output)

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{
		subagentDiagnosticResponse(testReviewSubagentDiagnostic("delegation", 1, 2, "started: Review first")),
		subagentDiagnosticResponse(testReviewSubagentDiagnostic("assistant message", 1, 2, "child one")),
		subagentDiagnosticResponse(testReviewSubagentDiagnostic("delegation", 1, 2, "finished")),
		subagentDiagnosticResponse(testReviewSubagentDiagnostic("delegation", 2, 2, "started: Review second")),
		subagentDiagnosticResponse(testReviewSubagentDiagnostic("assistant message", 2, 2, "child two")),
		subagentDiagnosticResponse(testReviewSubagentDiagnostic("delegation", 2, 2, "finished")),
		assistantMessage("parent done"),
	}, collectResponses(output))
}

func testTaskFactory(client responsesAPI, agents Agents) *toolFactory {
	var bashTool looperTool

	bashTool.Permission = "bash"

	var readTool looperTool

	readTool.Permission = "read"

	var factory toolFactory

	factory.resolver = testResolverForResponsesAPI(client)
	factory.autoApproverModel = defaultModelRef().display()
	factory.agents = agents
	factory.skills = Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}
	factory.baseTools = map[string]looperTool{
		"bash": bashTool,
		"read": readTool,
	}
	factory.childRunLogger = DiscardChildRunLog

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

func testAgentWithPrompt(name, prompt string) Agent {
	agent := testAgent(name)
	agent.Prompt = prompt

	return agent
}

func testAgentWithPermissionName(name string, permission PermissionSet) Agent {
	agent := testAgent(name)
	agent.Permission = permission

	return agent
}

func testTaskParams(description, prompt, subagentType string) taskParams {
	var params taskParams

	params.Description = description
	params.Prompt = prompt
	params.SubagentType = subagentType

	return params
}

func testTaskOutput() chan ChatResponse {
	return make(chan ChatResponse, 20)
}

func testGuardrailSubagentDiagnostic(guardedAgent, guardrailAgent string, stage ChildRunStage, label string, index, total int, text string) *SubagentDiagnostic {
	return &SubagentDiagnostic{Name: guardedAgent, Index: index, Total: total, Subagent: &SubagentDiagnostic{Name: guardrailAgent, Label: "guardrail(" + string(stage) + ")", Subagent: &SubagentDiagnostic{Label: label, Text: text}}}
}

func testGuardrailResultDiagnostic(stage ChildRunStage, text string) *SubagentDiagnostic {
	diagnostic := testGuardrailSubagentDiagnostic("review", "safety", stage, "result", 1, 1, "")
	diagnostic.Subagent.Text = text

	return diagnostic
}

func testPromptExpansion(primary, subagent, skill bool) PromptShellCommandExpansion {
	return PromptShellCommandExpansion{PrimaryPrompts: primary, SubagentPrompts: subagent, SkillPrompts: skill, InputPrompts: false}
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

func responseWithTaskMessages() *responses.Response {
	id := "child"

	return testResponse(id, []responses.ResponseOutputItemUnion{
		testReasoningOutputItem(id+"-reasoning", "", "thinking"),
		testMessageOutputItem(id+"-commentary", "commentary", "commentary"),
		testMessageOutputItem(id+"-first", "", "first"),
		testMessageOutputItem(id+"-second", "", "second"),
	})
}
