package rpc

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
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
		name                   string
		state, updated         time.Time
		wantState, wantUpdated string
	}{
		{"identical", at, at, "2024-01-02T03:04:05.123456789Z", "2024-01-02T03:04:05.123456789Z"},
		{"different instants", at, at.Add(2 * time.Second), "2024-01-02T03:04:05.123456789Z", "2024-01-02T03:04:07.123456789Z"},
		{"same instant in another zone", at, at.In(time.FixedZone("example", -3600)), "2024-01-02T03:04:05.123456789Z", "2024-01-02T02:04:05.123456789-01:00"},
		{"zero state", time.Time{}, at, "", "2024-01-02T03:04:05.123456789Z"},
		{"zero updated", at, time.Time{}, "2024-01-02T03:04:05.123456789Z", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sidebarSession(&backend.SidebarChat{Session: backend.SidebarSession{StateSince: tc.state, Summary: &protocol.SessionSummary{LastUpdated: tc.updated}}})
			require.Equal(t, tc.wantState, got.StateSince)
			require.Equal(t, tc.wantUpdated, got.UpdatedAt)
		})
	}

	chat := backend.SidebarChat{Session: backend.SidebarSession{StateSince: at, Summary: &protocol.SessionSummary{LastUpdated: at}}}

	var got *Session

	allocs := testing.AllocsPerRun(1, func() { got = sidebarSession(&chat) })

	require.Equal(t, "2024-01-02T03:04:05.123456789Z", got.UpdatedAt)
	require.Equal(t, got.StateSince, got.UpdatedAt)
	require.LessOrEqual(t, allocs, float64(2), "identical timestamps need only one formatted string and the response")
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
		createWebChat(tb, sessions, protocol.Conversation{ID: id, Agent: "choice-00"})
		_, err := sessions.AppendEntryID(tb.Context(), id, &harness.SessionEntry{})
		require.NoError(tb, err)
	}

	return New(&mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning}, sessions, config.NewLockedConfig(cfg), &mockChannels{RenameThreadSessionFunc: func(context.Context, string, string) {}}, &mockRoutines{}), metadata.NewIncomingContext(tb.Context(), tailnetUser("alice"))
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
		_, err := server.sessions.UpdateConversationDetails(ctx, fmt.Sprintf("chat-%02d", i), &name)
		require.NoError(b, err)
	}

	b.ReportAllocs()

	for b.Loop() {
		require.NoError(b, server.listSessions(&ListSessionsRequest{}, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(any) error { return nil }}))
	}
}

func TestListSessionsHeaderDoesNotLoadSkillCatalog(t *testing.T) {
	server, root := sidebarHeaderFixture(t)
	withoutSkills := testing.AllocsPerRun(20, func() { sidebarHeader(t, server) })

	addSidebarSkills(t, root)

	withSkills := testing.AllocsPerRun(20, func() { sidebarHeader(t, server) })
	require.LessOrEqual(t, withSkills, withoutSkills+32, "unrelated skills should not add work before the session list starts")
}

func BenchmarkListSessionsHeader(b *testing.B) {
	server, root := sidebarHeaderFixture(b)
	addSidebarSkills(b, root)

	for b.Loop() {
		sidebarHeader(b, server)
	}
}

func sidebarHeaderFixture(tb testing.TB) (*Server, *os.Root) {
	tb.Helper()
	cfg := &config.Config{Workspace: tb.TempDir()}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(tb, err)
	tb.Cleanup(func() { require.NoError(tb, root.Close()) })
	require.NoError(tb, root.MkdirAll(".rocketclaw/agents", 0o700))
	require.NoError(tb, root.MkdirAll(".rocketclaw/skills", 0o700))
	require.NoError(tb, root.WriteFile(".rocketclaw/agents/example.md", []byte("---\nmodel: example-model\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[draft, ready]]\n---\nHelp."), 0o600))

	return &Server{cfg: config.NewLockedConfig(cfg), sessions: new(backend.SessionService)}, root
}

func addSidebarSkills(tb testing.TB, root *os.Root) {
	tb.Helper()

	for i := range 16 {
		name := fmt.Sprintf("example-%d", i)
		dir := ".rocketclaw/skills/" + name
		require.NoError(tb, root.MkdirAll(dir, 0o700))
		require.NoError(tb, root.WriteFile(dir+"/SKILL.md", []byte("---\nname: "+name+"\ndescription: Example skill\n---\nHelp."), 0o600))
	}
}

func sidebarHeader(tb testing.TB, server *Server) {
	tb.Helper()
	ctx := metadata.NewIncomingContext(tb.Context(), tailnetUser("alice"))
	err := server.listSessions(&ListSessionsRequest{}, &mockServerStream{
		ContextFunc: func() context.Context { return ctx },
		SendMsgFunc: func(message any) error {
			first := message.(*ListSessionsResponse)
			require.Equal(tb, "alice", first.Owner)
			require.ElementsMatch(tb, []string{"draft", "ready"}, first.AgentTags["example"].GetTags())

			return io.EOF
		},
	})
	require.ErrorIs(tb, err, io.EOF)
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
	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer(grpc.MaxSendMsgSize(1))
	New(&mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning}, sessions, config.NewLockedConfig(cfg), &mockChannels{}, &mockRoutines{}).Register(transport)

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
	ctx := metadata.NewOutgoingContext(t.Context(), tailnetUser("alice"))

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
	core := &mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning, AgentPausedFunc: notPaused, RunTurnFunc: func(_ context.Context, inbound *protocol.InboundMessage) error {
		if inbound.ConversationID != "slack-thread:C1:1.1" || inbound.Text != "send while listing" || inbound.Metadata[protocol.InboundPrincipalMetadataKey] != "alice" {
			return fmt.Errorf("unexpected prompt during enumeration: %v", inbound)
		}

		return nil
	}}
	New(core, sessions, config.NewLockedConfig(cfg), channels, &mockRoutines{}).Register(transport)

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

func TestListSessionsSendsChangesSince(t *testing.T) {
	t.Parallel()

	server, ctx := sidebarChoicesFixture(t, 3)
	rows := make(map[string]*Session)
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
	require.Equal(t, queuedAt.UTC().Format(time.RFC3339Nano), rows["chat-00"].StateSince, "a queued chat's state began when its turn was queued")

	_, err := server.updateSession(ctx, &UpdateSessionRequest{Id: "chat-01", Name: new("Renamed")})
	require.NoError(t, err)

	sent, renamed := list(ctx, queued.Version)
	require.Equal(t, []string{"chat-01"}, sent, "only the changed row")

	_, err = openTestTurns(t, server.cfg.Clone().DatabaseURL).db.ExecContext(ctx, `UPDATE conversations SET paused = TRUE WHERE conversation_id = 'chat-01'`)
	require.NoError(t, err)

	sent, paused := list(ctx, renamed.Version)
	require.Equal(t, []string{"chat-01"}, sent, "pausing a chat changes its row for every viewer")
	require.True(t, rows["chat-01"].GetPaused())
	require.False(t, rows["chat-00"].GetPaused())

	sent, _ = list(ctx, full.Version)
	require.ElementsMatch(t, []string{"chat-00", "chat-01"}, sent, "an older listing the server still holds works too")

	_, err = server.sessions.DeleteSession(t.Context(), "chat-02")
	require.NoError(t, err)

	sent, deleted := list(ctx, paused.Version)
	require.Empty(t, sent)
	require.Equal(t, []string{"chat-02"}, deleted.Removed, "a deleted session is named as removed")

	sent, unknown := list(ctx, "unknown")
	require.Len(t, sent, 2, "an unknown version gets every row")
	require.Empty(t, unknown.Since)

	bob := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-tailnet-login-bin", "bob@example.com", "rocketclaw-tailnet-name-bin", "Bob"))
	sent, bobs := list(bob, deleted.Version)
	require.Len(t, sent, 2, "another person's version gets every row")
	require.Empty(t, bobs.Since)

	owner, _, err := server.spaceLimit(ctx)
	require.NoError(t, err)

	firstSlot := &server.listings[owner][0]
	_, last := list(ctx, full.Version)
	require.Equal(t, full.Version, last.Since, "the oldest retained version still works")
	sent, evicted := list(ctx, full.Version)
	require.ElementsMatch(t, []string{"chat-00", "chat-01"}, sent, "an evicted version gets every row")
	require.Empty(t, evicted.Since)

	listings := server.listings[owner]
	require.Len(t, listings, sessionListingsKept)
	require.Same(t, firstSlot, &listings[0], "eviction must not leave old snapshots before the slice")

	fingerprints := listings[len(listings)-1].rows
	server = New(server.backend, server.sessions, server.cfg, server.channels, server.routines)
	sent, fresh := list(ctx, unknown.Version)
	require.ElementsMatch(t, []string{"chat-00", "chat-01"}, sent, "another server's version gets every row")
	require.Empty(t, fresh.Since)
	require.NotEqual(t, fingerprints, server.listings[owner][0].rows, "each server uses its own row fingerprints")
}

func TestQuietSessionRefreshSize(t *testing.T) {
	t.Parallel()

	server, ctx := sidebarChoicesFixture(t, 300)
	list := func(request *ListSessionsRequest) (size int, done *ListSessionsResponse, sessions map[string]*Session) {
		sessions = map[string]*Session{}

		require.NoError(t, server.listSessions(request, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(message any) error {
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

	require.NoError(t, server.sessions.ChangeAttention(ctx, "alice", pages, backend.AttentionPark, &soon, 0))

	full, done, _ := list(&ListSessionsRequest{})
	require.Greater(t, full, 2<<10)
	require.Len(t, done.Tabs, 10, "every tab row is sent")

	quiet, done, _ := list(&ListSessionsRequest{Since: done.Version})
	require.LessOrEqual(t, quiet, 2<<10, "a quiet refresh must not grow with the session or tab row count")
	require.Empty(t, done.Tabs)

	require.NoError(t, server.sessions.ChangeAttention(ctx, "alice", pages[:1], backend.AttentionPark, nil, 0))
	require.NoError(t, server.sessions.ChangeAttention(ctx, "alice", pages[1:2], backend.AttentionUnpark, nil, 0))

	_, done, _ = list(&ListSessionsRequest{Since: done.Version})
	require.Equal(t, []string{pages[0] + " parked=true timer=false", pages[1] + " parked=false timer=false"}, tabs(done), "a changed row, and one with only its path for a row gone")

	_, done, _ = list(&ListSessionsRequest{Since: done.Version})
	require.Empty(t, tabs(done), "each change is sent once")

	path := func(id string) string { return "/s/" + base64.RawURLEncoding.EncodeToString([]byte(id)) }
	require.NoError(t, server.sessions.ChangeAttention(ctx, "alice", []string{path("chat-00")}, backend.AttentionPark, &soon, 0))

	plain := ctx
	ctx = metadata.NewIncomingContext(plain, metadata.Join(tailnetUser("alice"), metadata.Pairs("rocketclaw-web-poll", "true")))
	_, done, sessions := list(&ListSessionsRequest{Since: done.Version})
	ctx = plain

	require.Len(t, sessions, 1)
	require.True(t, sessions["chat-00"].Parked)
	require.Equal(t, soon.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), sessions["chat-00"].Until)
	require.Empty(t, tabs(done))

	require.NoError(t, server.sessions.SetAutoParkAll(ctx, "alice", true))
	require.NoError(t, server.sessions.ChangeAttention(ctx, "alice", []string{path("chat-00")}, backend.AttentionNoAutoPark, nil, 0))

	_, _, sessions = list(&ListSessionsRequest{})
	require.Equal(t, []bool{false, true}, []bool{sessions["chat-00"].AutoPark, sessions["chat-01"].AutoPark})

	unfollowed := func(done *ListSessionsResponse) map[string]bool {
		out := map[string]bool{}
		for _, tab := range done.Tabs {
			out[tab.Path] = tab.Unfollowed
		}

		return out
	}

	require.NoError(t, server.changeAttention(ctx, "alice", &AttentionChange{Paths: []string{path("chat-00"), pages[2]}, Action: AttentionAction_UNFOLLOW}))

	_, done, sessions = list(&ListSessionsRequest{})
	require.Equal(t, []bool{true, false}, []bool{sessions["chat-00"].Unfollowed, sessions["chat-01"].Unfollowed})
	require.Equal(t, []bool{true, false}, []bool{unfollowed(done)[pages[2]], unfollowed(done)[pages[3]]})

	require.NoError(t, server.changeAttention(ctx, "alice", &AttentionChange{Paths: []string{pages[2]}, Action: AttentionAction_FOLLOW}))

	_, done, _ = list(&ListSessionsRequest{Since: done.Version})
	require.Equal(t, map[string]bool{pages[2]: false}, unfollowed(done), "following again resends the row")
}
