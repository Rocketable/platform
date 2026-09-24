package rocketcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var promptShellPattern = regexp.MustCompile("!`([^`]+)`")

type promptExpansionEnvironment struct {
	root         *os.Root
	hostDir      string
	shellTemp    shellTempConfig
	env          []string
	shellCommand ShellCommandFunc
}

func newPromptExpansionEnvironment(root *os.Root, shellTemp shellTempConfig, env []string, shellCommand ShellCommandFunc) (promptExpansionEnvironment, error) {
	var zero promptExpansionEnvironment

	if root == nil {
		return zero, errors.New("prompt expansion root is required")
	}

	rootName := root.Name()
	if rootName == "" {
		return zero, errors.New("prompt expansion root name is required")
	}

	if _, err := root.Stat("."); err != nil {
		return zero, fmt.Errorf("stat prompt expansion root: %w", err)
	}

	hostDir, err := filepath.Abs(rootName)
	if err != nil {
		return zero, fmt.Errorf("resolve prompt expansion root: %w", err)
	}

	return promptExpansionEnvironment{root: root, hostDir: hostDir, shellTemp: shellTemp, env: slices.Clone(env), shellCommand: shellCommand}, nil
}

func (e *promptExpansionEnvironment) expandShellCommands(ctx context.Context, prompt string) string {
	return expandPromptShellCommands(prompt, func(command string) string {
		if err := e.shellTemp.ensureTempDir(e.root); err != nil {
			return ""
		}

		shell, args := e.shellCommand(command)
		cmd := exec.CommandContext(context.WithoutCancel(ctx), shell, args...)
		cmd.Dir = e.hostDir
		cmd.Env = append(os.Environ(), e.env...)
		cmd.Env = append(cmd.Env, "TMPDIR="+e.shellTemp.tmpDir)

		var stdout bytes.Buffer

		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			return stdout.String()
		}

		return stdout.String()
	})
}

func expandPromptShellCommands(prompt string, run func(string) string) string {
	if prompt == "" || run == nil {
		return prompt
	}

	matches := promptShellPattern.FindAllStringSubmatchIndex(prompt, -1)
	if len(matches) == 0 {
		return prompt
	}

	var output strings.Builder
	output.Grow(len(prompt))

	last := 0
	for _, match := range matches {
		output.WriteString(prompt[last:match[0]])
		output.WriteString(run(prompt[match[2]:match[3]]))
		last = match[1]
	}

	output.WriteString(prompt[last:])

	return output.String()
}

func expandAgentPrompt(ctx context.Context, agent *Agent, enabled bool, env *promptExpansionEnvironment) {
	if agent == nil {
		return
	}

	if !enabled {
		return
	}

	agent.Prompt = env.expandShellCommands(ctx, agent.Prompt)
}

// DefaultShellCommand uses Bash to match the permission parser's grammar.
// Privileged mode disables startup files and inherited functions and shell options.
func DefaultShellCommand(command string) (path string, args []string) {
	return "/bin/bash", []string{"--noprofile", "--norc", "-p", "-c", command}
}
