package backend

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
)

func TestPageTools(t *testing.T) {
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

	reporter := pageTools(store, root, threads, "reporter", "Billing", "")
	for agent, want := range map[string]string{"reporter": "https://rc.example/pages/Billing/reporter/dashboard/index.html", "main": "https://rc.example/pages/-/main/dashboard/index.html", "main/review": "https://rc.example/pages/-/main%2Freview/dashboard/index.html"} {
		tools := pageTools(store, root, threads, agent, map[string]string{"reporter": "Billing"}[agent], "")
		require.Equal(t, want, must(tools, "rocketclaw_store", map[string]string{"path": "dashboard/index.html", "content": "<p>" + agent + "</p>"}))
	}

	team := pageTools(store, root, threads, "reporter", "Billing Team", "")
	require.Equal(t, "https://rc.example/pages/Billing%20Team/reporter/a%20b/c.html", must(team, "rocketclaw_store", map[string]string{"path": "a b/c.html", "content": "x"}))

	require.Equal(t, "https://rc.example/pages/Billing/reporter/chart.js", must(reporter, "rocketclaw_store", map[string]string{"path": "chart.js", "source": "chart.js"}))
	require.JSONEq(t, `{"content_type":"text/javascript","content":"draw()"}`, must(reporter, "rocketclaw_read", map[string]string{"path": "chart.js"}))

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
	require.Len(t, listed.Files, 4)
	require.Equal(t, "a b/c.html", listed.Files[0].Path)
	require.Equal(t, "text/html", listed.Files[0].ContentType)
	require.Equal(t, 1, listed.Files[0].Size)
	require.Equal(t, "https://rc.example/pages/Billing%20Team/reporter/a%20b/c.html", listed.Files[0].URL)
	require.Equal(t, "revenue", listed.Keys[0].Key)
	require.Len(t, listed.Keys, 1)

	// Tools for main see only main's files, never main/review's (AE7).
	mainTools := pageTools(store, root, threads, "main", "", "")
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
