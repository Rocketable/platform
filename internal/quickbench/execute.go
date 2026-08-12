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

type runOptions struct {
	json    bool
	timeout time.Duration // 0 = none
}

// Report is the run + ELO output.
type Report struct {
	Path    string       `json:"path"`
	Name    string       `json:"name,omitempty"`
	Cells   []CellResult `json:"cells"`
	Ladder  []EloRating  `json:"ladder,omitempty"`
	Pairs   []PairResult `json:"pairs,omitempty"`
	Skipped string       `json:"eloSkipped,omitempty"`
}

// CellResult is one variation×matrix run.
type CellResult struct {
	Label         string             `json:"label"`
	Variation     string             `json:"variation"`
	Matrix        string             `json:"matrix"`
	Model         string             `json:"model"` // same as Matrix; kept for report compatibility
	Text          string             `json:"text,omitempty"`
	ToolCalls     []observedToolCall `json:"toolCalls,omitempty"`
	Error         string             `json:"error,omitempty"`
	Latency       string             `json:"latency"`
	LatencyMillis int64              `json:"latencyMillis"`
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

// cellRunner runs one matrix cell. Tests inject a stub.
type cellRunner func(ctx context.Context, providers rocketcode.Providers, bar *BAR, variation Variation, entry MatrixEntry, timeout time.Duration) CellResult

func runBAR(ctx context.Context, providers rocketcode.Providers, bar *BAR, opt runOptions) (Report, error) {
	return runBARWith(ctx, providers, bar, opt, runCell, nil)
}

func runBARWith(ctx context.Context, providers rocketcode.Providers, bar *BAR, opt runOptions, run cellRunner, judge judgePairFunc) (Report, error) {
	timeout := opt.timeout

	matrix := bar.Matrix
	if len(matrix) == 0 {
		matrix = []MatrixEntry{{ID: "default"}}
	}

	report := Report{Path: bar.Path, Name: bar.Meta.Name}
	for _, variation := range bar.Variations {
		for _, entry := range matrix {
			cell := run(ctx, providers, bar, variation, entry, timeout)
			report.Cells = append(report.Cells, cell)
		}
	}

	if strings.TrimSpace(bar.Criteria) == "" || isStubCriteria(bar.Criteria) {
		report.Skipped = "elo criteria is empty or capture stub; edit bench.yaml elo.criteria before ranking"
		return report, nil
	}

	judgeSel, err := parseModelSelector(bar.Judge)
	if err != nil {
		return report, fmt.Errorf("bench.yaml elo.model: %w", err)
	}

	ladder, pairs, err := rankCells(ctx, providers, judgeSel, bar.Criteria, report.Cells, judge)
	if err != nil {
		// Keep cell outputs; principal still needs the matrix when ELO cannot run (e.g. single cell).
		report.Skipped = err.Error()
		return report, nil
	}

	report.Ladder = ladder
	report.Pairs = pairs

	return report, nil
}

func isStubCriteria(criteria string) bool {
	return strings.Contains(criteria, "TODO: replace with ranking criteria")
}

func runCell(ctx context.Context, providers rocketcode.Providers, bar *BAR, variation Variation, entry MatrixEntry, timeout time.Duration) CellResult {
	label := cellLabel(variation.ID, entry.ID)
	result := CellResult{Label: label, Variation: variation.ID, Matrix: entry.ID, Model: entry.ID}
	startedAt := time.Now()

	if timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	prior, finalPrompt, err := conversationParts(variation)
	if err != nil {
		return finishCell(result, startedAt, "%s", err.Error())
	}

	agents, rootName, err := buildAgents(bar, variation, entry.Agents)
	if err != nil {
		return finishCell(result, startedAt, "%s", err.Error())
	}

	recorder := &toolRecorder{}

	tools := make([]rocketcode.Tool, 0, len(variation.Tools))
	for _, tool := range variation.Tools {
		if tool.Name == "task" {
			// Live task tool must spawn BAR agents; never mock delegation.
			continue
		}

		params := maps.Clone(tool.Parameters)
		if params == nil {
			params = map[string]any{"type": "object"}
		}

		tools = append(tools, rocketcode.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  params,
			Permission:  "tools",
			Call: func(_ context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
				recorder.record(tool.Name, raw)
				return rocketcode.TextToolResult(tool.Response), nil
			},
		})
	}

	tmpDir, err := scratchDir("cell-*")
	if err != nil {
		return finishCell(result, startedAt, "create workspace: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	shellTempDir := filepath.Join(tmpDir, "shell-tmp")
	if err := os.Mkdir(shellTempDir, 0o700); err != nil {
		return finishCell(result, startedAt, "create shell temp dir: %v", err)
	}

	root, err := os.OpenRoot(tmpDir)
	if err != nil {
		return finishCell(result, startedAt, "open workspace: %v", err)
	}

	defer func() { _ = root.Close() }()

	rootAgent := agents.Items[rootName]
	agentModel := shared.ResponsesModel(rootAgent.Model)

	config := rocketcode.Config{
		Model:                  agentModel,
		ReasoningEffort:        shared.ReasoningEffort(rootAgent.ReasoningEffort),
		Diagnostics:            true,
		ParallelToolCalls:      16,
		ShellTempDir:           shellTempDir,
		ChildRunLogger:         rocketcode.DiscardChildRunLog,
		CheckpointSink:         rocketcode.InertCheckpointSink{},
		CustomTools:            tools,
		AutoApprovePermissions: true,
		ShellCommand:           shellCommandFromBashDoubles(variation.BashDoubles),
	}

	runtime, err := rocketcode.NewWithProviders(providers, &config, root, agents, rocketcode.Skills{Items: map[string]rocketcode.Skill{}}, rootName, io.Discard)
	if err != nil {
		return finishCell(result, startedAt, "create runtime: %v", err)
	}

	replay, err := replayEntry(prior)
	if err != nil {
		return finishCell(result, startedAt, "build replay: %v", err)
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
	result.Latency = time.Since(startedAt).String()
	result.LatencyMillis = time.Since(startedAt).Milliseconds()

	return result
}

func finishCell(result CellResult, startedAt time.Time, format string, args ...any) CellResult {
	result.Error = fmt.Sprintf(format, args...)
	result.Latency = time.Since(startedAt).String()
	result.LatencyMillis = time.Since(startedAt).Milliseconds()

	return result
}

func cellLabel(variationID, matrixID string) string {
	if strings.TrimSpace(matrixID) == "" {
		return variationID
	}

	return variationID + "@" + matrixID
}

func conversationParts(v Variation) (prior []Message, final string, err error) {
	if err := validateTranscript(v.Transcript); err != nil {
		return nil, "", err
	}

	lastUser := -1
	for i, msg := range v.Transcript {
		if msg.Role == "user" {
			lastUser = i
		}
	}

	prior = append([]Message(nil), v.Transcript[:lastUser]...)

	return prior, v.Transcript[lastUser].Text, nil
}

func replayEntry(prior []Message) (func(func(rocketcode.SessionEntry, error) bool), error) {
	if len(prior) == 0 {
		return func(func(rocketcode.SessionEntry, error) bool) {}, nil
	}

	items := make([]responses.ResponseInputItemUnionParam, 0, len(prior))
	for _, msg := range prior {
		messageParam := responses.EasyInputMessageParam{
			Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(msg.Text)},
			Role:    responses.EasyInputMessageRole(msg.Role),
			Type:    "message",
		}
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

func scratchDir(pattern string) (string, error) {
	root := filepath.Join(".tmp", "quickbench")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}

	return os.MkdirTemp(root, pattern)
}

func writeHumanReport(w io.Writer, report Report) error {
	if report.Name != "" {
		if _, err := fmt.Fprintf(w, "%s (%s)\n", report.Name, report.Path); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(w, report.Path); err != nil {
			return err
		}
	}

	for _, cell := range report.Cells {
		status := "ok"
		if cell.Error != "" {
			status = "error: " + cell.Error
		}

		if _, err := fmt.Fprintf(w, "  %s  %s  (%s)\n", cell.Label, status, cell.Latency); err != nil {
			return err
		}

		if cell.Text != "" {
			if _, err := fmt.Fprintf(w, "    %s\n", singleLine(cell.Text)); err != nil {
				return err
			}
		}
	}

	if report.Skipped != "" {
		if _, err := fmt.Fprintf(w, "\nELO skipped: %s\n", report.Skipped); err != nil {
			return err
		}

		return nil
	}

	if len(report.Ladder) == 0 {
		return nil
	}

	if _, err := fmt.Fprintln(w, "\nELO ladder:"); err != nil {
		return err
	}

	for i, row := range report.Ladder {
		if _, err := fmt.Fprintf(w, "  %2d. %s  %.1f\n", i+1, row.Label, row.Rating); err != nil {
			return err
		}
	}

	if len(report.Pairs) > 0 {
		if _, err := fmt.Fprintln(w, "\nPairs:"); err != nil {
			return err
		}

		for _, pair := range report.Pairs {
			outcome := pair.Winner
			if pair.Error != "" {
				outcome = "error: " + pair.Error
			}

			if _, err := fmt.Fprintf(w, "  %s vs %s → %s\n", pair.A, pair.B, outcome); err != nil {
				return err
			}
		}
	}

	return nil
}

func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 120 {
		return s[:117] + "..."
	}

	return s
}
