package backend

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
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
			rt := &Runtime{Cfg: config.NewLockedConfig(cfg), Log: log, RunCtx: t.Context(), Sessions: &SessionService{}, Assemble: assembler, sandboxes: sandbox.None{}, ExternalMCPAgents: refresh, CronJobs: cron}
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

// InspectAgents lints or graphs the live runtime directory ("current") or a stage of the workspace
// ("next"), which it removes afterwards, against the live config.
func TestRuntimeInspectAgents(t *testing.T) {
	cfg := &config.Config{Workspace: t.TempDir(), AutoApproverModel: "gpt-6.1-sol"}
	cfg.Slack.Channels = []config.SlackChannelConfig{{Channel: "@", Agents: []string{"main"}, AllowedUserIDs: []string{"U1"}}}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	agents := map[string]string{
		"main.md":   "---\nmodel: software-development-sol\ndescription: main\nmaxRecursion: 0\npermission:\n  task:\n    \"worker\": allow\n---\nmain\n",
		"worker.md": "---\nmodel: gpt-6.1-sol\ndescription: worker\n---\nworker\n",
	}

	for _, dir := range []string{"agents", cfg.RuntimeDirName() + "/agents"} {
		require.NoError(t, root.MkdirAll(dir, 0o755))

		for name, content := range agents {
			require.NoError(t, root.WriteFile(dir+"/"+name, []byte(content), 0o600))
		}
	}

	// Plural permissions, finding RC006, is only in the workspace, which the next start loads.
	require.NoError(t, root.WriteFile("agents/lint-bad.md", []byte("---\nmodel: gpt-6.1-sol\ndescription: bad\npermissions:\n  bash:\n    \"echo ok\": allow\n---\nbad\n"), 0o600))

	rt := &Runtime{Cfg: config.NewLockedConfig(cfg), Log: slog.New(slog.DiscardHandler), Assemble: &frontendAssemblerMock{ValidateAssetsFunc: func(*config.Config, string) error { return nil }}}
	for _, tt := range []struct {
		target   protocol.InspectTarget
		kind     protocol.InspectKind
		want     string
		findings int
	}{
		{protocol.InspectCurrent, protocol.InspectLint, "rocketclaw lint current: OK\n", 0},
		{protocol.InspectNext, protocol.InspectLint, "rocketclaw lint next: found 1 findings\nRC006 error agents/lint-bad.md: plural permissions frontmatter is ignored; use permission\n", 1},
		{protocol.InspectCurrent, protocol.InspectGraph, "digraph agent_graph {\n  \"main\" [label=\"main\\nmaxRecursion=0\"];\n  \"worker\" [label=\"worker\\nmaxRecursion=unbounded\"];\n  \"main\" -> \"worker\";\n}\n", 0},
		// Settings never show who may use the server: Slack allowed user IDs.
		{protocol.InspectCurrent, protocol.InspectSettings, `{
  "auto_approver_model": "gpt-6.1-sol",
  "slack": {
    "channels": [
      {
        "channel": "@",
        "agents": [
          "main"
        ]
      }
    ]
  },
  "server_display_name": "",
  "logging": {
    "level": ""
  },
  "instrumentation": {
    "hide_inputs": false,
    "hide_outputs": false
  },
  "web": {
    "sentry": {
      "environment": "",
      "traces_sample_rate": null
    }
  },
  "overlays": null
}
`, 0},
	} {
		output, findings, err := rt.InspectAgents(tt.target, tt.kind)
		require.NoError(t, err, tt.kind)
		require.Equal(t, tt.want, output, tt.kind)
		require.Equal(t, tt.findings, findings, tt.kind)
	}

	require.Equal(t, []string{"U1"}, rt.Cfg.Clone().Slack.Channels[0].AllowedUserIDs, "settings leave the live config intact")

	stages, err := filepath.Glob(filepath.Join(cfg.Workspace, ".rocketclaw-inspect-*"))
	require.NoError(t, err)
	require.Empty(t, stages, "next removes its stage")
}

// A save validates the candidate against the runtime assets, stores it, then applies it; a save that
// keeps the overlay list never publishes pending workspace edits.
func TestRuntimeSaveBehavior(t *testing.T) {
	cfg := &config.Config{Workspace: t.TempDir(), AutoApproverModel: "gpt-a"}
	cfg.Slack.Channels = []config.SlackChannelConfig{{Channel: "@", Agents: []string{"main"}, AllowedUserIDs: []string{"U1"}}, {Channel: "#cron", Agents: []string{"main"}, AllowedUserIDs: []string{"U1"}}}
	require.NoError(t, cfg.ValidateBehavior())

	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	const (
		oldAgent = "---\ndescription: Original\nmodel: test-model\n---\nOriginal prompt\n"
		newAgent = "---\ndescription: Updated\nmodel: test-model\n---\nUpdated prompt\n"
	)

	require.NoError(t, root.MkdirAll("agents", 0o755))
	require.NoError(t, root.WriteFile("agents/main.md", []byte(oldAgent), 0o600))

	log := slog.New(slog.DiscardHandler)
	require.NoError(t, skel.SyncInWithOverlays(cfg.Workspace, cfg.RuntimeDirName(), nil, log))
	require.NoError(t, root.WriteFile("agents/main.md", []byte(newAgent), 0o600))

	store := newTestSessionService(t)
	require.NoError(t, putBehaviorSettings(t.Context(), store.db, new(cfg.Behavior())))

	// fail names the apply step a save fails at after it wrote the document; rejectAssets fails the
	// frontend asset check before it.
	var (
		fail         string
		rejectAssets bool
		cancelRun    context.CancelFunc
	)

	refresh, cron := &runtimeRefresherMock{}, &runtimeRefresherMock{RefreshFunc: func() error { return nil }}
	rt := &Runtime{
		Cfg: config.NewLockedConfig(cfg), Log: log, logLevel: new(slog.LevelVar), RunCtx: t.Context(), Sessions: store, ExternalMCPAgents: refresh, CronJobs: cron, sandboxes: sandbox.None{},
		Assemble: &frontendAssemblerMock{ValidateAssetsFunc: func(_ *config.Config, runtimeDir string) error {
			if rejectAssets {
				return errors.New(`cron job "report" is invalid`)
			}

			// A staged entry that collides with a preserved live directory fails the asset swap part way.
			if fail == "swap" {
				for _, dir := range []string{runtimeDir, cfg.RuntimeDirName()} {
					require.NoError(t, root.MkdirAll(dir+"/attachments/kept", 0o755))
				}
			}

			return nil
		}},
	}
	refresh.RefreshFunc = func() error {
		switch {
		case fail == "refresh" && rt.Cfg.Clone().AutoApproverModel == "gpt-c":
			return errors.New("refresh failed")
		case fail == "commit":
			cancelRun()
		}

		return nil
	}
	edit := func(change func(*config.Behavior)) *config.Behavior {
		behavior := rt.Cfg.Clone().Behavior()
		change(&behavior)

		return &behavior
	}
	stored := func() config.Behavior {
		t.Helper()

		behavior, found, err := store.behaviorSettings(t.Context())
		require.NoError(t, err)
		require.True(t, found)

		return behavior
	}
	liveAgent := func(name string) string {
		t.Helper()

		data, err := root.ReadFile(cfg.RuntimeDirName() + "/agents/" + name)
		require.NoError(t, err)

		return string(data)
	}

	before := cfg.Behavior()

	for _, tt := range []struct {
		name   string
		change func(*config.Behavior)
		want   string
	}{
		{"channel without agents", func(b *config.Behavior) { b.Slack.Channels[1].Agents = nil }, "slack.channels[].agents is required"},
		{"frontend assets rejected", func(b *config.Behavior) { b.AutoApproverModel, rejectAssets = "gpt-s", true }, `cron job "report" is invalid`},
		{"overlay fails to sync", func(b *config.Behavior) { b.Overlays = []string{filepath.Join(t.TempDir(), "missing")} }, "apply configured rocketclaw overlay"},
	} {
		require.ErrorContains(t, rt.SaveBehavior(edit(tt.change)), tt.want, tt.name)

		rejectAssets = false

		require.Equal(t, before, rt.Cfg.Clone().Behavior(), tt.name)
		require.Equal(t, before, stored(), tt.name)
		require.Equal(t, oldAgent, liveAgent("main.md"), tt.name)
	}

	require.Empty(t, refresh.RefreshCalls())

	saved := edit(func(b *config.Behavior) { b.AutoApproverModel, b.Logging.Level = "gpt-b", "warn" })
	require.NoError(t, rt.SaveBehavior(saved))
	require.Equal(t, "gpt-b", rt.Cfg.Clone().AutoApproverModel)
	require.Equal(t, rt.Cfg.Clone().Behavior(), stored())
	require.Equal(t, slog.LevelWarn, rt.logLevel.Level())
	require.Equal(t, oldAgent, liveAgent("main.md"), "a save that keeps the overlays does not publish workspace edits")
	require.Len(t, refresh.RefreshCalls(), 1)
	require.Len(t, cron.RefreshCalls(), 1)

	repo := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repo, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "agents", "remote.md"), []byte("---\ndescription: Remote\nmodel: test-model\n---\nRemote prompt\n"), 0o600))

	for _, args := range [][]string{{"init"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "remote"}} {
		output, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, err, string(output))
	}

	// A failure at any apply step after the document is written restores the assets, behavior, and
	// log level, refreshes External MCP agents and the cron jobs again, and leaves the stored row as it was.
	before = rt.Cfg.Clone().Behavior()

	for _, tt := range []struct {
		fail      string
		change    func(*config.Behavior)
		want      string
		refreshes int
	}{
		{"swap", func(b *config.Behavior) { b.Overlays, b.Logging.Level = []string{repo}, "error" }, "commit rocketclaw reload stage", 0},
		{"refresh", func(b *config.Behavior) { b.AutoApproverModel, b.Logging.Level = "gpt-c", "error" }, "refresh failed", 2},
		{"commit", func(b *config.Behavior) { b.Overlays, b.Logging.Level = []string{repo}, "error" }, "commit behavior settings", 2},
	} {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		rt.RunCtx, cancelRun, fail = ctx, cancel, tt.fail
		calls := len(refresh.RefreshCalls())

		require.ErrorContains(t, rt.SaveBehavior(edit(tt.change)), tt.want, tt.fail)
		require.Equal(t, before, rt.Cfg.Clone().Behavior(), tt.fail)
		require.Equal(t, before, stored(), tt.fail)
		require.Equal(t, slog.LevelWarn, rt.logLevel.Level(), tt.fail)
		require.Equal(t, oldAgent, liveAgent("main.md"), tt.fail)
		require.NoFileExists(t, filepath.Join(cfg.Workspace, cfg.RuntimeDirName(), "agents", "remote.md"), tt.fail)
		require.Len(t, refresh.RefreshCalls(), calls+tt.refreshes, tt.fail)
	}

	rt.RunCtx, fail = t.Context(), ""

	require.NoError(t, rt.SaveBehavior(edit(func(b *config.Behavior) { b.Overlays = []string{repo} })))
	require.Equal(t, []string{repo}, rt.Cfg.Clone().Overlays)
	require.Equal(t, rt.Cfg.Clone().Behavior(), stored())
	require.Contains(t, liveAgent("remote.md"), "Remote prompt")
	require.Equal(t, newAgent, liveAgent("main.md"), "an overlay change re-stages runtime assets")
	require.Len(t, refresh.RefreshCalls(), 6)
	require.Len(t, cron.RefreshCalls(), 5)
}
