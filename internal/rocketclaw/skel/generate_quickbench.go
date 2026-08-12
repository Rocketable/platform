//go:build ignore

// Copies quickbench skill/agent sources into this embedded skel package.
//
// Source of truth: cmd/quickbench/{skills,agents}/
// Generated into: internal/rocketclaw/skel/
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "generate_quickbench: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// go:generate runs with cwd = this package directory (internal/rocketclaw/skel).
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		return err
	}

	copies := []struct{ src, dst string }{
		{
			src: filepath.Join(root, "cmd", "quickbench", "skills", "main-archive-benchmarks", "SKILL.md"),
			dst: filepath.Join(root, "internal", "rocketclaw", "skel", ".rocketclaw", "skills", "main-archive-benchmarks", "SKILL.md"),
		},
		{
			src: filepath.Join(root, "cmd", "quickbench", "agents", "slack-to-benchmark.md"),
			dst: filepath.Join(root, "internal", "rocketclaw", "skel", "agents", "slack-to-benchmark.md"),
		},
	}

	for _, c := range copies {
		data, err := os.ReadFile(c.src)
		if err != nil {
			return fmt.Errorf("read %s: %w", c.src, err)
		}

		if err := os.MkdirAll(filepath.Dir(c.dst), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(c.dst), err)
		}

		if err := os.WriteFile(c.dst, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", c.dst, err)
		}

		fmt.Printf("wrote %s\n", c.dst)
	}

	return nil
}
