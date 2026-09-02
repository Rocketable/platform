package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/stretchr/testify/require"
)

func TestRunServeReportsAppStartupError(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	configData := fmt.Sprintf(
		`{"workspace":%q,"database_url":"postgres://127.0.0.1:1/none?sslmode=disable","slack":{"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},"mcp_external":{"enabled":true,"listen_addr":"127.0.0.1:0"},"openai":{"api_key":"sk-test"}}`,
		workspace,
	)
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(configData), 0o600))

	err := runServe(nil)
	require.ErrorContains(t, err, "run rocketclaw")
	require.ErrorContains(t, err, "start rocketcode session service")
}

func TestRunServeReportsSlackStartupErrorWithCurrentConfig(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	configData := fmt.Sprintf(
		`{"workspace":%q,"database_url":"postgres://localhost/rocketclaw_test?sslmode=disable","slack":{"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},"openai":{"api_key":"sk-test"}}`,
		workspace,
	)
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(configData), 0o600))

	err := runServe(nil)
	require.ErrorContains(t, err, "run rocketclaw")
}

func TestServeRunErrorMapsRestartRequestToSupervisorExitCode(t *testing.T) {
	err := serveRunError(backend.ErrRestartRequested)
	require.ErrorIs(t, err, exitCodeError(255))
}
