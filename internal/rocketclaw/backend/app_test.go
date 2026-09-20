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

	seed, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	recoveryID := protocol.SlackThreadConversationID("C888", "888.0")
	require.NoError(t, seed.UpsertThread(recoveryID, ThreadState{Agent: "main"}))
	require.NoError(t, seed.UpsertActiveTurn(ctx, &rocketcode.ActiveTurnCheckpoint{TurnID: "recover-1", ConversationKey: recoveryID, Agent: "main", Model: "gpt-5.5", DisplayModel: "gpt-5.5", ReplayInput: startupRecoveryReplayInput(t)}, nil))
	require.NoError(t, seed.UpsertActiveTurn(ctx, &rocketcode.ActiveTurnCheckpoint{TurnID: "cron", ConversationKey: "cron:daily"}, nil))
	require.NoError(t, seed.UpsertActiveTurn(ctx, &rocketcode.ActiveTurnCheckpoint{TurnID: "oneoff", ConversationKey: "one-off-cron:job"}, nil))
	require.NoError(t, seed.UpsertActiveTurn(ctx, &rocketcode.ActiveTurnCheckpoint{TurnID: "unknown", ConversationKey: protocol.SlackThreadConversationID("C9", "9.9")}, nil))
	require.NoError(t, seed.Stop())

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

			bridge := assembledRT.threads.bridges[threadID].(*Bridge)
			inputs := bridge.config.SteerDrain.Drain(ctx, 0)
			require.Equal(t, []rocketcode.PromptInput{{Text: "steer"}}, inputs)
			require.NoError(t, bridge.config.EnqueueActivation.Activate(ctx, &protocol.ThreadQueueItem{ID: "q1"}, protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindEnqueue, "", "later", true)))
			msg, err := bridge.config.RequestRestart("test restart")
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
			require.Equal(t, map[string]string{"alice": "secret"}, rt.ExternalMCPUsers)

			target := protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.0"}
			created, err := rt.TextRouter.RegisterThread(target, "main")
			require.NoError(t, err)
			require.True(t, created)
			require.False(t, rt.TextRouter.ThreadBusy(target))
			require.NoError(t, rt.threads.PickLaterWork(rt.RunCtx, protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)))
			scheduled, err := rt.TextRouter.ScheduledMessages(target)
			require.NoError(t, err)
			require.Empty(t, scheduled)

			release, reserved, err := rt.TextRouter.ReserveWorkflowTurn(target)
			require.NoError(t, err)
			require.True(t, reserved)
			release()

			bridge := rt.threads.bridges[threadID].(*Bridge)
			reloaded, err := bridge.config.RequestReload("test reload")
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

func TestRunStartsPersistedQueueWithoutOtherWork(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Queue recovery\nmodel: gpt-5.5\npermission: {}\n---\nRespond concisely.\n")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "rocketclaw.json"), []byte(`{}`), 0o600))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Connection", "close")

		_, errWrite := w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[]}]}]}`))
		if errWrite != nil {
			t.Error(errWrite)
		}
	}))
	t.Cleanup(server.Close)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	conversationID := "web-queued"
	seed, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NoError(t, seed.UpsertThread(conversationID, ThreadState{Agent: "main"}))
	require.NoError(t, seed.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "first", Principal: "alice", Source: protocol.SourceWeb, Position: 0, StashAt: time.Unix(1, 0).UTC()}))
	require.NoError(t, seed.PutThreadQueueItem("q2", &protocol.ThreadQueueItem{ID: "q2", ConversationID: conversationID, Message: "second", Principal: "alice", Source: protocol.SourceWeb, Position: 1, StashAt: time.Unix(2, 0).UTC()}))
	require.NoError(t, seed.Stop())

	cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}, Slack: config.SlackConfig{
		Channels: []config.SlackChannelConfig{{Channel: "@"}},
	}}

	runQueuedStartup := func(t *testing.T, wait time.Duration) []string {
		t.Helper()

		finals := make(chan string, 4)
		copyDone := make(chan struct{})
		assembler := &frontendAssemblerMock{
			ValidateAssetsFunc: func(*config.Config, string, []string) error { return nil },
			AssembleFunc: func(rt *Runtime) (SlackFrontend, <-chan struct{}, []func(context.Context) error, error) {
				events := rt.Subscribe(rt.RunCtx)
				go func() {
					for event := range events {
						if event.Message.Complete && strings.TrimSpace(event.Message.Text) != "" {
							finals <- event.Message.Text
						}

						event.Acknowledgement <- nil
					}
				}()

				return &slackFrontendMock{
					DrainSteersFunc:          func(context.Context, string) []string { return nil },
					RestorePendingSteersFunc: func(string, []protocol.PendingSteer) {},
					DiscardPendingSteersFunc: func(context.Context, []protocol.PendingSteer) {},
					ActivateEnqueueFunc: func(context.Context, *protocol.ThreadQueueItem, *protocol.InboundMessage) error {
						return nil
					},
					SetPendingSteersSinkFunc: func(protocol.PendingSteersSink) {},
					StopFunc:                 func(context.Context) error { return nil },
				}, copyDone, nil, nil
			},
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		errRun := make(chan error, 1)
		go func() {
			errRun <- Run(ctx, cfg, filepath.Join(workspace, "rocketclaw.json"), slog.New(slog.DiscardHandler), assembler)
		}()

		var texts []string

		deadline := time.After(wait)

		for len(texts) < 2 {
			select {
			case text := <-finals:
				texts = append(texts, text)
			case err := <-errRun:
				require.NoError(t, err)
				close(copyDone)

				return texts
			case <-deadline:
				close(copyDone)
				require.NoError(t, <-errRun)

				return texts
			}
		}

		close(copyDone)
		require.NoError(t, <-errRun)

		return texts
	}

	require.Equal(t, []string{"answer", "answer"}, runQueuedStartup(t, 20*time.Second))
	require.Empty(t, runQueuedStartup(t, time.Second))
}

func TestRunHoldsPairedStartupRecoveryBeforeAssemble(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Pair recovery\nmodel: gpt-5.5\npermission: {}\n---\nRespond concisely.\n")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "rocketclaw.json"), []byte(`{}`), 0o600))

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	managedID := protocol.SlackThreadConversationID("C1", "1.1")
	privateID := "external_mcp:planner:private"
	seed, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NoError(t, seed.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: privateID, ManagedConversationID: managedID, SlackChannel: "#ops"}))

	for _, conversationID := range []string{privateID, managedID} {
		_, err = seed.AppendEntryID(t.Context(), conversationID, testSessionEntryAt(time.Now().UTC(), conversationID))
		require.NoError(t, err)
	}

	require.NoError(t, seed.UpsertActiveTurn(t.Context(), &rocketcode.ActiveTurnCheckpoint{TurnID: "pair-turn", ConversationKey: privateID, Agent: "planner", Model: "gpt-5.5", DisplayModel: "gpt-5.5", ReplayInput: startupRecoveryReplayInput(t)}, nil))
	require.NoError(t, seed.Stop())

	held := false
	copyDone := make(chan struct{})
	close(copyDone)

	assembler := &frontendAssemblerMock{
		ValidateAssetsFunc: func(*config.Config, string, []string) error { return nil },
		AssembleFunc: func(rt *Runtime) (SlackFrontend, <-chan struct{}, []func(context.Context) error, error) {
			held = rt.Sessions.startupRecoveryBlocks(managedID) && rt.Sessions.startupRecoveryBlocks(privateID)
			return nil, copyDone, nil, nil
		},
	}
	require.NoError(t, Run(t.Context(), &config.Config{Workspace: workspace, DatabaseURL: dsn, Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "@"}}}}, filepath.Join(workspace, "rocketclaw.json"), slog.New(slog.DiscardHandler), assembler))
	require.True(t, held)
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
