package quickbench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/openai/openai-go/v3/shared"
)

const (
	eloStart = 1000.0
	eloK     = 32.0
)

// EloRating is one ladder row.
type EloRating struct {
	Label  string  `json:"label"`
	Rating float64 `json:"rating"`
}

// PairResult is one pairwise judgment.
type PairResult struct {
	A      string `json:"a"`
	B      string `json:"b"`
	Winner string `json:"winner"` // A, B, or TIE
	Raw    string `json:"raw,omitempty"`
	Error  string `json:"error,omitempty"`
}

type judgePairFunc func(ctx context.Context, criteria, aLabel, aText, bLabel, bText string) (winner string, raw string, err error)

func rankCells(ctx context.Context, providers rocketcode.Providers, judge modelSelector, criteria string, cells []CellResult, inject judgePairFunc) ([]EloRating, []PairResult, error) {
	if strings.TrimSpace(criteria) == "" {
		return nil, nil, errors.New("missing elo criteria")
	}

	playable := make([]CellResult, 0, len(cells))
	for _, c := range cells {
		if c.Error == "" {
			playable = append(playable, c)
		}
	}

	if len(playable) < 2 {
		return nil, nil, errors.New("need at least two successful cells for ELO")
	}

	if inject == nil {
		inject = func(ctx context.Context, criteria, aLabel, aText, bLabel, bText string) (string, string, error) {
			return defaultJudge(ctx, providers, judge, criteria, aLabel, aText, bLabel, bText)
		}
	}

	ratings := map[string]float64{}
	for _, c := range playable {
		ratings[c.Label] = eloStart
	}

	var pairs []PairResult

	for i := range len(playable) {
		for j := i + 1; j < len(playable); j++ {
			a, b := playable[i], playable[j]

			left, right := a, b
			if rand.IntN(2) == 1 {
				left, right = b, a
			}

			winner, raw, err := inject(ctx, criteria, left.Label, left.Text, right.Label, right.Text)

			pair := PairResult{A: left.Label, B: right.Label, Raw: raw}
			if err != nil {
				pair.Error = err.Error()
				pairs = append(pairs, pair)

				continue
			}

			winner = strings.ToUpper(strings.TrimSpace(winner))
			switch winner {
			case "A", "B", "TIE":
				pair.Winner = winner
			default:
				pair.Error = fmt.Sprintf("unparseable winner %q", winner)
				pairs = append(pairs, pair)

				continue
			}

			pairs = append(pairs, pair)
			applyElo(ratings, left.Label, right.Label, pair.Winner)
		}
	}

	ladder := make([]EloRating, 0, len(ratings))
	for label, rating := range ratings {
		ladder = append(ladder, EloRating{Label: label, Rating: rating})
	}

	slices.SortFunc(ladder, func(a, b EloRating) int {
		if a.Rating == b.Rating {
			return strings.Compare(a.Label, b.Label)
		}

		if a.Rating > b.Rating {
			return -1
		}

		return 1
	})

	return ladder, pairs, nil
}

func applyElo(ratings map[string]float64, a, b, winner string) {
	ra, rb := ratings[a], ratings[b]
	ea := 1.0 / (1.0 + math.Pow(10, (rb-ra)/400.0))
	eb := 1.0 - ea

	var sa, sb float64

	switch winner {
	case "A":
		sa, sb = 1, 0
	case "B":
		sa, sb = 0, 1
	default:
		sa, sb = 0.5, 0.5
	}

	ratings[a] = ra + eloK*(sa-ea)
	ratings[b] = rb + eloK*(sb-eb)
}

func defaultJudge(ctx context.Context, providers rocketcode.Providers, judge modelSelector, criteria, aLabel, aText, bLabel, bText string) (string, string, error) {
	prompt := fmt.Sprintf(`You are ranking two model outputs.

Criteria:
%s

Output A (%s):
%s

Output B (%s):
%s

Reply with exactly one first line:
WINNER: A
or
WINNER: B
or
WINNER: TIE
Then a short reason.
`, criteria, aLabel, aText, bLabel, bText)

	tmpDir, err := scratchDir("judge-*")
	if err != nil {
		return "", "", err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	root, err := os.OpenRoot(tmpDir)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = root.Close() }()

	agentModel := shared.ResponsesModel(judge.Model)
	permission := rocketcode.PermissionSet{}
	agent := rocketcode.Agent{Name: "judge", Model: agentModel, Prompt: "You are a strict pairwise judge.", Verbosity: judge.Verbosity, Permission: permission}
	config := rocketcode.Config{
		Model:           agentModel,
		ReasoningEffort: shared.ReasoningEffort(judge.ReasoningEffort),
		Diagnostics:     true,
		ChildRunLogger:  rocketcode.DiscardChildRunLog,
		CheckpointSink:  rocketcode.InertCheckpointSink{},
		ShellCommand:    rocketcode.DefaultShellCommand,
	}

	runtime, err := rocketcode.NewWithProviders(providers, &config, root, rocketcode.Agents{Items: map[string]rocketcode.Agent{"judge": agent}}, rocketcode.Skills{Items: map[string]rocketcode.Skill{}}, "judge", io.Discard)
	if err != nil {
		return "", "", err
	}

	input := make(chan rocketcode.PromptInput, 1)

	output := make(chan rocketcode.ChatResponse, 64)
	input <- rocketcode.PromptInput{Text: prompt, Responses: output}

	close(input)

	errCh := make(chan error, 1)
	go func() {
		errCh <- runtime.Loop(ctx, input, func(func(rocketcode.SessionEntry, error) bool) {}, func(rocketcode.SessionEntry) error { return nil }, make(chan os.Signal, 1))
	}()

	var text strings.Builder

	for item := range output {
		if item.Kind == rocketcode.ChatResponseAssistantMessage {
			text.WriteString(item.Text)
		}
	}

	if err := <-errCh; err != nil {
		return "", text.String(), err
	}

	raw := text.String()
	winner, err := parseWinner(raw)

	return winner, raw, err
}

func parseWinner(raw string) (string, error) {
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		upper := strings.ToUpper(line)
		if after, ok := strings.CutPrefix(upper, "WINNER:"); ok {
			switch strings.TrimSpace(after) {
			case "A", "B", "TIE":
				return strings.TrimSpace(after), nil
			}

			return "", fmt.Errorf("invalid WINNER line %q", line)
		}

		switch upper {
		case "A", "B", "TIE":
			return upper, nil
		}

		break
	}

	return "", errors.New("no WINNER line in judge output")
}
