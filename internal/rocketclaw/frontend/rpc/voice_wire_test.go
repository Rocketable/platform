package rpc

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVoiceDialectURLs(t *testing.T) {
	public := func(create, sideband, hangup string) *publicVoice {
		return &publicVoice{createURL: create, sidebandURL: sideband, hangupURL: hangup}
	}

	for _, tc := range []struct {
		provider config.OpenAIConfig
		want     *publicVoice
	}{
		{config.OpenAIConfig{RocketCodeAuth: "api_key"}, public("https://api.openai.com/v1/live/sessions", "wss://api.openai.com/v1/live/sessions/%s/attach", "https://api.openai.com/v1/live/sessions/%s/hangup")},
		{config.OpenAIConfig{RocketCodeAuth: "api_key", APIBaseURL: "ws://localhost:8080/v1/"}, public("http://localhost:8080/v1/live/sessions", "ws://localhost:8080/v1/live/sessions/%s/attach", "http://localhost:8080/v1/live/sessions/%s/hangup")},
		{config.OpenAIConfig{RocketCodeAuth: "api_key", APIBaseURL: "http://127.0.0.1:8080"}, public("http://127.0.0.1:8080/live/sessions", "ws://127.0.0.1:8080/live/sessions/%s/attach", "http://127.0.0.1:8080/live/sessions/%s/hangup")},
		{config.OpenAIConfig{RocketCodeAuth: "api_key", APIBaseURL: "http://[::1]:8080/v1"}, public("http://[::1]:8080/v1/live/sessions", "ws://[::1]:8080/v1/live/sessions/%s/attach", "http://[::1]:8080/v1/live/sessions/%s/hangup")},
		{config.OpenAIConfig{RocketCodeAuth: "api_key", APIBaseURL: "https://proxy.test/v1"}, public("https://proxy.test/v1/live/sessions", "wss://proxy.test/v1/live/sessions/%s/attach", "https://proxy.test/v1/live/sessions/%s/hangup")},
	} {
		d, err := newPublicVoice(tc.provider)
		require.NoError(t, err)
		assert.Equal(t, tc.want, d)
	}

	assert.Equal(t, &codexVoice{createURL: "https://chatgpt.com/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas", sidebandURL: "wss://api.openai.com/v1/live/%s"}, newCodexVoice())

	// The API key never travels in plain text to a non-local host.
	for _, base := range []string{"http://example.com/v1", "ws://example.com", "http://localhost.example.com", "http://127.0.0.1.nip.io:8080", "http://127.0.0.1@example.com", "http://[::ffff:8.8.8.8]"} {
		_, err := newPublicVoice(config.OpenAIConfig{RocketCodeAuth: "api_key", APIBaseURL: base})
		require.ErrorIs(t, err, errVoicePlainHTTP, base)
	}
}

func TestVoiceWireRoundTrip(t *testing.T) {
	instructions, err := json.Marshal(fmt.Sprintf(voiceInstructions, "main"))
	require.NoError(t, err)

	history := []*TranscriptEvent{{Role: "user", Text: "hello", Complete: true}, {Role: "assistant", Text: "hi there", Complete: true}}
	seed := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi there"}]}]`
	session := `"instructions":` + string(instructions) + `,"delegation":{"type":"client"}`

	for _, tc := range []struct {
		auth                       string
		creds                      voiceCredentials
		header                     http.Header
		create, body, location     string
		reply, id, sideband, spoke string
		hangup                     string
		events                     []string
		want                       []voiceEvent
	}{
		{
			auth:     "api_key",
			creds:    voiceCredentials{bearer: "sk-test"},
			header:   http.Header{"Authorization": {"Bearer sk-test"}, "Api-Key": {"sk-test"}},
			create:   "/v1/live/sessions",
			body:     `{"session":{"model":"gpt-live-1",` + session + `,"audio":{"output":{"voice":"marin"}},"input":` + seed + `,"client":{"data_channel":{"allowed_client_events":["session.close","session.input_audio.mute","session.input_audio.unmute"]}}},"transport":{"type":"webrtc","sdp":"offer"}}`,
			reply:    `{"session":{"id":"live_1"},"transport":{"type":"webrtc","sdp":"answer"}}`,
			id:       "live_1",
			sideband: "/v1/live/sessions/live_1/attach",
			hangup:   "/v1/live/sessions/live_1/hangup",
			events: []string{
				`{"type":"session.started","session":{"id":"live_1"}}`,
				`{"type":"session.output_audio.delta","delta":"AAAA","start_ms":1,"end_ms":2}`,
				`{"type":"session.input_transcript.delta","delta":"run the tests","start_ms":100,"end_ms":900}`,
				`{"type":"session.output_transcript.delta","delta":"sure","start_ms":1000,"end_ms":1400}`,
				`{"type":"session.delegation.created","event_id":"e1","offset_ms":1500,"delegation":{"id":"d1","type":"delegation","target":"client"}}`,
				`{"type":"error","error":{"message":"unknown delegation"}}`,
				`{"type":"session.closed","reason":"expired"}`,
				`{"type":"session.mystery"}`,
			},
			want: []voiceEvent{
				{}, {}, {kind: voiceEventInput, text: "run the tests"}, {kind: voiceEventOutput, text: "sure"},
				{kind: voiceEventHandOff, id: "d1"}, {kind: voiceEventError}, {kind: voiceEventClosed, text: "expired"}, {},
			},
			spoke: `{"type":"session.commentary.append","delegation_id":"d1","content":"Done."}`,
		},
		{
			auth:     "chatgpt",
			creds:    voiceCredentials{bearer: "token", accountID: "acct"},
			header:   http.Header{"Authorization": {"Bearer token"}, "Chatgpt-Account-Id": {"acct"}, "Openai-Alpha": {"quicksilver=v2"}, "Originator": {"codex_cli_rs"}},
			create:   "/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas",
			body:     `{"sdp":"offer","session":{"model":"gpt-live-1-codex",` + session + `,"audio":{"output":{"voice":"cove"}},"initial_items":` + seed + `}}`,
			location: "/v1/realtime/calls/rtc_abc",
			reply:    "answer",
			id:       "rtc_abc",
			sideband: "/v1/live/rtc_abc",
			events: []string{
				`{"type":"session.started","session":{"id":"rtc_abc","expires_at":1}}`,
				`{"type":"input_transcript.added","item":{"text":"list the folders"},"start_ms":100,"end_ms":900}`,
				`{"type":"output_transcript.added","item":{"text":"sure"},"start_ms":1000,"end_ms":1300}`,
				`{"type":"turn.done","turn":{"id":"t1","role":"user","transcript":"list the folders","start_ms":100,"end_ms":900}}`,
				`{"type":"delegation.created","item":{"id":"d1","type":"delegation","target":"client","content":[{"type":"input_text","text":"list the"},{"type":"input_text","text":"folders"}]},"offset_ms":1500}`,
				`{"type":"delegation.context.appended"}`,
				`{"type":"session.closed","reason":"client_request"}`,
			},
			want: []voiceEvent{
				{}, {kind: voiceEventInput, text: "list the folders"}, {kind: voiceEventOutput, text: "sure"},
				{kind: voiceEventTurnDone, role: "user", text: "list the folders"}, {kind: voiceEventHandOff, id: "d1", text: "list the\nfolders"}, {}, {kind: voiceEventClosed, text: "client_request"},
			},
			spoke: `{"type":"delegation.context.append","delegation_item_id":"d1","channel":"speakable","content":[{"type":"input_text","text":"Done."}]}`,
		},
	} {
		t.Run(tc.auth, func(t *testing.T) {
			type request struct {
				uri    string
				header http.Header
			}

			requests, messages := make(chan request, 3), make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- request{r.RequestURI, r.Header.Clone()}

				switch r.RequestURI {
				case tc.create:
					body, err := io.ReadAll(r.Body)
					assert.NoError(t, err)
					assert.JSONEq(t, tc.body, string(body))
					w.Header().Set("Location", tc.location)
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, tc.reply)
				case tc.sideband:
					conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if !assert.NoError(t, err) {
						return
					}
					defer func() { _ = conn.Close() }()

					for _, event := range tc.events {
						assert.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(event)))
					}

					for range 2 {
						_, data, err := conn.ReadMessage()
						if !assert.NoError(t, err) {
							return
						}

						messages <- string(data)
					}
				case tc.hangup:
					assert.Equal(t, http.MethodPost, r.Method)
				default:
					t.Errorf("unexpected request %s", r.RequestURI)
				}
			}))
			t.Cleanup(server.Close)

			d := testVoiceDialects(t, server.URL)[tc.auth]
			header := d.header(tc.creds)

			answer, id, err := d.create(t.Context(), header, "offer", "main", d.voices()[0], history)
			require.NoError(t, err)
			assert.Equal(t, []string{"answer", tc.id}, []string{answer, id})

			conn, err := voiceDial(t.Context(), d.sideband(id), header)
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })

			got := make([]voiceEvent, 0, len(tc.want))
			for range tc.want {
				_, data, err := conn.ReadMessage()
				require.NoError(t, err)

				event, err := voiceDecode(data, d)
				require.NoError(t, err)

				got = append(got, event)
			}

			assert.Equal(t, tc.want, got)

			for _, message := range append(d.appends("d1", "Done."), []byte(voiceCloseMessage)) {
				require.NoError(t, conn.WriteMessage(websocket.TextMessage, message))
			}

			assert.JSONEq(t, tc.spoke, <-messages)
			assert.JSONEq(t, `{"type":"session.close"}`, <-messages)

			if tc.hangup != "" {
				require.NoError(t, d.hangup(t.Context(), header, id))
			}

			uris := []string{tc.create, tc.sideband}
			if tc.hangup != "" {
				uris = append(uris, tc.hangup)
			}

			assert.Equal(t, tc.auth == "chatgpt", header.Get("X-Session-Id") != "")

			for _, uri := range uris {
				seen := <-requests
				assert.Equal(t, uri, seen.uri)
				assert.Equal(t, header.Get("X-Session-Id"), seen.header.Get("X-Session-Id"))

				for name, values := range tc.header {
					assert.Equal(t, values, seen.header.Values(name), name)
				}
			}

			assert.Empty(t, requests)
		})
	}
}

func TestVoiceAppends(t *testing.T) {
	euro := func(n int) string {
		return `{"type":"delegation.context.append","delegation_item_id":"d1","channel":"speakable","content":[{"type":"input_text","text":"` + strings.Repeat("€", n) + `"}]}`
	}

	for _, tc := range []struct {
		auth, id, text string
		want           []string
	}{
		// Text is sent whole, in chunks that end on rune boundaries.
		{"chatgpt", "d1", strings.Repeat("€", 700), []string{euro(166), euro(166), euro(166), euro(166), euro(36)}},
		{"api_key", "d1", "Run this:\n```go\nfmt.Println(1)\n```\n| a | b |", []string{`{"type":"session.commentary.append","delegation_id":"d1","content":"Run this:\n` + "```go\\nfmt.Println(1)\\n```\\n| a | b |" + `"}`}},
		// Before any hand-off, text goes to the whole session.
		{"chatgpt", "", "Ship?", []string{`{"type":"session.context.append","channel":"speakable","content":[{"type":"input_text","text":"Ship?"}]}`}},
		{"api_key", "", "Ship?", []string{`{"type":"session.commentary.append","delegation_id":null,"content":"Ship?"}`}},
	} {
		got := map[string]voiceDialect{"api_key": &publicVoice{}, "chatgpt": newCodexVoice()}[tc.auth].appends(tc.id, tc.text)
		require.Len(t, got, len(tc.want), tc.text)

		for i, want := range tc.want {
			assert.JSONEq(t, want, string(got[i]))
		}
	}
}

func TestVoiceSeed(t *testing.T) {
	skipped := []*TranscriptEvent{{Role: "developer", Text: "dev", Complete: true}, {Role: "tool", Text: "secret", Complete: true}, {Role: "reasoning", Text: "think", Complete: true}, {Role: "assistant", Text: "partial"}}

	counted := make([]*TranscriptEvent, 0, 50)

	want := make([]voiceItem, 0, 40)

	for i := range 50 {
		role, part := "user", "input_text"
		if i%2 == 1 {
			role, part = "assistant", "output_text"
		}

		text := fmt.Sprintf("m%02d", i)

		counted = append(counted, &TranscriptEvent{Role: role, Text: text, Complete: true})
		if i >= 10 {
			want = append(want, voiceItem{Type: "message", Role: role, Content: []voiceContent{{Type: part, Text: text}}})
		}
	}

	assert.Equal(t, want, voiceSeed(append(counted, skipped...)))

	long := make([]*TranscriptEvent, 0, 25)
	for range 25 {
		long = append(long, &TranscriptEvent{Role: "user", Text: strings.Repeat("é", 1000), Complete: true})
	}

	cut := voiceSeed(long)
	require.Len(t, cut, 20)
	assert.Equal(t, strings.Repeat("é", 300), cut[0].Content[0].Text)

	coded := voiceSeed([]*TranscriptEvent{{Role: "assistant", Text: "Here:\n```go\nfmt.Println(1)\n```", Complete: true}})
	assert.Equal(t, []voiceContent{{Type: "output_text", Text: "Here:\n```go\nfmt.Println(1)\n```"}}, coded[0].Content, "the seed is sent as written (R14)")
}

// testVoiceDialects are both routes aimed at a test server, by provider auth.
func testVoiceDialects(t *testing.T, serverURL string) map[string]voiceDialect {
	t.Helper()

	public, err := newPublicVoice(config.OpenAIConfig{RocketCodeAuth: "api_key", APIBaseURL: serverURL + "/v1"})
	require.NoError(t, err)

	codex := newCodexVoice()
	codex.createURL = strings.Replace(codex.createURL, "https://chatgpt.com", serverURL, 1)
	codex.sidebandURL = strings.Replace(codex.sidebandURL, "wss://api.openai.com", "ws"+strings.TrimPrefix(serverURL, "http"), 1)

	return map[string]voiceDialect{"api_key": public, "chatgpt": codex}
}

func TestVoiceWireFailures(t *testing.T) {
	for _, tc := range []struct {
		name, auth, location, body string
		status                     int // 0: the server is down
		create, dial, hangup       string
	}{
		{"public rejected", "api_key", "", "upstream-secret", http.StatusUnauthorized, "HTTP 401", "HTTP 401", "HTTP 401"},
		{"codex rejected", "chatgpt", "", "upstream-secret", http.StatusForbidden, "HTTP 403", "HTTP 403", "HTTP 403"},
		{"no call id", "chatgpt", "/v1/realtime/calls/not-a-call", "", http.StatusCreated, errVoiceCallID.Error(), "HTTP 201", "HTTP 201"},
		{"garbled answer", "api_key", "", "not json", http.StatusCreated, "decode voice answer", "HTTP 201", ""},
		{"down", "api_key", "", "", 0, "send voice request", "attach voice sideband", "send voice request"},
		// The websocket dial reports a redirect instead of following it.
		{"redirect loop", "api_key", "/v1/live/sessions", "", http.StatusTemporaryRedirect, "stopped after 10 redirects", "HTTP 307", "stopped after 10 redirects"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", tc.location)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			if tc.status == 0 {
				server.Close()
			}

			t.Cleanup(server.Close)

			d := testVoiceDialects(t, server.URL)[tc.auth]
			header := d.header(voiceCredentials{bearer: "token"})

			_, _, errCreate := d.create(t.Context(), header, "offer", "main", d.voices()[0], nil)
			_, errDial := voiceDial(t.Context(), d.sideband("rtc_abc"), header)
			errHangup := d.hangup(t.Context(), header, "rtc_abc")

			for _, check := range []struct {
				err  error
				want string
			}{{errCreate, tc.create}, {errDial, tc.dial}, {errHangup, tc.hangup}} {
				if check.want == "" {
					require.NoError(t, check.err)
					continue
				}

				require.ErrorContains(t, check.err, check.want)
				assert.NotContains(t, check.err.Error(), "upstream-secret")
			}
		})
	}

	_, err := voiceDecode([]byte("{"), &publicVoice{})
	require.ErrorContains(t, err, "decode voice sideband event")
	assert.Equal(t, "€", voiceTail("€€", 4), "the tail starts on a rune boundary")
}

func TestVoiceRedirects(t *testing.T) {
	type hop struct {
		url, authorization, apiKey, account, body string
	}

	for _, tc := range []struct {
		name string
		hops []string // Each hop redirects to the next with a 307; the last answers.
		kept bool
	}{
		{"same origin", []string{"https://api.test/v1/live/sessions", "https://api.test/v1/moved"}, true},
		{"another port", []string{"https://api.test/v1/live/sessions", "https://api.test:8443/v1/live/sessions"}, false},
		{"plain http", []string{"https://api.test/v1/live/sessions", "http://api.test/v1/live/sessions"}, false},
		{"another host", []string{"https://a.test/v1/live/sessions", "https://b.test/v1/live/sessions", "https://c.test/v1/live/sessions"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen []hop

			base := http.DefaultClient.Transport
			http.DefaultClient.Transport = &mockRoundTripper{RoundTripFunc: func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)

				seen = append(seen, hop{req.URL.String(), req.Header.Get("Authorization"), req.Header.Get("Api-Key"), req.Header.Get("Chatgpt-Account-Id"), string(body)})
				if len(seen) == len(tc.hops) {
					return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody}, nil
				}

				return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {tc.hops[len(seen)]}}, Body: http.NoBody}, nil
			}}

			t.Cleanup(func() { http.DefaultClient.Transport = base })

			resp, err := voicePost(t.Context(), tc.hops[0], http.Header{"Authorization": {"Bearer token"}, "Api-Key": {"token"}, "Chatgpt-Account-Id": {"acct"}}, []byte("seed"))
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())

			// A cross-origin 307 still resends the body, as Codex's client does.
			want := []hop{{tc.hops[0], "Bearer token", "token", "acct", "seed"}}
			for _, next := range tc.hops[1:] {
				if tc.kept {
					want = append(want, hop{next, "Bearer token", "token", "acct", "seed"})
				} else {
					want = append(want, hop{next, "", "", "", "seed"})
				}
			}

			assert.Equal(t, want, seen)
		})
	}
}

func TestVoiceCodexHangupClosesOverSideband(t *testing.T) {
	closes := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer func() { _ = conn.Close() }()

		_, data, err := conn.ReadMessage()
		assert.NoError(t, err)

		closes <- r.URL.Path + " " + string(data)
	}))
	t.Cleanup(server.Close)

	d := testVoiceDialects(t, server.URL)["chatgpt"]
	require.NoError(t, d.hangup(t.Context(), d.header(voiceCredentials{bearer: "token", accountID: "acct"}), "rtc_abc"))
	assert.Equal(t, "/v1/live/rtc_abc "+voiceCloseMessage, <-closes)
}
