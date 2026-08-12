package skel

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuickbenchSkelGenerated(t *testing.T) {
	root := repoRoot(t)

	pairs := []struct{ src, dst string }{
		{
			src: filepath.Join(root, "cmd", "quickbench", "skills", "main-archive-benchmarks", "SKILL.md"),
			dst: filepath.Join(root, "internal", "rocketclaw", "skel", ".rocketclaw", "skills", "main-archive-benchmarks", "SKILL.md"),
		},
		{
			src: filepath.Join(root, "cmd", "quickbench", "agents", "slack-to-benchmark.md"),
			dst: filepath.Join(root, "internal", "rocketclaw", "skel", "agents", "slack-to-benchmark.md"),
		},
	}

	for _, p := range pairs {
		want, err := os.ReadFile(p.src)
		require.NoError(t, err, p.src)
		got, err := os.ReadFile(p.dst)
		require.NoError(t, err, p.dst)

		if !bytes.Equal(want, got) {
			t.Fatalf("skel out of date for %s\nrun: go generate ./internal/rocketclaw/skel", filepath.Base(p.dst))
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()

	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}")
	out, err := cmd.Output()
	require.NoError(t, err)

	return string(bytes.TrimSpace(out))
}
