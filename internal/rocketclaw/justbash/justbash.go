// Package justbash runs the shell commands of easy agents and of agents that set shell: emulated in
// just-bash (https://github.com/vercel-labs/just-bash), through wrapper.ts run by Node.
package justbash

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
)

var (
	//go:embed wrapper.ts
	wrapper []byte
	//go:embed package.json
	packageJSON []byte
)

// Shell returns the customShell of an agent whose frontmatter sets shell: emulated, running from dir, the
// install directory inside runtimeDir. It returns a zero CustomShell for other agents.
func Shell(frontmatter map[string]any, workspace, runtimeDir, dir string) (harness.CustomShell, error) {
	if frontmatter["shell"] != "emulated" {
		return harness.CustomShell{}, nil
	}

	// just-bash always works on top of a working directory.
	if dir, ok := frontmatter["workingDirectory"].(string); !ok || strings.TrimSpace(dir) == "" {
		return harness.CustomShell{}, errors.New("workingDirectory: required with shell: emulated")
	}

	config := map[string]any{"workspace": workspace, "runtimeDir": runtimeDir}

	for _, key := range []string{"workingDirectory", "commands", "allowedNetworkTargets"} {
		if v, ok := frontmatter[key]; ok {
			config[key] = v
		}
	}

	setup, err := json.Marshal(config)
	if err != nil {
		return harness.CustomShell{}, fmt.Errorf("just-bash settings: %w", err)
	}

	return harness.CustomShell{Path: filepath.Join(dir, "wrapper.ts"), Setup: setup, Emulated: true}, nil
}

// Setup is how an easy agent's commands run in just-bash for one turn.
type Setup struct {
	// Folder is the agent's workingDirectory, resolved and checked by RocketClaw; the wrapper takes it as is.
	Folder   string            `json:"folder"`
	Commands map[string]string `json:"commands"`
	// NetworkTargets nil means public addresses only.
	NetworkTargets []string `json:"allowedNetworkTargets"`
	// Env is the emulator's whole environment but TMPDIR: the turn's variables.
	Env map[string]string `json:"env"`
	// Secrets join RocketClaw's environment for commands only.
	Secrets map[string]string `json:"secrets"`
	// ReadOnly are folders, none inside another, shown read-only at their paths: the chat's uploads and the
	// agent's skills.
	ReadOnly []string `json:"readOnly"`
}

// EasyShell returns the customShell that runs an easy agent's commands with setup, from dir, the install
// directory.
func EasyShell(setup *Setup, dir string) harness.CustomShell {
	data, _ := json.Marshal(setup)

	return harness.CustomShell{Path: filepath.Join(dir, "wrapper.ts"), Setup: data, Emulated: true, Dir: setup.Folder, ReadOnly: setup.ReadOnly, Secrets: setup.Secrets}
}

// Install writes the wrapper into dir and installs its pinned just-bash with npm unless that version is
// already there.
func Install(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("install just-bash: %w", err)
	}

	// Written aside and renamed, so a running bash call never reads a half-written wrapper.
	for name, data := range map[string][]byte{"wrapper.ts": wrapper, "package.json": packageJSON} {
		tmp := filepath.Join(dir, "."+name)
		if err := os.WriteFile(tmp, data, 0o755); err != nil {
			return fmt.Errorf("install just-bash: %w", err)
		}

		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("install just-bash: %w", err)
		}
	}

	var want, have struct {
		Version      string
		Dependencies map[string]string
	}

	_ = json.Unmarshal(packageJSON, &want) // The embedded package.json is valid.

	if data, err := os.ReadFile(filepath.Join(dir, "node_modules", "just-bash", "package.json")); err == nil && json.Unmarshal(data, &have) == nil && have.Version == want.Dependencies["just-bash"] {
		return nil
	}

	cmd := exec.Command("npm", "install", "--no-audit", "--no-fund")
	cmd.Dir = dir

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("npm install just-bash@%s: %w: %s", want.Dependencies["just-bash"], err, out)
	}

	return nil
}
