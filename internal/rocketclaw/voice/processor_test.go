package voice

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/openaiaudio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishTranscriptionRelaysBeforePublishingInbound(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	steps := make([]string, 0, 2)
	processor := NewProcessor(bus, nil, testLogger(), nil, func(context.Context, string) (*events.InboundMessage, error) {
		steps = append(steps, "relay")
		return &events.InboundMessage{SlackReply: &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: ""}}, nil
	})

	published, err := processor.publisher.PublishTranscription(context.Background(), "hello from Discord", "")
	require.NoError(t, err)
	require.True(t, published)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	for msg := range bus.Inbound(ctx) {
		steps = append(steps, "publish")

		assert.Equal(t, events.SourceDiscordVoice, msg.Source)
		assert.Equal(t, "hello from Discord", msg.Text)
		require.NotNil(t, msg.SlackReply)
		assert.Equal(t, "D123", msg.SlackReply.ChannelID)
		assert.Equal(t, "111.222", msg.SlackReply.MessageTS)

		break
	}

	require.Len(t, steps, 2, "expected inbound message after publishTranscription")
	assert.Equal(t, []string{"relay", "publish"}, steps)
}

func TestPublishTranscriptionStopsWhenRelayFails(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	processor := NewProcessor(bus, nil, testLogger(), nil, func(context.Context, string) (*events.InboundMessage, error) {
		return nil, errors.New("relay failed")
	})

	_, err := processor.publisher.PublishTranscription(context.Background(), "hello from Discord", "")
	require.Error(t, err)

	requireNoInboundMessages(t, bus)
}

func TestNormalizeUtterancePacketsReordersAndDropsDuplicates(t *testing.T) {
	assert.Empty(t, normalizeUtterancePackets(nil))
	assert.Empty(t, normalizeUtterancePackets([]events.AudioChunk{}))

	packets := []events.AudioChunk{
		testAudioChunk(2, 200, []byte{0x02}),
		testAudioChunk(1, 100, []byte{0x01}),
		testAudioChunk(2, 200, []byte{0x09}),
		testAudioChunk(3, 300, []byte{0x03}),
	}

	normalized := normalizeUtterancePackets(packets)
	require.Len(t, normalized, 3)
	assert.Equal(t, []uint16{1, 2, 3}, []uint16{normalized[0].RTPSequence, normalized[1].RTPSequence, normalized[2].RTPSequence})
}

func TestProcessUtteranceSkipsTinyAudio(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	processor := NewProcessor(bus, nil, testLogger(), nil, testBeforeMainSession)
	acc := new(accumulator)
	acc.packets = []events.AudioChunk{testAudioChunk(1, 100, []byte("tiny"))}

	processor.processUtterance(context.Background(), acc)

	requireNoInboundMessages(t, bus)
}

func TestProcessUtteranceSkipsInvalidAudioParameters(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var logs bytes.Buffer

	processor := NewProcessor(bus, nil, slog.New(slog.NewTextHandler(&logs, nil)), nil, testBeforeMainSession)
	chunk := testAudioChunk(1, 100, bytes.Repeat([]byte{0x01}, minimumUtteranceBytes))
	chunk.SampleRate = 0
	acc := &accumulator{packets: []events.AudioChunk{chunk}}

	processor.processUtterance(context.Background(), acc)

	assert.Contains(t, logs.String(), "invalid Discord voice utterance audio parameters")
	assert.Contains(t, logs.String(), "sample_rate=0")
	requireNoInboundMessages(t, bus)
}

func TestProcessUtteranceCreatesOGGBeforeTranscribing(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var logs bytes.Buffer

	processor := NewProcessor(bus, nil, slog.New(slog.NewTextHandler(&logs, nil)), nil, testBeforeMainSession)
	chunk := testAudioChunk(1, 100, bytes.Repeat([]byte{0x01}, minimumUtteranceBytes))
	acc := &accumulator{packets: []events.AudioChunk{chunk}}

	processor.processUtterance(context.Background(), acc)

	assert.Contains(t, logs.String(), "transcribe utterance")
	assert.Contains(t, logs.String(), "whisper transcriber is nil")
	requireNoInboundMessages(t, bus)
}

func TestProcessUtterancePublishesTranscription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assert.Equal(t, http.MethodPost, req.Method)
		assert.Equal(t, "/audio/transcriptions", req.URL.Path)
		assert.Equal(t, "Bearer secret", req.Header.Get("Authorization"))

		_, err := io.Copy(io.Discard, req.Body)
		assert.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, err = io.WriteString(w, `{"text":" hello voice "}`)
		assert.NoError(t, err)
	}))
	defer server.Close()

	bus := events.New()
	defer bus.Close()

	processor := NewProcessor(bus, openaiaudio.NewWhisperClient("secret", server.URL, "whisper-1", ""), testLogger(), nil, func(_ context.Context, text string) (*events.InboundMessage, error) {
		assert.Equal(t, "hello voice", text)
		return &events.InboundMessage{SlackReply: &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222"}}, nil
	})
	chunk := testAudioChunk(1, 100, bytes.Repeat([]byte{0x01}, minimumUtteranceBytes))
	acc := &accumulator{packets: []events.AudioChunk{chunk}}

	processor.processUtterance(context.Background(), acc)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	for msg := range bus.Inbound(ctx) {
		assert.Equal(t, events.SourceDiscordVoice, msg.Source)
		assert.Equal(t, "hello voice", msg.Text)
		require.NotNil(t, msg.SlackReply)
		assert.Equal(t, "D123", msg.SlackReply.ChannelID)

		return
	}

	t.Fatal("processUtterance did not publish inbound transcription")
}

func TestProcessUtteranceStopsWithoutPublishingOnTranscriptionEdges(t *testing.T) {
	errRelay := errors.New("relay failed")
	tests := []struct {
		name    string
		status  int
		body    string
		before  func(context.Context, string) (*events.InboundMessage, error)
		wantLog string
	}{
		{
			name:    "transcription error",
			status:  http.StatusInternalServerError,
			body:    "whisper failed",
			before:  testBeforeMainSession,
			wantLog: "whisper API error",
		},
		{
			name:    "empty transcription",
			status:  http.StatusOK,
			body:    `{"text":" \n\t "}`,
			before:  testBeforeMainSession,
			wantLog: "transcription returned empty text",
		},
		{
			name:   "relay error",
			status: http.StatusOK,
			body:   `{"text":"voice"}`,
			before: func(context.Context, string) (*events.InboundMessage, error) {
				return nil, errRelay
			},
			wantLog: "publish transcribed utterance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				assert.Equal(t, http.MethodPost, req.Method)
				assert.Equal(t, "/audio/transcriptions", req.URL.Path)
				w.WriteHeader(tt.status)
				_, err := io.WriteString(w, tt.body)
				assert.NoError(t, err)
			}))
			defer server.Close()

			bus := events.New()
			defer bus.Close()

			var logs bytes.Buffer

			processor := NewProcessor(bus, openaiaudio.NewWhisperClient("secret", server.URL, "whisper-1", ""), slog.New(slog.NewTextHandler(&logs, nil)), nil, tt.before)
			chunk := testAudioChunk(1, 100, bytes.Repeat([]byte{0x01}, minimumUtteranceBytes))

			processor.processUtterance(context.Background(), &accumulator{packets: []events.AudioChunk{chunk}})

			assert.Contains(t, logs.String(), tt.wantLog)
			requireNoInboundMessages(t, bus)
		})
	}
}

func TestHandleChunkStartsOpusAccumulator(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	processor := NewProcessor(bus, nil, testLogger(), nil, testBeforeMainSession)
	processor.handleChunk(nil)

	nonOpus := testAudioChunk(1, 100, []byte("pcm"))
	nonOpus.Format = "pcm"
	processor.handleChunk(&nonOpus)
	require.Empty(t, processor.sessions)

	chunk := testAudioChunk(2, 200, []byte("opus"))
	chunk.SessionID = "session-1"
	chunk.SpeakerID = "speaker-1"
	processor.handleChunk(&chunk)

	acc := processor.sessions["session-1:speaker-1"]
	require.NotNil(t, acc)
	acc.mu.Lock()
	defer acc.mu.Unlock()

	assert.Equal(t, "session-1", acc.sessionID)
	assert.Equal(t, "speaker-1", acc.speakerID)
	assert.Equal(t, []events.AudioChunk{chunk}, acc.packets)
}

func TestCheckSilenceFinalizesExpiredSessions(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	processor := NewProcessor(bus, nil, testLogger(), nil, testBeforeMainSession)
	old := time.Now().Add(-UtteranceSilenceWindow - time.Second)
	expired := &accumulator{
		startedAt:   old.Add(-time.Second),
		lastAudioAt: old,
		sessionID:   "session-expired",
		speakerID:   "speaker-expired",
		packets:     []events.AudioChunk{testAudioChunk(1, 100, []byte("tiny"))},
	}
	active := &accumulator{
		startedAt:   time.Now(),
		lastAudioAt: time.Now().Add(time.Hour),
		sessionID:   "session-active",
		speakerID:   "speaker-active",
	}
	processor.sessions["expired"] = expired
	processor.sessions["active"] = active

	processor.checkSilence(context.Background())

	assert.NotContains(t, processor.sessions, "expired")
	assert.Same(t, active, processor.sessions["active"])

	expired.mu.Lock()
	defer expired.mu.Unlock()

	assert.True(t, expired.closed)
}

func TestCleanupClosesAndRemovesSessions(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	processor := NewProcessor(bus, nil, testLogger(), nil, testBeforeMainSession)
	acc := &accumulator{sessionID: "session-1", speakerID: "speaker-1"}
	processor.sessions["session-1:speaker-1"] = acc

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	processor.cleanup(ctx)

	assert.Empty(t, processor.sessions)
	acc.mu.Lock()
	defer acc.mu.Unlock()

	assert.True(t, acc.closed)
}

func TestStartConsumesAudioAndStopsOnCancel(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	processor := NewProcessor(bus, nil, testLogger(), nil, testBeforeMainSession)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	processor.Start(ctx)

	chunk := testAudioChunk(7, 100, []byte("opus"))
	chunk.SessionID = "session-start"
	chunk.SpeakerID = "speaker-start"
	require.NoError(t, bus.PublishAudio(t.Context(), &chunk))

	require.Eventually(t, func() bool {
		processor.mu.Lock()
		defer processor.mu.Unlock()

		return processor.sessions["session-start:speaker-start"] != nil
	}, time.Second, time.Millisecond)

	cancel()

	require.Eventually(t, func() bool {
		processor.mu.Lock()
		defer processor.mu.Unlock()

		return len(processor.sessions) == 0
	}, time.Second, time.Millisecond)
}

func TestAccumulatorPushIgnoresClosedAccumulator(t *testing.T) {
	chunk := testAudioChunk(1, 100, []byte("opus"))
	acc := &accumulator{closed: true}

	acc.push(&chunk)
	assert.Empty(t, acc.packets)

	acc.closed = false
	acc.push(&chunk)
	assert.Equal(t, []events.AudioChunk{chunk}, acc.packets)
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func testBeforeMainSession(context.Context, string) (*events.InboundMessage, error) {
	return nil, nil
}

func requireNoInboundMessages(t *testing.T, bus *events.Bus) {
	t.Helper()

	bus.StopInbound()

	var msgs []*events.InboundMessage
	for msg := range bus.Inbound(context.Background()) {
		msgs = append(msgs, msg)
	}

	require.Empty(t, msgs)
}

func testAudioChunk(sequence, timestamp uint32, data []byte) events.AudioChunk {
	return events.AudioChunk{
		SessionID:   "",
		SpeakerID:   "",
		Source:      "",
		RTPSequence: uint16(sequence),
		Timestamp:   timestamp,
		SSRC:        7,
		SampleRate:  48000,
		Channels:    2,
		Format:      "opus",
		Data:        data,
	}
}
