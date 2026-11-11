package backend

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
)

func TestPageTools(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	threads := &threadBridgeManager{runtime: config.NewLockedConfig(&config.Config{Web: config.WebConfig{ListenAddress: "127.0.0.1:8080", Tsnet: &config.TsnetConfig{Hostname: "rc", Origin: "https://rc.example"}}}), store: store}

	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.WriteFile("chart.js", []byte("draw()"), 0o644))

	call := func(tools []harness.Tool, name string, input any) (string, error) {
		t.Helper()

		raw, err := json.Marshal(input)
		require.NoError(t, err)

		i := slices.IndexFunc(tools, func(tool harness.Tool) bool { return tool.Name == name })
		require.GreaterOrEqual(t, i, 0, name)

		result, err := tools[i].Call(t.Context(), raw, nil)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}

		return result.Output, nil
	}
	must := func(tools []harness.Tool, name string, input any) string {
		t.Helper()

		out, err := call(tools, name, input)
		require.NoError(t, err)

		return out
	}

	reporter := pageSpace{sessions: store, threads: threads, agent: "reporter", space: "Billing"}.tools()
	require.NotContains(t, reporter[0].Parameters["properties"], "source", "only native sessions read workspace files")
	require.NotContains(t, reporter[0].Description, "source")

	for agent, want := range map[string]string{"reporter": "https://rc.example/pages/Billing/reporter/dashboard/index.html", "main": "https://rc.example/pages/-/main/dashboard/index.html", "main/review": "https://rc.example/pages/-/main%2Freview/dashboard/index.html"} {
		tools := pageSpace{sessions: store, threads: threads, agent: agent, space: map[string]string{"reporter": "Billing"}[agent]}.tools()
		require.Equal(t, want, must(tools, "rocketclaw_store", map[string]string{"path": "dashboard/index.html", "content": "<p>" + agent + "</p>"}))
	}

	team := pageSpace{sessions: store, threads: threads, agent: "reporter", space: "Billing Team"}.tools()
	require.Equal(t, "https://rc.example/pages/Billing%20Team/reporter/a%20b/c.html", must(team, "rocketclaw_store", map[string]string{"path": "a b/c.html", "content": "x"}))

	// A native session's store also reads a workspace file; given content wins, and an empty file stores an empty page.
	native := pageSpace{sessions: store, threads: threads, agent: "reporter", space: "Billing"}.nativeTools(root, &harness.CustomShell{})
	require.Contains(t, native[0].Parameters["properties"], "source")
	require.Equal(t, "https://rc.example/pages/Billing/reporter/chart.js", must(native, "rocketclaw_store", map[string]string{"path": "chart.js", "source": "chart.js"}))
	require.JSONEq(t, `{"content_type":"text/javascript","content":"draw()"}`, must(reporter, "rocketclaw_read", map[string]string{"path": "chart.js"}))
	must(native, "rocketclaw_store", map[string]string{"path": "given.txt", "content": "given", "source": "chart.js"})
	require.JSONEq(t, `{"content_type":"text/plain","content":"given"}`, must(reporter, "rocketclaw_read", map[string]string{"path": "given.txt"}))
	require.NoError(t, root.WriteFile("empty.txt", nil, 0o644))
	require.Equal(t, "https://rc.example/pages/Billing/reporter/empty.txt", must(native, "rocketclaw_store", map[string]string{"path": "empty.txt", "source": "empty.txt"}))
	require.JSONEq(t, `{"content_type":"text/plain"}`, must(reporter, "rocketclaw_read", map[string]string{"path": "empty.txt"}))

	require.Equal(t, "https://rc.example/pages/Billing/reporter/logo.png", must(reporter, "rocketclaw_store", map[string]string{"path": "logo.png", "content_base64": base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe})}))
	require.JSONEq(t, `{"content_type":"image/png","content_base64":"//4="}`, must(reporter, "rocketclaw_read", map[string]string{"path": "logo.png"}))

	for _, path := range []string{"../x", "/abs", "a//b", ".", ""} {
		_, err := call(reporter, "rocketclaw_store", map[string]string{"path": path, "content": "x"})
		require.Error(t, err, path)
	}

	_, err = call(reporter, "rocketclaw_store", map[string]string{"path": "big.txt", "content": strings.Repeat("x", 10<<20+1)})
	require.ErrorContains(t, err, "10 MiB")

	require.Equal(t, "stored revenue", must(reporter, "rocketclaw_kv_store", map[string]any{"key": "revenue", "value": map[string]any{"q1": []int{1, 2}}}))
	require.JSONEq(t, `{"q1":[1,2]}`, must(reporter, "rocketclaw_kv_read", map[string]string{"key": "revenue"}))

	_, err = call(reporter, "rocketclaw_kv_store", map[string]string{"key": "empty"})
	require.Error(t, err, "a missing value is not JSON")

	_, err = call(reporter, "rocketclaw_kv_store", map[string]any{"key": "big", "value": bytes.Repeat([]byte("x"), 10<<20)})
	require.ErrorContains(t, err, "10 MiB")

	var listed struct {
		Files []struct {
			Path        string `json:"path"`
			ContentType string `json:"content_type"`
			Size        int    `json:"size"`
			URL         string `json:"url"`
		} `json:"files"`
		Keys []struct {
			Key  string `json:"key"`
			Size int    `json:"size"`
		} `json:"keys"`
	}
	require.NoError(t, json.Unmarshal([]byte(must(team, "rocketclaw_list", struct{}{})), &listed))
	require.Len(t, listed.Files, 6)
	require.Equal(t, "a b/c.html", listed.Files[0].Path)
	require.Equal(t, "text/html", listed.Files[0].ContentType)
	require.Equal(t, 1, listed.Files[0].Size)
	require.Equal(t, "https://rc.example/pages/Billing%20Team/reporter/a%20b/c.html", listed.Files[0].URL)
	require.Equal(t, "revenue", listed.Keys[0].Key)
	require.Len(t, listed.Keys, 1)

	// Tools for main see only main's files, never main/review's (AE7).
	mainTools := pageSpace{sessions: store, threads: threads, agent: "main"}.tools()
	require.NoError(t, json.Unmarshal([]byte(must(mainTools, "rocketclaw_list", struct{}{})), &listed))
	require.Len(t, listed.Files, 1)
	require.Empty(t, listed.Keys)
	require.JSONEq(t, `{"content_type":"text/html","content":"<p>main</p>"}`, must(mainTools, "rocketclaw_read", map[string]string{"path": "dashboard/index.html"}))
	_, err = call(mainTools, "rocketclaw_read", map[string]string{"path": "chart.js"})
	require.Error(t, err)

	require.Equal(t, "deleted chart.js", must(reporter, "rocketclaw_delete", map[string]string{"path": "chart.js", "key": ""}))
	require.Equal(t, "deleted revenue", must(reporter, "rocketclaw_delete", map[string]string{"path": "", "key": "revenue"}))

	for _, input := range []map[string]string{{"path": "chart.js"}, {"key": "revenue"}, {}, {"path": "logo.png", "key": "revenue"}} {
		_, err := call(reporter, "rocketclaw_delete", input)
		require.Error(t, err, input)
	}

	_, err = call(reporter, "rocketclaw_kv_read", map[string]string{"key": "revenue"})
	require.Error(t, err)
}

// Storing an HTML file reaches the people with that page's tab under any spelling of its path, and with the Pages list
// of its space, or of any space for a page without one. A stylesheet, Page Data or a delete reaches nobody, and a person
// limited to other spaces gets nothing.
func TestPageStoreMarksPageTabs(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	threads := &threadBridgeManager{runtime: config.NewLockedConfig(&config.Config{Web: config.WebConfig{Tsnet: &config.TsnetConfig{Origin: "https://rc.example"}}}), store: store}

	const (
		carol = "carol@example.com" // limited to Billing
		dave  = "dave@example.com"  // limited to Main
	)

	require.NoError(t, store.definitions.Do(func(cfg *config.Config) error {
		cfg.Permissions = map[string][]string{carol: {"Billing"}, dave: {"Main"}}
		return nil
	}))
	require.NoError(t, store.CreateLimitSpaces(ctx, []string{"Billing"}))

	spaces, err := store.Spaces(ctx)
	require.NoError(t, err)

	mainID, billing := spaces[0].ID, spaces[1].ID
	linked, dashed, shared := "/pages/Billing/main%2Freview/a%20b/", "/pages/-/main%2Freview/a%20b/index.html", "/pages/Main/main%2Freview/a%20b/index.html"

	for person, tabs := range map[string][]Tab{
		alice: {{Path: linked, Space: billing}, {Path: "/pages", Space: billing}, {Path: "/pages/Billing/main%2Freview/other.html", Space: billing}},
		bob:   {{Path: dashed, Space: mainID}, {Path: "/pages", Space: mainID}, {Path: "/pages/Billing/main/a%20b/index.html", Space: billing}},
		carol: {{Path: "/pages/billing/main%2Freview/a%20b/index.html", Space: billing}},
		dave:  {{Path: linked, Space: mainID}, {Path: "/pages", Space: mainID}},
	} {
		saved, err := store.SaveTabState(ctx, person, &TabState{Tabs: tabs, Space: mainID}, 0)
		require.NoError(t, err)
		require.True(t, saved)
	}

	require.NoError(t, store.ShareTab(ctx, mainID, shared, nil))

	call := func(tools []harness.Tool, name string, input any) {
		t.Helper()

		raw, err := json.Marshal(input)
		require.NoError(t, err)

		_, err = tools[slices.IndexFunc(tools, func(tool harness.Tool) bool { return tool.Name == name })].Call(ctx, raw, nil)
		require.NoError(t, err)
	}

	review := pageSpace{sessions: store, threads: threads, agent: "main/review", space: "Billing"}.tools()
	call(review, "rocketclaw_store", map[string]string{"path": "a b/style.css", "content": "p {}"})
	call(review, "rocketclaw_kv_store", map[string]any{"key": "rows", "value": 1})
	call(review, "rocketclaw_delete", map[string]string{"path": "a b/style.css", "key": ""})

	var marks int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM web_tab_attention`).Scan(&marks))
	require.Zero(t, marks, "only an HTML file marks")

	// Carol's own tab reaches her, but not the tab Shared in Main; nothing reaches Dave.
	call(review, "rocketclaw_store", map[string]string{"path": "a b/index.html", "content": "<p>review</p>"})
	assert.ElementsMatch(t, []string{
		alice + " " + linked + " loud", alice + " /pages loud", alice + " " + shared + " loud",
		bob + " " + dashed + " loud", bob + " " + shared + " loud",
		carol + " /pages/billing/main%2Freview/a%20b/index.html loud",
	}, locationMarks(t, store, "main/review · a b/index.html updated"))

	// A page without a space is on the Pages list of every space, and reaches no limited person.
	call(pageSpace{sessions: store, threads: threads, agent: "main"}.tools(), "rocketclaw_store", map[string]string{"path": "index.html", "content": "<p>main</p>"})
	assert.ElementsMatch(t, []string{alice + " /pages loud", bob + " /pages loud"}, locationMarks(t, store, "main · index.html updated"))
}

// An easy agent stores a page from a file only through its file view, as it attaches one (R30).
func TestPageStoreReadsEasyFileView(t *testing.T) {
	dir := t.TempDir()
	base, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, base.Close()) })

	require.NoError(t, base.Mkdir("workspace", 0o755))
	require.NoError(t, base.Mkdir("folder", 0o755))
	require.NoError(t, base.WriteFile("workspace/rocketclaw.json", []byte("{}"), 0o600))
	require.NoError(t, base.WriteFile("folder/chart.js", []byte("draw()"), 0o600))

	workspace, err := base.OpenRoot("workspace")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, workspace.Close()) })

	store := newTestSessionService(t)
	threads := &threadBridgeManager{runtime: config.NewLockedConfig(&config.Config{Web: config.WebConfig{ListenAddress: "127.0.0.1:8080", Tsnet: &config.TsnetConfig{Hostname: "rc", Origin: "https://rc.example"}}}), store: store}
	pageStore := pageSpace{sessions: store, threads: threads, agent: "easy", space: "Ops"}.nativeTools(workspace, &harness.CustomShell{Emulated: true, Dir: filepath.Join(dir, "folder")})[0]

	_, err = pageStore.Call(t.Context(), json.RawMessage(`{"path":"config.json","source":"rocketclaw.json"}`), nil)
	require.ErrorIs(t, err, fs.ErrNotExist, "a relative source starts from the agent's folder")

	result, err := pageStore.Call(t.Context(), json.RawMessage(`{"path":"chart.js","source":"chart.js"}`), nil)
	require.NoError(t, err)
	require.Equal(t, "https://rc.example/pages/Ops/easy/chart.js", result.Output)

	outside := filepath.Join(dir, "workspace", "rocketclaw.json")
	_, err = pageStore.Call(t.Context(), json.RawMessage(`{"path":"config.json","source":`+strconv.Quote(outside)+`}`), nil)
	require.EqualError(t, err, "read attachment: "+outside+" is outside this agent's folders")
}
