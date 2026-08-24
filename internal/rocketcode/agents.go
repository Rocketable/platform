package rocketcode

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
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
	ModelRouter     string
	ModelOptions    []ModelOption
	MaxRecursion    *int
	Guardrail       string
	Prompt          string
	Location        string
	Permission      PermissionSet
	OutputSchema    map[string]any
	Frontmatter     map[string]any
	FileMode        fs.FileMode
}

// ModelOption is one allowed model / reasoning effort / verbosity triple.
type ModelOption struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort"`
	Verbosity       string `json:"verbosity"`
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

// LoadAgents scans the top level of fsys for markdown agent files.
func LoadAgents(fsys fs.FS, resolveModel func(string) (string, error)) AgentLoadResult {
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
		agent, err := loadAgent(fsys, filePath, resolveModel)
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

func loadAgent(fsys fs.FS, filePath string, resolveModel func(string) (string, error)) (Agent, error) {
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

	var maxRecursion *int

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
			maxRecursion = &value
		}
	}

	name := strings.TrimSuffix(filePath, filepath.Ext(filePath))
	if name == "" {
		return Agent{}, fmt.Errorf("%s: empty agent name", filePath)
	}

	modelRouter := strings.TrimSpace(frontmatterString(frontmatter, "modelRouter"))
	modelField := frontmatterField(frontmatterNode, "model")
	reasoningEffort := frontmatterString(frontmatter, "reasoningEffort")
	verbosity := frontmatterString(frontmatter, "verbosity")

	var (
		model        string
		modelOptions []ModelOption
	)

	if modelRouter != "" {
		if modelField != nil {
			return Agent{}, fmt.Errorf("%s: model: must be omitted when modelRouter is set", filePath)
		}

		if strings.TrimSpace(reasoningEffort) != "" || strings.TrimSpace(verbosity) != "" {
			return Agent{}, fmt.Errorf("%s: reasoningEffort and verbosity: must be omitted when modelRouter is set", filePath)
		}

		var err error

		modelOptions, err = parseModelOptions(frontmatterField(frontmatterNode, "modelOptions"), resolveModel)
		if err != nil {
			return Agent{}, fmt.Errorf("%s: %w", filePath, err)
		}
	} else {
		if modelField == nil || modelField.Kind != yaml.ScalarNode || modelField.ShortTag() != "!!str" || strings.TrimSpace(modelField.Value) == "" {
			return Agent{}, fmt.Errorf("%s: model: required non-empty string", filePath)
		}

		var err error

		model, err = resolveModel(modelField.Value)
		if err != nil {
			return Agent{}, fmt.Errorf("%s: model: %w", filePath, err)
		}

		if strings.TrimSpace(model) == "" {
			return Agent{}, fmt.Errorf("%s: model: required non-empty string", filePath)
		}
	}

	outputSchema, err := parseAgentOutputSchema(frontmatterField(frontmatterNode, "schema"))
	if err != nil {
		return Agent{}, fmt.Errorf("%s: %w", filePath, err)
	}

	return Agent{
		Name:            name,
		Description:     frontmatterString(frontmatter, "description"),
		Model:           model,
		ReasoningEffort: reasoningEffort,
		Verbosity:       verbosity,
		ModelRouter:     modelRouter,
		ModelOptions:    modelOptions,
		MaxRecursion:    maxRecursion,
		Guardrail:       frontmatterString(frontmatter, "guardrail"),
		Prompt:          strings.TrimSpace(prompt),
		Location:        filePath,
		Permission:      permission,
		OutputSchema:    outputSchema,
		Frontmatter:     frontmatter,
		FileMode:        info.Mode(),
	}, nil
}

func passThroughAgentModel(model string) (string, error) {
	return model, nil
}

func parseModelOptions(node *yaml.Node, resolveModel func(string) (string, error)) ([]ModelOption, error) {
	if node == nil || node.Kind != yaml.SequenceNode || len(node.Content) == 0 {
		return nil, errors.New("modelOptions: required non-empty list")
	}

	options := make([]ModelOption, 0, len(node.Content))
	for i, item := range node.Content {
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("modelOptions[%d]: must be a mapping", i)
		}

		modelField := frontmatterField(item, "model")
		if modelField == nil || modelField.Kind != yaml.ScalarNode || modelField.ShortTag() != "!!str" || strings.TrimSpace(modelField.Value) == "" {
			return nil, fmt.Errorf("modelOptions[%d]: model: required non-empty string", i)
		}

		model, err := resolveModel(modelField.Value)
		if err != nil {
			return nil, fmt.Errorf("modelOptions[%d]: model: %w", i, err)
		}

		if strings.TrimSpace(model) == "" {
			return nil, fmt.Errorf("modelOptions[%d]: model: required non-empty string", i)
		}

		options = append(options, ModelOption{
			Model:           model,
			ReasoningEffort: yamlMappingString(item, "reasoningEffort"),
			Verbosity:       yamlMappingString(item, "verbosity"),
		})
	}

	return options, nil
}

func yamlMappingString(node *yaml.Node, key string) string {
	field := frontmatterField(node, key)
	if field == nil || field.Kind != yaml.ScalarNode {
		return ""
	}

	return field.Value
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
