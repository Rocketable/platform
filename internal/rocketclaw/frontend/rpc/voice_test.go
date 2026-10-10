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
)

const voiceSecret = "sk-voice-secret"

// voiceHarness is a real backend behind the Web HTTP bridge, with one fake OpenAI
// server for both the agent's Responses calls and GPT-Live.
type voiceHarness struct {
	rt       *backend.Runtime
	server   *Server
	url      string
	requests chan string // Responses request bodies
	replies  chan string // Responses replies, one per request
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
	id   string
	sent chan string // client messages; closed when the socket ends
	read []string    // messages the test has taken from sent

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
	mux.HandleFunc("POST /v1/responses", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)

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

			// A quiet peer never confirms the close, so the call must hang up.
			if string(data) == voiceCloseMessage && !strings.Contains(peer.id, "quiet") {
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
		DatabaseURL: dsn, Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"},
		OpenAI:    config.OpenAIConfig{APIKey: voiceSecret, APIBaseURL: openai.URL + "/v1"},
		Providers: map[string]config.OpenAIConfig{"nokey": {APIBaseURL: openai.URL + "/v1"}, "chat": {RocketCodeAuth: "chatgpt"}, "stale": {RocketCodeAuth: "chatgpt"}},
	}
	// An expired login whose refresh TestVoiceRejections makes fail.
	require.NoError(t, oai.SaveTokenIn(cfg.Workspace, cfg.RuntimeDirName(), "stale", oai.Token{Refresh: "stale-refresh"}))

	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("agents", 0o700))

	for name, model := range map[string]string{"main": "gpt-5.5", "nokey": "nokey/gpt-5.5", "chat": "chat/gpt-5.5", "stale": "stale/gpt-5.5"} {
		require.NoError(t, root.WriteFile(filepath.Join("agents", name+".md"), []byte("---\ndescription: Voice fixture\nmode: primary\nmodel: "+model+"\npermission: {}\n---\nReply plainly.\n"), 0o600))
	}

	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan *backend.Runtime)
	assembly := &mockFrontendAssembler{AssembleFunc: func(rt *backend.Runtime) (backend.SlackFrontend, <-chan struct{}, []func(context.Context) error, error) {
		ready <- rt
		return nil, rt.RunCtx.Done(), nil, nil // The public assembler contract permits absent Slack.
	}, ValidateAssetsFunc: func(*config.Config, string, []string) error { return nil }}

	var running errgroup.Group
	running.Go(func() error { return backend.Run(ctx, cfg, "", slog.New(slog.DiscardHandler), assembly) })
	t.Cleanup(func() { cancel(); require.NoError(t, running.Wait()) })

	h.rt = <-ready

	listener, err := Listen(testSocketPath(t))
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

	web := httptest.NewServer(NewHTTPHandler(connection, cfg.Web.Sentry))
	t.Cleanup(web.Close)
	h.url = web.URL

	for _, id := range []string{"main", "nokey", "chat", "stale"} {
		require.NoError(t, h.rt.CreateConversation(ctx, protocol.Conversation{ID: id, Agent: id, CreatedBy: "alice"}))
	}

	return h
}

// call posts a Voice request and returns its SSE reader and a cancel that aborts it.
func (h *voiceHarness) call(t *testing.T, sdp string) (*bufio.Reader, context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	body, err := json.Marshal(map[string]string{"conversationId": "main", "sdp": sdp})
	require.NoError(t, err)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url+"/api/Voice", bytes.NewReader(body))
	require.NoError(t, err)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	require.Equal(t, http.StatusOK, response.StatusCode)

	return bufio.NewReader(response.Body), cancel
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

// expect returns the next client message of type kind that contains text.
func (p *voicePeer) expect(t *testing.T, kind, text string) string {
	t.Helper()

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
	return `{"id":"` + id + `","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[` + item + `]}`
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

// handOff speaks text on the public sideband, then hands it off as delegation id at offset.
func handOff(t *testing.T, peer *voicePeer, id, text string, offset int) {
	t.Helper()

	quoted, err := json.Marshal(text)
	require.NoError(t, err)
	peer.send(t,
		fmt.Sprintf(`{"type":"session.input_transcript.delta","delta":%s,"start_ms":%d,"end_ms":%d}`, quoted, offset-900, offset-100),
		fmt.Sprintf(`{"type":"session.delegation.created","offset_ms":%d,"delegation":{"id":%q}}`, offset, id),
		fmt.Sprintf(`{"type":"session.output_transcript.delta","delta":"On it.","start_ms":%d,"end_ms":%d}`, offset+100, offset+300),
	)
}

func TestVoiceRejections(t *testing.T) {
	h := startVoiceHarness(t)

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
		{"no api key", http.MethodPost, `{"conversationId":"nokey","sdp":"offer-x"}`, "", http.StatusBadRequest, `"code":9`},
		{"no ChatGPT login", http.MethodPost, `{"conversationId":"chat","sdp":"offer-x"}`, "", http.StatusBadRequest, "no usable voice credentials"},
		{"ChatGPT refresh failure", http.MethodPost, `{"conversationId":"stale","sdp":"offer-x"}`, "", http.StatusBadRequest, `refresh ChatGPT OAuth token for provider \"stale\"; run ` + "`rocketclaw oai login stale`"},
		{"upstream rejection", http.MethodPost, `{"conversationId":"main","sdp":"offer-reject"}`, "", http.StatusBadRequest, "openai voice request failed with HTTP 401"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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

			if tc.name != "upstream rejection" {
				assert.Empty(t, h.creates, "no call is created")
			} else {
				assert.Len(t, h.creates, 1)
				<-h.creates
			}
		})
	}
}

func (h *voiceHarness) messages(t *testing.T) []*TranscriptEvent {
	t.Helper()

	view, err := h.server.history(metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1")), &HistoryRequest{Id: "main"})
	require.NoError(t, err)

	return view.Messages
}

// ended waits until no call is live on conversation id.
func (h *voiceHarness) ended(t *testing.T, id string) {
	t.Helper()

	require.Eventually(t, func() bool {
		h.server.voiceMu.Lock()
		defer h.server.voiceMu.Unlock()

		return h.server.voiceCalls[id] == nil
	}, 10*time.Second, 10*time.Millisecond)
}

func TestVoiceCall(t *testing.T) {
	h := startVoiceHarness(t)

	reader, cancel := h.call(t, "offer-1")
	require.Len(t, h.peers, 1, "the sideband attaches before the browser gets the answer")
	assert.JSONEq(t, `{"answer":{"sdp":"answer-offer-1", "route":"public"}}`, strings.TrimPrefix(frame(t, reader), "data: "))

	create := <-h.creates
	assert.Equal(t, "Bearer "+voiceSecret, create.header.Get("Authorization"))
	assert.Contains(t, create.body, `"sdp":"offer-1"`)

	peer := <-h.peers
	assert.Equal(t, "live_offer-1", peer.id)

	cancel()
	peer.expect(t, "session.close", "")
	assert.Equal(t, []string{voiceCloseMessage}, peer.all(t))
	h.ended(t, "main")
	assert.Empty(t, h.hangups, "a confirmed close needs no hangup")

	_, cancel = h.call(t, "offer-quiet")

	peer = <-h.peers

	cancel()
	peer.expect(t, "session.close", "")
	assert.Equal(t, "live_offer-quiet", <-h.hangups, "an unconfirmed close hangs up")
	<-h.creates

	reader, _ = h.call(t, "offer-2")
	frame(t, reader)

	peer = <-h.peers
	peer.send(t, `{"type":"session.closed","reason":"expired"}`)
	assert.JSONEq(t, `{"ended":{"reason":"expired"}}`, strings.TrimPrefix(frame(t, reader), "data: "))
	assert.Equal(t, "event: complete\ndata: {}", frame(t, reader))
	assert.Empty(t, peer.all(t), "an expired session gets no close")
	<-h.creates

	// A newer call replaces the older one while its hand-off turn runs (R20); the turn finishes (R17).
	first, _ := h.call(t, "offer-3")
	frame(t, first)

	older := <-h.peers
	handOff(t, older, "d1", "please run the tests", 1000)
	assert.Contains(t, <-h.requests, "please run the tests")

	second, cancel := h.call(t, "offer-4")
	frame(t, second)

	newer := <-h.peers

	assert.JSONEq(t, `{"ended":{"reason":"replaced"}}`, strings.TrimPrefix(frame(t, first), "data: "))
	assert.Equal(t, []string{voiceCloseMessage}, older.all(t))

	h.replies <- voiceModelText("r1", "All done.")

	newer.expect(t, "session.thinking.append", "All done.")
	assert.Equal(t, "event: complete\ndata: {}", frame(t, first), "the replaced call's stream ends after its hand-off turn")

	cancel()

	for _, message := range newer.all(t) {
		assert.NotContains(t, message, "session.commentary.append", "a turn this call did not start is never spoken")
	}

	assert.Equal(t, "All done.", h.messages(t)[len(h.messages(t))-1].Text)
}

func TestVoiceHandOffs(t *testing.T) {
	h := startVoiceHarness(t)

	reader, cancel := h.call(t, "offer-a")
	frame(t, reader)

	peer := <-h.peers
	handOff(t, peer, "d1", "please run the tests", 1000)
	assert.Contains(t, <-h.requests, "please run the tests")

	h.replies <- voiceModelCode("r1", "def main():\n    return 'CANARY_TOOL_RESULT'\n")

	assert.Contains(t, <-h.requests, "CANARY_TOOL_RESULT")
	peer.expect(t, "session.thinking.append", "execute") // Silent progress names the tool (R10).

	// A second hand-off steers into the running turn, and a re-delivered one is dropped.
	handOff(t, peer, "d2", "$workflow deploy", 5000)
	peer.send(t, `{"type":"session.delegation.created","offset_ms":1000,"delegation":{"id":"d1"}}`)
	require.Eventually(t, func() bool {
		items, err := h.rt.QueueItems("main")
		return err == nil && len(items) == 1 && items[0].ID == "d2"
	}, 10*time.Second, 10*time.Millisecond)

	queued, err := http.Post(h.url+"/api/Prompt", "application/json", strings.NewReader(`{"id":"main","text":"typed hello","delivery":"QUEUE"}`))
	require.NoError(t, err)
	require.NoError(t, queued.Body.Close())
	require.Equal(t, http.StatusOK, queued.StatusCode)

	h.replies <- voiceModelText("r2", "Tests passed.")

	steered := <-h.requests
	assert.Contains(t, steered, "$workflow deploy", "the second hand-off joins the running turn as plain text (R8)")
	assert.NotContains(t, steered, "typed hello")

	for len(peer.sent) > 0 {
		message := <-peer.sent
		peer.read = append(peer.read, message)
		assert.NotContains(t, message, "session.commentary.append", "nothing is spoken before the turn settles")
	}

	h.replies <- voiceModelText("r3", "Lint passed too.")

	assert.Contains(t, <-h.requests, "typed hello")

	spoken := peer.expect(t, "session.commentary.append", "Lint passed too.")
	assert.JSONEq(t, `{"type":"session.commentary.append","delegation_id":"d1","content":"Lint passed too."}`, spoken)

	h.replies <- voiceModelText("r4", "Typed reply.")

	peer.expect(t, "session.thinking.append", "Typed reply.") // A typed turn is silent context (R11).
	cancel()

	sent := strings.Join(peer.all(t), "\n")
	assert.Equal(t, 1, strings.Count(sent, "session.commentary.append"), sent)
	assert.NotContains(t, sent, "CANARY_TOOL_RESULT", "tool results never leave the server (R19)")

	var users []*TranscriptEvent

	for _, message := range h.messages(t) {
		if message.Role == "user" {
			users = append(users, message)
		}
	}

	require.Len(t, users, 3, "a re-delivered hand-off and talk after the last hand-off add no message")
	assert.Equal(t, []string{"d1", "d2"}, []string{users[0].InputId, users[1].InputId})
	assert.Equal(t, "please run the tests\n\nVoice conversation:\n> user: please run the tests", users[0].Text)
	assert.Equal(t, "$workflow deploy\n\nVoice conversation:\n> assistant: On it.\n> user: $workflow deploy", users[1].Text)
	assert.Contains(t, users[0].Header, `[Web media=Voice principal="alice"`)
	assert.Equal(t, "typed hello", users[2].Text)

	h.ended(t, "main")
	h.call(t, "offer-b")

	<-h.creates

	seed := (<-h.creates).body

	assert.Contains(t, seed, "Lint passed too.", "a new call is seeded with the conversation (R13)")
	assert.NotContains(t, seed, "CANARY_TOOL_RESULT")
}

func TestVoiceQuestions(t *testing.T) {
	h := startVoiceHarness(t)
	ask := func(id, question string) string {
		return voiceModelCode(id, "def main():\n    return ask_user_question(question='"+question+"', details='', options=[{'label': 'Yes', 'value': 'yes', 'description': ''}], multiple=False)\n")
	}

	// A question pending before the call is read out first and answered by voice (R12).
	var typed errgroup.Group
	typed.Go(func() error {
		response, err := http.Post(h.url+"/api/Prompt", "application/json", strings.NewReader(`{"id":"main","text":"ask me","delivery":"STEER"}`))
		if err != nil {
			return fmt.Errorf("post typed prompt: %w", err)
		}

		_ = response.Body.Close()

		return nil
	})
	assert.Contains(t, <-h.requests, "ask me")

	h.replies <- ask("r1", "Ship?")

	require.Eventually(t, func() bool {
		pending, err := h.rt.PendingQuestions(t.Context(), "main")
		return err == nil && len(pending) == 1
	}, 10*time.Second, 10*time.Millisecond)

	reader, cancel := h.call(t, "offer-q")
	frame(t, reader)

	peer := <-h.peers
	peer.expect(t, "session.commentary.append", `The agent asks: Ship?\nOptions: Yes`)
	handOff(t, peer, "d1", "yes please", 1000)
	assert.Contains(t, <-h.requests, `yes please`)

	h.replies <- voiceModelText("r2", "Shipped.")

	peer.expect(t, "session.thinking.append", "Shipped.") // The typed turn's reply stays silent.
	require.NoError(t, typed.Wait())

	// A question asked during the call is read out, and the next hand-off answers it.
	handOff(t, peer, "d2", "ship the second one", 4000)
	assert.Contains(t, <-h.requests, "ship the second one")

	h.replies <- ask("r3", "Really?")

	peer.expect(t, "session.commentary.append", "Really?")
	handOff(t, peer, "d3", "yes really", 8000)
	assert.Contains(t, <-h.requests, "yes really")

	h.replies <- voiceModelText("r4", "Shipped twice.")

	peer.expect(t, "session.commentary.append", "Shipped twice.")
	cancel()
	peer.all(t)

	pending, err := h.rt.PendingQuestions(t.Context(), "main")
	require.NoError(t, err)
	assert.Empty(t, pending)

	var inputs []string

	for _, message := range h.messages(t) {
		if message.Role == "user" {
			inputs = append(inputs, message.InputId)
		}
	}

	require.Len(t, inputs, 2, "answers are not new messages")
	assert.Equal(t, "d2", inputs[1])
}
