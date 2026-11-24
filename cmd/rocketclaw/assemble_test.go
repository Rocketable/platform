package main

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
)

func TestValidateAssetsAcceptsWorkspaceWithoutCronjobs(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "agents"), 0o755))
	require.NoError(t, processAssembler{}.ValidateAssets(&config.Config{Workspace: workspace}, ".rocketclaw"))
}

func TestValidateAssetsReportsRoutineError(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "agents"), 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "helper.md"), []byte("---\nmodel: gpt-6.1-sol\nroutines: [every 30min]\n---\nBody"), 0o644))
	require.ErrorContains(t, processAssembler{}.ValidateAssets(&config.Config{Workspace: workspace}, "."), `agents/helper.md: routines: schedule "every 30min": invalid cron expression`)
}

func TestAssembleFrontendsReportsSlackStartError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	rt := &backend.Runtime{
		Cfg:    config.NewLockedConfig(&config.Config{Slack: config.SlackConfig{BotToken: "xoxb", AppToken: "xapp"}}),
		Log:    slog.New(slog.DiscardHandler),
		RunCtx: ctx,
	}
	rt.ExternalMCPAgents = &mcpAgentIndex{cfg: rt.Cfg}
	_, _, err := (&processAssembler{}).Assemble(rt)
	require.Error(t, err)
	require.ErrorContains(t, err, "start Slack connector")
}

func TestAssembleFrontendsWiresSlackCronAndMCP(t *testing.T) {
	for _, slack := range []config.SlackConfig{
		{
			BotToken: "xoxb-test", AppToken: "xapp-test",
			Channels: []config.SlackChannelConfig{{Channel: "@", Agents: []string{"ops"}, AllowedUserIDs: []string{"U1"}}, {Channel: "#ops", Agents: []string{"ops"}, AllowedUserIDs: []string{"U1"}}},
		},
		{},
	} {
		t.Run(cmp.Or(slack.BotToken, "without Slack"), func(t *testing.T) {
			serveFakeSlack(t, func(w http.ResponseWriter, r *http.Request) {
				assert.NotEmpty(t, slack.BotToken, "Slack is not configured, yet %s was called", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": "https://example.slack.com/", "team": "t", "user": "bot", "team_id": "T1", "user_id": "U1"}))
			})

			workspace := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(workspace, "agents"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "ops.md"), []byte("---\ndescription: Ops\nmodel: gpt-6.1-sol\nsticky: slack\n---\n"), 0o600))
			require.NoError(t, os.Mkdir(filepath.Join(workspace, "cron"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "cron", "ops.md"), []byte("---\nschedule: 1h\nagent: ops\n---\nReport.\n"), 0o600))

			usePassingTailnet(t)

			dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			cfg := &config.Config{
				Workspace:   workspace,
				DatabaseURL: dsn,
				Slack:       slack,
				MCPExternal: config.MCPExternalConfig{Enabled: true, ListenAddr: "127.0.0.1:0", Users: map[string]string{"alice": "secret"}},
				Web:         config.WebConfig{Tsnet: &config.TsnetConfig{Hostname: "rocketclaw-test", AuthKey: "tskey-auth-test"}},
			}
			assembler := &processAssembler{}
			require.NoError(t, backend.Run(t.Context(), cfg, slog.New(slog.DiscardHandler), new(slog.LevelVar), &FrontendAssemblerMock{
				GateFunc:           assembler.Gate,
				ValidateAssetsFunc: assembler.ValidateAssets,
				AssembleFunc: func(rt *backend.Runtime) (<-chan struct{}, []func(context.Context) error, error) {
					rt.RunCtx = ctx
					done, stops, err := assembler.Assemble(rt)
					require.NoError(t, err)
					require.NotNil(t, done)
					require.Len(t, stops, 4, "Slack, Cron, external MCP, and Web must all start")
					if slack.BotToken == "" {
						require.Equal(t, ctx.Done(), done, "the inert Slack ends with the run")
					}

					require.IsType(t, &cronfrontend.Manager{}, rt.CronJobs, "a reload refreshes the started cron jobs")
					cancel()
					<-done
					for _, stop := range stops {
						require.NoError(t, stop(context.Background()))
					}

					return done, nil, nil
				},
			}, sandbox.None{}))
		})
	}
}
