package skel

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
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

	require.NoError(t, syncFSFiltered(src, payloadRoot, root, "test sync", testLogger(), true, false, inertFileFilter{}))

	data, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "new", string(data))

	data, err = os.ReadFile(filepath.Join(root, "nested", "tool.txt"))
	require.NoError(t, err)
	assert.Equal(t, "nested", string(data))
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
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "agents", "bot.md"), []byte("overlay"), 0o644))

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

	data, err = os.ReadFile(filepath.Join(root, "agents", "bot.md"))
	require.NoError(t, err)
	assert.Equal(t, "overlay", string(data))
}

func TestSyncReportsWorkspaceDirectoryConflicts(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		symlink string
		dirMode os.FileMode
		want    string
	}{
		{name: "agents file", path: agentsRoot, want: "unpack embedded rocketclaw overlay agents"},
		{name: "skills file", path: skillsRoot, want: "create rocketclaw skills overlay directory"},
		{name: "target file", path: targetRoot, want: "reset rocketclaw target"},
		{name: "scripts file", path: scriptsRoot, want: "apply rocketclaw overlay: rocketclaw overlay path is not a directory"},
		{name: "overlay clones file", path: targetRoot + "/" + overlaysRoot, want: "reconcile configured rocketclaw overlays: configured overlay clone path is not a directory"},
		{name: "scripts symlink loop", path: scriptsRoot, symlink: scriptsRoot, want: "remove stale workspace script symlinks: stat workspace scripts directory"},
		{name: "scripts unreadable", path: scriptsRoot, dirMode: os.ModeDir, want: "remove stale workspace script symlinks: walk workspace scripts: walk workspace scripts"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			placeConflict(t, filepath.Join(tmp, tt.path), tt.symlink, tt.dirMode)

			err := SyncInWithOverlays(tmp, targetRoot, nil, testLogger())
			require.ErrorContains(t, err, tt.want)

			_, err = os.Lstat(filepath.Join(tmp, "missing"))
			require.ErrorIs(t, err, os.ErrNotExist, "sync must not write through dangling symlinks")
		})
	}
}

func placeConflict(t *testing.T, path, symlink string, dirMode os.FileMode) {
	t.Helper()

	require.NoError(t, os.RemoveAll(path))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	switch {
	case symlink != "":
		require.NoError(t, os.Symlink(symlink, path))
	case dirMode.IsDir():
		require.NoError(t, os.Mkdir(path, 0o755))
		require.NoError(t, os.Chmod(path, dirMode.Perm()))
		t.Cleanup(func() { require.NoError(t, os.Chmod(path, 0o755)) })
	default:
		require.NoError(t, os.WriteFile(path, []byte("file"), 0o644))
	}
}

func TestResetRuntimeDirectoryPreservesAuthAndAttachments(t *testing.T) {
	target := filepath.Join(t.TempDir(), targetRoot)
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, ".auth.json-in-flight"), []byte("token"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(target, "remove-me"), []byte("trash"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(target, "attachments"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(target, "attachments", "original"), []byte("immutable"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(target, "sandbox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "sandbox", "rootfs"), []byte("rootfs"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(target, "uploads", "web:c1", "up1"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(target, "uploads", "web:c1", "up1", "notes.txt"), []byte("notes"), 0o600))

	for _, preserveOverlays := range []bool{false, true} {
		require.NoError(t, resetRuntimeDirectory(target, "", testLogger(), preserveOverlays))
		data, err := os.ReadFile(filepath.Join(target, "attachments", "original"))
		require.NoError(t, err)
		require.Equal(t, "immutable", string(data))
		require.FileExists(t, filepath.Join(target, "sandbox", "rootfs"))
		require.FileExists(t, filepath.Join(target, "uploads", "web:c1", "up1", "notes.txt"))
	}

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
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "agents", "bot.md"), []byte("keep overlay"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "skills", "example", "SKILL.md"), []byte("keep skill"), 0o644))

	require.NoError(t, SyncInWithOverlays(tmp, targetRoot, nil, testLogger()))

	data, err := os.ReadFile(filepath.Join(tmp, "agents", "bot.md"))
	require.NoError(t, err)
	assert.Equal(t, "keep overlay", string(data))

	data, err = os.ReadFile(filepath.Join(tmp, targetRoot, "agents", "bot.md"))
	require.NoError(t, err)
	assert.Equal(t, "keep overlay", string(data))

	data, err = os.ReadFile(filepath.Join(tmp, targetRoot, "skills", "example", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "keep skill", string(data))
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

	require.NoError(t, syncFSFiltered(src, scriptsRoot, root, "test sync", testLogger(), true, true, inertFileFilter{}))

	info, err := os.Stat(filepath.Join(root, "run"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	info, err = os.Stat(filepath.Join(root, "plain.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestSyncFSFilteredReportsMissingSourceRoot(t *testing.T) {
	err := syncFSFiltered(fstest.MapFS{}, payloadRoot, filepath.Join(t.TempDir(), targetRoot), "test sync", testLogger(), true, false, inertFileFilter{})
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

	err := syncFSFiltered(src, payloadRoot, root, "test sync", testLogger(), true, false, inertFileFilter{})
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

	err := syncFSFiltered(src, payloadRoot, root, "test sync", testLogger(), true, false, inertFileFilter{})
	require.ErrorContains(t, err, "create skeleton directory")
}

func TestOverlayMissingIsNoop(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, overlayWorkspaceIn(tmp, filepath.Join(tmp, targetRoot), testLogger()))

	_, err := os.Stat(filepath.Join(tmp, targetRoot))
	require.ErrorIs(t, err, os.ErrNotExist)
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

	assert.NoDirExists(t, filepath.Join(tmp, targetRoot, "cron"), "cron/ is no longer copied")
}

func TestSyncInWithOverlaysAppliesGitBeforeLocalOverlay(t *testing.T) {
	_, err := exec.LookPath("git")
	require.NoError(t, err, "git is required for overlay test")

	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	repoGit(t, repo, "init")
	repoGit(t, repo, "config", "user.email", "test@example.com")
	repoGit(t, repo, "config", "user.name", "Test User")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "skills", "remote"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "cron"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "agents", "bot.md"), []byte("remote agent"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "skills", "remote", "SKILL.md"), []byte("remote skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "cron", "daily.md"), []byte("remote cron"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "scripts", "tool.sh"), []byte("remote script"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "scripts", "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "scripts", "nested", "remote.sh"), []byte("nested remote script"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "scripts", "run"), []byte("remote executable"), 0o755))
	require.NoError(t, os.Chmod(filepath.Join(repo, "scripts", "run"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "scripts", "plain.sh"), []byte("remote non-executable"), 0o644))
	repoGit(t, repo, "add", ".")
	repoGit(t, repo, "commit", "-m", "overlay")

	workspace := filepath.Join(tmp, "workspace")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "bot.md"), []byte("local agent"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "scripts", "tool.sh"), []byte("local script"), 0o644))

	require.NoError(t, SyncInWithOverlays(workspace, targetRoot, []string{repo}, testLogger()))
	overlayInfo := OverlayInfos(filepath.Join(workspace, targetRoot), []string{repo})[0]
	assert.DirExists(t, filepath.Join(overlayInfo.ClonePath, ".git"))
	assert.FileExists(t, filepath.Join(overlayInfo.ClonePath, "agents", "bot.md"))

	data, err := os.ReadFile(filepath.Join(workspace, targetRoot, "agents", "bot.md"))
	require.NoError(t, err)
	assert.Equal(t, "local agent", string(data))

	data, err = os.ReadFile(filepath.Join(workspace, targetRoot, "skills", "remote", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "remote skill", string(data))

	assert.NoDirExists(t, filepath.Join(workspace, targetRoot, "cron"), "cron/ is no longer copied")

	data, err = os.ReadFile(filepath.Join(workspace, targetRoot, "scripts", "tool.sh"))
	require.NoError(t, err)
	assert.Equal(t, "local script", string(data))

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
	_, err := exec.LookPath("git")
	require.NoError(t, err, "git is required for overlay test")

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

func TestSyncWorkspaceScriptSymlinksReplacesRuntimeSymlinks(t *testing.T) {
	for _, tt := range []struct {
		runtimeRoot string
		relative    bool
	}{{".rocketclaw", false}, {".femtoclaw", false}, {".rocketclaw", true}} {
		t.Run(fmt.Sprintf("%s/relative=%t", tt.runtimeRoot, tt.relative), func(t *testing.T) {
			workspace := t.TempDir()

			if tt.relative {
				cwd, err := os.Getwd()
				require.NoError(t, err)
				workspace, err = filepath.Rel(cwd, workspace)
				require.NoError(t, err)
			}

			runtimeScripts := filepath.Join(workspace, targetRoot, "scripts")
			workspaceScripts := filepath.Join(workspace, "scripts")

			require.NoError(t, os.MkdirAll(runtimeScripts, 0o755))
			require.NoError(t, os.MkdirAll(workspaceScripts, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(runtimeScripts, "tool.sh"), []byte("runtime script"), 0o644))

			staleTarget := filepath.Join("..", tt.runtimeRoot, "scripts", "tool.sh")
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

func TestSyncWorkspaceScriptSymlinksReportsConflicts(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		symlink string
		dirMode os.FileMode
		want    string
	}{
		{name: "workspace script directory", path: "scripts/nested/tool.sh", dirMode: os.ModeDir | 0o755, want: "workspace script path is a directory"},
		{name: "workspace scripts symlink loop", path: "scripts", symlink: "scripts", want: "stat workspace scripts directory"},
		{name: "runtime scripts symlink loop", path: targetRoot + "/scripts", symlink: "scripts", want: "stat runtime scripts directory"},
		{name: "runtime scripts file", path: targetRoot + "/scripts", want: "runtime scripts path is not a directory"},
		{name: "runtime scripts unreadable", path: targetRoot + "/scripts/nested", dirMode: os.ModeDir, want: "walk runtime scripts: walk runtime scripts"},
		{name: "workspace script parent file", path: "scripts/nested", want: "walk runtime scripts: stat workspace script"},
		{name: "workspace script parent dangling symlink", path: "scripts/nested", symlink: "missing", want: "walk runtime scripts: create workspace script directory"},
		{name: "workspace script parent read-only", path: "scripts/nested", dirMode: os.ModeDir | 0o500, want: "walk runtime scripts: create workspace script symlink"},
		{name: "git directory symlink loop", path: ".git", symlink: ".git", want: "stat git directory"},
		{name: "git exclude directory", path: ".git/info/exclude", dirMode: os.ModeDir | 0o755, want: "read git exclude file"},
		{name: "git info dangling symlink", path: ".git/info", symlink: "missing", want: "create git info directory"},
		{name: "git info read-only", path: ".git/info", dirMode: os.ModeDir | 0o500, want: "write git exclude file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, targetRoot, "scripts", "nested"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(workspace, targetRoot, "scripts", "nested", "tool.sh"), []byte("runtime script"), 0o644))
			placeConflict(t, filepath.Join(workspace, tt.path), tt.symlink, tt.dirMode)

			err := syncWorkspaceScriptSymlinks(workspace, targetRoot, testLogger())
			require.ErrorContains(t, err, tt.want)
		})
	}
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

func TestSyncWorkspaceScriptSymlinksSkipsGitExcludeWithoutGitDirectory(t *testing.T) {
	for _, tt := range []struct {
		name    string
		gitFile bool
	}{
		{name: "no repository"},
		{name: "worktree git file", gitFile: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, targetRoot, "scripts"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(workspace, targetRoot, "scripts", "tool.sh"), []byte("tool"), 0o644))

			gitPath := filepath.Join(workspace, ".git")
			if tt.gitFile {
				require.NoError(t, os.WriteFile(gitPath, []byte("gitdir: ../repository/.git/worktrees/test\n"), 0o644))
			}

			require.NoError(t, syncWorkspaceScriptSymlinks(workspace, targetRoot, testLogger()))

			if tt.gitFile {
				data, err := os.ReadFile(gitPath)
				require.NoError(t, err)
				assert.Equal(t, "gitdir: ../repository/.git/worktrees/test\n", string(data))
			} else {
				_, err := os.Stat(gitPath)
				require.ErrorIs(t, err, os.ErrNotExist)
			}

			link, err := os.Readlink(filepath.Join(workspace, "scripts", "tool.sh"))
			require.NoError(t, err)
			assert.Equal(t, filepath.Join("..", targetRoot, "scripts", "tool.sh"), link)
		})
	}
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

func TestOverlayInfosParsesGitSpec(t *testing.T) {
	for _, tt := range []struct {
		spec, wantURL, wantRef string
	}{
		{spec: "github.com/rocketable/overlay", wantURL: "https://github.com/rocketable/overlay"},
		{spec: "github.com/rocketable/overlay@main", wantURL: "https://github.com/rocketable/overlay", wantRef: "main"},
		{spec: "git@github.com:rocketable/overlay.git", wantURL: "git@github.com:rocketable/overlay.git"},
		{spec: "git@github.com:Rocketable/acme-cs.git@main", wantURL: "git@github.com:Rocketable/acme-cs.git", wantRef: "main"},
		{spec: "https://github.com/Rocketable/acme-cs.git@main", wantURL: "https://github.com/Rocketable/acme-cs.git", wantRef: "main"},
		{spec: "ssh://git@github.com/rocketable/overlay.git", wantURL: "ssh://git@github.com/rocketable/overlay.git"},
		{spec: "ssh://git@github.com/Rocketable/acme-cs.git@main", wantURL: "ssh://git@github.com/Rocketable/acme-cs.git", wantRef: "main"},
		{spec: "./overlay", wantURL: "./overlay"},
		{spec: "../overlay", wantURL: "../overlay"},
		{spec: "/local/overlay", wantURL: "/local/overlay"},
		{spec: "overlay", wantURL: "overlay"},
		{spec: "github.com/rocketable/overlay @ main ", wantURL: "https://github.com/rocketable/overlay", wantRef: "main"},
	} {
		t.Run(tt.spec, func(t *testing.T) {
			infos := OverlayInfos(filepath.Join("workspace", targetRoot), []string{tt.spec})
			require.Len(t, infos, 1)
			assert.Equal(t, tt.wantURL, infos[0].URL)
			assert.Equal(t, tt.wantRef, infos[0].Ref)
		})
	}
}

func TestOverlayInfos(t *testing.T) {
	workspace := string(filepath.Separator) + "workspace"
	infos := OverlayInfos(filepath.Join(workspace, targetRoot), []string{" github.com/rocketable/overlay@main ", ""})

	require.Len(t, infos, 1)
	assert.Equal(t, "github.com/rocketable/overlay@main", infos[0].Spec)
	assert.Equal(t, "https://github.com/rocketable/overlay", infos[0].URL)
	assert.Equal(t, "main", infos[0].Ref)
	assert.Equal(t, filepath.Join(workspace, targetRoot, "overlays", "github.com-rocketable-overlay-main"), infos[0].ClonePath)

	for _, tt := range []struct {
		url, ref, want string
	}{
		{"https://École.example//a+++项目.git", "--β /٢--", "École.example-a-项目-β-٢"},
		{"---///+++", "", "overlay"},
		{"ssh://git@EXAMPLE/a._b.git", "", "git-EXAMPLE-a._b"},
	} {
		assert.Equal(t, tt.want, gitOverlaySlug(tt.url, tt.ref))
	}
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
	assert.Equal(t, "auth.json\nauth.json.lock\nsandbox/\n", string(data))

	for _, dir := range []string{agentsRoot, filepath.Join(targetRoot, agentsRoot)} {
		entries, err := os.ReadDir(filepath.Join(tmp, dir))
		require.NoError(t, err)
		require.Len(t, entries, 1, dir)
		assert.Equal(t, "examples", entries[0].Name(), dir)
	}
}

func TestSyncIgnoresBuiltinAgentFiles(t *testing.T) {
	_, err := exec.LookPath("git")
	require.NoError(t, err, "git is required for overlay test")

	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	workspace := filepath.Join(tmp, "workspace")

	repoGit(t, repo, "init")
	repoGit(t, repo, "config", "user.email", "test@example.com")
	repoGit(t, repo, "config", "user.name", "Test User")

	for _, dir := range []string{repo, workspace} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, agentsRoot, "main"), 0o755))

		for _, name := range []string{"main.md", "voice.md", "main/alt.md", "bot.md"} {
			require.NoError(t, os.WriteFile(filepath.Join(dir, agentsRoot, name), []byte(dir), 0o644))
		}
	}

	repoGit(t, repo, "add", ".")
	repoGit(t, repo, "commit", "-m", "overlay")

	var logs strings.Builder
	require.NoError(t, SyncInWithOverlays(workspace, targetRoot, []string{repo}, slog.New(slog.NewTextHandler(&logs, nil))))

	entries, err := os.ReadDir(filepath.Join(workspace, targetRoot, agentsRoot))
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	assert.Equal(t, []string{"bot.md", "examples"}, names)

	for _, source := range []string{OverlayInfos(filepath.Join(workspace, targetRoot), []string{repo})[0].ClonePath, workspace} {
		for _, name := range []string{"main.md", "voice.md", "main"} {
			assert.Contains(t, logs.String(), `level=WARN msg="ignored file named after a built-in agent" path=`+filepath.Join(source, agentsRoot, name)+"\n")
		}
	}
}

func TestBuiltinTextNamesRepositoryPaths(t *testing.T) {
	for _, name := range []string{"builtin/about.md", "builtin/agents/main.md"} {
		data, err := builtin.ReadFile(name)
		require.NoError(t, err)

		paths := regexp.MustCompile(`[\w-]+(?:/[\w.-]+)*\.(?:md|proto)`).FindAllString(string(data), -1)
		require.NotEmpty(t, paths, name)

		for _, path := range paths {
			_, err := os.Stat(filepath.Join("..", "..", "..", path))
			require.NoError(t, err, name)
		}
	}
}

func TestSyncEffectiveRuntimeAssetsDoesNotMutateRuntimeOrScriptSymlinks(t *testing.T) {
	workspace := t.TempDir()
	realRuntime := filepath.Join(workspace, targetRoot)
	realScripts := filepath.Join(workspace, scriptsRoot)
	target := filepath.Join(t.TempDir(), targetRoot)
	require.NoError(t, os.MkdirAll(filepath.Join(realRuntime, agentsRoot), 0o755))
	require.NoError(t, os.MkdirAll(realScripts, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(realRuntime, agentsRoot, "bot.md"), []byte("real runtime"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, agentsRoot, "bot.md"), []byte("overlay"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, scriptsRoot, "helper.sh"), []byte("script"), 0o644))

	require.NoError(t, SyncEffectiveRuntimeAssets(workspace, target, nil, testLogger()))

	data, err := os.ReadFile(filepath.Join(realRuntime, agentsRoot, "bot.md"))
	require.NoError(t, err)
	assert.Equal(t, "real runtime", string(data))
	data, err = os.ReadFile(filepath.Join(target, agentsRoot, "bot.md"))
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
	require.NoError(t, os.WriteFile(filepath.Join(liveAgents, "bot.md"), []byte("live"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(realScripts, "tool.sh"), []byte("regular"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, agentsRoot, "bot.md"), []byte("broken"), 0o644))

	errValidate := errors.New("invalid staged assets")
	err := ReplaceRuntimeAssetsAfterValidation(workspace, targetRoot, new(config.Behavior), testLogger(), new(config.LockedConfig), func(string) error { return errValidate }, func() error { return nil })
	require.ErrorIs(t, err, errValidate)

	data, err := os.ReadFile(filepath.Join(liveAgents, "bot.md"))
	require.NoError(t, err)
	assert.Equal(t, "live", string(data))
	data, err = os.ReadFile(filepath.Join(realScripts, "tool.sh"))
	require.NoError(t, err)
	assert.Equal(t, "regular", string(data))
}

func TestReplaceRuntimeAssetsAfterValidationCommitsFreshOverlayAndRefreshesScripts(t *testing.T) {
	_, err := exec.LookPath("git")
	require.NoError(t, err, "git is required for overlay test")

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

	locked := new(config.LockedConfig)
	require.NoError(t, ReplaceRuntimeAssetsAfterValidation(workspace, targetRoot, &config.Behavior{Overlays: []string{repo}}, testLogger(), locked, func(stagedWorkDir string) error {
		data, err := os.ReadFile(filepath.Join(workspace, stagedWorkDir, agentsRoot, "remote.md"))
		require.NoError(t, err)
		assert.Equal(t, "new remote", string(data))
		assert.Empty(t, locked.Clone().Overlays, "behavior applies only with the commit")

		return nil
	}, func() error { return nil }))
	assert.Equal(t, []string{repo}, locked.Clone().Overlays)

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

func TestValidateRuntimeAssetsWithStagesTheFile(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, agentsRoot), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, agentsRoot, "bot.md"), []byte("old"), 0o644))

	example, err := os.ReadFile(filepath.Join(agentsRoot, "examples", "session-tags.example.md"))
	require.NoError(t, err)

	errValidate := errors.New("invalid staged assets")
	staged := ""
	err = ValidateRuntimeAssetsWith(workspace, nil, testLogger(), map[string][]byte{"agents/bot.md": []byte("new")}, func(stagedWorkDir string) error {
		staged = stagedWorkDir

		for file, want := range map[string]string{"bot.md": "new", "examples/session-tags.example.md": string(example)} {
			data, err := os.ReadFile(filepath.Join(workspace, stagedWorkDir, agentsRoot, file))
			require.NoError(t, err)
			assert.Equal(t, want, string(data))
		}

		return errValidate
	})
	require.ErrorIs(t, err, errValidate)
	require.NotEmpty(t, staged)
	require.NoDirExists(t, filepath.Join(workspace, staged))
	require.NoDirExists(t, filepath.Join(workspace, targetRoot))

	data, err := os.ReadFile(filepath.Join(workspace, agentsRoot, "bot.md"))
	require.NoError(t, err)
	assert.Equal(t, "old", string(data))

	accept := func(string) error { return nil }
	require.ErrorContains(t, ValidateRuntimeAssetsWith(filepath.Join(workspace, "missing"), nil, testLogger(), nil, accept), "create rocketclaw reload stage")
	require.ErrorContains(t, ValidateRuntimeAssetsWith(workspace, nil, testLogger(), map[string][]byte{"nowhere/bot.md": nil}, accept), "write staged file nowhere/bot.md")

	require.NoError(t, os.RemoveAll(filepath.Join(workspace, agentsRoot)))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, agentsRoot), nil, 0o644))
	require.ErrorContains(t, ValidateRuntimeAssetsWith(workspace, nil, testLogger(), nil, accept), "not a directory")
}

func TestSyncFSRootMapping(t *testing.T) {
	tests := []struct {
		name string
		root string
		path string
		want string
	}{
		{name: "root dot", root: ".", path: "AGENTS.md", want: "AGENTS.md"},
		{name: "same path", root: ".rocketclaw", path: ".rocketclaw", want: "."},
		{name: "nested path", root: ".rocketclaw", path: ".rocketclaw/agents/ops.md", want: "agents/ops.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := fstest.MapFS{tt.path: {Data: []byte("mapped")}}
			if tt.path == tt.root {
				src[tt.path] = &fstest.MapFile{Mode: fs.ModeDir | 0o755}
			}

			target := filepath.Join(t.TempDir(), "target")
			require.NoError(t, syncFSFiltered(src, tt.root, target, "test sync", testLogger(), true, false, inertFileFilter{}))

			if tt.want == "." {
				info, err := os.Stat(target)
				require.NoError(t, err)
				assert.True(t, info.IsDir())

				return
			}

			data, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(tt.want)))
			require.NoError(t, err)
			assert.Equal(t, "mapped", string(data))
		})
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
