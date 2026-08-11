package harnessbridge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
)

func loadRocketCodeDefinitions(root *os.Root, workspace string, mode toolMode, models ...map[string]string) (rocketcode.Agents, rocketcode.Skills, error) {
	cfg := &config.Config{Workspace: workspace}
	if len(models) > 0 {
		cfg.Models = models[0]
	}

	return loadRocketCodeDefinitionsIn(root, cfg, config.DefaultRuntimeDir, mode)
}

func TestLoadRocketCodeDefinitionsPreparesPersistentAgents(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "assistant", "---\ndescription: Main\nmodel: gpt-5.4\nreasoningEffort: high\nverbosity: low\npermission:\n  bash:\n    \"gh *\": allow\n---\nPrompt\n")
	writeAgent(t, workspace, "restricted", "---\ndescription: Restricted\nmodel: gpt-5.4\npermission:\n  task:\n    \"go-reviewer\": allow\n---\nPrompt\n")
	writeAgent(t, workspace, "helper", "---\ndescription: Helper\nmodel: gpt-5.5\n---\nPrompt\n")
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
	requireNoRocketClawPermissionMatch(t, primary.Permission, restartToolName)
	requireRocketClawPermissionAction(t, primary.Permission, reloadToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, primary.Permission, scheduleMessageToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, primary.Permission, resetScheduledMessagesToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, primary.Permission, attachFilesToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, primary.Permission, updateGoalToolName, rocketcode.PermissionAllow)
	requireNoRocketClawPermissionMatch(t, helper.Permission, restartToolName)
	requireRocketClawPermissionAction(t, helper.Permission, reloadToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, helper.Permission, scheduleMessageToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, helper.Permission, resetScheduledMessagesToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, helper.Permission, attachFilesToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, helper.Permission, updateGoalToolName, rocketcode.PermissionAllow)

	externalAgents, err := ExternalMCPAgentsIn(&config.Config{Workspace: workspace}, config.DefaultRuntimeDir)
	require.NoError(t, err)
	require.Equal(t, []string{"assistant", "helper", "restricted"}, externalAgents)
}

func TestLoadRocketCodeDefinitionsResolvesModelTemplate(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: '{{ model \"team/coding-high\" }}'\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	root, err := os.OpenRoot(workspace)

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	agents, _, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent, map[string]string{"team/coding-high": "software-development-sol"})
	require.NoError(t, err)
	require.Equal(t, "software-development-sol", agents.Items["main"].Model)

	_, _, err = loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.ErrorContains(t, err, `main.md: model: execute model template`)
	require.ErrorContains(t, err, `model "team/coding-high" is not configured`)
}

func TestLoadRocketCodeDefinitionsPreparesCronAgents(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", `---
description: Main
model: gpt-5.4
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
	requireNoRocketClawPermissionMatch(t, agents.Items["main"].Permission, restartToolName)
	requireRocketClawPermissionAction(t, agents.Items["main"].Permission, reloadToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, agents.Items["main"].Permission, scheduleMessageToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, agents.Items["main"].Permission, resetScheduledMessagesToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, agents.Items["main"].Permission, attachFilesToolName, rocketcode.PermissionAllow)
	requireRocketClawPermissionAction(t, agents.Items["main"].Permission, updateGoalToolName, rocketcode.PermissionAllow)
}

func TestLoadRocketCodeDefinitionsPreservesGuardrailReference(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.4\nmode: primary\nguardrail: guardrail\n---\nPrompt\n")
	writeAgent(t, workspace, "guardrail", "---\ndescription: Guardrail\nmodel: gpt-5.5\nreasoningEffort: low\nverbosity: low\npermission:\n  read:\n    \"docs/*\": allow\n---\nCheck delegated work\n")
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
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.4\nmaxRecursion: nope\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	_, _, err = loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.ErrorContains(t, err, "main.md: parse maxRecursion:")
}

func TestLoadRocketCodeDefinitionsReportsMissingModel(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	_, _, err = loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.ErrorContains(t, err, "main.md: model: required non-empty string")
}

func TestLoadRuntimeDefinitionsReportsInvalidStagedAgent(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw-stage", "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw-stage", "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".rocketclaw-stage", "agents", "main.md"), []byte("---\ndescription: Main\n---\nPrompt\n"), 0o644))

	_, _, err := LoadRuntimeDefinitions(&config.Config{Workspace: workspace}, ".rocketclaw-stage")
	require.ErrorContains(t, err, "main.md: model: required non-empty string")
}

func TestLoadRuntimeDefinitionsUsesLoadedModels(t *testing.T) {
	workspace := t.TempDir()
	stage := ".rocketclaw-stage"
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, stage, "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, stage, "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, stage, "agents", "main.md"), []byte("---\ndescription: Main\nmodel: '{{ model \"loaded\" }}'\n---\nPrompt\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "rocketclaw.json"), []byte(`{"models":{"disk":"gpt-5.5"}}`), 0o600))

	_, _, err := LoadRuntimeDefinitions(&config.Config{Workspace: workspace, Models: map[string]string{"loaded": "gpt-5.5"}}, stage)
	require.NoError(t, err)
}

func TestLoadRocketCodeDefinitionsPreparesRocketClawRuntimeToolPermissions(t *testing.T) {
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
			name:           "exact persistent restart allow",
			mode:           toolModePersistent,
			permission:     "permission:\n  rocketclaw:\n    rocketclaw_restart: allow\n",
			wantTool:       restartToolName,
			wantAction:     rocketcode.PermissionAllow,
			wantAllowTools: []string{reloadToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
		{
			name:           "exact cron restart allow",
			mode:           toolModeCron,
			permission:     "permission:\n  rocketclaw:\n    rocketclaw_restart: allow\n",
			wantTool:       restartToolName,
			wantAction:     rocketcode.PermissionAllow,
			wantAllowTools: []string{reloadToolName, rawRunToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
		{
			name:           "exact persistent restart deny",
			mode:           toolModePersistent,
			permission:     "permission:\n  rocketclaw:\n    rocketclaw_restart: deny\n",
			wantTool:       restartToolName,
			wantAction:     rocketcode.PermissionDeny,
			wantAllowTools: []string{reloadToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
		{
			name:           "exact cron restart deny",
			mode:           toolModeCron,
			permission:     "permission:\n  rocketclaw:\n    rocketclaw_restart: deny\n",
			wantTool:       restartToolName,
			wantAction:     rocketcode.PermissionDeny,
			wantAllowTools: []string{reloadToolName, rawRunToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
		{
			name:          "wildcard deny",
			mode:          toolModePersistent,
			permission:    "permission:\n  rocketclaw:\n    rocketclaw_*: deny\n",
			wantTool:      restartToolName,
			wantAction:    rocketcode.PermissionDeny,
			wantDenyTools: []string{reloadToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
		{
			name:          "broad deny followed by narrow allow",
			mode:          toolModePersistent,
			permission:    "permission:\n  rocketclaw:\n    '*': deny\n    rocketclaw_restart: allow\n",
			wantTool:      restartToolName,
			wantAction:    rocketcode.PermissionAllow,
			wantDenyTools: []string{reloadToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.4\nmode: primary\n"+tt.permission+"---\nPrompt\n")
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
model: gpt-5.4
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

func TestRocketCodeReadsAllowedSkillFilesFromConfiguredRuntimeDirectory(t *testing.T) {
	for _, runtimeDir := range []string{".rocketclaw", ".femtoclaw"} {
		t.Run(runtimeDir, func(t *testing.T) {
			workspace := t.TempDir()
			root, err := os.OpenRoot(workspace)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })

			require.NoError(t, root.MkdirAll(filepath.Join(runtimeDir, "agents"), 0o755))
			require.NoError(t, root.WriteFile(filepath.Join(runtimeDir, "agents", "main.md"), []byte("---\ndescription: Main\nmodel: gpt-5.4\npermission:\n  skill:\n    example: allow\n---\nPrompt\n"), 0o644))
			skillDir := filepath.Join(runtimeDir, "skills", "example")
			require.NoError(t, root.MkdirAll(skillDir, 0o755))
			require.NoError(t, root.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: example\ndescription: Example\n---\n"), 0o644))
			require.NoError(t, root.WriteFile(filepath.Join(skillDir, "asset.txt"), []byte("asset"), 0o644))

			agents, skills, err := loadRocketCodeDefinitionsIn(root, &config.Config{Workspace: workspace}, runtimeDir, toolModePersistent)
			require.NoError(t, err)

			client := openai.NewClient()
			runtime, err := rocketcode.New(&client, &rocketcode.Config{ShellOutputDir: workspace, ChildRunLogger: rocketcode.DiscardChildRunLog, CheckpointSink: rocketcode.InertCheckpointSink{}, ShellCommand: rocketcode.DefaultShellCommand}, root, agents, skills, "main", nil)
			require.NoError(t, err)

			action, _ := runtime.Permissions.Evaluate("read", filepath.ToSlash(filepath.Join(skillDir, "asset.txt")))
			require.Equal(t, rocketcode.PermissionAllow, action)
		})
	}
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

	_, _, err = loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.ErrorContains(t, err, "main.md: read agent: openat main.md: path escapes from parent")
}

func TestLoadRocketCodeDefinitionsRejectsEscapingSkillSymlink(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.4\nmode: primary\n---\nPrompt\n")
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

func requireNoRocketClawPermissionMatch(t *testing.T, set rocketcode.PermissionSet, subject string) {
	t.Helper()

	_, matched := set.Evaluate("rocketclaw", subject)
	require.False(t, matched)
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
