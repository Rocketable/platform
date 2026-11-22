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
	goruntime "runtime"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cirello.io/pglock"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
		text  string
		reuse bool
	}{
		{"quoted \"value\", path \\\\ and newline\n", true},
		{"literal \\u0000", false},
		{"first\x00\x00\\\x00\\\\\x00\n\t😀", false},
		{strings.Repeat("text\x00 and literal \\u0000", 1024), false},
	} {
		data, err := json.Marshal(sample.text)
		require.NoError(t, err)

		original := string(data)
		cleaned := removeSessionEntryNUL(data)

		var text string
		require.NoError(t, json.Unmarshal(cleaned, &text))
		require.Equal(t, strings.ReplaceAll(sample.text, "\x00", ""), text)
		require.Equal(t, original, string(data))

		if sample.reuse {
			require.Same(t, &data[0], &cleaned[0])
		} else {
			require.NotSame(t, &data[0], &cleaned[0])
			require.Equal(t, len(data), cap(cleaned))
		}
	}
}

func BenchmarkObserveEntries(b *testing.B) {
	service, err := NewSessionService(b.TempDir())
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, service.Stop()) })

	recordTestConversations(b, service, "main", map[string]ConversationKind{"benchmark": KindWebChat})

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
	result := testing.Benchmark(BenchmarkObserveEntries)
	require.Positive(t, result.N)

	if !assert.Less(t, result.AllocedBytesPerOp(), int64(13<<19),
		"benchmark N=%d T=%s MemAllocs=%d MemBytes=%d B/op=%d allocs/op=%d GOMAXPROCS=%d GOGC=%q GOMEMLIMIT=%q",
		result.N, result.T, result.MemAllocs, result.MemBytes, result.AllocedBytesPerOp(), result.AllocsPerOp(), goruntime.GOMAXPROCS(0), os.Getenv("GOGC"), os.Getenv("GOMEMLIMIT")) {
		t.Log("sampled cumulative process allocations, not benchmark delta; may lag GC")
		require.NoError(t, pprof.Lookup("allocs").WriteTo(t.Output(), 1))
	}
}

func TestSessionServiceAppendEntryIDAndObserveEntries(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	recordTestConversations(t, service, "main", map[string]ConversationKind{"main": KindWebChat})

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

	require.NoError(t, service.db.QueryRowContext(t.Context(), `WITH `+sessionHistorySQL+`
SELECT pg_typeof(entry_json)::text FROM effective_entries WHERE id = $2`, "main", id1).Scan(&columnType))
	require.Equal(t, "json", columnType)

	for _, raw := range []string{`{`, `{"text":"\u0000"}`, `{"text":"\ud800"}`, `{"text":"\udc00"}`} {
		_, err := service.db.ExecContext(t.Context(), `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ('main', $1, '')`, raw)
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
		recordTestConversations(t, store, "helper", map[string]ConversationKind{"slack-thread:D123:111.222": KindSlackThread})

		message := protocol.ScheduledMessageState{ConversationID: "slack-thread:D123:111.222", Agent: "helper", Message: "later", DueAt: dueAt, Recurring: test.recurring}
		if test.recurring {
			message.Interval = time.Minute
		}

		require.NoError(t, store.PutScheduledMessage("schedule-1", &message))
		messages, err := store.ScheduledMessagesForConversation("")
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
		Attachments:        []protocol.InboundAttachment{{Name: "image.png", MIMEType: "image/png", Data: tinyPNG()}},
		AttachmentPresence: protocol.AttachmentPresenceImages,
		AttachmentWarnings: []string{"original warning"},
	}
	reply := &protocol.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.333", ThreadTS: "111.222"}

	recordTestConversations(t, store, "helper", map[string]ConversationKind{conversationID: KindSlackThread, "other": KindWebChat})

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

	_, _, err = store.DeleteThreadQueueItem(t.Context(), conversationID, "q2")
	require.NoError(t, err)

	items, err = store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "q1", items[0].ID)
}

func TestSessionServiceHeadSchemaTables(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	client, _ := pglock.UnsafeNew(store.db, pglock.WithCustomTable(runLockTable))
	require.NoError(t, client.TryCreateTable())

	tables, err := queryStrings(t.Context(), store.db, `SELECT tablename FROM pg_tables WHERE schemaname = current_schema()`, "tables")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		"conversations", "turns", "entries", "steps", "schedules", "attachments", "pending_attachment_deletions", "slack_channel_facts",
		"voice_settings", "hosted_pages", "web_spaces", "web_tab_states", "web_tab_groups", "web_tabs", "web_shared_tabs", "web_tab_attention", "web_closed_tabs", "web_search_pages", "web_searches", "rocketclaw_locks", "pg_migrations", "shadow_turns", "shadow_steps",
		"agent_drafts", "agent_draft_ops", "agent_draft_presence", "agent_draft_proposals", "agent_draft_messages",
		"behavior_settings",
	}, tables)
}

func TestSessionServiceInitializesActiveTurnIndexes(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())

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
	assert.Equal(t, []string{"turns", "turns_conversation_updated", "turns_job", "turns_pending_notes", "turns_pkey", "turns_running_replies", "turns_search_text", "turns_waiting_input"}, names)
}

func TestInitializeSessionDBReportsMigrationError(t *testing.T) {
	t.Parallel()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	logger := slog.New(slog.DiscardHandler)
	db, err := openSessionDB(t.Context(), dsn)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	err = initializeSessionDB(t.Context(), db, logger)
	require.ErrorIs(t, err, errApplySchemaMigrations)

	db, err = openSessionDB(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), `CREATE TABLE pg_migrations (id int)`)
	require.NoError(t, err)
	err = initializeSessionDB(t.Context(), db, logger)
	require.ErrorIs(t, err, errApplySchemaMigrations)
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

func TestCronRunSourcesBoundsDeliveryRows(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `INSERT INTO conversations
    (conversation_id, agent, created_by, kind, listed, retention, reports_to, continues, cron_path, cron_run_at_unix_ns, revert_message_id)
VALUES ('run', 'main', '', 'cron_run', FALSE, 'private', 'report', NULL, 'cron/report.md', 1, ''),
       ('report', 'main', 'alice', 'web_chat', TRUE, 'record', NULL, DEFAULT, NULL, DEFAULT, '1:1'),
       ('fork', 'main', 'alice', 'web_chat', TRUE, 'record', NULL, 'run', NULL, DEFAULT, '1:0');
INSERT INTO entries (id, conversation_id, entry_json, entry_timestamp)
SELECT n, 'run', '{}', '' FROM generate_series(1, 512) n;`)
	require.NoError(t, err)

	sources, err := store.CronRunSources(ctx)
	require.NoError(t, err)
	require.Equal(t, []CronRunSource{{ConversationID: "report", SourceConversationID: "run", Run: protocol.CronRun{Path: "cron/report.md", At: time.Unix(0, 1).UTC()}}}, sources)

	plan, err := queryStrings(ctx, store.db, `EXPLAIN (ANALYZE, TIMING OFF) `+cronRunSourcesSQL, "cron run sources plan")
	require.NoError(t, err)
	require.Regexp(t, `Append +\([^\n]+\(actual rows=3(?:\.0+)? loops=1\)`, strings.Join(plan, "\n"))
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

	run := protocol.Conversation{ID: "cron:daily.md:run", Agent: "main", Run: &protocol.CronRun{Path: "daily.md", At: now}}
	_, err = store.recordConversation(t.Context(), &run, conversationFacts{}, harness.AgentStickyOff)
	require.NoError(t, err)
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

func TestSessionServiceBeginGoalAllowsGoalAfterTerminal(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	recordTestConversations(t, store, "main", map[string]ConversationKind{"thread-1": KindWebChat})

	require.NoError(t, store.BeginGoal("thread-1", &GoalState{Objective: "first", MaxTurns: 3}))
	_, err := store.UpdateGoalStatus("thread-1", GoalStatusComplete, "done")
	require.NoError(t, err)
	require.NoError(t, store.BeginGoal("thread-1", &GoalState{Objective: "second", MaxTurns: 1}))

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
}

func TestHoldRunLockWaitsForReleaseOrCancellation(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
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

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	err = holdRunLock(ctx, service.db, work)
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	require.ErrorIs(t, err, pglock.ErrNotAcquired)
	require.Empty(t, work.RunCalls())

	require.NoError(t, lock.Close())
	require.ErrorIs(t, holdRunLock(t.Context(), service.db, work), errWork)
	require.Len(t, work.RunCalls(), 1)

	for i := range 200 {
		require.ErrorIs(t, holdRunLock(t.Context(), service.db, &runLockWorkMock{RunFunc: func(context.Context) error {
			time.Sleep(time.Duration(i%40) * 50 * time.Microsecond)
			return errWork
		}}), errWork)

		lock, err := client.Acquire(runLockName, pglock.FailIfLocked())
		require.NoError(t, err, "the run lock is free once holdRunLock returns, even when the work ends during its first heartbeat")
		require.NoError(t, lock.Close())
	}
}

func TestHoldRunLockAllowsSessionServiceWhileHeld(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
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
	service := newTestSessionService(t)
	service.db.SetMaxOpenConns(2)
	recordTestConversations(t, service, "main", map[string]ConversationKind{"thread": KindWebChat})

	conn, err := service.db.Conn(t.Context())
	require.NoError(t, err)
	errPing := service.db.PingContext(t.Context())
	require.NoError(t, errors.Join(errPing, conn.Close()))

	t.Parallel()

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
			_, err := service.finishTurn(t.Context(), turnID, &turnFinish{conversationID: strings.TrimSpace("thread"), accountGoal: true, outbound: protocol.NewOutboundMessage("thread", "")})
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
	recordTestConversations(t, service, "main", map[string]ConversationKind{"main": KindWebChat, "thread": KindWebChat})
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
	summaries := sidebarSummaries(t, service)
	require.Equal(t, "seed", summaries["main"].LastMessage)
	require.Equal(t, &protocol.SessionSummary{ConversationID: "thread", LastMessage: "assistant", LastUpdated: time.Unix(1, 0).UTC()}, summaries["thread"])
}

func TestDeleteChatRemovesItAsRetentionDoes(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	recordTestConversations(t, service, "main", map[string]ConversationKind{"chat": KindWebChat, "busy": KindWebChat, "cron:daily:run": KindCronRun})
	recordPrivateHistories(t, service, map[string]ConversationKind{"mcp-run": KindExternalMCPRun})
	_, err := service.db.ExecContext(t.Context(), `WITH linked AS (
    UPDATE conversations SET reports_to = 'chat' WHERE conversation_id = 'cron:daily:run'
)
UPDATE conversations SET external_id = 'external', external_agent = 'main', external_run = 'mcp-run' WHERE conversation_id = 'chat'`)
	require.NoError(t, err)

	for _, id := range []string{"chat", "busy", "cron:daily:run", "mcp-run"} {
		_, err := service.AppendEntryID(t.Context(), id, testSessionEntry(id, "assistant"))
		require.NoError(t, err)
	}

	require.NoError(t, service.SaveAttachment(t.Context(), "chat", "", &protocol.OutboundAttachment{ID: "blob", Name: "result", Data: []byte("bytes")}, false))
	_, err = service.db.ExecContext(t.Context(), `INSERT INTO web_tabs (username, path, conversation_id, position) VALUES ('bob', '/s/chat', 'chat', 0)`)
	require.NoError(t, err)
	seedActiveTurn(t, service, "busy", "running", nil)

	require.ErrorIs(t, service.DeleteChat(t.Context(), "busy"), ErrSessionBusy)
	require.ErrorIs(t, service.DeleteChat(t.Context(), "mcp-run"), ErrNotChat)
	require.NoError(t, service.DeleteChat(t.Context(), "chat"))

	remaining, err := queryStrings(t.Context(), service.db, `SELECT conversation_id FROM conversations ORDER BY 1`, "remaining conversations")
	require.NoError(t, err)
	assert.Equal(t, []string{"busy"}, remaining)

	for table, want := range map[string]int{"entries": 1, "attachments": 0, "pending_attachment_deletions": 0, "web_tabs": 0} {
		var count int
		require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&count))
		assert.Equal(t, want, count, table)
	}
}

func TestHistoryRemovalIncludesDelegations(t *testing.T) {
	t.Parallel()

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
		_, err = reopened.db.ExecContext(t.Context(), `INSERT INTO pending_attachment_deletions (id) VALUES ('blob')`)
		require.NoError(t, err)
		_, err = reopened.PruneStateBefore(t.Context(), time.Unix(0, 0), 0)
		require.NoError(t, err)
		require.NoError(t, reopened.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pending_attachment_deletions`).Scan(&count))
		require.Zero(t, count, "missing bytes are an idempotent deletion, not a failed retry")
	})
	t.Run("S3 plain delete", func(t *testing.T) {
		requests := make(chan string, 8)

		firstDelete := make(chan struct{}, 1)
		firstDelete <- struct{}{}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests <- r.Method

			switch r.Method {
			case http.MethodPut:
			case http.MethodDelete:
				assert.False(t, r.URL.Query().Has("versionId"), "delete the key, not one version")

				select {
				case <-firstDelete:
					w.WriteHeader(http.StatusServiceUnavailable)
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			default:
				w.WriteHeader(http.StatusForbidden)
			}
		}))
		t.Cleanup(server.Close)
		store := newTestSessionService(t)
		store.attachments = s3Attachments{bucket: "originals", client: s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: new(server.URL), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1})}
		require.NoError(t, store.SaveAttachment(t.Context(), "parent", "", &protocol.OutboundAttachment{ID: "blob", Name: "result", Data: []byte("bytes")}, false))
		_, err := store.DeleteSession(t.Context(), "parent")
		require.Error(t, err)
		require.Equal(t, http.MethodPut, <-requests)
		require.Equal(t, http.MethodDelete, <-requests)
		pending, err := queryStrings(t.Context(), store.db, `SELECT id FROM pending_attachment_deletions`, "failed S3 deletion")
		require.NoError(t, err)
		require.Equal(t, []string{"blob"}, pending, "a failed deletion keeps ownership")
		_, err = store.PruneStateBefore(t.Context(), time.Unix(0, 0), 0)
		require.NoError(t, err)
		require.Equal(t, http.MethodDelete, <-requests)
		_, err = store.db.ExecContext(t.Context(), `INSERT INTO pending_attachment_deletions (id) VALUES ('blob')`)
		require.NoError(t, err)
		_, err = store.PruneStateBefore(t.Context(), time.Unix(0, 0), 0)
		require.NoError(t, err, "deleting a missing S3 key succeeds even without permission to see that it is missing")
		require.Equal(t, http.MethodDelete, <-requests)
		require.Empty(t, requests)
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
		typed := testSessionEntryAt(cutoff.Add(-time.Hour), id)
		typed.ReplayInput[0] = json.RawMessage(`{"type":"message","role":"user","prompt_header":"[Web]","content":"typed"}`)
		_, err := store.AppendEntryID(t.Context(), id, typed)
		require.NoError(t, err)

		decoy := protocol.SlackThreadConversationID("DX1", slackTestTS(cutoff.Add(-time.Hour))) + "/call-1"
		unrelated := id + "/unrelated"

		require.NoError(t, store.UpsertThread(decoy, ThreadState{Agent: "planner"}))
		require.NoError(t, store.UpsertThread(unrelated, ThreadState{Agent: "planner"}))
		_, err = store.AppendEntryID(t.Context(), unrelated, testSessionEntryAt(cutoff.Add(time.Hour), unrelated))
		require.NoError(t, err)
		children, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM conversations WHERE shadow_parent = $1 ORDER BY 1`, "owned children", id)
		require.NoError(t, err)

		histories := make([]string, 0, 3+2*len(children))
		histories = append(histories, id+"/call-1", id+"/call-1/call-2", decoy)

		require.NoError(t, store.startSubagent(t.Context(), id, &harness.SubagentWrite{Key: harness.ChildKey{Path: "/call-1", Turn: "turn-1", Call: "call-1"}, Agent: "helper"}))
		require.NoError(t, store.startSubagent(t.Context(), id+"/call-1", &harness.SubagentWrite{Key: harness.ChildKey{Path: "/call-2", Turn: "turn-2", Call: "call-2"}, Agent: "helper"}))

		histories = append(histories, children...)
		for _, child := range children {
			require.NoError(t, store.startSubagent(t.Context(), child, &harness.SubagentWrite{Key: harness.ChildKey{Path: "/task", Turn: "turn-1", Call: "task"}, Agent: "helper"}))
			require.NoError(t, store.startSubagent(t.Context(), child+"/task", &harness.SubagentWrite{Key: harness.ChildKey{Path: "/nested", Turn: "turn-1", Call: "nested"}, Agent: "helper"}))
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

			bridge := &Bridge{config: Config{ConversationID: id, Agent: "main", SessionService: store}, log: store.log, runtime: slackAgentRuntime(t)}
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

func TestDeleteSessionRejectsBlankConversationID(t *testing.T) {
	t.Parallel()

	_, err := DeleteSession(context.Background(), t.TempDir(), " ")
	require.ErrorContains(t, err, "conversation ID is required")
}

func TestSessionServicePersistsExternalMCPSessionMapping(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)

	require.NoError(t, store.RegisterExternalMCPConversation(" ticket-123 ", " selected ", &ExternalMCPSessionState{Agent: " planner ", PrivateConversationID: " external_mcp:planner:def ", ManagedConversationID: " web-chat "}))

	session, ok, err := store.ExternalMCPSession("ticket-123")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ExternalMCPSessionState{Agent: "planner", PrivateConversationID: "external_mcp:planner:def", ManagedConversationID: "web-chat"}, session)
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
	assert.Equal(t, []string{session.ManagedConversationID}, listed, "only the Web chat holds the binding")

	for conversationID, entryID := range map[string]int64{session.PrivateConversationID: privateEntryID, session.ManagedConversationID: managedEntryID} {
		entries, err := store.ObserveEntries(t.Context(), conversationID)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, entryID, entries[0].ID)
	}

	require.Error(t, store.RegisterExternalMCPConversation("duplicate-private", "main", &ExternalMCPSessionState{Agent: "main", PrivateConversationID: session.PrivateConversationID, ManagedConversationID: "duplicate-managed"}))
}

func TestSessionServiceRejectsBlankKeys(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)

	require.ErrorContains(t, store.UpsertThread(" ", ThreadState{Agent: "agent"}), "thread conversation ID is required")
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

func TestDeleteOwnedHistoryReportsDeleteFailures(t *testing.T) {
	t.Parallel()

	_, err := deleteOwnedHistory(context.Background(), errStore{errExec: errors.New("no delete")}, []string{"main"})
	require.ErrorContains(t, err, "lock session history")
}

func TestSessionServicePrunesOldState(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	cutoff := time.Unix(1_700_000_000, 0).UTC()
	oldTime := cutoff.Add(-time.Second)
	newTime := cutoff.Add(time.Second)

	oldThread := protocol.SlackThreadConversationID("DOLD", slackTestTS(oldTime))
	activeOldThread := protocol.SlackThreadConversationID("DACTIVE", slackTestTS(oldTime))
	newThread := protocol.SlackThreadConversationID("DNEW", slackTestTS(newTime))

	boundaryThread := protocol.SlackThreadConversationID("DBOUNDARY", slackTestTS(cutoff))
	recordTestConversations(t, store, "planner", map[string]ConversationKind{
		oldThread: KindSlackThread, activeOldThread: KindSlackThread, newThread: KindSlackThread, boundaryThread: KindSlackThread, "slack-thread:D123:not-a-time": KindSlackThread,
		"cron:daily:old": KindCronRun, "one-off-cron:daily:old": KindOneOffCronRun, "cron:daily:new": KindCronRun, "cron:daily:boundary": KindCronRun, "cron:daily:reported": KindCronRun,
		"cron:daily:continued": KindCronRun, "draft-chat": KindAgentDraft,
	})

	orphanGoal := protocol.SlackThreadConversationID("DGOAL", slackTestTS(oldTime))
	recordPrivateHistories(t, store, map[string]ConversationKind{
		"external_mcp:cron:orphan": KindExternalMCPRun, "slack-thread:DORPHAN:1.000": KindSlackThread, orphanGoal: KindSlackThread,
	})
	_, err := store.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention) VALUES ('unmanaged-web', '', '', 'web_chat', FALSE, 'nested')`)
	require.NoError(t, err)

	require.NoError(t, store.UpsertThread("empty-recorded", ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, store.UpsertThread("empty-new", ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, store.UpsertThread("empty-running", ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, store.UpsertThread("continuing", ThreadState{Agent: "planner"}))
	require.NoError(t, store.UpsertThread("old-web", ThreadState{Agent: "planner"}))
	require.NoError(t, store.UpsertThread("active-web", ThreadState{Agent: "planner"}))
	_, err = store.db.ExecContext(t.Context(), `UPDATE conversations SET created_at = $1, continues = CASE conversation_id WHEN 'continuing' THEN 'cron:daily:new' WHEN 'active-web' THEN 'cron:daily:continued' END
WHERE conversation_id IN ('empty-recorded', 'continuing', 'old-web', 'active-web')`, oldTime)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `UPDATE conversations SET reports_to = 'active-web' WHERE conversation_id = 'cron:daily:reported'`)
	require.NoError(t, err)
	require.NoError(t, store.startSubagent(t.Context(), "active-web", &harness.SubagentWrite{Key: harness.ChildKey{Path: "/helper", Turn: "turn-0", Call: "helper"}, Agent: "planner"}))
	require.NoError(t, store.startSubagent(t.Context(), "cron:daily:old", &harness.SubagentWrite{Key: harness.ChildKey{Path: "/helper", Turn: "turn-0", Call: "helper"}, Agent: "planner"}))
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
		"old-web":                      oldTime,
		"active-web":                   newTime,
		"cron:daily:reported":          oldTime,
		"cron:daily:continued":         oldTime,
		"draft-chat":                   oldTime,
		"active-web/helper":            oldTime,
		"cron:daily:old/helper":        oldTime,
	} {
		_, err := store.AppendEntryID(t.Context(), conversationID, testSessionEntryAt(ts, conversationID))
		require.NoError(t, err)
	}

	seedActiveTurn(t, store, oldThread, "stale-turn", &harness.SessionEntry{Version: 1, Type: "turn", TurnID: "stale-turn"})
	seedActiveTurn(t, store, "cron:daily:old", "stale-run-turn", &harness.SessionEntry{Version: 1, Type: "turn", TurnID: "stale-run-turn"})
	require.NoError(t, store.SaveAttachment(t.Context(), "cron:daily:old", "", &protocol.OutboundAttachment{ID: "run-blob", Name: "result", Data: []byte("bytes")}, false))

	require.NoError(t, store.BeginGoal(orphanGoal, &GoalState{Objective: "stale goal", MaxTurns: 1}))
	require.NoError(t, store.BeginGoal(activeOldThread, &GoalState{Objective: "keep", MaxTurns: 1}))

	stats, err := store.PruneStateBefore(context.Background(), cutoff, 0)
	require.NoError(t, err)
	assert.Equal(t, PruneStateStats{Threads: 6, SessionRows: 8}, stats)

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
	assert.NotContains(t, threadIDs, "old-web", "a Web chat expires with its last message")
	assert.Contains(t, threadIDs, "active-web", "a Web chat's age is its last message, not its creation")

	thread, ok, err := store.Thread(activeOldThread)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ThreadState{Agent: "selected", CreatedBy: ThreadCreatedByCron, sticky: harness.AgentStickySlack}, thread)
	assert.Contains(t, threadIDs, activeOldThread)
	assert.Contains(t, threadIDs, newThread)
	assert.Contains(t, threadIDs, boundaryThread)

	active, err := store.HasActiveTurn(t.Context(), oldThread)
	require.NoError(t, err)
	assert.False(t, active)
	assert.Empty(t, testTurnStepKeys(t, store, oldThread))
	assert.Empty(t, testTurnStepKeys(t, store, "cron:daily:old"))
	attachments, err := queryStrings(t.Context(), store.db, `SELECT id FROM attachments`, "remaining attachments")
	require.NoError(t, err)
	assert.Empty(t, attachments)

	for _, conversationID := range []string{oldThread, "slack-thread:D123:not-a-time", "external_mcp:cron:orphan", "cron:daily:old", "cron:daily:old/helper", "one-off-cron:daily:old", "slack-thread:DORPHAN:1.000"} {
		entries, err := store.ObserveEntries(context.Background(), conversationID)
		require.NoError(t, err)
		assert.Empty(t, entries, conversationID)
		assert.NotContains(t, threadIDs, conversationID)
	}

	for _, conversationID := range []string{activeOldThread, "cron:daily:new", "cron:daily:boundary", "unmanaged-web", "cron:daily:reported", "cron:daily:continued", "draft-chat", "active-web/helper"} {
		entries, err := store.ObserveEntries(context.Background(), conversationID)
		require.NoError(t, err)
		assert.Len(t, entries, 1, conversationID)
		assert.Contains(t, threadIDs, conversationID)
	}

	t.Run("sticky message window", func(t *testing.T) {
		store := newTestSessionService(t)
		ctx := t.Context()
		cutoff := time.Unix(1_700_000_000, 0).UTC()
		old := cutoff.Add(-time.Hour)

		for _, sticky := range []string{"user", "global"} {
			id, _, err := store.CreateStickyConversation(ctx, protocol.Conversation{ID: sticky, Agent: sticky, CreatedBy: "alice@example.com"}, harness.AgentSticky(sticky))
			require.NoError(t, err)

			for _, item := range []struct {
				at   time.Time
				text string
			}{{old, "expired"}, {cutoff, "boundary"}, {cutoff.Add(time.Second), "fresh"}} {
				_, err := store.AppendEntryID(ctx, id, testSessionEntryAt(item.at, item.text))
				require.NoError(t, err)
			}

			seedActiveTurn(t, store, id, "ended-"+id, testSessionEntryAt(old, "expired fallback"))
			_, err = store.db.ExecContext(ctx, `UPDATE turns SET phase = 'done', terminal = 'stopped', created_at_unix_ns = $2 WHERE id = $1`, "ended-"+id, old.UnixNano())
			require.NoError(t, err)
			seedActiveTurn(t, store, id, "active-"+id, testSessionEntryAt(cutoff.Add(time.Second), "current work"))
			require.NoError(t, store.PutThreadQueueItem("queued-"+id, &protocol.ThreadQueueItem{ID: "queued-" + id, ConversationID: id, Message: "pending", Kind: protocol.InboundKindHeld, StashAt: old}))
		}

		_, _, err := store.CreateStickyConversation(ctx, protocol.Conversation{ID: "empty-user", Agent: "user", CreatedBy: "bob@example.com"}, harness.AgentStickyUser)
		require.NoError(t, err)
		_, err = store.AppendEntryID(ctx, "empty-user", testSessionEntryAt(old, "expired only"))
		require.NoError(t, err)
		require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(ctx, protocol.Conversation{ID: "born-off", Agent: "user", CreatedBy: "carol@example.com"}))
		_, err = store.AppendEntryID(ctx, "born-off", testSessionEntryAt(old, "expired ordinary"))
		require.NoError(t, err)
		require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(ctx, protocol.Conversation{ID: "cron:old", Agent: "global", Run: &protocol.CronRun{Path: "cron/daily.md"}}))
		_, err = store.AppendEntryID(ctx, "cron:old", testSessionEntryAt(old, "expired hidden report"))
		require.NoError(t, err)
		_, err = (&Runtime{Sessions: store}).ContinueRun(ctx, "cron:old", "global", true)
		require.NoError(t, err)
		_, err = store.db.ExecContext(ctx, `UPDATE conversations SET created_at = $1 WHERE conversation_id = 'cron:old'`, old)
		require.NoError(t, err)

		_, err = store.PruneStateBefore(ctx, cutoff, 0)
		require.NoError(t, err)

		runs, err := queryStrings(ctx, store.db, `SELECT conversation_id FROM conversations WHERE kind = 'cron_run'`, "remaining runs")
		require.NoError(t, err)
		assert.Empty(t, runs, "a run whose messages all expired is deleted, not left empty under the sticky chat")

		_, recorded, err := store.Thread("born-off")
		require.NoError(t, err)
		require.False(t, recorded, "an expired chat born off is deleted, not kept as a sticky chat")

		for id, want := range map[string]int{"user": 2, "global": 2, "empty-user": 0} {
			_, recorded, err := store.Thread(id)
			require.NoError(t, err)
			require.True(t, recorded, "sticky rows survive even when every message expired")

			entries, err := store.ObserveEntries(ctx, id)
			require.NoError(t, err)
			require.Len(t, entries, want)

			transcript := testHistoryEntries(t, store, id)

			for _, entry := range transcript {
				raw, err := json.Marshal(entry.Entry)
				require.NoError(t, err)
				assert.NotContains(t, string(raw), "expired")
			}

			if id == "empty-user" {
				continue
			}

			queue, err := store.ThreadQueueForConversation(id)
			require.NoError(t, err)
			require.Len(t, queue, 1, "queued input survives")

			active, err := store.HasActiveTurn(ctx, id)
			require.NoError(t, err)
			assert.True(t, active, "a live turn survives")
		}
	})

	t.Run("sticky expired attachments", func(t *testing.T) {
		store := newTestSessionService(t)
		ctx := t.Context()
		cutoff := time.Unix(1_700_000_000, 0).UTC()
		old := cutoff.Add(-time.Hour)

		id, _, err := store.CreateStickyConversation(ctx, protocol.Conversation{ID: "room", Agent: "room", CreatedBy: "alice@example.com"}, harness.AgentStickyUser)
		require.NoError(t, err)
		require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(ctx, protocol.Conversation{ID: "cron:old", Agent: "room", Run: &protocol.CronRun{Path: "cron/daily.md"}}))
		_, err = (&Runtime{Sessions: store}).ContinueRun(ctx, "cron:old", id, true)
		require.NoError(t, err)

		seedActiveTurn(t, store, id, "ended", nil)
		_, err = store.db.ExecContext(ctx, `UPDATE turns SET phase = 'done', terminal = 'stopped', created_at_unix_ns = $2 WHERE id = $1`, "ended", old.UnixNano())
		require.NoError(t, err)
		seedActiveTurn(t, store, id, "live", nil)

		owners := map[string]string{"file-expired": id, "file-shown": id, "file-queued": id, "file-scheduled": id, "file-upload": id, "file-ended": id, "file-live": id, "file-run": "cron:old"}

		turns := map[string]string{"file-ended": "ended", "file-live": "live"}
		for file, owner := range owners {
			require.NoError(t, store.SaveAttachment(ctx, owner, turns[file], &protocol.OutboundAttachment{ID: file, Name: file, Data: []byte(file)}, file == "file-upload"))
		}

		for owner, text := range map[string]string{id: "attachment:file-expired attachment:file-shown attachment:file-queued attachment:file-scheduled attachment:file-live", "cron:old": "attachment:file-run"} {
			_, err := store.AppendEntryID(ctx, owner, testSessionEntryAt(old, text))
			require.NoError(t, err)
		}

		_, err = store.AppendEntryID(ctx, id, testSessionEntryAt(cutoff.Add(time.Second), "attachment:file-shown"))
		require.NoError(t, err)
		require.NoError(t, store.PutThreadQueueItem("queued", &protocol.ThreadQueueItem{ID: "queued", ConversationID: id, Message: "attachment:file-queued", Kind: protocol.InboundKindHeld, StashAt: old}))
		require.NoError(t, store.PutScheduledMessage("daily", &protocol.ScheduledMessageState{ConversationID: id, Agent: "room", Message: "attachment:file-scheduled", DueAt: cutoff, Recurring: true, Interval: time.Hour}))

		_, err = store.PruneStateBefore(ctx, cutoff, 0)
		require.NoError(t, err)

		kept, err := queryStrings(ctx, store.db, `SELECT id FROM attachments ORDER BY id`, "kept attachments")
		require.NoError(t, err)
		assert.Equal(t, []string{"file-live", "file-queued", "file-scheduled", "file-shown", "file-upload"}, kept)

		pending, err := queryStrings(ctx, store.db, `SELECT id FROM pending_attachment_deletions`, "pending attachment deletions")
		require.NoError(t, err)
		assert.Empty(t, pending)

		for file := range owners {
			_, err := store.attachments.Get(ctx, file)
			if slices.Contains(kept, file) {
				assert.NoError(t, err, file)
			} else {
				assert.ErrorIs(t, err, fs.ErrNotExist, file)
			}
		}
	})
}

func TestSessionServiceRetainsQueuedConversations(t *testing.T) {
	t.Parallel()

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
	require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: privateID, ManagedConversationID: managedID}))
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
		_, _, err := store.DeleteThreadQueueItem(t.Context(), orphanID, id)
		require.NoError(t, err)
	}

	_, _, err = store.DeleteThreadQueueItem(t.Context(), privateID, privateItem.ID)
	require.NoError(t, err)
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

	scheduledStore := newTestSessionService(t)
	for _, tc := range []struct {
		name, conversation, parent, destination string
		orphan                                  bool
	}{
		{"scheduled", emptySlack, "", "", false},
		{"scheduled run", "cron:scheduled", "", "", false},
		{"scheduled orphan", "external_mcp:scheduled", "", "", true},
		{"scheduled shadow", "scheduled-child", "scheduled-parent", "", false},
		{"scheduled producer", "scheduled-producer", "", "scheduled-owner", false},
		{"scheduled producer shadow", "scheduled-child", "scheduled-producer", "scheduled-owner", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := scheduledStore
			ctx := t.Context()

			for _, id := range []string{tc.conversation, tc.parent, tc.destination} {
				switch {
				case id == "":
				case tc.orphan && id == tc.conversation:
					recordPrivateHistories(t, store, map[string]ConversationKind{id: KindExternalMCPRun})
				case id == emptySlack:
					recordTestConversations(t, store, "planner", map[string]ConversationKind{id: KindSlackThread})
				case id == "cron:scheduled":
					recordTestConversations(t, store, "planner", map[string]ConversationKind{id: KindCronRun})
				default:
					require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "planner"}))
				}
			}

			if tc.orphan || tc.conversation == emptySlack || tc.conversation == "cron:scheduled" {
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

			scheduled := protocol.ScheduledMessageState{ConversationID: tc.conversation, Agent: "planner", Message: "pending follow-up", DueAt: cutoff.Add(2160 * time.Hour)}
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

			ids, err := queryStrings(ctx, store.db, `SELECT conversation_id FROM conversations`, "remaining scheduled conversations")
			require.NoError(t, err)
			require.Empty(t, ids)
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
		require.NoError(t, bridge.ScheduleMessage(origin, 2160*time.Hour, "pending follow-up", false))

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
	require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID}))

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
	t.Parallel()

	store := newTestSessionService(t)
	cutoff := time.Unix(1_700_000_000, 0).UTC()

	cases := []struct {
		name                                           string
		paired, privateFresh, managedFresh, wantPruned bool
		privateEmpty, managedEmpty                     bool
	}{
		{name: "paired_both_stale", paired: true, wantPruned: true},
		{name: "paired_private_fresh", paired: true, privateFresh: true},
		{name: "paired_managed_fresh", paired: true, managedFresh: true},
		{name: "paired_private_empty", paired: true, privateEmpty: true, managedFresh: true},
		{name: "paired_managed_empty", paired: true, managedEmpty: true, privateFresh: true},
		{name: "paired_both_empty", paired: true, privateEmpty: true, managedEmpty: true, wantPruned: true},
		{name: "legacy_stale", wantPruned: true},
		{name: "legacy_fresh", managedFresh: true},
	}

	for _, tt := range cases {
		managedConversationID := "managed-" + tt.name
		publicID := "public-" + tt.name

		privateConversationID := ""
		if tt.paired {
			privateConversationID = "external_mcp:planner:" + tt.name
			require.NoError(t, store.RegisterExternalMCPConversation(publicID, "main", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID}))
		} else {
			recordTestConversations(t, store, "main", map[string]ConversationKind{managedConversationID: KindWebChat})
			_, err := store.db.ExecContext(t.Context(), `UPDATE conversations SET external_id = $2, external_agent = 'planner', origin_pairs = 'null' WHERE conversation_id = $1`, managedConversationID, publicID)
			require.NoError(t, err)
		}

		_, err := store.db.ExecContext(t.Context(), `UPDATE conversations SET created_at = $1 WHERE conversation_id IN ($2, $3)`, cutoff.Add(-time.Hour), managedConversationID, privateConversationID)
		require.NoError(t, err)

		if tt.paired && !tt.privateEmpty {
			_, err := store.AppendEntryID(t.Context(), privateConversationID, testSessionEntryAt(pruneTestTime(cutoff, tt.privateFresh), "private"))
			require.NoError(t, err)
		}

		if !tt.managedEmpty {
			_, err := store.AppendEntryID(t.Context(), managedConversationID, testSessionEntryAt(pruneTestTime(cutoff, tt.managedFresh), "managed"))
			require.NoError(t, err)
		}
	}

	_, err := store.PruneStateBefore(t.Context(), cutoff, 0)
	require.NoError(t, err)

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, ok, err := store.ExternalMCPSession("public-" + tt.name)
			require.NoError(t, err)
			assert.Equal(t, !tt.wantPruned, ok, "External MCP binding")

			conversationIDs := []string{"managed-" + tt.name}
			if tt.paired {
				conversationIDs = append(conversationIDs, "external_mcp:planner:"+tt.name)
			}

			for _, conversationID := range conversationIDs {
				_, ok, err := store.Thread(conversationID)
				require.NoError(t, err)
				assert.Equal(t, !tt.wantPruned, ok, conversationID)
			}
		})
	}
}

func pruneTestTime(cutoff time.Time, fresh bool) time.Time {
	if fresh {
		return cutoff.Add(time.Second)
	}

	return cutoff.Add(-time.Second)
}

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
	errExec error
}

func (s errStore) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, s.errExec
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

func testTurnStepKeys(t *testing.T, service *SessionService, conversationID string) []string {
	t.Helper()

	keys, err := queryStrings(t.Context(), service.db, `SELECT key FROM steps WHERE conversation_id = $1 ORDER BY key`, "turn step keys", conversationID)
	require.NoError(t, err)

	return keys
}

func seedActiveTurn(t *testing.T, service *SessionService, conversationID, turnID string, record *harness.SessionEntry) {
	t.Helper()

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "seed", true)
	inbound.ConversationID = conversationID
	require.NoError(t, startTurnDB(t.Context(), service.db, turnID, conversationID, inbound))

	if record != nil {
		require.NoError(t, saveTestTurn(t.Context(), service, conversationID, turnID, nil, record))
	}
}

func endTestTurn(t *testing.T, service *SessionService, conversationID, turnID string, terminal protocol.Terminal) {
	t.Helper()

	_, err := service.finishTurn(t.Context(), turnID, &turnFinish{conversationID: strings.TrimSpace(conversationID), outbound: protocol.NewOutboundMessage(conversationID, ""), terminal: terminal})
	require.NoError(t, err)
	require.NoError(t, service.closeTurn(t.Context(), turnID))
}

func newTestSessionService(t *testing.T) *SessionService {
	t.Helper()

	return newTestSessionServiceAt(t, t.TempDir())
}

func newColdTestSessionServiceAt(t *testing.T, workspace string) *SessionService {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, "agents"), 0o755))
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

func recordTestConversations(tb testing.TB, s *SessionService, agent string, kinds map[string]ConversationKind) {
	tb.Helper()

	for id, kind := range kinds {
		facts := conversationFacts{kind: kind}
		if kind == KindSlackThread {
			facts.slackChannel, facts.slackThread = testSlackThread(id)
		}

		_, err := s.recordConversation(tb.Context(), &protocol.Conversation{ID: id, Agent: agent}, facts, harness.AgentStickyOff)
		require.NoError(tb, err)
	}
}

func recordLegacyDelegations(t *testing.T, s *SessionService, parent string, calls ...string) {
	t.Helper()

	for _, call := range calls {
		_, err := s.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, parent_conversation_id, kind, listed, retention, call_id)
VALUES ($1 || '/' || $2, '', '', $1, $3, FALSE, 'nested', $2)`, parent, call, KindSubagent)
		require.NoError(t, err)
	}
}

func recordPrivateHistories(t *testing.T, s *SessionService, kinds map[string]ConversationKind) {
	t.Helper()

	for id, kind := range kinds {
		channel, thread := testSlackThread(id)
		_, err := s.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, slack_channel, slack_thread)
VALUES ($1, '', '', $2, FALSE, 'private', NULLIF($3, ''), NULLIF($4, ''))`, id, kind, channel, thread)
		require.NoError(t, err)
	}
}

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

func TestVoiceSettings(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()

	settings := func(username string) VoiceSettings {
		t.Helper()

		current, err := store.VoiceSettings(ctx, username)
		require.NoError(t, err)

		return current
	}

	assert.Equal(t, VoiceSettings{}, settings("alice"), "a person with no row gets the defaults")

	agent, voice, on := "main", "cedar", true
	require.NoError(t, store.UpdateVoiceSettings(ctx, "alice", &agent, nil, nil))
	require.NoError(t, store.UpdateVoiceSettings(ctx, "alice", nil, &voice, &on))
	require.NoError(t, store.UpdateVoiceSettings(ctx, "bob", nil, nil, &on))
	assert.Equal(t, VoiceSettings{Agent: "main", Voice: "cedar", Transcript: true}, settings("alice"), "an update keeps the fields it was not given")
	assert.Equal(t, VoiceSettings{Transcript: true}, settings("bob"))
}

func TestStickySessions(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()
	_, _, err := store.CreateStickyConversation(ctx, protocol.Conversation{ID: "anonymous", Agent: "mine"}, harness.AgentStickyUser)
	require.ErrorContains(t, err, "identifiable person")

	for _, conversation := range []protocol.Conversation{{ID: "plain-mine", Agent: "mine", CreatedBy: "alice"}, {ID: "plain-ours", Agent: "ours", CreatedBy: "bob"}} {
		require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(ctx, conversation))
	}

	agents := harness.Agents{Items: map[string]harness.Agent{"mine": {Sticky: harness.AgentStickyUser}, "ours": {Sticky: harness.AgentStickyGlobal}, "free": {Sticky: harness.AgentStickyOff}}}
	open := func(id, agent, username string) (string, bool) {
		t.Helper()

		got, resumed, err := store.CreateStickyConversation(ctx, protocol.Conversation{ID: id, Agent: agent, CreatedBy: username}, agents.Items[agent].Sticky)
		require.NoError(t, err)

		return got, resumed
	}

	sessions, err := store.StickySessions(ctx, "alice", agents)
	require.NoError(t, err)
	assert.Empty(t, sessions, "chats born off are not sticky chats, even with an agent that is sticky now")

	for _, first := range []struct{ id, agent, username string }{{"alice-mine", "mine", "alice"}, {"bob-mine", "mine", "bob"}, {"shared", "ours", ""}} {
		id, resumed := open(first.id, first.agent, first.username)
		assert.Equal(t, []any{first.id, false}, []any{id, resumed}, "the first request creates the chat")
	}

	for username, want := range map[string]map[string]string{
		"alice": {"mine": "alice-mine", "ours": "shared"},
		"bob":   {"mine": "bob-mine", "ours": "shared"},
		"carol": {"ours": "shared"},
	} {
		sessions, err := store.StickySessions(ctx, username, agents)
		require.NoError(t, err)
		assert.Equal(t, want, sessions, username)

		for agent, selected := range want {
			id, resumed := open("candidate", agent, username)
			assert.Equal(t, []any{selected, true}, []any{id, resumed}, "later requests resume it")
		}
	}

	agents.Items["mine"] = harness.Agent{Sticky: harness.AgentStickyGlobal}
	id, resumed := open("mine-shared", "mine", "alice")
	assert.Equal(t, []any{"mine-shared", false}, []any{id, resumed}, "a chat born for one person is not the shared chat after the agent turns global")
}

func TestBehaviorSettingsRoundTrip(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()

	_, found, err := store.behaviorSettings(ctx)
	require.NoError(t, err)
	require.False(t, found)

	cfg := &config.Config{AutoApproverModel: "gpt-b", Overlays: []string{"https://example.com/overlay.git"}, Logging: config.LoggingConfig{Level: "warn"},
		Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"main"}, AllowedUserIDs: []string{"U1"}}}}}

	for _, model := range []string{"gpt-a", "gpt-c"} {
		cfg.AutoApproverModel = model
		require.NoError(t, putBehaviorSettings(ctx, store.db, new(cfg.Behavior())))

		stored, found, err := store.behaviorSettings(ctx)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, cfg.Behavior(), stored)
	}
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

	_, err := store.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, title, directory, last_updated, running, synced_at, remote_session, space_id)
VALUES ('opencode:box:ses_b', '', '', 'remote', FALSE, 'remote', 'box', '', '/srv', now(), FALSE, now(), 'ses_b', $1),
    ('opencode:box:ses_o', '', '', 'remote', FALSE, 'remote', 'box', '', '/srv', now(), FALSE, now(), 'ses_o', $2)`, banana, ops.ID)
	require.NoError(t, err)

	for _, owner := range []string{"opencode:box:ses_b", "opencode:box:ses_o"} {
		require.NoError(t, store.PutPage(ctx, &Page{Agent: owner, Kind: PageFile, Name: "index.html", ContentType: "text/html", Body: []byte("x"), Conversation: owner}))
	}

	require.NoError(t, store.DeleteSpace(ctx, banana))
	assert.Empty(t, birth("banana-1", "Banana"), "deleting a space clears the birth space of sessions born there; an existing session never recreates it")

	pages, err := store.Pages(ctx, "")
	require.NoError(t, err)
	require.Len(t, pages, 1)
	assert.Equal(t, "opencode:box:ses_o", pages[0].Agent)

	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `DELETE FROM web_spaces WHERE id = $1`, ops.ID)
	require.NoError(t, err)

	deleted := make(chan error, 1)
	go func() { deleted <- store.DeleteSpace(ctx, "main") }()

	assert.Eventually(t, func() bool {
		var waiting bool

		_ = store.db.QueryRowContext(ctx, `SELECT count(*) > 0 FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%web_spaces%'`).Scan(&waiting)

		return waiting || len(deleted) > 0
	}, 5*time.Second, time.Millisecond)
	require.NoError(t, tx.Commit())
	require.ErrorIs(t, <-deleted, ErrSpaceRejected)
	assert.Equal(t, []Space{{ID: "main", Name: "Main"}}, spaces())

	state, revision, err := store.TabState(ctx, "alice")
	require.NoError(t, err)
	assert.Zero(t, state)
	assert.Zero(t, revision)

	save := func(space string, revision int64) bool {
		t.Helper()

		saved, err := store.SaveTabState(ctx, "alice", &TabState{Space: space}, revision)
		require.NoError(t, err)

		return saved
	}
	assert.True(t, save("first", 0))
	assert.False(t, save("stale", 0), "a save made from an older version is refused")
	assert.True(t, save("second", 1))

	state, revision, err = store.TabState(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, TabState{Space: "second", Last: map[string]string{}, SharedOrder: []string{}}, state)
	assert.Equal(t, int64(2), revision)

	last, order := map[string]string{"main": "/cron", "ops": "/cron", "dev": "/search"}, []string{"/search", "/cron"}
	saved, err := store.SaveTabState(ctx, "alice", &TabState{Tabs: []Tab{{Path: "/cron", Space: "ops"}}, Space: "main", Last: last, SharedOrder: order}, revision)
	require.NoError(t, err)
	require.True(t, saved)

	state, _, err = store.TabState(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, TabState{Tabs: []Tab{{Path: "/cron", Space: "ops"}}, Space: "main", Last: last, SharedOrder: order}, state, "every space keeps its own last tab")

	page := &SearchPage{Name: "Triage", Searches: []SavedSearch{{ID: "1", Query: "is:unread"}, {ID: "1", Name: "Mine", Query: "principal:alice"}}, Active: "1"}
	for i, tab := range []Tab{{Path: "/search/a", Space: "main", Search: page}, {Path: "/search/a", Group: "g", Space: "ops", Search: page}} {
		saved, err = store.SaveTabState(ctx, "alice", &TabState{Tabs: []Tab{{Path: "/cron", Space: "ops"}, tab}, Space: "main"}, int64(3+i))
		require.NoError(t, err)
		require.True(t, saved)

		state, _, err = store.TabState(ctx, "alice")
		require.NoError(t, err)
		assert.Equal(t, []Tab{{Path: "/cron", Space: "ops"}, tab}, state.Tabs)
		assertNoOrphanSearchPages(t, store)
	}

	saved, err = store.SaveTabState(ctx, "alice", &TabState{Tabs: []Tab{{Path: "/search/a", Space: "main"}}, Space: "main"}, 5)
	require.NoError(t, err)
	require.True(t, saved)

	state, _, err = store.TabState(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, []Tab{{Path: "/search/a", Space: "main", Search: page}}, state.Tabs)
	assertNoOrphanSearchPages(t, store)

	state, revision, err = store.TabState(ctx, "bob")
	require.NoError(t, err)
	assert.Zero(t, state, "each person has their own state")
	assert.Zero(t, revision)
}

func TestWebSharedTabs(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, store, "main", map[string]ConversationKind{"a": KindWebChat})
	require.NoError(t, store.CreateSpace(ctx, "Ops"))
	require.NoError(t, store.CreateSpace(ctx, "Dev"))

	spaces, err := store.Spaces(ctx)
	require.NoError(t, err)

	ops, dev := spaces[1].ID, spaces[2].ID
	share := func(space, path string) {
		t.Helper()
		require.NoError(t, store.ShareTab(ctx, space, path, nil))
	}
	shared := func() []Tab {
		t.Helper()

		tabs, err := store.SharedTabs(ctx)
		require.NoError(t, err)

		return tabs
	}

	share("main", "/search")
	share("main", "/s/a")
	share("main", "/search")
	share("main", "/s/bm9uZQ")
	share("gone", "/cron")
	assert.Equal(t, []Tab{{Space: "main", Path: "/search"}, {Space: "main", Path: "/s/YQ"}}, shared(),
		"oldest first; a legacy spelling is stored canonical; sharing again, a missing session, or a missing space changes nothing")

	share(ops, "/cron")
	share(ops, "/s/YQ")
	share(dev, "/agents")

	unshare := func(space, path string) *SearchPage {
		t.Helper()

		page, err := store.UnshareTab(ctx, space, path)
		require.NoError(t, err)

		return page
	}
	assert.Nil(t, unshare("main", "/s/YQ"))
	assert.Nil(t, unshare("main", "/pages"))
	assert.Equal(t, []Tab{{Space: "main", Path: "/search"}, {Space: ops, Path: "/cron"}, {Space: ops, Path: "/s/YQ"}, {Space: dev, Path: "/agents"}}, shared(),
		"removing a tab from one space leaves it in another; removing one not shared changes nothing")

	share("main", "/s/YQ")
	require.NoError(t, store.DeleteSpace(ctx, ops))
	assert.Equal(t, []Tab{{Space: "main", Path: "/search"}, {Space: "main", Path: "/cron"}, {Space: dev, Path: "/agents"}, {Space: "main", Path: "/s/YQ"}}, shared(),
		"a deleted space's tabs move to the first space, keeping when they were added; one shared there already stays once")

	share(dev, "/s/YQ")

	_, err = store.db.ExecContext(ctx, `DELETE FROM conversations WHERE conversation_id = 'a'`)
	require.NoError(t, err)
	require.NoError(t, store.DeleteSpace(ctx, dev))
	require.ErrorIs(t, store.DeleteSpace(ctx, "main"), ErrSpaceRejected)
	assert.Equal(t, []Tab{{Space: "main", Path: "/search"}, {Space: "main", Path: "/cron"}, {Space: "main", Path: "/agents"}}, shared(),
		"deleting a session takes it out of every space; the last space keeps its tabs")

	require.NoError(t, store.CreateSpace(ctx, "Docs"))

	spaces, err = store.Spaces(ctx)
	require.NoError(t, err)

	docs := spaces[1].ID
	first := &SearchPage{Name: "Triage", Searches: []SavedSearch{{ID: "1", Query: "is:unread"}, {ID: "1", Query: "tag:bug"}}, Active: "1"}
	second, mine := &SearchPage{Name: "Deploys", Searches: []SavedSearch{{ID: "2", Name: "Mine", Query: "principal:alice"}}}, &SearchPage{Name: "Mine"}
	page := func(space, path string) *SearchPage {
		t.Helper()

		tabs := shared()
		index := slices.IndexFunc(tabs, func(tab Tab) bool { return tab.Space == space && tab.Path == path })
		require.GreaterOrEqual(t, index, 0)

		return tabs[index].Search
	}

	require.NoError(t, store.ShareTab(ctx, docs, "/search/x", first))
	assert.Equal(t, first, page(docs, "/search/x"))
	require.NoError(t, store.ShareTab(ctx, docs, "/search/x", second))
	require.NoError(t, store.ShareTab(ctx, "gone", "/search/x", second))
	require.NoError(t, store.ShareTab(ctx, docs, "/s/bm9uZQ", second))
	assert.Equal(t, first, page(docs, "/search/x"), "sharing again, into a missing space, or a missing session stores no page")
	assertNoOrphanSearchPages(t, store)

	edit := func(space, path string, base, search *SearchPage) bool {
		t.Helper()

		edited, err := store.EditSharedSearch(ctx, space, path, base, search)
		require.NoError(t, err)

		return edited
	}
	assert.False(t, edit(docs, "/search/x", second, second), "an edit made from a page that has since changed is refused")
	assert.Equal(t, first, page(docs, "/search/x"))
	assert.True(t, edit(docs, "/search/x", first, second))
	assert.Equal(t, second, page(docs, "/search/x"))
	assert.False(t, edit(docs, "/search/y", nil, second))
	assert.False(t, slices.ContainsFunc(shared(), func(tab Tab) bool { return tab.Path == "/search/y" }))
	assertNoOrphanSearchPages(t, store)

	require.NoError(t, store.ShareTab(ctx, "main", "/search/z", mine))
	require.NoError(t, store.ShareTab(ctx, docs, "/search/z", first))
	require.NoError(t, store.DeleteSpace(ctx, docs))
	assert.Equal(t, []Tab{{Space: "main", Path: "/search"}, {Space: "main", Path: "/cron"}, {Space: "main", Path: "/agents"}, {Space: "main", Path: "/search/x", Search: second}, {Space: "main", Path: "/search/z", Search: mine}}, shared(),
		"a deleted space's Search pages move with their tabs, keeping when they were added; one shared there already keeps its own page")
	assertNoOrphanSearchPages(t, store)

	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	var holder int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_backend_pid() FROM web_shared_tabs WHERE space = 'main' AND path = '/search/x' FOR UPDATE`).Scan(&holder))

	results := make(chan bool, 2)

	for _, search := range []*SearchPage{{Name: "A"}, {Name: "B"}} {
		go func() {
			edited, err := store.EditSharedSearch(ctx, "main", "/search/x", second, search)
			assert.NoError(t, err)

			results <- edited
		}()
	}

	assert.Eventually(t, func() bool {
		var waiting int

		_ = store.db.QueryRowContext(ctx, `WITH RECURSIVE waiting AS (
    SELECT $1::integer AS pid
    UNION SELECT a.pid FROM pg_stat_activity a JOIN waiting w ON w.pid = ANY(pg_blocking_pids(a.pid))
)
SELECT count(*) - 1 FROM waiting`, holder).Scan(&waiting)

		return waiting == 2
	}, 5*time.Second, time.Millisecond)
	require.NoError(t, tx.Commit())
	assert.ElementsMatch(t, []bool{true, false}, []bool{<-results, <-results})

	saved := page("main", "/search/x")
	assert.Contains(t, []string{"A", "B"}, saved.Name)
	assert.Equal(t, saved, unshare("main", "/search/x"), "taking it out of Shared returns the page it held")
	assert.Nil(t, shared()[0].Search, "other tabs have no page")
	assertNoOrphanSearchPages(t, store)
}

func assertNoOrphanSearchPages(t *testing.T, store *SessionService) {
	t.Helper()

	orphans, err := queryStrings(t.Context(), store.db, `WITH owned AS (
    SELECT search_page FROM web_tabs UNION ALL SELECT search_page FROM web_shared_tabs UNION ALL SELECT search_page FROM web_closed_tabs
)
SELECT p.id::text FROM web_search_pages p LEFT JOIN owned o ON o.search_page = p.id WHERE o.search_page IS NULL`, "orphan search pages")
	require.NoError(t, err)
	assert.Empty(t, orphans)
}

func TestWebClosedTabs(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, store, "main", map[string]ConversationKind{"a": KindWebChat, "b": KindWebChat})

	revisions := map[string]int64{}
	save := func(username string, tabs ...Tab) {
		t.Helper()

		saved, err := store.SaveTabState(ctx, username, &TabState{Tabs: tabs, Space: "main"}, revisions[username])
		require.NoError(t, err)
		require.True(t, saved)

		revisions[username]++
	}
	closed := func(username string) []string {
		t.Helper()

		tabs, err := store.ClosedTabs(ctx, username)
		require.NoError(t, err)

		out := make([]string, 0, len(tabs))
		for _, tab := range tabs {
			out = append(out, fmt.Sprint(tab.Path, " ", tab.Space, " ", tab.Search != nil))
		}

		return out
	}

	page := &SearchPage{Name: "Triage", Searches: []SavedSearch{{ID: "1", Query: "is:unread"}}}
	a, b, cron, search := Tab{Path: "/s/YQ", Space: "main"}, Tab{Path: "/s/Yg", Space: "ops"}, Tab{Path: "/cron", Space: "main"}, Tab{Path: "/search/x", Space: "main", Search: page}
	save("alice", Tab{Path: "/", Space: "main"}, Tab{Path: "group:g", Group: "g", Space: "main"}, a, b, search, cron)
	save("alice", Tab{Path: "/s/a", Space: "main"}, b, search)
	save("alice", Tab{Path: "/s/a", Space: "main"}, b)
	assert.Equal(t, []string{"/search/x main true", "/cron main false"}, closed("alice"),
		"newest closed first, keeping space and page; New session, a kept group's placeholder, and a tab saved under another spelling are not closed")

	tabs, err := store.ClosedTabs(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, search, tabs[0])
	assertNoOrphanSearchPages(t, store)

	saved, err := store.SaveTabState(ctx, "alice", &TabState{Space: "main"}, 0)
	require.NoError(t, err)
	require.False(t, saved)
	assert.Equal(t, []string{"/search/x main true", "/cron main false"}, closed("alice"), "a refused save closes nothing")
	save("alice", a, cron)
	assert.Equal(t, []string{"/s/Yg ops false", "/search/x main true"}, closed("alice"), "a reopened tab is no longer closed")
	assert.Empty(t, closed("bob"), "each person's closed tabs are their own")

	save("alice", a, cron, search)

	state, _, err := store.TabState(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, []Tab{a, cron, search}, state.Tabs)
	assert.Equal(t, []string{"/s/Yg ops false"}, closed("alice"))
	save("alice", a, cron)
	assert.Equal(t, []string{"/search/x main true", "/s/Yg ops false"}, closed("alice"))
	assertNoOrphanSearchPages(t, store)

	_, err = store.db.ExecContext(ctx, `DELETE FROM conversations WHERE conversation_id = 'b'`)
	require.NoError(t, err)
	assert.Equal(t, []string{"/search/x main true"}, closed("alice"), "deleting a session deletes its closed tab")

	pages := make([]Tab, 50)
	for i := range pages {
		pages[i] = Tab{Path: fmt.Sprint("/pages/main/agent/", i), Space: "main"}
	}

	soon := time.Now().Add(time.Hour)
	require.NoError(t, store.ChangeAttention(ctx, "alice", []string{"/cron"}, AttentionPark, &soon))
	save("alice", pages...)
	save("alice")

	got := closed("alice")
	assert.Len(t, got, 51, "only the newest 50 stay, and a tab with a timer")
	assert.NotContains(t, got, "/search/x main true")
	assert.Contains(t, got, "/cron main false")

	rows, err := queryStrings(ctx, store.db, `SELECT 'page' FROM web_search_pages UNION ALL SELECT 'search' FROM web_searches`, "search rows")
	require.NoError(t, err)
	assert.Empty(t, rows, "a closed tab's page and searches go with it")
}

func TestConversationKindsAtCreation(t *testing.T) {
	t.Parallel()

	store, _ := newShadowTestStore(t)
	ctx := t.Context()
	runtime := &Runtime{Sessions: store}
	run, oneOff := "cron:cron/daily.md:20261019T120000.000000001Z:RUN", "one-off-cron:cron/once.md:20261019T130000.000000001Z:ONE"
	slack := "slack-channel:C1:main"

	require.NoError(t, runtime.CreateConversation(ctx, protocol.Conversation{ID: "web", Agent: "main", CreatedBy: "alice"}))
	require.NoError(t, runtime.CreateConversation(ctx, protocol.Conversation{ID: run, Agent: "job", CreatedBy: "cron", Run: &protocol.CronRun{Path: "cron/daily.md"}}))
	require.NoError(t, runtime.CreateConversation(ctx, protocol.Conversation{ID: oneOff, Agent: "job", CreatedBy: "cron", Run: &protocol.CronRun{Path: "cron/once.md", OneOff: true}}))
	require.NoError(t, runtime.CreateConversation(ctx, protocol.Conversation{ID: "web:" + run, Agent: "main", CreatedBy: "alice"}))
	_, err := runtime.ContinueRun(ctx, run, "web:"+run, false)
	require.NoError(t, err)
	require.NoError(t, store.RegisterExternalMCPConversation("ticket", "main", &ExternalMCPSessionState{Agent: "job", PrivateConversationID: "private", ManagedConversationID: "mcp-chat"}))
	require.NoError(t, store.startSubagent(ctx, "web", &harness.SubagentWrite{Key: harness.ChildKey{Path: "/call-1", Turn: "turn-1", Call: "call-1"}, Agent: "helper"}))

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
		"mcp-chat": "web_chat listed record", "private": "external_mcp_run hidden record", slack: "slack_thread listed record",
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

	for row, err := range store.sidebarSessions(ctx, "", nil) {
		require.NoError(t, err)

		listed = append(listed, row.Conversation.ID)
	}

	require.ElementsMatch(t, []string{"web", "web:" + run, "mcp-chat", slack}, listed)

	for _, id := range ids {
		_, eligible, _, err := store.RevertState(ctx, id)
		require.NoError(t, err)
		require.Equal(t, slices.Contains(listed, id) || slices.Contains(shadows, id), eligible, id)
	}
}
