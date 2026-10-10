package rpc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

// pipeListener hands net.Pipe ends to an http.Server, so a gorilla Upgrader can
// serve the fake sideband inside a synctest bubble.
type pipeListener chan net.Conn

func (l pipeListener) Accept() (net.Conn, error) {
	conn, ok := <-l
	if !ok {
		return nil, net.ErrClosed
	}

	return conn, nil
}

func (l pipeListener) Close() error {
	close(l)
	return nil
}

func (l pipeListener) Addr() net.Addr {
	return &net.UnixAddr{Name: "pipe", Net: "unix"}
}

// startVoiceLoop runs a call's reader and loop over a net.Pipe sideband. It returns the
// fake OpenAI end, the call's cancel, and a wait that returns the call group's error.
func startVoiceLoop(t *testing.T, auth string, engine *mockBackend, snapshots <-chan voiceSnapshot) (*websocket.Conn, context.CancelCauseFunc, func() voiceEndError) {
	t.Helper()

	client, server := net.Pipe()

	listener, peers := pipeListener(make(chan net.Conn, 1)), make(chan *websocket.Conn, 1)
	listener <- server

	sideband := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		assert.NoError(t, err)

		peers <- conn
	}), ReadHeaderTimeout: time.Second}

	var serving errgroup.Group
	serving.Go(func() error { _ = sideband.Serve(listener); return nil })

	dialer := websocket.Dialer{NetDialContext: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	conn, resp, err := dialer.DialContext(t.Context(), "ws://voice/", nil)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	peer := <-peers

	require.NoError(t, sideband.Close())
	require.NoError(t, serving.Wait())

	call := &voiceCall{server: &Server{backend: engine}, dialect: newVoiceDialect(config.OpenAIConfig{RocketCodeAuth: auth}), conn: conn, conversationID: "chat", principal: "alice"}
	ctx, cancel := context.WithCancelCause(t.Context())
	events, done := make(chan voiceEvent), make(chan struct{})

	var handoffs errgroup.Group

	calls, groupCtx := errgroup.WithContext(ctx)
	calls.Go(func() error { return call.read(events, done) })
	calls.Go(func() error { return call.loop(groupCtx, events, snapshots, done, &handoffs) })

	return peer, cancel, func() voiceEndError {
		end, ok := errors.AsType[voiceEndError](calls.Wait())
		require.True(t, ok)
		require.NoError(t, handoffs.Wait())
		require.NoError(t, peer.Close())

		return end
	}
}

func TestVoiceLoopEnds(t *testing.T) {
	for _, tc := range []struct {
		name, auth string
		events     []string
		cause      error
		reply      bool
		after      time.Duration
		reason     voiceEndError
	}{
		{name: "no media", auth: "api_key", events: []string{`{"type":"session.started"}`}, reply: true, after: time.Minute, reason: "no_media"},
		{name: "idle", auth: "api_key", events: []string{`{"type":"session.started"}`, `{"type":"session.input_transcript.delta","delta":"hi"}`}, reply: true, after: 5 * time.Minute, reason: "idle"},
		{name: "idle after input", auth: "chatgpt", events: []string{`{"type":"input_transcript.added","item":{"text":"hi"}}`}, reply: true, after: 5 * time.Minute, reason: "idle"},
		{name: "stopped quiet peer", auth: "chatgpt", cause: context.Canceled, reason: "stopped"},
		{name: "replaced", auth: "api_key", cause: voiceEndError("replaced"), reply: true, reason: "replaced"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				peer, cancel, wait := startVoiceLoop(t, tc.auth, &mockBackend{}, nil)
				start := time.Now()

				for _, event := range tc.events {
					require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(event)))
				}

				if tc.cause != nil {
					synctest.Wait()
					cancel(tc.cause)
				}

				_, data, err := peer.ReadMessage()
				require.NoError(t, err)
				assert.JSONEq(t, voiceCloseMessage, string(data))
				assert.Equal(t, tc.after, time.Since(start))

				closing := time.Now()

				if tc.reply {
					require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed","reason":"client_request"}`)))
				}

				_, _, err = peer.ReadMessage()
				require.Error(t, err, "the socket closes after session.close")

				if !tc.reply {
					assert.Equal(t, 3*time.Second, time.Since(closing), "a quiet peer gets the close budget")
				}

				assert.Equal(t, tc.reason, wait())
			})
		})
	}
}

func TestVoiceLoopClosedByOpenAI(t *testing.T) {
	for reason, want := range map[string]voiceEndError{"expired": "expired", "content": "safety", "client_request": "closed"} {
		synctest.Test(t, func(t *testing.T) {
			peer, _, wait := startVoiceLoop(t, "api_key", &mockBackend{}, nil)
			// Malformed, ignored and rejected-append events are skipped without ending the call.
			for _, event := range []string{`{`, `{"type":"session.output_audio.delta","delta":"AAAA"}`, `{"type":"error","error":{"message":"unknown delegation"}}`, `{"type":"session.closed","reason":"` + reason + `"}`} {
				require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(event)))
			}

			_, _, err := peer.ReadMessage()
			require.Error(t, err, "a closed session gets no session.close")
			assert.Equal(t, want, wait())
			assert.Equal(t, "voice call ended: "+string(want), want.Error())
		})
	}

	synctest.Test(t, func(t *testing.T) {
		peer, _, wait := startVoiceLoop(t, "chatgpt", &mockBackend{}, nil)
		require.NoError(t, peer.Close())
		assert.Equal(t, voiceEndError("closed"), wait(), "losing the sideband ends the call")
	})
}

func TestVoiceLoopHandOffMessage(t *testing.T) {
	const want = "sure\n\nVoice conversation:\n> assistant: Run the tests?\n> user: sure"

	for _, tc := range []struct {
		auth   string
		events []string
	}{
		{"api_key", []string{
			`{"type":"session.output_transcript.delta","delta":"Run the tests?","start_ms":100,"end_ms":900}`,
			`{"type":"session.input_transcript.delta","delta":"su","start_ms":1000,"end_ms":1100}`,
			`{"type":"session.input_transcript.delta","delta":"re","start_ms":1100,"end_ms":1300}`,
			`{"type":"session.delegation.created","offset_ms":1400,"delegation":{"id":"d1"}}`,
			`{"type":"session.output_transcript.delta","delta":"On it.","start_ms":1500,"end_ms":1800}`,
			`{"type":"session.delegation.created","offset_ms":1400,"delegation":{"id":"d1"}}`,
			`{"type":"session.input_transcript.delta","delta":"thanks","start_ms":2000,"end_ms":2300}`,
		}},
		{"chatgpt", []string{
			`{"type":"turn.done","turn":{"role":"assistant","transcript":"Run the tests?"}}`,
			`{"type":"input_transcript.added","item":{"text":"sure"},"start_ms":1000,"end_ms":1300}`,
			`{"type":"turn.done","turn":{"role":"user","transcript":"sure"}}`,
			`{"type":"delegation.created","item":{"id":"d1","content":[{"type":"input_text","text":"sure"}]},"offset_ms":1400}`,
			`{"type":"delegation.created","item":{"id":"d1","content":[{"type":"input_text","text":"sure"}]},"offset_ms":1400}`,
			`{"type":"turn.done","turn":{"role":"user","transcript":"thanks"}}`,
		}},
		{"api_key", []string{
			`{"type":"session.output_transcript.delta","delta":"Run the tests?","start_ms":100,"end_ms":900}`,
			`{"type":"session.input_transcript.delta","delta":"sure","start_ms":1000,"end_ms":1300}`,
			`{"type":"session.delegation.created","offset_ms":1400,"delegation":{"id":"d1"}}`,
		}},
	} {
		synctest.Test(t, func(t *testing.T) {
			inbounds := make(chan *protocol.InboundMessage, 4)
			peer, cancel, wait := startVoiceLoop(t, tc.auth, &mockBackend{RunTurnFunc: func(ctx context.Context, inbound *protocol.InboundMessage) error {
				assert.NoError(t, ctx.Err(), "a hand-off outlives its call")

				inbounds <- inbound

				return nil
			}}, nil)

			for _, event := range tc.events {
				require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(event)))
			}

			time.Sleep(2 * time.Second) // A public hand-off waits at most this long for late words.
			synctest.Wait()
			require.Len(t, inbounds, 1, "a re-delivered hand-off and later talk start nothing")

			inbound := <-inbounds
			assert.Equal(t, want, inbound.Text)
			assert.Equal(t, protocol.InboundKindSteer, inbound.Kind)
			assert.Equal(t, protocol.SourceWeb, inbound.Source)
			assert.True(t, inbound.Human)
			assert.Equal(t, []string{"alice", "Voice", "d1"}, []string{inbound.Metadata[protocol.InboundPrincipalMetadataKey], inbound.Metadata[protocol.InboundMediaMetadataKey], inbound.Metadata["web_message_id"]})

			cancel(nil)

			_, _, err := peer.ReadMessage()
			require.NoError(t, err)
			require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed"}`)))
			assert.Equal(t, voiceEndError("stopped"), wait())
		})
	}
}

func TestVoiceLoopAnswersReadOutQuestions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		snapshots, answers, inbounds := make(chan voiceSnapshot), make(chan string, 4), make(chan string, 4)
		peer, cancel, wait := startVoiceLoop(t, "chatgpt", &mockBackend{
			AnswerQuestionFunc: func(_ context.Context, conversation, id string, answer protocol.AskUserQuestionAnswer) error {
				answers <- id + ":" + answer.Custom

				if id == "q1" { // Answered from the browser card first.
					return &backend.QuestionNotPendingError{ConversationID: conversation, QuestionID: id}
				}

				return nil
			},
			RunTurnFunc: func(_ context.Context, inbound *protocol.InboundMessage) error {
				inbounds <- inbound.Metadata["web_message_id"]
				return nil
			},
		}, snapshots)

		latest := "" // Questions are spoken under the newest hand-off.

		for _, tc := range []struct {
			question, spoken, request string
			answer, turn              string
		}{
			{"q1", "The agent asks: Ship?\nOptions: Yes, No", "yes", "q1:yes", "d1"},
			{"q2", "The agent asks: Deploy?\nOptions: Yes, No", "go", "q2:go", ""},
			{"q2", "", "later", "", "d3"},
		} {
			snapshots <- voiceSnapshot{questions: []protocol.AskUserQuestionRequest{{ID: tc.question, Question: map[string]string{"q1": "Ship?", "q2": "Deploy?"}[tc.question], Options: []protocol.AskUserQuestionOption{{Label: "Yes"}, {Label: "No"}}}}}

			if tc.spoken != "" {
				_, data, err := peer.ReadMessage()
				require.NoError(t, err)
				assert.Equal(t, string(newVoiceDialect(config.OpenAIConfig{RocketCodeAuth: "chatgpt"}).appends(latest, tc.spoken, true)[0]), string(data))
			}

			id := map[string]string{"yes": "d1", "go": "d2", "later": "d3"}[tc.request]
			latest = id
			require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"delegation.created","item":{"id":"`+id+`","content":[{"type":"input_text","text":"`+tc.request+`"}]},"offset_ms":1}`)))
			synctest.Wait()

			got := []string{"", ""}
			if len(answers) > 0 {
				got[0] = <-answers
			}

			if len(inbounds) > 0 {
				got[1] = <-inbounds
			}

			assert.Equal(t, []string{tc.answer, tc.turn}, got, "a question is answered once; a missed answer steers")
		}

		cancel(nil)

		_, data, err := peer.ReadMessage()
		require.NoError(t, err)
		assert.JSONEq(t, voiceCloseMessage, string(data), "a question still listed is not read out again")
		require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed"}`)))
		assert.Equal(t, voiceEndError("stopped"), wait())
	})
}
