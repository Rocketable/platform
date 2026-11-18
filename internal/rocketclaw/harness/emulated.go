package harness

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

const emulatedExpansionTimeout = defaultShellTimeout * time.Millisecond

func runEmulated(ctx context.Context, rt *sandbox.Runtime, shell *CustomShell, command, cwd string, turnEnv, hostEnv []string, timeout time.Duration) (sandbox.Result, error) {
	if shell.Dir == "" {
		return sandbox.Result{}, errors.New("emulated shell: the agent's folder is not set up for this turn")
	}

	cwd, ok := guestWorkdir(append([]string{shell.Dir}, shell.ReadOnly...), cwd)
	if !ok {
		return sandbox.Result{}, fmt.Errorf("emulated shell: workdir %s is outside this agent's folders", cwd)
	}

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

type emulatedHost struct {
	shell  *CustomShell
	env    []string
	client *http.Client
}

func (h *emulatedHost) Command(ctx context.Context, log *slog.Logger, name string, args []string, cwd string, stdin io.Reader, stdout, stderr io.Writer) int {
	if name == sandbox.Curl {
		return curl(ctx, log, h.client, h.shell.Dir, args, cwd, stdout, stderr)
	}

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

func (h *emulatedHost) dir(cwd string) string {
	if realCwd, err := filepath.EvalSymlinks(cwd); err == nil && pathWithinRoot(h.shell.Dir, realCwd) {
		return realCwd
	}

	return h.shell.Dir
}
