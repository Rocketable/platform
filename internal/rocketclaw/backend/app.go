package backend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/Rocketable/platform/internal/rocketcode"
)

// ErrRestartRequested indicates rocketclaw should exit so a supervisor can restart it.
var ErrRestartRequested = errors.New("restart requested")

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
}

type lockedRun struct {
	cancel     context.CancelFunc
	cfg        *config.Config
	configPath string
	logger     *slog.Logger
	assemble   FrontendAssembler
	sessions   *SessionService
}

// Run starts rocketclaw and blocks until the context is canceled or a fatal error occurs.
func Run(ctx context.Context, cfg *config.Config, configPath string, logger *slog.Logger, assemble FrontendAssembler) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

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

	rocketcodeSessions, err := NewSessionServiceIn(cfg.DatabaseURL, stateLogger)
	if err != nil {
		return fmt.Errorf("start rocketcode session service: %w", err)
	}

	stateLogger.Info("started rocketclaw state store", "elapsed", time.Since(startedAt))

	defer func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()

		if err := rocketcodeSessions.Stop(stopCtx); err != nil {
			logger.Warn("stop rocketcode session service", "error", err)
		}
	}()

	return holdRunLock(runCtx, rocketcodeSessions.db, &lockedRun{
		cancel:     cancel,
		cfg:        cfg,
		configPath: configPath,
		logger:     logger,
		assemble:   assemble,
		sessions:   rocketcodeSessions,
	})
}

func (s *lockedRun) Run(runCtx context.Context) error { //nolint:gocyclo // Same runtime wiring as Run, held under pglock.Do.
	cancel, cfg, configPath, logger, rocketcodeSessions := s.cancel, s.cfg, s.configPath, s.logger, s.sessions
	connectorChannels := protocol.NewChannels()

	var (
		shutdownOnce     sync.Once
		restartRequested = make(chan struct{})
		threadBridges    *threadBridgeManager
		cronjobs         *Manager
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

	if err := rocketcodeSessions.ApplyPendingRestartNotifications(runCtx); err != nil {
		return fmt.Errorf("apply pending restart notifications: %w", err)
	}

	if err := skel.SyncInWithOverlays(cfg.Workspace, cfg.RuntimeDirName(), cfg.Overlays, logger); err != nil {
		return fmt.Errorf("sync rocketclaw skeleton: %w", err)
	}

	channels := make([]string, 0, len(cfg.Slack.Channels))
	for _, channel := range cfg.Slack.Channels {
		if channel.Channel == "@" {
			continue
		}

		channels = append(channels, channel.Channel)
	}

	if err := validateRuntimeAssets(cfg, cfg.RuntimeDirName(), channels); err != nil {
		return err
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

			logger.Warn("shutdown requested; canceling rocketclaw runtime", "reason", reason, "restart", restart)
			cancel()
		})

		return started
	}

	requestRestart := func(_ context.Context, reason string) (string, error) { //nolint:unparam // Signature is shared with restart hooks that may fail.
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

	requestReload := func(_ context.Context, reason string) (string, error) {
		reloadMu.Lock()
		defer reloadMu.Unlock()

		logger.Info("reload requested", "reason", reason)

		if err := skel.ReplaceRuntimeAssetsAfterValidation(cfg.Workspace, cfg.RuntimeDirName(), cfg.Overlays, logger, func(runtimeDir string) error {
			return validateRuntimeAssets(cfg, runtimeDir, channels)
		}); err != nil {
			return "", fmt.Errorf("reload runtime assets: %w", err)
		}

		if err := refreshExternalMCPAgents(); err != nil {
			return "", err
		}

		return "rocketclaw runtime assets reloaded", nil
	}

	startNewThread := func(startCtx context.Context, req *protocol.StartNewThreadRequest) (protocol.StartNewThreadResult, error) {
		createRoot := startThreadRoot

		if req.Response != nil {
			rootCh := make(chan protocol.StartNewThreadRootResult, 1)

			errCh := make(chan error, 1)
			select {
			case req.Response <- protocol.Response{Payload: protocol.StartNewThreadResponse{Request: req, Root: rootCh, Err: errCh}}:
			case <-startCtx.Done():
				return protocol.StartNewThreadResult{}, startCtx.Err()
			}

			var root protocol.StartNewThreadRootResult
			select {
			case root = <-rootCh:
			case err := <-errCh:
				return protocol.StartNewThreadResult{}, err
			case <-startCtx.Done():
				return protocol.StartNewThreadResult{}, startCtx.Err()
			}

			createRoot = func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error) {
				return root, nil
			}
		}

		return threadBridges.StartNewThread(startCtx, req, createRoot)
	}

	cronjobs = New(cfg.Workspace, cfg.RuntimeDirName(), channels, connectorChannels.Broadcasts, rocketcodeSessions, func(jobCtx context.Context, agent, prompt string, log *slog.Logger, progress *RawRunProgress) (protocol.CronRunResult, error) {
		progress.SessionService = rocketcodeSessions
		progress.RequestRestart = requestRestart
		progress.RequestReload = requestReload
		progress.StartNewThread = startNewThread

		result, err := RunRawWithProgress(jobCtx, cfg, agent, prompt, log, progress)
		if err != nil {
			return protocol.CronRunResult{}, fmt.Errorf("run raw cronjob turn: %w", err)
		}

		return protocol.CronRunResult{Text: result.Text, VerbatimMessage: result.VerbatimMessage, Attachments: result.Attachments}, nil
	}, logger)

	logger.Info(
		"initializing rocketclaw runtime",
		"workspace", cfg.Workspace,
		"mcp_external_enabled", cfg.MCPExternal.Enabled,
	)

	recoveringConversations := map[string]bool{}

	var (
		recoveredTurns []ActiveTurnState
		cannotResume   []cannotResumeItem
	)

	if err := recoverStartupActiveTurns(runCtx, rocketcodeSessions, func(_ context.Context, turn *ActiveTurnState) error {
		conversationID := strings.TrimSpace(turn.Checkpoint.ConversationKey)
		recoveringConversations[conversationID] = true

		recoveredTurns = append(recoveredTurns, *turn)
		logger.Info("startup active turn selected for recovery", "conversation_id", turn.Checkpoint.ConversationKey, "turn_id", turn.Checkpoint.TurnID, "agent", turn.Checkpoint.Agent)

		return nil
	}, func(conversationID string, steers []protocol.PendingSteer) {
		cannotResume = append(cannotResume, cannotResumeItem{conversationID: conversationID, steers: steers})
	}, logger); err != nil {
		return err
	}

	for i := range recoveredTurns {
		if err := rocketcodeSessions.ReserveExternalMCPRecovery(recoveredTurns[i].Checkpoint.ConversationKey); err != nil {
			return fmt.Errorf("reserve paired startup recovery: %w", err)
		}
	}

	// Starts as No; set to Slack after the connector exists. Factory reads the current value per bridge.
	slackUserQuestionAsker := protocol.NoUserQuestionAsker()
	drainSlack := func(context.Context, string, rocketcode.TurnPhase) []string { return nil }

	threadBridges = newThreadBridgeManager(cfg, rocketcodeSessions, logger, func(Config Config) directBridge {
		Config.RequestRestart = requestRestart
		Config.RequestReload = requestReload
		// ensureStartedThread defaults to NoUserQuestionAsker; Slack-origin overrides with current slack asker.
		if Config.ExternalConversationID == "" && !Config.RecoveringActiveTurn {
			Config.UserQuestionAsker = slackUserQuestionAsker
		}

		conversationID := Config.ConversationID
		Config.SteerDrain = rocketcode.SteerDrain{Fn: func(ctx context.Context, phase rocketcode.TurnPhase) []string {
			return drainSlack(ctx, conversationID, phase)
		}}
		Config.EnqueueActivation = EnqueueActivation{Fn: func(ctx context.Context, item *protocol.ThreadQueueItem, inbound *protocol.InboundMessage) error {
			if slackSink == nil {
				return nil
			}

			return slackSink.ActivateEnqueue(ctx, item, inbound)
		}}
		Config.StartNewThread = startNewThread
		Config.SessionService = rocketcodeSessions

		return NewConversation(cfg, protocol.BroadcastPublisher(connectorChannels.Broadcasts), &Config, logger)
	})
	if err := threadBridges.StartPendingScheduledMessages(recoveringConversations); err != nil {
		return err
	}

	if err := threadBridges.StartActiveGoals(recoveringConversations); err != nil {
		return err
	}

	defer func() {
		logger.Info("shutting down rocketclaw runtime")
		startShutdown("runtime cleanup", false)

		cleanupCtx := context.Background()
		for _, sink := range stops {
			if err := sink.stop(cleanupCtx); err != nil {
				logger.Warn("stop connector", "connector", sink.name, "error", err)
			}
		}

		if err := threadBridges.Stop(); err != nil {
			logger.Warn("stop thread bridges", "error", err)
		}
	}()

	rt := &Runtime{
		Cfg: cfg, ConfigPath: configPath, Log: logger, RunCtx: runCtx, Channels: connectorChannels,
		Sessions: rocketcodeSessions, Cron: cronjobs, OverlayMu: &reloadMu, Reload: requestReload, Restart: requestRestart,
		RecoveredTurns: recoveredTurns, CannotResume: cannotResume, ExternalMCPUsers: externalMCPUsers,
		RefreshExternalMCPAgents: &refreshExternalMCPAgents, TextRouter: threadBridges, threads: threadBridges,
		startThreadRoot: &startThreadRoot, slackAsker: &slackUserQuestionAsker, drainSlack: &drainSlack,
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
		rt.AttachSlack(slack)
		slack.SetPendingSteersSink(protocol.PendingSteersSink{Set: rocketcodeSessions.SetPendingSteers})
	}

	if slackSink != nil {
		if err := applyStartupSteerRecovery(runCtx, slackSink, threadBridges.PickLaterWork, recoveredTurns, cannotResume); err != nil {
			return err
		}

		for i := range recoveredTurns {
			turn := &recoveredTurns[i]

			conversationID := strings.TrimSpace(turn.Checkpoint.ConversationKey)

			err = threadBridges.RecoverActiveTurn(runCtx, turn)
			if err != nil {
				if activeTurnRecoveryPreserveError(err) {
					return err
				}

				if errRelease := rocketcodeSessions.ReleaseExternalMCPRecovery(conversationID); errRelease != nil {
					return fmt.Errorf("release failed paired startup recovery: %w", errRelease)
				}

				reason := fmt.Sprintf("enqueue startup active turn recovery: %v", err)

				if errClear := cannotResumeActiveTurn(runCtx, rocketcodeSessions, turn, func(string, []protocol.PendingSteer) {}); errClear != nil {
					return fmt.Errorf("delete failed startup active turn enqueue: %w", errClear)
				}

				if errPick := applyStartupSteerRecovery(runCtx, slackSink, threadBridges.PickLaterWork, nil, []cannotResumeItem{{conversationID: conversationID, steers: turn.PendingSteers}}); errPick != nil {
					logger.Error("pick later work after failed startup active turn enqueue", "conversation_id", conversationID, "error", errPick)
				}

				logger.Warn("deleted failed startup active turn after enqueue error", "conversation_id", conversationID, "turn_id", turn.Checkpoint.TurnID, "error", err, "reason", reason)

				continue
			}

			logger.Info("startup active turn recovery enqueued", "conversation_id", conversationID, "turn_id", turn.Checkpoint.TurnID)
		}
	}

	if err := cronjobs.Start(runCtx); err != nil {
		return fmt.Errorf("start cronjobs: %w", err)
	}

	go func() {
		<-runCtx.Done()
		startShutdown("runtime context canceled", false)
	}()

	if copyDone != nil {
		<-copyDone
	}

	select {
	case <-restartRequested:
		return ErrRestartRequested
	default:
	}

	return nil
}

func validateRuntimeAssets(cfg *config.Config, runtimeDir string, channels []string) error {
	if _, _, err := LoadRuntimeDefinitions(cfg, runtimeDir); err != nil {
		return fmt.Errorf("validate rocketcode definitions: %w", err)
	}

	if err := ValidateRuntimeDefinitions(cfg.Workspace, runtimeDir, channels); err != nil {
		return fmt.Errorf("validate cron definitions: %w", err)
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
