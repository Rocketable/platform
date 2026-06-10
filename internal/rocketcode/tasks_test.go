//nolint:exhaustruct // Test fixtures intentionally use sparse SDK and app literals.
package rocketcode

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"text/template"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestTaskTool(t *testing.T) {
	t.Run("returns last final child text wrapped in task result", func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{responseWithTaskMessages()}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Verbosity: "low", Prompt: "review carefully"},
		}})

		got, err := factory.runTask(context.Background(), taskParams{Description: "Review", Prompt: "check this", SubagentType: "review"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1})

		require.NoError(t, err)
		require.Equal(t, "<task_result>\nsecond\n</task_result>", got)
		require.Len(t, mock.calls, 1)
		require.Equal(t, "review carefully", mock.calls[0].Instructions.Value)
		require.Equal(t, responses.ResponseTextConfigVerbosityLow, mock.calls[0].Text.Verbosity)
		require.Contains(t, marshalJSON(t, mock.calls[0].Input.OfInputItemList), "check this")
	})

	t.Run("returns empty task result when child has no final text", func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{{ID: "empty"}}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"empty": {Name: "empty"},
		}})

		got, err := factory.runTask(context.Background(), taskParams{Description: "Empty", Prompt: "do it", SubagentType: "empty"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1})

		require.NoError(t, err)
		require.Equal(t, "<task_result>\n\n</task_result>", got)
	})

	t.Run("rejects unknown subagent", func(t *testing.T) {
		factory := testTaskFactory(&mockResponsesAPI{}, Agents{Items: map[string]Agent{}})

		_, err := factory.runTask(context.Background(), taskParams{SubagentType: "missing"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1})

		require.EqualError(t, err, "unknown agent type: missing is not a valid agent type")
	})

	t.Run("rejects delegation when recursion budget is exhausted", func(t *testing.T) {
		remaining := 0
		factory := testTaskFactory(&mockResponsesAPI{}, Agents{Items: map[string]Agent{
			"review": {Name: "review"},
		}})
		factory.recursionRemaining = &remaining

		_, err := factory.runTask(context.Background(), taskParams{Description: "Review", Prompt: "check this", SubagentType: "review"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1})

		require.EqualError(t, err, "maxRecursion limit reached: task delegation is unavailable")
	})

	t.Run("allows any agent", func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{responseWithTaskMessages()}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"helper": {Name: "helper", Prompt: "help carefully"},
		}})

		got, err := factory.runTask(context.Background(), taskParams{Description: "Help", Prompt: "assist", SubagentType: "helper"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1})

		require.NoError(t, err)
		require.Equal(t, "<task_result>\nsecond\n</task_result>", got)
	})

	t.Run("leaves subagent prompt shell commands literal by default", func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{responseWithTaskMessages()}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Prompt: "review !`printf carefully`"},
		}})
		factory.systemPrompt = "base prompt"

		got, err := factory.runTask(context.Background(), taskParams{Description: "Review", Prompt: "check this", SubagentType: "review"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1})

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
		shellOutput := testPromptShellOutputConfig(t, root, dir)
		env, err := newPromptExpansionEnvironment(root, shellOutput, nil)
		require.NoError(t, err)

		mock := &mockResponsesAPI{responses: []*responses.Response{responseWithTaskMessages()}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Prompt: "review !`cat MEMORY.md`"},
		}})
		factory.systemPrompt = "base prompt"
		factory.expandPromptShellCommands = PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: true, SkillPrompts: false}
		factory.promptExpansion = env

		got, err := factory.runTask(context.Background(), taskParams{Description: "Review", Prompt: "check this", SubagentType: "review"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1})

		require.NoError(t, err)
		require.Equal(t, "<task_result>\nsecond\n</task_result>", got)
		require.Equal(t, "base prompt\n\nreview carefully", mock.calls[0].Instructions.Value)
		require.Equal(t, "review !`cat MEMORY.md`", factory.agents.Items["review"].Prompt)
	})

	t.Run("primary expansion does not enable subagent expansion", func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{responseWithTaskMessages()}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Prompt: "review !`printf carefully`"},
		}})
		factory.systemPrompt = "base prompt"
		factory.expandPromptShellCommands = PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: false, SkillPrompts: false}

		got, err := factory.runTask(context.Background(), taskParams{Description: "Review", Prompt: "check this", SubagentType: "review"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1})

		require.NoError(t, err)
		require.Equal(t, "<task_result>\nsecond\n</task_result>", got)
		require.Equal(t, "base prompt\n\nreview !`printf carefully`", mock.calls[0].Instructions.Value)
	})

	t.Run("parent context cancellation stops child", func(t *testing.T) {
		started := make(chan struct{})
		mock := &mockResponsesAPI{newFunc: func(ctx context.Context, _ responses.ResponseNewParams) (*responses.Response, error) {
			close(started)
			<-ctx.Done()

			return nil, ctx.Err()
		}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"slow": {Name: "slow"},
		}})
		ctx, cancel := context.WithCancel(context.Background())

		var group errgroup.Group

		group.Go(func() error {
			_, err := factory.runTask(ctx, taskParams{Description: "Slow", Prompt: "wait", SubagentType: "slow"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1})
			return err
		})

		<-started
		cancel()
		require.ErrorIs(t, group.Wait(), context.Canceled)
	})

	t.Run("diagnostics mirrors subagent output with prefixes", func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{responseWithTaskMessages()}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Prompt: "review carefully"},
		}})
		factory.diagnostics = true

		output := make(chan ChatResponse, 10)
		got, err := factory.runTask(context.Background(), taskParams{Description: "Review", Prompt: "check this", SubagentType: "review"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, output)

		require.NoError(t, err)
		require.Equal(t, "<task_result>\nsecond\n</task_result>", got)

		diagnostics := drainBufferedResponses(output)

		require.Equal(t, []ChatResponse{
			{Kind: ChatResponseAssistantTool, Subagent: &SubagentDiagnostic{Name: "review", Label: "delegation", Index: 1, Total: 1, Text: "started: Review"}},
			{Kind: ChatResponseAssistantTool, Subagent: &SubagentDiagnostic{Name: "review", Label: "reasoning summary", Index: 1, Total: 1, Text: "thinking"}},
			{Kind: ChatResponseAssistantTool, Subagent: &SubagentDiagnostic{Name: "review", Label: "assistant commentary", Index: 1, Total: 1, Text: "commentary"}},
			{Kind: ChatResponseAssistantTool, Subagent: &SubagentDiagnostic{Name: "review", Label: "assistant message", Index: 1, Total: 1, Text: "first"}},
			{Kind: ChatResponseAssistantTool, Subagent: &SubagentDiagnostic{Name: "review", Label: "assistant message", Index: 1, Total: 1, Text: "second"}},
			{Kind: ChatResponseAssistantTool, Subagent: &SubagentDiagnostic{Name: "review", Label: "delegation", Index: 1, Total: 1, Text: "finished"}},
		}, diagnostics)
	})

	t.Run("inter-agent filter approval allows child and response", func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{
			responseWithMessage("prompt-filter", `{"approved":true,"reason":""}`),
			responseWithTaskMessages(),
			responseWithMessage("response-filter", `{"approved":true,"reason":""}`),
		}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Prompt: "review carefully"},
		}})
		factory.interAgentFilter = testInterAgentFilter(t, "filter {{.ParentAgentPrompt}}", PermissionSet{})

		got, err := factory.runTask(context.Background(), taskParams{Description: "Review", Prompt: "check this", SubagentType: "review"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1})

		require.NoError(t, err)
		require.Equal(t, "<task_result>\nsecond\n</task_result>", got)
		require.Len(t, mock.calls, 3)
		require.Equal(t, "filter check this", mock.calls[0].Instructions.Value)
		require.NotNil(t, mock.calls[0].Text.Format.OfJSONSchema)
		require.Equal(t, "filter second", mock.calls[2].Instructions.Value)
	})

	t.Run("inter-agent filter rejection skips child", func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{responseWithMessage("prompt-filter", `{"approved":false,"reason":"too risky"}`)}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Prompt: "review carefully"},
		}})
		factory.interAgentFilter = testInterAgentFilter(t, "filter {{.ParentAgentPrompt}}", PermissionSet{})

		got, err := factory.runTask(context.Background(), taskParams{Description: "Review", Prompt: "check this", SubagentType: "review"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1})

		require.NoError(t, err)
		require.Equal(t, "<task_result>\ndelegation blocked: too risky\n</task_result>", got)
		require.Len(t, mock.calls, 1)
	})

	t.Run("inter-agent filter response rejection bubbles reason", func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{
			responseWithMessage("prompt-filter", `{"approved":true,"reason":""}`),
			responseWithTaskMessages(),
			responseWithMessage("response-filter", `{"approved":false,"reason":"do not share"}`),
		}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Prompt: "review carefully"},
		}})
		factory.diagnostics = true
		factory.interAgentFilter = testInterAgentFilter(t, "filter {{.ParentAgentPrompt}}", PermissionSet{})
		output := make(chan ChatResponse, 10)

		got, err := factory.runTask(context.Background(), taskParams{Description: "Review", Prompt: "check this", SubagentType: "review"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1}, output)

		require.NoError(t, err)
		require.Equal(t, "<task_result>\ndelegation response blocked: do not share\n</task_result>", got)
		require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantTool, Subagent: &SubagentDiagnostic{Name: "review", Label: "delegation", Index: 1, Total: 1, Text: "started: Review"}}}, drainBufferedResponses(output))
	})

	t.Run("inter-agent filter invalid JSON fails closed", func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{responseWithMessage("prompt-filter", `not json`)}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Prompt: "review carefully"},
		}})
		factory.interAgentFilter = testInterAgentFilter(t, "filter {{.ParentAgentPrompt}}", PermissionSet{})

		got, err := factory.runTask(context.Background(), taskParams{Description: "Review", Prompt: "check this", SubagentType: "review"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1})

		require.NoError(t, err)
		require.Equal(t, "<task_result>\ndelegation blocked: inter-agent guardrail returned invalid JSON\n</task_result>", got)
		require.Len(t, mock.calls, 1)
	})

	t.Run("inter-agent filter tools follow its permissions", func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{
			responseWithMessage("prompt-filter", `{"approved":false,"reason":"stop"}`),
		}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Prompt: "review carefully"},
		}})
		factory.baseTools["read"] = looperTool{Definition: functionTool("read", "Read", map[string]any{}), Permission: "read"}
		factory.interAgentFilter = testInterAgentFilter(t, "filter", PermissionSet{Buckets: []PermissionBucket{{Name: "read", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}})

		_, err := factory.runTask(context.Background(), taskParams{Description: "Review", Prompt: "check this", SubagentType: "review"}, toolCallMetadata{subagentIndex: 1, subagentTotal: 1})

		require.NoError(t, err)
		require.Len(t, mock.calls, 1)
		require.Contains(t, marshalJSON(t, mock.calls[0].Tools), `"name":"read"`)
	})
}

func TestTaskToolPermissionDefaults(t *testing.T) {
	factory := testTaskFactory(&mockResponsesAPI{}, Agents{Items: map[string]Agent{}})

	t.Run("startup agent denies tools by default", func(t *testing.T) {
		tools := factory.toolsFor(nil)

		require.Empty(t, tools)
	})

	t.Run("tasked subagent denies tools by default", func(t *testing.T) {
		agent := &Agent{Name: "plain"}

		tools := factory.toolsFor(agent)

		require.Empty(t, tools)
	})

	t.Run("tasked subagent can allow individual tools", func(t *testing.T) {
		agent := &Agent{Name: "reader", Permission: permissionSetForActions(map[string]PermissionAction{"read": permissionAllow, "task": permissionAllow})}

		tools := factory.toolsFor(agent)

		require.Contains(t, tools, "read")
		require.Contains(t, tools, "task")
		require.NotContains(t, tools, "bash")
	})

	t.Run("recursion budget hides task", func(t *testing.T) {
		remaining := 0
		factory := testTaskFactory(&mockResponsesAPI{}, Agents{Items: map[string]Agent{}})
		factory.recursionRemaining = &remaining
		agent := &Agent{Name: "main", Permission: permissionSetForActions(map[string]PermissionAction{"task": permissionAllow})}

		tools := factory.toolsFor(agent)

		require.NotContains(t, tools, "task")
	})

	t.Run("startup agent can deny individual tools", func(t *testing.T) {
		agent := &Agent{Name: "main", Permission: permissionSetForActions(map[string]PermissionAction{"bash": permissionDeny})}

		tools := factory.toolsFor(agent)

		require.NotContains(t, tools, "bash")
		require.NotContains(t, tools, "read")
	})

	t.Run("startup agent exposes read for edit allow", func(t *testing.T) {
		agent := &Agent{Name: "main", Permission: permissionSetForActions(map[string]PermissionAction{"edit": permissionAllow})}

		tools := factory.toolsFor(agent)

		require.Contains(t, tools, "read")
		require.NotContains(t, tools, "bash")
	})

	t.Run("startup agent can allow hosted websearch", func(t *testing.T) {
		factory.baseTools["websearch"] = webSearchTool()
		agent := &Agent{Name: "main", Permission: permissionSetForActions(map[string]PermissionAction{"websearch": permissionAllow})}

		tools := factory.toolsFor(agent)

		require.Contains(t, tools, "websearch")
		require.Equal(t, "web_search", *tools["websearch"].Hosted.GetType())
	})

	t.Run("specific allow keeps tool visible after wildcard deny", func(t *testing.T) {
		agent := &Agent{Name: "main", Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{
			{Pattern: "*", Action: permissionDeny},
			{Pattern: "reviewer", Action: permissionAllow},
		}}}}}

		tools := factory.toolsFor(agent)

		require.Contains(t, tools, "task")
	})

	t.Run("specific skill allow keeps skill tool visible", func(t *testing.T) {
		agent := &Agent{Name: "main", Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "skill", Rules: []PermissionRule{
			{Pattern: "*", Action: permissionDeny},
			{Pattern: "docs-helper", Action: permissionAllow},
		}}}}}

		tools := factory.toolsFor(agent)

		require.Contains(t, tools, "skill")
	})
}

func TestBashPermissionGrantsOnlyShellOutputRead(t *testing.T) {
	shellOutput := shellOutputConfig{readPattern: ".tmp/shell-outputs/rocketcode-bash-*"}
	factory := testTaskFactory(&mockResponsesAPI{}, Agents{Items: map[string]Agent{}})
	factory.shellOutput = shellOutput

	agent := &Agent{Name: "main", Permission: permissionSetForActions(map[string]PermissionAction{"bash": permissionAllow})}
	tools := factory.toolsFor(agent)
	require.Contains(t, tools, "bash")
	require.Contains(t, tools, "read")

	permissions := shellOutput.effectivePermissions(agent.Permission)
	loop := &looper{Permissions: permissions}
	readTool := looperTool{Permission: "read", Subjects: func(raw json.RawMessage) ([]string, error) {
		params, err := decodeToolParams[readToolParams](raw)
		if err != nil {
			return nil, err
		}

		return []string{rootedPathSubject(readToolPath(params))}, nil
	}}

	decision, denied, err := loop.permissionDecision("read", readTool, json.RawMessage(`{"filePath":".tmp/shell-outputs/rocketcode-bash-123"}`))
	require.NoError(t, err)
	require.False(t, denied, "saved bash output should be readable: %#v", decision)

	decision, denied, err = loop.permissionDecision("read", readTool, json.RawMessage(`{"filePath":".tmp/shell-outputs/tmp/script-temp"}`))
	require.NoError(t, err)
	require.True(t, denied)
	require.Equal(t, "read", decision.Permission)
	require.Equal(t, ".tmp/shell-outputs/tmp/script-temp", decision.Subject)
}

func TestExplicitReadDenyOverridesBashOutputReadGrant(t *testing.T) {
	shellOutput := shellOutputConfig{readPattern: ".tmp/shell-outputs/rocketcode-bash-*"}
	permissions := shellOutput.effectivePermissions(PermissionSet{Buckets: []PermissionBucket{
		{Name: "bash", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}},
		{Name: "read", Rules: []PermissionRule{{Pattern: ".tmp/shell-outputs/rocketcode-bash-*", Action: permissionDeny}}},
	}})
	loop := &looper{Permissions: permissions}
	readTool := looperTool{Permission: "read", Subjects: func(raw json.RawMessage) ([]string, error) {
		params, err := decodeToolParams[readToolParams](raw)
		if err != nil {
			return nil, err
		}

		return []string{rootedPathSubject(readToolPath(params))}, nil
	}}

	decision, denied, err := loop.permissionDecision("read", readTool, json.RawMessage(`{"filePath":".tmp/shell-outputs/rocketcode-bash-123"}`))
	require.NoError(t, err)
	require.True(t, denied)
	require.True(t, decision.Matched)
	require.Equal(t, permissionDeny, decision.Action)
}

func TestTaskToolDescriptionFiltersDeniedSubagents(t *testing.T) {
	agents := Agents{Items: map[string]Agent{
		"builder":  {Name: "builder", Description: "Build things"},
		"helper":   {Name: "helper", Description: "Help everywhere"},
		"reviewer": {Name: "reviewer", Description: "Review changes"},
		"main":     {Name: "main", Description: "Default agent"},
	}}

	t.Run("no active agent lists no subagents", func(t *testing.T) {
		factory := testTaskFactory(&mockResponsesAPI{}, agents)

		description := factory.taskDescription()

		require.Contains(t, description, "No agents are currently available.")
		require.NotContains(t, description, "- builder: Build things")
		require.NotContains(t, description, "- helper: Help everywhere")
		require.NotContains(t, description, "- reviewer: Review changes")
		require.NotContains(t, description, "Default agent")
	})

	t.Run("active agent hides denied subagents", func(t *testing.T) {
		factory := testTaskFactory(&mockResponsesAPI{}, agents)
		factory.agent = &Agent{Name: "main", Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{
			{Pattern: "*", Action: permissionDeny},
			{Pattern: "reviewer", Action: permissionAllow},
		}}}}}

		description := factory.taskDescription()

		require.NotContains(t, description, "- builder: Build things")
		require.NotContains(t, description, "- helper: Help everywhere")
		require.Contains(t, description, "- reviewer: Review changes")
	})

	t.Run("active agent can allow default agent as subagent", func(t *testing.T) {
		factory := testTaskFactory(&mockResponsesAPI{}, agents)
		factory.agent = &Agent{Name: "main", Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "main", Action: permissionAllow}}}}}}

		description := factory.taskDescription()

		require.Contains(t, description, "- main: Default agent")
	})
}

func TestTaskToolDescriptionUsesOpenCodeGuidance(t *testing.T) {
	factory := testTaskFactory(&mockResponsesAPI{}, Agents{Items: map[string]Agent{}})

	description := factory.taskDescription()

	require.Contains(t, description, "Launch a new agent to handle complex, multistep tasks autonomously.")
	require.Contains(t, description, "When NOT to use the Task tool:")
	require.Contains(t, description, "Launch multiple agents concurrently whenever possible")
}

func TestLooperRunsTaskToolCall(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{
		responseWithFunctionCalls("parent-tool", []responses.ResponseFunctionToolCall{{ID: "tool-1", CallID: "call-1", Name: "task", Arguments: `{"description":"Review","prompt":"look","subagent_type":"review"}`}}),
		responseWithMessage("child-final", "child answer"),
		responseWithMessage("parent-final", "parent done"),
	}}
	factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
		"review": {Name: "review"},
	}})
	looper := &looper{
		Client:      mock,
		Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "review", Action: permissionAllow}}}}},
		Tools:       map[string]looperTool{"task": factory.taskTool()},
	}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "start", Responses: output}

	close(input)

	interrupts := make(chan os.Signal, 1)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession, interrupts)

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "parent done"}}, collectResponses(output))
	require.Len(t, mock.calls, 3)
	encoded := marshalJSON(t, mock.calls[2].Input.OfInputItemList)
	require.Contains(t, encoded, "child answer")
	require.Contains(t, encoded, `\u003ctask_result\u003e`)
}

func TestLooperTaskMaxRecursion(t *testing.T) {
	t.Run("unlimited preserves nested delegation", func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{
			responseWithFunctionCalls("parent-tool", []responses.ResponseFunctionToolCall{{ID: "tool-1", CallID: "call-1", Name: "task", Arguments: `{"description":"Review","prompt":"look","subagent_type":"review"}`}}),
			responseWithFunctionCalls("child-tool", []responses.ResponseFunctionToolCall{{ID: "tool-2", CallID: "call-2", Name: "task", Arguments: `{"description":"Work","prompt":"go","subagent_type":"worker"}`}}),
			responseWithMessage("grandchild-final", "grandchild done"),
			responseWithMessage("child-final", "child done"),
			responseWithMessage("parent-final", "parent done"),
		}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "worker", Action: permissionAllow}}}}}},
			"worker": {Name: "worker"},
		}})
		looper := &looper{
			Client:      mock,
			Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "review", Action: permissionAllow}}}}},
			Tools:       map[string]looperTool{"task": factory.taskTool()},
		}
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- PromptInput{Role: PromptInputRoleUser, Text: "start", Responses: output}

		close(input)

		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "parent done"}}, collectResponses(output))
		require.Len(t, mock.calls, 5)
		require.Contains(t, marshalJSON(t, mock.calls[3].Input.OfInputItemList), "grandchild done")
	})

	t.Run("one level blocks grandchild delegation", func(t *testing.T) {
		childLimit := 5
		remaining := 1
		mock := &mockResponsesAPI{responses: []*responses.Response{
			responseWithFunctionCalls("parent-tool", []responses.ResponseFunctionToolCall{{ID: "tool-1", CallID: "call-1", Name: "task", Arguments: `{"description":"Review","prompt":"look","subagent_type":"review"}`}}),
			responseWithFunctionCalls("child-tool", []responses.ResponseFunctionToolCall{{ID: "tool-2", CallID: "call-2", Name: "task", Arguments: `{"description":"Work","prompt":"go","subagent_type":"worker"}`}}),
			responseWithMessage("child-final", "child done"),
			responseWithMessage("parent-final", "parent done"),
		}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review", MaxRecursion: &childLimit, Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "worker", Action: permissionAllow}}}}}},
			"worker": {Name: "worker"},
		}})
		factory.recursionRemaining = &remaining
		looper := &looper{
			Client:      mock,
			Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "review", Action: permissionAllow}}}}},
			Tools:       map[string]looperTool{"task": factory.taskTool()},
		}
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- PromptInput{Role: PromptInputRoleUser, Text: "start", Responses: output}

		close(input)

		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "parent done"}}, collectResponses(output))
		require.Len(t, mock.calls, 4)
		require.Contains(t, marshalJSON(t, mock.calls[2].Input.OfInputItemList), "tool not found")
	})

	t.Run("siblings each receive remaining depth", func(t *testing.T) {
		remaining := 1
		mock := &mockResponsesAPI{responses: []*responses.Response{
			responseWithFunctionCalls("parent-tool", []responses.ResponseFunctionToolCall{
				{ID: "tool-1", CallID: "call-1", Name: "task", Arguments: `{"description":"Review first","prompt":"look","subagent_type":"review"}`},
				{ID: "tool-2", CallID: "call-2", Name: "task", Arguments: `{"description":"Review second","prompt":"look","subagent_type":"review"}`},
			}),
			responseWithMessage("child-first", "child one"),
			responseWithMessage("child-second", "child two"),
			responseWithMessage("parent-final", "parent done"),
		}}
		factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
			"review": {Name: "review"},
		}})
		factory.recursionRemaining = &remaining
		looper := &looper{
			Client:            mock,
			Permissions:       PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "review", Action: permissionAllow}}}}},
			Tools:             map[string]looperTool{"task": factory.taskTool()},
			ParallelToolCalls: 1,
		}
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- PromptInput{Role: PromptInputRoleUser, Text: "start", Responses: output}

		close(input)

		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "parent done"}}, collectResponses(output))
		require.Len(t, mock.calls, 4)
	})
}

func TestLooperNumbersSiblingTaskDiagnostics(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{
		responseWithFunctionCalls("parent-tool", []responses.ResponseFunctionToolCall{
			{ID: "tool-1", CallID: "call-1", Name: "task", Arguments: `{"description":"Review first","prompt":"look","subagent_type":"review"}`},
			{ID: "tool-2", CallID: "call-2", Name: "task", Arguments: `{"description":"Review second","prompt":"look","subagent_type":"review"}`},
		}),
		responseWithMessage("child-first", "child one"),
		responseWithMessage("child-second", "child two"),
		responseWithMessage("parent-final", "parent done"),
	}}
	factory := testTaskFactory(mock, Agents{Items: map[string]Agent{
		"review": {Name: "review"},
	}})
	factory.diagnostics = true
	looper := &looper{
		Client:            mock,
		Permissions:       PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "review", Action: permissionAllow}}}}},
		Tools:             map[string]looperTool{"task": factory.taskTool()},
		ParallelToolCalls: 1,
	}
	output := make(chan ChatResponse, 20)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "start", Responses: output}

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{
		{Kind: ChatResponseAssistantTool, Subagent: &SubagentDiagnostic{Name: "review", Label: "delegation", Index: 1, Total: 2, Text: "started: Review first"}},
		{Kind: ChatResponseAssistantTool, Subagent: &SubagentDiagnostic{Name: "review", Label: "assistant message", Index: 1, Total: 2, Text: "child one"}},
		{Kind: ChatResponseAssistantTool, Subagent: &SubagentDiagnostic{Name: "review", Label: "delegation", Index: 1, Total: 2, Text: "finished"}},
		{Kind: ChatResponseAssistantTool, Subagent: &SubagentDiagnostic{Name: "review", Label: "delegation", Index: 2, Total: 2, Text: "started: Review second"}},
		{Kind: ChatResponseAssistantTool, Subagent: &SubagentDiagnostic{Name: "review", Label: "assistant message", Index: 2, Total: 2, Text: "child two"}},
		{Kind: ChatResponseAssistantTool, Subagent: &SubagentDiagnostic{Name: "review", Label: "delegation", Index: 2, Total: 2, Text: "finished"}},
		{Kind: ChatResponseAssistantMessage, Text: "parent done"},
	}, collectResponses(output))
}

func testTaskFactory(client responsesAPI, agents Agents) *toolFactory {
	return &toolFactory{ //nolint:exhaustruct // Tests only need task-relevant dependencies.
		client: client,
		agents: agents,
		skills: Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil},
		baseTools: map[string]looperTool{
			"bash": {Permission: "bash"},
			"read": {Permission: "read"},
		},
	}
}

func testInterAgentFilter(t *testing.T, prompt string, permission PermissionSet) *interAgentFilter {
	t.Helper()

	parsed, err := template.New("test_filter").Parse(prompt)
	require.NoError(t, err)

	return &interAgentFilter{agent: Agent{Name: "guardrail", Permission: permission}, prompt: parsed}
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

	return &responses.Response{
		ID: id,
		Output: []responses.ResponseOutputItemUnion{
			{
				ID:   id + "-reasoning",
				Type: "reasoning",
				Summary: []responses.ResponseReasoningItemSummary{{
					Text: "thinking",
					Type: "summary_text",
				}},
			},
			{
				ID:     id + "-commentary",
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Phase:  "commentary",
				Content: []responses.ResponseOutputMessageContentUnion{{
					Type: "output_text",
					Text: "commentary",
				}},
			},
			{
				ID:     id + "-first",
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Content: []responses.ResponseOutputMessageContentUnion{{
					Type: "output_text",
					Text: "first",
				}},
			},
			{
				ID:     id + "-second",
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Content: []responses.ResponseOutputMessageContentUnion{{
					Type: "output_text",
					Text: "second",
				}},
			},
		},
	}
}
