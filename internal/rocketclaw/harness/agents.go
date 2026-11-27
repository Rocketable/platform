package harness

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/mail"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/google/jsonschema-go/jsonschema"
	"gopkg.in/yaml.v3"
)

var AgentFrontmatterPattern = regexp.MustCompile(`\A---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|\z)`)

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
	FrontmatterNode *yaml.Node
	FileMode        fs.FileMode
	Sticky          AgentSticky
	Identity        string
	ReplyHint       string

	WorkingDirectory string
	Commands         map[string]string
	SecretsARN       []string
	CloneModels      []string
	ApproverModel    string
	ApproverPrompt   string
	Approver         *Agent
	Routines         []AgentRoutine
	TagGroups        [][]string
	NetworkTargets   []string
	Tools            []string
}

type AgentRoutine struct {
	Schedule    config.RoutineSchedule
	Instruction string
}

type AgentSticky string

const (
	AgentStickyOff      AgentSticky = ""
	AgentStickyUser     AgentSticky = "user"
	AgentStickyGlobal   AgentSticky = "global"
	AgentStickySlack    AgentSticky = "slack"
	AgentStickyIdentity AgentSticky = "identity"
)

type Agents struct {
	Items map[string]Agent
}

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

func (a Agents) PrivateFrom(name, login string) bool {
	identity := a.Items[name].Identity

	return identity != "" && identity != login
}

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

type AgentLoadResult struct {
	Agents Agents
	Errors []error
}

type AgentLoadError struct {
	Path string
	Err  error
}

func (e *AgentLoadError) Error() string {
	return e.Path + ": " + e.Err.Error()
}

func (e *AgentLoadError) Unwrap() error {
	return e.Err
}

func LoadAgents(fsys fs.FS, cfg *AgentLoadConfig) AgentLoadResult {
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
			result.Errors = append(result.Errors, &AgentLoadError{Path: filePath, Err: err})
			continue
		}

		if existing, ok := result.Agents.Items[agent.Name]; ok {
			result.Errors = append(result.Errors, fmt.Errorf("%s: duplicate agent name %q overrides %s", filePath, agent.Name, existing.Location))
		}

		result.Agents.Items[agent.Name] = agent
	}

	return result
}

func ParseModelRef(ref string) (model, effort string) {
	model, effort, _ = strings.Cut(ref, "#")

	return strings.TrimSpace(model), effort
}

func loadAgent(fsys fs.FS, filePath string, cfg *AgentLoadConfig) (Agent, error) {
	data, err := fs.ReadFile(fsys, filePath)
	if err != nil {
		return Agent{}, fmt.Errorf("read agent: %w", err)
	}

	info, err := fs.Stat(fsys, filePath)
	if err != nil {
		return Agent{}, fmt.Errorf("stat agent: %w", err)
	}

	frontmatter, frontmatterNode, prompt, err := parseAgentFrontmatter(data)
	if err != nil {
		return Agent{}, err
	}

	mode := AgentMode(frontmatterValue(frontmatterNode, "mode", string(AgentModeAdvanced)))
	if !mode.Valid() {
		return Agent{}, errors.New("mode: must be easy or advanced")
	}

	if err := checkKeys(frontmatterNode, mode, ""); err != nil {
		return Agent{}, err
	}

	stickyValue, identity := frontmatterValue(frontmatterNode, "sticky", "off"), ""

	sticky, ok := map[string]AgentSticky{"off": AgentStickyOff, "user": AgentStickyUser, "global": AgentStickyGlobal, "slack": AgentStickySlack}[stickyValue]
	if !ok {
		if address, err := mail.ParseAddress(stickyValue); err != nil || address.Address != stickyValue {
			return Agent{}, errors.New("sticky: must be off, user, global, slack or one email address")
		}

		sticky, identity = AgentStickyIdentity, stickyValue
	}

	name := strings.TrimSuffix(filePath, filepath.Ext(filePath))
	if name == "" {
		return Agent{}, errors.New("empty agent name")
	}

	modelField := frontmatterField(frontmatterNode, "model")
	if modelField == nil || modelField.Kind != yaml.ScalarNode || modelField.ShortTag() != "!!str" {
		return Agent{}, errors.New("model: required non-empty string")
	}

	model, modelEffort := ParseModelRef(modelField.Value)
	if model == "" {
		return Agent{}, errors.New("model: required non-empty string")
	}

	var space string

	if field := frontmatterField(frontmatterNode, "space"); field != nil {
		if field.Kind != yaml.ScalarNode || field.ShortTag() != "!!str" {
			return Agent{}, errors.New("space: must be a string")
		}

		space = field.Value
	}

	outputSchema, err := parseAgentOutputSchema(frontmatterField(frontmatterNode, "schema"))
	if err != nil {
		return Agent{}, err
	}

	routines, err := parseAgentRoutines(frontmatterField(frontmatterNode, "routines"), sticky)
	if err != nil {
		return Agent{}, err
	}

	maxRecursion := 1
	if field := frontmatterField(frontmatterNode, "maxRecursion"); field != nil && (field.Kind != yaml.ScalarNode || field.ShortTag() != "!!int" || field.Decode(&maxRecursion) != nil || maxRecursion < 0) {
		return Agent{}, errors.New("maxRecursion: must be an integer of 0 or more")
	}

	agent := Agent{Name: name, Model: model, ReasoningEffort: modelEffort, MaxRecursion: &maxRecursion, Space: space, Prompt: strings.TrimSpace(prompt), Location: filePath, OutputSchema: outputSchema, FrontmatterNode: frontmatterNode, FileMode: info.Mode(), Sticky: sticky, Identity: identity, Routines: routines}
	if mode == AgentModeEasy {
		if err := compileEasyAgent(&agent, cfg); err != nil {
			return Agent{}, err
		}

		return agent, nil
	}

	if err := compileAdvancedAgent(&agent, cfg, frontmatter); err != nil {
		return Agent{}, err
	}

	return agent, nil
}

func compileAdvancedAgent(agent *Agent, cfg *AgentLoadConfig, frontmatter map[string]any) error {
	node := agent.FrontmatterNode

	var sudo bool

	if field := frontmatterField(node, "sudo"); field != nil {
		switch strings.ToLower(field.Value) {
		case "true", "1", "on", "yes":
			sudo = true
		case "false", "0", "off", "no":
		default:
			return errors.New("sudo: must be one of true/1/on/yes or false/0/off/no")
		}
	}

	var err error
	if agent.Permission, err = parsePermissionNode(frontmatterField(node, "permission"), cfg.MCPFacets); err != nil {
		return fmt.Errorf("parse permission: %w", err)
	}

	if sudo {
		agent.Permission = sudoPermissions(cfg.MCPFacets, agent.Permission)
	}

	if err := parseAgentShell(node, agent); err != nil {
		return err
	}

	field := frontmatterField(frontmatterField(frontmatterField(node, "permission"), "rocketclaw"), "rocketclaw_set_tag")
	if field != nil && field.ShortTag() != "!!str" && (field.Decode(&agent.TagGroups) != nil || agent.TagGroups == nil) {
		return errors.New("permission.rocketclaw.rocketclaw_set_tag: must be a list of tag lists")
	}

	if err := checkTagGroups(agent.TagGroups); err != nil {
		return fmt.Errorf("permission.rocketclaw.rocketclaw_set_tag: %w", err)
	}

	if err := checkModelProviders(cfg, agent.Model); err != nil {
		return err
	}

	agent.Description = argString(frontmatter, "description")
	agent.ReasoningEffort = cmp.Or(agent.ReasoningEffort, argString(frontmatter, "reasoningEffort"))
	agent.Verbosity = argString(frontmatter, "verbosity")

	if hint := argString(frontmatter, "additionalInstructions"); strings.TrimSpace(hint) != "" {
		agent.ReplyHint = hint
	}

	return nil
}

func checkTagGroups(groups [][]string) error {
	var names []string

	for _, group := range groups {
		if len(group) == 0 {
			return errors.New("tag groups must not be empty")
		}

		for _, name := range group {
			if name == "" || slices.Contains(names, name) {
				return fmt.Errorf("empty or duplicate tag name %q", name)
			}

			names = append(names, name)
		}
	}

	return nil
}

func checkModelProviders(cfg *AgentLoadConfig, models ...string) error {
	for _, model := range models {
		name, _ := ParseModelRef(model)
		if provider, _, qualified := strings.Cut(name, "/"); qualified && !slices.Contains(cfg.Providers, provider) {
			return fmt.Errorf("model %q: provider %q is not configured", model, provider)
		}
	}

	return nil
}

func checkDecisionModels(cfg *AgentLoadConfig, models ...string) error {
	for _, model := range models {
		if provider, _, _ := strings.Cut(model, "/"); model != "" && !slices.Contains(cfg.DecisionProviders, provider) {
			return fmt.Errorf("decision model %q: provider %q is not configured in decision", model, provider)
		}
	}

	return nil
}

func parseAgentFrontmatter(content []byte) (frontmatter map[string]any, frontmatterNode *yaml.Node, prompt string, err error) {
	match := AgentFrontmatterPattern.FindSubmatchIndex(content)
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

func sudoPermissions(facets map[string]map[string][]string, rules PermissionSet) PermissionSet {
	permissions := PermissionSet{Sudo: true}
	_ = permissions.Set("skill", "*", PermissionAllow)
	_ = permissions.Set("task", "*", PermissionAllow)
	_ = permissions.Set(mcpPermissionBucket, "*", PermissionDeny)

	everyFacet := map[string]map[string]PermissionAction{}
	for server := range facets {
		everyFacet[server] = map[string]PermissionAction{}
		for facet := range facets[server] {
			everyFacet[server][facet] = PermissionAuto
		}
	}

	_ = compileMCPGrant(&permissions, facets, everyFacet)

	for _, bucket := range rules.Buckets {
		if i := slices.IndexFunc(permissions.Buckets, func(sudoBucket PermissionBucket) bool { return sudoBucket.Name == bucket.Name }); i >= 0 {
			permissions.Buckets[i].Rules = append(permissions.Buckets[i].Rules, bucket.Rules...)
		} else {
			permissions.Buckets = append(permissions.Buckets, bucket)
		}
	}

	return permissions
}

func frontmatterField(frontmatter *yaml.Node, key string) *yaml.Node {
	for i := 0; frontmatter != nil && i+1 < len(frontmatter.Content); i += 2 {
		if frontmatter.Content[i].Value == key {
			return frontmatter.Content[i+1]
		}
	}

	return nil
}

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

		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}

		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key == "" || value == "" || value == ">" || value == "|" || strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") || !strings.Contains(value, ":") {
			continue
		}

		lines[i] = key + ": |-\n  " + value
	}

	return strings.Join(lines, "\n")
}

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

func parseAgentRoutines(field *yaml.Node, sticky AgentSticky) ([]AgentRoutine, error) {
	if field == nil {
		return nil, nil
	}

	if slices.Contains([]AgentSticky{AgentStickyUser, AgentStickySlack}, sticky) {
		return nil, fmt.Errorf("routines: a sticky: %s agent cannot have routines", sticky)
	}

	errShape := errors.New("routines: must be a list of schedules and one-entry maps from schedule to instruction")
	if field.Kind != yaml.SequenceNode {
		return nil, errShape
	}

	var routines []AgentRoutine

	for _, entry := range field.Content {
		var raw, instruction string

		switch {
		case entry.Kind == yaml.ScalarNode:
			raw = entry.Value
		case entry.Kind == yaml.MappingNode && len(entry.Content) == 2 && entry.Content[1].Kind == yaml.ScalarNode:
			raw, instruction = entry.Content[0].Value, entry.Content[1].Value
		default:
			return nil, errShape
		}

		schedule, err := config.ParseRoutineSchedule(raw)
		if err != nil {
			return nil, fmt.Errorf("routines: schedule %q: %w", raw, err)
		}

		if slices.ContainsFunc(routines, func(routine AgentRoutine) bool { return routine.Schedule.Raw == schedule.Raw }) {
			return nil, fmt.Errorf("routines: schedule %q is listed twice", schedule.Raw)
		}

		routines = append(routines, AgentRoutine{Schedule: schedule, Instruction: instruction})
	}

	return routines, nil
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
