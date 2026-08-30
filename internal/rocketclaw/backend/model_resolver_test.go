package backend

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/oai"
	"github.com/Rocketable/platform/internal/rocketcode"
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
		Providers: map[string]config.OpenAIConfig{"work": {APIKey: "work-key", APIBaseURL: work.URL, RocketCodeAuth: "api_key"}},
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
	for _, model := range []string{"missing/gpt-5.5", "/gpt-5.5", "work/", "work/gpt-5.5/extra", " work/gpt-5.5", "work/ gpt-5.5", "work/gpt-5.5 ", "work/\tgpt-5.5"} {
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

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", "ok")
	}))
	t.Cleanup(server.Close)

	workspace := t.TempDir()
	require.NoError(t, oai.SaveTokenIn(workspace, config.DefaultRuntimeDir, "chat", oai.Token{Refresh: "chat-refresh"}))

	resolver := newModelResolver(&config.Config{
		Workspace: workspace,
		Providers: map[string]config.OpenAIConfig{
			"keyed": {APIKey: "provider-key", APIBaseURL: server.URL, RocketCodeAuth: "api_key"},
			"chat":  {APIKey: "must-not-be-used", APIBaseURL: server.URL, RocketCodeAuth: "chatgpt"},
		},
	}, slog.New(slog.DiscardHandler))

	client, origin, err := resolver.Resolve("keyed/api-model")
	require.NoError(t, err)
	_, err = client.Responses.New(t.Context(), responses.ResponseNewParams{Model: origin.Model})
	require.NoError(t, err)
	assert.Equal(t, "Bearer provider-key", <-apiKey)

	client, origin, err = resolver.Resolve("chat/chat-model")
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, rocketcode.ProviderOrigin{Provider: "chat", Model: "chat-model"}, origin)
}

func TestModelResolverLogsProviderAndAPIModelWithoutCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "blocked", http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	var logs lockedBuffer

	resolver := newModelResolver(&config.Config{Providers: map[string]config.OpenAIConfig{"work": {APIKey: "secret-key", APIBaseURL: server.URL}}}, slog.New(slog.NewJSONHandler(&logs, nil)))
	client, origin, err := resolver.Resolve("work/api-model")
	require.NoError(t, err)

	_, err = client.Responses.New(context.Background(), responses.ResponseNewParams{Model: origin.Model})
	require.Error(t, err)
	assert.Contains(t, logs.String(), `"provider":"work"`)
	assert.Contains(t, logs.String(), `"model":"api-model"`)
	assert.NotContains(t, logs.String(), "secret-key")
	assert.NotContains(t, logs.String(), fmt.Sprint(config.OpenAIConfig{APIKey: "secret-key", APIBaseURL: server.URL}))
}
