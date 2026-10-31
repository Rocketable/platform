package backend

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	openai "github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestSessionTagDefinitions(t *testing.T) {
	t.Parallel()

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

			require.NoError(t, root.WriteFile(".rocketclaw/agents/main.md", []byte("---\nmodel: gpt-5.4\npermission:\n  rocketclaw:\n    rocketclaw_get_tags: allow\n    rocketclaw_sessions: allow\n"+frontmatter+"---\nPrompt\n"), 0o644))

			if tags == "example" {
				// Read the shipped example, then install the fixture through the sandbox root.
				data, err := os.ReadFile("../skel/agents/examples/session-tags.example.md")
				require.NoError(t, err)
				require.NoError(t, root.WriteFile(".rocketclaw/agents/main.md", data, 0o644))
			}

			for _, mode := range []toolMode{toolModePersistent, toolModeWorkflow} {
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
						require.Equal(t, harness.PermissionDeny, action)
						require.NotContains(t, agent.Prompt, name)
					} else {
						require.Equal(t, harness.PermissionAllow, action)

						if mode != toolModeWorkflow {
							require.Contains(t, agent.Prompt, name)
							require.Contains(t, agent.Prompt, "exclusive")
							require.Contains(t, agent.Prompt, "toggle")
						}
					}

					_, matched = agent.Permission.Evaluate("rocketclaw", name)
					require.False(t, matched)
				}

				require.Equal(t, tags != "example", permissionSetAllows(agent.Permission, "rocketclaw", sessionsSubject))
			}
		})
	}
}

func TestLoadRocketCodeDefinitionsSudo(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll(".rocketclaw/agents", 0o755))
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/agents/tagged.md", []byte("---\nmodel: openai/gpt-6-luna\nsudo: yes\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[red, green]]\n---\nPrompt\n"), 0o644))
	require.NoError(t, root.WriteFile(".rocketclaw/agents/untagged.md", []byte("---\nmodel: openai/gpt-6-luna\nsudo: yes\n---\nPrompt\n"), 0o644))

	agents, _, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.NoError(t, err)

	tagged, untagged := agents.Items["tagged"], agents.Items["untagged"]

	// Conversation helpers skip review; every other RocketClaw tool is reviewed.
	for subject, want := range map[string]harness.PermissionAction{
		reloadToolName: harness.PermissionAllow, scheduleMessageToolName: harness.PermissionAllow, attachFilesToolName: harness.PermissionAllow,
		updateGoalToolName: harness.PermissionAllow, askUserQuestionToolName: harness.PermissionAllow,
		restartToolName: harness.PermissionAuto, sessionsWriteToolName: harness.PermissionAuto, sessionsListToolName: harness.PermissionAuto,
	} {
		action, _ := tagged.Permission.Evaluate("rocketclaw", subject)
		require.Equal(t, want, action, subject)
	}

	require.True(t, tagged.Permission.Enabled("rocketclaw", restartToolName))

	for _, name := range []string{setTagToolName, getTagsToolName} {
		action, _ := tagged.Permission.Evaluate("rocketclaw_tags", name)
		require.Equal(t, harness.PermissionAuto, action, name)
		require.Contains(t, tagged.Prompt, name)

		action, _ = untagged.Permission.Evaluate("rocketclaw_tags", name)
		require.Equal(t, harness.PermissionDeny, action, name)
		require.NotContains(t, untagged.Prompt, name)
	}
}

func TestSessionTagsExistingPermissions(t *testing.T) {
	t.Parallel()

	for _, permission := range []string{"allow", "rocketclaw: {load_agents_md: false}", "rocketclaw: {rocketclaw_set_tag: deny, rocketclaw_get_tags: allow}", `rocketclaw:
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
					require.Equal(t, harness.PermissionDeny, action)
				}

				return
			}
			// Match the live sudo agent's frontmatter and preserve its other rules.
			require.Equal(t, [][]string{{"red", "yellow", "green"}}, groups)

			for _, bucket := range []string{"webfetch", "websearch", "glob", "grep", "read", "edit", "skill"} {
				action, matched := agent.Permission.Evaluate(bucket, "*")
				require.True(t, matched)
				require.Equal(t, harness.PermissionAllow, action)
			}

			for _, tc := range []struct {
				bucket, subject string
				want            harness.PermissionAction
			}{
				{"bash", "echo ok", harness.PermissionAuto},
				{"task", "helper", harness.PermissionAllow},
				{"task", "main", harness.PermissionDeny},
				{"task", "cron", harness.PermissionDeny},
				{"mcp", "context7.query", harness.PermissionAllow},
				{"rocketclaw_tags", setTagToolName, harness.PermissionAllow},
				{"rocketclaw_tags", getTagsToolName, harness.PermissionAllow},
			} {
				action, matched := agent.Permission.Evaluate(tc.bucket, tc.subject)
				require.True(t, matched)
				require.Equal(t, tc.want, action)
			}
		})
	}
}

// rocketclaw_sessions_recipient loads as a boolean or a non-empty list of unique agent names. Any other
// value fails the load naming the file and key, except in a shadow definition, which is dropped.
func TestSessionRecipientDefinitions(t *testing.T) {
	t.Parallel()

	for value, valid := range map[string]bool{"true": true, "false": true, "[main]": true, "[main, researcher]": true, "allow": false, "'true'": false, "null": false, "[]": false, "['']": false, "[main, main]": false, "[12]": false, "[[main]]": false} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			definition := "---\nmodel: gpt-5.4\npermission:\n  rocketclaw:\n    rocketclaw_sessions_recipient: " + value + "\n---\nPrompt\n"
			writeMainAgentSkills(t, workspace, definition)
			root := openWorkspaceRoot(t, workspace)

			agents, _, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent)
			if valid {
				require.NoError(t, err)
				requireNoRocketClawPermissionMatch(t, agents.Items["main"].Permission, recipientSetting)
			} else {
				require.ErrorContains(t, err, "main.md: permission.rocketclaw."+recipientSetting)
				writeMainAgentSkills(t, workspace, "---\nmodel: gpt-5.4\n---\nPrompt\n")
			}

			require.NoError(t, root.MkdirAll(".rocketclaw/agents/main", 0o755))
			require.NoError(t, root.WriteFile(".rocketclaw/agents/main/a.md", []byte(definition), 0o644))

			agents, _, err = loadRocketCodeDefinitions(root, workspace, toolModePersistent)
			require.NoError(t, err)
			require.Contains(t, agents.Items, "main")
			_, shadow := agents.Items["main/a"]
			require.Equal(t, valid, shadow, "an invalid shadow definition is dropped")
		})
	}
}

func loadRocketCodeDefinitions(root *os.Root, workspace string, mode toolMode, models ...map[string]string) (harness.Agents, harness.Skills, error) {
	cfg := &config.Config{Workspace: workspace}
	if len(models) > 0 {
		cfg.Models = models[0]
	}

	return loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(cfg), config.DefaultRuntimeDir, mode)
}

func TestLoadRocketCodeDefinitionsKeepsBackgroundExact(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "background", "---\nmodel: gpt-5.4\npermission:\n  rocketclaw:\n    allow_background: allow\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	for _, mode := range []toolMode{toolModePersistent, toolModeWorkflow} {
		agents, _, err := loadRocketCodeDefinitions(root, workspace, mode)
		require.NoError(t, err)

		action, _ := agents.Items["background"].Permission.Evaluate("rocketclaw", "allow_background")
		require.Equal(t, harness.PermissionAllow, action)
	}

	writeAgent(t, workspace, "background", "---\nmodel: gpt-5.4\npermission:\n  rocketclaw:\n    allow_background: auto\n---\nPrompt\n")

	_, _, err = loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.ErrorContains(t, err, "allow_background must be allow or deny")
}

// Only an exact rocketclaw_sessions rule offers the session tools; wildcards, the scalar form, the
// removed children scope, and session.spawn_mode fail to load.
func TestLoadRocketCodeDefinitionsSessions(t *testing.T) {
	workspace := t.TempDir()
	cases := []struct {
		name, rule string
		want       harness.PermissionAction
	}{
		{"missing", "", harness.PermissionDeny},
		{"deny", "rocketclaw_sessions: deny", harness.PermissionDeny},
		{"auto", "rocketclaw_sessions: auto", harness.PermissionAuto},
		{"reviewed", "rocketclaw_sessions: auto(guardian)", harness.PermissionAuto},
		{"allow", "rocketclaw_sessions: allow", harness.PermissionAllow},
	}

	for _, tc := range cases {
		writeAgent(t, workspace, tc.name, "---\nmodel: gpt-5.4\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[red]]\n    "+tc.rule+"\n---\nPrompt\n")
	}

	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	agents, _, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.NoError(t, err)

	for _, tc := range cases {
		action, _ := agents.Items[tc.name].Permission.Evaluate("rocketclaw", sessionsSubject)
		require.Equal(t, tc.want, action, tc.name)
	}

	for permission, message := range map[string]string{
		"rocketclaw: allow":                                 "name each rocketclaw tool",
		"rocketclaw: {'*': allow}":                          "name each rocketclaw tool",
		"rocketclaw: {rocketclaw_sessions_*: allow}":        "name each rocketclaw tool",
		"rocketclaw: {rocketclaw_sessions_write: children}": `pattern "rocketclaw_sessions_write": unknown permission action "children"`,
		"rocketclaw: {session: {spawn_mode: all}}":          "session.spawn_mode is removed; use rocketclaw_sessions",
	} {
		writeAgent(t, workspace, "bad", "---\nmodel: gpt-5.4\npermission:\n  "+permission+"\n---\nPrompt\n")

		_, _, err = loadRocketCodeDefinitions(root, workspace, toolModePersistent)
		require.ErrorContains(t, err, `bad.md: parse permission: permission "rocketclaw": `+message)
	}
}

func TestLoadRocketCodeDefinitionsPreparesPersistentAgents(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "assistant", "---\ndescription: Main\nmodel: gpt-5.4\nreasoningEffort: high\nverbosity: low\npermission:\n  bash:\n    \"gh *\": allow\n  rocketclaw:\n    code_mode_approve: auto(release-reviewer)\n---\nPrompt\n")
	writeAgent(t, workspace, "restricted", "---\ndescription: Restricted\nmodel: gpt-5.4\npermission:\n  task:\n    \"go-reviewer\": allow\n---\nPrompt\n")
	writeAgent(t, workspace, "helper", "---\ndescription: Helper\nmodel: gpt-6.1-sol\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll(filepath.Join(config.DefaultRuntimeDir, "agents", "assistant"), 0o700))
	require.NoError(t, root.WriteFile(filepath.Join(config.DefaultRuntimeDir, "agents", "assistant", "alternative.md"), []byte("---\nmodel: gpt-6.1-sol\n---\nAlternative"), 0o600))

	agents, _, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.NoError(t, err)
	require.Contains(t, agents.Items, "assistant/alternative")

	primary := agents.Items["assistant"]
	helper := agents.Items["helper"]
	restricted := agents.Items["restricted"]

	require.Equal(t, "gpt-5.4", primary.Model)
	require.Equal(t, "gpt-6.1-sol", helper.Model)
	require.True(t, permissionSetAllows(primary.Permission, "bash", "gh *"))
	require.Equal(t, harness.PermissionRule{Pattern: "code_mode_approve", Action: harness.PermissionAuto, Reviewer: "release-reviewer"}, primary.Permission.Buckets[1].Rules[0])
	require.False(t, permissionSetAllows(primary.Permission, "task", "*"))
	require.False(t, permissionSetAllows(helper.Permission, "task", "*"))
	require.True(t, permissionSetAllows(restricted.Permission, "task", "go-reviewer"))
	require.False(t, permissionSetAllows(restricted.Permission, "task", "*"))
	requireNoRocketClawPermissionMatch(t, primary.Permission, restartToolName)
	requireRocketClawPermissionAction(t, primary.Permission, reloadToolName, harness.PermissionAllow)
	requireRocketClawPermissionAction(t, primary.Permission, scheduleMessageToolName, harness.PermissionAllow)
	requireRocketClawPermissionAction(t, primary.Permission, resetScheduledMessagesToolName, harness.PermissionAllow)
	requireRocketClawPermissionAction(t, primary.Permission, attachFilesToolName, harness.PermissionAllow)
	requireRocketClawPermissionAction(t, primary.Permission, updateGoalToolName, harness.PermissionAllow)
	requireNoRocketClawPermissionMatch(t, helper.Permission, restartToolName)
	requireRocketClawPermissionAction(t, helper.Permission, reloadToolName, harness.PermissionAllow)
	requireRocketClawPermissionAction(t, helper.Permission, scheduleMessageToolName, harness.PermissionAllow)
	requireRocketClawPermissionAction(t, helper.Permission, resetScheduledMessagesToolName, harness.PermissionAllow)
	requireRocketClawPermissionAction(t, helper.Permission, attachFilesToolName, harness.PermissionAllow)
	requireRocketClawPermissionAction(t, helper.Permission, updateGoalToolName, harness.PermissionAllow)

	for _, mode := range []toolMode{toolModePersistent, toolModeWorkflow} {
		prepared, _, err := loadRocketCodeDefinitions(root, workspace, mode)
		require.NoError(t, err)

		for _, agent := range prepared.Items {
			requireNoRocketClawPermissionMatch(t, agent.Permission, sessionsSubject)
		}
	}

	externalAgents, err := ExternalMCPAgentsIn(config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir)
	require.NoError(t, err)
	require.Equal(t, []string{"assistant", "helper", "restricted"}, externalAgents)
}

func TestLoadRocketCodeDefinitionsResolvesModelTemplate(t *testing.T) {
	t.Parallel()

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

func TestLoadRocketCodeDefinitionsReportsInvalidTypedFrontmatter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "max recursion", line: "maxRecursion: nope", want: "main.md: parse maxRecursion:"},
		{name: "shell", line: "shell: sandbox/enter", want: "main.md: shell: must be an absolute path or emulated"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.4\n"+tt.line+"\n---\nPrompt\n")
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

			root, err := os.OpenRoot(workspace)
			require.NoError(t, err)

			defer func() { require.NoError(t, root.Close()) }()

			_, _, err = loadRocketCodeDefinitions(root, workspace, toolModePersistent)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestLoadRocketCodeDefinitionsReportsMissingModel(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw-stage", "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw-stage", "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".rocketclaw-stage", "agents", "main.md"), []byte("---\ndescription: Main\n---\nPrompt\n"), 0o644))

	_, _, err := LoadRuntimeDefinitions(config.NewLockedConfig(&config.Config{Workspace: workspace}), ".rocketclaw-stage")
	require.ErrorContains(t, err, "main.md: model: required non-empty string")
}

func TestSessionTagInvalidReloadKeepsLiveDefinitions(t *testing.T) {
	t.Parallel()

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
	locked := config.NewLockedConfig(cfg)
	before, _, err := LoadRuntimeDefinitions(locked, cfg.RuntimeDirName())
	require.NoError(t, err)
	err = skel.ReplaceRuntimeAssetsAfterValidation(workspace, cfg.RuntimeDirName(), new(cfg.Behavior()), slog.New(slog.DiscardHandler), locked, func(runtimeDir string) error {
		_, _, err := LoadRuntimeDefinitions(locked, runtimeDir)
		return err
	}, func() error { return nil })
	require.ErrorContains(t, err, "main.md: permission.rocketclaw.rocketclaw_set_tag")
	after, _, err := LoadRuntimeDefinitions(locked, cfg.RuntimeDirName())
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestLoadRuntimeDefinitionsDuringReload(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll("agents", 0o755))
	require.NoError(t, root.WriteFile("agents/main.md", []byte("---\nmodel: test\n---\nPrompt\n"), 0o644))
	require.NoError(t, root.MkdirAll("skills/example", 0o755))
	require.NoError(t, root.WriteFile("skills/example/SKILL.md", []byte("---\nname: example\ndescription: Example\n---\nInstructions\n"), 0o644))

	cfg := &config.Config{Workspace: workspace}
	locked := config.NewLockedConfig(cfg)
	logger := slog.New(slog.DiscardHandler)
	require.NoError(t, skel.SyncInWithOverlays(workspace, cfg.RuntimeDirName(), nil, logger))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		defer cancel()

		for range 20 {
			if err := skel.ReplaceRuntimeAssetsAfterValidation(workspace, cfg.RuntimeDirName(), new(cfg.Behavior()), logger, locked, func(runtimeDir string) error {
				_, _, err := LoadRuntimeDefinitions(locked, runtimeDir)
				return err
			}, func() error { return nil }); err != nil {
				return fmt.Errorf("reload runtime assets: %w", err)
			}
		}

		return nil
	})
	group.Go(func() error {
		for ctx.Err() == nil {
			agents, skills, err := LoadRuntimeDefinitions(locked, cfg.RuntimeDirName())
			if err != nil {
				return err
			}

			if agents.Items["main"].Prompt != "Prompt" || skills.Items["example"].Description != "Example" {
				return fmt.Errorf("reload exposed incomplete definitions: main=%+v example=%+v", agents.Items["main"], skills.Items["example"])
			}
		}

		return nil
	})
	require.NoError(t, group.Wait())

	agents, skills, err := LoadRuntimeDefinitions(locked, cfg.RuntimeDirName())
	require.NoError(t, err)
	require.Equal(t, "Prompt", agents.Items["main"].Prompt)
	require.Equal(t, "Example", skills.Items["example"].Description)
}

func TestLoadRuntimeDefinitionsUsesLoadedModels(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	stage := ".rocketclaw-stage"
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, stage, "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, stage, "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, stage, "agents", "main.md"), []byte("---\ndescription: Main\nmodel: '{{ model \"loaded\" }}'\n---\nPrompt\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "rocketclaw.json"), []byte(`{"models":{"disk":"gpt-6.1-sol"}}`), 0o600))

	_, _, err := LoadRuntimeDefinitions(config.NewLockedConfig(&config.Config{Workspace: workspace, Models: map[string]string{"loaded": "gpt-6.1-sol"}}), stage)
	require.NoError(t, err)
}

func TestLoadRocketCodeDefinitionsPreparesRocketClawRuntimeToolPermissions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mode           toolMode
		permission     string
		wantTool       string
		wantAction     harness.PermissionAction
		wantAllowTools []string
	}{
		{
			name:           "exact persistent restart allow",
			mode:           toolModePersistent,
			permission:     "permission:\n  rocketclaw:\n    rocketclaw_restart: allow\n",
			wantTool:       restartToolName,
			wantAction:     harness.PermissionAllow,
			wantAllowTools: []string{reloadToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
		{
			name:           "exact persistent restart auto",
			mode:           toolModePersistent,
			permission:     "permission:\n  rocketclaw:\n    rocketclaw_restart: auto\n",
			wantTool:       restartToolName,
			wantAction:     harness.PermissionAuto,
			wantAllowTools: []string{reloadToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
		{
			name:           "exact persistent restart deny",
			mode:           toolModePersistent,
			permission:     "permission:\n  rocketclaw:\n    rocketclaw_restart: deny\n",
			wantTool:       restartToolName,
			wantAction:     harness.PermissionDeny,
			wantAllowTools: []string{reloadToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
		{
			name:           "exact deny keeps other defaults",
			mode:           toolModePersistent,
			permission:     "permission:\n  rocketclaw:\n    rocketclaw_reload: deny\n",
			wantTool:       reloadToolName,
			wantAction:     harness.PermissionDeny,
			wantAllowTools: []string{scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
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
				requireRocketClawPermissionAction(t, agents.Items["main"].Permission, tool, harness.PermissionAllow)
			}
		})
	}
}

func TestLoadRocketCodeDefinitionsLoadsStructuredSkillMetadata(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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

			agents, skills, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), runtimeDir, toolModePersistent)
			require.NoError(t, err)

			client := openai.NewClient()
			runtime, err := harness.NewWithProviders(harness.Providers{OpenAI: &client}, &harness.Config{ShellTempDir: workspace, RetainedResultDir: "retained", ChildSessions: harness.InertChildSessions{}, Journal: harness.InertJournal{}, ShellCommand: harness.DefaultShellCommand{}}, root, agents, skills, "main", nil)
			require.NoError(t, err)

			action, _ := runtime.Permissions.Evaluate("read", filepath.ToSlash(filepath.Join(skillDir, "asset.txt")))
			require.Equal(t, harness.PermissionAllow, action)
		})
	}
}

func TestRocketCodeInterpolatesPermissionPatternsFromShellEnv(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.MkdirAll(filepath.Join(".rocketclaw", "agents"), 0o755))
	require.NoError(t, root.WriteFile(filepath.Join(".rocketclaw", "agents", "main.md"), []byte("---\ndescription: Main\nmodel: gpt-5.4\npermission:\n  edit:\n    \".tmp/*\": deny\n    \".tmp/${ROCKETCLAW_METADATA_FRUIT}/note.txt\": allow\n---\nPrompt\n"), 0o644))
	require.NoError(t, root.MkdirAll(filepath.Join(".rocketclaw", "skills"), 0o755))

	agents, skills, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir, toolModePersistent)
	require.NoError(t, err)
	require.Equal(t, ".tmp/*", agents.Items["main"].Permission.Buckets[0].Rules[0].Pattern)
	require.Equal(t, ".tmp/${ROCKETCLAW_METADATA_FRUIT}/note.txt", agents.Items["main"].Permission.Buckets[0].Rules[1].Pattern)

	client := openai.NewClient()
	runtime, err := harness.NewWithProviders(harness.Providers{OpenAI: &client}, &harness.Config{ShellTempDir: workspace, RetainedResultDir: "retained", ChildSessions: harness.InertChildSessions{}, Journal: harness.InertJournal{}, ShellCommand: harness.DefaultShellCommand{}, ShellEnv: map[string]string{"ROCKETCLAW_METADATA_FRUIT": "banana"}}, root, agents, skills, "main", nil)
	require.NoError(t, err)

	action, _ := runtime.Permissions.Evaluate("edit", ".tmp/banana/note.txt")
	require.Equal(t, harness.PermissionAllow, action)
	action, _ = runtime.Permissions.Evaluate("edit", ".tmp/apple/note.txt")
	require.Equal(t, harness.PermissionDeny, action)
	require.Contains(t, runtime.SystemPrompt, ".tmp/banana/note.txt")
}

func TestLoadRocketCodeDefinitionsRejectsEscapingAgentSymlink(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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

func requireRocketClawPermissionAction(t *testing.T, set harness.PermissionSet, subject string, want harness.PermissionAction) {
	t.Helper()

	action, matched := set.Evaluate("rocketclaw", subject)
	require.True(t, matched)
	require.Equal(t, want, action)
}

func requireNoRocketClawPermissionMatch(t *testing.T, set harness.PermissionSet, subject string) {
	t.Helper()

	_, matched := set.Evaluate("rocketclaw", subject)
	require.False(t, matched)
}

func permissionSetAllows(set harness.PermissionSet, bucket, pattern string) bool {
	for _, candidate := range set.Buckets {
		if candidate.Name != bucket {
			continue
		}

		for _, rule := range candidate.Rules {
			if rule.Pattern == pattern && rule.Action == harness.PermissionAllow {
				return true
			}
		}
	}

	return false
}
