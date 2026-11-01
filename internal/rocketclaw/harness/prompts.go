package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
)

var promptShellPattern = regexp.MustCompile("!`([^`]+)`")

type promptExpansionEnvironment struct {
	root         *os.Root
	hostDir      string
	shellTemp    shellTempConfig
	env          []string
	shellCommand shellCommander
	sandbox      *sandbox.Runtime
	customShell  CustomShell
}

func newPromptExpansionEnvironment(root *os.Root, shellTemp shellTempConfig, env []string, shellCommand shellCommander, rt *sandbox.Runtime) (promptExpansionEnvironment, error) {
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

	return promptExpansionEnvironment{root: root, hostDir: hostDir, shellTemp: shellTemp, env: slices.Clone(env), shellCommand: shellCommand, sandbox: rt}, nil
}

func (e *promptExpansionEnvironment) expandShellCommands(ctx context.Context, prompt string) string {
	return promptShellPattern.ReplaceAllStringFunc(prompt, func(match string) string {
		command := match[2 : len(match)-1]

		if err := e.shellTemp.ensureTempDir(e.root); err != nil {
			return ""
		}

		env := append(os.Environ(), e.env...)
		env = append(env, "TMPDIR="+e.shellTemp.tmpDir)

		// An emulated shell runs in its folder; like host bash here, only stdout counts, and no limit but the sandbox's.
		if e.customShell.Emulated {
			res, err := runEmulated(context.WithoutCancel(ctx), e.sandbox, &e.customShell, "exec 2>/dev/null\n"+command, e.customShell.Dir, e.env, env, emulatedExpansionTimeout)
			if err != nil {
				return ""
			}

			return string(res.Output)
		}

		shell, args := withCustomShell(e.shellCommand, &e.customShell, command)
		cmd := exec.CommandContext(context.WithoutCancel(ctx), shell, args...)
		cmd.Dir, cmd.Env = e.hostDir, env

		var stdout bytes.Buffer

		cmd.Stdout = &stdout
		// Keep captured stdout even when the command fails.
		_ = cmd.Run()

		return stdout.String()
	})
}

func expandAgentPrompt(ctx context.Context, agent *Agent, enabled bool, env *promptExpansionEnvironment) {
	if !enabled {
		return
	}

	agentEnv := env.forAgent(agent)
	agent.Prompt = agentEnv.expandShellCommands(ctx, agent.Prompt)
}

// forAgent returns e for agent's prompt and skills: expanding through agent's shell or, for an easy agent,
// in host bash outside its emulator, with its secrets (R33).
func (e *promptExpansionEnvironment) forAgent(agent *Agent) promptExpansionEnvironment {
	scoped := *e
	scoped.customShell = agent.CustomShell

	if agent.Mode == AgentModeEasy {
		scoped.customShell = CustomShell{}
		scoped.env = slices.Clone(e.env)

		for name, value := range agent.CustomShell.Secrets {
			scoped.env = append(scoped.env, name+"="+value)
		}
	}

	return scoped
}

// DefaultShellCommand uses Bash to match the permission parser's grammar.
type DefaultShellCommand struct{}

// Command selects Bash and its arguments for command.
// Privileged mode disables startup files and inherited functions and shell options.
func (DefaultShellCommand) Command(command string) (path string, args []string) {
	return "/bin/bash", []string{"--noprofile", "--norc", "-p", "-c", command}
}
