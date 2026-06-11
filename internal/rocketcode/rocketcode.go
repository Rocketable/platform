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
	"text/template"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
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
	InterAgentFilter           InterAgentFilterConfig
	CustomTools                []Tool
	ShellEnv                   map[string]string
}

// InterAgentFilterConfig configures an agent that approves task prompts and responses.
type InterAgentFilterConfig struct {
	Prompt          string
	Model           string
	ReasoningEffort string
	Verbosity       string
	Permission      PermissionSet
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
	if configInput == nil {
		return nil, errors.New("config is required")
	}

	config := normalizeConfig(configInput)

	if client == nil {
		return nil, errors.New("client is required")
	}

	if root == nil {
		return nil, errors.New("root is required")
	}

	if config.ShellOutputDir == "" {
		return nil, errors.New("shell output dir is required")
	}

	shellEnvKeys := slices.Sorted(maps.Keys(config.ShellEnv))

	shellEnv := make([]string, 0, len(shellEnvKeys))
	for _, key := range shellEnvKeys {
		if key == "TMPDIR" {
			continue
		}

		value := config.ShellEnv[key]
		if key == "" {
			return nil, errors.New("shell env key is required")
		}

		if strings.Contains(key, "=") {
			return nil, fmt.Errorf("shell env key %q must not contain =", key)
		}

		if strings.Contains(key, "\x00") || strings.Contains(value, "\x00") {
			return nil, fmt.Errorf("shell env %q must not contain NUL", key)
		}

		shellEnv = append(shellEnv, key+"="+value)
	}

	shellOutput, err := newShellOutputConfig(root, config.ShellOutputDir)
	if err != nil {
		return nil, err
	}

	if agents.Items == nil {
		return nil, errors.New("agents are required")
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

	expandAgentPrompt(context.Background(), &activeAgent, config.ExpandPromptShellCommands.PrimaryPrompts, &promptExpansion)
	systemPrompt := activeAgent.Prompt

	rootInstructions, err := loadRootInstructions(root)
	if err != nil {
		return nil, err
	}

	rootInstructions = strings.TrimSpace(rootInstructions) + "\n\n" + fmt.Sprintf("<current-workspace>\nWorkspace root: %s\n</current-workspace>", promptExpansion.hostDir)
	systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + strings.TrimSpace(rootInstructions))

	model := parseAgentModel(activeAgent.Model, config.Model)
	reasoningEffort := shared.ReasoningEffort(cmp.Or(activeAgent.ReasoningEffort, string(config.ReasoningEffort)))
	agentForTools := &activeAgent
	activeAgent.Permission = shellOutput.effectivePermissions(activeAgent.Permission)
	baseTools := newSandboxedTools(root, shellOutput, shellEnv, config.SandboxedBash)

	customTools, err := customLooperTools(config.CustomTools, baseTools)
	if err != nil {
		return nil, err
	}

	var filter *interAgentFilter

	if strings.TrimSpace(config.InterAgentFilter.Prompt) != "" {
		prompt, err := template.New("inter_agent_filter").Parse(config.InterAgentFilter.Prompt)
		if err != nil {
			return nil, fmt.Errorf("parse inter-agent filter prompt: %w", err)
		}

		filter = &interAgentFilter{
			agent: Agent{
				Name:            "inter_agent_filter",
				Description:     "",
				Model:           config.InterAgentFilter.Model,
				ReasoningEffort: config.InterAgentFilter.ReasoningEffort,
				Verbosity:       config.InterAgentFilter.Verbosity,
				MaxRecursion:    nil,
				Prompt:          config.InterAgentFilter.Prompt,
				Location:        "",
				Permission:      config.InterAgentFilter.Permission,
				Frontmatter:     nil,
				FileMode:        0,
			},
			prompt: prompt,
		}
	}

	maps.Copy(baseTools, customTools)
	factory := &toolFactory{
		client:                     responseServiceClient{service: &client.Responses},
		systemPrompt:               systemPrompt,
		model:                      model,
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
		interAgentFilter:           filter,
	}
	runtimeSystemPrompt := composeSystemPromptWithSkills(systemPrompt, skills, agentForTools)

	var (
		responseFormat responses.ResponseFormatTextConfigUnionParam
		rewriteHistory func([]responses.ResponseInputItemUnionParam) []responses.ResponseInputItemUnionParam
	)

	looper := &looper{
		agent:              activeAgent,
		Client:             responseServiceClient{service: &client.Responses},
		SystemPrompt:       runtimeSystemPrompt,
		Model:              model,
		ReasoningEffort:    reasoningEffort,
		Verbosity:          activeAgent.Verbosity,
		CompactThreshold:   config.CompactThreshold,
		CompactionSteering: config.CompactionSteering,
		ParallelToolCalls:  config.ParallelToolCalls,
		ResponseFormat:     responseFormat,
		Permissions:        activeAgent.Permission,
		Tools:              factory.toolsFor(agentForTools),
		RewriteHistory:     rewriteHistory,
		Diagnostics:        config.Diagnostics,
		expandInputPrompts: config.ExpandPromptShellCommands.InputPrompts,
		promptExpansion:    promptExpansion,
	}

	if config.Diagnostics {
		if err := printRuntimeDiagnostics(diagnosticsWriter, &activeAgent, looper.Tools, skills, runtimeSystemPrompt); err != nil {
			return nil, err
		}
	}

	return looper, nil
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
		config.Model = openai.ChatModelGPT5_4
	}

	if config.ReasoningEffort == "" {
		config.ReasoningEffort = shared.ReasoningEffort("high")
	}

	if config.CompactThreshold == 0 {
		config.CompactThreshold = defaultCompactThreshold
	}

	return config
}

func printRuntimeDiagnostics(w io.Writer, activeAgent *Agent, tools map[string]looperTool, skills Skills, systemPrompt string) error {
	agentName := "(none)"
	if activeAgent != nil {
		agentName = activeAgent.Name
	}

	toolNames := slices.Sorted(maps.Keys(tools))
	skillNames := slices.Sorted(maps.Keys(skills.Items))

	if _, err := fmt.Fprintf(w, "agent: %s\n", agentName); err != nil {
		return fmt.Errorf("write agent diagnostic: %w", err)
	}

	if _, err := fmt.Fprintf(w, "tools: %s\n", strings.Join(toolNames, ", ")); err != nil {
		return fmt.Errorf("write tools diagnostic: %w", err)
	}

	if _, err := fmt.Fprintf(w, "skills: %s\n", strings.Join(skillNames, ", ")); err != nil {
		return fmt.Errorf("write skills diagnostic: %w", err)
	}

	if _, err := fmt.Fprintln(w, "system_prompt:"); err != nil {
		return fmt.Errorf("write system prompt diagnostic header: %w", err)
	}

	if _, err := fmt.Fprintln(w, "---"); err != nil {
		return fmt.Errorf("write system prompt diagnostic fence: %w", err)
	}

	if _, err := fmt.Fprintln(w, systemPrompt); err != nil {
		return fmt.Errorf("write system prompt diagnostic: %w", err)
	}

	if _, err := fmt.Fprintln(w, "---"); err != nil {
		return fmt.Errorf("write system prompt diagnostic closing fence: %w", err)
	}

	return nil
}
