package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
)

type basicAuthTransport struct{ user, password string }

func (b basicAuthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.SetBasicAuth(b.user, b.password)

	response, err := http.DefaultTransport.RoundTrip(request)
	if err != nil {
		return nil, fmt.Errorf("send with basic auth: %w", err)
	}

	return response, nil
}

func TestOpenCodeMCP(t *testing.T) {
	t.Parallel()

	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	fake := &fakeOpenCode{
		sessions: []openCodeSession{openCodeTestSession("ses_a", "", "/srv", 3000), openCodeTestSession("ses_b", "", "/srv", 2000), openCodeTestSession("ses_c", "", "/srv", 1000)},
		parents:  map[string]string{"ses_child": "ses_a", "ses_lone": ""},
	}
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	box := config.OpenCodeConfig{URL: upstream.URL, Password: "pw"}
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	_, err := s.db.ExecContext(ctx, `WITH space AS (INSERT INTO web_spaces (name) VALUES ('Billing') RETURNING id)
UPDATE conversations c SET space_id = space.id FROM space WHERE c.conversation_id IN ('opencode:box:ses_a', 'opencode:box:ses_c')`)
	require.NoError(t, err)

	cfg := config.NewLockedConfig(&config.Config{OpenCode: map[string]config.OpenCodeConfig{"box": box}, Web: config.WebConfig{Tsnet: &config.TsnetConfig{Origin: "https://rc.example"}}})
	rt := &Runtime{Cfg: cfg, Sessions: s, threads: &threadBridgeManager{runtime: cfg, store: s}}
	mux := http.NewServeMux()
	mux.Handle("/opencode/{name}/mcp", rt.OpenCodeMCP())
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	connect := func(name, password string) (*mcp.ClientSession, error) {
		client := mcp.NewClient(&mcp.Implementation{Name: "opencode", Version: "test"}, nil)
		return client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/opencode/" + name + "/mcp", HTTPClient: &http.Client{Transport: basicAuthTransport{"opencode", password}}, DisableStandaloneSSE: true}, nil)
	}

	for _, refused := range [][2]string{{"box", "wrong"}, {"nope", "pw"}} {
		_, err := connect(refused[0], refused[1])
		require.Error(t, err, refused)
	}

	session, err := connect("box", "pw")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, session.Close()) })
	assert.Contains(t, session.InitializeResult().Instructions, "space", "the agent learns the bridge's rules when it connects")

	call := func(caller, tool string, arguments any) (string, bool) {
		t.Helper()

		params := &mcp.CallToolParams{Name: tool, Arguments: arguments}
		if caller != "" {
			params.Meta = mcp.Meta{"ai.opencode/sessionID": caller}
		}

		result, err := session.CallTool(ctx, params)
		require.NoError(t, err)
		require.Len(t, result.Content, 1)

		return result.Content[0].(*mcp.TextContent).Text, result.IsError
	}
	must := func(caller, tool string, arguments any) string {
		t.Helper()

		out, failed := call(caller, tool, arguments)
		require.False(t, failed, out)

		return out
	}
	none := map[string]any{}

	tools, err := session.ListTools(ctx, nil)
	require.NoError(t, err)

	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		assert.NotContains(t, tool.Description, "source", tool.Name)
		assert.NotContains(t, tool.InputSchema.(map[string]any)["properties"], "source", tool.Name)
	}

	assert.ElementsMatch(t, []string{"store", "read", "kv_store", "kv_read", "delete", "list", "get_tags", "toggle_tag", "navigate_for_me", "ask_secret", "run_with_secrets"}, names)

	fake.messages = map[string][]openCodeTestMessage{
		"ses_a": {{Type: "user", Username: bob, Created: 1}, {Type: "user", Username: alice, Created: 2}},
		"ses_c": {{Type: "user", Username: alice, Created: 1}, {Type: "user", Created: 2}},
	}

	assert.Equal(t, "https://rc.example/s/eA?x=1", must("ses_child", "navigate_for_me", map[string]string{"location": "https://rc.example/s/eA?x=1"}))

	for _, caller := range []string{"ses_b", "ses_c"} {
		out, failed := call(caller, "navigate_for_me", map[string]string{"location": "/cron"})
		assert.True(t, failed, out)
	}

	taken := func(person, viewing string) string {
		_, navigation, err := s.PollAttention(ctx, person, viewing, false)
		require.NoError(t, err)

		return navigation
	}
	assert.Equal(t, []string{"", "", "/s/eA?x=1", ""}, []string{taken(alice, ""), taken(bob, "/"), taken(alice, "/"), taken(alice, "/")}, "only a focused window of the person who asked takes it, once")

	assert.JSONEq(t, `{"tags":["customer"]}`, must("ses_a", "toggle_tag", map[string]string{"tag": " customer "}))
	assert.JSONEq(t, `{"tags":["customer"]}`, must("ses_child", "get_tags", none))
	assert.JSONEq(t, `{"tags":[]}`, must("ses_child", "toggle_tag", map[string]string{"tag": "customer"}))
	assert.JSONEq(t, `{"tags":["triage"]}`, must("ses_b", "toggle_tag", map[string]string{"tag": "triage"}))

	for _, tag := range []string{" ", "a\x00b"} {
		out, failed := call("ses_a", "toggle_tag", map[string]string{"tag": tag})
		assert.True(t, failed, out)
	}

	assert.JSONEq(t, `{"tags":[]}`, must("ses_a", "get_tags", none))

	out, failed := call("", "get_tags", none)
	assert.True(t, failed)
	assert.Contains(t, out, "ai.opencode/sessionID")

	for _, caller := range []string{"ses_lone", "ses_x"} {
		out, failed := call(caller, "get_tags", none)
		assert.True(t, failed, out)
	}

	kind, err := s.ConversationKind(ctx, "opencode:box:ses_lone")
	require.NoError(t, err)
	assert.Empty(t, kind)

	spaces, err := s.Spaces(ctx)
	require.NoError(t, err)

	for person, space := range map[string]string{alice: spaces[1].ID, bob: spaces[0].ID} {
		saved, err := s.SaveTabState(ctx, person, &TabState{Tabs: []Tab{{Path: "/pages", Space: space}}}, 0)
		require.NoError(t, err)
		require.True(t, saved)
	}

	assert.Equal(t, "https://rc.example/pages/Billing/index.html", must("ses_child", "store", map[string]string{"path": "index.html", "content": "<p>a</p>"}))
	assert.Equal(t, []string{alice + " /pages loud"}, locationMarks(t, s, "index.html updated"))
	assert.JSONEq(t, `{"content_type":"text/html","content":"<p>a</p>"}`, must("ses_a", "read", map[string]string{"path": "index.html"}))

	for tool, arguments := range map[string]any{"store": map[string]string{"path": "index.html", "content": "<p>b</p>"}, "kv_store": map[string]any{"key": "rows", "value": 1}, "list": none} {
		out, failed := call("ses_b", tool, arguments)
		assert.True(t, failed, tool)
		assert.Contains(t, out, openCodePageNoSpace, tool)
		assert.NotContains(t, out, "space:", tool)
	}

	out, failed = call("ses_a", "store", map[string]string{"path": "x.js", "source": "x.js"})
	assert.True(t, failed, out)
	assert.Contains(t, out, "has no content")
	must("ses_a", "store", map[string]string{"path": "x.js", "content": "x()", "source": "x.js"})

	large := strings.Repeat("x", 6<<20)
	must("ses_a", "store", map[string]string{"path": "large.txt", "content_base64": base64.StdEncoding.EncodeToString([]byte(large))})

	var read struct {
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal([]byte(must("ses_a", "read", map[string]string{"path": "large.txt"})), &read))
	assert.Equal(t, large, read.Content)

	assert.Equal(t, "stored rows", must("ses_a", "kv_store", map[string]any{"key": "rows", "value": []int{1, 2}}))
	assert.JSONEq(t, `[1,2]`, must("ses_a", "kv_read", map[string]string{"key": "rows"}))
	must("ses_c", "store", map[string]string{"path": "other.html", "content": "<p>c</p>"})

	var listed struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
		Keys []struct {
			Key string `json:"key"`
		} `json:"keys"`
	}
	require.NoError(t, json.Unmarshal([]byte(must("ses_a", "list", none)), &listed))
	require.Len(t, listed.Files, 4, "sessions in one space share its pages")
	assert.Equal(t, "index.html", listed.Files[0].Path)
	assert.Equal(t, "large.txt", listed.Files[1].Path)
	assert.Equal(t, "other.html", listed.Files[2].Path)
	assert.Equal(t, "x.js", listed.Files[3].Path)
	require.Len(t, listed.Keys, 1)
	assert.Equal(t, "rows", listed.Keys[0].Key)

	assert.Equal(t, "deleted other.html", must("ses_a", "delete", map[string]string{"path": "other.html", "key": ""}), "a session may delete a page another session of its space stored")

	out, failed = call("ses_c", "read", map[string]string{"path": "other.html"})
	assert.True(t, failed, out)
}

func TestOpenCodeMCPTurnSecrets(t *testing.T) {
	t.Parallel()

	s := newOpenCodeTestService(t, "box")
	ctx := t.Context()
	fake := &fakeOpenCode{
		sessions:  []openCodeSession{openCodeTestSession("ses_a", "", t.TempDir(), 1000)},
		parents:   map[string]string{"ses_child": "ses_a"},
		messages:  map[string][]openCodeTestMessage{"ses_a": {{Type: "user", Created: 1}}},
		events:    make(chan string, 4),
		terminals: map[string]*fakeTerminal{},
	}
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	box := config.OpenCodeConfig{URL: upstream.URL, Password: "pw"}
	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

	cfg := config.NewLockedConfig(&config.Config{OpenCode: map[string]config.OpenCodeConfig{"box": box}})
	rt := &Runtime{Cfg: cfg, Sessions: s, threads: &threadBridgeManager{runtime: cfg, store: s}}
	mux := http.NewServeMux()
	mux.Handle("/opencode/{name}/mcp", rt.OpenCodeMCP())
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "opencode", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/opencode/box/mcp", HTTPClient: &http.Client{Transport: basicAuthTransport{"opencode", "pw"}}, DisableStandaloneSSE: true}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, session.Close()) })

	call := func(ctx context.Context, caller, tool string, arguments map[string]string) string {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: arguments, Meta: mcp.Meta{"ai.opencode/sessionID": caller}})
		if err != nil {
			return err.Error()
		}

		return result.Content[0].(*mcp.TextContent).Text
	}
	ask := func(ctx context.Context, name string) <-chan string {
		answer := make(chan string, 1)
		go func() {
			answer <- call(ctx, "ses_a", "ask_secret", map[string]string{"name": name, "reason": "open a pull request"})
		}()

		return answer
	}
	within := func(answer <-chan string) string {
		t.Helper()

		select {
		case out := <-answer:
			return out
		case <-time.After(5 * time.Second):
			require.FailNow(t, "the call never returned")

			return ""
		}
	}
	run := func(command string) string {
		return call(ctx, "ses_a", "run_with_secrets", map[string]string{"command": command})
	}

	conversation := "opencode:box:ses_a"
	pending := func(want int) []SecretRequest {
		t.Helper()

		require.Eventually(t, func() bool { return len(s.PendingSecrets(conversation, alice)) == want }, 5*time.Second, 10*time.Millisecond)

		return s.PendingSecrets(conversation, alice)
	}
	value := "ghp_0123456789abcdef"
	save := func() {
		t.Helper()

		saved := ask(ctx, "GITHUB_TOKEN")

		require.NoError(t, s.AnswerSecret(conversation, pending(1)[0].ID, alice, []byte(value), false))
		assert.Contains(t, within(saved), "Saved: $GITHUB_TOKEN is set in every rocketclaw_run_with_secrets command")
	}
	refused := "run_with_secrets: no secrets held; call ask_secret first"

	brief, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()

	assert.Contains(t, call(brief, "ses_a", "ask_secret", map[string]string{"name": "GITHUB_TOKEN", "reason": "x"}), "nobody sent", "a run not prompted from the RocketClaw Web cannot ask")

	fake.messages["ses_a"] = append(fake.messages["ses_a"], openCodeTestMessage{Type: "user", Username: alice, Created: 2})

	for _, tool := range []string{"ask_secret", "run_with_secrets"} {
		assert.Contains(t, call(brief, "ses_child", tool, map[string]string{"name": "GITHUB_TOKEN", "reason": "x", "command": "true"}), "subagent", "a child session can neither ask nor use secrets")
	}

	assert.Equal(t, refused, run("true"))

	saved := ask(ctx, "GITHUB_TOKEN")

	pending(1)

	for range 2 {
		require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))

		var asking bool
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT asking FROM conversations WHERE conversation_id = $1`, conversation).Scan(&asking))
		assert.True(t, asking, "a pending ask makes the synced row ask, and the next sync keeps it")
	}

	require.NoError(t, s.AnswerSecret(conversation, pending(1)[0].ID, alice, []byte(value), false))
	assert.Contains(t, within(saved), "Saved: $GITHUB_TOKEN is set in every rocketclaw_run_with_secrets command")

	for _, test := range []struct {
		command, want string
		code          int
	}{
		{`echo "$GITHUB_TOKEN"`, "[secret:GITHUB_TOKEN]\n", 0},
		{"true", "", 0},
		{"echo out; exit 3", "out\n", 3},
		{"[ -t 0 ] || [ -t 1 ] || echo notty", "notty\n", 0},
		{`read answer; echo "answer:$answer"; echo "$PAGER/$GIT_PAGER $GIT_TERMINAL_PROMPT $GH_PROMPT_DISABLED $TERM"`, "answer:\ncat/cat 0 1 dumb\n", 0},
	} {
		assert.Equal(t, fmt.Sprintf("Ran with $GITHUB_TOKEN set; exit code %d.\n%s", test.code, test.want), run(test.command), "a value split across frames still comes back as its placeholder")
	}

	assert.Contains(t, run("git init -q && git -c user.name=a -c user.email=a@example.com commit -q --allow-empty -m first && git log"), "\n    first\n", "git log neither pages nor waits")

	fake.mu.Lock()
	assert.Empty(t, fake.terminals, "every terminal is deleted")
	assert.Len(t, fake.ptys, 6)

	for _, created := range fake.ptys {
		var body struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		}
		require.NoError(t, json.Unmarshal([]byte(created), &body))
		assert.Equal(t, map[string]string{"GITHUB_TOKEN": value}, body.Env)
		assert.NotContains(t, body.Command+strings.Join(body.Args, " "), value)
	}

	assert.False(t, slices.ContainsFunc(fake.requests, func(request string) bool { return strings.HasSuffix(request, "/environment") }), "OpenCode's own shell never gets a value")
	fake.mu.Unlock()

	shells := &openCodeShells{running: map[string]string{}}
	go shells.follow(ctx, s, "box", box)

	withdrawn := ask(ctx, "NPM_TOKEN")

	pending(1)

	fake.events <- `{"type":"session.idle","data":{"sessionID":"ses_child"}}`

	fake.events <- `{"type":"shell.created","data":{"info":{"id":"sh_1","status":"running","metadata":{"sessionID":"ses_a"}}}}`

	require.Eventually(t, func() bool {
		shells.mu.Lock()
		defer shells.mu.Unlock()

		return len(shells.running) == 1
	}, 5*time.Second, 10*time.Millisecond)
	assert.Len(t, s.PendingSecrets(conversation, alice), 1, "a subagent going idle does not end the run")

	fake.events <- `{"type":"session.idle","data":{"sessionID":"ses_a"}}`

	assert.Contains(t, within(withdrawn), "No answer", "the run's end withdraws a pending ask")
	assert.Equal(t, refused, run("true"), "and erases its secrets")

	for _, event := range []string{"session.execution.succeeded", "session.execution.failed", "session.execution.interrupted"} {
		save()

		fake.events <- `{"type":"` + event + `","data":{"sessionID":"ses_a"}}`

		require.Eventually(t, func() bool { return run("true") == refused }, 5*time.Second, 10*time.Millisecond, event)
	}

	save()

	late := make(chan string, 1)
	go func() { late <- run(`while [ ! -e go ]; do sleep 0.01; done; echo "$GITHUB_TOKEN"`) }()

	require.Eventually(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()

		return len(fake.terminals) == 1
	}, 5*time.Second, 10*time.Millisecond)

	fake.events <- `{"type":"session.idle","data":{"sessionID":"ses_a"}}`

	require.Eventually(t, func() bool {
		s.secretsMu.Lock()
		defer s.secretsMu.Unlock()

		_, held := s.openCodeSecrets[conversation]

		return !held
	}, 5*time.Second, 10*time.Millisecond)

	folder, err := os.OpenRoot(fake.sessions[0].Location.Directory)
	require.NoError(t, err)
	require.NoError(t, folder.WriteFile("go", nil, 0o600))
	require.NoError(t, folder.Close())
	assert.Equal(t, "Ran with $GITHUB_TOKEN set; exit code 0.\n[secret:GITHUB_TOKEN]\n", within(late), "a run ending during a command still hides the values that command got")

	save()

	withdrawn = ask(ctx, "NPM_TOKEN")

	pending(1)

	fake.sessions[0].Time.Updated = 2000
	fake.messages["ses_a"] = append(fake.messages["ses_a"], openCodeTestMessage{Type: "idle", Created: 3, Outcome: "succeeded"})

	require.NoError(t, s.syncOpenCode(ctx, "box", box, &openCodeShells{}))
	assert.Contains(t, within(withdrawn), "No answer", "a sync that sees the run ended withdraws its ask")
	assert.Equal(t, refused, run("true"), "and erases its secrets")

	save()
	require.NoError(t, s.ChangeAttention(ctx, alice, []string{"/oc/" + base64.RawURLEncoding.EncodeToString([]byte(conversation))}, AttentionSent, nil))
	assert.Equal(t, refused, run("true"), "relaying a new Web prompt starts the run with no secrets")

	canceled, cancel := context.WithCancel(ctx)
	ask(canceled, "NPM_TOKEN")
	pending(1)
	cancel()
	pending(0)
}
