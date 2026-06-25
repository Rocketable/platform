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
	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
	"go.opentelemetry.io/otel/trace"
)

// Config contains runtime settings supplied by the embedding application.
type Config struct {
	Model                      shared.ResponsesModel
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
	CustomTools                []Tool
	ShellEnv                   map[string]string
}

// ObservabilityConfig controls OpenInference-compatible tracing for RocketCode.
type ObservabilityConfig struct {
	Enabled     bool
	Tracer      trace.Tracer
	TraceConfig instrumentation.TraceConfig
}

// Providers contains model provider clients supplied by embedding applications.
type Providers struct {
	OpenAI           *openai.Client
	Anthropic        *anthropic.Client
	OpenAICompatible map[string]OpenAICompatibleProvider
}

// OpenAICompatibleMode selects which OpenAI-compatible API surface a provider supports.
type OpenAICompatibleMode string

const (
	// OpenAICompatibleModeResponses routes through the OpenAI-compatible responses API.
	OpenAICompatibleModeResponses OpenAICompatibleMode = "responses"
	// OpenAICompatibleModeChatCompletions routes through the OpenAI-compatible chat completions API.
	OpenAICompatibleModeChatCompletions OpenAICompatibleMode = "chat_completions"
)

// OpenAICompatibleProvider contains one named OpenAI-compatible provider client.
type OpenAICompatibleProvider struct {
	Client openai.Client
	Mode   OpenAICompatibleMode
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

	return PermissionSet{Buckets: buckets}
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

	return NewWithProviders(Providers{OpenAI: client, Anthropic: nil}, configInput, root, agents, skills, defaultAgent, diagnosticsWriter)
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

	promptExpansion, err := newPromptExpansionEnvironment(root, shellOutput, shellEnv)
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

	if _, err := parseModelRef(config.Model); err != nil {
		return nil, err
	}

	if err := validateAgentModels(agents, providers); err != nil {
		return nil, err
	}

	modelRef, err := parseAgentModelRef(activeAgent.Model)
	if err != nil {
		return nil, fmt.Errorf("agent %q model: %w", activeAgent.Name, err)
	}

	providerClient, err := responsesAPIForModel(providers, modelRef)
	if err != nil {
		return nil, err
	}

	reasoningEffort := shared.ReasoningEffort(cmp.Or(activeAgent.ReasoningEffort, string(config.ReasoningEffort)))
	agentForTools := &activeAgent
	activeAgent.Permission = shellOutput.effectivePermissions(activeAgent.Permission)
	baseTools := newSandboxedTools(root, shellOutput, shellEnv, config.SandboxedBash)

	customTools, err := customLooperTools(config.CustomTools, baseTools)
	if err != nil {
		return nil, err
	}

	maps.Copy(baseTools, customTools)
	factory := &toolFactory{
		providers:                  providers,
		client:                     providerClient.client,
		target:                     providerClient.target,
		anthropicClient:            providers.Anthropic,
		systemPrompt:               systemPrompt,
		modelRef:                   modelRef,
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
	}
	runtimeSystemPrompt := composeSystemPromptWithSkills(systemPrompt, skills, agentForTools)

	looper := &looper{
		agent:                  activeAgent,
		provider:               modelRef.provider,
		modelRef:               modelRef,
		target:                 providerClient.target,
		Client:                 providerClient.client,
		AnthropicClient:        providers.Anthropic,
		SystemPrompt:           runtimeSystemPrompt,
		Model:                  modelRef.apiModel,
		DisplayModel:           modelRef.display(),
		ReasoningEffort:        reasoningEffort,
		Verbosity:              activeAgent.Verbosity,
		CompactThreshold:       config.CompactThreshold,
		CompactionSteering:     config.CompactionSteering,
		ParallelToolCalls:      config.ParallelToolCalls,
		Permissions:            activeAgent.Permission,
		Tools:                  factory.toolsFor(agentForTools),
		Diagnostics:            config.Diagnostics,
		AutoApprovePermissions: config.AutoApprovePermissions,
		PermissionReviewer:     factory,
		Observability:          config.Observability,
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
		config.Model = modelProviderOpenAI + "/" + openai.ChatModelGPT5_4
	}

	if config.ReasoningEffort == "" {
		config.ReasoningEffort = shared.ReasoningEffort("high")
	}

	if config.CompactThreshold == 0 {
		config.CompactThreshold = defaultCompactThreshold
	}

	return config
}

func requireProvider(providers Providers, model modelRef) error {
	if model.provider == modelProviderOpenAI && providers.OpenAI == nil {
		return errors.New("openai provider is required")
	}

	if model.provider == modelProviderAnthropic && providers.Anthropic == nil {
		return errors.New("anthropic provider is required")
	}

	if model.provider == modelProviderOpenAICompatible {
		if _, ok := providers.OpenAICompatible[model.compatibleProvider]; !ok {
			return fmt.Errorf("openai-compatible provider %q is required", model.compatibleProvider)
		}
	}

	return nil
}

func validateAgentModels(agents Agents, providers Providers) error {
	for name := range agents.Items {
		agent := agents.Items[name]

		modelRef, err := parseAgentModelRef(agent.Model)
		if err != nil {
			return fmt.Errorf("agent %q model: %w", name, err)
		}

		if err := requireProvider(providers, modelRef); err != nil {
			return fmt.Errorf("agent %q model: %w", name, err)
		}
	}

	return nil
}

type responsesAPISelection struct {
	client responsesAPI
	target providerTarget
}

type providerSurface string

const (
	providerSurfaceResponses       providerSurface = "responses"
	providerSurfaceChatCompletions providerSurface = "chat_completions"
	providerSurfaceAnthropic       providerSurface = "messages"
)

type providerTarget struct {
	modelRef modelRef
	surface  providerSurface
}

func responsesAPIForModel(providers Providers, model modelRef) (responsesAPISelection, error) {
	if err := requireProvider(providers, model); err != nil {
		return responsesAPISelection{}, err
	}

	switch model.provider {
	case modelProviderOpenAI:
		return responsesAPISelection{client: responseServiceClient{service: &providers.OpenAI.Responses}, target: providerTarget{modelRef: model, surface: providerSurfaceResponses}}, nil
	case modelProviderOpenAICompatible:
		provider := providers.OpenAICompatible[model.compatibleProvider]
		if provider.Mode == "" || provider.Mode == OpenAICompatibleModeResponses {
			return responsesAPISelection{client: responseServiceClient{service: &provider.Client.Responses}, target: providerTarget{modelRef: model, surface: providerSurfaceResponses}}, nil
		}

		if provider.Mode == OpenAICompatibleModeChatCompletions {
			return responsesAPISelection{client: chatCompletionServiceClient{service: &provider.Client.Chat.Completions}, target: providerTarget{modelRef: model, surface: providerSurfaceChatCompletions}}, nil
		}

		return responsesAPISelection{}, fmt.Errorf("openai-compatible provider %q mode %q is not implemented", model.compatibleProvider, provider.Mode)
	case modelProviderAnthropic:
		return responsesAPISelection{target: providerTarget{modelRef: model, surface: providerSurfaceAnthropic}}, nil
	default:
		return responsesAPISelection{}, fmt.Errorf("unsupported model provider %q", model.provider)
	}
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
