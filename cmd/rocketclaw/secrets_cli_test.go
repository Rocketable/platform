package main

import (
	"context"
	"os"
	"testing"
	"testing/synctest"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecretsARN = "arn:aws:secretsmanager:us-east-1:123456789012:secret:femto"

type testSecrets map[string]string

func (s testSecrets) SecretString(_ context.Context, arn string) (string, error) {
	body, ok := s[arn]
	if !ok {
		return "", &config.SecretFetchError{ARN: arn}
	}

	return body, nil
}

func TestRuntimeAndServeResolveTheSameSecret(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(`{
		"workspace": ".", "web": {"tsnet": {}}, "database_url": "postgres://localhost/rocketclaw_test?sslmode=disable",
		"openai": {"api_key": "test-key"},
		"slack": {"bot_token":"xoxb-local","app_token":"xapp-test","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]}
	}`), 0o600))

	secrets := testSecrets{testSecretsARN: `{"slack":{"bot_token":"xoxb-secret"}}`}

	_, runtimeCfg, err := loadRuntimeConfig(testSecretsARN, "", secrets)
	require.NoError(t, err)
	serveCfg, err := config.Load(defaultConfigPath, testSecretsARN, secrets)
	require.NoError(t, err)
	assert.Equal(t, runtimeCfg.Slack.BotToken, serveCfg.Slack.BotToken)
	assert.Equal(t, "xoxb-secret", runtimeCfg.Slack.BotToken)
}

func TestSecretAloneConfiguresRuntime(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)

	secrets := testSecrets{testSecretsARN: `{
		"web": {"tsnet": {}}, "database_url": "postgres://localhost/rocketclaw_test?sslmode=disable",
		"openai": {"api_key": "test-key"}
	}`}

	selected, cfg, err := loadRuntimeConfig(testSecretsARN, "", secrets)
	require.NoError(t, err)
	assert.Equal(t, defaultConfigPath, selected.Path)
	assert.Equal(t, workspace, cfg.Workspace)
	assert.Equal(t, config.DefaultRuntimeDir, cfg.WorkDir)
	assert.Equal(t, "test-key", cfg.OpenAI.APIKey)

	_, _, err = loadRuntimeConfig("", "", secrets)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestUsersSecretReplacesExternalMCPUsers(t *testing.T) {
	const (
		usersARN     = "arn:aws:secretsmanager:us-east-1:123456789012:secret:users"
		configSecret = `{
			"web": {"tsnet": {}}, "database_url": "postgres://localhost/rocketclaw_test?sslmode=disable",
			"openai": {"api_key": "test-key"},
			"mcp_external": {"users": {"config-user": "config-pw"}}
		}`
	)

	t.Chdir(t.TempDir())

	_, cfg, err := loadRuntimeConfig(testSecretsARN, usersARN, testSecrets{testSecretsARN: configSecret, usersARN: `{"alice":"pw-a","bob":"pw-b"}`})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"alice": "pw-a", "bob": "pw-b"}, cfg.MCPExternal.Users)

	for _, body := range []string{`["LEAKED-SECRET-VALUE"]`, `{"alice":{"pw":"LEAKED-SECRET-VALUE"}}`, `null`} {
		err := runServe([]string{"--aws-secrets-manager-arn", testSecretsARN, "--aws-secrets-manager-users-arn", usersARN}, testSecrets{testSecretsARN: configSecret, usersARN: body})
		require.ErrorContains(t, err, "--aws-secrets-manager-users-arn "+usersARN)
		assert.NotContains(t, err.Error(), "LEAKED-SECRET-VALUE")
	}
}

func TestCommandsAcceptSecretsARNFlag(t *testing.T) {
	secrets := testSecrets{testSecretsARN: `{}`}

	t.Run("oai list", func(t *testing.T) {
		prepareOAIConfig(t)
		require.NoError(t, runOAIList([]string{"--aws-secrets-manager-arn", testSecretsARN}, secrets))
	})
	t.Run("oai logout", func(t *testing.T) {
		prepareOAIConfig(t)
		require.NoError(t, runOAILogout([]string{"--aws-secrets-manager-arn", testSecretsARN}, secrets))
	})
	t.Run("oai login", func(t *testing.T) {
		prepareOAIConfig(t)
		mockDeviceLogin(t, "work-refresh")
		synctest.Test(t, func(t *testing.T) {
			require.NoError(t, runOAILogin([]string{"--aws-secrets-manager-arn", testSecretsARN, "work", "--headless"}, secrets))
		})
	})
}

func TestOAILoginWriteKeepsAWSObject(t *testing.T) {
	prepareOAIConfig(t)

	patched := []byte(`{
  "workspace": "../runtime",
  "web": {"tsnet": {}},
  "database_url": "postgres://localhost/rocketclaw_test?sslmode=disable",
  "openai": {"auth":"api_key","api_key":"openai-key"},
  "providers": {
    "work": {"auth":"api_key","api_key":"work-key"}
  },
  "slack": {
    "bot_token": {"aws":{"arn":"` + testSecretsARN + `","key":"token"}},
    "app_token": "xapp",
    "channels": [{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]
  }
}`)
	require.NoError(t, os.WriteFile(defaultConfigPath, patched, 0o640))
	mockDeviceLogin(t, "work-refresh")

	synctest.Test(t, func(t *testing.T) {
		require.NoError(t, runOAILogin([]string{"work", "--headless"}, testSecrets{testSecretsARN: `{"token":"LEAKED-SECRET-VALUE"}`}))
	})

	data, err := os.ReadFile(defaultConfigPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"aws"`)
	assert.NotContains(t, string(data), "LEAKED-SECRET-VALUE")
}

func TestServeErrorOmitsResolvedSecret(t *testing.T) {
	usePassingTailnet(t)

	workspace := t.TempDir()
	t.Chdir(workspace)
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(`{
		"workspace": ".", "web": {"tsnet": {"auth_key": "tskey-auth-test"}}, "database_url": "postgres://localhost/rocketclaw_test?sslmode=disable",
		"openai": {"api_key": "test-key"},
		"slack": {
		  "bot_token": {"aws":{"arn":"`+testSecretsARN+`","key":"token"}},
		  "app_token": "xapp-test",
		  "channels": [{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]
		}
	}`), 0o600))
	err := runServe([]string{"--aws-secrets-manager-arn", testSecretsARN, "--pprof=127.0.0.1:0"}, testSecrets{testSecretsARN: `{"token":"LEAKED-SECRET-VALUE"}`})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "LEAKED-SECRET-VALUE")
}
