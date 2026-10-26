package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/jackc/pgx/v5/pgconn"
)

type stateDAO struct {
	db stateStoreDB
}

type ConversationKind string

const (
	KindWebChat        ConversationKind = "web_chat"
	KindSlackThread    ConversationKind = "slack_thread"
	KindCronRun        ConversationKind = "cron_run"
	KindOneOffCronRun  ConversationKind = "one_off_cron_run"
	KindExternalMCPRun ConversationKind = "external_mcp_run"
	KindSubagent       ConversationKind = "subagent"
	KindReview         ConversationKind = "review"
	KindShadow         ConversationKind = "shadow"
	KindRemote         ConversationKind = "remote"
	KindAgentDraft     ConversationKind = "agent_draft"
)

type conversationFacts struct {
	kind                               ConversationKind
	slackChannel, slackThread, cronRun string
}

func (d stateDAO) stickyConversationID(ctx context.Context, agent, username string, sticky harness.AgentSticky) (string, error) {
	if sticky == harness.AgentStickyUser && username == "" {
		return "", errors.New("sticky: user requires an identifiable person")
	}

	var id string

	err := d.db.QueryRowContext(ctx, `SELECT conversation_id FROM conversations WHERE sticky = $1 AND agent = $2 AND ($1 = 'global' OR created_by = $3)`,
		string(sticky), agent, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("read sticky session: %w", err)
	}

	return id, nil
}

func (d stateDAO) createConversation(ctx context.Context, conversation protocol.Conversation, facts conversationFacts, sticky harness.AgentSticky) (bool, error) {
	summary, err := loadSessionSummary(ctx, d.db, conversation.ID)
	if err != nil {
		return false, err
	}

	path, at := "", int64(0)
	if run := conversation.Run; run != nil {
		facts.kind, path, at = KindCronRun, run.Path, run.At.UnixNano()
		if run.OneOff {
			facts.kind = KindOneOffCronRun
		}
	}

	birth := map[harness.AgentSticky]struct{ column, value, conflict string }{
		harness.AgentStickyOff:    {"", "", "(conversation_id)"},
		harness.AgentStickyUser:   {", sticky", ", 'user'", "(agent, created_by) WHERE sticky = 'user'"},
		harness.AgentStickyGlobal: {", sticky", ", 'global'", "(agent) WHERE sticky = 'global'"},
	}[sticky]

	created, err := execRows(ctx, d.db, "create conversation", `WITH existing AS (SELECT count(*) AS n FROM conversations WHERE conversation_id = $1),
space AS (
    INSERT INTO web_spaces (name) SELECT $4::text FROM existing WHERE n = 0 AND $4 <> ''
    ON CONFLICT ((lower(name))) DO UPDATE SET name = web_spaces.name RETURNING id
)
INSERT INTO conversations (conversation_id, agent, created_by, space_id, kind, listed, slack_channel, slack_thread, cron_run, cron_path, cron_run_at_unix_ns`+birth.column+`)
SELECT $1, $2, $3, max(id), $5, $11, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), NULLIF($10::bigint, 0)`+birth.value+` FROM space
ON CONFLICT `+birth.conflict+` DO NOTHING`, conversation.ID, conversation.Agent, conversation.CreatedBy, conversation.Space, facts.kind, facts.slackChannel, facts.slackThread, facts.cronRun, path, at,
		facts.kind == KindWebChat || facts.kind == KindSlackThread)
	if err != nil {
		return false, err
	}

	return created > 0, saveSessionSummary(ctx, d.db, summary)
}

func (d stateDAO) observedEntries(ctx context.Context, conversationID string) ([]ObservedSessionEntry, error) {
	return queryRows(ctx, d.db, `WITH `+sessionHistorySQL+`
SELECT id, entry_json, source_conversation_id, synced, revert_index FROM effective_entries
ORDER BY id`, "rocketcode session entries", scanObservedEntry, conversationID)
}

func scanObservedEntry(row rowScanner) (ObservedSessionEntry, error) {
	var (
		entry ObservedSessionEntry
		raw   sql.RawBytes
		index int
	)
	if err := row.Scan(&entry.ID, &raw, &entry.SourceConversationID, &entry.Synced, &index); err != nil {
		return ObservedSessionEntry{}, fmt.Errorf("scan rocketcode session entry: %w", err)
	}

	if err := json.Unmarshal(raw, &entry.Entry); err != nil {
		return ObservedSessionEntry{}, fmt.Errorf("parse rocketcode session entry: %w", err)
	}

	if err := visibleEntry(&entry); err != nil {
		return ObservedSessionEntry{}, err
	}

	clipRevertEntry(&entry.Entry, index)

	return entry, nil
}

func (d stateDAO) bindProducer(ctx context.Context, conversationID, destination string) (owner string, bound bool, err error) {
	if err := d.db.QueryRowContext(ctx, `UPDATE conversations SET reports_to = COALESCE(reports_to, $2) WHERE conversation_id = $1
RETURNING new.reports_to, old.reports_to IS NULL`, conversationID, destination).Scan(&owner, &bound); err != nil {
		return "", false, fmt.Errorf("bind producer destination: %w", err)
	}

	return owner, bound, nil
}

func (d stateDAO) bindRun(ctx context.Context, run, destination string) (owner string, schedules map[string]protocol.ScheduledMessageState, err error) {
	owner, bound, err := d.bindProducer(ctx, run, destination)
	if err != nil {
		return "", nil, err
	}

	if bound {
		entries, err := queryRows(ctx, d.db, `SELECT id, entry_json, '', TRUE, -1 FROM entries WHERE conversation_id = $1 AND audience <> 'sandbox' ORDER BY id`, "bound run entries", scanObservedEntry, run)
		if err != nil {
			return "", nil, err
		}

		summary, err := loadSessionSummary(ctx, d.db, owner)
		if err != nil {
			return "", nil, err
		}

		for i := range entries {
			entry := &entries[i].Entry

			text, _, err := messageSearchText(ctx, d.db, run, entry.ReplayInput)
			if err != nil {
				return "", nil, err
			}

			if _, err := d.db.ExecContext(ctx, `UPDATE entries SET search_text = NULLIF($2, '') WHERE id = $1`, entries[i].ID, text); err != nil {
				return "", nil, fmt.Errorf("index bound run entry: %w", err)
			}

			if err := projectSessionSummary(&summary, entry, entry.Timestamp.UTC().Format(time.RFC3339Nano)); err != nil {
				return "", nil, err
			}
		}

		if err := saveSessionSummary(ctx, d.db, summary); err != nil {
			return "", nil, err
		}

		if err := refreshPrincipals(ctx, d.db, []string{owner}); err != nil {
			return "", nil, err
		}
	}

	schedules, err = queryMap(ctx, d.db, `WITH chat AS (
    SELECT conversation_id, agent FROM conversations WHERE conversation_id = $2
), recurring AS (
    UPDATE schedules s SET conversation_id = o.conversation_id, agent = o.agent FROM chat o WHERE s.conversation_id = $1
    RETURNING s.id, s.conversation_id, s.agent, s.message, s.next_due_unix_ns, TRUE, s.interval_ns, s.origin
), once AS (
    UPDATE turns t SET conversation_id = o.conversation_id, agent = o.agent FROM chat o WHERE t.conversation_id = $1 AND t.phase = 'waiting' AND t.reason = $3
    RETURNING t.input_id, t.conversation_id, t.agent, t.message, t.run_at_unix_ns, FALSE, 0::bigint, t.origin
)
SELECT * FROM recurring UNION ALL SELECT * FROM once`, "rehome producer schedules", scheduledMessageScanner{}, run, owner, waitScheduled)

	return owner, schedules, err
}

type producerEffect struct {
	Reset    bool                            `json:"reset,omitempty"`
	Schedule *protocol.ScheduledMessageState `json:"schedule,omitempty"`
}

func (d stateDAO) applyProducerEffects(ctx context.Context, run string, effects []producerEffect) (target string, schedules map[string]protocol.ScheduledMessageState, err error) {
	var agent string
	if err := d.db.QueryRowContext(ctx, `WITH run AS (
    SELECT COALESCE(reports_to, conversation_id) AS target FROM conversations WHERE conversation_id = $1
)
SELECT r.target, t.agent FROM run r JOIN conversations t ON t.conversation_id = r.target`, run).Scan(&target, &agent); err != nil {
		return "", nil, fmt.Errorf("read producer effect target: %w", err)
	}

	schedules = map[string]protocol.ScheduledMessageState{}

	for _, effect := range effects {
		if effect.Reset {
			if err := d.resetScheduledMessages(ctx, target); err != nil {
				return "", nil, err
			}

			clear(schedules)

			continue
		}

		scheduled := *effect.Schedule
		scheduled.ConversationID, scheduled.Agent = target, agent

		id := rand.Text()
		if err := d.putScheduledMessage(ctx, id, &scheduled); err != nil {
			return "", nil, err
		}

		schedules[id] = scheduled
	}

	return target, schedules, nil
}

func (s *SessionService) Thread(conversationID string) (ThreadState, bool, error) {
	var thread ThreadState

	err := s.db.QueryRowContext(context.Background(), `SELECT agent, created_by, COALESCE(parent_conversation_id, ''), CASE WHEN sticky = 'off' THEN '' ELSE sticky END
FROM conversations WHERE conversation_id = $1 AND retention = 'record'`, strings.TrimSpace(conversationID)).Scan(&thread.Agent, &thread.CreatedBy, &thread.parent, &thread.sticky)
	if err == sql.ErrNoRows {
		return ThreadState{}, false, nil
	}

	if err != nil {
		return ThreadState{}, false, fmt.Errorf("read managed conversation: %w", err)
	}

	return thread, true, nil
}

func (s *SessionService) StickySessions(ctx context.Context, username string, agents harness.Agents) (map[string]string, error) {
	var user, global []string

	for name := range agents.Items {
		switch agents.Items[name].Sticky {
		case harness.AgentStickyUser:
			user = append(user, name)
		case harness.AgentStickyGlobal:
			global = append(global, name)
		case harness.AgentStickyOff:
		}
	}

	sessions := map[string]string{}
	if user == nil && global == nil {
		return sessions, nil
	}

	err := readRows(ctx, s.db, `SELECT agent, conversation_id FROM conversations
WHERE sticky = 'global' AND agent = ANY($1) OR sticky = 'user' AND agent = ANY($2) AND created_by = $3 AND created_by <> ''`, "sticky sessions", func(row rowScanner) error {
		var agent, id string
		if err := row.Scan(&agent, &id); err != nil {
			return fmt.Errorf("scan sticky session: %w", err)
		}

		sessions[agent] = id

		return nil
	}, global, user, username)

	return sessions, err
}

type VoiceSettings struct {
	Agent      string
	Voice      string
	Transcript bool
}

func (s *SessionService) VoiceSettings(ctx context.Context, username string) (VoiceSettings, error) {
	var settings VoiceSettings

	err := s.db.QueryRowContext(ctx, `SELECT agent, voice, transcript FROM voice_settings WHERE username = $1`, username).Scan(&settings.Agent, &settings.Voice, &settings.Transcript)
	if errors.Is(err, sql.ErrNoRows) {
		return VoiceSettings{}, nil
	}

	if err != nil {
		return VoiceSettings{}, fmt.Errorf("read voice settings: %w", err)
	}

	return settings, nil
}

func (s *SessionService) UpdateVoiceSettings(ctx context.Context, username string, agent, voice *string, transcript *bool) error {
	_, err := execState(ctx, s.db, "update voice settings", `INSERT INTO voice_settings (username, agent, voice, transcript) VALUES ($1, COALESCE($2, ''), COALESCE($3, ''), COALESCE($4, FALSE))
ON CONFLICT (username) DO UPDATE SET agent = COALESCE($2, voice_settings.agent), voice = COALESCE($3, voice_settings.voice), transcript = COALESCE($4, voice_settings.transcript)`, username, agent, voice, transcript)

	return err
}

func (s *SessionService) behaviorSettings(ctx context.Context) (config.Behavior, bool, error) {
	var (
		behavior config.Behavior
		document []byte
	)

	err := s.db.QueryRowContext(ctx, `SELECT document FROM behavior_settings`).Scan(&document)
	if errors.Is(err, sql.ErrNoRows) {
		return config.Behavior{}, false, nil
	}

	if err != nil {
		return config.Behavior{}, false, fmt.Errorf("read behavior settings: %w", err)
	}

	if err := json.Unmarshal(document, &behavior); err != nil {
		return config.Behavior{}, false, fmt.Errorf("parse behavior settings: %w", err)
	}

	return behavior, true, nil
}

func putBehaviorSettings(ctx context.Context, db stateStoreDB, behavior *config.Behavior) error {
	document, err := json.Marshal(behavior)
	if err != nil {
		return fmt.Errorf("encode behavior settings: %w", err)
	}

	_, err = execState(ctx, db, "store behavior settings", `INSERT INTO behavior_settings (document) VALUES ($1)
ON CONFLICT (id) DO UPDATE SET document = excluded.document`, document)

	return err
}

type Space struct{ ID, Name string }

var ErrSpaceRejected = errors.New("another space has that name, or it is the last space")

func (s *SessionService) Spaces(ctx context.Context) ([]Space, error) {
	return queryRows(ctx, s.db, `SELECT id, name FROM web_spaces ORDER BY created_at, id`, "web spaces", func(row rowScanner) (Space, error) {
		var space Space
		return space, row.Scan(&space.ID, &space.Name)
	})
}

type SpaceLimit struct {
	names, ids, agents []string
}

func (s *SessionService) SpaceLimitFor(ctx context.Context, spaces []string) (SpaceLimit, error) {
	limit := SpaceLimit{names: spaces}
	if !limit.Limited() {
		return limit, nil
	}

	cfg := s.definitions.Clone()

	stored, err := s.Spaces(ctx)
	if err != nil {
		return SpaceLimit{}, err
	}

	agents, _, err := LoadRuntimeDefinitions(s.definitions, cfg.RuntimeDirName())
	if err != nil {
		return SpaceLimit{}, fmt.Errorf("load agents for space limit: %w", err)
	}

	for _, space := range stored {
		if limit.AllowsSpaceName(space.Name) {
			limit.ids = append(limit.ids, space.ID)
		}
	}

	for name := range agents.Items {
		if space := agents.Items[name].Space; space != "" && limit.AllowsSpaceName(space) {
			limit.agents = append(limit.agents, name)
		}
	}

	return limit, nil
}

func (s *SessionService) CreateLimitSpaces(ctx context.Context, names []string) error {
	_, err := execState(ctx, s.db, "create limit spaces", `INSERT INTO web_spaces (name) SELECT unnest($1::text[]) ON CONFLICT ((lower(name))) DO NOTHING`, names)
	return err
}

func (l SpaceLimit) Limited() bool { return len(l.names) > 0 }

func (l SpaceLimit) AllowsAgent(agent string) bool {
	return !l.Limited() || slices.Contains(l.agents, agent)
}

func (l SpaceLimit) AllowsSpaceID(id string) bool {
	return !l.Limited() || slices.Contains(l.ids, id)
}

func (l SpaceLimit) AllowsSpaceName(name string) bool {
	return !l.Limited() || slices.ContainsFunc(l.names, func(listed string) bool { return strings.EqualFold(listed, name) })
}

func (l SpaceLimit) AllowsSession(session *SidebarSession) bool {
	switch {
	case !l.Limited():
		return true
	case session.Kind == KindRemote:
		return false
	case session.SpaceID != "":
		return slices.Contains(l.ids, session.SpaceID)
	default:
		return slices.Contains(l.agents, session.Conversation.Agent)
	}
}

func (s *SessionService) TopLevelSession(ctx context.Context, conversationID string) (SidebarSession, error) {
	var session SidebarSession

	err := s.db.QueryRowContext(ctx, `WITH RECURSIVE starts AS (
    SELECT $1::text AS id
), `+ancestorsSQL+`, top AS (
    SELECT conversation_id FROM ancestors ORDER BY depth DESC LIMIT 1
)
SELECT COALESCE(c.space_id, ''), c.agent, c.kind FROM top JOIN conversations c USING (conversation_id)`, conversationID).Scan(&session.SpaceID, &session.Conversation.Agent, &session.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return SidebarSession{}, nil
	}

	if err != nil {
		return SidebarSession{}, fmt.Errorf("read top-level session: %w", err)
	}

	return session, nil
}

func (s *SessionService) CreateSpace(ctx context.Context, name string) error {
	return spaceNameError(execState(ctx, s.db, "create space", `INSERT INTO web_spaces (name) VALUES ($1)`, name))
}

func (s *SessionService) RenameSpace(ctx context.Context, id, name string) error {
	return spaceNameError(execState(ctx, s.db, "rename space", `UPDATE web_spaces SET name = $2 WHERE id = $1`, id, name))
}

func spaceNameError(_ sql.Result, err error) error {
	if errPg, ok := errors.AsType[*pgconn.PgError](err); ok && errPg.Code == "23505" {
		return fmt.Errorf("%w: %w", ErrSpaceRejected, err)
	}

	return err
}

func (s *SessionService) DeleteSpace(ctx context.Context, id string) error {
	deleted, err := execRows(ctx, s.db, "delete space", `WITH locked AS (SELECT id, created_at FROM web_spaces ORDER BY id FOR UPDATE),
others AS (SELECT id FROM locked WHERE id <> $1 ORDER BY created_at, id LIMIT 1),
moving AS (
    SELECT t.path, o.id AS space FROM web_shared_tabs t CROSS JOIN others o
    LEFT JOIN web_shared_tabs kept ON kept.space = o.id AND kept.path = t.path
    WHERE t.space = $1 AND kept.path IS NULL
),
moved AS (
    UPDATE web_shared_tabs t SET space = m.space FROM moving m WHERE t.space = $1 AND t.path = m.path
),
pages AS (
    DELETE FROM hosted_pages p USING conversations c, others WHERE p.conversation_id = c.conversation_id AND c.space_id = $1
)
DELETE FROM web_spaces USING others WHERE web_spaces.id = $1`, id)
	if err == nil && deleted == 0 {
		return ErrSpaceRejected
	}

	return err
}

type Tab struct {
	Path         string
	Group, Space string
	Search       *SearchPage
}

type SearchPage struct {
	Name     string        `json:"name,omitempty"`
	Board    string        `json:"board,omitempty"`
	Active   string        `json:"active,omitempty"`
	Searches []SavedSearch `json:"searches,omitempty"`
}

type SavedSearch struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Query  string `json:"query,omitempty"`
	Notify bool   `json:"notify,omitempty"`
}

func (p *SearchPage) Scan(src any) error {
	if err := json.Unmarshal(src.([]byte), p); err != nil {
		return fmt.Errorf("read search page: %w", err)
	}

	return nil
}

const searchPageJoinSQL = `LEFT JOIN web_search_pages p ON p.id = t.search_page LEFT JOIN web_searches s ON s.page = p.id`

const searchPageSQL = `CASE WHEN p.id IS NOT NULL THEN jsonb_strip_nulls(jsonb_build_object('name', NULLIF(p.name, ''), 'board', NULLIF(p.board, ''), 'active', NULLIF(p.active, ''),
    'searches', jsonb_agg(jsonb_build_object('id', NULLIF(s.id, ''), 'name', NULLIF(s.name, ''), 'query', NULLIF(s.query, ''), 'notify', NULLIF(s.notify, FALSE)) ORDER BY s.position) FILTER (WHERE s.page IS NOT NULL))) END`

const searchPagesSQL = `pages AS (
    INSERT INTO web_search_pages (id, name, board, active)
    SELECT search_page, COALESCE(search->>'name', ''), COALESCE(search->>'board', ''), COALESCE(search->>'active', '') FROM written
    ON CONFLICT (id) DO UPDATE SET name = excluded.name, board = excluded.board, active = excluded.active
), dropped AS (
    DELETE FROM web_searches s USING written w WHERE s.page = w.search_page AND s.position > jsonb_array_length(COALESCE(w.search->'searches', '[]'))
), searches AS (
    INSERT INTO web_searches (page, position, id, name, query, notify)
    SELECT w.search_page, s.n, COALESCE(s.search->>'id', ''), COALESCE(s.search->>'name', ''), COALESCE(s.search->>'query', ''), COALESCE((s.search->>'notify')::boolean, FALSE)
    FROM written w CROSS JOIN jsonb_array_elements(w.search->'searches') WITH ORDINALITY AS s(search, n)
    ON CONFLICT (page, position) DO UPDATE SET id = excluded.id, name = excluded.name, query = excluded.query, notify = excluded.notify
)`

type TabGroup struct {
	ID, Name, Color string
	Collapsed       bool
}

type TabState struct {
	Tabs        []Tab
	Groups      []TabGroup
	Space       string
	Last        map[string]string
	SharedOrder []string
}

const tabPathSQL = `CASE WHEN c.kind = '` + string(KindRemote) + `' THEN '/oc/' ELSE '/s/' END || rtrim(translate(replace(encode(convert_to(c.conversation_id, 'UTF8'), 'base64'), E'\n', ''), '+/', '-_'), '=')`

func TabConversation(path string) string {
	encoded, ok := strings.CutPrefix(path, "/s/")
	if !ok {
		if encoded, ok = strings.CutPrefix(path, "/oc/"); !ok {
			return ""
		}
	}

	if unescaped, err := url.PathUnescape(encoded); err == nil {
		encoded = unescaped
	}

	if id, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(encoded), "=")); err == nil {
		encoded = string(id)
	}

	return strings.ToValidUTF8(encoded, "\uFFFD")
}

func (s *SessionService) TabState(ctx context.Context, username string) (TabState, int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return TabState{}, 0, fmt.Errorf("begin tab state read: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var (
		state       TabState
		last, order []byte
		revision    int64
	)

	err = tx.QueryRowContext(ctx, `SELECT space, last, shared_order, revision FROM web_tab_states WHERE username = $1`, username).Scan(&state.Space, &last, &order, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return TabState{}, 0, nil
	}

	if err != nil {
		return TabState{}, 0, fmt.Errorf("read tab state: %w", err)
	}

	if err := json.Unmarshal(last, &state.Last); err != nil {
		return TabState{}, 0, fmt.Errorf("decode last tabs: %w", err)
	}

	if err := json.Unmarshal(order, &state.SharedOrder); err != nil {
		return TabState{}, 0, fmt.Errorf("decode shared order: %w", err)
	}

	state.Tabs, err = queryRows(ctx, tx, `SELECT t.path, t.group_id, t.space, `+searchPageSQL+` FROM web_tabs t
`+searchPageJoinSQL+`
WHERE t.username = $1 GROUP BY t.username, t.path, p.id ORDER BY t.position`, "tabs", func(row rowScanner) (Tab, error) {
		var tab Tab
		return tab, row.Scan(&tab.Path, &tab.Group, &tab.Space, &tab.Search)
	}, username)
	if err != nil {
		return TabState{}, 0, err
	}

	state.Groups, err = queryRows(ctx, tx, `SELECT id, name, color, collapsed FROM web_tab_groups WHERE username = $1 ORDER BY position`, "tab groups", func(row rowScanner) (TabGroup, error) {
		var group TabGroup
		return group, row.Scan(&group.ID, &group.Name, &group.Color, &group.Collapsed)
	}, username)

	return state, revision, err
}

func (s *SessionService) SaveTabState(ctx context.Context, username string, state *TabState, revision int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tab state save: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var lastSpaces, lastPaths, lastConversations []string
	for space, path := range state.Last {
		lastSpaces, lastPaths, lastConversations = append(lastSpaces, space), append(lastPaths, path), append(lastConversations, TabConversation(path))
	}

	if claimed, err := execRows(ctx, tx, "save tab state", `WITH lasts AS (
    SELECT COALESCE(jsonb_object_agg(l.space, COALESCE(`+tabPathSQL+`, l.path)), '{}') AS last
    FROM unnest($4::text[], $5::text[], $6::text[]) AS l(space, path, conversation_id)
    LEFT JOIN conversations c ON c.conversation_id = l.conversation_id
)
INSERT INTO web_tab_states (username, space, last, shared_order, revision) SELECT $1, $2, last, COALESCE(to_jsonb($7::text[]), '[]'), $3 + 1 FROM lasts
ON CONFLICT (username) DO UPDATE SET space = excluded.space, last = excluded.last, shared_order = excluded.shared_order, revision = excluded.revision
WHERE web_tab_states.revision = $3`, username, state.Space, revision, lastSpaces, lastPaths, lastConversations, state.SharedOrder); err != nil || claimed == 0 {
		return false, err
	}

	var (
		paths, conversations, groups, spaces []string
		searches                             []*SearchPage
		groupIDs, names, colors              []string
		collapsed                            []bool
	)

	pages, err := queryRows(ctx, tx, `SELECT t.path, `+searchPageSQL+` FROM web_tabs t
JOIN web_search_pages p ON p.id = t.search_page LEFT JOIN web_searches s ON s.page = p.id
WHERE t.username = $1 GROUP BY t.username, t.path, p.id`, "tab search pages", func(row rowScanner) (Tab, error) {
		var tab Tab
		return tab, row.Scan(&tab.Path, &tab.Search)
	}, username)
	if err != nil {
		return false, err
	}

	for _, tab := range state.Tabs {
		if i := slices.IndexFunc(pages, func(page Tab) bool { return page.Path == tab.Path }); tab.Search == nil && i >= 0 {
			tab.Search = pages[i].Search
		}

		paths, conversations, groups, spaces, searches = append(paths, tab.Path), append(conversations, TabConversation(tab.Path)), append(groups, tab.Group), append(spaces, tab.Space), append(searches, tab.Search)
	}

	for _, group := range state.Groups {
		groupIDs, names, colors, collapsed = append(groupIDs, group.ID), append(names, group.Name), append(colors, group.Color), append(collapsed, group.Collapsed)
	}

	var recorded int
	if err := tx.QueryRowContext(ctx, `WITH incoming AS (
    SELECT COALESCE(`+tabPathSQL+`, t.path) AS path
    FROM unnest($2::text[], $3::text[]) AS t(path, conversation_id)
    LEFT JOIN conversations c ON c.conversation_id = t.conversation_id
), reopened AS (
    DELETE FROM web_closed_tabs w USING incoming i WHERE w.username = $1 AND w.path = i.path
), opening AS (
    SELECT i.path FROM incoming i LEFT JOIN web_tabs o ON o.username = $1 AND o.path = i.path WHERE o.path IS NULL
), closing AS (
    SELECT o.path, o.conversation_id, o.space, o.group_id, o.position, o.search_page AS source, CASE WHEN o.search_page IS NOT NULL THEN gen_random_uuid() END AS search_page
    FROM web_tabs o
    LEFT JOIN incoming i ON i.path = o.path
    WHERE o.username = $1 AND i.path IS NULL AND o.path <> '/' AND NOT starts_with(o.path, 'group:')
), closed AS (
    INSERT INTO web_closed_tabs (username, path, conversation_id, space, group_id, position, search_page, closed_at)
    SELECT $1, path, conversation_id, space, group_id, position, search_page, now() FROM closing
    ON CONFLICT (username, path) DO UPDATE SET conversation_id = excluded.conversation_id, space = excluded.space,
        group_id = excluded.group_id, position = excluded.position, search_page = excluded.search_page, closed_at = excluded.closed_at
    RETURNING search_page
), copied AS (
    SELECT c.source, c.search_page FROM closed JOIN closing c USING (search_page)
), pages AS (
    INSERT INTO web_search_pages (id, name, board, active) SELECT c.search_page, p.name, p.board, p.active FROM copied c JOIN web_search_pages p ON p.id = c.source
), searches AS (
    INSERT INTO web_searches (page, position, id, name, query, notify)
    SELECT c.search_page, s.position, s.id, s.name, s.query, s.notify FROM copied c JOIN web_searches s ON s.page = c.source
), touched AS (
    SELECT a.path, c.path IS NOT NULL AS closing FROM web_tab_attention a
    LEFT JOIN closing c USING (path) LEFT JOIN opening n USING (path)
    WHERE a.username = $1 AND (c.path IS NOT NULL AND a.parked AND a.until IS NULL OR n.path IS NOT NULL AND a.unread_at IS NOT NULL AND NOT a.quiet)
    ORDER BY a.path FOR UPDATE OF a
), unparked AS (
    UPDATE web_tab_attention a SET parked = FALSE FROM touched u WHERE a.username = $1 AND a.path = u.path AND u.closing AND a.parked AND a.until IS NULL
), quieted AS (
    UPDATE web_tab_attention a SET quiet = TRUE FROM touched u WHERE a.username = $1 AND a.path = u.path AND NOT u.closing AND a.unread_at IS NOT NULL
)
SELECT count(*) FROM closed`, username, paths, conversations).Scan(&recorded); err != nil {
		return false, fmt.Errorf("record closed tabs: %w", err)
	}

	if recorded > 0 {
		if _, err := execState(ctx, tx, "trim closed tabs", `WITH timed AS (
    SELECT path FROM web_tab_attention WHERE username = $1 AND until IS NOT NULL
), ranked AS (
    SELECT w.path, row_number() OVER (ORDER BY w.closed_at DESC, w.path) AS n FROM web_closed_tabs w
    LEFT JOIN timed t USING (path) WHERE w.username = $1 AND t.path IS NULL
)
DELETE FROM web_closed_tabs w USING ranked r WHERE w.username = $1 AND w.path = r.path AND r.n > 50`, username); err != nil {
			return false, err
		}
	}

	if _, err := execState(ctx, tx, "clear tab state", `WITH groups AS (
    DELETE FROM web_tab_groups WHERE username = $1
)
DELETE FROM web_tabs WHERE username = $1`, username); err != nil {
		return false, err
	}

	if _, err := execState(ctx, tx, "write tab state", `WITH incoming AS (
    SELECT COALESCE(`+tabPathSQL+`, t.path) AS path, c.conversation_id, t.n - 1 AS position, t.group_id, t.space, t.search
    FROM unnest($6::text[], $7::text[], $8::text[], $9::text[], $10::jsonb[]) WITH ORDINALITY AS t(path, conversation_id, group_id, space, search, n)
    LEFT JOIN conversations c ON c.conversation_id = t.conversation_id
    WHERE t.conversation_id = '' OR c.conversation_id IS NOT NULL
), kept AS (
    SELECT DISTINCT ON (path) path, conversation_id, position, group_id, space, search, CASE WHEN search IS NOT NULL THEN gen_random_uuid() END AS search_page
    FROM incoming ORDER BY path, position
), tabs AS (
    INSERT INTO web_tabs (username, path, conversation_id, position, group_id, space, search_page)
    SELECT $1, path, conversation_id, position, group_id, space, search_page FROM kept
    RETURNING search_page
), written AS (
    SELECT k.search_page, k.search FROM tabs JOIN kept k USING (search_page)
), `+searchPagesSQL+`
INSERT INTO web_tab_groups (username, id, position, name, color, collapsed)
SELECT $1, id, n - 1, name, color, collapsed FROM unnest($2::text[], $3::text[], $4::text[], $5::boolean[]) WITH ORDINALITY AS g(id, name, color, collapsed, n)`,
		username, groupIDs, names, colors, collapsed, paths, conversations, groups, spaces, searches); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit tab state: %w", err)
	}

	return true, nil
}

func (s *SessionService) ClosedTabs(ctx context.Context, username string) ([]Tab, error) {
	return queryRows(ctx, s.db, `SELECT t.path, t.space, `+searchPageSQL+` FROM web_closed_tabs t
`+searchPageJoinSQL+`
WHERE t.username = $1 GROUP BY t.username, t.path, p.id ORDER BY t.closed_at DESC, t.path`, "closed tabs", func(row rowScanner) (Tab, error) {
		var tab Tab
		return tab, row.Scan(&tab.Path, &tab.Space, &tab.Search)
	}, username)
}

type SharedTab struct {
	Space, Path string
	Search      *SearchPage
}

func (s *SessionService) SharedTabs(ctx context.Context) ([]SharedTab, error) {
	return queryRows(ctx, s.db, `SELECT t.space, t.path, `+searchPageSQL+` FROM web_shared_tabs t
`+searchPageJoinSQL+`
GROUP BY t.space, t.path, p.id ORDER BY t.added_at, t.path`, "shared tabs", func(row rowScanner) (SharedTab, error) {
		var tab SharedTab
		return tab, row.Scan(&tab.Space, &tab.Path, &tab.Search)
	})
}

func (s *SessionService) ShareTab(ctx context.Context, space, path string, search *SearchPage) error {
	_, err := execState(ctx, s.db, "share tab", `WITH shared AS (
    INSERT INTO web_shared_tabs (space, path, conversation_id, search_page)
    SELECT w.id, COALESCE(`+tabPathSQL+`, $2), c.conversation_id, CASE WHEN $4::jsonb IS NOT NULL THEN gen_random_uuid() END FROM web_spaces w
    LEFT JOIN conversations c ON c.conversation_id = $3
    WHERE w.id = $1 AND ($3 = '' OR c.conversation_id IS NOT NULL)
    ON CONFLICT DO NOTHING
    RETURNING search_page
), written AS (
    SELECT search_page, $4::jsonb AS search FROM shared WHERE search_page IS NOT NULL
), `+searchPagesSQL+`
SELECT count(*) FROM written`, space, path, TabConversation(path), search)

	return err
}

func (s *SessionService) EditSharedSearch(ctx context.Context, space, path string, base, search *SearchPage) (bool, error) {
	tx, err := s.beginStateTx(ctx, "shared search edit")
	if err != nil {
		return false, err
	}

	defer func() { _ = tx.Rollback() }()

	if locked, err := execRows(ctx, tx, "lock shared search", `SELECT FROM web_shared_tabs WHERE space = $1 AND path = $2 FOR UPDATE`, space, path); err != nil || locked == 0 {
		return false, err
	}

	var edited int
	if err := tx.QueryRowContext(ctx, `WITH stored AS (
    SELECT t.space, t.path, `+searchPageSQL+` AS search FROM web_shared_tabs t
    `+searchPageJoinSQL+`
    WHERE t.space = $1 AND t.path = $2 GROUP BY t.space, t.path, p.id
), edited AS (
    UPDATE web_shared_tabs t SET search_page = CASE WHEN $3::jsonb IS NOT NULL THEN COALESCE(t.search_page, gen_random_uuid()) END
    FROM stored WHERE t.space = stored.space AND t.path = stored.path AND stored.search IS NOT DISTINCT FROM $4::jsonb
    RETURNING t.search_page
), written AS (
    SELECT search_page, $3::jsonb AS search FROM edited WHERE search_page IS NOT NULL
), `+searchPagesSQL+`
SELECT count(*) FROM edited`, space, path, search, base).Scan(&edited); err != nil {
		return false, fmt.Errorf("edit shared search: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit shared search edit: %w", err)
	}

	return edited > 0, nil
}

func (s *SessionService) UnshareTab(ctx context.Context, space, path string) (*SearchPage, error) {
	tx, err := s.beginStateTx(ctx, "unshare tab")
	if err != nil {
		return nil, err
	}

	defer func() { _ = tx.Rollback() }()

	if locked, err := execRows(ctx, tx, "lock shared tab", `SELECT FROM web_shared_tabs WHERE space = $1 AND path = $2 FOR UPDATE`, space, path); err != nil || locked == 0 {
		return nil, err
	}

	var page *SearchPage
	if err := tx.QueryRowContext(ctx, `WITH unshared AS (
    DELETE FROM web_shared_tabs WHERE space = $1 AND path = $2
)
SELECT `+searchPageSQL+` FROM web_shared_tabs t
`+searchPageJoinSQL+`
WHERE t.space = $1 AND t.path = $2 GROUP BY t.space, t.path, p.id`, space, path).Scan(&page); err != nil {
		return nil, fmt.Errorf("unshare tab: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit unshare tab: %w", err)
	}

	return page, nil
}

type TopShelf struct {
	Tabs      []ShelfTab
	Collapsed bool
}

type ShelfTab struct {
	Server, Space, Path string
	Search              *SearchPage
}

func (s *SessionService) TopShelf(ctx context.Context, username string) (TopShelf, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return TopShelf{}, fmt.Errorf("begin top shelf read: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var shelf TopShelf
	if err := queryScalar(ctx, tx, "read top shelf", `SELECT shelf_collapsed FROM web_tab_states WHERE username = $1`, &shelf.Collapsed, username); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TopShelf{}, err
	}

	shelf.Tabs, err = queryRows(ctx, tx, `SELECT t.server, t.space, t.path, `+searchPageSQL+` FROM web_top_shelves t
`+searchPageJoinSQL+`
WHERE t.username = $1 GROUP BY t.username, t.server, t.path, p.id ORDER BY t.position`, "top shelf tabs", func(row rowScanner) (ShelfTab, error) {
		var tab ShelfTab
		return tab, row.Scan(&tab.Server, &tab.Space, &tab.Path, &tab.Search)
	}, username)

	return shelf, err
}

func (s *SessionService) SaveTopShelf(ctx context.Context, username string, shelf *TopShelf) error {
	tx, err := s.beginStateTx(ctx, "top shelf save")
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := execState(ctx, tx, "save top shelf state", `INSERT INTO web_tab_states (username, revision, shelf_collapsed) VALUES ($1, 0, $2)
ON CONFLICT (username) DO UPDATE SET shelf_collapsed = excluded.shelf_collapsed`, username, shelf.Collapsed); err != nil {
		return err
	}

	if _, err := execState(ctx, tx, "clear top shelf", `DELETE FROM web_top_shelves WHERE username = $1`, username); err != nil {
		return err
	}

	var (
		servers, spaces, paths, conversations []string
		searches                              []*SearchPage
	)

	for _, tab := range shelf.Tabs {
		conversation := ""
		if tab.Server == "" {
			conversation = TabConversation(tab.Path)
		}

		servers, spaces, paths, conversations, searches = append(servers, tab.Server), append(spaces, tab.Space), append(paths, tab.Path), append(conversations, conversation), append(searches, tab.Search)
	}

	if _, err := execState(ctx, tx, "write top shelf", `WITH incoming AS (
    SELECT t.server, t.space, COALESCE(`+tabPathSQL+`, t.path) AS path, c.conversation_id, t.n - 1 AS position, t.search
    FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::jsonb[]) WITH ORDINALITY AS t(server, space, path, conversation_id, search, n)
    LEFT JOIN conversations c ON c.conversation_id = t.conversation_id
    WHERE t.conversation_id = '' OR c.conversation_id IS NOT NULL
), kept AS (
    SELECT DISTINCT ON (server, path) server, space, path, conversation_id, position, search, CASE WHEN search IS NOT NULL THEN gen_random_uuid() END AS search_page
    FROM incoming ORDER BY server, path, position
), shelf AS (
    INSERT INTO web_top_shelves (username, position, server, space, path, conversation_id, search_page)
    SELECT $1, position, server, space, path, conversation_id, search_page FROM kept
    RETURNING search_page
), written AS (
    SELECT k.search_page, k.search FROM shelf JOIN kept k USING (search_page)
), `+searchPagesSQL+`
SELECT count(*) FROM written`, username, servers, spaces, paths, conversations, searches); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit top shelf: %w", err)
	}

	return nil
}

func (s *SessionService) SetThreadAgentIfExists(conversationID, agent string) (bool, error) {
	rows, err := execRows(context.Background(), s.db, "update managed conversation agent", `UPDATE conversations SET agent = $1 WHERE conversation_id = $2`, strings.TrimSpace(agent), strings.TrimSpace(conversationID))
	return rows > 0, err
}

func (d stateDAO) externalMCPSession(ctx context.Context, externalConversationID string) (ExternalMCPSessionState, bool, error) {
	_, session, err := (externalMCPSessionScanner{}).scan(d.db.QueryRowContext(ctx, externalMCPSessionSQL+` AND external_id = $1`, strings.TrimSpace(externalConversationID)))
	if errors.Is(err, sql.ErrNoRows) {
		return ExternalMCPSessionState{}, false, nil
	}

	if err != nil {
		return ExternalMCPSessionState{}, false, fmt.Errorf("read external MCP session: %w", err)
	}

	return session, true, nil
}

func (s *SessionService) ExternalMCPSessionByConversationID(conversationID string) (externalConversationID string, session ExternalMCPSessionState, ok bool, err error) {
	externalConversationID, session, err = (externalMCPSessionScanner{}).scan(s.db.QueryRowContext(context.Background(), externalMCPSessionSQL+` AND (external_run = $1 OR conversation_id = $1)`, strings.TrimSpace(conversationID)))
	if errors.Is(err, sql.ErrNoRows) {
		return "", ExternalMCPSessionState{}, false, nil
	}

	if err != nil {
		return "", ExternalMCPSessionState{}, false, fmt.Errorf("read external MCP session by conversation ID: %w", err)
	}

	return externalConversationID, session, true, nil
}

const externalMCPSessionSQL = `SELECT external_id, external_agent, external_run, conversation_id, origin_pairs
FROM conversations WHERE external_id IS NOT NULL`

type externalMCPSessionScanner struct{}

func (externalMCPSessionScanner) scan(scanner rowScanner) (string, ExternalMCPSessionState, error) {
	var (
		externalConversationID string
		session                ExternalMCPSessionState
		privateConversationID  sql.NullString
		originPairs            []byte
	)
	if err := scanner.Scan(&externalConversationID, &session.Agent, &privateConversationID, &session.ManagedConversationID, &originPairs); err != nil {
		return "", ExternalMCPSessionState{}, fmt.Errorf("scan external MCP session: %w", err)
	}

	if err := json.Unmarshal(originPairs, &session.OriginPairs); err != nil {
		return "", ExternalMCPSessionState{}, fmt.Errorf("decode external MCP origin pairs: %w", err)
	}

	session.PrivateConversationID = privateConversationID.String

	return externalConversationID, session, nil
}

func originPairsJSON(metadata map[string]string) []byte {
	pairs := maps.Clone(metadata)
	maps.DeleteFunc(pairs, func(key, _ string) bool {
		return key == "external_conversation_id" || strings.HasPrefix(key, "rocketclaw_")
	})
	raw, _ := json.Marshal(pairs)

	return raw
}

func (d stateDAO) goal(ctx context.Context, conversationID string) (GoalState, bool, error) {
	var (
		goal                 GoalState
		createdAt, updatedAt int64
	)

	err := d.db.QueryRowContext(ctx, `SELECT goal_objective, goal_max_turns, goal_turns_used, goal_status, goal_note, goal_created_at_unix_ns, goal_updated_at_unix_ns, goal_principal_name, goal_principal_email, goal_origin
FROM conversations WHERE conversation_id = $1 AND goal_objective IS NOT NULL`, strings.TrimSpace(conversationID)).Scan(&goal.Objective, &goal.MaxTurns, &goal.TurnsUsed, &goal.Status, &goal.Note, &createdAt, &updatedAt, &goal.PrincipalName, &goal.PrincipalEmail, &goal.Origin)
	if errors.Is(err, sql.ErrNoRows) {
		return GoalState{}, false, nil
	}

	if err != nil {
		return GoalState{}, false, fmt.Errorf("read goal: %w", fmt.Errorf("scan goal: %w", err))
	}

	goal.CreatedAt = timeFromUnixNano(createdAt)

	goal.UpdatedAt = timeFromUnixNano(updatedAt)
	if strings.TrimSpace(goal.Status) == "" {
		goal.Status = GoalStatusActive
	}

	return goal, true, nil
}

const remindersSQL = `reminders AS (
    SELECT id, conversation_id, agent, message, next_due_unix_ns, TRUE AS recurring, interval_ns, origin FROM schedules WHERE cron_path IS NULL
    UNION ALL SELECT input_id, conversation_id, agent, message, run_at_unix_ns, FALSE, 0, origin FROM turns WHERE phase = 'waiting' AND reason = 'scheduled'
)`

func (d stateDAO) putScheduledMessage(ctx context.Context, id string, message *protocol.ScheduledMessageState) error {
	_, err := execState(ctx, d.db, "put scheduled message", `WITH recurring AS (
    INSERT INTO schedules (id, conversation_id, agent, message, next_due_unix_ns, interval_ns, origin) SELECT $1, $2, $3, $4, $5::bigint, $7::bigint, $12 WHERE $6::boolean
    ON CONFLICT (id) DO UPDATE SET conversation_id = excluded.conversation_id, agent = excluded.agent, message = excluded.message, next_due_unix_ns = excluded.next_due_unix_ns, interval_ns = excluded.interval_ns, origin = excluded.origin
)
INSERT INTO turns (input_id, conversation_id, agent, message, run_at_unix_ns, created_at_unix_ns, updated_at_unix_ns, phase, reason, delivery, origin)
SELECT $1, $2, $3, $4, $5, $8::bigint, $8, $9, $10, $11, $12 WHERE NOT $6
ON CONFLICT (input_id) WHERE phase = 'waiting' DO UPDATE SET conversation_id = excluded.conversation_id, agent = excluded.agent, message = excluded.message, run_at_unix_ns = excluded.run_at_unix_ns, origin = excluded.origin WHERE turns.reason = excluded.reason`, strings.TrimSpace(id), strings.TrimSpace(message.ConversationID), strings.TrimSpace(message.Agent), message.Message,
		timeUnixNano(message.DueAt), message.Recurring, int64(message.Interval), timeUnixNano(time.Now()), turnWaiting, waitScheduled, protocol.InboundKindPrompt, message.Origin)

	return err
}

func (d stateDAO) scheduledMessages(ctx context.Context, conversationID string) (map[string]protocol.ScheduledMessageState, error) {
	query := `WITH ` + remindersSQL + ` SELECT * FROM reminders`
	args := []any{}

	if strings.TrimSpace(conversationID) != "" {
		query += ` WHERE conversation_id = $1`

		args = append(args, strings.TrimSpace(conversationID))
	}

	messages, err := queryMap(ctx, d.db, query, "scheduled messages", scheduledMessageScanner{}, args...)
	if err != nil || len(messages) == 0 {
		return nil, err
	}

	return messages, nil
}

func (d stateDAO) resetScheduledMessages(ctx context.Context, conversationID string) error {
	_, err := execState(ctx, d.db, "reset scheduled messages", `WITH parked AS (
    UPDATE turns SET park_after = '' WHERE conversation_id = $1 AND phase = 'waiting' AND park_after <> ''
), recurring AS (
    DELETE FROM schedules WHERE conversation_id = $1
)
DELETE FROM turns WHERE conversation_id = $1 AND phase = 'waiting' AND reason = $2`, strings.TrimSpace(conversationID), waitScheduled)

	return err
}

func (s *SessionService) PutThreadQueueItem(id string, item *protocol.ThreadQueueItem) error {
	if err := putThreadQueueItem(context.Background(), s.db, id, item); err != nil {
		return fmt.Errorf("store waiting input: %w", err)
	}

	return nil
}

func putThreadQueueItem(ctx context.Context, db stateStoreDB, id string, item *protocol.ThreadQueueItem) error {
	kind := cmp.Or(item.Kind, protocol.InboundKindEnqueue)

	content, _ := json.Marshal(item.Content)
	reply, _ := json.Marshal(item.SlackReply)

	var inbound *string

	if item.Inbound != nil {
		data, _ := json.Marshal(item.Inbound)

		inbound = new(string(removeSessionEntryNUL(data)))
	}

	changed, err := execRows(ctx, db, "put thread queue item", `INSERT INTO turns (`+waitingColumns+`, updated_at_unix_ns, phase, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $5, $15, CASE WHEN $10 <> 'held' THEN $16 WHEN $14::json->>'Kind' = 'enqueue' THEN $17 ELSE $18 END)
ON CONFLICT (input_id) WHERE phase = 'waiting' DO UPDATE SET message = excluded.message, principal = excluded.principal, created_at_unix_ns = excluded.created_at_unix_ns, updated_at_unix_ns = excluded.updated_at_unix_ns, position = excluded.position, park_after = excluded.park_after, slack_channel = excluded.slack_channel, slack_ts = excluded.slack_ts, delivery = excluded.delivery, reason = excluded.reason, content = excluded.content, source = excluded.source, slack_reply = excluded.slack_reply, inbound_json = excluded.inbound_json WHERE turns.conversation_id = excluded.conversation_id`,
		strings.TrimSpace(id), strings.TrimSpace(item.ConversationID), item.Message, item.Principal, timeUnixNano(item.StashAt), item.Position, strings.TrimSpace(item.ParkAfter), item.SlackChannel, item.SlackTS, kind, content, item.Source, reply, inbound, turnWaiting, waitQueued, waitHeld, waitStashed)
	if err != nil {
		return err
	}

	if changed == 0 {
		return errors.New("queue item ID belongs to another conversation")
	}

	return nil
}

const waitingColumns = `input_id, conversation_id, message, principal, created_at_unix_ns, position, park_after, slack_channel, slack_ts, delivery, content, source, slack_reply, inbound_json`

func (s *SessionService) ThreadQueueForConversation(conversationID string) ([]protocol.ThreadQueueItem, error) {
	return queryRows(context.Background(), s.db, `SELECT `+waitingColumns+` FROM turns WHERE conversation_id = $1 AND phase = 'waiting' AND reason <> $2 ORDER BY position, created_at_unix_ns`, "thread queue", scanThreadQueueItem, strings.TrimSpace(conversationID), waitScheduled)
}

func (s *SessionService) DeleteThreadQueueItem(ctx context.Context, conversationID, id string) (removed bool, owner string, err error) {
	owners, err := queryStrings(ctx, s.db, `WITH gone AS (
    DELETE FROM turns WHERE conversation_id = $1 AND input_id = $2 AND phase = 'waiting' RETURNING conversation_id, input_id
), settled AS (
    UPDATE turns r SET phase = 'done', terminal = $3, note = $4, wake = TRUE, updated_at_unix_ns = $5 FROM gone g
    WHERE r.kind = 'reply' AND r.phase = 'running' AND r.reply_target = g.conversation_id AND r.input_id = g.input_id RETURNING r.conversation_id
)
SELECT COALESCE(s.conversation_id, '') FROM gone LEFT JOIN settled s ON TRUE`, "delete thread queue item", conversationID, id, backgroundRemoved, notePending, timeUnixNano(time.Now()))
	if err != nil || len(owners) == 0 {
		return false, "", err
	}

	return true, owners[0], nil
}

func (d stateDAO) claimThreadQueueItem(ctx context.Context, conversationID, id string) (protocol.ThreadQueueItem, bool, error) {
	item, err := scanThreadQueueItem(d.db.QueryRowContext(ctx, `DELETE FROM turns WHERE conversation_id = $1 AND input_id = $2 AND phase = 'waiting' AND reason = $3 RETURNING `+waitingColumns, conversationID, id, waitQueued))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return item, false, fmt.Errorf("claim thread queue item: %w", err)
	}

	return item, err == nil, nil
}

func scanThreadQueueItem(scanner rowScanner) (protocol.ThreadQueueItem, error) {
	var (
		item           protocol.ThreadQueueItem
		stashAt        int64
		content, reply []byte
		inbound        []byte
	)

	if err := scanner.Scan(&item.ID, &item.ConversationID, &item.Message, &item.Principal, &stashAt, &item.Position, &item.ParkAfter, &item.SlackChannel, &item.SlackTS, &item.Kind, &content, &item.Source, &reply, &inbound); err != nil {
		return protocol.ThreadQueueItem{}, fmt.Errorf("scan thread queue item: %w", err)
	}

	if inbound != nil {
		if err := json.Unmarshal(inbound, &item.Inbound); err != nil {
			return item, fmt.Errorf("decode thread queue inbound: %w", err)
		}
	}

	if err := json.Unmarshal(content, &item.Content); err != nil {
		return item, fmt.Errorf("decode thread queue content: %w", err)
	}

	if err := json.Unmarshal(reply, &item.SlackReply); err != nil {
		return item, fmt.Errorf("decode thread queue reply: %w", err)
	}

	item.StashAt = timeFromUnixNano(stashAt)

	return item, nil
}

type turnPhase string

const (
	turnWaiting    turnPhase = "waiting"
	turnRunning    turnPhase = "running"
	turnDelivering turnPhase = "delivering"
	turnDone       turnPhase = "done"
)

type waitReason string

const (
	waitQueued    waitReason = "queued"
	waitStashed   waitReason = "stashed"
	waitHeld      waitReason = "held"
	waitScheduled waitReason = "scheduled"
)

type activeTurn struct {
	id, conversationID string
	phase              turnPhase
	inbound            *protocol.InboundMessage
	outbound           *protocol.OutboundMessage
}

type turnFinish struct {
	conversationID string
	entries        []harness.SessionEntry
	accountGoal    bool
	outbound       *protocol.OutboundMessage
	terminal       protocol.Terminal
	hidden         bool
	report         string
	target         string
	schedules      map[string]protocol.ScheduledMessageState
	retry          int
}

type conversationJournal struct {
	store          *SessionService
	conversationID string
	log            *slog.Logger
}

func (s *SessionService) LoadTurnStep(ctx context.Context, conversationID, key string) (value json.RawMessage, found bool, err error) {
	err = queryScalar(ctx, s.db, "load turn step", `SELECT value FROM steps WHERE conversation_id = $1 AND key = $2`, &value, conversationID, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, err
	}

	return value, true, nil
}

func (s *SessionService) SaveTurnStep(ctx context.Context, conversationID, key string, value json.RawMessage) error {
	_, err := execState(ctx, s.db, "save turn step", `INSERT INTO steps (conversation_id, key, value) VALUES ($1, $2, $3) ON CONFLICT (conversation_id, key) DO UPDATE SET value = excluded.value`, conversationID, key, string(removeSessionEntryNUL(value)))
	return err
}

func (j conversationJournal) Load(ctx context.Context, key string) (value json.RawMessage, found bool, err error) {
	return j.store.LoadTurnStep(ctx, j.conversationID, key)
}

func (j conversationJournal) Save(ctx context.Context, key string, value json.RawMessage) error {
	return j.store.SaveTurnStep(ctx, j.conversationID, key, value)
}

func (j conversationJournal) SaveTurn(ctx context.Context, turnID string, step json.RawMessage, lists []harness.TurnItems) error {
	keys, values, ends := []string{turnID}, []string{string(removeSessionEntryNUL(step))}, make([]string, 0, len(lists))

	for _, list := range lists {
		for i, item := range list.Items {
			keys, values = append(keys, fmt.Sprintf("%s/%s/%010d", turnID, list.List, list.From+i)), append(values, string(removeSessionEntryNUL(item)))
		}

		ends = append(ends, fmt.Sprintf("%s/%s/%010d", turnID, list.List, list.From+len(list.Items)))
	}

	rowID, _, _ := strings.Cut(turnID, "/")

	_, err := execState(ctx, j.store.db, "save turn", `WITH ended AS (
    SELECT id FROM turns WHERE id = $5 AND conversation_id = $1 AND terminal <> ''
), dropped AS (
    DELETE FROM steps s USING unnest($4::text[]) e(key) LEFT JOIN ended d ON TRUE
    WHERE d.id IS NULL AND s.conversation_id = $1 AND starts_with(s.key, left(e.key, -10)) AND s.key >= e.key AND s.key <= left(e.key, -10) || '9999999999'
)
INSERT INTO steps (conversation_id, key, value) SELECT $1, key, value::json FROM unnest($2::text[], $3::text[]) AS saved(key, value)
LEFT JOIN ended d ON TRUE WHERE d.id IS NULL
ON CONFLICT (conversation_id, key) DO UPDATE SET value = excluded.value`, j.conversationID, keys, values, ends, rowID)

	return err
}

func (j conversationJournal) Items(ctx context.Context, turnID string, list harness.TurnList) ([]json.RawMessage, error) {
	return queryRows(ctx, j.store.db, `SELECT value FROM steps WHERE conversation_id = $1 AND starts_with(key, $2) ORDER BY key`, "turn items", scanJSON, j.conversationID, fmt.Sprintf("%s/%s/", turnID, list))
}

func (j conversationJournal) SaveTrace(ctx context.Context, turnID string, trace []json.RawMessage) (err error) {
	startedAt := time.Now()

	defer func() {
		observed := harness.PublicProgressFromTrace(trace)
		for i := range observed {
			progress := &observed[i]
			if progress.Kind != harness.PublicProgressText && progress.Kind != harness.PublicProgressThinking {
				j.log.Info("operation observed", "event", "operation_snapshot", "conversation_id", j.conversationID, "turn_id", turnID, "operation_id", progress.ID, "parent_id", progress.ParentID, "kind", progress.Kind, "state", progress.State, "boundary", "journal_observation", "observation_elapsed_ms", time.Since(startedAt).Milliseconds(), "error_type", fmt.Sprintf("%T", err))
			}
		}
	}()

	rowID, _, _ := strings.Cut(turnID, "/")

	data, err := json.Marshal(trace)
	if err != nil {
		return fmt.Errorf("encode turn trace: %w", err)
	}

	_, errExec := execState(ctx, j.store.db, "save turn trace", `UPDATE turns SET output_trace_json = $2, updated_at_unix_ns = $3 WHERE id = $1 AND terminal = '' AND output_trace_json::jsonb IS DISTINCT FROM $2::jsonb`, rowID, string(removeSessionEntryNUL(data)), timeUnixNano(time.Now()))

	return errExec
}

func (j conversationJournal) SaveCodeModeCall(ctx context.Context, turnID, key string, record json.RawMessage) error {
	rowID, _, _ := strings.Cut(turnID, "/")

	_, err := execState(ctx, j.store.db, "save code mode call", `WITH turn AS (
    SELECT id, conversation_id FROM turns WHERE id = $1 AND terminal = ''
), saved AS (
    INSERT INTO steps (conversation_id, key, value) SELECT conversation_id, id || '/code_mode/' || $2, $3 FROM turn
    ON CONFLICT (conversation_id, key) DO UPDATE SET value = excluded.value RETURNING key
)
UPDATE turns a SET output_trace_json = a.output_trace_json, updated_at_unix_ns = $4 FROM saved WHERE a.id = $1`,
		rowID, key, string(removeSessionEntryNUL(record)), timeUnixNano(time.Now()))

	return err
}

func (j conversationJournal) CodeModeCalls(ctx context.Context, turnID string) ([]json.RawMessage, error) {
	rowID, _, _ := strings.Cut(turnID, "/")

	return queryRows(ctx, j.store.db, `SELECT s.value FROM turns a
JOIN steps s ON s.conversation_id = a.conversation_id AND starts_with(s.key, a.id || '/code_mode/' || $2 || '/call/') WHERE a.id = $1 ORDER BY s.id`, "code mode calls", scanJSON, rowID, turnID)
}

func startTurnDB(ctx context.Context, db stateStoreDB, turnID, conversationID string, inbound *protocol.InboundMessage) error {
	data, _ := json.Marshal(inbound)

	if hiddenRun(inbound) {
		var attached bool
		if err := db.QueryRowContext(ctx, `WITH bound AS (
    UPDATE conversations SET producer_inbound_json = $2, reports_to = COALESCE(reports_to, NULLIF($3, ''))
    WHERE conversation_id = $1 RETURNING old.reports_to IS NULL AND new.reports_to IS NOT NULL AS attached
), unsearched AS (
    UPDATE entries e SET search_text = NULL FROM bound b WHERE b.attached AND e.conversation_id = $1 AND e.search_text IS NOT NULL
)
SELECT COALESCE(bool_or(attached), FALSE) FROM bound`, conversationID, string(removeSessionEntryNUL(data)), inbound.SyncDestination).Scan(&attached); err != nil {
			return fmt.Errorf("retain producer routing: %w", err)
		}

		if attached {
			if _, err := execState(ctx, db, "refresh attached chat entry facts", entryFactsSQL+`
UPDATE conversations SET has_entries = f.has_entries, first_source = f.first_source FROM facts f WHERE conversation_id = $1`, inbound.SyncDestination); err != nil {
				return err
			}
		}
	}

	now := timeUnixNano(time.Now())
	if _, err := db.ExecContext(ctx, `WITH runs AS (
    SELECT conversation_id FROM conversations WHERE reports_to = $2
    UNION SELECT continues FROM conversations WHERE conversation_id = $2 AND continues IS NOT NULL
), shown AS (
    SELECT max(id) AS id FROM entries WHERE conversation_id = $2
    UNION ALL SELECT max(e.id) FROM runs r JOIN entries e USING (conversation_id)
), running AS (
    UPDATE conversations SET running = TRUE WHERE conversation_id = $2
)
INSERT INTO turns (id, conversation_id, inbound_json, output_trace_json, created_at_unix_ns, updated_at_unix_ns, history_anchor_id)
SELECT $1, $2, $3, '[]', $4, $4, COALESCE(max(id), 0) FROM shown
ON CONFLICT (id) DO UPDATE SET inbound_json = excluded.inbound_json, output_trace_json = excluded.output_trace_json, created_at_unix_ns = excluded.created_at_unix_ns,
    updated_at_unix_ns = excluded.updated_at_unix_ns, history_anchor_id = excluded.history_anchor_id`, turnID, conversationID, string(removeSessionEntryNUL(data)), now); err != nil {
		return fmt.Errorf("start active turn: %w", err)
	}

	return nil
}

const laneTurnSQL = `SELECT a.id, a.conversation_id, a.phase, a.inbound_json, a.outbound_json FROM turns a
LEFT JOIN conversations m ON m.conversation_id = a.conversation_id
WHERE a.phase NOT IN ('waiting', 'done') AND a.takes_lane AND COALESCE(m.reports_to, a.conversation_id) = $1 ORDER BY a.created_at_unix_ns`

func (s *SessionService) headTurn(ctx context.Context, workerID string) (activeTurn, bool, error) {
	var (
		turn              activeTurn
		inbound, outbound []byte
	)

	err := s.db.QueryRowContext(ctx, laneTurnSQL+` LIMIT 1`, workerID).Scan(&turn.id, &turn.conversationID, &turn.phase, &inbound, &outbound)
	if errors.Is(err, sql.ErrNoRows) {
		return activeTurn{}, false, nil
	}

	if err != nil {
		return activeTurn{}, false, fmt.Errorf("load active turn: %w", err)
	}

	if err := json.Unmarshal(inbound, &turn.inbound); err != nil {
		return activeTurn{}, false, fmt.Errorf("decode active turn inbound: %w", err)
	}

	if outbound != nil {
		if err := json.Unmarshal(outbound, &turn.outbound); err != nil {
			return activeTurn{}, false, fmt.Errorf("decode active turn outbound: %w", err)
		}

		if err := s.loadAttachmentData(ctx, turn.outbound.Attachments); err != nil {
			return activeTurn{}, false, err
		}
	}

	return turn, true, nil
}

func (s *SessionService) HasActiveTurn(ctx context.Context, conversationID string) (bool, error) {
	var active bool
	if err := queryScalar(ctx, s.db, "check active turn", `SELECT count(*) > 0 FROM turns WHERE conversation_id = $1 AND phase NOT IN ('waiting', 'done') AND takes_lane`, &active, conversationID); err != nil {
		return false, err
	}

	return active, nil
}

func (s *SessionService) TurnProgress(ctx context.Context, conversationID, turnID string) (protocol.TurnProgress, error) {
	var (
		progress protocol.TurnProgress
		raw      []byte
	)

	rowID, _, _ := strings.Cut(turnID, "/")

	err := s.db.QueryRowContext(ctx, `WITH questions AS (
    SELECT count(*) AS waiting FROM steps q
    LEFT JOIN steps a ON a.conversation_id = q.conversation_id AND a.key = left(q.key, -length('`+webQuestionStep+`')) || '/answer'
    WHERE q.conversation_id = $1 AND q.key LIKE '%`+webQuestionStep+`' AND a.key IS NULL
)
SELECT t.phase NOT IN ('waiting', 'done'), q.waiting > 0, COALESCE(t.output_trace_json::text, '[]')
FROM turns t CROSS JOIN questions q WHERE t.conversation_id = $1 AND t.id = $2`, conversationID, rowID).Scan(&progress.Running, &progress.Asking, &raw)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !progress.Running {
		return protocol.TurnProgress{}, nil
	}

	if err != nil {
		return protocol.TurnProgress{}, fmt.Errorf("read turn progress: %w", err)
	}

	progress.Steps, err = queryRows(ctx, s.db, `WITH items AS MATERIALIZED (
    SELECT key, value::jsonb AS item FROM steps WHERE conversation_id = $1 AND starts_with(key, $2)
), calls AS (
    SELECT key, item->>'call_id' AS call_id, item->>'name' AS name, item->>'arguments' AS arguments FROM items
    WHERE item->>'type' = 'function_call' AND item->>'name' IN ('execute', 'task')
    ORDER BY key DESC LIMIT 50
)
SELECT call_id, name, CASE WHEN pg_input_is_valid(arguments, 'jsonb') THEN COALESCE(arguments::jsonb->>'description', '') ELSE '' END
FROM calls ORDER BY key`, "turn steps", func(row rowScanner) (protocol.TurnStep, error) {
		var (
			step        protocol.TurnStep
			name, title string
		)

		if err := row.Scan(&step.ID, &name, &title); err != nil {
			return step, fmt.Errorf("scan turn step: %w", err)
		}

		step.Title = cmp.Or(strings.TrimSpace(title), name)

		return step, nil
	}, conversationID, turnID+"/"+string(harness.TurnReplayInput)+"/")
	if err != nil || len(progress.Steps) == 0 {
		return progress, err
	}

	var trace []json.RawMessage
	if err := json.Unmarshal(raw, &trace); err != nil {
		return protocol.TurnProgress{}, fmt.Errorf("read turn progress: %w", err)
	}

	states := map[string]harness.PublicProgressState{}

	observed := harness.PublicProgressFromTrace(trace)
	for i := range observed {
		states[observed[i].ID] = observed[i].State
	}

	for i := range progress.Steps {
		switch states[progress.Steps[i].ID] {
		case harness.PublicProgressCompleted, harness.PublicProgressBackground:
			progress.Steps[i].State = protocol.TurnStepDone
		case harness.PublicProgressFailed, harness.PublicProgressBlocked, harness.PublicProgressStopped:
			progress.Steps[i].State = protocol.TurnStepFailed
		case harness.PublicProgressWorking, harness.PublicProgressReview:
			progress.Steps[i].State = protocol.TurnStepRunning
		default:
			progress.Steps[i].State = protocol.TurnStepRunning
		}
	}

	return progress, nil
}

func (s *SessionService) finishTurn(ctx context.Context, turnID string, finish *turnFinish) (result *protocol.OutboundMessage, err error) {
	defer func() {
		if err != nil {
			err = inputPersistenceError{cause: err}
		}
	}()

	outbound, err := json.Marshal(finish.outbound)
	if err != nil {
		return nil, fmt.Errorf("encode final outbound: %w", err)
	}

	tx, err := s.beginStateTx(ctx, "turn finish")
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := lockRunHistories(ctx, tx, finish.conversationID); err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `SELECT id FROM shadow_turns WHERE child_conversation_id = $1 AND child_turn_id = $2 FOR UPDATE`, finish.conversationID, turnID); err != nil {
		return nil, fmt.Errorf("lock comparison settlement: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE shadow_turns SET input_closed = TRUE,
terminal = CASE WHEN terminal <> '' THEN terminal WHEN (child_turn_id = '' AND jsonb_array_length(inputs) = 0) OR $3 = 'stopped' THEN $3 ELSE terminal END
WHERE parent_source_conversation_id = $1 AND parent_turn_id = $2`, finish.conversationID, turnID, cmp.Or(finish.outbound.Terminal, protocol.TerminalComplete)); err != nil {
		return nil, inputPersistenceError{cause: fmt.Errorf("close mirrored input: %w", err)}
	}

	moved, err := execRows(ctx, tx, "finish active turn", `UPDATE turns SET phase = $2, terminal = $4, updated_at_unix_ns = $5, outbound_json = $3::json
WHERE id = $1 AND phase = $6`, turnID, turnDelivering, string(removeSessionEntryNUL(outbound)), finish.terminal, timeUnixNano(time.Now()), turnRunning)
	if err != nil {
		return nil, err
	}

	if moved == 0 {
		var stored protocol.OutboundMessage

		if err := tx.QueryRowContext(ctx, `SELECT outbound_json FROM turns WHERE id = $1`, turnID).Scan(&outbound); err != nil {
			return nil, fmt.Errorf("load finished turn outbound: %w", err)
		}

		if err := json.Unmarshal(outbound, &stored); err != nil {
			return nil, fmt.Errorf("decode finished turn outbound: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit late turn finish: %w", err)
		}

		return &stored, s.loadAttachmentData(ctx, stored.Attachments)
	}

	if _, err := tx.ExecContext(ctx, `WITH replay AS (
    SELECT json_agg(i.value ORDER BY i.key) AS replay FROM turns a
    JOIN steps i ON i.conversation_id = $1 AND starts_with(i.key, $2 || '/replay_input/') WHERE a.id = $2 AND a.terminal <> ''
)
UPDATE turns a SET replay = r.replay, record = CASE WHEN a.terminal <> '' THEN (`+turnRecordSQL+`)::json END, applied_input_ids = s.value::jsonb->'applied_input_ids'
FROM steps s, replay r WHERE a.id = $2 AND s.conversation_id = $1 AND s.key = $2`, finish.conversationID, turnID); err != nil {
		return nil, fmt.Errorf("move turn record: %w", err)
	}

	if err := s.markTurnEnd(ctx, tx, turnID, finish); err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE turns a SET output_trace_json = (a.output_trace_json::jsonb || jsonb_build_array(jsonb_build_object('type', 'rocketcode_public_progress',
    'progress', jsonb_build_object('id', 'shadow-step-limit', 'kind', 'text', 'state', $2::text, 'text', t.limit_note,
        'agent', COALESCE(a.record::jsonb->>'agent', ''), 'model', COALESCE(a.record::jsonb->>'model', '')))))::text
FROM shadow_turns t WHERE a.id = $1 AND t.child_turn_id = $1 AND $2 = 'stopped' AND t.limit_note <> ''`, turnID, finish.terminal); err != nil {
		return nil, fmt.Errorf("note shadow step limit: %w", err)
	}

	if err := finish.preserveStoppedInput(ctx, tx, turnID); err != nil {
		return nil, err
	}

	for i := range finish.entries {
		if _, err := appendSessionEntryDB(ctx, tx, finish.conversationID, &finish.entries[i]); err != nil {
			return nil, err
		}
	}

	if err := finish.applyEffects(ctx, tx, turnID); err != nil {
		return nil, err
	}

	if finish.terminal != "" {
		if err := s.indexTurnMessages(ctx, tx, finish.conversationID, turnID); err != nil {
			return nil, err
		}
	}

	owners, err := finish.settleInputs(ctx, tx, turnID)
	if err != nil {
		return nil, err
	}

	if finish.accountGoal {
		if _, err := tx.ExecContext(ctx, `UPDATE conversations SET goal_turns_used = goal_turns_used + 1, goal_status = CASE WHEN goal_max_turns > 0 AND goal_turns_used + 1 >= goal_max_turns THEN $1 ELSE $2 END, goal_updated_at_unix_ns = $3 WHERE conversation_id = $4 AND (goal_status = '' OR goal_status = $2)`, GoalStatusBudgetExhausted, GoalStatusActive, timeUnixNano(time.Now()), finish.conversationID); err != nil {
			return nil, fmt.Errorf("account goal turn: %w", err)
		}
	}

	var errSettle error

	switch {
	case len(finish.entries) > 0:
		_, errSettle = tx.ExecContext(ctx, `UPDATE turns SET note = $3 WHERE conversation_id = $1 AND claimed_by = $2 AND note = 'pending'`, finish.conversationID, turnID, noteConsumed)
	case finish.hidden:
		errSettle = tx.QueryRowContext(ctx, `WITH released AS (UPDATE turns SET claimed_by = '', wake = TRUE, wake_attempts = wake_attempts + 1,
run_at_unix_ns = $4 + ($5::bigint[])[LEAST(wake_attempts + 1, 3)] WHERE conversation_id = $1 AND claimed_by = $2 AND note = $3 RETURNING wake_attempts)
SELECT COALESCE(MIN(wake_attempts), 0) FROM released`, finish.conversationID, turnID, notePending, timeUnixNano(time.Now()), []int64{int64(time.Minute), int64(5 * time.Minute), int64(30 * time.Minute)}).Scan(&finish.retry)
	default:
		_, errSettle = tx.ExecContext(ctx, `UPDATE turns SET claimed_by = '', wake = FALSE WHERE conversation_id = $1 AND claimed_by = $2 AND note = $3`, finish.conversationID, turnID, notePending)
	}

	if errSettle != nil {
		return nil, fmt.Errorf("settle background note claims: %w", errSettle)
	}

	if _, err := tx.ExecContext(ctx, deleteUnownedStepsSQL+`t.conversation_id = $1 AND (t.key = $2 OR starts_with(t.key, $2 || '/'))`, finish.conversationID, turnID); err != nil {
		return nil, fmt.Errorf("clear turn steps: %w", err)
	}

	if err := releaseShadowPayloads(ctx, tx, finish.conversationID, turnID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit turn finish: %w", err)
	}

	s.jobs.turnFinished(owners)

	return finish.outbound, nil
}

func (s *SessionService) markTurnEnd(ctx context.Context, tx *sql.Tx, turnID string, finish *turnFinish) error {
	var cronPath, space, agent string
	if err := tx.QueryRowContext(ctx, `WITH run AS (
    SELECT c.cron_path, t.inbound_json->'Cronjob'->>'Space' AS space, t.inbound_json->'Cronjob'->>'Agent' AS agent
    FROM conversations c JOIN turns t ON t.id = $4 WHERE c.conversation_id = $1 AND c.kind IN ($2, $3)
), runs AS (
    SELECT $1::text AS conversation_id
    UNION ALL SELECT conversation_id FROM conversations WHERE reports_to = $1
), pending AS (
    SELECT count(*) AS n FROM turns t JOIN runs USING (conversation_id)
    WHERE t.id <> $4 AND (t.phase = 'running' OR t.phase = 'waiting' AND t.reason = 'queued' OR NOT t.takes_lane AND t.note = 'pending')
)
SELECT COALESCE(max(r.cron_path), ''), COALESCE(max(r.space), ''), COALESCE(max(r.agent), '') FROM run r CROSS JOIN pending p WHERE p.n = 0`,
		finish.conversationID, KindCronRun, KindOneOffCronRun, turnID).Scan(&cronPath, &space, &agent); err != nil {
		return fmt.Errorf("read cron run: %w", err)
	}

	if cronPath != "" {
		stem := CronStem(cronPath)

		if space == "" {
			agents, _, err := LoadRuntimeDefinitions(s.definitions, s.definitions.Clone().RuntimeDirName())
			if err != nil {
				return fmt.Errorf("load cron agents: %w", err)
			}

			space = agents.Items[agent].Space
		}

		if err := s.markLocation(ctx, tx, "/cron", space, "Run finished", func(rest string) bool { return decodeSessionID(rest) == stem }); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `WITH turn AS (
    SELECT a.id, c.conversation_id, c.kind, a.inbound_json, FALSE AS waking FROM turns a JOIN conversations c ON c.conversation_id = $1 AND c.listed WHERE a.id = $2
)`+turnEventsSQL+sessionReadersSQL+markTabsSQL+`
SELECT count(*) FROM marked`, finish.conversationID, turnID); err != nil {
		return fmt.Errorf("mark turn readers unread: %w", err)
	}

	return nil
}

func (f *turnFinish) settleInputs(ctx context.Context, tx stateStoreDB, turnID string) ([]string, error) {
	released, err := execRows(ctx, tx, "release recorded input ownership", `WITH queued AS (
    SELECT input_id FROM turns WHERE conversation_id = $1 AND phase = 'waiting'
), recorded AS (
    SELECT q.input_id FROM queued q JOIN entries e ON e.conversation_id = $1
    CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item WHERE item->>'input_id' = q.input_id
    UNION ALL SELECT q.input_id FROM queued q JOIN turns a ON a.id = $2 AND a.applied_input_ids ? q.input_id
)
DELETE FROM turns q USING recorded r WHERE q.conversation_id = $1 AND q.phase = 'waiting' AND q.input_id = r.input_id`, f.conversationID, turnID)
	if err != nil {
		return nil, err
	}

	answer := ""

	for i := range f.entries {
		text, err := turnAnswer(&f.entries[i])
		if err != nil {
			return nil, err
		}

		answer = appendText(answer, text)
	}

	owners, err := queryStrings(ctx, tx, `WITH turn AS (
    SELECT conversation_id, inbound_json->'Metadata'->>'web_message_id' AS input, applied_input_ids FROM turns WHERE id = $1
), settled AS (
    UPDATE turns r SET phase = 'done', terminal = $2, note = $3, wake = TRUE, result = $4, updated_at_unix_ns = $5 FROM turn t
    WHERE r.kind = 'reply' AND r.phase = 'running' AND r.reply_target = t.conversation_id AND (r.input_id = t.input OR t.applied_input_ids ? r.input_id)
    RETURNING r.conversation_id
)
SELECT DISTINCT conversation_id FROM settled`, "settle session replies", turnID, f.terminal, notePending, strings.ReplaceAll(answer, "\x00", ""), timeUnixNano(time.Now()))
	if err != nil {
		return nil, err
	}

	if released == 0 && f.terminal != protocol.TerminalStopped {
		result := f.report
		if f.terminal != "" {
			result = f.outbound.Text
		}

		parents, err := queryStrings(ctx, tx, `WITH wake AS (
    SELECT conversation_id FROM turns WHERE id = $1 AND inbound_json->'Metadata'->>$2::text <> ''
), subagent AS (
    SELECT c.parent_conversation_id AS parent, c.agent FROM conversations c JOIN wake USING (conversation_id) WHERE c.parent_conversation_id IS NOT NULL
)
INSERT INTO turns (conversation_id, input_id, kind, takes_lane, phase, terminal, agent, label, call_id, subagent_key, inbound_json, runner_id, note, wake, wake_attempts, run_at_unix_ns, claimed_by, created_at_unix_ns, updated_at_unix_ns, result, message)
SELECT s.parent, $1, $3, FALSE, 'done', $4, '', s.agent, '', substr(w.conversation_id, length(s.parent) + 1), '{}', '', $5, TRUE, 0, 0, '', $6, $6, $7, ''
FROM subagent s CROSS JOIN wake w
ON CONFLICT (conversation_id, input_id) WHERE NOT takes_lane DO NOTHING RETURNING conversation_id`, "report subagent wake", turnID, backgroundNotesMetadataKey, turnNote, f.terminal, notePending, timeUnixNano(time.Now()), strings.ReplaceAll(result, "\x00", ""))
		if err != nil {
			return nil, err
		}

		owners = append(owners, parents...)
	}

	return owners, nil
}

func (f *turnFinish) applyEffects(ctx context.Context, tx stateStoreDB, turnID string) error {
	if f.terminal != "" {
		return nil
	}

	var raw json.RawMessage

	err := tx.QueryRowContext(ctx, `SELECT value FROM steps WHERE conversation_id = $1 AND key = $2`, f.conversationID, turnID+producerEffectsStep).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("load producer effects: %w", err)
	}

	var effects []producerEffect
	if err := json.Unmarshal(raw, &effects); err != nil {
		return fmt.Errorf("load producer effects: %w", err)
	}

	f.target, f.schedules, err = (stateDAO{db: tx}).applyProducerEffects(ctx, f.conversationID, effects)

	return err
}

func (f *turnFinish) preserveStoppedInput(ctx context.Context, db stateStoreDB, turnID string) error {
	if f.terminal != protocol.TerminalStopped || len(f.entries) > 0 {
		return nil
	}

	_, err := appendQueriedEntries(ctx, db, f.conversationID, "stopped input", `SELECT `+turnEntrySQL+` FROM turns a
LEFT JOIN shadow_turns t ON t.child_conversation_id = a.conversation_id AND t.child_turn_id = a.id
WHERE a.id = $1 AND COALESCE(a.record->>'response_id', '') = '' AND COALESCE(t.inbound_json, a.inbound_json)->>'Human' = 'true'
    AND jsonb_array_length(a.applied_input_ids) > 0`, turnID)

	return err
}

const turnRecordSQL = `COALESCE(s.value::jsonb->'record', '{}'::jsonb) || jsonb_build_object('version', 1, 'type', 'turn', 'turn_id', a.id, 'output_trace', turn_trace(a.id, a.output_trace_json))`

const turnEntrySQL = `'{"replay_input":' || COALESCE(a.replay::text, 'null') || ',' || substr(a.record::text, 2)`

const turnReplaySQL = `turn_replay AS (
    SELECT a.id AS turn_id, i.value AS item, right(i.key, 10)::bigint AS n FROM turns a
    JOIN steps i ON i.conversation_id = a.conversation_id AND starts_with(i.key, a.id || '/replay_input/') WHERE a.conversation_id = $1 AND a.record IS NULL
    UNION ALL SELECT a.id, i.item, i.n FROM turns a CROSS JOIN LATERAL json_array_elements(readable_replay(a.replay)) WITH ORDINALITY AS i(item, n)
    WHERE a.conversation_id = $1
)`

func appendQueriedEntries(ctx context.Context, db stateStoreDB, conversationID, label, query string, args ...any) (int, error) {
	entries, err := queryRows(ctx, db, query, label, scanJSON, args...)
	if err != nil {
		return 0, err
	}

	for _, raw := range entries {
		var entry harness.SessionEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return 0, fmt.Errorf("decode %s: %w", label, err)
		}

		if _, err := appendSessionEntryDB(ctx, db, conversationID, &entry); err != nil {
			return 0, err
		}
	}

	return len(entries), nil
}

const deleteUnownedStepsSQL = `WITH unowned_steps AS (
    SELECT s.conversation_id, s.key FROM steps s LEFT JOIN turns j ON j.conversation_id = s.conversation_id
        AND NOT j.takes_lane AND j.phase = 'running' AND starts_with(j.input_id, $2 || '/') AND starts_with(s.key, j.input_id || '/')
    WHERE starts_with(s.key, $2) AND j.id IS NULL
)
DELETE FROM steps t USING unowned_steps u WHERE (t.conversation_id, t.key) = (u.conversation_id, u.key) AND `

func recordRunning(ctx context.Context, db stateStoreDB, ids []string) error {
	_, err := execState(ctx, db, "record running conversations", `WITH running AS (
    SELECT DISTINCT conversation_id FROM turns WHERE conversation_id = ANY($1) AND phase NOT IN ('waiting', 'done') AND takes_lane
), targets AS (
    SELECT id, r.conversation_id IS NOT NULL AS running FROM unnest($1::text[]) id LEFT JOIN running r ON r.conversation_id = id
)
UPDATE conversations c SET running = t.running FROM targets t WHERE c.conversation_id = t.id`, ids)

	return err
}

func (s *SessionService) closeTurn(ctx context.Context, turnID string) error {
	_, err := execState(ctx, s.db, "close active turn", `WITH replay AS (
    SELECT 1 FROM turns a JOIN entries e USING (conversation_id)
    CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
    WHERE a.id = $2 AND a.inbound_json->>'Source' = 'web' AND item->>'input_id' = a.inbound_json->'Metadata'->>'web_message_id' LIMIT 1
), replays AS (SELECT count(*) AS n FROM replay),
delivered AS (DELETE FROM turns USING replays WHERE id = $2 AND terminal = '' AND
    NOT (inbound_json->>'Source' = 'web' AND COALESCE(inbound_json->'Metadata'->>'web_message_id', '') <> '' AND
        (inbound_json->>'GoalAction' = 'goal' OR n = 0)) RETURNING id),
delivery_steps AS (`+deleteUnownedStepsSQL+`(t.key = $2 OR starts_with(t.key, $2 || '/'))),
turn AS (SELECT conversation_id FROM turns WHERE id = $2),
others AS (SELECT count(*) AS n FROM turns a JOIN turn t USING (conversation_id) WHERE a.id <> $2 AND a.phase NOT IN ('waiting', 'done') AND a.takes_lane),
idle AS (UPDATE conversations c SET running = o.n > 0 FROM turn t, others o WHERE c.conversation_id = t.conversation_id),
kept AS (SELECT count(*) = 0 AS undelivered FROM delivered)
UPDATE turns SET phase = $1 FROM kept WHERE id = $2 AND undelivered`, turnDone, turnID)

	return err
}

type turnKind string

const (
	turnScript turnKind = "script"
	turnNote   turnKind = "note"
	turnReply  turnKind = "reply"
)

func (k turnKind) tool() harness.BackgroundKind {
	return map[turnKind]harness.BackgroundKind{turnScript: harness.BackgroundKindExecute, turnNote: harness.BackgroundKindTask, turnReply: sessionsWriteToolName}[k]
}

type backgroundJobStatus string

const (
	backgroundRunning   backgroundJobStatus = "running"
	backgroundCompleted backgroundJobStatus = "completed"
	backgroundFailed    backgroundJobStatus = "failed"
	backgroundStopped   backgroundJobStatus = "stopped"
	backgroundKilled    backgroundJobStatus = "killed"
	backgroundRemoved   backgroundJobStatus = "removed"
)

type noteState string

const (
	noteNone     noteState = "none"
	notePending  noteState = "pending"
	noteConsumed noteState = "consumed"
)

type backgroundJob struct {
	conversationID, jobID string
	kind                  turnKind
	status                backgroundJobStatus
	agent, label, callID  string
	subagentKey           string
	origin                *protocol.InboundMessage
	runnerID              string
	noteState             noteState
	wake                  bool
	wakeAfter             time.Time
	claimedTurnID         string
	createdAt             time.Time
	finishedAt            time.Time
	result                string
	input                 string
	target                string
}

const backgroundJobColumns = `j.conversation_id, j.input_id, j.kind, CASE WHEN j.phase = 'running' THEN 'running' WHEN j.terminal = '' THEN 'completed' ELSE j.terminal END, j.agent, j.label, j.call_id, j.subagent_key, j.inbound_json, COALESCE(c.reports_to, ''), j.runner_id, j.note, j.wake, j.run_at_unix_ns, j.claimed_by, j.created_at_unix_ns, CASE WHEN j.phase = 'running' THEN 0 ELSE j.updated_at_unix_ns END, j.result, j.message, COALESCE(j.reply_target, '')`

func scanBackgroundJob(scanner rowScanner) (backgroundJob, error) {
	var (
		job                              backgroundJob
		origin                           []byte
		reportsTo                        string
		wakeAfter, createdAt, finishedAt int64
	)

	if err := scanner.Scan(&job.conversationID, &job.jobID, &job.kind, &job.status, &job.agent, &job.label, &job.callID, &job.subagentKey, &origin, &reportsTo, &job.runnerID, &job.noteState, &job.wake, &wakeAfter, &job.claimedTurnID, &createdAt, &finishedAt, &job.result, &job.input, &job.target); err != nil {
		return backgroundJob{}, fmt.Errorf("scan background job: %w", err)
	}

	if err := json.Unmarshal(origin, &job.origin); err != nil {
		return backgroundJob{}, fmt.Errorf("decode background job origin: %w", err)
	}

	job.origin.SyncDestination = cmp.Or(job.origin.SyncDestination, reportsTo)
	job.wakeAfter, job.createdAt, job.finishedAt = timeFromUnixNano(wakeAfter), timeFromUnixNano(createdAt), timeFromUnixNano(finishedAt)

	return job, nil
}

func createBackgroundJob(ctx context.Context, db stateStoreDB, job *backgroundJob) (stored backgroundJob, created bool, err error) {
	origin, _ := json.Marshal(job.origin)

	inserted, err := execRows(ctx, db, "create background job", `INSERT INTO turns (conversation_id, input_id, kind, takes_lane, agent, label, call_id, subagent_key, inbound_json, runner_id, note, wake, wake_attempts, run_at_unix_ns, claimed_by, created_at_unix_ns, updated_at_unix_ns, result, message, reply_target)
VALUES ($1, $2, $3, FALSE, $4, $5, $6, $7, $8, $9, $10, FALSE, 0, 0, '', $11, $11, '', $12, NULLIF($13, '')) ON CONFLICT (conversation_id, input_id) WHERE NOT takes_lane DO NOTHING`, job.conversationID, job.jobID, job.kind, job.agent, job.label, job.callID, job.subagentKey, string(removeSessionEntryNUL(origin)), job.runnerID, noteNone, timeUnixNano(time.Now()), strings.ReplaceAll(job.input, "\x00", ""), job.target)
	if err != nil {
		return backgroundJob{}, false, err
	}

	stored, err = scanBackgroundJob(db.QueryRowContext(ctx, `SELECT `+backgroundJobColumns+` FROM turns j LEFT JOIN conversations c USING (conversation_id) WHERE j.conversation_id = $1 AND j.input_id = $2 AND NOT j.takes_lane`, job.conversationID, job.jobID))

	return stored, inserted > 0, err
}

func (s *SessionService) finishBackgroundJob(ctx context.Context, job *backgroundJob) (bool, error) {
	tx, err := s.beginStateTx(ctx, "background job finish")
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	finished, err := execRows(ctx, tx, "finish background job", `UPDATE turns SET phase = 'done', terminal = CASE WHEN $4 = $9 THEN '' ELSE $4 END, note = $5, wake = $6, result = $7, updated_at_unix_ns = $8
WHERE conversation_id = $1 AND input_id = $2 AND NOT takes_lane AND runner_id = $3 AND phase = 'running'`, job.conversationID, job.jobID, job.runnerID, job.status, job.noteState, job.wake, strings.ReplaceAll(job.result, "\x00", ""), timeUnixNano(time.Now()), backgroundCompleted)
	if err != nil || finished == 0 {
		return false, err
	}

	if _, err := tx.ExecContext(ctx, deleteUnownedStepsSQL+`t.conversation_id = $1 AND starts_with(t.key, $2 || '/')`, job.conversationID, job.jobID); err != nil {
		return false, fmt.Errorf("clear background job steps: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit background job finish: %w", err)
	}

	return true, nil
}

func (s *SessionService) restampBackgroundJob(ctx context.Context, job *backgroundJob, runnerID string) (bool, error) {
	moved, err := execRows(ctx, s.db, "restamp background job", `UPDATE turns SET runner_id = $4 WHERE conversation_id = $1 AND input_id = $2 AND NOT takes_lane AND runner_id = $3 AND phase = 'running'`, job.conversationID, job.jobID, job.runnerID, runnerID)
	return moved > 0, err
}

func claimBackgroundNotes(ctx context.Context, db stateStoreDB, conversationID, turnID string, held bool) ([]backgroundJob, error) {
	return queryRows(ctx, db, `WITH unclaimed AS (SELECT id FROM turns WHERE conversation_id = $1 AND note = $3 AND claimed_by = '' FOR UPDATE SKIP LOCKED),
claimed AS (UPDATE turns t SET claimed_by = $2 FROM unclaimed u WHERE t.id = u.id RETURNING t.*), notes AS (SELECT * FROM claimed UNION ALL SELECT * FROM turns WHERE $4 AND conversation_id = $1 AND note = $3 AND claimed_by = $2)
SELECT `+backgroundJobColumns+` FROM notes j LEFT JOIN conversations c USING (conversation_id) ORDER BY j.updated_at_unix_ns, j.input_id`, "background note claims", scanBackgroundJob, conversationID, turnID, notePending, held)
}

func (s *SessionService) backgroundWake(ctx context.Context, conversationID string) (backgroundJob, bool, error) {
	job, err := scanBackgroundJob(s.db.QueryRowContext(ctx, `SELECT `+backgroundJobColumns+` FROM turns j LEFT JOIN conversations c USING (conversation_id)
WHERE j.conversation_id = $1 AND j.note = $2 AND j.wake AND j.claimed_by = '' ORDER BY GREATEST(j.run_at_unix_ns, $3), j.updated_at_unix_ns, j.input_id LIMIT 1`, conversationID, notePending, timeUnixNano(time.Now())))
	if errors.Is(err, sql.ErrNoRows) {
		return backgroundJob{}, false, nil
	}

	return job, err == nil, err
}

func (s *SessionService) notedBackgroundJobs(ctx context.Context, conversationID string, jobIDs []string) ([]backgroundJob, error) {
	return queryRows(ctx, s.db, `SELECT `+backgroundJobColumns+` FROM turns j LEFT JOIN conversations c USING (conversation_id) WHERE j.conversation_id = $1 AND NOT j.takes_lane AND j.input_id = ANY($2) ORDER BY j.updated_at_unix_ns, j.input_id`, "noted background jobs", scanBackgroundJob, conversationID, jobIDs)
}

func (s *SessionService) releaseStaleBackgroundClaims(ctx context.Context) error {
	_, err := execState(ctx, s.db, "release stale background note claims", `WITH running AS (SELECT id FROM turns WHERE phase = $2 AND takes_lane), stale AS (
    SELECT j.id FROM turns j LEFT JOIN running r ON r.id = j.claimed_by WHERE j.note = $1 AND j.claimed_by <> '' AND r.id IS NULL
)
UPDATE turns j SET claimed_by = '' FROM stale s WHERE j.id = s.id`, notePending, turnRunning)

	return err
}

func (s *SessionService) backgroundJobs(ctx context.Context, conversationID string) ([]backgroundJob, error) {
	return queryRows(ctx, s.db, `WITH owners AS (SELECT $1::text AS conversation_id UNION SELECT conversation_id FROM conversations WHERE reports_to = $1)
SELECT `+backgroundJobColumns+` FROM owners JOIN turns j USING (conversation_id) LEFT JOIN conversations c USING (conversation_id) WHERE NOT j.takes_lane AND (j.phase = 'running' OR j.note = $2) ORDER BY j.created_at_unix_ns, j.input_id`, "background jobs", scanBackgroundJob, conversationID, notePending)
}

func (s *SessionService) runningBackgroundJobs(ctx context.Context) ([]backgroundJob, error) {
	return queryRows(ctx, s.db, `SELECT `+backgroundJobColumns+` FROM turns j LEFT JOIN conversations c USING (conversation_id) WHERE NOT j.takes_lane AND j.phase = 'running' ORDER BY j.created_at_unix_ns, j.conversation_id, j.input_id`, "running background jobs", scanBackgroundJob)
}

func timeUnixNano(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}

	return value.UTC().UnixNano()
}

func timeFromUnixNano(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}

	return time.Unix(0, value).UTC()
}

type rowScanner interface {
	Scan(...any) error
}

func scanJSON(row rowScanner) (json.RawMessage, error) {
	var value []byte
	if err := row.Scan(&value); err != nil {
		return nil, fmt.Errorf("scan JSON value: %w", err)
	}

	return value, nil
}

type scheduledMessageScanner struct{}

func (scheduledMessageScanner) scan(scanner rowScanner) (id string, message protocol.ScheduledMessageState, err error) {
	var dueAt, interval int64
	if err := scanner.Scan(&id, &message.ConversationID, &message.Agent, &message.Message, &dueAt, &message.Recurring, &interval, &message.Origin); err != nil {
		return "", protocol.ScheduledMessageState{}, fmt.Errorf("scan scheduled message: %w", err)
	}

	message.DueAt = timeFromUnixNano(dueAt)
	message.Interval = time.Duration(interval)

	return id, message, nil
}

func scanCronSchedule(scanner rowScanner) (CronScheduleState, error) {
	var (
		schedule CronScheduleState
		nextDue  int64
	)

	if err := scanner.Scan(&schedule.ScheduleID, &schedule.RelativePath, &nextDue); err != nil {
		return CronScheduleState{}, fmt.Errorf("scan cron schedule: %w", err)
	}

	schedule.NextDue = timeFromUnixNano(nextDue)

	return schedule, nil
}
