package harness

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"slices"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"golang.org/x/sync/errgroup"
)

// guardrailEntryType marks the guardrail runs saved in a subagent session, which its turns do not read.
const guardrailEntryType = "guardrail"

type taskParams struct {
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
	SubagentType string `json:"subagent_type"`
	Command      string `json:"command"`
	// Continue names a saved subagent; backgroundTool checks it is a direct child.
	Continue taskContinue `json:"continue"`
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

// childSystemPrompt puts the instructions a guardrail agent may load ahead of its prompt; a session's
// agent has its prompt first instead (sessionSystemPrompt).
func (f *toolFactory) childSystemPrompt(agent *Agent) string {
	rootInstructions := ""
	if agent.Permission.evaluate("rocketclaw", "load_agents_md").Action == permissionAllow {
		rootInstructions = f.rootInstructions
	}

	return strings.TrimSpace(strings.TrimSpace(rootInstructions+"\n\n"+f.workspaceInstructions) + "\n\n" + agent.Prompt)
}

// sessionSystemPrompt expands the prompt of agent, which runs a session, and follows it with the
// instructions agent may load and the workspace.
func (f *toolFactory) sessionSystemPrompt(ctx context.Context, agent *Agent) string {
	expandAgentPrompt(ctx, agent, f.expandPromptShellCommands.PrimaryPrompts, &f.promptExpansion)

	rootInstructions := ""
	if agent.Permission.evaluate("rocketclaw", "load_agents_md").Action == permissionAllow {
		rootInstructions = f.rootInstructions
	}

	return strings.TrimSpace(agent.Prompt + "\n\n" + strings.TrimSpace(rootInstructions+"\n\n"+f.workspaceInstructions))
}

// runTask delegates params to a subagent session: a new one, which it writes the prompt to, or the
// continued one backgroundTool already wrote it to. It answers once that session's turn ends.
func (f *toolFactory) runTask(ctx context.Context, params *taskParams, metadata toolCallMetadata, parentOutput chan<- ChatResponse) (string, error) {
	if f.recursionRemaining != nil && *f.recursionRemaining == 0 {
		return "", errors.New("maxRecursion limit reached: task delegation is unavailable")
	}

	agent, ok := f.agents.Items[params.SubagentType]
	if !ok {
		return "", fmt.Errorf(unknownAgentType, params.SubagentType)
	}

	progress := *metadata.progress
	// The model stays absent until the turn ends; neither aliases nor parent attribution leak.
	progress.Kind, progress.Agent, progress.Model = PublicProgressDelegation, agent.Name, ""
	// The row names the subagent by its key under the history that holds the call.
	key := ChildKey{Path: "/" + delegationName(metadata.callID, ToolCallKey(ctx)), Turn: metadata.observations.turnID, Call: metadata.callID}
	if id := params.Continue.SessionID; id != "" {
		key.Path = strings.TrimPrefix(id, f.childKey.Path) // backgroundTool checked that it names a child of this session.
	}

	progress.SubagentKey = key.Path
	key = key.under(f.childKey.Path)

	progress.State = PublicProgressReview
	if err := metadata.observations.observe(ctx, &progress); err != nil {
		return "", err
	}

	originatingAgent := ""
	if f.agent != nil {
		originatingAgent = f.agent.Name
	}

	if params.Continue.SessionID == "" {
		if blocked, err := f.guardTask(ctx, &agent, key, ChildRunStageDelegation, originatingAgent, params.Prompt, "", &progress, metadata, parentOutput); err != nil || blocked != "" {
			return blocked, err
		}

		if _, err := f.childSessions.WriteSubagent(ctx, &SubagentWrite{Key: key, CallKey: ToolCallKey(ctx), Agent: agent.Name, Delegator: originatingAgent, Prompt: params.Prompt}); err != nil {
			return "", fmt.Errorf("write subagent: %w", err)
		}
	}

	progress.State = PublicProgressWorking
	if err := metadata.observations.observe(ctx, &progress); err != nil {
		return "", err
	}

	return f.awaitTask(ctx, originatingAgent, params.Prompt, ToolCallKey(ctx), key.Path, &progress, metadata, parentOutput)
}

// awaitTask answers the task call callKey by which originatingAgent delegated prompt to the subagent
// session at key, once that session's turn running it ends and the response guardrail of the
// type the turn ended as, which a steer may have switched it to, approves its answer.
func (f *toolFactory) awaitTask(ctx context.Context, originatingAgent, prompt, callKey, key string, progress *PublicProgress, metadata toolCallMetadata, parentOutput chan<- ChatResponse) (string, error) {
	last, final, err := f.childSessions.AwaitSubagent(ctx, key, callKey)
	if err != nil {
		return "", fmt.Errorf("await subagent: %w", err)
	}

	progress.Agent, progress.Model = final.Agent, final.Model
	if err := metadata.observations.observe(ctx, progress); err != nil {
		return "", err
	}

	agent := f.agents.Items[final.Agent]

	return f.taskResult(ctx, &agent, ChildKey{Path: key}, originatingAgent, prompt, last, progress, metadata, parentOutput)
}

// taskResult is the task result of last, the answer of agent's subagent session at key to originatingAgent's
// prompt, once agent's response guardrail approves it, or the blocked result.
func (f *toolFactory) taskResult(ctx context.Context, agent *Agent, key ChildKey, originatingAgent, prompt, last string, progress *PublicProgress, metadata toolCallMetadata, parentOutput chan<- ChatResponse) (string, error) {
	if blocked, err := f.guardTask(ctx, agent, key, ChildRunStageResponse, originatingAgent, prompt, last, progress, metadata, parentOutput); err != nil || blocked != "" {
		return blocked, err
	}

	return strings.Join([]string{"<task_result>", last, "</task_result>"}, "\n"), nil
}

// guardTask runs agent's guardrail, if any, at stage of the task by which originatingAgent delegated
// prompt to the subagent progress names, reviewing at the response stage the subagent's answer last
// too, and returns the blocked task result when the guardrail rejects it.
func (f *toolFactory) guardTask(ctx context.Context, agent *Agent, key ChildKey, stage ChildRunStage, originatingAgent, prompt, last string, progress *PublicProgress, metadata toolCallMetadata, parentOutput chan<- ChatResponse) (blocked string, err error) {
	if agent.Guardrail == "" || f.inGuardrailRun {
		return "", nil
	}

	progress.State = PublicProgressReview
	if err := metadata.observations.observe(ctx, progress); err != nil {
		return "", err
	}

	lines, outcome := []string{"Current Action: " + string(stage), fmt.Sprintf("The agent %s wants to delegate to %s:", originatingAgent, agent.Name), prompt}, "delegation blocked: "
	if stage == ChildRunStageResponse {
		lines, outcome = append(lines, "", fmt.Sprintf("And the response from %s to %s:", agent.Name, originatingAgent), last), "delegation response blocked: "
	}

	guardrailAgent := f.agents.Items[agent.Guardrail]

	decision := f.runGuardrail(ctx, &guardrailAgent, stage, strings.Join(lines, "\n"), agent.Name, key, metadata, parentOutput)
	if decision.Approved {
		return "", nil
	}

	// Cancellation retains the canonical result; the parent records stopped.
	if ctx.Err() == nil {
		progress.State = PublicProgressBlocked
		if err := metadata.observations.observe(ctx, progress); err != nil {
			return "", err
		}
	}

	reason := cmp.Or(strings.TrimSpace(decision.Reason), "rejected by inter-agent guardrail")

	return strings.Join([]string{"<task_result>", outcome + reason, "</task_result>"}, "\n"), nil
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
	return delegationName(callID+"-review", CallKey(turnID, callID))
}

// ReviewTurn is the turn under which the automatic permission review of the call keyed callKey (see
// ToolCallKey) saves its answer in the review's Delegation History. It has no slash, so the review's
// retained results never nest under another turn's.
func ReviewTurn(callKey string) string {
	return delegationName("review", callKey)
}

// asRecordAgent returns l, or, once a steer switched the turn record to another agent type, a looper
// that runs as that type for the turn's remaining steps, as OpenCode packages/core/src/session/context.ts
// selects the session's agent at every step. l stays as it was for the jobs its calls left running. When
// the type runs on another provider than l, the turn's items and the history before them, which l's input
// holds, are converted for it as loadSession and openTurn convert saved turns.
func (l *looper) asRecordAgent(ctx context.Context, record *SessionEntry, history *[]responses.ResponseInputItemUnionParam, attribution *[]ReplayAttribution, turnItems *[]responses.ResponseInputItemUnionParam) (*looper, error) {
	if record.Agent == l.agent.Name || record.Agent == "" { // A turn journaled before records named their agent keeps l.
		return l, nil
	}

	f := l.PermissionReviewer.(*toolFactory) // newLooper installs its factory as the reviewer.

	agent, ok := f.agents.Items[record.Agent]
	if !ok {
		return nil, fmt.Errorf(unknownAgentType, record.Agent)
	}

	agent.Permission = f.shellTemp.effectivePermissions(agent.Permission)

	next, err := f.newLooper(&agent, f.sessionSystemPrompt(ctx, &agent))
	if err != nil {
		return nil, err
	}

	next.Journal, next.observations, next.SteerDrain, next.expandInputPrompts = l.Journal, l.observations, l.SteerDrain, l.expandInputPrompts
	next.restoreTurnExecuteResults(record.TurnID)
	// The items the new type adds name its model, so a switch back converts them in turn.
	record.Model, record.ReasoningEffort = next.DisplayModel, new(string(next.ReasoningEffort))

	provider := providerForModel(next.DisplayModel)
	if provider == providerForModel(l.DisplayModel) {
		return next, nil
	}

	base := SessionEntry{ReplayAttribution: *attribution}
	if base.ReplayInput, err = ReplayInputFromParams(*history); err != nil {
		return nil, err
	}

	for _, entry := range []*SessionEntry{&base, record} {
		if err := entry.forProvider(provider); err != nil {
			return nil, fmt.Errorf("convert switched turn for provider %s: %w", provider, err)
		}
	}

	if *history, err = ReplayInputToParams(base.ReplayInput); err != nil {
		return nil, err
	}

	*attribution = base.ReplayAttribution

	if *turnItems, err = ReplayInputToParams(record.ReplayInput); err != nil {
		return nil, err
	}

	return next, nil
}

// AwaitTask answers, after a restart, the background task call jobID by which delegator delegated prompt
// to the subagent session at subagentKey, once that session's turn running it ends.
func (r *Runtime) AwaitTask(ctx context.Context, jobID, subagentKey, delegator, prompt string) (string, error) {
	// NewWithModelResolver installs its tool factory as the reviewer.
	return r.PermissionReviewer.(*toolFactory).awaitTask(ctx, delegator, prompt, jobID, subagentKey, &PublicProgress{}, toolCallMetadata{observations: &turnObservations{journal: InertJournal{}}}, nil)
}

// ReviewTask returns, as a task call would, the task result of answer, the final answer of a turn of r's
// subagent session that ran prompt and that no task call awaits, for delegator, its parent session's agent:
// once the response guardrail of r's agent, which saves its run in r's session, approves it.
func (r *Runtime) ReviewTask(ctx context.Context, delegator, prompt, answer string) (string, error) {
	f := r.PermissionReviewer.(*toolFactory)

	return f.taskResult(ctx, f.agent, ChildKey{}, delegator, prompt, answer, &PublicProgress{}, toolCallMetadata{observations: &turnObservations{journal: InertJournal{}}}, nil)
}

func (f *toolFactory) childSession(yield func(SessionEntry, error) bool) {
	for i := range f.childContext {
		if !yield(f.childContext[i], nil) {
			return
		}
	}
}

// chatObserver receives each output item of a turn runPrompt runs, as it arrives.
type chatObserver interface {
	observe(ChatResponse)
}

type inertChatObserver struct{}

func (inertChatObserver) observe(ChatResponse) {}

// runPrompt runs texts as the only turn of l, journaled under turnID, passes each output item to
// observer as it arrives, and returns the turn's last assistant message.
func (l *looper) runPrompt(ctx context.Context, turnID string, texts []string, sessionIn iter.Seq2[SessionEntry, error], sessionOut sessionWriter, observer chatObserver) (last string, err error) {
	output := make(chan ChatResponse)

	input := make(chan PromptInput, 1)
	input <- PromptInput{TurnID: turnID, Role: PromptInputRoleUser, Texts: texts, Responses: output}

	close(input)

	var group errgroup.Group

	group.Go(func() error {
		for item := range output {
			observer.observe(item)

			if item.Kind == ChatResponseAssistantMessage {
				last = item.Text
			}
		}

		return nil
	})

	err = l.Loop(ctx, input, sessionIn, sessionOut, make(chan os.Signal, 1))
	_ = group.Wait() // The collector cannot fail.

	return last, err
}

// childSessionWriter saves a hidden run under its factory's childKey, as entryType when not empty.
type childSessionWriter struct {
	factory   *toolFactory
	entryType string
}

func (w childSessionWriter) Append(ctx context.Context, entry *SessionEntry) error {
	saved := *entry

	saved.Type = cmp.Or(w.entryType, saved.Type)
	if err := w.factory.childSessions.AppendChildEntry(ctx, w.factory.childKey, &saved); err != nil {
		return fmt.Errorf("append child session entry: %w", err)
	}

	return nil
}

// runGuardrail saves its run under subagentKey, the key of the subagent the task call runs.
func (f *toolFactory) runGuardrail(ctx context.Context, guardrail *Agent, stage ChildRunStage, message, guardedAgent string, subagentKey ChildKey, metadata toolCallMetadata, parentOutput chan<- ChatResponse) guardrailDecision {
	agent := *guardrail
	agent.Permission = f.shellTemp.effectivePermissions(agent.Permission)
	expandAgentPrompt(ctx, &agent, f.expandPromptShellCommands.SubagentPrompts, &f.promptExpansion)

	childFactory := *f
	childFactory.inGuardrailRun = true
	childFactory.childKey = subagentKey

	child, err := childFactory.newLooper(&agent, childFactory.childSystemPrompt(&agent))
	if err != nil {
		return guardrailDecision{Approved: false, Reason: "inter-agent guardrail model failed: " + err.Error()}
	}

	child.ResponseFormat = guardrailResponseFormat()

	var observer chatObserver = inertChatObserver{}
	if f.diagnostics {
		observer = &guardrailDiagnostics{output: parentOutput, guardedAgent: guardedAgent, guardrailAgent: agent.Name, stage: stage, metadata: metadata}
	}

	last, err := child.runPrompt(ctx, "", []string{message}, f.childSession, childSessionWriter{factory: &childFactory, entryType: guardrailEntryType}, observer)
	if err != nil {
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

// guardrailDiagnostics streams a guardrail run's output items to output as diagnostics of the
// agent it guards.
type guardrailDiagnostics struct {
	output                       chan<- ChatResponse
	guardedAgent, guardrailAgent string
	stage                        ChildRunStage
	metadata                     toolCallMetadata
}

func (d *guardrailDiagnostics) observe(item ChatResponse) {
	if item.Kind == ChatResponseAssistantMessage {
		return
	}

	emitSubagentDiagnostic(d.output, &SubagentDiagnostic{
		Name:  d.guardedAgent,
		Index: d.metadata.subagentIndex,
		Total: d.metadata.subagentTotal,
		Subagent: &SubagentDiagnostic{
			Name:  d.guardrailAgent,
			Label: "guardrail(" + string(d.stage) + ")",
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
	format := responses.ResponseFormatTextConfigParamOfJSONSchema("guardrail_decision", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"approved": map[string]any{"type": "boolean"},
			"reason":   map[string]any{"type": "string"},
		},
		"required":             []string{"approved", "reason"},
		"additionalProperties": false,
	})
	format.OfJSONSchema.Strict = openai.Bool(true)

	return format
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
