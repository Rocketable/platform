package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/developmentmcp"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/externalmcp"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

func startExternalMCPServer(
	ctx context.Context,
	cfg *config.Config,
	textRelay func(context.Context, *protocol.ExternalMCPRelay, *protocol.InboundMessage, string) (*protocol.InboundMessage, error),
	cleanupTextRelay func(context.Context, *protocol.InboundMessage),
	users map[string]string,
	agentExposed func(string) bool,
	store *backend.SessionService,
	submitAgent func(context.Context, string, string, *protocol.InboundMessage, protocol.ActivationHook) error,
	logger *slog.Logger,
) (*externalmcp.Server, error) {
	locks := backend.NewKeyedConversationLocks()

	server, err := externalmcp.StartSessionPromptServer(ctx, logger, cfg.MCPExternal.ListenAddr, users, func(callCtx context.Context, username, externalConversationID, requestedAgent, input string, metadata map[string]string, attachments []externalmcp.SessionAttachment, slackChannel string) (result externalmcp.SessionResult, err error) {
		var (
			reply                 *protocol.InboundMessage
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

		channelIndex := slices.IndexFunc(cfg.Slack.Channels, func(channel config.SlackChannelConfig) bool {
			return channel.Channel != "@" && channel.Channel == slackChannel
		})
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

		unlockExternalConversation := locks.Lock(externalConversationID)
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

			channelID, threadTS, ok := protocol.SlackThreadTarget(session.ManagedConversationID)
			if !ok {
				return externalmcp.SessionResult{}, fmt.Errorf("external_conversation_id %q has invalid persisted managed conversation ID", externalConversationID)
			}

			persistedChannel := strings.TrimSpace(session.SlackChannel)
			if slackChannel != persistedChannel {
				return externalmcp.SessionResult{}, fmt.Errorf("external_conversation_id %q is bound to Slack channel %q", externalConversationID, session.SlackChannel)
			}

			if !agentExposed(usedAgent) {
				return externalmcp.SessionResult{}, fmt.Errorf("external MCP agent %q is not exposed", usedAgent)
			}

			reply = &protocol.InboundMessage{SlackReply: &protocol.SlackReplyTarget{ChannelID: channelID, MessageTS: threadTS, ThreadTS: threadTS}}

			conversationID := session.PrivateConversationID
			if conversationID == "" {
				conversationID = session.ManagedConversationID
			}

			if store.PairBusyFor(session.ManagedConversationID, conversationID) {
				result, _, err := submitExternalMCPInput(callCtx, submitAgent, usedAgent, conversationID, &inboundContent, metadata, strings.TrimSpace(username), nil, externalConversationID, backend.NoopActivationHook)

				return result, err
			}

			activation := func(activeCtx context.Context, inbound *protocol.InboundMessage) error {
				relayed, err := textRelay(activeCtx, &protocol.ExternalMCPRelay{ConversationID: conversationID, ExternalConversationID: externalConversationID, Agent: usedAgent, Text: input, Attachments: outboundAttachments}, reply, "")
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

		reply, err = textRelay(callCtx, &protocol.ExternalMCPRelay{ConversationID: privateConversationID, ExternalConversationID: externalConversationID, Agent: usedAgent, Text: input, Attachments: outboundAttachments}, nil, slackChannel)
		if err != nil {
			return externalmcp.SessionResult{}, err
		}

		if reply == nil || reply.SlackReply == nil {
			return externalmcp.SessionResult{}, errors.New("slack external MCP relay returned no reply target")
		}

		reply.SlackReply.ThreadTS = reply.SlackReply.MessageTS

		managedConversationID := protocol.SlackThreadConversationID(reply.SlackReply.ChannelID, reply.SlackReply.ThreadTS)

		createdConversationID = managedConversationID
		if err := store.RegisterExternalMCPConversation(externalConversationID, managedAgent, &backend.ExternalMCPSessionState{Agent: usedAgent, PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID, SlackChannel: slackChannel}); err != nil {
			return externalmcp.SessionResult{}, fmt.Errorf("persist external MCP conversation: %w", err)
		}

		durableRegistration = true

		result, promptAccepted, err = submitExternalMCPInput(callCtx, submitAgent, usedAgent, privateConversationID, &inboundContent, metadata, strings.TrimSpace(username), reply, externalConversationID, backend.NoopActivationHook)

		return result, err
	})
	if err != nil {
		return nil, fmt.Errorf("start external MCP HTTP server: %w", err)
	}

	return server, nil
}

func startDevelopmentMCP(ctx context.Context, cfg *config.Config, configPath string, overlayMu *sync.Mutex, reload, restart func(reason string) (string, error), logger *slog.Logger) (*developmentmcp.Server, error) {
	if !cfg.MCPDevelopment.Enabled {
		return nil, nil
	}

	users, err := config.LoadDevelopmentMCPUsers(configPath)
	if err != nil {
		return nil, fmt.Errorf("load development MCP auth users: %w", err)
	}

	if len(users) == 0 {
		return nil, errors.New("development MCP users are required")
	}

	var (
		chatsMu sync.Mutex
		chats   = map[string]*backend.DevelopmentChat{}
		locks   = backend.NewKeyedConversationLocks()
	)

	server, err := developmentmcp.Start(ctx, logger, cfg.MCPDevelopment.ListenAddr, users, skel.OverlaySpecs(cfg.Overlays), func(spec string) (protocol.OverlayContext, error) {
		overlayMu.Lock()
		defer overlayMu.Unlock()

		got, err := skel.ReadOverlayContext(cfg.Workspace, cfg.RuntimeDirName(), cfg.Overlays, spec)
		if err != nil {
			return protocol.OverlayContext{}, fmt.Errorf("read overlay context: %w", err)
		}

		return backend.OverlayContextFromSkel(got), nil
	}, func(baseOverlay string, files []protocol.OverlayFile) (protocol.LintResult, error) {
		overlayMu.Lock()
		defer overlayMu.Unlock()

		return backend.LintTry(cfg.Workspace, cfg.RuntimeDirName(), cfg.Overlays, baseOverlay, files, cfg, logger)
	}, func(turnCtx context.Context, baseOverlay string, files []protocol.OverlayFile, agent, prompt, conversationID string) (string, string, error) {
		overlayMu.Lock()
		defer overlayMu.Unlock()

		unlock := locks.Lock(conversationID)
		defer unlock()

		chatsMu.Lock()
		chat, ok := chats[conversationID]

		if !ok {
			chat = new(backend.DevelopmentChat)
			chats[conversationID] = chat
		}
		chatsMu.Unlock()

		return backend.RunTryTurn(turnCtx, cfg.Workspace, cfg.RuntimeDirName(), cfg.Overlays, cfg, logger, chat, baseOverlay, files, agent, prompt)
	}, reload, restart)
	if err != nil {
		return nil, fmt.Errorf("start development MCP HTTP server: %w", err)
	}

	return server, nil
}

func cleanupFailedExternalMCPConversation(cleanupTextRelay func(context.Context, *protocol.InboundMessage), store *backend.SessionService, logger *slog.Logger, reply *protocol.InboundMessage, externalConversationID, conversationID string, durableRegistration, promptAccepted bool) {
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

func externalMCPInboundContent(attachments []externalmcp.SessionAttachment) (protocol.InboundContent, []protocol.OutboundAttachment, error) {
	if len(attachments) == 0 {
		return protocol.InboundContent{}, nil, nil
	}

	var content protocol.InboundContent

	outbound := make([]protocol.OutboundAttachment, 0, len(attachments))
	for i := range attachments {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(attachments[i].DataBase64))
		if err != nil {
			return protocol.InboundContent{}, nil, fmt.Errorf("decode external MCP attachment %d: %w", i+1, err)
		}

		name := strings.TrimSpace(attachments[i].Name)
		mimeType := strings.TrimSpace(attachments[i].MIMEType)
		outbound = append(outbound, protocol.OutboundAttachment{Name: name, MIMEType: mimeType, Data: append([]byte(nil), data...)})

		if protocol.IsTextAttachment(name, mimeType) {
			descriptor := name
			if descriptor == "" {
				descriptor = "attachment"
			}

			if descriptorMIMEType := protocol.NormalizeMIMEType(mimeType); descriptorMIMEType != "" {
				descriptor += " (" + descriptorMIMEType + ")"
			}

			switch {
			case len(data) > protocol.MaxInboundTextAttachmentBytes:
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

		content.Attachments = append(content.Attachments, protocol.InboundAttachment{Name: name, MIMEType: mimeType, Data: data})
	}

	return content, outbound, nil
}

func submitExternalMCPInput(ctx context.Context, submitAgent func(context.Context, string, string, *protocol.InboundMessage, protocol.ActivationHook) error, usedAgent, conversationID string, content *protocol.InboundContent, metadata map[string]string, principal string, reply *protocol.InboundMessage, externalConversationID string, activation protocol.ActivationHook) (externalmcp.SessionResult, bool, error) {
	inbound := protocol.NewInboundMessageFromContent(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "", content, true)

	inbound.Metadata = maps.Clone(metadata)
	delete(inbound.Metadata, protocol.InboundOriginMetadataKey)
	delete(inbound.Metadata, protocol.InboundMediaMetadataKey)
	delete(inbound.Metadata, protocol.InboundPrincipalMetadataKey)

	if strings.TrimSpace(principal) != "" {
		if inbound.Metadata == nil {
			inbound.Metadata = map[string]string{}
		}

		inbound.Metadata[protocol.InboundPrincipalMetadataKey] = strings.TrimSpace(principal)
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
