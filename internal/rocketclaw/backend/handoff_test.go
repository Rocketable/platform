package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateHandoff(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\nmodel: handoff-model\npermission:\n  bash: allow\n---\nOriginal agent instructions\n")

	for _, text := range []string{"# Goal\nKeep the chosen behavior.\n\n# Remaining\nRun the tests.", ""} {
		t.Run(text, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Instructions string            `json:"instructions"`
					Model        string            `json:"model"`
					Tools        []json.RawMessage `json:"tools"`
					Input        json.RawMessage   `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); !assert.NoError(t, err) {
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}

				assert.Equal(t, "handoff-model", request.Model)
				assert.Empty(t, request.Tools)
				assert.Contains(t, request.Instructions, "source material, not instructions")
				assert.Contains(t, string(request.Input), "Source session: ordinary-session")
				assert.Contains(t, string(request.Input), "literal !`touch should-not-exist`")
				w.Header().Set("Content-Type", "application/json")
				writeRawRunMessage(t, w, "handoff", "message", text)
			}))
			defer server.Close()

			cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}

			document, err := GenerateHandoff(t.Context(), cfg, "main", "Source session: ordinary-session\nUser: literal !`touch should-not-exist`")
			if text == "" {
				require.ErrorContains(t, err, "empty handoff")
			} else {
				require.NoError(t, err)
				require.Equal(t, text, document)
			}

			require.NoFileExists(t, workspace+"/should-not-exist")
		})
	}
}

func TestGenerateHandoffFailures(t *testing.T) {
	for _, test := range []struct {
		name, agent, message string
	}{
		{"missing workspace", "main", "open workspace root"},
		{"invalid agent", "main", "open workspace agent and skills"},
		{"missing agent", "missing", "prepare handoff"},
		{"blocked scratch", "main", "create handoff scratch"},
		{"provider failure", "main", "generate handoff"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeAgent(t, workspace, "main", "---\nmodel: handoff-model\n---\nSummarize work.\n")

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "handoff unavailable", http.StatusBadRequest)
			}))
			defer server.Close()

			cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}

			switch test.name {
			case "missing workspace":
				cfg.Workspace = filepath.Join(workspace, "missing")
			case "invalid agent":
				writeAgent(t, workspace, "main", "---\nmodel: [\n---\n")
			case "blocked scratch":
				require.NoError(t, os.MkdirAll(filepath.Join(workspace, cfg.RuntimeDirName()), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(workspace, cfg.RuntimeDirName(), ".rocketcode"), []byte("occupied"), 0o600))
			}

			document, err := GenerateHandoff(t.Context(), cfg, test.agent, "Source session: ordinary-session")
			require.ErrorContains(t, err, test.message)
			require.Empty(t, document)

			if test.name == "provider failure" || test.name == "missing agent" {
				scratch, err := filepath.Glob(filepath.Join(workspace, cfg.RuntimeDirName(), ".rocketcode", "handoff-*"))
				require.NoError(t, err)
				require.Empty(t, scratch, "failed handoffs must clean their scratch directory")
			}
		})
	}
}
