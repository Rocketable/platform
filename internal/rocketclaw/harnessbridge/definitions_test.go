package harnessbridge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
)

func loadRocketCodeDefinitions(root *os.Root, workspace string, mode toolMode) (rocketcode.Agents, rocketcode.Skills, error) {
	return loadRocketCodeDefinitionsIn(root, workspace, config.DefaultWorkDir, mode)
}

func ExternalMCPAgents(workspace string) ([]string, error) {
	return ExternalMCPAgentsIn(workspace, config.DefaultWorkDir)
}

func TestLoadRocketCodeDefinitionsPreparesPersistentAgents(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "assistant", "---\ndescription: Main\nmodel: openai/gpt-5.4\nreasoningEffort: high\nverbosity: low\npermission:\n  bash:\n    \"gh *\": allow\n---\nPrompt\n")
	writeAgent(t, workspace, "restricted", "---\ndescription: Restricted\npermission:\n  task:\n    \"go-reviewer\": allow\n---\nPrompt\n")
	writeAgent(t, workspace, "helper", "---\ndescription: Helper\nmodel: openai/gpt-5.5\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	agents, _, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.NoError(t, err)

	primary := agents.Items["assistant"]
	helper := agents.Items["helper"]
	restricted := agents.Items["restricted"]

	require.Equal(t, "gpt-5.4", primary.Model)
	require.Equal(t, "gpt-5.5", helper.Model)
	require.True(t, permissionSetAllows(primary.Permission, "bash", "gh *"))
	require.False(t, permissionSetAllows(primary.Permission, "task", "*"))
	require.False(t, permissionSetAllows(helper.Permission, "task", "*"))
	require.True(t, permissionSetAllows(restricted.Permission, "task", "go-reviewer"))
	require.False(t, permissionSetAllows(restricted.Permission, "task", "*"))
	requireRocketClawPermissionAction(t, primary.Permission, restartToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, primary.Permission, scheduleMessageToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, primary.Permission, resetScheduledMessagesToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, primary.Permission, attachFilesToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, primary.Permission, updateGoalToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, helper.Permission, restartToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, helper.Permission, scheduleMessageToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, helper.Permission, resetScheduledMessagesToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, helper.Permission, attachFilesToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, helper.Permission, updateGoalToolName, rocketcode.PermissionAllow)

	externalAgents, err := ExternalMCPAgents(workspace)
	require.NoError(t, err)
	require.Equal(t, []string{"assistant", "helper", "restricted"}, externalAgents)
}

func TestLoadRocketCodeDefinitionsPreparesCronAgents(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", `---
description: Main
mode: primary
---
Prompt
`)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	agents, _, err := loadRocketCodeDefinitions(root, workspace, toolModeCron)
	require.NoError(t, err)
	requireRocketClawPermissionAction(t, agents.Items["main"].Permission, rawRunToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, agents.Items["main"].Permission, restartToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, agents.Items["main"].Permission, scheduleMessageToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, agents.Items["main"].Permission, resetScheduledMessagesToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, agents.Items["main"].Permission, attachFilesToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, agents.Items["main"].Permission, updateGoalToolName, rocketcode.PermissionAllow)
}

func TestLoadRocketCodeDefinitionsPreservesGuardrailReference(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nguardrail: guardrail\n---\nPrompt\n")
	writeAgent(t, workspace, "guardrail", "---\ndescription: Guardrail\nmodel: openai/gpt-5.5\nreasoningEffort: low\nverbosity: low\npermission:\n  read:\n    \"docs/*\": allow\n---\nCheck delegated work\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	agents, _, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.NoError(t, err)

	main := agents.Items["main"]
	guardrail := agents.Items["guardrail"]

	require.Equal(t, "guardrail", main.Guardrail)
	require.Equal(t, "Check delegated work", guardrail.Prompt)
	require.Equal(t, "gpt-5.5", guardrail.Model)
	require.Equal(t, "low", guardrail.ReasoningEffort)
	require.Equal(t, "low", guardrail.Verbosity)
	action, matched := guardrail.Permission.Evaluate("read", "docs/a.md")
	require.True(t, matched)
	require.Equal(t, rocketcode.PermissionAllow, action)
}

func TestLoadRocketCodeDefinitionsReportsInvalidMaxRecursion(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmaxRecursion: nope\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	_, _, err = loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.ErrorContains(t, err, "main.md: parse maxRecursion:")
}

func TestLoadRocketCodeDefinitionsPreservesRocketClawRuntimeToolDenies(t *testing.T) {
	tests := []struct {
		name           string
		mode           toolMode
		permission     string
		wantTool       string
		wantAction     rocketcode.PermissionAction
		wantAllowTools []string
		wantDenyTools  []string
	}{
		{
			name:           "exact persistent restart deny",
			mode:           toolModePersistent,
			permission:     "permission:\n  rocketclaw:\n    rocketclaw_restart: deny\n",
			wantTool:       restartToolName,
			wantAction:     rocketcode.PermissionDeny,
			wantAllowTools: []string{scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
		{
			name:           "exact cron restart deny",
			mode:           toolModeCron,
			permission:     "permission:\n  rocketclaw:\n    rocketclaw_restart: deny\n",
			wantTool:       restartToolName,
			wantAction:     rocketcode.PermissionDeny,
			wantAllowTools: []string{rawRunToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
		{
			name:          "wildcard deny",
			mode:          toolModePersistent,
			permission:    "permission:\n  rocketclaw:\n    rocketclaw_*: deny\n",
			wantTool:      restartToolName,
			wantAction:    rocketcode.PermissionDeny,
			wantDenyTools: []string{scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
		{
			name:          "broad deny followed by narrow allow",
			mode:          toolModePersistent,
			permission:    "permission:\n  rocketclaw:\n    '*': deny\n    rocketclaw_restart: allow\n",
			wantTool:      restartToolName,
			wantAction:    rocketcode.PermissionAllow,
			wantDenyTools: []string{scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\n"+tt.permission+"---\nPrompt\n")
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

			root, err := os.OpenRoot(workspace)
			require.NoError(t, err)

			defer func() { require.NoError(t, root.Close()) }()

			agents, _, err := loadRocketCodeDefinitions(root, workspace, tt.mode)
			require.NoError(t, err)

			requireRocketClawPermissionAction(t, agents.Items["main"].Permission, tt.wantTool, tt.wantAction)

			for _, tool := range tt.wantAllowTools {
				requireRocketClawPermissionAction(t, agents.Items["main"].Permission, tool, rocketcode.PermissionAllow)
			}

			for _, tool := range tt.wantDenyTools {
				requireRocketClawPermissionAction(t, agents.Items["main"].Permission, tool, rocketcode.PermissionDeny)
			}
		})
	}
}

func TestLoadRocketCodeDefinitionsLoadsStructuredSkillMetadata(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", `---
description: Main
mode: primary
---
Prompt
`)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills", "example"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".rocketclaw", "skills", "example", "SKILL.md"), []byte(`---
name: example
description: Structured metadata should load
metadata:
  openclaw:
    tools: true
---
Content
`), 0o644))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	agents, skills, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.NoError(t, err)
	require.Contains(t, agents.Items, "main")

	skill := skills.Items["example"]
	require.Equal(t, "Structured metadata should load", skill.Description)
	require.Equal(t, map[string]any{"tools": true}, skill.Metadata["openclaw"])
}

func TestLoadRocketCodeDefinitionsRejectsEscapingAgentSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "main.md")
	require.NoError(t, os.WriteFile(outside, []byte("---\ndescription: Outside\nmode: primary\n---\nOutside\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(workspace, ".rocketclaw", "agents", "main.md")))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	agents, _, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.NoError(t, err)
	require.Empty(t, agents.Items)
}

func TestLoadRocketCodeDefinitionsRejectsEscapingSkillSymlink(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\n---\nPrompt\n")
	outside := filepath.Join(t.TempDir(), "SKILL.md")
	require.NoError(t, os.WriteFile(outside, []byte("---\nname: outside\ndescription: Outside\n---\nOutside\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills", "outside"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(workspace, ".rocketclaw", "skills", "outside", "SKILL.md")))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	_, skills, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.NoError(t, err)
	require.Empty(t, skills.Items)
}

func writeAgent(t *testing.T, workspace, name, content string) {
	t.Helper()

	dir := filepath.Join(workspace, ".rocketclaw", "agents")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644))
}

func requireRocketClawPermissionAction(t *testing.T, set rocketcode.PermissionSet, subject string, want rocketcode.PermissionAction) {
	t.Helper()

	action, matched := set.Evaluate("rocketclaw", subject)
	require.True(t, matched)
	require.Equal(t, want, action)
}

func permissionSetAllows(set rocketcode.PermissionSet, bucket, pattern string) bool {
	for _, candidate := range set.Buckets {
		if candidate.Name != bucket {
			continue
		}

		for _, rule := range candidate.Rules {
			if rule.Pattern == pattern && rule.Action == rocketcode.PermissionAllow {
				return true
			}
		}
	}

	return false
}
