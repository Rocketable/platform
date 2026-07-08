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
	slackRetryInitial, slackRetryMax = time.Second, 30 * time.Second
	defaultSlackDeliveryMax          = 30 * time.Second
	stateRetention                   = 30 * 24 * time.Hour
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
		mainBridge       *harnessbridge.Bridge
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
	} else if stats.Threads+stats.ResponseCheckpoints+stats.ExternalMCPSessions > 0 || stats.SessionRows > 0 {
		logger.Info("pruned stale rocketclaw state", "threads", stats.Threads, "response_checkpoints", stats.ResponseCheckpoints, "external_mcp_sessions", stats.ExternalMCPSessions, "session_rows", stats.SessionRows)
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
			if err := harnessbridge.ValidateRuntimeDefinitions(cfg.Workspace, runtimeDir); err != nil {
				return fmt.Errorf("validate rocketcode definitions: %w", err)
			}

			if err := cronjob.ValidateRuntimeDefinitions(cfg.Workspace, runtimeDir); err != nil {
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

	cronjobs = cronjob.New(cfg.Workspace, cfg.RuntimeDirName(), bus, rocketcodeSessions, func(jobCtx context.Context, agent, prompt string, log *slog.Logger, progress *harnessbridge.RawRunProgress) (cronjob.RunResult, error) {
		progress.SessionService = rocketcodeSessions
		progress.ScheduleMessage = mainBridge.ScheduleMessage
		progress.ResetScheduledMessages = mainBridge.ResetScheduledMessages
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
		"slack_enabled", cfg.Slack.Enabled,
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

	mainOutputTargets := configuredMainOutputTargets(cfg)
	mainBridge = harnessbridge.NewConversation(cfg, bus, &harnessbridge.Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: true, OutputTargets: mainOutputTargets, RecoveringActiveTurn: recoveringConversations[events.MainConversationID()], RequestRestart: requestRestart, RequestReload: requestReload, AskUserQuestion: questionBroker.ask, StartNewThread: startNewThread, SessionService: rocketcodeSessions}, logger)
	threadBridges = newThreadBridgeManager(bus, cfg, rocketcodeSessions, logger, func(bridgeConfig bridgeConfig) directBridge {
		return harnessbridge.NewConversation(cfg, bus, &harnessbridge.Config{ConversationID: bridgeConfig.ConversationID, Agent: bridgeConfig.Agent, ConsumeSharedInbound: false, OutputTargets: bridgeConfig.OutputTargets, RecoveringActiveTurn: bridgeConfig.RecoveringActiveTurn, RequestRestart: requestRestart, RequestReload: requestReload, AskUserQuestion: questionBroker.ask, StartNewThread: startNewThread, SessionService: rocketcodeSessions}, logger)
	})
	threadBridges.targets = mainOutputTargets

	logger.Info("starting rocketcode bridge")

	if err := mainBridge.Start(runCtx); err != nil {
		return fmt.Errorf("start rocketcode bridge: %w", err)
	}

	logger.Info("bridge started")

	if err := threadBridges.StartPendingScheduledMessages(recoveringConversations); err != nil {
		return err
	}

	if err := threadBridges.StartActiveGoals(recoveringConversations); err != nil {
		return err
	}

	for i := range recoveredTurns {
		turn := &recoveredTurns[i]

		conversationID := strings.TrimSpace(turn.Checkpoint.ConversationKey)
		if conversationID == events.MainConversationID() {
			err = mainBridge.RecoverActiveTurn(runCtx, turn)
		} else {
			err = threadBridges.RecoverActiveTurn(runCtx, turn)
		}

		if err != nil {
			if isStartupRecoveryShutdownError(err) {
				return err
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

	if cfg.Slack.Enabled {
		logger.Info("starting Slack connector", "room", cfg.Slack.Room)

		slackSink = slackconnector.New(&cfg.Slack, bus, cfg.EmergencySafeWords, cfg.ThreadAgents, threadBridges, cronjobs, mainBridge.InterruptActiveTurn, questionBroker.answer, logger)
		questionBroker.post, questionBroker.delete = slackSink.AskUserQuestion, slackSink.DeleteUserQuestion
		startThreadRoot = slackSink.StartNewThreadRoot

		cronjobs.SendTextChannel = slackSink.SendCronjobChannelThread
		if err := slackSink.Start(runCtx); err != nil {
			return fmt.Errorf("start Slack connector: %w", err)
		}

		stops = append(stops, namedStopper{name: "slack", stop: slackSink.Stop})
	}

	primaryTextSend := func(context.Context, *events.OutboundMessage) error { return nil }
	textRelay := func(context.Context, string, []events.OutboundAttachment, *events.InboundMessage, string) (*events.InboundMessage, error) {
		return nil, nil
	}
	cleanupTextRelay := func(context.Context, *events.InboundMessage) {}

	if slackSink != nil {
		primaryTextSend = slackSink.SendResponse
		textRelay = func(relayCtx context.Context, text string, attachments []events.OutboundAttachment, reply *events.InboundMessage, channelName string) (*events.InboundMessage, error) {
			var (
				target *events.SlackReplyTarget
				err    error
			)

			if reply != nil && reply.SlackReply != nil {
				target, err = slackSink.SendExternalMCPThreadRelay(relayCtx, reply.SlackReply.ChannelID, reply.SlackReply.ThreadTS, text, attachments)
				if err != nil {
					return nil, fmt.Errorf("send Slack external MCP thread relay: %w", err)
				}

				return &events.InboundMessage{SlackReply: target}, nil
			}

			channelID := cfg.Slack.Room
			if strings.TrimSpace(channelName) != "" {
				channelID = channelName
			}

			target, err = slackSink.SendExternalMCPRelay(relayCtx, channelID, text, attachments)
			if err != nil {
				return nil, fmt.Errorf("send Slack external MCP relay: %w", err)
			}

			return &events.InboundMessage{SlackReply: target}, nil
		}
		cleanupTextRelay = func(cleanupCtx context.Context, reply *events.InboundMessage) {
			if reply != nil {
				slackSink.CleanupPendingReplyPlaceholder(cleanupCtx, reply.SlackReply)
			}
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
			agents, err := harnessbridge.ExternalMCPAgentsIn(cfg.Workspace, cfg.RuntimeDirName())
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

		externalMCP, err := startExternalMCPServer(runCtx, cfg, textRelay, cleanupTextRelay, externalMCPUsers, externalMCPAgentExposed, rocketcodeSessions, threadBridges.SubmitExternalMCP, logger)
		if err != nil {
			return err
		}

		stops = append(stops, namedStopper{name: "external_mcp", stop: externalMCP.Close})
	}

	slackSend := func(context.Context, *events.OutboundMessage) error { return nil }
	if slackSink != nil {
		slackSend = primaryTextSend
	}

	logger.Info(
		"outbound routing loop started",
		"slack_enabled", slackSink != nil,
	)

	go func() {
		select {
		case <-ctx.Done():
			startShutdown("runtime context canceled", false)
		case <-runCtx.Done():
		}
	}()

	err = outboundLoop(runCtx, bus, slackSend, logger)

	select {
	case <-restartRequested:
		return ErrRestartRequested
	default:
	}

	return err
}

func configuredMainOutputTargets(cfg *config.Config) []events.OutputTarget {
	targets := []events.OutputTarget{}
	if cfg.Slack.Enabled {
		targets = append(targets, events.OutputTargetSlackMain)
	}

	return targets
}

//nolint:gocyclo // External MCP routing branches are explicit to preserve main vs fork semantics.
func startExternalMCPServer(
	ctx context.Context,
	cfg *config.Config,
	textRelay func(context.Context, string, []events.OutboundAttachment, *events.InboundMessage, string) (*events.InboundMessage, error),
	cleanupTextRelay func(context.Context, *events.InboundMessage),
	users map[string]string,
	agentExposed func(string) bool,
	store *harnessbridge.SessionService,
	submitAgent func(context.Context, string, string, *events.InboundMessage, harnessbridge.ActivationHook) error,
	logger *slog.Logger,
) (*externalmcp.Server, error) {
	server, err := externalmcp.StartSessionPromptServer(ctx, logger, cfg.MCPExternal.ListenAddr, users, func(callCtx context.Context, username, externalConversationID, requestedAgent, input string, metadata map[string]string, attachments []externalmcp.SessionPromptAttachment, slackChannel string) (result externalmcp.SessionResult, err error) {
		var reply *events.InboundMessage

		defer func() {
			if err != nil {
				cleanupTextRelay(callCtx, reply)
			}
		}()

		externalConversationID = strings.TrimSpace(externalConversationID)

		requestedAgent = strings.TrimSpace(requestedAgent)

		inboundContent, outboundAttachments, err := externalMCPInboundContent(attachments)
		if err != nil {
			return externalmcp.SessionResult{}, err
		}

		inboundContent.Text = input

		if externalConversationID != "" {
			session, ok, err := store.ExternalMCPSession(externalConversationID)
			if err != nil {
				return externalmcp.SessionResult{}, fmt.Errorf("load external MCP session state: %w", err)
			}

			if ok {
				session.Agent = strings.TrimSpace(session.Agent)

				session.ConversationID = strings.TrimSpace(session.ConversationID)

				usedAgent := session.Agent
				if requestedAgent != "" && requestedAgent != usedAgent {
					logger.Warn(
						"external MCP requested agent mismatched persisted session agent; using persisted agent",
						"external_conversation_id", externalConversationID,
						"requested_agent", requestedAgent,
						"used_agent", usedAgent,
					)
				}

				if usedAgent == "" || session.ConversationID == "" {
					return externalmcp.SessionResult{}, fmt.Errorf("external_conversation_id %q has incomplete persisted state", externalConversationID)
				}

				if !agentExposed(usedAgent) {
					return externalmcp.SessionResult{}, fmt.Errorf("external MCP agent %q is not exposed", usedAgent)
				}

				conversationID, _, ok, err := store.ThreadForSeed(session.ConversationID)
				if err != nil {
					return externalmcp.SessionResult{}, fmt.Errorf("load external MCP text thread alias: %w", err)
				}

				if ok {
					if channelID, threadTS, ok := harnessbridge.SlackThreadTarget(conversationID); ok {
						reply = &events.InboundMessage{SlackReply: &events.SlackReplyTarget{ChannelID: channelID, MessageTS: threadTS, ThreadTS: threadTS}}
					}
				}

				activation := harnessbridge.NoopActivationHook
				if reply != nil {
					activation = func(activeCtx context.Context, inbound *events.InboundMessage) error {
						return retrySlackDelivery(activeCtx, logger, "external MCP thread relay", func(sendCtx context.Context) error {
							var (
								err     error
								relayed *events.InboundMessage
							)

							relayed, err = textRelay(sendCtx, input, outboundAttachments, reply, "")
							if err != nil {
								return fmt.Errorf("send text connector external MCP thread relay: %w", err)
							}

							if relayed != nil {
								reply = relayed
								inbound.SlackReply = relayed.SlackReply
							}

							return nil
						})
					}
				}

				return submitExternalMCPInput(callCtx, submitAgent, usedAgent, session.ConversationID, &inboundContent, metadata, strings.TrimSpace(username), reply, externalConversationID, activation)
			}
		}

		usedAgent := requestedAgent
		if usedAgent == "" {
			return externalmcp.SessionResult{}, errors.New("external MCP agent is required for new conversations")
		}

		if !agentExposed(usedAgent) {
			return externalmcp.SessionResult{}, fmt.Errorf("external MCP agent %q is not exposed", usedAgent)
		}

		publicConversationID := externalConversationID
		if publicConversationID == "" {
			publicConversationID = rand.Text()
		}

		conversationID := "external_mcp:" + usedAgent + ":" + rand.Text()
		if err := store.UpsertExternalMCPSession(publicConversationID, harnessbridge.ExternalMCPSessionState{Agent: usedAgent, ConversationID: conversationID}); err != nil {
			return externalmcp.SessionResult{}, fmt.Errorf("persist external MCP session mapping: %w", err)
		}

		relayText := input

		threadPrefix := ""
		for prefix, threadAgent := range cfg.ThreadAgents {
			if prefix = strings.TrimSpace(prefix); prefix != "" && strings.TrimSpace(threadAgent.Agent) == usedAgent && (threadPrefix == "" || prefix < threadPrefix) {
				threadPrefix = prefix
			}
		}

		if threadPrefix != "" {
			relayText = threadPrefix + " " + input
		}

		activation := func(activeCtx context.Context, inbound *events.InboundMessage) error {
			logger.Info("relaying external MCP input to text connector thread root", "text_len", len(relayText))

			if err := retrySlackDelivery(activeCtx, logger, "external MCP relay", func(sendCtx context.Context) error {
				var err error

				reply, err = textRelay(sendCtx, relayText, outboundAttachments, nil, slackChannel)
				if err != nil {
					return fmt.Errorf("send text connector external MCP relay: %w", err)
				}

				return nil
			}); err != nil {
				return err
			}

			if reply != nil {
				inbound.SlackReply = reply.SlackReply
				threadKey := ""

				if reply.SlackReply != nil {
					reply.SlackReply.ThreadTS = reply.SlackReply.MessageTS
					inbound.SlackReply = reply.SlackReply
					threadKey = harnessbridge.SlackThreadConversationID(reply.SlackReply.ChannelID, reply.SlackReply.ThreadTS)
				}

				if err := store.UpsertThread(threadKey, usedAgent); err != nil {
					return fmt.Errorf("persist external MCP text thread alias: %w", err)
				}

				if err := store.MarkThreadSeeded(threadKey, conversationID); err != nil {
					return fmt.Errorf("persist external MCP text thread alias: %w", err)
				}
			}

			return nil
		}

		return submitExternalMCPInput(callCtx, submitAgent, usedAgent, conversationID, &inboundContent, metadata, strings.TrimSpace(username), reply, publicConversationID, activation)
	})
	if err != nil {
		return nil, fmt.Errorf("start external MCP HTTP server: %w", err)
	}

	return server, nil
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

func submitExternalMCPInput(ctx context.Context, submitAgent func(context.Context, string, string, *events.InboundMessage, harnessbridge.ActivationHook) error, usedAgent, conversationID string, content *events.InboundContent, metadata map[string]string, principal string, reply *events.InboundMessage, externalConversationID string, activation harnessbridge.ActivationHook) (externalmcp.SessionResult, error) {
	inbound := events.NewMainInboundMessageFromContent(events.SourceExternalMCP, events.InboundKindPrompt, "", content, true)

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
		return externalmcp.SessionResult{}, fmt.Errorf("submit external MCP input to agent %q: %w", usedAgent, err)
	}

	select {
	case <-ctx.Done():
		return externalmcp.SessionResult{}, fmt.Errorf("wait for external MCP reply: %w", ctx.Err())
	case result, ok := <-resultCh:
		if !ok {
			return externalmcp.SessionResult{}, errors.New("wait for external MCP reply: response channel closed")
		}

		if result.Err != nil {
			return externalmcp.SessionResult{}, fmt.Errorf("wait for external MCP reply: %w", result.Err)
		}

		attachments := make([]externalmcp.SessionAttachment, 0, len(result.Attachments))
		for i := range result.Attachments {
			name := strings.TrimSpace(result.Attachments[i].Name)
			if name == "" {
				name = fmt.Sprintf("attachment-%d", i+1)
			}

			attachments = append(attachments, externalmcp.SessionAttachment{Name: name, MIMEType: result.Attachments[i].MIMEType, DataBase64: base64.StdEncoding.EncodeToString(result.Attachments[i].Data)})
		}

		return externalmcp.SessionResult{ExternalConversationID: externalConversationID, Agent: usedAgent, Answer: result.Text, Attachments: attachments}, nil
	}
}

func outboundLoop(
	ctx context.Context,
	bus *events.Bus,
	slackSend func(context.Context, *events.OutboundMessage) error,
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
		return retrySlackDelivery(sendCtx, logger, "assistant response", func(retryCtx context.Context) error {
			return slackSend(retryCtx, msg)
		})
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

		if slices.Contains(msg.Targets, events.OutputTargetSlackMain) {
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

func retrySlackDelivery(
	ctx context.Context,
	logger *slog.Logger,
	purpose string,
	send func(context.Context) error,
) error {
	if defaultSlackDeliveryMax > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, defaultSlackDeliveryMax)
		defer cancel()
	}

	delay := slackRetryInitial

	for attempt := 1; ; attempt++ {
		err := send(ctx)
		if err == nil {
			if attempt > 1 {
				logger.Info("Slack delivery recovered", "purpose", purpose, "attempt", attempt)
			}

			return nil
		}

		if ctx.Err() != nil {
			return fmt.Errorf("slack delivery canceled while retrying %s after %v: %w", purpose, err, ctx.Err())
		}

		logger.Error(
			"Slack delivery failed; retrying",
			"purpose", purpose,
			"attempt", attempt,
			"retry_in", delay,
			"error", err,
		)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()

			return fmt.Errorf("slack delivery canceled while retrying %s after %v: %w", purpose, err, ctx.Err())
		case <-timer.C:
		}

		if delay < slackRetryMax {
			delay *= 2
			if delay > slackRetryMax {
				delay = slackRetryMax
			}
		}
	}
}
