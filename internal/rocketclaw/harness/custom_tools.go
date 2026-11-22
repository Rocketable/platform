package harness

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

type Tool struct {
	Name               string
	Description        string
	Parameters         map[string]any
	Permission         string
	VisibilitySubjects []string
	Subjects           func(json.RawMessage) ([]string, error)
	Call               func(context.Context, json.RawMessage, chan<- ChatResponse) (ToolResult, error)
	Resumable          bool
	TurnBound          bool
}

var customToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func customLooperTools(customTools []Tool, reserved map[string]looperTool) (map[string]looperTool, error) {
	tools := make(map[string]looperTool, len(customTools))

	for i := range customTools {
		tool, err := customLooperTool(&customTools[i])
		if err != nil {
			return nil, fmt.Errorf("custom tool %q: %w", customTools[i].Name, err)
		}

		name := customTools[i].Name
		if _, exists := reserved[name]; exists || slices.Contains([]string{"find_skills", "skill", "task", executeToolName, loadExecuteResultToolName}, name) {
			return nil, fmt.Errorf("custom tool %q collides with a built-in tool", name)
		}

		if _, exists := tools[name]; exists {
			return nil, fmt.Errorf("custom tool %q is duplicated", name)
		}

		tools[name] = tool
	}

	return tools, nil
}

func customLooperTool(tool *Tool) (looperTool, error) {
	if tool.Name == "" {
		return looperTool{}, errors.New("name is required")
	}

	if !customToolNamePattern.MatchString(tool.Name) {
		return looperTool{}, errors.New("name must contain only letters, numbers, underscores, and dashes")
	}

	if tool.Call == nil {
		return looperTool{}, errors.New("call is required")
	}

	parameters, err := customToolParameters(tool.Parameters)
	if err != nil {
		return looperTool{}, err
	}

	permission := cmp.Or(tool.Permission, "tools")

	visibilitySubjects := tool.VisibilitySubjects
	if len(visibilitySubjects) == 0 {
		visibilitySubjects = []string{tool.Name}
	}

	subjects := tool.Subjects
	if subjects == nil {
		subjects = func(json.RawMessage) ([]string, error) {
			return []string{tool.Name}, nil
		}
	}

	call := func(ctx context.Context, raw json.RawMessage, output chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
		if tool.TurnBound && backgroundDetached(ctx) {
			return TextToolResult(fmt.Sprintf(TurnBoundRefused, tool.Name)), nil
		}

		return tool.Call(ctx, raw, output)
	}

	var definition responses.FunctionToolParam

	definition.Name = tool.Name
	definition.Description = openai.String(tool.Description)
	definition.Parameters = parameters
	definition.Strict = openai.Bool(true)

	return looperTool{Definition: definition, Call: call, Permission: permission, Subjects: subjects, VisibilitySubjects: visibilitySubjects, codeModeOnly: true, resumable: tool.Resumable}, nil
}

func customToolParameters(parameters map[string]any) (map[string]any, error) {
	root := map[string]any{"type": "object"}
	maps.Copy(root, parameters)

	return normalizeOwnedToolSchema(root)
}

func normalizeOwnedToolSchema(result map[string]any) (map[string]any, error) {
	properties, ok := result["properties"]
	if ok && properties != nil {
		typedProperties, ok := properties.(map[string]any)
		if !ok {
			return nil, errors.New("parameters.properties must be an object")
		}

		normalizedProperties := maps.Clone(typedProperties)
		for name, property := range typedProperties {
			propertySchema, ok := property.(map[string]any)
			if !ok {
				continue
			}

			normalizedProperty, err := normalizeOwnedToolSchema(maps.Clone(propertySchema))
			if err != nil {
				return nil, err
			}

			normalizedProperties[name] = normalizedProperty
		}

		result["properties"] = normalizedProperties
	}

	items, ok := result["items"].(map[string]any)
	if ok {
		normalizedItems, err := normalizeOwnedToolSchema(maps.Clone(items))
		if err != nil {
			return nil, err
		}

		result["items"] = normalizedItems
	}

	if result["type"] == "object" {
		properties, ok := result["properties"]
		if !ok || properties == nil {
			properties = map[string]any{}
			result["properties"] = properties
		}

		typedProperties := properties.(map[string]any)

		if _, ok := result["additionalProperties"]; !ok {
			result["additionalProperties"] = false
		}

		required := slices.Sorted(maps.Keys(typedProperties))
		if required == nil {
			required = []string{}
		}

		result["required"] = required
	}

	return result, nil
}
