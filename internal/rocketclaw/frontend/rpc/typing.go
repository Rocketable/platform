package rpc

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
)

const typingEventsBuffered = 16

func (s *Server) typingVisible(ctx context.Context, id string) error {
	if !strings.HasPrefix(id, "opencode:") {
		return s.visibleConversation(ctx, id)
	}

	_, limit, err := s.spaceLimit(ctx)
	if err != nil {
		return err
	}

	if limit.Limited() {
		return fmt.Errorf("web typing: %w", errOpenCodeClosed)
	}

	return nil
}

func (s *Server) reportTyping(ctx context.Context, request *ReportTypingRequest) (*ReportTypingResponse, error) {
	if err := s.typingVisible(ctx, request.Id); err != nil {
		return nil, err
	}

	username, principal, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}

	event := &TypingEvent{Principal: principal, Typing: request.Typing}
	tab := [2]string{username, request.Tab}

	s.typingMu.Lock()
	defer s.typingMu.Unlock()

	newest, watched := s.typingNewest[request.Id]
	if !watched || request.Seq <= newest[tab] {
		return &ReportTypingResponse{}, nil
	}

	newest[tab] = request.Seq
	for events, watching := range s.typingWatchers[request.Id] {
		if watching == username {
			continue
		}

		select {
		case events <- event:
		default:
		}
	}

	return &ReportTypingResponse{}, nil
}

func (s *Server) watchTyping(request *WatchTypingRequest, stream grpc.ServerStream) error {
	ctx := stream.Context()
	if err := s.typingVisible(ctx, request.Id); err != nil {
		return err
	}

	username, _, err := s.principal(ctx)
	if err != nil {
		return err
	}

	events := make(chan *TypingEvent, typingEventsBuffered)

	s.typingMu.Lock()
	if s.typingWatchers[request.Id] == nil {
		s.typingWatchers[request.Id] = map[chan *TypingEvent]string{}
		s.typingNewest[request.Id] = map[[2]string]uint32{}
	}

	s.typingWatchers[request.Id][events] = username
	s.typingMu.Unlock()

	defer func() {
		s.typingMu.Lock()
		delete(s.typingWatchers[request.Id], events)

		if len(s.typingWatchers[request.Id]) == 0 {
			delete(s.typingWatchers, request.Id)
			delete(s.typingNewest, request.Id)
		}
		s.typingMu.Unlock()
	}()

	event := &TypingEvent{}
	for {
		if err := stream.SendMsg(event); err != nil {
			return fmt.Errorf("send web typing event: %w", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case event = <-events:
		}
	}
}
