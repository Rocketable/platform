package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cirello.io/pglock"
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

	require.NoError(t, seed.Stop())

	var (
		order       []string
		assembledRT *Runtime
		threadID    = protocol.SlackThreadConversationID("C123", "111.0")
	)

	slack := &slackFrontendMock{
		StartFunc: func(context.Context) error {
			order = append(order, "slack started")
			return nil
		},
		DrainSteersFunc: func(context.Context, string) []string { return []string{"steer"} },
		ActivateEnqueueFunc: func(context.Context, *protocol.ThreadQueueItem, *protocol.InboundMessage) error {
			return nil
		},
		StopFunc: func(cleanupCtx context.Context) error {
			require.NoError(t, cleanupCtx.Err())
			require.NoError(t, ctx.Err(), "restart shuts down without the caller's signal")
			require.True(t, assembledRT.threads.stopping, "bridges stop before frontends")
			require.ErrorIs(t, assembledRT.RunCtx.Err(), context.Canceled, "the run lock context ends after bridges and before frontends")

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

			bridge := rt.threads.bridges[threadID].(*Bridge)
			reloaded, err := bridge.config.RequestReload("test reload")
			require.NoError(t, err)
			require.Equal(t, "rocketclaw runtime assets reloaded", reloaded)

			_, err = bridge.config.StartNewThread(rt.RunCtx, &protocol.StartNewThreadRequest{CurrentAgent: "missing"})
			require.ErrorContains(t, err, `agent "missing" is not configured`)

			msg, err := bridge.config.RequestRestart("test restart")
			require.NoError(t, err)
			require.Equal(t, "restart requested; runtime cancellation started", msg)
			require.NoError(t, rt.RunCtx.Err(), "restart cancels work, not the run lock")

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
	// A canceled workspace-lock wait must also join the pre-lock health reporter.
	seed, err = NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	client, err := pglock.UnsafeNew(seed.db, pglock.WithCustomTable(runLockTable), pglock.WithHeartbeatFrequency(0))
	require.NoError(t, err)
	require.NoError(t, client.TryCreateTable())
	lock, err := client.Acquire(runLockName, pglock.FailIfLocked())
	require.NoError(t, err)
	ctxLock, cancelLock := context.WithTimeout(t.Context(), time.Second)
	err = Run(ctxLock, cfg, configPath, slog.New(slog.DiscardHandler), assembler)

	cancelLock()
	require.ErrorIs(t, err, pglock.ErrNotAcquired)
	require.Empty(t, assembler.AssembleCalls())
	require.NoError(t, lock.Close())
	require.NoError(t, seed.Stop())

	require.ErrorIs(t, Run(ctx, cfg, configPath, slog.New(slog.DiscardHandler), assembler), ErrRestartRequested)
	require.Equal(t, []string{"validated", "validated", "assembled", "slack started", "slack stopped", "extra stopped"}, order)

	bridge := assembledRT.threads.bridges[threadID].(*Bridge)
	require.Equal(t, []rocketcode.PromptInput{{Text: "steer"}}, bridge.config.SteerDrain.Drain(t.Context(), 0), "Slack steers drain once Slack is attached")
	require.NoError(t, bridge.config.EnqueueActivation.Activate(t.Context(), &protocol.ThreadQueueItem{ID: "q1"}, protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindEnqueue, "later", true)))

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
	boundary, err := seed.AppendEntryID(t.Context(), conversationID, testSessionEntry("recorded", "answer"))
	require.NoError(t, err)
	_, _, err = stageRevertDB(t.Context(), seed.db, conversationID, fmt.Sprintf("%d:0", boundary))
	require.NoError(t, err)
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
					StartFunc:       func(context.Context) error { return nil },
					DrainSteersFunc: func(context.Context, string) []string { return nil },
					ActivateEnqueueFunc: func(context.Context, *protocol.ThreadQueueItem, *protocol.InboundMessage) error {
						return nil
					},
					StopFunc: func(context.Context) error { return nil },
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

	require.Empty(t, runQueuedStartup(t, time.Second), "daemon startup must not resume a staged queue")
	seed, err = NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	marker, _, _, err := seed.RevertState(t.Context(), conversationID)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d:0", boundary), marker)

	queue, err := seed.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Equal(t, []string{"q1", "q2"}, []string{queue[0].ID, queue[1].ID})
	_, err = seed.db.ExecContext(t.Context(), `UPDATE managed_conversations SET revert_message_id = '' WHERE conversation_id = $1`, conversationID)
	require.NoError(t, err)
	require.NoError(t, seed.Stop())
	require.Equal(t, []string{"answer", "answer"}, runQueuedStartup(t, 20*time.Second))
	require.Empty(t, runQueuedStartup(t, time.Second))
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

// AE1, Risk "Lock lease": shutdown waits for a running bash command with the run
// lock context, and so its heartbeat, still alive, and stops frontends only after.
func TestShutdownKeepsRunLockAliveDuringBash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "waiter.md"), []byte("---\ndescription: Waiter\nmode: primary\nmodel: gpt-5.5\npermission:\n  bash:\n    \"*\": allow\n---\nPrompt\n"), 0o600))
	started, finished := filepath.Join(workspace, "started"), filepath.Join(workspace, "done")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		code, err := json.Marshal(struct {
			Code string `json:"code"`
		}{"def main():\n    return bash(command=r'''touch " + started + " && sleep 2 && touch " + finished + "''')\n"})
		assert.NoError(t, err)

		output := fmt.Sprintf(`{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_b","name":"execute","arguments":%q}`, code)

		if strings.Contains(string(body), "function_call_output") {
			output = `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[]}]}`
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[` + output + `]}`))
	}))
	t.Cleanup(server.Close)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}}

	ctx, signal := context.WithCancel(t.Context())
	defer signal()

	runCtx := make(chan context.Context, 1)

	slack := &slackFrontendMock{
		StartFunc:       func(context.Context) error { return nil },
		DrainSteersFunc: func(context.Context, string) []string { return nil },
		StopFunc: func(context.Context) error {
			assert.FileExists(t, finished, "frontends stop only after bash finishes")
			return nil
		},
	}
	assembler := &frontendAssemblerMock{
		ValidateAssetsFunc: func(*config.Config, string, []string) error { return nil },
		AssembleFunc: func(rt *Runtime) (SlackFrontend, <-chan struct{}, []func(context.Context) error, error) {
			runCtx <- rt.RunCtx

			target := protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.0"}
			_, err := rt.TextRouter.RegisterThread(target, "waiter")
			require.NoError(t, err)

			msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "wait for it", true)
			msg.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.0", ThreadTS: "111.0"}
			_, err = rt.TextRouter.SubmitThreadReply(rt.RunCtx, target, msg)
			require.NoError(t, err)

			return slack, nil, []func(context.Context) error{slack.Stop}, nil
		},
	}

	var logs bytes.Buffer

	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "", slog.New(slog.NewTextHandler(&logs, nil)), assembler) }()

	require.Eventually(t, func() bool {
		_, err := os.Stat(started)
		return err == nil
	}, 10*time.Second, 10*time.Millisecond)
	signal()

	lock := <-runCtx

	for {
		if _, err := os.Stat(finished); err == nil {
			break
		}

		require.NoError(t, lock.Err(), "the run lock stays held while bash runs")
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not finish after bash")
	}

	assert.Len(t, slack.StopCalls(), 1)
	assert.Contains(t, logs.String(), `msg="shutdown waiting for running tool calls" component=thread_bridges conversation_id=slack-thread:C123:111.0`)
}

// Slack starts accepting input only after it is attached, so a Slack message that
// arrives as soon as the connector starts still gets ask_user_question.
func TestSlackMessageAtStartupCanAskUserQuestion(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "main.md"), []byte("---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n"), 0o600))

	bodies := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies <- string(body)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}}

	ctx, signal := context.WithCancel(t.Context())
	defer signal()

	var assembled *Runtime

	slack := &slackFrontendMock{
		DrainSteersFunc: func(context.Context, string) []string { return nil },
		StopFunc:        func(context.Context) error { return nil },
	}
	slack.StartFunc = func(context.Context) error {
		target := protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.0"}
		if _, err := assembled.TextRouter.RegisterThread(target, "main"); err != nil {
			return fmt.Errorf("register startup thread: %w", err)
		}

		msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "first message", true)
		msg.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.0", ThreadTS: "111.0"}

		if _, err := assembled.TextRouter.SubmitThreadReply(assembled.RunCtx, target, msg); err != nil {
			return fmt.Errorf("submit startup message: %w", err)
		}

		return nil
	}
	assembler := &frontendAssemblerMock{
		ValidateAssetsFunc: func(*config.Config, string, []string) error { return nil },
		AssembleFunc: func(rt *Runtime) (SlackFrontend, <-chan struct{}, []func(context.Context) error, error) {
			assembled = rt
			return slack, nil, []func(context.Context) error{slack.Stop}, nil
		},
	}

	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, "", slog.New(slog.DiscardHandler), assembler) }()

	select {
	case body := <-bodies:
		assert.Contains(t, body, `"name":"ask_user_question"`)
	case err := <-done:
		t.Fatalf("Run returned before the startup message ran: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the startup Slack message never ran")
	}

	assert.Len(t, slack.StartCalls(), 1)
	signal()
	require.NoError(t, <-done)
}
