package developmentmcp

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

func TestLintTryCleanTreeHasNoRC003(t *testing.T) {
	result, err := LintTry(t.TempDir(), config.DefaultRuntimeDir, nil, "", []skel.OverlayFile{
		{Path: "agents/ok.md", Content: `---
description: ok
model: gpt-5.5
---
ok
`},
		{Path: "agents/other.md", Content: `---
description: other
model: gpt-5.5
---
other
`},
	}, new(config.Config), slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	for _, finding := range result.Findings {
		assert.NotEqual(t, "RC003", finding.Code)
	}
}

func TestLintTryCyclicPairReportsRC003(t *testing.T) {
	result, err := LintTry(t.TempDir(), config.DefaultRuntimeDir, nil, "", []skel.OverlayFile{
		{Path: "agents/a.md", Content: `---
description: a
model: gpt-5.5
permission:
  task:
    "b": allow
---
a
`},
		{Path: "agents/b.md", Content: `---
description: b
model: gpt-5.5
permission:
  task:
    "a": allow
---
b
`},
	}, new(config.Config), slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	found := false

	for _, finding := range result.Findings {
		if finding.Code == "RC003" {
			found = true
		}
	}

	assert.True(t, found)
}
