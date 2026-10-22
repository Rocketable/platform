package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

// fakeOpenCode serves the OpenCode v2 routes the sync and create paths call.
type fakeOpenCode struct {
	sessions []openCodeSession
	active   []string
	created  []string
	// replies holds each session's newest assistant text; a session missing here fails the read.
	replies map[string]string
	// parents maps a child session to its parent. locations are the loaded locations, and shells
	// maps each to its running shells, by ID, with their sessions; a location missing from shells
	// fails its list. The event stream sends events after its connected event.
	parents   map[string]string
	locations []string
	shells    map[string]map[string]string
	events    chan string
	messages  map[string][]openCodeTestMessage
	reads     []string
}

type openCodeTestMessage struct {
	Type     string
	Username string
	Created  int64
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
	case "GET /api/debug/location":
		locations := []map[string]string{}
		for _, directory := range f.locations {
			locations = append(locations, map[string]string{"directory": directory})
		}

		_ = json.NewEncoder(w).Encode(locations)
	case "GET /api/shell":
		running, ok := f.shells[r.URL.Query().Get("location[directory]")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		shells := []map[string]any{}
		for id, session := range running {
			shells = append(shells, map[string]any{"id": id, "status": "running", "metadata": map[string]string{"sessionID": session}})
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"data": shells})
	case "GET /api/event":
		_, _ = fmt.Fprint(w, "data: {\"type\":\"server.connected\",\"data\":{}}\n\n: heartbeat\n\n")
		w.(http.Flusher).Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case event, ok := <-f.events:
				if !ok {
					return
				}

				_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
				w.(http.Flusher).Flush()
			}
		}
	case "POST /api/session":
		var body struct {
			Location struct{ Directory string } `json:"location"`
		}

		_ = json.NewDecoder(r.Body).Decode(&body)
		f.created = append(f.created, body.Location.Directory)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": openCodeTestSession("ses_new", "", body.Location.Directory, 5000)})
	default:
		id, messages := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/api/session/"), "/message")
		if r.Method == http.MethodPatch && !messages {
			var body struct {
				Title *string `json:"title"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Title == nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			for i := range f.sessions {
				if f.sessions[i].ID == id {
					f.sessions[i].Title = *body.Title

					w.WriteHeader(http.StatusNoContent)

					return
				}
			}

			w.WriteHeader(http.StatusNotFound)

			return
		}

		if parent, ok := f.parents[id]; ok && !messages {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"parentID": parent}})
			return
		}

		f.reads = append(f.reads, id+"?"+r.URL.RawQuery)

		if query := r.URL.Query(); query.Get("type") != "assistant" {
			history := f.messages[id]
			limit, _ := strconv.Atoi(query.Get("limit"))

			if len(history) == 0 || query.Get("order") != "desc" || limit < 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			type wireMessage struct {
				Type     string            `json:"type"`
				Metadata map[string]string `json:"metadata"`
				Time     struct {
					Created int64 `json:"created"`
				} `json:"time"`
			}

			data := []wireMessage{}
			for _, message := range slices.Backward(history) {
				if message.Type == query.Get("type") && len(data) < limit {
					wire := wireMessage{Type: message.Type, Metadata: map[string]string{"displayText": "hi"}}
					if message.Username != "" {
						wire.Metadata["rocketclaw_username"] = message.Username
					}

					wire.Time.Created = message.Created
					data = append(data, wire)
				}
			}

			_ = json.NewEncoder(w).Encode(struct {
				Data []wireMessage `json:"data"`
			}{data})

			return
		}

		reply, ok := f.replies[id]
		if !ok || r.URL.RawQuery != "type=assistant&order=desc&limit=1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		text, _ := json.Marshal(reply)
		_, _ = fmt.Fprintf(w, `{"data":[{"content":[{"type":"reasoning","text":"thinking"},{"type":"text","text":%s},{"type":"tool"}]}]}`, text)
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

	for row, err := range s.sidebarSessions(t.Context(), "", nil) {
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

	box := config.OpenCodeConfig{URL: server.URL, Password: "pw"}

	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.OpenCode["box"] = box
		return nil
	}))
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

	rows := remoteSidebar(t, s)
	require.Len(t, rows, 3)
	assert.Equal(t, "opencode:box:ses_a", rows[0].Conversation.ID)
	assert.Equal(t, "Alpha", rows[0].Summary.LastMessage)
	assert.Equal(t, "Alpha", rows[0].Name)
	assert.Equal(t, "/srv/api", rows[1].Directory)
	assert.True(t, rows[1].Running)
	assert.False(t, rows[0].Running)
	assert.False(t, rows[0].Offline)

	// A session seen running and then idle stores its newest assistant text and keeps it while idle,
	// even when a later read would fail; a failed read when the next turn ends clears it.
	fake.active, fake.replies = nil, map[string]string{"ses_b": "Done: tests pass."}

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

	fake.replies = nil

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

	rows = remoteSidebar(t, s)
	assert.Equal(t, []string{"", "Done: tests pass.", ""}, []string{rows[0].Reply, rows[1].Reply, rows[2].Reply})

	fake.active = []string{"ses_b"}

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

	fake.active = nil

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	assert.Empty(t, remoteSidebar(t, s)[1].Reply)

	// Pin, name, and tag survive later syncs and state pruning.
	updated, err := s.UpdateConversationDetails(ctx, "opencode:box:ses_c", new(true), new("old work"))
	require.NoError(t, err)
	require.True(t, updated)
	// OpenCode v2 session.update uses PATCH and returns 204, not a JSON session:
	// https://opencode.ai/v2/openapi.json#/paths/~1api~1session~1{sessionID}/patch
	assert.Equal(t, "old work", fake.sessions[2].Title, "renaming in RocketClaw must rename the OpenCode session")
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

	_, err = s.toggleSessionTag(ctx, "opencode:box:ses_c", "customer", []string{"customer"})
	require.NoError(t, err)
	_, err = s.PruneStateBefore(ctx, time.Now().Add(time.Hour), 0)
	require.NoError(t, err)

	rows = remoteSidebar(t, s)
	require.Len(t, rows, 3)
	assert.Equal(t, "opencode:box:ses_c", rows[0].Conversation.ID, "pinned rows sort first")
	assert.Equal(t, "old work", rows[0].Name)
	assert.Equal(t, []string{"customer"}, rows[0].Tags)
	assert.Equal(t, "old work", rows[0].Summary.LastMessage)

	// The v2 Session.Info.title from GET /api/session is the shared name, not a local alias:
	// https://opencode.ai/v2/openapi.json#/paths/~1api~1session/get
	for _, title := range []string{"Delta", ""} {
		fake.sessions[2].Title = title

		require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
		rows = remoteSidebar(t, s)
		assert.Equal(t, title, rows[0].Name, "an upstream rename or clear must replace the local name")
		assert.Equal(t, title, rows[0].Summary.LastMessage)
		assert.True(t, rows[0].Pinned)
		assert.Equal(t, []string{"customer"}, rows[0].Tags)
	}

	_, err = s.UpdateConversationDetails(ctx, "opencode:box:ses_c", nil, new("old work"))
	require.NoError(t, err)

	// Pins still work while OpenCode refuses writes, but a refused rename saves nothing locally.
	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.OpenCode["box"] = config.OpenCodeConfig{URL: server.URL, Password: "wrong"}
		return nil
	}))
	updated, err = s.UpdateConversationDetails(ctx, "opencode:box:ses_c", new(false), nil)
	require.NoError(t, err)
	require.True(t, updated)
	updated, err = s.UpdateConversationDetails(ctx, "opencode:box:ses_c", new(true), new("refused"))
	require.ErrorAs(t, err, new(*openCodeError))
	assert.False(t, updated)
	assert.Equal(t, "old work", remoteSidebar(t, s)[2].Name)
	assert.False(t, remoteSidebar(t, s)[2].Pinned)
	assert.Equal(t, "old work", fake.sessions[2].Title)

	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.OpenCode["box"] = box
		return nil
	}))
	_, err = s.UpdateConversationDetails(ctx, "opencode:box:ses_c", new(true), nil)
	require.NoError(t, err)
	updated, err = s.UpdateConversationDetails(ctx, "opencode:box:ses_c", nil, new(""))
	require.NoError(t, err)
	require.True(t, updated)
	assert.Empty(t, fake.sessions[2].Title, "clearing a name also clears the remote title")
	assert.Empty(t, remoteSidebar(t, s)[0].Name)
	assert.Empty(t, remoteSidebar(t, s)[0].Summary.LastMessage)

	search, err := s.SearchSessions(ctx, "", "/srv/api", SearchFields, nil)
	require.NoError(t, err)
	require.Len(t, search.Matches, 1)
	assert.Equal(t, "opencode:box:ses_b", search.Matches[0].Chat.Session.Conversation.ID)

	// A below-limit fetch is the whole root set, so a missing session was deleted (R6).
	fake.sessions = fake.sessions[:2]

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	assert.Len(t, remoteSidebar(t, s), 2)

	// A failed poll writes nothing; rows age into offline once their stamp is stale.
	box.Password = "wrong"
	errSync := s.syncOpenCode(ctx, "box", box, &openCodeShells{})
	require.ErrorAs(t, errSync, new(*openCodeError))
	require.EqualError(t, errSync, "opencode box /api/session?parentID=null&limit=100: status 401")

	_, err = s.db.ExecContext(ctx, `UPDATE conversations SET synced_at = now() - interval '1 minute' WHERE kind = 'remote'`)
	require.NoError(t, err)

	rows = remoteSidebar(t, s)
	require.Len(t, rows, 2)
	assert.True(t, rows[0].Offline)
}

func TestOpenCodeRenamesKeepRemoteAndLocalOrder(t *testing.T) {
	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	titles := make(chan string, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Title string }

		_ = json.NewDecoder(r.Body).Decode(&body)
		titles <- body.Title

		if body.Title == "first" {
			<-release
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.OpenCode["box"] = config.OpenCodeConfig{URL: server.URL, Password: "pw"}
		return nil
	}))
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, remote_session, title, directory, last_updated, running, synced_at) VALUES ('opencode:box:ses_a', '', '', 'remote', FALSE, 'remote', 'box', 'ses_a', 'original', '/srv', now(), FALSE, now())`)
	require.NoError(t, err)

	var renames errgroup.Group
	renames.Go(func() error {
		_, err := s.UpdateConversationDetails(ctx, "opencode:box:ses_a", nil, new("first"))
		return err
	})
	require.Equal(t, "first", <-titles)

	secondDone := make(chan struct{})

	renames.Go(func() error {
		defer close(secondDone)

		_, err := s.UpdateConversationDetails(ctx, "opencode:box:ses_a", nil, new("second"))

		return err
	})

	secondApplied := false

	assert.Eventually(t, func() bool {
		select {
		case <-titles:
			secondApplied = true
			return true
		default:
			var blocked int

			err := queryScalar(ctx, s.db, "read blocked rename", `WITH blocked AS (
    SELECT pid FROM pg_stat_activity WHERE wait_event_type = 'Lock'
), sessions AS (
    SELECT pid FROM pg_locks WHERE relation = 'conversations'::regclass
)
SELECT count(*) FROM blocked JOIN sessions USING (pid)`, &blocked)

			return err == nil && blocked > 0
		}
	}, 5*time.Second, 10*time.Millisecond)

	if secondApplied {
		<-secondDone
	}

	close(release)
	require.NoError(t, renames.Wait())
	assert.Equal(t, "second", remoteSidebar(t, s)[0].Name, "local name must match the last title applied upstream, not HTTP response order")
}

func TestOpenCodeRenameSurvivesEarlierPoll(t *testing.T) {
	for _, name := range []string{"renamed", ""} {
		t.Run(name, func(t *testing.T) {
			s := newOpenCodeTestService(t, "box")
			ctx := t.Context()
			fake := &fakeOpenCode{sessions: []openCodeSession{openCodeTestSession("ses_a", "original", "/srv/app", 1000)}}
			listed, release := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/session/active" {
					close(listed)
					<-release
				}

				fake.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			box := config.OpenCodeConfig{URL: server.URL, Password: "pw"}

			require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
				cfg.OpenCode["box"] = box
				return nil
			}))
			_, err := s.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, remote_session, title, name, directory, last_updated, running, synced_at) VALUES ('opencode:box:ses_a', '', '', 'remote', FALSE, 'remote', 'box', 'ses_a', 'original', 'original', '/srv/app', now(), FALSE, now())`)
			require.NoError(t, err)

			var poll errgroup.Group
			poll.Go(func() error {
				return s.syncOpenCode(ctx, "box", box, &openCodeShells{})
			})
			<-listed

			updated, err := s.UpdateConversationDetails(ctx, "opencode:box:ses_a", nil, &name)

			close(release)

			errPoll := poll.Wait()

			require.NoError(t, err)
			require.True(t, updated)
			require.NoError(t, errPoll)
			assert.Equal(t, name, fake.sessions[0].Title)
			rows := remoteSidebar(t, s)
			require.Len(t, rows, 1)
			assert.Equal(t, name, rows[0].Name)
			assert.Equal(t, name, rows[0].Summary.LastMessage)
		})
	}
}

func TestSyncOpenCodeMarksBackgroundWork(t *testing.T) {
	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	fake := &fakeOpenCode{
		sessions: []openCodeSession{openCodeTestSession("ses_a", "", "/srv/app", 4000), openCodeTestSession("ses_b", "", "/srv/app", 3000), openCodeTestSession("ses_c", "", "/srv/api", 2000), openCodeTestSession("ses_d", "", "/srv/gone", 1000)},
		// ses_b's background subagent runs, and a grandchild of ses_c runs a shell.
		active:  []string{"ses_a", "ses_b_child"},
		parents: map[string]string{"ses_b_child": "ses_b", "ses_c_grandchild": "ses_c_child", "ses_c_child": "ses_c"},
	}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	box := config.OpenCodeConfig{URL: server.URL, Password: "pw"}
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{running: map[string]string{"sh_1": "ses_c_grandchild"}}))

	rows := remoteSidebar(t, s)
	require.Len(t, rows, 4)
	assert.Equal(t, []bool{true, false, false, false}, []bool{rows[0].Running, rows[1].Running, rows[2].Running, rows[3].Running})
	assert.Equal(t, []bool{false, true, true, false}, []bool{rows[0].Background, rows[1].Background, rows[2].Background, rows[3].Background})

	fake.active = nil

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	assert.False(t, slices.ContainsFunc(remoteSidebar(t, s), func(row SidebarSession) bool { return row.Background }))
}

// An OpenCode turn end marks the people whose stamped prompts it ran, once each (R13, R14, AE7).
func TestSyncOpenCodeMarksTurnSenders(t *testing.T) {
	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	fake := &fakeOpenCode{messages: map[string][]openCodeTestMessage{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	box := config.OpenCodeConfig{URL: server.URL, Password: "pw"}
	id, err := s.CreateOpenCodeSession(ctx, "box", box, "/srv/app")
	require.NoError(t, err)

	unread := func(username string) []string {
		ids := []string{}

		for row, err := range s.sidebarSessions(ctx, username, nil) {
			require.NoError(t, err)

			if !row.UnreadAt.IsZero() {
				ids = append(ids, row.Conversation.ID)
			}
		}

		return ids
	}
	turn := func(updated int64, busy bool, messages ...openCodeTestMessage) {
		t.Helper()

		_, err := s.db.ExecContext(ctx, `DELETE FROM web_tabs; DELETE FROM web_tab_states`)
		require.NoError(t, err)

		fake.sessions = []openCodeSession{openCodeTestSession("ses_new", "", "/srv/app", updated)}

		fake.active = nil
		if busy {
			fake.active = []string{"ses_new"}
		}

		fake.messages["ses_new"] = append(fake.messages["ses_new"], messages...)

		require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	}
	prompt := func(username string, created int64) openCodeTestMessage {
		return openCodeTestMessage{Type: "user", Username: username, Created: created}
	}
	idle := func(created int64) openCodeTestMessage { return openCodeTestMessage{Type: "idle", Created: created} }

	// A session created here marks Alice when its first turn, seen busy, ends.
	turn(5000, true, prompt("alice", 5100))
	assert.Empty(t, fake.reads, "a busy session reads no messages")
	turn(5000, false, idle(5200))
	assert.Equal(t, []string{id}, unread("alice"))
	assert.Empty(t, unread("bob"))

	paths, err := queryStrings(ctx, s.db, `SELECT path FROM web_tabs`, "marked tabs")
	require.NoError(t, err)
	assert.Equal(t, []string{"/oc/" + base64.RawURLEncoding.EncodeToString([]byte(id))}, paths, "the mark is on the tab path the Web spells")

	// A turn never seen busy is found by its moved time.updated; Alice's prompt before the cursor
	// and an unstamped TUI prompt mark no one.
	turn(6000, false, prompt("bob", 5900), prompt("", 5950), idle(6000))
	assert.Equal(t, []string{id}, unread("bob"))
	assert.Empty(t, unread("alice"))

	// An idle session whose time.updated did not move reads no messages.
	reads := len(fake.reads)

	turn(6000, false)
	assert.Len(t, fake.reads, reads)

	// A TUI-only turn and a /command turn with no user message mark no one.
	turn(7000, false, prompt("", 6500), idle(6600), idle(7000))
	assert.Empty(t, unread("alice"))
	assert.Empty(t, unread("bob"))
	assert.Empty(t, unread(""))

	// Two turns that ended between polls, after a sync saw the session busy, mark both senders.
	turn(7000, true)
	turn(7000, false, prompt("alice", 7100), idle(7200), prompt("bob", 7300), prompt("bob", 7350), idle(7400))
	assert.Equal(t, []string{id}, unread("alice"))
	assert.Equal(t, []string{id}, unread("bob"))

	// Alice viewed the session 3 seconds ago, so only Bob, who viewed it long ago, is marked.
	_, err = s.db.ExecContext(ctx, `DELETE FROM web_tabs; DELETE FROM web_tab_states`)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, revision, viewing, viewed_at) VALUES ('alice', 0, $1, now() - interval '3 seconds'), ('bob', 0, $1, now() - interval '30 seconds')`, id)
	require.NoError(t, err)

	fake.sessions = []openCodeSession{openCodeTestSession("ses_new", "", "/srv/app", 7600)}
	fake.messages["ses_new"] = append(fake.messages["ses_new"], prompt("alice", 7500), prompt("bob", 7550), idle(7600))

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	assert.Empty(t, unread("alice"))
	assert.Equal(t, []string{id}, unread("bob"))

	// A failed message read leaves the cursor, so the next turn end marks the missed sender too.
	// Carol's prompt after the newest marker belongs to a turn that has not ended.
	fake.messages["ses_new"] = append(fake.messages["ses_new"], prompt("alice", 7900), idle(8000))
	history := fake.messages["ses_new"]
	delete(fake.messages, "ses_new")
	turn(8000, false)
	assert.Empty(t, unread("alice"))

	fake.messages["ses_new"] = history

	turn(8200, false, prompt("bob", 8100), idle(8200), prompt("carol", 8300))
	assert.Equal(t, []string{id}, unread("alice"))
	assert.Equal(t, []string{id}, unread("bob"))
	assert.Empty(t, unread("carol"))
}

func TestOpenCodeShellsFollowEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	fake := &fakeOpenCode{
		// /srv/gone is loaded but its list fails. sh_old's exit is on the stream before the list
		// that still names it, and it must apply after.
		locations: []string{"/srv/app", "/srv/gone"},
		shells:    map[string]map[string]string{"/srv/app": {"sh_old": "ses_a", "sh_kept": "ses_b"}},
		events:    make(chan string, 4),
	}
	fake.events <- `{"type":"shell.exited","data":{"id":"sh_old","status":"exited"}}`

	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	shells := &openCodeShells{running: map[string]string{"sh_stale": "ses_z"}}
	done := make(chan error, 1)

	go func() {
		done <- shells.follow(ctx, "box", config.OpenCodeConfig{URL: server.URL, Password: "pw"})
	}()

	running := func(want map[string]string) func() bool {
		return func() bool {
			shells.mu.Lock()
			defer shells.mu.Unlock()

			return maps.Equal(want, shells.running)
		}
	}
	require.Eventually(t, running(map[string]string{"sh_kept": "ses_b"}), 5*time.Second, 10*time.Millisecond, "connecting replaces stale shells with the loaded locations'")

	for _, event := range []string{
		`{"type":"shell.created","data":{"info":{"id":"sh_new","status":"running","metadata":{"sessionID":"ses_c"}}}}`,
		`{"type":"session.text.delta","data":{"info":"not a shell"}}`,
		`{"type":"shell.deleted","data":{"id":"sh_kept"}}`,
	} {
		fake.events <- event
	}

	require.Eventually(t, running(map[string]string{"sh_new": "ses_c"}), 5*time.Second, 10*time.Millisecond)

	close(fake.events)
	require.NoError(t, <-done, "an ended stream returns so the caller reconnects")
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

	box := config.OpenCodeConfig{URL: server.URL, Password: "pw"}
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

	// The newest 100 form a full window: the oldest row leaves the fetch but stays listed, online.
	oldest := "opencode:box:" + fake.sessions[openCodeSyncLimit].ID
	fake.sessions = fake.sessions[:openCodeSyncLimit]
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET synced_at = now() - interval '1 minute' WHERE kind = 'remote'`)
	require.NoError(t, err)
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

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
	server := httptest.NewServer(&fakeOpenCode{sessions: []openCodeSession{openCodeTestSession("ses_a", "", "/srv", 1000)}})
	t.Cleanup(server.Close)
	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.OpenCode["box"] = config.OpenCodeConfig{URL: server.URL, Password: "pw"}
		return nil
	}))

	agents := rocketcode.Agents{Items: map[string]rocketcode.Agent{"main": {Frontmatter: map[string]any{"permission": map[string]any{"rocketclaw": map[string]any{setTagToolName: []any{[]any{"customer", "internal"}}}}}}}}

	require.NoError(t, s.UpsertThread("web", ThreadState{Agent: "main"}))
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, title, directory, last_updated, running, synced_at, remote_session)
VALUES ('opencode:box:ses_a', '', '', 'remote', FALSE, 'remote', 'box', '', '/srv', now(), FALSE, now(), 'ses_a')`)
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

	// Tags, pins, and names of both kinds are facts on their own conversation rows.
	for _, id := range []string{"web", "opencode:box:ses_a"} {
		updated, err := s.UpdateConversationDetails(ctx, id, new(true), new("Named "+id))
		require.NoError(t, err)
		require.True(t, updated)
	}

	facts, err := queryStrings(ctx, s.db, `SELECT concat_ws(' ', conversation_id, kind, tags::text, pinned, name) FROM conversations ORDER BY 1`, "conversation facts")
	require.NoError(t, err)
	assert.Equal(t, []string{`opencode:box:ses_a remote ["billing", "urgent"] t Named opencode:box:ses_a`, `web web_chat ["internal"] t Named web`}, facts)
}

func TestOpenCodeMovesOldNamesToTheServer(t *testing.T) {
	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	fake := &fakeOpenCode{sessions: []openCodeSession{openCodeTestSession("ses_a", "Alpha", "/srv/app", 2000), openCodeTestSession("ses_b", "Beta", "/srv/app", 1000)}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	box := config.OpenCodeConfig{URL: server.URL, Password: "pw"}

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

	names := func() []string {
		var names []string

		require.NoError(t, readRows(ctx, s.db, `SELECT name || '|' || title FROM conversations WHERE server = 'box' ORDER BY conversation_id`, "read names", func(row rowScanner) error {
			var name string
			if err := row.Scan(&name); err != nil {
				return fmt.Errorf("scan name: %w", err)
			}

			names = append(names, name)

			return nil
		}))

		return names
	}

	// A name stored before names synced with OpenCode moves there once the server accepts it.
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET name = 'old work' WHERE conversation_id = 'opencode:box:ses_b'`)
	require.NoError(t, err)
	require.NoError(t, s.pushOpenCodeNames(ctx, "box", config.OpenCodeConfig{URL: server.URL, Password: "wrong"}))
	assert.Equal(t, []string{"Alpha|Alpha", "old work|Beta"}, names(), "a refused name stays to move later")

	require.NoError(t, s.pushOpenCodeNames(ctx, "box", box))
	assert.Equal(t, []string{"Alpha", "old work"}, []string{fake.sessions[0].Title, fake.sessions[1].Title})
	assert.Equal(t, []string{"Alpha|Alpha", "old work|old work"}, names())
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
		box := config.OpenCodeConfig{URL: url, Password: "pw"}
		require.Error(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}), url)

		_, err := s.CreateOpenCodeSession(ctx, "box", box, "/srv")
		require.Error(t, err, url)
	}

	require.Error(t, s.pruneOpenCode(ctx, "box", config.OpenCodeConfig{URL: gone.URL, Password: "pw"}))
	assert.Empty(t, remoteSidebar(t, s), "a failed poll writes nothing")
}

// Remote sessions carry their selected agent and model; a RocketClaw chat's model is its agent's
// configured one. model: matches part of a model, ranking chats with the whole model first.
func TestSearchSessionsFiltersByServerAgentAndModel(t *testing.T) {
	s := newOpenCodeTestService(t, "box", "lab")
	ctx := t.Context()

	root, err := os.OpenRoot(s.definitions.Clone().Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(".rocketclaw/agents", 0o755))
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/agents/main.md", []byte("---\nmodel: gpt-6.1-sol\n---\nPrompt\n"), 0o644))
	require.NoError(t, s.UpsertThread("web-session:local", ThreadState{Agent: "main"}))
	_, err = s.AppendEntryID(ctx, "web-session:local", testSessionEntry("question", "answer"))
	require.NoError(t, err)

	remote := func(id, agent, model string, updated int64) openCodeSession {
		session := openCodeTestSession(id, id, "/srv", updated)
		session.Agent = agent
		session.Model.ProviderID, session.Model.ID, _ = strings.Cut(model, "/")

		return session
	}

	for name, sessions := range map[string][]openCodeSession{
		"box": {remote("ses_fast", "plan", "anthropic/claude-opus-4-7-fast", 4000), remote("ses_opus", "build", "anthropic/claude-opus-4-7", 3000)},
		"lab": {remote("ses_new", "build", "", 2000)},
	} {
		server := httptest.NewServer(&fakeOpenCode{sessions: sessions})
		t.Cleanup(server.Close)
		require.NoError(t, s.syncOpenCode(ctx, name, config.OpenCodeConfig{URL: server.URL, Password: "pw"}, &openCodeShells{}))
	}

	rows := remoteSidebar(t, s)
	require.Len(t, rows, 3)
	assert.Equal(t, []string{"plan", "anthropic/claude-opus-4-7-fast"}, []string{rows[0].Conversation.Agent, rows[0].Model})
	assert.Equal(t, []string{"build", ""}, []string{rows[2].Conversation.Agent, rows[2].Model})

	for query, want := range map[string][]string{
		"server:box":                        {"opencode:box:ses_fast", "opencode:box:ses_opus"},
		"server:lab agent:build":            {"opencode:lab:ses_new"},
		"agent:build":                       {"opencode:box:ses_opus", "opencode:lab:ses_new"},
		"model:OPUS":                        {"opencode:box:ses_fast", "opencode:box:ses_opus"},
		"model:anthropic/claude-opus-4-7":   {"opencode:box:ses_opus", "opencode:box:ses_fast"},
		"model:gpt-6.1":                     {"web-session:local"},
		"model:gpt-6.1-sol agent:main":      {"web-session:local"},
		"server:box model:opus agent:build": {"opencode:box:ses_opus"},
		"server:other":                      {},
	} {
		search, err := s.SearchSessions(ctx, "", query, SearchFields, noSlackLookups{})
		require.NoError(t, err)

		ids := make([]string, 0, len(search.Matches))
		for _, match := range search.Matches {
			ids = append(ids, match.Chat.Session.Conversation.ID)
		}

		assert.Equal(t, want, ids, query)
	}
}

func TestCreateOpenCodeSessionListsItAtOnce(t *testing.T) {
	fake := &fakeOpenCode{}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	s := newOpenCodeTestService(t, "box")

	id, err := s.CreateOpenCodeSession(t.Context(), "box", config.OpenCodeConfig{URL: server.URL, Password: "pw"}, "/srv/app")
	require.NoError(t, err)
	assert.Equal(t, "opencode:box:ses_new", id)
	assert.Equal(t, []string{"/srv/app"}, fake.created)

	rows := remoteSidebar(t, s)
	require.Len(t, rows, 1)
	assert.Equal(t, "/srv/app", rows[0].Directory)
}

func TestSidebarHidesUnconfiguredOpenCodeServers(t *testing.T) {
	s := newOpenCodeTestService(t, "box")
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, title, directory, last_updated, running, synced_at, pinned, remote_session)
VALUES ('opencode:box:ses_a', '', '', 'remote', FALSE, 'remote', 'box', '', '/srv', now(), FALSE, now(), FALSE, 'ses_a'),
    ('opencode:gone:ses_b', '', '', 'remote', FALSE, 'remote', 'gone', '', '/srv', now(), FALSE, now(), TRUE, 'ses_b')`)
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

// A remote session's history stays on its server: deleting its entries is refused, and its row,
// updated time included, stays as it was.
func TestDeleteSessionRefusesRemoteSessions(t *testing.T) {
	s := newOpenCodeTestService(t, "box")
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, title, directory, last_updated, running, synced_at, remote_session)
VALUES ('opencode:box:ses_a', '', '', 'remote', FALSE, 'remote', 'box', 'title', '/srv', '2026-10-01T00:00:00Z', FALSE, now(), 'ses_a')`)
	require.NoError(t, err)

	_, err = s.DeleteSession(t.Context(), "opencode:box:ses_a")
	require.ErrorIs(t, err, ErrRemoteSession)

	var updated time.Time
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT last_updated FROM conversations WHERE conversation_id = 'opencode:box:ses_a'`).Scan(&updated))
	assert.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), updated.UTC())
}
