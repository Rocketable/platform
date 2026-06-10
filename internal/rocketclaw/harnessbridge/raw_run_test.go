package harnessbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmaxRecursion: nope\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	_, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace}, "main", "prompt", slog.New(slog.DiscardHandler), nil)

	require.ErrorContains(t, err, "main.md: parse maxRecursion:")
}

func TestInertRawRunProgressCallbacksAreNoops(t *testing.T) {
	progress := newInertRawRunProgress()

	require.NoError(t, progress.Thinking(context.Background(), "thinking"))
	require.NoError(t, progress.Message(context.Background(), "message"))
	require.NoError(t, progress.ScheduleMessage(time.Second, "later", false))
	require.NoError(t, progress.ResetScheduledMessages())

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
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: openai/gpt-5.5\npermission:\n  edit: allow\n  rocketclaw:\n    rocketclaw_schedule_message: allow\n---\nPrompt\n")
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
			assert.Contains(t, string(data), attachFilesToolName)
			assert.Contains(t, string(data), scheduleMessageToolName)
			assert.Contains(t, string(data), `"required":["message","send_this_in","recurring"]`)
			assert.Contains(t, string(data), resetScheduledMessagesToolName)
		}

		w.Header().Set("Content-Type", "application/json")

		switch request {
		case 1:
			writeRawRunFunctionCall(t, w, "resp_1", "call_1", "apply_patch", map[string]string{"patchText": "*** Begin Patch\n*** Update File: cron/HEARTBEAT.md\n@@\n-old heartbeat\n+new heartbeat\n*** End Patch"})
		case 2:
			writeRawRunFunctionCall(t, w, "resp_2", "call_2", "apply_patch", map[string]string{"patchText": "*** Begin Patch\n*** Update File: rocketclaw.json\n@@\n-{\"name\":\"old\"}\n+{\"name\":\"new\"}\n*** End Patch"})
		case 3:
			writeRawRunFunctionCall(t, w, "resp_3", "call_3", restartToolName, map[string]string{"reason": "rocketclaw.json changed and runtime config must reload"})
		case 4:
			writeRawRunFunctionCall(t, w, "resp_4", "call_4", scheduleMessageToolName, map[string]any{"message": "follow up", "send_this_in": "5m", "recurring": false})
		case 5:
			writeRawRunFunctionCall(t, w, "resp_5", "call_5", rawRunToolName, map[string]string{"payload": "cron done"})
		case 6:
			_, err := w.Write([]byte(`{"id":"resp_6","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"assistant complete","annotations":[]}]}]}`))
			assert.NoError(t, err)
		default:
			t.Fatalf("unexpected response request %d", request)
		}
	}))
	t.Cleanup(server.Close)

	restarts := 0
	schedules := 0
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
	progress.ScheduleMessage = func(delay time.Duration, message string, recurring bool) error {
		schedules++

		assert.Equal(t, 5*time.Minute, delay)
		assert.Equal(t, "follow up", message)
		assert.False(t, recurring)

		return nil
	}
	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}

	result, err := RunRawWithProgress(t.Context(), cfg, "main", "!`printf raw-expanded`", slog.New(slog.DiscardHandler), progress)
	require.NoError(t, err)
	assert.Equal(t, RawRunResult{Text: "assistant complete", VerbatimMessage: "cron done"}, result)
	assert.Equal(t, 1, restarts)
	assert.Equal(t, 1, schedules)
	entries, err := ObserveSessionEntries(t.Context(), sessionDBPath(workspace), progress.ConversationID, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	items, err := rocketcode.ReplayInputToParams(entries[0].Entry.ReplayInput)
	require.NoError(t, err)
	assert.Equal(t, "raw-expanded", items[0].OfMessage.Content.OfString.Value)
	requestMu.Lock()
	assert.Equal(t, 6, requests)
	requestMu.Unlock()
	assertFileContent(t, root, "cron/HEARTBEAT.md", "new heartbeat\n")
	assertFileContent(t, root, "rocketclaw.json", "{\"name\":\"new\"}\n")
}

func TestRunRawRetriesMissingMandatoryToolUntilDecision(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: openai/gpt-5.5\n---\nPrompt\n")
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
}

func TestRunRawLoadsSkillContentAsDeveloperMessage(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: openai/gpt-5.5\npermission:\n  skill:\n    demo: allow\n---\nPrompt\n")
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

func TestRunRawReturnsQueuedAttachmentsWithNonEmptyDecision(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: openai/gpt-5.5\n---\nPrompt\n")
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
	assert.Equal(t, []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report body")}}, result.Attachments)
}

func TestRunRawDropsQueuedAttachmentsWithEmptyDecision(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: openai/gpt-5.5\n---\nPrompt\n")
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
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: openai/gpt-5.5\n---\nPrompt\n")
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
	_, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", "prompt", slog.New(slog.DiscardHandler), progress)
	require.ErrorIs(t, err, errProgress)
}

func TestRunRawReturnsProgressThinkingError(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: openai/gpt-5.5\npermission:\n  bash:\n    \"*\": allow\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		writeRawRunFunctionCall(t, w, "resp_1", "call_1", "bash", map[string]string{"command": "printf ok", "description": "run command"})
	}))
	t.Cleanup(server.Close)

	errProgress := errors.New("thinking failed")
	progress := newInertRawRunProgress()
	progress.Thinking = func(context.Context, string) error { return errProgress }
	_, err := RunRawWithProgress(t.Context(), &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", "prompt", slog.New(slog.DiscardHandler), progress)
	require.ErrorIs(t, err, errProgress)
}

func writeRawRunFunctionCall(t *testing.T, w http.ResponseWriter, responseID, callID, name string, args any) {
	t.Helper()

	data, err := json.Marshal(args)
	require.NoError(t, err)
	_, err = fmt.Fprintf(w, `{"id":%q,"object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":%q,"type":"function_call","status":"completed","call_id":%q,"name":%q,"arguments":%q}]}`, responseID, "fc_"+callID, callID, name, string(data))
	require.NoError(t, err)
}

func assertFileContent(t *testing.T, root *os.Root, filename, want string) {
	t.Helper()

	data, err := root.ReadFile(filename)
	require.NoError(t, err)
	assert.Equal(t, want, string(data))
}
