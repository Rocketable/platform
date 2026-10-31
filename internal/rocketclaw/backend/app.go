package backend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/metrics"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"golang.org/x/sync/errgroup"
)

// ErrRestartRequested indicates rocketclaw should exit so a supervisor can restart it.
var ErrRestartRequested = errors.New("restart requested")

// errShutdown stops conversation work: bridges leave it for the next start, and
// harness turns stop once their running calls finish.
var errShutdown = fmt.Errorf("%w: %w", protocol.ErrBridgeStopped, harness.ErrShutdown)

const (
	stateRetention = 90 * 24 * time.Hour
	// statePruneInterval separates background retention passes; statePrunePace
	// is the pause after each deletion, a tenth of it after each check.
	statePruneInterval = time.Hour
	statePrunePace     = time.Second
)

// FrontendAssembler constructs Slack and MCP frontends for a running backend.
type FrontendAssembler interface {
	Assemble(*Runtime) (<-chan struct{}, []func(context.Context) error, error)
	ValidateAssets(*config.Config, string) error
}

type lockedRun struct {
	cancel     context.CancelFunc
	detach     func() bool
	signal     <-chan struct{}
	configPath string
	logger     *slog.Logger
	assemble   FrontendAssembler
	sessions   *SessionService
}

type runShutdown struct {
	logger           *slog.Logger
	cancelWork       context.CancelCauseFunc
	restartRequested chan struct{}

	once sync.Once
}

func (s *runShutdown) restart(reason string) string {
	started := s.start(reason, true)

	if !started {
		s.logger.Warn("restart requested while shutdown already in progress", "reason", reason)
	}

	return "restart requested; runtime cancellation started"
}

func (s *runShutdown) start(reason string, restart bool) bool {
	started := false

	s.once.Do(func() {
		started = true

		if restart {
			close(s.restartRequested)
		}

		s.logger.Warn("shutdown requested; canceling rocketclaw work", "reason", reason, "restart", restart)
		s.cancelWork(errShutdown)
	})

	return started
}

// Run starts rocketclaw and blocks until the context is canceled or a fatal error occurs.
func Run(ctx context.Context, cfg *config.Config, configPath string, logger *slog.Logger, assemble FrontendAssembler) error {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()

	detach := context.AfterFunc(ctx, cancel)

	stopInstrumentation, err := configureInstrumentation(runCtx, cfg.Instrumentation, cfg.PostHog)
	if err != nil {
		return err
	}

	defer func() {
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()

		if err := stopInstrumentation(shutdownCtx); err != nil {
			logger.Warn("stop instrumentation", "error", err)
		}
	}()

	stateLogger := logger.With("component", "state_store")

	startedAt := time.Now()

	stateLogger.Info("starting rocketclaw state store", "workspace", cfg.Workspace, "runtime_dir", cfg.RuntimeDirName())

	rocketcodeSessions, err := NewSessionServiceIn(runCtx, cfg, stateLogger)
	if err != nil {
		return fmt.Errorf("start rocketcode session service: %w", err)
	}

	stateLogger.Info("started rocketclaw state store", "elapsed", time.Since(startedAt))

	defer func() {
		if err := rocketcodeSessions.Stop(); err != nil {
			logger.Warn("stop rocketcode session service", "error", err)
		}
	}()

	samples := []metrics.Sample{
		{Name: "/gc/heap/allocs:bytes"},
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/cpu/classes/gc/total:cpu-seconds"},
		{Name: "/sched/goroutines:goroutines"},
		{Name: "/sched/latencies:seconds"},
	}
	baseline := sampleHealth(samples, rocketcodeSessions.db.Stats())
	healthCtx, cancelHealth := context.WithCancel(runCtx)

	var health errgroup.Group
	health.Go(func() error {
		reportHealth(healthCtx, rocketcodeSessions.db, logger, samples, &baseline)
		return nil
	})

	defer func() {
		cancelHealth()

		_ = health.Wait()
	}()

	return holdRunLock(runCtx, rocketcodeSessions.db, &lockedRun{
		cancel:     cancel,
		detach:     detach,
		signal:     ctx.Done(),
		configPath: configPath,
		logger:     logger,
		assemble:   assemble,
		sessions:   rocketcodeSessions,
	})
}

func (s *lockedRun) Run(runCtx context.Context) error {
	s.detach()

	cfg, configPath, logger, rocketcodeSessions := s.sessions.definitions.Clone(), s.configPath, s.logger, s.sessions
	rt := new(Runtime)
	workCtx, cancelWork := context.WithCancelCause(runCtx)

	defer cancelWork(errShutdown)

	shutdown := &runShutdown{logger: logger, cancelWork: cancelWork, restartRequested: make(chan struct{})}

	var (
		threadBridges *threadBridgeManager
		background    *backgroundRegistry
		stops         []func(context.Context) error
	)

	if err := rocketcodeSessions.sweepRetainedResults(); err != nil {
		logger.Warn("sweep expired retained execute results", "error", err)
	}

	if err := skel.SyncInWithOverlays(cfg.Workspace, cfg.RuntimeDirName(), cfg.Overlays, logger); err != nil {
		return fmt.Errorf("sync rocketclaw skeleton: %w", err)
	}

	if err := validateRuntimeAssets(rocketcodeSessions.definitions, cfg.RuntimeDirName()); err != nil {
		return err
	}

	if err := s.assemble.ValidateAssets(cfg, cfg.RuntimeDirName()); err != nil {
		return fmt.Errorf("validate frontend assets: %w", err)
	}

	var externalMCPUsers map[string]string

	if cfg.MCPExternal.Enabled {
		var err error

		externalMCPUsers, err = config.LoadExternalMCPUsers(configPath)
		if err != nil {
			return fmt.Errorf("load external MCP auth users: %w", err)
		}
	}

	logger.Info(
		"initializing rocketclaw runtime",
		"workspace", cfg.Workspace,
		"mcp_external_enabled", cfg.MCPExternal.Enabled,
	)

	questions := &webQuestions{store: rocketcodeSessions, waiting: map[webQuestionKey]chan protocol.AskUserQuestionAnswer{}}

	threadBridges = newThreadBridgeManager(rocketcodeSessions.definitions, rocketcodeSessions, logger, conversationFactory{rt: rt, restart: shutdown, secrets: &config.AWSFetcher{}})
	background = newBackgroundRegistry(rocketcodeSessions, threadBridges, logger.With("component", "background_jobs"))
	rocketcodeSessions.jobs = background
	rocketcodeSessions.historyWorkers = threadBridges

	var bridgeLoops errgroup.Group

	bridgeLoops.Go(func() error {
		threadBridges.Run(workCtx)
		return nil
	})

	defer func() {
		logger.Info("shutting down rocketclaw runtime")
		shutdown.start("runtime cleanup", false)

		_ = bridgeLoops.Wait()

		background.shutdown()
		s.cancel()

		cleanupCtx := context.Background()
		for _, sink := range stops {
			if err := sink(cleanupCtx); err != nil {
				logger.Warn("stop connector", "connector", "frontend", "error", err)
			}
		}
	}()

	*rt = Runtime{
		Cfg: rocketcodeSessions.definitions, Log: logger, RunCtx: runCtx,
		Sessions:          rocketcodeSessions,
		ExternalMCPUsers:  externalMCPUsers,
		ExternalMCPAgents: inertExternalMCPAgents{}, assemble: s.assemble,
		TextRouter: threadBridges, threads: threadBridges, background: background,
		questions: questions,
	}

	rt.installJustBash()

	resumeJobs, err := background.recoverJobs(runCtx)
	if err != nil {
		return err
	}

	copyDone, extraStops, err := s.assemble.Assemble(rt)
	if err != nil {
		return fmt.Errorf("assemble frontends: %w", err)
	}

	stops = extraStops

	if err := resumeJobs(runCtx); err != nil {
		return err
	}

	if err := threadBridges.StartActiveTurns(runCtx); err != nil {
		return err
	}

	if err := threadBridges.StartPendingScheduledMessages(runCtx); err != nil {
		return err
	}

	if err := threadBridges.StartActiveGoals(runCtx); err != nil {
		return err
	}

	if err := threadBridges.StartQueuedConversations(runCtx); err != nil {
		return err
	}

	pruneCtx, cancelPrune := context.WithCancel(workCtx)

	var prune errgroup.Group
	prune.Go(func() error {
		for {
			stats, err := rocketcodeSessions.PruneStateBefore(pruneCtx, time.Now().Add(-stateRetention), statePrunePace)
			if err != nil && pruneCtx.Err() == nil {
				logger.Warn("prune stale rocketclaw state", "error", err)
			}

			if stats.Threads+stats.ExternalMCPSessions > 0 || stats.SessionRows > 0 {
				logger.Info("pruned stale rocketclaw state", "threads", stats.Threads, "external_mcp_sessions", stats.ExternalMCPSessions, "session_rows", stats.SessionRows)
			}

			select {
			case <-pruneCtx.Done():
				return nil
			case <-time.After(statePruneInterval):
			}
		}
	})

	defer func() {
		cancelPrune()

		_ = prune.Wait()
	}()

	select {
	case <-s.signal:
		shutdown.start("shutdown signal", false)
	case <-workCtx.Done():
	case <-copyDone:
	}

	select {
	case <-shutdown.restartRequested:
		return ErrRestartRequested
	default:
	}

	return nil
}

type conversationFactory struct {
	rt      *Runtime
	restart runtimeRestarter
	secrets config.SecretFetcher
}

func (f conversationFactory) newBridge(cfg *Config) directBridge {
	cfg.RequestRestart = f.restart
	cfg.RequestReload = f.rt
	cfg.UserQuestionAsker = f.rt.questions
	cfg.SessionService = f.rt.Sessions
	cfg.Secrets = f.secrets

	bridge := NewConversation(f.rt.Cfg, f.rt, cfg, f.rt.Log)
	bridge.background = f.rt.background

	return bridge
}

func validateRuntimeAssets(cfg *config.LockedConfig, runtimeDir string) error {
	if _, _, err := LoadRuntimeDefinitions(cfg, runtimeDir); err != nil {
		return fmt.Errorf("validate rocketcode definitions: %w", err)
	}

	return validateWorkflowDefinitions(cfg.Clone(), runtimeDir)
}

func validateWorkflowDefinitions(cfg *config.Config, runtimeDir string) (err error) {
	root, err := os.OpenRoot(cfg.Workspace)
	if err != nil {
		return fmt.Errorf("open workflow root: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()

	if _, err = workflow.Load(root, runtimeDir); err != nil {
		return fmt.Errorf("validate workflow definitions: %w", err)
	}

	return nil
}
