package backend

import (
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/stretchr/testify/require"
)

func TestRuntimeReload(t *testing.T) {
	for _, scenario := range []string{"success", "invalid-agent", "invalid-frontend", "refresh-failure", "cron-refresh-failure"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := &config.Config{Workspace: t.TempDir()}
			root, err := os.OpenRoot(cfg.Workspace)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			require.NoError(t, root.Mkdir("agents", 0o755))

			const (
				oldAgent = "---\ndescription: Original\nmodel: test-model\n---\nOriginal prompt\n"
				newAgent = "---\ndescription: Updated\nmodel: test-model\n---\nUpdated prompt\n"
			)

			require.NoError(t, root.WriteFile("agents/main.md", []byte(oldAgent), 0o600))

			log := slog.New(slog.DiscardHandler)
			require.NoError(t, skel.SyncInWithOverlays(cfg.Workspace, cfg.RuntimeDirName(), cfg.Overlays, log))

			updated := newAgent
			if scenario == "invalid-agent" {
				updated = "---\nmodel: [\n---\n"
			}

			require.NoError(t, root.WriteFile("agents/main.md", []byte(updated), 0o600))
			// Reload must not read configuration changes from disk.
			require.NoError(t, root.WriteFile("rocketclaw.json", []byte("invalid config"), 0o600))

			assembler := &frontendAssemblerMock{ValidateAssetsFunc: func(*config.Config, string) error {
				if scenario == "invalid-frontend" {
					return errors.New("invalid staged frontend")
				}

				return nil
			}}
			refresh, cron := &runtimeRefresherMock{}, &runtimeRefresherMock{}
			rt := &Runtime{Cfg: config.NewLockedConfig(cfg), Log: log, assemble: assembler, ExternalMCPAgents: refresh, CronJobs: cron}
			refreshed := func(failure string) error {
				require.False(t, rt.reloadMu.TryLock(), "reload remains serialized through each refresh")
				live, err := root.ReadFile(cfg.RuntimeDirName() + "/agents/main.md")
				require.NoError(t, err)
				require.Equal(t, newAgent, string(live), "refresh runs after replacement")

				if scenario == failure {
					return errors.New("refresh failed")
				}

				return nil
			}
			refresh.RefreshFunc = func() error { return refreshed("refresh-failure") }
			cron.RefreshFunc = func() error {
				require.Len(t, refresh.RefreshCalls(), 1, "cron jobs refresh after External MCP agents")
				return refreshed("cron-refresh-failure")
			}

			receipt, err := rt.Reload("updated assets")
			if scenario == "success" {
				require.NoError(t, err)
				require.Equal(t, "rocketclaw runtime assets reloaded", receipt)
			} else {
				require.Error(t, err)
				require.Empty(t, receipt)
			}

			live, err := root.ReadFile(cfg.RuntimeDirName() + "/agents/main.md")
			require.NoError(t, err)

			if scenario == "invalid-agent" || scenario == "invalid-frontend" {
				require.Equal(t, oldAgent, string(live))
				require.Empty(t, refresh.RefreshCalls())
				require.Empty(t, cron.RefreshCalls())
			} else {
				require.Equal(t, newAgent, string(live))
				require.Len(t, refresh.RefreshCalls(), 1)

				cronCalls := 1
				if scenario == "refresh-failure" {
					cronCalls = 0
				}

				require.Len(t, cron.RefreshCalls(), cronCalls)
			}
		})
	}
}
