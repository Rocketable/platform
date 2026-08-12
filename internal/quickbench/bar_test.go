package quickbench

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPackUnpackRoundTrip(t *testing.T) {
	dir := writeFixtureBAR(t)
	outBar := filepath.Join(t.TempDir(), "round.bar")
	require.NoError(t, Pack(dir, outBar))
	outDir := filepath.Join(t.TempDir(), "unpacked")
	require.NoError(t, Unpack(outBar, outDir))

	orig, err := Open(dir)
	require.NoError(t, err)
	again, err := Open(outDir)
	require.NoError(t, err)
	assert.Equal(t, orig.Meta, again.Meta)
	assert.Equal(t, orig.Criteria, again.Criteria)
	assert.Equal(t, orig.Judge, again.Judge)
	require.Len(t, again.Variations, 2)
	assert.Equal(t, orig.Variations[0].Transcript, again.Variations[0].Transcript)
}

func TestOpenBarAndDir(t *testing.T) {
	dir := writeFixtureBAR(t)
	barPath := filepath.Join(t.TempDir(), "f.bar")
	require.NoError(t, Pack(dir, barPath))

	fromDir, err := Open(dir)
	require.NoError(t, err)
	fromBar, err := Open(barPath)
	require.NoError(t, err)
	assert.Equal(t, fromDir.Meta.Name, fromBar.Meta.Name)
	assert.Len(t, fromDir.Variations, 2)
	assert.Len(t, fromBar.Variations, 2)
}

func TestOpenRejectsMissingCriteriaAndYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bench.yaml"), []byte("name: x\nroot: main\nelo:\n  model: gpt-5.6-luna\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "main.md"), defaultMainAgentMarkdown("gpt-5.4", "x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "variations", "a"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "variations", "a", "turns.yaml"), []byte("turns:\n  - role: user\n    text: hi\n"), 0o644))

	_, err := Open(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "criteria")

	yamlPath := filepath.Join(t.TempDir(), "old.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte("name: x\n"), 0o644))
	_, err = Open(yamlPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "YAML")
}

func TestOpenRejectsZeroVariations(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bench.yaml"), []byte(fixtureBenchYAML("x", "prefer short")), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "main.md"), defaultMainAgentMarkdown("gpt-5.4", "x"), 0o644))
	_, err := Open(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "variation")
}

func TestDumpIncludesVariationAndJudge(t *testing.T) {
	dir := writeFixtureBAR(t)
	bar, err := Open(dir)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, Dump(&buf, bar, false))
	out := buf.String()
	assert.Contains(t, out, "variations/alpha/turns.yaml")
	assert.Contains(t, out, "bench.yaml")
	assert.Contains(t, out, "gpt-5.6-luna")
	assert.Less(t, strings.Index(out, "bench.yaml"), strings.Index(out, "agents/main.md"))
	assert.Less(t, strings.Index(out, "variations/alpha/turns.yaml"), strings.Index(out, "agents/main.md"))

	buf.Reset()
	require.NoError(t, Dump(&buf, bar, true))
	names := buf.String()
	assert.Contains(t, names, "bench.yaml")
	assert.NotContains(t, names, "prefer concise")
}

func TestParseJudgeDecision(t *testing.T) {
	w, err := parseJudgeDecision(`{"winner":"A","rationale":"crisper"}`)
	require.NoError(t, err)
	assert.Equal(t, "A", w)
	w, err = parseJudgeDecision(`{"winner":"tie","rationale":"same"}`)
	require.NoError(t, err)
	assert.Equal(t, "TIE", w)

	_, err = parseJudgeDecision("WINNER: A")
	require.Error(t, err)
}

func TestEloRankingDeterministic(t *testing.T) {
	cells := []CellResult{
		{Label: "a@m", Text: "best"},
		{Label: "b@m", Text: "mid"},
		{Label: "c@m", Text: "worst"},
	}
	score := map[string]int{"best": 3, "mid": 2, "worst": 1}
	judge := func(_ context.Context, _ string, a, b CellResult) (string, string, error) {
		if score[a.Text] > score[b.Text] {
			return "A", "WINNER: A", nil
		}

		if score[a.Text] < score[b.Text] {
			return "B", "WINNER: B", nil
		}

		return "TIE", "WINNER: TIE", nil
	}
	ladder, pairs, err := rankCells(t.Context(), "criteria", cells, judge)
	require.NoError(t, err)
	require.Len(t, pairs, 3)
	require.Len(t, ladder, 3)
	assert.Equal(t, "a@m", ladder[0].Label)
	assert.Equal(t, "c@m", ladder[2].Label)
	assert.Greater(t, ladder[0].Rating, ladder[1].Rating)
	assert.Greater(t, ladder[1].Rating, ladder[2].Rating)
}

func TestEloTie(t *testing.T) {
	cells := []CellResult{{Label: "x@m", Text: "same"}, {Label: "y@m", Text: "same"}}
	judge := func(_ context.Context, _ string, _, _ CellResult) (string, string, error) {
		return "TIE", "WINNER: TIE", nil
	}
	ladder, _, err := rankCells(t.Context(), "c", cells, judge)
	require.NoError(t, err)
	require.Len(t, ladder, 2)
	assert.InDelta(t, ladder[0].Rating, ladder[1].Rating, 0.001)
	assert.InDelta(t, 1000.0, ladder[0].Rating, 0.001)
}

func TestRunMatrixDimensions(t *testing.T) {
	dir := writeFixtureBAR(t)
	// two matrix rows × two variations = 4 cells
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bench.yaml"), []byte(`
name: fixture
root: main
matrix:
  - id: m1
    agents:
      main:
        model: gpt-5.4
  - id: m2
    agents:
      main:
        model: gpt-5.4-mini
elo:
  model: gpt-5.6-luna
  reasoningEffort: max
  criteria: |
    prefer concise answers
`), 0o644))
	bar, err := Open(dir)
	require.NoError(t, err)

	run := func(_ context.Context, _ rocketcode.Providers, _ *BAR, v Variation, entry MatrixEntry, _ time.Duration) CellResult {
		return CellResult{Label: v.ID + "@" + entry.ID, Variation: v.ID, Matrix: entry.ID, Model: entry.ID, Text: v.ID + entry.ID}
	}
	judge := func(_ context.Context, _ string, a, b CellResult) (string, string, error) {
		if a.Text < b.Text {
			return "A", "WINNER: A", nil
		}

		if a.Text > b.Text {
			return "B", "WINNER: B", nil
		}

		return "TIE", "WINNER: TIE", nil
	}
	report, err := runBARWith(t.Context(), rocketcode.Providers{}, bar, runOptions{}, run, judge)
	require.NoError(t, err)
	assert.Len(t, report.Cells, 4)
	assert.Len(t, report.Ladder, 4)
}

func TestRunSingleCellSkipsELO(t *testing.T) {
	dir := writeFixtureBAR(t)
	// one variation only, default matrix (one cell)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "variations", "beta")))
	bar, err := Open(dir)
	require.NoError(t, err)

	run := func(_ context.Context, _ rocketcode.Providers, _ *BAR, v Variation, entry MatrixEntry, _ time.Duration) CellResult {
		return CellResult{Label: cellLabel(v.ID, entry.ID), Variation: v.ID, Matrix: entry.ID, Model: entry.ID, Text: "only"}
	}
	report, err := runBARWith(t.Context(), rocketcode.Providers{}, bar, runOptions{}, run, func(context.Context, string, CellResult, CellResult) (string, string, error) {
		t.Fatal("pair judge should not run")
		return "", "", nil
	})
	require.NoError(t, err)
	require.Len(t, report.Cells, 1)
	assert.NotEmpty(t, report.Skipped)
	assert.Empty(t, report.Ladder)
}

func TestPackRejectsTxtarHeaderInBody(t *testing.T) {
	dir := writeFixtureBAR(t)
	// Header must appear as a full line in a member body (not YAML-indented).
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "main.md"), []byte("---\ndescription: x\nmodel: gpt-5.4\n---\n\nok\n-- before --\nmore\n"), 0o644))
	err := Pack(dir, filepath.Join(t.TempDir(), "x.bar"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "txtar header")
}

func TestOpenRejectsMissingJudge(t *testing.T) {
	dir := writeFixtureBAR(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bench.yaml"), []byte("name: x\nroot: main\nelo:\n  criteria: |\n    prefer short\n"), 0o644))
	_, err := Open(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "elo.model")
}

func TestValidateTranscriptRejects(t *testing.T) {
	err := validateTranscript([]Message{{Role: "assistant", Text: "hi"}})
	require.Error(t, err)
	err = validateTranscript([]Message{{Role: "system", Text: "nope"}, {Role: "user", Text: "u"}})
	require.Error(t, err)
}

func TestEloSkipsFailedCells(t *testing.T) {
	cells := []CellResult{
		{Label: "ok@m", Text: "low"},
		{Label: "bad@m", Text: "x", Error: "boom"},
		{Label: "ok2@m", Text: "high"},
	}
	score := map[string]int{"high": 2, "low": 1}
	judge := func(_ context.Context, _ string, a, b CellResult) (string, string, error) {
		if score[a.Text] > score[b.Text] {
			return "A", "WINNER: A", nil
		}

		return "B", "WINNER: B", nil
	}
	ladder, pairs, err := rankCells(t.Context(), "c", cells, judge)
	require.NoError(t, err)
	require.Len(t, ladder, 2)
	require.Len(t, pairs, 1)
	assert.Equal(t, "ok2@m", ladder[0].Label)
}

func TestTimeoutPassedToRunner(t *testing.T) {
	dir := writeFixtureBAR(t)
	bar, err := Open(dir)
	require.NoError(t, err)

	var saw time.Duration

	run := func(_ context.Context, _ rocketcode.Providers, _ *BAR, v Variation, entry MatrixEntry, timeout time.Duration) CellResult {
		saw = timeout
		return CellResult{Label: cellLabel(v.ID, entry.ID), Variation: v.ID, Matrix: entry.ID, Model: entry.ID, Text: "t"}
	}
	judge := func(_ context.Context, _ string, _, _ CellResult) (string, string, error) {
		return "TIE", "WINNER: TIE", nil
	}
	_, err = runBARWith(t.Context(), rocketcode.Providers{}, bar, runOptions{
		timeout: 3 * time.Second,
	}, run, judge)
	require.NoError(t, err)
	assert.Equal(t, 3*time.Second, saw)
}

func TestHumanReportShowsPairError(t *testing.T) {
	var buf bytes.Buffer

	err := writeHumanReport(&buf, Report{
		Path:   "p",
		Cells:  []CellResult{{Label: "a", Text: "x"}},
		Ladder: []EloRating{{Label: "a", Rating: 1000}, {Label: "b", Rating: 1000}},
		Pairs:  []PairResult{{A: "a", B: "b", Error: "judge failed"}},
	})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "error: judge failed")
}

func TestRoundTripPreservesToolsAndSystem(t *testing.T) {
	dir := writeFixtureBAR(t)
	outBar := filepath.Join(t.TempDir(), "t.bar")
	require.NoError(t, Pack(dir, outBar))
	again, err := Open(outBar)
	require.NoError(t, err)
	require.Len(t, again.Variations[0].Tools, 1)
	assert.Equal(t, "echo", again.Variations[0].Tools[0].Name)
	assert.Equal(t, "pong", again.Variations[0].Tools[0].Response)
	require.NotEmpty(t, again.Variations[0].System)
}

func TestRunSkipsStubCriteria(t *testing.T) {
	dir := writeFixtureBAR(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bench.yaml"), []byte(fixtureBenchYAML("fixture", stubCriteria)), 0o644))
	bar, err := Open(dir)
	require.NoError(t, err)

	run := func(_ context.Context, _ rocketcode.Providers, _ *BAR, v Variation, entry MatrixEntry, _ time.Duration) CellResult {
		return CellResult{Label: cellLabel(v.ID, entry.ID), Variation: v.ID, Matrix: entry.ID, Model: entry.ID, Text: "x"}
	}
	report, err := runBARWith(t.Context(), rocketcode.Providers{}, bar, runOptions{}, run, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, report.Skipped)
	assert.Empty(t, report.Ladder)
}

func TestBuildAgentsOverlays(t *testing.T) {
	dir := writeFixtureBAR(t)
	// worker agent + variation overlay
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "worker.md"), []byte("---\ndescription: w\nmodel: gpt-worker\n---\n\nworker body\n"), 0o644))
	ovDir := filepath.Join(dir, "variations", "alpha", "agents", "worker")
	require.NoError(t, os.MkdirAll(ovDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ovDir, "model.txt"), []byte("gpt-worker-v2\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ovDir, "system.txt"), []byte("overlay prompt\n"), 0o644))

	bar, err := Open(dir)
	require.NoError(t, err)
	agents, root, err := buildAgents(bar, bar.Variations[0], nil)
	require.NoError(t, err)
	assert.Equal(t, "main", root)
	assert.Equal(t, "gpt-worker-v2", agents.Items["worker"].Model)
	assert.Equal(t, "overlay prompt", agents.Items["worker"].Prompt)
	assert.Equal(t, "gpt-5.4", agents.Items["main"].Model)

	// Matrix can override model and/or system after variation overlays.
	sel, err := parseModelSelector("gpt-matrix")
	require.NoError(t, err)
	agents, _, err = buildAgents(bar, bar.Variations[0], map[string]MatrixAgent{
		"main":   {System: "matrix system"},
		"worker": {Model: sel},
	})
	require.NoError(t, err)
	assert.Equal(t, "matrix system", agents.Items["main"].Prompt)
	assert.Equal(t, "gpt-matrix", agents.Items["worker"].Model)
	assert.Equal(t, "overlay prompt", agents.Items["worker"].Prompt) // variation system kept
}

func TestConversationParts(t *testing.T) {
	prior, final, err := conversationParts(Variation{Transcript: []Message{
		{Role: "user", Text: "u1"},
		{Role: "assistant", Text: "a1"},
		{Role: "user", Text: "u2"},
	}})
	require.NoError(t, err)
	assert.Equal(t, "u2", final)
	assert.Equal(t, []Message{{Role: "user", Text: "u1"}, {Role: "assistant", Text: "a1"}}, prior)

	prior, final, err = conversationParts(Variation{Transcript: []Message{
		{Role: "user", Text: "ask"},
		{Role: "assistant", Text: "answer"},
	}})
	require.NoError(t, err)
	assert.Equal(t, "ask", final)
	assert.Empty(t, prior)
}

func fixtureBenchYAML(name, criteria string) string {
	return "name: " + name + "\n" +
		"description: test\n" +
		"tags:\n  - a\n  - b\n" +
		"root: main\n" +
		"elo:\n" +
		"  model: gpt-5.6-luna\n" +
		"  reasoningEffort: max\n" +
		"  criteria: |\n" +
		"    " + strings.TrimSpace(criteria) + "\n"
}

func writeFixtureBAR(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bench.yaml"), []byte(fixtureBenchYAML("fixture", "prefer concise answers")), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "main.md"), defaultMainAgentMarkdown("gpt-5.4", "fixture root"), 0o644))

	for _, id := range []string{"alpha", "beta"} {
		base := filepath.Join(dir, "variations", id)
		require.NoError(t, os.MkdirAll(base, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(base, "system.txt"), []byte("sys-"+id+"\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(base, "turns.yaml"), []byte("turns:\n  - role: user\n    text: hello "+id+"\ntools:\n  - name: echo\n    description: echo\n    parameters:\n      type: object\n    response: pong\n"), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("ignore\n"), 0o644))

	return dir
}
