package rocketcode

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStandaloneConfigAutoApprovePermissionsFromEnv(t *testing.T) {
	t.Run("empty leaves disabled", func(t *testing.T) {
		t.Setenv("ROCKETCODE_AUTO_APPROVE_PERMISSIONS", "")

		config, err := StandaloneConfigFromEnv()

		require.NoError(t, err)
		require.False(t, config.AutoApprovePermissions)
	})

	t.Run("non empty enables", func(t *testing.T) {
		t.Setenv("ROCKETCODE_AUTO_APPROVE_PERMISSIONS", "1")

		config, err := StandaloneConfigFromEnv()

		require.NoError(t, err)
		require.True(t, config.AutoApprovePermissions)
	})
}

func TestStandaloneProvidersFromEnvConfiguresOpenAI(t *testing.T) {
	providers, err := StandaloneProvidersFromEnv()

	require.NoError(t, err)
	require.NotNil(t, providers.OpenAI)
}

func TestLoadWorkspaceDefinitionsReportsAgentLoadErrors(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("agents", 0o755))
	require.NoError(t, root.Mkdir("skills", 0o755))
	require.NoError(t, root.WriteFile("agents/main.md", []byte("---\ndescription: Main\n---\nPrompt\n"), 0o644))

	_, _, cleanup, err := LoadWorkspaceDefinitions(root)
	defer cleanup()

	require.ErrorContains(t, err, "main.md: model: required non-empty string")
}
