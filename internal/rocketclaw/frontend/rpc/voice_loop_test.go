package rpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/metadata"
)

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

func startVoiceLoop(t *testing.T, auth string, engine *mockBackend, stream *mockServerStream, snapshots <-chan voiceSnapshot, refusals <-chan int) (
	first *websocket.Conn, peers <-chan *websocket.Conn, cancel context.CancelCauseFunc, wait func() voiceEndError,
) {
	t.Helper()

	listener, upgraded := pipeListener(make(chan net.Conn, 1)), make(chan *websocket.Conn, 8)
	sideband := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case code := <-refusals:
			if code == 0 {
				<-r.Context().Done()
				return
			}

			w.WriteHeader(code)

			return
		default:
		}

		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		assert.NoError(t, err)

		upgraded <- conn
	}), ReadHeaderTimeout: time.Second}

	var serving errgroup.Group
	serving.Go(func() error { _ = sideband.Serve(listener); return nil })
	t.Cleanup(func() { require.NoError(t, sideband.Close()); require.NoError(t, serving.Wait()) })

	dialer := websocket.DefaultDialer
	websocket.DefaultDialer = &websocket.Dialer{NetDialContext: func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		listener <- server

		return client, nil
	}}

	t.Cleanup(func() { websocket.DefaultDialer = dialer })

	dialect := map[string]voiceDialect{"api_key": &publicVoice{sidebandURL: "ws://voice/%s"}, "chatgpt": &codexVoice{sidebandURL: "ws://voice/%s"}}[auth]

	conn, err := voiceDial(t.Context(), dialect.sideband("call"), nil)
	require.NoError(t, err)

	stream.ContextFunc = func() context.Context {
		return metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-tailnet-login-bin", "alice@example.com", "rocketclaw-tailnet-name-bin", "alice"))
	}
	call := &voiceCall{server: &Server{backend: engine, cfg: new(config.LockedConfig)}, dialect: dialect, id: "call", conn: conn, stream: stream, conversationID: "chat", principal: "alice", username: "alice@example.com"}
	ctx, cancel := context.WithCancelCause(t.Context())

	calls, groupCtx := errgroup.WithContext(ctx)
	calls.Go(func() error { return call.loop(groupCtx, calls, snapshots) })

	return <-upgraded, upgraded, cancel, func() voiceEndError {
		end, ok := errors.AsType[voiceEndError](calls.Wait())
		require.True(t, ok)

		return end
	}
}

func TestVoiceLoopEnds(t *testing.T) {
	for _, tc := range []struct {
		name, auth string
		closed     string
		drop       bool
		normal     bool
		cause      error
		quiet      bool
		reason     voiceEndError
	}{
		{name: "expired", auth: "api_key", closed: "expired", reason: "expired"},
		{name: "safety", auth: "api_key", closed: "content", reason: "safety"},
		{name: "closed", auth: "chatgpt", closed: "client_request", reason: "closed"},
		{name: "closed then dropped", auth: "chatgpt", closed: "client_request", drop: true, reason: "closed"},
		{name: "normal close", auth: "chatgpt", normal: true, reason: "closed"},
		{name: "stopped", auth: "api_key", cause: context.Canceled, reason: "stopped"},
		{name: "stopped quiet peer", auth: "chatgpt", cause: context.Canceled, quiet: true, reason: "stopped"},
		{name: "replaced", auth: "api_key", cause: voiceEndError("replaced"), reason: "replaced"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				peer, peers, cancel, wait := startVoiceLoop(t, tc.auth, &mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning}, &mockServerStream{}, nil, nil)
				for _, event := range []string{`{`, `{"type":"session.output_audio.delta","delta":"AAAA"}`, `{"type":"error","error":{"message":"unknown delegation"}}`} {
					require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(event)))
				}

				time.Sleep(time.Hour)
				synctest.Wait()

				switch {
				case tc.normal:
					require.NoError(t, peer.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")))
				case tc.closed != "":
					require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed","reason":"`+tc.closed+`"}`)))
				default:
					cancel(tc.cause)

					_, data, err := peer.ReadMessage()
					require.NoError(t, err)
					assert.JSONEq(t, voiceCloseMessage, string(data))

					closing := time.Now()

					if !tc.quiet {
						require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed","reason":"client_request"}`)))
					}

					_, _, err = peer.ReadMessage()
					require.Error(t, err, "the socket closes after session.close")

					if tc.quiet {
						assert.Equal(t, 3*time.Second, time.Since(closing), "a quiet peer gets the close budget")
					}
				}

				switch {
				case tc.drop:
					require.NoError(t, peer.Close())
				case tc.closed != "" || tc.normal:
					_, _, err := peer.ReadMessage()
					require.Error(t, err, "a closed session gets no session.close")
				}

				assert.Equal(t, tc.reason, wait())
				assert.Equal(t, "voice call ended: "+string(tc.reason), tc.reason.Error())

				time.Sleep(time.Minute)
				assert.Empty(t, peers, "an ending call never reconnects")
			})
		})
	}
}

func voiceDelegation(input, delta string) string {
	return "<realtime_delegation>\n  <input>" + input + "</input>\n  <transcript_delta>" + delta + "</transcript_delta>\n</realtime_delegation>"
}

func TestVoiceLoopHandOffs(t *testing.T) {
	long, start, end := strings.Repeat("x", 5000), strings.Repeat("x", 4093)+"…", "…"+strings.Repeat("x", 4093)

	for _, tc := range []struct {
		name, auth string
		events     []string
		want       [][2]string
	}{
		{"public", "api_key", []string{
			`{"type":"session.input_transcript.delta","delta":"run the"}`,
			`{"type":"session.delegation.created","delegation":{"id":"d1"}}`,
			`{"type":"session.delegation.created","delegation":{"id":"d1"}}`,
			`{"type":"session.output_transcript.delta","delta":"On it."}`,
			`{"type":"session.input_transcript.delta","delta":"tests"}`,
			`{"type":"session.delegation.created","delegation":{"id":"d2"}}`,
			`{"type":"session.delegation.created","delegation":{"id":"d3"}}`,
			`{"type":"session.input_transcript.delta","delta":"` + long + `"}`,
			`{"type":"session.delegation.created","delegation":{"id":"d4"}}`,
			`{"type":"session.input_transcript.delta","delta":"thanks"}`,
		}, [][2]string{
			{"d1", voiceDelegation("user: run the", "user: run the")},
			{"d2", voiceDelegation("assistant: On it.\nuser: tests", "assistant: On it.\nuser: tests")},
			{"d4", voiceDelegation("user: "+strings.Repeat("x", 4087)+"…", end)},
		}},
		{"codex", "chatgpt", []string{
			`{"type":"turn.done","turn":{"role":"assistant","transcript":"Run the tests?"}}`,
			`{"type":"input_transcript.added","item":{"text":"sure"}}`,
			`{"type":"turn.done","turn":{"role":"user","transcript":"sure"}}`,
			`{"type":"delegation.created","item":{"id":"d1","content":[{"type":"input_text","text":"sure"}]}}`,
			`{"type":"delegation.created","item":{"id":"d1","content":[{"type":"input_text","text":"sure"}]}}`,
			`{"type":"delegation.created","item":{"id":"d2","content":[{"type":"input_text","text":"lint & tests"}]}}`,
			`{"type":"turn.done","turn":{"role":"user","transcript":"` + long + `"}}`,
			`{"type":"delegation.created","item":{"id":"d3","content":[{"type":"input_text","text":"` + long + `"}]}}`,
			`{"type":"output_transcript.added","item":{"text":"Tests "}}`,
			`{"type":"output_transcript.added","item":{"text":"or lint?"}}`,
			`{"type":"input_transcript.added","item":{"text":"both"}}`,
			`{"type":"turn.done","turn":{"role":"assistant","transcript":"Tests or lint?"}}`,
			`{"type":"delegation.created","item":{"id":"d4","content":[{"type":"input_text","text":"both"}]}}`,
			`{"type":"turn.done","turn":{"role":"user","transcript":"thanks"}}`,
		}, [][2]string{
			{"d1", voiceDelegation("sure", "assistant: Run the tests?\nuser: sure")},
			{"d2", voiceDelegation("lint &amp; tests", "user: lint &amp; tests")},
			{"d3", voiceDelegation(start, end)},
			{"d4", voiceDelegation("both", "assistant: Tests or lint?\nuser: both")},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				engine := &mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning, AgentPausedFunc: notPaused, StashQueueItemFunc: func(context.Context, string, *protocol.ThreadQueueItem) error { return nil }}
				peer, _, cancel, wait := startVoiceLoop(t, tc.auth, engine, &mockServerStream{}, nil, nil)

				for _, event := range tc.events {
					require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(event)))
				}

				synctest.Wait()

				got := make([][2]string, 0, len(engine.StashQueueItemCalls()))

				for _, call := range engine.StashQueueItemCalls() {
					item, inbound := call.ThreadQueueItem, call.ThreadQueueItem.Inbound
					got = append(got, [2]string{item.ID, inbound.Text})

					assert.Equal(t, []any{"chat", protocol.InboundKindSteer, protocol.SourceWeb, "alice", inbound.Text}, []any{call.S, item.Kind, item.Source, item.Principal, item.Message}, item.ID)
					assert.Equal(t, []any{"chat", protocol.InboundKindSteer, protocol.SourceWeb, true, "alice", "Voice", item.ID}, []any{inbound.ConversationID, inbound.Kind, inbound.Source, inbound.Human, inbound.Metadata[protocol.InboundPrincipalMetadataKey], inbound.Metadata[protocol.InboundMediaMetadataKey], inbound.Metadata["web_message_id"]}, item.ID)
					assert.Equal(t, []string{"alice", "alice@example.com"}, []string{inbound.Metadata[protocol.InboundPrincipalNameMetadataKey], inbound.Metadata[protocol.InboundPrincipalEmailMetadataKey]}, "the hand-off names the caller")
				}

				assert.Equal(t, tc.want, got)

				cancel(nil)

				_, _, err := peer.ReadMessage()
				require.NoError(t, err)
				require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed"}`)))
				assert.Equal(t, voiceEndError("stopped"), wait())
			})
		})
	}
}

func TestVoiceLoopReplies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		snapshots := make(chan voiceSnapshot)
		engine := &mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning, AgentPausedFunc: notPaused, StashQueueItemFunc: func(context.Context, string, *protocol.ThreadQueueItem) error { return nil }}
		stream := &mockServerStream{SendMsgFunc: func(any) error { return nil }}
		peer, _, cancel, wait := startVoiceLoop(t, "api_key", engine, stream, snapshots, nil)
		dialect := &publicVoice{}

		handOff := func(id string) {
			for _, event := range []string{`{"type":"session.input_transcript.delta","delta":"go"}`, `{"type":"session.delegation.created","delegation":{"id":"` + id + `"}}`} {
				require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(event)))
			}

			synctest.Wait()
		}

		expect := func(snapshot voiceSnapshot, want ...[]byte) {
			snapshots <- snapshot

			for _, message := range want {
				_, data, err := peer.ReadMessage()
				require.NoError(t, err)
				assert.Equal(t, string(message), string(data))
			}

			synctest.Wait()
		}

		for _, id := range []string{"d1", "d2", "d3", "d4", "d5", "d6", "d7"} {
			handOff(id)
		}

		ship := protocol.AskUserQuestionRequest{ID: "q1", Question: "Ship?", Options: []protocol.AskUserQuestionOption{{Label: "Yes"}, {Label: "No"}}}
		deploy := protocol.AskUserQuestionRequest{ID: "q2", Question: "Deploy?", Details: "To production."}
		turns := []voiceTurn{
			{key: "before", inputs: []string{"typed"}, reply: "Earlier reply."},
			{key: "t1", inputs: []string{"d1", "typed"}, reply: "Superseded."},
			{key: "t2", inputs: []string{"typed", "d2"}, reply: strings.Repeat("a", 3960)},
			{key: "t3", inputs: []string{"d3"}, reply: "Done."},
			{key: "t4", inputs: []string{"d4"}, reply: strings.Repeat("b", 3961)},
			{key: "t5", inputs: []string{"d5", "d6"}, reply: "Both."},
		}

		typedBy := map[string]string{"typed": "alice", "typed-during": "alice", "bob-typed": "bob", "bob-steer": "bob"}
		expect(voiceSnapshot{turns: turns, questions: []protocol.AskUserQuestionRequest{ship}, typedBy: typedBy},
			slices.Concat(
				dialect.appendMessages(nil, "d7", "I need your input. Please respond in the app.\n\nShip?\nOptions: Yes, No"),
				dialect.appendMessages(nil, "d2", strings.Repeat("a", 3960)),
				dialect.appendMessages(nil, "d3", "Done."),
				dialect.appendMessages(nil, "d4", voiceLongReply),
				dialect.appendMessages(nil, "d5", "Both."),
			)...)
		require.Len(t, stream.SendMsgCalls(), 1, "what was typed before the call mutes nothing")
		assert.Equal(t, strings.Repeat("b", 3961), stream.SendMsgCalls()[0].M.(*VoiceEvent).GetReply())

		later := []voiceTurn{{key: "t6", inputs: []string{"d7"}, reply: "Stale."}, {key: "t7", inputs: []string{"typed-during"}, reply: "Typed reply."}}
		expect(voiceSnapshot{turns: slices.Concat(turns, later), questions: []protocol.AskUserQuestionRequest{ship, deploy}, typedBy: typedBy},
			dialect.appendMessages(nil, "d7", "I need your input. Please respond in the app.\n\nDeploy?\nTo production.")...)
		expect(voiceSnapshot{turns: slices.Concat(turns, later), typedBy: typedBy})
		require.Len(t, stream.SendMsgCalls(), 2)
		assert.True(t, stream.SendMsgCalls()[1].M.(*VoiceEvent).GetTyped())

		handOff("d8")
		assert.Len(t, engine.StashQueueItemCalls(), 8)
		assert.Equal(t, voiceDelegation("user: go", "user: go"), engine.StashQueueItemCalls()[7].ThreadQueueItem.Message)
		assert.Empty(t, engine.AnswerQuestionCalls())

		handOff("d9")

		colleague := voiceSnapshot{turns: slices.Concat(turns, later, []voiceTurn{
			{key: "t8", inputs: []string{"d8"}, reply: "Still read."},
			{key: "t9", inputs: []string{"bob-typed"}, reply: "Bob's reply."},
			{key: "t10", inputs: []string{"d9", "bob-steer"}, reply: "Read anyway."},
		}), typedBy: typedBy}
		expect(colleague, slices.Concat(dialect.appendMessages(nil, "d8", "Still read."), dialect.appendMessages(nil, "d9", "Read anyway."))...)
		expect(colleague)
		assert.Len(t, stream.SendMsgCalls(), 2, "another person's messages cancel and mute nothing")

		cancel(nil)

		_, data, err := peer.ReadMessage()
		require.NoError(t, err)
		assert.JSONEq(t, voiceCloseMessage, string(data), "each reply is read once")
		require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed"}`)))
		assert.Equal(t, voiceEndError("stopped"), wait())
	})
}

func TestVoiceLoopAdmission(t *testing.T) {
	const refused = "The agent could not accept that request. Try again."

	for _, tc := range []struct {
		name     string
		refusal  error
		handOffs []string
		notices  []string
		reason   voiceEndError
	}{
		{"refused", errors.New("message ID was already accepted with different content"), []string{"d1", "d2"}, []string{refused, refused}, "stopped"},
		{"bridge stopped", fmt.Errorf("enqueue Web prompt: %w", protocol.ErrBridgeStopped), []string{"d1"}, []string{}, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				engine := &mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning, AgentPausedFunc: notPaused, StashQueueItemFunc: func(context.Context, string, *protocol.ThreadQueueItem) error { return tc.refusal }}
				stream := &mockServerStream{SendMsgFunc: func(any) error { return nil }}
				peer, _, cancel, wait := startVoiceLoop(t, "api_key", engine, stream, nil, nil)

				for _, id := range tc.handOffs {
					require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.input_transcript.delta","delta":"go"}`)))
					require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.delegation.created","delegation":{"id":"`+id+`"}}`)))
				}

				if tc.reason == "stopped" {
					synctest.Wait()
					cancel(nil)
				}

				_, data, err := peer.ReadMessage()
				require.NoError(t, err)
				assert.JSONEq(t, voiceCloseMessage, string(data))
				require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed"}`)))
				assert.Equal(t, tc.reason, wait())

				notices := make([]string, 0, len(stream.SendMsgCalls()))
				for _, call := range stream.SendMsgCalls() {
					notices = append(notices, call.M.(*VoiceEvent).GetNotice())
				}

				assert.Equal(t, tc.notices, notices)
				assert.Len(t, engine.StashQueueItemCalls(), len(tc.handOffs))
			})
		})
	}
}

func TestVoiceLoopResendsAfterDrop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		snapshots := make(chan voiceSnapshot)
		engine := &mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning, AgentPausedFunc: notPaused, StashQueueItemFunc: func(context.Context, string, *protocol.ThreadQueueItem) error { return nil }}
		peer, peers, cancel, wait := startVoiceLoop(t, "chatgpt", engine, &mockServerStream{SendMsgFunc: func(any) error { return nil }}, snapshots, nil)
		dialect := newCodexVoice()

		require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"delegation.created","item":{"id":"d1","content":[{"type":"input_text","text":"go"}]}}`)))
		synctest.Wait()

		reply := voiceSnapshot{turns: []voiceTurn{{key: "t1", inputs: []string{"d1"}, reply: strings.Repeat("a", 500) + strings.Repeat("b", 500) + "c"}}}
		chunks := dialect.appendMessages(nil, "d1", reply.turns[0].reply)
		require.Len(t, chunks, 3)

		snapshots <- reply

		_, data, err := peer.ReadMessage()
		require.NoError(t, err)
		assert.Equal(t, string(chunks[0]), string(data))

		require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"delegation.created","item":{"id":"d2","content":[{"type":"input_text","text":"lint"}]}}`)))
		synctest.Wait()
		require.NoError(t, peer.Close())

		time.Sleep(time.Second)
		synctest.Wait()
		require.Len(t, peers, 1, "the call reconnects")

		peer = <-peers

		for _, want := range chunks[1:] {
			_, data, err := peer.ReadMessage()
			require.NoError(t, err)
			assert.Equal(t, string(want), string(data))
		}

		snapshots <- reply

		cancel(nil)

		_, data, err = peer.ReadMessage()
		require.NoError(t, err)
		assert.JSONEq(t, voiceCloseMessage, string(data), "nothing else was sent")

		require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed"}`)))
		assert.Equal(t, voiceEndError("stopped"), wait())

		admitted := make([]string, 0, 2)
		for _, call := range engine.StashQueueItemCalls() {
			admitted = append(admitted, call.ThreadQueueItem.ID)
		}

		assert.Equal(t, []string{"d1", "d2"}, admitted, "a hand-off read before the drop is admitted")
	})
}

func TestVoiceLoopReconnect(t *testing.T) {
	for _, tc := range []struct {
		name      string
		refusals  []int
		stop      bool
		stall     bool
		code      int
		reconnect bool
		reason    voiceEndError
	}{
		{name: "not found", refusals: []int{http.StatusNotFound}, reason: "closed"},
		{name: "going away", refusals: []int{http.StatusNotFound}, code: websocket.CloseGoingAway, reason: "closed"},
		{name: "gone", refusals: []int{http.StatusGone}, reason: "closed"},
		{name: "refused five times", refusals: slices.Repeat([]int{http.StatusInternalServerError}, 5), reason: "lost"},
		{name: "refused once", refusals: []int{http.StatusInternalServerError}, reconnect: true, reason: "stopped"},
		{name: "stopped during backoff", stop: true, reason: "stopped"},
		{name: "stopped during a stalled handshake", refusals: []int{0}, stall: true, reason: "stopped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				refusals, stream := make(chan int, len(tc.refusals)), &mockServerStream{SendMsgFunc: func(any) error { return nil }}
				peer, peers, cancel, wait := startVoiceLoop(t, "chatgpt", &mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning}, stream, nil, refusals)

				for _, code := range tc.refusals {
					refusals <- code
				}

				if tc.code != 0 {
					require.NoError(t, peer.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(tc.code, "")))
				}

				require.NoError(t, peer.Close())
				synctest.Wait()

				if tc.stop {
					cancel(nil)
				}

				if tc.stall {
					time.Sleep(time.Second)
					cancel(nil)
				}

				stopped := time.Now()

				if tc.reconnect {
					peer = <-peers

					synctest.Wait()

					cancel(nil)

					_, data, err := peer.ReadMessage()
					require.NoError(t, err)
					assert.JSONEq(t, voiceCloseMessage, string(data))
					require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed"}`)))
				}

				assert.Equal(t, tc.reason, wait())

				if tc.stall {
					assert.Zero(t, time.Since(stopped), "Stop ends a stalled dial at once")
				}

				reconnecting := make([]bool, 0, len(stream.SendMsgCalls()))
				for _, call := range stream.SendMsgCalls() {
					reconnecting = append(reconnecting, call.M.(*VoiceEvent).GetReconnecting())
				}

				want := []bool{true}
				if tc.reconnect {
					want = append(want, false)
				}

				assert.Equal(t, want, reconnecting)

				time.Sleep(time.Minute)
				assert.Empty(t, refusals, "every refusal was dialed")
				assert.Empty(t, peers, "nothing dials after the call ends")
			})
		})
	}
}

func TestVoiceLoopBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		peer, peers, cancel, wait := startVoiceLoop(t, "chatgpt", &mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning}, &mockServerStream{SendMsgFunc: func(any) error { return nil }}, nil, nil)

		drop := func(want time.Duration) {
			dropped := time.Now()

			require.NoError(t, peer.Close())

			peer = <-peers

			assert.Equal(t, want, time.Since(dropped))
			synctest.Wait()
		}

		for _, want := range []time.Duration{200, 400, 800, 1600, 3200, 5000, 5000} {
			drop(want * time.Millisecond)
		}

		time.Sleep(30 * time.Second)
		drop(200 * time.Millisecond)

		cancel(nil)

		_, _, err := peer.ReadMessage()
		require.NoError(t, err)
		require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed"}`)))
		assert.Equal(t, voiceEndError("stopped"), wait())
	})
}

func TestVoiceLoopStopDuringAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		admitting, release := make(chan struct{}), make(chan struct{})
		engine := &mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning, AgentPausedFunc: notPaused, StashQueueItemFunc: func(ctx context.Context, _ string, _ *protocol.ThreadQueueItem) error {
			close(admitting)
			<-release

			assert.NoError(t, ctx.Err(), "Stop never cancels an admission, so its turn runs to completion")

			return nil
		}}
		peer, _, cancel, wait := startVoiceLoop(t, "api_key", engine, &mockServerStream{}, nil, nil)

		require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.input_transcript.delta","delta":"go"}`)))
		require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.delegation.created","delegation":{"id":"d1"}}`)))
		<-admitting
		cancel(nil)
		synctest.Wait()
		close(release)

		_, data, err := peer.ReadMessage()
		require.NoError(t, err)
		assert.JSONEq(t, voiceCloseMessage, string(data))
		require.NoError(t, peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed"}`)))
		assert.Equal(t, voiceEndError("stopped"), wait())
	})
}
