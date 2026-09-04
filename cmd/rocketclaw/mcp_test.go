package main

import (
	"context"
	"encoding/base64"
	"errors"
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

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")

	submit := func(_ context.Context, agent, gotConversationID string, inbound *protocol.InboundMessage) error {
		assert.Equal(t, "planner", agent)
		assert.Equal(t, conversationID, gotConversationID)

		captured = inbound
		inbound.CompleteResponseWithAttachments("answer", nil, nil)

		return nil
	}

	content := &protocol.InboundContent{Text: "prompt"}
	result, accepted, err := submitExternalMCPInput(context.Background(), submit, "planner", conversationID, content, map[string]string{"ticket": "123"}, "alice", nil, "public-1")

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

	recovered := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "", "recovered", false)
	queue <- recovered

	require.Equal(t, "recovered", <-started)

	resultCh := make(chan externalmcp.SessionResult, 1)
	errCh := make(chan error, 1)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	submit := func(ctx context.Context, _ string, _ string, inbound *protocol.InboundMessage) error {
		select {
		case queue <- inbound:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	go func() {
		result, _, err := submitExternalMCPInput(context.Background(), submit, "planner", conversationID, &protocol.InboundContent{Text: "follow-up"}, nil, "", nil, "public-1")
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

func TestExternalMCPInboundContentCoversAttachmentWarnings(t *testing.T) {
	_, _, err := externalMCPInboundContent([]externalmcp.SessionAttachment{{DataBase64: "not-base64"}})
	require.ErrorContains(t, err, "decode external MCP attachment")

	empty, outbound, err := externalMCPInboundContent(nil)
	require.NoError(t, err)
	require.Empty(t, empty.Attachments)
	require.Nil(t, outbound)

	huge := strings.Repeat("a", protocol.MaxInboundTextAttachmentBytes+1)
	content, _, err := externalMCPInboundContent([]externalmcp.SessionAttachment{{Name: "", MIMEType: "text/plain", DataBase64: base64.StdEncoding.EncodeToString([]byte(huge))}})
	require.NoError(t, err)
	require.NotEmpty(t, content.AttachmentWarnings)

	content, _, err = externalMCPInboundContent([]externalmcp.SessionAttachment{{Name: "bin.txt", MIMEType: "text/plain", DataBase64: base64.StdEncoding.EncodeToString([]byte{0})}})
	require.NoError(t, err)
	require.NotEmpty(t, content.AttachmentWarnings)

	content, _, err = externalMCPInboundContent([]externalmcp.SessionAttachment{{Name: "blank.txt", MIMEType: "text/plain", DataBase64: base64.StdEncoding.EncodeToString([]byte("  "))}})
	require.NoError(t, err)
	require.NotEmpty(t, content.AttachmentWarnings)

	content, _, err = externalMCPInboundContent([]externalmcp.SessionAttachment{{Name: "pic.png", MIMEType: "image/png", DataBase64: base64.StdEncoding.EncodeToString([]byte("png"))}})
	require.NoError(t, err)
	require.Len(t, content.Attachments, 1)
}

func TestExternalMCPDuplicateSuppliedIDCreatesOneSlackRoot(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	store, err := backend.NewSessionServiceIn(dsn, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop()) })

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
	submit := func(_ context.Context, _ string, _ string, inbound *protocol.InboundMessage) error {
		inbound.CompleteResponseWithAttachments("answer", nil, nil)

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

func TestExternalMCPRepeatedIDKeepsLockedAgentAndRejectsChannelMismatch(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	store, err := backend.NewSessionServiceIn(dsn, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop()) })

	var agents []string

	textRelay := func(_ context.Context, _ *protocol.ExternalMCPRelay, reply *protocol.InboundMessage, _ string) (*protocol.InboundMessage, error) {
		if reply == nil {
			return &protocol.InboundMessage{SlackReply: &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.222", ThreadTS: "111.222"}}, nil
		}

		return reply, nil
	}
	submit := func(_ context.Context, agent string, _ string, inbound *protocol.InboundMessage) error {
		agents = append(agents, agent)
		inbound.CompleteResponseWithAttachments("answer", nil, nil)

		return nil
	}
	cfg := &config.Config{MCPExternal: config.MCPExternalConfig{ListenAddr: "127.0.0.1:0"}, Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"managed"}}, {Channel: "#triage", Agents: []string{"triage"}}}}}
	server, err := startExternalMCPServer(t.Context(), cfg, textRelay, func(context.Context, *protocol.InboundMessage) {}, nil, func(string) bool { return true }, store, submit, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL(), HTTPClient: http.DefaultClient, DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	first, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "ticket-1", "agent": "planner", "input": "hello", "slack_channel": "#ops"}})
	require.NoError(t, err)
	require.False(t, first.IsError)

	repeat, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "ticket-1", "agent": "other", "input": "again", "slack_channel": "#ops"}})
	require.NoError(t, err)
	require.False(t, repeat.IsError)
	require.Equal(t, []string{"planner", "planner"}, agents)

	stored, ok, err := store.ExternalMCPSession("ticket-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "planner", stored.Agent)
	assert.Equal(t, "#ops", stored.SlackChannel)

	mismatch, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{"external_conversation_id": "ticket-1", "agent": "planner", "input": "wrong channel", "slack_channel": "#triage"}})
	require.NoError(t, err)
	require.True(t, mismatch.IsError)
	require.Contains(t, mismatch.Content[0].(*mcp.TextContent).Text, `bound to Slack channel "#ops"`)
	require.Equal(t, []string{"planner", "planner"}, agents)
}

func TestSubmitExternalMCPInputReturnsAfterSubmitAgent(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	submit := func(_ context.Context, _ string, _ string, inbound *protocol.InboundMessage) error {
		inbound.CompleteResponseWithAttachments("answer", nil, nil)
		close(started)
		<-release

		return nil
	}

	resultCh := make(chan externalmcp.SessionResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, _, err := submitExternalMCPInput(context.Background(), submit, "planner", "external_mcp:planner:x", &protocol.InboundContent{Text: "hello"}, nil, "", nil, "public-1")
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
			t.Cleanup(func() { require.NoError(t, store.Stop()) })

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

			submit := func(_ context.Context, _ string, _ string, inbound *protocol.InboundMessage) error {
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

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
