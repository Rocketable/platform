package rpc

import (
	"context"
	"errors"
	"fmt"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
		saved, err = s.sessions.SaveTabState(ctx, username, savedTabState(request.State), request.Revision)
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
		state    backend.TabState
		response = &TabsResponse{Stale: request.State != nil && !saved}
	)

	if saved {
		response.State, response.Revision = request.State, request.Revision+1
	}

	if err == nil {
		spaces, err = s.sessions.Spaces(ctx)
	}

	if err == nil && !saved {
		state, response.Revision, err = s.sessions.TabState(ctx, username)
		response.State = &TabState{Space: state.Space, Last: state.Last}

		for _, tab := range state.Tabs {
			response.State.Tabs = append(response.State.Tabs, &Tab{Path: tab.Path, Pinned: tab.Pinned, Preview: tab.Preview, Group: tab.Group, Space: tab.Space})
		}

		for _, group := range state.Groups {
			response.State.Groups = append(response.State.Groups, &TabGroup{Id: group.ID, Name: group.Name, Color: group.Color, Collapsed: group.Collapsed})
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

// savedTabState is state as the backend saves it.
func savedTabState(state *TabState) *backend.TabState {
	saved := &backend.TabState{Space: state.Space, Last: state.Last}
	for _, tab := range state.Tabs {
		saved.Tabs = append(saved.Tabs, backend.Tab{Path: tab.Path, Pinned: tab.Pinned, Preview: tab.Preview, Group: tab.Group, Space: tab.Space})
	}

	for _, group := range state.Groups {
		saved.Groups = append(saved.Groups, backend.TabGroup{ID: group.Id, Name: group.Name, Color: group.Color, Collapsed: group.Collapsed})
	}

	return saved
}
