package backend

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunInitializesRuntimeAndCleansUpOnCancellation(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("agents", 0o755))

	const agent = "---\ndescription: Lifecycle test agent\nmodel: gpt-5.5\n---\nRespond concisely.\n"
	require.NoError(t, root.WriteFile("agents/main.md", []byte(agent), 0o600))

	resources := make([]*os.File, 2)
	for i := range resources {
		resources[i], err = root.Open("agents/main.md")
		require.NoError(t, err)
		t.Cleanup(func() { _ = resources[i].Close() })
	}

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(collector.Close)

	configPath := filepath.Join(workspace, "rocketclaw.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "rocketclaw.users.json"), []byte(`{"alice":"secret"}`), 0o600))

	cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn, Slack: config.SlackConfig{
		Channels: []config.SlackChannelConfig{{Channel: "@"}, {Channel: "#general"}},
	}, MCPExternal: config.MCPExternalConfig{Enabled: true}, Instrumentation: config.InstrumentationConfig{
		Enabled: true, CollectorEndpoint: collector.URL, ProjectName: "rocketclaw-test", APIKey: "token",
	}}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	seed, err := NewSessionServiceIn(dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	recoveryID := protocol.SlackThreadConversationID("C888", "888.0")
	require.NoError(t, seed.UpsertThread(recoveryID, ThreadState{Agent: "main"}))
	require.NoError(t, seed.StartActiveTurn(ctx, &rocketcode.ActiveTurnCheckpoint{TurnID: "recover-1", ConversationKey: recoveryID, Agent: "main", Model: "gpt-5.5", DisplayModel: "gpt-5.5", ReplayInput: startupRecoveryReplayInput(t)}))
	require.NoError(t, seed.StartActiveTurn(ctx, &rocketcode.ActiveTurnCheckpoint{TurnID: "cron", ConversationKey: "cron:daily"}))
	require.NoError(t, seed.StartActiveTurn(ctx, &rocketcode.ActiveTurnCheckpoint{TurnID: "oneoff", ConversationKey: "one-off-cron:job"}))
	require.NoError(t, seed.StartActiveTurn(ctx, &rocketcode.ActiveTurnCheckpoint{TurnID: "unknown", ConversationKey: protocol.SlackThreadConversationID("C9", "9.9")}))
	require.NoError(t, seed.Stop(ctx))

	var (
		order       []string
		assembledRT *Runtime
		threadID    = protocol.SlackThreadConversationID("C123", "111.0")
	)

	slack := &slackFrontendMock{
		DrainSteersFunc:          func(context.Context, string) []string { return []string{"steer"} },
		RestorePendingSteersFunc: func(string, []protocol.PendingSteer) {},
		DiscardPendingSteersFunc: func(context.Context, []protocol.PendingSteer) {},
		ActivateEnqueueFunc: func(context.Context, *protocol.ThreadQueueItem, *protocol.InboundMessage) error {
			return nil
		},
		SetPendingSteersSinkFunc: func(sink protocol.PendingSteersSink) {
			require.NotNil(t, sink.Set)
			require.NoError(t, ctx.Err())

			bridge := assembledRT.threads.bridges[threadID].bridge.(*Bridge)
			inputs := bridge.config.SteerDrain.Drain(ctx, 0)
			require.Equal(t, []rocketcode.PromptInput{{Text: "steer"}}, inputs)
			require.NoError(t, bridge.config.EnqueueActivation.Activate(ctx, &protocol.ThreadQueueItem{ID: "q1"}, protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindEnqueue, "", "later", true)))
			msg, err := bridge.config.RequestRestart(ctx, "test restart")
			require.NoError(t, err)
			require.Equal(t, "restart requested; runtime cancellation started", msg)
			cancel()

			order = append(order, "attached")
		},
		StopFunc: func(cleanupCtx context.Context) error {
			require.NoError(t, cleanupCtx.Err())
			require.ErrorIs(t, ctx.Err(), context.Canceled)

			order = append(order, "slack stopped")

			require.NoError(t, resources[0].Close())

			return nil
		},
	}
	assembler := &frontendAssemblerMock{
		ValidateAssetsFunc: func(got *config.Config, runtimeDir string, channels []string) error {
			require.Same(t, cfg, got)
			require.True(t, runtimeDir == cfg.RuntimeDirName() || strings.HasPrefix(runtimeDir, cfg.RuntimeDirName()+"-reload-"))
			require.Equal(t, []string{"#general"}, channels)

			data, err := root.ReadFile(runtimeDir + "/agents/main.md")
			require.NoError(t, err)
			require.Equal(t, agent, string(data))

			order = append(order, "validated")

			return nil
		},
		AssembleFunc: func(rt *Runtime) (SlackFrontend, <-chan struct{}, []func(context.Context) error, error) {
			assembledRT = rt
			require.Same(t, cfg, rt.Cfg)
			require.NoError(t, rt.RunCtx.Err())
			require.NoError(t, rt.Sessions.db.PingContext(rt.RunCtx))
			require.Same(t, rt.threads, rt.TextRouter)
			require.NotNil(t, rt.Channels.Broadcasts)
			require.Equal(t, map[string]string{"alice": "secret"}, rt.ExternalMCPUsers)

			target := protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.0"}
			created, err := rt.TextRouter.RegisterThread(target, "main")
			require.NoError(t, err)
			require.True(t, created)
			require.False(t, rt.TextRouter.ThreadBusy(target))
			require.NoError(t, rt.TextRouter.PickQueuedWork(rt.RunCtx, target))
			scheduled, err := rt.TextRouter.ScheduledMessages(rt.RunCtx, target)
			require.NoError(t, err)
			require.Empty(t, scheduled)

			release, reserved, err := rt.TextRouter.ReserveWorkflowTurn(target)
			require.NoError(t, err)
			require.True(t, reserved)
			release()

			bridge := rt.threads.bridges[threadID].bridge.(*Bridge)
			reloaded, err := bridge.config.RequestReload(rt.RunCtx, "test reload")
			require.NoError(t, err)
			require.Equal(t, "rocketclaw runtime assets reloaded", reloaded)

			_, err = bridge.config.StartNewThread(rt.RunCtx, &protocol.StartNewThreadRequest{Source: protocol.SourceWeb})
			require.ErrorContains(t, err, "not available for web turns")

			order = append(order, "assembled")

			return slack, rt.RunCtx.Done(), []func(context.Context) error{
				slack.Stop,
				func(cleanupCtx context.Context) error {
					require.NoError(t, cleanupCtx.Err())

					order = append(order, "extra stopped")

					require.NoError(t, resources[1].Close())

					return nil
				},
			}, nil
		},
	}
	require.ErrorIs(t, Run(ctx, cfg, configPath, slog.New(slog.DiscardHandler), assembler), ErrRestartRequested)
	require.Equal(t, []string{"validated", "validated", "assembled", "attached", "slack stopped", "extra stopped"}, order)

	for _, resource := range resources {
		_, err := resource.Stat()
		require.ErrorIs(t, err, os.ErrClosed)
	}

	require.Len(t, assembler.AssembleCalls(), 1)
	require.ErrorContains(t, assembler.AssembleCalls()[0].Runtime.Sessions.db.PingContext(t.Context()), "database is closed")
}

func TestConfigureInstrumentationStartsAndStopsExporter(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(collector.Close)

	stop, err := configureInstrumentation(t.Context(), config.InstrumentationConfig{
		Enabled: true, CollectorEndpoint: collector.URL, ProjectName: "rocketclaw-test", APIKey: "token",
	})
	require.NoError(t, err)
	require.NoError(t, stop(t.Context()))

	_, err = configureInstrumentation(t.Context(), config.InstrumentationConfig{Enabled: true, CollectorEndpoint: "not-a-url"})
	require.ErrorContains(t, err, "parse instrumentation.collector_endpoint")
}

func TestKeyedConversationLocksSerializeOneIDAndReleaseEntries(t *testing.T) {
	locks := NewKeyedConversationLocks()
	unlockFirst := locks.Lock("shared")
	acquired := make(chan struct{})
	releaseSecond := make(chan struct{})
	done := make(chan struct{})

	go func() {
		unlockSecond := locks.Lock("shared")

		close(acquired)
		<-releaseSecond
		unlockSecond()
		close(done)
	}()

	select {
	case <-acquired:
		t.Fatal("same conversation lock overtook first holder")
	default:
	}

	unlockFirst()
	<-acquired
	close(releaseSecond)
	<-done

	locks.mu.Lock()
	assert.Empty(t, locks.locks)
	locks.mu.Unlock()
}

func TestKeyedConversationLocksAllowIndependentIDs(t *testing.T) {
	locks := NewKeyedConversationLocks()
	unlockFirst := locks.Lock("first")
	unlockedSecond := make(chan struct{})

	go func() {
		unlockSecond := locks.Lock("second")
		unlockSecond()
		close(unlockedSecond)
	}()

	select {
	case <-unlockedSecond:
	case <-time.After(time.Second):
		t.Fatal("independent conversation ID was blocked")
	}

	unlockFirst()
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
