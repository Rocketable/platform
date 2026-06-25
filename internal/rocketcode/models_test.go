package rocketcode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/require"
)

func TestParseModelRef(t *testing.T) {
	for _, tc := range []struct {
		name               string
		model              string
		provider           string
		compatibleProvider string
		apiModel           string
		display            string
	}{
		{name: "empty", model: "", provider: "openai", apiModel: "gpt-5.4", display: "openai/gpt-5.4"},
		{name: "openai", model: "openai/gpt-5.5", provider: "openai", apiModel: "gpt-5.5", display: "openai/gpt-5.5"},
		{name: "anthropic", model: "anthropic/claude-sonnet-4-20250514", provider: "anthropic", apiModel: "claude-sonnet-4-20250514", display: "anthropic/claude-sonnet-4-20250514"},
		{name: "openai-compatible", model: "openai-compatible/local/gpt-oss", provider: "openai-compatible", compatibleProvider: "local", apiModel: "gpt-oss", display: "openai-compatible/local/gpt-oss"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseModelRef(tc.model)

			require.NoError(t, err)
			require.Equal(t, tc.provider, parsed.provider)
			require.Equal(t, tc.compatibleProvider, parsed.compatibleProvider)
			require.Equal(t, tc.apiModel, parsed.apiModel)
			require.Equal(t, tc.display, parsed.display())
		})
	}
}

func TestParseModelRefRejectsInvalidProvider(t *testing.T) {
	for _, model := range []string{"gpt-5.5", "anthropic/", "/model", "bogus/model", "openai/gpt/extra", "openai-compatible/local", "openai-compatible//gpt", "openai-compatible/local/gpt/extra"} {
		t.Run(model, func(t *testing.T) {
			_, err := parseModelRef(model)

			require.Error(t, err)
		})
	}
}

func TestNewWithProvidersRoutesAnthropicModel(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	config := testConfig(dir)
	config.Model = "anthropic/claude-sonnet-4-20250514"
	client := anthropic.NewClient(anthropicoption.WithAPIKey("test-key"))
	loop, err := NewWithProviders(Providers{Anthropic: &client}, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Model: "anthropic/claude-sonnet-4-20250514", Prompt: "prompt"},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)

	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-20250514", loop.Model)
	require.Equal(t, "anthropic/claude-sonnet-4-20250514", loop.DisplayModel)
}

func TestNewWithProvidersRequiresSelectedProvider(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	config := testConfig(dir)
	config.Model = "anthropic/claude-sonnet-4-20250514"
	openAIClient := openai.NewClient()
	_, err = NewWithProviders(Providers{OpenAI: &openAIClient}, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Model: "anthropic/claude-sonnet-4-20250514", Prompt: "prompt"},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)

	require.EqualError(t, err, `agent "main" model: anthropic provider is required`)
}

func TestNewWithProvidersRoutesOpenAICompatibleResponsesMode(t *testing.T) {
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

		_, err = io.WriteString(w, `{"id":"resp-local","object":"response","created_at":1,"model":"gpt-oss","output":[{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"responses answer"}]}],"parallel_tool_calls":true}`)
		if err != nil {
			t.Errorf("write response body: %v", err)
			return
		}
	}))
	t.Cleanup(server.Close)

	client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(server.URL))
	loop := testCompatibleLoop(t, &client, OpenAICompatibleModeResponses)
	output := make(chan ChatResponse, 8)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "hello", output)

	close(input)

	err := loop.Loop(context.Background(), input, func(func(SessionEntry, error) bool) {}, func(SessionEntry) error { return nil }, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, "/responses", route)
	require.Contains(t, body, `"model":"gpt-oss"`)
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

	session := sessionEntries([]SessionEntry{{ReplayInput: []json.RawMessage{json.RawMessage(`{"content":"private-content-value","encrypted_content":"sealed","id":"cmp-1","origin_compatible_provider":"local","origin_mode":"responses","origin_provider":"openai","recent":[{"id":"private-recent-id"}],"summary":{"text":"private-summary-value"},"type":"compaction"}`)}}})
	err := loop.Loop(context.Background(), input, session, func(SessionEntry) error { return nil }, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.NotContains(t, body, "origin_provider")
	require.NotContains(t, body, "origin_compatible_provider")
	require.NotContains(t, body, "origin_mode")
	require.NotContains(t, body, "private-content-value")
	require.NotContains(t, body, "private-summary-value")
	require.NotContains(t, body, "private-recent-id")
	require.Contains(t, body, `"encrypted_content":"sealed"`)
	require.Equal(t, []ChatResponse{assistantMessage("responses answer")}, collectResponses(output))
}

func TestOpenAICompatibleResponsesModeStripsPersistedCompactionMetadata(t *testing.T) {
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

		_, err = io.WriteString(w, `{"id":"resp-local","object":"response","created_at":1,"model":"gpt-oss","output":[{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"responses answer"}]}],"parallel_tool_calls":true}`)
		if err != nil {
			t.Errorf("write response body: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(server.URL))
	loop := testCompatibleLoop(t, &client, OpenAICompatibleModeResponses)
	output := make(chan ChatResponse, 8)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "hello", output)

	close(input)

	session := sessionEntries([]SessionEntry{{ReplayInput: []json.RawMessage{json.RawMessage(`{"content":"private-content-value","encrypted_content":"sealed","id":"cmp-1","origin_compatible_provider":"local","origin_mode":"responses","origin_provider":"openai-compatible","recent":[{"id":"private-recent-id"}],"summary":{"text":"private-summary-value"},"type":"compaction"}`)}}})
	err := loop.Loop(context.Background(), input, session, func(SessionEntry) error { return nil }, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.NotContains(t, body, "origin_provider")
	require.NotContains(t, body, "origin_compatible_provider")
	require.NotContains(t, body, "origin_mode")
	require.NotContains(t, body, "private-content-value")
	require.NotContains(t, body, "private-summary-value")
	require.NotContains(t, body, "private-recent-id")
	require.Contains(t, body, `"encrypted_content":"sealed"`)
	require.Equal(t, []ChatResponse{assistantMessage("responses answer")}, collectResponses(output))
}

func TestOpenAICompatibleChatCompletionsModeStripsPersistedCompactionMetadata(t *testing.T) {
	body := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("request path = %q; want /chat/completions", r.URL.Path)
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

		_, err = io.WriteString(w, `{"id":"chatcmpl-local","object":"chat.completion","created":1,"model":"gpt-oss","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"chat answer"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
		if err != nil {
			t.Errorf("write response body: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(server.URL))
	loop := testCompatibleLoop(t, &client, OpenAICompatibleModeChatCompletions)
	output := make(chan ChatResponse, 8)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "hello", output)

	close(input)

	session := sessionEntries([]SessionEntry{{ReplayInput: []json.RawMessage{json.RawMessage(`{"content":"private-checkpoint-value","encrypted_content":"private-encrypted-value","id":"cmp-1","origin_compatible_provider":"local","origin_mode":"responses","origin_provider":"openai-compatible","recent":[{"id":"private-recent-id"}],"summary":{"text":"private-summary-value"},"type":"compaction"}`)}}})
	err := loop.Loop(context.Background(), input, session, func(SessionEntry) error { return nil }, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.NotContains(t, body, "origin_provider")
	require.NotContains(t, body, "origin_compatible_provider")
	require.NotContains(t, body, "origin_mode")
	require.NotContains(t, body, "private-encrypted-value")
	require.NotContains(t, body, "private-summary-value")
	require.NotContains(t, body, "private-recent-id")
	require.Contains(t, body, "private-checkpoint-value")
	require.Contains(t, body, "context_checkpoint")
	require.Equal(t, []ChatResponse{assistantMessage("chat answer")}, collectResponses(output))
}

func TestNewWithProvidersRoutesOpenAICompatibleChatCompletionsMode(t *testing.T) {
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

		_, err = io.WriteString(w, `{"id":"chatcmpl-local","object":"chat.completion","created":1,"model":"gpt-oss","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"chat answer"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
		if err != nil {
			t.Errorf("write response body: %v", err)
			return
		}
	}))
	t.Cleanup(server.Close)

	client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(server.URL))
	loop := testCompatibleLoop(t, &client, OpenAICompatibleModeChatCompletions)
	output := make(chan ChatResponse, 8)

	input := make(chan PromptInput, 1)
	input <- testPromptInputWithAttachments(PromptInputRoleUser, "hello", []Attachment{
		{MIME: "image/png", Filename: "image.png", URL: "data:image/png;base64,aW1hZ2U="},
		{MIME: "application/pdf", Filename: "doc.pdf", URL: "data:application/pdf;base64,cGRm"},
	}, output)

	close(input)

	err := loop.Loop(context.Background(), input, func(func(SessionEntry, error) bool) {}, func(SessionEntry) error { return nil }, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, "/chat/completions", route)
	require.Contains(t, body, `"model":"gpt-oss"`)
	require.Contains(t, body, `"messages"`)
	require.Contains(t, body, `"type":"image_url"`)
	require.Contains(t, body, `"url":"data:image/png;base64,aW1hZ2U="`)
	require.Contains(t, body, `"type":"file"`)
	require.Contains(t, body, `"filename":"doc.pdf"`)
	require.Contains(t, body, `"file_data":"data:application/pdf;base64,cGRm"`)
	require.NotContains(t, body, `"input"`)
	require.NotContains(t, body, `"context_management"`)
	require.Equal(t, []ChatResponse{assistantMessage("chat answer")}, collectResponses(output))
}

func testCompatibleLoop(t *testing.T, client *openai.Client, mode OpenAICompatibleMode) *Runtime {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	loop, err := NewWithProviders(Providers{OpenAICompatible: map[string]OpenAICompatibleProvider{"local": {Client: *client, Mode: mode}}}, testConfig(dir), root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Model: "openai-compatible/local/gpt-oss", Prompt: "prompt"},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)
	require.NoError(t, err)

	return loop
}

func testOpenAILoop(t *testing.T, client *openai.Client) *Runtime {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	config := testConfig(dir)
	config.Model = "openai/gpt-5.5"
	loop, err := NewWithProviders(Providers{OpenAI: client}, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Model: "openai/gpt-5.5", Prompt: "prompt"},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)
	require.NoError(t, err)

	return loop
}
