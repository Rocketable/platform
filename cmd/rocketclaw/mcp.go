package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/externalmcp"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func startExternalMCPServer(
	ctx context.Context,
	cfg *config.Config,
	startThread func(context.Context, string, string, string) (string, error),
	users map[string]string,
	agentExposed func(string) bool,
	store *backend.SessionService,
	rt frontend.Backend,
	logger *slog.Logger,
) (*externalmcp.Server, error) {
	locks := backend.NewKeyedConversationLocks()

	server, err := externalmcp.StartSessionPromptServer(ctx, logger, cfg.MCPExternal.ListenAddr, users, func(callCtx context.Context, _, externalConversationID, requestedAgent, input string, _ map[string]string, attachments []externalmcp.SessionAttachment, slackChannel string) (result externalmcp.SessionResult, err error) {
		var (
			createdConversationID string
			durableRegistration   bool
			promptAccepted        bool
		)

		defer func() {
			if err != nil && createdConversationID != "" {
				cleanupFailedExternalMCPConversation(store, logger, externalConversationID, createdConversationID, durableRegistration, promptAccepted)
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

		channelAgents := cfg.Slack.Channels[channelIndex].Agents
		managedAgent := channelAgents[0]

		inboundContent, err := externalMCPInboundContent(attachments)
		if err != nil {
			return externalmcp.SessionResult{}, err
		}

		if strings.TrimSpace(input) == "" && len(attachments) == 0 {
			return externalmcp.SessionResult{}, errors.New("external MCP turn requires input or attachments")
		}

		promptParts := make([]string, 0, 1+len(inboundContent.TextAttachments)+len(inboundContent.AttachmentWarnings))
		if strings.TrimSpace(input) != "" {
			promptParts = append(promptParts, input)
		}
		promptParts = append(promptParts, inboundContent.TextAttachments...)
		promptParts = append(promptParts, inboundContent.AttachmentWarnings...)
		prompt := strings.Join(promptParts, "\n\n")

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

			if slackChannel != strings.TrimSpace(session.SlackChannel) {
				return externalmcp.SessionResult{}, fmt.Errorf("external_conversation_id %q is bound to Slack channel %q", externalConversationID, session.SlackChannel)
			}

			if !agentExposed(usedAgent) {
				return externalmcp.SessionResult{}, fmt.Errorf("external MCP agent %q is not exposed", usedAgent)
			}

			conversationID := session.PrivateConversationID
			if conversationID == "" {
				conversationID = session.ManagedConversationID
			}

			promptAccepted = true
			if err := rt.RunTurn(callCtx, &protocol.TurnRequest{ID: conversationID, Kind: protocol.TurnPrompt, Text: prompt, Agent: usedAgent}); err != nil {
				return externalmcp.SessionResult{}, err
			}

			if err := syncMCPPair(callCtx, rt, store, usedAgent, channelAgents, conversationID, session.ManagedConversationID); err != nil {
				return externalmcp.SessionResult{}, err
			}

			return sessionPromptResult(callCtx, store, externalConversationID, usedAgent, conversationID)
		}

		usedAgent := requestedAgent

		if !agentExposed(usedAgent) {
			return externalmcp.SessionResult{}, fmt.Errorf("external MCP agent %q is not exposed", usedAgent)
		}

		privateConversationID := "external_mcp:" + usedAgent + ":" + rand.Text()

		managedConversationID, err := startThread(callCtx, slackChannel, externalConversationID, prompt)
		if err != nil {
			return externalmcp.SessionResult{}, err
		}

		createdConversationID = managedConversationID
		if err := store.RegisterExternalMCPConversation(externalConversationID, managedAgent, &backend.ExternalMCPSessionState{Agent: usedAgent, PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID, SlackChannel: slackChannel}); err != nil {
			return externalmcp.SessionResult{}, fmt.Errorf("persist external MCP conversation: %w", err)
		}

		durableRegistration = true

		if err := rt.CreateConversation(privateConversationID, []string{usedAgent}, nil); err != nil {
			return externalmcp.SessionResult{}, err
		}

		if err := rt.CreateConversation(managedConversationID, channelAgents, []protocol.ConversationTag{protocol.ConversationUserFacing}); err != nil {
			return externalmcp.SessionResult{}, err
		}

		promptAccepted = true
		if err := rt.RunTurn(callCtx, &protocol.TurnRequest{ID: privateConversationID, Kind: protocol.TurnPrompt, Text: prompt, Agent: usedAgent}); err != nil {
			return externalmcp.SessionResult{}, err
		}

		if err := syncMCPPair(callCtx, rt, store, usedAgent, channelAgents, privateConversationID, managedConversationID); err != nil {
			return externalmcp.SessionResult{}, err
		}

		return sessionPromptResult(callCtx, store, externalConversationID, usedAgent, privateConversationID)
	})
	if err != nil {
		return nil, fmt.Errorf("start external MCP HTTP server: %w", err)
	}

	return server, nil
}

func syncMCPPair(ctx context.Context, rt frontend.Backend, store *backend.SessionService, lockedAgent string, channelAgents []string, src, dst string) error {
	if _, ok, err := store.Thread(src); err != nil {
		return err
	} else if !ok {
		if err := rt.CreateConversation(src, []string{lockedAgent}, nil); err != nil {
			return err
		}
	}

	if _, ok, err := store.Thread(dst); err != nil {
		return err
	} else if !ok {
		if err := rt.CreateConversation(dst, channelAgents, []protocol.ConversationTag{protocol.ConversationUserFacing}); err != nil {
			return err
		}
	}

	return rt.SyncConversation(ctx, src, dst)
}

func unionMCPPairX(listed []protocol.ConversationRecord, pairs map[string]backend.ExternalMCPSessionState) []protocol.ConversationRecord {
	seen := make(map[string]struct{}, len(listed))
	for _, rec := range listed {
		seen[rec.ID] = struct{}{}
	}

	for _, session := range pairs {
		x := strings.TrimSpace(session.PrivateConversationID)
		if x == "" {
			continue
		}
		if _, ok := seen[x]; ok {
			continue
		}

		listed = append(listed, protocol.ConversationRecord{ID: x, Agents: []string{session.Agent}})
		seen[x] = struct{}{}
	}

	return listed
}

func sessionPromptResult(ctx context.Context, store *backend.SessionService, externalConversationID, usedAgent, lockedID string) (externalmcp.SessionResult, error) {
	summaries, err := store.ListSessions(ctx, &backend.SessionListOptions{IDs: []string{lockedID}})
	if err != nil {
		return externalmcp.SessionResult{}, err
	}

	answer := ""
	if len(summaries) > 0 {
		answer = summaries[0].LastAssistantMessage
	}

	return externalmcp.SessionResult{ExternalConversationID: externalConversationID, Agent: usedAgent, Answer: answer}, nil
}

func cleanupFailedExternalMCPConversation(store *backend.SessionService, logger *slog.Logger, externalConversationID, conversationID string, durableRegistration, promptAccepted bool) {
	if promptAccepted || !durableRegistration {
		return
	}

	if err := store.RemoveExternalMCPConversation(externalConversationID); err != nil {
		logger.Error("clean failed external MCP conversation", "external_conversation_id", externalConversationID, "conversation_id", conversationID, "error", err)
	}
}

func externalMCPInboundContent(attachments []externalmcp.SessionAttachment) (protocol.InboundContent, error) {
	if len(attachments) == 0 {
		return protocol.InboundContent{}, nil
	}

	var content protocol.InboundContent

	for i := range attachments {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(attachments[i].DataBase64))
		if err != nil {
			return protocol.InboundContent{}, fmt.Errorf("decode external MCP attachment %d: %w", i+1, err)
		}

		name := strings.TrimSpace(attachments[i].Name)
		mimeType := strings.TrimSpace(attachments[i].MIMEType)

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
		}
	}

	return content, nil
}
