package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"cirello.io/pglock"
	migrate "github.com/rubenv/sql-migrate"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
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

	stats, err := store.PruneStateBefore(ctx, time.Now().Add(-time.Hour))
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
	store := newTestSessionService(t)
	ctx := t.Context()
	group := []string{"triage", "resolved"}

	for _, id := range []string{"owning", "other"} {
		_, err := store.toggleSessionTag(ctx, id, "customer", []string{"customer"})
		require.NoError(t, err)
	}

	_, err := store.toggleSessionTag(ctx, "owning", "triage", group)
	require.NoError(t, err)
	_, err = store.toggleSessionTag(ctx, "other", "resolved", group)
	require.NoError(t, err)

	// A deferred constraint fails at commit, after the replacement write succeeds.
	_, err = store.db.ExecContext(ctx, `ALTER TABLE session_tags ADD CONSTRAINT reject_duplicate_tags UNIQUE (tags) DEFERRABLE INITIALLY DEFERRED`)
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
	// Existing history, even before the epoch, takes precedence over the tag-only fallback.
	_, err := store.AppendEntryID(ctx, "external_mcp:pre-epoch", testSessionEntryAt(time.Unix(-20, 0).UTC(), "old"))
	require.NoError(t, err)
	_, err = store.toggleSessionTag(ctx, "external_mcp:pre-epoch", "customer", []string{"customer"})
	require.NoError(t, err)
	_, err = store.PruneStateBefore(ctx, time.Unix(-10, 0).UTC())
	require.NoError(t, err)
	tags, err := sessionTags(ctx, store.db, "external_mcp:pre-epoch")
	require.NoError(t, err)
	require.Empty(t, tags)

	cutoff := time.Unix(1_700_000_000, 0).UTC()

	ids := []string{"external_mcp:orphan", "cron:recent", "external_mcp:queued", "external_mcp:bound", "visible"}
	for _, id := range ids {
		_, err := store.toggleSessionTag(ctx, id, "customer", []string{"customer"})
		require.NoError(t, err)
	}

	_, err = store.AppendEntryID(ctx, ids[1], testSessionEntryAt(cutoff.Add(time.Hour), "recent"))
	require.NoError(t, err)

	item := protocol.ThreadQueueItem{ID: "tags-queue", ConversationID: ids[2], Message: "waiting"}
	require.NoError(t, store.PutThreadQueueItem(item.ID, &item))
	require.NoError(t, store.RegisterExternalMCPConversation("tags-bound", "main", &ExternalMCPSessionState{Agent: "main", PrivateConversationID: ids[3], ManagedConversationID: ids[4]}))
	_, err = store.AppendEntryID(ctx, ids[4], testSessionEntryAt(cutoff.Add(time.Hour), "recent"))
	require.NoError(t, err)
	_, err = store.PruneStateBefore(ctx, cutoff)
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

func listSessions(ctx context.Context, workspace string, conversationIDs ...string) ([]protocol.SessionSummary, error) {
	service, err := NewSessionServiceIn(ctx, &config.Config{DatabaseURL: testStoreDSN(workspace), Workspace: workspace}, slog.New(slog.DiscardHandler))
	if err != nil {
		return nil, err
	}
	defer func() { _ = service.Stop() }()

	return service.ListSessions(ctx, conversationIDs)
}

func TestAttachmentMetadata(t *testing.T) {
	service := newTestSessionService(t)
	attachment := protocol.OutboundAttachment{ID: "snapshot", Name: "report.txt", MIMEType: "text/plain", Data: []byte(strings.Repeat("original", 700000)), OriginalUnverified: true}
	require.NoError(t, service.SaveAttachment(t.Context(), "producer", &attachment, false))

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

func TestSessionStoreAppendAndLoad(t *testing.T) {
	service := newTestSessionService(t)
	store := newSessionStore("slack-thread:C123:111.222", service)
	entry := testSessionEntry("hello", "hi")

	id, err := store.outID(*entry)
	require.NoError(t, err)
	assert.Positive(t, id)
	require.Equal(t, []harness.SessionEntry{*entry}, collectEntries(t, store.in()))

	for got, err := range store.in() {
		require.NoError(t, err)
		assert.Equal(t, *entry, got)

		break
	}
}

func TestSessionServiceAppendEntryIDAndObserveEntries(t *testing.T) {
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
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT pg_typeof(entry_json)::text FROM session_entries WHERE id = $1`, id1).Scan(&columnType))
	require.Equal(t, "json", columnType)

	for _, raw := range []string{`{`, `{"text":"\u0000"}`, `{"text":"\ud800"}`, `{"text":"\udc00"}`} {
		_, err := service.db.ExecContext(t.Context(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('invalid', $1, '')`, raw)
		require.Error(t, err)
	}
}

func TestSessionServiceTurnPairAllowsOnlyOneActiveTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		unlockFirst, err := service.lockTurnPair(t.Context(), "slack-thread:C1:1.1", "external_mcp:private")
		require.NoError(t, err)

		secondAcquired := false

		var errSecond error

		go func() {
			var unlockSecond func()

			unlockSecond, errSecond = service.lockTurnPair(t.Context(), "slack-thread:C1:1.1", "slack-thread:C1:1.1")
			if errSecond != nil {
				return
			}

			secondAcquired = true

			unlockSecond()
		}()

		synctest.Wait()
		assert.False(t, secondAcquired)

		unlockFirst()
		synctest.Wait()
		require.NoError(t, errSecond)
		assert.True(t, secondAcquired)
	})
}

func TestSessionServiceTurnPairReservationPrioritizesPrivateTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		pairID, privateID := "slack-thread:C1:1.1", "external_mcp:private"
		service.reserveTurnPair(pairID, privateID)

		managedAcquired := false

		go func() {
			unlock, err := service.lockTurnPair(t.Context(), pairID, pairID)
			if err != nil {
				return
			}

			managedAcquired = true

			unlock()
		}()

		synctest.Wait()
		assert.False(t, managedAcquired)

		privateAcquired := false

		go func() {
			unlock, err := service.lockTurnPair(t.Context(), pairID, privateID)
			if err != nil {
				return
			}

			privateAcquired = true

			unlock()
		}()

		synctest.Wait()
		assert.True(t, privateAcquired)
		assert.False(t, managedAcquired)

		service.completeTurnPairReservation(pairID, privateID)
		synctest.Wait()
		assert.True(t, managedAcquired)
	})
}

func TestSessionServiceScheduledMessages(t *testing.T) {
	store := newTestSessionService(t)
	dueAt := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)

	require.NoError(t, store.PutScheduledMessage("schedule-1", &protocol.ScheduledMessageState{ConversationID: "slack-thread:D123:111.222", Agent: "helper", Message: "later", DueAt: dueAt}))

	messages, err := store.ScheduledMessages()
	require.NoError(t, err)
	assert.Equal(t, map[string]protocol.ScheduledMessageState{"schedule-1": {ConversationID: "slack-thread:D123:111.222", Agent: "helper", Message: "later", DueAt: dueAt}}, messages)
}

func TestLegacyProducerScheduleWaitsForResetHandoff(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, store.UpsertThread("producer", ThreadState{Agent: "main"}))

	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "produce", false)
	inbound.SyncDestination = "owner"
	require.NoError(t, startTurnDB(ctx, store.db, "active", "producer", inbound))
	require.NoError(t, store.PutScheduledMessage("legacy", &protocol.ScheduledMessageState{ConversationID: "producer", Agent: "main", Message: "legacy", DueAt: time.Now().Add(time.Hour)}))
	id, err := store.AppendEntryID(ctx, "producer", &harness.SessionEntry{Version: 1, Type: producerResetEntryType})
	require.NoError(t, err)

	dao := stateDAO{db: store.db}
	_, err = dao.projectProducerEffects(ctx, "producer", "owner", "selected")
	require.NoError(t, err)
	_, through, err := dao.producer(ctx, "producer")
	require.NoError(t, err)
	assert.Zero(t, through, "early Sync cannot consume a reset before the legacy row can move")
	endTestTurn(t, store, "producer", "active", "")

	schedules, err := dao.projectProducerEffects(ctx, "producer", "owner", "selected")
	require.NoError(t, err)
	assert.Empty(t, schedules)

	messages, err := store.ScheduledMessages()
	require.NoError(t, err)
	assert.Empty(t, messages, "the resumed reset cancels the rehomed legacy row")

	_, through, err = dao.producer(ctx, "producer")
	require.NoError(t, err)
	assert.Equal(t, id, through)
}

func TestProducerRoutingAndEffectsSurviveCompletion(t *testing.T) {
	for _, destination := range []string{"owner", ""} {
		t.Run("destination="+destination, func(t *testing.T) {
			workspace := t.TempDir()
			store := newTestSessionServiceAt(t, workspace)
			ctx := t.Context()
			require.NoError(t, store.UpsertThread("producer", ThreadState{Agent: "main"}))

			inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "produce", false)
			inbound.ConversationID, inbound.SyncDestination = "producer", destination
			inbound.RequireOutputDecision = destination == ""
			inbound.Metadata = map[string]string{"origin": "explicit"}
			require.NoError(t, startTurnDB(ctx, store.db, "first", "producer", inbound))
			id, err := store.AppendEntryID(ctx, "producer", &harness.SessionEntry{Version: 1, Type: producerResetEntryType})
			require.NoError(t, err)
			endTestTurn(t, store, "producer", "first", "")
			require.NoError(t, store.Stop())
			store = newTestSessionServiceAt(t, workspace)
			dao := stateDAO{db: store.db}
			routing, through, err := dao.producer(ctx, "producer")
			require.NoError(t, err)
			require.NotNil(t, routing)
			require.Equal(t, destination, routing.SyncDestination)
			require.Equal(t, inbound.RequireOutputDecision, routing.RequireOutputDecision)
			require.Equal(t, inbound.Metadata, routing.Metadata)
			require.Zero(t, through)

			ids, err := store.pendingProducerIDs(ctx)
			require.NoError(t, err)
			require.Equal(t, []string{"producer"}, ids)

			effects, err := dao.producerEffects(ctx, "producer", through)
			require.NoError(t, err)
			require.Len(t, effects, 1)
			require.Equal(t, id, effects[0].ID)

			owner, err := dao.bindProducer(ctx, "producer", "owner")
			require.NoError(t, err)
			require.Equal(t, "owner", owner)

			// A later activation and a competing authorized bind cannot replace the owner.
			inbound.SyncDestination = "other"
			require.NoError(t, startTurnDB(ctx, store.db, "second", "producer", inbound))
			owner, err = dao.bindProducer(ctx, "producer", "third")
			require.NoError(t, err)
			require.Equal(t, "owner", owner)

			routing, _, err = dao.producer(ctx, "producer")
			require.NoError(t, err)
			require.Equal(t, "owner", routing.SyncDestination)

			ids, err = store.pendingProducerIDs(ctx)
			require.NoError(t, err)
			require.Empty(t, ids, "unfinished producers are not ready for handoff")
			endTestTurn(t, store, "producer", "second", "")

			require.NoError(t, dao.advanceProducerEffects(ctx, "producer", id))
			ids, err = store.pendingProducerIDs(ctx)
			require.NoError(t, err)
			require.Empty(t, ids)

			effects, err = dao.producerEffects(ctx, "producer", id)
			require.NoError(t, err)
			require.Empty(t, effects)
			require.NoError(t, store.Stop())
			store = newTestSessionServiceAt(t, workspace)
			routing, through, err = (stateDAO{db: store.db}).producer(ctx, "producer")
			require.NoError(t, err)
			require.Equal(t, "owner", routing.SyncDestination)
			require.Equal(t, id, through)
		})
	}
}

func TestSessionServiceThreadQueuePersistsOrderAndParkAfter(t *testing.T) {
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

	require.NoError(t, (stateDAO{db: store.db}).deleteScheduledMessage(t.Context(), "missing"))
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
	store := newTestSessionService(t)
	// Recreate the pre-006 queue in this test's isolated database schema.
	_, err := store.db.ExecContext(t.Context(), `ALTER TABLE thread_queue DROP COLUMN IF EXISTS kind; DELETE FROM pg_migrations WHERE id = '006_thread_queue_kind.sql'; INSERT INTO thread_queue (queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position) VALUES ('old', 'recorded', 'original content', 'original author', 1, 0)`)
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

func TestSessionServiceInitializesCronScheduleSchema(t *testing.T) {
	store := newTestSessionService(t)

	rows, err := store.db.QueryContext(context.Background(), `SELECT tablename FROM pg_tables WHERE schemaname = current_schema() AND tablename LIKE 'cron_%' UNION SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND indexname LIKE 'cron_%' ORDER BY 1`)

	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var names []string

	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}

	require.NoError(t, rows.Err())

	assert.Equal(t, []string{"cron_schedule_runs", "cron_schedule_runs_pkey", "cron_schedule_runs_running_path", "cron_schedules", "cron_schedules_next_due_id", "cron_schedules_pkey", "cron_schedules_relative_path"}, names)
}

func TestSessionServiceInitializesActiveTurnIndexes(t *testing.T) {
	store := newTestSessionService(t)

	rows, err := store.db.QueryContext(context.Background(), `SELECT tablename FROM pg_tables WHERE schemaname = current_schema() AND tablename LIKE 'active_turns%' UNION SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND indexname LIKE 'active_turns%' ORDER BY 1`)

	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var names []string

	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}

	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"active_turns", "active_turns_conversation_updated", "active_turns_pkey"}, names)
}

func TestInitializeSessionDBReportsMigrationError(t *testing.T) {
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
	workspace := t.TempDir()
	first := newTestSessionServiceAt(t, workspace)

	var n int
	require.NoError(t, first.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pg_migrations`).Scan(&n))
	assert.Equal(t, 32, n)
	require.Error(t, first.db.QueryRowContext(t.Context(), `SELECT 1 FROM store_bootstrap`).Scan(&n))

	second, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: testStoreDSN(workspace), Workspace: workspace}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Stop()) })
	require.NoError(t, second.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pg_migrations`).Scan(&n))
	assert.Equal(t, 32, n)
}

func TestInitializeSessionDBUpgradesMainSchema(t *testing.T) {
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

				for range 2 {
					require.NoError(t, initializeSessionDB(t.Context(), db, slog.New(slog.DiscardHandler)))
				}

				var count int
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_migrations`).Scan(&count))
				require.Equal(t, 32, count)
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_migrations WHERE applied_at='2026-01-01Z'`).Scan(&count))
				require.Equal(t, prefix, count)
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM session_tags`).Scan(&count))
				require.Zero(t, count)

				for _, query := range []string{
					`SELECT count(*) FROM managed_conversations WHERE conversation_id='synthetic' AND agent='main' AND created_by='owner' AND NOT settled AND NOT pinned AND snoozed_until IS NULL AND name='' AND forked_from='' AND settled_override AND bumped_at_unix_ns=123`,
					`SELECT count(*) FROM session_entries WHERE conversation_id='synthetic' AND entry_json::text='{"text":"synthetichistory","literal":"\\u0000","mixed":"\\","nested":[""],"key":"value"}' AND entry_timestamp='2026-09-09T12:00:00.123456Z'`,
					`SELECT count(*) FROM thread_queue WHERE queue_item_id='q' AND message='queued' AND principal='owner' AND stash_at_unix_ns=456 AND position=7 AND content='{}'`,
				} {
					require.NoError(t, db.QueryRowContext(t.Context(), query).Scan(&count))
					require.Equal(t, 1, count)
				}

				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM active_turns`).Scan(&count))
				require.Zero(t, count, "the clean cutover drops in-flight turns from the old version")

				if prefix >= 16 {
					require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM session_summaries WHERE conversation_id = 'synthetic'`).Scan(&count))
					require.Zero(t, count)
					require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM session_summaries WHERE conversation_id = 'clean' AND preview = 'valid'::bytea`).Scan(&count))
					require.Equal(t, 1, count)
				}

				var kind, defaultKind string
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT kind FROM thread_queue WHERE queue_item_id='q'`).Scan(&kind))
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT column_default FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='thread_queue' AND column_name='kind'`).Scan(&defaultKind))

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
	workspace := t.TempDir()
	first := newTestSessionServiceAt(t, workspace)
	_, err := first.db.ExecContext(t.Context(), `ALTER TABLE pg_migrations RENAME TO gorp_migrations`)
	require.NoError(t, err)

	second := newTestSessionServiceAt(t, workspace)

	var n int
	require.NoError(t, second.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pg_migrations`).Scan(&n))
	assert.Equal(t, 32, n)
	require.Error(t, second.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM gorp_migrations`).Scan(&n))
}

func TestSlackChannelFacts(t *testing.T) {
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

	require.NoError(t, store.UpsertThread(protocol.SlackThreadConversationID("G1", "1.1"), ThreadState{Agent: "main"}))
	require.NoError(t, store.UpsertThread(protocol.SlackThreadConversationID("G1", "2.2"), ThreadState{Agent: "main"}))
	require.NoError(t, store.UpsertThread("web-session", ThreadState{Agent: "main"}))
	require.NoError(t, store.RecordChannelFact(ctx, "T2", "Cother", "other", at))
	ids, err := store.SlackChannelIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"G1"}, ids)
}

func TestSessionServiceSyncCronSchedulesInsertsUpdatesAndDeletes(t *testing.T) {
	store := newTestSessionService(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	due := now.Add(time.Minute)

	require.NoError(t, store.SyncCronSchedules([]CronScheduleState{
		{ScheduleID: "daily#0", RelativePath: "daily.md", NextDue: due},
		{ScheduleID: "daily#1", RelativePath: "daily.md", NextDue: due},
		{ScheduleID: "weekly#0", RelativePath: "weekly.md", NextDue: due.Add(time.Hour)},
		{ScheduleID: "running#0", RelativePath: "running.md", NextDue: now},
	}, now))
	_, claimed, err := store.ClaimCronSchedule(CronScheduleState{ScheduleID: "running#0", RelativePath: "running.md", NextDue: now}, due, now)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, store.SyncCronSchedules([]CronScheduleState{
		{ScheduleID: "daily#0", RelativePath: "daily.md", NextDue: due.Add(2 * time.Hour)},
	}, now.Add(time.Second)))

	rows, err := store.db.QueryContext(context.Background(), `SELECT schedule_id, relative_path, next_due_unix_ns FROM cron_schedules ORDER BY schedule_id`)

	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var schedules []CronScheduleState

	for rows.Next() {
		schedule, err := scanCronSchedule(rows)
		require.NoError(t, err)

		schedules = append(schedules, schedule)
	}

	require.NoError(t, rows.Err())

	assert.Equal(t, []CronScheduleState{{ScheduleID: "daily#0", RelativePath: "daily.md", NextDue: due}}, schedules)
	paths, err := queryStrings(t.Context(), store.db, `SELECT relative_path FROM cron_schedule_runs ORDER BY relative_path`, "cron run paths")
	require.NoError(t, err)
	assert.Equal(t, []string{"daily.md", "running.md"}, paths)
	require.NoError(t, store.CompleteCronRun("running.md", now))

	require.NoError(t, store.SyncCronSchedules([]CronScheduleState{{ScheduleID: "empty#0", RelativePath: "empty.md"}}, now))

	var nextDue int64
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT next_due_unix_ns FROM cron_schedules WHERE schedule_id = 'empty#0'`).Scan(&nextDue))
	assert.Equal(t, int64(0), nextDue)
	paths, err = queryStrings(t.Context(), store.db, `SELECT relative_path FROM cron_schedule_runs ORDER BY relative_path`, "cron run paths")
	require.NoError(t, err)
	assert.Equal(t, []string{"empty.md"}, paths)

	dueSchedules, err := store.DueCronSchedules(now)
	require.NoError(t, err)
	assert.Contains(t, dueSchedules, CronScheduleState{ScheduleID: "empty#0", RelativePath: "empty.md"})
}

func TestSessionServiceDueCronSchedulesHonorsDueTime(t *testing.T) {
	store := newTestSessionService(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)

	require.NoError(t, store.SyncCronSchedules([]CronScheduleState{
		{ScheduleID: "second", RelativePath: "second.md", NextDue: now},
		{ScheduleID: "first", RelativePath: "first.md", NextDue: now.Add(-time.Minute)},
		{ScheduleID: "later", RelativePath: "later.md", NextDue: now.Add(time.Minute)},
	}, now))

	due, err := store.DueCronSchedules(now)
	require.NoError(t, err)
	assert.Equal(t, []CronScheduleState{
		{ScheduleID: "first", RelativePath: "first.md", NextDue: now.Add(-time.Minute)},
		{ScheduleID: "second", RelativePath: "second.md", NextDue: now},
	}, due)
}

func TestSessionServiceClaimCronScheduleSerializesSamePathAndCompletionClearsRunning(t *testing.T) {
	store := newTestSessionService(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	next := now.Add(time.Hour)

	dueDaily := CronScheduleState{ScheduleID: "daily#0", RelativePath: "daily.md", NextDue: now}
	dueDailySecond := CronScheduleState{ScheduleID: "daily#1", RelativePath: "daily.md", NextDue: now}
	require.NoError(t, store.SyncCronSchedules([]CronScheduleState{
		dueDaily,
		dueDailySecond,
	}, now))

	run, ok, err := store.ClaimCronSchedule(dueDaily, next, now)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "daily.md", run)

	run, ok, err = store.ClaimCronSchedule(dueDailySecond, next, now)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, run)

	due, err := store.DueCronSchedules(now)
	require.NoError(t, err)
	assert.Empty(t, due)

	_, ok, err = store.ClaimCronSchedule(dueDaily, next.Add(time.Hour), now)
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, store.CompleteCronRun("daily.md", now.Add(time.Minute)))

	dueDaily.NextDue = next
	run, ok, err = store.ClaimCronSchedule(dueDaily, next.Add(time.Hour), next)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "daily.md", run)
}

func TestSessionServiceActiveGoalThreads(t *testing.T) {
	store := newTestSessionService(t)
	threads, err := store.ActiveGoalThreads()
	require.NoError(t, err)
	assert.Nil(t, threads)

	for _, id := range []string{"active", "legacy", "terminal", "orphan"} {
		if id != "orphan" {
			require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))
		}

		require.NoError(t, store.BeginGoal(id, "work", "", 1))
	}

	_, err = store.db.ExecContext(t.Context(), `UPDATE conversation_goals SET status = '' WHERE conversation_id = 'legacy'`)
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
	store := newTestSessionService(t)

	require.NoError(t, store.BeginGoal("thread-1", " fix lint ", " ./scripts/check.sh --linter-mode ", 3))
	goal, ok, err := store.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "fix lint", goal.Objective)
	assert.Equal(t, "./scripts/check.sh --linter-mode", goal.CheckScript)
	assert.Equal(t, 3, goal.MaxTurns)

	require.NoError(t, store.BeginGoal("thread-2", "write docs", " ", 1))
	goal, ok, err = store.Goal("thread-2")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, goal.CheckScript)
}

func TestSessionServiceBeginGoalRejectsActiveGoal(t *testing.T) {
	store := newTestSessionService(t)

	require.NoError(t, store.BeginGoal("thread-1", "first", "", 3))
	err := store.BeginGoal("thread-1", "second", "", 3)
	require.ErrorIs(t, err, protocol.ErrGoalAlreadyActive)

	goal, ok, err := store.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "first", goal.Objective)
}

func TestSessionServiceBeginGoalAllowsGoalAfterTerminal(t *testing.T) {
	store := newTestSessionService(t)

	require.NoError(t, store.BeginGoal("thread-1", "first", "", 3))
	_, err := store.UpdateGoalStatus("thread-1", GoalStatusComplete, "done")
	require.NoError(t, err)
	require.NoError(t, store.BeginGoal("thread-1", "second", "./check.sh", 1))

	goal, ok, err := store.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "second", goal.Objective)
	assert.Equal(t, GoalStatusActive, goal.Status)
	assert.Equal(t, 0, goal.TurnsUsed)
}

func TestSessionServiceProgressGoalKeepsGoalActiveAndRecordsNote(t *testing.T) {
	store := newTestSessionService(t)

	require.NoError(t, store.BeginGoal("thread-1", "first", "", 3))
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
	service := newTestSessionService(t)
	store := newSessionStore("main", service)
	entry := harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), ResponseID: "", Model: "gpt-5.5", ReplayInput: testReplayInput(replayInputMessage{role: "user", text: strings.Repeat("x", 128*1024)})}

	_, err := store.outID(entry)
	require.NoError(t, err)
	require.Equal(t, []harness.SessionEntry{entry}, collectEntries(t, store.in()))
}

func TestAppendSessionEntryDBReportsWriteFailures(t *testing.T) {
	entry := &harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), ReplayInput: []json.RawMessage{json.RawMessage("{")}}
	_, err := appendSessionEntryDB(context.Background(), errStore{}, "main", "main", entry)
	require.ErrorContains(t, err, "marshal rocketcode session entry")

	entry.ReplayInput = nil
	_, err = appendSessionEntryDB(context.Background(), errStore{errExec: errors.New("no write")}, "main", "main", entry)
	require.ErrorContains(t, err, "lock session history")
}

func TestNewSessionServiceReportsInvalidDatabaseURL(t *testing.T) {
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
	service := newTestSessionService(t)
	store := newSessionStore("main", service)
	require.Empty(t, collectEntries(t, store.in()))
}

func TestSessionStoreReportsObserveError(t *testing.T) {
	service, err := NewSessionService(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, service.Stop())

	store := newSessionStore("main", service)

	var errObserve error
	for _, err := range store.in() {
		errObserve = err
		break
	}

	require.Error(t, errObserve)
	assert.ErrorContains(t, errObserve, "query rocketcode session entries")
}

func TestMemoryStoreAppendAndLoad(t *testing.T) {
	var store memoryStore

	entry := testSessionEntry("memory", "assistant")

	require.NoError(t, store.out(*entry))
	require.Equal(t, []harness.SessionEntry{*entry}, collectEntries(t, store.in()))

	for got, err := range store.in() {
		require.NoError(t, err)
		assert.Equal(t, *entry, got)

		break
	}
}

func TestSessionStoreRejectsNilEntry(t *testing.T) {
	_, err := AppendSessionEntryID(context.Background(), t.TempDir(), "main", nil)
	require.ErrorContains(t, err, "rocketcode session entry is required")
}

func TestAppendSessionEntryIDRejectsBlankConversationID(t *testing.T) {
	workspace := t.TempDir()
	entry := testSessionEntry("blank conversation", "assistant")

	_, err := AppendSessionEntryID(context.Background(), workspace, " \t ", entry)
	require.EqualError(t, err, "conversation ID is required")
}

func TestSessionInspectionMissingDBDoesNotCreateRuntimeDir(t *testing.T) {
	workspace := t.TempDir()

	summaries, err := listSessions(context.Background(), workspace)
	require.NoError(t, err)
	assert.Empty(t, summaries)

	service, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: testStoreDSN(workspace), Workspace: workspace}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	entries, err := service.ObserveEntries(context.Background(), "main")
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.NoDirExists(t, filepath.Join(workspace, ".rocketclaw"))
}

func TestListSessionsIncludesLastMessages(t *testing.T) {
	workspace := t.TempDir()

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

	summaries, err := listSessions(context.Background(), workspace, "slack-thread:D123:111.222", "main", "missing")
	require.NoError(t, err)
	require.Len(t, summaries, 2)

	assert.Equal(t, protocol.SessionSummary{ConversationID: "main", LastUpdated: summaries[0].LastUpdated, LastMessage: "assistant without a new user message"}, summaries[0])
	assert.Equal(t, last.Timestamp.Truncate(time.Microsecond), summaries[0].LastUpdated)
	assert.Equal(t, protocol.SessionSummary{ConversationID: "slack-thread:D123:111.222", LastUpdated: summaries[1].LastUpdated, LastMessage: "thread assistant"}, summaries[1])
}

func TestListSessionsMissingDBIsEmpty(t *testing.T) {
	summaries, err := listSessions(context.Background(), t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestSlackStateKeyTimeParsesAndRejectsKeys(t *testing.T) {
	got, ok := slackStateKeyTime("slack-thread:D123:1700000000.1234567899")
	require.True(t, ok)
	assert.Equal(t, time.Unix(1700000000, 123456789).UTC(), got)

	for _, key := range []string{
		"external-mcp:D123:1700000000.123456789",
		"slack-thread:D123:",
		"slack-thread:D123:not-seconds",
		"slack-thread:D123:1700000000.not-nanos",
	} {
		t.Run(key, func(t *testing.T) {
			got, ok := slackStateKeyTime(key)
			assert.False(t, ok)
			assert.True(t, got.IsZero())
		})
	}
}

func TestSessionServiceSerializesConcurrentAccess(t *testing.T) {
	service := newTestSessionService(t)

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
	summaries, err := service.ListSessions(t.Context(), []string{"main"})
	require.NoError(t, err)
	require.Equal(t, []protocol.SessionSummary{{ConversationID: "main", LastMessage: messages[len(messages)-1].text, LastUpdated: last.Timestamp.Truncate(time.Microsecond)}}, summaries)

	threadIDs, err := queryStrings(context.Background(), service.db, `SELECT conversation_id FROM managed_conversations ORDER BY conversation_id`, "managed conversation IDs")
	require.NoError(t, err)
	assert.Len(t, threadIDs, 25)
}

func TestSessionServiceBeginGoalRejectsConcurrentActiveStarts(t *testing.T) {
	service := newTestSessionService(t)

	errCh := make(chan error, 20)

	var group sync.WaitGroup

	for i := range 20 {
		group.Add(1)

		go func(i int) {
			defer group.Done()

			errCh <- service.BeginGoal("thread", fmt.Sprintf("goal %02d", i), "", 5)
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
	service := newTestSessionService(t)
	require.NoError(t, service.BeginGoal("thread", "ship it", "", 20))

	errCh := make(chan error, 20)

	var group sync.WaitGroup

	for range 20 {
		group.Go(func() {
			_, _, err := service.AccountGoalTurn("thread")
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
	service := newTestSessionService(t)
	require.NoError(t, service.UpsertThread("thread", ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, service.UpsertThread("thread", ThreadState{Agent: "main"}))

	thread, ok, err := service.Thread("thread")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ThreadState{Agent: "main", CreatedBy: ThreadCreatedByCron}, thread)
}

func TestDeleteSessionDeletesOnlyTarget(t *testing.T) {
	service := newTestSessionService(t)
	_, err := service.AppendEntryID(context.Background(), "main", testSessionEntry("main", "assistant"))
	require.NoError(t, err)
	_, err = service.AppendEntryID(context.Background(), "thread", testSessionEntry("thread", "assistant"))
	require.NoError(t, err)
	require.NoError(t, service.UpsertThread("main", ThreadState{Agent: "ops"}))
	require.NoError(t, service.BeginGoal("main", "ship it", "", 5))

	deleted, err := service.DeleteSession(context.Background(), "main")
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)

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
	summaries, err := service.ListSessions(t.Context(), []string{"main", "thread"})
	require.NoError(t, err)
	require.Equal(t, []protocol.SessionSummary{{ConversationID: "main"}, {ConversationID: "thread", LastMessage: "assistant", LastUpdated: time.Unix(1, 0).UTC()}}, summaries)
}

func TestHistoryRemovalIncludesDelegations(t *testing.T) {
	cutoff := time.Unix(1_700_000_000, 0).UTC()

	for _, remove := range []func(*SessionService, string) error{
		func(s *SessionService, id string) error { _, err := s.DeleteSession(t.Context(), id); return err },
		func(s *SessionService, _ string) error { _, err := s.PruneStateBefore(t.Context(), cutoff); return err },
	} {
		store := newTestSessionService(t)
		id := protocol.SlackThreadConversationID("D_1", slackTestTS(cutoff.Add(-time.Hour)))
		require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "planner"}))
		_, err := store.AppendEntryID(t.Context(), id, testSessionEntryAt(cutoff.Add(-time.Hour), id))
		require.NoError(t, err)

		decoy := protocol.SlackThreadConversationID("DX1", slackTestTS(cutoff.Add(-time.Hour))) + "/call-1"
		histories := []string{id + "/call-1", id + "/call-1/call-2", decoy}

		for _, history := range histories {
			_, err := store.AppendEntryID(t.Context(), history, testSessionEntryAt(cutoff.Add(time.Hour), history))
			require.NoError(t, err)
		}

		require.NoError(t, remove(store, id))

		summaries, err := store.ListSessions(t.Context(), histories)
		require.NoError(t, err)
		require.Len(t, summaries, 1)
		require.Equal(t, decoy, summaries[0].ConversationID)

		entries, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM session_entries WHERE conversation_id = ANY($1)`, "delegation entries", histories)
		require.NoError(t, err)
		require.Equal(t, []string{decoy}, entries)
	}
}

func TestDeleteSessionMissingIDReturnsZero(t *testing.T) {
	service := newTestSessionService(t)
	deleted, err := service.DeleteSession(context.Background(), "missing")
	require.NoError(t, err)
	assert.Zero(t, deleted)
}

func TestDeleteSessionMissingDBReturnsZero(t *testing.T) {
	deleted, err := DeleteSession(context.Background(), t.TempDir(), "main")
	require.NoError(t, err)
	assert.Zero(t, deleted)
}

func TestDeleteSessionRejectsBlankConversationID(t *testing.T) {
	_, err := DeleteSession(context.Background(), t.TempDir(), " ")
	require.ErrorContains(t, err, "conversation ID is required")
}

func TestSessionServicePersistsExternalMCPSessionMapping(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)

	require.NoError(t, store.UpsertExternalMCPSession("ticket-123", &ExternalMCPSessionState{Agent: " cron ", PrivateConversationID: " external_mcp:cron:abc ", ManagedConversationID: " slack-thread:C1:1.1 ", SlackChannel: " #ops "}))

	session, ok, err := store.ExternalMCPSession("ticket-123")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ExternalMCPSessionState{Agent: "cron", PrivateConversationID: "external_mcp:cron:abc", ManagedConversationID: "slack-thread:C1:1.1", SlackChannel: "#ops"}, session)

	require.NoError(t, store.UpsertExternalMCPSession("ticket-123", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: "external_mcp:planner:def", ManagedConversationID: "slack-thread:C1:2.2", SlackChannel: "#ops"}))

	session, ok, err = store.ExternalMCPSession("ticket-123")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ExternalMCPSessionState{Agent: "planner", PrivateConversationID: "external_mcp:planner:def", ManagedConversationID: "slack-thread:C1:2.2", SlackChannel: "#ops"}, session)
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
	listed, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM managed_conversations ORDER BY conversation_id`, "managed conversation IDs")
	require.NoError(t, err)
	assert.Equal(t, []string{session.ManagedConversationID}, listed)

	for conversationID, entryID := range map[string]int64{session.PrivateConversationID: privateEntryID, session.ManagedConversationID: managedEntryID} {
		entries, err := store.ObserveEntries(t.Context(), conversationID)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, entryID, entries[0].ID)
	}
}

func TestSessionServiceRejectsBlankKeys(t *testing.T) {
	store := newTestSessionService(t)

	require.ErrorContains(t, store.UpsertThread(" ", ThreadState{Agent: "agent"}), "thread conversation ID is required")
	require.ErrorContains(t, store.UpsertExternalMCPSession(" ", &ExternalMCPSessionState{}), "external MCP conversation ID is required")
	require.EqualError(t, store.BeginGoal(" ", "obj", "", 1), "goal conversation ID is required")
	require.EqualError(t, store.BeginGoal("thread-1", " ", "", 1), "goal objective is required")
	require.NoError(t, store.BeginGoal("thread-1", "obj", "", -1))
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
	_, err := deleteSessionEntries(context.Background(), errStore{errExec: errors.New("no delete")}, map[string]struct{}{"main": {}})
	require.ErrorContains(t, err, "lock session history")

	_, err = deleteSessionEntries(context.Background(), errStore{result: errResult{errRows: errors.New("no rows")}}, map[string]struct{}{"main": {}})
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
	for _, conversationID := range []string{oldThread, activeOldThread, newThread, boundaryThread, "slack-thread:D123:not-a-time"} {
		require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner"}))
	}

	require.NoError(t, store.UpsertThread("empty-recorded", ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, store.UpsertThread("empty-running", ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))
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

	orphanGoal := protocol.SlackThreadConversationID("DGOAL", slackTestTS(oldTime))
	require.NoError(t, store.BeginGoal(orphanGoal, "stale goal", "", 1))
	require.NoError(t, store.BeginGoal(activeOldThread, "keep", "", 1))
	listed, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM managed_conversations ORDER BY conversation_id`, "managed conversation IDs")
	require.NoError(t, err)
	assert.Contains(t, listed, activeOldThread)
	assert.NotContains(t, listed, "cron:daily:new")
	assert.NotContains(t, listed, "cron:daily:old")
	assert.NotContains(t, listed, "one-off-cron:daily:old")

	stats, err := store.PruneStateBefore(context.Background(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, PruneStateStats{Threads: 2, SessionRows: 5}, stats)

	_, ok, err := store.Goal(orphanGoal)
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = store.Goal(activeOldThread)
	require.NoError(t, err)
	assert.True(t, ok)

	threadIDs, err := queryStrings(context.Background(), store.db, `SELECT conversation_id FROM managed_conversations ORDER BY conversation_id`, "managed conversation IDs")
	require.NoError(t, err)
	assert.NotContains(t, threadIDs, oldThread)
	assert.NotContains(t, threadIDs, "empty-recorded")
	assert.Contains(t, threadIDs, "empty-running")

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
		summaries, err := store.ListSessions(t.Context(), []string{conversationID})
		require.NoError(t, err)
		assert.Empty(t, summaries, conversationID)
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

	require.NoError(t, store.UpsertThread(queuedSlack, ThreadState{Agent: "planner"}))
	require.NoError(t, store.UpsertThread(emptySlack, ThreadState{Agent: "planner"}))
	_, err := store.AppendEntryID(t.Context(), queuedSlack, testSessionEntryAt(cutoff.Add(-time.Hour), queuedSlack))
	require.NoError(t, err)
	_, err = store.AppendEntryID(t.Context(), emptySlack, testSessionEntryAt(cutoff.Add(-time.Hour), emptySlack))
	require.NoError(t, err)

	slackItem := protocol.ThreadQueueItem{ID: "slack-q1", ConversationID: queuedSlack, Message: "waiting slack", Principal: "U1"}
	require.NoError(t, store.PutThreadQueueItem(slackItem.ID, &slackItem))

	managedID := protocol.SlackThreadConversationID("C1", slackTestTS(cutoff.Add(-time.Hour)))
	privateID := "external_mcp:planner:private"
	require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: privateID, ManagedConversationID: managedID, SlackChannel: "#ops"}))
	require.NoError(t, store.UpsertThread(privateID, ThreadState{Agent: "planner"}))

	for _, conversationID := range []string{privateID, managedID} {
		_, err := store.AppendEntryID(t.Context(), conversationID, testSessionEntryAt(cutoff.Add(-time.Hour), conversationID))
		require.NoError(t, err)
	}

	privateItem := protocol.ThreadQueueItem{ID: "private-q1", ConversationID: privateID, Message: "private follow-up", Principal: "mcp"}
	require.NoError(t, store.PutThreadQueueItem(privateItem.ID, &privateItem))

	orphanID := "external_mcp:planner:queued-orphan"
	_, err = store.AppendEntryID(t.Context(), orphanID, testSessionEntryAt(cutoff.Add(-time.Hour), orphanID))
	require.NoError(t, err)

	for _, id := range []string{"orphan-q1", "orphan-q2"} {
		item := protocol.ThreadQueueItem{ID: id, ConversationID: orphanID, Message: "orphan follow-up", Principal: "mcp"}
		require.NoError(t, store.PutThreadQueueItem(id, &item))
	}

	_, err = store.PruneStateBefore(t.Context(), cutoff)
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
	_, err = store.PruneStateBefore(t.Context(), cutoff)
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
}

func TestSessionServicePrunesStaleExternalConversationWithActiveTurn(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)
	cutoff := time.Unix(1_700_000_000, 0).UTC()
	managedConversationID := protocol.SlackThreadConversationID("C1", slackTestTS(cutoff.Add(-time.Hour)))
	privateConversationID := "external_mcp:planner:private"
	require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID, SlackChannel: "#ops"}))

	for _, conversationID := range []string{privateConversationID, managedConversationID} {
		_, err := store.AppendEntryID(t.Context(), conversationID, testSessionEntryAt(cutoff.Add(-time.Hour), conversationID))
		require.NoError(t, err)
	}

	seedActiveTurn(t, store, privateConversationID, "active-mcp", &harness.SessionEntry{Version: 1, Type: "turn", TurnID: "active-mcp"})

	stats, err := store.PruneStateBefore(t.Context(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, PruneStateStats{Threads: 1, ExternalMCPSessions: 1, SessionRows: 2}, stats)

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
				require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID, SlackChannel: "#ops"}))
				require.NoError(t, store.UpsertThread(privateConversationID, ThreadState{Agent: "planner"}))
			} else {
				require.NoError(t, store.UpsertThread(managedConversationID, ThreadState{Agent: "main"}))
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

			_, err := store.PruneStateBefore(t.Context(), cutoff)
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

func pruneTestTime(cutoff time.Time, fresh bool) time.Time {
	if fresh {
		return cutoff.Add(time.Second)
	}

	return cutoff.Add(-time.Second)
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

	keys, err := queryStrings(t.Context(), service.db, `SELECT key FROM turn_steps WHERE conversation_id = $1 ORDER BY key`, "turn step keys", conversationID)
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
		data, err := json.Marshal(struct {
			Record *harness.SessionEntry `json:"record"`
		}{record})
		require.NoError(t, err)
		require.NoError(t, conversationJournal{store: service, conversationID: conversationID, log: slog.New(slog.DiscardHandler)}.Save(t.Context(), turnID, data))
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
	return &harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), ResponseID: "", Model: "gpt-5.5", ReplayInput: testReplayInput(replayInputMessage{role: "user", text: user}, replayInputMessage{role: "assistant", text: assistant})}
}

func testSessionEntryAt(ts time.Time, user string) *harness.SessionEntry {
	entry := testSessionEntry(user, "assistant")
	entry.Timestamp = ts.UTC()

	return entry
}

func slackTestTS(ts time.Time) string {
	ts = ts.UTC()
	return fmt.Sprintf("%d.%06d", ts.Unix(), ts.Nanosecond()/1_000)
}

func testReplayInput(messages ...replayInputMessage) []json.RawMessage {
	var replayInput []json.RawMessage

	for i := range messages {
		raw, err := replayInputForMessage(messages[i].role, messages[i].text)
		if err != nil {
			panic(err)
		}

		replayInput = append(replayInput, raw...)
	}

	return replayInput
}
func TestCopiedAttributionSurvivesSourceRetention(t *testing.T) {
	store := newTestSessionService(t)
	entry := harness.SessionEntry{Version: 1, Type: "turn", Agent: "planner", Model: "work/model-a", ReasoningEffort: new("high"), ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"answer"}`)}}
	_, err := store.appendExternalMCPEntry(t.Context(), "producer-x", "interactive-y", &entry, nil)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `DELETE FROM session_entries WHERE conversation_id=$1`, "producer-x")
	require.NoError(t, err)
	entries, err := store.ObserveEntries(t.Context(), "interactive-y")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "producer-x", entries[0].SourceConversationID)
	require.Equal(t, entry.AttributionAt(0), entries[0].Entry.AttributionAt(0))
	require.JSONEq(t, string(entry.ReplayInput[0]), string(entries[0].Entry.ReplayInput[0]))
}
