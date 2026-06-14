package voice

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
)

// UtteranceSilenceWindow matches the current Discord-style utterance timeout.
const UtteranceSilenceWindow = 1500 * time.Millisecond

// TranscriptionPublisher relays transcribed voice text into the shared main session.
type TranscriptionPublisher struct {
	log                *slog.Logger
	bus                *events.Bus
	source             events.Source
	emergencySafeWords []string
	beforeMainSession  func(context.Context, string) (*events.InboundMessage, error)
}

// NewTranscriptionPublisher constructs a reusable voice transcription publisher.
func NewTranscriptionPublisher(
	bus *events.Bus,
	logger *slog.Logger,
	source events.Source,
	emergencySafeWords []string,
	beforeMainSession func(context.Context, string) (*events.InboundMessage, error),
) *TranscriptionPublisher {
	return &TranscriptionPublisher{log: logger.With("component", "voice"), bus: bus, source: source, emergencySafeWords: slices.Clone(emergencySafeWords), beforeMainSession: beforeMainSession}
}

// PublishTranscription publishes a transcribed utterance into the main conversation.
func (p *TranscriptionPublisher) PublishTranscription(ctx context.Context, text, webSessionID string) (bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return false, nil
	}

	normalizedText := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r):
			return unicode.ToLower(r)
		case unicode.IsDigit(r):
			return r
		default:
			return -1
		}
	}, text)
	if slices.Contains(p.emergencySafeWords, normalizedText) {
		os.Exit(254)
	}

	reply, err := p.beforeMainSession(ctx, text)
	if err != nil {
		return false, fmt.Errorf("relay voice utterance before main-session publish: %w", err)
	}

	inbound := events.NewMainInboundMessage(p.source, events.InboundKindPrompt, "", text, true)
	if reply != nil {
		inbound.SlackReply = reply.SlackReply
		inbound.DiscordReply = reply.DiscordReply
	}

	inbound.WebSessionID = strings.TrimSpace(webSessionID)
	if err := p.bus.PublishInbound(ctx, inbound); err != nil {
		return false, fmt.Errorf("publish voice utterance to inbound bus: %w", err)
	}

	return true, nil
}
