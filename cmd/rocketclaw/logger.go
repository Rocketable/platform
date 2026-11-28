package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/posthog/posthog-go"
)

func newLogger(level *slog.LevelVar, mirrors ...slog.Handler) *slog.Logger {
	logger := slog.New(slog.NewMultiHandler(append(mirrors, slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))...))
	slog.SetDefault(logger)

	return logger
}

func postHogMirrors(client posthog.EnqueueClient, distinctID string) []slog.Handler {
	exceptions := posthog.NewSlogCaptureHandler(slog.DiscardHandler, client,
		posthog.WithDistinctIDFn(func(context.Context, slog.Record) string { return distinctID }),
		posthog.WithPropertiesFn(posthog.SlogAttrsAsProperties),
	)

	return []slog.Handler{exceptions, slog.NewJSONHandler(postHogEvents{client: client, distinctID: distinctID}, nil)}
}

type postHogEvents struct {
	client     posthog.EnqueueClient
	distinctID string
}

func (e postHogEvents) Write(record []byte) (int, error) {
	var properties posthog.Properties
	if err := json.Unmarshal(record, &properties); err != nil {
		return 0, fmt.Errorf("decode log record: %w", err)
	}

	event, _ := properties["event"].(string)
	message, _ := properties["msg"].(string)

	return len(record), e.client.Enqueue(posthog.Capture{DistinctId: e.distinctID, Event: cmp.Or(event, message), Properties: properties})
}
