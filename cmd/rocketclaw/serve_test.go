package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunServeReportsAppStartupError(t *testing.T) {
	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(cwd))
	})

	workspaceFile := filepath.Join(workspace, "workspace-file")
	require.NoError(t, os.WriteFile(workspaceFile, []byte("not a directory"), 0o600))
	configData := fmt.Sprintf(
		`{"workspace":%q,"mcp_external":{"enabled":true,"listen_addr":"127.0.0.1:0"},"openai":{"api_key":"sk-test"}}`,
		workspaceFile,
	)
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(configData), 0o600))

	err = runServe(nil)
	require.ErrorContains(t, err, "run rocketclaw")
	require.ErrorContains(t, err, "start rocketcode session service")
}

func TestRunServeRejectsBadFlagBeforeConfigLoad(t *testing.T) {
	require.ErrorContains(t, runServe([]string{"--bad"}), "parse serve flags")
}

func TestRunServeReportsConfigLoadError(t *testing.T) {
	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(cwd))
	})

	err = runServe(nil)
	require.ErrorContains(t, err, "load config")
}
