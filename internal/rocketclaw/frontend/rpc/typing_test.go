package rpc

import (
	"context"
	"log/slog"
	"maps"
	"net"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestTyping(t *testing.T) {
	t.Parallel()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), Permissions: map[string][]string{"cy@example.com": {"Ops"}}}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(cfg.RuntimeDirName()+"/agents", 0o700))

	for _, id := range []string{"x", "y"} {
		require.NoError(t, sessions.UpsertThread(id, backend.ThreadState{Agent: "main"}))
	}

	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	server := New(&mockBackend{SandboxErrorFunc: noSandboxError}, sessions, config.NewLockedConfig(cfg), &mockChannels{}, &mockCronJobs{})
	transport := grpc.NewServer()
	server.Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	as := func(login, name string) context.Context {
		return metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-tailnet-login-bin", login, "rocketclaw-tailnet-name-bin", name))
	}
	ana, bea, cy := as("ana@example.com", "Ana"), as("bea@example.com", "Bea"), as("cy@example.com", "Cy")

	open := func(ctx context.Context, id string) grpc.ClientStream {
		stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/WatchTyping")
		require.NoError(t, err)
		require.NoError(t, stream.SendMsg(&WatchTypingRequest{Id: id}))
		require.NoError(t, stream.CloseSend())

		return stream
	}
	next := func(stream grpc.ClientStream) *TypingEvent {
		event := &TypingEvent{}
		require.NoError(t, stream.RecvMsg(event))

		return event
	}
	// watch returns a watch on conversation id once it listens, and ends it.
	watch := func(ctx context.Context, id string) (grpc.ClientStream, context.CancelFunc) {
		ctx, cancel := context.WithCancel(ctx)
		stream := open(ctx, id)
		require.True(t, proto.Equal(&TypingEvent{}, next(stream)), "a watch opens with an empty event")

		return stream, cancel
	}
	send := func(ctx context.Context, id string, typing bool, tab string, seq uint32) error {
		_, err := invoke[ReportTypingResponse](ctx, connection, "ReportTyping", &ReportTypingRequest{Id: id, Typing: typing, Tab: tab, Seq: seq})
		return err
	}
	// report sends from one tab per person, numbering its reports.
	reports := map[context.Context]uint32{}
	report := func(ctx context.Context, id string, typing bool) error {
		reports[ctx]++
		return send(ctx, id, typing, "tab", reports[ctx])
	}
	hub := func() map[string][]string {
		server.typingMu.Lock()
		defer server.typingMu.Unlock()

		watching := map[string][]string{}
		for id, watchers := range server.typingWatchers {
			watching[id] = slices.Sorted(maps.Values(watchers))
		}

		return watching
	}
	// numbered names the conversations whose report numbers the server keeps.
	numbered := func() []string {
		server.typingMu.Lock()
		defer server.typingMu.Unlock()

		return slices.Sorted(maps.Keys(server.typingNewest))
	}

	beaX, endBeaX := watch(bea, "x")
	anaX, endAnaX := watch(ana, "x")

	require.NoError(t, report(ana, "x", true))
	require.True(t, proto.Equal(&TypingEvent{Principal: "Ana", Typing: true}, next(beaX)))

	// A report on y, then one on x: Bea's next event is the one on x.
	require.NoError(t, report(ana, "y", true))
	require.NoError(t, report(ana, "x", false))
	require.True(t, proto.Equal(&TypingEvent{Principal: "Ana"}, next(beaX)))

	// Ana's watch of x skipped both her reports there: its next event is Bea's.
	require.NoError(t, report(bea, "x", true))
	require.True(t, proto.Equal(&TypingEvent{Principal: "Bea", Typing: true}, next(anaX)))

	// A watcher that takes nothing more leaves the report, and the other watchers, unblocked.
	stuck := make(chan *TypingEvent)

	server.typingMu.Lock()
	server.typingWatchers["x"][stuck] = "dan@example.com"
	server.typingMu.Unlock()

	require.NoError(t, report(ana, "x", true))
	require.True(t, proto.Equal(&TypingEvent{Principal: "Ana", Typing: true}, next(beaX)))

	server.typingMu.Lock()
	delete(server.typingWatchers["x"], stuck)
	server.typingMu.Unlock()

	// A report that arrives after a later one from its tab is dropped: Bea's next event after Ana's stop is her
	// next stop, not the typing report that came late. Another tab of hers numbers its reports on its own.
	require.NoError(t, send(ana, "x", false, "late", 2))
	require.True(t, proto.Equal(&TypingEvent{Principal: "Ana"}, next(beaX)))
	require.NoError(t, send(ana, "x", true, "late", 1))
	require.NoError(t, send(ana, "x", false, "late", 3))
	require.True(t, proto.Equal(&TypingEvent{Principal: "Ana"}, next(beaX)))
	require.NoError(t, send(ana, "x", true, "other", 1))
	require.True(t, proto.Equal(&TypingEvent{Principal: "Ana", Typing: true}, next(beaX)))

	// Someone whose display name is blank is named by their login, as on their prompts.
	require.NoError(t, report(as("dan@example.com", " "), "x", true))
	require.True(t, proto.Equal(&TypingEvent{Principal: "dan@example.com", Typing: true}, next(beaX)))

	// A conversation nobody watches keeps no numbers, so its reports go nowhere.
	require.NoError(t, report(ana, "y", false))
	require.Equal(t, []string{"x"}, numbered())

	endBeaX()
	require.Eventually(t, func() bool {
		return maps.EqualFunc(map[string][]string{"x": {"ana@example.com"}}, hub(), slices.Equal)
	}, 10*time.Second, time.Millisecond, "a watch that ends leaves the hub")

	// An OpenCode session needs no stored row, but stays closed to people limited to spaces.
	beaRemote, endBeaRemote := watch(bea, "opencode:s:ses_1")

	require.NoError(t, report(ana, "opencode:s:ses_1", true))
	require.True(t, proto.Equal(&TypingEvent{Principal: "Ana", Typing: true}, next(beaRemote)))

	errReport := report(cy, "opencode:s:ses_1", true)
	errWatch := open(cy, "opencode:s:ses_1").RecvMsg(&TypingEvent{})

	for _, err := range []error{errReport, errWatch} {
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
		require.Contains(t, status.Convert(err).Message(), status.Convert(errOpenCodeClosed).Message())
	}

	endAnaX()
	endBeaRemote()
	require.Eventually(t, func() bool { return len(hub()) == 0 && len(numbered()) == 0 }, 10*time.Second, time.Millisecond, "the hub keeps no conversation without watchers")
}
