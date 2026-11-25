package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	routinefrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/routine"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type webHarness struct {
	rt       *backend.Runtime
	server   *Server
	url      string
	conn     *grpc.ClientConn
	requests chan string
	replies  chan string
}

func startWebHarness(t *testing.T) *webHarness {
	t.Helper()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	h := &webHarness{requests: make(chan string, 64), replies: make(chan string, 64)}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /{provider}/responses", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)

		if titleRequest(body) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, titleResponse)

			return
		}

		var request struct{ Model string }
		assert.NoError(t, json.Unmarshal(body, &request))

		if request.Model == "test-model" {
			assert.Equal(t, "/worker/responses", r.URL.Path)
			assert.Equal(t, "Bearer worker-key", r.Header.Get("Authorization"))
		}

		h.requests <- string(body)

		select {
		case reply := <-h.replies:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, reply)
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected OpenAI request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})

	openai := httptest.NewServer(mux)
	t.Cleanup(openai.Close)

	cfg := &config.Config{
		DatabaseURL: dsn, Workspace: t.TempDir(),
		OpenAI:    config.OpenAIConfig{APIKey: "test-key", APIBaseURL: openai.URL + "/v1"},
		Providers: map[string]config.OpenAIConfig{"nokey": {APIBaseURL: openai.URL + "/v1"}, "worker": {APIKey: "worker-key", APIBaseURL: openai.URL + "/worker"}},
		Slack:     config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "@", Agents: []string{"worker"}, AllowedUserIDs: []string{"U1"}}}},
	}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("agents", 0o700))

	for name, model := range map[string]string{"primary": "gpt-6.1-sol\nsticky: user", "nokey": "nokey/gpt-6.1-sol", "worker": "worker/test-model"} {
		require.NoError(t, root.WriteFile(filepath.Join("agents", name+".md"), []byte("---\ndescription: Web fixture\nmodel: "+model+"\npermission: {}\n---\nReply plainly.\n"), 0o600))
	}

	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan *backend.Runtime)
	assembly := &mockFrontendAssembler{GateFunc: passGate, AssembleFunc: func(rt *backend.Runtime) (<-chan struct{}, []func(context.Context) error, error) {
		ready <- rt
		return rt.RunCtx.Done(), nil, nil
	}, ValidateAssetsFunc: func(*config.Config, string) error { return nil }}

	var running errgroup.Group
	running.Go(func() error {
		return backend.Run(ctx, cfg, slog.New(slog.DiscardHandler), new(slog.LevelVar), assembly, sandbox.None{})
	})
	t.Cleanup(func() { cancel(); require.NoError(t, running.Wait()) })

	h.rt = <-ready

	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	h.server = New(h.rt, h.rt.Sessions, h.rt.Cfg, &mockChannels{}, &mockRoutines{JobsFunc: func() ([]routinefrontend.Job, error) { return nil, nil }})
	h.server.Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	h.conn = connection
	web := httptest.NewServer(signedIn("alice", NewHTTPHandler(connection, config.NewLockedConfig(cfg), "")))
	t.Cleanup(web.Close)
	h.url = web.URL

	for _, id := range []string{"primary", "nokey", "worker"} {
		require.NoError(t, h.rt.CreateConversation(ctx, protocol.Conversation{ID: id, Agent: id, CreatedBy: "alice"}))
	}

	return h
}

func modelReply(id, item string) string {
	return `{"id":"` + id + `","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[` + item + `]}`
}

func modelReplyText(id, text string) string {
	quoted, _ := json.Marshal(text)
	return modelReply(id, `{"id":"msg_`+id+`","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":`+string(quoted)+`,"annotations":[]}]}`)
}

func TestPausedChat(t *testing.T) {
	h := startWebHarness(t)

	const id = "worker"

	alice := metadata.NewIncomingContext(t.Context(), tailnetUser("alice"))
	prompt := func(messageID, text string, delivery PromptDelivery) (*PromptResponse, error) {
		return h.server.prompt(alice, &PromptRequest{Id: id, Text: text, Delivery: delivery, MessageId: messageID})
	}
	pause := func(paused bool) {
		t.Helper()

		_, err := h.server.updateSession(alice, &UpdateSessionRequest{Id: id, Paused: &paused})
		require.NoError(t, err)
	}
	listed := func() (*Session, backend.Attention) {
		t.Helper()

		for chat, err := range h.rt.Sessions.SidebarChats(t.Context(), "alice", h.server.channels, []string{id}) {
			require.NoError(t, err)
			return sidebarSession(&chat), chat.Session.Attention
		}

		return nil, backend.Attention{}
	}
	queue := func() []string {
		t.Helper()

		items, err := h.rt.QueueItems(id)
		require.NoError(t, err)

		waiting := make([]string, len(items))
		for i := range items {
			waiting[i] = items[i].ID + ":" + string(items[i].Kind)
		}

		return waiting
	}
	idle := func() {
		t.Helper()
		require.Eventually(t, func() bool {
			active, err := h.rt.Sessions.HasActiveTurn(t.Context(), id)
			return err == nil && !active
		}, 10*time.Second, 10*time.Millisecond)
	}

	require.NoError(t, h.rt.CreateConversation(t.Context(), protocol.Conversation{ID: "report", Agent: id, CreatedBy: "routine"}))

	for _, chat := range []string{id, "report"} {
		_, err := h.rt.Sessions.AppendEntryID(t.Context(), chat, &harness.SessionEntry{Timestamp: time.Now().UTC()})
		require.NoError(t, err)
	}

	_, err := h.server.updateSession(alice, &UpdateSessionRequest{Id: "report", Paused: new(true)})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)

	pause(true)

	session, _ := listed()
	require.True(t, session.GetPaused(), "the chat lists as paused")
	require.True(t, session.GetPausable())

	_, err = prompt("m1", "lunch at noon?", PromptDelivery_STEER)
	require.NoError(t, err)
	_, err = prompt("m2", "/steer noon works for me", PromptDelivery_STEER)
	require.NoError(t, err)

	view, err := h.server.history(alice, &HistoryRequest{Id: id})
	require.NoError(t, err)

	var posted []string

	for _, message := range view.Messages {
		if message.Role == "user" {
			posted = append(posted, message.InputId+" "+message.Principal+": "+message.Text)
		}
	}

	assert.Equal(t, []string{"m1 alice: lunch at noon?", "m2 alice: noon works for me"}, posted, "each message posts under its sender")

	_, attention := listed()
	assert.False(t, attention.Parked, "a message posted while the agent is paused parks nothing")
	assert.Empty(t, h.requests, "and starts no turn")
	idle()

	for _, send := range []struct {
		id, text string
		delivery PromptDelivery
	}{{"q1", "queued item", PromptDelivery_QUEUE}, {"q2", "/enqueue enqueued item", PromptDelivery_STEER}, {"s1", "stashed item", PromptDelivery_STASH}} {
		_, err := prompt(send.id, send.text, send.delivery)
		require.NoError(t, err, send.text)
	}

	waiting := queue()
	assert.ElementsMatch(t, []string{"q1:enqueue", "q2:enqueue", "s1:held"}, waiting)

	_, errGoal := prompt("refused", "/goal ship it", PromptDelivery_STEER)
	_, goalRefused := errors.AsType[backend.AgentPausedError](errGoal)
	assert.True(t, goalRefused, "%v", errGoal)

	_, found, err := h.rt.Sessions.Goal(id)
	require.NoError(t, err)
	assert.False(t, found, "no goal starts")

	_, err = h.server.queueItem(alice, "SteerQueueItem", &QueueItemRequest{Id: id, ItemId: "q2"})
	_, refused := errors.AsType[backend.AgentPausedError](err)
	assert.True(t, refused, "%v", err)
	assert.Equal(t, waiting, queue(), "a refused promote leaves the item in its place")

	for _, agent := range []string{"nokey", id} {
		switched, err := prompt("agent", "/agent "+agent, PromptDelivery_STEER)
		require.NoError(t, err)
		assert.Equal(t, "Agent: "+agent, switched.PrivateText)
	}

	pause(false)

	request := <-h.requests
	assert.Contains(t, request, "noon works for me", "the agent reads the paused chat")
	assert.Contains(t, request, "queued item")

	h.replies <- modelReplyText("r1", "Queued done.")

	assert.Contains(t, <-h.requests, "enqueued item")

	h.replies <- modelReplyText("r2", "Enqueued done.")

	idle()

	var turn errgroup.Group

	turn.Go(func() error {
		_, err := prompt("w1", "work", PromptDelivery_STEER)
		return err
	})
	assert.Contains(t, <-h.requests, "work")
	pause(true)

	_, err = prompt("stop", "/stop", PromptDelivery_STEER)
	require.NoError(t, err)
	require.NoError(t, turn.Wait())
	idle()
	assert.Empty(t, h.requests, "the stopped reply in the paused chat is followed by nothing")
}
