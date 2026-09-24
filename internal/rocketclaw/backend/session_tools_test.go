package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionToolsStoredContract(t *testing.T) {
	service := newTestSessionService(t)
	list := listSessionsTool(service)
	get := getSessionTool(service)
	result, err := list.Call(t.Context(), json.RawMessage(`{"since":"","until":"","limit":0,"include_message_preview":true}`), nil)
	require.NoError(t, err)
	require.Equal(t, "conversation_id\tturns\tlast_updated\tlast_user_message\tlast_assistant_message\n", result.Output)

	base := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
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
		_, err := service.AppendEntryID(t.Context(), fixture.id, testSessionEntryAt(fixture.at, fixture.user))
		require.NoError(t, err)
	}

	for _, tc := range []struct{ input, want string }{
		{`{"since":"","until":"","limit":1,"include_message_preview":true}`, "conversation_id\tturns\tlast_updated\tlast_user_message\tlast_assistant_message\nexec:last\t1\t2026-09-24T13:00:00Z\texec\tassistant\n"},
		{`{"since":"2026-09-24T13:00:00Z","until":"","limit":0,"include_message_preview":false}`, "conversation_id\tturns\tlast_updated\nexec:last\t1\t2026-09-24T13:00:00Z\nexternal_mcp:private\t2\t2026-09-24T11:00:00Z\n"},
		{`{"since":"2026-09-24T12:00:00Z","until":"2026-09-24T13:00:00Z","limit":0,"include_message_preview":false}`, "conversation_id\tturns\tlast_updated\ncron:first\t1\t2026-09-24T12:00:00Z\n"},
		{`{"since":"2026-09-24T14:00:00Z","until":"2026-09-24T12:00:00Z","limit":0,"include_message_preview":true}`, "conversation_id\tturns\tlast_updated\tlast_user_message\tlast_assistant_message\n"},
	} {
		result, err := list.Call(t.Context(), json.RawMessage(tc.input), nil)
		require.NoError(t, err)
		require.Equal(t, tc.want, result.Output)
	}

	result, err = get.Call(t.Context(), json.RawMessage(`{"conversation_id":" external_mcp:private "}`), nil)
	require.NoError(t, err)

	require.Equal(t, fmt.Sprintf("timestamp\trole\tcontent\n2026-09-24T13:00:00Z\tuser\tfirst\n2026-09-24T13:00:00Z\tassistant\t%s\n2026-09-24T11:00:00Z\tuser\t  latest\\n user\\tΩ\\r\\\\  \n2026-09-24T11:00:00Z\tassistant\t%s\n", "assistant", "assistant"), result.Output)
	result, err = get.Call(t.Context(), json.RawMessage(`{"conversation_id":"unknown"}`), nil)
	require.NoError(t, err)
	require.Equal(t, "timestamp\trole\tcontent\n", result.Output)

	for _, input := range []string{`{"since":"","until":"","limit":0,"include_message_preview":true}`, `{"since":" ","until":" ","limit":0,"include_message_preview":true}`} {
		result, err := list.Call(t.Context(), json.RawMessage(input), nil)
		require.NoError(t, err)
		require.Equal(t, "conversation_id\tturns\tlast_updated\tlast_user_message\tlast_assistant_message\ncron:first\t1\t2026-09-24T12:00:00Z\told\tassistant\nexec:last\t1\t2026-09-24T13:00:00Z\texec\tassistant\nexternal_mcp:private\t2\t2026-09-24T11:00:00Z\t  latest\\n user\\tΩ\\r\\\\  \tassistant\n", result.Output)
	}
}

func TestSessionToolsTimePrecisionAndDurations(t *testing.T) {
	service := newTestSessionService(t)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, offset := range []time.Duration{0, 1, 2, time.Second} {
		_, err := service.AppendEntryID(t.Context(), strconv.Itoa(i), testSessionEntryAt(base.Add(offset), "user"))
		require.NoError(t, err)
	}

	list := listSessionsTool(service)
	for _, tc := range []struct {
		input string
		ids   []string
	}{
		{`{"since":" 2025-12-31T19:00:00.000000001-05:00 ","until":"2026-01-01T00:00:00.000000002Z","limit":0,"include_message_preview":true}`, []string{"1"}},
		{`{"since":"","until":"2026-01-01T00:00:00.000000001Z","limit":0,"include_message_preview":true}`, []string{"0"}},
		{`{"since":"","until":"","limit":4,"include_message_preview":true}`, []string{"3", "2", "1", "0"}},
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
		result, err := list.Call(t.Context(), json.RawMessage(fmt.Sprintf(`{"since":%q,"until":"","limit":0,"include_message_preview":true}`, tc.since)), nil)
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
	service := newTestSessionService(t)
	require.NoError(t, service.UpsertThread("web:empty", ThreadState{Agent: "main"}))
	require.NoError(t, service.UpsertThread("web:stored", ThreadState{Agent: "main"}))
	require.NoError(t, service.BeginGoal("web:stored", "work", "", 3, "", ""))

	entry := &rocketcode.SessionEntry{Version: 1, Type: "replay", Timestamp: time.Now().UTC(), Model: "model", ResponseID: "response",
		TokenUsage: &rocketcode.TokenUsage{CompletionReasoningTokens: 7},
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
		},
	}
	for _, id := range []string{"external_mcp:private", "cron:one", "exec:one", "slack-thread:C1:1", "web:stored", "unrecorded"} {
		_, err := service.AppendEntryID(t.Context(), id, entry)
		require.NoError(t, err)
	}

	snapshot := func() []string {
		values := make([]string, 0, 8)

		for _, table := range []string{"session_entries", "session_summaries", "managed_conversations", "external_mcp_sessions", "active_turns", "conversation_goals", "thread_queue", "scheduled_messages"} {
			var value string

			err := service.db.QueryRowContext(t.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)::text, '[]') FROM `+table+` t`).Scan(&value)
			require.NoError(t, err)

			values = append(values, value)
		}

		return values
	}
	before := snapshot()
	list, get := listSessionsTool(service), getSessionTool(service)
	result, err := list.Call(t.Context(), json.RawMessage(`{"since":"","until":"","limit":0,"include_message_preview":true}`), nil)
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
			ID string `json:"conversation_id"`
		}{fields[0]})
		require.NoError(t, err)
		result, err := get.Call(t.Context(), raw, nil)
		require.NoError(t, err)

		at := entry.Timestamp.Format(time.RFC3339Nano)
		require.Equal(t, "timestamp\trole\tcontent\n"+at+"\ttool_call\tRead [call] {}\n"+at+"\ttool_result\t[call] line\\nΩ\\t\\\\\\r\n"+at+"\ttool_result\t[parts] text\\n[non-text tool result omitted]\n"+at+"\tuser\t\n"+at+"\treasoning\tthinking\\nplaintext\n"+at+"\tevent\t[web_search_call: stored event not rendered]\n"+at+"\tevent\t[unknown_provider_item: stored event not rendered]\n", result.Output)
	}

	require.Equal(t, before, snapshot())
}

func TestSessionToolsErrors(t *testing.T) {
	service := newTestSessionService(t)

	list, get := listSessionsTool(service), getSessionTool(service)
	for _, input := range []string{`{`, `[]`, `{"since":"","until":"","limit":-1,"include_message_preview":true}`, `{"since":"","until":"","limit":"1","include_message_preview":true}`, `{"since":"","until":"","limit":1.5,"include_message_preview":true}`, `{"since":true,"until":"","limit":0,"include_message_preview":true}`, `{"since":"","until":1,"limit":0,"include_message_preview":true}`, `{"since":"","until":"","limit":0,"include_message_preview":"false"}`, `{"since":"yesterday","until":"","limit":0,"include_message_preview":true}`, `{"since":"","until":"1h","limit":0,"include_message_preview":true}`} {
		_, err := list.Call(t.Context(), json.RawMessage(input), nil)
		require.Error(t, err, input)
	}

	for _, input := range []string{`{`, `{}`, `{"conversation_id":" "}`, `{"conversation_id":12}`} {
		_, err := get.Call(t.Context(), json.RawMessage(input), nil)
		require.Error(t, err, input)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := list.Call(ctx, json.RawMessage(`{"since":"","until":"","limit":0,"include_message_preview":true}`), nil)
	require.ErrorIs(t, err, context.Canceled)
	_, err = get.Call(ctx, json.RawMessage(`{"conversation_id":"broken"}`), nil)
	require.ErrorIs(t, err, context.Canceled)
	_, err = service.db.ExecContext(t.Context(), `INSERT INTO session_entries (conversation_id,entry_json,entry_timestamp) VALUES ('broken','{"timestamp":false}','2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	_, err = list.Call(t.Context(), json.RawMessage(`{"since":"","until":"","limit":0,"include_message_preview":true}`), nil)
	require.Error(t, err)
	_, err = get.Call(t.Context(), json.RawMessage(`{"conversation_id":"broken"}`), nil)
	require.Error(t, err)
	// Bodies outside the selected conversation limit must not be decoded.
	_, err = service.AppendEntryID(t.Context(), "valid", testSessionEntryAt(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "ok"))
	require.NoError(t, err)
	_, err = list.Call(t.Context(), json.RawMessage(`{"since":"","until":"","limit":1,"include_message_preview":true}`), nil)
	require.NoError(t, err)
	_, err = service.db.ExecContext(t.Context(), `UPDATE session_entries SET entry_json = '{"replay_input":[false]}' WHERE conversation_id = 'broken'`)
	require.NoError(t, err)
	_, err = list.Call(t.Context(), json.RawMessage(`{"since":"","until":"","limit":0,"include_message_preview":true}`), nil)
	require.Error(t, err)
}

func TestSessionToolsBridgePermissions(t *testing.T) {
	for _, tc := range []struct {
		name, rules        string
		list, get, current bool
		auto               bool
	}{
		{"exact", "rocketclaw_list_sessions: allow\n    rocketclaw_get_session: deny\n    rocketclaw_current_session_id: allow", true, false, true, false},
		{"absent", "{}", false, false, false, false},
		{"wildcard", "'rocketclaw_*': allow", true, true, true, false},
		{"wildcard deny", "'*': allow\n    rocketclaw_get_session: deny\n    rocketclaw_current_session_id: deny", true, false, false, false},
		{"auto", "rocketclaw_list_sessions: auto\n    rocketclaw_get_session: deny\n    rocketclaw_current_session_id: auto", true, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeMainAgentSkills(t, workspace, "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  read: allow\n  rocketclaw:\n    "+tc.rules+"\n---\nPrompt\n")
			service := newTestSessionServiceAt(t, workspace)
			history := newSessionStore("external_mcp:private", service)

			entries := []rocketcode.SessionEntry{
				*testSessionEntry("secret user", "secret answer"),
				{Version: 1, Type: "compaction", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"compaction","id":"cmp_1","encrypted_content":"sealed"}`)}},
				*testSessionEntry("after compaction", "recent answer"),
			}
			for _, entry := range entries {
				_, err := history.outID(entry)
				require.NoError(t, err)
			}

			unlock, err := service.lockTurnPair(t.Context(), "external_mcp:private", "external_mcp:private")
			require.NoError(t, err)

			defer unlock()

			requests, approvals := 0, 0

			var outputs []string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if !assert.NoError(t, err) {
					return
				}

				var body struct {
					Model string `json:"model"`
					Tools []struct {
						Name       string `json:"name"`
						Strict     bool   `json:"strict"`
						Parameters struct {
							Required             []string        `json:"required"`
							Type                 string          `json:"type"`
							Properties           json.RawMessage `json:"properties"`
							AdditionalProperties json.RawMessage `json:"additionalProperties"`
						} `json:"parameters"`
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
					approvals++
					writeRawRunMessage(t, w, fmt.Sprintf("approval-%d", approvals), "approval", `{"risk_level":"low","user_authorization":"unknown","outcome":"allow","rationale":"Read-only inspection."}`)

					return
				}

				requests++
				if requests == 1 {
					assert.NotContains(t, string(raw), "secret user")
					assert.Contains(t, string(raw), `"type":"compaction"`)
					assert.Contains(t, string(raw), "after compaction")
				}

				for _, tool := range body.Tools {
					switch tool.Name {
					case listSessionsToolName:
						assert.True(t, tool.Strict)
						assert.Equal(t, []string{"include_message_preview", "limit", "since", "until"}, tool.Parameters.Required)
					case getSessionToolName:
						assert.True(t, tool.Strict)
						assert.Equal(t, []string{"conversation_id"}, tool.Parameters.Required)
					case "rocketclaw_current_session_id":
						assert.True(t, tool.Strict)
						assert.Equal(t, []string{}, tool.Parameters.Required)
						assert.Equal(t, "object", tool.Parameters.Type)
						assert.JSONEq(t, `{}`, string(tool.Parameters.Properties))
						assert.JSONEq(t, `false`, string(tool.Parameters.AdditionalProperties))
					}
				}

				outputs = nil

				for _, input := range body.Input {
					if input.Type == "function_call_output" {
						outputs = append(outputs, input.Output)
					}
				}

				switch requests {
				case 1:
					writeRawRunFunctionCall(t, w, "list", "execute", json.RawMessage(`{"code":"def main():\n    return rocketclaw_list_sessions(since=\"\", until=\"\", limit=0, include_message_preview=True)\n"}`))
				case 2:
					writeRawRunFunctionCall(t, w, "get", "execute", json.RawMessage(`{"code":"def main():\n    return rocketclaw_get_session(conversation_id=\"external_mcp:private\")\n"}`))
				case 3:
					writeRawRunFunctionCall(t, w, "direct-list", listSessionsToolName, json.RawMessage(`{"since":"","until":"","limit":0,"include_message_preview":true}`))
				case 4:
					writeRawRunFunctionCall(t, w, "direct-get", getSessionToolName, json.RawMessage(`{"conversation_id":"external_mcp:private"}`))
				case 5:
					writeRawRunFunctionCall(t, w, "current", "execute", json.RawMessage(`{"code":"def main():\n    return rocketclaw_current_session_id()\n"}`))
				case 6:
					writeRawRunFunctionCall(t, w, "direct-current", "rocketclaw_current_session_id", json.RawMessage(`{}`))
				case 7:
					if tc.current && tc.get {
						arguments, err := json.Marshal(struct {
							ConversationID string `json:"conversation_id"`
						}{outputs[5]})
						if !assert.NoError(t, err) {
							return
						}

						writeRawRunFunctionCall(t, w, "current-history", getSessionToolName, json.RawMessage(arguments))
					} else {
						writeRawRunMessage(t, w, "done", "message", "done")
					}
				default:
					writeRawRunMessage(t, w, "done", "message", "done")
				}
			}))
			t.Cleanup(server.Close)
			cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}, AutoApproverModel: "review-model"}
			root, agents, skills, resolver, err := prepareRocketCode(cfg, "main", slog.New(slog.DiscardHandler), toolModePersistent)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			require.NoError(t, root.MkdirAll("shell", 0o700))

			bridge := &Bridge{runtime: cfg, config: Config{ConversationID: "external_mcp:private", ManagedConversationID: "managed:other", ExternalConversationID: "public-id", SessionService: service, RequestReload: testNoopRestart}, log: slog.New(slog.DiscardHandler)}
			runtimeConfig := bridge.rocketcodeConfig(filepath.Join(workspace, "shell"), nil, nil)
			runtimeConfig.CheckpointSink = rocketcode.InertCheckpointSink{}
			runtimeConfig.ChildRunLogger = rocketcode.DiscardChildRunLog
			runtime, err := rocketcode.NewWithModelResolver(resolver, &runtimeConfig, root, agents, skills, "main", io.Discard)
			require.NoError(t, err)

			_, hasList := runtime.CodeModeHosts[listSessionsToolName]
			_, hasGet := runtime.CodeModeHosts[getSessionToolName]
			current, hasCurrent := runtime.CodeModeHosts["rocketclaw_current_session_id"]

			require.Equal(t, tc.list, hasList)
			require.Equal(t, tc.get, hasGet)
			require.Equal(t, tc.current, hasCurrent)

			if tc.current {
				schema, err := json.Marshal(current.Definition.Parameters)
				require.NoError(t, err)
				require.JSONEq(t, `{"type":"object","properties":{},"required":[],"additionalProperties":false}`, string(schema))
				require.True(t, current.Definition.Strict.Value)
			}

			input := make(chan rocketcode.PromptInput, 1)
			input <- rocketcode.PromptInput{Text: "inspect", Responses: make(chan rocketcode.ChatResponse, 100)}

			close(input)

			store := &memoryStore{}
			require.NoError(t, runtime.Loop(t.Context(), input, history.in(), store.out, make(chan os.Signal)))

			if tc.current && tc.get {
				require.Equal(t, 8, requests)
				require.Len(t, outputs, 7)
			} else {
				require.Equal(t, 7, requests)
				require.Len(t, outputs, 6)
			}

			if tc.list {
				require.Contains(t, outputs[0], "external_mcp:private")
				require.Equal(t, outputs[0], outputs[2])
			} else {
				require.Contains(t, outputs[0], "undefined: rocketclaw_list_sessions")
				require.Contains(t, outputs[2], "tool not found")
			}

			if tc.get {
				require.Equal(t, outputs[1], outputs[3])
				require.Equal(t, outputs[1], outputs[6])
				require.Equal(t, "timestamp\trole\tcontent\n1970-01-01T00:00:01Z\tuser\tsecret user\n1970-01-01T00:00:01Z\tassistant\tsecret answer\n0001-01-01T00:00:00Z\tcompaction\tCompaction boundary\n1970-01-01T00:00:01Z\tuser\tafter compaction\n1970-01-01T00:00:01Z\tassistant\trecent answer\n", outputs[1])
			} else {
				require.Contains(t, outputs[1], "undefined: rocketclaw_get_session")
				require.Contains(t, outputs[3], "tool not found")
			}

			if tc.current {
				require.Equal(t, "external_mcp:private", outputs[4])
				require.Equal(t, outputs[4], outputs[5])
			} else {
				require.Contains(t, outputs[4], "undefined: rocketclaw_current_session_id")
				require.Contains(t, outputs[5], "tool not found")
			}

			require.Equal(t, tc.auto, approvals > 0)
			require.True(t, service.PairBusyFor("external_mcp:private"))
		})
	}
}
