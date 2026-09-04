package main

import (
	"context"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/rpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func TestWebRPC(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	logger := slog.New(slog.DiscardHandler)
	sessions, err := backend.NewSessionServiceIn(dsn, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop(context.Background())) })

	rt := &backend.Runtime{Sessions: sessions, Cfg: &config.Config{WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}, Log: logger}

	t.Setenv("ROCKETCLAW_WEB_GRPC", "127.0.0.1:18790")

	_, err = startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.ErrorContains(t, err, "must be unix:")
	require.NoError(t, os.MkdirAll("../../.tmp", 0o700))

	dir, err := os.MkdirTemp("../../.tmp", "web-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })

	socketPath, err := filepath.Abs(filepath.Join(dir, "web.sock"))
	require.NoError(t, err)
	t.Setenv("ROCKETCLAW_WEB_GRPC", "unix:"+socketPath)
	require.NoError(t, os.Chmod(dir, 0o755))

	_, err = startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.ErrorContains(t, err, "private")
	require.NoError(t, os.Chmod(dir, 0o700))

	stop, err := startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.NoError(t, err)
	connection, err := grpc.NewClient("unix:"+socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))

	var response rpc.ListSessionEntriesResponse
	require.NoError(t, connection.Invoke(ctx, "/rpc.Web/ListSessionEntries", &rpc.SessionEntriesRequest{Id: "empty"}, &response))
	require.Empty(t, response.Entries)
	require.NoError(t, stop(ctx))

	_, err = os.Stat(socketPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	// Immediate shutdown is valid even before Serve's goroutine is scheduled.
	stop, err = startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.NoError(t, err)
	require.NoError(t, stop(ctx))
}
