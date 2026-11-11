package backend

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"time"
)

const dashboardWatchInterval = 5 * time.Second

// dashboardTab is a Dashboard a person has open: their username, the id of the space it is in, and its tab path.
type dashboardTab struct{ username, space, path string }

// seenDashboard is what a pass sorted into an open Dashboard: its layout, each lane's id and query in order, and what
// each lane held.
type seenDashboard struct {
	layout string
	lanes  []seenLane
}

// seenLane is what a lane held: the chats it listed, newest arrival first, how many it held, and when the last one
// listed arrived.
type seenLane struct {
	ids   []string
	total int
	last  time.Time
}

// dashboardsSeen is what the Dashboard watch keeps from one pass to the next: a fingerprint of the session list
// without anyone's marks, and what it sorted into each open Dashboard.
type dashboardsSeen struct {
	sessions   uint64
	dashboards map[dashboardTab]seenDashboard
}

// WatchDashboards marks, every interval until ctx ends, each Dashboard a session entered a lane flagged to notify of
// since the pass before, for everyone who has it open, resolving Slack rooms through slack. The first pass only records.
func (s *SessionService) WatchDashboards(ctx context.Context, slack slackLookup) {
	ticker := time.NewTicker(dashboardWatchInterval)
	defer ticker.Stop()

	var seen dashboardsSeen

	for {
		// A failed pass keeps what it could not sort; the next tries again.
		var err error
		if seen, err = s.watchDashboards(ctx, slack, seen); err != nil && ctx.Err() == nil {
			s.log.Error("watch dashboards", "error", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// watchDashboards is one pass of WatchDashboards over seen, what the pass before kept, and returns what to keep. It
// reads every Dashboard with a flagged lane and who has it open: in their own tabs, on their Top Shelf, or Shared in a
// space they can see, its space being that tab's; a person with a Space Limit only in a space it admits. It sorts the
// session list into each as Board does for that person and space, and gives each person a session entered a flagged
// lane of one mark on it (dashboardArrivals). A Dashboard is sorted when the session list without anyone's marks
// changed, or when it was not sorted with its layout before; one whose sort fails keeps what it held.
func (s *SessionService) watchDashboards(ctx context.Context, slack slackLookup, seen dashboardsSeen) (dashboardsSeen, error) {
	type lane struct {
		tab       dashboardTab
		spaceName string
		search    SavedSearch
	}

	rows, err := queryRows(ctx, s.db, `WITH flagged AS (
    SELECT DISTINCT s.page FROM web_searches s JOIN web_search_pages p ON p.id = s.page WHERE s.notify AND p.board <> ''
), open AS (
    SELECT t.username, t.space, t.path, t.search_page, 2 AS precedence FROM web_tabs t JOIN flagged f ON f.page = t.search_page
    UNION ALL SELECT t.username, t.space, t.path, t.search_page, 1 FROM web_top_shelves t JOIN flagged f ON f.page = t.search_page WHERE t.server = ''
    UNION ALL SELECT p.username, t.space, t.path, t.search_page, 0 FROM web_shared_tabs t JOIN flagged f ON f.page = t.search_page CROSS JOIN web_tab_states p
), dashboards AS (
    -- A person's Shared tab, shelf tab and own tab of one path in one space are one Dashboard to them, in that order.
    SELECT DISTINCT ON (username, space, path) username, space, path, search_page FROM open ORDER BY username, space, path, precedence
)
SELECT d.username, d.space, COALESCE(w.name, ''), d.path, s.id, s.name, s.query, s.notify FROM dashboards d
JOIN web_searches s ON s.page = d.search_page LEFT JOIN web_spaces w ON w.id = d.space
ORDER BY d.username, d.space, d.path, s.position`, "open dashboards", func(row rowScanner) (lane, error) {
		var l lane
		return l, row.Scan(&l.tab.username, &l.tab.space, &l.spaceName, &l.tab.path, &l.search.ID, &l.search.Name, &l.search.Query, &l.search.Notify)
	})
	if err != nil {
		return seen, err
	}

	type dashboard struct {
		tab    dashboardTab
		layout string
		lanes  []SavedSearch
	}

	cfg := s.definitions.Clone()
	open := make(map[struct{ username, space string }][]dashboard)

	for _, l := range rows {
		// Only names are compared: a limit admits a space's ID exactly when it admits its name.
		if !(SpaceLimit{names: cfg.PermittedSpaces(l.tab.username)}).AllowsSpaceName(l.spaceName) {
			continue
		}

		key := struct{ username, space string }{l.tab.username, l.tab.space}

		group := open[key]
		if len(group) == 0 || group[len(group)-1].tab != l.tab {
			group = append(group, dashboard{tab: l.tab})
		}

		last := &group[len(group)-1]
		last.layout += l.search.ID + "\n" + l.search.Query + "\x00"
		last.lanes = append(last.lanes, l.search)
		open[key] = group
	}

	if len(open) == 0 {
		return dashboardsSeen{}, nil
	}

	// Rooms are part of the list: a room: lane depends on them.
	sessions := fnv.New64a()
	encoder := json.NewEncoder(sessions)

	for chat, err := range s.SidebarChats(ctx, "", slack, nil) {
		if err != nil {
			return seen, err
		}

		_ = encoder.Encode(chat) // A chat always encodes, and a hash takes every write.
	}

	next := dashboardsSeen{sessions: sessions.Sum64(), dashboards: make(map[dashboardTab]seenDashboard)}

	var (
		usernames, paths, notes []string
		errs                    []error
	)

	for key, group := range open {
		var (
			sorting []dashboard
			queries [][]string
		)

		for _, d := range group {
			if was, ok := seen.dashboards[d.tab]; ok && was.layout == d.layout && next.sessions == seen.sessions {
				next.dashboards[d.tab] = was
				continue
			}

			lanes := make([]string, 0, len(d.lanes))
			for _, lane := range d.lanes {
				lanes = append(lanes, lane.Query)
			}

			sorting, queries = append(sorting, d), append(queries, lanes)
		}

		if len(sorting) == 0 {
			continue
		}

		board, err := s.Board(ctx, key.username, key.space, queries, slack)
		if err != nil {
			errs = append(errs, err)

			for _, d := range sorting {
				if was, ok := seen.dashboards[d.tab]; ok {
					next.dashboards[d.tab] = was
				}
			}

			continue
		}

		for i, d := range sorting {
			var was []seenLane
			if last, ok := seen.dashboards[d.tab]; ok && last.layout == d.layout {
				was = last.lanes
			}

			held, note := dashboardArrivals(d.lanes, was, board[i])
			next.dashboards[d.tab] = seenDashboard{layout: d.layout, lanes: held}

			if note != "" {
				usernames, paths, notes = append(usernames, d.tab.username), append(paths, d.tab.path), append(notes, note)
			}
		}
	}

	if len(errs) > 0 {
		// Every Dashboard is sorted again on the next pass, so the ones that failed see what changed.
		next.sessions = seen.sessions
	}

	// A mark that fails to be written is found again on the next pass, which sorts from the same contents.
	if len(usernames) == 0 {
		return next, errors.Join(errs...)
	}

	if _, err := execState(ctx, s.db, "mark dashboard tabs", `WITH readers AS (
    SELECT r.username, r.path, NULL::text AS conversation_id, FALSE AS waking, r.note FROM unnest($1::text[], $2::text[], $3::text[]) AS r(username, path, note)
)`+markTabsSQL+` SELECT count(*) FROM marked`, usernames, paths, notes); err != nil {
		return seen, err
	}

	return next, errors.Join(errs...)
}

// dashboardArrivals reads sorted, what Board sorted into a Dashboard's lanes. It returns what each lane
// holds, and a note naming the lane and title of the newest session that entered a flagged lane since was, what the
// lanes held the pass before, and how many more; none when was is nil: no pass before saw these lanes. A session entered
// a lane that did not list it, unless it slid in from beyond the 200 listed: the lane held more, and the session arrived
// before the last one listed. One that a lane of unread sessions lists for a quiet mark, such as the person's own, did
// not enter it for anything that notifies.
func dashboardArrivals(lanes []SavedSearch, was []seenLane, sorted []BoardLane) (held []seenLane, note string) {
	held = make([]seenLane, len(sorted))

	var (
		newest  *SidebarChat
		name    string
		arrived int
	)

	for j, found := range sorted {
		held[j].total = found.Total
		terms, _ := parseSessionQuery(lanes[j].Query)
		unread := slices.ContainsFunc(terms, func(term SearchTerm) bool { return term.Key == keyIs && term.Value == "unread" })

		for k := range found.Chats {
			chat := &found.Chats[k]
			id, at := chat.Session.Conversation.ID, chat.Session.ArrivedAt
			held[j].ids, held[j].last = append(held[j].ids, id), at

			if !lanes[j].Notify || was == nil || slices.Contains(was[j].ids, id) || was[j].total > len(was[j].ids) && !at.After(was[j].last) ||
				unread && chat.Session.Attention.Quiet {
				continue
			}

			arrived++

			if newest == nil || at.After(newest.Session.ArrivedAt) {
				newest, name = chat, cmp.Or(lanes[j].Name, lanes[j].Query, fmt.Sprintf("Search %d", j+1))
			}
		}
	}

	if newest == nil {
		return held, ""
	}

	// The session's title as the Web shows it: its name, else its preview's first line, else its label.
	var preview string
	if newest.Session.Summary != nil {
		preview, _, _ = strings.Cut(newest.Session.Summary.LastMessage, "\n")
	}

	note = name + ": " + cmp.Or(newest.Session.Name, preview, newest.Session.Label())
	if arrived > 1 {
		note += fmt.Sprintf(" and %d more", arrived-1)
	}

	return held, note
}
