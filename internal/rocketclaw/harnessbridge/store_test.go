package harnessbridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLiteSessionStoreAppendAndLoad(t *testing.T) {
	service := newTestSessionService(t)
	store := newSessionStore(" ", service)
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

	defaulted := testSessionEntry("default conversation", "assistant")
	_, err = service.AppendEntryID(context.Background(), " \t ", defaulted)
	require.NoError(t, err)

	observed, err = service.ObserveEntries(context.Background(), " \n ", 0)
	require.NoError(t, err)
	require.Len(t, observed, 3)
	assert.Equal(t, *defaulted, observed[2].Entry)
}

func TestSessionServiceScheduledMessages(t *testing.T) {
	store := newTestSessionService(t)
	dueAt := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)

	require.NoError(t, store.updateState(func(state *State) {
		state.ScheduledMessages = map[string]ScheduledMessageState{"schedule-1": {ConversationID: "slack-thread:D123:111.222", Agent: "helper", Message: "later", DueAt: dueAt}}
	}))

	state, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, map[string]ScheduledMessageState{"schedule-1": {ConversationID: "slack-thread:D123:111.222", Agent: "helper", Message: "later", DueAt: dueAt}}, state.ScheduledMessages)
}

func TestSessionServiceScheduledMessageDefaults(t *testing.T) {
	store := newTestSessionService(t)
	dueAt := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)

	require.NoError(t, store.updateState(func(state *State) {
		state.ScheduledMessages = map[string]ScheduledMessageState{"schedule-1": {ConversationID: mainConversationID, Agent: mainConversationID, Message: "later", DueAt: dueAt}}
	}))

	state, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, ScheduledMessageState{ConversationID: mainConversationID, Agent: mainConversationID, Message: "later", DueAt: dueAt}, state.ScheduledMessages["schedule-1"])
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
	db, err := openSessionDB(context.Background(), sessionDBPath(workspace))
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

func TestAppendSessionEntryIDDefaultsBlankConversationID(t *testing.T) {
	workspace := t.TempDir()
	dbPath := sessionDBPath(workspace)
	entry := testSessionEntry("blank conversation", "assistant")

	_, err := AppendSessionEntryID(context.Background(), dbPath, " \t ", entry)
	require.NoError(t, err)

	observed, err := ObserveSessionEntries(context.Background(), dbPath, mainConversationID, 0)
	require.NoError(t, err)
	require.Len(t, observed, 1)
	assert.Equal(t, *entry, observed[0].Entry)

	observed, err = ObserveSessionEntries(context.Background(), dbPath, " \t ", 0)
	require.NoError(t, err)
	require.Len(t, observed, 1)
	assert.Equal(t, *entry, observed[0].Entry)
}

func TestSessionDBPathReturnsWorkspaceSessionDB(t *testing.T) {
	workspace := t.TempDir()

	assert.Equal(t, filepath.Join(workspace, ".rocketclaw", "state.sqlite3"), SessionDBPath(workspace))
}

func TestSessionDBPathInUsesWorkDir(t *testing.T) {
	workspace := t.TempDir()
	service, err := NewSessionServiceIn(workspace, ".femtoclaw")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(t.Context())) })

	assert.Equal(t, filepath.Join(workspace, ".femtoclaw", "state.sqlite3"), SessionDBPathIn(workspace, ".femtoclaw"))
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

	summaries, err := ListSessions(context.Background(), workspace)
	require.NoError(t, err)
	assert.Empty(t, summaries)

	entries, err := ObserveSessionEntries(context.Background(), sessionDBPath(workspace), "main", 0)
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.NoDirExists(t, filepath.Join(workspace, ".rocketclaw"))
}

func TestSessionMaintenanceRejectsEscapingDBSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "sessions.sqlite3")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionDBPath(workspace)), 0o755))
	require.NoError(t, os.Symlink(outside, sessionDBPath(workspace)))

	_, err := DeleteSession(context.Background(), workspace, "main")
	require.ErrorContains(t, err, "rocketcode session db must not be a symlink")

	_, err = VacuumSessions(context.Background(), workspace)
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

	summaries, err := ListSessions(context.Background(), workspace)
	require.NoError(t, err)
	require.Len(t, summaries, 2)

	assert.Equal(t, SessionSummary{ConversationID: "main", Turns: 2, LastUpdated: summaries[0].LastUpdated, LastUserMessage: "second\nuser", LastAssistantMessage: "second assistant"}, summaries[0])
	assert.Equal(t, SessionSummary{ConversationID: "slack-thread:D123:111.222", Turns: 1, LastUpdated: summaries[1].LastUpdated, LastUserMessage: "thread user", LastAssistantMessage: "thread assistant"}, summaries[1])
}

func TestListSessionsMissingDBIsEmpty(t *testing.T) {
	summaries, err := ListSessions(context.Background(), t.TempDir())
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

func TestListSessionsReportsCorruptEntry(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionDBPath(workspace)), 0o755))
	db, err := openSessionDB(context.Background(), sessionDBPath(workspace))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.ExecContext(context.Background(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES (?, ?, ?)`, "main", "not-json", time.Unix(1, 0).UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	_, err = ListSessions(context.Background(), workspace)
	require.ErrorContains(t, err, "parse rocketcode session summary entry")
}

func TestListSessionsReportsReplayInputDecodeError(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionDBPath(workspace)), 0o755))
	db, err := openSessionDB(context.Background(), sessionDBPath(workspace))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.ExecContext(context.Background(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES (?, ?, ?)`, "main", `{"version":1,"type":"turn","replay_input":[true]}`, time.Unix(1, 0).UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	_, err = ListSessions(context.Background(), workspace)
	require.ErrorContains(t, err, "decode rocketcode session summary replay input")
}

func TestListSessionsKeepsSummaryWithInvalidTimestamp(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionDBPath(workspace)), 0o755))
	db, err := openSessionDB(context.Background(), sessionDBPath(workspace))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	data, err := json.Marshal(testSessionEntry("user", "assistant"))
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES (?, ?, ?)`, "main", string(data), "not-a-time")
	require.NoError(t, err)

	summaries, err := ListSessions(context.Background(), workspace)
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

			errCh <- service.UpsertThread(fmt.Sprintf("thread-%02d", i), "main")
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

	state, err := service.Load()
	require.NoError(t, err)
	assert.Len(t, state.Threads, 25)
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
		db, errOpen := openSessionDB(context.Background(), dbPath)
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

	db, err := openSessionDB(context.Background(), dbPath)
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

func TestVacuumSessionsMissingDBIsNoop(t *testing.T) {
	stats, err := VacuumSessions(context.Background(), t.TempDir())
	require.NoError(t, err)
	assert.False(t, stats.DBExists)
}

func TestVacuumSessionsReportsCorruptDB(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionDBPath(workspace)), 0o755))
	require.NoError(t, os.WriteFile(sessionDBPath(workspace), []byte("not-sqlite"), 0o600))

	_, err := VacuumSessions(context.Background(), workspace)
	require.ErrorContains(t, err, "initialize rocketcode session db")
}

func TestVacuumSessionsPreservesRows(t *testing.T) {
	workspace := t.TempDir()
	dbPath := sessionDBPath(workspace)
	_, err := AppendSessionEntryID(context.Background(), dbPath, "main", testSessionEntry("main", "assistant"))
	require.NoError(t, err)

	stats, err := VacuumSessions(context.Background(), workspace)
	require.NoError(t, err)
	assert.True(t, stats.DBExists)

	entries, err := ObserveSessionEntries(context.Background(), dbPath, "main", 0)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestSessionServiceVacuumsAndCheckpointsWAL(t *testing.T) {
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

	require.NoError(t, store.UpsertExternalMCPSession("ticket-123", ExternalMCPSessionState{Agent: " cron ", ConversationID: " external_mcp:cron:abc "}))

	state, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, ExternalMCPSessionState{Agent: "cron", ConversationID: "external_mcp:cron:abc"}, state.ExternalMCPSessions["ticket-123"])

	require.NoError(t, store.UpsertExternalMCPSession("ticket-123", ExternalMCPSessionState{Agent: "planner", ConversationID: "external_mcp:planner:def"}))

	state, err = store.Load()
	require.NoError(t, err)
	assert.Equal(t, ExternalMCPSessionState{Agent: "planner", ConversationID: "external_mcp:planner:def"}, state.ExternalMCPSessions["ticket-123"])
}

func TestSessionServiceMarksThreadSeeded(t *testing.T) {
	store := newTestSessionService(t)

	require.NoError(t, store.MarkThreadSeeded(" thread ", " response "))

	state, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, ThreadState{Agent: "main", SeededFromResponse: "response"}, state.Threads["thread"])

	require.NoError(t, store.UpsertThread("thread", " planner "))
	require.NoError(t, store.MarkThreadSeeded("thread", " other-response "))

	state, err = store.Load()
	require.NoError(t, err)
	assert.Equal(t, ThreadState{Agent: "planner", SeededFromResponse: "other-response"}, state.Threads["thread"])
}

func TestDiscordStateKeys(t *testing.T) {
	assert.Equal(t, "discord-thread:123", DiscordThreadConversationID(" 123 "))
	assert.Empty(t, DiscordThreadConversationID(" "))
	assert.Equal(t, "discord-response:C123:456", DiscordResponseCheckpointKey(" C123 ", " 456 "))
	assert.Empty(t, DiscordResponseCheckpointKey("", "456"))
	assert.Empty(t, DiscordResponseCheckpointKey("C123", ""))
}

func TestSessionServiceRejectsBlankKeys(t *testing.T) {
	store := newTestSessionService(t)

	require.ErrorContains(t, store.UpsertThread(" ", "agent"), "thread conversation ID is required")
	require.ErrorContains(t, store.MarkThreadSeeded(" ", "response"), "thread conversation ID is required")
	require.ErrorContains(t, store.UpsertResponseCheckpoint(" ", ResponseCheckpointState{}), "response checkpoint key is required")
	require.ErrorContains(t, store.UpsertExternalMCPSession(" ", ExternalMCPSessionState{}), "external MCP conversation ID is required")
}

func TestSessionServiceReportsCorruptState(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionDBPath(workspace)), 0o755))
	db, err := openSessionDB(context.Background(), sessionDBPath(workspace))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.ExecContext(context.Background(), `INSERT INTO session_meta (key, value) VALUES (?, ?)`, "rocketclaw_state", "not-json")
	require.NoError(t, err)

	service := newTestSessionServiceAt(t, workspace)
	_, err = service.Load()
	require.ErrorContains(t, err, "parse persisted state")
}

func TestLoadRocketClawStateHandlesMissingAndClosedDB(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, prepareSessionDBPath(workspace))
	db, err := openSessionDB(context.Background(), sessionDBPath(workspace))
	require.NoError(t, err)

	state, err := loadRocketClawState(context.Background(), db)
	require.NoError(t, err)
	assert.Empty(t, state)

	require.NoError(t, db.Close())
	_, err = loadRocketClawState(context.Background(), db)
	require.ErrorContains(t, err, "read persisted state")
}

func TestSaveRocketClawStateReportsWriteError(t *testing.T) {
	err := saveRocketClawState(context.Background(), errStore{errExec: errors.New("no state write")}, State{Threads: map[string]ThreadState{"main": {Agent: "planner"}}})
	require.ErrorContains(t, err, "write persisted state")
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
	oldCheckpoint := SlackResponseCheckpointKey("DOLD", slackTestTS(oldTime))
	newCheckpoint := SlackResponseCheckpointKey("DNEW", slackTestTS(newTime))
	boundaryCheckpoint := SlackResponseCheckpointKey("DBOUNDARY", slackTestTS(cutoff))

	for _, conversationID := range []string{oldThread, activeOldThread, newThread, boundaryThread, "slack-thread:D123:not-a-time"} {
		require.NoError(t, store.UpsertThread(conversationID, "planner"))
	}

	for _, key := range []string{oldCheckpoint, newCheckpoint, boundaryCheckpoint, "slack-response:D123:not-a-time"} {
		require.NoError(t, store.UpsertResponseCheckpoint(key, ResponseCheckpointState{ConversationID: "main", SessionEntryID: 1, ResponseID: "resp", Model: "gpt-5.5", AssistantText: "answer"}))
	}

	require.NoError(t, store.UpsertExternalMCPSession("old-mcp", ExternalMCPSessionState{Agent: "cron", ConversationID: "external_mcp:cron:old"}))
	require.NoError(t, store.UpsertExternalMCPSession("new-mcp", ExternalMCPSessionState{Agent: "cron", ConversationID: "external_mcp:cron:new"}))
	require.NoError(t, store.UpsertExternalMCPSession("boundary-mcp", ExternalMCPSessionState{Agent: "cron", ConversationID: "external_mcp:cron:boundary"}))
	require.NoError(t, store.UpsertExternalMCPSession("empty-mcp", ExternalMCPSessionState{Agent: "cron", ConversationID: "external_mcp:cron:empty"}))
	require.NoError(t, store.UpsertExternalMCPSession("blank-mcp", ExternalMCPSessionState{Agent: "cron", ConversationID: " "}))

	for conversationID, ts := range map[string]time.Time{
		oldThread:                      oldTime,
		activeOldThread:                newTime,
		"slack-thread:D123:not-a-time": oldTime,
		"external_mcp:cron:old":        oldTime,
		"external_mcp:cron:new":        newTime,
		"external_mcp:cron:boundary":   cutoff,
		"external_mcp:cron:orphan":     oldTime,
		"cron:daily:old":               oldTime,
		"one-off-cron:daily:old":       oldTime,
		"cron:daily:new":               newTime,
		"slack-thread:DORPHAN:1.000":   oldTime,
		"main":                         oldTime,
	} {
		_, err := AppendSessionEntryID(context.Background(), dbPath, conversationID, testSessionEntryAt(ts, conversationID, "assistant"))
		require.NoError(t, err)
	}

	require.NoError(t, store.MarkRestartRequester(context.Background(), oldThread))
	require.NoError(t, store.MarkRestartRequester(context.Background(), activeOldThread))
	require.NoError(t, store.MarkRestartRequester(context.Background(), "external_mcp:cron:old"))

	stats, err := store.PruneStateBefore(context.Background(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, PruneStateStats{Threads: 1, ResponseCheckpoints: 1, ExternalMCPSessions: 3, SessionRows: 6}, stats)

	state, err := store.Load()
	require.NoError(t, err)
	assert.NotContains(t, state.Threads, oldThread)
	assert.Contains(t, state.Threads, activeOldThread)
	assert.Contains(t, state.Threads, newThread)
	assert.Contains(t, state.Threads, boundaryThread)
	assert.Contains(t, state.Threads, "slack-thread:D123:not-a-time")
	assert.NotContains(t, state.ResponseCheckpoints, oldCheckpoint)
	assert.Contains(t, state.ResponseCheckpoints, newCheckpoint)
	assert.Contains(t, state.ResponseCheckpoints, boundaryCheckpoint)
	assert.Contains(t, state.ResponseCheckpoints, "slack-response:D123:not-a-time")
	assert.NotContains(t, state.ExternalMCPSessions, "old-mcp")
	assert.NotContains(t, state.ExternalMCPSessions, "empty-mcp")
	assert.NotContains(t, state.ExternalMCPSessions, "blank-mcp")
	assert.Contains(t, state.ExternalMCPSessions, "new-mcp")
	assert.Contains(t, state.ExternalMCPSessions, "boundary-mcp")
	assert.NotContains(t, state.PendingRestartNotifications, oldThread)
	assert.NotContains(t, state.PendingRestartNotifications, "external_mcp:cron:old")
	assert.Contains(t, state.PendingRestartNotifications, activeOldThread)

	for _, conversationID := range []string{oldThread, "external_mcp:cron:old", "external_mcp:cron:orphan", "cron:daily:old", "one-off-cron:daily:old", "slack-thread:DORPHAN:1.000"} {
		entries, err := ObserveSessionEntries(context.Background(), dbPath, conversationID, 0)
		require.NoError(t, err)
		assert.Empty(t, entries, conversationID)
	}

	for _, conversationID := range []string{"main", activeOldThread, "slack-thread:D123:not-a-time", "external_mcp:cron:new", "external_mcp:cron:boundary", "cron:daily:new"} {
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

func testSessionEntryAt(ts time.Time, user, assistant string) *harness.SessionEntry {
	entry := testSessionEntry(user, assistant)
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
