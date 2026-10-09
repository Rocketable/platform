package rpc

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"unicode"

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

	principal, _, err := s.principal(ctx)
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

func (s *Server) stageRevert(ctx context.Context, request *StageRevertRequest) (*StageRevertResponse, error) {
	if err := s.visibleConversation(ctx, request.Id); err != nil {
		return nil, err
	}

	marker, text, err := s.backend.StageRevert(ctx, request.Id, request.MessageId)
	if err != nil {
		return nil, fmt.Errorf("web revert: %w", err)
	}

	response := &StageRevertResponse{RevertMessageId: marker}
	// Undo at the beginning changes neither history nor the owning composer.
	if text == "" && request.MessageId == "" {
		return response, nil
	}

	response.Prompt, err = s.inputEvent(ctx, request.Id, text)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func (s *Server) clearRevert(ctx context.Context, request *ClearRevertRequest) (*ClearRevertResponse, error) {
	if err := s.visibleConversation(ctx, request.Id); err != nil {
		return nil, err
	}

	if err := s.backend.ClearRevert(ctx, request.Id); err != nil {
		return nil, fmt.Errorf("web redo: %w", err)
	}

	return &ClearRevertResponse{}, nil
}

func (s *Server) searchMessages(ctx context.Context, request *SearchMessagesRequest) (*SearchMessagesResponse, error) {
	if _, _, err := s.principal(ctx); err != nil {
		return nil, err
	}

	response := &SearchMessagesResponse{}

	needle := strings.ToLower(strings.TrimSpace(request.Query))
	if needle == "" {
		return response, nil
	}

	found, err := s.sharedSearch(ctx, needle, func(scanCtx context.Context) (any, error) {
		hits, mentions, complete, err := s.sessions.SearchMessagesMentioning(scanCtx, needle, s.channels)
		if err != nil {
			return nil, fmt.Errorf("web message search: %w", err)
		}

		response.Matches = messageMatches(hits)
		response.TagIds = backend.MentionedIDs(hits, mentions)
		response.IndexComplete = complete

		return response, nil
	})
	if err != nil {
		return nil, err
	}

	return found.(*SearchMessagesResponse), nil
}

func (s *Server) searchSessions(ctx context.Context, request *SearchSessionsRequest) (*SearchSessionsResponse, error) {
	if _, _, err := s.principal(ctx); err != nil {
		return nil, err
	}

	// Leading whitespace moves term offsets, so only trailing whitespace is dropped.
	query := strings.TrimRightFunc(request.Query, unicode.IsSpace)

	// A leading space keeps this key apart from SearchMessages' trimmed needles.
	found, err := s.sharedSearch(ctx, fmt.Sprintf(" %t %s", request.Messages, query), func(scanCtx context.Context) (any, error) {
		search, err := s.sessions.SearchSessions(scanCtx, query, request.Messages, s.channels)
		if err != nil {
			return nil, fmt.Errorf("web session search: %w", err)
		}

		response := &SearchSessionsResponse{Text: search.Text, Needle: search.Needle, Messages: messageMatches(search.Hits), MentionIds: search.MentionIDs, IndexComplete: search.IndexComplete, SummariesComplete: search.SummariesComplete}
		for _, term := range search.Terms {
			response.Terms = append(response.Terms, &SearchTerm{Key: string(term.Key), Text: term.Text, Start: int32(term.Start), End: int32(term.End)})
		}

		for i := range search.Matches {
			match := &search.Matches[i]
			response.Matches = append(response.Matches, &SessionMatch{ConversationId: match.Chat.Session.Conversation.ID, Field: string(match.Field), Text: match.Text})
		}

		return response, nil
	})
	if err != nil {
		return nil, err
	}

	return found.(*SearchSessionsResponse), nil
}

func messageMatches(hits []backend.MessageSearchHit) []*MessageMatch {
	matches := make([]*MessageMatch, 0, len(hits))
	for _, hit := range hits {
		matches = append(matches, &MessageMatch{ConversationId: hit.ConversationID, Message: &TranscriptEvent{Role: hit.Role, Text: hit.Text, MessageId: hit.MessageID}})
	}

	return matches
}

// sharedSearch runs search once for the callers waiting on key and stops it only
// when all of them stop waiting. All authenticated Web callers share the same
// humanConversation visibility set.
func (s *Server) sharedSearch(ctx context.Context, key string, search func(context.Context) (any, error)) (any, error) {
	s.searchMu.Lock()
	scanCtx := ctx

	flight := s.searches[key]
	if flight != nil {
		select {
		case <-flight.done:
			s.searchGroup.Forget(key)

			flight = nil
		default:
		}
	}

	if flight == nil {
		var cancel context.CancelFunc

		scanCtx, cancel = context.WithCancel(context.WithoutCancel(ctx))

		flight = &searchFlight{cancel: cancel, done: make(chan struct{})}
		if s.searches == nil {
			s.searches = make(map[string]*searchFlight)
		}

		s.searches[key] = flight
	}

	flight.waiters++
	result := s.searchGroup.DoChan(key, func() (any, error) {
		defer close(flight.done)

		return search(scanCtx)
	})
	s.searchMu.Unlock()

	select {
	case <-ctx.Done():
		s.searchMu.Lock()

		flight.waiters--
		if flight.waiters == 0 {
			if s.searches[key] == flight {
				delete(s.searches, key)
				s.searchGroup.Forget(key)
			}

			flight.cancel()
		}
		s.searchMu.Unlock()

		return nil, fmt.Errorf("wait for search: %w", ctx.Err())
	case outcome := <-result:
		s.searchMu.Lock()

		flight.waiters--
		if s.searches[key] == flight {
			delete(s.searches, key)
		}

		if flight.waiters == 0 {
			flight.cancel()
		}
		s.searchMu.Unlock()

		return outcome.Val, outcome.Err
	}
}

func (s *Server) slackNames(ctx context.Context, request *SlackNamesRequest) (*SlackNamesResponse, error) {
	if _, _, err := s.principal(ctx); err != nil {
		return nil, err
	}

	return &SlackNamesResponse{Names: s.channels.SlackNames(ctx, request.GetIds())}, nil
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

	return &HandoffResponse{Document: fmt.Sprintf("# Session handoff\n\nSource session: %s\n\nFor more details, call `rocketclaw_get_session` inside Execute with `conversation_id` set to the source session ID above.\n\n%s", request.Id, document)}, nil
}
