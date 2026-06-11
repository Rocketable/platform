// Package skel seeds the embedded rocketclaw skeleton into workspaces.
package skel

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	payloadRoot   = ".rocketclaw"
	targetRoot    = payloadRoot
	agentsRoot    = "agents"
	skillsRoot    = "skills"
	workspaceCron = "cron"
	scriptsRoot   = "scripts"
	overlaysRoot  = "overlays"
)

// OverlayInfo describes one configured git overlay materialized by SyncInWithOverlays.
type OverlayInfo struct {
	Spec, URL, Ref, ClonePath string
}

//go:embed AGENTS.md main-update-cortex.sh all:.rocketclaw all:agents all:cron
var payload embed.FS

// SyncInWithOverlays materializes embedded setup files, configured git overlays, and local overlays into workDir.
func SyncInWithOverlays(workspace, workDir string, overlays []string, logger *slog.Logger) error {
	entries, err := fs.ReadDir(payload, ".")
	if err != nil {
		return fmt.Errorf("read embedded root setup files: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		dst := filepath.Join(workspace, name)

		info, err := os.Stat(dst)
		if err == nil {
			if info.IsDir() {
				return fmt.Errorf("root setup path is a directory: %s", dst)
			}

			continue
		}

		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat root setup path %s: %w", dst, err)
		}

		data, err := fs.ReadFile(payload, name)
		if err != nil {
			return fmt.Errorf("read embedded root setup file %s: %w", name, err)
		}

		if err := os.WriteFile(dst, data, embeddedFileMode(name)); err != nil {
			return fmt.Errorf("write root setup file %s: %w", dst, err)
		}

		logger.Debug("wrote embedded root setup file", "path", dst, "bytes", len(data))
	}

	for _, root := range [...]string{agentsRoot, workspaceCron} {
		overlayTarget := filepath.Join(workspace, root)
		if err := syncFSFiltered(payload, root, overlayTarget, "unpacking embedded rocketclaw setup files", logger, false, false, nil); err != nil {
			return fmt.Errorf("unpack embedded rocketclaw overlay %s: %w", root, err)
		}
	}

	if err := os.MkdirAll(filepath.Join(workspace, skillsRoot), 0o755); err != nil {
		return fmt.Errorf("create rocketclaw skills overlay directory: %w", err)
	}

	target := filepath.Join(workspace, workDir)
	if err := resetTarget(target, logger); err != nil {
		return fmt.Errorf("reset rocketclaw target: %w", err)
	}

	if err := syncFSFiltered(payload, payloadRoot, target, "syncing embedded rocketclaw skeleton", logger, true, false, nil); err != nil {
		return fmt.Errorf("sync embedded rocketclaw skeleton: %w", err)
	}

	for _, root := range [...]string{agentsRoot, workspaceCron} {
		if err := syncFSFiltered(payload, root, filepath.Join(target, root), "syncing embedded rocketclaw runtime assets", logger, true, false, nil); err != nil {
			return fmt.Errorf("sync embedded rocketclaw runtime assets %s: %w", root, err)
		}
	}

	overlayInfos := overlayInfosIn(filepath.Join(workspace, workDir), overlays)
	if err := reconcileGitOverlays(target, overlayInfos, logger); err != nil {
		return fmt.Errorf("reconcile configured rocketclaw overlays: %w", err)
	}

	for _, overlay := range overlayInfos {
		if err := applyGitOverlay(target, overlay, logger); err != nil {
			return fmt.Errorf("apply configured rocketclaw overlay %q: %w", overlay.Spec, err)
		}
	}

	if err := overlayWorkspaceIn(workspace, target, logger); err != nil {
		return fmt.Errorf("apply rocketclaw overlay: %w", err)
	}

	if err := syncWorkspaceScriptSymlinks(workspace, workDir, logger); err != nil {
		return fmt.Errorf("sync workspace script symlinks: %w", err)
	}

	return nil
}

// SyncEffectiveRuntimeAssets materializes startup-equivalent runtime assets into target without touching runtime state or workspace script symlinks.
func SyncEffectiveRuntimeAssets(workspace, target string, overlays []string, logger *slog.Logger) error {
	if err := syncFSFiltered(payload, payloadRoot, target, "syncing embedded rocketclaw skeleton", logger, true, false, nil); err != nil {
		return fmt.Errorf("sync embedded rocketclaw skeleton: %w", err)
	}

	for _, root := range [...]string{agentsRoot, workspaceCron} {
		if err := syncFSFiltered(payload, root, filepath.Join(target, root), "syncing embedded rocketclaw runtime assets", logger, true, false, nil); err != nil {
			return fmt.Errorf("sync embedded rocketclaw runtime assets %s: %w", root, err)
		}
	}

	overlayInfos := overlayInfosIn(target, overlays)
	if err := reconcileGitOverlays(target, overlayInfos, logger); err != nil {
		return fmt.Errorf("reconcile configured rocketclaw overlays: %w", err)
	}

	for _, overlay := range overlayInfos {
		if err := applyGitOverlay(target, overlay, logger); err != nil {
			return fmt.Errorf("apply configured rocketclaw overlay %q: %w", overlay.Spec, err)
		}
	}

	if err := overlayWorkspaceIn(workspace, target, logger); err != nil {
		return fmt.Errorf("apply rocketclaw overlay: %w", err)
	}

	return nil
}

func syncWorkspaceScriptSymlinks(workspace, workDir string, logger *slog.Logger) error {
	workspaceScripts := filepath.Join(workspace, scriptsRoot)
	runtimeScripts := filepath.Join(workspace, workDir, scriptsRoot)

	if _, err := os.Stat(workspaceScripts); err == nil {
		if err := removeRuntimeScriptSymlinks(workspace, workspaceScripts, logger); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat workspace scripts directory %s: %w", workspaceScripts, err)
	}

	info, err := os.Stat(runtimeScripts)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("stat runtime scripts directory %s: %w", runtimeScripts, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("runtime scripts path is not a directory: %s", runtimeScripts)
	}

	if err := filepath.WalkDir(runtimeScripts, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk runtime scripts %s: %w", path, err)
		}

		if d.IsDir() {
			return nil
		}

		if d.Type()&os.ModeType != 0 {
			return nil
		}

		rel, err := filepath.Rel(runtimeScripts, path)
		if err != nil {
			return fmt.Errorf("compute runtime script relative path %s: %w", path, err)
		}

		dst := filepath.Join(workspaceScripts, rel)
		if info, err := os.Lstat(dst); err == nil {
			if info.IsDir() {
				return fmt.Errorf("workspace script path is a directory: %s", dst)
			}

			logger.Debug("preserved existing workspace script", "path", dst)

			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat workspace script %s: %w", dst, err)
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("create workspace script directory %s: %w", filepath.Dir(dst), err)
		}

		target, err := filepath.Rel(filepath.Dir(dst), path)
		if err != nil {
			return fmt.Errorf("compute workspace script symlink target %s: %w", path, err)
		}

		if err := os.Symlink(target, dst); err != nil {
			return fmt.Errorf("create workspace script symlink %s: %w", dst, err)
		}

		logger.Debug("created workspace script symlink", "path", dst, "target", target)

		return nil
	}); err != nil {
		return fmt.Errorf("walk runtime scripts: %w", err)
	}

	return nil
}

func removeRuntimeScriptSymlinks(workspace, workspaceScripts string, logger *slog.Logger) error {
	runtimeRoots := []string{
		filepath.Clean(filepath.Join(workspace, ".rocketclaw")),
		filepath.Clean(filepath.Join(workspace, ".femtoclaw")),
	}

	if err := filepath.WalkDir(workspaceScripts, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk workspace scripts %s: %w", path, err)
		}

		if d.Type()&os.ModeSymlink == 0 {
			return nil
		}

		target, err := os.Readlink(path)
		if err != nil {
			return fmt.Errorf("read workspace script symlink %s: %w", path, err)
		}

		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}

		target = filepath.Clean(target)
		for _, root := range runtimeRoots {
			if target == root || strings.HasPrefix(target, root+string(filepath.Separator)) {
				if err := os.Remove(path); err != nil {
					return fmt.Errorf("remove runtime workspace script symlink %s: %w", path, err)
				}

				logger.Debug("removed runtime workspace script symlink", "path", path, "target", target)

				return nil
			}
		}

		return nil
	}); err != nil {
		return fmt.Errorf("walk workspace scripts: %w", err)
	}

	return nil
}

func overlayWorkspaceIn(workspace, target string, logger *slog.Logger) error {
	for _, root := range [...]string{agentsRoot, skillsRoot, workspaceCron, scriptsRoot} {
		dir := filepath.Join(workspace, root)

		info, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				logger.Debug("no rocketclaw overlay directory found", "path", dir)
				continue
			}

			return fmt.Errorf("stat rocketclaw overlay directory %s: %w", dir, err)
		}

		if !info.IsDir() {
			return fmt.Errorf("rocketclaw overlay path is not a directory: %s", dir)
		}

		logger.Info("applying rocketclaw overlay directory", "path", dir)

		if err := syncFSFiltered(
			os.DirFS(workspace),
			root,
			filepath.Join(target, root),
			"applying rocketclaw overlay directory",
			logger,
			true,
			true,
			func(name string, d fs.DirEntry) bool {
				return !d.IsDir() && strings.HasSuffix(strings.ToLower(filepath.Base(name)), "example.md")
			},
		); err != nil {
			return err
		}
	}

	return nil
}

// OverlayInfos returns normalized configured overlay clone metadata in config order.
func OverlayInfos(workspace, workDir string, overlays []string) []OverlayInfo {
	return overlayInfosIn(filepath.Join(workspace, workDir), overlays)
}

func overlayInfosIn(target string, overlays []string) []OverlayInfo {
	infos := make([]OverlayInfo, 0, len(overlays))
	for _, spec := range overlays {
		spec = strings.TrimSpace(spec)

		url, ref := parseGitOverlaySpec(spec)
		if url == "" {
			continue
		}

		infos = append(infos, OverlayInfo{Spec: spec, URL: url, Ref: ref, ClonePath: filepath.Join(target, overlaysRoot, gitOverlaySlug(url, ref))})
	}

	return infos
}

func reconcileGitOverlays(target string, overlays []OverlayInfo, logger *slog.Logger) error {
	overlayRoot := filepath.Join(target, overlaysRoot)

	info, err := os.Stat(overlayRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if len(overlays) == 0 {
				return nil
			}

			if err := os.MkdirAll(overlayRoot, 0o755); err != nil {
				return fmt.Errorf("create configured overlay clone directory %s: %w", overlayRoot, err)
			}
		} else {
			return fmt.Errorf("stat configured overlay clone directory %s: %w", overlayRoot, err)
		}
	}

	if info != nil && !info.IsDir() {
		return fmt.Errorf("configured overlay clone path is not a directory: %s", overlayRoot)
	}

	active := map[string]struct{}{}
	for _, overlay := range overlays {
		active[filepath.Base(overlay.ClonePath)] = struct{}{}
	}

	entries, err := os.ReadDir(overlayRoot)
	if err != nil {
		return fmt.Errorf("read configured overlay clone directory %s: %w", overlayRoot, err)
	}

	for _, entry := range entries {
		if _, ok := active[entry.Name()]; ok {
			continue
		}

		path := filepath.Join(overlayRoot, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove stale configured overlay clone %s: %w", path, err)
		}

		logger.Debug("removed stale configured overlay clone", "path", path)
	}

	return nil
}

func applyGitOverlay(target string, overlay OverlayInfo, logger *slog.Logger) error {
	if overlay.URL == "" {
		return errors.New("overlay repository is required")
	}

	if err := os.RemoveAll(overlay.ClonePath); err != nil {
		return fmt.Errorf("clean configured overlay clone %s: %w", overlay.ClonePath, err)
	}

	if err := os.MkdirAll(overlay.ClonePath, 0o755); err != nil {
		return fmt.Errorf("create configured overlay clone %s: %w", overlay.ClonePath, err)
	}

	for _, args := range [][]string{
		{"init"},
		{"remote", "add", "origin", overlay.URL},
		{"sparse-checkout", "init", "--cone"},
		{"sparse-checkout", "set", agentsRoot, skillsRoot, workspaceCron, scriptsRoot},
	} {
		if err := runGit(overlay.ClonePath, args...); err != nil {
			return err
		}
	}

	fetchRef := overlay.Ref
	if fetchRef == "" {
		fetchRef = "HEAD"
	}

	if err := runGit(overlay.ClonePath, "fetch", "--depth=1", "--filter=blob:none", "origin", fetchRef); err != nil {
		return err
	}

	if err := runGit(overlay.ClonePath, "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return err
	}

	for _, root := range [...]string{agentsRoot, skillsRoot, workspaceCron, scriptsRoot} {
		if _, err := os.Stat(filepath.Join(overlay.ClonePath, root)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			return fmt.Errorf("stat overlay directory %s: %w", root, err)
		}

		var skip func(string, fs.DirEntry) bool
		if root == agentsRoot {
			skip = func(name string, d fs.DirEntry) bool {
				return !d.IsDir() && relativePath(root, name) == "guardrail.md"
			}
		}

		if err := syncFSFiltered(os.DirFS(overlay.ClonePath), root, filepath.Join(target, root), "applying configured rocketclaw overlay", logger, true, true, skip); err != nil {
			return err
		}
	}

	return nil
}

func parseGitOverlaySpec(spec string) (url, ref string) {
	spec = strings.TrimSpace(spec)
	if i := strings.LastIndex(spec, "@"); i > 0 && !strings.Contains(spec[i+1:], "/") && strings.Contains(spec[:i], "/") {
		return normalizeGitOverlayURL(spec[:i]), strings.TrimSpace(spec[i+1:])
	}

	return normalizeGitOverlayURL(spec), ""
}

func normalizeGitOverlayURL(url string) string {
	url = strings.TrimSpace(url)
	if strings.Contains(url, "://") || strings.HasPrefix(url, "git@") {
		return url
	}

	if filepath.IsAbs(url) || strings.HasPrefix(url, "./") || strings.HasPrefix(url, "../") {
		return url
	}

	if strings.Contains(url, "/") {
		return "https://" + url
	}

	return url
}

func gitOverlaySlug(url, ref string) string {
	value := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "ssh://"), ".git")
	if ref != "" {
		value += "-" + ref
	}

	var slug strings.Builder

	lastHyphen := false

	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' {
			slug.WriteRune(r)

			lastHyphen = false

			continue
		}

		if !lastHyphen {
			slug.WriteByte('-')

			lastHyphen = true
		}
	}

	result := strings.Trim(slug.String(), "-")
	if result == "" {
		return "overlay"
	}

	return result
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}

	return nil
}

func resetTarget(target string, logger *slog.Logger) error {
	preservedTargetEntries := map[string]struct{}{
		".rocketcode":   {},
		"auth.json":     {},
		"overlays":      {},
		"state.sqlite3": {},
		"web-ui.crt":    {},
		"web-ui.key":    {},
	}

	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return fmt.Errorf("stat rocketclaw target %s: %w", target, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("rocketclaw target path is not a directory: %s", target)
	}

	entries, err := os.ReadDir(target)
	if err != nil {
		return fmt.Errorf("read rocketclaw target %s: %w", target, err)
	}

	for _, entry := range entries {
		if _, ok := preservedTargetEntries[entry.Name()]; ok {
			logger.Debug("preserved rocketclaw target entry", "path", filepath.Join(target, entry.Name()))
			continue
		}

		path := filepath.Join(target, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove rocketclaw target entry %s: %w", path, err)
		}

		logger.Debug("removed rocketclaw target entry", "path", path)
	}

	return nil
}

func syncFSFiltered(src fs.FS, root, target, message string, logger *slog.Logger, overwrite, preserveExecutable bool, skip func(string, fs.DirEntry) bool) error {
	logger.Info(message, "path", target)

	if err := fs.WalkDir(src, root, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk skeleton source %s: %w", name, err)
		}

		if skip != nil && skip(name, d) {
			logger.Debug("skipped rocketclaw overlay file", "path", name)
			return nil
		}

		rel := relativePath(root, name)

		dst := target
		if rel != "." {
			dst = filepath.Join(target, filepath.FromSlash(rel))
		}

		if d.IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return fmt.Errorf("create skeleton directory %s: %w", dst, err)
			}

			return nil
		}

		data, err := fs.ReadFile(src, name)
		if err != nil {
			return fmt.Errorf("read skeleton source file %s: %w", name, err)
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("create parent directory for %s: %w", dst, err)
		}

		if !overwrite {
			if _, err := os.Stat(dst); err == nil {
				logger.Debug("preserved existing embedded rocketclaw file", "path", dst)
				return nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("stat skeleton target %s: %w", dst, err)
			}
		}

		mode := embeddedFileMode(dst)

		if preserveExecutable {
			info, err := d.Info()
			if err != nil {
				return fmt.Errorf("stat skeleton source file %s: %w", name, err)
			}

			mode = 0o644
			if info.Mode().Perm()&0o111 != 0 {
				mode = 0o755
			}
		}

		if err := os.WriteFile(dst, data, mode); err != nil {
			return fmt.Errorf("write skeleton file %s: %w", dst, err)
		}

		if err := os.Chmod(dst, mode); err != nil {
			return fmt.Errorf("chmod skeleton file %s: %w", dst, err)
		}

		logger.Debug("wrote embedded rocketclaw file", "path", dst, "bytes", len(data))

		return nil
	}); err != nil {
		return fmt.Errorf("copy skeleton source %s: %w", root, err)
	}

	return nil
}

func relativePath(root, name string) string {
	if name == root {
		return "."
	}

	if root == "." {
		return name
	}

	return strings.TrimPrefix(name, root+"/")
}

func embeddedFileMode(name string) os.FileMode {
	if strings.HasSuffix(name, ".sh") {
		return 0o755
	}

	return 0o644
}
