package backend

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
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

// dynamicWorkflowTool runs a workflow's steps with shellEnv, the environment of the turn that calls it,
// originHeader, that turn's trusted origin header, and shell, that turn's agent's shell.
func (b *Bridge) dynamicWorkflowTool(permissions harness.PermissionSet, agentName, tagConversationID string, definitions map[string]*workflow.Definition, shellEnv map[string]string, originHeader string, shell *harness.CustomShell) (harness.Tool, bool) {
	allowed := slices.DeleteFunc(workflow.Descriptions(definitions), func(description protocol.WorkflowDescription) bool {
		return !permissions.Enabled("workflow", description.Name)
	})
	if len(allowed) == 0 {
		return harness.Tool{}, false
	}

	visibility := make([]string, len(allowed))
	lines := make([]string, 0, 5+len(allowed))
	lines = append(lines,
		"Run a saved Starlark workflow as a nested tool call inside this turn and return its final result text.",
		"This does not start a second managed conversation turn.",
		"Pass args as an empty string when the workflow needs no arguments.",
		"",
		"Available workflows (permission.workflow subjects):",
	)

	for i, description := range allowed {
		visibility[i] = description.Name
		text := cmp.Or(strings.TrimSpace(description.Description), "Saved Starlark workflow.")

		lines = append(lines, fmt.Sprintf("- %s: %s", description.Name, text))
	}

	return harness.Tool{
		Name:               dynamicWorkflowToolName,
		Resumable:          true,
		Description:        strings.Join(lines, "\n"),
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
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
			params, err := parseDynamicWorkflowParams(raw)
			if err != nil {
				return harness.ToolResult{}, err
			}

			result, err := b.runNestedWorkflow(ctx, agentName, tagConversationID, params.Name, definitions[params.Name], params.Args, shellEnv, originHeader, shell)
			if err != nil {
				return harness.ToolResult{}, err
			}

			return harness.TextToolResult(result), nil
		},
	}, true
}

func (b *Bridge) maybeDynamicWorkflowTool(root *os.Root, agent *harness.Agent, agentName, tagConversationID string, shellEnv map[string]string, originHeader string) (harness.Tool, bool) {
	runtimeCfg := b.runtime.Clone()

	if !agent.Permission.Sudo && !agent.Permission.Easy && !slices.ContainsFunc(agent.Permission.Buckets, func(bucket harness.PermissionBucket) bool {
		return bucket.Name == "workflow" && slices.ContainsFunc(bucket.Rules, func(rule harness.PermissionRule) bool {
			return rule.Action == harness.PermissionAllow
		})
	}) {
		return harness.Tool{}, false
	}

	definitions, err := workflow.Load(root, runtimeCfg.RuntimeDirName())
	if err != nil {
		b.log.Warn("skip rocketclaw_dynamic_workflow tool: load workflows", "error", err)
		return harness.Tool{}, false
	}

	return b.dynamicWorkflowTool(agent.Permission, agentName, tagConversationID, definitions, shellEnv, originHeader, &agent.CustomShell)
}

func (b *Bridge) runNestedWorkflow(ctx context.Context, agentName, tagConversationID, name string, definition *workflow.Definition, args string, shellEnv map[string]string, originHeader string, shell *harness.CustomShell) (resultText string, err error) {
	if definition == nil {
		return "", fmt.Errorf("workflow %q is not configured", name)
	}

	agentRun, err := newWorkflowAgentRunner(b.runtime, b.config.SessionService.shells.current(), agentName, harness.TracelessJournal{Parent: conversationJournal{store: b.config.SessionService, conversationID: b.config.ConversationID, log: b.log}}, b.log, sessionTagTools(b.config.SessionService, tagConversationID)...)
	if err != nil {
		return "", fmt.Errorf("prepare nested workflow agent runner: %w", err)
	}
	defer func() { err = errors.Join(err, agentRun.Close()) }()

	agentRun.shellEnv, agentRun.originHeader = shellEnv, originHeader
	if agent := agentRun.agents.Items[agentName]; agent.Mode == harness.AgentModeEasy {
		agent.CustomShell = *shell
		agentRun.agents.Items[agentName] = agent
	}

	result, errRun := workflow.Run(ctx, definition, workflow.RunRequest{
		RunID: harness.ToolCallKey(ctx), Args: args,
	}, agentRun)
	if errRun != nil {
		return "", fmt.Errorf("run nested workflow %q: %w", name, errRun)
	}

	if result.Silent {
		return nestedWorkflowSilentCompleteText, nil
	}

	return result.Text, nil
}
