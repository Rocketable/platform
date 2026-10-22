package funneld

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestRunRejectsInvalidArgs(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"--unknown"}, "flag provided but not defined: -unknown"},
		{"positional argument", []string{"unexpected"}, "funneld takes flags only, got extra arguments: [unexpected]"},
		{"invalid TLS address", []string{"--config", writeConfig(t, `{"addr":"invalid","host":"example.ts.net","routes":[{"path":"/hook","target":"http://localhost:7070/hook"}]}`)}, "listen invalid: listen tcp: address invalid: missing port in address"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := Run(t.Context(), "funneld", tt.args)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("Run(%q) error = %v; want %q", tt.args, err, tt.want)
			}
		})
	}
}

func TestConfigAttrs(t *testing.T) {
	cfg := config{
		Addr:      "127.0.0.1:8443",
		HTTPAddr:  "127.0.0.1:8080",
		Host:      "example.ts.net",
		CertCache: "cert-cache",
		Routes: []routeConfig{
			{Path: "/first", Target: "http://localhost:7070/hook"},
			{Path: "/second", Target: "https://localhost:7071/events"},
		},
	}
	want := []slog.Attr{
		slog.String("addr", "127.0.0.1:8443"),
		slog.String("http_addr", "127.0.0.1:8080"),
		slog.String("host", "example.ts.net"),
		slog.String("cert_cache", "cert-cache"),
		slog.Int("route_count", 2),
		slog.GroupAttrs("route_0", slog.String("path", "/first"), slog.String("target", "http://localhost:7070/hook")),
		slog.GroupAttrs("route_1", slog.String("path", "/second"), slog.String("target", "https://localhost:7071/events")),
	}
	if got := configAttrs(cfg); !slices.EqualFunc(got, want, slog.Attr.Equal) {
		t.Fatalf("configAttrs = %v; want %v", got, want)
	}

	var logs bytes.Buffer
	logConfiguredRoutes(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)), cfg.Routes)
	decoder := json.NewDecoder(&logs)
	for i, route := range cfg.Routes {
		var record struct {
			Message string `json:"msg"`
			Route   int    `json:"route"`
			Path    string `json:"path"`
			Target  string `json:"target"`
		}
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		if record.Message != "funneld route configured" || record.Route != i || record.Path != route.Path || record.Target != route.Target {
			t.Fatalf("route log = %+v; want route %d: %+v", record, i, route)
		}
	}
	if _, err := decoder.Token(); err != io.EOF {
		t.Fatalf("after route logs error = %v; want EOF", err)
	}
}

func TestRunCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := Run(ctx, "funneld", []string{"--unknown"}); err != context.Canceled {
		t.Fatalf("Run with canceled context error = %v; want %v", err, context.Canceled)
	}
}

func TestLoadConfigFromArgs(t *testing.T) {
	path := writeConfig(t, `{
		"host": "https://example.ts.net",
		"routes": [
			{"path": "/service/webhook", "target": "http://localhost:7070/service/webhook"}
		]
	}`)

	for _, tt := range []struct {
		name         string
		args         []string
		wantHost     string
		wantHTTPAddr string
	}{
		{"disable HTTP", []string{"--http-addr", "none"}, "example.ts.net", ""},
		{"override host and HTTP address", []string{"--host", "other.ts.net", "--http-addr", "127.0.0.1:8080"}, "other.ts.net", "127.0.0.1:8080"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"--config", path, "--addr", "127.0.0.1:8443"}, tt.args...)
			cfg, err := loadConfigFromArgs(args, func(string) string { return "" })
			if err != nil {
				t.Fatal(err)
			}

			if cfg.Host != tt.wantHost {
				t.Fatalf("Host = %q; want %q", cfg.Host, tt.wantHost)
			}

			if cfg.Addr != "127.0.0.1:8443" {
				t.Fatalf("Addr = %q; want 127.0.0.1:8443", cfg.Addr)
			}

			if cfg.HTTPAddr != tt.wantHTTPAddr {
				t.Fatalf("HTTPAddr = %q; want %q", cfg.HTTPAddr, tt.wantHTTPAddr)
			}

			if cfg.CertCache != defaultCertCache {
				t.Fatalf("CertCache = %q; want %q", cfg.CertCache, defaultCertCache)
			}
		})
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	cfg, err := loadConfigFromArgs([]string{"--config", path}, os.Getenv)
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "read config "+path) {
		t.Fatalf("loadConfigFromArgs error = %v; want missing config %q", err, path)
	}
	if !reflect.DeepEqual(cfg, config{}) {
		t.Fatalf("loadConfigFromArgs config = %+v; want zero config on error", cfg)
	}
}

func TestLoadConfigRejectsInvalidConfig(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
		want    string
	}{
		{"duplicate paths", `{"host":"example.ts.net","routes":[{"path":"/hook","target":"http://localhost:7070/hook"},{"path":"/hook","target":"http://localhost:7071/hook"}]}`, `duplicate route path "/hook"`},
		{"invalid JSON", `{`, "parse config"},
		{"missing host", `{"routes":[{"path":"/hook","target":"http://localhost:7070/hook"}]}`, "host is required"},
		{"malformed host URL", `{"host":"https://%zz","routes":[{"path":"/hook","target":"http://localhost:7070/hook"}]}`, `parse host "https://%zz"`},
		{"host path", `{"host":"example.ts.net/path","routes":[{"path":"/hook","target":"http://localhost:7070/hook"}]}`, `host "example.ts.net/path" must not include a path`},
		{"URL without host", `{"host":"https:///path","routes":[{"path":"/hook","target":"http://localhost:7070/hook"}]}`, "host is required"},
		{"empty address", `{"host":"example.ts.net","addr":"","routes":[{"path":"/hook","target":"http://localhost:7070/hook"}]}`, "addr is required"},
		{"empty cert cache", `{"host":"example.ts.net","cert_cache":"","routes":[{"path":"/hook","target":"http://localhost:7070/hook"}]}`, "cert_cache is required"},
		{"missing routes", `{"host":"example.ts.net"}`, "at least one route is required"},
		{"relative route path", `{"host":"example.ts.net","routes":[{"path":"hook","target":"http://localhost:7070/hook"}]}`, `route path "hook" must start with /`},
		{"malformed target", `{"host":"example.ts.net","routes":[{"path":"/hook","target":"http://%zz/hook"}]}`, "parse target for /hook"},
		{"unsupported target scheme", `{"host":"example.ts.net","routes":[{"path":"/hook","target":"ftp://localhost:7070/hook"}]}`, "route /hook target must use http or https"},
		{"target without path", `{"host":"example.ts.net","routes":[{"path":"/hook","target":"http://localhost:7070"}]}`, "route /hook target must include host and path"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.content)
			cfg, err := loadConfigFromArgs([]string{"--config", path}, os.Getenv)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("loadConfigFromArgs error = %v; want %q", err, tt.want)
			}

			if !reflect.DeepEqual(cfg, config{}) {
				t.Fatalf("loadConfigFromArgs config = %+v; want zero config on error", cfg)
			}
		})
	}
}

func TestHandlerProxiesMountedPath(t *testing.T) {
	var backendHost string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/service/webhook/events?event=ping" {
			t.Fatalf("backend request URI = %q; want /service/webhook/events?event=ping", r.URL.RequestURI())
		}

		if r.Host != backendHost {
			t.Fatalf("backend Host = %q; want target host", r.Host)
		}

		_, _ = fmt.Fprint(w, "proxied")
	}))
	defer backend.Close()
	backendHost = strings.TrimPrefix(backend.URL, "http://")

	handler, err := newHandler(config{Routes: []routeConfig{{Path: "/public/webhook", Target: backend.URL + "/service/webhook"}}}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/public/webhook/events?event=ping", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "proxied" {
		t.Fatalf("response body = %q; want proxied", data)
	}
}

func TestHandlerProxiesMountedBasePath(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/service/webhook?event=ping" {
			t.Fatalf("backend request URI = %q; want /service/webhook?event=ping", r.URL.RequestURI())
		}

		_, _ = fmt.Fprint(w, "proxied")
	}))
	defer backend.Close()

	handler, err := newHandler(config{Routes: []routeConfig{{Path: "/public/webhook", Target: backend.URL + "/service/webhook"}}}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/public/webhook?event=ping", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", w.Code, http.StatusOK)
	}
}

func TestHandlerReturnsNotFoundForUnmappedPath(t *testing.T) {
	handler, err := newHandler(config{Routes: []routeConfig{{Path: "/hook", Target: "http://localhost:7070/hook"}}}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandlerLogsRequestAndResponse(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, "created")
	}))
	defer backend.Close()

	logs := &recordHandler{}
	logger := slog.New(logs)
	handler, err := newHandler(config{Routes: []routeConfig{{Path: "/public/webhook", Target: backend.URL + "/service/webhook"}}}, logger)
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/public/webhook/events?event=ping", nil))

	if len(logs.records) != 1 {
		t.Fatalf("log records = %d; want 1", len(logs.records))
	}

	record := logs.records[0]
	if record.Message != "http request" {
		t.Fatalf("log msg = %q; want http request", record.Message)
	}

	if got := attrValue(t, record, "method"); got.String() != http.MethodPost {
		t.Fatalf("log method = %q; want %s", got.String(), http.MethodPost)
	}

	if got := attrValue(t, record, "path"); got.String() != "/public/webhook/events" {
		t.Fatalf("log path = %q; want /public/webhook/events", got.String())
	}

	if got := attrValue(t, record, "query"); got.String() != "event=ping" {
		t.Fatalf("log query = %q; want event=ping", got.String())
	}

	if got := attrValue(t, record, "status"); got.Int64() != http.StatusCreated {
		t.Fatalf("log status = %d; want %d", got.Int64(), http.StatusCreated)
	}

	if got := attrValue(t, record, "bytes"); got.Int64() != int64(len("created")) {
		t.Fatalf("log bytes = %d; want %d", got.Int64(), len("created"))
	}

	if got := attrValue(t, record, "duration"); got.Duration() <= 0 {
		t.Fatalf("log duration = %s; want positive", got.Duration())
	}

	logRequests(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "ok")
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if got := attrValue(t, logs.records[1], "status"); got.Int64() != http.StatusOK {
		t.Fatalf("implicit log status = %d; want %d", got.Int64(), http.StatusOK)
	}
}

type recordHandler struct {
	records []slog.Record
}

func (h *recordHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *recordHandler) Handle(_ context.Context, record slog.Record) error {
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *recordHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *recordHandler) WithGroup(string) slog.Handler {
	return h
}

func attrValue(t *testing.T, record slog.Record, key string) slog.Value {
	t.Helper()

	var value slog.Value
	found := false
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == key {
			value = attr.Value
			found = true
			return false
		}

		return true
	})

	if !found {
		t.Fatalf("missing log attr %q", key)
	}

	return value
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "funneld.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
