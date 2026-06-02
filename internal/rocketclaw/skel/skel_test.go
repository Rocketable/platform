package skel

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncFSOverwritesEmbeddedFiles(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, targetRoot)
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("old"), 0o644))

	src := fstest.MapFS{
		".rocketclaw/AGENTS.md":       {Data: []byte("new")},
		".rocketclaw/nested/tool.txt": {Data: []byte("nested")},
	}

	require.NoError(t, syncFSFiltered(src, payloadRoot, root, "test sync", testLogger(), true, nil))

	data, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "new", string(data))

	data, err = os.ReadFile(filepath.Join(root, "nested", "tool.txt"))
	require.NoError(t, err)
	assert.Equal(t, "nested", string(data))
}

func TestSyncFSPreservesExtraFiles(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, targetRoot)
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.txt"), []byte("keep"), 0o644))

	src := fstest.MapFS{
		".rocketclaw/AGENTS.md": {Data: []byte("seed")},
	}

	require.NoError(t, syncFSFiltered(src, payloadRoot, root, "test sync", testLogger(), true, nil))

	data, err := os.ReadFile(filepath.Join(root, "keep.txt"))
	require.NoError(t, err)
	assert.Equal(t, "keep", string(data))
}

func TestSyncResetsTargetPreservingState(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, targetRoot)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "trashdir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "auth.json"), []byte("token"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "state.sqlite3"), []byte("state"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "web-ui.crt"), []byte("cert"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "web-ui.key"), []byte("key"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "state.json"), []byte("legacy"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "trash.txt"), []byte("trash"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "trashdir", "old.txt"), []byte("old"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "agents", "main.md"), []byte("overlay"), 0o644))

	require.NoError(t, Sync(tmp, testLogger()))

	_, err := os.Stat(filepath.Join(root, "trash.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(root, "trashdir"))
	require.ErrorIs(t, err, os.ErrNotExist)

	data, err := os.ReadFile(filepath.Join(root, "state.sqlite3"))
	require.NoError(t, err)
	assert.Equal(t, "state", string(data))

	_, err = os.Stat(filepath.Join(root, "state.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
	data, err = os.ReadFile(filepath.Join(root, "auth.json"))
	require.NoError(t, err)
	assert.Equal(t, "token", string(data))
	data, err = os.ReadFile(filepath.Join(root, "web-ui.crt"))
	require.NoError(t, err)
	assert.Equal(t, "cert", string(data))
	data, err = os.ReadFile(filepath.Join(root, "web-ui.key"))
	require.NoError(t, err)
	assert.Equal(t, "key", string(data))

	data, err = os.ReadFile(filepath.Join(root, "agents", "main.md"))
	require.NoError(t, err)
	assert.Equal(t, "overlay", string(data))
}

func TestSyncRejectsRootSetupDirectory(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(tmp, "AGENTS.md"), 0o755))

	err := Sync(tmp, testLogger())
	require.ErrorContains(t, err, "root setup path is a directory")
}

func TestSyncReportsWorkspaceDirectoryConflicts(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "agents file", path: agentsRoot, want: "unpack embedded rocketclaw overlay agents"},
		{name: "skills file", path: skillsRoot, want: "create rocketclaw skills overlay directory"},
		{name: "target file", path: targetRoot, want: "reset rocketclaw target"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			path := filepath.Join(tmp, tt.path)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte("file"), 0o644))

			err := Sync(tmp, testLogger())
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestResetTargetMissingIsNoop(t *testing.T) {
	require.NoError(t, resetTarget(filepath.Join(t.TempDir(), targetRoot), testLogger()))
}

func TestResetTargetRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), targetRoot)
	require.NoError(t, os.WriteFile(path, []byte("file"), 0o644))

	err := resetTarget(path, testLogger())
	require.ErrorContains(t, err, "rocketclaw target path is not a directory")
}

func TestSyncPreservesExistingWorkspaceSetupFiles(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "skills", "example"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("keep root"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "agents", "main.md"), []byte("keep overlay"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "skills", "example", "SKILL.md"), []byte("keep skill"), 0o644))

	require.NoError(t, Sync(tmp, testLogger()))

	data, err := os.ReadFile(filepath.Join(tmp, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "keep root", string(data))

	data, err = os.ReadFile(filepath.Join(tmp, "agents", "main.md"))
	require.NoError(t, err)
	assert.Equal(t, "keep overlay", string(data))

	data, err = os.ReadFile(filepath.Join(tmp, targetRoot, "agents", "main.md"))
	require.NoError(t, err)
	assert.Equal(t, "keep overlay", string(data))

	data, err = os.ReadFile(filepath.Join(tmp, targetRoot, "skills", "example", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "keep skill", string(data))
}

func TestSyncFSFilteredPreservesExistingWhenOverwriteFalse(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, targetRoot)
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("old"), 0o644))

	src := fstest.MapFS{
		".rocketclaw/AGENTS.md": {Data: []byte("new")},
	}

	require.NoError(t, syncFSFiltered(src, payloadRoot, root, "test sync", testLogger(), false, nil))

	data, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "old", string(data))
}

func TestSyncFSFilteredReportsMissingSourceRoot(t *testing.T) {
	err := syncFSFiltered(fstest.MapFS{}, payloadRoot, filepath.Join(t.TempDir(), targetRoot), "test sync", testLogger(), true, nil)
	require.ErrorContains(t, err, "copy skeleton source")
	require.ErrorContains(t, err, "walk skeleton source")
}

func TestSyncFSFilteredReportsTargetDirectoryForFile(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, targetRoot)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "AGENTS.md"), 0o755))

	src := fstest.MapFS{
		".rocketclaw/AGENTS.md": {Data: []byte("seed")},
	}

	err := syncFSFiltered(src, payloadRoot, root, "test sync", testLogger(), true, nil)
	require.ErrorContains(t, err, "write skeleton file")
}

func TestSyncFSFilteredReportsTargetFileForDirectory(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, targetRoot)
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "nested"), []byte("file"), 0o644))

	src := fstest.MapFS{
		".rocketclaw/nested/file.txt": {Data: []byte("seed")},
	}

	err := syncFSFiltered(src, payloadRoot, root, "test sync", testLogger(), true, nil)
	require.ErrorContains(t, err, "create skeleton directory")
}

func TestOverlayMissingIsNoop(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, overlay(tmp, testLogger()))

	_, err := os.Stat(filepath.Join(tmp, targetRoot))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestOverlayRejectsFile(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "agents"), []byte("file"), 0o644))

	err := overlay(tmp, testLogger())
	require.ErrorContains(t, err, "rocketclaw overlay path is not a directory")
}

func TestOverlaySkipsFilteredFiles(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "agents", "nested"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "cron"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "agents", "nested", "guide.example.md"), []byte("example"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "agents", "nested", "GUIDE.EXAMPLE.MD"), []byte("upper example"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "agents", "nested", "example.md"), []byte("plain example"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "agents", "nested", "guide.md"), []byte("real"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "cron", "job.md"), []byte("cron"), 0o644))

	require.NoError(t, overlay(tmp, testLogger()))

	for _, path := range []string{
		"guide.example.md",
		"GUIDE.EXAMPLE.MD",
		"example.md",
	} {
		_, err := os.Stat(filepath.Join(tmp, targetRoot, "agents", "nested", path))
		require.ErrorIs(t, err, os.ErrNotExist)
	}

	data, err := os.ReadFile(filepath.Join(tmp, targetRoot, "agents", "nested", "guide.md"))
	require.NoError(t, err)
	assert.Equal(t, "real", string(data))

	_, err = os.Stat(filepath.Join(tmp, targetRoot, "cron", "job.md"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSyncWritesEmbeddedSetupFiles(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, Sync(tmp, testLogger()))

	info, err := os.Stat(filepath.Join(tmp, "skills"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	for _, name := range []string{
		"AGENTS.md",
		filepath.Join("agents", "main.md"),
		filepath.Join("cron", "HEARTBEAT.example.md"),
		filepath.Join(targetRoot, "skills", "main-create-or-update-agent", "SKILL.md"),
	} {
		data, err := os.ReadFile(filepath.Join(tmp, name))
		require.NoError(t, err)
		assert.NotEmpty(t, data)
	}
}

func TestListSetupFiles(t *testing.T) {
	names, err := ListSetupFiles()
	require.NoError(t, err)

	assert.True(t, slices.IsSorted(names), "ListSetupFiles() = %v; want sorted names", names)

	for _, name := range []string{
		"AGENTS.md",
		"agents/main.md",
		"cron/HEARTBEAT.example.md",
	} {
		assert.Contains(t, names, name)
	}

	assert.NotContains(t, names, ".rocketclaw")
	assert.NotContains(t, names, "agents")
	assert.NotContains(t, names, "cron")
}

func TestReadSetupFile(t *testing.T) {
	data, err := ReadSetupFile("AGENTS.md")
	require.NoError(t, err)
	assert.NotEmpty(t, data)
}

func TestReadSetupFileRejectsUnknown(t *testing.T) {
	_, err := ReadSetupFile("../AGENTS.md")
	require.ErrorIs(t, err, errUnknownSetupFile)
}

func TestRelativePath(t *testing.T) {
	tests := []struct {
		name string
		root string
		path string
		want string
	}{
		{name: "root dot", root: ".", path: "AGENTS.md", want: "AGENTS.md"},
		{name: "same path", root: ".rocketclaw", path: ".rocketclaw", want: "."},
		{name: "nested path", root: ".rocketclaw", path: ".rocketclaw/agents/main.md", want: "agents/main.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, relativePath(tt.root, tt.path))
		})
	}
}

func TestResolveSetupFilePath(t *testing.T) {
	tests := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{name: "AGENTS.md", want: "AGENTS.md"},
		{name: "../AGENTS.md", wantErr: true},
		{name: ".", wantErr: true},
		{name: "agents", wantErr: true},
		{name: "missing.md", wantErr: true},
	}

	for _, tt := range tests {
		got, err := resolveSetupFilePath(tt.name)
		if tt.wantErr {
			require.Error(t, err, tt.name)
			continue
		}

		require.NoError(t, err, tt.name)
		assert.Equal(t, tt.want, got)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
