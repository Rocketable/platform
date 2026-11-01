package backend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
)

// emulatedShells holds the process's emulated shell sandbox: prepared once, when an agent first needs it,
// and shared by every harness. The zero value has none, so its emulated shells refuse.
type emulatedShells struct {
	mu sync.Mutex
	rt *sandbox.Runtime
}

// sandboxDir holds the guest image and per-command state. A reload keeps it.
func sandboxDir(cfg *config.Config) string {
	return filepath.Join(cfg.Workspace, cfg.RuntimeDirName(), "sandbox")
}

// prepare starts preparing the sandbox unless it is ready or on its way, and returns it.
func (e *emulatedShells) prepare(ctx context.Context, dir string, log *slog.Logger) *sandbox.Runtime {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.rt == nil || e.rt.Err() != nil && !errors.Is(e.rt.Err(), sandbox.ErrPreparing) {
		e.rt = sandbox.Prepare(ctx, dir, log)
	}

	return e.rt
}

// prepareSandbox starts readying the emulated shell when an agent is easy or sets shell: emulated. It never
// fails or delays startup or a reload: commands wait for it, and the Web shows why it cannot run instead.
func (r *Runtime) prepareSandbox() {
	agents, _, err := LoadRuntimeDefinitions(r.Cfg, r.Cfg.Clone().RuntimeDirName())

	var rt *sandbox.Runtime

	if err == nil {
		for name := range agents.Items {
			if agents.Items[name].CustomShell.Emulated {
				rt = r.Sessions.shells.prepare(r.RunCtx, sandboxDir(r.Cfg.Clone()), r.Log)

				break
			}
		}
	}

	if err != nil {
		err = fmt.Errorf("RocketClaw couldn't read its agents to tell whether one needs the sandbox: %w", err)
		r.Log.Error("emulated shell unavailable", "error", err)
	}

	r.sandboxMu.Lock()
	r.sandbox, r.sandboxErr = rt, err
	r.sandboxMu.Unlock()
}

// current is the sandbox for a new harness: the zero Runtime until an agent needed one.
func (e *emulatedShells) current() *sandbox.Runtime {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.rt == nil {
		return &sandbox.Runtime{}
	}

	return e.rt
}

// SandboxError is the Web's notice about emulated shells: why they cannot run, that they are getting ready, or
// "" when they can or no agent uses one.
func (r *Runtime) SandboxError() string {
	r.sandboxMu.Lock()
	defer r.sandboxMu.Unlock()

	err := r.sandboxErr
	if err == nil && r.sandbox != nil {
		err = r.sandbox.Err()
	}

	switch {
	case err == nil:
		return ""
	case errors.Is(err, sandbox.ErrPreparing):
		return "Emulated shell getting ready: " + err.Error()
	}

	return "Emulated shell unavailable: " + err.Error()
}
