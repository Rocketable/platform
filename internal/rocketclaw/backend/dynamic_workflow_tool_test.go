package backend

import (
	"context"
	"encoding/json"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeWorkflowFixture(t *testing.T, workspace, name, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "workflows"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".rocketclaw", "workflows", name+".star"), []byte(body), 0o600))
}

func writeMainAgentSkills(t *testing.T, workspace, agentYAML string) {
	t.Helper()
	writeAgent(t, workspace, "main", agentYAML)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
}

func openWorkspaceRoot(t *testing.T, workspace string) *os.Root {
	t.Helper()

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	return root
}

func TestAllowedWorkflowDescriptionsFiltersWorkflowAllow(t *testing.T) {
	t.Parallel()

	var permissions rocketcode.PermissionSet
	require.NoError(t, permissions.Allow("workflow", "audit-routes"))
	require.NoError(t, permissions.Allow("workflow", "secret-*"))
	require.NoError(t, permissions.Deny("workflow", "secret-flow"))

	got := allowedWorkflowDescriptions(permissions, []protocol.WorkflowDescription{
		{Name: "audit-routes", Description: "Audit"},
		{Name: "secret-flow", Description: "Secret"},
		{Name: "other", Description: "Other"},
	})
	require.Equal(t, []protocol.WorkflowDescription{{Name: "audit-routes", Description: "Audit"}}, got)
}

func TestDynamicWorkflowToolSchemaAndCall(t *testing.T) {
	t.Parallel()

	var permissions rocketcode.PermissionSet
	require.NoError(t, permissions.Allow("workflow", "audit"))

	var gotName, gotArgs string

	output := make(chan rocketcode.ChatResponse, 4)
	tool, ok := dynamicWorkflowTool(permissions, []protocol.WorkflowDescription{
		{Name: "audit", Description: "Audit routes"},
		{Name: "secret", Description: "Secret"},
	}, func(_ context.Context, name, args string, out chan<- rocketcode.ChatResponse) (string, error) {
		gotName, gotArgs = name, args

		emitNestedWorkflowProgress(out, "workflow audit phase work: in-progress")

		return "result-text", nil
	})
	require.True(t, ok)
	assert.Equal(t, "workflow", tool.Permission)
	assert.Equal(t, []string{"audit"}, tool.VisibilitySubjects)
	assert.Contains(t, tool.Description, "audit")
	assert.NotContains(t, tool.Description, "secret")

	subjects, err := tool.Subjects(json.RawMessage(`{"name":"audit","args":""}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"audit"}, subjects)

	_, err = tool.Subjects(json.RawMessage(`{"name":"","args":""}`))
	require.ErrorContains(t, err, "name is required")

	result, err := tool.Call(t.Context(), json.RawMessage(`{"name":"audit","args":"path/to"}`), output)
	require.NoError(t, err)
	assert.Equal(t, "audit", gotName)
	assert.Equal(t, "path/to", gotArgs)
	assert.Equal(t, rocketcode.TextToolResult("result-text"), result)
	require.Len(t, drainNestedWorkflowProgress(output), 1)

	_, ok = dynamicWorkflowTool(rocketcode.PermissionSet{}, []protocol.WorkflowDescription{{Name: "audit", Description: "A"}}, nil)
	assert.False(t, ok)
}

func TestEmitNestedWorkflowProgressNeverBlocks(t *testing.T) {
	t.Parallel()
	emitNestedWorkflowProgress(make(chan rocketcode.ChatResponse), "phase work: complete")
	emitNestedWorkflowProgress(make(chan rocketcode.ChatResponse), "   ")
}

func TestRunNestedWorkflowAndMaybeTool(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeMainAgentSkills(t, workspace, "---\ndescription: Main\nmodel: gpt-5.5\n---\nPrompt\n")
	writeWorkflowFixture(t, workspace, "echo", `meta = {"name": "echo", "description": "Echo", "phases": ["work"]}
def main(args):
    return phase("work", lambda: args)
`)
	writeWorkflowFixture(t, workspace, "quiet", `meta = {"name": "quiet", "description": "Quiet"}
def main(args):
    return None
`)
	writeWorkflowFixture(t, workspace, "audit-routes", `meta = {"name": "audit-routes", "description": "Audit"}
def main(args):
    return args
`)

	root := openWorkspaceRoot(t, workspace)
	bridge := &Bridge{runtime: &config.Config{Workspace: workspace}, log: slog.New(slog.DiscardHandler)}

	// Nested run with turn-start definitions (same freeze as production Call).
	definitions, err := workflow.Load(root, ".rocketclaw")
	require.NoError(t, err)

	_, err = bridge.runNestedWorkflow(t.Context(), "main", "turn-1", "missing", nil, "", make(chan rocketcode.ChatResponse, 1))
	require.ErrorContains(t, err, `workflow "missing" is not configured`)

	output := make(chan rocketcode.ChatResponse, 32)
	text, err := bridge.runNestedWorkflow(t.Context(), "main", "turn-echo", "echo", definitions["echo"], "hello-nested", output)
	require.NoError(t, err)
	assert.Equal(t, "hello-nested", text)
	require.NotEmpty(t, drainNestedWorkflowProgress(output))

	// Full channel must not fail the run.
	text, err = bridge.runNestedWorkflow(t.Context(), "main", "turn-blocked", "echo", definitions["echo"], "still-ok", make(chan rocketcode.ChatResponse))
	require.NoError(t, err)
	assert.Equal(t, "still-ok", text)

	text, err = bridge.runNestedWorkflow(t.Context(), "main", "turn-quiet", "quiet", definitions["quiet"], "", make(chan rocketcode.ChatResponse, 8))
	require.NoError(t, err)
	assert.Equal(t, nestedWorkflowSilentCompleteText, text)

	// maybeDynamicWorkflowTool: workflow allow → tool; task-only → omit; Call works.
	var workflowAllow rocketcode.PermissionSet
	require.NoError(t, workflowAllow.Allow("workflow", "audit-routes"))
	tool, ok := bridge.maybeDynamicWorkflowTool(root, &rocketcode.Agent{Permission: workflowAllow}, "main", "turn-1")
	require.True(t, ok)
	assert.Equal(t, []string{"audit-routes"}, tool.VisibilitySubjects)
	result, err := tool.Call(t.Context(), json.RawMessage(`{"name":"audit-routes","args":"src"}`), make(chan rocketcode.ChatResponse, 16))
	require.NoError(t, err)
	assert.Equal(t, rocketcode.TextToolResult("src"), result)

	var taskOnly rocketcode.PermissionSet
	require.NoError(t, taskOnly.Allow("task", "*"))
	_, ok = bridge.maybeDynamicWorkflowTool(root, &rocketcode.Agent{Permission: taskOnly}, "main", "turn-1")
	assert.False(t, ok)

	// Load failure omits tool.
	bad := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bad, ".rocketclaw", "workflows"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bad, ".rocketclaw", "workflows", "Bad_Name.star"), []byte("meta={}\n"), 0o600))
	badRoot := openWorkspaceRoot(t, bad)

	var star rocketcode.PermissionSet
	require.NoError(t, star.Allow("workflow", "*"))
	_, ok = (&Bridge{runtime: &config.Config{Workspace: bad}, log: slog.New(slog.DiscardHandler)}).maybeDynamicWorkflowTool(badRoot, &rocketcode.Agent{Permission: star}, "main", "turn-1")
	assert.False(t, ok)
}

func TestDynamicWorkflowToolNotInRocketClawAutoAllowList(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.4\nmode: primary\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	root := openWorkspaceRoot(t, workspace)

	agents, _, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.NoError(t, err)

	action, matched := agents.Items["main"].Permission.Evaluate("rocketclaw", dynamicWorkflowToolName)
	assert.False(t, matched)
	assert.Equal(t, rocketcode.PermissionDeny, action)
}

func TestWorkflowRunnerRuntimeOmitsDynamicWorkflowTool(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeMainAgentSkills(t, workspace, "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  read: {\"*\": allow}\n  workflow: {\"*\": allow}\n---\nPrompt\n")
	writeWorkflowFixture(t, workspace, "echo", `meta = {"name": "echo", "description": "Echo"}
def main(args):
    return args
`)

	cfg := &config.Config{Workspace: workspace}
	root, agents, skills, resolver, err := prepareRocketCode(cfg, "main", slog.New(slog.DiscardHandler), toolModeWorkflow)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.MkdirAll(filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), ".rocketcode")), 0o755))
	shellRel := filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), ".rocketcode", "wf-test"))
	require.NoError(t, root.Mkdir(shellRel, 0o700))

	runtime, err := rocketcode.NewWithModelResolver(resolver, &rocketcode.Config{
		ShellTempDir:   filepath.Join(cfg.Workspace, filepath.FromSlash(shellRel)),
		Diagnostics:    true,
		ChildRunLogger: rocketcode.DiscardChildRunLog,
		CheckpointSink: rocketcode.InertCheckpointSink{},
		ShellCommand:   rocketcode.DefaultShellCommand,
	}, root, agents, skills, "main", io.Discard)
	require.NoError(t, err)

	_, hasDynamic := runtime.Tools[dynamicWorkflowToolName]
	assert.False(t, hasDynamic)
}

func drainNestedWorkflowProgress(ch <-chan rocketcode.ChatResponse) []rocketcode.ChatResponse {
	var items []rocketcode.ChatResponse

	for {
		select {
		case item := <-ch:
			items = append(items, item)
		default:
			return items
		}
	}
}
