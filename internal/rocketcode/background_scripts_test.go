package rocketcode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestTurnBoundToolRefusesDetachedWork(t *testing.T) {
	const (
		args    = `{"code":"def main():\n    return ask() + \"|\" + ask()\n","description":"Ask","background":%t}`
		refusal = "Refused: ask did nothing, because this call now runs as background work, after the turn it acts on. Nothing was shown to the user, and nobody will answer. This text is not ask's result or the user's answer. Decide without it, or call ask in the turn that receives this job's result."
	)

	for _, tc := range []struct {
		name             string
		background, move bool
		want             string
	}{
		{name: "foreground", want: "asked|asked"},
		{name: "started in background", background: true, move: true, want: refusal + "|" + refusal},
		{name: "moved mid-run", move: true, want: "asked|" + refusal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var (
					work     errgroup.Group
					finished string
					errWork  error
				)

				release := make(chan struct{})
				jobs := enabledBackgroundJobs()
				jobs.RunFunc = InertBackgroundJobs{}.Run

				if tc.move {
					jobs.RunFunc = func(ctx context.Context, _ *BackgroundJob, w BackgroundWork) (BackgroundResult, error) {
						work.Go(func() error {
							finished, errWork = w.RunBackground(context.WithoutCancel(ctx))
							return nil
						})
						synctest.Wait() // An attached script blocks in its first ask before it moves.

						return BackgroundResult{Moved: true}, nil
					}
				} else {
					close(release)
				}

				ask, err := customLooperTool(&Tool{Name: "ask", Permission: "probe", TurnBound: true, Call: func(context.Context, json.RawMessage, chan<- ChatResponse) (ToolResult, error) {
					<-release
					return TextToolResult("asked"), nil
				}})
				require.NoError(t, err)

				loop := backgroundTestLooper(t, backgroundPermissions, jobs, func(context.Context) (ToolResult, error) { return TextToolResult("probed"), nil })
				loop.CodeModeHosts["ask"] = ask

				outputs, _, err := loop.dispatchToolCalls(t.Context(), responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{testFunctionCall("item", "call-1", executeToolName, fmt.Sprintf(args, tc.background))}), nil, make(chan ChatResponse, 64))
				require.NoError(t, err)

				got := outputs[0].Result.Output

				if tc.move {
					close(release)
					require.NoError(t, work.Wait())
					require.NoError(t, errWork)

					got = finished
				}

				require.Equal(t, tc.want, got)

				// A background subagent calls the same tool top-level, inside its job's context.
				detached := context.WithValue(t.Context(), backgroundSinkKey{}, &backgroundSink{detached: true})
				result, err := ask.Call(detached, json.RawMessage(`{}`), nil, toolCallMetadata{})
				require.NoError(t, err)
				require.Equal(t, refusal, result.Output)
			})
		})
	}
}

func TestBackgroundExecuteRetainsLargeResult(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	client := openai.NewClient()
	agents := Agents{Items: map[string]Agent{"main": {Name: "main", Model: "gpt-5.4", Permission: parsePermissionYAML(t, "rocketclaw: {allow_background: allow}\nread: {'*': allow}")}}}

	var finished string

	jobs := enabledBackgroundJobs()
	jobs.RunFunc = func(ctx context.Context, _ *BackgroundJob, w BackgroundWork) (BackgroundResult, error) {
		var errRun error

		finished, errRun = w.RunBackground(ctx)
		if errRun != nil {
			return BackgroundResult{}, fmt.Errorf("run work: %w", errRun)
		}

		return BackgroundResult{Moved: true}, nil
	}

	// turn builds a conversation's runtime for one turn, as RocketClaw does.
	turn := func(conversation, turnID string) (*Runtime, context.Context) {
		config := testWorkspaceConfig(t, root.Name())
		config.BackgroundJobs = jobs
		config.RetainedResultDir = filepath.Join("retained", conversation)
		loop, err := New(&client, config, root, agents, Skills{Items: map[string]Skill{}}, "main", nil)
		require.NoError(t, err)

		loop.observations = &turnObservations{journal: InertJournal{}, turnID: turnID}
		loop.restoreTurnExecuteResults(turnID)
		t.Cleanup(loop.deleteTurnExecuteResults)

		return loop, withToolCallContext(t.Context(), loop, nil, "call-1")
	}
	execute := func(loop *Runtime, ctx context.Context) string {
		raw, err := json.Marshal(struct {
			Code        string `json:"code"`
			Description string `json:"description"`
			Background  bool   `json:"background"`
		}{Code: "def main():\n    return \"line\\n\" * 2100\n", Description: "Print", Background: true})
		require.NoError(t, err)
		_, err = loop.Tools[executeToolName].Call(ctx, raw, make(chan ChatResponse, 8), toolCallMetadata{callID: "call-1", observations: loop.observations, progress: &PublicProgress{}})
		require.NoError(t, err)
		require.Contains(t, finished, "This result expires in 7 days.")
		require.Equal(t, 2000, strings.Count(finished, "line\n"))

		_, footer, ok := strings.Cut(finished, `result_id="`)
		require.True(t, ok)

		id, _, _ := strings.Cut(footer, `"`)

		return id
	}
	load := func(loop *Runtime, ctx context.Context, id string) (ToolResult, error) {
		raw, err := json.Marshal(loadExecuteResultParams{ResultID: id, StartLine: 2100})
		require.NoError(t, err)

		return loop.Tools[loadExecuteResultToolName].Call(ctx, raw, nil, emptyToolCallMetadata())
	}
	stored := func(id string) string { return defaultSpillRel + "/retained/a/" + id + ".txt" }
	expire := func(id string) {
		old := time.Now().Add(-retainedResultTTL - time.Hour)
		require.NoError(t, root.Chtimes(stored(id), old, old))
	}

	first, ctx := turn("a", "turn-1")
	id := execute(first, ctx)
	first.deleteTurnExecuteResults()

	later, ctx := turn("a", "turn-2")
	page, err := load(later, ctx, id)
	require.NoError(t, err)
	require.Equal(t, "line\n\n[EOF]\n", page.Output)

	loader := later.Tools[loadExecuteResultToolName]
	decision, err := later.permissionDecision(loadExecuteResultToolName, &loader, json.RawMessage(`{"result_id":"`+id+`"}`))
	require.NoError(t, err, "a later turn's dispatch admits a retained ID")
	require.False(t, decision.denied)

	raw, err := json.Marshal(readToolParams{FilePath: stored(id)})
	require.NoError(t, err)

	read := later.CodeModeHosts["read"]
	result, err := read.Call(ctx, raw, nil, emptyToolCallMetadata())
	require.NoError(t, err)
	require.Contains(t, result.Output, deniedSpillAccess)

	other, otherCtx := turn("b", "turn-3")
	_, err = load(other, otherCtx, id)
	require.EqualError(t, err, "unknown or expired execute result")

	expire(id)
	_, err = load(later, ctx, id)
	require.EqualError(t, err, "execute result expired")
	_, err = root.Stat(stored(id))
	require.ErrorIs(t, err, os.ErrNotExist)

	swept := execute(later, ctx)
	expire(swept)

	kept := execute(later, ctx)
	_, err = root.Stat(stored(swept))
	require.ErrorIs(t, err, os.ErrNotExist, "retaining a result sweeps expired ones")
	_, err = load(later, ctx, kept)
	require.NoError(t, err)

	// A move landing after an attached script stored its result for the turn finds the job settled,
	// so the call keeps that turn-scoped result instead of leaving the turn.
	jobs.RunFunc = func(ctx context.Context, _ *BackgroundJob, w BackgroundWork) (BackgroundResult, error) {
		output, errRun := w.RunBackground(ctx)
		require.NoError(t, errRun)
		require.False(t, w.Detach())

		return BackgroundResult{Output: output}, nil
	}
	attached := `{"code":"def main():\n    return \"line\\n\" * 2100\n","description":"Print","background":false}`
	result, err = later.Tools[executeToolName].Call(ctx, json.RawMessage(attached), make(chan ChatResponse, 8), toolCallMetadata{callID: "call-1", observations: later.observations, progress: &PublicProgress{}})
	require.NoError(t, err)
	require.Contains(t, result.Output, "This result expires when this turn ends.")

	for _, dir := range []string{"", "../outside", "/absolute"} {
		config := testWorkspaceConfig(t, root.Name())
		config.RetainedResultDir = dir
		_, err := New(&client, config, root, agents, Skills{Items: map[string]Skill{}}, "main", nil)
		require.ErrorContains(t, err, "retained result dir", dir)
	}
}
