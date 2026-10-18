package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOpenCode serves the OpenCode v2 routes the sync and create paths call.
type fakeOpenCode struct {
	sessions []openCodeSession
	active   []string
	created  []string
}

func (f *fakeOpenCode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if user, password, ok := r.BasicAuth(); !ok || user != "opencode" || password != "pw" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	switch r.Method + " " + r.URL.Path {
	case "GET /api/session":
		_ = json.NewEncoder(w).Encode(map[string]any{"data": f.sessions})
	case "GET /api/session/active":
		active := map[string]any{}
		for _, id := range f.active {
			active[id] = map[string]string{"type": "running"}
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"data": active})
	case "POST /api/session":
		var body struct {
			Location struct{ Directory string } `json:"location"`
		}

		_ = json.NewDecoder(r.Body).Decode(&body)
		f.created = append(f.created, body.Location.Directory)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": openCodeTestSession("ses_new", "", body.Location.Directory, 5000)})
	}
}

func openCodeTestSession(id, title, directory string, updated int64) openCodeSession {
	session := openCodeSession{ID: id, Title: title}
	session.Time.Updated = updated
	session.Location.Directory = directory

	return session
}

// newOpenCodeTestService lists the remote sessions of the named servers, as RocketClaw
// does for the servers its config names.
func newOpenCodeTestService(t *testing.T, names ...string) *SessionService {
	t.Helper()

	s := newTestSessionService(t)
	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.OpenCode = map[string]config.OpenCodeConfig{}
		for _, name := range names {
			cfg.OpenCode[name] = config.OpenCodeConfig{}
		}

		return nil
	}))

	return s
}

func remoteSidebar(t *testing.T, s *SessionService) []SidebarSession {
	t.Helper()

	var rows []SidebarSession

	for row, err := range s.SidebarSessions(t.Context()) {
		require.NoError(t, err)

		if row.Server != "" {
			rows = append(rows, row)
		}
	}

	return rows
}

func TestSyncOpenCodeListsRemoteSessions(t *testing.T) {
	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	fake := &fakeOpenCode{
		sessions: []openCodeSession{openCodeTestSession("ses_a", "Alpha", "/srv/app", 3000), openCodeTestSession("ses_b", "Beta", "/srv/api", 2000), openCodeTestSession("ses_c", "Gamma", "/srv/app", 1000)},
		active:   []string{"ses_b"},
	}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	box := config.OpenCodeConfig{URL: server.URL, Password: "pw", Port: 4100}
	require.NoError(t, s.syncOpenCode(ctx, "box", box))

	rows := remoteSidebar(t, s)
	require.Len(t, rows, 3)
	assert.Equal(t, "opencode:box:ses_a", rows[0].Conversation.ID)
	assert.Equal(t, "Alpha", rows[0].Summary.LastMessage)
	assert.Equal(t, "/srv/api", rows[1].Directory)
	assert.True(t, rows[1].Running)
	assert.False(t, rows[0].Running)
	assert.False(t, rows[0].Offline)

	// Pin, name, and tag survive later syncs and state pruning.
	updated, err := s.UpdateConversationDetails(ctx, "opencode:box:ses_c", new(true), new("old work"))
	require.NoError(t, err)
	require.True(t, updated)

	_, err = s.toggleSessionTag(ctx, "opencode:box:ses_c", "customer", []string{"customer"})
	require.NoError(t, err)
	_, err = s.PruneStateBefore(ctx, time.Now().Add(time.Hour), 0)
	require.NoError(t, err)

	rows = remoteSidebar(t, s)
	require.Len(t, rows, 3)
	assert.Equal(t, "opencode:box:ses_c", rows[0].Conversation.ID, "pinned rows sort first")
	assert.Equal(t, "old work", rows[0].Name)
	assert.Equal(t, []string{"customer"}, rows[0].Tags)

	search, err := s.SearchSessions(ctx, "/srv/api", false, nil)
	require.NoError(t, err)
	require.Len(t, search.Matches, 1)
	assert.Equal(t, "opencode:box:ses_b", search.Matches[0].Chat.Session.Conversation.ID)

	// A below-limit fetch is the whole root set, so a missing session was deleted (R6).
	fake.sessions = fake.sessions[:2]

	require.NoError(t, s.syncOpenCode(ctx, "box", box))
	assert.Len(t, remoteSidebar(t, s), 2)

	// A failed poll writes nothing; rows age into offline once their stamp is stale.
	box.Password = "wrong"
	errSync := s.syncOpenCode(ctx, "box", box)
	require.ErrorAs(t, errSync, new(*openCodeError))
	require.EqualError(t, errSync, "opencode box /api/session?parentID=null&limit=100: status 401")

	_, err = s.db.ExecContext(ctx, `UPDATE opencode_sessions SET synced_at = now() - interval '1 minute'`)
	require.NoError(t, err)

	rows = remoteSidebar(t, s)
	require.Len(t, rows, 2)
	assert.True(t, rows[0].Offline)
}

func TestSyncOpenCodeKeepsRowsOutsideTheWindow(t *testing.T) {
	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	fake := &fakeOpenCode{}

	for i := range openCodeSyncLimit + 1 {
		fake.sessions = append(fake.sessions, openCodeTestSession(fmt.Sprintf("ses_%03d", i), "", "/srv", int64(10_000-i)))
	}

	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	box := config.OpenCodeConfig{URL: server.URL, Password: "pw", Port: 4100}
	require.NoError(t, s.syncOpenCode(ctx, "box", box))

	// The newest 100 form a full window: the oldest row leaves the fetch but stays listed, online.
	oldest := "opencode:box:" + fake.sessions[openCodeSyncLimit].ID
	fake.sessions = fake.sessions[:openCodeSyncLimit]
	_, err := s.db.ExecContext(ctx, `UPDATE opencode_sessions SET synced_at = now() - interval '1 minute'`)
	require.NoError(t, err)
	require.NoError(t, s.syncOpenCode(ctx, "box", box))

	rows := remoteSidebar(t, s)
	i := slices.IndexFunc(rows, func(row SidebarSession) bool { return row.Conversation.ID == oldest })
	require.NotEqual(t, -1, i)
	assert.False(t, rows[i].Offline)

	// The daily prune drops the kept row once the server lost it, and never lists old sessions.
	fake.sessions = append(fake.sessions, openCodeTestSession("ses_never_listed", "", "/srv", 1))

	require.NoError(t, s.pruneOpenCode(ctx, "box", box))

	rows = remoteSidebar(t, s)
	assert.Len(t, rows, openCodeSyncLimit)
	assert.False(t, slices.ContainsFunc(rows, func(row SidebarSession) bool {
		return row.Conversation.ID == oldest || row.Conversation.ID == "opencode:box:ses_never_listed"
	}))
}

func TestToggleManualTag(t *testing.T) {
	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	agents := rocketcode.Agents{Items: map[string]rocketcode.Agent{"main": {Frontmatter: map[string]any{"permission": map[string]any{"rocketclaw": map[string]any{setTagToolName: []any{[]any{"customer", "internal"}}}}}}}}

	require.NoError(t, s.UpsertThread("web", ThreadState{Agent: "main"}))
	_, err := s.db.ExecContext(ctx, `INSERT INTO opencode_sessions (conversation_id, server, title, directory, updated_at, running, synced_at)
VALUES ('opencode:box:ses_a', 'box', '', '/srv', now(), FALSE, now())`)
	require.NoError(t, err)

	for _, step := range [][2]string{{"web", "customer"}, {"web", "internal"}, {"opencode:box:ses_a", "billing"}, {"opencode:box:ses_a", "urgent"}} {
		require.NoError(t, s.ToggleManualTag(ctx, agents, step[0], step[1]))
	}

	require.ErrorIs(t, s.ToggleManualTag(ctx, agents, "web", "billing"), ErrTagNotAllowed)

	// The agent's group makes internal replace customer; remote sessions take any tags.
	for id, want := range map[string][]string{"web": {"internal"}, "opencode:box:ses_a": {"billing", "urgent"}} {
		tags, err := sessionTags(ctx, s.db, id)
		require.NoError(t, err)
		assert.Equal(t, want, tags, id)
	}
}

func TestOpenCodeUpstreamFailuresWriteNothing(t *testing.T) {
	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()

	activeFails := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session" {
			_, _ = w.Write([]byte(`{"data":[{"id":"ses_a"}]}`))
			return
		}

		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(activeFails.Close)

	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{`)) }))
	t.Cleanup(malformed.Close)

	for _, url := range []string{gone.URL, activeFails.URL, malformed.URL} {
		box := config.OpenCodeConfig{URL: url, Password: "pw", Port: 4100}
		require.Error(t, s.syncOpenCode(ctx, "box", box), url)

		_, err := s.CreateOpenCodeSession(ctx, "box", box, "/srv")
		require.Error(t, err, url)
	}

	require.Error(t, s.pruneOpenCode(ctx, "box", config.OpenCodeConfig{URL: gone.URL, Password: "pw", Port: 4100}))
	assert.Empty(t, remoteSidebar(t, s), "a failed poll writes nothing")
}

func TestCreateOpenCodeSessionListsItAtOnce(t *testing.T) {
	fake := &fakeOpenCode{}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	s := newOpenCodeTestService(t, "box")

	id, err := s.CreateOpenCodeSession(t.Context(), "box", config.OpenCodeConfig{URL: server.URL, Password: "pw", Port: 4100}, "/srv/app")
	require.NoError(t, err)
	assert.Equal(t, "opencode:box:ses_new", id)
	assert.Equal(t, []string{"/srv/app"}, fake.created)

	rows := remoteSidebar(t, s)
	require.Len(t, rows, 1)
	assert.Equal(t, "/srv/app", rows[0].Directory)
}

func TestSidebarHidesUnconfiguredOpenCodeServers(t *testing.T) {
	s := newOpenCodeTestService(t, "box")
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO opencode_sessions (conversation_id, server, title, directory, updated_at, running, synced_at, pinned)
VALUES ('opencode:box:ses_a', 'box', '', '/srv', now(), FALSE, now(), FALSE), ('opencode:gone:ses_b', 'gone', '', '/srv', now(), FALSE, now(), TRUE)`)
	require.NoError(t, err)

	rows := remoteSidebar(t, s)
	require.Len(t, rows, 1)
	assert.Equal(t, "opencode:box:ses_a", rows[0].Conversation.ID)

	// The removed server's row and its pin return when the server is configured again.
	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.OpenCode["gone"] = config.OpenCodeConfig{}
		return nil
	}))

	rows = remoteSidebar(t, s)
	require.Len(t, rows, 2)
	assert.True(t, rows[0].Pinned)
}
