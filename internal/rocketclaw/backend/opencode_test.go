package backend

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

type fakeOpenCode struct {
	sessions  []openCodeSession
	active    []string
	activeAt  [][]string
	created   []string
	replies   map[string]string
	parents   map[string]string
	locations []string
	shells    map[string]map[string]string
	events    chan string
	messages  map[string][]openCodeTestMessage
	userPage  int
	reads     []string
	mcps      map[string][]string
	bridges   []string

	pending     map[string]map[string][]string
	automations string
}

type openCodeTestMessage struct {
	Type     string
	Username string
	Created  int64
	Outcome  string
	Text     string
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
		answer := f.active
		if len(f.activeAt) > 0 {
			answer, f.activeAt = f.activeAt[0], f.activeAt[1:]
		}

		active := map[string]any{}
		for _, id := range answer {
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
	case "GET /api/form", "GET /api/permission/request":
		sessions, ok := f.pending[r.URL.Path][r.URL.Query().Get("location[directory]")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		requests := []map[string]string{}
		for _, session := range sessions {
			requests = append(requests, map[string]string{"sessionID": session})
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"data": requests})
	case "GET /api/experimental/automation":
		if f.automations == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		_, _ = io.WriteString(w, f.automations)
	case "GET /api/mcp":
		servers := []map[string]string{}
		for _, name := range f.mcps[r.URL.Query().Get("location[directory]")] {
			servers = append(servers, map[string]string{"name": name})
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"data": servers})
	case "PUT /api/experimental/mcp/rocketclaw":
		body, _ := io.ReadAll(r.Body)
		f.bridges = append(f.bridges, r.URL.Query().Get("location[directory]")+" "+string(body))

		w.WriteHeader(http.StatusNoContent)
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

		query := r.URL.Query()
		if cursor := query.Get("cursor"); cursor != "" {
			if query.Has("order") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			position, _ := url.ParseQuery(cursor)
			query.Set("order", position.Get("order"))
			query.Set("offset", position.Get("offset"))
		}

		if query.Get("type") != "assistant" {
			history := f.messages[id]
			limit, _ := strconv.Atoi(query.Get("limit"))

			if len(history) == 0 || limit < 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			type wirePart struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}

			type wireMessage struct {
				ID       string            `json:"id"`
				Type     string            `json:"type"`
				Text     string            `json:"text,omitempty"`
				Content  []wirePart        `json:"content,omitempty"`
				Metadata map[string]string `json:"metadata"`
				Outcome  string            `json:"outcome,omitempty"`
				Time     struct {
					Created int64 `json:"created"`
				} `json:"time"`
			}

			type wireCursor struct {
				Next string `json:"next,omitempty"`
			}

			ordered := slices.Clone(history)
			if query.Get("order") != "asc" {
				slices.Reverse(ordered)
			}

			ordered = slices.DeleteFunc(ordered, func(message openCodeTestMessage) bool {
				return query.Get("type") != "" && message.Type != query.Get("type")
			})
			offset, _ := strconv.Atoi(query.Get("offset"))

			size := limit
			if query.Get("type") == "user" {
				size = cmp.Or(min(limit, f.userPage), limit)
			}

			var cursor wireCursor
			if ordered = ordered[offset:]; len(ordered) > size {
				ordered, cursor.Next = ordered[:size], url.Values{"order": {query.Get("order")}, "offset": {strconv.Itoa(offset + size)}}.Encode()
			}

			data := []wireMessage{}

			for _, message := range ordered {
				wire := wireMessage{ID: fmt.Sprint("msg_", message.Created), Type: message.Type, Metadata: map[string]string{"displayText": "hi"}, Outcome: message.Outcome}
				if message.Username != "" {
					wire.Metadata["rocketclaw_username"] = message.Username
				}

				if message.Type == "assistant" {
					wire.Content = []wirePart{{Type: "reasoning", Text: "thinking"}, {Type: "text", Text: message.Text}}
				} else {
					wire.Text = message.Text
				}

				wire.Time.Created = message.Created
				data = append(data, wire)
			}

			_ = json.NewEncoder(w).Encode(struct {
				Data   []wireMessage `json:"data"`
				Cursor wireCursor    `json:"cursor"`
			}{data, cursor})

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

func TestAddOpenCodeBridgesReachesOpenFoldersWithoutIt(t *testing.T) {
	t.Parallel()

	fake := &fakeOpenCode{locations: []string{"/srv/a", "/srv/b"}, mcps: map[string][]string{"/srv/a": {"context7"}, "/srv/b": {"context7", "rocketclaw"}}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	addOpenCodeBridges(t.Context(), "https://claw.example.ts.net", "box", config.OpenCodeConfig{URL: server.URL, Password: "pw"})

	assert.Equal(t, []string{`/srv/a {"config":{"type":"remote","url":"https://claw.example.ts.net/opencode/box/mcp","oauth":false,"headers":{"Authorization":"Basic b3BlbmNvZGU6cHc="}}}`}, fake.bridges)
}

func TestSyncOpenCodeListsRemoteSessions(t *testing.T) {
	t.Parallel()

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

	updated, err := s.UpdateConversationDetails(ctx, "opencode:box:ses_c", new("old work"))
	require.NoError(t, err)
	require.True(t, updated)
	assert.Equal(t, "old work", fake.sessions[2].Title, "renaming in RocketClaw must rename the OpenCode session")
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

	_, err = s.toggleSessionTag(ctx, "opencode:box:ses_c", "customer", []string{"customer"})
	require.NoError(t, err)
	_, err = s.PruneStateBefore(ctx, time.Now().Add(time.Hour), 0)
	require.NoError(t, err)

	rows = remoteSidebar(t, s)
	require.Len(t, rows, 3)
	assert.Equal(t, "opencode:box:ses_c", rows[2].Conversation.ID)
	assert.Equal(t, "old work", rows[2].Name)
	assert.Equal(t, []string{"customer"}, rows[2].Tags)
	assert.Equal(t, "old work", rows[2].Summary.LastMessage)

	for _, title := range []string{"Delta", ""} {
		fake.sessions[2].Title = title

		require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
		rows = remoteSidebar(t, s)
		assert.Equal(t, title, rows[2].Name, "an upstream rename or clear must replace the local name")
		assert.Equal(t, title, rows[2].Summary.LastMessage)
		assert.Equal(t, []string{"customer"}, rows[2].Tags)
	}

	_, err = s.UpdateConversationDetails(ctx, "opencode:box:ses_c", new("old work"))
	require.NoError(t, err)

	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.OpenCode["box"] = config.OpenCodeConfig{URL: server.URL, Password: "wrong"}
		return nil
	}))
	updated, err = s.UpdateConversationDetails(ctx, "opencode:box:ses_c", nil)
	require.NoError(t, err)
	require.True(t, updated)
	updated, err = s.UpdateConversationDetails(ctx, "opencode:box:ses_c", new("refused"))
	require.ErrorAs(t, err, new(*openCodeError))
	assert.False(t, updated)
	assert.Equal(t, "old work", remoteSidebar(t, s)[2].Name)
	assert.Equal(t, "old work", fake.sessions[2].Title)

	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.OpenCode["box"] = box
		return nil
	}))
	updated, err = s.UpdateConversationDetails(ctx, "opencode:box:ses_c", new(""))
	require.NoError(t, err)
	require.True(t, updated)
	assert.Empty(t, fake.sessions[2].Title, "clearing a name also clears the remote title")
	assert.Empty(t, remoteSidebar(t, s)[2].Name)
	assert.Empty(t, remoteSidebar(t, s)[2].Summary.LastMessage)

	search, err := s.SearchSessions(ctx, "", "/srv/api", SearchFields, nil)
	require.NoError(t, err)
	require.Len(t, search.Matches, 1)
	assert.Equal(t, "opencode:box:ses_b", search.Matches[0].Chat.Session.Conversation.ID)

	fake.sessions = fake.sessions[:2]

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	assert.Len(t, remoteSidebar(t, s), 2)

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
	t.Parallel()

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
		_, err := s.UpdateConversationDetails(ctx, "opencode:box:ses_a", new("first"))
		return err
	})
	require.Equal(t, "first", <-titles)

	secondDone := make(chan struct{})

	renames.Go(func() error {
		defer close(secondDone)

		_, err := s.UpdateConversationDetails(ctx, "opencode:box:ses_a", new("second"))

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
    SELECT pid FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'
), sessions AS (
    SELECT l.pid FROM pg_locks l JOIN pg_database d ON d.oid = l.database
    WHERE d.datname = current_database() AND l.relation = 'conversations'::regclass
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
	t.Parallel()

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

			updated, err := s.UpdateConversationDetails(ctx, "opencode:box:ses_a", &name)

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
	t.Parallel()

	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	fake := &fakeOpenCode{
		sessions: []openCodeSession{openCodeTestSession("ses_a", "", "/srv/app", 4000), openCodeTestSession("ses_b", "", "/srv/app", 3000), openCodeTestSession("ses_c", "", "/srv/api", 2000), openCodeTestSession("ses_d", "", "/srv/gone", 1000)},
		active:   []string{"ses_a", "ses_b_child"},
		parents:  map[string]string{"ses_b_child": "ses_b", "ses_c_grandchild": "ses_c_child", "ses_c_child": "ses_c"},
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

	_, err := s.db.ExecContext(ctx, `DELETE FROM conversations`)
	require.NoError(t, err)

	fake.sessions = fake.sessions[:3]
	fake.locations = []string{"/srv/app", "/srv/api", "/srv/broken"}
	fake.pending = map[string]map[string][]string{
		"/api/form":               {"/srv/app": {"ses_a", "global"}, "/srv/api": {}},
		"/api/permission/request": {"/srv/app": {}, "/srv/api": {"ses_c_child"}},
	}

	asking := func() []bool {
		require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

		rows := remoteSidebar(t, s)

		asking := make([]bool, 0, len(rows))
		for _, row := range rows {
			asking = append(asking, row.Asking)
		}

		return asking
	}

	assert.Equal(t, []bool{true, false, true}, asking())

	fake.pending = nil

	assert.Equal(t, []bool{false, false, false}, asking())

	automation := func() []bool {
		require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

		rows := remoteSidebar(t, s)

		automation := make([]bool, 0, len(rows))
		for _, row := range rows {
			automation = append(automation, row.Automation)
		}

		return automation
	}

	fake.automations = `{"data":[
		{"sessionID":"ses_a","status":"active","nextDueAt":9000,"action":{"type":"code"}},
		{"sessionID":"ses_b","status":"active","nextDueAt":9000,"action":{"type":"prompt","to":{"session":"ses_c_child"}}},
		{"sessionID":"ses_b","status":"paused","nextDueAt":9000,"action":{"type":"shell"}},
		{"sessionID":"ses_b","status":"active","action":{"type":"prompt","to":{"session":"ses_b"}}}
	]}`

	assert.Equal(t, []bool{true, false, true}, automation(), "an active timer marks the root of the session it wakes; paused automations and event watches do not")

	fake.automations = ""

	assert.Equal(t, []bool{false, false, false}, automation())
}

func TestSyncOpenCodeMarksTurnSenders(t *testing.T) {
	t.Parallel()

	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	fake := &fakeOpenCode{messages: map[string][]openCodeTestMessage{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	box := config.OpenCodeConfig{URL: server.URL, Password: "pw"}

	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error { cfg.OpenCode["box"] = box; return nil }))
	id, err := s.CreateOpenCodeSession(ctx, "box", box, "/srv/app", "")
	require.NoError(t, err)

	unread := func(username string) []string {
		ids := []string{}

		for row, err := range s.sidebarSessions(ctx, username, nil) {
			require.NoError(t, err)

			if !row.Attention.UnreadAt.IsZero() {
				ids = append(ids, row.Conversation.ID)
			}
		}

		return ids
	}
	turn := func(updated int64, busy bool, messages ...openCodeTestMessage) {
		t.Helper()

		_, err := s.db.ExecContext(ctx, `DELETE FROM web_tab_attention; DELETE FROM web_tab_states`)
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

	turn(5000, true, prompt("alice", 5100))
	assert.Empty(t, fake.reads, "a busy session reads no messages")
	turn(5000, false, idle(5200))
	assert.Equal(t, []string{id}, unread("alice"))
	assert.Empty(t, unread("bob"))

	path := "/oc/" + base64.RawURLEncoding.EncodeToString([]byte(id))
	paths, err := queryStrings(ctx, s.db, `SELECT path FROM web_tab_attention`, "marked tabs")
	require.NoError(t, err)
	assert.Equal(t, []string{path}, paths, "the mark is on the tab path the Web spells")

	turn(6000, false, prompt("bob", 5900), prompt("", 5950), idle(6000))
	assert.Equal(t, []string{id}, unread("bob"))
	assert.Empty(t, unread("alice"))

	reads := len(fake.reads)

	turn(6000, false)
	assert.Len(t, fake.reads, reads)

	turn(7000, false, prompt("", 6500), idle(6600), idle(7000))
	assert.Empty(t, unread("alice"))
	assert.Empty(t, unread("bob"))
	assert.Empty(t, unread(""))

	require.NoError(t, s.ChangeAttention(ctx, "alice", []string{path}, AttentionParkUntilDone, nil, 0))

	fake.sessions = []openCodeSession{openCodeTestSession("ses_new", "", "/srv/app", 7040)}

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	assert.Equal(t, "read parked until=done", attentionOf(t, s, "alice", path))

	fake.sessions = []openCodeSession{openCodeTestSession("ses_new", "", "/srv/app", 7050)}
	fake.messages["ses_new"] = append(fake.messages["ses_new"], idle(7050))

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	assert.Equal(t, "new quiet parked until=done worked", attentionOf(t, s, "alice", path), "a turn end records work; the session's idleness ends the park")

	_, err = s.db.ExecContext(ctx, `UPDATE conversations SET background = TRUE WHERE conversation_id = $1`, id)
	require.NoError(t, err)
	require.NoError(t, s.tickParks(ctx))
	assert.Equal(t, "new quiet parked until=done worked", attentionOf(t, s, "alice", path), "background work on the remote session keeps it parked")

	_, err = s.db.ExecContext(ctx, `UPDATE conversations SET background = FALSE WHERE conversation_id = $1`, id)
	require.NoError(t, err)
	require.NoError(t, s.tickParks(ctx))
	assert.Equal(t, "new", attentionOf(t, s, "alice", path), "an idle remote session ends it")

	for i, outcome := range []string{"interrupted", "failed"} {
		require.NoError(t, s.ChangeAttention(ctx, "alice", []string{path}, AttentionParkUntilDone, nil, 0))

		created := int64(7060 + 10*i)
		fake.sessions = []openCodeSession{openCodeTestSession("ses_new", "", "/srv/app", created)}
		fake.messages["ses_new"] = append(fake.messages["ses_new"], openCodeTestMessage{Type: "idle", Created: created, Outcome: outcome})

		require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
		assert.Equal(t, map[string]string{"interrupted": "new quiet parked until=done worked", "failed": "new quiet parked until=due worked"}[outcome], attentionOf(t, s, "alice", path), outcome)
	}

	turn(7000, true)
	turn(7000, false, prompt("alice", 7100), idle(7200), prompt("bob", 7300), prompt("bob", 7350), idle(7400))
	assert.Equal(t, []string{id}, unread("alice"))
	assert.Equal(t, []string{id}, unread("bob"))

	_, err = s.db.ExecContext(ctx, `DELETE FROM web_tab_attention; DELETE FROM web_tab_states`)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, revision, viewing, viewed_at) VALUES ('alice', 0, $1, now() - interval '3 seconds'), ('bob', 0, $1, now() - interval '30 seconds')`, path)
	require.NoError(t, err)

	fake.sessions = []openCodeSession{openCodeTestSession("ses_new", "", "/srv/app", 7600)}
	fake.messages["ses_new"] = append(fake.messages["ses_new"], prompt("alice", 7500), prompt("bob", 7550), idle(7600))

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	assert.Empty(t, unread("alice"))
	assert.Equal(t, []string{id}, unread("bob"))

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

	require.NoError(t, s.ShareTab(ctx, "main", path, nil))
	_, err = s.db.ExecContext(ctx, `DELETE FROM web_tab_attention; DELETE FROM web_tab_states`)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, revision, viewing, viewed_at) VALUES ('alice', 0, '', now()), ('bob', 0, '', now()), ('carol', 0, $1, now())`, path)
	require.NoError(t, err)

	saved, err := s.SaveTabState(ctx, "dave", &TabState{Tabs: []Tab{{Path: path, Space: "main"}}}, 0)
	require.NoError(t, err)
	require.True(t, saved)

	fake.sessions = []openCodeSession{openCodeTestSession("ses_new", "", "/srv/app", 8500)}
	fake.messages["ses_new"] = append(fake.messages["ses_new"], idle(8500))

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	assert.Equal(t, []string{"alice loud", "bob loud", "dave loud"}, attentionOn(t, s, id))
}

func TestSyncOpenCodeChecksTheTitleEveryFourthPrompt(t *testing.T) {
	t.Parallel()

	for _, recorded := range []bool{false, true} {
		t.Run(fmt.Sprint("recorded=", recorded), func(t *testing.T) {
			t.Parallel()

			decisions := make(chan string, 8)
			judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				decisions <- string(body)

				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"model":"judge","usage":{},"answers":[{"type":"predicate","name":"title_fits","probability":0.1}]}`)
			}))
			t.Cleanup(judge.Close)

			titler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				writeResponseMessage(t, w, "title", "msg", "Discombobulation fix")
			}))
			t.Cleanup(titler.Close)

			s := newOpenCodeTestService(t, "box")
			ctx := t.Context()
			fake := &fakeOpenCode{sessions: []openCodeSession{openCodeTestSession("ses_a", "Worktree setup", "/srv/app", 1000)}, messages: map[string][]openCodeTestMessage{}, userPage: 3}
			server := httptest.NewServer(fake)
			t.Cleanup(server.Close)

			box := config.OpenCodeConfig{URL: server.URL, Password: "pw"}

			require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
				cfg.OpenCode["box"], cfg.OpenAI = box, config.OpenAIConfig{APIKey: "test", APIBaseURL: titler.URL}
				cfg.Decision, cfg.TitleDecisionModel = map[config.DecisionProvider]config.DecisionProviderConfig{config.DecisionOpenAI: {APIKey: "test", APIBaseURL: judge.URL}}, "openai/judge"

				return nil
			}))

			sync := func() {
				t.Helper()
				require.NoError(t, s.pushOpenCodeNames(ctx, "box", box))
				require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
				s.titles.Wait()
			}
			prompt := func(created int64, text string) {
				fake.sessions[0].Time.Updated = created + 2
				fake.messages["ses_a"] = append(fake.messages["ses_a"], openCodeTestMessage{Type: "user", Username: "alice", Created: created, Text: text},
					openCodeTestMessage{Type: "assistant", Created: created + 1, Text: "done: " + text}, openCodeTestMessage{Type: "idle", Created: created + 2})
			}
			turn := func(created int64, text string) {
				t.Helper()
				prompt(created, text)
				sync()
			}

			if recorded {
				_, err := s.recordOpenCodeSession(ctx, "box", &fake.sessions[0], "", "")
				require.NoError(t, err)
			}

			for i, text := range []string{"add a worktree", "second", "third", "fourth"} {
				prompt(int64(1000+10*i), text)
			}

			sync()
			require.Empty(t, decisions, "turns that ended before RocketClaw saw the session do not check the title")

			for i, text := range []string{"fifth", "sixth", "seventh"} {
				turn(int64(2000+10*i), text)
			}

			require.Empty(t, decisions, "the next three prompts do not check the title")

			turn(2100, "now fix the discombobulation")
			require.Len(t, decisions, 1)

			asked := <-decisions
			for _, want := range []string{`\"title\":\"Worktree setup\"`, `\"original_request\":\"add a worktree\"`, `User: second`, `Assistant: done: seventh`, `User: now fix the discombobulation`} {
				require.Contains(t, asked, want)
			}

			require.NotContains(t, asked, `User: add a worktree`, "the original request is not repeated in the recent conversation")
			require.NotContains(t, asked, "thinking", "assistant reasoning is not sent to the title check")
			require.Equal(t, "Discombobulation fix (was Worktree setup)", remoteSidebar(t, s)[0].Name, "an outgrown OpenCode title is regenerated and keeps the old one")
			require.Equal(t, "Discombobulation fix (was Worktree setup)", fake.sessions[0].Title, "the new title reaches OpenCode")

			sync()
			require.Equal(t, "Discombobulation fix (was Worktree setup)", remoteSidebar(t, s)[0].Name, "the next sync keeps the new title")

			updated, err := s.UpdateConversationDetails(ctx, "opencode:box:ses_a", new("Chosen name"))
			require.NoError(t, err)
			require.True(t, updated)

			for i, text := range []string{"ninth", "tenth", "eleventh", "twelfth"} {
				turn(int64(3000+10*i), text)
			}

			require.Empty(t, decisions, "a name typed in RocketClaw is never checked")
			require.Equal(t, "Chosen name", remoteSidebar(t, s)[0].Name)

			fake.sessions[0].Title = "Renamed in OpenCode"

			for i, text := range []string{"thirteenth", "fourteenth", "fifteenth", "sixteenth"} {
				turn(int64(4000+10*i), text)
			}

			require.Len(t, decisions, 1, "a title that came from OpenCode is checked again")
			require.Contains(t, <-decisions, `\"title\":\"Renamed in OpenCode\"`)
		})
	}
}

func TestSyncOpenCodeMarksAskingStarts(t *testing.T) {
	t.Parallel()

	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	fake := &fakeOpenCode{
		sessions:  []openCodeSession{openCodeTestSession("ses_q", "", "/srv/app", 5000)},
		active:    []string{"ses_q"},
		locations: []string{"/srv/app"},
		messages:  map[string][]openCodeTestMessage{"ses_q": {{Type: "user", Username: "bob", Created: 5100}}},
	}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	box := config.OpenCodeConfig{URL: server.URL, Password: "pw"}
	id := "opencode:box:ses_q"
	path := "/oc/" + base64.RawURLEncoding.EncodeToString([]byte(id))
	sync := func(asking bool) []string {
		t.Helper()

		fake.pending = map[string]map[string][]string{"/api/form": {"/srv/app": {}}, "/api/permission/request": {"/srv/app": {}}}
		if asking {
			fake.pending["/api/form"]["/srv/app"] = []string{"ses_q"}
		}

		require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

		return attentionOn(t, s, id)
	}

	require.Empty(t, sync(false))

	saved, err := s.SaveTabState(ctx, "alice", &TabState{Tabs: []Tab{{Path: path, Space: "main"}}}, 0)
	require.NoError(t, err)
	require.True(t, saved)
	require.NoError(t, s.ChangeAttention(ctx, "alice", []string{path}, AttentionPark, nil, 0))

	assert.Equal(t, []string{"alice quiet parked", "bob loud"}, sync(true), "the question makes Alice's park due")

	require.NoError(t, s.tickParks(ctx))
	require.NoError(t, s.tickParks(ctx))
	assert.Equal(t, "new resume=unpark", attentionOf(t, s, "alice", path), "the server ends it, and keeps the park to put back while the session still asks")

	_, err = s.db.ExecContext(ctx, `DELETE FROM web_tab_attention`)
	require.NoError(t, err)
	assert.Empty(t, sync(true), "a session that goes on asking marks no one again")
	assert.Empty(t, sync(false))
	assert.Equal(t, []string{"alice loud", "bob loud"}, sync(true), "asking again marks again")
}

func TestOpenCodeShellsFollowEvents(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	fake := &fakeOpenCode{
		locations: []string{"/srv/app", "/srv/gone"},
		shells:    map[string]map[string]string{"/srv/app": {"sh_old": "ses_a", "sh_kept": "ses_b"}},
		events:    make(chan string, 4),
	}
	fake.events <- `{"type":"shell.exited","data":{"id":"sh_old","status":"exited"}}`

	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	shells := &openCodeShells{running: map[string]string{"sh_stale": "ses_z"}}
	wake := make(chan struct{}, 1)
	done := make(chan struct{})

	go func() {
		shells.follow(ctx, "box", config.OpenCodeConfig{URL: server.URL, Password: "pw"}, wake)
		close(done)
	}()

	running := func(want map[string]string) func() bool {
		return func() bool {
			shells.mu.Lock()
			defer shells.mu.Unlock()

			return maps.Equal(want, shells.running)
		}
	}
	require.Eventually(t, running(map[string]string{"sh_kept": "ses_b"}), 5*time.Second, 10*time.Millisecond, "connecting replaces stale shells with the loaded locations'")
	require.Eventually(t, func() bool { return len(wake) == 1 }, 5*time.Second, 10*time.Millisecond, "connecting wakes the sync, since events before it were missed")
	<-wake

	for _, event := range []string{
		`{"type":"shell.created","data":{"info":{"id":"sh_new","status":"running","metadata":{"sessionID":"ses_c"}}}}`,
		`{"type":"session.text.delta","data":{"info":"not a shell"}}`,
		`{"type":"shell.deleted","data":{"id":"sh_kept"}}`,
	} {
		fake.events <- event
	}

	require.Eventually(t, running(map[string]string{"sh_new": "ses_c"}), 5*time.Second, 10*time.Millisecond)
	assert.Empty(t, wake, "events that are not a session's status leave the sync asleep")

	fake.events <- `{"type":"session.status","data":{"sessionID":"ses_c","status":{"type":"idle"}}}`

	fake.events <- `{"type":"session.status","data":{"sessionID":"ses_c","status":{"type":"busy"}}}`

	require.Eventually(t, func() bool { return len(wake) == 1 }, 5*time.Second, 10*time.Millisecond, "a session's status wakes the sync, and wakes that arrive together become one")

	close(fake.events)
	<-done
}

func TestSyncOpenCodeKeepsRowsOutsideTheWindow(t *testing.T) {
	t.Parallel()

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

	oldest := "opencode:box:" + fake.sessions[openCodeSyncLimit].ID
	fake.sessions = fake.sessions[:openCodeSyncLimit]
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET synced_at = now() - interval '1 minute' WHERE kind = 'remote'`)
	require.NoError(t, err)

	pg, err := pgx.ParseConfig(s.definitions.Clone().DatabaseURL)
	require.NoError(t, err)

	trace := &openCodeSyncRowTrace{rows: -1}
	pg.Tracer = trace
	db := stdlib.OpenDB(*pg)
	original := s.db
	s.db = db

	t.Cleanup(func() {
		s.db = original
		require.NoError(t, db.Close())
	})
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	assert.Equal(t, int64(len(fake.sessions)), trace.rows)

	rows := remoteSidebar(t, s)
	i := slices.IndexFunc(rows, func(row SidebarSession) bool { return row.Conversation.ID == oldest })
	require.NotEqual(t, -1, i)
	assert.False(t, rows[i].Offline)

	fake.sessions = append(fake.sessions, openCodeTestSession("ses_never_listed", "", "/srv", 1))

	require.NoError(t, s.pruneOpenCode(ctx, "box", box))

	rows = remoteSidebar(t, s)
	assert.Len(t, rows, openCodeSyncLimit)
	assert.False(t, slices.ContainsFunc(rows, func(row SidebarSession) bool {
		return row.Conversation.ID == oldest || row.Conversation.ID == "opencode:box:ses_never_listed"
	}))
}

type openCodeSyncRowTrace struct{ rows int64 }

type openCodeSyncQueryKey struct{}

func (trace *openCodeSyncRowTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "SELECT conversation_id, running, asking, last_updated") {
		return context.WithValue(ctx, openCodeSyncQueryKey{}, true)
	}

	return ctx
}

func (trace *openCodeSyncRowTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if ctx.Value(openCodeSyncQueryKey{}) == true {
		trace.rows = data.CommandTag.RowsAffected()
	}
}

func BenchmarkSyncOpenCodeWindow(b *testing.B) {
	service, err := NewSessionService(b.TempDir())
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, service.Stop()) })
	ctx := b.Context()

	fake := &fakeOpenCode{}
	for i := range openCodeSyncLimit {
		fake.sessions = append(fake.sessions, openCodeTestSession(fmt.Sprintf("ses_recent_%03d", i), "", "/srv", int64(10_000-i)))
	}

	_, err = service.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, remote_session, last_updated, synced_at)
SELECT 'opencode:box:ses_older_' || i, '', '', 'remote', FALSE, 'remote', 'box', 'ses_older_' || i, to_timestamp(1), now()
FROM generate_series(1, 1024) AS series(i)`)
	require.NoError(b, err)

	server := httptest.NewServer(fake)
	b.Cleanup(server.Close)
	box := config.OpenCodeConfig{URL: server.URL, Password: "pw"}
	shells := &openCodeShells{}
	require.NoError(b, service.syncOpenCode(ctx, "box", box, shells))
	b.ReportAllocs()

	for b.Loop() {
		require.NoError(b, service.syncOpenCode(ctx, "box", box, shells))
	}
}

func TestSyncOpenCodePrunesOnItsOwnSchedule(t *testing.T) {
	t.Parallel()

	s := newOpenCodeTestService(t, "box")
	fake := &fakeOpenCode{}
	prunes, passes, release := make(chan struct{}, 10), make(chan struct{}, 10), make(chan struct{})
	pruneArrived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("order") == "asc":
			prunes <- struct{}{}

			select {
			case <-pruneArrived:
			default:
				close(pruneArrived)
			}

			select {
			case <-release:
			case <-r.Context().Done():
			}

			w.WriteHeader(http.StatusServiceUnavailable)

			return
		case r.URL.Path == "/api/session/active":
			select {
			case <-pruneArrived:
			case <-r.Context().Done():
				return
			}

			passes <- struct{}{}
		}

		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.OpenCode["box"] = config.OpenCodeConfig{URL: server.URL, Password: "pw"}
		cfg.Web.Tsnet = &config.TsnetConfig{}

		return nil
	}))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var syncing errgroup.Group
	syncing.Go(func() error {
		s.SyncOpenCode(ctx, s.definitions)
		return nil
	})

	select {
	case <-prunes:
	case <-time.After(openCodeSyncInterval):
		require.FailNow(t, "the prune starts at startup, beside the first sync pass")
	}

	select {
	case <-passes:
	case <-time.After(openCodeSyncInterval / 2):
		require.FailNow(t, "a sync pass runs while the prune is slow")
	}

	close(release)

	select {
	case <-passes:
	case <-time.After(2 * openCodeSyncInterval):
		require.FailNow(t, "sync keeps running after the prune failed")
	}

	require.Never(t, func() bool { return len(prunes) > 0 }, time.Second, 10*time.Millisecond, "a failed prune waits for the next day")

	cancel()
	require.NoError(t, syncing.Wait())
}

func TestPruneOpenCodeListsEveryPageFirst(t *testing.T) {
	t.Parallel()

	for _, failing := range []int{0, 2} {
		t.Run(strconv.Itoa(failing), func(t *testing.T) {
			t.Parallel()

			s := newOpenCodeTestService(t, "box")
			ctx := t.Context()

			sessions := make([]openCodeSession, 2*openCodePrunePage+1)
			for i := range sessions {
				sessions[i] = openCodeTestSession(fmt.Sprintf("ses_%05d", i), "", "/srv", int64(i+1))
			}

			key := func(session openCodeSession) string { return fmt.Sprintf("%020d %s", session.Time.Updated, session.ID) }
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				query := r.URL.Query()

				if limit, _ := strconv.Atoi(query.Get("limit")); requests == failing || limit > openCodePrunePage {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}

				order, anchor := cmp.Or(query.Get("order"), "desc"), ""
				if cursor := query.Get("cursor"); cursor != "" {
					order, anchor, _ = strings.Cut(cursor, " ")
				}

				listed := slices.SortedFunc(slices.Values(sessions), func(a, b openCodeSession) int { return strings.Compare(key(a), key(b)) })
				if order == "desc" {
					slices.Reverse(listed)
				}

				listed = slices.DeleteFunc(listed, func(session openCodeSession) bool {
					return anchor != "" && ((order != "asc" || key(session) <= anchor) && (order != "desc" || key(session) >= anchor))
				})
				listed = listed[:min(len(listed), openCodePrunePage)]

				response := map[string]any{"data": listed, "cursor": map[string]string{}}
				if len(listed) > 0 {
					response["cursor"] = map[string]string{"next": order + " " + key(listed[len(listed)-1])}
				}

				_ = json.NewEncoder(w).Encode(response)

				if requests == 1 {
					sessions[openCodePrunePage].Time.Updated = int64(len(sessions) + 1)
				}
			}))
			t.Cleanup(server.Close)

			_, err := s.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, title, directory, last_updated, running, synced_at, remote_session)
SELECT 'opencode:box:' || id, '', '', 'remote', FALSE, 'remote', 'box', '', '/srv', now(), FALSE, now() - interval '1 minute', id FROM unnest($1::text[]) AS id`,
				[]string{sessions[openCodePrunePage].ID, sessions[len(sessions)-1].ID, "ses_gone"})
			require.NoError(t, err)

			kept := []string{"opencode:box:" + sessions[openCodePrunePage].ID, "opencode:box:" + sessions[len(sessions)-1].ID}

			errPrune := s.pruneOpenCode(ctx, "box", config.OpenCodeConfig{URL: server.URL, Password: "pw"})
			if failing > 0 {
				require.Error(t, errPrune)

				kept = append(kept, "opencode:box:ses_gone")
			} else {
				require.NoError(t, errPrune)
			}

			ids, err := queryStrings(ctx, s.db, `SELECT conversation_id FROM conversations WHERE server = 'box' ORDER BY 1`, "pruned rows")
			require.NoError(t, err)
			assert.Equal(t, kept, ids)
		})
	}
}

func TestToggleManualTag(t *testing.T) {
	t.Parallel()

	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	server := httptest.NewServer(&fakeOpenCode{sessions: []openCodeSession{openCodeTestSession("ses_a", "", "/srv", 1000)}})
	t.Cleanup(server.Close)
	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.OpenCode["box"] = config.OpenCodeConfig{URL: server.URL, Password: "pw"}
		return nil
	}))

	agents := harness.Agents{Items: map[string]harness.Agent{"main": {TagGroups: [][]string{{"customer", "internal"}}}}}

	createWebChat(t, s, protocol.Conversation{ID: "web", Agent: "main"})
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, title, directory, last_updated, running, synced_at, remote_session)
VALUES ('opencode:box:ses_a', '', '', 'remote', FALSE, 'remote', 'box', '', '/srv', now(), FALSE, now(), 'ses_a')`)
	require.NoError(t, err)

	for _, step := range [][2]string{{"web", "customer"}, {"web", "internal"}, {"opencode:box:ses_a", "billing"}, {"opencode:box:ses_a", "urgent"}} {
		require.NoError(t, s.ToggleManualTag(ctx, agents, step[0], step[1]))
	}

	require.ErrorIs(t, s.ToggleManualTag(ctx, agents, "web", "billing"), ErrTagNotAllowed)

	for id, want := range map[string][]string{"web": {"internal"}, "opencode:box:ses_a": {"billing", "urgent"}} {
		tags, err := sessionTags(ctx, s.db, id)
		require.NoError(t, err)
		assert.Equal(t, want, tags, id)
	}

	for _, id := range []string{"web", "opencode:box:ses_a"} {
		updated, err := s.UpdateConversationDetails(ctx, id, new("Named "+id))
		require.NoError(t, err)
		require.True(t, updated)
	}

	facts, err := queryStrings(ctx, s.db, `SELECT concat_ws(' ', conversation_id, kind, tags::text, name) FROM conversations ORDER BY 1`, "conversation facts")
	require.NoError(t, err)
	assert.Equal(t, []string{`opencode:box:ses_a remote ["billing", "urgent"] Named opencode:box:ses_a`, `web web_chat ["internal"] Named web`}, facts)
}

type openCodeLockProbe struct {
	fake *fakeOpenCode
	s    *SessionService
	held chan bool
}

func (p *openCodeLockProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPatch {
		_, err := p.s.db.ExecContext(r.Context(), `SELECT 1 FROM conversations WHERE remote_session = $1 FOR UPDATE NOWAIT`, strings.TrimPrefix(r.URL.Path, "/api/session/"))
		p.held <- err != nil
	}

	p.fake.ServeHTTP(w, r)
}

func TestOpenCodeMovesOldNamesToTheServer(t *testing.T) {
	t.Parallel()

	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	fake := &fakeOpenCode{sessions: []openCodeSession{openCodeTestSession("ses_a", "Alpha", "/srv/app", 2000), openCodeTestSession("ses_b", "Beta", "/srv/app", 1000)}}
	probe := &openCodeLockProbe{fake: fake, s: s, held: make(chan bool, 4)}
	server := httptest.NewServer(probe)
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

	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET name = 'old work' WHERE conversation_id = 'opencode:box:ses_b'`)
	require.NoError(t, err)
	require.NoError(t, s.pushOpenCodeNames(ctx, "box", config.OpenCodeConfig{URL: server.URL, Password: "wrong"}))
	require.Len(t, probe.held, 1)
	<-probe.held
	require.Equal(t, []string{"Alpha|Alpha", "old work|Beta"}, names(), "a refused name stays to move later")

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	require.Equal(t, []string{"Alpha|Alpha", "old work|Beta"}, names(), "a sync keeps a name that still waits to move")

	require.NoError(t, s.pushOpenCodeName(ctx, "box", box, "ses_b", "older work"))
	require.Empty(t, probe.held, "a name that changed since it was read is not sent")
	require.Equal(t, "Beta", fake.sessions[1].Title)

	require.NoError(t, s.pushOpenCodeNames(ctx, "box", box))
	require.Len(t, probe.held, 1)
	assert.True(t, <-probe.held, "the row stays locked while its name is sent, so a rename waits for the push")
	assert.Equal(t, []string{"Alpha", "old work"}, []string{fake.sessions[0].Title, fake.sessions[1].Title})
	assert.Equal(t, []string{"Alpha|Alpha", "old work|old work"}, names())

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

	var generated, typed string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT COALESCE(generated_name, ''), array_to_string(typed_titles, ',') FROM conversations WHERE conversation_id = 'opencode:box:ses_b'`).Scan(&generated, &typed))
	assert.Equal(t, []string{"Alpha|Alpha", "old work|old work"}, names())
	assert.NotEqual(t, "old work", generated, "a moved name stays typed")
	assert.Empty(t, typed)
}

func TestSyncOpenCodeKeepsATypedTitleTyped(t *testing.T) {
	t.Parallel()

	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	fake := &fakeOpenCode{sessions: []openCodeSession{openCodeTestSession("ses_a", "Alpha", "/srv/app", 1000)}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	box := config.OpenCodeConfig{URL: server.URL, Password: "pw"}

	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error { cfg.OpenCode["box"] = box; return nil }))
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

	row := func() string {
		var name, generated, typed string
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT name, COALESCE(generated_name, ''), array_to_string(typed_titles, ',') FROM conversations WHERE conversation_id = 'opencode:box:ses_a'`).Scan(&name, &generated, &typed))

		return name + "|" + generated + "|" + typed
	}

	_, err := s.db.ExecContext(ctx, `CREATE FUNCTION fail_typed_save() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'save failed'; END $$;
CREATE CONSTRAINT TRIGGER fail_typed_save AFTER UPDATE ON conversations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.name = 'Typed') EXECUTE FUNCTION fail_typed_save()`)
	require.NoError(t, err)

	_, err = s.UpdateConversationDetails(ctx, "opencode:box:ses_a", new("Typed"))
	require.ErrorContains(t, err, "save failed")
	require.Equal(t, "Typed", fake.sessions[0].Title, "OpenCode took the name before the save failed")
	require.Equal(t, "Alpha|Alpha|Typed", row())

	_, err = s.db.ExecContext(ctx, `DROP TRIGGER fail_typed_save ON conversations`)
	require.NoError(t, err)
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	require.Equal(t, "Typed|Alpha|", row(), "a typed name OpenCode took while RocketClaw failed to save it stays typed")

	updated, err := s.UpdateConversationDetails(ctx, "opencode:box:ses_a", new("Chosen"))
	require.NoError(t, err)
	require.True(t, updated)
	require.Equal(t, "Chosen|Alpha|", row(), "a saved rename leaves no mark behind")
}

func TestOpenCodeUpstreamFailuresWriteNothing(t *testing.T) {
	t.Parallel()

	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(gone.Close)

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

		_, err := s.CreateOpenCodeSession(ctx, "box", box, "/srv", "")
		require.Error(t, err, url)
	}

	require.Error(t, s.pruneOpenCode(ctx, "box", config.OpenCodeConfig{URL: gone.URL, Password: "pw"}))
	assert.Empty(t, remoteSidebar(t, s), "a failed poll writes nothing")
}

func TestSearchSessionsFiltersByServerAgentAndModel(t *testing.T) {
	t.Parallel()

	s := newOpenCodeTestService(t, "box", "lab")
	ctx := t.Context()

	root, err := os.OpenRoot(s.definitions.Clone().Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(".rocketclaw/agents", 0o755))
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/agents/primary.md", []byte("---\nmodel: gpt-6.1-sol\n---\nPrompt\n"), 0o644))
	createWebChat(t, s, protocol.Conversation{ID: "web-session:local", Agent: "primary"})
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
		"model:gpt-6.1-sol agent:primary":   {"web-session:local"},
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
	t.Parallel()

	fake := &fakeOpenCode{sessions: []openCodeSession{openCodeTestSession("ses_new", "", "/srv/app", 5000)}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	box := config.OpenCodeConfig{URL: server.URL, Password: "pw"}
	spaces, err := s.Spaces(ctx)
	require.NoError(t, err)

	id, err := s.CreateOpenCodeSession(ctx, "box", box, "/srv/app", "no-such-space")
	require.NoError(t, err)
	assert.Equal(t, "opencode:box:ses_new", id)
	assert.Equal(t, []string{"/srv/app"}, fake.created)

	rows := remoteSidebar(t, s)
	require.Len(t, rows, 1)
	assert.Equal(t, "/srv/app", rows[0].Directory)
	assert.Empty(t, rows[0].SpaceID)

	_, err = s.CreateOpenCodeSession(ctx, "box", box, "/srv/app", spaces[0].ID)
	require.NoError(t, err)
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	assert.Equal(t, spaces[0].ID, remoteSidebar(t, s)[0].SpaceID)
}

func TestOpenCodeSessionPagesOutliveTheirSession(t *testing.T) {
	t.Parallel()

	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	fake := &fakeOpenCode{sessions: []openCodeSession{openCodeTestSession("ses_a", "", "/srv", 2000)}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	box := config.OpenCodeConfig{URL: server.URL, Password: "pw"}
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	_, err := s.db.ExecContext(ctx, `WITH space AS (INSERT INTO web_spaces (name) VALUES ('Billing') RETURNING id)
UPDATE conversations c SET space_id = space.id FROM space WHERE c.conversation_id = 'opencode:box:ses_a'`)
	require.NoError(t, err)

	for _, page := range []Page{{Kind: PageFile, Name: "index.html"}, {Kind: PageData, Name: "rows"}} {
		page.ContentType, page.Body = "text/html", []byte("1")
		require.NoError(t, s.PutPage(ctx, &page, "opencode:box:ses_a", ""))
		assert.Equal(t, "Billing", page.Space)
	}

	fake.sessions = nil

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

	pages, err := s.Pages(ctx, "billing")
	require.NoError(t, err)
	assert.Len(t, pages, 2, "a session's pages belong to its space and stay when the session goes")
}

func TestSidebarHidesUnconfiguredOpenCodeServers(t *testing.T) {
	t.Parallel()

	s := newOpenCodeTestService(t, "box")
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, title, directory, last_updated, running, synced_at, remote_session)
VALUES ('opencode:box:ses_a', '', '', 'remote', FALSE, 'remote', 'box', '', '/srv', now(), FALSE, now(), 'ses_a'),
    ('opencode:gone:ses_b', '', '', 'remote', FALSE, 'remote', 'gone', '', '/srv', now(), FALSE, now(), 'ses_b')`)
	require.NoError(t, err)

	rows := remoteSidebar(t, s)
	require.Len(t, rows, 1)
	assert.Equal(t, "opencode:box:ses_a", rows[0].Conversation.ID)

	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.OpenCode["gone"] = config.OpenCodeConfig{}
		return nil
	}))

	rows = remoteSidebar(t, s)
	require.Len(t, rows, 2)
	assert.Equal(t, "opencode:gone:ses_b", rows[1].Conversation.ID)
}

func TestDeleteSessionRefusesRemoteSessions(t *testing.T) {
	t.Parallel()

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

func TestOpenCodeAPITool(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "opencode" || password != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		body, _ := io.ReadAll(r.Body)

		switch r.Method + " " + r.URL.Path {
		case "GET /openapi.json":
			_, _ = w.Write([]byte(`{"paths":{"/api/session/{sessionID}/prompt":{"summary":"Prompt","post":{"operationId":"session.prompt"}}}}`))
		case "GET /api/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "DELETE /api/session/ses_1":
			w.WriteHeader(http.StatusNoContent)
		case "POST /api/session/ses_1/prompt", "GET /api/session":
			_, _ = fmt.Fprintf(w, "%s %s %s %s", r.Method, r.URL.EscapedPath(), r.URL.RawQuery, body)
		default:
			http.Error(w, "missing", http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	tool := openCodeAPITool(map[string]config.OpenCodeConfig{"box": {URL: upstream.URL + "/", Password: "pw"}, "spare": {URL: upstream.URL, Password: "pw"}})
	assert.Equal(t, "opencode", tool.Permission)
	assert.Equal(t, []string{"box", "spare"}, tool.VisibilitySubjects)

	firstLine, _, _ := strings.Cut(tool.Description, "\n")
	assert.LessOrEqual(t, len(firstLine), 120)
	assert.Contains(t, firstLine, "params: list of {name, value} dicts")

	tests := []struct {
		name, input, want, wantErr string
	}{
		{name: "operation ID", input: `{"server":"box","operation":"session.prompt","params":[{"name":"sessionID","value":"ses_1"},{"name":"location[directory]","value":"/repo"}],"body":"{\"text\":\"hi\"}"}`, want: `POST /api/session/ses_1/prompt location%5Bdirectory%5D=%2Frepo {"text":"hi"}`},
		{name: "method and path", input: `{"server":"box","operation":"get /api/session?limit=5","params":[{"name":"order","value":"desc"}],"body":""}`, want: "GET /api/session limit=5&order=desc "},
		{name: "empty body", input: `{"server":"box","operation":"DELETE /api/session/ses_1","params":[],"body":""}`, want: "204 No Content"},
		{name: "event stream", input: `{"server":"box","operation":"GET /api/event","params":[],"body":""}`, wantErr: "opencode box /api/event is an event stream"},
		{name: "failure status", input: `{"server":"box","operation":"GET /api/nope","params":[],"body":""}`, wantErr: "opencode box /api/nope: status 404: missing"},
		{name: "unknown operation", input: `{"server":"box","operation":"session.nope","params":[],"body":""}`, wantErr: `opencode server "box" has no operation "session.nope"`},
		{name: "relative path", input: `{"server":"box","operation":"GET @elsewhere/api","params":[],"body":""}`, wantErr: `path "@elsewhere/api" must start with /`},
		{name: "unknown server", input: `{"server":"other","operation":"GET /api/session","params":[],"body":""}`, wantErr: `opencode server "other" is not configured`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subjects, err := tool.Subjects(json.RawMessage(tt.input))
			require.NoError(t, err)
			assert.Len(t, subjects, 1)

			result, err := tool.Call(t.Context(), json.RawMessage(tt.input), nil)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, harness.TextToolResult(tt.want), result)
		})
	}
}

func TestOpenCodeParkWaitsForItsOwnPrompt(t *testing.T) {
	t.Parallel()

	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	fake := &fakeOpenCode{messages: map[string][]openCodeTestMessage{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	box := config.OpenCodeConfig{URL: server.URL, Password: "pw"}

	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error { cfg.OpenCode["box"] = box; return nil }))
	id, err := s.CreateOpenCodeSession(ctx, "box", box, "/srv/app", "")
	require.NoError(t, err)
	require.NoError(t, s.SetAutoParkAll(ctx, "alice", true))

	path := "/oc/" + base64.RawURLEncoding.EncodeToString([]byte(id))
	updated := int64(5000)
	opencode := func(busy bool, messages ...openCodeTestMessage) {
		t.Helper()

		updated += 100
		fake.sessions = []openCodeSession{openCodeTestSession("ses_new", "", "/srv/app", updated)}

		fake.active = nil
		if busy {
			fake.active = []string{"ses_new"}
		}

		fake.messages["ses_new"] = append(fake.messages["ses_new"], messages...)
	}
	prompt := func(created int64) openCodeTestMessage {
		return openCodeTestMessage{Type: "user", Username: "alice", Created: created}
	}
	idle := func(created int64, outcome string) openCodeTestMessage {
		return openCodeTestMessage{Type: "idle", Created: created, Outcome: outcome}
	}
	sync := func() {
		t.Helper()
		require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
		require.NoError(t, s.tickParks(ctx))
	}
	sent := func(admitted int64) {
		t.Helper()
		require.NoError(t, s.ChangeAttention(ctx, "alice", []string{path}, AttentionSent, nil, admitted))
	}
	parked := func() bool {
		t.Helper()
		return strings.Contains(attentionOf(t, s, "alice", path), "parked until=done")
	}

	listing, stop := s.WatchListing("alice")
	defer stop()

	opencode(true, prompt(5100))
	sync()
	assert.Len(t, s.parkWake, 1, "a sync wakes the park-ending step")
	<-s.parkWake
	assert.Len(t, listing, 1, "a sync that finds the session running rings every window")
	<-listing

	opencode(true)
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	assert.Empty(t, listing, "a sync that only sees the session's update time move rings nobody")

	opencode(false, idle(5200, ""))
	sent(5300)
	sync()
	assert.True(t, parked(), "the turn that ended before OpenCode admitted the prompt leaves its park")

	opencode(true, prompt(5300))
	sync()
	assert.True(t, parked())

	opencode(false, idle(5400, ""))
	sync()
	assert.False(t, parked(), "the idle marker after the admitted prompt ends its park")

	opencode(false, prompt(5500), idle(5600, ""))
	sync()
	sent(5500)
	require.NoError(t, s.tickParks(ctx))
	assert.False(t, parked(), "a turn that ended before its park was recorded ends the park at once")

	opencode(false, idle(5700, "failed"))
	sent(5800)
	sync()
	assert.True(t, parked(), "a failed turn from before the admitted prompt leaves its park")

	opencode(true, prompt(5800))
	sync()
	opencode(false, idle(5900, "failed"))
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	assert.Contains(t, attentionOf(t, s, "alice", path), "parked until=due", "a failed turn after the admitted prompt ends its park at once")
	require.NoError(t, s.tickParks(ctx))

	opencode(true, prompt(6000))
	sync()
	sent(6050)
	opencode(true, idle(6100, ""))

	fake.activeAt = [][]string{{}}

	sync()
	assert.True(t, parked(), "a prompt admitted while a turn settles keeps its park while OpenCode runs it next")

	opencode(false, idle(6200, ""))
	sync()
	assert.False(t, parked(), "the successor turn's idle marker ends the park")

	opencode(true, prompt(6250))
	sync()
	sent(6250)
	opencode(true, idle(6260, "failed"))

	fake.activeAt = [][]string{{}}

	sync()
	assert.True(t, parked(), "a failed turn that OpenCode moves straight past keeps the park until the session goes idle")

	opencode(false, idle(6270, ""))
	sync()
	assert.False(t, parked())

	opencode(false, idle(6300, ""))
	require.NoError(t, s.ChangeAttention(ctx, "alice", []string{path}, AttentionParkUntilDone, nil, 0))
	sync()
	assert.True(t, parked(), "Park until it is done on an idle session waits for the next round of work, even before the last idle marker is synced")

	opencode(false, idle(6400, ""))
	sync()
	assert.False(t, parked(), "a turn OpenCode starts by itself ends the park")

	recordTestConversations(t, s, "main", map[string]ConversationKind{"chat": KindWebChat})

	native := "/s/" + base64.RawURLEncoding.EncodeToString([]byte("chat"))

	server.Close()
	require.NoError(t, s.ChangeAttention(ctx, "alice", []string{path, native}, AttentionParkUntilDone, nil, 0), "an unreachable OpenCode server refuses none of the parks")
	assert.True(t, parked())
	assert.Contains(t, attentionOf(t, s, "alice", native), "parked until=done")

	var afterIdle int64
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT after_idle FROM web_tab_attention WHERE username = 'alice' AND path = $1`, path).Scan(&afterIdle))
	assert.Equal(t, int64(6400), afterIdle, "the park waits past the last idle marker the sync stored")
}
