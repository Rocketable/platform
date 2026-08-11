// Package rocketcode provides the reusable RocketCode runtime.
package rocketcode

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Arize-ai/openinference/go/openinference-instrumentation"
	"github.com/Rocketable/platform/internal/rocketcode/mcpclient"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
	"go.opentelemetry.io/otel/trace"
)

// ShellCommandFunc selects the process used to run a bash tool command string.
// The returned path and args are passed to exec.Command (for example
// "/bin/bash", []string{"-lc", command}).
type ShellCommandFunc func(command string) (path string, args []string)

// Config contains runtime settings supplied by the embedding application.
type Config struct {
	Model                      shared.ResponsesModel
	AutoApproverModel          shared.ResponsesModel
	ReasoningEffort            shared.ReasoningEffort
	Diagnostics                bool
	ExperimentalStrongerSkills bool
	ExpandPromptShellCommands  PromptShellCommandExpansion
	CompactThreshold           int64
	CompactionSteering         string
	ParallelToolCalls          int
	ShellOutputDir             string
	SandboxedBash              bool
	AutoApprovePermissions     bool
	Observability              ObservabilityConfig
	ChildRunLogger             ChildRunLogger
	CheckpointSink             CheckpointSink
	CustomTools                []Tool
	ShellEnv                   map[string]string
	// ShellCommand builds the executable used for bash tool (and prompt !`…`)
	// commands. Required; pass DefaultShellCommand for normal host shell behavior.
	ShellCommand ShellCommandFunc
	MCPServers   map[string]mcpclient.ServerConfig
	MCPWorkspace string
}

// ChildRunKind identifies a hidden child-run category.
type ChildRunKind string

const (
	// ChildRunKindGuardrail identifies an inter-agent guardrail child run.
	ChildRunKindGuardrail ChildRunKind = "guardrail"
	// ChildRunKindPermissionReview identifies an automatic permission reviewer child run.
	ChildRunKindPermissionReview ChildRunKind = "permission_review"
)

// ChildRunStage identifies the operation being reviewed by a hidden child run.
type ChildRunStage string

const (
	// ChildRunStageDelegation identifies guardrail review before delegation.
	ChildRunStageDelegation ChildRunStage = "delegation"
	// ChildRunStageResponse identifies guardrail review after a child response.
	ChildRunStageResponse ChildRunStage = "response"
	// ChildRunStageToolPermission identifies automatic tool permission review.
	ChildRunStageToolPermission ChildRunStage = "tool_permission"
)

// ChildRunEvent contains server/operator-only hidden child-run output.
type ChildRunEvent struct {
	Kind  ChildRunKind
	Stage ChildRunStage
	Agent string
	Item  ChatResponse
}

// ChildRunLogger consumes server/operator-only hidden child-run output.
type ChildRunLogger func(*ChildRunEvent)

// DiscardChildRunLog ignores hidden child-run output.
func DiscardChildRunLog(*ChildRunEvent) {}

// ObservabilityConfig controls OpenInference-compatible tracing for RocketCode.
type ObservabilityConfig struct {
	Enabled     bool
	Tracer      trace.Tracer
	TraceConfig instrumentation.TraceConfig
}

// Providers contains model provider clients supplied by embedding applications.
type Providers struct {
	OpenAI *openai.Client
}

// PromptShellCommandExpansion controls which prompt sources expand !`command` snippets.
type PromptShellCommandExpansion struct {
	PrimaryPrompts  bool
	SubagentPrompts bool
	SkillPrompts    bool
	InputPrompts    bool
}

type shellOutputConfig struct {
	outputRelDir string
	tmpDir       string
	readPattern  string
}

func newShellOutputConfig(root *os.Root, outputDir string) (shellOutputConfig, error) {
	info, err := os.Stat(outputDir)
	if err != nil {
		return shellOutputConfig{}, fmt.Errorf("resolve shell output dir %q: %w", outputDir, err)
	}

	if !info.IsDir() {
		return shellOutputConfig{}, fmt.Errorf("resolve shell output dir %q: not a directory", outputDir)
	}

	rootAbs, err := filepath.Abs(root.Name())
	if err != nil {
		return shellOutputConfig{}, fmt.Errorf("resolve workspace root %q: %w", root.Name(), err)
	}

	outputAbs, err := filepath.Abs(outputDir)
	if err != nil {
		return shellOutputConfig{}, fmt.Errorf("resolve shell output dir %q: %w", outputDir, err)
	}

	rel, err := filepath.Rel(rootAbs, outputAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return shellOutputConfig{}, fmt.Errorf("resolve shell output dir %q: must be inside workspace root", outputDir)
	}

	if _, err := root.Stat(rel); err != nil {
		return shellOutputConfig{}, fmt.Errorf("resolve shell output dir %q: %w", outputDir, err)
	}

	rel = filepath.ToSlash(filepath.Clean(rel))

	if rel == "." {
		return shellOutputConfig{
			outputRelDir: rel,
			tmpDir:       filepath.Join(outputAbs, "tmp"),
			readPattern:  "rocketcode-bash-*",
		}, nil
	}

	return shellOutputConfig{
		outputRelDir: rel,
		tmpDir:       filepath.Join(outputAbs, "tmp"),
		readPattern:  rel + "/rocketcode-bash-*",
	}, nil
}

func (c *shellOutputConfig) effectivePermissions(permissions PermissionSet) PermissionSet {
	if c.readPattern == "" || !permissions.hasAllowRuleForPermission("bash") {
		return permissions
	}

	buckets := make([]PermissionBucket, 0, len(permissions.Buckets)+1)
	buckets = append(buckets, PermissionBucket{Name: "read", Rules: []PermissionRule{{Pattern: c.readPattern, Action: permissionAllow}}})
	buckets = append(buckets, permissions.Buckets...)

	return PermissionSet{Buckets: buckets, skillRead: permissions.skillRead}
}

func (c shellOutputConfig) ensureTempDir(root *os.Root) error {
	tmpRelDir := filepath.ToSlash(filepath.Join(c.outputRelDir, "tmp"))
	if err := root.MkdirAll(tmpRelDir, 0o700); err != nil {
		return fmt.Errorf("create shell temp dir: %w", err)
	}

	if err := root.Chmod(tmpRelDir, 0o700); err != nil {
		return fmt.Errorf("secure shell temp dir: %w", err)
	}

	return nil
}

// New loads the supplied runtime dependencies and returns a configured looper.
func New(
	client *openai.Client,
	configInput *Config,
	root *os.Root,
	agents Agents,
	skills Skills,
	defaultAgent string,
	diagnosticsWriter io.Writer,
) (*Runtime, error) {
	if client == nil {
		return nil, errors.New("client is required")
	}

	return NewWithProviders(Providers{OpenAI: client}, configInput, root, agents, skills, defaultAgent, diagnosticsWriter)
}

// NewWithProviders loads the supplied runtime dependencies and returns a configured looper.
func NewWithProviders(
	providers Providers,
	configInput *Config,
	root *os.Root,
	agents Agents,
	skills Skills,
	defaultAgent string,
	diagnosticsWriter io.Writer,
) (*Runtime, error) {
	if configInput == nil {
		return nil, errors.New("config is required")
	}

	config := normalizeConfig(configInput)
	if _, err := parseModelRef(config.Model); err != nil {
		return nil, err
	}

	if config.AutoApproverModel != "" {
		if _, err := parseModelRef(config.AutoApproverModel); err != nil {
			return nil, fmt.Errorf("auto approver model: %w", err)
		}
	}

	if err := validateAgentModels(agents); err != nil {
		return nil, err
	}

	if err := requireProvider(providers); err != nil {
		return nil, err
	}

	return NewWithModelResolver(openAIModelResolver{client: providers.OpenAI}, configInput, root, agents, skills, defaultAgent, diagnosticsWriter)
}

// NewWithModelResolver loads the supplied runtime dependencies and resolves the active agent model.
func NewWithModelResolver(
	resolver ModelResolver,
	configInput *Config,
	root *os.Root,
	agents Agents,
	skills Skills,
	defaultAgent string,
	diagnosticsWriter io.Writer,
) (*Runtime, error) {
	if resolver == nil {
		return nil, errors.New("resolver is required")
	}

	if configInput == nil {
		return nil, errors.New("config is required")
	}

	config := normalizeConfig(configInput)

	if root == nil {
		return nil, errors.New("root is required")
	}

	if config.ShellOutputDir == "" {
		return nil, errors.New("shell output dir is required")
	}

	shellEnv, err := shellEnvList(config.ShellEnv)
	if err != nil {
		return nil, err
	}

	shellOutput, err := newShellOutputConfig(root, config.ShellOutputDir)
	if err != nil {
		return nil, err
	}

	if agents.Items == nil {
		return nil, errors.New("agents are required")
	}

	if err := validateAutoPermissionReviewers(config.AutoApprovePermissions, agents); err != nil {
		return nil, err
	}

	if skills.Items == nil {
		return nil, errors.New("skills are required")
	}

	if defaultAgent == "" {
		return nil, errors.New("defaultAgent is required")
	}

	if config.Diagnostics && diagnosticsWriter == nil {
		return nil, errors.New("diagnosticsWriter is required when diagnostics are enabled")
	}

	if config.ChildRunLogger == nil {
		return nil, errors.New("child run logger is required")
	}

	if config.CheckpointSink == nil {
		return nil, errors.New("checkpoint sink is required")
	}

	agents = skills.withReadPermissions(root, agents)

	promptExpansion, err := newPromptExpansionEnvironment(root, shellOutput, shellEnv, config.ShellCommand)
	if err != nil {
		return nil, fmt.Errorf("initialize prompt expansion: %w", err)
	}

	activeAgent, hasActiveAgent := agents.Items[defaultAgent]
	if !hasActiveAgent {
		return nil, fmt.Errorf("missing required default agent %q", defaultAgent)
	}

	for name := range agents.Items {
		agent := agents.Items[name]
		if agent.Guardrail == "" {
			continue
		}

		if _, ok := agents.Items[agent.Guardrail]; !ok {
			return nil, fmt.Errorf("agent %q references missing guardrail agent %q", name, agent.Guardrail)
		}
	}

	expandAgentPrompt(context.Background(), &activeAgent, config.ExpandPromptShellCommands.PrimaryPrompts, &promptExpansion)
	systemPrompt := activeAgent.Prompt

	rootInstructions, err := loadRootInstructions(root)
	if err != nil {
		return nil, err
	}

	rootInstructions = strings.TrimSpace(rootInstructions) + "\n\n" + fmt.Sprintf("<current-workspace>\nWorkspace root: %s\n</current-workspace>", promptExpansion.hostDir)
	systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + strings.TrimSpace(rootInstructions))

	client, origin, err := resolveModel(resolver, activeAgent.Model)
	if err != nil {
		return nil, fmt.Errorf("agent %q model: %w", activeAgent.Name, err)
	}

	reasoningEffort := shared.ReasoningEffort(cmp.Or(activeAgent.ReasoningEffort, string(config.ReasoningEffort)))
	agentForTools := &activeAgent
	activeAgent.Permission = shellOutput.effectivePermissions(activeAgent.Permission)
	baseTools := newSandboxedTools(root, shellOutput, shellEnv, config.SandboxedBash, config.ShellCommand)

	customTools, err := customLooperTools(config.CustomTools, baseTools)
	if err != nil {
		return nil, err
	}

	maps.Copy(baseTools, customTools)

	mcpRegistry, err := newMCPRegistry(config.MCPWorkspace, config.MCPServers)
	if err != nil {
		return nil, err
	}

	factory := &toolFactory{
		resolver:                   resolver,
		systemPrompt:               systemPrompt,
		autoApproverModel:          cmp.Or(config.AutoApproverModel, config.Model),
		reasoningEffort:            reasoningEffort,
		compactThreshold:           config.CompactThreshold,
		compactionSteering:         config.CompactionSteering,
		parallelToolCalls:          config.ParallelToolCalls,
		diagnostics:                config.Diagnostics,
		experimentalStrongerSkills: config.ExperimentalStrongerSkills,
		expandPromptShellCommands:  config.ExpandPromptShellCommands,
		promptExpansion:            promptExpansion,
		agent:                      agentForTools,
		recursionRemaining:         activeAgent.MaxRecursion,
		agents:                     agents,
		skills:                     skills,
		baseTools:                  baseTools,
		shellOutput:                shellOutput,
		autoApprovePermissions:     config.AutoApprovePermissions,
		observability:              config.Observability,
		childRunLogger:             config.ChildRunLogger,
		mcpRegistry:                mcpRegistry,
	}
	modelTools, codeHosts := factory.assembleTools(agentForTools)

	var mcpServers []string
	if factory.mcpRegistry != nil && agentForTools != nil {
		mcpServers = visibleMCPServers(agentForTools.Permission, factory.mcpRegistry.Names())
	}

	runtimeSystemPrompt := withCodeModeSystemPrompt(
		composeSystemPromptWithSkills(systemPrompt, skills, agentForTools),
		modelTools, codeHosts, mcpServers,
	)
	looper := &looper{
		agent:                  activeAgent,
		ProviderOrigin:         origin,
		Client:                 responseServiceClient{service: &client.Responses},
		SystemPrompt:           runtimeSystemPrompt,
		Model:                  origin.Model,
		DisplayModel:           origin.displayModel(),
		ReasoningEffort:        reasoningEffort,
		Verbosity:              activeAgent.Verbosity,
		CompactThreshold:       config.CompactThreshold,
		CompactionSteering:     config.CompactionSteering,
		ParallelToolCalls:      config.ParallelToolCalls,
		Permissions:            activeAgent.Permission,
		Tools:                  modelTools,
		CodeModeHosts:          codeHosts,
		Diagnostics:            config.Diagnostics,
		AutoApprovePermissions: config.AutoApprovePermissions,
		PermissionReviewer:     factory,
		Observability:          config.Observability,
		CheckpointSink:         config.CheckpointSink,
		expandInputPrompts:     config.ExpandPromptShellCommands.InputPrompts,
		promptExpansion:        promptExpansion,
	}

	if config.Diagnostics {
		if err := printRuntimeDiagnostics(diagnosticsWriter, &activeAgent, looper.Tools, skills, runtimeSystemPrompt); err != nil {
			return nil, err
		}
	}

	return looper, nil
}

// RestrictTools limits the runtime to the exact supplied tool allowlist.
func (r *Runtime) RestrictTools(names []string) error {
	if names == nil {
		return nil
	}

	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		if allowed[name] {
			return fmt.Errorf("duplicate tool %q", name)
		}

		if _, ok := r.Tools[name]; !ok {
			return fmt.Errorf("unknown tool %q", name)
		}

		allowed[name] = true
	}

	for name := range r.Tools {
		if !allowed[name] {
			delete(r.Tools, name)
		}
	}

	return nil
}

func validateAutoPermissionReviewers(enabled bool, agents Agents) error {
	if !enabled {
		return nil
	}

	if _, ok := agents.Items[guardianAgentName]; ok {
		return fmt.Errorf("agent name %q is reserved when auto permission approval is enabled", guardianAgentName)
	}

	for agentName := range agents.Items {
		agent := agents.Items[agentName]
		for _, bucket := range agent.Permission.Buckets {
			for _, rule := range bucket.Rules {
				if rule.Action != permissionAuto || rule.Reviewer == "" {
					continue
				}

				if rule.Reviewer == guardianAgentName {
					return fmt.Errorf("agent %q permission %q rule %q references reserved reviewer %q; use bare auto for the embedded guardian", agentName, bucket.Name, rule.Pattern, guardianAgentName)
				}

				if _, ok := agents.Items[rule.Reviewer]; !ok {
					return fmt.Errorf("agent %q permission %q rule %q references missing reviewer agent %q", agentName, bucket.Name, rule.Pattern, rule.Reviewer)
				}
			}
		}
	}

	return nil
}

func loadRootInstructions(root *os.Root) (string, error) {
	file, err := root.Open("AGENTS.md")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}

		return "", fmt.Errorf("open AGENTS.md: %w", err)
	}

	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("read AGENTS.md: %w", err)
	}

	if len(data) == 0 {
		return "", nil
	}

	return "Instructions from: AGENTS.md\n" + string(data), nil
}

func normalizeConfig(configInput *Config) Config {
	config := *configInput

	if config.Model == "" {
		config.Model = defaultModelRef().apiModel
	}

	if config.ReasoningEffort == "" {
		config.ReasoningEffort = shared.ReasoningEffort("high")
	}

	if config.CompactThreshold == 0 {
		config.CompactThreshold = defaultCompactThreshold
	}

	return config
}

func requireProvider(providers Providers) error {
	if providers.OpenAI == nil {
		return errors.New("openai provider is required")
	}

	return nil
}

func validateAgentModels(agents Agents) error {
	for name := range agents.Items {
		agent := agents.Items[name]
		if strings.TrimSpace(agent.Model) == "" {
			return fmt.Errorf("agent %q model: required non-empty string", name)
		}

		if _, err := parseModelRef(agent.Model); err != nil {
			return fmt.Errorf("agent %q model: %w", name, err)
		}
	}

	return nil
}

func printRuntimeDiagnostics(w io.Writer, activeAgent *Agent, tools map[string]looperTool, skills Skills, systemPrompt string) error {
	agentName := "(none)"
	if activeAgent != nil {
		agentName = activeAgent.Name
	}

	toolNames := slices.Sorted(maps.Keys(tools))
	skillNames := slices.Sorted(maps.Keys(skills.Items))

	if _, err := fmt.Fprintf(w, "agent: %s\ntools: %s\nskills: %s\nsystem_prompt:\n---\n%s\n---\n", agentName, strings.Join(toolNames, ", "), strings.Join(skillNames, ", "), systemPrompt); err != nil {
		return fmt.Errorf("write runtime diagnostics: %w", err)
	}

	return nil
}
