//nolint:exhaustruct,gocritic,wsl_v5 // Tests intentionally use sparse fixtures.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"iter"
	"os"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
)

type fakeLooper struct {
	run func(rocketcode.PromptInput) error
}

func (f fakeLooper) Loop(_ context.Context, input <-chan rocketcode.PromptInput, _ iter.Seq2[rocketcode.SessionEntry, error], _ func(rocketcode.SessionEntry) error, _ <-chan os.Signal) error {
	for prompt := range input {
		if f.run != nil {
			if err := f.run(prompt); err != nil {
				close(prompt.Responses)
				return err
			}
		}

		prompt.Responses <- rocketcode.ChatResponse{Kind: rocketcode.ChatResponseAssistantMessage, Text: "turn complete"}
		close(prompt.Responses)
	}

	return nil
}

func TestParseOptionsAcceptsPositionalGoal(t *testing.T) {
	opt, err := parseOptions([]string{"--script", "make test", "fix bug"}, strings.NewReader(""))

	require.NoError(t, err)
	require.Equal(t, "fix bug", opt.goal)
	require.Equal(t, "make test", opt.script)
}

func TestParseOptionsAcceptsStdinGoal(t *testing.T) {
	opt, err := parseOptions([]string{"--max-loops", "3"}, strings.NewReader("fix from stdin\n"))

	require.NoError(t, err)
	require.Equal(t, "fix from stdin", opt.goal)
	require.Equal(t, 3, opt.maxLoops)
}

func TestParseOptionsRejectsPositionalAndStdin(t *testing.T) {
	_, err := parseOptions([]string{"fix bug"}, strings.NewReader("fix from stdin"))

	require.EqualError(t, err, "provide goal either as positional arguments or stdin, not both")
}

func TestPromptInputSupportsAttachmentsButNotDeveloperPrefix(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.WriteFile("image.png", []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 0o644))

	prompt, err := promptInput("developer: inspect @attach:image.png", root, dir)

	require.NoError(t, err)
	require.Equal(t, rocketcode.PromptInputRoleUser, prompt.Role)
	require.Equal(t, "developer: inspect", prompt.Text)
	require.Len(t, prompt.Attachments, 1)
	require.Equal(t, "image/png", prompt.Attachments[0].MIME)
}

func TestGoalToolRecordsSummaryAndEvidence(t *testing.T) {
	recorder := &claimRecorder{}
	tool := newGoalTool(recorder)

	result, err := tool.Call(context.Background(), json.RawMessage(`{"summary":"done","evidence":"tests pass"}`), nil)

	require.NoError(t, err)
	require.Contains(t, result.Output, "recorded")
	require.Equal(t, &goalClaim{Summary: "done", Evidence: "tests pass"}, recorder.latest())
}

func TestCriticToolRecordsVerdict(t *testing.T) {
	recorder := &verdictRecorder{}
	tool := newCriticTool(recorder)

	_, err := tool.Call(context.Background(), json.RawMessage(`{"approved":false,"feedback":"missing tests"}`), nil)

	require.NoError(t, err)
	require.Equal(t, &criticVerdict{Approved: false, Feedback: "missing tests"}, recorder.latest())
}

func TestRunAutonomousLoopSucceedsAfterCriticApproval(t *testing.T) {
	deps, cleanup := testDeps(t)
	defer cleanup()

	deps.mainLooper = fakeLooper{run: func(rocketcode.PromptInput) error {
		deps.claims.set(&goalClaim{Summary: "done", Evidence: "checked"})
		return nil
	}}
	deps.criticLooper = fakeLooper{run: func(rocketcode.PromptInput) error {
		deps.verdicts.set(&criticVerdict{Approved: true, Feedback: ""})
		return nil
	}}

	var out strings.Builder
	err := runAutonomousLoop(context.Background(), options{goal: "finish task"}, deps, &eventWriter{w: &out})

	require.NoError(t, err)
	events := decodeEvents(t, out.String())
	require.Equal(t, "goal_achieved", events[1].Type)
	require.Equal(t, "critic_verdict", events[3].Type)
	require.Equal(t, "loop_result", events[4].Type)
	require.True(t, events[4].Succeeded)
}

func TestRunAutonomousLoopFeedsScriptFailureBackAsDeveloperPrompt(t *testing.T) {
	deps, cleanup := testDeps(t)
	defer cleanup()

	var prompts []rocketcode.PromptInput
	deps.mainLooper = fakeLooper{run: func(prompt rocketcode.PromptInput) error {
		prompts = append(prompts, prompt)
		deps.claims.set(&goalClaim{Summary: "done", Evidence: "checked"})
		return nil
	}}
	deps.criticLooper = fakeLooper{run: func(rocketcode.PromptInput) error {
		deps.verdicts.set(&criticVerdict{Approved: true, Feedback: ""})
		return nil
	}}

	scriptCalls := 0
	deps.runScript = func(context.Context, string, string, int64) (scriptResult, error) {
		scriptCalls++
		if scriptCalls == 1 {
			return scriptResult{Command: "verify", ExitCode: 2, Stdout: "out", Stderr: "err"}, nil
		}

		return scriptResult{Command: "verify", ExitCode: 0}, nil
	}

	var out strings.Builder
	err := runAutonomousLoop(context.Background(), options{goal: "finish task", script: "verify", maxLoops: 2}, deps, &eventWriter{w: &out})

	require.NoError(t, err)
	require.Len(t, prompts, 2)
	require.Equal(t, rocketcode.PromptInputRoleDeveloper, prompts[1].Role)
	require.Contains(t, prompts[1].Text, "Exit code: 2")
	require.Contains(t, prompts[1].Text, "Stdout:\nout")
	require.Contains(t, prompts[1].Text, "Stderr:\nerr")
}

func TestRunAutonomousLoopReturnsErrorAfterMaxLoops(t *testing.T) {
	deps, cleanup := testDeps(t)
	defer cleanup()

	deps.mainLooper = fakeLooper{}
	deps.criticLooper = fakeLooper{}

	var out strings.Builder
	err := runAutonomousLoop(context.Background(), options{goal: "finish task", maxLoops: 1}, deps, &eventWriter{w: &out})

	require.EqualError(t, err, "max loops exhausted")
	events := decodeEvents(t, out.String())
	require.Equal(t, "loop_result", events[len(events)-1].Type)
	require.False(t, events[len(events)-1].Succeeded)
}

func TestLimitOutputKeepsTailWhenConfigured(t *testing.T) {
	got := limitOutput("0123456789", 4)

	require.Equal(t, "[truncated to last 4 bytes]\n6789", got)
}

func testDeps(t *testing.T) (runtimeDeps, func()) {
	t.Helper()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)

	return runtimeDeps{
		root:       root,
		cwd:        dir,
		interrupts: make(chan os.Signal),
		runScript: func(context.Context, string, string, int64) (scriptResult, error) {
			return scriptResult{ExitCode: 0}, nil
		},
		claims:   &claimRecorder{},
		verdicts: &verdictRecorder{},
	}, func() { require.NoError(t, root.Close()) }
}

func decodeEvents(t *testing.T, text string) []jsonlEvent {
	t.Helper()

	scanner := bufio.NewScanner(strings.NewReader(text))
	events := []jsonlEvent{}
	for scanner.Scan() {
		var event jsonlEvent
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &event))
		events = append(events, event)
	}

	require.NoError(t, scanner.Err())
	return events
}
