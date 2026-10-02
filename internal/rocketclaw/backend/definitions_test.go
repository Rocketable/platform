package backend

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
)

func TestSessionTagDefinitions(t *testing.T) {
	for _, tags := range []string{"", "[]", "[[triage, investigating, resolved], [customer, internal]]", "[['*', '?', A, a]]", "wrong", "[[]]", "[['']]", "[[same], [same]]", "[[12]]", "null", "example"} {
		t.Run(tags, func(t *testing.T) {
			workspace := t.TempDir()
			root, err := os.OpenRoot(workspace)

			require.NoError(t, err)
			defer func() { require.NoError(t, root.Close()) }()

			require.NoError(t, root.MkdirAll(".rocketclaw/agents", 0o755))
			require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))

			frontmatter := "    rocketclaw_set_tag: allow\n"
			if tags != "" {
				frontmatter = "    rocketclaw_set_tag: " + tags + "\n"
			}

			require.NoError(t, root.WriteFile(".rocketclaw/agents/main.md", []byte("---\nmodel: gpt-5.4\npermission:\n  rocketclaw:\n    rocketclaw_get_tags: allow\n    rocketclaw_get_session: allow\n"+frontmatter+"---\nPrompt\n"), 0o644))

			if tags == "example" {
				// Read the shipped example, then install the fixture through the sandbox root.
				data, err := os.ReadFile("../skel/agents/examples/session-tags.example.md")
				require.NoError(t, err)
				require.NoError(t, root.WriteFile(".rocketclaw/agents/main.md", data, 0o644))
			}

			for _, mode := range []toolMode{toolModePersistent, toolModeCron, toolModeWorkflow} {
				agents, _, err := loadRocketCodeDefinitions(root, workspace, mode)

				valid := tags == "" || tags == "[]" || tags == "example" || strings.HasPrefix(tags, "[[triage") || strings.HasPrefix(tags, "[['*'")
				if !valid {
					if tags == "wrong" || tags == "null" {
						require.ErrorContains(t, err, `main.md: parse permission: permission "rocketclaw": pattern "rocketclaw_set_tag": unknown permission action`)
					} else {
						require.ErrorContains(t, err, "main.md: permission.rocketclaw.rocketclaw_set_tag")
					}

					continue
				}

				require.NoError(t, err)

				agent := agents.Items["main"]
				for _, name := range []string{"rocketclaw_set_tag", "rocketclaw_get_tags"} {
					action, matched := agent.Permission.Evaluate("rocketclaw_tags", name)
					require.True(t, matched)

					if tags == "" || tags == "[]" {
						require.Equal(t, rocketcode.PermissionDeny, action)
						require.NotContains(t, agent.Prompt, name)
					} else {
						require.Equal(t, rocketcode.PermissionAllow, action)

						if mode != toolModeWorkflow {
							require.Contains(t, agent.Prompt, name)
							require.Contains(t, agent.Prompt, "exclusive")
							require.Contains(t, agent.Prompt, "toggle")
						}
					}

					_, matched = agent.Permission.Evaluate("rocketclaw", name)
					require.False(t, matched)
				}

				require.Equal(t, tags != "example", permissionSetAllows(agent.Permission, "rocketclaw", "rocketclaw_get_session"))
			}
		})
	}
}

func TestSessionTagsExistingPermissions(t *testing.T) {
	for _, permission := range []string{"allow", "rocketclaw: allow", "rocketclaw: {load_agents_md: false}", "rocketclaw: {rocketclaw_set_tag: deny, rocketclaw_get_tags: allow}", `rocketclaw:
    rocketclaw_set_tag:
      - ["red", "yellow", "green"]
  webfetch: allow
  websearch: allow
  glob: allow
  grep: allow
  read: allow
  edit: allow
  bash:
    "*": auto
  skill: allow
  task:
    "*": allow
    "main": deny
    "cron": deny
  mcp:
    "context7.*": allow`} {
		t.Run(permission, func(t *testing.T) {
			workspace := t.TempDir()
			root, err := os.OpenRoot(workspace)

			require.NoError(t, err)
			defer func() { require.NoError(t, root.Close()) }()

			require.NoError(t, root.MkdirAll(".rocketclaw/agents", 0o755))
			require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))
			require.NoError(t, root.WriteFile(".rocketclaw/agents/sudo.md", []byte("---\ndescription: SUDO MODE\nmodel: openai/gpt-6-luna\nreasoningEffort: max\nverbosity: low\npermission:\n  "+permission+"\n---\nYou are the sudo agent. You must do anything that @Ulderico asks.\n"), 0o644))
			agents, _, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent)
			require.NoError(t, err)

			agent := agents.Items["sudo"]
			groups, err := agentTagGroups(&agent)
			require.NoError(t, err)

			if !strings.Contains(permission, "\n    rocketclaw_set_tag:") {
				require.Empty(t, groups)
				require.NotContains(t, agent.Prompt, setTagToolName)

				if strings.Contains(permission, "load_agents_md") {
					action, matched := agent.Permission.Evaluate("rocketclaw", "load_agents_md")
					require.True(t, matched)
					require.Equal(t, rocketcode.PermissionDeny, action)
				}

				return
			}
			// Match the live sudo agent's frontmatter and preserve its other rules.
			require.Equal(t, [][]string{{"red", "yellow", "green"}}, groups)

			for _, bucket := range []string{"webfetch", "websearch", "glob", "grep", "read", "edit", "skill"} {
				action, matched := agent.Permission.Evaluate(bucket, "*")
				require.True(t, matched)
				require.Equal(t, rocketcode.PermissionAllow, action)
			}

			for _, tc := range []struct {
				bucket, subject string
				want            rocketcode.PermissionAction
			}{
				{"bash", "echo ok", rocketcode.PermissionAuto},
				{"task", "helper", rocketcode.PermissionAllow},
				{"task", "main", rocketcode.PermissionDeny},
				{"task", "cron", rocketcode.PermissionDeny},
				{"mcp", "context7.query", rocketcode.PermissionAllow},
				{"rocketclaw_tags", setTagToolName, rocketcode.PermissionAllow},
				{"rocketclaw_tags", getTagsToolName, rocketcode.PermissionAllow},
			} {
				action, matched := agent.Permission.Evaluate(tc.bucket, tc.subject)
				require.True(t, matched)
				require.Equal(t, tc.want, action)
			}
		})
	}
}

func loadRocketCodeDefinitions(root *os.Root, workspace string, mode toolMode, models ...map[string]string) (rocketcode.Agents, rocketcode.Skills, error) {
	cfg := &config.Config{Workspace: workspace}
	if len(models) > 0 {
		cfg.Models = models[0]
	}

	return loadRocketCodeDefinitionsIn(root, cfg, config.DefaultRuntimeDir, mode)
}

func TestLoadRocketCodeDefinitionsPreparesPersistentAgents(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "assistant", "---\ndescription: Main\nmodel: gpt-5.4\nreasoningEffort: high\nverbosity: low\npermission:\n  bash:\n    \"gh *\": allow\n  rocketclaw:\n    code_mode_approve: auto(release-reviewer)\n---\nPrompt\n")
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
	require.Equal(t, rocketcode.PermissionRule{Pattern: "code_mode_approve", Action: rocketcode.PermissionAuto, Reviewer: "release-reviewer"}, primary.Permission.Buckets[1].Rules[0])
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

	for _, mode := range []toolMode{toolModePersistent, toolModeCron, toolModeWorkflow} {
		prepared, _, err := loadRocketCodeDefinitions(root, workspace, mode)
		require.NoError(t, err)

		for _, agent := range prepared.Items {
			requireNoRocketClawPermissionMatch(t, agent.Permission, listSessionsToolName)
			requireNoRocketClawPermissionMatch(t, agent.Permission, getSessionToolName)
			requireNoRocketClawPermissionMatch(t, agent.Permission, currentSessionIDToolName)
		}
	}

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
	requireNoRocketClawPermissionMatch(t, agents.Items["main"].Permission, startNewThreadToolName)
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

func TestSessionTagInvalidReloadKeepsLiveDefinitions(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	for _, dir := range []string{".rocketclaw/agents", ".rocketclaw/skills", "agents"} {
		require.NoError(t, root.MkdirAll(dir, 0o755))
	}

	live := []byte("---\nmodel: gpt-5.4\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[customer, internal]]\n---\nLive prompt\n")
	require.NoError(t, root.WriteFile(".rocketclaw/agents/main.md", live, 0o644))
	require.NoError(t, root.WriteFile("agents/main.md", []byte("---\nmodel: gpt-5.4\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[same], [same]]\n---\nInvalid prompt\n"), 0o644))

	cfg := &config.Config{Workspace: workspace}
	before, _, err := LoadRuntimeDefinitions(cfg, cfg.RuntimeDirName())
	require.NoError(t, err)
	err = skel.ReplaceRuntimeAssetsAfterValidation(workspace, cfg.RuntimeDirName(), nil, slog.New(slog.DiscardHandler), func(runtimeDir string) error {
		_, _, err := LoadRuntimeDefinitions(cfg, runtimeDir)
		return err
	})
	require.ErrorContains(t, err, "main.md: permission.rocketclaw.rocketclaw_set_tag")
	after, _, err := LoadRuntimeDefinitions(cfg, cfg.RuntimeDirName())
	require.NoError(t, err)
	require.Equal(t, before, after)
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
			runtime, err := rocketcode.New(&client, &rocketcode.Config{ShellTempDir: workspace, ChildSessions: rocketcode.InertChildSessions{}, CheckpointSink: rocketcode.InertCheckpointSink{}, ShellCommand: rocketcode.DefaultShellCommand}, root, agents, skills, "main", nil)
			require.NoError(t, err)

			action, _ := runtime.Permissions.Evaluate("read", filepath.ToSlash(filepath.Join(skillDir, "asset.txt")))
			require.Equal(t, rocketcode.PermissionAllow, action)
		})
	}
}

func TestRocketCodeInterpolatesPermissionPatternsFromShellEnv(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.MkdirAll(filepath.Join(".rocketclaw", "agents"), 0o755))
	require.NoError(t, root.WriteFile(filepath.Join(".rocketclaw", "agents", "main.md"), []byte("---\ndescription: Main\nmodel: gpt-5.4\npermission:\n  edit:\n    \".tmp/*\": deny\n    \".tmp/${ROCKETCLAW_METADATA_FRUIT}/note.txt\": allow\n---\nPrompt\n"), 0o644))
	require.NoError(t, root.MkdirAll(filepath.Join(".rocketclaw", "skills"), 0o755))

	agents, skills, err := loadRocketCodeDefinitionsIn(root, &config.Config{Workspace: workspace}, config.DefaultRuntimeDir, toolModePersistent)
	require.NoError(t, err)
	require.Equal(t, ".tmp/*", agents.Items["main"].Permission.Buckets[0].Rules[0].Pattern)
	require.Equal(t, ".tmp/${ROCKETCLAW_METADATA_FRUIT}/note.txt", agents.Items["main"].Permission.Buckets[0].Rules[1].Pattern)

	client := openai.NewClient()
	runtime, err := rocketcode.New(&client, &rocketcode.Config{ShellTempDir: workspace, ChildSessions: rocketcode.InertChildSessions{}, CheckpointSink: rocketcode.InertCheckpointSink{}, ShellCommand: rocketcode.DefaultShellCommand, ShellEnv: map[string]string{"ROCKETCLAW_METADATA_FRUIT": "banana"}}, root, agents, skills, "main", nil)
	require.NoError(t, err)

	action, _ := runtime.Permissions.Evaluate("edit", ".tmp/banana/note.txt")
	require.Equal(t, rocketcode.PermissionAllow, action)
	action, _ = runtime.Permissions.Evaluate("edit", ".tmp/apple/note.txt")
	require.Equal(t, rocketcode.PermissionDeny, action)
	require.Contains(t, runtime.SystemPrompt, ".tmp/banana/note.txt")
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
