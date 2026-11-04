package rpc

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestSpacePermissionsRealBrowser(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []string
	)

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer func() { _ = conn.Close() }()

		for {
			_, request, err := conn.ReadMessage()
			if err != nil {
				return
			}

			mu.Lock()

			requests = append(requests, string(request))
			mu.Unlock()

			for _, event := range []string{`{"type":"response.created","response":{"id":"resp"}}`, `{"type":"response.completed","response":{"id":"resp","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ops-agent here: Thursday's deploy checklist is ready.","annotations":[]}]}]}}`} {
				if conn.WriteMessage(websocket.TextMessage, []byte(event)) != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(provider.Close)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), Permissions: map[string][]string{"alice": {"Ops"}},
		OpenAI: config.OpenAIConfig{APIKey: "permissions-test", APIBaseURL: strings.Replace(provider.URL, "http://", "ws://", 1)}}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("agents", 0o700))

	for name, front := range map[string]string{
		"ops-agent":  "description: Ops team agent for on-call and deploys (space Ops)\nspace: Ops\n",
		"main-agent": "description: Main team agent for budgets and planning (space Main)\nspace: Main\n",
		"loose":      "description: Agent with no space, hidden from people limited to spaces\n",
	} {
		require.NoError(t, root.WriteFile("agents/"+name+".md", []byte("---\n"+front+"model: gpt-6.1-sol\npermission: {}\n---\nYou are "+name+". Reply plainly.\n"), 0o600))
	}

	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan *backend.Runtime)
	assembly := &mockFrontendAssembler{AssembleFunc: func(rt *backend.Runtime) (<-chan struct{}, []func(context.Context) error, error) {
		ready <- rt
		return rt.RunCtx.Done(), nil, nil
	}, ValidateAssetsFunc: func(*config.Config, string) error { return nil }}

	var running errgroup.Group
	running.Go(func() error { return backend.Run(ctx, cfg, "", slog.New(slog.DiscardHandler), assembly) })
	t.Cleanup(func() { cancel(); require.NoError(t, running.Wait()) })

	rt := <-ready

	type message struct {
		Type         string `json:"type"`
		Role         string `json:"role"`
		PromptHeader string `json:"prompt_header,omitempty"`
		Content      string `json:"content"`
	}

	say := func(id, agent, who, ask, answer string) {
		header := `[Web principal="` + who + `"]`
		asked, err := json.Marshal(message{Type: "message", Role: "user", PromptHeader: header, Content: header + "\n\n" + ask})
		require.NoError(t, err)
		answered, err := json.Marshal(message{Type: "message", Role: "assistant", Content: answer})
		require.NoError(t, err)
		_, err = rt.Sessions.AppendEntryID(ctx, id, &harness.SessionEntry{Type: "turn", Timestamp: time.Now().UTC(), Agent: agent, Model: "gpt-6.1-sol", ReplayInput: []json.RawMessage{asked, answered}})
		require.NoError(t, err)
	}

	for _, chat := range []struct{ id, agent, space, name, who, ask, answer string }{
		{"main-born", "main-agent", "Main", "Main: quarterly budget", "bob", "Summarize the Q4 budget.", "Q4 spend is 8% under plan."},
		{"ops-born", "ops-agent", "Ops", "Ops: on-call handoff", "alice", "Who is on call for Ops this week?", "Dana is on call until Friday."},
		{"switched", "ops-agent", "Ops", "Ops: deploy checklist", "alice", "Draft Thursday's deploy checklist.", "Drafted: freeze, canary, rollback plan."},
		{"loose", "loose", "", "No space: scratch notes", "bob", "Keep these scratch notes.", "Saved."},
	} {
		require.NoError(t, rt.CreateConversation(ctx, protocol.Conversation{ID: chat.id, Agent: chat.agent, CreatedBy: chat.who, Space: chat.space}))
		say(chat.id, chat.agent, chat.who, chat.ask, chat.answer)
		_, err := rt.Sessions.UpdateConversationDetails(ctx, chat.id, nil, &chat.name)
		require.NoError(t, err)
	}

	_, err = rt.SwitchConversationAgent("switched", "main-agent")
	require.NoError(t, err)
	say("switched", "main-agent", "bob", "Main team here: add the budget sign-off step.", "main-agent added the budget sign-off step.")

	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	New(rt, rt.Sessions, rt.Cfg, &mockChannels{SlackTagsMatchingFunc: func(context.Context, string) []string { return nil }}, &mockCronJobs{JobsFunc: func() ([]cronfrontend.Job, error) { return nil, nil }}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	handler := NewHTTPHandler(connection, cfg.Web.Sentry, cfg.PostHog, cfg.OpenCode, "", nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/test/unlimit" {
			handler.ServeHTTP(w, r)
			return
		}

		unlimited := rt.Cfg.Clone()
		unlimited.Permissions = nil
		rt.Cfg.Store(unlimited)
	}))
	t.Cleanup(server.Close)

	// The screenshots are for people, so they go to the workspace rather than a test folder.
	out, err := filepath.Abs("../../../../.tmp/permissions-demo")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(out))
	require.NoError(t, os.MkdirAll(out, 0o750))
	runWebTest(t, "src/permissions-transport.test.ts", "ROCKETCLAW_PERMISSIONS_TEST_URL="+server.URL, "ROCKETCLAW_PERMISSIONS_DEMO_DIR="+out)

	thread, ok, err := rt.Sessions.Thread("switched")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ops-agent", thread.Agent, "alice's send switched the chat back to Ops' agent")

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, requests, 1, "only alice's send reached the model")
	require.Contains(t, requests[0], "You are ops-agent.")
	require.NotContains(t, requests[0], "You are main-agent.")
}
