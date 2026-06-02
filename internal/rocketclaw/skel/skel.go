// Package skel seeds the embedded rocketclaw skeleton into workspaces.
package skel

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
)

const (
	payloadRoot   = ".rocketclaw"
	targetRoot    = payloadRoot
	agentsRoot    = "agents"
	skillsRoot    = "skills"
	workspaceCron = "cron"
)

//go:embed AGENTS.md main-update-cortex.sh main-split-markdown-files.sh all:.rocketclaw all:agents all:cron
var payload embed.FS

// Sync materializes the embedded setup files and refreshes workspace/.rocketclaw.
func Sync(workspace string, logger *slog.Logger) error {
	return SyncIn(workspace, config.DefaultWorkDir, logger)
}

// SyncIn materializes the embedded setup files and refreshes workDir.
func SyncIn(workspace, workDir string, logger *slog.Logger) error {
	if strings.TrimSpace(workDir) == "" {
		workDir = config.DefaultWorkDir
	}

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

		if err := os.WriteFile(dst, data, syncFileMode(name)); err != nil {
			return fmt.Errorf("write root setup file %s: %w", dst, err)
		}

		logger.Debug("wrote embedded root setup file", "path", dst, "bytes", len(data))
	}

	for _, root := range [...]string{agentsRoot, workspaceCron} {
		overlayTarget := filepath.Join(workspace, root)
		if err := syncFSFiltered(payload, root, overlayTarget, "unpacking embedded rocketclaw setup files", logger, false, nil); err != nil {
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

	if err := syncFSFiltered(payload, payloadRoot, target, "syncing embedded rocketclaw skeleton", logger, true, nil); err != nil {
		return fmt.Errorf("sync embedded rocketclaw skeleton: %w", err)
	}

	if err := overlayIn(workspace, workDir, logger); err != nil {
		return fmt.Errorf("apply rocketclaw overlay: %w", err)
	}

	return nil
}

func overlay(workspace string, logger *slog.Logger) error {
	return overlayIn(workspace, config.DefaultWorkDir, logger)
}

func overlayIn(workspace, workDir string, logger *slog.Logger) error {
	for _, root := range [...]string{agentsRoot, skillsRoot} {
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
			filepath.Join(workspace, workDir, root),
			"applying rocketclaw overlay directory",
			logger,
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

func resetTarget(target string, logger *slog.Logger) error {
	preservedTargetEntries := map[string]struct{}{
		".rocketcode":   {},
		"auth.json":     {},
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

func syncFSFiltered(src fs.FS, root, target, message string, logger *slog.Logger, overwrite bool, skip func(string, fs.DirEntry) bool) error {
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

		if err := os.WriteFile(dst, data, syncFileMode(dst)); err != nil {
			return fmt.Errorf("write skeleton file %s: %w", dst, err)
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

func syncFileMode(name string) os.FileMode {
	if strings.HasSuffix(name, ".sh") {
		return 0o755
	}

	return 0o644
}
