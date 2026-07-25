package quickweb

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillCommandIndex(t *testing.T) {
	var out bytes.Buffer
	if err := runSkillCommand(&out, "quickweb", nil); err != nil {
		t.Fatalf("runSkillCommand returned error: %v", err)
	}

	body := out.String()
	for _, want := range []string{"# Quickweb Skill Index", "quickweb skill install", "quickweb skill run", "quickweb skill create-applet", "quickweb skill find-applet", "quickweb skill troubleshoot"} {
		if !strings.Contains(body, want) {
			t.Fatalf("skill index missing %q", want)
		}
	}
	if strings.Contains(body, "# Quickweb Skill: create-applet") {
		t.Fatalf("skill index includes focused skill body")
	}
}

func TestSkillCommandHelp(t *testing.T) {
	for _, arg := range []string{"--help", "-h"} {
		t.Run(arg, func(t *testing.T) {
			var out bytes.Buffer
			if err := runSkillCommand(&out, "quickweb", []string{arg}); err != nil {
				t.Fatalf("runSkillCommand returned error: %v", err)
			}
			if body := out.String(); !strings.Contains(body, "# Quickweb Skill Index") || !strings.Contains(body, "quickweb skill create-applet") {
				t.Fatalf("skill help output missing index: %s", body)
			}
		})
	}
}

func TestSkillCommandNamedSkills(t *testing.T) {
	tests := []struct {
		name    string
		want    []string
		missing string
	}{
		{name: "install", want: []string{"# Quickweb Skill: install", "not a hosted service", "already installed", "go run ./cmd/quickweb --help"}, missing: "# Quickweb Skill: run"},
		{name: "run", want: []string{"# Quickweb Skill: run", "There is no `--root` flag", "`--db`", "`--addr`", "`--service-name`", "`--base-url`"}, missing: "# Quickweb Skill: create-applet"},
		{name: "create-applet", want: []string{"# Quickweb Skill: create-applet", "static HTML", "`/data`", "`location.pathname`", "full overwrite", "There is no `PATCH` endpoint", "Last write wins", "No browser libraries are approved yet"}, missing: "# Quickweb Skill: troubleshoot"},
		{name: "find-applet", want: []string{"# Quickweb Skill: find-applet", "`/tool`", "`/tool/`", "dotfiles", "SQLite files"}, missing: "# Quickweb Skill: install"},
		{name: "troubleshoot", want: []string{"# Quickweb Skill: troubleshoot", "Missing state", "directory applets normalize", "Startup failure", "Browser 404"}, missing: "# Quickweb Skill: find-applet"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := runSkillCommand(&out, "quickweb", []string{tt.name}); err != nil {
				t.Fatalf("runSkillCommand returned error: %v", err)
			}

			body := out.String()
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Fatalf("skill %s missing %q", tt.name, want)
				}
			}
			if strings.Contains(body, tt.missing) {
				t.Fatalf("skill %s includes unrelated focused skill body", tt.name)
			}
		})
	}
}

func TestSkillCommandErrors(t *testing.T) {
	var out bytes.Buffer
	err := runSkillCommand(&out, "quickweb", []string{"bad-name"})
	if err == nil {
		t.Fatal("runSkillCommand returned nil error for unknown skill")
	}
	for _, want := range []string{"unknown quickweb skill", "install", "run", "create-applet", "find-applet", "troubleshoot"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("unknown skill error missing %q: %v", want, err)
		}
	}

	err = runSkillCommand(&out, "quickweb", []string{"create-applet", "extra"})
	if err == nil {
		t.Fatal("runSkillCommand returned nil error for extra arg")
	}
	if !strings.Contains(err.Error(), "usage: quickweb skill [name]") {
		t.Fatalf("extra arg error missing usage: %v", err)
	}
}

func TestRunDispatchesNamedSkillArguments(t *testing.T) {
	err := Run(context.Background(), "quickweb", []string{"skill", "bad-name"})
	if err == nil {
		t.Fatal("Run returned nil error for unknown skill")
	}
	if !strings.Contains(err.Error(), "unknown quickweb skill") {
		t.Fatalf("Run skill error = %v, want unknown skill error", err)
	}
}

func TestSkillRunDoesNotNeedWorkingDirectory(t *testing.T) {
	removedDir := filepath.Join(t.TempDir(), "removed")
	if err := os.Mkdir(removedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(removedDir)
	if err := os.Remove(removedDir); err != nil {
		t.Fatal(err)
	}

	if err := Run(context.Background(), "quickweb", []string{"skill", "--help"}); err != nil {
		t.Fatalf("Run skill returned error from removed working directory: %v", err)
	}
}

func TestHelpMentionsSkillCommand(t *testing.T) {
	body := helpText("quickweb")
	if !strings.Contains(body, "quickweb skill [name]") {
		t.Fatalf("help text missing skill command: %s", body)
	}

	err := Run(context.Background(), "quickweb", []string{"not-skill"})
	if err == nil {
		t.Fatal("Run returned nil error for positional argument")
	}
	if !strings.Contains(err.Error(), "quickweb skill [name]") {
		t.Fatalf("positional argument error missing skill guidance: %v", err)
	}
}

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
	writeFile(t, rootDir, "skills", "user skills")

	assertResponse(t, server.Handler(), http.MethodGet, "/", nil, http.StatusOK, "home")
	assertResponse(t, server.Handler(), http.MethodGet, "/demo/", nil, http.StatusOK, "demo")
	assertResponse(t, server.Handler(), http.MethodGet, "/skills", nil, http.StatusOK, "user skills")

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
	if !got.OK || got.ContentRoot == "" || got.DBPath == "" || got.Addr == "" || len(got.CandidateURLs) == 0 {
		t.Fatalf("health response missing diagnostics: %+v", got)
	}
}

func TestCreateAppletSkillIncludesAppletGuidanceFacts(t *testing.T) {
	var out bytes.Buffer
	if err := runSkillCommand(&out, "quickweb", []string{"create-applet"}); err != nil {
		t.Fatalf("runSkillCommand returned error: %v", err)
	}

	cliBody := out.String()
	for _, want := range []string{"/data", "location.pathname", "full overwrite", "PATCH", "directory applets normalize", "static HTML", "SQLite files", "dotfiles", "Last write wins", "No browser libraries are approved yet"} {
		if !strings.Contains(strings.ToLower(cliBody), strings.ToLower(want)) {
			t.Fatalf("create-applet skill missing %q", want)
		}
	}
}

func TestMethodRestrictions(t *testing.T) {
	server, _ := newTestServer(t)
	assertResponse(t, server.Handler(), http.MethodPatch, "/data?path=%2Fdemo%2F", nil, http.StatusMethodNotAllowed, "")
	assertResponse(t, server.Handler(), http.MethodPost, "/index.html", nil, http.StatusMethodNotAllowed, "")
}

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()

	rootDir := t.TempDir()
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}

	db, err := openDatabase(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = root.Close()
		_ = db.Close()
	})

	cfg := Config{ContentRoot: rootDir, DBPath: filepath.Join(rootDir, "quickweb.sqlite"), Addr: "127.0.0.1:8797", ServiceName: "test-quickweb"}
	server := NewServer(cfg, root, db, []string{"http://127.0.0.1:8797"})

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
