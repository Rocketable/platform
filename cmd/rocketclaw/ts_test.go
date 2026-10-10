package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/derp/derpserver"
	"tailscale.com/net/netns"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

func TestRunTS(t *testing.T) {
	prepareOAIConfig(t) // Its configuration has no web.tsnet.

	assert.Contains(t, captureStdout(t, func() error { return runTS(nil) }), "rocketclaw ts login")
	require.EqualError(t, runTS([]string{"logout"}), `unknown ts command "logout"`)
	require.EqualError(t, runTS([]string{"login", "extra"}), "ts login takes no arguments")
	require.ErrorIs(t, runTS([]string{"login"}), errTailnetNotConfigured)
}

// TestTailnetLogin runs the Web's node against Tailscale's test control server, as
// tsnet's own tests do: the server never waits on a login, `ts login` prints the link
// and returns once it is completed, and the logged-in server then serves its HTTPS name.
func TestTailnetLogin(t *testing.T) {
	netns.SetEnabled(false) // As tsnet's tests: loopback control and DERP need no interface binding.
	t.Cleanup(func() { netns.SetEnabled(true) })

	control := &testcontrol.Server{
		DERPMap:        startTestDERP(t),
		DNSConfig:      &tailcfg.DNSConfig{Proxied: true},
		MagicDNSDomain: "tail-scale.ts.net",
		RequireAuth:    true,
		Logf:           logger.Discard,
	}
	control.HTTPTestServer = httptest.NewUnstartedServer(control)
	control.HTTPTestServer.Start()
	t.Cleanup(control.HTTPTestServer.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("TS_CONTROL_URL", control.HTTPTestServer.URL)

	prepareOAIConfig(t)

	var file map[string]any

	data, err := os.ReadFile(defaultConfigPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &file))

	file["web"] = map[string]any{"tsnet": map[string]any{"hostname": "rocketclaw-test"}}
	data, err = json.Marshal(file)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(defaultConfigPath, data, 0o640))

	var logs bytes.Buffer

	cfg := &config.Config{Web: config.WebConfig{ListenAddress: "127.0.0.1:0", Tsnet: &config.TsnetConfig{Hostname: "rocketclaw-test"}}}
	rt := &backend.Runtime{Cfg: config.NewLockedConfig(cfg), Log: slog.New(slog.NewTextHandler(&logs, nil))}

	stop, err := startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.NoError(t, err)
	require.NoError(t, stop(t.Context()))
	assert.Contains(t, logs.String(), "Tailscale authentication unavailable")
	assert.NotContains(t, logs.String(), "/auth/", "the server never prints a login link")

	reader, writer, err := os.Pipe()
	require.NoError(t, err)

	stdout := os.Stdout
	os.Stdout = writer

	t.Cleanup(func() { os.Stdout = stdout })

	done := make(chan error, 1)

	go func() { done <- runTS([]string{"login"}) }()

	lines := bufio.NewScanner(reader)
	for lines.Scan() {
		if line := strings.TrimSpace(lines.Text()); strings.Contains(line, "/auth/") {
			require.True(t, control.CompleteAuth(line), "ts login prints the login link")
			break
		}
	}

	require.NoError(t, <-done)
	require.NoError(t, writer.Close())

	os.Stdout = stdout

	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Contains(t, string(rest), "Logged in as rocketclaw-test.tail-scale.ts.net. Start RocketClaw")

	rt.Log = slog.New(slog.DiscardHandler)
	stop, err = startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return rt.Cfg.Clone().Web.Tsnet.Origin == "https://rocketclaw-test.tail-scale.ts.net"
	}, 30*time.Second, 50*time.Millisecond, "the logged-in node serves its HTTPS name")
	require.NoError(t, stop(t.Context()))
}

// startTestDERP serves a relay, as tsnet's tests do: a node counts as running only once
// it reaches one. STUN is off; the node needs no direct paths here.
func startTestDERP(t *testing.T) *tailcfg.DERPMap {
	t.Helper()

	relay := derpserver.New(key.NewNode(), logger.Discard)
	t.Cleanup(func() { _ = relay.Close() })

	server := httptest.NewTLSServer(derpserver.Handler(relay))
	t.Cleanup(server.Close)

	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)

	derpPort, err := strconv.Atoi(port)
	require.NoError(t, err)

	return &tailcfg.DERPMap{Regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{1: {
		RegionID: 1, RegionCode: "test",
		Nodes: []*tailcfg.DERPNode{{Name: "t1", RegionID: 1, HostName: "127.0.0.1", IPv4: "127.0.0.1", IPv6: "none", STUNPort: -1, DERPPort: derpPort, InsecureForTests: true}},
	}}}
}
