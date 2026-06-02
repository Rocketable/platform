//nolint:exhaustruct // OpenAI SDK structs are intentionally sparse at call sites.
package rocketcode

import (
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

// Tool is a custom function tool supplied by an embedding application.
type Tool struct {
	Name               string
	Description        string
	Parameters         map[string]any
	Permission         string
	VisibilitySubjects []string
	Subjects           func(json.RawMessage) ([]string, error)
	Call               func(context.Context, json.RawMessage, chan<- ChatResponse) (ToolResult, error)
}

var customToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func customLooperTools(customTools []Tool, reserved map[string]looperTool) (map[string]looperTool, error) {
	tools := make(map[string]looperTool, len(customTools))

	reservedNames := map[string]struct{}{
		"find_skills": {},
		"skill":       {},
		"task":        {},
	}
	for name := range reserved {
		reservedNames[name] = struct{}{}
	}

	for i := range customTools {
		tool, err := customLooperTool(&customTools[i])
		if err != nil {
			return nil, fmt.Errorf("custom tool %q: %w", customTools[i].Name, err)
		}

		name := customTools[i].Name
		if _, exists := reservedNames[name]; exists {
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

	permission := tool.Permission
	if permission == "" {
		permission = "tools"
	}

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

	return looperTool{
		Definition: responses.FunctionToolParam{
			Name:        tool.Name,
			Description: openai.String(tool.Description),
			Parameters:  parameters,
			Strict:      openai.Bool(true),
		},
		Call: func(ctx context.Context, raw json.RawMessage, output chan<- ChatResponse) (ToolResult, error) {
			return tool.Call(ctx, raw, output)
		},
		Permission:         permission,
		Subjects:           subjects,
		VisibilitySubjects: visibilitySubjects,
	}, nil
}

func customToolParameters(parameters map[string]any) (map[string]any, error) {
	if parameters == nil {
		return map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"required":             []string{},
			"additionalProperties": false,
		}, nil
	}

	result := maps.Clone(parameters)
	if _, ok := result["type"]; !ok {
		result["type"] = "object"
	}

	properties, ok := result["properties"]
	if !ok || properties == nil {
		properties = map[string]any{}
		result["properties"] = properties
	}

	typedProperties, ok := properties.(map[string]any)
	if !ok {
		return nil, errors.New("parameters.properties must be an object")
	}

	if _, ok := result["additionalProperties"]; !ok {
		result["additionalProperties"] = false
	}

	if _, ok := result["required"]; !ok {
		required := slices.Sorted(maps.Keys(typedProperties))
		if required == nil {
			required = []string{}
		}

		result["required"] = required
	}

	return result, nil
}
