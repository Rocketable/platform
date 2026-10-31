package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/posthog/posthog-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostHogMirrorsCaptureEventsAndExceptions(t *testing.T) {
	previous := slog.Default()

	t.Cleanup(func() { slog.SetDefault(previous) })

	type captured struct {
		Event      string         `json:"event"`
		DistinctID string         `json:"distinct_id"`
		Properties map[string]any `json:"properties"`
	}

	var (
		mu     sync.Mutex
		events []captured
	)

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var body struct {
			Batch []captured `json:"batch"`
		}

		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		mu.Lock()
		defer mu.Unlock()

		events = append(events, body.Batch...)
	}))
	t.Cleanup(server.Close)

	client, err := posthog.NewWithConfig("phc_test", posthog.Config{Endpoint: server.URL})
	require.NoError(t, err)

	level := new(slog.LevelVar)
	level.Set(slog.LevelError)

	logger := newLogger(level, postHogMirrors(client, "lab")...).With("component", "cron")
	logger.Debug("ignored")
	logger.Info("completed cronjob", "event", "cron_completed", "outcome", "ok")
	logger.Info("rocketclaw health", "go_goroutines", 7)
	logger.Error("cronjob failed", "error", errors.New("boom"))
	require.NoError(t, client.Close())

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, events, 4)

	names := map[string]captured{}
	for _, event := range events {
		// The SDK sends an exception's distinct ID among its properties.
		exceptionDistinctID, _ := event.Properties["distinct_id"].(string)
		assert.Equal(t, "lab", cmp.Or(event.DistinctID, exceptionDistinctID))
		names[event.Event] = event
	}

	assert.Subset(t, names["cron_completed"].Properties, map[string]any{"msg": "completed cronjob", "component": "cron", "outcome": "ok", "level": "INFO"})
	assert.Subset(t, names["rocketclaw health"].Properties, map[string]any{"go_goroutines": float64(7)})
	assert.Subset(t, names["cronjob failed"].Properties, map[string]any{"error": "boom", "level": "ERROR"})
	assert.Equal(t, "boom", names["$exception"].Properties["$exception_list"].([]any)[0].(map[string]any)["value"])
}
