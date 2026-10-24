package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// tabs saves the caller's tab state, changes one space, adds, removes, or swaps Shared tabs, or mutes or
// unmutes one session tab for the caller, then returns every space, the caller's saved state, every Shared
// tab, and the caller's live mutes. New session and sessions people may not open are never shared or muted.
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
	case request.Share != nil && request.Share.Path == "/":
		err = status.Error(codes.InvalidArgument, "New session cannot be shared")
	case request.Share != nil:
		// Every person's Web decodes a shared path to label its tab, so one it cannot decode is refused.
		if path, errPath := url.PathUnescape(request.Share.Path); errPath != nil || !utf8.ValidString(path) {
			err = status.Error(codes.InvalidArgument, "tab path is not a valid escaped path")
		} else if id := backend.TabConversation(request.Share.Path); id != "" {
			err = s.visibleConversation(ctx, id)
		}

		if err == nil {
			err = s.sessions.ShareTab(ctx, request.Share.Space, request.Share.Path)
		}
	case request.Unshare != nil:
		err = s.sessions.UnshareTab(ctx, request.Unshare.Space, request.Unshare.Path)
	case request.SwapShared != nil:
		err = s.sessions.SwapSharedTabs(ctx, request.SwapShared.Space, request.SwapShared.Path, request.SwapShared.With)
	case request.Mute != nil:
		err = s.muteTab(ctx, username, request.Mute)
	case request.Unmute != "":
		err = s.sessions.UnmuteTab(ctx, username, request.Unmute)
	}

	if errors.Is(err, backend.ErrSpaceRejected) {
		err = status.Error(codes.InvalidArgument, backend.ErrSpaceRejected.Error())
	}

	// A save answers with the state it saved; anything else, a refused save included, reads it.
	var (
		spaces   []backend.Space
		shared   []backend.SharedTab
		mutes    []backend.TabMute
		state    backend.TabState
		response = &TabsResponse{Stale: request.State != nil && !saved}
	)

	if saved {
		response.State, response.Revision = request.State, request.Revision+1
	}

	if err == nil {
		spaces, err = s.sessions.Spaces(ctx)
	}

	if err == nil {
		shared, err = s.sessions.SharedTabs(ctx)
	}

	if err == nil {
		mutes, err = s.sessions.TabMutes(ctx, username)
	}

	if err == nil && !saved {
		state, response.Revision, err = s.sessions.TabState(ctx, username)
		response.State = &TabState{Space: state.Space, Last: state.Last}

		for _, tab := range state.Tabs {
			response.State.Tabs = append(response.State.Tabs, &Tab{Path: tab.Path, Preview: tab.Preview, Group: tab.Group, Space: tab.Space})
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

	for _, tab := range shared {
		response.Shared = append(response.Shared, &SharedTab{Space: tab.Space, Path: tab.Path})
	}

	response.Mutes = tabMutes(mutes)

	return response, nil
}

// muteTab mutes mute's session for username until its until, or until unmuted when until is empty.
// Only a session people may open can be muted. It is apart from tabs, whose branches would pass gocyclo's limit.
func (s *Server) muteTab(ctx context.Context, username string, mute *TabMute) error {
	var until *time.Time

	if mute.Until != "" {
		parsed, err := time.Parse(time.RFC3339, mute.Until)
		if err != nil {
			return fmt.Errorf("web tab mute: %w", status.Error(codes.InvalidArgument, "mute until is not an RFC 3339 time"))
		}

		until = &parsed
	}

	if err := s.visibleConversation(ctx, backend.TabConversation(mute.Path)); err != nil {
		return err
	}

	if err := s.sessions.MuteTab(ctx, username, mute.Path, until); err != nil {
		return fmt.Errorf("web tab mute: %w", err)
	}

	return nil
}

// tabMutes is mutes as the Web reads them, with each snooze's end as an RFC 3339 time in UTC.
func tabMutes(mutes []backend.TabMute) []*TabMute {
	out := make([]*TabMute, 0, len(mutes))

	for _, mute := range mutes {
		until := ""
		if mute.Until != nil {
			until = mute.Until.UTC().Format(time.RFC3339)
		}

		out = append(out, &TabMute{Path: mute.Path, Until: until})
	}

	return out
}

// savedTabState is state as the backend saves it.
func savedTabState(state *TabState) *backend.TabState {
	saved := &backend.TabState{Space: state.Space, Last: state.Last}
	for _, tab := range state.Tabs {
		saved.Tabs = append(saved.Tabs, backend.Tab{Path: tab.Path, Preview: tab.Preview, Group: tab.Group, Space: tab.Space})
	}

	for _, group := range state.Groups {
		saved.Groups = append(saved.Groups, backend.TabGroup{ID: group.Id, Name: group.Name, Color: group.Color, Collapsed: group.Collapsed})
	}

	return saved
}
