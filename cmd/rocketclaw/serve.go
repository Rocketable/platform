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

	"github.com/Rocketable/platform/internal/rocketclaw/app"
)

func runServe(args []string) error {
	flagSet := flag.NewFlagSet("rocketclaw", flag.ContinueOnError)
	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse serve flags: %w", err)
	}

	selected, cfg, err := loadRuntimeConfig()
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
		"work_dir", cfg.WorkDirName(),
		"log_level", cfg.Logging.Level,
		"discord_enabled", cfg.DiscordVoice.Enabled,
		"slack_enabled", cfg.Slack.Enabled,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("starting rocketclaw", "version", buildInfoMainVersion())

	if err := app.Run(ctx, cfg, configPath, logger); err != nil {
		if errors.Is(err, app.ErrRestartRequested) {
			logger.Info("rocketclaw restart requested; exiting with code 255 for supervisor restart")
			return exitCodeError(255)
		}

		logger.Error("rocketclaw exited with error", "error", err)

		return fmt.Errorf("run rocketclaw: %w", err)
	}

	logger.Info("rocketclaw stopped")

	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("wait for rocketclaw shutdown: %w", err)
	}

	return nil
}

func buildInfoMainVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(unknown)"
	}

	return info.Main.Version
}
