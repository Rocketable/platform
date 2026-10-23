package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"golang.org/x/sync/errgroup"
)

func runServe(args []string) error {
	processStart := time.Now().UTC().Format(time.RFC3339Nano)
	flagSet := flag.NewFlagSet("rocketclaw", flag.ContinueOnError)
	secretsARN := flagSet.String(secretsARNFlag, "", secretsARNUsage)
	pprofEnabled := flagSet.Bool("pprof", true, "serve private Go profiles at 127.0.0.1:6060")
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

	logger := newLogger(cfg.Logging.Level).With("process_start", processStart)
	logger.Info(
		"loaded rocketclaw configuration",
		"config_path", selected.Path,
		"workspace", cfg.Workspace,
		"work_dir", cfg.RuntimeDirName(),
		"log_level", cfg.Logging.Level,
	)

	ctx, stop := shutdownSignals()
	defer stop()

	var build debug.BuildInfo
	if info, ok := debug.ReadBuildInfo(); ok {
		build = *info
	}
	logBuildIdentity(logger, &build)

	if *pprofEnabled {
		listener, err := net.Listen("tcp", "127.0.0.1:6060")
		if err != nil {
			return fmt.Errorf("start private pprof HTTP: %w", err)
		}

		ctxDiagnostics, cancelDiagnostics := context.WithCancel(ctx)

		var connections sync.WaitGroup

		server := newDiagnosticsServer(ctxDiagnostics, &connections)

		var serving errgroup.Group

		serving.Go(func() error {
			serveDiagnostics(server, listener, logger)
			return nil
		})

		defer func() {
			cancelDiagnostics()
			if err := server.Close(); err != nil {
				logger.Error("close private pprof HTTP", "error_type", fmt.Sprintf("%T", err))
			}

			_ = listener.Close()
			_ = serving.Wait()
			// Serve has stopped adding connections; Closed follows handler exit.
			connections.Wait()
		}()

		logger.Info("started private pprof HTTP", "address", listener.Addr().String())
	}

	if err := backend.Run(ctx, cfg, configPath, logger, processAssembler{}); err != nil {
		if errors.Is(err, backend.ErrRestartRequested) {
			logger.Info("rocketclaw restart requested; exiting with code 255 for supervisor restart")
			return exitCodeError(255)
		}

		logger.Error("rocketclaw exited with error", "error", err)

		return fmt.Errorf("run rocketclaw: %w", err)
	}

	logger.Info("rocketclaw stopped")

	return nil
}

// logBuildIdentity uses only metadata embedded in the running binary, not checkout history.
func logBuildIdentity(logger *slog.Logger, build *debug.BuildInfo) {
	attrs := []slog.Attr{slog.String("version", cmp.Or(build.Main.Version, "(unknown)"))}
	if build.GoVersion != "" {
		attrs = append(attrs, slog.String("go_version", build.GoVersion))
	}

	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			attrs = append(attrs, slog.String("build_revision", setting.Value))
		case "vcs.modified":
			attrs = append(attrs, slog.String("build_dirty", setting.Value))
		}
	}

	logger.LogAttrs(context.Background(), slog.LevelInfo, "starting rocketclaw", attrs...)
}

// shutdownSignals is canceled by the first SIGINT or SIGTERM, which starts the graceful shutdown. A second Ctrl-C
// (SIGINT) then forces the exit. A repeated SIGTERM does not: systemd sends SIGTERM again to the processes left
// once a unit's main process exits, which happens at once when `go run` starts RocketClaw.
func shutdownSignals() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	context.AfterFunc(ctx, func() {
		signal.Notify(make(chan os.Signal, 1), syscall.SIGTERM)
		stop()
	})

	return ctx, stop
}
