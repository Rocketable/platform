package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func TestValidateAssetsAcceptsWorkspaceWithoutCronjobs(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, processAssembler{}.ValidateAssets(&config.Config{Workspace: workspace}, ".rocketclaw", nil))
}

func TestValidateAssetsReportsCronChannelError(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "cron"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "cron", "daily.md"), []byte("---\nschedule: 1h\nchannel: '#ops'\n---\nBody"), 0o644))
	require.ErrorContains(t, processAssembler{}.ValidateAssets(&config.Config{Workspace: workspace}, ".", []string{"#release"}), "validate cron definitions")
}

func TestAssembleFrontendsReportsSlackStartError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	refresh := func() error { return nil }
	rt := &backend.Runtime{
		Cfg:                      &config.Config{},
		Log:                      slog.New(slog.DiscardHandler),
		RunCtx:                   ctx,
		Channels:                 protocol.NewChannels(),
		RefreshExternalMCPAgents: &refresh,
	}
	_, _, _, err := processAssembler{}.Assemble(rt)
	require.Error(t, err)
	require.ErrorContains(t, err, "start Slack connector")
}

func TestAssembleFrontendsWiresSlackCronAndMCP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": "https://example.slack.com/", "team": "t", "user": "bot", "team_id": "T1", "user_id": "U1"}))
	}))
	t.Cleanup(server.Close)
	t.Setenv("ROCKETCLAW_SLACK_API_URL", server.URL+"/")

	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".rocketclaw", "agents", "main.md"), []byte("---\ndescription: Main\nmodel: gpt-5.5\n---\n"), 0o600))

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop(context.Background())) })

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	refresh := func() error { return nil }
	rt := &backend.Runtime{
		Cfg: &config.Config{
			Workspace: workspace,
			Slack: config.SlackConfig{
				BotToken: "xoxb-test", AppToken: "xapp-test",
				Channels: []config.SlackChannelConfig{{Channel: "@"}, {Channel: "#ops", Agents: []string{"main"}}},
			},
			MCPExternal: config.MCPExternalConfig{Enabled: true, ListenAddr: "127.0.0.1:0"},
		},
		Log:                      slog.New(slog.DiscardHandler),
		RunCtx:                   ctx,
		Channels:                 protocol.NewChannels(),
		Sessions:                 sessions,
		RefreshExternalMCPAgents: &refresh,
		ExternalMCPUsers:         map[string]string{"alice": "secret"},
	}
	front, done, stops, err := processAssembler{}.Assemble(rt)
	require.NoError(t, err)
	require.NotNil(t, front)
	require.NotNil(t, done)
	require.NotEmpty(t, stops)
	published := make(chan struct{})
	go func() {
		rt.Channels.Broadcasts <- protocol.Broadcast{Message: protocol.NewOutboundMessage(protocol.SourceSystem, "c", "hi")}
		close(published)
	}()
	<-published
	cancel()
	<-done
	for _, stop := range stops {
		require.NoError(t, stop(context.Background()))
	}
}
