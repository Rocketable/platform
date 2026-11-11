package backend

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

// Attention is one person's attention on one tab location: when something new started waiting for them, zero once
// read, and whether that mark raises no notification; whether they parked the tab, and when the park ends, zero for
// none; whether the park lasts until the session's next turn ends, which has no time; and the notification text of a
// mark the server wrote.
type Attention struct {
	Path          string
	UnreadAt      time.Time
	Quiet, Parked bool
	Until         time.Time
	UntilDone     bool
	Note          string
}

// AttentionAction is what a person does to the attention of their tabs.
type AttentionAction string

// The attention actions.
const (
	AttentionRead          AttentionAction = "read"
	AttentionUnread        AttentionAction = "unread"
	AttentionPark          AttentionAction = "park"
	AttentionParkUntilDone AttentionAction = "park_done"
	AttentionUnpark        AttentionAction = "unpark"
)

// markTabsSQL continues a WITH holding readers (username, path, conversation_id, waking, note), the people something
// new on a tab location reaches, a session's under its canonical path, into marked: their marks, written once per
// person and location, a question first, in key order. A mark raises a notification unless the tab is parked, when it
// is quiet. A question also makes a park due at once, so the person's own timer wake ends it, and brings the tab back
// if they closed it; so does anything new on a tab parked until it is done, whose until is 'infinity'. Nobody is marked
// for the location they viewed in the last 10 seconds, or 4 for a question, though a question, or anything new on a
// tab parked until it is done, still makes its park due. Whatever depends on the stored row is decided on the locked
// row, in the conflict clause, which tells a question by its mark's time: one microsecond after now(), or none while
// in view.
// ponytail: a tab in view joins for its park until done as it was before the row's lock, so a park the person turns
// timed in that instant can end then, silently, as the tab is in view; telling the row's two in-view reasons apart in
// the conflict clause would close it.
const markTabsSQL = `, strongest AS (
    SELECT DISTINCT ON (username, path) username, path, conversation_id, waking, note FROM readers
    ORDER BY username, path, waking DESC
), marked AS (
    INSERT INTO web_tab_attention AS a (username, path, conversation_id, unread_at, note)
    SELECT r.username, r.path, r.conversation_id,
        CASE WHEN v.username IS NOT NULL THEN NULL WHEN r.waking THEN now() + interval '1 microsecond' ELSE now() END, r.note
    FROM strongest r
    LEFT JOIN web_tab_states v ON v.username = r.username AND v.viewing = r.path
        AND v.viewed_at >= now() - CASE WHEN r.waking THEN interval '4 seconds' ELSE interval '10 seconds' END
    LEFT JOIN web_tab_attention d ON d.username = r.username AND d.path = r.path AND d.parked AND d.until = 'infinity'
    WHERE v.username IS NULL OR r.waking OR d.path IS NOT NULL
    ORDER BY r.username, r.path
    ON CONFLICT (username, path) DO UPDATE SET
        until = CASE WHEN a.parked AND (excluded.unread_at IS DISTINCT FROM now() OR a.until = 'infinity') THEN now() ELSE a.until END,
        unread_at = COALESCE(excluded.unread_at, a.unread_at),
        quiet = CASE WHEN excluded.unread_at IS NULL THEN a.quiet ELSE a.parked END,
        note = CASE WHEN excluded.unread_at IS NULL THEN a.note ELSE excluded.note END
    RETURNING a.path
)`

// sessionReadersSQL continues a WITH holding events (conversation_id, kind, waking, username), something new in a
// session and, when username is not empty, a person it is for, into readers for markTabsSQL: that person, the people
// with the session in their own tabs, those with it on a Top Shelf, every Web person, who sees it in some space's
// Shared group, as a tab of their own, and those who parked it until it is done, also after closing its tab. A shelf
// row of another server and a kept group's placeholder tab name no session.
const sessionReadersSQL = `, people AS (
    SELECT username, conversation_id, kind, waking FROM events WHERE username <> ''
    UNION SELECT t.username, e.conversation_id, e.kind, e.waking FROM events e JOIN web_tabs t USING (conversation_id)
    UNION SELECT a.username, e.conversation_id, e.kind, e.waking FROM events e JOIN web_tab_attention a USING (conversation_id)
        WHERE a.parked AND a.until = 'infinity'
    UNION SELECT s.username, e.conversation_id, e.kind, e.waking FROM events e JOIN web_top_shelves s USING (conversation_id)
    UNION SELECT p.username, e.conversation_id, e.kind, e.waking FROM events e JOIN web_shared_tabs t USING (conversation_id) CROSS JOIN web_tab_states p
), readers AS (
    SELECT c.username, ` + tabPathSQL + ` AS path, c.conversation_id, c.waking, '' AS note FROM people c
)`

// turnEventsSQL continues a WITH holding turn (id, conversation_id, kind, inbound_json, waking), a session's turn
// something new happened in, into events for sessionReadersSQL: the turn, and each Web person who sent its input or a
// steer its own step lists as applied, which it does from the moment it applies one.
const turnEventsSQL = `, inbounds AS (
    SELECT inbound_json AS inbound FROM turn
    UNION ALL SELECT steer->'inbound' FROM turn t JOIN steps r ON r.conversation_id = t.conversation_id AND r.key = t.id
    JOIN steps s ON s.conversation_id = t.conversation_id AND s.key = t.id || '/steers'
    CROSS JOIN LATERAL json_array_elements(s.value) steer WHERE r.value::jsonb->'applied_input_ids' ? (steer->>'id')
), events AS (
    SELECT conversation_id, kind, waking, '' AS username FROM turn
    UNION SELECT t.conversation_id, t.kind, t.waking, i.inbound->'Metadata'->>'` + protocol.InboundPrincipalEmailMetadataKey + `' FROM turn t CROSS JOIN inbounds i
    WHERE i.inbound->>'Source' = '` + string(protocol.SourceWeb) + `' AND i.inbound->>'Human' = 'true'
)`

// markLocation marks, in tx and with note, the readers of something new in a cron or page of the space named space,
// empty for every space: the people with the overview page, such as /cron, in that space, or in any space for none,
// and those with a tab under it whose rest after "<page>/", as stored, match accepts: in their own tabs, on a Top Shelf
// this server keeps, or Shared, which every Web person has. A person with a Space Limit is reached only when it admits
// space, and for a Shared tab that tab's space too.
func (s *SessionService) markLocation(ctx context.Context, tx *sql.Tx, page, space, note string, match func(rest string) bool) error {
	type tab struct {
		username, path, space string
		own                   bool
	}

	tabs, err := queryRows(ctx, tx, `WITH tabs AS (
    SELECT username, path, space, TRUE AS own FROM web_tabs WHERE starts_with(path, $1)
    UNION ALL SELECT username, path, space, TRUE FROM web_top_shelves WHERE server = '' AND starts_with(path, $1)
    UNION ALL SELECT p.username, t.path, t.space, FALSE FROM web_shared_tabs t CROSS JOIN web_tab_states p WHERE starts_with(t.path, $1)
)
SELECT t.username, t.path, COALESCE(w.name, ''), t.own FROM tabs t LEFT JOIN web_spaces w ON w.id = t.space`, "location tabs", func(row rowScanner) (tab, error) {
		var t tab
		return t, row.Scan(&t.username, &t.path, &t.space, &t.own)
	}, page)
	if err != nil {
		return err
	}

	cfg := s.definitions.Clone()

	var usernames, paths []string

	for _, t := range tabs {
		rest, under := strings.CutPrefix(t.path, page+"/")
		// Only names are compared: a limit admits a space's ID exactly when it admits its name.
		limit := SpaceLimit{names: cfg.PermittedSpaces(t.username)}

		if (under && match(rest) || t.path == page && (space == "" || strings.EqualFold(t.space, space))) && limit.AllowsSpaceName(space) && (t.own || limit.AllowsSpaceName(t.space)) {
			usernames, paths = append(usernames, t.username), append(paths, t.path)
		}
	}

	if len(usernames) == 0 {
		return nil
	}

	_, err = execState(ctx, tx, "mark location tabs", `WITH readers AS (
    SELECT r.username, r.path, NULL::text AS conversation_id, FALSE AS waking, $3::text AS note FROM unnest($1::text[], $2::text[]) AS r(username, path)
)`+markTabsSQL+` SELECT count(*) FROM marked`, usernames, paths, note)

	return err
}

// MarkSearchFinished gives username one mark with the note "Search finished" on the Search tab at path, when they
// still have it: among their own tabs, on their Top Shelf of this server, or Shared in a space they can see.
func (s *SessionService) MarkSearchFinished(ctx context.Context, username, path string) error {
	type tab struct {
		space string
		own   bool
	}

	tabs, err := queryRows(ctx, s.db, `WITH tabs AS (
    SELECT '' AS space, TRUE AS own FROM web_tabs WHERE username = $1 AND path = $2
    UNION ALL SELECT '', TRUE FROM web_top_shelves WHERE username = $1 AND server = '' AND path = $2
    UNION ALL SELECT w.name, FALSE FROM web_shared_tabs t JOIN web_spaces w ON w.id = t.space WHERE t.path = $2
)
SELECT space, own FROM tabs`, "search tabs", func(row rowScanner) (tab, error) {
		var t tab
		return t, row.Scan(&t.space, &t.own)
	}, username, path)
	if err != nil {
		return err
	}

	limit := SpaceLimit{names: s.definitions.Clone().PermittedSpaces(username)}

	if !slices.ContainsFunc(tabs, func(t tab) bool { return t.own || limit.AllowsSpaceName(t.space) }) {
		return nil
	}

	_, err = execState(ctx, s.db, "mark search tab", `WITH readers AS (
    SELECT $1::text AS username, $2::text AS path, NULL::text AS conversation_id, FALSE AS waking, 'Search finished' AS note
)`+markTabsSQL+` SELECT count(*) FROM marked`, username, path)

	return err
}

// decodeSessionID reads the id in a tab path as the Web's decodeSessionId does: URI-escaped or not, then base64url
// of one byte per character when atob accepts it, else that text itself.
func decodeSessionID(value string) string {
	if decoded, err := url.PathUnescape(value); err == nil && utf8.ValidString(decoded) {
		value = decoded
	}

	// atob drops ASCII whitespace, then up to two "=" ending a length divisible by 4, and leaves the extra bits.
	data := strings.Map(func(r rune) rune {
		if strings.ContainsRune("\t\n\f\r ", r) {
			return -1
		}

		return r
	}, strings.NewReplacer("-", "+", "_", "/").Replace(value)+strings.Repeat("=", (4-len(value)%4)%4))
	if len(data)%4 == 0 {
		data = strings.TrimSuffix(strings.TrimSuffix(data, "="), "=")
	}

	raw, err := base64.RawStdEncoding.DecodeString(data)
	if err != nil {
		return value
	}

	text := make([]rune, len(raw))
	for i, b := range raw {
		text[i] = rune(b)
	}

	return string(text)
}

// ChangeAttention does action to username's attention on the tab locations paths, a session's under its canonical
// path; one naming a session that does not exist is skipped. Mark as unread marks a read, unparked tab quietly; Park
// sets a park until until, nil for until unparked, which makes a mark quiet; Park until done parks a session's tab
// until its next turn ends, its until 'infinity', which no clock reaches, and skips any other location; Unpark leaves
// the mark. A row left read and unparked is deleted.
func (s *SessionService) ChangeAttention(ctx context.Context, username string, paths []string, action AttentionAction, until *time.Time) error {
	tx, err := s.beginStateTx(ctx, "attention change")
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	// Every writer for one person locks their tab state before any attention row, and writers for many people lock
	// none; every writer takes a person's attention rows in path order, so they cannot deadlock.
	if _, err := execState(ctx, tx, "lock tab state", `INSERT INTO web_tab_states (username, revision) VALUES ($1, 0)
ON CONFLICT (username) DO UPDATE SET revision = web_tab_states.revision`, username); err != nil {
		return err
	}

	conversations := make([]string, 0, len(paths))
	for _, path := range paths {
		conversations = append(conversations, TabConversation(path))
	}

	changed, err := queryStrings(ctx, tx, `WITH locations AS (
    SELECT DISTINCT COALESCE(`+tabPathSQL+`, l.path) AS path, c.conversation_id
    FROM unnest($2::text[], $3::text[]) AS l(path, conversation_id) LEFT JOIN conversations c ON c.conversation_id = l.conversation_id
    WHERE l.conversation_id = '' AND $4::text <> 'park_done' OR c.conversation_id IS NOT NULL
)
INSERT INTO web_tab_attention AS a (username, path, conversation_id, unread_at, quiet, parked, until)
SELECT $1, path, conversation_id, CASE WHEN $4::text = 'unread' THEN now() END, $4::text = 'unread', $4::text IN ('park', 'park_done'),
    CASE $4::text WHEN 'park' THEN $5::timestamptz WHEN 'park_done' THEN 'infinity' END
FROM locations ORDER BY path
ON CONFLICT (username, path) DO UPDATE SET
    unread_at = CASE $4::text WHEN 'read' THEN NULL WHEN 'unread' THEN now() ELSE a.unread_at END,
    quiet = CASE $4::text WHEN 'unread' THEN TRUE WHEN 'park' THEN a.quiet OR a.unread_at IS NOT NULL
        WHEN 'park_done' THEN a.quiet OR a.unread_at IS NOT NULL ELSE a.quiet END,
    note = CASE $4::text WHEN 'unread' THEN '' ELSE a.note END,
    parked = CASE $4::text WHEN 'park' THEN TRUE WHEN 'park_done' THEN TRUE WHEN 'unpark' THEN FALSE ELSE a.parked END,
    until = CASE $4::text WHEN 'read' THEN a.until WHEN 'unread' THEN a.until ELSE excluded.until END
WHERE CASE $4::text WHEN 'unread' THEN a.unread_at IS NULL AND NOT a.parked WHEN 'unpark' THEN a.parked ELSE TRUE END
RETURNING a.path`, "change attention", username, paths, conversations, string(action), until)
	if err != nil {
		return err
	}

	if _, err := execState(ctx, tx, "delete idle attention", `DELETE FROM web_tab_attention
WHERE username = $1 AND path = ANY($2) AND unread_at IS NULL AND NOT parked AND until IS NULL`, username, changed); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit attention change: %w", err)
	}

	return nil
}

// PollAttention records that username views the tab at path now, none when path is empty, which clears their mark
// on it unless hold is set and the mark is quiet: they marked it unread while viewing it. It then wakes their timers
// that came due, unparking the tab and marking it with a notification, except on the tab they viewed in the last 4
// seconds, where a park ends without a mark. A park that ends says so,
// unless news came as or after it was due, such as a question or the turn end a park until done waited for, which
// then speaks for itself. A closed tab whose timer came due goes back where it was. It returns username's tab-state
// revision.
func (s *SessionService) PollAttention(ctx context.Context, username, path string, hold bool) (int64, error) {
	// A poll that views nothing and has no timer due, as most are, takes no lock.
	if path == "" {
		var revision, due int64

		err := s.db.QueryRowContext(ctx, `WITH due AS (
    SELECT count(*) AS n FROM web_tab_attention WHERE username = $1 AND until <= now()
)
SELECT s.revision, d.n FROM web_tab_states s CROSS JOIN due d WHERE s.username = $1`, username).Scan(&revision, &due)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}

		if err != nil {
			return 0, fmt.Errorf("read tab state: %w", err)
		}

		if due == 0 {
			return revision, nil
		}
	}

	tx, err := s.beginStateTx(ctx, "attention poll")
	if err != nil {
		return 0, err
	}

	defer func() { _ = tx.Rollback() }()

	// The view locks the person's state before their attention rows, as every writer for one person does.
	if path != "" {
		if err := tx.QueryRowContext(ctx, `WITH viewed AS (
    SELECT $2::text AS path, $3::text AS conversation_id
)
INSERT INTO web_tab_states (username, revision, viewing, viewed_at)
SELECT $1, 0, COALESCE(`+tabPathSQL+`, v.path), now() FROM viewed v LEFT JOIN conversations c ON c.conversation_id = v.conversation_id
ON CONFLICT (username) DO UPDATE SET viewing = excluded.viewing, viewed_at = excluded.viewed_at
RETURNING viewing`, username, path, TabConversation(path)).Scan(&path); err != nil {
			return 0, fmt.Errorf("record tab view: %w", err)
		}

		// The viewed row and the due ones are locked together in key order, as the marking statement takes them.
		if _, err := tx.ExecContext(ctx, `SELECT path FROM web_tab_attention WHERE username = $1 AND (path = $2 OR until <= now()) ORDER BY path FOR UPDATE`,
			username, path); err != nil {
			return 0, fmt.Errorf("lock viewed attention: %w", err)
		}

		if _, err := execState(ctx, tx, "clear viewed mark", `UPDATE web_tab_attention SET unread_at = NULL
WHERE username = $1 AND path = $2 AND unread_at IS NOT NULL AND NOT ($3 AND quiet)`, username, path, hold); err != nil {
			return 0, err
		}
	}

	var (
		revision int64
		viewing  string
	)

	err = tx.QueryRowContext(ctx, `SELECT revision, CASE WHEN viewed_at >= now() - interval '4 seconds' THEN viewing ELSE '' END
FROM web_tab_states WHERE username = $1 FOR UPDATE`, username).Scan(&revision, &viewing)
	if errors.Is(err, sql.ErrNoRows) {
		// Only a person's own action sets a timer, and it saves their state first.
		return 0, nil
	}

	if err != nil {
		return 0, fmt.Errorf("lock tab state: %w", err)
	}

	// Due rows are locked in key order first, as the marking statement takes them.
	woken, err := queryStrings(ctx, tx, `WITH due AS (
    SELECT path FROM web_tab_attention WHERE username = $1 AND until <= now() ORDER BY path FOR UPDATE
)
UPDATE web_tab_attention a SET parked = FALSE, until = NULL,
    unread_at = CASE WHEN a.path = $2 THEN a.unread_at ELSE now() END, quiet = a.quiet AND a.path = $2,
    note = CASE WHEN a.path = $2 OR a.unread_at >= a.until THEN a.note ELSE 'Park ended' END
FROM due d WHERE a.username = $1 AND a.path = d.path AND a.until <= now()
RETURNING a.path`, "wake tab timers", username, viewing)
	if err != nil {
		return 0, err
	}

	// A woken location that is among the person's closed tabs and not their open ones comes back in its space, in its
	// group if that still exists, and at its position, ahead of the tab there now, with a copy of its Search page.
	if len(woken) > 0 {
		err = tx.QueryRowContext(ctx, `WITH due AS (
    SELECT w.path, w.conversation_id, w.space, COALESCE(g.id, '') AS group_id, w.position, w.search_page AS source,
        CASE WHEN w.search_page IS NOT NULL THEN gen_random_uuid() END AS search_page
    FROM web_closed_tabs w
    LEFT JOIN web_tabs t ON t.username = w.username AND t.path = w.path
    LEFT JOIN web_tab_groups g ON g.username = w.username AND g.id = w.group_id
    WHERE w.username = $1 AND w.path = ANY($2) AND t.path IS NULL
), strip AS (
    SELECT path, position, FALSE AS restored FROM web_tabs WHERE username = $1
    UNION ALL SELECT path, position, TRUE FROM due
), placed AS (
    SELECT path, restored, row_number() OVER (ORDER BY position, restored DESC, path) - 1 AS position FROM strip
), moved AS (
    UPDATE web_tabs t SET position = p.position FROM placed p WHERE t.username = $1 AND t.path = p.path AND NOT p.restored AND t.position <> p.position
), tabs AS (
    INSERT INTO web_tabs (username, path, conversation_id, position, group_id, space, search_page)
    SELECT $1, d.path, d.conversation_id, p.position, d.group_id, d.space, d.search_page FROM due d JOIN placed p ON p.path = d.path AND p.restored
), pages AS (
    INSERT INTO web_search_pages (id, name, board, active) SELECT d.search_page, p.name, p.board, p.active FROM due d JOIN web_search_pages p ON p.id = d.source
), searches AS (
    INSERT INTO web_searches (page, position, id, name, query, notify)
    SELECT d.search_page, s.position, s.id, s.name, s.query, s.notify FROM due d JOIN web_searches s ON s.page = d.source
), reopened AS (
    DELETE FROM web_closed_tabs w USING due d WHERE w.username = $1 AND w.path = d.path
), restored AS (
    SELECT count(*) AS n FROM due
)
UPDATE web_tab_states s SET revision = s.revision + 1 FROM restored r WHERE s.username = $1 AND r.n > 0
RETURNING s.revision`, username, woken).Scan(&revision)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("restore closed tabs: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit attention poll: %w", err)
	}

	return revision, nil
}

// TabAttention lists username's attention on tab locations that are not sessions, by path. A session's travels with
// its listing row.
func (s *SessionService) TabAttention(ctx context.Context, username string) ([]Attention, error) {
	return queryRows(ctx, s.db, `SELECT path, unread_at, quiet, parked, until, note FROM web_tab_attention
WHERE username = $1 AND conversation_id IS NULL ORDER BY path`, "tab attention", func(row rowScanner) (Attention, error) {
		var (
			attention     Attention
			unread, until sql.NullTime
		)

		if err := row.Scan(&attention.Path, &unread, &attention.Quiet, &attention.Parked, &until, &attention.Note); err != nil {
			return Attention{}, fmt.Errorf("read tab attention: %w", err)
		}

		attention.UnreadAt, attention.Until = unread.Time.UTC(), until.Time.UTC()

		return attention, nil
	}, username)
}
