package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/agentlint"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

func runLint(args []string) error {
	target := "next"
	if len(args) > 0 {
		target = args[0]
	}
	if len(args) > 1 || (target != "next" && target != "current") {
		return fmt.Errorf("usage: rocketclaw lint [next|current]")
	}

	runtimeRoot, cleanup, err := runtimeRootForInspectionTarget(target, "rocketclaw-lint-*", "lint")
	if err != nil {
		return err
	}
	defer cleanup()

	result, err := agentlint.Lint(runtimeRoot)
	if err != nil {
		return err
	}

	if len(result.Findings) == 0 {
		return printStdout(fmt.Sprintf("rocketclaw lint %s: OK\n", target), "lint result")
	}

	lines := []string{fmt.Sprintf("rocketclaw lint %s: found %d findings", target, len(result.Findings))}
	for _, finding := range result.Findings {
		lines = append(lines, fmt.Sprintf("%s %s %s: %s", finding.Code, finding.Severity, finding.Path, finding.Message))
	}
	if err := printStdout(strings.Join(lines, "\n")+"\n", "lint result"); err != nil {
		return err
	}

	return exitCodeError(1)
}

func runtimeRootForInspectionTarget(target, tempPattern, buildName string) (string, func(), error) {
	cleanup := func() {
	}

	_, cfg, err := loadRuntimeConfig()
	if err != nil {
		return "", cleanup, fmt.Errorf("load config: %w", err)
	}

	runtimeRoot := filepath.Join(cfg.Workspace, cfg.WorkDirName())
	if target == "next" {
		tmp, err := os.MkdirTemp("", tempPattern)
		if err != nil {
			return "", cleanup, fmt.Errorf("create %s temp dir: %w", buildName, err)
		}
		cleanup = func() {
			os.RemoveAll(tmp)
		}

		runtimeRoot = filepath.Join(tmp, cfg.WorkDirName())
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		if err := skel.SyncEffectiveRuntimeAssets(cfg.Workspace, runtimeRoot, cfg.Overlays, logger); err != nil {
			cleanup()
			return "", cleanup, fmt.Errorf("build %s target: %w", buildName, err)
		}
	}

	return runtimeRoot, cleanup, nil
}
