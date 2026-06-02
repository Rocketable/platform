//nolint:exhaustruct // Tests intentionally focus only on fields relevant to each case.
package rocketcode

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/responses"
)

func TestCustomToolDefaultsAndParameters(t *testing.T) {
	tools, err := customLooperTools([]Tool{{
		Name: "github_create_issue",
		Parameters: map[string]any{
			"properties": map[string]any{
				"body":  map[string]any{"type": "string"},
				"title": map[string]any{"type": "string"},
			},
		},
		Call: func(context.Context, json.RawMessage, chan<- ChatResponse) (ToolResult, error) {
			return TextToolResult("created"), nil
		},
	}}, nil)
	if err != nil {
		t.Fatalf("customLooperTools returned error: %v", err)
	}

	tool := tools["github_create_issue"]
	if got, want := tool.Permission, "tools"; got != want {
		t.Fatalf("tool.Permission = %q; want %q", got, want)
	}

	if got, want := tool.VisibilitySubjects, []string{"github_create_issue"}; !slices.Equal(got, want) {
		t.Fatalf("tool.VisibilitySubjects = %#v; want %#v", got, want)
	}

	subjects, err := tool.Subjects(nil)
	if err != nil {
		t.Fatalf("tool.Subjects returned error: %v", err)
	}

	if want := []string{"github_create_issue"}; !slices.Equal(subjects, want) {
		t.Fatalf("tool.Subjects = %#v; want %#v", subjects, want)
	}

	parameters := tool.Definition.Parameters
	if got := parameters["type"]; got != "object" {
		t.Fatalf("parameters.type = %#v; want object", got)
	}

	if got := parameters["additionalProperties"]; got != false {
		t.Fatalf("parameters.additionalProperties = %#v; want false", got)
	}

	if got, ok := parameters["required"].([]string); !ok || !slices.Equal(got, []string{"body", "title"}) {
		t.Fatalf("parameters.required = %#v; want [body title]", parameters["required"])
	}
}

func TestCustomToolParametersRequiredJSON(t *testing.T) {
	tests := []struct {
		name       string
		parameters map[string]any
		wantJSON   string
	}{
		{
			name:       "nil parameters",
			parameters: nil,
			wantJSON:   `"required":[]`,
		},
		{
			name: "empty properties without required",
			parameters: map[string]any{
				"properties": map[string]any{},
			},
			wantJSON: `"required":[]`,
		},
		{
			name: "explicit required preserved",
			parameters: map[string]any{
				"properties": map[string]any{},
				"required":   []string{},
			},
			wantJSON: `"required":[]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parameters, err := customToolParameters(tt.parameters)
			if err != nil {
				t.Fatalf("customToolParameters returned error: %v", err)
			}

			data, err := json.Marshal(parameters)
			if err != nil {
				t.Fatalf("json.Marshal returned error: %v", err)
			}

			got := string(data)
			if !strings.Contains(got, tt.wantJSON) {
				t.Fatalf("customToolParameters JSON = %s; want substring %s", got, tt.wantJSON)
			}

			if strings.Contains(got, `"required":null`) {
				t.Fatalf("customToolParameters JSON = %s; want required array, not null", got)
			}
		})
	}
}

func TestCustomToolParametersRequiredDefaultsToSortedPropertyNames(t *testing.T) {
	parameters, err := customToolParameters(map[string]any{
		"properties": map[string]any{
			"b": map[string]any{"type": "string"},
			"a": map[string]any{"type": "string"},
		},
	})
	if err != nil {
		t.Fatalf("customToolParameters returned error: %v", err)
	}

	if got, ok := parameters["required"].([]string); !ok || !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("parameters.required = %#v; want [a b]", parameters["required"])
	}
}

func TestCustomToolPermissionVisibilitySupportsWildcards(t *testing.T) {
	tools := customPermissionTestTools(t)
	factory := &toolFactory{baseTools: tools}
	agent := &Agent{Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "tools", Rules: []PermissionRule{
		{Pattern: "*", Action: permissionDeny},
		{Pattern: "github_*", Action: permissionAllow},
		{Pattern: "github_delete_repo", Action: permissionDeny},
	}}}}}

	visible := factory.toolsFor(agent)

	if _, ok := visible["github_create_issue"]; !ok {
		t.Fatalf("github_create_issue is hidden; want visible")
	}

	if _, ok := visible["github_delete_repo"]; ok {
		t.Fatalf("github_delete_repo is visible; want hidden")
	}

	if _, ok := visible["linear_create_issue"]; ok {
		t.Fatalf("linear_create_issue is visible; want hidden")
	}
}

func TestCustomToolPermissionVisibilityScalarAllowDeny(t *testing.T) {
	tools := customPermissionTestTools(t)
	factory := &toolFactory{baseTools: tools}

	defaultAgent := &Agent{}
	if _, ok := factory.toolsFor(defaultAgent)["github_create_issue"]; ok {
		t.Fatalf("github_create_issue is visible without an allow rule; want hidden")
	}

	deniedAgent := &Agent{Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "tools", Rules: []PermissionRule{{Pattern: "*", Action: permissionDeny}}}}}}
	if _, ok := factory.toolsFor(deniedAgent)["github_create_issue"]; ok {
		t.Fatalf("github_create_issue is visible with tools deny; want hidden")
	}

	allowedAgent := &Agent{Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "tools", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}}
	if _, ok := factory.toolsFor(allowedAgent)["github_create_issue"]; !ok {
		t.Fatalf("github_create_issue is hidden with tools allow; want visible")
	}
}

func TestCustomToolPermissionDeniedAtCallTime(t *testing.T) {
	called := false

	tools, err := customLooperTools([]Tool{{
		Name: "github_create_issue",
		Call: func(context.Context, json.RawMessage, chan<- ChatResponse) (ToolResult, error) {
			called = true
			return TextToolResult("called"), nil
		},
	}}, nil)
	if err != nil {
		t.Fatalf("customLooperTools returned error: %v", err)
	}

	looper := &looper{
		Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: "tools", Rules: []PermissionRule{{Pattern: "github_*", Action: permissionDeny}}}}},
		Tools:       tools,
	}

	outputs, hadToolCalls, err := looper.dispatchToolCalls(context.Background(), responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{{
		ID:        "item_1",
		CallID:    "call_1",
		Name:      "github_create_issue",
		Arguments: `{}`,
	}}), nil, nil)
	if err != nil {
		t.Fatalf("dispatchToolCalls returned error: %v", err)
	}

	if !hadToolCalls {
		t.Fatalf("dispatchToolCalls hadToolCalls = false; want true")
	}

	if called {
		t.Fatalf("custom tool handler was called; want denied before handler")
	}

	if got := outputs[0].Result.Output; got == "called" || got == "" {
		t.Fatalf("denied custom tool output = %q; want permission denial", got)
	}
}

func TestCustomToolPermissionDefaultsDenyAtCallTime(t *testing.T) {
	called := false

	tools, err := customLooperTools([]Tool{{
		Name: "github_create_issue",
		Call: func(context.Context, json.RawMessage, chan<- ChatResponse) (ToolResult, error) {
			called = true
			return TextToolResult("called"), nil
		},
	}}, nil)
	if err != nil {
		t.Fatalf("customLooperTools returned error: %v", err)
	}

	looper := &looper{Tools: tools}

	outputs, hadToolCalls, err := looper.dispatchToolCalls(context.Background(), responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{{
		ID:        "item_1",
		CallID:    "call_1",
		Name:      "github_create_issue",
		Arguments: `{}`,
	}}), nil, nil)
	if err != nil {
		t.Fatalf("dispatchToolCalls returned error: %v", err)
	}

	if !hadToolCalls {
		t.Fatalf("dispatchToolCalls hadToolCalls = false; want true")
	}

	if called {
		t.Fatalf("custom tool handler was called; want denied before handler")
	}

	if got := outputs[0].Result.Output; got == "called" || got == "" {
		t.Fatalf("default denied custom tool output = %q; want permission denial", got)
	}
}

func TestCustomToolUsesCustomSubjects(t *testing.T) {
	tools, err := customLooperTools([]Tool{{
		Name: "github_create_issue",
		Subjects: func(json.RawMessage) ([]string, error) {
			return []string{"github_private_repo"}, nil
		},
		Call: func(context.Context, json.RawMessage, chan<- ChatResponse) (ToolResult, error) {
			return TextToolResult("called"), nil
		},
	}}, nil)
	if err != nil {
		t.Fatalf("customLooperTools returned error: %v", err)
	}

	looper := &looper{
		Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: "tools", Rules: []PermissionRule{
			{Pattern: "github_create_issue", Action: permissionAllow},
			{Pattern: "github_private_repo", Action: permissionDeny},
		}}}},
		Tools: tools,
	}

	outputs, _, err := looper.dispatchToolCalls(context.Background(), responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{{
		ID:        "item_1",
		CallID:    "call_1",
		Name:      "github_create_issue",
		Arguments: `{}`,
	}}), nil, nil)
	if err != nil {
		t.Fatalf("dispatchToolCalls returned error: %v", err)
	}

	if got := outputs[0].Result.Output; got == "called" || got == "" {
		t.Fatalf("custom subject denial output = %q; want permission denial", got)
	}
}

func TestCustomToolValidation(t *testing.T) {
	validCall := func(context.Context, json.RawMessage, chan<- ChatResponse) (ToolResult, error) {
		return TextToolResult("ok"), nil
	}
	tests := []struct {
		name     string
		tools    []Tool
		reserved map[string]looperTool
		want     string
	}{
		{name: "empty name", tools: []Tool{{Name: "", Call: validCall}}, want: "name is required"},
		{name: "invalid name", tools: []Tool{{Name: "bad name", Call: validCall}}, want: "name must contain only"},
		{name: "nil call", tools: []Tool{{Name: "github_create_issue"}}, want: "call is required"},
		{name: "duplicate", tools: []Tool{{Name: "github_create_issue", Call: validCall}, {Name: "github_create_issue", Call: validCall}}, want: "duplicated"},
		{name: "built-in collision", tools: []Tool{{Name: "read", Call: validCall}}, reserved: map[string]looperTool{"read": {}}, want: "collides"},
		{name: "dynamic collision", tools: []Tool{{Name: "task", Call: validCall}}, want: "collides"},
		{name: "invalid properties", tools: []Tool{{Name: "github_create_issue", Parameters: map[string]any{"properties": "bad"}, Call: validCall}}, want: "parameters.properties"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := customLooperTools(tt.tools, tt.reserved)
			if err == nil {
				t.Fatalf("customLooperTools returned nil error; want %q", tt.want)
			}

			if got := err.Error(); !strings.Contains(got, tt.want) {
				t.Fatalf("customLooperTools error = %q; want substring %q", got, tt.want)
			}
		})
	}
}

func customPermissionTestTools(t *testing.T) map[string]looperTool {
	t.Helper()

	tools, err := customLooperTools([]Tool{
		{Name: "github_create_issue", Call: func(context.Context, json.RawMessage, chan<- ChatResponse) (ToolResult, error) {
			return TextToolResult("ok"), nil
		}},
		{Name: "github_delete_repo", Call: func(context.Context, json.RawMessage, chan<- ChatResponse) (ToolResult, error) {
			return TextToolResult("ok"), nil
		}},
		{Name: "linear_create_issue", Call: func(context.Context, json.RawMessage, chan<- ChatResponse) (ToolResult, error) {
			return TextToolResult("ok"), nil
		}},
	}, nil)
	if err != nil {
		t.Fatalf("customLooperTools returned error: %v", err)
	}

	return tools
}
