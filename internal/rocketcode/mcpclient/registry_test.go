package mcpclient

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestFlattenCallToolResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		result  *mcp.CallToolResult
		want    string
		wantErr string
	}{
		{
			name:   "text",
			result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hello"}}},
			want:   "hello",
		},
		{
			name:   "concat text",
			result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "a"}, &mcp.TextContent{Text: "b"}}},
			want:   "ab",
		},
		{
			name:    "isError with text",
			result:  &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "boom"}}},
			wantErr: "boom",
		},
		{
			name:    "isError empty",
			result:  &mcp.CallToolResult{IsError: true},
			wantErr: "mcp tool error",
		},
		{
			name:   "structured only",
			result: &mcp.CallToolResult{StructuredContent: map[string]any{"ok": true}},
			want:   `{"ok":true}`,
		},
		{
			name:   "text wins over structured",
			result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "t"}}, StructuredContent: map[string]any{"ok": true}},
			want:   "t",
		},
		{
			name:   "empty success",
			result: &mcp.CallToolResult{},
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := flattenCallToolResult(tt.result)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRegistryNamesHas(t *testing.T) {
	t.Parallel()

	reg, err := New("/ws", map[string]ServerConfig{
		"beta":  {URL: "http://example"},
		"alpha": {Command: "echo"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta"}, reg.Names())
	assert.True(t, reg.Has("alpha"))
	assert.False(t, reg.Has("missing"))
}

func TestUnknownServer(t *testing.T) {
	t.Parallel()

	reg, err := New(t.TempDir(), nil)
	require.NoError(t, err)

	session := reg.Open()

	t.Cleanup(func() { require.NoError(t, session.Close()) })

	_, err = session.ListTools(t.Context(), "nope")
	require.ErrorContains(t, err, `unknown mcp server "nope"`)

	_, err = session.CallTool(t.Context(), "nope", "echo", nil)
	require.ErrorContains(t, err, `unknown mcp server "nope"`)
}

func TestCloseIdempotent(t *testing.T) {
	t.Parallel()

	url := startTestHTTPServer(t, func(server *mcp.Server) {
		mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
			Message string `json:"message"`
		}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Message}}}, nil, nil
		})
	})

	reg, err := New(t.TempDir(), map[string]ServerConfig{"demo": {URL: url}})
	require.NoError(t, err)

	session := reg.Open()
	_, err = session.CallTool(t.Context(), "demo", "echo", map[string]any{"message": "x"})
	require.NoError(t, err)
	require.NoError(t, session.Close())
	require.NoError(t, session.Close())
}

func TestHTTPCallTool(t *testing.T) {
	t.Parallel()

	url := startTestHTTPServer(t, func(server *mcp.Server) {
		mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echo text"}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
			Message string `json:"message"`
		}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Message}}}, nil, nil
		})
		server.AddTool(&mcp.Tool{
			Name:        "fail",
			Description: "fail",
			InputSchema: map[string]any{"type": "object"},
		}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "nope"}}}, nil
		})
		server.AddTool(&mcp.Tool{
			Name:        "struct_only",
			Description: "structured",
			InputSchema: map[string]any{"type": "object"},
		}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{StructuredContent: map[string]any{"n": float64(1)}}, nil
		})
	})

	reg, err := New(t.TempDir(), map[string]ServerConfig{"demo": {URL: url}})
	require.NoError(t, err)

	session := reg.Open()

	t.Cleanup(func() { require.NoError(t, session.Close()) })

	tools, err := session.ListTools(t.Context(), "demo")
	require.NoError(t, err)
	require.Len(t, tools, 3)

	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		assert.Equal(t, "demo", tool.Server)
		names = append(names, tool.Name)
		assert.NotNil(t, tool.InputSchema)
	}

	assert.ElementsMatch(t, []string{"echo", "fail", "struct_only"}, names)

	got, err := session.CallTool(t.Context(), "demo", "echo", map[string]any{"message": "hi"})
	require.NoError(t, err)
	assert.Equal(t, "hi", got)

	_, err = session.CallTool(t.Context(), "demo", "fail", map[string]any{})
	require.EqualError(t, err, "nope")

	got, err = session.CallTool(t.Context(), "demo", "struct_only", map[string]any{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"n":1}`, got)
}

func TestHTTPHeaders(t *testing.T) {
	t.Parallel()

	var sawAuth string

	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "hdr", Version: "1.0.0"}, nil)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "ping", Description: "ping"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
	})

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		handler.ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()

	t.Cleanup(func() { _ = srv.Close() })

	url := "http://" + ln.Addr().String()

	reg, err := New(t.TempDir(), map[string]ServerConfig{"demo": {
		URL:     url,
		Headers: map[string]string{"Authorization": "Bearer secret"},
	}})
	require.NoError(t, err)

	session := reg.Open()

	t.Cleanup(func() { require.NoError(t, session.Close()) })

	got, err := session.CallTool(t.Context(), "demo", "ping", nil)
	require.NoError(t, err)
	assert.Equal(t, "pong", got)
	assert.Equal(t, "Bearer secret", sawAuth)
}

func TestStdioCallTool(t *testing.T) {
	t.Parallel()

	helper := buildStdioHelper(t)
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "marker"), []byte("ok"), 0o600))

	reg, err := New(workspace, map[string]ServerConfig{
		"local": {
			Command: helper,
			Env:     map[string]string{"MCPCLIENT_MARKER": "from-env"},
		},
	})
	require.NoError(t, err)

	session := reg.Open()

	t.Cleanup(func() { require.NoError(t, session.Close()) })

	tools, err := session.ListTools(t.Context(), "local")
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "echo", tools[0].Name)

	got, err := session.CallTool(t.Context(), "local", "echo", map[string]any{"message": "stdio"})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(got), &payload))
	assert.Equal(t, "stdio", payload["message"])
	assert.Equal(t, "from-env", payload["env"])
	assert.Equal(t, "ok", payload["marker"])
}

func startTestHTTPServer(t *testing.T, register func(*mcp.Server)) string {
	t.Helper()

	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "test-mcp", Version: "1.0.0"}, nil)
	register(mcpServer)

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()

	t.Cleanup(func() { _ = srv.Close() })

	return "http://" + ln.Addr().String()
}

func buildStdioHelper(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "stdio-helper")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/stdiohelper")
	cmd.Dir = "."

	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	return bin
}

func TestChildEnvMinimal(t *testing.T) {
	t.Setenv("SECRET_SHOULD_NOT_LEAK", "x")

	env := childEnv(map[string]string{"MCPCLIENT_MARKER": "from-env"})
	joined := strings.Join(env, "\n")
	assert.Contains(t, joined, "MCPCLIENT_MARKER=from-env")
	assert.NotContains(t, joined, "SECRET_SHOULD_NOT_LEAK")
	assert.Contains(t, joined, "PATH=")
}

func TestResolveCwdRejectsEscape(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	got, err := resolveCwd(ws, "")
	require.NoError(t, err)
	assert.Equal(t, filepath.Clean(ws), got)

	got, err = resolveCwd(ws, "sub")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(ws, "sub"), got)

	_, err = resolveCwd(ws, filepath.Join(string(filepath.Separator), "tmp", "outside"))
	require.Error(t, err)
}

func TestSessionConnectConcurrent(t *testing.T) {
	t.Parallel()
	url1 := startTestHTTPServer(t, func(server *mcp.Server) {
		mcp.AddTool(server, &mcp.Tool{Name: "a", Description: "a"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "a"}}}, nil, nil
		})
	})
	url2 := startTestHTTPServer(t, func(server *mcp.Server) {
		mcp.AddTool(server, &mcp.Tool{Name: "b", Description: "b"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "b"}}}, nil, nil
		})
	})
	reg, err := New(t.TempDir(), map[string]ServerConfig{
		"s1": {URL: url1},
		"s2": {URL: url2},
	})
	require.NoError(t, err)

	session := reg.Open()

	t.Cleanup(func() { require.NoError(t, session.Close()) })

	var g errgroup.Group
	g.Go(func() error {
		_, err := session.ListTools(t.Context(), "s1")
		return err
	})
	g.Go(func() error {
		_, err := session.ListTools(t.Context(), "s2")
		return err
	})
	require.NoError(t, g.Wait())
}
