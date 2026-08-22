package developmentmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/agentlint"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

func TestStartRequiresBasicAuth(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	unauthStatus, unauthRealm := postInitialize(t, server.URL(), "", "")
	assert.Equal(t, http.StatusUnauthorized, unauthStatus)
	assert.Equal(t, `Basic realm="rocketclaw development mcp"`, unauthRealm)

	wrongStatus, _ := postInitialize(t, server.URL(), "dev", "wrong")
	assert.Equal(t, http.StatusUnauthorized, wrongStatus)

	externalStatus, _ := postInitialize(t, server.URL(), "admin", "secret")
	assert.Equal(t, http.StatusUnauthorized, externalStatus)

	okStatus, _ := postInitialize(t, server.URL(), "dev", "token")
	assert.NotEqual(t, http.StatusUnauthorized, okStatus)
}

func TestStartRejectsEmptyUsers(t *testing.T) {
	for _, users := range []map[string]string{nil, {}} {
		_, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", users, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart)
		require.ErrorContains(t, err, "development MCP users are required")
	}
}

func TestStartRejectsInvalidListenAddr(t *testing.T) {
	_, err := Start(t.Context(), slog.New(slog.DiscardHandler), "bad listen address", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart)
	require.ErrorContains(t, err, "listen for development MCP HTTP server")
}

func TestServerCloseIsIdempotent(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart)
	require.NoError(t, err)
	assert.NotEmpty(t, server.URL())
	require.NoError(t, server.Close(context.Background()))
	require.NoError(t, server.Close(context.Background()))
}

func postInitialize(t *testing.T, endpoint, username, password string) (status int, wwwAuthenticate string) {
	t.Helper()

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

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-03-26")

	if username != "" || password != "" {
		req.SetBasicAuth(username, password)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	return resp.StatusCode, resp.Header.Get("WWW-Authenticate")
}

func TestListOverlayReturnsConfiguredSpecsInOrder(t *testing.T) {
	for _, tt := range []struct {
		name  string
		specs []string
	}{
		{name: "configured order", specs: []string{"github.com/rocketable/overlay@main", "github.com/rocketable/other"}},
		{name: "none configured", specs: []string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, tt.specs, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

			assert.Equal(t, tt.specs, listOverlay(t, server.URL(), "dev", "token"))
		})
	}
}

func TestReadContextFromOverlayKnownSpec(t *testing.T) {
	spec := "github.com/rocketable/overlay@main"
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, func(got string) (skel.OverlayContext, error) {
		assert.Equal(t, spec, got)

		return skel.OverlayContext{
			BaseOverlay: spec,
			Files: []skel.OverlayFile{
				{Path: "agents/a.md", Content: "agent"},
				{Path: "skills/pack/SKILL.md", Content: "skill"},
			},
		}, nil
	}, unusedLint, unusedRunTurn, unusedReload, unusedRestart)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	result := callTool(t, server.URL(), "dev", "token", readContextFromOverlayToolName, map[string]any{"overlay": spec})
	require.False(t, result.IsError)

	data, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	var got overlayContext
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, overlayContext{
		BaseOverlay: spec,
		Files: []overlayFile{
			{Path: "agents/a.md", Content: "agent"},
			{Path: "skills/pack/SKILL.md", Content: "skill"},
		},
	}, got)
}

func TestReadContextFromOverlayUnknownSpec(t *testing.T) {
	spec := "github.com/other/overlay"
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, func(got string) (skel.OverlayContext, error) {
		return skel.OverlayContext{}, fmt.Errorf("unknown overlay spec %q", got)
	}, unusedLint, unusedRunTurn, unusedReload, unusedRestart)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	result := callTool(t, server.URL(), "dev", "token", readContextFromOverlayToolName, map[string]any{"overlay": spec})
	require.True(t, result.IsError)
	require.NotEmpty(t, result.Content)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, `unknown overlay spec "github.com/other/overlay"`)
}

func unusedReadOverlayContext(string) (skel.OverlayContext, error) {
	return skel.OverlayContext{}, errors.New("read overlay context unused")
}

func unusedLint(string, []skel.OverlayFile) (agentlint.Result, error) {
	return agentlint.Result{}, errors.New("lint unused")
}

func unusedRunTurn(context.Context, string, []skel.OverlayFile, string, string, string) (thinking, answer string, err error) {
	return "", "", errors.New("run turn unused")
}

func unusedReload(string) (string, error) {
	return "", errors.New("reload unused")
}

func unusedRestart(string) (string, error) {
	return "", errors.New("restart unused")
}

func TestLintRequiresContext(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	result := callTool(t, server.URL(), "dev", "token", lintToolName, map[string]any{})
	require.True(t, result.IsError)
	require.NotEmpty(t, result.Content)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, "context")
}

func TestLintAE1NamedBaseOverlay(t *testing.T) {
	var (
		gotBase  string
		gotFiles []skel.OverlayFile
	)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, func(baseOverlay string, files []skel.OverlayFile) (agentlint.Result, error) {
		gotBase, gotFiles = baseOverlay, files

		return agentlint.Result{Findings: []agentlint.Finding{{Code: "RC003", Severity: "error", Path: "agents/a.md", Message: "cycle"}}}, nil
	}, unusedRunTurn, unusedReload, unusedRestart)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	result := callTool(t, server.URL(), "dev", "token", lintToolName, map[string]any{
		"context": map[string]any{
			"base_overlay": "skills",
			"files":        []map[string]any{{"path": "agents/a.md", "content": "new a"}},
		},
	})
	require.False(t, result.IsError)
	assert.Equal(t, "skills", gotBase)
	assert.Equal(t, []skel.OverlayFile{{Path: "agents/a.md", Content: "new a"}}, gotFiles)
	assert.Equal(t, []lintFinding{{Code: "RC003", Severity: "error", Path: "agents/a.md", Message: "cycle"}}, decodeLintFindings(t, result))
}

func TestLintAE2NoBaseOverlay(t *testing.T) {
	var (
		gotBase  string
		gotFiles []skel.OverlayFile
	)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, func(baseOverlay string, files []skel.OverlayFile) (agentlint.Result, error) {
		gotBase, gotFiles = baseOverlay, files

		return agentlint.Result{}, nil
	}, unusedRunTurn, unusedReload, unusedRestart)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	result := callTool(t, server.URL(), "dev", "token", lintToolName, map[string]any{
		"context": map[string]any{
			"files": []map[string]any{{"path": "agents/a.md", "content": "only a"}},
		},
	})
	require.False(t, result.IsError)
	assert.Empty(t, gotBase)
	assert.Equal(t, []skel.OverlayFile{{Path: "agents/a.md", Content: "only a"}}, gotFiles)
	assert.Empty(t, decodeLintFindings(t, result))
}

func TestLintDoesNotRememberContext(t *testing.T) {
	var (
		gotBase  string
		gotFiles []skel.OverlayFile
	)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, func(baseOverlay string, files []skel.OverlayFile) (agentlint.Result, error) {
		gotBase, gotFiles = baseOverlay, files

		return agentlint.Result{}, nil
	}, unusedRunTurn, unusedReload, unusedRestart)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	first := callTool(t, server.URL(), "dev", "token", lintToolName, map[string]any{
		"context": map[string]any{
			"base_overlay": "skills",
			"files":        []map[string]any{{"path": "agents/a.md", "content": "first"}},
		},
	})
	require.False(t, first.IsError)

	second := callTool(t, server.URL(), "dev", "token", lintToolName, map[string]any{
		"context": map[string]any{
			"files": []map[string]any{{"path": "agents/b.md", "content": "second"}},
		},
	})
	require.False(t, second.IsError)
	assert.Empty(t, gotBase)
	assert.Equal(t, []skel.OverlayFile{{Path: "agents/b.md", Content: "second"}}, gotFiles)
}

func TestReloadAE4CallsInjectedClosureWithReason(t *testing.T) {
	var gotReason string

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, func(reason string) (string, error) {
		gotReason = reason

		return "reloaded published overlay", nil
	}, unusedRestart)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	result := callTool(t, server.URL(), "dev", "token", reloadToolName, map[string]any{"reason": "picked up published overlay"})
	require.False(t, result.IsError)
	assert.Equal(t, "picked up published overlay", gotReason)

	data, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	var got string
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "reloaded published overlay", got)
}

func TestRestartCallsInjectedClosureWithReason(t *testing.T) {
	var gotReason string

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, func(reason string) (string, error) {
		gotReason = reason

		return "restarted for overlay-list change", nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	result := callTool(t, server.URL(), "dev", "token", restartToolName, map[string]any{"reason": "overlay list changed"})
	require.False(t, result.IsError)
	assert.Equal(t, "overlay list changed", gotReason)

	data, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	var got string
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "restarted for overlay-list change", got)
}

func TestRunTurnEmptyConversationID(t *testing.T) {
	var (
		gotBase           string
		gotFiles          []skel.OverlayFile
		gotAgent          string
		gotPrompt         string
		gotConversationID string
	)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, func(_ context.Context, baseOverlay string, files []skel.OverlayFile, agent, prompt, conversationID string) (string, string, error) {
		gotBase, gotFiles, gotAgent, gotPrompt, gotConversationID = baseOverlay, files, agent, prompt, conversationID

		return "stub thinking", "stub answer", nil
	}, unusedReload, unusedRestart)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	result := callTool(t, server.URL(), "dev", "token", runTurnToolName, map[string]any{
		"context": map[string]any{
			"base_overlay": "skills",
			"files":        []map[string]any{{"path": "agents/a.md", "content": "new a"}},
		},
		"agent":           "main",
		"prompt":          "try this overlay",
		"conversation_id": "",
	})
	require.False(t, result.IsError)

	data, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	var got runTurnOutput
	require.NoError(t, json.Unmarshal(data, &got))
	assert.True(t, strings.HasPrefix(got.ConversationID, "devmcp-"))
	assert.NotEmpty(t, strings.TrimPrefix(got.ConversationID, "devmcp-"))
	assert.Equal(t, "stub thinking", got.Thinking)
	assert.Equal(t, "stub answer", got.Answer)
	assert.Equal(t, "skills", gotBase)
	assert.Equal(t, []skel.OverlayFile{{Path: "agents/a.md", Content: "new a"}}, gotFiles)
	assert.Equal(t, "main", gotAgent)
	assert.Equal(t, "try this overlay", gotPrompt)
	assert.Equal(t, got.ConversationID, gotConversationID)
}

func TestRunTurnAE3FollowUpUsesThisCallContext(t *testing.T) {
	var calls []runTurnCall

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, func(_ context.Context, baseOverlay string, files []skel.OverlayFile, _, _, conversationID string) (string, string, error) {
		calls = append(calls, runTurnCall{base: baseOverlay, files: files, conversationID: conversationID})

		return "thinking", "answer", nil
	}, unusedReload, unusedRestart)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	first := decodeRunTurn(t, callTool(t, server.URL(), "dev", "token", runTurnToolName, map[string]any{
		"context": map[string]any{
			"base_overlay": "skills",
			"files":        []map[string]any{{"path": "agents/a.md", "content": "first a"}},
		},
		"agent":           "main",
		"prompt":          "first",
		"conversation_id": "",
	}))
	require.True(t, strings.HasPrefix(first.ConversationID, "devmcp-"))

	second := decodeRunTurn(t, callTool(t, server.URL(), "dev", "token", runTurnToolName, map[string]any{
		"context": map[string]any{
			"base_overlay": "other",
			"files":        []map[string]any{{"path": "agents/b.md", "content": "second b"}},
		},
		"agent":           "main",
		"prompt":          "follow up",
		"conversation_id": first.ConversationID,
	}))
	assert.Equal(t, first.ConversationID, second.ConversationID)
	require.Len(t, calls, 2)
	assert.Equal(t, first.ConversationID, calls[0].conversationID)
	assert.Equal(t, first.ConversationID, calls[1].conversationID)
	assert.Equal(t, "skills", calls[0].base)
	assert.Equal(t, []skel.OverlayFile{{Path: "agents/a.md", Content: "first a"}}, calls[0].files)
	assert.Equal(t, "other", calls[1].base)
	assert.Equal(t, []skel.OverlayFile{{Path: "agents/b.md", Content: "second b"}}, calls[1].files)
}

func TestRunTurnAE5FollowUpEmptyFilesUsesThisCallContext(t *testing.T) {
	var calls []runTurnCall

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, func(_ context.Context, baseOverlay string, files []skel.OverlayFile, _, _, conversationID string) (string, string, error) {
		calls = append(calls, runTurnCall{base: baseOverlay, files: files, conversationID: conversationID})

		return "thinking", "answer", nil
	}, unusedReload, unusedRestart)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	first := decodeRunTurn(t, callTool(t, server.URL(), "dev", "token", runTurnToolName, map[string]any{
		"context": map[string]any{
			"base_overlay": "skills",
			"files":        []map[string]any{{"path": "agents/a.md", "content": "new a"}},
		},
		"agent":           "main",
		"prompt":          "first",
		"conversation_id": "",
	}))

	second := decodeRunTurn(t, callTool(t, server.URL(), "dev", "token", runTurnToolName, map[string]any{
		"context": map[string]any{
			"base_overlay": "skills",
			"files":        []map[string]any{},
		},
		"agent":           "main",
		"prompt":          "follow up",
		"conversation_id": first.ConversationID,
	}))
	assert.Equal(t, first.ConversationID, second.ConversationID)
	require.Len(t, calls, 2)
	assert.Equal(t, first.ConversationID, calls[1].conversationID)
	assert.Equal(t, "skills", calls[1].base)
	assert.Empty(t, calls[1].files)
	assert.Equal(t, []skel.OverlayFile{{Path: "agents/a.md", Content: "new a"}}, calls[0].files)
}

type runTurnCall struct {
	base           string
	files          []skel.OverlayFile
	conversationID string
}

func decodeRunTurn(t *testing.T, result *mcp.CallToolResult) runTurnOutput {
	t.Helper()
	require.False(t, result.IsError)

	data, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	var got runTurnOutput
	require.NoError(t, json.Unmarshal(data, &got))

	return got
}

func TestRunTurnRequiresContext(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	result := callTool(t, server.URL(), "dev", "token", runTurnToolName, map[string]any{
		"agent":           "main",
		"prompt":          "try this overlay",
		"conversation_id": "",
	})
	require.True(t, result.IsError)
	require.NotEmpty(t, result.Content)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, "context")
}

func TestReloadAndRestartRejectContext(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	for _, tt := range []struct {
		name    string
		tool    string
		wantErr string
	}{
		{name: "reload", tool: reloadToolName, wantErr: "reload does not take context"},
		{name: "restart", tool: restartToolName, wantErr: "restart does not take context"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := callTool(t, server.URL(), "dev", "token", tt.tool, map[string]any{
				"reason":  "should not run",
				"context": map[string]any{"files": []map[string]any{{"path": "agents/a.md", "content": "a"}}},
			})
			require.True(t, result.IsError)
			require.NotEmpty(t, result.Content)
			text, ok := result.Content[0].(*mcp.TextContent)
			require.True(t, ok)
			assert.Contains(t, text.Text, tt.wantErr)
		})
	}
}

func decodeLintFindings(t *testing.T, result *mcp.CallToolResult) []lintFinding {
	t.Helper()

	data, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	var findings []lintFinding
	require.NoError(t, json.Unmarshal(data, &findings))

	return findings
}

func listOverlay(t *testing.T, endpoint, username, password string) []string {
	t.Helper()

	data, err := json.Marshal(callTool(t, endpoint, username, password, listOverlayToolName, map[string]any{}).StructuredContent)
	require.NoError(t, err)

	var specs []string
	require.NoError(t, json.Unmarshal(data, &specs))

	return specs
}

func callTool(t *testing.T, endpoint, username, password, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	implementation := new(mcp.Implementation)
	implementation.Name = "test-client"
	implementation.Version = "1.0.0"
	client := mcp.NewClient(implementation, nil)
	transport := new(mcp.StreamableClientTransport)
	transport.Endpoint = endpoint
	transport.HTTPClient = &http.Client{Transport: basicAuthRoundTripper{base: http.DefaultTransport, username: username, password: password}}
	transport.DisableStandaloneSSE = true
	session, err := client.Connect(t.Context(), transport, nil)

	require.NoError(t, err)
	defer func() { require.NoError(t, session.Close()) }()

	params := new(mcp.CallToolParams)
	params.Name = name
	params.Arguments = args
	result, err := session.CallTool(t.Context(), params)
	require.NoError(t, err)

	return result
}

type basicAuthRoundTripper struct {
	base     http.RoundTripper
	username string
	password string
}

func (r basicAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.SetBasicAuth(r.username, r.password)

	resp, err := r.base.RoundTrip(clone)
	if err != nil {
		return nil, fmt.Errorf("send HTTP request: %w", err)
	}

	return resp, nil
}
