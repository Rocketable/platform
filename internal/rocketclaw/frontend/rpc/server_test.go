package rpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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

func TestSessionEntries(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(dsn, slog.New(slog.DiscardHandler))
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
		require.Equal(t, "C1", channel)
		return channelChoices, nil
	}}
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

	entry := rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Date(2026, 9, 5, 12, 0, 0, 123, time.UTC)}
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
	require.Equal(t, []*QueueItem{{Id: "q1", Text: "queued later"}}, queued.Items)

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
	require.Equal(t, []*QueueItem{{Id: "q1", Text: "queued later"}, {Id: "q3", Text: "third"}, {Id: "q2", Text: "second"}}, queued.Items)

	_, err = invoke[QueueItemResponse](ctx, connection, "RemoveQueueItem", &QueueItemRequest{Id: id, ItemId: "q2"})
	require.NoError(t, err)

	queued, err = invoke[ListQueueResponse](ctx, connection, "ListQueue", &ListQueueRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, []*QueueItem{{Id: "q1", Text: "queued later"}, {Id: "q3", Text: "third"}}, queued.Items)

	_, err = invoke[QueueItemResponse](ctx, connection, "RemoveQueueItem", &QueueItemRequest{Id: id, ItemId: "missing"})
	require.Equal(t, codes.NotFound, status.Code(err))

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
	emptyHistory, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: id})
	require.NoError(t, err)
	require.Empty(t, emptyHistory.Messages)

	for _, hidden := range []string{"private-X", "cron:cron/daily.md:20000102T030405.000000006Z:a"} {
		_, err = invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: hidden})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
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
		json.RawMessage(`{"type":"message","id":"msg_one","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer one","annotations":[]}]}`),
		json.RawMessage(`{"type":"message","role":"user","content":"[Web media=Text principal=\"alice\" additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nhuman two"}`),
		json.RawMessage(`{"type":"message","role":"assistant","content":"answer two"}`),
	}}
	_, err = sessions.AppendEntryID(ctx, "empty-web", &historyEntry)
	require.NoError(t, err)
	history, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: "empty-web"})
	require.NoError(t, err)
	require.Len(t, history.Messages, 4)

	for i, text := range []string{"human one", "answer one", "human two", "answer two"} {
		require.Equal(t, text, history.Messages[i].Text)
		require.Equal(t, []string{"user", "assistant"}[i%2], history.Messages[i].Role)
	}

	listedSessions, err = invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
	require.NoError(t, err)
	require.Equal(t, "empty-web", listedSessions.Sessions[0].Id)
	require.Equal(t, "human two", listedSessions.Sessions[0].Preview)

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

	for _, method := range []string{"ListConfig", "ListSkills"} {
		_, err = invoke[ListConfigResponse](t.Context(), connection, method, &ListConfigRequest{})
		require.Equal(t, codes.Unauthenticated, status.Code(err))
	}

	cfg.Overlays = []string{"local-overlay"}
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

	wantConfig := &ConfigView{Workspace: cfg.Workspace, Overlays: []string{"local-overlay"}, Models: []*ConfigModel{{Name: "alpha", Model: "gpt-5.4"}, {Name: "zeta", Model: "gpt-5.5"}}, SlackChannels: []*ConfigChannel{{Channel: "#ops", Agents: []string{"main"}}}, McpServers: []string{"alpha", "zeta"}, LoggingLevel: "info", AutoApproverModel: "gpt-5.5", InstrumentationEnabled: true, McpExternal: true}
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

	_, err = sessions.AppendEntryID(ctx, id, &entry)
	require.NoError(t, err)
	proxy := exec.CommandContext(t.Context(), "bun", "test", "src/entry-transport.test.ts")
	proxy.Dir = "../../../../web"

	proxy.Env = append(os.Environ(), "ROCKETCLAW_WEB_GRPC=unix:"+socketPath, "ROCKETCLAW_ENTRY_TEST_ID="+id, "ROCKETCLAW_HISTORY_TEST_ID=empty-web", "ROCKETCLAW_VIEW_TEST_WORKSPACE="+cfg.Workspace)
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
		require.Equal(t, tc.want, listed.Sessions[0].Preview)

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

	listedSessions, err = invoke[ListSessionsResponse](ctx, connection, "ListSessions", &ListSessionsRequest{})
	require.NoError(t, err)

	for _, session := range listedSessions.Sessions {
		if session.Id == id {
			require.Equal(t, "planner", session.Agent)
			require.Equal(t, []string{"main"}, session.AllowedAgents)
		}

		if session.Id == created.Id {
			require.Equal(t, "selected", session.Agent)
			require.Equal(t, []string{"main", "planner", "selected"}, session.AllowedAgents)
		}

		require.NotEqual(t, "private-X", session.Id)
	}

	historyAfter, err := sessions.ObserveEntries(ctx, "empty-web")
	require.NoError(t, err)
	require.Equal(t, historyBefore, historyAfter)

	private, found, err := sessions.Thread("private-X")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "producer", private.Agent)

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
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	sources := []string{"cron:cron/report:daily.md:20260905T010000.000000001Z:first", "one-off-cron:cron/report_daily.md:20260905T020000.000000002Z:second"}
	for _, source := range sources {
		sourceEntry, err := sessions.AppendEntryID(ctx, source, &entry)
		require.NoError(t, err)

		for range 2 {
			destinationEntry, err := sessions.AppendEntryID(ctx, id, &entry)
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

	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "cron", "bad.md"), []byte("not a cron definition"), 0o600))

	_, err = invoke[ListCronJobsResponse](ctx, connection, "ListCronJobs", &ListCronJobsRequest{})
	require.ErrorContains(t, err, "bad.md")
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
	if err := connection.Invoke(ctx, "/rpc.Web/"+method, request, response); err != nil {
		return nil, fmt.Errorf("invoke %s: %w", method, err)
	}

	return response, nil
}

func testSocketPath(t *testing.T) string {
	t.Helper()
	// Keep Unix socket paths below macOS's limit and all artifacts in the repo.
	require.NoError(t, os.MkdirAll("../../../../.tmp", 0o700))

	dir, err := os.MkdirTemp("../../../../.tmp", "rpc-")
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
