package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/oai"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelResolverSelectsUnqualifiedExplicitAndNamedProviders(t *testing.T) {
	tests := []struct {
		selector string
		origin   rocketcode.ProviderOrigin
		server   string
	}{
		{selector: "gpt-5.5", origin: rocketcode.ProviderOrigin{Provider: "openai", Model: "gpt-5.5"}, server: "openai"},
		{selector: "openai/gpt-5.5", origin: rocketcode.ProviderOrigin{Provider: "openai", Model: "gpt-5.5"}, server: "openai"},
		{selector: "work/gpt-5.5", origin: rocketcode.ProviderOrigin{Provider: "work", Model: "gpt-5.5"}, server: "work"},
		{selector: "anthropic/api/claude-sonnet-5-5", origin: rocketcode.ProviderOrigin{Provider: "anthropic", Model: "api/claude-sonnet-5-5"}, server: "work"},
	}

	requests := make(chan string, len(tests))
	newServer := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests <- name

			w.Header().Set("Content-Type", "application/json")
			writeRawRunMessage(t, w, "response", "message", "ok")
		}))
	}

	openAI := newServer("openai")
	t.Cleanup(openAI.Close)

	work := newServer("work")
	t.Cleanup(work.Close)

	resolver := newModelResolver(&config.Config{
		Workspace: t.TempDir(),
		OpenAI:    config.OpenAIConfig{APIKey: "openai-key", APIBaseURL: openAI.URL, RocketCodeAuth: "api_key"},
		Providers: map[string]config.OpenAIConfig{
			"work":      {APIKey: "work-key", APIBaseURL: work.URL, RocketCodeAuth: "api_key"},
			"anthropic": {APIKey: "work-key", APIBaseURL: work.URL, RocketCodeAuth: "api_key"},
		},
	}, slog.New(slog.DiscardHandler))

	for _, test := range tests {
		client, origin, err := resolver.Resolve(test.selector)
		require.NoError(t, err)
		assert.Equal(t, test.origin, origin)

		_, err = client.Responses.New(t.Context(), responses.ResponseNewParams{Model: origin.Model})
		require.NoError(t, err)
		assert.Equal(t, test.server, <-requests)
	}
}

func TestModelResolverUsesProviderAutocompactionThreshold(t *testing.T) {
	resolver := newModelResolver(&config.Config{
		OpenAI:    config.OpenAIConfig{APIKey: "openai-key", AutocompactionThreshold: 150000},
		Providers: map[string]config.OpenAIConfig{"work": {APIKey: "work-key", AutocompactionThreshold: 80000}},
	}, slog.New(slog.DiscardHandler))

	_, origin, err := resolver.Resolve("gpt-5.5")
	require.NoError(t, err)
	assert.Equal(t, int64(150000), origin.CompactThreshold)

	_, origin, err = resolver.Resolve("work/gpt-5.5")
	require.NoError(t, err)
	assert.Equal(t, int64(80000), origin.CompactThreshold)
}

func TestModelResolverRejectsUnknownProviderWithoutRequest(t *testing.T) {
	requests := make(chan struct{}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests <- struct{}{}
	}))
	t.Cleanup(server.Close)

	resolver := newModelResolver(&config.Config{OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}, Providers: map[string]config.OpenAIConfig{"work": {APIBaseURL: server.URL}}}, slog.New(slog.DiscardHandler))
	for _, model := range []string{"missing/gpt-5.5", "/gpt-5.5", "work/", "work//gpt-5.5", "work/gpt-5.5/", " work/gpt-5.5", "work/ gpt-5.5", "work/gpt-5.5 ", "work/\tgpt-5.5"} {
		_, _, err := resolver.Resolve(model)
		require.Error(t, err, model)
	}

	select {
	case <-requests:
		t.Fatal("resolver sent an HTTP request for an invalid model")
	default:
	}
}

func TestModelResolverConstructsProviderSpecificAPIKeyAndChatGPTClients(t *testing.T) {
	apiKey := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey <- r.Header.Get("Authorization")

		if r.Header.Get("Authorization") == "Bearer chat-access-secret" {
			var request struct {
				Model string `json:"model"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			w.Header().Set("Content-Type", "text/event-stream")

			if request.Model == "failed-model" {
				_, err := io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\",\"message\":\"stream-body-secret\"}}}\n\n")
				assert.NoError(t, err)

				return
			}

			_, err := io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response\",\"output\":[]}}\n\n")
			assert.NoError(t, err)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", "ok")
	}))
	t.Cleanup(server.Close)

	workspace := t.TempDir()
	require.NoError(t, oai.SaveTokenIn(workspace, config.DefaultRuntimeDir, "chat", oai.Token{Refresh: "chat-refresh", Access: "chat-access-secret", Expires: time.Now().Add(time.Hour).UnixMilli()}))

	var logs lockedBuffer

	resolver := newModelResolver(&config.Config{
		Workspace: workspace,
		Providers: map[string]config.OpenAIConfig{
			"keyed": {APIKey: "provider-key", APIBaseURL: server.URL, RocketCodeAuth: "api_key"},
			"chat":  {APIKey: "must-not-be-used", APIBaseURL: server.URL, RocketCodeAuth: "chatgpt"},
		},
	}, slog.New(slog.NewJSONHandler(&logs, nil)).With("process_start", "test-start", "conversation_id", "conversation", "turn_id", "turn"))

	client, origin, err := resolver.Resolve("keyed/api-model")
	require.NoError(t, err)
	_, err = client.Responses.New(t.Context(), responses.ResponseNewParams{Model: origin.Model})
	require.NoError(t, err)
	assert.Equal(t, "Bearer provider-key", <-apiKey)

	client, origin, err = resolver.Resolve("chat/chat-model")
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, rocketcode.ProviderOrigin{Provider: "chat", Model: "chat-model"}, origin)
	_, err = client.Responses.New(t.Context(), responses.ResponseNewParams{Model: origin.Model}, option.WithBaseURL(server.URL), option.WithUnsafeAllowHTTP())
	require.NoError(t, err)
	assert.Equal(t, "Bearer chat-access-secret", <-apiKey)
	assert.Equal(t, 1, strings.Count(logs.String(), `"event":"provider_http_attempt"`))
	assert.Equal(t, 1, strings.Count(logs.String(), `"event":"provider_sdk_return"`))
	assert.Equal(t, 1, strings.Count(logs.String(), `"event":"provider_sdk_attempt"`), "only the API-key middleware event is an SDK attempt")
	assert.Contains(t, logs.String(), `"boundary":"sdk_return"`)
	assert.NotContains(t, logs.String(), "chat-access-secret")
	assert.NotContains(t, logs.String(), "must-not-be-used")
	assert.NotContains(t, logs.String(), server.URL)
	decoder := json.NewDecoder(strings.NewReader(logs.String()))

	for range 3 {
		var event struct {
			Provider       string `json:"provider"`
			Model          string `json:"model"`
			ProcessStart   string `json:"process_start"`
			ConversationID string `json:"conversation_id"`
			TurnID         string `json:"turn_id"`
		}
		require.NoError(t, decoder.Decode(&event))
		assert.Contains(t, []string{"keyed", "chat"}, event.Provider)
		assert.Contains(t, []string{"api-model", "chat-model"}, event.Model)
		assert.Equal(t, "test-start", event.ProcessStart)
		assert.Equal(t, "conversation", event.ConversationID)
		assert.Equal(t, "turn", event.TurnID)
	}

	logOffset := len(logs.String())
	_, err = client.Responses.New(t.Context(), responses.ResponseNewParams{Model: "failed-model"}, option.WithBaseURL(server.URL), option.WithUnsafeAllowHTTP(), option.WithMaxRetries(0))
	require.ErrorContains(t, err, "stream-body-secret", "returned errors retain their original details")
	assert.Equal(t, "Bearer chat-access-secret", <-apiKey)

	eventLogs := logs.String()[logOffset:]
	assert.Equal(t, 1, strings.Count(eventLogs, `"event":"provider_http_attempt"`))
	assert.Contains(t, eventLogs, `"status":200,"outcome":"success"`, "HTTP success precedes the failed stream conversion")
	assert.Equal(t, 1, strings.Count(eventLogs, `"event":"provider_sdk_return"`))
	assert.Contains(t, eventLogs, `"outcome":"sdk_error"`)
	assert.NotContains(t, eventLogs, `"event":"provider_sdk_attempt"`)
	assert.NotContains(t, eventLogs, "stream-body-secret")
	assert.NotContains(t, eventLogs, "chat-access-secret")
}

func TestModelResolverLogsProviderAndAPIModelWithoutCredentials(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++

		w.Header().Set("X-Request-ID", "req-test")

		if attempts == 1 {
			w.Header().Set("Retry-After-Ms", "1")
			http.Error(w, "body-secret", http.StatusTooManyRequests)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", "ok")
	}))
	t.Cleanup(server.Close)

	var logs lockedBuffer

	resolver := newModelResolver(&config.Config{Providers: map[string]config.OpenAIConfig{"work": {APIKey: "secret-key", APIBaseURL: server.URL + "/url-secret"}}}, slog.New(slog.NewJSONHandler(&logs, nil)))
	client, origin, err := resolver.Resolve("work/api-model")
	require.NoError(t, err)

	_, err = client.Responses.New(context.Background(), responses.ResponseNewParams{Model: origin.Model})
	require.NoError(t, err)
	assert.Equal(t, 2, attempts)
	assert.Equal(t, 2, strings.Count(logs.String(), `"event":"provider_sdk_attempt"`))
	assert.Contains(t, logs.String(), `"retry_count":0`)
	assert.Contains(t, logs.String(), `"retry_count":1`)
	assert.Contains(t, logs.String(), `"status":200`)
	assert.Contains(t, logs.String(), `"outcome":"success"`)
	assert.Contains(t, logs.String(), `"boundary":"http_headers"`)
	assert.Contains(t, logs.String(), `"duration_ms":`)
	assert.NotContains(t, logs.String(), "body-secret")

	for line := range strings.SplitSeq(logs.String(), "\n") {
		if strings.Contains(line, `"outcome":"success"`) {
			t.Logf("fast provider success: 1 event, %d JSON bytes (baseline: 0 events)", len(line)+1)
		}
	}

	server.Close()

	_, err = client.Responses.New(context.Background(), responses.ResponseNewParams{Model: origin.Model})
	require.Error(t, err)
	assert.Contains(t, logs.String(), `"error_type":"*url.Error"`)
	assert.NotContains(t, logs.String(), server.URL)
	assert.NotContains(t, logs.String(), "url-secret")
	assert.Contains(t, logs.String(), `"provider":"work"`)
	assert.Contains(t, logs.String(), `"model":"api-model"`)
	assert.NotContains(t, logs.String(), "secret-key")
	assert.NotContains(t, logs.String(), fmt.Sprint(config.OpenAIConfig{APIKey: "secret-key", APIBaseURL: server.URL}))
}

func BenchmarkProviderAttemptEvent(b *testing.B) {
	req, err := http.NewRequest(http.MethodPost, "http://localhost/responses", http.NoBody)
	if err != nil {
		b.Fatal(err)
	}

	req.Header.Set("X-Stainless-Retry-Count", "0")
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}

	var logs bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&logs, nil)).With("process_start", "test-start", "conversation_id", "conversation", "turn_id", "turn")

	b.ReportAllocs()

	for b.Loop() {
		logs.Reset()

		attrs := append(providerLogAttrs(req, resp, resp.StatusCode, time.Millisecond, nil, "http_headers"), "provider", "work", "model", "api-model")
		logger.Info("provider request completed", attrs...)
	}
}
