// Package app wires the rocketclaw runtime together.
package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/cronjob"
	"github.com/Rocketable/platform/internal/rocketclaw/discordtext"
	"github.com/Rocketable/platform/internal/rocketclaw/discordvoice"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/externalmcp"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/Rocketable/platform/internal/rocketclaw/openaiaudio"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/Rocketable/platform/internal/rocketclaw/slackconnector"
	"github.com/Rocketable/platform/internal/rocketclaw/voice"
	"github.com/Rocketable/platform/internal/rocketclaw/webui"
)

// ErrRestartRequested indicates rocketclaw should exit so a supervisor can restart it.
var ErrRestartRequested = errors.New("restart requested")

type namedStopper struct {
	name string
	stop func(context.Context) error
}

const (
	slackRetryInitial, slackRetryMax                = time.Second, 30 * time.Second
	defaultSlackDeliveryMax, gracefulRestartTimeout = 30 * time.Second, 300 * time.Second
	stateRetention                                  = 30 * 24 * time.Hour
)

// Run starts rocketclaw and blocks until the context is canceled or a fatal error occurs.
//
//nolint:gocyclo // Runtime wiring is kept in one place so startup order remains explicit.
func Run(ctx context.Context, cfg *config.Config, configPath string, logger *slog.Logger) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	bus := events.New(events.Config{MinimumWaitAfterHumanInteraction: cfg.MinimumWaitAfterHumanInteractionDuration})
	defer bus.Close()

	var (
		restartOnce      sync.Once
		restartRequested = make(chan struct{})
		mainBridge       *harnessbridge.Bridge
		threadBridges    *threadBridgeManager
		cronjobs         *cronjob.Manager
		slackSink        *slackconnector.Connector
		discordTextSink  *discordtext.Connector
		externalMCP      *externalmcp.Server
	)

	rocketcodeSessions, err := harnessbridge.NewSessionServiceIn(cfg.Workspace, cfg.WorkDirName())
	if err != nil {
		return fmt.Errorf("start rocketcode session service: %w", err)
	}

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

	if stats, err := rocketcodeSessions.Vacuum(runCtx); err != nil {
		logger.Warn("vacuum rocketclaw state", "error", err)
	} else {
		logger.Info("vacuumed rocketclaw state", "before_pages", stats.BeforePageCount, "before_free_pages", stats.BeforeFreePages, "after_pages", stats.AfterPageCount, "after_free_pages", stats.AfterFreePages)
	}

	if stats, err := rocketcodeSessions.CheckpointWAL(runCtx); err != nil {
		logger.Warn("checkpoint rocketclaw state WAL", "error", err)
	} else if stats.Busy > 0 {
		logger.Warn("checkpoint rocketclaw state WAL busy", "busy", stats.Busy, "log_frames", stats.LogFrames, "checkpointed_frames", stats.CheckpointedFrames)
	} else {
		logger.Info("checkpointed rocketclaw state WAL", "busy", stats.Busy, "log_frames", stats.LogFrames, "checkpointed_frames", stats.CheckpointedFrames)
	}

	if err := rocketcodeSessions.ApplyPendingRestartNotifications(runCtx); err != nil {
		return fmt.Errorf("apply pending restart notifications: %w", err)
	}

	if err := skel.SyncInWithOverlays(cfg.Workspace, cfg.WorkDirName(), cfg.Overlays, logger); err != nil {
		return fmt.Errorf("sync rocketclaw skeleton: %w", err)
	}

	var externalMCPUsers map[string]string
	if cfg.MCPExternal.Enabled {
		externalMCPUsers, err = config.LoadExternalMCPUsers(configPath)
		if err != nil {
			return fmt.Errorf("load external MCP auth users: %w", err)
		}
	}

	requestRestart := func(_ context.Context, reason string) (string, error) { //nolint:unparam // Signature is shared with restart hooks that may fail.
		started := false

		restartOnce.Do(func() {
			started = true

			close(restartRequested)
			logger.Warn("restart requested; draining rocketclaw before supervisor restart", "reason", reason)

			go func() {
				stopCtx, stop := context.WithTimeout(context.Background(), gracefulRestartTimeout)
				if slackSink != nil {
					_ = slackSink.Stop(stopCtx)
				}

				if discordTextSink != nil {
					_ = discordTextSink.Stop(stopCtx)
				}

				if externalMCP != nil {
					_ = externalMCP.Stop(stopCtx)
				}

				stop()

				threadBridges.StopAccepting()

				cronCtx, stop := context.WithTimeout(context.Background(), gracefulRestartTimeout)
				if err := cronjobs.Stop(cronCtx); err != nil {
					logger.Warn("graceful restart stopped waiting for cronjobs idle", "error", err)
				}

				stop()

				bus.StopInbound()

				drainCtx, stop := context.WithTimeout(context.Background(), gracefulRestartTimeout)
				defer stop()

				if err := bus.WaitInboundDequeued(drainCtx); err != nil {
					logger.Warn("graceful restart stopped waiting for inbound queue handoff", "error", err)
				}

				if err := mainBridge.WaitIdle(drainCtx); err != nil {
					logger.Warn("graceful restart stopped waiting for bridge idle", "error", err)
				}

				if err := threadBridges.WaitIdle(drainCtx); err != nil {
					logger.Warn("graceful restart stopped waiting for thread bridges idle", "error", err)
				}

				if err := bus.WaitOutboundIdle(drainCtx); err != nil {
					logger.Warn("graceful restart stopped waiting for outbound drain", "error", err)
				}

				cancel()
			}()
		})

		if !started {
			logger.Warn("restart requested while shutdown already in progress", "reason", reason)
		}

		return "graceful restart scheduled", nil
	}

	cronjobs = cronjob.New(cfg.Workspace, cfg.WorkDirName(), bus, func(jobCtx context.Context, agent, prompt string, log *slog.Logger, progress *harnessbridge.RawRunProgress) (cronjob.RunResult, error) {
		progress.SessionService = rocketcodeSessions
		progress.ScheduleMessage = mainBridge.ScheduleMessage
		progress.ResetScheduledMessages = mainBridge.ResetScheduledMessages
		progress.RequestRestart = requestRestart

		result, err := harnessbridge.RunRawWithProgress(jobCtx, cfg, agent, prompt, log, progress)
		if err != nil {
			return cronjob.RunResult{}, fmt.Errorf("run raw cronjob turn: %w", err)
		}

		return cronjob.RunResult{Text: result.Text, VerbatimMessage: result.VerbatimMessage, Attachments: result.Attachments}, nil
	}, logger)

	logger.Info(
		"initializing rocketclaw runtime",
		"workspace", cfg.Workspace,
		"discord_text_enabled", cfg.DiscordText.Enabled,
		"discord_voice_enabled", cfg.DiscordVoice.Enabled,
		"mcp_external_enabled", cfg.MCPExternal.Enabled,
		"slack_enabled", cfg.Slack.Enabled,
	)

	var discordSink *discordvoice.Connector

	whisper := openaiaudio.NewWhisperClient(
		cfg.OpenAI.STTAPIKey,
		cfg.OpenAI.STTAPIBaseURL,
		cfg.OpenAI.STTModel,
		cfg.OpenAI.STTPrompt,
	)
	tts := openaiaudio.NewTTSClient(
		cfg.OpenAI.TTSAPIKey,
		cfg.OpenAI.TTSAPIBaseURL,
		cfg.OpenAI.TTSModel,
		cfg.OpenAI.TTSVoice,
		cfg.OpenAI.TTSInstructions,
	)

	mainOutputTargets := configuredMainOutputTargets(cfg)
	mainBridge = harnessbridge.NewConversation(cfg, bus, &harnessbridge.Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: true, OutputTargets: mainOutputTargets, RequestRestart: requestRestart, SessionService: rocketcodeSessions}, logger)
	threadBridges = newThreadBridgeManager(bus, rocketcodeSessions, logger, func(bridgeConfig bridgeConfig) directBridge {
		return harnessbridge.NewConversation(cfg, bus, &harnessbridge.Config{ConversationID: bridgeConfig.ConversationID, Agent: bridgeConfig.Agent, ConsumeSharedInbound: false, OutputTargets: bridgeConfig.OutputTargets, RequestRestart: requestRestart, SessionService: rocketcodeSessions}, logger)
	})
	threadBridges.targets = mainOutputTargets

	logger.Info("starting rocketcode bridge")

	if err := mainBridge.Start(runCtx); err != nil {
		return fmt.Errorf("start rocketcode bridge: %w", err)
	}

	logger.Info("bridge started")

	if err := threadBridges.StartPendingScheduledMessages(); err != nil {
		return err
	}

	var stops []namedStopper

	defer func() {
		logger.Info("shutting down rocketclaw runtime")

		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		for _, sink := range stops {
			if err := sink.stop(stopCtx); err != nil {
				logger.Warn("stop connector", "connector", sink.name, "error", err)
			}
		}

		_ = cronjobs.Stop(stopCtx)
		_ = threadBridges.Stop(stopCtx)
		_ = mainBridge.Stop(stopCtx)
	}()

	if cfg.Slack.Enabled {
		logger.Info("starting Slack connector", "room", cfg.Slack.Room)

		slackSink = slackconnector.New(&cfg.Slack, bus, cfg.EmergencySafeWords, cfg.ThreadAgents, threadBridges, cronjobs, logger)

		cronjobs.SendSlackChannel = slackSink.SendCronjobChannelThread
		if err := slackSink.Start(runCtx); err != nil {
			return fmt.Errorf("start Slack connector: %w", err)
		}

		stops = append(stops, namedStopper{name: "slack", stop: slackSink.Stop})
	}

	if cfg.DiscordText.Enabled {
		logger.Info("starting Discord text connector", "channel_id", cfg.DiscordText.ChannelID)

		discordTextSink = discordtext.New(cfg.DiscordText, bus, cfg.ThreadAgents, threadBridges, cronjobs, logger)
		if err := discordTextSink.Start(runCtx); err != nil {
			return fmt.Errorf("start Discord text connector: %w", err)
		}

		cronjobs.SendSlackChannel = discordTextSink.SendCronjobChannelThread
		stops = append(stops, namedStopper{name: "discord_text", stop: discordTextSink.Stop})
	}

	if err := cronjobs.Start(runCtx); err != nil {
		return fmt.Errorf("start cronjobs: %w", err)
	}

	var webUI *webui.Server

	if cfg.WebUI.Enabled {
		webRelay := func(context.Context, string) (*events.SlackReplyTarget, error) { return nil, nil }
		if slackSink != nil {
			webRelay = func(relayCtx context.Context, text string) (*events.SlackReplyTarget, error) {
				return relayVoiceUtteranceToSlack(relayCtx, slackSink, text, logger, "browser voice utterance")
			}
		}

		webPublisher := voice.NewTranscriptionPublisher(bus, logger, events.SourceWebVoice, cfg.EmergencySafeWords, webRelay)

		logger.Info("starting web UI listener", "listen_addr", cfg.WebUI.ListenAddr)

		webUI, err = webui.StartIn(runCtx, logger, cfg.Workspace, cfg.WorkDirName(), cfg.WebUI.ListenAddr, cfg.WebUI.CertFile, cfg.WebUI.KeyFile, whisper, tts, webPublisher)
		if err != nil {
			return fmt.Errorf("start web UI HTTPS server: %w", err)
		}

		for _, url := range webUI.URLs() {
			logger.Info("web UI voice mode available", "url", url)
		}

		stops = append(stops, namedStopper{name: "web_ui", stop: webUI.Stop})
	}

	if cfg.MCPExternal.Enabled {
		externalMCPAgents, err := harnessbridge.ExternalMCPAgentsIn(cfg.Workspace, cfg.WorkDirName())
		if err != nil {
			return fmt.Errorf("load external MCP agents: %w", err)
		}

		if len(cfg.MCPExternal.AllowedAgents) > 0 {
			filtered := make([]string, 0, len(cfg.MCPExternal.AllowedAgents))
			for _, agent := range cfg.MCPExternal.AllowedAgents {
				agent = strings.TrimSpace(agent)
				if slices.Contains(externalMCPAgents, agent) {
					filtered = append(filtered, agent)
				}
			}

			externalMCPAgents = filtered
		}

		externalMCPDefaultAgent := events.MainConversationID()
		if len(cfg.MCPExternal.AllowedAgents) > 0 && len(externalMCPAgents) > 0 {
			externalMCPDefaultAgent = externalMCPAgents[0]
		}

		slackRelay := func(context.Context, string, []events.OutboundAttachment, *events.SlackReplyTarget, string) (*events.SlackReplyTarget, error) {
			return nil, nil
		}
		cleanupSlackRelay := func(context.Context, *events.SlackReplyTarget) {}

		if slackSink != nil {
			slackRelay = func(relayCtx context.Context, text string, attachments []events.OutboundAttachment, replyTarget *events.SlackReplyTarget, channelName string) (*events.SlackReplyTarget, error) {
				if replyTarget != nil {
					return slackSink.SendExternalMCPThreadRelay(relayCtx, replyTarget.ChannelID, replyTarget.ThreadTS, text, attachments)
				}

				channelID := cfg.Slack.Room
				if strings.TrimSpace(channelName) != "" {
					channelID = channelName
				}

				return slackSink.SendExternalMCPRelay(relayCtx, channelID, text, attachments)
			}
			cleanupSlackRelay = slackSink.CleanupPendingReplyPlaceholder
		}

		externalMCP, err = startExternalMCPServer(runCtx, cfg, slackRelay, cleanupSlackRelay, externalMCPUsers, externalMCPAgents, externalMCPDefaultAgent, rocketcodeSessions, threadBridges.SubmitExternalMCP, logger)
		if err != nil {
			return err
		}

		stops = append(stops, namedStopper{name: "external_mcp", stop: externalMCP.Stop})
	}

	if cfg.DiscordVoice.Enabled {
		if cfg.OpenAI.STTAPIKey == "" || cfg.OpenAI.TTSAPIKey == "" {
			logger.Warn("Discord voice disabled because OpenAI TTS/STT API keys are missing")
		} else {
			logger.Info("starting Discord voice connector", "voice_channel_id", cfg.DiscordVoice.VoiceChannelID)

			discordSink, err = discordvoice.New(runCtx, cfg.DiscordVoice, bus, tts, requestRestart, logger)
			if err != nil {
				return fmt.Errorf("start Discord connector: %w", err)
			}

			discordRelay := func(context.Context, string) (*events.SlackReplyTarget, error) { return nil, nil }
			if slackSink != nil {
				discordRelay = func(relayCtx context.Context, text string) (*events.SlackReplyTarget, error) {
					return relayVoiceUtteranceToSlack(relayCtx, slackSink, text, logger, "discord voice utterance")
				}
			}

			processor := voice.NewProcessor(bus, whisper, logger, cfg.EmergencySafeWords, discordRelay)
			processor.Start(runCtx)
		}
	}

	if discordSink != nil {
		stops = append(stops, namedStopper{name: "discord_voice", stop: func(context.Context) error {
			return discordSink.Stop()
		}})
	}

	slackSend := discardOutboundSend
	if slackSink != nil {
		slackSend = slackSink.SendResponse
	}

	discordSend := discardOutboundSend
	if discordSink != nil {
		discordSend = discordSink.SendResponse
	}

	discordTextSend := discardOutboundSend
	if discordTextSink != nil {
		discordTextSend = discordTextSink.SendResponse
	}

	webSend := discardOutboundSend
	if webUI != nil {
		webSend = webUI.SendResponse
	}

	logger.Info(
		"outbound routing loop started",
		"slack_enabled", slackSink != nil,
		"discord_text_enabled", discordTextSink != nil,
		"discord_voice_enabled", discordSink != nil,
	)

	err = outboundLoopWithDiscordText(runCtx, bus, slackSend, discordTextSend, discordSend, webSend, logger)

	select {
	case <-restartRequested:
		return ErrRestartRequested
	default:
	}

	return err
}

func discardOutboundSend(context.Context, *events.OutboundMessage) error { return nil }

func configuredMainOutputTargets(cfg *config.Config) []events.OutputTarget {
	targets := []events.OutputTarget{}
	if cfg.Slack.Enabled {
		targets = append(targets, events.OutputTargetSlackMain)
	}

	if cfg.DiscordText.Enabled {
		targets = append(targets, events.OutputTargetDiscordText)
	}

	if cfg.DiscordVoice.Enabled {
		targets = append(targets, events.OutputTargetDiscord)
	}

	return targets
}

func relayVoiceUtteranceToSlack(
	ctx context.Context,
	slackSink *slackconnector.Connector,
	text string,
	logger *slog.Logger,
	purpose string,
) (*events.SlackReplyTarget, error) {
	logger.Info("relaying voice utterance to Slack before main session", "purpose", purpose, "text_len", len(text), "text", text)

	var replyTarget *events.SlackReplyTarget

	err := retrySlackDelivery(ctx, logger, purpose+" relay", func(sendCtx context.Context) error {
		var err error
		if purpose == "browser voice utterance" {
			replyTarget, err = slackSink.SendWebVoiceRelay(sendCtx, text)
		} else {
			replyTarget, err = slackSink.SendDiscordRelay(sendCtx, text)
		}

		if err != nil {
			return fmt.Errorf("send Slack voice relay: %w", err)
		}

		return nil
	})

	return replyTarget, err
}

//nolint:gocyclo // External MCP routing branches are explicit to preserve main vs fork semantics.
func startExternalMCPServer(
	ctx context.Context,
	cfg *config.Config,
	slackRelay func(context.Context, string, []events.OutboundAttachment, *events.SlackReplyTarget, string) (*events.SlackReplyTarget, error),
	cleanupSlackRelay func(context.Context, *events.SlackReplyTarget),
	users map[string]string,
	agents []string,
	defaultAgent string,
	store *harnessbridge.SessionService,
	submitAgent func(context.Context, string, string, *events.InboundMessage) error,
	logger *slog.Logger,
) (*externalmcp.Server, error) {
	server, err := externalmcp.StartSessionPromptServer(ctx, logger, cfg.MCPExternal.ListenAddr, users, defaultAgent, func(callCtx context.Context, username, externalConversationID, agent, input string, metadata map[string]string, attachments []externalmcp.SessionPromptAttachment, slackChannel string) (result externalmcp.SessionResult, err error) {
		_ = username

		var slackReply *events.SlackReplyTarget

		defer func() {
			if err != nil {
				cleanupSlackRelay(callCtx, slackReply)
			}
		}()

		externalConversationID = strings.TrimSpace(externalConversationID)

		agent = strings.TrimSpace(agent)

		inboundAttachments, err := externalMCPInboundAttachments(attachments)
		if err != nil {
			return externalmcp.SessionResult{}, err
		}

		outboundAttachments := make([]events.OutboundAttachment, 0, len(inboundAttachments))
		for i := range inboundAttachments {
			outboundAttachments = append(outboundAttachments, events.OutboundAttachment{Name: inboundAttachments[i].Name, MIMEType: inboundAttachments[i].MIMEType, Data: append([]byte(nil), inboundAttachments[i].Data...)})
		}

		state, err := store.Load()
		if err != nil {
			return externalmcp.SessionResult{}, fmt.Errorf("load external MCP session state: %w", err)
		}

		if externalConversationID != "" {
			if session, ok := state.ExternalMCPSessions[externalConversationID]; ok {
				session.Agent = strings.TrimSpace(session.Agent)

				session.ConversationID = strings.TrimSpace(session.ConversationID)
				if agent != "" && agent != session.Agent {
					return externalmcp.SessionResult{}, fmt.Errorf("external_conversation_id %q belongs to agent %q, not %q", externalConversationID, session.Agent, agent)
				}

				if session.Agent == "" || session.ConversationID == "" {
					return externalmcp.SessionResult{}, fmt.Errorf("external_conversation_id %q has incomplete persisted state", externalConversationID)
				}

				if !slices.Contains(agents, session.Agent) {
					return externalmcp.SessionResult{}, fmt.Errorf("external MCP agent %q is not exposed", session.Agent)
				}

				for conversationID, thread := range state.Threads {
					if strings.TrimSpace(thread.SeededFromResponse) != session.ConversationID {
						continue
					}

					rest, ok := strings.CutPrefix(conversationID, "slack-thread:")
					if !ok {
						continue
					}

					channelID, threadTS, ok := strings.Cut(rest, ":")
					if ok && strings.TrimSpace(channelID) != "" && strings.TrimSpace(threadTS) != "" {
						slackReply = &events.SlackReplyTarget{ChannelID: channelID, MessageTS: threadTS, ThreadTS: threadTS}
						break
					}
				}

				if slackReply != nil {
					if err := retrySlackDelivery(callCtx, logger, "external MCP thread relay", func(sendCtx context.Context) error {
						var (
							err     error
							relayed *events.SlackReplyTarget
						)

						relayed, err = slackRelay(sendCtx, input, outboundAttachments, slackReply, "")
						if err != nil {
							return fmt.Errorf("send Slack external MCP thread relay: %w", err)
						}

						if relayed != nil {
							slackReply = relayed
						}

						return nil
					}); err != nil {
						return externalmcp.SessionResult{}, err
					}
				}

				return submitExternalMCPInput(callCtx, submitAgent, session.Agent, session.ConversationID, input, metadata, inboundAttachments, slackReply, externalConversationID)
			}
		}

		if agent == "" {
			agent = strings.TrimSpace(defaultAgent)
		}

		if !slices.Contains(agents, agent) {
			return externalmcp.SessionResult{}, fmt.Errorf("external MCP agent %q is not exposed", agent)
		}

		publicConversationID := externalConversationID
		if publicConversationID == "" {
			publicConversationID = rand.Text()
		}

		conversationID := "external_mcp:" + agent + ":" + rand.Text()
		if err := store.UpsertExternalMCPSession(publicConversationID, harnessbridge.ExternalMCPSessionState{Agent: agent, ConversationID: conversationID}); err != nil {
			return externalmcp.SessionResult{}, fmt.Errorf("persist external MCP session mapping: %w", err)
		}

		relayText := input

		threadPrefix := ""
		for prefix, threadAgent := range cfg.ThreadAgents {
			if prefix = strings.TrimSpace(prefix); prefix != "" && strings.TrimSpace(threadAgent.Agent) == agent && (threadPrefix == "" || prefix < threadPrefix) {
				threadPrefix = prefix
			}
		}

		if threadPrefix != "" {
			relayText = threadPrefix + " " + input
		}

		logger.Info("relaying external MCP input to Slack thread root", "text_len", len(relayText))

		if err := retrySlackDelivery(callCtx, logger, "external MCP relay", func(sendCtx context.Context) error {
			var err error

			slackReply, err = slackRelay(sendCtx, relayText, outboundAttachments, nil, slackChannel)
			if err != nil {
				return fmt.Errorf("send Slack external MCP relay: %w", err)
			}

			return nil
		}); err != nil {
			return externalmcp.SessionResult{}, err
		}

		if slackReply != nil {
			slackReply.ThreadTS = slackReply.MessageTS

			threadKey := harnessbridge.SlackThreadConversationID(slackReply.ChannelID, slackReply.ThreadTS)
			if err := store.UpsertThread(threadKey, agent); err != nil {
				return externalmcp.SessionResult{}, fmt.Errorf("persist external MCP Slack thread alias: %w", err)
			}

			if err := store.MarkThreadSeeded(threadKey, conversationID); err != nil {
				return externalmcp.SessionResult{}, fmt.Errorf("persist external MCP Slack thread alias: %w", err)
			}
		}

		return submitExternalMCPInput(callCtx, submitAgent, agent, conversationID, input, metadata, inboundAttachments, slackReply, publicConversationID)
	})
	if err != nil {
		return nil, fmt.Errorf("start external MCP HTTP server: %w", err)
	}

	return server, nil
}

func externalMCPInboundAttachments(attachments []externalmcp.SessionPromptAttachment) ([]events.InboundAttachment, error) {
	if len(attachments) == 0 {
		return nil, nil
	}

	inbound := make([]events.InboundAttachment, 0, len(attachments))
	for i := range attachments {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(attachments[i].DataBase64))
		if err != nil {
			return nil, fmt.Errorf("decode external MCP attachment %d: %w", i+1, err)
		}

		inbound = append(inbound, events.InboundAttachment{Name: strings.TrimSpace(attachments[i].Name), MIMEType: strings.TrimSpace(attachments[i].MIMEType), Data: data})
	}

	return inbound, nil
}

func submitExternalMCPInput(ctx context.Context, submitAgent func(context.Context, string, string, *events.InboundMessage) error, agent, conversationID, input string, metadata map[string]string, attachments []events.InboundAttachment, slackReply *events.SlackReplyTarget, externalConversationID string) (externalmcp.SessionResult, error) {
	inbound := events.NewMainInboundMessage(events.SourceExternalMCP, events.InboundKindPrompt, "", input, true)
	inbound.Metadata = metadata
	inbound.Attachments = attachments
	inbound.HadAttachments = len(attachments) > 0
	inbound.SlackReply = slackReply
	resultCh := inbound.EnableResponseWait()

	if err := submitAgent(ctx, agent, conversationID, inbound); err != nil {
		return externalmcp.SessionResult{}, fmt.Errorf("submit external MCP input to agent %q: %w", agent, err)
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

		return externalmcp.SessionResult{ExternalConversationID: externalConversationID, Answer: result.Text}, nil
	}
}

func outboundLoop(
	ctx context.Context,
	bus *events.Bus,
	slackSend func(context.Context, *events.OutboundMessage) error,
	discordSend func(context.Context, *events.OutboundMessage) error,
	webSend func(context.Context, *events.OutboundMessage) error,
	logger *slog.Logger,
) error {
	return outboundLoopWithDiscordText(ctx, bus, slackSend, discardOutboundSend, discordSend, webSend, logger)
}

func outboundLoopWithDiscordText(
	ctx context.Context,
	bus *events.Bus,
	slackSend func(context.Context, *events.OutboundMessage) error,
	discordTextSend func(context.Context, *events.OutboundMessage) error,
	discordSend func(context.Context, *events.OutboundMessage) error,
	webSend func(context.Context, *events.OutboundMessage) error,
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
				attrs = append(attrs, "target", target, "source", delivery.msg.Source, "conversation_id", delivery.msg.ConversationID, "turn_id", delivery.msg.TurnID, "web_session_id", delivery.msg.WebSessionID, "sequence", delivery.msg.Sequence, "complete", delivery.msg.Complete, "slack_post_text", delivery.msg.SlackPostText, "text_len", len(delivery.msg.Text), "slack_text_len", len([]rune(delivery.msg.Text)), "slack_thinking_len", len([]rune(delivery.msg.SlackThinking)))
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

	discordTextDeliver := func(sendCtx context.Context, msg *events.OutboundMessage) error {
		if err := discordTextSend(sendCtx, msg); err != nil {
			return fmt.Errorf("send Discord text response: %w", err)
		}

		return nil
	}

	discordTextQueue := startWorker("discord_text", discordTextDeliver)

	discordDeliver := func(sendCtx context.Context, msg *events.OutboundMessage) error {
		err := discordSend(sendCtx, msg)
		if errors.Is(err, discordvoice.ErrPlaybackInterrupted) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("send Discord response: %w", err)
		}

		return nil
	}

	discordQueue := startWorker("discord", discordDeliver)

	webDeliver := func(sendCtx context.Context, msg *events.OutboundMessage) error {
		if err := webSend(sendCtx, msg); err != nil {
			return fmt.Errorf("send web UI response: %w", err)
		}

		return nil
	}

	webQueue := startWorker("web_ui", webDeliver)

	defer func() {
		for _, queue := range []chan outboundTargetDelivery{slackQueue, discordTextQueue, discordQueue, webQueue} {
			close(queue)
		}
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

		if slices.Contains(msg.Targets, events.OutputTargetDiscordText) {
			pending++

			dispatch(discordTextQueue, msg, notify)
		}

		if slices.Contains(msg.Targets, events.OutputTargetDiscord) || discordQueue != nil && msg.Source == events.SourceDiscordVoice && msg.SlackThinking != "" {
			pending++

			dispatch(discordQueue, msg, notify)
		}

		if slices.Contains(msg.Targets, events.OutputTargetWebUI) {
			pending++

			dispatch(webQueue, msg, notify)
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
