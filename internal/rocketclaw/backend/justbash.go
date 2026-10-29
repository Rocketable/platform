package backend

import (
	"path/filepath"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/justbash"
)

// justBashDir holds just-bash's wrapper and node_modules. A reload keeps it.
func justBashDir(cfg *config.Config) string {
	return filepath.Join(cfg.Workspace, cfg.RuntimeDirName(), "justbash")
}

// installJustBash installs just-bash when an agent sets shell: emulated. It never fails startup or a
// reload: the Web shows why it failed instead.
func (r *Runtime) installJustBash() {
	agents, _, err := LoadRuntimeDefinitions(r.Cfg, r.Cfg.Clone().RuntimeDirName())
	if err == nil {
		for name := range agents.Items {
			if agents.Items[name].CustomShell.Setup != nil {
				err = justbash.Install(justBashDir(r.Cfg.Clone()))

				break
			}
		}
	}

	if err != nil {
		r.Log.Error("install just-bash", "error", err)
	}

	r.justBashMu.Lock()
	r.justBashErr = err
	r.justBashMu.Unlock()
}

// JustBashInstallError reports why just-bash failed to install, or "" when it did not fail.
func (r *Runtime) JustBashInstallError() string {
	r.justBashMu.Lock()
	defer r.justBashMu.Unlock()

	if r.justBashErr == nil {
		return ""
	}

	return r.justBashErr.Error()
}
