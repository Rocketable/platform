package backend

import (
	"context"
	"encoding/json"
	"errors"
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
	_, ok := decision.Decision()
	assert.False(t, ok)

	tool := decision.Tool()
	_, err := tool.Call(context.Background(), json.RawMessage("{"), make(chan rocketcode.ChatResponse))
	require.ErrorContains(t, err, "parse raw run decision")

	result, err := tool.Call(context.Background(), json.RawMessage(`{"payload":"ship it"}`), make(chan rocketcode.ChatResponse))
	require.NoError(t, err)
	assert.Equal(t, "queued for verbatim delivery", result.Output)

	payload, ok := decision.Decision()
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
	run, cleanup, err := newWorkflowAgentRunner(cfg, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })

	require.NoError(t, root.Remove(".rocketclaw/agents/main.md"))

	cfg.OpenAI.APIBaseURL = "http://127.0.0.1:1"

	result, err := run(t.Context(), workflow.AgentRequest{Prompt: "literal !`printf unsafe`"}, discardWorkflowThinking)
	require.NoError(t, err)
	require.JSONEq(t, `"first"`, string(result))

	result, err = run(t.Context(), workflow.AgentRequest{Prompt: "structured", Worker: workflow.Worker{Name: "reviewer", Instructions: "Worker !`printf unsafe`", Model: "nested", Tools: []string{"skill"}}, Schema: map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []string{"ok"}}}, discardWorkflowThinking)
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(result))

	result, err = run(t.Context(), workflow.AgentRequest{Prompt: "second"}, discardWorkflowThinking)
	require.NoError(t, err)
	require.JSONEq(t, `"second"`, string(result))

	result, err = run(t.Context(), workflow.AgentRequest{Prompt: "no-tools", Worker: workflow.Worker{Name: "reasoner", Instructions: "Reason", Tools: []string{}}}, discardWorkflowThinking)
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
	run, cleanup, err := newWorkflowAgentRunner(cfg, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })

	cfg.Providers["work"] = config.OpenAIConfig{APIBaseURL: "http://127.0.0.1:1"}

	result, err := run(t.Context(), workflow.AgentRequest{Prompt: "run", Worker: workflow.Worker{Name: "worker", Model: "worker"}}, discardWorkflowThinking)
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
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  bash:\n    \"printf ok\": auto\n---\nMain prompt\n")
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

	run, cleanup, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}, AutoApproverModel: "review-model"}, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })
	result, err := run(t.Context(), workflow.AgentRequest{Prompt: "run", Worker: workflow.Worker{Name: "worker", Instructions: "work", Tools: []string{"execute"}}}, discardWorkflowThinking)
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

	run, cleanup, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })

	var thinking []string

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
	result, err := run(t.Context(), request, func(_ context.Context, text string) error {
		thinking = append(thinking, text)
		return nil
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true,"private":"PRIVATE WORKER RESULT"}`, string(result))
	require.Equal(t, 2, requests)
	assert.Equal(t, []string{"checking context", "checking the fixture", "Execute", "Execute → Read: README.md"}, thinking)

	for _, private := range []string{"PRIVATE TOOL RESULT", "PRIVATE WORKER RESULT", "PRIVATE WORKER PROMPT", "PRIVATE SCHEMA"} {
		assert.NotContains(t, strings.Join(thinking, "\n"), private)
	}
}

func TestWorkflowAgentRunnerCancelsOnThinkingFailure(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  read: {\"*\": allow}\n---\nMain prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("fixture"), 0o644))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeRawRunFunctionCall(t, w, "resp_1", "execute", executeBashScript("true"))
	}))
	t.Cleanup(server.Close)

	run, cleanup, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })

	errThinking := errors.New("publish activity")
	_, err = run(t.Context(), workflow.AgentRequest{Prompt: "review"}, func(context.Context, string) error { return errThinking })
	require.ErrorIs(t, err, errThinking)
	require.ErrorContains(t, err, "publish workflow agent thinking")
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

	run, cleanup, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })

	_, err = run(t.Context(), workflow.AgentRequest{Prompt: "prompt", Worker: workflow.Worker{Name: "worker", Instructions: "work", Tools: []string{"skill"}}}, discardWorkflowThinking)
	require.NoError(t, err)
	assert.Contains(t, fmt.Sprint(request["tools"]), `name:skill`)
	assert.NotContains(t, fmt.Sprint(request["tools"]), `name:find_skills`)
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

	run, cleanup, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, Models: map[string]string{"known": "gpt-5.5", "broken": `{{ model "missing" }}`}, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })

	_, err = run(t.Context(), workflow.AgentRequest{Worker: workflow.Worker{Name: "worker", Instructions: "prompt", Model: "missing"}, Prompt: "model"}, discardWorkflowThinking)
	require.ErrorContains(t, err, `workflow worker model "missing" is not configured`)
	_, err = run(t.Context(), workflow.AgentRequest{Worker: workflow.Worker{Name: "worker", Instructions: "prompt", Model: "broken"}, Prompt: "model"}, discardWorkflowThinking)
	require.ErrorContains(t, err, `render workflow worker model "broken"`)
	require.ErrorContains(t, err, `model "missing" is not configured`)
	_, err = run(t.Context(), workflow.AgentRequest{Worker: workflow.Worker{Name: "worker", Instructions: "prompt", Tools: []string{"missing"}}, Prompt: "tools"}, discardWorkflowThinking)
	require.ErrorContains(t, err, `unknown tool "missing"`)
	_, err = run(t.Context(), workflow.AgentRequest{Prompt: "schema", Schema: map[string]any{"type": "string"}}, discardWorkflowThinking)
	require.ErrorContains(t, err, "workflow worker returned invalid JSON")
	require.Equal(t, 1, requests)
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

	run, cleanup, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup()) })

	type result struct {
		raw json.RawMessage
		err error
	}

	results := make(chan result, 2)

	for _, prompt := range []string{"one", "two"} {
		go func() {
			raw, err := run(t.Context(), workflow.AgentRequest{Prompt: prompt}, discardWorkflowThinking)
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
		_, err := run(ctx, workflow.AgentRequest{Prompt: "cancel"}, discardWorkflowThinking)
		canceled <- err
	}()

	<-cancelArrived
	cancel()
	require.ErrorIs(t, <-canceled, context.Canceled)
	<-cancelDone

	dirs, err = fs.Glob(root.FS(), ".rocketclaw/.rocketcode/workflow-*")
	require.NoError(t, err)
	require.Empty(t, dirs)
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

			run, cleanup, err := newWorkflowAgentRunner(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, "main", slog.New(slog.DiscardHandler))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, cleanup()) })

			_, err = run(t.Context(), workflow.AgentRequest{Prompt: "run"}, discardWorkflowThinking)

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

func discardWorkflowThinking(context.Context, string) error { return nil }

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
