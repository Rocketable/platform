package backend

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
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
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func TestPageTools(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	threads := &threadBridgeManager{runtime: config.NewLockedConfig(&config.Config{Web: config.WebConfig{Tsnet: &config.TsnetConfig{Hostname: "rc", Origin: "https://rc.example"}}}), store: store}

	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.WriteFile("chart.js", []byte("draw()"), 0o644))

	for id, space := range map[string]string{"first": "Atlas", "second": "Atlas", "parent": "Billing Team", "shadow": "", "loose": ""} {
		_, _, err := store.CreateStickyConversation(ctx, protocol.Conversation{ID: id, Agent: "main", Space: space}, harness.AgentStickyOff)
		require.NoError(t, err)
	}

	_, err = store.db.ExecContext(ctx, `UPDATE conversations SET shadow_parent = 'parent' WHERE conversation_id = 'shadow'`)
	require.NoError(t, err)

	tools := func(conversation, fallback string) []harness.Tool {
		return pageSpace{sessions: store, threads: threads, conversation: conversation, fallback: fallback, noSpace: nativePageNoSpace}.tools()
	}
	call := func(tools []harness.Tool, name string, input any) (string, error) {
		t.Helper()

		raw, err := json.Marshal(input)
		require.NoError(t, err)

		i := slices.IndexFunc(tools, func(tool harness.Tool) bool { return tool.Name == name })
		require.GreaterOrEqual(t, i, 0, name)

		result, err := tools[i].Call(ctx, raw, nil)
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

	first, second := tools("first", "Ignored"), tools("second", "")
	require.NotContains(t, first[0].Parameters["properties"], "source", "only native sessions read workspace files")
	require.NotContains(t, first[0].Description, "source")

	require.Equal(t, "https://rc.example/pages/Atlas/index.html", must(first, "rocketclaw_store", map[string]string{"path": "index.html", "content": "<p>one</p>"}))
	require.JSONEq(t, `{"content_type":"text/html","content":"<p>one</p>"}`, must(second, "rocketclaw_read", map[string]string{"path": "index.html"}), "a later chat in the space reads the page")
	must(second, "rocketclaw_store", map[string]string{"path": "index.html", "content": "<p>two</p>"})
	require.JSONEq(t, `{"content_type":"text/html","content":"<p>two</p>"}`, must(first, "rocketclaw_read", map[string]string{"path": "index.html"}), "and replaces it")

	require.Equal(t, "https://rc.example/pages/Billing%20Team/a%20b/c.html", must(tools("shadow", ""), "rocketclaw_store", map[string]string{"path": "a b/c.html", "content": "x"}), "a shadow publishes into its parent's space")
	require.Equal(t, "https://rc.example/pages/Reports/r.html", must(tools("loose", "Reports"), "rocketclaw_store", map[string]string{"path": "r.html", "content": "r"}), "a chat with no Birth Space uses its fallback")

	spaces, err := store.Spaces(ctx)
	require.NoError(t, err)
	require.True(t, slices.ContainsFunc(spaces, func(space Space) bool { return space.Name == "Reports" }), "publishing creates the fallback space")
	require.JSONEq(t, `{"files":[],"keys":[]}`, must(tools("loose", "Nowhere"), "rocketclaw_list", struct{}{}))

	spaces, err = store.Spaces(ctx)
	require.NoError(t, err)
	require.False(t, slices.ContainsFunc(spaces, func(space Space) bool { return space.Name == "Nowhere" }), "only publishing creates a space")

	for name, input := range map[string]any{"rocketclaw_store": map[string]string{"path": "x.html", "content": "x"}, "rocketclaw_read": map[string]string{"path": "r.html"}, "rocketclaw_kv_store": map[string]any{"key": "k", "value": 1}, "rocketclaw_kv_read": map[string]string{"key": "k"}, "rocketclaw_delete": map[string]string{"path": "r.html"}, "rocketclaw_list": struct{}{}} {
		_, err := call(tools("loose", ""), name, input)
		require.EqualError(t, err, name+": "+nativePageNoSpace)
	}

	native := pageSpace{sessions: store, threads: threads, conversation: "first", noSpace: nativePageNoSpace}.nativeTools(root, &harness.CustomShell{})
	require.Contains(t, native[0].Parameters["properties"], "source")
	require.Equal(t, "https://rc.example/pages/Atlas/chart.js", must(native, "rocketclaw_store", map[string]string{"path": "chart.js", "source": "chart.js"}))
	require.JSONEq(t, `{"content_type":"text/javascript","content":"draw()"}`, must(first, "rocketclaw_read", map[string]string{"path": "chart.js"}))
	must(native, "rocketclaw_store", map[string]string{"path": "given.txt", "content": "given", "source": "chart.js"})
	require.JSONEq(t, `{"content_type":"text/plain","content":"given"}`, must(first, "rocketclaw_read", map[string]string{"path": "given.txt"}))
	require.NoError(t, root.WriteFile("empty.txt", nil, 0o644))
	require.Equal(t, "https://rc.example/pages/Atlas/empty.txt", must(native, "rocketclaw_store", map[string]string{"path": "empty.txt", "source": "empty.txt"}))
	require.JSONEq(t, `{"content_type":"text/plain"}`, must(first, "rocketclaw_read", map[string]string{"path": "empty.txt"}))

	require.Equal(t, "https://rc.example/pages/Atlas/logo.png", must(first, "rocketclaw_store", map[string]string{"path": "logo.png", "content_base64": base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe})}))
	require.JSONEq(t, `{"content_type":"image/png","content_base64":"//4="}`, must(first, "rocketclaw_read", map[string]string{"path": "logo.png"}))

	for _, path := range []string{"../x", "/abs", "a//b", ".", ""} {
		_, err := call(first, "rocketclaw_store", map[string]string{"path": path, "content": "x"})
		require.Error(t, err, path)
	}

	_, err = call(first, "rocketclaw_store", map[string]string{"path": "big.txt", "content": strings.Repeat("x", 10<<20+1)})
	require.ErrorContains(t, err, "10 MiB")

	require.Equal(t, "stored revenue", must(first, "rocketclaw_kv_store", map[string]any{"key": "revenue", "value": map[string]any{"q1": []int{1, 2}}}))
	require.JSONEq(t, `{"q1":[1,2]}`, must(second, "rocketclaw_kv_read", map[string]string{"key": "revenue"}))

	_, err = call(first, "rocketclaw_kv_store", map[string]string{"key": "empty"})
	require.Error(t, err, "a missing value is not JSON")

	_, err = call(first, "rocketclaw_kv_store", map[string]any{"key": "big", "value": bytes.Repeat([]byte("x"), 10<<20)})
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
	require.NoError(t, json.Unmarshal([]byte(must(second, "rocketclaw_list", struct{}{})), &listed))
	require.Len(t, listed.Files, 5, "every chat's files in the space")
	require.Equal(t, "chart.js", listed.Files[0].Path)
	require.Equal(t, "text/javascript", listed.Files[0].ContentType)
	require.Equal(t, 6, listed.Files[0].Size)
	require.Equal(t, "https://rc.example/pages/Atlas/chart.js", listed.Files[0].URL)
	require.Len(t, listed.Keys, 1)
	require.Equal(t, "revenue", listed.Keys[0].Key)

	_, err = call(tools("shadow", ""), "rocketclaw_read", map[string]string{"path": "chart.js"})
	require.Error(t, err, "another space's files stay out of reach")

	require.Equal(t, "deleted chart.js", must(second, "rocketclaw_delete", map[string]string{"path": "chart.js", "key": ""}))
	require.Equal(t, "deleted revenue", must(second, "rocketclaw_delete", map[string]string{"path": "", "key": "revenue"}))

	for _, input := range []map[string]string{{"path": "chart.js"}, {"key": "revenue"}, {}, {"path": "logo.png", "key": "revenue"}} {
		_, err := call(first, "rocketclaw_delete", input)
		require.Error(t, err, input)
	}

	_, err = call(first, "rocketclaw_kv_read", map[string]string{"key": "revenue"})
	require.Error(t, err)
}

func TestPageStoreMarksPageTabs(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	threads := &threadBridgeManager{runtime: config.NewLockedConfig(&config.Config{Web: config.WebConfig{Tsnet: &config.TsnetConfig{Origin: "https://rc.example"}}}), store: store}

	const (
		carol = "carol@example.com"
		dave  = "dave@example.com"
	)

	require.NoError(t, store.definitions.Do(func(cfg *config.Config) error {
		cfg.Permissions = map[string][]string{carol: {"Billing"}, dave: {"Main"}}
		return nil
	}))
	require.NoError(t, store.CreateLimitSpaces(ctx, []string{"Billing"}))

	spaces, err := store.Spaces(ctx)
	require.NoError(t, err)

	mainID, billing := spaces[0].ID, spaces[1].ID
	linked, fresh, shared := "/pages/Billing/main%2Freview/a%20b/", "/pages/billing/main/review/a%20b/index.html", "/pages/Billing/main/review/a%20b/index.html"

	for person, tabs := range map[string][]Tab{
		alice: {{Path: linked, Space: billing}, {Path: "/pages", Space: billing}, {Path: "/pages/Billing/main/review/other.html", Space: billing}},
		bob:   {{Path: "/pages/Ops/main/review/a%20b/index.html", Space: mainID}, {Path: "/pages", Space: mainID}, {Path: fresh, Space: mainID}},
		carol: {{Path: "/pages/billing/main%2Freview/a%20b/index.html", Space: billing}},
		dave:  {{Path: linked, Space: mainID}, {Path: "/pages", Space: mainID}},
	} {
		saved, err := store.SaveTabState(ctx, person, &TabState{Tabs: tabs, Space: mainID}, 0)
		require.NoError(t, err)
		require.True(t, saved)
	}

	require.NoError(t, store.ShareTab(ctx, mainID, shared, nil))

	for id, space := range map[string]string{"billing-chat": "Billing", "main-chat": "Main"} {
		_, _, err := store.CreateStickyConversation(ctx, protocol.Conversation{ID: id, Agent: "main", Space: space}, harness.AgentStickyOff)
		require.NoError(t, err)
	}

	call := func(tools []harness.Tool, name string, input any) {
		t.Helper()

		raw, err := json.Marshal(input)
		require.NoError(t, err)

		_, err = tools[slices.IndexFunc(tools, func(tool harness.Tool) bool { return tool.Name == name })].Call(ctx, raw, nil)
		require.NoError(t, err)
	}

	review := pageSpace{sessions: store, threads: threads, conversation: "billing-chat"}.tools()
	call(review, "rocketclaw_store", map[string]string{"path": "main/review/a b/style.css", "content": "p {}"})
	call(review, "rocketclaw_kv_store", map[string]any{"key": "rows", "value": 1})
	call(review, "rocketclaw_delete", map[string]string{"path": "main/review/a b/style.css", "key": ""})

	var marks int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM web_tab_attention`).Scan(&marks))
	require.Zero(t, marks, "only an HTML file marks")

	call(review, "rocketclaw_store", map[string]string{"path": "main/review/a b/index.html", "content": "<p>review</p>"})
	assert.ElementsMatch(t, []string{
		alice + " " + linked + " loud", alice + " /pages loud", alice + " " + shared + " loud",
		bob + " " + fresh + " loud", bob + " " + shared + " loud",
		carol + " /pages/billing/main%2Freview/a%20b/index.html loud",
	}, locationMarks(t, store, "main/review/a b/index.html updated"))

	call(pageSpace{sessions: store, threads: threads, conversation: "main-chat"}.tools(), "rocketclaw_store", map[string]string{"path": "index.html", "content": "<p>main</p>"})
	assert.ElementsMatch(t, []string{bob + " /pages loud", dave + " /pages loud"}, locationMarks(t, store, "index.html updated"))
}

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
	threads := &threadBridgeManager{runtime: config.NewLockedConfig(&config.Config{Web: config.WebConfig{Tsnet: &config.TsnetConfig{Hostname: "rc", Origin: "https://rc.example"}}}), store: store}
	_, _, err = store.CreateStickyConversation(t.Context(), protocol.Conversation{ID: "easy", Agent: "easy", Space: "Ops"}, harness.AgentStickyOff)
	require.NoError(t, err)

	pageStore := pageSpace{sessions: store, threads: threads, conversation: "easy", noSpace: nativePageNoSpace}.nativeTools(workspace, &harness.CustomShell{Emulated: true, Dir: filepath.Join(dir, "folder")})[0]

	_, err = pageStore.Call(t.Context(), json.RawMessage(`{"path":"config.json","source":"rocketclaw.json"}`), nil)
	require.ErrorIs(t, err, fs.ErrNotExist, "a relative source starts from the agent's folder")

	result, err := pageStore.Call(t.Context(), json.RawMessage(`{"path":"chart.js","source":"chart.js"}`), nil)
	require.NoError(t, err)
	require.Equal(t, "https://rc.example/pages/Ops/chart.js", result.Output)

	outside := filepath.Join(dir, "workspace", "rocketclaw.json")
	_, err = pageStore.Call(t.Context(), json.RawMessage(`{"path":"config.json","source":`+strconv.Quote(outside)+`}`), nil)
	require.EqualError(t, err, "read attachment: "+outside+" is outside this agent's folders")
}

func TestPageToolsPublishIntoTheChatsSpace(t *testing.T) {
	t.Parallel()

	for name, cron := range map[string]*protocol.CronjobMessage{"agent space": nil, "cron space": {RelativePath: "cron/report.md", Agent: "primary", Space: "Ops"}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			nt := newNoteTest(t, func(w http.ResponseWriter, r *http.Request, n int, body string) {
				if n > 0 {
					answerNoteTest(w, r, n, body)

					return
				}

				writeResponseFunctionCall(t, w, "store", "execute", struct {
					Code string `json:"code"`
				}{"def main():\n    return rocketclaw_store(path='r.html', content='r', content_base64='', content_type='', source='')\n"})
			})
			writeAgent(t, nt.cfg.Workspace, "primary", "---\ndescription: Agent\nmodel: gpt-6.1-sol\nspace: Billing\npermission:\n  rocketclaw: {rocketclaw_store: allow}\n---\nPrompt\n")
			nt.cfg.Web.Tsnet = &config.TsnetConfig{Origin: "https://rc.example"}

			msg := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "publish", true)
			msg.Cronjob = cron

			manager, _ := nt.run(t, ignoreOutbound)
			require.NoError(t, nt.bridge(t, manager, nt.conversationID).Submit(t.Context(), msg))
			readFinalFor(t, nt.finals, nt.conversationID)
			nt.waitIdle(t, manager, nt.conversationID)

			pages, err := nt.service.Pages(t.Context(), "")
			require.NoError(t, err)
			require.Len(t, pages, 1)
			assert.Equal(t, map[string]string{"agent space": "Billing", "cron space": "Ops"}[name], pages[0].Space, "a chat with no Birth Space publishes into its cron's space, else its agent's")
		})
	}
}
