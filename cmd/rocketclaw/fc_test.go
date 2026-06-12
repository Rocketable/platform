package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteFCListIncludesLastMessages(t *testing.T) {
	workspace := t.TempDir()
	_, err := harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "main", fcTestEntry("hello", "hi there"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, writeFCListInOptions(t.Context(), workspace, config.DefaultWorkDir, harnessbridge.SessionListOptions{}, true, &out))

	text := out.String()
	assert.Contains(t, text, "CONVERSATION_ID")
	assert.Contains(t, text, "main")
	assert.Contains(t, text, "hello")
	assert.Contains(t, text, "hi there")
}

func TestWriteFCObserveDefaultsToMain(t *testing.T) {
	workspace := t.TempDir()
	_, err := harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "main", fcTestEntry("main user", "main assistant"))
	require.NoError(t, err)
	_, err = harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "thread", fcTestEntry("thread user", "thread assistant"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, writeFCObserveIn(t.Context(), workspace, config.DefaultWorkDir, "", false, time.Millisecond, &out))

	assert.Contains(t, out.String(), "main user")
	assert.NotContains(t, out.String(), "thread user")
}

func TestWriteFCObserveFollowEmitsLaterRows(t *testing.T) {
	workspace := t.TempDir()
	_, err := harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "main", fcTestEntry("later user", "later assistant"))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	writer := &cancelingWriter{cancel: cancel, ch: make(chan string, 1)}

	require.ErrorIs(t, writeFCObserveIn(ctx, workspace, config.DefaultWorkDir, "main", true, 10*time.Millisecond, writer), context.Canceled)
	line := <-writer.ch
	assert.Contains(t, line, "later user")
}

func TestRunFCObserveSelectsConversation(t *testing.T) {
	workspace := t.TempDir()
	_, err := harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "main", fcTestEntry("main user", "main assistant"))
	require.NoError(t, err)
	_, err = harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "thread", fcTestEntry("thread user", "thread assistant"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runFCObserveIn(workspace, config.DefaultWorkDir, []string{"thread"}, &out))

	assert.Contains(t, out.String(), "thread user")
	assert.NotContains(t, out.String(), "main user")
}

func TestRunFCObserveRejectsExtraArguments(t *testing.T) {
	var out bytes.Buffer

	err := runFCObserveIn(t.TempDir(), config.DefaultWorkDir, []string{"one", "two"}, &out)
	require.ErrorContains(t, err, "at most one conversation-id")
}

func TestRunFCObserveRejectsBadFlag(t *testing.T) {
	var out bytes.Buffer

	err := runFCObserveIn(t.TempDir(), config.DefaultWorkDir, []string{"--bad"}, &out)
	require.ErrorContains(t, err, "parse rocketcode observe flags")
}

func TestRunFCListLoadsConfig(t *testing.T) {
	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })

	require.NoError(t, os.WriteFile(filepath.Join(workspace, defaultConfigPath), []byte(fcTestConfigJSON()), 0o600))
	_, err = harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "main", fcTestEntry("hello", "hi"))
	require.NoError(t, err)

	output := captureStdout(t, func() error { return runFC([]string{"list"}) })
	assert.Contains(t, output, "main")
}

func TestRunFCListFiltersSinceDuration(t *testing.T) {
	workspace := t.TempDir()
	now := time.Now().UTC()
	_, err := harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "old", fcTestEntryAt(now.Add(-48*time.Hour), "old user", "assistant"))
	require.NoError(t, err)
	_, err = harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "recent", fcTestEntryAt(now.Add(-time.Hour), "recent user", "assistant"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runFCListIn(workspace, config.DefaultWorkDir, []string{"--since", "24h"}, &out))
	assert.Contains(t, out.String(), "recent")
	assert.NotContains(t, out.String(), "old")
}

func TestRunFCListFiltersRFC3339Range(t *testing.T) {
	workspace := t.TempDir()
	since := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	until := since.Add(24 * time.Hour)
	_, err := harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "before", fcTestEntryAt(since.Add(-time.Second), "before user", "assistant"))
	require.NoError(t, err)
	_, err = harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "inside", fcTestEntryAt(since.Add(time.Hour), "inside user", "assistant"))
	require.NoError(t, err)
	_, err = harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "until", fcTestEntryAt(until, "until user", "assistant"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runFCListIn(workspace, config.DefaultWorkDir, []string{"--since", since.Format(time.RFC3339), "--until", until.Format(time.RFC3339)}, &out))
	assert.Contains(t, out.String(), "inside")
	assert.NotContains(t, out.String(), "before")
	assert.NotContains(t, out.String(), "until")
}

func TestRunFCListLimitUsesMostRecent(t *testing.T) {
	workspace := t.TempDir()
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	for i, conversationID := range []string{"old", "middle", "new"} {
		_, err := harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), conversationID, fcTestEntryAt(base.Add(time.Duration(i)*time.Hour), conversationID+" user", "assistant"))
		require.NoError(t, err)
	}

	var out bytes.Buffer
	require.NoError(t, runFCListIn(workspace, config.DefaultWorkDir, []string{"--limit", "1"}, &out))
	assert.Contains(t, out.String(), "new")
	assert.NotContains(t, out.String(), "middle")
	assert.NotContains(t, out.String(), "old")
}

func TestRunFCListNoMessagePreviewOmitsPreviewColumns(t *testing.T) {
	workspace := t.TempDir()
	_, err := harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "main", fcTestEntry("hidden user", "hidden assistant"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runFCListIn(workspace, config.DefaultWorkDir, []string{"--no-message-preview"}, &out))
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
			err := runFCListIn(t.TempDir(), config.DefaultWorkDir, tt.args, &out)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestRunFCDispatchesConfigBackedCommands(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(fcTestConfigJSON()), 0o600))
	_, err := harnessbridge.AppendSessionEntryID(
		t.Context(),
		harnessbridge.SessionDBPath(workspace),
		"main",
		fcTestEntry("dispatch user", "dispatch assistant"),
	)
	require.NoError(t, err)

	output := captureStdout(t, func() error { return runFC([]string{"observe", "main"}) })
	assert.Contains(t, output, "dispatch user")

	output = captureStdout(t, func() error { return runFC([]string{"delete", "--no-vacuum", "main"}) })
	assert.Contains(t, output, "deleted 1 turns; skipped vacuum")

	output = captureStdout(t, func() error { return runFC([]string{"vacuum"}) })
	assert.Contains(t, output, "vacuumed sessions")
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
}

func TestRunFCHelpAliasesLoadConfigAndPrintHelp(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)

	err := runFC([]string{"help"})
	require.ErrorContains(t, err, "load config")

	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(fcTestConfigJSON()), 0o600))

	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		output := captureStdout(t, func() error { return runFC(args) })
		assert.Contains(t, output, "rocketclaw fc list")
		assert.Contains(t, output, "rocketclaw fc observe")
		assert.Contains(t, output, "rocketclaw fc delete")
	}
}

func TestRunFCUnknownCommand(t *testing.T) {
	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })
	require.NoError(t, os.WriteFile(filepath.Join(workspace, defaultConfigPath), []byte(fcTestConfigJSON()), 0o600))

	err = runFC([]string{"bogus"})
	require.ErrorContains(t, err, `unknown rocketcode command "bogus"`)
}

func TestRunFCDeleteRequiresConversationID(t *testing.T) {
	var out bytes.Buffer

	err := runFCDeleteIn(t.TempDir(), config.DefaultWorkDir, nil, &out)
	require.ErrorContains(t, err, "conversation-id")
}

func TestRunFCDeleteReportsDeleteAndWriteErrors(t *testing.T) {
	workspaceFile := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.WriteFile(workspaceFile, []byte("not a directory"), 0o600))

	err := runFCDeleteIn(workspaceFile, config.DefaultWorkDir, []string{"main"}, io.Discard)
	require.ErrorContains(t, err, "delete rocketcode session")

	err = runFCDeleteIn(t.TempDir(), config.DefaultWorkDir, []string{"--no-vacuum", "main"}, failingWriter{})
	require.ErrorContains(t, err, "write rocketcode delete result")
	require.ErrorIs(t, err, errFailingWrite)
}

func TestRunFCDeleteReportsHintWriteError(t *testing.T) {
	workspace := t.TempDir()
	_, err := harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "main", fcTestEntry("hello", "hi"))
	require.NoError(t, err)

	writer := new(failOnSecondWrite)
	err = runFCDeleteIn(workspace, config.DefaultWorkDir, []string{"--no-vacuum", "main"}, writer)
	require.ErrorContains(t, err, "write rocketcode delete hint")
	require.ErrorIs(t, err, errFailingWrite)
}

func TestRunFCDeleteRejectsBadFlag(t *testing.T) {
	var out bytes.Buffer

	err := runFCDeleteIn(t.TempDir(), config.DefaultWorkDir, []string{"--bad"}, &out)
	require.ErrorContains(t, err, "parse rocketcode delete flags")
}

func TestRunFCDeleteNoVacuumDeletesOnlyTarget(t *testing.T) {
	workspace := t.TempDir()
	_, err := harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "main", fcTestEntry("main user", "main assistant"))
	require.NoError(t, err)
	_, err = harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "thread", fcTestEntry("thread user", "thread assistant"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runFCDeleteIn(workspace, config.DefaultWorkDir, []string{"--no-vacuum", "main"}, &out))
	assert.Contains(t, out.String(), "deleted 1 turns; skipped vacuum")

	mainEntries, err := harnessbridge.ObserveSessionEntries(t.Context(), harnessbridge.SessionDBPath(workspace), "main", 0)
	require.NoError(t, err)
	assert.Empty(t, mainEntries)

	threadEntries, err := harnessbridge.ObserveSessionEntries(t.Context(), harnessbridge.SessionDBPath(workspace), "thread", 0)
	require.NoError(t, err)
	assert.Len(t, threadEntries, 1)
}

func TestRunFCDeleteDefaultVacuumPreservesOtherSessions(t *testing.T) {
	workspace := t.TempDir()
	_, err := harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "main", fcTestEntry("main user", "main assistant"))
	require.NoError(t, err)
	_, err = harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "thread", fcTestEntry("thread user", "thread assistant"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runFCDeleteIn(workspace, config.DefaultWorkDir, []string{"main"}, &out))
	assert.Contains(t, out.String(), "deleted 1 turns")
	assert.Contains(t, out.String(), "vacuumed sessions")

	threadEntries, err := harnessbridge.ObserveSessionEntries(t.Context(), harnessbridge.SessionDBPath(workspace), "thread", 0)
	require.NoError(t, err)
	assert.Len(t, threadEntries, 1)
}

func TestRunFCDeleteMissingDBReportsZero(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, runFCDeleteIn(t.TempDir(), config.DefaultWorkDir, []string{"main"}, &out))
	assert.Contains(t, out.String(), "deleted 0 turns")
	assert.Contains(t, out.String(), "nothing to vacuum")
}

func TestRunFCDeleteRefusesWhileStateStoreLocked(t *testing.T) {
	workspace := t.TempDir()
	lock, err := harnessbridge.AcquireStateStoreLock(workspace, ".rocketclaw")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Close()) })

	err = runFCDeleteIn(workspace, config.DefaultWorkDir, []string{"main"}, io.Discard)
	require.ErrorContains(t, err, "rocketclaw daemon is running; stop it before running fc delete")
	require.ErrorIs(t, err, harnessbridge.ErrStateStoreLocked)
}

func TestRunFCDeleteNoVacuumMissingDBSkipsVacuumHint(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, runFCDeleteIn(t.TempDir(), config.DefaultWorkDir, []string{"--no-vacuum", "main"}, &out))
	assert.Contains(t, out.String(), "deleted 0 turns; skipped vacuum")
	assert.NotContains(t, out.String(), "run rocketclaw fc vacuum")
}

func TestRunFCVacuumMissingDBIsNoop(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, runFCVacuumIn(t.TempDir(), config.DefaultWorkDir, nil, &out))
	assert.Contains(t, out.String(), "nothing to vacuum")
}

func TestRunFCVacuumRefusesWhileStateStoreLocked(t *testing.T) {
	workspace := t.TempDir()
	lock, err := harnessbridge.AcquireStateStoreLock(workspace, ".rocketclaw")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Close()) })

	err = runFCVacuumIn(workspace, config.DefaultWorkDir, nil, io.Discard)
	require.ErrorContains(t, err, "rocketclaw daemon is running; stop it before running fc vacuum")
	require.ErrorIs(t, err, harnessbridge.ErrStateStoreLocked)
}

func TestRunFCVacuumRejectsArguments(t *testing.T) {
	var out bytes.Buffer

	err := runFCVacuumIn(t.TempDir(), config.DefaultWorkDir, []string{"extra"}, &out)
	require.ErrorContains(t, err, "vacuum does not accept arguments")
}

func TestRunFCVacuumReportsWorkspaceErrors(t *testing.T) {
	workspaceFile := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.WriteFile(workspaceFile, []byte("not a directory"), 0o600))

	err := runFCVacuumIn(workspaceFile, config.DefaultWorkDir, nil, io.Discard)
	require.ErrorContains(t, err, "vacuum rocketcode sessions")
}

func TestRunFCVacuumPreservesRows(t *testing.T) {
	workspace := t.TempDir()
	_, err := harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "main", fcTestEntry("main user", "main assistant"))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runFCVacuumIn(workspace, config.DefaultWorkDir, nil, &out))
	assert.Contains(t, out.String(), "vacuumed sessions")

	entries, err := harnessbridge.ObserveSessionEntries(t.Context(), harnessbridge.SessionDBPath(workspace), "main", 0)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestWriteVacuumStatsReportsWriterErrors(t *testing.T) {
	err := writeVacuumStats(failingWriter{}, harnessbridge.VacuumStats{})
	require.ErrorContains(t, err, "write rocketcode vacuum result")
	require.ErrorIs(t, err, errFailingWrite)

	err = writeVacuumStats(failingWriter{}, harnessbridge.VacuumStats{DBExists: true})
	require.ErrorContains(t, err, "write rocketcode vacuum result")
	require.ErrorIs(t, err, errFailingWrite)
}

func TestWriteFCListReportsFlushError(t *testing.T) {
	err := writeFCListInOptions(t.Context(), t.TempDir(), config.DefaultWorkDir, harnessbridge.SessionListOptions{}, true, failingWriter{})
	require.ErrorContains(t, err, "flush rocketcode session list")
	require.ErrorIs(t, err, errFailingWrite)
}

func TestWriteFCListReportsWorkspaceErrors(t *testing.T) {
	workspaceFile := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.WriteFile(workspaceFile, []byte("not a directory"), 0o600))

	err := writeFCListInOptions(t.Context(), workspaceFile, config.DefaultWorkDir, harnessbridge.SessionListOptions{}, true, io.Discard)
	require.ErrorContains(t, err, "list rocketcode sessions")
}

func TestWriteFCObserveReportsWriterErrors(t *testing.T) {
	workspace := t.TempDir()
	_, err := harnessbridge.AppendSessionEntryID(t.Context(), harnessbridge.SessionDBPath(workspace), "main", fcTestEntry("hello", "hi"))
	require.NoError(t, err)

	err = writeFCObserveIn(t.Context(), workspace, config.DefaultWorkDir, "main", false, time.Millisecond, failingWriter{})
	require.ErrorContains(t, err, "write rocketcode session entry")
	require.ErrorIs(t, err, errFailingWrite)
}

var errFailingWrite = errors.New("write failed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errFailingWrite
}

type failOnSecondWrite struct{ writes int }

func (w *failOnSecondWrite) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 2 {
		return 0, errFailingWrite
	}

	return len(p), nil
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

func fcTestConfigJSON() string {
	return strings.TrimSpace(`{
  "workspace": ".",
  "openai": {
    "api_key": "shared-key",
    "stt_key": "stt-key",
    "tts_key": "tts-key"
  },
  "slack": {
    "enabled": true,
    "bot_token": "xoxb-test",
    "app_token": "xapp-test",
    "room": "D123",
    "human_user_id": "U123"
  }
}`)
}
