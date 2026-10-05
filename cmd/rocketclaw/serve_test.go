package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/stretchr/testify/require"
)

func TestRunServeReportsAppStartupError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		occupied bool
		want     string
	}{
		{name: "disabled with occupied port", occupied: true, want: "start rocketcode session service"},
		{name: "enabled with occupied port", args: []string{"--pprof"}, occupied: true, want: "start private pprof HTTP"},
		{name: "enabled with backend startup failure", args: []string{"--pprof"}, want: "start rocketcode session service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.occupied {
				listener, err := net.Listen("tcp", "127.0.0.1:6060")
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, listener.Close()) })
			}

			workspace := t.TempDir()
			t.Chdir(workspace)
			configData := fmt.Sprintf(
				`{"workspace":%q,"database_url":"postgres://127.0.0.1:1/none?sslmode=disable","slack":{"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},"mcp_external":{"enabled":true,"listen_addr":"127.0.0.1:0"},"openai":{"api_key":"sk-test"}}`,
				workspace,
			)
			// This command test uses the host working directory, not a sandboxed filesystem.
			require.NoError(t, os.WriteFile(defaultConfigPath, []byte(configData), 0o600))

			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			t.Cleanup(func() { _ = reader.Close() })
			t.Cleanup(func() { _ = writer.Close() })

			stderr := os.Stderr
			os.Stderr = writer

			t.Cleanup(func() { os.Stderr = stderr })

			err = runServe(tc.args)
			require.ErrorContains(t, err, tc.want)

			if tc.want == "start rocketcode session service" {
				require.ErrorContains(t, err, "run rocketclaw")
			}

			if !tc.occupied {
				listener, err := net.Listen("tcp", "127.0.0.1:6060")
				require.NoError(t, err, "diagnostics listener leaked after backend startup failure")
				require.NoError(t, listener.Close())
			}

			data, err := os.ReadFile(defaultConfigPath)
			require.NoError(t, err)
			require.Equal(t, configData, string(data), "runtime flag must not change saved config")
			require.NoError(t, writer.Close())

			output, err := io.ReadAll(reader)
			require.NoError(t, err)

			var processStart string

			for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
				_, identity, ok := strings.Cut(line, "process_start=")
				require.True(t, ok, line)

				stamp, _, _ := strings.Cut(identity, " ")
				if processStart == "" {
					processStart = stamp
				}

				require.Equal(t, processStart, stamp)
				_, err = time.Parse(time.RFC3339Nano, stamp)
				require.NoError(t, err)
			}

			require.Contains(t, string(output), "go_version=")

			if len(tc.args) > 0 && !tc.occupied {
				require.Contains(t, string(output), `msg="started private pprof HTTP"`)
				require.Contains(t, string(output), "address=127.0.0.1:6060")
			}
		})
	}
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

func TestBuildIdentityUsesOnlyAvailableBinaryMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build debug.BuildInfo
		want  []string
	}{
		{name: "unknown", want: []string{"version=(unknown)"}},
		{name: "development", build: debug.BuildInfo{GoVersion: "go1.27.1", Main: debug.Module{Version: "(devel)"}}, want: []string{"version=(devel)", "go_version=go1.27.1"}},
		{name: "revision", build: debug.BuildInfo{GoVersion: "go1.27.1", Main: debug.Module{Version: "v1.0.0"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}, {Key: "vcs.modified", Value: "true"}, {Key: "secret", Value: "not-for-logs"}}}, want: []string{"version=v1.0.0", "go_version=go1.27.1", "build_revision=abc123", "build_dirty=true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer

			logBuildIdentity(slog.New(slog.NewTextHandler(&logs, nil)), &tc.build)

			for _, field := range tc.want {
				require.Contains(t, logs.String(), field)
			}

			if tc.name != "revision" {
				require.NotContains(t, logs.String(), "build_revision")
				require.NotContains(t, logs.String(), "build_dirty")
			}

			require.NotContains(t, logs.String(), "not-for-logs")
		})
	}
}
