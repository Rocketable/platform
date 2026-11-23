package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"cirello.io/pglock"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
)

func TestRunInitializesRuntimeAndCleansUpOnCancellation(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("agents", 0o755))

	const agent = "---\ndescription: Lifecycle test agent\nmodel: gpt-6.1-sol\n---\nRespond concisely.\n"
	require.NoError(t, root.WriteFile("agents/primary.md", []byte(agent), 0o600))

	resources := make([]*os.File, 2)
	for i := range resources {
		resources[i], err = root.Open("agents/primary.md")
		require.NoError(t, err)
		t.Cleanup(func() { _ = resources[i].Close() })
	}

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(collector.Close)

	cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn, Slack: config.SlackConfig{
		Channels: []config.SlackChannelConfig{{Channel: "@", Agents: []string{"primary"}, AllowedUserIDs: []string{"U1"}}, {Channel: "#general", Agents: []string{"primary"}, AllowedUserIDs: []string{"U1"}}},
	}, MCPExternal: config.MCPExternalConfig{Enabled: true}, PostHog: config.PostHogConfig{ProjectToken: "phc_test", Host: collector.URL}}
	require.NoError(t, cfg.ValidateBehavior())

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

	assembler := &frontendAssemblerMock{
		GateFunc: passGate,
		ValidateAssetsFunc: func(got *config.Config, runtimeDir string) error {
			require.Equal(t, cfg, got)
			require.True(t, runtimeDir == cfg.RuntimeDirName() || strings.HasPrefix(runtimeDir, cfg.RuntimeDirName()+"-reload-"))

			data, err := root.ReadFile(runtimeDir + "/agents/primary.md")
			require.NoError(t, err)
			require.Equal(t, agent, string(data))

			order = append(order, "validated")

			return nil
		},
		AssembleFunc: func(rt *Runtime) (<-chan struct{}, []func(context.Context) error, error) {
			assembledRT = rt
			require.Equal(t, cfg, rt.Cfg.Clone())
			require.NoError(t, rt.RunCtx.Err())
			require.NoError(t, rt.Sessions.db.PingContext(rt.RunCtx))
			require.Same(t, rt.threads, rt.TextRouter)

			require.NoError(t, rt.threads.store.UpsertThread(threadID, ThreadState{Agent: "primary"}))
			require.NoError(t, rt.threads.PickLaterWork(rt.RunCtx, threadID))

			bridge := rt.threads.bridges[threadID].(*Bridge)
			require.Same(t, rt, bridge.config.RequestReload)
			reloaded, err := bridge.config.RequestReload.Reload("test reload")
			require.NoError(t, err)
			require.Equal(t, "rocketclaw runtime assets reloaded\n\nSlack channel @ uses agent primary, which is not sticky: slack, so it does not answer mentions there. Slack channel #general uses agent primary, which is not sticky: slack, so it does not answer mentions there.", reloaded)

			_, _, err = bridge.threads.createSession(rt.RunCtx, &threadStart{agent: "missing"}, nil, "", nil, nil)
			require.ErrorContains(t, err, `agent "missing" is not configured`)

			msg := bridge.config.RequestRestart.Restart("test restart")
			require.Equal(t, "restart requested; runtime cancellation started", msg)
			require.NoError(t, rt.RunCtx.Err(), "restart cancels work, not the run lock")

			order = append(order, "assembled")

			return rt.RunCtx.Done(), []func(context.Context) error{
				func(cleanupCtx context.Context) error {
					require.NoError(t, cleanupCtx.Err())
					require.NoError(t, ctx.Err(), "restart shuts down without the caller's signal")
					require.True(t, assembledRT.threads.stopping, "bridges stop before frontends")
					require.ErrorIs(t, assembledRT.RunCtx.Err(), context.Canceled, "the run lock context ends after bridges and before frontends")

					order = append(order, "slack stopped")

					require.NoError(t, resources[0].Close())

					return nil
				},
				func(cleanupCtx context.Context) error {
					require.NoError(t, cleanupCtx.Err())

					order = append(order, "extra stopped")

					require.NoError(t, resources[1].Close())

					return nil
				},
			}, nil
		},
	}
	seed, err = NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	client, err := pglock.UnsafeNew(seed.db, pglock.WithCustomTable(runLockTable), pglock.WithHeartbeatFrequency(0))
	require.NoError(t, err)
	require.NoError(t, client.TryCreateTable())
	lock, err := client.Acquire(runLockName, pglock.FailIfLocked())
	require.NoError(t, err)
	ctxLock, cancelLock := context.WithTimeout(t.Context(), time.Second)
	err = Run(ctxLock, cfg, slog.New(slog.DiscardHandler), new(slog.LevelVar), assembler, sandbox.None{})

	cancelLock()
	require.ErrorIs(t, err, pglock.ErrNotAcquired)
	require.Empty(t, assembler.AssembleCalls())
	require.NoError(t, lock.Close())
	require.NoError(t, seed.Stop())

	require.ErrorIs(t, Run(ctx, cfg, slog.New(slog.DiscardHandler), new(slog.LevelVar), assembler, sandbox.None{}), ErrRestartRequested)
	require.Equal(t, []string{"validated", "validated", "assembled", "slack stopped", "extra stopped"}, order)

	for _, resource := range resources {
		_, err := resource.Stat()
		require.ErrorIs(t, err, os.ErrClosed)
	}

	require.Len(t, assembler.AssembleCalls(), 1)
	require.ErrorContains(t, assembler.AssembleCalls()[0].Runtime.Sessions.db.PingContext(t.Context()), "database is closed")
}

func TestRunStartsPersistedQueueWithoutOtherWork(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgentFile(t, workspace, "primary", "---\ndescription: Queue recovery\nmodel: gpt-6.1-sol\npermission: {}\n---\nRespond concisely.\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Connection", "close")

		_, errWrite := w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[]}]}]}`))
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
	require.NoError(t, seed.UpsertThread(conversationID, ThreadState{Agent: "primary"}))
	boundary, err := seed.AppendEntryID(t.Context(), conversationID, testSessionEntry("recorded", "answer"))
	require.NoError(t, err)
	_, _, err = stageRevertDB(t.Context(), seed.db, conversationID, fmt.Sprintf("%d:0", boundary), protocol.RevertBefore)
	require.NoError(t, err)
	require.NoError(t, seed.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "first", Principal: "alice", Source: protocol.SourceWeb, Position: 0, StashAt: time.Unix(1, 0).UTC()}))
	require.NoError(t, seed.PutThreadQueueItem("q2", &protocol.ThreadQueueItem{ID: "q2", ConversationID: conversationID, Message: "second", Principal: "alice", Source: protocol.SourceWeb, Position: 1, StashAt: time.Unix(2, 0).UTC()}))
	require.NoError(t, seed.Stop())

	cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}, Slack: testSlack()}

	runQueuedStartup := func(t *testing.T, wait time.Duration) []string {
		t.Helper()

		finals := make(chan string, 4)
		copyDone := make(chan struct{})
		assembler := &frontendAssemblerMock{
			GateFunc:           passGate,
			ValidateAssetsFunc: func(*config.Config, string) error { return nil },
			AssembleFunc: func(rt *Runtime) (<-chan struct{}, []func(context.Context) error, error) {
				events := rt.Subscribe(rt.RunCtx)
				go func() {
					for event := range events {
						if event.Message.Complete && strings.TrimSpace(event.Message.Text) != "" {
							finals <- event.Message.Text
						}

						event.Acknowledgement <- nil
					}
				}()

				return copyDone, nil, nil
			},
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		errRun := make(chan error, 1)
		go func() {
			errRun <- Run(ctx, cfg, slog.New(slog.DiscardHandler), new(slog.LevelVar), assembler, sandbox.None{})
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

	require.Equal(t, []string{"answer", "answer"}, runQueuedStartup(t, 20*time.Second), "daemon startup runs a queue kept through a move")
	seed, err = NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	marker, _, _, err := seed.RevertState(t.Context(), conversationID)
	require.NoError(t, err)
	require.Empty(t, marker, "the first queued turn continued from the current point")
	texts := observedEntryTexts(t, seed, conversationID)
	require.Len(t, texts, 4)
	require.NotContains(t, texts, "message:recorded", "the queue runs from the point before the recorded entry")
	require.NoError(t, seed.Stop())
	require.Empty(t, runQueuedStartup(t, time.Second))
}

func TestConfigureInstrumentationExportsToPostHog(t *testing.T) {
	var mu sync.Mutex

	authorizations := map[string]string{}
	collector := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		authorizations[r.URL.Path] = r.Header.Get("Authorization")
	}))
	t.Cleanup(collector.Close)

	stop, err := configureInstrumentation(t.Context(), config.PostHogConfig{ProjectToken: "phc_test", Host: collector.URL})
	require.NoError(t, err)

	_, span := otel.Tracer("test").Start(t.Context(), "turn")
	span.End()
	require.NoError(t, stop(t.Context()))

	mu.Lock()
	assert.Equal(t, map[string]string{"/i/v0/ai/otel": "Bearer phc_test", "/i/v1/traces": "Bearer phc_test"}, authorizations)
	mu.Unlock()
}

func TestKeyedConversationLocksSerializeOneIDAndReleaseEntries(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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

func TestRunMergesStoredBehaviorSettings(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	store, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop()) })

	type started struct {
		live  *config.Config
		level slog.Level
		logs  string
	}

	start := func(behavior func(*config.Config)) (started, error) {
		t.Helper()

		var (
			logs bytes.Buffer
			live *config.Config
		)

		cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn}
		behavior(cfg)
		cfg.Slack.BotToken, cfg.Slack.AppToken = "xoxb-test", "xapp-test"

		level, copyDone := new(slog.LevelVar), make(chan struct{})
		close(copyDone)

		err := Run(t.Context(), cfg, slog.New(slog.NewTextHandler(&logs, nil)), level, &frontendAssemblerMock{
			GateFunc:           passGate,
			ValidateAssetsFunc: func(*config.Config, string) error { return nil },
			AssembleFunc: func(rt *Runtime) (<-chan struct{}, []func(context.Context) error, error) {
				live = rt.Cfg.Clone()
				return copyDone, nil, nil
			},
		}, sandbox.None{})

		return started{live, level.Level(), logs.String()}, err
	}
	stored := func() (config.Behavior, bool) {
		t.Helper()

		behavior, found, err := store.behaviorSettings(t.Context())
		require.NoError(t, err)

		return behavior, found
	}
	setupOnly := func(*config.Config) {}

	_, err = start(setupOnly)
	require.ErrorContains(t, err, "slack.channels is required")

	_, found := stored()
	require.False(t, found, "a fresh install needs behavior from the file or the database")

	_, err = start(func(cfg *config.Config) {
		cfg.Slack.Channels = []config.SlackChannelConfig{{Channel: "#ops"}}
	})
	require.ErrorContains(t, err, "slack.channels[].agents is required")

	_, found = stored()
	require.False(t, found, "a failed import stores nothing")

	got, err := start(func(cfg *config.Config) {
		cfg.Slack, cfg.AutoApproverModel, cfg.Logging.Level = testSlack(), "gpt-a", "info"
	})
	require.NoError(t, err)
	require.Equal(t, "gpt-a", got.live.AutoApproverModel)
	require.Equal(t, slog.LevelInfo, got.level)
	require.Equal(t, 1, strings.Count(got.logs, `level=WARN msg="slack channel agent is not sticky: slack" channel=@ agent=assistant`+"\n"), "a channel whose agent is not sticky: slack starts with one warning: %s", got.logs)

	imported, found := stored()
	require.True(t, found)
	require.Equal(t, got.live.Behavior(), imported, "the first start stores the normalized file behavior")

	got, err = start(func(cfg *config.Config) {
		cfg.Slack, cfg.AutoApproverModel, cfg.Logging.Level = testSlack(), "gpt-b", "error"
	})
	require.NoError(t, err)
	require.Equal(t, "gpt-a", got.live.AutoApproverModel, "the stored auto_approver_model applies")
	require.Equal(t, slog.LevelInfo, got.level, "the stored logging.level replaces the file's")
	require.Contains(t, got.logs, `level=WARN msg="ignoring behavior settings in the config file; the stored behavior settings apply" fields="[auto_approver_model slack.channels logging.level]"`)

	_, err = store.db.ExecContext(t.Context(), `UPDATE behavior_settings SET document = document || '{"server_display_name": "stored"}'::jsonb`)
	require.NoError(t, err)

	got, err = start(func(cfg *config.Config) { cfg.ServerDisplayName = "file" })
	require.NoError(t, err)
	require.Equal(t, imported.Slack.Channels, got.live.Slack.Channels, "a setup-only file serves the stored Slack channels")
	require.Equal(t, "file", got.live.ServerDisplayName, "server_display_name comes only from the file")
	require.NotContains(t, got.logs, "ignoring behavior settings")

	invalid := imported
	invalid.Slack.Channels = []config.SlackChannelConfig{{Channel: "#ops"}}
	require.NoError(t, putBehaviorSettings(t.Context(), store.db, &invalid))

	_, err = start(setupOnly)
	require.ErrorContains(t, err, "stored behavior settings: validate behavior settings: slack.channels[].agents is required")

	unreachable := imported
	unreachable.Overlays = []string{filepath.Join(workspace, "missing-overlay") + "@main"}
	require.NoError(t, putBehaviorSettings(t.Context(), store.db, &unreachable))

	_, err = start(setupOnly)
	require.ErrorContains(t, err, `apply configured rocketclaw overlay "`+unreachable.Overlays[0]+`": git fetch`)
	require.NotContains(t, err.Error(), "stored behavior settings:", "a failed overlay download is not a stored settings problem")

	_, err = store.db.ExecContext(t.Context(), `UPDATE behavior_settings SET document = '{"slack": []}'::jsonb`)
	require.NoError(t, err)

	_, err = start(setupOnly)
	require.ErrorContains(t, err, "parse behavior settings")
}

func testSlack() config.SlackConfig {
	return config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "@", Agents: []string{"assistant"}, AllowedUserIDs: []string{"U1"}}}}
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestShutdownKeepsRunLockAliveDuringBash(t *testing.T) {
	t.Parallel()

	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background=%v", background), func(t *testing.T) {
			workspace := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, "agents"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "waiter.md"), []byte("---\ndescription: Waiter\nmodel: gpt-6.1-sol\nsticky: slack\npermission:\n  bash:\n    \"*\": allow\n  rocketclaw:\n    allow_background: allow\n---\nPrompt\n"), 0o600))
			started, finished, pid := filepath.Join(workspace, "started"), filepath.Join(workspace, "done"), filepath.Join(workspace, "pid")
			sleep := map[bool]string{false: "2", true: "60"}[background]
			waiting, ended := filepath.Join(workspace, "waiting"), filepath.Join(workspace, "ended")
			code := "def main():\n    return bash(command=r'''echo $$ > " + pid + " && touch " + started + " && sleep " + sleep + " && touch " + finished + "''')\n"

			if background {
				code = strings.Replace(code, "    return", "    bash(command=r'''touch "+waiting+" && until [ -f "+ended+" ]; do sleep 0.01; done''')\n    return", 1)
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if answerTitleRequest(w, r) {
					return
				}

				body, _ := io.ReadAll(r.Body)
				arguments, err := json.Marshal(struct {
					Code        string `json:"code"`
					Description string `json:"description"`
					Background  bool   `json:"background"`
				}{code, "Wait", background})
				assert.NoError(t, err)

				output := fmt.Sprintf(`{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_b","name":"execute","arguments":%q}`, arguments)

				if strings.Contains(string(body), "function_call_output") {
					output = `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[]}]}`
				}

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[` + output + `]}`))
			}))
			t.Cleanup(server.Close)

			dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
			require.NoError(t, err)

			cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}, Slack: testSlack()}

			ctx, signal := context.WithCancel(t.Context())
			defer signal()

			runtimes := make(chan *Runtime, 1)

			stops := 0
			assembler := &frontendAssemblerMock{
				GateFunc:           passGate,
				ValidateAssetsFunc: func(*config.Config, string) error { return nil },
				AssembleFunc: func(rt *Runtime) (<-chan struct{}, []func(context.Context) error, error) {
					runtimes <- rt

					msg := protocol.NewInboundMessageFromContent(protocol.SourceSlack, protocol.InboundKindPrompt, &protocol.InboundContent{Text: "wait for it"}, true)
					msg.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.0", ThreadTS: "111.0"}
					_, err := rt.TextRouter.SubmitMention(rt.RunCtx, "waiter", protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.0"}, msg)
					require.NoError(t, err)

					return nil, []func(context.Context) error{func(context.Context) error {
						stops++

						if background {
							assert.NoFileExists(t, finished, "background bash is killed")
						} else {
							assert.FileExists(t, finished, "frontends stop only after bash finishes")
						}

						return nil
					}}, nil
				},
			}

			var logs bytes.Buffer

			done := make(chan error, 1)
			go func() {
				done <- Run(ctx, cfg, slog.New(slog.NewTextHandler(&logs, nil)), new(slog.LevelVar), assembler, sandbox.None{})
			}()

			var rt *Runtime

			select {
			case rt = <-runtimes:
			case err := <-done:
				t.Fatalf("run ended before assembling frontends: %v", err)
			}

			if background {
				require.Eventually(t, func() bool {
					_, err := os.Stat(waiting)
					return err == nil
				}, 10*time.Second, 10*time.Millisecond)
				require.Eventually(t, func() bool {
					active, err := rt.Sessions.HasActiveTurn(t.Context(), "slack-channel:C123:waiter")
					return err == nil && !active
				}, 10*time.Second, 10*time.Millisecond)
				require.NoError(t, os.WriteFile(ended, nil, 0o600))
			}

			require.Eventually(t, func() bool {
				_, err := os.Stat(started)
				return err == nil
			}, 10*time.Second, 10*time.Millisecond, "a background script's tools keep working after its turn ends")
			signal()

			lock := rt.RunCtx

			for !background {
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

			assert.Equal(t, 1, stops)

			if !background {
				assert.Contains(t, logs.String(), `msg="shutdown waiting for running tool calls" component=thread_bridges conversation_id=slack-channel:C123:waiter`)
				return
			}

			data, err := os.ReadFile(pid)
			require.NoError(t, err)
			shell, err := strconv.Atoi(strings.TrimSpace(string(data)))
			require.NoError(t, err)
			require.ErrorIs(t, syscall.Kill(shell, 0), syscall.ESRCH, "shutdown waits for killed background bash")

			store, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Stop()) })

			running, err := store.runningBackgroundJobs(t.Context())
			require.NoError(t, err)
			require.Len(t, running, 1, "the startup step decides how a killed job ends")
			require.Equal(t, "Wait", running[0].label)

			ctx, signal = context.WithCancel(t.Context())
			defer signal()

			go func() {
				done <- Run(ctx, cfg, slog.New(slog.DiscardHandler), new(slog.LevelVar), &frontendAssemblerMock{
					GateFunc:           passGate,
					ValidateAssetsFunc: func(*config.Config, string) error { return nil },
					AssembleFunc: func(*Runtime) (<-chan struct{}, []func(context.Context) error, error) {
						return nil, nil, nil
					},
				}, sandbox.None{})
			}()

			require.Eventually(t, func() bool {
				jobs, err := store.backgroundJobs(t.Context(), "slack-channel:C123:waiter")
				return err == nil && len(jobs) == 1 && jobs[0].status == backgroundKilled && jobs[0].noteState == notePending && !jobs[0].wake
			}, 10*time.Second, 10*time.Millisecond, "the next start reports the script killed, for the next turn")
			signal()
			require.NoError(t, <-done)
		})
	}
}

func TestRunStopsOnInvalidFrontendAssets(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	errInvalid := errors.New("invalid cron")
	assembler := &frontendAssemblerMock{GateFunc: passGate, ValidateAssetsFunc: func(*config.Config, string) error { return errInvalid }}
	require.ErrorIs(t, Run(t.Context(), &config.Config{Workspace: workspace, DatabaseURL: dsn, Slack: testSlack()}, slog.New(slog.DiscardHandler), new(slog.LevelVar), assembler, sandbox.None{}), errInvalid)
	assert.Empty(t, assembler.AssembleCalls())
}

func TestRunTakesTheLockThenGatesThenMigrates(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	db, err := openSessionDB(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	migrated := func() bool {
		var ledger *string
		require.NoError(t, db.QueryRowContext(t.Context(), `SELECT to_regclass('pg_migrations')::text`).Scan(&ledger))

		return ledger != nil
	}

	client, err := pglock.UnsafeNew(db, pglock.WithCustomTable(runLockTable), pglock.WithHeartbeatFrequency(0))
	require.NoError(t, err)
	require.NoError(t, client.TryCreateTable())
	lock, err := client.Acquire(runLockName, pglock.FailIfLocked())
	require.NoError(t, err)

	errGate := errors.New("the Web node needs a login")
	errInvalid := errors.New("invalid cron")
	gate := errGate
	assembler := &frontendAssemblerMock{
		GateFunc: func(context.Context, *config.Config, *slog.Logger) (func() error, error) {
			require.False(t, migrated(), "the gate runs before migrations")
			return func() error { return nil }, gate
		},
		ValidateAssetsFunc: func(*config.Config, string) error { return errInvalid },
	}
	cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn, Slack: testSlack()}

	ctxWait, cancelWait := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancelWait)
	require.ErrorIs(t, Run(ctxWait, cfg, slog.New(slog.DiscardHandler), new(slog.LevelVar), assembler, sandbox.None{}), pglock.ErrNotAcquired)
	require.Empty(t, assembler.GateCalls(), "a process waiting for the run lock does not start its node")
	require.False(t, migrated(), "a process waiting for the run lock does not migrate")

	require.NoError(t, lock.Close())
	require.ErrorIs(t, Run(t.Context(), cfg, slog.New(slog.DiscardHandler), new(slog.LevelVar), assembler, sandbox.None{}), errGate)
	require.False(t, migrated(), "a failed gate stops before migrations")

	lock, err = client.Acquire(runLockName, pglock.FailIfLocked())
	require.NoError(t, err, "a failed gate releases the run lock")
	require.NoError(t, lock.Close())

	gate = nil

	require.ErrorIs(t, Run(t.Context(), cfg, slog.New(slog.DiscardHandler), new(slog.LevelVar), assembler, sandbox.None{}), errInvalid)
	require.True(t, migrated(), "a fresh database migrates once the gate passes")
	require.Len(t, assembler.GateCalls(), 2)
}

func TestSlackMentionTurnHasNoQuestionTool(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "primary.md"), []byte("---\ndescription: Primary\nmodel: gpt-6.1-sol\nsticky: slack\npermission: {}\n---\nPrompt\n"), 0o600))

	bodies := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if answerTitleRequest(w, r) {
			return
		}

		body, _ := io.ReadAll(r.Body)
		bodies <- string(body)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{Workspace: workspace, DatabaseURL: cloneMigratedTestDatabase(t), OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}, Slack: testSlack()}

	ctx, signal := context.WithCancel(t.Context())

	var startupMu sync.Mutex

	lastStep := "Slack mention submission not started"

	assembler := &frontendAssemblerMock{
		GateFunc:           passGate,
		ValidateAssetsFunc: func(*config.Config, string) error { return nil },
		AssembleFunc: func(rt *Runtime) (<-chan struct{}, []func(context.Context) error, error) {
			msg := protocol.NewInboundMessageFromContent(protocol.SourceSlack, protocol.InboundKindPrompt, &protocol.InboundContent{Text: "first message"}, true)
			msg.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.0", ThreadTS: "111.0"}

			startupMu.Lock()
			lastStep = "submitting the Slack mention"
			startupMu.Unlock()

			accepted, err := rt.TextRouter.SubmitMention(rt.RunCtx, "primary", protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.0"}, msg)

			startupMu.Lock()
			lastStep = fmt.Sprintf("SubmitMention returned accepted=%t, error=%v", accepted, err)
			startupMu.Unlock()

			if err != nil {
				return nil, nil, fmt.Errorf("submit startup message: %w", err)
			}

			assert.True(t, accepted, "the startup Slack mention must be admitted")

			return nil, nil, nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfg, slog.New(slog.NewTextHandler(t.Output(), nil)), new(slog.LevelVar), assembler, sandbox.None{})

		close(done)
	}()

	t.Cleanup(func() {
		signal()
		require.NoError(t, <-done)
	})

	select {
	case body := <-bodies:
		assert.Contains(t, body, "- rocketclaw_attach_files_to_response(", "platform tools are listed inside execute")
		assert.NotContains(t, body, "- ask_user_question(")
	case err := <-done:
		t.Fatalf("Run returned before the startup message ran: %v", err)
	case <-time.After(10 * time.Second):
		startupMu.Lock()
		step := lastStep
		startupMu.Unlock()
		t.Fatalf("timed out waiting for the fake model request; last observed step: %s", step)
	}

	assert.Len(t, assembler.AssembleCalls(), 1)
}

func TestPruneRocketcodeTempRemovesEntriesOlderThanCutoff(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.MkdirAll("outside", 0o700))
	require.NoError(t, root.WriteFile("outside/keep.txt", []byte("keep"), 0o600))
	require.NoError(t, root.MkdirAll("runtime/.rocketcode/tmp/old-conversation", 0o700))
	require.NoError(t, root.MkdirAll("runtime/.rocketcode/tmp/fresh-conversation", 0o700))
	require.NoError(t, root.MkdirAll("runtime/.rocketcode/shell-outputs", 0o700))
	require.NoError(t, root.MkdirAll("empty/.rocketcode", 0o700))
	require.NoError(t, root.WriteFile("runtime/.rocketcode/tmp/old-conversation/out.txt", []byte("old"), 0o600))
	require.NoError(t, root.Symlink(filepath.Join(workspace, "outside"), "runtime/.rocketcode/tmp/old-conversation/escape"))
	require.NoError(t, root.Symlink(filepath.Join(workspace, "outside"), "runtime/.rocketcode/tmp/fresh-link"))
	require.NoError(t, root.WriteFile("runtime/.rocketcode/shell-outputs/rocketcode-bash-1", []byte("old"), 0o600))
	require.NoError(t, root.WriteFile("runtime/.rocketcode/shell-outputs/rocketcode-bash-2", []byte("fresh"), 0o600))

	old := time.Now().Add(-stateRetention - time.Hour)
	require.NoError(t, root.Chtimes("runtime/.rocketcode/tmp/old-conversation", old, old))
	require.NoError(t, root.Chtimes("runtime/.rocketcode/shell-outputs/rocketcode-bash-1", old, old))

	cutoff := time.Now().Add(-stateRetention)

	removed, err := pruneRocketcodeTemp(t.Context(), filepath.Join(workspace, "runtime", ".rocketcode"), cutoff)
	require.NoError(t, err)
	assert.Equal(t, 2, removed)

	for name, kept := range map[string]bool{
		"runtime/.rocketcode/tmp/old-conversation":            false,
		"runtime/.rocketcode/shell-outputs/rocketcode-bash-1": false,
		"runtime/.rocketcode/tmp/fresh-conversation":          true,
		"runtime/.rocketcode/tmp/fresh-link":                  true,
		"runtime/.rocketcode/shell-outputs/rocketcode-bash-2": true,
		"outside/keep.txt": true,
	} {
		_, err := root.Lstat(name)
		assert.Equal(t, kept, err == nil, name)
	}

	for _, dir := range []string{"empty/.rocketcode", "missing/.rocketcode"} {
		removed, err := pruneRocketcodeTemp(t.Context(), filepath.Join(workspace, dir), cutoff)
		require.NoError(t, err, dir)
		assert.Zero(t, removed, dir)
	}
}

func passGate(context.Context, *config.Config, *slog.Logger) (func() error, error) {
	return func() error { return nil }, nil
}
