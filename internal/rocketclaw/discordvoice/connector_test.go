package discordvoice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/openaiaudio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeDiscordWire struct {
	played   [][]byte
	errPlay  error
	errClose error
	closed   bool
}

func (f *fakeDiscordWire) play(_ context.Context, frames opusFrames) (int, error) {
	if f.errPlay != nil {
		return 0, f.errPlay
	}

	sent := 0
	err := frames(func(frame []byte) error {
		f.played = append(f.played, slices.Clone(frame))
		sent++

		return nil
	})

	return sent, err
}

func (f *fakeDiscordWire) close() error {
	f.closed = true
	return f.errClose
}

func TestStopClosesWireAndInterruptsPlayback(t *testing.T) {
	connector := newTestConnector()
	wire := &fakeDiscordWire{}
	connector.wire = wire

	ctx, cancel := context.WithCancel(t.Context())
	connector.cancel = cancel

	playbackCtx, cancelPlayback := context.WithCancelCause(t.Context())
	defer cancelPlayback(nil)

	connector.playback = &playbackState{cancel: cancelPlayback}

	require.NoError(t, connector.Stop())

	assert.True(t, wire.closed)
	assert.Nil(t, connector.wire)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.ErrorIs(t, context.Cause(playbackCtx), ErrPlaybackInterrupted)
}

func TestStopReportsWireCloseError(t *testing.T) {
	connector := newTestConnector()
	errClose := errors.New("close failed")
	connector.wire = &fakeDiscordWire{errClose: errClose}

	err := connector.Stop()
	require.Error(t, err)
	assert.ErrorIs(t, err, errClose)
}

func TestReceiveWirePublishesAudio(t *testing.T) {
	connector := newTestConnector()

	bus := events.New()
	defer bus.Close()

	connector.bus = bus

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	voiceEvents := make(chan voiceEvent, 1)
	voiceEvents <- voiceEvent{packet: opusPacket{Sequence: 101, Timestamp: 202, SSRC: 7, SpeakerID: connector.config.HumanUserID, Opus: []byte{0x01, 0x02}}}

	go connector.receiveWire(ctx, voiceEvents)

	readCtx, stopRead := context.WithTimeout(t.Context(), time.Second)
	defer stopRead()

	var chunk *events.AudioChunk
	for audio := range bus.Audio(readCtx) {
		chunk = audio
		break
	}

	require.NotNil(t, chunk)
	assert.Equal(t, "discord_voice:voice-1", chunk.SessionID)
	assert.Equal(t, connector.config.HumanUserID, chunk.SpeakerID)
	assert.Equal(t, events.SourceDiscordVoice, chunk.Source)
	assert.Equal(t, uint16(101), chunk.RTPSequence)
	assert.Equal(t, uint32(202), chunk.Timestamp)
	assert.Equal(t, uint32(7), chunk.SSRC)
	assert.Equal(t, []byte{0x01, 0x02}, chunk.Data)
}

func TestReceiveWireErrorRequestsRestart(t *testing.T) {
	connector := newTestConnector()
	errWire := errors.New("bad handshake")

	voiceEvents := make(chan voiceEvent, 1)
	voiceEvents <- voiceEvent{err: errWire}

	restarted := make(chan string, 1)
	connector.restart = func(_ context.Context, reason string) (string, error) {
		restarted <- reason
		return "", nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go connector.receiveWire(ctx, voiceEvents)

	select {
	case reason := <-restarted:
		assert.Equal(t, "discord voice connection died: bad handshake", reason)
	case <-time.After(time.Second):
		t.Fatal("restart was not requested")
	}
}

func TestReceiveWireReturnsWhenEventsClose(t *testing.T) {
	connector := newTestConnector()
	voiceEvents := make(chan voiceEvent)
	close(voiceEvents)

	done := make(chan struct{})

	go func() {
		connector.receiveWire(t.Context(), voiceEvents)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("receiveWire did not return after voice events closed")
	}
}

func TestInterruptPlaybackCancelsActivePlayback(t *testing.T) {
	connector := newTestConnector()

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	connector.playback = &playbackState{cancel: cancel}

	require.True(t, connector.interruptPlayback())
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.ErrorIs(t, context.Cause(ctx), ErrPlaybackInterrupted)
}

func TestInterruptPlaybackWithoutActivePlayback(t *testing.T) {
	assert.False(t, newTestConnector().interruptPlayback())
}

func TestSendResponseSkipsNilAndBlankMessages(t *testing.T) {
	connector := newTestConnector()

	require.NoError(t, connector.SendResponse(t.Context(), nil))
	require.NoError(t, connector.SendResponse(t.Context(), events.NewMainOutboundMessage(events.SourceDiscordVoice, "   ", events.OutputTargetDiscord)))
}

func TestSendResponseSkipsThrottledThinking(t *testing.T) {
	connector := newTestConnector()
	connector.progressTurn = "turn-1"
	connector.progressAt = time.Now().Add(progressUtteranceInterval)

	msg := &events.OutboundMessage{Source: events.SourceDiscordVoice, SlackThinking: "thinking", TurnID: "turn-1"}

	require.NoError(t, connector.SendResponse(t.Context(), msg))
}

func TestSendResponseSpeaksUnthrottledThinking(t *testing.T) {
	inputs := make(chan string, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var request struct {
			Input string `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		inputs <- request.Input

		if _, errWrite := w.Write(testOggOpusStream([]byte("thinking-audio"))); errWrite != nil {
			t.Errorf("write TTS response: %v", errWrite)
		}
	}))
	defer server.Close()

	connector := newTestConnector()
	connector.tts = openaiaudio.NewTTSClient("test-key", server.URL, "", "", "")
	wire := &fakeDiscordWire{}
	connector.wire = wire

	msg := &events.OutboundMessage{Source: events.SourceDiscordVoice, SlackThinking: "thinking", TurnID: "turn-1"}

	require.NoError(t, connector.SendResponse(t.Context(), msg))

	select {
	case got := <-inputs:
		assert.True(t, slices.Contains([]string{"Yes, working.", "One second.", "Still working on it.", "Okay, working on that."}, got), "TTS input = %q", got)
	case <-time.After(time.Second):
		t.Fatal("TTS input = <none>; want progress utterance")
	}

	require.Equal(t, [][]byte{{'t', 'h', 'i', 'n', 'k', 'i', 'n', 'g', '-', 'a', 'u', 'd', 'i', 'o'}}, wire.played)
}

func TestSendResponseReportsContextCanceledWaitingForPlaybackSlot(t *testing.T) {
	connector := newTestConnector()
	<-connector.playbackSlot

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := connector.SendResponse(ctx, testPlaybackOutboundMessage("hello", false))
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "wait for Discord playback slot")
}

func TestSendResponseReportsUnavailableWire(t *testing.T) {
	connector := newTestConnector()
	connector.wire = nil

	err := connector.SendResponse(t.Context(), testPlaybackOutboundMessage("hello", false))
	require.ErrorContains(t, err, "discord voice connection unavailable")
}

func TestSendResponseStreamsTTSAndTracksTurnText(t *testing.T) {
	inputs := make(chan string, 2)

	responses := make(chan []byte, 2)
	responses <- testOggOpusStream([]byte("first-audio"))

	responses <- testOggOpusStream([]byte("final-audio"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var request struct {
			Input string `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		inputs <- request.Input

		select {
		case response := <-responses:
			if _, errWrite := w.Write(response); errWrite != nil {
				t.Errorf("write TTS response: %v", errWrite)
			}
		default:
			http.Error(w, "unexpected TTS request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	connector := newTestConnector()
	connector.tts = openaiaudio.NewTTSClient("test-key", server.URL, "", "", "")
	wire := &fakeDiscordWire{}
	connector.wire = wire

	reply := testPlaybackOutboundMessage("Hello", false)
	require.NoError(t, connector.SendResponse(t.Context(), reply))
	requireTTSInput(t, inputs, "Hello")
	assert.Equal(t, "Hello", connector.turnText["turn-1"])

	final := testPlaybackOutboundMessage("Hello world", true)
	require.NoError(t, connector.SendResponse(t.Context(), final))
	requireTTSInput(t, inputs, "world")
	assert.NotContains(t, connector.turnText, "turn-1")
	require.Equal(t, [][]byte{{'f', 'i', 'r', 's', 't', '-', 'a', 'u', 'd', 'i', 'o'}, {'f', 'i', 'n', 'a', 'l', '-', 'a', 'u', 'd', 'i', 'o'}}, wire.played)
}

func TestSendResponseReportsWirePlaybackError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write(testOggOpusStream([]byte("audio")))
		assert.NoError(t, err)
	}))
	defer server.Close()

	connector := newTestConnector()
	connector.tts = openaiaudio.NewTTSClient("test-key", server.URL, "", "", "")
	connector.wire = &fakeDiscordWire{errPlay: errors.New("play failed")}

	err := connector.SendResponse(t.Context(), testPlaybackOutboundMessage("hello", false))
	require.ErrorContains(t, err, "stream Discord voice response")
	require.ErrorContains(t, err, "play failed")
}

func TestSendResponseReportsTTSError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "quota exhausted", http.StatusTooManyRequests)
	}))
	defer server.Close()

	connector := newTestConnector()
	connector.tts = openaiaudio.NewTTSClient("test-key", server.URL, "", "", "")
	connector.wire = &fakeDiscordWire{}

	err := connector.SendResponse(t.Context(), testPlaybackOutboundMessage("hello", false))
	require.ErrorContains(t, err, "synthesize Discord voice response")
	require.ErrorContains(t, err, "tts API error (429): quota exhausted")
}

func TestNextPlaybackTextUsesOnlyUnseenSuffixAndClearsOnComplete(t *testing.T) {
	connector := newTestConnector()
	first := testPlaybackOutboundMessage("Hel", false)
	second := testPlaybackOutboundMessage("Hello", false)
	final := testPlaybackOutboundMessage("Hello", true)

	playback := connector.nextPlaybackText(first)
	assert.Equal(t, "Hel", playback.suffix)
	connector.turnText[playback.turnID] = playback.full
	playback = connector.nextPlaybackText(second)
	assert.Equal(t, "lo", playback.suffix)
	connector.turnText[playback.turnID] = playback.full
	playback = connector.nextPlaybackText(final)
	assert.Empty(t, playback.suffix)
	delete(connector.turnText, playback.turnID)
	assert.Equal(t, "Hello again", connector.nextPlaybackText(testPlaybackOutboundMessage("Hello again", false)).suffix)

	connector.turnText["turn-1"] = "old answer"
	playback = connector.nextPlaybackText(testPlaybackOutboundMessage("new answer", false))
	assert.Equal(t, "new answer", playback.suffix)
}

func TestNextPlaybackTextIncludesAttachmentNamesOnComplete(t *testing.T) {
	connector := newTestConnector()
	message := testPlaybackOutboundMessage("Done", true)
	message.Attachments = []events.OutboundAttachment{{Name: "diagram.png"}, {Name: " notes.txt "}, {Name: " "}}

	playback := connector.nextPlaybackText(message)

	assert.Equal(t, "Done\n\nAttached files: diagram.png, notes.txt.", playback.suffix)
}

func testPlaybackOutboundMessage(text string, complete bool) *events.OutboundMessage {
	message := new(events.OutboundMessage)
	message.Text = text
	message.Source = events.SourceDiscordVoice
	message.Targets = []events.OutputTarget{events.OutputTargetDiscord}
	message.ConversationID = events.MainConversationID()
	message.TurnID = "turn-1"
	message.Complete = complete

	return message
}

func testOggOpusStream(frames ...[]byte) []byte {
	packets := make([][]byte, 0, len(frames)+2)
	packets = append(packets, []byte("OpusHead v1"), []byte("OpusTags encoder"))
	packets = append(packets, frames...)

	var stream []byte

	for _, packet := range packets {
		var body, lacing []byte
		for len(packet) >= 255 {
			lacing = append(lacing, 255)
			body = append(body, packet[:255]...)
			packet = packet[255:]
		}

		lacing = append(lacing, byte(len(packet)))
		body = append(body, packet...)

		header := make([]byte, 27)
		copy(header, "OggS")
		header[26] = byte(len(lacing))

		stream = append(stream, header...)
		stream = append(stream, lacing...)
		stream = append(stream, body...)
	}

	return stream
}

func requireTTSInput(t *testing.T, inputs <-chan string, want string) {
	t.Helper()

	select {
	case got := <-inputs:
		assert.Equal(t, want, got)
	case <-time.After(time.Second):
		t.Fatalf("TTS input = <none>; want %q", want)
	}
}

func newTestConnector() *Connector {
	bus := events.New()
	tts := openaiaudio.NewTTSClient("test-key", "http://127.0.0.1", "", "", "")

	connector := &Connector{log: slog.New(slog.DiscardHandler), config: config.DiscordVoiceConfig{Token: "test-token", VoiceChannelID: "voice-1", HumanUserID: "human-user"}, bus: bus, tts: tts, restart: func(context.Context, string) (string, error) { return "", nil }, playbackSlot: make(chan struct{}, 1), turnText: map[string]string{}}
	connector.playbackSlot <- struct{}{}

	return connector
}
