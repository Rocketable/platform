package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/agentlint"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

func runLint(args []string) error {
	target, secretsARN, err := parseInspectionCommand("lint", args)
	if err != nil {
		return err
	}

	runtimeRoot, cfg, cleanup, err := runtimeRootForInspectionTarget(target, "rocketclaw-lint-*", "lint", secretsARN)
	if err != nil {
		return err
	}
	defer cleanup()

	result, err := agentlint.Lint(runtimeRoot, cfg)
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

func parseInspectionCommand(name string, args []string) (string, string, error) {
	flagSet := flag.NewFlagSet("rocketclaw "+name, flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	arn := flagSet.String(secretsARNFlag, "", secretsARNUsage)
	if err := flagSet.Parse(args); err != nil {
		return "", "", fmt.Errorf("parse %s flags: %w", name, err)
	}
	rest := flagSet.Args()
	target := "next"
	if len(rest) > 0 {
		target = rest[0]
	}
	if len(rest) > 1 || (target != "next" && target != "current") {
		return "", "", fmt.Errorf("usage: rocketclaw %s [next|current]", name)
	}

	return target, *arn, nil
}

func runtimeRootForInspectionTarget(target, tempPattern, buildName, secretsARN string) (string, *config.Config, func(), error) {
	cleanup := func() {
	}

	_, cfg, err := loadRuntimeConfig(secretsARN)
	if err != nil {
		return "", nil, cleanup, fmt.Errorf("load config: %w", err)
	}

	runtimeRoot := filepath.Join(cfg.Workspace, cfg.RuntimeDirName())
	if target == "next" {
		tmp, err := os.MkdirTemp("", tempPattern)
		if err != nil {
			return "", nil, cleanup, fmt.Errorf("create %s temp dir: %w", buildName, err)
		}
		cleanup = func() {
			os.RemoveAll(tmp)
		}

		runtimeRoot = filepath.Join(tmp, cfg.RuntimeDirName())
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		if err := skel.SyncEffectiveRuntimeAssets(cfg.Workspace, runtimeRoot, cfg.Overlays, logger); err != nil {
			cleanup()
			return "", nil, cleanup, fmt.Errorf("build %s target: %w", buildName, err)
		}
	}

	return runtimeRoot, cfg, cleanup, nil
}
