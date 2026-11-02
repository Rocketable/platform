package backend

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
)

// emulatedShells holds the process's emulated shell sandbox: prepared once, when an agent first needs it,
// and shared by every harness. The zero value has no rootfs, so its emulated shells refuse.
type emulatedShells struct {
	// rootfs is the sandbox rootfs cmd/rocketclaw embeds; set before the runtime starts.
	rootfs []byte

	mu sync.Mutex
	rt sandbox.Runtime
}

// sandboxDir holds the unpacked rootfs and per-command state. A reload keeps it.
func sandboxDir(cfg *config.Config) string {
	return filepath.Join(cfg.Workspace, cfg.RuntimeDirName(), "sandbox")
}

// prepare readies the sandbox unless it already is, and returns why it is unavailable, or nil.
func (e *emulatedShells) prepare(ctx context.Context, dir string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.rt.Err() != nil {
		e.rt = *sandbox.Prepare(ctx, e.rootfs, dir)
	}

	if err := e.rt.Err(); err != nil {
		return fmt.Errorf("prepare sandbox: %w", err)
	}

	return nil
}

// current is the sandbox for a new harness, a copy of its own.
func (e *emulatedShells) current() *sandbox.Runtime {
	e.mu.Lock()
	defer e.mu.Unlock()

	rt := e.rt

	return &rt
}

// prepareSandbox readies the emulated shell when an agent is easy or sets shell: emulated. It never fails
// startup or a reload: the Web shows why emulated shells cannot run instead.
func (r *Runtime) prepareSandbox() {
	agents, _, err := LoadRuntimeDefinitions(r.Cfg, r.Cfg.Clone().RuntimeDirName())
	if err == nil {
		for name := range agents.Items {
			if agents.Items[name].CustomShell.Emulated {
				err = r.Sessions.shells.prepare(r.RunCtx, sandboxDir(r.Cfg.Clone()))

				break
			}
		}
	}

	if err != nil {
		r.Log.Error("prepare emulated shell", "error", err)
	}

	r.sandboxMu.Lock()
	r.sandboxErr = err
	r.sandboxMu.Unlock()
}

// SandboxError reports why emulated shells cannot run, or "" when they can or no agent uses one.
func (r *Runtime) SandboxError() string {
	r.sandboxMu.Lock()
	defer r.sandboxMu.Unlock()

	if r.sandboxErr == nil {
		return ""
	}

	return r.sandboxErr.Error()
}
