package harness

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/stretchr/testify/require"
)

func mapFile(data string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(data), Mode: 0, ModTime: time.Time{}, Sys: nil}
}

func TestLoadSkills(t *testing.T) {
	t.Run("preserves body bytes after frontmatter", func(t *testing.T) {
		for _, tc := range []struct {
			name, input, content string
		}{
			{"unicode", "---\nname: example\ndescription: café\n---\nRésumé\n", "Résumé\n"},
			{"crlf", "---\r\nname: example\r\ndescription: Example\r\n---\r\nBody\r\n", "Body\r\n"},
			{"empty", "---\nname: example\ndescription: Example\n---", ""},
			{"raw bytes", "---\nname: example\ndescription: Example\n---\n\xff\x00", "\xff\x00"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				result := LoadSkills(fstest.MapFS{"example/SKILL.md": mapFile(tc.input)}, "skills")
				require.Empty(t, result.Errors)
				require.Equal(t, tc.content, result.Skills.Items["example"].Content)
			})
		}
	})

	t.Run("loads multiple valid skills from nested directories", func(t *testing.T) {
		fsys := fstest.MapFS{
			"git-release/SKILL.md": mapFile(`---
name: git-release
description: Create consistent releases and changelogs
license: MIT
compatibility: rocketcode
metadata:
  audience: maintainers
  workflow: github
---

## What I do
- Draft release notes
`),
			"docs-writer/SKILL.md": mapFile(`---
name: docs-writer
description: Write docs
---

Use for docs.
`),
			"docs-writer/scripts/example.sh": mapFile("#!/bin/sh\n"),
		}

		result := LoadSkills(fsys, "/tmp/skills")

		require.Empty(t, result.Errors)
		require.Equal(t, "/tmp/skills", result.Skills.Root)
		require.Len(t, result.Skills.Items, 2)
		require.Equal(t, []string{"docs-writer", "git-release"}, slices.Sorted(maps.Keys(result.Skills.Items)))
		require.Equal(t, []string{"docs-writer", "git-release"}, result.Skills.dirs)

		skill := result.Skills.Items["git-release"]
		require.Equal(t, "git-release", skill.Name)
		require.Equal(t, "Create consistent releases and changelogs", skill.Description)
		require.Equal(t, "MIT", skill.License)
		require.Equal(t, "rocketcode", skill.Compatibility)
		require.Equal(t, "git-release/SKILL.md", skill.Location)
		require.Equal(t, "\n## What I do\n- Draft release notes\n", skill.Content)
	})

	t.Run("loads files without frontmatter", func(t *testing.T) {
		benchtime := flag.Lookup("test.benchtime").Value.String()

		require.NoError(t, flag.Set("test.benchtime", "4x"))
		t.Cleanup(func() { require.NoError(t, flag.Set("test.benchtime", benchtime)) })

		content := "# Missing frontmatter\n" + strings.Repeat("x", 64<<10)
		fsys := fstest.MapFS{"bad-skill/SKILL.md": mapFile(content)}

		var result SkillLoadResult

		cost := testing.Benchmark(func(b *testing.B) {
			b.Helper()

			for b.Loop() {
				result = LoadSkills(fsys, "skills")
			}
		})

		require.Empty(t, result.Errors)
		require.Equal(t, []string{"bad-skill"}, result.Skills.dirs)
		require.Equal(t, Skill{Name: "bad-skill", DisplayName: "bad-skill", Location: "bad-skill/SKILL.md", Content: content}, result.Skills.Items["bad-skill"])
		require.Less(t, cost.AllocedBytesPerOp(), int64(len(content)*5/2))
	})

	t.Run("reports invalid YAML frontmatter", func(t *testing.T) {
		result := LoadSkills(fstest.MapFS{"bad-yaml/SKILL.md": mapFile("---\nname: bad-yaml\ndescription: [unterminated\n---\n")}, "/tmp/skills")

		require.Empty(t, result.Skills.Items)
		require.Len(t, result.Errors, 1)
		require.Contains(t, result.Errors[0].Error(), "bad-yaml/SKILL.md: parse YAML frontmatter:")
	})

	t.Run("takes the id from the path and the name as a label", func(t *testing.T) {
		fsys := fstest.MapFS{
			"missing-name/SKILL.md":        mapFile("---\ndescription: Missing name\n---\n"),
			"missing-description/SKILL.md": mapFile("---\nname: missing-description\n---\n"),
			"git-release/SKILL.md":         mapFile("---\nname: Git Release\n---\n"),
		}

		result := LoadSkills(fsys, "/tmp/skills")

		require.Empty(t, result.Errors)

		names := map[string]string{}

		for id, skill := range result.Skills.Items {
			require.Equal(t, id, skill.Name)
			names[id] = skill.DisplayName
		}

		require.Equal(t, map[string]string{"missing-name": "missing-name", "missing-description": "missing-description", "git-release": "Git Release"}, names)
	})

	t.Run("discovers skills as OpenCode does", func(t *testing.T) {
		fsys := fstest.MapFS{"notes.txt": mapFile("notes"), "nested/README.md": mapFile("readme")}
		for _, name := range []string{
			"review.md", "git-release/SKILL.md", "git-release/inner/SKILL.md", "teams/release/SKILL.md",
			".hidden/skill/SKILL.md", "node_modules/skill/SKILL.md", "teams/.private/SKILL.md",
			"deep/1/2/3/4/5/SKILL.md", "deeper/1/2/3/4/5/6/SKILL.md",
		} {
			fsys[name] = mapFile("---\ndescription: " + name + "\n---\n")
		}

		result := LoadSkills(fsys, "/tmp/skills")

		require.Empty(t, result.Errors)

		locations := map[string]string{}
		for id, skill := range result.Skills.Items {
			locations[id] = skill.Location
		}

		require.Equal(t, map[string]string{"review": "review.md", "git-release": "git-release/SKILL.md", "release": "teams/release/SKILL.md", "5": "deep/1/2/3/4/5/SKILL.md"}, locations)
		require.Equal(t, []string{"deep/1/2/3/4/5", "git-release", "teams/release"}, result.Skills.dirs)

		root := LoadSkills(fstest.MapFS{"SKILL.md": mapFile("root"), "inner/SKILL.md": mapFile("inner"), "flat.md": mapFile("flat")}, "/tmp/skills")
		require.Equal(t, []string{"skills"}, slices.Collect(maps.Keys(root.Skills.Items)))
		require.Equal(t, "root", root.Skills.Items["skills"].Content)
	})

	t.Run("retries frontmatter values with unquoted colons", func(t *testing.T) {
		result := LoadSkills(fstest.MapFS{"example/SKILL.md": mapFile("---\r\nname: Example: One\r\ndescription: Use when: releasing\r\nlicense: 'MIT: yes'\r\n---\r\nBody\r\n")}, "/tmp/skills")
		require.Empty(t, result.Errors)
		require.Equal(t, Skill{Name: "example", DisplayName: "Example: One", Description: "Use when: releasing", License: "MIT: yes", Location: "example/SKILL.md", Content: "Body\r\n"}, result.Skills.Items["example"])

		result = LoadSkills(fstest.MapFS{"example/SKILL.md": mapFile("---\nname: Example: One\nmetadata:\n  note: a: b\n---\n")}, "/tmp/skills")
		require.Len(t, result.Errors, 1)
		require.ErrorContains(t, result.Errors[0], "example/SKILL.md: parse YAML frontmatter:")
	})

	t.Run("reads model invocation flags", func(t *testing.T) {
		for frontmatter, want := range map[string]bool{
			"":                                       false,
			"disable-model-invocation: true":         true,
			"disable-model-invocation: 'Yes '":       true,
			"disable-model-invocation: 1":            true,
			"disable-model-invocation: 0":            false,
			"disable-model-invocation: maybe":        false,
			"metadata: {opencode/autoinvoke: false}": true,
			"metadata: {opencode/autoinvoke: off}":   true,
			"metadata: {opencode/autoinvoke: 2}":     false,
			"metadata: {opencode/autoinvoke: true}\ndisable-model-invocation: true": false,
			"metadata: {opencode/autoinvoke: 0}\ndisable-model-invocation: false":   true,
		} {
			result := LoadSkills(fstest.MapFS{"example/SKILL.md": mapFile("---\nname: example\n" + frontmatter + "\n---\n")}, "/tmp/skills")
			require.Empty(t, result.Errors, frontmatter)
			require.Equal(t, want, result.Skills.Items["example"].DisableModelInvocation, frontmatter)
		}
	})

	t.Run("reports duplicate skill names and keeps the last discovered skill", func(t *testing.T) {
		fsys := fstest.MapFS{
			"aaa/dupe/SKILL.md": mapFile("---\nname: dupe\ndescription: First\n---\nfirst\n"),
			"zzz/dupe/SKILL.md": mapFile("---\nname: dupe\ndescription: Second\n---\nsecond\n"),
		}

		result := LoadSkills(fsys, "/tmp/skills")

		require.Len(t, result.Skills.Items, 1)
		require.Equal(t, "zzz/dupe/SKILL.md", result.Skills.Items["dupe"].Location)
		require.Equal(t, "Second", result.Skills.Items["dupe"].Description)
		require.Len(t, result.Errors, 1)
		require.Equal(t, `zzz/dupe/SKILL.md: duplicate skill id "dupe" overrides aaa/dupe/SKILL.md`, result.Errors[0].Error())
	})

	t.Run("continues loading valid skills when some are invalid", func(t *testing.T) {
		fsys := fstest.MapFS{
			"good-skill/SKILL.md": mapFile("---\nname: good-skill\ndescription: Valid\n---\nready\n"),
			"bad-skill/SKILL.md":  mapFile("---\nname: [unterminated\n---\n"),
		}

		result := LoadSkills(fsys, "/tmp/skills")

		require.Len(t, result.Skills.Items, 1)
		require.Equal(t, "ready\n", result.Skills.Items["good-skill"].Content)
		require.Len(t, result.Errors, 1)
		require.ErrorContains(t, result.Errors[0], "bad-skill/SKILL.md: parse YAML frontmatter:")
	})
}

func TestSkillDescriptions(t *testing.T) {
	skills := Skills{Root: "/tmp/skills", Items: map[string]Skill{
		"docs-helper": {Name: "docs-helper", DisplayName: "Docs Helper", Description: "Write docs", Location: "docs-helper/SKILL.md"},
		"git-review":  {Name: "git-review", DisplayName: "git-review", Description: "Review git changes", Location: "git-review/SKILL.md"},
	}, dirs: nil, fsys: nil}

	t.Run("tool description does not list skills", func(t *testing.T) {
		factory := testSkillFactory(t, skills, agentWithSkillPermission())

		require.Equal(t, "Load a specialized skill's instructions and resources into the current conversation when the task at hand matches its description.\n\n"+
			"The skill ID must match an available skill or a skill explicitly referenced by the user.", factory.skillTool().Definition.Description.Value)
	})

	t.Run("system prompt lists advertised skills", func(t *testing.T) {
		require.Equal(t, strings.Join([]string{
			"base prompt",
			"",
			"Skills provide specialized instructions and workflows for specific tasks.",
			"Use the skill tool to load a skill when a task matches its description.",
			"The user may also invoke a skill directly. When that happens, its instructions appear in the conversation as a <skill_content> block, the same shape the skill tool returns. A skill that is already present this way does not need to be invoked again.",
			"<available_skills>",
			"  <skill>",
			"    <id>docs-helper</id>",
			"    <name>Docs Helper</name>",
			"    <description>Write docs</description>",
			"  </skill>",
			"  <skill>",
			"    <id>git-review</id>",
			"    <name>git-review</name>",
			"    <description>Review git changes</description>",
			"  </skill>",
			"</available_skills>",
			"",
			"## Skill Permissions",
			"",
			"Rules are listed in order. The last matching rule wins.",
			"",
			"- Allow `*`.",
			"",
			"## Permission Wildcard Rules",
			"",
			"- `*` matches any text.",
			"- `?` matches any single character.",
			"- Path separators are normalized to `/`.",
			"- A pattern ending in space-star, like `git status *`, also matches the command without extra arguments.",
		}, "\n"), composeSystemPromptWithSkills("base prompt", skills, agentWithSkillPermission()))
	})

	t.Run("system prompt filters denied skills", func(t *testing.T) {
		prompt := composeSystemPromptWithSkills("base prompt", skills, agentWithSkillRules(
			PermissionRule{Pattern: "*", Action: permissionDeny},
			PermissionRule{Pattern: "docs-helper", Action: permissionAllow},
		))

		require.Contains(t, prompt, "<id>docs-helper</id>")
		require.NotContains(t, prompt, "git-review")
	})

	t.Run("system prompt omits skills when none are advertised", func(t *testing.T) {
		prompt := composeSystemPromptWithSkills("base prompt", skills, agentWithSkillRules(PermissionRule{Pattern: "*", Action: permissionDeny}))

		require.NotContains(t, prompt, "Skills provide")
		require.NotContains(t, prompt, "<available_skills>")
	})
}

func TestSkillsAvailable(t *testing.T) {
	skills := Skills{Items: map[string]Skill{
		"z-last":   {Name: "z-last", Description: "Last"},
		"a-first":  {Name: "a-first", Description: "First"},
		"a-denied": {Name: "a-denied", Description: "Denied"},
		"a-review": {Name: "a-review", Description: "Review"},
		"b-silent": {Name: "b-silent"},
		"b-manual": {Name: "b-manual", Description: "Manual", DisableModelInvocation: true},
	}}
	agent := agentWithSkillRules(
		PermissionRule{Pattern: "*", Action: permissionDeny},
		PermissionRule{Pattern: "?-*", Action: permissionAllow},
		PermissionRule{Pattern: "*-denied", Action: permissionDeny},
		PermissionRule{Pattern: "*-review", Action: permissionAuto},
	)

	require.Equal(t, []Skill{skills.Items["a-first"], skills.Items["b-manual"], skills.Items["b-silent"], skills.Items["z-last"]}, skills.Available(agent))
	require.Equal(t, "## Matching skills\n- **a-first**: First\n- **a-review**: Review\n- **z-last**: Last", skills.FindAvailable("", agent))
	require.Equal(t, []Skill{skills.Items["a-first"], skills.Items["a-review"], skills.Items["z-last"]}, skills.advertised(agent))
	require.Empty(t, skills.Available(agentWithSkillRules(PermissionRule{Pattern: "*", Action: permissionDeny})))

	ordered := Skills{Items: map[string]Skill{}}
	for _, id := range []string{"Zeta", "beta-2", "a1", "alpha", "beta", "Beta", "éclair", "a_b"} {
		ordered.Items[id] = Skill{Name: id}
	}

	ids := make([]string, 0, len(ordered.Items))
	for _, skill := range ordered.Available(agentWithSkillPermission()) {
		ids = append(ids, skill.Name)
	}

	require.Equal(t, []string{"a_b", "a1", "alpha", "beta", "Beta", "beta-2", "éclair", "Zeta"}, ids)
}

func TestPermissionPrompt(t *testing.T) {
	t.Run("escapes rule patterns and reviewer names", func(t *testing.T) {
		prompt := PermissionPrompt(PermissionSet{Buckets: []PermissionBucket{{Name: "bash", Rules: []PermissionRule{{Pattern: "deploy\r\n`tag`\\", Action: permissionAuto, Reviewer: "review\r\n`agent`\\"}}}}})
		require.Contains(t, prompt, "- Require automatic approval by `review  \\`agent\\`\\` for `deploy  \\`tag\\`\\`.")
	})
	t.Run("omits generated session tag authority", func(t *testing.T) {
		permissions := parsePermissionYAML(t, "rocketclaw_tags: {rocketclaw_set_tag: deny, rocketclaw_get_tags: allow}\nread: {README.md: allow}")
		prompt := PermissionPrompt(permissions)
		require.NotContains(t, prompt, "rocketclaw_set_tag")
		require.NotContains(t, prompt, "rocketclaw_get_tags")
		require.Contains(t, prompt, "README.md")
	})
	t.Run("renders full bash allow", func(t *testing.T) {
		prompt := composeSystemPromptWithSkills("base prompt", emptySkills(), testAgentWithPermission(PermissionSet{Buckets: []PermissionBucket{{Name: "bash", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}))

		require.Contains(t, prompt, "## Bash Permissions\n\nRules are listed in order. The last matching rule wins.\n\n- Allow `*`.")
		require.Contains(t, prompt, "## Permission Wildcard Rules")
		require.NotContains(t, prompt, "{Name:bash")
	})

	t.Run("renders explicit allow list for each bucket", func(t *testing.T) {
		prompt := composeSystemPromptWithSkills("base prompt", emptySkills(), testAgentWithPermission(PermissionSet{Buckets: []PermissionBucket{
			{Name: "read", Rules: []PermissionRule{{Pattern: "README.md", Action: permissionAllow}}},
			{Name: "bash", Rules: []PermissionRule{{Pattern: "git status", Action: permissionAllow}, {Pattern: "git diff *", Action: permissionAllow}}},
		}}))

		require.Contains(t, prompt, "## Read Permissions")
		require.Contains(t, prompt, "- Allow `README.md`.")
		require.Contains(t, prompt, "## Bash Permissions")
		require.Contains(t, prompt, "- Allow `git status`.\n- Allow `git diff *`.")
	})

	t.Run("renders mixed allow and auto permissions", func(t *testing.T) {
		prompt := composeSystemPromptWithSkills("base prompt", emptySkills(), testAgentWithPermission(PermissionSet{Buckets: []PermissionBucket{{Name: "bash", Rules: []PermissionRule{{Pattern: "git status", Action: permissionAllow}, {Pattern: "deploy *", Action: permissionAuto}}}}}))

		require.Contains(t, prompt, "- Allow `git status`.\n- Require automatic approval for `deploy *`.")
	})

	t.Run("omits block without effective bash allow", func(t *testing.T) {
		prompt := composeSystemPromptWithSkills("base prompt", emptySkills(), testAgentWithPermission(PermissionSet{Buckets: []PermissionBucket{{Name: "*", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}))

		require.NotContains(t, prompt, "## Bash Permissions")
		require.NotContains(t, prompt, "## Permission Wildcard Rules")
	})

	t.Run("renders rule exceptions and sudo MCP limits", func(t *testing.T) {
		prompt := PermissionPrompt(PermissionSet{Sudo: true, Buckets: []PermissionBucket{{Name: "mcp", Rules: []PermissionRule{
			{Pattern: "*", Action: permissionDeny},
			{Pattern: "s.*", Action: permissionAuto, Except: []string{"s.delete_*", "s.get_secret"}},
		}}}})
		require.Contains(t, prompt, "## Mcp Permissions\n\nRules are listed in order. The last matching rule wins.\n\n- Deny `*`.\n- Require automatic approval for `s.*` except `s.delete_*`, `s.get_secret`.")
		require.Contains(t, prompt, "## Sudo Mode\n\nEvery tool is available except outside MCP tools, which follow the Mcp rules above. Any call no rule above covers requires automatic approval.")
	})

	t.Run("preserves deny then allow exception", func(t *testing.T) {
		permissions := parsePermissionYAML(t, "edit: {'*': allow, '.tmp/*': deny, '.tmp/banana/note.txt': allow}\nbash: deny")
		prompt := PermissionPrompt(permissions)
		require.Contains(t, prompt, "## Edit Permissions\n\nRules are listed in order. The last matching rule wins.\n\n- Allow `*`.\n- Deny `.tmp/*`.\n- Allow `.tmp/banana/note.txt`.")
		require.Contains(t, prompt, "## Bash Permissions\n\nRules are listed in order. The last matching rule wins.\n\n- Deny `*`.")
	})
}

func TestSystemPromptWorkingDirectory(t *testing.T) {
	agent := Agent{CustomShell: CustomShell{Emulated: true, Dir: "/srv/agent"}}
	require.Equal(t, "base prompt\n\n## Working Directory\n\nYour working directory is `/srv/agent`. Shell commands start there and relative paths resolve from it. "+
		"It is the only folder where changes persist: `/tmp` is cleared after every command and every other folder is read-only.", composeSystemPromptWithSkills("base prompt", emptySkills(), &agent))

	wrapped := Agent{CustomShell: CustomShell{Path: "/usr/local/bin/wrap", Dir: "/srv/agent"}}
	require.Equal(t, "base prompt\n\n## Working Directory\n\nYour working directory is `/srv/agent`. Shell commands start there and relative paths resolve from it.",
		composeSystemPromptWithSkills("base prompt", emptySkills(), &wrapped))
	require.Equal(t, "base prompt", composeSystemPromptWithSkills("base prompt", emptySkills(), &Agent{}))
}

func TestFindSkillsTool(t *testing.T) {
	skills := Skills{Root: "/tmp/skills", Items: map[string]Skill{
		"docs-helper": {Name: "docs-helper", Description: "Write docs", License: "", Compatibility: "", Location: "docs-helper/SKILL.md", Content: "documentation guides"},
		"git-review":  {Name: "git-review", Description: "Review git changes", License: "", Compatibility: "", Location: "git-review/SKILL.md", Content: "git status diff"},
	}, dirs: nil, fsys: nil}

	t.Run("finds explicitly allowed skills not presented in system prompt", func(t *testing.T) {
		factory := testSkillFactory(t, skills, agentWithSkillPermission())
		tool := factory.findSkillsTool()

		got, err := tool.Call(context.Background(), json.RawMessage(`{"query":"git"}`), nil, emptyToolCallMetadata())

		require.NoError(t, err)
		require.Equal(t, "## Matching skills\n- **git-review**: Review git changes", got.Output)
	})

	t.Run("excludes permission denied skills", func(t *testing.T) {
		factory := testSkillFactory(t, skills, agentWithSkillRules(
			PermissionRule{Pattern: "*", Action: permissionDeny},
			PermissionRule{Pattern: "docs-helper", Action: permissionAllow},
		))
		tool := factory.findSkillsTool()

		got, err := tool.Call(context.Background(), json.RawMessage(`{"query":"git"}`), nil, emptyToolCallMetadata())

		require.NoError(t, err)
		require.Equal(t, "No matching skills found.", got.Output)
	})
}

func TestFindSkillsToolVisibilityFollowsSkillPermission(t *testing.T) {
	skills := Skills{Root: "/tmp/skills", Items: map[string]Skill{
		"docs-helper": {Name: "docs-helper", Description: "Write docs", License: "", Compatibility: "", Location: "docs-helper/SKILL.md", Content: ""},
	}, dirs: nil, fsys: nil}

	t.Run("hidden when all skills are denied", func(t *testing.T) {
		factory := testSkillFactory(t, skills, agentWithSkillPermission())
		agent := agentWithSkillRules(PermissionRule{Pattern: "*", Action: permissionDeny})

		tools, _ := factory.assembleTools(agent)

		require.NotContains(t, tools, "find_skills")
	})

	t.Run("visible when any skill is allowed", func(t *testing.T) {
		factory := testSkillFactory(t, skills, agentWithSkillPermission())
		agent := agentWithSkillRules(
			PermissionRule{Pattern: "*", Action: permissionDeny},
			PermissionRule{Pattern: "docs-helper", Action: permissionAllow},
		)

		tools, _ := factory.assembleTools(agent)

		require.Contains(t, tools, "find_skills")
	})
}

func TestSkillsFind(t *testing.T) {
	loaded := LoadSkills(fstest.MapFS{
		"git-release/SKILL.md": mapFile(`---
name: git-release
description: Create consistent releases and changelogs
---

Draft release notes from merged PRs.
`),
		"release-notes/SKILL.md": mapFile(`---
name: release-notes
description: Summarize merged PRs
---

Create release notes for a new tag.
`),
		"docs-writer/SKILL.md": mapFile(`---
name: docs-writer
description: Write product documentation
---

Help with guides and docs. École １２.
`),
	}, "/tmp/skills").Skills

	agent := agentWithSkillPermission()

	t.Run("returns all skills when query is empty", func(t *testing.T) {
		require.Equal(t, strings.Join([]string{
			"## Matching skills",
			"- **docs-writer**: Write product documentation",
			"- **git-release**: Create consistent releases and changelogs",
			"- **release-notes**: Summarize merged PRs",
		}, "\n"), loaded.FindAvailable("   ", agent))
	})

	t.Run("exact name match ranks first case insensitively", func(t *testing.T) {
		got := loaded.FindAvailable("GIT-RELEASE", agent)
		lines := strings.Split(got, "\n")
		require.Equal(t, "## Matching skills", lines[0])
		require.Equal(t, "- **git-release**: Create consistent releases and changelogs", lines[1])
	})

	t.Run("returns all matching skills in ranked order", func(t *testing.T) {
		skills := Skills{Items: map[string]Skill{"git-release": loaded.Items["git-release"], "release-notes": loaded.Items["release-notes"]}}
		for _, name := range []string{"all-release", "big-release", "z-release"} {
			skill := loaded.Items["git-release"]
			skill.Name = name
			skills.Items[name] = skill
		}

		skills.Items["tag"] = Skill{Name: "tag", Description: "Summarize releases"}

		require.Equal(t, strings.Join([]string{
			"## Matching skills",
			"- **z-release**: Create consistent releases and changelogs",
			"- **all-release**: Create consistent releases and changelogs",
			"- **big-release**: Create consistent releases and changelogs",
			"- **git-release**: Create consistent releases and changelogs",
			"- **release-notes**: Summarize merged PRs",
			"- **tag**: Summarize releases",
		}, "\n"), skills.FindAvailable("release", agent))
	})

	t.Run("matches description and content case insensitively", func(t *testing.T) {
		for _, query := range []string{"GUIDES", " --GUIDES///DOCS?! ", " ÉCOLE---１２ ", "GUIDES\xffDOCS"} {
			require.Equal(t, "## Matching skills"+"\n"+"- **docs-writer**: Write product documentation", loaded.FindAvailable(query, agent), "query %q", query)
		}
	})
}

func TestSkillsRender(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "git-release", "scripts"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "git-release", "reference"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "git-release", "SKILL.md"), []byte(`---
name: Git Release
description: Create consistent releases and changelogs
license: MIT
compatibility: rocketcode
metadata:
  audience: maintainers
---

## What I do
- Draft release notes
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "git-release", "scripts", "demo.sh"), []byte("#!/bin/sh\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "git-release", "reference", "notes.md"), []byte("notes\n"), 0o644))

	for i := range 12 {
		name := filepath.Join(root, "git-release", "reference", fmt.Sprintf("file-%02d.txt", i))
		require.NoError(t, os.WriteFile(name, []byte("x"), 0o644))
	}

	loaded := LoadSkills(os.DirFS(root), root).Skills

	t.Run("renders skill content with sampled file list", func(t *testing.T) {
		factory := testSkillFactory(t, loaded, agentWithSkillPermission())
		result, err := factory.skillTool().Call(context.Background(), json.RawMessage(`{"id":"git-release"}`), nil, emptyToolCallMetadata())
		require.NoError(t, err)

		files := make([]string, 0, 10)
		for i := range 10 {
			files = append(files, fmt.Sprintf("<file>%s/git-release/reference/file-%02d.txt</file>", root, i))
		}

		require.Equal(t, strings.Join(slices.Concat([]string{
			`<skill_content name="Git Release">`,
			"# Skill: Git Release",
			"",
			"## What I do",
			"- Draft release notes",
			"",
			"Base directory for this skill: " + root + "/git-release",
			"Relative paths in this skill (e.g., scripts/, reference/) are relative to this base directory.",
			"Note: file list is sampled.",
			"",
			"<skill_files>",
		}, files, []string{"</skill_files>", "</skill_content>"}), "\n"), result.Output)
	})

	t.Run("returns an error for unknown skills", func(t *testing.T) {
		factory := testSkillFactory(t, loaded, agentWithSkillPermission())
		_, err := factory.skillTool().Call(context.Background(), json.RawMessage(`{"id":"missing-skill"}`), nil, emptyToolCallMetadata())
		require.EqualError(t, err, "Unable to load skill missing-skill")
	})
}

func TestSkillsRenderUsesLoadedFS(t *testing.T) {
	loaded := LoadSkills(fstest.MapFS{
		"docs-helper/SKILL.md": mapFile(`---
name: docs-helper
description: Write docs
disable-model-invocation: true
---

Use for docs.
`),
		"docs-helper/reference/guide.md": mapFile("guide\n"),
		"docs-helper/reference/link.md":  {Data: []byte("guide.md"), Mode: fs.ModeSymlink, ModTime: time.Time{}, Sys: nil},
		"docs-helper/node_modules/x.js":  mapFile("x"),
		"docs-helper/.env":               mapFile("hidden"),
		"review.md":                      mapFile("Review it."),
	}, "/virtual/skills").Skills

	factory := testSkillFactory(t, loaded, agentWithSkillPermission())
	result, err := factory.skillTool().Call(context.Background(), json.RawMessage(`{"id":"docs-helper"}`), nil, emptyToolCallMetadata())
	require.NoError(t, err)

	got := result.Output
	require.Contains(t, got, "<file>/virtual/skills/docs-helper/reference/guide.md</file>")
	require.Contains(t, got, "<file>/virtual/skills/docs-helper/.env</file>")
	require.NotContains(t, got, "link.md")
	require.NotContains(t, got, "node_modules")

	result, err = factory.skillTool().Call(context.Background(), json.RawMessage(`{"id":"review"}`), nil, emptyToolCallMetadata())
	require.NoError(t, err)
	require.Equal(t, "<skill_content name=\"review\">\n# Skill: review\n\nReview it.\n\nBase directory for this skill: /virtual/skills\n"+
		"Relative paths in this skill (e.g., scripts/, reference/) are relative to this base directory.\nNote: file list is sampled.\n\n<skill_files>\n</skill_files>\n</skill_content>", result.Output)
}

func TestSkillsRenderRequiresLoadedFS(t *testing.T) {
	skills := Skills{Root: "/virtual/skills", Items: map[string]Skill{"docs-helper": {Name: "docs-helper", Description: "Write docs", License: "", Compatibility: "", Location: "docs-helper/SKILL.md", Content: "Use for docs."}}, dirs: []string{"docs-helper"}, fsys: nil}
	factory := testSkillFactory(t, skills, agentWithSkillPermission())

	_, err := factory.skillTool().Call(context.Background(), json.RawMessage(`{"id":"docs-helper"}`), nil, emptyToolCallMetadata())

	require.EqualError(t, err, "skills filesystem is not available")
}

func TestSkillsRenderArguments(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.Mkdir("arg-skill", 0o755))
	require.NoError(t, root.WriteFile("SECRET.md", []byte("secret-output"), 0o644))
	require.NoError(t, root.WriteFile(filepath.Join("arg-skill", "SKILL.md"), []byte(`---
name: arg-skill
description: Uses arguments
---

Generated: !`+"`"+`cat SECRET.md`+"`"+`
Args: $ARGUMENTS
`), 0o644))

	loaded := LoadSkills(os.DirFS(dir), dir).Skills
	env, err := newPromptExpansionEnvironment(root, testPromptShellTempConfig(t, root, dir), nil, &sandbox.Runtime{})
	require.NoError(t, err)

	factory := testSkillFactory(t, loaded, agentWithSkillPermission())
	factory.expandPromptShellCommands = PromptShellCommandExpansion{SkillPrompts: true}
	factory.promptExpansion = env

	result, err := factory.skillTool().Call(context.Background(), json.RawMessage(`{"id":"arg-skill","arguments":"!`+"`"+`cat SECRET.md`+"`"+`","direct":true}`), nil, emptyToolCallMetadata())
	require.NoError(t, err)

	require.Contains(t, result.Output, "Generated: secret-output")
	require.Contains(t, result.Output, "Args: secret-output")
	require.Equal(t, 2, strings.Count(result.Output, "secret-output"))

	result, err = factory.skillTool().Call(context.Background(), json.RawMessage(`{"id":"arg-skill"}`), nil, emptyToolCallMetadata())
	require.NoError(t, err)
	require.Contains(t, result.Output, "Args: $ARGUMENTS\n")

	result, err = factory.skillTool().Call(context.Background(), json.RawMessage(`{"id":"arg-skill","arguments":"appended","direct":true}`), nil, emptyToolCallMetadata())
	require.NoError(t, err)
	require.Contains(t, result.Output, "Args: appended")
}

func TestSkillsRenderDirectArgumentsAppendWhenUnused(t *testing.T) {
	loaded := LoadSkills(fstest.MapFS{"arg-skill/SKILL.md": mapFile(`---
name: arg-skill
description: $ARGUMENTS and $1 are metadata
---

No placeholder here.
`), "arg-skill/$ARGUMENTS.txt": mapFile("resource")}, "/virtual/skills").Skills
	factory := testSkillFactory(t, loaded, agentWithSkillPermission())

	output, err := factory.renderSkillToolOutput(t.Context(), skillToolParams{ID: "arg-skill", Arguments: "literal request", Direct: true})
	require.NoError(t, err)
	require.Contains(t, output, "No placeholder here.\n\nliteral request\n\nBase directory")
	require.Contains(t, output, "<file>/virtual/skills/arg-skill/$ARGUMENTS.txt</file>")
}

func TestSkillsRenderShellCommands(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.Mkdir("dynamic-skill", 0o755))
	require.NoError(t, root.WriteFile("MEMORY.md", []byte("dynamic-output"), 0o644))
	require.NoError(t, root.WriteFile(filepath.Join("dynamic-skill", "SKILL.md"), []byte(`---
name: dynamic-skill
description: Expands shell snippets
---

Generated: !`+"`"+`cat '$1'; printf ran > EFFECT`+"`"+`
`), 0o644))

	loaded := LoadSkills(os.DirFS(dir), dir).Skills
	require.Contains(t, loaded.Items["dynamic-skill"].Content, "!`cat '$1'; printf ran > EFFECT`")
	t.Run("converter instructions survive real skill loading", func(t *testing.T) {
		data, err := os.ReadFile("../skel/.rocketclaw/skills/main-convert-agent-to-easy/SKILL.md")
		require.NoError(t, err)
		require.NoError(t, root.Mkdir("main-convert-agent-to-easy", 0o755))
		require.NoError(t, root.WriteFile("main-convert-agent-to-easy/SKILL.md", data, 0o644))
		skills := LoadSkills(root.FS(), dir).Skills
		factory := testSkillFactory(t, skills, agentWithSkillPermission())
		factory.expandPromptShellCommands = PromptShellCommandExpansion{SkillPrompts: true}
		factory.promptExpansion, err = newPromptExpansionEnvironment(root, testPromptShellTempConfig(t, root, dir), nil, &sandbox.Runtime{})
		require.NoError(t, err)
		result, err := factory.skillTool().Call(t.Context(), json.RawMessage(`{"id":"main-convert-agent-to-easy"}`), nil, emptyToolCallMetadata())
		require.NoError(t, err)
		require.Contains(t, result.Output, strings.TrimSpace(skills.Items["main-convert-agent-to-easy"].Content))
		require.NotContains(t, result.Output, "dynamic-output")
	})

	t.Run("leaves shell commands literal by default", func(t *testing.T) {
		factory := testSkillFactory(t, loaded, agentWithSkillPermission())
		result, err := factory.skillTool().Call(context.Background(), json.RawMessage(`{"id":"dynamic-skill","arguments":"MEMORY.md","direct":true}`), nil, emptyToolCallMetadata())

		require.NoError(t, err)

		got := result.Output
		require.Contains(t, got, "Generated: !`cat 'MEMORY.md'; printf ran > EFFECT`")

		_, err = root.Stat("EFFECT")
		require.ErrorIs(t, err, fs.ErrNotExist)
	})

	t.Run("skill tool expands shell commands in runtime root", func(t *testing.T) {
		env, err := newPromptExpansionEnvironment(root, testPromptShellTempConfig(t, root, dir), nil, &sandbox.Runtime{})
		require.NoError(t, err)

		factory := testSkillFactory(t, loaded, agentWithSkillPermission())
		factory.expandPromptShellCommands = PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: true}
		factory.promptExpansion = env
		tool := factory.skillTool()

		got, err := tool.Call(context.Background(), json.RawMessage(`{"id":"dynamic-skill","arguments":"MEMORY.md","direct":true}`), nil, emptyToolCallMetadata())

		require.NoError(t, err)
		require.Contains(t, got.Output, "Generated: dynamic-output")
		require.NotContains(t, got.Output, "!`cat 'MEMORY.md'; printf ran > EFFECT`")

		effect, err := root.ReadFile("EFFECT")
		require.NoError(t, err)
		require.Equal(t, "ran", string(effect))
	})

	t.Run("skill expands through the loading agent shell", func(t *testing.T) {
		env, err := newPromptExpansionEnvironment(root, testPromptShellTempConfig(t, root, dir), nil, &sandbox.Runtime{})
		require.NoError(t, err)

		shell := writeCustomShell(t, root)
		boxed := agentWithSkillPermission()
		boxed.CustomShell = CustomShell{Path: shell}

		for _, tc := range []struct {
			name  string
			agent *Agent
			want  string
		}{
			{name: "sandboxed", agent: boxed, want: "Generated: custom:1:cat 'MEMORY.md'; printf ran > EFFECT\n"},
			{name: "default", agent: agentWithSkillPermission(), want: "Generated: dynamic-output\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				factory := testSkillFactory(t, loaded, tc.agent)
				factory.expandPromptShellCommands = PromptShellCommandExpansion{SkillPrompts: true}
				factory.promptExpansion = env
				factory.promptExpansion.customShell = CustomShell{Path: shell}

				model, _ := factory.assembleTools(tc.agent)
				got, err := model["skill"].Call(context.Background(), json.RawMessage(`{"id":"dynamic-skill","arguments":"MEMORY.md","direct":true}`), nil, emptyToolCallMetadata())

				require.NoError(t, err)
				require.Contains(t, got.Output, tc.want)
			})
		}
	})
}

func agentWithSkillPermission() *Agent {
	return agentWithSkillRules(PermissionRule{Pattern: "*", Action: permissionAllow})
}

func agentWithSkillRules(rules ...PermissionRule) *Agent {
	return testAgentWithPermission(PermissionSet{Buckets: []PermissionBucket{{Name: "skill", Rules: rules}}})
}

func emptySkills() Skills {
	return Skills{Root: "", Items: nil, dirs: nil, fsys: nil}
}

func testSkillFactory(t *testing.T, skills Skills, agent *Agent) *toolFactory {
	t.Helper()

	var factory toolFactory

	factory.promptExpansion = testPromptExpansionEnvironment(t)
	factory.agent = agent
	factory.agents = Agents{Items: nil}
	factory.skills = skills

	return &factory
}

type failingFS struct{}

func (failingFS) Open(_ string) (fs.File, error) {
	return nil, fs.ErrPermission
}

func TestLoadSkillsWalkError(t *testing.T) {
	result := LoadSkills(failingFS{}, "/tmp/skills")

	require.Empty(t, result.Skills.Items)
	require.Len(t, result.Errors, 1)
	require.True(t, strings.HasPrefix(result.Errors[0].Error(), `walk ".": permission denied`))
}
