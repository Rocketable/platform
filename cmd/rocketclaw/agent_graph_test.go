package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunAgentGraphCurrent(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeLintConfig(t, workspace)
	writeLintAgent(t, filepath.Join(workspace, ".rocketclaw"), "main.md", `---
description: main
maxRecursion: 0
permission:
  task:
    "worker": allow
---
main
`)
	writeLintAgent(t, filepath.Join(workspace, ".rocketclaw"), "worker.md", `---
description: worker
---
worker
`)

	output := captureStdout(t, func() error { return runAgentGraph([]string{"current"}) })
	assert.Equal(t, `digraph agent_graph {
  "main" [label="main\nmaxRecursion=0"];
  "worker" [label="worker\nmaxRecursion=unbounded"];
  "main" -> "worker";
}
`, output)
}

func TestRunAgentGraphDefaultUsesNext(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeLintConfig(t, workspace)
	writeLintAgent(t, workspace, "main.md", `---
description: main
---
main
`)

	output := captureStdout(t, func() error { return runAgentGraph(nil) })
	assert.Equal(t, `digraph agent_graph {
  "main" [label="main\nmaxRecursion=unbounded"];
}
`, output)
	_, err := os.Stat(filepath.Join(workspace, ".rocketclaw", "agents", "main.md"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunAgentGraphRejectsUnknownTarget(t *testing.T) {
	err := runAgentGraph([]string{"later"})
	require.ErrorContains(t, err, "usage: rocketclaw agent-graph [next|current]")
}

func TestHelpMentionsAgentGraph(t *testing.T) {
	output := captureStdout(t, func() error { return run([]string{"help"}) })
	assert.Contains(t, output, "rocketclaw agent-graph [next|current]")
}
