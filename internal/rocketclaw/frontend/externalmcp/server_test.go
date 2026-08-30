package externalmcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartSessionPromptServerCallsHandler(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, func(_ context.Context, username, externalConversationID, agent, input string, metadata map[string]string, attachments []SessionAttachment, slackChannel string) (SessionResult, error) {
		assert.Empty(t, username)
		assert.Equal(t, "test-conversation", externalConversationID)
		assert.Equal(t, "main", agent)
		assert.Equal(t, "what now?", input)
		assert.Nil(t, metadata)
		assert.Empty(t, attachments)
		assert.Equal(t, "#triage", slackChannel)

		return SessionResult{ExternalConversationID: externalConversationID, Agent: "main", Answer: "plain text reply"}, nil
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	result := callSessionPrompt(t, server.url, "", "", "main", "what now?", nil)
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "plain text reply", content.Text)
	assert.Equal(t, map[string]any{"external_conversation_id": "test-conversation", "agent": "main", "answer": "plain text reply"}, structuredContentMap(t, result))
}

func TestStartSessionPromptServerReturnsExternalConversationID(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, func(_ context.Context, _, externalConversationID, agent, input string, metadata map[string]string, _ []SessionAttachment, slackChannel string) (SessionResult, error) {
		assert.Equal(t, "test-conversation", externalConversationID)
		assert.Equal(t, "planner", agent)
		assert.Equal(t, "what now?", input)
		assert.Equal(t, map[string]string{"ticket-id": "123"}, metadata)
		assert.Equal(t, "#triage", slackChannel)

		return SessionResult{ExternalConversationID: "external_mcp:planner:abc", Agent: "planner", Answer: "planner reply"}, nil
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	result := callSessionPrompt(t, server.url, "", "", "planner", "what now?", map[string]string{"ticket-id": "123"})
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "planner reply", content.Text)
	assert.Equal(t, map[string]any{"external_conversation_id": "external_mcp:planner:abc", "agent": "planner", "answer": "planner reply"}, structuredContentMap(t, result))
}

func TestStartSessionPromptServerReturnsHandlerAgent(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, func(_ context.Context, _, externalConversationID, agent, input string, _ map[string]string, _ []SessionAttachment, _ string) (SessionResult, error) {
		assert.Equal(t, "external-1", externalConversationID)
		assert.Equal(t, "main", agent)
		assert.Equal(t, "what now?", input)

		return SessionResult{ExternalConversationID: externalConversationID, Agent: "planner", Answer: "planner reply"}, nil
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	result := callTool(t, server.url, "", "", map[string]any{"external_conversation_id": "external-1", "agent": "main", "input": "what now?", "slack_channel": "#triage"})
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "planner reply", content.Text)
	assert.Equal(t, map[string]any{"external_conversation_id": "external-1", "agent": "planner", "answer": "planner reply"}, structuredContentMap(t, result))
}

func TestStartSessionPromptServerPassesSlackChannel(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, func(_ context.Context, _, _, _, input string, _ map[string]string, _ []SessionAttachment, slackChannel string) (SessionResult, error) {
		assert.Equal(t, "what now?", input)
		assert.Equal(t, "#triage", slackChannel)

		return SessionResult{Answer: "plain text reply"}, nil
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	result := callTool(t, server.url, "", "", map[string]any{"external_conversation_id": "external-1", "agent": "main", "input": "what now?", "slack_channel": " #triage "})
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "plain text reply", content.Text)
}

func TestStartSessionPromptServerPassesAttachments(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, func(_ context.Context, _, _, _, input string, _ map[string]string, attachments []SessionAttachment, _ string) (SessionResult, error) {
		assert.Equal(t, "look", input)
		require.Len(t, attachments, 1)
		assert.Equal(t, "scorecard.png", attachments[0].Name)
		assert.Equal(t, "image/png", attachments[0].MIMEType)
		assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("png")), attachments[0].DataBase64)

		return SessionResult{Answer: "plain text reply"}, nil
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	result := callTool(t, server.url, "", "", map[string]any{
		"external_conversation_id": "external-1",
		"agent":                    "main",
		"input":                    "look",
		"slack_channel":            "#ops",
		"attachments": []map[string]any{{
			"name":        "scorecard.png",
			"mime_type":   "image/png",
			"data_base64": base64.StdEncoding.EncodeToString([]byte("png")),
		}},
	})
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "plain text reply", content.Text)
}

func TestStartSessionPromptServerReturnsAttachments(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, func(context.Context, string, string, string, string, map[string]string, []SessionAttachment, string) (SessionResult, error) {
		return SessionResult{ExternalConversationID: "test-conversation", Agent: "main", Answer: "plain text reply", Attachments: []SessionAttachment{
			{Name: "chart.png", MIMEType: "image/png; charset=binary", DataBase64: base64.StdEncoding.EncodeToString([]byte("png"))},
			{Name: "report.txt", MIMEType: "text/plain", DataBase64: base64.StdEncoding.EncodeToString([]byte("report"))},
		}}, nil
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	result := callSessionPrompt(t, server.url, "", "", "", "return attachments", nil)
	require.Len(t, result.Content, 3)
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "plain text reply", content.Text)

	image, ok := result.Content[1].(*mcp.ImageContent)
	require.True(t, ok)
	assert.Equal(t, "image/png", image.MIMEType)
	assert.Equal(t, []byte("png"), image.Data)

	resource, ok := result.Content[2].(*mcp.EmbeddedResource)
	require.True(t, ok)
	require.NotNil(t, resource.Resource)
	assert.Equal(t, "attachment://2/report.txt", resource.Resource.URI)
	assert.Equal(t, "text/plain", resource.Resource.MIMEType)
	assert.Equal(t, []byte("report"), resource.Resource.Blob)
	assert.Equal(t, map[string]any{"external_conversation_id": "test-conversation", "agent": "main", "answer": "plain text reply", "attachments": []any{map[string]any{"name": "chart.png", "mime_type": "image/png; charset=binary", "data_base64": base64.StdEncoding.EncodeToString([]byte("png"))}, map[string]any{"name": "report.txt", "mime_type": "text/plain", "data_base64": base64.StdEncoding.EncodeToString([]byte("report"))}}}, structuredContentMap(t, result))
}

func TestStartSessionPromptServerContinuesSession(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, func(_ context.Context, username, externalConversationID, agent, input string, metadata map[string]string, _ []SessionAttachment, slackChannel string) (SessionResult, error) {
		assert.Empty(t, username)
		assert.Equal(t, "planner", agent)
		assert.Equal(t, "external_mcp:planner:abc", externalConversationID)
		assert.Equal(t, "follow up", input)
		assert.Nil(t, metadata)
		assert.Equal(t, "#triage", slackChannel)

		return SessionResult{ExternalConversationID: externalConversationID, Agent: "planner", Answer: "continued reply"}, nil
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	result := callSessionPromptWithExternalConversationID(t, server.url, "external_mcp:planner:abc", "follow up", nil)
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "continued reply", content.Text)
	assert.Equal(t, map[string]any{"external_conversation_id": "external_mcp:planner:abc", "agent": "planner", "answer": "continued reply"}, structuredContentMap(t, result))
}

func TestStartSessionPromptServerExposesMetadataSchema(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, func(context.Context, string, string, string, string, map[string]string, []SessionAttachment, string) (SessionResult, error) {
		return SessionResult{}, nil
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	implementation := new(mcp.Implementation)
	implementation.Name = "test-client"
	implementation.Version = "1.0.0"
	client := mcp.NewClient(implementation, nil)
	transport := new(mcp.StreamableClientTransport)
	transport.Endpoint = server.url
	transport.DisableStandaloneSSE = true
	session, err := client.Connect(t.Context(), transport, nil)
	require.NoError(t, err)

	defer func() { require.NoError(t, session.Close()) }()

	tools, err := session.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)

	var sessionPromptTool *mcp.Tool

	for i := range tools.Tools {
		if tools.Tools[i].Name == SessionPromptToolName {
			sessionPromptTool = tools.Tools[i]
		}
	}

	require.NotNil(t, sessionPromptTool)

	schema, ok := sessionPromptTool.InputSchema.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"external_conversation_id", "input", "agent", "slack_channel"}, schema["required"])
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	metadata, ok := properties["metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "object", metadata["type"])

	_, ok = properties["external_conversation_id"].(map[string]any)
	assert.True(t, ok)

	_, ok = properties["input"].(map[string]any)
	assert.True(t, ok)
	_, ok = properties["slack_channel"].(map[string]any)
	assert.True(t, ok)

	attachments, ok := properties["attachments"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "array", attachments["type"])

	outputSchema, ok := sessionPromptTool.OutputSchema.(map[string]any)
	require.True(t, ok)
	required, ok := outputSchema["required"].([]any)
	require.True(t, ok)
	assert.ElementsMatch(t, []any{"external_conversation_id", "agent", "answer"}, required)
}

func TestStartSessionPromptServerUsesStatelessProtocol(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, func(_ context.Context, _, externalConversationID, agent, input string, _ map[string]string, _ []SessionAttachment, slackChannel string) (SessionResult, error) {
		assert.Equal(t, "external-1", externalConversationID)
		assert.Equal(t, "main", agent)
		assert.Equal(t, "hello", input)
		assert.Equal(t, "#ops", slackChannel)

		return SessionResult{ExternalConversationID: externalConversationID, Agent: agent, Answer: "reply"}, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: server.url, DisableStandaloneSSE: true}
	session, err := client.Connect(t.Context(), transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, session.Close()) })

	require.NotNil(t, session.InitializeResult())
	assert.Equal(t, "2026-07-28", session.InitializeResult().ProtocolVersion)
	assert.Empty(t, session.ID())

	tools, err := session.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)
	assert.Equal(t, SessionPromptToolName, tools.Tools[0].Name)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: SessionPromptToolName, Arguments: map[string]any{
		"external_conversation_id": "external-1",
		"agent":                    "main",
		"input":                    "hello",
		"slack_channel":            "#ops",
	}})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "reply", content.Text)
}

func TestStartSessionPromptServerRejectsMissingSlackChannel(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, func(context.Context, string, string, string, string, map[string]string, []SessionAttachment, string) (SessionResult, error) {
		t.Fatal("handler called without required Slack channel")
		return SessionResult{}, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	result := callTool(t, server.url, "", "", map[string]any{"external_conversation_id": "external-1", "agent": "main", "input": "hello", "slack_channel": nil})
	assert.Contains(t, result.Content[0].(*mcp.TextContent).Text, "slack_channel")
}

func TestStartSessionPromptServerRejectsBlankInputWithoutAttachments(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, func(context.Context, string, string, string, string, map[string]string, []SessionAttachment, string) (SessionResult, error) {
		t.Fatal("handler called for blank turn")
		return SessionResult{}, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	result := callTool(t, server.url, "", "", map[string]any{"external_conversation_id": "external-1", "agent": "main", "input": " \n\t", "slack_channel": "#ops"})
	assert.Contains(t, result.Content[0].(*mcp.TextContent).Text, "input or attachments")
}

func TestStartSessionPromptServerAcceptsAttachmentsWithoutInput(t *testing.T) {
	called := false
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, func(_ context.Context, _, _, _, input string, _ map[string]string, attachments []SessionAttachment, _ string) (SessionResult, error) {
		called = true

		assert.Empty(t, input)
		require.Len(t, attachments, 1)

		return SessionResult{Answer: "accepted"}, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	result := callTool(t, server.url, "", "", map[string]any{"external_conversation_id": "external-1", "agent": "main", "input": "", "slack_channel": "#ops", "attachments": []any{map[string]any{"data_base64": "eA=="}}})
	require.True(t, called)
	assert.Equal(t, "accepted", result.Content[0].(*mcp.TextContent).Text)
}

func TestStartSessionPromptServerRequiresBasicAuth(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"alice": "secret"}, func(_ context.Context, username, externalConversationID, agent, input string, metadata map[string]string, _ []SessionAttachment, slackChannel string) (SessionResult, error) {
		assert.Equal(t, "alice", username)
		assert.Equal(t, "test-conversation", externalConversationID)
		assert.Equal(t, "main", agent)
		assert.Equal(t, "what now?", input)
		assert.Nil(t, metadata)
		assert.Equal(t, "#triage", slackChannel)

		return SessionResult{Answer: "plain text reply"}, nil
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	body, err := json.Marshal([]map[string]any{{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test-client", "version": "1.0.0"},
		},
	}})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-03-26")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, `Basic realm="rocketclaw external mcp"`, resp.Header.Get("WWW-Authenticate"))

	result := callSessionPrompt(t, server.url, "alice", "secret", "main", "what now?", nil)
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "plain text reply", content.Text)
}

func TestServerAccessorsAndClose(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, func(context.Context, string, string, string, string, map[string]string, []SessionAttachment, string) (SessionResult, error) {
		return SessionResult{Answer: "plain text reply"}, nil
	})
	require.NoError(t, err)

	assert.NotEmpty(t, server.url)
	require.NoError(t, server.Close(context.Background()))
	require.NoError(t, server.Close(context.Background()))
}

func TestStartSessionPromptServerRejectsInvalidListenAddr(t *testing.T) {
	_, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "bad listen address", nil, func(context.Context, string, string, string, string, map[string]string, []SessionAttachment, string) (SessionResult, error) {
		return SessionResult{}, nil
	})
	require.ErrorContains(t, err, "listen for external MCP HTTP server")
}

func TestWithBasicAuthAllowsNilUsers(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	handler := withBasicAuth(next, nil)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	assert.True(t, called)
	assert.Nil(t, withBasicAuth(nil, map[string]string{"alice": "secret"}))
}

func callSessionPromptWithExternalConversationID(t *testing.T, endpoint, externalConversationID, input string, metadata map[string]string) *mcp.CallToolResult {
	t.Helper()

	args := map[string]any{"external_conversation_id": externalConversationID, "agent": "planner", "input": input, "slack_channel": "#triage"}
	if metadata != nil {
		args["metadata"] = metadata
	}

	return callTool(t, endpoint, "", "", args)
}

func callSessionPrompt(t *testing.T, endpoint, username, password, agent, input string, metadata map[string]string) *mcp.CallToolResult {
	t.Helper()

	if agent == "" {
		agent = "main"
	}

	args := map[string]any{"external_conversation_id": "test-conversation", "agent": agent, "input": input, "slack_channel": "#triage"}

	if metadata != nil {
		args["metadata"] = metadata
	}

	return callTool(t, endpoint, username, password, args)
}

func callTool(t *testing.T, endpoint, username, password string, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	if _, ok := args["slack_channel"]; !ok {
		args["slack_channel"] = "#triage"
	}

	implementation := new(mcp.Implementation)
	implementation.Name = "test-client"
	implementation.Version = "1.0.0"
	client := mcp.NewClient(implementation, nil)
	clientHTTP := new(http.Client)
	clientHTTP.Transport = basicAuthRoundTripper{base: http.DefaultTransport, username: username, password: password}
	transport := new(mcp.StreamableClientTransport)
	transport.Endpoint = endpoint
	transport.HTTPClient = clientHTTP
	transport.DisableStandaloneSSE = true
	session, err := client.Connect(t.Context(), transport, nil)
	require.NoError(t, err)

	defer func() { require.NoError(t, session.Close()) }()

	params := new(mcp.CallToolParams)
	params.Name = SessionPromptToolName
	params.Arguments = args
	result, err := session.CallTool(t.Context(), params)
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)

	return result
}

func structuredContentMap(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()

	data, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	var structured map[string]any
	require.NoError(t, json.Unmarshal(data, &structured))

	return structured
}

type basicAuthRoundTripper struct {
	base     http.RoundTripper
	username string
	password string
}

func (r basicAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if r.username != "" || r.password != "" {
		clone.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(r.username+":"+r.password)))
	}

	base := r.base
	if base == nil {
		base = http.DefaultTransport
	}

	resp, err := base.RoundTrip(clone)
	if err != nil {
		return nil, fmt.Errorf("send HTTP request: %w", err)
	}

	return resp, nil
}
