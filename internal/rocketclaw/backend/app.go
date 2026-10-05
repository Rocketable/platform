package backend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/Rocketable/platform/internal/rocketcode"
	"golang.org/x/sync/errgroup"
)

// ErrRestartRequested indicates rocketclaw should exit so a supervisor can restart it.
var ErrRestartRequested = errors.New("restart requested")

// errShutdown stops conversation work: bridges leave it for the next start, and
// RocketCode turns stop once their running calls finish.
var errShutdown = fmt.Errorf("%w: %w", protocol.ErrBridgeStopped, rocketcode.ErrShutdown)

type namedStopper struct {
	name string
	stop func(context.Context) error
}

const (
	stateRetention = 30 * 24 * time.Hour
)

// FrontendAssembler constructs Slack and MCP frontends for a running backend.
type FrontendAssembler interface {
	Assemble(*Runtime) (SlackFrontend, <-chan struct{}, []func(context.Context) error, error)
	ValidateAssets(*config.Config, string, []string) error
}

type lockedRun struct {
	cancel     context.CancelFunc
	detach     func() bool
	signal     <-chan struct{}
	cfg        *config.Config
	configPath string
	logger     *slog.Logger
	assemble   FrontendAssembler
	sessions   *SessionService
}

// Run starts rocketclaw and blocks until the context is canceled or a fatal error occurs.
func Run(ctx context.Context, cfg *config.Config, configPath string, logger *slog.Logger, assemble FrontendAssembler) error {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()

	detach := context.AfterFunc(ctx, cancel)

	stopInstrumentation, err := configureInstrumentation(runCtx, cfg.Instrumentation)
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

	return holdRunLock(runCtx, rocketcodeSessions.db, &lockedRun{
		cancel:     cancel,
		detach:     detach,
		signal:     ctx.Done(),
		cfg:        cfg,
		configPath: configPath,
		logger:     logger,
		assemble:   assemble,
		sessions:   rocketcodeSessions,
	})
}

func (s *lockedRun) Run(runCtx context.Context) error { //nolint:gocyclo // Same runtime wiring as Run, held under pglock.Do.
	s.detach()

	cfg, configPath, logger, rocketcodeSessions := s.cfg, s.configPath, s.logger, s.sessions
	rt := new(Runtime)
	workCtx, cancelWork := context.WithCancelCause(runCtx)

	defer cancelWork(errShutdown)

	var (
		shutdownOnce     sync.Once
		restartRequested = make(chan struct{})
		threadBridges    *threadBridgeManager
		slackSink        SlackFrontend
		stops            []namedStopper
		startThreadRoot  func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error)
	)

	startThreadRoot = func(_ context.Context, req *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error) {
		return protocol.StartNewThreadRootResult{}, fmt.Errorf("text root is not available for %s turns", req.Source)
	}

	if stats, err := rocketcodeSessions.PruneStateBefore(runCtx, time.Now().Add(-stateRetention)); err != nil {
		logger.Warn("prune stale rocketclaw state", "error", err)
	} else if stats.Threads+stats.ExternalMCPSessions > 0 || stats.SessionRows > 0 {
		logger.Info("pruned stale rocketclaw state", "threads", stats.Threads, "external_mcp_sessions", stats.ExternalMCPSessions, "session_rows", stats.SessionRows)
	}

	backfillCtx, cancelBackfill := context.WithCancel(runCtx)

	var backfill errgroup.Group
	backfill.Go(func() error {
		if err := rocketcodeSessions.backfillSessionSummaries(backfillCtx); err != nil && backfillCtx.Err() == nil {
			logger.Error("backfill session summaries", "error", err)
		}

		return nil
	})

	defer func() {
		cancelBackfill()

		_ = backfill.Wait()
	}()

	if err := skel.SyncInWithOverlays(cfg.Workspace, cfg.RuntimeDirName(), cfg.Overlays, logger); err != nil {
		return fmt.Errorf("sync rocketclaw skeleton: %w", err)
	}

	channels := cfg.Slack.MappedChannels()

	if err := validateRuntimeAssets(cfg, cfg.RuntimeDirName()); err != nil {
		return err
	}

	if err := s.assemble.ValidateAssets(cfg, cfg.RuntimeDirName(), channels); err != nil {
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

	startShutdown := func(reason string, restart bool) bool {
		started := false

		shutdownOnce.Do(func() {
			started = true

			if restart {
				close(restartRequested)
			}

			logger.Warn("shutdown requested; canceling rocketclaw work", "reason", reason, "restart", restart)
			cancelWork(errShutdown)
		})

		return started
	}

	requestRestart := func(reason string) (string, error) { //nolint:unparam // Config.RequestRestart requires error; restart cancellation never fails.
		started := startShutdown(reason, true)

		if !started {
			logger.Warn("restart requested while shutdown already in progress", "reason", reason)
		}

		return "restart requested; runtime cancellation started", nil
	}

	var (
		reloadMu sync.Mutex

		refreshExternalMCPAgents = func() error { return nil }
	)

	requestReload := func(reason string) (string, error) {
		reloadMu.Lock()
		defer reloadMu.Unlock()

		logger.Info("reload requested", "reason", reason)

		if err := skel.ReplaceRuntimeAssetsAfterValidation(cfg.Workspace, cfg.RuntimeDirName(), cfg.Overlays, logger, func(runtimeDir string) error {
			if err := validateRuntimeAssets(cfg, runtimeDir); err != nil {
				return err
			}

			return s.assemble.ValidateAssets(cfg, runtimeDir, channels)
		}); err != nil {
			return "", fmt.Errorf("reload runtime assets: %w", err)
		}

		if err := refreshExternalMCPAgents(); err != nil {
			return "", err
		}

		return "rocketclaw runtime assets reloaded", nil
	}

	startNewThread := func(startCtx context.Context, req *protocol.StartNewThreadRequest) (protocol.StartNewThreadResult, error) {
		return threadBridges.StartNewThread(startCtx, req, startThreadRoot)
	}

	logger.Info(
		"initializing rocketclaw runtime",
		"workspace", cfg.Workspace,
		"mcp_external_enabled", cfg.MCPExternal.Enabled,
	)

	// Starts as No; set to Slack after the connector exists. Factory reads the current value per bridge.
	slackUserQuestionAsker := protocol.NoUserQuestionAsker()
	drainSlack := func(context.Context, string) []string { return nil }

	threadBridges = newThreadBridgeManager(cfg, rocketcodeSessions, logger, func(Config Config) directBridge {
		Config.RequestRestart = requestRestart
		Config.RequestReload = requestReload
		// ensureStartedThread defaults to NoUserQuestionAsker; Slack-origin overrides with current slack asker.
		if Config.ExternalConversationID == "" {
			Config.UserQuestionAsker = slackUserQuestionAsker
		}

		conversationID := Config.ConversationID
		Config.SteerDrain = rocketcode.SteerDrain{Fn: func(ctx context.Context, _ rocketcode.TurnPhase) []rocketcode.PromptInput {
			texts := drainSlack(ctx, conversationID)

			inputs := make([]rocketcode.PromptInput, 0, len(texts))
			for _, text := range texts {
				inputs = append(inputs, rocketcode.PromptInput{Text: text, DirectSkill: parseDirectSkillTrigger(text)})
			}

			return inputs
		}}
		Config.EnqueueActivation = EnqueueActivation{Fn: func(ctx context.Context, item *protocol.ThreadQueueItem, inbound *protocol.InboundMessage) error {
			if slackSink == nil {
				return nil
			}

			return slackSink.ActivateEnqueue(ctx, item, inbound)
		}}
		Config.StartNewThread = startNewThread
		Config.SessionService = rocketcodeSessions

		bridge := NewConversation(cfg, rt, &Config, logger)
		bridge.threads = threadBridges

		return bridge
	})

	var bridgeLoops errgroup.Group

	bridgeLoops.Go(func() error { return threadBridges.Run(workCtx) })

	defer func() {
		logger.Info("shutting down rocketclaw runtime")
		startShutdown("runtime cleanup", false)

		if err := bridgeLoops.Wait(); err != nil {
			logger.Warn("stop thread bridges", "error", err)
		}

		s.cancel()

		cleanupCtx := context.Background()
		for _, sink := range stops {
			if err := sink.stop(cleanupCtx); err != nil {
				logger.Warn("stop connector", "connector", sink.name, "error", err)
			}
		}
	}()

	*rt = Runtime{
		Cfg: cfg, Log: logger, RunCtx: runCtx,
		Sessions:                 rocketcodeSessions,
		ExternalMCPUsers:         externalMCPUsers,
		RefreshExternalMCPAgents: &refreshExternalMCPAgents, TextRouter: threadBridges, threads: threadBridges,
		startThreadRoot: &startThreadRoot, slackAsker: &slackUserQuestionAsker,
	}

	slack, copyDone, extraStops, err := s.assemble.Assemble(rt)
	if err != nil {
		return fmt.Errorf("assemble frontends: %w", err)
	}

	for _, stop := range extraStops {
		stops = append(stops, namedStopper{name: "frontend", stop: stop})
	}

	if slack != nil {
		slackSink = slack
		drainSlack = slack.DrainSteers
		rt.AttachSlack(slack)

		if err := slack.Start(runCtx); err != nil {
			return fmt.Errorf("start Slack connector: %w", err)
		}
	}

	if err := threadBridges.StartActiveTurns(runCtx); err != nil {
		return err
	}

	if err := threadBridges.StartPendingScheduledMessages(); err != nil {
		return err
	}

	if err := threadBridges.StartActiveGoals(); err != nil {
		return err
	}

	if err := threadBridges.StartQueuedConversations(); err != nil {
		return err
	}

	select {
	case <-s.signal:
		startShutdown("shutdown signal", false)
	case <-workCtx.Done():
	case <-copyDone:
	}

	select {
	case <-restartRequested:
		return ErrRestartRequested
	default:
	}

	return nil
}

func validateRuntimeAssets(cfg *config.Config, runtimeDir string) error {
	if _, _, err := LoadRuntimeDefinitions(cfg, runtimeDir); err != nil {
		return fmt.Errorf("validate rocketcode definitions: %w", err)
	}

	return validateWorkflowDefinitions(cfg, runtimeDir)
}

func validateWorkflowDefinitions(cfg *config.Config, runtimeDir string) (err error) {
	root, err := os.OpenRoot(cfg.Workspace)
	if err != nil {
		return fmt.Errorf("open workflow root: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()

	definitions, err := workflow.Load(root, runtimeDir)
	if err != nil {
		return fmt.Errorf("validate workflow definitions: %w", err)
	}

	for _, name := range slices.Sorted(maps.Keys(definitions)) {
		for _, model := range definitions[name].WorkerModels {
			if _, ok := cfg.Models[model]; !ok {
				return fmt.Errorf("validate workflow definitions: workflow worker model %q is not configured", model)
			}
		}
	}

	return nil
}
