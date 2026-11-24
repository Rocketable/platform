package backend

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type Attention struct {
	Path          string
	UnreadAt      time.Time
	Quiet, Parked bool
	Unfollowed    bool
	Until         time.Time
	UntilDone     bool
	Note          string
}

type AttentionAction string

const (
	AttentionRead          AttentionAction = "read"
	AttentionUnread        AttentionAction = "unread"
	AttentionPark          AttentionAction = "park"
	AttentionParkUntilDone AttentionAction = "park_done"
	AttentionUnpark        AttentionAction = "unpark"
	AttentionAutoPark      AttentionAction = "auto_park"
	AttentionNoAutoPark    AttentionAction = "no_auto_park"
	AttentionFollow        AttentionAction = "follow"
	AttentionUnfollow      AttentionAction = "unfollow"
	AttentionSent          AttentionAction = "sent"
)

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
    WHERE v.username IS NULL OR r.waking
    ORDER BY r.username, r.path
    ON CONFLICT (username, path) DO UPDATE SET
        until = CASE WHEN a.parked AND excluded.unread_at IS DISTINCT FROM now() THEN now() ELSE a.until END,
        resumes = a.resumes OR a.parked AND excluded.unread_at IS DISTINCT FROM now(),
        resume_until = CASE WHEN a.parked AND NOT a.resumes AND excluded.unread_at IS DISTINCT FROM now() THEN a.until ELSE a.resume_until END,
        unread_at = COALESCE(excluded.unread_at, a.unread_at),
        quiet = CASE WHEN excluded.unread_at IS NULL THEN a.quiet ELSE a.parked END,
        note = CASE WHEN excluded.unread_at IS NULL THEN a.note ELSE excluded.note END
    WHERE NOT a.unfollowed
    RETURNING a.path
)`

const endTurnParksSQL = `WITH ended AS (
    SELECT conversation_id, failed, idle_at FROM unnest($1::text[], $2::boolean[], $3::bigint[]) AS e(conversation_id, failed, idle_at)
), parks AS (
    SELECT a.username, a.path, e.failed AND (a.after_idle IS NULL OR e.idle_at > a.after_idle) AS failed FROM web_tab_attention a JOIN ended e USING (conversation_id)
    WHERE (a.parked AND a.until = 'infinity' OR a.resumes) AND (e.failed OR NOT a.worked)
    ORDER BY a.username, a.path FOR UPDATE OF a
)
UPDATE web_tab_attention a SET worked = NOT (p.failed AND a.resumes), resumes = a.resumes AND NOT p.failed,
    until = CASE WHEN p.failed AND a.parked AND a.until = 'infinity' THEN now() ELSE a.until END
FROM parks p WHERE a.username = p.username AND a.path = p.path`

const sessionReadersSQL = `, people AS (
    SELECT username, conversation_id, kind, waking FROM events WHERE username <> ''
    UNION SELECT t.username, e.conversation_id, e.kind, e.waking FROM events e JOIN web_tabs t USING (conversation_id)
    UNION SELECT a.username, e.conversation_id, e.kind, e.waking FROM events e JOIN web_tab_attention a USING (conversation_id)
        WHERE a.parked AND a.until = 'infinity'
    UNION SELECT p.username, e.conversation_id, e.kind, e.waking FROM events e JOIN web_shared_tabs t USING (conversation_id) CROSS JOIN web_tab_states p
), readers AS (
    SELECT c.username, ` + tabPathSQL + ` AS path, c.conversation_id, c.waking, '' AS note FROM people c
)`

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

func (s *SessionService) markLocation(ctx context.Context, tx *sql.Tx, page, space, note string, match func(rest string) bool) error {
	type tab struct {
		username, path, space string
		own                   bool
	}

	tabs, err := queryRows(ctx, tx, `WITH tabs AS (
    SELECT username, path, space, TRUE AS own FROM web_tabs WHERE starts_with(path, $1)
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

func (s *SessionService) MarkSearchFinished(ctx context.Context, username, path string) error {
	type tab struct {
		space string
		own   bool
	}

	tabs, err := queryRows(ctx, s.db, `WITH tabs AS (
    SELECT '' AS space, TRUE AS own FROM web_tabs WHERE username = $1 AND path = $2
    UNION ALL SELECT w.name, FALSE FROM web_shared_tabs t JOIN web_spaces w ON w.id = t.space WHERE t.path = $2
)
SELECT space, own FROM tabs`, "search tabs", func(row rowScanner) (tab, error) {
		var t tab
		return t, row.Scan(&t.space, &t.own)
	}, username, path)
	if err != nil {
		return err
	}

	limit := SpaceLimit{names: s.definitions.PermittedSpaces(username)}

	if !slices.ContainsFunc(tabs, func(t tab) bool { return t.own || limit.AllowsSpaceName(t.space) }) {
		return nil
	}

	_, err = execState(ctx, s.db, "mark search tab", `WITH readers AS (
    SELECT $1::text AS username, $2::text AS path, NULL::text AS conversation_id, FALSE AS waking, 'Search finished' AS note
)`+markTabsSQL+` SELECT count(*) FROM marked`, username, path)

	return err
}

func decodeSessionID(value string) string {
	if decoded, err := url.PathUnescape(value); err == nil && utf8.ValidString(decoded) {
		value = decoded
	}

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

func (s *SessionService) ChangeAttention(ctx context.Context, username string, paths []string, action AttentionAction, until *time.Time, admitted int64) error {
	sent := action == AttentionSent
	if sent {
		action = AttentionParkUntilDone
	}

	conversations := make([]string, 0, len(paths))
	afterIdle := make([]*int64, len(paths))

	for i, path := range paths {
		conversations = append(conversations, TabConversation(path))

		remote, isRemote := strings.CutPrefix(conversations[i], "opencode:")
		switch {
		case !isRemote || action != AttentionParkUntilDone:
		case sent:
			afterIdle[i] = &admitted
		default:
			name, session, _ := strings.Cut(remote, ":")

			var newest struct {
				Data []struct {
					Time struct {
						Created int64 `json:"created"`
					} `json:"time"`
				} `json:"data"`
			}
			if openCodeCall(ctx, name, s.definitions.Clone().OpenCode[name], http.MethodGet, "/api/session/"+url.PathEscape(session)+"/message?type=idle&order=desc&limit=1", nil, &newest) == nil {
				afterIdle[i] = new(int64)
				if len(newest.Data) > 0 {
					*afterIdle[i] = newest.Data[0].Time.Created
				}
			}
		}
	}

	tx, err := s.beginStateTx(ctx, "attention change")
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := execState(ctx, tx, "lock tab state", `INSERT INTO web_tab_states (username, revision) VALUES ($1, 0)
ON CONFLICT (username) DO UPDATE SET revision = web_tab_states.revision`, username); err != nil {
		return err
	}

	changed, err := queryStrings(ctx, tx, `WITH everyone AS (
    SELECT auto_park FROM web_tab_states WHERE username = $1
), locations AS (
    SELECT DISTINCT COALESCE(`+tabPathSQL+`, l.path) AS path, c.conversation_id,
        COALESCE(l.after_idle, CASE WHEN $4::text = 'park_done' AND c.kind = '`+string(KindRemote)+`' THEN COALESCE(c.read_through, 0) END) AS after_idle
    FROM unnest($2::text[], $3::text[], $7::bigint[]) AS l(path, conversation_id, after_idle) LEFT JOIN conversations c ON c.conversation_id = l.conversation_id
    LEFT JOIN web_tab_attention o ON o.username = $1 AND o.path = `+tabPathSQL+` CROSS JOIN everyone e
    WHERE (l.conversation_id = '' AND $4::text NOT IN ('park_done', 'auto_park', 'no_auto_park') OR c.conversation_id IS NOT NULL)
        AND (NOT $6 OR COALESCE(o.auto_park, e.auto_park))
)
INSERT INTO web_tab_attention AS a (username, path, conversation_id, unread_at, quiet, parked, until, auto_park, unfollowed, after_idle)
SELECT $1, path, conversation_id, CASE WHEN $4::text = 'unread' THEN now() END, $4::text = 'unread', $4::text IN ('park', 'park_done'),
    CASE $4::text WHEN 'park' THEN $5::timestamptz WHEN 'park_done' THEN 'infinity' END, CASE $4::text WHEN 'auto_park' THEN TRUE WHEN 'no_auto_park' THEN FALSE END,
    $4::text = 'unfollow', after_idle
FROM locations ORDER BY path
ON CONFLICT (username, path) DO UPDATE SET
    unread_at = CASE $4::text WHEN 'read' THEN NULL WHEN 'unfollow' THEN NULL WHEN 'unread' THEN now() ELSE a.unread_at END,
    quiet = CASE $4::text WHEN 'unread' THEN TRUE WHEN 'park' THEN a.quiet OR a.unread_at IS NOT NULL
        WHEN 'park_done' THEN a.quiet OR a.unread_at IS NOT NULL ELSE a.quiet END,
    note = CASE WHEN $4::text IN ('unread', 'unfollow') THEN '' ELSE a.note END,
    parked = CASE $4::text WHEN 'park' THEN TRUE WHEN 'park_done' THEN TRUE WHEN 'unpark' THEN FALSE ELSE a.parked END,
    until = CASE WHEN $4::text IN ('park', 'park_done', 'unpark') THEN excluded.until ELSE a.until END,
    after_idle = CASE WHEN $4::text IN ('park', 'park_done', 'unpark') THEN excluded.after_idle ELSE a.after_idle END,
    auto_park = COALESCE(excluded.auto_park, a.auto_park),
    unfollowed = CASE $4::text WHEN 'unfollow' THEN TRUE WHEN 'follow' THEN FALSE ELSE a.unfollowed END,
    worked = a.worked AND $4::text NOT IN ('park', 'park_done'),
    resumes = a.resumes AND $4::text IN ('auto_park', 'no_auto_park', 'follow', 'unfollow')
WHERE CASE $4::text WHEN 'unread' THEN a.unread_at IS NULL AND NOT a.parked WHEN 'unpark' THEN a.parked ELSE TRUE END
RETURNING a.path`, "change attention", username, paths, conversations, string(action), until, sent, afterIdle)
	if err != nil {
		return err
	}

	if _, err := execState(ctx, tx, "delete idle attention", `DELETE FROM web_tab_attention
WHERE username = $1 AND path = ANY($2) AND unread_at IS NULL AND NOT parked AND until IS NULL AND auto_park IS NULL AND NOT resumes AND NOT unfollowed`, username, changed); err != nil {
		return err
	}

	if err := commitState(tx, "attention change"); err != nil {
		return err
	}

	if len(changed) > 0 {
		s.ringListing(username)
	}

	if action == AttentionParkUntilDone {
		s.ringParks()
	}

	return nil
}

func (s *SessionService) FindWebLogin(ctx context.Context, email string) (string, error) {
	var username string

	err := queryScalar(ctx, s.db, "match Web login", `SELECT CASE WHEN count(*) = 1 THEN min(username) ELSE '' END
FROM web_tab_states WHERE lower(username) = lower($1)`, &username, email)

	return username, err
}

func (s *SessionService) PollAttention(ctx context.Context, username, path string, hold bool) (revision int64, navigation string, err error) {
	if path == "" {
		_, err := scanFound("read tab state", s.db.QueryRowContext(ctx, `SELECT revision FROM web_tab_states WHERE username = $1`, username).Scan(&revision))

		return revision, "", err
	}

	err = s.db.QueryRowContext(ctx, `WITH viewed AS (
    SELECT $2::text AS path, $3::text AS conversation_id
), recorded AS (
    INSERT INTO web_tab_states (username, revision, viewing, viewed_at)
    SELECT $1, 0, COALESCE(`+tabPathSQL+`, v.path), now() FROM viewed v LEFT JOIN conversations c ON c.conversation_id = v.conversation_id
    ON CONFLICT (username) DO UPDATE SET viewing = excluded.viewing, viewed_at = excluded.viewed_at, navigation = ''
    RETURNING new.viewing, new.revision, CASE WHEN old.navigation_at > now() - interval '3 days' THEN old.navigation ELSE '' END AS navigation
), cleared AS (
    UPDATE web_tab_attention a SET unread_at = NULL FROM recorded r
    WHERE a.username = $1 AND a.path = r.viewing AND a.unread_at IS NOT NULL AND NOT ($4 AND a.quiet)
)
SELECT revision, navigation FROM recorded`, username, path, TabConversation(path), hold).Scan(&revision, &navigation)
	if err != nil {
		return 0, "", fmt.Errorf("record tab view: %w", err)
	}

	return revision, navigation, nil
}

func (s *SessionService) requestNavigation(ctx context.Context, username, path string) error {
	_, err := execState(ctx, s.db, "request navigation", `INSERT INTO web_tab_states (username, revision, navigation, navigation_at) VALUES ($1, 0, $2, now())
ON CONFLICT (username) DO UPDATE SET navigation = excluded.navigation, navigation_at = excluded.navigation_at`, username, path)

	return err
}

const parkTickEvery = 2 * time.Second

const parkWorkDoneSQL = `CASE WHEN c.kind = '` + string(KindRemote) + `' THEN c.read_through > a.after_idle ELSE a.worked END`

const sessionWorkSQL = `, watched AS (
    SELECT conversation_id, conversation_id AS owner FROM candidates
    UNION ALL SELECT c.conversation_id, c.reports_to FROM conversations c JOIN candidates d ON c.reports_to = d.conversation_id
), working AS (
    SELECT w.owner AS conversation_id FROM watched w JOIN turns t USING (conversation_id)
    WHERE t.phase IN ('` + string(turnRunning) + `', '` + string(turnDelivering) + `') OR t.phase = '` + string(turnWaiting) + `' AND t.reason = '` + string(waitQueued) + `'
        OR NOT t.takes_lane AND t.note = '` + string(notePending) + `' AND t.wake
    UNION SELECT c.conversation_id FROM conversations c JOIN candidates d USING (conversation_id)
    WHERE c.kind = '` + string(KindRemote) + `' AND (c.running OR c.background) OR c.goal_status = '` + GoalStatusActive + `'
)`

func (s *SessionService) WakeParks(ctx context.Context) {
	ticker := time.NewTicker(parkTickEvery)
	defer ticker.Stop()

	for {
		_ = s.tickParks(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.parkWake:
		}
	}
}

func (s *SessionService) WatchListing(username string) (rings <-chan struct{}, stop func()) {
	changed := make(chan struct{}, 1)

	s.listingMu.Lock()
	s.listingWatchers[changed] = username
	s.listingMu.Unlock()

	return changed, func() {
		s.listingMu.Lock()
		delete(s.listingWatchers, changed)
		s.listingMu.Unlock()
	}
}

func (s *SessionService) ringListing(usernames ...string) {
	s.listingMu.Lock()
	defer s.listingMu.Unlock()

	for changed, username := range s.listingWatchers {
		if len(usernames) == 0 || slices.Contains(usernames, username) {
			select {
			case changed <- struct{}{}:
			default:
			}
		}
	}
}

func (s *SessionService) ringParks() {
	select {
	case s.parkWake <- struct{}{}:
	default:
	}
}

func (s *SessionService) tickParks(ctx context.Context) error {
	if _, err := execState(ctx, s.db, "end idle parks", `WITH candidates AS (
    SELECT DISTINCT a.conversation_id FROM web_tab_attention a JOIN conversations c USING (conversation_id)
    WHERE a.parked AND a.until = 'infinity' AND `+parkWorkDoneSQL+`
)`+sessionWorkSQL+`, idle AS (
    SELECT a.username, a.path FROM web_tab_attention a JOIN conversations c USING (conversation_id) LEFT JOIN working w USING (conversation_id)
    WHERE a.parked AND a.until = 'infinity' AND `+parkWorkDoneSQL+` AND w.conversation_id IS NULL
    ORDER BY a.username, a.path FOR UPDATE OF a SKIP LOCKED
)
UPDATE web_tab_attention a SET until = COALESCE(a.unread_at, now())
FROM idle i WHERE a.username = i.username AND a.path = i.path`); err != nil {
		return err
	}

	restored, err := queryStrings(ctx, s.db, `WITH candidates AS (
    SELECT DISTINCT conversation_id FROM web_tab_attention WHERE resumes AND NOT parked
), asked AS (
    SELECT q.conversation_id FROM candidates d JOIN turns r USING (conversation_id)
    JOIN steps q ON q.conversation_id = r.conversation_id AND q.key LIKE '%`+webQuestionStep+`'
    LEFT JOIN steps x ON x.conversation_id = q.conversation_id AND x.key = left(q.key, -length('`+webQuestionStep+`')) || '/answer'
    WHERE r.phase NOT IN ('waiting', 'done') AND r.takes_lane AND x.key IS NULL
    UNION SELECT c.conversation_id FROM conversations c JOIN candidates d USING (conversation_id) WHERE c.asking
), answered AS (
    SELECT a.username, a.path, (a.resume_until <= now()) IS TRUE AS dropped
    FROM web_tab_attention a LEFT JOIN asked q USING (conversation_id)
    WHERE a.resumes AND NOT a.parked AND q.conversation_id IS NULL
    ORDER BY a.username, a.path FOR UPDATE OF a SKIP LOCKED
)
UPDATE web_tab_attention a SET resumes = FALSE, resume_until = NULL, parked = NOT d.dropped, worked = a.worked AND NOT d.dropped,
    after_idle = CASE WHEN d.dropped THEN NULL ELSE a.after_idle END,
    until = CASE WHEN d.dropped THEN a.until ELSE a.resume_until END, quiet = a.quiet OR NOT d.dropped AND a.unread_at IS NOT NULL
FROM answered d WHERE a.username = d.username AND a.path = d.path
RETURNING a.username`, "restore question parks")
	if err != nil {
		return err
	}

	if len(restored) > 0 {
		s.ringListing(restored...)
	}

	due, err := queryStrings(ctx, s.db, `SELECT DISTINCT username FROM web_tab_attention WHERE until <= now() ORDER BY username`, "due parks")
	if err != nil {
		return err
	}

	var errWake error

	for _, username := range due {
		errWake = errors.Join(errWake, s.wakeDue(ctx, username))
	}

	return errWake
}

func (s *SessionService) wakeDue(ctx context.Context, username string) error {
	tx, err := s.beginStateTx(ctx, "park wake")
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	var viewing string

	found, err := scanFound("lock tab state", tx.QueryRowContext(ctx, `SELECT CASE WHEN viewed_at >= now() - interval '4 seconds' THEN viewing ELSE '' END
FROM web_tab_states WHERE username = $1 FOR UPDATE`, username).Scan(&viewing))
	if err != nil || !found {
		return err
	}

	woken, err := queryStrings(ctx, tx, `WITH due AS (
    SELECT path FROM web_tab_attention WHERE username = $1 AND until <= now() ORDER BY path FOR UPDATE SKIP LOCKED
)
UPDATE web_tab_attention a SET parked = FALSE, until = NULL, worked = a.worked AND a.resumes, after_idle = CASE WHEN a.resumes THEN a.after_idle END,
    unread_at = CASE WHEN a.path = $2 OR a.unfollowed THEN a.unread_at ELSE now() END, quiet = a.quiet AND (a.path = $2 OR a.unfollowed),
    note = CASE WHEN a.path = $2 OR a.unfollowed OR a.unread_at >= a.until THEN a.note ELSE 'Park ended' END
FROM due d WHERE a.username = $1 AND a.path = d.path AND a.until <= now()
RETURNING a.path`, "wake tab timers", username, viewing)
	if err != nil {
		return err
	}

	if len(woken) > 0 {
		if _, err := tx.ExecContext(ctx, `WITH due AS (
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
    INSERT INTO web_search_pages (id, name, active) SELECT d.search_page, p.name, p.active FROM due d JOIN web_search_pages p ON p.id = d.source
), searches AS (
    INSERT INTO web_searches (page, position, id, name, query)
    SELECT d.search_page, s.position, s.id, s.name, s.query FROM due d JOIN web_searches s ON s.page = d.source
), reopened AS (
    DELETE FROM web_closed_tabs w USING due d WHERE w.username = $1 AND w.path = d.path
), restored AS (
    SELECT count(*) AS n FROM due
)
UPDATE web_tab_states s SET revision = s.revision + 1 FROM restored r WHERE s.username = $1 AND r.n > 0`, username, woken); err != nil {
			return fmt.Errorf("restore closed tabs: %w", err)
		}
	}

	if err := commitState(tx, "park wake"); err != nil {
		return err
	}

	if len(woken) > 0 {
		s.ringListing(username)
	}

	return nil
}

func (s *SessionService) AutoParkAll(ctx context.Context, username string) (bool, error) {
	on := true

	err := s.db.QueryRowContext(ctx, `SELECT auto_park FROM web_tab_states WHERE username = $1`, username).Scan(&on)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read auto-park: %w", err)
	}

	return on, nil
}

func (s *SessionService) SetAutoParkAll(ctx context.Context, username string, on bool) error {
	_, err := execState(ctx, s.db, "save auto-park", `INSERT INTO web_tab_states (username, revision, auto_park) VALUES ($1, 0, $2)
ON CONFLICT (username) DO UPDATE SET auto_park = excluded.auto_park`, username, on)

	return err
}

func (s *SessionService) TabAttention(ctx context.Context, username string) ([]Attention, error) {
	return queryRows(ctx, s.db, `SELECT path, unread_at, quiet, parked, unfollowed, until, note FROM web_tab_attention
WHERE username = $1 AND conversation_id IS NULL ORDER BY path`, "tab attention", func(row rowScanner) (Attention, error) {
		var (
			attention     Attention
			unread, until sql.NullTime
		)

		if err := row.Scan(&attention.Path, &unread, &attention.Quiet, &attention.Parked, &attention.Unfollowed, &until, &attention.Note); err != nil {
			return Attention{}, fmt.Errorf("read tab attention: %w", err)
		}

		attention.UnreadAt, attention.Until = unread.Time.UTC(), until.Time.UTC()

		return attention, nil
	}, username)
}
