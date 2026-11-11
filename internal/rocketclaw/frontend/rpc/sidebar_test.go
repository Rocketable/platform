package rpc

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestSidebarSessionTimestamps(t *testing.T) {
	at := time.Date(2024, time.January, 2, 3, 4, 5, 123456789, time.UTC)
	for _, tc := range []struct {
		name                    string
		state, arrived, updated time.Time
		wantState               string
		wantArrived             string
		wantUpdated             string
	}{
		{"identical", at, at, at, "2024-01-02T03:04:05.123456789Z", "2024-01-02T03:04:05.123456789Z", "2024-01-02T03:04:05.123456789Z"},
		{"different instants", at, at.Add(time.Second), at.Add(2 * time.Second), "2024-01-02T03:04:05.123456789Z", "2024-01-02T03:04:06.123456789Z", "2024-01-02T03:04:07.123456789Z"},
		{"same instant in another zone", at, at.In(time.FixedZone("example", 3600)), at.In(time.FixedZone("example", -3600)), "2024-01-02T03:04:05.123456789Z", "2024-01-02T04:04:05.123456789+01:00", "2024-01-02T02:04:05.123456789-01:00"},
		{"zero arrival", at, time.Time{}, at, "2024-01-02T03:04:05.123456789Z", "0001-01-01T00:00:00Z", "2024-01-02T03:04:05.123456789Z"},
		{"zero state", time.Time{}, at, at, "", "", "2024-01-02T03:04:05.123456789Z"},
		{"zero updated", at, at, time.Time{}, "2024-01-02T03:04:05.123456789Z", "2024-01-02T03:04:05.123456789Z", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sidebarSession(&backend.SidebarChat{Session: backend.SidebarSession{StateSince: tc.state, ArrivedAt: tc.arrived, Summary: &protocol.SessionSummary{LastUpdated: tc.updated}}})
			require.Equal(t, tc.wantState, got.StateSince)
			require.Equal(t, tc.wantArrived, got.ArrivedAt)
			require.Equal(t, tc.wantUpdated, got.UpdatedAt)
		})
	}

	chat := backend.SidebarChat{Session: backend.SidebarSession{StateSince: at, ArrivedAt: at, Summary: &protocol.SessionSummary{LastUpdated: at}}}

	var got *Session

	allocs := testing.AllocsPerRun(1, func() { got = sidebarSession(&chat) })

	require.Equal(t, "2024-01-02T03:04:05.123456789Z", got.ArrivedAt)
	require.Equal(t, got.ArrivedAt, got.UpdatedAt)
	require.LessOrEqual(t, allocs, float64(2), "identical timestamps need only one formatted string and the response")
}

func BenchmarkSidebarSession(b *testing.B) {
	at := time.Date(2024, time.January, 2, 3, 4, 5, 123456789, time.UTC)
	chat := backend.SidebarChat{Session: backend.SidebarSession{StateSince: at, ArrivedAt: at, Summary: &protocol.SessionSummary{LastUpdated: at}}}

	b.ReportAllocs()

	for b.Loop() {
		got := sidebarSession(&chat)
		if got.StateSince != got.ArrivedAt || got.StateSince != got.UpdatedAt {
			b.Fatal("identical timestamps differ")
		}
	}
}

func BenchmarkSidebarResponses(b *testing.B) {
	server, ctx := sidebarChoicesFixture(b, 64)
	b.ReportAllocs()

	for b.Loop() {
		require.NoError(b, server.listSessions(&ListSessionsRequest{}, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(message any) error {
			_, err := (protojson.MarshalOptions{EmitDefaultValues: true}).Marshal(message.(*ListSessionsResponse))
			require.NoError(b, err)

			return nil
		}}))
	}
}

func sidebarChoicesFixture(tb testing.TB, count int) (*Server, context.Context) {
	tb.Helper()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(tb, err)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: tb.TempDir()}
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
		_, err := sessions.AppendEntryID(tb.Context(), id, &harness.SessionEntry{})
		require.NoError(tb, err)
	}

	return New(&mockBackend{SandboxErrorFunc: noSandboxError}, sessions, config.NewLockedConfig(cfg), &mockChannels{RenameThreadSessionFunc: func(context.Context, string, string) {}}, &mockCronJobs{}), metadata.NewIncomingContext(tb.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
}

func TestSidebarFingerprintAllocationBudget(t *testing.T) {
	result := testing.Benchmark(BenchmarkSidebarFingerprints)
	require.Positive(t, result.N)
	require.Less(t, result.AllocedBytesPerOp(), int64(4<<20))
}

func BenchmarkSidebarFingerprints(b *testing.B) {
	server, ctx := sidebarChoicesFixture(b, 32)
	name := strings.Repeat("example", 8192)

	for i := range 32 {
		_, err := server.sessions.UpdateConversationDetails(ctx, fmt.Sprintf("chat-%02d", i), nil, &name)
		require.NoError(b, err)
	}

	b.ReportAllocs()

	for b.Loop() {
		require.NoError(b, server.listSessions(&ListSessionsRequest{}, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(any) error { return nil }}))
	}
}

func TestSidebarSendFailures(t *testing.T) {
	t.Parallel()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })
	cfg := &config.Config{Workspace: t.TempDir()}
	root, err := os.OpenRoot(cfg.Workspace)

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents"), 0o700))
	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "main.md"), []byte("---\nmodel: gpt-6.1-sol\n---\nHelp."), 0o600))
	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	// This test-only limit rejects the first message, which carries the owner.
	transport := grpc.NewServer(grpc.MaxSendMsgSize(1))
	New(&mockBackend{SandboxErrorFunc: noSandboxError}, sessions, config.NewLockedConfig(cfg), &mockChannels{}, &mockCronJobs{}).Register(transport)

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

	stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/ListSessions")
	require.NoError(t, err)
	require.NoError(t, stream.SendMsg(&ListSessionsRequest{}))
	require.NoError(t, stream.CloseSend())

	var batch ListSessionsResponse

	err = stream.RecvMsg(&batch)
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Equal(t, fmt.Sprintf("trying to send message larger than max (%d vs. 1)", proto.Size(&ListSessionsResponse{Owner: "alice"})), status.Convert(err).Message())
	require.Empty(t, batch.Owner)
}

func TestSidebarHTTPStreamsBeforeGoTailAndCancels(t *testing.T) {
	t.Parallel()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })
	cfg := &config.Config{Workspace: t.TempDir()}
	root, err := os.OpenRoot(cfg.Workspace)

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents"), 0o700))
	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "main.md"), []byte("---\nmodel: gpt-6.1-sol\n---\nHelp."), 0o600))

	for i, id := range []string{"slack-thread:C1:1.1", "slack-thread:C2:2.2"} {
		openTestTurns(t, dsn).RecordSlackThread(t, sessions, id, backend.ThreadState{Agent: "main"})
		_, err := sessions.AppendEntryID(t.Context(), id, &harness.SessionEntry{Timestamp: time.Unix(int64(2-i), 123456789).UTC()})
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
	core := &mockBackend{SandboxErrorFunc: noSandboxError, RunTurnFunc: func(_ context.Context, inbound *protocol.InboundMessage) error {
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

// An External MCP chat's row carries its binding and when its work arrived: its last update while
// no turn of it or its run waits or runs.
func TestListSessionsExternalArrival(t *testing.T) {
	t.Parallel()

	server, ctx := sidebarChoicesFixture(t, 0)
	require.NoError(t, server.sessions.RegisterExternalMCPConversation("ticket", "choice-00", &backend.ExternalMCPSessionState{Agent: "choice-00", PrivateConversationID: "private", ManagedConversationID: "mcp"}))
	_, err := server.sessions.AppendEntryID(ctx, "mcp", &harness.SessionEntry{Timestamp: time.Unix(1, 0)})
	require.NoError(t, err)

	var sessions []*Session

	require.NoError(t, server.listSessions(&ListSessionsRequest{}, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(message any) error {
		sessions = append(sessions, message.(*ListSessionsResponse).Sessions...)
		return nil
	}}))
	require.Len(t, sessions, 1)
	require.True(t, sessions[0].External)
	require.Equal(t, WorkState_IDLE, sessions[0].WorkState)
	require.Equal(t, "1970-01-01T00:00:01Z", sessions[0].ArrivedAt)
	require.Equal(t, "1970-01-01T00:00:01Z", sessions[0].StateSince)
}

// A poll naming a listing the server still holds gets only the rows changed since and the sessions
// removed since, so the browser keeps unchanged rows and drops removed ones.
func TestListSessionsSendsChangesSince(t *testing.T) {
	t.Parallel()

	server, ctx := sidebarChoicesFixture(t, 3)
	rows := make(map[string]*Session) // Each chat's last sent row.
	list := func(ctx context.Context, since string) (sent []string, done *ListSessionsResponse) {
		var messages []*ListSessionsResponse

		require.NoError(t, server.listSessions(&ListSessionsRequest{Since: since}, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(message any) error {
			response := message.(*ListSessionsResponse)
			messages = append(messages, response)

			for _, session := range response.Sessions {
				require.Less(t, proto.Size(session), 256, "session rows must not repeat the agent catalog")
				sent = append(sent, session.Id)
				rows[session.Id] = session
			}

			return nil
		}}))

		first, done := messages[0], messages[len(messages)-1]
		require.NotEmpty(t, first.Owner, "the first message names the owner")
		require.NotNil(t, first.AgentTags, "the first message carries the agent tags")
		require.Empty(t, first.Sessions)
		require.True(t, done.UpstreamSuccess)

		for _, message := range messages[1:] {
			require.Empty(t, message.Owner, "only the first message names the owner")
		}

		return sent, done
	}

	sent, full := list(ctx, "")
	require.ElementsMatch(t, []string{"chat-00", "chat-01", "chat-02"}, sent, "the first listing sends every row")
	require.Empty(t, full.Since)
	require.Empty(t, full.Removed)
	require.NotEmpty(t, full.Version)

	sent, unchanged := list(ctx, full.Version)
	require.Empty(t, sent, "nothing changed")
	require.Equal(t, full.Version, unchanged.Since, "the listing names the version it sent changes since")
	require.Empty(t, unchanged.Removed)
	require.NotEqual(t, full.Version, unchanged.Version)

	queuedAt := time.Now()
	require.NoError(t, server.sessions.PutThreadQueueItem("later", &protocol.ThreadQueueItem{ConversationID: "chat-00", Message: "later", StashAt: queuedAt}))

	sent, queued := list(ctx, unchanged.Version)
	require.Equal(t, []string{"chat-00"}, sent, "queueing work into an idle chat changes its work state")
	require.Equal(t, WorkState_QUEUED, rows["chat-00"].WorkState)
	require.Equal(t, queuedAt.UTC().Format(time.RFC3339Nano), rows["chat-00"].StateSince, "a queued chat's state began when its turn was queued")

	_, err := server.updateSession(ctx, &UpdateSessionRequest{Id: "chat-01", Name: new("Renamed")})
	require.NoError(t, err)

	sent, renamed := list(ctx, queued.Version)
	require.Equal(t, []string{"chat-01"}, sent, "only the changed row")

	sent, _ = list(ctx, full.Version)
	require.ElementsMatch(t, []string{"chat-00", "chat-01"}, sent, "an older listing the server still holds works too")

	_, err = server.sessions.DeleteSession(t.Context(), "chat-02")
	require.NoError(t, err)

	sent, deleted := list(ctx, renamed.Version)
	require.Empty(t, sent)
	require.Equal(t, []string{"chat-02"}, deleted.Removed, "a deleted session is named as removed")

	sent, unknown := list(ctx, "unknown")
	require.Len(t, sent, 2, "an unknown version gets every row")
	require.Empty(t, unknown.Since)

	bob := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1", "rocketclaw-tailnet-login-bin", "bob@example.com", "rocketclaw-tailnet-name-bin", "Bob"))
	sent, bobs := list(bob, deleted.Version)
	require.Len(t, sent, 2, "another person's version gets every row")
	require.Empty(t, bobs.Since)
}

// A refresh where nothing changed costs the same few bytes however many sessions and tab rows there are; a changed
// tab row is sent once, and a session's attention rides its own row.
func TestQuietSessionRefreshSize(t *testing.T) {
	t.Parallel()

	server, ctx := sidebarChoicesFixture(t, 300)
	list := func(request *ListSessionsRequest) (size int, done *ListSessionsResponse, sessions map[string]*Session) {
		sessions = map[string]*Session{}

		require.NoError(t, server.listSessions(request, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(message any) error {
			// The Web receives listing messages as protojson without default values.
			response := message.(*ListSessionsResponse)
			body, err := protojson.Marshal(response)
			require.NoError(t, err)

			size += len(body)

			for _, session := range response.Sessions {
				sessions[session.Id] = session
			}

			if response.UpstreamSuccess {
				done = response
			}

			return nil
		}}))

		return size, done, sessions
	}
	tabs := func(done *ListSessionsResponse) []string {
		out := make([]string, 0, len(done.Tabs))
		for _, tab := range done.Tabs {
			out = append(out, fmt.Sprint(tab.Path, " parked=", tab.Parked, " timer=", tab.Until != ""))
		}

		return out
	}

	soon := time.Now().Add(time.Hour)
	pages := make([]string, 10)

	for i := range pages {
		pages[i] = fmt.Sprint("/pages/main/agent/", i)
	}

	require.NoError(t, server.sessions.ChangeAttention(ctx, "alice", pages, backend.AttentionPark, &soon))

	full, done, _ := list(&ListSessionsRequest{})
	require.Greater(t, full, 2<<10)
	require.Len(t, done.Tabs, 10, "every tab row is sent")

	quiet, done, _ := list(&ListSessionsRequest{Since: done.Version})
	require.LessOrEqual(t, quiet, 2<<10, "a quiet refresh must not grow with the session or tab row count")
	require.Empty(t, done.Tabs)

	require.NoError(t, server.sessions.ChangeAttention(ctx, "alice", pages[:1], backend.AttentionPark, nil))
	require.NoError(t, server.sessions.ChangeAttention(ctx, "alice", pages[1:2], backend.AttentionUnpark, nil))

	_, done, _ = list(&ListSessionsRequest{Since: done.Version})
	require.Equal(t, []string{pages[0] + " parked=true timer=false", pages[1] + " parked=false timer=false"}, tabs(done), "a changed row, and one with only its path for a row gone")

	_, done, _ = list(&ListSessionsRequest{Since: done.Version})
	require.Empty(t, tabs(done), "each change is sent once")

	// A session's park, timer and a timer's note travel on its own row.
	path := func(id string) string { return "/s/" + base64.RawURLEncoding.EncodeToString([]byte(id)) }
	require.NoError(t, server.sessions.ChangeAttention(ctx, "alice", []string{path("chat-00")}, backend.AttentionPark, &soon))
	require.NoError(t, server.sessions.ChangeAttention(ctx, "alice", []string{path("chat-01")}, backend.AttentionPark, new(time.Now().Add(-time.Second))))

	_, done, sessions := list(&ListSessionsRequest{Since: done.Version, SameOrigin: true})
	require.Len(t, sessions, 2)
	require.True(t, sessions["chat-00"].Parked)
	require.Equal(t, soon.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), sessions["chat-00"].Until)
	require.NotEmpty(t, sessions["chat-01"].UnreadAt, "the poll ended the park")
	require.False(t, sessions["chat-01"].MarkedUnread)
	require.False(t, sessions["chat-01"].Parked)
	require.Equal(t, "Park ended", sessions["chat-01"].Note)
	require.Empty(t, tabs(done))
}
