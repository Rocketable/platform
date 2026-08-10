// Package mcpclient connects to outbound MCP servers for RocketCode code mode.
package mcpclient

import (
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

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const clientName = "rocketcode-mcp-client"

const httpClientTimeout = 60 * time.Second

var errSessionClosed = errors.New("mcp session closed")

// ServerConfig describes one outbound MCP server transport.
type ServerConfig struct {
	Command string
	Args    []string
	Env     map[string]string
	Cwd     string
	URL     string
	Headers map[string]string
}

// Registry holds configured MCP server specs for a workspace.
type Registry struct {
	workspace string
	servers   map[string]ServerConfig
}

// New builds a registry. servers is copied; later mutation is ignored.
func New(workspace string, servers map[string]ServerConfig) (*Registry, error) {
	cloned := make(map[string]ServerConfig, len(servers))
	for name, cfg := range servers {
		cloned[name] = ServerConfig{
			Command: cfg.Command,
			Args:    slices.Clone(cfg.Args),
			Env:     maps.Clone(cfg.Env),
			Cwd:     cfg.Cwd,
			URL:     cfg.URL,
			Headers: maps.Clone(cfg.Headers),
		}
	}

	return &Registry{workspace: workspace, servers: cloned}, nil
}

// Names returns configured server names in sorted order.
func (r *Registry) Names() []string {
	return slices.Sorted(maps.Keys(r.servers))
}

// Has reports whether name is a configured server.
func (r *Registry) Has(name string) bool {
	_, ok := r.servers[name]
	return ok
}

// Open starts a session that connects servers lazily and closes them on Close.
func (r *Registry) Open() *Session {
	return &Session{reg: r, conns: make(map[string]*mcp.ClientSession)}
}

// ToolInfo is one tool from a server's catalog.
type ToolInfo struct {
	Server      string
	Name        string
	Description string
	InputSchema map[string]any
}

// Session is one execute tool-call lifetime.
type Session struct {
	reg    *Registry
	mu     sync.Mutex
	conns  map[string]*mcp.ClientSession
	closed bool
}

// ListTools returns the full tool catalog for server.
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

		out = append(out, ToolInfo{
			Server:      server,
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: schemaMap(tool.InputSchema),
		})
	}

	return out, nil
}

// CallTool invokes tool on server and flattens the result to a string.
func (s *Session) CallTool(ctx context.Context, server, tool string, args map[string]any) (string, error) {
	cs, err := s.connect(ctx, server)
	if err != nil {
		return "", err
	}

	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("call %s.%s: %w", server, tool, err)
	}

	if result == nil {
		return "", fmt.Errorf("call %s.%s: nil result", server, tool)
	}

	return flattenCallToolResult(result)
}

// Close ends all connections opened by this session. It is idempotent.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true

	var errs []error

	for name, cs := range s.conns {
		if err := cs.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close mcp server %q: %w", name, err))
		}
	}

	clear(s.conns)

	return errors.Join(errs...)
}

func (s *Session) connect(ctx context.Context, name string) (*mcp.ClientSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
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
		if len(cfg.Headers) > 0 {
			endpoint, errURL := url.Parse(cfg.URL)
			if errURL != nil {
				return nil, fmt.Errorf("connect mcp server %q: %w", name, errURL)
			}

			t.HTTPClient = &http.Client{
				Timeout: httpClientTimeout,
				Transport: headerRoundTripper{
					base:    http.DefaultTransport,
					headers: cfg.Headers,
					host:    endpoint.Host,
				},
				CheckRedirect: sameHostRedirects,
			}
		} else {
			t.HTTPClient = &http.Client{Timeout: httpClientTimeout, CheckRedirect: sameHostRedirects}
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

// childEnv builds a minimal process environment plus configured overrides.
// It does not pass the full parent environ (avoids leaking host secrets to MCP children).
func childEnv(overrides map[string]string) []string {
	allow := []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "TMP", "TEMP", "USER", "LOGNAME", "SHELL"}
	parent := map[string]string{}

	for _, entry := range os.Environ() {
		key, val, ok := strings.Cut(entry, "=")
		if ok {
			parent[key] = val
		}
	}

	out := make([]string, 0, len(allow)+len(overrides))

	seen := make(map[string]struct{}, len(allow)+len(overrides))
	for _, key := range allow {
		if val, ok := overrides[key]; ok {
			out = append(out, key+"="+val)
			seen[key] = struct{}{}

			continue
		}

		if val, ok := parent[key]; ok {
			out = append(out, key+"="+val)
			seen[key] = struct{}{}
		}
	}

	for key, val := range overrides {
		if _, ok := seen[key]; ok {
			continue
		}

		out = append(out, key+"="+val)
	}

	return out
}

func resolveCwd(workspace, cwd string) (string, error) {
	ws := filepath.Clean(workspace)
	if cwd == "" {
		return ws, nil
	}

	var abs string
	if filepath.IsAbs(cwd) {
		abs = filepath.Clean(cwd)
	} else {
		abs = filepath.Clean(filepath.Join(ws, cwd))
	}

	sep := string(filepath.Separator)
	if abs != ws && !strings.HasPrefix(abs, ws+sep) {
		return "", fmt.Errorf("cwd %q escapes workspace", cwd)
	}

	return abs, nil
}

func sameHostRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}

	if len(via) == 0 {
		return nil
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

func schemaMap(schema any) map[string]any {
	if schema == nil {
		return nil
	}

	if m, ok := schema.(map[string]any); ok {
		return m
	}

	data, err := json.Marshal(schema)
	if err != nil {
		return nil
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}

	return m
}

func flattenCallToolResult(result *mcp.CallToolResult) (string, error) {
	var b strings.Builder

	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}

	text := b.String()
	if result.IsError {
		if text != "" {
			return "", errors.New(text)
		}

		if result.StructuredContent != nil {
			data, err := json.Marshal(result.StructuredContent)
			if err != nil {
				return "", fmt.Errorf("mcp tool error: %w", err)
			}

			return "", errors.New(string(data))
		}

		return "", errors.New("mcp tool error")
	}

	if text != "" {
		return text, nil
	}

	if result.StructuredContent == nil {
		return "", nil
	}

	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return "", fmt.Errorf("marshal structured content: %w", err)
	}

	return string(data), nil
}
