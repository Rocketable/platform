package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestSidebarSendFailures(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })
	cfg := &config.Config{Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	root, err := os.OpenRoot(cfg.Workspace)

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents"), 0o700))
	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "main.md"), []byte("---\nmodel: gpt-5.5\n---\nHelp."), 0o600))
	listener, err := Listen(testSocketPath(t))
	require.NoError(t, err)

	// This test-only limit rejects both the owner-bearing terminal and a session row.
	transport := grpc.NewServer(grpc.MaxSendMsgSize(1))
	New(&mockBackend{}, sessions, cfg, &mockChannels{}, &mockCronJobs{}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error {
		if err := transport.Serve(listener); err != nil {
			return fmt.Errorf("serve limited test RPC: %w", err)
		}

		return nil
	})
	t.Cleanup(func() {
		transport.Stop()
		require.NoError(t, serving.Wait())
	})

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))

	for _, tc := range []struct {
		name     string
		response *ListSessionsResponse
	}{
		{"empty completion", &ListSessionsResponse{Owner: "alice", UpstreamSuccess: true, SummariesComplete: true}},
		{"session row", &ListSessionsResponse{Owner: "alice", Sessions: []*Session{{Id: "web", Agent: "main", AllowedAgents: []string{"main"}}}, SummariesComplete: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "session row" {
				require.NoError(t, sessions.UpsertThread("web", backend.ThreadState{Agent: "main"}))
			}

			stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/ListSessions")
			require.NoError(t, err)
			require.NoError(t, stream.SendMsg(&ListSessionsRequest{}))
			require.NoError(t, stream.CloseSend())

			var batch ListSessionsResponse

			err = stream.RecvMsg(&batch)
			require.Equal(t, codes.ResourceExhausted, status.Code(err))
			require.Equal(t, fmt.Sprintf("trying to send message larger than max (%d vs. 1)", proto.Size(tc.response)), status.Convert(err).Message())
			require.Empty(t, batch.Sessions)
			require.False(t, batch.UpstreamSuccess)
		})
	}
}

func TestSidebarHTTPStreamsBeforeGoTailAndCancels(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })
	cfg := &config.Config{Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	root, err := os.OpenRoot(cfg.Workspace)

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents"), 0o700))
	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "main.md"), []byte("---\nmodel: gpt-5.5\n---\nHelp."), 0o600))

	for i, id := range []string{"slack-thread:C1:1.1", "slack-thread:C2:2.2"} {
		require.NoError(t, sessions.UpsertThread(id, backend.ThreadState{Agent: "main"}))
		_, err := sessions.AppendEntryID(t.Context(), id, &rocketcode.SessionEntry{Timestamp: time.Unix(int64(2-i), 123456789).UTC()})
		require.NoError(t, err)
	}

	cancelled := make(chan struct{})
	channels := &mockChannels{SidebarChannelAgentChoicesFunc: func(ctx context.Context, channel string) (string, []string, error) {
		if channel == "C2" {
			<-ctx.Done()
			close(cancelled)

			return "", nil, ctx.Err()
		}

		return channel, []string{"main"}, nil
	}}
	listener, err := Listen(testSocketPath(t))
	require.NoError(t, err)

	listReturned := make(chan struct{})
	transport := grpc.NewServer(grpc.StreamInterceptor(func(service any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if info.FullMethod == "/rpc.Web/ListSessions" {
			defer close(listReturned)
		}

		return handler(service, stream)
	}))
	core := &mockBackend{RunTurnFunc: func(_ context.Context, inbound *protocol.InboundMessage) error {
		if inbound.ConversationID != "slack-thread:C1:1.1" || inbound.Text != "send while listing" || inbound.Metadata[protocol.InboundPrincipalMetadataKey] != "alice" {
			return fmt.Errorf("unexpected prompt during enumeration: %v", inbound)
		}

		return nil
	}}
	New(core, sessions, cfg, channels, &mockCronJobs{}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() {
		transport.Stop()
		require.NoError(t, serving.Wait())
	})
	proxy := exec.CommandContext(t.Context(), "bun", "test", "src/sidebar-transport.test.ts")
	proxy.Dir = "../../../../web"

	proxy.Env = append(os.Environ(), "ROCKETCLAW_WEB_GRPC=unix:"+listener.Addr().String(), "ROCKETCLAW_SIDEBAR_TEST=1")
	output, err := proxy.CombinedOutput()
	require.NoError(t, err, "%s", output)
	t.Log(string(output))

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP abort did not cancel the pending Go enumeration")
	}

	require.Empty(t, channels.ChannelAgentChoicesCalls(), "display uses stored facts, never live action authorization")

	select {
	case <-listReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP abort did not unwind the Go list handler and its deferred row cleanup")
	}
}
