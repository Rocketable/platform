package backend

import (
	"context"
	"encoding/json"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSideAskHistoryStopsAtClickedCard(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "planner", "---\ndescription: Planner\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPlanner prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service := newTestSessionServiceAt(t, workspace)
	conversationID := "slack-thread:C123:111.222"
	id1, err := service.AppendEntryID(t.Context(), conversationID, testSessionEntry("card-one-user", "card-one-assistant"))
	require.NoError(t, err)
	_, err = service.AppendEntryID(t.Context(), conversationID, testSessionEntry("card-two-user", "card-two-assistant"))
	require.NoError(t, err)
	before, err := service.ObserveEntries(t.Context(), conversationID, 0)
	require.NoError(t, err)

	var requestBody string

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}

		requests++

		body, errRead := io.ReadAll(r.Body)
		if !assert.NoError(t, errRead) {
			http.Error(w, errRead.Error(), http.StatusBadRequest)
			return
		}

		if requests == 1 {
			requestBody = string(body)
		}

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "resp-1", "message", "private side ask answer")
	}))
	t.Cleanup(server.Close)

	runner := &SideAskRunner{Config: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, Sessions: service, Logger: slog.New(slog.DiscardHandler)}
	require.NoError(t, runner.Run(t.Context(), protocol.SideAskRequest{
		ConversationID: conversationID,
		SessionEntryID: id1,
		Agent:          "planner",
		Question:       "What broke?",
		Thinking:       func(context.Context, string) error { return nil },
		Message:        func(context.Context, string) error { return nil },
	}))

	assert.Contains(t, requestBody, "card-one-user")
	assert.Contains(t, requestBody, "card-one-assistant")
	assert.NotContains(t, requestBody, "card-two-user")
	assert.NotContains(t, requestBody, "card-two-assistant")
	assert.Contains(t, requestBody, "What broke?")
	after, err := service.ObserveEntries(t.Context(), conversationID, 0)
	require.NoError(t, err)
	require.Len(t, after, len(before))

	for i := range before {
		assert.Equal(t, before[i].ID, after[i].ID)
		assert.Equal(t, before[i].Entry.ReplayInput, after[i].Entry.ReplayInput)
	}

	turns, err := service.RecoverableActiveTurns(t.Context())
	require.NoError(t, err)

	for _, turn := range turns {
		assert.False(t, strings.HasPrefix(turn.Checkpoint.ConversationKey, "slack-thread:"))
	}
}

func TestSideAskStripsThreadMutatingToolsAndUsesOwnTemp(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission:\n  rocketclaw:\n    rocketclaw_restart: allow\n    rocketclaw_schedule_message: allow\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service := newTestSessionServiceAt(t, workspace)
	conversationID := "slack-thread:C123:111.222"
	id, err := service.AppendEntryID(t.Context(), conversationID, testSessionEntry("prior-user", "prior-assistant"))
	require.NoError(t, err)

	var (
		mu        sync.Mutex
		toolNames []string
	)

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

		data, err := json.Marshal(body["tools"])
		if !assert.NoError(t, err) {
			http.Error(w, "encode tools", http.StatusInternalServerError)
			return
		}

		var tools []struct {
			Name string `json:"name"`
		}
		if !assert.NoError(t, json.Unmarshal(data, &tools)) {
			http.Error(w, "decode tools", http.StatusInternalServerError)
			return
		}

		mu.Lock()
		for _, tool := range tools {
			toolNames = append(toolNames, tool.Name)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "resp-1", "message", "ok")
	}))
	t.Cleanup(server.Close)

	runner := &SideAskRunner{Config: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, Sessions: service, Logger: slog.New(slog.DiscardHandler)}
	require.NoError(t, runner.Run(t.Context(), protocol.SideAskRequest{
		ConversationID: conversationID,
		SessionEntryID: id,
		Agent:          "main",
		Question:       "status?",
		Thinking:       func(context.Context, string) error { return nil },
		Message:        func(context.Context, string) error { return nil },
	}))

	mu.Lock()
	defer mu.Unlock()

	assert.NotContains(t, toolNames, scheduleMessageToolName)
	assert.NotContains(t, toolNames, resetScheduledMessagesToolName)
	assert.NotContains(t, toolNames, updateGoalToolName)
	assert.NotContains(t, toolNames, askUserQuestionToolName)
	assert.NotContains(t, toolNames, startNewThreadToolName)
	assert.NotContains(t, toolNames, restartToolName)
	assert.NotContains(t, toolNames, rawRunToolName)
	assert.NoDirExists(t, filepath.Join(workspace, rocketcodeShellTempRel(".rocketclaw", conversationID)))
}

func TestSideAskHonorsCancel(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service := newTestSessionServiceAt(t, workspace)
	conversationID := "slack-thread:C123:111.222"
	id, err := service.AppendEntryID(t.Context(), conversationID, testSessionEntry("prior-user", "prior-assistant"))
	require.NoError(t, err)

	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}

		close(started)
		<-release
		http.Error(w, "canceled", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(t.Context())
	runner := &SideAskRunner{Config: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, Sessions: service, Logger: slog.New(slog.DiscardHandler)}

	done := make(chan error, 1)
	go func() {
		done <- runner.Run(ctx, protocol.SideAskRequest{
			ConversationID: conversationID,
			SessionEntryID: id,
			Agent:          "main",
			Question:       "status?",
			Thinking:       func(context.Context, string) error { return nil },
			Message:        func(context.Context, string) error { return nil },
		})
	}()

	<-started
	cancel()
	close(release)
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestSideAskUsesChosenAgentIdentity(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "social", "---\ndescription: Social\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nSocial prompt\n")
	writeAgent(t, workspace, "planner", "---\ndescription: Planner\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPlanner prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service := newTestSessionServiceAt(t, workspace)
	conversationID := "slack-thread:C123:111.222"
	id, err := service.AppendEntryID(t.Context(), conversationID, testSessionEntry("prior-user", "prior-assistant"))
	require.NoError(t, err)

	var prompt string

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

		instructions, _ := body["instructions"].(string)
		prompt = instructions

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "resp-1", "message", "ok")
	}))
	t.Cleanup(server.Close)

	runner := &SideAskRunner{Config: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, Sessions: service, Logger: slog.New(slog.DiscardHandler)}
	require.NoError(t, runner.Run(t.Context(), protocol.SideAskRequest{
		ConversationID: conversationID,
		SessionEntryID: id,
		Agent:          "planner",
		Question:       "status?",
		Thinking:       func(context.Context, string) error { return nil },
		Message:        func(context.Context, string) error { return nil },
	}))
	assert.Contains(t, prompt, "Planner prompt")
	assert.NotContains(t, prompt, "Social prompt")
}
