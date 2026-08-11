package quickbench

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.txt"), []byte("name: x\nroot: main\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "main.md"), defaultMainAgentMarkdown("gpt-5.4", "x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "variations", "a"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "variations", "a", "transcript.json"), []byte(`[{"role":"user","text":"hi"}]`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "elo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "elo", "judge.txt"), []byte("gpt-5.4\n"), 0o644))

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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.txt"), []byte("name: x\nroot: main\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "main.md"), defaultMainAgentMarkdown("gpt-5.4", "x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "elo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "elo", "criteria.txt"), []byte("prefer short\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "elo", "judge.txt"), []byte("gpt-5.4\n"), 0o644))
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
	assert.Contains(t, out, "variations/alpha/transcript.json")
	assert.Contains(t, out, "elo/judge.txt")
	assert.Contains(t, out, "gpt-5.4")

	buf.Reset()
	require.NoError(t, Dump(&buf, bar, true))
	names := buf.String()
	assert.Contains(t, names, "meta.txt")
	assert.NotContains(t, names, "prefer concise")
}

func TestParseWinner(t *testing.T) {
	w, err := parseWinner("WINNER: A\nbecause")
	require.NoError(t, err)
	assert.Equal(t, "A", w)
	w, err = parseWinner("winner: tie\n")
	require.NoError(t, err)
	assert.Equal(t, "TIE", w)

	_, err = parseWinner("nope")
	require.Error(t, err)
}

func TestEloRankingDeterministic(t *testing.T) {
	cells := []CellResult{
		{Label: "a@m", Text: "best"},
		{Label: "b@m", Text: "mid"},
		{Label: "c@m", Text: "worst"},
	}
	score := map[string]int{"best": 3, "mid": 2, "worst": 1}
	judge := func(_ context.Context, _, _, aText, _, bText string) (string, string, error) {
		if score[aText] > score[bText] {
			return "A", "WINNER: A", nil
		}

		if score[aText] < score[bText] {
			return "B", "WINNER: B", nil
		}

		return "TIE", "WINNER: TIE", nil
	}
	ladder, pairs, err := rankCells(t.Context(), rocketcode.Providers{}, modelSelector{Raw: "gpt", Model: "gpt"}, "criteria", cells, judge)
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
	judge := func(_ context.Context, _, _, _, _, _ string) (string, string, error) {
		return "TIE", "WINNER: TIE", nil
	}
	ladder, _, err := rankCells(t.Context(), rocketcode.Providers{}, modelSelector{Raw: "g", Model: "g"}, "c", cells, judge)
	require.NoError(t, err)
	require.Len(t, ladder, 2)
	assert.InDelta(t, ladder[0].Rating, ladder[1].Rating, 0.001)
	assert.InDelta(t, 1000.0, ladder[0].Rating, 0.001)
}

func TestRunMatrixDimensions(t *testing.T) {
	dir := writeFixtureBAR(t)
	bar, err := Open(dir)
	require.NoError(t, err)

	models := []modelSelector{{Raw: "m1", Model: "m1"}, {Raw: "m2", Model: "m2"}}
	run := func(_ context.Context, _ rocketcode.Providers, _ *BAR, v Variation, root *modelSelector, _ map[string]modelSelector, _ time.Duration) CellResult {
		raw := root.Raw
		return CellResult{Label: v.ID + "@" + raw, Variation: v.ID, Model: raw, Text: v.ID + raw}
	}
	judge := func(_ context.Context, _, _, aText, _, bText string) (string, string, error) {
		if aText < bText {
			return "A", "WINNER: A", nil
		}

		if aText > bText {
			return "B", "WINNER: B", nil
		}

		return "TIE", "WINNER: TIE", nil
	}
	report, err := runBARWith(t.Context(), rocketcode.Providers{}, bar, runOptions{rootModels: models}, run, judge)
	require.NoError(t, err)
	assert.Len(t, report.Cells, 4)
	assert.Len(t, report.Ladder, 4)
}

func TestRunSingleCellSkipsELO(t *testing.T) {
	dir := writeFixtureBAR(t)
	// one variation only
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "variations", "beta")))
	bar, err := Open(dir)
	require.NoError(t, err)

	run := func(_ context.Context, _ rocketcode.Providers, _ *BAR, v Variation, root *modelSelector, _ map[string]modelSelector, _ time.Duration) CellResult {
		return CellResult{Label: v.ID + "@" + root.Raw, Variation: v.ID, Model: root.Raw, Text: "only"}
	}
	report, err := runBARWith(t.Context(), rocketcode.Providers{}, bar, runOptions{rootModels: []modelSelector{{Raw: "m", Model: "m"}}}, run, nil)
	require.NoError(t, err)
	require.Len(t, report.Cells, 1)
	assert.NotEmpty(t, report.Skipped)
	assert.Empty(t, report.Ladder)
}

func TestPackRejectsTxtarHeaderInBody(t *testing.T) {
	dir := writeFixtureBAR(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "elo", "criteria.txt"), []byte("prefer short\n-- before --\nmore\n"), 0o644))
	err := Pack(dir, filepath.Join(t.TempDir(), "x.bar"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "txtar header")
}

func TestOpenRejectsMissingJudge(t *testing.T) {
	dir := writeFixtureBAR(t)
	require.NoError(t, os.Remove(filepath.Join(dir, "elo", "judge.txt")))
	_, err := Open(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "judge")
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
	judge := func(_ context.Context, _, _, aText, _, bText string) (string, string, error) {
		if score[aText] > score[bText] {
			return "A", "WINNER: A", nil
		}

		return "B", "WINNER: B", nil
	}
	ladder, pairs, err := rankCells(t.Context(), rocketcode.Providers{}, modelSelector{Raw: "g", Model: "g"}, "c", cells, judge)
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

	run := func(_ context.Context, _ rocketcode.Providers, _ *BAR, v Variation, root *modelSelector, _ map[string]modelSelector, timeout time.Duration) CellResult {
		saw = timeout
		return CellResult{Label: v.ID + "@" + root.Raw, Variation: v.ID, Model: root.Raw, Text: "t"}
	}
	judge := func(_ context.Context, _, _, _, _, _ string) (string, string, error) {
		return "TIE", "WINNER: TIE", nil
	}
	_, err = runBARWith(t.Context(), rocketcode.Providers{}, bar, runOptions{
		rootModels: []modelSelector{{Raw: "m", Model: "m"}},
		timeout:    3 * time.Second,
		timeoutOK:  true,
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
	require.Len(t, again.Tools, 1)
	assert.Equal(t, "echo", again.Tools[0].Name)
	assert.Equal(t, "pong", again.Tools[0].Response)
	require.NotEmpty(t, again.Variations[0].System)
}

func TestRunSkipsStubCriteria(t *testing.T) {
	dir := writeFixtureBAR(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "elo", "criteria.txt"), []byte(stubCriteria), 0o644))
	bar, err := Open(dir)
	require.NoError(t, err)

	run := func(_ context.Context, _ rocketcode.Providers, _ *BAR, v Variation, root *modelSelector, _ map[string]modelSelector, _ time.Duration) CellResult {
		return CellResult{Label: v.ID + "@" + root.Raw, Variation: v.ID, Model: root.Raw, Text: "x"}
	}
	report, err := runBARWith(t.Context(), rocketcode.Providers{}, bar, runOptions{rootModels: []modelSelector{{Raw: "m", Model: "m"}}}, run, nil)
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
	agents, root, err := buildAgents(bar, bar.Variations[0], nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "main", root)
	assert.Equal(t, "gpt-worker-v2", agents.Items["worker"].Model)
	assert.Equal(t, "overlay prompt", agents.Items["worker"].Prompt)
	assert.Equal(t, "gpt-5.4", agents.Items["main"].Model)
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
}

func writeFixtureBAR(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.txt"), []byte("name: fixture\ndescription: test\ntags: a, b\nroot: main\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "main.md"), defaultMainAgentMarkdown("gpt-5.4", "fixture root"), 0o644))

	for _, id := range []string{"alpha", "beta"} {
		base := filepath.Join(dir, "variations", id)
		require.NoError(t, os.MkdirAll(base, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(base, "system.txt"), []byte("sys-"+id+"\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(base, "transcript.json"), []byte(`[{"role":"user","text":"hello `+id+`"}]`+"\n"), 0o644))
	}

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "elo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "elo", "criteria.txt"), []byte("prefer concise answers\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "elo", "judge.txt"), []byte("gpt-5.4\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "mocks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mocks", "tools.json"), []byte(`[{"name":"echo","description":"echo","parameters":{"type":"object"},"response":"pong"}]`+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("ignore\n"), 0o644))

	return dir
}
