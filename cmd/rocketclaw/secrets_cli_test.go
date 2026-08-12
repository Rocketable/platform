package main

import (
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecretsARN = "arn:aws:secretsmanager:us-east-1:123456789012:secret:femto"

type testSecrets map[string]string

func (s testSecrets) SecretString(arn string) (string, error) {
	body, ok := s[arn]
	if !ok {
		return "", &config.SecretFetchError{ARN: arn}
	}
	return body, nil
}

func useTestSecrets(t *testing.T, secrets testSecrets) {
	t.Helper()
	previous := secretFetcher
	secretFetcher = secrets
	t.Cleanup(func() { secretFetcher = previous })
}

func TestDoctorAndServeResolveTheSameSecret(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(`{
		"workspace": ".",
		"openai": {"api_key": "test-key"},
		"slack": {"bot_token":"xoxb-local","app_token":"xapp-test","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]}
	}`), 0o600))
	useTestSecrets(t, testSecrets{testSecretsARN: `{"slack":{"bot_token":"xoxb-secret"}}`})

	_, doctorCfg, err := loadRuntimeConfig(testSecretsARN)
	require.NoError(t, err)
	serveCfg, err := config.Load(defaultConfigPath, testSecretsARN, secretFetcher)
	require.NoError(t, err)
	assert.Equal(t, doctorCfg.Slack.BotToken, serveCfg.Slack.BotToken)
	assert.Equal(t, "xoxb-secret", doctorCfg.Slack.BotToken)
}

func TestCommandsAcceptSecretsARNFlag(t *testing.T) {
	useTestSecrets(t, testSecrets{testSecretsARN: `{}`})

	t.Run("oai list", func(t *testing.T) {
		prepareOAIConfig(t)
		require.NoError(t, runOAIList([]string{"--aws-secrets-manager-arn", testSecretsARN}))
	})
	t.Run("lint", func(t *testing.T) {
		workspace := t.TempDir()
		t.Chdir(workspace)
		writeLintConfig(t, workspace)
		writeLintAgent(t, filepath.Join(workspace, ".rocketclaw"), "main.md", "---\ndescription: main\n---\nmain\n")
		require.NoError(t, runLint([]string{"--aws-secrets-manager-arn", testSecretsARN, "current"}))
	})
	t.Run("fc", func(t *testing.T) {
		workspace := t.TempDir()
		t.Chdir(workspace)
		require.NoError(t, os.WriteFile(defaultConfigPath, []byte(fcTestConfigJSON()), 0o600))
		require.NoError(t, runFC([]string{"list", "--aws-secrets-manager-arn", testSecretsARN}))
	})
	t.Run("agent-graph", func(t *testing.T) {
		workspace := t.TempDir()
		t.Chdir(workspace)
		writeLintConfig(t, workspace)
		writeLintAgent(t, filepath.Join(workspace, ".rocketclaw"), "main.md", "---\ndescription: main\n---\nmain\n")
		require.NoError(t, runAgentGraph([]string{"--aws-secrets-manager-arn", testSecretsARN, "current"}))
	})
	t.Run("oai logout", func(t *testing.T) {
		prepareOAIConfig(t)
		require.NoError(t, runOAILogout([]string{"--aws-secrets-manager-arn", testSecretsARN}))
	})
	t.Run("oai login", func(t *testing.T) {
		prepareOAIConfig(t)
		mockDeviceLogin(t, "work-refresh")
		synctest.Test(t, func(t *testing.T) {
			require.NoError(t, runOAILogin([]string{"--aws-secrets-manager-arn", testSecretsARN, "work", "--headless"}))
		})
	})
	t.Run("exec", func(t *testing.T) {
		workspace := t.TempDir()
		t.Chdir(workspace)
		writeLintConfig(t, workspace)
		writeLintAgent(t, filepath.Join(workspace, config.DefaultRuntimeDir), "main.md", "---\n---\nMain agent.\n")
		err := runExecIn(t.Context(), []string{"--aws-secrets-manager-arn", testSecretsARN, "missing", "do it"}, os.Stdout, execRunnerNotCalled(t))
		require.ErrorContains(t, err, `unknown agent "missing"`)
	})
}

func TestOAILoginWriteKeepsAWSObject(t *testing.T) {
	prepareOAIConfig(t)
	patched := []byte(`{
  "workspace": "../runtime",
  "openai": {"rocketcode_auth":"api_key","api_key":"openai-key"},
  "providers": {
    "work": {"rocketcode_auth":"api_key","api_key":"work-key"}
  },
  "slack": {
    "bot_token": {"aws":{"arn":"` + testSecretsARN + `","key":"token"}},
    "app_token": "xapp",
    "channels": [{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]
  }
}`)
	require.NoError(t, os.WriteFile(defaultConfigPath, patched, 0o640))
	useTestSecrets(t, testSecrets{testSecretsARN: `{"token":"LEAKED-SECRET-VALUE"}`})
	mockDeviceLogin(t, "work-refresh")

	synctest.Test(t, func(t *testing.T) {
		require.NoError(t, runOAILogin([]string{"work", "--headless"}))
	})

	data, err := os.ReadFile(defaultConfigPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"aws"`)
	assert.NotContains(t, string(data), "LEAKED-SECRET-VALUE")
}

func TestDoctorOutputOmitsResolvedSecret(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(`{
		"workspace": ".",
		"openai": {"api_key": "test-key"},
		"slack": {
		  "bot_token": {"aws":{"arn":"`+testSecretsARN+`","key":"token"}},
		  "app_token": "xapp-test",
		  "channels": [{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]
		}
	}`), 0o600))
	useTestSecrets(t, testSecrets{testSecretsARN: `{"token":"LEAKED-SECRET-VALUE"}`})

	output := captureStdout(t, func() error {
		return runDoctor([]string{"--aws-secrets-manager-arn", testSecretsARN})
	})
	assert.NotContains(t, output, "LEAKED-SECRET-VALUE")
	assert.Contains(t, output, "Configuration: OK")
}

func TestServeErrorOmitsResolvedSecret(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(`{
		"workspace": ".",
		"openai": {"api_key": "test-key"},
		"slack": {
		  "bot_token": {"aws":{"arn":"`+testSecretsARN+`","key":"token"}},
		  "app_token": "xapp-test",
		  "channels": [{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]
		}
	}`), 0o600))
	useTestSecrets(t, testSecrets{testSecretsARN: `{"token":"LEAKED-SECRET-VALUE"}`})

	err := runServe([]string{"--aws-secrets-manager-arn", testSecretsARN})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "LEAKED-SECRET-VALUE")
}
