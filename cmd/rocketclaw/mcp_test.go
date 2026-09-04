package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/externalmcp"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExternalMCPInboundContentInlinesTextAttachments(t *testing.T) {
	content, err := externalMCPInboundContent([]externalmcp.SessionAttachment{{
		Name:       "report.txt",
		MIMEType:   "text/plain",
		DataBase64: "cmVwb3J0",
	}})

	require.NoError(t, err)
	assert.Equal(t, []string{"External MCP text file attachment report.txt (text/plain):\nreport"}, content.TextAttachments)
}

func TestExternalMCPDuplicateSuppliedIDCreatesOneSlackRoot(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	store, err := backend.NewSessionServiceIn(dsn, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	var (
		mu        sync.Mutex
		rootCount int
	)

	startThread := func(context.Context, string, string, string, string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		rootCount++
		return protocol.SlackThreadConversationID("C123", "111.222"), nil
	}
	cfg := &config.Config{MCPExternal: config.MCPExternalConfig{ListenAddr: "127.0.0.1:0"}, Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"managed"}}}}}
	conv := newTestMCPBackend(t, store)
	server, err := startExternalMCPServer(t.Context(), cfg, startThread, nil, func(string) bool { return true }, store, conv, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	const calls = 8

	errCh := make(chan error, calls)

	var group sync.WaitGroup
	for range calls {
		group.Go(func() {
			client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
			transport := &mcp.StreamableClientTransport{Endpoint: server.URL(), HTTPClient: http.DefaultClient, DisableStandaloneSSE: true}

			session, err := client.Connect(t.Context(), transport, nil)
			if err != nil {
				errCh <- err
				return
			}

			_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "shared-1", "agent": "planner", "input": "hello", "slack_channel": "#ops"}})

			errClose := session.Close()
			if errors.Is(errClose, context.Canceled) {
				errClose = nil
			}

			errCh <- errors.Join(err, errClose)
		})
	}

	group.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	mu.Lock()
	assert.Equal(t, 1, rootCount)
	mu.Unlock()

	session, ok, err := store.ExternalMCPSession("shared-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, protocol.SlackThreadConversationID("C123", "111.222"), session.ManagedConversationID)
	assert.Equal(t, "planner", session.Agent)
	assert.Contains(t, session.PrivateConversationID, "external_mcp:planner:")
	thread, ok, err := store.Thread(session.ManagedConversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "managed", thread.Agent)
}

func TestLegacyExternalMCPFollowupUsesExistingSharedConversation(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	store, err := backend.NewSessionServiceIn(dsn, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, backend.ThreadState{Agent: "planner"}))
	require.NoError(t, store.UpsertExternalMCPSession("existing-1", &backend.ExternalMCPSessionState{Agent: "planner", ManagedConversationID: conversationID, SlackChannel: "#ops"}))

	startThreadCalls := 0
	startThread := func(context.Context, string, string, string, string) (string, error) {
		startThreadCalls++
		return "", errors.New("startThread should not run for a known pair")
	}
	cfg := &config.Config{MCPExternal: config.MCPExternalConfig{ListenAddr: "127.0.0.1:0"}, Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"managed"}}}}}
	conv := newTestMCPBackend(t, store)
	server, err := startExternalMCPServer(t.Context(), cfg, startThread, nil, func(string) bool { return true }, store, conv, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL(), HTTPClient: http.DefaultClient, DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "existing-1", "agent": "other", "input": "follow up", "slack_channel": "#ops"}})
	require.NoError(t, err)
	assert.Equal(t, 0, startThreadCalls)
	turns := conv.turns()
	require.Len(t, turns, 1)
	assert.Equal(t, "planner", turns[0].Agent)
	assert.Equal(t, conversationID, turns[0].ID)
	assert.Equal(t, protocol.TurnPrompt, turns[0].Kind)
}

func TestExternalMCPSlackChannelMismatchErrors(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	store, err := backend.NewSessionServiceIn(dsn, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, backend.ThreadState{Agent: "planner"}))
	require.NoError(t, store.UpsertExternalMCPSession("existing-1", &backend.ExternalMCPSessionState{Agent: "planner", PrivateConversationID: "external_mcp:planner:x", ManagedConversationID: conversationID, SlackChannel: "#ops"}))

	cfg := &config.Config{MCPExternal: config.MCPExternalConfig{ListenAddr: "127.0.0.1:0"}, Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"managed"}}, {Channel: "#other", Agents: []string{"managed"}}}}}
	conv := newTestMCPBackend(t, store)
	server, err := startExternalMCPServer(t.Context(), cfg, func(context.Context, string, string, string, string) (string, error) {
		return "", errors.New("startThread should not run")
	}, nil, func(string) bool { return true }, store, conv, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL(), HTTPClient: http.DefaultClient, DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "existing-1", "agent": "planner", "input": "hello", "slack_channel": "#other"}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	assert.Empty(t, conv.turns())
}

func TestExternalMCPAfterSyncYHasXEntriesLaterYPromptNotOnX(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	store, err := backend.NewSessionServiceIn(dsn, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	yID := protocol.SlackThreadConversationID("C123", "111.222")
	startThread := func(context.Context, string, string, string, string) (string, error) {
		return yID, nil
	}
	cfg := &config.Config{MCPExternal: config.MCPExternalConfig{ListenAddr: "127.0.0.1:0"}, Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"managed"}}}}}
	conv := newTestMCPBackend(t, store)
	server, err := startExternalMCPServer(t.Context(), cfg, startThread, nil, func(string) bool { return true }, store, conv, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL(), HTTPClient: http.DefaultClient, DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "ae1-1", "agent": "planner", "input": "hello", "slack_channel": "#ops"}})
	require.NoError(t, err)

	pair, ok, err := store.ExternalMCPSession("ae1-1")
	require.NoError(t, err)
	require.True(t, ok)

	xEntries, err := store.ObserveEntries(t.Context(), pair.PrivateConversationID, 0)
	require.NoError(t, err)
	require.NotEmpty(t, xEntries)
	yEntries, err := store.ObserveEntries(t.Context(), pair.ManagedConversationID, 0)
	require.NoError(t, err)
	require.Equal(t, xEntries[0].Entry, yEntries[0].Entry)

	_, err = store.AppendEntryID(t.Context(), pair.ManagedConversationID, &harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(99, 0).UTC()})
	require.NoError(t, err)

	xAfter, err := store.ObserveEntries(t.Context(), pair.PrivateConversationID, 0)
	require.NoError(t, err)
	assert.Len(t, xAfter, len(xEntries))
	yAfter, err := store.ObserveEntries(t.Context(), pair.ManagedConversationID, 0)
	require.NoError(t, err)
	assert.Greater(t, len(yAfter), len(xAfter))

	listed, err := conv.ListConversations()
	require.NoError(t, err)
	byID := map[string]protocol.ConversationRecord{}
	for _, rec := range listed {
		byID[rec.ID] = rec
	}
	require.Contains(t, byID, pair.PrivateConversationID)
	require.NotContains(t, byID[pair.PrivateConversationID].Tags, protocol.ConversationUserFacing)
	require.Contains(t, byID, pair.ManagedConversationID)
	require.Contains(t, byID[pair.ManagedConversationID].Tags, protocol.ConversationUserFacing)
}

func TestUnionMCPPairXAddsPrivateID(t *testing.T) {
	yID := protocol.SlackThreadConversationID("C1", "1.000")
	xID := "external_mcp:planner:private"
	listed := []protocol.ConversationRecord{{ID: yID, Agents: []string{"main"}, Tags: []protocol.ConversationTag{protocol.ConversationUserFacing}}}
	got := unionMCPPairX(listed, map[string]backend.ExternalMCPSessionState{
		"public-1": {Agent: "planner", PrivateConversationID: xID, ManagedConversationID: yID},
	})

	byID := map[string]protocol.ConversationRecord{}
	for _, rec := range got {
		byID[rec.ID] = rec
	}
	require.Contains(t, byID, xID)
	require.Equal(t, []string{"planner"}, byID[xID].Agents)
	require.NotContains(t, byID[xID].Tags, protocol.ConversationUserFacing)
	require.Contains(t, byID, yID)
	require.Contains(t, byID[yID].Tags, protocol.ConversationUserFacing)

	again := unionMCPPairX(got, map[string]backend.ExternalMCPSessionState{
		"public-1": {Agent: "planner", PrivateConversationID: xID, ManagedConversationID: yID},
	})
	require.Len(t, again, len(got))
}

func TestExternalMCPNewConversationFailureCompensation(t *testing.T) {
	for _, tt := range []struct {
		name           string
		prepare        func(*backend.SessionService) error
		startThreadErr error
		runTurnErr     error
		wantBinding    bool
	}{
		{name: "root creation", startThreadErr: errors.New("post failed")},
		{name: "atomic persistence", prepare: func(store *backend.SessionService) error {
			return store.UpsertThread(protocol.SlackThreadConversationID("C123", "111.222"), backend.ThreadState{Agent: "existing"})
		}},
		{name: "first run turn", runTurnErr: errors.New("run turn failed"), wantBinding: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
			require.NoError(t, err)
			store, err := backend.NewSessionServiceIn(dsn, testLogger())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

			if tt.prepare != nil {
				require.NoError(t, tt.prepare(store))
			}

			startThread := func(context.Context, string, string, string, string) (string, error) {
				if tt.startThreadErr != nil {
					return "", tt.startThreadErr
				}

				return protocol.SlackThreadConversationID("C123", "111.222"), nil
			}
			cfg := &config.Config{MCPExternal: config.MCPExternalConfig{ListenAddr: "127.0.0.1:0"}, Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"managed"}}}}}
			conv := newTestMCPBackend(t, store)
			conv.errRunTurn = tt.runTurnErr
			server, err := startExternalMCPServer(t.Context(), cfg, startThread, nil, func(string) bool { return true }, store, conv, testLogger())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

			client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
			session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL(), HTTPClient: http.DefaultClient, DisableStandaloneSSE: true}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = session.Close() })

			_, _ = session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "failed-1", "agent": "planner", "input": "hello", "slack_channel": "#ops"}})

			_, ok, err := store.ExternalMCPSession("failed-1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantBinding, ok)
		})
	}
}

type testMCPBackend struct {
	rt         *backend.Runtime
	store      *backend.SessionService
	errRunTurn error

	mu    sync.Mutex
	saved []protocol.TurnRequest
}

func newTestMCPBackend(t *testing.T, store *backend.SessionService) *testMCPBackend {
	t.Helper()

	rt := backend.RuntimeFor()
	rt.Sessions = store

	return &testMCPBackend{rt: rt, store: store}
}

func (b *testMCPBackend) Subscribe(context.Context) <-chan protocol.ConversationEvent {
	ch := make(chan protocol.ConversationEvent)
	close(ch)

	return ch
}

func (b *testMCPBackend) CreateConversation(id string, agents []string, tags []protocol.ConversationTag) error {
	return b.rt.CreateConversation(id, agents, tags)
}

func (b *testMCPBackend) RunTurn(ctx context.Context, req *protocol.TurnRequest) error {
	b.mu.Lock()
	b.saved = append(b.saved, *req)
	errRunTurn := b.errRunTurn
	b.mu.Unlock()

	if errRunTurn != nil {
		return errRunTurn
	}

	_, err := b.store.AppendEntryID(ctx, req.ID, &harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC()})
	return err
}

func (b *testMCPBackend) SyncConversation(ctx context.Context, src, dst string) error {
	srcEntries, err := b.store.ObserveEntries(ctx, src, 0)
	if err != nil {
		return err
	}

	dstEntries, err := b.store.ObserveEntries(ctx, dst, 0)
	if err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(dstEntries))
	for i := range dstEntries {
		raw, errMarshal := json.Marshal(dstEntries[i].Entry)
		if errMarshal != nil {
			return errMarshal
		}
		seen[string(raw)] = struct{}{}
	}

	for i := range srcEntries {
		raw, errMarshal := json.Marshal(srcEntries[i].Entry)
		if errMarshal != nil {
			return errMarshal
		}
		if _, ok := seen[string(raw)]; ok {
			continue
		}
		if _, err := b.store.AppendEntryID(ctx, dst, &srcEntries[i].Entry); err != nil {
			return err
		}
	}

	return nil
}

func (b *testMCPBackend) ListConversations() ([]protocol.ConversationRecord, error) {
	return b.rt.ListConversations()
}

func (b *testMCPBackend) ConversationAgent(string) (string, error) {
	return "", protocol.ErrUnknownConversation
}

func (b *testMCPBackend) SwitchAgent(string, string) error {
	return nil
}

func (b *testMCPBackend) ListLaterWork(ctx context.Context, id string) ([]protocol.ThreadQueueItem, error) {
	return b.rt.ListLaterWork(ctx, id)
}

func (b *testMCPBackend) DeleteLaterWork(ctx context.Context, id, itemID string) error {
	return b.rt.DeleteLaterWork(ctx, id, itemID)
}

func (b *testMCPBackend) ReorderLaterWork(ctx context.Context, id string, itemIDs []string) error {
	return b.rt.ReorderLaterWork(ctx, id, itemIDs)
}

func (b *testMCPBackend) ConversationBusy(id string) bool {
	return b.rt.ConversationBusy(id)
}

func (b *testMCPBackend) ScheduledMessages(id string) (map[string]protocol.ScheduledMessageState, error) {
	return b.rt.ScheduledMessages(id)
}

func (b *testMCPBackend) WorkflowDescriptions() ([]protocol.WorkflowDescription, error) {
	return b.rt.WorkflowDescriptions()
}

func (b *testMCPBackend) turns() []protocol.TurnRequest {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]protocol.TurnRequest(nil), b.saved...)
}

var _ frontend.Backend = (*testMCPBackend)(nil)

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
