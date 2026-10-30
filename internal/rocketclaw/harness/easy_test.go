package harness

import (
	"maps"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

const easyRequired = "mode: easy\nmodel: gpt-6.1-sol#high\nspace: Marketing\nworkingDirectory: sessions/$ROCKETCLAW_CONVERSATION_ID"

func loadEasyTestAgent(t *testing.T, frontmatter string) (Agent, []error) {
	t.Helper()

	result := LoadAgents(fstest.MapFS{"main.md": testMapFile("---\n" + frontmatter + "\n---\nBody\n")}, AgentLoadConfig{
		MCPToolGroups: map[string]map[string][]string{"s": {"read": {"list_*"}, "write": {"update_*"}}, "t": {"read": {"get_*"}}},
		Providers:     []string{"openai", "work"},
		Skills: fstest.MapFS{
			"marketing/seo/SKILL.md":      testMapFile("---\nname: seo\ndescription: SEO\n---\n"),
			"marketing/team/ads/SKILL.md": testMapFile("---\nname: ads\ndescription: Ads\n---\n"),
			"support/SKILL.md":            testMapFile("---\nname: support\ndescription: Support\n---\n"),
		},
	})

	return result.Agents.Items["main"], result.Errors
}

func TestLoadEasyAgentErrors(t *testing.T) {
	for _, tc := range []struct{ name, frontmatter, want string }{
		{"primary in advanced", "mode: primary\ndescription: Main\nmodel: gpt-5.4", "main.md: mode: must be easy or advanced"},
		{"subagent in easy", "mode: subagent\nmodel: gpt-5.4\nspace: Marketing\nworkingDirectory: work", "main.md: mode: must be easy or advanced"},
		{"misspelled key", easyRequired + "\nskils: [marketing]", "main.md: skils: not an easy-mode key"},
		{"missing model", "mode: easy\nspace: Marketing\nworkingDirectory: work", "main.md: model: required non-empty string"},
		{"missing space", "mode: easy\nmodel: gpt-5.4\nworkingDirectory: work", "main.md: space: required non-empty string"},
		{"missing workingDirectory", "mode: easy\nmodel: gpt-5.4\nspace: Marketing", "main.md: workingDirectory: required non-empty string"},
		{"description", easyRequired + "\ndescription: Main", "main.md: description: not an easy-mode key"},
		{"sudo", easyRequired + "\nsudo: yes", "main.md: sudo: not an easy-mode key"},
		{"shell", easyRequired + "\nshell: emulated", "main.md: shell: not an easy-mode key"},
		{"negative maxRecursion", easyRequired + "\nmaxRecursion: -1", "main.md: maxRecursion: must be an integer of 0 or more"},
		{"unknown subagent provider", easyRequired + "\nsubagents: [nope/gpt-6]", `main.md: model "nope/gpt-6": provider "nope" is not configured`},
		{"unknown approver provider", easyRequired + "\napprover: {model: nope/gpt-6}", `main.md: model "nope/gpt-6": provider "nope" is not configured`},
		{"misspelled approver key", easyRequired + "\napprover: {promt: rules}", "main.md: approver.promt: not an easy-mode key"},
		{"unknown rocketclaw group", easyRequired + "\nrocketclaw: [chat]", `main.md: rocketclaw: unknown group "chat"`},
		{"tags group without tags", easyRequired + "\nrocketclaw: [tags]", "main.md: tags: required by the tags group in rocketclaw, and only with it"},
		{"tags without tags group", easyRequired + "\nrocketclaw: [sessions]\ntags: [[open, closed]]", "main.md: tags: required by the tags group in rocketclaw, and only with it"},
		{"missing skill folder", easyRequired + "\nskills: [missing]", `main.md: skills: folder "missing" does not exist`},
		{"unknown mcp server", easyRequired + "\nmcp: {unknown: [read]}", `main.md: mcp: server "unknown" is not configured`},
		{"undefined mcp group", easyRequired + "\nmcp: {t: [write]}", `main.md: mcp: server "t" has no "write" tool group`},
		{"wildcard target", easyRequired + "\nallowedNetworkTargets: ['*.googleapis.com']", "main.md: allowedNetworkTargets: *.googleapis.com: wildcards and CIDR ranges are not supported"},
		{"CIDR target", easyRequired + "\nallowedNetworkTargets: [10.0.0.0/8]", "main.md: allowedNetworkTargets: 10.0.0.0/8: wildcards and CIDR ranges are not supported"},
		{"target with methods", easyRequired + "\nallowedNetworkTargets: [{target: api.example.com, methods: [GET]}]", "main.md: allowedNetworkTargets: must be auto or a list of strings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := loadEasyTestAgent(t, tc.frontmatter)

			require.Len(t, errs, 1)
			require.EqualError(t, errs[0], tc.want)
		})
	}
}

func TestLoadEasyAgentFields(t *testing.T) {
	t.Run("required keys only", func(t *testing.T) {
		agent, errs := loadEasyTestAgent(t, easyRequired)
		require.Empty(t, errs)

		one := 1

		require.Equal(t, AgentModeEasy, agent.Mode)
		require.Equal(t, "gpt-6.1-sol", agent.Model)
		require.Equal(t, "high", agent.ReasoningEffort)
		require.Equal(t, "Marketing", agent.Space)
		require.Equal(t, "sessions/$ROCKETCLAW_CONVERSATION_ID", agent.WorkingDirectory)
		require.Equal(t, CustomShell{Emulated: true}, agent.CustomShell, "emulated, and failing closed until set up per turn")
		require.Equal(t, &one, agent.MaxRecursion)
		require.Equal(t, []string{"gpt-6.1-sol#high"}, agent.Subagents)
		require.Equal(t, "Body", agent.Prompt)
		require.Nil(t, agent.DelegatedFrom)
		require.Nil(t, agent.NetworkTargets)
		require.Nil(t, agent.Commands)
		require.Nil(t, agent.SecretsARN)
		require.Nil(t, agent.TagGroups)
		require.Empty(t, agent.ApproverModel+agent.ApproverPrompt)
	})

	t.Run("every section", func(t *testing.T) {
		agent, errs := loadEasyTestAgent(t, easyRequired+`
subagents: [work/gpt-6-luna#max, gpt-6-luna]
maxRecursion: 0
approver: {model: work/gpt-6-luna#max, prompt: Deny writes.}
secretsARN: ["arn::marketing-read"]
commands: {stripe-write: "stripe -c $ROCKETCLAW_METADATA_CUSTOMER_ID"}
mcp: {s: [read]}
rocketclaw: [sessions, tags]
tags: [[triage, resolved]]
skills: [marketing]
allowedNetworkTargets: [api.stripe.com, "https://api.example.com/v1/"]
routines:
  - "*/5 * * * *": Check the dashboard.
  - 30m
delegatedFrom: [main]
schema: {output: {type: object, properties: {answer: {type: string}}}}`)
		require.Empty(t, errs)

		zero := 0
		require.Equal(t, &zero, agent.MaxRecursion)
		require.Equal(t, []string{"work/gpt-6-luna#max", "gpt-6-luna"}, agent.Subagents)
		require.Equal(t, "work/gpt-6-luna#max", agent.ApproverModel)
		require.Equal(t, "Deny writes.", agent.ApproverPrompt)
		require.Equal(t, []string{"arn::marketing-read"}, agent.SecretsARN)
		require.Equal(t, map[string]string{"stripe-write": "stripe -c $ROCKETCLAW_METADATA_CUSTOMER_ID"}, agent.Commands)
		require.Equal(t, [][]string{{"triage", "resolved"}}, agent.TagGroups)
		require.Equal(t, []string{"api.stripe.com", "https://api.example.com/v1/"}, agent.NetworkTargets)
		require.Equal(t, []AgentRoutine{{Schedule: "*/5 * * * *", Instruction: "Check the dashboard."}, {Schedule: "30m"}}, agent.Routines)
		require.Equal(t, []string{"main"}, agent.DelegatedFrom)
		require.Equal(t, map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}}, agent.OutputSchema)
	})

	t.Run("delegatedFrom false", func(t *testing.T) {
		agent, errs := loadEasyTestAgent(t, easyRequired+"\ndelegatedFrom: false")
		require.Empty(t, errs)
		require.Equal(t, []string{}, agent.DelegatedFrom)
	})

	t.Run("advanced", func(t *testing.T) {
		for _, frontmatter := range []string{"model: gpt-5.4\ndescription: Main\nskils: [ignored]", "mode: advanced\nmodel: gpt-5.4\ndescription: Main\nskils: [ignored]"} {
			agent, errs := loadEasyTestAgent(t, frontmatter)
			require.Empty(t, errs)
			require.Equal(t, AgentModeAdvanced, agent.Mode)
			require.Equal(t, "Main", agent.Description)
			require.False(t, agent.Permission.Easy)
			require.Nil(t, agent.MaxRecursion)
		}
	})
}

// An easy agent with no sections has every feature on behind review; only the R18 exceptions skip it.
func TestEasyAgentPermissions(t *testing.T) {
	agent, errs := loadEasyTestAgent(t, easyRequired)
	require.Empty(t, errs)
	require.True(t, agent.Permission.Easy)
	require.False(t, agent.Permission.Sudo)

	for _, tc := range []struct {
		permission, subject string
		want                PermissionAction
	}{
		{"bash", "rm -rf build", PermissionAuto},
		{"read", "notes.md", PermissionAuto},
		{"edit", "notes.md", PermissionAuto},
		{"glob", "*.md", PermissionAuto},
		{"grep", "TODO", PermissionAuto},
		{"webfetch", "https://example.com", PermissionAuto},
		{"websearch", "*", PermissionAuto},
		{"opencode", "local", PermissionAuto},
		{"mcp", "s.update_x", PermissionAuto},
		{"mcp", "t.anything", PermissionAuto},
		{"rocketclaw", codeModeApproveSubject, PermissionAuto},
		{"rocketclaw", "load_agents_md", PermissionDeny},
		{"rocketclaw", "rocketclaw_sessions", PermissionAuto},
		{"rocketclaw", "rocketclaw_kv_store", PermissionAuto},
		{"rocketclaw", "rocketclaw_list", PermissionAuto},
		{"rocketclaw", AllowBackgroundSubject, PermissionAuto},
		{"rocketclaw", "rocketclaw_restart", PermissionAuto},
		{"rocketclaw", "rocketclaw_set_tag", PermissionDeny},
		{"workflow", "deploy", PermissionAuto},
		{"task", "main", PermissionAllow},
		{"task", "other", PermissionDeny},
		{"skill", "support", PermissionAllow},
	} {
		action, _ := agent.Permission.Evaluate(tc.permission, tc.subject)
		require.Equal(t, tc.want, action, tc.permission+" "+tc.subject)

		if tc.want == PermissionAuto && tc.permission != "rocketclaw" || tc.subject == "rocketclaw_restart" || tc.subject == AllowBackgroundSubject {
			require.True(t, agent.Permission.Enabled(tc.permission, tc.subject), tc.subject)
		}
	}

	var allowed []string

	for _, bucket := range agent.Permission.Buckets {
		for _, rule := range bucket.Rules {
			if rule.Action == PermissionAllow {
				allowed = append(allowed, bucket.Name+" "+rule.Pattern)
			}
		}
	}

	require.ElementsMatch(t, []string{"skill *", "task main"}, allowed)
	require.Equal(t, []string{"s", "t"}, visibleMCPServers(agent.Permission, []string{"s", "t"}))
	require.NotContains(t, permissionPrompt(agent.Permission), "Sudo Mode")

	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{"main": agent}})
	factory.rootInstructions = "ROOT AGENTS.md RULES"
	model, hosts := factory.assembleTools(&agent)

	require.Subset(t, slices.Collect(maps.Keys(model)), []string{"skill", "task", executeToolName})
	require.Subset(t, slices.Collect(maps.Keys(hosts)), []string{"bash", "read"})
	require.Equal(t, "Body", factory.sessionSystemPrompt(t.Context(), &agent))
}

func TestEasyAgentSections(t *testing.T) {
	for _, tc := range []struct {
		name, sections string
		on, off        [][2]string
	}{
		{name: "sessions only", sections: "rocketclaw: [sessions]",
			on:  [][2]string{{"rocketclaw", "rocketclaw_sessions"}},
			off: [][2]string{{"rocketclaw", "rocketclaw_restart"}, {"rocketclaw", "rocketclaw_store"}, {"rocketclaw", AllowBackgroundSubject}, {"workflow", "deploy"}}},
		{name: "no rocketclaw groups", sections: "rocketclaw: []",
			off: [][2]string{{"rocketclaw", "rocketclaw_sessions"}, {"rocketclaw", "rocketclaw_restart"}, {"rocketclaw", "rocketclaw_read"}, {"rocketclaw", AllowBackgroundSubject}, {"workflow", "deploy"}}},
		{name: "workflow and restart", sections: "rocketclaw: [workflow, restart, background, pages]",
			on:  [][2]string{{"workflow", "deploy"}, {"rocketclaw", "rocketclaw_restart"}, {"rocketclaw", AllowBackgroundSubject}, {"rocketclaw", "rocketclaw_delete"}},
			off: [][2]string{{"rocketclaw", "rocketclaw_sessions"}}},
		{name: "marketing skills", sections: "skills: [marketing]",
			on:  [][2]string{{"skill", "seo"}, {"skill", "ads"}},
			off: [][2]string{{"skill", "support"}}},
		{name: "no skills", sections: "skills: []", off: [][2]string{{"skill", "seo"}, {"skill", "support"}}},
		{name: "mcp read group", sections: "mcp: {s: [read]}",
			on:  [][2]string{{"mcp", "s.list_reports"}},
			off: [][2]string{{"mcp", "s.update_x"}, {"mcp", "s.other"}, {"mcp", "t.get_x"}}},
		{name: "mcp server auto", sections: "mcp: {s: auto}",
			on:  [][2]string{{"mcp", "s.other"}, {"mcp", "s.update_x"}},
			off: [][2]string{{"mcp", "t.get_x"}}},
		{name: "no mcp", sections: "mcp: {}", off: [][2]string{{"mcp", "s.list_reports"}, {"mcp", "t.get_x"}}},
		{name: "no subagents", sections: "subagents: []", off: [][2]string{{"task", "main"}}},
		{name: "no recursion", sections: "maxRecursion: 0", off: [][2]string{{"task", "main"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent, errs := loadEasyTestAgent(t, easyRequired+"\n"+tc.sections)
			require.Empty(t, errs)

			for _, subject := range tc.on {
				action, _ := agent.Permission.Evaluate(subject[0], subject[1])
				require.Contains(t, []PermissionAction{PermissionAuto, PermissionAllow}, action, subject)
			}

			for _, subject := range tc.off {
				action, _ := agent.Permission.Evaluate(subject[0], subject[1])
				require.Equal(t, PermissionDeny, action, subject)
			}
		})
	}

	agent, errs := loadEasyTestAgent(t, easyRequired+"\nmcp: {}")
	require.Empty(t, errs)
	require.Empty(t, visibleMCPServers(agent.Permission, []string{"s", "t"}))
}
