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

type Bubblewrap struct{}

func (Bubblewrap) Prepare(ctx context.Context, dir string, log *slog.Logger) *Runtime {
	rt := &Runtime{p: &prepared{ready: make(chan struct{}), log: log}}

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

	rt.p.seccomp = seccompProgram()

	if rt.p.cgroup, err = delegateCgroup(); err != nil {
		return fmt.Errorf("RocketClaw can't cap each command's memory and processes, because it wasn't started with a cgroup of its own (systemd Delegate=yes; see the README's Emulated shell section): %w", err)
	}

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

	res, err := rt.run(ctx, &Command{Script: "test -r /proc/self/status", Cwd: rt.p.state, Dir: rt.p.state, Timeout: 30 * time.Second}, probeHost{})
	if err == nil && res.ExitCode != 0 {
		err = fmt.Errorf("probe exited %d: %s", res.ExitCode, res.Output)
	}

	if err != nil {
		return fmt.Errorf("a test command couldn't start inside the sandbox: %w; on Ubuntu 23.10 or later this usually means AppArmor doesn't yet let bubblewrap create user namespaces (see the README's Emulated shell section)", err)
	}

	return nil
}

const (
	firstRetry   = 30 * time.Second
	longestRetry = 15 * time.Minute
)

func (rt *Runtime) buildImage(ctx context.Context, log *slog.Logger) ([]string, error) {
	for pause := firstRetry; ; pause = min(2*pause, longestRetry) {
		installed, _, err := BuildImage(ctx, Snapshot, runtime.GOARCH, rt.p.rootfs, rt.p.cgroup)

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
