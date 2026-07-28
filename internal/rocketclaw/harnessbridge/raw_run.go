package harnessbridge

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/Arize-ai/openinference/go/openinference-instrumentation"
	semconv "github.com/Arize-ai/openinference/go/openinference-semantic-conventions"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/openai/openai-go/v3/responses"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace/noop"
	"golang.org/x/sync/errgroup"
)

// RawRunResult is the observable result of one non-publishing raw rocketcode turn.
type RawRunResult struct {
	Text, VerbatimMessage string
	Attachments           []events.OutboundAttachment
}

// RawRunProgress controls raw rocketcode run persistence and receives observable output.
type RawRunProgress struct {
	SessionService *SessionService
	ConversationID string

	Thinking, Message func(context.Context, string) error
	RequestRestart    func(context.Context, string) (string, error)
	RequestReload     func(context.Context, string) (string, error)
}

const rawRunMissingToolPrompt = "You did not call the mandatory " + rawRunToolName + " tool. Normal assistant replies do not count and this background run cannot finish until you call that exact tool. Before this turn ends, call " + rawRunToolName + "(\"full exact message to show the human, or empty string if the human should see nothing\"). If the human partner should see a final message from this background turn, the full final message must be the tool argument. Do not send a summary, paraphrase, or reduced view."

// RunRawWithProgress executes a raw rocketcode turn and reports optional progress.
func RunRawWithProgress(ctx context.Context, cfg *config.Config, agent, prompt string, logger *slog.Logger, progress *RawRunProgress) (result RawRunResult, err error) {
	diagnostics := progress != nil

	if progress == nil {
		progress = newInertRawRunProgress()
	}

	agent = strings.TrimSpace(agent)
	if agent == "" {
		agent = "main"
	}

	ctx = instrumentation.WithSession(ctx, progress.ConversationID)

	tracer := noop.NewTracerProvider().Tracer("rocketclaw")
	if cfg.Instrumentation.Enabled {
		tracer = otel.Tracer("rocketclaw")
	}

	ctx, span := tracer.Start(ctx, "rocketclaw.raw_run")
	instrumentation.ApplyContextAttributes(ctx, span)
	span.SetAttributes(attribute.String(semconv.OpenInferenceSpanKind, semconv.SpanKindAgent))
	span.SetAttributes(
		attribute.String(semconv.AgentName, agent),
		attribute.String(semconv.SessionID, progress.ConversationID),
		attribute.String("rocketclaw.conversation_id", progress.ConversationID),
		rocketclawInputValue(cfg, prompt),
	)

	defer func() {
		recordRocketClawSpanError(span, err)
		span.SetAttributes(rocketclawOutputValue(cfg, result.Text))
		span.End()
	}()

	memory := new(memoryStore)
	sessionIn := memory.in()
	sessionOut := memory.out

	if strings.TrimSpace(progress.ConversationID) != "" {
		store := newSessionStore(progress.ConversationID, progress.SessionService)
		sessionIn = store.in()
		sessionOut = func(entry rocketcode.SessionEntry) error {
			_, err := store.outID(entry)

			return err
		}
	}

	decision := new(rawRunDecision)
	attachments := new(outboundAttachmentCollector)

	for {
		text, err := runRawAttempt(ctx, cfg, agent, prompt, logger, sessionIn, sessionOut, decision, attachments, progress, diagnostics)
		if err != nil {
			return RawRunResult{}, err
		}

		if payload, ok := decision.Decision(); ok {
			if strings.TrimSpace(payload) == "" {
				return RawRunResult{Text: text}, nil
			}

			return RawRunResult{Text: text, VerbatimMessage: payload, Attachments: attachments.Attachments()}, nil
		}

		if err := ctx.Err(); err != nil {
			return RawRunResult{}, fmt.Errorf("mandatory tool %s was not called before context ended: %w", rawRunToolName, err)
		}

		prompt = rawRunMissingToolPrompt
	}
}

func newInertRawRunProgress() *RawRunProgress {
	return &RawRunProgress{
		Thinking:       func(context.Context, string) error { return nil },
		Message:        func(context.Context, string) error { return nil },
		RequestRestart: func(context.Context, string) (string, error) { return "", nil },
		RequestReload:  func(context.Context, string) (string, error) { return "rocketclaw runtime assets reloaded", nil },
	}
}

func newWorkflowAgentRunner(cfg *config.Config, agent string, logger *slog.Logger) (workflow.AgentRunFunc, func() error, error) {
	root, agents, skills, providers, err := prepareRocketCode(cfg, agent, logger, toolModeWorkflow)
	if err != nil {
		return nil, nil, err
	}

	parent := filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), ".rocketcode"))
	if err := root.MkdirAll(parent, 0o755); err != nil {
		_ = root.Close()
		return nil, nil, fmt.Errorf("create workflow shell output parent dir: %w", err)
	}

	run := func(ctx context.Context, request workflow.AgentRequest, thinkingProgress workflow.AgentThinkingFunc) (result json.RawMessage, err error) {
		callAgents := rocketcode.Agents{Items: maps.Clone(agents.Items)}

		active := callAgents.Items[agent]
		if request.Worker.Name != "" {
			active.Prompt = request.Worker.Instructions
		}

		if request.Worker.Model != "" {
			model, ok := cfg.Models[request.Worker.Model]
			if !ok {
				return nil, fmt.Errorf("workflow worker model %q is not configured", request.Worker.Model)
			}

			active.Model, err = cfg.RenderAgentModel(model)
			if err != nil {
				return nil, fmt.Errorf("render workflow worker model %q: %w", request.Worker.Model, err)
			}
		}

		callAgents.Items[agent] = active

		shellOutputRel := filepath.ToSlash(filepath.Join(parent, "workflow-"+rand.Text()))
		if err := root.Mkdir(shellOutputRel, 0o700); err != nil {
			return nil, fmt.Errorf("create workflow shell output dir: %w", err)
		}
		defer func() {
			if errRemove := root.RemoveAll(shellOutputRel); errRemove != nil {
				err = errors.Join(err, fmt.Errorf("remove workflow shell output dir: %w", errRemove))
			}
		}()

		runtimeConfig := rocketcode.Config{AutoApproverModel: cfg.AutoApproverModel, ShellOutputDir: filepath.Join(cfg.Workspace, filepath.FromSlash(shellOutputRel)), Diagnostics: true, ParallelToolCalls: 16, ExperimentalStrongerSkills: true, AutoApprovePermissions: true, Observability: rocketcode.ObservabilityConfig{Enabled: cfg.Instrumentation.Enabled, Tracer: otel.Tracer("rocketcode"), TraceConfig: instrumentation.TraceConfig{HideInputs: cfg.Instrumentation.HideInputs, HideOutputs: cfg.Instrumentation.HideOutputs}}, ChildRunLogger: rocketcode.DiscardChildRunLog, CheckpointSink: rocketcode.InertCheckpointSink{}}

		runtime, err := rocketcode.NewWithProviders(providers, &runtimeConfig, root, callAgents, skills, agent, io.Discard)
		if err != nil {
			return nil, fmt.Errorf("prepare workflow rocketcode run: %w", err)
		}

		tools := slices.Clone(request.Worker.Tools)
		if tools == nil {
			tools = slices.DeleteFunc(slices.Collect(maps.Keys(runtime.Tools)), func(name string) bool { return name == "task" })
		}

		if _, available := runtime.Tools["find_skills"]; available && slices.Contains(tools, "skill") && !slices.Contains(tools, "find_skills") {
			tools = append(tools, "find_skills")
		}

		if err := runtime.RestrictTools(tools); err != nil {
			return nil, fmt.Errorf("restrict workflow worker tools: %w", err)
		}

		if request.Schema != nil {
			schema := maps.Clone(request.Schema)
			if schema["type"] == "object" {
				schema["additionalProperties"] = false
			}

			runtime.ResponseFormat.OfJSONSchema = &responses.ResponseFormatTextJSONSchemaConfigParam{Name: "workflow_response", Schema: schema}
		}

		memory := new(memoryStore)
		input := make(chan rocketcode.PromptInput, 1)

		output := make(chan rocketcode.ChatResponse, 128)
		input <- rocketcode.PromptInput{Role: rocketcode.PromptInputRoleUser, Text: request.Prompt, Responses: output}

		close(input)

		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		var group errgroup.Group
		group.Go(func() error { return runtime.Loop(runCtx, input, memory.in(), memory.out, make(chan os.Signal, 1)) })

		var errProgress error

		last := ""

		for item := range output {
			if errProgress != nil {
				continue
			}

			switch item.Kind {
			case rocketcode.ChatResponseAssistantCommentary, rocketcode.ChatResponseAssistantTool, rocketcode.ChatResponseReasoningSummary:
				if thinking := rocketcodeThinkingText(item); thinking != "" {
					if err := thinkingProgress(runCtx, thinking); err != nil {
						errProgress = fmt.Errorf("publish workflow agent thinking: %w", err)

						cancel()
					}
				}
			case rocketcode.ChatResponseAssistantMessage:
				if request.Schema != nil {
					last = item.Text
				} else {
					last = appendText(last, item.Text)
				}
			}
		}

		errRun := group.Wait()

		if errProgress != nil {
			return nil, errProgress
		}

		if errRun != nil {
			return nil, fmt.Errorf("run workflow rocketcode turn: %w", errRun)
		}

		if request.Schema == nil {
			return json.Marshal(last)
		}

		if !json.Valid([]byte(last)) {
			return nil, errors.New("workflow worker returned invalid JSON")
		}

		return json.RawMessage(last), nil
	}

	return run, root.Close, nil
}

func prepareRocketCode(cfg *config.Config, agent string, logger *slog.Logger, mode toolMode) (*os.Root, rocketcode.Agents, rocketcode.Skills, rocketcode.Providers, error) {
	root, err := os.OpenRoot(cfg.Workspace)
	if err != nil {
		return nil, rocketcode.Agents{}, rocketcode.Skills{}, rocketcode.Providers{}, fmt.Errorf("open workspace root: %w", err)
	}

	agents, skills, err := loadRocketCodeDefinitionsIn(root, cfg, cfg.RuntimeDirName(), mode)
	if err != nil {
		_ = root.Close()
		return nil, rocketcode.Agents{}, rocketcode.Skills{}, rocketcode.Providers{}, fmt.Errorf("open workspace agent and skills: %w", err)
	}

	appendOverlayPromptToAgent(agents, agent, cfg)

	b := &Bridge{log: logger, config: Config{Agent: agent}, runtime: cfg}

	providers, err := b.rocketcodeProviders()
	if err != nil {
		_ = root.Close()
		return nil, rocketcode.Agents{}, rocketcode.Skills{}, rocketcode.Providers{}, fmt.Errorf("prepare RocketCode providers: %w", err)
	}

	return root, agents, skills, providers, nil
}

func runRawAttempt(ctx context.Context, cfg *config.Config, agent, prompt string, logger *slog.Logger, sessionIn iter.Seq2[rocketcode.SessionEntry, error], sessionOut func(rocketcode.SessionEntry) error, decision *rawRunDecision, attachments *outboundAttachmentCollector, progress *RawRunProgress, diagnostics bool) (string, error) {
	last := ""

	root, agents, skills, providers, err := prepareRocketCode(cfg, agent, logger, toolModeCron)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()

	if err := root.MkdirAll(filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), ".rocketcode")), 0o755); err != nil {
		return "", fmt.Errorf("create rocketcode cron shell output parent dir: %w", err)
	}

	shellOutputRel := filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), ".rocketcode", "cron-"+rand.Text()))
	if err := root.Mkdir(shellOutputRel, 0o700); err != nil {
		return "", fmt.Errorf("create rocketcode cron shell output dir: %w", err)
	}

	shellOutputDir := filepath.Join(cfg.Workspace, filepath.FromSlash(shellOutputRel))

	defer func() { _ = root.RemoveAll(shellOutputRel) }()

	requestRestart := progress.RequestRestart
	requestReload := progress.RequestReload

	recordRestartRequester := func(context.Context) error { return nil }
	if strings.TrimSpace(progress.ConversationID) != "" {
		recordRestartRequester = func(ctx context.Context) error {
			return progress.SessionService.MarkRestartRequester(ctx, progress.ConversationID)
		}
	}

	b := &Bridge{log: logger, config: Config{Agent: agent, RequestRestart: requestRestart, RequestReload: requestReload}, runtime: cfg, mu: sync.Mutex{}}

	customTools := []rocketcode.Tool{decision.Tool(), attachments.Tool(root), reloadTool(requestReload)}

	activeAgent := agents.Items[agent]
	if agentExplicitlyAllowsRocketClawTool(&activeAgent, restartToolName) {
		customTools = append(customTools, restartTool(requestRestart, recordRestartRequester))
	}

	rocketcodeConfig := rocketcode.Config{Model: "", AutoApproverModel: cfg.AutoApproverModel, ReasoningEffort: "", ShellOutputDir: shellOutputDir, Diagnostics: diagnostics, ExperimentalStrongerSkills: true, ExpandPromptShellCommands: rocketcode.PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: true, SkillPrompts: true, InputPrompts: true}, CompactThreshold: 0, CompactionSteering: "", ParallelToolCalls: 16, AutoApprovePermissions: true, Observability: rocketcode.ObservabilityConfig{Enabled: cfg.Instrumentation.Enabled, Tracer: otel.Tracer("rocketcode"), TraceConfig: instrumentation.TraceConfig{HideInputs: cfg.Instrumentation.HideInputs, HideOutputs: cfg.Instrumentation.HideOutputs}}, ChildRunLogger: b.logRocketCodeChildRun, CheckpointSink: rocketcode.InertCheckpointSink{}, CustomTools: customTools}

	looper, err := rocketcode.NewWithProviders(providers, &rocketcodeConfig, root, agents, skills, agent, io.Discard)
	if err != nil {
		return "", fmt.Errorf("prepare raw rocketcode run: %w", err)
	}

	input := make(chan rocketcode.PromptInput, 1)
	output := make(chan rocketcode.ChatResponse, 128)
	interrupts := make(chan os.Signal, 1)

	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	input <- rocketcode.PromptInput{Role: "", Text: provenanceHeader(promptProvenance{origin: "Cron", media: "Text"}) + "\n\n" + prompt, Attachments: nil, Responses: output}

	close(input)

	var group errgroup.Group
	group.Go(func() error {
		return looper.Loop(attemptCtx, input, sessionIn, sessionOut, interrupts)
	})

	var errProgress error

	for item := range output {
		if errProgress != nil {
			continue
		}

		if item.Kind == rocketcode.ChatResponseAssistantCommentary || item.Kind == rocketcode.ChatResponseAssistantTool || item.Kind == rocketcode.ChatResponseReasoningSummary {
			if thinking := rocketcodeThinkingText(item); thinking != "" {
				if err := progress.Thinking(attemptCtx, thinking); err != nil {
					errProgress = fmt.Errorf("publish raw rocketcode thinking: %w", err)

					cancel()

					continue
				}
			}
		}

		if item.Kind == rocketcode.ChatResponseAssistantMessage {
			last = appendText(last, item.Text)
			if err := progress.Message(attemptCtx, item.Text); err != nil {
				errProgress = fmt.Errorf("publish raw rocketcode message: %w", err)

				cancel()
			}
		}
	}

	if errProgress != nil {
		_ = group.Wait()

		return last, errProgress
	}

	if errGroup := group.Wait(); errGroup != nil {
		return last, fmt.Errorf("run raw rocketcode turn: %w", errGroup)
	}

	return last, nil
}

type rawRunDecision struct {
	mu       sync.Mutex
	decision *string
}

type rawRunDecisionInput struct {
	Payload string `json:"payload"`
}

func (d *rawRunDecision) Tool() rocketcode.Tool {
	return rocketcode.Tool{Name: rawRunToolName, Description: "Mandatory decision tool for background turns. If the human partner should see anything from this turn, call this with the full exact message.", Permission: "rocketclaw", VisibilitySubjects: []string{rawRunToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{rawRunToolName}, nil }, Parameters: map[string]any{"properties": map[string]any{"payload": map[string]any{"type": "string"}}}, Call: func(_ context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
		var input rawRunDecisionInput
		if err := json.Unmarshal(raw, &input); err != nil {
			return rocketcode.ToolResult{}, fmt.Errorf("parse raw run decision: %w", err)
		}

		d.mu.Lock()
		d.decision = &input.Payload
		d.mu.Unlock()

		return rocketcode.TextToolResult("queued for verbatim delivery"), nil
	}}
}

func (d *rawRunDecision) Decision() (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.decision == nil {
		return "", false
	}

	return *d.decision, true
}
