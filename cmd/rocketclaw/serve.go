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
	"regexp"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/posthog/posthog-go"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"
)

var pseudoVersionRevision = regexp.MustCompile(`[-.]\d{14}-([0-9a-f]{12})$`)

func runServe(args []string) error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("disable crash dumps: %w", err)
	}

	processStart := time.Now().UTC().Format(time.RFC3339Nano)
	flagSet := flag.NewFlagSet("rocketclaw", flag.ContinueOnError)
	secretsARN := flagSet.String(secretsARNFlag, "", secretsARNUsage)
	usersARN := flagSet.String(usersARNFlag, "", "Secrets Manager ARN of a JSON secret to use as mcp_external.users")
	diagnosticsAddress := pprofAddress(defaultPprofAddress)
	flagSet.Var(&diagnosticsAddress, "pprof", "serve private Go profiles at this host:port; true means "+defaultPprofAddress+", false turns them off")
	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse serve flags: %w", err)
	}

	selected, cfg, err := loadRuntimeConfig(*secretsARN, *usersARN)
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	var mirrors []slog.Handler

	if cfg.PostHog.ProjectToken != "" {
		client, err := posthog.NewWithConfig(cfg.PostHog.ProjectToken, posthog.Config{Endpoint: cfg.PostHog.Host, ShutdownTimeout: 5 * time.Second})
		if err != nil {
			return fmt.Errorf("start PostHog client: %w", err)
		}

		defer func() {
			if err := client.Close(); err != nil {
				slog.Warn("close PostHog client", "error", err)
			}
		}()

		mirrors = postHogMirrors(client, cmp.Or(cfg.ServerDisplayName, cfg.Web.Tsnet.Hostname))
	}

	level := new(slog.LevelVar)
	backend.SetLogLevel(level, cfg.Logging.Level)
	logger := newLogger(level, mirrors...).With("process_start", processStart)
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
	cfg.Revision, cfg.RevisionModified = logBuildIdentity(logger, &build)

	if diagnosticsAddress != "" {
		listener, err := net.Listen("tcp", string(diagnosticsAddress))
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
			connections.Wait()
		}()

		logger.Info("started private pprof HTTP", "address", listener.Addr().String())
	}

	if err := backend.Run(ctx, cfg, logger, level, &processAssembler{}, sandbox.Bubblewrap{}); err != nil {
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

func logBuildIdentity(logger *slog.Logger, build *debug.BuildInfo) (revision string, modified bool) {
	attrs := []slog.Attr{slog.String("version", cmp.Or(build.Main.Version, "(unknown)"))}
	if build.GoVersion != "" {
		attrs = append(attrs, slog.String("go_version", build.GoVersion))
	}

	revision, modified = buildRevision(build)
	if revision != "" {
		attrs = append(attrs, slog.String("build_revision", revision), slog.Bool("build_dirty", modified))
	}

	logger.LogAttrs(context.Background(), slog.LevelInfo, "starting rocketclaw", attrs...)

	return revision, modified
}

func buildRevision(build *debug.BuildInfo) (revision string, modified bool) {
	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}

	if revision != "" || build.Main.Version == "(devel)" {
		return revision, modified
	}

	if match := pseudoVersionRevision.FindStringSubmatch(build.Main.Version); match != nil {
		return match[1], modified
	}

	return build.Main.Version, modified
}

func shutdownSignals() (context.Context, context.CancelFunc) {
	ctxSignal, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := context.WithCancelCause(context.Background())
	context.AfterFunc(ctxSignal, func() {
		signal.Notify(make(chan os.Signal, 1), syscall.SIGTERM)
		stop()
		cancel(context.Cause(ctxSignal))
	})

	return ctx, stop
}
