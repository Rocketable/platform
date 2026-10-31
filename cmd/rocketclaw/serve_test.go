package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunServeReportsAppStartupError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		occupied bool
		want     string
	}{
		{name: "disabled with occupied port", args: []string{"--pprof=false"}, occupied: true, want: "start rocketcode session service"},
		{name: "enabled with occupied port", occupied: true, want: "start private pprof HTTP"},
		{name: "enabled with backend startup failure", want: "start rocketcode session service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diagnosticsAddress := "127.0.0.1:0"
			if tc.occupied {
				listener, err := net.Listen("tcp", diagnosticsAddress)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, listener.Close()) })
				diagnosticsAddress = listener.Addr().String()
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

			err = runServe(tc.args, diagnosticsAddress)
			require.ErrorContains(t, err, tc.want)

			if tc.want == "start rocketcode session service" {
				require.ErrorContains(t, err, "run rocketclaw")
			} else {
				require.EqualError(t, err, "start private pprof HTTP: listen tcp "+diagnosticsAddress+": bind: address already in use")
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

			if len(tc.args) == 0 && !tc.occupied {
				require.Contains(t, string(output), `msg="started private pprof HTTP"`)
				_, address, ok := strings.Cut(string(output), "address=")
				require.True(t, ok)
				address, _, _ = strings.Cut(address, "\n")
				require.True(t, strings.HasPrefix(address, "127.0.0.1:"), address)
				require.NotEqual(t, "127.0.0.1:0", address)

				listener, err := net.Listen("tcp", address)
				require.NoError(t, err, "diagnostics listener leaked after backend startup failure")
				require.NoError(t, listener.Close())
			} else {
				require.NotContains(t, string(output), `msg="started private pprof HTTP"`)
			}
		})
	}
}

func TestRunServeReportsSlackStartupErrorWithCurrentConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/auth.test", r.URL.Path)
		assert.Equal(t, "xoxb", r.FormValue("token"))
		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	t.Setenv("ROCKETCLAW_SLACK_API_URL", server.URL+"/")
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	workspace := t.TempDir()
	t.Chdir(workspace)
	configData := fmt.Sprintf(
		`{"workspace":%q,"database_url":%q,"slack":{"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},"openai":{"api_key":"sk-test"}}`,
		workspace, dsn,
	)
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(configData), 0o600))

	err = runServe([]string{"--pprof=false"}, "127.0.0.1:0")
	require.EqualError(t, err, "run rocketclaw: assemble frontends: start Slack connector: slack auth test failed: invalid_auth")
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
