package rpc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestConfigTailscaleIdentityDoesNotChangeAccess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	server := &Server{cfg: &config.Config{}, usernames: map[netip.Addr]string{netip.MustParseAddr("100.64.0.1"): "configured-user"}}

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "100.64.0.1"))
	for _, tt := range []struct{ name, output, want string }{
		{"identified", `{"UserProfile":{"LoginName":"connected@example.com"}}`, "connected@example.com"},
		{"no user", `{"Node":{"Tags":["tag:server"]}}`, ""},
		{"invalid response", `not json`, ""},
		{"unavailable", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Exercise the real command boundary with a controlled executable.
			script := "#!/bin/sh\n[ \"$*\" = 'whois --json 100.64.0.1' ] || exit 1\nprintf '%s' '" + tt.output + "'\n"
			if tt.name == "unavailable" {
				script = "#!/bin/sh\nexit 1\n"
			}

			require.NoError(t, os.WriteFile(filepath.Join(dir, "tailscale"), []byte(script), 0o700))

			view, err := server.listConfig(ctx)
			require.NoError(t, err)
			require.Equal(t, tt.want, view.Config.TailscaleUser)

			username, err := server.principal(ctx)
			require.NoError(t, err)
			require.Equal(t, "configured-user", username)
			_, err = server.listConfig(metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "100.64.0.2")))
			require.Equal(t, codes.Unauthenticated, status.Code(err))
		})
	}
}

func TestSessionEntries(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	storageConfig := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}
	sessions, err := backend.NewSessionServiceIn(t.Context(), storageConfig, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })
	configPath := filepath.Join(t.TempDir(), "rocketclaw.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"database_url":"postgres://localhost/rocketclaw_test", "openai":{"api_key":"test"}, "slack":{"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]}, "web_users":{"192.0.2.1":"alice","127.0.0.1":"alice"}}`), 0o600))
	cfg, err := config.Load(configPath, "", config.AWSFetcher{})
	require.NoError(t, err)

	cfg.Workspace = t.TempDir()
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents"), 0o700))

	for _, name := range []string{"main", "planner", "selected"} {
		require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", name+".md"), []byte("---\nmodel: gpt-5.5\n---\nHelp."), 0o600))
	}

	rt := &backend.Runtime{Sessions: sessions}

	var promoted, promotedIDs []string

	var turns []*protocol.InboundMessage

	core := &mockBackend{
		SubscribeFunc:          rt.Subscribe,
		CreateConversationFunc: rt.CreateConversation,
		ListConversationsFunc:  rt.ListConversations,
		RunTurnFunc: func(ctx context.Context, inbound *protocol.InboundMessage) error {
			turns = append(turns, inbound)
			if inbound.Kind == protocol.InboundKindCancel {
				return nil
			}

			if err := rt.RunTurn(ctx, inbound); err != nil {
				return fmt.Errorf("run turn: %w", err)
			}

			return nil
		},
		SwitchConversationAgentFunc: sessions.SetThreadAgentIfExists,
		QueueItemsFunc: func(conversationID string) ([]protocol.ThreadQueueItem, error) {
			return sessions.ThreadQueueForConversation(conversationID)
		},
		PromoteQueueItemFunc: func(_ context.Context, conversationID, itemID string) (bool, error) {
			if itemID == "missing" {
				return false, nil
			}

			promoted = append(promoted, conversationID)
			promotedIDs = append(promotedIDs, itemID)

			return true, nil
		},
		DeleteQueueItemFunc: func(_ context.Context, conversationID, itemID string) (bool, error) {
			if itemID == "missing" {
				return false, nil
			}

			items, err := sessions.ThreadQueueForConversation(conversationID)
			if err != nil {
				return false, fmt.Errorf("list queue: %w", err)
			}

			for i := range items {
				if items[i].ID == itemID {
					return true, sessions.DeleteThreadQueueItem(itemID)
				}
			}

			return false, nil
		},
		ReorderQueueItemsFunc: func(conversationID string, ids []string) error {
			items, err := sessions.ThreadQueueForConversation(conversationID)
			if err != nil {
				return fmt.Errorf("list queue: %w", err)
			}

			byID := make(map[string]protocol.ThreadQueueItem, len(items))
			for i := range items {
				byID[items[i].ID] = items[i]
			}

			for i, itemID := range ids {
				item, ok := byID[itemID]
				if !ok {
					continue
				}

				item.Position = i
				if err := sessions.PutThreadQueueItem(itemID, &item); err != nil {
					return fmt.Errorf("reorder queue: %w", err)
				}
			}

			return nil
		},
		StashQueueItemFunc: func(_ context.Context, conversationID string, item *protocol.ThreadQueueItem) error {
			item.ConversationID = conversationID
			if err := sessions.PutThreadQueueItem(item.ID, item); err != nil {
				return fmt.Errorf("stash queue: %w", err)
			}

			return nil
		},
	}
	channelChoices := []string{"main", "planner"}
	channels := &mockChannels{ChannelAgentChoicesFunc: func(_ context.Context, channel string) ([]string, error) {
		if channel == "C2" {
			return []string{"selected"}, nil
		}

		require.Equal(t, "C1", channel)

		return channelChoices, nil
	}}
	channelTitle := "triage"
	channels.SidebarChannelAgentChoicesFunc = func(_ context.Context, channel string) (string, []string, error) {
		if channel == "C2" {
			return channel, nil, nil
		}

		require.Equal(t, "C1", channel)

		return channelTitle, []string{"main", "planner"}, nil
	}
	cronRunner := &mockCronRunner{RunFunc: func(_ context.Context, agent, prompt string, progress *backend.RawRunProgress) (protocol.CronRunResult, error) {
		require.Equal(t, "planner", agent)
		require.Contains(t, prompt, "Cron body")
		require.Equal(t, "#ops", progress.TextChannel)
		require.Empty(t, progress.SyncDestination)
		require.True(t, strings.HasPrefix(progress.ConversationID, "one-off-cron:cron/alpha.md:"))

		return protocol.CronRunResult{ConversationID: "slack-thread:C1:cron-y"}, nil
	}}
	cronjobs := cronfrontend.New(cfg.Workspace, cfg.RuntimeDirName(), []string{"#ops"}, sessions, cronRunner, slog.New(slog.DiscardHandler))
	require.NoError(t, cronjobs.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, cronjobs.Stop(context.Background())) })

	server := New(core, sessions, cfg, channels, cronjobs)
	socketPath := testSocketPath(t)
	listener, err := Listen(socketPath)
	require.NoError(t, err)

	transport := grpc.NewServer()
	server.Register(transport)

	var serving errgroup.Group
	serving.Go(func() error {
		if err := transport.Serve(listener); err != nil {
			return fmt.Errorf("serve test RPC: %w", err)
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

	var wire ListSessionEntriesResponse

	err = connection.Invoke(metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "192.0.2.2")), "/rpc.Web/ListSessionEntries", &SessionEntriesRequest{Id: "unrelated"}, &wire)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	_, err = invoke[PromptResponse](t.Context(), connection, "Prompt", &PromptRequest{Id: "unrelated", Text: "hello"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	clear(cfg.WebUsers) // The server retains the startup mapping, not mutable config state.

	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "192.0.2.1"))
	identity, err := invoke[IdentityResponse](ctx, connection, "Identity", &IdentityRequest{})
	require.NoError(t, err)
	require.Equal(t, "alice", identity.Username)

	for _, values := range [][]string{nil, {"alice"}, {"192.0.2.2"}, {"192.0.2.1", "192.0.2.1"}} {
		denied := metadata.NewOutgoingContext(t.Context(), metadata.MD{"rocketclaw-principal": values})
		_, err := invoke[IdentityResponse](denied, connection, "Identity", &IdentityRequest{})
		require.Equal(t, codes.Unauthenticated, status.Code(err))
		_, err = invoke[ListSessionsResponse](denied, connection, "ListSessions", &ListSessionsRequest{})
		require.Equal(t, codes.Unauthenticated, status.Code(err))
		_, err = invoke[ListAgentsResponse](denied, connection, "ListAgents", &ListAgentsRequest{ConversationId: "selected"})
		require.Equal(t, codes.Unauthenticated, status.Code(err))
	}

	version, err := invoke[ProtocolResponse](t.Context(), connection, "Protocol", &ProtocolRequest{})
	require.NoError(t, err)
	require.Equal(t, protoSHA256, version.ProtoSha256)

	empty, err := invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
	require.NoError(t, err)
	require.True(t, proto.Equal(&ListSessionsResponse{Owner: "alice", UpstreamSuccess: true, SummariesComplete: true}, empty))

	// Invalid protobuf bytes must fail decoding, even for an otherwise empty request.
	malformedIdentity := &IdentityRequest{}
	malformedIdentity.ProtoReflect().SetUnknown([]byte{0xff})
	_, err = invoke[IdentityResponse](ctx, connection, "Identity", malformedIdentity)
	require.Equal(t, codes.Internal, status.Code(err))

	malformedList := &ListSessionsRequest{}
	malformedList.ProtoReflect().SetUnknown([]byte{0xff})
	_, err = invoke[ListSessionsResponse](ctx, connection, "ListSessions", malformedList)
	require.Equal(t, codes.Internal, status.Code(err))

	missingRequest, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/ListSessions")
	require.NoError(t, err)
	require.NoError(t, missingRequest.CloseSend())
	err = missingRequest.RecvMsg(&ListSessionsResponse{})
	require.Equal(t, codes.Internal, status.Code(err))
	require.ErrorContains(t, err, "received no request message")

	_, err = invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: "unrecorded", Text: "hello"})
	require.ErrorContains(t, err, `conversation "unrecorded" is not recorded`)

	const id = "slack-thread:C1:1.1"

	thread := backend.ThreadState{Agent: "main"}
	require.NoError(t, sessions.UpsertThread(id, thread))
	require.NoError(t, sessions.BeginGoal(id, "keep goal", "", 3, "", ""))
	goal, exists, err := sessions.Goal(id)
	require.NoError(t, err)
	require.True(t, exists)
	before, exists, err := sessions.Thread(id)
	require.NoError(t, err)
	require.True(t, exists)

	entry := rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now().UTC().Add(-8 * 24 * time.Hour)}
	first, err := sessions.AppendEntryID(ctx, id, &entry)
	require.NoError(t, err)
	second, err := sessions.AppendEntryID(ctx, id, &entry)
	require.NoError(t, err)
	unrelated, err := sessions.AppendEntryID(ctx, "unrelated", &entry)
	require.NoError(t, err)

	request := &SessionEntriesRequest{Id: id}
	listed, err := invoke[ListSessionEntriesResponse](ctx, connection, "ListSessionEntries", request)
	require.NoError(t, err)
	require.Len(t, listed.Entries, 2)
	require.Equal(t, first, listed.Entries[0].Id)
	require.Equal(t, second, listed.Entries[1].Id)
	require.Equal(t, "turn", listed.Entries[0].Type)
	require.Equal(t, entry.Timestamp.Format(time.RFC3339Nano), listed.Entries[0].Timestamp)

	loaded, err := invoke[LoadSessionEntriesResponse](ctx, connection, "LoadSessionEntries", request)
	require.NoError(t, err)
	require.Len(t, loaded.Entries, 2)

	for i, row := range loaded.Entries {
		require.Equal(t, listed.Entries[i].Id, row.Id)

		var got rocketcode.SessionEntry
		require.NoError(t, json.Unmarshal([]byte(row.Json), &got))
		require.Equal(t, entry, got)
	}

	for _, tt := range []struct {
		duration string
		settled  bool
	}{{"", true}, {"240h", false}} {
		cfg.Web.AutoSettleAfter = tt.duration
		conversations, err := invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
		require.NoError(t, err)
		require.Len(t, conversations.Sessions, 1)
		require.Equal(t, tt.settled, conversations.Sessions[0].Settled)
	}

	cfg.Web.AutoSettleAfter = ""

	for _, tt := range []struct {
		request *UpdateSessionRequest
		pinned  bool
		name    string
	}{
		{&UpdateSessionRequest{Id: id, Pinned: new(true)}, true, ""},
		{&UpdateSessionRequest{Id: id, Name: new("  Launch notes  ")}, true, "Launch notes"},
		{&UpdateSessionRequest{Id: id, Pinned: new(false)}, false, "Launch notes"},
		{&UpdateSessionRequest{Id: id, Name: new(" ")}, false, ""},
	} {
		_, err = invoke[UpdateSessionResponse](ctx, connection, "UpdateSession", tt.request)
		require.NoError(t, err)
		conversations, err := invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
		require.NoError(t, err)
		require.Len(t, conversations.Sessions, 1)
		require.Equal(t, tt.pinned, conversations.Sessions[0].GetPinned())
		require.Equal(t, tt.name, conversations.Sessions[0].GetName())
		require.Equal(t, !tt.pinned, conversations.Sessions[0].Settled)
		updatedAt, err := time.Parse(time.RFC3339Nano, conversations.Sessions[0].UpdatedAt)
		require.NoError(t, err)
		require.Equal(t, entry.Timestamp.Truncate(time.Microsecond), updatedAt)

		preserved, err := invoke[LoadSessionEntriesResponse](ctx, connection, "LoadSessionEntries", request)
		require.NoError(t, err)
		require.True(t, proto.Equal(loaded, preserved))
	}

	_, err = invoke[UpdateSessionResponse](t.Context(), connection, "UpdateSession", &UpdateSessionRequest{Id: id, Pinned: new(true)})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	for _, tt := range []struct {
		request *UpdateSessionRequest
		code    codes.Code
	}{
		{&UpdateSessionRequest{Id: "missing", Name: new("name")}, codes.NotFound},
		{&UpdateSessionRequest{Id: "cron:private", Pinned: new(true)}, codes.PermissionDenied},
		{&UpdateSessionRequest{Id: id, Name: new("bad\x00name")}, codes.InvalidArgument},
	} {
		_, err = invoke[UpdateSessionResponse](ctx, connection, "UpdateSession", tt.request)
		require.Equal(t, tt.code, status.Code(err))
	}

	for _, settled := range []bool{true, false} {
		_, err = invoke[SettleSessionResponse](ctx, connection, "SettleSession", &SettleSessionRequest{Id: id, Settled: settled})
		require.NoError(t, err)
		conversations, err := invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
		require.NoError(t, err)
		require.Len(t, conversations.Sessions, 1)
		require.Equal(t, id, conversations.Sessions[0].Id)
		require.Equal(t, "main", conversations.Sessions[0].Agent)
		require.Equal(t, settled, conversations.Sessions[0].Settled)

		stored, found, err := sessions.Thread(id)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, backend.ThreadState{Agent: "main", Settled: settled}, stored)

		preservedGoal, found, err := sessions.Goal(id)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, goal, preservedGoal)

		preserved, err := invoke[LoadSessionEntriesResponse](ctx, connection, "LoadSessionEntries", request)
		require.NoError(t, err)
		require.True(t, proto.Equal(loaded, preserved))
	}

	_, err = invoke[SettleSessionResponse](t.Context(), connection, "SettleSession", &SettleSessionRequest{Id: id, Settled: true})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	_, err = invoke[SettleSessionResponse](ctx, connection, "SettleSession", &SettleSessionRequest{Id: "missing", Settled: true})
	require.Equal(t, codes.NotFound, status.Code(err))

	deleted, err := invoke[DeleteSessionEntriesResponse](ctx, connection, "DeleteSessionEntries", request)
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted.Deleted)

	listed, err = invoke[ListSessionEntriesResponse](ctx, connection, "ListSessionEntries", request)
	require.NoError(t, err)
	require.Empty(t, listed.Entries)

	loaded, err = invoke[LoadSessionEntriesResponse](ctx, connection, "LoadSessionEntries", request)
	require.NoError(t, err)
	require.Empty(t, loaded.Entries)
	loaded, err = invoke[LoadSessionEntriesResponse](ctx, connection, "LoadSessionEntries", &SessionEntriesRequest{Id: "unrelated"})
	require.NoError(t, err)
	require.Len(t, loaded.Entries, 1)
	require.Equal(t, unrelated, loaded.Entries[0].Id)

	after, exists, err := sessions.Thread(id)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, before, after)

	afterGoal, exists, err := sessions.Goal(id)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, goal, afterGoal)

	deleted, err = invoke[DeleteSessionEntriesResponse](ctx, connection, "DeleteSessionEntries", request)
	require.NoError(t, err)
	require.Zero(t, deleted.Deleted)

	for _, values := range [][]string{nil, {"alice"}, {"192.0.2.2"}, {"192.0.2.1", "192.0.2.2"}, {""}} {
		unauthorized := metadata.NewOutgoingContext(t.Context(), metadata.MD{"rocketclaw-principal": values})
		_, err = invoke[ListSessionEntriesResponse](unauthorized, connection, "ListSessionEntries", request)
		require.Equal(t, codes.Unauthenticated, status.Code(err))
		_, err = invoke[LoadSessionEntriesResponse](unauthorized, connection, "LoadSessionEntries", request)
		require.Equal(t, codes.Unauthenticated, status.Code(err))
		_, err = invoke[DeleteSessionEntriesResponse](unauthorized, connection, "DeleteSessionEntries", &SessionEntriesRequest{Id: "unrelated"})
		require.Equal(t, codes.Unauthenticated, status.Code(err))
	}

	remaining, err := sessions.ObserveEntries(ctx, "unrelated")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	require.Equal(t, unrelated, remaining[0].ID)

	_, err = invoke[ListSessionEntriesResponse](ctx, connection, "ListSessionEntries", &SessionEntriesRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = invoke[LoadSessionEntriesResponse](ctx, connection, "LoadSessionEntries", &SessionEntriesRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = invoke[DeleteSessionEntriesResponse](ctx, connection, "DeleteSessionEntries", &SessionEntriesRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	principal, err := server.principal(metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "192.0.2.1")))
	require.NoError(t, err)
	require.Equal(t, "alice", principal)
	negotiated, err := invoke[ProtocolResponse](t.Context(), connection, "Protocol", &ProtocolRequest{})
	require.NoError(t, err)
	require.Equal(t, protoSHA256, negotiated.ProtoSha256)

	_, err = invoke[ListQueueResponse](ctx, connection, "ListQueue", &ListQueueRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = invoke[ListQueueResponse](t.Context(), connection, "ListQueue", &ListQueueRequest{Id: id})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	_, err = invoke[QueueItemResponse](t.Context(), connection, "SteerQueueItem", &QueueItemRequest{Id: id, ItemId: "q1"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	require.NoError(t, sessions.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{
		ConversationID: id, Message: "queued later", Principal: "alice", StashAt: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
	}))
	queued, err := invoke[ListQueueResponse](ctx, connection, "ListQueue", &ListQueueRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, []*QueueItem{{Id: "q1", Text: "queued later", Delivery: PromptDelivery_QUEUE}}, queued.Items)
	t.Run("pending steers retain delivery and queue order", func(t *testing.T) {
		list := core.QueueItemsFunc
		defer func() { core.QueueItemsFunc = list }()

		core.QueueItemsFunc = func(string) ([]protocol.ThreadQueueItem, error) {
			return []protocol.ThreadQueueItem{
				{ID: "later-first", Kind: protocol.InboundKindEnqueue, Message: "first later message"},
				{ID: "steering", Kind: protocol.InboundKindSteer, Message: "guide the current response"},
				{ID: "later-second", Kind: protocol.InboundKindEnqueue, Message: "second later message"},
			}, nil
		}
		queued, err := invoke[ListQueueResponse](ctx, connection, "ListQueue", &ListQueueRequest{Id: id})
		require.NoError(t, err)
		require.Equal(t, []*QueueItem{
			{Id: "later-first", Text: "first later message", Delivery: PromptDelivery_QUEUE},
			{Id: "steering", Text: "guide the current response", Delivery: PromptDelivery_STEER},
			{Id: "later-second", Text: "second later message", Delivery: PromptDelivery_QUEUE},
		}, queued.Items)
	})

	storedQueue, err := sessions.ThreadQueueForConversation(id)
	require.NoError(t, err)
	require.Equal(t, "alice", storedQueue[0].Principal)

	_, err = invoke[QueueItemResponse](ctx, connection, "SteerQueueItem", &QueueItemRequest{Id: id, ItemId: "q1"})
	require.NoError(t, err)
	require.Equal(t, []string{id}, promoted)
	require.Equal(t, []string{"q1"}, promotedIDs)

	_, err = invoke[QueueItemResponse](ctx, connection, "SteerQueueItem", &QueueItemRequest{Id: id})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = invoke[QueueItemResponse](ctx, connection, "SteerQueueItem", &QueueItemRequest{Id: id, ItemId: "missing"})
	require.Equal(t, codes.NotFound, status.Code(err))

	require.NoError(t, sessions.PutThreadQueueItem("q2", &protocol.ThreadQueueItem{
		ConversationID: id, Message: "second", Principal: "alice", Position: 0, StashAt: time.Date(2026, 9, 6, 12, 0, 1, 0, time.UTC),
	}))
	require.NoError(t, sessions.PutThreadQueueItem("q3", &protocol.ThreadQueueItem{
		ConversationID: id, Message: "third", Principal: "alice", Position: 1, StashAt: time.Date(2026, 9, 6, 12, 0, 2, 0, time.UTC),
	}))

	_, err = invoke[QueueItemResponse](t.Context(), connection, "RemoveQueueItem", &QueueItemRequest{Id: id, ItemId: "q2"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	_, err = invoke[QueueItemResponse](ctx, connection, "ReorderQueue", &ReorderQueueRequest{Id: id, ItemIds: []string{"q3", "q2"}})
	require.NoError(t, err)

	queued, err = invoke[ListQueueResponse](ctx, connection, "ListQueue", &ListQueueRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, []*QueueItem{{Id: "q1", Text: "queued later", Delivery: PromptDelivery_QUEUE}, {Id: "q3", Text: "third", Delivery: PromptDelivery_QUEUE}, {Id: "q2", Text: "second", Delivery: PromptDelivery_QUEUE}}, queued.Items)

	_, err = invoke[QueueItemResponse](ctx, connection, "RemoveQueueItem", &QueueItemRequest{Id: id, ItemId: "q2"})
	require.NoError(t, err)

	queued, err = invoke[ListQueueResponse](ctx, connection, "ListQueue", &ListQueueRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, []*QueueItem{{Id: "q1", Text: "queued later", Delivery: PromptDelivery_QUEUE}, {Id: "q3", Text: "third", Delivery: PromptDelivery_QUEUE}}, queued.Items)

	_, err = invoke[QueueItemResponse](ctx, connection, "RemoveQueueItem", &QueueItemRequest{Id: id, ItemId: "missing"})
	require.Equal(t, codes.NotFound, status.Code(err))
	_, err = invoke[QueueItemResponse](ctx, connection, "RemoveQueueItem", &QueueItemRequest{Id: id})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = invoke[QueueItemResponse](t.Context(), connection, "ReorderQueue", &ReorderQueueRequest{Id: id, ItemIds: []string{"q3", "q1"}})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	t.Run("queue failures preserve pending work", func(t *testing.T) {
		stash := core.StashQueueItemFunc
		defer func() { core.StashQueueItemFunc = stash }()

		promote, remove, reorder, list := core.PromoteQueueItemFunc, core.DeleteQueueItemFunc, core.ReorderQueueItemsFunc, core.QueueItemsFunc
		defer func() {
			core.PromoteQueueItemFunc, core.DeleteQueueItemFunc, core.ReorderQueueItemsFunc, core.QueueItemsFunc = promote, remove, reorder, list
		}()

		errUnavailable := fmt.Errorf("queue store: %w", status.Error(codes.Unavailable, "queue store unavailable"))
		core.PromoteQueueItemFunc = func(context.Context, string, string) (bool, error) { return false, errUnavailable }
		core.DeleteQueueItemFunc = func(context.Context, string, string) (bool, error) { return false, errUnavailable }
		core.ReorderQueueItemsFunc = func(string, []string) error { return errUnavailable }
		core.QueueItemsFunc = func(string) ([]protocol.ThreadQueueItem, error) { return nil, errUnavailable }
		core.StashQueueItemFunc = func(context.Context, string, *protocol.ThreadQueueItem) error { return errUnavailable }

		for _, test := range []struct {
			method            string
			request, response proto.Message
		}{
			{"SteerQueueItem", &QueueItemRequest{Id: id, ItemId: "q1"}, &QueueItemResponse{}},
			{"RemoveQueueItem", &QueueItemRequest{Id: id, ItemId: "q1"}, &QueueItemResponse{}},
			{"ReorderQueue", &ReorderQueueRequest{Id: id, ItemIds: []string{"q3", "q1"}}, &QueueItemResponse{}},
			{"ListQueue", &ListQueueRequest{Id: id}, &ListQueueResponse{}},
			{"Prompt", &PromptRequest{Id: id, Text: "must not disappear", Delivery: PromptDelivery_QUEUE}, &PromptResponse{}},
		} {
			err := connection.Invoke(ctx, "/rpc.Web/"+test.method, test.request, test.response)
			require.Equal(t, codes.Unavailable, status.Code(err), test.method)
		}

		items, err := sessions.ThreadQueueForConversation(id)
		require.NoError(t, err)
		require.Len(t, items, 2)
		require.Equal(t, "q1", items[0].ID)
		require.Equal(t, "q3", items[1].ID)
	})

	_, err = invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: id, Text: "queued from prompt", Delivery: PromptDelivery_QUEUE})
	require.NoError(t, err)

	queued, err = invoke[ListQueueResponse](ctx, connection, "ListQueue", &ListQueueRequest{Id: id})
	require.NoError(t, err)

	var queuedPrompt *QueueItem

	for _, item := range queued.Items {
		if item.Text == "queued from prompt" {
			queuedPrompt = item
		}
	}

	require.NotNil(t, queuedPrompt)

	storedQueue, err = sessions.ThreadQueueForConversation(id)
	require.NoError(t, err)

	var storedPrompt protocol.ThreadQueueItem

	for i := range storedQueue {
		if storedQueue[i].Message == "queued from prompt" {
			storedPrompt = storedQueue[i]
		}
	}

	require.Equal(t, "alice", storedPrompt.Principal)
	require.Equal(t, protocol.SourceWeb, storedPrompt.Source)

	_, err = invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: id, Text: "$stop later"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: id, Text: "$stop"})
	require.NoError(t, err)
	require.Equal(t, protocol.InboundKindCancel, turns[len(turns)-1].Kind)
	require.Equal(t, id, turns[len(turns)-1].ConversationID)
	require.Equal(t, "alice", turns[len(turns)-1].Label)

	// Discovery starts from recorded conversations, never orphaned entry rows.
	for _, hidden := range []string{"private-X", "cron:cron/daily.md:20000102T030405.000000006Z:a", "one-off-cron:cron/daily.md:20000102T030405.000000006Z:b"} {
		require.NoError(t, sessions.UpsertThread(hidden, backend.ThreadState{Agent: "producer"}))
	}

	require.NoError(t, sessions.UpsertExternalMCPSession("external", &backend.ExternalMCPSessionState{PrivateConversationID: "private-X", ManagedConversationID: id, Agent: "producer", SlackChannel: "#ops"}))
	require.NoError(t, sessions.UpsertThread("empty-web", backend.ThreadState{Agent: "selected"}))

	listedSessions, err := invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
	require.NoError(t, err)
	require.Len(t, listedSessions.Sessions, 2)
	require.Equal(t, "empty-web", listedSessions.Sessions[0].Id)
	require.Equal(t, "selected", listedSessions.Sessions[0].Agent)
	require.Equal(t, id, listedSessions.Sessions[1].Id)
	require.True(t, listedSessions.SummariesComplete)

	for _, session := range listedSessions.Sessions {
		require.Empty(t, session.Preview)
		require.Empty(t, session.UpdatedAt, "empty and deleted conversations retain blank display timestamps")
	}

	// A legacy empty row stays visible while its summary is still loading.
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.ExecContext(ctx, `DELETE FROM session_summaries WHERE conversation_id = 'empty-web'`)
	require.NoError(t, err)
	loading, err := invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
	require.NoError(t, err)
	require.True(t, loading.UpstreamSuccess)
	require.False(t, loading.SummariesComplete)
	require.Equal(t, "empty-web", loading.Sessions[0].Id)
	require.Empty(t, loading.Sessions[0].UpdatedAt)
	require.NoError(t, sessions.UpsertThread("empty-web", backend.ThreadState{Agent: "selected"}))

	emptyHistory, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: id})
	require.NoError(t, err)
	require.Empty(t, emptyHistory.Messages)

	for _, hidden := range []string{"private-X", "cron:cron/daily.md:20000102T030405.000000006Z:a"} {
		_, err = invoke[LoadSessionEntriesResponse](ctx, connection, "LoadSessionEntries", &SessionEntriesRequest{Id: hidden})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		_, err = invoke[ListSessionEntriesResponse](ctx, connection, "ListSessionEntries", &SessionEntriesRequest{Id: hidden})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		_, err = invoke[DeleteSessionEntriesResponse](ctx, connection, "DeleteSessionEntries", &SessionEntriesRequest{Id: hidden})
		require.Equal(t, codes.PermissionDenied, status.Code(err))

		_, err = invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: hidden})
		if hidden == "private-X" {
			require.Equal(t, codes.PermissionDenied, status.Code(err))
		} else {
			require.NoError(t, err)
		}

		_, err = invoke[ListQueueResponse](ctx, connection, "ListQueue", &ListQueueRequest{Id: hidden})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		_, err = invoke[QueueItemResponse](ctx, connection, "SteerQueueItem", &QueueItemRequest{Id: hidden, ItemId: "q1"})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		_, err = invoke[QueueItemResponse](ctx, connection, "RemoveQueueItem", &QueueItemRequest{Id: hidden, ItemId: "q1"})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	}

	historyEntry := rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: entry.Timestamp, ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"developer","content":"private instructions"}`),
		json.RawMessage(`{"type":"message","role":"user","content":"human one"}`),
		json.RawMessage(`{"type":"reasoning","summary":[{"type":"summary_text","text":"**Planning the answer**"}]}`),
		json.RawMessage(`{"type":"function_call","name":"execute","arguments":"{\"code\":\"true\"}"}`),
		json.RawMessage(`{"type":"function_call_output","output":"ok"}`),
		json.RawMessage(`{"type":"message","id":"msg_one","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer one","annotations":[]}]}`),
		json.RawMessage(`{"type":"message","role":"user","content":"[Web media=Text principal=\"alice\" additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nhuman two"}`),
		json.RawMessage(`{"type":"message","role":"assistant","content":"answer two"}`),
		json.RawMessage(`{"type":"function_call","call_id":"report","name":"rocketclaw_i_want_human_partner_to_see_this","arguments":"{\"payload\":\"Exact report\\nwith details\"}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"report","output":"queued for verbatim delivery"}`),
		json.RawMessage(`{"type":"function_call","call_id":"failed","name":"rocketclaw_i_want_human_partner_to_see_this","arguments":"invalid"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"failed","output":"invalid arguments"}`),
		json.RawMessage(`{"type":"message","role":"assistant","content":""}`),
	}}
	_, err = sessions.AppendEntryID(ctx, "empty-web", &historyEntry)
	require.NoError(t, err)
	history, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: "empty-web"})
	require.NoError(t, err)
	require.Equal(t, "report", history.Messages[8].ToolCallId)
	require.Equal(t, "rocketclaw_i_want_human_partner_to_see_this", history.Messages[8].ToolName)
	require.Equal(t, "report", history.Messages[9].ToolCallId)
	require.Empty(t, history.Messages[9].ToolName)

	got := make([]struct{ role, text string }, 0, len(history.Messages))
	for _, message := range history.Messages {
		got = append(got, struct{ role, text string }{message.Role, message.Text})
	}

	require.Equal(t, []struct{ role, text string }{
		{"developer", "private instructions"},
		{"user", "human one"},
		{"thinking", "**Planning the answer**"},
		{"tool", "execute\n{\"code\":\"true\"}"},
		{"tool", "ok"},
		{"assistant", "answer one"},
		{"user", "human two"},
		{"assistant", "answer two"},
		{"tool", "rocketclaw_i_want_human_partner_to_see_this\n{\"payload\":\"Exact report\\nwith details\"}"},
		{"tool", "queued for verbatim delivery"},
		{"tool", "rocketclaw_i_want_human_partner_to_see_this\ninvalid"},
		{"tool", "invalid arguments"},
		{"assistant", "Exact report\nwith details"},
	}, got)

	listedSessions, err = invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
	require.NoError(t, err)
	require.Equal(t, "empty-web", listedSessions.Sessions[0].Id)
	require.Equal(t, "Exact report\nwith details", listedSessions.Sessions[0].Preview)
	require.False(t, listedSessions.Sessions[0].Running)

	err = sessions.UpsertActiveTurn(ctx, &rocketcode.ActiveTurnCheckpoint{TurnID: "sidebar-turn", ConversationKey: "empty-web"}, nil)
	require.NoError(t, err)
	listedSessions, err = invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
	require.NoError(t, err)
	require.True(t, listedSessions.Sessions[0].Running)
	require.NoError(t, sessions.ClearActiveTurn(ctx, "sidebar-turn"))
	listedSessions, err = invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
	require.NoError(t, err)
	require.False(t, listedSessions.Sessions[0].Running)

	storedHistory, err := sessions.ObserveEntries(ctx, "empty-web")
	require.NoError(t, err)
	require.Len(t, storedHistory, 1)
	require.Equal(t, historyEntry.ReplayInput, storedHistory[0].Entry.ReplayInput)

	for _, method := range []string{"History", "ListSessions"} {
		_, err = invoke[HistoryResponse](t.Context(), connection, method, &HistoryRequest{Id: id})
		require.Equal(t, codes.Unauthenticated, status.Code(err))
	}

	// The actual HTTP proxy and TypeScript gRPC client exercise the same storage.
	emptySkills, err := invoke[ListSkillsResponse](ctx, connection, "ListSkills", &ListSkillsRequest{})
	require.NoError(t, err)
	require.Empty(t, emptySkills.Skills)

	emptyConfig, err := invoke[ListConfigResponse](ctx, connection, "ListConfig", &ListConfigRequest{})
	require.NoError(t, err)
	require.Empty(t, emptyConfig.Config.Models)
	require.Empty(t, emptyConfig.Config.Overlays)
	require.Empty(t, emptyConfig.Config.McpServers)
	require.Equal(t, "168h0m0s", emptyConfig.Config.GetWebAutoSettleAfter())

	for _, method := range []string{"ListConfig", "ListSkills"} {
		_, err = invoke[ListConfigResponse](t.Context(), connection, method, &ListConfigRequest{})
		require.Equal(t, codes.Unauthenticated, status.Code(err))
	}

	cfg.Overlays = []string{"local-overlay"}
	cfg.Web.AutoSettleAfter = "1h30m"
	cfg.Models = map[string]string{"zeta": "gpt-5.5", "alpha": "gpt-5.4"}
	cfg.Logging.Level, cfg.AutoApproverModel = "info", "gpt-5.5"
	cfg.MCPExternal.Enabled, cfg.Instrumentation.Enabled = true, true
	cfg.OpenAI.APIKey, cfg.OpenAI.RocketCodeAuth = "secret-provider-key", "secret-auth-path"
	cfg.Slack.BotToken, cfg.Slack.AppToken = "secret-bot-token", "secret-app-token"
	cfg.Instrumentation.APIKey = "secret-telemetry-key"
	cfg.Environment = []string{"TOKEN=secret-environment"}
	cfg.MCPServers = map[string]config.MCPServerConfig{
		"zeta":  {URL: "https://secret-endpoint", Headers: map[string]string{"Authorization": "secret-header"}},
		"alpha": {Command: "secret-command", Args: []string{"secret-argument"}, Env: map[string]string{"TOKEN": "secret-server-env"}},
	}
	view, err := invoke[ListConfigResponse](ctx, connection, "ListConfig", &ListConfigRequest{})
	require.NoError(t, err)

	wantConfig := &ConfigView{Workspace: cfg.Workspace, Overlays: []string{"local-overlay"}, Models: []*ConfigModel{{Name: "alpha", Model: "gpt-5.4"}, {Name: "zeta", Model: "gpt-5.5"}}, SlackChannels: []*ConfigChannel{{Channel: "#ops", Agents: []string{"main"}}}, McpServers: []string{"alpha", "zeta"}, LoggingLevel: "info", AutoApproverModel: "gpt-5.5", InstrumentationEnabled: true, McpExternal: true, WebAutoSettleAfter: "1h30m0s"}
	require.True(t, proto.Equal(wantConfig, view.Config), "unexpected config view: %v", view.Config)
	encodedView, err := proto.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(encodedView), "secret-")
	require.NotContains(t, string(encodedView), "postgres://")
	require.NotContains(t, string(encodedView), "U123")
	require.NotContains(t, string(encodedView), "alice")

	for _, name := range []string{"zeta", "alpha"} {
		dir := filepath.Join(cfg.RuntimeDirName(), "skills", name)
		require.NoError(t, root.MkdirAll(dir, 0o700))
		require.NoError(t, root.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Read-only skill\nlicense: MIT\ncompatibility: Unix\n---\n# Instructions\nKeep [literal] text.\n"), 0o600))
	}

	outside := t.TempDir()
	outsideRoot, err := os.OpenRoot(outside)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, outsideRoot.Close()) })
	require.NoError(t, outsideRoot.WriteFile("SKILL.md", []byte("---\nname: escape\ndescription: Outside workspace\n---\nsecret-outside-workspace\n"), 0o600))
	require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "skills", "escape"), 0o700))
	require.NoError(t, root.Symlink(filepath.Join(outside, "SKILL.md"), filepath.Join(cfg.RuntimeDirName(), "skills", "escape", "SKILL.md")))

	skills, err := invoke[ListSkillsResponse](ctx, connection, "ListSkills", &ListSkillsRequest{})
	require.NoError(t, err)
	require.Len(t, skills.Skills, 2)

	for i, name := range []string{"alpha", "zeta"} {
		want := &Skill{Name: name, Description: "Read-only skill", License: "MIT", Compatibility: "Unix", Content: "# Instructions\nKeep [literal] text.\n", Origin: name + "/SKILL.md"}
		require.True(t, proto.Equal(want, skills.Skills[i]), "unexpected skill: %v", skills.Skills[i])
	}

	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "planner.md"), []byte("---\nmodel: gpt-5.5\npermission:\n  skill: {alpha: allow, zeta: auto}\n---\nHelp."), 0o600))
	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "selected.md"), []byte("---\nmodel: gpt-5.5\npermission:\n  skill: {alpha: deny, zeta: allow}\n---\nHelp."), 0o600))

	for _, tc := range []struct {
		agent string
		want  []string
	}{{"planner", []string{"alpha"}}, {"selected", []string{"zeta"}}, {"main", nil}, {"missing", nil}} {
		filtered, err := invoke[ListSkillsResponse](ctx, connection, "ListSkills", &ListSkillsRequest{Agent: tc.agent})
		require.NoError(t, err)

		var names []string

		for _, skill := range filtered.Skills {
			names = append(names, skill.Name)
		}

		require.Equal(t, tc.want, names, tc.agent)
	}

	_, err = sessions.AppendEntryID(ctx, id, &entry)
	require.NoError(t, err)
	httpServer := startHTTPTestServer(t, connection)

	proxy := exec.CommandContext(t.Context(), "bun", "test", "src/entry-transport.test.ts")
	proxy.Dir = "../../web"

	proxy.Env = append(os.Environ(), "ROCKETCLAW_TEST_HTTP_URL="+httpServer.URL, "ROCKETCLAW_ENTRY_TEST_ID="+id, "ROCKETCLAW_HISTORY_TEST_ID=empty-web", "ROCKETCLAW_VIEW_TEST_WORKSPACE="+cfg.Workspace)
	output, err := proxy.CombinedOutput()
	require.NoError(t, err, "%s", output)
	t.Log(string(output))

	const webHeader = "[Web media=Text principal=\"alice\" additional_instructions=\"Reply plainly.\"]\n\n"
	for i, tc := range []struct{ input, want string }{
		{webHeader + "[literal]\n\nkeep my brackets", "[literal]\n\nkeep my brackets"},
		{"[Web media=Text principal=alice]\n\nnot a generated header", "[Web media=Text principal=alice]\n\nnot a generated header"},
		{webHeader + webHeader + "quoted header", webHeader + "quoted header"},
	} {
		replay, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
			{OfMessage: &responses.EasyInputMessageParam{Role: "user", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(tc.input)}, Type: "message"}},
			{OfMessage: &responses.EasyInputMessageParam{Role: "assistant", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(webHeader + "assistant unchanged")}, Type: "message"}},
		})
		require.NoError(t, err)
		_, err = sessions.AppendEntryID(ctx, "empty-web", &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: entry.Timestamp.Add(time.Duration(i+1) * time.Second), ReplayInput: replay})
		require.NoError(t, err)
		history, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: "empty-web"})
		require.NoError(t, err)
		require.Equal(t, tc.want, history.Messages[len(history.Messages)-2].Text)
		require.Equal(t, webHeader+"assistant unchanged", history.Messages[len(history.Messages)-1].Text)

		listed, err := invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
		require.NoError(t, err)
		require.Equal(t, "empty-web", listed.Sessions[0].Id)
		require.Equal(t, webHeader+"assistant unchanged", listed.Sessions[0].Preview)

		stored, err := sessions.ObserveEntries(ctx, "empty-web")
		require.NoError(t, err)
		require.Equal(t, replay, stored[len(stored)-1].Entry.ReplayInput)
	}

	remaining, err = sessions.ObserveEntries(ctx, id)
	require.NoError(t, err)
	require.Empty(t, remaining)

	after, exists, err = sessions.Thread(id)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, before, after)

	afterGoal, exists, err = sessions.Goal(id)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, goal, afterGoal)

	remaining, err = sessions.ObserveEntries(ctx, "unrelated")
	require.NoError(t, err)
	require.Len(t, remaining, 1)

	catalog, err := invoke[ListAgentsResponse](ctx, connection, "ListAgents", &ListAgentsRequest{})
	require.NoError(t, err)
	require.Len(t, catalog.Agents, 3)

	for i, name := range []string{"main", "planner", "selected"} {
		require.Equal(t, name, catalog.Agents[i].Name)
	}

	created, err := invoke[CreateSessionResponse](ctx, connection, "CreateSession", &CreateSessionRequest{Agent: "planner"})
	require.NoError(t, err)
	require.NotEmpty(t, created.Id)
	require.NotContains(t, created.Id, ":")
	createdThread, found, err := sessions.Thread(created.Id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, backend.ThreadState{Agent: "planner", CreatedBy: "alice"}, createdThread)
	_, err = invoke[CreateSessionResponse](ctx, connection, "CreateSession", &CreateSessionRequest{Agent: "missing"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	historyBefore, err := sessions.ObserveEntries(ctx, "empty-web")
	require.NoError(t, err)

	for _, selectedID := range []string{created.Id, "empty-web", id} {
		_, err = invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: selectedID, Text: "$agent planner"})
		require.NoError(t, err)
		selected, found, err := sessions.Thread(selectedID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "planner", selected.Agent)
	}

	channelChoices = []string{"main"}
	liveCalls := len(channels.ChannelAgentChoicesCalls())
	sidebarCalls := len(channels.SidebarChannelAgentChoicesCalls())

	for _, tc := range []struct {
		id, agent string
		code      codes.Code
	}{
		{id, "planner", codes.InvalidArgument},
		{created.Id, "selected", codes.OK},
		{"empty-web", "missing", codes.InvalidArgument},
		{"private-X", "main", codes.PermissionDenied},
		{"cron:cron/daily.md:20000102T030405.000000006Z:a", "main", codes.PermissionDenied},
		{"unrecorded", "main", codes.NotFound},
	} {
		_, err = invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: tc.id, Text: "$agent " + tc.agent})
		require.Equal(t, tc.code, status.Code(err))
	}

	for _, extra := range []string{"slack-thread:C1:2.2", "slack-thread:C2:3.3"} {
		require.NoError(t, sessions.UpsertThread(extra, backend.ThreadState{Agent: "main"}))
	}

	require.Len(t, channels.ChannelAgentChoicesCalls(), liveCalls+1, "$agent uses current live channel policy")
	require.Len(t, channels.SidebarChannelAgentChoicesCalls(), sidebarCalls, "$agent must not authorize from stored sidebar facts")

	channels.ChannelAgentChoicesFunc = func(context.Context, string) ([]string, error) {
		return nil, errors.New("Slack unavailable")
	}
	channelCalls := len(channels.ChannelAgentChoicesCalls())
	listedSessions, err = invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
	require.NoError(t, err)
	require.Len(t, channels.ChannelAgentChoicesCalls(), channelCalls, "sidebar must not call live Slack policy")
	require.Len(t, channels.SidebarChannelAgentChoicesCalls(), sidebarCalls+2, "resolve each stored Slack channel once per list request")
	require.Len(t, listedSessions.Sessions, 5)
	require.Equal(t, "slack-thread:C1:2.2", listedSessions.Sessions[3].Id)
	require.Equal(t, "triage", listedSessions.Sessions[3].Title)
	require.Equal(t, []string{"main", "planner"}, listedSessions.Sessions[3].AllowedAgents)
	require.Equal(t, "slack-thread:C2:3.3", listedSessions.Sessions[4].Id)
	require.Equal(t, "C2", listedSessions.Sessions[4].Title)
	require.Empty(t, listedSessions.Sessions[4].AllowedAgents, "unknown channel must not guess policy")
	require.Equal(t, identity.Username, listedSessions.Owner)
	require.True(t, listedSessions.UpstreamSuccess)
	require.True(t, listedSessions.SummariesComplete)

	channelTitle = "renamed"
	listedSessions, err = invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
	require.NoError(t, err)

	for _, session := range listedSessions.Sessions {
		switch channel, _, slack := protocol.SlackThreadTarget(session.Id); {
		case !slack:
			require.Empty(t, session.Title)
		case channel == "C1":
			require.Equal(t, "renamed", session.Title)
			require.Equal(t, []string{"main", "planner"}, session.AllowedAgents)
		case channel == "C2":
			require.Equal(t, "C2", session.Title)
			require.Empty(t, session.AllowedAgents)
		}
	}

	require.Len(t, channels.SidebarChannelAgentChoicesCalls(), sidebarCalls+4, "reuse both name and choices per channel on refresh")
	require.Len(t, channels.ChannelAgentChoicesCalls(), channelCalls)

	choices, err := invoke[ListAgentsResponse](ctx, connection, "ListAgents", &ListAgentsRequest{ConversationId: id})
	require.NoError(t, err)
	require.Equal(t, "planner", choices.CurrentAgent)
	require.Equal(t, []string{"main", "planner"}, []string{choices.Agents[0].Name, choices.Agents[1].Name})
	require.Len(t, channels.ChannelAgentChoicesCalls(), channelCalls)
	require.NoError(t, root.Remove(filepath.Join(cfg.RuntimeDirName(), "agents", "planner.md")))

	choices, err = invoke[ListAgentsResponse](ctx, connection, "ListAgents", &ListAgentsRequest{ConversationId: id})
	require.NoError(t, err)
	require.Len(t, choices.Agents, 1)
	require.Equal(t, "main", choices.Agents[0].Name)
	require.Equal(t, "planner", choices.CurrentAgent, "stored current agent survives removal from the loaded choices")
	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "planner.md"), []byte("---\nmodel: gpt-5.5\n---\nHelp."), 0o600))

	for _, hidden := range []string{"private-X", "cron:cron/daily.md:20000102T030405.000000006Z:a", "one-off-cron:cron/daily.md:20000102T030405.000000006Z:b"} {
		_, err := invoke[ListAgentsResponse](ctx, connection, "ListAgents", &ListAgentsRequest{ConversationId: hidden})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	}

	for _, session := range listedSessions.Sessions {
		if session.Id == id {
			require.Equal(t, "planner", session.Agent)
			require.Equal(t, []string{"main", "planner"}, session.AllowedAgents)
		}

		if session.Id == created.Id {
			require.Equal(t, "selected", session.Agent)
			require.Equal(t, []string{"main", "planner", "selected"}, session.AllowedAgents)
		}

		require.NotEqual(t, "private-X", session.Id)
	}

	_, err = invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: id, Text: "$agent main"})
	require.ErrorContains(t, err, "Slack unavailable")
	unchanged, found, err := sessions.Thread(id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "planner", unchanged.Agent, "failed live authorization must not switch the agent")

	historyAfter, err := sessions.ObserveEntries(ctx, "empty-web")
	require.NoError(t, err)
	require.Equal(t, historyBefore, historyAfter)

	private, found, err := sessions.Thread("private-X")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "producer", private.Agent)

	largePreview := strings.Repeat("x", (2<<20)+1)
	largeReplay, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		{OfMessage: &responses.EasyInputMessageParam{Role: "user", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(largePreview)}, Type: "message"}},
	})
	require.NoError(t, err)

	for _, conversationID := range []string{"large-a", "large-b"} {
		require.NoError(t, sessions.UpsertThread(conversationID, backend.ThreadState{Agent: "selected"}))
		_, err = sessions.AppendEntryID(ctx, conversationID, &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: entry.Timestamp.Add(time.Hour), ReplayInput: largeReplay})
		require.NoError(t, err)
	}

	listedSessions, err = invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
	require.NoError(t, err)
	require.Greater(t, proto.Size(listedSessions), 4<<20)

	for _, session := range listedSessions.Sessions[:2] {
		require.Equal(t, largePreview, session.Preview)
	}

	emptyJobs, err := invoke[ListCronJobsResponse](ctx, connection, "ListCronJobs", &ListCronJobsRequest{})
	require.NoError(t, err)
	require.Empty(t, emptyJobs.Jobs)
	require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "cron"), 0o700))

	for _, name := range []string{"zeta", "alpha"} {
		require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "cron", name+".md"), []byte("---\nschedule: 24h\nagent: planner\nchannel: '#ops'\n---\nCron body\n"), 0o600))
	}

	next := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	require.NoError(t, sessions.SyncCronSchedules([]backend.CronScheduleState{{ScheduleID: "cron/alpha.md#0#24h", RelativePath: "cron/alpha.md", NextDue: next}, {ScheduleID: "cron/zeta.md#0#24h", RelativePath: "cron/zeta.md", NextDue: next}}, time.Now()))

	jobs, err := invoke[ListCronJobsResponse](ctx, connection, "ListCronJobs", &ListCronJobsRequest{})
	require.NoError(t, err)
	require.Len(t, jobs.Jobs, 2)

	for i, name := range []string{"alpha", "zeta"} {
		require.True(t, proto.Equal(&CronJob{Stem: name, Schedule: "24h", Body: "Cron body\n", Agent: "planner", Channel: "#ops", Origin: "cron/" + name + ".md", NextRun: next.Format(time.RFC3339Nano), Upcoming: []string{next.Format(time.RFC3339Nano)}}, jobs.Jobs[i]), "unexpected job: %v", jobs.Jobs[i])
	}

	// Seed the exact persisted relation written by Sync, without changing Sync.
	sources := []string{"cron:cron/report:daily.md:20260905T010000.000000001Z:first", "one-off-cron:cron/report_daily.md:20260905T020000.000000002Z:second"}
	for _, source := range sources {
		require.NoError(t, sessions.UpsertThread(source, backend.ThreadState{Agent: "producer"}))

		runEntry := entry
		runEntry.ReplayInput = []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"` + source + `"}`)}
		sourceEntry, err := sessions.AppendEntryID(ctx, source, &runEntry)
		require.NoError(t, err)
		trace, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: source})
		require.NoError(t, err)
		require.Len(t, trace.Messages, 1)
		require.Equal(t, source, trace.Messages[0].Text)

		for range 2 {
			destinationEntry, err := sessions.AppendEntryID(ctx, id, &runEntry)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `UPDATE session_entries SET entry_json = (entry_json::jsonb || jsonb_build_object('sync_source_entry_id', $1::bigint))::text WHERE id = $2`, sourceEntry, destinationEntry)
			require.NoError(t, err)
		}

		for _, hiddenDestination := range []string{"private-X", "unrecorded-history-destination"} {
			destinationEntry, err := sessions.AppendEntryID(ctx, hiddenDestination, &entry)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `UPDATE session_entries SET entry_json = (entry_json::jsonb || jsonb_build_object('sync_source_entry_id', $1::bigint))::text WHERE id = $2`, sourceEntry, destinationEntry)
			require.NoError(t, err)
		}
	}

	observed, err := sessions.ObserveEntries(ctx, id)
	require.NoError(t, err)
	require.Len(t, observed, 4)
	require.Equal(t, sources[0], observed[0].SourceConversationID)
	require.Equal(t, sources[1], observed[3].SourceConversationID)
	preview, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: id, SourceConversationId: sources[0]})
	require.NoError(t, err)
	require.Len(t, preview.Messages, 2)

	for _, message := range preview.Messages {
		require.Equal(t, sources[0], message.Text)
	}

	preview, err = invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: id, SourceConversationId: "absent"})
	require.NoError(t, err)
	require.Empty(t, preview.Messages)
	// A preview must not decode replay content belonging to another run.
	_, err = db.ExecContext(ctx, `UPDATE session_entries SET entry_json = jsonb_set(entry_json::jsonb, '{replay_input}', '[{"type":"function_call","call_id":"broken","name":"rocketclaw_i_want_human_partner_to_see_this","arguments":"invalid"},{"type":"function_call_output","call_id":"broken","output":"queued for verbatim delivery"}]')::text WHERE id = $1`, observed[3].ID)
	require.NoError(t, err)
	preview, err = invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: id, SourceConversationId: sources[0]})
	require.NoError(t, err)
	require.Len(t, preview.Messages, 2)

	_, err = invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: id, SourceConversationId: sources[1]})
	require.ErrorContains(t, err, "decode delivery report")
	_, err = db.ExecContext(ctx, `UPDATE session_entries SET entry_json = jsonb_set(entry_json::jsonb, '{replay_input}', (SELECT entry_json::jsonb->'replay_input' FROM session_entries WHERE id = $1))::text WHERE id = $2`, observed[2].ID, observed[3].ID)
	require.NoError(t, err)

	jobs, err = invoke[ListCronJobsResponse](ctx, connection, "ListCronJobs", &ListCronJobsRequest{})
	require.NoError(t, err)
	require.Len(t, jobs.Jobs, 4) // Two definitions and two runs, not four copied entries.

	for i, stem := range []string{"report_daily", "report:daily"} {
		run := jobs.Jobs[i+2]
		require.Equal(t, "ran", run.Status)
		require.Equal(t, stem, run.Stem)
		require.Equal(t, id, run.NextRun)
		require.Empty(t, run.Body)
	}

	require.Equal(t, "2026-09-05T02:00:00.000000002Z", jobs.Jobs[2].LastRun)
	require.Equal(t, "2026-09-05T01:00:00.000000001Z", jobs.Jobs[3].LastRun)

	again, err := invoke[ListCronJobsResponse](ctx, connection, "ListCronJobs", &ListCronJobsRequest{})
	require.NoError(t, err)
	require.True(t, proto.Equal(jobs, again))

	undelivered := "cron:cron/silent.md:20260905T030000.000000003Z:third"
	require.NoError(t, sessions.UpsertThread(undelivered, backend.ThreadState{Agent: "producer"}))

	traceEntry := entry
	traceEntry.ReplayInput = []json.RawMessage{
		json.RawMessage(`{"type":"function_call","call_id":"silent-tool","name":"inspect","arguments":"{}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"silent-tool","output":"Nothing to deliver"}`),
	}
	traceEntryID, err := sessions.AppendEntryID(ctx, undelivered, &traceEntry)
	require.NoError(t, err)
	withSilent, err := invoke[ListCronJobsResponse](ctx, connection, "ListCronJobs", &ListCronJobsRequest{})
	require.NoError(t, err)
	require.Len(t, withSilent.Jobs, 5)
	require.True(t, proto.Equal(&CronJob{Stem: "silent", Status: "ran", LastRun: "2026-09-05T03:00:00.000000003Z", Origin: undelivered}, withSilent.Jobs[2]))
	trace, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: undelivered})
	require.NoError(t, err)
	require.Len(t, trace.Messages, 2)
	require.Equal(t, "tool", trace.Messages[0].Role)
	require.Equal(t, "inspect", trace.Messages[0].ToolName)
	require.Equal(t, "Nothing to deliver", trace.Messages[1].Text)
	_, err = invoke[HistoryResponse](t.Context(), connection, "History", &HistoryRequest{Id: undelivered})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	t.Run("open cron as writable web chat", func(t *testing.T) {
		path := filepath.Join(cfg.RuntimeDirName(), "cron", "silent.md")

		require.NoError(t, root.WriteFile(path, []byte("---\nschedule: 24h\nagent: planner\nchannel: '#ops'\n---\nSilent\n"), 0o600))
		defer func() { require.NoError(t, root.Remove(path)) }()

		originalAgents := cfg.Slack.Channels[0].Agents
		defer func() { cfg.Slack.Channels[0].Agents = originalAgents }()

		cfg.Slack.Channels[0].Agents = []string{"missing", "selected", "main"}
		request := &CreateSessionRequest{SourceConversationId: undelivered}
		core.SyncConversationFunc = func(context.Context, string, string) error { return errors.New("sync interrupted") }
		_, err := invoke[CreateSessionResponse](ctx, connection, "CreateSession", request)
		require.ErrorContains(t, err, "sync interrupted")
		// The RPC delegates history copying to SyncConversation; its transaction and
		// concurrent deduplication are covered by backend runtime tests.
		core.SyncConversationFunc = func(context.Context, string, string) error { return nil }

		var opens errgroup.Group

		ids := make([]string, 4)
		for i := range ids {
			opens.Go(func() error {
				opened, err := invoke[CreateSessionResponse](ctx, connection, "CreateSession", request)
				if err == nil {
					ids[i] = opened.Id
				}

				return err
			})
		}

		require.NoError(t, opens.Wait())

		for _, opened := range ids {
			require.Equal(t, ids[0], opened)
		}

		webID := ids[0]
		_, _, slack := protocol.SlackThreadTarget(webID)
		require.False(t, slack)

		thread, found, err := sessions.Thread(webID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, backend.ThreadState{Agent: "selected", CreatedBy: "alice"}, thread)
		require.Len(t, core.SyncConversationCalls(), len(ids)+1)

		for _, call := range core.SyncConversationCalls() {
			require.Equal(t, undelivered, call.S)
			require.Equal(t, webID, call.S1)
		}

		conversations, err := rt.ListConversations(ctx)
		require.NoError(t, err)

		count := 0

		for _, conversation := range conversations {
			if conversation.ID == webID {
				count++
			}
		}

		require.Equal(t, 1, count)
		// Seed the persisted provenance produced by the real SyncConversation.
		_, err = db.ExecContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) SELECT $1, (entry_json::jsonb || jsonb_build_object('sync_source_entry_id', id))::text, entry_timestamp FROM session_entries WHERE id=$2`, webID, traceEntryID)
		require.NoError(t, err)
		history, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: webID})
		require.NoError(t, err)
		require.True(t, proto.Equal(trace, history))

		for _, test := range []struct {
			name, channel string
			agents, want  []string
		}{
			{"current config", "#ops", []string{"selected", "main"}, []string{"main", "selected"}},
			{"config changed", "#ops", []string{"planner"}, []string{"planner"}},
			{"no loaded channel agents", "#ops", []string{"missing"}, []string{"main", "planner", "selected"}},
			{"unmapped channel", "#unknown", []string{"planner"}, []string{"main", "planner", "selected"}},
			{"missing definition", "", []string{"planner"}, []string{"main", "planner", "selected"}},
		} {
			t.Run(test.name, func(t *testing.T) {
				cfg.Slack.Channels[0].Agents = test.agents
				if test.channel == "" {
					require.NoError(t, root.Remove(path))
					defer func() { require.NoError(t, root.WriteFile(path, []byte("removed definition"), 0o600)) }()
				} else {
					require.NoError(t, root.WriteFile(path, []byte("---\nschedule: 24h\nagent: planner\nchannel: '"+test.channel+"'\n---\nSilent\n"), 0o600))
				}

				catalog, err := invoke[ListAgentsResponse](ctx, connection, "ListAgents", &ListAgentsRequest{ConversationId: webID})
				require.NoError(t, err)

				names := make([]string, 0, len(catalog.Agents))
				for _, agent := range catalog.Agents {
					names = append(names, agent.Name)
				}

				require.Equal(t, test.want, names)

				listed, err := invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
				require.NoError(t, err)

				index := slices.IndexFunc(listed.Sessions, func(session *Session) bool { return session.Id == webID })
				require.NotEqual(t, -1, index)
				require.ElementsMatch(t, test.want, listed.Sessions[index].AllowedAgents)

				for _, agent := range []string{"main", "planner", "selected"} {
					_, err := invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: webID, Text: "$agent " + agent})
					if slices.Contains(test.want, agent) {
						require.NoError(t, err)
					} else {
						require.Equal(t, codes.InvalidArgument, status.Code(err))
					}
				}
			})
		}

		reopened, err := invoke[CreateSessionResponse](ctx, connection, "CreateSession", request)
		require.NoError(t, err)
		require.Equal(t, webID, reopened.Id)
		thread, _, err = sessions.Thread(webID)
		require.NoError(t, err)
		require.Equal(t, "selected", thread.Agent, "reopening preserves the user's selection")

		_, err = invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: webID, Text: "Continue this run", Delivery: PromptDelivery_QUEUE})
		require.NoError(t, err)
		queue, err := sessions.ThreadQueueForConversation(webID)
		require.NoError(t, err)
		require.Len(t, queue, 1)
		require.Equal(t, "Continue this run", queue[0].Message)
		require.Equal(t, protocol.SourceWeb, queue[0].Source)
		require.Equal(t, "alice", queue[0].Principal)

		for _, test := range []struct {
			source string
			code   codes.Code
		}{{"private-X", codes.InvalidArgument}, {id, codes.InvalidArgument}, {"cron:invalid", codes.InvalidArgument}, {undelivered + "-missing", codes.NotFound}} {
			_, err := invoke[CreateSessionResponse](ctx, connection, "CreateSession", &CreateSessionRequest{SourceConversationId: test.source})
			require.Equal(t, test.code, status.Code(err))
		}

		_, err = invoke[CreateSessionResponse](t.Context(), connection, "CreateSession", request)
		require.Equal(t, codes.Unauthenticated, status.Code(err))
		require.Empty(t, cronRunner.RunCalls(), "opening history must not rerun cron")
	})

	_, err = sessions.DeleteSession(ctx, undelivered)
	require.NoError(t, err)

	deletedSourceEntries, err := sessions.DeleteSession(ctx, sources[0])
	require.NoError(t, err)
	require.EqualValues(t, 1, deletedSourceEntries)

	remainingRuns, err := invoke[ListCronJobsResponse](ctx, connection, "ListCronJobs", &ListCronJobsRequest{})
	require.NoError(t, err)
	require.Len(t, remainingRuns.Jobs, 3)
	require.True(t, proto.Equal(jobs.Jobs[2], remainingRuns.Jobs[2]))

	for range 2 {
		ran, err := invoke[RunCronJobResponse](ctx, connection, "RunCronJob", &RunCronJobRequest{Stem: "alpha"})
		require.NoError(t, err)
		require.Equal(t, "slack-thread:C1:cron-y", ran.Id)
	}

	calls := cronRunner.RunCalls()
	require.Len(t, calls, 2)
	require.NotEqual(t, calls[0].RawRunProgress.ConversationID, calls[1].RawRunProgress.ConversationID)

	cronRunner.RunFunc = func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		return protocol.CronRunResult{}, errors.New("cron execution failed")
	}
	failed, err := invoke[RunCronJobResponse](ctx, connection, "RunCronJob", &RunCronJobRequest{Stem: "alpha"})
	require.ErrorContains(t, err, "cron execution failed")
	require.Nil(t, failed)

	_, err = invoke[RunCronJobResponse](ctx, connection, "RunCronJob", &RunCronJobRequest{Stem: "../escape"})
	require.ErrorContains(t, err, "nested paths are not allowed")

	for _, method := range []string{"ListCronJobs", "RunCronJob"} {
		_, err = invoke[RunCronJobResponse](t.Context(), connection, method, &RunCronJobRequest{Stem: "alpha"})
		require.Equal(t, codes.Unauthenticated, status.Code(err))
	}

	// A missing projection is not a complete snapshot, even when enumeration succeeds.
	_, err = db.ExecContext(ctx, `DELETE FROM session_summaries WHERE conversation_id = $1`, id)
	require.NoError(t, err)
	incomplete, err := invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
	require.NoError(t, err)
	require.True(t, incomplete.UpstreamSuccess)
	require.False(t, incomplete.SummariesComplete)

	for _, row := range incomplete.Sessions {
		if row.Id == id {
			require.Empty(t, row.Preview)
			require.Empty(t, row.UpdatedAt)
		}
	}

	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "cron", "bad.md"), []byte("not a cron definition"), 0o600))

	_, err = invoke[ListCronJobsResponse](ctx, connection, "ListCronJobs", &ListCronJobsRequest{})
	require.ErrorContains(t, err, "bad.md")

	channels.SidebarChannelAgentChoicesFunc = func(context.Context, string) (string, []string, error) {
		return "", nil, errors.New("stored channel facts unavailable")
	}
	_, err = invoke[ListAgentsResponse](ctx, connection, "ListAgents", &ListAgentsRequest{ConversationId: id})
	require.ErrorContains(t, err, "stored channel facts unavailable")

	// Web rows arrive before the Slack facts failure; no success terminal follows.
	stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/ListSessions")
	require.NoError(t, err)
	require.NoError(t, stream.SendMsg(&ListSessionsRequest{}))
	require.NoError(t, stream.CloseSend())

	prefix := 0

	for {
		var batch ListSessionsResponse

		err := stream.RecvMsg(&batch)
		if err != nil {
			require.ErrorContains(t, err, "stored channel facts unavailable")
			break
		}

		require.False(t, batch.UpstreamSuccess)
		require.Equal(t, "alice", batch.Owner)
		require.Len(t, batch.Sessions, 1)

		prefix++
	}

	require.Positive(t, prefix)

	for i, arguments := range []string{`{}`, `null`, `{"payload":null}`, `{"payload":""}`, `{"payload":"report"}`} {
		replay := []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"assistant","content":"report"}`),
			json.RawMessage(`{"type":"function_call","call_id":"first","name":"rocketclaw_i_want_human_partner_to_see_this","arguments":"{\"payload\":\"superseded\"}"}`),
			json.RawMessage(`{"type":"function_call_output","call_id":"first","output":"queued for verbatim delivery"}`),
			json.RawMessage(fmt.Sprintf(`{"type":"function_call","call_id":"last","name":"rocketclaw_i_want_human_partner_to_see_this","arguments":%q}`, arguments)),
			json.RawMessage(`{"type":"function_call_output","call_id":"last","output":"queued for verbatim delivery"}`),
		}
		conversationID := fmt.Sprintf("delivery-%d", i)
		_, err := sessions.AppendEntryID(ctx, conversationID, &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: entry.Timestamp, ReplayInput: replay})
		require.NoError(t, err)
		history, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: conversationID})
		require.NoError(t, err)
		// The last decision replaces the first, without duplicating an existing reply.
		require.Len(t, history.Messages, len(replay), "arguments: %s", arguments)
	}

	// Definition failures must fail both independent choices and enumeration.
	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "broken.md"), []byte("---\nmodel: [\n---\nHelp."), 0o600))

	_, err = invoke[ListAgentsResponse](ctx, connection, "ListAgents", &ListAgentsRequest{ConversationId: id})
	require.ErrorContains(t, err, "load web agents")
	_, err = invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
	require.ErrorContains(t, err, "load web agents")
	require.NoError(t, root.Remove(filepath.Join(cfg.RuntimeDirName(), "agents", "broken.md")))

	t.Run("selected conversation read unavailable", func(t *testing.T) {
		lockedDSN, err := url.Parse(dsn)
		require.NoError(t, err)

		options := lockedDSN.Query()
		options.Set("lock_timeout", "1ms")
		lockedDSN.RawQuery = options.Encode()
		lockedSessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: lockedDSN.String(), Workspace: storageConfig.Workspace}, slog.New(slog.DiscardHandler))

		require.NoError(t, err)
		defer func() { require.NoError(t, lockedSessions.Stop()) }()

		transaction, err := db.BeginTx(t.Context(), nil)

		require.NoError(t, err)
		defer func() { require.NoError(t, transaction.Rollback()) }()

		_, err = transaction.ExecContext(t.Context(), `LOCK TABLE managed_conversations IN ACCESS EXCLUSIVE MODE`)
		require.NoError(t, err)

		// Visibility remains readable; only the selected thread lookup is blocked.
		selectedServer := *server
		selectedServer.sessions = lockedSessions
		incoming := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "192.0.2.1"))
		selected, err := selectedServer.listAgents(incoming, id)
		require.ErrorContains(t, err, "read selected conversation")
		require.Nil(t, selected)
	})

	t.Run("durable attachments", func(t *testing.T) {
		const conversation = "attachment-visible"

		previousRunTurn := core.RunTurnFunc

		core.RunTurnFunc = func(_ context.Context, inbound *protocol.InboundMessage) error {
			turns = append(turns, inbound)
			return nil
		}
		defer func() { core.RunTurnFunc = previousRunTurn }()

		data := bytes.Repeat([]byte("exact binary\x00\xff"), 400000)
		upload, err := connection.NewStream(ctx, &grpc.StreamDesc{ClientStreams: true}, "/rpc.Web/UploadAttachment")
		require.NoError(t, err)
		require.NoError(t, upload.SendMsg(&Attachment{ConversationId: conversation, Name: "../../input.bin"}))

		for offset := 0; offset < len(data); offset += attachmentChunkBytes {
			require.NoError(t, upload.SendMsg(&Attachment{Data: data[offset:min(offset+attachmentChunkBytes, len(data))]}))
		}

		require.NoError(t, upload.CloseSend())

		file := &Attachment{}
		require.NoError(t, upload.RecvMsg(file))
		require.Equal(t, "input.bin", file.Name)
		require.Equal(t, int64(len(data)), file.Size)
		require.Empty(t, file.Data)

		imageData, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aL1kAAAAASUVORK5CYII=")
		require.NoError(t, err)
		imageUpload, err := connection.NewStream(ctx, &grpc.StreamDesc{ClientStreams: true}, "/rpc.Web/UploadAttachment")
		require.NoError(t, err)
		require.NoError(t, imageUpload.SendMsg(&Attachment{ConversationId: conversation, Name: "image.png", Data: imageData}))
		require.NoError(t, imageUpload.CloseSend())

		imageFile := &Attachment{}
		require.NoError(t, imageUpload.RecvMsg(imageFile))
		require.Equal(t, "image/png", imageFile.MimeType)
		require.NoError(t, sessions.UpsertThread(conversation, backend.ThreadState{Agent: "main"}))

		exact := "  keep\n\t this  text \"intact\" attachment:literal prose  \n"
		_, err = invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: conversation, Text: exact, Delivery: PromptDelivery_QUEUE, AttachmentIds: []string{file.Id, imageFile.Id}})
		require.NoError(t, err)
		reopened, err := backend.NewSessionServiceIn(t.Context(), storageConfig, slog.New(slog.DiscardHandler))

		require.NoError(t, err)
		defer func() { require.NoError(t, reopened.Stop()) }()

		queue, err := reopened.ThreadQueueForConversation(conversation)
		require.NoError(t, err)
		require.Len(t, queue, 1)
		require.Equal(t, exact, queue[0].Message)
		require.Equal(t, exact, queue[0].Content.Text)
		require.Equal(t, "alice", queue[0].Principal)
		require.Equal(t, protocol.SourceWeb, queue[0].Source)
		require.Contains(t, queue[0].Content.TextAttachments[0], file.Id)
		require.Contains(t, queue[0].Content.TextAttachments[1], imageFile.Id)
		require.Equal(t, []protocol.InboundAttachment{{Name: "image.png", MIMEType: "image/png", Data: imageData}}, queue[0].Content.Attachments)
		materialized, err := root.ReadFile(filepath.Join("artifacts", "uploads", file.Id, file.Name))
		require.NoError(t, err)
		require.Equal(t, data, materialized)

		listed, err := invoke[ListQueueResponse](ctx, connection, "ListQueue", &ListQueueRequest{Id: conversation})
		require.NoError(t, err)
		require.Equal(t, file.Id, listed.Items[0].Attachments[0].Id)
		_, err = invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: conversation, Text: exact, MessageId: "attachment-input", AttachmentIds: []string{file.Id, imageFile.Id}})
		require.NoError(t, err)
		require.Equal(t, "attachment-input", turns[len(turns)-1].Metadata["web_message_id"])
		require.Equal(t, "alice", turns[len(turns)-1].Metadata[protocol.InboundPrincipalMetadataKey])
		require.Contains(t, turns[len(turns)-1].Text, file.Id)
		require.Equal(t, exact, turns[len(turns)-1].Metadata[protocol.InboundRawTextMetadataKey])
		require.Equal(t, queue[0].Content.Attachments, turns[len(turns)-1].Attachments)
		t.Run("pending steer restores attachment cards", func(t *testing.T) {
			list := core.QueueItemsFunc
			defer func() { core.QueueItemsFunc = list }()

			core.QueueItemsFunc = func(string) ([]protocol.ThreadQueueItem, error) {
				return []protocol.ThreadQueueItem{{ID: "attachment-input", Kind: protocol.InboundKindSteer, Message: turns[len(turns)-1].Text}}, nil
			}
			pending, err := invoke[ListQueueResponse](ctx, connection, "ListQueue", &ListQueueRequest{Id: conversation})
			require.NoError(t, err)
			require.Len(t, pending.Items, 1)
			require.Equal(t, "attachment-input", pending.Items[0].Id)
			require.Equal(t, PromptDelivery_STEER, pending.Items[0].Delivery)
			require.Equal(t, exact, pending.Items[0].Text)
			require.Len(t, pending.Items[0].Attachments, 2)
			require.Equal(t, file.Id, pending.Items[0].Attachments[0].Id)
			require.Equal(t, imageFile.Id, pending.Items[0].Attachments[1].Id)
		})

		_, err = sessions.AppendEntryID(ctx, conversation, &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ReplayInput: []json.RawMessage{
			json.RawMessage(fmt.Sprintf(`{"type":"message","role":"user","content":%q}`, turns[len(turns)-1].Text)),
		}})
		require.NoError(t, err)
		inputHistory, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: conversation})
		require.NoError(t, err)
		require.Len(t, inputHistory.Messages[0].Attachments, 2)
		require.Equal(t, exact, inputHistory.Messages[0].Text)
		require.Equal(t, file.Id, inputHistory.Messages[0].Attachments[0].Id)
		require.Equal(t, imageFile.Id, inputHistory.Messages[0].Attachments[1].Id)

		t.Run("consumed attachment input streams once with its identity", func(t *testing.T) {
			subscribe := core.SubscribeFunc
			defer func() { core.SubscribeFunc = subscribe }()

			consumed := protocol.NewOutboundMessage(conversation, "")
			consumed.ConsumedID, consumed.ConsumedText = "attachment-input", turns[len(turns)-1].Text
			ack := make(chan error, 1)
			core.SubscribeFunc = func(context.Context) iter.Seq[protocol.Event] {
				return func(yield func(protocol.Event) bool) {
					yield(protocol.Event{Message: consumed, Acknowledgement: ack})
				}
			}
			stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
			require.NoError(t, err)
			require.NoError(t, stream.SendMsg(&JoinRequest{Id: conversation}))
			require.NoError(t, stream.CloseSend())

			event := &TranscriptEvent{}
			require.NoError(t, stream.RecvMsg(event))
			require.Equal(t, "attachment-input", event.MessageId)
			require.Equal(t, "user", event.Role)
			require.Equal(t, exact, event.Text)
			require.Len(t, event.Attachments, 2)
			require.Equal(t, file.Id, event.Attachments[0].Id)
			require.Equal(t, imageFile.Id, event.Attachments[1].Id)
			require.False(t, event.Complete)
			require.ErrorIs(t, stream.RecvMsg(&TranscriptEvent{}), io.EOF)
			require.NoError(t, <-ack)

			t.Run("attachment lookup failure aborts queue and stream", func(t *testing.T) {
				_, err := db.ExecContext(ctx, `ALTER TABLE attachments RENAME TO unavailable_attachments`)

				require.NoError(t, err)
				defer func() {
					_, err := db.ExecContext(ctx, `ALTER TABLE unavailable_attachments RENAME TO attachments`)
					require.NoError(t, err)
				}()

				_, err = invoke[ListQueueResponse](ctx, connection, "ListQueue", &ListQueueRequest{Id: conversation})
				require.ErrorContains(t, err, "load input attachment metadata")
				stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
				require.NoError(t, err)
				require.NoError(t, stream.SendMsg(&JoinRequest{Id: conversation}))
				require.NoError(t, stream.CloseSend())

				event := &TranscriptEvent{}
				require.ErrorContains(t, stream.RecvMsg(event), "load input attachment metadata")
				require.Empty(t, event.MessageId, "failed attachment lookup must not acknowledge consumption to the browser")
				require.ErrorContains(t, <-ack, "load input attachment metadata")

				remaining, err := reopened.ThreadQueueForConversation(conversation)
				require.NoError(t, err)
				require.Equal(t, queue, remaining, "failed rendering must preserve waiting work")
			})
		})

		for _, test := range []struct{ text, want string }{
			{"attachment:" + file.Id + " is literal prose  \n", "attachment:" + file.Id + " is literal prose  \n"},
			{"literal\n\n" + queue[0].Content.TextAttachments[0] + "\nkeep this", "literal\n\n" + queue[0].Content.TextAttachments[0] + "\nkeep this"},
			{queue[0].Content.TextAttachments[1], ""},
		} {
			_, err = sessions.AppendEntryID(ctx, conversation, &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ReplayInput: []json.RawMessage{
				json.RawMessage(fmt.Sprintf(`{"type":"message","role":"user","content":%q}`, test.text)),
			}})
			require.NoError(t, err)
			inputHistory, err = invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: conversation})
			require.NoError(t, err)
			require.Equal(t, test.want, inputHistory.Messages[len(inputHistory.Messages)-1].Text)
		}

		t.Run("filename attachment tokens do not hide the generated suffix", func(t *testing.T) {
			upload := protocol.OutboundAttachment{ID: "filename-reference", Name: "photo attachment:" + file.Id + " extra.png", MIMEType: "image/png", Data: imageData}
			require.NoError(t, sessions.SaveAttachment(ctx, conversation, &upload, true))
			_, err := invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: conversation, Text: exact, AttachmentIds: []string{imageFile.Id, upload.ID}})
			require.NoError(t, err)

			raw := turns[len(turns)-1].Text
			require.Contains(t, raw, upload.Name)
			_, err = sessions.AppendEntryID(ctx, conversation, &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ReplayInput: []json.RawMessage{
				json.RawMessage(fmt.Sprintf(`{"type":"message","role":"user","content":%q}`, raw)),
			}})
			require.NoError(t, err)
			inputHistory, err = invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: conversation})
			require.NoError(t, err)
			require.Equal(t, exact, inputHistory.Messages[len(inputHistory.Messages)-1].Text)

			entries, err := sessions.ObserveEntries(ctx, conversation)
			require.NoError(t, err)
			require.JSONEq(t, fmt.Sprintf(`{"type":"message","role":"user","content":%q}`, raw), string(entries[len(entries)-1].Entry.ReplayInput[0]))
		})

		workspaceParent, err := os.OpenRoot(filepath.Dir(cfg.Workspace))

		require.NoError(t, err)
		defer func() { require.NoError(t, workspaceParent.Close()) }()

		for _, test := range []struct {
			name, path, message string
		}{
			{"missing workspace", "", "open upload workspace"},
			{"uploads collision", "artifacts/uploads", "create upload directory"},
			{"filename directory", filepath.Join("artifacts", "uploads", imageFile.Id, imageFile.Name), "materialize upload"},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixtureRoot, path := root, test.path
				if path == "" {
					fixtureRoot, path = workspaceParent, filepath.Base(cfg.Workspace)
				}

				require.NoError(t, fixtureRoot.Rename(path, path+".saved"))
				t.Cleanup(func() { require.NoError(t, fixtureRoot.Rename(path+".saved", path)) })

				switch test.name {
				case "uploads collision":
					require.NoError(t, root.WriteFile(path, []byte("collision"), 0o600))
					t.Cleanup(func() { require.NoError(t, root.Remove(path)) })
				case "filename directory":
					require.NoError(t, root.Mkdir(path, 0o700))
					t.Cleanup(func() { require.NoError(t, root.Remove(path)) })
				}

				beforeTurns := len(turns)

				for _, delivery := range []PromptDelivery{PromptDelivery_QUEUE, PromptDelivery_STEER} {
					_, err := invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: conversation, Text: exact, Delivery: delivery, AttachmentIds: []string{file.Id, imageFile.Id}})
					require.ErrorContains(t, err, test.message)
					require.Len(t, turns, beforeTurns)

					remaining, err := reopened.ThreadQueueForConversation(conversation)
					require.NoError(t, err)
					require.Equal(t, queue, remaining)
				}
			})
			// Failed ingestion must preserve the original and its existing references.
			history, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: conversation})
			require.NoError(t, err)
			require.True(t, proto.Equal(inputHistory, history))

			download, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/DownloadAttachment")
			require.NoError(t, err)
			require.NoError(t, download.SendMsg(&Attachment{ConversationId: conversation, Id: imageFile.Id}))
			require.NoError(t, download.CloseSend())

			original := &Attachment{}
			require.NoError(t, download.RecvMsg(original))
			require.Equal(t, imageData, original.Data)
			require.ErrorIs(t, download.RecvMsg(&Attachment{}), io.EOF)
		}

		for _, test := range []struct {
			name   string
			frames []*Attachment
			code   codes.Code
		}{
			{"empty stream", nil, codes.Unknown},
			{"missing filename", []*Attachment{{ConversationId: conversation}}, codes.InvalidArgument},
			{"oversized first chunk", []*Attachment{{ConversationId: conversation, Name: "bad.bin", Data: make([]byte, (256<<10)+1)}}, codes.InvalidArgument},
			{"continuation metadata", []*Attachment{{ConversationId: conversation, Name: "bad.bin"}, {Name: "changed.bin"}}, codes.InvalidArgument},
			{"oversized continuation", []*Attachment{{ConversationId: conversation, Name: "bad.bin"}, {Data: make([]byte, (256<<10)+1)}}, codes.InvalidArgument},
		} {
			t.Run(test.name, func(t *testing.T) {
				upload, err := connection.NewStream(ctx, &grpc.StreamDesc{ClientStreams: true}, "/rpc.Web/UploadAttachment")
				require.NoError(t, err)

				for _, frame := range test.frames {
					require.NoError(t, upload.SendMsg(frame))
				}

				require.NoError(t, upload.CloseSend())
				require.Equal(t, test.code, status.Code(upload.RecvMsg(&Attachment{})))
			})
		}

		require.NoError(t, sessions.DeleteThreadQueueItem(queue[0].ID))

		_, err = invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: "another", Text: exact, AttachmentIds: []string{file.Id}})
		require.Equal(t, codes.NotFound, status.Code(err))

		// A real gRPC download stays below default message limits and preserves every byte.
		download, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/DownloadAttachment")
		require.NoError(t, err)
		require.NoError(t, download.SendMsg(&Attachment{ConversationId: conversation, Id: file.Id}))
		require.NoError(t, download.CloseSend())

		var received []byte

		for {
			frame := &Attachment{}

			err := download.RecvMsg(frame)
			if errors.Is(err, io.EOF) {
				break
			}

			require.NoError(t, err)
			require.LessOrEqual(t, len(frame.Data), attachmentChunkBytes)
			received = append(received, frame.Data...)
		}

		require.Equal(t, data, received)

		// Historical capture is rooted at cfg.Workspace and never refreshed from mutable paths.
		require.NoError(t, root.MkdirAll("artifacts/memes", 0o700))
		require.NoError(t, root.WriteFile("artifacts/memes/old.png", []byte("old image bytes"), 0o600))
		outside, err := os.OpenRoot(t.TempDir())

		require.NoError(t, err)
		defer func() { require.NoError(t, outside.Close()) }()

		require.NoError(t, outside.WriteFile("secret", []byte("outside workspace"), 0o600))
		require.NoError(t, root.Symlink(filepath.Join(outside.Name(), "secret"), "escape"))

		legacy := []json.RawMessage{
			json.RawMessage(`{"type":"function_call","call_id":"legacy","name":"rocketclaw_attach_files_to_response","arguments":"{\"attachments\":[{\"path\":\"artifacts/memes/old.png\"},{\"path\":\"escape\"},{\"path\":\"missing\"}]}"}`),
			json.RawMessage(`{"type":"function_call_output","call_id":"legacy","output":"queued attachments for final response"}`),
		}
		_, err = sessions.AppendEntryID(ctx, conversation, &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ReplayInput: legacy})
		require.NoError(t, err)
		history, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: conversation})
		require.NoError(t, err)
		require.Len(t, history.Messages[len(history.Messages)-1].Attachments, 1)

		recovered := history.Messages[len(history.Messages)-1].Attachments[0]
		require.True(t, recovered.OriginalUnverified)
		require.Empty(t, recovered.Data)
		require.NoError(t, root.WriteFile("artifacts/memes/old.png", []byte("overwritten"), 0o600))

		history, err = invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: conversation})
		require.NoError(t, err)
		require.True(t, proto.Equal(recovered, history.Messages[len(history.Messages)-1].Attachments[0]))
		stored, err := reopened.LoadAttachment(ctx, conversation, recovered.Id, false)
		require.NoError(t, err)
		require.Equal(t, []byte("old image bytes"), stored.Data)
		// A delivered copy authorizes only the original producer's referenced object.
		producerFile := protocol.OutboundAttachment{ID: "private-generated", Name: "generated.png", MIMEType: "image/png", Data: []byte("private original")}
		require.NoError(t, sessions.SaveAttachment(ctx, "private-X", &producerFile, false))
		sourceID, err := sessions.AppendEntryID(ctx, "private-X", &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ReplayInput: []json.RawMessage{
			json.RawMessage(`{"type":"function_call","call_id":"private-file","name":"rocketclaw_attach_files_to_response","arguments":"{}"}`),
			json.RawMessage(`{"type":"function_call_output","call_id":"private-file","output":"queued attachments for final response: private-generated"}`),
		}})
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) SELECT $1, (entry_json::jsonb || jsonb_build_object('sync_source_entry_id', id))::text, entry_timestamp FROM session_entries WHERE id=$2`, conversation, sourceID)
		require.NoError(t, err)
		filtered, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: conversation, SourceConversationId: "private-X"})
		require.NoError(t, err)
		require.Len(t, filtered.Messages, 2)
		require.Equal(t, producerFile.ID, filtered.Messages[1].Attachments[0].Id)
		require.Equal(t, "private-X", filtered.Messages[1].Attachments[0].ConversationId)

		_, err = sessions.DeleteSession(ctx, "private-X")
		require.NoError(t, err)
		filtered, err = invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: conversation, SourceConversationId: "private-X"})
		require.NoError(t, err)
		require.Empty(t, filtered.Messages)
		// Unreferenced immutable objects cannot be downloaded by knowing their ID.
		require.NoError(t, sessions.SaveAttachment(ctx, conversation, &protocol.OutboundAttachment{ID: "orphan", Name: "orphan", Data: []byte("private")}, false))

		for _, test := range []struct {
			name, conversation, id, principal string
			code                              codes.Code
		}{
			{"unreferenced", conversation, "orphan", "192.0.2.1", codes.NotFound},
			{"cross conversation", "other", file.Id, "192.0.2.1", codes.NotFound},
			{"missing principal", conversation, file.Id, "", codes.Unauthenticated},
			{"unmapped principal", conversation, file.Id, "192.0.2.99", codes.Unauthenticated},
			{"cron", "cron:private", file.Id, "192.0.2.1", codes.PermissionDenied},
			{"one off cron", "one-off-cron:private", file.Id, "192.0.2.1", codes.PermissionDenied},
			{"external MCP private", "private-X", producerFile.ID, "192.0.2.1", codes.PermissionDenied},
			{"deleted source", conversation, producerFile.ID, "192.0.2.1", codes.NotFound},
		} {
			t.Run(test.name, func(t *testing.T) {
				callCtx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", test.principal))
				stream, err := connection.NewStream(callCtx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/DownloadAttachment")
				require.NoError(t, err)
				require.NoError(t, stream.SendMsg(&Attachment{ConversationId: test.conversation, Id: test.id}))
				require.NoError(t, stream.CloseSend())
				require.Equal(t, test.code, status.Code(stream.RecvMsg(&Attachment{})))

				if test.code == codes.PermissionDenied || test.code == codes.Unauthenticated {
					upload, err := connection.NewStream(callCtx, &grpc.StreamDesc{ClientStreams: true}, "/rpc.Web/UploadAttachment")
					require.NoError(t, err)
					// Authentication can reject the stream before SendMsg; RecvMsg carries the status.
					if err := upload.SendMsg(&Attachment{ConversationId: test.conversation, Name: "denied.txt", Data: []byte("denied")}); err != nil {
						require.ErrorIs(t, err, io.EOF)
					}

					require.NoError(t, upload.CloseSend())
					require.Equal(t, test.code, status.Code(upload.RecvMsg(&Attachment{})))
				}
			})
		}

		_, err = sessions.DeleteSession(ctx, conversation)
		require.NoError(t, err)
		stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/DownloadAttachment")
		require.NoError(t, err)
		require.NoError(t, stream.SendMsg(&Attachment{ConversationId: conversation, Id: recovered.Id}))
		require.NoError(t, stream.CloseSend())
		require.Equal(t, codes.NotFound, status.Code(stream.RecvMsg(&Attachment{})))
	})

	// A database outage cannot turn selected choices or enumeration into empty success.
	require.NoError(t, sessions.Stop())

	_, err = invoke[ListAgentsResponse](ctx, connection, "ListAgents", &ListAgentsRequest{ConversationId: id})
	require.ErrorContains(t, err, "resolve web conversation visibility")
	_, err = invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
	require.ErrorContains(t, err, "query sidebar sessions")

	for _, test := range []struct {
		method            string
		request, response proto.Message
	}{
		{"History", &HistoryRequest{Id: id}, &HistoryResponse{}},
		{"SettleSession", &SettleSessionRequest{Id: id, Settled: true}, &SettleSessionResponse{}},
		{"UpdateSession", &UpdateSessionRequest{Id: id, Name: new("Retain this name")}, &UpdateSessionResponse{}},
		{"ListQueue", &ListQueueRequest{Id: id}, &ListQueueResponse{}},
		{"ListSessionEntries", &SessionEntriesRequest{Id: id}, &ListSessionEntriesResponse{}},
		{"LoadSessionEntries", &SessionEntriesRequest{Id: id}, &LoadSessionEntriesResponse{}},
		{"DeleteSessionEntries", &SessionEntriesRequest{Id: id}, &DeleteSessionEntriesResponse{}},
	} {
		err := connection.Invoke(ctx, "/rpc.Web/"+test.method, test.request, test.response)
		require.Error(t, err, test.method)
		require.Contains(t, err.Error(), "database is closed", test.method)
	}

	catalog, err = invoke[ListAgentsResponse](ctx, connection, "ListAgents", &ListAgentsRequest{})
	require.NoError(t, err)
	require.Len(t, catalog.Agents, 3, "the unselected catalog is independent of the database")
}

func TestCronHistoryUsesStoredSourceLabelsAndDestination(t *testing.T) {
	colon := "cron:cron/report:daily.md:20260905T010000.000000001Z:first"
	underscore := "one-off-cron:cron/report_daily.md:20260905T020000.000000002Z:second"
	entries := []backend.ObservedSessionEntry{{SourceConversationID: colon}, {SourceConversationID: colon}, {SourceConversationID: underscore}, {}, {SourceConversationID: "external_mcp:unrelated"}, {SourceConversationID: "cron:invalid"}, {SourceConversationID: "cron:cron/invalid.md:not-time:random"}}
	runs := cronHistory(entries, "opaque-human-Y")
	require.Len(t, runs, 2)

	for i, want := range []*CronJob{
		{Stem: "report:daily", Status: "ran", LastRun: "2026-09-05T01:00:00.000000001Z", NextRun: "opaque-human-Y", Origin: colon},
		{Stem: "report_daily", Status: "ran", LastRun: "2026-09-05T02:00:00.000000002Z", NextRun: "opaque-human-Y", Origin: underscore},
	} {
		require.True(t, proto.Equal(want, runs[i]))
	}

	require.Empty(t, cronHistory(nil, "opaque-human-Y"))
	require.Len(t, cronHistory([]backend.ObservedSessionEntry{{SourceConversationID: colon}, {SourceConversationID: colon + "-another-run"}}, "opaque-human-Y"), 2)
}

func invoke[Response any](ctx context.Context, connection *grpc.ClientConn, method string, request any) (*Response, error) {
	response := new(Response)

	if method == "ListSessions" {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/"+method)
		if err != nil {
			return nil, fmt.Errorf("open %s stream: %w", method, err)
		}

		if err := stream.SendMsg(request); err != nil {
			return nil, fmt.Errorf("send %s request: %w", method, err)
		}

		if err := stream.CloseSend(); err != nil {
			return nil, fmt.Errorf("close %s request: %w", method, err)
		}

		for {
			chunk := new(Response)

			err := stream.RecvMsg(chunk)
			if errors.Is(err, io.EOF) {
				if !any(response).(*ListSessionsResponse).UpstreamSuccess {
					return nil, errors.New("session enumeration ended without upstream success")
				}

				return response, nil
			}

			if err != nil {
				return nil, fmt.Errorf("receive %s response: %w", method, err)
			}

			proto.Merge(any(response).(proto.Message), any(chunk).(proto.Message))

			if terminal := any(chunk).(*ListSessionsResponse); terminal.UpstreamSuccess {
				any(response).(*ListSessionsResponse).SummariesComplete = terminal.SummariesComplete
			}
		}
	}

	if err := connection.Invoke(ctx, "/rpc.Web/"+method, request, response); err != nil {
		return nil, fmt.Errorf("invoke %s: %w", method, err)
	}

	return response, nil
}

func testSocketPath(t *testing.T) string {
	t.Helper()
	// TMPDIR is a short repository .tmp path, keeping sockets below macOS's limit.
	dir, err := os.MkdirTemp(os.Getenv("TMPDIR"), "rpc-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })

	socketPath, err := filepath.Abs(filepath.Join(dir, "web.sock"))
	require.NoError(t, err)

	return socketPath
}

func TestPrivateSocket(t *testing.T) {
	socketPath := testSocketPath(t)
	_, err := Listen("web.sock")
	require.ErrorContains(t, err, "absolute")
	_, err = Listen(filepath.Join(socketPath, "missing", "web.sock"))
	require.ErrorContains(t, err, "stat web RPC socket directory")
	require.NoError(t, os.Chmod(filepath.Dir(socketPath), 0o755))
	_, err = Listen(socketPath)
	require.ErrorContains(t, err, "private")
	require.NoError(t, os.Chmod(filepath.Dir(socketPath), 0o700))
	listener, err := Listen(socketPath)
	require.NoError(t, err)
	_, err = Listen(socketPath)
	require.ErrorContains(t, err, "listen on web RPC socket")
	require.NoError(t, listener.Close())

	_, err = os.Stat(socketPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}
