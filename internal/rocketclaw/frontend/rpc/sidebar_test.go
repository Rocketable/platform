package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
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

func sidebarChoicesFixture(tb testing.TB, count int) (*Server, context.Context) {
	tb.Helper()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(tb, err)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: tb.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	sessions, err := backend.NewSessionServiceIn(tb.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(tb, err)
	tb.Cleanup(func() { require.NoError(tb, sessions.Stop()) })

	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(tb, err)
	tb.Cleanup(func() { require.NoError(tb, root.Close()) })
	require.NoError(tb, root.MkdirAll(cfg.RuntimeDirName()+"/agents", 0o700))

	for i := range 32 {
		require.NoError(tb, root.WriteFile(fmt.Sprintf("%s/agents/choice-%02d.md", cfg.RuntimeDirName(), i), []byte("---\nmodel: example-model\n---\nHelp."), 0o600))
	}

	for i := range count {
		id := fmt.Sprintf("chat-%02d", i)
		require.NoError(tb, sessions.UpsertThread(id, backend.ThreadState{Agent: "choice-00"}))
		_, err := sessions.AppendEntryID(tb.Context(), id, &rocketcode.SessionEntry{})
		require.NoError(tb, err)
	}

	return New(&mockBackend{}, sessions, config.NewLockedConfig(cfg), &mockChannels{}, &mockCronJobs{}), metadata.NewIncomingContext(tb.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
}

func TestSidebarReusesAgentChoices(t *testing.T) {
	server, ctx := sidebarChoicesFixture(t, 2)

	for count := 32; count <= 33; count++ {
		var choices []string

		rows := 0

		require.NoError(t, server.listSessions(&ListSessionsRequest{}, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(message any) error {
			for _, session := range message.(*ListSessionsResponse).Sessions {
				require.Len(t, session.AllowedAgents, count)

				for i, name := range session.AllowedAgents {
					require.Equal(t, fmt.Sprintf("choice-%02d", i), name)
				}

				if choices != nil {
					require.Same(t, &choices[0], &session.AllowedAgents[0], "rows reuse the request's sorted names")
				}

				choices = session.AllowedAgents
				rows++
			}

			return nil
		}}))
		require.Equal(t, 2, rows)

		if count == 32 {
			cfg := server.cfg.Clone()
			root, err := os.OpenRoot(cfg.Workspace)
			require.NoError(t, err)
			require.NoError(t, root.WriteFile(cfg.RuntimeDirName()+"/agents/choice-32.md", []byte("---\nmodel: example-model\n---\nHelp."), 0o600))
			require.NoError(t, root.Close())
		}
	}
}

func BenchmarkSidebarAgentChoices(b *testing.B) {
	server, ctx := sidebarChoicesFixture(b, 64)
	for b.Loop() {
		require.NoError(b, server.listSessions(&ListSessionsRequest{}, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(any) error { return nil }}))
	}
}

func TestSidebarSendFailures(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })
	cfg := &config.Config{Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	root, err := os.OpenRoot(cfg.Workspace)

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents"), 0o700))
	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "main.md"), []byte("---\nmodel: gpt-6.1-sol\n---\nHelp."), 0o600))
	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	// This test-only limit rejects both the owner-bearing terminal and a session row.
	transport := grpc.NewServer(grpc.MaxSendMsgSize(1))
	New(&mockBackend{}, sessions, config.NewLockedConfig(cfg), &mockChannels{}, &mockCronJobs{}).Register(transport)

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
		{"empty completion", &ListSessionsResponse{Owner: "alice", UpstreamSuccess: true, Version: strings.Repeat("v", 26)}}, // rand.Text is 26 characters.
		{"session row", &ListSessionsResponse{Owner: "alice", Sessions: []*Session{{Id: "web", Agent: "main", AllowedAgents: []string{"main"}, Label: "web"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "session row" {
				require.NoError(t, sessions.UpsertThread("web", backend.ThreadState{Agent: "main"}))
				_, err := sessions.AppendEntryID(ctx, "web", &rocketcode.SessionEntry{})
				require.NoError(t, err)
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
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })
	cfg := &config.Config{Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	root, err := os.OpenRoot(cfg.Workspace)

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents"), 0o700))
	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "main.md"), []byte("---\nmodel: gpt-6.1-sol\n---\nHelp."), 0o600))

	for i, id := range []string{"slack-thread:C1:1.1", "slack-thread:C2:2.2"} {
		openTestTurns(t, dsn).RecordSlackThread(t, sessions, id, backend.ThreadState{Agent: "main"})
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
	listener, err := net.Listen("unix", testSocketPath(t))
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
	New(core, sessions, config.NewLockedConfig(cfg), channels, &mockCronJobs{}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() {
		transport.Stop()
		require.NoError(t, serving.Wait())
	})

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	httpServer := startHTTPTestServer(t, connection)

	runWebTest(t, "src/sidebar-transport.test.ts", "ROCKETCLAW_SIDEBAR_TEST_URL="+httpServer.URL)

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

// A poll naming a listing the server still holds gets only the rows changed since; every listing ends
// with all IDs in order, so the browser keeps unchanged rows and drops missing ones.
func TestListSessionsSendsChangesSince(t *testing.T) {
	server, ctx := sidebarChoicesFixture(t, 3)
	list := func(ctx context.Context, since string) (sent []string, done *ListSessionsResponse) {
		require.NoError(t, server.listSessions(&ListSessionsRequest{Since: since}, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(message any) error {
			response := message.(*ListSessionsResponse)
			for _, session := range response.Sessions {
				sent = append(sent, session.Id)
			}

			if response.UpstreamSuccess {
				done = response
			}

			return nil
		}}))

		return sent, done
	}

	sent, full := list(ctx, "")
	require.ElementsMatch(t, []string{"chat-00", "chat-01", "chat-02"}, sent)
	require.Equal(t, sent, full.Order, "the first listing sends every row, in order")
	require.NotEmpty(t, full.Version)

	sent, unchanged := list(ctx, full.Version)
	require.Empty(t, sent, "nothing changed")
	require.Equal(t, full.Order, unchanged.Order)
	require.NotEqual(t, full.Version, unchanged.Version)

	_, err := server.updateSession(ctx, &UpdateSessionRequest{Id: "chat-01", Name: new("Renamed")})
	require.NoError(t, err)

	sent, renamed := list(ctx, unchanged.Version)
	require.Equal(t, []string{"chat-01"}, sent, "only the changed row")

	sent, _ = list(ctx, full.Version)
	require.Equal(t, []string{"chat-01"}, sent, "an older listing the server still holds works too")

	_, err = server.sessions.DeleteSession(t.Context(), "chat-02")
	require.NoError(t, err)

	sent, deleted := list(ctx, renamed.Version)
	require.Empty(t, sent)
	require.ElementsMatch(t, []string{"chat-00", "chat-01"}, deleted.Order, "a deleted session leaves the order")

	sent, _ = list(ctx, "unknown")
	require.Len(t, sent, 2, "an unknown version gets every row")

	bob := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1", "rocketclaw-tailnet-login-bin", "bob@example.com", "rocketclaw-tailnet-name-bin", "Bob"))
	sent, bobs := list(bob, deleted.Version)
	require.NotEmpty(t, bobs.Order)
	require.Equal(t, bobs.Order, sent, "another person's version gets every row")
}
