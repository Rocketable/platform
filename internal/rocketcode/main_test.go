package rocketcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
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
	looper, err := New(&client, config, root, Agents{Items: map[string]Agent{
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
	_, err = New(&client, testConfig(""), root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)

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
	_, err = New(&client, testConfig(filepath.Join(dir, "missing")), root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
	require.ErrorContains(t, err, "resolve shell temp dir")
	require.ErrorContains(t, err, "missing")

	_, err = New(&client, testConfig(filePath), root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
	require.EqualError(t, err, "resolve shell temp dir \""+filePath+"\": not a directory")

	outsideDir := t.TempDir()
	_, err = New(&client, testConfig(outsideDir), root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
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
	config := testWorkspaceConfig(t, dir)
	config.ShellEnv = env
	loop, err := New(&client, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Description: "", Model: "gpt-5.4", ReasoningEffort: "", Verbosity: "", MaxRecursion: nil, Prompt: "prompt", Location: "", Permission: PermissionSet{Buckets: []PermissionBucket{{Name: "shell", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}, Frontmatter: nil, FileMode: 0},
	}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "main", nil)
	require.NoError(t, err)

	env["ROCKETCLAW_CONVERSATION_ID"] = "second"

	bash, ok := loop.CodeModeHosts["shell"]
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
	_, err = New(&client, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Description: "", Model: "gpt-5.4", ReasoningEffort: "", Verbosity: "", MaxRecursion: nil, Prompt: "env !`printf %s \"$ROCKETCLAW_CONVERSATION_ID\"` tmp !`printf %s \"$TMPDIR\"`", Location: "", Permission: PermissionSet{Buckets: nil}, Frontmatter: nil, FileMode: 0},
	}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "main", &diagnostics)

	require.NoError(t, err)
	require.Contains(t, diagnostics.String(), "env prompt tmp "+filepath.Join(dir, ".tmp", "shell-tmp"))
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
  parent: allow
  dupe: allow
read:
  "skills/parent/private*": deny
  "skills/parent/review*": auto`)
	agents := Agents{Items: map[string]Agent{
		"main":   {Name: "main", Model: "gpt-5.4", Permission: permissions},
		"worker": {Name: "worker", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `skill: {child: allow}`)},
		"review": {Name: "review", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `skill: {parent: auto}`)},
	}}
	client := openai.NewClient()

	loop, err := New(&client, testWorkspaceConfig(t, dir), root, agents, skills, "main", nil)
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
	require.NotContains(t, loop.Tools, "shell")
	require.NotContains(t, loop.Tools, "python3")
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

func TestRuntimeRestrictTools(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	agent := Agent{Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `skill: {demo: allow}`)}
	skills := LoadSkills(fstest.MapFS{"demo/SKILL.md": mapFile("---\nname: demo\ndescription: Demo\n---\n")}, "/virtual/skills").Skills

	newRuntime := func(t *testing.T) *Runtime {
		t.Helper()

		runtime, err := New(&client, testWorkspaceConfig(t, dir), root, Agents{Items: map[string]Agent{"main": agent}}, skills, "main", nil)
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

	loop, err := New(&client, testWorkspaceConfig(t, dir), root, Agents{Items: map[string]Agent{"main": agent}}, skills, "main", nil)
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

	loop, err := New(&client, testWorkspaceConfig(t, dir), root, Agents{Items: map[string]Agent{"main": agent}}, skills, "main", nil)
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

	loop, err := New(&client, testWorkspaceConfig(t, dir), root, Agents{Items: map[string]Agent{"main": agent}}, skills, "main", nil)
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

	loop, err := New(&client, testWorkspaceConfig(t, dir), root, Agents{Items: map[string]Agent{"main": agent}}, skills, "main", nil)
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
		requireRootSymlink(t, root, "skill-source", "skills-link")
	})

	// os.DirFS intentionally follows the host symlink so construction can reject its root.
	skills := LoadSkills(os.DirFS(filepath.Join(dir, "skills-link")), filepath.Join(dir, "skills-link")).Skills
	agent := Agent{Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `skill: {linked: allow}`)}
	client := openai.NewClient()

	loop, err := New(&client, testWorkspaceConfig(t, dir), root, Agents{Items: map[string]Agent{"main": agent}}, skills, "main", nil)
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

	_, err = New(&client, config, root, Agents{Items: nil}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
	require.EqualError(t, err, "agents are required")

	_, err = New(&client, config, root, Agents{Items: map[string]Agent{}}, Skills{Root: "", Items: nil, Dirs: nil, fsys: nil}, "", nil)
	require.EqualError(t, err, "skills are required")

	_, err = New(&client, config, root, Agents{Items: map[string]Agent{}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "", nil)
	require.EqualError(t, err, "defaultAgent is required")

	_, err = New(&client, config, root, Agents{Items: map[string]Agent{}}, Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}, "main", nil)
	require.EqualError(t, err, `missing required default agent "main"`)
}

func TestNewRejectsMissingGuardrailAgent(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	config := testWorkspaceConfig(t, dir)
	agents := Agents{Items: map[string]Agent{"main": {Name: "main", Model: "gpt-5.4", Guardrail: "safety"}}}
	skills := Skills{Root: "", Items: map[string]Skill{}, Dirs: nil, fsys: nil}

	_, err = New(&client, config, root, agents, skills, "main", nil)

	require.EqualError(t, err, `agent "main" references missing guardrail agent "safety"`)
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

		_, err := New(&client, config, root, agents, skills, "main", nil)

		require.NoError(t, err)
	})

	t.Run("enabled rejects guardian agent", func(t *testing.T) {
		config := testWorkspaceConfig(t, dir)
		config.AutoApprovePermissions = true
		agents := Agents{Items: map[string]Agent{"main": {Name: "main", Model: "gpt-5.4"}, "guardian": {Name: "guardian", Model: "gpt-5.4"}}}

		_, err := New(&client, config, root, agents, skills, "main", nil)

		require.EqualError(t, err, `agent name "guardian" is reserved when auto permission approval is enabled`)
	})

	t.Run("enabled rejects missing custom reviewer", func(t *testing.T) {
		config := testWorkspaceConfig(t, dir)
		config.AutoApprovePermissions = true
		agents := Agents{Items: map[string]Agent{"main": {Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `shell: {"deploy *": auto(release-guardian)}`)}}}

		_, err := New(&client, config, root, agents, skills, "main", nil)

		require.ErrorContains(t, err, `references missing reviewer agent "release-guardian"`)
	})

	t.Run("enabled rejects explicit guardian reviewer", func(t *testing.T) {
		config := testWorkspaceConfig(t, dir)
		config.AutoApprovePermissions = true
		agents := Agents{Items: map[string]Agent{"main": {Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `shell: {"deploy *": auto(guardian)}`)}}}

		_, err := New(&client, config, root, agents, skills, "main", nil)

		require.ErrorContains(t, err, `references reserved reviewer "guardian"`)
	})

	t.Run("enabled accepts custom reviewer", func(t *testing.T) {
		config := testWorkspaceConfig(t, dir)
		config.AutoApprovePermissions = true
		agents := Agents{Items: map[string]Agent{
			"main":             {Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, `shell: {"deploy *": auto(release-guardian)}`)},
			"release-guardian": {Name: "release-guardian", Model: "gpt-5.4"},
		}}

		_, err := New(&client, config, root, agents, skills, "main", nil)

		require.NoError(t, err)
	})
}

func testConfig(shellTempDir string) *Config {
	return &Config{Model: "", ReasoningEffort: "", Diagnostics: false, ExperimentalStrongerSkills: false, ExpandPromptShellCommands: PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false, InputPrompts: false}, CompactThreshold: 0, CompactionSteering: "", ParallelToolCalls: 0, ShellTempDir: shellTempDir, AutoApprovePermissions: false, Observability: ObservabilityConfig{}, ChildRunLogger: DiscardChildRunLog, CheckpointSink: InertCheckpointSink{}, CustomTools: nil, ShellEnv: nil, ShellCommand: DefaultShellCommand}
}

func testWorkspaceConfig(t *testing.T, workspace string) *Config {
	t.Helper()

	tempDir := filepath.Join(workspace, ".tmp", "shell-tmp")
	require.NoError(t, os.MkdirAll(tempDir, 0o755))

	return testConfig(tempDir)
}
