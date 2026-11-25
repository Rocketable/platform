package backend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/metrics"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"golang.org/x/sync/errgroup"
)

var ErrRestartRequested = errors.New("restart requested")

var errShutdown = fmt.Errorf("%w: %w", protocol.ErrBridgeStopped, harness.ErrShutdown)

const (
	stateRetention     = 90 * 24 * time.Hour
	statePruneInterval = time.Hour
	statePrunePace     = time.Second
)

type FrontendAssembler interface {
	Gate(context.Context, *config.Config, *slog.Logger) (func() error, error)
	Assemble(*Runtime) (<-chan struct{}, []func(context.Context) error, error)
	ValidateAssets(*config.Config, string) error
}

type lockedRun struct {
	cancel    context.CancelFunc
	detach    func() bool
	signal    <-chan struct{}
	logger    *slog.Logger
	logLevel  *slog.LevelVar
	assemble  FrontendAssembler
	sandboxes sandboxPreparer
	cfg       *config.Config
	db        *sql.DB
}

type runShutdown struct {
	logger           *slog.Logger
	cancelWork       context.CancelCauseFunc
	restartRequested chan struct{}

	once sync.Once
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

func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger, logLevel *slog.LevelVar, assemble FrontendAssembler, sandboxes sandboxPreparer) error {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()

	detach := context.AfterFunc(ctx, cancel)

	db, err := openSessionDB(runCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("start rocketcode session service: %w", err)
	}

	defer func() {
		if err := db.Close(); err != nil {
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
	baseline := sampleHealth(samples, db.Stats())

	var health errgroup.Group
	health.Go(func() error {
		reportHealth(runCtx, db, logger, samples, &baseline)
		return nil
	})

	defer func() {
		cancel()

		_ = health.Wait()
	}()

	return holdRunLock(runCtx, db, &lockedRun{
		cancel:    cancel,
		detach:    detach,
		signal:    ctx.Done(),
		logger:    logger,
		logLevel:  logLevel,
		assemble:  assemble,
		sandboxes: sandboxes,
		cfg:       cfg,
		db:        db,
	})
}

func (s *lockedRun) Run(runCtx context.Context) error {
	closeGate, err := s.assemble.Gate(runCtx, s.cfg, s.logger)
	if err != nil {
		return fmt.Errorf("start the Web: %w", err)
	}

	defer func() {
		if err := closeGate(); err != nil {
			s.logger.Warn("close the gate's resources", "error", err)
		}
	}()

	stopInstrumentation, err := configureInstrumentation(runCtx, s.cfg.PostHog)
	if err != nil {
		return err
	}

	defer func() {
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()

		if err := stopInstrumentation(shutdownCtx); err != nil {
			s.logger.Warn("stop instrumentation", "error", err)
		}
	}()

	stateLogger := s.logger.With("component", "state_store")
	startedAt := time.Now()

	stateLogger.Info("starting rocketclaw state store", "workspace", s.cfg.Workspace, "runtime_dir", s.cfg.RuntimeDirName())

	sessions, err := newSessionService(runCtx, s.cfg, s.db, stateLogger)
	if err != nil {
		return fmt.Errorf("start rocketcode session service: %w", err)
	}

	stateLogger.Info("started rocketclaw state store", "elapsed", time.Since(startedAt))

	return s.serve(runCtx, sessions)
}

func (s *lockedRun) serve(runCtx context.Context, rocketcodeSessions *SessionService) error {
	s.detach()

	cfg, logger := rocketcodeSessions.definitions.Clone(), s.logger
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

	if err := rocketcodeSessions.resetSavingAgentDrafts(runCtx); err != nil {
		return err
	}

	slackWarningText, err := s.startWithBehavior(runCtx, cfg, rocketcodeSessions)
	if err != nil {
		return err
	}

	logger.Info(
		"initializing rocketclaw runtime",
		"workspace", cfg.Workspace,
		"mcp_external_enabled", cfg.MCPExternal.Enabled,
	)

	questions := &webQuestions{store: rocketcodeSessions, waiting: map[webQuestionKey]chan protocol.AskUserQuestionAnswer{}}

	threadBridges = newThreadBridgeManager(rocketcodeSessions.definitions, rocketcodeSessions, logger, conversationFactory{rt: rt, secrets: &config.AWSFetcher{}})
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
		Cfg: rocketcodeSessions.definitions, Log: logger, logLevel: s.logLevel, RunCtx: runCtx,
		Sessions:          rocketcodeSessions,
		ExternalMCPAgents: inertRefresher{}, CronJobs: inertRefresher{}, Assemble: s.assemble, sandboxes: s.sandboxes,
		TextRouter: threadBridges, threads: threadBridges, background: background,
		questions: questions, shutdown: shutdown, slackWarningText: slackWarningText,
	}

	rt.prepareSandbox()

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

	if err := rt.resumeScheduledSessions(runCtx); err != nil {
		return err
	}

	pruneCtx, cancelPrune := context.WithCancel(workCtx)

	var prune errgroup.Group
	prune.Go(func() error {
		for {
			cutoff := time.Now().Add(-stateRetention)

			stats, err := rocketcodeSessions.PruneStateBefore(pruneCtx, cutoff, statePrunePace)
			if err != nil && pruneCtx.Err() == nil {
				logger.Warn("prune stale rocketclaw state", "error", err)
			}

			if stats.Threads+stats.ExternalMCPSessions > 0 || stats.SessionRows > 0 {
				logger.Info("pruned stale rocketclaw state", "threads", stats.Threads, "external_mcp_sessions", stats.ExternalMCPSessions, "session_rows", stats.SessionRows)
			}

			removed, err := pruneRocketcodeTemp(pruneCtx, filepath.Join(cfg.Workspace, cfg.RuntimeDirName(), ".rocketcode"), cutoff)
			if err != nil && pruneCtx.Err() == nil {
				logger.Warn("prune stale rocketcode temp files", "error", err)
			}

			if removed > 0 {
				logger.Info("pruned stale rocketcode temp files", "entries", removed)
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

func pruneRocketcodeTemp(ctx context.Context, dir string, cutoff time.Time) (removed int, err error) {
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}

	if err != nil {
		return 0, fmt.Errorf("open rocketcode dir: %w", err)
	}

	defer func() { err = errors.Join(err, root.Close()) }()

	for _, sub := range []string{"tmp", "shell-outputs"} {
		entries, errRead := fs.ReadDir(root.FS(), sub)
		if !errors.Is(errRead, fs.ErrNotExist) {
			err = errors.Join(err, errRead)
		}

		for _, entry := range entries {
			if ctx.Err() != nil {
				return removed, errors.Join(err, ctx.Err())
			}

			info, errEntry := entry.Info()
			if errEntry == nil && info.ModTime().Before(cutoff) {
				if errEntry = root.RemoveAll(filepath.Join(sub, entry.Name())); errEntry == nil {
					removed++
				}
			}

			err = errors.Join(err, errEntry)
		}
	}

	return removed, err
}

type conversationFactory struct {
	rt      *Runtime
	secrets config.SecretFetcher
}

func (f conversationFactory) newBridge(cfg *Config) directBridge {
	cfg.RequestRestart = f.rt
	cfg.RequestReload = f.rt
	cfg.UserQuestionAsker = f.rt.questions
	cfg.SessionService = f.rt.Sessions
	cfg.Secrets = f.secrets

	bridge := NewConversation(f.rt.Cfg, f.rt, cfg, f.rt.Log)
	bridge.background = f.rt.background

	return bridge
}

func (s *lockedRun) startWithBehavior(ctx context.Context, cfg *config.Config, sessions *SessionService) (string, error) {
	stored, found, err := sessions.behaviorSettings(ctx)
	if err != nil {
		return "", err
	}

	if found {
		if ignored := setBehaviorFields(new(cfg.Behavior())); len(ignored) > 0 {
			s.logger.Warn("ignoring behavior settings in the config file; the stored behavior settings apply", "fields", ignored)
		}

		cfg.SetBehavior(&stored)
	}

	if err := cfg.ValidateBehavior(); err != nil {
		err = fmt.Errorf("validate behavior settings: %w", err)
		if found {
			err = fmt.Errorf("stored behavior settings: %w", err)
		}

		return "", err
	}

	sessions.definitions.Store(cfg)
	SetLogLevel(s.logLevel, cfg.Logging.Level)

	runtimeDir := cfg.RuntimeDirName()
	if err := skel.SyncInWithOverlays(cfg.Workspace, runtimeDir, cfg.Overlays, s.logger); err != nil {
		return "", fmt.Errorf("sync rocketclaw skeleton: %w", err)
	}

	warnings, err := validateRuntimeAssets(sessions.definitions, runtimeDir, s.logger)
	if err != nil {
		return "", err
	}

	if err := sessions.placePendingPages(ctx, runtimeDir); err != nil {
		return "", err
	}

	if err := s.assemble.ValidateAssets(cfg, runtimeDir); err != nil {
		return "", fmt.Errorf("validate frontend assets: %w", err)
	}

	if found {
		return warnings, nil
	}

	return warnings, putBehaviorSettings(ctx, sessions.db, new(cfg.Behavior()))
}

func setBehaviorFields(b *config.Behavior) []string {
	var names []string

	for _, field := range []struct {
		name string
		set  bool
	}{
		{"auto_approver_model", b.AutoApproverModel != ""},
		{"slack.channels", b.Slack.Channels != nil},
		{"slack.ambient_decision_model", b.Slack.AmbientDecisionModel != ""},
		{"logging.level", b.Logging.Level != ""},
		{"overlays", b.Overlays != nil},
		{"embedded_agents", b.EmbeddedAgents != config.EmbeddedAgentsConfig{}},
		{"title_model", b.TitleModel != ""},
		{"title_decision_model", b.TitleDecisionModel != ""},
	} {
		if field.set {
			names = append(names, field.name)
		}
	}

	return names
}

func SetLogLevel(level *slog.LevelVar, text string) {
	text = strings.TrimSpace(strings.ToUpper(text))
	if text == "WARNING" {
		text = "WARN"
	}

	if err := level.UnmarshalText([]byte(text)); err != nil {
		level.Set(slog.LevelDebug)
	}
}

func validateRuntimeAssets(cfg *config.LockedConfig, runtimeDir string, logger *slog.Logger) (string, error) {
	root, err := os.OpenRoot(cfg.Clone().Workspace)
	if err != nil {
		return "", fmt.Errorf("validate rocketcode definitions: open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	var (
		agents   harness.Agents
		warnings []string
	)

	err = cfg.Do(func(current *config.Config) error {
		agents, _, warnings, err = readRocketCodeDefinitionsIn(root, current, runtimeDir, nil, false)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("validate rocketcode definitions: %w", err)
	}

	for _, channel := range cfg.Clone().Slack.Channels {
		for _, name := range channel.Agents {
			if agents.Items[name].Sticky != harness.AgentStickySlack {
				logger.Warn("slack channel agent is not sticky: slack", "channel", channel.Channel, "agent", name)
				warnings = append(warnings, fmt.Sprintf("Slack channel %s uses agent %s, which is not sticky: slack, so it does not answer mentions there.", channel.Channel, name))
			}
		}
	}

	return strings.Join(warnings, " "), nil
}
