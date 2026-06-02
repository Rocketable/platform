// Package discordvoice bridges Discord voice into rocketclaw.
package discordvoice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/openaiaudio"
)

const progressUtteranceInterval = 10 * time.Second

// ErrPlaybackInterrupted reports that Discord playback was intentionally cut short.
var ErrPlaybackInterrupted = errors.New("discord playback interrupted")

type playbackState struct {
	cancel context.CancelCauseFunc
}

type playbackText struct {
	full, suffix, turnID string
	done                 bool
}

type voiceWire interface {
	play(ctx context.Context, frames opusFrames) (int, error)
	close() error
}

type opusFrames func(yield func([]byte) error) error

// Connector bridges Discord voice input and output into the shared rocketclaw bus.
type Connector struct {
	log     *slog.Logger
	config  config.DiscordVoiceConfig
	bus     *events.Bus
	tts     *openaiaudio.TTSClient
	restart func(context.Context, string) (string, error)

	cancel context.CancelFunc
	wire   voiceWire

	mu           sync.Mutex
	playback     *playbackState
	playbackSlot chan struct{}
	progressTurn string
	progressAt   time.Time
	turnText     map[string]string
}

// New constructs and starts a Discord voice connector.
func New(ctx context.Context, cfg config.DiscordVoiceConfig, bus *events.Bus, tts *openaiaudio.TTSClient, restart func(context.Context, string) (string, error), logger *slog.Logger) (*Connector, error) {
	connector := &Connector{
		log:          logger.With("component", "discord_voice"),
		config:       cfg,
		bus:          bus,
		tts:          tts,
		restart:      restart,
		playbackSlot: make(chan struct{}, 1),
		turnText:     map[string]string{},
	}
	connector.playbackSlot <- struct{}{}

	connectorCtx, cancel := context.WithCancel(ctx)
	connector.cancel = cancel

	wire, err := newWire(connectorCtx, wireConfig{token: "Bot " + cfg.Token, voiceChannelID: cfg.VoiceChannelID, humanUserID: cfg.HumanUserID, log: connector.log})
	if err != nil {
		cancel()
		return nil, err
	}

	connector.wire = wire

	go connector.receiveWire(connectorCtx, wire.events)

	return connector, nil
}

// Stop disconnects the connector from Discord.
func (c *Connector) Stop() error {
	if c.cancel != nil {
		c.cancel()
	}

	c.interruptPlayback()

	c.mu.Lock()
	wire := c.wire
	c.wire = nil
	c.mu.Unlock()

	if wire != nil {
		if err := wire.close(); err != nil {
			return fmt.Errorf("close Discord voice wire: %w", err)
		}
	}

	return nil
}

// SendResponse synthesizes and streams a response into the voice channel.
func (c *Connector) SendResponse(ctx context.Context, msg *events.OutboundMessage) error {
	if msg == nil {
		return nil
	}

	thinking := msg.Source == events.SourceDiscordVoice && msg.Text == "" && msg.SlackThinking != ""

	playback := c.nextPlaybackText(msg)
	if thinking {
		now := time.Now()
		turnID := strings.TrimSpace(msg.TurnID)

		c.mu.Lock()

		speak := turnID != c.progressTurn || now.Sub(c.progressAt) >= progressUtteranceInterval
		if speak {
			c.progressTurn = turnID
			c.progressAt = now
		}
		c.mu.Unlock()

		if !speak {
			return nil
		}

		playback = playbackText{full: "", suffix: [...]string{"Yes, working.", "One second.", "Still working on it.", "Okay, working on that."}[rand.IntN(4)], turnID: "", done: false}
	} else if msg.Source == events.SourceDiscordVoice {
		c.mu.Lock()
		c.progressTurn = ""
		c.progressAt = time.Time{}
		c.mu.Unlock()
	}

	if strings.TrimSpace(playback.suffix) == "" {
		return nil
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for Discord playback slot: %w", ctx.Err())
	case <-c.playbackSlot:
	}

	defer func() {
		c.playbackSlot <- struct{}{}
	}()

	c.mu.Lock()
	wire := c.wire
	c.mu.Unlock()

	if wire == nil {
		return errors.New("discord voice connection unavailable")
	}

	playbackCtx, cancel := context.WithCancelCause(ctx)
	state := &playbackState{cancel: cancel}

	c.mu.Lock()
	c.playback = state
	c.mu.Unlock()

	defer func() {
		cancel(nil)
		c.mu.Lock()
		if c.playback == state {
			c.playback = nil
		}
		c.mu.Unlock()
	}()

	stream, err := c.tts.Synthesize(playbackCtx, playback.suffix)
	if err != nil {
		if errors.Is(context.Cause(playbackCtx), ErrPlaybackInterrupted) {
			return ErrPlaybackInterrupted
		}

		return fmt.Errorf("synthesize Discord voice response: %w", err)
	}

	defer func() { _ = stream.Close() }()

	_, err = wire.play(playbackCtx, func(yield func([]byte) error) error {
		return openaiaudio.DecodeOggOpus(stream, yield)
	})
	if err != nil {
		if errors.Is(context.Cause(playbackCtx), ErrPlaybackInterrupted) {
			return ErrPlaybackInterrupted
		}

		return fmt.Errorf("stream Discord voice response: %w", err)
	}

	if strings.TrimSpace(playback.turnID) != "" {
		c.mu.Lock()
		if playback.done {
			delete(c.turnText, playback.turnID)
		} else {
			c.turnText[playback.turnID] = playback.full
		}
		c.mu.Unlock()
	}

	return nil
}

func (c *Connector) interruptPlayback() bool {
	c.mu.Lock()

	state := c.playback
	if state == nil {
		c.mu.Unlock()
		return false
	}

	c.playback = nil
	cancel := state.cancel
	c.mu.Unlock()

	if cancel != nil {
		cancel(ErrPlaybackInterrupted)
	}

	return true
}

func (c *Connector) nextPlaybackText(msg *events.OutboundMessage) playbackText {
	if msg == nil {
		return playbackText{}
	}

	text := strings.TrimSpace(msg.Text)
	if msg.Complete {
		text = strings.TrimSpace(text + "\n\n" + events.AttachmentNamesSpeech(msg.Attachments))
	}

	turnID := strings.TrimSpace(msg.TurnID)
	if turnID == "" {
		return playbackText{full: text, suffix: text, done: msg.Complete}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	previous := c.turnText[turnID]
	if text == "" {
		return playbackText{full: text, suffix: "", turnID: turnID, done: msg.Complete}
	}

	if previous == "" {
		return playbackText{full: text, suffix: text, turnID: turnID, done: msg.Complete}
	}

	if strings.HasPrefix(text, previous) {
		return playbackText{full: text, suffix: strings.TrimSpace(text[len(previous):]), turnID: turnID, done: msg.Complete}
	}

	return playbackText{full: text, suffix: text, turnID: turnID, done: msg.Complete}
}

func (c *Connector) receiveWire(ctx context.Context, voiceEvents <-chan voiceEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-voiceEvents:
			if !ok {
				return
			}

			if event.err != nil {
				c.interruptPlayback()

				reason := fmt.Sprintf("discord voice connection died: %v", event.err)
				c.log.Error("Discord voice connection died; requesting application restart", "error", event.err, "reason", reason)

				restartCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if _, err := c.restart(restartCtx, reason); err != nil {
					c.log.Error("request application restart after Discord voice failure", "error", err, "voice_error", event.err, "reason", reason)
				}

				cancel()

				return
			}

			packet := event.packet
			chunk := &events.AudioChunk{
				SessionID:   "discord_voice:" + c.config.VoiceChannelID,
				SpeakerID:   packet.SpeakerID,
				Source:      events.SourceDiscordVoice,
				RTPSequence: packet.Sequence,
				Timestamp:   packet.Timestamp,
				SSRC:        packet.SSRC,
				SampleRate:  48000,
				Channels:    2,
				Format:      "opus",
				Data:        slices.Clone(packet.Opus),
			}

			publishCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			err := c.bus.PublishAudio(publishCtx, chunk)

			cancel()

			if err != nil {
				c.log.Error("publish Discord audio chunk", "error", err, "ssrc", packet.SSRC, "rtp_sequence", packet.Sequence)
			}
		}
	}
}
