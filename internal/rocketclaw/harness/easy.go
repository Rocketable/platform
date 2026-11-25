package harness

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"gopkg.in/yaml.v3"
)

type AgentMode string

const (
	AgentModeAdvanced AgentMode = "advanced"
	AgentModeEasy     AgentMode = "easy"
)

func (m AgentMode) Valid() bool { return m == AgentModeEasy || m == AgentModeAdvanced }

type AgentLoadConfig struct {
	MCPFacets         map[string]map[string][]string
	Providers         []string
	DecisionProviders []string
	OpenCodeServers   []string
	Skills            fs.FS
}

func (a *Agent) allowsSubagentModel(model string) bool {
	return !strings.Contains(model, "*") && slices.ContainsFunc(a.Subagents, func(entry string) bool {
		prefix, wildcard := strings.CutSuffix(entry, "*")

		return entry == model || wildcard && len(model) > len(prefix) && strings.HasPrefix(model, prefix)
	})
}

func easySubagents(node *yaml.Node) ([]string, error) {
	subagents, err := easyList(frontmatterField(node, "subagents"), "subagents")
	if err != nil {
		return nil, err
	}

	for _, entry := range subagents {
		if strings.Contains(strings.TrimSuffix(entry, "*"), "*") {
			return nil, fmt.Errorf("subagents: %q: * may only end an entry", entry)
		}
	}

	if subagents == nil {
		subagents = []string{strings.TrimSpace(frontmatterField(node, "model").Value)}
	}

	return subagents, nil
}

func compileEasyAgent(agent *Agent, cfg *AgentLoadConfig) error {
	node, keys := agent.FrontmatterNode, easyKeys()
	if err := checkKeys(node, "", "not an easy-mode key", keys[""]...); err != nil {
		return err
	}

	if agent.Space == "" {
		return errors.New("space: required non-empty string")
	}

	for _, parent := range []string{"approver", "schema"} {
		if err := checkKeys(frontmatterField(node, parent), parent+".", "not an easy-mode key", keys[parent]...); err != nil {
			return err
		}
	}

	maxRecursion := 1

	var approver struct {
		Model  string `yaml:"model"`
		Prompt string `yaml:"prompt"`
	}

	for _, section := range []struct {
		key, shape string
		target     any
	}{
		{"maxRecursion", "an integer of 0 or more", &maxRecursion},
		{"approver", "a mapping of model and prompt strings", &approver},
	} {
		if field := frontmatterField(node, section.key); field != nil && field.Decode(section.target) != nil {
			return fmt.Errorf("%s: must be %s", section.key, section.shape)
		}
	}

	if err := parseEmulatedShell(node, agent); err != nil {
		return err
	}

	if err := errors.Join(parseAgentShell(node, agent), compileEasyApprover(agent, cfg)); err != nil {
		return err
	}

	if maxRecursion < 0 {
		return errors.New("maxRecursion: must be an integer of 0 or more")
	}

	agent.MaxRecursion = &maxRecursion
	agent.ApproverModel, agent.ApproverPrompt = strings.TrimSpace(approver.Model), approver.Prompt

	subagents, err := easySubagents(node)
	if err != nil {
		return err
	}

	agent.Subagents = subagents

	for _, model := range append([]string{agent.Model, agent.ApproverModel}, agent.Subagents...) {
		name, _ := ParseModelRef(model)
		provider, _, qualified := strings.Cut(name, "/")

		if !qualified {
			provider = "openai"
		}

		if model != "" && !slices.Contains(cfg.Providers, provider) {
			return fmt.Errorf("model %q: provider %q is not configured", model, provider)
		}
	}

	if field := frontmatterField(node, "delegatedFrom"); field != nil {
		all := true

		switch {
		case field.ShortTag() == "!!bool" && field.Decode(&all) == nil:
			if !all {
				agent.DelegatedFrom = []string{}
			}
		case field.Kind != yaml.SequenceNode || field.Decode(&agent.DelegatedFrom) != nil:
			return errors.New("delegatedFrom: must be true, false, or a list of agent names")
		}
	}

	return compileEasyPermissions(agent, cfg)
}

func compileEasyPermissions(agent *Agent, cfg *AgentLoadConfig) error {
	agent.Permission = PermissionSet{Easy: true}
	permission := &agent.Permission

	for _, bucket := range []string{"bash", "read", "edit", "glob", "grep", "webfetch", "websearch"} {
		_ = permission.Set(bucket, "*", PermissionAuto)
	}

	var servers []string
	if field := frontmatterField(agent.FrontmatterNode, "opencode"); field != nil && (field.Kind != yaml.SequenceNode || field.Decode(&servers) != nil) {
		return errors.New("opencode: must be a list of servers")
	}

	for _, server := range servers {
		if !slices.Contains(cfg.OpenCodeServers, server) {
			return fmt.Errorf("opencode: server %q is not configured", server)
		}

		_ = permission.Set("opencode", server, PermissionAuto)
	}

	_ = permission.Set("rocketclaw", codeModeApproveSubject, PermissionAuto)

	if len(agent.Subagents) > 0 && *agent.MaxRecursion > 0 {
		_ = permission.Set("task", agent.Name, PermissionAllow)
	}

	if err := compileEasySections(agent, cfg, PermissionAuto); err != nil {
		return err
	}

	return compileEasyDecision(agent, cfg, permission)
}

func compileEasyApprover(agent *Agent, cfg *AgentLoadConfig) error {
	var offUnlessListed yaml.Node

	_ = yaml.Unmarshal([]byte("{rocketclaw: [], skills: [], allowedNetworkTargets: []}"), &offUnlessListed)

	approver := embeddedGuardianAgent()
	approver.Mode, approver.WorkingDirectory = AgentModeEasy, agent.WorkingDirectory
	approver.FrontmatterNode = offUnlessListed.Content[0]

	if node := frontmatterField(agent.FrontmatterNode, "approver"); node != nil {
		approver.FrontmatterNode.Content = slices.Concat(node.Content, approver.FrontmatterNode.Content)
	}

	groups, _ := easyList(frontmatterField(approver.FrontmatterNode, "rocketclaw"), "rocketclaw")
	for _, group := range []string{"background", "restart"} {
		if slices.Contains(groups, group) {
			return fmt.Errorf("approver.rocketclaw: an approver cannot use the %s group", group)
		}
	}

	if err := cmp.Or(parseEmulatedShell(approver.FrontmatterNode, &approver), compileEasySections(&approver, cfg, PermissionAllow)); err != nil {
		return fmt.Errorf("approver.%w", err)
	}

	approver.CustomShell = agent.CustomShell
	_ = approver.Permission.Set("rocketclaw", AllowBackgroundSubject, PermissionDeny)
	_ = approver.Permission.Set("rocketclaw", "rocketclaw_restart", PermissionDeny)

	if len(approver.Commands) > 0 {
		_ = approver.Permission.Set("bash", "*", PermissionAllow)
	}

	if approver.NetworkTargets == nil || len(approver.NetworkTargets) > 0 {
		_ = approver.Permission.Set("webfetch", "*", PermissionAllow)
	}

	agent.Approver = &approver

	return nil
}

func compileEasySections(agent *Agent, cfg *AgentLoadConfig, action PermissionAction) error {
	node, permission := agent.FrontmatterNode, &agent.Permission

	if field := frontmatterField(node, "secretsARN"); field != nil && field.Decode(&agent.SecretsARN) != nil {
		return errors.New("secretsARN: must be a list of ARNs")
	}

	if field := frontmatterField(node, "tags"); field != nil && field.Decode(&agent.TagGroups) != nil {
		return errors.New("tags: must be a list of tag lists")
	}

	field := frontmatterField(node, "mcp")
	if field != nil && field.Kind != yaml.MappingNode {
		return errors.New("mcp: must be a map from server to a list of facets")
	}

	grant := map[string]map[string]PermissionAction{}

	for i := 0; field != nil && i+1 < len(field.Content); i += 2 {
		server := field.Content[i].Value

		var facets []string
		if field.Content[i+1].Kind != yaml.SequenceNode || field.Content[i+1].Decode(&facets) != nil {
			return fmt.Errorf("mcp.%s: must be a list of facets", server)
		}

		grant[server] = map[string]PermissionAction{}
		for _, facet := range facets {
			grant[server][facet] = action
		}
	}

	if err := compileMCPGrant(permission, cfg.MCPFacets, grant); err != nil {
		return fmt.Errorf("mcp: %w", err)
	}

	rocketclawGroups := map[string][][2]string{
		"sessions":   {{"rocketclaw", "rocketclaw_sessions"}},
		"pages":      {{"rocketclaw", "rocketclaw_store"}, {"rocketclaw", "rocketclaw_read"}, {"rocketclaw", "rocketclaw_delete"}, {"rocketclaw", "rocketclaw_list"}, {"rocketclaw", "rocketclaw_kv_store"}, {"rocketclaw", "rocketclaw_kv_read"}},
		"tags":       nil,
		"background": {{"rocketclaw", AllowBackgroundSubject}},
		"restart":    {{"rocketclaw", "rocketclaw_restart"}},
	}

	groups, err := easyList(frontmatterField(node, "rocketclaw"), "rocketclaw")
	if err != nil {
		return err
	}

	if groups == nil {
		groups = slices.Sorted(maps.Keys(rocketclawGroups))
	} else if slices.Contains(groups, "tags") != (len(agent.TagGroups) > 0) {
		return errors.New("tags: required by the tags group in rocketclaw, and only with it")
	}

	for _, group := range groups {
		subjects, ok := rocketclawGroups[group]
		if !ok {
			return fmt.Errorf("rocketclaw: unknown group %q", group)
		}

		for _, subject := range subjects {
			_ = permission.Set(subject[0], subject[1], action)
		}
	}

	folders, err := easyList(frontmatterField(node, "skills"), "skills")
	if err != nil {
		return err
	}

	if folders == nil {
		_ = permission.Set("skill", "*", PermissionAllow)
	}

	if len(folders) == 0 {
		return nil
	}

	skills, _ := discoverSkills(cfg.Skills)

	for _, folder := range folders {
		if info, err := fs.Stat(cfg.Skills, folder); err != nil || !info.IsDir() {
			return fmt.Errorf("skills: folder %q does not exist", folder)
		}

		for _, skill := range skills {
			if path.Base(skill) == "SKILL.md" && strings.HasPrefix(skill, path.Clean(folder)+"/") {
				_ = permission.Set("skill", path.Base(path.Dir(skill)), PermissionAllow)
			}
		}
	}

	return nil
}

func compileEasyDecision(agent *Agent, cfg *AgentLoadConfig, permission *PermissionSet) error {
	field := frontmatterField(agent.FrontmatterNode, "decision")
	if field != nil && field.Kind != yaml.SequenceNode {
		return errors.New("decision: must be a list of provider/model names")
	}

	models, err := easyList(field, "decision")
	if err != nil {
		return err
	}

	if err := checkDecisionModels(cfg, models...); err != nil {
		return err
	}

	for _, model := range models {
		_ = permission.Set("decision", model, PermissionAllow)
	}

	return nil
}

func parseEmulatedShell(node *yaml.Node, agent *Agent) error {
	if field := frontmatterField(node, "workingDirectory"); field != nil && field.Decode(&agent.WorkingDirectory) != nil {
		return errors.New("workingDirectory: must be a string")
	}

	if strings.TrimSpace(agent.WorkingDirectory) == "" {
		return errors.New("workingDirectory: required non-empty string")
	}

	if field := frontmatterField(node, "commands"); field != nil && field.Decode(&agent.Commands) != nil {
		return errors.New("commands: must be a map from command name to command line")
	}

	for name := range agent.Commands {
		if err := sandbox.CheckCommandName(name); err != nil {
			return fmt.Errorf("commands: %w", err)
		}
	}

	targets, err := easyList(frontmatterField(node, "allowedNetworkTargets"), "allowedNetworkTargets")
	for _, target := range targets {
		if !strings.Contains(target, "://") && (strings.HasPrefix(target, "*.") || strings.Contains(target, "/")) {
			return fmt.Errorf("allowedNetworkTargets: %s: wildcards and CIDR ranges are not supported", target)
		}
	}

	if err != nil {
		return err
	}

	agent.NetworkTargets = targets

	agent.CustomShell = CustomShell{Emulated: true}

	return nil
}

func checkKeys(node *yaml.Node, prefix, problem string, keys ...string) error {
	for i := 0; node != nil && i < len(node.Content); i += 2 {
		if !slices.Contains(keys, node.Content[i].Value) {
			return fmt.Errorf("%s%s: %s", prefix, node.Content[i].Value, problem)
		}
	}

	return nil
}

func easyKeys() map[string][]string {
	return map[string][]string{
		"": {"mode", "model", "space", "workingDirectory", "subagents", "maxRecursion", "approver", "secretsARN", "shell", "commands", "mcp", "opencode", "rocketclaw",
			"tags", "skills", "allowedNetworkTargets", "routines", "delegatedFrom", "schema", "sticky", "decision"},
		"approver": {"model", "prompt", "secretsARN", "commands", "mcp", "rocketclaw", "tags", "skills", "allowedNetworkTargets"},
		"schema":   {"output"},
	}
}

func easyList(node *yaml.Node, key string) ([]string, error) {
	if node == nil || node.Kind == yaml.ScalarNode && node.Value == "auto" {
		return nil, nil
	}

	list := []string{}
	if node.Kind != yaml.SequenceNode || node.Decode(&list) != nil {
		return nil, fmt.Errorf("%s: must be auto or a list of strings", key)
	}

	return list, nil
}
