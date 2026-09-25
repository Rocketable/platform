package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestPromptAndLiveTransport(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	rt := &backend.Runtime{Sessions: sessions}
	entered := make(chan *protocol.InboundMessage)
	release := make(chan struct{})
	stashed := make(chan *protocol.ThreadQueueItem, 1)
	subscribed := make(chan struct{}, 1)
	unsubscribed := make(chan struct{}, 1)
	core := &mockBackend{
		StashQueueItemFunc: func(_ context.Context, conversationID string, item *protocol.ThreadQueueItem) error {
			item.ConversationID = conversationID
			stashed <- item

			return nil
		},
		RunTurnFunc: func(ctx context.Context, inbound *protocol.InboundMessage) error {
			select {
			case entered <- inbound:
			case <-ctx.Done():
				return ctx.Err()
			}

			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}

			if inbound.Text == "fail" {
				return errors.New("turn failed")
			}

			return nil
		},
		SubscribeFunc: func(ctx context.Context) iter.Seq[protocol.Event] {
			events := rt.Subscribe(ctx)

			subscribed <- struct{}{}

			return func(yield func(protocol.Event) bool) {
				events(yield)

				unsubscribed <- struct{}{}
			}
		},
	}
	listener, err := Listen(testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	New(core, rt.Sessions, &config.Config{Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}, &mockChannels{}, &mockCronJobs{}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() {
		transport.Stop()
		require.NoError(t, serving.Wait())
	})

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))

	const id = "slack-thread:C1:1.1"

	var prompts errgroup.Group

	finished := make(chan struct{})

	prompts.Go(func() error {
		response, err := invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: id, Text: "exact input", Delivery: PromptDelivery_STEER})
		if err != nil {
			return err
		}

		assert.Empty(t, response.PrivateText)
		close(finished)

		return nil
	})

	inbound := <-entered
	require.Equal(t, id, inbound.ConversationID)
	require.Equal(t, protocol.SourceWeb, inbound.Source)
	require.Equal(t, "alice", inbound.Label)
	require.Equal(t, "alice", inbound.Metadata[protocol.InboundPrincipalMetadataKey])
	require.Equal(t, "exact input", inbound.Text)
	require.Equal(t, "exact input", inbound.Metadata[protocol.InboundRawTextMetadataKey])
	require.True(t, inbound.Human)
	require.Equal(t, protocol.InboundKindSteer, inbound.Kind)

	select {
	case <-finished:
		t.Fatal("Prompt returned before RunTurn completed")
	default:
	}

	release <- struct{}{}

	require.NoError(t, prompts.Wait())

	response, err := invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: id, Text: "queued input", Delivery: PromptDelivery_QUEUE})
	require.NoError(t, err)
	assert.Empty(t, response.PrivateText)

	item := <-stashed
	require.Equal(t, id, item.ConversationID)
	require.Equal(t, protocol.SourceWeb, item.Source)
	require.Equal(t, "alice", item.Principal)
	require.Equal(t, "queued input", item.Message)
	require.Equal(t, protocol.InboundKindEnqueue, item.Kind)

	for _, inner := range []string{"$review \"first area\"  second\tthird  ", "$skill stop inspect  the logs\nnext"} {
		_, err := invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: id, Text: "$enqueue \t" + inner, Delivery: PromptDelivery_STEER})
		require.NoError(t, err)

		item := <-stashed
		require.Equal(t, inner, item.Message)
		require.Equal(t, inner, item.Content.Text)
		require.Equal(t, "alice", item.Principal)
	}

	var failure errgroup.Group
	failure.Go(func() error {
		_, err := invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: id, Text: "fail"})
		return err
	})
	<-entered

	release <- struct{}{}

	require.ErrorContains(t, failure.Wait(), "turn failed")

	for _, request := range []*PromptRequest{{}, {Id: id, Delivery: PromptDelivery(99)}} {
		_, err := invoke[PromptResponse](ctx, connection, "Prompt", request)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}

	_, err = invoke[PromptResponse](metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "alice")), connection, "Prompt", &PromptRequest{Id: id})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	// Subscribe is the real runtime's live-only, acknowledged event stream.
	require.NoError(t, rt.PublishOutbound(ctx, protocol.NewOutboundMessage(id, "old history")))

	liveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := connection.NewStream(liveCtx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
	require.NoError(t, err)
	require.NoError(t, stream.SendMsg(&JoinRequest{Id: id}))
	require.NoError(t, stream.CloseSend())
	<-subscribed

	for _, other := range []string{"private-X", "web-only", "slack-thread:C2:2.2"} {
		require.NoError(t, rt.PublishOutbound(ctx, protocol.NewOutboundMessage(other, "not this conversation")))
	}

	message := protocol.NewOutboundMessage(id, "answer")
	message.Agent, message.Model, message.ReasoningEffort, message.SourceConversationID = "planner", "work/model-a", new(""), id
	attachment := protocol.OutboundAttachment{ID: "live-image", Name: "image.png", MIMEType: "image/png", Data: []byte("original")}
	require.NoError(t, sessions.SaveAttachment(ctx, id, &attachment, false))
	_, err = sessions.AppendEntryID(ctx, id, &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"function_call","call_id":"live","name":"rocketclaw_attach_files_to_response","arguments":"{}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"live","output":"queued attachments for final response: live-image"}`),
	}})
	require.NoError(t, err)

	message.Attachments = []protocol.OutboundAttachment{attachment, {ID: "not-referenced", Data: []byte("do not expose")}}
	message.TurnID = "turn-one"
	message.ProgressText = "thinking"
	message.Complete = true
	require.NoError(t, rt.PublishOutbound(ctx, message))

	for _, want := range []*TranscriptEvent{{Text: "thinking", Role: "thinking"}, {Text: "answer", Role: "assistant", Complete: true}} {
		var got TranscriptEvent
		require.NoError(t, stream.RecvMsg(&got))
		require.Equal(t, want.Text, got.Text)
		require.Equal(t, want.Role, got.Role)
		require.Equal(t, want.Complete, got.Complete)
		require.False(t, got.Snapshot)
		require.Equal(t, "turn-one", got.TurnId)

		if got.Role == "assistant" {
			require.Equal(t, "planner", got.Agent)
			require.Equal(t, "work/model-a", got.Model)
			require.Equal(t, new(""), got.ReasoningEffort)
			require.Equal(t, "canonical", got.Origin)
			require.Equal(t, id, got.SourceConversationId)
			require.Equal(t, id, got.DestinationConversationId)
			require.Len(t, got.Attachments, 1)
			require.Equal(t, attachment.ID, got.Attachments[0].Id)
			require.Equal(t, int64(len(attachment.Data)), got.Attachments[0].Size)
			require.Empty(t, got.Attachments[0].Data)
		}
	}

	message.Text, message.ProgressText = "", ""
	require.NoError(t, rt.PublishOutbound(ctx, message))

	var terminal TranscriptEvent
	require.NoError(t, stream.RecvMsg(&terminal))
	require.True(t, terminal.Complete)
	require.Empty(t, terminal.Text)
	cancel()
	require.Equal(t, codes.Canceled, status.Code(stream.RecvMsg(&terminal)))
	// Client cancellation does not wait for the server's subscription cleanup.
	<-unsubscribed
	require.NoError(t, rt.PublishOutbound(ctx, message))

	denied, err := connection.NewStream(t.Context(), &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
	require.NoError(t, err)
	require.NoError(t, denied.SendMsg(&JoinRequest{Id: id}))
	require.NoError(t, denied.CloseSend())
	require.Equal(t, codes.Unauthenticated, status.Code(denied.RecvMsg(&terminal)))
	require.NoError(t, sessions.UpsertExternalMCPSession("live-external", &backend.ExternalMCPSessionState{PrivateConversationID: "private-X", ManagedConversationID: id, Agent: "main", SlackChannel: "#ops"}))

	for _, private := range []string{"cron:hidden", "one-off-cron:hidden", "private-X"} {
		denied, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
		require.NoError(t, err)
		require.NoError(t, denied.SendMsg(&JoinRequest{Id: private}))
		require.NoError(t, denied.CloseSend())
		require.Equal(t, codes.PermissionDenied, status.Code(denied.RecvMsg(&terminal)))
	}

	var browser errgroup.Group
	browser.Go(func() error {
		<-subscribed

		outbound := protocol.NewOutboundMessage(id, "live browser answer")

		outbound.Agent, outbound.Model, outbound.ReasoningEffort, outbound.SourceConversationID = "planner", "work/model-a", new(""), id
		if err := rt.PublishOutbound(ctx, outbound); err != nil {
			return fmt.Errorf("publish browser event: %w", err)
		}

		inbound := <-entered
		assert.Equal(t, id, inbound.ConversationID)
		assert.Equal(t, "alice", inbound.Metadata[protocol.InboundPrincipalMetadataKey])
		assert.Equal(t, protocol.InboundKindSteer, inbound.Kind)

		release <- struct{}{}

		return nil
	})

	httpServer := startHTTPTestServer(t, connection)

	proxy := exec.CommandContext(t.Context(), "bun", "test", "src/live-transport.test.ts")
	proxy.Dir = "../../web"

	proxy.Env = append(os.Environ(), "ROCKETCLAW_TEST_HTTP_URL="+httpServer.URL, "ROCKETCLAW_LIVE_TEST_ID="+id)
	output, err := proxy.CombinedOutput()
	require.NoError(t, err, "%s", output)
	t.Log(string(output))
	require.NoError(t, browser.Wait())
}
