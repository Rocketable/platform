package quickbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

type report struct {
	Files []fileResult `json:"files"`
}

type fileResult struct {
	Path        string        `json:"path"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Tags        []string      `json:"tags,omitempty"`
	Models      []modelResult `json:"models"`
}

type modelResult struct {
	Model    string      `json:"model"`
	Runs     []runResult `json:"runs"`
	PassRate float64     `json:"passRate"`
}

type runResult struct {
	Run           int                `json:"run"`
	Passed        bool               `json:"passed"`
	Text          string             `json:"text,omitempty"`
	ToolCalls     []observedToolCall `json:"toolCalls,omitempty"`
	Failures      []string           `json:"failures,omitempty"`
	Error         string             `json:"error,omitempty"`
	Latency       string             `json:"latency"`
	LatencyMillis int64              `json:"latencyMillis"`
	TokenUsage    map[string]int64   `json:"tokenUsage,omitempty"`
	Cost          string             `json:"cost,omitempty"`
}

type observedToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Raw       string         `json:"raw,omitempty"`
}

type toolRecorder struct {
	mu    sync.Mutex
	calls []observedToolCall
}

func (r *toolRecorder) record(name string, raw json.RawMessage) {
	var arguments map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &arguments)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, observedToolCall{Name: name, Arguments: arguments, Raw: string(raw)})
}

func (r *toolRecorder) snapshot() []observedToolCall {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]observedToolCall(nil), r.calls...)
}

func runBenchmarkFile(ctx context.Context, providers rocketcode.Providers, opt options, file benchmarkFile) (fileResult, error) {
	result := fileResult{Path: file.Path, Name: file.Benchmark.Name, Description: file.Benchmark.Description, Tags: file.Benchmark.Tags}
	for _, model := range opt.models {
		runs := file.Benchmark.Runs
		if runs == 0 {
			runs = 1
		}

		if opt.runs > 0 {
			runs = opt.runs
		}

		modelResults := modelResult{Model: model.Raw, Runs: make([]runResult, 0, runs)}
		passed := 0
		for run := 1; run <= runs; run++ {
			runResult := runOne(ctx, providers, opt, file.Benchmark, model, run)
			if runResult.Passed {
				passed++
			}

			modelResults.Runs = append(modelResults.Runs, runResult)
		}

		modelResults.PassRate = float64(passed) / float64(runs)
		result.Models = append(result.Models, modelResults)
	}

	return result, nil
}

func runOne(ctx context.Context, providers rocketcode.Providers, opt options, bench benchmark, model modelSelector, runNumber int) runResult {
	timeout := bench.duration
	if opt.timeoutOK {
		timeout = opt.timeout
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	startedAt := time.Now()
	result := runResult{Run: runNumber, TokenUsage: map[string]int64{}, Cost: ""}
	recorder := &toolRecorder{}
	systemPrompt, prior, finalPrompt := benchConversation(bench)
	tools := make([]rocketcode.Tool, 0, len(bench.Tools))
	for _, tool := range bench.Tools {
		tool := tool
		tools = append(tools, rocketcode.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  maps.Clone(tool.Parameters),
			Permission:  "tools",
			Call: func(_ context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
				recorder.record(tool.Name, raw)

				return rocketcode.TextToolResult(tool.Static.Response), nil
			},
		})
	}

	tmpDir, err := os.MkdirTemp("", "quickbench-*")
	if err != nil {
		result.Error = fmt.Sprintf("create temporary workspace: %v", err)
		result.Latency = time.Since(startedAt).String()
		result.LatencyMillis = time.Since(startedAt).Milliseconds()
		return result
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	shellOutputDir := filepath.Join(tmpDir, "shell-outputs")
	if err := os.Mkdir(shellOutputDir, 0o700); err != nil {
		result.Error = fmt.Sprintf("create shell output dir: %v", err)
		result.Latency = time.Since(startedAt).String()
		result.LatencyMillis = time.Since(startedAt).Milliseconds()
		return result
	}

	root, err := os.OpenRoot(tmpDir)
	if err != nil {
		result.Error = fmt.Sprintf("open temporary workspace: %v", err)
		result.Latency = time.Since(startedAt).String()
		result.LatencyMillis = time.Since(startedAt).Milliseconds()
		return result
	}
	defer func() { _ = root.Close() }()

	permission := rocketcode.PermissionSet{}
	if err := permission.Allow("tools", "*"); err != nil {
		result.Error = fmt.Sprintf("configure tool permissions: %v", err)
		result.Latency = time.Since(startedAt).String()
		result.LatencyMillis = time.Since(startedAt).Milliseconds()
		return result
	}

	agent := rocketcode.Agent{Name: "main", Prompt: systemPrompt, Verbosity: model.Verbosity, Permission: permission}
	config := rocketcode.Config{
		Model:             shared.ResponsesModel(model.rocketCodeModel()),
		ReasoningEffort:   shared.ReasoningEffort(model.ReasoningEffort),
		Diagnostics:       true,
		ParallelToolCalls: 16,
		ShellOutputDir:    shellOutputDir,
		CustomTools:       tools,
	}

	runtime, err := rocketcode.NewWithProviders(providers, &config, root, rocketcode.Agents{Items: map[string]rocketcode.Agent{"main": agent}}, rocketcode.Skills{Items: map[string]rocketcode.Skill{}}, "main", io.Discard)
	if err != nil {
		result.Error = fmt.Sprintf("create RocketCode runtime: %v", err)
		result.Latency = time.Since(startedAt).String()
		result.LatencyMillis = time.Since(startedAt).Milliseconds()
		return result
	}

	replay, err := replayEntry(prior)
	if err != nil {
		result.Error = fmt.Sprintf("build replay: %v", err)
		result.Latency = time.Since(startedAt).String()
		result.LatencyMillis = time.Since(startedAt).Milliseconds()
		return result
	}

	input := make(chan rocketcode.PromptInput, 1)
	output := make(chan rocketcode.ChatResponse, 64)
	input <- rocketcode.PromptInput{Text: finalPrompt, Responses: output}
	close(input)

	errCh := make(chan error, 1)
	go func() {
		errCh <- runtime.Loop(ctx, input, replay, func(rocketcode.SessionEntry) error { return nil }, make(chan os.Signal, 1))
	}()

	var text strings.Builder
	for item := range output {
		if item.Kind == rocketcode.ChatResponseAssistantMessage {
			text.WriteString(item.Text)
		}
	}

	if err := <-errCh; err != nil {
		result.Error = err.Error()
	}

	result.Text = text.String()
	result.ToolCalls = recorder.snapshot()
	result.Failures = evaluateAssertions(bench.Expected, result.Text, result.ToolCalls)
	result.Passed = result.Error == "" && len(result.Failures) == 0
	result.Latency = time.Since(startedAt).String()
	result.LatencyMillis = time.Since(startedAt).Milliseconds()

	return result
}

func benchConversation(bench benchmark) (string, []message, string) {
	systemPrompt := ""
	start := 0
	if bench.Inference[0].Role == "system" {
		systemPrompt = bench.Inference[0].Text
		start = 1
	}

	prior := append([]message(nil), bench.Inference[start:len(bench.Inference)-1]...)

	return systemPrompt, prior, bench.Inference[len(bench.Inference)-1].Text
}

func replayEntry(prior []message) (func(func(rocketcode.SessionEntry, error) bool), error) {
	if len(prior) == 0 {
		return func(func(rocketcode.SessionEntry, error) bool) {}, nil
	}

	items := make([]responses.ResponseInputItemUnionParam, 0, len(prior))
	for _, msg := range prior {
		messageParam := responses.EasyInputMessageParam{Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(msg.Text)}, Role: responses.EasyInputMessageRole(msg.Role), Type: "message"}
		if msg.Role == "assistant" {
			messageParam.Phase = responses.EasyInputMessagePhase("final_answer")
		}

		items = append(items, responses.ResponseInputItemUnionParam{OfMessage: &messageParam})
	}

	replayInput, err := rocketcode.ReplayInputFromParams(items)
	if err != nil {
		return nil, err
	}

	entry := rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now().UTC(), ReplayInput: replayInput}

	return func(yield func(rocketcode.SessionEntry, error) bool) {
		yield(entry, nil)
	}, nil
}
