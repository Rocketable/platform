package rocketcode

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"os"
	"slices"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"golang.org/x/sync/errgroup"
)

const (
	// askUserQuestionToolName is RocketClaw's tool that asks the human partner (bridge.go).
	askUserQuestionToolName = "ask_user_question"
	// taskTurnSuffix ends the turn IDs of task subagents, apart from the timestamp turn IDs of
	// guardrail and permission review runs saved under the same child key.
	taskTurnSuffix = "/task"
)

type taskParams struct {
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
	SubagentType string `json:"subagent_type"`
	Command      string `json:"command"`
	// Continue is the continue ID of a saved subagent; backgroundTool checks it is a direct child.
	Continue string `json:"continue"`
}

type guardrailDecision struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason"`
}

func (f *toolFactory) taskTool() looperTool {
	return f.backgroundTool(f.agent, BackgroundKindTask, &looperTool{
		Definition: *functionTool("task", f.taskDescription(), map[string]any{
			"description":   map[string]any{"type": "string"},
			"prompt":        map[string]any{"type": "string"},
			"subagent_type": map[string]any{"type": "string"},
			"command":       map[string]any{"type": "string"},
		}),
		Permission: "task",
		resumable:  true,
		Subjects: func(raw json.RawMessage) ([]string, error) {
			var params taskParams
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, fmt.Errorf("parse task params: %w", err)
			}

			return []string{params.SubagentType}, nil
		},
		Call: func(ctx context.Context, raw json.RawMessage, output chan<- ChatResponse, metadata toolCallMetadata) (ToolResult, error) {
			var params taskParams
			if err := json.Unmarshal(raw, &params); err != nil {
				return ToolResult{}, fmt.Errorf("parse task params: %w", err)
			}

			result, err := f.runTask(ctx, &params, metadata, output)
			if err != nil {
				return ToolResult{}, err
			}

			return TextToolResult(result), nil
		},
	})
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

func (f *toolFactory) childSystemPrompt(agent *Agent, modelTools, codeHosts map[string]looperTool) string {
	rootInstructions := ""
	if agent.Permission.evaluate("rocketclaw", "load_agents_md").Action == permissionAllow {
		rootInstructions = f.rootInstructions
	}

	rootInstructions = strings.TrimSpace(rootInstructions + "\n\n" + f.workspaceInstructions)

	var mcpServers []string
	if f.mcpRegistry != nil {
		mcpServers = visibleMCPServers(agent.Permission, f.mcpRegistry.Names())
	}

	return withCodeModeSystemPrompt(
		composeSystemPromptWithSkills(strings.TrimSpace(rootInstructions+"\n\n"+agent.Prompt), f.skills, agent),
		modelTools, codeHosts, mcpServers,
	)
}

// taskChildJournal journals a Task child under its parent call; outside a tool call there is nothing to resume.
func taskChildJournal(ctx context.Context) TracelessJournal {
	if parent, ok := toolCallContextFrom(ctx); ok {
		return TracelessJournal{Parent: parent.looper.Journal}
	}

	return TracelessJournal{Parent: InertJournal{}}
}

func (f *toolFactory) runTask(ctx context.Context, params *taskParams, metadata toolCallMetadata, parentOutput chan<- ChatResponse) (string, error) {
	if f.recursionRemaining != nil && *f.recursionRemaining == 0 {
		return "", errors.New("maxRecursion limit reached: task delegation is unavailable")
	}

	agent, ok := f.agents.Items[params.SubagentType]
	if !ok {
		return "", fmt.Errorf("unknown agent type: %s is not a valid agent type", params.SubagentType)
	}

	progress := *metadata.progress
	// The model stays absent until resolution; neither aliases nor parent attribution leak.
	progress.Kind, progress.Agent, progress.Model = PublicProgressDelegation, agent.Name, ""
	progress.SubagentKey = cmp.Or(params.Continue, f.childKey+"/"+delegationName(metadata.callID, ToolCallKey(ctx)))

	progress.State = PublicProgressReview
	if err := metadata.observations.observe(ctx, &progress); err != nil {
		return "", err
	}

	originatingAgent := ""
	if f.agent != nil {
		originatingAgent = f.agent.Name
	}

	if agent.Guardrail != "" && !f.inGuardrailRun {
		guardrailAgent := f.agents.Items[agent.Guardrail]
		message := strings.Join([]string{
			"Current Action: delegation",
			fmt.Sprintf("The agent %s wants to delegate to %s:", originatingAgent, agent.Name),
			params.Prompt,
		}, "\n")

		decision := f.runGuardrail(ctx, &guardrailAgent, ChildRunStageDelegation, message, agent.Name, progress.SubagentKey, metadata, parentOutput)
		if !decision.Approved {
			// Cancellation retains the canonical result; the parent records stopped.
			if ctx.Err() == nil {
				progress.State = PublicProgressBlocked
				if err := metadata.observations.observe(ctx, &progress); err != nil {
					return "", err
				}
			}

			reason := cmp.Or(strings.TrimSpace(decision.Reason), "rejected by inter-agent guardrail")

			return strings.Join([]string{"<task_result>", "delegation blocked: " + reason, "</task_result>"}, "\n"), nil
		}
	}

	child, sessionIn, sessionOut, err := f.subagent(ctx, &agent, progress.SubagentKey, params.Continue != "", false)
	if err != nil {
		return "", err
	}

	progress.Model = child.DisplayModel

	progress.State = PublicProgressWorking
	if err := metadata.observations.observe(ctx, &progress); err != nil {
		return "", err
	}

	output := make(chan ChatResponse)

	input := make(chan PromptInput, 1)
	input <- PromptInput{TurnID: ToolCallKey(ctx) + taskTurnSuffix, Role: PromptInputRoleUser, Text: params.Prompt, Responses: output}

	close(input)

	var (
		group            errgroup.Group
		last             string
		childDiagnostics []ChatResponse
	)

	if f.diagnostics {
		emitSubagentDiagnostic(parentOutput, &SubagentDiagnostic{
			Name:  agent.Name,
			Label: "delegation",
			Index: metadata.subagentIndex,
			Total: metadata.subagentTotal,
			Text:  "started: " + params.Description,
		})
	}

	group.Go(func() error {
		for item := range output {
			if item.Kind == ChatResponseAssistantMessage {
				last = item.Text
			}

			if f.diagnostics {
				childDiagnostics = append(childDiagnostics, ChatResponse{Kind: ChatResponseAssistantTool, Subagent: &SubagentDiagnostic{
					Name:     agent.Name,
					Label:    nestedDiagnosticLabel(item),
					Index:    metadata.subagentIndex,
					Total:    metadata.subagentTotal,
					Text:     item.Text,
					Tool:     item.Tool,
					Subagent: item.Subagent,
					Provider: item.Provider,
				}})
			}
		}

		return nil
	})

	interrupts := make(chan os.Signal, 1)
	err = child.Loop(ctx, input, sessionIn, sessionOut, interrupts)

	if errWait := group.Wait(); errWait != nil {
		return "", fmt.Errorf("collect task output: %w", errWait)
	}

	if err != nil {
		return "", err
	}

	if agent.Guardrail != "" && !f.inGuardrailRun {
		progress.State = PublicProgressReview
		if err := metadata.observations.observe(ctx, &progress); err != nil {
			return "", err
		}

		if blocked, rejected := f.guardResponse(ctx, &agent, originatingAgent, params.Prompt, last, progress.SubagentKey, metadata, parentOutput); rejected {
			if ctx.Err() == nil {
				progress.State = PublicProgressBlocked
				if err := metadata.observations.observe(ctx, &progress); err != nil {
					return "", err
				}
			}

			return blocked, nil
		}
	}

	if f.diagnostics {
		for _, diagnostic := range childDiagnostics {
			emitDiagnosticChatResponse(parentOutput, diagnostic)
		}

		emitSubagentDiagnostic(parentOutput, &SubagentDiagnostic{
			Name:  agent.Name,
			Label: "delegation",
			Index: metadata.subagentIndex,
			Total: metadata.subagentTotal,
			Text:  "finished",
		})
	}

	return strings.Join([]string{"<task_result>", last, "</task_result>"}, "\n"), nil
}

// guardResponse runs agent's response-stage guardrail on the answer last to the prompt
// originatingAgent delegated, and returns the blocked task result when the guardrail rejects it.
func (f *toolFactory) guardResponse(ctx context.Context, agent *Agent, originatingAgent, prompt, last, subagentKey string, metadata toolCallMetadata, parentOutput chan<- ChatResponse) (blocked string, rejected bool) {
	guardrailAgent := f.agents.Items[agent.Guardrail]
	message := strings.Join([]string{
		"Current Action: response",
		fmt.Sprintf("The agent %s wants to delegate to %s:", originatingAgent, agent.Name),
		prompt,
		"",
		fmt.Sprintf("And the response from %s to %s:", agent.Name, originatingAgent),
		last,
	}, "\n")

	decision := f.runGuardrail(ctx, &guardrailAgent, ChildRunStageResponse, message, agent.Name, subagentKey, metadata, parentOutput)
	if decision.Approved {
		return "", false
	}

	reason := cmp.Or(strings.TrimSpace(decision.Reason), "rejected by inter-agent guardrail")

	return strings.Join([]string{"<task_result>", "delegation response blocked: " + reason, "</task_result>"}, "\n"), true
}

// delegationName is the child-session key segment of a Delegation History a call starts: the
// subagent of a new task call, or a call's permission review. Like OpenCode's per-subagent session
// ID, it stays unique when a provider reuses a call ID in a later turn: it adds to name a hash of
// the call's tool call key, which a resumed turn keeps.
func delegationName(name, callKey string) string {
	sum := sha256.Sum256([]byte(callKey))
	return name + "-" + hex.EncodeToString(sum[:4])
}

// ReviewKey is the child-session key segment of every automatic permission review of call callID
// in turn turnID, before the call runs or during it, such as of a Code Mode script's tool calls.
func ReviewKey(turnID, callID string) string {
	return delegationName(callID+"-review", turnID+"/call/"+callID)
}

// subagent rebuilds the looper of the subagent saved at childKey the way task first built it,
// with the turns it saved so far when continued. Only a resumed turn may continue a
// subagent that saved none: it is the subagent's first.
func (f *toolFactory) subagent(ctx context.Context, base *Agent, childKey string, continued, resumed bool) (child *looper, sessionIn iter.Seq2[SessionEntry, error], sessionOut func(SessionEntry) error, err error) {
	agent := *base

	var history []SessionEntry

	if continued {
		saved, errLoad := f.childSessions.ChildEntries(ctx, childKey)
		if errLoad != nil {
			return nil, nil, nil, fmt.Errorf("load subagent history: %w", errLoad)
		}

		// Guardrail and permission review runs save under the same key with their own turn IDs.
		history = slices.DeleteFunc(saved, func(entry SessionEntry) bool { return !strings.HasSuffix(entry.TurnID, taskTurnSuffix) })
		if len(history) == 0 && !resumed {
			return nil, nil, nil, fmt.Errorf(continueWithoutTurn, childKey)
		}

		// Another agent type would continue that subagent outside its own prompt and guardrail.
		if i := slices.IndexFunc(history, func(entry SessionEntry) bool { return entry.Agent != base.Name }); i >= 0 {
			return nil, nil, nil, fmt.Errorf(continueOtherAgent, childKey, history[i].Agent)
		}
	}

	agent.Permission = f.shellTemp.effectivePermissions(agent.Permission)
	expandAgentPrompt(ctx, &agent, f.expandPromptShellCommands.SubagentPrompts, &f.promptExpansion)

	childFactory := *f
	childFactory.childKey = childKey
	// Only the root agent asks the human partner.
	childFactory.baseTools = maps.Clone(f.baseTools)
	delete(childFactory.baseTools, askUserQuestionToolName)

	if f.recursionRemaining != nil {
		// A woken subagent can sit several levels below the factory that runs it.
		remaining := max(*f.recursionRemaining-strings.Count(childKey, "/")+strings.Count(f.childKey, "/"), 0)
		childFactory.recursionRemaining = &remaining
	}

	client, origin, err := resolveModel(f.resolver, agent.Model)
	if err != nil {
		return nil, nil, nil, err
	}

	modelTools, codeHosts := childFactory.assembleTools(&agent)
	child = &looper{
		agent:                  agent,
		ProviderOrigin:         origin,
		Client:                 newResponsesAPI(client),
		SystemPrompt:           childFactory.childSystemPrompt(&agent, modelTools, codeHosts),
		Model:                  origin.Model,
		DisplayModel:           origin.displayModel(),
		ReasoningEffort:        shared.ReasoningEffort(cmp.Or(agent.ReasoningEffort, string(f.reasoningEffort))),
		Verbosity:              agent.Verbosity,
		CompactThreshold:       cmp.Or(origin.CompactThreshold, f.compactThreshold),
		CompactionSteering:     f.compactionSteering,
		ParallelToolCalls:      f.parallelToolCalls,
		ResponseFormat:         agentOutputResponseFormat(agent.OutputSchema),
		Permissions:            agent.Permission,
		Tools:                  modelTools,
		CodeModeHosts:          codeHosts,
		Diagnostics:            f.diagnostics,
		AutoApprovePermissions: f.autoApprovePermissions,
		PermissionReviewer:     &childFactory,
		Observability:          f.observability,
		Journal:                taskChildJournal(ctx),
		notes:                  f.backgroundJobs,
		subagentKey:            childKey,
	}
	childFactory.configureSpill(child)

	sessionIn = func(yield func(SessionEntry, error) bool) {
		for i := range history {
			if !yield(history[i], nil) {
				return
			}
		}
	}

	return child, sessionIn, childFactory.childSessionOut(ctx), nil
}

// ContinueSubagent runs input as a new turn of the subagent saved at subagentKey (a
// BackgroundJob.SubagentKey), as agentName, journaled under jobID, and returns its final answer.
// The turn joins the subagent's saved history and never reaches its parent. Without input it
// instead resumes, as the agent that ran it, the turn journaled under jobID before a restart, which
// agentName delegated; its answer reaches agentName, so the subagent's guardrail reviews it as task does.
func (r *Runtime) ContinueSubagent(ctx context.Context, jobID, subagentKey, agentName, input string) (string, error) {
	f := r.PermissionReviewer.(*toolFactory) // NewWithModelResolver installs its tool factory as the reviewer.
	turnID := jobID + taskTurnSuffix

	var step turnStep

	resumed, originatingAgent := input == "", agentName
	if resumed {
		found, err := loadStep(ctx, r.Journal, turnID, &step)
		if err != nil {
			return "", err
		}

		if !found {
			return "", fmt.Errorf(resumeWithoutTurn, subagentKey)
		}

		// The journaled turn replaces the input, which Loop only needs to be non-empty.
		agentName, input = step.Record.Agent, turnID
	}

	agent, ok := f.agents.Items[agentName]
	if !ok {
		return "", fmt.Errorf("unknown agent type: %s is not a valid agent type", agentName)
	}

	child, sessionIn, sessionOut, err := f.subagent(ctx, &agent, subagentKey, true, resumed)
	if err != nil {
		return "", err
	}

	child.Journal = TracelessJournal{Parent: r.Journal}
	output := make(chan ChatResponse)

	inputs := make(chan PromptInput, 1)
	inputs <- PromptInput{TurnID: turnID, Role: PromptInputRoleUser, Text: input, Responses: output}

	close(inputs)

	var (
		group errgroup.Group
		last  string
	)

	group.Go(func() error {
		for item := range output {
			if item.Kind == ChatResponseAssistantMessage {
				last = item.Text
			}
		}

		return nil
	})

	err = child.Loop(ctx, inputs, sessionIn, sessionOut, make(chan os.Signal, 1))
	_ = group.Wait() // The collector cannot fail.

	if err != nil || !resumed {
		return last, err
	}

	// A resumed task answers its parent's Completion Note the way the task call would have.
	if agent.Guardrail != "" {
		items, err := ReplayInputToParams(step.Record.ReplayInput[:1])
		if err != nil {
			return "", err
		}

		// The turn starts with the task prompt, unless a context overflow recovery compacted it away.
		prompt := ""
		if message := items[0].OfMessage; message != nil {
			prompt = message.Content.OfString.Value
		}

		if blocked, rejected := f.guardResponse(ctx, &agent, originatingAgent, prompt, last, subagentKey, toolCallMetadata{}, nil); rejected {
			return blocked, nil
		}
	}

	return strings.Join([]string{"<task_result>", last, "</task_result>"}, "\n"), nil
}

func (f *toolFactory) childSession(yield func(SessionEntry, error) bool) {
	for i := range f.childContext {
		if !yield(f.childContext[i], nil) {
			return
		}
	}
}

func (f *toolFactory) childSessionOut(ctx context.Context) func(SessionEntry) error {
	return func(entry SessionEntry) error {
		if err := f.childSessions.AppendChildEntry(ctx, f.childKey, &entry); err != nil {
			return fmt.Errorf("append child session entry: %w", err)
		}

		return nil
	}
}

// runGuardrail saves its run under subagentKey, the key of the subagent the task call runs.
func (f *toolFactory) runGuardrail(ctx context.Context, guardrail *Agent, stage ChildRunStage, message, guardedAgent, subagentKey string, metadata toolCallMetadata, parentOutput chan<- ChatResponse) guardrailDecision {
	agent := *guardrail
	agent.Permission = f.shellTemp.effectivePermissions(agent.Permission)
	expandAgentPrompt(ctx, &agent, f.expandPromptShellCommands.SubagentPrompts, &f.promptExpansion)

	responseFormat := guardrailResponseFormat()

	client, origin, err := resolveModel(f.resolver, agent.Model)
	if err != nil {
		return guardrailDecision{Approved: false, Reason: "inter-agent guardrail model failed: " + err.Error()}
	}

	childFactory := *f
	childFactory.inGuardrailRun = true
	childFactory.childKey = subagentKey

	modelTools, codeHosts := childFactory.assembleTools(&agent)
	child := &looper{
		agent:                  agent,
		ProviderOrigin:         origin,
		Client:                 newResponsesAPI(client),
		SystemPrompt:           childFactory.childSystemPrompt(&agent, modelTools, codeHosts),
		Model:                  origin.Model,
		DisplayModel:           origin.displayModel(),
		ReasoningEffort:        shared.ReasoningEffort(cmp.Or(agent.ReasoningEffort, string(f.reasoningEffort))),
		Verbosity:              agent.Verbosity,
		CompactThreshold:       cmp.Or(origin.CompactThreshold, f.compactThreshold),
		CompactionSteering:     f.compactionSteering,
		ParallelToolCalls:      f.parallelToolCalls,
		ResponseFormat:         responseFormat,
		Permissions:            agent.Permission,
		Tools:                  modelTools,
		CodeModeHosts:          codeHosts,
		Diagnostics:            f.diagnostics,
		AutoApprovePermissions: f.autoApprovePermissions,
		PermissionReviewer:     &childFactory,
		InPermissionReview:     f.inPermissionReview,
		Observability:          f.observability,
		Journal:                InertJournal{},
		notes:                  InertBackgroundJobs{},
	}
	childFactory.configureSpill(child)

	output := make(chan ChatResponse)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: message, Responses: output}

	close(input)

	var (
		group errgroup.Group
		last  string
	)

	group.Go(func() error {
		for item := range output {
			if f.diagnostics {
				emitGuardrailDiagnostic(parentOutput, guardedAgent, agent.Name, stage, metadata, item)
			}

			if item.Kind == ChatResponseAssistantMessage {
				last = item.Text
			}
		}

		return nil
	})

	if err := child.Loop(ctx, input, f.childSession, childFactory.childSessionOut(ctx), make(chan os.Signal, 1)); err != nil {
		_ = group.Wait()
		return guardrailDecision{Approved: false, Reason: "inter-agent guardrail failed: " + err.Error()}
	}

	if err := group.Wait(); err != nil {
		return guardrailDecision{Approved: false, Reason: "inter-agent guardrail failed: " + err.Error()}
	}

	var decision guardrailDecision
	if err := json.Unmarshal([]byte(last), &decision); err != nil {
		return guardrailDecision{Approved: false, Reason: "inter-agent guardrail returned invalid JSON"}
	}

	if f.diagnostics {
		emitGuardrailResult(parentOutput, guardedAgent, agent.Name, stage, metadata, decision)
	}

	return decision
}

func emitGuardrailDiagnostic(output chan<- ChatResponse, guardedAgent, guardrailAgent string, stage ChildRunStage, metadata toolCallMetadata, item ChatResponse) {
	if item.Kind == ChatResponseAssistantMessage {
		return
	}

	emitSubagentDiagnostic(output, &SubagentDiagnostic{
		Name:  guardedAgent,
		Index: metadata.subagentIndex,
		Total: metadata.subagentTotal,
		Subagent: &SubagentDiagnostic{
			Name:  guardrailAgent,
			Label: "guardrail(" + string(stage) + ")",
			Subagent: &SubagentDiagnostic{
				Label:    nestedDiagnosticLabel(item),
				Text:     item.Text,
				Tool:     item.Tool,
				Subagent: item.Subagent,
				Provider: item.Provider,
			},
		},
	})
}

func emitGuardrailResult(output chan<- ChatResponse, guardedAgent, guardrailAgent string, stage ChildRunStage, metadata toolCallMetadata, decision guardrailDecision) {
	action := "reject"
	if decision.Approved {
		action = "approve"
	}

	text := action
	if reason := strings.TrimSpace(decision.Reason); reason != "" {
		text += ": " + reason
	}

	emitSubagentDiagnostic(output, &SubagentDiagnostic{
		Name:  guardedAgent,
		Index: metadata.subagentIndex,
		Total: metadata.subagentTotal,
		Subagent: &SubagentDiagnostic{
			Name:  guardrailAgent,
			Label: "guardrail(" + string(stage) + ")",
			Text:  text,
			Subagent: &SubagentDiagnostic{
				Label: "result",
			},
		},
	})
}

func nestedDiagnosticLabel(item ChatResponse) string {
	if item.Subagent != nil {
		return ""
	}

	return subagentResponseLabel(item.Kind)
}

func emitSubagentDiagnostic(output chan<- ChatResponse, diagnostic *SubagentDiagnostic) {
	emitDiagnosticChatResponse(output, ChatResponse{Kind: ChatResponseAssistantTool, Subagent: diagnostic})
}

func guardrailResponseFormat() responses.ResponseFormatTextConfigUnionParam {
	var jsonSchema responses.ResponseFormatTextJSONSchemaConfigParam

	jsonSchema.Name = "guardrail_decision"
	jsonSchema.Strict = openai.Bool(true)
	jsonSchema.Schema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"approved": map[string]any{"type": "boolean"},
			"reason":   map[string]any{"type": "string"},
		},
		"required":             []string{"approved", "reason"},
		"additionalProperties": false,
	}

	var responseFormat responses.ResponseFormatTextConfigUnionParam

	responseFormat.OfJSONSchema = &jsonSchema

	return responseFormat
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
