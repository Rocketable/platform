package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	legacy := fmt.Appendf(nil, `{"workspace":".","database_url":%q,"slack":{"enabled":true,"bot_token":"xoxb","app_token":"xapp","human_user_id":"U123","room":"D123","social_mode":{"channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]}},"openai":{"api_key":"test"}}`, dsn)
	require.NoError(t, os.WriteFile(defaultConfigPath, legacy, 0o600))

	err = runServe([]string{"--pprof=false"})
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

func TestNewLoggerLevel(t *testing.T) {
	previous := slog.Default()

	t.Cleanup(func() { slog.SetDefault(previous) })

	level := new(slog.LevelVar)
	logger := newLogger(level)
	assert.Same(t, logger, slog.Default())

	for _, test := range []struct {
		text  string
		level slog.Level
	}{{"", slog.LevelDebug}, {" warning ", slog.LevelWarn}, {"ERROR", slog.LevelError}, {"bogus", slog.LevelDebug}} {
		backend.SetLogLevel(level, test.text)
		assert.True(t, logger.Enabled(t.Context(), test.level))
		assert.False(t, logger.Enabled(t.Context(), test.level-1))
	}
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

func TestSelectRuntimeConfigFileReportsStatErrors(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	require.NoError(t, os.Chmod(workspace, 0))
	t.Cleanup(func() { require.NoError(t, os.Chmod(workspace, 0o755)) })

	_, err := selectRuntimeConfigFile()
	require.ErrorContains(t, err, "stat")
	require.ErrorIs(t, err, os.ErrPermission)

	_, _, err = loadRuntimeConfig("")
	require.ErrorIs(t, err, os.ErrPermission)

	err = run(nil)
	require.ErrorContains(t, err, "stat config path")
	require.ErrorIs(t, err, os.ErrPermission)
}

func TestRunDispatchesSubcommandErrorsBeforeDefaultConfig(t *testing.T) {
	t.Chdir(t.TempDir())

	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "serve", args: []string{"run", "--bad-flag"}, want: "parse serve flags"},
		{name: "oai", args: []string{"oai", "bogus"}, want: `unknown oai command "bogus"`},
		{name: "lint flags", args: []string{"lint", "--bad-flag"}, want: "parse lint flags"},
		{name: "agent graph flags", args: []string{"agent-graph", "--bad-flag"}, want: "parse agent-graph flags"},
		{name: "lint config", args: []string{"lint", "current"}, want: "load config"},
		{name: "agent graph config", args: []string{"agent-graph", "current"}, want: "load config"},
		{name: "settings config", args: []string{"settings"}, want: "load config"},
		{name: "oai login config", args: []string{"oai", "login", "--headless"}, want: "load runtime config"},
		{name: "oai list config", args: []string{"oai", "list"}, want: "load runtime config"},
		{name: "oai logout config", args: []string{"oai", "logout"}, want: "load runtime config"},
		{name: "oai list flags", args: []string{"oai", "list", "--bad-flag"}, want: "parse oai list flags"},
		{name: "ts flags", args: []string{"ts", "login", "--bad-flag"}, want: "parse ts login flags"},
		{name: "ts config", args: []string{"ts", "login"}, want: "load runtime config"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := run(tt.args)
			require.ErrorContains(t, err, tt.want)
			if strings.HasSuffix(tt.name, " config") {
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

func TestRunDispatchesHelp(t *testing.T) {
	output := captureStdout(t, func() error { return run([]string{"help"}) })
	assert.Contains(t, output, "Usage:")
	assert.Contains(t, output, "rocketclaw run [--pprof=false]")
	assert.Contains(t, output, "127.0.0.1:6060")
	assert.Contains(t, output, "enabled by default")
	assert.NotContains(t, output, "rocketclaw cli")
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
