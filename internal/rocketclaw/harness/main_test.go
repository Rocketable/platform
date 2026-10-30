package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	openai "github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
)

func TestPrintRuntimeDiagnosticsIncludesSystemPrompt(t *testing.T) {
	var (
		out  bytes.Buffer
		tool looperTool
	)

	err := printRuntimeDiagnostics(&out, &Agent{Name: "main", Description: "", Model: "", ReasoningEffort: "", Verbosity: "", MaxRecursion: nil, Prompt: "", Location: "", Permission: PermissionSet{Buckets: nil}, Frontmatter: nil, FileMode: 0}, map[string]looperTool{"find_skills": tool, "skill": tool}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "system prompt text")

	require.NoError(t, err)
	require.Contains(t, out.String(), "agent: main\n")
	require.Contains(t, out.String(), "tools: find_skills, skill\n")
	require.Contains(t, out.String(), "system_prompt:\n---\nsystem prompt text\n---\n")
}

func TestLoadRootInstructions(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })

		got, err := loadRootInstructions(root)

		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("present", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })

		require.NoError(t, root.WriteFile("AGENTS.md", []byte("# Project Rules\nRun make test.\n"), 0o644))

		got, err := loadRootInstructions(root)

		require.NoError(t, err)
		require.Equal(t, "Instructions from: AGENTS.md\n# Project Rules\nRun make test.\n", got)
	})

	t.Run("does not expand shell commands", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })

		require.NoError(t, root.WriteFile("AGENTS.md", []byte("Keep this literal: !`printf expanded`.\n"), 0o644))

		got, err := loadRootInstructions(root)

		require.NoError(t, err)
		require.Equal(t, "Instructions from: AGENTS.md\nKeep this literal: !`printf expanded`.\n", got)
	})
}

func TestNewExpandsPrimaryPromptInRoot(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.WriteFile("MEMORY.md", []byte("workspace memory"), 0o644))

	client := openai.NewClient()

	var diagnostics bytes.Buffer

	config := testWorkspaceConfig(t, dir)
	config.Diagnostics = true
	config.ExpandPromptShellCommands.PrimaryPrompts = true
	looper, err := NewWithProviders(Providers{OpenAI: &client}, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Description: "", Model: "gpt-5.4", ReasoningEffort: "", Verbosity: "", MaxRecursion: nil, Prompt: "remember !`cat MEMORY.md`", Location: "", Permission: PermissionSet{Buckets: nil}, Frontmatter: nil, FileMode: 0},
	}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "main", &diagnostics)

	require.NoError(t, err)
	require.NotNil(t, looper)
	require.Contains(t, diagnostics.String(), "remember workspace memory\n\n<current-workspace>\nWorkspace root: "+dir+"\n</current-workspace>")
}

func TestNewRequiresShellTempDir(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	_, err = NewWithProviders(Providers{OpenAI: &client}, testConfig(""), root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)

	require.EqualError(t, err, "shell temp dir is required")
}

func TestNewRejectsInvalidShellTempDir(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	filePath := filepath.Join(dir, "file")

	require.NoError(t, root.WriteFile("file", []byte("not a dir"), 0o644))

	client := openai.NewClient()
	_, err = NewWithProviders(Providers{OpenAI: &client}, testConfig(filepath.Join(dir, "missing")), root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
	require.ErrorContains(t, err, "resolve shell temp dir")
	require.ErrorContains(t, err, "missing")

	_, err = NewWithProviders(Providers{OpenAI: &client}, testConfig(filePath), root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
	require.EqualError(t, err, "resolve shell temp dir \""+filePath+"\": not a directory")

	outsideDir := t.TempDir()
	_, err = NewWithProviders(Providers{OpenAI: &client}, testConfig(outsideDir), root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
	require.EqualError(t, err, "resolve shell temp dir \""+outsideDir+"\": must be inside workspace root")
}

func TestNewRejectsInvalidShellEnv(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()

	for _, tc := range []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{name: "empty key", env: map[string]string{"": "value"}, wantErr: "shell env key is required"},
		{name: "key contains equals", env: map[string]string{"BAD=KEY": "value"}, wantErr: `shell env key "BAD=KEY" must not contain =`},
		{name: "key contains nul", env: map[string]string{"BAD\x00KEY": "value"}, wantErr: `shell env "BAD\x00KEY" must not contain NUL`},
		{name: "value contains nul", env: map[string]string{"BAD_KEY": "bad\x00value"}, wantErr: `shell env "BAD_KEY" must not contain NUL`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := testWorkspaceConfig(t, dir)
			config.ShellEnv = tc.env
			_, err := NewWithProviders(Providers{OpenAI: &client}, config, root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)

			require.EqualError(t, err, tc.wantErr)
		})
	}
}

func TestNewCopiesShellEnv(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	env := map[string]string{"ROCKETCLAW_CONVERSATION_ID": "first"}
	config := testWorkspaceConfig(t, dir)
	config.ShellEnv = env
	loop, err := NewWithProviders(Providers{OpenAI: &client}, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Description: "", Model: "gpt-5.4", ReasoningEffort: "", Verbosity: "", MaxRecursion: nil, Prompt: "prompt", Location: "", Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "bash", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}, Frontmatter: nil, FileMode: 0},
	}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "main", nil)
	require.NoError(t, err)

	env["ROCKETCLAW_CONVERSATION_ID"] = "second"

	bash, ok := loop.CodeModeHosts["bash"]
	require.True(t, ok)

	result, err := bash.Call(context.Background(), json.RawMessage(`{"command":"printf %s \"$ROCKETCLAW_CONVERSATION_ID\"","timeout_ms":0,"workdir":"","description":"env mutation"}`), nil, toolCallMetadata{subagentIndex: 0, subagentTotal: 0})

	require.NoError(t, err)
	require.Equal(t, "first", result.Output)
}

func TestNewShellEnvAppliesToPromptExpansion(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()

	var diagnostics bytes.Buffer

	config := testWorkspaceConfig(t, dir)
	config.Diagnostics = true
	config.ExpandPromptShellCommands.PrimaryPrompts = true
	config.ShellEnv = map[string]string{"ROCKETCLAW_CONVERSATION_ID": "prompt", "TMPDIR": "/ignored"}
	_, err = NewWithProviders(Providers{OpenAI: &client}, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Description: "", Model: "gpt-5.4", ReasoningEffort: "", Verbosity: "", MaxRecursion: nil, Prompt: "env !`printf %s \"$ROCKETCLAW_CONVERSATION_ID\"` tmp !`printf %s \"$TMPDIR\"`", Location: "", Permission: parsePermissionYAML(t, `edit: {"${ROCKETCLAW_CONVERSATION_ID}/note.txt": allow}`), Frontmatter: nil, FileMode: 0},
	}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "main", &diagnostics)

	require.NoError(t, err)
	require.Contains(t, diagnostics.String(), "env prompt tmp "+filepath.Join(dir, ".tmp", "shell-tmp"))
	require.Contains(t, diagnostics.String(), "prompt/note.txt")
}

// writeCustomShell writes a wrapper that reports its argument count and first argument.
func writeCustomShell(t *testing.T, root *os.Root) string {
	t.Helper()

	require.NoError(t, root.WriteFile("enter.sh", []byte("#!/bin/sh\nprintf 'custom:%s:%s' \"$#\" \"$1\""), 0o755))

	return filepath.Join(root.Name(), "enter.sh")
}

func TestNewBindsBashToEachAgentCustomShell(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	shell := writeCustomShell(t, root)
	agents := LoadAgents(fstest.MapFS{
		"boxed.md": {Data: []byte("---\nmodel: gpt-5.4\nshell: " + shell + "\npermission: {bash: allow, task: allow}\n---\nBOXED")},
		"plain.md": {Data: []byte("---\nmodel: gpt-5.4\npermission: {bash: allow, task: allow}\n---\nPLAIN")},
	}, &mockAgentModelRenderer{RenderAgentModelFunc: func(model string) (string, error) { return model, nil }})
	require.Empty(t, agents.Errors)

	run := func(t *testing.T, tool looperTool) string {
		t.Helper()

		result, err := tool.Call(t.Context(), json.RawMessage(`{"command":"printf plain"}`), nil, emptyToolCallMetadata())
		require.NoError(t, err)

		return result.Output
	}

	const wrapped = "custom:1:printf plain"

	for _, tc := range []struct {
		name, root, other   string
		rootWant, otherWant string
	}{
		{name: "sandboxed root", root: "boxed", other: "plain", rootWant: wrapped, otherWant: "plain"},
		{name: "unsandboxed root", root: "plain", other: "boxed", rootWant: "plain", otherWant: wrapped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loop, err := NewWithModelResolver(testResolverForResponsesAPI(mockResponses()), testWorkspaceConfig(t, dir), root, agents.Agents, Skills{Items: map[string]Skill{}}, tc.root, nil)
			require.NoError(t, err)
			require.Equal(t, tc.rootWant, run(t, loop.CodeModeHosts["bash"]))

			factory := loop.PermissionReviewer.(*toolFactory)
			other := factory.agents.Items[tc.other]

			child, err := factory.newLooper(&other, factory.sessionSystemPrompt(t.Context(), &other))
			require.NoError(t, err)
			require.Equal(t, tc.otherWant, run(t, child.CodeModeHosts["bash"]), "a session switched to another type uses its own shell")
			require.Equal(t, other.CustomShell, child.promptExpansion.customShell, "a session switched to another type expands prompts with its own shell")

			for _, bash := range []looperTool{loop.CodeModeHosts["bash"], child.CodeModeHosts["bash"]} {
				require.Equal(t, strings.Contains(run(t, bash), "custom:"), strings.Contains(bash.Definition.Description.Value, shell), "the bash description names the program only when one is used")
			}

			// Permission reviews bind tools through the same assembleTools.
			_, reviewerHosts := factory.assembleTools(&other)
			require.Equal(t, tc.otherWant, run(t, reviewerHosts["bash"]), "a permission reviewer uses its own shell")

			require.Equal(t, "plain", run(t, factory.baseTools["bash"]), "the shared tool keeps default bash")
			require.Equal(t, tc.rootWant, run(t, loop.CodeModeHosts["bash"]))
		})
	}

	t.Run("emulated shell without a program fails instead of running under bash", func(t *testing.T) {
		loop, err := NewWithModelResolver(testResolverForResponsesAPI(mockResponses()), testWorkspaceConfig(t, dir), root, agents.Agents, Skills{Items: map[string]Skill{}}, "plain", nil)
		require.NoError(t, err)

		emulated := agents.Agents.Items["plain"]
		emulated.CustomShell = CustomShell{Emulated: true}

		_, hosts := loop.PermissionReviewer.(*toolFactory).assembleTools(&emulated)
		require.Equal(t, "shell command path is required", run(t, hosts["bash"]))
	})
}

func TestNewExpandsPromptsThroughRootAgentCustomShell(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	var diagnostics bytes.Buffer

	config := testWorkspaceConfig(t, dir)
	config.Diagnostics = true
	config.ExpandPromptShellCommands.PrimaryPrompts = true
	config.ExpandPromptShellCommands.InputPrompts = true
	client := openai.NewClient()
	loop, err := NewWithProviders(Providers{OpenAI: &client}, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Model: "gpt-5.4", Prompt: "remember !`echo hi`", CustomShell: CustomShell{Path: writeCustomShell(t, root)}},
	}}, Skills{Items: map[string]Skill{}}, "main", &diagnostics)
	require.NoError(t, err)
	require.Contains(t, diagnostics.String(), "remember custom:1:echo hi\n")

	for _, tc := range []struct {
		runtime, input bool
		want           string
	}{
		{true, false, "input custom:1:echo there"},
		{false, true, "input custom:1:echo there"},
		{false, false, "input !`echo there`"},
	} {
		loop.expandInputPrompts = tc.runtime
		input := PromptInput{Role: PromptInputRoleUser, Texts: []string{"input !`echo there`"}, ExpandShellCommands: tc.input}
		_, err = loop.promptTurnItems(t.Context(), &input)
		require.NoError(t, err)
		require.Equal(t, tc.want, input.Texts[0])
	}
}

func TestNewAllowsReadingFilesFromAllowedSkills(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	seed := map[string][]byte{
		"skills/parent/private.txt":        []byte("private"),
		"skills/parent/review.txt":         []byte("review"),
		"skills/parent/reference/guide.md": []byte("guide"),
		"skills/parent/broken/SKILL.md":    []byte("---\nname: broken\n---\n"),
		"skills/parent/broken/asset.txt":   []byte("broken asset"),
	}
	for name, description := range map[string]string{
		"parent":       "Parent",
		"parent/child": "Child",
		"other":        "Other",
		"aaa/dupe":     "First duplicate",
		"zzz/dupe":     "Second duplicate",
	} {
		seed[filepath.Join("skills", name, "SKILL.md")] = fmt.Appendf(nil, "---\nname: %s\ndescription: %s\n---\n", filepath.Base(name), description)
		seed[filepath.Join("skills", name, "asset.txt")] = []byte(name + " asset")
	}

	seedRoot(t, root, seed, nil)

	skillsRoot, err := root.OpenRoot("skills")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, skillsRoot.Close()) })

	skills := LoadSkills(skillsRoot.FS(), skillsRoot.Name()).Skills

	permissions := parsePermissionYAML(t, `skill:
  "*": deny
  "${ROCKETCLAW_PARENT}": allow
  dupe: allow
read:
  "skills/parent/private*": deny
  "skills/parent/review*": auto`)
	agents := Agents{Items: map[string]Agent{
		"main":   {Name: "main", Model: "gpt-5.4", Permission: permissions},
		"worker": {Name: "worker", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `skill: {"${ROCKETCLAW_CHILD}": allow}`)},
		"review": {Name: "review", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `skill: {parent: auto}`)},
	}}
	client := openai.NewClient()

	config := testWorkspaceConfig(t, dir)
	config.ShellEnv = map[string]string{"ROCKETCLAW_PARENT": "parent", "ROCKETCLAW_CHILD": "child"}
	loop, err := NewWithProviders(Providers{OpenAI: &client}, config, root, agents, skills, "main", nil)
	require.NoError(t, err)

	for _, tt := range []struct {
		path   string
		action PermissionAction
	}{
		{path: "skills/parent/asset.txt", action: permissionAllow},
		{path: "skills/parent/reference/guide.md", action: permissionAllow},
		{path: "skills/zzz/dupe/asset.txt", action: permissionAllow},
		{path: "skills/parent/private.txt", action: permissionDeny},
		{path: "skills/parent/review.txt", action: permissionAuto},
		{path: "skills/parent/child/asset.txt", action: permissionDeny},
		{path: "skills/other/asset.txt", action: permissionDeny},
		{path: "skills/aaa/dupe/asset.txt", action: permissionDeny},
		{path: "skills/parent/broken/asset.txt", action: permissionDeny},
	} {
		action, _ := loop.Permissions.Evaluate("read", tt.path)
		require.Equal(t, tt.action, action, tt.path)
	}

	caseVariantChild := "skills/parent/CHILD/asset.txt"
	if _, err := root.Stat(caseVariantChild); err == nil {
		action, _ := loop.Permissions.Evaluate("read", caseVariantChild)
		require.Equal(t, PermissionDeny, action)
		action, _ = loop.Permissions.Evaluate("read", "skills/parent/PRIVATE.TXT")
		require.Equal(t, PermissionDeny, action)
		action, _ = loop.Permissions.Evaluate("read", "skills/parent/REVIEW.TXT")
		require.Equal(t, PermissionAuto, action)
	}

	require.Contains(t, loop.Tools, "execute")
	require.Contains(t, loop.Tools, "skill")
	require.NotContains(t, loop.Tools, "read")
	require.NotContains(t, loop.Tools, "edit")
	require.NotContains(t, loop.Tools, "bash")
	require.NotContains(t, loop.Tools, "glob")
	require.NotContains(t, loop.Tools, "grep")
	require.Contains(t, loop.CodeModeHosts, "read")
	require.NotContains(t, loop.SystemPrompt, "skills/parent/asset.txt")

	readTool := loop.CodeModeHosts["read"]
	readArgs := json.RawMessage(`{"filePath":"skills/parent/asset.txt"}`)
	decision, err := loop.permissionDecision("read", &readTool, readArgs)
	require.NoError(t, err)
	require.False(t, decision.denied)

	deniedArgs := json.RawMessage(`{"filePath":"skills/parent/child/asset.txt"}`)
	decision, err = loop.permissionDecision("read", &readTool, deniedArgs)
	require.NoError(t, err)
	require.True(t, decision.denied)

	result, err := readTool.Call(context.Background(), readArgs, nil, emptyToolCallMetadata())
	require.NoError(t, err)
	require.Contains(t, result.Output, "1: parent asset")

	skillResult, err := loop.Tools["skill"].Call(context.Background(), json.RawMessage(`{"name":"parent"}`), nil, emptyToolCallMetadata())
	require.NoError(t, err)
	require.NotContains(t, skillResult.Output, "parent/broken/asset.txt")

	action, matched := agents.Items["main"].Permission.Evaluate("read", "skills/parent/asset.txt")
	require.Equal(t, PermissionDeny, action)
	require.False(t, matched)

	factory := loop.PermissionReviewer.(*toolFactory)
	worker := factory.agents.Items["worker"]
	action, _ = worker.Permission.Evaluate("read", "skills/parent/child/asset.txt")
	require.Equal(t, PermissionAllow, action)
	action, _ = worker.Permission.Evaluate("read", "skills/parent/asset.txt")
	require.Equal(t, PermissionDeny, action)
	action, _ = factory.agents.Items["review"].Permission.Evaluate("read", "skills/parent/asset.txt")
	require.Equal(t, PermissionDeny, action)
}

func TestExecuteSpillStorageIsPrivate(t *testing.T) {
	for _, spillRel := range []string{defaultSpillRel, "private output/spill"} {
		t.Run(spillRel, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			config := testWorkspaceConfig(t, root.Name())
			config.SpillDir = filepath.Join(root.Name(), spillRel)
			client := openai.NewClient()
			agent := Agent{Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, "read: {'*': allow}\nedit: {'*': allow}\nbash: {'*': allow}\nglob: {'*': allow}\ngrep: {'*': allow}")}
			loop, err := NewWithProviders(Providers{OpenAI: &client}, config, root, Agents{Items: map[string]Agent{"main": agent}}, Skills{Items: map[string]Skill{}}, "main", nil)
			require.NoError(t, err)
			loop.restoreTurnExecuteResults("turn")
			t.Cleanup(loop.deleteTurnExecuteResults)

			full := strings.Repeat("private-result\n", 2100)
			_, err = loop.saveExecuteResult(full)
			require.NoError(t, err)
			require.NoError(t, root.WriteFile("ordinary.txt", []byte("ordinary\n"), 0o600))

			ctx := withToolCallContext(t.Context(), loop, nil, "")
			for id, stored := range loop.spillResults {
				paths := []string{stored, filepath.Join(root.Name(), stored), filepath.Dir(stored) + "/../turn/" + filepath.Base(stored)}
				if _, err := root.Stat(strings.ToUpper(spillRel)); err == nil {
					paths = append(paths, strings.ToUpper(spillRel)+"/turn/"+filepath.Base(stored))
				}
				// Host stat is needed to detect aliases of the workspace prefix itself.
				if info, err := os.Stat(strings.ToUpper(root.Name())); err == nil {
					original, err := root.Stat(".")
					require.NoError(t, err)

					if os.SameFile(original, info) {
						paths = append(paths, filepath.Join(strings.ToUpper(root.Name()), stored))
					}
				}

				for _, path := range paths {
					literal := strings.ReplaceAll(path, " ", `\ `)

					partial := strings.Replace(literal, "/spill", `/""spill`, 1)
					for _, input := range []struct {
						name   string
						params any
					}{
						{"read", readToolParams{FilePath: path}},
						{"glob", globToolParams{Pattern: "*", Path: filepath.Dir(path)}},
						{"grep", grepToolParams{Pattern: "private-result", Path: path}},
						{"apply_patch", applyPatchToolParams{PatchText: "*** Begin Patch\n*** Delete File: " + path + "\n*** End Patch"}},
						{"apply_patch", applyPatchToolParams{PatchText: "*** Begin Patch\n*** Add File: " + path + "\n+overwritten\n*** End Patch"}},
						{"apply_patch", applyPatchToolParams{PatchText: "*** Begin Patch\n*** Update File: " + path + "\n@@\n-private-result\n+overwritten\n*** End Patch"}},
						{"apply_patch", applyPatchToolParams{PatchText: "*** Begin Patch\n*** Update File: ordinary.txt\n*** Move to: " + path + "\n@@\n-ordinary\n+overwritten\n*** End Patch"}},
						{"bash", bashParams{Command: fmt.Sprintf("cat %q", path)}},
						{"bash", bashParams{Command: "cat " + partial}},
						{"bash", bashParams{Command: "cat " + literal}},
						{"bash", bashParams{Command: "cat $'" + path + "'"}},
						{"bash", bashParams{Command: "printf overwritten > " + literal}},
						{"bash", bashParams{Command: "printf -- --file=" + partial}},
						{"bash", bashParams{Command: "printf overwritten > " + partial}},
						{"bash", bashParams{Command: "cat < " + partial}},
						{"bash", bashParams{Command: fmt.Sprintf("printf %%s %q", path)}},
						{"bash", bashParams{Command: fmt.Sprintf("printf -- %q", "--file="+path)}},
						{"bash", bashParams{Command: fmt.Sprintf("printf -- --file=%q", path)}},
						{"bash", bashParams{Command: fmt.Sprintf("printf overwritten > %q", path)}},
						{"bash", bashParams{Command: fmt.Sprintf("cat < %q", path)}},
						{"bash", bashParams{Command: "pwd", Workdir: filepath.Dir(path)}},
						{"bash", bashParams{Command: fmt.Sprintf("cat %q", filepath.Base(path)), Workdir: filepath.Dir(path)}},
					} {
						raw, err := json.Marshal(input.params)
						require.NoError(t, err)

						tool := loop.CodeModeHosts[input.name]
						result, err := tool.Call(ctx, raw, nil, emptyToolCallMetadata())
						require.NoError(t, err)

						if filepath.IsAbs(path) && !strings.HasPrefix(path, root.Name()+"/") {
							// Root aliases may be rejected by an earlier path boundary.
							require.Regexp(t, "execute output storage is private|path escapes root|bash command denied: external path access is blocked", result.Output, "%s(%s)", input.name, raw)
						} else {
							require.Contains(t, result.Output, deniedSpillAccess, "%s(%s)", input.name, raw)
						}
					}
				}

				grep := loop.CodeModeHosts["grep"]
				result, err := grep.Call(ctx, json.RawMessage(`{"pattern":"private-result"}`), nil, emptyToolCallMetadata())
				require.NoError(t, err)
				require.Equal(t, "No files found", result.Output)

				glob := loop.CodeModeHosts["glob"]
				result, err = glob.Call(ctx, json.RawMessage(`{"pattern":"**/*.txt"}`), nil, emptyToolCallMetadata())
				require.NoError(t, err)
				require.NotContains(t, result.Output, spillRel)
				require.Contains(t, result.Output, "ordinary.txt")
				require.NoError(t, root.MkdirAll(spillRel+"-other", 0o700))
				ordinary := spillRel + "-other/file.txt"
				require.NoError(t, root.WriteFile(ordinary, []byte("ordinary"), 0o600))
				ordinaryPaths := []string{ordinary}

				if _, err := root.Stat(strings.ToUpper(spillRel)); os.IsNotExist(err) {
					require.NoError(t, root.MkdirAll(strings.ToUpper(spillRel), 0o700))
					distinct := strings.ToUpper(spillRel) + "/ordinary.txt"
					require.NoError(t, root.WriteFile(distinct, []byte("ordinary"), 0o600))
					ordinaryPaths = append(ordinaryPaths, distinct)
				}

				for _, ordinary := range ordinaryPaths {
					read := loop.CodeModeHosts["read"]
					args, err := json.Marshal(readToolParams{FilePath: ordinary})
					require.NoError(t, err)
					result, err = read.Call(ctx, args, nil, emptyToolCallMetadata())
					require.NoError(t, err)
					require.Contains(t, result.Output, "1: ordinary")

					bash := loop.CodeModeHosts["bash"]
					args, err = json.Marshal(bashParams{Command: fmt.Sprintf("cat %q", ordinary)})
					require.NoError(t, err)
					result, err = bash.Call(ctx, args, nil, emptyToolCallMetadata())
					require.NoError(t, err)
					require.Equal(t, "ordinary", result.Output)
				}

				page, err := loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id, Limit: 1})
				require.NoError(t, err)
				require.Equal(t, "private-result\n\n[next_start_line=2]\n", page.Output)

				raw, err := root.ReadFile(stored)
				require.NoError(t, err)
				require.Equal(t, full, string(raw))
			}
		})
	}
}

func TestRuntimeRestrictTools(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	agent := Agent{Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, "skill: {demo: allow}\nread: {'*': allow}")}
	skills := LoadSkills(fstest.MapFS{"demo/SKILL.md": mapFile("---\nname: demo\ndescription: Demo\n---\n")}, "/virtual/skills").Skills

	newRuntime := func(t *testing.T) *Runtime {
		t.Helper()

		runtime, err := NewWithProviders(Providers{OpenAI: &client}, testWorkspaceConfig(t, dir), root, Agents{Items: map[string]Agent{"main": agent}}, skills, "main", nil)
		require.NoError(t, err)

		return runtime
	}

	t.Run("nil keeps all derived tools", func(t *testing.T) {
		runtime := newRuntime(t)
		before := slices.Sorted(maps.Keys(runtime.Tools))
		require.NoError(t, runtime.RestrictTools(nil))
		require.Equal(t, before, slices.Sorted(maps.Keys(runtime.Tools)))
	})

	t.Run("exact and empty allowlists", func(t *testing.T) {
		runtime := newRuntime(t)
		require.Contains(t, runtime.Tools, "skill")
		require.Contains(t, runtime.Tools, "find_skills")
		require.Contains(t, runtime.Tools, loadExecuteResultToolName)
		require.NoError(t, runtime.RestrictTools([]string{"skill", executeToolName}))
		runtime.restoreTurnExecuteResults("restricted")
		_, err := runtime.saveExecuteResult(strings.Repeat("line\n", 2100))
		require.NoError(t, err)
		runtime.deleteTurnExecuteResults()
		require.NotContains(t, runtime.Tools, loadExecuteResultToolName)
		require.NoError(t, runtime.RestrictTools([]string{"skill"}))
		require.Equal(t, []string{"skill"}, slices.Sorted(maps.Keys(runtime.Tools)))
		require.NoError(t, runtime.RestrictTools([]string{}))
		require.Empty(t, runtime.Tools)
	})

	for _, tc := range []struct {
		name  string
		tools []string
	}{
		{name: "unknown", tools: []string{"missing"}},
		{name: "duplicate", tools: []string{"skill", "skill"}},
	} {
		t.Run(tc.name+" is atomic", func(t *testing.T) {
			runtime := newRuntime(t)
			before := slices.Sorted(maps.Keys(runtime.Tools))
			require.Error(t, runtime.RestrictTools(tc.tools))
			require.Equal(t, before, slices.Sorted(maps.Keys(runtime.Tools)))
		})
	}
}

func TestNewDoesNotAllowReadingFilesFromVirtualSkills(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	skills := LoadSkills(fstest.MapFS{
		"virtual/SKILL.md":  mapFile("---\nname: virtual\ndescription: Virtual\n---\n"),
		"virtual/asset.txt": mapFile("asset"),
	}, "/virtual/skills").Skills
	agent := Agent{Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `skill: {virtual: allow}`)}
	client := openai.NewClient()

	loop, err := NewWithProviders(Providers{OpenAI: &client}, testWorkspaceConfig(t, dir), root, Agents{Items: map[string]Agent{"main": agent}}, skills, "main", nil)
	require.NoError(t, err)
	require.NotContains(t, loop.Tools, "read")
}

func TestNewDoesNotTrustVirtualSkillsWithWorkspaceDisplayPath(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	seedRoot(t, root, map[string][]byte{
		"skills/virtual/SKILL.md":   []byte("---\nname: virtual\ndescription: Physical\n---\n"),
		"skills/virtual/secret.txt": []byte("secret"),
	}, nil)

	skills := LoadSkills(fstest.MapFS{
		"virtual/SKILL.md": mapFile("---\nname: virtual\ndescription: Virtual\n---\n"),
	}, filepath.Join(dir, "skills")).Skills
	agent := Agent{Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `skill: {virtual: allow}`)}
	client := openai.NewClient()

	loop, err := NewWithProviders(Providers{OpenAI: &client}, testWorkspaceConfig(t, dir), root, Agents{Items: map[string]Agent{"main": agent}}, skills, "main", nil)
	require.NoError(t, err)
	require.NotContains(t, loop.Tools, "read")
}

func TestNewDoesNotTrustExternalSkillDirectoryWithLinkedMarker(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	seedRoot(t, root, map[string][]byte{
		"skills/external/SKILL.md":   []byte("---\nname: external\ndescription: External\n---\n"),
		"skills/external/secret.txt": []byte("secret"),
	}, nil)
	external := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(external, "external"), 0o755))
	// A host hard link is required to prove that matching SKILL.md files do not establish directory identity.
	require.NoError(t, os.Link(filepath.Join(dir, "skills", "external", "SKILL.md"), filepath.Join(external, "external", "SKILL.md")))
	skills := LoadSkills(os.DirFS(external), filepath.Join(dir, "skills")).Skills
	agent := Agent{Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `skill: {external: allow}`)}
	client := openai.NewClient()

	loop, err := NewWithProviders(Providers{OpenAI: &client}, testWorkspaceConfig(t, dir), root, Agents{Items: map[string]Agent{"main": agent}}, skills, "main", nil)
	require.NoError(t, err)
	require.NotContains(t, loop.Tools, "read")
}

func TestNewDoesNotAllowReadingFilesFromExternalSkills(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	// This fixture must live outside the sandbox root to exercise rejection.
	external := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(external, "external"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(external, "external", "SKILL.md"), []byte("---\nname: external\ndescription: External\n---\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(external, "external", "asset.txt"), []byte("asset"), 0o644))
	skills := LoadSkills(os.DirFS(external), external).Skills
	agent := Agent{Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `skill: {external: allow}`)}
	client := openai.NewClient()

	loop, err := NewWithProviders(Providers{OpenAI: &client}, testWorkspaceConfig(t, dir), root, Agents{Items: map[string]Agent{"main": agent}}, skills, "main", nil)
	require.NoError(t, err)
	require.NotContains(t, loop.Tools, "read")
}

func TestNewDoesNotAllowReadingFilesThroughSymlinkedSkillRoot(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	seedRoot(t, root, map[string][]byte{
		"skill-source/linked/SKILL.md":  []byte("---\nname: linked\ndescription: Linked\n---\n"),
		"skill-source/linked/asset.txt": []byte("asset"),
	}, func(t *testing.T, root *os.Root) {
		t.Helper()
		require.NoError(t, root.Symlink("skill-source", "skills-link"))
	})

	// os.DirFS intentionally follows the host symlink so construction can reject its root.
	skills := LoadSkills(os.DirFS(filepath.Join(dir, "skills-link")), filepath.Join(dir, "skills-link")).Skills
	agent := Agent{Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `skill: {linked: allow}`)}
	client := openai.NewClient()

	loop, err := NewWithProviders(Providers{OpenAI: &client}, testWorkspaceConfig(t, dir), root, Agents{Items: map[string]Agent{"main": agent}}, skills, "main", nil)
	require.NoError(t, err)
	require.NotContains(t, loop.Tools, "read")
}

func TestNewRequiresParsedAgentsAndSkills(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	config := testWorkspaceConfig(t, dir)

	_, err = NewWithProviders(Providers{OpenAI: &client}, config, root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
	require.EqualError(t, err, "agents are required")

	_, err = NewWithProviders(Providers{OpenAI: &client}, config, root, Agents{Items: map[string]Agent{}}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
	require.EqualError(t, err, "skills are required")

	_, err = NewWithProviders(Providers{OpenAI: &client}, config, root, Agents{Items: map[string]Agent{}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "", nil)
	require.EqualError(t, err, "defaultAgent is required")

	_, err = NewWithProviders(Providers{OpenAI: &client}, config, root, Agents{Items: map[string]Agent{}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "main", nil)
	require.EqualError(t, err, `missing required default agent "main"`)
}

func TestNewValidatesAutoPermissionReviewers(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	skills := Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}

	t.Run("disabled allows guardian agent", func(t *testing.T) {
		config := testWorkspaceConfig(t, dir)
		agents := Agents{Items: map[string]Agent{"main": {Name: "main", Model: "gpt-5.4"}, "guardian": {Name: "guardian", Model: "gpt-5.4"}}}

		_, err := NewWithProviders(Providers{OpenAI: &client}, config, root, agents, skills, "main", nil)

		require.NoError(t, err)
	})

	t.Run("enabled rejects guardian agent", func(t *testing.T) {
		config := testWorkspaceConfig(t, dir)
		config.AutoApprovePermissions = true
		agents := Agents{Items: map[string]Agent{"main": {Name: "main", Model: "gpt-5.4"}, "guardian": {Name: "guardian", Model: "gpt-5.4"}}}

		_, err := NewWithProviders(Providers{OpenAI: &client}, config, root, agents, skills, "main", nil)

		require.EqualError(t, err, `agent name "guardian" is reserved when auto permission approval is enabled`)
	})

	t.Run("enabled rejects missing custom reviewer", func(t *testing.T) {
		config := testWorkspaceConfig(t, dir)
		config.AutoApprovePermissions = true
		agents := Agents{Items: map[string]Agent{"main": {Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `bash: {"deploy *": auto(release-guardian)}`)}}}

		_, err := NewWithProviders(Providers{OpenAI: &client}, config, root, agents, skills, "main", nil)

		require.ErrorContains(t, err, `references missing reviewer agent "release-guardian"`)
	})

	t.Run("enabled rejects explicit guardian reviewer", func(t *testing.T) {
		config := testWorkspaceConfig(t, dir)
		config.AutoApprovePermissions = true
		agents := Agents{Items: map[string]Agent{"main": {Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `bash: {"deploy *": auto(guardian)}`)}}}

		_, err := NewWithProviders(Providers{OpenAI: &client}, config, root, agents, skills, "main", nil)

		require.ErrorContains(t, err, `references reserved reviewer "guardian"`)
	})

	t.Run("enabled accepts custom reviewer", func(t *testing.T) {
		config := testWorkspaceConfig(t, dir)
		config.AutoApprovePermissions = true
		agents := Agents{Items: map[string]Agent{
			"main":             {Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `bash: {"deploy *": auto(release-guardian)}`)},
			"release-guardian": {Name: "release-guardian", Model: "gpt-5.4"},
		}}

		_, err := NewWithProviders(Providers{OpenAI: &client}, config, root, agents, skills, "main", nil)

		require.NoError(t, err)
	})
}

func testConfig(shellTempDir string) *Config {
	return &Config{Model: "", ReasoningEffort: "", Diagnostics: false, ExperimentalStrongerSkills: false, ExpandPromptShellCommands: PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false, InputPrompts: false}, CompactThreshold: 0, CompactionSteering: "", ParallelToolCalls: 0, ShellTempDir: shellTempDir, RetainedResultDir: "retained", AutoApprovePermissions: false, Observability: ObservabilityConfig{}, ChildSessions: InertChildSessions{}, Journal: InertJournal{}, BackgroundJobs: InertBackgroundJobs{}, CustomTools: nil, ShellEnv: nil, ShellCommand: DefaultShellCommand{}}
}

func testWorkspaceConfig(t *testing.T, workspace string) *Config {
	t.Helper()

	tempDir := filepath.Join(workspace, ".tmp", "shell-tmp")
	require.NoError(t, os.MkdirAll(tempDir, 0o755))

	return testConfig(tempDir)
}
