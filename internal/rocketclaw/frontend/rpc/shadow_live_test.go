package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestShadowsRealBackendBrowser(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	type providerRequest struct {
		Model string            `json:"model"`
		Input []json.RawMessage `json:"input"`
	}

	requests := make(chan providerRequest, 6)
	releaseA, releaseB := make(chan struct{}), make(chan struct{})
	unlockA := sync.OnceFunc(func() { close(releaseA) })
	unlockB := sync.OnceFunc(func() { close(releaseB) })

	t.Cleanup(unlockA)
	t.Cleanup(unlockB)

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer func() { _ = conn.Close() }()

		_, raw, err := conn.ReadMessage()
		if !assert.NoError(t, err) {
			return
		}

		var request providerRequest
		if !assert.NoError(t, json.Unmarshal(raw, &request)) {
			return
		}

		requests <- request

		switch request.Model {
		case "gpt-shadow-a":
			<-releaseA
		case "gpt-shadow-b":
			<-releaseB
		}

		turn := 1
		if len(request.Input) > 1 {
			turn = 2
		}

		text := fmt.Sprintf("%s answer %d", request.Model, turn)
		if request.Model == "gpt-parent" && turn == 1 || request.Model == "gpt-shadow-a" && turn == 2 {
			text += strings.Repeat("\n\nAn unequal output paragraph.", 9)
		}

		encoded, err := json.Marshal(text)
		if !assert.NoError(t, err) {
			return
		}

		assert.NoError(t, conn.WriteMessage(websocket.TextMessage, fmt.Appendf(nil, `{"type":"response.completed","response":{"id":"resp","object":"response","created_at":0,"status":"completed","model":%q,"output":[{"id":"reply","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":%s,"annotations":[]}]}]}}`, request.Model, encoded)))
	}))
	t.Cleanup(provider.Close)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}, OpenAI: config.OpenAIConfig{APIKey: "owned-test", APIBaseURL: strings.Replace(provider.URL, "http://", "ws://", 1)}}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	for name, model := range map[string]string{"main": "gpt-parent", "main/a": "gpt-shadow-a", "main/b": "gpt-shadow-b"} {
		file := filepath.Join("agents", name+".md")
		require.NoError(t, root.MkdirAll(filepath.Dir(file), 0o700))
		require.NoError(t, root.WriteFile(file, fmt.Appendf(nil, "---\ndescription: Live shadow fixture\nmode: primary\nmodel: %s\npermission: {}\n---\nReply as %s.\n", model, name), 0o600))
	}

	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan *backend.Runtime)
	assembly := &mockFrontendAssembler{AssembleFunc: func(rt *backend.Runtime) (backend.SlackFrontend, <-chan struct{}, []func(context.Context) error, error) {
		ready <- rt
		return nil, rt.RunCtx.Done(), nil, nil // Public assembler permits absent Slack.
	}, ValidateAssetsFunc: func(*config.Config, string, []string) error { return nil }}

	var running errgroup.Group
	running.Go(func() error { return backend.Run(ctx, cfg, "", slog.New(slog.DiscardHandler), assembly) })
	t.Cleanup(func() {
		unlockA()
		unlockB()
		cancel()
		require.NoError(t, running.Wait())
	})

	rt := <-ready
	require.NoError(t, rt.CreateConversation(ctx, protocol.Conversation{ID: "parent", Agent: "main", CreatedBy: "alice"}))
	events := rt.Subscribe(ctx)

	var (
		outbound  []*protocol.OutboundMessage
		observing errgroup.Group
	)
	observing.Go(func() error {
		for event := range events {
			outbound = append(outbound, event.Message)
			event.Acknowledgement <- nil
		}

		return nil
	})
	t.Cleanup(func() { cancel(); require.NoError(t, observing.Wait()) })
	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	New(rt, rt.Sessions, rt.Cfg, &mockChannels{}, &mockCronJobs{JobsFunc: func() ([]cronfrontend.Job, error) { return nil, nil }}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	handler := NewHTTPHandler(connection, cfg.Web.Sentry, cfg.OpenCode)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/test/release/a":
			unlockA()
		case "/test/release/b":
			unlockB()
		default:
			handler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(httpServer.Close)
	runWebTest(t, "src/shadows-transport.test.ts", "ROCKETCLAW_SHADOW_TEST_URL="+httpServer.URL)
	// Compare the actual provider inputs, not browser History fixtures. Each model
	// keeps its own first answer while consuming the same next human input.
	byModel := make(map[string][]providerRequest)

	for range 6 {
		request := <-requests
		byModel[request.Model] = append(byModel[request.Model], request)
	}

	require.Len(t, byModel, 3)

	for _, model := range []string{"gpt-parent", "gpt-shadow-a", "gpt-shadow-b"} {
		require.Len(t, byModel[model], 2)

		for turn, request := range byModel[model] {
			parent := byModel["gpt-parent"][turn]
			require.JSONEq(t, string(parent.Input[len(parent.Input)-1]), string(request.Input[len(request.Input)-1]), "consumed input in turn %d for %s", turn+1, model)
		}

		require.Contains(t, string(byModel[model][1].Input[1]), model+" answer 1", "independent history")
	}

	active, err := rt.Sessions.HasActiveTurn(ctx, "parent")
	require.NoError(t, err)
	require.False(t, active)
	cancel()
	require.NoError(t, observing.Wait())

	finals := 0

	for _, message := range outbound {
		require.Equal(t, "parent", message.ConversationID, "no child output reaches the live delivery bus")

		if message.Complete {
			finals++
		}
	}

	require.Equal(t, 2, finals)
}
