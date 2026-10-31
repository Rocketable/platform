package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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

	turns := mcpTurns(func(inbound *protocol.InboundMessage) error {
		captured = inbound
		inbound.CompleteResponseWithAttachments("answer", nil, nil)

		return nil
	})
	content := &protocol.InboundContent{Text: "prompt"}
	metadata := map[string]string{"ticket": "123", protocol.InboundPrincipalNameMetadataKey: "Mallory", protocol.InboundPrincipalEmailMetadataKey: "mallory@example.com", protocol.InboundSenderAgentMetadataKey: "main"}
	result, accepted, err := submitExternalMCPInput(context.Background(), turns, "planner", "external_mcp:planner:private", "managed-chat", content, metadata, "alice", "public-1")

	require.NoError(t, err)
	assert.True(t, accepted)
	require.NotNil(t, captured)
	assert.Equal(t, protocol.SourceExternalMCP, captured.Source)
	assert.Equal(t, "public-1", captured.Metadata["external_conversation_id"])
	assert.Equal(t, "123", captured.Metadata["ticket"])
	assert.Equal(t, "alice", captured.Metadata[protocol.InboundPrincipalMetadataKey])
	assert.NotContains(t, captured.Metadata, protocol.InboundPrincipalNameMetadataKey, "an External MCP caller cannot name a person")
	assert.NotContains(t, captured.Metadata, protocol.InboundPrincipalEmailMetadataKey)
	assert.NotContains(t, captured.Metadata, protocol.InboundSenderAgentMetadataKey, "nor the agent a permission review names")
	assert.Equal(t, "external_mcp:planner:private", captured.ConversationID)
	assert.Equal(t, "managed-chat", captured.SyncDestination, "the private run reports to its Web chat")
	assert.Nil(t, captured.SlackReply, "External MCP never replies in Slack")
	assert.False(t, captured.RequireOutputDecision, "MCP output does not use the cron decision contract")
	assert.Equal(t, externalmcp.SessionResult{ExternalConversationID: "public-1", Agent: "planner", Answer: "answer", Attachments: []externalmcp.SessionAttachment{}}, result)
}

func TestSubmitExternalMCPInputWaitsForOwnQueuedTurnResult(t *testing.T) {
	queue := make(chan *protocol.InboundMessage, 2)
	started := make(chan string, 2)
	releaseRecovered := make(chan struct{})

	go func() {
		for inbound := range queue {
			started <- inbound.Text

			if inbound.Text == "recovered" {
				<-releaseRecovered
				inbound.CompleteResponseWithAttachments("recovered answer", nil, nil)

				continue
			}

			inbound.CompleteResponseWithAttachments("follow-up answer", nil, nil)
		}
	}()

	recovered := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "recovered", false)
	queue <- recovered

	require.Equal(t, "recovered", <-started)

	resultCh := make(chan externalmcp.SessionResult, 1)
	errCh := make(chan error, 1)
	turns := mcpTurns(func(inbound *protocol.InboundMessage) error {
		queue <- inbound
		return nil
	})

	go func() {
		result, _, err := submitExternalMCPInput(context.Background(), turns, "planner", "external_mcp:planner:x", "managed-chat", &protocol.InboundContent{Text: "follow-up"}, nil, "", "public-1")
		if err != nil {
			errCh <- err

			return
		}

		resultCh <- result
	}()

	select {
	case result := <-resultCh:
		t.Fatalf("follow-up completed before recovered turn released: %#v", result)
	case err := <-errCh:
		t.Fatalf("follow-up failed before recovered turn released: %v", err)
	case <-started:
		t.Fatal("follow-up started before recovered turn released")
	case <-time.After(10 * time.Millisecond):
	}

	close(releaseRecovered)
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

func TestExternalMCPInboundContentAddsTextAttachments(t *testing.T) {
	content, err := externalMCPInboundContent([]externalmcp.SessionAttachment{{
		Name:       "report.txt",
		MIMEType:   "text/plain",
		DataBase64: "cmVwb3J0",
	}})

	require.NoError(t, err)
	assert.Equal(t, []string{"External MCP text file attachment report.txt (text/plain):\nreport"}, content.TextAttachments)
}

func TestExternalMCPInboundContentCoversAttachmentWarnings(t *testing.T) {
	_, err := externalMCPInboundContent([]externalmcp.SessionAttachment{{DataBase64: "not-base64"}})
	require.ErrorContains(t, err, "decode external MCP attachment")

	empty, err := externalMCPInboundContent(nil)
	require.NoError(t, err)
	require.Empty(t, empty.Attachments)

	huge := strings.Repeat("a", protocol.MaxInboundTextAttachmentBytes+1)
	content, err := externalMCPInboundContent([]externalmcp.SessionAttachment{{Name: "", MIMEType: "text/plain", DataBase64: base64.StdEncoding.EncodeToString([]byte(huge))}})
	require.NoError(t, err)
	require.NotEmpty(t, content.AttachmentWarnings)

	content, err = externalMCPInboundContent([]externalmcp.SessionAttachment{{Name: "bin.txt", MIMEType: "text/plain", DataBase64: base64.StdEncoding.EncodeToString([]byte{0})}})
	require.NoError(t, err)
	require.NotEmpty(t, content.AttachmentWarnings)

	content, err = externalMCPInboundContent([]externalmcp.SessionAttachment{{Name: "blank.txt", MIMEType: "text/plain", DataBase64: base64.StdEncoding.EncodeToString([]byte("  "))}})
	require.NoError(t, err)
	require.NotEmpty(t, content.AttachmentWarnings)

	content, err = externalMCPInboundContent([]externalmcp.SessionAttachment{{Name: "pic.png", MIMEType: "image/png", DataBase64: base64.StdEncoding.EncodeToString([]byte("png"))}})
	require.NoError(t, err)
	require.Len(t, content.Attachments, 1)
}

func TestExternalMCPDuplicateSuppliedIDCreatesOneWebChat(t *testing.T) {
	store := newMCPTestStore(t)

	var (
		mu    sync.Mutex
		turns []*protocol.InboundMessage
	)

	cfg := &config.Config{MCPExternal: config.MCPExternalConfig{ListenAddr: "127.0.0.1:0"}}
	server, err := startExternalMCPServer(t.Context(), cfg, nil, &mcpAgentIndex{names: []string{"planner"}}, store, mcpTurns(func(inbound *protocol.InboundMessage) error {
		mu.Lock()
		turns = append(turns, inbound)
		mu.Unlock()

		inbound.CompleteResponseWithAttachments("answer", nil, nil)
		return nil
	}), testLogger())
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

			_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "shared-1", "agent": "planner", "input": "hello"}})

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

	session, ok, err := store.ExternalMCPSession("shared-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "planner", session.Agent)
	assert.Contains(t, session.PrivateConversationID, "external_mcp:planner:")

	require.Len(t, turns, calls)
	for _, turn := range turns {
		assert.Equal(t, session.PrivateConversationID, turn.ConversationID)
		assert.Equal(t, session.ManagedConversationID, turn.SyncDestination)
	}

	kind, ok, err := store.ConversationKind(t.Context(), session.ManagedConversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, backend.KindWebChat, kind)

	thread, ok, err := store.Thread(session.ManagedConversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "planner", thread.Agent, "the Web chat runs the caller's agent")
}

func TestExternalMCPRepeatedIDKeepsLockedAgent(t *testing.T) {
	store := newMCPTestStore(t)

	var conversations []string

	turns := mcpTurns(func(inbound *protocol.InboundMessage) error {
		conversations = append(conversations, inbound.ConversationID)
		inbound.CompleteResponseWithAttachments("answer", nil, nil)
		return nil
	})
	cfg := &config.Config{MCPExternal: config.MCPExternalConfig{ListenAddr: "127.0.0.1:0"}}
	server, err := startExternalMCPServer(t.Context(), cfg, nil, &mcpAgentIndex{names: []string{"planner"}}, store, turns, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL(), HTTPClient: http.DefaultClient, DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	first, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "ticket-1", "agent": "planner", "input": "hello", "metadata": map[string]string{"topic": "original"}}})
	require.NoError(t, err)
	require.False(t, first.IsError)

	repeat, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "ticket-1", "agent": "other", "input": "again", "metadata": map[string]string{"topic": "later"}}})
	require.NoError(t, err)
	require.False(t, repeat.IsError)

	stored, ok, err := store.ExternalMCPSession("ticket-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "planner", stored.Agent)
	assert.Equal(t, map[string]string{"topic": "original"}, stored.OriginPairs)
	assert.Equal(t, []string{stored.PrivateConversationID, stored.PrivateConversationID}, conversations)
}

// A call naming an agent MCP does not expose is refused and records nothing; a session whose
// agent stops being exposed is refused too.
func TestExternalMCPRefusesUnexposedAgents(t *testing.T) {
	store := newMCPTestStore(t)
	agents := &mcpAgentIndex{names: []string{"planner"}}

	cfg := &config.Config{MCPExternal: config.MCPExternalConfig{ListenAddr: "127.0.0.1:0"}}
	server, err := startExternalMCPServer(t.Context(), cfg, nil, agents, store, mcpTurns(func(inbound *protocol.InboundMessage) error {
		inbound.CompleteResponseWithAttachments("answer", nil, nil)
		return nil
	}), testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL(), HTTPClient: http.DefaultClient, DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	call := func(id, agent string) *mcp.CallToolResult {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": id, "agent": agent, "input": "hello"}})
		require.NoError(t, err)

		return result
	}

	refused := call("ticket-1", "ghost")
	require.True(t, refused.IsError)
	assert.Contains(t, refused.Content[0].(*mcp.TextContent).Text, `external MCP agent "ghost" is not exposed`)

	refused, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "ticket-1", "agent": "planner", "input": "hello", "attachments": []map[string]any{{"data_base64": "not-base64"}}}})
	require.NoError(t, err)
	require.True(t, refused.IsError, "an undecodable attachment is refused")
	_, ok, err := store.ExternalMCPSession("ticket-1")
	require.NoError(t, err)
	assert.False(t, ok, "a refused first call records nothing")

	require.False(t, call("ticket-2", "planner").IsError)

	agents.mu.Lock()
	agents.names = nil
	agents.mu.Unlock()

	refused = call("ticket-2", "planner")
	require.True(t, refused.IsError)
	assert.Contains(t, refused.Content[0].(*mcp.TextContent).Text, `external MCP agent "planner" is not exposed`)
}

func TestSubmitExternalMCPInputReturnsAfterSubmitAgent(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	turns := mcpTurns(func(inbound *protocol.InboundMessage) error {
		inbound.CompleteResponseWithAttachments("answer", nil, nil)
		close(started)
		<-release

		return nil
	})

	resultCh := make(chan externalmcp.SessionResult, 1)
	errCh := make(chan error, 1)

	go func() {
		result, _, err := submitExternalMCPInput(context.Background(), turns, "planner", "external_mcp:planner:x", "managed-chat", &protocol.InboundContent{Text: "hello"}, nil, "", "public-1")
		if err != nil {
			errCh <- err

			return
		}

		resultCh <- result
	}()

	<-started

	select {
	case result := <-resultCh:
		t.Fatalf("returned before submitAgent finished: %#v", result)
	case err := <-errCh:
		t.Fatalf("failed before submitAgent finished: %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case result := <-resultCh:
		assert.Equal(t, externalmcp.SessionResult{ExternalConversationID: "public-1", Agent: "planner", Answer: "answer", Attachments: []externalmcp.SessionAttachment{}}, result)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for submitAgent result")
	}
}

func TestExternalMCPNewConversationFailureCompensation(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		submitErr, resultErr error
		cancelAfterSubmit    bool
		wantBinding          bool
	}{
		{name: "first submit", submitErr: errors.New("submit failed")},
		{name: "provider error after acceptance", resultErr: errors.New("provider failed"), wantBinding: true},
		{name: "caller cancellation after acceptance", cancelAfterSubmit: true, wantBinding: true},
		{name: "shutdown after acceptance", submitErr: fmt.Errorf("run turn: %w", protocol.ErrBridgeStopped), wantBinding: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newMCPTestStore(t)

			callCtx, cancel := context.WithCancel(t.Context())
			defer cancel()

			var managed string

			turns := mcpTurns(func(inbound *protocol.InboundMessage) error {
				managed = inbound.SyncDestination

				if tt.submitErr != nil {
					return tt.submitErr
				}

				switch {
				case tt.cancelAfterSubmit:
					cancel()
				case tt.resultErr != nil:
					inbound.CompleteResponseWithAttachments("", nil, tt.resultErr)
				default:
					inbound.CompleteResponseWithAttachments("answer", nil, nil)
				}

				return nil
			})
			cfg := &config.Config{MCPExternal: config.MCPExternalConfig{ListenAddr: "127.0.0.1:0"}}
			server, err := startExternalMCPServer(t.Context(), cfg, nil, &mcpAgentIndex{names: []string{"planner"}}, store, turns, testLogger())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

			client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
			session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL(), HTTPClient: http.DefaultClient, DisableStandaloneSSE: true}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = session.Close() })

			_, _ = session.CallTool(callCtx, &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "failed-1", "agent": "planner", "input": "hello"}})

			_, ok, err := store.ExternalMCPSession("failed-1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantBinding, ok)

			_, ok, err = store.ConversationKind(t.Context(), managed)
			require.NoError(t, err)
			assert.Equal(t, tt.wantBinding, ok, "a refused first prompt leaves no Web chat behind")
		})
	}
}

func newMCPTestStore(t *testing.T) *backend.SessionService {
	t.Helper()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	store, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop()) })

	return store
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func mcpTurns(run func(*protocol.InboundMessage) error) *BackendMock {
	return &BackendMock{
		RunTurnFunc: func(_ context.Context, inbound *protocol.InboundMessage) error {
			return run(inbound)
		},
	}
}
