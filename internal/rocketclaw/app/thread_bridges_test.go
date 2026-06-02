package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunReportsPendingRestartNotificationStartupErrors(t *testing.T) {
	workspace := t.TempDir()
	service, err := harnessbridge.NewSessionService(workspace)
	require.NoError(t, err)
	require.NoError(t, service.Stop(context.Background()))

	db, err := sql.Open("sqlite", harnessbridge.SessionDBPath(workspace))
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `INSERT INTO session_meta (key, value) VALUES (?, ?)`, "rocketclaw_state", "not-json")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	err = Run(context.Background(), &config.Config{Workspace: workspace}, "", slog.New(slog.DiscardHandler))
	require.ErrorContains(t, err, "apply pending restart notifications")
}

func TestRunStopsWebUIOnlyRuntimeWhenContextCanceled(t *testing.T) {
	workspace := t.TempDir()
	urlCh := make(chan string, 1)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- Run(ctx, &config.Config{
			Workspace: workspace,
			WebUI: config.WebUIConfig{
				Enabled:    true,
				ListenAddr: "127.0.0.1:0",
			},
		}, "", slog.New(webUIURLHandler{urlCh: urlCh}))
	}()

	var webURL string
	select {
	case webURL = <-urlCh:
	case err := <-done:
		require.NoError(t, err)
		t.Fatal("Run stopped before web UI startup")
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not report web UI URL")
	}

	certPEM, err := os.ReadFile(filepath.Join(workspace, ".rocketclaw", "web-ui.crt"))
	require.NoError(t, err)

	certPool := x509.NewCertPool()
	require.True(t, certPool.AppendCertsFromPEM(certPEM))

	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: certPool}}
	defer transport.CloseIdleConnections()

	client := &http.Client{Transport: transport}

	reqCtx, stopRequest := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopRequest()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, webURL, http.NoBody)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

type webUIURLHandler struct {
	urlCh chan string
}

func (h webUIURLHandler) Enabled(context.Context, slog.Level) bool { return true }

//nolint:gocritic // slog.Handler requires slog.Record by value.
func (h webUIURLHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "web UI voice mode available" {
		return nil
	}

	r.Attrs(func(a slog.Attr) bool {
		if a.Key != "url" {
			return true
		}

		select {
		case h.urlCh <- a.Value.String():
		default:
		}

		return false
	})

	return nil
}

func (h webUIURLHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h webUIURLHandler) WithGroup(string) slog.Handler { return h }

func TestThreadBridgeManagerCreatesSeparateBridgesPerThreadAndPersistsThem(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionService(t, workspace)
	created := make([]bridgeConfig, 0, 2)
	manager := newThreadBridgeManager(events.New(), store, slog.New(slog.DiscardHandler), func(cfg bridgeConfig) directBridge {
		created = append(created, cfg)
		return new(fakeDirectBridge)
	})

	require.NoError(t, manager.StartThread(t.Context(), "main", true, newThreadInboundMessage("first", "111.222", "111.222")))
	require.NoError(t, manager.StartThread(t.Context(), "factory", true, newThreadInboundMessage("second", "333.444", "333.444")))

	require.Len(t, created, 2)

	state, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, harnessbridge.ThreadState{Agent: "main", SeededFromResponse: ""}, state.Threads[created[0].ConversationID])
}

func TestThreadBridgeManagerStartsPendingScheduledMessageBridges(t *testing.T) {
	workspace := t.TempDir()

	store := newTestSessionService(t, workspace)
	for _, cfg := range []harnessbridge.Config{
		{ConversationID: events.MainConversationID(), Agent: "main", SessionService: store},
		{ConversationID: harnessbridge.SlackThreadConversationID("D123", "111.222"), Agent: "planner", SessionService: store},
		{ConversationID: "external_mcp:ticket-123", Agent: "helper", SessionService: store},
	} {
		bridge := harnessbridge.NewConversation(&config.Config{Workspace: workspace}, nil, &cfg, slog.New(slog.DiscardHandler))
		require.NoError(t, bridge.Start(t.Context()))
		require.NoError(t, bridge.ScheduleMessage(time.Hour, "later", false))
		require.NoError(t, bridge.Stop(context.Background()))
	}

	created := make([]bridgeConfig, 0, 2)
	manager := newThreadBridgeManager(events.New(), store, slog.New(slog.DiscardHandler), func(cfg bridgeConfig) directBridge {
		created = append(created, cfg)
		return new(fakeDirectBridge)
	})

	require.NoError(t, manager.StartPendingScheduledMessages())
	require.Len(t, created, 2)
	assert.ElementsMatch(t, []bridgeConfig{
		{ConversationID: harnessbridge.SlackThreadConversationID("D123", "111.222"), Agent: "planner", OutputTargets: []events.OutputTarget{events.OutputTargetSlackMain}},
		{ConversationID: "external_mcp:ticket-123", Agent: "helper", OutputTargets: events.MainOutputTargets()},
	}, created)
}

func TestThreadBridgeManagerSeedsStartedThreadBeforeSubmit(t *testing.T) {
	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(events.New(), newTestSessionService(t, t.TempDir()), slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })

	require.NoError(t, manager.StartThread(t.Context(), "main", true, newThreadInboundMessage("first", "111.222", "111.222")))

	assert.Equal(t, []string{"seed_main", "submit:first"}, bridge.ops)
}

func TestThreadBridgeManagerStopsStartedThreadWhenSeedFails(t *testing.T) {
	bridge := &fakeDirectBridge{errSeedMain: assert.AnError}
	manager := newThreadBridgeManager(events.New(), newTestSessionService(t, t.TempDir()), slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge {
		return bridge
	})

	err := manager.StartThread(t.Context(), "main", true, newThreadInboundMessage("first", "111.222", "111.222"))
	require.ErrorContains(t, err, "seed Slack thread from main session")
	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, 1, bridge.stops)
	assert.Empty(t, bridge.submits)
}

func TestThreadBridgeManagerStopsStartedThreadWhenPersistFails(t *testing.T) {
	store, err := harnessbridge.NewSessionService(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Stop(context.Background()))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(events.New(), store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge {
		return bridge
	})

	err = manager.StartThread(t.Context(), "main", false, newThreadInboundMessage("first", "111.222", "111.222"))
	require.ErrorContains(t, err, "persist Slack thread bridge")
	assert.Equal(t, 1, bridge.stops)
	assert.Empty(t, bridge.submits)
}

func TestThreadBridgeManagerCanSkipStartedThreadSeed(t *testing.T) {
	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(events.New(), newTestSessionService(t, t.TempDir()), slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })

	require.NoError(t, manager.StartThread(t.Context(), "main", false, newThreadInboundMessage("first", "111.222", "111.222")))

	assert.Equal(t, []string{"submit:first"}, bridge.ops)
}

func TestThreadBridgeManagerRejectsMissingSlackThreadTarget(t *testing.T) {
	manager := newThreadBridgeManager(events.New(), newTestSessionService(t, t.TempDir()), slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return new(fakeDirectBridge) })

	inbound := events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	err := manager.StartThread(t.Context(), "main", false, inbound)
	require.ErrorContains(t, err, "slack reply target is required")

	inbound.SlackReply = &events.SlackReplyTarget{ChannelID: " ", ThreadTS: " "}
	err = manager.StartThread(t.Context(), "main", false, inbound)
	require.ErrorContains(t, err, "slack thread target is required")
}

func TestThreadBridgeManagerSubmitsPersistedThreadReply(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, "factory"))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(events.New(), store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })

	handled, err := manager.PrepareThreadReply(context.Background(), "D123", "111.222")
	require.NoError(t, err)
	assert.True(t, handled)

	inbound := newThreadInboundMessage("follow up", "222.333", "")
	handled, err = manager.SubmitThreadReply(context.Background(), "D123", "111.222", inbound)
	require.NoError(t, err)
	assert.True(t, handled)

	require.Len(t, bridge.submits, 1)
	assert.Equal(t, conversationID, bridge.submits[0].ConversationID)
	assert.Equal(t, "111.222", bridge.submits[0].SlackReply.ThreadTS)
}

func TestThreadBridgeManagerIgnoresUnmanagedThreadTargets(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	store := newTestSessionService(t, t.TempDir())
	created := 0
	manager := newThreadBridgeManager(bus, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge {
		created++

		return new(fakeDirectBridge)
	})

	for _, tt := range []struct {
		name string
		call func() (bool, error)
	}{
		{
			name: "blank thread reply",
			call: func() (bool, error) {
				return manager.SubmitThreadReply(context.Background(), " ", " ", newThreadInboundMessage("reply", "222.333", " "))
			},
		},
		{
			name: "unknown thread reply",
			call: func() (bool, error) {
				return manager.SubmitThreadReply(context.Background(), "D123", "111.222", newThreadInboundMessage("reply", "222.333", "111.222"))
			},
		},
		{
			name: "blank response prepare",
			call: func() (bool, error) {
				return manager.PrepareResponseThreadReply(context.Background(), " ", "111.222")
			},
		},
		{
			name: "blank response submit",
			call: func() (bool, error) {
				return manager.SubmitResponseThreadReply(context.Background(), " ", "111.222", newThreadInboundMessage("reply", "222.333", "111.222"))
			},
		},
		{
			name: "missing response checkpoint",
			call: func() (bool, error) {
				return manager.SubmitResponseThreadReply(context.Background(), "D123", "111.222", newThreadInboundMessage("reply", "222.333", "111.222"))
			},
		},
		{
			name: "blank summarize",
			call: func() (bool, error) {
				return manager.SummarizeThread(context.Background(), " ", " ")
			},
		},
		{
			name: "missing summarize",
			call: func() (bool, error) {
				return manager.SummarizeThread(context.Background(), "D123", "111.222")
			},
		},
		{
			name: "blank prepare",
			call: func() (bool, error) {
				return manager.PrepareThreadReply(context.Background(), " ", " ")
			},
		},
		{
			name: "missing prepare",
			call: func() (bool, error) {
				return manager.PrepareThreadReply(context.Background(), "D123", "111.222")
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handled, err := tt.call()
			require.NoError(t, err)
			assert.False(t, handled)
		})
	}

	err := manager.SubmitExternalMCP(context.Background(), "main", " ", newThreadInboundMessage("reply", "222.333", "111.222"))
	require.ErrorContains(t, err, "slack thread conversation ID is required")
	assert.Zero(t, created)
}

func TestThreadBridgeManagerSummarizeDrainsQueuedReplies(t *testing.T) {
	for _, tc := range []struct {
		name    string
		queued  []string
		outcome summarizeOutcome
	}{
		{name: "success", queued: []string{"first reply", "second reply"}, outcome: summarizeOutcome{text: "summary text", err: nil}},
		{name: "failure", queued: []string{"queued reply"}, outcome: summarizeOutcome{text: "", err: assert.AnError}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			store := newTestSessionService(t, workspace)
			conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
			require.NoError(t, store.UpsertThread(conversationID, "factory"))

			bus := events.New()
			defer bus.Close()

			bridge := new(fakeDirectBridge)
			bridge.summarizeStarted = make(chan struct{}, 1)
			bridge.releaseSummarize = make(chan summarizeOutcome, 1)
			manager := newThreadBridgeManager(bus, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })
			summaryDone := make(chan error, 1)

			go func() { _, err := manager.SummarizeThread(context.Background(), "D123", "111.222"); summaryDone <- err }()

			<-bridge.summarizeStarted

			for i, text := range tc.queued {
				_, err := manager.SubmitThreadReply(context.Background(), "D123", "111.222", newThreadInboundMessage(text, []string{"222.333", "333.444"}[i], "111.222"))
				require.NoError(t, err)
			}

			bridge.releaseSummarize <- tc.outcome

			if tc.outcome.err != nil {
				require.ErrorIs(t, <-summaryDone, tc.outcome.err)
			} else {
				require.NoError(t, <-summaryDone)
			}

			for i, text := range tc.queued {
				assert.Equal(t, text, bridge.submits[i].Text)
			}

			if tc.outcome.err != nil {
				return
			}

			assert.Contains(t, readOneInbound(t, bus).Text, tc.outcome.text)
		})
	}
}

func TestThreadBridgeManagerSummarizeDrainsQueuedRepliesAfterContextCancellation(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionService(t, workspace)
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, "factory"))

	bus := events.New()
	defer bus.Close()

	bridge := new(fakeDirectBridge)
	bridge.summarizeStarted = make(chan struct{}, 1)
	bridge.releaseSummarize = make(chan summarizeOutcome, 1)
	manager := newThreadBridgeManager(bus, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })

	summaryCtx, cancel := context.WithCancel(context.Background())
	summaryDone := make(chan error, 1)

	go func() {
		_, err := manager.SummarizeThread(summaryCtx, "D123", "111.222")
		summaryDone <- err
	}()

	<-bridge.summarizeStarted

	_, err := manager.SubmitThreadReply(context.Background(), "D123", "111.222", newThreadInboundMessage("queued reply", "222.333", "111.222"))
	require.NoError(t, err)

	cancel()

	bridge.releaseSummarize <- summarizeOutcome{text: "summary text", err: nil}

	require.Error(t, <-summaryDone)
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "queued reply", bridge.submits[0].Text)
}

func TestThreadBridgeManagerCanSummarizeRestoredThread(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionService(t, workspace)
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, "factory"))

	bus := events.New()
	defer bus.Close()

	bridge := &fakeDirectBridge{submits: nil, seeds: nil, summarizeStarted: nil, releaseSummarize: make(chan summarizeOutcome, 1), waitStarted: nil, releaseWait: nil}
	manager := newThreadBridgeManager(bus, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })

	bridge.releaseSummarize <- summarizeOutcome{text: "summary text", err: nil}

	handled, err := manager.SummarizeThread(context.Background(), "D123", "111.222")
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Contains(t, readOneInbound(t, bus).Text, "summary text")
}

func TestThreadBridgeManagerSeedsResponseRootedThreadOnce(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionService(t, workspace)
	checkpointKey := harnessbridge.SlackResponseCheckpointKey("D123", "111.222")
	checkpoint := harnessbridge.ResponseCheckpointState{ConversationID: "main", SessionEntryID: 3, ResponseID: "resp-3", Model: "gpt-5.4", AssistantText: "root answer"}
	require.NoError(t, store.UpsertResponseCheckpoint(checkpointKey, checkpoint))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(events.New(), store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })

	handled, err := manager.PrepareResponseThreadReply(context.Background(), "D123", "111.222")
	require.NoError(t, err)
	assert.True(t, handled)

	handled, err = manager.SubmitResponseThreadReply(context.Background(), "D123", "111.222", newThreadInboundMessage("first", "222.333", "111.222"))
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, bridge.seeds, 1)
	assert.Equal(t, events.ResponseCheckpoint{ConversationID: "main", SessionEntryID: 3, ResponseID: "resp-3", Model: "gpt-5.4", AssistantText: "root answer"}, bridge.seeds[0])

	handled, err = manager.SubmitResponseThreadReply(context.Background(), "D123", "111.222", newThreadInboundMessage("second", "333.444", "111.222"))
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, bridge.seeds, 1)
	require.Len(t, bridge.submits, 2)

	state, err := store.Load()
	require.NoError(t, err)

	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	assert.Equal(t, harnessbridge.ThreadState{Agent: "main", SeededFromResponse: checkpointKey}, state.Threads[conversationID])
}

func TestThreadBridgeManagerIgnoresMissingResponseCheckpoint(t *testing.T) {
	manager := newThreadBridgeManager(events.New(), newTestSessionService(t, t.TempDir()), slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return new(fakeDirectBridge) })

	handled, err := manager.PrepareResponseThreadReply(context.Background(), "D123", "111.222")
	require.NoError(t, err)
	assert.False(t, handled)
}

func TestThreadBridgeManagerRejectsResponseThreadSeededFromDifferentCheckpoint(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	checkpointKey := harnessbridge.SlackResponseCheckpointKey("D123", "111.222")
	require.NoError(t, store.UpsertResponseCheckpoint(checkpointKey, harnessbridge.ResponseCheckpointState{ConversationID: "main", SessionEntryID: 3, ResponseID: "resp-3", Model: "gpt-5.5", AssistantText: "answer"}))

	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, "main"))
	require.NoError(t, store.MarkThreadSeeded(conversationID, harnessbridge.SlackResponseCheckpointKey("D123", "000.111")))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(events.New(), store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })

	handled, err := manager.SubmitResponseThreadReply(context.Background(), "D123", "111.222", newThreadInboundMessage("follow up", "222.333", "111.222"))
	require.ErrorContains(t, err, "slack thread already seeded")
	assert.True(t, handled)
	assert.Empty(t, bridge.seeds)
	assert.Empty(t, bridge.submits)
}

func TestThreadBridgeManagerRoutesExternalMCPThreadAlias(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionService(t, workspace)
	threadKey := harnessbridge.SlackThreadConversationID("D123", "111.222")
	conversationID := "external_mcp:planner:abc"

	require.NoError(t, store.UpsertThread(threadKey, "planner"))
	require.NoError(t, store.MarkThreadSeeded(threadKey, conversationID))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(events.New(), store, slog.New(slog.DiscardHandler), func(cfg bridgeConfig) directBridge {
		assert.Equal(t, bridgeConfig{ConversationID: conversationID, Agent: "planner", OutputTargets: events.MainOutputTargets()}, cfg)

		return bridge
	})

	handled, err := manager.PrepareThreadReply(context.Background(), "D123", "111.222")
	require.NoError(t, err)
	assert.True(t, handled)

	inbound := newThreadInboundMessage("follow up", "222.333", "111.222")
	handled, err = manager.SubmitThreadReply(context.Background(), "D123", "111.222", inbound)
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "follow up", bridge.submits[0].Text)
}

func TestThreadBridgeManagerKeepsExternalMCPBridgeAfterRequestContextEnds(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionService(t, workspace)
	threadKey := harnessbridge.SlackThreadConversationID("D123", "111.222")
	conversationID := "external_mcp:planner:abc"

	require.NoError(t, store.UpsertThread(threadKey, "planner"))
	require.NoError(t, store.MarkThreadSeeded(threadKey, conversationID))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(events.New(), store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })

	requestCtx, cancel := context.WithCancel(context.Background())
	require.NoError(t, manager.SubmitExternalMCP(requestCtx, "planner", conversationID, newThreadInboundMessage("initial", "123.456", "111.222")))
	cancel()

	handled, err := manager.SubmitThreadReply(context.Background(), "D123", "111.222", newThreadInboundMessage("follow up", "222.333", "111.222"))
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, bridge.submits, 2)
	assert.Equal(t, "follow up", bridge.submits[1].Text)
}

func TestThreadBridgeManagerRecordsResponseCheckpoint(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	manager := newThreadBridgeManager(events.New(), store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return new(fakeDirectBridge) })

	err := manager.RecordResponseCheckpoint(context.Background(), "D123", "111.222", events.ResponseCheckpoint{ConversationID: "main", SessionEntryID: 7, ResponseID: "resp", Model: "gpt-5.5", AssistantText: "answer"})
	require.NoError(t, err)
	require.NoError(t, manager.RecordResponseCheckpoint(context.Background(), "", "111.222", events.ResponseCheckpoint{}))

	state, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, harnessbridge.ResponseCheckpointState{ConversationID: "main", SessionEntryID: 7, ResponseID: "resp", Model: "gpt-5.5", AssistantText: "answer"}, state.ResponseCheckpoints[harnessbridge.SlackResponseCheckpointKey("D123", "111.222")])
}

func TestThreadBridgeManagerWaitIdleWaitsForActiveBridges(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionService(t, workspace)
	bridge := &fakeDirectBridge{submits: nil, seeds: nil, summarizeStarted: nil, releaseSummarize: nil, waitStarted: make(chan struct{}, 1), releaseWait: make(chan error, 1)}
	manager := newThreadBridgeManager(events.New(), store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })
	require.NoError(t, manager.StartThread(context.Background(), "main", true, newThreadInboundMessage("start", "111.222", "111.222")))

	waitDone := make(chan error, 1)

	go func() { waitDone <- manager.WaitIdle(context.Background()) }()

	<-bridge.waitStarted

	select {
	case err := <-waitDone:
		t.Fatalf("WaitIdle returned before bridge idle: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	bridge.releaseWait <- nil

	require.NoError(t, <-waitDone)
}

func TestThreadBridgeManagerStopStopsActiveBridges(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	bridges := make([]*fakeDirectBridge, 0, 2)
	manager := newThreadBridgeManager(events.New(), store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge {
		bridge := new(fakeDirectBridge)
		bridges = append(bridges, bridge)

		return bridge
	})

	require.NoError(t, manager.StartThread(context.Background(), "main", false, newThreadInboundMessage("first", "111.222", "111.222")))
	require.NoError(t, manager.StartThread(context.Background(), "main", false, newThreadInboundMessage("second", "333.444", "333.444")))
	require.NoError(t, manager.Stop(context.Background()))

	require.Len(t, bridges, 2)
	assert.Equal(t, 1, bridges[0].stops)
	assert.Equal(t, 1, bridges[1].stops)
}

func TestThreadBridgeManagerStopAcceptingRejectsNewSubmissions(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionService(t, workspace)
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, "main"))

	checkpointKey := harnessbridge.SlackResponseCheckpointKey("D123", "111.222")
	require.NoError(t, store.UpsertResponseCheckpoint(checkpointKey, harnessbridge.ResponseCheckpointState{ConversationID: "main", SessionEntryID: 9, ResponseID: "resp", Model: "gpt-5.5", AssistantText: "answer"}))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(events.New(), store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })
	require.NoError(t, manager.StartThread(context.Background(), "main", false, newThreadInboundMessage("start", "111.222", "111.222")))
	manager.StopAccepting()

	err := manager.StartThread(context.Background(), "main", false, newThreadInboundMessage("late start", "222.333", "222.333"))
	require.ErrorContains(t, err, "thread bridges are draining")

	handled, err := manager.SubmitThreadReply(context.Background(), "D123", "111.222", newThreadInboundMessage("late reply", "333.444", "111.222"))
	require.ErrorContains(t, err, "thread bridges are draining")
	assert.False(t, handled)

	err = manager.SubmitExternalMCP(context.Background(), "main", "external_mcp:main:late", newThreadInboundMessage("late external", "444.555", "111.222"))
	require.ErrorContains(t, err, "thread bridges are draining")

	handled, err = manager.SubmitResponseThreadReply(context.Background(), "D123", "111.222", newThreadInboundMessage("late response", "555.666", "111.222"))
	require.ErrorContains(t, err, "thread bridges are draining")
	assert.True(t, handled)

	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "start", bridge.submits[0].Text)
}

type fakeDirectBridge struct {
	submits          []*events.InboundMessage
	seeds            []events.ResponseCheckpoint
	mainSeeds        int
	stops            int
	ops              []string
	errSeedMain      error
	summarizeStarted chan struct{}
	releaseSummarize chan summarizeOutcome
	waitStarted      chan struct{}
	releaseWait      chan error
	startedCtx       context.Context
}

type summarizeOutcome struct {
	text string
	err  error
}

func (f *fakeDirectBridge) Start(ctx context.Context) error { f.startedCtx = ctx; return nil }

func (f *fakeDirectBridge) Stop(context.Context) error { f.stops++; return nil }

func (f *fakeDirectBridge) Submit(_ context.Context, msg *events.InboundMessage) error {
	if f.startedCtx != nil && f.startedCtx.Err() != nil {
		return fmt.Errorf("fake bridge started context: %w", f.startedCtx.Err())
	}

	f.submits = append(f.submits, msg)
	f.ops = append(f.ops, "submit:"+msg.Text)

	return nil
}

func (f *fakeDirectBridge) SeedThreadFromMain(context.Context) error {
	f.mainSeeds++
	f.ops = append(f.ops, "seed_main")

	return f.errSeedMain
}

func (f *fakeDirectBridge) SeedResponseThread(_ context.Context, checkpoint events.ResponseCheckpoint, _ string) error {
	f.seeds = append(f.seeds, checkpoint)
	f.ops = append(f.ops, "seed_response")

	return nil
}

func (f *fakeDirectBridge) Summarize(_ context.Context, _ string) (string, error) {
	if f.summarizeStarted != nil {
		f.summarizeStarted <- struct{}{}
	}

	if f.releaseSummarize == nil {
		return "", nil
	}

	outcome := <-f.releaseSummarize

	return outcome.text, outcome.err
}

func (f *fakeDirectBridge) WaitIdle(ctx context.Context) error {
	if f.waitStarted != nil {
		f.waitStarted <- struct{}{}
	}

	if f.releaseWait == nil {
		return nil
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for fake bridge idle: %w", ctx.Err())
	case err := <-f.releaseWait:
		return err
	}
}

func newThreadInboundMessage(text, messageTS, threadTS string) *events.InboundMessage {
	inbound := events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", text, true)
	inbound.SlackReply = &events.SlackReplyTarget{ChannelID: "D123", MessageTS: messageTS, ThreadTS: threadTS}

	return inbound
}

func readOneInbound(t *testing.T, bus *events.Bus) *events.InboundMessage {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	for msg := range bus.Inbound(ctx) {
		return msg
	}

	t.Fatal("timed out waiting for inbound message")

	return nil
}

func newTestSessionService(t *testing.T, workspace string) *harnessbridge.SessionService {
	t.Helper()

	service, err := harnessbridge.NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	return service
}
