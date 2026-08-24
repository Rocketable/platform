package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/require"
)

type testModelResolverFunc func(string) (*openai.Client, ProviderOrigin, error)

func (f testModelResolverFunc) Resolve(model string) (*openai.Client, ProviderOrigin, error) {
	return f(model)
}

func testResolverClient(t *testing.T, output string) (client *openai.Client, requests <-chan string) {
	t.Helper()

	quotedOutput, err := json.Marshal(output)
	require.NoError(t, err)

	requestBodies := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}

		requestBodies <- string(data)

		w.Header().Set("Content-Type", "application/json")

		_, err = io.WriteString(w, `{"id":"resp-test","object":"response","created_at":1,"model":"test-model","output":[{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":`+string(quotedOutput)+`}]}],"parallel_tool_calls":true}`)
		if err != nil {
			t.Errorf("write response body: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	resolvedClient := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(server.URL))

	return &resolvedClient, requestBodies
}

func TestNewWithModelResolverRequiresResolverBeforeInitialization(t *testing.T) {
	_, err := NewWithModelResolver(nil, nil, nil, Agents{}, Skills{}, "", nil)

	require.EqualError(t, err, "resolver is required")
}

func TestNewWithModelResolverRejectsEmptyRootModelWithoutResolving(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	for _, model := range []string{"", "   "} {
		t.Run(model, func(t *testing.T) {
			calls := 0
			resolver := testModelResolverFunc(func(string) (*openai.Client, ProviderOrigin, error) {
				calls++
				client := openai.NewClient()

				return &client, ProviderOrigin{Provider: "openai", Model: "gpt-5.5"}, nil
			})

			_, err := NewWithModelResolver(resolver, testConfig(dir), root, Agents{Items: map[string]Agent{
				"main": {Name: "main", Model: model, Prompt: "prompt"},
			}}, Skills{Items: map[string]Skill{}}, "main", nil)

			require.ErrorContains(t, err, "required non-empty string")
			require.Zero(t, calls)
		})
	}
}

func TestNewAppliesAgentOutputSchema(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	loop, err := NewWithModelResolver(testModelResolverFunc(func(model string) (*openai.Client, ProviderOrigin, error) {
		return &client, ProviderOrigin{Provider: "openai", Model: model}, nil
	}), testConfig(dir), root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Model: "gpt-5.5", Prompt: "prompt", OutputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"answer": map[string]any{"type": "string"}},
			"required":   []any{"answer"},
		}},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)

	require.NoError(t, err)
	require.NotNil(t, loop.ResponseFormat.OfJSONSchema)
	require.Equal(t, "agent_output", loop.ResponseFormat.OfJSONSchema.Name)
	require.True(t, loop.ResponseFormat.OfJSONSchema.Strict.Value)
	require.Equal(t, "object", loop.ResponseFormat.OfJSONSchema.Schema["type"])
	require.Equal(t, false, loop.ResponseFormat.OfJSONSchema.Schema["additionalProperties"])
}

func TestNewWithModelResolverResolvesRootAgent(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	defaultClient, defaultRequests := testResolverClient(t, "wrong")
	workClient, workRequests := testResolverClient(t, "work answer")

	var resolved []string

	resolver := testModelResolverFunc(func(model string) (*openai.Client, ProviderOrigin, error) {
		resolved = append(resolved, model)
		if model == "work/gpt-5.5" {
			return workClient, ProviderOrigin{Provider: "work", Model: "gpt-5.5"}, nil
		}

		return defaultClient, ProviderOrigin{Provider: "openai", Model: model}, nil
	})
	loop, err := NewWithModelResolver(resolver, testConfig(dir), root, Agents{Items: map[string]Agent{
		"main":   {Name: "main", Model: "work/gpt-5.5", Prompt: "prompt"},
		"unused": {Name: "unused", Model: "   ", Prompt: "unused"},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"work/gpt-5.5"}, resolved)

	output := make(chan ChatResponse, 8)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "hello", output)

	close(input)
	require.NoError(t, loop.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1)))
	require.Len(t, workRequests, 1)
	require.Contains(t, <-workRequests, `"model":"gpt-5.5"`)
	require.Empty(t, defaultRequests)
}

func TestNewWithModelResolverRejectsUnknownProvider(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	tests := []struct {
		name     string
		resolve  testModelResolverFunc
		wantText string
	}{
		{name: "resolver error", resolve: func(string) (*openai.Client, ProviderOrigin, error) {
			return nil, ProviderOrigin{}, errors.New("unknown provider work")
		}, wantText: "unknown provider work"},
		{name: "nil client", resolve: func(string) (*openai.Client, ProviderOrigin, error) {
			return nil, ProviderOrigin{Provider: "work", Model: "gpt-5.5"}, nil
		}, wantText: "client is required"},
		{name: "empty provider", resolve: func(string) (*openai.Client, ProviderOrigin, error) {
			return &client, ProviderOrigin{Model: "gpt-5.5"}, nil
		}, wantText: "provider is required"},
		{name: "empty model", resolve: func(string) (*openai.Client, ProviderOrigin, error) {
			return &client, ProviderOrigin{Provider: "work"}, nil
		}, wantText: "model is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewWithModelResolver(tt.resolve, testConfig(dir), root, Agents{Items: map[string]Agent{
				"main": {Name: "main", Model: "work/gpt-5.5", Prompt: "prompt"},
			}}, Skills{Items: map[string]Skill{}}, "main", nil)

			require.ErrorContains(t, err, tt.wantText)
		})
	}
}

func TestNewUsesResolverCompactThreshold(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	cfg := testConfig(dir)
	cfg.CompactThreshold = 11111
	loop, err := NewWithModelResolver(testModelResolverFunc(func(string) (*openai.Client, ProviderOrigin, error) {
		return &client, ProviderOrigin{Provider: "work", Model: "gpt-5.5", CompactThreshold: 80000}, nil
	}), cfg, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Model: "work/gpt-5.5", Prompt: "prompt"},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)

	require.NoError(t, err)
	require.Equal(t, int64(80000), loop.CompactThreshold)
}

func TestNewUsesConfigCompactThresholdWhenResolverOmitsIt(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	cfg := testConfig(dir)
	cfg.CompactThreshold = 11111
	loop, err := NewWithModelResolver(testModelResolverFunc(func(string) (*openai.Client, ProviderOrigin, error) {
		return &client, ProviderOrigin{Provider: "openai", Model: "gpt-5.5"}, nil
	}), cfg, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Model: "gpt-5.5", Prompt: "prompt"},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)

	require.NoError(t, err)
	require.Equal(t, int64(11111), loop.CompactThreshold)
}

func TestNewWithModelResolverStoresResolvedDisplayModel(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client, _ := testResolverClient(t, "answer")
	resolver := testModelResolverFunc(func(string) (*openai.Client, ProviderOrigin, error) {
		return client, ProviderOrigin{Provider: "work", Model: "gpt-5.5"}, nil
	})
	loop, err := NewWithModelResolver(resolver, testConfig(dir), root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Model: "work/template", Prompt: "prompt"},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)

	require.NoError(t, err)
	require.Equal(t, "gpt-5.5", loop.Model)
	require.Equal(t, "work/gpt-5.5", loop.DisplayModel)
	require.Equal(t, ProviderOrigin{Provider: "work", Model: "gpt-5.5"}, loop.ProviderOrigin)

	output := make(chan ChatResponse, 8)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "hello", output)

	close(input)

	var saved SessionEntry

	require.NoError(t, loop.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = entry
		return nil
	}, make(chan os.Signal, 1)))
	require.Equal(t, "work/gpt-5.5", saved.Model)
}

func TestParseModelRef(t *testing.T) {
	for _, tc := range []struct{ name, model, apiModel, display string }{
		{name: "empty", model: "", apiModel: "gpt-5.5", display: "gpt-5.5"},
		{name: "openai", model: "gpt-5.5", apiModel: "gpt-5.5", display: "gpt-5.5"},
		{name: "legacy openai prefix", model: "openai/gpt-5.5", apiModel: "gpt-5.5", display: "gpt-5.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseModelRef(tc.model)

			require.NoError(t, err)
			require.Equal(t, tc.apiModel, parsed.apiModel)
			require.Equal(t, tc.display, parsed.display())
		})
	}
}

func TestParseModelRefRejectsInvalidOrUnsupportedQualifiedModel(t *testing.T) {
	for _, model := range []string{"/model", "openai/", "openai/gpt/extra", "openai-compatible/local/gpt-oss", "anthropic/claude"} {
		t.Run(model, func(t *testing.T) {
			_, err := parseModelRef(model)

			require.Error(t, err)
		})
	}
}

func TestResolveAgentModelRefRejectsEmptyAgentModel(t *testing.T) {
	for _, tc := range []struct{ name, model string }{
		{name: "empty", model: ""},
		{name: "whitespace", model: "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveAgentModelRef(tc.model)

			require.EqualError(t, err, "required non-empty string")
		})
	}
}

func TestNewWithProvidersNormalizesOpenAIPrefixedAgentModel(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	openAIClient := openai.NewClient()
	loop, err := NewWithProviders(Providers{OpenAI: &openAIClient}, testConfig(dir), root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Model: "openai/gpt-5.5", Prompt: "prompt"},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)

	require.NoError(t, err)
	require.Equal(t, "gpt-5.5", loop.DisplayModel)
	require.Equal(t, "gpt-5.5", loop.Model)
}

func TestNewWithProvidersRejectsNonOpenAIProviderQualifiedAgentModel(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	openAIClient := openai.NewClient()
	_, err = NewWithProviders(Providers{OpenAI: &openAIClient}, testConfig(dir), root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Model: "anthropic/claude", Prompt: "prompt"},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)

	require.EqualError(t, err, `agent "main" model: invalid model "anthropic/claude": expected unprefixed OpenAI model ID`)
}

func TestNewWithProvidersRejectsEmptyAgentModel(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	config := testConfig(dir)
	config.Model = "gpt-5.5"
	_, err = NewWithProviders(Providers{OpenAI: &client}, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Prompt: "prompt"},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)

	require.EqualError(t, err, `agent "main" model: required non-empty string`)
}

func TestOpenAIResponsesModeUsesResponsesEndpoint(t *testing.T) {
	route := ""
	body := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route = r.URL.Path

		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}

		body = string(data)

		w.Header().Set("Content-Type", "application/json")

		_, err = io.WriteString(w, `{"id":"resp-local","object":"response","created_at":1,"model":"gpt-5.5","output":[{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"responses answer"}]}],"parallel_tool_calls":true}`)
		if err != nil {
			t.Errorf("write response body: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(server.URL))
	loop := testOpenAILoop(t, &client)
	output := make(chan ChatResponse, 8)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "hello", output)

	close(input)

	err := loop.Loop(context.Background(), input, func(func(SessionEntry, error) bool) {}, func(SessionEntry) error { return nil }, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, "/responses", route)
	require.Contains(t, body, `"model":"gpt-5.5"`)
	require.NotContains(t, body, `"messages"`)
	require.Equal(t, []ChatResponse{assistantMessage("responses answer")}, collectResponses(output))
}

func TestOpenAIResponsesModeStripsPersistedCompactionMetadata(t *testing.T) {
	body := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("request path = %q; want /responses", r.URL.Path)
			http.NotFound(w, r)

			return
		}

		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}

		body = string(data)

		w.Header().Set("Content-Type", "application/json")

		_, err = io.WriteString(w, `{"id":"resp-local","object":"response","created_at":1,"model":"gpt-5.5","output":[{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"responses answer"}]}],"parallel_tool_calls":true}`)
		if err != nil {
			t.Errorf("write response body: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(server.URL))
	loop := testOpenAILoop(t, &client)
	output := make(chan ChatResponse, 8)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "hello", output)

	close(input)

	session := sessionEntries([]SessionEntry{{ReplayInput: []json.RawMessage{json.RawMessage(`{"content":"private-content-value","encrypted_content":"sealed","id":"cmp-1","recent":[{"id":"private-recent-id"}],"summary":{"text":"private-summary-value"},"type":"compaction"}`)}}})
	err := loop.Loop(context.Background(), input, session, func(SessionEntry) error { return nil }, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.NotContains(t, body, "private-content-value")
	require.NotContains(t, body, "private-summary-value")
	require.NotContains(t, body, "private-recent-id")
	require.Contains(t, body, `"encrypted_content":"sealed"`)
	require.Equal(t, []ChatResponse{assistantMessage("responses answer")}, collectResponses(output))
}

func testOpenAILoop(t *testing.T, client *openai.Client) *Runtime {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	config := testConfig(dir)
	config.Model = "gpt-5.5"
	loop, err := NewWithProviders(Providers{OpenAI: client}, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Model: "gpt-5.5", Prompt: "prompt"},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)
	require.NoError(t, err)

	return loop
}
