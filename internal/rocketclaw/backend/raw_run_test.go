package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRawRunDecisionToolStoresPayload(t *testing.T) {
	decision := new(rawRunDecision)
	_, ok := decision.Decision(nil)
	assert.False(t, ok)

	recorded := []rocketcode.SessionEntry{{ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"function_call","name":"` + rawRunToolName + `","arguments":"{\"payload\":\"first\"}"}`),
		json.RawMessage(`{"type":"function_call","name":"other","arguments":"{\"payload\":\"ignored\"}"}`),
		json.RawMessage(`{"type":"function_call","name":"` + rawRunToolName + `","arguments":"{\"payload\":\"recorded\"}"}`),
	}}}
	payload, ok := decision.Decision(recorded)
	require.True(t, ok)
	assert.Equal(t, "recorded", payload)

	tool := decision.Tool()
	_, err := tool.Call(context.Background(), json.RawMessage("{"), make(chan rocketcode.ChatResponse))
	require.ErrorContains(t, err, "parse raw run decision")

	result, err := tool.Call(context.Background(), json.RawMessage(`{"payload":"ship it"}`), make(chan rocketcode.ChatResponse))
	require.NoError(t, err)
	assert.Equal(t, "queued for verbatim delivery", result.Output)

	payload, ok = decision.Decision(recorded)
	require.True(t, ok)
	assert.Equal(t, "ship it", payload)
}

func TestWorkflowAgentRunnerUsesPreparedIsolatedRuntime(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: '{{ model \"active\" }}'\npermission:\n  read: {\"*\": allow}\n  edit: allow\n  glob: allow\n  grep: allow\n  bash: {\"*\": allow}\n  webfetch: {\"*\": allow}\n  websearch: allow\n  skill: {\"demo\": allow}\n  task: {\"*\": allow}\n  rocketclaw: {\"rocketclaw_reload\": allow}\n---\nMain prompt\n")
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(".rocketclaw/skills/demo", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/skills/demo/SKILL.md", []byte("---\nname: demo\ndescription: Demo\n---\nDemo\n"), 0o644))

	var (
		mu       sync.Mutex
		requests []map[string]any
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		mu.Lock()

		requests = append(requests, body)
		mu.Unlock()

		prompt := rawRunRequestPrompt(t, body)

		text := "first"

		switch prompt {
		case "structured":
			text = `{"ok":true}`
		case "second":
			text = "second"
		}

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", text)
	}))
	t.Cleanup(server.Close)

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousProvider := otel.GetTracerProvider()

	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	cfg := &config.Config{Workspace: workspace, Models: map[string]string{"active": "active-model", "fast": "fast-model", "nested": `{{ model "fast" }}`}, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}, Instrumentation: config.InstrumentationConfig{Enabled: true, HideInputs: true, HideOutputs: true}}
	run, err := newWorkflowAgentRunner(cfg, "main", rocketcode.InertJournal{}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, run.Close()) })

	require.NoError(t, root.Remove(".rocketclaw/agents/main.md"))

	cfg.OpenAI.APIBaseURL = "http://127.0.0.1:1"

	result, err := run.Run(t.Context(), &workflow.AgentRequest{Prompt: "literal !`printf unsafe`"})
	require.NoError(t, err)
	require.JSONEq(t, `"first"`, string(result))

	result, err = run.Run(t.Context(), &workflow.AgentRequest{Prompt: "structured", Worker: workflow.Worker{Name: "reviewer", Instructions: "Worker !`printf unsafe`", Model: "nested", Tools: []string{"skill"}}, Schema: map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []string{"ok"}}})
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(result))

	result, err = run.Run(t.Context(), &workflow.AgentRequest{Prompt: "second"})
	require.NoError(t, err)
	require.JSONEq(t, `"second"`, string(result))

	result, err = run.Run(t.Context(), &workflow.AgentRequest{Prompt: "no-tools", Worker: workflow.Worker{Name: "reasoner", Instructions: "Reason", Tools: []string{}}})
	require.NoError(t, err)
	require.JSONEq(t, `"first"`, string(result))

	mu.Lock()
	require.Len(t, requests, 4)
	first, structured, second, noTools := requests[0], requests[1], requests[2], requests[3]
	mu.Unlock()

	require.Equal(t, "active-model", first["model"])
	require.Contains(t, fmt.Sprint(first["instructions"]), "Main prompt")
	require.Contains(t, fmt.Sprint(first), "literal !`printf unsafe`")
	require.NotContains(t, fmt.Sprint(first["tools"]), `"name":"task"`)
	require.NotContains(t, fmt.Sprint(first["tools"]), "rocketclaw_")
	require.Contains(t, fmt.Sprint(first["tools"]), `name:execute`)
	require.NotContains(t, fmt.Sprint(first["tools"]), `name:read`)

	require.Equal(t, "fast-model", structured["model"])
	require.Contains(t, fmt.Sprint(structured["instructions"]), "Worker !`printf unsafe`")
	require.NotContains(t, fmt.Sprint(structured["instructions"]), "Main prompt")
	require.Contains(t, fmt.Sprint(structured["tools"]), `name:skill`)
	require.Contains(t, fmt.Sprint(structured["tools"]), `name:find_skills`)
	require.NotContains(t, fmt.Sprint(structured["tools"]), `name:read`)
	require.NotContains(t, fmt.Sprint(structured["tools"]), `name:execute`)
	require.Contains(t, fmt.Sprint(structured["text"]), "json_schema")
	require.Contains(t, fmt.Sprint(structured["text"]), "additionalProperties:false")
	require.NotContains(t, fmt.Sprint(structured["text"]), "strict:true")

	require.NotContains(t, fmt.Sprint(second["input"])+" "+fmt.Sprint(second["previous_response_id"]), "first")
	require.Empty(t, noTools["tools"])
	assert.NotEmpty(t, recorder.Ended(), "workflow run should emit configured tracing spans")
}

func TestWorkflowAgentRunnerTagLimits(t *testing.T) {
	workspace := t.TempDir()
	writeMainAgentSkills(t, workspace, "---\nmodel: gpt-5.5\npermission:\n  read: allow\n  rocketclaw:\n    rocketclaw_set_tag: [[customer, internal]]\n---\nBase prompt\n")
	service := newTestSessionServiceAt(t, workspace)

	var (
		tools   []string
		execute bool
	)

	requests := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Instructions string
			Tools        []struct{ Name string }
			Input        []struct{ Type, Output string }
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}

		for _, name := range []string{setTagToolName, getTagsToolName} {
			allowed := tools == nil || slices.Contains(tools, name)

			present := false
			for _, tool := range body.Tools {
				present = present || tool.Name == name
			}

			assert.Equal(t, allowed, present)
			assert.Equal(t, allowed, strings.Contains(body.Instructions, name))
		}

		assert.Contains(t, body.Instructions, "Worker instructions")
		assert.NotContains(t, body.Instructions, "Base prompt")

		var outputs []string

		for _, item := range body.Input {
			if item.Type == "function_call_output" {
				outputs = append(outputs, item.Output)
			}
		}

		requests++

		w.Header().Set("Content-Type", "application/json")

		if requests < 3 {
			name, args := setTagToolName, `{"tag":"customer"}`
			if requests == 2 {
				name, args = getTagsToolName, `{}`
			}

			if execute {
				code := "def main():\n    return rocketclaw_set_tag(tag=\"customer\")\n"
				if requests == 2 {
					code = "def main():\n    return rocketclaw_get_tags()\n"
				}

				writeRawRunFunctionCall(t, w, strconv.Itoa(requests), "execute", struct {
					Code string `json:"code"`
				}{code})
			} else {
				writeRawRunFunctionCall(t, w, strconv.Itoa(requests), name, json.RawMessage(args))
			}
		} else {
			for i, name := range []string{setTagToolName, getTagsToolName} {
				if tools == nil || slices.Contains(tools, name) {
					assert.Contains(t, outputs[i], `{"tags":[`)
				} else {
					assert.Contains(t, outputs[i], "failed")
				}
			}

			writeRawRunMessage(t, w, "done", "message", "done")
		}
	}))
	defer server.Close()

	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}
	run, err := newWorkflowAgentRunner(cfg, "main", rocketcode.InertJournal{}, slog.New(slog.DiscardHandler), sessionTagTools(service, "owning")...)

	require.NoError(t, err)
	defer func() { require.NoError(t, run.Close()) }()

	for _, path := range []bool{false, true} {
		execute = path

		for _, limit := range [][]string{{"execute", setTagToolName}, {"execute", getTagsToolName}, {}, nil} {
			tools, requests = limit, 0
			before, err := sessionTags(t.Context(), service.db, "owning")
			require.NoError(t, err)
			_, err = run.Run(t.Context(), &workflow.AgentRequest{Prompt: "tag", Worker: workflow.Worker{Name: "label", Instructions: "Worker instructions", Tools: tools}})
			require.NoError(t, err)
			require.Equal(t, 3, requests)
			after, err := sessionTags(t.Context(), service.db, "owning")
			require.NoError(t, err)

			if tools == nil || slices.Contains(tools, setTagToolName) {
				require.NotEqual(t, before, after)
			} else {
				require.Equal(t, before, after)
			}
		}
	}
}

func TestWorkflowPermissionReviewerTagGuidance(t *testing.T) {
	workspace := t.TempDir()
	writeMainAgentSkills(t, workspace, "---\nmodel: gpt-5.5\npermission:\n  read: auto(reviewer)\n  rocketclaw:\n    rocketclaw_set_tag: [[worker]]\n---\nWorker prompt\n")
	writeAgent(t, workspace, "reviewer", "---\ndescription: Reviewer\nmodel: gpt-5.4\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[review, published]]\n---\nReviewer prompt\n")
	root, err := os.OpenRoot(workspace)

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.WriteFile("note.txt", []byte("fixture"), 0o644))
	service := newTestSessionServiceAt(t, workspace)

	requests := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model, Instructions string
			Tools               []struct{ Name string }
			Input               []struct{ Type, Output string }
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}

		requests++

		w.Header().Set("Content-Type", "application/json")

		switch {
		case body.Model == "gpt-5.4":
			assert.Contains(t, body.Instructions, "Reviewer prompt")
			assert.Contains(t, body.Instructions, `[["review" "published"]]`)
			assert.Contains(t, body.Instructions, "set an active tag to toggle it off")
			assert.NotContains(t, body.Instructions, `[["worker"]]`)

			for _, name := range []string{setTagToolName, getTagsToolName} {
				assert.Contains(t, body.Tools, struct{ Name string }{name})
				assert.Contains(t, body.Instructions, name)
			}

			writeRawRunMessage(t, w, "approval", "approval", `{"risk_level":"low","user_authorization":"unknown","outcome":"allow","rationale":"Read-only."}`)
		case requests == 1:
			assert.NotContains(t, body.Instructions, "## Session Tags")
			writeRawRunFunctionCall(t, w, "read", "execute", json.RawMessage(`{"code":"def main():\n    return read(filePath=\"note.txt\")\n"}`))
		default:
			for _, item := range body.Input {
				if item.Type == "function_call_output" {
					assert.Contains(t, item.Output, "fixture")
				}
			}

			writeRawRunMessage(t, w, "done", "done", "done")
		}
	}))
	defer server.Close()

	run, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", rocketcode.InertJournal{}, slog.New(slog.DiscardHandler), sessionTagTools(service, "owning")...)

	require.NoError(t, err)
	defer func() { require.NoError(t, run.Close()) }()

	_, err = run.Run(t.Context(), &workflow.AgentRequest{Prompt: "read", Worker: workflow.Worker{Name: "worker", Instructions: "Worker instructions", Tools: []string{"execute"}}})
	require.NoError(t, err)
	require.Equal(t, 3, requests)
}

func TestWorkflowAgentRunnerResolvesNamedProviderModel(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\n---\nMain prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	defaultRequests := make(chan struct{}, 1)

	defaultServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		defaultRequests <- struct{}{}
	}))
	t.Cleanup(defaultServer.Close)

	models := make(chan string, 1)
	workServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			http.Error(w, "decode request", http.StatusBadRequest)

			return
		}

		models <- body.Model

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", "done")
	}))
	t.Cleanup(workServer.Close)

	cfg := &config.Config{
		Workspace: workspace,
		Models:    map[string]string{"worker": "work/worker-api-model"},
		OpenAI:    config.OpenAIConfig{APIBaseURL: defaultServer.URL},
		Providers: map[string]config.OpenAIConfig{"work": {APIBaseURL: workServer.URL}},
	}
	run, err := newWorkflowAgentRunner(cfg, "main", rocketcode.InertJournal{}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, run.Close()) })

	cfg.Providers["work"] = config.OpenAIConfig{APIBaseURL: "http://127.0.0.1:1"}

	result, err := run.Run(t.Context(), &workflow.AgentRequest{Prompt: "run", Worker: workflow.Worker{Name: "worker", Model: "worker"}})
	require.NoError(t, err)
	require.JSONEq(t, `"done"`, string(result))
	assert.Equal(t, "worker-api-model", <-models)

	select {
	case <-defaultRequests:
		t.Fatal("workflow worker used the default provider")
	default:
	}
}

func TestWorkflowAgentRunnerUsesConfiguredAutoApproverModel(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  rocketclaw:\n    code_mode_approve: auto\n  bash:\n    \"printf ok\": auto\n---\nMain prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var models []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		models = append(models, fmt.Sprint(body["model"]))

		w.Header().Set("Content-Type", "application/json")

		switch len(models) {
		case 1:
			writeRawRunFunctionCall(t, w, "resp_1", "execute", executeBashScript("printf ok"))
		case 2, 3:
			writeRawRunMessage(t, w, fmt.Sprintf("resp_%d", len(models)), fmt.Sprintf("msg_%d", len(models)), `{"risk_level":"low","user_authorization":"unknown","outcome":"allow","rationale":"Low risk."}`)
		case 4:
			writeRawRunMessage(t, w, "resp_4", "msg_4", "done")
		default:
			t.Fatalf("unexpected request %d", len(models))
		}
	}))
	t.Cleanup(server.Close)

	run, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}, AutoApproverModel: "review-model"}, "main", rocketcode.InertJournal{}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, run.Close()) })
	result, err := run.Run(t.Context(), &workflow.AgentRequest{Prompt: "run", Worker: workflow.Worker{Name: "worker", Instructions: "work", Tools: []string{"execute"}}})
	require.NoError(t, err)
	require.JSONEq(t, `"done"`, string(result))
	require.Equal(t, []string{"gpt-5.5", "review-model", "review-model", "gpt-5.5"}, models)
}

func TestWorkflowAgentRunnerStructuredOutputUsesFinalAssistantMessage(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  read: {\"*\": allow}\n---\nMain prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("PRIVATE TOOL RESULT"), 0o644))

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++

		w.Header().Set("Content-Type", "application/json")

		switch requests {
		case 1:
			_, err := w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"rsn_1","type":"reasoning","summary":[{"type":"summary_text","text":"checking context"}]},{"id":"msg_1","type":"message","status":"completed","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"checking the fixture","annotations":[]}]},{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"execute","arguments":"{\"code\":\"def main():\\n    return read(filePath=\\\"README.md\\\")\\n\"}"}]}`))
			assert.NoError(t, err)
		case 2:
			writeRawRunMessage(t, w, "resp_2", "msg_2", `{"ok":true,"private":"PRIVATE WORKER RESULT"}`)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	t.Cleanup(server.Close)

	run, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", rocketcode.InertJournal{}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, run.Close()) })

	request := workflow.AgentRequest{
		Prompt: "PRIVATE WORKER PROMPT",
		Schema: map[string]any{
			"type":        "object",
			"description": "PRIVATE SCHEMA",
			"properties": map[string]any{
				"ok":      map[string]any{"type": "boolean"},
				"private": map[string]any{"type": "string"},
			},
			"required": []string{"ok", "private"},
		},
	}
	result, err := run.Run(t.Context(), &request)
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true,"private":"PRIVATE WORKER RESULT"}`, string(result))
	require.Equal(t, 2, requests)
}

func TestWorkflowExplicitSkillWithoutAvailableSubjects(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  skill:\n    unavailable: allow\n---\nMain prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var request map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "resp", "msg", "done")
	}))
	t.Cleanup(server.Close)

	run, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", rocketcode.InertJournal{}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, run.Close()) })

	_, err = run.Run(t.Context(), &workflow.AgentRequest{Prompt: "prompt", Worker: workflow.Worker{Name: "worker", Instructions: "work", Tools: []string{"skill"}}})
	require.NoError(t, err)
	assert.Contains(t, fmt.Sprint(request["tools"]), `name:skill`)
	assert.NotContains(t, fmt.Sprint(request["tools"]), `name:find_skills`)
}

// A recorded worker result is reused only for the same request under the same key.
func TestWorkflowAgentRunnerReplaysOnlyMatchingRecordedWorker(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\n---\nMain prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "resp", "msg", "live "+strconv.Itoa(requests))
	}))
	t.Cleanup(server.Close)

	journal := rocketcode.TracelessJournal{Parent: conversationJournal{store: newTestSessionServiceAt(t, workspace), conversationID: "conversation"}}
	run, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", journal, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, run.Close()) })

	for _, tt := range []struct{ prompt, want string }{{"prompt", `"live 1"`}, {"prompt", `"live 1"`}, {"changed", `"live 2"`}} {
		result, err := run.Run(t.Context(), &workflow.AgentRequest{Prompt: tt.prompt, Key: "turn-1/workflow/0"})
		require.NoError(t, err)
		assert.JSONEq(t, tt.want, string(result), tt.prompt)
	}

	assert.Equal(t, 2, requests, "a changed request runs live")
}

func TestWorkflowAgentRunnerRejectsInvalidOverridesAndStructuredOutput(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  read: {\"*\": allow}\n---\nMain prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", "not JSON")
	}))
	t.Cleanup(server.Close)

	run, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, Models: map[string]string{"known": "gpt-5.5", "broken": `{{ model "missing" }}`, "unavailable": "unknown/model"}, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", rocketcode.InertJournal{}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, run.Close()) })

	_, err = run.Run(t.Context(), &workflow.AgentRequest{Worker: workflow.Worker{Name: "worker", Instructions: "prompt", Model: "missing"}, Prompt: "model"})
	require.ErrorContains(t, err, `workflow worker model "missing" is not configured`)
	_, err = run.Run(t.Context(), &workflow.AgentRequest{Worker: workflow.Worker{Name: "worker", Instructions: "prompt", Model: "broken"}, Prompt: "model"})
	require.ErrorContains(t, err, `render workflow worker model "broken"`)
	require.ErrorContains(t, err, `model "missing" is not configured`)
	_, err = run.Run(t.Context(), &workflow.AgentRequest{Worker: workflow.Worker{Model: "unavailable"}, Prompt: "model"})
	require.ErrorContains(t, err, "prepare workflow rocketcode run")
	require.ErrorContains(t, err, `unknown provider "unknown"`)
	_, err = run.Run(t.Context(), &workflow.AgentRequest{Worker: workflow.Worker{Name: "worker", Instructions: "prompt", Tools: []string{"missing"}}, Prompt: "tools"})
	require.ErrorContains(t, err, `unknown tool "missing"`)
	_, err = run.Run(t.Context(), &workflow.AgentRequest{Prompt: "schema", Schema: map[string]any{"type": "string"}})
	require.ErrorContains(t, err, "workflow worker returned invalid JSON")
	require.Equal(t, 1, requests)
}

func TestWorkflowAgentRunnerMissingPermissionEnvironment(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  edit: {'${ROCKETCLAW_METADATA_FRUIT}/note.txt': allow}\n---\nMain prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "resp", "msg", "done")
	}))
	t.Cleanup(server.Close)

	run, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", rocketcode.InertJournal{}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, run.Close()) })
	_, err = run.Run(t.Context(), &workflow.AgentRequest{Prompt: "work"})

	// The scoped edit rule is dropped for the turn, so the run completes without it.
	require.NoError(t, err)
}

func TestWorkflowAgentRunnerConcurrentDirectoriesAndCancellation(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\n---\nMain prompt\n")
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))

	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	cancelArrived := make(chan struct{})
	cancelDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		if rawRunRequestPrompt(t, body) == "cancel" {
			close(cancelArrived)
			<-r.Context().Done()
			close(cancelDone)

			return
		}

		arrived <- struct{}{}

		<-release
		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", "ok")
	}))
	t.Cleanup(server.Close)

	run, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", rocketcode.InertJournal{}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, run.Close()) })

	type result struct {
		raw json.RawMessage
		err error
	}

	results := make(chan result, 2)

	for _, prompt := range []string{"one", "two"} {
		go func() {
			raw, err := run.Run(t.Context(), &workflow.AgentRequest{Prompt: prompt})
			results <- result{raw: raw, err: err}
		}()
	}

	<-arrived
	<-arrived

	dirs, err := fs.Glob(root.FS(), ".rocketclaw/.rocketcode/workflow-*")
	require.NoError(t, err)
	require.Len(t, dirs, 2)
	close(release)

	for range 2 {
		result := <-results
		require.NoError(t, result.err)
		require.JSONEq(t, `"ok"`, string(result.raw))
	}

	dirs, err = fs.Glob(root.FS(), ".rocketclaw/.rocketcode/workflow-*")
	require.NoError(t, err)
	require.Empty(t, dirs)

	ctx, cancel := context.WithCancel(t.Context())
	canceled := make(chan error, 1)

	go func() {
		_, err := run.Run(ctx, &workflow.AgentRequest{Prompt: "cancel"})
		canceled <- err
	}()

	<-cancelArrived
	cancel()
	require.ErrorIs(t, <-canceled, context.Canceled)
	<-cancelDone

	dirs, err = fs.Glob(root.FS(), ".rocketclaw/.rocketcode/workflow-*")
	require.NoError(t, err)
	require.Empty(t, dirs)

	// A runtime-directory replacement must fail before a provider request starts.
	require.NoError(t, root.RemoveAll(".rocketclaw/.rocketcode"))
	require.NoError(t, root.WriteFile(".rocketclaw/.rocketcode", []byte("not a directory"), 0o600))
	_, err = run.Run(t.Context(), &workflow.AgentRequest{Prompt: "blocked"})
	require.ErrorContains(t, err, "create workflow shell temp dir")
}

func TestWorkflowAgentRunnerReportsSetupFailures(t *testing.T) {
	for _, scenario := range []string{"missing workspace", "missing definitions", "blocked runtime directory"} {
		t.Run(scenario, func(t *testing.T) {
			workspace := t.TempDir()
			root, err := os.OpenRoot(workspace)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })

			want := "open workspace agent and skills"

			switch scenario {
			case "missing workspace":
				workspace = filepath.Join(workspace, "missing")
				want = "open workspace root"
			case "blocked runtime directory":
				writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\n---\nMain prompt\n")
				require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))
				require.NoError(t, root.WriteFile(".rocketclaw/.rocketcode", []byte("not a directory"), 0o600))

				want = "create workflow shell temp parent dir"
			}

			runner, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace}, "main", rocketcode.InertJournal{}, slog.New(slog.DiscardHandler))
			require.ErrorContains(t, err, want)
			require.Nil(t, runner)
		})
	}
}

func TestWorkflowAgentRunnerReturnsShellDirectoryCleanupError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		runFailure bool
	}{
		{name: "cleanup failure"},
		{name: "run and cleanup failure", runFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\n---\nMain prompt\n")
			root, err := os.OpenRoot(workspace)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))

			const (
				parent      = ".rocketclaw/.rocketcode"
				savedParent = ".rocketclaw/.rocketcode-saved"
			)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				dirs, err := fs.Glob(root.FS(), parent+"/workflow-*")
				if !assert.NoError(t, err) || !assert.Len(t, dirs, 1) {
					http.Error(w, "inspect workflow directory", http.StatusInternalServerError)

					return
				}

				if !assert.NoError(t, root.Rename(parent, savedParent)) || !assert.NoError(t, root.Symlink("../..", parent)) {
					http.Error(w, "replace workflow parent", http.StatusInternalServerError)

					return
				}

				w.Header().Set("Content-Type", "application/json")

				if tc.runFailure {
					_, err := w.Write([]byte("{"))
					assert.NoError(t, err)

					return
				}

				writeRawRunMessage(t, w, "response", "message", "ok")
			}))
			t.Cleanup(server.Close)

			run, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", rocketcode.InertJournal{}, slog.New(slog.DiscardHandler))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, run.Close()) })

			_, err = run.Run(t.Context(), &workflow.AgentRequest{Prompt: "run"})

			require.NoError(t, root.Remove(parent))
			require.NoError(t, root.Rename(savedParent, parent))
			dirs, errGlob := fs.Glob(root.FS(), parent+"/workflow-*")
			require.NoError(t, errGlob)
			require.Len(t, dirs, 1)
			require.NoError(t, root.RemoveAll(dirs[0]))

			require.ErrorContains(t, err, "remove workflow shell temp dir")

			if tc.runFailure {
				require.ErrorContains(t, err, "run workflow rocketcode turn")
			}
		})
	}
}

func rawRunRequestPrompt(t *testing.T, body map[string]any) string {
	t.Helper()

	input, ok := body["input"].([]any)
	require.True(t, ok)

	if len(input) == 0 {
		return ""
	}

	for _, v := range slices.Backward(input) {
		message, ok := v.(map[string]any)
		require.True(t, ok)

		content, ok := message["content"].(string)
		if ok {
			return content
		}
	}

	return ""
}

func executeBashScript(command string) map[string]string {
	return map[string]string{"code": "def main():\n    return bash(command=" + strconv.Quote(command) + ")\n"}
}

func writeRawRunFunctionCall(t *testing.T, w http.ResponseWriter, responseID, name string, args any) {
	t.Helper()

	data, err := json.Marshal(args)
	require.NoError(t, err)
	_, err = fmt.Fprintf(w, `{"id":%q,"object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":%q,"type":"function_call","status":"completed","call_id":%q,"name":%q,"arguments":%q}]}`, responseID, "fc_call_1", "call_1", name, string(data))
	require.NoError(t, err)
}

func writeRawRunMessage(t *testing.T, w http.ResponseWriter, responseID, messageID, text string) {
	t.Helper()

	_, err := fmt.Fprintf(w, `{"id":%q,"object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":%q,"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q,"annotations":[]}]}]}`, responseID, messageID, text)
	require.NoError(t, err)
}
