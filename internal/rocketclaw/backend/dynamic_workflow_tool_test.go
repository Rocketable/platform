package backend

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
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

func TestDynamicWorkflowToolSchemaAndCall(t *testing.T) {
	t.Parallel()

	var permissions harness.PermissionSet
	require.NoError(t, permissions.Allow("workflow", "audit"))

	workspace := t.TempDir()
	writeMainAgentSkills(t, workspace, "---\ndescription: Main\nmodel: gpt-6.1-sol\n---\nPrompt\n")
	writeWorkflowFixture(t, workspace, "audit", `meta = {"name": "audit", "description": "Audit routes", "phases": ["work"]}
def main(args):
    return phase("work", lambda: "audit:" + args)
`)
	writeWorkflowFixture(t, workspace, "secret", `meta = {"name": "secret", "description": "Secret"}
def main(args): return None
`)
	root := openWorkspaceRoot(t, workspace)
	definitions, err := workflow.Load(root, ".rocketclaw")
	require.NoError(t, err)

	bridge := &Bridge{runtime: config.NewLockedConfig(&config.Config{Workspace: workspace}), log: slog.New(slog.DiscardHandler)}

	output := make(chan harness.ChatResponse, 4)
	tool, ok := bridge.dynamicWorkflowTool(permissions, "main", "main", definitions, nil)
	require.True(t, ok)
	assert.Equal(t, "workflow", tool.Permission)
	assert.True(t, tool.Resumable)
	assert.Equal(t, []string{"audit"}, tool.VisibilitySubjects)

	wantDescription := "Run a saved Starlark workflow as a nested tool call inside this turn and return its final result text.\nThis does not start a second managed conversation turn.\nPass args as an empty string when the workflow needs no arguments.\n\nAvailable workflows (permission.workflow subjects):\n- audit: Audit routes"
	assert.Equal(t, wantDescription, tool.Description)

	subjects, err := tool.Subjects(json.RawMessage(`{"name":"audit","args":""}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"audit"}, subjects)

	_, err = tool.Subjects(json.RawMessage(`{"name":"","args":""}`))
	require.ErrorContains(t, err, "name is required")

	result, err := tool.Call(t.Context(), json.RawMessage(`{"name":"audit","args":"path/to"}`), output)
	require.NoError(t, err)
	assert.Equal(t, harness.TextToolResult("audit:path/to"), result)
	require.Empty(t, output)

	_, ok = bridge.dynamicWorkflowTool(harness.PermissionSet{}, "main", "main", definitions, nil)
	assert.False(t, ok)

	sudo, ok := bridge.dynamicWorkflowTool(harness.PermissionSet{Sudo: true}, "main", "main", definitions, nil)
	require.True(t, ok)
	assert.Equal(t, []string{"audit", "secret"}, sudo.VisibilitySubjects)

	for _, definitions := range []map[string]*workflow.Definition{nil, {}} {
		_, ok := bridge.dynamicWorkflowTool(permissions, "main", "main", definitions, nil)
		assert.False(t, ok)
	}

	require.NoError(t, permissions.Allow("workflow", "sec*"))
	tool, ok = bridge.dynamicWorkflowTool(permissions, "main", "main", definitions, nil)
	require.True(t, ok)
	assert.Equal(t, []string{"audit", "secret"}, tool.VisibilitySubjects)
	assert.Equal(t, wantDescription+"\n- secret: Secret", tool.Description)
	require.NoError(t, permissions.Deny("workflow", "secret"))
	tool, ok = bridge.dynamicWorkflowTool(permissions, "main", "main", definitions, nil)
	require.True(t, ok)
	assert.Equal(t, []string{"audit"}, tool.VisibilitySubjects)
	assert.Equal(t, wantDescription, tool.Description)
	assert.Equal(t, "Secret", definitions["secret"].Description)

	require.NoError(t, permissions.Allow("workflow", "secret"))

	definitions["audit"].Description = "\t Audit routes \n"
	definitions["secret"].Description = "\n\t "
	tool, ok = bridge.dynamicWorkflowTool(permissions, "main", "main", definitions, nil)
	require.True(t, ok)
	assert.Equal(t, []string{"audit", "secret"}, tool.VisibilitySubjects)
	assert.Equal(t, wantDescription+"\n- secret: Saved Starlark workflow.", tool.Description)
	assert.Equal(t, "\t Audit routes \n", definitions["audit"].Description)
	assert.Equal(t, "\n\t ", definitions["secret"].Description)
}

func TestRunNestedWorkflowAndMaybeTool(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeMainAgentSkills(t, workspace, "---\ndescription: Main\nmodel: gpt-6.1-sol\n---\nPrompt\n")
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
    return phase("work", lambda: args)
`)

	root := openWorkspaceRoot(t, workspace)
	bridge := &Bridge{runtime: config.NewLockedConfig(&config.Config{Workspace: workspace}), log: slog.New(slog.DiscardHandler)}

	// Nested run with turn-start definitions (same freeze as production Call).
	definitions, err := workflow.Load(root, ".rocketclaw")
	require.NoError(t, err)

	_, err = bridge.runNestedWorkflow(t.Context(), "main", "main", "missing", nil, "", nil)
	require.ErrorContains(t, err, `workflow "missing" is not configured`)

	text, err := bridge.runNestedWorkflow(t.Context(), "main", "main", "echo", definitions["echo"], "hello-nested", nil)
	require.NoError(t, err)
	assert.Equal(t, "hello-nested", text)

	text, err = bridge.runNestedWorkflow(t.Context(), "main", "main", "quiet", definitions["quiet"], "", nil)
	require.NoError(t, err)
	assert.Equal(t, nestedWorkflowSilentCompleteText, text)

	// maybeDynamicWorkflowTool: workflow allow → tool; task-only → omit; Call works.
	var workflowAllow harness.PermissionSet
	require.NoError(t, workflowAllow.Allow("workflow", "audit-routes"))
	tool, ok := bridge.maybeDynamicWorkflowTool(root, &harness.Agent{Permission: workflowAllow}, "main", "main", nil)
	require.True(t, ok)
	assert.Equal(t, []string{"audit-routes"}, tool.VisibilitySubjects)

	output := make(chan harness.ChatResponse, 16)
	result, err := tool.Call(t.Context(), json.RawMessage(`{"name":"audit-routes","args":"src"}`), output)
	require.NoError(t, err)
	assert.Equal(t, harness.TextToolResult("src"), result)
	require.Empty(t, output)

	var taskOnly harness.PermissionSet
	require.NoError(t, taskOnly.Allow("task", "*"))
	_, ok = bridge.maybeDynamicWorkflowTool(root, &harness.Agent{Permission: taskOnly}, "main", "main", nil)
	assert.False(t, ok)

	// Load failure omits tool.
	bad := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bad, ".rocketclaw", "workflows"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bad, ".rocketclaw", "workflows", "Bad_Name.star"), []byte("meta={}\n"), 0o600))
	badRoot := openWorkspaceRoot(t, bad)

	var star harness.PermissionSet
	require.NoError(t, star.Allow("workflow", "*"))

	var logs strings.Builder

	badBridge := &Bridge{runtime: config.NewLockedConfig(&config.Config{Workspace: bad}), log: slog.New(slog.NewTextHandler(&logs, nil))}
	_, ok = badBridge.maybeDynamicWorkflowTool(badRoot, &harness.Agent{Permission: star}, "main", "main", nil)
	assert.False(t, ok)
	assert.Contains(t, logs.String(), "load workflows")

	logs.Reset()
	require.NoError(t, star.Deny("workflow", "*"))
	_, ok = badBridge.maybeDynamicWorkflowTool(badRoot, &harness.Agent{Permission: star}, "main", "main", nil)
	assert.False(t, ok)
	assert.Contains(t, logs.String(), "load workflows", "an overridden allow rule still triggers workflow loading")
}

func TestNestedWorkflowSessionTags(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeMainAgentSkills(t, workspace, "---\nmodel: gpt-6.1-sol\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[customer, internal]]\n---\nPrompt\n")
	root := openWorkspaceRoot(t, workspace)
	require.NoError(t, root.MkdirAll(".rocketclaw/workflows", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/workflows/tag.star", []byte("meta = {\"name\": \"tag\", \"description\": \"Tag\"}\ndef main(args): return agent(\"tag\", label=\"worker\")\n"), 0o600))
	service := newTestSessionServiceAt(t, workspace)
	recordTestConversations(t, service, "main", map[string]ConversationKind{"visible": KindWebChat})

	requests := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []struct{ Type, Output string }
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}

		requests++

		w.Header().Set("Content-Type", "application/json")

		if requests == 1 {
			writeRawRunFunctionCall(t, w, "set", "execute", json.RawMessage(`{"code":"def main():\n    return rocketclaw_set_tag(tag=\"customer\")\n"}`))
		} else {
			for _, item := range body.Input {
				if item.Type == "function_call_output" {
					assert.JSONEq(t, `{"tags":["customer"]}`, item.Output)
				}
			}

			writeRawRunMessage(t, w, "done", "message", "done")
		}
	}))
	defer server.Close()

	// A nested workflow inside a producer turn tags the turn's visible destination.
	bridge := &Bridge{runtime: config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}), config: Config{ConversationID: "external_mcp:owning", SessionService: service}, log: slog.New(slog.DiscardHandler)}
	definitions, err := workflow.Load(root, ".rocketclaw")
	require.NoError(t, err)

	for range 2 {
		_, err = bridge.runNestedWorkflow(t.Context(), "main", "visible", "tag", definitions["tag"], "", nil)
		require.NoError(t, err)
		require.Equal(t, 2, requests, "a rerun returns the finished worker's recorded result")
	}

	ids, err := queryStrings(t.Context(), service.db, "SELECT conversation_id FROM conversations WHERE tags IS NOT NULL", "tag owners")
	require.NoError(t, err)
	require.Equal(t, []string{"visible"}, ids)
	tags, err := sessionTags(t.Context(), service.db, "visible")
	require.NoError(t, err)
	require.Equal(t, []string{"customer"}, tags)
}

// A nested workflow's steps run with the environment of the turn that calls it.
func TestNestedWorkflowStepsSeeTurnEnvironment(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeMainAgentSkills(t, workspace, "---\nmodel: gpt-6.1-sol\npermission:\n  bash: allow\n---\nPrompt\n")
	root := openWorkspaceRoot(t, workspace)
	require.NoError(t, root.MkdirAll(".rocketclaw/workflows", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/workflows/env.star", []byte("meta = {\"name\": \"env\", \"description\": \"Env\"}\ndef main(args): return agent(\"print\", label=\"worker\")\n"), 0o600))

	var output string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []struct{ Type, Output string }
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}

		w.Header().Set("Content-Type", "application/json")

		if last := body.Input[len(body.Input)-1]; last.Type == "function_call_output" {
			output = last.Output

			writeRawRunMessage(t, w, "done", "message", "done")

			return
		}

		writeRawRunFunctionCall(t, w, "print", "execute", executeBashScript(`printf %s "${ROCKETCLAW_METADATA_TICKET-unset}"`))
	}))
	defer server.Close()

	bridge := &Bridge{runtime: config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}), config: Config{ConversationID: "chat", SessionService: newTestSessionServiceAt(t, workspace)}, log: slog.New(slog.DiscardHandler)}
	definitions, err := workflow.Load(root, ".rocketclaw")
	require.NoError(t, err)

	_, err = bridge.runNestedWorkflow(t.Context(), "main", "chat", "env", definitions["env"], "", map[string]string{"ROCKETCLAW_METADATA_TICKET": "T-1"})
	require.NoError(t, err)
	require.Equal(t, "T-1", output)
}

func TestDynamicWorkflowToolNotInRocketClawAutoAllowList(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.4\nmode: primary\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	root := openWorkspaceRoot(t, workspace)

	agents, _, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir, toolModePersistent)
	require.NoError(t, err)

	action, matched := agents.Items["main"].Permission.Evaluate("rocketclaw", dynamicWorkflowToolName)
	assert.False(t, matched)
	assert.Equal(t, harness.PermissionDeny, action)
}

func TestWorkflowRunnerRuntimeOmitsDynamicWorkflowTool(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeMainAgentSkills(t, workspace, "---\ndescription: Main\nmodel: gpt-6.1-sol\npermission:\n  read: {\"*\": allow}\n  workflow: {\"*\": allow}\n---\nPrompt\n")
	writeWorkflowFixture(t, workspace, "echo", `meta = {"name": "echo", "description": "Echo"}
def main(args):
    return args
`)

	cfg := &config.Config{Workspace: workspace}
	root, agents, skills, resolver, err := prepareRocketCode(config.NewLockedConfig(cfg), "main", slog.New(slog.DiscardHandler), toolModeWorkflow)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.MkdirAll(filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), ".rocketcode")), 0o755))
	shellRel := filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), ".rocketcode", "wf-test"))
	require.NoError(t, root.Mkdir(shellRel, 0o700))

	runtime, err := harness.NewWithModelResolver(resolver, &harness.Config{
		ShellTempDir:      filepath.Join(cfg.Workspace, filepath.FromSlash(shellRel)),
		RetainedResultDir: "retained",
		ChildSessions:     harness.InertChildSessions{},
		Journal:           harness.InertJournal{},
		ShellCommand:      harness.DefaultShellCommand{},
	}, root, agents, skills, "main", io.Discard)
	require.NoError(t, err)

	_, hasDynamic := runtime.Tools[dynamicWorkflowToolName]
	assert.False(t, hasDynamic)
}
