package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"tailscale.com/derp/derpserver"
	"tailscale.com/net/netns"
	"tailscale.com/net/stun"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

func TestRunTS(t *testing.T) {
	prepareOAIConfig(t)

	assert.Contains(t, captureStdout(t, func() error { return runTS(nil) }), "rocketclaw ts login")
	require.EqualError(t, runTS([]string{"logout"}), `unknown ts command "logout"`)
	require.EqualError(t, runTS([]string{"login", "extra"}), "ts login takes no arguments")

	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(`{"web": {"tsnet": {"hostname": "Bad_Host"}}}`), 0o600))
	require.ErrorContains(t, runTS([]string{"login"}), "load runtime config: web.tsnet.hostname must use only a-z, 0-9 and -")
}

func TestTailnetLogin(t *testing.T) {
	control := &testcontrol.Server{DNSConfig: &tailcfg.DNSConfig{Proxied: true}, MagicDNSDomain: "tail-scale.ts.net", RequireAuth: true}
	useTestTailnet(t, control)

	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(`{"web": {"tsnet": {"hostname": "rocketclaw-test"}}}`), 0o600))

	tailnet := &config.TsnetConfig{Hostname: "rocketclaw-test"}

	_, err := startTailnet(t.Context(), tailnet, slog.New(slog.DiscardHandler))
	require.ErrorIs(t, err, errTailnetLogin)
	assert.NotContains(t, err.Error(), "/auth/", "the server never prints a login link")

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

	node, err := startTailnet(t.Context(), tailnet, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.Equal(t, "https://rocketclaw-test.tail-scale.ts.net", tailnet.Origin, "the logged-in node serves its HTTPS name")

	rt := &backend.Runtime{Cfg: config.NewLockedConfig(&config.Config{Web: config.WebConfig{Tsnet: tailnet}}), Log: slog.New(slog.DiscardHandler), Sessions: webSessions(t)}
	stop := startWebRPC(rt, node, &mockWebChannels{}, &mockWebRoutines{})
	require.NoError(t, stop(t.Context()))
	require.NoError(t, node.server.Close())
}

func TestTailnetNodeState(t *testing.T) {
	startTestTailnet(t)

	configDir, err := os.UserConfigDir()
	require.NoError(t, err)

	dir := filepath.Join(configDir, "rocketclaw", "tsnet-rocketclaw-test")
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.ModeDir|0o700, info.Mode())
	require.FileExists(t, filepath.Join(dir, "tailscaled.state"))
}

func TestTailnetStopRestartsRocketClaw(t *testing.T) {
	usePassingTailnet(t)

	workspace := t.TempDir()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)

	cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn, Web: config.WebConfig{Tsnet: &config.TsnetConfig{Hostname: "rocketclaw-test", AuthKey: "tskey-auth-test"}}}
	assembler := &processAssembler{}
	require.ErrorIs(t, backend.Run(ctx, cfg, slog.New(slog.DiscardHandler), new(slog.LevelVar), &FrontendAssemblerMock{
		GateFunc:           assembler.Gate,
		ValidateAssetsFunc: assembler.ValidateAssets,
		AssembleFunc: func(rt *backend.Runtime) (<-chan struct{}, []func(context.Context) error, error) {
			done, stops, err := assembler.Assemble(rt)
			require.NoError(t, assembler.tailnet.https.Close())

			return done, stops, err
		},
	}, sandbox.None{}), backend.ErrRestartRequested)
}

func useTestTailnet(t *testing.T, control *testcontrol.Server) {
	t.Helper()

	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })

	control.DERPMap, control.Logf = startTestDERP(t), logger.Discard
	control.HTTPTestServer = httptest.NewUnstartedServer(control)
	control.HTTPTestServer.Start()
	t.Cleanup(control.HTTPTestServer.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("TS_CONTROL_URL", control.HTTPTestServer.URL)
}

func usePassingTailnet(t *testing.T) {
	t.Helper()
	useTestTailnet(t, &testcontrol.Server{DNSConfig: &tailcfg.DNSConfig{Proxied: true}, MagicDNSDomain: "tail-scale.ts.net"})
}

func startTestTailnet(t *testing.T) *tailnetNode {
	t.Helper()
	usePassingTailnet(t)

	node, err := startTailnet(t.Context(), &config.TsnetConfig{Hostname: "rocketclaw-test", AuthKey: "tskey-auth-test"}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, node.server.Close()) })

	return node
}

func startTestDERP(t *testing.T) *tailcfg.DERPMap {
	t.Helper()

	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	var serving errgroup.Group
	serving.Go(func() error {
		var packet [1500]byte
		for {
			n, address, err := udp.ReadFromUDPAddrPort(packet[:])
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			if err != nil {
				return err
			}
			tx, err := stun.ParseBindingRequest(packet[:n])
			if err != nil {
				return err
			}
			if _, err := udp.WriteToUDPAddrPort(stun.Response(tx, address), address); err != nil && !errors.Is(err, net.ErrClosed) {
				return err
			}
		}
	})
	t.Cleanup(func() {
		require.NoError(t, udp.Close())
		require.NoError(t, serving.Wait())
	})

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
		Nodes: []*tailcfg.DERPNode{{Name: "t1", RegionID: 1, HostName: "127.0.0.1", IPv4: "127.0.0.1", IPv6: "none", STUNPort: udp.LocalAddr().(*net.UDPAddr).Port, DERPPort: derpPort, InsecureForTests: true}},
	}}}
}
