package quickbench

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/Rocketable/platform/internal/rocketcode"
)

const bashDoublesMember = "mocks/bash.json"

// BashDouble is a recorded or hand-authored shell command double for bench re-runs.
// Prefer exact Command; Pattern is a simple prefix match ending in "*" (e.g. "gh *").
type BashDouble struct {
	Command  string `json:"command,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	Output   string `json:"output"`
	ExitCode int    `json:"exitCode,omitempty"`
}

func parseBashDoubles(data []byte) ([]BashDouble, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}

	var doubles []BashDouble
	if err := json.Unmarshal(data, &doubles); err != nil {
		return nil, fmt.Errorf("bash doubles: %w", err)
	}

	for i, d := range doubles {
		if strings.TrimSpace(d.Command) == "" && strings.TrimSpace(d.Pattern) == "" {
			return nil, fmt.Errorf("bash doubles[%d]: command or pattern required", i)
		}
	}

	return doubles, nil
}

// shellCommandFromBashDoubles returns a RocketCode ShellCommandFunc that matches
// command strings in Go against doubles and emits output via /bin/sh -c.
// Unmatched commands fail closed (stderr message, exit 127).
func shellCommandFromBashDoubles(doubles []BashDouble) rocketcode.ShellCommandFunc {
	rules := compileBashDoubleRules(doubles)

	sh, err := exec.LookPath("sh")
	if err != nil {
		sh = "/bin/sh"
	}

	return func(command string) (path string, args []string) {
		command = strings.TrimSpace(command)
		if out, code, ok := matchBashDouble(rules, command); ok {
			script := fmt.Sprintf("printf '%%s' %s; exit %d", shellQuote(out), code)
			return sh, []string{"-c", script}
		}

		script := fmt.Sprintf("printf 'quickbench: unmocked bash command: %%s\\n' %s >&2; exit 127", shellQuote(command))

		return sh, []string{"-c", script}
	}
}

type bashDoubleRule struct {
	kind     string // exact | prefix
	match    string
	output   string
	exitCode int
}

func compileBashDoubleRules(doubles []BashDouble) []bashDoubleRule {
	var rules []bashDoubleRule

	for _, d := range doubles {
		if c := strings.TrimSpace(d.Command); c != "" {
			rules = append(rules, bashDoubleRule{kind: "exact", match: c, output: d.Output, exitCode: d.ExitCode})
			continue
		}

		p := strings.TrimSpace(d.Pattern)
		if p == "" {
			continue
		}

		if before, ok := strings.CutSuffix(p, "*"); ok {
			rules = append(rules, bashDoubleRule{kind: "prefix", match: before, output: d.Output, exitCode: d.ExitCode})
		} else {
			rules = append(rules, bashDoubleRule{kind: "exact", match: p, output: d.Output, exitCode: d.ExitCode})
		}
	}

	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].kind != rules[j].kind {
			return rules[i].kind == "exact"
		}

		return len(rules[i].match) > len(rules[j].match)
	})

	return rules
}

func matchBashDouble(rules []bashDoubleRule, command string) (output string, exitCode int, ok bool) {
	for _, r := range rules {
		switch r.kind {
		case "exact":
			if command == r.match {
				return r.output, r.exitCode, true
			}
		case "prefix":
			if strings.HasPrefix(command, r.match) {
				return r.output, r.exitCode, true
			}
		}
	}

	return "", 0, false
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func extractBashDoubles(entries []observedBash) []BashDouble {
	byCmd := map[string]BashDouble{}

	var order []string

	for _, e := range entries {
		cmd := strings.TrimSpace(e.command)
		if cmd == "" {
			continue
		}

		if _, ok := byCmd[cmd]; !ok {
			order = append(order, cmd)
		}

		byCmd[cmd] = BashDouble{Command: cmd, Output: e.output, ExitCode: e.exitCode}
	}

	out := make([]BashDouble, 0, len(order))
	for _, cmd := range order {
		out = append(out, byCmd[cmd])
	}

	return out
}

type observedBash struct {
	command  string
	output   string
	exitCode int
}

func parseShellCommandFromToolArgs(raw string) string {
	var params struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return ""
	}

	return strings.TrimSpace(params.Command)
}

func parseShellCommandsFromExecuteArgs(raw string) []string {
	var params struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil
	}

	code := params.Code
	if code == "" {
		return nil
	}

	var cmds []string

	for _, quote := range []byte{'"', '\''} {
		marker := "shell(command=" + string(quote)

		rest := code
		for {
			i := strings.Index(rest, marker)
			if i < 0 {
				break
			}

			rest = rest[i+len(marker):]

			j := strings.IndexByte(rest, quote)
			if j < 0 {
				break
			}

			cmd := rest[:j]
			cmd = strings.ReplaceAll(cmd, `\\`, `\`)
			cmd = strings.ReplaceAll(cmd, `\"`, `"`)

			cmd = strings.ReplaceAll(cmd, `\'`, `'`)
			if strings.TrimSpace(cmd) != "" {
				cmds = append(cmds, cmd)
			}

			rest = rest[j+1:]
		}
	}

	return cmds
}
