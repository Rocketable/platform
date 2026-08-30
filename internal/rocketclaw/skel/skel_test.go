package skel

import (
	"cmp"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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

	require.NoError(t, syncFSFiltered(src, payloadRoot, root, "test sync", testLogger(), true, false, nil))

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

	require.NoError(t, syncFSFiltered(src, payloadRoot, root, "test sync", testLogger(), true, false, nil))

	data, err := os.ReadFile(filepath.Join(root, "keep.txt"))
	require.NoError(t, err)
	assert.Equal(t, "keep", string(data))
}

func TestSyncResetsTargetPreservingState(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, targetRoot)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "trashdir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "auth.json"), []byte("token"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "auth.json.lock"), []byte("lock"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "overlays"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "overlays", "keep.txt"), []byte("stale"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "state.json"), []byte("legacy"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "trash.txt"), []byte("trash"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "trashdir", "old.txt"), []byte("old"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "agents", "main.md"), []byte("overlay"), 0o644))

	require.NoError(t, SyncInWithOverlays(tmp, targetRoot, nil, testLogger()))

	_, err := os.Stat(filepath.Join(root, "trash.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(root, "trashdir"))
	require.ErrorIs(t, err, os.ErrNotExist)

	_, err = os.Stat(filepath.Join(root, "state.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
	data, err := os.ReadFile(filepath.Join(root, "auth.json"))
	require.NoError(t, err)
	assert.Equal(t, "token", string(data))
	data, err = os.ReadFile(filepath.Join(root, "auth.json.lock"))
	require.NoError(t, err)
	assert.Equal(t, "lock", string(data))
	assert.DirExists(t, filepath.Join(root, "overlays"))
	_, err = os.Stat(filepath.Join(root, "overlays", "keep.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)

	data, err = os.ReadFile(filepath.Join(root, "agents", "main.md"))
	require.NoError(t, err)
	assert.Equal(t, "overlay", string(data))
}

func TestSyncRejectsRootSetupDirectory(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(tmp, "AGENTS.md"), 0o755))

	err := SyncInWithOverlays(tmp, targetRoot, nil, testLogger())
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

			err := SyncInWithOverlays(tmp, targetRoot, nil, testLogger())
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestResetTargetMissingIsNoop(t *testing.T) {
	require.NoError(t, resetRuntimeDirectory(filepath.Join(t.TempDir(), targetRoot), testLogger(), true))
}

func TestResetTargetRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), targetRoot)
	require.NoError(t, os.WriteFile(path, []byte("file"), 0o644))

	err := resetRuntimeDirectory(path, testLogger(), true)
	require.ErrorContains(t, err, "rocketclaw target path is not a directory")
}

func TestResetRuntimeDirectoryPreservesAuthTemporaryFiles(t *testing.T) {
	target := filepath.Join(t.TempDir(), targetRoot)
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, ".auth.json-in-flight"), []byte("token"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(target, "remove-me"), []byte("trash"), 0o600))

	require.NoError(t, resetRuntimeDirectory(target, testLogger(), false))

	data, err := os.ReadFile(filepath.Join(target, ".auth.json-in-flight"))
	require.NoError(t, err)
	assert.Equal(t, "token", string(data))

	_, err = os.Stat(filepath.Join(target, "remove-me"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSyncPreservesExistingWorkspaceSetupFiles(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "skills", "example"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("keep root"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "agents", "main.md"), []byte("keep overlay"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "skills", "example", "SKILL.md"), []byte("keep skill"), 0o644))

	require.NoError(t, SyncInWithOverlays(tmp, targetRoot, nil, testLogger()))

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

func TestSyncMaterializesLocalWorkflowOverlay(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("workflows", 0o755))
	require.NoError(t, root.WriteFile("workflows/audit.star", []byte("local workflow"), 0o644))

	require.NoError(t, SyncInWithOverlays(workspace, targetRoot, nil, testLogger()))

	data, err := root.ReadFile(targetRoot + "/workflows/audit.star")
	require.NoError(t, err)
	assert.Equal(t, "local workflow", string(data))
}

func TestSyncCreatesEmptyWorkspaceWorkflowsWithoutRuntimePlaceholder(t *testing.T) {
	workspace := t.TempDir()

	require.NoError(t, SyncInWithOverlays(workspace, targetRoot, nil, testLogger()))

	entries, err := os.ReadDir(filepath.Join(workspace, workflowsRoot))
	require.NoError(t, err)
	assert.Empty(t, entries)

	_, err = os.Stat(filepath.Join(workspace, targetRoot, workflowsRoot))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSyncFSFilteredPreservesExistingWhenOverwriteFalse(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, targetRoot)
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("old"), 0o644))

	src := fstest.MapFS{
		".rocketclaw/AGENTS.md": {Data: []byte("new")},
	}

	require.NoError(t, syncFSFiltered(src, payloadRoot, root, "test sync", testLogger(), false, false, nil))

	data, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "old", string(data))
}

func TestSyncFSFilteredPreservesSourceExecutableBit(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, targetRoot)
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "run"), []byte("old"), 0o644))

	src := fstest.MapFS{
		"scripts/run":      {Data: []byte("run"), Mode: 0o755},
		"scripts/plain.sh": {Data: []byte("plain"), Mode: 0o644},
	}

	require.NoError(t, syncFSFiltered(src, scriptsRoot, root, "test sync", testLogger(), true, true, nil))

	info, err := os.Stat(filepath.Join(root, "run"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	info, err = os.Stat(filepath.Join(root, "plain.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestSyncFSFilteredReportsMissingSourceRoot(t *testing.T) {
	err := syncFSFiltered(fstest.MapFS{}, payloadRoot, filepath.Join(t.TempDir(), targetRoot), "test sync", testLogger(), true, false, nil)
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

	err := syncFSFiltered(src, payloadRoot, root, "test sync", testLogger(), true, false, nil)
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

	err := syncFSFiltered(src, payloadRoot, root, "test sync", testLogger(), true, false, nil)
	require.ErrorContains(t, err, "create skeleton directory")
}

func TestOverlayMissingIsNoop(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, overlayWorkspaceIn(tmp, filepath.Join(tmp, targetRoot), testLogger()))

	_, err := os.Stat(filepath.Join(tmp, targetRoot))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestOverlayRejectsFile(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "agents"), []byte("file"), 0o644))

	err := overlayWorkspaceIn(tmp, filepath.Join(tmp, targetRoot), testLogger())
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

	require.NoError(t, overlayWorkspaceIn(tmp, filepath.Join(tmp, targetRoot), testLogger()))

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

	data, err = os.ReadFile(filepath.Join(tmp, targetRoot, "cron", "job.md"))
	require.NoError(t, err)
	assert.Equal(t, "cron", string(data))
}

func TestSyncInWithOverlaysAppliesGitBeforeLocalOverlay(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for overlay test")
	}

	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	repoGit(t, repo, "init")
	repoGit(t, repo, "config", "user.email", "test@example.com")
	repoGit(t, repo, "config", "user.name", "Test User")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "skills", "remote"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "cron"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "scripts"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "workflows"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "agents", "main.md"), []byte("remote agent"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "skills", "remote", "SKILL.md"), []byte("remote skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "cron", "daily.md"), []byte("remote cron"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "scripts", "tool.sh"), []byte("remote script"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "scripts", "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "scripts", "nested", "remote.sh"), []byte("nested remote script"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "scripts", "run"), []byte("remote executable"), 0o755))
	require.NoError(t, os.Chmod(filepath.Join(repo, "scripts", "run"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "scripts", "plain.sh"), []byte("remote non-executable"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "workflows", "audit.star"), []byte("remote workflow"), 0o644))
	repoGit(t, repo, "add", ".")
	repoGit(t, repo, "commit", "-m", "overlay")

	workspace := filepath.Join(tmp, "workspace")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "main.md"), []byte("local agent"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "scripts", "tool.sh"), []byte("local script"), 0o644))

	require.NoError(t, SyncInWithOverlays(workspace, targetRoot, []string{repo}, testLogger()))
	overlayInfo := OverlayInfos(workspace, targetRoot, []string{repo})[0]
	assert.DirExists(t, filepath.Join(overlayInfo.ClonePath, ".git"))
	assert.FileExists(t, filepath.Join(overlayInfo.ClonePath, "agents", "main.md"))

	data, err := os.ReadFile(filepath.Join(workspace, targetRoot, "agents", "main.md"))
	require.NoError(t, err)
	assert.Equal(t, "local agent", string(data))

	data, err = os.ReadFile(filepath.Join(workspace, targetRoot, "skills", "remote", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "remote skill", string(data))

	data, err = os.ReadFile(filepath.Join(workspace, targetRoot, "cron", "daily.md"))
	require.NoError(t, err)
	assert.Equal(t, "remote cron", string(data))

	data, err = os.ReadFile(filepath.Join(workspace, targetRoot, "scripts", "tool.sh"))
	require.NoError(t, err)
	assert.Equal(t, "local script", string(data))

	data, err = os.ReadFile(filepath.Join(workspace, targetRoot, "workflows", "audit.star"))
	require.NoError(t, err)
	assert.Equal(t, "remote workflow", string(data))

	data, err = os.ReadFile(filepath.Join(workspace, "scripts", "tool.sh"))
	require.NoError(t, err)
	assert.Equal(t, "local script", string(data))

	link := filepath.Join(workspace, "scripts", "nested", "remote.sh")
	target, err := os.Readlink(link)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("..", "..", targetRoot, "scripts", "nested", "remote.sh"), target)

	data, err = os.ReadFile(link)
	require.NoError(t, err)
	assert.Equal(t, "nested remote script", string(data))
	repoGit(t, repo, "rm", "scripts/nested/remote.sh")
	repoGit(t, repo, "commit", "-m", "remove remote script")

	info, err := os.Stat(filepath.Join(workspace, targetRoot, "scripts", "run"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	info, err = os.Stat(filepath.Join(workspace, targetRoot, "scripts", "plain.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())

	require.NoError(t, os.WriteFile(filepath.Join(overlayInfo.ClonePath, "untracked.txt"), []byte("discard me"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, targetRoot, "overlays", "stale-overlay"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, targetRoot, "overlays", "stale-overlay", "file.txt"), []byte("stale"), 0o644))

	require.NoError(t, SyncInWithOverlays(workspace, targetRoot, []string{repo}, testLogger()))

	_, err = os.Stat(filepath.Join(overlayInfo.ClonePath, "untracked.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(workspace, targetRoot, "overlays", "stale-overlay"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Lstat(link)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSyncInWithOverlaysAppliesConfiguredOverlaysInConfigOrder(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for overlay test")
	}

	tmp := t.TempDir()
	makeRepo := func(name, content string) string {
		repo := filepath.Join(tmp, name)
		repoGit(t, repo, "init")
		repoGit(t, repo, "config", "user.email", "test@example.com")
		repoGit(t, repo, "config", "user.name", "Test User")
		require.NoError(t, os.MkdirAll(filepath.Join(repo, "agents"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(repo, "agents", "shared.md"), []byte(content), 0o644))
		repoGit(t, repo, "add", ".")
		repoGit(t, repo, "commit", "-m", "overlay")

		return repo
	}

	repo1 := makeRepo("repo1", "first")
	repo2 := makeRepo("repo2", "second")
	workspace := filepath.Join(tmp, "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	require.NoError(t, SyncInWithOverlays(workspace, targetRoot, []string{repo1, repo2}, testLogger()))

	data, err := os.ReadFile(filepath.Join(workspace, targetRoot, "agents", "shared.md"))
	require.NoError(t, err)
	assert.Equal(t, "second", string(data))
}

func TestSyncInWithOverlaysTreatsGuardrailAsNormalAgent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for overlay test")
	}

	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	repoGit(t, repo, "init")
	repoGit(t, repo, "config", "user.email", "test@example.com")
	repoGit(t, repo, "config", "user.name", "Test User")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "agents", "guardrail.md"), []byte("remote guardrail"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "agents", "helper.md"), []byte("remote helper"), 0o644))
	repoGit(t, repo, "add", ".")
	repoGit(t, repo, "commit", "-m", "overlay")

	remoteOnly := filepath.Join(tmp, "remote-only")
	require.NoError(t, os.MkdirAll(remoteOnly, 0o755))
	require.NoError(t, SyncInWithOverlays(remoteOnly, targetRoot, []string{repo}, testLogger()))
	data, err := os.ReadFile(filepath.Join(remoteOnly, targetRoot, "agents", "guardrail.md"))
	require.NoError(t, err)
	assert.Equal(t, "remote guardrail", string(data))
	data, err = os.ReadFile(filepath.Join(remoteOnly, targetRoot, "agents", "helper.md"))
	require.NoError(t, err)
	assert.Equal(t, "remote helper", string(data))

	withLocal := filepath.Join(tmp, "with-local")
	require.NoError(t, os.MkdirAll(filepath.Join(withLocal, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(withLocal, "agents", "guardrail.md"), []byte("local guardrail"), 0o644))
	require.NoError(t, SyncInWithOverlays(withLocal, targetRoot, []string{repo}, testLogger()))
	data, err = os.ReadFile(filepath.Join(withLocal, targetRoot, "agents", "guardrail.md"))
	require.NoError(t, err)
	assert.Equal(t, "local guardrail", string(data))
}

func TestSyncWorkspaceScriptSymlinksReplacesRuntimeSymlinks(t *testing.T) {
	for _, runtimeRoot := range []string{".rocketclaw", ".femtoclaw"} {
		t.Run(runtimeRoot, func(t *testing.T) {
			workspace := t.TempDir()
			runtimeScripts := filepath.Join(workspace, targetRoot, "scripts")
			workspaceScripts := filepath.Join(workspace, "scripts")

			require.NoError(t, os.MkdirAll(runtimeScripts, 0o755))
			require.NoError(t, os.MkdirAll(workspaceScripts, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(runtimeScripts, "tool.sh"), []byte("runtime script"), 0o644))

			staleTarget := filepath.Join("..", runtimeRoot, "scripts", "tool.sh")
			require.NoError(t, os.Symlink(staleTarget, filepath.Join(workspaceScripts, "tool.sh")))

			require.NoError(t, syncWorkspaceScriptSymlinks(workspace, targetRoot, testLogger()))

			target, err := os.Readlink(filepath.Join(workspaceScripts, "tool.sh"))
			require.NoError(t, err)
			assert.Equal(t, filepath.Join("..", targetRoot, "scripts", "tool.sh"), target)

			data, err := os.ReadFile(filepath.Join(workspaceScripts, "tool.sh"))
			require.NoError(t, err)
			assert.Equal(t, "runtime script", string(data))
		})
	}
}

func TestSyncWorkspaceScriptSymlinksPreservesRegularFilesAndUnrelatedSymlinks(t *testing.T) {
	workspace := t.TempDir()
	runtimeScripts := filepath.Join(workspace, targetRoot, "scripts")
	workspaceScripts := filepath.Join(workspace, "scripts")

	require.NoError(t, os.MkdirAll(runtimeScripts, 0o755))
	require.NoError(t, os.MkdirAll(workspaceScripts, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runtimeScripts, "regular.sh"), []byte("runtime regular"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(runtimeScripts, "linked.sh"), []byte("runtime linked"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceScripts, "regular.sh"), []byte("workspace regular"), 0o644))
	require.NoError(t, os.Symlink("../external.sh", filepath.Join(workspaceScripts, "linked.sh")))

	require.NoError(t, syncWorkspaceScriptSymlinks(workspace, targetRoot, testLogger()))

	data, err := os.ReadFile(filepath.Join(workspaceScripts, "regular.sh"))
	require.NoError(t, err)
	assert.Equal(t, "workspace regular", string(data))

	target, err := os.Readlink(filepath.Join(workspaceScripts, "linked.sh"))
	require.NoError(t, err)
	assert.Equal(t, "../external.sh", target)
}

func TestSyncWorkspaceScriptSymlinksRejectsDirectoryConflict(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, targetRoot, "scripts"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "scripts", "tool.sh"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, targetRoot, "scripts", "tool.sh"), []byte("runtime script"), 0o644))

	err := syncWorkspaceScriptSymlinks(workspace, targetRoot, testLogger())
	require.ErrorContains(t, err, "workspace script path is a directory")
}

func TestSyncWorkspaceScriptSymlinksWithoutRuntimeScriptsDoesNotCreateWorkspaceScripts(t *testing.T) {
	workspace := t.TempDir()

	require.NoError(t, syncWorkspaceScriptSymlinks(workspace, targetRoot, testLogger()))

	_, err := os.Stat(filepath.Join(workspace, "scripts"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSyncWorkspaceScriptSymlinksUpdatesGitExclude(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".git", "info"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, targetRoot, "scripts", "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, targetRoot, "scripts", "tool.sh"), []byte("tool"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, targetRoot, "scripts", "nested", "remote.sh"), []byte("remote"), 0o644))

	require.NoError(t, syncWorkspaceScriptSymlinks(workspace, targetRoot, testLogger()))

	data, err := os.ReadFile(filepath.Join(workspace, ".git", "info", "exclude"))
	require.NoError(t, err)
	assert.Equal(t, "# BEGIN rocketclaw generated script symlinks\n/scripts/nested/remote.sh\n/scripts/tool.sh\n# END rocketclaw generated script symlinks\n", string(data))
}

func TestSyncWorkspaceScriptSymlinksReplacesGitExcludeBlock(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".git", "info"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, targetRoot, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, targetRoot, "scripts", "tool.sh"), []byte("tool"), 0o644))
	excludePath := filepath.Join(workspace, ".git", "info", "exclude")
	require.NoError(t, os.WriteFile(excludePath, []byte("keep\n# BEGIN rocketclaw generated script symlinks\n/scripts/old.sh\n# END rocketclaw generated script symlinks\nafter\n"), 0o644))

	require.NoError(t, syncWorkspaceScriptSymlinks(workspace, targetRoot, testLogger()))

	data, err := os.ReadFile(excludePath)
	require.NoError(t, err)
	assert.Equal(t, "keep\nafter\n# BEGIN rocketclaw generated script symlinks\n/scripts/tool.sh\n# END rocketclaw generated script symlinks\n", string(data))
}

func TestSyncWorkspaceScriptSymlinksRemovesStaleGitExcludeBlock(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".git", "info"), 0o755))
	excludePath := filepath.Join(workspace, ".git", "info", "exclude")
	require.NoError(t, os.WriteFile(excludePath, []byte("keep\n# BEGIN rocketclaw generated script symlinks\n/scripts/old.sh\n# END rocketclaw generated script symlinks\nafter\n"), 0o644))

	require.NoError(t, syncWorkspaceScriptSymlinks(workspace, targetRoot, testLogger()))

	data, err := os.ReadFile(excludePath)
	require.NoError(t, err)
	assert.Equal(t, "keep\nafter\n", string(data))
}

func TestSyncWorkspaceScriptSymlinksSkipsGitExcludeOutsideGitRepo(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, targetRoot, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, targetRoot, "scripts", "tool.sh"), []byte("tool"), 0o644))

	require.NoError(t, syncWorkspaceScriptSymlinks(workspace, targetRoot, testLogger()))

	_, err := os.Stat(filepath.Join(workspace, ".git"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSyncWorkspaceScriptSymlinksDoesNotExcludeRegularWorkspaceScripts(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".git", "info"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, targetRoot, "scripts"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, targetRoot, "scripts", "tool.sh"), []byte("runtime"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "scripts", "tool.sh"), []byte("workspace"), 0o644))
	excludePath := filepath.Join(workspace, ".git", "info", "exclude")
	require.NoError(t, os.WriteFile(excludePath, []byte("keep\n"), 0o644))

	require.NoError(t, syncWorkspaceScriptSymlinks(workspace, targetRoot, testLogger()))

	data, err := os.ReadFile(excludePath)
	require.NoError(t, err)
	assert.Equal(t, "keep\n", string(data))
}

func TestParseGitOverlaySpec(t *testing.T) {
	for _, tt := range []struct {
		spec, wantURL, wantRef string
	}{
		{spec: "github.com/rocketable/overlay", wantURL: "https://github.com/rocketable/overlay"},
		{spec: "github.com/rocketable/overlay@main", wantURL: "https://github.com/rocketable/overlay", wantRef: "main"},
		{spec: "git@github.com:rocketable/overlay.git", wantURL: "git@github.com:rocketable/overlay.git"},
		{spec: "git@github.com:Rocketable/alitu-cs.git@main", wantURL: "git@github.com:Rocketable/alitu-cs.git", wantRef: "main"},
		{spec: "https://github.com/Rocketable/alitu-cs.git@main", wantURL: "https://github.com/Rocketable/alitu-cs.git", wantRef: "main"},
		{spec: "ssh://git@github.com/rocketable/overlay.git", wantURL: "ssh://git@github.com/rocketable/overlay.git"},
		{spec: "ssh://git@github.com/Rocketable/alitu-cs.git@main", wantURL: "ssh://git@github.com/Rocketable/alitu-cs.git", wantRef: "main"},
	} {
		t.Run(tt.spec, func(t *testing.T) {
			gotURL, gotRef := parseGitOverlaySpec(tt.spec)
			assert.Equal(t, tt.wantURL, gotURL)
			assert.Equal(t, tt.wantRef, gotRef)
		})
	}
}

func TestOverlayInfos(t *testing.T) {
	workspace := string(filepath.Separator) + "workspace"
	infos := OverlayInfos(workspace, targetRoot, []string{" github.com/rocketable/overlay@main ", ""})

	require.Len(t, infos, 1)
	assert.Equal(t, "github.com/rocketable/overlay@main", infos[0].Spec)
	assert.Equal(t, "https://github.com/rocketable/overlay", infos[0].URL)
	assert.Equal(t, "main", infos[0].Ref)
	assert.Equal(t, filepath.Join(workspace, targetRoot, "overlays", "github.com-rocketable-overlay-main"), infos[0].ClonePath)
}

func TestOverlaySpecs(t *testing.T) {
	assert.Empty(t, OverlaySpecs(nil))

	first := "github.com/rocketable/overlay@main"
	second := "github.com/rocketable/other"
	assert.Equal(t, []string{first, second}, OverlaySpecs([]string{" " + first + " ", "", second}))
}

func TestReadOverlayContextKnownSpec(t *testing.T) {
	workspace := t.TempDir()
	spec := "github.com/rocketable/overlay@main"
	overlays := []string{spec}
	clone := OverlayInfos(workspace, targetRoot, overlays)[0].ClonePath
	require.NoError(t, os.MkdirAll(filepath.Join(clone, agentsRoot), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(clone, skillsRoot, "pack"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(clone, agentsRoot, "a.md"), []byte("agent"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(clone, skillsRoot, "pack", "SKILL.md"), []byte("skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(clone, "README.md"), []byte("ignore"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, agentsRoot, "local.md"), []byte("workspace"), 0o644))

	got, err := ReadOverlayContext(workspace, targetRoot, overlays, spec)
	require.NoError(t, err)
	assert.Equal(t, spec, got.BaseOverlay)
	assert.Equal(t, []OverlayFile{
		{Path: "agents/a.md", Content: "agent"},
		{Path: "skills/pack/SKILL.md", Content: "skill"},
	}, got.Files)
}

func TestReadOverlayContextUnknownSpec(t *testing.T) {
	workspace := t.TempDir()
	overlays := []string{"github.com/rocketable/overlay"}

	_, err := ReadOverlayContext(workspace, targetRoot, overlays, "github.com/other/overlay")
	require.ErrorContains(t, err, `unknown overlay spec "github.com/other/overlay"`)

	_, err = ReadOverlayContext(workspace, targetRoot, overlays, "")
	require.ErrorContains(t, err, `unknown overlay spec ""`)
}

func TestReadOverlayContextWalkError(t *testing.T) {
	workspace := t.TempDir()
	spec := "github.com/rocketable/overlay@main"
	overlays := []string{spec}
	clone := OverlayInfos(workspace, targetRoot, overlays)[0].ClonePath
	require.NoError(t, os.MkdirAll(filepath.Join(clone, scriptsRoot), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(clone, "missing"), filepath.Join(clone, scriptsRoot, "aa-link")))
	require.NoError(t, os.WriteFile(filepath.Join(clone, scriptsRoot, "zz.sh"), []byte("keep"), 0o644))

	_, err := ReadOverlayContext(workspace, targetRoot, overlays, spec)
	require.ErrorContains(t, err, "walk overlay root")
}

func TestReadOverlayContextMissingClone(t *testing.T) {
	workspace := t.TempDir()
	spec := "github.com/rocketable/overlay@main"

	got, err := ReadOverlayContext(workspace, targetRoot, []string{spec}, spec)
	require.NoError(t, err)
	assert.Equal(t, spec, got.BaseOverlay)
	assert.Empty(t, got.Files)
}

func TestStageLiveRuntimeCopiesLiveMergeWithoutTouchingLiveTree(t *testing.T) {
	workspace := t.TempDir()
	spec := "github.com/rocketable/overlay@main"
	overlays := []string{spec}
	clone := OverlayInfos(workspace, targetRoot, overlays)[0].ClonePath
	liveRuntime := filepath.Join(workspace, targetRoot)
	liveFile := filepath.Join(liveRuntime, agentsRoot, "live-only.md")
	cloneFile := filepath.Join(clone, agentsRoot, "from-overlay.md")
	cloneShared := filepath.Join(clone, agentsRoot, "shared.md")

	require.NoError(t, os.MkdirAll(filepath.Join(liveRuntime, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(liveFile, []byte("live"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(clone, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(cloneFile, []byte("overlay"), 0o644))
	require.NoError(t, os.WriteFile(cloneShared, []byte("from-overlay"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, agentsRoot, "from-workspace.md"), []byte("workspace"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, agentsRoot, "shared.md"), []byte("from-workspace"), 0o644))

	before := snapshotLiveTree(t, liveRuntime)

	for _, tt := range []struct {
		name       string
		base       string
		files      []OverlayFile
		checkMerge bool
	}{
		{name: "empty base", checkMerge: true},
		{name: "named base", base: spec, files: []OverlayFile{{Path: "agents/from-overlay.md", Content: "tried"}}},
		{name: "extra top layer", files: []OverlayFile{{Path: "agents/try-only.md", Content: "try"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stage, err := StageLiveRuntime(workspace, targetRoot, overlays, tt.base, tt.files, testLogger())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(stage)) })

			if tt.checkMerge {
				assert.Equal(t, workspace, filepath.Dir(stage))
				assert.True(t, strings.HasPrefix(filepath.Base(stage), ".rocketclaw-devmcp-"))

				data, err := os.ReadFile(filepath.Join(stage, ".gitignore"))
				require.NoError(t, err)
				assert.Equal(t, "auth.json\nauth.json.lock\n", string(data))

				data, err = os.ReadFile(filepath.Join(stage, agentsRoot, "main.md"))
				require.NoError(t, err)
				assert.Contains(t, string(data), "main-archive-benchmarks")

				data, err = os.ReadFile(filepath.Join(stage, agentsRoot, "from-overlay.md"))
				require.NoError(t, err)
				assert.Equal(t, "overlay", string(data))

				data, err = os.ReadFile(filepath.Join(stage, agentsRoot, "from-workspace.md"))
				require.NoError(t, err)
				assert.Equal(t, "workspace", string(data))

				data, err = os.ReadFile(filepath.Join(stage, agentsRoot, "shared.md"))
				require.NoError(t, err)
				assert.Equal(t, "from-workspace", string(data))

				_, err = os.Stat(filepath.Join(stage, agentsRoot, "live-only.md"))
				require.ErrorIs(t, err, os.ErrNotExist)
			}

			assert.Equal(t, before, snapshotLiveTree(t, liveRuntime))
		})
	}
}

func TestStageLiveRuntimeReplacesNamedLayerKeepsOmittedCloneFile(t *testing.T) {
	workspace := t.TempDir()
	spec := "github.com/rocketable/skills"
	overlays := []string{spec}
	clone := OverlayInfos(workspace, targetRoot, overlays)[0].ClonePath
	require.NoError(t, os.MkdirAll(filepath.Join(clone, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(clone, agentsRoot, "a.md"), []byte("old-a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(clone, agentsRoot, "b.md"), []byte("old-b"), 0o644))

	stage, err := StageLiveRuntime(workspace, targetRoot, overlays, spec, []OverlayFile{{Path: "agents/a.md", Content: "new-a"}}, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(stage)) })

	data, err := os.ReadFile(filepath.Join(stage, agentsRoot, "a.md"))
	require.NoError(t, err)
	assert.Equal(t, "new-a", string(data))

	data, err = os.ReadFile(filepath.Join(stage, agentsRoot, "b.md"))
	require.NoError(t, err)
	assert.Equal(t, "old-b", string(data))

	data, err = os.ReadFile(filepath.Join(clone, agentsRoot, "a.md"))
	require.NoError(t, err)
	assert.Equal(t, "old-a", string(data))
}

func TestStageLiveRuntimeReplacingFirstLayerStillAppliesLaterOverlay(t *testing.T) {
	workspace := t.TempDir()
	first := "github.com/rocketable/first"
	second := "github.com/rocketable/second"
	overlays := []string{first, second}
	infos := OverlayInfos(workspace, targetRoot, overlays)
	require.NoError(t, os.MkdirAll(filepath.Join(infos[0].ClonePath, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(infos[0].ClonePath, agentsRoot, "a.md"), []byte("first-a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(infos[0].ClonePath, agentsRoot, "b.md"), []byte("first-b"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(infos[1].ClonePath, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(infos[1].ClonePath, agentsRoot, "a.md"), []byte("second-a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(infos[1].ClonePath, agentsRoot, "c.md"), []byte("second-c"), 0o644))

	stage, err := StageLiveRuntime(workspace, targetRoot, overlays, first, []OverlayFile{{Path: "agents/a.md", Content: "new-a"}}, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(stage)) })

	data, err := os.ReadFile(filepath.Join(stage, agentsRoot, "a.md"))
	require.NoError(t, err)
	assert.Equal(t, "second-a", string(data))

	data, err = os.ReadFile(filepath.Join(stage, agentsRoot, "b.md"))
	require.NoError(t, err)
	assert.Equal(t, "first-b", string(data))

	data, err = os.ReadFile(filepath.Join(stage, agentsRoot, "c.md"))
	require.NoError(t, err)
	assert.Equal(t, "second-c", string(data))
}

func TestStageLiveRuntimeUnsetBaseAddsExtraLayerWithoutCloneFiles(t *testing.T) {
	workspace := t.TempDir()
	spec := "github.com/rocketable/overlay"
	overlays := []string{spec}
	clone := OverlayInfos(workspace, targetRoot, overlays)[0].ClonePath
	require.NoError(t, os.MkdirAll(filepath.Join(clone, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(clone, agentsRoot, "b.md"), []byte("live-b"), 0o644))

	stage, err := StageLiveRuntime(workspace, targetRoot, overlays, "", []OverlayFile{{Path: "agents/a.md", Content: "new-a"}}, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(stage)) })

	data, err := os.ReadFile(filepath.Join(stage, agentsRoot, "a.md"))
	require.NoError(t, err)
	assert.Equal(t, "new-a", string(data))

	data, err = os.ReadFile(filepath.Join(stage, agentsRoot, "b.md"))
	require.NoError(t, err)
	assert.Equal(t, "live-b", string(data))

	data, err = os.ReadFile(filepath.Join(clone, agentsRoot, "b.md"))
	require.NoError(t, err)
	assert.Equal(t, "live-b", string(data))

	_, err = os.Stat(filepath.Join(clone, agentsRoot, "a.md"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestStageLiveRuntimeUnsetBaseExtraLayerWinsOverWorkspace(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, agentsRoot, "a.md"), []byte("workspace-a"), 0o644))

	stage, err := StageLiveRuntime(workspace, targetRoot, nil, " \t", []OverlayFile{{Path: "agents/a.md", Content: "new-a"}}, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(stage)) })

	data, err := os.ReadFile(filepath.Join(stage, agentsRoot, "a.md"))
	require.NoError(t, err)
	assert.Equal(t, "new-a", string(data))
}

func TestStageLiveRuntimeUnknownBaseOverlay(t *testing.T) {
	workspace := t.TempDir()
	spec := "github.com/rocketable/overlay"
	overlays := []string{spec}
	clone := OverlayInfos(workspace, targetRoot, overlays)[0].ClonePath
	liveRuntime := filepath.Join(workspace, targetRoot)
	require.NoError(t, os.MkdirAll(filepath.Join(clone, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(clone, agentsRoot, "a.md"), []byte("overlay"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(liveRuntime, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(liveRuntime, agentsRoot, "live.md"), []byte("live"), 0o644))

	before := snapshotLiveTree(t, liveRuntime)

	_, err := StageLiveRuntime(workspace, targetRoot, overlays, "github.com/other/overlay", []OverlayFile{{Path: "agents/a.md", Content: "tried"}}, testLogger())
	require.ErrorContains(t, err, `unknown overlay spec "github.com/other/overlay"`)

	assert.Equal(t, before, snapshotLiveTree(t, liveRuntime))
}

func TestStageLiveRuntimeRejectsEscapingRequestPath(t *testing.T) {
	workspace := t.TempDir()
	spec := "github.com/rocketable/skills"
	overlays := []string{spec}
	clone := OverlayInfos(workspace, targetRoot, overlays)[0].ClonePath
	require.NoError(t, os.MkdirAll(filepath.Join(clone, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(clone, agentsRoot, "a.md"), []byte("old-a"), 0o644))

	_, err := StageLiveRuntime(workspace, targetRoot, overlays, spec, []OverlayFile{{Path: "../agents/a.md", Content: "escaped"}}, testLogger())
	require.ErrorContains(t, err, "escapes stage")
}

func repoGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)

	cmd.Dir = dir
	if len(args) > 0 && args[0] == "init" {
		cmd = exec.Command("git", append(args, dir)...)
	}

	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
}

func TestSyncCreatesEmbeddedRuntimeSupport(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, SyncInWithOverlays(tmp, targetRoot, nil, testLogger()))

	info, err := os.Stat(filepath.Join(tmp, "skills"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	for _, name := range []string{
		filepath.Join(targetRoot, ".gitignore"),
	} {
		data, err := os.ReadFile(filepath.Join(tmp, name))
		require.NoError(t, err)
		assert.NotEmpty(t, data)
	}

	data, err := os.ReadFile(filepath.Join(tmp, targetRoot, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "auth.json\nauth.json.lock\n", string(data))
}

func TestListSetupFilesIncludesRuntimeSupport(t *testing.T) {
	names, err := ListSetupFiles()
	require.NoError(t, err)

	assert.True(t, slices.IsSorted(names), "ListSetupFiles() = %v; want sorted names", names)

	for _, name := range []string{
		".rocketclaw/.gitignore",
		".rocketclaw/skills/main-archive-benchmarks/SKILL.md",
		"agents/main.md",
		"agents/slack-to-benchmark.md",
	} {
		assert.Contains(t, names, name)
	}

	assert.NotContains(t, names, ".rocketclaw")
	assert.NotContains(t, names, "agents")
	assert.NotContains(t, names, "cron")
	assert.NotContains(t, names, "workflows")
	assert.NotContains(t, names, "workflows/.gitkeep")
}

func TestSyncShipsQuickbenchSkillAndAgent(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, SyncInWithOverlays(tmp, targetRoot, nil, testLogger()))

	skill, err := os.ReadFile(filepath.Join(tmp, targetRoot, "skills", "main-archive-benchmarks", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(skill), "go run github.com/Rocketable/platform/cmd/quickbench@main")

	agent, err := os.ReadFile(filepath.Join(tmp, targetRoot, "agents", "slack-to-benchmark.md"))
	require.NoError(t, err)
	assert.Contains(t, string(agent), "go run github.com/Rocketable/platform/cmd/quickbench@main")

	mainAgent, err := os.ReadFile(filepath.Join(tmp, targetRoot, "agents", "main.md"))
	require.NoError(t, err)
	assert.Contains(t, string(mainAgent), "main-archive-benchmarks")
	assert.Contains(t, string(mainAgent), "slack-to-benchmark")
}

func TestReadSetupFile(t *testing.T) {
	data, err := ReadSetupFile("main-update-cortex.sh")
	require.NoError(t, err)
	assert.Contains(t, string(data), "#!/usr/bin/env bash")
}

func TestReadSetupFileRejectsUnknown(t *testing.T) {
	_, err := ReadSetupFile("../main-update-cortex.sh")
	require.ErrorIs(t, err, errUnknownSetupFile)
}

func TestSyncEffectiveRuntimeAssetsDoesNotMutateRuntimeOrScriptSymlinks(t *testing.T) {
	workspace := t.TempDir()
	realRuntime := filepath.Join(workspace, targetRoot)
	realScripts := filepath.Join(workspace, scriptsRoot)
	target := filepath.Join(t.TempDir(), targetRoot)
	require.NoError(t, os.MkdirAll(filepath.Join(realRuntime, agentsRoot), 0o755))
	require.NoError(t, os.MkdirAll(realScripts, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(realRuntime, agentsRoot, "main.md"), []byte("real runtime"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, agentsRoot, "main.md"), []byte("overlay"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, scriptsRoot, "helper.sh"), []byte("script"), 0o644))

	require.NoError(t, SyncEffectiveRuntimeAssets(workspace, target, nil, testLogger()))

	data, err := os.ReadFile(filepath.Join(realRuntime, agentsRoot, "main.md"))
	require.NoError(t, err)
	assert.Equal(t, "real runtime", string(data))
	data, err = os.ReadFile(filepath.Join(target, agentsRoot, "main.md"))
	require.NoError(t, err)
	assert.Equal(t, "overlay", string(data))

	info, err := os.Lstat(filepath.Join(realScripts, "helper.sh"))
	require.NoError(t, err)
	assert.Zero(t, info.Mode()&os.ModeSymlink)
}

func TestReplaceRuntimeAssetsAfterValidationLeavesLiveAssetsUnchangedOnValidationFailure(t *testing.T) {
	workspace := t.TempDir()
	liveAgents := filepath.Join(workspace, targetRoot, agentsRoot)
	realScripts := filepath.Join(workspace, scriptsRoot)

	require.NoError(t, os.MkdirAll(liveAgents, 0o755))
	require.NoError(t, os.MkdirAll(realScripts, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(liveAgents, "main.md"), []byte("live"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(realScripts, "tool.sh"), []byte("regular"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, agentsRoot, "main.md"), []byte("broken"), 0o644))

	errValidate := errors.New("invalid staged assets")
	err := ReplaceRuntimeAssetsAfterValidation(workspace, targetRoot, nil, testLogger(), func(string) error { return errValidate })
	require.ErrorIs(t, err, errValidate)

	data, err := os.ReadFile(filepath.Join(liveAgents, "main.md"))
	require.NoError(t, err)
	assert.Equal(t, "live", string(data))
	data, err = os.ReadFile(filepath.Join(realScripts, "tool.sh"))
	require.NoError(t, err)
	assert.Equal(t, "regular", string(data))
}

func TestReplaceRuntimeAssetsAfterValidationCommitsFreshOverlayAndRefreshesScripts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for overlay test")
	}

	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	repoGit(t, repo, "init")
	repoGit(t, repo, "config", "user.email", "test@example.com")
	repoGit(t, repo, "config", "user.name", "Test User")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, agentsRoot), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, scriptsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, agentsRoot, "remote.md"), []byte("old remote"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, scriptsRoot, "remote.sh"), []byte("old script"), 0o644))
	repoGit(t, repo, "add", ".")
	repoGit(t, repo, "commit", "-m", "old")

	workspace := filepath.Join(tmp, "workspace")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, agentsRoot, "local.md"), []byte("local"), 0o644))
	require.NoError(t, SyncInWithOverlays(workspace, targetRoot, []string{repo}, testLogger()))

	require.NoError(t, os.WriteFile(filepath.Join(repo, agentsRoot, "remote.md"), []byte("new remote"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, scriptsRoot, "remote.sh"), []byte("new script"), 0o644))
	repoGit(t, repo, "add", ".")
	repoGit(t, repo, "commit", "-m", "new")

	require.NoError(t, ReplaceRuntimeAssetsAfterValidation(workspace, targetRoot, []string{repo}, testLogger(), func(stagedWorkDir string) error {
		data, err := os.ReadFile(filepath.Join(workspace, stagedWorkDir, agentsRoot, "remote.md"))
		require.NoError(t, err)
		assert.Equal(t, "new remote", string(data))

		return nil
	}))

	data, err := os.ReadFile(filepath.Join(workspace, targetRoot, agentsRoot, "remote.md"))
	require.NoError(t, err)
	assert.Equal(t, "new remote", string(data))
	data, err = os.ReadFile(filepath.Join(workspace, targetRoot, agentsRoot, "local.md"))
	require.NoError(t, err)
	assert.Equal(t, "local", string(data))
	data, err = os.ReadFile(filepath.Join(workspace, scriptsRoot, "remote.sh"))
	require.NoError(t, err)
	assert.Equal(t, "new script", string(data))
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

type liveTreeFile struct {
	path    string
	mode    os.FileMode
	modTime int64
	content string
}

func snapshotLiveTree(t *testing.T, root string) []liveTreeFile {
	t.Helper()

	var files []liveTreeFile

	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		require.NoError(t, err)

		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		require.NoError(t, err)

		info, err := d.Info()
		require.NoError(t, err)

		data, err := os.ReadFile(path)
		require.NoError(t, err)

		files = append(files, liveTreeFile{path: rel, mode: info.Mode(), modTime: info.ModTime().UnixNano(), content: string(data)})

		return nil
	}))

	slices.SortFunc(files, func(a, b liveTreeFile) int { return cmp.Compare(a.path, b.path) })

	return files
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
