package rpc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
)

func TestObserveLineThinking(t *testing.T) {
	text, role := observeLine(json.RawMessage(`{"type":"function_call","name":"read"}`))
	require.Equal(t, "read", text)
	require.Equal(t, "thinking", role)

	text, role = observeLine(json.RawMessage(`{"type":"reasoning","summary":[{"text":"Finding files"}]}`))
	require.Equal(t, "Finding files", text)
	require.Equal(t, "thinking", role)

	text, role = observeLine(json.RawMessage(`{"type":"message","role":"assistant","content":"done"}`))
	require.Equal(t, "done", text)
	require.Equal(t, "assistant", role)
}

func TestInboxSettled(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	require.False(t, inboxSettled("", time.Time{}, now))
	require.False(t, inboxSettled("", now.Add(-24*time.Hour), now))
	require.True(t, inboxSettled("", now.Add(-73*time.Hour), now))
	require.True(t, inboxSettled("settled", now, now))
	require.False(t, inboxSettled("active", now.Add(-73*time.Hour), now))
}

func TestWebConfigViewOmitsSecrets(t *testing.T) {
	view := webConfigView(&config.Config{
		Workspace:         "/ws",
		DatabaseURL:       "postgres://secret",
		Models:            map[string]string{"default": "gpt-5.4"},
		AutoApproverModel: "gpt-5.4",
		Logging:           config.LoggingConfig{Level: "info"},
		Slack:             config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"main"}}}},
		MCPServers:        map[string]config.MCPServerConfig{"docs": {Command: "npx"}},
		MCPExternal:       config.MCPExternalConfig{Enabled: true},
		Instrumentation:   config.InstrumentationConfig{Enabled: true, APIKey: "otel-secret"},
	})
	require.Equal(t, "/ws", view.GetWorkspace())
	require.Equal(t, "default", view.GetModels()[0].GetName())
	require.Equal(t, "#ops", view.GetSlackChannels()[0].GetChannel())
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret")
}

func TestCronRunStem(t *testing.T) {
	stem, ok := cronRunStem("cron:cron/daily.md:x:y")
	require.True(t, ok)
	require.Equal(t, "daily", stem)

	_, ok = cronRunStem("web-session:ops")
	require.False(t, ok)
}
