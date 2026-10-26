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
	"google.golang.org/protobuf/encoding/protojson"
)

// tabs saves the caller's tab state, changes one space, adds, removes, swaps, or edits Shared tabs, or mutes or
// unmutes one session tab for the caller, then returns every space, the caller's saved state, every Shared
// tab, and the caller's live mutes. New session and sessions people may not open are never shared or muted.
func (s *Server) tabs(ctx context.Context, request *TabsRequest) (*TabsResponse, error) {
	username, _, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}

	saved, refused, unshared, err := s.changeTabs(ctx, username, request)
	if errors.Is(err, backend.ErrSpaceRejected) {
		err = status.Error(codes.InvalidArgument, backend.ErrSpaceRejected.Error())
	}

	// A save answers with the state it saved; anything else, a refused save included, reads it.
	var (
		spaces   []backend.Space
		shared   []backend.SharedTab
		mutes    []backend.TabMute
		state    backend.TabState
		response = &TabsResponse{Stale: (request.State != nil && !saved) || refused}
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
		var errState error

		state, response.Revision, err = s.sessions.TabState(ctx, username)
		response.State, errState = tabState(state)
		err = errors.Join(err, errState)
	}

	if err == nil {
		response.Shared, err = sharedTabs(shared)
	}

	if err == nil {
		response.Unshared, err = searchPage(unshared)
	}

	if err != nil {
		return nil, fmt.Errorf("web tabs: %w", err)
	}

	for _, space := range spaces {
		response.Spaces = append(response.Spaces, &Space{Id: space.ID, Name: space.Name})
	}

	response.Mutes = tabMutes(mutes)

	return response, nil
}

// changeTabs makes request's one change for username. It reports whether a save was made from the saved revision,
// whether an edit of a Shared Search page was refused because the page changed since the edit's base, and the page a
// Search tab taken out of Shared held.
func (s *Server) changeTabs(ctx context.Context, username string, request *TabsRequest) (saved, refused bool, unshared []byte, err error) {
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
			err = s.sessions.ShareTab(ctx, request.Share.Space, request.Share.Path, searchJSON(request.Share.Search))
		}
	case request.EditShared != nil:
		var edited bool

		edited, err = s.sessions.EditSharedSearch(ctx, request.EditShared.Space, request.EditShared.Path, searchJSON(request.EditShared.Base), searchJSON(request.EditShared.Search))
		refused = !edited
	case request.Unshare != nil:
		unshared, err = s.sessions.UnshareTab(ctx, request.Unshare.Space, request.Unshare.Path)
	case request.SwapShared != nil:
		err = s.sessions.SwapSharedTabs(ctx, request.SwapShared.Space, request.SwapShared.Path, request.SwapShared.With)
	case request.Mute != nil:
		err = s.muteTab(ctx, username, request.Mute)
	case request.Unmute != "":
		err = s.sessions.UnmuteTab(ctx, username, request.Unmute)
	}

	if err != nil {
		return saved, refused, unshared, fmt.Errorf("change tabs: %w", err)
	}

	return saved, refused, unshared, nil
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

// sharedTabs is every space's Shared tabs as the Web reads them.
func sharedTabs(shared []backend.SharedTab) ([]*SharedTab, error) {
	var err error

	out := make([]*SharedTab, 0, len(shared))
	for _, tab := range shared {
		page, errPage := searchPage(tab.Search)
		err = errors.Join(err, errPage)

		out = append(out, &SharedTab{Space: tab.Space, Path: tab.Path, Search: page})
	}

	return out, err
}

// tabState is the backend's saved state as the Web reads it.
func tabState(state backend.TabState) (out *TabState, err error) {
	out = &TabState{Space: state.Space, Last: state.Last}
	for _, tab := range state.Tabs {
		page, errPage := searchPage(tab.Search)
		err = errors.Join(err, errPage)

		out.Tabs = append(out.Tabs, &Tab{Path: tab.Path, Preview: tab.Preview, Group: tab.Group, Space: tab.Space, Search: page})
	}

	for _, group := range state.Groups {
		out.Groups = append(out.Groups, &TabGroup{Id: group.ID, Name: group.Name, Color: group.Color, Collapsed: group.Collapsed})
	}

	return out, err
}

// savedTabState is state as the backend saves it.
func savedTabState(state *TabState) *backend.TabState {
	saved := &backend.TabState{Space: state.Space, Last: state.Last}
	for _, tab := range state.Tabs {
		saved.Tabs = append(saved.Tabs, backend.Tab{Path: tab.Path, Preview: tab.Preview, Group: tab.Group, Space: tab.Space, Search: searchJSON(tab.Search)})
	}

	for _, group := range state.Groups {
		saved.Groups = append(saved.Groups, backend.TabGroup{ID: group.Id, Name: group.Name, Color: group.Color, Collapsed: group.Collapsed})
	}

	return saved
}

// searchPage reads a Search page the backend saved as JSON, nil for other tabs.
func searchPage(saved []byte) (*SearchPage, error) {
	if saved == nil {
		return nil, nil
	}

	page := &SearchPage{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(saved, page); err != nil {
		return nil, fmt.Errorf("read search page: %w", err)
	}

	return page, nil
}

// searchJSON is page as the backend saves it, nil for other tabs. The page came from a request's protobuf JSON,
// so it marshals back without error.
func searchJSON(page *SearchPage) []byte {
	if page == nil {
		return nil
	}

	saved, _ := protojson.Marshal(page)

	return saved
}
