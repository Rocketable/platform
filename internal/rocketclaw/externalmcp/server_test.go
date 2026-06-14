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
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, "main", func(_ context.Context, username, externalConversationID, agent, input string, metadata map[string]string, attachments []SessionPromptAttachment, slackChannel string) (SessionResult, error) {
		assert.Empty(t, username)
		assert.Empty(t, externalConversationID)
		assert.Equal(t, "main", agent)
		assert.Equal(t, "what now?", input)
		assert.Nil(t, metadata)
		assert.Empty(t, attachments)
		assert.Empty(t, slackChannel)

		return SessionResult{Answer: "plain text reply"}, nil
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	result := callSessionPrompt(t, server.url, "", "", "", "what now?", nil)
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "plain text reply", content.Text)
	assert.Equal(t, map[string]any{"answer": "plain text reply"}, structuredContentMap(t, result))
}

func TestStartSessionPromptServerReturnsExternalConversationID(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, "main", func(_ context.Context, _, externalConversationID, agent, input string, metadata map[string]string, _ []SessionPromptAttachment, slackChannel string) (SessionResult, error) {
		assert.Empty(t, externalConversationID)
		assert.Equal(t, "planner", agent)
		assert.Equal(t, "what now?", input)
		assert.Equal(t, map[string]string{"ticket-id": "123"}, metadata)
		assert.Empty(t, slackChannel)

		return SessionResult{ExternalConversationID: "external_mcp:planner:abc", Answer: "planner reply"}, nil
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	result := callSessionPrompt(t, server.url, "", "", "planner", "what now?", map[string]string{"ticket-id": "123"})
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "planner reply", content.Text)
	assert.Equal(t, map[string]any{"external_conversation_id": "external_mcp:planner:abc", "answer": "planner reply"}, structuredContentMap(t, result))
}

func TestStartSessionPromptServerPassesSlackChannel(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, "main", func(_ context.Context, _, _, _, input string, _ map[string]string, _ []SessionPromptAttachment, slackChannel string) (SessionResult, error) {
		assert.Equal(t, "what now?", input)
		assert.Equal(t, "#triage", slackChannel)

		return SessionResult{Answer: "plain text reply"}, nil
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	result := callTool(t, server.url, "", "", map[string]any{"input": "what now?", "slack_channel": " #triage "})
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "plain text reply", content.Text)
}

func TestStartSessionPromptServerPassesAttachments(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, "main", func(_ context.Context, _, _, _, input string, _ map[string]string, attachments []SessionPromptAttachment, _ string) (SessionResult, error) {
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
		"input": "look",
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
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, "main", func(context.Context, string, string, string, string, map[string]string, []SessionPromptAttachment, string) (SessionResult, error) {
		return SessionResult{Answer: "plain text reply", Attachments: []SessionAttachment{
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
	assert.Equal(t, map[string]any{"answer": "plain text reply", "attachments": []any{map[string]any{"name": "chart.png", "mime_type": "image/png; charset=binary", "data_base64": base64.StdEncoding.EncodeToString([]byte("png"))}, map[string]any{"name": "report.txt", "mime_type": "text/plain", "data_base64": base64.StdEncoding.EncodeToString([]byte("report"))}}}, structuredContentMap(t, result))
}

func TestStartSessionPromptServerContinuesSession(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, "main", func(_ context.Context, username, externalConversationID, agent, input string, metadata map[string]string, _ []SessionPromptAttachment, slackChannel string) (SessionResult, error) {
		assert.Empty(t, username)
		assert.Empty(t, agent)
		assert.Equal(t, "external_mcp:planner:abc", externalConversationID)
		assert.Equal(t, "follow up", input)
		assert.Nil(t, metadata)
		assert.Empty(t, slackChannel)

		return SessionResult{ExternalConversationID: externalConversationID, Answer: "continued reply"}, nil
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	result := callSessionPromptWithExternalConversationID(t, server.url, "external_mcp:planner:abc", "follow up", nil)
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "continued reply", content.Text)
	assert.Equal(t, map[string]any{"external_conversation_id": "external_mcp:planner:abc", "answer": "continued reply"}, structuredContentMap(t, result))
}

func TestStartSessionPromptServerExposesMetadataSchema(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, "main", func(context.Context, string, string, string, string, map[string]string, []SessionPromptAttachment, string) (SessionResult, error) {
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
	assert.Equal(t, []any{"input"}, schema["required"])
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
}

func TestStartSessionPromptServerRequiresBasicAuth(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"alice": "secret"}, "main", func(_ context.Context, username, externalConversationID, agent, input string, metadata map[string]string, _ []SessionPromptAttachment, slackChannel string) (SessionResult, error) {
		assert.Equal(t, "alice", username)
		assert.Empty(t, externalConversationID)
		assert.Equal(t, "main", agent)
		assert.Equal(t, "what now?", input)
		assert.Nil(t, metadata)
		assert.Empty(t, slackChannel)

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

	result := callSessionPrompt(t, server.url, "alice", "secret", "", "what now?", nil)
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "plain text reply", content.Text)
}

func TestServerAccessorsAndClose(t *testing.T) {
	server, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", nil, "main", func(context.Context, string, string, string, string, map[string]string, []SessionPromptAttachment, string) (SessionResult, error) {
		return SessionResult{Answer: "plain text reply"}, nil
	})
	require.NoError(t, err)

	assert.NotEmpty(t, server.url)
	require.NoError(t, server.Stop(context.Background()))
	require.NoError(t, server.Close(context.Background()))
}

func TestStartSessionPromptServerRejectsInvalidListenAddr(t *testing.T) {
	_, err := StartSessionPromptServer(t.Context(), slog.New(slog.DiscardHandler), "bad listen address", nil, "main", func(context.Context, string, string, string, string, map[string]string, []SessionPromptAttachment, string) (SessionResult, error) {
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

	args := map[string]any{"external_conversation_id": externalConversationID, "input": input}
	if metadata != nil {
		args["metadata"] = metadata
	}

	return callTool(t, endpoint, "", "", args)
}

func callSessionPrompt(t *testing.T, endpoint, username, password, agent, input string, metadata map[string]string) *mcp.CallToolResult {
	t.Helper()

	args := map[string]any{"input": input}
	if agent != "" {
		args["agent"] = agent
	}

	if metadata != nil {
		args["metadata"] = metadata
	}

	return callTool(t, endpoint, username, password, args)
}

func callTool(t *testing.T, endpoint, username, password string, args map[string]any) *mcp.CallToolResult {
	t.Helper()

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
