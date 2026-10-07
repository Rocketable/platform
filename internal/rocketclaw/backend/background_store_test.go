package backend

import (
	"crypto/rand"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
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

func testBackgroundJob(conversationID, jobID string) *backgroundJob {
	return &backgroundJob{conversationID: conversationID, jobID: jobID, kind: backgroundExecute, agent: "main", label: "tests", callID: "call", origin: &protocol.InboundMessage{Source: protocol.SourceSlack}, runnerID: "runner-1"}
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
CREATE TRIGGER reject_cleanup BEFORE DELETE ON turn_steps FOR EACH ROW EXECUTE FUNCTION reject_cleanup()`)
	require.NoError(t, err)

	end := *job
	end.status, end.noteState, end.wake, end.result = backgroundFailed, notePending, true, "exit 1\x00"
	_, err = store.finishBackgroundJob(ctx, &end)
	require.ErrorContains(t, err, "cleanup failed")

	running, err := store.runningBackgroundJobs(ctx)
	require.NoError(t, err)
	require.Len(t, running, 2)
	require.Equal(t, noteNone, running[0].noteState)

	_, err = store.db.ExecContext(ctx, `DROP TRIGGER reject_cleanup ON turn_steps; DROP FUNCTION reject_cleanup()`)
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

	claimed, err := claimBackgroundNotes(ctx, first, "main", "", "turn-2", false)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, "turn-2", claimed[0].claimedTurnID)
	claimed, err = claimBackgroundNotes(ctx, second, "main", "", "turn-3", false)
	require.NoError(t, err)
	require.Empty(t, claimed)
	require.NoError(t, first.Commit())
	require.NoError(t, second.Rollback())

	claimed, err = claimBackgroundNotes(ctx, store.db, "main", "", "turn-3", true)
	require.NoError(t, err)
	require.Empty(t, claimed, "a turn gets back only the notes it holds")
	claimed, err = claimBackgroundNotes(ctx, store.db, "main", "", "turn-2", true)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "a resumed turn gets back the notes it holds")
}

func TestFinishTurnConsumesOrReleasesNoteClaims(t *testing.T) {
	for _, saved := range []bool{true, false} {
		t.Run(strconv.FormatBool(saved), func(t *testing.T) {
			store := newTestSessionService(t)
			ctx := t.Context()
			job := testBackgroundJob("main", "turn-1/call/a")
			createTestBackgroundJob(t, store, job)
			finishTestBackgroundJob(t, store, job, backgroundCompleted, true)
			seedActiveTurn(t, store, "main", "turn-2", nil)
			claimed, err := claimBackgroundNotes(ctx, store.db, "main", "", "turn-2", false)
			require.NoError(t, err)
			require.Len(t, claimed, 1)

			finish := &turnFinish{store: newSessionStore("main", store), outbound: protocol.NewOutboundMessage("main", ""), terminal: protocol.TerminalStopped}
			if saved {
				finish.entries, finish.terminal = []harness.SessionEntry{*testSessionEntry("note", "answer")}, ""
			}

			_, err = store.finishTurn(ctx, "turn-2", finish)
			require.NoError(t, err)

			notes, err := queryStrings(ctx, store.db, `SELECT note_state || ' ' || wake::text || ' ' || claimed_turn_id FROM background_jobs`, "background notes")
			require.NoError(t, err)

			if saved {
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
			claimed, err := claimBackgroundNotes(ctx, store.db, "main", "", turnID, false)
			require.NoError(t, err)
			require.Len(t, claimed, 1)

			_, err = store.finishTurn(ctx, turnID, &turnFinish{store: newSessionStore("main", store), outbound: protocol.NewOutboundMessage("main", ""), terminal: protocol.TerminalFailed, hidden: true})
			require.NoError(t, err)

			notes, err := queryStrings(ctx, store.db, `SELECT note_state || ' ' || wake::text || ' ' || claimed_turn_id || ' ' || wake_attempts || ' ' || wake_after_unix_ns FROM background_jobs`, "background notes")
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
		next, _, err := store.backgroundWake(ctx, "main", "")
		require.NoError(t, err)
		require.Equal(t, fresh.jobID, next.jobID, "a new note wakes the run before a waiting retry")

		seedActiveTurn(t, store, "main", "running", nil)
		claimed, err := claimBackgroundNotes(ctx, store.db, "main", "", "running", true)
		require.NoError(t, err)
		require.Len(t, claimed, 2, "a running turn takes the notes before their delay")

		_, err = store.finishTurn(ctx, "running", &turnFinish{store: newSessionStore("main", store), entries: []harness.SessionEntry{*testSessionEntry("note", "answer")}, outbound: protocol.NewOutboundMessage("main", ""), hidden: true})
		require.NoError(t, err)

		notes, err := queryStrings(ctx, store.db, `SELECT note_state FROM background_jobs`, "background notes")
		require.NoError(t, err)
		require.Equal(t, []string{"consumed", "consumed"}, notes)
	})
}

// Unless its job completes, a subagent's drained notes go back to its next turn (OpenCode packages/core/src/session/input.ts).
func TestBackgroundJobFinishConsumesOrReleasesNoteClaims(t *testing.T) {
	for _, status := range []backgroundJobStatus{backgroundCompleted, backgroundFailed, backgroundStopped} {
		t.Run(string(status), func(t *testing.T) {
			store := newTestSessionService(t)
			note := testBackgroundJob("main", "turn-1/call/a/x")
			note.childKey = "/a"
			createTestBackgroundJob(t, store, note)
			finishTestBackgroundJob(t, store, note, backgroundCompleted, false)

			task := testBackgroundJob("main", "turn-1/call/a")
			task.kind, task.subagentKey = backgroundTask, "/a"
			createTestBackgroundJob(t, store, task)
			claimed, err := claimBackgroundNotes(t.Context(), store.db, "main", "/a", task.jobID, false)
			require.NoError(t, err)
			require.Len(t, claimed, 1)
			finishTestBackgroundJob(t, store, task, status, true)

			want := "pending false "
			if status == backgroundCompleted {
				want = "consumed false turn-1/call/a"
			}

			notes, err := queryStrings(t.Context(), store.db, `SELECT note_state || ' ' || wake::text || ' ' || claimed_turn_id FROM background_jobs WHERE job_id = $1`, "background note", note.jobID)
			require.NoError(t, err)
			require.Equal(t, []string{want}, notes)
		})
	}
}

func TestReleaseStaleBackgroundClaims(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()
	seedActiveTurn(t, store, "main", "live", nil)

	for turnID, jobID := range map[string]string{"live": "turn-1/call/a", "gone": "turn-1/call/b"} {
		job := testBackgroundJob("main", jobID)
		createTestBackgroundJob(t, store, job)
		finishTestBackgroundJob(t, store, job, backgroundCompleted, true)
		_, err := claimBackgroundNotes(ctx, store.db, "main", "", turnID, false)
		require.NoError(t, err)
	}

	require.NoError(t, store.releaseStaleBackgroundClaims(ctx))

	notes, err := queryStrings(ctx, store.db, `SELECT job_id || '=' || claimed_turn_id || ' ' || wake::text FROM background_jobs ORDER BY job_id`, "background claims")
	require.NoError(t, err)
	require.Equal(t, []string{"turn-1/call/a=live true", "turn-1/call/b= true"}, notes)
}

func TestTurnCleanupKeepsBackgroundJournal(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()
	seedActiveTurn(t, store, "main", "turn-1", &harness.SessionEntry{Version: 1, Type: "turn", TurnID: "turn-1"})
	createTestBackgroundJob(t, store, testBackgroundJob("main", "turn-1/call/a"))

	for _, key := range []string{"turn-1/call/a", "turn-1/call/a/task", "turn-1/call/b", "turn-1/call/b/host/1"} {
		require.NoError(t, store.SaveTurnStep(ctx, "main", key, json.RawMessage(`{}`)))
	}

	_, err := store.finishTurn(ctx, "turn-1", &turnFinish{store: newSessionStore("main", store), outbound: protocol.NewOutboundMessage("main", "")})
	require.NoError(t, err)
	require.Equal(t, []string{"turn-1/call/a/task"}, testTurnStepKeys(t, store, "main"))

	require.NoError(t, store.SaveTurnStep(ctx, "main", "turn-1/reply", json.RawMessage(`{}`)))
	require.NoError(t, store.closeTurn(ctx, "turn-1"))
	require.Equal(t, []string{"turn-1/call/a/task"}, testTurnStepKeys(t, store, "main"))
}

func TestBackgroundSubagentRunsOnce(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()
	subagent := func(jobID, subagentKey string, kind backgroundJobKind) *backgroundJob {
		job := testBackgroundJob("main", jobID)
		job.kind, job.subagentKey = kind, subagentKey

		return job
	}

	first := subagent("turn-1/call/a", "/a", backgroundTask)
	createTestBackgroundJob(t, store, first)
	createTestBackgroundJob(t, store, subagent("turn-1/call/b", "/b", backgroundTask))
	createTestBackgroundJob(t, store, testBackgroundJob("main", "turn-1/call/c"))
	createTestBackgroundJob(t, store, testBackgroundJob("main", "turn-1/call/d"))

	_, _, err := createBackgroundJob(ctx, store.db, subagent("turn-2/wake", "/a", backgroundSubagentWake))
	require.Error(t, err)

	finishTestBackgroundJob(t, store, first, backgroundCompleted, true)
	createTestBackgroundJob(t, store, subagent("turn-2/wake", "/a", backgroundSubagentWake))
}

func TestBackgroundJobsListOwnAndDestinationJobs(t *testing.T) {
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
		// hidden runs the jobs in a hidden run whose sync destination is the conversation.
		hidden bool
	}{
		{name: "running", status: backgroundRunning, note: noteNone, keep: true},
		{name: "wakeable note", status: backgroundCompleted, wake: true, note: notePending, keep: true},
		{name: "hidden running", status: backgroundRunning, note: noteNone, keep: true, hidden: true},
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

			require.NoError(t, store.UpsertThread(thread, ThreadState{Agent: "planner"}))
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
					job = testBackgroundJob("one-off-cron:hidden", "turn-1/call/"+conversationID)
					job.origin.SyncDestination = conversationID
				}

				createTestBackgroundJob(t, store, job)

				if tt.status != backgroundRunning {
					finishTestBackgroundJob(t, store, job, tt.status, tt.wake)
				}
			}

			_, err := store.db.ExecContext(ctx, `UPDATE background_jobs SET note_state = $1 WHERE note_state = $2`, tt.note, notePending)
			require.NoError(t, err)
			_, err = store.PruneStateBefore(ctx, cutoff)
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
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM background_jobs`).Scan(&rows))

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
	store := newTestSessionService(t)
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
	createTestBackgroundJob(t, store, testBackgroundJob(private, "turn-1/call/a"))
	require.NoError(t, store.RemoveExternalMCPConversation("public-1"))
	require.Empty(t, backgroundJobIDs(t, store, private))
	require.Equal(t, []string{rocketcodeRetainedDir("other"), rocketcodeRetainedDir("other") + " other"}, retainedResults(t, spill))
}

func TestBackgroundJobChangeNotices(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()
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

	_, err = claimBackgroundNotes(ctx, store.db, "cron:daily", "", "turn-2", false)
	require.NoError(t, err)
	require.Empty(t, notices(), "claims carry no transcript change")

	for _, statement := range []string{
		`UPDATE background_jobs SET label = 'renamed' WHERE conversation_id = 'cron:daily'`,
		`DELETE FROM background_jobs WHERE conversation_id = 'cron:daily'`,
	} {
		_, err = store.db.ExecContext(ctx, statement)
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"cron:daily", "visible"}, notices(), statement)
	}
}
