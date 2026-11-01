//go:build amd64 || arm64

package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Prepare readies the sandbox under dir in the background and returns at once: it checks the host, builds
// the pinned guest image the first time (a minute or two; later starts find it under the lock's hash), takes over
// RocketClaw's delegated cgroup, and runs a probe. Run waits for it. On any failure the Runtime stays
// unavailable with a reason that says what to do, and log records it in full, so emulated shells refuse rather
// than run unsandboxed (spec §5.3).
func Prepare(ctx context.Context, dir string, log *slog.Logger) *Runtime {
	rt := &Runtime{p: &prepared{ready: make(chan struct{})}}

	go func() {
		defer close(rt.p.ready)

		if rt.p.err = prepare(ctx, dir, rt, log); rt.p.err != nil {
			attrs := []any{"error", rt.p.err}
			if b, ok := errors.AsType[*BuildError](rt.p.err); ok {
				attrs = append(attrs, "output", string(b.Output))
			}

			log.Error("emulated shell unavailable", attrs...)
		}
	}()

	return rt
}

func prepare(ctx context.Context, dir string, rt *Runtime, log *slog.Logger) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find RocketClaw: %w", err)
	}

	if rt.p.self, err = filepath.EvalSymlinks(self); err != nil {
		return fmt.Errorf("find RocketClaw: %w", err)
	}

	if rt.p.bwrap, err = exec.LookPath("bwrap"); err != nil {
		return errors.New("this machine doesn't have bubblewrap, which the sandbox is built on; install the bubblewrap package and restart RocketClaw")
	}

	if rt.p.seccomp, err = seccompProgram(); err != nil {
		return err
	}

	if rt.p.cgroup, err = delegateCgroup(); err != nil {
		return fmt.Errorf("RocketClaw can't cap each command's memory and processes, because it wasn't started with a cgroup of its own (systemd Delegate=yes; see the README's Emulated shell section): %w", err)
	}

	// The image is named by its pin, so an upgrade builds afresh.
	sum := sha256.Sum256([]byte(Snapshot + suite + packages + string(packagesLock)))
	rt.p.rootfs = filepath.Join(dir, "rootfs-"+hex.EncodeToString(sum[:8]))
	rt.p.state = filepath.Join(dir, "state")

	if _, err := os.Stat(filepath.Join(rt.p.rootfs, ".complete")); err != nil {
		installed, err := rt.buildImage(ctx, log)

		switch {
		case errors.Is(err, exec.ErrNotFound):
			return errors.New("this machine doesn't have mmdebstrap, which builds the sandbox's Ubuntu image; install mmdebstrap and uidmap, then restart RocketClaw")
		case err != nil:
			return fmt.Errorf("RocketClaw couldn't build the Ubuntu image the sandbox runs in: %w; the server log has the details", err)
		}

		if strings.Join(installed, "\n")+"\n" != string(packagesLock) {
			_ = os.RemoveAll(rt.p.rootfs)

			return fmt.Errorf("the Ubuntu image that was built doesn't have the package versions in packages.%s.lock, so it was thrown away for safety", runtime.GOARCH)
		}
	}

	// Older images go only once this one is built: going back to the previous RocketClaw while an upgrade's
	// build still fails finds its image intact.
	old, _ := filepath.Glob(filepath.Join(dir, "rootfs-*"))
	for _, o := range old {
		if o != rt.p.rootfs {
			if err := os.RemoveAll(o); err != nil {
				return fmt.Errorf("remove old image: %w", err)
			}
		}
	}

	if err := os.RemoveAll(rt.p.state); err != nil {
		return fmt.Errorf("clear state: %w", err)
	}

	if err := os.Mkdir(rt.p.state, 0o700); err != nil {
		return fmt.Errorf("create state: %w", err)
	}

	// A probe proves every isolation layer works here (E08); a refused namespace or seccomp filter fails now.
	res, err := rt.run(ctx, &Command{Script: "test -r /proc/self/status", Cwd: rt.p.state, Dir: rt.p.state, Timeout: 30 * time.Second}, probeHost{})
	if err == nil && res.ExitCode != 0 {
		err = fmt.Errorf("probe exited %d: %s", res.ExitCode, res.Output)
	}

	if err != nil {
		return fmt.Errorf("a test command couldn't start inside the sandbox: %w; on Ubuntu 23.10 or later this usually means AppArmor doesn't yet let bubblewrap create user namespaces (see the README's Emulated shell section)", err)
	}

	return nil
}

// The pauses between tries to build the image while Ubuntu's archive is unreachable or flaky: they double
// from the first up to the longest, and the tries never stop, so a host recovers on its own.
const (
	firstRetry   = 30 * time.Second
	longestRetry = 15 * time.Minute
)

// buildImage builds the pinned image, trying again while mmdebstrap fails, which is how an unreachable or
// flaky archive shows. Meanwhile Err says when the next try is and why the last one failed, and the server log
// has each failure. Any other error returns at once: no retry fixes it.
func (rt *Runtime) buildImage(ctx context.Context, log *slog.Logger) ([]string, error) {
	for pause := firstRetry; ; pause = min(2*pause, longestRetry) {
		installed, _, err := BuildImage(ctx, Snapshot, runtime.GOARCH, rt.p.rootfs)

		b, ok := errors.AsType[*BuildError](err)
		if !ok || errors.Is(err, exec.ErrNotFound) {
			return installed, err
		}

		next := time.Now().Add(pause)
		log.Warn("building the emulated shell's Ubuntu image failed; trying again", "error", err, "next", next, "output", string(b.Output))

		rt.p.mu.Lock()
		rt.p.retry = fmt.Errorf("%w; the last try failed (%v), and the next is at %s", ErrPreparing, err, next.Format("15:04"))
		rt.p.mu.Unlock()

		select {
		case <-time.After(pause):
		case <-ctx.Done():
			return nil, fmt.Errorf("stopped while building: %w", ctx.Err())
		}
	}
}
