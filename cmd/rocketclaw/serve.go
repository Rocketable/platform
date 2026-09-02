package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
)

func runServe(args []string) error {
	flagSet := flag.NewFlagSet("rocketclaw", flag.ContinueOnError)
	secretsARN := flagSet.String(secretsARNFlag, "", secretsARNUsage)
	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse serve flags: %w", err)
	}

	selected, cfg, err := loadRuntimeConfig(*secretsARN)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	configPath, err := filepath.Abs(selected.Path)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}

	logger := newLogger(cfg.Logging.Level)
	logger.Info(
		"loaded rocketclaw configuration",
		"config_path", selected.Path,
		"workspace", cfg.Workspace,
		"work_dir", cfg.RuntimeDirName(),
		"log_level", cfg.Logging.Level,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	version := "(unknown)"
	if info, ok := debug.ReadBuildInfo(); ok {
		version = info.Main.Version
	}
	logger.Info("starting rocketclaw", "version", version)

	if err := backend.Run(ctx, cfg, configPath, logger, processAssembler{}); err != nil {
		if errors.Is(err, backend.ErrRestartRequested) {
			logger.Info("rocketclaw restart requested; exiting with code 255 for supervisor restart")
			return serveRunError(err)
		}

		logger.Error("rocketclaw exited with error", "error", err)

		return serveRunError(err)
	}

	logger.Info("rocketclaw stopped")

	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("wait for rocketclaw shutdown: %w", err)
	}

	return nil
}

func serveRunError(err error) error {
	if errors.Is(err, backend.ErrRestartRequested) {
		return exitCodeError(255)
	}

	return fmt.Errorf("run rocketclaw: %w", err)
}
