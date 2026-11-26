package mcpclient

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const clientName = "rocketcode-mcp-client"

const httpClientTimeout = 60 * time.Second

var errSessionClosed = errors.New("mcp session closed")

type Registry struct {
	workspace string
	servers   map[string]config.MCPServerConfig
}

func New(workspace string, servers map[string]config.MCPServerConfig, sessionEnv map[string]string) Registry {
	cloned := make(map[string]config.MCPServerConfig, len(servers))
	for name, cfg := range servers {
		env := make(map[string]string, len(cfg.Env)+len(sessionEnv))
		for key, val := range cfg.Env {
			if !strings.HasPrefix(key, "ROCKETCLAW_") {
				env[key] = val
			}
		}

		headers := make(map[string]string, len(cfg.Headers)+len(sessionEnv))
		for key, val := range cfg.Headers {
			if !strings.HasPrefix(http.CanonicalHeaderKey(key), "X-Rocketclaw-") {
				headers[key] = val
			}
		}

		for key, val := range sessionEnv {
			env[key] = val
			headers["X-"+strings.ReplaceAll(key, "_", "-")] = val
		}

		cfg.Args, cfg.Env, cfg.Headers = slices.Clone(cfg.Args), env, headers
		cloned[name] = cfg
	}

	return Registry{workspace: workspace, servers: cloned}
}

func (r Registry) Names() []string {
	return slices.Sorted(maps.Keys(r.servers))
}

func (r Registry) Open() *Session {
	return &Session{reg: r, conns: make(map[string]*mcp.ClientSession)}
}

type ToolInfo struct {
	Name        string
	Description string
	InputSchema map[string]any
}

type Session struct {
	reg   Registry
	mu    sync.Mutex
	conns map[string]*mcp.ClientSession
}

func (s *Session) ListTools(ctx context.Context, server string) ([]ToolInfo, error) {
	cs, err := s.connect(ctx, server)
	if err != nil {
		return nil, err
	}

	var out []ToolInfo

	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list tools on %q: %w", server, err)
		}

		schema, _ := tool.InputSchema.(map[string]any)
		out = append(out, ToolInfo{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: schema,
		})
	}

	return out, nil
}

func (s *Session) CallTool(ctx context.Context, server, tool string, args map[string]any) (*mcp.CallToolResult, string, error) {
	cs, err := s.connect(ctx, server)
	if err != nil {
		return nil, "", err
	}

	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return nil, "", fmt.Errorf("call %s.%s: %w", server, tool, err)
	}

	text, err := flattenCallToolResult(result)
	if err != nil {
		return nil, "", err
	}

	return result, text, nil
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var errs []error

	for name, cs := range s.conns {
		if err := cs.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close mcp server %q: %w", name, err))
		}
	}

	s.conns = nil

	return errors.Join(errs...)
}

func (s *Session) connect(ctx context.Context, name string) (*mcp.ClientSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conns == nil {
		return nil, errSessionClosed
	}

	if cs, ok := s.conns[name]; ok {
		return cs, nil
	}

	cfg, ok := s.reg.servers[name]
	if !ok {
		return nil, fmt.Errorf("unknown mcp server %q", name)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "1.0.0"}, nil)

	var cs *mcp.ClientSession

	var err error

	switch {
	case cfg.Command != "":
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Env = childEnv(cfg.Env)

		cmd.Dir, err = resolveCwd(s.reg.workspace, cfg.Cwd)
		if err != nil {
			return nil, fmt.Errorf("connect mcp server %q: %w", name, err)
		}

		cs, err = client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	case cfg.URL != "":
		t := &mcp.StreamableClientTransport{Endpoint: cfg.URL, DisableStandaloneSSE: true}
		t.HTTPClient = &http.Client{Timeout: httpClientTimeout, CheckRedirect: sameHostRedirects}

		if len(cfg.Headers) > 0 {
			endpoint, errURL := url.Parse(cfg.URL)
			if errURL != nil {
				return nil, fmt.Errorf("connect mcp server %q: %w", name, errURL)
			}

			t.HTTPClient.Transport = headerRoundTripper{
				base:    http.DefaultTransport,
				headers: cfg.Headers,
				host:    endpoint.Host,
			}
		}

		cs, err = client.Connect(ctx, t, nil)
	default:
		return nil, fmt.Errorf("mcp server %q: no transport configured", name)
	}

	if err != nil {
		return nil, fmt.Errorf("connect mcp server %q: %w", name, err)
	}

	s.conns[name] = cs

	return cs, nil
}

func childEnv(overrides map[string]string) []string {
	allow := []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "TMP", "TEMP", "USER", "LOGNAME", "SHELL"}
	out := make([]string, 0, len(allow)+len(overrides))

	for _, key := range allow {
		val, ok := overrides[key]
		if !ok {
			val, ok = os.LookupEnv(key)
		}

		if ok {
			out = append(out, key+"="+val)
		}
	}

	for key, val := range overrides {
		if slices.Contains(allow, key) {
			continue
		}

		out = append(out, key+"="+val)
	}

	return out
}

func resolveCwd(workspace, cwd string) (string, error) {
	ws := filepath.Clean(workspace)

	abs := cwd
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(ws, cwd)
	}

	if rel, err := filepath.Rel(ws, abs); err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("cwd %q escapes workspace", cwd)
	}

	return filepath.Clean(abs), nil
}

func sameHostRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}

	if req.URL.Host != via[0].URL.Host {
		return fmt.Errorf("refusing cross-host redirect to %s", req.URL.Host)
	}

	return nil
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
	host    string
}

func (r headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if clone.URL.Host == r.host {
		for k, v := range r.headers {
			clone.Header.Set(k, v)
		}
	}

	resp, err := r.base.RoundTrip(clone)
	if err != nil {
		return nil, fmt.Errorf("mcp http: %w", err)
	}

	return resp, nil
}

func flattenCallToolResult(result *mcp.CallToolResult) (string, error) {
	var b strings.Builder

	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}

	text := b.String()

	isError := result.IsError
	if text == "" && result.StructuredContent != nil {
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			if isError {
				return "", fmt.Errorf("mcp tool error: %w", err)
			}

			return "", fmt.Errorf("marshal structured content: %w", err)
		}

		text = string(data)
	}

	if isError {
		return "", errors.New(cmp.Or(text, "mcp tool error"))
	}

	return text, nil
}
