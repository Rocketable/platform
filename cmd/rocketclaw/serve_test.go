package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"cirello.io/pglock"
	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/tstest/integration/testcontrol"
)

func TestRunServeReportsAppStartupError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		disabled bool
		occupied bool
		want     string
	}{
		{name: "disabled with occupied port", disabled: true, occupied: true, want: "start rocketcode session service"},
		{name: "enabled with occupied port", occupied: true, want: "start private pprof HTTP"},
		{name: "enabled with backend startup failure", want: "start rocketcode session service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			diagnosticsAddress := listener.Addr().String()
			if tc.occupied {
				t.Cleanup(func() { require.NoError(t, listener.Close()) })
			} else {
				require.NoError(t, listener.Close())
			}

			args := []string{"--pprof=" + diagnosticsAddress}
			if tc.disabled {
				args = append(args, "--pprof=false")
			}

			workspace := t.TempDir()
			t.Chdir(workspace)
			configData := fmt.Sprintf(
				`{"workspace":%q,"web":{"tsnet":{}},"database_url":"postgres://127.0.0.1:1/none?sslmode=disable","slack":{"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},"mcp_external":{"enabled":true,"listen_addr":"127.0.0.1:0"},"openai":{"api_key":"sk-test"}}`,
				workspace,
			)
			require.NoError(t, os.WriteFile(defaultConfigPath, []byte(configData), 0o600))

			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			t.Cleanup(func() { _ = reader.Close() })
			t.Cleanup(func() { _ = writer.Close() })

			stderr := os.Stderr
			os.Stderr = writer

			t.Cleanup(func() { os.Stderr = stderr })

			err = runServe(args)
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

			if !tc.occupied {
				require.Contains(t, string(output), `msg="started private pprof HTTP"`)
				_, address, ok := strings.Cut(string(output), "address=")
				require.True(t, ok)
				address, _, _ = strings.Cut(address, "\n")
				require.Equal(t, diagnosticsAddress, address)

				listener, err := net.Listen("tcp", address)
				require.NoError(t, err, "diagnostics listener leaked after backend startup failure")
				require.NoError(t, listener.Close())
			} else {
				require.NotContains(t, string(output), `msg="started private pprof HTTP"`)
			}
		})
	}
}

func TestRunServeStopsAtTheTailnetGate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		control *testcontrol.Server
		authKey string
		want    string
	}{
		{name: "logged out", control: &testcontrol.Server{RequireAuth: true}, want: errTailnetLogin.Error()},
		{name: "key needs a login", control: &testcontrol.Server{RequireAuth: true}, authKey: "tskey-auth-test", want: errTailnetLogin.Error()},
		{name: "no HTTPS name", control: &testcontrol.Server{}, authKey: "tskey-auth-test", want: "no HTTPS name: enable MagicDNS and HTTPS certificates"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTestTailnet(t, tc.control)

			dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
			require.NoError(t, err)

			workspace := t.TempDir()
			t.Chdir(workspace)
			require.NoError(t, os.WriteFile(defaultConfigPath, fmt.Appendf(nil,
				`{"workspace":%q,"web":{"tsnet":{"auth_key":%q}},"database_url":%q,"openai":{"api_key":"sk-test"}}`,
				workspace, tc.authKey, dsn,
			), 0o600))

			err = runServe([]string{"--pprof=false"})
			require.ErrorContains(t, err, "start the Web: ")
			require.ErrorContains(t, err, tc.want)
			require.Equal(t, 1, exitCodeForError(err))

			db, err := sql.Open("pgx", dsn)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })

			var ledger *string
			require.NoError(t, db.QueryRowContext(t.Context(), `SELECT to_regclass('pg_migrations')::text`).Scan(&ledger))
			require.Nil(t, ledger, "a failed gate stops before migrations")

			client, err := pglock.UnsafeNew(db, pglock.WithCustomTable("rocketclaw_locks"), pglock.WithHeartbeatFrequency(0))
			require.NoError(t, err)
			lock, err := client.Acquire("rocketclaw-run", pglock.FailIfLocked())
			require.NoError(t, err, "a failed gate releases the run lock")
			require.NoError(t, lock.Close())
		})
	}
}

func TestRunServeReportsSlackStartupErrorWithCurrentConfig(t *testing.T) {
	serveFakeSlack(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/auth.test", r.URL.Path)
		assert.Equal(t, "xoxb", r.FormValue("token"))
		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`)
		assert.NoError(t, err)
	})
	usePassingTailnet(t)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	workspace := t.TempDir()
	t.Chdir(workspace)
	require.NoError(t, os.Mkdir("agents", 0o755))
	require.NoError(t, os.WriteFile(filepath.Join("agents", "ops.md"), []byte("---\ndescription: Ops\nmodel: gpt-6.1-sol\nsticky: slack\n---\n"), 0o600))
	configData := fmt.Sprintf(
		`{"workspace":%q,"web":{"tsnet":{"auth_key":"tskey-auth-test"}},"database_url":%q,"slack":{"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["ops"],"allowed_user_ids":["U123"]}]},"embedded_agents":{"voice":{"disabled":true}},"openai":{"api_key":"sk-test"}}`,
		workspace, dsn,
	)
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(configData), 0o600))

	err = runServe([]string{"--pprof=false"})
	require.EqualError(t, err, "run rocketclaw: assemble frontends: start Slack connector: slack auth test failed: invalid_auth")
	cfg, err := config.Load(defaultConfigPath, "", secretFetcher)
	require.NoError(t, err)
	agents, _, err := backend.LoadRuntimeDefinitions(config.NewLockedConfig(cfg), ".rocketclaw")
	require.NoError(t, err)
	require.Contains(t, agents.Items, "ops")
	for name, agent := range agents.Items {
		require.False(t, agent.CustomShell.Emulated, "Slack auth fixture must not start background sandbox preparation for agent %q", name)
	}
}

func serveFakeSlack(t *testing.T, handler http.HandlerFunc) {
	t.Helper()

	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)

	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == "slack.com:443" {
			address = server.Listener.Addr().String()
		}

		return new(net.Dialer).DialContext(ctx, network, address)
	}

	base := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = base })
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
		{name: "pseudo-version", build: debug.BuildInfo{GoVersion: "go1.27.1", Main: debug.Module{Version: "v0.0.0-20261123120000-0123456789ab"}}, want: []string{"version=v0.0.0-20261123120000-0123456789ab", "go_version=go1.27.1", "build_revision=0123456789ab", "build_dirty=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer

			revision, modified := logBuildIdentity(slog.New(slog.NewTextHandler(&logs, nil)), &tc.build)
			wantRevision, wantModified := buildRevision(&tc.build)
			require.Equal(t, []any{wantRevision, wantModified}, []any{revision, modified})

			for _, field := range tc.want {
				require.Contains(t, logs.String(), field)
			}

			if revision == "" {
				require.NotContains(t, logs.String(), "build_revision")
				require.NotContains(t, logs.String(), "build_dirty")
			}

			require.NotContains(t, logs.String(), "not-for-logs")
		})
	}
}

func TestBuildRevision(t *testing.T) {
	for _, tc := range []struct {
		name     string
		build    debug.BuildInfo
		revision string
		modified bool
	}{
		{name: "unknown"},
		{name: "development", build: debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}},
		{name: "vcs", build: debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261123120000-0123456789ab+dirty"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}, {Key: "vcs.modified", Value: "true"}}}, revision: "abc123", modified: true},
		{name: "pseudo-version", build: debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261123120000-0123456789ab"}}, revision: "0123456789ab"},
		{name: "pre-release pseudo-version", build: debug.BuildInfo{Main: debug.Module{Version: "v1.2.4-0.20261123120000-0123456789ab"}}, revision: "0123456789ab"},
		{name: "release", build: debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, revision: "v1.2.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			revision, modified := buildRevision(&tc.build)
			require.Equal(t, []any{tc.revision, tc.modified}, []any{revision, modified})
		})
	}
}
