package backend

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/Arize-ai/openinference/go/openinference-instrumentation"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/openai/openai-go/v3/responses"
	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"
)

// RawRunProgress carries the conversation and Slack destination for one cron run.
type RawRunProgress struct {
	ConversationID  string
	SyncDestination string
	Cronjob         *protocol.CronjobMessage
	TextChannel     string
}

func newWorkflowAgentRunner(cfg *config.Config, agent string, logger *slog.Logger) (workflow.AgentRunFunc, func() error, error) {
	root, agents, skills, resolver, err := prepareRocketCode(cfg, agent, logger, toolModeWorkflow)
	if err != nil {
		return nil, nil, err
	}

	parent := filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), ".rocketcode"))
	if err := root.MkdirAll(parent, 0o755); err != nil {
		_ = root.Close()
		return nil, nil, fmt.Errorf("create workflow shell temp parent dir: %w", err)
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

		shellTempRel := filepath.ToSlash(filepath.Join(parent, "workflow-"+rand.Text()))
		if err := root.Mkdir(shellTempRel, 0o700); err != nil {
			return nil, fmt.Errorf("create workflow shell temp dir: %w", err)
		}
		defer func() {
			if errRemove := root.RemoveAll(shellTempRel); errRemove != nil {
				err = errors.Join(err, fmt.Errorf("remove workflow shell temp dir: %w", errRemove))
			}
		}()

		runtimeConfig := rocketcode.Config{AutoApproverModel: cfg.AutoApproverModel, ShellTempDir: filepath.Join(cfg.Workspace, filepath.FromSlash(shellTempRel)), SpillDir: rocketcodeSpillDir(cfg), Diagnostics: true, ParallelToolCalls: 16, ExperimentalStrongerSkills: true, AutoApprovePermissions: true, Observability: rocketcode.ObservabilityConfig{Enabled: cfg.Instrumentation.Enabled, Tracer: otel.Tracer("rocketcode"), TraceConfig: instrumentation.TraceConfig{HideInputs: cfg.Instrumentation.HideInputs, HideOutputs: cfg.Instrumentation.HideOutputs}}, ChildRunLogger: rocketcode.DiscardChildRunLog, CheckpointSink: rocketcode.InertCheckpointSink{}, ShellCommand: rocketcode.DefaultShellCommand}

		runtime, err := rocketcode.NewWithModelResolver(resolver, &runtimeConfig, root, callAgents, skills, agent, io.Discard)
		if err != nil {
			return nil, fmt.Errorf("prepare workflow rocketcode run: %w", err)
		}

		tools := slices.Clone(request.Worker.Tools)
		if tools == nil {
			tools = slices.Collect(maps.Keys(runtime.Tools))
		}

		// Workflows call agents; agents use execute for FS/shell. Keep execute.
		// Strip task and any direct host tools if a caller allowlist still names them.
		tools = slices.DeleteFunc(tools, func(name string) bool {
			return name == "task" || rocketcode.CodeModeOnlyHostTool(name)
		})

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

func prepareRocketCode(cfg *config.Config, agent string, logger *slog.Logger, mode toolMode) (*os.Root, rocketcode.Agents, rocketcode.Skills, *modelResolver, error) {
	root, err := os.OpenRoot(cfg.Workspace)
	if err != nil {
		return nil, rocketcode.Agents{}, rocketcode.Skills{}, nil, fmt.Errorf("open workspace root: %w", err)
	}

	agents, skills, err := loadRocketCodeDefinitionsIn(root, cfg, cfg.RuntimeDirName(), mode)
	if err != nil {
		_ = root.Close()
		return nil, rocketcode.Agents{}, rocketcode.Skills{}, nil, fmt.Errorf("open workspace agent and skills: %w", err)
	}

	appendOverlayPromptToAgent(agents, agent, cfg)

	return root, agents, skills, newModelResolver(cfg, logger), nil
}

func rocketcodeSpillDir(cfg *config.Config) string {
	return filepath.Join(cfg.Workspace, cfg.RuntimeDirName(), ".rocketcode", "spill")
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
