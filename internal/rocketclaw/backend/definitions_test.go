package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestSessionTagDefinitions(t *testing.T) {
	t.Parallel()

	for _, tags := range []string{"", "[]", "[[triage, investigating, resolved], [customer, internal]]", "[['*', '?', A, a]]", "[[]]", "[['']]", "[[same], [same]]", "[[12]]", "null", "example"} {
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

			require.NoError(t, root.WriteFile(".rocketclaw/agents/primary.md", []byte("---\nmodel: gpt-5.4\npermission:\n  rocketclaw:\n    rocketclaw_get_tags: allow\n    rocketclaw_sessions: allow\n"+frontmatter+"---\nPrompt\n"), 0o644))

			if tags == "example" {
				data, err := os.ReadFile("../skel/.rocketclaw/skills/main-create-or-update-agent/session-tags.example.md")
				require.NoError(t, err)
				require.NoError(t, root.WriteFile(".rocketclaw/agents/primary.md", data, 0o644))
			}

			agents, _, warnings, err := readRocketCodeDefinitionsIn(root, &config.Config{Workspace: workspace}, config.DefaultRuntimeDir, nil, false)
			require.NoError(t, err)

			valid := tags == "" || tags == "[]" || tags == "example" || strings.HasPrefix(tags, "[[triage") || strings.HasPrefix(tags, "[['*'") || tags == "[[12]]"
			if !valid {
				require.NotContains(t, agents.Items, "primary")
				require.Len(t, warnings, 1)
				require.Regexp(t, `^Agent primary was not loaded: permission\.rocketclaw\.rocketclaw_set_tag: `, warnings[0])

				return
			}

			require.Empty(t, warnings)

			agent := agents.Items["primary"]
			for _, name := range []string{"rocketclaw_set_tag", "rocketclaw_get_tags"} {
				action, matched := agent.Permission.Evaluate("rocketclaw_tags", name)
				require.True(t, matched)

				if tags == "" || tags == "[]" {
					require.Equal(t, harness.PermissionDeny, action)
					require.NotContains(t, agent.Prompt, name)
				} else {
					require.Equal(t, harness.PermissionAllow, action)
					require.Contains(t, agent.Prompt, name)
					require.Contains(t, agent.Prompt, "exclusive")
					require.Contains(t, agent.Prompt, "toggle")
				}

				_, matched = agent.Permission.Evaluate("rocketclaw", name)
				require.False(t, matched)
			}

			require.Equal(t, tags != "example", permissionSetAllows(agent.Permission, "rocketclaw", sessionsSubject))
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

	agents, _, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir, nil)
	require.NoError(t, err)

	tagged, untagged := agents.Items["tagged"], agents.Items["untagged"]

	for subject, want := range map[string]harness.PermissionAction{
		reloadToolName: harness.PermissionAllow, scheduleMessageToolName: harness.PermissionAllow, attachFilesToolName: harness.PermissionAllow,
		updateGoalToolName: harness.PermissionAllow, askUserQuestionToolName: harness.PermissionAllow, navigateToolName: harness.PermissionAllow, respondInThreadToolName: harness.PermissionAllow, noReplyToolName: harness.PermissionAllow,
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

func TestLoadRocketCodeDefinitionsEasy(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	root := openWorkspaceRoot(t, workspace)
	easy := "---\nmode: easy\nmodel: gpt-6.1-sol\nspace: Ops\nworkingDirectory: work\n"

	for name, content := range map[string]string{
		".rocketclaw/agents/open.md":                     easy + "tags: [[red, green]]\n---\nPrompt\n",
		".rocketclaw/agents/none.md":                     easy + "rocketclaw: []\nmcp: {}\nskills: [marketing]\napprover: {skills: [support]}\n---\nPrompt\n",
		".rocketclaw/agents/reviewed.md":                 easy + "approver: {rocketclaw: [tags], tags: [[red, green]]}\n---\nPrompt\n",
		".rocketclaw/agents/grouped.md":                  easy + "mcp: {s: [read]}\n---\nPrompt\n",
		".rocketclaw/agents/advanced.md":                 "---\nmodel: gpt-6.1-sol\n---\nPrompt\n",
		".rocketclaw/agents/advanced/easy.md":            easy + "---\nPrompt\n",
		".rocketclaw/skills/marketing/seo/SKILL.md":      "---\nname: seo\ndescription: SEO\n---\n",
		".rocketclaw/skills/support/SKILL.md":            "---\nname: support\ndescription: Support\n---\n",
		".rocketclaw/skills/marketing/team/ads/SKILL.md": "---\nname: ads\ndescription: Ads\n---\n",
	} {
		require.NoError(t, root.MkdirAll(filepath.Dir(name), 0o755))
		require.NoError(t, root.WriteFile(name, []byte(content), 0o644))
	}

	cfg := &config.Config{Workspace: workspace, MCPServers: map[string]config.MCPServerConfig{"s": {Command: "s", Tools: map[string][]string{"read": {"list_*"}, "write": {"update_*"}}}}}
	agents, skills, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(cfg), config.DefaultRuntimeDir, nil)
	require.NoError(t, err)

	open, none, grouped := agents.Items["open"], agents.Items["none"], agents.Items["grouped"]
	require.Contains(t, agents.Items, "advanced/easy", "an easy shadow follows an advanced parent")

	for _, subject := range []string{reloadToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName, askUserQuestionToolName, navigateToolName, respondInThreadToolName, noReplyToolName} {
		requireRocketClawPermissionAction(t, open.Permission, subject, harness.PermissionAllow)
		requireRocketClawPermissionAction(t, none.Permission, subject, harness.PermissionAllow)
	}

	for _, subject := range []string{restartToolName, sessionsSubject, "rocketclaw_store"} {
		requireRocketClawPermissionAction(t, open.Permission, subject, harness.PermissionAllow)

		action, _ := none.Permission.Evaluate("rocketclaw", subject)
		require.Equal(t, harness.PermissionDeny, action, subject)
	}

	require.True(t, open.Permission.Enabled("rocketclaw", restartToolName))
	require.False(t, none.Permission.Enabled("rocketclaw", restartToolName))

	for _, name := range []string{setTagToolName, getTagsToolName} {
		action, _ := open.Permission.Evaluate("rocketclaw_tags", name)
		require.Equal(t, harness.PermissionAllow, action, name)
		require.Contains(t, open.Prompt, name)

		action, _ = none.Permission.Evaluate("rocketclaw_tags", name)
		require.Equal(t, harness.PermissionDeny, action, name)
	}

	noneSkills := skills.Available(&none)

	available := make([]string, 0, len(noneSkills))
	for _, skill := range noneSkills {
		available = append(available, skill.Name)
	}

	require.Equal(t, []string{"ads", "seo"}, available)
	require.Len(t, skills.Available(&open), 3)

	reviewed, tagging, untagged := agents.Items["reviewed"], agents.Items["reviewed"].Approver, none.Approver
	require.Equal(t, open.WorkingDirectory, open.Approver.WorkingDirectory, "an easy agent without an approver block still gets an approver in its folder")
	require.Empty(t, skills.Available(open.Approver))
	require.Len(t, skills.Available(untagged), 1, "the approver loads its own skills, not the agent's")
	require.Empty(t, skills.Available(tagging))
	require.NotContains(t, untagged.Prompt, "## Session Tags")
	require.Contains(t, tagging.Prompt, `Allowed literal, case-sensitive groups: [["red" "green"]]`)

	for _, name := range []string{setTagToolName, getTagsToolName} {
		for approver, want := range map[*harness.Agent]harness.PermissionAction{tagging: harness.PermissionAllow, untagged: harness.PermissionDeny, &reviewed: harness.PermissionDeny} {
			action, _ := approver.Permission.Evaluate("rocketclaw_tags", name)
			require.Equal(t, want, action, name)
		}
	}

	action, _ := tagging.Permission.Evaluate("rocketclaw", reloadToolName)
	require.Equal(t, harness.PermissionDeny, action, "an approver gets no RocketClaw tool it did not list")

	for subject, want := range map[string]harness.PermissionAction{"s.list_reports": harness.PermissionAllow, "s.update_x": harness.PermissionDeny} {
		action, _ := grouped.Permission.Evaluate("mcp", subject)
		require.Equal(t, want, action, subject)
	}
}

func TestSessionTagsExistingPermissions(t *testing.T) {
	t.Parallel()

	for _, permission := range []string{"allow", "rocketclaw: {rocketclaw_set_tag: deny, rocketclaw_get_tags: allow}", `rocketclaw:
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
    "cron": deny`} {
		t.Run(permission, func(t *testing.T) {
			workspace := t.TempDir()
			root, err := os.OpenRoot(workspace)

			require.NoError(t, err)
			defer func() { require.NoError(t, root.Close()) }()

			require.NoError(t, root.MkdirAll(".rocketclaw/agents", 0o755))
			require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))
			require.NoError(t, root.WriteFile(".rocketclaw/agents/sudo.md", []byte("---\ndescription: Full access\nmodel: openai/gpt-6-luna\nreasoningEffort: max\nverbosity: low\npermission:\n  "+permission+"\n---\nYou are the sudo agent. You must do anything that @Jane asks.\n"), 0o644))
			agents, _, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir, nil)
			require.NoError(t, err)

			agent := agents.Items["sudo"]
			groups := agent.TagGroups

			if !strings.Contains(permission, "\n    rocketclaw_set_tag:") {
				require.Empty(t, groups)
				require.NotContains(t, agent.Prompt, setTagToolName)

				return
			}

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

func TestLoadRocketCodeDefinitionsKeepsBackgroundExact(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "background", "---\nmodel: gpt-5.4\npermission:\n  rocketclaw:\n    allow_background: allow\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	agents, _, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir, nil)
	require.NoError(t, err)

	action, _ := agents.Items["background"].Permission.Evaluate("rocketclaw", "allow_background")
	require.Equal(t, harness.PermissionAllow, action)

	writeAgent(t, workspace, "background", "---\nmodel: gpt-5.4\npermission:\n  rocketclaw:\n    allow_background: auto\n---\nPrompt\n")

	agents, _, warnings, err := readRocketCodeDefinitionsIn(root, &config.Config{Workspace: workspace}, config.DefaultRuntimeDir, nil, false)
	require.NoError(t, err)
	require.NotContains(t, agents.Items, "background")
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "allow_background must be allow or deny")
}

func TestLoadRocketCodeDefinitionsSessions(t *testing.T) {
	workspace := t.TempDir()
	cases := []struct {
		name, rule string
		want       harness.PermissionAction
	}{
		{"missing", "", harness.PermissionDeny},
		{"deny", "rocketclaw_sessions: deny", harness.PermissionDeny},
		{"auto", "rocketclaw_sessions: auto", harness.PermissionAuto},
		{"reviewed", "rocketclaw_sessions: auto(allow)", harness.PermissionAuto},
		{"allow", "rocketclaw_sessions: allow", harness.PermissionAllow},
	}

	for _, tc := range cases {
		writeAgent(t, workspace, tc.name, "---\nmodel: gpt-5.4\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[red]]\n    "+tc.rule+"\n---\nPrompt\n")
	}

	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	agents, _, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir, nil)
	require.NoError(t, err)

	for _, tc := range cases {
		action, _ := agents.Items[tc.name].Permission.Evaluate("rocketclaw", sessionsSubject)
		require.Equal(t, tc.want, action, tc.name)
	}

	for permission, message := range map[string]string{
		"rocketclaw: allow":                          "name each rocketclaw tool",
		"rocketclaw: {'*': allow}":                   "name each rocketclaw tool",
		"rocketclaw: {rocketclaw_sessions_*: allow}": "name each rocketclaw tool",
	} {
		writeAgent(t, workspace, "bad", "---\nmodel: gpt-5.4\npermission:\n  "+permission+"\n---\nPrompt\n")

		_, _, warnings, err := readRocketCodeDefinitionsIn(root, &config.Config{Workspace: workspace}, config.DefaultRuntimeDir, nil, false)
		require.NoError(t, err)
		require.Len(t, warnings, 1)
		require.Contains(t, warnings[0], `Agent bad was not loaded: parse permission: permission "rocketclaw": `+message)
	}
}

func TestLoadRocketCodeDefinitionsPreparesPersistentAgents(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "assistant", "---\ndescription: Main\nmodel: gpt-5.4\nreasoningEffort: high\nverbosity: low\npermission:\n  bash:\n    \"gh *\": allow\n  rocketclaw:\n    code_mode_approve: auto(release-reviewer)\n---\nPrompt\n")
	writeAgent(t, workspace, "restricted", "---\ndescription: Restricted\nmodel: gpt-5.4\npermission:\n  task:\n    \"go-reviewer\": allow\n---\nPrompt\n")
	writeAgent(t, workspace, "helper", "---\ndescription: Helper\nmodel: gpt-6.1-sol\nsticky: off\n---\nPrompt\n")
	writeAgent(t, workspace, "release-reviewer", "---\nmodel: gpt-6.1-sol\n---\nReview releases.\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll(filepath.Join(config.DefaultRuntimeDir, "agents", "assistant"), 0o700))
	require.NoError(t, root.WriteFile(filepath.Join(config.DefaultRuntimeDir, "agents", "assistant", "alternative.md"), []byte("---\nmodel: gpt-6.1-sol\n---\nAlternative"), 0o600))

	agents, _, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir, nil)
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

	prepared, _, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace, EmbeddedAgents: config.EmbeddedAgentsConfig{Main: config.EmbeddedAgentConfig{Disabled: true}}}), config.DefaultRuntimeDir, nil)
	require.NoError(t, err)

	for _, agent := range prepared.Items {
		requireNoRocketClawPermissionMatch(t, agent.Permission, sessionsSubject)
	}

	writeAgent(t, workspace, "personal", "---\nmodel: gpt-6.1-sol\nsticky: user\n---\nPrompt\n")
	writeAgent(t, workspace, "shared", "---\nmodel: gpt-6.1-sol\nsticky: global\n---\nPrompt\n")
	writeAgent(t, workspace, "channel", "---\nmodel: gpt-6.1-sol\nsticky: slack\n---\nPrompt\n")
	t.Run("MCP offers only non-sticky agents", func(t *testing.T) {
		externalAgents, err := ExternalMCPAgentsIn(config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir)
		require.NoError(t, err)
		require.Equal(t, []string{"assistant", "helper", "release-reviewer", "restricted"}, externalAgents)
	})

	for _, name := range []string{"channel", "helper", "personal", "shared"} {
		t.Run("Slack "+name, func(t *testing.T) {
			cfg := &config.Config{Workspace: workspace, Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"channel", name}}}}}

			var logs bytes.Buffer

			warnings, err := validateRuntimeAssets(config.NewLockedConfig(cfg), config.DefaultRuntimeDir, slog.New(slog.NewTextHandler(&logs, nil)))
			require.NoError(t, err)

			if name == "channel" {
				require.Empty(t, warnings)
				require.Empty(t, logs.String())
			} else {
				require.Equal(t, "Slack channel #ops uses agent "+name+", which is not sticky: slack, so it does not answer mentions there.", warnings)
				require.Contains(t, logs.String(), `level=WARN msg="slack channel agent is not sticky: slack" channel=#ops agent=`+name+"\n")
			}
		})
	}
}

func TestAgentFileErrorsFailOnlyTheirAgent(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "helper", "---\nmodel: gpt-6.1-sol\nroutines: [1h]\n---\nPrompt\n")
	writeAgent(t, workspace, "phrase", "---\nmodel: gpt-6.1-sol\nroutines: [every 30min]\n---\nPrompt\n")
	writeAgent(t, workspace, "provider", "---\nmodel: nope/gpt-6\n---\nPrompt\n")
	writeAgent(t, workspace, "typo", "---\nmodel: gpt-6.1-sol\nsudu: yes\n---\nPrompt\n")
	writeAgent(t, workspace, "tagged", "---\nmodel: gpt-6.1-sol\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[same], [same]]\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	locked := config.NewLockedConfig(&config.Config{Workspace: workspace})

	warnings, err := validateRuntimeAssets(locked, config.DefaultRuntimeDir, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.Equal(t, `Agent phrase was not loaded: routines: schedule "every 30min": invalid cron expression: expected exactly 5 fields, found 2: [every 30min]. `+
		`Agent provider was not loaded: model "nope/gpt-6": provider "nope" is not configured. `+
		`Agent tagged was not loaded: permission.rocketclaw.rocketclaw_set_tag: empty or duplicate tag name "same". `+
		`Agent typo was not loaded: sudu: not an advanced-mode key.`, warnings)

	agents, err := LoadRuntimeAgents(locked, config.DefaultRuntimeDir)
	require.NoError(t, err)
	require.Equal(t, []string{"helper", "main"}, agents.PrimaryNames())
}

func TestLoadRuntimeDefinitionsDuringReload(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll("agents", 0o755))
	require.NoError(t, root.WriteFile("agents/primary.md", []byte("---\nmodel: test\n---\nPrompt\n"), 0o644))
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

			if agents.Items["primary"].Prompt != "Prompt" || skills.Items["example"].Description != "Example" {
				return fmt.Errorf("reload exposed incomplete definitions: primary=%+v example=%+v", agents.Items["primary"], skills.Items["example"])
			}
		}

		return nil
	})
	require.NoError(t, group.Wait())

	agents, skills, err := LoadRuntimeDefinitions(locked, cfg.RuntimeDirName())
	require.NoError(t, err)
	require.Equal(t, "Prompt", agents.Items["primary"].Prompt)
	require.Equal(t, "Example", skills.Items["example"].Description)
}

func TestLoadRocketCodeDefinitionsPreparesRocketClawRuntimeToolPermissions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		permission     string
		wantTool       string
		wantAction     harness.PermissionAction
		wantAllowTools []string
	}{
		{
			name:           "exact persistent restart allow",
			permission:     "permission:\n  rocketclaw:\n    rocketclaw_restart: allow\n",
			wantTool:       restartToolName,
			wantAction:     harness.PermissionAllow,
			wantAllowTools: []string{reloadToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
		{
			name:           "exact persistent restart auto",
			permission:     "permission:\n  rocketclaw:\n    rocketclaw_restart: auto\n",
			wantTool:       restartToolName,
			wantAction:     harness.PermissionAuto,
			wantAllowTools: []string{reloadToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
		{
			name:           "exact persistent restart deny",
			permission:     "permission:\n  rocketclaw:\n    rocketclaw_restart: deny\n",
			wantTool:       restartToolName,
			wantAction:     harness.PermissionDeny,
			wantAllowTools: []string{reloadToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
		{
			name:           "exact deny keeps other defaults",
			permission:     "permission:\n  rocketclaw:\n    rocketclaw_reload: deny\n",
			wantTool:       reloadToolName,
			wantAction:     harness.PermissionDeny,
			wantAllowTools: []string{scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeAgent(t, workspace, "primary", "---\ndescription: Primary\nmodel: gpt-5.4\n"+tt.permission+"---\nPrompt\n")
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

			root, err := os.OpenRoot(workspace)
			require.NoError(t, err)

			defer func() { require.NoError(t, root.Close()) }()

			agents, _, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir, nil)
			require.NoError(t, err)

			requireRocketClawPermissionAction(t, agents.Items["primary"].Permission, tt.wantTool, tt.wantAction)

			for _, tool := range tt.wantAllowTools {
				requireRocketClawPermissionAction(t, agents.Items["primary"].Permission, tool, harness.PermissionAllow)
			}
		})
	}
}

func TestLoadRocketCodeDefinitionsLoadsStructuredSkillMetadata(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "primary", `---
description: Main
model: gpt-5.4
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

	agents, skills, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir, nil)
	require.NoError(t, err)
	require.Contains(t, agents.Items, "primary")

	skill := skills.Items["example"]
	require.Equal(t, "Structured metadata should load", skill.Description)
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
			require.NoError(t, root.WriteFile(filepath.Join(runtimeDir, "agents", "primary.md"), []byte("---\ndescription: Primary\nmodel: gpt-5.4\npermission:\n  skill:\n    example: allow\n---\nPrompt\n"), 0o644))
			skillDir := filepath.Join(runtimeDir, "skills", "example")
			require.NoError(t, root.MkdirAll(skillDir, 0o755))
			require.NoError(t, root.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: example\ndescription: Example\n---\n"), 0o644))
			require.NoError(t, root.WriteFile(filepath.Join(skillDir, "asset.txt"), []byte("asset"), 0o644))

			agents, skills, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), runtimeDir, nil)
			require.NoError(t, err)

			resolver := newModelResolver(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test-key", Auth: "api_key"}}, slog.New(slog.DiscardHandler))
			runtime, err := harness.NewWithModelResolver(resolver, &harness.Config{ShellTempDir: workspace, RetainedResultDir: "retained", ChildSessions: harness.InertChildSessions{}, Journal: harness.InertJournal{}}, root, agents, skills, "primary")
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
	require.NoError(t, root.WriteFile(filepath.Join(".rocketclaw", "agents", "primary.md"), []byte("---\ndescription: Primary\nmodel: gpt-5.4\npermission:\n  edit:\n    \".tmp/*\": deny\n    \".tmp/${ROCKETCLAW_METADATA_FRUIT}/note.txt\": allow\n---\nPrompt\n"), 0o644))
	require.NoError(t, root.MkdirAll(filepath.Join(".rocketclaw", "skills"), 0o755))

	agents, skills, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir, nil)
	require.NoError(t, err)
	require.Equal(t, ".tmp/*", agents.Items["primary"].Permission.Buckets[0].Rules[0].Pattern)
	require.Equal(t, ".tmp/${ROCKETCLAW_METADATA_FRUIT}/note.txt", agents.Items["primary"].Permission.Buckets[0].Rules[1].Pattern)

	resolver := newModelResolver(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test-key", Auth: "api_key"}}, slog.New(slog.DiscardHandler))
	runtime, err := harness.NewWithModelResolver(resolver, &harness.Config{ShellTempDir: workspace, RetainedResultDir: "retained", ChildSessions: harness.InertChildSessions{}, Journal: harness.InertJournal{}, ShellEnv: map[string]string{"ROCKETCLAW_METADATA_FRUIT": "banana"}}, root, agents, skills, "primary")
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
	outside := filepath.Join(t.TempDir(), "primary.md")
	require.NoError(t, os.WriteFile(outside, []byte("---\ndescription: Outside\n---\nOutside\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(workspace, ".rocketclaw", "agents", "primary.md")))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	agents, _, warnings, err := readRocketCodeDefinitionsIn(root, &config.Config{Workspace: workspace}, config.DefaultRuntimeDir, nil, false)
	require.NoError(t, err)
	require.NotContains(t, agents.Items, "primary")
	require.Equal(t, []string{"Agent primary was not loaded: read agent: openat primary.md: path escapes from parent."}, warnings)
}

func TestLoadRocketCodeDefinitionsRejectsEscapingSkillSymlink(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "primary", "---\ndescription: Primary\nmodel: gpt-5.4\n---\nPrompt\n")
	outside := filepath.Join(t.TempDir(), "SKILL.md")
	require.NoError(t, os.WriteFile(outside, []byte("---\nname: outside\ndescription: Outside\n---\nOutside\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills", "outside"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(workspace, ".rocketclaw", "skills", "outside", "SKILL.md")))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	_, skills, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir, nil)
	require.NoError(t, err)
	require.Empty(t, skills.Items)
}

func TestRuntimeAgentsUseBuiltinMain(t *testing.T) {
	t.Parallel()

	custom := "---\nmodel: gpt-5.4\nsticky: slack\n---\nCustom prompt\n"

	for _, tt := range []struct {
		name                  string
		embedded              config.EmbeddedAgentsConfig
		layer                 map[string]string
		wantMain              bool
		wantModel, wantEffort string
		wantWarnings          []string
	}{
		{name: "defaults", wantMain: true, wantModel: "gpt-6.1-sol", wantEffort: "high"},
		{name: "draft layer", layer: map[string]string{"main.md": custom, "main/alt.md": custom}, wantMain: true, wantModel: "gpt-6.1-sol", wantEffort: "high"},
		{name: "main off", embedded: config.EmbeddedAgentsConfig{Main: config.EmbeddedAgentConfig{Disabled: true}}},
		{name: "main model", embedded: config.EmbeddedAgentsConfig{Main: config.EmbeddedAgentConfig{Model: "p/m#low"}}, wantMain: true, wantModel: "p/m", wantEffort: "low"},
		{
			name: "main model on an unconfigured provider", embedded: config.EmbeddedAgentsConfig{Main: config.EmbeddedAgentConfig{Model: "gone/m#low"}}, wantMain: true, wantModel: "gpt-6.1-sol", wantEffort: "high",
			wantWarnings: []string{"Agent main uses its shipped model because its Behavior Settings model names provider gone, which is not configured."},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			root := openWorkspaceRoot(t, workspace)
			require.NoError(t, root.MkdirAll("agents/main", 0o755))
			require.NoError(t, root.WriteFile("agents/main.md", []byte(custom), 0o644))
			require.NoError(t, root.WriteFile("agents/main/alt.md", []byte(custom), 0o644))
			require.NoError(t, skel.SyncInWithOverlays(workspace, config.DefaultRuntimeDir, nil, slog.New(slog.DiscardHandler)))

			agents, _, warnings, err := readRocketCodeDefinitionsIn(root, &config.Config{Workspace: workspace, Providers: map[string]config.OpenAIConfig{"p": {}}, EmbeddedAgents: tt.embedded}, config.DefaultRuntimeDir, tt.layer, false)
			require.NoError(t, err)
			require.Equal(t, tt.wantWarnings, warnings)

			mainAgent, ok := agents.Items["main"]
			require.Equal(t, tt.wantMain, ok)
			require.NotContains(t, agents.Items, "main/alt")

			if tt.wantMain {
				require.Equal(t, harness.AgentStickyGlobal, mainAgent.Sticky)
				require.True(t, mainAgent.Permission.Sudo)
				require.NotContains(t, mainAgent.Prompt, "Custom prompt")
				require.Equal(t, tt.wantModel, mainAgent.Model)
				require.Equal(t, tt.wantEffort, mainAgent.ReasoningEffort)
			}
		})
	}
}

func TestNoAgentDelegatesToBuiltinAgents(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "helper", "---\ndescription: Helper\nmodel: gpt-6.1-sol\nsticky: off\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	locked := config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test-key", Auth: "api_key"}})
	runtimeConfig := harness.Config{ShellTempDir: workspace, RetainedResultDir: "retained", ChildSessions: harness.InertChildSessions{}, Journal: harness.InertJournal{}, BackgroundJobs: harness.InertBackgroundJobs{}}
	runtime := func(agent string) (*harness.Runtime, error) {
		root, agents, skills, resolver, err := prepareRocketCode(locked, agent)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })

		return harness.NewWithModelResolver(resolver, &runtimeConfig, root, agents, skills, agent)
	}

	mainRuntime, err := runtime("main")
	require.NoError(t, err)

	task := mainRuntime.Tools["task"]
	require.Contains(t, task.Definition.Description.Value, "- helper: Helper")
	require.NotContains(t, task.Definition.Description.Value, "- main:")

	subjects, err := task.Subjects(json.RawMessage(`{"agent":"main"}`))
	require.NoError(t, err)

	action, _ := mainRuntime.Permissions.Evaluate("task", subjects[0])
	require.Equal(t, harness.PermissionDeny, action)

	writeAgent(t, workspace, "reviewed", "---\nmodel: gpt-6.1-sol\npermission:\n  bash:\n    \"*\": auto(main)\n---\nPrompt\n")

	agents, err := LoadRuntimeAgents(locked, config.DefaultRuntimeDir)
	require.NoError(t, err)
	require.NotContains(t, agents.Items, "reviewed")
	require.Contains(t, agents.Items, "helper")

	warnings, err := validateRuntimeAssets(locked, config.DefaultRuntimeDir, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.Equal(t, "Agent reviewed was not loaded: permission \"bash\" rule \"*\" references sticky: global reviewer agent \"main\", which reviews only its own rules.", warnings)
}

func TestBuiltinAgentPromptsEndWithAbout(t *testing.T) {
	t.Parallel()

	tools := []string{reloadToolName, restartToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName, createGoalToolName, navigateToolName, stopBackgroundJobToolName,
		sessionsListToolName, sessionsGetToolName, sessionsCurrentIDToolName, sessionsWriteToolName, sessionsGetURLToolName, sessionsMoveToolName, setTagToolName, getTagsToolName}

	const (
		unknownNote  = "This build's revision is unknown, so these links point at the main branch and may not match the running code."
		modifiedNote = "This build has local changes, so these links may not match the running code."
	)

	for _, tt := range []struct {
		name, revision string
		modified       bool
		ref, note      string
	}{
		{name: "revision", revision: "abc123", ref: "abc123"},
		{name: "unknown", ref: "main", note: unknownNote},
		{name: "modified", revision: "abc123", modified: true, ref: "abc123", note: modifiedNote},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "agents"), 0o755))
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

			agents, err := LoadRuntimeAgents(config.NewLockedConfig(&config.Config{Workspace: workspace, Revision: tt.revision, RevisionModified: tt.modified}), config.DefaultRuntimeDir)
			require.NoError(t, err)

			prompt := agents.Items["main"].Prompt
			_, about, found := strings.Cut(prompt, "## About RocketClaw")
			require.True(t, found)
			require.Contains(t, about, "https://github.com/Rocketable/platform/blob/"+tt.ref+"/CONCEPTS.md")

			for _, note := range []string{unknownNote, modifiedNote} {
				require.Equal(t, note == tt.note, strings.HasSuffix(about, "\n\n"+note))
				require.Equal(t, note == tt.note, strings.Contains(about, note))
			}

			require.NotContains(t, about, "\n#", "nothing follows the About section")
			require.NotContains(t, prompt, "{revision}")
			require.Contains(t, prompt, "## The Web's RPC interface")

			for _, tool := range regexp.MustCompile(`rocketclaw_\w+`).FindAllString(prompt, -1) {
				require.Contains(t, tools, tool)
			}

			require.True(t, strings.HasPrefix(prompt, "You are the main RocketClaw agent."))
		})
	}
}

func writeAgent(t *testing.T, workspace, name, content string) {
	t.Helper()

	dir := filepath.Join(workspace, ".rocketclaw", "agents")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644))
}

func writePrimaryAgentSkills(t *testing.T, workspace, agentYAML string) {
	t.Helper()
	writeAgent(t, workspace, "primary", agentYAML)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
}

func openWorkspaceRoot(t *testing.T, workspace string) *os.Root {
	t.Helper()

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	return root
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
