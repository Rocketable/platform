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
	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/externalmcp"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type mcpRelay interface {
	SendExternalMCPRelay(context.Context, string, string, *protocol.ExternalMCPRelay) (*protocol.SlackReplyTarget, error)
	CleanupExternalMCPRelay(context.Context, *protocol.SlackReplyTarget)
}

type mcpAgentIndex struct {
	mu    sync.Mutex
	names []string
	cfg   *config.Config
}

func (i *mcpAgentIndex) Refresh() error {
	agents, err := backend.ExternalMCPAgentsIn(i.cfg, i.cfg.RuntimeDirName())
	if err != nil {
		return fmt.Errorf("load external MCP agents: %w", err)
	}
	i.mu.Lock()
	i.names = agents
	i.mu.Unlock()
	return nil
}

func (i *mcpAgentIndex) Exposed(agent string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return slices.Contains(i.names, agent)
}

func startExternalMCPServer(
	ctx context.Context,
	cfg *config.Config,
	relay mcpRelay,
	users map[string]string,
	agents *mcpAgentIndex,
	store *backend.SessionService,
	turns frontend.Backend,
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
				cleanupFailedExternalMCPConversation(relay, store, logger, reply, externalConversationID, createdConversationID, durableRegistration, promptAccepted)
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

			if !agents.Exposed(usedAgent) {
				return externalmcp.SessionResult{}, fmt.Errorf("external MCP agent %q is not exposed", usedAgent)
			}

			conversationID := session.PrivateConversationID
			target, err := relay.SendExternalMCPRelay(callCtx, channelID, threadTS, &protocol.ExternalMCPRelay{ConversationID: conversationID, ExternalConversationID: externalConversationID, Agent: usedAgent, Text: input, Attachments: outboundAttachments})
			if err != nil {
				return externalmcp.SessionResult{}, fmt.Errorf("send text connector external MCP thread relay: %w", err)
			}
			reply = &protocol.InboundMessage{SlackReply: target}

			result, _, err := submitExternalMCPInput(callCtx, turns, usedAgent, conversationID, &inboundContent, metadata, strings.TrimSpace(username), reply, externalConversationID)

			return result, err
		}

		usedAgent := requestedAgent

		if !agents.Exposed(usedAgent) {
			return externalmcp.SessionResult{}, fmt.Errorf("external MCP agent %q is not exposed", usedAgent)
		}

		privateConversationID := "external_mcp:" + usedAgent + ":" + rand.Text()

		target, err := relay.SendExternalMCPRelay(callCtx, slackChannel, "", &protocol.ExternalMCPRelay{ConversationID: privateConversationID, ExternalConversationID: externalConversationID, Agent: usedAgent, Text: input, Attachments: outboundAttachments})
		if err != nil {
			return externalmcp.SessionResult{}, err
		}
		reply = &protocol.InboundMessage{SlackReply: target}

		if reply.SlackReply == nil {
			return externalmcp.SessionResult{}, errors.New("slack external MCP relay returned no reply target")
		}

		reply.SlackReply.ThreadTS = reply.SlackReply.MessageTS

		managedConversationID := protocol.SlackThreadConversationID(reply.SlackReply.ChannelID, reply.SlackReply.ThreadTS)

		createdConversationID = managedConversationID
		if err := store.RegisterExternalMCPConversation(externalConversationID, managedAgent, &backend.ExternalMCPSessionState{Agent: usedAgent, PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID, SlackChannel: slackChannel}); err != nil {
			return externalmcp.SessionResult{}, fmt.Errorf("persist external MCP conversation: %w", err)
		}

		durableRegistration = true

		result, promptAccepted, err = submitExternalMCPInput(callCtx, turns, usedAgent, privateConversationID, &inboundContent, metadata, strings.TrimSpace(username), reply, externalConversationID)

		return result, err
	})
	if err != nil {
		return nil, fmt.Errorf("start external MCP HTTP server: %w", err)
	}

	return server, nil
}

func cleanupFailedExternalMCPConversation(relay mcpRelay, store *backend.SessionService, logger *slog.Logger, reply *protocol.InboundMessage, externalConversationID, conversationID string, durableRegistration, promptAccepted bool) {
	if promptAccepted {
		return
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	relay.CleanupExternalMCPRelay(cleanupCtx, reply.SlackReply)

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

func submitExternalMCPInput(ctx context.Context, turns frontend.Backend, usedAgent, conversationID string, content *protocol.InboundContent, metadata map[string]string, principal string, reply *protocol.InboundMessage, externalConversationID string) (externalmcp.SessionResult, bool, error) {
	inbound := protocol.NewInboundMessageFromContent(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "", content, true)

	inbound.Metadata = maps.Clone(metadata)
	delete(inbound.Metadata, protocol.InboundOriginMetadataKey)
	delete(inbound.Metadata, protocol.InboundMediaMetadataKey)
	delete(inbound.Metadata, protocol.InboundPrincipalMetadataKey)

	principal = strings.TrimSpace(principal)
	externalConversationID = strings.TrimSpace(externalConversationID)
	if principal != "" || externalConversationID != "" {
		if inbound.Metadata == nil {
			inbound.Metadata = map[string]string{}
		}
		if principal != "" {
			inbound.Metadata[protocol.InboundPrincipalMetadataKey] = principal
		}
		if externalConversationID != "" {
			inbound.Metadata["external_conversation_id"] = externalConversationID
		}
	}

	if reply != nil {
		inbound.SlackReply = reply.SlackReply
		inbound.SyncDestination = protocol.SlackThreadConversationID(reply.SlackReply.ChannelID, reply.SlackReply.ThreadTS)
	}
	inbound.ConversationID = conversationID

	resultCh := inbound.EnableResponseWait()

	if err := turns.CreateConversation(ctx, protocol.Conversation{ID: conversationID, Agent: usedAgent}); err != nil {
		return externalmcp.SessionResult{}, false, fmt.Errorf("submit external MCP input to agent %q: %w", usedAgent, err)
	}
	errRun := turns.RunTurn(ctx, inbound)
	errSync := turns.SyncConversation(context.WithoutCancel(ctx), conversationID, inbound.SyncDestination)
	if err := errors.Join(errRun, errSync); err != nil {
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
