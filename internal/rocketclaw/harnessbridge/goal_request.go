package harnessbridge

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

// ParseGoalRequest parses a text-connector goal trigger. The boolean reports whether the text was a goal trigger.
func ParseGoalRequest(text string) (GoalRequest, string, bool) {
	text = strings.TrimSpace(text)
	if after, ok := strings.CutPrefix(text, "🔁"); ok {
		text = after
	} else if after, ok := strings.CutPrefix(text, "🏁"); ok {
		text = after
	} else {
		return GoalRequest{}, "", false
	}

	text = strings.TrimSpace(text)
	maxTurns := 20
	checkScript := ""

	if text == "" {
		return GoalRequest{}, "Tell me the goal after `🔁`, for example `🔁 maxTurns: 20 update the docs`.", true
	}

	for {
		fields := strings.Fields(text)
		if len(fields) == 0 {
			return GoalRequest{}, "Tell me the goal after the parameters, for example `🔁 maxTurns: 20 update the docs`.", true
		}

		switch fields[0] {
		case "maxTurns:":
			if len(fields) == 1 {
				return GoalRequest{}, "`maxTurns:` needs a value like `20`, `0`, `-1`, or `infinite`.", true
			}

			value := strings.ToLower(fields[1])
			switch value {
			case "infinite":
				maxTurns = 0
			default:
				parsed, err := strconv.Atoi(value)
				if err != nil || parsed < -1 {
					return GoalRequest{}, "`maxTurns:` must be a positive integer, `0`, `-1`, or `infinite`.", true
				}

				maxTurns = max(parsed, 0)
			}

			text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(text, fields[0])), fields[1]))
		case "checkScript:":
			value, rest, err := consumeGoalCheckScriptValue(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
			if err != nil {
				return GoalRequest{}, err.Error(), true
			}

			checkScript = value
			text = rest
		default:
			objective := strings.TrimSpace(text)
			if objective == "" {
				return GoalRequest{}, "Tell me the goal after the parameters, for example `🔁 maxTurns: 20 update the docs`.", true
			}

			return GoalRequest{Objective: objective, CheckScript: checkScript, MaxTurns: maxTurns}, "", true
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

		value, err := staticGoalCheckWord(word)
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
