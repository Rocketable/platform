//nolint:exhaustruct // Tool definitions use sparse SDK and runtime structs.
package rocketcode

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/openai/openai-go/v3/shared"
	"golang.org/x/sync/errgroup"
)

type taskParams struct {
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
	SubagentType string `json:"subagent_type"`
	Command      string `json:"command"`
}

func (f *toolFactory) taskTool() looperTool {
	return looperTool{
		Definition: functionTool("task", f.taskDescription(), map[string]any{
			"description":   map[string]any{"type": "string"},
			"prompt":        map[string]any{"type": "string"},
			"subagent_type": map[string]any{"type": "string"},
			"command":       map[string]any{"type": "string"},
		}),
		Permission:         "task",
		VisibilitySubjects: nil,
		Subjects: func(raw json.RawMessage) ([]string, error) {
			var params taskParams
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, fmt.Errorf("parse task params: %w", err)
			}

			return []string{params.SubagentType}, nil
		},
		Call: func(ctx context.Context, raw json.RawMessage, output chan<- ChatResponse) (ToolResult, error) {
			var params taskParams
			if err := json.Unmarshal(raw, &params); err != nil {
				return ToolResult{}, fmt.Errorf("parse task params: %w", err)
			}

			result, err := f.runTask(ctx, params, output)
			if err != nil {
				return ToolResult{}, err
			}

			return textToolResult(result), nil
		},
	}
}

func (f *toolFactory) taskDescription() string {
	return strings.Join([]string{
		"Launch a new agent to handle complex, multistep tasks autonomously.",
		"",
		"When using the Task tool, you must specify a subagent_type parameter to select which agent type to use.",
		"",
		"When to use the Task tool:",
		"- When you are instructed to execute custom slash commands. Use the Task tool with the slash command invocation as the entire prompt. The slash command can take arguments. For example: Task(description=\"Check the file\", prompt=\"/check-file path/to/file.py\")",
		"",
		"When NOT to use the Task tool:",
		"- If you want to read a specific file path, use the Read or Glob tool instead of the Task tool, to find the match more quickly",
		"- If you are searching for a specific class definition like \"class Foo\", use the Glob tool instead, to find the match more quickly",
		"- If you are searching for code within a specific file or set of 2-3 files, use the Read tool instead of the Task tool, to find the match more quickly",
		"- Other tasks that are not related to the agent descriptions above",
		"",
		"Usage notes:",
		"1. Launch multiple agents concurrently whenever possible, to maximize performance; to do that, use a single message with multiple tool uses",
		"2. When the agent is done, it will return a single message back to you. The result returned by the agent is not visible to the user. To show the user the result, you should send a text message back to the user with a concise summary of the result.",
		"3. Each agent invocation starts with a fresh context. Your prompt should contain a highly detailed task description for the agent to perform autonomously and you should specify exactly what information the agent should return back to you in its final and only message to you.",
		"4. The agent's outputs should generally be trusted",
		"5. Clearly tell the agent whether you expect it to write code or just to do research (search, file reads, web fetches, etc.), since it is not aware of the user's intent. Tell it how to verify its work if possible (e.g. relevant test commands).",
		"6. If the agent description mentions that it should be used proactively, then you should try your best to use it without the user having to ask for it first. Use your judgement.",
		"",
		"Example usage (NOTE: The agents below are fictional examples for illustration only - use the actual agents listed above):",
		"",
		"<example_agent_descriptions>",
		"\"code-reviewer\": use this agent after you are done writing a significant piece of code",
		"\"greeting-responder\": use this agent when to respond to user greetings with a friendly joke",
		"</example_agent_description>",
		"",
		"<example>",
		"user: \"Please write a function that checks if a number is prime\"",
		"assistant: Sure let me write a function that checks if a number is prime",
		"assistant: First let me use the Write tool to write a function that checks if a number is prime",
		"assistant: I'm going to use the Write tool to write the following code:",
		"<code>",
		"function isPrime(n) {",
		"  if (n <= 1) return false",
		"  for (let i = 2; i * i <= n; i++) {",
		"    if (n % i === 0) return false",
		"  }",
		"  return true",
		"}",
		"</code>",
		"<commentary>",
		"Since a significant piece of code was written and the task was completed, now use the code-reviewer agent to review the code",
		"</commentary>",
		"assistant: Now let me use the code-reviewer agent to review the code",
		"assistant: Uses the Task tool to launch the code-reviewer agent",
		"</example>",
		"",
		"<example>",
		"user: \"Hello\"",
		"<commentary>",
		"Since the user is greeting, use the greeting-responder agent to respond with a friendly joke",
		"</commentary>",
		"assistant: \"I'm going to use the Task tool to launch the with the greeting-responder agent\"",
		"</example>",
		"",
		f.availableSubagentsDescription(),
	}, "\n")
}

func (f *toolFactory) availableSubagentsDescription() string {
	names := make([]string, 0, len(f.agents.Items))
	for name := range f.agents.Items {
		if f.agent == nil || f.agent.Permission.evaluate("task", name).Action != permissionAllow {
			continue
		}

		names = append(names, name)
	}

	slices.Sort(names)

	if len(names) == 0 {
		return "Available agent types and the tools they have access to:\nNo agents are currently available."
	}

	lines := []string{"Available agent types and the tools they have access to:"}

	for _, name := range names {
		agent := f.agents.Items[name]

		description := agent.Description
		if description == "" {
			description = "This subagent should only be called manually by the user."
		}

		lines = append(lines, fmt.Sprintf("- %s: %s", name, description))
	}

	return strings.Join(lines, "\n")
}

func (f *toolFactory) runTask(ctx context.Context, params taskParams, parentOutput ...chan<- ChatResponse) (string, error) {
	agent, ok := f.agents.Items[params.SubagentType]
	if !ok {
		return "", fmt.Errorf("unknown agent type: %s is not a valid agent type", params.SubagentType)
	}

	agent.Permission = f.shellOutput.effectivePermissions(agent.Permission)
	expandAgentPrompt(ctx, &agent, f.expandPromptShellCommands.SubagentPrompts, f.promptExpansion)
	systemPrompt := composeSystemPromptWithSkills(strings.TrimSpace(f.systemPrompt+"\n\n"+agent.Prompt), f.skills, &agent)
	child := &looper{ //nolint:exhaustruct // Child tasks intentionally inherit only runtime execution fields.
		agent:              agent,
		Client:             f.client,
		SystemPrompt:       systemPrompt,
		Model:              parseAgentModel(agent.Model, f.model),
		ReasoningEffort:    shared.ReasoningEffort(cmp.Or(agent.ReasoningEffort, string(f.reasoningEffort))),
		Verbosity:          agent.Verbosity,
		CompactThreshold:   f.compactThreshold,
		CompactionSteering: f.compactionSteering,
		Permissions:        agent.Permission,
		Tools:              f.toolsFor(&agent),
		Diagnostics:        f.diagnostics,
	}

	output := make(chan ChatResponse)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: params.Prompt, Responses: output}

	close(input)

	var (
		group errgroup.Group
		items []ChatResponse
	)

	var outputSink chan<- ChatResponse
	if len(parentOutput) > 0 {
		outputSink = parentOutput[0]
	}

	if f.diagnostics {
		emitSubagentDiagnostic(outputSink, SubagentDiagnostic{Name: agent.Name, Label: "delegation", Text: "started: " + params.Description})
	}

	group.Go(func() error {
		for item := range output {
			items = append(items, item)
			if f.diagnostics {
				emitSubagentDiagnostic(outputSink, SubagentDiagnostic{
					Name:     agent.Name,
					Label:    subagentResponseLabel(item.Kind),
					Text:     item.Text,
					Tool:     item.Tool,
					Subagent: item.Subagent,
					Provider: item.Provider,
				})
			}
		}

		return nil
	})

	interrupts := make(chan os.Signal, 1)
	err := child.Loop(ctx, input, func(func(SessionEntry, error) bool) {}, func(SessionEntry) error { return nil }, interrupts)

	if errWait := group.Wait(); errWait != nil {
		return "", fmt.Errorf("collect task output: %w", errWait)
	}

	if err != nil {
		return "", err
	}

	if f.diagnostics {
		emitSubagentDiagnostic(outputSink, SubagentDiagnostic{Name: agent.Name, Label: "delegation", Text: "finished"})
	}

	last := ""

	for _, item := range items {
		if item.Kind == ChatResponseAssistantMessage {
			last = item.Text
		}
	}

	return strings.Join([]string{"<task_result>", last, "</task_result>"}, "\n"), nil
}

func emitSubagentDiagnostic(output chan<- ChatResponse, diagnostic SubagentDiagnostic) {
	emitDiagnosticChatResponse(output, ChatResponse{Kind: ChatResponseAssistantTool, Subagent: &diagnostic})
}

func subagentResponseLabel(kind string) string {
	switch kind {
	case ChatResponseAssistantMessage:
		return "assistant message"
	case ChatResponseAssistantCommentary:
		return "assistant commentary"
	case ChatResponseAssistantTool:
		return "assistant tool"
	case ChatResponseReasoningSummary:
		return "reasoning summary"
	default:
		return "output"
	}
}
