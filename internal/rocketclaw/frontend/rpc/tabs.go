package rpc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
		shelf    backend.TopShelf
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
		shelf, err = s.sessions.TopShelf(ctx, username)
	}

	if err == nil {
		closed, err = s.sessions.ClosedTabs(ctx, username)
	}

	if err == nil && !saved {
		state, response.Revision, err = s.sessions.TabState(ctx, username)
		response.State = tabState(&state)
	}

	if err != nil {
		return nil, fmt.Errorf("web tabs: %w", err)
	}

	response.Shared = sharedTabs(slices.DeleteFunc(shared, func(tab backend.SharedTab) bool { return !limit.AllowsSpaceID(tab.Space) }))
	response.Unshared = webSearchPage(unshared)
	response.Closed = webTabs(closed)
	response.TopShelf = webTopShelf(&shelf)

	for _, space := range spaces {
		if limit.AllowsSpaceID(space.ID) {
			response.Spaces = append(response.Spaces, &Space{Id: space.ID, Name: space.Name})
		}
	}

	return response, nil
}

// changeTabs makes request's one change for username. It reports whether a save was made from the saved revision,
// whether an edit of a Shared Search page was refused because the page changed since the edit's base, and the page a
// Search tab taken out of Shared held.
func (s *Server) changeTabs(ctx context.Context, username string, limit backend.SpaceLimit, request *TabsRequest) (saved, refused bool, unshared *backend.SearchPage, err error) {
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
			err = s.sessions.ShareTab(ctx, request.Share.Space, request.Share.Path, savedSearchPage(request.Share.Search))
		}
	case request.EditShared != nil:
		var edited bool

		edited, err = s.sessions.EditSharedSearch(ctx, request.EditShared.Space, request.EditShared.Path, savedSearchPage(request.EditShared.Base), savedSearchPage(request.EditShared.Search))
		refused = !edited
	case request.Unshare != nil:
		unshared, err = s.sessions.UnshareTab(ctx, request.Unshare.Space, request.Unshare.Path)
	case request.Attention != nil:
		err = s.changeAttention(ctx, username, request.Attention)
	case request.TopShelf != nil:
		err = s.sessions.SaveTopShelf(ctx, username, savedTopShelf(request.TopShelf))
	}

	if err != nil {
		return saved, refused, unshared, fmt.Errorf("change tabs: %w", err)
	}

	return saved, refused, unshared, nil
}

// changeAttention makes change to username's attention on its tab locations. New session and a kept group's
// placeholder have none, and every session must be one the caller may open, before any location changes. It is apart
// from changeTabs, whose branches would pass gocyclo's limit.
func (s *Server) changeAttention(ctx context.Context, username string, change *AttentionChange) error {
	action, ok := map[AttentionAction]backend.AttentionAction{
		AttentionAction_MARK_READ: backend.AttentionRead, AttentionAction_MARK_UNREAD: backend.AttentionUnread, AttentionAction_PARK: backend.AttentionPark,
		AttentionAction_UNPARK: backend.AttentionUnpark, AttentionAction_PARK_UNTIL_DONE: backend.AttentionParkUntilDone,
	}[change.Action]
	if !ok {
		return fmt.Errorf("web tab attention: %w", status.Error(codes.InvalidArgument, "unknown attention action"))
	}

	var until *time.Time

	if change.Until != "" {
		parsed, err := time.Parse(time.RFC3339, change.Until)
		if err != nil {
			return fmt.Errorf("web tab attention: %w", status.Error(codes.InvalidArgument, "until is not an RFC 3339 time"))
		}

		until = &parsed
	}

	for _, path := range change.Paths {
		if path == "/" || strings.HasPrefix(path, "group:") {
			return fmt.Errorf("web tab attention: %w", status.Error(codes.InvalidArgument, "New session and tab groups have no attention"))
		}

		if id := backend.TabConversation(path); id != "" {
			if err := s.visibleConversation(ctx, id); err != nil {
				return err
			}
		}
	}

	if err := s.sessions.ChangeAttention(ctx, username, change.Paths, action, until); err != nil {
		return fmt.Errorf("web tab attention: %w", err)
	}

	return nil
}

// sharedTabs is every space's Shared tabs as the Web reads them.
func sharedTabs(shared []backend.SharedTab) []*SharedTab {
	out := make([]*SharedTab, 0, len(shared))
	for _, tab := range shared {
		out = append(out, &SharedTab{Space: tab.Space, Path: tab.Path, Search: webSearchPage(tab.Search)})
	}

	return out
}

// tabState is the backend's saved state as the Web reads it.
func tabState(state *backend.TabState) *TabState {
	out := &TabState{Space: state.Space, Last: state.Last, SharedOrder: state.SharedOrder, Tabs: webTabs(state.Tabs)}
	for _, group := range state.Groups {
		out.Groups = append(out.Groups, &TabGroup{Id: group.ID, Name: group.Name, Color: group.Color, Collapsed: group.Collapsed})
	}

	return out
}

func webTabs(saved []backend.Tab) (out []*Tab) {
	for _, tab := range saved {
		out = append(out, &Tab{Path: tab.Path, Group: tab.Group, Space: tab.Space, Search: webSearchPage(tab.Search)})
	}

	return out
}

// savedTabState is state as the backend saves it.
func savedTabState(state *TabState) *backend.TabState {
	saved := &backend.TabState{Space: state.Space, Last: state.Last, SharedOrder: state.SharedOrder}
	for _, tab := range state.Tabs {
		saved.Tabs = append(saved.Tabs, backend.Tab{Path: tab.Path, Group: tab.Group, Space: tab.Space, Search: savedSearchPage(tab.Search)})
	}

	for _, group := range state.Groups {
		saved.Groups = append(saved.Groups, backend.TabGroup{ID: group.Id, Name: group.Name, Color: group.Color, Collapsed: group.Collapsed})
	}

	return saved
}

// webTopShelf is the backend's saved Top Shelf as the Web reads it.
func webTopShelf(shelf *backend.TopShelf) *TopShelf {
	out := &TopShelf{Collapsed: shelf.Collapsed}
	for _, tab := range shelf.Tabs {
		out.Tabs = append(out.Tabs, &ShelfTab{Server: tab.Server, Space: tab.Space, Path: tab.Path, Search: webSearchPage(tab.Search)})
	}

	return out
}

// savedTopShelf is shelf as the backend saves it.
func savedTopShelf(shelf *TopShelf) *backend.TopShelf {
	saved := &backend.TopShelf{Collapsed: shelf.Collapsed}
	for _, tab := range shelf.Tabs {
		saved.Tabs = append(saved.Tabs, backend.ShelfTab{Server: tab.Server, Space: tab.Space, Path: tab.Path, Search: savedSearchPage(tab.Search)})
	}

	return saved
}

// webSearchPage is a Search page the backend saved as the Web reads it, nil for other tabs.
func webSearchPage(saved *backend.SearchPage) *SearchPage {
	if saved == nil {
		return nil
	}

	page := &SearchPage{Name: saved.Name, Board: saved.Board, Active: saved.Active}
	for _, search := range saved.Searches {
		page.Searches = append(page.Searches, &SavedSearch{Id: search.ID, Name: search.Name, Query: search.Query, Notify: search.Notify})
	}

	return page
}

// savedSearchPage is page as the backend saves it, nil for other tabs.
func savedSearchPage(page *SearchPage) *backend.SearchPage {
	if page == nil {
		return nil
	}

	saved := &backend.SearchPage{Name: page.Name, Board: page.Board, Active: page.Active}
	for _, search := range page.Searches {
		saved.Searches = append(saved.Searches, backend.SavedSearch{ID: search.Id, Name: search.Name, Query: search.Query, Notify: search.Notify})
	}

	return saved
}
