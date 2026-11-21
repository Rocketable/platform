package harness

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

func TestWebFetchRequestAcceptHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("Accept"))
	}))
	t.Cleanup(server.Close)

	for _, tt := range []struct {
		format string
		want   string
	}{
		{"markdown", "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1"},
		{"text", "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1"},
		{"html", "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1"},
		{"", "*/*"},
	} {
		t.Run(tt.format, func(t *testing.T) {
			resp, err := doWebFetchRequest(t.Context(), http.DefaultClient, server.URL, tt.format, false)
			require.NoError(t, err)

			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tt.want, string(body))
		})
	}
}

func TestWebFetchFormatsHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if r.URL.Path == "/without-type" {
			w.Header().Set("Content-Type", "text/plain")
		}

		_, _ = w.Write([]byte(`<html><body><h1>Title</h1><script>hidden()</script><p>Hello <strong>world</strong>.</p><a href="guide">Guide</a></body></html>`))
	}))
	t.Cleanup(server.Close)

	markdown, err := webFetch(context.Background(), http.DefaultClient, testWebFetchParams(server.URL+"/pages/index.html", "markdown"))
	require.NoError(t, err)
	require.Contains(t, markdown.Output, "# Title")
	require.Contains(t, markdown.Output, "**world**")
	require.Contains(t, markdown.Output, "[Guide]("+server.URL+"/guide)")

	text, err := webFetch(context.Background(), http.DefaultClient, testWebFetchParams(server.URL, "text"))
	require.NoError(t, err)
	require.Contains(t, text.Output, "Title")
	require.Contains(t, text.Output, "Hello")
	require.NotContains(t, text.Output, "hidden")

	fallback, err := webFetch(t.Context(), http.DefaultClient, testWebFetchParams(server.URL+"/without-type", "markdown"))
	require.NoError(t, err)
	require.Equal(t, "# Title\n\nHello **world**.\n\n[Guide](guide)", fallback.Output)
}

func TestWebFetchRefusesNonTextContent(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n\x00\x00"

	for _, tt := range []struct {
		name, contentType, body, wantErr string
	}{
		{name: "png header", contentType: "image/png", body: png, wantErr: "Unsupported fetched image content type: image/png"},
		{name: "jpeg header with parameters", contentType: "Image/JPEG; q=1", body: "x", wantErr: "Unsupported fetched image content type: image/jpeg"},
		{name: "pdf header", contentType: "application/pdf", body: "%PDF-1.7\n", wantErr: "Unsupported fetched file content type: application/pdf"},
		{name: "octet stream", contentType: "application/octet-stream", body: "\xff\xd8\xff", wantErr: "Unsupported fetched file content type: application/octet-stream"},
		{name: "zip", contentType: "application/zip", body: "PK", wantErr: "Unsupported fetched file content type: application/zip"},
		{name: "text header with png bytes", contentType: "text/plain", body: png, wantErr: "Unsupported fetched image content type: image/png"},
		{name: "missing header with png bytes", body: png, wantErr: "Unsupported fetched image content type: image/png"},
		{name: "json header with pdf bytes", contentType: "application/json", body: "%PDF-1.4", wantErr: "Unsupported fetched file content type: application/pdf"},
		{name: "svg", contentType: "image/svg+xml", body: "<svg/>"},
		{name: "json", contentType: "application/json", body: `{"a":1}`},
		{name: "json suffix", contentType: "application/ld+json", body: `{"@id":"x"}`},
		{name: "xml", contentType: "application/xml; charset=utf-8", body: "<a/>"},
		{name: "xml suffix", contentType: "application/atom+xml", body: "<feed/>"},
		{name: "javascript", contentType: "application/javascript", body: "f()"},
		{name: "legacy javascript", contentType: "application/x-javascript", body: "g()"},
		{name: "csv", contentType: "text/csv", body: "a,b"},
		{name: "missing header", body: "plain words"},
		{name: "text starting with BM", body: "BMW 320i,2019,blue,4 doors"},
		{name: "csv starting with BM", contentType: "text/csv", body: "BMI,age\n21.5,30\n"},
		{name: "bmp bytes", body: "BM\x46\x00\x00\x00\x00\x00\x00\x00\x36\x00\x00\x00\x28\x00\x00\x00\x01\x00", wantErr: "Unsupported fetched image content type: image/bmp"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header()["Content-Type"] = nil
				if tt.contentType != "" {
					w.Header().Set("Content-Type", tt.contentType)
				}

				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(server.Close)

			got, err := webFetch(t.Context(), http.DefaultClient, testWebFetchParams(server.URL+"/doc", "markdown"))
			require.NoError(t, err)

			page := got.Data.(*webFetchResult).page
			require.Equal(t, []any{cmp.Or(tt.wantErr, tt.body), tt.wantErr, tt.contentType, server.URL + "/doc", http.StatusOK}, []any{got.Output, page.Error, page.ContentType, page.URL, page.Status})

			if tt.wantErr == "" {
				require.Equal(t, tt.body, page.Output)
			}
		})
	}
}

func TestWebFetchRejectsInvalidURL(t *testing.T) {
	for url, want := range map[string]string{
		"file:///tmp/image.png":                "URL must start with http:// or https://",
		"ftp://example.com/x":                  "URL must start with http:// or https://",
		"https://user@docs.example/":           "URL must not include credentials",
		"http://user:secret@docs.example/page": "URL must not include credentials",
		"https://:secret@docs.example/":        "URL must not include credentials",
	} {
		_, err := webFetch(t.Context(), http.DefaultClient, testWebFetchParams(url, ""))
		require.EqualError(t, err, want, url)
	}
}

func TestWebFetchStripsFragment(t *testing.T) {
	var requested []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.String())

		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "page")
	}))
	t.Cleanup(server.Close)

	tool := webFetchTool(http.DefaultClient)

	subjects, err := tool.Subjects(json.RawMessage(`{"url":"` + server.URL + `/page?q=1#section"}`))
	require.NoError(t, err)
	require.Equal(t, []string{server.URL + "/page?q=1"}, subjects)

	got, err := tool.Call(t.Context(), json.RawMessage(`{"url":"`+server.URL+`/page?q=1#section"}`), nil, emptyToolCallMetadata())
	require.NoError(t, err)
	require.Equal(t, []string{"/page?q=1"}, requested)
	require.Equal(t, server.URL+"/page?q=1", got.Data.(*webFetchResult).page.URL)
}

func TestWebFetchChecksEveryRedirectAgainstDenyRules(t *testing.T) {
	denied := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a denied redirect target must not be fetched")
	}))
	t.Cleanup(denied.Close)

	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a redirect target with credentials must not be fetched")
	}))
	t.Cleanup(other.Close)

	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/to-denied":
			http.Redirect(w, r, denied.URL+"/secret", http.StatusFound)
		case r.URL.Path == "/to-exact":
			http.Redirect(w, r, "/exact#top", http.StatusMovedPermanently)
		case r.URL.Path == "/to-file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case r.URL.Path == "/to-credentials":
			http.Redirect(w, r, strings.Replace(other.URL, "://", "://user:pw@", 1)+"/", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/hop/"):
			n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/hop/"))
			if n == 0 {
				_, _ = io.WriteString(w, "end of chain")

				return
			}

			http.Redirect(w, r, "/hop/"+strconv.Itoa(n-1), http.StatusSeeOther)
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	t.Cleanup(allowed.Close)

	permissions := parsePermissionYAML(t, `
webfetch:
  "`+allowed.URL+`/*": allow
  "`+allowed.URL+`/exact": deny
  "`+denied.URL+`/*": deny
`)
	ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: &looper{Permissions: permissions}, observations: nil, output: nil, callID: "call-1"})
	fetch := func(path string) webFetchPage {
		got, err := webFetch(ctx, http.DefaultClient, testWebFetchParams(allowed.URL+path, "text"))
		require.NoError(t, err)

		return got.Data.(*webFetchResult).page
	}

	page := fetch("/to-denied")
	require.Equal(t, `redirect to `+denied.URL+`/secret: tool call denied: permission "webfetch" rejected subject "`+denied.URL+`/secret" by rule "`+denied.URL+`/*" => deny. Choose a different action.`, page.Error)
	require.Equal(t, []any{allowed.URL + "/to-denied", http.StatusFound, []webFetchRedirect{{URL: allowed.URL + "/to-denied", Status: http.StatusFound, Location: denied.URL + "/secret"}}}, []any{page.URL, page.Status, page.Redirects})

	require.Equal(t, `redirect to `+allowed.URL+`/exact: tool call denied: permission "webfetch" rejected subject "`+allowed.URL+`/exact" by rule "`+allowed.URL+`/exact" => deny. Choose a different action.`, fetch("/to-exact").Error)
	require.Equal(t, `redirect to http://user:xxxxx@`+strings.TrimPrefix(other.URL, "http://")+`/: URL must not include credentials`, fetch("/to-credentials").Error)

	require.Equal(t, `redirect to file:///etc/passwd: URL must start with http:// or https://`, fetch("/to-file").Error)

	page = fetch("/hop/10")
	require.Equal(t, []any{"end of chain", "", 10}, []any{page.Output, page.Error, len(page.Redirects)})

	page = fetch("/hop/11")
	require.Equal(t, []any{"too many redirects (more than 10)", 11, allowed.URL + "/hop/1"}, []any{page.Error, len(page.Redirects), page.URL})
}

func TestWebFetchChallengeRetry(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
	}{
		{name: "success", status: http.StatusOK},
		{name: "second challenge", status: http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var agents []string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				agents = append(agents, r.UserAgent())
				if len(agents) == 1 {
					w.Header().Set("Cf-Mitigated", "challenge")
					w.WriteHeader(http.StatusForbidden)

					return
				}

				w.Header().Set("Cf-Mitigated", "challenge")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, "retried content")
			}))

			got, err := webFetch(t.Context(), http.DefaultClient, testWebFetchParams(server.URL, "text"))
			server.Close()

			require.Len(t, agents, 2)
			require.Contains(t, agents[0], "Mozilla/5.0")
			require.Equal(t, "www.rocketable.com", agents[1])

			require.NoError(t, err)

			if tt.status == http.StatusForbidden {
				require.Equal(t, []any{"HTTP 403", "retried content"}, []any{got.Output, got.Data.(*webFetchResult).page.Output})
			} else {
				require.Equal(t, "retried content", got.Output)
			}
		})
	}
}

func TestWebFetchAcceptsLargeBodies(t *testing.T) {
	body := strings.Repeat("x", 12<<20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	got, err := webFetch(t.Context(), http.DefaultClient, testWebFetchParams(server.URL, "text"))
	require.NoError(t, err)
	require.Len(t, got.Output, len(body))
	require.Equal(t, body, got.Data.(*webFetchResult).page.Output)
}

func TestWebFetchTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	t.Cleanup(server.Close)

	params := testWebFetchParams(server.URL, "text")
	params.TimeoutSecond = 1
	start := time.Now()

	_, err := webFetch(t.Context(), http.DefaultClient, params)
	require.EqualError(t, err, `send request: Get "`+server.URL+`": context deadline exceeded`)
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestWebFetchResultInExecute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/image":
			w.Header().Set("Content-Type", "image/png")
		case "/missing":
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "no such page")
		case "/empty":
			w.Header().Set("Content-Type", "text/plain")
		case "/raw":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<p>x & y</p>")
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<html><body><h1>Title</h1><p>a, b</p></body></html>")
		}
	}))
	t.Cleanup(server.Close)

	tool := webFetchTool(http.DefaultClient)
	host := []codemode.HostTool{{
		Details:     codemode.NoDetails{},
		Name:        "webfetch",
		InputSchema: codeModeHostInputSchema("webfetch", &tool.Definition),
		FileArgs:    nil,
		CallValue: func(ctx context.Context, args map[string]any) (starlark.Value, error) {
			raw, err := json.Marshal(args)
			require.NoError(t, err)

			result, err := tool.Call(ctx, raw, nil, emptyToolCallMetadata())
			if err != nil {
				return nil, err
			}

			return result.Data.(*webFetchResult), nil
		},
	}}

	run := func(body string) starlark.Value {
		t.Helper()

		got, err := codemode.Run(t.Context(), "U = "+strconv.Quote(server.URL)+"\ndef main():\n"+body, nil, nil, nil, host)
		require.NoError(t, err)

		return got
	}

	page := `webfetch(url=U + "/page#intro")`
	require.Equal(t, starlark.String(`["webfetch_result",true,true,null,"text/html; charset=utf-8","`+server.URL+`/page","# Title\n\na, b","# Title\n\na, b",true,true,["# Title","","a, b"],200,"OK","text/html; charset=utf-8",[],"GET","`+server.URL+`/page",true]`),
		run("    r = "+page+"\n    return json.encode([type(r), r.ok, bool(r), r.error, r.content_type, r.url, r.output, str(r), r.unwrap() == r.output, r.or_else(1) == r, r.splitlines(), r.status, r.status_text, r.headers[\"content-type\"], r.redirects, r.request[\"method\"], r.request[\"url\"], r.timing_ms[\"total\"] >= r.timing_ms[\"first_byte\"]])\n"))
	encoded := string(run("    return json.encode([" + page + ", webfetch(url=U + \"/raw\", format=\"html\")])\n").(starlark.String))
	require.Contains(t, encoded, `"<p>x & y</p>"`)

	var pages []map[string]any
	require.NoError(t, json.Unmarshal([]byte(encoded), &pages))
	require.Equal(t, []string{"content_type", "headers", "output", "redirects", "request", "status", "status_text", "timing_ms", "url"}, slices.Sorted(maps.Keys(pages[0])))
	require.Equal(t, []any{"text/html", "<p>x & y</p>", server.URL + "/raw", 200.0}, []any{pages[1]["content_type"], pages[1]["output"], pages[1]["url"], pages[1]["status"]})
	require.Equal(t, starlark.String(`[false,true,true,true,false]`), run(`    r = webfetch(url=U + "/empty")
    return json.encode([bool(r), r.ok, r == "", "Title" in `+page+`, "Missing" in `+page+`])
`))
	require.Equal(t, starlark.String(`["error",false,false,"webfetch: Unsupported fetched image content type: image/png",200,"image/png","error",false,"webfetch: HTTP 404","webfetch: HTTP 404\nno such page",404,"Not Found","no such page","text/plain","error","fallback",false,false,false,false,true,404]`),
		run(`    img = webfetch(url=U + "/image")
    missing = webfetch(url=U + "/missing")
    return json.encode([type(img), img.ok, bool(img), img.error, img.status, img.headers["content-type"], type(missing), missing.ok, missing.error, str(missing), missing.status, missing.status_text, missing.output, missing.content_type, type(missing.split(",")), missing.or_else("fallback"), missing == "HTTP 404", missing in ["HTTP 404"], missing in {"HTTP 404": 1}, missing in set(["HTTP 404"]), missing == webfetch(url=U + "/missing"), json.decode(missing).status])
`))
	failed := string(run("    return json.encode(webfetch(url=U + \"/missing\"))\n").(starlark.String))
	require.Contains(t, failed, `"error":"webfetch: HTTP 404"`)
	require.Contains(t, failed, `"status":404`)

	_, err := codemode.Run(t.Context(), "def main():\n    return webfetch(url=\"https://user:pw@docs.example/\").unwrap()\n", nil, nil, nil, host)
	require.EqualError(t, err, "call main: webfetch: URL must not include credentials")
}

func TestWebFetchReplaysFromJournal(t *testing.T) {
	fetches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches++

		w.Header().Set("Content-Type", "text/plain")

		if r.URL.Path == "/gone" {
			w.WriteHeader(http.StatusGone)
		}

		_, _ = io.WriteString(w, "page")
	}))
	t.Cleanup(server.Close)

	var permissions PermissionSet
	require.NoError(t, permissions.Set("webfetch", "*", PermissionAllow))

	journal := recordingJournal()
	observations := &turnObservations{journal: journal, turnID: "turn-1"}
	ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: &looper{Journal: journal, observations: observations, Permissions: permissions}, observations: observations, output: nil, callID: "call-1"})
	tool := webFetchTool(http.DefaultClient)

	for path, want := range map[string]string{"/page": "", "/gone": "HTTP 410"} {
		args := map[string]any{"url": server.URL + path}

		first, err := recordHostCall(ctx, "webfetch", args, false, toolHostCall{name: "webfetch", tool: &tool, output: nil})
		require.NoError(t, err)

		replayed, err := recordHostCall(ctx, "webfetch", args, false, toolHostCall{name: "webfetch", tool: &tool, output: nil})
		require.NoError(t, err)

		page := first.Data.(*webFetchResult).page
		require.Equal(t, []any{"page", want, server.URL + path}, []any{page.Output, page.Error, page.URL})
		require.Equal(t, first, replayed)
	}

	require.Equal(t, 2, fetches)
}

func TestEasyWebFetchNetworkTargets(t *testing.T) {
	unlisted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("unlisted")) }))
	t.Cleanup(unlisted.Close)

	listed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, unlisted.URL+"/", http.StatusFound)

			return
		}

		_, _ = w.Write([]byte("listed"))
	}))
	t.Cleanup(listed.Close)

	data, err := os.ReadFile(filepath.Join("testdata", "network_targets.json"))
	require.NoError(t, err)

	var rows []struct {
		Targets json.RawMessage
		URL     string
		Allow   bool
	}

	hosts := strings.NewReplacer("{listed}", strings.TrimPrefix(listed.URL, "http://"), "{unlisted}", strings.TrimPrefix(unlisted.URL, "http://"))
	require.NoError(t, json.Unmarshal([]byte(hosts.Replace(string(data))), &rows))

	factory := &toolFactory{baseTools: makeSandboxedTools(&sandboxedFileSystem{}, nil)}
	permission := parsePermissionYAML(t, `webfetch: {"http://127.0.0.1:*": allow}`)
	ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: &looper{Permissions: permission}, observations: nil, output: nil, callID: "call-1"})
	fetch := func(agent *Agent, url string) (ToolResult, error) {
		_, hosts := factory.assembleTools(agent)

		got, err := hosts["webfetch"].Call(ctx, json.RawMessage(`{"url":"`+url+`"}`), nil, toolCallMetadata{})
		if err == nil && got.Data.(*webFetchResult).page.Error != "" {
			err = errors.New(got.Output)
		}

		return got, err
	}

	for _, row := range rows {
		t.Run(string(row.Targets)+" "+row.URL, func(t *testing.T) {
			agent := &Agent{Mode: AgentModeEasy, Permission: permission}
			if string(row.Targets) != `"auto"` {
				require.NoError(t, json.Unmarshal(row.Targets, &agent.NetworkTargets))
			}

			_, err := fetch(agent, row.URL)
			require.Equal(t, !row.Allow, err != nil && strings.Contains(err.Error(), "not in allowedNetworkTargets"), err)
		})
	}

	got, err := fetch(&Agent{Permission: permission}, listed.URL+"/redirect")
	require.NoError(t, err)
	require.Equal(t, "unlisted", got.Output, "an advanced agent fetches as before")

	client := easyWebFetchClient(nil)
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		if err := publicAddressOnly(network, "93.184.215.14:80", nil); err != nil {
			return nil, err
		}

		var dialer net.Dialer

		return dialer.DialContext(ctx, network, listed.Listener.Addr().String())
	}

	got, err = webFetch(t.Context(), client, testWebFetchParams("http://public.example/", "text"))
	require.NoError(t, err)
	require.Equal(t, "listed", got.Output, "auto reaches a public address")
}

func TestWebFetchClientKeepsUnnamedHopsPublic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)

	client := webFetchClient(false)

	for _, unnamedHop := range []bool{true, false} {
		ctx := t.Context()
		if unnamedHop {
			ctx = context.WithValue(ctx, publicHopContext{}, true)
		}

		resp, err := doWebFetchRequest(ctx, client, server.URL, "text", false)
		if err == nil {
			require.NoError(t, resp.Body.Close())
		}

		require.Equal(t, unnamedHop, err != nil && strings.Contains(err.Error(), "private address"), err)
	}
}

func TestWebFetchMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final?x=1", http.StatusMovedPermanently)

			return
		}

		w.Header().Set("Content-Type", "text/plain")
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		w.Header().Add("X-Trace", "one")
		w.Header().Add("X-Trace", "two")
		_, _ = io.WriteString(w, "final")
	}))
	t.Cleanup(server.Close)

	ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: &looper{Permissions: parsePermissionYAML(t, `webfetch: {"http://127.0.0.1:*": allow}`)}, callID: "call-1"})
	got, err := webFetch(ctx, http.DefaultClient, testWebFetchParams(server.URL+"/start", "text"))
	require.NoError(t, err)

	page := got.Data.(*webFetchResult).page
	require.LessOrEqual(t, page.TimingMS.FirstByte, page.TimingMS.Total)
	require.Equal(t, []any{"final", "", server.URL + "/final?x=1", http.StatusOK, "OK", "text/plain"}, []any{page.Output, page.Error, page.URL, page.Status, page.StatusText, page.ContentType})
	require.Equal(t, []string{"<redacted>", "one, two", "5"}, []string{page.Headers["set-cookie"], page.Headers["x-trace"], page.Headers["content-length"]})
	require.Equal(t, []webFetchRedirect{{URL: server.URL + "/start", Status: http.StatusMovedPermanently, Location: "/final?x=1"}}, page.Redirects)
	require.Equal(t, webFetchRequest{Method: http.MethodGet, URL: server.URL + "/final?x=1", Headers: map[string]string{
		"accept":          "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1",
		"accept-language": "en-US,en;q=0.9",
		"user-agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36",
	}}, page.Request)

	require.Equal(t, map[string]string{"authorization": "<redacted>", "cookie": "<redacted>", "set-cookie": "<redacted>", "x-a": "1, 2"},
		webFetchHeaders(http.Header{"Authorization": {"Bearer t"}, "Cookie": {"c=1"}, "Set-Cookie": {"a=1", "b=2"}, "X-A": {"1", "2"}}))
}

func TestExecuteWebFetchRedirectReview(t *testing.T) {
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "hop page")
	}))
	t.Cleanup(hop.Close)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/go" {
			http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)

			return
		}

		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "public page")
	}))
	t.Cleanup(origin.Close)

	localHop := "http://localhost:" + hop.URL[strings.LastIndex(hop.URL, ":")+1:] + "/landing"
	hopURL := "http://93.184.215.14/landing"
	originHost := strings.TrimPrefix(origin.URL, "http://")
	viaOrigin := origin.URL + "/go?to=" + hopURL
	private := func(hop string) string {
		return `[false,"webfetch: redirect to ` + hop + `: ` + strings.Trim(strings.TrimSuffix(strings.TrimPrefix(hop, "http://"), "/x"), "[]") + `: private address, not named in this agent's rules",1]`
	}

	for _, tc := range []struct {
		name, rules, first string
		easy               bool
		targets            []string
		hopOutcome         permissionReviewOutcome
		want, approval     string
		reviews            []string
		reviewTime         time.Duration
	}{
		{name: "easy follows a hop listed in allowedNetworkTargets", easy: true, targets: []string{originHost, strings.TrimPrefix(hopURL, "http://")}, first: viaOrigin, want: `[true,"hop page",1]`, reviews: []string{"execute"}},
		{name: "easy follows a private hop listed in allowedNetworkTargets", easy: true, targets: []string{originHost, strings.TrimPrefix(localHop, "http://")}, first: origin.URL + "/go?to=" + localHop, want: `[true,"hop page",1]`, reviews: []string{"execute"}},
		{name: "easy refuses an unlisted private hop without review", easy: true, targets: []string{originHost}, first: origin.URL + "/go?to=http://10.0.0.1/x", hopOutcome: permissionReviewOutcomeAllow, want: private("http://10.0.0.1/x"), reviews: []string{"execute"}},
		{name: "easy refuses the metadata address without review", easy: true, targets: []string{originHost}, first: origin.URL + "/go?to=http://169.254.169.254/x", hopOutcome: permissionReviewOutcomeAllow, want: private("http://169.254.169.254/x"), reviews: []string{"execute"}},
		{name: "easy asks the approver about an unlisted hop", easy: true, targets: []string{originHost}, first: viaOrigin, hopOutcome: permissionReviewOutcomeAllow, want: `[true,"hop page",1]`, approval: "the reviewed script; listed in allowedNetworkTargets", reviews: []string{"execute", "webfetch"}},
		{name: "easy refuses an unlisted hop the approver denies", easy: true, targets: []string{originHost}, first: viaOrigin, hopOutcome: permissionReviewOutcomeDeny, want: `[false,"webfetch: redirect to ` + hopURL + `: tool call denied: automatic permission review rejected the action: no. Choose a different action.",1]`, approval: "the reviewed script; listed in allowedNetworkTargets", reviews: []string{"execute", "webfetch"}},
		{name: "easy auto follows a public hop", easy: true, first: "http://public.example/go?to=http://public.example/landing", want: `[true,"public page",1]`, reviews: []string{"execute"}},
		{name: "easy auto refuses a private hop without review", easy: true, first: "http://public.example/go?to=http://private.example/landing", want: `[false,"webfetch: send request: Get \"http://private.example/landing\": 10.0.0.1:80: private address, not in allowedNetworkTargets",1]`, reviews: []string{"execute"}},
		{name: "advanced follows an allowed hop", rules: "webfetch: allow", first: viaOrigin, want: `[true,"hop page",1]`},
		{name: "advanced refuses a private hop a wildcard allows", rules: "webfetch: allow", first: origin.URL + "/go?to=http://[fd00::1]/x", want: private("http://[fd00::1]/x")},
		{name: "advanced refuses a private hop without asking the guardian", rules: fmt.Sprintf("webfetch:\n  %q: allow", origin.URL+"/*"), first: origin.URL + "/go?to=http://127.0.0.1/x", hopOutcome: permissionReviewOutcomeAllow, want: private("http://127.0.0.1/x")},
		{name: "advanced follows a private hop its rules name", rules: fmt.Sprintf("webfetch:\n  %q: allow\n  %q: allow", origin.URL+"/*", "http://localhost:*/landing"), first: origin.URL + "/go?to=" + localHop, want: `[true,"hop page",1]`},
		{name: "advanced asks the guardian about an auto hop", rules: fmt.Sprintf("webfetch:\n  %q: allow\n  %q: auto", origin.URL+"/*", hopURL), first: viaOrigin, hopOutcome: permissionReviewOutcomeAllow, want: `[true,"hop page",1]`, approval: fmt.Sprintf("rule %q => allow", origin.URL+"/*"), reviews: []string{"webfetch"}},
		{name: "advanced review time does not count against timeout_s", rules: fmt.Sprintf("webfetch:\n  %q: allow", origin.URL+"/*"), first: viaOrigin, hopOutcome: permissionReviewOutcomeAllow, want: `[true,"hop page",1]`, approval: fmt.Sprintf("rule %q => allow", origin.URL+"/*"), reviews: []string{"webfetch"}, reviewTime: 1500 * time.Millisecond},
		{name: "advanced asks the guardian about an unmatched hop", rules: fmt.Sprintf("webfetch:\n  %q: allow", origin.URL+"/*"), first: viaOrigin, hopOutcome: permissionReviewOutcomeAllow, want: `[true,"hop page",1]`, approval: fmt.Sprintf("rule %q => allow", origin.URL+"/*"), reviews: []string{"webfetch"}},
		{name: "advanced refuses a denied hop without review", rules: fmt.Sprintf("webfetch:\n  %q: allow\n  %q: deny", origin.URL+"/*", hopURL), first: viaOrigin, want: `[false,"webfetch: redirect to ` + hopURL + `: tool call denied: permission \"webfetch\" rejected subject \"` + hopURL + `\" by rule \"` + hopURL + `\" => deny. Choose a different action.",1]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := webFetchClient(false)
			rules := cmp.Or(tc.rules, "rocketclaw: {code_mode_approve: auto}\nwebfetch: auto")
			permissions := parsePermissionYAML(t, rules)

			if tc.easy {
				permissions.Easy = true
				client = easyWebFetchClient(tc.targets)
			}

			client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if fake, ok := map[string]string{"public.example:80": "93.184.215.14:80", "private.example:80": "10.0.0.1:80"}[address]; ok {
					if err := publicAddressOnly(network, fake, nil); err != nil {
						return nil, err
					}

					address = originHost
				}

				if address == "93.184.215.14:80" {
					address = strings.TrimPrefix(hop.URL, "http://")
				}

				var dialer net.Dialer

				return dialer.DialContext(ctx, network, address)
			}

			hosts := map[string]looperTool{"webfetch": webFetchTool(client)}
			loop := testLooper(mockResponses())
			loop.agent = Agent{Name: "main", NetworkTargets: tc.targets}
			loop.Tools = (&toolFactory{}).mcpToolsFor(&Agent{Permission: permissions}, hosts)
			loop.Permissions = permissions
			loop.CodeModeHosts = hosts
			reviewer := &mockPermissionReviewer{reviewPermissionFunc: func(_ context.Context, request *permissionReviewRequest) permissionReviewDecision {
				if request.ToolName == "webfetch" {
					time.Sleep(tc.reviewTime)

					return permissionReviewDecision{Outcome: tc.hopOutcome, Rationale: "no"}
				}

				return permissionReviewDecision{Outcome: permissionReviewOutcomeAllow, Rationale: "ok"}
			}}
			loop.PermissionReviewer = reviewer

			code, err := json.Marshal(map[string]string{"code": fmt.Sprintf("FIRST = %q\ndef main():\n    r = webfetch(url=FIRST, format=\"text\", timeout_s=1)\n    return json.encode([r.ok, str(r), len(r.redirects)])\n", tc.first)})
			require.NoError(t, err)

			results, err := loop.dispatchToolCalls(t.Context(), responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{testFunctionCall("tool", "call", executeToolName, string(code))}), nil, nil, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, results[0].Param.Output.OfString.Value)

			var reviewed []string

			for _, request := range reviewedRequests(reviewer) {
				reviewed = append(reviewed, request.ToolName)
				if request.ToolName != "webfetch" {
					continue
				}

				require.Equal(t, []any{"webfetch", []string{hopURL}, `{"format":"text","timeout_s":1,"url":"` + viaOrigin + `"}`}, []any{request.Permission, request.Subjects, request.RawArguments})
				require.Equal(t, &webFetchRedirectReview{
					OriginalURL: viaOrigin, OriginalApproval: tc.approval, Redirects: []webFetchRedirect{{URL: viaOrigin, Status: http.StatusFound, Location: hopURL}},
					Hop: hopURL, ChangesSite: true, ScriptLine: 3, ScriptCode: `r = webfetch(url=FIRST, format="text", timeout_s=1)`,
				}, request.Redirect)
				require.Equal(t, ReviewTurn(CallKey(loop.observations.turnID, "call")+"/host/0/redirect/1"), request.Turn)
			}

			require.Equal(t, tc.reviews, reviewed)
		})
	}
}

func testWebFetchParams(url, format string) webFetchToolParams {
	return webFetchToolParams{URL: url, Format: format, TimeoutSecond: 0}
}
