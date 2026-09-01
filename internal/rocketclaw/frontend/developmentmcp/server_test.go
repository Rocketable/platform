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
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func TestStartRequiresBasicAuth(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
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
		_, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", users, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
		require.ErrorContains(t, err, "development MCP users are required")
	}
}

func TestStartRejectsInvalidListenAddr(t *testing.T) {
	_, err := Start(t.Context(), slog.New(slog.DiscardHandler), "bad listen address", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
	require.ErrorContains(t, err, "listen for development MCP HTTP server")
}

func TestServerCloseIsIdempotent(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
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
			server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, tt.specs, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

			assert.Equal(t, tt.specs, listOverlay(t, server.URL(), "dev", "token"))
		})
	}
}

func TestReadContextFromOverlayKnownSpec(t *testing.T) {
	spec := "github.com/rocketable/overlay@main"
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, func(got string) (protocol.OverlayContext, error) {
		assert.Equal(t, spec, got)

		return protocol.OverlayContext{
			BaseOverlay: spec,
			Files: []protocol.OverlayFile{
				{Path: "agents/a.md", Content: "agent"},
				{Path: "skills/pack/SKILL.md", Content: "skill"},
			},
		}, nil
	}, unusedLint, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
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
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, func(got string) (protocol.OverlayContext, error) {
		return protocol.OverlayContext{}, fmt.Errorf("unknown overlay spec %q", got)
	}, unusedLint, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	result := callTool(t, server.URL(), "dev", "token", readContextFromOverlayToolName, map[string]any{"overlay": spec})
	require.True(t, result.IsError)
	require.NotEmpty(t, result.Content)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, `unknown overlay spec "github.com/other/overlay"`)
}

func unusedReadOverlayContext(string) (protocol.OverlayContext, error) {
	return protocol.OverlayContext{}, errors.New("read overlay context unused")
}

func unusedLint(string, []protocol.OverlayFile) (protocol.LintResult, error) {
	return protocol.LintResult{}, errors.New("lint unused")
}

func unusedRunTurn(context.Context, string, []protocol.OverlayFile, string, string, string) (thinking, answer string, err error) {
	return "", "", errors.New("run turn unused")
}

func unusedReload(string) (string, error) {
	return "", errors.New("reload unused")
}

func unusedRestart(string) (string, error) {
	return "", errors.New("restart unused")
}

func unusedListSessions(context.Context, protocol.ListSessionsRequest) (protocol.ListSessionsResult, error) {
	return protocol.ListSessionsResult{}, errors.New("list sessions unused")
}

func unusedObserveSession(context.Context, protocol.ObserveSessionRequest) (protocol.ObserveSessionResult, error) {
	return protocol.ObserveSessionResult{}, errors.New("observe session unused")
}

func unusedDeleteSession(context.Context, protocol.DeleteSessionRequest) (protocol.DeleteSessionResult, error) {
	return protocol.DeleteSessionResult{}, errors.New("delete session unused")
}

func TestLintRequiresContext(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
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
		gotFiles []protocol.OverlayFile
	)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, func(baseOverlay string, files []protocol.OverlayFile) (protocol.LintResult, error) {
		gotBase, gotFiles = baseOverlay, files

		return protocol.LintResult{Findings: []protocol.LintFinding{{Code: "RC003", Severity: "error", Path: "agents/a.md", Message: "cycle"}}}, nil
	}, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
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
	assert.Equal(t, []protocol.OverlayFile{{Path: "agents/a.md", Content: "new a"}}, gotFiles)
	assert.Equal(t, []lintFinding{{Code: "RC003", Severity: "error", Path: "agents/a.md", Message: "cycle"}}, decodeLintFindings(t, result))
}

func TestLintAE2NoBaseOverlay(t *testing.T) {
	var (
		gotBase  string
		gotFiles []protocol.OverlayFile
	)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, func(baseOverlay string, files []protocol.OverlayFile) (protocol.LintResult, error) {
		gotBase, gotFiles = baseOverlay, files

		return protocol.LintResult{}, nil
	}, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	result := callTool(t, server.URL(), "dev", "token", lintToolName, map[string]any{
		"context": map[string]any{
			"files": []map[string]any{{"path": "agents/a.md", "content": "only a"}},
		},
	})
	require.False(t, result.IsError)
	assert.Empty(t, gotBase)
	assert.Equal(t, []protocol.OverlayFile{{Path: "agents/a.md", Content: "only a"}}, gotFiles)
	assert.Empty(t, decodeLintFindings(t, result))
}

func TestLintDoesNotRememberContext(t *testing.T) {
	var (
		gotBase  string
		gotFiles []protocol.OverlayFile
	)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, func(baseOverlay string, files []protocol.OverlayFile) (protocol.LintResult, error) {
		gotBase, gotFiles = baseOverlay, files

		return protocol.LintResult{}, nil
	}, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
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
	assert.Equal(t, []protocol.OverlayFile{{Path: "agents/b.md", Content: "second"}}, gotFiles)
}

func TestReloadAE4CallsInjectedClosureWithReason(t *testing.T) {
	var gotReason string

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, func(reason string) (string, error) {
		gotReason = reason

		return "reloaded published overlay", nil
	}, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
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
	}, unusedListSessions, unusedObserveSession, unusedDeleteSession)
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
		gotFiles          []protocol.OverlayFile
		gotAgent          string
		gotPrompt         string
		gotConversationID string
	)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, func(_ context.Context, baseOverlay string, files []protocol.OverlayFile, agent, prompt, conversationID string) (string, string, error) {
		gotBase, gotFiles, gotAgent, gotPrompt, gotConversationID = baseOverlay, files, agent, prompt, conversationID

		return "stub thinking", "stub answer", nil
	}, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
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
	assert.Equal(t, []protocol.OverlayFile{{Path: "agents/a.md", Content: "new a"}}, gotFiles)
	assert.Equal(t, "main", gotAgent)
	assert.Equal(t, "try this overlay", gotPrompt)
	assert.Equal(t, got.ConversationID, gotConversationID)
}

func TestRunTurnAE3FollowUpUsesThisCallContext(t *testing.T) {
	var calls []runTurnCall

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, func(_ context.Context, baseOverlay string, files []protocol.OverlayFile, _, _, conversationID string) (string, string, error) {
		calls = append(calls, runTurnCall{base: baseOverlay, files: files, conversationID: conversationID})

		return "thinking", "answer", nil
	}, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
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
	assert.Equal(t, []protocol.OverlayFile{{Path: "agents/a.md", Content: "first a"}}, calls[0].files)
	assert.Equal(t, "other", calls[1].base)
	assert.Equal(t, []protocol.OverlayFile{{Path: "agents/b.md", Content: "second b"}}, calls[1].files)
}

func TestRunTurnAE5FollowUpEmptyFilesUsesThisCallContext(t *testing.T) {
	var calls []runTurnCall

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, func(_ context.Context, baseOverlay string, files []protocol.OverlayFile, _, _, conversationID string) (string, string, error) {
		calls = append(calls, runTurnCall{base: baseOverlay, files: files, conversationID: conversationID})

		return "thinking", "answer", nil
	}, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
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
	assert.Equal(t, []protocol.OverlayFile{{Path: "agents/a.md", Content: "new a"}}, calls[0].files)
}

type runTurnCall struct {
	base           string
	files          []protocol.OverlayFile
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
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
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
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, unusedDeleteSession)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	for _, tt := range []struct {
		name    string
		tool    string
		wantErr string
	}{
		{name: "reload", tool: reloadToolName, wantErr: "reload does not take context"},
		{name: "restart", tool: restartToolName, wantErr: "restart does not take context"},
		{name: "list", tool: listSessionToolName, wantErr: "list session does not take overlay context"},
		{name: "observe", tool: observeSessionToolName, wantErr: "observe session does not take overlay context"},
		{name: "delete", tool: deleteSessionToolName, wantErr: "delete session does not take overlay context"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{
				"reason":          "should not run",
				"conversation_id": "main",
				"context":         map[string]any{"files": []map[string]any{{"path": "agents/a.md", "content": "a"}}},
			}
			result := callTool(t, server.URL(), "dev", "token", tt.tool, args)
			require.True(t, result.IsError)
			require.NotEmpty(t, result.Content)
			text, ok := result.Content[0].(*mcp.TextContent)
			require.True(t, ok)
			assert.Contains(t, text.Text, tt.wantErr)
		})
	}
}

func TestListSessionMapsFilters(t *testing.T) {
	until := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)

	var got protocol.ListSessionsRequest

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart, func(_ context.Context, req protocol.ListSessionsRequest) (protocol.ListSessionsResult, error) {
		got = req

		return protocol.ListSessionsResult{Sessions: []protocol.SessionSummary{{
			ConversationID: "main", Turns: 2, LastUpdated: until,
			LastUserMessage: "user", LastAssistantMessage: "assistant",
		}}}, nil
	}, unusedObserveSession, unusedDeleteSession)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	before := time.Now().UTC()
	result := callTool(t, server.URL(), "dev", "token", listSessionToolName, map[string]any{
		"since": "24h",
		"until": until.Format(time.RFC3339),
		"limit": 2,
	})
	after := time.Now().UTC()

	require.False(t, result.IsError)
	assert.Equal(t, 2, got.Limit)
	assert.False(t, got.OmitPreview)
	assert.Equal(t, until, got.Until)
	assert.False(t, got.Since.IsZero())
	assert.False(t, got.Since.Before(before.Add(-24*time.Hour-time.Second)))
	assert.False(t, got.Since.After(after.Add(-24*time.Hour+time.Second)))

	summaries := decodeListSessions(t, result)
	require.Len(t, summaries, 1)
	assert.Equal(t, "user", summaries[0].LastUserMessage)
	assert.Equal(t, "assistant", summaries[0].LastAssistantMessage)

	previewOff := false
	result = callTool(t, server.URL(), "dev", "token", listSessionToolName, map[string]any{
		"include_message_preview": previewOff,
	})
	require.False(t, result.IsError)
	assert.True(t, got.OmitPreview)
	summaries = decodeListSessions(t, result)
	require.Len(t, summaries, 1)
	assert.Empty(t, summaries[0].LastUserMessage)
	assert.Empty(t, summaries[0].LastAssistantMessage)
}

func TestObserveSessionReturnsSnapshot(t *testing.T) {
	entry := json.RawMessage(`{"type":"turn"}`)

	var calls int

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, func(_ context.Context, req protocol.ObserveSessionRequest) (protocol.ObserveSessionResult, error) {
		calls++

		assert.Equal(t, "main", req.ConversationID)

		return protocol.ObserveSessionResult{Entries: []json.RawMessage{entry}}, nil
	}, unusedDeleteSession)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	first := decodeObserveSessions(t, callTool(t, server.URL(), "dev", "token", observeSessionToolName, map[string]any{"conversation_id": "main"}))
	second := decodeObserveSessions(t, callTool(t, server.URL(), "dev", "token", observeSessionToolName, map[string]any{"conversation_id": "main"}))
	assert.Equal(t, 2, calls)
	assert.Equal(t, []json.RawMessage{entry}, first)
	assert.Equal(t, first, second)
}

func TestObserveAndDeleteMissingAndTryTurn(t *testing.T) {
	var (
		observed []string
		deleted  []string
	)

	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart, func(context.Context, protocol.ListSessionsRequest) (protocol.ListSessionsResult, error) {
		return protocol.ListSessionsResult{Sessions: []protocol.SessionSummary{{ConversationID: "main", Turns: 1}}}, nil
	}, func(_ context.Context, req protocol.ObserveSessionRequest) (protocol.ObserveSessionResult, error) {
		observed = append(observed, req.ConversationID)
		return protocol.ObserveSessionResult{}, nil
	}, func(_ context.Context, req protocol.DeleteSessionRequest) (protocol.DeleteSessionResult, error) {
		deleted = append(deleted, req.ConversationID)
		return protocol.DeleteSessionResult{}, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	summaries := decodeListSessions(t, callTool(t, server.URL(), "dev", "token", listSessionToolName, map[string]any{}))
	require.Len(t, summaries, 1)
	assert.Equal(t, "main", summaries[0].ConversationID)

	assert.Empty(t, decodeObserveSessions(t, callTool(t, server.URL(), "dev", "token", observeSessionToolName, map[string]any{"conversation_id": "missing"})))
	assert.Empty(t, decodeObserveSessions(t, callTool(t, server.URL(), "dev", "token", observeSessionToolName, map[string]any{"conversation_id": "devmcp-try"})))
	assert.Empty(t, decodeObserveSessions(t, callTool(t, server.URL(), "dev", "token", observeSessionToolName, map[string]any{})))
	assert.Equal(t, []string{"missing", "devmcp-try"}, observed)

	assert.Equal(t, int64(0), decodeDeleteSession(t, callTool(t, server.URL(), "dev", "token", deleteSessionToolName, map[string]any{"conversation_id": "missing"})))
	assert.Equal(t, int64(0), decodeDeleteSession(t, callTool(t, server.URL(), "dev", "token", deleteSessionToolName, map[string]any{"conversation_id": "devmcp-try"})))
	assert.Equal(t, int64(0), decodeDeleteSession(t, callTool(t, server.URL(), "dev", "token", deleteSessionToolName, map[string]any{})))
	assert.Equal(t, []string{"missing", "devmcp-try"}, deleted)
}

func TestDeleteSessionReturnsDeletedCount(t *testing.T) {
	server, err := Start(t.Context(), slog.New(slog.DiscardHandler), "127.0.0.1:0", map[string]string{"dev": "token"}, nil, unusedReadOverlayContext, unusedLint, unusedRunTurn, unusedReload, unusedRestart, unusedListSessions, unusedObserveSession, func(_ context.Context, req protocol.DeleteSessionRequest) (protocol.DeleteSessionResult, error) {
		assert.Equal(t, "main", req.ConversationID)
		return protocol.DeleteSessionResult{Deleted: 3}, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close(context.Background())) })

	assert.Equal(t, int64(3), decodeDeleteSession(t, callTool(t, server.URL(), "dev", "token", deleteSessionToolName, map[string]any{"conversation_id": "main"})))
}

func decodeListSessions(t *testing.T, result *mcp.CallToolResult) []listSessionSummary {
	t.Helper()
	require.False(t, result.IsError)

	data, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	var summaries []listSessionSummary
	require.NoError(t, json.Unmarshal(data, &summaries))

	return summaries
}

func decodeObserveSessions(t *testing.T, result *mcp.CallToolResult) []json.RawMessage {
	t.Helper()
	require.False(t, result.IsError)

	data, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	var entries []json.RawMessage
	require.NoError(t, json.Unmarshal(data, &entries))

	return entries
}

func decodeDeleteSession(t *testing.T, result *mcp.CallToolResult) int64 {
	t.Helper()
	require.False(t, result.IsError)

	data, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	var out deleteSessionOutput
	require.NoError(t, json.Unmarshal(data, &out))

	return out.Deleted
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
