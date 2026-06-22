package rocketcode

import (
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

func TestStandaloneProvidersFromEnvConfiguresOpenAICompatibleProvider(t *testing.T) {
	for _, mode := range []OpenAICompatibleMode{OpenAICompatibleModeResponses, OpenAICompatibleModeChatCompletions} {
		t.Run(string(mode), func(t *testing.T) {
			t.Setenv("ROCKETCODE_OPENAI_COMPATIBLE_PROVIDER", "local")
			t.Setenv("ROCKETCODE_OPENAI_COMPATIBLE_API_KEY", "test-key")
			t.Setenv("ROCKETCODE_OPENAI_COMPATIBLE_BASE_URL", "http://127.0.0.1:1234")
			t.Setenv("ROCKETCODE_OPENAI_COMPATIBLE_MODE", string(mode))

			providers, err := StandaloneProvidersFromEnv()

			require.NoError(t, err)
			require.NotNil(t, providers.OpenAI)
			require.NotEmpty(t, providers.OpenAICompatible["local"].Client)
			require.Equal(t, mode, providers.OpenAICompatible["local"].Mode)
		})
	}
}

func TestStandaloneProvidersFromEnvIgnoresUnsetOpenAICompatibleConfig(t *testing.T) {
	providers, err := StandaloneProvidersFromEnv()

	require.NoError(t, err)
	require.NotNil(t, providers.OpenAI)
	require.Empty(t, providers.OpenAICompatible)
}

func TestStandaloneProvidersFromEnvRejectsPartialOpenAICompatibleConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{name: "provider", env: map[string]string{"ROCKETCODE_OPENAI_COMPATIBLE_PROVIDER": "local"}},
		{name: "api key", env: map[string]string{"ROCKETCODE_OPENAI_COMPATIBLE_API_KEY": "test-key"}},
		{name: "base url", env: map[string]string{"ROCKETCODE_OPENAI_COMPATIBLE_BASE_URL": "http://127.0.0.1:1234"}},
		{name: "mode", env: map[string]string{"ROCKETCODE_OPENAI_COMPATIBLE_MODE": "responses"}},
		{name: "missing mode", env: map[string]string{"ROCKETCODE_OPENAI_COMPATIBLE_PROVIDER": "local", "ROCKETCODE_OPENAI_COMPATIBLE_API_KEY": "test-key", "ROCKETCODE_OPENAI_COMPATIBLE_BASE_URL": "http://127.0.0.1:1234"}},
		{name: "missing base url", env: map[string]string{"ROCKETCODE_OPENAI_COMPATIBLE_PROVIDER": "local", "ROCKETCODE_OPENAI_COMPATIBLE_API_KEY": "test-key", "ROCKETCODE_OPENAI_COMPATIBLE_MODE": "responses"}},
		{name: "missing api key", env: map[string]string{"ROCKETCODE_OPENAI_COMPATIBLE_PROVIDER": "local", "ROCKETCODE_OPENAI_COMPATIBLE_BASE_URL": "http://127.0.0.1:1234", "ROCKETCODE_OPENAI_COMPATIBLE_MODE": "responses"}},
		{name: "missing provider", env: map[string]string{"ROCKETCODE_OPENAI_COMPATIBLE_API_KEY": "test-key", "ROCKETCODE_OPENAI_COMPATIBLE_BASE_URL": "http://127.0.0.1:1234", "ROCKETCODE_OPENAI_COMPATIBLE_MODE": "responses"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}

			_, err := StandaloneProvidersFromEnv()

			require.ErrorContains(t, err, "OpenAI-compatible provider environment is incomplete")
		})
	}
}

func TestStandaloneProvidersFromEnvRejectsInvalidOpenAICompatibleMode(t *testing.T) {
	t.Setenv("ROCKETCODE_OPENAI_COMPATIBLE_PROVIDER", "local")
	t.Setenv("ROCKETCODE_OPENAI_COMPATIBLE_API_KEY", "test-key")
	t.Setenv("ROCKETCODE_OPENAI_COMPATIBLE_BASE_URL", "http://127.0.0.1:1234")
	t.Setenv("ROCKETCODE_OPENAI_COMPATIBLE_MODE", "completions")

	_, err := StandaloneProvidersFromEnv()

	require.EqualError(t, err, "ROCKETCODE_OPENAI_COMPATIBLE_MODE must be responses or chat_completions")
}
