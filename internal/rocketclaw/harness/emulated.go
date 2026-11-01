package harness

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
)

// emulatedExpansionTimeout bounds a prompt's !`…` line in an emulated shell, as the bash tool's default does.
const emulatedExpansionTimeout = defaultShellTimeout * time.Millisecond

// runEmulated runs command in shell's sandbox from cwd, which must lie in its folders, with the turn's
// variables turnEnv. The agent's commands run on the host with hostEnv and its secrets.
func runEmulated(ctx context.Context, rt *sandbox.Runtime, shell *CustomShell, command, cwd string, turnEnv, hostEnv []string, timeout time.Duration) (sandbox.Result, error) {
	if shell.Dir == "" {
		return sandbox.Result{}, errors.New("emulated shell: the agent's folder is not set up for this turn")
	}

	cwd, ok := guestWorkdir(append([]string{shell.Dir}, shell.ReadOnly...), cwd)
	if !ok {
		return sandbox.Result{}, fmt.Errorf("emulated shell: workdir %s is outside this agent's folders", cwd)
	}

	// Besides the turn's variables the guest has only these; nothing comes from RocketClaw's own environment.
	env := append(slices.Clone(turnEnv), "PATH=/extensions:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/tmp", "TMPDIR=/tmp", "LANG=C.UTF-8", "TZ=UTC")
	host := &emulatedHost{shell: shell, env: slices.Clone(hostEnv), client: easyWebFetchClient(shell.NetworkTargets)}

	for k, v := range shell.Secrets {
		host.env = append(host.env, k+"="+v)
	}

	res, err := rt.Run(ctx, &sandbox.Command{
		Script: command, Cwd: cwd, Dir: shell.Dir, ReadOnly: shell.ReadOnly, Env: env,
		Commands: slices.Sorted(maps.Keys(shell.Commands)), Timeout: timeout,
	}, host)
	if err != nil {
		return res, fmt.Errorf("emulated shell: %w", err)
	}

	return res, nil
}

// guestWorkdir finds cwd in folders, which the sandbox mounts where they are named. A cwd given by its real
// path, as the OS reports one under a linked folder, is turned back into that name: the check compares real
// paths, the guest uses names (docs/solutions/security-issues/easy-agent-folder-checks-must-compare-real-paths.md).
func guestWorkdir(folders []string, cwd string) (string, bool) {
	realCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return cwd, false
	}

	for _, dir := range folders {
		realDir, err := filepath.EvalSymlinks(dir)
		if rel, errRel := filepath.Rel(realDir, realCwd); err == nil && errRel == nil && (rel == "." || filepath.IsLocal(rel)) {
			return filepath.Join(dir, rel), true
		}
	}

	return cwd, false
}

// emulatedBashResult reports an emulated shell's run as the bash tool reports host bash.
func emulatedBashResult(res sandbox.Result, err error) BashResult {
	if err != nil {
		return bashFailure(err.Error())
	}

	code := ""

	switch {
	case res.TimedOut:
		code = "timeout"
	case res.ExitCode != 0:
		code = strconv.Itoa(res.ExitCode)
	}

	return BashResult{Output: cmp.Or(string(res.Output), "(no output)"), ErrorCode: code, Success: code == ""}
}

// emulatedHost runs, on the host, the commands an emulated shell asks for. The guest controls args, cwd and
// stdin only: the command lines, the environment and the network rules are the agent's.
type emulatedHost struct {
	shell  *CustomShell
	env    []string // RocketClaw's environment, the turn's variables and the agent's secrets
	client *http.Client
}

func (h *emulatedHost) Command(ctx context.Context, name string, args []string, cwd string, stdin io.Reader, stdout, stderr io.Writer) int {
	if name == sandbox.Curl {
		return curl(ctx, h.client, h.shell.Dir, args, cwd, stdout, stderr)
	}

	// An agent's command runs its command line with the guest's arguments appended, in host bash.
	cmd := exec.CommandContext(ctx, "/bin/bash", append([]string{"--noprofile", "--norc", "-c", h.shell.Commands[name] + ` "$@"`, name}, args...)...)
	cmd.Dir, cmd.Env = h.dir(cwd), h.env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

	err := cmd.Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}

		return exitErr.ExitCode()
	}

	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", name, err)

		return 126
	}

	return 0
}

// dir is where a host command runs: the guest's directory, which is a host path, when it is really inside the
// agent's folder; the folder otherwise.
func (h *emulatedHost) dir(cwd string) string {
	// shortcut: a guest could swap a checked directory for a link before the command starts; it only moves
	// where a trusted host command runs. Upgrade with a directory descriptor if needed.
	if realCwd, err := filepath.EvalSymlinks(cwd); err == nil && pathWithinRoot(h.shell.Dir, realCwd) {
		return realCwd
	}

	return h.shell.Dir
}
