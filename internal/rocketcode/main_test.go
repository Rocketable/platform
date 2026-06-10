package rocketcode

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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

	config := testConfig(dir)
	config.Diagnostics = true
	config.ExpandPromptShellCommands.PrimaryPrompts = true
	looper, err := New(&client, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Description: "", Model: "", ReasoningEffort: "", Verbosity: "", MaxRecursion: nil, Prompt: "remember !`cat MEMORY.md`", Location: "", Permission: PermissionSet{Buckets: nil}, Frontmatter: nil, FileMode: 0},
	}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "main", &diagnostics)

	require.NoError(t, err)
	require.NotNil(t, looper)
	require.Contains(t, diagnostics.String(), "remember workspace memory\n\n<current-workspace>\nWorkspace root: "+dir+"\n</current-workspace>")
}

func TestNewRequiresShellOutputDir(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	_, err = New(&client, testConfig(""), root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)

	require.EqualError(t, err, "shell output dir is required")
}

func TestNewRejectsInvalidShellOutputDir(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	filePath := filepath.Join(dir, "file")

	require.NoError(t, root.WriteFile("file", []byte("not a dir"), 0o644))

	client := openai.NewClient()
	_, err = New(&client, testConfig(filepath.Join(dir, "missing")), root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
	require.ErrorContains(t, err, "resolve shell output dir")
	require.ErrorContains(t, err, "missing")

	_, err = New(&client, testConfig(filePath), root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
	require.EqualError(t, err, "resolve shell output dir \""+filePath+"\": not a directory")

	outsideDir := t.TempDir()
	_, err = New(&client, testConfig(outsideDir), root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
	require.EqualError(t, err, "resolve shell output dir \""+outsideDir+"\": must be inside workspace root")
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
			config := testConfig(dir)
			config.ShellEnv = tc.env
			_, err := New(&client, config, root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)

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
	config := testConfig(dir)
	config.ShellEnv = env
	loop, err := New(&client, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Description: "", Model: "", ReasoningEffort: "", Verbosity: "", MaxRecursion: nil, Prompt: "prompt", Location: "", Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "bash", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}, Frontmatter: nil, FileMode: 0},
	}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "main", nil)
	require.NoError(t, err)

	env["ROCKETCLAW_CONVERSATION_ID"] = "second"
	runtimeLoop, ok := loop.(*looper)
	require.True(t, ok)

	result, err := runtimeLoop.Tools["bash"].Call(context.Background(), json.RawMessage(`{"command":"printf %s \"$ROCKETCLAW_CONVERSATION_ID\"","timeout":0,"workdir":"","description":"env mutation"}`), nil, toolCallMetadata{subagentIndex: 0, subagentTotal: 0})

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

	config := testConfig(dir)
	config.Diagnostics = true
	config.ExpandPromptShellCommands.PrimaryPrompts = true
	config.ShellEnv = map[string]string{"ROCKETCLAW_CONVERSATION_ID": "prompt", "TMPDIR": "/ignored"}
	_, err = New(&client, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Description: "", Model: "", ReasoningEffort: "", Verbosity: "", MaxRecursion: nil, Prompt: "env !`printf %s \"$ROCKETCLAW_CONVERSATION_ID\"` tmp !`printf %s \"$TMPDIR\"`", Location: "", Permission: PermissionSet{Buckets: nil}, Frontmatter: nil, FileMode: 0},
	}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "main", &diagnostics)

	require.NoError(t, err)
	require.Contains(t, diagnostics.String(), "env prompt tmp "+filepath.Join(dir, "tmp"))
}

func TestNewSandboxedBashConfigAppliesToBashTool(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	config := testConfig(dir)
	config.SandboxedBash = true

	loop, err := New(&client, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Description: "", Model: "", ReasoningEffort: "", Verbosity: "", MaxRecursion: nil, Prompt: "prompt", Location: "", Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "bash", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}, Frontmatter: nil, FileMode: 0},
	}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "main", nil)
	require.NoError(t, err)

	t.Setenv("PATH", "")

	runtimeLoop, ok := loop.(*looper)
	require.True(t, ok)

	result, err := runtimeLoop.Tools["bash"].Call(context.Background(), json.RawMessage(`{"command":"true","timeout":0,"workdir":"","description":"sandbox"}`), nil, toolCallMetadata{subagentIndex: 0, subagentTotal: 0})

	require.NoError(t, err)
	require.Contains(t, result.Output, "sandboxed bash:")
	require.Contains(t, result.Output, "not found")
}

func TestNewRequiresParsedAgentsAndSkills(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	config := testConfig(dir)

	_, err = New(&client, config, root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
	require.EqualError(t, err, "agents are required")

	_, err = New(&client, config, root, Agents{Items: map[string]Agent{}}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
	require.EqualError(t, err, "skills are required")

	_, err = New(&client, config, root, Agents{Items: map[string]Agent{}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "", nil)
	require.EqualError(t, err, "defaultAgent is required")

	_, err = New(&client, config, root, Agents{Items: map[string]Agent{}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "main", nil)
	require.EqualError(t, err, `missing required default agent "main"`)
}

func testConfig(shellOutputDir string) Config {
	return Config{Model: "", ReasoningEffort: "", Diagnostics: false, ExperimentalStrongerSkills: false, ExpandPromptShellCommands: PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false, InputPrompts: false}, CompactThreshold: 0, CompactionSteering: "", ParallelToolCalls: 0, ShellOutputDir: shellOutputDir, SandboxedBash: false, InterAgentFilter: InterAgentFilterConfig{Prompt: "", Model: "", ReasoningEffort: "", Verbosity: "", Permission: PermissionSet{Buckets: nil}}, CustomTools: nil, ShellEnv: nil}
}
