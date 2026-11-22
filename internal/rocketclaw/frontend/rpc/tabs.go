package rpc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
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

	var (
		spaces   []backend.Space
		shared   []backend.Tab
		layout   backend.StripLayout
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
		layout, err = s.sessions.StripLayout(ctx, username)
	}

	if err == nil {
		closed, err = s.sessions.ClosedTabs(ctx, username)
	}

	if err == nil && !saved {
		state, response.Revision, err = s.sessions.TabState(ctx, username)
		response.State = tabState(&state)
	}

	if err == nil {
		response.AutoPark, err = s.sessions.AutoParkAll(ctx, username)
	}

	if err != nil {
		return nil, fmt.Errorf("web tabs: %w", err)
	}

	response.Shared = []*SharedTab{}

	for _, tab := range shared {
		if limit.AllowsSpaceID(tab.Space) {
			response.Shared = append(response.Shared, &SharedTab{Space: tab.Space, Path: tab.Path, Search: webSearchPage(tab.Search)})
		}
	}

	response.Unshared = webSearchPage(unshared)
	response.Closed = webTabs(closed)
	response.Layout = &StripLayout{Folded: layout.Folded, Order: layout.Order}

	for _, space := range spaces {
		if limit.AllowsSpaceID(space.ID) {
			response.Spaces = append(response.Spaces, &Space{Id: space.ID, Name: space.Name})
		}
	}

	return response, nil
}

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
	case request.Layout != nil:
		err = s.sessions.SaveStripLayout(ctx, username, &backend.StripLayout{Folded: request.Layout.Folded, Order: request.Layout.Order})
	case request.AutoPark != nil:
		err = s.sessions.SetAutoParkAll(ctx, username, *request.AutoPark)
	}

	if err != nil {
		return saved, refused, unshared, fmt.Errorf("change tabs: %w", err)
	}

	return saved, refused, unshared, nil
}

func (s *Server) changeAttention(ctx context.Context, username string, change *AttentionChange) error {
	action, ok := map[AttentionAction]backend.AttentionAction{
		AttentionAction_MARK_READ: backend.AttentionRead, AttentionAction_MARK_UNREAD: backend.AttentionUnread, AttentionAction_PARK: backend.AttentionPark,
		AttentionAction_UNPARK: backend.AttentionUnpark, AttentionAction_PARK_UNTIL_DONE: backend.AttentionParkUntilDone,
		AttentionAction_AUTO_PARK: backend.AttentionAutoPark, AttentionAction_STOP_AUTO_PARK: backend.AttentionNoAutoPark, AttentionAction_SENT: backend.AttentionSent,
		AttentionAction_FOLLOW: backend.AttentionFollow, AttentionAction_UNFOLLOW: backend.AttentionUnfollow,
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

func webSearchPage(saved *backend.SearchPage) *SearchPage {
	if saved == nil {
		return nil
	}

	page := &SearchPage{Name: saved.Name, Active: saved.Active}
	for _, search := range saved.Searches {
		page.Searches = append(page.Searches, &SavedSearch{Id: search.ID, Name: search.Name, Query: search.Query})
	}

	return page
}

func savedSearchPage(page *SearchPage) *backend.SearchPage {
	if page == nil {
		return nil
	}

	saved := &backend.SearchPage{Name: page.Name, Active: page.Active}
	for _, search := range page.Searches {
		saved.Searches = append(saved.Searches, backend.SavedSearch{ID: search.Id, Name: search.Name, Query: search.Query})
	}

	return saved
}
