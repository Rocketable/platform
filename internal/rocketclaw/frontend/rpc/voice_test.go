package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/oai"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
)

const voiceSecret = "sk-voice-secret"

// voiceHarness is a real backend behind the Web HTTP bridge, with one fake OpenAI
// server for both the agent's Responses calls and GPT-Live.
type voiceHarness struct {
	rt       *backend.Runtime
	server   *Server
	url      string
	conn     *grpc.ClientConn // the in-process gRPC connection, for callers other than alice
	requests chan string      // Responses request bodies
	replies  chan string      // Responses replies, one per request
	creates  chan voiceCreate
	peers    chan *voicePeer
	hangups  chan string
}

type voiceCreate struct {
	header http.Header
	body   string
}

// voicePeer is the fake OpenAI end of one sideband.
type voicePeer struct {
	id     string
	create string      // the call's create request body
	sent   chan string // client messages; closed when the socket ends
	read   []string    // messages the test has taken from sent

	mu   sync.Mutex
	conn *websocket.Conn
}

func startVoiceHarness(t *testing.T) *voiceHarness {
	t.Helper()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	var logs bytes.Buffer

	previous := slog.Default()

	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previous)
		assert.NotContains(t, logs.String(), voiceSecret)
		assert.NotContains(t, logs.String(), "offer-")
		assert.NotContains(t, logs.String(), "please run")
	})

	h := &voiceHarness{requests: make(chan string, 64), replies: make(chan string, 64), creates: make(chan voiceCreate, 16), peers: make(chan *voicePeer, 16), hangups: make(chan string, 16)}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /{provider}/responses", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)

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
	mux.HandleFunc("POST /v1/live/sessions", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)

		h.creates <- voiceCreate{r.Header.Clone(), string(body)}

		var create struct{ Transport struct{ SDP string } }
		assert.NoError(t, json.Unmarshal(body, &create))

		if create.Transport.SDP == "offer-reject" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "upstream-secret")

			return
		}

		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"session":{"id":"live_`+create.Transport.SDP+`"},"transport":{"sdp":"answer-`+create.Transport.SDP+`"}}`)
	})
	mux.HandleFunc("GET /v1/live/sessions/{id}/attach", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "live_offer-unattached" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}

		peer := &voicePeer{id: r.PathValue("id"), sent: make(chan string, 256), conn: conn}
		h.peers <- peer

		defer close(peer.sent)

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}

			peer.sent <- string(data)

			if string(data) == voiceCloseMessage {
				peer.send(t, `{"type":"session.closed","reason":"client_request"}`)

				_ = conn.Close()
			}
		}
	})
	mux.HandleFunc("POST /v1/live/sessions/{id}/hangup", func(_ http.ResponseWriter, r *http.Request) {
		h.hangups <- r.PathValue("id")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected OpenAI request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})

	openai := httptest.NewServer(mux)
	t.Cleanup(openai.Close)

	cfg := &config.Config{
		DatabaseURL: dsn, Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice", netip.MustParseAddr("192.0.2.2"): "bob"},
		OpenAI:    config.OpenAIConfig{APIKey: voiceSecret, APIBaseURL: openai.URL + "/v1"},
		Providers: map[string]config.OpenAIConfig{"nokey": {APIBaseURL: openai.URL + "/v1"}, "chat": {Auth: "chatgpt"}, "stale": {Auth: "chatgpt"}, "worker": {APIKey: "worker-key", APIBaseURL: openai.URL + "/worker"}},
		Slack:     liveSlack(),
	}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("agents", 0o700))

	for name, model := range map[string]string{"main": "gpt-6.1-sol", "nokey": "nokey/gpt-6.1-sol", "chat": "chat/gpt-6.1-sol", "stale": "stale/gpt-6.1-sol", "worker": "worker/test-model"} {
		require.NoError(t, root.WriteFile(filepath.Join("agents", name+".md"), []byte("---\ndescription: Voice fixture\nmode: primary\nmodel: "+model+"\npermission: {}\n---\nReply plainly.\n"), 0o600))
	}

	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan *backend.Runtime)
	assembly := &mockFrontendAssembler{AssembleFunc: func(rt *backend.Runtime) (<-chan struct{}, []func(context.Context) error, error) {
		ready <- rt
		return rt.RunCtx.Done(), nil, nil
	}, ValidateAssetsFunc: func(*config.Config, string) error { return nil }}

	var running errgroup.Group
	running.Go(func() error {
		return backend.Run(ctx, cfg, "", slog.New(slog.DiscardHandler), new(slog.LevelVar), assembly)
	})
	t.Cleanup(func() { cancel(); require.NoError(t, running.Wait()) })

	h.rt = <-ready

	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	h.server = New(h.rt, h.rt.Sessions, h.rt.Cfg, &mockChannels{}, &mockCronJobs{JobsFunc: func() ([]cronfrontend.Job, error) { return nil, nil }})
	h.server.Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	h.conn = connection
	web := httptest.NewServer(NewHTTPHandler(connection, config.NewLockedConfig(cfg), ""))
	t.Cleanup(web.Close)
	h.url = web.URL

	for _, id := range []string{"main", "nokey", "chat", "stale", "worker"} {
		require.NoError(t, h.rt.CreateConversation(ctx, protocol.Conversation{ID: id, Agent: id, CreatedBy: "alice"}))
	}

	require.NoError(t, h.rt.CreateConversation(ctx, protocol.Conversation{ID: "cron:main", Agent: "main", CreatedBy: "cron", Run: &protocol.CronRun{Path: "cron/main.md", At: time.Unix(1, 0)}}))

	return h
}

// call starts alice's Voice call on conversation id and returns its SSE reader past the answer, a cancel
// that aborts the request, and the call's sideband.
func (h *voiceHarness) call(t *testing.T, id, sdp string) (*bufio.Reader, context.CancelFunc, *voicePeer) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	body, err := json.Marshal(map[string]string{"conversationId": id, "sdp": sdp, "voice": "cedar"})
	require.NoError(t, err)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url+"/api/Voice", bytes.NewReader(body))
	require.NoError(t, err)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	require.Equal(t, http.StatusOK, response.StatusCode)

	reader := bufio.NewReader(response.Body)
	assert.JSONEq(t, `{"answer":{"sdp":"answer-`+sdp+`","route":"public"}}`, strings.TrimPrefix(frame(t, reader), "data: "))

	// Upgrade can reach the client before the fixture publishes its peer.
	create, peer := <-h.creates, <-h.peers
	assert.Equal(t, "Bearer "+voiceSecret, create.header.Get("Authorization"))
	assert.Equal(t, "live_"+sdp, peer.id)

	peer.create = create.body

	return reader, cancel, peer
}

// frame reads one SSE frame.
func frame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()

	var lines []string

	for {
		line, err := reader.ReadString('\n')
		require.NoError(t, err)

		if line == "\n" {
			return strings.Join(lines, "\n")
		}

		lines = append(lines, strings.TrimSuffix(line, "\n"))
	}
}

func (p *voicePeer) send(t *testing.T, events ...string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, event := range events {
		assert.NoError(t, p.conn.WriteMessage(websocket.TextMessage, []byte(event)))
	}
}

// expect returns the next spoken append that contains text.
func (p *voicePeer) expect(t *testing.T, text string) string {
	t.Helper()

	const kind = "session.commentary.append"

	timeout := time.After(20 * time.Second)

	for {
		select {
		case message, ok := <-p.sent:
			require.True(t, ok, "sideband %s closed before %s %q", p.id, kind, text)

			p.read = append(p.read, message)
			if strings.Contains(message, `"type":"`+kind+`"`) && strings.Contains(message, text) {
				return message
			}
		case <-timeout:
			require.FailNow(t, "no sideband message", "%s %q on %s after %q", kind, text, p.id, p.read)
		}
	}
}

// all waits for the socket to close and returns every client message.
func (p *voicePeer) all(t *testing.T) []string {
	t.Helper()

	for message := range p.sent {
		p.read = append(p.read, message)
	}

	return p.read
}

func voiceModel(id, item string) string {
	return `{"id":"` + id + `","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[` + item + `]}`
}

func voiceModelText(id, text string) string {
	quoted, _ := json.Marshal(text)
	return voiceModel(id, `{"id":"msg_`+id+`","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":`+string(quoted)+`,"annotations":[]}]}`)
}

func voiceModelCode(id, code string) string {
	arguments, _ := json.Marshal(map[string]string{"code": code})
	quoted, _ := json.Marshal(string(arguments))

	return voiceModel(id, `{"id":"fc_`+id+`","type":"function_call","status":"completed","call_id":"call_`+id+`","name":"execute","arguments":`+string(quoted)+`}`)
}

// handOff speaks text on the public sideband, hands it off as delegation id, then answers aloud.
func handOff(t *testing.T, peer *voicePeer, id, text string) {
	t.Helper()

	quoted, err := json.Marshal(text)
	require.NoError(t, err)
	peer.send(t,
		fmt.Sprintf(`{"type":"session.input_transcript.delta","delta":%s}`, quoted),
		fmt.Sprintf(`{"type":"session.delegation.created","delegation":{"id":%q}}`, id),
		`{"type":"session.output_transcript.delta","delta":"On it."}`,
	)
}

// spoken is the public append that speaks text under hand-off id.
func spoken(id, text string) string {
	return string((&publicVoice{}).appends(id, text)[0])
}

func (h *voiceHarness) messages(t *testing.T) []*TranscriptEvent {
	t.Helper()

	view, err := h.server.history(metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1")), &HistoryRequest{Id: "main"})
	require.NoError(t, err)

	return view.Messages
}

// ended waits until no call is live.
func (h *voiceHarness) ended(t *testing.T) {
	t.Helper()

	require.Eventually(t, func() bool {
		h.server.voiceMu.Lock()
		defer h.server.voiceMu.Unlock()

		return len(h.server.voiceCalls) == 0
	}, 10*time.Second, 10*time.Millisecond)
}

// prompt steers text into main as the Web composer does; it returns when its turn ends.
func (h *voiceHarness) prompt(group *errgroup.Group, text string) {
	group.Go(func() error {
		response, err := http.Post(h.url+"/api/Prompt", "application/json", strings.NewReader(`{"id":"main","text":"`+text+`","delivery":"STEER"}`))
		if err != nil {
			return fmt.Errorf("post typed prompt: %w", err)
		}

		_ = response.Body.Close()

		return nil
	})
}

// The subtests share one backend and run in order on conversation main.
func TestVoice(t *testing.T) {
	h := startVoiceHarness(t)
	if !t.Run("voice transport is independent of the agent provider", func(t *testing.T) {
		_, cancel, peer := h.call(t, "worker", "offer-worker")
		assert.Contains(t, peer.create, `"instructions":"You are the voice of worker,`)
		handOff(t, peer, "worker-request", "please check the result")
		assert.Contains(t, <-h.requests, `"model":"test-model"`)

		h.replies <- voiceModelText("worker-reply", "The check passed.")

		peer.expect(t, "The check passed.")
		cancel()
		assert.Contains(t, peer.all(t), voiceCloseMessage)
		h.ended(t)

		cfg := h.server.cfg.Clone()

		t.Cleanup(func() { h.server.cfg.Store(cfg) })

		next := *cfg
		next.OpenAI.Auth = "chatgpt"
		h.server.cfg.Store(&next)

		ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
		catalog, err := h.server.listAgents(ctx, "")
		require.NoError(t, err)

		for _, agent := range catalog.Agents {
			assert.Equal(t, newCodexVoice().voices(), agent.Voices, agent.Name)
		}
	}) {
		return
	}
	// The subtests share conversation main: after one fails, the rest would wait on it forever.
	if !t.Run("rejections", func(t *testing.T) {
		// The stale login's token refresh is refused before it leaves the host.
		base, refusing := http.DefaultClient.Transport, &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == "auth.openai.com:443" {
				return nil, errors.New("refresh refused")
			}

			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}}
		http.DefaultClient.Transport = refusing

		t.Cleanup(func() { http.DefaultClient.Transport = base; refusing.CloseIdleConnections() })

		for _, tc := range []struct {
			name, method, body, origin string
			code                       int
			contains                   string
		}{
			{"private conversation", http.MethodPost, `{"conversationId":"cron:main","sdp":"offer-x"}`, "", http.StatusForbidden, `"code":7`},
			{"unknown conversation", http.MethodPost, `{"conversationId":"missing","sdp":"offer-x"}`, "", http.StatusNotFound, `"code":5`},
			{"cross site", http.MethodPost, `{"conversationId":"main","sdp":"offer-x"}`, "https://untrusted.example", http.StatusForbidden, ""},
			{"not routed for GET", http.MethodGet, "", "", http.StatusNotFound, ""},
			{"sdp required", http.MethodPost, `{"conversationId":"main"}`, "", http.StatusBadRequest, "sdp is required"},
			{"conversation required", http.MethodPost, `{"sdp":"offer-x"}`, "", http.StatusBadRequest, "conversationId is required"},
			{"no api key", http.MethodPost, `{"conversationId":"main","sdp":"offer-x"}`, "", http.StatusBadRequest, `"code":9`},
			{"no ChatGPT login", http.MethodPost, `{"conversationId":"main","sdp":"offer-x"}`, "", http.StatusBadRequest, "no usable voice credentials"},
			{"ChatGPT refresh failure", http.MethodPost, `{"conversationId":"main","sdp":"offer-x"}`, "", http.StatusBadRequest, `refresh ChatGPT OAuth token for provider \"openai\"; run ` + "`rocketclaw oai login`"},
			{"voice not offered", http.MethodPost, `{"conversationId":"main","sdp":"offer-x","voice":"cove"}`, "", http.StatusBadRequest, `provider \"openai\" offers no voice \"cove\"`},
			{"upstream rejection", http.MethodPost, `{"conversationId":"main","sdp":"offer-reject"}`, "", http.StatusBadRequest, "openai voice request failed with HTTP 401"},
			// OpenAI created and bills the call, but its sideband is refused: the call is hung up.
			{"sideband refused", http.MethodPost, `{"conversationId":"main","sdp":"offer-unattached"}`, "", http.StatusBadRequest, "openai voice request failed with HTTP 503"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cfg := h.server.cfg.Clone()

				t.Cleanup(func() { h.server.cfg.Store(cfg) })

				next := *cfg

				switch tc.name {
				case "no api key":
					next.OpenAI.APIKey = ""
				case "no ChatGPT login", "ChatGPT refresh failure":
					next.OpenAI.Auth = "chatgpt"

					if tc.name == "ChatGPT refresh failure" {
						require.NoError(t, oai.SaveTokenIn(cfg.Workspace, cfg.RuntimeDirName(), "openai", oai.Token{Refresh: "stale-refresh"}))
						t.Cleanup(func() {
							require.NoError(t, oai.RemoveTokenIn(cfg.Workspace, cfg.RuntimeDirName(), "openai"))
						})
					}
				}

				h.server.cfg.Store(&next)
				request, err := http.NewRequestWithContext(t.Context(), tc.method, h.url+"/api/Voice", strings.NewReader(tc.body))
				require.NoError(t, err)

				if tc.origin != "" {
					request.Header.Set("Origin", tc.origin)
					request.Header.Set("Sec-Fetch-Site", "cross-site")
				}

				response, err := http.DefaultClient.Do(request)
				require.NoError(t, err)

				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())

				assert.Equal(t, tc.code, response.StatusCode, string(body))
				assert.Contains(t, string(body), tc.contains)
				assert.NotContains(t, string(body), "upstream-secret")

				switch tc.name {
				case "upstream rejection":
					assert.Len(t, h.creates, 1)
					<-h.creates
				case "sideband refused":
					assert.Len(t, h.creates, 1)
					<-h.creates
					require.Len(t, h.hangups, 1, "the call is hung up before the browser hears of the failure")
					assert.Equal(t, "live_offer-unattached", <-h.hangups)
				default:
					assert.Empty(t, h.creates, "no call is created")
				}
			})
		}
	}) {
		return
	}

	if !t.Run("call", func(t *testing.T) {
		_, cancel, peer := h.call(t, "main", "offer-1")
		assert.Contains(t, peer.create, `"sdp":"offer-1"`)
		assert.Contains(t, peer.create, `"voice":"cedar"`, "the call speaks with the chosen voice")

		cancel()
		assert.Equal(t, []string{voiceCloseMessage}, peer.all(t))
		h.ended(t)
		assert.Empty(t, h.hangups, "a confirmed close needs no hangup")

		reader, _, peer := h.call(t, "main", "offer-2")
		peer.send(t, `{"type":"session.closed","reason":"expired"}`)
		assert.JSONEq(t, `{"ended":{"reason":"expired"}}`, strings.TrimPrefix(frame(t, reader), "data: "))
		assert.Equal(t, "event: complete\ndata: {}", frame(t, reader))
		assert.Empty(t, peer.all(t), "an expired session gets no close")

		// A dropped public sideband ends the call as lost, without a reconnect, and hangs up (KTD4).
		reader, _, peer = h.call(t, "main", "offer-lost")
		peer.mu.Lock()
		require.NoError(t, peer.conn.Close())
		peer.mu.Unlock()
		assert.JSONEq(t, `{"ended":{"reason":"lost"}}`, strings.TrimPrefix(frame(t, reader), "data: "))
		assert.Equal(t, "live_offer-lost", <-h.hangups)
		assert.Empty(t, peer.all(t))
		assert.Empty(t, h.peers)

		// A newer call replaces the older one while its hand-off turn runs (R20); the turn finishes (R17).
		first, _, older := h.call(t, "main", "offer-3")
		handOff(t, older, "c1", "please run the tests")
		assert.Contains(t, <-h.requests, "please run the tests")

		_, cancel, newer := h.call(t, "main", "offer-4")
		assert.JSONEq(t, `{"ended":{"reason":"replaced"}}`, strings.TrimPrefix(frame(t, first), "data: "))
		assert.Equal(t, "event: complete\ndata: {}", frame(t, first), "the replaced call's stream ends without waiting for its hand-off turn")
		assert.Equal(t, []string{voiceCloseMessage}, older.all(t))

		h.replies <- voiceModelText("r1", "All done.")

		require.Eventually(t, func() bool {
			messages := h.messages(t)
			return messages[len(messages)-1].Text == "All done."
		}, 10*time.Second, 10*time.Millisecond, "the hand-off turn finishes after its call (R17)")
		cancel()
		assert.Equal(t, []string{voiceCloseMessage}, newer.all(t), "a turn this call did not hand off sends nothing (R11)")
	}) {
		return
	}

	if !t.Run("hand-offs", func(t *testing.T) {
		const lint = "Lint passed:\n```\nok\n```"

		_, cancel, peer := h.call(t, "main", "offer-a")
		handOff(t, peer, "d1", "please run the tests")
		assert.Contains(t, <-h.requests, "please run the tests")

		h.replies <- voiceModelCode("r1", "def main():\n    return 'CANARY_TOOL_RESULT'\n")

		assert.Contains(t, <-h.requests, "CANARY_TOOL_RESULT")

		// A second hand-off steers into the running turn; a re-delivered one is skipped.
		handOff(t, peer, "d2", "$workflow deploy")
		peer.send(t, `{"type":"session.delegation.created","delegation":{"id":"d1"}}`)
		require.Eventually(t, func() bool {
			items, err := h.rt.QueueItems("main")
			return err == nil && len(items) == 1 && items[0].ID == "d2"
		}, 10*time.Second, 10*time.Millisecond)

		h.replies <- voiceModelText("r2", "Tests passed.")

		assert.Contains(t, <-h.requests, "$workflow deploy", "the second hand-off joins the running turn as plain text (R8)")

		h.replies <- voiceModelText("r3", lint)

		peer.expect(t, "Lint passed") // Spoken as written, under the first hand-off.

		// A typed turn sends nothing (R11).
		var typed errgroup.Group

		h.prompt(&typed, "typed hello")
		assert.Contains(t, <-h.requests, "typed hello")

		h.replies <- voiceModelText("r4", "Typed reply.")

		require.NoError(t, typed.Wait())

		// Typing into a hand-off's turn supersedes it: the reply is not spoken (KTD5).
		handOff(t, peer, "d3", "check the build")
		assert.Contains(t, <-h.requests, "check the build")
		h.prompt(&typed, "typed steer")
		require.Eventually(t, func() bool {
			items, err := h.rt.QueueItems("main")
			return err == nil && len(items) == 1
		}, 10*time.Second, 10*time.Millisecond)

		h.replies <- voiceModelText("r5", "Build ok.")

		assert.Contains(t, <-h.requests, "typed steer")

		h.replies <- voiceModelText("r6", "Steered reply.")

		require.NoError(t, typed.Wait())

		handOff(t, peer, "d4", "one more")
		assert.Contains(t, <-h.requests, "one more")

		h.replies <- voiceModelText("r7", "Last.")

		peer.expect(t, "Last.")
		cancel()

		// Nothing reaches OpenAI while the agent works: no progress, no tool results (R10, R19).
		assert.Equal(t, []string{spoken("d1", lint), spoken("d4", "Last."), voiceCloseMessage}, peer.all(t))

		var users []*TranscriptEvent

		for _, message := range h.messages(t) {
			if message.Role == "user" && strings.HasPrefix(message.InputId, "d") {
				users = append(users, message)
			}
		}

		require.Len(t, users, 4, "a re-delivered hand-off and talk after the last hand-off add no message")
		assert.Equal(t, "d1 "+voiceDelegation("user: please run the tests", "user: please run the tests"), users[0].InputId+" "+users[0].Text)
		assert.Equal(t, "d2 "+voiceDelegation("assistant: On it.\nuser: $workflow deploy", "assistant: On it.\nuser: $workflow deploy"), users[1].InputId+" "+users[1].Text)
		assert.Contains(t, users[0].Header, `[Web media=Voice principal="alice"`)

		h.ended(t)

		_, _, next := h.call(t, "main", "offer-b")
		assert.Contains(t, next.create, `"Lint passed:\n`+"```"+`\nok\n`+"```"+`"`, "a new call is seeded with the conversation as written (R13, R14)")
		assert.Contains(t, next.create, `user: please run the tests`, "the seed keeps the person's requests")
		assert.NotContains(t, next.create, "CANARY_TOOL_RESULT")
	}) {
		return
	}

	if !t.Run("questions", func(t *testing.T) {
		h.ended(t)

		var typed errgroup.Group

		h.prompt(&typed, "ask me")
		assert.Contains(t, <-h.requests, "ask me")

		h.replies <- voiceModelCode("r1", "def main():\n    return ask_user_question(question='Ship?', details='', options=[{'label': 'Yes', 'value': 'yes', 'description': ''}], multiple=False)\n")

		var pending []protocol.AskUserQuestionRequest

		require.Eventually(t, func() bool {
			var err error

			pending, err = h.rt.PendingQuestions(t.Context(), "main")

			return err == nil && len(pending) == 1
		}, 10*time.Second, 10*time.Millisecond)

		// A question pending before the call gets one session-wide notice (R12).
		_, cancel, peer := h.call(t, "main", "offer-q")
		notice := spoken("", "I need your input. Please respond in the app.\n\nShip?\nOptions: Yes")
		assert.Equal(t, notice, peer.expect(t, "Ship?"))

		// Spoken words never answer it: the next hand-off is a normal message.
		handOff(t, peer, "a1", "yes please")
		require.Eventually(t, func() bool {
			items, err := h.rt.QueueItems("main")
			return err == nil && len(items) == 1 && items[0].ID == "a1"
		}, 10*time.Second, 10*time.Millisecond)

		still, err := h.rt.PendingQuestions(t.Context(), "main")
		require.NoError(t, err)
		assert.Equal(t, pending, still)

		require.NoError(t, h.rt.AnswerQuestion(t.Context(), "main", pending[0].ID, protocol.AskUserQuestionAnswer{Selected: []string{"yes"}}))
		assert.Contains(t, <-h.requests, "yes please")

		h.replies <- voiceModelText("r2", "Shipped.")

		peer.expect(t, "Shipped.")
		require.NoError(t, typed.Wait())
		cancel()
		assert.Equal(t, []string{notice, spoken("a1", "Shipped."), voiceCloseMessage}, peer.all(t))
	}) {
		return
	}

	if !t.Run("reads", func(t *testing.T) {
		h.ended(t)

		ctx, cancel := context.WithCancel(t.Context())
		snapshots := make(chan voiceSnapshot)

		var forwarding errgroup.Group

		forwarding.Go(func() error { return (&voiceCall{server: h.server, conversationID: "main"}).forward(ctx, snapshots) })

		first := <-snapshots

		// While the loop takes no snapshot, as while its sideband is down, more turns
		// finish than one latest-10 read holds.
		for i := range 11 {
			var typed errgroup.Group

			h.prompt(&typed, fmt.Sprintf("turn %d", i))
			assert.Contains(t, <-h.requests, fmt.Sprintf("turn %d", i))

			h.replies <- voiceModelText(fmt.Sprintf("w%d", i), "ok")

			require.NoError(t, typed.Wait())
		}

		var later voiceSnapshot

		require.Eventually(t, func() bool {
			select {
			case later = <-snapshots:
			default:
			}

			return len(later.turns) == len(first.turns)+11
		}, 10*time.Second, 10*time.Millisecond, "a later read keeps every turn since the call's first read")
		assert.Equal(t, first.turns, later.turns[:len(first.turns)])

		cancel()
		require.NoError(t, forwarding.Wait())
	}) {
		return
	}

	if !t.Run("settings", func(t *testing.T) {
		post := func(body string) (int, *VoiceSettingsResponse) {
			t.Helper()

			response, err := http.Post(h.url+"/api/VoiceSettings", "application/json", strings.NewReader(body))
			require.NoError(t, err)

			defer func() { require.NoError(t, response.Body.Close()) }()

			data, err := io.ReadAll(response.Body)
			require.NoError(t, err)

			settings := &VoiceSettingsResponse{}
			if response.StatusCode == http.StatusOK {
				require.NoError(t, protojson.Unmarshal(data, settings))
			}

			return response.StatusCode, settings
		}
		bob := func(request *VoiceSettingsRequest) (*VoiceSettingsResponse, error) {
			return h.server.voiceSettings(metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "192.0.2.2")), request)
		}
		recorded := func(id string) bool {
			t.Helper()

			thread, ok, err := h.rt.Sessions.Thread(id)
			require.NoError(t, err)
			assert.Equal(t, "alice", string(thread.CreatedBy))

			return ok
		}

		code, settings := post(`{}`)
		require.Equal(t, http.StatusOK, code)
		assert.Empty(t, settings.GetConversationId(), "a person with no row has no voice conversation")

		code, settings = post(`{"action":"START","agent":"main"}`)
		require.Equal(t, http.StatusOK, code)

		first := settings.GetConversationId()
		require.NotEmpty(t, first)
		assert.Equal(t, "main", settings.GetAgent())
		assert.True(t, recorded(first))

		_, settings = post(`{"action":"START","agent":"nokey"}`)
		assert.Equal(t, first, settings.GetConversationId(), "another device of the same person gets the current conversation")
		assert.Equal(t, "main", settings.GetAgent())

		_, err := bob(&VoiceSettingsRequest{Action: VoiceSettingsAction_START, Agent: new("ghost")})
		require.ErrorContains(t, err, "select a loaded agent")
		answer, err := bob(&VoiceSettingsRequest{})
		require.NoError(t, err)
		assert.Empty(t, answer.GetConversationId(), "a refused start creates nothing")

		code, settings = post(`{"action":"UPDATE","voice":"cedar","transcript":true}`)
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, []any{first, "main", "cedar", true}, []any{settings.GetConversationId(), settings.GetAgent(), settings.GetVoice(), settings.GetTranscript()})

		code, _ = post(`{"action":"UPDATE","voice":"no-such-voice"}`)
		assert.Equal(t, http.StatusBadRequest, code)
		code, _ = post(`{"action":"UPDATE","agent":"ghost"}`)
		assert.Equal(t, http.StatusBadRequest, code)
		code, _ = post(`{"action":"SHOUT"}`)
		assert.Equal(t, http.StatusBadRequest, code, "an unknown action is refused before the server")

		answer, err = bob(&VoiceSettingsRequest{Action: VoiceSettingsAction_UPDATE, Voice: new("marin")})
		require.NoError(t, err)
		assert.Equal(t, "marin", answer.GetVoice())

		_, settings = post(`{}`)
		assert.Equal(t, []any{"cedar", true}, []any{settings.GetVoice(), settings.GetTranscript()}, "one person's update never changes another's settings")

		// The 2026-10-13 failure: a call that admits no hand-off leaves the voice
		// conversation empty, and startup cleanup deleted it.
		_, err = h.rt.Sessions.PruneStateBefore(t.Context(), time.Now(), 0)
		require.NoError(t, err)
		require.True(t, recorded(first))

		response, err := http.Post(h.url+"/api/Voice", "application/json", strings.NewReader(`{"conversationId":"`+first+`","sdp":"offer-reject"}`))
		require.NoError(t, err)
		data, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		assert.Contains(t, string(data), "openai voice request failed with HTTP 401", "the call reaches OpenAI instead of failing as not recorded")

		h.replies <- voiceModelText("rv", "Ready.")

		response, err = http.Post(h.url+"/api/Prompt", "application/json", strings.NewReader(`{"id":"`+first+`","text":"hello"}`))
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		assert.Equal(t, http.StatusOK, response.StatusCode)
		<-h.requests

		_, settings = post(`{"action":"RESET","conversationId":"stale"}`)
		assert.Equal(t, first, settings.GetConversationId(), "a stale reset leaves the current conversation")

		_, settings = post(`{"action":"RESET","conversationId":"` + first + `"}`)
		assert.Equal(t, []any{"", "main", "cedar", true}, []any{settings.GetConversationId(), settings.GetAgent(), settings.GetVoice(), settings.GetTranscript()})

		_, settings = post(`{"action":"START","agent":"main"}`)
		assert.NotEqual(t, first, settings.GetConversationId(), "after Reset, Start creates a fresh conversation")
		assert.True(t, recorded(first), "Reset keeps the old conversation")
	}) {
		return
	}

	t.Run("one call per person", func(t *testing.T) {
		// An earlier subtest's retention pass removed the fixture chats, so this one makes its own.
		for _, id := range []string{"second", "shared"} {
			require.NoError(t, h.rt.CreateConversation(t.Context(), protocol.Conversation{ID: id, Agent: "main", CreatedBy: "alice"}))
		}

		phone, hangUpPhone, _ := h.call(t, "second", "offer-phone")
		desk, hangUpDesk, alice := h.call(t, "shared", "offer-desk")
		assert.JSONEq(t, `{"ended":{"reason":"replaced"}}`, strings.TrimPrefix(frame(t, phone), "data: "), "a person's new call on another conversation ends their older one")
		hangUpPhone()

		// bob starts his own call on the same conversation; alice's call stays live.
		ctx, hangUpBob := context.WithCancel(t.Context())
		t.Cleanup(hangUpBob)

		stream, err := h.conn.NewStream(metadata.NewOutgoingContext(ctx, metadata.Pairs("rocketclaw-principal", "192.0.2.2")), &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Voice")
		require.NoError(t, err)
		require.NoError(t, stream.SendMsg(&VoiceRequest{ConversationId: "shared", Sdp: "offer-bob"}))
		require.NoError(t, stream.CloseSend())

		answer := &VoiceEvent{}
		require.NoError(t, stream.RecvMsg(answer))
		assert.Equal(t, "answer-offer-bob", answer.GetAnswer().GetSdp())
		<-h.creates
		bob := <-h.peers

		h.server.voiceMu.Lock()
		live := len(h.server.voiceCalls)
		h.server.voiceMu.Unlock()
		assert.Equal(t, 2, live, "two people's calls on one conversation run side by side")

		// Each person hears only replies to their own hand-offs; another person's never mutes them.
		var heard bytes.Buffer

		reading := make(chan struct{})
		go func() { defer close(reading); _, _ = io.Copy(&heard, desk) }()

		for _, turn := range []struct {
			peer             *voicePeer
			id, text, answer string
		}{{alice, "alice-1", "alice asks", "Alpha."}, {bob, "bob-1", "bob asks", "Bravo."}, {alice, "alice-2", "alice again", "Charlie."}} {
			handOff(t, turn.peer, turn.id, turn.text)
			assert.Contains(t, <-h.requests, turn.text)

			h.replies <- voiceModelText("r"+turn.id, turn.answer)

			turn.peer.expect(t, turn.answer)
		}

		hangUpBob()
		hangUpDesk()
		assert.NotContains(t, alice.all(t), "Bravo.")
		<-reading
		assert.NotContains(t, heard.String(), "typed", "another person's hand-off is not typed input")

		h.ended(t)
	})
}
