package backend

import (
	"context"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

func TestSessionOperationsRejectCanceledContext(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	recordTestConversations(t, store, "main", map[string]ConversationKind{"chat": KindWebChat})
	_, err := store.AppendEntryID(t.Context(), "chat", testSessionEntry("saved", "answer"))
	require.NoError(t, err)
	before, err := store.ObserveEntries(t.Context(), "chat")
	require.NoError(t, err)

	registry, _ := newTestBackgroundRegistry(store)
	runtime := &Runtime{Sessions: store, background: registry}

	for _, operation := range []string{
		"append", "rename", "attachment", "voice", "tabs", "save tabs", "channel",
		"history", "active turn", "Slack thread", "conversations", "background jobs",
		"completion notes", "page", "continue run", "external MCP metadata",
	} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			var err error

			switch operation {
			case "append":
				_, err = store.AppendEntryID(ctx, "chat", testSessionEntry("not saved", "not saved"))
			case "rename":
				_, err = store.UpdateConversationDetails(ctx, "chat", new(true), new("not saved"))
			case "attachment":
				err = store.SaveAttachment(ctx, "chat", "", &protocol.OutboundAttachment{ID: "not-saved", Name: "note.txt", Data: []byte("not saved")}, false)
			case "voice":
				_, err = store.VoiceSettings(ctx, "alice")
			case "tabs":
				_, _, err = store.TabState(ctx, "alice")
			case "save tabs":
				_, err = store.SaveTabState(ctx, "alice", &TabState{}, 0)
			case "channel":
				_, _, err = store.ChannelFact(ctx, "workspace", "channel")
			case "history":
				_, err = store.ObserveHistory(ctx, "chat", "", 0, 0, 0, &HistoryInventory{})
			case "active turn":
				_, err = store.HasActiveTurn(ctx, "chat")
			case "Slack thread":
				_, _, err = store.SlackThread(ctx, "chat")
			case "conversations":
				_, err = runtime.ListConversations(ctx)
			case "background jobs":
				_, _, err = runtime.BackgroundJobs(ctx, "chat")
			case "completion notes":
				_, err = runtime.CompletionNotes(ctx, "chat", []string{"job"})
			case "page":
				_, _, err = store.Page(ctx, "main", PageData, "note")
			case "continue run":
				err = runtime.ContinueRun(ctx, "run", "chat")
			case "external MCP metadata":
				_, _, err = store.externalMCPMetadataEntry(ctx, "chat")
			}

			require.ErrorIs(t, err, context.Canceled)
		})
	}

	after, err := store.ObserveEntries(t.Context(), "chat")
	require.NoError(t, err)
	require.Equal(t, before, after)

	var (
		pinned bool
		name   string
	)
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT pinned, name FROM conversations WHERE conversation_id = 'chat'`).Scan(&pinned, &name))
	require.False(t, pinned)
	require.Empty(t, name)

	var attachments int
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM attachments`).Scan(&attachments))
	require.Zero(t, attachments)
	tabs, revision, err := store.TabState(t.Context(), "alice")
	require.NoError(t, err)
	require.Equal(t, TabState{}, tabs)
	require.Zero(t, revision)
}
