package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestShadowReviewDefinitionIsolation(t *testing.T) {
	for _, failure := range []string{"new malformed", "bound malformed", "bound missing"} {
		t.Run(failure, func(t *testing.T) {
			var logs bytes.Buffer

			logger := slog.Default()

			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(logger) })
			store, root := newShadowTestStore(t)
			require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o700))

			for _, item := range []struct{ name, model string }{{"main", "gpt-parent"}, {"main/a", "gpt-broken"}, {"main/z", "gpt-good"}, {"other", "gpt-other"}} {
				require.NoError(t, root.WriteFile(".rocketclaw/agents/"+item.name+".md", fmt.Appendf(nil, "---\nmodel: %s\npermission: {}\n---\nSystem %s", item.model, item.name), 0o600))
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				writeRawRunMessage(t, w, "response", "message", "answer "+request.Model)
			}))
			t.Cleanup(server.Close)
			cfg := config.NewLockedConfig(&config.Config{Workspace: root.Name(), OpenAI: config.OpenAIConfig{APIBaseURL: server.URL, APIKey: "test"}})
			store.definitions = cfg

			runtime := &Runtime{Sessions: store}
			if failure != "new malformed" {
				require.NoError(t, runtime.CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}))
			}

			if failure == "bound missing" {
				require.NoError(t, root.Remove(".rocketclaw/agents/main/a.md"))
			} else {
				require.NoError(t, root.WriteFile(".rocketclaw/agents/main/a.md", []byte("---\nmodel: [\n---\nBroken"), 0o600))
			}

			require.NoError(t, runtime.CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}))
			require.NoError(t, runtime.CreateConversation(t.Context(), protocol.Conversation{ID: "unrelated", Agent: "other"}))

			agents, _, err := LoadRuntimeDefinitions(cfg, config.DefaultRuntimeDir)
			require.NoError(t, err, "shared execution loader must retain healthy definitions")
			require.Contains(t, agents.Items, "main")
			require.Contains(t, agents.Items, "main/z")
			require.Contains(t, agents.Items, "other")
			require.NotContains(t, agents.Items, "main/a")

			if failure != "bound missing" {
				require.Contains(t, logs.String(), "main/a.md", "operators must see the malformed file")
			}

			finals := make(chan *protocol.OutboundMessage, 8)
			manager, shutdown := newTestBridgeManager(t, cfg, store, finals)
			t.Cleanup(func() { require.NoError(t, shutdown()) })

			for _, id := range []string{"parent", "unrelated"} {
				bridge, err := manager.recordedBridge(t.Context(), id)
				require.NoError(t, err)
				require.NoError(t, bridge.Submit(t.Context(), protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "same", true)))

				want := "answer gpt-parent"
				if id == "unrelated" {
					want = "answer gpt-other"
				}

				require.Equal(t, want, readFinal(t, finals).Text)
			}

			var good string
			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT conversation_id FROM conversations WHERE shadow_agent = 'main/z'`).Scan(&good))
			require.Eventually(t, func() bool {
				entries, err := store.ObserveEntries(t.Context(), good)
				return err == nil && len(entries) == 1
			}, 3*time.Second, time.Millisecond)
			entries, err := store.ObserveEntries(t.Context(), good)
			require.NoError(t, err)
			require.Contains(t, string(entries[0].Entry.ReplayInput[len(entries[0].Entry.ReplayInput)-1]), "answer gpt-good")

			if failure != "new malformed" {
				var broken string
				require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT conversation_id FROM conversations WHERE shadow_agent = 'main/a'`).Scan(&broken))
				// Existing durable work must fail locally, never run a default agent.
				inbound, err := json.Marshal(protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "saved input", true))
				require.NoError(t, err)
				_, err = store.db.ExecContext(t.Context(), `INSERT INTO shadow_turns (child_conversation_id, parent_source_conversation_id, parent_turn_id, inbound_json) VALUES ($1, 'parent', 'bound-work', $2)`, broken, inbound)
				require.NoError(t, err)
				require.NoError(t, store.captureTurnInputs(t.Context(), "parent", "bound-work", &turnInputSnapshot{Inputs: []harness.PromptInput{{ID: "saved", Texts: []string{"saved input"}}}, Closed: true}))
				require.NoError(t, manager.PickLaterWork(t.Context(), broken))
				require.Eventually(t, func() bool {
					var failed bool

					err := store.db.QueryRowContext(t.Context(), `SELECT count(*) > 0 FROM turns WHERE conversation_id = $1 AND terminal = $2`, broken, protocol.TerminalFailed).Scan(&failed)

					return err == nil && failed
				}, 3*time.Second, time.Millisecond)

				var message string
				require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT outbound_json->>'Text' FROM turns WHERE conversation_id = $1 AND terminal = $2`, broken, protocol.TerminalFailed).Scan(&message))
				require.Contains(t, message, `missing required default agent "main/a"`)
			}

			require.Empty(t, finals, "child success and definition errors stay local")
			require.NoError(t, root.WriteFile(".rocketclaw/agents/other.md", []byte("---\nmodel: [\n---\nBroken primary"), 0o600))

			_, _, err = LoadRuntimeDefinitions(cfg, config.DefaultRuntimeDir)
			require.ErrorContains(t, err, "other.md: parse YAML frontmatter", "ordinary malformed primary behavior is unchanged")
		})
	}
}

func TestShadowReviewOwnershipReadIsolation(t *testing.T) {
	store, root := newShadowTestStore(t)
	cfg := config.NewLockedConfig(&config.Config{Workspace: root.Name()})
	constructed := 0

	manager := newThreadBridgeManager(cfg, store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(bridgeCfg *Config) directBridge {
		constructed++
		bridgeCfg.SessionService = store

		return NewConversation(cfg, discardPublisher{}, bridgeCfg, store.log)
	}})
	for _, id := range []string{"existing", "blocked"} {
		require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "main"}))
	}

	existing, err := manager.recordedBridge(t.Context(), "existing")
	require.NoError(t, err)
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(t.Context(), `LOCK TABLE conversations IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var group errgroup.Group

	done := make(chan error, 1)

	group.Go(func() error {
		_, err := manager.ensureThreadBridge(ctx, "blocked", ThreadState{Agent: "main"})
		done <- err

		return nil
	})

	defer func() { _ = tx.Rollback(); require.NoError(t, group.Wait()) }()

	require.Eventually(t, func() bool {
		var blocked bool

		err := store.db.QueryRowContext(t.Context(), `SELECT count(*) > 0 FROM pg_locks WHERE relation = 'conversations'::regclass AND NOT granted AND mode = 'AccessShareLock'`).Scan(&blocked)

		return err == nil && blocked
	}, 3*time.Second, time.Millisecond)

	if !manager.mu.TryLock() {
		t.Fatal("blocked ownership read holds the global worker lock")
	}
	manager.mu.Unlock()
	lookup, err := manager.ensureThreadBridge(t.Context(), "existing", ThreadState{Agent: "main"})
	require.NoError(t, err)
	require.Same(t, existing, lookup)
	cancel()

	select {
	case err := <-done:
		require.ErrorContains(t, err, "load worker ownership")
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("ownership read did not honor caller cancellation")
	}

	manager.mu.Lock()
	require.NotContains(t, manager.bridges, "blocked")
	require.NotContains(t, manager.pending, "blocked")
	manager.mu.Unlock()
	require.Equal(t, 1, constructed, "a failed ownership read cannot construct a worker")

	// The lock transaction occupies the entire pool; an existing worker needs no I/O.
	maxConnections := store.db.Stats().MaxOpenConnections
	store.db.SetMaxOpenConns(1)

	lookup, err = manager.ensureThreadBridge(ctx, "existing", ThreadState{Agent: "main"})
	require.NoError(t, err)
	require.Same(t, existing, lookup)
	store.db.SetMaxOpenConns(maxConnections)

	results := make(chan directBridge, 2)

	for range 2 {
		group.Go(func() error {
			bridge, err := manager.ensureThreadBridge(t.Context(), "blocked", ThreadState{Agent: "main"})
			if err == nil {
				results <- bridge
			}

			return err
		})
	}

	require.Eventually(t, func() bool {
		var blocked int

		err := store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_locks WHERE relation = 'conversations'::regclass AND NOT granted AND mode = 'AccessShareLock'`).Scan(&blocked)

		return err == nil && blocked == 2
	}, 3*time.Second, time.Millisecond)
	require.NoError(t, tx.Rollback())
	require.NoError(t, group.Wait())

	first, second := <-results, <-results
	require.Same(t, first, second, "concurrent callers share one worker")
	require.Equal(t, 2, constructed, "the second lookup must recheck before construction")
}
