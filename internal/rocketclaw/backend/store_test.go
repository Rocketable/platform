package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cirello.io/pglock"
	migrate "github.com/rubenv/sql-migrate"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestSessionTags(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)
	ctx := t.Context()

	const id = "external_mcp:tags"

	recordPrivateHistories(t, store, map[string]ConversationKind{id: KindExternalMCPRun})

	groups := [][]string{{"triage", "investigating", "resolved"}, {"customer", "internal"}}
	for _, step := range []struct {
		tag   string
		group int
		want  []string
	}{
		{"triage", 0, []string{"triage"}},
		{"customer", 1, []string{"customer", "triage"}},
		{"investigating", 0, []string{"customer", "investigating"}},
		{"investigating", 0, []string{"customer"}},
	} {
		tags, err := store.toggleSessionTag(ctx, id, step.tag, groups[step.group])
		require.NoError(t, err)
		require.Equal(t, step.want, tags)
		tags, err = sessionTags(ctx, store.db, id)
		require.NoError(t, err)
		require.Equal(t, step.want, tags)
	}

	ctxCanceled, cancel := context.WithCancel(ctx)
	cancel()

	_, err := store.toggleSessionTag(ctxCanceled, id, "triage", groups[0])
	require.ErrorIs(t, err, context.Canceled)
	_, err = store.AppendEntryID(ctx, id, testSessionEntry("history", "user"))
	require.NoError(t, err)
	_, err = store.DeleteSession(ctx, id)
	require.NoError(t, err)
	require.NoError(t, store.Stop())
	store = newTestSessionServiceAt(t, workspace)
	tags, err := sessionTags(ctx, store.db, id)
	require.NoError(t, err)
	require.Equal(t, []string{"customer"}, tags)
	// Regrouping removes every current member, but keeps unrelated names.
	_, err = store.toggleSessionTag(ctx, id, "triage", groups[0])
	require.NoError(t, err)
	tags, err = store.toggleSessionTag(ctx, id, "resolved", []string{"customer", "triage", "resolved"})
	require.NoError(t, err)
	require.Equal(t, []string{"resolved"}, tags)

	stats, err := store.PruneStateBefore(ctx, time.Now().Add(-time.Hour), 0)
	require.NoError(t, err)
	require.Zero(t, stats.SessionRows)

	tags, err = sessionTags(ctx, store.db, id)
	require.NoError(t, err)
	require.Equal(t, []string{}, tags)
	require.NoError(t, store.Stop())
	_, err = sessionTags(ctx, store.db, id)
	require.Error(t, err)
	_, err = store.toggleSessionTag(ctx, id, "triage", groups[0])
	require.Error(t, err)
}

func TestSessionTagsCommitRollback(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	group := []string{"triage", "resolved"}

	for _, id := range []string{"owning", "other"} {
		require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "main"}))
		_, err := store.toggleSessionTag(ctx, id, "customer", []string{"customer"})
		require.NoError(t, err)
	}

	_, err := store.toggleSessionTag(ctx, "owning", "triage", group)
	require.NoError(t, err)
	_, err = store.toggleSessionTag(ctx, "other", "resolved", group)
	require.NoError(t, err)

	// A deferred constraint fails at commit, after the replacement write succeeds.
	_, err = store.db.ExecContext(ctx, `ALTER TABLE conversations ADD CONSTRAINT reject_duplicate_tags UNIQUE (tags) DEFERRABLE INITIALLY DEFERRED`)
	require.NoError(t, err)
	_, err = store.toggleSessionTag(ctx, "owning", "resolved", group)
	require.ErrorContains(t, err, "commit session tag toggle")
	tags, err := sessionTags(ctx, store.db, "owning")
	require.NoError(t, err)
	require.Equal(t, []string{"customer", "triage"}, tags)
	tags, err = sessionTags(ctx, store.db, "other")
	require.NoError(t, err)
	require.Equal(t, []string{"customer", "resolved"}, tags)

	_, err = store.toggleSessionTag(ctx, "other", "resolved", group)
	require.NoError(t, err)
	tags, err = store.toggleSessionTag(ctx, "owning", "resolved", group)
	require.NoError(t, err)
	require.Equal(t, []string{"customer", "resolved"}, tags)
}

func TestSessionTagsConcurrent(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()

	stores := []*SessionService{newTestSessionServiceAt(t, workspace), newTestSessionServiceAt(t, workspace)}
	for _, scenario := range []struct {
		name   string
		tags   []string
		groups [][]string
		want   []string
	}{
		{"independent", []string{"triage", "customer"}, [][]string{{"triage"}, {"customer"}}, []string{"customer", "triage"}},
		{"toggle", []string{"triage", "triage"}, [][]string{{"triage"}, {"triage"}}, []string{}},
		{"exclusive", []string{"triage", "resolved"}, [][]string{{"triage", "resolved"}, {"triage", "resolved"}}, nil},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			recordTestConversations(t, stores[0], "main", map[string]ConversationKind{scenario.name: KindWebChat})

			var work errgroup.Group
			for i, store := range stores {
				work.Go(func() error {
					_, err := store.toggleSessionTag(t.Context(), scenario.name, scenario.tags[i], scenario.groups[i])
					return err
				})
			}

			require.NoError(t, work.Wait())
			tags, err := sessionTags(t.Context(), stores[0].db, scenario.name)
			require.NoError(t, err)

			if scenario.name == "exclusive" {
				require.Len(t, tags, 1)
				require.Contains(t, scenario.tags, tags[0])
			} else {
				require.Equal(t, scenario.want, tags)
			}
		})
	}
}

func TestSessionTagsPrivateLifecycle(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()
	recordPrivateHistories(t, store, map[string]ConversationKind{
		"external_mcp:pre-epoch": KindExternalMCPRun, "external_mcp:orphan": KindExternalMCPRun, "cron:recent": KindCronRun, "external_mcp:queued": KindExternalMCPRun,
	})
	// Existing history, even before the epoch, takes precedence over the tag-only fallback.
	_, err := store.AppendEntryID(ctx, "external_mcp:pre-epoch", testSessionEntryAt(time.Unix(-20, 0).UTC(), "old"))
	require.NoError(t, err)
	_, err = store.toggleSessionTag(ctx, "external_mcp:pre-epoch", "customer", []string{"customer"})
	require.NoError(t, err)
	_, err = store.PruneStateBefore(ctx, time.Unix(-10, 0).UTC(), 0)
	require.NoError(t, err)
	tags, err := sessionTags(ctx, store.db, "external_mcp:pre-epoch")
	require.NoError(t, err)
	require.Empty(t, tags)

	cutoff := time.Unix(1_700_000_000, 0).UTC()

	ids := []string{"external_mcp:orphan", "cron:recent", "external_mcp:queued", "external_mcp:bound", "visible"}
	require.NoError(t, store.RegisterExternalMCPConversation("tags-bound", "main", &ExternalMCPSessionState{Agent: "main", PrivateConversationID: ids[3], ManagedConversationID: ids[4]}))

	for _, id := range ids {
		_, err := store.toggleSessionTag(ctx, id, "customer", []string{"customer"})
		require.NoError(t, err)
	}

	_, err = store.AppendEntryID(ctx, ids[1], testSessionEntryAt(cutoff.Add(time.Hour), "recent"))
	require.NoError(t, err)

	item := protocol.ThreadQueueItem{ID: "tags-queue", ConversationID: ids[2], Message: "waiting"}
	require.NoError(t, store.PutThreadQueueItem(item.ID, &item))
	_, err = store.AppendEntryID(ctx, ids[4], testSessionEntryAt(cutoff.Add(time.Hour), "recent"))
	require.NoError(t, err)
	_, err = store.PruneStateBefore(ctx, cutoff, 0)
	require.NoError(t, err)

	for i, id := range ids {
		tags, err := sessionTags(ctx, store.db, id)
		require.NoError(t, err)

		if i == 0 {
			require.Empty(t, tags)
		} else {
			require.Equal(t, []string{"customer"}, tags)
		}
	}

	require.NoError(t, store.RemoveExternalMCPConversation("tags-bound"))

	for _, id := range ids[3:] {
		tags, err := sessionTags(ctx, store.db, id)
		require.NoError(t, err)
		require.Empty(t, tags)
	}

	tags, err = sessionTags(ctx, store.db, ids[2])
	require.NoError(t, err)
	require.Equal(t, []string{"customer"}, tags)
}

func testDSNFile(workspace string) string {
	return filepath.Join(workspace, ".test-database-url")
}

func NewSessionService(workspace string) (*SessionService, error) {
	if data, err := os.ReadFile(testDSNFile(workspace)); err == nil {
		return NewSessionServiceIn(context.Background(), &config.Config{DatabaseURL: strings.TrimSpace(string(data)), Workspace: workspace}, slog.New(slog.DiscardHandler))
	}

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	if err != nil {
		return nil, fmt.Errorf("isolate test database: %w", err)
	}

	if err := os.WriteFile(testDSNFile(workspace), []byte(dsn), 0o600); err != nil {
		return nil, fmt.Errorf("remember test database url: %w", err)
	}

	return NewSessionServiceIn(context.Background(), &config.Config{DatabaseURL: dsn, Workspace: workspace}, slog.New(slog.DiscardHandler))
}

func testStoreDSN(workspace string) string {
	if data, err := os.ReadFile(testDSNFile(workspace)); err == nil {
		return strings.TrimSpace(string(data))
	}

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	if err != nil {
		return ""
	}

	if err := os.WriteFile(testDSNFile(workspace), []byte(dsn), 0o600); err != nil {
		return ""
	}

	return dsn
}

func AppendSessionEntryID(ctx context.Context, workspace, conversationID string, entry *harness.SessionEntry) (int64, error) {
	if entry == nil {
		return 0, errors.New("rocketcode session entry is required")
	}

	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return 0, errors.New("conversation ID is required")
	}

	if testStoreDSN(workspace) == os.Getenv("ROCKETCLAW_TEST_DATABASE_URL") {
		if _, err := NewSessionService(workspace); err != nil {
			return 0, err
		}
	}

	service, err := NewSessionServiceIn(ctx, &config.Config{DatabaseURL: testStoreDSN(workspace), Workspace: workspace}, slog.New(slog.DiscardHandler))
	if err != nil {
		return 0, err
	}

	defer func() { _ = service.Stop() }()

	return service.AppendEntryID(ctx, conversationID, entry)
}

func DeleteSession(ctx context.Context, workspace, conversationID string) (int64, error) {
	service, err := NewSessionServiceIn(ctx, &config.Config{DatabaseURL: testStoreDSN(workspace), Workspace: workspace}, slog.New(slog.DiscardHandler))
	if err != nil {
		return 0, err
	}
	defer func() { _ = service.Stop() }()

	return service.DeleteSession(ctx, conversationID)
}

func TestAttachmentMetadata(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	attachment := protocol.OutboundAttachment{ID: "snapshot", Name: "report.txt", MIMEType: "text/plain", Data: []byte(strings.Repeat("original", 700000)), OriginalUnverified: true}
	require.NoError(t, service.SaveAttachment(t.Context(), "producer", "", &attachment, false))

	var columns []string

	rows, err := service.db.QueryContext(t.Context(), `SELECT column_name FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='attachments' ORDER BY ordinal_position`)
	require.NoError(t, err)

	for rows.Next() {
		var column string
		require.NoError(t, rows.Scan(&column))
		columns = append(columns, column)
	}

	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Contains(t, columns, "size")
	require.NotContains(t, columns, "data")
	metadata, err := service.AttachmentMetadata(t.Context(), "producer", attachment.ID, false)
	require.NoError(t, err)

	want := attachment
	want.Size, want.Data = int64(len(attachment.Data)), nil
	require.Equal(t, want, metadata)

	for _, scope := range []struct {
		conversation string
		uploadOnly   bool
	}{{"other", false}, {"producer", true}} {
		_, err := service.AttachmentMetadata(t.Context(), scope.conversation, attachment.ID, scope.uploadOnly)
		require.ErrorIs(t, err, sql.ErrNoRows)
	}

	stored, err := service.LoadAttachment(t.Context(), "producer", attachment.ID, false)
	require.NoError(t, err)

	attachment.Size = int64(len(attachment.Data))
	require.Equal(t, attachment, stored)
	_, err = service.AttachmentMetadata(t.Context(), "producer", "missing", false)
	require.ErrorIs(t, err, sql.ErrNoRows)
	root, err := os.OpenRoot(service.attachments.(filesystemAttachments).path)

	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	original, err := root.ReadFile(attachment.ID)
	require.NoError(t, err)
	require.Equal(t, attachment.Data, original)
	require.NoError(t, root.Remove(attachment.ID))
	metadata, err = service.AttachmentMetadata(t.Context(), "producer", attachment.ID, false)
	require.NoError(t, err, "metadata must not read the storage driver")
	require.Equal(t, want, metadata)
	_, err = service.LoadAttachment(t.Context(), "producer", attachment.ID, false)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRemoveSessionEntryNUL(t *testing.T) {
	for _, sample := range []struct {
		text      string
		maxAllocs float64
	}{
		{"quoted \"value\", path \\\\ and newline\n", 0},
		{"literal \\u0000", 1},
		{"first\x00\x00\\\x00\\\\\x00\n\t😀", 1},
		{strings.Repeat("text\x00 and literal \\u0000", 1024), 1},
	} {
		data, err := json.Marshal(sample.text)
		require.NoError(t, err)

		original := string(data)

		var text string
		require.NoError(t, json.Unmarshal(removeSessionEntryNUL(data), &text))
		require.Equal(t, strings.ReplaceAll(sample.text, "\x00", ""), text)
		require.Equal(t, original, string(data))
		require.LessOrEqual(t, testing.AllocsPerRun(100, func() {
			removeSessionEntryNUL(data)
		}), sample.maxAllocs)
	}
}

func BenchmarkRemoveSessionEntryNUL(b *testing.B) {
	for _, sample := range []struct {
		name string
		text string
	}{
		{"plain", "ordinary text"},
		{"escaped", "quoted \"value\", path \\\\ and newline\n"},
		{"nul", "text\x00 and literal \\u0000"},
	} {
		b.Run(sample.name, func(b *testing.B) {
			data, err := json.Marshal(strings.Repeat(sample.text, 1024))
			require.NoError(b, err)
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()

			for b.Loop() {
				removeSessionEntryNUL(data)
			}
		})
	}
}

func BenchmarkObserveEntries(b *testing.B) {
	service, err := NewSessionService(b.TempDir())
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, service.Stop()) })

	entry := testSessionEntry(strings.Repeat("synthetic entry ", 4096), "assistant")
	for range 32 {
		_, err := service.AppendEntryID(b.Context(), "benchmark", entry)
		require.NoError(b, err)
	}

	b.ReportAllocs()

	for b.Loop() {
		entries, err := service.ObserveEntries(b.Context(), "benchmark")
		require.NoError(b, err)
		require.Len(b, entries, 32)
	}
}

func TestObserveEntriesJSONAllocationBudget(t *testing.T) {
	// Decoded entries own their JSON; the input conversion can reuse the row buffer.
	result := testing.Benchmark(BenchmarkObserveEntries)
	require.Positive(t, result.N)
	require.Less(t, result.AllocedBytesPerOp(), int64(6<<20))
}

func TestSessionStoreAppendAndLoad(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	store := newSessionStore("slack-thread:C123:111.222", service)
	entry := testSessionEntry("hello", "hi")

	_, err := service.AppendEntryID(t.Context(), store.conversationID, entry)
	require.NoError(t, err)
	require.Equal(t, []harness.SessionEntry{*entry}, collectEntries(t, storedEntries(t, service, store.conversationID)))
}

func TestSessionServiceAppendEntryIDAndObserveEntries(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)

	first := testSessionEntry("first\x00\x00\\\x00\\\\\x00\n\t😀", "assistant")
	second := testSessionEntry("literal \\u0000", "assistant")
	second.TokenUsage = &harness.TokenUsage{PromptTokens: 12, CompletionTokens: 5, TotalTokens: 17}
	id1, err := service.AppendEntryID(context.Background(), "main", first)
	require.NoError(t, err)
	id2, err := service.AppendEntryID(context.Background(), "main", second)
	require.NoError(t, err)
	assert.Greater(t, id2, id1)

	observed, err := service.ObserveEntries(context.Background(), "main")
	require.NoError(t, err)
	require.Len(t, observed, 2)
	assert.Equal(t, id1, observed[0].ID)
	assert.Equal(t, *testSessionEntry("first\\\\\\\n\t😀", "assistant"), observed[0].Entry)
	assert.Equal(t, id2, observed[1].ID)
	assert.Equal(t, *second, observed[1].Entry)

	var columnType string
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT pg_typeof(entry_json)::text FROM entries WHERE id = $1`, id1).Scan(&columnType))
	require.Equal(t, "json", columnType)

	for _, raw := range []string{`{`, `{"text":"\u0000"}`, `{"text":"\ud800"}`, `{"text":"\udc00"}`} {
		_, err := service.db.ExecContext(t.Context(), `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ('invalid', $1, '')`, raw)
		require.Error(t, err)
	}
}

func TestSessionServiceScheduledMessages(t *testing.T) {
	t.Parallel()

	dueAt := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)

	for _, test := range []struct {
		recurring bool
		stored    int
	}{{false, 0}, {true, 1}} {
		store := newTestSessionService(t)

		message := protocol.ScheduledMessageState{ConversationID: "slack-thread:D123:111.222", Agent: "helper", Message: "later", DueAt: dueAt, Recurring: test.recurring}
		if test.recurring {
			message.Interval = time.Minute
		}

		require.NoError(t, store.PutScheduledMessage("schedule-1", &message))
		messages, err := store.ScheduledMessages()
		require.NoError(t, err)
		assert.Equal(t, map[string]protocol.ScheduledMessageState{"schedule-1": message}, messages)

		var stored int
		require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM schedules WHERE id = $1`, "schedule-1").Scan(&stored))
		assert.Equal(t, test.stored, stored)
	}
}

func TestProducerRoutingSurvivesCompletion(t *testing.T) {
	t.Parallel()

	for _, destination := range []string{"owner", ""} {
		t.Run("destination="+destination, func(t *testing.T) {
			workspace := t.TempDir()
			store := newTestSessionServiceAt(t, workspace)
			ctx := t.Context()
			recordTestConversations(t, store, "main", map[string]ConversationKind{"producer": KindCronRun, "owner": KindWebChat})

			inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "produce", false)
			inbound.ConversationID, inbound.SyncDestination = "producer", destination
			inbound.RequireOutputDecision = destination == ""
			inbound.Metadata = map[string]string{"origin": "explicit"}
			require.NoError(t, startTurnDB(ctx, store.db, "first", "producer", inbound))
			endTestTurn(t, store, "producer", "first", "")
			require.NoError(t, store.Stop())
			store = newTestSessionServiceAt(t, workspace)

			routing := func() (*protocol.InboundMessage, string) {
				var (
					data      []byte
					reportsTo sql.NullString
					routing   *protocol.InboundMessage
				)
				require.NoError(t, store.db.QueryRowContext(ctx, `SELECT producer_inbound_json, reports_to FROM conversations WHERE conversation_id = 'producer'`).Scan(&data, &reportsTo))
				require.NoError(t, json.Unmarshal(data, &routing))

				return routing, reportsTo.String
			}

			retained, owner := routing()
			require.Equal(t, destination, owner)
			require.Equal(t, inbound.RequireOutputDecision, retained.RequireOutputDecision)
			require.Equal(t, inbound.Metadata, retained.Metadata)

			dao := stateDAO{db: store.db}
			owner, bound, err := dao.bindProducer(ctx, "producer", "owner")
			require.NoError(t, err)
			require.Equal(t, "owner", owner)
			require.Equal(t, destination == "", bound)

			// A later activation and a competing authorized bind cannot replace the owner.
			inbound.SyncDestination = "other"
			require.NoError(t, startTurnDB(ctx, store.db, "second", "producer", inbound))
			owner, bound, err = dao.bindProducer(ctx, "producer", "third")
			require.NoError(t, err)
			require.Equal(t, "owner", owner)
			require.False(t, bound)
			endTestTurn(t, store, "producer", "second", "")

			_, owner = routing()
			require.Equal(t, "owner", owner)
		})
	}
}

func TestSessionServiceThreadQueuePersistsOrderAndParkAfter(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)
	firstStash := time.Date(2000, 1, 2, 3, 0, 0, 0, time.UTC)
	secondStash := time.Date(2000, 1, 2, 4, 0, 0, 0, time.UTC)
	conversationID := "slack-thread:D123:111.222"
	dueAt := time.Date(2000, 1, 2, 16, 0, 0, 0, time.UTC)
	content := protocol.InboundContent{
		Text:               "write tests",
		TextAttachments:    []string{"Slack text file attachment data.csv:\na,b", "Forwarded Slack thread:\noriginal author: original text"},
		Attachments:        []protocol.InboundAttachment{{Name: "image.png", MIMEType: "image/png", Data: []byte("acquired image")}},
		AttachmentPresence: protocol.AttachmentPresenceImages,
		AttachmentWarnings: []string{"original warning"},
	}
	reply := &protocol.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.333", ThreadTS: "111.222"}

	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ConversationID: conversationID, Message: "write tests", Content: content, Source: protocol.SourceSlack, SlackReply: reply, Principal: "U1", StashAt: firstStash, Position: 0, ParkAfter: "s1", SlackChannel: reply.ChannelID, SlackTS: reply.MessageTS}))
	require.NoError(t, store.PutThreadQueueItem("q2", &protocol.ThreadQueueItem{Kind: "steer", ConversationID: conversationID, Message: "write changelog", Principal: "U2", StashAt: secondStash, Position: 1, ParkAfter: "s1", SlackChannel: "D123", SlackTS: "333.444"}))
	require.NoError(t, store.PutThreadQueueItem("other", &protocol.ThreadQueueItem{ConversationID: "other", Message: "keep", Principal: "U2", StashAt: firstStash, Position: 0}))
	require.NoError(t, store.PutScheduledMessage("s1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "helper", Message: "later", DueAt: dueAt}))
	require.NoError(t, store.Stop())
	store = newTestSessionServiceAt(t, workspace)

	items, err := store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, content, items[0].Content)
	assert.Equal(t, protocol.SourceSlack, items[0].Source)
	assert.Equal(t, reply, items[0].SlackReply)

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{}), config: Config{ConversationID: conversationID, SessionService: store}}
	require.NoError(t, bridge.submitEnqueuedItem(t.Context(), &items[0]))
	inbound := (<-bridge.requestCh).inbound
	assert.Equal(t, "write tests\n\nSlack text file attachment data.csv:\na,b\n\nForwarded Slack thread:\noriginal author: original text", inbound.Text)
	assert.Equal(t, content.Attachments, inbound.Attachments)
	assert.Equal(t, content.AttachmentWarnings, inbound.AttachmentWarnings)
	assert.Equal(t, protocol.AttachmentPresenceImages, inbound.AttachmentPresence)
	assert.Equal(t, protocol.SourceSlack, inbound.Source)
	assert.Equal(t, reply, inbound.SlackReply)
	assert.Equal(t, "U1", inbound.Metadata[protocol.InboundPrincipalMetadataKey])
	assert.Equal(t, protocol.InboundKind("enqueue"), items[0].Kind)
	assert.Equal(t, protocol.InboundKind("steer"), items[1].Kind)
	assert.Equal(t, "U1", items[0].Principal)
	assert.Equal(t, "U2", items[1].Principal)
	assert.Equal(t, "write changelog", items[1].Message)
	assert.Equal(t, "D123", items[1].SlackChannel)
	assert.Equal(t, "333.444", items[1].SlackTS)
	assert.Equal(t, "q1", items[0].ID)
	assert.Equal(t, "write tests", items[0].Message)
	assert.Equal(t, firstStash, items[0].StashAt)
	assert.Equal(t, "s1", items[0].ParkAfter)
	assert.Equal(t, "q2", items[1].ID)
	assert.Equal(t, secondStash, items[1].StashAt)
	assert.Equal(t, "s1", items[1].ParkAfter)

	scheduled, err := store.ScheduledMessagesForConversation(conversationID)
	require.NoError(t, err)
	assert.Equal(t, protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "helper", Message: "later", DueAt: dueAt}, scheduled["s1"])
	rows := protocol.MixedLaterWork(items, scheduled)
	require.Len(t, rows, 3)
	assert.Equal(t, protocol.LaterWorkScheduled, rows[0].Kind)
	assert.Equal(t, "q1", rows[1].Queue.ID)
	assert.Equal(t, "q2", rows[2].Queue.ID)

	require.NoError(t, store.ResetScheduledMessages(conversationID))

	items, err = store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, items, 2)

	require.NoError(t, store.DeleteThreadQueueItem("q2"))

	items, err = store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "q1", items[0].ID)
}

func TestSessionServiceMigratesThreadQueueKind(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	// Recreate the pre-006 queue in this test's isolated database schema, from before 056 moved it into turns.
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(t.Context(), store.db, "postgres", source, migrate.Down, 55)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `ALTER TABLE thread_queue DROP COLUMN IF EXISTS kind; DELETE FROM pg_migrations WHERE id = '006_thread_queue_kind.sql'; INSERT INTO thread_queue (queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position) VALUES ('old', 'recorded', 'original content', 'original author', 1, 0);
INSERT INTO legacy_lane_work (conversation_id, position, queue_item_id, kind) VALUES ('recorded', 1, 'old', 'enqueue')`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(t.Context(), store.db, slog.New(slog.DiscardHandler)))
	items, err := store.ThreadQueueForConversation("recorded")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, protocol.InboundKind("enqueue"), items[0].Kind)
	assert.Equal(t, "original content", items[0].Message)
	assert.Equal(t, "original author", items[0].Principal)
	items[0].Kind = "steer"
	require.NoError(t, store.PutThreadQueueItem("old", &items[0]))
	items, err = store.ThreadQueueForConversation("recorded")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, protocol.InboundKind("steer"), items[0].Kind)
}

// The State Store ends with its 14 tables, shadow_turns and shadow_steps, and the retired tables and 049's manifest
// under legacy_ names; nothing ties a legacy table to a live row or notifies for it.
func TestSessionServiceHeadSchemaTables(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	// The run lock creates its table at startup, as holdRunLock does.
	client, _ := pglock.UnsafeNew(store.db, pglock.WithCustomTable(runLockTable))
	require.NoError(t, client.TryCreateTable())

	tables, err := queryStrings(t.Context(), store.db, `SELECT tablename FROM pg_tables WHERE schemaname = current_schema()`, "tables")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		"conversations", "turns", "entries", "steps", "schedules", "attachments", "pending_attachment_deletions", "slack_channel_facts",
		"voice_settings", "hosted_pages", "web_spaces", "web_tab_states", "rocketclaw_locks", "pg_migrations", "shadow_turns", "shadow_steps",
		"legacy_entry_md5", "legacy_history", "legacy_listed", "legacy_shown", "legacy_lane_work", "legacy_row_counts", "legacy_hidden_run_buckets",
		"legacy_unrecorded_conversations", "legacy_unmoved_facts", "legacy_turn_steps",
		"legacy_thread_queue", "legacy_background_jobs", "legacy_scheduled_messages", "legacy_cron_schedules", "legacy_cron_schedule_runs",
		"legacy_session_summaries", "legacy_session_tags", "legacy_conversation_goals", "legacy_external_mcp_sessions", "legacy_shadow_bindings",
		"legacy_opencode_sessions", "legacy_message_search", "legacy_message_search_indexed", "legacy_code_mode_calls",
	}, tables)

	bound, err := queryStrings(t.Context(), store.db, `WITH legacy AS (
    SELECT oid FROM pg_class WHERE relnamespace = current_schema()::regnamespace AND relkind = 'r' AND starts_with(relname, 'legacy_')
)
SELECT c.conname::text FROM pg_constraint c JOIN legacy l ON l.oid IN (c.conrelid, c.confrelid) WHERE c.contype = 'f'
UNION ALL SELECT t.tgname::text FROM pg_trigger t JOIN legacy l ON l.oid = t.tgrelid WHERE NOT t.tgisinternal`, "legacy foreign keys and triggers")
	require.NoError(t, err)
	require.Empty(t, bound)
}

func TestSessionServiceInitializesActiveTurnIndexes(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)

	rows, err := store.db.QueryContext(context.Background(), `SELECT tablename FROM pg_tables WHERE schemaname = current_schema() AND tablename LIKE 'turns%' UNION SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND indexname LIKE 'turns%' ORDER BY 1`)

	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var names []string

	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}

	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"turns", "turns_conversation_updated", "turns_job", "turns_pending_notes", "turns_pkey", "turns_search_text", "turns_waiting_input"}, names)
}

func TestInitializeSessionDBReportsMigrationError(t *testing.T) {
	t.Parallel()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	logger := slog.New(slog.DiscardHandler)
	db, err := openSessionDB(t.Context(), dsn, logger)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	err = initializeSessionDB(t.Context(), db, logger)
	require.ErrorIs(t, err, errApplySchemaMigrations)

	db, err = openSessionDB(t.Context(), dsn, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), `DROP TABLE pg_migrations`)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `CREATE TABLE pg_migrations (id int)`)
	require.NoError(t, err)
	err = initializeSessionDB(t.Context(), db, logger)
	require.ErrorIs(t, err, errApplySchemaMigrations)
}

func TestSessionServiceAppliesSchemaMigrationsOnce(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	first := newTestSessionServiceAt(t, workspace)

	var n int
	require.NoError(t, first.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pg_migrations`).Scan(&n))
	assert.Equal(t, sessionMigrationCount(t), n)
	require.Error(t, first.db.QueryRowContext(t.Context(), `SELECT 1 FROM store_bootstrap`).Scan(&n))

	second, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: testStoreDSN(workspace), Workspace: workspace}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Stop()) })
	require.NoError(t, second.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pg_migrations`).Scan(&n))
	assert.Equal(t, sessionMigrationCount(t), n)
}

func TestInitializeSessionDBUpgradesMainSchema(t *testing.T) {
	t.Parallel()

	for _, prefix := range []int{5, 8, 16, 17, 18} {
		for _, ledger := range []string{"pg_migrations", "gorp_migrations"} {
			t.Run(fmt.Sprintf("%d/%s", prefix, ledger), func(t *testing.T) {
				dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
				require.NoError(t, err)
				cfg, err := pgx.ParseConfig(dsn)
				require.NoError(t, err)

				db := stdlib.OpenDB(*cfg)

				t.Cleanup(func() { require.NoError(t, db.Close()) })

				source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
				applied, err := (migrate.MigrationSet{TableName: ledger}).ExecMaxContext(t.Context(), db, "postgres", source, migrate.Up, prefix)
				require.NoError(t, err)
				require.Equal(t, prefix, applied)

				_, err = db.ExecContext(t.Context(), `UPDATE `+ledger+` SET applied_at='2026-01-01Z';
					ALTER TABLE managed_conversations ADD COLUMN settled_override boolean NOT NULL DEFAULT false;
					ALTER TABLE managed_conversations ADD COLUMN bumped_at_unix_ns bigint NOT NULL DEFAULT 0;
					INSERT INTO managed_conversations (conversation_id, agent, created_by, settled_override, bumped_at_unix_ns) VALUES ('synthetic', 'main', 'owner', true, 123);
					INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('synthetic', '{"text":"synthetic\u0000\u0000history","literal":"\\u0000","mixed":"\\\u0000","nested":["\u0000"],"\u0000key":"value"}', '2026-09-09T12:00:00.123456Z');
					INSERT INTO active_turns (id, conversation_id, agent, model, display_model, replay_input_json, output_trace_json, token_usage_json, response_id, open_function_calls_json, completed_function_outputs_json, restart_notice_json, source_metadata_json, created_at_unix_ns, updated_at_unix_ns) VALUES ('checkpoint', 'synthetic', '', '', '', '[]', '[]', 'null', '', '[]', '[]', '', '{}', 1, 1);
					INSERT INTO thread_queue (queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position) VALUES ('q', 'synthetic', 'queued', 'owner', 456, 7)`)
				require.NoError(t, err)

				if prefix == 8 {
					_, err = db.ExecContext(t.Context(), `ALTER TABLE thread_queue ALTER COLUMN kind SET DEFAULT ''; UPDATE thread_queue SET kind=''`)
					require.NoError(t, err)
				}

				if prefix >= 16 {
					_, err = db.ExecContext(t.Context(), `INSERT INTO session_summaries (conversation_id, preview, last_updated) VALUES ('synthetic', decode('610062', 'hex'), now()), ('clean', 'valid'::bytea, now())`)
					require.NoError(t, err)
				}

				backfillPreviousRelease(t, db, ledger)

				for range 2 {
					require.NoError(t, initializeSessionDB(t.Context(), db, slog.New(slog.DiscardHandler)))
				}

				var count int
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_migrations`).Scan(&count))
				require.Equal(t, sessionMigrationCount(t), count)
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_migrations WHERE applied_at='2026-01-01Z'`).Scan(&count))
				require.Equal(t, prefix, count)
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM legacy_session_tags`).Scan(&count))
				require.Zero(t, count)

				for _, query := range []string{
					`SELECT count(*) FROM conversations WHERE conversation_id='synthetic' AND agent='main' AND created_by='owner' AND NOT settled AND NOT pinned AND snoozed_until IS NULL AND name='' AND forked_from='' AND settled_override AND bumped_at_unix_ns=123`,
					`SELECT count(*) FROM entries WHERE conversation_id='synthetic' AND entry_json::text='{"text":"synthetichistory","literal":"\\u0000","mixed":"\\","nested":[""],"key":"value"}' AND entry_timestamp='2026-09-09T12:00:00.123456Z'`,
					`SELECT count(*) FROM turns WHERE phase='waiting' AND input_id='q' AND message='queued' AND principal='owner' AND created_at_unix_ns=456 AND position=7 AND content='{}'`,
				} {
					require.NoError(t, db.QueryRowContext(t.Context(), query).Scan(&count))
					require.Equal(t, 1, count)
				}

				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM turns WHERE phase <> 'waiting'`).Scan(&count))
				require.Zero(t, count, "the clean cutover drops in-flight turns from the old version")

				if prefix >= 16 {
					require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM conversations WHERE conversation_id = 'synthetic' AND last_updated IS NOT NULL`).Scan(&count))
					require.Zero(t, count)
					// A summary of an ID without a conversation stays as it was, outside the conversations.
					require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM legacy_unmoved_facts WHERE conversation_id = 'clean' AND row->>'preview' = '\x76616c6964'`).Scan(&count))
					require.Equal(t, 1, count)
				}

				var kind, defaultKind string
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT delivery FROM turns WHERE input_id='q'`).Scan(&kind))
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT column_default FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='legacy_thread_queue' AND column_name='kind'`).Scan(&defaultKind))

				if prefix == 8 {
					require.Empty(t, kind)
					require.Equal(t, "''::text", defaultKind)
				} else {
					require.Equal(t, "enqueue", kind)
					require.Equal(t, "'enqueue'::text", defaultKind)
				}
			})
		}
	}
}

func TestSessionServiceRenamesGorpMigrations(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	first := newTestSessionServiceAt(t, workspace)
	_, err := first.db.ExecContext(t.Context(), `ALTER TABLE pg_migrations RENAME TO gorp_migrations`)
	require.NoError(t, err)

	second := newTestSessionServiceAt(t, workspace)

	var n int
	require.NoError(t, second.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pg_migrations`).Scan(&n))
	assert.Equal(t, sessionMigrationCount(t), n)
	require.Error(t, second.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM gorp_migrations`).Scan(&n))
}

func TestSlackChannelFacts(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	at := time.Unix(10, 123)
	require.NoError(t, store.RecordChannelFact(ctx, "T1", "C1", "old", at))
	require.NoError(t, store.RecordChannelFact(ctx, "T1", "C1", "new", at.Add(time.Nanosecond)))
	require.NoError(t, store.RecordChannelFact(ctx, "T1", "C1", "stale", at))
	require.NoError(t, store.RecordChannelFact(ctx, "T2", "C1", "other", at))

	for _, tc := range []struct {
		workspace, id, name string
		found               bool
	}{
		{"T1", "C1", "new", true}, {"T2", "C1", "other", true}, {"T1", "missing", "", false},
	} {
		name, found, err := store.ChannelFact(ctx, tc.workspace, tc.id)
		require.NoError(t, err)
		assert.Equal(t, tc.name, name)
		assert.Equal(t, tc.found, found)
	}

	recordTestConversations(t, store, "main", map[string]ConversationKind{
		protocol.SlackThreadConversationID("G1", "1.1"): KindSlackThread, protocol.SlackThreadConversationID("G1", "2.2"): KindSlackThread, "web-session": KindWebChat,
	})
	require.NoError(t, store.RecordChannelFact(ctx, "T2", "Cother", "other", at))
	ids, err := store.SlackChannelIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"G1"}, ids)
}

func TestSessionServiceSyncCronSchedulesInsertsUpdatesAndDeletes(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	due := now.Add(time.Minute)

	require.NoError(t, store.SyncCronSchedules([]CronScheduleState{
		{ScheduleID: "daily#0", RelativePath: "daily.md", NextDue: due},
		{ScheduleID: "daily#1", RelativePath: "daily.md", NextDue: due},
		{ScheduleID: "weekly#0", RelativePath: "weekly.md", NextDue: due.Add(time.Hour)},
		{ScheduleID: "running#0", RelativePath: "running.md", NextDue: now},
	}))
	claimed, err := store.ClaimCronSchedule(CronScheduleState{ScheduleID: "running#0", RelativePath: "running.md", NextDue: now}, due, now)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, store.SyncCronSchedules([]CronScheduleState{
		{ScheduleID: "daily#0", RelativePath: "daily.md", NextDue: due.Add(2 * time.Hour)},
	}))

	schedules, err := store.DueCronSchedules(due.Add(24 * time.Hour))
	require.NoError(t, err)
	assert.Equal(t, []CronScheduleState{{ScheduleID: "daily#0", RelativePath: "daily.md", NextDue: due}}, schedules)

	require.NoError(t, store.SyncCronSchedules([]CronScheduleState{{ScheduleID: "empty#0", RelativePath: "empty.md"}}))

	dueSchedules, err := store.DueCronSchedules(now)
	require.NoError(t, err)
	assert.Contains(t, dueSchedules, CronScheduleState{ScheduleID: "empty#0", RelativePath: "empty.md"})
	claimed, err = store.ClaimCronSchedule(dueSchedules[0], due, now)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, store.ResetCronSchedules())
	ids, err := queryStrings(t.Context(), store.db, `SELECT id FROM schedules`, "cron schedules after reset")
	require.NoError(t, err)
	require.Empty(t, ids)
}

func TestSessionServiceDueCronSchedulesHonorsDueTime(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)

	require.NoError(t, store.SyncCronSchedules([]CronScheduleState{
		{ScheduleID: "second", RelativePath: "second.md", NextDue: now},
		{ScheduleID: "first", RelativePath: "first.md", NextDue: now.Add(-time.Minute)},
		{ScheduleID: "later", RelativePath: "later.md", NextDue: now.Add(time.Minute)},
	}))

	due, err := store.DueCronSchedules(now)
	require.NoError(t, err)
	assert.Equal(t, []CronScheduleState{
		{ScheduleID: "first", RelativePath: "first.md", NextDue: now.Add(-time.Minute)},
		{ScheduleID: "second", RelativePath: "second.md", NextDue: now},
	}, due)
}

// AE4: a cron line due while a run of its file is still running is skipped and moves to its next time.
func TestSessionServiceClaimCronScheduleSerializesSamePathAndCompletionClearsRunning(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	next := now.Add(time.Hour)

	dueDaily := CronScheduleState{ScheduleID: "daily#0", RelativePath: "daily.md", NextDue: now}
	dueDailySecond := CronScheduleState{ScheduleID: "daily#1", RelativePath: "daily.md", NextDue: now}
	require.NoError(t, store.SyncCronSchedules([]CronScheduleState{
		dueDaily,
		dueDailySecond,
	}))

	ok, err := store.ClaimCronSchedule(dueDaily, next, now)
	require.NoError(t, err)
	require.True(t, ok)

	// The claimed run starts; a run of another file, and a reminder waiting in this one, do not count.
	run := protocol.Conversation{ID: "cron:daily.md:run", Agent: "main", Run: &protocol.CronRun{Path: "daily.md", At: now}}
	require.NoError(t, store.recordConversation(t.Context(), run, conversationFacts{}))
	require.NoError(t, startTurnDB(t.Context(), store.db, "daily-run", run.ID, protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "job", false)))
	require.NoError(t, store.PutScheduledMessage("follow-up", &protocol.ScheduledMessageState{ConversationID: run.ID, Agent: "main", Message: "later", DueAt: next}))

	ok, err = store.ClaimCronSchedule(dueDailySecond, next, now)
	require.NoError(t, err)
	assert.False(t, ok)

	due, err := store.DueCronSchedules(now)
	require.NoError(t, err)
	assert.Empty(t, due)

	ok, err = store.ClaimCronSchedule(dueDaily, next.Add(time.Hour), now)
	require.NoError(t, err)
	assert.False(t, ok)

	endTestTurn(t, store, run.ID, "daily-run", "")

	dueDaily.NextDue = next
	ok, err = store.ClaimCronSchedule(dueDaily, next.Add(time.Hour), next)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestSessionServiceActiveGoalThreads(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	threads, err := store.ActiveGoalThreads()
	require.NoError(t, err)
	assert.Nil(t, threads)

	for _, id := range []string{"active", "legacy", "terminal"} {
		require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))
		require.NoError(t, store.BeginGoal(id, &GoalState{Objective: "work", MaxTurns: 1}))
	}

	_, err = store.db.ExecContext(t.Context(), `UPDATE conversations SET goal_status = '' WHERE conversation_id = 'legacy'`)
	require.NoError(t, err)
	_, err = store.UpdateGoalStatus("terminal", GoalStatusComplete, "done")
	require.NoError(t, err)

	threads, err = store.ActiveGoalThreads()
	require.NoError(t, err)
	assert.Equal(t, map[string]ThreadState{
		"active": {Agent: "planner", CreatedBy: ThreadCreatedByCron},
		"legacy": {Agent: "planner", CreatedBy: ThreadCreatedByCron},
	}, threads)
}

func TestSessionServiceBeginGoalPersistsCheckScript(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	recordTestConversations(t, store, "main", map[string]ConversationKind{"thread-1": KindWebChat, "thread-2": KindWebChat})

	require.NoError(t, store.BeginGoal("thread-1", &GoalState{Objective: " fix lint ", CheckScript: " ./scripts/check.sh --linter-mode ", MaxTurns: 3}))
	goal, ok, err := store.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "fix lint", goal.Objective)
	assert.Equal(t, "./scripts/check.sh --linter-mode", goal.CheckScript)
	assert.Equal(t, 3, goal.MaxTurns)

	require.NoError(t, store.BeginGoal("thread-2", &GoalState{Objective: "write docs", CheckScript: " ", MaxTurns: 1}))
	goal, ok, err = store.Goal("thread-2")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, goal.CheckScript)
}

func TestSessionServiceBeginGoalRejectsActiveGoal(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	recordTestConversations(t, store, "main", map[string]ConversationKind{"thread-1": KindWebChat})

	require.NoError(t, store.BeginGoal("thread-1", &GoalState{Objective: "first", MaxTurns: 3}))
	err := store.BeginGoal("thread-1", &GoalState{Objective: "second", MaxTurns: 3})
	require.ErrorIs(t, err, protocol.ErrGoalAlreadyActive)

	goal, ok, err := store.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "first", goal.Objective)
}

func TestSessionServiceBeginGoalAllowsGoalAfterTerminal(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	recordTestConversations(t, store, "main", map[string]ConversationKind{"thread-1": KindWebChat})

	require.NoError(t, store.BeginGoal("thread-1", &GoalState{Objective: "first", MaxTurns: 3}))
	_, err := store.UpdateGoalStatus("thread-1", GoalStatusComplete, "done")
	require.NoError(t, err)
	require.NoError(t, store.BeginGoal("thread-1", &GoalState{Objective: "second", CheckScript: "./check.sh", MaxTurns: 1}))

	goal, ok, err := store.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "second", goal.Objective)
	assert.Equal(t, GoalStatusActive, goal.Status)
	assert.Equal(t, 0, goal.TurnsUsed)
}

func TestSessionServiceProgressGoalKeepsGoalActiveAndRecordsNote(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	recordTestConversations(t, store, "main", map[string]ConversationKind{"thread-1": KindWebChat})

	require.NoError(t, store.BeginGoal("thread-1", &GoalState{Objective: "first", MaxTurns: 3}))
	goal, err := store.UpdateGoalStatus("thread-1", GoalStatusProgress, "next step")
	require.NoError(t, err)
	assert.Equal(t, GoalStatusActive, goal.Status)
	assert.Equal(t, "next step", goal.Note)

	goal, ok, err := store.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, GoalStatusActive, goal.Status)
	assert.Equal(t, "next step", goal.Note)
}

func TestSessionStoreLoadsLargeImageTurn(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	entry := harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), ResponseID: "", Model: "gpt-6.1-sol", ReplayInput: testReplayInput(replayInputMessage{role: "user", text: strings.Repeat("x", 128*1024)})}

	_, err := service.AppendEntryID(t.Context(), "main", &entry)
	require.NoError(t, err)
	require.Equal(t, []harness.SessionEntry{entry}, collectEntries(t, storedEntries(t, service, "main")))
}

func TestAppendSessionEntryDBReportsWriteFailures(t *testing.T) {
	t.Parallel()

	entry := &harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), ReplayInput: []json.RawMessage{json.RawMessage("{")}}
	_, err := appendSessionEntryDB(context.Background(), errStore{}, "main", entry)
	require.ErrorContains(t, err, "marshal rocketcode session entry")

	entry.ReplayInput = nil
	_, err = appendSessionEntryDB(context.Background(), errStore{errExec: errors.New("no write")}, "main", entry)
	require.ErrorContains(t, err, "read run destination")
}

func TestNewSessionServiceReportsInvalidDatabaseURL(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)
	_, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: "not-a-dsn", Workspace: t.TempDir()}, logger)
	require.Error(t, err)

	_, err = NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: "postgres://u:s3cret@127.0.0.1:1/none?sslmode=disable", Workspace: t.TempDir()}, logger)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "s3cret")

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	db, err := openSessionDB(t.Context(), dsn, logger)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `DROP TABLE pg_migrations`)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `CREATE TABLE pg_migrations (id int)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, logger)
	require.Error(t, err)
}

func TestHoldRunLockWaitsForReleaseOrCancellation(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	// This holder tests contention, not renewal; a heartbeat can race Close.
	client, err := pglock.UnsafeNew(service.db, pglock.WithCustomTable(runLockTable), pglock.WithHeartbeatFrequency(0))
	require.NoError(t, err)
	require.NoError(t, client.TryCreateTable())
	lock, err := client.Acquire(runLockName, pglock.FailIfLocked())
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })

	errWork := errors.New("work completed")
	work := &runLockWorkMock{RunFunc: func(ctx context.Context) error {
		require.NoError(t, ctx.Err())
		return errWork
	}}
	// pglock waits on real PostgreSQL I/O; cancellation bounds the contended wait.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	err = holdRunLock(ctx, service.db, work)
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	require.ErrorIs(t, err, pglock.ErrNotAcquired)
	require.Empty(t, work.RunCalls())

	require.NoError(t, lock.Close())
	require.ErrorIs(t, holdRunLock(t.Context(), service.db, work), errWork)
	require.Len(t, work.RunCalls(), 1)
}

func TestHoldRunLockAllowsSessionServiceWhileHeld(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	// This holder tests service access, not renewal; a heartbeat can race Close.
	client, err := pglock.UnsafeNew(service.db, pglock.WithCustomTable(runLockTable), pglock.WithHeartbeatFrequency(0))
	require.NoError(t, err)
	require.NoError(t, client.TryCreateTable())
	lock, err := client.Acquire(runLockName, pglock.FailIfLocked())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Close()) })

	second, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: testStoreDSN(workspace), Workspace: workspace}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NoError(t, second.Stop())
}

func TestSessionStoreMissingIsEmpty(t *testing.T) {
	t.Parallel()

	require.Empty(t, collectEntries(t, storedEntries(t, newTestSessionService(t), "main")))
}

func TestSessionStoreReportsObserveError(t *testing.T) {
	t.Parallel()

	service, err := NewSessionService(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, service.Stop())

	_, err = service.ObserveEntries(t.Context(), "main")
	assert.ErrorContains(t, err, "query rocketcode session entries")
}

func TestMemoryStoreAppendAndLoad(t *testing.T) {
	t.Parallel()

	var store memoryStore

	entry := testSessionEntry("memory", "assistant")

	require.NoError(t, store.Append(t.Context(), entry))
	require.Equal(t, []harness.SessionEntry{*entry}, collectEntries(t, store.in()))

	for got, err := range store.in() {
		require.NoError(t, err)
		assert.Equal(t, *entry, got)

		break
	}
}

func TestSessionStoreRejectsNilEntry(t *testing.T) {
	t.Parallel()

	_, err := AppendSessionEntryID(context.Background(), t.TempDir(), "main", nil)
	require.ErrorContains(t, err, "rocketcode session entry is required")
}

func TestAppendSessionEntryIDRejectsBlankConversationID(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	entry := testSessionEntry("blank conversation", "assistant")

	_, err := AppendSessionEntryID(context.Background(), workspace, " \t ", entry)
	require.EqualError(t, err, "conversation ID is required")
}

func TestSessionInspectionMissingDBDoesNotCreateRuntimeDir(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()

	service, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: testStoreDSN(workspace), Workspace: workspace}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	assert.Empty(t, sidebarSummaries(t, service))

	entries, err := service.ObserveEntries(context.Background(), "main")
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.NoDirExists(t, filepath.Join(workspace, ".rocketclaw"))
}

func TestSidebarSummariesIncludeLastMessages(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	recordTestConversations(t, service, "main", map[string]ConversationKind{"main": KindWebChat, "slack-thread:D123:111.222": KindSlackThread})

	_, err := AppendSessionEntryID(context.Background(), workspace, "main", testSessionEntry("first user", "first assistant\x00"))
	require.NoError(t, err)
	_, err = AppendSessionEntryID(context.Background(), workspace, "main", testSessionEntry("second\nuser", "second assistant"))
	require.NoError(t, err)

	last := testSessionEntry(" \n", "assistant without a new user message")
	last.Timestamp = last.Timestamp.Add(-time.Second + 123456789*time.Nanosecond)
	_, err = AppendSessionEntryID(context.Background(), workspace, "main", last)
	require.NoError(t, err)
	_, err = AppendSessionEntryID(context.Background(), workspace, "slack-thread:D123:111.222", testSessionEntry("thread user", "thread assistant"))
	require.NoError(t, err)
	_, err = AppendSessionEntryID(context.Background(), workspace, "unrequested", testSessionEntry("unrequested user", "unrequested assistant"))
	require.NoError(t, err)

	summaries := sidebarSummaries(t, service)
	require.Len(t, summaries, 2, "history without a recorded conversation is not listed")

	assert.Equal(t, &protocol.SessionSummary{ConversationID: "main", LastUpdated: last.Timestamp.Truncate(time.Microsecond), LastMessage: "assistant without a new user message"}, summaries["main"])
	assert.Equal(t, "thread assistant", summaries["slack-thread:D123:111.222"].LastMessage)
}

func TestSessionServiceSerializesConcurrentAccess(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	recordTestConversations(t, service, "main", map[string]ConversationKind{"main": KindWebChat})

	var group sync.WaitGroup

	errCh := make(chan error, 50)

	for i := range 25 {
		group.Add(1)

		go func(i int) {
			defer group.Done()

			_, err := service.AppendEntryID(t.Context(), "main", testSessionEntryAt(time.Unix(int64(25-i), 123456789).UTC(), fmt.Sprintf("user %d 日本 %s", i, strings.Repeat("full preview ", 100))))
			errCh <- err

			errCh <- service.UpsertThread(fmt.Sprintf("thread-%02d", i), ThreadState{Agent: "main"})
		}(i)
	}

	group.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	entries, err := service.ObserveEntries(context.Background(), "main")
	require.NoError(t, err)
	require.Len(t, entries, 25)
	last := entries[len(entries)-1].Entry
	messages, err := replayInputMessages(last.ReplayInput)
	require.NoError(t, err)
	require.Equal(t, map[string]*protocol.SessionSummary{"main": {ConversationID: "main", LastMessage: messages[len(messages)-1].text, LastUpdated: last.Timestamp.Truncate(time.Microsecond)}}, sidebarSummaries(t, service))

	threadIDs, err := queryStrings(context.Background(), service.db, `SELECT conversation_id FROM conversations ORDER BY conversation_id`, "managed conversation IDs")
	require.NoError(t, err)
	assert.Len(t, threadIDs, 26, "main and each thread")
}

func TestSessionServiceBeginGoalRejectsConcurrentActiveStarts(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	recordTestConversations(t, service, "main", map[string]ConversationKind{"thread": KindWebChat})

	errCh := make(chan error, 20)

	var group sync.WaitGroup

	for i := range 20 {
		group.Add(1)

		go func(i int) {
			defer group.Done()

			errCh <- service.BeginGoal("thread", &GoalState{Objective: fmt.Sprintf("goal %02d", i), MaxTurns: 5})
		}(i)
	}

	group.Wait()
	close(errCh)

	successes := 0
	duplicates := 0

	for err := range errCh {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, protocol.ErrGoalAlreadyActive):
			duplicates++
		default:
			require.NoError(t, err)
		}
	}

	assert.Equal(t, 1, successes)
	assert.Equal(t, 19, duplicates)
}

func TestSessionServiceConcurrentGoalTurnsPreserveAccounting(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	recordTestConversations(t, service, "main", map[string]ConversationKind{"thread": KindWebChat})
	require.NoError(t, service.BeginGoal("thread", &GoalState{Objective: "ship it", MaxTurns: 20}))

	errCh := make(chan error, 20)

	var group sync.WaitGroup

	for i := range 20 {
		turnID := fmt.Sprintf("turn-%d", i)
		seedActiveTurn(t, service, "thread", turnID, nil)
		group.Go(func() {
			_, err := service.finishTurn(t.Context(), turnID, &turnFinish{store: newSessionStore("thread", service), accountGoal: true, outbound: protocol.NewOutboundMessage("thread", "")})
			errCh <- err
		})
	}

	group.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	goal, ok, err := service.Goal("thread")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 20, goal.TurnsUsed)
	assert.Equal(t, GoalStatusBudgetExhausted, goal.Status)
}

func TestSessionServiceThreadStatePersistsAtomically(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)

	for i := range 25 {
		conversationID := fmt.Sprintf("thread-%02d", i)
		require.NoError(t, service.UpsertThread(conversationID, ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))

		thread, ok, err := service.Thread(conversationID)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}, thread)
	}
}

func TestSessionServiceThreadAgentUpdatePreservesCreator(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	require.NoError(t, service.UpsertThread("thread", ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, service.UpsertThread("thread", ThreadState{Agent: "main"}))

	thread, ok, err := service.Thread("thread")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ThreadState{Agent: "main", CreatedBy: ThreadCreatedByCron}, thread)
}

func TestDeleteSessionDeletesOnlyTarget(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	recordTestConversations(t, service, "main", map[string]ConversationKind{"thread": KindWebChat})
	_, err := service.AppendEntryID(context.Background(), "main", testSessionEntry("main", "assistant"))
	require.NoError(t, err)
	_, err = service.AppendEntryID(context.Background(), "thread", testSessionEntry("thread", "assistant"))
	require.NoError(t, err)
	require.NoError(t, service.UpsertThread("main", ThreadState{Agent: "ops"}))
	require.NoError(t, service.BeginGoal("main", &GoalState{Objective: "ship it", MaxTurns: 5}))
	seedActiveTurn(t, service, "main", "unshadowed-active", nil)
	manager := newThreadBridgeManager(service.definitions, service, service.log, &bridgeFactoryMock{newBridgeFunc: func(cfg *Config) directBridge {
		cfg.SessionService = service
		return NewConversation(service.definitions, discardPublisher{}, cfg, service.log)
	}})
	_, err = manager.recordedBridge(t.Context(), "main")
	require.NoError(t, err)

	service.historyWorkers = manager

	deleted, err := service.DeleteSession(context.Background(), "main")
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)
	active, err := service.HasActiveTurn(t.Context(), "main")
	require.NoError(t, err)
	require.True(t, active, "history clear does not stop unshadowed work")

	mainEntries, err := service.ObserveEntries(context.Background(), "main")
	require.NoError(t, err)
	assert.Empty(t, mainEntries)

	threadEntries, err := service.ObserveEntries(context.Background(), "thread")
	require.NoError(t, err)
	assert.Len(t, threadEntries, 1)

	thread, ok, err := service.Thread("main")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ThreadState{Agent: "ops"}, thread)

	goal, ok, err := service.Goal("main")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "ship it", goal.Objective)
	// The cleared chat stays listed only by its running turn's prompt; the other keeps its preview.
	summaries := sidebarSummaries(t, service)
	require.Equal(t, "seed", summaries["main"].LastMessage)
	require.Equal(t, &protocol.SessionSummary{ConversationID: "thread", LastMessage: "assistant", LastUpdated: time.Unix(1, 0).UTC()}, summaries["thread"])
}

func TestHistoryRemovalIncludesDelegations(t *testing.T) {
	t.Run("filesystem failure retry", func(t *testing.T) {
		store, root := newShadowTestStore(t)
		recordTestConversations(t, store, "other", map[string]ConversationKind{"parent": KindWebChat})
		require.NoError(t, store.SaveAttachment(t.Context(), "parent", "", &protocol.OutboundAttachment{ID: "blob", Name: "result", Data: []byte("bytes")}, false))
		_, err := store.db.ExecContext(t.Context(), `ALTER TABLE conversations ADD CONSTRAINT reject_deletion_summary CHECK (conversation_id <> 'parent' OR preview IS NOT NULL) NOT VALID`)
		require.NoError(t, err)
		_, err = store.DeleteSession(t.Context(), "parent")
		require.Error(t, err)

		var count int
		require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM attachments`).Scan(&count))
		require.Equal(t, 1, count, "SQL failure rolls ownership deletion back")
		require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pending_attachment_deletions`).Scan(&count))
		require.Zero(t, count, "tombstone insertion rolls back in the same transaction")
		_, err = store.db.ExecContext(t.Context(), `ALTER TABLE conversations DROP CONSTRAINT reject_deletion_summary`)
		require.NoError(t, err)

		storage := store.attachments.(filesystemAttachments)
		path, err := filepath.Rel(root.Name(), storage.path)
		require.NoError(t, err)
		// Temporarily move the storage directory to simulate an unavailable backing store.
		require.NoError(t, root.Rename(path, path+"-unavailable"))
		_, err = store.DeleteSession(t.Context(), "parent")
		require.ErrorIs(t, err, fs.ErrNotExist)
		require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM attachments`).Scan(&count))
		require.Zero(t, count, "SQL ownership deletion committed despite storage failure")
		pending, err := queryStrings(t.Context(), store.db, `SELECT id FROM pending_attachment_deletions`, "pending byte deletion")
		require.NoError(t, err)
		require.Equal(t, []string{"blob"}, pending, "durable ownership survives the failed deletion")
		require.NoError(t, root.Rename(path+"-unavailable", path))
		reopened, err := NewSessionService(root.Name())

		require.NoError(t, err)
		defer func() { require.NoError(t, reopened.Stop()) }()

		_, err = reopened.PruneStateBefore(t.Context(), time.Unix(0, 0), 0)
		require.NoError(t, err, "ordinary startup prune retries after a process restart")
		_, err = reopened.attachments.Get(t.Context(), "blob")
		require.ErrorIs(t, err, fs.ErrNotExist)
		require.NoError(t, reopened.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pending_attachment_deletions`).Scan(&count))
		require.Zero(t, count)
		// Simulate a crash after byte deletion but before the tombstone is removed.
		_, err = reopened.db.ExecContext(t.Context(), `INSERT INTO pending_attachment_deletions (id) VALUES ('blob')`)
		require.NoError(t, err)
		_, err = reopened.PruneStateBefore(t.Context(), time.Unix(0, 0), 0)
		require.NoError(t, err)
		require.NoError(t, reopened.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pending_attachment_deletions`).Scan(&count))
		require.Zero(t, count, "missing bytes are an idempotent deletion, not a failed retry")
	})
	t.Run("S3 original version", func(t *testing.T) {
		requests := make(chan string, 9)

		firstHead := make(chan struct{}, 1)
		firstHead <- struct{}{}

		firstDelete := make(chan struct{}, 1)
		firstDelete <- struct{}{}

		gone := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests <- r.Method

			if r.Method == http.MethodHead {
				select {
				case <-firstHead:
					w.WriteHeader(http.StatusForbidden)
					return
				default:
				}

				select {
				case <-gone:
					w.WriteHeader(http.StatusNotFound)
					return
				default:
				}

				w.Header().Set("X-Amz-Version-Id", "original-version")
			}

			if r.Method == http.MethodDelete {
				assert.Equal(t, "original-version", r.URL.Query().Get("versionId"), "erase original bytes instead of only creating a delete marker")

				select {
				case <-firstDelete:
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				default:
					close(gone)
				}
			}
		}))
		t.Cleanup(server.Close)
		store := newTestSessionService(t)
		store.attachments = s3Attachments{bucket: "originals", client: s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: new(server.URL), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1})}
		require.NoError(t, store.SaveAttachment(t.Context(), "parent", "", &protocol.OutboundAttachment{ID: "blob", Name: "result", Data: []byte("bytes")}, false))
		_, err := store.DeleteSession(t.Context(), "parent")
		require.Error(t, err)
		require.Equal(t, http.MethodPut, <-requests)
		require.Equal(t, http.MethodHead, <-requests)
		pending, err := queryStrings(t.Context(), store.db, `SELECT id FROM pending_attachment_deletions`, "failed S3 deletion")
		require.NoError(t, err)
		require.Equal(t, []string{"blob"}, pending, "403 is not proof that the bytes are absent")
		_, err = store.PruneStateBefore(t.Context(), time.Unix(0, 0), 0)
		require.Error(t, err)
		require.Equal(t, http.MethodHead, <-requests)
		require.Equal(t, http.MethodDelete, <-requests)
		pending, err = queryStrings(t.Context(), store.db, `SELECT id FROM pending_attachment_deletions`, "failed S3 version deletion")
		require.NoError(t, err)
		require.Equal(t, []string{"blob"}, pending, "a failed permanent deletion keeps ownership")
		_, err = store.PruneStateBefore(t.Context(), time.Unix(0, 0), 0)
		require.NoError(t, err)
		require.Equal(t, http.MethodHead, <-requests)
		require.Equal(t, http.MethodDelete, <-requests)
		_, err = store.db.ExecContext(t.Context(), `INSERT INTO pending_attachment_deletions (id) VALUES ('blob')`)
		require.NoError(t, err)
		_, err = store.PruneStateBefore(t.Context(), time.Unix(0, 0), 0)
		require.NoError(t, err, "a missing S3 key after permanent version deletion is a completed retry")
		require.Equal(t, http.MethodHead, <-requests)
		require.Empty(t, requests, "a 404 must not issue another marker-producing DELETE")
		pending, err = queryStrings(t.Context(), store.db, `SELECT id FROM pending_attachment_deletions`, "completed S3 deletion")
		require.NoError(t, err)
		require.Empty(t, pending)
	})

	cutoff := time.Unix(1_700_000_000, 0).UTC()

	for index, remove := range []func(*SessionService, string) error{
		func(s *SessionService, id string) error { _, err := s.DeleteSession(t.Context(), id); return err },
		func(s *SessionService, _ string) error {
			_, err := s.PruneStateBefore(t.Context(), cutoff, 0)
			return err
		},
	} {
		store, _ := newShadowTestStore(t)
		id := protocol.SlackThreadConversationID("D_1", slackTestTS(cutoff.Add(-time.Hour)))
		recordTestConversations(t, store, "main", map[string]ConversationKind{id: KindSlackThread})
		// A person's Web message, which a wipe removes as undo would.
		typed := testSessionEntryAt(cutoff.Add(-time.Hour), id)
		typed.ReplayInput[0] = json.RawMessage(`{"type":"message","role":"user","prompt_header":"[Web]","content":"typed"}`)
		_, err := store.AppendEntryID(t.Context(), id, typed)
		require.NoError(t, err)

		decoy := protocol.SlackThreadConversationID("DX1", slackTestTS(cutoff.Add(-time.Hour))) + "/call-1"
		unrelated := id + "/unrelated"
		require.NoError(t, store.UpsertThread(unrelated, ThreadState{Agent: "planner"}))
		_, err = store.AppendEntryID(t.Context(), unrelated, testSessionEntryAt(cutoff.Add(time.Hour), unrelated))
		require.NoError(t, err)
		children, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM conversations WHERE shadow_parent = $1 ORDER BY 1`, "owned children", id)
		require.NoError(t, err)

		histories := make([]string, 0, 3+2*len(children))
		histories = append(histories, id+"/call-1", id+"/call-1/call-2", decoy)

		require.NoError(t, store.startSubagent(t.Context(), id, harness.ChildKey{Path: "/call-1", Turn: "turn-1", Call: "call-1"}, "helper", true))
		require.NoError(t, store.startSubagent(t.Context(), id+"/call-1", harness.ChildKey{Path: "/call-2", Turn: "turn-2", Call: "call-2"}, "helper", true))

		histories = append(histories, children...)
		for _, child := range children {
			require.NoError(t, store.startSubagent(t.Context(), child, harness.ChildKey{Path: "/task", Turn: "turn-1", Call: "task"}, "helper", true))
			require.NoError(t, store.startSubagent(t.Context(), child+"/task", harness.ChildKey{Path: "/nested", Turn: "turn-1", Call: "nested"}, "helper", true))
			histories = append(histories, child+"/task/nested")
		}

		for _, history := range histories {
			_, err := store.AppendEntryID(t.Context(), history, testSessionEntryAt(cutoff.Add(time.Hour), history))
			require.NoError(t, err)
			require.NoError(t, store.SaveTurnStep(t.Context(), history, "old/step", json.RawMessage(`{}`)))
			require.NoError(t, store.SaveAttachment(t.Context(), history, "", &protocol.OutboundAttachment{ID: strings.ReplaceAll(history, "/", "_") + "-blob", Name: "result", MIMEType: "text/plain", Data: []byte("bytes")}, false))
		}

		require.NoError(t, remove(store, id))

		entries, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM entries WHERE conversation_id = ANY($1)`, "delegation entries", histories)
		require.NoError(t, err)
		require.Equal(t, []string{decoy}, entries)
		entries, err = queryStrings(t.Context(), store.db, `SELECT conversation_id FROM entries WHERE conversation_id = $1`, "unrelated slash session", unrelated)
		require.NoError(t, err)
		require.Equal(t, []string{unrelated}, entries)

		for _, history := range histories {
			_, err := store.attachments.Get(t.Context(), strings.ReplaceAll(history, "/", "_")+"-blob")
			if history == decoy {
				require.NoError(t, err)
				require.Equal(t, []string{"old/step"}, testTurnStepKeys(t, store, history))
			} else {
				require.Error(t, err, "owned original bytes must be removed")
				require.Empty(t, testTurnStepKeys(t, store, history))
			}
		}

		var bindings int
		require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM conversations WHERE shadow_parent = $1`, id).Scan(&bindings))

		if index == 0 {
			require.Equal(t, 2, bindings, "history-only clear preserves bindings")

			bridge := &Bridge{config: Config{ConversationID: id, Agent: "main", SessionService: store}, log: store.log}
			request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "future", true)}
			admitted, err := bridge.activateInbound(t.Context(), &request)
			require.NoError(t, err)
			require.True(t, admitted)

			var comparisons int
			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns WHERE parent_turn_id = $1`, request.turnID).Scan(&comparisons))
			require.Equal(t, 2, comparisons, "future parent work reaches the retained children")
		} else {
			require.Zero(t, bindings)

			for _, child := range children {
				_, found, err := store.Thread(child)
				require.NoError(t, err)
				require.False(t, found, "retention removes child managed rows")
			}
		}
	}
}

func TestDeleteSessionMissingIDReturnsZero(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	deleted, err := service.DeleteSession(context.Background(), "missing")
	require.NoError(t, err)
	assert.Zero(t, deleted)
}

func TestDeleteSessionMissingDBReturnsZero(t *testing.T) {
	t.Parallel()

	deleted, err := DeleteSession(context.Background(), t.TempDir(), "main")
	require.NoError(t, err)
	assert.Zero(t, deleted)
}

func TestDeleteSessionRejectsBlankConversationID(t *testing.T) {
	t.Parallel()

	_, err := DeleteSession(context.Background(), t.TempDir(), " ")
	require.ErrorContains(t, err, "conversation ID is required")
}

func TestSessionServicePersistsExternalMCPSessionMapping(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)
	recordTestConversations(t, store, "selected", map[string]ConversationKind{"slack-thread:C1:1.1": KindSlackThread, "slack-thread:C1:2.2": KindSlackThread})

	require.NoError(t, store.UpsertExternalMCPSession("ticket-123", &ExternalMCPSessionState{Agent: " cron ", PrivateConversationID: " external_mcp:cron:abc ", ManagedConversationID: " slack-thread:C1:1.1 ", SlackChannel: " #ops "}))

	session, ok, err := store.ExternalMCPSession("ticket-123")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ExternalMCPSessionState{Agent: "cron", PrivateConversationID: "external_mcp:cron:abc", ManagedConversationID: "slack-thread:C1:1.1", SlackChannel: "#ops", Thread: protocol.SlackReplyTarget{ChannelID: "C1", ThreadTS: "1.1"}}, session)

	require.NoError(t, store.UpsertExternalMCPSession("ticket-123", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: "external_mcp:planner:def", ManagedConversationID: "slack-thread:C1:2.2", SlackChannel: "#ops"}))

	session, ok, err = store.ExternalMCPSession("ticket-123")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ExternalMCPSessionState{Agent: "planner", PrivateConversationID: "external_mcp:planner:def", ManagedConversationID: "slack-thread:C1:2.2", SlackChannel: "#ops", Thread: protocol.SlackReplyTarget{ChannelID: "C1", ThreadTS: "2.2"}}, session)
	require.NoError(t, store.UpsertThread(session.ManagedConversationID, ThreadState{Agent: "selected"}))
	privateEntryID, err := store.AppendEntryID(t.Context(), session.PrivateConversationID, testSessionEntry("private", "private answer"))
	require.NoError(t, err)
	managedEntryID, err := store.AppendEntryID(t.Context(), session.ManagedConversationID, testSessionEntry("human", "human answer"))
	require.NoError(t, err)
	require.NoError(t, store.Stop())
	store = newTestSessionServiceAt(t, workspace)
	reopened, ok, err := store.ExternalMCPSession("ticket-123")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, session, reopened)
	thread, ok, err := store.Thread(session.ManagedConversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "selected", thread.Agent)
	listed, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM conversations WHERE external_id IS NOT NULL`, "bound conversation IDs")
	require.NoError(t, err)
	assert.Equal(t, []string{session.ManagedConversationID}, listed, "the binding left the thread it was on")

	for conversationID, entryID := range map[string]int64{session.PrivateConversationID: privateEntryID, session.ManagedConversationID: managedEntryID} {
		entries, err := store.ObserveEntries(t.Context(), conversationID)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, entryID, entries[0].ID)
	}

	for _, id := range []string{"optional-1", "optional-2", "duplicate-managed"} {
		require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "main"}))
	}

	for _, id := range []string{"optional-1", "optional-2"} {
		require.NoError(t, store.UpsertExternalMCPSession(id, &ExternalMCPSessionState{PrivateConversationID: " \t ", ManagedConversationID: id}))
		require.NoError(t, store.UpsertExternalMCPSession(id, &ExternalMCPSessionState{ManagedConversationID: id}))
	}

	var unmapped int
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM conversations WHERE external_id IS NOT NULL AND external_run IS NULL`).Scan(&unmapped))
	assert.Equal(t, 2, unmapped)
	require.Error(t, store.UpsertExternalMCPSession("duplicate-private", &ExternalMCPSessionState{PrivateConversationID: session.PrivateConversationID, ManagedConversationID: "duplicate-managed"}))
}

func TestSessionServiceRejectsBlankKeys(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)

	require.ErrorContains(t, store.UpsertThread(" ", ThreadState{Agent: "agent"}), "thread conversation ID is required")
	require.ErrorContains(t, store.UpsertExternalMCPSession(" ", &ExternalMCPSessionState{}), "external MCP conversation ID is required")
	require.EqualError(t, store.BeginGoal(" ", &GoalState{Objective: "obj", MaxTurns: 1}), "goal conversation ID is required")
	require.EqualError(t, store.BeginGoal("thread-1", &GoalState{Objective: " ", MaxTurns: 1}), "goal objective is required")
	require.NoError(t, store.UpsertThread("thread-1", ThreadState{Agent: "agent"}))
	require.NoError(t, store.BeginGoal("thread-1", &GoalState{Objective: "obj", MaxTurns: -1}))
	goal, ok, err := store.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 0, goal.MaxTurns)

	_, err = store.UpdateGoalStatus("thread-1", "nope", "")
	require.EqualError(t, err, `unsupported goal status "nope"`)
	_, err = store.ObserveEntries(t.Context(), " ")
	require.EqualError(t, err, "conversation ID is required")
}

func TestDeleteSessionEntriesReportsDeleteFailures(t *testing.T) {
	t.Parallel()

	_, err := deleteSessionEntries(context.Background(), errStore{errExec: errors.New("no delete")}, []string{"main"})
	require.ErrorContains(t, err, "lock session history")

	_, err = deleteSessionEntries(context.Background(), errStore{result: errResult{errRows: errors.New("no rows")}}, []string{"main"})
	require.ErrorContains(t, err, "count stale session entries")
}

func TestSessionServicePrunesOldState(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)
	cutoff := time.Unix(1_700_000_000, 0).UTC()
	oldTime := cutoff.Add(-time.Second)
	newTime := cutoff.Add(time.Second)

	oldThread := protocol.SlackThreadConversationID("DOLD", slackTestTS(oldTime))
	activeOldThread := protocol.SlackThreadConversationID("DACTIVE", slackTestTS(oldTime))
	newThread := protocol.SlackThreadConversationID("DNEW", slackTestTS(newTime))

	boundaryThread := protocol.SlackThreadConversationID("DBOUNDARY", slackTestTS(cutoff))
	recordTestConversations(t, store, "planner", map[string]ConversationKind{
		oldThread: KindSlackThread, activeOldThread: KindSlackThread, newThread: KindSlackThread, boundaryThread: KindSlackThread, "slack-thread:D123:not-a-time": KindSlackThread,
	})

	// The histories that had no record; any but a private one goes only with its family.
	orphanGoal := protocol.SlackThreadConversationID("DGOAL", slackTestTS(oldTime))
	recordPrivateHistories(t, store, map[string]ConversationKind{
		"external_mcp:cron:orphan": KindExternalMCPRun, "cron:daily:old": KindCronRun, "one-off-cron:daily:old": KindOneOffCronRun, "cron:daily:new": KindCronRun,
		"slack-thread:DORPHAN:1.000": KindSlackThread, "cron:daily:boundary": KindCronRun, orphanGoal: KindSlackThread,
	})
	_, err := store.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention) VALUES ('unmanaged-web', '', '', 'web_chat', FALSE, 'nested')`)
	require.NoError(t, err)

	require.NoError(t, store.UpsertThread("empty-recorded", ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, store.UpsertThread("empty-new", ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, store.UpsertThread("empty-running", ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, store.UpsertThread("continuing", ThreadState{Agent: "planner"}))
	_, err = store.db.ExecContext(t.Context(), `UPDATE conversations SET created_at = $1, continues = CASE WHEN conversation_id = 'continuing' THEN 'cron:daily:new' END
WHERE conversation_id IN ('empty-recorded', 'continuing')`, oldTime)
	require.NoError(t, err)
	seedActiveTurn(t, store, "empty-running", "running", &harness.SessionEntry{Version: 1, Type: "turn", TurnID: "running"})
	require.NoError(t, store.UpsertThread(activeOldThread, ThreadState{Agent: "selected", CreatedBy: ThreadCreatedByCron}))

	for conversationID, ts := range map[string]time.Time{
		oldThread:                      oldTime,
		activeOldThread:                newTime,
		"slack-thread:D123:not-a-time": oldTime,
		"external_mcp:cron:orphan":     oldTime,
		"cron:daily:old":               oldTime,
		"one-off-cron:daily:old":       oldTime,
		"cron:daily:new":               newTime,
		"slack-thread:DORPHAN:1.000":   oldTime,
		"cron:daily:boundary":          cutoff,
		"unmanaged-web":                oldTime,
	} {
		_, err := AppendSessionEntryID(context.Background(), workspace, conversationID, testSessionEntryAt(ts, conversationID))
		require.NoError(t, err)
	}

	seedActiveTurn(t, store, oldThread, "stale-turn", &harness.SessionEntry{Version: 1, Type: "turn", TurnID: "stale-turn"})

	require.NoError(t, store.BeginGoal(orphanGoal, &GoalState{Objective: "stale goal", MaxTurns: 1}))
	require.NoError(t, store.BeginGoal(activeOldThread, &GoalState{Objective: "keep", MaxTurns: 1}))
	listed, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM conversations WHERE retention = 'record' ORDER BY conversation_id`, "managed conversation IDs")
	require.NoError(t, err)
	assert.Contains(t, listed, activeOldThread)
	assert.NotContains(t, listed, "cron:daily:new")
	assert.NotContains(t, listed, "cron:daily:old")
	assert.NotContains(t, listed, "one-off-cron:daily:old")

	stats, err := store.PruneStateBefore(context.Background(), cutoff, 0)
	require.NoError(t, err)
	assert.Equal(t, PruneStateStats{Threads: 2, SessionRows: 5}, stats)

	_, ok, err := store.Goal(orphanGoal)
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = store.Goal(activeOldThread)
	require.NoError(t, err)
	assert.True(t, ok)

	threadIDs, err := queryStrings(context.Background(), store.db, `SELECT conversation_id FROM conversations ORDER BY conversation_id`, "managed conversation IDs")
	require.NoError(t, err)
	assert.NotContains(t, threadIDs, oldThread)
	assert.NotContains(t, threadIDs, "empty-recorded")
	assert.Contains(t, threadIDs, "empty-new", "an empty conversation newer than cutoff awaits its first turn")
	assert.Contains(t, threadIDs, "empty-running")
	assert.Contains(t, threadIDs, "continuing", "the run a chat continues is its history")

	thread, ok, err := store.Thread(activeOldThread)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ThreadState{Agent: "selected", CreatedBy: ThreadCreatedByCron}, thread)
	assert.Contains(t, threadIDs, activeOldThread)
	assert.Contains(t, threadIDs, newThread)
	assert.Contains(t, threadIDs, boundaryThread)
	assert.Contains(t, threadIDs, "slack-thread:D123:not-a-time")

	active, err := store.HasActiveTurn(t.Context(), oldThread)
	require.NoError(t, err)
	assert.False(t, active)
	assert.Empty(t, testTurnStepKeys(t, store, oldThread))

	for _, conversationID := range []string{oldThread, "external_mcp:cron:orphan", "cron:daily:old", "one-off-cron:daily:old", "slack-thread:DORPHAN:1.000"} {
		entries, err := store.ObserveEntries(context.Background(), conversationID)
		require.NoError(t, err)
		assert.Empty(t, entries, conversationID)
		assert.NotContains(t, threadIDs, conversationID)
	}

	for _, conversationID := range []string{activeOldThread, "slack-thread:D123:not-a-time", "cron:daily:new", "cron:daily:boundary", "unmanaged-web"} {
		entries, err := store.ObserveEntries(context.Background(), conversationID)
		require.NoError(t, err)
		assert.Len(t, entries, 1, conversationID)
	}
}

func TestSessionServiceRetainsQueuedConversations(t *testing.T) {
	cutoff := time.Unix(1_700_000_000, 0).UTC()
	store := newTestSessionServiceAt(t, t.TempDir())

	webID := "web-queued"
	require.NoError(t, store.UpsertThread(webID, ThreadState{Agent: "planner"}))
	webItem := protocol.ThreadQueueItem{ID: "web-q1", ConversationID: webID, Message: "first web message", Principal: "alice", Source: protocol.SourceWeb}
	require.NoError(t, store.PutThreadQueueItem(webItem.ID, &webItem))

	queuedSlack := protocol.SlackThreadConversationID("DQUEUE", slackTestTS(cutoff.Add(-time.Hour)))
	emptySlack := protocol.SlackThreadConversationID("DEMPTY", slackTestTS(cutoff.Add(-time.Hour)))

	recordTestConversations(t, store, "planner", map[string]ConversationKind{queuedSlack: KindSlackThread, emptySlack: KindSlackThread})
	_, err := store.AppendEntryID(t.Context(), queuedSlack, testSessionEntryAt(cutoff.Add(-time.Hour), queuedSlack))
	require.NoError(t, err)
	_, err = store.AppendEntryID(t.Context(), emptySlack, testSessionEntryAt(cutoff.Add(-time.Hour), emptySlack))
	require.NoError(t, err)

	slackItem := protocol.ThreadQueueItem{ID: "slack-q1", ConversationID: queuedSlack, Message: "waiting slack", Principal: "U1"}
	require.NoError(t, store.PutThreadQueueItem(slackItem.ID, &slackItem))

	managedID := protocol.SlackThreadConversationID("C1", slackTestTS(cutoff.Add(-time.Hour)))
	privateID := "external_mcp:planner:private"
	require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Thread: protocol.SlackReplyTarget{ChannelID: "C1", ThreadTS: slackTestTS(cutoff.Add(-time.Hour))}, Agent: "planner", PrivateConversationID: privateID, ManagedConversationID: managedID, SlackChannel: "#ops"}))
	require.NoError(t, store.UpsertThread(privateID, ThreadState{Agent: "planner"}))

	for _, conversationID := range []string{privateID, managedID} {
		_, err := store.AppendEntryID(t.Context(), conversationID, testSessionEntryAt(cutoff.Add(-time.Hour), conversationID))
		require.NoError(t, err)
	}

	privateItem := protocol.ThreadQueueItem{ID: "private-q1", ConversationID: privateID, Message: "private follow-up", Principal: "mcp"}
	require.NoError(t, store.PutThreadQueueItem(privateItem.ID, &privateItem))

	orphanID := "external_mcp:planner:queued-orphan"
	recordPrivateHistories(t, store, map[string]ConversationKind{orphanID: KindExternalMCPRun})
	_, err = store.AppendEntryID(t.Context(), orphanID, testSessionEntryAt(cutoff.Add(-time.Hour), orphanID))
	require.NoError(t, err)

	for _, id := range []string{"orphan-q1", "orphan-q2"} {
		item := protocol.ThreadQueueItem{ID: id, ConversationID: orphanID, Message: "orphan follow-up", Principal: "mcp"}
		require.NoError(t, store.PutThreadQueueItem(id, &item))
	}

	_, err = store.PruneStateBefore(t.Context(), cutoff, 0)
	require.NoError(t, err)

	thread, ok, err := store.Thread(webID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "planner", thread.Agent)

	queue, err := store.ThreadQueueForConversation(webID)
	require.NoError(t, err)
	require.Len(t, queue, 1)
	assert.Equal(t, webItem.ID, queue[0].ID)
	assert.Equal(t, webItem.Message, queue[0].Message)

	_, ok, err = store.Thread(queuedSlack)
	require.NoError(t, err)
	assert.True(t, ok)
	_, ok, err = store.Thread(emptySlack)
	require.NoError(t, err)
	assert.False(t, ok)

	_, ok, err = store.ExternalMCPSession("public-1")
	require.NoError(t, err)
	assert.True(t, ok)
	_, ok, err = store.Thread(managedID)
	require.NoError(t, err)
	assert.True(t, ok)
	_, ok, err = store.Thread(privateID)
	require.NoError(t, err)
	assert.True(t, ok)
	entries, err := store.ObserveEntries(t.Context(), orphanID)
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	for _, id := range []string{"orphan-q1", "orphan-q2"} {
		require.NoError(t, store.DeleteThreadQueueItem(id))
	}

	require.NoError(t, store.DeleteThreadQueueItem(privateItem.ID))
	_, err = store.PruneStateBefore(t.Context(), cutoff, 0)
	require.NoError(t, err)
	_, ok, err = store.ExternalMCPSession("public-1")
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = store.Thread(managedID)
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = store.Thread(privateID)
	require.NoError(t, err)
	assert.False(t, ok)
	entries, err = store.ObserveEntries(t.Context(), orphanID)
	require.NoError(t, err)
	assert.Empty(t, entries)

	for _, tc := range []struct {
		name, conversation, parent, destination string
		orphan                                  bool
	}{
		{"scheduled", emptySlack, "", "", false},
		{"scheduled orphan", "external_mcp:scheduled", "", "", true},
		{"scheduled shadow", "scheduled-child", "scheduled-parent", "", false},
		{"scheduled producer", "scheduled-producer", "", "scheduled-owner", false},
		{"scheduled producer shadow", "scheduled-child", "scheduled-producer", "scheduled-owner", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestSessionService(t)
			ctx := t.Context()

			for _, id := range []string{tc.conversation, tc.parent, tc.destination} {
				switch {
				case id == "":
				case tc.orphan && id == tc.conversation:
					recordPrivateHistories(t, store, map[string]ConversationKind{id: KindExternalMCPRun})
				case id == emptySlack:
					recordTestConversations(t, store, "planner", map[string]ConversationKind{id: KindSlackThread})
				default:
					require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "planner"}))
				}
			}

			if tc.orphan || tc.conversation == emptySlack {
				_, err := store.AppendEntryID(ctx, tc.conversation, testSessionEntryAt(cutoff.Add(-time.Hour), tc.conversation))
				require.NoError(t, err)
			}

			producer := tc.conversation
			if tc.parent != "" {
				_, err := store.db.ExecContext(ctx, `UPDATE conversations SET shadow_parent = $2, parent_agent = 'planner', shadow_agent = 'shadow' WHERE conversation_id = $1`, tc.conversation, tc.parent)
				require.NoError(t, err)

				producer = tc.parent
			}

			if tc.destination != "" {
				_, err := store.db.ExecContext(ctx, `UPDATE conversations SET producer_inbound_json = json_build_object('SyncDestination', $2::text), reports_to = $2 WHERE conversation_id = $1`, producer, tc.destination)
				require.NoError(t, err)
			}

			scheduled := protocol.ScheduledMessageState{ConversationID: tc.conversation, Agent: "planner", Message: "pending follow-up", DueAt: cutoff.Add(5040 * time.Hour)}
			require.NoError(t, store.PutScheduledMessage("pending", &scheduled))
			_, err := store.PruneStateBefore(ctx, cutoff, 0)
			require.NoError(t, err)
			messages, err := store.ScheduledMessagesForConversation(tc.conversation)
			require.NoError(t, err)
			assert.Equal(t, map[string]protocol.ScheduledMessageState{"pending": scheduled}, messages)

			for _, id := range []string{tc.conversation, tc.parent, tc.destination} {
				if id != "" && (!tc.orphan || id != tc.conversation) {
					_, ok, err := store.Thread(id)
					require.NoError(t, err)
					assert.True(t, ok, id)
				}
			}

			require.NoError(t, store.ResetScheduledMessages(tc.conversation))
			_, err = store.db.ExecContext(ctx, `UPDATE conversations SET created_at = $1`, cutoff.Add(-time.Hour))
			require.NoError(t, err)
			_, err = store.PruneStateBefore(ctx, cutoff, 0)
			require.NoError(t, err)

			for _, id := range []string{tc.conversation, tc.parent, tc.destination} {
				if id != "" {
					_, ok, err := store.Thread(id)
					require.NoError(t, err)
					assert.False(t, ok, id)
				}
			}

			entries, err := store.ObserveEntries(ctx, tc.conversation)
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}

	t.Run("pending producer effect", func(t *testing.T) {
		store := newTestSessionService(t)

		ctx := t.Context()
		recordTestConversations(t, store, "planner", map[string]ConversationKind{managedID: KindSlackThread, "scheduled-producer": KindWebChat, "scheduled-child": KindWebChat})

		_, err := store.db.ExecContext(ctx, `UPDATE conversations SET shadow_parent = 'scheduled-producer', parent_agent = 'planner', shadow_agent = 'shadow' WHERE conversation_id = 'scheduled-child'`)
		require.NoError(t, err)
		_, err = store.db.ExecContext(ctx, `UPDATE conversations SET producer_inbound_json = json_build_object('SyncDestination', $1::text), reports_to = $1 WHERE conversation_id = 'scheduled-producer'`, managedID)
		require.NoError(t, err)

		manager, _ := newTestBridgeManager(t, config.NewLockedConfig(&config.Config{Workspace: t.TempDir()}), store, make(chan *protocol.OutboundMessage, 1))
		bridge, err := manager.recordedBridge(ctx, "scheduled-producer")
		require.NoError(t, err)

		origin := &protocol.InboundMessage{SyncDestination: managedID}
		require.NoError(t, bridge.ScheduleMessage(origin, 5040*time.Hour, "pending follow-up", false))

		_, err = store.PruneStateBefore(ctx, cutoff, 0)
		require.NoError(t, err)
		_, ok, err := store.Thread(managedID)
		require.NoError(t, err)
		require.True(t, ok)

		scheduled, err := store.ScheduledMessagesForConversation(managedID)
		require.NoError(t, err)
		require.Len(t, scheduled, 1, "with no turn running, the run's schedule takes effect where it reports")
		require.NoError(t, bridge.ResetScheduledMessages(origin))

		_, err = store.PruneStateBefore(ctx, cutoff, 0)
		require.NoError(t, err)
		_, ok, err = store.Thread(managedID)
		require.NoError(t, err)
		assert.False(t, ok)

		scheduled, err = store.ScheduledMessagesForConversation(managedID)
		require.NoError(t, err)
		assert.Empty(t, scheduled)
	})
}

func TestSessionServicePruneBacksOffFromBusyWorkers(t *testing.T) {
	store := newTestSessionService(t)
	cutoff := time.Unix(1_700_000_000, 0).UTC()
	id := protocol.SlackThreadConversationID("DBUSY", slackTestTS(cutoff.Add(-time.Hour)))
	recordTestConversations(t, store, "planner", map[string]ConversationKind{id: KindSlackThread})
	_, err := store.AppendEntryID(t.Context(), id, testSessionEntryAt(cutoff.Add(-time.Hour), id))
	require.NoError(t, err)

	worker := &Bridge{handling: true, log: store.log, config: Config{ConversationID: id, SessionService: store}}
	store.historyWorkers = &threadBridgeManager{store: store, bridges: map[string]directBridge{id: worker}}

	stats, err := store.PruneStateBefore(t.Context(), cutoff, 0)
	require.NoError(t, err)
	assert.Zero(t, stats)

	_, ok, err := store.Thread(id)
	require.NoError(t, err)
	require.True(t, ok, "a stale conversation with a running worker waits for a later pass")
	assert.False(t, worker.historyMutation, "backing off never pauses the worker")

	worker.handling = false
	stats, err = store.PruneStateBefore(t.Context(), cutoff, 0)
	require.NoError(t, err)
	assert.Equal(t, PruneStateStats{Threads: 1, SessionRows: 1}, stats)

	_, ok, err = store.Thread(id)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSessionServicePrunesStaleExternalConversationWithActiveTurn(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)
	cutoff := time.Unix(1_700_000_000, 0).UTC()
	managedConversationID := protocol.SlackThreadConversationID("C1", slackTestTS(cutoff.Add(-time.Hour)))
	privateConversationID := "external_mcp:planner:private"
	require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Thread: protocol.SlackReplyTarget{ChannelID: "C1", ThreadTS: slackTestTS(cutoff.Add(-time.Hour))}, Agent: "planner", PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID, SlackChannel: "#ops"}))

	for _, conversationID := range []string{privateConversationID, managedConversationID} {
		_, err := store.AppendEntryID(t.Context(), conversationID, testSessionEntryAt(cutoff.Add(-time.Hour), conversationID))
		require.NoError(t, err)
	}

	seedActiveTurn(t, store, privateConversationID, "active-mcp", &harness.SessionEntry{Version: 1, Type: "turn", TurnID: "active-mcp"})

	stats, err := store.PruneStateBefore(t.Context(), cutoff, 0)
	require.NoError(t, err)
	assert.Equal(t, PruneStateStats{Threads: 2, ExternalMCPSessions: 1, SessionRows: 2}, stats)

	_, ok, err := store.ExternalMCPSession("public-1")
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = store.Thread(managedConversationID)
	require.NoError(t, err)
	assert.False(t, ok)
	active, err := store.HasActiveTurn(t.Context(), privateConversationID)
	require.NoError(t, err)
	assert.False(t, active)
	assert.Empty(t, testTurnStepKeys(t, store, privateConversationID))
}

func TestSessionServicePrunesExternalConversationOnlyWhenAllHistoriesAreStale(t *testing.T) {
	cutoff := time.Unix(1_700_000_000, 0).UTC()

	for _, tt := range []struct {
		name                                           string
		paired, privateFresh, managedFresh, wantPruned bool
		privateEmpty, managedEmpty                     bool
	}{
		{name: "paired both stale", paired: true, wantPruned: true},
		{name: "paired private fresh", paired: true, privateFresh: true},
		{name: "paired managed fresh", paired: true, managedFresh: true},
		{name: "paired both fresh", paired: true, privateFresh: true, managedFresh: true},
		{name: "paired private empty", paired: true, privateEmpty: true, managedFresh: true},
		{name: "paired managed empty", paired: true, managedEmpty: true, privateFresh: true},
		{name: "paired both empty", paired: true, privateEmpty: true, managedEmpty: true, wantPruned: true},
		{name: "legacy stale", wantPruned: true},
		{name: "legacy fresh", managedFresh: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestSessionServiceAt(t, t.TempDir())
			managedConversationID := protocol.SlackThreadConversationID("C1", slackTestTS(cutoff.Add(-time.Hour)))

			privateConversationID := ""
			if tt.paired {
				privateConversationID = "external_mcp:planner:private"
				require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Thread: protocol.SlackReplyTarget{ChannelID: "C1", ThreadTS: slackTestTS(cutoff.Add(-time.Hour))}, Agent: "planner", PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID, SlackChannel: "#ops"}))
			} else {
				recordTestConversations(t, store, "main", map[string]ConversationKind{managedConversationID: KindSlackThread})
				require.NoError(t, store.UpsertExternalMCPSession("public-1", &ExternalMCPSessionState{Agent: "planner", ManagedConversationID: managedConversationID, SlackChannel: "#ops"}))
			}

			if privateConversationID != "" && !tt.privateEmpty {
				_, err := store.AppendEntryID(t.Context(), privateConversationID, testSessionEntryAt(pruneTestTime(cutoff, tt.privateFresh), "private"))
				require.NoError(t, err)
			}

			if !tt.managedEmpty {
				_, err := store.AppendEntryID(t.Context(), managedConversationID, testSessionEntryAt(pruneTestTime(cutoff, tt.managedFresh), "managed"))
				require.NoError(t, err)
			}

			_, err := store.PruneStateBefore(t.Context(), cutoff, 0)
			require.NoError(t, err)
			_, ok, err := store.ExternalMCPSession("public-1")
			require.NoError(t, err)
			assert.Equal(t, !tt.wantPruned, ok)

			for _, conversationID := range []string{managedConversationID, privateConversationID} {
				if conversationID == "" {
					continue
				}

				_, ok, err := store.Thread(conversationID)
				require.NoError(t, err)
				assert.Equal(t, !tt.wantPruned, ok, conversationID)
			}
		})
	}
}

// A finished cron run past the cutoff loses the context only it replays. The run, the entries its
// chat shows, and their attachments stay, and the chat still shows every message. A run with newer
// entries or pending work keeps its context.
func TestPruneStateDropsFinishedCronRunContext(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	cutoff := time.Unix(1_700_000_000, 0).UTC()

	recordTestConversations(t, store, "main", map[string]ConversationKind{"chat": KindWebChat, "finished": KindCronRun, "fresh": KindCronRun, "pending": KindCronRun})
	_, err := store.db.ExecContext(ctx, `UPDATE conversations SET reports_to = 'chat' WHERE kind = 'cron_run'`)
	require.NoError(t, err)

	for run, shown := range map[string]time.Time{"finished": cutoff.Add(-time.Hour), "fresh": cutoff.Add(time.Hour), "pending": cutoff.Add(-time.Hour)} {
		sandbox, err := store.AppendEntryID(ctx, run, testSessionEntryAt(cutoff.Add(-2*time.Hour), run+" context"))
		require.NoError(t, err)
		_, err = store.db.ExecContext(ctx, `UPDATE entries SET audience = 'sandbox' WHERE id = $1`, sandbox)
		require.NoError(t, err)
		_, err = store.AppendEntryID(ctx, run, testSessionEntryAt(shown, run+" report"))
		require.NoError(t, err)
		require.NoError(t, store.SaveAttachment(ctx, run, "", &protocol.OutboundAttachment{ID: run + "-file", Name: "report", Data: []byte(run)}, false))
	}

	require.NoError(t, store.PutScheduledMessage("later", &protocol.ScheduledMessageState{ConversationID: "pending", Agent: "main", Message: "later", DueAt: cutoff.Add(time.Hour)}))

	shown, err := store.ObserveEntries(ctx, "chat")
	require.NoError(t, err)
	require.Len(t, shown, 3)

	stats, err := store.PruneStateBefore(ctx, cutoff, 0)
	require.NoError(t, err)
	assert.Equal(t, PruneStateStats{SessionRows: 1}, stats)

	after, err := store.ObserveEntries(ctx, "chat")
	require.NoError(t, err)
	require.Equal(t, shown, after, "the chat still shows every message")

	audiences, err := queryStrings(ctx, store.db, `SELECT conversation_id || ' ' || audience FROM entries ORDER BY id`, "kept entries")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"finished both", "fresh sandbox", "fresh both", "pending sandbox", "pending both"}, audiences)

	for _, run := range []string{"finished", "fresh", "pending"} {
		_, isRun, err := store.CronRun(ctx, run)
		require.NoError(t, err)
		require.True(t, isRun, run)
		attachment, err := store.LoadAttachment(ctx, run, run+"-file", false)
		require.NoError(t, err)
		require.Equal(t, []byte(run), attachment.Data)
	}
}

func pruneTestTime(cutoff time.Time, fresh bool) time.Time {
	if fresh {
		return cutoff.Add(time.Second)
	}

	return cutoff.Add(-time.Second)
}

// storedEntries reads conversationID's saved history the way runTurn does.
func storedEntries(t *testing.T, service *SessionService, conversationID string) iter.Seq2[harness.SessionEntry, error] {
	t.Helper()

	observed, err := service.ObserveEntries(t.Context(), conversationID)
	require.NoError(t, err)

	var memory memoryStore
	for i := range observed {
		memory.entries = append(memory.entries, observed[i].Entry)
	}

	return memory.in()
}

func collectEntries(t *testing.T, seq iter.Seq2[harness.SessionEntry, error]) []harness.SessionEntry {
	t.Helper()

	return slices.Collect(func(yield func(harness.SessionEntry) bool) {
		for entry, err := range seq {
			require.NoError(t, err)

			if !yield(entry) {
				return
			}
		}
	})
}

type errStore struct {
	result  sql.Result
	errExec error
}

func (s errStore) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	if s.errExec != nil {
		return nil, s.errExec
	}

	return s.result, nil
}

func (errStore) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, nil
}

func (s errStore) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	db, err := sql.Open("pgx", "postgres://127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		panic(err)
	}

	return db.QueryRowContext(ctx, query, args...)
}

type errResult struct {
	errID   error
	errRows error
}

func (r errResult) LastInsertId() (int64, error) {
	return 0, r.errID
}

func (r errResult) RowsAffected() (int64, error) {
	return 0, r.errRows
}

func testTurnStepKeys(t *testing.T, service *SessionService, conversationID string) []string {
	t.Helper()

	keys, err := queryStrings(t.Context(), service.db, `SELECT key FROM steps WHERE conversation_id = $1 ORDER BY key`, "turn step keys", conversationID)
	require.NoError(t, err)

	return keys
}

// seedActiveTurn records a running row and, when record is set, its root journal step.
func seedActiveTurn(t *testing.T, service *SessionService, conversationID, turnID string, record *harness.SessionEntry) {
	t.Helper()

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "seed", true)
	inbound.ConversationID = conversationID
	require.NoError(t, startTurnDB(t.Context(), service.db, turnID, conversationID, inbound))

	if record != nil {
		require.NoError(t, saveTestTurn(t.Context(), service, conversationID, turnID, nil, record))
	}
}

// endTestTurn finishes a seeded row as $stop or a failure does and closes it after delivery.
func endTestTurn(t *testing.T, service *SessionService, conversationID, turnID string, terminal protocol.Terminal) {
	t.Helper()

	_, err := service.finishTurn(t.Context(), turnID, &turnFinish{store: newSessionStore(conversationID, service), outbound: protocol.NewOutboundMessage(conversationID, ""), terminal: terminal})
	require.NoError(t, err)
	require.NoError(t, service.closeTurn(t.Context(), turnID))
}

func newTestSessionService(t *testing.T) *SessionService {
	t.Helper()

	return newTestSessionServiceAt(t, t.TempDir())
}

func newTestSessionServiceAt(t *testing.T, workspace string) *SessionService {
	t.Helper()

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	return service
}

func testSessionEntry(user, assistant string) *harness.SessionEntry {
	return &harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), ResponseID: "", Model: "gpt-6.1-sol", ReplayInput: testReplayInput(replayInputMessage{role: "user", text: user}, replayInputMessage{role: "assistant", text: assistant})}
}

func testSessionEntryAt(ts time.Time, user string) *harness.SessionEntry {
	entry := testSessionEntry(user, "assistant")
	entry.Timestamp = ts.UTC()

	return entry
}

// recordTestConversations records each conversation as the code creating its kind does, with a
// Slack thread's channel and thread.
func recordTestConversations(t *testing.T, s *SessionService, agent string, kinds map[string]ConversationKind) {
	t.Helper()

	for id, kind := range kinds {
		facts := conversationFacts{kind: kind}
		if kind == KindSlackThread {
			facts.slackChannel, facts.slackThread = testSlackThread(id)
		}

		require.NoError(t, s.recordConversation(t.Context(), protocol.Conversation{ID: id, Agent: agent}, facts))
	}
}

// recordLegacyDelegations records each history saved under a call ID of parent, as 051 records one
// saved before delegations had their own key: started by that call, in no recorded turn.
func recordLegacyDelegations(t *testing.T, s *SessionService, parent string, calls ...string) {
	t.Helper()

	for _, call := range calls {
		_, err := s.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, parent_conversation_id, kind, listed, retention, call_id)
VALUES ($1 || '/' || $2, '', '', $1, $3, FALSE, 'nested', $2)`, parent, call, KindSubagent)
		require.NoError(t, err)
	}
}

// recordPrivateHistories records each conversation as 051 records a private history that had no
// managed record: hidden, expiring by age while unreferenced.
func recordPrivateHistories(t *testing.T, s *SessionService, kinds map[string]ConversationKind) {
	t.Helper()

	for id, kind := range kinds {
		channel, thread := testSlackThread(id)
		_, err := s.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, slack_channel, slack_thread)
VALUES ($1, '', '', $2, FALSE, 'private', NULLIF($3, ''), NULLIF($4, ''))`, id, kind, channel, thread)
		require.NoError(t, err)
	}
}

// testSlackThread returns the channel and thread a test names in a slack-thread: ID, as the code
// creating that Slack thread knows them; empty for any other ID.
func testSlackThread(id string) (channel, thread string) {
	if rest, ok := strings.CutPrefix(id, "slack-thread:"); ok {
		channel, thread, _ = strings.Cut(rest, ":")
	}

	return channel, thread
}

func slackTestTS(ts time.Time) string {
	ts = ts.UTC()
	return fmt.Sprintf("%d.%06d", ts.Unix(), ts.Nanosecond()/1_000)
}

func testReplayInput(messages ...replayInputMessage) []json.RawMessage {
	var replayInput []json.RawMessage

	for i := range messages {
		raw := replayInputForMessage(messages[i].role, messages[i].text)

		replayInput = append(replayInput, raw...)
	}

	return replayInput
}
func TestCopiedAttributionSurvivesSourceRetention(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	entry := harness.SessionEntry{Version: 1, Type: "turn", Agent: "planner", Model: "work/model-a", ReasoningEffort: new("high"), ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"answer"}`)}}
	producerID, err := store.AppendEntryID(t.Context(), "producer-x", &entry)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT 'interactive-y', (entry_json::jsonb || jsonb_build_object('sync_source_entry_id', id, 'sync_source_conversation_id', 'producer-x'))::json, entry_timestamp FROM entries WHERE id = $1`, producerID)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `DELETE FROM entries WHERE conversation_id=$1`, "producer-x")
	require.NoError(t, err)
	entries, err := store.ObserveEntries(t.Context(), "interactive-y")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "producer-x", entries[0].SourceConversationID)
	require.Equal(t, entry.AttributionAt(0), entries[0].Entry.AttributionAt(0))
	require.JSONEq(t, string(entry.ReplayInput[0]), string(entries[0].Entry.ReplayInput[0]))
}

func TestVoiceSettings(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()

	create := func(id string) {
		t.Helper()
		require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(ctx, protocol.Conversation{ID: id, Agent: "main"}))
	}
	create("voice-a")
	create("voice-b")

	settings := func(username string) VoiceSettings {
		t.Helper()

		current, err := store.VoiceSettings(ctx, username)
		require.NoError(t, err)

		return current
	}
	recorded := func(id string) bool {
		t.Helper()

		_, ok, err := store.Thread(id)
		require.NoError(t, err)

		return ok
	}
	prune := func() {
		t.Helper()

		// The cutoff is an hour ahead: conversations carry the database's clock,
		// which may run ahead of this process's.
		_, err := store.PruneStateBefore(ctx, time.Now().Add(time.Hour), 0)
		require.NoError(t, err)
	}

	assert.Equal(t, VoiceSettings{}, settings("alice"), "a person with no row gets the defaults")

	require.NoError(t, store.StartVoiceConversation(ctx, "alice", "voice-a", "main"))
	require.NoError(t, store.StartVoiceConversation(ctx, "alice", "voice-b", "other"))

	voice, on := "cedar", true
	require.NoError(t, store.UpdateVoiceSettings(ctx, "alice", nil, &voice, &on))
	require.NoError(t, store.UpdateVoiceSettings(ctx, "bob", nil, nil, &on))
	assert.Equal(t, VoiceSettings{ConversationID: "voice-a", Agent: "main", Voice: "cedar", Transcript: true}, settings("alice"), "the first start wins; an update keeps the fields it was not given")
	assert.Equal(t, VoiceSettings{Transcript: true}, settings("bob"))

	// Startup cleanup deleted an empty current voice conversation on 2026-10-13.
	prune()
	assert.True(t, recorded("voice-a"), "an empty current voice conversation survives startup cleanup")
	assert.False(t, recorded("voice-b"), "an empty conversation that is nobody's is still deleted")

	require.NoError(t, store.ResetVoiceConversation(ctx, "alice", "voice-b"))
	assert.Equal(t, "voice-a", settings("alice").ConversationID, "a stale reset leaves the newer conversation current")

	require.NoError(t, store.ResetVoiceConversation(ctx, "alice", "voice-a"))
	assert.Equal(t, VoiceSettings{Agent: "main", Voice: "cedar", Transcript: true}, settings("alice"))
	prune()
	assert.False(t, recorded("voice-a"), "after Reset, ordinary cleanup applies")

	create("voice-c")
	require.NoError(t, store.StartVoiceConversation(ctx, "alice", "voice-c", "main"))
	_, err := store.db.ExecContext(ctx, `DELETE FROM conversations WHERE conversation_id = 'voice-c'`)
	require.NoError(t, err)
	assert.Equal(t, VoiceSettings{Agent: "main", Voice: "cedar", Transcript: true}, settings("alice"), "a deleted conversation clears only the pointer")
	assert.Equal(t, VoiceSettings{Transcript: true}, settings("bob"))
}

func TestWebSpaces(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()

	spaces := func() []Space {
		t.Helper()

		list, err := store.Spaces(ctx)
		require.NoError(t, err)

		return list
	}
	birth := func(id, space string) string {
		t.Helper()
		require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(ctx, protocol.Conversation{ID: id, Agent: "main", Space: space}))

		var spaceID string
		require.NoError(t, store.db.QueryRowContext(ctx, `SELECT COALESCE(space_id, '') FROM conversations WHERE conversation_id = $1`, id).Scan(&spaceID))

		return spaceID
	}

	assert.Equal(t, []Space{{ID: "main", Name: "Main"}}, spaces())

	require.NoError(t, store.CreateSpace(ctx, "Ops"))
	require.ErrorIs(t, store.CreateSpace(ctx, "ops"), ErrSpaceRejected, "names are unique ignoring case")

	ops := spaces()[1]
	require.NoError(t, store.RenameSpace(ctx, ops.ID, "Operations"))
	require.NoError(t, store.RenameSpace(ctx, ops.ID, "operations"), "a space may change its own name's case")
	require.ErrorIs(t, store.RenameSpace(ctx, ops.ID, "MAIN"), ErrSpaceRejected)
	assert.Equal(t, []Space{{ID: "main", Name: "Main"}, {ID: ops.ID, Name: "operations"}}, spaces(), "renames keep the id; the list is oldest first")

	banana := birth("banana-1", "Banana")
	assert.Equal(t, []Space{{ID: "main", Name: "Main"}, {ID: ops.ID, Name: "operations"}, {ID: banana, Name: "Banana"}}, spaces(), "a missing birth space is created")
	assert.Equal(t, banana, birth("banana-2", "banana"), "an existing space is reused ignoring case")
	assert.Equal(t, ops.ID, birth("ops-1", "Operations"))
	assert.Empty(t, birth("none", ""), "no name, no birth space")

	require.NoError(t, store.DeleteSpace(ctx, banana))
	assert.Empty(t, birth("banana-1", "Banana"), "deleting a space clears the birth space of sessions born there; an existing session never recreates it")

	// Deleting the last space waits for a delete of the only other space in flight, then refuses: the last space stays.
	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `DELETE FROM web_spaces WHERE id = $1`, ops.ID)
	require.NoError(t, err)

	deleted := make(chan error, 1)
	go func() { deleted <- store.DeleteSpace(ctx, "main") }()

	assert.Eventually(t, func() bool {
		var waiting bool

		_ = store.db.QueryRowContext(ctx, `SELECT count(*) > 0 FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE '%web_spaces%'`).Scan(&waiting)

		return waiting || len(deleted) > 0
	}, 5*time.Second, time.Millisecond)
	require.NoError(t, tx.Commit())
	require.ErrorIs(t, <-deleted, ErrSpaceRejected)
	assert.Equal(t, []Space{{ID: "main", Name: "Main"}}, spaces())

	state, revision, err := store.TabState(ctx, "alice")
	require.NoError(t, err)
	assert.Nil(t, state)
	assert.Zero(t, revision)

	save := func(state string, revision int64) bool {
		t.Helper()

		saved, err := store.SaveTabState(ctx, "alice", []byte(state), revision)
		require.NoError(t, err)

		return saved
	}
	assert.True(t, save("first", 0))
	assert.False(t, save("stale", 0), "a save made from an older version is refused")
	assert.True(t, save("second", 1))

	state, revision, err = store.TabState(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, []byte("second"), state)
	assert.Equal(t, int64(2), revision)

	state, revision, err = store.TabState(ctx, "bob")
	require.NoError(t, err)
	assert.Nil(t, state, "each person has their own state")
	assert.Zero(t, revision)
}

// Each conversation-creating path stores what it creates and whether Web lists it, and Web lists,
// and lets people undo in, exactly the conversations it did when it read their IDs.
func TestConversationKindsAtCreation(t *testing.T) {
	t.Parallel()

	store, _ := newShadowTestStore(t)
	ctx := t.Context()
	runtime := &Runtime{Sessions: store}
	run, oneOff := "cron:cron/daily.md:20261019T120000.000000001Z:RUN", "one-off-cron:cron/once.md:20261019T130000.000000001Z:ONE"
	slack := protocol.SlackThreadConversationID("C1", "1.1")

	require.NoError(t, runtime.CreateConversation(ctx, protocol.Conversation{ID: "web", Agent: "main", CreatedBy: "alice"}))
	require.NoError(t, runtime.CreateConversation(ctx, protocol.Conversation{ID: run, Agent: "job", CreatedBy: "cron", Run: &protocol.CronRun{Path: "cron/daily.md"}}))
	require.NoError(t, runtime.CreateConversation(ctx, protocol.Conversation{ID: oneOff, Agent: "job", CreatedBy: "cron", Run: &protocol.CronRun{Path: "cron/once.md", OneOff: true}}))
	require.NoError(t, runtime.CreateConversation(ctx, protocol.Conversation{ID: "web:" + run, Agent: "main", CreatedBy: "alice"}))
	require.NoError(t, runtime.ContinueRun(ctx, run, "web:"+run))
	require.NoError(t, store.RegisterExternalMCPConversation("ticket", "main", &ExternalMCPSessionState{Thread: protocol.SlackReplyTarget{ChannelID: "C2", ThreadTS: "2.2"}, Agent: "job", PrivateConversationID: "private", ManagedConversationID: protocol.SlackThreadConversationID("C2", "2.2")}))
	// A delegation's guardrail saves its review where the subagent session it approves then starts.
	require.NoError(t, (&Bridge{config: Config{ConversationID: "web", SessionService: store}}).AppendChildEntry(ctx, harness.ChildKey{Path: "/call-1", Turn: "turn-1", Call: "call-1"}, testSessionEntry("guard", "approved")))
	require.NoError(t, store.startSubagent(ctx, "web", harness.ChildKey{Path: "/call-1", Turn: "turn-1", Call: "call-1"}, "helper", true))

	for _, parent := range []string{"web", run} {
		require.NoError(t, (&Bridge{config: Config{ConversationID: parent, SessionService: store}}).AppendChildEntry(ctx, harness.ChildKey{Path: "/call-2-review-0a1b2c3d", Turn: "turn-2", Call: "call-2"}, testSessionEntry("review", "allowed")))
	}

	bridge := &Bridge{log: store.log, config: Config{ConversationID: slack, Agent: "main", SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{}), handling: true}
	inbound := newThreadInboundMessage("mention", "1.1", "1.1")
	inbound.SlackReply.ChannelID = "C1"
	protocol.SetInboundAllowedAgents(inbound, []string{"main"})
	accepted, err := newThreadBridgeManager(nil, store, store.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return bridge }}).SubmitMention(ctx, "main", slackTarget("C1", "1.1"), inbound)
	require.NoError(t, err)
	require.True(t, accepted)

	kinds := map[string]string{}

	require.NoError(t, readRows(ctx, store.db, `SELECT c.conversation_id, concat_ws(' ', c.kind, CASE WHEN c.listed THEN 'listed' ELSE 'hidden' END, c.retention, c.parent_conversation_id)
FROM conversations c WHERE c.shadow_parent IS NULL`, "kinds", func(row rowScanner) error {
		var id, kind string
		if err := row.Scan(&id, &kind); err != nil {
			return fmt.Errorf("scan kind: %w", err)
		}

		kinds[id] = kind

		return nil
	}))
	require.Equal(t, map[string]string{
		"web": "web_chat listed record", run: "cron_run hidden record", oneOff: "one_off_cron_run hidden record", "web:" + run: "web_chat listed record",
		protocol.SlackThreadConversationID("C2", "2.2"): "slack_thread listed record", "private": "external_mcp_run hidden record", slack: "slack_thread listed record",
		"web/call-1": "subagent hidden record web", "web/call-2-review-0a1b2c3d": "review hidden nested web",
		run + "/call-2-review-0a1b2c3d": "review hidden private " + run,
	}, kinds, "a review history is no record; one of a cron run expires as its unrecorded history did")

	shadows, err := queryStrings(ctx, store.db, `SELECT conversation_id FROM conversations WHERE shadow_parent IS NOT NULL AND kind = 'shadow' AND NOT listed`, "shadows")
	require.NoError(t, err)
	require.Len(t, shadows, 8, "each Web chat and Slack thread of main has both of its shadows")

	ids, err := queryStrings(ctx, store.db, `SELECT conversation_id FROM conversations`, "conversations")
	require.NoError(t, err)

	for _, id := range ids {
		_, err := store.AppendEntryID(ctx, id, testSessionEntry("question", "answer"))
		require.NoError(t, err)
	}

	var listed []string

	for row, err := range store.sidebarSessions(ctx, nil) {
		require.NoError(t, err)

		listed = append(listed, row.Conversation.ID)
	}

	require.ElementsMatch(t, []string{"web", "web:" + run, protocol.SlackThreadConversationID("C2", "2.2"), slack}, listed)

	// Each chat Web lists, and each shadow, whoever started it; never a hidden run or a delegation.
	for _, id := range ids {
		_, eligible, _, err := store.RevertState(ctx, id)
		require.NoError(t, err)
		require.Equal(t, slices.Contains(listed, id) || slices.Contains(shadows, id), eligible, id)
	}
}
