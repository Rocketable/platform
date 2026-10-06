package backend

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"

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

func allowedWorkflowDescriptions(permissions rocketcode.PermissionSet, descriptions []protocol.WorkflowDescription) []protocol.WorkflowDescription {
	allowed := make([]protocol.WorkflowDescription, 0, len(descriptions))
	for _, description := range descriptions {
		action, _ := permissions.Evaluate("workflow", description.Name)
		if action != rocketcode.PermissionAllow {
			continue
		}

		allowed = append(allowed, description)
	}

	return allowed
}

func dynamicWorkflowToolDescription(allowed []protocol.WorkflowDescription) string {
	lines := make([]string, 0, 5+len(allowed))
	lines = append(lines,
		"Run a saved Starlark workflow as a nested tool call inside this turn and return its final result text.",
		"This does not start a second managed conversation turn.",
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

func (b *Bridge) dynamicWorkflowTool(permissions rocketcode.PermissionSet, agentName string, definitions map[string]*workflow.Definition) (rocketcode.Tool, bool) {
	allowed := allowedWorkflowDescriptions(permissions, workflow.Descriptions(definitions))
	if len(allowed) == 0 {
		return rocketcode.Tool{}, false
	}

	visibility := make([]string, len(allowed))
	for i := range allowed {
		visibility[i] = allowed[i].Name
	}

	return rocketcode.Tool{
		Name:               dynamicWorkflowToolName,
		Resumable:          true,
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
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			params, err := parseDynamicWorkflowParams(raw)
			if err != nil {
				return rocketcode.ToolResult{}, err
			}

			result, err := b.runNestedWorkflow(ctx, agentName, params.Name, definitions[params.Name], params.Args)
			if err != nil {
				return rocketcode.ToolResult{}, err
			}

			return rocketcode.TextToolResult(result), nil
		},
	}, true
}

func (b *Bridge) maybeDynamicWorkflowTool(root *os.Root, agent *rocketcode.Agent, agentName string) (rocketcode.Tool, bool) {
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

	return b.dynamicWorkflowTool(agent.Permission, agentName, definitions)
}

func (b *Bridge) runNestedWorkflow(ctx context.Context, agentName, name string, definition *workflow.Definition, args string) (resultText string, err error) {
	if definition == nil {
		return "", fmt.Errorf("workflow %q is not configured", name)
	}

	b.mu.Lock()
	tagConversationID := cmp.Or(b.activeReply.SyncDestination, b.config.ConversationID)
	b.mu.Unlock()

	agentRun, err := newWorkflowAgentRunner(b.runtime, agentName, rocketcode.TracelessJournal{Parent: conversationJournal{store: b.config.SessionService, conversationID: b.config.ConversationID, log: b.log}}, b.log, sessionTagTools(b.config.SessionService, tagConversationID)...)
	if err != nil {
		return "", fmt.Errorf("prepare nested workflow agent runner: %w", err)
	}
	defer func() { err = errors.Join(err, agentRun.Close()) }()

	result, errRun := workflow.Run(ctx, definition, workflow.RunRequest{
		RunID: rocketcode.ToolCallKey(ctx), Args: args, Definition: definition,
	}, agentRun)
	if errRun != nil {
		return "", fmt.Errorf("run nested workflow %q: %w", name, errRun)
	}

	if result.Silent {
		return nestedWorkflowSilentCompleteText, nil
	}

	return result.Text, nil
}
