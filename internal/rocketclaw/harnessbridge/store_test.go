package harnessbridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func NewSessionService(workspace string) (*SessionService, error) {
	return NewSessionServiceIn(workspace, config.DefaultRuntimeDir, slog.New(slog.DiscardHandler))
}

func sessionDBPath(workspace string) string {
	return sessionDBPathIn(workspace, config.DefaultRuntimeDir)
}

func prepareSessionDBPath(workspace string) error {
	return prepareSessionDBPathIn(workspace, config.DefaultRuntimeDir)
}

func AppendSessionEntryID(ctx context.Context, dbPath, conversationID string, entry *harness.SessionEntry) (int64, error) {
	if entry == nil {
		return 0, errors.New("rocketcode session entry is required")
	}

	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return 0, errors.New("conversation ID is required")
	}

	if err := prepareSessionDBPathIn(filepath.Dir(filepath.Dir(dbPath)), filepath.Base(filepath.Dir(dbPath))); err != nil {
		return 0, err
	}

	db, err := openSessionDB(ctx, dbPath, slog.New(slog.DiscardHandler))
	if err != nil {
		return 0, err
	}

	defer func() { _ = db.Close() }()

	return appendSessionEntryDB(ctx, db, conversationID, entry)
}

func DeleteSession(ctx context.Context, workspace, conversationID string) (int64, error) {
	return DeleteSessionIn(ctx, workspace, config.DefaultRuntimeDir, conversationID)
}

func TestSQLiteSessionStoreAppendAndLoad(t *testing.T) {
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

func TestSessionServiceScheduledMessages(t *testing.T) {
	store := newTestSessionService(t)
	dueAt := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)

	require.NoError(t, store.PutScheduledMessage("schedule-1", &ScheduledMessageState{ConversationID: "slack-thread:D123:111.222", Agent: "helper", Message: "later", DueAt: dueAt}))

	messages, err := store.ScheduledMessages()
	require.NoError(t, err)
	assert.Equal(t, map[string]ScheduledMessageState{"schedule-1": {ConversationID: "slack-thread:D123:111.222", Agent: "helper", Message: "later", DueAt: dueAt}}, messages)
}

func TestSessionServiceInitializesCronScheduleSchema(t *testing.T) {
	store := newTestSessionService(t)

	rows, err := store.db.QueryContext(context.Background(), `SELECT name FROM sqlite_master WHERE type IN ('table', 'index') AND name LIKE 'cron_%' ORDER BY name`)

	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var names []string

	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}

	require.NoError(t, rows.Err())

	assert.Equal(t, []string{"cron_schedule_runs", "cron_schedule_runs_running_path", "cron_schedules", "cron_schedules_next_due_id", "cron_schedules_relative_path"}, names)
}

func TestSessionServiceInitializesActiveTurnIndexes(t *testing.T) {
	store := newTestSessionService(t)

	rows, err := store.db.QueryContext(context.Background(), `SELECT name FROM sqlite_master WHERE type IN ('table', 'index') AND name LIKE 'active_turns%' ORDER BY name`)

	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var names []string

	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}

	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"active_turns", "active_turns_conversation_updated"}, names)
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
	require.NoError(t, store.RecordActiveTurnCheckpoint(context.Background(), checkpoint))

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

	require.NoError(t, store.ClearCompletedActiveTurn(context.Background(), " turn-1 "))
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

	_, err := store.db.ExecContext(context.Background(), `INSERT INTO active_turns (id, conversation_id, agent, model, display_model, replay_input_json, output_trace_json, token_usage_json, response_id, open_function_calls_json, completed_function_outputs_json, restart_notice_json, source_metadata_json, created_at_unix_ns, updated_at_unix_ns) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "turn-corrupt", "conversation-corrupt", "planner", "gpt-5.5", "gpt-5.5", `{`, `null`, `null`, "", `null`, `null`, "", `{}`, int64(1), int64(1))
	require.NoError(t, err)

	turns, err := store.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	require.Len(t, turns, 1)
	assert.Equal(t, "turn-valid", turns[0].Checkpoint.TurnID)

	var count int
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM active_turns WHERE id = ?`, "turn-corrupt").Scan(&count))
	assert.Equal(t, 0, count)
}

func TestSessionServiceRecoverableActiveTurnsReportsDBFailures(t *testing.T) {
	store := newTestSessionService(t)
	require.NoError(t, store.Stop(context.Background()))

	_, err := store.RecoverableActiveTurns(context.Background())
	require.ErrorContains(t, err, "query recoverable active turns")
}

func TestSessionServiceInitializesActiveTurnSchema(t *testing.T) {
	store := newTestSessionService(t)

	rows, err := store.db.QueryContext(context.Background(), `PRAGMA table_info(active_turns)`)

	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	columns := map[string]string{}

	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue sql.NullString
			primaryKey   int
		)
		require.NoError(t, rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey))
		columns[name] = columnType
	}

	require.NoError(t, rows.Err())

	assert.Equal(t, map[string]string{
		"id":                              "TEXT",
		"conversation_id":                 "TEXT",
		"agent":                           "TEXT",
		"model":                           "TEXT",
		"display_model":                   "TEXT",
		"replay_input_json":               "TEXT",
		"output_trace_json":               "TEXT",
		"token_usage_json":                "TEXT",
		"response_id":                     "TEXT",
		"open_function_calls_json":        "TEXT",
		"completed_function_outputs_json": "TEXT",
		"restart_notice_json":             "TEXT",
		"source_metadata_json":            "TEXT",
		"created_at_unix_ns":              "INTEGER",
		"updated_at_unix_ns":              "INTEGER",
	}, columns)

	var indexCount int
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_schema WHERE type = 'index' AND name = 'active_turns_conversation_updated'`).Scan(&indexCount))
	assert.Equal(t, 1, indexCount)
}

func TestSessionServiceMigratesVersionOneWithoutCronScheduleSpec(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, prepareSessionDBPath(workspace))
	db, err := sql.Open("sqlite", sessionDBPath(workspace))
	require.NoError(t, err)
	require.NoError(t, createSessionSchema(context.Background(), db))
	_, err = db.ExecContext(context.Background(), `PRAGMA user_version = 1`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store := newTestSessionServiceAt(t, workspace)

	version, err := sessionDBUserVersion(context.Background(), store.db)
	require.NoError(t, err)
	assert.Equal(t, sessionDBSchemaVersion, version)
}

func TestSessionServiceMigratesVersionTwoWithActiveTurnSchema(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, prepareSessionDBPath(workspace))
	db, err := sql.Open("sqlite", sessionDBPath(workspace))
	require.NoError(t, err)
	require.NoError(t, createSessionSchema(context.Background(), db))
	_, err = db.ExecContext(context.Background(), `DROP TABLE active_turns`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `PRAGMA user_version = 2`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store := newTestSessionServiceAt(t, workspace)
	reopened := newTestSessionServiceAt(t, workspace)

	version, err := sessionDBUserVersion(context.Background(), reopened.db)
	require.NoError(t, err)
	assert.Equal(t, sessionDBSchemaVersion, version)

	var count int
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'active_turns'`).Scan(&count))
	assert.Equal(t, 1, count)
}

func TestSessionServiceMigratesActiveTurnStatusSchemaDeletesLegacyRows(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, prepareSessionDBPath(workspace))
	db, err := sql.Open("sqlite", sessionDBPath(workspace))
	require.NoError(t, err)
	require.NoError(t, createSessionSchema(context.Background(), db))
	_, err = db.ExecContext(context.Background(), `DROP TABLE active_turns`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `CREATE TABLE active_turns (id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, status TEXT NOT NULL, agent TEXT NOT NULL, model TEXT NOT NULL, display_model TEXT NOT NULL, replay_input_json TEXT NOT NULL, output_trace_json TEXT NOT NULL, token_usage_json TEXT NOT NULL, response_id TEXT NOT NULL, open_function_calls_json TEXT NOT NULL, completed_function_outputs_json TEXT NOT NULL, restart_notice_json TEXT NOT NULL, source_metadata_json TEXT NOT NULL, created_at_unix_ns INTEGER NOT NULL, updated_at_unix_ns INTEGER NOT NULL)`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `CREATE INDEX active_turns_conversation_status ON active_turns (conversation_id, status, updated_at_unix_ns)`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `CREATE INDEX active_turns_status_updated ON active_turns (status, updated_at_unix_ns)`)
	require.NoError(t, err)

	for _, status := range []string{"active", "interrupting", "interrupted", "recovering", "completed", "failed", "canceled"} {
		_, err = db.ExecContext(context.Background(), `INSERT INTO active_turns (id, conversation_id, status, agent, model, display_model, replay_input_json, output_trace_json, token_usage_json, response_id, open_function_calls_json, completed_function_outputs_json, restart_notice_json, source_metadata_json, created_at_unix_ns, updated_at_unix_ns) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "turn-"+status, "conversation-"+status, status, "planner", "gpt-5.5", "gpt-5.5", `[]`, `null`, `null`, "", `null`, `null`, "", `{}`, int64(1), int64(1))
		require.NoError(t, err)
	}

	_, err = db.ExecContext(context.Background(), `PRAGMA user_version = 4`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store := newTestSessionServiceAt(t, workspace)
	turns, err := store.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)

	turnIDs := make([]string, 0, len(turns))
	for _, turn := range turns {
		turnIDs = append(turnIDs, turn.Checkpoint.TurnID)
	}

	assert.Empty(t, turnIDs)

	hasStatus, err := activeTurnsHasColumn(context.Background(), store.db, "status")
	require.NoError(t, err)
	assert.False(t, hasStatus)
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
	require.ErrorIs(t, err, ErrGoalAlreadyActive)

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

func TestSessionServiceTreatsEmptyPersistedStateAsMissing(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, prepareSessionDBPath(workspace))
	db, err := sql.Open("sqlite", sessionDBPath(workspace))
	require.NoError(t, err)
	require.NoError(t, createSessionSchema(context.Background(), db))
	_, err = db.ExecContext(context.Background(), `INSERT INTO session_meta (key, value) VALUES (?, ?)`, "rocketclaw_state", "")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store := newTestSessionServiceAt(t, workspace)

	threads, err := managedConversationIDs(context.Background(), store.db)
	require.NoError(t, err)
	assert.Empty(t, threads)
}

func TestSQLiteSessionStoreLoadsLargeImageTurn(t *testing.T) {
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

	_, err = appendSessionEntryDB(context.Background(), errStore{result: errResult{errID: errors.New("no id")}}, "main", entry)
	require.ErrorContains(t, err, "read appended rocketcode session entry id")
}

func TestNewSessionServiceReportsInvalidWorkspace(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace-file")
	require.NoError(t, os.WriteFile(workspace, []byte("not a directory"), 0o600))

	_, err := NewSessionService(workspace)
	require.Error(t, err)
}

func TestAppendSessionEntryIDRejectsWorkspaceWithRocketClawFile(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".rocketclaw"), []byte("not a directory"), 0o600))

	_, err := AppendSessionEntryID(context.Background(), sessionDBPath(workspace), "main", testSessionEntry("user", "assistant"))
	require.ErrorContains(t, err, "create rocketcode session db dir")
}

func TestSQLiteSessionStoreMissingIsEmpty(t *testing.T) {
	service := newTestSessionService(t)
	store := newSessionStore("main", service)
	require.Empty(t, collectEntries(t, store.in()))
}

func TestSQLiteSessionStoreReportsObserveError(t *testing.T) {
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

func TestSQLiteSessionStoreReportsCorruptDB(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionDBPath(workspace)), 0o755))
	require.NoError(t, os.WriteFile(sessionDBPath(workspace), []byte("not-sqlite"), 0o644))

	_, err := ObserveSessionEntries(context.Background(), sessionDBPath(workspace), "main", 0)
	require.Error(t, err)
}

func TestSQLiteSessionStoreReportsCorruptEntry(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionDBPath(workspace)), 0o755))
	db, err := openSessionDB(context.Background(), sessionDBPath(workspace), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.ExecContext(context.Background(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES (?, ?, ?)`, "main", "not-json", time.Unix(1, 0).UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	_, err = ObserveSessionEntries(context.Background(), sessionDBPath(workspace), "main", 0)
	require.ErrorContains(t, err, "parse rocketcode session entry")
}

func TestSQLiteSessionStoreRejectsNilEntry(t *testing.T) {
	_, err := AppendSessionEntryID(context.Background(), sessionDBPath(t.TempDir()), "main", nil)
	require.ErrorContains(t, err, "rocketcode session entry is required")
}

func TestAppendSessionEntryIDRejectsBlankConversationID(t *testing.T) {
	workspace := t.TempDir()
	dbPath := sessionDBPath(workspace)
	entry := testSessionEntry("blank conversation", "assistant")

	_, err := AppendSessionEntryID(context.Background(), dbPath, " \t ", entry)
	require.EqualError(t, err, "conversation ID is required")
}

func TestSessionDBPathReturnsWorkspaceSessionDB(t *testing.T) {
	workspace := t.TempDir()

	assert.Equal(t, filepath.Join(workspace, ".rocketclaw", "state.sqlite3"), sessionDBPath(workspace))
}

func TestSessionDBPathInUsesWorkDir(t *testing.T) {
	workspace := t.TempDir()
	service, err := NewSessionServiceIn(workspace, ".femtoclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(t.Context())) })

	assert.Equal(t, filepath.Join(workspace, ".femtoclaw", "state.sqlite3"), sessionDBPathIn(workspace, ".femtoclaw"))
	assert.FileExists(t, filepath.Join(workspace, ".femtoclaw", "state.sqlite3"))
	assert.NoDirExists(t, filepath.Join(workspace, ".rocketclaw"))
}

func TestSQLiteSessionStoreRejectsEscapingDBSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "sessions.sqlite3")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionDBPath(workspace)), 0o755))
	require.NoError(t, os.Symlink(outside, sessionDBPath(workspace)))

	_, err := ObserveSessionEntries(context.Background(), sessionDBPath(workspace), "main", 0)
	require.Error(t, err)
}

func TestSessionInspectionMissingDBDoesNotCreateRuntimeDir(t *testing.T) {
	workspace := t.TempDir()

	summaries, err := ListSessionsInOptions(context.Background(), workspace, config.DefaultRuntimeDir, SessionListOptions{})
	require.NoError(t, err)
	assert.Empty(t, summaries)

	entries, err := ObserveSessionEntries(context.Background(), sessionDBPath(workspace), "main", 0)
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.NoDirExists(t, filepath.Join(workspace, ".rocketclaw"))
}

func TestSessionDeleteRejectsEscapingDBSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "sessions.sqlite3")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionDBPath(workspace)), 0o755))
	require.NoError(t, os.Symlink(outside, sessionDBPath(workspace)))

	_, err := DeleteSession(context.Background(), workspace, "main")
	require.ErrorContains(t, err, "rocketcode session db must not be a symlink")
}

func TestListSessionsIncludesLastMessages(t *testing.T) {
	workspace := t.TempDir()
	dbPath := sessionDBPath(workspace)

	_, err := AppendSessionEntryID(context.Background(), dbPath, "main", testSessionEntry("first user", "first assistant"))
	require.NoError(t, err)
	_, err = AppendSessionEntryID(context.Background(), dbPath, "main", testSessionEntry("second\nuser", "second assistant"))
	require.NoError(t, err)
	_, err = AppendSessionEntryID(context.Background(), dbPath, "slack-thread:D123:111.222", testSessionEntry("thread user", "thread assistant"))
	require.NoError(t, err)

	summaries, err := ListSessionsInOptions(context.Background(), workspace, config.DefaultRuntimeDir, SessionListOptions{})
	require.NoError(t, err)
	require.Len(t, summaries, 2)

	assert.Equal(t, SessionSummary{ConversationID: "main", Turns: 2, LastUpdated: summaries[0].LastUpdated, LastUserMessage: "second\nuser", LastAssistantMessage: "second assistant"}, summaries[0])
	assert.Equal(t, SessionSummary{ConversationID: "slack-thread:D123:111.222", Turns: 1, LastUpdated: summaries[1].LastUpdated, LastUserMessage: "thread user", LastAssistantMessage: "thread assistant"}, summaries[1])
}

func TestListSessionsOptionsBoundsByLatestUpdate(t *testing.T) {
	workspace := t.TempDir()
	dbPath := sessionDBPath(workspace)
	since := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	until := since.Add(48 * time.Hour)

	_, err := AppendSessionEntryID(context.Background(), dbPath, "old", testSessionEntryAt(since.Add(-time.Second), "old"))
	require.NoError(t, err)
	_, err = AppendSessionEntryID(context.Background(), dbPath, "inside", testSessionEntryAt(since.Add(time.Hour), "inside"))
	require.NoError(t, err)
	_, err = AppendSessionEntryID(context.Background(), dbPath, "boundary", testSessionEntryAt(since, "boundary"))
	require.NoError(t, err)
	_, err = AppendSessionEntryID(context.Background(), dbPath, "until", testSessionEntryAt(until, "until"))
	require.NoError(t, err)

	summaries, err := ListSessionsInOptions(context.Background(), workspace, config.DefaultRuntimeDir, SessionListOptions{Since: since, Until: until})
	require.NoError(t, err)
	require.Len(t, summaries, 2)
	assert.Equal(t, "inside", summaries[0].ConversationID)
	assert.Equal(t, "boundary", summaries[1].ConversationID)

	summaries, err = ListSessionsInOptions(context.Background(), workspace, config.DefaultRuntimeDir, SessionListOptions{Since: since})
	require.NoError(t, err)
	require.Len(t, summaries, 3)
	assert.Equal(t, "until", summaries[0].ConversationID)

	summaries, err = ListSessionsInOptions(context.Background(), workspace, config.DefaultRuntimeDir, SessionListOptions{Until: until})
	require.NoError(t, err)
	require.Len(t, summaries, 3)
	assert.Equal(t, "inside", summaries[0].ConversationID)
}

func TestListSessionsOptionsLimitUsesMostRecent(t *testing.T) {
	workspace := t.TempDir()
	dbPath := sessionDBPath(workspace)
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

	for i, conversationID := range []string{"old", "middle", "new"} {
		_, err := AppendSessionEntryID(context.Background(), dbPath, conversationID, testSessionEntryAt(base.Add(time.Duration(i)*time.Hour), conversationID))
		require.NoError(t, err)
	}

	summaries, err := ListSessionsInOptions(context.Background(), workspace, config.DefaultRuntimeDir, SessionListOptions{Limit: 2})
	require.NoError(t, err)
	require.Len(t, summaries, 2)
	assert.Equal(t, "new", summaries[0].ConversationID)
	assert.Equal(t, "middle", summaries[1].ConversationID)
}

func TestListSessionsMissingDBIsEmpty(t *testing.T) {
	summaries, err := ListSessionsInOptions(context.Background(), t.TempDir(), config.DefaultRuntimeDir, SessionListOptions{})
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestListSessionsOptionsMissingDBIsEmpty(t *testing.T) {
	summaries, err := ListSessionsInOptions(context.Background(), t.TempDir(), config.DefaultRuntimeDir, SessionListOptions{Limit: 1})
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestOpenSessionDBReadOnlyRejectsWrites(t *testing.T) {
	workspace := t.TempDir()
	dbPath := sessionDBPath(workspace)
	_, err := AppendSessionEntryID(context.Background(), dbPath, "main", testSessionEntry("user", "assistant"))
	require.NoError(t, err)

	db, err := openSessionDBReadOnly(context.Background(), dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.ExecContext(context.Background(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES (?, ?, ?)`, "main", `{"version":1}`, time.Unix(1, 0).UTC().Format(time.RFC3339Nano))
	require.Error(t, err)
}

func TestCheckAndRecoverSessionDBRecoversIndexPageCorruption(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 command is required for state store recovery")
	}

	workspace := t.TempDir()
	dbPath := sessionDBPath(workspace)
	_, err := AppendSessionEntryID(context.Background(), dbPath, "main", testSessionEntry("user", "assistant"))
	require.NoError(t, err)

	db, err := openSessionDB(context.Background(), dbPath, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	var rootPage, pageSize int64
	require.NoError(t, db.QueryRowContext(context.Background(), `SELECT rootpage FROM sqlite_schema WHERE name = 'session_entries_conversation_id_id'`).Scan(&rootPage))
	require.NoError(t, db.QueryRowContext(context.Background(), `PRAGMA page_size`).Scan(&pageSize))
	require.NoError(t, db.Close())

	file, err := os.OpenFile(dbPath, os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteAt(make([]byte, 128), (rootPage-1)*pageSize)
	require.NoError(t, err)
	require.NoError(t, file.Close())

	result, err := CheckAndRecoverSessionDB(context.Background(), workspace, ".rocketclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	assert.True(t, result.DBExists)
	assert.True(t, result.Healthy)
	assert.True(t, result.Recovered)

	entries, err := ObserveSessionEntries(context.Background(), dbPath, "main", 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	db, err = openSessionDBReadOnly(context.Background(), dbPath)
	require.NoError(t, err)
	require.NoError(t, quickCheckSessionDB(context.Background(), db))
	require.NoError(t, db.Close())
}

func TestListSessionsReportsCorruptEntry(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionDBPath(workspace)), 0o755))
	db, err := openSessionDB(context.Background(), sessionDBPath(workspace), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.ExecContext(context.Background(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES (?, ?, ?)`, "main", "not-json", time.Unix(1, 0).UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	_, err = ListSessionsInOptions(context.Background(), workspace, config.DefaultRuntimeDir, SessionListOptions{})
	require.ErrorContains(t, err, "parse rocketcode session summary entry")
}

func TestListSessionsReportsReplayInputDecodeError(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionDBPath(workspace)), 0o755))
	db, err := openSessionDB(context.Background(), sessionDBPath(workspace), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.ExecContext(context.Background(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES (?, ?, ?)`, "main", `{"version":1,"type":"turn","replay_input":[true]}`, time.Unix(1, 0).UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	_, err = ListSessionsInOptions(context.Background(), workspace, config.DefaultRuntimeDir, SessionListOptions{})
	require.ErrorContains(t, err, "decode rocketcode session summary replay input")
}

func TestListSessionsKeepsSummaryWithInvalidTimestamp(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionDBPath(workspace)), 0o755))
	db, err := openSessionDB(context.Background(), sessionDBPath(workspace), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	data, err := json.Marshal(testSessionEntry("user", "assistant"))
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES (?, ?, ?)`, "main", string(data), "not-a-time")
	require.NoError(t, err)

	summaries, err := ListSessionsInOptions(context.Background(), workspace, config.DefaultRuntimeDir, SessionListOptions{})
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.True(t, summaries[0].LastUpdated.IsZero())
	assert.Equal(t, "user", summaries[0].LastUserMessage)
	assert.Equal(t, "assistant", summaries[0].LastAssistantMessage)
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
		case errors.Is(err, ErrGoalAlreadyActive):
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

func TestOpenSessionDBWaitsForTransientWriteLock(t *testing.T) {
	workspace := t.TempDir()
	dbPath := sessionDBPath(workspace)
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o755))

	setup, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = setup.ExecContext(context.Background(), `CREATE TABLE session_entries (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL, entry_json TEXT NOT NULL, entry_timestamp TEXT NOT NULL)`)
	require.NoError(t, err)
	require.NoError(t, setup.Close())

	blocker, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, blocker.Close()) })

	tx, err := blocker.BeginTx(context.Background(), nil)
	require.NoError(t, err)

	_, err = tx.ExecContext(context.Background(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES (?, ?, ?)`, "main", `{"version":1,"type":"turn"}`, time.Unix(1, 0).UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	errCh := make(chan error, 1)

	go func() {
		db, errOpen := openSessionDB(context.Background(), dbPath, slog.New(slog.DiscardHandler))
		if errOpen == nil {
			errOpen = db.Close()
		}

		errCh <- errOpen
	}()

	select {
	case err := <-errCh:
		require.NoError(t, err)
		require.Fail(t, "openSessionDB returned while the write lock was still held")
	case <-time.After(50 * time.Millisecond):
	}

	require.NoError(t, tx.Rollback())
	require.NoError(t, <-errCh)
}

func TestOpenSessionDBConfiguresSQLitePolicy(t *testing.T) {
	workspace := t.TempDir()
	dbPath := sessionDBPath(workspace)
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o755))

	db, err := openSessionDB(context.Background(), dbPath, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	stats := db.Stats()
	assert.Equal(t, 1, stats.MaxOpenConnections)

	for _, tt := range []struct {
		name string
		want string
	}{
		{name: "journal_mode", want: "wal"},
		{name: "synchronous", want: "1"},
		{name: "busy_timeout", want: "30000"},
		{name: "cache_size", want: "-64000"},
		{name: "mmap_size", want: "268435456"},
		{name: "temp_store", want: "2"},
		{name: "auto_vacuum", want: "2"},
		{name: "page_size", want: "4096"},
	} {
		var got string
		require.NoError(t, db.QueryRowContext(context.Background(), "PRAGMA "+tt.name).Scan(&got), tt.name)
		assert.Equal(t, tt.want, got, tt.name)
	}
}

func TestDeleteSessionDeletesOnlyTarget(t *testing.T) {
	workspace := t.TempDir()
	dbPath := sessionDBPath(workspace)
	_, err := AppendSessionEntryID(context.Background(), dbPath, "main", testSessionEntry("main", "assistant"))
	require.NoError(t, err)
	_, err = AppendSessionEntryID(context.Background(), dbPath, "thread", testSessionEntry("thread", "assistant"))
	require.NoError(t, err)

	deleted, err := DeleteSession(context.Background(), workspace, "main")
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)

	mainEntries, err := ObserveSessionEntries(context.Background(), dbPath, "main", 0)
	require.NoError(t, err)
	assert.Empty(t, mainEntries)

	threadEntries, err := ObserveSessionEntries(context.Background(), dbPath, "thread", 0)
	require.NoError(t, err)
	assert.Len(t, threadEntries, 1)
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

func TestSessionServiceIncrementalVacuumsAndCheckpointsWAL(t *testing.T) {
	store := newTestSessionService(t)
	_, err := store.AppendEntryID(context.Background(), "main", testSessionEntry("main", "assistant"))
	require.NoError(t, err)

	vacuumStats, err := store.Vacuum(context.Background())
	require.NoError(t, err)
	assert.True(t, vacuumStats.DBExists)

	checkpointStats, err := store.CheckpointWAL(context.Background())
	require.NoError(t, err)
	assert.Zero(t, checkpointStats.Busy)

	entries, err := store.ObserveEntries(context.Background(), "main", 0)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestSessionServicePersistsExternalMCPSessionMapping(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)

	require.NoError(t, store.UpsertExternalMCPSession("ticket-123", &ExternalMCPSessionState{Agent: " cron ", ConversationID: " external_mcp:cron:abc "}))

	session, ok, err := store.ExternalMCPSession("ticket-123")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ExternalMCPSessionState{Agent: "cron", ConversationID: "external_mcp:cron:abc"}, session)

	require.NoError(t, store.UpsertExternalMCPSession("ticket-123", &ExternalMCPSessionState{Agent: "planner", ConversationID: "external_mcp:planner:def"}))

	session, ok, err = store.ExternalMCPSession("ticket-123")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ExternalMCPSessionState{Agent: "planner", ConversationID: "external_mcp:planner:def"}, session)
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

	require.NoError(t, store.ClearCompletedActiveTurn(context.Background(), "turn-1"))
	turns, err = store.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	assert.Empty(t, turns)
}

func TestSessionServiceRejectsBlankKeys(t *testing.T) {
	store := newTestSessionService(t)

	require.ErrorContains(t, store.UpsertThread(" ", ThreadState{Agent: "agent"}), "thread conversation ID is required")
	require.ErrorContains(t, store.UpsertExternalMCPSession(" ", &ExternalMCPSessionState{}), "external MCP conversation ID is required")
}

func TestLoadRocketClawStateHandlesMissingAndClosedDB(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, prepareSessionDBPath(workspace))
	db, err := openSessionDB(context.Background(), sessionDBPath(workspace), slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	state, err := loadRocketClawState(context.Background(), db)
	require.NoError(t, err)
	assert.Empty(t, state)

	require.NoError(t, db.Close())
	_, err = loadRocketClawState(context.Background(), db)
	require.ErrorContains(t, err, "read persisted state")
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
	dbPath := sessionDBPath(workspace)
	cutoff := time.Unix(1_700_000_000, 0).UTC()
	oldTime := cutoff.Add(-time.Second)
	newTime := cutoff.Add(time.Second)

	oldThread := SlackThreadConversationID("DOLD", slackTestTS(oldTime))
	activeOldThread := SlackThreadConversationID("DACTIVE", slackTestTS(oldTime))
	newThread := SlackThreadConversationID("DNEW", slackTestTS(newTime))

	boundaryThread := SlackThreadConversationID("DBOUNDARY", slackTestTS(cutoff))
	for _, conversationID := range []string{oldThread, activeOldThread, newThread, boundaryThread, "slack-thread:D123:not-a-time"} {
		require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner"}))
	}

	for conversationID, ts := range map[string]time.Time{
		oldThread:                      oldTime,
		activeOldThread:                newTime,
		"slack-thread:D123:not-a-time": oldTime,
		"external_mcp:cron:orphan":     oldTime,
		"cron:daily:old":               oldTime,
		"one-off-cron:daily:old":       oldTime,
		"cron:daily:new":               newTime,
		"slack-thread:DORPHAN:1.000":   oldTime,
	} {
		_, err := AppendSessionEntryID(context.Background(), dbPath, conversationID, testSessionEntryAt(ts, conversationID))
		require.NoError(t, err)
	}

	require.NoError(t, store.MarkRestartRequester(context.Background(), oldThread))
	require.NoError(t, store.MarkRestartRequester(context.Background(), activeOldThread))
	require.NoError(t, store.MarkRestartRequester(context.Background(), oldThread))

	stats, err := store.PruneStateBefore(context.Background(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, PruneStateStats{Threads: 1, SessionRows: 5}, stats)

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
		entries, err := ObserveSessionEntries(context.Background(), dbPath, conversationID, 0)
		require.NoError(t, err)
		assert.Empty(t, entries, conversationID)
	}

	for _, conversationID := range []string{activeOldThread, "slack-thread:D123:not-a-time", "cron:daily:new"} {
		entries, err := ObserveSessionEntries(context.Background(), dbPath, conversationID, 0)
		require.NoError(t, err)
		assert.Len(t, entries, 1, conversationID)
	}
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

func (errStore) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
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
