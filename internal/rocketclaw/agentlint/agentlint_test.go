package agentlint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLintFindings(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeAgent(t, runtimeRoot, "writer.md", `---
description: writer
permission:
  edit:
    "scripts/helper.sh": allow
  task:
    "executor": allow
---
writer
`)
	writeAgent(t, runtimeRoot, "executor.md", `---
description: executor
permission:
  bash:
    "scripts/helper.sh *": allow
---
executor
`)
	writeAgent(t, runtimeRoot, "browser.md", `---
description: browser
permission:
  webfetch: allow
  edit:
    "BROWSING_NOTES.md": allow
---
browser
`)
	writeAgent(t, runtimeRoot, "reader.md", `---
description: reader
permission:
  read:
    "BROWSING_NOTES.md": allow
---
reader
`)
	writeAgent(t, runtimeRoot, "loop.md", `---
description: loop
permission:
  task:
    "loop": allow
---
loop
`)
	writeAgent(t, runtimeRoot, "plural.md", `---
description: plural
permissions:
  bash:
    "echo ok": allow
---
plural
`)
	writeAgent(t, runtimeRoot, "same.md", `---
description: same
permission:
  read:
    "scripts/call.sh": allow
  edit:
    "scripts/call.sh": allow
  bash:
    "scripts/call.sh subcommand *": allow
---
same
`)

	result, err := Lint(runtimeRoot)
	require.NoError(t, err)
	assertFindingCodes(t, result.Findings, rc001, rc002, rc003, rc004, rc005, rc006)
}

func TestLintSuppressions(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeAgent(t, runtimeRoot, "same.md", `---
description: same
permission:
  edit:
    "scripts/call.sh": allow
  bash:
    "scripts/call.sh *": allow #nolint RC001: sandboxed
---
same
`)

	result, err := Lint(runtimeRoot)
	require.NoError(t, err)

	for _, finding := range result.Findings {
		assert.NotEqual(t, rc001, finding.Code)
	}
}

func TestLintSuppressionIsLocal(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeAgent(t, runtimeRoot, "same.md", `---
description: same
permission:
  edit:
    "scripts/allowed-risk.sh": allow
    "scripts/open-risk.sh": allow
  bash:
    "scripts/allowed-risk.sh *": allow #nolint RC001: sandboxed
    "scripts/open-risk.sh *": allow
---
same
`)

	result, err := Lint(runtimeRoot)
	require.NoError(t, err)

	foundUnsuppressed := false

	for _, finding := range result.Findings {
		if finding.Code == rc001 && strings.Contains(finding.Message, "open-risk.sh") {
			foundUnsuppressed = true
		}

		assert.False(t, finding.Code == rc001 && strings.Contains(finding.Message, "allowed-risk.sh"))
	}

	assert.True(t, foundUnsuppressed)
}

func TestLintReportsBadSuppressions(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeAgent(t, runtimeRoot, "bad.md", `---
description: bad
maxRecursion: -1 #nolint RC999: unknown
permission:
  task:
    "bad": allow #nolint:
---
bad
`)

	result, err := Lint(runtimeRoot)
	require.NoError(t, err)
	assertFindingCodes(t, result.Findings, rc000)
}

func writeAgent(t *testing.T, runtimeRoot, name, content string) {
	t.Helper()

	agentsRoot := filepath.Join(runtimeRoot, "agents")
	require.NoError(t, os.MkdirAll(agentsRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsRoot, name), []byte(content), 0o644))
}

func assertFindingCodes(t *testing.T, findings []Finding, codes ...string) {
	t.Helper()

	seen := map[string]bool{}
	for _, finding := range findings {
		seen[finding.Code] = true
	}

	for _, code := range codes {
		assert.Truef(t, seen[code], "missing finding code %s in %#v", code, findings)
	}
}
