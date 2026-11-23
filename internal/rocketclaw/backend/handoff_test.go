package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateOneShot(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "primary", "---\nmodel: handoff-model\npermission:\n  bash: allow\n  rocketclaw:\n    rocketclaw_set_tag: [[triage, resolved]]\n---\nOriginal agent instructions\n")

	for _, text := range []string{"# Goal\nKeep the chosen behavior.\n\n# Remaining\nRun the tests.", ""} {
		t.Run(text, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Model string            `json:"model"`
					Tools []json.RawMessage `json:"tools"`
					Input json.RawMessage   `json:"input"`
				}

				data, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(data, &request); !assert.NoError(t, err) {
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}

				instructions := requestSystemPrompt(data)

				assert.Equal(t, "handoff-model", request.Model)
				assert.Empty(t, request.Tools)
				assert.NotContains(t, instructions, "rocketclaw_set_tag")
				assert.NotContains(t, instructions, "rocketclaw_get_tags")
				assert.NotContains(t, instructions, "Session Tags")
				assert.Contains(t, instructions, "Write the handoff.")
				assert.Contains(t, string(request.Input), "Source session: ordinary-session")
				assert.Contains(t, string(request.Input), "literal !`touch should-not-exist`")
				w.Header().Set("Content-Type", "application/json")
				writeRawRunMessage(t, w, "handoff", "message", text)
			}))
			defer server.Close()

			cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}

			document, err := GenerateOneShot(t.Context(), config.NewLockedConfig(cfg), "primary", "Write the handoff.", "Source session: ordinary-session\nUser: literal !`touch should-not-exist`")
			if text == "" {
				require.ErrorContains(t, err, "empty reply")
			} else {
				require.NoError(t, err)
				require.Equal(t, text, document)
			}

			require.NoFileExists(t, workspace+"/should-not-exist")
		})
	}
}

func TestGenerateOneShotFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, agent, message string
	}{
		{"missing workspace", "primary", "open workspace root"},
		{"invalid agent", "primary", "open workspace agent and skills"},
		{"missing agent", "missing", "prepare one-shot run"},
		{"blocked scratch", "primary", "create one-shot scratch"},
		{"provider failure", "primary", "generate one-shot reply"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeAgent(t, workspace, "primary", "---\nmodel: handoff-model\n---\nSummarize work.\n")

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "handoff unavailable", http.StatusBadRequest)
			}))
			defer server.Close()

			cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}

			switch test.name {
			case "missing workspace":
				cfg.Workspace = filepath.Join(workspace, "missing")
			case "invalid agent":
				writeAgent(t, workspace, "primary", "---\nmodel: [\n---\n")
			case "blocked scratch":
				require.NoError(t, os.MkdirAll(filepath.Join(workspace, cfg.RuntimeDirName()), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(workspace, cfg.RuntimeDirName(), ".rocketcode"), []byte("occupied"), 0o600))
			}

			document, err := GenerateOneShot(t.Context(), config.NewLockedConfig(cfg), test.agent, "Write the handoff.", "Source session: ordinary-session")
			require.ErrorContains(t, err, test.message)
			require.Empty(t, document)

			if test.name == "provider failure" || test.name == "missing agent" {
				scratch, err := filepath.Glob(filepath.Join(workspace, cfg.RuntimeDirName(), ".rocketcode", "one-shot-*"))
				require.NoError(t, err)
				require.Empty(t, scratch, "failed one-shot runs must clean their scratch directory")
			}
		})
	}
}
