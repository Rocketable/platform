package rpc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func (s *Server) tabs(ctx context.Context, request *TabsRequest) (*TabsResponse, error) {
	username, _, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}

	names := s.cfg.Clone().PermittedSpaces(username)
	if names != nil {
		if err := s.sessions.CreateLimitSpaces(ctx, names); err != nil {
			return nil, fmt.Errorf("web tabs: %w", err)
		}
	}

	limit, err := s.sessions.SpaceLimitFor(ctx, names)
	if err != nil {
		return nil, fmt.Errorf("web tabs: %w", err)
	}

	saved, refused, unshared, err := s.changeTabs(ctx, username, limit, request)
	if errors.Is(err, backend.ErrSpaceRejected) {
		err = status.Error(codes.InvalidArgument, backend.ErrSpaceRejected.Error())
	}

	// A save answers with the state it saved; anything else, a refused save included, reads it.
	var (
		spaces   []backend.Space
		shared   []backend.SharedTab
		mutes    []backend.TabMute
		shelf    []byte
		state    backend.TabState
		closed   []backend.Tab
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

	if err == nil {
		shelf, err = s.sessions.TopShelf(ctx, username)
	}

	if err == nil {
		closed, err = s.sessions.ClosedTabs(ctx, username)
	}

	if err == nil && shelf != nil {
		response.TopShelf = &TopShelf{}
		err = (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(shelf, response.TopShelf)
	}

	if err == nil && response.TopShelf != nil {
		response.TopShelf.Tabs, err = s.liveShelfTabs(ctx, response.TopShelf.Tabs)
	}

	if err == nil && !saved {
		var errState error

		state, response.Revision, err = s.sessions.TabState(ctx, username)
		response.State, errState = tabState(&state)
		err = errors.Join(err, errState)
	}

	if err == nil {
		response.Shared, err = sharedTabs(slices.DeleteFunc(shared, func(tab backend.SharedTab) bool { return !limit.AllowsSpaceID(tab.Space) }))
	}

	if err == nil {
		response.Unshared, err = searchPage(unshared)
	}

	if err == nil {
		response.Closed, err = webTabs(closed)
	}

	if err != nil {
		return nil, fmt.Errorf("web tabs: %w", err)
	}

	for _, space := range spaces {
		if limit.AllowsSpaceID(space.ID) {
			response.Spaces = append(response.Spaces, &Space{Id: space.ID, Name: space.Name})
		}
	}

	response.Mutes = tabMutes(mutes)

	return response, nil
}

// changeTabs makes request's one change for username. It reports whether a save was made from the saved revision,
// whether an edit of a Shared Search page was refused because the page changed since the edit's base, and the page a
// Search tab taken out of Shared held.
func (s *Server) changeTabs(ctx context.Context, username string, limit backend.SpaceLimit, request *TabsRequest) (saved, refused bool, unshared []byte, err error) {
	shared := cmp.Or(request.Share, request.EditShared, request.Unshare)

	switch {
	case request.State != nil:
		saved, err = s.sessions.SaveTabState(ctx, username, savedTabState(request.State), request.Revision)
	case limit.Limited() && (request.CreateSpace != "" || request.RenameSpace != nil || request.DeleteSpace != ""):
		err = status.Error(codes.PermissionDenied, "people limited to spaces cannot create, rename, or delete spaces")
	case shared != nil && !limit.AllowsSpaceID(shared.Space):
		err = status.Error(codes.PermissionDenied, "this space is outside your spaces")
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
	case request.Mute != nil:
		err = s.muteTab(ctx, username, request.Mute)
	case request.Unmute != "":
		err = s.sessions.UnmuteTab(ctx, username, request.Unmute)
	case request.TopShelf != nil:
		// The shelf came from a request's protobuf JSON, so it marshals back without error.
		shelf, _ := protojson.Marshal(request.TopShelf)
		err = s.sessions.SaveTopShelf(ctx, username, shelf)
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

func (s *Server) liveShelfTabs(ctx context.Context, tabs []*ShelfTab) ([]*ShelfTab, error) {
	conversation := func(tab *ShelfTab) string {
		if tab.Server != "" {
			return ""
		}

		return backend.TabConversation(tab.Path)
	}

	var ids []string

	for _, tab := range tabs {
		if id := conversation(tab); id != "" {
			ids = append(ids, id)
		}
	}

	if len(ids) == 0 {
		return tabs, nil
	}

	live, err := s.sessions.ExistingConversations(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("read top shelf sessions: %w", err)
	}

	return slices.DeleteFunc(tabs, func(tab *ShelfTab) bool {
		id := conversation(tab)
		return id != "" && !slices.Contains(live, id)
	}), nil
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
func tabState(state *backend.TabState) (out *TabState, err error) {
	out = &TabState{Space: state.Space, Last: state.Last, SharedOrder: state.SharedOrder}
	out.Tabs, err = webTabs(state.Tabs)

	for _, group := range state.Groups {
		out.Groups = append(out.Groups, &TabGroup{Id: group.ID, Name: group.Name, Color: group.Color, Collapsed: group.Collapsed})
	}

	return out, err
}

func webTabs(saved []backend.Tab) (out []*Tab, err error) {
	for _, tab := range saved {
		page, errPage := searchPage(tab.Search)
		err = errors.Join(err, errPage)

		out = append(out, &Tab{Path: tab.Path, Group: tab.Group, Space: tab.Space, Search: page})
	}

	return out, err
}

// savedTabState is state as the backend saves it.
func savedTabState(state *TabState) *backend.TabState {
	saved := &backend.TabState{Space: state.Space, Last: state.Last, SharedOrder: state.SharedOrder}
	for _, tab := range state.Tabs {
		saved.Tabs = append(saved.Tabs, backend.Tab{Path: tab.Path, Group: tab.Group, Space: tab.Space, Search: searchJSON(tab.Search)})
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
