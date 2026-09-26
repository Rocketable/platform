package rpc

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) forkSession(ctx context.Context, request *ForkSessionRequest) (*ForkSessionResponse, error) {
	if err := s.visibleConversation(ctx, request.Id); err != nil {
		return nil, err
	}

	history, err := s.history(ctx, &HistoryRequest{Id: request.Id})
	if err != nil {
		return nil, err
	}

	if request.Before != "" && !slices.ContainsFunc(history.Messages, func(message *TranscriptEvent) bool {
		return message.MessageId == request.Before && message.Role == "user"
	}) {
		return nil, fmt.Errorf("web fork: %w", status.Error(codes.InvalidArgument, "select a recorded user message"))
	}

	thread, recorded, err := s.sessions.Thread(request.Id)
	if err != nil {
		return nil, fmt.Errorf("read fork source: %w", err)
	}

	if !recorded {
		return nil, fmt.Errorf("web session: %w", status.Error(codes.NotFound, "session is not recorded"))
	}

	choices, err := s.agentChoices(ctx, request.Id)
	if err != nil {
		return nil, err
	}

	if !slices.Contains(choices, thread.Agent) {
		return nil, fmt.Errorf("web fork: %w", status.Error(codes.FailedPrecondition, "session agent is no longer available"))
	}

	principal, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}

	id := rand.Text()

	prompt, err := s.sessions.ForkConversation(ctx, request.Id, protocol.Conversation{ID: id, Agent: thread.Agent, CreatedBy: principal}, request.Before)
	if err != nil {
		return nil, fmt.Errorf("fork web session: %w", err)
	}

	event, err := s.inputEvent(ctx, id, prompt)
	if err != nil {
		return nil, err
	}

	return &ForkSessionResponse{Id: id, Prompt: event}, nil
}

func (s *Server) searchMessages(ctx context.Context, request *SearchMessagesRequest) (*SearchMessagesResponse, error) {
	if _, err := s.principal(ctx); err != nil {
		return nil, err
	}

	response := &SearchMessagesResponse{}

	needle := strings.ToLower(strings.TrimSpace(request.Query))
	if needle == "" {
		return response, nil
	}

	conversations, err := s.backend.ListConversations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list message search sessions: %w", err)
	}
	// Ponytail: scans recorded transcripts; add a message index if volume demands it.
	for _, conversation := range conversations {
		visible, err := s.humanConversation(conversation.ID)
		if err != nil {
			return nil, err
		}

		if !visible {
			continue
		}

		history, err := s.history(ctx, &HistoryRequest{Id: conversation.ID})
		if err != nil {
			return nil, err
		}

		for _, message := range history.Messages {
			if (message.Role == "user" || message.Role == "assistant") && strings.Contains(strings.ToLower(message.Text), needle) {
				response.Matches = append(response.Matches, &MessageMatch{ConversationId: conversation.ID, Message: message})
			}
		}
	}

	return response, nil
}

func (s *Server) handoff(ctx context.Context, request *HandoffRequest) (*HandoffResponse, error) {
	if err := s.visibleConversation(ctx, request.Id); err != nil {
		return nil, err
	}

	history, err := s.history(ctx, &HistoryRequest{Id: request.Id})
	if err != nil {
		return nil, err
	}

	thread, recorded, err := s.sessions.Thread(request.Id)
	if err != nil {
		return nil, fmt.Errorf("read handoff source: %w", err)
	}

	if !recorded {
		return nil, fmt.Errorf("web session: %w", status.Error(codes.NotFound, "session is not recorded"))
	}

	var transcript strings.Builder
	fmt.Fprintf(&transcript, "Source session: %s\n\n", request.Id)

	for _, message := range history.Messages {
		fmt.Fprintf(&transcript, "## %s\n%s\n\n", message.Role, message.Text)

		for _, file := range message.Attachments {
			fmt.Fprintf(&transcript, "Attachment: %s (%s), source session %s\n", file.Name, file.Id, file.ConversationId)
		}
	}

	document, err := backend.GenerateHandoff(ctx, s.cfg, thread.Agent, transcript.String())
	if err != nil {
		return nil, fmt.Errorf("generate web handoff: %w", err)
	}

	return &HandoffResponse{Document: fmt.Sprintf("# Session handoff\n\nSource session: %s\n\nFor more details, use `rocketclaw_get_session` with `conversation_id` set to the source session ID above.\n\n%s", request.Id, document)}, nil
}
