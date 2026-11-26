package protocol

import (
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type GoalRequest struct {
	Objective string
	MaxTurns  int
	Check     string
	Force     bool
}

const GoalCommandHelp = `/goal <objective>

Parameters:
maxTurns: omitted (default infinite); positive integer; 0, -1, or infinite
check: a bash command that must exit 0 before the goal can end as complete or blocked; quote it when it has spaces
force: yes, true, on or 1 leaves the agent no way to end the goal as blocked; no, false, off or 0 (default) keeps it

Examples:
/goal ship the release
/goal maxTurns: 10 ship the release
/goal maxTurns:10 ship the release
/goal maxTurns: infinite ship the release
/goal check: ./scripts/check.sh ship the release
/goal check: "make lint && make test" force: yes fix every lint and test failure

/goal stop ends the goal and lets the running turn finish.
/goal resume restarts a stopped, blocked or out-of-turns goal with a fresh turn count.`

func ParseGoalRequest(text string) (goal GoalRequest, rejection string) {
	text = strings.TrimSpace(text)

	if text == "" {
		return GoalRequest{}, GoalCommandHelp
	}

	for text != "" {
		if after, ok := strings.CutPrefix(text, "maxTurns:"); ok {
			fields := strings.Fields(after)
			if len(fields) == 0 {
				return GoalRequest{}, "`maxTurns:` needs a value like `20`, `0`, `-1`, or `infinite`."
			}

			value := strings.ToLower(fields[0])
			switch value {
			case "infinite":
				goal.MaxTurns = 0
			default:
				parsed, err := strconv.Atoi(value)
				if err != nil || parsed < -1 {
					return GoalRequest{}, "`maxTurns:` must be a positive integer, `0`, `-1`, or `infinite`."
				}

				goal.MaxTurns = max(parsed, 0)
			}

			text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(after), fields[0]))
		} else if after, ok := strings.CutPrefix(text, "check:"); ok {
			after = strings.TrimSpace(after)
			if goal.Check, text = goalCheck(after); goal.Check == "" {
				return GoalRequest{}, "`check:` needs a bash command like `./scripts/check.sh` or `\"make lint && make test\"`, quoted when it has spaces."
			}
		} else if after, ok := strings.CutPrefix(text, "force:"); ok {
			fields := strings.Fields(after)
			if len(fields) == 0 {
				return GoalRequest{}, "`force:` needs a value like `yes` or `no`."
			}

			switch strings.ToLower(fields[0]) {
			case "true", "1", "on", "yes":
				goal.Force = true
			case "false", "0", "off", "no":
				goal.Force = false
			default:
				return GoalRequest{}, "`force:` must be `yes`, `true`, `on`, `1`, `no`, `false`, `off`, or `0`."
			}

			text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(after), fields[0]))
		} else {
			goal.Objective = text
			return goal, ""
		}
	}

	return GoalRequest{}, "Tell me the goal after the parameters, for example `/goal maxTurns: 5 update the docs`."
}

func goalCheck(text string) (command, rest string) {
	for word, err := range syntax.NewParser().WordsSeq(strings.NewReader(text)) {
		if err != nil {
			return "", text
		}

		command = text[word.Pos().Offset():word.End().Offset()]

		if len(word.Parts) == 1 {
			switch part := word.Parts[0].(type) {
			case *syntax.SglQuoted:
				if !part.Dollar {
					command = part.Value
				}
			case *syntax.DblQuoted:
				if !part.Dollar {
					command = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\$`, `$`, "\\`", "`", "\\\n", "").Replace(command[1 : len(command)-1])
				}
			}
		}

		return strings.TrimSpace(command), strings.TrimSpace(text[word.End().Offset():])
	}

	return "", text
}
