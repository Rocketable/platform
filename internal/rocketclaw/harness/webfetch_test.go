package harness

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
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
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				require.Equal(t, ToolResult{}, got)

				return
			}

			require.NoError(t, err)
			require.Equal(t, ToolResult{Output: tt.body, Data: &webFetchResult{page: webFetchPage{ContentType: tt.contentType, Output: tt.body, URL: server.URL + "/doc"}}}, got)
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

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "unmatched host")
	}))
	t.Cleanup(other.Close)

	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/to-denied":
			http.Redirect(w, r, denied.URL+"/secret", http.StatusFound)
		case r.URL.Path == "/to-exact":
			http.Redirect(w, r, "/exact#top", http.StatusMovedPermanently)
		case r.URL.Path == "/to-other":
			http.Redirect(w, r, other.URL+"/page#frag", http.StatusTemporaryRedirect)
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
	fetch := func(path string) (ToolResult, error) {
		return webFetch(ctx, http.DefaultClient, testWebFetchParams(allowed.URL+path, "text"))
	}

	_, err := fetch("/to-denied")
	require.EqualError(t, err, `send request: Get "`+denied.URL+`/secret": tool call denied: permission "webfetch" rejected subject "`+denied.URL+`/secret" by rule "`+denied.URL+`/*" => deny. Choose a different action.`)

	_, err = fetch("/to-exact")
	require.EqualError(t, err, `send request: Get "/exact#top": tool call denied: permission "webfetch" rejected subject "`+allowed.URL+`/exact" by rule "`+allowed.URL+`/exact" => deny. Choose a different action.`)

	_, err = fetch("/to-credentials")
	require.ErrorContains(t, err, "URL must not include credentials")

	got, err := fetch("/to-other")
	require.NoError(t, err)
	require.Equal(t, ToolResult{Output: "unmatched host", Data: &webFetchResult{page: webFetchPage{ContentType: "text/plain; charset=utf-8", Output: "unmatched host", URL: other.URL + "/page"}}}, got)

	got, err = fetch("/hop/10")
	require.NoError(t, err)
	require.Equal(t, "end of chain", got.Output)

	_, err = fetch("/hop/11")
	require.EqualError(t, err, `send request: Get "/hop/0": too many redirects (more than 10)`)
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

			if tt.status == http.StatusForbidden {
				require.EqualError(t, err, "request failed with status 403")
				require.Equal(t, ToolResult{}, got)
			} else {
				require.NoError(t, err)
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
			w.WriteHeader(http.StatusNotFound)
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
	require.Equal(t, &webFetchResult{page: webFetchPage{ContentType: "text/html; charset=utf-8", Output: "# Title\n\na, b", URL: server.URL + "/page"}}, run("    return "+page+"\n"))
	require.Equal(t, starlark.String(`["webfetch_result",true,true,null,"text/html; charset=utf-8","`+server.URL+`/page","# Title\n\na, b","# Title\n\na, b",true,true,["# Title","","a, b"]]`),
		run("    r = "+page+"\n    return json.encode([type(r), r.ok, bool(r), r.error, r.content_type, r.url, r.output, str(r), r.unwrap() == r.output, r.or_else(1) == r, r.splitlines()])\n"))
	encoded := string(run("    return json.encode([" + page + ", webfetch(url=U + \"/raw\", format=\"html\")])\n").(starlark.String))
	require.JSONEq(t, `[{"content_type":"text/html; charset=utf-8","output":"# Title\n\na, b"},{"content_type":"text/html","output":"<p>x & y</p>"}]`, encoded)
	require.Contains(t, encoded, `"<p>x & y</p>"`)
	require.Equal(t, starlark.String(`[false,true,true,true,false]`), run(`    r = webfetch(url=U + "/empty")
    return json.encode([bool(r), r.ok, r == "", "Title" in `+page+`, "Missing" in `+page+`])
`))
	require.Equal(t, starlark.String(`["error",false,"Unsupported fetched image content type: image/png","request failed with status 404","fallback"]`),
		run(`    img = webfetch(url=U + "/image")
    missing = webfetch(url=U + "/missing")
    return json.encode([type(img), img.ok, img.error, str(missing), missing.or_else("fallback")])
`))

	_, err := codemode.Run(t.Context(), "def main():\n    return webfetch(url=\"https://user:pw@docs.example/\").unwrap()\n", nil, nil, nil, host)
	require.EqualError(t, err, "call main: webfetch: URL must not include credentials")
}

func TestWebFetchReplaysFromJournal(t *testing.T) {
	fetches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches++

		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "page")
	}))
	t.Cleanup(server.Close)

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("webfetch", "*"))

	journal := recordingJournal()
	observations := &turnObservations{journal: journal, turnID: "turn-1"}
	ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: &looper{Journal: journal, observations: observations, Permissions: permissions}, observations: observations, output: nil, callID: "call-1"})
	tool := webFetchTool(http.DefaultClient)
	args := map[string]any{"url": server.URL + "/page"}

	first, err := recordHostCall(ctx, "webfetch", args, false, toolHostCall{name: "webfetch", tool: &tool, output: nil})
	require.NoError(t, err)

	replayed, err := recordHostCall(ctx, "webfetch", args, false, toolHostCall{name: "webfetch", tool: &tool, output: nil})
	require.NoError(t, err)
	require.Equal(t, 1, fetches)
	require.Equal(t, ToolResult{Output: "page", Data: &webFetchResult{page: webFetchPage{ContentType: "text/plain", Output: "page", URL: server.URL + "/page"}}}, first)
	require.Equal(t, first, replayed)
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
	permission := parsePermissionYAML(t, "webfetch: allow")
	ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: &looper{Permissions: permission}, observations: nil, output: nil, callID: "call-1"})
	fetch := func(agent *Agent, url string) (ToolResult, error) {
		_, hosts := factory.assembleTools(agent)
		return hosts["webfetch"].Call(ctx, json.RawMessage(`{"url":"`+url+`"}`), nil, toolCallMetadata{})
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

func testWebFetchParams(url, format string) webFetchToolParams {
	return webFetchToolParams{URL: url, Format: format, TimeoutSecond: 0}
}
