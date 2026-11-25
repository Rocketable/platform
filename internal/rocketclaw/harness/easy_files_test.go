package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
)

func TestEasyFileView(t *testing.T) {
	t.Parallel()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	base, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, base.Close()) })

	for _, name := range []string{"workspace/agents", "folder", "uploads/chat/up1", "skills/tool/scripts"} {
		require.NoError(t, base.MkdirAll(name, 0o755))
	}

	files := []struct{ name, content string }{
		{"workspace/agents/secret.md", "workspace secret\n"},
		{"secret", "secret\n"},
		{"skills/tool/SKILL.md", "---\nname: tool\n---\n"},
		{"skills/tool/scripts/run.sh", "echo needle\n"},
		{"uploads/chat/up1/report.txt", "upload needle\n"},
		{"folder/note.txt", "hello needle\n"},
	}
	for i, file := range files {
		require.NoError(t, base.WriteFile(file.name, []byte(file.content), 0o644))

		at := time.Unix(int64(1_700_000_000+i), 0)
		require.NoError(t, base.Chtimes(file.name, at, at))
	}

	require.NoError(t, base.Symlink("../workspace/agents/secret.md", "folder/link"))

	workspace, err := base.OpenRoot("workspace")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, workspace.Close()) })

	folder, uploads, skill := filepath.Join(dir, "folder"), filepath.Join(dir, "uploads", "chat"), filepath.Join(dir, "skills", "tool")
	upload := filepath.Join(uploads, "up1", "report.txt")

	factory := &toolFactory{baseTools: makeSandboxedTools(&sandboxedFileSystem{root: workspace}, nil)}
	permission := parsePermissionYAML(t, "read: allow\nedit: allow\nglob: allow\ngrep: allow")
	_, easy := factory.assembleTools(&Agent{Permission: permission, CustomShell: CustomShell{Emulated: true, Dir: folder, ReadOnly: []string{uploads, skill}}})
	_, advanced := factory.assembleTools(&Agent{Permission: permission})

	loaded := LoadAgents(fstest.MapFS{"emulated.md": testMapFile("---\nmodel: gpt-5.4\nshell: emulated\nworkingDirectory: work\npermission:\n  read: allow\n---\nPrompt\n")}, &AgentLoadConfig{})
	require.Empty(t, loaded.Errors)

	emulatedAgent := loaded.Agents.Items["emulated"]
	emulatedAgent.CustomShell.Dir = folder
	_, emulated := factory.assembleTools(&emulatedAgent)

	call := func(hosts map[string]looperTool, name string, params any) string {
		raw, err := json.Marshal(params)
		require.NoError(t, err)

		result, err := hosts[name].Call(t.Context(), raw, nil, toolCallMetadata{})
		if err != nil {
			return "error: " + err.Error()
		}

		return result.Output
	}
	subjects := func(name string, params any) ([]string, error) {
		raw, err := json.Marshal(params)
		require.NoError(t, err)

		return easy[name].Subjects(raw)
	}
	readOutput := func(hosts map[string]looperTool, path string) string {
		raw, err := json.Marshal(readToolParams{FilePath: path})
		require.NoError(t, err)

		result, err := hosts["read"].Call(t.Context(), raw, nil, toolCallMetadata{})
		require.NoError(t, err)

		text, ok := result.Data.(*codemode.TextValue)
		require.True(t, ok)
		require.Equal(t, text.String(), result.Output)

		return numberedView(t, text)
	}
	numbered := func(_, line string) string {
		return "1: " + line
	}

	t.Run("read", func(t *testing.T) {
		require.Equal(t, numbered(filepath.Join(folder, "note.txt"), "hello needle"), readOutput(easy, filepath.Join(folder, "note.txt")))
		require.Equal(t, numbered(filepath.Join(folder, "note.txt"), "hello needle"), readOutput(easy, "note.txt"), "a relative path starts from the folder")
		require.Equal(t, numbered(upload, "upload needle"), readOutput(easy, upload))
		require.Equal(t, "upload needle\n", call(easy, "read", readToolParams{FilePath: upload}))
		require.Equal(t, "error: "+filepath.Join(dir, "secret")+" is outside this agent's folders", call(easy, "read", readToolParams{FilePath: folder + "/../secret"}))
		require.Equal(t, "error: "+filepath.Join(dir, "workspace", "agents", "secret.md")+" is outside this agent's folders", call(easy, "read", readToolParams{FilePath: filepath.Join(dir, "workspace", "agents", "secret.md")}))
		require.Equal(t, "error: File not found: "+filepath.Join(folder, "agents", "secret.md"), call(easy, "read", readToolParams{FilePath: "agents/secret.md"}), "a workspace path is not the folder's")
		require.Equal(t, "error: symlink access denied: link", call(easy, "read", readToolParams{FilePath: filepath.Join(folder, "link")}))
		require.Equal(t, numbered("agents/secret.md", "workspace secret"), readOutput(advanced, "agents/secret.md"), "an agent without a folder reads the workspace")
		require.Equal(t, "error: "+filepath.Join(dir, "secret")+" is outside this agent's folders", call(emulated, "read", readToolParams{FilePath: "../secret"}), "an advanced emulated agent's file tools keep to its folder")
		require.Equal(t, numbered(filepath.Join(folder, "note.txt"), "hello needle"), readOutput(emulated, "note.txt"))

		got, err := subjects("read", readToolParams{FilePath: "note.txt"})
		require.NoError(t, err)
		require.Equal(t, []string{filepath.Join(folder, "note.txt")}, got)
	})

	t.Run("apply_patch", func(t *testing.T) {
		made := filepath.Join(folder, "made", "new.md")
		patch := applyPatchToolParams{PatchText: "*** Begin Patch\n*** Add File: " + made + "\n+made\n*** End Patch"}

		got, err := subjects("apply_patch", patch)
		require.NoError(t, err)
		require.Equal(t, []string{made}, got)
		require.Equal(t, "Success. Updated the following files:\nA "+made, call(easy, "apply_patch", patch))

		data, err := base.ReadFile("folder/made/new.md")
		require.NoError(t, err)
		require.Equal(t, "made\n", string(data))

		patch = applyPatchToolParams{PatchText: "*** Begin Patch\n*** Update File: " + upload + "\n@@\n-upload needle\n+changed\n*** End Patch"}
		_, err = subjects("apply_patch", patch)
		require.EqualError(t, err, "apply_patch verification failed: "+upload+" is read-only")
		require.Equal(t, "error: apply_patch verification failed: "+upload+" is read-only", call(easy, "apply_patch", patch))

		data, err = base.ReadFile("uploads/chat/up1/report.txt")
		require.NoError(t, err)
		require.Equal(t, "upload needle\n", string(data))
	})

	t.Run("glob and grep", func(t *testing.T) {
		require.Equal(t, filepath.Join(folder, "note.txt")+"\n"+upload, call(easy, "glob", globToolParams{Pattern: "**/*.txt"}), "every folder, newest first")
		require.Equal(t, filepath.Join(skill, "scripts", "run.sh"), call(easy, "glob", globToolParams{Pattern: "**/*.sh", Path: skill}))
		require.Equal(t, "No files found", call(easy, "glob", globToolParams{Pattern: "**/*.md", Path: uploads}))
		require.Equal(t, "error: "+filepath.Join(dir, "workspace")+" is outside this agent's folders", call(easy, "glob", globToolParams{Pattern: "**/*.md", Path: filepath.Join(dir, "workspace")}))

		require.Equal(t, "Found 3 matches\n"+filepath.Join(folder, "note.txt")+":\n  Line 1: hello needle\n\n"+upload+":\n  Line 1: upload needle\n\n"+filepath.Join(skill, "scripts", "run.sh")+":\n  Line 1: echo needle", call(easy, "grep", grepToolParams{Pattern: "needle"}))
		require.Equal(t, "Found 1 matches\n"+upload+":\n  Line 1: upload needle", call(easy, "grep", grepToolParams{Pattern: "needle", Path: upload}))
		require.Equal(t, "No files found", call(easy, "grep", grepToolParams{Pattern: "absent"}))

		got, err := subjects("glob", globToolParams{Pattern: "*"})
		require.NoError(t, err)
		require.Equal(t, []string{folder}, got)
	})
}
