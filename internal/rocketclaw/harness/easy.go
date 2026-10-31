package harness

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentMode selects an agent file's format.
type AgentMode string

const (
	// AgentModeAdvanced is the permission-rule format, used when mode is omitted.
	AgentModeAdvanced AgentMode = "advanced"
	// AgentModeEasy is the section format, compiled into explicit permission rules.
	AgentModeEasy AgentMode = "easy"
)

// AgentLoadConfig is the runtime configuration easy agents are checked and compiled against.
type AgentLoadConfig struct {
	// MCPToolGroups maps every configured MCP server to its tool groups (read, write) of tool-name patterns.
	MCPToolGroups map[string]map[string][]string
	// Providers names every configured model provider, openai included.
	Providers []string
	// Skills is the skills root; easy skills entries name folders in it.
	Skills fs.FS
}

// AgentRoutine is one easy-agent routine: a schedule and the instruction it sends, empty for a bare entry.
type AgentRoutine struct {
	Schedule    string
	Instruction string
}

// compileEasyAgent fills agent's easy-only fields and compiles its sections into explicit rules: a section
// left out or set to auto turns on everything in it behind review, a written section only what it lists.
// Only skill, task and the embedder's default-on tools skip review.
func compileEasyAgent(agent *Agent, cfg AgentLoadConfig) error {
	node := agent.FrontmatterNode
	if err := checkEasyKeys(node, "", "mode", "model", "space", "workingDirectory", "subagents", "maxRecursion", "approver", "secretsARN",
		"commands", "mcp", "rocketclaw", "tags", "skills", "allowedNetworkTargets", "routines", "delegatedFrom", "schema"); err != nil {
		return err
	}

	if agent.Space == "" {
		return errors.New("space: required non-empty string")
	}

	for _, nested := range [][]string{{"approver", "model", "prompt"}, {"schema", "output"}} {
		if err := checkEasyKeys(frontmatterField(node, nested[0]), nested[0]+".", nested[1:]...); err != nil {
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
		{"workingDirectory", "a string", &agent.WorkingDirectory},
		{"maxRecursion", "an integer of 0 or more", &maxRecursion},
		{"approver", "a mapping of model and prompt strings", &approver},
		{"secretsARN", "a list of ARNs", &agent.SecretsARN},
		{"commands", "a map from command name to command line", &agent.Commands},
		{"tags", "a list of tag lists", &agent.TagGroups},
	} {
		if field := frontmatterField(node, section.key); field != nil && field.Decode(section.target) != nil {
			return fmt.Errorf("%s: must be %s", section.key, section.shape)
		}
	}

	if strings.TrimSpace(agent.WorkingDirectory) == "" {
		return errors.New("workingDirectory: required non-empty string")
	}

	if maxRecursion < 0 {
		return errors.New("maxRecursion: must be an integer of 0 or more")
	}

	// The embedder sets up the emulated shell per turn; until it does, shell commands do not run.
	agent.CustomShell = CustomShell{Emulated: true}
	agent.MaxRecursion = &maxRecursion
	agent.ApproverModel, agent.ApproverPrompt = strings.TrimSpace(approver.Model), approver.Prompt

	subagents, err := easyList(frontmatterField(node, "subagents"), "subagents")
	if err != nil {
		return err
	}

	if subagents == nil {
		subagents = []string{strings.TrimSpace(frontmatterField(node, "model").Value)}
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

	if agent.NetworkTargets, err = easyNetworkTargets(frontmatterField(node, "allowedNetworkTargets")); err != nil {
		return err
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

	if field := frontmatterField(node, "routines"); field != nil {
		if field.Kind != yaml.SequenceNode {
			return errors.New("routines: must be a list of schedules and one-entry maps from schedule to instruction")
		}

		for _, entry := range field.Content {
			switch {
			case entry.Kind == yaml.ScalarNode:
				agent.Routines = append(agent.Routines, AgentRoutine{Schedule: entry.Value})
			case entry.Kind == yaml.MappingNode && len(entry.Content) == 2 && entry.Content[1].Kind == yaml.ScalarNode:
				agent.Routines = append(agent.Routines, AgentRoutine{Schedule: entry.Content[0].Value, Instruction: entry.Content[1].Value})
			default:
				return errors.New("routines: must be a list of schedules and one-entry maps from schedule to instruction")
			}
		}
	}

	agent.Permission, err = compileEasyPermissions(agent, cfg)

	return err
}

// compileEasyPermissions builds agent's permission set from its mcp, rocketclaw and skills sections and
// its subagents. Tag groups compile in the embedder, which owns the tag tools.
func compileEasyPermissions(agent *Agent, cfg AgentLoadConfig) (PermissionSet, error) {
	node := agent.FrontmatterNode
	permission := PermissionSet{Easy: true}

	for _, bucket := range []string{"bash", "read", "edit", "glob", "grep", "webfetch", "websearch", "opencode"} {
		_ = permission.Set(bucket, "*", PermissionAuto)
	}

	_ = permission.Set("rocketclaw", codeModeApproveSubject, PermissionAuto)
	_ = permission.Deny("rocketclaw", "load_agents_md")

	if len(agent.Subagents) > 0 && *agent.MaxRecursion > 0 {
		_ = permission.Allow("task", agent.Name)
	}

	field := frontmatterField(node, "mcp")
	if field == nil || field.Kind == yaml.ScalarNode && field.Value == "auto" {
		_ = permission.Set("mcp", "*", PermissionAuto)
	} else if field.Kind != yaml.MappingNode {
		return PermissionSet{}, errors.New("mcp: must be auto or a map from server to auto or tool groups")
	}

	for i := 0; field != nil && i+1 < len(field.Content); i += 2 {
		server := field.Content[i].Value

		groups, ok := cfg.MCPToolGroups[server]
		if !ok {
			return PermissionSet{}, fmt.Errorf("mcp: server %q is not configured", server)
		}

		names, err := easyList(field.Content[i+1], "mcp."+server)
		if err != nil {
			return PermissionSet{}, err
		}

		if names == nil {
			_ = permission.Set("mcp", server+".*", PermissionAuto)
		}

		for _, name := range names {
			patterns := groups[name]
			if len(patterns) == 0 {
				return PermissionSet{}, fmt.Errorf("mcp: server %q has no %q tool group", server, name)
			}

			for _, pattern := range patterns {
				_ = permission.Set("mcp", server+"."+pattern, PermissionAuto)
			}
		}
	}

	rocketclawGroups := map[string][][2]string{
		"sessions":   {{"rocketclaw", "rocketclaw_sessions"}},
		"pages":      {{"rocketclaw", "rocketclaw_store"}, {"rocketclaw", "rocketclaw_read"}, {"rocketclaw", "rocketclaw_delete"}, {"rocketclaw", "rocketclaw_list"}, {"rocketclaw", "rocketclaw_kv_store"}, {"rocketclaw", "rocketclaw_kv_read"}},
		"tags":       nil,
		"background": {{"rocketclaw", AllowBackgroundSubject}},
		"workflow":   {{"workflow", "*"}},
		"restart":    {{"rocketclaw", "rocketclaw_restart"}},
	}

	groups, err := easyList(frontmatterField(node, "rocketclaw"), "rocketclaw")
	if err != nil {
		return PermissionSet{}, err
	}

	if groups == nil {
		groups = slices.Sorted(maps.Keys(rocketclawGroups))
	} else if slices.Contains(groups, "tags") != (len(agent.TagGroups) > 0) {
		return PermissionSet{}, errors.New("tags: required by the tags group in rocketclaw, and only with it")
	}

	for _, group := range groups {
		subjects, ok := rocketclawGroups[group]
		if !ok {
			return PermissionSet{}, fmt.Errorf("rocketclaw: unknown group %q", group)
		}

		for _, subject := range subjects {
			_ = permission.Set(subject[0], subject[1], PermissionAuto)
		}
	}

	folders, err := easyList(frontmatterField(node, "skills"), "skills")
	if err != nil {
		return PermissionSet{}, err
	}

	if folders == nil {
		_ = permission.Allow("skill", "*")
	}

	for _, folder := range folders {
		if fs.WalkDir(cfg.Skills, folder, func(name string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() && path.Base(name) == "SKILL.md" {
				_ = permission.Allow("skill", path.Base(path.Dir(name)))
			}

			return err
		}) != nil {
			return PermissionSet{}, fmt.Errorf("skills: folder %q does not exist", folder)
		}
	}

	return permission, nil
}

// easyNetworkTargets decodes allowedNetworkTargets node and refuses, when the file loads, the entries the
// wrapper refuses on every command: its just-bash matches origins and URL prefixes only.
func easyNetworkTargets(node *yaml.Node) ([]string, error) {
	targets, err := easyList(node, "allowedNetworkTargets")
	for _, target := range targets {
		if !strings.Contains(target, "://") && (strings.HasPrefix(target, "*.") || strings.Contains(target, "/")) {
			return nil, fmt.Errorf("allowedNetworkTargets: %s: wildcards and CIDR ranges are not supported", target)
		}
	}

	return targets, err
}

// checkEasyKeys reports the first key of mapping node, named under prefix, that keys does not list.
// An absent node, nil, has no keys.
func checkEasyKeys(node *yaml.Node, prefix string, keys ...string) error {
	for i := 0; node != nil && i < len(node.Content); i += 2 {
		if !slices.Contains(keys, node.Content[i].Value) {
			return fmt.Errorf("%s%s: not an easy-mode key", prefix, node.Content[i].Value)
		}
	}

	return nil
}

// easyList decodes an omitted section, or one set to auto, as nil, and a written section as its list.
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
