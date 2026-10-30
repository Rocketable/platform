package harness

import (
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const benchmarkAgentFrontmatter = `description: Reviews proposed changes
model: example-model
reasoningEffort: high
verbosity: low
maxRecursion: 2
permission:
  edit: deny
schema:
  output:
    type: object
    properties:
      answer:
        type: string
    required: [answer]
`

func BenchmarkDecodeAgentFrontmatter(b *testing.B) {
	data := []byte(benchmarkAgentFrontmatter)
	for b.Loop() {
		_, _, err := decodeAgentFrontmatter(data)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLoadAgent(b *testing.B) {
	fsys := fstest.MapFS{"review.md": testMapFile("---\n" + benchmarkAgentFrontmatter + "---\n" + strings.Repeat("Check the change and its tests.\n", 100))}

	models := &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }}
	for b.Loop() {
		_, err := loadAgent(fsys, "review.md", models)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestDecodeAgentFrontmatterAllocations(t *testing.T) {
	data := []byte(benchmarkAgentFrontmatter)
	allocs := testing.AllocsPerRun(100, func() {
		_, _, err := decodeAgentFrontmatter(data)
		require.NoError(t, err)
	})
	// Leave headroom for decoder changes, but not a second YAML parser pass.
	require.Less(t, allocs, float64(300))
}

func TestDecodeAgentFrontmatter(t *testing.T) {
	for _, text := range []string{
		benchmarkAgentFrontmatter,
		"{}",
		"value: &value example\nalias: *value",
		"defaults: &defaults {enabled: true, count: 2}\nmerged: {<<: *defaults, count: 3}",
		"string: yes\nquoted: '2'\ninteger: 2\nfloat: 0.1\nbool: true\nnull: null\ndate: 2026-01-01",
		"nested: {1: value}\nsequence: [one, 2, false]",
		"key: first\nkey: second",
		"nested: {key: first, key: second}",
		"value: *missing",
		"value: &value [*value]",
		"value: [unterminated",
		"? [one, two]\n: value",
	} {
		t.Run(text, func(t *testing.T) {
			var want map[string]any

			errWant := yaml.Unmarshal([]byte(text), &want)

			got, node, err := decodeAgentFrontmatter([]byte(text))
			if errWant != nil {
				require.EqualError(t, err, "unmarshal YAML frontmatter: "+errWant.Error())
				require.Nil(t, got)
				require.Nil(t, node)

				return
			}

			require.NoError(t, err)
			require.Equal(t, want, got)

			var document yaml.Node
			require.NoError(t, yaml.Unmarshal([]byte(text), &document))
			require.Equal(t, document.Content[0], node)
		})
	}
}

func testMapFile(data string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(data), Mode: 0, ModTime: time.Time{}, Sys: nil}
}

func testMapFileMode(mode fs.FileMode, data string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(data), Mode: mode, ModTime: time.Time{}, Sys: nil}
}

func TestLoadAgents(t *testing.T) {
	t.Run("loads only immediate shadows of matching parent files", func(t *testing.T) {
		root, err := os.OpenRoot(t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })

		for _, name := range []string{"main.md", "main/z.md", "main/a.md", "main/deeper/ignored.md", "unmatched/ignored.md", "ordinary.md"} {
			require.NoError(t, root.MkdirAll(path.Dir(name), 0o700))
			require.NoError(t, root.WriteFile(name, []byte("---\nmodel: "+name+"\npermission:\n  edit: deny\n---\nPrompt "+name), 0o600))
		}

		require.NoError(t, root.WriteFile("main/notes.txt", []byte("ignored"), 0o600))
		require.NoError(t, root.WriteFile("main/broken.md", []byte("missing frontmatter"), 0o600))

		result := LoadAgents(root.FS(), &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})
		require.Len(t, result.Agents.Items, 4)
		require.Equal(t, []string{"main", "ordinary"}, result.Agents.PrimaryNames())
		require.Equal(t, []string{"main/a", "main/z"}, result.Agents.ShadowNames("main"))
		require.Empty(t, result.Agents.ShadowNames("ordinary"))
		require.Empty(t, result.Agents.ShadowNames("unmatched"))
		require.Len(t, result.Errors, 1)
		require.EqualError(t, result.Errors[0], "main/broken.md: missing YAML frontmatter")

		for _, name := range []string{"main", "main/a", "main/z", "ordinary"} {
			agent := result.Agents.Items[name]
			require.Equal(t, name, agent.Name)
			require.Equal(t, name+".md", agent.Location)
			require.Equal(t, name+".md", agent.Model)
			require.Equal(t, "Prompt "+name+".md", agent.Prompt)
			action, matched := agent.Permission.Evaluate("edit", "example.txt")
			require.True(t, matched)
			require.Equal(t, PermissionDeny, action)
		}
	})

	t.Run("reports a shadow directory that escapes the root", func(t *testing.T) {
		root, err := os.OpenRoot(t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })
		require.NoError(t, root.WriteFile("main.md", []byte("---\nmodel: gpt-5.4\n---\nPrompt"), 0o600))
		require.NoError(t, root.Symlink("..", "main"))

		result := LoadAgents(root.FS(), &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})
		require.Len(t, result.Agents.Items, 1)
		require.Len(t, result.Errors, 1)
		require.ErrorContains(t, result.Errors[0], "main: read shadow agents:")
	})

	t.Run("loads valid top level markdown agents", func(t *testing.T) {
		fsys := fstest.MapFS{
			"review.md": testMapFileMode(0o640, `---
description: Reviews code for correctness
model: gpt-6.1-sol
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
model: gpt-5.4
---

Plan, do not edit.
`),
			"README.txt": testMapFile("ignored"),
			"plan":       testMapFile("not a shadow directory"),
			"nested/child.md": testMapFile(`---
description: ignored nested agent
---
ignored
`),
		}

		result := LoadAgents(fsys, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

		require.Empty(t, result.Errors)
		require.Len(t, result.Agents.Items, 2)

		review := result.Agents.Items["review"]
		require.Equal(t, "review", review.Name)
		require.Equal(t, "Reviews code for correctness", review.Description)
		require.Equal(t, "gpt-6.1-sol", review.Model)
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

	t.Run("loads optional string values", func(t *testing.T) {
		for _, tt := range []struct{ frontmatter, want string }{
			{"", ""},
			{"description: null", ""},
			{"description: 123", ""},
			{"description: true", ""},
			{"description: []", ""},
			{"description: {}", ""},
			{`description: ""`, ""},
			{`description: "  exact text  "`, "  exact text  "},
		} {
			t.Run(tt.frontmatter, func(t *testing.T) {
				result := LoadAgents(fstest.MapFS{
					"main.md": testMapFile("---\nmodel: gpt-5.4\n" + tt.frontmatter + "\n---\nPrompt\n"),
				}, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

				require.Empty(t, result.Errors)
				require.Equal(t, tt.want, result.Agents.Items["main"].Description)
			})
		}
	})

	t.Run("loads reasoning effort from model suffix", func(t *testing.T) {
		for _, tt := range []struct{ frontmatter, model, effort string }{
			{"model: work/gpt-6.1-sol#max", "work/gpt-6.1-sol", "max"},
			{"model: work/gpt-6.1-sol#max\nreasoningEffort: high", "work/gpt-6.1-sol", "max"},
			{"model: work/gpt-6.1-sol\nreasoningEffort: high", "work/gpt-6.1-sol", "high"},
			{"model: work/gpt-6.1-sol", "work/gpt-6.1-sol", ""},
		} {
			t.Run(tt.frontmatter, func(t *testing.T) {
				result := LoadAgents(fstest.MapFS{"main.md": testMapFile("---\n" + tt.frontmatter + "\n---\nPrompt\n")}, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

				require.Empty(t, result.Errors)
				require.Equal(t, tt.model, result.Agents.Items["main"].Model)
				require.Equal(t, tt.effort, result.Agents.Items["main"].ReasoningEffort)
			})
		}
	})

	t.Run("loads schema output", func(t *testing.T) {
		result := LoadAgents(fstest.MapFS{
			"review.md": testMapFile(`---
description: Structured
model: gpt-5.4
schema:
  output:
    type: object
    properties:
      answer:
        type: string
    required:
      - answer
---
Prompt
`),
		}, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

		require.Empty(t, result.Errors)
		require.Equal(t, map[string]any{
			"type":       "object",
			"properties": map[string]any{"answer": map[string]any{"type": "string"}},
			"required":   []any{"answer"},
		}, result.Agents.Items["review"].OutputSchema)
	})

	t.Run("ignores schema without output", func(t *testing.T) {
		result := LoadAgents(fstest.MapFS{
			"main.md": testMapFile("---\ndescription: Main\nmodel: gpt-5.4\nschema:\n  input:\n    type: object\n---\nPrompt\n"),
		}, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

		require.Empty(t, result.Errors)
		require.Nil(t, result.Agents.Items["main"].OutputSchema)
	})

	t.Run("rejects invalid schema output", func(t *testing.T) {
		tests := []struct {
			name  string
			value string
			want  string
		}{
			{name: "schema list", value: "schema: []", want: "main.md: schema: must be a mapping"},
			{name: "output string", value: "schema:\n  output: nope", want: "main.md: schema.output: must be a mapping"},
			{name: "invalid json schema", value: "schema:\n  output:\n    pattern: \"[\"", want: "main.md: schema.output:"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := LoadAgents(fstest.MapFS{
					"main.md": testMapFile("---\ndescription: Main\nmodel: gpt-5.4\n" + tt.value + "\n---\nPrompt\n"),
				}, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

				require.Empty(t, result.Agents.Items)
				require.Len(t, result.Errors, 1)
				require.Contains(t, result.Errors[0].Error(), tt.want)
			})
		}
	})

	t.Run("loads max recursion values", func(t *testing.T) {
		result := LoadAgents(fstest.MapFS{
			"unlimited.md": testMapFile("---\ndescription: Unlimited\nmodel: gpt-5.4\nmaxRecursion: -1\n---\nPrompt\n"),
			"zero.md":      testMapFile("---\ndescription: Zero\nmodel: gpt-5.4\nmaxRecursion: 0\n---\nPrompt\n"),
			"positive.md":  testMapFile("---\ndescription: Positive\nmodel: gpt-5.4\nmaxRecursion: 3\n---\nPrompt\n"),
		}, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

		require.Empty(t, result.Errors)
		require.Nil(t, result.Agents.Items["unlimited"].MaxRecursion)
		require.NotNil(t, result.Agents.Items["zero"].MaxRecursion)
		require.Equal(t, 0, *result.Agents.Items["zero"].MaxRecursion)
		require.NotNil(t, result.Agents.Items["positive"].MaxRecursion)
		require.Equal(t, 3, *result.Agents.Items["positive"].MaxRecursion)
	})

	t.Run("rejects invalid typed frontmatter values", func(t *testing.T) {
		tests := []struct {
			name  string
			field string
			value string
			want  string
		}{
			{name: "max recursion below unlimited", field: "maxRecursion", value: "-2", want: "main.md: parse maxRecursion:"},
			{name: "max recursion float", field: "maxRecursion", value: "1.0", want: "main.md: parse maxRecursion:"},
			{name: "max recursion string", field: "maxRecursion", value: "\"1\"", want: "main.md: parse maxRecursion:"},
			{name: "max recursion bool", field: "maxRecursion", value: "true", want: "main.md: parse maxRecursion:"},
			{name: "max recursion null", field: "maxRecursion", value: "null", want: "main.md: parse maxRecursion:"},
			{name: "max recursion sequence", field: "maxRecursion", value: "[]", want: "main.md: parse maxRecursion:"},
			{name: "max recursion map", field: "maxRecursion", value: "{}", want: "main.md: parse maxRecursion:"},
			{name: "shell relative path", field: "shell", value: "sandbox/enter", want: "main.md: shell: must be an absolute path or emulated"},
			{name: "shell bare name", field: "shell", value: "enter", want: "main.md: shell: must be an absolute path or emulated"},
			{name: "shell empty", field: "shell", value: "\"\"", want: "main.md: shell: must be an absolute path or emulated"},
			{name: "shell null", field: "shell", value: "null", want: "main.md: shell: must be an absolute path or emulated"},
			{name: "shell number", field: "shell", value: "123", want: "main.md: shell: must be an absolute path or emulated"},
			{name: "shell sequence", field: "shell", value: "[/a]", want: "main.md: shell: must be an absolute path or emulated"},
			{name: "shell map", field: "shell", value: "{a: b}", want: "main.md: shell: must be an absolute path or emulated"},
			{name: "space sequence", field: "space", value: "[Billing]", want: "main.md: space:"},
			{name: "space number", field: "space", value: "123", want: "main.md: space:"},
			{name: "sudo word", field: "sudo", value: "maybe", want: "main.md: sudo:"},
			{name: "sudo number", field: "sudo", value: "2", want: "main.md: sudo:"},
			{name: "sudo null", field: "sudo", value: "null", want: "main.md: sudo:"},
			{name: "sudo sequence", field: "sudo", value: "[yes]", want: "main.md: sudo:"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := LoadAgents(fstest.MapFS{
					"main.md": testMapFile("---\ndescription: Main\nmodel: gpt-5.4\n" + tt.field + ": " + tt.value + "\n---\nPrompt\n"),
				}, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

				require.Empty(t, result.Agents.Items)
				require.Len(t, result.Errors, 1)
				require.Contains(t, result.Errors[0].Error(), tt.want)
			})
		}
	})

	t.Run("loads sudo values", func(t *testing.T) {
		for value, sudo := range map[string]bool{"True": true, "1": true, "ON": true, "yes": true, "\"yes\"": true, "false": false, "0": false, "Off": false, "NO": false} {
			result := LoadAgents(fstest.MapFS{
				"main.md": testMapFile("---\nmodel: gpt-5.4\nsudo: " + value + "\npermission:\n  bash: allow\n---\nPrompt\n"),
			}, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

			require.Empty(t, result.Errors, value)

			permission := result.Agents.Items["main"].Permission
			require.Equal(t, sudo, permission.Sudo, value)

			// Sudo replaces the permission block and allows skills and delegation.
			want := []PermissionBucket{{Name: "bash", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}
			if sudo {
				want = []PermissionBucket{{Name: "skill", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}, {Name: "task", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}
			}

			require.Equal(t, want, permission.Buckets, value)
		}
	})

	t.Run("loads shell values", func(t *testing.T) {
		result := LoadAgents(fstest.MapFS{
			"sandboxed.md": testMapFile("---\ndescription: Sandboxed\nmodel: gpt-5.4\nshell: /opt/sandbox/enter\n---\nPrompt\n"),
			"emulated.md":  testMapFile("---\ndescription: Emulated\nmodel: gpt-5.4\nshell: emulated\n---\nPrompt\n"),
			"host.md":      testMapFile("---\ndescription: Host\nmodel: gpt-5.4\n---\nPrompt\n"),
			"fallback.md":  testMapFile("---\ndescription: Review: code\nmodel: gpt-5.4\nshell: /opt/sandbox/enter\nspace: Billing\n---\nPrompt\n"),
		}, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

		require.Empty(t, result.Errors)
		require.Equal(t, "Billing", result.Agents.Items["fallback"].Space, "space survives fallback sanitization")
		require.Equal(t, CustomShell{Path: "/opt/sandbox/enter"}, result.Agents.Items["sandboxed"].CustomShell)
		require.Equal(t, CustomShell{Emulated: true}, result.Agents.Items["emulated"].CustomShell)
		require.Empty(t, result.Agents.Items["host"].CustomShell)
		require.Equal(t, "Review: code", result.Agents.Items["fallback"].Description)
		require.Equal(t, "/opt/sandbox/enter", result.Agents.Items["fallback"].CustomShell.Path)
	})

	t.Run("supports fallback yaml sanitization", func(t *testing.T) {
		fsys := fstest.MapFS{
			"review.md": testMapFile(`---
description: Review code: security and performance
verbosity: level: high
model: openai/gpt-5.4
---

Strictly follow the rules.
`),
		}

		result := LoadAgents(fsys, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

		require.Empty(t, result.Errors)
		require.Equal(t, "Review code: security and performance", result.Agents.Items["review"].Description)
		require.Equal(t, "level: high", result.Agents.Items["review"].Verbosity)
		require.Equal(t, "openai/gpt-5.4", result.Agents.Items["review"].Model)
	})

	t.Run("keeps prompt shell commands raw", func(t *testing.T) {
		fsys := fstest.MapFS{
			"main.md": testMapFile("---\ndescription: Main\nmodel: gpt-5.4\n---\nUse !`printf expanded` now.\n"),
		}

		result := LoadAgents(fsys, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

		require.Empty(t, result.Errors)
		require.Equal(t, "Use !`printf expanded` now.", result.Agents.Items["main"].Prompt)
	})

	t.Run("preserves prompt bytes after frontmatter", func(t *testing.T) {
		for _, tc := range []struct{ name, input, prompt string }{
			{"unicode", "---\nmodel: example-model\ndescription: café\n---\nRésumé\n", "Résumé"},
			{"crlf", "---\r\nmodel: example-model\r\n---\r\nFirst\r\nsecond\r\n", "First\r\nsecond"},
			{"empty", "---\nmodel: example-model\n---", ""},
			{"raw bytes", "---\nmodel: example-model\n---\n\xff\x00", "\xff\x00"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				result := LoadAgents(fstest.MapFS{"review.md": testMapFile(tc.input)}, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})
				require.Empty(t, result.Errors)
				require.Equal(t, tc.prompt, result.Agents.Items["review"].Prompt)
			})
		}
	})

	t.Run("reports missing frontmatter", func(t *testing.T) {
		content := "# Missing frontmatter\n" + strings.Repeat("x", 64<<10)
		fsys := fstest.MapFS{
			"review.md": testMapFile(content),
		}

		var result AgentLoadResult

		models := &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }}
		cost := testing.Benchmark(func(b *testing.B) {
			b.Helper()

			for b.Loop() {
				result = LoadAgents(fsys, models)
			}
		})

		require.Empty(t, result.Agents.Items)
		require.Len(t, result.Errors, 1)
		require.Equal(t, "review.md: missing YAML frontmatter", result.Errors[0].Error())
		// Allow the file read and loader overhead, but not another full-file copy.
		require.Less(t, cost.AllocedBytesPerOp(), int64(len(content)*3/2))
	})

	t.Run("rejects missing empty or non string model", func(t *testing.T) {
		tests := []struct {
			name        string
			frontmatter string
		}{
			{name: "missing", frontmatter: "description: Main"},
			{name: "empty", frontmatter: "description: Main\nmodel: \"\""},
			{name: "whitespace", frontmatter: "description: Main\nmodel: \"   \""},
			{name: "number", frontmatter: "description: Main\nmodel: 123"},
			{name: "null", frontmatter: "description: Main\nmodel: null"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := LoadAgents(fstest.MapFS{
					"main.md": testMapFile("---\n" + tt.frontmatter + "\n---\nPrompt\n"),
				}, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

				require.Empty(t, result.Agents.Items)
				require.Len(t, result.Errors, 1)
				require.Equal(t, "main.md: model: required non-empty string", result.Errors[0].Error())
			})
		}
	})

	t.Run("rejects empty resolved model", func(t *testing.T) {
		result := LoadAgents(fstest.MapFS{
			"main.md": testMapFile("---\ndescription: Main\nmodel: source-model\n---\nPrompt\n"),
		}, &mockAgentModelRenderer{RenderAgentModelFunc: func(string) (string, error) {
			return "", nil
		}})

		require.Empty(t, result.Agents.Items)
		require.Len(t, result.Errors, 1)
		require.Equal(t, "main.md: model: required non-empty string", result.Errors[0].Error())
	})

	t.Run("ignores mode frontmatter", func(t *testing.T) {
		fsys := fstest.MapFS{
			"main.md": testMapFile("---\ndescription: Main\nmodel: gpt-5.4\nmode: invalid\n---\nPrompt\n"),
		}

		result := LoadAgents(fsys, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

		require.Empty(t, result.Errors)
		require.Equal(t, "Prompt", result.Agents.Items["main"].Prompt)
		require.Equal(t, "invalid", result.Agents.Items["main"].Frontmatter["mode"])
	})

	t.Run("reports invalid yaml after fallback", func(t *testing.T) {
		fsys := fstest.MapFS{
			"review.md": testMapFile("---\ndescription: [unterminated\n---\n"),
		}

		result := LoadAgents(fsys, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

		require.Empty(t, result.Agents.Items)
		require.Len(t, result.Errors, 1)
		require.Contains(t, result.Errors[0].Error(), "review.md: parse YAML frontmatter:")
	})

	t.Run("rejects non map frontmatter", func(t *testing.T) {
		fsys := fstest.MapFS{
			"review.md": testMapFile("---\n- not\n- a\n- map\n---\n"),
		}

		result := LoadAgents(fsys, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

		require.Empty(t, result.Agents.Items)
		require.Len(t, result.Errors, 1)
		require.Contains(t, result.Errors[0].Error(), "review.md: parse YAML frontmatter:")
	})

	t.Run("continues loading valid files when some are invalid", func(t *testing.T) {
		fsys := fstest.MapFS{
			"good.md": testMapFile("---\ndescription: Valid\nmodel: gpt-5.4\n---\nready\n"),
			"bad.md":  testMapFile("---\ndescription: [broken\n---\n"),
		}

		result := LoadAgents(fsys, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

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
		}, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

		require.Empty(t, result.Errors)
		require.Empty(t, result.Agents.Items)
	})

	t.Run("reports read dir failure", func(t *testing.T) {
		result := LoadAgents(failingFS{}, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})

		require.Empty(t, result.Agents.Items)
		require.Len(t, result.Errors, 1)
		require.Equal(t, "read agents dir: permission denied", result.Errors[0].Error())
	})
}
