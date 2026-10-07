package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

// newTestBackgroundRegistry returns a registry on store whose noted conversations arrive on the channel.
func newTestBackgroundRegistry(store *SessionService) (registry *backgroundRegistry, noted chan string) {
	notes := make(chan string, 64)
	return newBackgroundRegistry(store, &backgroundNotesMock{noteReadyFunc: func(conversationID string) { notes <- conversationID }}, testLogger()), notes
}

// testTurnRoot is a turn's workspace root that the test holds as its turn until cleanup.
func testTurnRoot(t *testing.T) *turnRoot {
	t.Helper()

	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)

	shared := &turnRoot{root: root, users: 1}
	t.Cleanup(shared.release)

	return shared
}

// blockedWork reports its start, then returns "done" once release closes or the cause of its context's end.
func blockedWork(started chan<- struct{}, release <-chan struct{}) *backgroundWorkMock {
	return &backgroundWorkMock{
		RunBackgroundFunc: func(ctx context.Context) (string, error) {
			started <- struct{}{}

			select {
			case <-release:
				return "done", nil
			case <-ctx.Done():
				return "", context.Cause(ctx)
			}
		},
		DetachFunc: func() bool { return true },
	}
}

func testBackgroundRow(t *testing.T, store *SessionService, conversationID, jobID string) backgroundJob {
	t.Helper()

	jobs, err := store.backgroundJobs(t.Context(), conversationID)
	require.NoError(t, err)

	for i := range jobs {
		if jobs[i].jobID == jobID {
			return jobs[i]
		}
	}

	t.Fatalf("background job %s is not listed at %s", jobID, conversationID)

	return backgroundJob{}
}

func TestBackgroundJobRunsDetachedOnce(t *testing.T) {
	store := newTestSessionService(t)
	registry, notes := newTestBackgroundRegistry(store)
	turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{Source: protocol.SourceSystem, RequireOutputDecision: true}}
	started, release := make(chan struct{}, 1), make(chan struct{})
	work := blockedWork(started, release)
	job := &rocketcode.BackgroundJob{ID: "turn-1/call/a", Kind: rocketcode.BackgroundKindExecute, Label: "tests", Agent: "main", CallID: "a", Detached: true}

	result, err := turn.Run(t.Context(), job, work)
	require.NoError(t, err)
	require.Equal(t, rocketcode.BackgroundResult{Moved: true}, result)
	require.Len(t, work.DetachCalls(), 1)
	<-started

	row := testBackgroundRow(t, store, "main", job.ID)
	require.Equal(t, backgroundRunning, row.status)
	require.Equal(t, backgroundExecute, row.kind)
	require.Equal(t, "tests", row.label)
	require.Equal(t, registry.runnerID, row.runnerID)
	require.True(t, row.origin.RequireOutputDecision)

	for _, detached := range []bool{true, false} {
		resumed := &backgroundWorkMock{DetachFunc: func() bool { return true }}
		result, err = turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: job.ID, Kind: job.Kind, Detached: detached}, resumed)
		require.NoError(t, err)
		require.Equal(t, rocketcode.BackgroundResult{Moved: true}, result, "a resumed call reports the job without running it again")
		require.Len(t, resumed.DetachCalls(), 1)
	}

	close(release)
	require.Equal(t, "main", <-notes)

	row = testBackgroundRow(t, store, "main", job.ID)
	require.Equal(t, backgroundCompleted, row.status)
	require.Equal(t, notePending, row.noteState)
	require.True(t, row.wake)
	require.Equal(t, "done", row.result)
}

func TestBackgroundJobFinishRetried(t *testing.T) {
	store := newTestSessionService(t)
	registry, notes := newTestBackgroundRegistry(store)
	turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{}}
	started, release := make(chan struct{}, 1), make(chan struct{})

	_, err := store.db.ExecContext(t.Context(), `CREATE SEQUENCE finish_attempts;
CREATE FUNCTION fail_first_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF nextval('finish_attempts') = 1 THEN RAISE EXCEPTION 'database restarting'; END IF; RETURN NEW; END $$;
CREATE TRIGGER fail_first_finish BEFORE UPDATE OF status ON background_jobs FOR EACH ROW EXECUTE FUNCTION fail_first_finish()`)
	require.NoError(t, err)

	_, err = turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: "turn-1/call/a", Kind: rocketcode.BackgroundKindExecute, Detached: true}, blockedWork(started, release))
	require.NoError(t, err)
	<-started
	close(release)
	require.NoError(t, registry.group.Wait())

	row := testBackgroundRow(t, store, "main", "turn-1/call/a")
	require.Equal(t, backgroundCompleted, row.status)
	require.Equal(t, notePending, row.noteState)
	require.Equal(t, "main", <-notes)
}

func TestBackgroundJobStop(t *testing.T) {
	store := newTestSessionService(t)
	registry, notes := newTestBackgroundRegistry(store)
	started, release := make(chan struct{}, 2), make(chan struct{})

	defer close(release)

	for conversationID, destination := range map[string]string{"main": "", "cron:daily": "main"} {
		turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: conversationID, origin: &protocol.InboundMessage{SyncDestination: destination}}
		_, err := turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: "turn-1/call/" + conversationID, Kind: rocketcode.BackgroundKindTask, SubagentKey: "/a", Detached: true}, blockedWork(started, release))
		require.NoError(t, err)
		<-started
	}

	_, err := registry.stop(t.Context(), "other", "turn-1/call/main", errStoppedByUser)
	require.ErrorContains(t, err, `background job "turn-1/call/main" is neither running nor waiting to report in this conversation`)

	for jobID, reason := range map[string]backgroundStopError{"turn-1/call/main": errStoppedByUser, "turn-1/call/cron:daily": errStoppedByAgent} {
		stopped, err := registry.stop(t.Context(), "main", jobID, reason)
		require.NoError(t, err)
		require.True(t, stopped)

		row := testBackgroundRow(t, store, "main", jobID)
		require.Equal(t, backgroundStopped, row.status)
		require.Equal(t, notePending, row.noteState)
		require.True(t, row.wake)
		require.Equal(t, string(reason), row.result)

		stopped, err = registry.stop(t.Context(), "main", jobID, reason)
		require.NoError(t, err)
		require.False(t, stopped, "stopping a finished job does nothing")
	}

	require.ElementsMatch(t, []string{"main", "cron:daily"}, []string{<-notes, <-notes})
}

// A stopped subagent is woken neither by late notes nor by its own jobs ending, which stop with it.
func TestStoppedSubagentStaysStopped(t *testing.T) {
	store := newTestSessionService(t)
	woken := make(chan string, 4)
	registry := newBackgroundRegistry(store, &backgroundNotesMock{noteReadyFunc: func(string) {}, continueSubagentFunc: func(_ context.Context, job *backgroundJob, _ string) (string, error) {
		woken <- job.subagentKey
		return "", nil
	}}, testLogger())
	started, release := make(chan struct{}, 2), make(chan struct{})

	defer close(release)

	turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{}}
	for _, job := range []*rocketcode.BackgroundJob{
		{ID: "turn-1/call/a", Kind: rocketcode.BackgroundKindTask, SubagentKey: "/a", Detached: true},
		{ID: "turn-1/call/a/task/call/x", Kind: rocketcode.BackgroundKindExecute, ChildKey: "/a", Detached: true},
	} {
		_, err := turn.Run(t.Context(), job, blockedWork(started, release))
		require.NoError(t, err)
		<-started
	}

	stopped, err := registry.stop(t.Context(), "main", "turn-1/call/a", errStoppedByUser)
	require.NoError(t, err)
	require.True(t, stopped)

	require.Eventually(t, func() bool {
		states, err := queryStrings(context.Background(), store.db, `SELECT status || ' ' || note_state FROM background_jobs WHERE job_id = 'turn-1/call/a/task/call/x'`, "owned job state")
		return err == nil && slices.Equal(states, []string{"stopped none"})
	}, 5*time.Second, 5*time.Millisecond, "the subagent's own job stops with it and leaves no note")
	require.Equal(t, notePending, testBackgroundRow(t, store, "main", "turn-1/call/a").noteState, "the parent still learns of the stop")
	require.Never(t, func() bool { return len(woken) > 0 }, 200*time.Millisecond, 5*time.Millisecond, "the stopped subagent is not woken")
}

func TestBackgroundJobAttachedCall(t *testing.T) {
	store := newTestSessionService(t)
	registry, notes := newTestBackgroundRegistry(store)
	turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{}}
	job := &rocketcode.BackgroundJob{ID: "turn-1/call/a", Kind: rocketcode.BackgroundKindTask, SubagentKey: "/a"}

	t.Run("finished", func(t *testing.T) {
		started, release := make(chan struct{}, 1), make(chan struct{})
		close(release)

		work := blockedWork(started, release)
		result, err := turn.Run(t.Context(), job, work)
		require.NoError(t, err)
		require.Equal(t, rocketcode.BackgroundResult{Output: "done"}, result)
		require.Empty(t, work.DetachCalls())
		require.False(t, registry.movable("main"))
	})

	t.Run("cancelled with the call's cause", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		started := make(chan struct{}, 1)

		var call errgroup.Group
		call.Go(func() error {
			_, err := turn.Run(ctx, job, blockedWork(started, make(chan struct{})))
			return err
		})
		<-started
		cancel(rocketcode.ErrShutdown)
		require.ErrorIs(t, call.Wait(), rocketcode.ErrShutdown, "foreground bash must see the shutdown cause")
	})

	t.Run("busy subagent", func(t *testing.T) {
		started, release := make(chan struct{}, 1), make(chan struct{})

		var call errgroup.Group
		call.Go(func() error {
			_, err := turn.Run(t.Context(), job, blockedWork(started, release))
			return err
		})
		<-started

		for _, detached := range []bool{false, true} {
			_, err := turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: "turn-1/call/b", Kind: rocketcode.BackgroundKindTask, SubagentKey: "/a", Detached: detached}, &backgroundWorkMock{})
			require.EqualError(t, err, "subagent /a is busy with another turn; continue it again later", "an attached task reports to its caller, not with a note")
		}

		close(release)
		require.NoError(t, call.Wait())
	})

	// A move whose row cannot be written ends the work instead of leaving it running unrecorded.
	t.Run("moved without a row", func(t *testing.T) {
		started := make(chan struct{}, 1)
		taken := testBackgroundJob("main", "turn-0/call/z")
		taken.kind, taken.subagentKey = backgroundTask, "/a"
		createTestBackgroundJob(t, store, taken)

		var call errgroup.Group

		work := blockedWork(started, make(chan struct{}))

		call.Go(func() error {
			_, err := turn.Run(t.Context(), job, work)
			return err
		})
		<-started
		require.Len(t, registry.move("main"), 1)
		require.Error(t, call.Wait(), "the subagent key is held by another running row")

		workErr := context.Cause(work.RunBackgroundCalls()[0].Ctx)
		require.ErrorIs(t, workErr, errBackgroundAbandoned)
		require.False(t, registry.movable("main"))
		require.Len(t, work.DetachCalls(), 1, "the work detaches before its row is written")

		finishTestBackgroundJob(t, store, taken, backgroundCompleted, false)
	})

	// A move landing after the script stored its result for the turn leaves the call that result.
	t.Run("moved after the result settled", func(t *testing.T) {
		started, release := make(chan struct{}, 1), make(chan struct{})
		work := blockedWork(started, release)
		work.DetachFunc = func() bool { return false }

		var (
			call   errgroup.Group
			result rocketcode.BackgroundResult
		)

		call.Go(func() (err error) {
			result, err = turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: "turn-1/call/s", Kind: rocketcode.BackgroundKindExecute}, work)
			return err
		})
		<-started
		require.Len(t, registry.move("main"), 1)
		close(release)
		require.NoError(t, call.Wait())
		require.Equal(t, rocketcode.BackgroundResult{Output: "done"}, result)
		require.NotContains(t, backgroundJobIDs(t, store, "main"), "turn-1/call/s", "the job leaves no row and no note")
	})

	t.Run("moved", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		started, release := make(chan struct{}, 1), make(chan struct{})

		var (
			call    errgroup.Group
			result  rocketcode.BackgroundResult
			workCtx context.Context
		)

		work := &backgroundWorkMock{
			RunBackgroundFunc: func(ctx context.Context) (string, error) {
				workCtx = ctx

				started <- struct{}{}

				<-release

				return "done", context.Cause(ctx)
			},
			DetachFunc: func() bool { return true },
		}

		call.Go(func() (err error) {
			result, err = turn.Run(ctx, job, work)
			return err
		})
		<-started
		require.True(t, registry.movable("main"))
		require.False(t, registry.movable("other"))
		require.Len(t, registry.move("main"), 1)
		require.NoError(t, call.Wait())
		require.Equal(t, rocketcode.BackgroundResult{Moved: true}, result)
		require.Len(t, work.DetachCalls(), 1)
		require.False(t, registry.movable("main"))
		require.Equal(t, backgroundRunning, testBackgroundRow(t, store, "main", job.ID).status)

		cancel(errors.New("turn ended"))
		require.Never(t, func() bool { return workCtx.Err() != nil }, 100*time.Millisecond, time.Millisecond, "moved work no longer follows its call")
		close(release)
		require.Equal(t, "main", <-notes)

		row := testBackgroundRow(t, store, "main", job.ID)
		require.Equal(t, backgroundCompleted, row.status)
		require.Equal(t, "done", row.result)
	})
}

func TestBackgroundJobsRunWithoutLimit(t *testing.T) {
	store := newTestSessionService(t)
	registry, notes := newTestBackgroundRegistry(store)
	turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{}}

	const jobs = 40

	started, release := make(chan struct{}, jobs), make(chan struct{})

	for i := range jobs {
		_, err := turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: fmt.Sprintf("turn-1/call/%d", i), Kind: rocketcode.BackgroundKindExecute, Detached: true}, blockedWork(started, release))
		require.NoError(t, err)
	}

	for range jobs {
		<-started
	}

	close(release)

	for range jobs {
		require.Equal(t, "main", <-notes)
	}
}

func TestBackgroundJobsAbandoned(t *testing.T) {
	store := newTestSessionService(t)
	registry, notes := newTestBackgroundRegistry(store)
	store.jobs = registry
	started := make(chan struct{}, 2)

	for _, conversationID := range []string{"main", "other"} {
		turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: conversationID, origin: &protocol.InboundMessage{}}
		_, err := turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: "turn-1/call/a", Kind: rocketcode.BackgroundKindExecute, Detached: true}, blockedWork(started, make(chan struct{})))
		require.NoError(t, err)
		<-started
	}

	_, err := store.DeleteSession(t.Context(), "main")
	require.NoError(t, err)
	require.Empty(t, backgroundJobIDs(t, store, "main"))
	require.Equal(t, []string{"turn-1/call/a"}, backgroundJobIDs(t, store, "other"), "deleting a conversation kills only its own jobs")

	registry.shutdown()
	require.Equal(t, backgroundRunning, testBackgroundRow(t, store, "other", "turn-1/call/a").status, "shutdown leaves the row to the next start")
	require.Empty(t, notes)
}

func backgroundRows(t *testing.T, store *SessionService) []string {
	t.Helper()

	rows, err := queryStrings(t.Context(), store.db, `SELECT kind || ' ' || status || ' ' || note_state || ' ' || result FROM background_jobs ORDER BY kind, job_id`, "background rows")
	require.NoError(t, err)

	return rows
}

// A wake leaves no note of its own; a completed wake consumes the note and a failed one releases it.
func TestSubagentNoteWakesSubagent(t *testing.T) {
	for _, errWake := range []error{nil, errors.New("provider down")} {
		t.Run(fmt.Sprint(errWake), func(t *testing.T) {
			store := newTestSessionService(t)
			notes := &backgroundNotesMock{noteReadyFunc: func(string) {}, continueSubagentFunc: func(context.Context, *backgroundJob, string) (string, error) { return "analysis", errWake }}
			registry := newBackgroundRegistry(store, notes, testLogger())
			turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{Source: protocol.SourceSlack}, turnID: "turn-1"}
			started, release := make(chan struct{}, 1), make(chan struct{})

			_, err := turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: "turn-1/call/a/x", Kind: rocketcode.BackgroundKindExecute, Label: "tests", Agent: "researcher", ChildKey: "/a", Detached: true}, blockedWork(started, release))
			require.NoError(t, err)
			<-started
			close(release)
			require.NoError(t, registry.group.Wait())

			require.Empty(t, notes.noteReadyCalls(), "the parent is not told")

			calls := notes.continueSubagentCalls()
			require.Len(t, calls, 1, "one wake and no further one")
			require.Equal(t, "/a", calls[0].Job.subagentKey)
			require.Equal(t, "researcher", calls[0].Job.agent)
			require.Equal(t, "[System]\n\n"+`<execute id="turn-1/call/a/x" state="completed" description="tests">`+"\ndone\n</execute>", calls[0].Input)

			note, wake := "execute completed consumed done", "subagent_wake completed none analysis"
			if errWake != nil {
				note, wake = "execute completed pending done", "subagent_wake failed none provider down"
			}

			require.Equal(t, []string{note, wake}, backgroundRows(t, store))

			if errWake != nil {
				require.False(t, testBackgroundRow(t, store, "main", "turn-1/call/a/x").wake, "the released note wakes nothing")
				require.Equal(t, []rocketcode.PromptInput{systemPromptInput("turn-1/call/a/x", `<execute id="turn-1/call/a/x" state="completed" description="tests">`+"\ndone\n</execute>")}, turn.DrainNotes(t.Context(), "/a"), "the subagent's next turn takes it")
			}
		})
	}
}

// A resumed parent call re-attaching to a resumed subagent starts nothing; a second start replays nothing.
func TestRestartSettlesRunningJobs(t *testing.T) {
	store := newTestSessionService(t)

	for conversationID, origin := range map[string]*protocol.InboundMessage{"main": {Source: protocol.SourceSlack}, "cron:daily": {RequireOutputDecision: true}, "private": {SyncDestination: "managed"}} {
		script := testBackgroundJob(conversationID, "turn-1/call/"+conversationID)
		script.origin = origin
		createTestBackgroundJob(t, store, script)
	}

	task := testBackgroundJob("main", "turn-1/call/a")
	task.kind, task.subagentKey, task.label = backgroundTask, "/a", "research"
	createTestBackgroundJob(t, store, task)

	note := testBackgroundJob("main", "turn-1/call/n")
	note.childKey, note.agent = "/b", "researcher"
	createTestBackgroundJob(t, store, note)
	finishTestBackgroundJob(t, store, note, backgroundCompleted, true)

	wake := testBackgroundJob("main", "/b/wake/W")
	wake.kind, wake.childKey, wake.subagentKey = backgroundSubagentWake, "/b", "/b"
	createTestBackgroundJob(t, store, wake)
	_, err := claimBackgroundNotes(t.Context(), store.db, "main", "/b", wake.jobID, false)
	require.NoError(t, err)

	started, release := make(chan struct{}, 1), make(chan struct{})
	notes := &backgroundNotesMock{noteReadyFunc: func(string) {}, continueSubagentFunc: func(_ context.Context, job *backgroundJob, _ string) (string, error) {
		if job.kind == backgroundSubagentWake {
			return "analysis", nil
		}

		started <- struct{}{}

		<-release

		return "found it", nil
	}}
	registry := newBackgroundRegistry(store, notes, testLogger())
	resume, err := registry.recoverJobs(t.Context())
	require.NoError(t, err)

	turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{}}
	_, err = turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: "turn-2/call/c", Kind: rocketcode.BackgroundKindTask, SubagentKey: "/a"}, &backgroundWorkMock{})
	require.EqualError(t, err, "subagent /a is still running; wait for its note before continuing it", "the subagent is busy before it resumes")
	require.Empty(t, notes.noteReadyCalls(), "nothing wakes before resume")

	require.NoError(t, resume(t.Context()))
	<-started

	woken := make([]string, 0, 2)
	for _, call := range notes.noteReadyCalls() {
		woken = append(woken, call.ConversationID)
	}

	require.Equal(t, []string{"cron:daily", "private"}, woken, "only hidden runs are woken for killed scripts")

	for _, detached := range []bool{false, true} {
		attach := &backgroundWorkMock{DetachFunc: func() bool { return true }}
		result, err := turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: task.jobID, Kind: rocketcode.BackgroundKindTask, SubagentKey: "/a", Detached: detached}, attach)
		require.NoError(t, err)
		require.Equal(t, rocketcode.BackgroundResult{Moved: true}, result, "a re-attached call does not run the subagent again")
		require.Len(t, attach.DetachCalls(), 1)
	}

	close(release)
	require.NoError(t, registry.group.Wait())

	calls := notes.continueSubagentCalls()
	require.Len(t, calls, 2)

	for _, call := range calls {
		if call.Job.kind == backgroundTask {
			require.Equal(t, task.jobID, call.Job.jobID)
			require.Equal(t, registry.runnerID, call.Job.runnerID)
			require.Empty(t, call.Input, "the subagent resumes its journaled turn")

			continue
		}

		require.Equal(t, "/b", call.Job.subagentKey)
		require.Contains(t, call.Input, `<execute id="turn-1/call/n" state="completed"`, "the wake's note is delivered again")
	}

	rows := func() []string {
		rows, err := queryStrings(t.Context(), store.db, `SELECT CASE kind WHEN 'subagent_wake' THEN kind ELSE job_id END || ' ' || status || ' ' || note_state || ' ' || wake::text || ' ' || result FROM background_jobs ORDER BY 1`, "background rows")
		require.NoError(t, err)

		return rows
	}
	settled := []string{
		"subagent_wake completed none false analysis",
		"subagent_wake killed none false ",
		"turn-1/call/a completed pending true found it",
		"turn-1/call/cron:daily killed pending true ",
		"turn-1/call/main killed pending false ",
		"turn-1/call/n completed consumed true completed",
		"turn-1/call/private killed pending true ",
	}
	require.Equal(t, settled, rows())

	again := &backgroundNotesMock{noteReadyFunc: func(string) {}}
	next := newBackgroundRegistry(store, again, testLogger())
	resume, err = next.recoverJobs(t.Context())
	require.NoError(t, err)
	require.NoError(t, resume(t.Context()))
	require.Equal(t, settled, rows(), "a second start replays nothing")
	require.Empty(t, again.continueSubagentCalls())
}

// Notes are claimed for the nearest detached job among the subagent and its ancestors; no wake starts.
func TestRunningSubagentDrainsItsNotes(t *testing.T) {
	store := newTestSessionService(t)
	notes := &backgroundNotesMock{noteReadyFunc: func(string) {}}
	registry := newBackgroundRegistry(store, notes, testLogger())
	turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{}, turnID: "turn-1"}
	started, release, finished := make(chan struct{}, 4), make(chan struct{}), make(chan struct{})
	close(finished)

	// The detached subagent /a runs its own subagent /a/b attached.
	_, err := turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: "turn-1/call/a", Kind: rocketcode.BackgroundKindTask, Label: "research", Agent: "main", SubagentKey: "/a", Detached: true}, blockedWork(started, release))
	require.NoError(t, err)
	<-started

	var nested errgroup.Group
	nested.Go(func() error {
		_, err := turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: "turn-1/call/a/b", Kind: rocketcode.BackgroundKindTask, Agent: "researcher", ChildKey: "/a", SubagentKey: "/a/b"}, blockedWork(started, release))
		return err
	})
	<-started

	for jobID, owner := range map[string]string{"turn-1/call/s1": "/a", "turn-1/call/s2": "/a/b"} {
		_, err = turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: jobID, Kind: rocketcode.BackgroundKindExecute, Label: "tests", Agent: "researcher", ChildKey: owner, Detached: true}, blockedWork(started, finished))
		require.NoError(t, err)
		<-started
	}

	require.Eventually(t, func() bool {
		states, err := queryStrings(context.Background(), store.db, `SELECT note_state FROM background_jobs WHERE kind = 'execute'`, "notes")
		return err == nil && slices.Equal(states, []string{"pending", "pending"})
	}, 5*time.Second, time.Millisecond)

	for jobID, owner := range map[string]string{"turn-1/call/s1": "/a", "turn-1/call/s2": "/a/b"} {
		header := "[System]"
		require.Equal(t, []rocketcode.PromptInput{{ID: jobID, Text: header + "\n\n" + `<execute id="` + jobID + `" state="completed" description="tests">` + "\ndone\n</execute>", Header: header}}, turn.DrainNotes(t.Context(), owner))
		require.Empty(t, turn.DrainNotes(t.Context(), owner))
	}

	claims, err := queryStrings(t.Context(), store.db, `SELECT claimed_turn_id FROM background_jobs WHERE kind = 'execute'`, "claims")
	require.NoError(t, err)
	require.Equal(t, []string{"turn-1/call/a", "turn-1/call/a"}, claims, "the detached /a settles both claims")

	close(release)
	require.NoError(t, nested.Wait())
	require.NoError(t, registry.group.Wait())
	require.Empty(t, notes.continueSubagentCalls(), "the running subagents took the notes")
	require.Equal(t, []string{"execute completed consumed done", "execute completed consumed done", "task completed pending done"}, backgroundRows(t, store))
	require.Len(t, notes.noteReadyCalls(), 1, "only the task's own note reaches the main agent")
}
