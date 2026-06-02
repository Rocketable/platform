package rocketcode

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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
	Prompt          string
	Location        string
	Permission      PermissionSet
	Frontmatter     map[string]any
	FileMode        fs.FileMode
}

// Agents contains all discovered agents keyed by name.
type Agents struct {
	Items map[string]Agent
}

// AgentLoadResult contains loaded agents and any non-fatal load errors.
type AgentLoadResult struct {
	Agents Agents
	Errors []error
}

// LoadAgents scans the top level of fsys for markdown agent files.
func LoadAgents(fsys fs.FS) AgentLoadResult {
	result := AgentLoadResult{
		Agents: Agents{Items: map[string]Agent{}},
		Errors: nil,
	}

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("read agents dir: %w", err))
		return result
	}

	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		if filepath.Ext(entry.Name()) != ".md" {
			continue
		}

		paths = append(paths, entry.Name())
	}

	sort.Strings(paths)

	for _, filePath := range paths {
		agent, err := loadAgent(fsys, filePath)
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

func loadAgent(fsys fs.FS, filePath string) (Agent, error) {
	data, err := fs.ReadFile(fsys, filePath)
	if err != nil {
		return Agent{}, fmt.Errorf("%s: read agent: %w", filePath, err)
	}

	info, err := fs.Stat(fsys, filePath)
	if err != nil {
		return Agent{}, fmt.Errorf("%s: stat agent: %w", filePath, err)
	}

	frontmatter, frontmatterNode, prompt, err := parseAgentFrontmatter(string(data))
	if err != nil {
		return Agent{}, fmt.Errorf("%s: %w", filePath, err)
	}

	permission, err := parsePermissionNode(frontmatterField(frontmatterNode, "permission"))
	if err != nil {
		return Agent{}, fmt.Errorf("%s: parse permission: %w", filePath, err)
	}

	name := strings.TrimSuffix(filePath, filepath.Ext(filePath))
	if name == "" {
		return Agent{}, fmt.Errorf("%s: empty agent name", filePath)
	}

	return Agent{
		Name:            name,
		Description:     frontmatterString(frontmatter, "description"),
		Model:           frontmatterString(frontmatter, "model"),
		ReasoningEffort: frontmatterString(frontmatter, "reasoningEffort"),
		Verbosity:       frontmatterString(frontmatter, "verbosity"),
		Prompt:          strings.TrimSpace(prompt),
		Location:        filePath,
		Permission:      permission,
		Frontmatter:     frontmatter,
		FileMode:        info.Mode(),
	}, nil
}

func parseAgentFrontmatter(content string) (frontmatter map[string]any, frontmatterNode *yaml.Node, prompt string, err error) {
	match := agentFrontmatterPattern.FindStringSubmatchIndex(content)
	if match == nil {
		return nil, nil, "", errors.New("missing YAML frontmatter")
	}

	frontmatterText := content[match[2]:match[3]]

	frontmatter, frontmatterNode, err = decodeAgentFrontmatter(frontmatterText)
	if err != nil {
		frontmatter, frontmatterNode, err = decodeAgentFrontmatter(sanitizeAgentFrontmatter(frontmatterText))
		if err != nil {
			return nil, nil, "", fmt.Errorf("parse YAML frontmatter: %w", err)
		}
	}

	return frontmatter, frontmatterNode, content[match[1]:], nil
}

func decodeAgentFrontmatter(frontmatterText string) (map[string]any, *yaml.Node, error) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(frontmatterText), &node); err != nil {
		return nil, nil, fmt.Errorf("unmarshal YAML frontmatter: %w", err)
	}

	if len(node.Content) == 0 || node.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errors.New("unmarshal YAML frontmatter: expected mapping")
	}

	var frontmatter map[string]any
	if err := yaml.Unmarshal([]byte(frontmatterText), &frontmatter); err != nil {
		return nil, nil, fmt.Errorf("unmarshal YAML frontmatter: %w", err)
	}

	if frontmatter == nil {
		return map[string]any{}, node.Content[0], nil
	}

	return frontmatter, node.Content[0], nil
}

func frontmatterField(frontmatter *yaml.Node, key string) *yaml.Node {
	if frontmatter == nil || frontmatter.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(frontmatter.Content); i += 2 {
		if frontmatter.Content[i].Value == key {
			return frontmatter.Content[i+1]
		}
	}

	return nil
}

func sanitizeAgentFrontmatter(frontmatterText string) string {
	lines := strings.Split(frontmatterText, "\n")
	result := make([]string, 0, len(lines))

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			result = append(result, line)
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			result = append(result, line)
			continue
		}

		key := strings.TrimSpace(parts[0])

		value := strings.TrimSpace(parts[1])
		if key == "" || value == "" || value == ">" || value == "|" || strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") || !strings.Contains(value, ":") {
			result = append(result, line)
			continue
		}

		result = append(result, key+": |-", "  "+value)
	}

	return strings.Join(result, "\n")
}

func frontmatterString(frontmatter map[string]any, key string) string {
	value, ok := frontmatter[key]
	if !ok {
		return ""
	}

	text, ok := value.(string)
	if !ok {
		return ""
	}

	return text
}
