package quickbench

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadProviderConfigEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quickbench.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"providers":{"openai":{"apiKey":"{{env.QB_TEST_KEY}}","baseURL":"https://example.test"}}}`), 0o644))
	t.Setenv("QB_TEST_KEY", "secret")

	providers, err := loadProviderConfig(path, []modelSelector{{Provider: "openai", Model: "gpt"}})
	require.NoError(t, err)
	assert.NotNil(t, providers.OpenAI)
}

func TestParseModelSelector(t *testing.T) {
	sel, err := parseModelSelector("gpt-5.4?reasoningEffort=low&verbosity=high")
	require.NoError(t, err)
	assert.Equal(t, "openai", sel.Provider)
	assert.Equal(t, "gpt-5.4", sel.Model)
	assert.Equal(t, "low", sel.ReasoningEffort)
	assert.Equal(t, "high", sel.Verbosity)
}
