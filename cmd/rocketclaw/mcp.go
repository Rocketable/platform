package main

import (
	"bytes"
	"cmp"
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
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/externalmcp"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type mcpAgentIndex struct {
	cfg *config.LockedConfig

	mu    sync.Mutex
	names []string
}

func (i *mcpAgentIndex) Refresh() error {
	agents, err := backend.ExternalMCPAgentsIn(i.cfg, i.cfg.Clone().RuntimeDirName())
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
	users map[string]string,
	agents *mcpAgentIndex,
	store *backend.SessionService,
	turns frontend.Backend,
	logger *slog.Logger,
) (*externalmcp.Server, error) {
	locks := backend.NewKeyedConversationLocks()

	server, err := externalmcp.StartSessionPromptServer(ctx, logger, cfg.MCPExternal.ListenAddr, users, func(callCtx context.Context, username, externalConversationID, requestedAgent, input string, metadata map[string]string, attachments []externalmcp.SessionAttachment) (result externalmcp.SessionResult, err error) {
		var (
			managedConversationID string
			durableRegistration   bool
			promptAccepted        bool
		)

		defer func() {
			if err == nil || !durableRegistration || promptAccepted {
				return
			}

			if err := store.RemoveExternalMCPConversation(externalConversationID); err != nil {
				logger.Error("clean failed external MCP conversation", "external_conversation_id", externalConversationID, "conversation_id", managedConversationID, "error", err)
			}
		}()

		if externalConversationID == "" {
			return externalmcp.SessionResult{}, errors.New("external MCP conversation ID is required")
		}

		if requestedAgent == "" {
			return externalmcp.SessionResult{}, errors.New("external MCP agent is required")
		}

		inboundContent, err := externalMCPInboundContent(attachments)
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

			if !agents.Exposed(usedAgent) {
				return externalmcp.SessionResult{}, fmt.Errorf("external MCP agent %q is not exposed", usedAgent)
			}

			result, _, err := submitExternalMCPInput(callCtx, turns, usedAgent, session.PrivateConversationID, session.ManagedConversationID, &inboundContent, metadata, username, externalConversationID)

			return result, err
		}

		usedAgent := requestedAgent

		if !agents.Exposed(usedAgent) {
			return externalmcp.SessionResult{}, fmt.Errorf("external MCP agent %q is not exposed", usedAgent)
		}

		privateConversationID := "external_mcp:" + usedAgent + ":" + rand.Text()
		managedConversationID = rand.Text()

		if err := store.RegisterExternalMCPConversation(externalConversationID, usedAgent, &backend.ExternalMCPSessionState{Agent: usedAgent, PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID, OriginPairs: metadata}); err != nil {
			return externalmcp.SessionResult{}, fmt.Errorf("persist external MCP conversation: %w", err)
		}

		durableRegistration = true

		logger.Info("started external MCP session", "event", "external_mcp_session_started", "external_conversation_id", externalConversationID, "conversation_id", privateConversationID, "managed_conversation_id", managedConversationID, "agent", usedAgent, "user", username)

		result, promptAccepted, err = submitExternalMCPInput(callCtx, turns, usedAgent, privateConversationID, managedConversationID, &inboundContent, metadata, username, externalConversationID)

		return result, err
	})
	if err != nil {
		return nil, fmt.Errorf("start external MCP HTTP server: %w", err)
	}

	return server, nil
}

func externalMCPInboundContent(attachments []externalmcp.SessionAttachment) (protocol.InboundContent, error) {
	var content protocol.InboundContent

	for i := range attachments {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(attachments[i].DataBase64))
		if err != nil {
			return protocol.InboundContent{}, fmt.Errorf("decode external MCP attachment %d: %w", i+1, err)
		}

		name := strings.TrimSpace(attachments[i].Name)
		mimeType := strings.TrimSpace(attachments[i].MIMEType)

		if protocol.IsTextAttachment(name, mimeType) {
			descriptor := cmp.Or(name, "attachment")

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

	return content, nil
}

func submitExternalMCPInput(ctx context.Context, turns frontend.Backend, usedAgent, conversationID, managedConversationID string, content *protocol.InboundContent, metadata map[string]string, principal, externalConversationID string) (externalmcp.SessionResult, bool, error) {
	inbound := protocol.NewInboundMessageFromContent(protocol.SourceExternalMCP, protocol.InboundKindPrompt, content, true)

	inbound.Metadata = maps.Clone(metadata)
	delete(inbound.Metadata, protocol.InboundOriginMetadataKey)
	delete(inbound.Metadata, protocol.InboundMediaMetadataKey)
	delete(inbound.Metadata, protocol.InboundPrincipalMetadataKey)
	delete(inbound.Metadata, protocol.InboundPrincipalNameMetadataKey)
	delete(inbound.Metadata, protocol.InboundPrincipalEmailMetadataKey)

	if inbound.Metadata == nil {
		inbound.Metadata = map[string]string{}
	}
	if principal != "" {
		inbound.Metadata[protocol.InboundPrincipalMetadataKey] = principal
	}
	inbound.Metadata["external_conversation_id"] = externalConversationID

	inbound.ConversationID, inbound.SyncDestination = conversationID, managedConversationID

	resultCh := inbound.EnableResponseWait()

	if err := turns.RunTurn(ctx, inbound); err != nil {
		return externalmcp.SessionResult{}, errors.Is(err, protocol.ErrBridgeStopped), fmt.Errorf("submit external MCP input to agent %q: %w", usedAgent, err)
	}

	select {
	case <-ctx.Done():
		return externalmcp.SessionResult{}, true, fmt.Errorf("wait for external MCP reply: %w", ctx.Err())
	case result := <-resultCh:
		if result.Err != nil {
			return externalmcp.SessionResult{}, true, fmt.Errorf("wait for external MCP reply: %w", result.Err)
		}

		attachments := make([]externalmcp.SessionAttachment, 0, len(result.Attachments))
		for i := range result.Attachments {
			name := cmp.Or(strings.TrimSpace(result.Attachments[i].Name), fmt.Sprintf("attachment-%d", i+1))

			attachments = append(attachments, externalmcp.SessionAttachment{Name: name, MIMEType: result.Attachments[i].MIMEType, DataBase64: base64.StdEncoding.EncodeToString(result.Attachments[i].Data)})
		}

		return externalmcp.SessionResult{ExternalConversationID: externalConversationID, Agent: usedAgent, Answer: result.Text, Attachments: attachments}, true, nil
	}
}
