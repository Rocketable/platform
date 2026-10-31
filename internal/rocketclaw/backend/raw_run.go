package backend

import (
	"cmp"
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
	"strings"

	instrumentation "github.com/Arize-ai/openinference/go/openinference-instrumentation"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/openai/openai-go/v3/responses"
	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"
)

// RawRunProgress carries the conversation and destination for one cron run.
type RawRunProgress struct {
	ConversationID  string
	Run             *protocol.CronRun
	SyncDestination string
	Cronjob         *protocol.CronjobMessage
}

type workflowAgentRunner struct {
	definition    *workflow.Definition
	cfg           *config.Config
	agent, parent string
	journal       harness.Journal
	root          *os.Root
	agents        harness.Agents
	skills        harness.Skills
	resolver      *modelResolver
	customTools   []harness.Tool
	// shellEnv is the environment of the turn the workflow runs for (Bridge.turnShellEnv), and
	// originHeader that turn's trusted origin header (reviewOriginHeader).
	shellEnv     map[string]string
	originHeader string
}

// workflowWorkerStep is a finished workflow worker's recorded result.
type workflowWorkerStep struct {
	Name   string          `json:"name"`
	Hash   string          `json:"hash"`
	Result json.RawMessage `json:"result"`
}

func newWorkflowAgentRunner(locked *config.LockedConfig, agent string, journal harness.Journal, logger *slog.Logger, customTools ...harness.Tool) (*workflowAgentRunner, error) {
	root, agents, skills, resolver, err := prepareRocketCode(locked, agent, logger, toolModeWorkflow)
	if err != nil {
		return nil, err
	}

	cfg := locked.Clone()

	parent := filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), ".rocketcode"))
	if err := root.MkdirAll(parent, 0o755); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("create workflow shell temp parent dir: %w", err)
	}

	return &workflowAgentRunner{cfg: cfg, agent: agent, parent: parent, journal: journal, root: root, agents: agents, skills: skills, resolver: resolver, customTools: customTools}, nil
}

// Close releases prepared data ownership; an unprepared request has no
// definition or open root. Every workflow.Run still receives a real runner.
func (r *workflowAgentRunner) Close() error {
	if r.root == nil {
		return nil
	}

	err := r.root.Close()
	*r = workflowAgentRunner{}

	if err != nil {
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

	callAgents := harness.Agents{Items: maps.Clone(r.agents.Items)}

	active := callAgents.Items[r.agent]
	if request.Worker.Name != "" {
		active.Prompt = request.Worker.Instructions
	}

	if model := request.Worker.Model; model != "" {
		// An easy agent's effort comes only from its model's #effort, so this is its model as written.
		own := strings.TrimSuffix(active.Model+"#"+active.ReasoningEffort, "#")
		if active.Mode == harness.AgentModeEasy && !slices.Contains(append([]string{own}, active.Subagents...), model) {
			return nil, fmt.Errorf("worker model %q is neither this agent's model nor one of its subagent models: %s", model, strings.Join(active.Subagents, ", "))
		}

		name, effort := harness.ParseModelRef(model)
		active.Model, active.ReasoningEffort = name, cmp.Or(effort, active.ReasoningEffort)
	}

	callAgents.Items[r.agent] = active
	prepareWorkflowTags(callAgents, r.agent, request.Worker.Tools)

	shellTempRel := filepath.ToSlash(filepath.Join(r.parent, "workflow-"+rand.Text()))
	if err := r.root.Mkdir(shellTempRel, 0o700); err != nil {
		return nil, fmt.Errorf("create workflow shell temp dir: %w", err)
	}
	defer func() {
		if errRemove := r.root.RemoveAll(shellTempRel); errRemove != nil {
			err = errors.Join(err, fmt.Errorf("remove workflow shell temp dir: %w", errRemove))
		}
	}()

	runtimeConfig := harness.Config{AutoApproverModel: r.cfg.AutoApproverModel, ShellTempDir: filepath.Join(r.cfg.Workspace, filepath.FromSlash(shellTempRel)), SpillDir: rocketcodeSpillDir(r.cfg), RetainedResultDir: filepath.Join("retained", "unused"), ParallelToolCalls: 16, ExperimentalStrongerSkills: true, AutoApprovePermissions: true, Observability: harness.ObservabilityConfig{Enabled: r.cfg.Instrumentation.Enabled, Tracer: otel.Tracer("rocketcode"), TraceConfig: instrumentation.TraceConfig{HideInputs: r.cfg.Instrumentation.HideInputs, HideOutputs: r.cfg.Instrumentation.HideOutputs}}, ChildSessions: harness.InertChildSessions{}, Journal: r.journal, BackgroundJobs: harness.InertBackgroundJobs{}, ShellCommand: harness.DefaultShellCommand{}}
	runtimeConfig.CustomTools = r.customTools
	runtimeConfig.ShellEnv, runtimeConfig.OriginHeader = r.shellEnv, r.originHeader

	runtime, err := harness.NewWithModelResolver(r.resolver, &runtimeConfig, r.root, callAgents, r.skills, r.agent, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("prepare workflow rocketcode run: %w", err)
	}

	tools := slices.Clone(request.Worker.Tools)
	if tools == nil {
		tools = slices.Collect(maps.Keys(runtime.Tools))
	}

	// Workflows call agents; agents reach every host and platform tool through execute.
	// Strip task and names that live inside execute; permissions still govern those.
	tools = slices.DeleteFunc(tools, func(name string) bool {
		_, nested := runtime.CodeModeHosts[name]
		return name == "task" || nested || harness.CodeModeOnlyHostTool(name)
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
	input := make(chan harness.PromptInput, 1)

	output := make(chan harness.ChatResponse, 128)
	input <- harness.PromptInput{TurnID: request.Key + "/" + want.Hash, Role: harness.PromptInputRoleUser, Texts: []string{request.Prompt}, Responses: output}

	close(input)

	var group errgroup.Group
	group.Go(func() error { return runtime.Loop(ctx, input, memory.in(), memory, make(chan os.Signal, 1)) })

	last := ""

	for item := range output {
		if item.Kind == harness.ChatResponseAssistantMessage {
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
func prepareWorkflowTags(agents harness.Agents, worker string, tools []string) {
	agent := agents.Items[worker]

	agent.Permission.Buckets = slices.Clone(agent.Permission.Buckets)
	for i, bucket := range agent.Permission.Buckets {
		if bucket.Name == "rocketclaw_tags" {
			agent.Permission.Buckets[i].Rules = slices.Clone(bucket.Rules)
			for j, rule := range bucket.Rules {
				if tools != nil && !slices.Contains(tools, rule.Pattern) {
					agent.Permission.Buckets[i].Rules[j].Action = harness.PermissionDeny
				}
			}
		}
	}

	agents.Items[worker] = agent

	for name := range agents.Items {
		item := agents.Items[name]

		groups, _ := agentTagGroups(&item) // readRocketCodeDefinitionsIn already rejected invalid groups.

		appendSessionTagPrompt(&item, groups)
		agents.Items[name] = item
	}
}

func prepareRocketCode(locked *config.LockedConfig, agent string, logger *slog.Logger, mode toolMode) (*os.Root, harness.Agents, harness.Skills, *modelResolver, error) {
	cfg := locked.Clone()

	root, err := os.OpenRoot(cfg.Workspace)
	if err != nil {
		return nil, harness.Agents{}, harness.Skills{}, nil, fmt.Errorf("open workspace root: %w", err)
	}

	agents, skills, err := loadRocketCodeDefinitionsIn(root, locked, cfg.RuntimeDirName(), mode)
	if err != nil {
		_ = root.Close()
		return nil, harness.Agents{}, harness.Skills{}, nil, fmt.Errorf("open workspace agent and skills: %w", err)
	}

	appendOverlayPromptToAgent(agents, agent, cfg)

	return root, agents, skills, newModelResolver(cfg, logger), nil
}

func rocketcodeSpillDir(cfg *config.Config) string {
	return filepath.Join(cfg.Workspace, cfg.RuntimeDirName(), ".rocketcode", "spill")
}

// rocketcodeRetainedDir is a conversation's retained background output, relative to the spill dir.
func rocketcodeRetainedDir(conversationID string) string {
	sum := sha256.Sum256([]byte(conversationID))
	return filepath.Join("retained", hex.EncodeToString(sum[:]))
}
