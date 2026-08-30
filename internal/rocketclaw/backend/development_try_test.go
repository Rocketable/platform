package backend

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func TestLintTryCleanTreeHasNoRC003(t *testing.T) {
	result, err := LintTry(t.TempDir(), config.DefaultRuntimeDir, nil, "", []protocol.OverlayFile{
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
	result, err := LintTry(t.TempDir(), config.DefaultRuntimeDir, nil, "", []protocol.OverlayFile{
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

func TestRunTryTurnReturnsThinkingAndAnswer(t *testing.T) {
	const (
		thinking = "considering the try tree"
		answer   = "try tree final answer"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}

		var requestBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&requestBody); !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, err := fmt.Fprintf(w, `{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"rsn_1","type":"reasoning","summary":[{"type":"summary_text","text":%q}]},{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q,"annotations":[]}]}]}`, thinking, answer)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	workspace := t.TempDir()
	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}
	gotThinking, gotAnswer, err := RunTryTurn(t.Context(), workspace, config.DefaultRuntimeDir, nil, cfg, slog.New(slog.DiscardHandler), new(DevelopmentChat), "", []protocol.OverlayFile{{
		Path: "agents/main.md",
		Content: `---
description: Try Main
mode: primary
model: gpt-5.5
permission:
  read:
    "*": allow
---
TRY TREE AGENT PROMPT
`,
	}}, "main", "what do you see?")
	require.NoError(t, err)
	assert.Equal(t, thinking, gotThinking)
	assert.Equal(t, answer, gotAnswer)
}
