package harnessbridge

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunDevelopmentTurnReturnsThinkingAndAnswerFromTryTree(t *testing.T) {
	workspace := t.TempDir()
	runtimeDir := ".rocketclaw-devmcp-try"
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, runtimeDir, "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, runtimeDir, "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, runtimeDir, "agents", "main.md"), []byte("---\ndescription: Try Main\nmode: primary\nmodel: gpt-5.5\npermission:\n  read:\n    \"*\": allow\n---\nTRY TREE AGENT PROMPT\n"), 0o644))

	const (
		thinking = "considering the try tree"
		answer   = "try tree final answer"
	)

	var requestBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}

		if err := json.NewDecoder(r.Body).Decode(&requestBody); !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, err := fmt.Fprintf(w, `{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"rsn_1","type":"reasoning","summary":[{"type":"summary_text","text":%q}]},{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q,"annotations":[]}]}]}`, thinking, answer)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}
	result, err := RunDevelopmentTurn(t.Context(), cfg, filepath.Join(workspace, runtimeDir), "main", "what do you see?", slog.New(slog.DiscardHandler), new(DevelopmentChat))
	require.NoError(t, err)
	assert.Equal(t, DevelopmentTurnResult{Thinking: thinking, Answer: answer}, result)
	assert.Equal(t, workspace, cfg.Workspace)
	assert.Empty(t, cfg.WorkDir)
	assert.NoFileExists(t, filepath.Join(workspace, ".rocketclaw", "agents", "main.md"))
	assert.FileExists(t, filepath.Join(workspace, runtimeDir, "agents", "main.md"))
	assert.Contains(t, fmt.Sprint(requestBody["instructions"]), "TRY TREE AGENT PROMPT")
	assert.NotContains(t, fmt.Sprint(requestBody), "[Cron media=Text]")
	assert.Contains(t, fmt.Sprint(requestBody["tools"]), "execute")
}

func TestRunDevelopmentTurnReusesChatAcrossPrompts(t *testing.T) {
	workspace := t.TempDir()
	runtimeDir := ".rocketclaw-devmcp-try"
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, runtimeDir, "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, runtimeDir, "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, runtimeDir, "agents", "main.md"), []byte("---\ndescription: Try Main\nmode: primary\nmodel: gpt-5.5\npermission:\n  read:\n    \"*\": allow\n---\nTRY TREE AGENT PROMPT\n"), 0o644))

	var requests []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		requests = append(requests, body)

		w.Header().Set("Content-Type", "application/json")
		_, err := fmt.Fprintf(w, `{"id":"resp_%d","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_%d","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q,"annotations":[]}]}]}`, len(requests), len(requests), fmt.Sprintf("answer %d", len(requests)))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}
	chat := new(DevelopmentChat)
	first, err := RunDevelopmentTurn(t.Context(), cfg, filepath.Join(workspace, runtimeDir), "main", "first prompt", slog.New(slog.DiscardHandler), chat)
	require.NoError(t, err)
	assert.Equal(t, "answer 1", first.Answer)
	require.Len(t, chat.memory.entries, 1)

	second, err := RunDevelopmentTurn(t.Context(), cfg, filepath.Join(workspace, runtimeDir), "main", "second prompt", slog.New(slog.DiscardHandler), chat)
	require.NoError(t, err)
	assert.Equal(t, "answer 2", second.Answer)
	require.Len(t, requests, 2)
	require.Len(t, chat.memory.entries, 2)
	assert.Contains(t, fmt.Sprint(requests[1]["input"]), "first prompt")
	assert.Contains(t, fmt.Sprint(requests[1]["input"]), "second prompt")
}

func TestRunDevelopmentTurnReloadDoesNotChangeLiveAssets(t *testing.T) {
	workspace := t.TempDir()
	runtimeDir := ".rocketclaw-devmcp-try"
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, runtimeDir, "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, runtimeDir, "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, runtimeDir, "agents", "main.md"), []byte("---\ndescription: Try Main\nmode: primary\nmodel: gpt-5.5\npermission:\n  read:\n    \"*\": allow\n  rocketclaw:\n    rocketclaw_reload: allow\n    rocketclaw_restart: allow\n---\nTRY TREE AGENT PROMPT\n"), 0o644))

	liveFile := filepath.Join(workspace, ".rocketclaw", "agents", "main.md")
	liveBytes := []byte("live runtime must stay put\n")

	require.NoError(t, os.MkdirAll(filepath.Dir(liveFile), 0o755))
	require.NoError(t, os.WriteFile(liveFile, liveBytes, 0o644))

	var requests []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		requests = append(requests, body)

		w.Header().Set("Content-Type", "application/json")

		switch len(requests) {
		case 1:
			writeRawRunFunctionCall(t, w, "resp_1", "call_1", reloadToolName, map[string]string{"reason": "try overlay changed"})
		case 2:
			_, err := fmt.Fprintf(w, `{"id":"resp_2","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"done","annotations":[]}]}]}`)
			assert.NoError(t, err)
		default:
			t.Fatalf("unexpected response request %d", len(requests))
		}
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}
	result, err := RunDevelopmentTurn(t.Context(), cfg, filepath.Join(workspace, runtimeDir), "main", "reload now", slog.New(slog.DiscardHandler), new(DevelopmentChat))
	require.NoError(t, err)
	assert.Equal(t, "done", result.Answer)
	require.Len(t, requests, 2)
	assert.Contains(t, fmt.Sprint(requests[0]["tools"]), reloadToolName)
	assert.Contains(t, fmt.Sprint(requests[0]["tools"]), restartToolName)
	assert.Contains(t, fmt.Sprint(requests[1]["input"]), "rocketclaw runtime assets reloaded")

	got, err := os.ReadFile(liveFile)
	require.NoError(t, err)
	assert.Equal(t, liveBytes, got)
}
