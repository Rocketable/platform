package rpc

import (
	"encoding/json"
	"errors"
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
	for _, tc := range []struct {
		provider                    config.OpenAIConfig
		create, sideband, hangupURL string
	}{
		{config.OpenAIConfig{RocketCodeAuth: "api_key"}, "https://api.openai.com/v1/live/sessions", "wss://api.openai.com/v1/live/sessions/%s/attach", "https://api.openai.com/v1/live/sessions/%s/hangup"},
		{config.OpenAIConfig{RocketCodeAuth: "api_key", APIBaseURL: "ws://proxy.test/v1/"}, "http://proxy.test/v1/live/sessions", "ws://proxy.test/v1/live/sessions/%s/attach", "http://proxy.test/v1/live/sessions/%s/hangup"},
		{config.OpenAIConfig{RocketCodeAuth: "chatgpt", APIBaseURL: "https://ignored.test/v1"}, "https://chatgpt.com/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas", "wss://api.openai.com/v1/live/%s", ""},
	} {
		d := newVoiceDialect(tc.provider)
		assert.Equal(t, []string{tc.create, tc.sideband, tc.hangupURL}, []string{d.createURL, d.sideband, d.hangupURL})
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
			header:   http.Header{"Authorization": {"Bearer sk-test"}},
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
				{kind: voiceEventStarted}, {}, {kind: voiceEventInput, text: "run the tests", startMS: 100, endMS: 900}, {kind: voiceEventOutput, text: "sure", startMS: 1000, endMS: 1400},
				{kind: voiceEventHandOff, id: "d1", offsetMS: 1500}, {kind: voiceEventError, text: "unknown delegation"}, {kind: voiceEventClosed, text: "expired"}, {},
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
				{kind: voiceEventStarted}, {kind: voiceEventInput, text: "list the folders", startMS: 100, endMS: 900}, {kind: voiceEventOutput, text: "sure", startMS: 1000, endMS: 1300},
				{kind: voiceEventTurnDone, role: "user", text: "list the folders"}, {kind: voiceEventHandOff, id: "d1", text: "list the\nfolders", offsetMS: 1500}, {}, {kind: voiceEventClosed, text: "client_request"},
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

			d := testVoiceDialect(t, tc.auth, server.URL)
			header := d.header(tc.creds)

			answer, id, err := d.create(t.Context(), header, "offer", "main", history)
			require.NoError(t, err)
			assert.Equal(t, []string{"answer", tc.id}, []string{answer, id})

			conn, err := d.dial(t.Context(), header, id)
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })

			got := make([]voiceEvent, 0, len(tc.want))
			for range tc.want {
				_, data, err := conn.ReadMessage()
				require.NoError(t, err)

				event, err := d.decode(data)
				require.NoError(t, err)

				got = append(got, event)
			}

			assert.Equal(t, tc.want, got)

			for _, message := range append(d.appends("d1", "Done.", true), []byte(voiceCloseMessage)) {
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

func TestVoiceUpstreamRejections(t *testing.T) {
	for _, tc := range []struct {
		auth     string
		status   int
		location string
	}{
		{"api_key", http.StatusUnauthorized, ""},
		{"chatgpt", http.StatusForbidden, ""},
		{"chatgpt", http.StatusCreated, "/v1/realtime/calls/not-a-call"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", tc.location)
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, "upstream-secret")
		}))
		t.Cleanup(server.Close)

		d := testVoiceDialect(t, tc.auth, server.URL)
		header := d.header(voiceCredentials{bearer: "token"})

		_, _, errCreate := d.create(t.Context(), header, "offer", "main", nil)
		_, errDial := d.dial(t.Context(), header, "rtc_abc")

		if tc.status == http.StatusCreated {
			require.ErrorIs(t, errCreate, errVoiceCallID)
			continue
		}

		for _, err := range []error{errCreate, errDial} {
			upstream, ok := errors.AsType[*voiceUpstreamError](err)
			require.True(t, ok, "%v", err)
			assert.Equal(t, tc.status, upstream.status)
			assert.NotContains(t, err.Error(), "upstream-secret")
		}
	}
}

func TestVoiceAppends(t *testing.T) {
	note := `{"type":"delegation.context.append","delegation_item_id":"d1","channel":"commentary","content":[{"type":"input_text","text":"` + voiceCutNote + `"}]}`
	euro := `{"type":"delegation.context.append","delegation_item_id":"d1","channel":"speakable","content":[{"type":"input_text","text":"` + strings.Repeat("€", 166) + `"}]}`

	for _, tc := range []struct {
		auth, id, text string
		spoken         bool
		want           []string
	}{
		{"chatgpt", "d1", strings.Repeat("€", 700), true, []string{euro, euro, euro, note}},
		{"api_key", "d1", "Run this:\n```go\nfmt.Println(1)\n```\n| a | b |\n|---|---|\nDone.", true, []string{`{"type":"session.commentary.append","delegation_id":"d1","content":"Run this:\n(The code is on screen.)\n(The table is on screen.)\nDone."}`}},
		{"api_key", "d1", "old " + strings.Repeat("n", 1500), false, []string{`{"type":"session.thinking.append","delegation_id":"d1","content":"` + strings.Repeat("n", 1500) + `"}`}},
		// Before any hand-off, text goes to the whole session.
		{"chatgpt", "", "Ship?", true, []string{`{"type":"session.context.append","channel":"speakable","content":[{"type":"input_text","text":"Ship?"}]}`}},
		{"chatgpt", "", "Working", false, []string{`{"type":"session.context.append","channel":"commentary","content":[{"type":"input_text","text":"Working"}]}`}},
		{"api_key", "", "Ship?", true, []string{`{"type":"session.commentary.append","delegation_id":null,"content":"Ship?"}`}},
		{"api_key", "", "Working", false, []string{`{"type":"session.thinking.append","delegation_id":null,"content":"Working"}`}},
	} {
		got := newVoiceDialect(config.OpenAIConfig{RocketCodeAuth: tc.auth}).appends(tc.id, tc.text, tc.spoken)
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

	coded := make([]*TranscriptEvent, 0, 100)
	for range 100 {
		coded = append(coded, &TranscriptEvent{Role: "assistant", Text: "Here:\n```\n" + strings.Repeat("x", 5000) + "\n```", Complete: true})
	}

	shaped := voiceSeed(coded)
	require.Len(t, shaped, 40)
	assert.Equal(t, []voiceContent{{Type: "output_text", Text: "Here:\n(The code is on screen.)"}}, shaped[39].Content)
}

func testVoiceDialect(t *testing.T, auth, serverURL string) *voiceDialect {
	t.Helper()

	d := newVoiceDialect(config.OpenAIConfig{RocketCodeAuth: auth, APIBaseURL: serverURL + "/v1"})
	if auth == "chatgpt" {
		d.createURL = strings.Replace(d.createURL, "https://chatgpt.com", serverURL, 1)
		d.sideband = strings.Replace(d.sideband, "wss://api.openai.com", "ws"+strings.TrimPrefix(serverURL, "http"), 1)
	}

	return d
}

func TestVoiceWireFailures(t *testing.T) {
	garbled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "not json")
	}))
	t.Cleanup(garbled.Close)

	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()

	header := http.Header{"Authorization": {"Bearer sk-test"}}

	_, _, err := testVoiceDialect(t, "api_key", garbled.URL).create(t.Context(), header, "offer", "main", nil)
	require.ErrorContains(t, err, "decode voice answer")

	down := testVoiceDialect(t, "api_key", gone.URL)
	_, _, err = down.create(t.Context(), header, "offer", "main", nil)
	require.ErrorContains(t, err, "send voice request")
	require.ErrorContains(t, down.hangup(t.Context(), header, "live_1"), "send voice request")

	_, err = down.dial(t.Context(), header, "live_1")
	require.ErrorContains(t, err, "attach voice sideband")

	_, err = down.decode([]byte("{"))
	require.ErrorContains(t, err, "decode voice sideband event")

	assert.Equal(t, "€", voiceTail("€€", 4), "the tail starts on a rune boundary")
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

	d := testVoiceDialect(t, "chatgpt", server.URL)
	require.NoError(t, d.hangup(t.Context(), d.header(voiceCredentials{bearer: "token", accountID: "acct"}), "rtc_abc"))
	assert.Equal(t, "/v1/live/rtc_abc "+voiceCloseMessage, <-closes)
}
