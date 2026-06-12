package harnessbridge

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketcode"
	"mvdan.cc/sh/v3/syntax"
)

const goalCheckTimeout = 2 * 60 * 1000

type goalCheckCommand struct {
	command string
	subject string
}

// ValidateGoalCheckScriptStart validates a goal check script before goal persistence.
func ValidateGoalCheckScriptStart(cfg *config.Config, agentName, script string) error {
	root, err := os.OpenRoot(cfg.Workspace)
	if err != nil {
		return fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	agents, _, err := loadRocketCodeDefinitionsIn(root, cfg.Workspace, cfg.WorkDirName(), toolModePersistent)
	if err != nil {
		return err
	}

	agent, ok := agents.Items[strings.TrimSpace(agentName)]
	if !ok {
		return fmt.Errorf("goal check script agent %q is not configured", strings.TrimSpace(agentName))
	}

	_, err = validateGoalCheckScript(root, cfg.Workspace, script, agent.Permission)

	return err
}

func validateGoalCheckScript(root *os.Root, workspace, script string, permission rocketcode.PermissionSet) (goalCheckCommand, error) {
	command, subject, err := parseGoalCheckCommand(script)
	if err != nil {
		return goalCheckCommand{}, err
	}

	if err := requireWorkspaceExecutable(root, workspace, command.argv[0]); err != nil {
		return goalCheckCommand{}, err
	}

	action, matched := permission.Evaluate("bash", subject)
	if !matched || action != rocketcode.PermissionAllow {
		return goalCheckCommand{}, fmt.Errorf("goal check script is not allowed by agent bash permission: %s", subject)
	}

	return goalCheckCommand{command: command.text, subject: subject}, nil
}

type parsedGoalCheckCommand struct {
	text string
	argv []string
}

func parseGoalCheckCommand(script string) (parsedGoalCheckCommand, string, error) {
	script = strings.TrimSpace(script)
	if script == "" {
		return parsedGoalCheckCommand{}, "", errors.New("goal check script is required")
	}

	parser := syntax.NewParser()

	file, err := parser.Parse(strings.NewReader(script), "")
	if err != nil {
		return parsedGoalCheckCommand{}, "", fmt.Errorf("parse goal check script: %w", err)
	}

	if len(file.Stmts) != 1 {
		return parsedGoalCheckCommand{}, "", errors.New("goal check script must be exactly one command")
	}

	stmt := file.Stmts[0]
	if stmt.Negated || stmt.Background || stmt.Coprocess || stmt.Disown || stmt.Semicolon.IsValid() || len(stmt.Redirs) > 0 {
		return parsedGoalCheckCommand{}, "", errors.New("goal check script must be one simple command without control operators or redirects")
	}

	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok {
		return parsedGoalCheckCommand{}, "", errors.New("goal check script must be one simple command")
	}

	if len(call.Assigns) > 0 {
		return parsedGoalCheckCommand{}, "", errors.New("goal check script must not set environment assignments")
	}

	if len(call.Args) == 0 {
		return parsedGoalCheckCommand{}, "", errors.New("goal check script command is required")
	}

	argv := make([]string, 0, len(call.Args))
	for _, arg := range call.Args {
		value, err := staticGoalCheckWord(arg)
		if err != nil {
			return parsedGoalCheckCommand{}, "", err
		}

		argv = append(argv, value)
	}

	if strings.TrimSpace(argv[0]) == "" {
		return parsedGoalCheckCommand{}, "", errors.New("goal check script executable is required")
	}

	var buf bytes.Buffer

	printer := syntax.NewPrinter()
	if err := printer.Print(&buf, call); err != nil {
		return parsedGoalCheckCommand{}, "", fmt.Errorf("render goal check script: %w", err)
	}

	subjects := rocketcode.BashPermissionSubjects(buf.String())
	if len(subjects) != 1 {
		return parsedGoalCheckCommand{}, "", errors.New("goal check script must render one bash permission subject")
	}

	return parsedGoalCheckCommand{text: strings.TrimSpace(buf.String()), argv: argv}, subjects[0], nil
}

func staticGoalCheckWord(word *syntax.Word) (string, error) {
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

func requireWorkspaceExecutable(root *os.Root, workspace, name string) error {
	workspace = filepath.Clean(workspace)

	resolved := name
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(workspace, resolved)
	}

	resolved = filepath.Clean(resolved)

	rel, err := filepath.Rel(workspace, resolved)
	if err != nil {
		return fmt.Errorf("resolve goal check executable %q: %w", name, err)
	}

	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("goal check executable must stay inside the workspace: %s", name)
	}

	info, err := root.Stat(filepath.ToSlash(rel))
	if err != nil {
		return fmt.Errorf("stat goal check executable %q: %w", name, err)
	}

	if info.IsDir() {
		return fmt.Errorf("goal check executable %q is a directory", name)
	}

	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("goal check executable %q is not executable", name)
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("goal check executable %q is not a regular executable file", name)
	}

	return nil
}
