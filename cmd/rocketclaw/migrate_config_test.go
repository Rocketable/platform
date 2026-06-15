package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunMigrateConfigReadsStdinAndDoesNotRequireConfig(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)

	input := `{"slack":{"social_mode":{"enabled":true,"channel_agents":{"triage":"main"},"allowed_user_ids":["U123"]}}}`
	stdinFile, err := os.CreateTemp(t.TempDir(), "stdin-*.json")
	require.NoError(t, err)
	_, err = stdinFile.WriteString(input)
	require.NoError(t, err)
	_, err = stdinFile.Seek(0, 0)
	require.NoError(t, err)

	oldStdin := os.Stdin
	os.Stdin = stdinFile
	t.Cleanup(func() {
		os.Stdin = oldStdin
		require.NoError(t, stdinFile.Close())
	})

	output := captureStdout(t, func() error {
		return run([]string{"migrate-config"})
	})

	assert.Contains(t, output, `"channels"`)
	assert.Contains(t, output, `"channel": "#triage"`)
	assert.Contains(t, output, `"allowed_user_ids": [`)
	assert.NotContains(t, output, "channel_agents")

	_, err = os.Stat(filepath.Join(workspace, defaultConfigPath))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunMigrateConfigRejectsArguments(t *testing.T) {
	err := runMigrateConfig([]string{"extra"})
	require.EqualError(t, err, "usage: rocketclaw migrate-config < old.json > new.json")
}
