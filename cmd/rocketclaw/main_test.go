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
	assert.Contains(t, output, "rocketclaw fc list")
	assert.Contains(t, output, "rocketclaw fc observe [--follow|-f] [conversation-id]")
	assert.Contains(t, output, "rocketclaw setup\n")
	assert.Contains(t, output, "rocketclaw setup files list\n")
	assert.Contains(t, output, "rocketclaw setup files get <path>\n")
	assert.Contains(t, output, "rocketclaw oai login [--headless]")
	assert.NotContains(t, output, "rocketclaw setup [flags]")
	assert.NotContains(t, output, "migrate-config")
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

func TestBuildInfoMainVersionReturnsVersion(t *testing.T) {
	assert.NotEmpty(t, buildInfoMainVersion())
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

func TestRunServeRejectsBadFlag(t *testing.T) {
	err := runServe([]string{"--bad-flag"})
	require.ErrorContains(t, err, "parse serve flags")
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
		{name: "doctor", args: []string{"doctor", "tts", "extra"}, want: "does not accept positional"},
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
	assert.NotContains(t, output, "migrate-config")
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

func TestRunDispatchesSetupAndFCHelp(t *testing.T) {
	output := captureStdout(t, func() error { return run([]string{"setup", "files", "list"}) })
	assert.Contains(t, output, "AGENTS.md")

	output = captureStdout(t, func() error { return run([]string{"fc"}) })
	assert.Contains(t, output, "rocketclaw fc list")
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
