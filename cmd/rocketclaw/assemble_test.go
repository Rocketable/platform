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
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
)

func TestValidateAssetsAcceptsWorkspaceWithoutCronjobs(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "agents"), 0o755))
	require.NoError(t, processAssembler{}.ValidateAssets(&config.Config{Workspace: workspace}, ".rocketclaw"))
}

func TestValidateAssetsReportsCronDefinitionError(t *testing.T) {
	workspace := t.TempDir()
	for _, dir := range []string{"agents", "cron"} {
		require.NoError(t, os.Mkdir(filepath.Join(workspace, dir), 0o755))
	}

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "cron", "daily.md"), []byte("---\nagent: main\n---\nBody"), 0o644))
	require.ErrorContains(t, processAssembler{}.ValidateAssets(&config.Config{Workspace: workspace}, "."), "validate cron definitions: load cron definition: parse cronjob cron/daily.md frontmatter: schedule is required")
}

func TestAssembleFrontendsReportsSlackStartError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	rt := &backend.Runtime{
		Cfg:    config.NewLockedConfig(&config.Config{}),
		Log:    slog.New(slog.DiscardHandler),
		RunCtx: ctx,
	}
	rt.ExternalMCPAgents = &mcpAgentIndex{cfg: rt.Cfg}
	_, _, err := processAssembler{}.Assemble(rt)
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
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".rocketclaw", "agents", "main.md"), []byte("---\ndescription: Main\nmodel: gpt-6.1-sol\n---\n"), 0o600))

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	configPath := filepath.Join(workspace, "rocketclaw.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "rocketclaw.users.json"), []byte(`{"alice":"secret"}`), 0o600))

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	cfg := &config.Config{
		Workspace:   workspace,
		DatabaseURL: dsn,
		Slack: config.SlackConfig{
			BotToken: "xoxb-test", AppToken: "xapp-test",
			Channels: []config.SlackChannelConfig{{Channel: "@"}, {Channel: "#ops", Agents: []string{"main"}}},
		},
		MCPExternal: config.MCPExternalConfig{Enabled: true, ListenAddr: "127.0.0.1:0"},
		Web:         config.WebConfig{ListenAddress: "127.0.0.1:0"},
	}
	require.NoError(t, backend.Run(t.Context(), cfg, configPath, slog.New(slog.DiscardHandler), &FrontendAssemblerMock{
		ValidateAssetsFunc: processAssembler{}.ValidateAssets,
		AssembleFunc: func(rt *backend.Runtime) (<-chan struct{}, []func(context.Context) error, error) {
			rt.RunCtx = ctx
			done, stops, err := processAssembler{}.Assemble(rt)
			require.NoError(t, err)
			require.NotNil(t, done)
			require.Len(t, stops, 4, "Slack, Cron, external MCP, and Web must all start")
			require.IsType(t, &cronfrontend.Manager{}, rt.CronJobs, "a reload refreshes the started cron jobs")
			cancel()
			<-done
			for _, stop := range stops {
				require.NoError(t, stop(context.Background()))
			}

			return done, nil, nil
		},
	}, nil))
}
