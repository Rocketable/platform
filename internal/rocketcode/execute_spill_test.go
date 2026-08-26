package rocketcode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClipExecuteHead(t *testing.T) {
	t.Parallel()

	small := strings.Repeat("a\n", 10)
	got, oversized := clipExecuteHead(small)
	require.False(t, oversized)
	require.Equal(t, small, got)

	var many strings.Builder
	for i := range 2100 {
		many.WriteString("line\n")

		_ = i
	}

	head, oversized := clipExecuteHead(many.String())
	require.True(t, oversized)
	require.Equal(t, 2000, strings.Count(head, "\n"))
	require.NotContains(t, head, "output truncated")

	long := strings.Repeat("x", executeHeadMaxBytes+10)
	head, oversized = clipExecuteHead(long)
	require.True(t, oversized)
	require.LessOrEqual(t, len(head), executeHeadMaxBytes)
}

func TestSpillExecuteOutput(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	sfs := &sandboxedFileSystem{root: root}
	readTool := makeSandboxedTools(sfs, nil)["read"]
	loop := &looper{
		promptExpansion: promptExpansionEnvironment{root: root},
		spillRel:        defaultSpillRel,
		sandboxRead:     readTool,
		Permissions:     PermissionSet{},
		CodeModeHosts:   map[string]looperTool{},
	}
	loop.beginTurnSpills("turn-1")

	small, err := loop.spillExecuteOutput("ok")
	require.NoError(t, err)
	require.Equal(t, "ok", small)

	var many strings.Builder
	for range 2100 {
		many.WriteString("line\n")
	}

	got, err := loop.spillExecuteOutput(many.String())
	require.NoError(t, err)
	require.Contains(t, got, "...output truncated...")
	require.Contains(t, got, `read(filePath=".rocketcode/spill/turn-1/output-1.txt")`)
	require.Contains(t, got, "This file is deleted when this turn ends.")
	require.NotContains(t, got, "call the read tool")

	raw, err := root.ReadFile(".rocketcode/spill/turn-1/output-1.txt")
	require.NoError(t, err)
	require.Equal(t, many.String(), string(raw))

	action, matched := loop.Permissions.Evaluate("read", ".rocketcode/spill/turn-1/output-1.txt")
	require.True(t, matched)
	require.Equal(t, PermissionAllow, action)

	_, ok := loop.CodeModeHosts["read"]
	require.True(t, ok)

	other, matched := loop.Permissions.Evaluate("read", "secret.txt")
	require.False(t, matched)
	require.NotEqual(t, PermissionAllow, other)

	second, err := loop.spillExecuteOutput(many.String())
	require.NoError(t, err)
	require.Contains(t, second, "output-2.txt")

	wrapped := sfs.ReadResult(".rocketcode/spill/turn-1/output-1.txt", 1).Output
	require.True(t, len(wrapped) > executeHeadMaxBytes || strings.Count(wrapped, "\n") > executeHeadMaxLines)
	reread, err := loop.spillExecuteOutput(wrapped)
	require.NoError(t, err)
	require.Contains(t, reread, `read(filePath=".rocketcode/spill/turn-1/output-1.txt")`)
	require.NotContains(t, reread, "output-3.txt")
	require.Equal(t, []string{
		".rocketcode/spill/turn-1/output-1.txt",
		".rocketcode/spill/turn-1/output-2.txt",
	}, loop.spillPaths)

	_, err = root.Stat(".rocketcode/spill/turn-1/output-3.txt")
	require.ErrorIs(t, err, os.ErrNotExist)

	loop.endTurnSpills()

	_, err = root.Stat(".rocketcode/spill/turn-1/output-1.txt")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSpillExecuteOutputWriteFailure(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	loop := &looper{
		promptExpansion: promptExpansionEnvironment{root: root},
		spillRel:        "blocked/spill",
	}
	loop.beginTurnSpills("turn-1")
	require.NoError(t, root.WriteFile("blocked", []byte("not a dir"), 0o644))

	_, err = loop.spillExecuteOutput(strings.Repeat("line\n", 2100))
	require.Error(t, err)
	require.NotContains(t, err.Error(), strings.Repeat("line\n", 10))
}

func TestMakeSandboxedToolsHasRead(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	tmp := filepath.Join(dir, "tmp")
	require.NoError(t, os.Mkdir(tmp, 0o755))
	tools := newSandboxedTools(root, testShellTempConfig(t, root, tmp), nil, DefaultShellCommand)
	require.NotNil(t, tools["read"].Call)
}
