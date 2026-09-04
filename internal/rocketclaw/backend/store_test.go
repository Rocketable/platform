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

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testDSNFile(workspace string) string {
	return filepath.Join(workspace, ".test-database-url")
}

func NewSessionService(workspace string) (*SessionService, error) {
	if data, err := os.ReadFile(testDSNFile(workspace)); err == nil {
		return NewSessionServiceIn(strings.TrimSpace(string(data)), slog.New(slog.DiscardHandler))
	}

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	if err != nil {
		return nil, fmt.Errorf("isolate test database: %w", err)
	}

	if err := os.WriteFile(testDSNFile(workspace), []byte(dsn), 0o600); err != nil {
		return nil, fmt.Errorf("remember test database url: %w", err)
	}

	return NewSessionServiceIn(dsn, slog.New(slog.DiscardHandler))
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

	service, err := NewSessionServiceIn(testStoreDSN(workspace), slog.New(slog.DiscardHandler))
	if err != nil {
		return 0, err
	}

	defer func() { _ = service.Stop(ctx) }()

	return appendSessionEntryDB(ctx, service.db, conversationID, entry)
}

func DeleteSession(ctx context.Context, workspace, conversationID string) (int64, error) {
	return DeleteSessionIn(ctx, testStoreDSN(workspace), conversationID)
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

	first := testSessionEntry("first", "assistant")
	second := testSessionEntry("second", "assistant")
	second.TokenUsage = &harness.TokenUsage{PromptTokens: 12, CompletionTokens: 5, TotalTokens: 17}
	id1, err := service.AppendEntryID(context.Background(), "main", first)
	require.NoError(t, err)
	id2, err := service.AppendEntryID(context.Background(), "main", second)
	require.NoError(t, err)
	assert.Greater(t, id2, id1)

	observed, err := service.ObserveEntries(context.Background(), "main", id1)
	require.NoError(t, err)
	require.Len(t, observed, 1)
	assert.Equal(t, id2, observed[0].ID)
	assert.Equal(t, *second, observed[0].Entry)
}

func TestSessionServiceTurnPairAllowsOnlyOneActiveTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		assert.False(t, service.PairBusyFor("slack-thread:C1:1.1", ""))
		unlockFirst, err := service.lockTurnPair(t.Context(), "slack-thread:C1:1.1", "external_mcp:private")
		require.NoError(t, err)
		assert.True(t, service.PairBusyFor("slack-thread:C1:1.1", ""))

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
		assert.False(t, service.PairBusyFor("slack-thread:C1:1.1", ""))
	})
}

func TestSessionServiceWorkflowAndMCPReservationsHaveOneWinner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		pairID, privateID := "slack-thread:C1:1.1", "external_mcp:private"
		require.NoError(t, service.UpsertExternalMCPSession("public-1", &ExternalMCPSessionState{PrivateConversationID: privateID, ManagedConversationID: pairID}))

		start, privateRelease := make(chan struct{}), make(chan struct{})
		privateAcquired := false

		go func() {
			<-start
			service.reserveTurnPair(pairID, privateID)

			unlock, err := service.lockTurnPair(t.Context(), pairID, privateID)
			if err != nil {
				return
			}

			privateAcquired = true

			<-privateRelease
			unlock()
			service.completeTurnPairReservation(pairID, privateID)
		}()

		workflowResult := make(chan struct {
			release  func()
			reserved bool
			err      error
		}, 1)

		go func() {
			<-start

			release, reserved, err := service.ReserveWorkflowTurn(pairID)
			workflowResult <- struct {
				release  func()
				reserved bool
				err      error
			}{release, reserved, err}
		}()

		close(start)

		result := <-workflowResult

		synctest.Wait()
		require.NoError(t, result.err)
		assert.NotEqual(t, result.reserved, privateAcquired)
		result.release()
		close(privateRelease)
	})
}

func TestSessionServiceWorkflowReservationReleaseAndUnpairedInert(t *testing.T) {
	service := newTestSessionService(t)
	pairID := "slack-thread:C1:1.1"
	release, reserved, err := service.ReserveWorkflowTurn(pairID)
	require.NoError(t, err)
	require.True(t, reserved)
	release()
	assert.Empty(t, service.turnGates)

	require.NoError(t, service.UpsertExternalMCPSession("public-1", &ExternalMCPSessionState{PrivateConversationID: "external_mcp:private", ManagedConversationID: pairID}))
	release, reserved, err = service.ReserveWorkflowTurn(pairID)
	require.NoError(t, err)
	require.True(t, reserved)
	release()

	release, reserved, err = service.ReserveWorkflowTurn(pairID)
	require.NoError(t, err)
	assert.True(t, reserved)

	inert, reserved, err := service.ReserveWorkflowTurn(pairID)
	require.NoError(t, err)
	assert.False(t, reserved)
	inert()
	release()
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

func TestSessionServiceReleasesAbandonedExternalMCPRecovery(t *testing.T) {
	service := newTestSessionService(t)
	pairID, privateID := protocol.SlackThreadConversationID("C1", "1.1"), "external_mcp:private"
	require.NoError(t, service.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: privateID, ManagedConversationID: pairID, SlackChannel: "#ops"}))
	_, err := service.appendExternalMCPEntry(t.Context(), privateID, pairID, testSessionEntry("first", "answer"), nil)
	require.NoError(t, err)

	require.NoError(t, service.ReserveExternalMCPRecovery(privateID))
	require.NoError(t, service.ReleaseExternalMCPRecovery(privateID))

	unlock, err := service.lockTurnPair(t.Context(), pairID, pairID)
	require.NoError(t, err)
	unlock()
}

func TestSessionServiceScheduledMessages(t *testing.T) {
	store := newTestSessionService(t)
	dueAt := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)

	require.NoError(t, store.PutScheduledMessage("schedule-1", &protocol.ScheduledMessageState{ConversationID: "slack-thread:D123:111.222", Agent: "helper", Message: "later", DueAt: dueAt}))

	messages, err := store.ScheduledMessages()
	require.NoError(t, err)
	assert.Equal(t, map[string]protocol.ScheduledMessageState{"schedule-1": {ConversationID: "slack-thread:D123:111.222", Agent: "helper", Message: "later", DueAt: dueAt}}, messages)
}

func TestSessionServiceThreadQueuePersistsOrderAndParkAfter(t *testing.T) {
	store := newTestSessionService(t)
	firstStash := time.Date(2000, 1, 2, 3, 0, 0, 0, time.UTC)
	secondStash := time.Date(2000, 1, 2, 4, 0, 0, 0, time.UTC)
	conversationID := "slack-thread:D123:111.222"
	dueAt := time.Date(2000, 1, 2, 16, 0, 0, 0, time.UTC)

	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ConversationID: conversationID, Message: "write tests", Principal: "U1", StashAt: firstStash, Position: 0, ParkAfter: "s1"}))
	require.NoError(t, store.PutThreadQueueItem("q2", &protocol.ThreadQueueItem{ConversationID: conversationID, Message: "write changelog", Principal: "U1", StashAt: secondStash, Position: 1, ParkAfter: "s1"}))
	require.NoError(t, store.PutThreadQueueItem("other", &protocol.ThreadQueueItem{ConversationID: "other", Message: "keep", Principal: "U2", StashAt: firstStash, Position: 0}))
	require.NoError(t, store.PutScheduledMessage("s1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "helper", Message: "later", DueAt: dueAt}))

	items, err := store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "q1", items[0].ID)
	assert.Equal(t, "write tests", items[0].Message)
	assert.Equal(t, firstStash, items[0].StashAt)
	assert.Equal(t, "s1", items[0].ParkAfter)
	assert.Equal(t, "q2", items[1].ID)
	assert.Equal(t, secondStash, items[1].StashAt)
	assert.Equal(t, "s1", items[1].ParkAfter)

	rows := protocol.MixedLaterWork(items, map[string]protocol.ScheduledMessageState{"s1": {DueAt: dueAt}})
	require.Len(t, rows, 3)
	assert.Equal(t, protocol.LaterWorkScheduled, rows[0].Kind)
	assert.Equal(t, "q1", rows[1].Queue.ID)
	assert.Equal(t, "q2", rows[2].Queue.ID)

	require.NoError(t, store.DeleteScheduledMessage("missing"))
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
	assert.Equal(t, 6, n)
	require.Error(t, first.db.QueryRowContext(t.Context(), `SELECT 1 FROM store_bootstrap`).Scan(&n))

	second, err := NewSessionServiceIn(testStoreDSN(workspace), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Stop(context.Background())) })
	require.NoError(t, second.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pg_migrations`).Scan(&n))
	assert.Equal(t, 6, n)
}

func TestSessionServiceRenamesGorpMigrations(t *testing.T) {
	workspace := t.TempDir()
	first := newTestSessionServiceAt(t, workspace)
	_, err := first.db.ExecContext(t.Context(), `ALTER TABLE pg_migrations RENAME TO gorp_migrations`)
	require.NoError(t, err)

	second := newTestSessionServiceAt(t, workspace)

	var n int
	require.NoError(t, second.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pg_migrations`).Scan(&n))
	assert.Equal(t, 6, n)
	require.Error(t, second.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM gorp_migrations`).Scan(&n))
}

func TestSessionServiceActiveTurnLifecycle(t *testing.T) {
	store := newTestSessionService(t)
	checkpoint := &harness.ActiveTurnCheckpoint{
		TurnID:          " turn-1 ",
		ConversationKey: " conversation-1 ",
		Agent:           " planner ",
		Model:           " gpt-5.5 ",
		DisplayModel:    " GPT-5.5 ",
		ReplayInput:     []json.RawMessage{json.RawMessage(`{"type":"message","role":"user"}`)},
		OutputTrace:     []json.RawMessage{json.RawMessage(`{"id":"output-1"}`)},
		TokenUsage:      &harness.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
		ResponseID:      " resp-1 ",
		OpenFunctionCalls: []harness.FunctionCallCheckpoint{{
			CallID:    "call-1",
			Name:      "read",
			Arguments: json.RawMessage(`{"filePath":"README.md"}`),
		}},
	}

	require.NoError(t, store.StartActiveTurn(context.Background(), checkpoint))

	checkpoint.ResponseID = "resp-2"
	checkpoint.OpenFunctionCalls = nil
	checkpoint.CompletedFunctionOutputs = []harness.FunctionOutputCheckpoint{{CallID: "call-1", Name: "read", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"function_call_output"}`)}}}
	require.NoError(t, store.StartActiveTurn(context.Background(), checkpoint))

	turns, err := store.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	require.Len(t, turns, 1)
	assert.Equal(t, "turn-1", turns[0].Checkpoint.TurnID)
	assert.Equal(t, "conversation-1", turns[0].Checkpoint.ConversationKey)
	assert.Equal(t, "planner", turns[0].Checkpoint.Agent)
	assert.Equal(t, "gpt-5.5", turns[0].Checkpoint.Model)
	assert.Equal(t, "GPT-5.5", turns[0].Checkpoint.DisplayModel)
	assert.Equal(t, "resp-2", turns[0].Checkpoint.ResponseID)
	assert.Equal(t, checkpoint.ReplayInput, turns[0].Checkpoint.ReplayInput)
	assert.Equal(t, checkpoint.CompletedFunctionOutputs, turns[0].Checkpoint.CompletedFunctionOutputs)

	require.NoError(t, store.ClearActiveTurn(context.Background(), " turn-1 "))
	turns, err = store.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	assert.Empty(t, turns)
}

func TestSessionServiceActiveTurnPersistsThroughCentralizedOpener(t *testing.T) {
	workspace := t.TempDir()
	store, err := NewSessionService(workspace)
	require.NoError(t, err)

	checkpoint := &harness.ActiveTurnCheckpoint{
		TurnID:          "turn-1",
		ConversationKey: "conversation-1",
		Agent:           "planner",
		Model:           "gpt-5.5",
		DisplayModel:    "gpt-5.5",
		ReplayInput:     []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)},
	}

	require.NoError(t, store.StartActiveTurn(context.Background(), checkpoint))
	require.NoError(t, store.Stop(context.Background()))

	reopened := newTestSessionServiceAt(t, workspace)
	turns, err := reopened.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	require.Len(t, turns, 1)
	assert.Equal(t, "turn-1", turns[0].Checkpoint.TurnID)
	assert.Equal(t, "conversation-1", turns[0].Checkpoint.ConversationKey)
}

func TestSessionServiceRecoverableActiveTurnsReturnsEveryRemainingRow(t *testing.T) {
	store := newTestSessionService(t)

	for _, turnID := range []string{"turn-1", "turn-2", "turn-3"} {
		checkpoint := &harness.ActiveTurnCheckpoint{TurnID: turnID, ConversationKey: "conversation-" + turnID, Agent: "planner", Model: "gpt-5.5", DisplayModel: "gpt-5.5"}
		require.NoError(t, store.StartActiveTurn(context.Background(), checkpoint))
	}

	turns, err := store.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)

	turnIDs := make([]string, 0, len(turns))
	for _, turn := range turns {
		turnIDs = append(turnIDs, turn.Checkpoint.TurnID)
	}

	assert.ElementsMatch(t, []string{"turn-1", "turn-2", "turn-3"}, turnIDs)
}

func TestSessionServiceRecoverableActiveTurnsDeletesCorruptRows(t *testing.T) {
	store := newTestSessionService(t)
	valid := &harness.ActiveTurnCheckpoint{TurnID: "turn-valid", ConversationKey: "conversation-valid", Agent: "planner", Model: "gpt-5.5", DisplayModel: "gpt-5.5"}
	require.NoError(t, store.StartActiveTurn(context.Background(), valid))
	_, err := store.db.ExecContext(context.Background(), `INSERT INTO active_turns (id, conversation_id, agent, model, display_model, replay_input_json, output_trace_json, token_usage_json, response_id, open_function_calls_json, completed_function_outputs_json, restart_notice_json, source_metadata_json, created_at_unix_ns, updated_at_unix_ns) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`, "turn-notice", "conversation-notice", "planner", "gpt-5.5", "gpt-5.5", `null`, `null`, `null`, "", `null`, `null`, "restarted", "", int64(0), int64(0))
	require.NoError(t, err)

	insertCorrupt := func(id, replay, output, usage, openCalls, completed, metadata string) {
		t.Helper()

		_, err := store.db.ExecContext(context.Background(), `INSERT INTO active_turns (id, conversation_id, agent, model, display_model, replay_input_json, output_trace_json, token_usage_json, response_id, open_function_calls_json, completed_function_outputs_json, restart_notice_json, source_metadata_json, created_at_unix_ns, updated_at_unix_ns) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`, id, "conversation-corrupt", "planner", "gpt-5.5", "gpt-5.5", replay, output, usage, "", openCalls, completed, "", metadata, int64(1), int64(1))
		require.NoError(t, err)
	}
	insertCorrupt("turn-corrupt", `{`, `null`, `null`, `null`, `null`, `{}`)
	insertCorrupt("turn-output", `null`, `{`, `null`, `null`, `null`, `{}`)
	insertCorrupt("turn-usage", `null`, `null`, `{`, `null`, `null`, `{}`)
	insertCorrupt("turn-open", `null`, `null`, `null`, `{`, `null`, `{}`)
	insertCorrupt("turn-completed", `null`, `null`, `null`, `null`, `{`, `{}`)
	insertCorrupt("turn-metadata", `null`, `null`, `null`, `null`, `null`, `{`)

	_, _, err = store.ActiveTurn(context.Background(), "turn-corrupt")
	got, ok := errors.AsType[activeTurnCorruptError](err)
	require.True(t, ok)
	assert.Equal(t, "turn-corrupt", got.turnID)
	assert.Equal(t, "replay input", got.field)
	require.ErrorIs(t, err, got.err)

	turns, err := store.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	require.Len(t, turns, 2)

	byID := map[string]ActiveTurnState{}
	for _, turn := range turns {
		byID[turn.Checkpoint.TurnID] = turn
	}

	assert.Contains(t, byID, "turn-valid")
	assert.Equal(t, "restarted", byID["turn-notice"].SourceMetadata["restart_notice_json"])
	assert.True(t, byID["turn-notice"].CreatedAt.IsZero())

	var count int
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM active_turns WHERE id LIKE 'turn-%'`).Scan(&count))
	assert.Equal(t, 2, count)
}

func TestSessionServiceRecoverableActiveTurnsReportsDBFailures(t *testing.T) {
	store := newTestSessionService(t)
	require.NoError(t, store.Stop(context.Background()))

	_, err := store.RecoverableActiveTurns(context.Background())
	require.ErrorContains(t, err, "query recoverable active turns")
}

func TestSessionServiceSyncCronSchedulesInsertsUpdatesAndDeletes(t *testing.T) {
	store := newTestSessionService(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	due := now.Add(time.Minute)

	require.NoError(t, store.SyncCronSchedules([]CronScheduleState{
		{ScheduleID: "daily#0", RelativePath: "daily.md", NextDue: due},
		{ScheduleID: "weekly#0", RelativePath: "weekly.md", NextDue: due.Add(time.Hour)},
	}, now))

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

	require.NoError(t, store.SyncCronSchedules([]CronScheduleState{{ScheduleID: "empty#0", RelativePath: "empty.md"}}, now))

	var nextDue int64
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT next_due_unix_ns FROM cron_schedules WHERE schedule_id = 'empty#0'`).Scan(&nextDue))
	assert.Equal(t, int64(0), nextDue)

	dueSchedules, err := store.DueCronSchedules(now, 10)
	require.NoError(t, err)
	assert.Contains(t, dueSchedules, CronScheduleState{ScheduleID: "empty#0", RelativePath: "empty.md"})
}

func TestSessionServiceDueCronSchedulesHonorsDueTimeAndLimit(t *testing.T) {
	store := newTestSessionService(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)

	require.NoError(t, store.SyncCronSchedules([]CronScheduleState{
		{ScheduleID: "second", RelativePath: "second.md", NextDue: now},
		{ScheduleID: "first", RelativePath: "first.md", NextDue: now.Add(-time.Minute)},
		{ScheduleID: "later", RelativePath: "later.md", NextDue: now.Add(time.Minute)},
	}, now))

	due, err := store.DueCronSchedules(now, 1)
	require.NoError(t, err)
	assert.Equal(t, []CronScheduleState{{ScheduleID: "first", RelativePath: "first.md", NextDue: now.Add(-time.Minute)}}, due)
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
	assert.Equal(t, CronScheduleRun{ScheduleID: "daily#0", RelativePath: "daily.md", DueAt: now}, run)

	run, ok, err = store.ClaimCronSchedule(dueDailySecond, next, now)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Zero(t, run)

	due, err := store.DueCronSchedules(now, 10)
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
	assert.Equal(t, CronScheduleRun{ScheduleID: "daily#0", RelativePath: "daily.md", DueAt: next}, run)
}

func TestSessionServiceBeginGoalPersistsCheckScript(t *testing.T) {
	store := newTestSessionService(t)

	require.NoError(t, store.BeginGoal("thread-1", " fix lint ", " ./scripts/check.sh --linter-mode ", 3, " T123 ", " U456 "))
	goal, ok, err := store.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "fix lint", goal.Objective)
	assert.Equal(t, "./scripts/check.sh --linter-mode", goal.CheckScript)
	assert.Equal(t, 3, goal.MaxTurns)
	assert.Equal(t, "T123", goal.SlackRecipientTeamID)
	assert.Equal(t, "U456", goal.SlackRecipientUserID)

	goals, err := store.ActiveGoals()
	require.NoError(t, err)
	assert.Equal(t, goal, goals["thread-1"])

	require.NoError(t, store.BeginGoal("thread-2", "write docs", " ", 1, "", ""))
	goal, ok, err = store.Goal("thread-2")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, goal.CheckScript)
}

func TestSessionServiceBeginGoalRejectsActiveGoal(t *testing.T) {
	store := newTestSessionService(t)

	require.NoError(t, store.BeginGoal("thread-1", "first", "", 3, "", ""))
	err := store.BeginGoal("thread-1", "second", "", 3, "", "")
	require.ErrorIs(t, err, protocol.ErrGoalAlreadyActive)

	goal, ok, err := store.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "first", goal.Objective)
}

func TestSessionServiceBeginGoalAllowsGoalAfterTerminal(t *testing.T) {
	store := newTestSessionService(t)

	require.NoError(t, store.BeginGoal("thread-1", "first", "", 3, "old-team", "old-user"))
	_, err := store.UpdateGoalStatus("thread-1", GoalStatusComplete, "done")
	require.NoError(t, err)
	require.NoError(t, store.BeginGoal("thread-1", "second", "./check.sh", 1, "new-team", "new-user"))

	goal, ok, err := store.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "second", goal.Objective)
	assert.Equal(t, GoalStatusActive, goal.Status)
	assert.Equal(t, 0, goal.TurnsUsed)
	assert.Equal(t, "new-team", goal.SlackRecipientTeamID)
	assert.Equal(t, "new-user", goal.SlackRecipientUserID)
}

func TestSessionServiceProgressGoalKeepsGoalActiveAndRecordsNote(t *testing.T) {
	store := newTestSessionService(t)

	require.NoError(t, store.BeginGoal("thread-1", "first", "", 3, "", ""))
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

func TestSessionServiceAppliesPendingRestartNotificationsOnce(t *testing.T) {
	store := newTestSessionService(t)

	requesters := []string{"main", "thread", "spaced"}
	for _, conversationID := range append(requesters, "unmarked") {
		_, err := store.AppendEntryID(context.Background(), conversationID, testSessionEntry(conversationID, "assistant"))
		require.NoError(t, err)
	}

	require.ErrorContains(t, store.MarkRestartRequester(context.Background(), " "), "restart requester conversation ID is required")
	require.NoError(t, store.MarkRestartRequester(context.Background(), "main"))
	require.NoError(t, store.MarkRestartRequester(context.Background(), "thread"))
	require.NoError(t, store.MarkRestartRequester(context.Background(), " spaced "))
	require.NoError(t, store.MarkRestartRequester(context.Background(), "main"))
	require.NoError(t, store.ApplyPendingRestartNotifications(context.Background()))
	require.NoError(t, store.ApplyPendingRestartNotifications(context.Background()))

	for _, conversationID := range requesters {
		entries, err := store.ObserveEntries(context.Background(), conversationID, 0)
		require.NoError(t, err)
		require.Len(t, entries, 2)
		messages, err := replayInputMessages(entries[1].Entry.ReplayInput)
		require.NoError(t, err)
		assert.Equal(t, []replayInputMessage{{role: "developer", text: restartNotificationDeveloperMessage}}, messages)
	}

	entries, err := store.ObserveEntries(context.Background(), "unmarked", 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
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
	_, err := appendSessionEntryDB(context.Background(), errStore{}, "main", entry)
	require.ErrorContains(t, err, "marshal rocketcode session entry")

	entry.ReplayInput = nil
	_, err = appendSessionEntryDB(context.Background(), errStore{errExec: errors.New("no write")}, "main", entry)
	require.ErrorContains(t, err, "append rocketcode session entry")
}

func TestNewSessionServiceReportsInvalidDatabaseURL(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	_, err := NewSessionServiceIn("not-a-dsn", logger)
	require.Error(t, err)

	_, err = NewSessionServiceIn("postgres://u:s3cret@127.0.0.1:1/none?sslmode=disable", logger)
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

	_, err = NewSessionServiceIn(dsn, logger)
	require.Error(t, err)
}

func TestHoldRunLockRejectsSecondHolder(t *testing.T) {
	service := newTestSessionService(t)
	client, err := newRunLockClient(service.db)
	require.NoError(t, err)
	lock, err := client.Acquire(runLockName, pglock.FailIfLocked())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Close()) })

	err = holdRunLock(t.Context(), service.db, &lockedRun{})
	require.ErrorIs(t, err, errRunLocked)
}

func TestHoldRunLockAllowsSessionServiceWhileHeld(t *testing.T) {
	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	client, err := newRunLockClient(service.db)
	require.NoError(t, err)
	lock, err := client.Acquire(runLockName, pglock.FailIfLocked())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Close()) })

	second, err := NewSessionServiceIn(testStoreDSN(workspace), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NoError(t, second.Stop(t.Context()))
}

func TestSessionStoreMissingIsEmpty(t *testing.T) {
	service := newTestSessionService(t)
	store := newSessionStore("main", service)
	require.Empty(t, collectEntries(t, store.in()))
}

func TestSessionStoreReportsObserveError(t *testing.T) {
	service, err := NewSessionService(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, service.Stop(context.Background()))

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

	summaries, err := ListSessionsInOptions(context.Background(), testStoreDSN(workspace), &SessionListOptions{})
	require.NoError(t, err)
	assert.Empty(t, summaries)

	entries, err := ObserveSessionEntries(context.Background(), testStoreDSN(workspace), "main", 0)
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.NoDirExists(t, filepath.Join(workspace, ".rocketclaw"))
}

func TestListSessionsIncludesLastMessages(t *testing.T) {
	workspace := t.TempDir()

	_, err := AppendSessionEntryID(context.Background(), workspace, "main", testSessionEntry("first user", "first assistant"))
	require.NoError(t, err)
	_, err = AppendSessionEntryID(context.Background(), workspace, "main", testSessionEntry("second\nuser", "second assistant"))
	require.NoError(t, err)
	_, err = AppendSessionEntryID(context.Background(), workspace, "slack-thread:D123:111.222", testSessionEntry("thread user", "thread assistant"))
	require.NoError(t, err)

	summaries, err := ListSessionsInOptions(context.Background(), testStoreDSN(workspace), &SessionListOptions{})
	require.NoError(t, err)
	require.Len(t, summaries, 2)

	assert.Equal(t, protocol.SessionSummary{ConversationID: "main", Turns: 2, LastUpdated: summaries[0].LastUpdated, LastUserMessage: "second\nuser", LastAssistantMessage: "second assistant"}, summaries[0])
	assert.Equal(t, protocol.SessionSummary{ConversationID: "slack-thread:D123:111.222", Turns: 1, LastUpdated: summaries[1].LastUpdated, LastUserMessage: "thread user", LastAssistantMessage: "thread assistant"}, summaries[1])
}

func TestListSessionsOptionsBoundsByLatestUpdate(t *testing.T) {
	workspace := t.TempDir()
	since := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	until := since.Add(48 * time.Hour)

	_, err := AppendSessionEntryID(context.Background(), workspace, "old", testSessionEntryAt(since.Add(-time.Second), "old"))
	require.NoError(t, err)
	_, err = AppendSessionEntryID(context.Background(), workspace, "inside", testSessionEntryAt(since.Add(time.Hour), "inside"))
	require.NoError(t, err)
	_, err = AppendSessionEntryID(context.Background(), workspace, "boundary", testSessionEntryAt(since, "boundary"))
	require.NoError(t, err)
	_, err = AppendSessionEntryID(context.Background(), workspace, "until", testSessionEntryAt(until, "until"))
	require.NoError(t, err)

	summaries, err := ListSessionsInOptions(context.Background(), testStoreDSN(workspace), &SessionListOptions{Since: since, Until: until})
	require.NoError(t, err)
	require.Len(t, summaries, 2)
	assert.Equal(t, "inside", summaries[0].ConversationID)
	assert.Equal(t, "boundary", summaries[1].ConversationID)

	summaries, err = ListSessionsInOptions(context.Background(), testStoreDSN(workspace), &SessionListOptions{Since: since})
	require.NoError(t, err)
	require.Len(t, summaries, 3)
	assert.Equal(t, "until", summaries[0].ConversationID)

	summaries, err = ListSessionsInOptions(context.Background(), testStoreDSN(workspace), &SessionListOptions{Until: until})
	require.NoError(t, err)
	require.Len(t, summaries, 3)
	assert.Equal(t, "inside", summaries[0].ConversationID)
}

func TestListSessionsOptionsLimitUsesMostRecent(t *testing.T) {
	workspace := t.TempDir()
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

	for i, conversationID := range []string{"old", "middle", "new"} {
		_, err := AppendSessionEntryID(context.Background(), workspace, conversationID, testSessionEntryAt(base.Add(time.Duration(i)*time.Hour), conversationID))
		require.NoError(t, err)
	}

	summaries, err := ListSessionsInOptions(context.Background(), testStoreDSN(workspace), &SessionListOptions{Limit: 2})
	require.NoError(t, err)
	require.Len(t, summaries, 2)
	assert.Equal(t, "new", summaries[0].ConversationID)
	assert.Equal(t, "middle", summaries[1].ConversationID)
}

func TestListSessionsMissingDBIsEmpty(t *testing.T) {
	summaries, err := ListSessionsInOptions(context.Background(), testStoreDSN(t.TempDir()), &SessionListOptions{})
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestListSessionsOptionsMissingDBIsEmpty(t *testing.T) {
	summaries, err := ListSessionsInOptions(context.Background(), testStoreDSN(t.TempDir()), &SessionListOptions{Limit: 1})
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestSlackStateKeyTimeParsesAndRejectsKeys(t *testing.T) {
	got, ok := slackStateKeyTime("slack-thread:D123:1700000000.1234567899", "slack-thread:")
	require.True(t, ok)
	assert.Equal(t, time.Unix(1700000000, 123456789).UTC(), got)

	for _, key := range []string{
		"external-mcp:D123:1700000000.123456789",
		"slack-thread:D123:",
		"slack-thread:D123:not-seconds",
		"slack-thread:D123:1700000000.not-nanos",
	} {
		t.Run(key, func(t *testing.T) {
			got, ok := slackStateKeyTime(key, "slack-thread:")
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

			_, err := service.AppendEntryID(context.Background(), "main", testSessionEntry(fmt.Sprintf("user %d", i), "assistant"))
			errCh <- err

			errCh <- service.UpsertThread(fmt.Sprintf("thread-%02d", i), ThreadState{Agent: "main"})
		}(i)
	}

	group.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	entries, err := service.ObserveEntries(context.Background(), "main", 0)
	require.NoError(t, err)
	assert.Len(t, entries, 25)

	threadIDs, err := managedConversationIDs(context.Background(), service.db)
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

			errCh <- service.BeginGoal("thread", fmt.Sprintf("goal %02d", i), "", 5, "", "")
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
	require.NoError(t, service.BeginGoal("thread", "ship it", "", 20, "", ""))

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

func TestSessionServiceThreadSettlement(t *testing.T) {
	service := newTestSessionService(t)
	require.NoError(t, service.UpsertThread("thread", ThreadState{Agent: "main"}))
	require.NoError(t, service.SetThreadSettlement("thread", "settled", false))
	thread, ok, err := service.Thread("thread")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "settled", thread.SettledOverride)
	assert.True(t, thread.BumpedAt.IsZero())
	require.NoError(t, service.SetThreadSettlement("thread", "active", true))
	thread, ok, err = service.Thread("thread")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "active", thread.SettledOverride)
	assert.False(t, thread.BumpedAt.IsZero())
	require.NoError(t, service.SetThreadSettlement("thread", "", false))
	thread, ok, err = service.Thread("thread")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, thread.SettledOverride)
	assert.False(t, thread.BumpedAt.IsZero())
}

func TestDeleteSessionDeletesOnlyTarget(t *testing.T) {
	service := newTestSessionService(t)
	_, err := service.AppendEntryID(context.Background(), "main", testSessionEntry("main", "assistant"))
	require.NoError(t, err)
	_, err = service.AppendEntryID(context.Background(), "thread", testSessionEntry("thread", "assistant"))
	require.NoError(t, err)
	require.NoError(t, service.UpsertThread("main", ThreadState{Agent: "ops"}))
	require.NoError(t, service.BeginGoal("main", "ship it", "", 5, "", ""))

	deleted, err := service.DeleteSession(context.Background(), "main")
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)

	mainEntries, err := service.ObserveEntries(context.Background(), "main", 0)
	require.NoError(t, err)
	assert.Empty(t, mainEntries)

	threadEntries, err := service.ObserveEntries(context.Background(), "thread", 0)
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
}

func TestSessionServicePersistsActiveTurnSourceMetadata(t *testing.T) {
	store := newTestSessionService(t)
	checkpoint := &harness.ActiveTurnCheckpoint{
		TurnID:          "turn-1",
		ConversationKey: "external_mcp:planner:private",
		Agent:           "planner",
		Model:           "gpt-5.5",
		DisplayModel:    "gpt-5.5",
		ReplayInput:     []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)},
	}

	require.NoError(t, store.UpsertActiveTurn(context.Background(), checkpoint, map[string]string{"source": "external_mcp", "external_conversation_id": "public-1"}))

	turns, err := store.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	require.Len(t, turns, 1)
	state := turns[0]
	assert.Equal(t, "external_mcp:planner:private", state.Checkpoint.ConversationKey)
	assert.Equal(t, "public-1", state.SourceMetadata["external_conversation_id"])
	assert.Equal(t, "external_mcp", state.SourceMetadata["source"])

	require.NoError(t, store.ClearActiveTurn(context.Background(), "turn-1"))
	turns, err = store.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	assert.Empty(t, turns)
}

func TestSessionServicePersistsPendingSteersOnActiveTurn(t *testing.T) {
	store := newTestSessionService(t)
	checkpoint := &harness.ActiveTurnCheckpoint{TurnID: "turn-1", ConversationKey: "slack-thread:C123:111.222", Agent: "main", Model: "gpt-5.5", DisplayModel: "gpt-5.5"}
	require.NoError(t, store.UpsertActiveTurn(context.Background(), checkpoint, map[string]string{"source": "slack"}))

	steers := []protocol.PendingSteer{{Text: "don't touch the database", Principal: "U1", SlackChannel: "C123", SlackTS: "222.333", SlackThreadTS: "111.222"}}
	require.NoError(t, store.SetPendingSteers("slack-thread:C123:111.222", steers))

	turns, err := store.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	require.Len(t, turns, 1)
	assert.Equal(t, steers, turns[0].PendingSteers)
	assert.Equal(t, "slack", turns[0].SourceMetadata["source"])

	require.NoError(t, store.ClearActiveTurn(context.Background(), "turn-1"))
	turns, err = store.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	assert.Empty(t, turns)
	require.ErrorContains(t, store.SetPendingSteers(" ", nil), "conversation ID is required")
}

func TestSessionServiceRejectsBlankKeys(t *testing.T) {
	store := newTestSessionService(t)

	require.ErrorContains(t, store.UpsertThread(" ", ThreadState{Agent: "agent"}), "thread conversation ID is required")
	require.ErrorContains(t, store.UpsertExternalMCPSession(" ", &ExternalMCPSessionState{}), "external MCP conversation ID is required")
	require.EqualError(t, store.BeginGoal(" ", "obj", "", 1, "", ""), "goal conversation ID is required")
	require.EqualError(t, store.BeginGoal("thread-1", " ", "", 1, "", ""), "goal objective is required")
	require.NoError(t, store.BeginGoal("thread-1", "obj", "", -1, "", ""))
	goal, ok, err := store.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 0, goal.MaxTurns)

	_, err = store.UpdateGoalStatus("thread-1", "nope", "")
	require.EqualError(t, err, `unsupported goal status "nope"`)
	require.EqualError(t, store.ClearActiveTurn(t.Context(), " "), "active turn ID is required")
	require.EqualError(t, store.StartActiveTurn(t.Context(), nil), "active turn checkpoint is required")
	require.EqualError(t, store.StartActiveTurn(t.Context(), &harness.ActiveTurnCheckpoint{}), "active turn ID is required")
	require.EqualError(t, store.StartActiveTurn(t.Context(), &harness.ActiveTurnCheckpoint{TurnID: "turn-1"}), "active turn conversation ID is required")
	_, err = ObserveSessionEntries(t.Context(), testStoreDSN(t.TempDir()), " ", 0)
	require.EqualError(t, err, "conversation ID is required")
}

func TestDeleteSessionEntriesReportsDeleteFailures(t *testing.T) {
	_, err := deleteSessionEntries(context.Background(), errStore{errExec: errors.New("no delete")}, map[string]struct{}{"main": {}})
	require.ErrorContains(t, err, "delete stale session entries")

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

	for conversationID, ts := range map[string]time.Time{
		oldThread:                      oldTime,
		activeOldThread:                newTime,
		newThread:                      newTime,
		boundaryThread:                 cutoff,
		"slack-thread:D123:not-a-time": oldTime,
		"external_mcp:cron:orphan":     oldTime,
		"cron:daily:old":               oldTime,
		"one-off-cron:daily:old":       oldTime,
		"cron:daily:new":               newTime,
		"slack-thread:DORPHAN:1.000":   oldTime,
	} {
		_, err := AppendSessionEntryID(context.Background(), workspace, conversationID, testSessionEntryAt(ts, conversationID))
		require.NoError(t, err)
	}

	require.NoError(t, store.MarkRestartRequester(context.Background(), oldThread))
	require.NoError(t, store.MarkRestartRequester(context.Background(), activeOldThread))
	require.NoError(t, store.MarkRestartRequester(context.Background(), oldThread))

	orphanGoal := protocol.SlackThreadConversationID("DGOAL", slackTestTS(oldTime))
	require.NoError(t, store.BeginGoal(orphanGoal, "stale goal", "", 1, "", ""))
	require.NoError(t, store.BeginGoal(activeOldThread, "keep", "", 1, "", ""))

	stats, err := store.PruneStateBefore(context.Background(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, PruneStateStats{Threads: 1, SessionRows: 5}, stats)

	_, ok, err := store.Goal(orphanGoal)
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = store.Goal(activeOldThread)
	require.NoError(t, err)
	assert.True(t, ok)

	threadIDs, err := managedConversationIDs(context.Background(), store.db)
	require.NoError(t, err)
	assert.NotContains(t, threadIDs, oldThread)
	assert.Contains(t, threadIDs, activeOldThread)
	assert.Contains(t, threadIDs, newThread)
	assert.Contains(t, threadIDs, boundaryThread)
	assert.Contains(t, threadIDs, "slack-thread:D123:not-a-time")

	restartNotifications := pendingRestartNotifications(t, store.db)
	assert.NotContains(t, restartNotifications, oldThread)
	assert.Contains(t, restartNotifications, activeOldThread)

	for _, conversationID := range []string{oldThread, "external_mcp:cron:orphan", "cron:daily:old", "one-off-cron:daily:old", "slack-thread:DORPHAN:1.000"} {
		entries, err := ObserveSessionEntries(context.Background(), testStoreDSN(workspace), conversationID, 0)
		require.NoError(t, err)
		assert.Empty(t, entries, conversationID)
	}

	for _, conversationID := range []string{activeOldThread, "slack-thread:D123:not-a-time", "cron:daily:new"} {
		entries, err := ObserveSessionEntries(context.Background(), testStoreDSN(workspace), conversationID, 0)
		require.NoError(t, err)
		assert.Len(t, entries, 1, conversationID)
	}
}

func TestSessionServiceKeepsWebSessionRowsWhenPruning(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)
	cutoff := time.Unix(1_700_000_000, 0).UTC()
	web := protocol.WebSessionConversationID("ops")
	require.NoError(t, store.UpsertThread(web, ThreadState{Agent: "planner"}))
	_, err := store.AppendEntryID(t.Context(), web, testSessionEntryAt(cutoff.Add(-time.Hour), "web"))
	require.NoError(t, err)

	_, err = store.PruneStateBefore(t.Context(), cutoff)
	require.NoError(t, err)

	threadIDs, err := managedConversationIDs(t.Context(), store.db)
	require.NoError(t, err)
	assert.Contains(t, threadIDs, web)
}

func TestSessionServicePrunesEmptyManagedWithoutPair(t *testing.T) {
	store := newTestSessionServiceAt(t, t.TempDir())
	cutoff := time.Unix(1_700_000_000, 0).UTC()
	empty := protocol.SlackThreadConversationID("DEMPTY", "1.000")
	kept := protocol.SlackThreadConversationID("DKEPT", slackTestTS(cutoff.Add(time.Hour)))

	require.NoError(t, store.UpsertThread(empty, ThreadState{Agent: "planner"}))
	require.NoError(t, store.UpsertThread(kept, ThreadState{Agent: "planner"}))
	_, err := store.AppendEntryID(t.Context(), kept, testSessionEntryAt(cutoff.Add(time.Hour), "kept"))
	require.NoError(t, err)

	stats, err := store.PruneStateBefore(t.Context(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, 1, stats.EmptyManaged)

	threadIDs, err := managedConversationIDs(t.Context(), store.db)
	require.NoError(t, err)
	assert.NotContains(t, threadIDs, empty)
	assert.Contains(t, threadIDs, kept)
}

func TestSessionServiceKeepsPairProtectedEmptyManaged(t *testing.T) {
	store := newTestSessionServiceAt(t, t.TempDir())
	managedConversationID := protocol.SlackThreadConversationID("C1", "1.000")
	require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{
		Agent: "planner", PrivateConversationID: "external_mcp:planner:private", ManagedConversationID: managedConversationID, SlackChannel: "#ops",
	}))

	_, err := store.PruneStateBefore(t.Context(), time.Unix(1_700_000_000, 0).UTC())
	require.NoError(t, err)

	threadIDs, err := managedConversationIDs(t.Context(), store.db)
	require.NoError(t, err)
	assert.Contains(t, threadIDs, managedConversationID)
	ids, err := store.ManagedConversationIDs(t.Context())
	require.NoError(t, err)
	assert.NotContains(t, ids, "cron:daily:old")
}

func TestManagedConversationIDsOmitCronSessionEntries(t *testing.T) {
	store := newTestSessionServiceAt(t, t.TempDir())
	_, err := store.AppendEntryID(t.Context(), "cron:daily:old", testSessionEntryAt(time.Unix(1_700_000_000, 0).UTC(), "cron"))
	require.NoError(t, err)

	human := protocol.SlackThreadConversationID("C1", "1.000")
	require.NoError(t, store.UpsertThread(human, ThreadState{Agent: "main", CreatedBy: ThreadCreatedByCron}))

	ids, err := store.ManagedConversationIDs(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{human}, ids)
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

	require.NoError(t, store.UpsertActiveTurn(t.Context(), &harness.ActiveTurnCheckpoint{TurnID: "active-mcp", ConversationKey: privateConversationID, Agent: "planner", Model: "model", DisplayModel: "model"}, map[string]string{"source": "external_mcp"}))

	stats, err := store.PruneStateBefore(t.Context(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, PruneStateStats{Threads: 1, ExternalMCPSessions: 1, SessionRows: 2}, stats)

	_, ok, err := store.ExternalMCPSession("public-1")
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = store.Thread(managedConversationID)
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = store.ActiveTurn(t.Context(), "active-mcp")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSessionServicePrunesExternalConversationOnlyWhenAllHistoriesAreStale(t *testing.T) {
	cutoff := time.Unix(1_700_000_000, 0).UTC()

	for _, tt := range []struct {
		name                                           string
		paired, privateFresh, managedFresh, wantPruned bool
	}{
		{name: "paired both stale", paired: true, wantPruned: true},
		{name: "paired private fresh", paired: true, privateFresh: true},
		{name: "paired managed fresh", paired: true, managedFresh: true},
		{name: "paired both fresh", paired: true, privateFresh: true, managedFresh: true},
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
			} else {
				require.NoError(t, store.UpsertThread(managedConversationID, ThreadState{Agent: "main"}))
				require.NoError(t, store.UpsertExternalMCPSession("public-1", &ExternalMCPSessionState{Agent: "planner", ManagedConversationID: managedConversationID, SlackChannel: "#ops"}))
			}

			if privateConversationID != "" {
				_, err := store.AppendEntryID(t.Context(), privateConversationID, testSessionEntryAt(pruneTestTime(cutoff, tt.privateFresh), "private"))
				require.NoError(t, err)
			}

			_, err := store.AppendEntryID(t.Context(), managedConversationID, testSessionEntryAt(pruneTestTime(cutoff, tt.managedFresh), "managed"))
			require.NoError(t, err)

			_, err = store.PruneStateBefore(t.Context(), cutoff)
			require.NoError(t, err)
			_, ok, err := store.ExternalMCPSession("public-1")
			require.NoError(t, err)
			assert.Equal(t, !tt.wantPruned, ok)
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

func pendingRestartNotifications(t *testing.T, db stateStoreDB) map[string]bool {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), `SELECT conversation_id FROM pending_restart_notifications`)

	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	notifications := make(map[string]bool)

	for rows.Next() {
		var conversationID string
		require.NoError(t, rows.Scan(&conversationID))
		notifications[conversationID] = true
	}

	require.NoError(t, rows.Err())

	return notifications
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

func newTestSessionService(t *testing.T) *SessionService {
	t.Helper()

	return newTestSessionServiceAt(t, t.TempDir())
}

func newTestSessionServiceAt(t *testing.T, workspace string) *SessionService {
	t.Helper()

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

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
