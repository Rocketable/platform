package quickbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
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

type judgePairFunc func(ctx context.Context, criteria string, a, b CellResult) (winner string, raw string, err error)

func rankCells(ctx context.Context, criteria string, cells []CellResult, judge judgePairFunc) ([]EloRating, []PairResult, error) {
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

			winner, raw, err := judge(ctx, criteria, left, right)

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

			ra, rb := ratings[left.Label], ratings[right.Label]
			ea := 1.0 / (1.0 + math.Pow(10, (rb-ra)/400.0))
			eb := 1.0 - ea

			var sa, sb float64

			switch pair.Winner {
			case "A":
				sa, sb = 1, 0
			case "B":
				sa, sb = 0, 1
			default:
				sa, sb = 0.5, 0.5
			}

			ratings[left.Label] = ra + eloK*(sa-ea)
			ratings[right.Label] = rb + eloK*(sb-eb)
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

type eloJudgeDecision struct {
	Winner    string `json:"winner"`
	Rationale string `json:"rationale"`
}

func parseJudgeDecision(raw string) (string, error) {
	var decision eloJudgeDecision
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &decision); err != nil {
		return "", fmt.Errorf("parse judge JSON: %w", err)
	}

	winner := strings.ToUpper(strings.TrimSpace(decision.Winner))
	switch winner {
	case "A", "B", "TIE":
		return winner, nil
	default:
		return "", fmt.Errorf("invalid winner %q", decision.Winner)
	}
}
