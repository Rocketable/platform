// Package harness provides the reusable agent runtime.
package harness

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Arize-ai/openinference/go/openinference-instrumentation"
	"github.com/Rocketable/platform/internal/rocketclaw/mcpclient"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
	"go.opentelemetry.io/otel/trace"
)

// shellCommander selects the executable and arguments for a Bash command.
type shellCommander interface {
	Command(command string) (path string, args []string)
}

// CustomShell is the program that runs an agent's shell commands instead of bash, from the agent's
// shell frontmatter.
type CustomShell struct {
	// Path is the program. Without Setup it runs as <Path> <command>.
	Path string
	// Setup, when set, makes Path take no arguments and read from stdin one JSON line,
	// {"config": Setup, "env": the command's environment}, then the command.
	Setup json.RawMessage
	// Emulated means the agent asked for the embedding application's emulated shell (shell: emulated).
	// The embedder fills in Path and Setup; until it does, the agent's shell commands do not run.
	Emulated bool
}

// withCustomShell runs command with an agent's customShell, or through shell when the agent declares
// none. stdin is what the program reads, empty unless customShell has a Setup; env is its environment.
// An emulated customShell without a Path returns an empty path, so the command fails instead of running
// under bash.
func withCustomShell(shell shellCommander, customShell CustomShell, command string, env []string) (path string, args []string, stdin []byte) {
	if customShell.Path == "" {
		if customShell.Emulated {
			return "", nil, nil
		}

		path, args = shell.Command(command)

		return path, args, nil
	}

	if customShell.Setup == nil {
		return customShell.Path, []string{command}, nil
	}

	vars := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v // Later entries win, as they do for exec.
	}

	// Setup is valid JSON by contract, so encoding cannot fail.
	line, _ := json.Marshal(struct {
		Config json.RawMessage   `json:"config"`
		Env    map[string]string `json:"env"`
	}{customShell.Setup, vars})

	return customShell.Path, nil, append(append(line, '\n'), command...)
}

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
	// ShellTempDir is the workspace-relative or absolute directory used as TMPDIR
	// for bash and prompt !`…` commands. Required; must exist inside the workspace.
	ShellTempDir string
	// SpillDir is the workspace-relative or absolute directory for oversized
	// execute output. Empty uses <workspace>/.rocketcode/spill.
	SpillDir string
	// RetainedResultDir is the directory, relative to SpillDir, that keeps oversized output of
	// background execute scripts readable for 7 days. Required; give each conversation its own.
	RetainedResultDir      string
	AutoApprovePermissions bool
	Observability          ObservabilityConfig
	ChildSessions          ChildSessions
	Journal                Journal
	BackgroundJobs         BackgroundJobs
	CustomTools            []Tool
	ShellEnv               map[string]string
	// ChildContext supplies host messages to permission reviewers.
	ChildContext []SessionEntry
	// ShellCommand builds the executable used for bash tool (and prompt !`…`)
	// commands. Required; pass DefaultShellCommand{} to match permission validation.
	// Custom implementations must preserve the validated Bash syntax and semantics.
	// An agent's shell frontmatter overrides it for that agent.
	ShellCommand shellCommander
	MCPServers   map[string]mcpclient.ServerConfig
	MCPWorkspace string
}

// ChildSessions stores finished permission review turns, and runs Task subagents as
// sessions of the embedder, on the turn runner its other sessions use.
type ChildSessions interface {
	AppendChildEntry(ctx context.Context, key ChildKey, entry *SessionEntry) error
	// WriteSubagent delivers write to its subagent session, which it starts when new, and reports
	// whether write joined the session's running turn. A replayed write delivers nothing again.
	WriteSubagent(ctx context.Context, write *SubagentWrite) (steered bool, err error)
	// AwaitSubagent waits for the turn of the subagent session at key that runs the write of the task
	// call callKey, and returns its answer and the agent type and model it ended as.
	AwaitSubagent(ctx context.Context, key, callKey string) (string, ReplayAttribution, error)
}

// ChildKey names a child session by Path, its key under the embedder's session, which an empty Path
// names. A new child starts under the child at key Parent, by call Call of turn Turn there.
type ChildKey struct {
	Path, Parent, Turn, Call string
}

// under places k, a key relative to the child at key parent, under the embedder's session.
func (k ChildKey) under(parent string) ChildKey {
	k.Path, k.Parent = parent+k.Path, parent

	return k
}

// SubagentWrite is a task call's prompt to a subagent session.
type SubagentWrite struct {
	// Key is the session's child key.
	Key ChildKey
	// CallKey is the task call's tool call key, which identifies the write.
	CallKey string
	// Agent is the subagent type that runs Prompt, and Delegator the agent that wrote it.
	Agent, Delegator, Prompt string
	// Mode is a continue's mode; empty for a new subagent.
	Mode ContinueMode
}

// InertChildSessions discards child session entries and runs no subagents.
type InertChildSessions struct{}

// AppendChildEntry discards one child session entry.
func (InertChildSessions) AppendChildEntry(context.Context, ChildKey, *SessionEntry) error {
	return nil
}

// WriteSubagent refuses to run a subagent.
func (InertChildSessions) WriteSubagent(context.Context, *SubagentWrite) (bool, error) {
	return false, errSubagentsUnavailable
}

// AwaitSubagent refuses to run a subagent.
func (InertChildSessions) AwaitSubagent(context.Context, string, string) (string, ReplayAttribution, error) {
	return "", ReplayAttribution{}, errSubagentsUnavailable
}

// ObservabilityConfig controls OpenInference-compatible tracing for the harness.
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
	// PrimaryPrompts covers the agent prompt of every session, a Task subagent's included, since a
	// subagent runs as a session of the embedder (ChildSessions).
	PrimaryPrompts bool
	// SubagentPrompts covers the prompts of permission review agents.
	SubagentPrompts bool
	SkillPrompts    bool
	InputPrompts    bool
}

type shellTempConfig struct {
	tmpRelDir string
	tmpDir    string
}

func newShellTempConfig(root *os.Root, tempDir string) (shellTempConfig, error) {
	info, err := os.Stat(tempDir)
	if err != nil {
		return shellTempConfig{}, fmt.Errorf("resolve shell temp dir %q: %w", tempDir, err)
	}

	if !info.IsDir() {
		return shellTempConfig{}, fmt.Errorf("resolve shell temp dir %q: not a directory", tempDir)
	}

	rootAbs, err := filepath.Abs(root.Name())
	if err != nil {
		return shellTempConfig{}, fmt.Errorf("resolve workspace root %q: %w", root.Name(), err)
	}

	tempAbs, err := filepath.Abs(tempDir)
	if err != nil {
		return shellTempConfig{}, fmt.Errorf("resolve shell temp dir %q: %w", tempDir, err)
	}

	rel, err := filepath.Rel(rootAbs, tempAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return shellTempConfig{}, fmt.Errorf("resolve shell temp dir %q: must be inside workspace root", tempDir)
	}

	if _, err := root.Stat(rel); err != nil {
		return shellTempConfig{}, fmt.Errorf("resolve shell temp dir %q: %w", tempDir, err)
	}

	return shellTempConfig{
		tmpRelDir: filepath.ToSlash(filepath.Clean(rel)),
		tmpDir:    tempAbs,
	}, nil
}

func (c shellTempConfig) ensureTempDir(root *os.Root) error {
	if err := root.MkdirAll(c.tmpRelDir, 0o700); err != nil {
		return fmt.Errorf("create shell temp dir: %w", err)
	}

	if err := root.Chmod(c.tmpRelDir, 0o700); err != nil {
		return fmt.Errorf("secure shell temp dir: %w", err)
	}

	return nil
}

// effectivePermissions grants read/glob on this session shell temp tree when bash is allowed.
func (c *shellTempConfig) effectivePermissions(permissions PermissionSet) PermissionSet {
	if c.tmpRelDir == "" || c.tmpRelDir == "." || !permissions.hasRuleForPermission("bash", permissionAllow) {
		return permissions
	}

	rules := []PermissionRule{
		{Pattern: c.tmpRelDir, Action: permissionAllow},
		{Pattern: c.tmpRelDir + "/*", Action: permissionAllow},
	}
	buckets := make([]PermissionBucket, 0, len(permissions.Buckets)+2)
	buckets = append(buckets,
		PermissionBucket{Name: "read", Rules: slices.Clone(rules)},
		PermissionBucket{Name: "glob", Rules: slices.Clone(rules)},
	)
	buckets = append(buckets, permissions.Buckets...)

	return PermissionSet{Buckets: buckets, Sudo: permissions.Sudo, skillRead: permissions.skillRead}
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

	if providers.OpenAI == nil {
		return nil, errors.New("openai provider is required")
	}

	return NewWithModelResolver(openAIModelResolver{client: providers.OpenAI}, configInput, root, agents, skills, defaultAgent, diagnosticsWriter)
}

// interpolateAllAgentPermissions resolves ${ROCKETCLAW_*} patterns in every agent's permission rules against env
// (InterpolatePermissions).
func interpolateAllAgentPermissions(agents map[string]Agent, env []string) map[string]Agent {
	prepared := make(map[string]Agent, len(agents))

	for name := range agents {
		agent := agents[name]
		InterpolatePermissions(&agent, env)
		prepared[name] = agent
	}

	return prepared
}

// NewWithModelResolver loads the supplied model resolver and returns a configured looper.
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

	if config.ShellTempDir == "" {
		return nil, errors.New("shell temp dir is required")
	}

	shellEnv, err := shellEnvList(config.ShellEnv)
	if err != nil {
		return nil, err
	}

	shellTemp, err := newShellTempConfig(root, config.ShellTempDir)
	if err != nil {
		return nil, err
	}

	spillRel, err := resolveSpillRel(root, config.SpillDir)
	if err != nil {
		return nil, err
	}

	// Inside the spill dir, the file-tool block keeps retained output private.
	if !filepath.IsLocal(config.RetainedResultDir) {
		return nil, fmt.Errorf("retained result dir %q must be a relative path inside the spill dir", config.RetainedResultDir)
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

	if config.ChildSessions == nil {
		return nil, errors.New("child sessions are required")
	}

	activeAgent, hasActiveAgent := agents.Items[defaultAgent]
	if !hasActiveAgent {
		return nil, fmt.Errorf("missing required default agent %q", defaultAgent)
	}

	agents.Items = interpolateAllAgentPermissions(agents.Items, shellEnv)

	agents = skills.withReadPermissions(root, agents)

	activeAgent = agents.Items[defaultAgent]

	promptExpansion, err := newPromptExpansionEnvironment(root, shellTemp, shellEnv, config.ShellCommand)
	if err != nil {
		return nil, fmt.Errorf("initialize prompt expansion: %w", err)
	}

	rootInstructions, err := loadRootInstructions(root)
	if err != nil {
		return nil, err
	}

	workspaceInstructions := fmt.Sprintf("<current-workspace>\nWorkspace root: %s\n</current-workspace>", promptExpansion.hostDir)

	reasoningEffort := shared.ReasoningEffort(cmp.Or(activeAgent.ReasoningEffort, string(config.ReasoningEffort)))
	agentForTools := &activeAgent
	activeAgent.Permission = shellTemp.effectivePermissions(activeAgent.Permission)
	baseTools, shellSystem := newSandboxedTools(root, spillRel, shellTemp, shellEnv, config.ShellCommand)

	customTools, err := customLooperTools(config.CustomTools, baseTools)
	if err != nil {
		return nil, err
	}

	maps.Copy(baseTools, customTools)

	mcpRegistry := mcpclient.New(config.MCPWorkspace, config.MCPServers)

	factory := &toolFactory{
		resolver:                   resolver,
		rootInstructions:           strings.TrimSpace(rootInstructions),
		workspaceInstructions:      workspaceInstructions,
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
		shellSystem:                shellSystem,
		childContext:               config.ChildContext,
		shellTemp:                  shellTemp,
		spillRel:                   spillRel,
		retainedRel:                filepath.Join(spillRel, config.RetainedResultDir),
		autoApprovePermissions:     config.AutoApprovePermissions,
		observability:              config.Observability,
		childSessions:              config.ChildSessions,
		backgroundJobs:             config.BackgroundJobs,
		mcpRegistry:                mcpRegistry,
	}

	looper, err := factory.newLooper(agentForTools, factory.sessionSystemPrompt(context.Background(), agentForTools))
	if err != nil {
		return nil, fmt.Errorf("agent %q model: %w", activeAgent.Name, err)
	}

	looper.Journal, looper.expandInputPrompts = config.Journal, config.ExpandPromptShellCommands.InputPrompts

	if config.Diagnostics {
		if err := printRuntimeDiagnostics(diagnosticsWriter, &activeAgent, looper.Tools, skills, looper.SystemPrompt); err != nil {
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
		config.Model = defaultOpenAIModel
	}

	if config.ReasoningEffort == "" {
		config.ReasoningEffort = shared.ReasoningEffort("high")
	}

	if config.CompactThreshold == 0 {
		config.CompactThreshold = defaultCompactThreshold
	}

	return config
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
