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
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/stretchr/testify/require"
)

func TestRuntimeReload(t *testing.T) {
	sessions := newTestSessionService(t)

	for _, scenario := range []string{"success", "invalid-agent", "invalid-frontend", "refresh-failure", "cron-refresh-failure"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := &config.Config{Workspace: t.TempDir(), Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"primary"}}}}}
			root, err := os.OpenRoot(cfg.Workspace)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			require.NoError(t, root.Mkdir("agents", 0o755))

			const (
				oldAgent = "---\ndescription: Original\nmodel: test-model\nsticky: slack\n---\nOriginal prompt\n"
				newAgent = "---\ndescription: Updated\nmodel: test-model\n---\nUpdated prompt\n"
			)

			require.NoError(t, root.WriteFile("agents/primary.md", []byte(oldAgent), 0o600))

			log := slog.New(slog.DiscardHandler)
			require.NoError(t, skel.SyncInWithOverlays(cfg.Workspace, cfg.RuntimeDirName(), cfg.Overlays, log))

			updated, warnings := newAgent, "Slack channel #ops uses agent primary, which is not sticky: slack, so it does not answer mentions there."
			if scenario == "invalid-agent" {
				updated, warnings = "---\nmodel: test-model\nsudu: yes\n---\n", "Agent primary was not loaded: sudu: not an advanced-mode key. "+warnings
			}

			require.NoError(t, root.WriteFile("agents/primary.md", []byte(updated), 0o600))
			require.NoError(t, root.WriteFile("rocketclaw.json", []byte("invalid config"), 0o600))

			assembler := &frontendAssemblerMock{GateFunc: passGate, ValidateAssetsFunc: func(*config.Config, string) error {
				if scenario == "invalid-frontend" {
					return errors.New("invalid staged frontend")
				}

				return nil
			}}
			refresh, cron := &runtimeRefresherMock{}, &runtimeRefresherMock{}
			rt := &Runtime{Cfg: config.NewLockedConfig(cfg), Log: log, RunCtx: t.Context(), Sessions: sessions, Assemble: assembler, sandboxes: sandbox.None{}, ExternalMCPAgents: refresh, Routines: cron}
			refreshed := func(failure string) error {
				require.False(t, rt.reloadMu.TryLock(), "reload remains serialized through each refresh")
				live, err := root.ReadFile(cfg.RuntimeDirName() + "/agents/primary.md")
				require.NoError(t, err)
				require.Equal(t, updated, string(live), "refresh runs after replacement")

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
			if scenario == "success" || scenario == "invalid-agent" {
				require.NoError(t, err)
				require.Equal(t, "rocketclaw runtime assets reloaded\n\n"+warnings, receipt)
			} else {
				require.Error(t, err)
				require.Empty(t, receipt)
			}

			live, err := root.ReadFile(cfg.RuntimeDirName() + "/agents/primary.md")
			require.NoError(t, err)

			if scenario == "invalid-frontend" {
				require.Equal(t, oldAgent, string(live))
				require.Empty(t, refresh.RefreshCalls())
				require.Empty(t, cron.RefreshCalls())
				require.Empty(t, rt.SlackWarning())
			} else {
				require.Equal(t, updated, string(live))
				require.Equal(t, warnings, rt.SlackWarning())
				require.Len(t, refresh.RefreshCalls(), 1)

				routineCalls := 1
				if scenario == "refresh-failure" {
					routineCalls = 0
				}

				require.Len(t, cron.RefreshCalls(), routineCalls)
			}
		})
	}
}

func TestRuntimeSaveBehavior(t *testing.T) {
	cfg := &config.Config{Workspace: t.TempDir(), AutoApproverModel: "gpt-a", Decision: map[config.DecisionProvider]config.DecisionProviderConfig{config.DecisionOpenAI: {APIKey: "key"}}}
	cfg.Slack.Channels = []config.SlackChannelConfig{{Channel: "@", Agents: []string{"primary"}, AllowedUserIDs: []string{"U1"}}, {Channel: "#cron", Agents: []string{"primary"}, AllowedUserIDs: []string{"U1"}}}
	require.NoError(t, cfg.ValidateBehavior())

	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	const (
		oldAgent = "---\ndescription: Original\nmodel: test-model\n---\nOriginal prompt\n"
		newAgent = "---\ndescription: Updated\nmodel: test-model\n---\nUpdated prompt\n"
	)

	require.NoError(t, root.MkdirAll("agents", 0o755))
	require.NoError(t, root.WriteFile("agents/primary.md", []byte(oldAgent), 0o600))

	log := slog.New(slog.DiscardHandler)
	require.NoError(t, skel.SyncInWithOverlays(cfg.Workspace, cfg.RuntimeDirName(), nil, log))
	require.NoError(t, root.WriteFile("agents/primary.md", []byte(newAgent), 0o600))

	store := newTestSessionService(t)
	require.NoError(t, putBehaviorSettings(t.Context(), store.db, new(cfg.Behavior())))

	var (
		fail         string
		rejectAssets bool
		cancelRun    context.CancelFunc
	)

	refresh, cron := &runtimeRefresherMock{}, &runtimeRefresherMock{}
	rt := &Runtime{
		Cfg: config.NewLockedConfig(cfg), Log: log, logLevel: new(slog.LevelVar), RunCtx: t.Context(), Sessions: store, ExternalMCPAgents: refresh, Routines: cron, sandboxes: sandbox.None{},
		Assemble: &frontendAssemblerMock{GateFunc: passGate, ValidateAssetsFunc: func(_ *config.Config, runtimeDir string) error {
			if rejectAssets {
				return errors.New(`cron job "report" is invalid`)
			}

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
	cron.RefreshFunc = func() error {
		if fail == "cron" {
			return errors.New("cron refresh failed")
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
		{"main model on an unconfigured provider", func(b *config.Behavior) { b.EmbeddedAgents.Main.Model = "nope/gpt-x" }, `embedded_agents.main.model: provider "nope" is not configured`},
		{"title model on an unconfigured provider", func(b *config.Behavior) { b.TitleModel = "nope/gpt-x#low" }, `title_model: provider "nope" is not configured`},
		{"ambient decision model on an unconfigured provider", func(b *config.Behavior) { b.Slack.AmbientDecisionModel = "typesafe/jev-latest" }, `slack.ambient_decision_model: "typesafe/jev-latest" must be provider/model with its provider configured in decision`},
	} {
		require.ErrorContains(t, rt.SaveBehavior(edit(tt.change)), tt.want, tt.name)

		rejectAssets = false

		require.Equal(t, before, rt.Cfg.Clone().Behavior(), tt.name)
		require.Equal(t, before, stored(), tt.name)
		require.Equal(t, oldAgent, liveAgent("primary.md"), tt.name)
		require.Empty(t, rt.SlackWarning(), tt.name)
	}

	require.Empty(t, refresh.RefreshCalls())

	agents, err := LoadRuntimeAgents(rt.Cfg, cfg.RuntimeDirName())
	require.NoError(t, err)
	require.Contains(t, agents.Items, "main")

	saved := edit(func(b *config.Behavior) {
		b.AutoApproverModel, b.Logging.Level, b.EmbeddedAgents.Main.Disabled = "gpt-b", "warn", true
		b.Slack.AmbientDecisionModel = " openai/gpt-6-luna "
	})
	require.NoError(t, rt.SaveBehavior(saved))
	require.Equal(t, "openai/gpt-6-luna", rt.Cfg.Clone().Slack.AmbientDecisionModel, "a save turns ambient listening on at once")

	agents, err = LoadRuntimeAgents(rt.Cfg, cfg.RuntimeDirName())
	require.NoError(t, err)
	require.NotContains(t, agents.Items, "main", "saving main turned off removes it")
	require.Equal(t, "Slack channel @ uses agent primary, which is not sticky: slack, so it does not answer mentions there. Slack channel #cron uses agent primary, which is not sticky: slack, so it does not answer mentions there.", rt.SlackWarning())
	require.Equal(t, "gpt-b", rt.Cfg.Clone().AutoApproverModel)
	require.Equal(t, rt.Cfg.Clone().Behavior(), stored())
	require.Equal(t, slog.LevelWarn, rt.logLevel.Level())
	require.Equal(t, oldAgent, liveAgent("primary.md"), "a save that keeps the overlays does not publish workspace edits")
	require.Len(t, refresh.RefreshCalls(), 1)
	require.Len(t, cron.RefreshCalls(), 1)

	repo := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repo, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "agents", "remote.md"), []byte("---\ndescription: Remote\nmodel: test-model\n---\nRemote prompt\n"), 0o600))

	for _, args := range [][]string{{"init"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "remote"}} {
		output, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, err, string(output))
	}

	before = rt.Cfg.Clone().Behavior()

	for _, tt := range []struct {
		fail      string
		change    func(*config.Behavior)
		want      string
		refreshes int
	}{
		{"swap", func(b *config.Behavior) { b.Overlays, b.Logging.Level = []string{repo}, "error" }, "commit rocketclaw reload stage", 0},
		{"refresh", func(b *config.Behavior) { b.AutoApproverModel, b.Logging.Level = "gpt-c", "error" }, "refresh failed", 2},
		{"cron", func(b *config.Behavior) { b.AutoApproverModel, b.Logging.Level = "gpt-e", "error" }, "refresh routines after restoring: cron refresh failed", 2},
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
		require.Equal(t, oldAgent, liveAgent("primary.md"), tt.fail)
		require.NoFileExists(t, filepath.Join(cfg.Workspace, cfg.RuntimeDirName(), "agents", "remote.md"), tt.fail)
		require.Len(t, refresh.RefreshCalls(), calls+tt.refreshes, tt.fail)
	}

	rt.RunCtx, fail = t.Context(), ""

	require.NoError(t, rt.SaveBehavior(edit(func(b *config.Behavior) { b.Overlays, b.Slack.AmbientDecisionModel = []string{repo}, "" })))
	require.Equal(t, []string{repo}, rt.Cfg.Clone().Overlays)
	require.Empty(t, rt.Cfg.Clone().Slack.AmbientDecisionModel, "a save turns ambient listening off at once")
	require.Equal(t, rt.Cfg.Clone().Behavior(), stored())
	require.Contains(t, liveAgent("remote.md"), "Remote prompt")
	require.Equal(t, newAgent, liveAgent("primary.md"), "an overlay change re-stages runtime assets")
	require.Len(t, refresh.RefreshCalls(), 8)
	require.Len(t, cron.RefreshCalls(), 7)
}
