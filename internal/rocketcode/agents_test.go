package rocketcode

import (
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"
)

func testMapFile(data string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(data), Mode: 0, ModTime: time.Time{}, Sys: nil}
}

func testMapFileMode(mode fs.FileMode, data string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(data), Mode: mode, ModTime: time.Time{}, Sys: nil}
}

func TestLoadAgents(t *testing.T) {
	t.Run("loads valid top level markdown agents", func(t *testing.T) {
		fsys := fstest.MapFS{
			"review.md": testMapFileMode(0o640, `---
description: Reviews code for correctness
model: anthropic/claude-sonnet-4-20250514
reasoningEffort: high
verbosity: low
maxRecursion: 2
permission:
  edit: deny
temperature: 0.1
hidden: true
---

You are in review mode.
`),
			"plan.md": testMapFileMode(0o600, `---
description: Plan changes only
---

Plan, do not edit.
`),
			"README.txt": testMapFile("ignored"),
			"nested/child.md": testMapFile(`---
description: ignored nested agent
---
ignored
`),
		}

		result := LoadAgents(fsys)

		require.Empty(t, result.Errors)
		require.Len(t, result.Agents.Items, 2)

		review := result.Agents.Items["review"]
		require.Equal(t, "review", review.Name)
		require.Equal(t, "Reviews code for correctness", review.Description)
		require.Equal(t, "anthropic/claude-sonnet-4-20250514", review.Model)
		require.Equal(t, "high", review.ReasoningEffort)
		require.Equal(t, "low", review.Verbosity)
		require.NotNil(t, review.MaxRecursion)
		require.Equal(t, 2, *review.MaxRecursion)
		require.Equal(t, "You are in review mode.", review.Prompt)
		require.Equal(t, "review.md", review.Location)
		require.Equal(t, fs.FileMode(0o640), review.FileMode.Perm())
		require.Equal(t, PermissionSet{Buckets: []PermissionBucket{{Name: "edit", Rules: []PermissionRule{{Pattern: "*", Action: permissionDeny}}}}}, review.Permission)
		require.InDelta(t, 0.1, review.Frontmatter["temperature"], 0.0001)
		require.Equal(t, true, review.Frontmatter["hidden"])

		plan := result.Agents.Items["plan"]
		require.Nil(t, plan.MaxRecursion)
		require.Equal(t, "Plan, do not edit.", plan.Prompt)
		require.NotContains(t, result.Agents.Items, "child")
	})

	t.Run("loads max recursion values", func(t *testing.T) {
		result := LoadAgents(fstest.MapFS{
			"unlimited.md": testMapFile("---\ndescription: Unlimited\nmaxRecursion: -1\n---\nPrompt\n"),
			"zero.md":      testMapFile("---\ndescription: Zero\nmaxRecursion: 0\n---\nPrompt\n"),
			"positive.md":  testMapFile("---\ndescription: Positive\nmaxRecursion: 3\n---\nPrompt\n"),
		})

		require.Empty(t, result.Errors)
		require.Nil(t, result.Agents.Items["unlimited"].MaxRecursion)
		require.NotNil(t, result.Agents.Items["zero"].MaxRecursion)
		require.Equal(t, 0, *result.Agents.Items["zero"].MaxRecursion)
		require.NotNil(t, result.Agents.Items["positive"].MaxRecursion)
		require.Equal(t, 3, *result.Agents.Items["positive"].MaxRecursion)
	})

	t.Run("rejects invalid max recursion values", func(t *testing.T) {
		tests := []struct {
			name  string
			value string
		}{
			{name: "below unlimited", value: "-2"},
			{name: "float", value: "1.0"},
			{name: "string", value: "\"1\""},
			{name: "bool", value: "true"},
			{name: "null", value: "null"},
			{name: "sequence", value: "[]"},
			{name: "map", value: "{}"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := LoadAgents(fstest.MapFS{
					"main.md": testMapFile("---\ndescription: Main\nmaxRecursion: " + tt.value + "\n---\nPrompt\n"),
				})

				require.Empty(t, result.Agents.Items)
				require.Len(t, result.Errors, 1)
				require.Contains(t, result.Errors[0].Error(), "main.md: parse maxRecursion:")
			})
		}
	})

	t.Run("supports fallback yaml sanitization", func(t *testing.T) {
		fsys := fstest.MapFS{
			"review.md": testMapFile(`---
description: Review code: security and performance
model: synthetic/hf:zai-org/GLM-4.7
---

Strictly follow the rules.
`),
		}

		result := LoadAgents(fsys)

		require.Empty(t, result.Errors)
		require.Equal(t, "Review code: security and performance", result.Agents.Items["review"].Description)
		require.Equal(t, "synthetic/hf:zai-org/GLM-4.7", result.Agents.Items["review"].Model)
	})

	t.Run("keeps prompt shell commands raw", func(t *testing.T) {
		fsys := fstest.MapFS{
			"main.md": testMapFile("---\ndescription: Main\n---\nUse !`printf expanded` now.\n"),
		}

		result := LoadAgents(fsys)

		require.Empty(t, result.Errors)
		require.Equal(t, "Use !`printf expanded` now.", result.Agents.Items["main"].Prompt)
	})

	t.Run("reports missing frontmatter", func(t *testing.T) {
		fsys := fstest.MapFS{
			"review.md": testMapFile("# Missing frontmatter\n"),
		}

		result := LoadAgents(fsys)

		require.Empty(t, result.Agents.Items)
		require.Len(t, result.Errors, 1)
		require.Equal(t, "review.md: missing YAML frontmatter", result.Errors[0].Error())
	})

	t.Run("ignores mode frontmatter", func(t *testing.T) {
		fsys := fstest.MapFS{
			"main.md": testMapFile("---\ndescription: Main\nmode: invalid\n---\nPrompt\n"),
		}

		result := LoadAgents(fsys)

		require.Empty(t, result.Errors)
		require.Equal(t, "Prompt", result.Agents.Items["main"].Prompt)
		require.Equal(t, "invalid", result.Agents.Items["main"].Frontmatter["mode"])
	})

	t.Run("reports invalid yaml after fallback", func(t *testing.T) {
		fsys := fstest.MapFS{
			"review.md": testMapFile("---\ndescription: [unterminated\n---\n"),
		}

		result := LoadAgents(fsys)

		require.Empty(t, result.Agents.Items)
		require.Len(t, result.Errors, 1)
		require.Contains(t, result.Errors[0].Error(), "review.md: parse YAML frontmatter:")
	})

	t.Run("rejects non map frontmatter", func(t *testing.T) {
		fsys := fstest.MapFS{
			"review.md": testMapFile("---\n- not\n- a\n- map\n---\n"),
		}

		result := LoadAgents(fsys)

		require.Empty(t, result.Agents.Items)
		require.Len(t, result.Errors, 1)
		require.Contains(t, result.Errors[0].Error(), "review.md: parse YAML frontmatter:")
	})

	t.Run("continues loading valid files when some are invalid", func(t *testing.T) {
		fsys := fstest.MapFS{
			"good.md": testMapFile("---\ndescription: Valid\n---\nready\n"),
			"bad.md":  testMapFile("---\ndescription: [broken\n---\n"),
		}

		result := LoadAgents(fsys)

		require.Len(t, result.Agents.Items, 1)
		require.Equal(t, "ready", result.Agents.Items["good"].Prompt)
		require.Len(t, result.Errors, 1)
		require.Contains(t, result.Errors[0].Error(), "bad.md: parse YAML frontmatter:")
	})

	t.Run("returns empty result when there are no agents", func(t *testing.T) {
		result := LoadAgents(fstest.MapFS{
			"notes.txt": testMapFile("ignored"),
			"nested/agent.md": testMapFile(`---
description: ignored nested agent
---
ignored
`),
		})

		require.Empty(t, result.Errors)
		require.Empty(t, result.Agents.Items)
	})

	t.Run("reports read dir failure", func(t *testing.T) {
		result := LoadAgents(failingFS{})

		require.Empty(t, result.Agents.Items)
		require.Len(t, result.Errors, 1)
		require.Equal(t, "read agents dir: permission denied", result.Errors[0].Error())
	})
}
