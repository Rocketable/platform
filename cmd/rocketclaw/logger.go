package main

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"

	"github.com/posthog/posthog-go"
)

// newLogger logs to stderr at levelText and also hands every record to the mirrors, which pick their own levels.
func newLogger(levelText string, mirrors ...slog.Handler) *slog.Logger {
	levelText = strings.TrimSpace(strings.ToUpper(levelText))
	if levelText == "WARNING" {
		levelText = "WARN"
	}

	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(levelText)); err != nil {
		level = slog.LevelDebug
	}

	logger := slog.New(slog.NewMultiHandler(append(mirrors, slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))...))
	slog.SetDefault(logger)

	return logger
}

// postHogMirrors send warnings and errors to PostHog Error Tracking, and every record at info or above
// as an event named by its event attribute, or else by its message.
func postHogMirrors(client posthog.EnqueueClient, distinctID string) []slog.Handler {
	exceptions := posthog.NewSlogCaptureHandler(slog.DiscardHandler, client,
		posthog.WithDistinctIDFn(func(context.Context, slog.Record) string { return distinctID }),
		posthog.WithPropertiesFn(posthog.SlogAttrsAsProperties),
	)

	return []slog.Handler{exceptions, slog.NewJSONHandler(postHogEvents{client: client, distinctID: distinctID}, nil)}
}

// postHogEvents captures each JSON log record it is written as one PostHog event.
type postHogEvents struct {
	client     posthog.EnqueueClient
	distinctID string
}

func (e postHogEvents) Write(record []byte) (int, error) {
	var properties posthog.Properties
	if err := json.Unmarshal(record, &properties); err != nil {
		return 0, err
	}

	event, _ := properties["event"].(string)
	message, _ := properties["msg"].(string)

	return len(record), e.client.Enqueue(posthog.Capture{DistinctId: e.distinctID, Event: cmp.Or(event, message), Properties: properties})
}
