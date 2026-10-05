package backend

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

type workflowAgentRunner struct {
	cfg           *config.Config
	agent, parent string
	journal       rocketcode.Journal
	root          *os.Root
	agents        rocketcode.Agents
	skills        rocketcode.Skills
	resolver      *modelResolver
	customTools   []rocketcode.Tool
}

// workflowWorkerStep is a finished workflow worker's recorded result.
type workflowWorkerStep struct {
	Name   string          `json:"name"`
	Hash   string          `json:"hash"`
	Result json.RawMessage `json:"result"`
}

func newWorkflowAgentRunner(cfg *config.Config, agent string, journal rocketcode.Journal, logger *slog.Logger, customTools ...rocketcode.Tool) (*workflowAgentRunner, error) {
	root, agents, skills, resolver, err := prepareRocketCode(cfg, agent, logger, toolModeWorkflow)
	if err != nil {
		return nil, err
	}

	parent := filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), ".rocketcode"))
	if err := root.MkdirAll(parent, 0o755); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("create workflow shell temp parent dir: %w", err)
	}

	return &workflowAgentRunner{cfg: cfg, agent: agent, parent: parent, journal: journal, root: root, agents: agents, skills: skills, resolver: resolver, customTools: customTools}, nil
}

func (r *workflowAgentRunner) Close() error {
	if err := r.root.Close(); err != nil {
		return fmt.Errorf("close workflow workspace root: %w", err)
	}

	return nil
}

// Run returns a worker's recorded result when the same request finished before a
// restart. Otherwise it runs the worker as a turn journaled under the request's
// key and hash, so an interrupted worker resumes and a changed request runs fresh.
func (r *workflowAgentRunner) Run(ctx context.Context, request *workflow.AgentRequest) (result json.RawMessage, err error) {
	encoded, _ := json.Marshal(request)
	sum := sha256.Sum256(encoded)
	want := workflowWorkerStep{Name: request.Worker.Name, Hash: hex.EncodeToString(sum[:])}

	recorded, found, err := r.journal.Load(ctx, request.Key)
	if err != nil {
		return nil, fmt.Errorf("load workflow worker result: %w", err)
	}

	if found {
		var step workflowWorkerStep
		if err := json.Unmarshal(recorded, &step); err != nil {
			return nil, fmt.Errorf("decode workflow worker result: %w", err)
		}

		if step.Name == want.Name && step.Hash == want.Hash {
			return step.Result, nil
		}
	}

	callAgents := rocketcode.Agents{Items: maps.Clone(r.agents.Items)}

	active := callAgents.Items[r.agent]
	if request.Worker.Name != "" {
		active.Prompt = request.Worker.Instructions
	}

	if request.Worker.Model != "" {
		model, ok := r.cfg.Models[request.Worker.Model]
		if !ok {
			return nil, fmt.Errorf("workflow worker model %q is not configured", request.Worker.Model)
		}

		active.Model, err = r.cfg.RenderAgentModel(model)
		if err != nil {
			return nil, fmt.Errorf("render workflow worker model %q: %w", request.Worker.Model, err)
		}
	}

	callAgents.Items[r.agent] = active
	if err := prepareWorkflowTags(callAgents, r.agent, request.Worker.Tools); err != nil {
		return nil, err
	}

	shellTempRel := filepath.ToSlash(filepath.Join(r.parent, "workflow-"+rand.Text()))
	if err := r.root.Mkdir(shellTempRel, 0o700); err != nil {
		return nil, fmt.Errorf("create workflow shell temp dir: %w", err)
	}
	defer func() {
		if errRemove := r.root.RemoveAll(shellTempRel); errRemove != nil {
			err = errors.Join(err, fmt.Errorf("remove workflow shell temp dir: %w", errRemove))
		}
	}()

	runtimeConfig := rocketcode.Config{AutoApproverModel: r.cfg.AutoApproverModel, ShellTempDir: filepath.Join(r.cfg.Workspace, filepath.FromSlash(shellTempRel)), SpillDir: rocketcodeSpillDir(r.cfg), ParallelToolCalls: 16, ExperimentalStrongerSkills: true, AutoApprovePermissions: true, Observability: rocketcode.ObservabilityConfig{Enabled: r.cfg.Instrumentation.Enabled, Tracer: otel.Tracer("rocketcode"), TraceConfig: instrumentation.TraceConfig{HideInputs: r.cfg.Instrumentation.HideInputs, HideOutputs: r.cfg.Instrumentation.HideOutputs}}, ChildSessions: rocketcode.InertChildSessions{}, Journal: r.journal, ShellCommand: rocketcode.DefaultShellCommand}
	runtimeConfig.CustomTools = r.customTools

	runtime, err := rocketcode.NewWithModelResolver(r.resolver, &runtimeConfig, r.root, callAgents, r.skills, r.agent, io.Discard)
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
	input <- rocketcode.PromptInput{TurnID: request.Key + "/" + want.Hash, Role: rocketcode.PromptInputRoleUser, Text: request.Prompt, Responses: output}

	close(input)

	var group errgroup.Group
	group.Go(func() error { return runtime.Loop(ctx, input, memory.in(), memory.out, make(chan os.Signal, 1)) })

	last := ""

	for item := range output {
		if item.Kind == rocketcode.ChatResponseAssistantMessage {
			if request.Schema != nil {
				last = item.Text
			} else {
				last = appendText(last, item.Text)
			}
		}
	}

	errRun := group.Wait()
	if errRun != nil {
		return nil, fmt.Errorf("run workflow rocketcode turn: %w", errRun)
	}

	result = json.RawMessage(last)
	if request.Schema == nil {
		result, _ = json.Marshal(last) // Encoding a string cannot fail.
	} else if !json.Valid(result) {
		return nil, errors.New("workflow worker returned invalid JSON")
	}

	want.Result = result
	step, _ := json.Marshal(want)

	if err := r.journal.Save(context.WithoutCancel(ctx), request.Key, step); err != nil {
		return nil, fmt.Errorf("record workflow worker result: %w", err)
	}

	return result, nil
}

// prepareWorkflowTags keeps worker limits and caller guidance local to one run.
func prepareWorkflowTags(agents rocketcode.Agents, worker string, tools []string) error {
	agent := agents.Items[worker]

	agent.Permission.Buckets = slices.Clone(agent.Permission.Buckets)
	for i, bucket := range agent.Permission.Buckets {
		if bucket.Name == "rocketclaw_tags" {
			agent.Permission.Buckets[i].Rules = slices.Clone(bucket.Rules)
			for j, rule := range bucket.Rules {
				if tools != nil && !slices.Contains(tools, rule.Pattern) {
					agent.Permission.Buckets[i].Rules[j].Action = rocketcode.PermissionDeny
				}
			}
		}
	}

	agents.Items[worker] = agent

	for name := range agents.Items {
		item := agents.Items[name]

		groups, err := agentTagGroups(&item)
		if err != nil {
			return err
		}

		appendSessionTagPrompt(&item, groups)
		agents.Items[name] = item
	}

	return nil
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

// Decision returns this run's decision, falling back to a decision call recorded
// in its finished turns when a resumed turn reused that call's journaled result.
func (d *rawRunDecision) Decision(entries []rocketcode.SessionEntry) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.decision != nil {
		return *d.decision, true
	}

	payload, decided := "", false

	for i := range entries {
		for _, raw := range entries[i].ReplayInput {
			var call struct {
				Type, Name, Arguments string
			}

			var input rawRunDecisionInput
			if json.Unmarshal(raw, &call) == nil && call.Type == "function_call" && call.Name == rawRunToolName && json.Unmarshal([]byte(call.Arguments), &input) == nil {
				payload, decided = input.Payload, true
			}
		}
	}

	return payload, decided
}
