package agentlint

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandPath(t *testing.T) {
	for _, tt := range []struct {
		pattern string
		want    string
	}{
		{"", ""},
		{" \t", " \t"},
		{"scripts/run.sh *", "scripts/run.sh"},
		{" bash ", "bash"},
		{"sh", "sh"},
		{"bash scripts/run.sh *", "scripts/run.sh"},
		{"\tsh\u2003scripts/run.sh arg", "scripts/run.sh"},
		{"/bin/bash scripts/run.sh", "/bin/bash"},
	} {
		t.Run(tt.pattern, func(t *testing.T) {
			assert.Equal(t, tt.want, commandPath(tt.pattern))
		})
	}
}

func TestLintFindings(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeAgent(t, runtimeRoot, "writer.md", `---
description: writer
permission:
  edit:
    "scripts/helper.sh": allow
    "BROWSING_NOTES.md": allow
  task:
    "executor": allow
---
writer
`)
	writeAgent(t, runtimeRoot, "executor.md", `---
description: executor
permission:
  bash:
    "scripts/helper.sh *": allow
---
executor
`)
	writeAgent(t, runtimeRoot, "reader.md", `---
description: reader
permission:
  read:
    "BROWSING_NOTES.md": allow
---
reader
`)
	writeAgent(t, runtimeRoot, "loop.md", `---
description: loop
permission:
  task:
    "loop": allow
---
loop
`)
	writeAgent(t, runtimeRoot, "plural.md", `---
description: plural
permissions:
  bash:
    "echo ok": allow
---
plural
`)
	writeAgent(t, runtimeRoot, "same.md", `---
description: same
permission:
  read:
    "scripts/call.sh": allow
  edit:
    "scripts/call.sh": allow
  bash:
    "scripts/call.sh subcommand *": allow
---
same
`)
	writeAgent(t, runtimeRoot, "guarded.md", `---
description: guarded
guardrail: missing-safety
---
guarded
`)
	writeAgent(t, runtimeRoot, "expensive.md", `---
description: expensive
reasoningEffort: xhigh
---
expensive
`)

	for _, external := range []string{"webfetch: allow", "websearch: allow", "webfetch: allow\n  websearch: allow", "webfetch:\n    'https://example.com': allow\n    '*': deny"} {
		t.Run(external, func(t *testing.T) {
			writeAgent(t, runtimeRoot, "browser.md", fmt.Sprintf(`---
description: browser
permission:
  %s
  read:
    "BROWSING_NOTES.md": allow
  edit:
    "BROWSING_NOTES.md": allow
---
browser
`, external))
			result, err := Lint(runtimeRoot, new(config.Config))
			require.NoError(t, err)
			assertFindingCodes(t, result.Findings, rc001, rc002, rc003, rc004, rc005, rc006, rc007, rc008)

			var externalContent []Finding

			for _, finding := range result.Findings {
				if finding.Code == rc005 {
					externalContent = append(externalContent, finding)
				}
			}

			assert.Equal(t, []Finding{
				{Code: rc005, Severity: "error", Path: "agents/browser.md -> agents/reader.md", Message: "browser can write external content to BROWSING_NOTES.md that reader can read", keys: []string{"edit", "BROWSING_NOTES.md", "read", "BROWSING_NOTES.md"}},
				{Code: rc005, Severity: "error", Path: "agents/browser.md -> agents/writer.md", Message: "browser can write external content to BROWSING_NOTES.md that writer can read", keys: []string{"edit", "BROWSING_NOTES.md", "edit", "BROWSING_NOTES.md"}},
			}, externalContent)
		})
	}
}

func TestLintRejectsInvalidSkills(t *testing.T) {
	runtimeRoot := t.TempDir()
	skillsRoot := filepath.Join(runtimeRoot, "skills", "broken")
	require.NoError(t, os.MkdirAll(skillsRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillsRoot, "SKILL.md"), []byte("no frontmatter"), 0o644))

	result, err := Lint(runtimeRoot, new(config.Config))
	require.EqualError(t, err, "load skills: broken/SKILL.md: missing YAML frontmatter")
	assert.Equal(t, Result{}, result)
}

func TestLintSuppressions(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeAgent(t, runtimeRoot, "same.md", `---
description: same
permission:
  edit:
    "scripts/call.sh": allow
  bash:
    "scripts/call.sh *": allow #nolint RC001: sandboxed
---
same
`)
	writeAgent(t, runtimeRoot, "guarded.md", `---
description: guarded
guardrail: missing-safety #nolint: defined by pending overlay
permission:
  task:
    "*": allow #nolint RC003: bounded by the caller
---
guarded
`)
	writeAgent(t, runtimeRoot, "expensive.md", `---
description: expensive
reasoningEffort: xhigh #nolint RC008: approved for hard reasoning
---
expensive
`)

	result, err := Lint(runtimeRoot, new(config.Config))
	require.NoError(t, err)

	assert.Equal(t, []Finding{
		{Code: rc002, Severity: "error", Path: "agents/same.md", Message: "same can read scripts/call.sh and execute constrained command scripts/call.sh *", keys: []string{"edit", "scripts/call.sh", "bash", "scripts/call.sh *"}},
	}, result.Findings)
}

func TestLintCustomShell(t *testing.T) {
	runtimeRoot, workspace := t.TempDir(), t.TempDir()
	writeAgent(t, runtimeRoot, "boxed.md", "---\ndescription: boxed\ncustomShell: "+filepath.Join(workspace, "bin", "enter.sh")+"\n---\nboxed\n")
	writeAgent(t, runtimeRoot, "outside.md", "---\ndescription: outside\ncustomShell: /usr/local/bin/agent-sandbox\n---\noutside\n")
	writeAgent(t, runtimeRoot, "editor.md", `---
description: editor
permission:
  edit:
    "bin/*": allow
    "agents/boxed.md": allow
---
editor
`)
	writeAgent(t, runtimeRoot, "typo.md", "---\ndescription: typo\ncustom_shell: /usr/local/bin/agent-sandbox\n---\ntypo\n")

	result, err := Lint(runtimeRoot, &config.Config{Workspace: workspace})
	require.NoError(t, err)

	assert.Equal(t, []Finding{
		{Code: rc009, Severity: "error", Path: "agents/typo.md", Message: "custom_shell is ignored; the field is customShell", keys: []string{"custom_shell"}},
		{Code: rc010, Severity: "error", Path: "agents/editor.md", Message: "editor can edit agents/boxed.md, which defines boxed and its customShell", keys: []string{"edit", "agents/boxed.md"}},
		{Code: rc010, Severity: "error", Path: "agents/editor.md", Message: "editor can edit bin/enter.sh, the customShell of boxed", keys: []string{"edit", "bin/*"}},
	}, result.Findings)
}

func TestLintSudo(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeAgent(t, runtimeRoot, "tagged.md", "---\ndescription: tagged\nsudo: yes\nmaxRecursion: 2\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[red, green]]\n---\ntagged\n")
	writeAgent(t, runtimeRoot, "mixed.md", "---\ndescription: mixed\nsudo: on\nmaxRecursion: 2\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[red, green]]\n  bash: allow\n---\nmixed\n")
	writeAgent(t, runtimeRoot, "unbounded.md", "---\ndescription: unbounded\nsudo: true\n---\nunbounded\n")
	writeAgent(t, runtimeRoot, "guarded.md", "---\ndescription: guarded\nsudo: yes\nmaxRecursion: 2\npermission:\n  rocketclaw:\n    rocketclaw_sessions_recipient: [main]\n---\nguarded\n")
	writeAgent(t, runtimeRoot, "both.md", "---\ndescription: both\nsudo: yes\nmaxRecursion: 2\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[red, green]]\n    rocketclaw_sessions_recipient: false\n---\nboth\n")
	writeAgent(t, runtimeRoot, "extra.md", "---\ndescription: extra\nsudo: yes\nmaxRecursion: 2\npermission:\n  rocketclaw:\n    rocketclaw_sessions_recipient: [main]\n    rocketclaw_sessions: allow\n---\nextra\n")

	result, err := Lint(runtimeRoot, new(config.Config))
	require.NoError(t, err)

	message := "sudo replaces permission; only permission.rocketclaw.rocketclaw_set_tag and permission.rocketclaw.rocketclaw_sessions_recipient may remain"
	assert.Equal(t, []Finding{
		{Code: rc003, Severity: "error", Path: "agents/unbounded.md", Message: "unbounded participates in a task delegation cycle without bounded maxRecursion", keys: []string{"maxRecursion", "task"}},
		{Code: rc011, Severity: "error", Path: "agents/extra.md", Message: message, keys: []string{"sudo", "permission"}},
		{Code: rc011, Severity: "error", Path: "agents/mixed.md", Message: message, keys: []string{"sudo", "permission"}},
	}, result.Findings)
}

// Lint reads the frontmatter the runtime loaded, so agents the runtime accepts lint too.
func TestLintAcceptsAgentsTheRuntimeLoads(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeAgent(t, runtimeRoot, "colon.md", "---\ndescription: Use when: tests fail\n---\ncolon\n")
	require.NoError(t, os.WriteFile(filepath.Join(runtimeRoot, "agents", "crlf.md"), []byte("---\r\nmodel: gpt-6.1-sol\r\ndescription: crlf\r\npermissions: {}\r\n---\r\ncrlf\r\n"), 0o644))

	result, err := Lint(runtimeRoot, new(config.Config))
	require.NoError(t, err)
	assert.Equal(t, []Finding{
		{Code: rc006, Severity: "error", Path: "agents/crlf.md", Message: "plural permissions frontmatter is ignored; use permission", keys: []string{"permissions"}},
	}, result.Findings)
}

func TestLintReasoningEffortXHighError(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeAgent(t, runtimeRoot, "expensive.md", `---
description: expensive
reasoningEffort: xhigh
---
expensive
`)
	writeAgent(t, runtimeRoot, "suffix.md", "---\ndescription: suffix\n---\nsuffix\n", "gpt-6.1-sol#xhigh")
	// writeAgent quotes the model, so a model line comment needs the raw file.
	require.NoError(t, os.WriteFile(filepath.Join(runtimeRoot, "agents", "approved.md"), []byte("---\nmodel: gpt-6.1-sol#xhigh #nolint RC008: approved for hard reasoning\ndescription: approved\n---\napproved\n"), 0o644))

	result, err := Lint(runtimeRoot, new(config.Config))
	require.NoError(t, err)
	require.Len(t, result.Findings, 2)
	assert.Equal(t, []string{"agents/expensive.md", "agents/suffix.md"}, []string{result.Findings[0].Path, result.Findings[1].Path})

	for _, finding := range result.Findings {
		assert.Equal(t, rc008, finding.Code)
		assert.Equal(t, "error", finding.Severity)
	}
}

func TestLintSuppressionIsLocal(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeAgent(t, runtimeRoot, "same.md", `---
description: same
permission:
  edit:
    "scripts/allowed-risk.sh": allow
    "scripts/open-risk.sh": allow
  bash:
    "scripts/allowed-risk.sh *": allow #nolint RC001: sandboxed
    "scripts/open-risk.sh *": allow
---
same
`)

	result, err := Lint(runtimeRoot, new(config.Config))
	require.NoError(t, err)

	assert.Equal(t, []Finding{
		{Code: rc001, Severity: "error", Path: "agents/same.md", Message: "same can edit scripts/open-risk.sh and execute scripts/open-risk.sh *", keys: []string{"edit", "scripts/open-risk.sh", "bash", "scripts/open-risk.sh *"}},
		{Code: rc002, Severity: "error", Path: "agents/same.md", Message: "same can read scripts/allowed-risk.sh and execute constrained command scripts/allowed-risk.sh *", keys: []string{"edit", "scripts/allowed-risk.sh", "bash", "scripts/allowed-risk.sh *"}},
		{Code: rc002, Severity: "error", Path: "agents/same.md", Message: "same can read scripts/open-risk.sh and execute constrained command scripts/open-risk.sh *", keys: []string{"edit", "scripts/open-risk.sh", "bash", "scripts/open-risk.sh *"}},
	}, result.Findings)
}

func TestLintReportsBadSuppressions(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeAgent(t, runtimeRoot, "bad.md", `---
description: bad
maxRecursion: -1 #nolint RC999: unknown
permission:
  task:
    "bad": allow #nolint:
---
bad
`)

	result, err := Lint(runtimeRoot, new(config.Config))
	require.NoError(t, err)
	assert.Equal(t, []Finding{
		{Code: rc000, Severity: "error", Path: "agents/bad.md", Message: "malformed #nolint comment requires optional code and non-empty reason"},
		{Code: rc000, Severity: "error", Path: "agents/bad.md", Message: "unknown #nolint code RC999"},
		{Code: rc003, Severity: "error", Path: "agents/bad.md", Message: "bad participates in a task delegation cycle without bounded maxRecursion", keys: []string{"maxRecursion", "task"}},
	}, result.Findings)
}

func TestAgentGraphDOTExpandsWildcardAndMarksCycles(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeAgent(t, runtimeRoot, "alpha.md", `---
description: alpha
maxRecursion: 0
permission:
  task:
    "hub": allow
---
alpha
`)
	writeAgent(t, runtimeRoot, "beta.md", `---
description: beta
maxRecursion: 2
guardrail: hub
customShell: /usr/local/bin/agent-sandbox
permission:
  task:
    "alpha": allow
---
beta
`)
	writeAgent(t, runtimeRoot, "hub.md", `---
description: hub
permission:
  task:
    "*": allow
    "beta": deny
---
hub
`)

	dot, err := AgentGraphDOT(runtimeRoot, new(config.Config))
	require.NoError(t, err)
	assert.Equal(t, `digraph agent_graph {
  "alpha" [label="alpha\nmaxRecursion=0"];
  "beta" [label="beta\nmaxRecursion=2\ncustomShell=/usr/local/bin/agent-sandbox"];
  "hub" [label="hub\nmaxRecursion=unbounded"];
  "alpha" -> "hub" [color="red", label="cycle"];
  "beta" -> "alpha";
  "hub" -> "alpha" [color="red", label="cycle"];
  "hub" -> "hub" [color="red", label="cycle"];
  "beta" -> "hub" [label="guardrail"];
}
`, dot)
}

func TestTaskEdgesSortsDestinations(t *testing.T) {
	infos := map[string]*agentInfo{}
	for _, name := range []string{"alpha", "beta", "delta", "epsilon", "gamma", "hub"} {
		infos[name] = &agentInfo{agent: rocketcode.Agent{Permission: rocketcode.PermissionSet{Buckets: []rocketcode.PermissionBucket{{
			Name:  "task",
			Rules: []rocketcode.PermissionRule{{Pattern: "*", Action: rocketcode.PermissionAllow}},
		}}}}}
	}

	for run := range 100 {
		for from, destinations := range taskEdges(infos) {
			if !slices.IsSorted(destinations) {
				t.Fatalf("run %d: taskEdges(%q) = %v, want sorted destinations", run, from, destinations)
			}
		}
	}
}

func TestLintResolvesModelTemplate(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeAgent(t, runtimeRoot, "main.md", "---\n---\nmain\n", `{{ model "coding-high" }}`)

	_, err := Lint(runtimeRoot, &config.Config{Models: map[string]string{"coding-high": "software-development-sol"}})
	require.NoError(t, err)

	_, err = Lint(runtimeRoot, new(config.Config))
	require.ErrorContains(t, err, `model "coding-high" is not configured`)

	dot, err := AgentGraphDOT(runtimeRoot, new(config.Config))
	require.ErrorContains(t, err, `model "coding-high" is not configured`)
	assert.Empty(t, dot)
}

func writeAgent(t *testing.T, runtimeRoot, name, content string, models ...string) {
	t.Helper()

	model := "gpt-6.1-sol"
	if len(models) > 0 {
		model = models[0]
	}

	content = fmt.Sprintf("---\nmodel: %q\n", model) + content[len("---\n"):]

	agentsRoot := filepath.Join(runtimeRoot, "agents")
	require.NoError(t, os.MkdirAll(agentsRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsRoot, name), []byte(content), 0o644))
}

func assertFindingCodes(t *testing.T, findings []Finding, codes ...string) {
	t.Helper()

	seen := map[string]bool{}
	for _, finding := range findings {
		seen[finding.Code] = true
	}

	for _, code := range codes {
		assert.Truef(t, seen[code], "missing finding code %s in %#v", code, findings)
	}
}
