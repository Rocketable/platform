package protocol

import (
	"strconv"
	"strings"
)

// GoalRequest is one parsed text-connector goal start request.
type GoalRequest struct {
	Objective string
	MaxTurns  int
}

// GoalCommandHelp is the bare `$goal` usage text (parameters and examples).
const GoalCommandHelp = `$goal <objective>

Parameters:
maxTurns: omitted (default 5); positive integer; 0, -1, or infinite

Examples:
$goal ship the release
$goal maxTurns: 10 ship the release
$goal maxTurns:10 ship the release
$goal maxTurns: infinite ship the release`

// ParseGoalRequest parses canonical $goal arguments.
func ParseGoalRequest(text string) (goal GoalRequest, rejection string) {
	text = strings.TrimSpace(text)
	maxTurns := 5

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
		} else {
			return GoalRequest{Objective: text, MaxTurns: maxTurns}, ""
		}
	}
}
