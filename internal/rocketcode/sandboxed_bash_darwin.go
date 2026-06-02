//go:build darwin

package rocketcode

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
)

const darwinSandboxedBashDefaultPath = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

func (b *sandboxedBash) command(ctx context.Context, req sandboxedBashCommand) (*exec.Cmd, error) {
	paths, err := b.resolvePaths(req.workdir)
	if err != nil {
		return nil, err
	}

	sandboxExec, err := exec.LookPath("sandbox-exec")
	if err != nil {
		return nil, fmt.Errorf("sandboxed bash: sandbox-exec not found: %w", err)
	}

	args := make([]string, 0, 3+len(req.args))
	args = append(args, "-p", macOSSandboxedBashProfile(paths.workspace, paths.tmp), req.shell)
	args = append(args, req.args...)

	cmd := exec.CommandContext(ctx, sandboxExec, args...)
	cmd.Dir = paths.workdir
	cmd.Env = sandboxedBashEnv(paths.workspace, paths.workdir, paths.tmp, b.env, darwinSandboxedBashDefaultPath)

	return cmd, nil
}

func macOSSandboxedBashProfile(workspace, tmp string) string {
	return fmt.Sprintf(`
(version 1)

(deny default)

(allow process*)
(allow signal (target same-sandbox))
(allow sysctl-read)
(allow mach-lookup)

(allow file-read-metadata)

(allow file-read*
  (subpath "/bin")
  (subpath "/sbin")
  (subpath "/usr")
  (subpath "/System")
  (subpath "/Library")
  (subpath "/private/etc")
  (subpath "/private/var/db")
  (subpath "/opt/homebrew"))

(allow file-read* file-write*
  (literal "/dev/null")
  (literal "/dev/tty")
  (literal "/dev/random")
  (literal "/dev/urandom"))

(allow file-read* file-write*
  (subpath %[1]s)
  (subpath %[2]s))
`, strconv.Quote(workspace), strconv.Quote(tmp))
}
