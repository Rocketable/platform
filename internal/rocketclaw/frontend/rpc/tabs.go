package rpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// tabs saves the caller's tab state or changes one space, then returns every space and the caller's saved state.
func (s *Server) tabs(ctx context.Context, request *TabsRequest) (*TabsResponse, error) {
	username, _, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}

	var saved bool

	switch {
	case request.State != nil:
		var state []byte
		if state, err = proto.Marshal(request.State); err == nil {
			saved, err = s.sessions.SaveTabState(ctx, username, state, request.Revision)
		}
	case request.CreateSpace != "":
		err = s.sessions.CreateSpace(ctx, request.CreateSpace)
	case request.RenameSpace != nil:
		err = s.sessions.RenameSpace(ctx, request.RenameSpace.Id, request.RenameSpace.Name)
	case request.DeleteSpace != "":
		err = s.sessions.DeleteSpace(ctx, request.DeleteSpace)
	}

	if errors.Is(err, backend.ErrSpaceRejected) {
		err = status.Error(codes.InvalidArgument, backend.ErrSpaceRejected.Error())
	}

	// A save answers with the state it saved; anything else, a refused save included, reads it.
	var (
		spaces   []backend.Space
		state    []byte
		response = &TabsResponse{Stale: request.State != nil && !saved}
	)

	if saved {
		response.State, response.Revision = request.State, request.Revision+1
	}

	if err == nil {
		spaces, err = s.sessions.Spaces(ctx)
	}

	if err == nil && !saved {
		response.State = &TabState{}
		state, response.Revision, err = s.sessions.TabState(ctx, username)
		// Unreadable saved tabs read as none; the next save, made from this revision, replaces them.
		if errUnmarshal := proto.Unmarshal(state, response.State); err == nil && errUnmarshal != nil {
			slog.Warn("read saved web tabs", "username", username, "error", errUnmarshal)

			response.State = &TabState{}
		}
	}

	if err != nil {
		return nil, fmt.Errorf("web tabs: %w", err)
	}

	for _, space := range spaces {
		response.Spaces = append(response.Spaces, &Space{Id: space.ID, Name: space.Name})
	}

	return response, nil
}
