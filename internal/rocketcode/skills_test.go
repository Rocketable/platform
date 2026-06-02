//nolint:exhaustruct // Test fixtures intentionally use sparse app literals.
package rocketcode

import (
	"context"
	"encoding/json"
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

	"github.com/stretchr/testify/require"
)

func mapFile(data string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(data), Mode: 0, ModTime: time.Time{}, Sys: nil}
}

func TestLoadSkills(t *testing.T) {
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
		require.Equal(t, []string{"docs-writer", "git-release"}, result.Skills.Dirs)

		skill := result.Skills.Items["git-release"]
		require.Equal(t, "git-release", skill.Name)
		require.Equal(t, "Create consistent releases and changelogs", skill.Description)
		require.Equal(t, "MIT", skill.License)
		require.Equal(t, "rocketcode", skill.Compatibility)
		require.Equal(t, map[string]any{"audience": "maintainers", "workflow": "github"}, skill.Metadata)
		require.Equal(t, "git-release/SKILL.md", skill.Location)
		require.Equal(t, "\n## What I do\n- Draft release notes\n", skill.Content)
	})

	t.Run("reports files missing frontmatter", func(t *testing.T) {
		result := LoadSkills(fstest.MapFS{"bad-skill/SKILL.md": mapFile("# Missing frontmatter\n")}, "/tmp/skills")

		require.Empty(t, result.Skills.Items)
		require.Equal(t, []string{"bad-skill"}, result.Skills.Dirs)
		require.Len(t, result.Errors, 1)
		require.Equal(t, `bad-skill/SKILL.md: missing YAML frontmatter`, result.Errors[0].Error())
	})

	t.Run("reports invalid YAML frontmatter", func(t *testing.T) {
		result := LoadSkills(fstest.MapFS{"bad-yaml/SKILL.md": mapFile("---\nname: bad-yaml\ndescription: [unterminated\n---\n")}, "/tmp/skills")

		require.Empty(t, result.Skills.Items)
		require.Len(t, result.Errors, 1)
		require.Contains(t, result.Errors[0].Error(), "bad-yaml/SKILL.md: parse YAML frontmatter:")
	})

	t.Run("reports missing required fields", func(t *testing.T) {
		fsys := fstest.MapFS{
			"missing-name/SKILL.md":        mapFile("---\ndescription: Missing name\n---\n"),
			"missing-description/SKILL.md": mapFile("---\nname: missing-description\n---\n"),
		}

		result := LoadSkills(fsys, "/tmp/skills")

		require.Empty(t, result.Skills.Items)
		require.Len(t, result.Errors, 2)
		require.Equal(t, `missing-description/SKILL.md: missing required frontmatter field "description"`, result.Errors[0].Error())
		require.Equal(t, `missing-name/SKILL.md: missing required frontmatter field "name"`, result.Errors[1].Error())
	})

	t.Run("enforces skill name rules", func(t *testing.T) {
		fsys := fstest.MapFS{
			"BadName/SKILL.md":      mapFile("---\nname: BadName\ndescription: Invalid\n---\n"),
			"mismatch-dir/SKILL.md": mapFile("---\nname: different-name\ndescription: Invalid\n---\n"),
		}

		result := LoadSkills(fsys, "/tmp/skills")

		require.Empty(t, result.Skills.Items)
		require.Len(t, result.Errors, 2)
		require.Equal(t, `BadName/SKILL.md: invalid skill name "BadName"`, result.Errors[0].Error())
		require.Equal(t, `mismatch-dir/SKILL.md: skill name "different-name" does not match directory "mismatch-dir"`, result.Errors[1].Error())
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
		require.Equal(t, `zzz/dupe/SKILL.md: duplicate skill name "dupe" overrides aaa/dupe/SKILL.md`, result.Errors[0].Error())
	})

	t.Run("accepts structured metadata values", func(t *testing.T) {
		result := LoadSkills(fstest.MapFS{"example/SKILL.md": mapFile(`---
name: example
description: Example skill
metadata:
  openclaw:
    tools: true
---
Example content.
`)}, "/tmp/skills")

		require.Empty(t, result.Errors)
		require.Len(t, result.Skills.Items, 1)
		require.Equal(t, map[string]any{"openclaw": map[string]any{"tools": true}}, result.Skills.Items["example"].Metadata)
	})

	t.Run("continues loading valid skills when some are invalid", func(t *testing.T) {
		fsys := fstest.MapFS{
			"good-skill/SKILL.md": mapFile("---\nname: good-skill\ndescription: Valid\n---\nready\n"),
			"bad-skill/SKILL.md":  mapFile("---\nname: bad-skill\n---\n"),
		}

		result := LoadSkills(fsys, "/tmp/skills")

		require.Len(t, result.Skills.Items, 1)
		require.Equal(t, "ready\n", result.Skills.Items["good-skill"].Content)
		require.Len(t, result.Errors, 1)
		require.Equal(t, `bad-skill/SKILL.md: missing required frontmatter field "description"`, result.Errors[0].Error())
	})
}

func TestSkillDescriptions(t *testing.T) {
	skills := Skills{Root: "/tmp/skills", Items: map[string]Skill{
		"docs-helper": {Name: "docs-helper", Description: "Write docs", License: "", Compatibility: "", Metadata: nil, Location: "docs-helper/SKILL.md", Content: ""},
		"git-review":  {Name: "git-review", Description: "Review git changes", License: "", Compatibility: "", Metadata: nil, Location: "git-review/SKILL.md", Content: ""},
	}, Dirs: nil, fsys: nil}

	t.Run("tool description lists explicitly allowed skills", func(t *testing.T) {
		factory := &toolFactory{client: nil, systemPrompt: "", model: "", reasoningEffort: "", compactThreshold: 0, compactionSteering: "", diagnostics: false, expandPromptShellCommands: PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false}, promptExpansion: testPromptExpansionEnvironment(t), agent: agentWithSkillPermission(permissionAllow), agents: Agents{Items: nil}, skills: skills, baseTools: nil}

		description := factory.skillDescription()

		require.Contains(t, description, "Load a specialized skill when the task at hand matches one of the skills listed in the system prompt.")
		require.Contains(t, description, "## Available skills")
		require.Contains(t, description, "- **docs-helper**: Write docs")
		require.Contains(t, description, "- **git-review**: Review git changes")
	})

	t.Run("tool description filters denied skills", func(t *testing.T) {
		factory := &toolFactory{promptExpansion: testPromptExpansionEnvironment(t), skills: skills, agent: agentWithSkillRules(
			PermissionRule{Pattern: "*", Action: permissionDeny},
			PermissionRule{Pattern: "docs-helper", Action: permissionAllow},
		)}

		description := factory.skillDescription()

		require.Contains(t, description, "- **docs-helper**: Write docs")
		require.NotContains(t, description, "git-review")
	})

	t.Run("system prompt instructs discovery without listing skills", func(t *testing.T) {
		prompt := composeSystemPromptWithSkills("base prompt", skills, agentWithSkillPermission(permissionAllow))

		require.Contains(t, prompt, "base prompt")
		require.Contains(t, prompt, "skills provide specialized instructions and workflows for specific tasks.")
		require.Contains(t, prompt, "When a task may benefit from specialized instructions, call the find_skills tool to search all available skills, then call the skill tool to load the selected skill.")
		require.NotContains(t, prompt, "<available_skills>")
		require.NotContains(t, prompt, "docs-helper")
		require.NotContains(t, prompt, "git-review")
	})
}

func TestPermissionPrompt(t *testing.T) {
	t.Run("renders full bash allow", func(t *testing.T) {
		prompt := composeSystemPromptWithSkills("base prompt", Skills{}, &Agent{Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "bash", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}})

		require.Contains(t, prompt, "## Allowed Bash Permissions\n\n- Everything is allowed.")
		require.Contains(t, prompt, "## Permission Wildcard Rules")
		require.NotContains(t, prompt, "{Name:bash")
	})

	t.Run("renders explicit allow list for each bucket", func(t *testing.T) {
		prompt := composeSystemPromptWithSkills("base prompt", Skills{}, &Agent{Permission: PermissionSet{Buckets: []PermissionBucket{
			{Name: "read", Rules: []PermissionRule{{Pattern: "README.md", Action: permissionAllow}}},
			{Name: "bash", Rules: []PermissionRule{{Pattern: "git status", Action: permissionAllow}, {Pattern: "git diff *", Action: permissionAllow}}},
		}}})

		require.Contains(t, prompt, "## Allowed Read Permissions\n\n- `README.md`")
		require.Contains(t, prompt, "## Allowed Bash Permissions\n\n- `git status`\n- `git diff *`")
	})

	t.Run("renders full bash allow with deny exceptions", func(t *testing.T) {
		prompt := composeSystemPromptWithSkills("base prompt", Skills{}, &Agent{Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "bash", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}, {Pattern: "rm *", Action: permissionDeny}, {Pattern: "sudo *", Action: permissionDeny}}}}}})

		require.Contains(t, prompt, "- All commands are allowed except:\n- `rm *`\n- `sudo *`")
	})

	t.Run("omits block without effective bash allow", func(t *testing.T) {
		prompt := composeSystemPromptWithSkills("base prompt", Skills{}, &Agent{Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "*", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}})

		require.NotContains(t, prompt, "## Allowed Bash Permissions")
		require.NotContains(t, prompt, "## Permission Wildcard Rules")
	})

	t.Run("renders non bash full allow with exceptions", func(t *testing.T) {
		prompt := composeSystemPromptWithSkills("base prompt", Skills{}, &Agent{Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "webfetch", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}, {Pattern: "https://private.example/*", Action: permissionDeny}}}}}})

		require.Contains(t, prompt, "## Allowed Webfetch Permissions")
		require.Contains(t, prompt, "- Everything is allowed except:\n- `https://private.example/*`")
	})
}

func TestFindSkillsTool(t *testing.T) {
	skills := Skills{Root: "/tmp/skills", Items: map[string]Skill{
		"docs-helper": {Name: "docs-helper", Description: "Write docs", License: "", Compatibility: "", Metadata: nil, Location: "docs-helper/SKILL.md", Content: "documentation guides"},
		"git-review":  {Name: "git-review", Description: "Review git changes", License: "", Compatibility: "", Metadata: nil, Location: "git-review/SKILL.md", Content: "git status diff"},
	}, Dirs: nil, fsys: nil}

	t.Run("finds explicitly allowed skills not presented in system prompt", func(t *testing.T) {
		factory := &toolFactory{client: nil, systemPrompt: "", model: "", reasoningEffort: "", compactThreshold: 0, compactionSteering: "", diagnostics: false, expandPromptShellCommands: PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false}, promptExpansion: testPromptExpansionEnvironment(t), agent: agentWithSkillPermission(permissionAllow), agents: Agents{Items: nil}, skills: skills, baseTools: nil}
		tool := factory.findSkillsTool()

		got, err := tool.Call(context.Background(), json.RawMessage(`{"query":"git"}`), nil)

		require.NoError(t, err)
		require.Contains(t, got.Output, "- **git-review**: Review git changes")
	})

	t.Run("excludes permission denied skills", func(t *testing.T) {
		factory := &toolFactory{promptExpansion: testPromptExpansionEnvironment(t), skills: skills, agent: agentWithSkillRules(
			PermissionRule{Pattern: "*", Action: permissionDeny},
			PermissionRule{Pattern: "docs-helper", Action: permissionAllow},
		)}
		tool := factory.findSkillsTool()

		got, err := tool.Call(context.Background(), json.RawMessage(`{"query":"git"}`), nil)

		require.NoError(t, err)
		require.Equal(t, "No matching skills found.", got.Output)
	})
}

func TestFindSkillsToolVisibilityFollowsSkillPermission(t *testing.T) {
	skills := Skills{Root: "/tmp/skills", Items: map[string]Skill{
		"docs-helper": {Name: "docs-helper", Description: "Write docs", License: "", Compatibility: "", Metadata: nil, Location: "docs-helper/SKILL.md", Content: ""},
	}, Dirs: nil, fsys: nil}

	t.Run("hidden when all skills are denied", func(t *testing.T) {
		factory := &toolFactory{client: nil, systemPrompt: "", model: "", reasoningEffort: "", compactThreshold: 0, compactionSteering: "", diagnostics: false, expandPromptShellCommands: PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false}, promptExpansion: testPromptExpansionEnvironment(t), agent: nil, agents: Agents{Items: nil}, skills: skills, baseTools: nil}
		agent := agentWithSkillRules(PermissionRule{Pattern: "*", Action: permissionDeny})

		tools := factory.toolsFor(agent)

		require.NotContains(t, tools, "find_skills")
	})

	t.Run("visible when any skill is allowed", func(t *testing.T) {
		factory := &toolFactory{client: nil, systemPrompt: "", model: "", reasoningEffort: "", compactThreshold: 0, compactionSteering: "", diagnostics: false, expandPromptShellCommands: PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false}, promptExpansion: testPromptExpansionEnvironment(t), agent: nil, agents: Agents{Items: nil}, skills: skills, baseTools: nil}
		agent := agentWithSkillRules(
			PermissionRule{Pattern: "*", Action: permissionDeny},
			PermissionRule{Pattern: "docs-helper", Action: permissionAllow},
		)

		tools := factory.toolsFor(agent)

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

Help with guides and docs.
`),
	}, "/tmp/skills").Skills

	t.Run("returns all skills when query is empty", func(t *testing.T) {
		require.Equal(t, strings.Join([]string{
			"## Matching skills",
			"- **docs-writer**: Write product documentation",
			"- **git-release**: Create consistent releases and changelogs",
			"- **release-notes**: Summarize merged PRs",
		}, "\n"), loaded.Find("   "))
	})

	t.Run("exact name match ranks first case insensitively", func(t *testing.T) {
		got := loaded.Find("GIT-RELEASE")
		lines := strings.Split(got, "\n")
		require.Equal(t, "## Matching skills", lines[0])
		require.Equal(t, "- **git-release**: Create consistent releases and changelogs", lines[1])
	})

	t.Run("returns all matching skills in ranked order", func(t *testing.T) {
		require.Equal(t, strings.Join([]string{
			"## Matching skills",
			"- **git-release**: Create consistent releases and changelogs",
			"- **release-notes**: Summarize merged PRs",
		}, "\n"), loaded.Find("release"))
	})

	t.Run("matches description and content case insensitively", func(t *testing.T) {
		require.Equal(t, "## Matching skills"+"\n"+"- **docs-writer**: Write product documentation", loaded.Find("GUIDES"))
	})

	t.Run("returns no matches message", func(t *testing.T) {
		require.Equal(t, "No matching skills found.", loaded.Find("security audit"))
	})

	t.Run("returns empty corpus message", func(t *testing.T) {
		require.Equal(t, "No skills are currently available.", (Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}).Find("anything"))
	})
}

func TestSkillsRender(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "git-release", "scripts"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "git-release", "reference"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "git-release", "SKILL.md"), []byte(`---
name: git-release
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
		factory := &toolFactory{client: nil, systemPrompt: "", model: "", reasoningEffort: "", compactThreshold: 0, compactionSteering: "", diagnostics: false, expandPromptShellCommands: PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false}, promptExpansion: testPromptExpansionEnvironment(t), agent: nil, agents: Agents{Items: nil}, skills: loaded, baseTools: nil}
		result, err := factory.skillTool().Call(context.Background(), json.RawMessage(`{"name":"git-release"}`), nil)
		require.NoError(t, err)

		got := result.Output

		baseURL := fileURL(filepath.Join(root, "git-release"))

		require.Contains(t, got, `<skill_content name="git-release">`)
		require.Contains(t, got, "# skill: git-release")
		require.Contains(t, got, "## What I do\n- Draft release notes")
		require.Contains(t, got, "Base directory for this skill: "+baseURL)
		require.Contains(t, got, "Relative paths in this skill (e.g., scripts/, reference/) are relative to this base directory.")
		require.Contains(t, got, "Note: file list is sampled.")
		require.Contains(t, got, "<skill_files>")
		require.Contains(t, got, "<file>"+filepath.ToSlash(filepath.Join(root, "git-release", "reference", "file-00.txt"))+"</file>")
		require.NotContains(t, got, filepath.ToSlash(filepath.Join(root, "git-release", "scripts", "demo.sh")))
		require.NotContains(t, got, "SKILL.md")
		require.Equal(t, 10, strings.Count(got, "<file>"))
	})

	t.Run("experimental stronger skills returns a short tool result and developer replay input", func(t *testing.T) {
		factory := &toolFactory{client: nil, systemPrompt: "", model: "", reasoningEffort: "", compactThreshold: 0, compactionSteering: "", diagnostics: false, experimentalStrongerSkills: true, expandPromptShellCommands: PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false}, promptExpansion: testPromptExpansionEnvironment(t), agent: nil, agents: Agents{Items: nil}, skills: loaded, baseTools: nil}
		result, replayInput, err := factory.skillTool().CallReplay(context.Background(), json.RawMessage(`{"name":"git-release"}`), nil)
		require.NoError(t, err)

		require.Equal(t, "skill git-release loaded", result.Output)
		require.Len(t, replayInput, 1)
		got := marshalJSON(t, replayInput[0])
		require.Contains(t, got, `"role":"developer"`)
		require.Contains(t, got, `skill_metadata`)
		require.Contains(t, got, `metadata: {\n  \"audience\": \"maintainers\"\n}`)
		require.Contains(t, got, "license: MIT")
		require.Contains(t, got, "compatibility: rocketcode")
		require.Contains(t, got, `## What I do\n- Draft release notes`)
	})

	t.Run("returns an error for unknown skills", func(t *testing.T) {
		factory := &toolFactory{client: nil, systemPrompt: "", model: "", reasoningEffort: "", compactThreshold: 0, compactionSteering: "", diagnostics: false, expandPromptShellCommands: PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false}, promptExpansion: testPromptExpansionEnvironment(t), agent: nil, agents: Agents{Items: nil}, skills: loaded, baseTools: nil}
		_, err := factory.skillTool().Call(context.Background(), json.RawMessage(`{"name":"missing-skill"}`), nil)
		require.EqualError(t, err, `skill "missing-skill" not found. Available skills: git-release`)
	})
}

func TestSkillsRenderUsesLoadedFS(t *testing.T) {
	loaded := LoadSkills(fstest.MapFS{
		"docs-helper/SKILL.md": mapFile(`---
name: docs-helper
description: Write docs
---

Use for docs.
`),
		"docs-helper/reference/guide.md": mapFile("guide\n"),
		"docs-helper/reference/link.md":  {Data: []byte("guide.md"), Mode: fs.ModeSymlink, ModTime: time.Time{}, Sys: nil},
	}, "/virtual/skills").Skills

	factory := &toolFactory{client: nil, systemPrompt: "", model: "", reasoningEffort: "", compactThreshold: 0, compactionSteering: "", diagnostics: false, expandPromptShellCommands: PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false}, promptExpansion: testPromptExpansionEnvironment(t), agent: nil, agents: Agents{Items: nil}, skills: loaded, baseTools: nil}
	result, err := factory.skillTool().Call(context.Background(), json.RawMessage(`{"name":"docs-helper"}`), nil)
	require.NoError(t, err)

	got := result.Output
	require.Contains(t, got, "<file>/virtual/skills/docs-helper/reference/guide.md</file>")
	require.NotContains(t, got, "link.md")
}

func TestSkillsRenderRequiresLoadedFS(t *testing.T) {
	skills := Skills{Root: "/virtual/skills", Items: map[string]Skill{"docs-helper": {Name: "docs-helper", Description: "Write docs", Location: "docs-helper/SKILL.md", Content: "Use for docs.", Metadata: nil}}, Dirs: []string{"docs-helper"}, fsys: nil}
	factory := &toolFactory{client: nil, systemPrompt: "", model: "", reasoningEffort: "", compactThreshold: 0, compactionSteering: "", diagnostics: false, expandPromptShellCommands: PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false}, promptExpansion: testPromptExpansionEnvironment(t), agent: nil, agents: Agents{Items: nil}, skills: skills, baseTools: nil}

	_, err := factory.skillTool().Call(context.Background(), json.RawMessage(`{"name":"docs-helper"}`), nil)

	require.EqualError(t, err, "skills filesystem is not available")
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

Generated: !`+"`"+`cat MEMORY.md`+"`"+`
`), 0o644))

	loaded := LoadSkills(os.DirFS(dir), dir).Skills
	require.Contains(t, loaded.Items["dynamic-skill"].Content, "!`cat MEMORY.md`")

	t.Run("leaves shell commands literal by default", func(t *testing.T) {
		factory := &toolFactory{client: nil, systemPrompt: "", model: "", reasoningEffort: "", compactThreshold: 0, compactionSteering: "", diagnostics: false, expandPromptShellCommands: PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false}, promptExpansion: testPromptExpansionEnvironment(t), agent: nil, agents: Agents{Items: nil}, skills: loaded, baseTools: nil}
		result, err := factory.skillTool().Call(context.Background(), json.RawMessage(`{"name":"dynamic-skill"}`), nil)

		require.NoError(t, err)

		got := result.Output
		require.Contains(t, got, "Generated: !`cat MEMORY.md`")
	})

	t.Run("skill tool expands shell commands in runtime root", func(t *testing.T) {
		env, err := newPromptExpansionEnvironment(root, testPromptShellOutputConfig(t, root, dir), nil)
		require.NoError(t, err)

		factory := &toolFactory{client: nil, systemPrompt: "", model: "", reasoningEffort: "", compactThreshold: 0, compactionSteering: "", diagnostics: false, expandPromptShellCommands: PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: true}, promptExpansion: env, agent: nil, agents: Agents{Items: nil}, skills: loaded, baseTools: nil}
		tool := factory.skillTool()

		got, err := tool.Call(context.Background(), json.RawMessage(`{"name":"dynamic-skill"}`), nil)

		require.NoError(t, err)
		require.Contains(t, got.Output, "Generated: dynamic-output")
		require.NotContains(t, got.Output, "!`cat MEMORY.md`")
	})
}

func agentWithSkillPermission(action PermissionAction) *Agent {
	return agentWithSkillRules(PermissionRule{Pattern: "*", Action: action})
}

func agentWithSkillRules(rules ...PermissionRule) *Agent {
	return &Agent{Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "skill", Rules: rules}}}}
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
