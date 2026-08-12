package quickbench

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCaptureFromSession(t *testing.T) {
	workspace := t.TempDir()
	service, err := harnessbridge.NewSessionServiceIn(workspace, config.DefaultRuntimeDir, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { _ = service.Stop(context.Background()) })

	conversationID := "slack-thread:C1:1.2"
	entry := &rocketcode.SessionEntry{
		Version:   1,
		Type:      "turn",
		Timestamp: time.Unix(1, 0).UTC(),
		ReplayInput: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","content":"please read it"}`),
			json.RawMessage(`{"type":"function_call","call_id":"c1","name":"read","arguments":"{\"path\":\"x\"}"}`),
			json.RawMessage(`{"type":"function_call_output","call_id":"c1","output":"file-body"}`),
			json.RawMessage(`{"type":"message","role":"assistant","content":"done"}`),
		},
	}
	_, err = service.AppendEntryID(context.Background(), conversationID, entry)
	require.NoError(t, err)

	dbPath := filepath.Join(workspace, config.DefaultRuntimeDir, "state.sqlite3")
	out := filepath.Join(t.TempDir(), "cap")
	agentsDir := filepath.Join(t.TempDir(), "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "main.md"), defaultMainAgentMarkdown("gpt-5.4", "root"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "worker.md"), []byte("---\ndescription: w\nmodel: gpt-5.4-mini\n---\n\nworker\n"), 0o644))

	require.NoError(t, Capture(context.Background(), CaptureOptions{
		DBPath:         dbPath,
		ConversationID: conversationID,
		AgentsDir:      agentsDir,
		Out:            out,
		Variation:      "from-slack",
	}))

	bar, err := Open(out)
	require.NoError(t, err)
	require.Len(t, bar.Variations, 1)
	assert.Equal(t, "from-slack", bar.Variations[0].ID)
	require.NotEmpty(t, bar.Variations[0].Transcript)
	assert.Equal(t, "user", bar.Variations[0].Transcript[0].Role)
	assert.Equal(t, "assistant", bar.Variations[0].Transcript[len(bar.Variations[0].Transcript)-1].Role)
	require.Len(t, bar.Variations[0].Tools, 1)
	assert.Equal(t, "read", bar.Variations[0].Tools[0].Name)
	assert.Equal(t, "file-body", bar.Variations[0].Tools[0].Response)
	assert.Contains(t, bar.Criteria, "TODO")
	require.Contains(t, bar.Agents, "main")
	require.Contains(t, bar.Agents, "worker")
}

func TestCaptureUnknownConversation(t *testing.T) {
	workspace := t.TempDir()
	service, err := harnessbridge.NewSessionServiceIn(workspace, config.DefaultRuntimeDir, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { _ = service.Stop(context.Background()) })

	agentsDir := filepath.Join(t.TempDir(), "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "main.md"), defaultMainAgentMarkdown("gpt-5.4", "root"), 0o644))

	dbPath := filepath.Join(workspace, config.DefaultRuntimeDir, "state.sqlite3")
	err = Capture(context.Background(), CaptureOptions{
		DBPath:         dbPath,
		ConversationID: "missing",
		AgentsDir:      agentsDir,
		Out:            filepath.Join(t.TempDir(), "out"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no session entries")
}

func TestExtractTranscriptAndMocks(t *testing.T) {
	entries := []harnessbridge.ObservedSessionEntry{{
		Entry: rocketcode.SessionEntry{ReplayInput: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","content":"hi"}`),
			json.RawMessage(`{"type":"function_call","call_id":"c1","name":"echo","arguments":"{}"}`),
			json.RawMessage(`{"type":"function_call_output","call_id":"c1","output":"pong"}`),
			json.RawMessage(`{"type":"function_call","call_id":"c2","name":"task","arguments":"{\"subagent_type\":\"worker\"}"}`),
			json.RawMessage(`{"type":"function_call_output","call_id":"c2","output":"<task_result>nope</task_result>"}`),
			json.RawMessage(`{"type":"function_call","call_id":"c3","name":"bash","arguments":"{\"command\":\"gh pr view 1\"}"}`),
			json.RawMessage(`{"type":"function_call_output","call_id":"c3","output":"PR title"}`),
			json.RawMessage(`{"type":"message","role":"assistant","content":"ok"}`),
		}},
	}}
	messages, tools, bash, err := extractFromEntries(entries)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(messages), 3)
	assert.Equal(t, "user", messages[0].Role)
	assert.Equal(t, "hi", messages[0].Text)
	assert.Equal(t, "assistant", messages[len(messages)-1].Role)
	assert.Equal(t, "ok", messages[len(messages)-1].Text)
	require.Len(t, tools, 1)
	assert.Equal(t, "echo", tools[0].Name)
	assert.Equal(t, "pong", tools[0].Response)
	require.Len(t, bash, 1)
	assert.Equal(t, "gh pr view 1", bash[0].Command)
	assert.Equal(t, "PR title", bash[0].Output)
}

func TestBashDoubleShellCommand(t *testing.T) {
	fn := shellCommandFromBashDoubles([]BashDouble{
		{Command: "gh pr view 1", Output: "one"},
		{Pattern: "gh *", Output: "any-gh"},
	})
	path, args := fn("gh pr view 1")
	out, err := exec.Command(path, args...).CombinedOutput()
	require.NoError(t, err)
	assert.Equal(t, "one", string(out))

	path, args = fn("gh issue list")
	out, err = exec.Command(path, args...).CombinedOutput()
	require.NoError(t, err)
	assert.Equal(t, "any-gh", string(out))

	path, args = fn("curl https://example.com")
	out, err = exec.Command(path, args...).CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "unmocked bash command")
}
