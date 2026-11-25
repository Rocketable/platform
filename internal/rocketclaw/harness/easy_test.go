package harness

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/require"
)

const easyRequired = "mode: easy\nmodel: gpt-6.1-sol#high\nspace: Marketing\nworkingDirectory: sessions/$ROCKETCLAW_CONVERSATION_ID"

func loadEasyTestAgent(t *testing.T, frontmatter string) (Agent, []error) {
	t.Helper()

	result := LoadAgents(fstest.MapFS{"main.md": testMapFile("---\n" + frontmatter + "\n---\nBody\n")}, &AgentLoadConfig{
		MCPFacets:         map[string]map[string][]string{"s": {"read": {"list_*", "-list_secrets"}, "write": {"update_*"}, "sudo": {"*"}}, "t": {"read": {"get_*"}}},
		Providers:         []string{"openai", "work"},
		DecisionProviders: []string{"openai"},
		OpenCodeServers:   []string{"local", "other"},
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
		{"relative shell", easyRequired + "\nshell: bash", "main.md: shell: must be an absolute path or emulated"},
		{"advanced sticky user routines", "model: gpt-5.4\nsticky: user\nroutines: [30m]", "main.md: routines: a sticky: user agent cannot have routines"},
		{"sticky slack routines", easyRequired + "\nsticky: slack\nroutines: [30m]", "main.md: routines: a sticky: slack agent cannot have routines"},
		{"negative maxRecursion", easyRequired + "\nmaxRecursion: -1", "main.md: maxRecursion: must be an integer of 0 or more"},
		{"unknown subagent provider", easyRequired + "\nsubagents: [nope/gpt-6]", `main.md: model "nope/gpt-6": provider "nope" is not configured`},
		{"unknown subagent prefix provider", easyRequired + "\nsubagents: ['nope/*']", `main.md: model "nope/*": provider "nope" is not configured`},
		{"subagent star inside", easyRequired + "\nsubagents: ['work/*#high']", `main.md: subagents: "work/*#high": * may only end an entry`},
		{"agent wildcard", easyRequired + "\nsubagents: [{agent: '*'}]", `main.md: subagents: agent "*": name one agent; wildcards are not supported`},
		{"agent pattern", easyRequired + "\nsubagents: [{agent: 'writer-*'}]", `main.md: subagents: agent "writer-*": name one agent; wildcards are not supported`},
		{"subagent entry of another shape", easyRequired + "\nsubagents: [{model: gpt-6}]", "main.md: subagents: must be auto or a list of models and {agent: name} entries"},
		{"unknown approver provider", easyRequired + "\napprover: {model: nope/gpt-6}", `main.md: model "nope/gpt-6": provider "nope" is not configured`},
		{"misspelled approver key", easyRequired + "\napprover: {promt: rules}", "main.md: approver.promt: not an easy-mode key"},
		{"approver decision", easyRequired + "\napprover: {decision: {model: openai/gpt-6-luna}}", "main.md: approver.decision: not an easy-mode key"},
		{"unknown decision tool provider", easyRequired + "\ndecision: [typesafe/jev-latest]", `main.md: decision model "typesafe/jev-latest": provider "typesafe" is not configured in decision`},
		{"decision auto", easyRequired + "\ndecision: auto", "main.md: decision: must be a list of provider/model names"},
		{"unknown rocketclaw group", easyRequired + "\nrocketclaw: [chat]", `main.md: rocketclaw: unknown group "chat"`},
		{"tags group without tags", easyRequired + "\nrocketclaw: [tags]", "main.md: tags: required by the tags group in rocketclaw, and only with it"},
		{"tags without tags group", easyRequired + "\nrocketclaw: [sessions]\ntags: [[open, closed]]", "main.md: tags: required by the tags group in rocketclaw, and only with it"},
		{"missing skill folder", easyRequired + "\nskills: [missing]", `main.md: skills: folder "missing" does not exist`},
		{"unknown mcp server", easyRequired + "\nmcp: {unknown: [read]}", `main.md: mcp: server "unknown" is not configured`},
		{"unknown opencode server", easyRequired + "\nopencode: [remote]", `main.md: opencode: server "remote" is not configured`},
		{"opencode auto", easyRequired + "\nopencode: auto", "main.md: opencode: must be a list of servers"},
		{"undefined mcp facet", easyRequired + "\nmcp: {t: [write]}", `main.md: mcp: server "t" has no "write" facet`},
		{"wildcard target", easyRequired + "\nallowedNetworkTargets: ['*.googleapis.com']", "main.md: allowedNetworkTargets: *.googleapis.com: wildcards and CIDR ranges are not supported"},
		{"CIDR target", easyRequired + "\nallowedNetworkTargets: [10.0.0.0/8]", "main.md: allowedNetworkTargets: 10.0.0.0/8: wildcards and CIDR ranges are not supported"},
		{"target with methods", easyRequired + "\nallowedNetworkTargets: [{target: api.example.com, methods: [GET]}]", "main.md: allowedNetworkTargets: must be auto or a list of strings"},
		{"secrets not a list", easyRequired + "\nsecretsARN: arn::one", "main.md: secretsARN: must be a list of ARNs"},
		{"approver folder of its own", easyRequired + "\napprover: {workingDirectory: other}", "main.md: approver.workingDirectory: not an easy-mode key"},
		{"approver tags group without tags", easyRequired + "\napprover: {rocketclaw: [tags]}", "main.md: approver.tags: required by the tags group in rocketclaw, and only with it"},
		{"approver tags without its rocketclaw list", easyRequired + "\napprover: {tags: [[open, closed]]}", "main.md: approver.tags: required by the tags group in rocketclaw, and only with it"},
		{"approver restart group", easyRequired + "\napprover: {rocketclaw: [restart]}", "main.md: approver.rocketclaw: an approver cannot use the restart group"},
		{"approver background group", easyRequired + "\napprover: {rocketclaw: [sessions, background]}", "main.md: approver.rocketclaw: an approver cannot use the background group"},
		{"approver unknown rocketclaw group", easyRequired + "\napprover: {rocketclaw: [chat]}", `main.md: approver.rocketclaw: unknown group "chat"`},
		{"approver missing skill folder", easyRequired + "\napprover: {skills: [missing]}", `main.md: approver.skills: folder "missing" does not exist`},
		{"approver undefined mcp facet", easyRequired + "\napprover: {mcp: {t: [write]}}", `main.md: approver.mcp: server "t" has no "write" facet`},
		{"approver wildcard target", easyRequired + "\napprover: {allowedNetworkTargets: ['*.googleapis.com']}", "main.md: approver.allowedNetworkTargets: *.googleapis.com: wildcards and CIDR ranges are not supported"},
		{"approver reserved command name", easyRequired + "\napprover: {commands: {curl: /usr/bin/curl}}", "main.md: approver.commands: curl is the emulated shell's own"},
		{"approver secrets not a list", easyRequired + "\napprover: {secretsARN: arn::one}", "main.md: approver.secretsARN: must be a list of ARNs"},
		{"approver copy model", easyRequired + "\napprover: {subagents: [gpt-6-luna]}", "main.md: approver.subagents: must be a list of {agent: name} entries"},
		{"approver subagents auto", easyRequired + "\napprover: {subagents: auto}", "main.md: approver.subagents: must be a list of {agent: name} entries"},
		{"leftover delegatedFrom", easyRequired + "\ndelegatedFrom: false", "main.md: delegatedFrom: not an easy-mode key"},
		{"schedule phrase", easyRequired + "\nroutines: [every 30min]", `main.md: routines: schedule "every 30min": invalid cron expression: expected exactly 5 fields, found 2: [every 30min]`},
		{"duplicate schedule", easyRequired + "\nroutines: [30m, {' 30m': Again.}]", `main.md: routines: schedule "30m" is listed twice`},
		{"advanced schedule phrase", "model: gpt-5.4\nroutines: [every 30min]", `main.md: routines: schedule "every 30min": invalid cron expression: expected exactly 5 fields, found 2: [every 30min]`},
		{"advanced unknown provider", "model: nope/gpt-6", `main.md: model "nope/gpt-6": provider "nope" is not configured`},
		{"advanced unknown key", "model: gpt-5.4\nsudu: yes", "main.md: sudu: not an advanced-mode key"},
		{"advanced approver", "model: gpt-5.4\napprover: {model: gpt-5.4}", "main.md: approver: not an advanced-mode key"},
		{"duplicate tag", easyRequired + "\nrocketclaw: [tags]\ntags: [[red], [red]]", `main.md: tags: empty or duplicate tag name "red"`},
		{"approver empty tag", easyRequired + "\napprover: {rocketclaw: [tags], tags: [['']]}", `main.md: approver.tags: empty or duplicate tag name ""`},
		{"advanced empty tag group", "model: gpt-5.4\npermission: {rocketclaw: {rocketclaw_set_tag: [[]]}}", "main.md: permission.rocketclaw.rocketclaw_set_tag: tag groups must not be empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := loadEasyTestAgent(t, tc.frontmatter)

			require.Len(t, errs, 1)
			require.EqualError(t, errs[0], tc.want)
		})
	}
}

func TestLoadAgentSticky(t *testing.T) {
	for _, required := range []string{"model: gpt-5.4", easyRequired} {
		for line, want := range map[string]AgentSticky{"": AgentStickyOff, "\nsticky: off": AgentStickyOff, "\nsticky: user": AgentStickyUser, "\nsticky: global": AgentStickyGlobal, "\nsticky: slack": AgentStickySlack} {
			agent, errs := loadEasyTestAgent(t, required+line)
			require.Empty(t, errs)
			require.Equal(t, want, agent.Sticky, required+line)
		}

		_, errs := loadEasyTestAgent(t, required+"\nsticky: [user]")
		require.Len(t, errs, 1)
		require.EqualError(t, errs[0], "main.md: sticky: must be off, user, global or slack")
	}
}

func TestLoadEasyAgentFields(t *testing.T) {
	t.Run("required keys only", func(t *testing.T) {
		agent, errs := loadEasyTestAgent(t, easyRequired)
		require.Empty(t, errs)

		one := 1

		require.Equal(t, "gpt-6.1-sol", agent.Model)
		require.Equal(t, "high", agent.ReasoningEffort)
		require.Equal(t, "Marketing", agent.Space)
		require.Equal(t, "sessions/$ROCKETCLAW_CONVERSATION_ID", agent.WorkingDirectory)
		require.Equal(t, CustomShell{Emulated: true}, agent.CustomShell, "emulated, and failing closed until set up per turn")
		require.Equal(t, &one, agent.MaxRecursion)
		require.Equal(t, []string{"gpt-6.1-sol#high"}, agent.CloneModels)
		require.Equal(t, "Body", agent.Prompt)
		require.Nil(t, agent.NetworkTargets)
		require.Nil(t, agent.Commands)
		require.Nil(t, agent.SecretsARN)
		require.Nil(t, agent.TagGroups)
		require.Empty(t, agent.ApproverModel+agent.ApproverPrompt)
		require.Equal(t, []any{agent.WorkingDirectory, agent.CustomShell}, []any{agent.Approver.WorkingDirectory, agent.Approver.CustomShell}, "every easy agent's approver works in its folder")
		require.Len(t, agent.Approver.Permission.Buckets, 4, "read, glob and grep, and the rocketclaw groups no approver gets")
	})

	t.Run("every section", func(t *testing.T) {
		agent, errs := loadEasyTestAgent(t, easyRequired+`
subagents: [work/gpt-6-luna#max, gpt-6-luna]
maxRecursion: 0
approver: {model: work/gpt-6-luna#max, prompt: Deny writes.}
decision: [openai/gpt-6-luna]
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
schema: {output: {type: object, properties: {answer: {type: string}}}}`)
		require.Empty(t, errs)

		zero := 0
		require.Equal(t, &zero, agent.MaxRecursion)
		require.Equal(t, []string{"work/gpt-6-luna#max", "gpt-6-luna"}, agent.CloneModels)
		require.Equal(t, "work/gpt-6-luna#max", agent.ApproverModel)
		require.Equal(t, "Deny writes.", agent.ApproverPrompt)
		require.Equal(t, []string{"arn::marketing-read"}, agent.SecretsARN)
		require.Equal(t, map[string]string{"stripe-write": "stripe -c $ROCKETCLAW_METADATA_CUSTOMER_ID"}, agent.Commands)
		require.Equal(t, [][]string{{"triage", "resolved"}}, agent.TagGroups)
		require.Equal(t, []string{"api.stripe.com", "https://api.example.com/v1/"}, agent.NetworkTargets)

		every5Minutes, errFive := config.ParseRoutineSchedule("*/5 * * * *")
		every30Minutes, errThirty := config.ParseRoutineSchedule("30m")
		require.NoError(t, errors.Join(errFive, errThirty))
		require.Equal(t, []AgentRoutine{{Schedule: every5Minutes, Instruction: "Check the dashboard."}, {Schedule: every30Minutes}}, agent.Routines)
		require.Equal(t, map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}}, agent.OutputSchema)
	})

	t.Run("shell", func(t *testing.T) {
		for frontmatter, want := range map[string]CustomShell{"shell: emulated": {Emulated: true}, "shell: /usr/local/bin/wrap": {Path: "/usr/local/bin/wrap"}} {
			agent, errs := loadEasyTestAgent(t, easyRequired+"\n"+frontmatter)
			require.Empty(t, errs)
			require.Equal(t, want, agent.CustomShell)
			require.Equal(t, "sessions/$ROCKETCLAW_CONVERSATION_ID", agent.WorkingDirectory)
		}
	})

	t.Run("advanced", func(t *testing.T) {
		for _, frontmatter := range []string{"model: gpt-5.4\ndescription: Main", "mode: advanced\nmodel: gpt-5.4\ndescription: Main"} {
			agent, errs := loadEasyTestAgent(t, frontmatter)
			require.Empty(t, errs)
			require.Equal(t, "Main", agent.Description)
			require.Equal(t, new(1), agent.MaxRecursion)
		}
	})
}

func TestEasySubagentTargets(t *testing.T) {
	agent, errs := loadEasyTestAgent(t, easyRequired+"\nsubagents: [gpt-6-luna, {agent: writer}]")
	require.Empty(t, errs)
	require.Equal(t, []string{"gpt-6-luna"}, agent.CloneModels)

	var tasks []PermissionRule

	for _, bucket := range agent.Permission.Buckets {
		if bucket.Name == "task" {
			tasks = append(tasks, bucket.Rules...)
		}
	}

	require.Equal(t, []PermissionRule{{Pattern: "writer", Action: PermissionAuto}}, tasks, "the agent entry is a reviewed target, and the agent is no target of its own")

	for _, name := range []string{"writer", "reader"} {
		action, _ := agent.Approver.Permission.Evaluate("task", name)
		require.Equal(t, map[string]PermissionAction{"writer": PermissionAuto, "reader": PermissionDeny}[name], action, "the reviewer borrows the agent's targets: %s", name)
	}

	agent, errs = loadEasyTestAgent(t, easyRequired+"\nsubagents: [{agent: writer}]\napprover: {subagents: [{agent: reader}]}")
	require.Empty(t, errs)
	require.Empty(t, agent.CloneModels)
	require.Nil(t, agent.Approver.CloneModels)

	for _, name := range []string{"writer", "reader"} {
		action, _ := agent.Approver.Permission.Evaluate("task", name)
		require.Equal(t, map[string]PermissionAction{"writer": PermissionDeny, "reader": PermissionAuto}[name], action, "the reviewer's own list replaces the agent's: %s", name)
	}
}

func TestEasyAndAdvancedShareRecordFields(t *testing.T) {
	easy, errs := loadEasyTestAgent(t, easyRequired+"\nrocketclaw: [tags]\ntags: [[red, green], [open]]")
	require.Empty(t, errs)

	advanced, errs := loadEasyTestAgent(t, "model: gpt-5.4\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[red, green], [open]]")
	require.Empty(t, errs)

	require.Equal(t, [][]string{{"red", "green"}, {"open"}}, easy.TagGroups)
	require.Equal(t, easy.TagGroups, advanced.TagGroups)
	require.Equal(t, easy.ReplyHint, advanced.ReplyHint, "neither file asks for a reply style")
	require.Empty(t, advanced.ReplyHint)
	require.Equal(t, easy.MaxRecursion, advanced.MaxRecursion)

	advanced, errs = loadEasyTestAgent(t, "model: gpt-5.4\nadditionalInstructions: Reply in one sentence.")
	require.Empty(t, errs)
	require.Equal(t, "Reply in one sentence.", advanced.ReplyHint)
}

func TestEasySubagentModelPrefixes(t *testing.T) {
	agent, errs := loadEasyTestAgent(t, easyRequired+"\nsubagents: [gpt-6-luna, 'work/*', 'gpt-6.1-sol#*']")
	require.Empty(t, errs)

	for model, want := range map[string]bool{
		"gpt-6-luna":            true,
		"gpt-6-luna#max":        false,
		"work/gpt-6-luna":       true,
		"work/gpt-6-luna#max":   true,
		"openai/gpt-6-luna":     false,
		"gpt-6.1-sol#low":       true,
		"gpt-6.1-sol":           false,
		"work-other/gpt-6-luna": false,
		"work/*":                false,
		"work/":                 false,
		"gpt-6.1-sol#":          false,
	} {
		require.Equal(t, want, agent.allowsSubagentModel(model), model)
	}

	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{"main": agent}})
	tools, _ := factory.assembleTools(&agent)
	clone := tools["task_clone"].Definition
	require.Contains(t, clone.Description.Value, "Available models:\n- gpt-6-luna\n- work/*\n- gpt-6.1-sol#*")
	require.Contains(t, clone.Description.Value, "A listed entry ending in * stands for every model whose name, #effort included, starts with the text before the *")
	require.JSONEq(t, `{"type":"string"}`, string(marshalParameters(t, clone.Parameters["properties"].(map[string]any)["model"].(map[string]any))), "a wildcard entry leaves the model open to every name it matches")
}

func TestEasyAgentPermissions(t *testing.T) {
	agent, errs := loadEasyTestAgent(t, easyRequired)
	require.Empty(t, errs)
	require.False(t, agent.Permission.Sudo)

	for _, tc := range []struct {
		permission, subject string
		want                PermissionAction
	}{
		{"bash", "rm -rf build", PermissionAllow},
		{"read", "notes.md", PermissionAllow},
		{"edit", "notes.md", PermissionAllow},
		{"glob", "*.md", PermissionAllow},
		{"grep", "TODO", PermissionAllow},
		{"webfetch", "https://example.com", PermissionAllow},
		{"websearch", "*", PermissionAllow},
		{"opencode", "local", PermissionDeny},
		{"mcp", "s.update_x", PermissionDeny},
		{"mcp", "t.get_x", PermissionDeny},
		{"rocketclaw", codeModeApproveSubject, PermissionAuto},
		{"rocketclaw", "rocketclaw_sessions", PermissionAllow},
		{"rocketclaw", "rocketclaw_kv_store", PermissionAllow},
		{"rocketclaw", "rocketclaw_list", PermissionAllow},
		{"rocketclaw", AllowBackgroundSubject, PermissionAllow},
		{"rocketclaw", "rocketclaw_restart", PermissionAllow},
		{"rocketclaw", "rocketclaw_set_tag", PermissionDeny},
		{"task", "main", PermissionDeny},
		{"task", "other", PermissionDeny},
		{"task_clone", "gpt-6.1-sol#high", PermissionAllow},
		{"skill", "support", PermissionAllow},
	} {
		action, _ := agent.Permission.Evaluate(tc.permission, tc.subject)
		require.Equal(t, tc.want, action, tc.permission+" "+tc.subject)
		require.Equal(t, tc.want == PermissionAllow, agent.Permission.Enabled(tc.permission, tc.subject), tc.subject)
	}

	var reviewed []string

	for _, bucket := range agent.Permission.Buckets {
		for _, rule := range bucket.Rules {
			if rule.Action == PermissionAuto {
				reviewed = append(reviewed, bucket.Name+" "+rule.Pattern)
			}
		}
	}

	require.Equal(t, []string{"rocketclaw " + codeModeApproveSubject}, reviewed, "only the whole script is reviewed")
	require.Empty(t, visibleMCPServers(agent.Permission, []string{"s", "t"}))
	require.NotContains(t, PermissionPrompt(agent.Permission), "Sudo Mode")

	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{"main": agent}})
	model, hosts := factory.assembleTools(&agent)

	require.Subset(t, slices.Collect(maps.Keys(model)), []string{"skill", executeToolName, "task_clone"})
	require.NotContains(t, model, "task", "model entries alone name no target")
	require.Subset(t, slices.Collect(maps.Keys(hosts)), []string{"bash", "read"})
	require.Equal(t, "Body\n\nWhen a turn carries a note that the message did not address you, "+UnaddressedReadOnly, factory.sessionSystemPrompt(t.Context(), &agent))
}

func TestEasyAgentSections(t *testing.T) {
	for _, tc := range []struct {
		name, sections string
		on, off        [][2]string
	}{
		{name: "sessions only", sections: "rocketclaw: [sessions]",
			on:  [][2]string{{"rocketclaw", "rocketclaw_sessions"}},
			off: [][2]string{{"rocketclaw", "rocketclaw_restart"}, {"rocketclaw", "rocketclaw_store"}, {"rocketclaw", AllowBackgroundSubject}}},
		{name: "no rocketclaw groups", sections: "rocketclaw: []",
			off: [][2]string{{"rocketclaw", "rocketclaw_sessions"}, {"rocketclaw", "rocketclaw_restart"}, {"rocketclaw", "rocketclaw_read"}, {"rocketclaw", AllowBackgroundSubject}}},
		{name: "restart, background and pages", sections: "rocketclaw: [restart, background, pages]",
			on:  [][2]string{{"rocketclaw", "rocketclaw_restart"}, {"rocketclaw", AllowBackgroundSubject}, {"rocketclaw", "rocketclaw_delete"}},
			off: [][2]string{{"rocketclaw", "rocketclaw_sessions"}}},
		{name: "marketing skills", sections: "skills: [marketing]",
			on:  [][2]string{{"skill", "seo"}, {"skill", "ads"}},
			off: [][2]string{{"skill", "support"}}},
		{name: "no skills", sections: "skills: []", off: [][2]string{{"skill", "seo"}, {"skill", "support"}}},
		{name: "mcp read facet", sections: "mcp: {s: [read]}",
			on:  [][2]string{{"mcp", "s.list_reports"}},
			off: [][2]string{{"mcp", "s.list_secrets"}, {"mcp", "s.update_x"}, {"mcp", "s.other"}, {"mcp", "t.get_x"}}},
		{name: "mcp overlapping facets", sections: "mcp: {s: [read, sudo]}",
			on:  [][2]string{{"mcp", "s.list_secrets"}, {"mcp", "s.other"}},
			off: [][2]string{{"mcp", "t.get_x"}}},
		{name: "no mcp", sections: "mcp: {}", off: [][2]string{{"mcp", "s.list_reports"}, {"mcp", "t.get_x"}}},
		{name: "opencode server", sections: "opencode: [local]", on: [][2]string{{"opencode", "local"}}, off: [][2]string{{"opencode", "other"}}},
		{name: "no subagents", sections: "subagents: []", off: [][2]string{{"task", "main"}, {"task_clone", "gpt-6.1-sol#high"}}},
		{name: "no decision models", off: [][2]string{{"decision", "openai/gpt-6-luna"}}},
		{name: "decision model", sections: "decision: [openai/gpt-6-luna]",
			on:  [][2]string{{"decision", "openai/gpt-6-luna"}},
			off: [][2]string{{"decision", "openai/other"}}},
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

	agent, errs = loadEasyTestAgent(t, easyRequired+"\nmcp: {s: [write, read]}")
	require.Empty(t, errs)
	require.Equal(t, []string{"s"}, visibleMCPServers(agent.Permission, []string{"s", "t"}))
	require.Equal(t, []PermissionRule{
		{Pattern: "s.list_*", Action: PermissionAllow, Except: []string{"s.list_secrets"}},
		{Pattern: "s.update_*", Action: PermissionAllow},
	}, agent.Permission.Buckets[slices.IndexFunc(agent.Permission.Buckets, func(bucket PermissionBucket) bool { return bucket.Name == mcpPermissionBucket })].Rules)

	openCodeAPI := looperTool{Permission: "opencode", VisibilitySubjects: []string{"local", "other"}}

	for sections, want := range map[string]bool{"": false, "\nopencode: []": false, "\nopencode: [other]": true} {
		agent, errs := loadEasyTestAgent(t, easyRequired+sections)
		require.Empty(t, errs)
		require.Equal(t, want, toolVisible(&agent, "opencode_api", &openCodeAPI), sections)
	}
}

func TestEasyApproverTools(t *testing.T) {
	guardian := [][2]string{{"read", "notes.md"}, {"glob", "*.md"}, {"grep", "TODO"}}

	for _, tc := range []struct {
		name, approver string
		on, off        [][2]string
	}{
		{name: "an emptied section keeps the guardian's own tools", approver: "skills: []",
			off: [][2]string{{"bash", "ls"}, {"edit", "notes.md"}, {"webfetch", "https://example.com"}, {"websearch", "*"}, {"mcp", "s.list_reports"}, {"skill", "seo"}, {"rocketclaw", "rocketclaw_sessions"}, {"rocketclaw", "rocketclaw_restart"}, {"task", "main"}}},
		{name: "listed sections", approver: "rocketclaw: [sessions, tags]\ntags: [[red, green]]\nskills: [marketing]\nmcp: {s: [read]}\nallowedNetworkTargets: [api.stripe.com]\ncommands: {lookup: ./lookup}",
			on:  [][2]string{{"rocketclaw", "rocketclaw_sessions"}, {"skill", "seo"}, {"skill", "ads"}, {"mcp", "s.list_reports"}, {"webfetch", "https://api.stripe.com"}, {"bash", "lookup 42"}},
			off: [][2]string{{"rocketclaw", "rocketclaw_restart"}, {"rocketclaw", "rocketclaw_store"}, {"skill", "support"}, {"mcp", "s.update_x"}, {"mcp", "t.get_x"}, {"edit", "notes.md"}}},
		{name: "auto sections", approver: "rocketclaw: auto\nskills: auto\nallowedNetworkTargets: auto",
			on:  [][2]string{{"rocketclaw", "rocketclaw_sessions"}, {"rocketclaw", "rocketclaw_kv_store"}, {"skill", "support"}, {"webfetch", "https://example.com"}},
			off: [][2]string{{"bash", "ls"}, {"rocketclaw", "rocketclaw_restart"}, {"rocketclaw", AllowBackgroundSubject}}},
		{name: "no commands and no targets", approver: "commands: {}\nallowedNetworkTargets: []",
			off: [][2]string{{"bash", "ls"}, {"webfetch", "https://example.com"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent, errs := loadEasyTestAgent(t, easyRequired+"\napprover:\n  "+strings.ReplaceAll(tc.approver, "\n", "\n  "))
			require.Empty(t, errs)
			require.NotNil(t, agent.Approver)

			for _, subject := range slices.Concat(guardian, tc.on) {
				action, _ := agent.Approver.Permission.Evaluate(subject[0], subject[1])
				require.Equal(t, PermissionAllow, action, "%v runs without review: nothing reviews the reviewer", subject)
			}

			for _, subject := range tc.off {
				action, _ := agent.Approver.Permission.Evaluate(subject[0], subject[1])
				require.Equal(t, PermissionDeny, action, subject)
			}
		})
	}

	agent, errs := loadEasyTestAgent(t, easyRequired+`
shell: /usr/local/bin/wrap
secretsARN: ["arn::agent"]
commands: {deploy: ./deploy}
tags: [[triage, resolved]]
approver:
  model: work/gpt-6-luna#max
  prompt: Deny writes.
  rocketclaw: [tags]
  tags: [[red, green]]
  secretsARN: ["arn::reviewer"]
  commands: {lookup: ./lookup}
  allowedNetworkTargets: []`)
	require.Empty(t, errs)

	approver := agent.Approver
	require.Equal(t, []any{guardianAgentName, "low"}, []any{approver.Name, approver.ReasoningEffort})
	require.Equal(t, agent.WorkingDirectory, approver.WorkingDirectory, "the reviewer works in the agent's folder")
	require.Equal(t, CustomShell{Path: "/usr/local/bin/wrap"}, approver.CustomShell, "and in the agent's kind of shell")
	require.Equal(t, [][]string{{"red", "green"}}, approver.TagGroups)
	require.Equal(t, []string{"arn::reviewer"}, approver.SecretsARN)
	require.Equal(t, map[string]string{"lookup": "./lookup"}, approver.Commands)
	require.Equal(t, []string{}, approver.NetworkTargets, "no targets, not the public addresses auto allows")

	require.Equal(t, "work/gpt-6-luna#max Deny writes.", agent.ApproverModel+" "+agent.ApproverPrompt)
	require.Equal(t, [][]string{{"triage", "resolved"}}, agent.TagGroups)
	require.Equal(t, []string{"arn::agent"}, agent.SecretsARN)
	require.Equal(t, map[string]string{"deploy": "./deploy"}, agent.Commands)
	require.Nil(t, agent.NetworkTargets)

	action, _ := agent.Permission.Evaluate("mcp", "t.get_x")
	require.Equal(t, PermissionDeny, action, "outside MCP tools stay off unless the agent's file names their facets")

	unlisted, errs := loadEasyTestAgent(t, easyRequired+"\napprover: {commands: {lookup: ./lookup}}")
	require.Empty(t, errs)
	require.Equal(t, []string{}, unlisted.Approver.NetworkTargets, "a shell without listed targets reaches no network")
	require.Nil(t, unlisted.Approver.SecretsARN)
}
