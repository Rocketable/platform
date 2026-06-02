package rocketcode

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

type sandboxedBash struct {
	root        *os.Root
	shellOutput shellOutputConfig
	env         []string
}

type sandboxedBashCommand struct {
	shell   string
	args    []string
	workdir string
}

type sandboxedBashPaths struct {
	workspace      string
	workdir        string
	tmp            string
	sandboxWorkdir string
	sandboxTmp     string
}

func newSandboxedBash(root *os.Root, shellOutput shellOutputConfig, env []string) sandboxedBash {
	return sandboxedBash{root: root, shellOutput: shellOutput, env: slices.Clone(env)}
}

func (b *sandboxedBash) resolvePaths(workdir string) (sandboxedBashPaths, error) {
	requestedWorkdir := workdir
	if workdir == "" {
		workdir = "."
	}

	workdir, err := normalizeRootName(b.root, workdir)
	if err != nil {
		return sandboxedBashPaths{}, fmt.Errorf("resolve sandboxed bash workdir %q: %w", requestedWorkdir, err)
	}

	workspace, err := b.hostDir(".")
	if err != nil {
		return sandboxedBashPaths{}, fmt.Errorf("resolve sandboxed bash workspace: %w", err)
	}

	hostWorkdir, err := b.hostDir(workdir)
	if err != nil {
		return sandboxedBashPaths{}, fmt.Errorf("resolve sandboxed bash workdir %q: %w", workdir, err)
	}

	tmpRel, err := normalizeRootName(b.root, b.shellOutput.tmpDir)
	if err != nil {
		return sandboxedBashPaths{}, fmt.Errorf("resolve sandboxed bash temp dir: %w", err)
	}

	if _, err := b.root.Stat(tmpRel); err != nil {
		return sandboxedBashPaths{}, fmt.Errorf("stat sandboxed bash temp dir %q: %w", tmpRel, err)
	}

	return sandboxedBashPaths{
		workspace:      workspace,
		workdir:        hostWorkdir,
		tmp:            b.shellOutput.tmpDir,
		sandboxWorkdir: sandboxedBashPath("/work", workdir),
		sandboxTmp:     sandboxedBashPath("/work", tmpRel),
	}, nil
}

func (b *sandboxedBash) hostDir(name string) (string, error) {
	root, err := b.root.OpenRoot(name)
	if err != nil {
		return "", fmt.Errorf("open rooted host dir %q: %w", name, err)
	}

	hostDir := root.Name()
	if errClose := root.Close(); errClose != nil {
		return "", fmt.Errorf("close rooted host dir %q: %w", name, errClose)
	}

	return hostDir, nil
}

func sandboxedBashPath(base, rel string) string {
	if rel == "." {
		return base
	}

	return path.Join(base, filepath.ToSlash(rel))
}

func sandboxedBashEnv(home, pwd, tmp string, extra []string, defaultPath string) []string {
	env := map[string]string{
		"PATH":   defaultPath,
		"HOME":   home,
		"PWD":    pwd,
		"TMPDIR": tmp,
	}

	order := []string{"PATH", "HOME", "PWD", "TMPDIR"}

	if term := os.Getenv("TERM"); term != "" {
		env["TERM"] = term

		order = append(order, "TERM")
	}

	for _, kv := range extra {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}

		if _, seen := env[k]; !seen {
			order = append(order, k)
		}

		env[k] = v
	}

	out := make([]string, 0, len(env))
	for _, k := range order {
		if v, ok := env[k]; ok {
			out = append(out, k+"="+v)
		}
	}

	return out
}
