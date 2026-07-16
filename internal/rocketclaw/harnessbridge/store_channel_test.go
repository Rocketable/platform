package harnessbridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFreshSchemaHasNoSeedState(t *testing.T) {
	store, err := NewSessionServiceIn(t.TempDir(), ".rocketclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	var count int
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'response_checkpoints'`).Scan(&count))
	assert.Zero(t, count)

	hasSeeded, err := tableHasColumn(t.Context(), store.db, "managed_conversations", "seeded_from_response")
	require.NoError(t, err)
	assert.False(t, hasSeeded)

	rows, err := store.db.QueryContext(t.Context(), `PRAGMA table_info(external_mcp_sessions)`)

	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var columns []string

	for rows.Next() {
		var (
			cid, notNull, primaryKey int
			name, columnType         string
			defaultValue             sql.NullString
		)
		require.NoError(t, rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey))
		columns = append(columns, name)
	}

	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"external_conversation_id", "private_conversation_id", "managed_conversation_id", "agent", "slack_channel"}, columns)

	_, err = store.db.ExecContext(t.Context(), `INSERT INTO external_mcp_sessions VALUES ('blank-private', '', 'managed', 'agent', '#ops')`)
	require.Error(t, err)
	_, err = store.db.ExecContext(t.Context(), `INSERT INTO external_mcp_sessions VALUES ('blank-managed', 'private', '', 'agent', '#ops')`)
	require.Error(t, err)
}

func TestSessionStoreRequiresConversationID(t *testing.T) {
	store, err := NewSessionServiceIn(t.TempDir(), ".rocketclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	_, err = store.ObserveEntries(t.Context(), " ", 0)
	require.EqualError(t, err, "conversation ID is required")
}

func TestExternalMCPBindingPersistsPrivateAndManagedConversations(t *testing.T) {
	store, err := NewSessionServiceIn(t.TempDir(), ".rocketclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	binding := ExternalMCPSessionState{Agent: "private-agent", PrivateConversationID: "external_mcp:private", ManagedConversationID: "slack-thread:C1:2.2", SlackChannel: "#ops"}
	require.NoError(t, store.UpsertExternalMCPSession("deploy-42", &binding))

	got, ok, err := store.ExternalMCPSession("deploy-42")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, binding, got)

	for _, conversationID := range []string{binding.PrivateConversationID, binding.ManagedConversationID} {
		externalConversationID, found, foundOK, err := store.ExternalMCPSessionByConversationID(conversationID)
		require.NoError(t, err)
		require.True(t, foundOK)
		assert.Equal(t, "deploy-42", externalConversationID)
		assert.Equal(t, binding, found)
	}
}

func TestExternalMCPConversationRegistrationIsAtomic(t *testing.T) {
	store, err := NewSessionServiceIn(t.TempDir(), ".rocketclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	require.NoError(t, store.RegisterExternalMCPConversation("existing", "managed-agent", &ExternalMCPSessionState{Agent: "private-agent", PrivateConversationID: "external_mcp:private-1", ManagedConversationID: "slack-thread:C1:1.1", SlackChannel: "#ops"}))
	thread, ok, err := store.Thread("slack-thread:C1:1.1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "managed-agent", thread.Agent)

	err = store.RegisterExternalMCPConversation("existing", "other-managed", &ExternalMCPSessionState{Agent: "other-private", PrivateConversationID: "external_mcp:private-2", ManagedConversationID: "slack-thread:C1:2.2", SlackChannel: "#ops"})
	require.Error(t, err)

	_, ok, err = store.Thread("slack-thread:C1:2.2")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestExternalMCPConversationCleanupRemovesOnlyBoundSessions(t *testing.T) {
	store, err := NewSessionServiceIn(t.TempDir(), ".rocketclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	session := ExternalMCPSessionState{Agent: "private-agent", PrivateConversationID: "external_mcp:private", ManagedConversationID: "slack-thread:C1:1.1", SlackChannel: "#ops"}
	require.NoError(t, store.RegisterExternalMCPConversation("public", "managed-agent", &session))

	for _, conversationID := range []string{session.PrivateConversationID, session.ManagedConversationID, "unrelated"} {
		_, err := store.AppendEntryID(t.Context(), conversationID, testSessionEntry(conversationID, "assistant"))
		require.NoError(t, err)
	}

	require.NoError(t, store.UpsertThread("unrelated", ThreadState{Agent: "other"}))

	require.NoError(t, store.RemoveExternalMCPConversation("public"))

	_, ok, err := store.ExternalMCPSession("public")
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = store.Thread(session.ManagedConversationID)
	require.NoError(t, err)
	assert.False(t, ok)

	for _, conversationID := range []string{session.PrivateConversationID, session.ManagedConversationID} {
		entries, err := store.ObserveEntries(t.Context(), conversationID, 0)
		require.NoError(t, err)
		assert.Empty(t, entries)
	}

	entries, err := store.ObserveEntries(t.Context(), "unrelated", 0)
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	_, ok, err = store.Thread("unrelated")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestMigrationRemovesMainDMAndSeedState(t *testing.T) {
	workspace := t.TempDir()
	runtimeDir := filepath.Join(workspace, ".rocketclaw")
	require.NoError(t, os.MkdirAll(runtimeDir, 0o755))

	// Direct SQL intentionally constructs the previous schema shape for migration coverage.
	db, err := sql.Open("sqlite", filepath.Join(runtimeDir, "state.sqlite3"))
	require.NoError(t, err)

	for _, statement := range []string{
		`CREATE TABLE managed_conversations (conversation_id TEXT PRIMARY KEY, agent TEXT NOT NULL, seeded_from_response TEXT NOT NULL, created_by TEXT NOT NULL)`,
		`CREATE TABLE external_mcp_sessions (external_conversation_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, agent TEXT NOT NULL)`,
		`CREATE TABLE session_entries (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL, entry_json TEXT NOT NULL, entry_timestamp TEXT NOT NULL)`,
		`CREATE TABLE conversation_goals (conversation_id TEXT PRIMARY KEY, objective TEXT NOT NULL, check_script TEXT NOT NULL, max_turns INTEGER NOT NULL, turns_used INTEGER NOT NULL, status TEXT NOT NULL, note TEXT NOT NULL, created_at_unix_ns INTEGER NOT NULL, updated_at_unix_ns INTEGER NOT NULL)`,
		`CREATE TABLE scheduled_messages (scheduled_message_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, agent TEXT NOT NULL, message TEXT NOT NULL, due_at_unix_ns INTEGER NOT NULL, recurring INTEGER NOT NULL, interval_ns INTEGER NOT NULL)`,
		`CREATE TABLE pending_restart_notifications (conversation_id TEXT PRIMARY KEY)`,
		`INSERT INTO managed_conversations VALUES ('main', 'planner', '', '')`,
		`INSERT INTO managed_conversations VALUES ('slack-thread:D1:1.1', 'planner', '', '')`,
		`INSERT INTO managed_conversations VALUES ('slack-thread:C1:2.2', 'main', 'external_mcp:planner:private', '')`,
		`INSERT INTO external_mcp_sessions VALUES ('deploy-42', 'external_mcp:planner:private', 'stale')`,
		`INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('slack-thread:C1:2.2', '{"type":"response_thread_seed"}', '2026-01-01T00:00:00Z')`,
		`INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('external_mcp:planner:private', '{"type":"message"}', '2026-01-02T00:00:00Z')`,
		`INSERT INTO conversation_goals VALUES ('external_mcp:planner:private', 'ship', '', 5, 2, 'active', '', 1, 2)`,
		`INSERT INTO scheduled_messages VALUES ('scheduled-1', 'external_mcp:planner:private', 'planner', 'continue', 10, 0, 0)`,
		`INSERT INTO pending_restart_notifications VALUES ('external_mcp:planner:private')`,
		`PRAGMA user_version = 5`,
	} {
		_, err = db.ExecContext(context.Background(), statement)
		require.NoError(t, err)
	}

	require.NoError(t, db.Close())

	store, err := NewSessionServiceIn(workspace, ".rocketclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	_, ok, err := store.Thread("main")
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = store.Thread("slack-thread:D1:1.1")
	require.NoError(t, err)
	assert.False(t, ok)
	thread, ok, err := store.Thread("slack-thread:C1:2.2")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "main", thread.Agent)

	session, ok, err := store.ExternalMCPSession("deploy-42")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, session.PrivateConversationID)
	assert.Equal(t, "slack-thread:C1:2.2", session.ManagedConversationID)
	assert.Equal(t, "C1", session.SlackChannel)
	assert.Equal(t, "main", session.Agent)

	var count int
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM session_entries WHERE conversation_id = 'slack-thread:C1:2.2' AND json_extract(entry_json, '$.type') = 'message'`).Scan(&count))
	assert.Equal(t, 1, count)
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM conversation_goals WHERE conversation_id = 'slack-thread:C1:2.2'`).Scan(&count))
	assert.Equal(t, 1, count)
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM scheduled_messages WHERE conversation_id = 'slack-thread:C1:2.2'`).Scan(&count))
	assert.Equal(t, 1, count)
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pending_restart_notifications WHERE conversation_id = 'slack-thread:C1:2.2'`).Scan(&count))
	assert.Equal(t, 1, count)

	require.NoError(t, store.Stop(t.Context()))
	store, err = NewSessionServiceIn(workspace, ".rocketclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	_, ok, err = store.ExternalMCPSession("deploy-42")
	require.NoError(t, err)
	assert.True(t, ok)
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM session_entries WHERE conversation_id = 'slack-thread:C1:2.2'`).Scan(&count))
	assert.Equal(t, 1, count)
}

func TestVersionZeroMigrationAppliesChannelOnlyCleanupAndIsIdempotent(t *testing.T) {
	workspace := t.TempDir()
	runtimeDir := filepath.Join(workspace, ".rocketclaw")
	require.NoError(t, os.MkdirAll(runtimeDir, 0o755))
	db, err := sql.Open("sqlite", filepath.Join(runtimeDir, "state.sqlite3"))
	require.NoError(t, err)
	stateJSON, err := json.Marshal(State{
		Threads: map[string]ThreadState{
			"main":                {Agent: "main"},
			"slack-thread:D1:1.1": {Agent: "dm"},
			"slack-thread:C2:3.3": {Agent: "aggregate"},
		},
		Goals:                       map[string]GoalState{"main": {Objective: "old"}, "slack-thread:C2:3.3": {Objective: "keep"}},
		ScheduledMessages:           map[string]ScheduledMessageState{"old": {ConversationID: "main"}, "keep": {ConversationID: "slack-thread:C2:3.3"}},
		PendingRestartNotifications: map[string]bool{"main": true, "slack-thread:C2:3.3": true},
	})
	require.NoError(t, err)

	for _, statement := range []string{
		`CREATE TABLE session_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE managed_conversations (conversation_id TEXT PRIMARY KEY, agent TEXT NOT NULL, seeded_from_response TEXT NOT NULL, created_by TEXT NOT NULL)`,
		`CREATE TABLE external_mcp_sessions (external_conversation_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, agent TEXT NOT NULL)`,
		`CREATE TABLE session_entries (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL, entry_json TEXT NOT NULL, entry_timestamp TEXT NOT NULL)`,
		`CREATE TABLE conversation_goals (conversation_id TEXT PRIMARY KEY, objective TEXT NOT NULL, check_script TEXT NOT NULL, max_turns INTEGER NOT NULL, turns_used INTEGER NOT NULL, status TEXT NOT NULL, note TEXT NOT NULL, created_at_unix_ns INTEGER NOT NULL, updated_at_unix_ns INTEGER NOT NULL)`,
		`CREATE TABLE scheduled_messages (scheduled_message_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, agent TEXT NOT NULL, message TEXT NOT NULL, due_at_unix_ns INTEGER NOT NULL, recurring INTEGER NOT NULL, interval_ns INTEGER NOT NULL)`,
		`CREATE TABLE pending_restart_notifications (conversation_id TEXT PRIMARY KEY)`,
		`CREATE TABLE active_turns (id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, agent TEXT NOT NULL, model TEXT NOT NULL, display_model TEXT NOT NULL, replay_input_json TEXT NOT NULL, output_trace_json TEXT NOT NULL, token_usage_json TEXT NOT NULL, response_id TEXT NOT NULL, open_function_calls_json TEXT NOT NULL, completed_function_outputs_json TEXT NOT NULL, restart_notice_json TEXT NOT NULL, source_metadata_json TEXT NOT NULL, created_at_unix_ns INTEGER NOT NULL, updated_at_unix_ns INTEGER NOT NULL)`,
		`INSERT INTO managed_conversations VALUES ('slack-thread:C1:2.2', 'planner', 'external_mcp:planner:valid', '')`,
		`INSERT INTO managed_conversations VALUES ('slack-thread:D2:4.4', 'dm', 'external_mcp:planner:dm', '')`,
		`INSERT INTO managed_conversations VALUES ('invalid-private', 'planner', 'external_mcp:planner:invalid', '')`,
		`INSERT INTO external_mcp_sessions VALUES ('valid', 'external_mcp:planner:valid', 'stale')`,
		`INSERT INTO external_mcp_sessions VALUES ('old-dm', 'external_mcp:planner:dm', 'stale')`,
		`INSERT INTO external_mcp_sessions VALUES ('invalid', 'external_mcp:planner:invalid', 'stale')`,
		`INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('external_mcp:planner:valid', '{"type":"message"}', '2026-01-01T00:00:00Z')`,
		`INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('external_mcp:planner:dm', '{"type":"message"}', '2026-01-01T00:00:00Z')`,
		`INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('external_mcp:planner:invalid', '{"type":"message"}', '2026-01-01T00:00:00Z')`,
		`INSERT INTO conversation_goals VALUES ('external_mcp:planner:valid', 'ship', '', 5, 1, 'active', '', 1, 2)`,
		`INSERT INTO conversation_goals VALUES ('external_mcp:planner:dm', 'discard', '', 5, 1, 'active', '', 1, 2)`,
		`INSERT INTO conversation_goals VALUES ('external_mcp:planner:invalid', 'discard', '', 5, 1, 'active', '', 1, 2)`,
		`INSERT INTO scheduled_messages VALUES ('valid', 'external_mcp:planner:valid', 'planner', 'continue', 10, 0, 0)`,
		`INSERT INTO scheduled_messages VALUES ('dm', 'external_mcp:planner:dm', 'planner', 'continue', 10, 0, 0)`,
		`INSERT INTO scheduled_messages VALUES ('invalid', 'external_mcp:planner:invalid', 'planner', 'continue', 10, 0, 0)`,
		`INSERT INTO pending_restart_notifications VALUES ('external_mcp:planner:valid')`,
		`INSERT INTO pending_restart_notifications VALUES ('external_mcp:planner:dm')`,
		`INSERT INTO pending_restart_notifications VALUES ('external_mcp:planner:invalid')`,
		`INSERT INTO active_turns VALUES ('valid', 'external_mcp:planner:valid', 'planner', 'model', 'model', '[]', '[]', '{}', '', '[]', '[]', '{}', '{}', 1, 2)`,
		`INSERT INTO active_turns VALUES ('dm', 'external_mcp:planner:dm', 'planner', 'model', 'model', '[]', '[]', '{}', '', '[]', '[]', '{}', '{}', 1, 2)`,
		`INSERT INTO active_turns VALUES ('invalid', 'external_mcp:planner:invalid', 'planner', 'model', 'model', '[]', '[]', '{}', '', '[]', '[]', '{}', '{}', 1, 2)`,
	} {
		_, err = db.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}

	_, err = db.ExecContext(t.Context(), `INSERT INTO session_meta VALUES ('rocketclaw_state', ?)`, string(stateJSON))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := NewSessionServiceIn(workspace, ".rocketclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	thread, ok, err := store.Thread("slack-thread:C1:2.2")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "planner", thread.Agent)

	session, ok, err := store.ExternalMCPSession("valid")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ExternalMCPSessionState{Agent: "planner", ManagedConversationID: "slack-thread:C1:2.2", SlackChannel: "C1"}, session)

	_, ok, err = store.Thread("main")
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = store.ExternalMCPSession("dm")
	require.NoError(t, err)
	assert.False(t, ok)

	for _, conversationID := range []string{"external_mcp:planner:dm", "external_mcp:planner:invalid", "slack-thread:D2:4.4", "invalid-private"} {
		for _, table := range []string{"session_entries", "active_turns", "scheduled_messages", "conversation_goals", "pending_restart_notifications"} {
			var count int
			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table+` WHERE conversation_id = ?`, conversationID).Scan(&count))
			assert.Zero(t, count, "%s retained state for %s", table, conversationID)
		}
	}

	require.NoError(t, store.Stop(t.Context()))

	store, err = NewSessionServiceIn(workspace, ".rocketclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	_, ok, err = store.ExternalMCPSession("valid")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestVersionSixMigrationRemovesRedundantSlackCoordinates(t *testing.T) {
	workspace := t.TempDir()
	runtimeDir := filepath.Join(workspace, ".rocketclaw")
	require.NoError(t, os.MkdirAll(runtimeDir, 0o755))

	db, err := sql.Open("sqlite", filepath.Join(runtimeDir, "state.sqlite3"))
	require.NoError(t, err)

	for _, statement := range []string{
		`CREATE TABLE managed_conversations (conversation_id TEXT PRIMARY KEY, agent TEXT NOT NULL, created_by TEXT NOT NULL)`,
		`CREATE TABLE session_entries (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL, entry_json TEXT NOT NULL, entry_timestamp TEXT NOT NULL)`,
		`CREATE TABLE external_mcp_sessions (external_conversation_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL UNIQUE, agent TEXT NOT NULL, slack_channel TEXT NOT NULL, slack_channel_id TEXT NOT NULL, slack_thread_ts TEXT NOT NULL)`,
		`INSERT INTO managed_conversations VALUES ('main', 'main', '')`,
		`INSERT INTO managed_conversations VALUES ('slack-thread:D1:1.1', 'main', '')`,
		`INSERT INTO managed_conversations VALUES ('slack-thread:C1:2.2', 'main', '')`,
		`INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('main', '{"type":"message"}', '2026-01-01T00:00:00Z')`,
		`INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('slack-thread:D1:1.1', '{"type":"message"}', '2026-01-01T00:00:00Z')`,
		`INSERT INTO external_mcp_sessions VALUES ('deploy-42', 'slack-thread:C1:2.2', 'main', '#ops', 'C1', '2.2')`,
		`PRAGMA user_version = 6`,
	} {
		_, err = db.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}

	require.NoError(t, db.Close())

	store, err := NewSessionServiceIn(workspace, ".rocketclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	session, ok, err := store.ExternalMCPSession("deploy-42")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ExternalMCPSessionState{Agent: "main", ManagedConversationID: "slack-thread:C1:2.2", SlackChannel: "#ops"}, session)

	hasChannelID, err := tableHasColumn(t.Context(), store.db, "external_mcp_sessions", "slack_channel_id")
	require.NoError(t, err)
	assert.False(t, hasChannelID)

	_, ok, err = store.Thread("main")
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = store.Thread("slack-thread:D1:1.1")
	require.NoError(t, err)
	assert.False(t, ok)
	require.NoError(t, store.Stop(t.Context()))
	store, err = NewSessionServiceIn(workspace, ".rocketclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	_, ok, err = store.ExternalMCPSession("deploy-42")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestVersionSevenMigrationPreservesLegacySessionHistory(t *testing.T) {
	workspace := t.TempDir()
	runtimeDir := filepath.Join(workspace, ".rocketclaw")
	require.NoError(t, os.MkdirAll(runtimeDir, 0o755))

	db, err := sql.Open("sqlite", filepath.Join(runtimeDir, "state.sqlite3"))
	require.NoError(t, err)

	mcpEntry := testSessionEntry("legacy request", "legacy answer")
	mcpEntry.ReplayInput[0] = testReplayInput(replayInputMessage{role: "user", text: "[ExternalMCP media=Text]\n\nlegacy request"})[0]
	mcpEntry.ReplayInput = append(mcpEntry.ReplayInput[:1], json.RawMessage(`{"type":"compaction","encrypted_content":"private"}`), mcpEntry.ReplayInput[1])
	slackEntry := testSessionEntry("[Slack media=Text]\n\nprivate human note", "managed answer")
	metadataReplay, err := replayInputForMessage("developer", externalMCPMetadataDeveloperMessage("This external MCP thread has metadata:", externalMCPMetadataEnv("slack-thread:C1:2.2", map[string]string{"owner": "alice"})))
	require.NoError(t, err)

	metadataEntry := &rocketcode.SessionEntry{Version: 1, Type: externalMCPMetadataEntryType, Timestamp: time.Unix(1, 0).UTC(), ReplayInput: metadataReplay}
	mcpJSON, err := json.Marshal(mcpEntry)
	require.NoError(t, err)
	slackJSON, err := json.Marshal(slackEntry)
	require.NoError(t, err)
	metadataJSON, err := json.Marshal(metadataEntry)
	require.NoError(t, err)

	for _, statement := range []string{
		`CREATE TABLE managed_conversations (conversation_id TEXT PRIMARY KEY, agent TEXT NOT NULL, created_by TEXT NOT NULL)`,
		`CREATE TABLE session_entries (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL, entry_json TEXT NOT NULL, entry_timestamp TEXT NOT NULL)`,
		`CREATE TABLE external_mcp_sessions (external_conversation_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL UNIQUE, agent TEXT NOT NULL, slack_channel TEXT NOT NULL)`,
		`INSERT INTO managed_conversations VALUES ('slack-thread:C1:2.2', 'managed-agent', '')`,
		`INSERT INTO external_mcp_sessions VALUES ('deploy-42', 'slack-thread:C1:2.2', 'private-agent', '#ops')`,
		`PRAGMA user_version = 7`,
	} {
		_, err = db.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}

	_, err = db.ExecContext(t.Context(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('slack-thread:C1:2.2', ?, '2026-01-01T00:00:00Z'), ('slack-thread:C1:2.2', ?, '2026-01-02T00:00:00Z'), ('slack-thread:C1:2.2', ?, '2026-01-03T00:00:00Z')`, string(metadataJSON), string(mcpJSON), string(slackJSON))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := NewSessionServiceIn(workspace, ".rocketclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	session, ok, err := store.ExternalMCPSession("deploy-42")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ExternalMCPSessionState{Agent: "private-agent", ManagedConversationID: "slack-thread:C1:2.2", SlackChannel: "#ops"}, session)
	thread, ok, err := store.Thread(session.ManagedConversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "managed-agent", thread.Agent)

	managed, err := store.ObserveEntries(t.Context(), session.ManagedConversationID, 0)
	require.NoError(t, err)
	require.Len(t, managed, 3)
	assert.Contains(t, string(managed[0].Entry.ReplayInput[0]), `ROCKETCLAW_CONVERSATION_ID=\"slack-thread:C1:2.2\"`)
	assert.Equal(t, mcpEntry.ReplayInput, managed[1].Entry.ReplayInput)
	assert.Equal(t, slackEntry.ReplayInput, managed[2].Entry.ReplayInput)
}

func TestFutureSchemaVersionIsRejected(t *testing.T) {
	workspace := t.TempDir()
	runtimeDir := filepath.Join(workspace, ".rocketclaw")
	require.NoError(t, os.MkdirAll(runtimeDir, 0o755))
	db, err := sql.Open("sqlite", filepath.Join(runtimeDir, "state.sqlite3"))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `PRAGMA user_version = 99`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = NewSessionServiceIn(workspace, ".rocketclaw", slog.New(slog.DiscardHandler))
	require.ErrorContains(t, err, "unsupported rocketclaw state schema version 99")
}
