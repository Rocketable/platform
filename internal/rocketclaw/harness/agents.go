package harness

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"gopkg.in/yaml.v3"
)

var agentFrontmatterPattern = regexp.MustCompile(`\A---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|\z)`)

// Agent contains a single preloaded markdown agent definition.
type Agent struct {
	Name            string
	Description     string
	Model           string
	ReasoningEffort string
	Verbosity       string
	MaxRecursion    *int
	CustomShell     CustomShell
	Space           string
	Prompt          string
	Location        string
	Permission      PermissionSet
	OutputSchema    map[string]any
	// ApproverDecision is the decision model asked before an approver. An easy agent's guards the guardian's
	// reviews of its own calls; an advanced agent's guards its reviews of others as auto(<agent>).
	ApproverDecision ApproverDecision
	Frontmatter      map[string]any
	FrontmatterNode  *yaml.Node
	FileMode         fs.FileMode
	Mode             AgentMode
	Sticky           AgentSticky

	// Easy-only fields, compiled from an easy file's sections; advanced agents leave them zero.
	// Model entries keep their text as written, provider prefix and #effort included.
	WorkingDirectory string
	Commands         map[string]string
	SecretsARN       []string
	Subagents        []string
	ApproverModel    string
	ApproverPrompt   string
	Routines         []AgentRoutine
	TagGroups        [][]string
	// DelegatedFrom lists the agents that may write into this agent's sessions; nil means any agent.
	DelegatedFrom []string
	// NetworkTargets lists the emulated shell's allowed network targets; nil means public addresses only.
	NetworkTargets []string
	// Tools, set by the embedder for an easy agent's subagent session, names the only tools it has: tool names
	// and MCP server.tool subjects. Nil means every tool.
	Tools []string
}

// AgentSticky is how many Web sessions an agent may have.
type AgentSticky string

const (
	// AgentStickyOff lets every new Web session start fresh; frontmatter writes it off or leaves it out.
	AgentStickyOff AgentSticky = ""
	// AgentStickyUser keeps one Web session per person: a new one resumes theirs.
	AgentStickyUser AgentSticky = "user"
	// AgentStickyGlobal keeps one Web session shared by everyone: a new one resumes it.
	AgentStickyGlobal AgentSticky = "global"
)

// Agents contains all discovered agents keyed by name.
type Agents struct {
	Items map[string]Agent
}

// PrimaryNames returns sorted top-level agents, excluding comparison shadows.
func (a Agents) PrimaryNames() []string {
	var names []string

	for name := range a.Items {
		if !strings.Contains(name, "/") {
			names = append(names, name)
		}
	}

	slices.Sort(names)

	return names
}

// ShadowNames returns sorted immediate shadow names for a top-level parent.
func (a Agents) ShadowNames(parent string) []string {
	var names []string

	for name := range a.Items {
		if path.Dir(name) == parent {
			names = append(names, name)
		}
	}

	slices.Sort(names)

	return names
}

// AgentLoadResult contains loaded agents and any non-fatal load errors.
type AgentLoadResult struct {
	Agents Agents
	Errors []error
}

// AgentMaxRecursionError reports invalid maxRecursion frontmatter.
type AgentMaxRecursionError struct {
	Location string
	Err      error
}

func (e *AgentMaxRecursionError) Error() string {
	return fmt.Sprintf("%s: parse maxRecursion: %v", e.Location, e.Err)
}

func (e *AgentMaxRecursionError) Unwrap() error {
	return e.Err
}

// LoadAgents loads top-level Markdown agents and immediate shadows in each
// matching directory. Shadow names retain their parent path. Easy agents are checked and
// compiled against cfg.
func LoadAgents(fsys fs.FS, cfg AgentLoadConfig) AgentLoadResult {
	result := AgentLoadResult{
		Agents: Agents{Items: map[string]Agent{}},
		Errors: nil,
	}

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("read agents dir: %w", err))
		return result
	}

	var files []string

	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".md" {
			continue
		}

		files = append(files, entry.Name())
		dir := strings.TrimSuffix(entry.Name(), ".md")

		info, err := fs.Stat(fsys, dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err == nil && !info.IsDir() {
			continue
		}

		var children []fs.DirEntry
		if err == nil {
			children, err = fs.ReadDir(fsys, dir)
		}

		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("%s: read shadow agents: %w", dir, err))
			continue
		}

		for _, child := range children {
			if !child.IsDir() && path.Ext(child.Name()) == ".md" {
				files = append(files, path.Join(dir, child.Name()))
			}
		}
	}

	for _, filePath := range files {
		agent, err := loadAgent(fsys, filePath, cfg)
		if err != nil {
			result.Errors = append(result.Errors, err)
			continue
		}

		if existing, ok := result.Agents.Items[agent.Name]; ok {
			result.Errors = append(result.Errors, fmt.Errorf("%s: duplicate agent name %q overrides %s", filePath, agent.Name, existing.Location))
		}

		result.Agents.Items[agent.Name] = agent
	}

	return result
}

// ParseModelRef splits a "model#effort" reference into its trimmed model and its effort, empty without "#".
func ParseModelRef(ref string) (model, effort string) {
	model, effort, _ = strings.Cut(ref, "#")

	return strings.TrimSpace(model), effort
}

func loadAgent(fsys fs.FS, filePath string, cfg AgentLoadConfig) (Agent, error) {
	data, err := fs.ReadFile(fsys, filePath)
	if err != nil {
		return Agent{}, fmt.Errorf("%s: read agent: %w", filePath, err)
	}

	info, err := fs.Stat(fsys, filePath)
	if err != nil {
		return Agent{}, fmt.Errorf("%s: stat agent: %w", filePath, err)
	}

	frontmatter, frontmatterNode, prompt, err := parseAgentFrontmatter(data)
	if err != nil {
		return Agent{}, fmt.Errorf("%s: %w", filePath, err)
	}

	// Non-scalar nodes have an empty Value and fail both checks too.
	mode := AgentMode(frontmatterValue(frontmatterNode, "mode", string(AgentModeAdvanced)))
	if !slices.Contains([]AgentMode{AgentModeAdvanced, AgentModeEasy}, mode) {
		return Agent{}, fmt.Errorf("%s: mode: must be easy or advanced", filePath)
	}

	sticky, ok := map[string]AgentSticky{"off": AgentStickyOff, "user": AgentStickyUser, "global": AgentStickyGlobal}[frontmatterValue(frontmatterNode, "sticky", "off")]
	if !ok {
		return Agent{}, fmt.Errorf("%s: sticky: must be off, user or global", filePath)
	}

	name := strings.TrimSuffix(filePath, filepath.Ext(filePath))
	if name == "" {
		return Agent{}, fmt.Errorf("%s: empty agent name", filePath)
	}

	modelField := frontmatterField(frontmatterNode, "model")
	if modelField == nil || modelField.Kind != yaml.ScalarNode || modelField.ShortTag() != "!!str" || strings.TrimSpace(modelField.Value) == "" {
		return Agent{}, fmt.Errorf("%s: model: required non-empty string", filePath)
	}

	model, modelEffort := ParseModelRef(modelField.Value)
	if model == "" {
		return Agent{}, fmt.Errorf("%s: model: required non-empty string", filePath)
	}

	var space string

	if field := frontmatterField(frontmatterNode, "space"); field != nil {
		if field.Kind != yaml.ScalarNode || field.ShortTag() != "!!str" {
			return Agent{}, fmt.Errorf("%s: space: must be a string", filePath)
		}

		space = field.Value
	}

	outputSchema, err := parseAgentOutputSchema(frontmatterField(frontmatterNode, "schema"))
	if err != nil {
		return Agent{}, fmt.Errorf("%s: %w", filePath, err)
	}

	agent := Agent{Name: name, Model: model, ReasoningEffort: modelEffort, Space: space, Prompt: strings.TrimSpace(prompt), Location: filePath, OutputSchema: outputSchema, Frontmatter: frontmatter, FrontmatterNode: frontmatterNode, FileMode: info.Mode(), Mode: mode, Sticky: sticky}
	if mode == AgentModeEasy {
		if err := compileEasyAgent(&agent, cfg); err != nil {
			return Agent{}, fmt.Errorf("%s: %w", filePath, err)
		}

		return agent, nil
	}

	var sudo bool

	if field := frontmatterField(frontmatterNode, "sudo"); field != nil {
		// Non-scalar nodes have an empty Value and fail here too.
		switch strings.ToLower(field.Value) {
		case "true", "1", "on", "yes":
			sudo = true
		case "false", "0", "off", "no":
		default:
			return Agent{}, fmt.Errorf("%s: sudo: must be one of true/1/on/yes or false/0/off/no", filePath)
		}
	}

	if sudo {
		// Sudo replaces the permission block; skills and delegation skip review.
		agent.Permission.Sudo = true
		_ = agent.Permission.Allow("skill", "*")
		_ = agent.Permission.Allow("task", "*")
	} else if agent.Permission, err = parsePermissionNode(frontmatterField(frontmatterNode, "permission")); err != nil {
		return Agent{}, fmt.Errorf("%s: parse permission: %w", filePath, err)
	}

	if field := frontmatterField(frontmatterNode, "maxRecursion"); field != nil {
		if field.Kind != yaml.ScalarNode || field.ShortTag() != "!!int" {
			return Agent{}, &AgentMaxRecursionError{Location: filePath, Err: errors.New("must be an integer greater than or equal to -1")}
		}

		var value int
		if err := field.Decode(&value); err != nil {
			return Agent{}, &AgentMaxRecursionError{Location: filePath, Err: err}
		}

		if value < -1 {
			return Agent{}, &AgentMaxRecursionError{Location: filePath, Err: errors.New("must be an integer greater than or equal to -1")}
		}

		if value >= 0 {
			agent.MaxRecursion = &value
		}
	}

	if err := cmp.Or(parseAgentShell(frontmatterNode, &agent), parseAgentApprover(frontmatterNode, &agent, cfg)); err != nil {
		return Agent{}, fmt.Errorf("%s: %w", filePath, err)
	}

	agent.Description = argString(frontmatter, "description")
	agent.ReasoningEffort = cmp.Or(modelEffort, argString(frontmatter, "reasoningEffort"))
	agent.Verbosity = argString(frontmatter, "verbosity")

	return agent, nil
}

// ApproverDecision is an approver.decision block: the decision model asked before the approver, the
// confidence each answer needs, and rules added to the decision model's prompt.
type ApproverDecision struct {
	Model         string  `yaml:"model"`
	MinConfidence float64 `yaml:"minConfidence"`
	Prompt        string  `yaml:"prompt"`
}

// parseAgentApprover reads an advanced agent's approver block, which holds only its settings as another
// agent's auto(<agent>) reviewer.
func parseAgentApprover(node *yaml.Node, agent *Agent, cfg AgentLoadConfig) error {
	field := frontmatterField(node, "approver")
	if field != nil && field.Kind != yaml.MappingNode {
		return errors.New("approver: must be a mapping with only decision")
	}

	if err := checkKeys(field, "approver.", "advanced agents take only approver.decision", "decision"); err != nil {
		return err
	}

	return parseApproverDecision(field, agent, cfg)
}

// parseApproverDecision reads the decision block of approver, an agent's approver block or nil, into
// agent.ApproverDecision; a missing one leaves it zero.
func parseApproverDecision(approver *yaml.Node, agent *Agent, cfg AgentLoadConfig) error {
	if approver == nil {
		return nil
	}

	node := frontmatterField(approver, "decision")
	if node == nil {
		return nil
	}

	errShape := errors.New("approver.decision: must be a mapping of model and prompt strings and a minConfidence number")
	if node.Kind != yaml.MappingNode {
		return errShape
	}

	if err := checkKeys(node, "approver.decision.", "not an approver.decision key", "model", "minConfidence", "prompt"); err != nil {
		return err
	}

	if node.Decode(&agent.ApproverDecision) != nil {
		return errShape
	}

	if decision := &agent.ApproverDecision; decision.MinConfidence < 0 || decision.MinConfidence > 1 {
		return errors.New("approver.decision.minConfidence: must be between 0 and 1")
	}

	agent.ApproverDecision.Model = strings.TrimSpace(agent.ApproverDecision.Model)

	return checkDecisionModels(cfg, agent.ApproverDecision.Model)
}

// checkDecisionModels reports the first non-empty provider/model whose provider is not configured.
func checkDecisionModels(cfg AgentLoadConfig, models ...string) error {
	for _, model := range models {
		if provider, _, _ := strings.Cut(model, "/"); model != "" && !slices.Contains(cfg.DecisionProviders, provider) {
			return fmt.Errorf("decision model %q: provider %q is not configured in decision", model, provider)
		}
	}

	return nil
}

func parseAgentFrontmatter(content []byte) (frontmatter map[string]any, frontmatterNode *yaml.Node, prompt string, err error) {
	match := agentFrontmatterPattern.FindSubmatchIndex(content)
	if match == nil {
		return nil, nil, "", errors.New("missing YAML frontmatter")
	}

	frontmatterText := content[match[2]:match[3]]

	frontmatter, frontmatterNode, err = decodeAgentFrontmatter(frontmatterText)
	if err != nil {
		frontmatter, frontmatterNode, err = decodeAgentFrontmatter([]byte(sanitizeAgentFrontmatter(string(frontmatterText))))
		if err != nil {
			return nil, nil, "", fmt.Errorf("parse YAML frontmatter: %w", err)
		}
	}

	return frontmatter, frontmatterNode, string(content[match[1]:]), nil
}

func decodeAgentFrontmatter(frontmatterText []byte) (map[string]any, *yaml.Node, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(frontmatterText, &node); err != nil {
		return nil, nil, fmt.Errorf("unmarshal YAML frontmatter: %w", err)
	}

	if len(node.Content) == 0 || node.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errors.New("unmarshal YAML frontmatter: expected mapping")
	}

	var frontmatter map[string]any
	if err := node.Decode(&frontmatter); err != nil {
		return nil, nil, fmt.Errorf("unmarshal YAML frontmatter: %w", err)
	}

	return frontmatter, node.Content[0], nil
}

func frontmatterField(frontmatter *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(frontmatter.Content); i += 2 {
		if frontmatter.Content[i].Value == key {
			return frontmatter.Content[i+1]
		}
	}

	return nil
}

// frontmatterValue is key's scalar value, or fallback when key is absent. A non-scalar's value is empty.
func frontmatterValue(frontmatter *yaml.Node, key, fallback string) string {
	if field := frontmatterField(frontmatter, key); field != nil {
		return field.Value
	}

	return fallback
}

func sanitizeAgentFrontmatter(frontmatterText string) string {
	lines := strings.Split(frontmatterText, "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])

		value := strings.TrimSpace(parts[1])
		if key == "" || value == "" || value == ">" || value == "|" || strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") || !strings.Contains(value, ":") {
			continue
		}

		lines[i] = key + ": |-\n  " + value
	}

	return strings.Join(lines, "\n")
}

// parseAgentShell reads the shell frontmatter field, an absolute program path or emulated, and for emulated
// the shell's other settings.
func parseAgentShell(node *yaml.Node, agent *Agent) error {
	field := frontmatterField(node, "shell")
	if field == nil {
		return nil
	}

	if field.Kind == yaml.ScalarNode && field.ShortTag() == "!!str" {
		if field.Value == "emulated" {
			agent.CustomShell = CustomShell{Emulated: true}

			return parseEmulatedShell(node, agent)
		}

		if filepath.IsAbs(field.Value) {
			agent.CustomShell = CustomShell{Path: field.Value}

			return nil
		}
	}

	return errors.New("shell: must be an absolute path or emulated")
}

func parseAgentOutputSchema(schema *yaml.Node) (map[string]any, error) {
	if schema == nil {
		return nil, nil
	}

	if schema.Kind != yaml.MappingNode {
		return nil, errors.New("schema: must be a mapping")
	}

	output := frontmatterField(schema, "output")
	if output == nil || output.ShortTag() == "!!null" {
		return nil, nil
	}

	if output.Kind != yaml.MappingNode {
		return nil, errors.New("schema.output: must be a mapping")
	}

	var decoded any
	if err := output.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("schema.output: %w", err)
	}

	raw, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("schema.output: %w", err)
	}

	var definition jsonschema.Schema
	if err := json.Unmarshal(raw, &definition); err != nil {
		return nil, fmt.Errorf("schema.output: %w", err)
	}

	if _, err := definition.Resolve(nil); err != nil {
		return nil, fmt.Errorf("schema.output: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("schema.output: %w", err)
	}

	return result, nil
}
