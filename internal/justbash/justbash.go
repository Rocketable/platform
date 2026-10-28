// Package justbash runs an agent's shell commands in just-bash (https://github.com/vercel-labs/just-bash)
// instead of host bash. Each bash call starts the embedded wrapper with Node; the wrapper asks this
// package's Server, over a Unix socket, for the call's setup, its approved host commands, and its
// network requests.
package justbash

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:runtime
var runtimeFS embed.FS

// install unpacks the embedded runtime into dir, named by its content hash so processes share one copy,
// and returns the path of the unpacked wrapper.
func install(dir string) (string, error) {
	sum := sha256.New()

	if err := fs.WalkDir(runtimeFS, "runtime", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := runtimeFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		_, _ = fmt.Fprintf(sum, "%s\x00%d\x00", path, len(data))
		_, _ = sum.Write(data)

		return nil
	}); err != nil {
		return "", fmt.Errorf("hash just-bash runtime: %w", err)
	}

	final := filepath.Join(dir, "runtime-"+hex.EncodeToString(sum.Sum(nil))[:16])
	wrapper := filepath.Join(final, "wrapper.mjs")

	if _, err := os.Stat(wrapper); err == nil {
		return wrapper, nil
	}

	tmp, err := os.MkdirTemp(dir, "unpack-")
	if err != nil {
		return "", fmt.Errorf("unpack just-bash runtime: %w", err)
	}

	defer func() { _ = os.RemoveAll(tmp) }()

	sub, _ := fs.Sub(runtimeFS, "runtime") // "runtime" is a valid path.
	if err := os.CopyFS(tmp, sub); err != nil {
		return "", fmt.Errorf("unpack just-bash runtime: %w", err)
	}

	if err := os.Rename(tmp, final); err != nil {
		// Another process may have unpacked the same runtime meanwhile; its copy is identical.
		if _, errStat := os.Stat(wrapper); errStat != nil {
			return "", fmt.Errorf("unpack just-bash runtime: %w", err)
		}
	}

	return wrapper, nil
}

// writeLauncher writes the program an agent's customShell runs: it runs the wrapper with Node from the
// bash call's PATH, naming the agent. It reports a missing Node itself.
func writeLauncher(dir, wrapper, agent string) (string, error) {
	agentHex := hex.EncodeToString([]byte(agent))
	path := filepath.Join(dir, agentHex)
	script := "#!/bin/sh\n" +
		"command -v node >/dev/null 2>&1 || { echo 'just-bash needs node 20.19 or later on PATH' >&2; exit 127; }\n" +
		"exec node " + shellQuote(wrapper) + " " + agentHex + " \"$1\"\n"

	// Write and rename, so a bash call never runs a half-written launcher.
	tmp, err := os.CreateTemp(dir, "launcher-")
	if err != nil {
		return "", fmt.Errorf("write just-bash launcher: %w", err)
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	_, errWrite := tmp.WriteString(script)
	if err := errors.Join(errWrite, tmp.Chmod(0o700), tmp.Close(), os.Rename(tmp.Name(), path)); err != nil {
		return "", fmt.Errorf("write just-bash launcher: %w", err)
	}

	return path, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
