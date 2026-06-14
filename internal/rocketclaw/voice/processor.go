// Package voice transcribes inbound voice traffic into text messages.
package voice

import (
	"cmp"
	"context"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3/pkg/media/oggwriter"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/openaiaudio"
)

const minimumUtteranceBytes = 100

// Processor collects Discord voice audio into utterances and transcribes them.
type Processor struct {
	log         *slog.Logger
	bus         *events.Bus
	transcriber *openaiaudio.WhisperClient
	publisher   *TranscriptionPublisher

	mu       sync.Mutex
	sessions map[string]*accumulator
}

type accumulator struct {
	startedAt   time.Time
	lastAudioAt time.Time
	sessionID   string
	speakerID   string
	packets     []events.AudioChunk

	mu     sync.Mutex
	closed bool
}

// NewProcessor constructs a voice processor.
func NewProcessor(
	bus *events.Bus,
	transcriber *openaiaudio.WhisperClient,
	logger *slog.Logger,
	emergencySafeWords []string,
	beforeMainSession func(context.Context, string) (*events.InboundMessage, error),
) *Processor {
	processor := new(Processor)
	processor.log = logger.With("component", "voice")
	processor.bus = bus
	processor.transcriber = transcriber
	processor.publisher = NewTranscriptionPublisher(bus, logger, events.SourceDiscordVoice, emergencySafeWords, beforeMainSession)
	processor.sessions = map[string]*accumulator{}

	return processor
}

// Start begins processing voice chunks until the context is canceled.
func (p *Processor) Start(ctx context.Context) {
	p.log.Info("voice processor started")

	go p.listen(ctx)
	go p.vad(ctx)
	go p.cleanup(ctx)
}

func (p *Processor) listen(ctx context.Context) {
	for chunk := range p.bus.Audio(ctx) {
		p.handleChunk(chunk)
	}
}

func (p *Processor) handleChunk(chunk *events.AudioChunk) {
	if chunk == nil {
		return
	}

	if chunk.Format != "opus" {
		p.log.Debug("ignoring unsupported audio chunk", "format", chunk.Format)
		return
	}

	key := chunk.SessionID + ":" + chunk.SpeakerID

	p.mu.Lock()

	acc, ok := p.sessions[key]
	if !ok {
		acc = new(accumulator)
		acc.startedAt = time.Now()
		acc.lastAudioAt = time.Now()
		acc.sessionID = chunk.SessionID
		acc.speakerID = chunk.SpeakerID
		p.sessions[key] = acc
		p.log.Info("started Discord voice utterance capture", "session_id", chunk.SessionID, "speaker_id", chunk.SpeakerID, "sample_rate", chunk.SampleRate, "channels", chunk.Channels)
	}
	p.mu.Unlock()

	acc.push(chunk)
}

func (p *Processor) vad(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.checkSilence(ctx)
		}
	}
}

func (p *Processor) checkSilence(ctx context.Context) {
	now := time.Now()

	var finished []*accumulator

	p.mu.Lock()
	for key, acc := range p.sessions {
		acc.mu.Lock()
		if now.Sub(acc.lastAudioAt) > UtteranceSilenceWindow {
			acc.closed = true
			sessionID := acc.sessionID
			speakerID := acc.speakerID
			chunks := len(acc.packets)
			startedAt := acc.startedAt
			acc.mu.Unlock()
			delete(p.sessions, key)

			finished = append(finished, acc)

			p.log.Info(
				"finalized Discord voice utterance",
				"session_id", sessionID,
				"speaker_id", speakerID,
				"chunks", chunks,
				"duration", now.Sub(startedAt),
			)

			continue
		}
		acc.mu.Unlock()
	}
	p.mu.Unlock()

	for _, acc := range finished {
		go p.processUtterance(ctx, acc)
	}
}

func (p *Processor) cleanup(ctx context.Context) {
	<-ctx.Done()
	p.log.Info("cleaning up voice processor state")
	p.mu.Lock()
	defer p.mu.Unlock()

	for key, acc := range p.sessions {
		acc.mu.Lock()
		acc.closed = true
		acc.mu.Unlock()
		delete(p.sessions, key)
	}
}

func (p *Processor) processUtterance(ctx context.Context, acc *accumulator) {
	acc.mu.Lock()
	packets := slices.Clone(acc.packets)
	chunks := len(acc.packets)
	acc.mu.Unlock()

	normalizedPackets := normalizeUtterancePackets(packets)

	normalizedBytes := 0
	for _, packet := range normalizedPackets {
		normalizedBytes += len(packet.Data)
	}

	if normalizedBytes < minimumUtteranceBytes {
		p.log.Info(
			"skipping tiny Discord voice utterance",
			"chunks", chunks,
			"normalized_chunks", len(normalizedPackets),
			"normalized_bytes", normalizedBytes,
		)

		return
	}

	sampleRate := normalizedPackets[0].SampleRate

	channels := normalizedPackets[0].Channels
	if sampleRate <= 0 || channels <= 0 {
		p.log.Error("invalid Discord voice utterance audio parameters", "sample_rate", sampleRate, "channels", channels)
		return
	}

	tempFile, err := os.CreateTemp("", "rocketclaw-*.ogg")
	if err != nil {
		p.log.Error("create Discord voice utterance temp file", "error", err)
		return
	}

	filename := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(filename)

		p.log.Error("close Discord voice utterance temp file", "error", err)

		return
	}

	defer func() {
		_ = os.Remove(filename)
	}()

	writer, err := oggwriter.New(filename, uint32(sampleRate), uint16(channels))
	if err != nil {
		p.log.Error("create Discord voice utterance OGG writer", "error", err)
		return
	}

	for _, packet := range normalizedPackets {
		rtpPacket := new(rtp.Packet)
		rtpPacket.SequenceNumber = packet.RTPSequence
		rtpPacket.Timestamp = packet.Timestamp
		rtpPacket.SSRC = packet.SSRC

		rtpPacket.Payload = packet.Data
		if err := writer.WriteRTP(rtpPacket); err != nil {
			_ = writer.Close()

			p.log.Error("write Discord voice utterance OGG", "error", err)

			return
		}
	}

	if err := writer.Close(); err != nil {
		p.log.Error("close Discord voice utterance OGG writer", "error", err)
		return
	}

	if p.transcriber == nil {
		p.log.Error("transcribe utterance", "error", "whisper transcriber is nil")
		return
	}

	p.log.Info("transcribing Discord voice utterance", "file", filename, "chunks", chunks, "normalized_chunks", len(normalizedPackets), "normalized_bytes", normalizedBytes)

	text, err := p.transcriber.Transcribe(ctx, filename)
	if err != nil {
		p.log.Error("transcribe utterance", "error", err)
		return
	}

	text = strings.TrimSpace(text)
	if text == "" {
		p.log.Info("transcription returned empty text", "file", filename)
		return
	}

	p.log.Info("transcribed Discord voice utterance", "text_len", len(text), "text", text)

	published, err := p.publisher.PublishTranscription(ctx, text, "")
	if err != nil {
		p.log.Error("publish transcribed utterance", "error", err)
		return
	}

	if !published {
		return
	}

	p.log.Info("published transcribed utterance to bus", "text_len", len(text), "text", text)
}
func normalizeUtterancePackets(packets []events.AudioChunk) []events.AudioChunk {
	if len(packets) == 0 {
		return nil
	}

	normalized := slices.Clone(packets)
	slices.SortFunc(normalized, func(left, right events.AudioChunk) int {
		return cmp.Or(
			cmp.Compare(left.Timestamp, right.Timestamp),
			cmp.Compare(left.SSRC, right.SSRC),
			cmp.Compare(left.RTPSequence, right.RTPSequence),
		)
	})

	return slices.CompactFunc(normalized, func(left, right events.AudioChunk) bool {
		return left.SSRC == right.SSRC && left.RTPSequence == right.RTPSequence
	})
}

func (a *accumulator) push(chunk *events.AudioChunk) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return
	}

	a.lastAudioAt = time.Now()
	a.packets = append(a.packets, *chunk)
}
