package backend

import (
	"crypto/rand"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

// openTestSpill opens store's spill dir, creating it on the host as a turn's rocketcode would.
func openTestSpill(t *testing.T, store *SessionService) *os.Root {
	t.Helper()

	require.NoError(t, os.MkdirAll(store.spillDir, 0o700))
	root, err := os.OpenRoot(store.spillDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	return root
}

// retainTestResult keeps output as a retained background result of conversationID, last modified at modified.
func retainTestResult(t *testing.T, root *os.Root, conversationID, output string, modified time.Time) {
	t.Helper()

	name := filepath.Join(rocketcodeRetainedDir(conversationID), rand.Text()+".txt")
	require.NoError(t, root.MkdirAll(filepath.Dir(name), 0o700))
	require.NoError(t, root.WriteFile(name, []byte(output), 0o600))
	require.NoError(t, root.Chtimes(name, modified, modified))
}

// retainedResults lists the retained directories under root and the output of their results.
func retainedResults(t *testing.T, root *os.Root) []string {
	t.Helper()

	var kept []string

	require.NoError(t, fs.WalkDir(root.FS(), "retained", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || name == "retained" {
			return err
		}

		if entry.IsDir() {
			kept = append(kept, name)
			return nil
		}

		data, errRead := root.ReadFile(name)
		require.NoError(t, errRead)

		kept = append(kept, filepath.Dir(name)+" "+string(data))

		return nil
	}))

	return kept
}

// The sweep also removes the conversation dirs it empties, and keeps fresh results.
func TestStartupSweepsExpiredRetainedResults(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	require.NoError(t, store.sweepRetainedResults(), "a spill dir no turn created yet holds nothing to sweep")

	root := openTestSpill(t, store)
	expired := time.Now().Add(-8 * 24 * time.Hour)

	retainTestResult(t, root, "old", "expired", expired)
	retainTestResult(t, root, "mixed", "expired", expired)
	retainTestResult(t, root, "mixed", "fresh", time.Now())
	require.NoError(t, store.sweepRetainedResults())

	mixed := rocketcodeRetainedDir("mixed")
	require.Equal(t, []string{mixed, mixed + " fresh"}, retainedResults(t, root))
}

// testJobsSQL selects the Background Jobs: the turns j, of conversations c, that do not take the lane.
const testJobsSQL = ` FROM turns j LEFT JOIN conversations c USING (conversation_id) WHERE NOT j.takes_lane`

// testJobStatusSQL is job j's status.
const testJobStatusSQL = `CASE WHEN j.phase = 'running' THEN 'running' WHEN j.terminal = '' THEN 'completed' ELSE j.terminal END`

// reportTestRun has the hidden run report to destination, as the run's first turn for it does.
func reportTestRun(t *testing.T, store *SessionService, run, destination string) {
	t.Helper()

	_, err := store.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed) VALUES ($1, 'main', '', 'web_chat', TRUE) ON CONFLICT DO NOTHING`, destination)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, reports_to) VALUES ($1, 'main', '', 'cron_run', FALSE, $2)
ON CONFLICT (conversation_id) DO UPDATE SET reports_to = excluded.reports_to`, run, destination)
	require.NoError(t, err)
}

func testBackgroundJob(conversationID, jobID string) *backgroundJob {
	return &backgroundJob{conversationID: conversationID, jobID: jobID, kind: turnScript, agent: "main", label: "tests", callID: "call", origin: &protocol.InboundMessage{Source: protocol.SourceSlack}, runnerID: "runner-1"}
}

// finishTestBackgroundJob ends a job as its runner with a pending note.
func finishTestBackgroundJob(t *testing.T, store *SessionService, job *backgroundJob, status backgroundJobStatus, wake bool) {
	t.Helper()

	end := *job
	end.status, end.noteState, end.wake, end.result = status, notePending, wake, string(status)
	won, err := store.finishBackgroundJob(t.Context(), &end)
	require.NoError(t, err)
	require.True(t, won)
}

func createTestBackgroundJob(t *testing.T, store *SessionService, job *backgroundJob) {
	t.Helper()

	_, created, err := createBackgroundJob(t.Context(), store.db, job)
	require.NoError(t, err)
	require.True(t, created)
}

func backgroundJobIDs(t *testing.T, store *SessionService, conversationID string) []string {
	t.Helper()

	jobs, err := store.backgroundJobs(t.Context(), conversationID)
	require.NoError(t, err)

	ids := make([]string, 0, len(jobs))
	for i := range jobs {
		ids = append(ids, jobs[i].jobID)
	}

	return ids
}

func TestBackgroundJobCreateKeepsFirstRow(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	first := testBackgroundJob("main", "turn-1/call/a")
	first.origin.SyncDestination = "visible"
	stored, created, err := createBackgroundJob(t.Context(), store.db, first)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, backgroundRunning, stored.status)
	require.Equal(t, noteNone, stored.noteState)
	require.Equal(t, "visible", stored.origin.SyncDestination)
	require.False(t, stored.createdAt.IsZero())

	second := testBackgroundJob("main", "turn-1/call/a")
	second.label, second.runnerID = "other", "runner-2"
	again, created, err := createBackgroundJob(t.Context(), store.db, second)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, stored, again)
}

func TestBackgroundJobTransitionsCompareAndSet(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	job := testBackgroundJob("main", "turn-1/call/a")
	createTestBackgroundJob(t, store, job)

	stale := *job
	stale.runnerID, stale.status, stale.noteState = "lost-lock", backgroundCompleted, notePending
	won, err := store.finishBackgroundJob(t.Context(), &stale)
	require.NoError(t, err)
	require.False(t, won)
	restamped, err := store.restampBackgroundJob(t.Context(), &stale, "runner-3")
	require.NoError(t, err)
	require.False(t, restamped)

	running, err := store.runningBackgroundJobs(t.Context())
	require.NoError(t, err)
	require.Len(t, running, 1)
	require.Equal(t, "runner-1", running[0].runnerID)
	require.Equal(t, noteNone, running[0].noteState)

	restamped, err = store.restampBackgroundJob(t.Context(), job, "runner-2")
	require.NoError(t, err)
	require.True(t, restamped)

	won, err = store.finishBackgroundJob(t.Context(), job)
	require.NoError(t, err)
	require.False(t, won, "the previous runner can no longer write")

	job.runnerID = "runner-2"
	statuses := []backgroundJobStatus{backgroundStopped, backgroundCompleted}
	wins := make([]bool, len(statuses))

	var racers errgroup.Group
	for i, status := range statuses {
		racers.Go(func() error {
			end := *job
			end.status, end.noteState, end.wake, end.result = status, notePending, true, string(status)

			var err error

			wins[i], err = store.finishBackgroundJob(t.Context(), &end)

			return err
		})
	}

	require.NoError(t, racers.Wait())
	require.ElementsMatch(t, []bool{true, false}, wins)

	jobs, err := store.backgroundJobs(t.Context(), "main")
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, statuses[slices.Index(wins, true)], jobs[0].status)
	require.Equal(t, string(jobs[0].status), jobs[0].result)
	require.Equal(t, notePending, jobs[0].noteState)
}

func TestBackgroundJobFinishStoresNoteAndClearsJournal(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	job := testBackgroundJob("main", "turn-1/call/a")
	createTestBackgroundJob(t, store, job)
	// A job started by a host call of the script keeps running, and owns its steps, after it.
	createTestBackgroundJob(t, store, testBackgroundJob("main", "turn-1/call/a/host/2"))

	for _, key := range []string{"turn-1/call/a", "turn-1/call/a/host/1", "turn-1/call/a/host/2/task", "turn-1/call/ab/host/1"} {
		require.NoError(t, store.SaveTurnStep(ctx, "main", key, json.RawMessage(`{}`)))
	}

	_, err := store.db.ExecContext(ctx, `CREATE FUNCTION reject_cleanup() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'cleanup failed'; END $$;
CREATE TRIGGER reject_cleanup BEFORE DELETE ON steps FOR EACH ROW EXECUTE FUNCTION reject_cleanup()`)
	require.NoError(t, err)

	end := *job
	end.status, end.noteState, end.wake, end.result = backgroundFailed, notePending, true, "exit 1\x00"
	_, err = store.finishBackgroundJob(ctx, &end)
	require.ErrorContains(t, err, "cleanup failed")

	running, err := store.runningBackgroundJobs(ctx)
	require.NoError(t, err)
	require.Len(t, running, 2)
	require.Equal(t, noteNone, running[0].noteState)

	_, err = store.db.ExecContext(ctx, `DROP TRIGGER reject_cleanup ON steps; DROP FUNCTION reject_cleanup()`)
	require.NoError(t, err)

	won, err := store.finishBackgroundJob(ctx, &end)
	require.NoError(t, err)
	require.True(t, won)

	jobs, err := store.backgroundJobs(ctx, "main")
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	require.Equal(t, backgroundFailed, jobs[0].status)
	require.Equal(t, notePending, jobs[0].noteState)
	require.True(t, jobs[0].wake)
	require.Equal(t, "exit 1", jobs[0].result)
	require.False(t, jobs[0].finishedAt.IsZero())
	require.ElementsMatch(t, []string{"turn-1/call/a", "turn-1/call/a/host/2/task", "turn-1/call/ab/host/1"}, testTurnStepKeys(t, store, "main"))

	running, err = store.runningBackgroundJobs(ctx)
	require.NoError(t, err)
	require.Len(t, running, 1)
	require.Equal(t, "turn-1/call/a/host/2", running[0].jobID)
}

func TestBackgroundNoteClaimSucceedsOnce(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	job := testBackgroundJob("main", "turn-1/call/a")
	createTestBackgroundJob(t, store, job)
	finishTestBackgroundJob(t, store, job, backgroundCompleted, true)

	first, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	defer func() { _ = first.Rollback() }()

	second, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	defer func() { _ = second.Rollback() }()

	claimed, err := claimBackgroundNotes(ctx, first, "main", "turn-2", false)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, "turn-2", claimed[0].claimedTurnID)
	claimed, err = claimBackgroundNotes(ctx, second, "main", "turn-3", false)
	require.NoError(t, err)
	require.Empty(t, claimed)
	require.NoError(t, first.Commit())
	require.NoError(t, second.Rollback())

	claimed, err = claimBackgroundNotes(ctx, store.db, "main", "turn-3", true)
	require.NoError(t, err)
	require.Empty(t, claimed, "a turn gets back only the notes it holds")
	claimed, err = claimBackgroundNotes(ctx, store.db, "main", "turn-2", true)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "a resumed turn gets back the notes it holds")
}

func TestFinishTurnConsumesOrReleasesNoteClaims(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		terminal protocol.Terminal
		saved    bool
	}{
		// Settlement depends on whether the turn saved entries, never on its terminal.
		{protocol.TerminalStopped, true}, {protocol.TerminalStopped, false},
	} {
		t.Run(string(tt.terminal)+"/saved="+strconv.FormatBool(tt.saved), func(t *testing.T) {
			store := newTestSessionService(t)
			ctx := t.Context()
			job := testBackgroundJob("main", "turn-1/call/a")
			createTestBackgroundJob(t, store, job)
			finishTestBackgroundJob(t, store, job, backgroundCompleted, true)
			seedActiveTurn(t, store, "main", "turn-2", nil)
			claimed, err := claimBackgroundNotes(ctx, store.db, "main", "turn-2", false)
			require.NoError(t, err)
			require.Len(t, claimed, 1)

			finish := &turnFinish{conversationID: strings.TrimSpace("main"), outbound: protocol.NewOutboundMessage("main", ""), terminal: tt.terminal}
			if tt.saved {
				entry := testSessionEntry("note", "answer")
				entry.Type = workflowRunEntryType
				finish.entries = []harness.SessionEntry{*entry}
			}

			_, err = store.finishTurn(ctx, "turn-2", finish)
			require.NoError(t, err)

			notes, err := queryStrings(ctx, store.db, `SELECT j.note || ' ' || j.wake::text || ' ' || j.claimed_by`+testJobsSQL, "background notes")
			require.NoError(t, err)

			if tt.saved {
				require.Equal(t, []string{"consumed true turn-2"}, notes)
				return
			}

			require.Equal(t, []string{"pending false "}, notes, "a turn that saved nothing must not be woken again")
		})
	}
}

// A hidden run may have no next turn, so a turn of it that saves nothing leaves its notes to wake
// it again 1 minute after the first failure, 5 minutes after the second, then every 30 minutes.
// No wake takes them sooner, but a running turn does, and its saved answer consumes them.
func TestHiddenRunTurnRetriesItsNotes(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		store := newTestSessionService(t)
		ctx := t.Context()
		job := testBackgroundJob("main", "turn-1/call/a")
		createTestBackgroundJob(t, store, job)
		finishTestBackgroundJob(t, store, job, backgroundCompleted, true)

		woken := func(turnID string) bool {
			tx, err := store.db.BeginTx(ctx, nil)
			require.NoError(t, err)

			defer func() { _ = tx.Rollback() }()

			woken, err := claimWakeNotes(ctx, tx, "main", turnID, protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "", false))
			require.NoError(t, err)

			return woken
		}

		for i, delay := range []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
			turnID := "turn-" + strconv.Itoa(i+2)
			seedActiveTurn(t, store, "main", turnID, nil)
			claimed, err := claimBackgroundNotes(ctx, store.db, "main", turnID, false)
			require.NoError(t, err)
			require.Len(t, claimed, 1)

			_, err = store.finishTurn(ctx, turnID, &turnFinish{conversationID: strings.TrimSpace("main"), outbound: protocol.NewOutboundMessage("main", ""), terminal: protocol.TerminalFailed, hidden: true})
			require.NoError(t, err)

			notes, err := queryStrings(ctx, store.db, `SELECT j.note || ' ' || j.wake::text || ' ' || j.claimed_by || ' ' || j.wake_attempts || ' ' || j.run_at_unix_ns`+testJobsSQL, "background notes")
			require.NoError(t, err)
			require.Equal(t, []string{"pending true  " + strconv.Itoa(i+1) + " " + strconv.FormatInt(time.Now().Add(delay).UnixNano(), 10)}, notes)

			if i == 3 {
				break
			}

			time.Sleep(delay - time.Nanosecond)
			require.False(t, woken("early"), "no wake takes the notes before their delay")
			time.Sleep(time.Nanosecond)
			require.True(t, woken("due"))
		}

		fresh := testBackgroundJob("main", "turn-6/call/b")
		createTestBackgroundJob(t, store, fresh)
		finishTestBackgroundJob(t, store, fresh, backgroundCompleted, true)
		next, _, err := store.backgroundWake(ctx, "main")
		require.NoError(t, err)
		require.Equal(t, fresh.jobID, next.jobID, "a new note wakes the run before a waiting retry")

		seedActiveTurn(t, store, "main", "running", nil)
		claimed, err := claimBackgroundNotes(ctx, store.db, "main", "running", true)
		require.NoError(t, err)
		require.Len(t, claimed, 2, "a running turn takes the notes before their delay")

		_, err = store.finishTurn(ctx, "running", &turnFinish{conversationID: strings.TrimSpace("main"), entries: []harness.SessionEntry{*testSessionEntry("note", "answer")}, outbound: protocol.NewOutboundMessage("main", ""), hidden: true})
		require.NoError(t, err)

		notes, err := queryStrings(ctx, store.db, `SELECT j.note`+testJobsSQL, "background notes")
		require.NoError(t, err)
		require.Equal(t, []string{"consumed", "consumed"}, notes)
	})
}

// testReply is a running reply of caller's write, call key, to target.
func testReply(caller, call, target string) *backgroundJob {
	return &backgroundJob{conversationID: caller, jobID: caller + "/" + call, kind: turnReply, agent: "main", label: string(sessionWriteEnqueue), origin: &protocol.InboundMessage{}, target: target}
}

// AE2, AE3: a write's reply settles with the target's turn that consumed it, as that turn's input or an
// applied steer, taking the turn's state and answer, and its caller then learns of the note. A turn of
// another session with the same input, such as a shadow copy's, settles nothing.
func TestFinishTurnSettlesSessionReplies(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, conversation string
		applied            bool
		terminal           protocol.Terminal
		want               string
	}{
		{"own input", "target", false, "", "completed pending true billing is fine"},
		{"applied steer", "target", true, "", "completed pending true billing is fine"},
		{"stopped", "target", false, protocol.TerminalStopped, "stopped pending true billing is fine"},
		{"failed", "target", true, protocol.TerminalFailed, "failed pending true billing is fine"},
		{"shadow copy", "shadow", false, "", "running none false "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := newTestSessionService(t)
			ctx := t.Context()
			notes := &backgroundNotesMock{noteReadyFunc: func(string) {}}
			store.jobs = newBackgroundRegistry(store, notes, testLogger())

			reply := testReply("caller", "turn-1/call/w", "target")
			createTestBackgroundJob(t, store, reply)

			inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "summarize billing", false)
			inbound.Metadata = map[string]string{"web_message_id": reply.jobID}

			if tc.applied {
				inbound.Metadata["web_message_id"] = "earlier"

				require.NoError(t, store.SaveTurnStep(ctx, tc.conversation, "turn-2", json.RawMessage(`{"applied_input_ids":["`+reply.jobID+`"]}`)))
			}

			require.NoError(t, startTurnDB(ctx, store.db, "turn-2", tc.conversation, inbound))
			_, err := store.finishTurn(ctx, "turn-2", &turnFinish{conversationID: strings.TrimSpace(tc.conversation), entries: []harness.SessionEntry{*testSessionEntry("summarize billing", "billing is fine")}, outbound: protocol.NewOutboundMessage(tc.conversation, "billing is fine"), terminal: tc.terminal})
			require.NoError(t, err)

			rows, err := queryStrings(ctx, store.db, `SELECT `+testJobStatusSQL+` || ' ' || j.note || ' ' || j.wake::text || ' ' || j.result`+testJobsSQL, "replies")
			require.NoError(t, err)
			require.Equal(t, []string{tc.want}, rows)

			woken := make([]string, 0, len(notes.noteReadyCalls()))
			for _, call := range notes.noteReadyCalls() {
				woken = append(woken, call.ConversationID)
			}

			require.Equal(t, map[bool][]string{true: {"caller"}, false: {}}[tc.conversation == "target"], woken)
		})
	}
}

// R14: a subagent's wake turn, which no task call awaits, reports to its parent, waking it, as a task note
// that continues the subagent: its answer as reviewed for the parent, or why it failed. The note of a
// hidden-run parent wakes it in the chat it reports to. A stopped wake turn, a turn that ran a task's input, and a wake
// turn that took a task's steer, whose task call gets the answer, report nothing.
func TestFinishTurnReportsSubagentWakeUp(t *testing.T) {
	t.Parallel()

	answered := "<subagent id=\"turn-1\" state=\"completed\" description=\"researcher\" continue=\"/a\">\n<task_result>\nbilling noted\n</task_result>\n</subagent>"

	for _, tc := range []struct {
		name                  string
		wake, steered, hidden bool
		terminal              protocol.Terminal
		want, destination     string
	}{
		{"wake turn", true, false, false, "", answered, ""},
		{"hidden-run parent", true, false, true, "", answered, "web:main"},
		{"failed wake turn", true, false, false, protocol.TerminalFailed, "<subagent id=\"turn-1\" state=\"failed\" description=\"researcher\" continue=\"/a\">\nturn failed\n</subagent>", ""},
		{"stopped wake turn", true, false, false, protocol.TerminalStopped, "", ""},
		{"task input", false, false, false, "", "", ""},
		{"wake turn took a task steer", true, true, false, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := newTestSessionService(t)
			ctx := t.Context()
			notes := &backgroundNotesMock{noteReadyFunc: func(string) {}}
			store.jobs = newBackgroundRegistry(store, notes, testLogger())

			recordTestConversations(t, store, "main", map[string]ConversationKind{"main": KindWebChat})

			if tc.hidden {
				reportTestRun(t, store, "main", "web:main")
			}

			require.NoError(t, store.startSubagent(ctx, "main", &harness.SubagentWrite{Key: harness.ChildKey{Path: "/a", Turn: "turn-0", Call: "a"}, Agent: "researcher"}))

			inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "", false)

			inbound.Metadata = map[string]string{"web_message_id": "main/turn-0/call/a"}
			if tc.wake {
				inbound.Metadata = map[string]string{backgroundNotesMetadataKey: "turn-0/call/x"}
			}

			if tc.steered {
				require.NoError(t, store.PutThreadQueueItem("main/turn-2/call/s", &protocol.ThreadQueueItem{ID: "main/turn-2/call/s", ConversationID: "main/a", Kind: protocol.InboundKindSteer}))
				require.NoError(t, store.SaveTurnStep(ctx, "main/a", "turn-1", json.RawMessage(`{"applied_input_ids":["main/turn-2/call/s"]}`)))
			}

			require.NoError(t, startTurnDB(ctx, store.db, "turn-1", "main/a", inbound))
			_, err := store.finishTurn(ctx, "turn-1", &turnFinish{conversationID: strings.TrimSpace("main/a"), entries: []harness.SessionEntry{*testSessionEntry("notes", "billing noted")}, outbound: protocol.NewOutboundMessage("main/a", "turn failed"), terminal: tc.terminal, report: "<task_result>\nbilling noted\n</task_result>"})
			require.NoError(t, err)

			reports, err := store.notedBackgroundJobs(ctx, "main", []string{"turn-1"})
			require.NoError(t, err)

			woken := make([]string, 0, len(notes.noteReadyCalls()))
			for _, call := range notes.noteReadyCalls() {
				woken = append(woken, call.ConversationID)
			}

			if tc.want == "" {
				require.Empty(t, reports)
				require.Empty(t, woken)

				return
			}

			require.Len(t, reports, 1)
			require.Equal(t, []any{tc.want, notePending, true, tc.destination}, []any{backgroundNote(&reports[0]), reports[0].noteState, reports[0].wake, reports[0].origin.SyncDestination})
			require.Equal(t, []string{"main"}, woken)
		})
	}
}

func TestReleaseStaleBackgroundClaims(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	seedActiveTurn(t, store, "main", "live", nil)

	for turnID, jobID := range map[string]string{"live": "turn-1/call/a", "gone": "turn-1/call/b"} {
		job := testBackgroundJob("main", jobID)
		createTestBackgroundJob(t, store, job)
		finishTestBackgroundJob(t, store, job, backgroundCompleted, true)
		_, err := claimBackgroundNotes(ctx, store.db, "main", turnID, false)
		require.NoError(t, err)
	}

	require.NoError(t, store.releaseStaleBackgroundClaims(ctx))

	notes, err := queryStrings(ctx, store.db, `SELECT j.input_id || '=' || j.claimed_by || ' ' || j.wake::text`+testJobsSQL+` ORDER BY j.input_id`, "background claims")
	require.NoError(t, err)
	require.Equal(t, []string{"turn-1/call/a=live true", "turn-1/call/b= true"}, notes)
}

func TestTurnCleanupKeepsBackgroundJournal(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	seedActiveTurn(t, store, "main", "turn-1", &harness.SessionEntry{Version: 1, Type: "turn", TurnID: "turn-1"})
	createTestBackgroundJob(t, store, testBackgroundJob("main", "turn-1/call/a"))

	for _, key := range []string{"turn-1/call/a", "turn-1/call/a/task", "turn-1/call/b", "turn-1/call/b/host/1"} {
		require.NoError(t, store.SaveTurnStep(ctx, "main", key, json.RawMessage(`{}`)))
	}

	_, err := store.finishTurn(ctx, "turn-1", &turnFinish{conversationID: strings.TrimSpace("main"), outbound: protocol.NewOutboundMessage("main", "")})
	require.NoError(t, err)
	require.Equal(t, []string{"turn-1/call/a/task"}, testTurnStepKeys(t, store, "main"))

	require.NoError(t, store.SaveTurnStep(ctx, "main", "turn-1/reply", json.RawMessage(`{}`)))
	require.NoError(t, store.closeTurn(ctx, "turn-1"))
	require.Equal(t, []string{"turn-1/call/a/task"}, testTurnStepKeys(t, store, "main"), "running jobs keep their journals")
}

// Background Jobs are turns beside the lane: a running script and a failed one leave the conversation
// idle, its session listed as not running, and its history without a live or ended turn.
func TestBackgroundJobsStayOffTheLane(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, store, "main", map[string]ConversationKind{"main": KindWebChat})
	_, err := store.AppendEntryID(ctx, "main", testSessionEntry("hello", "hi"))
	require.NoError(t, err)

	createTestBackgroundJob(t, store, testBackgroundJob("main", "turn-1/call/running"))
	failed := testBackgroundJob("main", "turn-1/call/failed")
	createTestBackgroundJob(t, store, failed)
	finishTestBackgroundJob(t, store, failed, backgroundFailed, true)
	require.NoError(t, recordRunning(ctx, store.db, []string{"main"}))

	active, err := store.HasActiveTurn(ctx, "main")
	require.NoError(t, err)
	require.False(t, active)

	_, found, err := store.headTurn(ctx, "main")
	require.NoError(t, err)
	require.False(t, found)

	transcript, err := store.ObserveTranscript(ctx, "main", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, transcript, 1, "only the saved entry")

	var listed []string

	for session, err := range store.sidebarSessions(ctx, "", nil) {
		require.NoError(t, err)
		require.False(t, session.Running)

		listed = append(listed, session.Conversation.ID)
	}

	require.Equal(t, []string{"main"}, listed)
}

func TestBackgroundJobsListOwnAndDestinationJobs(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	createTestBackgroundJob(t, store, testBackgroundJob("main", "turn-1/call/running"))

	pending := testBackgroundJob("main", "turn-1/call/pending")
	createTestBackgroundJob(t, store, pending)
	finishTestBackgroundJob(t, store, pending, backgroundKilled, false)

	silent := testBackgroundJob("main", "turn-1/call/silent")
	createTestBackgroundJob(t, store, silent)

	end := *silent
	end.status, end.noteState = backgroundCompleted, noteNone
	won, err := store.finishBackgroundJob(t.Context(), &end)
	require.NoError(t, err)
	require.True(t, won)

	reportTestRun(t, store, "cron:daily", "main")

	hidden := testBackgroundJob("cron:daily", "turn-9/call/report")
	hidden.origin.SyncDestination = "main"
	createTestBackgroundJob(t, store, hidden)
	createTestBackgroundJob(t, store, testBackgroundJob("other", "turn-1/call/elsewhere"))

	require.Equal(t, []string{"turn-1/call/running", "turn-1/call/pending", "turn-9/call/report"}, backgroundJobIDs(t, store, "main"))

	running, err := store.runningBackgroundJobs(t.Context())
	require.NoError(t, err)
	require.Len(t, running, 3)
}

func TestPruneKeepsConversationsWithLiveBackgroundJobs(t *testing.T) {
	cutoff := time.Unix(1_700_000_000, 0).UTC()
	old := cutoff.Add(-time.Hour)

	for _, tt := range []struct {
		name   string
		status backgroundJobStatus
		wake   bool
		note   noteState
		keep   bool
		// hidden runs the jobs in a hidden run that reports to the conversation.
		hidden bool
	}{
		{name: "running", status: backgroundRunning, note: noteNone, keep: true},
		{name: "hidden wakeable note", status: backgroundCompleted, wake: true, note: notePending, keep: true, hidden: true},
		{name: "no-wake note", status: backgroundKilled, note: notePending},
		{name: "consumed note", status: backgroundCompleted, wake: true, note: noteConsumed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestSessionService(t)
			ctx := t.Context()
			thread := protocol.SlackThreadConversationID("D1", slackTestTS(old))
			managed := protocol.SlackThreadConversationID("C1", slackTestTS(old))
			private, orphan := "external_mcp:planner:private", "cron:daily:old"

			recordTestConversations(t, store, "planner", map[string]ConversationKind{thread: KindSlackThread})
			recordPrivateHistories(t, store, map[string]ConversationKind{orphan: KindCronRun})
			require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: private, ManagedConversationID: managed}))

			for _, conversationID := range []string{thread, managed, private, orphan} {
				_, err := store.AppendEntryID(ctx, conversationID, testSessionEntryAt(old, conversationID))
				require.NoError(t, err)
			}

			spill := openTestSpill(t, store)

			for _, conversationID := range []string{thread, private, orphan} {
				retainTestResult(t, spill, conversationID, conversationID, time.Now())

				job := testBackgroundJob(conversationID, "turn-1/call/a")
				if tt.hidden {
					reportTestRun(t, store, "one-off-cron:"+conversationID, conversationID)
					job = testBackgroundJob("one-off-cron:"+conversationID, "turn-1/call/"+conversationID)
					job.origin.SyncDestination = conversationID
				}

				createTestBackgroundJob(t, store, job)

				if tt.status != backgroundRunning {
					finishTestBackgroundJob(t, store, job, tt.status, tt.wake)
				}
			}

			_, err := store.db.ExecContext(ctx, `UPDATE turns SET note = $1 WHERE note = $2`, tt.note, notePending)
			require.NoError(t, err)
			_, err = store.PruneStateBefore(ctx, cutoff, 0)
			require.NoError(t, err)

			_, ok, err := store.Thread(thread)
			require.NoError(t, err)
			require.Equal(t, tt.keep, ok, "thread conversation")
			_, ok, err = store.ExternalMCPSession("public-1")
			require.NoError(t, err)
			require.Equal(t, tt.keep, ok, "external MCP session")

			entries, err := store.ObserveEntries(ctx, orphan)
			require.NoError(t, err)
			require.Equal(t, tt.keep, len(entries) == 1, "private conversation")

			var rows int
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*)`+testJobsSQL).Scan(&rows))

			if tt.keep {
				require.Equal(t, 3, rows)
				require.Len(t, retainedResults(t, spill), 6, "kept conversations keep their retained output")

				return
			}

			require.Empty(t, retainedResults(t, spill), "pruned conversations lose their retained output")

			require.Zero(t, rows)
		})
	}
}

func TestConversationRemovalDeletesBackgroundJobs(t *testing.T) {
	t.Parallel()

	store, _ := newShadowTestStore(t)
	ctx := t.Context()
	spill := openTestSpill(t, store)
	private := "external_mcp:planner:private"

	for _, conversationID := range []string{"main", "other", private} {
		retainTestResult(t, spill, conversationID, conversationID, time.Now())
	}

	for _, conversationID := range []string{"main", "other"} {
		createTestBackgroundJob(t, store, testBackgroundJob(conversationID, "turn-1/call/a"))
		require.NoError(t, store.SaveTurnStep(ctx, conversationID, "turn-1/call/a/host/1", json.RawMessage(`{}`)))
	}

	reportTestRun(t, store, "cron:daily", "main")

	hidden := testBackgroundJob("cron:daily", "turn-1/call/a")
	hidden.origin.SyncDestination = "main"
	createTestBackgroundJob(t, store, hidden)

	_, err := store.DeleteSession(ctx, "main")
	require.NoError(t, err)
	require.Empty(t, testTurnStepKeys(t, store, "main"))
	require.Equal(t, []string{"turn-1/call/a/host/1"}, testTurnStepKeys(t, store, "other"))
	require.Equal(t, []string{"turn-1/call/a"}, backgroundJobIDs(t, store, "main"), "the hidden run's job stays listed at its destination")
	require.Equal(t, []string{"turn-1/call/a"}, backgroundJobIDs(t, store, "other"))
	require.ElementsMatch(t, []string{rocketcodeRetainedDir("other"), rocketcodeRetainedDir("other") + " other", rocketcodeRetainedDir(private), rocketcodeRetainedDir(private) + " " + private}, retainedResults(t, spill), "the deleted conversation's retained output goes with it")

	require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: private, ManagedConversationID: "managed"}))
	children, err := queryStrings(ctx, store.db, `SELECT conversation_id FROM conversations WHERE shadow_parent = 'managed'`, "failed MCP children")
	require.NoError(t, err)
	require.Len(t, children, 2)

	for _, child := range children {
		retainTestResult(t, spill, child, child, time.Now())
		_, err := store.AppendEntryID(ctx, child, testSessionEntry("child", "result"))
		require.NoError(t, err)
		require.NoError(t, store.SaveAttachment(ctx, child, "", &protocol.OutboundAttachment{ID: child, Name: "result", Data: []byte("bytes")}, false))
		require.NoError(t, store.SaveTurnStep(ctx, child, "old/step", json.RawMessage(`{}`)))
	}

	createTestBackgroundJob(t, store, testBackgroundJob(private, "turn-1/call/a"))
	require.NoError(t, store.RemoveExternalMCPConversation("public-1"))
	require.Empty(t, backgroundJobIDs(t, store, private))
	require.Equal(t, []string{rocketcodeRetainedDir("other"), rocketcodeRetainedDir("other") + " other"}, retainedResults(t, spill))

	for _, child := range children {
		_, found, err := store.Thread(child)
		require.NoError(t, err)
		require.False(t, found)

		entries, err := store.ObserveEntries(ctx, child)
		require.NoError(t, err)
		require.Empty(t, entries)
		require.Empty(t, testTurnStepKeys(t, store, child))
		_, err = store.attachments.Get(ctx, child)
		require.Error(t, err)
	}
}

func TestBackgroundJobChangeNotices(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	reportTestRun(t, store, "cron:daily", "visible")
	conn, err := store.db.Conn(ctx)
	require.NoError(t, err)

	defer func() { require.NoError(t, conn.Close()) }()

	var channel string
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT 'rocketclaw_transcript_' || md5(current_schema())`).Scan(&channel))
	_, err = conn.ExecContext(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize())
	require.NoError(t, err)

	notices := func() []string {
		_, err := store.db.ExecContext(ctx, `SELECT pg_notify($1, 'barrier')`, channel)
		require.NoError(t, err)

		conversations := []string{}

		require.NoError(t, conn.Raw(func(raw any) error {
			for {
				n, err := raw.(*stdlib.Conn).Conn().WaitForNotification(ctx)
				require.NoError(t, err)

				if n.Payload == "barrier" {
					return nil
				}

				var change protocol.ConversationChange
				require.NoError(t, json.Unmarshal([]byte(n.Payload), &change))

				conversations = append(conversations, change.ConversationID)
			}
		}))

		return conversations
	}

	hidden := testBackgroundJob("cron:daily", "turn-1/call/a")
	hidden.origin.SyncDestination = "visible"
	createTestBackgroundJob(t, store, hidden)
	require.ElementsMatch(t, []string{"cron:daily", "visible"}, notices(), "insert")

	createTestBackgroundJob(t, store, testBackgroundJob("main", "turn-1/call/a"))
	require.Equal(t, []string{"main"}, notices(), "an empty destination is not notified")

	restamped, err := store.restampBackgroundJob(ctx, hidden, "runner-2")
	require.NoError(t, err)
	require.True(t, restamped)
	require.Empty(t, notices(), "runner changes carry no transcript change")

	hidden.runnerID = "runner-2"
	finishTestBackgroundJob(t, store, hidden, backgroundCompleted, true)
	require.ElementsMatch(t, []string{"cron:daily", "visible"}, notices(), "status and note")

	_, err = claimBackgroundNotes(ctx, store.db, "cron:daily", "turn-2", false)
	require.NoError(t, err)
	require.Empty(t, notices(), "claims carry no transcript change")

	for _, statement := range []string{
		`UPDATE turns SET label = 'renamed' WHERE conversation_id = 'cron:daily'`,
		`DELETE FROM turns WHERE conversation_id = 'cron:daily'`,
	} {
		_, err = store.db.ExecContext(ctx, statement)
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"cron:daily", "visible"}, notices(), statement)
	}
}

// A chat shows background work while a Background Job of its own or of a hidden run reporting to
// it runs, or while such a hidden run's turn runs.
func TestSidebarMarksRunningBackgroundJobs(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)

	for _, id := range []string{"idle", "own", "destination", "reported"} {
		require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "main"}))
		_, err := store.AppendEntryID(t.Context(), id, testSessionEntry("question", "answer"))
		require.NoError(t, err)
	}

	reportTestRun(t, store, "cron:daily", "destination")
	reportTestRun(t, store, "cron:hourly", "reported")

	run := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "check", false)
	run.SyncDestination = "reported"
	require.NoError(t, startTurnDB(t.Context(), store.db, "hourly-turn", "cron:hourly", run))

	own, hidden := testBackgroundJob("own", "turn/call/a"), testBackgroundJob("cron:daily", "turn/call/b")
	hidden.origin.SyncDestination = "destination"

	for _, job := range []*backgroundJob{own, hidden} {
		createTestBackgroundJob(t, store, job)
	}

	finished := testBackgroundJob("idle", "turn/call/c")
	createTestBackgroundJob(t, store, finished)
	finishTestBackgroundJob(t, store, finished, backgroundCompleted, true)

	background := map[string]bool{}

	for row, err := range store.sidebarSessions(t.Context(), "", nil) {
		require.NoError(t, err)

		background[row.Conversation.ID] = row.Background
	}

	require.Equal(t, map[string]bool{"idle": false, "own": true, "destination": true, "reported": true}, background)
}
