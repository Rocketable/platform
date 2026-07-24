// Package app wires the rocketclaw runtime together.
package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"mime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/cronjob"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/externalmcp"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/Rocketable/platform/internal/rocketclaw/slackconnector"
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

// Run starts rocketclaw and blocks until the context is canceled or a fatal error occurs.
func Run(ctx context.Context, cfg *config.Config, configPath string, logger *slog.Logger) error {
	return run(ctx, cfg, configPath, logger)
}

//nolint:gocyclo // Runtime wiring is kept in one place so startup order remains explicit.
func run(ctx context.Context, cfg *config.Config, configPath string, logger *slog.Logger) error {
	runCtx, cancel := context.WithCancel(context.Background())
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

	bus := events.New()
	defer bus.Close()

	var (
		shutdownOnce     sync.Once
		restartRequested = make(chan struct{})
		threadBridges    *threadBridgeManager
		cronjobs         *cronjob.Manager
		slackSink        *slackconnector.Connector
		stops            []namedStopper
		startThreadRoot  func(context.Context, *events.StartNewThreadRequest) (events.StartNewThreadRootResult, error)
	)

	startThreadRoot = func(_ context.Context, req *events.StartNewThreadRequest) (events.StartNewThreadRootResult, error) {
		return events.StartNewThreadRootResult{}, fmt.Errorf("text root is not available for %s turns", req.Source)
	}

	stateLogger := logger.With("component", "state_store")

	startedAt := time.Now()

	stateLogger.Info("acquiring rocketclaw state store lock", "workspace", cfg.Workspace, "runtime_dir", cfg.RuntimeDirName())

	stateStoreLock, err := harnessbridge.AcquireStateStoreLock(cfg.Workspace, cfg.RuntimeDirName())
	if err != nil {
		if errors.Is(err, harnessbridge.ErrStateStoreLocked) {
			return fmt.Errorf("rocketclaw daemon already owns state store: %w", err)
		}

		return fmt.Errorf("lock rocketcode session db: %w", err)
	}

	stateLogger.Info("acquired rocketclaw state store lock", "elapsed", time.Since(startedAt))

	defer func() {
		if err := stateStoreLock.Close(); err != nil {
			logger.Warn("release rocketcode session db lock", "error", err)
		}
	}()

	startedAt = time.Now()

	stateLogger.Info("starting rocketclaw state store", "workspace", cfg.Workspace, "runtime_dir", cfg.RuntimeDirName())

	rocketcodeSessions, err := harnessbridge.NewSessionServiceIn(cfg.Workspace, cfg.RuntimeDirName(), stateLogger)
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

	if stats, err := rocketcodeSessions.PruneStateBefore(runCtx, time.Now().Add(-stateRetention)); err != nil {
		logger.Warn("prune stale rocketclaw state", "error", err)
	} else if stats.Threads+stats.ExternalMCPSessions > 0 || stats.SessionRows > 0 {
		logger.Info("pruned stale rocketclaw state", "threads", stats.Threads, "external_mcp_sessions", stats.ExternalMCPSessions, "session_rows", stats.SessionRows)
	}

	if stats, err := rocketcodeSessions.CheckpointWAL(runCtx); err != nil {
		logger.Warn("checkpoint rocketclaw state WAL", "error", err)
	} else if stats.Busy > 0 {
		logger.Warn("checkpoint rocketclaw state WAL busy", "busy", stats.Busy, "log_frames", stats.LogFrames, "checkpointed_frames", stats.CheckpointedFrames)
	} else {
		logger.Info("checkpointed rocketclaw state WAL", "busy", stats.Busy, "log_frames", stats.LogFrames, "checkpointed_frames", stats.CheckpointedFrames)
	}

	vacuumCtx, stopVacuum := context.WithCancel(context.Background())
	vacuumDone := make(chan struct{})

	go func() {
		defer close(vacuumDone)

		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()

		for {
			stats, err := rocketcodeSessions.Vacuum(vacuumCtx)
			if err != nil {
				if vacuumCtx.Err() != nil {
					return
				}

				logger.Warn("incremental vacuum rocketclaw state", "error", err)
			} else if stats.BeforePageCount != stats.AfterPageCount || stats.BeforeFreePages != stats.AfterFreePages {
				logger.Info("incremental vacuumed rocketclaw state", "before_pages", stats.BeforePageCount, "before_free_pages", stats.BeforeFreePages, "after_pages", stats.AfterPageCount, "after_free_pages", stats.AfterFreePages)
			}

			select {
			case <-vacuumCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	defer func() {
		stopVacuum()
		<-vacuumDone
	}()

	if err := rocketcodeSessions.ApplyPendingRestartNotifications(runCtx); err != nil {
		return fmt.Errorf("apply pending restart notifications: %w", err)
	}

	if err := skel.SyncInWithOverlays(cfg.Workspace, cfg.RuntimeDirName(), cfg.Overlays, logger); err != nil {
		return fmt.Errorf("sync rocketclaw skeleton: %w", err)
	}

	if _, _, err := harnessbridge.LoadRuntimeDefinitions(cfg, cfg.RuntimeDirName()); err != nil {
		return fmt.Errorf("validate rocketcode definitions: %w", err)
	}

	channels := make([]string, 0, len(cfg.Slack.Channels))
	for _, channel := range cfg.Slack.Channels {
		channels = append(channels, channel.Channel)
	}

	if err := cronjob.ValidateRuntimeDefinitions(cfg.Workspace, cfg.RuntimeDirName(), channels); err != nil {
		return fmt.Errorf("validate cron definitions: %w", err)
	}

	questionBroker := newAskUserQuestionBroker(logger)

	var externalMCPUsers map[string]string
	if cfg.MCPExternal.Enabled {
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

		validate := func(runtimeDir string) error {
			if _, _, err := harnessbridge.LoadRuntimeDefinitions(cfg, runtimeDir); err != nil {
				return fmt.Errorf("validate rocketcode definitions: %w", err)
			}

			if err := cronjob.ValidateRuntimeDefinitions(cfg.Workspace, runtimeDir, channels); err != nil {
				return fmt.Errorf("validate cron definitions: %w", err)
			}

			return nil
		}

		if err := skel.ReplaceRuntimeAssetsAfterValidation(cfg.Workspace, cfg.RuntimeDirName(), cfg.Overlays, logger, validate); err != nil {
			return "", fmt.Errorf("reload runtime assets: %w", err)
		}

		if err := refreshExternalMCPAgents(); err != nil {
			return "", err
		}

		return "rocketclaw runtime assets reloaded", nil
	}

	startNewThread := func(startCtx context.Context, req *events.StartNewThreadRequest) (events.StartNewThreadResult, error) {
		return threadBridges.StartNewThread(startCtx, req, startThreadRoot)
	}

	cronjobs = cronjob.New(cfg.Workspace, cfg.RuntimeDirName(), channels, bus, rocketcodeSessions, func(jobCtx context.Context, agent, prompt string, log *slog.Logger, progress *harnessbridge.RawRunProgress) (cronjob.RunResult, error) {
		progress.SessionService = rocketcodeSessions
		progress.RequestRestart = requestRestart
		progress.RequestReload = requestReload

		result, err := harnessbridge.RunRawWithProgress(jobCtx, cfg, agent, prompt, log, progress)
		if err != nil {
			return cronjob.RunResult{}, fmt.Errorf("run raw cronjob turn: %w", err)
		}

		return cronjob.RunResult{Text: result.Text, VerbatimMessage: result.VerbatimMessage, Attachments: result.Attachments}, nil
	}, logger)

	logger.Info(
		"initializing rocketclaw runtime",
		"workspace", cfg.Workspace,
		"mcp_external_enabled", cfg.MCPExternal.Enabled,
	)

	recoveringConversations := map[string]bool{}

	var recoveredTurns []harnessbridge.ActiveTurnState

	if err := recoverStartupActiveTurns(runCtx, rocketcodeSessions, func(_ context.Context, turn *harnessbridge.ActiveTurnState) error {
		conversationID := strings.TrimSpace(turn.Checkpoint.ConversationKey)
		recoveringConversations[conversationID] = true

		recoveredTurns = append(recoveredTurns, *turn)
		logger.Info("startup active turn selected for recovery", "conversation_id", turn.Checkpoint.ConversationKey, "turn_id", turn.Checkpoint.TurnID, "agent", turn.Checkpoint.Agent)

		return nil
	}, logger); err != nil {
		return err
	}

	for i := range recoveredTurns {
		if err := rocketcodeSessions.ReserveExternalMCPRecovery(recoveredTurns[i].Checkpoint.ConversationKey); err != nil {
			return fmt.Errorf("reserve paired startup recovery: %w", err)
		}
	}

	threadBridges = newThreadBridgeManager(cfg, rocketcodeSessions, logger, func(bridgeConfig harnessbridge.Config) directBridge {
		bridgeConfig.RequestRestart = requestRestart
		bridgeConfig.RequestReload = requestReload
		bridgeConfig.AskUserQuestion = questionBroker.ask
		bridgeConfig.StartNewThread = startNewThread
		bridgeConfig.SessionService = rocketcodeSessions

		return harnessbridge.NewConversation(cfg, bus, &bridgeConfig, logger)
	})
	if err := threadBridges.StartPendingScheduledMessages(recoveringConversations); err != nil {
		return err
	}

	if err := threadBridges.StartActiveGoals(recoveringConversations); err != nil {
		return err
	}

	for i := range recoveredTurns {
		turn := &recoveredTurns[i]

		conversationID := strings.TrimSpace(turn.Checkpoint.ConversationKey)

		err = threadBridges.RecoverActiveTurn(runCtx, turn)
		if err != nil {
			if isStartupRecoveryShutdownError(err) {
				return err
			}

			if errRelease := rocketcodeSessions.ReleaseExternalMCPRecovery(conversationID); errRelease != nil {
				return fmt.Errorf("release failed paired startup recovery: %w", errRelease)
			}

			reason := fmt.Sprintf("enqueue startup active turn recovery: %v", err)

			if errClear := rocketcodeSessions.ClearActiveTurn(runCtx, turn.Checkpoint.TurnID); errClear != nil {
				return fmt.Errorf("delete failed startup active turn enqueue: %w", errClear)
			}

			logger.Warn("deleted failed startup active turn after enqueue error", "conversation_id", conversationID, "turn_id", turn.Checkpoint.TurnID, "error", err, "reason", reason)

			continue
		}

		logger.Info("startup active turn recovery enqueued", "conversation_id", conversationID, "turn_id", turn.Checkpoint.TurnID)
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
	}()

	logger.Info("starting Slack connector")

	slackSink = slackconnector.New(&cfg.Slack, bus, threadBridges, cronjobs, questionBroker.answer, logger)
	questionBroker.post, questionBroker.delete = slackSink.AskUserQuestion, slackSink.DeleteUserQuestion
	startThreadRoot = slackSink.StartNewThreadRoot

	cronjobs.SendTextChannel = slackSink.SendCronjobChannelThread
	if err := slackSink.Start(runCtx); err != nil {
		return fmt.Errorf("start Slack connector: %w", err)
	}

	stops = append(stops, namedStopper{name: "slack", stop: slackSink.Stop})

	textRelay := func(relayCtx context.Context, relay *events.ExternalMCPRelay, reply *events.InboundMessage, channelName string) (*events.InboundMessage, error) {
		channelID, threadTS := channelName, ""
		if reply != nil && reply.SlackReply != nil {
			channelID, threadTS = reply.SlackReply.ChannelID, reply.SlackReply.ThreadTS
		}

		target, err := slackSink.SendExternalMCPRelay(relayCtx, channelID, threadTS, relay)
		if err != nil {
			return nil, fmt.Errorf("send Slack external MCP relay: %w", err)
		}

		return &events.InboundMessage{SlackReply: target}, nil
	}
	cleanupTextRelay := func(cleanupCtx context.Context, reply *events.InboundMessage) {
		if reply != nil {
			slackSink.CleanupExternalMCPRelay(cleanupCtx, reply.SlackReply)
		}
	}

	if err := cronjobs.Start(runCtx); err != nil {
		return fmt.Errorf("start cronjobs: %w", err)
	}

	if cfg.MCPExternal.Enabled {
		var (
			externalMCPAgentsMu sync.Mutex
			externalMCPAgents   = []string{}
		)

		refreshExternalMCPAgents = func() error {
			agents, err := harnessbridge.ExternalMCPAgentsIn(cfg, cfg.RuntimeDirName())
			if err != nil {
				return fmt.Errorf("load external MCP agents: %w", err)
			}

			externalMCPAgentsMu.Lock()
			externalMCPAgents = agents
			externalMCPAgentsMu.Unlock()

			return nil
		}

		if err := refreshExternalMCPAgents(); err != nil {
			return err
		}

		externalMCPAgentExposed := func(agent string) bool {
			externalMCPAgentsMu.Lock()
			defer externalMCPAgentsMu.Unlock()

			return slices.Contains(externalMCPAgents, agent)
		}

		externalMCP, err := startExternalMCPServer(runCtx, cfg, textRelay, cleanupTextRelay, slackSink.ResolveChannelName, externalMCPUsers, externalMCPAgentExposed, rocketcodeSessions, threadBridges.SubmitExternalMCP, logger)
		if err != nil {
			return err
		}

		stops = append(stops, namedStopper{name: "external_mcp", stop: externalMCP.Close})
	}

	logger.Info("outbound routing loop started")

	go func() {
		select {
		case <-ctx.Done():
			startShutdown("runtime context canceled", false)
		case <-runCtx.Done():
		}
	}()

	err = outboundLoop(runCtx, bus, slackSink.SendResponse, slackSink.AbortResponse, logger)

	select {
	case <-restartRequested:
		return ErrRestartRequested
	default:
	}

	return err
}

func startExternalMCPServer(
	ctx context.Context,
	cfg *config.Config,
	textRelay func(context.Context, *events.ExternalMCPRelay, *events.InboundMessage, string) (*events.InboundMessage, error),
	cleanupTextRelay func(context.Context, *events.InboundMessage),
	resolveSlackChannel func(context.Context, string) (string, error),
	users map[string]string,
	agentExposed func(string) bool,
	store *harnessbridge.SessionService,
	submitAgent func(context.Context, string, string, *events.InboundMessage, harnessbridge.ActivationHook) error,
	logger *slog.Logger,
) (*externalmcp.Server, error) {
	locks := newKeyedConversationLocks()

	server, err := externalmcp.StartSessionPromptServer(ctx, logger, cfg.MCPExternal.ListenAddr, users, func(callCtx context.Context, username, externalConversationID, requestedAgent, input string, metadata map[string]string, attachments []externalmcp.SessionPromptAttachment, slackChannel string) (result externalmcp.SessionResult, err error) {
		var (
			reply                 *events.InboundMessage
			createdConversationID string
			durableRegistration   bool
			promptAccepted        bool
		)

		defer func() {
			if err != nil && createdConversationID != "" {
				cleanupFailedExternalMCPConversation(cleanupTextRelay, store, logger, reply, externalConversationID, createdConversationID, durableRegistration, promptAccepted)
			}
		}()

		externalConversationID = strings.TrimSpace(externalConversationID)
		requestedAgent = strings.TrimSpace(requestedAgent)
		slackChannel = strings.TrimSpace(slackChannel)

		if externalConversationID == "" {
			return externalmcp.SessionResult{}, errors.New("external MCP conversation ID is required")
		}

		if requestedAgent == "" {
			return externalmcp.SessionResult{}, errors.New("external MCP agent is required")
		}

		channelIndex := slices.IndexFunc(cfg.Slack.Channels, func(channel config.SlackChannelConfig) bool { return channel.Channel == slackChannel })
		if channelIndex < 0 {
			return externalmcp.SessionResult{}, fmt.Errorf("slack channel %q is not configured", slackChannel)
		}

		managedAgent := cfg.Slack.Channels[channelIndex].Agents[0]

		inboundContent, outboundAttachments, err := externalMCPInboundContent(attachments)
		if err != nil {
			return externalmcp.SessionResult{}, err
		}

		inboundContent.Text = input
		if strings.TrimSpace(input) == "" && len(attachments) == 0 {
			return externalmcp.SessionResult{}, errors.New("external MCP turn requires input or attachments")
		}

		unlockExternalConversation := locks.lock(externalConversationID)
		defer unlockExternalConversation()

		session, ok, err := store.ExternalMCPSession(externalConversationID)
		if err != nil {
			return externalmcp.SessionResult{}, fmt.Errorf("load external MCP session state: %w", err)
		}

		if ok {
			session.Agent = strings.TrimSpace(session.Agent)
			session.PrivateConversationID = strings.TrimSpace(session.PrivateConversationID)
			session.ManagedConversationID = strings.TrimSpace(session.ManagedConversationID)

			usedAgent := session.Agent
			if requestedAgent != usedAgent {
				logger.Warn(
					"external MCP requested agent mismatched persisted session agent; using persisted agent",
					"external_conversation_id", externalConversationID,
					"requested_agent", requestedAgent,
					"used_agent", usedAgent,
				)
			}

			if usedAgent == "" || session.ManagedConversationID == "" {
				return externalmcp.SessionResult{}, fmt.Errorf("external_conversation_id %q has incomplete persisted state", externalConversationID)
			}

			channelID, threadTS, ok := harnessbridge.SlackThreadTarget(session.ManagedConversationID)
			if !ok {
				return externalmcp.SessionResult{}, fmt.Errorf("external_conversation_id %q has invalid persisted managed conversation ID", externalConversationID)
			}

			persistedChannel := strings.TrimSpace(session.SlackChannel)
			if !strings.HasPrefix(persistedChannel, "#") {
				persistedChannel, err = resolveSlackChannel(callCtx, channelID)
				if err != nil {
					return externalmcp.SessionResult{}, fmt.Errorf("resolve migrated external MCP Slack channel: %w", err)
				}

				session.SlackChannel = persistedChannel
				if err := store.UpsertExternalMCPSession(externalConversationID, &session); err != nil {
					return externalmcp.SessionResult{}, fmt.Errorf("persist migrated external MCP Slack channel: %w", err)
				}
			}

			if slackChannel != persistedChannel {
				return externalmcp.SessionResult{}, fmt.Errorf("external_conversation_id %q is bound to Slack channel %q", externalConversationID, session.SlackChannel)
			}

			if !agentExposed(usedAgent) {
				return externalmcp.SessionResult{}, fmt.Errorf("external MCP agent %q is not exposed", usedAgent)
			}

			reply = &events.InboundMessage{SlackReply: &events.SlackReplyTarget{ChannelID: channelID, MessageTS: threadTS, ThreadTS: threadTS}}

			conversationID := session.PrivateConversationID
			if conversationID == "" {
				conversationID = session.ManagedConversationID
			}

			activation := func(activeCtx context.Context, inbound *events.InboundMessage) error {
				relayed, err := textRelay(activeCtx, &events.ExternalMCPRelay{ConversationID: conversationID, ExternalConversationID: externalConversationID, Agent: usedAgent, Text: input, Attachments: outboundAttachments}, reply, "")
				if err != nil {
					return fmt.Errorf("send text connector external MCP thread relay: %w", err)
				}

				if relayed != nil {
					reply = relayed
					inbound.SlackReply = relayed.SlackReply
				}

				return nil
			}

			result, _, err := submitExternalMCPInput(callCtx, submitAgent, usedAgent, conversationID, &inboundContent, metadata, strings.TrimSpace(username), reply, externalConversationID, activation)

			return result, err
		}

		usedAgent := requestedAgent

		if !agentExposed(usedAgent) {
			return externalmcp.SessionResult{}, fmt.Errorf("external MCP agent %q is not exposed", usedAgent)
		}

		privateConversationID := "external_mcp:" + usedAgent + ":" + rand.Text()

		reply, err = textRelay(callCtx, &events.ExternalMCPRelay{ConversationID: privateConversationID, ExternalConversationID: externalConversationID, Agent: usedAgent, Text: input, Attachments: outboundAttachments}, nil, slackChannel)
		if err != nil {
			return externalmcp.SessionResult{}, err
		}

		if reply == nil || reply.SlackReply == nil {
			return externalmcp.SessionResult{}, errors.New("slack external MCP relay returned no reply target")
		}

		reply.SlackReply.ThreadTS = reply.SlackReply.MessageTS

		managedConversationID := harnessbridge.SlackThreadConversationID(reply.SlackReply.ChannelID, reply.SlackReply.ThreadTS)

		createdConversationID = managedConversationID
		if err := store.RegisterExternalMCPConversation(externalConversationID, managedAgent, &harnessbridge.ExternalMCPSessionState{Agent: usedAgent, PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID, SlackChannel: slackChannel}); err != nil {
			return externalmcp.SessionResult{}, fmt.Errorf("persist external MCP conversation: %w", err)
		}

		durableRegistration = true

		result, promptAccepted, err = submitExternalMCPInput(callCtx, submitAgent, usedAgent, privateConversationID, &inboundContent, metadata, strings.TrimSpace(username), reply, externalConversationID, harnessbridge.NoopActivationHook)

		return result, err
	})
	if err != nil {
		return nil, fmt.Errorf("start external MCP HTTP server: %w", err)
	}

	return server, nil
}

func cleanupFailedExternalMCPConversation(cleanupTextRelay func(context.Context, *events.InboundMessage), store *harnessbridge.SessionService, logger *slog.Logger, reply *events.InboundMessage, externalConversationID, conversationID string, durableRegistration, promptAccepted bool) {
	if promptAccepted {
		return
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cleanupTextRelay(cleanupCtx, reply)

	if !durableRegistration {
		return
	}

	if err := store.RemoveExternalMCPConversation(externalConversationID); err != nil {
		logger.Error("clean failed external MCP conversation", "external_conversation_id", externalConversationID, "conversation_id", conversationID, "error", err)
	}
}

type keyedConversationLock struct {
	refs int
	mu   sync.Mutex
}

type keyedConversationLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedConversationLock
}

func newKeyedConversationLocks() *keyedConversationLocks {
	return &keyedConversationLocks{locks: map[string]*keyedConversationLock{}}
}

func (l *keyedConversationLocks) lock(key string) func() {
	l.mu.Lock()

	entry := l.locks[key]
	if entry == nil {
		entry = new(keyedConversationLock)
		l.locks[key] = entry
	}

	entry.refs++
	l.mu.Unlock()

	entry.mu.Lock()

	return func() {
		entry.mu.Unlock()
		l.mu.Lock()

		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}

func externalMCPInboundContent(attachments []externalmcp.SessionPromptAttachment) (events.InboundContent, []events.OutboundAttachment, error) {
	if len(attachments) == 0 {
		return events.InboundContent{}, nil, nil
	}

	var content events.InboundContent

	outbound := make([]events.OutboundAttachment, 0, len(attachments))
	for i := range attachments {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(attachments[i].DataBase64))
		if err != nil {
			return events.InboundContent{}, nil, fmt.Errorf("decode external MCP attachment %d: %w", i+1, err)
		}

		name := strings.TrimSpace(attachments[i].Name)
		mimeType := strings.TrimSpace(attachments[i].MIMEType)
		outbound = append(outbound, events.OutboundAttachment{Name: name, MIMEType: mimeType, Data: append([]byte(nil), data...)})

		if events.IsTextAttachment(name, mimeType) {
			descriptor := name
			if descriptor == "" {
				descriptor = "attachment"
			}

			descriptorMIMEType := mimeType
			if mediaType, _, err := mime.ParseMediaType(descriptorMIMEType); err == nil {
				descriptorMIMEType = mediaType
			}

			if descriptorMIMEType = strings.ToLower(strings.TrimSpace(descriptorMIMEType)); descriptorMIMEType != "" {
				descriptor += " (" + descriptorMIMEType + ")"
			}

			switch {
			case len(data) > events.MaxInboundTextAttachmentBytes:
				content.AttachmentWarnings = append(content.AttachmentWarnings, "Skipped external MCP text attachment "+descriptor+" because it exceeded the text file size limit.")
			case !utf8.Valid(data) || bytes.Contains(data, []byte{0}):
				content.AttachmentWarnings = append(content.AttachmentWarnings, "Skipped external MCP text attachment "+descriptor+" because it contained non-UTF-8 text data.")
			case strings.TrimSpace(string(data)) == "":
				content.AttachmentWarnings = append(content.AttachmentWarnings, "Skipped external MCP text attachment "+descriptor+" because it contained empty text data.")
			default:
				content.TextAttachments = append(content.TextAttachments, "External MCP text file attachment "+descriptor+":\n"+string(data))
			}

			continue
		}

		content.Attachments = append(content.Attachments, events.InboundAttachment{Name: name, MIMEType: mimeType, Data: data})
	}

	return content, outbound, nil
}

func submitExternalMCPInput(ctx context.Context, submitAgent func(context.Context, string, string, *events.InboundMessage, harnessbridge.ActivationHook) error, usedAgent, conversationID string, content *events.InboundContent, metadata map[string]string, principal string, reply *events.InboundMessage, externalConversationID string, activation harnessbridge.ActivationHook) (externalmcp.SessionResult, bool, error) {
	inbound := events.NewInboundMessageFromContent(events.SourceExternalMCP, events.InboundKindPrompt, "", content, true)

	inbound.Metadata = maps.Clone(metadata)
	delete(inbound.Metadata, events.InboundOriginMetadataKey)
	delete(inbound.Metadata, events.InboundMediaMetadataKey)
	delete(inbound.Metadata, events.InboundPrincipalMetadataKey)

	if strings.TrimSpace(principal) != "" {
		if inbound.Metadata == nil {
			inbound.Metadata = map[string]string{}
		}

		inbound.Metadata[events.InboundPrincipalMetadataKey] = strings.TrimSpace(principal)
	}

	if strings.TrimSpace(externalConversationID) != "" {
		if inbound.Metadata == nil {
			inbound.Metadata = map[string]string{}
		}

		inbound.Metadata["external_conversation_id"] = strings.TrimSpace(externalConversationID)
	}

	if reply != nil {
		inbound.SlackReply = reply.SlackReply
	}

	resultCh := inbound.EnableResponseWait()

	if err := submitAgent(ctx, usedAgent, conversationID, inbound, activation); err != nil {
		return externalmcp.SessionResult{}, false, fmt.Errorf("submit external MCP input to agent %q: %w", usedAgent, err)
	}

	select {
	case <-ctx.Done():
		return externalmcp.SessionResult{}, true, fmt.Errorf("wait for external MCP reply: %w", ctx.Err())
	case result, ok := <-resultCh:
		if !ok {
			return externalmcp.SessionResult{}, true, errors.New("wait for external MCP reply: response channel closed")
		}

		if result.Err != nil {
			return externalmcp.SessionResult{}, true, fmt.Errorf("wait for external MCP reply: %w", result.Err)
		}

		attachments := make([]externalmcp.SessionAttachment, 0, len(result.Attachments))
		for i := range result.Attachments {
			name := strings.TrimSpace(result.Attachments[i].Name)
			if name == "" {
				name = fmt.Sprintf("attachment-%d", i+1)
			}

			attachments = append(attachments, externalmcp.SessionAttachment{Name: name, MIMEType: result.Attachments[i].MIMEType, DataBase64: base64.StdEncoding.EncodeToString(result.Attachments[i].Data)})
		}

		return externalmcp.SessionResult{ExternalConversationID: externalConversationID, Agent: usedAgent, Answer: result.Text, Attachments: attachments}, true, nil
	}
}

func outboundLoop(
	ctx context.Context,
	bus *events.Bus,
	slackSend func(context.Context, *events.OutboundMessage) error,
	slackAbort func(*events.OutboundMessage),
	logger *slog.Logger,
) error {
	type outboundTargetDelivery struct {
		msg    *events.OutboundMessage
		notify func(error)
	}

	startWorker := func(target string, deliver func(context.Context, *events.OutboundMessage) error) chan outboundTargetDelivery {
		queue := make(chan outboundTargetDelivery, 128)

		go func() {
			for delivery := range queue {
				started := time.Now()
				attrs := make([]any, 0, 26)
				attrs = append(attrs, "target", target, "source", delivery.msg.Source, "conversation_id", delivery.msg.ConversationID, "turn_id", delivery.msg.TurnID, "sequence", delivery.msg.Sequence, "complete", delivery.msg.Complete, "post_progress_text", delivery.msg.PostProgressText, "text_len", len(delivery.msg.Text), "text_rune_len", len([]rune(delivery.msg.Text)), "progress_text_len", len([]rune(delivery.msg.ProgressText)))
				logger.Info("starting outbound target delivery", attrs...)

				err := deliver(ctx, delivery.msg)

				attrs = append(attrs, "duration", time.Since(started), "error", err)
				if err != nil {
					logger.Error("finished outbound target delivery", attrs...)
				} else {
					logger.Info("finished outbound target delivery", attrs...)
				}

				delivery.notify(err)
			}
		}()

		return queue
	}

	slackDeliver := func(sendCtx context.Context, msg *events.OutboundMessage) error {
		err := slackSend(sendCtx, msg)
		if err != nil && msg.Complete && sendCtx.Err() == nil {
			slackAbort(msg)
		}

		return err
	}

	slackQueue := startWorker("slack_main", slackDeliver)

	defer func() {
		close(slackQueue)
	}()

	dispatch := func(queue chan outboundTargetDelivery, msg *events.OutboundMessage, notify func(error)) {
		select {
		case <-ctx.Done():
			notify(ctx.Err())
		case queue <- outboundTargetDelivery{msg: msg, notify: notify}:
		}
	}

	for msg := range bus.Outbound(ctx) {
		if msg == nil {
			continue
		}

		pending := 0
		results := make(chan error, len(msg.Targets))
		notify := func(err error) {
			results <- err
		}

		if slices.Contains(msg.Targets, events.OutputTargetSlack) {
			pending++

			dispatch(slackQueue, msg, notify)
		}

		if pending == 0 {
			msg.MarkDelivered(nil)
			continue
		}

		go func(msg *events.OutboundMessage, pending int, results <-chan error) {
			var errRoute error
			for range pending {
				errRoute = errors.Join(errRoute, <-results)
			}

			if errRoute != nil && ctx.Err() == nil {
				logger.Error("route outbound assistant response", "error", errRoute)
			}

			msg.MarkDelivered(errRoute)
		}(msg, pending, results)
	}

	if errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("outbound loop canceled: %w", err)
	}

	return nil
}
