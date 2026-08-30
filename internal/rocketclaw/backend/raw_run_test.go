package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRunRawReturnsPreLooperErrorsAndLogs(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var logs bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	cfg := new(config.Config)
	cfg.Workspace = workspace

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	type result struct {
		err error
	}

	done := make(chan result, 1)

	go func() {
		_, err := RunRawWithProgress(ctx, cfg, "main", "prompt", logger, nil)
		done <- result{err: err}
	}()

	select {
	case got := <-done:
		require.Error(t, got.err)
		require.ErrorContains(t, got.err, `missing required default agent "main"`)
	case <-time.After(time.Second):
		t.Fatal("RunRaw hung after rocketcode returned before closing output")
	}

	assert.DirExists(t, filepath.Join(workspace, ".rocketclaw", ".rocketcode"))
}

func TestRunRawReportsInvalidMaxRecursion(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.4\nmaxRecursion: nope\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	_, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace}, "main", "prompt", slog.New(slog.DiscardHandler), nil)

	require.ErrorContains(t, err, "main.md: parse maxRecursion:")
}

func TestInertRawRunProgressCallbacksAreNoops(t *testing.T) {
	progress := newInertRawRunProgress()

	require.NoError(t, progress.Thinking(context.Background(), "thinking"))
	require.NoError(t, progress.Message(context.Background(), "message"))

	restarted, err := progress.RequestRestart(context.Background(), "reason")
	require.NoError(t, err)
	assert.Empty(t, restarted)
}

func TestRawRunDecisionToolStoresPayload(t *testing.T) {
	decision := new(rawRunDecision)
	_, ok := decision.Decision()
	assert.False(t, ok)

	tool := decision.Tool()
	_, err := tool.Call(context.Background(), json.RawMessage("{"), make(chan rocketcode.ChatResponse))
	require.ErrorContains(t, err, "parse raw run decision")

	result, err := tool.Call(context.Background(), json.RawMessage(`{"payload":"ship it"}`), make(chan rocketcode.ChatResponse))
	require.NoError(t, err)
	assert.Equal(t, "queued for verbatim delivery", result.Output)

	payload, ok := decision.Decision()
	require.True(t, ok)
	assert.Equal(t, "ship it", payload)
}

func TestRunRawCronCanEditRestartAndCompleteDecision(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission:\n  edit: allow\n  rocketclaw:\n    rocketclaw_restart: allow\n    rocketclaw_schedule_message: allow\n---\nPrompt\n")
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))
	require.NoError(t, root.MkdirAll("cron", 0o755))
	require.NoError(t, root.WriteFile("cron/HEARTBEAT.md", []byte("old heartbeat\n"), 0o644))
	require.NoError(t, root.WriteFile("rocketclaw.json", []byte("{\"name\":\"old\"}\n"), 0o644))

	var (
		requestMu sync.Mutex
		requests  int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		requestMu.Lock()
		requests++
		request := requests
		requestMu.Unlock()

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		if request == 1 {
			data, err := json.Marshal(body["tools"])
			assert.NoError(t, err)
			assert.Contains(t, string(data), restartToolName)
			assert.Contains(t, string(data), reloadToolName)
			assert.Contains(t, string(data), attachFilesToolName)
			assert.NotContains(t, string(data), scheduleMessageToolName)
			assert.NotContains(t, string(data), resetScheduledMessagesToolName)
		}

		w.Header().Set("Content-Type", "application/json")

		switch request {
		case 1:
			writeRawRunFunctionCall(t, w, "resp_1", "call_1", "execute", executeApplyPatchScript("*** Begin Patch\n*** Update File: cron/HEARTBEAT.md\n@@\n-old heartbeat\n+new heartbeat\n*** End Patch"))
		case 2:
			writeRawRunFunctionCall(t, w, "resp_2", "call_2", "execute", executeApplyPatchScript("*** Begin Patch\n*** Update File: rocketclaw.json\n@@\n-{\"name\":\"old\"}\n+{\"name\":\"new\"}\n*** End Patch"))
		case 3:
			writeRawRunFunctionCall(t, w, "resp_3", "call_3", restartToolName, map[string]string{"reason": "rocketclaw.json changed and runtime config must reload"})
		case 4:
			writeRawRunFunctionCall(t, w, "resp_4", "call_4", rawRunToolName, map[string]string{"payload": "cron done"})
		case 5:
			_, err := w.Write([]byte(`{"id":"resp_5","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"assistant complete","annotations":[]}]}]}`))
			assert.NoError(t, err)
		default:
			t.Fatalf("unexpected response request %d", request)
		}
	}))
	t.Cleanup(server.Close)

	restarts := 0
	progress := newInertRawRunProgress()
	progress.SessionService, err = NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, progress.SessionService.Stop(context.Background())) })

	progress.ConversationID = "cron:test:trace"
	progress.RequestRestart = func(_ context.Context, reason string) (string, error) {
		restarts++

		assert.Equal(t, "rocketclaw.json changed and runtime config must reload", reason)

		return "", nil
	}
	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}

	result, err := RunRawWithProgress(t.Context(), cfg, "main", "!`printf raw-expanded`", slog.New(slog.DiscardHandler), progress)
	require.NoError(t, err)
	assert.Equal(t, RawRunResult{Text: "assistant complete", VerbatimMessage: "cron done"}, result)
	assert.Equal(t, 1, restarts)
	entries, err := ObserveSessionEntries(t.Context(), workspace, config.DefaultRuntimeDir, testStoreDSN(workspace), progress.ConversationID, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	items, err := rocketcode.ReplayInputToParams(entries[0].Entry.ReplayInput)
	require.NoError(t, err)
	assert.Equal(t, "[Cron media=Text]\n\nraw-expanded", items[0].OfMessage.Content.OfString.Value)
	requestMu.Lock()
	assert.Equal(t, 5, requests)
	requestMu.Unlock()
	assertFileContent(t, root, "cron/HEARTBEAT.md", "new heartbeat\n")
	assertFileContent(t, root, "rocketclaw.json", "{\"name\":\"new\"}\n")
}

func TestRunRawHidesRestartWithoutExplicitAllow(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var (
		requestMu sync.Mutex
		requests  int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		requestMu.Lock()
		requests++
		request := requests
		requestMu.Unlock()

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		if request == 1 {
			var tools []struct {
				Name string `json:"name"`
			}

			data, err := json.Marshal(body["tools"])
			if !assert.NoError(t, err) || !assert.NoError(t, json.Unmarshal(data, &tools)) {
				http.Error(w, "decode tools", http.StatusInternalServerError)

				return
			}

			toolNames := make([]string, 0, len(tools))
			for _, tool := range tools {
				toolNames = append(toolNames, tool.Name)
			}

			assert.NotContains(t, toolNames, restartToolName)
			assert.Contains(t, toolNames, reloadToolName)
			assert.NotContains(t, toolNames, scheduleMessageToolName)
			assert.NotContains(t, toolNames, resetScheduledMessagesToolName)
			assert.NotContains(t, toolNames, startNewThreadToolName)
			assert.Contains(t, toolNames, rawRunToolName)
		}

		w.Header().Set("Content-Type", "application/json")

		switch request {
		case 1:
			writeRawRunFunctionCall(t, w, "resp_1", "call_1", rawRunToolName, map[string]string{"payload": "done"})
		case 2:
			_, err := w.Write([]byte(`{"id":"resp_2","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"assistant complete","annotations":[]}]}]}`))
			assert.NoError(t, err)
		default:
			t.Fatalf("unexpected response request %d", request)
		}
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}
	result, err := RunRawWithProgress(t.Context(), cfg, "main", "prompt", slog.New(slog.DiscardHandler), nil)
	require.NoError(t, err)
	assert.Equal(t, RawRunResult{Text: "assistant complete", VerbatimMessage: "done"}, result)
	requestMu.Lock()
	assert.Equal(t, 2, requests)
	requestMu.Unlock()
}

func TestRunRawStartNewThreadAvailability(t *testing.T) {
	tests := []struct {
		name, permission, channel string
		want                      bool
	}{
		{name: "allow with channel", permission: "    rocketclaw_start_new_thread: allow\n", channel: "#ops", want: true},
		{name: "missing allow", permission: "", channel: "#ops", want: false},
		{name: "auto", permission: "    rocketclaw_start_new_thread: auto\n", channel: "#ops", want: false},
		{name: "deny", permission: "    rocketclaw_start_new_thread: deny\n", channel: "#ops", want: false},
		{name: "no channel", permission: "    rocketclaw_start_new_thread: allow\n", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()

			permission := "permission: {}\n"
			if tt.permission != "" {
				permission = "permission:\n  rocketclaw:\n" + tt.permission
			}

			writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\n"+permission+"---\nPrompt\n")
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

			progress := newInertRawRunProgress()
			progress.TextChannel = tt.channel

			toolNames := rawRunToolNames(t, workspace, progress)
			if tt.want {
				assert.Contains(t, toolNames, startNewThreadToolName)
			} else {
				assert.NotContains(t, toolNames, startNewThreadToolName)
			}

			assert.NotContains(t, toolNames, scheduleMessageToolName)
		})
	}
}

func TestRunRawStartNewThreadLocksChannelAndAgent(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission:\n  rocketclaw:\n    rocketclaw_start_new_thread: allow\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var captured *protocol.StartNewThreadRequest

	progress := newInertRawRunProgress()
	progress.TextChannel = "#ops"
	progress.StartNewThread = func(_ context.Context, req *protocol.StartNewThreadRequest) (protocol.StartNewThreadResult, error) {
		captured = req
		return protocol.StartNewThreadResult{ConversationID: "slack-thread:C1:2"}, nil
	}

	var (
		requestMu sync.Mutex
		requests  int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}

		requestMu.Lock()
		requests++
		request := requests
		requestMu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		switch request {
		case 1:
			writeRawRunFunctionCall(t, w, "resp_1", "call_1", startNewThreadToolName, map[string]string{"title": "Nightly", "prompt": "run suite", "agent": "other"})
		case 2:
			writeRawRunFunctionCall(t, w, "resp_2", "call_2", rawRunToolName, map[string]string{"payload": ""})
		case 3:
			writeRawRunMessage(t, w, "resp_3", "msg_1", "assistant complete")
		default:
			t.Fatalf("unexpected response request %d", request)
		}
	}))
	t.Cleanup(server.Close)

	_, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", "prompt", slog.New(slog.DiscardHandler), progress)
	require.NoError(t, err)
	require.NotNil(t, captured)
	assert.Equal(t, protocol.SourceSystem, captured.Source)
	require.NotNil(t, captured.SlackReply)
	assert.Equal(t, "#ops", captured.SlackReply.ChannelID)
	assert.Equal(t, "main", captured.CurrentAgent)
	assert.Equal(t, []string{"main"}, captured.AllowedAgents)
	assert.Equal(t, "other", captured.Agent)
	assert.Nil(t, captured.Response)
}

func TestRunRawOptsOutOfActiveTurnRecoveryWithConversationKey(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		requests++
		if requests == 1 {
			turns, err := service.RecoverableActiveTurns(r.Context())
			if !assert.NoError(t, err) {
				return
			}

			assert.Empty(t, turns)

			w.Header().Set("Content-Type", "application/json")
			writeRawRunFunctionCall(t, w, "resp_1", "call_1", rawRunToolName, map[string]string{"payload": "done"})

			return
		}

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "resp_2", "msg_1", "assistant done")
	}))
	t.Cleanup(server.Close)

	progress := newInertRawRunProgress()
	progress.SessionService = service
	progress.ConversationID = "cron:durable"

	result, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", "prompt", slog.New(slog.DiscardHandler), progress)
	require.NoError(t, err)
	assert.Equal(t, RawRunResult{Text: "assistant done", VerbatimMessage: "done"}, result)

	turns, err := service.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	assert.Empty(t, turns)
}

func TestRunRawOptsOutOfCheckpointsWithoutConversationKey(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		requests++
		if requests == 1 {
			turns, err := service.RecoverableActiveTurns(r.Context())
			if !assert.NoError(t, err) {
				return
			}

			assert.Empty(t, turns)

			w.Header().Set("Content-Type", "application/json")
			writeRawRunFunctionCall(t, w, "resp_1", "call_1", rawRunToolName, map[string]string{"payload": "done"})

			return
		}

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "resp_2", "msg_1", "assistant done")
	}))
	t.Cleanup(server.Close)

	progress := newInertRawRunProgress()
	progress.SessionService = service

	_, err = RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", "prompt", slog.New(slog.DiscardHandler), progress)
	require.NoError(t, err)
}

func TestRunRawRetriesMissingMandatoryToolUntilDecision(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	requests := 0

	var prompts []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		requests++

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		if requests <= 2 {
			prompts = append(prompts, rawRunRequestPrompt(t, body))
		}

		w.Header().Set("Content-Type", "application/json")

		switch requests {
		case 1:
			assert.NotContains(t, fmt.Sprint(body), rawRunMissingToolPrompt)

			_, err := w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ignored","annotations":[]}]}]}`))
			assert.NoError(t, err)
		case 2:
			assert.Contains(t, fmt.Sprint(body), rawRunMissingToolPrompt)
			writeRawRunFunctionCall(t, w, "resp_2", "call_2", rawRunToolName, map[string]string{"payload": "final payload"})
		case 3:
			_, err := w.Write([]byte(`{"id":"resp_3","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"assistant text","annotations":[]}]}]}`))
			assert.NoError(t, err)
		default:
			t.Fatalf("unexpected response request %d", requests)
		}
	}))
	t.Cleanup(server.Close)

	result, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", "original", slog.New(slog.DiscardHandler), nil)
	require.NoError(t, err)
	assert.Equal(t, RawRunResult{Text: "assistant text", VerbatimMessage: "final payload"}, result)
	assert.Equal(t, 3, requests)
	assert.Equal(t, []string{"[Cron media=Text]\n\noriginal", "[Cron media=Text]\n\n" + rawRunMissingToolPrompt}, prompts)
}

func rawRunRequestPrompt(t *testing.T, body map[string]any) string {
	t.Helper()

	input, ok := body["input"].([]any)
	require.True(t, ok)

	if len(input) == 0 {
		return ""
	}

	for _, v := range slices.Backward(input) {
		message, ok := v.(map[string]any)
		require.True(t, ok)

		content, ok := message["content"].(string)
		if ok {
			return content
		}
	}

	return ""
}

func TestRunRawLoadsSkillContentAsDeveloperMessage(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission:\n  skill:\n    demo: allow\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills", "demo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".rocketclaw", "skills", "demo", "SKILL.md"), []byte("---\nname: demo\ndescription: Demo skill\n---\nDemo skill body\n"), 0o644))

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		requests++

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		w.Header().Set("Content-Type", "application/json")

		switch requests {
		case 1:
			writeRawRunFunctionCall(t, w, "resp_1", "call_1", "skill", map[string]string{"name": "demo"})
		case 2:
			bodyText := fmt.Sprint(body)
			assert.Contains(t, bodyText, "skill demo loaded")
			assert.Contains(t, bodyText, "Demo skill body")
			assert.Contains(t, bodyText, "developer")
			writeRawRunFunctionCall(t, w, "resp_2", "call_2", rawRunToolName, map[string]string{"payload": "done"})
		case 3:
			_, err := w.Write([]byte(`{"id":"resp_3","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"assistant text","annotations":[]}]}]}`))
			assert.NoError(t, err)
		default:
			t.Fatalf("unexpected response request %d", requests)
		}
	}))
	t.Cleanup(server.Close)

	result, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", "prompt", slog.New(slog.DiscardHandler), nil)
	require.NoError(t, err)
	assert.Equal(t, RawRunResult{Text: "assistant text", VerbatimMessage: "done"}, result)
	assert.Equal(t, 3, requests)
}

func TestRunRawPassesLocalGuardrailToRocketCode(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission:\n  task:\n    helper: allow\n---\nPrompt\n")
	writeAgent(t, workspace, "helper", "---\ndescription: Helper\nmodel: gpt-5.5\nguardrail: guardrail\n---\nHelper prompt\n")
	writeAgent(t, workspace, "guardrail", "---\ndescription: Guardrail\nmodel: gpt-5.5\n---\nGuard delegated work\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		requests++

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		w.Header().Set("Content-Type", "application/json")

		switch requests {
		case 1:
			writeRawRunFunctionCall(t, w, "resp_1", "call_1", "task", map[string]string{"description": "delegate", "prompt": "delegated prompt", "subagent_type": "helper"})
		case 2:
			assert.Contains(t, fmt.Sprint(body["instructions"]), "Guard delegated work")
			assert.Contains(t, fmt.Sprint(body), "Current Action: delegation")
			assert.Contains(t, fmt.Sprint(body), "The agent main wants to delegate to helper")
			assert.Contains(t, fmt.Sprint(body), "delegated prompt")
			assert.Contains(t, fmt.Sprint(body["text"]), "json_schema")
			writeRawRunMessage(t, w, "resp_2", "msg_2", `{"approved":true,"reason":""}`)
		case 3:
			assert.Contains(t, fmt.Sprint(body["instructions"]), "Helper prompt")
			assert.Contains(t, fmt.Sprint(body), "delegated prompt")
			writeRawRunMessage(t, w, "resp_3", "msg_3", "child response")
		case 4:
			assert.Contains(t, fmt.Sprint(body["instructions"]), "Guard delegated work")
			assert.Contains(t, fmt.Sprint(body), "Current Action: response")
			assert.Contains(t, fmt.Sprint(body), "And the response from helper to main")
			assert.Contains(t, fmt.Sprint(body), "child response")
			assert.Contains(t, fmt.Sprint(body["text"]), "json_schema")
			writeRawRunMessage(t, w, "resp_4", "msg_4", `{"approved":true,"reason":""}`)
		case 5:
			assert.Contains(t, fmt.Sprint(body), "<task_result>\nchild response\n</task_result>")
			writeRawRunFunctionCall(t, w, "resp_5", "call_5", rawRunToolName, map[string]string{"payload": "done"})
		case 6:
			writeRawRunMessage(t, w, "resp_6", "msg_6", "assistant text")
		default:
			t.Fatalf("unexpected response request %d", requests)
		}
	}))
	t.Cleanup(server.Close)

	result, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", "prompt", slog.New(slog.DiscardHandler), nil)
	require.NoError(t, err)
	require.Equal(t, RawRunResult{Text: "assistant text", VerbatimMessage: "done"}, result)
	require.Equal(t, 6, requests)
}

func TestRunRawReturnsQueuedAttachmentsWithNonEmptyDecision(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "report.txt"), []byte("report body"), 0o644))

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		requests++

		w.Header().Set("Content-Type", "application/json")

		switch requests {
		case 1:
			writeRawRunFunctionCall(t, w, "resp_1", "call_1", attachFilesToolName, map[string]any{"attachments": []map[string]string{{"path": "report.txt"}}})
		case 2:
			writeRawRunFunctionCall(t, w, "resp_2", "call_2", rawRunToolName, map[string]string{"payload": "final payload"})
		case 3:
			_, err := w.Write([]byte(`{"id":"resp_3","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"assistant text","annotations":[]}]}]}`))
			assert.NoError(t, err)
		default:
			t.Fatalf("unexpected response request %d", requests)
		}
	}))
	t.Cleanup(server.Close)

	result, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", "prompt", slog.New(slog.DiscardHandler), nil)
	require.NoError(t, err)
	assert.Equal(t, "assistant text", result.Text)
	assert.Equal(t, "final payload", result.VerbatimMessage)
	assert.Equal(t, []protocol.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report body")}}, result.Attachments)
}

func TestRunRawDropsQueuedAttachmentsWithEmptyDecision(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		requests++

		w.Header().Set("Content-Type", "application/json")

		switch requests {
		case 1:
			writeRawRunFunctionCall(t, w, "resp_1", "call_1", attachFilesToolName, map[string]any{"attachments": []map[string]string{{"content": "hidden", "name": "hidden.txt"}}})
		case 2:
			writeRawRunFunctionCall(t, w, "resp_2", "call_2", rawRunToolName, map[string]string{"payload": " \t\n "})
		case 3:
			_, err := w.Write([]byte(`{"id":"resp_3","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"assistant text","annotations":[]}]}]}`))
			assert.NoError(t, err)
		default:
			t.Fatalf("unexpected response request %d", requests)
		}
	}))
	t.Cleanup(server.Close)

	result, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", "prompt", slog.New(slog.DiscardHandler), nil)
	require.NoError(t, err)
	assert.Equal(t, RawRunResult{Text: "assistant text"}, result)
}

func TestRunRawReturnsProgressMessageError(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"assistant text","annotations":[]}]}]}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	errProgress := errors.New("progress failed")
	progress := newInertRawRunProgress()
	progress.Message = func(context.Context, string) error { return errProgress }
	_, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}, AutoApproverModel: "gpt-5.4-mini"}, "main", "prompt", slog.New(slog.DiscardHandler), progress)
	require.ErrorIs(t, err, errProgress)
}

func TestRunRawReturnsProgressThinkingError(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission:\n  bash:\n    \"*\": allow\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		writeRawRunFunctionCall(t, w, "resp_1", "call_1", "execute", executeBashScript("printf ok"))
	}))
	t.Cleanup(server.Close)

	errProgress := errors.New("thinking failed")
	progress := newInertRawRunProgress()
	progress.Thinking = func(context.Context, string) error { return errProgress }
	_, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", "prompt", slog.New(slog.DiscardHandler), progress)
	require.ErrorIs(t, err, errProgress)
}

func TestRunRawAlwaysEnablesAutoApprovePermissions(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: '{{ model \"coding-high\" }}'\npermission:\n  bash:\n    \"printf ok\": auto\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var (
		mu      sync.Mutex
		request int
	)

	newServer := func(provider string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/responses" {
				http.NotFound(w, r)

				return
			}

			mu.Lock()
			request++
			current := request
			mu.Unlock()

			data, err := io.ReadAll(r.Body)
			if !assert.NoError(t, err) {
				http.Error(w, err.Error(), http.StatusBadRequest)

				return
			}

			w.Header().Set("Content-Type", "application/json")

			switch current {
			case 1:
				assert.Equal(t, "root", provider)
				assert.Contains(t, string(data), `"model":"software-development-sol"`)
				writeRawRunFunctionCall(t, w, "resp_1", "call_1", "execute", executeBashScript("printf ok"))
			case 2, 3:
				// Entry gate + nested bash both hit auto review under execute.
				assert.Equal(t, "review", provider)
				assert.Contains(t, string(data), `"model":"gpt-5.4-mini"`)
				writeRawRunMessage(t, w, fmt.Sprintf("resp_%d", current), fmt.Sprintf("msg_%d", current), `{"risk_level":"low","user_authorization":"unknown","outcome":"allow","rationale":"Low-risk action."}`)
			case 4:
				assert.Equal(t, "root", provider)
				writeRawRunFunctionCall(t, w, "resp_4", "call_4", rawRunToolName, map[string]string{"payload": "done"})
			case 5:
				assert.Equal(t, "root", provider)
				writeRawRunMessage(t, w, "resp_5", "msg_5", "assistant text")
			default:
				t.Fatalf("unexpected raw run request %d", current)
			}
		}))
	}
	rootServer := newServer("root")
	t.Cleanup(rootServer.Close)

	reviewServer := newServer("review")
	t.Cleanup(reviewServer.Close)

	result, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, Models: map[string]string{"coding-high": "root/software-development-sol"}, OpenAI: config.OpenAIConfig{APIBaseURL: rootServer.URL}, Providers: map[string]config.OpenAIConfig{"root": {APIBaseURL: rootServer.URL}, "review": {APIBaseURL: reviewServer.URL}}, AutoApproverModel: "review/gpt-5.4-mini"}, "main", "prompt", slog.New(slog.DiscardHandler), newInertRawRunProgress())

	require.NoError(t, err)
	require.Equal(t, RawRunResult{Text: "assistant text", VerbatimMessage: "done"}, result)
}

func TestRunRawWithProgressProjectsDifferentProviderStoredHistory(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: work/gpt\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	conversationID := "cron:provider-replay"
	_, err = service.AppendEntryID(t.Context(), conversationID, &rocketcode.SessionEntry{Version: 1, Type: "turn", Model: "openai/gpt", ResponseID: providerReplayPrivate, ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"portable-readable","id":"provider-private-sentinel"}`)}})
	require.NoError(t, err)

	var requestBody string

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		if requests == 1 {
			requestBody = string(body)
		}

		w.Header().Set("Content-Type", "application/json")

		if requests == 1 {
			writeRawRunFunctionCall(t, w, "resp-1", "call-1", rawRunToolName, map[string]string{"payload": "done"})
			return
		}

		writeRawRunMessage(t, w, "resp-2", "message", "ok")
	}))
	t.Cleanup(server.Close)

	progress := newInertRawRunProgress()
	progress.SessionService = service
	progress.ConversationID = conversationID

	_, err = RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, Providers: map[string]config.OpenAIConfig{"work": {APIBaseURL: server.URL}}}, "main", "prompt", slog.New(slog.DiscardHandler), progress)
	require.NoError(t, err)
	assert.Contains(t, requestBody, providerReplayReadable)
	assert.NotContains(t, requestBody, providerReplayPrivate)
}

func TestWorkflowAgentRunnerUsesPreparedIsolatedRuntime(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: '{{ model \"active\" }}'\npermission:\n  read: {\"*\": allow}\n  edit: allow\n  glob: allow\n  grep: allow\n  bash: {\"*\": allow}\n  webfetch: {\"*\": allow}\n  websearch: allow\n  skill: {\"demo\": allow}\n  task: {\"*\": allow}\n  rocketclaw: {\"rocketclaw_reload\": allow}\n---\nMain prompt\n")
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(".rocketclaw/skills/demo", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/skills/demo/SKILL.md", []byte("---\nname: demo\ndescription: Demo\n---\nDemo\n"), 0o644))

	var (
		mu       sync.Mutex
		requests []map[string]any
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		mu.Lock()

		requests = append(requests, body)
		mu.Unlock()

		prompt := rawRunRequestPrompt(t, body)

		text := "first"

		switch prompt {
		case "structured":
			text = `{"ok":true}`
		case "second":
			text = "second"
		}

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", text)
	}))
	t.Cleanup(server.Close)

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousProvider := otel.GetTracerProvider()

	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	cfg := &config.Config{Workspace: workspace, Models: map[string]string{"active": "active-model", "fast": "fast-model", "nested": `{{ model "fast" }}`}, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}, Instrumentation: config.InstrumentationConfig{Enabled: true, HideInputs: true, HideOutputs: true}}
	run, cleanup, err := newWorkflowAgentRunner(cfg, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })

	require.NoError(t, root.Remove(".rocketclaw/agents/main.md"))

	cfg.OpenAI.APIBaseURL = "http://127.0.0.1:1"

	result, err := run(t.Context(), workflow.AgentRequest{Prompt: "literal !`printf unsafe`"}, discardWorkflowThinking)
	require.NoError(t, err)
	require.JSONEq(t, `"first"`, string(result))

	result, err = run(t.Context(), workflow.AgentRequest{Prompt: "structured", Worker: workflow.Worker{Name: "reviewer", Instructions: "Worker !`printf unsafe`", Model: "nested", Tools: []string{"skill"}}, Schema: map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []string{"ok"}}}, discardWorkflowThinking)
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(result))

	result, err = run(t.Context(), workflow.AgentRequest{Prompt: "second"}, discardWorkflowThinking)
	require.NoError(t, err)
	require.JSONEq(t, `"second"`, string(result))

	result, err = run(t.Context(), workflow.AgentRequest{Prompt: "no-tools", Worker: workflow.Worker{Name: "reasoner", Instructions: "Reason", Tools: []string{}}}, discardWorkflowThinking)
	require.NoError(t, err)
	require.JSONEq(t, `"first"`, string(result))

	mu.Lock()
	require.Len(t, requests, 4)
	first, structured, second, noTools := requests[0], requests[1], requests[2], requests[3]
	mu.Unlock()

	require.Equal(t, "active-model", first["model"])
	require.Contains(t, fmt.Sprint(first["instructions"]), "Main prompt")
	require.Contains(t, fmt.Sprint(first), "literal !`printf unsafe`")
	require.NotContains(t, fmt.Sprint(first["tools"]), `"name":"task"`)
	require.NotContains(t, fmt.Sprint(first["tools"]), "rocketclaw_")
	require.Contains(t, fmt.Sprint(first["tools"]), `name:execute`)
	require.NotContains(t, fmt.Sprint(first["tools"]), `name:read`)

	require.Equal(t, "fast-model", structured["model"])
	require.Contains(t, fmt.Sprint(structured["instructions"]), "Worker !`printf unsafe`")
	require.NotContains(t, fmt.Sprint(structured["instructions"]), "Main prompt")
	require.Contains(t, fmt.Sprint(structured["tools"]), `name:skill`)
	require.Contains(t, fmt.Sprint(structured["tools"]), `name:find_skills`)
	require.NotContains(t, fmt.Sprint(structured["tools"]), `name:read`)
	require.NotContains(t, fmt.Sprint(structured["tools"]), `name:execute`)
	require.Contains(t, fmt.Sprint(structured["text"]), "json_schema")
	require.Contains(t, fmt.Sprint(structured["text"]), "additionalProperties:false")
	require.NotContains(t, fmt.Sprint(structured["text"]), "strict:true")

	require.NotContains(t, fmt.Sprint(second["input"])+" "+fmt.Sprint(second["previous_response_id"]), "first")
	require.Empty(t, noTools["tools"])
	assert.NotEmpty(t, recorder.Ended(), "workflow run should emit configured tracing spans")
}

func TestWorkflowAgentRunnerResolvesNamedProviderModel(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\n---\nMain prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	defaultRequests := make(chan struct{}, 1)

	defaultServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		defaultRequests <- struct{}{}
	}))
	t.Cleanup(defaultServer.Close)

	models := make(chan string, 1)
	workServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			http.Error(w, "decode request", http.StatusBadRequest)

			return
		}

		models <- body.Model

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", "done")
	}))
	t.Cleanup(workServer.Close)

	cfg := &config.Config{
		Workspace: workspace,
		Models:    map[string]string{"worker": "work/worker-api-model"},
		OpenAI:    config.OpenAIConfig{APIBaseURL: defaultServer.URL},
		Providers: map[string]config.OpenAIConfig{"work": {APIBaseURL: workServer.URL}},
	}
	run, cleanup, err := newWorkflowAgentRunner(cfg, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })

	cfg.Providers["work"] = config.OpenAIConfig{APIBaseURL: "http://127.0.0.1:1"}

	result, err := run(t.Context(), workflow.AgentRequest{Prompt: "run", Worker: workflow.Worker{Name: "worker", Model: "worker"}}, discardWorkflowThinking)
	require.NoError(t, err)
	require.JSONEq(t, `"done"`, string(result))
	assert.Equal(t, "worker-api-model", <-models)

	select {
	case <-defaultRequests:
		t.Fatal("workflow worker used the default provider")
	default:
	}
}

func TestWorkflowAgentRunnerUsesConfiguredAutoApproverModel(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  bash:\n    \"printf ok\": auto\n---\nMain prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var models []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		models = append(models, fmt.Sprint(body["model"]))

		w.Header().Set("Content-Type", "application/json")

		switch len(models) {
		case 1:
			writeRawRunFunctionCall(t, w, "resp_1", "call_1", "execute", executeBashScript("printf ok"))
		case 2, 3:
			writeRawRunMessage(t, w, fmt.Sprintf("resp_%d", len(models)), fmt.Sprintf("msg_%d", len(models)), `{"risk_level":"low","user_authorization":"unknown","outcome":"allow","rationale":"Low risk."}`)
		case 4:
			writeRawRunMessage(t, w, "resp_4", "msg_4", "done")
		default:
			t.Fatalf("unexpected request %d", len(models))
		}
	}))
	t.Cleanup(server.Close)

	run, cleanup, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}, AutoApproverModel: "review-model"}, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })
	result, err := run(t.Context(), workflow.AgentRequest{Prompt: "run", Worker: workflow.Worker{Name: "worker", Instructions: "work", Tools: []string{"execute"}}}, discardWorkflowThinking)
	require.NoError(t, err)
	require.JSONEq(t, `"done"`, string(result))
	require.Equal(t, []string{"gpt-5.5", "review-model", "review-model", "gpt-5.5"}, models)
}

func TestWorkflowAgentRunnerStructuredOutputUsesFinalAssistantMessage(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  read: {\"*\": allow}\n---\nMain prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("PRIVATE TOOL RESULT"), 0o644))

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++

		w.Header().Set("Content-Type", "application/json")

		switch requests {
		case 1:
			_, err := w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"rsn_1","type":"reasoning","summary":[{"type":"summary_text","text":"checking context"}]},{"id":"msg_1","type":"message","status":"completed","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"checking the fixture","annotations":[]}]},{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"execute","arguments":"{\"code\":\"def main():\\n    return read(filePath=\\\"README.md\\\")\\n\"}"}]}`))
			assert.NoError(t, err)
		case 2:
			writeRawRunMessage(t, w, "resp_2", "msg_2", `{"ok":true,"private":"PRIVATE WORKER RESULT"}`)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	t.Cleanup(server.Close)

	run, cleanup, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })

	var thinking []string

	request := workflow.AgentRequest{
		Prompt: "PRIVATE WORKER PROMPT",
		Schema: map[string]any{
			"type":        "object",
			"description": "PRIVATE SCHEMA",
			"properties": map[string]any{
				"ok":      map[string]any{"type": "boolean"},
				"private": map[string]any{"type": "string"},
			},
			"required": []string{"ok", "private"},
		},
	}
	result, err := run(t.Context(), request, func(_ context.Context, text string) error {
		thinking = append(thinking, text)
		return nil
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true,"private":"PRIVATE WORKER RESULT"}`, string(result))
	require.Equal(t, 2, requests)
	assert.Equal(t, []string{"checking context", "checking the fixture", "Execute", "Execute → Read: README.md"}, thinking)

	for _, private := range []string{"PRIVATE TOOL RESULT", "PRIVATE WORKER RESULT", "PRIVATE WORKER PROMPT", "PRIVATE SCHEMA"} {
		assert.NotContains(t, strings.Join(thinking, "\n"), private)
	}
}

func TestWorkflowAgentRunnerCancelsOnThinkingFailure(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  read: {\"*\": allow}\n---\nMain prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("fixture"), 0o644))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeRawRunFunctionCall(t, w, "resp_1", "call_1", "execute", executeBashScript("true"))
	}))
	t.Cleanup(server.Close)

	run, cleanup, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })

	errThinking := errors.New("publish activity")
	_, err = run(t.Context(), workflow.AgentRequest{Prompt: "review"}, func(context.Context, string) error { return errThinking })
	require.ErrorIs(t, err, errThinking)
	require.ErrorContains(t, err, "publish workflow agent thinking")
}

func TestWorkflowExplicitSkillWithoutAvailableSubjects(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  skill:\n    unavailable: allow\n---\nMain prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var request map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "resp", "msg", "done")
	}))
	t.Cleanup(server.Close)

	run, cleanup, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })

	_, err = run(t.Context(), workflow.AgentRequest{Prompt: "prompt", Worker: workflow.Worker{Name: "worker", Instructions: "work", Tools: []string{"skill"}}}, discardWorkflowThinking)
	require.NoError(t, err)
	assert.Contains(t, fmt.Sprint(request["tools"]), `name:skill`)
	assert.NotContains(t, fmt.Sprint(request["tools"]), `name:find_skills`)
}

func TestWorkflowAgentRunnerRejectsInvalidOverridesAndStructuredOutput(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  read: {\"*\": allow}\n---\nMain prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", "not JSON")
	}))
	t.Cleanup(server.Close)

	run, cleanup, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, Models: map[string]string{"known": "gpt-5.5", "broken": `{{ model "missing" }}`}, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })

	_, err = run(t.Context(), workflow.AgentRequest{Worker: workflow.Worker{Name: "worker", Instructions: "prompt", Model: "missing"}, Prompt: "model"}, discardWorkflowThinking)
	require.ErrorContains(t, err, `workflow worker model "missing" is not configured`)
	_, err = run(t.Context(), workflow.AgentRequest{Worker: workflow.Worker{Name: "worker", Instructions: "prompt", Model: "broken"}, Prompt: "model"}, discardWorkflowThinking)
	require.ErrorContains(t, err, `render workflow worker model "broken"`)
	require.ErrorContains(t, err, `model "missing" is not configured`)
	_, err = run(t.Context(), workflow.AgentRequest{Worker: workflow.Worker{Name: "worker", Instructions: "prompt", Tools: []string{"missing"}}, Prompt: "tools"}, discardWorkflowThinking)
	require.ErrorContains(t, err, `unknown tool "missing"`)
	_, err = run(t.Context(), workflow.AgentRequest{Prompt: "schema", Schema: map[string]any{"type": "string"}}, discardWorkflowThinking)
	require.ErrorContains(t, err, "workflow worker returned invalid JSON")
	require.Equal(t, 1, requests)
}

func TestWorkflowAgentRunnerConcurrentDirectoriesAndCancellation(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\n---\nMain prompt\n")
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))

	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	cancelArrived := make(chan struct{})
	cancelDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		if rawRunRequestPrompt(t, body) == "cancel" {
			close(cancelArrived)
			<-r.Context().Done()
			close(cancelDone)

			return
		}

		arrived <- struct{}{}

		<-release
		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", "ok")
	}))
	t.Cleanup(server.Close)

	run, cleanup, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })

	type result struct {
		raw json.RawMessage
		err error
	}

	results := make(chan result, 2)

	for _, prompt := range []string{"one", "two"} {
		go func() {
			raw, err := run(t.Context(), workflow.AgentRequest{Prompt: prompt}, discardWorkflowThinking)
			results <- result{raw: raw, err: err}
		}()
	}

	<-arrived
	<-arrived

	dirs, err := fs.Glob(root.FS(), ".rocketclaw/.rocketcode/workflow-*")
	require.NoError(t, err)
	require.Len(t, dirs, 2)
	close(release)

	for range 2 {
		result := <-results
		require.NoError(t, result.err)
		require.JSONEq(t, `"ok"`, string(result.raw))
	}

	dirs, err = fs.Glob(root.FS(), ".rocketclaw/.rocketcode/workflow-*")
	require.NoError(t, err)
	require.Empty(t, dirs)

	ctx, cancel := context.WithCancel(t.Context())
	canceled := make(chan error, 1)

	go func() {
		_, err := run(ctx, workflow.AgentRequest{Prompt: "cancel"}, discardWorkflowThinking)
		canceled <- err
	}()

	<-cancelArrived
	cancel()
	require.ErrorIs(t, <-canceled, context.Canceled)
	<-cancelDone

	dirs, err = fs.Glob(root.FS(), ".rocketclaw/.rocketcode/workflow-*")
	require.NoError(t, err)
	require.Empty(t, dirs)
}

func TestWorkflowAgentRunnerReturnsShellDirectoryCleanupError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		runFailure bool
	}{
		{name: "cleanup failure"},
		{name: "run and cleanup failure", runFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\n---\nMain prompt\n")
			root, err := os.OpenRoot(workspace)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))

			const (
				parent      = ".rocketclaw/.rocketcode"
				savedParent = ".rocketclaw/.rocketcode-saved"
			)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				dirs, err := fs.Glob(root.FS(), parent+"/workflow-*")
				if !assert.NoError(t, err) || !assert.Len(t, dirs, 1) {
					http.Error(w, "inspect workflow directory", http.StatusInternalServerError)

					return
				}

				if !assert.NoError(t, root.Rename(parent, savedParent)) || !assert.NoError(t, root.Symlink("../..", parent)) {
					http.Error(w, "replace workflow parent", http.StatusInternalServerError)

					return
				}

				w.Header().Set("Content-Type", "application/json")

				if tc.runFailure {
					_, err := w.Write([]byte("{"))
					assert.NoError(t, err)

					return
				}

				writeRawRunMessage(t, w, "response", "message", "ok")
			}))
			t.Cleanup(server.Close)

			run, cleanup, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", slog.New(slog.DiscardHandler))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, cleanup()) })

			_, err = run(t.Context(), workflow.AgentRequest{Prompt: "run"}, discardWorkflowThinking)

			require.NoError(t, root.Remove(parent))
			require.NoError(t, root.Rename(savedParent, parent))
			dirs, errGlob := fs.Glob(root.FS(), parent+"/workflow-*")
			require.NoError(t, errGlob)
			require.Len(t, dirs, 1)
			require.NoError(t, root.RemoveAll(dirs[0]))

			require.ErrorContains(t, err, "remove workflow shell temp dir")

			if tc.runFailure {
				require.ErrorContains(t, err, "run workflow rocketcode turn")
			}
		})
	}
}

func discardWorkflowThinking(context.Context, string) error { return nil }

func executeBashScript(command string) map[string]string {
	return map[string]string{"code": "def main():\n    return bash(command=" + strconv.Quote(command) + ")\n"}
}

func executeApplyPatchScript(patch string) map[string]string {
	return map[string]string{"code": "def main():\n    return apply_patch(patchText=" + strconv.Quote(patch) + ")\n"}
}

func rawRunToolNames(t *testing.T, workspace string, progress *RawRunProgress) []string {
	t.Helper()

	var (
		requestMu sync.Mutex
		requests  int
		toolNames []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}

		requestMu.Lock()
		requests++
		request := requests
		requestMu.Unlock()

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		if request == 1 {
			var tools []struct {
				Name string `json:"name"`
			}

			data, err := json.Marshal(body["tools"])
			if !assert.NoError(t, err) || !assert.NoError(t, json.Unmarshal(data, &tools)) {
				http.Error(w, "decode tools", http.StatusInternalServerError)
				return
			}

			for _, tool := range tools {
				toolNames = append(toolNames, tool.Name)
			}
		}

		w.Header().Set("Content-Type", "application/json")

		switch request {
		case 1:
			writeRawRunFunctionCall(t, w, "resp_1", "call_1", rawRunToolName, map[string]string{"payload": ""})
		case 2:
			writeRawRunMessage(t, w, "resp_2", "msg_1", "assistant complete")
		default:
			t.Fatalf("unexpected response request %d", request)
		}
	}))
	t.Cleanup(server.Close)

	_, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", "prompt", slog.New(slog.DiscardHandler), progress)
	require.NoError(t, err)

	return toolNames
}

func writeRawRunFunctionCall(t *testing.T, w http.ResponseWriter, responseID, callID, name string, args any) {
	t.Helper()

	data, err := json.Marshal(args)
	require.NoError(t, err)
	_, err = fmt.Fprintf(w, `{"id":%q,"object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":%q,"type":"function_call","status":"completed","call_id":%q,"name":%q,"arguments":%q}]}`, responseID, "fc_"+callID, callID, name, string(data))
	require.NoError(t, err)
}

func writeRawRunMessage(t *testing.T, w http.ResponseWriter, responseID, messageID, text string) {
	t.Helper()

	_, err := fmt.Fprintf(w, `{"id":%q,"object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":%q,"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q,"annotations":[]}]}]}`, responseID, messageID, text)
	require.NoError(t, err)
}

func assertFileContent(t *testing.T, root *os.Root, filename, want string) {
	t.Helper()

	data, err := root.ReadFile(filename)
	require.NoError(t, err)
	assert.Equal(t, want, string(data))
}
