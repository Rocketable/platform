//go:build linux

package rocketcode

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const linuxSandboxedBashDefaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

func (b *sandboxedBash) command(ctx context.Context, req sandboxedBashCommand) (*exec.Cmd, error) {
	paths, err := b.resolvePaths(req.workdir)
	if err != nil {
		return nil, err
	}

	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("sandboxed bash: bubblewrap/bwrap not found: %w", err)
	}

	args := []string{
		"--unshare-all",
		"--die-with-parent",
		"--new-session",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--bind", paths.workspace, "/work",
		"--chdir", paths.sandboxWorkdir,
	}

	for _, p := range []string{
		"/bin",
		"/sbin",
		"/usr",
		"/lib",
		"/lib64",
		"/etc",
		"/opt",
		"/nix/store",
		"/run/current-system",
	} {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--ro-bind", p, p)
		}
	}

	args = append(args, "--clearenv")
	for _, kv := range sandboxedBashEnv("/work", paths.sandboxWorkdir, paths.sandboxTmp, b.env, linuxSandboxedBashDefaultPath) {
		k, v, ok := strings.Cut(kv, "=")
		if ok && k != "" {
			args = append(args, "--setenv", k, v)
		}
	}

	args = append(args, "--", req.shell)
	args = append(args, req.args...)

	cmd := exec.CommandContext(ctx, bwrap, args...)
	cmd.Dir = paths.workspace

	return cmd, nil
}
