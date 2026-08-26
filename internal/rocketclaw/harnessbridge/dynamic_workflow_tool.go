package harnessbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/Rocketable/platform/internal/rocketcode"
)

const (
	dynamicWorkflowToolName          = "rocketclaw_dynamic_workflow"
	nestedWorkflowSilentCompleteText = "Workflow completed silently."
)

type dynamicWorkflowParams struct {
	Name string `json:"name"`
	Args string `json:"args"`
}

func parseDynamicWorkflowParams(raw json.RawMessage) (dynamicWorkflowParams, error) {
	var params dynamicWorkflowParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return dynamicWorkflowParams{}, fmt.Errorf("parse dynamic workflow params: %w", err)
	}

	params.Name = strings.TrimSpace(params.Name)
	if params.Name == "" {
		return dynamicWorkflowParams{}, errors.New("name is required")
	}

	return params, nil
}

func allowedWorkflowDescriptions(permissions rocketcode.PermissionSet, descriptions []workflow.Description) []workflow.Description {
	allowed := make([]workflow.Description, 0, len(descriptions))
	for _, description := range descriptions {
		action, _ := permissions.Evaluate("workflow", description.Name)
		if action != rocketcode.PermissionAllow {
			continue
		}

		allowed = append(allowed, description)
	}

	return allowed
}

func dynamicWorkflowToolDescription(allowed []workflow.Description) string {
	lines := make([]string, 0, 5+len(allowed))
	lines = append(lines,
		"Run a saved Starlark workflow as a nested tool call inside this turn and return its final result text.",
		"Progress is published into the parent turn thinking stream. This does not start a second managed conversation turn.",
		"Pass args as an empty string when the workflow needs no arguments.",
		"",
		"Available workflows (permission.workflow subjects):",
	)

	for _, description := range allowed {
		text := strings.TrimSpace(description.Description)
		if text == "" {
			text = "Saved Starlark workflow."
		}

		lines = append(lines, fmt.Sprintf("- %s: %s", description.Name, text))
	}

	return strings.Join(lines, "\n")
}

func dynamicWorkflowTool(permissions rocketcode.PermissionSet, descriptions []workflow.Description, run func(context.Context, string, string, chan<- rocketcode.ChatResponse) (string, error)) (rocketcode.Tool, bool) {
	allowed := allowedWorkflowDescriptions(permissions, descriptions)
	if len(allowed) == 0 {
		return rocketcode.Tool{}, false
	}

	visibility := make([]string, len(allowed))
	for i := range allowed {
		visibility[i] = allowed[i].Name
	}

	return rocketcode.Tool{
		Name:               dynamicWorkflowToolName,
		Description:        dynamicWorkflowToolDescription(allowed),
		Permission:         "workflow",
		VisibilitySubjects: visibility,
		Subjects: func(raw json.RawMessage) ([]string, error) {
			params, err := parseDynamicWorkflowParams(raw)
			if err != nil {
				return nil, err
			}

			return []string{params.Name}, nil
		},
		Parameters: map[string]any{
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Workflow name (filename stem under workflows/)."},
				"args": map[string]any{"type": "string", "description": "Argument string passed to main(args). Use an empty string when unused."},
			},
			"required": []string{"name", "args"},
		},
		Call: func(ctx context.Context, raw json.RawMessage, output chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			params, err := parseDynamicWorkflowParams(raw)
			if err != nil {
				return rocketcode.ToolResult{}, err
			}

			result, err := run(ctx, params.Name, params.Args, output)
			if err != nil {
				return rocketcode.ToolResult{}, err
			}

			return rocketcode.TextToolResult(result), nil
		},
	}, true
}

func emitNestedWorkflowProgress(output chan<- rocketcode.ChatResponse, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	select {
	case output <- rocketcode.ChatResponse{Kind: rocketcode.ChatResponseAssistantCommentary, Text: text}:
	default:
	}
}

func (b *Bridge) maybeDynamicWorkflowTool(root *os.Root, agent *rocketcode.Agent, agentName, turnID string) (rocketcode.Tool, bool) {
	allowed := false

	for _, bucket := range agent.Permission.Buckets {
		if bucket.Name != "workflow" {
			continue
		}

		for _, rule := range bucket.Rules {
			if rule.Action == rocketcode.PermissionAllow {
				allowed = true
			}
		}
	}

	if !allowed {
		return rocketcode.Tool{}, false
	}

	definitions, err := workflow.Load(root, b.runtime.RuntimeDirName())
	if err != nil {
		b.log.Warn("skip rocketclaw_dynamic_workflow tool: load workflows", "error", err)
		return rocketcode.Tool{}, false
	}

	return dynamicWorkflowTool(agent.Permission, workflow.Descriptions(definitions), func(ctx context.Context, name, args string, output chan<- rocketcode.ChatResponse) (string, error) {
		return b.runNestedWorkflow(ctx, agentName, turnID, name, definitions[name], args, output)
	})
}

func (b *Bridge) runNestedWorkflow(ctx context.Context, agentName, turnID, name string, definition *workflow.Definition, args string, output chan<- rocketcode.ChatResponse) (resultText string, err error) {
	if definition == nil {
		return "", fmt.Errorf("workflow %q is not configured", name)
	}

	agentRun, closeRunner, err := newWorkflowAgentRunner(b.runtime, agentName, b.log)
	if err != nil {
		return "", fmt.Errorf("prepare nested workflow agent runner: %w", err)
	}
	defer func() { err = errors.Join(err, closeRunner()) }()

	runID := strings.TrimSpace(turnID)
	if runID == "" {
		runID = "nested-" + name
	}

	progress := func(_ context.Context, update workflow.PhaseUpdate) error {
		emitNestedWorkflowProgress(output, fmt.Sprintf("workflow %s phase %s: %s", definition.Name, update.Name, update.Status))
		return nil
	}
	agentProgress := func(_ context.Context, update workflow.AgentUpdate) error {
		label := strings.TrimSpace(update.Label)
		if label == "" {
			label = "agent"
		}

		emitNestedWorkflowProgress(output, fmt.Sprintf("workflow %s %s: %s", definition.Name, label, strings.TrimSpace(update.Activity)))

		return nil
	}

	result, errRun := workflow.Run(ctx, definition, workflow.RunRequest{
		RunID: runID, Args: args, Definition: definition,
	}, agentRun, progress, agentProgress)
	if errRun != nil {
		return "", fmt.Errorf("run nested workflow %q: %w", name, errRun)
	}

	if result.Silent {
		return nestedWorkflowSilentCompleteText, nil
	}

	return result.Text, nil
}
