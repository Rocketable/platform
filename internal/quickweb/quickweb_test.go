package quickweb

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeNamespace(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootDir, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "dir", "index.html"), []byte("dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "root", raw: "/", want: "index.html"},
		{name: "explicit index", raw: "/index.html", want: "index.html"},
		{name: "directory slash", raw: "/x/", want: "x/index.html"},
		{name: "directory index", raw: "/x/index.html", want: "x/index.html"},
		{name: "clean parent segment", raw: "/x/../y/", want: "y/index.html"},
		{name: "query stripped", raw: "/tools/scoreboard/?x=1", want: "tools/scoreboard/index.html"},
		{name: "fragment stripped", raw: "/tools/scoreboard/#section", want: "tools/scoreboard/index.html"},
		{name: "directory applet", raw: "/dir", want: "dir/index.html"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeNamespace(tt.raw, root)
			if err != nil {
				t.Fatalf("normalizeNamespace returned error: %v", err)
			}

			if got != tt.want {
				t.Fatalf("normalizeNamespace(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestNormalizeNamespaceRejectsEscape(t *testing.T) {
	for _, raw := range []string{"../x", "/../x", "%2e%2e/x"} {
		if got, err := normalizeNamespace(raw, nil); err == nil {
			t.Fatalf("normalizeNamespace(%q) = %q, want error", raw, got)
		}
	}
}

func TestStaticServing(t *testing.T) {
	server, rootDir := newTestServer(t)
	writeFile(t, rootDir, "index.html", "home")
	writeFile(t, rootDir, "demo/index.html", "demo")

	assertResponse(t, server.Handler(), http.MethodGet, "/", nil, http.StatusOK, "home")
	assertResponse(t, server.Handler(), http.MethodGet, "/demo/", nil, http.StatusOK, "demo")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/demo", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("GET /demo status = %d, want %d", rec.Code, http.StatusPermanentRedirect)
	}
	if got := rec.Header().Get("Location"); got != "/demo/" {
		t.Fatalf("redirect location = %q, want /demo/", got)
	}
}

func TestStaticServingRejectsTraversalAndBlockedFiles(t *testing.T) {
	server, rootDir := newTestServer(t)
	writeFile(t, rootDir, "quickweb.sqlite", "db")
	writeFile(t, rootDir, "quickweb.sqlite-wal", "wal")
	writeFile(t, rootDir, ".env", "secret")
	writeFile(t, rootDir, ".git/config", "git")
	writeFile(t, rootDir, ".hidden", "hidden")
	writeFile(t, rootDir, "safe.txt", "safe")

	assertResponse(t, server.Handler(), http.MethodGet, "/safe.txt", nil, http.StatusOK, "safe")
	for _, target := range []string{"/../safe.txt", "/%2e%2e/safe.txt", "/quickweb.sqlite", "/quickweb.sqlite-wal", "/.env", "/.git/config", "/.hidden"} {
		assertResponse(t, server.Handler(), http.MethodGet, target, nil, http.StatusNotFound, "")
	}
}

func TestDataEndpoint(t *testing.T) {
	server, _ := newTestServer(t)

	assertResponse(t, server.Handler(), http.MethodGet, "/data?path=%2Fdemo%2F", nil, http.StatusOK, "{}")
	assertResponse(t, server.Handler(), http.MethodPut, "/data?path=%2Fdemo%2F", jsonRequest(`{"value":1}`), http.StatusOK, `{"value":1}`)
	assertResponse(t, server.Handler(), http.MethodGet, "/data?path=%2Fdemo%2Findex.html", nil, http.StatusOK, `{"value":1}`)
	assertResponse(t, server.Handler(), http.MethodPost, "/data?path=%2Fdemo%2F", jsonRequest(`{"value":2}`), http.StatusOK, `{"value":2}`)
	assertResponse(t, server.Handler(), http.MethodGet, "/data?path=%2Fdemo%2F", nil, http.StatusOK, `{"value":2}`)
}

func TestDataEndpointRejectsInvalidAndOversizedJSON(t *testing.T) {
	server, _ := newTestServer(t)

	assertResponse(t, server.Handler(), http.MethodPut, "/data?path=%2Fdemo%2F", jsonRequest(`{"value":`), http.StatusBadRequest, "")
	assertResponse(t, server.Handler(), http.MethodPut, "/data?path=%2Fdemo%2F", plainRequest(`{"value":1}`), http.StatusUnsupportedMediaType, "")

	tooLarge := `"` + strings.Repeat("x", hardMaxJSONBytes) + `"`
	assertResponse(t, server.Handler(), http.MethodPut, "/data?path=%2Fdemo%2F", jsonRequest(tooLarge), http.StatusRequestEntityTooLarge, "")
}

func TestHealthEndpoint(t *testing.T) {
	server, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want 200", rec.Code)
	}

	var got healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if !got.OK || !got.MigrationOK || got.ContentRoot == "" || got.DBPath == "" || got.Addr == "" || len(got.CandidateURLs) == 0 {
		t.Fatalf("health response missing diagnostics: %+v", got)
	}
}

func TestSkillsEndpoint(t *testing.T) {
	server, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/skills", nil)
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /skills status = %d, want 200", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{"/data", "full overwrite", "index.html", "There is no PATCH endpoint", "saveState"} {
		if !strings.Contains(body, want) {
			t.Fatalf("/skills response missing %q", want)
		}
	}
}

func TestMethodRestrictions(t *testing.T) {
	server, _ := newTestServer(t)
	assertResponse(t, server.Handler(), http.MethodPatch, "/data?path=%2Fdemo%2F", nil, http.StatusMethodNotAllowed, "")
	assertResponse(t, server.Handler(), http.MethodPost, "/skills", nil, http.StatusMethodNotAllowed, "")
	assertResponse(t, server.Handler(), http.MethodPost, "/index.html", nil, http.StatusMethodNotAllowed, "")
}

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()

	rootDir := t.TempDir()
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateDatabase(db); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = root.Close()
		_ = db.Close()
	})

	cfg := Config{ContentRoot: rootDir, DBPath: filepath.Join(rootDir, "quickweb.sqlite"), Addr: "127.0.0.1:8797", ServiceName: "test-quickweb"}
	server := NewServer(cfg, root, db, []string{"http://127.0.0.1:8797"}, true)

	return server, rootDir
}

type requestBody struct {
	contentType string
	body        string
}

func jsonRequest(body string) *requestBody {
	return &requestBody{contentType: "application/json", body: body}
}

func plainRequest(body string) *requestBody {
	return &requestBody{contentType: "text/plain", body: body}
}

func assertResponse(t *testing.T, handler http.Handler, method, target string, body *requestBody, wantStatus int, wantBody string) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(body.body)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", body.contentType)
	}
	handler.ServeHTTP(rec, req)

	if rec.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body: %s", method, target, rec.Code, wantStatus, rec.Body.String())
	}

	if wantBody != "" {
		if got := rec.Body.String(); got != wantBody {
			t.Fatalf("%s %s body = %q, want %q", method, target, got, wantBody)
		}
	}
}

func writeFile(t *testing.T, rootDir, name, contents string) {
	t.Helper()

	path := filepath.Join(rootDir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
