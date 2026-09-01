package protocol

import (
	"errors"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// GoalRequest is one parsed text-connector goal start request.
type GoalRequest struct {
	Objective, CheckScript string
	MaxTurns               int
}

// GoalCommandHelp is the bare `$goal` usage text (parameters and examples).
const GoalCommandHelp = `$goal <objective>

Parameters:
maxTurns: omitted (default 5); positive integer; 0, -1, or infinite
checkScript: workspace-local simple command (quote when it has spaces)

Examples:
$goal ship the release
$goal maxTurns: 10 ship the release
$goal maxTurns:10 ship the release
$goal maxTurns: infinite ship the release
$goal checkScript: ./scripts/check.sh ship the release
$goal checkScript:"./scripts/check.sh --full" ship the release
$goal maxTurns: 10 checkScript: ./scripts/check.sh ship the release`

// ParseGoalRequest parses canonical $goal arguments.
func ParseGoalRequest(text string) (goal GoalRequest, rejection string) {
	text = strings.TrimSpace(text)
	maxTurns := 5
	checkScript := ""

	if text == "" {
		return GoalRequest{}, GoalCommandHelp
	}

	for {
		text = strings.TrimSpace(text)
		if text == "" {
			return GoalRequest{}, "Tell me the goal after the parameters, for example `$goal maxTurns: 5 update the docs`."
		}

		if after, ok := strings.CutPrefix(text, "maxTurns:"); ok {
			fields := strings.Fields(after)
			if len(fields) == 0 {
				return GoalRequest{}, "`maxTurns:` needs a value like `20`, `0`, `-1`, or `infinite`."
			}

			value := strings.ToLower(fields[0])
			switch value {
			case "infinite":
				maxTurns = 0
			default:
				parsed, err := strconv.Atoi(value)
				if err != nil || parsed < -1 {
					return GoalRequest{}, "`maxTurns:` must be a positive integer, `0`, `-1`, or `infinite`."
				}

				maxTurns = max(parsed, 0)
			}

			text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(after), fields[0]))
		} else if after, ok := strings.CutPrefix(text, "checkScript:"); ok {
			value, rest, err := consumeGoalCheckScriptValue(strings.TrimSpace(after))
			if err != nil {
				return GoalRequest{}, err.Error()
			}

			checkScript = value
			text = rest
		} else {
			objective := strings.TrimSpace(text)
			if objective == "" {
				return GoalRequest{}, "Tell me the goal after the parameters, for example `$goal maxTurns: 5 update the docs`."
			}

			return GoalRequest{Objective: objective, CheckScript: checkScript, MaxTurns: maxTurns}, ""
		}
	}
}

func consumeGoalCheckScriptValue(text string) (value, rest string, err error) {
	if text == "" {
		return "", "", errors.New("`checkScript:` needs a value like `./scripts/check.sh` or `\"./scripts/check.sh --linter-mode\"`")
	}

	parser := syntax.NewParser()
	for word, err := range parser.WordsSeq(strings.NewReader(text)) {
		if err != nil {
			return "", "", errors.New("`checkScript:` has malformed shell quoting")
		}

		value, err := StaticGoalCheckWord(word)
		if err != nil {
			return "", "", err
		}

		if strings.TrimSpace(value) == "" {
			return "", "", errors.New("`checkScript:` needs a non-empty value")
		}

		return value, strings.TrimSpace(text[word.End().Offset():]), nil
	}

	return "", "", errors.New("`checkScript:` needs a non-empty value")
}

// StaticGoalCheckWord returns the literal text of a shell word with no expansions.
func StaticGoalCheckWord(word *syntax.Word) (string, error) {
	var value strings.Builder

	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			value.WriteString(part.Value)
		case *syntax.SglQuoted:
			if part.Dollar {
				return "", errors.New("goal check script arguments must be static literal strings")
			}

			value.WriteString(part.Value)
		case *syntax.DblQuoted:
			if part.Dollar {
				return "", errors.New("goal check script arguments must be static literal strings")
			}

			for _, quoted := range part.Parts {
				lit, ok := quoted.(*syntax.Lit)
				if !ok {
					return "", errors.New("goal check script arguments must be static literal strings")
				}

				value.WriteString(lit.Value)
			}
		default:
			return "", errors.New("goal check script arguments must be static literal strings")
		}
	}

	return value.String(), nil
}
