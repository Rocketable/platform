package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunDoctorReportsRuntime(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)

	require.NoError(t, os.WriteFile(filepath.Join(workspace, defaultConfigPath), []byte(`{
		"workspace": ".",
		"openai": {"api_key": "test-key"},
		"slack": {"bot_token": "xoxb-test", "app_token": "xapp-test", "channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
		"mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`), 0o600))

	output := captureStdout(t, func() error { return runDoctor(nil) })
	require.Contains(t, output, "Configuration: OK (rocketclaw.json)")
	require.Contains(t, output, "Workspace: "+workspace)
	require.Contains(t, output, "Work directory: .rocketclaw")
	require.Contains(t, output, "Slack: active")
	require.Contains(t, output, "External MCP: true")
	require.Contains(t, output, "RocketCode: OK (library)")
}

func TestRunDoctorReportsLegacyConfigAndWorkDir(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)

	require.NoError(t, os.WriteFile(filepath.Join(workspace, defaultConfigPath), []byte(`{
		"workspace": ".",
		"openai": {"api_key": "rocket-key"},
		"slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
		"mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, legacyConfigPath), []byte(`{
		"workspace": ".",
		"openai": {"api_key": "legacy-key"},
		"slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
		"mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`), 0o600))

	output := captureStdout(t, func() error { return runDoctor(nil) })
	require.Contains(t, output, "Configuration: OK (femtoclaw.json)")
	require.Contains(t, output, "Work directory: .femtoclaw")
}

func TestRunDoctorRejectsBadFlagBeforeConfigLoad(t *testing.T) {
	require.ErrorContains(t, runDoctor([]string{"--bad"}), "parse doctor flags")
}

func TestRunDoctorReportsConfigLoadError(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)

	require.ErrorContains(t, runDoctor(nil), "load config")
}

func TestRunDoctorReportsOutputWriteError(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, defaultConfigPath), []byte(`{
		"workspace": ".",
		"openai": {"api_key": "test-key"},
		"slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
		"mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`), 0o600))

	closeStdoutForTest(t)

	err := runDoctor(nil)
	require.ErrorContains(t, err, "write doctor output")
}

func closeStdoutForTest(t *testing.T) {
	t.Helper()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })

	oldStdout := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = oldStdout })
	require.NoError(t, writer.Close())
}
