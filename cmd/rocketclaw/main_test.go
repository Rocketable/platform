package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunWithoutDefaultConfigShowsHelp(t *testing.T) {
	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(cwd))
	})

	output := captureStdout(t, func() error {
		return run(nil)
	})
	assert.Contains(t, output, "Usage:")
	assert.Contains(t, output, "rocketclaw setup\n")
	assert.Contains(t, output, "rocketclaw setup files list\n")
	assert.Contains(t, output, "rocketclaw setup files get <path>\n")
	assert.Contains(t, output, "rocketclaw oai login [provider] [--headless]")
	assert.Contains(t, output, "rocketclaw oai list")
	assert.Contains(t, output, "rocketclaw oai logout [provider]")
	assert.NotContains(t, output, "rocketclaw setup [flags]")
}

func TestMainWithoutDefaultConfigShowsHelp(t *testing.T) {
	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(cwd))
	})

	args := os.Args
	os.Args = []string{"rocketclaw"}

	t.Cleanup(func() { os.Args = args })

	output := captureStdout(t, func() error {
		main()
		return nil
	})
	assert.Contains(t, output, "Usage:")
}

func TestRunWithDefaultConfigAttemptsServe(t *testing.T) {
	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(cwd))
	})

	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(`{`), 0o600))

	err = run(nil)
	require.ErrorContains(t, err, "load config")
}

func TestRunWithLegacyConfigAttemptsServe(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	require.NoError(t, os.WriteFile(legacyConfigPath, []byte(`{`), 0o600))

	err := run(nil)
	require.ErrorContains(t, err, "load config")
}

func TestRunServeKeepsLegacyConfigReadOnly(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	legacy := []byte(`{"workspace":".","database_url":"postgres://localhost/rocketclaw_test?sslmode=disable","slack":{"enabled":true,"bot_token":"xoxb","app_token":"xapp","human_user_id":"U123","room":"D123","social_mode":{"channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]}},"openai":{"api_key":"test"}}`)
	require.NoError(t, os.WriteFile(defaultConfigPath, legacy, 0o600))

	err := runServe(nil)
	require.ErrorContains(t, err, "slack.channels is required")

	data, err := os.ReadFile(defaultConfigPath)
	require.NoError(t, err)
	assert.Equal(t, legacy, data)
}

func TestLoadRuntimeConfigKeepsLegacyConfigReadOnly(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	legacy := []byte(`{"workspace":".","database_url":"postgres://localhost/rocketclaw_test?sslmode=disable","slack":{"enabled":true,"bot_token":"xoxb","app_token":"xapp","social_mode":{"channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]}},"openai":{"api_key":"test"}}`)
	require.NoError(t, os.WriteFile(defaultConfigPath, legacy, 0o600))

	_, _, err := loadRuntimeConfig("")
	require.ErrorContains(t, err, "slack.channels is required")

	data, err := os.ReadFile(defaultConfigPath)
	require.NoError(t, err)
	assert.Equal(t, legacy, data)
}

func TestExitCodeForError(t *testing.T) {
	assert.Equal(t, 255, exitCodeForError(exitCodeError(255)))
	assert.Empty(t, exitCodeError(255).Error())
	assert.Equal(t, 255, exitCodeError(255).ExitCode())
	assert.Equal(t, 1, exitCodeForError(errors.New("boom")))
}

func TestParseLogLevel(t *testing.T) {
	assert.Equal(t, slog.LevelDebug, parseLogLevel(""))
	assert.Equal(t, slog.LevelWarn, parseLogLevel(" warning "))
	assert.Equal(t, slog.LevelError, parseLogLevel("ERROR"))
	assert.Equal(t, slog.LevelDebug, parseLogLevel("bogus"))
}

func TestRunServeRequiresConfig(t *testing.T) {
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

func TestMissingFileReportsStatErrors(t *testing.T) {
	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "file")
	require.NoError(t, os.WriteFile(filePath, []byte("not a directory"), 0o600))

	missing, err := missingFile(filepath.Join(filePath, "child"))
	require.ErrorContains(t, err, "stat")
	assert.False(t, missing)
}

func TestRunDispatchesSubcommandErrorsBeforeDefaultConfig(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "serve", args: []string{"run", "--bad-flag"}, want: "parse serve flags"},
		{name: "doctor", args: []string{"doctor", "--bad"}, want: "parse doctor flags"},
		{name: "oai", args: []string{"oai", "bogus"}, want: `unknown oai command "bogus"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := run(tt.args)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestRunDispatchesHelp(t *testing.T) {
	output := captureStdout(t, func() error { return run([]string{"help"}) })
	assert.Contains(t, output, "Usage:")
	assert.NotContains(t, output, "rocketclaw cli")
}

func TestPrintStdoutReportsWriteError(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })

	oldStdout := os.Stdout
	os.Stdout = writer

	t.Cleanup(func() { os.Stdout = oldStdout })

	require.NoError(t, writer.Close())

	err = printStdout("hello", "greeting")
	require.ErrorContains(t, err, "print greeting")
}

func TestRunDispatchesSetupHelp(t *testing.T) {
	output := captureStdout(t, func() error { return run([]string{"setup", "files", "list"}) })
	assert.Contains(t, output, "main-update-cortex.sh")
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	require.NoError(t, err)

	oldStdout := os.Stdout
	os.Stdout = writer

	defer func() {
		os.Stdout = oldStdout
	}()

	errCall := fn()

	require.NoError(t, writer.Close())

	output, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.NoError(t, errCall)

	return string(output)
}
