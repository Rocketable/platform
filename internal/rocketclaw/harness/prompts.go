package harness

import (
	"bytes"
	"cmp"
	"context"
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
	root        *os.Root
	hostDir     string
	shellTemp   shellTempConfig
	env         []string
	sandbox     *sandbox.Runtime
	customShell CustomShell
}

func newPromptExpansionEnvironment(root *os.Root, shellTemp shellTempConfig, env []string, rt *sandbox.Runtime) (promptExpansionEnvironment, error) {
	hostDir, err := filepath.Abs(root.Name())
	if err != nil {
		return promptExpansionEnvironment{}, fmt.Errorf("resolve prompt expansion root: %w", err)
	}

	return promptExpansionEnvironment{root: root, hostDir: hostDir, shellTemp: shellTemp, env: slices.Clone(env), sandbox: rt}, nil
}

func (e *promptExpansionEnvironment) expandShellCommands(ctx context.Context, prompt string) string {
	return promptShellPattern.ReplaceAllStringFunc(prompt, func(match string) string {
		command := match[2 : len(match)-1]

		if err := e.shellTemp.ensureTempDir(e.root); err != nil {
			return ""
		}

		env := append(os.Environ(), e.env...)
		env = append(env, "TMPDIR="+e.shellTemp.tmpDir)

		if e.customShell.Emulated {
			res, err := runEmulated(context.WithoutCancel(ctx), e.sandbox, &e.customShell, "exec 2>/dev/null\n"+command, "", e.customShell.Dir, e.env, env, nil, emulatedExpansionTimeout)
			if err != nil {
				return ""
			}

			return string(res.Output)
		}

		env = append(env, e.customShell.Env...)
		shell, args := withCustomShell(&e.customShell, command)
		cmd := exec.CommandContext(context.WithoutCancel(ctx), shell, args...)
		cmd.Dir, cmd.Env = cmp.Or(e.customShell.Dir, e.hostDir), env

		var stdout bytes.Buffer

		cmd.Stdout = &stdout
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
