package backend

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
)

type emulatedShells struct {
	mu         sync.Mutex
	rt         *sandbox.Runtime
	errSandbox error
	sandbox    *sandbox.Runtime
}

func sandboxDir(cfg *config.Config) string {
	return filepath.Join(cfg.Workspace, cfg.RuntimeDirName(), "sandbox")
}

func (r *Runtime) prepareSandbox() {
	agents, _, err := LoadRuntimeDefinitions(r.Cfg, r.Cfg.Clone().RuntimeDirName())
	e := &r.Sessions.shells

	var rt *sandbox.Runtime

	if err == nil {
		for name := range agents.Items {
			if !agents.Items[name].CustomShell.Emulated {
				continue
			}

			dir := sandboxDir(r.Cfg.Clone())

			e.mu.Lock()
			if e.rt == nil || e.rt.Err() != nil && !errors.Is(e.rt.Err(), sandbox.ErrPreparing) {
				e.rt = r.sandboxes.Prepare(r.RunCtx, dir, r.Log)
			}

			rt = e.rt
			e.mu.Unlock()

			break
		}
	}

	if err != nil {
		err = fmt.Errorf("RocketClaw couldn't read its agents to tell whether one needs the sandbox: %w", err)
		r.Log.Error("emulated shell unavailable", "error", err)
	}

	e.mu.Lock()
	e.sandbox, e.errSandbox = rt, err
	e.mu.Unlock()
}

func (e *emulatedShells) current() *sandbox.Runtime {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.rt == nil {
		return &sandbox.Runtime{}
	}

	return e.rt
}

func (r *Runtime) SandboxError() string {
	e := &r.Sessions.shells
	e.mu.Lock()
	defer e.mu.Unlock()

	err := e.errSandbox
	if err == nil && e.sandbox != nil {
		err = e.sandbox.Err()
	}

	switch {
	case err == nil:
		return ""
	case errors.Is(err, sandbox.ErrPreparing):
		return "Emulated shell getting ready: " + err.Error()
	}

	return "Emulated shell unavailable: " + err.Error()
}
