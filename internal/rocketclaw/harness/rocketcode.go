package harness

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/mcpclient"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/openai/openai-go/v3/shared"
	"go.opentelemetry.io/otel/trace"
)

type CustomShell struct {
	Path           string
	Emulated       bool
	Dir            string
	ReadOnly       []string
	Commands       map[string]string
	NetworkTargets []string
	Secrets        map[string]string
	Env            []string
}

func withCustomShell(customShell *CustomShell, command string) (path string, args []string) {
	if customShell.Path != "" {
		return customShell.Path, []string{command}
	}

	return "/bin/bash", []string{"--noprofile", "--norc", "-p", "-c", command}
}

type Config struct {
	AutoApproverModel         shared.ResponsesModel
	AutoApproverDecisionModel string
	AutoApproverMinConfidence float64
	Deciders                  map[string]Decider
	ExpandPromptShellCommands PromptShellCommandExpansion
	ParallelToolCalls         int
	ShellTempDir              string
	SpillDir                  string
	RetainedResultDir         string
	Observability             ObservabilityConfig
	ChildSessions             ChildSessions
	Journal                   Journal
	BackgroundJobs            BackgroundJobs
	TurnSecrets               TurnSecrets
	CustomTools               []Tool
	ShellEnv                  map[string]string
	ChildContext              []SessionEntry
	OriginHeader              string
	Sandbox                   sandbox.Runtime
	MCPServers                map[string]mcpclient.ServerConfig
	MCPWorkspace              string
}

type ChildSessions interface {
	AppendChildEntry(ctx context.Context, key ChildKey, entry *SessionEntry) error
	WriteSubagent(ctx context.Context, write *SubagentWrite) (steered bool, err error)
	AwaitSubagent(ctx context.Context, key, callKey string) (string, ReplayAttribution, error)
}

type ChildKey struct {
	Path, Parent, Turn, Call string
}

func (k ChildKey) under(parent string) ChildKey {
	k.Path, k.Parent = parent+k.Path, parent

	return k
}

type SubagentWrite struct {
	Key                      ChildKey
	CallKey                  string
	Agent, Delegator, Prompt string
	Mode                     ContinueMode
	Model                    string
	Tools                    []string
}

type InertChildSessions struct{}

func (InertChildSessions) AppendChildEntry(context.Context, ChildKey, *SessionEntry) error {
	return nil
}

func (InertChildSessions) WriteSubagent(context.Context, *SubagentWrite) (bool, error) {
	return false, errSubagentsUnavailable
}

func (InertChildSessions) AwaitSubagent(context.Context, string, string) (string, ReplayAttribution, error) {
	return "", ReplayAttribution{}, errSubagentsUnavailable
}

type TurnSecrets interface {
	Env() []string
	Redact(text string) string
}

type InertTurnSecrets struct{}

func (InertTurnSecrets) Env() []string { return nil }

func (InertTurnSecrets) Redact(text string) string { return text }

type ObservabilityConfig struct {
	Enabled bool
	Tracer  trace.Tracer
}

type PromptShellCommandExpansion struct {
	PrimaryPrompts  bool
	SubagentPrompts bool
	SkillPrompts    bool
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

	return PermissionSet{Buckets: buckets, Sudo: permissions.Sudo, Easy: permissions.Easy, skillRead: permissions.skillRead}
}

func NewWithModelResolver(
	resolver ModelResolver,
	configInput *Config,
	root *os.Root,
	agents Agents,
	skills Skills,
	defaultAgent string,
) (*Runtime, error) {
	config := *configInput

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

	if !filepath.IsLocal(config.RetainedResultDir) {
		return nil, fmt.Errorf("retained result dir %q must be a relative path inside the spill dir", config.RetainedResultDir)
	}

	agents, errsReviewer := agents.WithoutInvalidReviewers()
	for _, err := range errsReviewer {
		if errReviewer, _ := errors.AsType[*AgentReviewerError](err); errReviewer.Agent == defaultAgent {
			return nil, err
		}
	}

	if _, ok := agents.Items[defaultAgent]; !ok {
		return nil, fmt.Errorf("agent %q is not loaded", defaultAgent)
	}

	prepared := make(map[string]Agent, len(agents.Items))
	for name := range agents.Items {
		agent := agents.Items[name]
		InterpolatePermissions(&agent, config.ShellEnv)
		prepared[name] = agent
	}

	agents.Items = prepared

	agents = skills.withReadPermissions(root, agents)

	activeAgent := agents.Items[defaultAgent]

	sb := config.Sandbox

	promptExpansion, err := newPromptExpansionEnvironment(root, shellTemp, shellEnv, &sb)
	if err != nil {
		return nil, fmt.Errorf("initialize prompt expansion: %w", err)
	}

	workspaceInstructions := fmt.Sprintf("<current-workspace>\nWorkspace root: %s\n</current-workspace>", promptExpansion.hostDir)

	activeAgent.Permission = shellTemp.effectivePermissions(activeAgent.Permission)

	baseTools, shellSystem := newSandboxedTools(root, spillRel, shellTemp, shellEnv, &sb, config.TurnSecrets)
	if len(config.Deciders) > 0 {
		baseTools["decision"] = decisionTool(config.Deciders, config.Observability)
	}

	customTools, err := customLooperTools(config.CustomTools, baseTools)
	if err != nil {
		return nil, err
	}

	maps.Copy(baseTools, customTools)

	mcpRegistry := mcpclient.New(config.MCPWorkspace, config.MCPServers, config.ShellEnv)

	factory := &toolFactory{
		resolver:                  resolver,
		workspaceInstructions:     workspaceInstructions,
		autoApproverModel:         cmp.Or(config.AutoApproverModel, defaultOpenAIModel),
		autoApproverDecisionModel: config.AutoApproverDecisionModel,
		autoApproverMinConfidence: config.AutoApproverMinConfidence,
		deciders:                  config.Deciders,
		parallelToolCalls:         config.ParallelToolCalls,
		expandPromptShellCommands: config.ExpandPromptShellCommands,
		promptExpansion:           promptExpansion,
		agent:                     &activeAgent,
		recursionRemaining:        activeAgent.MaxRecursion,
		agents:                    agents,
		skills:                    skills,
		baseTools:                 baseTools,
		shellSystem:               shellSystem,
		childContext:              config.ChildContext,
		originHeader:              config.OriginHeader,
		shellTemp:                 shellTemp,
		spillRel:                  spillRel,
		retainedRel:               filepath.Join(spillRel, config.RetainedResultDir),
		observability:             config.Observability,
		childSessions:             config.ChildSessions,
		backgroundJobs:            config.BackgroundJobs,
		mcpRegistry:               mcpRegistry,
	}

	looper, err := factory.newLooper(&activeAgent, factory.sessionSystemPrompt(context.Background(), &activeAgent))
	if err != nil {
		return nil, fmt.Errorf("agent %q model: %w", activeAgent.Name, err)
	}

	looper.Journal = config.Journal

	return looper, nil
}

type AgentReviewerError struct {
	Agent, Reviewer string
	Err             error
}

func (e *AgentReviewerError) Error() string {
	return fmt.Sprintf("agent %q: %v", e.Agent, e.Err)
}

func (e *AgentReviewerError) Unwrap() error {
	return e.Err
}

func (a Agents) WithoutInvalidReviewers() (Agents, []error) {
	valid := Agents{Items: maps.Clone(a.Items)}

	var errs []error

	for removed := true; removed; {
		removed = false

		for _, name := range slices.Sorted(maps.Keys(valid.Items)) {
			if err := valid.reviewerError(name); err != nil {
				errs = append(errs, err)
				removed = true

				delete(valid.Items, name)
			}
		}
	}

	return valid, errs
}

func (a Agents) reviewerError(name string) error {
	if name == guardianAgentName {
		return &AgentReviewerError{Agent: name, Reviewer: guardianAgentName, Err: errors.New("the name is reserved for the embedded permission reviewer")}
	}

	agent := a.Items[name]
	for _, bucket := range agent.Permission.Buckets {
		for _, rule := range bucket.Rules {
			if rule.Action != permissionAuto || rule.Reviewer == "" {
				continue
			}

			reviewer, ok := a.Items[rule.Reviewer]

			var reason string

			switch {
			case rule.Reviewer == guardianAgentName:
				reason = fmt.Sprintf("references reserved reviewer %q; use bare auto for the embedded guardian", guardianAgentName)
			case !ok:
				reason = fmt.Sprintf("references missing reviewer agent %q", rule.Reviewer)
			case !delegable(&reviewer, &agent):
				reason = fmt.Sprintf("references sticky: %s reviewer agent %q, which reviews only its own rules", reviewer.Sticky, rule.Reviewer)
			default:
				continue
			}

			return &AgentReviewerError{Agent: name, Reviewer: rule.Reviewer, Err: fmt.Errorf("permission %q rule %q %s", bucket.Name, rule.Pattern, reason)}
		}
	}

	return nil
}
