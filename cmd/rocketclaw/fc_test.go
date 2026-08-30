package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fcDSNs sync.Map

func fcAppendSessionEntryID(ctx context.Context, workspace, conversationID string, entry *rocketcode.SessionEntry) (int64, error) {
	dsn := fcTestDSN(workspace)
	if dsn == "" {
		var err error
		dsn, err = harnessbridgetest.IsolatedTestDatabaseURL()
		if err != nil {
			return 0, err
		}
		fcDSNs.Store(workspace, dsn)
	}
	service, err := backend.NewSessionServiceIn(workspace, config.DefaultRuntimeDir, dsn, slog.New(slog.DiscardHandler))
	if err != nil {
		return 0, err
	}
	defer func() { _ = service.Stop(ctx) }()
	return service.AppendEntryID(ctx, conversationID, entry)
}

func fcTestDSN(workspace string) string {
	v, ok := fcDSNs.Load(workspace)
	if !ok {
		return ""
	}
	return v.(string)
}

func TestWriteFCListIncludesLastMessages(t *testing.T) {
	workspace := t.TempDir()
	_, err := fcAppendSessionEntryID(t.Context(), workspace, "main", fcTestEntry("hello", "hi there"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, writeFCListInOptions(t.Context(), workspace, config.DefaultRuntimeDir, fcTestDSN(workspace), backend.SessionListOptions{}, true, &out))

	text := out.String()
	assert.Contains(t, text, "CONVERSATION_ID")
	assert.Contains(t, text, "main")
	assert.Contains(t, text, "hello")
	assert.Contains(t, text, "hi there")
}

func TestWriteFCObserveRequiresConversationID(t *testing.T) {
	workspace := t.TempDir()
	_, err := fcAppendSessionEntryID(t.Context(), workspace, "main", fcTestEntry("main user", "main assistant"))
	require.NoError(t, err)
	_, err = fcAppendSessionEntryID(t.Context(), workspace, "thread", fcTestEntry("thread user", "thread assistant"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.EqualError(t, writeFCObserveIn(t.Context(), workspace, config.DefaultRuntimeDir, fcTestDSN(workspace), "", false, time.Millisecond, &out), "conversation ID is required")
	assert.Empty(t, out.String())
}

func TestWriteFCObserveFollowEmitsLaterRows(t *testing.T) {
	workspace := t.TempDir()
	_, err := fcAppendSessionEntryID(t.Context(), workspace, "main", fcTestEntry("later user", "later assistant"))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	writer := &cancelingWriter{cancel: cancel, ch: make(chan string, 1)}

	require.ErrorIs(t, writeFCObserveIn(ctx, workspace, config.DefaultRuntimeDir, fcTestDSN(workspace), "main", true, 10*time.Millisecond, writer), context.Canceled)
	line := <-writer.ch
	assert.Contains(t, line, "later user")
}

func TestRunFCObserveSelectsConversation(t *testing.T) {
	workspace := t.TempDir()
	_, err := fcAppendSessionEntryID(t.Context(), workspace, "main", fcTestEntry("main user", "main assistant"))
	require.NoError(t, err)
	_, err = fcAppendSessionEntryID(t.Context(), workspace, "thread", fcTestEntry("thread user", "thread assistant"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runFCObserveIn(workspace, config.DefaultRuntimeDir, fcTestDSN(workspace), []string{"thread"}, &out))

	assert.Contains(t, out.String(), "thread user")
	assert.NotContains(t, out.String(), "main user")
}

func TestRunFCObserveRejectsExtraArguments(t *testing.T) {
	var out bytes.Buffer

	err := runFCObserveIn(t.TempDir(), config.DefaultRuntimeDir, "", []string{"one", "two"}, &out)
	require.ErrorContains(t, err, "requires exactly one conversation-id")
}

func TestRunFCObserveRejectsBadFlag(t *testing.T) {
	var out bytes.Buffer

	err := runFCObserveIn(t.TempDir(), config.DefaultRuntimeDir, "", []string{"--bad"}, &out)
	require.ErrorContains(t, err, "parse rocketcode observe flags")
}

func TestRunFCListLoadsConfig(t *testing.T) {
	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })

	_, err = fcAppendSessionEntryID(t.Context(), workspace, "main", fcTestEntry("hello", "hi"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, defaultConfigPath), []byte(fcTestConfigJSONWithDSN(fcTestDSN(workspace))), 0o600))

	output := captureStdout(t, func() error { return runFC([]string{"list"}) })
	assert.Contains(t, output, "main")
}

func TestRunFCListFiltersSinceDuration(t *testing.T) {
	workspace := t.TempDir()
	now := time.Now().UTC()
	_, err := fcAppendSessionEntryID(t.Context(), workspace, "old", fcTestEntryAt(now.Add(-48*time.Hour), "old user", "assistant"))
	require.NoError(t, err)
	_, err = fcAppendSessionEntryID(t.Context(), workspace, "recent", fcTestEntryAt(now.Add(-time.Hour), "recent user", "assistant"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runFCListIn(workspace, config.DefaultRuntimeDir, fcTestDSN(workspace), []string{"--since", "24h"}, &out))
	assert.Contains(t, out.String(), "recent")
	assert.NotContains(t, out.String(), "old")
}

func TestRunFCListFiltersRFC3339Range(t *testing.T) {
	workspace := t.TempDir()
	since := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	until := since.Add(24 * time.Hour)
	_, err := fcAppendSessionEntryID(t.Context(), workspace, "before", fcTestEntryAt(since.Add(-time.Second), "before user", "assistant"))
	require.NoError(t, err)
	_, err = fcAppendSessionEntryID(t.Context(), workspace, "inside", fcTestEntryAt(since.Add(time.Hour), "inside user", "assistant"))
	require.NoError(t, err)
	_, err = fcAppendSessionEntryID(t.Context(), workspace, "until", fcTestEntryAt(until, "until user", "assistant"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runFCListIn(workspace, config.DefaultRuntimeDir, fcTestDSN(workspace), []string{"--since", since.Format(time.RFC3339), "--until", until.Format(time.RFC3339)}, &out))
	assert.Contains(t, out.String(), "inside")
	assert.NotContains(t, out.String(), "before")
	assert.NotContains(t, out.String(), "until")
}

func TestRunFCListLimitUsesMostRecent(t *testing.T) {
	workspace := t.TempDir()
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	for i, conversationID := range []string{"old", "middle", "new"} {
		_, err := fcAppendSessionEntryID(t.Context(), workspace, conversationID, fcTestEntryAt(base.Add(time.Duration(i)*time.Hour), conversationID+" user", "assistant"))
		require.NoError(t, err)
	}

	var out bytes.Buffer
	require.NoError(t, runFCListIn(workspace, config.DefaultRuntimeDir, fcTestDSN(workspace), []string{"--limit", "1"}, &out))
	assert.Contains(t, out.String(), "new")
	assert.NotContains(t, out.String(), "middle")
	assert.NotContains(t, out.String(), "old")
}

func TestRunFCListNoMessagePreviewOmitsPreviewColumns(t *testing.T) {
	workspace := t.TempDir()
	_, err := fcAppendSessionEntryID(t.Context(), workspace, "main", fcTestEntry("hidden user", "hidden assistant"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runFCListIn(workspace, config.DefaultRuntimeDir, fcTestDSN(workspace), []string{"--no-message-preview"}, &out))
	assert.Contains(t, out.String(), "CONVERSATION_ID")
	assert.NotContains(t, out.String(), "LAST_USER_MESSAGE")
	assert.NotContains(t, out.String(), "hidden user")
	assert.NotContains(t, out.String(), "hidden assistant")
}

func TestRunFCListRejectsBadValues(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "extra argument", args: []string{"extra"}, want: "list does not accept arguments"},
		{name: "negative limit", args: []string{"--limit", "-1"}, want: "list limit must be non-negative"},
		{name: "bad since", args: []string{"--since", "not-a-time"}, want: "parse rocketcode list since"},
		{name: "bad until", args: []string{"--until", "24h"}, want: "parse rocketcode list until"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runFCListIn(t.TempDir(), config.DefaultRuntimeDir, "", tt.args, &out)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestRunFCDispatchesConfigBackedCommands(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	_, err := fcAppendSessionEntryID(t.Context(), workspace, "main", fcTestEntry("dispatch user", "dispatch assistant"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(fcTestConfigJSONWithDSN(fcTestDSN(workspace))), 0o600))

	output := captureStdout(t, func() error { return runFC([]string{"observe", "main"}) })
	assert.Contains(t, output, "dispatch user")

	output = captureStdout(t, func() error { return runFC([]string{"delete", "main"}) })
	assert.Contains(t, output, "deleted 1 turns")

	output = captureStdout(t, func() error { return runFC([]string{"check"}) })
	assert.Equal(t, "state store ok\n", output)
}

func TestRunFCNoArgsPrintsHelpWithoutConfig(t *testing.T) {
	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })

	output := captureStdout(t, func() error { return runFC(nil) })
	assert.Contains(t, output, "rocketclaw fc list")
	assert.Contains(t, output, "rocketclaw fc observe")
	assert.Contains(t, output, "rocketclaw fc delete")
	assert.Contains(t, output, "rocketclaw fc check")
	assert.Contains(t, output, "rocketclaw fc migrate")
}

func TestRunFCHelpAliasesLoadConfigAndPrintHelp(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)

	err := runFC([]string{"help"})
	require.ErrorContains(t, err, "load config")

	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(fcTestConfigJSONWithDSN("postgres://localhost/rocketclaw_test?sslmode=disable")), 0o600))

	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		output := captureStdout(t, func() error { return runFC(args) })
		assert.Contains(t, output, "rocketclaw fc list")
		assert.Contains(t, output, "rocketclaw fc observe")
		assert.Contains(t, output, "rocketclaw fc delete")
		assert.Contains(t, output, "rocketclaw fc check")
		assert.Contains(t, output, "rocketclaw fc migrate")
	}
}

func TestRunFCUnknownCommand(t *testing.T) {
	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })
	require.NoError(t, os.WriteFile(filepath.Join(workspace, defaultConfigPath), []byte(fcTestConfigJSONWithDSN("postgres://localhost/rocketclaw_test?sslmode=disable")), 0o600))

	err = runFC([]string{"bogus"})
	require.ErrorContains(t, err, `unknown rocketcode command "bogus"`)
}

func TestRunFCDeleteRequiresConversationID(t *testing.T) {
	var out bytes.Buffer

	err := runFCDeleteIn(t.TempDir(), config.DefaultRuntimeDir, "", nil, &out)
	require.ErrorContains(t, err, "conversation-id")
}

func TestRunFCDeleteReportsDeleteAndWriteErrors(t *testing.T) {
	workspaceFile := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.WriteFile(workspaceFile, []byte("not a directory"), 0o600))

	err := runFCDeleteIn(workspaceFile, config.DefaultRuntimeDir, "", []string{"main"}, io.Discard)
	require.ErrorContains(t, err, "delete rocketcode session")

	dsn, errDSN := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, errDSN)
	err = runFCDeleteIn(t.TempDir(), config.DefaultRuntimeDir, dsn, []string{"main"}, failingWriter{})
	require.ErrorContains(t, err, "write rocketcode delete result")
	require.ErrorIs(t, err, errFailingWrite)
}

func TestRunFCDeleteRejectsBadFlag(t *testing.T) {
	var out bytes.Buffer

	err := runFCDeleteIn(t.TempDir(), config.DefaultRuntimeDir, "", []string{"--bad"}, &out)
	require.ErrorContains(t, err, "parse rocketcode delete flags")
}

func TestRunFCDeleteDeletesOnlyTarget(t *testing.T) {
	workspace := t.TempDir()
	_, err := fcAppendSessionEntryID(t.Context(), workspace, "main", fcTestEntry("main user", "main assistant"))
	require.NoError(t, err)
	_, err = fcAppendSessionEntryID(t.Context(), workspace, "thread", fcTestEntry("thread user", "thread assistant"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runFCDeleteIn(workspace, config.DefaultRuntimeDir, fcTestDSN(workspace), []string{"main"}, &out))
	assert.Contains(t, out.String(), "deleted 1 turns")

	mainEntries, err := backend.ObserveSessionEntries(t.Context(), workspace, config.DefaultRuntimeDir, fcTestDSN(workspace), "main", 0)
	require.NoError(t, err)
	assert.Empty(t, mainEntries)

	threadEntries, err := backend.ObserveSessionEntries(t.Context(), workspace, config.DefaultRuntimeDir, fcTestDSN(workspace), "thread", 0)
	require.NoError(t, err)
	assert.Len(t, threadEntries, 1)
}

func TestRunFCDeleteMissingDBReportsZero(t *testing.T) {
	var out bytes.Buffer
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	require.NoError(t, runFCDeleteIn(t.TempDir(), config.DefaultRuntimeDir, dsn, []string{"main"}, &out))
	assert.Contains(t, out.String(), "deleted 0 turns")
}

func TestRunFCDeleteRefusesWhileStateStoreLocked(t *testing.T) {
	workspace := t.TempDir()
	lock, err := backend.AcquireStateStoreLock(workspace, ".rocketclaw")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Close()) })

	err = runFCDeleteIn(workspace, config.DefaultRuntimeDir, fcTestDSN(workspace), []string{"main"}, io.Discard)
	require.ErrorContains(t, err, "rocketclaw daemon is running; stop it before running fc delete")
	require.ErrorIs(t, err, backend.ErrStateStoreLocked)
}

func TestRunFCCheck(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, runFCCheckIn(t.TempDir(), config.DefaultRuntimeDir, dsn, nil, &out))
	assert.Equal(t, "state store ok\n", out.String())
}

func TestRunFCCheckReportsStoreError(t *testing.T) {
	err := runFCCheckIn(t.TempDir(), config.DefaultRuntimeDir, "postgres://127.0.0.1:1/none?sslmode=disable", nil, io.Discard)
	require.Error(t, err)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	err = runFCCheckIn(t.TempDir(), config.DefaultRuntimeDir, dsn, nil, failingWriter{})
	require.ErrorIs(t, err, errFailingWrite)
}

func TestRunFCCheckRejectsArguments(t *testing.T) {
	var out bytes.Buffer
	err := runFCCheckIn(t.TempDir(), config.DefaultRuntimeDir, "", []string{"extra"}, &out)
	require.ErrorContains(t, err, "check does not accept arguments")
}

func TestRunFCCheckRefusesWhileStateStoreLocked(t *testing.T) {
	workspace := t.TempDir()
	lock, err := backend.AcquireStateStoreLock(workspace, ".rocketclaw")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Close()) })

	err = runFCCheckIn(workspace, config.DefaultRuntimeDir, fcTestDSN(workspace), nil, io.Discard)
	require.ErrorContains(t, err, "rocketclaw daemon is running; stop it before running fc check")
	require.ErrorIs(t, err, backend.ErrStateStoreLocked)
}

func TestRunFCMigrateCopiesThenInsertsZero(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	writeFCSQLite(t, workspace)
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(fcTestConfigJSONWithDSN(dsn)), 0o600))

	output := captureStdout(t, func() error { return runFC([]string{"migrate"}) })
	assert.Equal(t, migrateProgressOutput(2), output)

	output = captureStdout(t, func() error { return runFC([]string{"migrate"}) })
	assert.Equal(t, migrateProgressOutput(0), output)
}

func TestRunFCMigrateRejectsArguments(t *testing.T) {
	err := runFCMigrateIn(t.TempDir(), config.DefaultRuntimeDir, "", []string{"extra"}, io.Discard)
	require.ErrorIs(t, err, errFCMigrateArgs)
}

func TestRunFCMigrateRejectsBadFlag(t *testing.T) {
	err := runFCMigrateIn(t.TempDir(), config.DefaultRuntimeDir, "", []string{"--bad"}, io.Discard)
	require.ErrorIs(t, err, errParseFCMigrateFlags)
}

func TestRunFCMigrateReportsWriterError(t *testing.T) {
	workspace := t.TempDir()
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	writeFCSQLite(t, workspace)
	err = runFCMigrateIn(workspace, config.DefaultRuntimeDir, dsn, nil, failingWriter{})
	require.ErrorIs(t, err, errFailingWrite)
}

func TestRunFCMigrateRequiresSQLiteFile(t *testing.T) {
	workspace := t.TempDir()
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	_, err = fcAppendSessionEntryID(t.Context(), workspace, "main", fcTestEntry("keep", "me"))
	require.NoError(t, err)
	dsn = fcTestDSN(workspace)

	var out bytes.Buffer
	err = runFCMigrateIn(workspace, config.DefaultRuntimeDir, dsn, nil, &out)
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Empty(t, out.String())

	summaries, err := backend.ListSessionsInOptions(t.Context(), workspace, config.DefaultRuntimeDir, dsn, backend.SessionListOptions{})
	require.NoError(t, err)
	require.Len(t, summaries, 1)
}

func TestRunFCMigrateRequiresConfig(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeFCSQLite(t, workspace)

	err := runFC([]string{"migrate"})
	require.ErrorIs(t, err, os.ErrNotExist)
}

func migrateProgressOutput(inserted int64) string {
	return fmt.Sprintf(`managed_conversations: 1
conversation_goals: 0
external_mcp_sessions: 0
scheduled_messages: 0
cron_schedules: 0
cron_schedule_runs: 0
pending_restart_notifications: 0
active_turns: 0
session_entries: 1
inserted %d rows
`, inserted)
}

func writeFCSQLite(t *testing.T, workspace string) {
	t.Helper()
	sqlitePath := filepath.Join(workspace, config.DefaultRuntimeDir, "state.sqlite3")
	require.NoError(t, os.MkdirAll(filepath.Dir(sqlitePath), 0o755))
	src, err := sql.Open("sqlite", sqlitePath)
	require.NoError(t, err)
	_, err = src.Exec(`CREATE TABLE session_entries (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL, entry_json TEXT NOT NULL, entry_timestamp TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = src.Exec(`CREATE TABLE managed_conversations (conversation_id TEXT PRIMARY KEY, agent TEXT NOT NULL, created_by TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = src.Exec(`INSERT INTO managed_conversations VALUES ('main', 'planner', 'human')`)
	require.NoError(t, err)
	_, err = src.Exec(`INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES (7, 'main', '{"version":1}', ?)`, time.Unix(1, 0).UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	_, err = src.Exec(`PRAGMA user_version = 9`)
	require.NoError(t, err)
	require.NoError(t, src.Close())
}

func TestWriteFCListReportsFlushError(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	err = writeFCListInOptions(t.Context(), t.TempDir(), config.DefaultRuntimeDir, dsn, backend.SessionListOptions{}, true, failingWriter{})
	require.ErrorContains(t, err, "flush rocketcode session list")
	require.ErrorIs(t, err, errFailingWrite)
}

func TestWriteFCObserveReportsWriterErrors(t *testing.T) {
	workspace := t.TempDir()
	_, err := fcAppendSessionEntryID(t.Context(), workspace, "main", fcTestEntry("hello", "hi"))
	require.NoError(t, err)

	err = writeFCObserveIn(t.Context(), workspace, config.DefaultRuntimeDir, fcTestDSN(workspace), "main", false, time.Millisecond, failingWriter{})
	require.ErrorContains(t, err, "write rocketcode session entry")
	require.ErrorIs(t, err, errFailingWrite)
}

var errFailingWrite = errors.New("write failed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errFailingWrite
}

type cancelingWriter struct {
	cancel context.CancelFunc
	ch     chan string
}

func (w *cancelingWriter) Write(p []byte) (int, error) {
	w.ch <- string(p)

	w.cancel()

	return len(p), nil
}

func fcTestEntry(user, assistant string) *rocketcode.SessionEntry {
	return &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), ResponseID: "", Model: "gpt-5.5", ReplayInput: fcTestReplayInput("user", user, "assistant", assistant)}
}

func fcTestEntryAt(ts time.Time, user, assistant string) *rocketcode.SessionEntry {
	entry := fcTestEntry(user, assistant)
	entry.Timestamp = ts.UTC()

	return entry
}

func fcTestReplayInput(values ...string) []json.RawMessage {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		message := responses.EasyInputMessageParam{Role: responses.EasyInputMessageRole(values[i]), Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(values[i+1])}, Type: "message"}
		items = append(items, responses.ResponseInputItemUnionParam{OfMessage: &message})
	}

	raw, err := rocketcode.ReplayInputFromParams(items)
	if err != nil {
		panic(err)
	}

	return raw
}

func fcTestConfigJSONWithDSN(dsn string) string {
	return fmt.Sprintf(`{
  "workspace": ".",
  "database_url": %q,
  "openai": {
    "api_key": "shared-key"
  },
  "slack": {
    "bot_token": "xoxb-test",
    "app_token": "xapp-test",
    "channels": [{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]
  }
}`, dsn)
}
