package webui

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/voice"
)

// Start launches the test browser-facing web UI listener in the default work dir.
func Start(
	ctx context.Context,
	logger *slog.Logger,
	workspace, listenAddr, certFile, keyFile string,
	transcriber transcriber,
	tts synthesizer,
	publisher *voice.TranscriptionPublisher,
) (*Server, error) {
	return StartIn(ctx, logger, workspace, config.DefaultWorkDir, listenAddr, certFile, keyFile, transcriber, tts, publisher)
}

func prepareTLSAssets(workspace, listenAddr, certFile, keyFile string, collectIPv4Addrs func() ([]net.IP, error)) (tlsAssets, error) {
	return prepareTLSAssetsIn(workspace, config.DefaultWorkDir, listenAddr, certFile, keyFile, collectIPv4Addrs)
}

func TestStartServesVoiceModePage(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", nil, nil, nil)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	resp, err := httpsClient(t, server).Get(server.URL())
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Contains(t, string(body), `id="muteButton"`)
	assert.NotContains(t, string(body), `id="connectButton"`)
	assert.NotContains(t, string(body), `connect-toggle`)
	assert.Contains(t, string(body), `id="responseText"`)
	assert.Contains(t, string(body), `navigator.mediaDevices.getUserMedia`)
	assert.Contains(t, string(body), `new WebSocket`)
	assert.Contains(t, string(body), `MediaRecorder`)
	assert.Contains(t, string(body), `audio/webm;codecs=opus`)
	assert.Contains(t, string(body), `connectSession();`)
	assert.Contains(t, string(body), `reloadWhenAPIBack();`)
	assert.Contains(t, string(body), `if (await pingKeepalive("reload-probe"))`)
	assert.Contains(t, string(body), `window.setTimeout(resolve, RELOAD_PROBE_MS)`)
	assert.Contains(t, string(body), `window.location.reload();`)
	assert.NotContains(t, string(body), `scheduleReconnect`)
	assert.NotContains(t, string(body), `connectButton.addEventListener`)
	assert.NotContains(t, string(body), `audio/ogg;codecs=opus`)
	assert.Contains(t, string(body), `setResponseText(text);`)
	assert.Contains(t, string(body), `playbackQueue: []`)
	assert.Contains(t, string(body), `function playNextServerAudio()`)
	assert.Contains(t, string(body), `queueServerAudio(payload.playback_url, payload.mime_type, payload.text);`)
	assert.Contains(t, string(body), `responseText.textContent = next;`)
	assert.Contains(t, string(body), `responseText.hidden = next === "";`)
	assert.NotContains(t, string(body), `setResponseText("")`)
	assert.NotContains(t, string(body), `responseText.textContent = ""`)
	assert.NotContains(t, string(body), `>Transcript<`)
	assert.NotContains(t, string(body), `>History<`)
}

func TestRootRedirectsToVoiceModePage(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", nil, nil, nil)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	client := httpsClient(t, server)
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := client.Get(httpBaseURL(server.URL()) + "/")
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode)
	assert.Equal(t, VoiceModePath, resp.Header.Get("Location"))
}

func TestRootRejectsUnknownPath(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", nil, nil, nil)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	resp, err := httpsClient(t, server).Get(httpBaseURL(server.URL()) + "/missing")
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestStartRejectsInvalidListenPort(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:bad", "", "", nil, nil, nil)
	require.ErrorContains(t, err, "listen for web UI HTTPS server")
	assert.Nil(t, server)
}

func TestKeepaliveEndpointReturnsJSON(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", nil, nil, nil)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, httpBaseURL(server.URL())+KeepalivePath, http.NoBody)
	require.NoError(t, err)
	req.Header.Set("X-Rocketclaw-Reason", "keepalive")

	resp, err := httpsClient(t, server).Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	var payload struct {
		OK        bool   `json:"ok"`
		Reason    string `json:"reason"`
		Timestamp string `json:"timestamp"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, payload.OK)
	assert.Equal(t, "keepalive", payload.Reason)
	assert.NotEmpty(t, payload.Timestamp)
}

func TestWebUIEndpointsRejectUnsupportedMethods(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", nil, nil, nil)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	client := httpsClient(t, server)
	for _, tt := range []struct {
		path  string
		allow string
	}{
		{path: VoiceModePath, allow: http.MethodGet + ", " + http.MethodHead},
		{path: KeepalivePath, allow: http.MethodGet + ", " + http.MethodHead},
		{path: voiceSocketPath, allow: http.MethodGet},
		{path: playbackPathPrefix + "missing", allow: http.MethodGet},
	} {
		t.Run(tt.path, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, httpBaseURL(server.URL())+tt.path, http.NoBody)
			require.NoError(t, err)

			resp, err := client.Do(req)
			require.NoError(t, err)

			defer func() { require.NoError(t, resp.Body.Close()) }()

			assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
			assert.Equal(t, tt.allow, resp.Header.Get("Allow"))
		})
	}
}

func TestVoiceHubHandleWebsocketClosesWhenHubClosed(t *testing.T) {
	hub := &voiceHub{
		log:       slog.New(slog.DiscardHandler),
		upgrader:  websocket.Upgrader{CheckOrigin: allowWebsocketOrigin},
		sessions:  map[string]*voiceSession{},
		playbacks: map[string]playbackAsset{},
		assetIDs:  map[string][]string{},
		closed:    true,
	}

	server := httptest.NewServer(http.HandlerFunc(hub.handleWebsocket))
	defer server.Close()

	headers := make(http.Header)
	headers.Set("Origin", server.URL)

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), headers)
	if resp != nil && resp.Body != nil {
		require.NoError(t, resp.Body.Close())
	}

	require.NoError(t, err)

	defer func() { require.NoError(t, conn.Close()) }()

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, _, err = conn.ReadMessage()
	require.Error(t, err)

	hub.mu.Lock()
	assert.Empty(t, hub.sessions)
	hub.mu.Unlock()
}

func TestVoiceSessionHandleClientMessageMute(t *testing.T) {
	session := &voiceSession{
		hub:  &voiceHub{log: slog.New(slog.DiscardHandler)},
		send: make(chan *serverMessage, 2),
	}

	require.NoError(t, session.handleClientMessage(&clientMessage{Type: "mute", Muted: true}))
	assert.True(t, session.muted)
	assert.Equal(t, stateServerMessage("muted", "Microphone muted."), readQueuedServerMessage(t, session.send))

	require.NoError(t, session.handleClientMessage(&clientMessage{Type: "mute", Muted: false}))
	assert.False(t, session.muted)
	assert.Equal(t, stateServerMessage("listening", "Listening."), readQueuedServerMessage(t, session.send))

	err := session.handleClientMessage(&clientMessage{Type: "bogus"})
	require.ErrorContains(t, err, `unsupported browser voice message type "bogus"`)
}

func TestVoiceSessionHandleHelloEdges(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	session := &voiceSession{
		hub: &voiceHub{
			log:         slog.New(slog.DiscardHandler),
			transcriber: new(fakeTranscriber),
			publisher:   voice.NewTranscriptionPublisher(bus, slog.New(slog.DiscardHandler), events.SourceWebVoice, nil, inertBeforeMainSession),
		},
		send: make(chan *serverMessage, 1),
	}

	err := session.handleHello(&clientMessage{Transport: "old", MIMEType: browserCaptureMIME})
	require.ErrorContains(t, err, `unsupported browser voice transport "old"`)

	require.NoError(t, session.handleHello(helloClientMessage()))
	assert.Equal(t, browserCaptureMIME, session.mimeType)
	assert.Equal(t, stateServerMessage("listening", "Connected and listening."), readQueuedServerMessage(t, session.send))
}

func TestVoiceSessionEnqueueRejectsClosedAndFull(t *testing.T) {
	session := &voiceSession{
		id:   "session-1",
		hub:  &voiceHub{log: slog.New(slog.DiscardHandler)},
		send: make(chan *serverMessage, 1),
	}

	session.send <- &serverMessage{Type: "state"}

	assert.False(t, session.enqueue(&serverMessage{Type: "state"}))
	<-session.send

	session.closed = true
	assert.False(t, session.enqueue(&serverMessage{Type: "state"}))
}

func TestVoiceSessionHandleAudioEdges(t *testing.T) {
	session := &voiceSession{
		id: "session-1",
		hub: &voiceHub{
			log:           slog.New(slog.DiscardHandler),
			prerollChunks: 2,
			silenceWindow: time.Second,
		},
		send:     make(chan *serverMessage, 1),
		turnText: map[string]string{},
	}

	err := session.handleAudio(&clientMessage{Sequence: 1, Data: base64.StdEncoding.EncodeToString([]byte("chunk"))})
	require.ErrorContains(t, err, "browser voice session is not initialized")

	session.mimeType = "audio/ogg;codecs=opus"
	err = session.handleAudio(&clientMessage{Sequence: 1, Data: "not base64"})
	require.ErrorContains(t, err, "decode browser voice chunk")

	session.muted = true
	require.NoError(t, session.handleAudio(&clientMessage{Sequence: 2, Data: base64.StdEncoding.EncodeToString([]byte("muted"))}))
	assert.Zero(t, session.lastSequence)

	session.muted = false
	session.lastSequence = 4
	require.NoError(t, session.handleAudio(&clientMessage{Sequence: 4, Data: base64.StdEncoding.EncodeToString([]byte("old"))}))
	assert.Nil(t, session.current)

	session.mimeType = browserCaptureMIME
	session.lastSequence = 0
	headerOnly := []byte("webm-header")
	require.NoError(t, session.handleAudio(&clientMessage{Sequence: 1, Data: base64.StdEncoding.EncodeToString(headerOnly)}))
	assert.Equal(t, headerOnly, session.webmHeader)
	assert.Zero(t, session.lastSequence)
	assert.Nil(t, session.current)

	session.mimeType = "audio/ogg;codecs=opus"
	session.lastSequence = 4
	require.NoError(t, session.handleAudio(&clientMessage{Sequence: 5, Speaking: true, Data: base64.StdEncoding.EncodeToString([]byte("voice"))}))
	require.NotNil(t, session.current)
	require.Len(t, session.current.Chunks, 1)
	assert.Equal(t, uint64(5), session.lastSequence)
	assert.Equal(t, []byte("voice"), session.current.Chunks[0].Data)
}

func TestVoiceSessionBufferingFinalizesAfterSilence(t *testing.T) {
	base := time.Unix(0, 0)
	session := &voiceSession{
		id: "session-1",
		hub: &voiceHub{
			log:           slog.New(slog.DiscardHandler),
			prerollChunks: 2,
			silenceWindow: time.Second,
			closed:        true,
		},
		webmHeader: []byte("header"),
	}

	session.handleBufferedChunkLocked(browserCaptureMIME, bufferedChunk{Sequence: 1, ReceivedAt: base, Data: []byte("pre-1")})
	session.handleBufferedChunkLocked(browserCaptureMIME, bufferedChunk{Sequence: 2, ReceivedAt: base.Add(time.Millisecond), Data: []byte("pre-2")})
	session.handleBufferedChunkLocked(browserCaptureMIME, bufferedChunk{Sequence: 3, ReceivedAt: base.Add(2 * time.Millisecond), Data: []byte("pre-3")})
	require.Len(t, session.preroll, 2)
	assert.Equal(t, []byte("pre-2"), session.preroll[0].Data)
	assert.Equal(t, []byte("pre-3"), session.preroll[1].Data)

	session.handleBufferedChunkLocked(browserCaptureMIME, bufferedChunk{Sequence: 4, ReceivedAt: base.Add(3 * time.Millisecond), Speaking: true, Data: []byte("voice-1")})
	require.NotNil(t, session.current)
	assert.Nil(t, session.preroll)
	assert.Equal(t, []byte("header"), session.current.HeaderData)
	assert.Equal(t, browserCaptureMIME, session.current.MIMEType)
	require.Len(t, session.current.Chunks, 2)
	assert.Equal(t, []byte("pre-3"), session.current.Chunks[0].Data)
	assert.Equal(t, []byte("voice-1"), session.current.Chunks[1].Data)

	session.handleBufferedChunkLocked(browserCaptureMIME, bufferedChunk{Sequence: 5, ReceivedAt: base.Add(4 * time.Millisecond), Speaking: true, Data: []byte("voice-2")})
	assert.Equal(t, base.Add(4*time.Millisecond), session.current.LastSpeechAt)

	session.handleBufferedChunkLocked(browserCaptureMIME, bufferedChunk{Sequence: 6, ReceivedAt: base.Add(2 * time.Second), Data: []byte("silence")})
	assert.Nil(t, session.current)
}

func TestVoiceHubEnqueuePlaybackSessionStates(t *testing.T) {
	hub := &voiceHub{
		log:       slog.New(slog.DiscardHandler),
		sessions:  map[string]*voiceSession{},
		playbacks: map[string]playbackAsset{},
		assetIDs:  map[string][]string{},
	}
	asset := playbackAsset{MIMEType: "audio/mpeg", Data: []byte("audio")}

	assert.False(t, hub.enqueuePlayback("missing", "asset-missing", asset, "hello"))

	session := &voiceSession{id: "session-1", hub: hub, send: make(chan *serverMessage, 1)}
	hub.sessions[session.id] = session

	hub.closed = true
	assert.False(t, hub.enqueuePlayback(session.id, "asset-closed", asset, "hello"))
	assert.NotContains(t, hub.playbacks, "asset-closed")
	assert.NotContains(t, hub.assetIDs, session.id)
	hub.closed = false

	session.send <- &serverMessage{Type: "state"}

	assert.False(t, hub.enqueuePlayback(session.id, "asset-full", asset, "hello"))
	assert.NotContains(t, hub.playbacks, "asset-full")
	assert.NotContains(t, hub.assetIDs, session.id)

	<-session.send
	require.True(t, hub.enqueuePlayback(session.id, "asset-ok", asset, "hello"))
	assert.Equal(t, asset, hub.playbacks["asset-ok"])
	assert.Equal(t, []string{"asset-ok"}, hub.assetIDs[session.id])

	want := &serverMessage{
		Type:        "playback",
		Status:      "playing",
		Message:     "Playing assistant response.",
		PlaybackURL: playbackPathPrefix + "asset-ok",
		MIMEType:    "audio/mpeg",
		Text:        "hello",
	}
	assert.Equal(t, want, readQueuedServerMessage(t, session.send))
}

func TestVoiceHubServePlaybackReturnsAndRemovesAsset(t *testing.T) {
	hub := &voiceHub{
		log:       slog.New(slog.DiscardHandler),
		sessions:  map[string]*voiceSession{},
		playbacks: map[string]playbackAsset{"asset-1": {MIMEType: "audio/mpeg", Data: []byte("audio")}},
		assetIDs:  map[string][]string{"session-1": {"asset-1", "asset-2"}},
	}
	req := httptest.NewRequest(http.MethodGet, playbackPathPrefix+"asset-1", http.NoBody)
	recorder := httptest.NewRecorder()

	hub.servePlayback(recorder, req)
	resp := recorder.Result()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "audio/mpeg", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Equal(t, []byte("audio"), body)
	assert.NotContains(t, hub.playbacks, "asset-1")
	assert.Equal(t, []string{"asset-2"}, hub.assetIDs["session-1"])

	recorder = httptest.NewRecorder()
	hub.servePlayback(recorder, req)
	missingResp := recorder.Result()
	assert.Equal(t, http.StatusNotFound, missingResp.StatusCode)
	require.NoError(t, missingResp.Body.Close())

	blankReq := httptest.NewRequest(http.MethodGet, playbackPathPrefix, http.NoBody)
	recorder = httptest.NewRecorder()
	hub.servePlayback(recorder, blankReq)
	blankResp := recorder.Result()
	assert.Equal(t, http.StatusNotFound, blankResp.StatusCode)
	require.NoError(t, blankResp.Body.Close())
}

func TestAllowWebsocketOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://example.test/voice/ws", http.NoBody)
	req.Host = "example.test"

	req.Header.Set("Origin", "https://example.test")
	assert.True(t, allowWebsocketOrigin(req))

	req.Header.Set("Origin", "https://other.test")
	assert.False(t, allowWebsocketOrigin(req))

	req.Header.Set("Origin", "://bad")
	assert.False(t, allowWebsocketOrigin(req))

	req.Header.Set("Origin", " ")
	assert.False(t, allowWebsocketOrigin(req))
}

func TestServerAccessors(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", nil, nil, nil)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	urls := server.URLs()
	require.NotEmpty(t, urls)
	urls[0] = "mutated"

	assert.Equal(t, "web_ui", server.Name())
	assert.NotEqual(t, "mutated", server.URL())
	assert.NoError(t, server.Stop(context.Background()))
}

func TestWebUIIPv4Addrs(t *testing.T) {
	collectorCalled := false
	collector := func() ([]net.IP, error) {
		collectorCalled = true
		return []net.IP{net.IPv4(10, 0, 0, 2), net.IPv4(127, 0, 0, 1), net.ParseIP("::1")}, nil
	}

	ips, err := webUIIPv4Addrs("0.0.0.0", collector)
	require.NoError(t, err)
	assert.True(t, collectorCalled)
	assert.True(t, ips[0].Equal(net.IPv4(127, 0, 0, 1)))
	assert.True(t, ips[1].Equal(net.IPv4(10, 0, 0, 2)))

	collectorCalled = false
	ips, err = webUIIPv4Addrs("127.0.0.1", collector)
	require.NoError(t, err)
	assert.False(t, collectorCalled)
	assert.True(t, ips[0].Equal(net.IPv4(127, 0, 0, 1)))

	ips, err = webUIIPv4Addrs("localhost", collector)
	require.NoError(t, err)
	assert.False(t, collectorCalled)
	require.NotEmpty(t, ips)

	for _, ip := range ips {
		assert.NotNil(t, ip.To4())
	}

	_, err = webUIIPv4Addrs("::1", collector)
	require.ErrorContains(t, err, "web UI listen address must be IPv4-only")

	_, err = webUIIPv4Addrs("bad host", collector)
	require.ErrorContains(t, err, "resolve web UI listen host")
}

func TestPrepareTLSAssetsRejectsInvalidListenAddress(t *testing.T) {
	_, err := prepareTLSAssets(t.TempDir(), "not-a-hostport", "", "", func() ([]net.IP, error) {
		t.Fatal("collector should not be called")
		return nil, nil
	})
	require.ErrorContains(t, err, "parse web UI listen address")
}

func TestPrepareTLSAssetsPropagatesIPv4CollectorError(t *testing.T) {
	errCollect := errors.New("interfaces unavailable")
	_, err := prepareTLSAssets(t.TempDir(), "0.0.0.0:0", "", "", func() ([]net.IP, error) {
		return nil, errCollect
	})
	require.ErrorIs(t, err, errCollect)
}

func TestPrepareTLSAssetsUsesExistingCertificatePair(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "web-ui.crt")
	keyFile := filepath.Join(dir, "web-ui.key")

	require.NoError(t, writeSelfSignedCertificate(certFile, keyFile, []net.IP{net.IPv4(127, 0, 0, 1)}))
	require.NoError(t, os.Chmod(certFile, 0o644))
	require.NoError(t, os.Chmod(keyFile, 0o644))

	assets, err := prepareTLSAssets(t.TempDir(), "127.0.0.1:0", certFile, keyFile, func() ([]net.IP, error) {
		t.Fatal("collector should not be called for explicit listen IP")

		return nil, nil
	})
	require.NoError(t, err)
	assert.Equal(t, certFile, assets.certFile)
	assert.Equal(t, keyFile, assets.keyFile)
	require.Len(t, assets.ips, 1)
	assert.True(t, assets.ips[0].Equal(net.IPv4(127, 0, 0, 1)))

	certInfo, err := os.Stat(certFile)
	require.NoError(t, err)
	keyInfo, err := os.Stat(keyFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), certInfo.Mode().Perm())
	assert.Equal(t, os.FileMode(0o644), keyInfo.Mode().Perm())
}

func TestPrepareTLSAssetsRejectsInvalidExplicitCertificatePair(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "web-ui.crt")
	keyFile := filepath.Join(dir, "web-ui.key")

	require.NoError(t, os.WriteFile(certFile, []byte("not a certificate"), 0o600))
	require.NoError(t, os.WriteFile(keyFile, []byte("not a key"), 0o600))

	_, err := prepareTLSAssets(t.TempDir(), "127.0.0.1:0", certFile, keyFile, func() ([]net.IP, error) {
		t.Fatal("collector should not be called for explicit listen IP")

		return nil, nil
	})
	require.ErrorContains(t, err, "validate web UI TLS certificate")
	require.ErrorContains(t, err, "load certificate pair")
}

func TestPrepareFallbackCertificateReusesExistingPairAndFixesModes(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "web-ui.crt")
	keyFile := filepath.Join(dir, "web-ui.key")
	ips := []net.IP{net.IPv4(127, 0, 0, 1)}

	require.NoError(t, writeSelfSignedCertificate(certFile, keyFile, ips))
	require.NoError(t, os.Chmod(certFile, 0o644))
	require.NoError(t, os.Chmod(keyFile, 0o644))

	certBefore, err := os.ReadFile(certFile)
	require.NoError(t, err)
	keyBefore, err := os.ReadFile(keyFile)
	require.NoError(t, err)

	require.NoError(t, prepareFallbackCertificate(dir, certFile, keyFile, ips))
	certInfo, err := os.Stat(certFile)
	require.NoError(t, err)
	keyInfo, err := os.Stat(keyFile)
	require.NoError(t, err)
	certAfter, err := os.ReadFile(certFile)
	require.NoError(t, err)
	keyAfter, err := os.ReadFile(keyFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), certInfo.Mode().Perm())
	assert.Equal(t, os.FileMode(0o600), keyInfo.Mode().Perm())
	assert.Equal(t, certBefore, certAfter)
	assert.Equal(t, keyBefore, keyAfter)
}

func TestPrepareFallbackCertificateRejectsPartialPair(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "web-ui.crt")
	keyFile := filepath.Join(dir, "web-ui.key")

	require.NoError(t, os.WriteFile(certFile, []byte("not a certificate"), 0o600))

	err := prepareFallbackCertificate(dir, certFile, keyFile, []net.IP{net.IPv4(127, 0, 0, 1)})
	require.EqualError(t, err, "web UI fallback TLS certificate and key must both exist or both be absent")
}

func TestPrepareFallbackCertificateRegeneratesInvalidExistingPair(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "web-ui.crt")
	keyFile := filepath.Join(dir, "web-ui.key")
	ips := []net.IP{net.IPv4(127, 0, 0, 1)}

	require.NoError(t, os.WriteFile(certFile, []byte("not a certificate"), 0o600))
	require.NoError(t, os.WriteFile(keyFile, []byte("not a key"), 0o600))

	require.NoError(t, prepareFallbackCertificate(dir, certFile, keyFile, ips))
	require.NoError(t, validateFallbackCertificatePair(certFile, keyFile, ips))
}

func TestPrepareFallbackCertificateReportsDirectoryCreateError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-directory")
	certFile := filepath.Join(dir, "web-ui.crt")
	keyFile := filepath.Join(dir, "web-ui.key")

	require.NoError(t, os.WriteFile(blocker, []byte("block mkdir"), 0o600))

	err := prepareFallbackCertificate(blocker, certFile, keyFile, []net.IP{net.IPv4(127, 0, 0, 1)})
	require.ErrorContains(t, err, "create web UI TLS directory")
}

func TestPrepareFallbackCertificateReportsCertificateWriteError(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "web-ui.crt")
	keyFile := filepath.Join(dir, "web-ui.key")
	ips := []net.IP{net.IPv4(127, 0, 0, 1)}

	require.NoError(t, os.Mkdir(certFile, 0o755))
	t.Cleanup(func() { require.NoError(t, os.Chmod(certFile, 0o755)) })
	require.NoError(t, writeSelfSignedCertificate(filepath.Join(dir, "other.crt"), keyFile, ips))

	err := prepareFallbackCertificate(dir, certFile, keyFile, ips)
	require.ErrorContains(t, err, "write web UI TLS certificate")
}

func TestPrepareFallbackCertificateReportsKeyWriteError(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "web-ui.crt")
	keyFile := filepath.Join(dir, "web-ui.key")
	ips := []net.IP{net.IPv4(127, 0, 0, 1)}

	require.NoError(t, os.Mkdir(keyFile, 0o755))
	t.Cleanup(func() { require.NoError(t, os.Chmod(keyFile, 0o755)) })
	require.NoError(t, writeSelfSignedCertificate(certFile, filepath.Join(dir, "other.key"), ips))

	err := prepareFallbackCertificate(dir, certFile, keyFile, ips)
	require.ErrorContains(t, err, "write web UI TLS key")
}

func TestValidateCertificatePairRequiresListenIPCoverage(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "web-ui.crt")
	keyFile := filepath.Join(dir, "web-ui.key")

	require.NoError(t, writeSelfSignedCertificate(certFile, keyFile, []net.IP{net.IPv4(127, 0, 0, 1)}))

	err := validateCertificatePair(certFile, keyFile, []net.IP{net.IPv4(10, 0, 0, 2)})
	require.ErrorContains(t, err, "certificate does not cover 10.0.0.2")
}

func TestValidateCertificatePairRejectsExpiredCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "web-ui.crt")
	keyFile := filepath.Join(dir, "web-ui.key")
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	writeTestCertificatePair(
		t, certFile, keyFile, privateKey,
		time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour),
		[]net.IP{net.IPv4(127, 0, 0, 1)},
	)

	err = validateCertificatePair(certFile, keyFile, []net.IP{net.IPv4(127, 0, 0, 1)})
	require.ErrorContains(t, err, "certificate is expired or not yet valid")
}

func TestValidateFallbackCertificatePairRequiresRSA(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "web-ui.crt")
	keyFile := filepath.Join(dir, "web-ui.key")
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	writeTestCertificatePair(
		t, certFile, keyFile, privateKey,
		time.Now().Add(-time.Minute), time.Now().Add(time.Hour),
		[]net.IP{net.IPv4(127, 0, 0, 1)},
	)

	err = validateFallbackCertificatePair(certFile, keyFile, []net.IP{net.IPv4(127, 0, 0, 1)})
	require.ErrorContains(t, err, "fallback certificate must use RSA")
}

func TestVoiceHubSendResponseStatusMessages(t *testing.T) {
	errSynthesis := errors.New("synthesis unavailable")
	hub := &voiceHub{
		log:      slog.New(slog.DiscardHandler),
		sessions: map[string]*voiceSession{},
		tts:      &fakeSynthesizer{failFormats: map[string]error{"mp3": errSynthesis, "opus": errSynthesis}},
	}
	session := &voiceSession{id: "session-1", hub: hub, send: make(chan *serverMessage, 2), turnText: map[string]string{}}
	hub.sessions[session.id] = session

	thinking := events.NewMainOutboundMessage(events.SourceWebVoice, "", events.OutputTargetWebUI)
	thinking.WebSessionID = session.id
	thinking.SlackThinking = "working"
	require.NoError(t, hub.SendResponse(t.Context(), thinking))
	assert.Equal(t, stateServerMessage("thinking", "Assistant is thinking."), readQueuedServerMessage(t, session.send))

	reply := events.NewMainOutboundMessage(events.SourceWebVoice, "browser reply", events.OutputTargetWebUI)
	reply.WebSessionID = session.id
	err := hub.SendResponse(t.Context(), reply)
	require.ErrorIs(t, err, errSynthesis)
	assert.Equal(t, stateServerMessage("error", "Assistant playback synthesis failed."), readQueuedServerMessage(t, session.send))
}

func TestVoiceHubSendResponseNoPlaybackEdges(t *testing.T) {
	hub := &voiceHub{
		log:      slog.New(slog.DiscardHandler),
		sessions: map[string]*voiceSession{},
	}
	session := &voiceSession{id: "session-1", hub: hub, send: make(chan *serverMessage, 1), turnText: map[string]string{"turn-1": "hello"}}
	hub.sessions[session.id] = session

	require.NoError(t, hub.SendResponse(t.Context(), events.NewMainOutboundMessage(events.SourceWebVoice, "no session", events.OutputTargetWebUI)))
	require.Empty(t, session.send)

	missing := events.NewMainOutboundMessage(events.SourceWebVoice, "missing session", events.OutputTargetWebUI)
	missing.WebSessionID = "missing"
	require.NoError(t, hub.SendResponse(t.Context(), missing))
	require.Empty(t, session.send)

	blank := events.NewMainOutboundMessage(events.SourceWebVoice, " \t ", events.OutputTargetWebUI)
	blank.WebSessionID = session.id
	require.NoError(t, hub.SendResponse(t.Context(), blank))
	require.Empty(t, session.send)

	duplicateComplete := events.NewMainOutboundMessage(events.SourceWebVoice, "hello", events.OutputTargetWebUI)
	duplicateComplete.WebSessionID = session.id
	duplicateComplete.TurnID = "turn-1"
	duplicateComplete.Complete = true
	require.NoError(t, hub.SendResponse(t.Context(), duplicateComplete))
	require.Empty(t, session.send)
	assert.NotContains(t, session.turnText, "turn-1")

	reply := events.NewMainOutboundMessage(events.SourceWebVoice, "browser reply", events.OutputTargetWebUI)
	reply.WebSessionID = session.id
	require.NoError(t, hub.SendResponse(t.Context(), reply))
	assert.Equal(t, stateServerMessage("error", "Speech playback is unavailable."), readQueuedServerMessage(t, session.send))
}

func TestVoiceHubSendResponseDropsPlaybackWhenSessionQueueFull(t *testing.T) {
	tts := &fakeSynthesizer{data: []byte("mp3-data")}
	hub := &voiceHub{
		log:       slog.New(slog.DiscardHandler),
		sessions:  map[string]*voiceSession{},
		playbacks: map[string]playbackAsset{},
		assetIDs:  map[string][]string{},
		tts:       tts,
	}
	session := &voiceSession{id: "session-1", hub: hub, send: make(chan *serverMessage, 1), turnText: map[string]string{}}

	hub.sessions[session.id] = session
	session.send <- &serverMessage{Type: "state"}

	reply := events.NewMainOutboundMessage(events.SourceWebVoice, "browser reply", events.OutputTargetWebUI)
	reply.WebSessionID = session.id
	require.NoError(t, hub.SendResponse(t.Context(), reply))

	assert.Equal(t, []string{"mp3"}, tts.formats)
	assert.Empty(t, hub.playbacks)
	assert.Empty(t, hub.assetIDs)
}

func TestSynthesizeBrowserPlaybackReturnsFormatErrors(t *testing.T) {
	errMP3 := errors.New("mp3 unavailable")
	errOpus := errors.New("opus unavailable")
	tts := &fakeSynthesizer{failFormats: map[string]error{"mp3": errMP3, "opus": errOpus}}
	hub := &voiceHub{tts: tts}

	_, err := hub.synthesizeBrowserPlayback(t.Context(), "browser reply")
	require.ErrorIs(t, err, errMP3)
	require.ErrorIs(t, err, errOpus)
	assert.Equal(t, []string{"mp3", "opus"}, tts.formats)
}

func TestSynthesizeBrowserPlaybackFallsBackAfterStreamErrors(t *testing.T) {
	errRead := errors.New("read failed")
	errClose := errors.New("close failed")

	for _, tt := range []struct {
		name        string
		readErrors  map[string]error
		closeErrors map[string]error
	}{
		{name: "read error", readErrors: map[string]error{"mp3": errRead}},
		{name: "close error", closeErrors: map[string]error{"mp3": errClose}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tts := &fakeSynthesizer{data: []byte("opus-data"), readErrors: tt.readErrors, closeErrors: tt.closeErrors}
			hub := &voiceHub{tts: tts}

			asset, err := hub.synthesizeBrowserPlayback(t.Context(), "browser reply")
			require.NoError(t, err)
			assert.Equal(t, playbackAsset{MIMEType: "audio/ogg", Data: []byte("opus-data")}, asset)
			assert.Equal(t, []string{"mp3", "opus"}, tts.formats)
		})
	}
}

func TestCollectSystemInterfaceIPv4Addrs(t *testing.T) {
	ips, err := collectSystemInterfaceIPv4Addrs()
	require.NoError(t, err)

	for _, ip := range ips {
		assert.NotNil(t, ip.To4())
	}
}

func TestWebsocketUtterancePublishesInboundWithPreroll(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	transcriber := new(fakeTranscriber)
	transcriber.text = "hello from browser voice"
	publisher := voice.NewTranscriptionPublisher(bus, slog.New(slog.DiscardHandler), events.SourceWebVoice, nil, inertBeforeMainSession)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", transcriber, nil, publisher)
	require.NoError(t, err)

	server.hub.silenceWindow = time.Nanosecond

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	conn := dialVoiceSocket(t, server)

	defer func() { require.NoError(t, conn.Close()) }()

	ready := readSocketMessage(t, conn)
	require.Equal(t, "ready", ready.Type)
	require.NoError(t, conn.WriteJSON(helloClientMessage()))
	require.Equal(t, "state", readSocketMessage(t, conn).Type)

	firstChunk := testWebMChunk(true, []byte("pre"))
	secondChunk := testWebMChunk(false, []byte("voice"))
	thirdChunk := testWebMChunk(false, []byte("tail"))

	require.NoError(t, conn.WriteJSON(audioClientMessage(1, false, firstChunk)))
	require.NoError(t, conn.WriteJSON(audioClientMessage(2, true, secondChunk)))
	require.NoError(t, conn.WriteJSON(audioClientMessage(3, false, thirdChunk)))

	inbound := readInboundMessage(t, bus)
	assert.Equal(t, events.SourceWebVoice, inbound.Source)
	assert.Equal(t, "hello from browser voice", inbound.Text)
	assert.NotContains(t, inbound.Text, "Browser voice utterance:")
	assert.Equal(t, ready.SessionID, inbound.WebSessionID)
	require.NotEmpty(t, transcriber.lastData())

	expectedHeader, _ := splitWebMHeader(firstChunk)
	expectedChunks := []bufferedChunk{
		testBufferedChunk(append([]byte(webMClusterElementID), []byte("pre")...)),
		testBufferedChunk(append([]byte(webMClusterElementID), []byte("voice")...)),
		testBufferedChunk(append([]byte(webMClusterElementID), []byte("tail")...)),
	}
	assert.Equal(t, assembleWebMUtterance(expectedHeader, expectedChunks), transcriber.lastData())
}

func TestWebsocketUtteranceCompletesWithoutTrailingAudioChunk(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	transcriber := new(fakeTranscriber)
	transcriber.text = "hello from browser voice"
	publisher := voice.NewTranscriptionPublisher(bus, slog.New(slog.DiscardHandler), events.SourceWebVoice, nil, inertBeforeMainSession)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", transcriber, nil, publisher)
	require.NoError(t, err)

	server.hub.silenceWindow = 10 * time.Millisecond

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	conn := dialVoiceSocket(t, server)

	defer func() { require.NoError(t, conn.Close()) }()

	ready := readSocketMessage(t, conn)
	require.Equal(t, "ready", ready.Type)
	require.NoError(t, conn.WriteJSON(helloClientMessage()))
	require.Equal(t, "state", readSocketMessage(t, conn).Type)

	voiceChunk := testWebMChunk(true, []byte("voice"))
	require.NoError(t, conn.WriteJSON(audioClientMessage(1, true, voiceChunk)))

	inbound := readInboundMessage(t, bus)
	assert.Equal(t, events.SourceWebVoice, inbound.Source)
	assert.Equal(t, "hello from browser voice", inbound.Text)
	assert.NotContains(t, inbound.Text, "Browser voice utterance:")
	assert.Equal(t, ready.SessionID, inbound.WebSessionID)

	expectedHeader, expectedBody := splitWebMHeader(voiceChunk)
	assert.Equal(t, assembleWebMUtterance(expectedHeader, []bufferedChunk{testBufferedChunk(expectedBody)}), transcriber.lastData())
}

func TestVoiceHubProcessUtteranceSkipsEmptyAndWhitespaceTranscription(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	transcriber := new(fakeTranscriber)
	transcriber.text = " \n\t"
	publisher := voice.NewTranscriptionPublisher(bus, slog.New(slog.DiscardHandler), events.SourceWebVoice, nil, inertBeforeMainSession)
	hub := newVoiceHub(t.Context(), slog.New(slog.DiscardHandler), transcriber, nil, publisher)

	defer func() { require.NoError(t, hub.close(context.Background())) }()

	hub.processUtterance(t.Context(), "session-empty", &utteranceBuffer{})
	assert.Empty(t, transcriber.lastData())

	hub.processUtterance(t.Context(), "session-whitespace", &utteranceBuffer{Chunks: []bufferedChunk{testBufferedChunk([]byte("voice"))}})
	assert.Equal(t, []byte("voice"), transcriber.lastData())

	bus.StopInbound()

	var inbound []*events.InboundMessage
	for msg := range bus.Inbound(context.Background()) {
		inbound = append(inbound, msg)
	}

	assert.Empty(t, inbound)
}

func TestVoiceHubProcessUtteranceReportsPublishError(t *testing.T) {
	bus := events.New()

	bus.StopInbound()
	defer bus.Close()

	transcriber := new(fakeTranscriber)
	transcriber.text = "published text"

	var logs bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&logs, nil))
	publisher := voice.NewTranscriptionPublisher(bus, logger, events.SourceWebVoice, nil, inertBeforeMainSession)
	hub := newVoiceHub(t.Context(), logger, transcriber, nil, publisher)

	defer func() { require.NoError(t, hub.close(context.Background())) }()

	hub.processUtterance(t.Context(), "session-publish-error", &utteranceBuffer{
		Chunks: []bufferedChunk{testBufferedChunk([]byte("dropped"))},
	})
	assert.Equal(t, []byte("dropped"), transcriber.lastData())
	assert.Contains(t, logs.String(), "publish browser voice transcription")
}

func TestVoiceHubProcessUtteranceReportsTranscribeError(t *testing.T) {
	errTranscribe := errors.New("transcription unavailable")
	transcriber := &fakeTranscriber{err: errTranscribe}

	var logs bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&logs, nil))

	bus := events.New()
	defer bus.Close()

	publisher := voice.NewTranscriptionPublisher(bus, logger, events.SourceWebVoice, nil, inertBeforeMainSession)
	hub := newVoiceHub(t.Context(), logger, transcriber, nil, publisher)

	defer func() { require.NoError(t, hub.close(context.Background())) }()

	hub.processUtterance(t.Context(), "session-transcribe-error", &utteranceBuffer{
		Chunks: []bufferedChunk{testBufferedChunk([]byte("voice"))},
	})
	assert.Equal(t, []byte("voice"), transcriber.lastData())
	assert.Contains(t, logs.String(), "transcribe browser voice utterance")
	assert.Contains(t, logs.String(), errTranscribe.Error())
}

func TestSendResponseQueuesPlaybackForBrowserSession(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	tts := new(fakeSynthesizer)
	tts.data = []byte("mp3-data")
	publisher := voice.NewTranscriptionPublisher(bus, slog.New(slog.DiscardHandler), events.SourceWebVoice, nil, inertBeforeMainSession)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", new(fakeTranscriber), tts, publisher)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	conn := dialVoiceSocket(t, server)

	defer func() { require.NoError(t, conn.Close()) }()

	ready := readSocketMessage(t, conn)
	require.Equal(t, "ready", ready.Type)
	require.NoError(t, conn.WriteJSON(helloClientMessage()))
	_ = readSocketMessage(t, conn)

	msg := events.NewMainOutboundMessage(events.SourceWebVoice, "browser reply", events.OutputTargetWebUI)
	msg.WebSessionID = ready.SessionID
	msg.Complete = true
	msg.Attachments = []events.OutboundAttachment{{Name: "report.txt"}}
	require.NoError(t, server.SendResponse(t.Context(), msg))

	playback := readSocketMessage(t, conn)
	require.Equal(t, "playback", playback.Type)
	assert.NotEmpty(t, playback.PlaybackURL)
	assert.Equal(t, "audio/mpeg", playback.MIMEType)

	resp, err := httpsClient(t, server).Get(httpBaseURL(server.URL()) + playback.PlaybackURL)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "audio/mpeg", resp.Header.Get("Content-Type"))
	assert.Equal(t, tts.data, body)
	assert.Equal(t, []string{"mp3"}, tts.formats)
	assert.Equal(t, []string{"browser reply\n\nAttached files: report.txt."}, tts.texts)
}

func TestSendResponseFallsBackToOggWhenMP3SynthesisFails(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	tts := new(fakeSynthesizer)
	tts.data = []byte("ogg-opus-data")
	tts.failFormats = map[string]error{"mp3": errors.New("mp3 unavailable")}
	publisher := voice.NewTranscriptionPublisher(bus, slog.New(slog.DiscardHandler), events.SourceWebVoice, nil, inertBeforeMainSession)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", new(fakeTranscriber), tts, publisher)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	conn := dialVoiceSocket(t, server)

	defer func() { require.NoError(t, conn.Close()) }()

	ready := readSocketMessage(t, conn)
	require.NoError(t, conn.WriteJSON(helloClientMessage()))
	_ = readSocketMessage(t, conn)

	msg := events.NewMainOutboundMessage(events.SourceWebVoice, "browser reply", events.OutputTargetWebUI)
	msg.WebSessionID = ready.SessionID
	msg.Complete = true
	require.NoError(t, server.SendResponse(t.Context(), msg))

	playback := readSocketMessage(t, conn)
	assert.Equal(t, "audio/ogg", playback.MIMEType)
	assert.Equal(t, []string{"mp3", "opus"}, tts.formats)
}

func TestSendResponseQueuesSequentialBrowserPlaybackWithoutDroppingLaterAudio(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	tts := new(fakeSynthesizer)
	tts.sequence = [][]byte{[]byte("first-mp3"), []byte("second-mp3")}
	publisher := voice.NewTranscriptionPublisher(bus, slog.New(slog.DiscardHandler), events.SourceWebVoice, nil, inertBeforeMainSession)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", new(fakeTranscriber), tts, publisher)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	conn := dialVoiceSocket(t, server)

	defer func() { require.NoError(t, conn.Close()) }()

	ready := readSocketMessage(t, conn)
	require.NoError(t, conn.WriteJSON(helloClientMessage()))
	_ = readSocketMessage(t, conn)

	first := events.NewMainOutboundMessage(events.SourceWebVoice, "first reply", events.OutputTargetWebUI)
	first.WebSessionID = ready.SessionID
	first.Complete = true
	require.NoError(t, server.SendResponse(t.Context(), first))

	second := events.NewMainOutboundMessage(events.SourceWebVoice, "second reply", events.OutputTargetWebUI)
	second.WebSessionID = ready.SessionID
	second.Complete = true
	require.NoError(t, server.SendResponse(t.Context(), second))

	firstPlayback := readSocketMessage(t, conn)
	secondPlayback := readSocketMessage(t, conn)
	require.NotEqual(t, firstPlayback.PlaybackURL, secondPlayback.PlaybackURL)
	assert.Equal(t, "audio/mpeg", firstPlayback.MIMEType)
	assert.Equal(t, "audio/mpeg", secondPlayback.MIMEType)
	assert.Equal(t, []string{"mp3", "mp3"}, tts.formats)

	resp1, err := httpsClient(t, server).Get(httpBaseURL(server.URL()) + firstPlayback.PlaybackURL)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp1.Body.Close()) }()

	body1, err := io.ReadAll(resp1.Body)
	require.NoError(t, err)
	assert.Equal(t, []byte("first-mp3"), body1)

	resp2, err := httpsClient(t, server).Get(httpBaseURL(server.URL()) + secondPlayback.PlaybackURL)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp2.Body.Close()) }()

	body2, err := io.ReadAll(resp2.Body)
	require.NoError(t, err)
	assert.Equal(t, []byte("second-mp3"), body2)
}

func TestSendResponseQueuesIncrementalBrowserSnapshots(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	tts := new(fakeSynthesizer)
	tts.data = []byte("ogg-opus-data")
	publisher := voice.NewTranscriptionPublisher(bus, slog.New(slog.DiscardHandler), events.SourceWebVoice, nil, inertBeforeMainSession)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", new(fakeTranscriber), tts, publisher)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	conn := dialVoiceSocket(t, server)

	defer func() { require.NoError(t, conn.Close()) }()

	ready := readSocketMessage(t, conn)
	require.NoError(t, conn.WriteJSON(helloClientMessage()))
	_ = readSocketMessage(t, conn)

	first := events.NewMainOutboundMessage(events.SourceWebVoice, "Hel", events.OutputTargetWebUI)
	first.WebSessionID = ready.SessionID
	first.TurnID = "turn-1"
	first.Complete = false
	require.NoError(t, server.SendResponse(t.Context(), first))

	second := events.NewMainOutboundMessage(events.SourceWebVoice, "Hello", events.OutputTargetWebUI)
	second.WebSessionID = ready.SessionID
	second.TurnID = "turn-1"
	second.Complete = false
	require.NoError(t, server.SendResponse(t.Context(), second))

	final := events.NewMainOutboundMessage(events.SourceWebVoice, "Hello", events.OutputTargetWebUI)
	final.WebSessionID = ready.SessionID
	final.TurnID = "turn-1"
	final.Complete = true
	require.NoError(t, server.SendResponse(t.Context(), final))

	firstPlayback := readSocketMessage(t, conn)
	secondPlayback := readSocketMessage(t, conn)
	require.Equal(t, "playback", firstPlayback.Type)
	require.Equal(t, "playback", secondPlayback.Type)
	assert.Equal(t, "Hel", firstPlayback.Text)
	assert.Equal(t, "Hello", secondPlayback.Text)
	assert.Equal(t, []string{"Hel", "lo"}, tts.texts)
}

func TestWebsocketReadLoopReportsClientMessageErrors(t *testing.T) {
	for _, tt := range []struct {
		name  string
		write func(*websocket.Conn) error
		want  string
	}{
		{
			name: "binary message",
			write: func(conn *websocket.Conn) error {
				return conn.WriteMessage(websocket.BinaryMessage, []byte("binary"))
			},
			want: "Unsupported browser voice message type.",
		},
		{
			name: "invalid JSON",
			write: func(conn *websocket.Conn) error {
				return conn.WriteMessage(websocket.TextMessage, []byte("{"))
			},
			want: "Invalid browser voice message.",
		},
		{
			name: "unsupported message",
			write: func(conn *websocket.Conn) error {
				return conn.WriteJSON(&clientMessage{Type: "bogus"})
			},
			want: `unsupported browser voice message type "bogus"`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			serverConnCh := make(chan struct {
				conn *websocket.Conn
				err  error
			}, 1)
			upgrader := new(websocket.Upgrader)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				serverConnCh <- struct {
					conn *websocket.Conn
					err  error
				}{conn: conn, err: err}
			}))

			defer server.Close()

			clientConn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if resp != nil && resp.Body != nil {
				require.NoError(t, resp.Body.Close())
			}

			require.NoError(t, err)

			defer func() { _ = clientConn.Close() }()

			serverResult := <-serverConnCh
			require.NoError(t, serverResult.err)

			hub := &voiceHub{log: slog.New(slog.DiscardHandler), sessions: map[string]*voiceSession{}, playbacks: map[string]playbackAsset{}, assetIDs: map[string][]string{}}
			session := &voiceSession{id: "session-1", hub: hub, conn: serverResult.conn, send: make(chan *serverMessage, 1), turnText: map[string]string{}}
			hub.sessions[session.id] = session

			require.NoError(t, tt.write(clientConn))
			session.readLoop()

			message := readQueuedServerMessage(t, session.send)
			require.Equal(t, "state", message.Type)
			assert.Equal(t, "error", message.Status)
			assert.Equal(t, tt.want, message.Message)
		})
	}
}

func TestWebsocketRejectsUnexpectedOrigin(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", nil, nil, nil)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	headers := make(http.Header)
	headers.Set("Origin", "https://evil.example")

	conn, resp, err := websocketDialer(t, server).Dial(websocketURL(server.URL())+voiceSocketPath, headers)
	if resp != nil && resp.Body != nil {
		defer func() { require.NoError(t, resp.Body.Close()) }()
	}

	if conn != nil {
		defer func() { require.NoError(t, conn.Close()) }()
	}

	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestWebsocketRejectsMissingOrigin(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", nil, nil, nil)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	conn, resp, err := websocketDialer(t, server).Dial(websocketURL(server.URL())+voiceSocketPath, nil)
	if resp != nil && resp.Body != nil {
		defer func() { require.NoError(t, resp.Body.Close()) }()
	}

	if conn != nil {
		defer func() { require.NoError(t, conn.Close()) }()
	}

	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestWebsocketRejectsInboundOggHelloMimeType(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	hub := newVoiceHub(t.Context(), slog.New(slog.DiscardHandler), new(fakeTranscriber), nil, voice.NewTranscriptionPublisher(bus, slog.New(slog.DiscardHandler), events.SourceWebVoice, nil, inertBeforeMainSession))
	session := new(voiceSession)
	session.hub = hub

	badHello := new(clientMessage)
	badHello.Type = "hello"
	badHello.Transport = transportVersion
	badHello.MIMEType = "audio/ogg;codecs=opus"
	err := session.handleHello(badHello)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported browser Opus MIME type")
}

func TestStartCreatesReusableIPv4FallbackCertificate(t *testing.T) {
	workspace := t.TempDir()
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), workspace, "127.0.0.1:0", "", "", nil, nil, nil)
	require.NoError(t, err)
	require.NoError(t, server.Close(context.Background()))

	certPath := filepath.Join(workspace, ".rocketclaw", fallbackCertFilename)
	keyPath := filepath.Join(workspace, ".rocketclaw", fallbackKeyFilename)

	certInfo, err := os.Stat(certPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), certInfo.Mode().Perm())
	assert.Equal(t, x509.RSA, readCertificate(t, certPath).PublicKeyAlgorithm)

	keyInfo, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), keyInfo.Mode().Perm())

	before := certInfo.ModTime()
	server, err = Start(t.Context(), slog.New(slog.DiscardHandler), workspace, "127.0.0.1:0", "", "", nil, nil, nil)
	require.NoError(t, err)
	require.NoError(t, server.Close(context.Background()))

	certInfo, err = os.Stat(certPath)
	require.NoError(t, err)
	assert.Equal(t, before, certInfo.ModTime())
}

func TestPrepareTLSAssetsRepairsFallbackPermissionsAndAcceptsExplicitCertificate(t *testing.T) {
	workspace := t.TempDir()
	collector := func() ([]net.IP, error) {
		t.Fatal("collector should not be called for explicit IPv4 listen address")
		return nil, nil
	}

	assets, err := prepareTLSAssets(workspace, "127.0.0.1:0", "", "", collector)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(assets.certFile, 0o644))
	require.NoError(t, os.Chmod(assets.keyFile, 0o644))

	_, err = prepareTLSAssets(workspace, "127.0.0.1:0", "", "", collector)
	require.NoError(t, err)

	certInfo, err := os.Stat(assets.certFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), certInfo.Mode().Perm())

	keyInfo, err := os.Stat(assets.keyFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), keyInfo.Mode().Perm())

	explicit, err := prepareTLSAssets(workspace, "127.0.0.1:0", assets.certFile, assets.keyFile, collector)
	require.NoError(t, err)
	assert.Equal(t, assets.certFile, explicit.certFile)
	assert.Equal(t, assets.keyFile, explicit.keyFile)
}

func TestStartRegeneratesFallbackCertificateForCurrentIPv4Interfaces(t *testing.T) {
	workspace := t.TempDir()
	collectIPv4Addrs := func() ([]net.IP, error) { return []net.IP{net.IPv4(10, 0, 0, 5)}, nil }

	_, err := prepareTLSAssets(workspace, "0.0.0.0:0", "", "", collectIPv4Addrs)
	require.NoError(t, err)

	certPath := filepath.Join(workspace, ".rocketclaw", fallbackCertFilename)
	cert := readCertificate(t, certPath)
	assert.NoError(t, cert.VerifyHostname("10.0.0.5"))
	assert.NoError(t, cert.VerifyHostname("127.0.0.1"))

	collectIPv4Addrs = func() ([]net.IP, error) { return []net.IP{net.IPv4(10, 0, 0, 6)}, nil }
	_, err = prepareTLSAssets(workspace, "0.0.0.0:0", "", "", collectIPv4Addrs)
	require.NoError(t, err)

	cert = readCertificate(t, certPath)
	assert.NoError(t, cert.VerifyHostname("10.0.0.6"))
	assert.NoError(t, cert.VerifyHostname("127.0.0.1"))
}

func TestStartRejectsPartialFallbackCertificatePair(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".rocketclaw")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, fallbackCertFilename), []byte("cert"), 0o600))

	_, err := Start(t.Context(), slog.New(slog.DiscardHandler), workspace, "127.0.0.1:0", "", "", nil, nil, nil)
	require.ErrorContains(t, err, "certificate and key must both exist")
}

func TestStartAdvertisesIPv4URLsOnly(t *testing.T) {
	urls := voiceModeURLs([]net.IP{net.IPv4(127, 0, 0, 1), net.IPv4(10, 0, 0, 5)}, "8766")
	assert.Contains(t, urls[0], "https://127.0.0.1:")
	assert.Contains(t, strings.Join(urls, "\n"), "https://10.0.0.5:")
	assert.NotContains(t, strings.Join(urls, "\n"), "localhost")
	assert.NotContains(t, strings.Join(urls, "\n"), "[")
}

func TestNormalizeBrowserOpusMimeTypeChromeOnly(t *testing.T) {
	assert.Equal(t, browserCaptureMIME, normalizeBrowserOpusMIMEType("audio/webm;codecs=opus"))
	assert.Equal(t, browserCaptureMIME, normalizeBrowserOpusMIMEType("audio/webm; codecs=opus"))
	assert.Empty(t, normalizeBrowserOpusMIMEType("audio/ogg;codecs=opus"))
	assert.Empty(t, normalizeBrowserOpusMIMEType("audio/webm"))
}

func TestSplitAndAssembleWebMUtterancePreservesHeader(t *testing.T) {
	headerChunk := testWebMChunk(true, []byte("alpha"))
	secondChunk := testWebMChunk(false, []byte("beta"))

	headerData, firstBody := splitWebMHeader(headerChunk)
	require.NotEmpty(t, headerData)
	require.NotEmpty(t, firstBody)

	assembled := assembleWebMUtterance(headerData, []bufferedChunk{testBufferedChunk(firstBody), testBufferedChunk(secondChunk)})
	expected := append(append([]byte(nil), headerData...), firstBody...)
	expected = append(expected, secondChunk...)
	assert.Equal(t, expected, assembled)
}

func TestSplitWebMHeaderEdges(t *testing.T) {
	header, body := splitWebMHeader(nil)
	assert.Nil(t, header)
	assert.Nil(t, body)

	header, body = splitWebMHeader([]byte("header-only"))
	assert.Equal(t, []byte("header-only"), header)
	assert.Nil(t, body)

	header, body = splitWebMHeader([]byte(webMClusterElementID + "voice"))
	assert.Nil(t, header)
	assert.Equal(t, []byte(webMClusterElementID+"voice"), body)
}

func TestAssembleWebMUtteranceEdges(t *testing.T) {
	body := assembleWebMUtterance(nil, []bufferedChunk{testBufferedChunk([]byte("a")), testBufferedChunk([]byte("b"))})
	assert.Equal(t, []byte("ab"), body)

	headerOnly := assembleWebMUtterance([]byte("webm-header"), nil)
	assert.Equal(t, []byte("webm-header"), headerOnly)

	empty := flattenUtteranceChunks([]bufferedChunk{testBufferedChunk(nil)})
	assert.Nil(t, empty)
}

func TestCloseCancelsOutstandingUtteranceProcessing(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	transcriber := new(blockingTranscriber)
	transcriber.started = make(chan struct{})
	publisher := voice.NewTranscriptionPublisher(bus, slog.New(slog.DiscardHandler), events.SourceWebVoice, nil, inertBeforeMainSession)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", transcriber, nil, publisher)
	require.NoError(t, err)

	server.hub.silenceWindow = time.Nanosecond

	conn := dialVoiceSocket(t, server)

	defer func() { _ = conn.Close() }()

	_ = readSocketMessage(t, conn)
	require.NoError(t, conn.WriteJSON(helloClientMessage()))
	_ = readSocketMessage(t, conn)

	require.NoError(t, conn.WriteJSON(audioClientMessage(1, true, testWebMChunk(true, []byte("voice")))))
	require.NoError(t, conn.WriteJSON(audioClientMessage(2, false, testWebMChunk(false, []byte("tail")))))

	select {
	case <-transcriber.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for browser utterance transcription to start")
	}

	closeDone := make(chan error, 1)

	go func() {
		closeDone <- server.Close(context.Background())
	}()

	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server close to cancel utterance processing")
	}
}

func TestCloseRespectsDeadlineWhenUtteranceWorkIgnoresCancellation(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	transcriber := new(stubbornTranscriber)
	transcriber.started = make(chan struct{})
	transcriber.release = make(chan struct{})
	publisher := voice.NewTranscriptionPublisher(bus, slog.New(slog.DiscardHandler), events.SourceWebVoice, nil, inertBeforeMainSession)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", transcriber, nil, publisher)
	require.NoError(t, err)

	server.hub.silenceWindow = time.Nanosecond

	conn := dialVoiceSocket(t, server)
	_ = readSocketMessage(t, conn)
	require.NoError(t, conn.WriteJSON(helloClientMessage()))
	_ = readSocketMessage(t, conn)
	require.NoError(t, conn.WriteJSON(audioClientMessage(1, true, testWebMChunk(true, []byte("voice")))))
	require.NoError(t, conn.WriteJSON(audioClientMessage(2, false, testWebMChunk(false, []byte("tail")))))

	select {
	case <-transcriber.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stubborn transcription to start")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err = server.Close(closeCtx)
	require.Error(t, err)
	require.ErrorContains(t, err, "wait for browser voice shutdown")

	retryCtx, retryCancel := context.WithCancel(context.Background())
	retryCancel()

	err = server.Close(retryCtx)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "wait for browser voice shutdown")

	close(transcriber.release)
	require.NoError(t, server.Close(context.Background()))

	_ = conn.Close()
}

func TestDisconnectClearsUnfetchedPlaybackAssets(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	tts := new(fakeSynthesizer)
	tts.data = []byte("ogg-opus-data")
	publisher := voice.NewTranscriptionPublisher(bus, slog.New(slog.DiscardHandler), events.SourceWebVoice, nil, inertBeforeMainSession)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", new(fakeTranscriber), tts, publisher)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	conn := dialVoiceSocket(t, server)
	_ = readSocketMessage(t, conn)
	require.NoError(t, conn.WriteJSON(helloClientMessage()))
	_ = readSocketMessage(t, conn)

	msg := events.NewMainOutboundMessage(events.SourceWebVoice, "browser reply", events.OutputTargetWebUI)
	msg.WebSessionID = serverSessionID(t, conn, server)
	msg.Complete = true
	require.NoError(t, server.SendResponse(t.Context(), msg))
	_ = readSocketMessage(t, conn)

	server.hub.mu.Lock()
	assetCount := len(server.hub.playbacks)
	server.hub.mu.Unlock()
	require.Equal(t, 1, assetCount)

	require.NoError(t, conn.Close())
	require.Eventually(t, func() bool {
		server.hub.mu.Lock()
		defer server.hub.mu.Unlock()

		return len(server.hub.playbacks) == 0
	}, time.Second, 10*time.Millisecond)
}

func TestSendResponseDoesNotLeakPlaybackWhenSessionClosesDuringSynthesis(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	tts := new(blockingSynthesizer)
	tts.started = make(chan struct{})
	tts.release = make(chan struct{})
	publisher := voice.NewTranscriptionPublisher(bus, slog.New(slog.DiscardHandler), events.SourceWebVoice, nil, inertBeforeMainSession)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), t.TempDir(), "127.0.0.1:0", "", "", new(fakeTranscriber), tts, publisher)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	conn := dialVoiceSocket(t, server)
	ready := readSocketMessage(t, conn)
	require.NoError(t, conn.WriteJSON(helloClientMessage()))
	_ = readSocketMessage(t, conn)

	msg := events.NewMainOutboundMessage(events.SourceWebVoice, "browser reply", events.OutputTargetWebUI)
	msg.WebSessionID = ready.SessionID
	msg.Complete = true

	resultCh := make(chan error, 1)

	go func() {
		resultCh <- server.SendResponse(t.Context(), msg)
	}()

	select {
	case <-tts.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for blocking synthesis to start")
	}

	require.NoError(t, conn.Close())
	close(tts.release)
	require.NoError(t, <-resultCh)

	require.Eventually(t, func() bool {
		server.hub.mu.Lock()
		defer server.hub.mu.Unlock()

		return len(server.hub.playbacks) == 0
	}, time.Second, 10*time.Millisecond)
}

func TestCloseRetriesAfterFailedShutdown(t *testing.T) {
	server := new(Server)
	callCount := 0
	server.closeFn = func(context.Context) error {
		callCount++
		if callCount == 1 {
			return context.DeadlineExceeded
		}

		return nil
	}

	err := server.Close(t.Context())
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, server.closed)

	require.NoError(t, server.Close(t.Context()))
	assert.True(t, server.closed)
	assert.Equal(t, 2, callCount)
}

type fakeTranscriber struct {
	mu   sync.Mutex
	text string
	err  error
	data [][]byte
}

func (f *fakeTranscriber) Transcribe(_ context.Context, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read transcribed test file: %w", err)
	}

	f.mu.Lock()
	f.data = append(f.data, append([]byte(nil), data...))
	f.mu.Unlock()

	if f.err != nil {
		return "", f.err
	}

	return f.text, nil
}

func (f *fakeTranscriber) lastData() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.data) == 0 {
		return nil
	}

	return append([]byte(nil), f.data[len(f.data)-1]...)
}

type fakeSynthesizer struct {
	data        []byte
	sequence    [][]byte
	callCount   int
	formats     []string
	texts       []string
	failFormats map[string]error
	readErrors  map[string]error
	closeErrors map[string]error
}

func (f *fakeSynthesizer) Synthesize(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.data)), nil
}

func (f *fakeSynthesizer) SynthesizeFormat(_ context.Context, text, responseFormat string) (io.ReadCloser, error) {
	f.texts = append(f.texts, text)

	f.formats = append(f.formats, responseFormat)
	if err := f.failFormats[responseFormat]; err != nil {
		return nil, err
	}

	data := f.data
	if f.callCount < len(f.sequence) {
		data = f.sequence[f.callCount]
	}

	f.callCount++

	if errRead := f.readErrors[responseFormat]; errRead != nil {
		return &fakeSynthesisStream{data: data, errRead: errRead}, nil
	}

	if errClose := f.closeErrors[responseFormat]; errClose != nil {
		return &fakeSynthesisStream{data: data, errClose: errClose}, nil
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

type fakeSynthesisStream struct {
	data     []byte
	offset   int
	errRead  error
	errClose error
}

func (f *fakeSynthesisStream) Read(p []byte) (int, error) {
	if f.errRead != nil {
		return 0, f.errRead
	}

	if f.offset >= len(f.data) {
		return 0, io.EOF
	}

	n := copy(p, f.data[f.offset:])
	f.offset += n

	return n, nil
}

func (f *fakeSynthesisStream) Close() error {
	return f.errClose
}

type blockingTranscriber struct {
	started chan struct{}
	once    sync.Once
}

func (b *blockingTranscriber) Transcribe(ctx context.Context, _ string) (string, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()

	return "", fmt.Errorf("wait for browser voice transcription cancellation: %w", ctx.Err())
}

type stubbornTranscriber struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *stubbornTranscriber) Transcribe(context.Context, string) (string, error) {
	s.once.Do(func() { close(s.started) })
	<-s.release

	return "", nil
}

type blockingSynthesizer struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingSynthesizer) Synthesize(context.Context, string) (io.ReadCloser, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release

	return io.NopCloser(bytes.NewReader([]byte("ogg-opus-data"))), nil
}

func (b *blockingSynthesizer) SynthesizeFormat(ctx context.Context, text, _ string) (io.ReadCloser, error) {
	return b.Synthesize(ctx, text)
}

func dialVoiceSocket(t *testing.T, server *Server) *websocket.Conn {
	t.Helper()

	headers := make(http.Header)
	headers.Set("Origin", httpBaseURL(server.URL()))

	conn, resp, err := websocketDialer(t, server).Dial(websocketURL(server.URL())+voiceSocketPath, headers)
	if resp != nil && resp.Body != nil {
		require.NoError(t, resp.Body.Close())
	}

	require.NoError(t, err)

	return conn
}

func httpsClient(t *testing.T, server *Server) *http.Client {
	t.Helper()

	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig(t, server)}}
}

func websocketDialer(t *testing.T, server *Server) *websocket.Dialer {
	t.Helper()

	dialer := *websocket.DefaultDialer
	dialer.TLSClientConfig = tlsConfig(t, server)

	return &dialer
}

func tlsConfig(t *testing.T, server *Server) *tls.Config {
	t.Helper()

	cert := readCertificate(t, server.certFile)
	pool := x509.NewCertPool()
	pool.AddCert(cert)

	return &tls.Config{RootCAs: pool}
}

func readCertificate(t *testing.T, path string) *x509.Certificate {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	block, _ := pem.Decode(data)
	require.NotNil(t, block)

	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	return cert
}

func writeTestCertificatePair(
	t *testing.T,
	certFile, keyFile string,
	privateKey crypto.Signer,
	notBefore, notAfter time.Time,
	ips []net.IP,
) {
	t.Helper()

	template := &x509.Certificate{
		SerialNumber:          new(big.Int).SetInt64(1),
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           ips,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, privateKey.Public(), privateKey)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		certFile,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		keyFile,
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		0o600,
	))
}

func readSocketMessage(t *testing.T, conn *websocket.Conn) serverMessage {
	t.Helper()

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))

	message := new(serverMessage)
	require.NoError(t, conn.ReadJSON(message))

	return *message
}

func readQueuedServerMessage(t *testing.T, ch <-chan *serverMessage) *serverMessage {
	t.Helper()

	select {
	case message := <-ch:
		return message
	default:
		t.Fatal("no queued server message")
		return nil
	}
}

func serverSessionID(t *testing.T, conn *websocket.Conn, server *Server) string {
	t.Helper()

	server.hub.mu.Lock()
	defer server.hub.mu.Unlock()

	for sessionID := range server.hub.sessions {
		return sessionID
	}

	t.Fatalf("no active browser voice session found for %v", conn.RemoteAddr())

	return ""
}

func readInboundMessage(t *testing.T, bus *events.Bus) *events.InboundMessage {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for msg := range bus.Inbound(ctx) {
		return msg
	}

	t.Fatal("timed out waiting for inbound browser voice message")

	return nil
}

func helloClientMessage() *clientMessage {
	message := new(clientMessage)
	message.Type = "hello"
	message.Transport = transportVersion
	message.MIMEType = browserCaptureMIME

	return message
}

func audioClientMessage(sequence uint64, speaking bool, chunk []byte) *clientMessage {
	message := new(clientMessage)
	message.Type = "audio"
	message.Sequence = sequence
	message.Speaking = speaking
	message.Data = base64.StdEncoding.EncodeToString(chunk)

	return message
}

func httpBaseURL(voiceModeURL string) string {
	return strings.TrimSuffix(voiceModeURL, VoiceModePath)
}

func inertBeforeMainSession(context.Context, string) (*events.SlackReplyTarget, error) {
	return nil, nil
}

func websocketURL(voiceModeURL string) string {
	return "ws" + strings.TrimPrefix(httpBaseURL(voiceModeURL), "http")
}

func testWebMChunk(includeHeader bool, payload []byte) []byte {
	body := append([]byte(nil), []byte(webMClusterElementID)...)

	body = append(body, payload...)
	if !includeHeader {
		return body
	}

	header := make([]byte, 0, 8+len(body))
	header = append(header, 0x1a, 0x45, 0xdf, 0xa3, 'W', 'E', 'B', 'M')

	return append(header, body...)
}

func testBufferedChunk(data []byte) bufferedChunk {
	chunk := new(bufferedChunk)
	chunk.Data = data

	return *chunk
}
