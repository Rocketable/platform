package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionTagToolsBridge(t *testing.T) {
	t.Parallel()

	for _, configured := range []bool{false, true} {
		t.Run(strconv.FormatBool(configured), func(t *testing.T) {
			workspace := t.TempDir()

			groups := "    rocketclaw_set_tag: allow\n"
			if configured {
				groups = "    rocketclaw_set_tag: [[triage, investigating, resolved], [customer, internal]]\n"
			}

			writeMainAgentSkills(t, workspace, "---\nmodel: gpt-6.1-sol\npermission:\n  read: allow\n  rocketclaw:\n    rocketclaw_get_tags: allow\n"+groups+"---\nPrompt\n")
			service := newTestSessionServiceAt(t, workspace)
			recordPrivateHistories(t, service, map[string]ConversationKind{"external_mcp:owning": KindExternalMCPRun})
			// A real PostgreSQL write failure must reach the tool without changing active tags.
			_, err := service.db.ExecContext(t.Context(), `ALTER TABLE conversations ADD CONSTRAINT reject_resolved CHECK (NOT (tags ? 'resolved'))`)
			require.NoError(t, err)

			// Each call is one execute script; tag tools are reachable only inside execute.
			calls := []string{
				`rocketclaw_set_tag(tag="triage")`,
				`rocketclaw_set_tag(tag="customer")`,
				`rocketclaw_set_tag(tag="investigating")`,
				`rocketclaw_set_tag(tag="investigating")`,
				`rocketclaw_get_tags()`,
				`rocketclaw_get_tags()`,
				`rocketclaw_set_tag(tag="resolved")`,
				`rocketclaw_set_tag(tag="urgent")`,
				`rocketclaw_set_tag(tag=12)`,
				`rocketclaw_set_tag()`,
				`rocketclaw_set_tag(tag="triage", conversation_id="other")`,
				`rocketclaw_get_tags(conversation_id="other")`,
				`rocketclaw_get_tags(tag=None)`,
				`rocketclaw_set_tag(tag=None)`,
			}

			var outputs []string

			requests := 0

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Instructions string `json:"instructions"`
					Tools        []struct {
						Name       string          `json:"name"`
						Parameters json.RawMessage `json:"parameters"`
					} `json:"tools"`
					Input []struct {
						Type   string `json:"type"`
						Output string `json:"output"`
					} `json:"input"`
				}
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
					return
				}

				for _, name := range []string{"rocketclaw_set_tag", "rocketclaw_get_tags"} {
					for _, tool := range body.Tools {
						assert.NotEqual(t, name, tool.Name, "tag tools run only inside execute")
					}

					assert.Equal(t, configured, strings.Contains(body.Instructions, name))
				}

				outputs = nil

				for _, item := range body.Input {
					if item.Type == "function_call_output" {
						outputs = append(outputs, item.Output)
					}
				}

				w.Header().Set("Content-Type", "application/json")

				if requests < len(calls) {
					writeRawRunFunctionCall(t, w, fmt.Sprintf("tag-%d", requests), "execute", struct {
						Code string `json:"code"`
					}{"def main():\n    return " + calls[requests] + "\n"})
				} else {
					writeRawRunMessage(t, w, "done", "message", "done")
				}

				requests++
			}))
			defer server.Close()

			cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}
			root, agents, skills, resolver, err := prepareRocketCode(config.NewLockedConfig(cfg), "main", slog.New(slog.DiscardHandler), toolModePersistent)

			require.NoError(t, err)
			defer func() { require.NoError(t, root.Close()) }()

			bridge := &Bridge{runtime: config.NewLockedConfig(cfg), config: Config{ConversationID: "external_mcp:owning", SessionService: service, RequestReload: &runtimeReloaderMock{ReloadFunc: testNoopRestart}}, log: slog.New(slog.DiscardHandler)}
			runtimeConfig := bridge.rocketcodeConfig(workspace, nil, sessionTagTools(service, "external_mcp:owning")...)
			runtimeConfig.Journal = harness.InertJournal{}
			runtimeConfig.ChildSessions = harness.InertChildSessions{}
			runtime, err := harness.NewWithModelResolver(resolver, &runtimeConfig, root, agents, skills, "main", io.Discard)
			require.NoError(t, err)

			for _, name := range []string{"rocketclaw_set_tag", "rocketclaw_get_tags"} {
				host, present := runtime.CodeModeHosts[name]
				require.Equal(t, configured, present)

				if configured {
					schema, err := json.Marshal(host.Definition.Parameters)
					require.NoError(t, err)

					if name == setTagToolName {
						require.JSONEq(t, `{"type":"object","properties":{"tag":{"type":"string"}},"required":["tag"],"additionalProperties":false}`, string(schema))
					} else {
						require.JSONEq(t, `{"type":"object","properties":{},"required":[],"additionalProperties":false}`, string(schema))
					}
				}
			}

			input := make(chan harness.PromptInput, 1)
			input <- harness.PromptInput{Texts: []string{"tag"}, Responses: make(chan harness.ChatResponse, 100)}

			close(input)

			memory := new(memoryStore)
			require.NoError(t, runtime.Loop(t.Context(), input, memory.in(), memory, make(chan os.Signal)))
			require.Len(t, outputs, len(calls))
			tags, err := sessionTags(t.Context(), service.db, "external_mcp:owning")
			require.NoError(t, err)

			if configured {
				require.Equal(t, []string{"customer"}, tags)
				require.Equal(t, []string{`{"tags":["triage"]}`, `{"tags":["customer","triage"]}`, `{"tags":["customer","investigating"]}`, `{"tags":["customer"]}`, `{"tags":["customer"]}`, `{"tags":["customer"]}`}, outputs[:6])
				require.Contains(t, outputs[6], "write session tags")

				for _, output := range outputs[6:] {
					require.Contains(t, output, "failed")
				}

				for i := range memory.entries {
					_, err := service.AppendEntryID(t.Context(), "external_mcp:owning", &memory.entries[i])
					require.NoError(t, err)
				}

				require.NoError(t, service.Stop())
				service = newTestSessionServiceAt(t, workspace)

				for _, freshCall := range []bool{false, true} {
					if freshCall {
						requests = len(calls)
						calls = append(calls, `rocketclaw_set_tag(tag="customer")`)
					}

					runtimeConfig.CustomTools = sessionTagTools(service, "external_mcp:owning")
					runtime, err := harness.NewWithModelResolver(resolver, &runtimeConfig, root, agents, skills, "main", io.Discard)
					require.NoError(t, err)

					input := make(chan harness.PromptInput, 1)
					input <- harness.PromptInput{Texts: []string{"resume"}, Responses: make(chan harness.ChatResponse, 100)}

					close(input)

					require.NoError(t, runtime.Loop(t.Context(), input, storedEntries(t, service, "external_mcp:owning"), memory, make(chan os.Signal)))
					tags, err := sessionTags(t.Context(), service.db, "external_mcp:owning")
					require.NoError(t, err)

					if freshCall {
						require.Empty(t, tags)
						require.JSONEq(t, `{"tags":[]}`, outputs[len(outputs)-1])
					} else {
						require.Equal(t, []string{"customer"}, tags, "completed replay must not toggle again")
					}
				}
			} else {
				require.Empty(t, tags)

				for _, output := range outputs {
					require.Contains(t, output, "failed")
				}
			}
		})
	}
}

func TestSessionToolsStoredContract(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	list := listSessionsTool(service, &threadBridgeManager{slackLookups: noSlackLookups{}})
	get := getSessionTool(service)
	result, err := list.Call(t.Context(), json.RawMessage(`{"criteria":"","since":"2000-01-01T00:00:00Z","until":"","limit":200,"include_message_preview":true}`), nil)
	require.NoError(t, err)
	require.Equal(t, "conversation_id\tturns\tlast_updated\tlast_user_message\tlast_assistant_message\n", result.Output)

	base := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	var privateIDs []int64

	for _, fixture := range []struct {
		id   string
		at   time.Time
		user string
	}{
		{"cron:first", base, "old"},
		{"external_mcp:private", base.Add(time.Hour), "first"},
		{"external_mcp:private", base.Add(-time.Hour), "  latest\n user\tΩ\r\\  "},
		{"exec:last", base.Add(time.Hour), "exec"},
	} {
		id, err := service.AppendEntryID(t.Context(), fixture.id, testSessionEntryAt(fixture.at, fixture.user))
		require.NoError(t, err)

		if fixture.id == "external_mcp:private" {
			privateIDs = append(privateIDs, id)
		}
	}

	for _, tc := range []struct{ input, want string }{
		{`{"since":"2000-01-01T00:00:00Z","until":"","limit":1,"include_message_preview":true}`, "conversation_id\tturns\tlast_updated\tlast_user_message\tlast_assistant_message\nexec:last\t1\t2026-09-24T13:00:00Z\texec\tassistant\n"},
		{`{"since":"2026-09-24T13:00:00Z","until":"","limit":200,"include_message_preview":false}`, "conversation_id\tturns\tlast_updated\nexec:last\t1\t2026-09-24T13:00:00Z\nexternal_mcp:private\t2\t2026-09-24T11:00:00Z\n"},
		{`{"since":"2026-09-24T12:00:00Z","until":"2026-09-24T13:00:00Z","limit":200,"include_message_preview":false}`, "conversation_id\tturns\tlast_updated\ncron:first\t1\t2026-09-24T12:00:00Z\n"},
		{`{"since":"2026-09-24T14:00:00Z","until":"2026-09-24T12:00:00Z","limit":200,"include_message_preview":true}`, "conversation_id\tturns\tlast_updated\tlast_user_message\tlast_assistant_message\n"},
		{`{"since":" 2000-01-01T00:00:00Z ","until":" ","limit":200,"include_message_preview":true}`, "conversation_id\tturns\tlast_updated\tlast_user_message\tlast_assistant_message\nexec:last\t1\t2026-09-24T13:00:00Z\texec\tassistant\nexternal_mcp:private\t2\t2026-09-24T11:00:00Z\t  latest\\n user\\tΩ\\r\\\\  \tassistant\ncron:first\t1\t2026-09-24T12:00:00Z\told\tassistant\n"},
	} {
		// Empty criteria lists every stored conversation, private MCP and cron included.
		for _, criteria := range []string{`""`, `"   "`} {
			result, err := list.Call(t.Context(), json.RawMessage(`{"criteria":`+criteria+`,`+tc.input[1:]), nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, result.Output)
		}
	}

	// Sprintf keeps dupword's autofix from merging the role and the content "assistant".
	first, latest := fmt.Sprintf("2026-09-24T13:00:00Z\tuser\tfirst\n2026-09-24T13:00:00Z\tassistant\t%s\n", "assistant"), fmt.Sprintf("2026-09-24T11:00:00Z\tuser\t  latest\\n user\\tΩ\\r\\\\  \n2026-09-24T11:00:00Z\tassistant\t%s\n", "assistant")
	for _, tc := range []struct {
		limit  int
		before int64
		want   string
	}{
		{100, 0, first + latest},
		{1, 0, latest + fmt.Sprintf("[next_before_entry_id=%d]\n", privateIDs[1])},
		{1, privateIDs[1], first},
		{100, privateIDs[0], ""},
	} {
		result, err = get.Call(t.Context(), json.RawMessage(fmt.Sprintf(`{"conversation_id":" external_mcp:private ","limit":%d,"before_entry_id":%d}`, tc.limit, tc.before)), nil)
		require.NoError(t, err)
		require.Equal(t, "timestamp\trole\tcontent\n"+tc.want, result.Output)
	}

	result, err = get.Call(t.Context(), json.RawMessage(`{"conversation_id":"unknown","limit":100,"before_entry_id":0}`), nil)
	require.NoError(t, err)
	require.Equal(t, "timestamp\trole\tcontent\n", result.Output)

	// The prompt header says who sent a human turn, so readers can tell an operator from a customer.
	headed := &harness.SessionEntry{Version: 1, Type: "turn", Timestamp: base, ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","prompt_header":"[Slack principal=\"Operator\"]","content":"[Slack principal=\"Operator\"]\n\nsend it"}`),
		json.RawMessage(`{"type":"message","role":"user","content":"no header"}`),
	}}
	_, err = service.AppendEntryID(t.Context(), "headed", headed)
	require.NoError(t, err)
	result, err = get.Call(t.Context(), json.RawMessage(`{"conversation_id":"headed","limit":100,"before_entry_id":0}`), nil)
	require.NoError(t, err)
	require.Equal(t, "timestamp\trole\tcontent\n2026-09-24T12:00:00Z\tuser\t[Slack principal=\"Operator\"]\\n\\nsend it\n2026-09-24T12:00:00Z\tuser\tno header\n", result.Output)
}

func TestSessionToolsTimePrecisionAndDurations(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, offset := range []time.Duration{0, 1, 2, time.Second} {
		_, err := service.AppendEntryID(t.Context(), strconv.Itoa(i), testSessionEntryAt(base.Add(offset), "user"))
		require.NoError(t, err)
	}

	list := listSessionsTool(service, &threadBridgeManager{slackLookups: noSlackLookups{}})
	for _, tc := range []struct {
		input string
		ids   []string
	}{
		{`{"since":" 2025-12-31T19:00:00.000000001-05:00 ","until":"2026-01-01T00:00:00.000000002Z","limit":200,"include_message_preview":true}`, []string{"1"}},
		{`{"since":"2025-01-01T00:00:00Z","until":"2026-01-01T00:00:00.000000001Z","limit":200,"include_message_preview":true}`, []string{"0"}},
		{`{"since":"2025-01-01T00:00:00Z","until":"","limit":4,"include_message_preview":true}`, []string{"3", "2", "1", "0"}},
	} {
		result, err := list.Call(t.Context(), json.RawMessage(tc.input), nil)
		require.NoError(t, err)

		rows := strings.Split(strings.TrimSuffix(result.Output, "\n"), "\n")[1:]

		ids := make([]string, len(rows))
		for i, row := range rows {
			ids[i], _, _ = strings.Cut(row, "\t")
		}

		require.Equal(t, tc.ids, ids)
	}

	now := time.Now()
	for _, fixture := range []struct {
		id     string
		offset time.Duration
	}{{"past", -30 * time.Minute}, {"near-future", 30 * time.Minute}, {"future", 2 * time.Hour}} {
		_, err := service.AppendEntryID(t.Context(), fixture.id, testSessionEntryAt(now.Add(fixture.offset), fixture.id))
		require.NoError(t, err)
	}

	for _, tc := range []struct {
		since string
		ids   []string
	}{{"1h", []string{"future", "near-future", "past"}}, {"0s", []string{"future", "near-future"}}, {"-1h", []string{"future"}}} {
		result, err := list.Call(t.Context(), json.RawMessage(fmt.Sprintf(`{"since":%q,"until":"","limit":200,"include_message_preview":true}`, tc.since)), nil)
		require.NoError(t, err)

		rows := strings.Split(strings.TrimSuffix(result.Output, "\n"), "\n")[1:]

		ids := make([]string, len(rows))
		for i, row := range rows {
			ids[i], _, _ = strings.Cut(row, "\t")
		}

		require.Equal(t, tc.ids, ids)
	}
}

func TestSessionToolsReadOnlyRawScope(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	require.NoError(t, service.UpsertThread("web:empty", ThreadState{Agent: "main"}))
	require.NoError(t, service.UpsertThread("web:stored", ThreadState{Agent: "main"}))
	require.NoError(t, service.BeginGoal("web:stored", &GoalState{Objective: "work", MaxTurns: 3}))

	entry := &harness.SessionEntry{Version: 1, Type: "replay", Timestamp: time.Now().UTC(), Model: "model", ResponseID: "response",
		TokenUsage: &harness.TokenUsage{CompletionReasoningTokens: 7},
		ReplayInput: []json.RawMessage{
			json.RawMessage(`{"type":"function_call","call_id":"call","name":"Read","arguments":"{}"}`),
			json.RawMessage(`{"type":"function_call_output","call_id":"call","output":"line\nΩ\t\\\r"}`),
			json.RawMessage(`{"type":"function_call_output","call_id":"parts","output":[{"type":"input_text","text":"text"},{"type":"input_image","image_url":"opaque"}]}`),
			json.RawMessage(`{"type":"message","role":"user","content":""}`),
		},
		OutputTrace: []json.RawMessage{
			json.RawMessage(`{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}],"content":[{"type":"reasoning_text","text":"plaintext"}],"encrypted_content":"sealed"}`),
			json.RawMessage(`{"type":"web_search_call","id":"search","status":"completed","action":{"type":"search","query":"query"}}`),
			json.RawMessage(`{"type":"unknown_provider_item","encrypted_content":"sealed"}`),
			json.RawMessage(`{"type":"rocketcode_code_mode_review","parent_call_id":"exec","outcome":"allow","reason":"The script only reads."}`),
			json.RawMessage(`{"type":"rocketcode_code_mode_call","parent_call_id":"exec","key":"t/call/exec/host/0","name":"bash","arguments":"{\"command\":\"ls\"}","status":"ok","summary":"go.mod","outcome":"allow","reason":"Listing is read-only."}`),
		},
	}
	// A synced producer schedule stores untyped bookkeeping, not a replay message, in its trace.
	schedule := &harness.SessionEntry{Version: 1, Type: "producer_schedule", Timestamp: entry.Timestamp, OutputTrace: []json.RawMessage{json.RawMessage(`{"conversation_id":"producer","agent":"main","message":"later","due_at":"2026-10-07T00:00:00Z"}`)}}
	for _, id := range []string{"external_mcp:private", "cron:one", "exec:one", "slack-thread:C1:1", "web:stored", "unrecorded"} {
		_, err := service.AppendEntryID(t.Context(), id, entry)
		require.NoError(t, err)
		_, err = service.AppendEntryID(t.Context(), id, schedule)
		require.NoError(t, err)
	}

	snapshot := func() []string {
		values := make([]string, 0, 8)

		for _, table := range []string{"entries", "conversations", "turns", "schedules"} {
			var value string

			err := service.db.QueryRowContext(t.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)::text, '[]') FROM `+table+` t`).Scan(&value)
			require.NoError(t, err)

			values = append(values, value)
		}

		return values
	}
	before := snapshot()
	list, get := listSessionsTool(service, &threadBridgeManager{slackLookups: noSlackLookups{}}), getSessionTool(service)
	result, err := list.Call(t.Context(), json.RawMessage(`{"since":"1h","until":"","limit":200,"include_message_preview":true}`), nil)
	require.NoError(t, err)

	rows := strings.Split(strings.TrimSuffix(result.Output, "\n"), "\n")[1:]
	require.Len(t, rows, 6)

	for _, row := range rows {
		fields := strings.Split(row, "\t")
		require.Len(t, fields, 5)
		require.NotEqual(t, "web:empty", fields[0])
		require.Empty(t, fields[3])
		require.Empty(t, fields[4])
		raw, err := json.Marshal(struct {
			ID     string `json:"conversation_id"`
			Limit  int    `json:"limit"`
			Before int64  `json:"before_entry_id"`
		}{fields[0], 100, 0})
		require.NoError(t, err)
		result, err := get.Call(t.Context(), raw, nil)
		require.NoError(t, err)

		at := entry.Timestamp.Format(time.RFC3339Nano)
		require.Equal(t, "timestamp\trole\tcontent\n"+at+"\ttool_call\tRead [call] {}\n"+at+"\ttool_result\t[call] line\\nΩ\\t\\\\\\r\n"+at+"\ttool_result\t[parts] text\\n[non-text tool result omitted]\n"+at+"\tuser\t\n"+at+"\treasoning\tthinking\\nplaintext\n"+at+"\tevent\t[web_search_call: stored event not rendered]\n"+at+"\tevent\t[unknown_provider_item: stored event not rendered]\n"+at+"\tcode_mode\t[exec] (approver allow: The script only reads.)\n"+at+"\tcode_mode\t[exec] bash {\"command\":\"ls\"} -> ok: go.mod (approver allow: Listing is read-only.)\n"+at+"\tevent\t[producer_schedule: stored event not rendered]\n", result.Output)
	}

	require.Equal(t, before, snapshot())
}

func TestSessionToolsErrors(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)

	list, get := listSessionsTool(service, &threadBridgeManager{slackLookups: noSlackLookups{}}), getSessionTool(service)
	for _, input := range []string{`{`, `[]`, `{"since":"1h","until":"","limit":-1,"include_message_preview":true}`, `{"since":"1h","until":"","limit":0,"include_message_preview":true}`, `{"since":"1h","until":"","limit":201,"include_message_preview":true}`, `{"since":"1h","until":"","limit":"1","include_message_preview":true}`, `{"since":"1h","until":"","limit":1.5,"include_message_preview":true}`, `{"since":true,"until":"","limit":1,"include_message_preview":true}`, `{"since":"1h","until":1,"limit":1,"include_message_preview":true}`, `{"since":"1h","until":"","limit":1,"include_message_preview":"false"}`, `{"since":"yesterday","until":"","limit":1,"include_message_preview":true}`, `{"since":"1h","until":"1h","limit":1,"include_message_preview":true}`, `{"since":"","until":"","limit":1,"include_message_preview":true}`, `{"since":" ","until":"","limit":1,"include_message_preview":true}`} {
		_, err := list.Call(t.Context(), json.RawMessage(input), nil)
		require.Error(t, err, input)
	}

	for _, input := range []string{`{`, `{}`, `{"conversation_id":" ","limit":1,"before_entry_id":0}`, `{"conversation_id":12,"limit":1,"before_entry_id":0}`, `{"conversation_id":"x"}`, `{"conversation_id":"x","limit":0,"before_entry_id":0}`, `{"conversation_id":"x","limit":101,"before_entry_id":0}`, `{"conversation_id":"x","limit":1,"before_entry_id":-1}`, `{"conversation_id":"x","limit":1,"before_entry_id":"1"}`} {
		_, err := get.Call(t.Context(), json.RawMessage(input), nil)
		require.Error(t, err, input)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := list.Call(ctx, json.RawMessage(`{"since":"2000-01-01T00:00:00Z","until":"","limit":200,"include_message_preview":true}`), nil)
	require.ErrorIs(t, err, context.Canceled)
	_, err = get.Call(ctx, json.RawMessage(`{"conversation_id":"broken","limit":100,"before_entry_id":0}`), nil)
	require.ErrorIs(t, err, context.Canceled)
	_, err = service.db.ExecContext(t.Context(), `INSERT INTO entries (conversation_id,entry_json,entry_timestamp) VALUES ('broken','{"timestamp":false}','2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	_, err = list.Call(t.Context(), json.RawMessage(`{"since":"2000-01-01T00:00:00Z","until":"","limit":200,"include_message_preview":true}`), nil)
	require.Error(t, err)
	_, err = get.Call(t.Context(), json.RawMessage(`{"conversation_id":"broken","limit":100,"before_entry_id":0}`), nil)
	require.Error(t, err)
	// Bodies outside the selected conversation limit must not be decoded.
	_, err = service.AppendEntryID(t.Context(), "valid", testSessionEntryAt(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "ok"))
	require.NoError(t, err)
	_, err = list.Call(t.Context(), json.RawMessage(`{"since":"2000-01-01T00:00:00Z","until":"","limit":1,"include_message_preview":true}`), nil)
	require.NoError(t, err)
	_, err = service.db.ExecContext(t.Context(), `UPDATE entries SET entry_json = '{"replay_input":[false]}' WHERE conversation_id = 'broken'`)
	require.NoError(t, err)
	_, err = list.Call(t.Context(), json.RawMessage(`{"since":"2000-01-01T00:00:00Z","until":"","limit":200,"include_message_preview":true}`), nil)
	require.Error(t, err)

	// Each undecodable stored item is reported in place without hiding the rest of the read.
	var brokenID int64

	require.NoError(t, service.db.QueryRowContext(t.Context(), `UPDATE entries SET entry_json = '{"replay_input":[{"type":"message","role":"user","content":"before"},false,{"type":"message","role":"user","content":"after"}],"output_trace":[false]}' WHERE conversation_id = 'broken' RETURNING id`).Scan(&brokenID))
	result, err := get.Call(t.Context(), json.RawMessage(`{"conversation_id":"broken","limit":100,"before_entry_id":0}`), nil)
	require.NoError(t, err)

	undecoded := "0001-01-01T00:00:00Z\tevent\t[entry %d %s item %d: stored event not decoded: unmarshal SDK replay input: apijson: was not able to coerce type as union]\n"
	require.Equal(t, "timestamp\trole\tcontent\n0001-01-01T00:00:00Z\tuser\tbefore\n"+fmt.Sprintf(undecoded, brokenID, "replay_input", 1)+"0001-01-01T00:00:00Z\tuser\tafter\n"+fmt.Sprintf(undecoded, brokenID, "output_trace", 0), result.Output)
}

func TestSessionToolsBridgePermissions(t *testing.T) {
	t.Parallel()

	// One rocketclaw_sessions rule offers all five session tools; the old per-tool rules load and offer none.
	for _, tc := range []struct {
		name, rules   string
		visible, auto bool
	}{
		{"allow", "rocketclaw_sessions: allow", true, false},
		{"absent", "{}", false, false},
		{"deny", "rocketclaw_sessions: deny", false, false},
		{"per-tool", "rocketclaw_sessions_list: allow\n    rocketclaw_sessions_get: allow\n    rocketclaw_sessions_current_id: allow\n    rocketclaw_sessions_get_url: allow\n    rocketclaw_sessions_write: allow", false, false},
		{"auto", "rocketclaw_sessions: auto", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeMainAgentSkills(t, workspace, "---\ndescription: Main\nmodel: gpt-6.1-sol\npermission:\n  read: allow\n  rocketclaw:\n    "+tc.rules+"\n---\nPrompt\n")
			service := newTestSessionServiceAt(t, workspace)

			entries := []harness.SessionEntry{
				*testSessionEntry("secret user", "secret answer"),
				{Version: 1, Type: "compaction", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"compaction","id":"cmp_1","encrypted_content":"sealed"}`)}},
				*testSessionEntry("after compaction", "recent answer"),
			}
			for _, entry := range entries {
				_, err := service.AppendEntryID(t.Context(), "external_mcp:private", &entry)
				require.NoError(t, err)
			}

			requests, approvals, listApprovals := 0, 0, 0

			probe := 7
			if tc.visible {
				probe = 8
			}

			var outputs []string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if !assert.NoError(t, err) {
					return
				}

				var body struct {
					Model string `json:"model"`
					Tools []struct {
						Name string `json:"name"`
					} `json:"tools"`
					Input []struct {
						Type   string `json:"type"`
						Output string `json:"output"`
					} `json:"input"`
				}
				if !assert.NoError(t, json.Unmarshal(raw, &body)) {
					return
				}

				w.Header().Set("Content-Type", "application/json")

				if body.Model == "review-model" {
					assert.Contains(t, string(raw), `\"rule_pattern\": \"rocketclaw_sessions\"`, "every session tool is reviewed under the one rule")

					approvals++
					writeRawRunMessage(t, w, fmt.Sprintf("approval-%d", approvals), "approval", `{"risk_level":"low","user_authorization":"unknown","outcome":"allow","rationale":"Read-only inspection."}`)

					return
				}

				requests++
				if requests == 2 {
					// Reviews so far came from the Execute call to rocketclaw_sessions_list alone.
					listApprovals = approvals
				}

				if requests == 1 {
					assert.NotContains(t, string(raw), "secret user")
					assert.Contains(t, string(raw), `"type":"compaction"`)
					assert.Contains(t, string(raw), "after compaction")
				}

				for _, tool := range body.Tools {
					switch tool.Name {
					case sessionsListToolName, sessionsGetToolName, sessionsCurrentIDToolName:
						assert.Failf(t, "execute-only tool is model-facing", "%s", tool.Name)
					}
				}

				outputs = nil

				for _, input := range body.Input {
					if input.Type == "function_call_output" {
						outputs = append(outputs, input.Output)
					}
				}

				if requests == probe {
					// The strict schema stops a call without criteria before the tool runs.
					writeRawRunFunctionCall(t, w, "missing-criteria", "execute", json.RawMessage(`{"code":"def main():\n    return rocketclaw_sessions_list(since=\"1h\", until=\"\", limit=1, include_message_preview=False)\n"}`))

					return
				}

				switch requests {
				case 1:
					writeRawRunFunctionCall(t, w, "list", "execute", json.RawMessage(`{"code":"def main():\n    return rocketclaw_sessions_list(criteria=\"\", since=\"1970-01-01T00:00:00Z\", until=\"\", limit=200, include_message_preview=True)\n"}`))
				case 2:
					writeRawRunFunctionCall(t, w, "get", "execute", json.RawMessage(`{"code":"def main():\n    return rocketclaw_sessions_get(conversation_id=\"external_mcp:private\", limit=100, before_entry_id=0)\n"}`))
				case 3:
					writeRawRunFunctionCall(t, w, "direct-list", sessionsListToolName, json.RawMessage(`{"criteria":"","since":"1970-01-01T00:00:00Z","until":"","limit":200,"include_message_preview":true}`))
				case 4:
					writeRawRunFunctionCall(t, w, "direct-get", sessionsGetToolName, json.RawMessage(`{"conversation_id":"external_mcp:private","limit":100,"before_entry_id":0}`))
				case 5:
					writeRawRunFunctionCall(t, w, "current", "execute", json.RawMessage(`{"code":"def main():\n    return rocketclaw_sessions_current_id()\n"}`))
				case 6:
					writeRawRunFunctionCall(t, w, "direct-current", "rocketclaw_sessions_current_id", json.RawMessage(`{}`))
				case 7:
					code, err := json.Marshal(struct {
						Code string `json:"code"`
					}{fmt.Sprintf("def main():\n    return rocketclaw_sessions_get(conversation_id=%q, limit=100, before_entry_id=0)\n", outputs[4])})
					if !assert.NoError(t, err) {
						return
					}

					writeRawRunFunctionCall(t, w, "current-history", "execute", json.RawMessage(code))
				default:
					writeRawRunMessage(t, w, "done", "message", "done")
				}
			}))
			t.Cleanup(server.Close)
			cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}, AutoApproverModel: "review-model"}
			root, agents, skills, resolver, err := prepareRocketCode(config.NewLockedConfig(cfg), "main", slog.New(slog.DiscardHandler), toolModePersistent)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			require.NoError(t, root.MkdirAll("shell", 0o700))

			bridge := &Bridge{runtime: config.NewLockedConfig(cfg), config: Config{ConversationID: "external_mcp:private", SessionService: service, RequestReload: &runtimeReloaderMock{ReloadFunc: testNoopRestart}}, log: slog.New(slog.DiscardHandler)}
			runtimeConfig := bridge.rocketcodeConfig(filepath.Join(workspace, "shell"), nil, currentSessionIDTool(bridge.config.ConversationID), sessionsWriteTool(bridge, new(protocol.InboundMessage), new(protocol.InboundMessage), "main", bridge.config.ConversationID, agents))
			runtimeConfig.Journal = harness.InertJournal{}
			runtimeConfig.ChildSessions = harness.InertChildSessions{}
			runtime, err := harness.NewWithModelResolver(resolver, &runtimeConfig, root, agents, skills, "main", io.Discard)
			require.NoError(t, err)

			list, hasList := runtime.CodeModeHosts[sessionsListToolName]
			get, hasGet := runtime.CodeModeHosts[sessionsGetToolName]
			current, hasCurrent := runtime.CodeModeHosts[sessionsCurrentIDToolName]
			_, hasURL := runtime.CodeModeHosts[sessionsGetURLToolName]
			_, hasWrite := runtime.CodeModeHosts[sessionsWriteToolName]

			require.Equal(t, []bool{tc.visible, tc.visible, tc.visible, tc.visible, tc.visible}, []bool{hasList, hasGet, hasCurrent, hasURL, hasWrite})

			if tc.visible {
				require.True(t, list.Definition.Strict.Value)
				require.Equal(t, []string{"criteria", "include_message_preview", "limit", "since", "until"}, list.Definition.Parameters["required"])
				require.True(t, get.Definition.Strict.Value)
				require.Equal(t, []string{"before_entry_id", "conversation_id", "limit"}, get.Definition.Parameters["required"])

				schema, err := json.Marshal(current.Definition.Parameters)
				require.NoError(t, err)
				require.JSONEq(t, `{"type":"object","properties":{},"required":[],"additionalProperties":false}`, string(schema))
				require.True(t, current.Definition.Strict.Value)
			}

			input := make(chan harness.PromptInput, 1)
			input <- harness.PromptInput{Texts: []string{"inspect"}, Responses: make(chan harness.ChatResponse, 100)}

			close(input)

			store := &memoryStore{}
			require.NoError(t, runtime.Loop(t.Context(), input, storedEntries(t, service, "external_mcp:private"), store, make(chan os.Signal)))

			require.Equal(t, probe+1, requests)
			require.Len(t, outputs, probe)

			// Session tools are execute-only: direct calls never reach them.
			require.Contains(t, outputs[2], "tool not found")
			require.Contains(t, outputs[3], "tool not found")
			require.Contains(t, outputs[5], "tool not found")

			if tc.visible {
				require.Contains(t, outputs[0], "external_mcp:private")
				require.Contains(t, outputs[probe-1], "invalid arguments")
				require.Contains(t, outputs[probe-1], "criteria")
				require.Equal(t, outputs[1], outputs[6])
				require.Equal(t, "timestamp\trole\tcontent\n1970-01-01T00:00:01Z\tuser\tsecret user\n1970-01-01T00:00:01Z\tassistant\tsecret answer\n0001-01-01T00:00:00Z\tcompaction\tCompaction boundary\n1970-01-01T00:00:01Z\tuser\tafter compaction\n1970-01-01T00:00:01Z\tassistant\trecent answer\n", outputs[1])
				require.Equal(t, "external_mcp:private", outputs[4])
			} else {
				require.Contains(t, outputs[0], "undefined: rocketclaw_sessions_list")
				require.Contains(t, outputs[probe-1], "undefined: rocketclaw_sessions_list")
				require.Contains(t, outputs[1], "undefined: rocketclaw_sessions_get")
				require.Contains(t, outputs[4], "undefined: rocketclaw_sessions_current_id")
			}

			require.Equal(t, tc.auto, approvals > 0)
			require.Equal(t, tc.auto, listApprovals > 0)

			// Calls made inside execute are saved with the turn only as records with a one-line summary.
			for _, entry := range store.entries {
				for _, raw := range entry.OutputTrace {
					if strings.Contains(string(raw), `"parent_call_id"`) {
						require.Contains(t, string(raw), `"type":"rocketcode_code_mode_`)
						require.NotContains(t, string(raw), `"output"`)
					}
				}
			}
		})
	}
}

func TestSessionsGetURLTool(t *testing.T) {
	s, _ := newSessionSearchFixture(t)
	// The fake CLI stands in for the host tailscale binary that RocketClaw shells out to.
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tailscale"), []byte("#!/bin/sh\necho 100.95.197.99\n"), 0o755))
	t.Setenv("PATH", bin)

	for origin, want := range map[string]string{"": "https://100.95.197.99:8080", "https://rocketclaw.example.ts.net": "https://rocketclaw.example.ts.net"} {
		cfg := &config.Config{Web: config.WebConfig{ListenAddress: "127.0.0.1:8080", Tsnet: &config.TsnetConfig{Hostname: "rocketclaw", Origin: origin}}}
		tool := sessionsGetURLTool(&threadBridgeManager{runtime: config.NewLockedConfig(cfg), store: s})

		result, err := tool.Call(t.Context(), json.RawMessage(fmt.Sprintf(`{"session_id":%q}`, searchChatW)), nil)
		require.NoError(t, err)
		require.Equal(t, want+"/s/"+base64.RawURLEncoding.EncodeToString([]byte(searchChatW)), result.Output)

		for _, id := range []string{searchChatD + "/call-1", "cron:cron/daily.md:20260102T030405.000000000Z:run", "external_mcp:private", ""} {
			_, err := tool.Call(t.Context(), json.RawMessage(fmt.Sprintf(`{"session_id":%q}`, id)), nil)
			require.ErrorContains(t, err, "is not a top-level session", id)
		}
	}
}

// R7, R9: the write tool offers steer, enqueue, and stop, and tells the model its reply arrives later
// as a note, so it neither waits nor writes again.
func TestSessionsWriteToolDescription(t *testing.T) {
	write := sessionsWriteTool(new(Bridge), new(protocol.InboundMessage), new(protocol.InboundMessage), "main", "web:sender", harness.Agents{})

	assert.Equal(t, "Create a top-level RocketClaw Web session, or write prompt to an existing one, without waiting for its turn. Supply every field. "+
		"With an empty session_id, create a session named title, run by agent (empty for this turn's agent), whose first turn runs prompt; mode is ignored. The new session cannot see this conversation, so prompt must carry all the context it needs. "+
		"With session_id, deliver prompt to that session; title and agent are ignored. steer joins its running turn while that turn still takes input, otherwise runs as its next turn; enqueue runs after the work already queued; stop stops its current turn and goal, then runs prompt before anything already queued. "+
		"Only stop takes an empty prompt: it then just stops the turn and goal, queues nothing, and posts that this session stopped it into that session's transcript without starting a turn. "+
		"The receiver is told this session and agent sent it and that its answer comes back here. Each call that returns a queue_item_id gets exactly one reply: when the target's turn that took prompt ends, or, for an empty-prompt stop, once the stopped turn is over, a <session_reply> note naming that queue_item_id arrives here with the outcome and that turn's final answer. A steer's answer covers the whole turn it joined. "+
		"DO NOT wait, sleep, poll, or write again to get the reply: work on anything that does not depend on it, or end your response; you will be resumed when it arrives. "+
		"Every call, including create and stop, fails before doing anything when the target agent's permission.rocketclaw.rocketclaw_sessions_recipient, or an easy target's delegatedFrom, does not accept this agent. "+
		"Returns JSON with outcome (created, steered, queued or stopped), session_id, url, queue_item_id (absent only for an empty-prompt stop when no turn was running, which gets no reply) and, for stop, turn_was_running. Share url so a person can open the session.", write.Description)

	mode, err := json.Marshal(write.Parameters["properties"].(map[string]any)["mode"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"string","enum":["stop","steer","enqueue"]}`, string(mode), "agents cannot stash")
}

// A stop write without a prompt stops the turn and goal without waiting, queues nothing, starts no
// turn, and posts who stopped it into the target's transcript. Only a stopped running turn gets a
// reply, so only then does the result carry a queue_item_id. Every other write needs a prompt.
func TestSessionsWriteStopWithoutPrompt(t *testing.T) {
	store := newTestSessionService(t)
	manager := &threadBridgeManager{log: slog.New(slog.DiscardHandler), runtime: config.NewLockedConfig(&config.Config{Web: config.WebConfig{ListenAddress: "127.0.0.1:8080", Tsnet: &config.TsnetConfig{Hostname: "rc", Origin: "https://rc.example"}}}), store: store, bridges: map[string]directBridge{}}
	bridge := runningTestBridge(t, store, manager, "web:target")
	require.NoError(t, store.BeginGoal("web:target", &GoalState{Objective: "ship it", MaxTurns: 5}))

	write := sessionsWriteTool(&Bridge{config: Config{ConversationID: "web:sender", SessionService: store}, threads: manager}, new(protocol.InboundMessage), new(protocol.InboundMessage), "main", "web:sender", harness.Agents{})
	require.False(t, write.Resumable, "a write cut off by a restart is reported interrupted, not run again")

	_, err := store.db.ExecContext(t.Context(), `CREATE FUNCTION reject_goal_stop() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'goal stop failed'; END $$;
CREATE TRIGGER reject_goal_stop BEFORE UPDATE OF goal_status ON conversations FOR EACH STATEMENT EXECUTE FUNCTION reject_goal_stop()`)
	require.NoError(t, err)

	failed := sessionsWriteTool(&Bridge{config: Config{ConversationID: "web:failed", SessionService: store}, threads: manager}, new(protocol.InboundMessage), new(protocol.InboundMessage), "main", "web:failed", harness.Agents{})
	_, err = failed.Call(t.Context(), json.RawMessage(`{"session_id":"web:target","mode":"stop","prompt":"","title":"","agent":""}`), nil)
	require.ErrorContains(t, err, "goal stop failed")
	_, err = store.db.ExecContext(t.Context(), `DROP TRIGGER reject_goal_stop ON conversations; DROP FUNCTION reject_goal_stop()`)
	require.NoError(t, err)

	output, err := write.Call(t.Context(), json.RawMessage(`{"session_id":"web:target","mode":"stop","prompt":" ","title":"","agent":""}`), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":"stopped","session_id":"web:target","url":"https://rc.example/s/`+base64.RawURLEncoding.EncodeToString([]byte("web:target"))+`","queue_item_id":"web:sender/","turn_was_running":true}`, output.Output)
	assert.Equal(t, os.Interrupt, <-bridge.activeTurnInterrupts)

	idle := runningTestBridge(t, store, manager, "web:idle")
	idle.handling, idle.activeReply, idle.activeTurnCancel, idle.activeTurnInterrupts = false, nil, nil, nil
	output, err = write.Call(t.Context(), json.RawMessage(`{"session_id":"web:idle","mode":"stop","prompt":"","title":"","agent":""}`), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":"stopped","session_id":"web:idle","url":"https://rc.example/s/`+base64.RawURLEncoding.EncodeToString([]byte("web:idle"))+`","turn_was_running":false}`, output.Output)

	goal, _, err := store.Goal("web:target")
	require.NoError(t, err)
	assert.Equal(t, GoalStatusStopped, goal.Status)
	assert.Empty(t, queueIDs(t, store, "web:target"), "nothing is queued")
	assert.Empty(t, bridge.requestCh, "the posted line starts no turn")

	entries, err := store.ObserveEntries(t.Context(), "web:target")
	require.NoError(t, err)
	require.Len(t, entries, 1, "a stop that failed never posts its line")

	messages, err := replayInputMessages(entries[0].Entry.ReplayInput)
	require.NoError(t, err)
	assert.Equal(t, []replayInputMessage{{role: "developer", text: "Stopped by session web:sender (agent )"}}, messages, "the next turn reads it and Web shows it")

	for _, raw := range []string{`{"session_id":"web:target","mode":"steer","prompt":"","title":"","agent":""}`, `{"session_id":"","mode":"stop","prompt":"","title":"Child","agent":""}`} {
		_, err = write.Call(t.Context(), json.RawMessage(raw), nil)
		require.ErrorContains(t, err, "prompt is required", raw)
	}

	_, err = write.Call(t.Context(), json.RawMessage(`{"session_id":"web:other","mode":"stop","prompt":"","title":"","agent":""}`), nil)
	require.ErrorContains(t, err, "is not a top-level session")
}

// A stop write without a prompt to a running turn posts its line once, after the stopped turn saves
// its history, and starts no turn.
func TestSessionsWriteStopLineFollowsStoppedTurn(t *testing.T) {
	t.Parallel()

	nt := newNoteTest(t, func(_ http.ResponseWriter, r *http.Request, _ int, _ string) { <-r.Context().Done() })
	nt.cfg.Web = config.WebConfig{ListenAddress: "127.0.0.1:8080", Tsnet: &config.TsnetConfig{Hostname: "rc", Origin: "https://rc.example"}}
	manager, _ := nt.run(t, ignoreOutbound)

	require.NoError(t, nt.bridge(t, manager, nt.conversationID).Submit(t.Context(), nt.slackPrompt("hello")))
	nt.request(t)

	write := sessionsWriteTool(&Bridge{config: Config{ConversationID: "web:sender", SessionService: nt.service}, threads: manager}, new(protocol.InboundMessage), new(protocol.InboundMessage), "main", "web:sender", harness.Agents{})
	output, err := write.Call(t.Context(), json.RawMessage(`{"session_id":"`+nt.conversationID+`","mode":"stop","prompt":"","title":"","agent":""}`), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":"stopped","session_id":"`+nt.conversationID+`","url":"https://rc.example/s/`+base64.RawURLEncoding.EncodeToString([]byte(nt.conversationID))+`","queue_item_id":"web:sender/","turn_was_running":true}`, output.Output)

	var early int
	require.NoError(t, nt.service.db.QueryRowContext(t.Context(), `WITH running AS (SELECT count(*) AS turns FROM turns WHERE conversation_id = $1 AND phase = $2)
SELECT COUNT(*) FROM entries, running WHERE conversation_id = $1 AND turns > 0`, nt.conversationID, turnRunning).Scan(&early))
	assert.Zero(t, early, "nothing is posted while the stopped turn has not saved")

	assert.Equal(t, protocol.TerminalStopped, readFinal(t, nt.finals).Terminal)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Empty(t, nt.requests, "the posted line starts no turn")
	assert.Empty(t, queueIDs(t, nt.service, nt.conversationID), "nothing is queued")
	require.NoError(t, nt.service.postStopLines(t.Context(), nt.conversationID), "a later flush posts nothing again")

	transcript, err := nt.service.ObserveTranscript(t.Context(), nt.conversationID, 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, transcript, 2)
	assert.Equal(t, protocol.TerminalStopped, transcript[0].Terminal, "the stopped turn comes first")

	messages, err := replayInputMessages(transcript[1].Entry.ReplayInput)
	require.NoError(t, err)
	assert.Equal(t, []replayInputMessage{{role: "developer", text: "Stopped by session web:sender (agent )"}}, messages, "the line is last, once")

	// AE4: the stop's reply settles with the line, naming how the stopped turn ended.
	assert.Equal(t, []string{"web:sender web:sender/ stopped pending true"}, replyQueueItems(t, nt.service))
	replies, err := nt.service.notedBackgroundJobs(t.Context(), "web:sender", []string{"web:sender/"})
	require.NoError(t, err)
	require.Len(t, replies, 1)
	assert.Contains(t, backgroundNote(&replies[0]), "Session "+nt.conversationID+"'s turn that your stop interrupted is over (state stopped).")
}

// AE6, AE7: the target agent's rocketclaw_sessions_recipient decides which calling agents may create,
// steer, enqueue into, or stop its sessions. The calling agent counts, not the top-level turn's agent
// main, and a refused write leaves nothing behind.
func TestSessionsWriteRecipients(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	for name, recipient := range map[string]string{"main": "", "researcher": "", "ops": "false", "lead": "[main]", "open": "true"} {
		if recipient != "" {
			recipient = "    " + recipientSetting + ": " + recipient + "\n"
		}

		writeAgent(t, workspace, name, "---\nmodel: gpt-6.1-sol\npermission:\n  rocketclaw:\n    rocketclaw_sessions: allow\n"+recipient+"---\nPrompt\n")
	}

	// A sudo agent refuses every agent unless its own setting says otherwise.
	writeAgent(t, workspace, "root", "---\nmodel: gpt-6.1-sol\nsudo: yes\n---\nPrompt\n")
	writeAgent(t, workspace, "admin", "---\nmodel: gpt-6.1-sol\nsudo: yes\npermission:\n  rocketclaw:\n    "+recipientSetting+": [researcher]\n---\nPrompt\n")

	// An easy agent's delegatedFrom says the same; omitted, any agent may write.
	for name, delegatedFrom := range map[string]string{"easyopen": "", "easyclosed": "delegatedFrom: false\n", "easylead": "delegatedFrom: [main]\n"} {
		writeAgent(t, workspace, name, "---\nmode: easy\nmodel: gpt-6.1-sol\nspace: Ops\nworkingDirectory: work\n"+delegatedFrom+"---\nPrompt\n")
	}

	store := newTestSessionServiceAt(t, workspace)
	cfg := &config.Config{Workspace: workspace, Web: config.WebConfig{ListenAddress: "127.0.0.1:8080", Tsnet: &config.TsnetConfig{Hostname: "rc", Origin: "https://rc.example"}}}
	manager := &threadBridgeManager{log: slog.New(slog.DiscardHandler), runtime: config.NewLockedConfig(cfg), store: store, bridges: map[string]directBridge{}}

	targets := map[string]*Bridge{}
	for _, agent := range []string{"main", "ops", "lead", "open", "root", "admin", "easyopen", "easyclosed", "easylead"} {
		targets[agent] = runningTestBridge(t, store, manager, "web:"+agent)
		require.NoError(t, store.UpsertThread("web:"+agent, ThreadState{Agent: agent}))
	}

	require.NoError(t, store.BeginGoal("web:ops", &GoalState{Objective: "keep going", MaxTurns: 5}))

	writes := []struct{ target, call string }{
		{"ops", `session_id="", mode="enqueue", prompt="p", title="Ops", agent="ops"`},
		{"ops", `session_id="web:ops", mode="stop", prompt="", title="", agent=""`},
		{"ops", `session_id="web:ops", mode="stop", prompt="p", title="", agent=""`},
		{"ops", `session_id="web:ops", mode="steer", prompt="p", title="", agent=""`},
		{"ops", `session_id="web:ops", mode="enqueue", prompt="p", title="", agent=""`},
		{"lead", `session_id="web:lead", mode="enqueue", prompt="p", title="", agent=""`},
		{"open", `session_id="web:open", mode="enqueue", prompt="p", title="", agent=""`},
		{"main", `session_id="web:main", mode="enqueue", prompt="p", title="", agent=""`},
		{"root", `session_id="web:root", mode="enqueue", prompt="p", title="", agent=""`},
		{"admin", `session_id="web:admin", mode="enqueue", prompt="p", title="", agent=""`},
		{"easyopen", `session_id="web:easyopen", mode="enqueue", prompt="p", title="", agent=""`},
		{"easyclosed", `session_id="web:easyclosed", mode="enqueue", prompt="p", title="", agent=""`},
		{"easylead", `session_id="web:easylead", mode="enqueue", prompt="p", title="", agent=""`},
	}

	for _, sender := range []string{"main", "researcher", "ops"} {
		var outputs []string

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Input []struct {
					Type   string `json:"type"`
					Output string `json:"output"`
				} `json:"input"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				return
			}

			outputs = nil

			for _, item := range body.Input {
				if item.Type == "function_call_output" {
					outputs = append(outputs, item.Output)
				}
			}

			w.Header().Set("Content-Type", "application/json")

			if len(outputs) == len(writes) {
				writeRawRunMessage(t, w, "done", "message", "done")

				return
			}

			arguments, _ := json.Marshal(struct {
				Code string `json:"code"`
			}{"def main():\n    return rocketclaw_sessions_write(" + writes[len(outputs)].call + ")\n"}) // Encoding a string cannot fail.
			_, _ = fmt.Fprintf(w, `{"id":"resp_%[1]d","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"fc_%[1]d","type":"function_call","status":"completed","call_id":"call_%[1]d","name":"execute","arguments":%[2]q}]}`, len(outputs), arguments)
		}))
		t.Cleanup(server.Close)

		senderCfg := *cfg
		senderCfg.OpenAI = config.OpenAIConfig{APIBaseURL: server.URL}
		root, agents, skills, resolver, err := prepareRocketCode(config.NewLockedConfig(&senderCfg), sender, slog.New(slog.DiscardHandler), toolModePersistent)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })

		// Every sender acts for a top-level turn of main, as a helper under Main does.
		caller := &Bridge{runtime: config.NewLockedConfig(&senderCfg), config: Config{ConversationID: "web:caller-" + sender, SessionService: store}, threads: manager, log: slog.New(slog.DiscardHandler)}
		runtimeConfig := caller.rocketcodeConfig(workspace, nil, sessionsWriteTool(caller, new(protocol.InboundMessage), new(protocol.InboundMessage), "main", caller.config.ConversationID, agents))
		runtimeConfig.Journal = harness.InertJournal{}
		runtimeConfig.ChildSessions = harness.InertChildSessions{}
		runtime, err := harness.NewWithModelResolver(resolver, &runtimeConfig, root, agents, skills, sender, io.Discard)
		require.NoError(t, err)

		input := make(chan harness.PromptInput, 1)
		input <- harness.PromptInput{Texts: []string{"write"}, Responses: make(chan harness.ChatResponse, 100)}

		close(input)

		memory := new(memoryStore)
		require.NoError(t, runtime.Loop(t.Context(), input, memory.in(), memory, make(chan os.Signal)))
		require.Len(t, outputs, len(writes))

		for i, write := range writes {
			setting := "permission.rocketclaw." + recipientSetting
			if strings.HasPrefix(write.target, "easy") {
				setting = "delegatedFrom"
			}

			refusal := fmt.Sprintf("agent %q does not accept writes from agent %q (%s)", write.target, sender, setting)
			if write.target == "ops" || write.target == "root" || write.target == "easyclosed" || (write.target == "lead" || write.target == "easylead") && sender != "main" || write.target == "admin" && sender != "researcher" {
				assert.Contains(t, outputs[i], refusal, "%s writes %s", sender, write.call)
			} else {
				assert.Contains(t, outputs[i], `"outcome":"queued"`, "%s writes %s", sender, write.call)
			}
		}
	}

	assert.Empty(t, queueIDs(t, store, "web:ops"), "a refused write queues nothing")
	assert.Empty(t, targets["ops"].steers, "a refused steer joins nothing")
	assert.Empty(t, targets["ops"].activeTurnInterrupts, "a refused stop stops nothing")

	goal, _, err := store.Goal("web:ops")
	require.NoError(t, err)
	assert.Equal(t, GoalStatusActive, goal.Status)

	var parked, opsSessions int
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM steps WHERE conversation_id = 'web:ops'`).Scan(&parked))
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM conversations WHERE agent = 'ops'`).Scan(&opsSessions))
	assert.Zero(t, parked, "a refused stop parks no line")
	assert.Equal(t, 1, opsSessions, "a refused create records no session")

	for target, accepted := range map[string]int{"lead": 1, "open": 3, "main": 3, "root": 0, "admin": 1, "easyopen": 3, "easyclosed": 0, "easylead": 1} {
		assert.Len(t, queueIDs(t, store, "web:"+target), accepted, target)
	}
}

// Inside a turn, the write tool creates a session and writes to a Slack thread session. Both run the
// prompt as a system turn under the sender header, and the thread's reply posts where a Web input's
// reply would. Under auto every write is reviewed.
func TestSessionsWriteRunsInTurn(t *testing.T) {
	const caller = "web:caller"

	script := func(callID, code string) string {
		arguments, _ := json.Marshal(struct {
			Code string `json:"code"`
		}{code}) // Encoding a string cannot fail.

		return fmt.Sprintf(`{"id":"resp_%[1]s","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"fc_%[1]s","type":"function_call","status":"completed","call_id":%[1]q,"name":"execute","arguments":%[2]q}]}`, callID, arguments)
	}

	for _, tc := range []struct {
		value           string
		finals, reviews int
	}{{"allow", 3, 0}, {"auto", 3, 2}, {"sudo", 3, 3}} {
		t.Run(tc.value, func(t *testing.T) {
			permission := "permission:\n  rocketclaw: {rocketclaw_sessions: " + tc.value + "}"
			if tc.value == "sudo" {
				// Sudo reviews the script and each write. Its own sessions accept its writes only when it says so.
				permission = "sudo: yes\npermission:\n  rocketclaw: {rocketclaw_sessions_recipient: true}"
			}

			nt := newNoteTest(t, func(w http.ResponseWriter, r *http.Request, n int, body string) {
				switch {
				case strings.Contains(body, `"model":"review-model"`):
					_, _ = fmt.Fprintf(w, `{"id":"resp_review","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_review","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q,"annotations":[]}]}]}`, `{"risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"Requested."}`)
				case strings.Contains(body, "message from session "+caller+" (agent main)"), strings.Contains(body, `\"outcome\":\"queued\"`):
					answerNoteTest(w, r, n, body)
				default:
					_, _ = w.Write([]byte(script("write", "def main():\n    return rocketclaw_sessions_write(session_id='', mode='stop', prompt='child task', title='Child', agent='') + '|' + rocketclaw_sessions_write(session_id='slack-thread:C123:111.222', mode='steer', prompt='also check 2025', title='', agent='')\n")))
				}
			})
			writeAgent(t, nt.cfg.Workspace, "main", "---\ndescription: Agent\nmodel: gpt-6.1-sol\n"+permission+"\n---\nPrompt\n")
			nt.cfg.AutoApproverModel = "review-model"
			nt.cfg.Web = config.WebConfig{ListenAddress: "127.0.0.1:8080", Tsnet: &config.TsnetConfig{Hostname: "rc", Origin: "https://rc.example"}}
			require.NoError(t, nt.service.UpsertThread(caller, ThreadState{Agent: "main"}))

			manager, _ := nt.run(t, ignoreOutbound)

			prompt := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "go", true)
			prompt.Metadata = map[string]string{protocol.InboundPrincipalMetadataKey: "Alice"}
			require.NoError(t, nt.bridge(t, manager, caller).Submit(t.Context(), prompt))

			finals := map[string]*protocol.OutboundMessage{}
			for len(finals) < tc.finals {
				final := readFinal(t, nt.finals)
				finals[final.ConversationID] = final
			}

			created, err := queryStrings(t.Context(), nt.service.db, `SELECT conversation_id FROM conversations WHERE name = 'Child' AND created_by = 'Alice'`, "created sessions")
			require.NoError(t, err)
			require.Len(t, created, 1, "the creator is recorded")
			require.Contains(t, finals, created[0])

			// Each write's reply, the create's after the new session's first turn, reaches the caller once.
			require.Eventually(t, func() bool {
				rows := replyQueueItems(t, nt.service)
				return len(rows) == 2 && !slices.ContainsFunc(rows, func(row string) bool { return !strings.HasSuffix(row, " completed consumed true") })
			}, 10*time.Second, 10*time.Millisecond)
			nt.waitIdle(t, manager, caller)

			var bodies []string

			reviews := 0

			// A reply can join the caller's turn after its script or wake it later, so only the requests
			// each write must cause are looked up below.
			for len(nt.requests) > 0 {
				if body := <-nt.requests; strings.Contains(body, `"model":"review-model"`) {
					reviews++
				} else {
					bodies = append(bodies, body)
				}
			}

			assert.Equal(t, tc.reviews, reviews)

			thread := finals[nt.conversationID]
			require.NotNil(t, thread)
			assert.Nil(t, thread.SlackReply, "the thread's reply posts to its thread as a Web input's does")
			assert.Equal(t, "answer", thread.Text)

			last := bodies[slices.IndexFunc(bodies, func(body string) bool { return strings.Contains(body, `\"outcome\":\"queued\"`) })]
			assert.Contains(t, last, fmt.Sprintf(`{\"outcome\":\"created\",\"session_id\":\"%[1]s\",\"url\":\"https://rc.example/s/%[2]s\",\"queue_item_id\":\"%[3]s/`, created[0], base64.RawURLEncoding.EncodeToString([]byte(created[0])), caller))
			assert.Contains(t, last, `\"session_id\":\"slack-thread:C123:111.222\",\"url\":\"https://rc.example/s/`)

			for _, text := range []string{"child task", "also check 2025"} {
				index := slices.IndexFunc(bodies, func(body string) bool {
					return strings.Contains(body, "message from session "+caller) && strings.Contains(lastRequestMessage(t, body), text)
				})
				require.GreaterOrEqual(t, index, 0, text)
				assert.True(t, strings.HasPrefix(lastRequestMessage(t, bodies[index]), `[System sender="message from session `+caller+` (agent main); your final answer returns to that session automatically, so do not write back to it"`), lastRequestMessage(t, bodies[index]))
			}
		})
	}
}

// replyNoteTest runs the Web session web:caller, whose agent main may write to sessions, beside nt's
// Slack thread session. Its first turn's script runs code, and answer answers each request that
// carries a session reply note, a turn of a session the caller wrote to, or the caller after its script.
func replyNoteTest(t *testing.T, code string, answer noteTestResponder) *noteTest {
	t.Helper()

	nt := newNoteTest(t, func(w http.ResponseWriter, r *http.Request, n int, body string) {
		if strings.Contains(body, "<session_reply") || strings.Contains(body, "message from session") || strings.Contains(body, "function_call_output") {
			answer(w, r, n, body)
			return
		}

		writeRawRunFunctionCall(t, w, "resp", "execute", struct {
			Code string `json:"code"`
		}{"def main():\n    return " + code + "\n"})
	})
	writeAgent(t, nt.cfg.Workspace, "main", "---\ndescription: Agent\nmodel: gpt-6.1-sol\npermission:\n  rocketclaw: {rocketclaw_sessions: allow}\n---\nPrompt\n")
	nt.cfg.Web = config.WebConfig{ListenAddress: "127.0.0.1:8080", Tsnet: &config.TsnetConfig{Hostname: "rc", Origin: "https://rc.example"}}
	require.NoError(t, nt.service.UpsertThread("web:caller", ThreadState{Agent: "main"}))

	return nt
}

// replyQueueItems lists the reply rows' queue item IDs with their state, note, and whether they wake.
func replyQueueItems(t *testing.T, service *SessionService) []string {
	t.Helper()

	rows, err := queryStrings(t.Context(), service.db, `SELECT j.conversation_id || ' ' || j.input_id || ' ' || `+testJobStatusSQL+` || ' ' || j.note || ' ' || j.wake::text`+testJobsSQL+` AND j.kind = 'reply' ORDER BY j.created_at_unix_ns`, "reply rows")
	require.NoError(t, err)

	return rows
}

// nextReplyBody returns the next request text that carried a session reply.
func nextReplyBody(t *testing.T, bodies <-chan string) string {
	t.Helper()

	select {
	case body := <-bodies:
		return body
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a session reply")
		return ""
	}
}

// AE1: an enqueue's reply reaches the caller as one note once the target's turn that ran it ends. An
// idle caller wakes once for it; a busy one takes it into its running turn and wakes no more.
func TestSessionWriteReplyReachesCaller(t *testing.T) {
	t.Parallel()

	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprintf("busy=%t", busy), func(t *testing.T) {
			t.Parallel()

			callerDone, targetDone, noted := make(chan struct{}), make(chan struct{}), make(chan string, 4)
			nt := replyNoteTest(t, "rocketclaw_sessions_write(session_id='slack-thread:C123:111.222', mode='enqueue', prompt='summarize billing', title='', agent='')", func(w http.ResponseWriter, r *http.Request, _ int, body string) {
				target := strings.Contains(body, "message from session")
				if strings.Contains(body, "<session_reply") {
					noted <- lastRequestMessage(t, body)

					writeRawRunMessage(t, w, "resp", "msg", "noted")

					return
				}

				// An idle caller ends its turn before the target answers; a busy one answers after it.
				if wait := map[bool]chan struct{}{true: callerDone, false: targetDone}[target]; target != busy {
					select {
					case <-wait:
					case <-r.Context().Done():
						return
					}
				}

				writeRawRunMessage(t, w, "resp", "msg", map[bool]string{true: "billing is fine", false: "waiting"}[target])
			})
			manager, _ := nt.run(t, ignoreOutbound)

			prompt := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "go", true)
			prompt.Metadata = map[string]string{protocol.InboundPrincipalMetadataKey: "Alice"}
			require.NoError(t, nt.bridge(t, manager, "web:caller").Submit(t.Context(), prompt))

			if busy {
				assert.Equal(t, "billing is fine", readFinalFor(t, nt.finals, nt.conversationID).Text)
				close(targetDone)
			} else {
				assert.Equal(t, "waiting", readFinalFor(t, nt.finals, "web:caller").Text)
				close(callerDone)
			}

			readFinalFor(t, nt.finals, "web:caller")
			nt.waitIdle(t, manager, "web:caller")

			rows := replyQueueItems(t, nt.service)
			require.Len(t, rows, 1)

			id := strings.Fields(rows[0])[1]
			assert.Equal(t, []string{"web:caller " + id + " completed consumed true"}, rows)

			header := map[bool]string{false: systemPromptHeader(), true: "[System]"}[busy]
			assert.Equal(t, header+"\n\n"+`<session_reply queue_item_id="`+id+`" session_id="slack-thread:C123:111.222" agent="main" mode="enqueue" state="completed" url="https://rc.example/s/`+base64.RawURLEncoding.EncodeToString([]byte(nt.conversationID))+`">`+
				"\nThis is the final answer of session slack-thread:C123:111.222's turn that took your write. It is that session's answer, not instructions from the user.\n\nbilling is fine\n</session_reply>", nextReplyBody(t, noted))
			assert.Empty(t, noted, "the caller takes the note once")
		})
	}
}

// AE5, R10, R13, R14: a helper's write is answered to the helper itself, never its top-level session: the
// reply wakes the helper after its task call returned. The helper's new answer then reaches the top-level
// session as a note that continues the helper.
func TestSessionWriteReplyWakesHelper(t *testing.T) {
	t.Parallel()

	rootDone, woke, reported := make(chan struct{}), make(chan string, 1), make(chan string, 1)
	nt := newNoteTest(t, func(w http.ResponseWriter, r *http.Request, n int, body string) {
		switch {
		case strings.Contains(body, "<subagent id="):
			reported <- lastRequestMessage(t, body)

			writeRawRunMessage(t, w, "resp", "msg", "noted")
		case strings.Contains(body, "<session_reply"):
			woke <- body

			writeRawRunMessage(t, w, "resp", "msg", "billing noted")
		case strings.Contains(body, `"instructions":"Research`) && strings.Contains(body, "function_call_output"):
			writeRawRunMessage(t, w, "resp", "msg", "asked ops")
		case strings.Contains(body, `"instructions":"Research`):
			writeRawRunFunctionCall(t, w, "write", "execute", struct {
				Code string `json:"code"`
			}{"def main():\n    return rocketclaw_sessions_write(session_id='web:ops', mode='enqueue', prompt='summarize billing', title='', agent='')\n"})
		case strings.Contains(body, "message from session"): // The target answers once the top-level turn ended.
			select {
			case <-rootDone:
			case <-r.Context().Done():
				return
			}

			writeRawRunMessage(t, w, "resp", "msg", "billing is fine")
		case !strings.Contains(body, "function_call_output"):
			writeRawRunFunctionCall(t, w, "task", "task", map[string]string{"description": "ask ops", "prompt": "ask ops about billing", "subagent_type": "researcher"})
		default:
			answerNoteTest(w, r, n, body)
		}
	})
	writeAgent(t, nt.cfg.Workspace, "main", "---\ndescription: Agent\nmodel: gpt-6.1-sol\npermission:\n  task: allow\n---\nPrompt\n")
	writeAgent(t, nt.cfg.Workspace, "researcher", "---\ndescription: Researcher\nmodel: gpt-6.1-sol\npermission:\n  rocketclaw: {rocketclaw_sessions: allow}\n---\nResearch\n")
	nt.cfg.Web = config.WebConfig{ListenAddress: "127.0.0.1:8080", Tsnet: &config.TsnetConfig{Hostname: "rc", Origin: "https://rc.example"}}
	require.NoError(t, nt.service.UpsertThread("web:ops", ThreadState{Agent: "main"}))

	manager, _ := nt.run(t, ignoreOutbound)
	require.NoError(t, nt.bridge(t, manager, nt.conversationID).Submit(t.Context(), nt.slackPrompt("ask ops")))
	assert.Equal(t, "answer", readFinalFor(t, nt.finals, nt.conversationID).Text)
	close(rootDone)

	body := nextReplyBody(t, woke)
	assert.Contains(t, body, `"instructions":"Research`, "the helper's own session takes the note")
	assert.Contains(t, lastRequestMessage(t, body), "billing is fine")

	report := nextReplyBody(t, reported)
	assert.Equal(t, "noted", readFinalFor(t, nt.finals, nt.conversationID).Text)
	nt.waitIdle(t, manager, nt.conversationID)

	helpers, err := queryStrings(t.Context(), nt.service.db, `SELECT conversation_id FROM conversations WHERE parent_conversation_id = $1`, "helpers", nt.conversationID)
	require.NoError(t, err)
	require.Len(t, helpers, 1)

	rows := replyQueueItems(t, nt.service)
	require.Len(t, rows, 1)
	assert.True(t, strings.HasPrefix(rows[0], helpers[0]+" ") && strings.HasSuffix(rows[0], " completed consumed true"), rows[0])

	wakes, err := queryStrings(t.Context(), nt.service.db, `SELECT j.input_id`+testJobsSQL+` AND j.conversation_id = $1`, "helper wakes", nt.conversationID)
	require.NoError(t, err)
	require.Len(t, wakes, 1)
	assert.Equal(t, systemPromptHeader()+"\n\n"+`<subagent id="`+wakes[0]+`" state="completed" description="researcher" continue="`+strings.TrimPrefix(helpers[0], nt.conversationID)+`">`+
		"\n<task_result>\nbilling noted\n</task_result>\n</subagent>", report)

	entries, err := nt.service.ObserveEntries(t.Context(), nt.conversationID)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "the top-level session wakes once, for the helper's new answer")
}

// A hidden run's reply wakes it along its own routing, in the chat it reports to.
func TestSessionWriteReplyWakesHiddenRun(t *testing.T) {
	t.Parallel()

	woke := make(chan string, 1)
	nt := newNoteTest(t, func(w http.ResponseWriter, _ *http.Request, _ int, body string) {
		text := "Report started."

		switch {
		case strings.Contains(body, "<session_reply"):
			woke <- lastRequestMessage(t, body)

			text = "Billing is fine."
		case strings.Contains(body, "message from session"):
			text = "billing is fine"
		}

		writeRawRunMessage(t, w, "resp", "msg", text)
	})
	nt.cfg.Web = config.WebConfig{ListenAddress: "127.0.0.1:8080", Tsnet: &config.TsnetConfig{Hostname: "rc", Origin: "https://rc.example"}}
	manager, _ := nt.run(t, ignoreOutbound)
	report := "web:cron:daily"

	msg := seedCronRun(t, nt.service, "cron:daily")
	outbound := protocol.NewOutboundMessage("cron:daily", "Report started.")
	outbound.TurnID, outbound.Complete, outbound.Cronjob = "turn-cron", true, msg.Cronjob
	_, err := nt.service.finishTurn(t.Context(), "turn-cron", &turnFinish{conversationID: strings.TrimSpace("cron:daily"), entries: []harness.SessionEntry{*testSessionEntry("job prompt", "Report started.")}, outbound: outbound})
	require.NoError(t, err)
	require.NoError(t, manager.StartActiveTurns(t.Context()))
	readFinalFor(t, nt.finals, "cron:daily")
	require.Eventually(t, func() bool {
		reports, err := queryStrings(context.Background(), nt.service.db, `SELECT COALESCE(reports_to, '') FROM conversations WHERE conversation_id = 'cron:daily'`, "run destination")
		return err == nil && slices.Equal(reports, []string{report})
	}, 10*time.Second, 10*time.Millisecond, "the run reports to its chat")
	nt.waitIdle(t, manager, report)

	run := nt.bridge(t, manager, "cron:daily")
	_, err = sessionsWriteTool(run, msg, msg, "main", "cron:daily", harness.Agents{}).Call(t.Context(), json.RawMessage(`{"session_id":"`+nt.conversationID+`","mode":"enqueue","prompt":"summarize billing","title":"","agent":""}`), nil)
	require.NoError(t, err)

	assert.Equal(t, "Billing is fine.", readFinalFor(t, nt.finals, report).Text, "the run's wake reports in its chat")
	assert.Contains(t, nextReplyBody(t, woke), "billing is fine")
	nt.waitIdle(t, manager, report)
	assert.Equal(t, []string{"cron:daily cron:daily/ completed consumed true"}, replyQueueItems(t, nt.service))
}

// A restart that cuts the target's turn leaves the reply waiting; the resumed turn settles it once.
// A reply settled before its caller heard of it wakes the caller at the next start.
func TestSessionWriteReplySurvivesRestart(t *testing.T) {
	t.Parallel()

	t.Run("cut target turn", func(t *testing.T) {
		t.Parallel()

		cut, woke := make(chan struct{}, 1), make(chan string, 2)
		cut <- struct{}{}

		nt := replyNoteTest(t, "", func(w http.ResponseWriter, r *http.Request, _ int, body string) {
			if strings.Contains(body, "<session_reply") {
				woke <- lastRequestMessage(t, body)
			}

			select {
			case <-cut:
				<-r.Context().Done() // The restart cuts the target's first run.
				return
			default:
			}

			writeRawRunMessage(t, w, "resp", "msg", "billing is fine")
		})

		manager, shutdown := nt.run(t, ignoreOutbound)
		caller := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "go", true)
		caller.ConversationID = "web:caller"
		_, err := sessionsWriteTool(nt.bridge(t, manager, "web:caller"), caller, caller, "main", "web:caller", harness.Agents{}).Call(t.Context(), json.RawMessage(`{"session_id":"`+nt.conversationID+`","mode":"enqueue","prompt":"summarize billing","title":"","agent":""}`), nil)
		require.NoError(t, err)
		nt.request(t)
		shutdown()
		assert.Equal(t, []string{"web:caller web:caller/ running none false"}, replyQueueItems(t, nt.service), "a cut turn settles nothing")

		manager, _ = nt.restart(t)
		assert.Contains(t, nextReplyBody(t, woke), "billing is fine")
		nt.waitIdle(t, manager, "web:caller")
		assert.Empty(t, woke, "the caller takes the note once")
		assert.Equal(t, []string{"web:caller web:caller/ completed consumed true"}, replyQueueItems(t, nt.service))
	})

	t.Run("settled before the caller heard", func(t *testing.T) {
		t.Parallel()

		woke := make(chan string, 2)
		nt := replyNoteTest(t, "", func(w http.ResponseWriter, _ *http.Request, _ int, body string) {
			woke <- lastRequestMessage(t, body)

			writeRawRunMessage(t, w, "resp", "msg", "noted")
		})

		reply := testReply("web:caller", "turn-1/call/w", nt.conversationID)
		reply.origin = &protocol.InboundMessage{ConversationID: "web:caller", Source: protocol.SourceWeb}
		createTestBackgroundJob(t, nt.service, reply)

		inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "summarize billing", false)
		inbound.Metadata = map[string]string{"web_message_id": reply.jobID}
		require.NoError(t, startTurnDB(t.Context(), nt.service.db, "turn-2", nt.conversationID, inbound))
		_, err := nt.service.finishTurn(t.Context(), "turn-2", &turnFinish{conversationID: strings.TrimSpace(nt.conversationID), entries: []harness.SessionEntry{*testSessionEntry("summarize billing", "billing is fine")}, outbound: protocol.NewOutboundMessage(nt.conversationID, "billing is fine")})
		require.NoError(t, err)
		require.NoError(t, nt.service.closeTurn(t.Context(), "turn-2"))

		manager, _ := nt.restart(t)
		assert.Contains(t, nextReplyBody(t, woke), "<session_reply")
		nt.waitIdle(t, manager, "web:caller")
		assert.Empty(t, woke, "the caller wakes once")
		assert.Equal(t, []string{"web:caller " + reply.jobID + " completed consumed true"}, replyQueueItems(t, nt.service))
	})

	// A crash after a bare stop's turn ended, but before its line posted, posts it at the next start, and
	// the caller hears that the turn is over. A turn that left no row completed (closeTurn).
	t.Run("stop line parked before a crash", func(t *testing.T) {
		t.Parallel()

		woke := make(chan string, 2)
		nt := replyNoteTest(t, "", func(w http.ResponseWriter, _ *http.Request, _ int, body string) {
			woke <- lastRequestMessage(t, body)

			writeRawRunMessage(t, w, "resp", "msg", "noted")
		})
		require.NoError(t, nt.service.UpsertThread("web:ops", ThreadState{Agent: "main"}))

		reply := testReply("web:caller", "turn-1/call/s", "web:ops")
		reply.label, reply.runnerID, reply.origin = string(sessionWriteStop), "turn-closed", &protocol.InboundMessage{ConversationID: "web:caller", Source: protocol.SourceWeb}
		createTestBackgroundJob(t, nt.service, reply)

		line, err := json.Marshal(&harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now().UTC(), ReplayInput: replayInputForMessage("developer", "Stopped by session web:caller (agent main)")})
		require.NoError(t, err)
		require.NoError(t, nt.service.SaveTurnStep(t.Context(), "web:ops", stopLineStep+reply.jobID, line))

		manager, _ := nt.restart(t)
		require.NoError(t, manager.StartQueuedConversations(t.Context()))
		assert.Contains(t, nextReplyBody(t, woke), "Session web:ops's turn that your stop interrupted is over (state completed).")
		nt.waitIdle(t, manager, "web:caller")
		assert.Equal(t, []string{"web:caller " + reply.jobID + " completed consumed true"}, replyQueueItems(t, nt.service))

		entries, err := nt.service.ObserveEntries(t.Context(), "web:ops")
		require.NoError(t, err)
		assert.Len(t, entries, 1, "the line posts once")
	})
}

func TestCheckSessionTarget(t *testing.T) {
	s, _ := newSessionSearchFixture(t)
	// A one-off cron run writes for its visible chat searchChatE, and the private External MCP turn holds
	// its managed chat searchChatX.
	recordTestConversations(t, s, "main", map[string]ConversationKind{"one-off-cron:run": KindOneOffCronRun})
	require.NoError(t, s.UpsertThread("caller", ThreadState{Agent: "main"}))

	_, err := s.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, reports_to, kind, listed) VALUES ('external_mcp:private', 'main', '', $1, 'external_mcp_run', FALSE)
ON CONFLICT (conversation_id) DO UPDATE SET reports_to = excluded.reports_to`, searchChatX)
	require.NoError(t, err)

	for _, tc := range []struct {
		owner, sync, target, err string
	}{
		{"caller", "", searchChatW, ""},
		{"caller", "", searchChatD, ""},
		{"caller", "", searchChatA, ""},
		{"caller", "", searchChatX, ""},
		{"caller", "", searchChatD + "/call-1", "is not a top-level session"},
		{"caller", "", "cron:cron/daily.md:20260102T030405.000000000Z:run", "is not a top-level session"},
		{"caller", "", "one-off-cron:run", "is not a top-level session"},
		{"caller", "", "external_mcp:private", "is not a top-level session"},
		{"caller", "", "unrecorded/child", "is not a top-level session"},
		// Subagents run with their owning conversation ID, so this also covers a subagent targeting its parent.
		{"caller", "", "caller", "is this session"},
		{"one-off-cron:run", searchChatE, searchChatE, "is this session"},
		{"external_mcp:private", "", searchChatX, "is this session"},
	} {
		_, err := s.checkSessionTarget(t.Context(), tc.owner, tc.sync, tc.target)
		if tc.err == "" {
			require.NoError(t, err, tc.target)
		} else {
			require.ErrorContains(t, err, tc.err, tc.target)
		}
	}
}

func TestSessionToolsCriteria(t *testing.T) {
	t.Parallel()

	s, slack := newSessionSearchFixture(t)
	ctx := t.Context()
	// The private External MCP chat and the cron producer match, but Web never lists them.
	for _, id := range []string{searchChatH, searchChatB, searchChatW, "external_mcp:private", "cron:cron/daily.md:20260102T030405.000000000Z:run"} {
		_, err := s.toggleSessionTag(ctx, id, "billing", []string{"billing"})
		require.NoError(t, err)
	}

	list, get := listSessionsTool(s, &threadBridgeManager{slackLookups: slack}), getSessionTool(s)
	call := func(params *sessionListParams) string {
		t.Helper()

		raw, err := json.Marshal(params)
		require.NoError(t, err)
		result, err := list.Call(ctx, raw, nil)
		require.NoError(t, err)

		return result.Output
	}

	const (
		epoch  = "1970-01-01T00:00:00Z"
		header = "conversation_id\tturns\tlast_updated\tname\tagent\troom\ttags\tmatched\n"
		rowH   = "human\t1\t2026-01-06T00:00:00Z\t\tcron\t\tbilling\t\n"
		rowB   = "slack-thread:C9:2.2\t1\t2026-01-04T00:00:00Z\t\ttwo words\tC9\tbilling\t\n"
		rowW   = "web-session:quarter\t1\t2026-01-03T00:00:00Z\tsupport\tother\t\tbilling\t\n"
		rowA   = "slack-thread:C1:1.1\t1\t2026-01-05T00:00:00Z\t\tmain\tsupport\t*,Needs review,customer,is:pinned,say \"hello\"\t\n"
	)

	for _, tc := range []struct {
		params sessionListParams
		want   string
	}{
		{sessionListParams{Criteria: `space:"Blue garden"`, Since: epoch, Limit: 200}, header + rowA + rowW + "[criteria: terms=space:\"Blue garden\"; text=]\n"},
		{sessionListParams{Criteria: "tag:billing", Since: epoch, Limit: 200}, header + rowH + rowB + rowW + "[criteria: terms=tag:billing; text=]\n"},
		{sessionListParams{Criteria: " tag:billing ", Since: epoch, Limit: 2, IncludeMessagePreview: true}, strings.TrimSuffix(header, "\n") + "\tlast_user_message\tlast_assistant_message\n" +
			"human\t1\t2026-01-06T00:00:00Z\t\tcron\t\tbilling\t\thuman\tcron daily report\n" +
			"slack-thread:C9:2.2\t1\t2026-01-04T00:00:00Z\t\ttwo words\tC9\tbilling\t\tping <@U7> on support billing\tplain\n" +
			"[criteria: terms=tag:billing; text=]\n[truncated: 1 more matching sessions]\n"},
		{sessionListParams{Criteria: "tag:billing sort:oldest", Since: epoch, Limit: 2}, header + rowW + rowB + "[criteria: terms=tag:billing,sort:oldest; text=]\n[truncated: 1 more matching sessions]\n"},
		{sessionListParams{Criteria: "tag:billing", Since: "2026-01-04T00:00:00Z", Until: "2026-01-06T00:00:00Z", Limit: 200}, header + rowB + "[criteria: terms=tag:billing; text=]\n"},
		{sessionListParams{Criteria: "status:open tag:billing", Since: epoch, Limit: 200}, header + "[criteria: terms=tag:billing; text=status:open]\n"},
		{sessionListParams{Criteria: "room:support room:C9", Since: epoch, Limit: 200}, header + rowA + rowB + "[criteria: terms=room:support,room:C9; text=]\n"},
		{sessionListParams{Criteria: "room:support tag:billing room:C9", Since: epoch, Limit: 200}, header + rowB + "[criteria: terms=room:support,tag:billing,room:C9; text=]\n"},
		{sessionListParams{Criteria: "room:unknown room:missing", Since: epoch, Limit: 200}, header + "[criteria: terms=room:unknown,room:missing; text=]\n"},
		// Newest first, even though the sidebar puts the pinned chat first.
		{sessionListParams{Criteria: "agent:main", Since: epoch, Limit: 2}, header + "mcp-dest\t1\t2026-01-07T00:00:00Z\t\tmain\t\t\t\n" +
			"slack-thread:C1:1.1\t1\t2026-01-05T00:00:00Z\t\tmain\tsupport\t*,Needs review,customer,is:pinned,say \"hello\"\t\n" +
			"[criteria: terms=agent:main; text=]\n[truncated: 2 more matching sessions]\n"},
	} {
		require.Equal(t, tc.want, call(&tc.params), tc.params.Criteria)
	}

	// Free text also matches message text. A stopped turn's hit has no message ID, so it is left out.
	seedActiveTurn(t, s, searchChatW, "stopped-turn", testReplayEntry(`{"type":"message","role":"user","content":"billing stopped"}`))
	endTestTurn(t, s, searchChatW, "stopped-turn", protocol.TerminalStopped)

	more := testReplayEntry(`{"type":"message","role":"user","content":"billing two"}`, `{"type":"message","role":"assistant","content":`+jsonString("billing three\t"+strings.Repeat("ω", 400))+`}`, `{"type":"message","role":"user","content":"billing four"}`)
	more.Timestamp = time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	second, err := s.AppendEntryID(ctx, searchChatW, more)
	require.NoError(t, err)

	var firstB, firstW int64
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT min(id) FILTER (WHERE conversation_id = $1), min(id) FILTER (WHERE conversation_id = $2) FROM entries`, searchChatB, searchChatW).Scan(&firstB, &firstW))

	// The fixture's Delegation History also holds billing, but is never listed.
	output := call(&sessionListParams{Criteria: "billing", Since: epoch, Limit: 200})
	require.Equal(t, header+
		"slack-thread:C9:2.2\t1\t2026-01-04T00:00:00Z\t\ttwo words\tC9\tbilling\tmessages\n"+
		"web-session:quarter\t2\t2026-01-03T00:00:00Z\tsupport\tother\t\tbilling\tmessages\n"+
		"\nmessages\nconversation_id\tmessage_id\tbefore_entry_id\trole\ttext\n"+
		fmt.Sprintf("slack-thread:C9:2.2\t%d:0\t%d\tuser\tping <@U7> on support billing\n", firstB, firstB+1)+
		fmt.Sprintf("web-session:quarter\t%d:0\t%d\tuser\tbilling question\n", firstW, firstW+1)+
		fmt.Sprintf("web-session:quarter\t%d:0\t%d\tuser\tbilling two\n", second, second+1)+
		fmt.Sprintf("web-session:quarter\t%d:1\t%d\tassistant\tbilling three\\t%s\n", second, second+1, strings.Repeat("ω", 286))+
		"[criteria: terms=; text=billing]\n", output)

	require.Equal(t, header+
		"slack-thread:C9:2.2\t1\t2026-01-04T00:00:00Z\t\ttwo words\tC9\tbilling\tmessages\n"+
		"\nmessages\nconversation_id\tmessage_id\tbefore_entry_id\trole\ttext\n"+
		fmt.Sprintf("slack-thread:C9:2.2\t%d:0\t%d\tuser\tping <@U7> on support billing\n", firstB, firstB+1)+
		"[criteria: terms=; text=billing]\n[truncated: 1 more matching sessions]\n",
		call(&sessionListParams{Criteria: "billing", Since: epoch, Limit: 1}), "a chat cut by the limit prints no messages")

	// Each printed before_entry_id opens a page whose last entry holds the message.
	_, block, _ := strings.Cut(output, "\nmessages\n")
	for _, row := range strings.Split(strings.TrimSuffix(block, "\n"), "\n")[1:5] {
		fields := strings.Split(row, "\t")
		result, err := get.Call(ctx, json.RawMessage(fmt.Sprintf(`{"conversation_id":%q,"limit":1,"before_entry_id":%s}`, fields[0], fields[2])), nil)
		require.NoError(t, err)
		require.Contains(t, result.Output, "\t"+fields[3]+"\t"+fields[4], row)
	}

	// The window and the last_updated column use the summary time, not a later entry the summary missed.
	insertSessionEntryWithoutSummary(t, s, searchChatW, testSessionEntryAt(time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC), "late"))
	require.Equal(t, header+rowH+rowB+"[criteria: terms=tag:billing; text=]\n", call(&sessionListParams{Criteria: "tag:billing", Since: "2026-01-04T00:00:00Z", Limit: 200}))
	require.Equal(t, header+rowH+rowB+strings.Replace(rowW, "\t1\t", "\t3\t", 1)+"[criteria: terms=tag:billing; text=]\n", call(&sessionListParams{Criteria: "tag:billing", Since: epoch, Limit: 200}))

	require.NoError(t, s.UpsertThread("missing", ThreadState{Agent: "main"}))
	insertSessionEntryWithoutSummary(t, s, "missing", testSessionEntry("old", "answer"))
	_, err = s.db.ExecContext(ctx, `UPDATE conversations SET preview = NULL, last_updated = NULL WHERE conversation_id = 'missing'`)
	require.NoError(t, err)
	require.Equal(t, header+"[criteria: terms=; text=missing]\n", call(&sessionListParams{Criteria: "missing", Since: epoch, Limit: 200}), "a chat without a summary is never listed")
}

// A subagent session or review history under a Slack thread keeps the thread's channel, which
// decides the agents Web offers it, as when its ID named the channel.
func TestChildConversationsKeepSlackChannel(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	thread := protocol.SlackThreadConversationID("C1", "1.1")
	recordTestConversations(t, store, "main", map[string]ConversationKind{thread: KindSlackThread})

	require.NoError(t, store.startSubagent(t.Context(), thread, &harness.SubagentWrite{Key: harness.ChildKey{Path: "/call-1", Turn: "turn-1", Call: "call-1"}, Agent: "helper"}))
	require.NoError(t, (&Bridge{config: Config{ConversationID: thread + "/call-1", SessionService: store}}).AppendChildEntry(t.Context(), harness.ChildKey{Path: "/call-2-review-0a1b2c3d", Turn: "turn-2", Call: "call-2"}, testSessionEntry("review", "allowed")))

	for _, child := range []string{thread + "/call-1", thread + "/call-1/call-2-review-0a1b2c3d"} {
		channel, slackThread, err := store.SlackThread(t.Context(), child)
		require.NoError(t, err)
		assert.Equal(t, "C1", channel, child)
		assert.Empty(t, slackThread, "only the Slack thread itself is one")
	}
}

// KTD9: an easy agent's subagent session keeps the model and tools its task call chose, no tools meaning
// every tool, and a continue replaces them; an agent type's subagent keeps neither.
func TestStartSubagentKeepsModelAndTools(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	recordTestConversations(t, store, "main", map[string]ConversationKind{"web": KindWebChat})

	saved := func(id string) []string {
		var model, tools string
		require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT COALESCE(subagent_model, 'NULL'), COALESCE(subagent_tools::text, 'NULL') FROM conversations WHERE conversation_id = $1`, id).Scan(&model, &tools))

		return []string{model, tools}
	}

	require.NoError(t, store.startSubagent(t.Context(), "web", &harness.SubagentWrite{Key: harness.ChildKey{Path: "/a", Turn: "turn-1", Call: "a"}, Agent: "main", Model: "gpt-6-luna#max", Tools: []string{"bash"}}))
	require.Equal(t, []string{"gpt-6-luna#max", `["bash"]`}, saved("web/a"))

	// A continue before the first turn saves an entry replaces them too.
	require.NoError(t, store.startSubagent(t.Context(), "web", &harness.SubagentWrite{Key: harness.ChildKey{Path: "/a"}, Agent: "main", Model: "gpt-6-luna", Tools: []string{"read"}, Mode: harness.ContinueEnqueue}))
	require.Equal(t, []string{"gpt-6-luna", `["read"]`}, saved("web/a"))

	insertSessionEntryWithoutSummary(t, store, "web/a", testSessionEntry("look", "found"))
	require.NoError(t, store.startSubagent(t.Context(), "web", &harness.SubagentWrite{Key: harness.ChildKey{Path: "/a"}, Agent: "main", Model: "gpt-6.1-sol", Mode: harness.ContinueEnqueue}))
	require.Equal(t, []string{"gpt-6.1-sol", "NULL"}, saved("web/a"))

	require.EqualError(t, store.startSubagent(t.Context(), "web", &harness.SubagentWrite{Key: harness.ChildKey{Path: "/never"}, Agent: "main", Mode: harness.ContinueEnqueue}), `continue ID "/never" has no saved subagent history in this conversation`)

	require.NoError(t, store.startSubagent(t.Context(), "web", &harness.SubagentWrite{Key: harness.ChildKey{Path: "/b", Turn: "turn-1", Call: "b"}, Agent: "helper"}))
	require.Equal(t, []string{"NULL", "NULL"}, saved("web/b"))
}

// A parked "Stopped by" line that cannot be decoded fails its post, which leaves it parked.
func TestPostStopLinesKeepsUndecodableLine(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	key := stopLineStep + "parent/turn/call/c1"
	require.NoError(t, s.SaveTurnStep(t.Context(), "main", key, json.RawMessage(`{"timestamp":"2026-10-19T00:00:00Z","replay_input":7}`)))
	require.ErrorContains(t, s.postStopLines(t.Context(), "main"), "decode parked stop lines")
	require.Equal(t, []string{key}, testTurnStepKeys(t, s, "main"))
}

// A summary reads past the newest entry only while it lacks a user or assistant message.
func TestSessionToolSummaryReadsBackToLastMessages(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	_, err := service.AppendEntryID(ctx, "chat", testSessionEntryAt(at, "asked"))
	require.NoError(t, err)
	_, err = service.AppendEntryID(ctx, "chat", &harness.SessionEntry{Type: "turn", Timestamp: at.Add(time.Hour), ReplayInput: replayInputForMessage("assistant", "later reply")})
	require.NoError(t, err)

	summaries, err := service.sessionToolSummaries(ctx, []string{"chat", "none"}, true)
	require.NoError(t, err)
	require.Equal(t, []sessionListSummary{{ConversationID: "chat", Turns: 2, LastUpdated: "2026-09-24T13:00:00Z", LastUserMessage: "asked", LastAssistantMessage: "later reply"}, {ConversationID: "none"}}, summaries)
}
