package backend

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

	assert.ElementsMatch(t, []string{"store", "read", "kv_store", "kv_read", "delete", "list", "get_tags", "toggle_tag", "navigate_for_me"}, names)

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
