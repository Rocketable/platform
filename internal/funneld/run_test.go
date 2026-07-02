package funneld

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigFromArgs(t *testing.T) {
	path := writeConfig(t, `{
		"host": "https://example.ts.net",
		"routes": [
			{"path": "/service/webhook", "target": "http://localhost:7070/service/webhook"}
		]
	}`)

	cfg, err := loadConfigFromArgs([]string{"--config", path, "--addr", "127.0.0.1:8443", "--http-addr", "none"}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Host != "example.ts.net" {
		t.Fatalf("Host = %q; want example.ts.net", cfg.Host)
	}

	if cfg.Addr != "127.0.0.1:8443" {
		t.Fatalf("Addr = %q; want 127.0.0.1:8443", cfg.Addr)
	}

	if cfg.HTTPAddr != "" {
		t.Fatalf("HTTPAddr = %q; want empty", cfg.HTTPAddr)
	}

	if cfg.CertCache != defaultCertCache {
		t.Fatalf("CertCache = %q; want %q", cfg.CertCache, defaultCertCache)
	}
}

func TestLoadConfigRejectsDuplicatePaths(t *testing.T) {
	path := writeConfig(t, `{
		"host": "example.ts.net",
		"routes": [
			{"path": "/hook", "target": "http://localhost:7070/hook"},
			{"path": "/hook", "target": "http://localhost:7071/hook"}
		]
	}`)

	_, err := loadConfigFromArgs([]string{"--config", path}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), `duplicate route path "/hook"`) {
		t.Fatalf("loadConfigFromArgs error = %v; want duplicate route path", err)
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
