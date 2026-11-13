package rpc

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
)

// typingEventsBuffered is how many reports a watch holds while it sends earlier ones. A report beyond them is
// dropped, as the typist's next one comes within seconds.
const typingEventsBuffered = 16

// typingVisible admits a typing call on conversation id by the rules for viewing it. An OpenCode session, which
// may have no stored row, is closed to people limited to spaces; any other conversation must be visible.
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

// reportTyping passes the caller's display name and typing state to every other person watching the
// conversation, never waiting for a watch that is behind. A browser tab's reports travel independently, so one may
// arrive after a later one; it is dropped. Reports leave nothing behind on a conversation nobody watches.
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

// watchTyping streams other people's typing reports on the conversation until the stream ends, after an empty
// event once the watch listens.
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
