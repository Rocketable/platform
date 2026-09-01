package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/externalmcp"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubmitExternalMCPInputPreservesPublicConversationMetadata(t *testing.T) {
	var captured *protocol.InboundMessage

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")

	submit := func(_ context.Context, agent, gotConversationID string, inbound *protocol.InboundMessage, activation protocol.ActivationHook) error {
		assert.Equal(t, "planner", agent)
		assert.Equal(t, conversationID, gotConversationID)

		captured = inbound
		require.NoError(t, activation(context.Background(), inbound))
		inbound.CompleteResponse("answer", nil)

		return nil
	}

	content := &protocol.InboundContent{Text: "prompt"}
	result, accepted, err := submitExternalMCPInput(context.Background(), submit, "planner", conversationID, content, map[string]string{"ticket": "123"}, "alice", nil, "public-1", backend.NoopActivationHook)

	require.NoError(t, err)
	assert.True(t, accepted)
	require.NotNil(t, captured)
	assert.Equal(t, protocol.SourceExternalMCP, captured.Source)
	assert.Equal(t, "public-1", captured.Metadata["external_conversation_id"])
	assert.Equal(t, "123", captured.Metadata["ticket"])
	assert.Equal(t, "alice", captured.Metadata[protocol.InboundPrincipalMetadataKey])
	assert.Nil(t, captured.SlackReply)
	assert.Equal(t, externalmcp.SessionResult{ExternalConversationID: "public-1", Agent: "planner", Answer: "answer", Attachments: []externalmcp.SessionAttachment{}}, result)
}

func TestSubmitExternalMCPInputWaitsForOwnQueuedTurnResult(t *testing.T) {
	type queuedTurn struct {
		inbound    *protocol.InboundMessage
		activation protocol.ActivationHook
	}

	queue := make(chan queuedTurn, 2)
	started := make(chan string, 2)
	releaseRecovered := make(chan struct{})
	relayed := make(chan struct{}, 1)

	go func() {
		for turn := range queue {
			inbound := turn.inbound
			if err := turn.activation(context.Background(), inbound); err != nil {
				inbound.CompleteResponse("", err)

				continue
			}

			started <- inbound.Text

			if inbound.Text == "recovered" {
				<-releaseRecovered
				inbound.CompleteResponse("recovered answer", nil)

				continue
			}

			inbound.CompleteResponse("follow-up answer", nil)
		}
	}()

	recovered := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "", "recovered", false)
	queue <- queuedTurn{inbound: recovered, activation: backend.NoopActivationHook}

	require.Equal(t, "recovered", <-started)

	resultCh := make(chan externalmcp.SessionResult, 1)
	errCh := make(chan error, 1)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	submit := func(ctx context.Context, _ string, _ string, inbound *protocol.InboundMessage, activation protocol.ActivationHook) error {
		select {
		case queue <- queuedTurn{inbound: inbound, activation: activation}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	go func() {
		content := &protocol.InboundContent{Text: "follow-up"}
		activation := func(context.Context, *protocol.InboundMessage) error {
			relayed <- struct{}{}

			return nil
		}

		result, _, err := submitExternalMCPInput(context.Background(), submit, "planner", conversationID, content, nil, "", nil, "public-1", activation)
		if err != nil {
			errCh <- err

			return
		}

		resultCh <- result
	}()

	select {
	case <-relayed:
		t.Fatal("follow-up relay posted before recovered turn released")
	case result := <-resultCh:
		t.Fatalf("follow-up completed before recovered turn released: %#v", result)
	case err := <-errCh:
		t.Fatalf("follow-up failed before recovered turn released: %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	close(releaseRecovered)
	require.Equal(t, struct{}{}, <-relayed)
	require.Equal(t, "follow-up", <-started)

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case result := <-resultCh:
		assert.Equal(t, externalmcp.SessionResult{ExternalConversationID: "public-1", Agent: "planner", Answer: "follow-up answer", Attachments: []externalmcp.SessionAttachment{}}, result)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for follow-up result")
	}

	close(queue)
}

func TestExternalMCPInboundContentProvidesRelayAttachments(t *testing.T) {
	content, outbound, err := externalMCPInboundContent([]externalmcp.SessionAttachment{{
		Name:       "report.txt",
		MIMEType:   "text/plain",
		DataBase64: "cmVwb3J0",
	}})

	require.NoError(t, err)
	assert.Equal(t, []string{"External MCP text file attachment report.txt (text/plain):\nreport"}, content.TextAttachments)
	assert.Equal(t, []protocol.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}, outbound)
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

	textRelay := func(_ context.Context, _ *protocol.ExternalMCPRelay, reply *protocol.InboundMessage, _ string) (*protocol.InboundMessage, error) {
		mu.Lock()
		defer mu.Unlock()

		if reply == nil {
			rootCount++
			return &protocol.InboundMessage{SlackReply: &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.222", ThreadTS: "111.222"}}, nil
		}

		return reply, nil
	}
	submit := func(ctx context.Context, _ string, _ string, inbound *protocol.InboundMessage, activation protocol.ActivationHook) error {
		if err := activation(ctx, inbound); err != nil {
			return err
		}

		inbound.CompleteResponse("answer", nil)

		return nil
	}
	cfg := &config.Config{MCPExternal: config.MCPExternalConfig{ListenAddr: "127.0.0.1:0"}, Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"managed"}}}}}
	server, err := startExternalMCPServer(t.Context(), cfg, textRelay, func(context.Context, *protocol.InboundMessage) {}, nil, func(string) bool { return true }, store, submit, testLogger())
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

	relayCalls := 0
	relayConversationID := ""
	usedAgent := ""
	usedConversationID := ""
	errRelay := errors.New("post failed")
	textRelay := func(_ context.Context, relay *protocol.ExternalMCPRelay, _ *protocol.InboundMessage, _ string) (*protocol.InboundMessage, error) {
		relayCalls++
		relayConversationID = relay.ConversationID

		return nil, errRelay
	}
	submit := func(ctx context.Context, agent, gotConversationID string, inbound *protocol.InboundMessage, activation protocol.ActivationHook) error {
		usedAgent = agent
		usedConversationID = gotConversationID

		return activation(ctx, inbound)
	}
	cfg := &config.Config{MCPExternal: config.MCPExternalConfig{ListenAddr: "127.0.0.1:0"}, Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"managed"}}}}}
	server, err := startExternalMCPServer(t.Context(), cfg, textRelay, func(context.Context, *protocol.InboundMessage) {}, nil, func(string) bool { return true }, store, submit, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL(), HTTPClient: http.DefaultClient, DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "existing-1", "agent": "other", "input": "follow up", "slack_channel": "#ops"}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	assert.Equal(t, 1, relayCalls)
	assert.Equal(t, "planner", usedAgent)
	assert.Equal(t, conversationID, usedConversationID)
	assert.Equal(t, conversationID, relayConversationID)
}

func TestExternalMCPNewConversationFailureCompensation(t *testing.T) {
	for _, tt := range []struct {
		name                string
		prepare             func(*backend.SessionService) error
		relayErr, submitErr error
		resultErr           error
		cancelAfterSubmit   bool
		wantCleanup         bool
		wantBinding         bool
	}{
		{name: "root creation", relayErr: errors.New("post failed")},
		{name: "atomic persistence", prepare: func(store *backend.SessionService) error {
			return store.UpsertThread(protocol.SlackThreadConversationID("C123", "111.222"), backend.ThreadState{Agent: "existing"})
		}, wantCleanup: true},
		{name: "first submit", submitErr: errors.New("submit failed"), wantCleanup: true},
		{name: "provider error after acceptance", resultErr: errors.New("provider failed"), wantBinding: true},
		{name: "caller cancellation after acceptance", cancelAfterSubmit: true, wantBinding: true},
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

			cleanupCalls := 0
			relayCalls := 0
			textRelay := func(context.Context, *protocol.ExternalMCPRelay, *protocol.InboundMessage, string) (*protocol.InboundMessage, error) {
				relayCalls++

				if tt.relayErr != nil {
					return nil, tt.relayErr
				}

				return &protocol.InboundMessage{SlackReply: &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.222", ThreadTS: "111.222"}}, nil
			}
			cleanup := func(context.Context, *protocol.InboundMessage) { cleanupCalls++ }

			callCtx, cancel := context.WithCancel(t.Context())
			defer cancel()

			submit := func(_ context.Context, _ string, _ string, inbound *protocol.InboundMessage, _ protocol.ActivationHook) error {
				if tt.submitErr != nil {
					return tt.submitErr
				}

				switch {
				case tt.cancelAfterSubmit:
					cancel()
				case tt.resultErr != nil:
					inbound.CompleteResponse("", tt.resultErr)
				default:
					inbound.CompleteResponse("answer", nil)
				}

				return nil
			}
			cfg := &config.Config{MCPExternal: config.MCPExternalConfig{ListenAddr: "127.0.0.1:0"}, Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"managed"}}}}}
			server, err := startExternalMCPServer(t.Context(), cfg, textRelay, cleanup, nil, func(string) bool { return true }, store, submit, testLogger())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

			client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
			session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL(), HTTPClient: http.DefaultClient, DisableStandaloneSSE: true}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = session.Close() })

			_, _ = session.CallTool(callCtx, &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "failed-1", "agent": "planner", "input": "hello", "slack_channel": "#ops"}})

			_, ok, err := store.ExternalMCPSession("failed-1")
			require.NoError(t, err)
			assert.Equal(t, 1, relayCalls)
			assert.Equal(t, tt.wantBinding, ok)
			assert.Equal(t, tt.wantCleanup, cleanupCalls == 1)
		})
	}
}

func TestStartDevelopmentMCPEnabledStarts(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "rocketclaw.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rocketclaw.development.users.json"), []byte(`{"dev":"token"}`), 0o600))

	cfg := &config.Config{Workspace: dir, MCPDevelopment: config.MCPDevelopmentConfig{Enabled: true, ListenAddr: "127.0.0.1:0"}}
	server, err := startDevelopmentMCP(t.Context(), cfg, configPath, new(sync.Mutex), inertDevelopmentReason, inertDevelopmentReason, testLogger(), testDevelopmentSessions(t))
	require.NoError(t, err)
	require.NotNil(t, server)
	assert.NotEmpty(t, server.URL())

	session := developmentMCPSession(t, server.URL())
	_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "rocketclaw_development_lint", Arguments: map[string]any{"context": map[string]any{"files": []map[string]any{}}}})
	require.NoError(t, err)
	_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "rocketclaw_development_run_turn", Arguments: map[string]any{"context": map[string]any{"files": []map[string]any{}}, "agent": "main", "prompt": "hi", "conversation_id": "devmcp-lock"}})
	require.NoError(t, err)
	_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "rocketclaw_development_list_session", Arguments: map[string]any{}})
	require.NoError(t, err)
	_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "rocketclaw_development_observe_session", Arguments: map[string]any{"conversation_id": "missing"}})
	require.NoError(t, err)
	_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "rocketclaw_development_delete_session", Arguments: map[string]any{"conversation_id": "missing"}})
	require.NoError(t, err)

	require.NoError(t, server.Close(context.Background()))
}

func TestStartDevelopmentMCPDisabledDoesNotListen(t *testing.T) {
	cfg := &config.Config{MCPDevelopment: config.MCPDevelopmentConfig{Enabled: false, ListenAddr: "bad listen address"}}
	server, err := startDevelopmentMCP(t.Context(), cfg, "", new(sync.Mutex), inertDevelopmentReason, inertDevelopmentReason, testLogger(), new(backend.SessionService))
	require.NoError(t, err)
	assert.Nil(t, server)
}

func TestStartDevelopmentMCPReadWaitsForOverlayLock(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "rocketclaw.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rocketclaw.development.users.json"), []byte(`{"dev":"token"}`), 0o600))

	overlayMu := new(sync.Mutex)
	cfg := &config.Config{Workspace: dir, MCPDevelopment: config.MCPDevelopmentConfig{Enabled: true, ListenAddr: "127.0.0.1:0"}}
	server, err := startDevelopmentMCP(t.Context(), cfg, configPath, overlayMu, inertDevelopmentReason, inertDevelopmentReason, testLogger(), new(backend.SessionService))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	overlayMu.Lock()

	started := make(chan struct{})
	done := make(chan struct{})

	go func() {
		client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)

		session, errConnect := client.Connect(t.Context(), &mcp.StreamableClientTransport{
			Endpoint:             server.URL(),
			HTTPClient:           &http.Client{Transport: developmentMCPAuthTransport{base: http.DefaultTransport}},
			DisableStandaloneSSE: true,
		}, nil)
		if errConnect != nil {
			close(started)
			close(done)

			return
		}

		defer func() { _ = session.Close() }()

		close(started)

		_, _ = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "rocketclaw_development_read_context_from_overlay", Arguments: map[string]any{"overlay": "github.com/rocketable/overlay"}})

		close(done)
	}()

	<-started

	select {
	case <-done:
		t.Fatal("read overlay proceeded while overlay lock was held")
	case <-time.After(50 * time.Millisecond):
	}

	overlayMu.Unlock()
	<-done
}

func TestStartDevelopmentMCPSessionToolsDoNotWaitForOverlayLock(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "rocketclaw.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rocketclaw.development.users.json"), []byte(`{"dev":"token"}`), 0o600))

	overlayMu := new(sync.Mutex)
	cfg := &config.Config{Workspace: dir, MCPDevelopment: config.MCPDevelopmentConfig{Enabled: true, ListenAddr: "127.0.0.1:0"}}
	server, err := startDevelopmentMCP(t.Context(), cfg, configPath, overlayMu, inertDevelopmentReason, inertDevelopmentReason, testLogger(), testDevelopmentSessions(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	overlayMu.Lock()
	t.Cleanup(overlayMu.Unlock)

	session := developmentMCPSession(t, server.URL())
	done := make(chan struct{})
	go func() {
		_, _ = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "rocketclaw_development_list_session", Arguments: map[string]any{}})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("list session waited on overlay lock")
	}
}

func TestStartDevelopmentMCPEnabledMissingUsers(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "rocketclaw.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0o600))

	cfg := &config.Config{Workspace: dir, MCPDevelopment: config.MCPDevelopmentConfig{Enabled: true, ListenAddr: "127.0.0.1:0"}}
	server, err := startDevelopmentMCP(t.Context(), cfg, configPath, new(sync.Mutex), inertDevelopmentReason, inertDevelopmentReason, testLogger(), new(backend.SessionService))
	require.ErrorContains(t, err, "development MCP users are required")
	assert.Nil(t, server)
}

func testDevelopmentSessions(t *testing.T) *backend.SessionService {
	t.Helper()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	service, err := backend.NewSessionServiceIn(dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	return service
}

func developmentMCPSession(t *testing.T, url string) *mcp.ClientSession {
	t.Helper()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             url,
		HTTPClient:           &http.Client{Transport: developmentMCPAuthTransport{base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	return session
}

type developmentMCPAuthTransport struct {
	base http.RoundTripper
}

func (t developmentMCPAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.SetBasicAuth("dev", "token")

	resp, err := t.base.RoundTrip(clone)
	if err != nil {
		return nil, fmt.Errorf("send development MCP request: %w", err)
	}

	return resp, nil
}

func inertDevelopmentReason(string) (string, error) {
	return "", nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
