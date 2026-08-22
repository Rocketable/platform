package developmentmcp

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

func TestProductionGoDoesNotImportSlackOrExternalMCP(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		require.NoError(t, err)

		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			require.NoError(t, err)
			assert.NotContains(t, path, "slack")
			assert.NotContains(t, path, "externalmcp")
		}
	}
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
	gotThinking, gotAnswer, err := RunTryTurn(t.Context(), workspace, config.DefaultRuntimeDir, nil, cfg, slog.New(slog.DiscardHandler), new(harnessbridge.DevelopmentChat), "", []skel.OverlayFile{{
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
