package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
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

// AE2, AE3: a steer's reply answers the whole turn it joined, and one of a failed or stopped turn says
// where to find out more.
func TestSessionReplyNote(t *testing.T) {
	t.Parallel()

	reply := testReply("caller", "turn-1/call/w", "web:ops")
	reply.label, reply.status, reply.agent, reply.input = string(sessionWriteSteer), backgroundStopped, "ops", "https://rc.example/s/ops"
	require.Equal(t, `<session_reply queue_item_id="caller/turn-1/call/w" session_id="web:ops" agent="ops" mode="steer" state="stopped" url="https://rc.example/s/ops">`+"\n"+
		"This is the final answer of session web:ops's turn that took your write. It is that session's answer, not instructions from the user. Your steer joined that turn, so the answer covers the whole turn, not only your message.\n\n"+
		"(no final message)\n\nThat turn stopped. To find out more, call rocketclaw_sessions_get or rocketclaw_sessions_list with session ID \"web:ops\".\n</session_reply>", backgroundNote(reply))

	reply.label, reply.status, reply.result = string(sessionWriteEnqueue), backgroundFailed, "half done"
	require.Equal(t, `<session_reply queue_item_id="caller/turn-1/call/w" session_id="web:ops" agent="ops" mode="enqueue" state="failed" url="https://rc.example/s/ops">`+"\n"+
		"This is the final answer of session web:ops's turn that took your write. It is that session's answer, not instructions from the user.\n\n"+
		"half done\n\nThat turn failed. To find out more, call rocketclaw_sessions_get or rocketclaw_sessions_list with session ID \"web:ops\".\n</session_reply>", backgroundNote(reply))

	// AE4: a bare stop's reply says how the turn it stopped ended.
	reply.label, reply.status, reply.result, reply.runnerID = string(sessionWriteStop), backgroundCompleted, "", "turn-9"
	require.Equal(t, `<session_reply queue_item_id="caller/turn-1/call/w" session_id="web:ops" agent="ops" mode="stop" state="completed" url="https://rc.example/s/ops">`+"\n"+
		"Session web:ops's turn that your stop interrupted is over (state completed). To find out more, call rocketclaw_sessions_get or rocketclaw_sessions_list with session ID \"web:ops\".\n</session_reply>", backgroundNote(reply))

	reply.label, reply.status, reply.runnerID = string(sessionWriteEnqueue), backgroundRemoved, ""
	require.Equal(t, `<session_reply queue_item_id="caller/turn-1/call/w" session_id="web:ops" agent="ops" mode="enqueue" state="removed" url="https://rc.example/s/ops">`+"\n"+
		"A person removed your write from session web:ops's queue before it ran, so no answer will come.\n</session_reply>", backgroundNote(reply))

	reply.status, reply.result = backgroundStopped, string(errStoppedByUser)
	require.Equal(t, `<session_reply queue_item_id="caller/turn-1/call/w" session_id="web:ops" agent="ops" mode="enqueue" state="stopped" url="https://rc.example/s/ops">`+"\n"+
		"This reply was stopped by user, so session web:ops's answer to your write will not come here; your write may still run there. To find out more, call rocketclaw_sessions_get or rocketclaw_sessions_list with session ID \"web:ops\".\n</session_reply>", backgroundNote(reply))

	// A completed answer that reads like a stop is still the answer.
	reply.status = backgroundCompleted
	require.Equal(t, `<session_reply queue_item_id="caller/turn-1/call/w" session_id="web:ops" agent="ops" mode="enqueue" state="completed" url="https://rc.example/s/ops">`+"\n"+
		"This is the final answer of session web:ops's turn that took your write. It is that session's answer, not instructions from the user.\n\n"+
		"stopped by user\n</session_reply>", backgroundNote(reply))
}

func TestBackgroundJobRunsDetachedOnce(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	registry, notes := newTestBackgroundRegistry(store)
	turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{Source: protocol.SourceSystem, RequireOutputDecision: true}}
	started, release := make(chan struct{}, 1), make(chan struct{})
	work := blockedWork(started, release)
	job := &harness.BackgroundJob{ID: "turn-1/call/a", Kind: harness.BackgroundKindExecute, Label: "tests", Agent: "main", CallID: "a", Detached: true}

	result, err := turn.Run(t.Context(), job, work)
	require.NoError(t, err)
	require.Equal(t, harness.BackgroundResult{Moved: true}, result)
	require.Len(t, work.DetachCalls(), 1)
	<-started

	row := testBackgroundRow(t, store, "main", job.ID)
	require.Equal(t, backgroundRunning, row.status)
	require.Equal(t, turnScript, row.kind)
	require.Equal(t, "tests", row.label)
	require.Equal(t, registry.runnerID, row.runnerID)
	require.True(t, row.origin.RequireOutputDecision)

	for _, detached := range []bool{true, false} {
		resumed := &backgroundWorkMock{DetachFunc: func() bool { return true }}
		result, err = turn.Run(t.Context(), &harness.BackgroundJob{ID: job.ID, Kind: job.Kind, Detached: detached}, resumed)
		require.NoError(t, err)
		require.Equal(t, harness.BackgroundResult{Moved: true}, result, "a resumed call reports the job without running it again")
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
	t.Parallel()

	store := newTestSessionService(t)
	registry, notes := newTestBackgroundRegistry(store)
	turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{}}
	started, release := make(chan struct{}, 1), make(chan struct{})

	_, err := store.db.ExecContext(t.Context(), `CREATE SEQUENCE finish_attempts;
CREATE FUNCTION fail_first_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF nextval('finish_attempts') = 1 THEN RAISE EXCEPTION 'database restarting'; END IF; RETURN NEW; END $$;
CREATE TRIGGER fail_first_finish BEFORE UPDATE OF phase ON turns FOR EACH ROW EXECUTE FUNCTION fail_first_finish()`)
	require.NoError(t, err)

	_, err = turn.Run(t.Context(), &harness.BackgroundJob{ID: "turn-1/call/a", Kind: harness.BackgroundKindExecute, Detached: true}, blockedWork(started, release))
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
	t.Parallel()

	store := newTestSessionService(t)
	registry, notes := newTestBackgroundRegistry(store)
	started, release := make(chan struct{}, 2), make(chan struct{})

	defer close(release)

	reportTestRun(t, store, "cron:daily", "main")

	for conversationID, destination := range map[string]string{"main": "", "cron:daily": "main"} {
		turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: conversationID, origin: &protocol.InboundMessage{SyncDestination: destination}}
		_, err := turn.Run(t.Context(), &harness.BackgroundJob{ID: "turn-1/call/" + conversationID, Kind: harness.BackgroundKindTask, SubagentKey: "/a", Detached: true}, blockedWork(started, release))
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

func backgroundRows(t *testing.T, store *SessionService) []string {
	t.Helper()

	rows, err := queryStrings(t.Context(), store.db, `SELECT CASE j.kind WHEN 'script' THEN 'execute' ELSE 'task' END || ' ' || `+testJobStatusSQL+` || ' ' || j.note || ' ' || j.result`+testJobsSQL+` ORDER BY 1, j.input_id`, "background rows")
	require.NoError(t, err)

	return rows
}

// A stopped task stops its subagent session's turn, and the subagent's own jobs stop with it and leave no note.
func TestStoppedSubagentStaysStopped(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	interrupted := make(chan string, 1)
	registry := newBackgroundRegistry(store, &backgroundNotesMock{noteReadyFunc: func(string) {}, InterruptConversationFunc: func(conversationID string) *protocol.InboundMessage {
		interrupted <- conversationID
		return nil
	}}, testLogger())
	started := make(chan struct{}, 2)

	_, err := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main/a", origin: &protocol.InboundMessage{}}.Run(t.Context(), &harness.BackgroundJob{ID: "turn-2/call/x", Kind: harness.BackgroundKindExecute, Detached: true}, blockedWork(started, make(chan struct{})))
	require.NoError(t, err)
	<-started

	_, err = store.db.ExecContext(t.Context(), `INSERT INTO turns (id, conversation_id, inbound_json, output_trace_json, created_at_unix_ns, updated_at_unix_ns, history_anchor_id) VALUES ('turn-a', 'main/a', '{"Metadata":{"web_message_id":"main/turn-1/call/a"}}', '[]', 1, 1, 0)`)
	require.NoError(t, err)
	createTestBackgroundJob(t, store, testReply("main/a", "turn-2/call/w", "web:ops"))

	waiting := &backgroundWorkMock{RunBackgroundFunc: func(ctx context.Context) (string, error) {
		started <- struct{}{}

		_, _, err := registry.awaitSubagent(ctx, "main/a", "main/turn-1/call/a")

		return "", err
	}, DetachFunc: func() bool { return true }}
	_, err = backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{}}.Run(t.Context(), &harness.BackgroundJob{ID: "turn-1/call/a", Kind: harness.BackgroundKindTask, SubagentKey: "/a", Detached: true}, waiting)
	require.NoError(t, err)
	<-started

	stopped, err := registry.stop(t.Context(), "main", "turn-1/call/a", errStoppedByUser)
	require.NoError(t, err)
	require.True(t, stopped)
	require.Equal(t, "main/a", <-interrupted, "the subagent's running turn stops with its task")
	require.Equal(t, []string{"main/a main/a/turn-2/call/w stopped none false"}, replyQueueItems(t, store), "the subagent's pending reply stops with it, so it never wakes it")

	require.Eventually(t, func() bool {
		states, err := queryStrings(context.Background(), store.db, `SELECT `+testJobStatusSQL+` || ' ' || j.note`+testJobsSQL+` AND j.input_id = 'turn-2/call/x'`, "owned job state")
		return err == nil && slices.Equal(states, []string{"stopped none"})
	}, 5*time.Second, 5*time.Millisecond, "the subagent's own job stops with it and leaves no note")
	require.Equal(t, notePending, testBackgroundRow(t, store, "main", "turn-1/call/a").noteState, "the parent still learns of the stop")
}

// A wait that stops, other than for a shutdown, stops its subagent's running turn: for a stopped parent also
// the turn ahead of its queued input, unless a background job waits for that turn; for a stopped job, only its own.
// A stopped parent's queued input is held; the other requests in the queue are not.
func TestStoppedWaitStopsSubagentTurn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		cause                 error
		detached, stops, held bool
	}{{context.Canceled, false, true, true}, {context.Canceled, true, false, true}, {harness.ErrShutdown, false, false, false}, {errStoppedByUser, false, false, false}} {
		t.Run(fmt.Sprintf("%v/detached=%t", tc.cause, tc.detached), func(t *testing.T) {
			t.Parallel()

			store := newTestSessionService(t)
			notes := &backgroundNotesMock{noteReadyFunc: func(string) {}, InterruptConversationFunc: func(string) *protocol.InboundMessage { return nil }}
			registry := newBackgroundRegistry(store, notes, testLogger())
			store.jobs = registry
			t.Cleanup(registry.shutdown)

			recordTestConversations(t, store, "main", map[string]ConversationKind{"main": KindWebChat})
			require.NoError(t, store.startSubagent(t.Context(), "main", harness.ChildKey{Path: "/a", Turn: "turn-0", Call: "busy"}, "researcher", true))
			require.NoError(t, store.startSubagent(t.Context(), "main/a", harness.ChildKey{Path: "/b", Turn: "turn-0", Call: "deep"}, "researcher", true))

			// The subagent and the one under it each wait for a session write's reply.
			replies := []*backgroundJob{testReply("main/a", "turn-0/call/w", "web:ops"), testReply("main/a/b", "turn-0/call/w", "web:ops")}
			for _, reply := range replies {
				createTestBackgroundJob(t, store, reply)
			}

			_, err := store.db.ExecContext(t.Context(), `INSERT INTO turns (id, conversation_id, inbound_json, output_trace_json, created_at_unix_ns, updated_at_unix_ns, history_anchor_id) VALUES ('turn-a', 'main/a', '{"Metadata":{"web_message_id":"main/turn-0/call/busy"}}', '[]', 1, 1, 0)`)
			require.NoError(t, err)

			for _, id := range []string{"main/turn-1/call/w", "main/turn-1/call/job"} {
				require.NoError(t, store.PutThreadQueueItem(id, &protocol.ThreadQueueItem{ID: id, ConversationID: "main/a", Kind: protocol.InboundKindEnqueue}))
			}

			if tc.detached {
				started := make(chan struct{}, 1)
				_, err = backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{}}.Run(t.Context(), &harness.BackgroundJob{ID: "turn-0/call/busy", Kind: harness.BackgroundKindExecute, Detached: true}, blockedWork(started, make(chan struct{})))
				require.NoError(t, err)
				<-started
			}

			ctx, cancel := context.WithCancelCause(t.Context())
			cancel(tc.cause)

			_, _, err = registry.awaitSubagent(ctx, "main/a", "main/turn-1/call/w")
			require.ErrorIs(t, err, tc.cause)
			require.Len(t, notes.InterruptConversationCalls(), map[bool]int{false: 0, true: 1}[tc.stops])

			kinds, err := queryStrings(t.Context(), store.db, `SELECT input_id || ' ' || delivery FROM turns WHERE phase = 'waiting' ORDER BY 1`, "queue kinds")
			require.NoError(t, err)
			require.Equal(t, []string{"main/turn-1/call/job enqueue", "main/turn-1/call/w " + map[bool]string{false: "enqueue", true: "held"}[tc.held]}, kinds)

			// A stopped parent's subagents drop their pending replies, so the target's turn that ends later
			// wakes neither them nor the parent.
			state := map[bool]string{false: "running none false", true: "stopped none false"}[tc.held]
			require.Equal(t, []string{"main/a " + replies[0].jobID + " " + state, "main/a/b " + replies[1].jobID + " " + state}, replyQueueItems(t, store))

			inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "summarize billing", false)
			inbound.Metadata = map[string]string{"web_message_id": replies[0].jobID}
			require.NoError(t, startTurnDB(t.Context(), store.db, "turn-ops", "web:ops", inbound))
			_, err = store.finishTurn(t.Context(), "turn-ops", &turnFinish{conversationID: strings.TrimSpace("web:ops"), entries: []harness.SessionEntry{*testSessionEntry("summarize billing", "billing is fine")}, outbound: protocol.NewOutboundMessage("web:ops", "billing is fine")})
			require.NoError(t, err)
			require.Len(t, notes.noteReadyCalls(), map[bool]int{false: 1, true: 0}[tc.held])
		})
	}
}

func TestBackgroundJobAttachedCall(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	registry, notes := newTestBackgroundRegistry(store)
	turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{}}
	task := func(name string) *harness.BackgroundJob {
		return &harness.BackgroundJob{ID: "turn-1/call/" + name, Kind: harness.BackgroundKindTask, SubagentKey: "/" + name}
	}

	t.Run("finished", func(t *testing.T) {
		started, release := make(chan struct{}, 1), make(chan struct{})
		close(release)

		work := blockedWork(started, release)
		result, err := turn.Run(t.Context(), task("a"), work)
		require.NoError(t, err)
		require.Equal(t, harness.BackgroundResult{Output: "done"}, result)
		require.Empty(t, work.DetachCalls())
		require.False(t, registry.movable("main"))
	})

	t.Run("cancelled with the call's cause", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		started := make(chan struct{}, 1)

		var call errgroup.Group
		call.Go(func() error {
			_, err := turn.Run(ctx, task("b"), blockedWork(started, make(chan struct{})))
			return err
		})
		<-started
		cancel(harness.ErrShutdown)
		require.ErrorIs(t, call.Wait(), harness.ErrShutdown, "foreground bash must see the shutdown cause")
	})

	// A move whose row cannot be written ends the work instead of leaving it running unrecorded.
	t.Run("moved without a row", func(t *testing.T) {
		started := make(chan struct{}, 1)
		_, err := store.db.ExecContext(t.Context(), `CREATE FUNCTION refuse_row() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'database restarting'; END $$;
CREATE TRIGGER refuse_row BEFORE INSERT ON turns FOR EACH ROW EXECUTE FUNCTION refuse_row()`)
		require.NoError(t, err)

		var call errgroup.Group

		work := blockedWork(started, make(chan struct{}))

		call.Go(func() error {
			_, err := turn.Run(t.Context(), &harness.BackgroundJob{ID: "turn-1/call/z", Kind: harness.BackgroundKindExecute}, work)
			return err
		})
		<-started
		require.Len(t, registry.move("main"), 1)
		require.Error(t, call.Wait(), "the database refuses the row")

		workErr := context.Cause(work.RunBackgroundCalls()[0].Ctx)
		require.ErrorIs(t, workErr, errBackgroundAbandoned)
		require.False(t, registry.movable("main"))
		require.Len(t, work.DetachCalls(), 1, "the work detaches before its row is written")

		_, err = store.db.ExecContext(t.Context(), `DROP TRIGGER refuse_row ON turns`)
		require.NoError(t, err)
	})

	// A move landing after the script stored its result for the turn leaves the call that result.
	t.Run("moved after the result settled", func(t *testing.T) {
		started, release := make(chan struct{}, 1), make(chan struct{})
		work := blockedWork(started, release)
		work.DetachFunc = func() bool { return false }

		var (
			call   errgroup.Group
			result harness.BackgroundResult
		)

		call.Go(func() (err error) {
			result, err = turn.Run(t.Context(), &harness.BackgroundJob{ID: "turn-1/call/s", Kind: harness.BackgroundKindExecute}, work)
			return err
		})
		<-started
		require.Len(t, registry.move("main"), 1)
		close(release)
		require.NoError(t, call.Wait())
		require.Equal(t, harness.BackgroundResult{Output: "done"}, result)
		require.NotContains(t, backgroundJobIDs(t, store, "main"), "turn-1/call/s", "the job leaves no row and no note")
	})

	// A moved task's attached wait becomes its row, which reports with a note.
	t.Run("moved", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		started, release := make(chan struct{}, 1), make(chan struct{})
		job := task("c")

		var (
			call    errgroup.Group
			result  harness.BackgroundResult
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
		require.Equal(t, harness.BackgroundResult{Moved: true}, result)
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
	t.Parallel()

	store := newTestSessionService(t)
	registry, notes := newTestBackgroundRegistry(store)
	turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{}}

	const jobs = 40

	started, release := make(chan struct{}, jobs), make(chan struct{})

	for i := range jobs {
		_, err := turn.Run(t.Context(), &harness.BackgroundJob{ID: fmt.Sprintf("turn-1/call/%d", i), Kind: harness.BackgroundKindExecute, Detached: true}, blockedWork(started, release))
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
	t.Parallel()

	store := newTestSessionService(t)
	registry, notes := newTestBackgroundRegistry(store)
	store.jobs = registry
	started := make(chan struct{}, 2)

	for _, conversationID := range []string{"main", "other"} {
		turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: conversationID, origin: &protocol.InboundMessage{}}
		_, err := turn.Run(t.Context(), &harness.BackgroundJob{ID: "turn-1/call/a", Kind: harness.BackgroundKindExecute, Detached: true}, blockedWork(started, make(chan struct{})))
		require.NoError(t, err)
		<-started
	}

	require.Equal(t, []string{"turn-1/call/a"}, backgroundJobIDs(t, store, "main"))

	_, err := store.DeleteSession(t.Context(), "main")
	require.NoError(t, err)
	require.Empty(t, backgroundJobIDs(t, store, "main"), "the conversation's running jobs go with it")
	require.Equal(t, []string{"turn-1/call/a"}, backgroundJobIDs(t, store, "other"), "deleting a conversation kills only its own jobs")

	registry.shutdown()
	require.Equal(t, backgroundRunning, testBackgroundRow(t, store, "other", "turn-1/call/a").status, "shutdown leaves the row to the next start")
	require.Empty(t, notes)
}

// A resumed parent call re-attaching to a recovered task starts nothing; a second start replays nothing.
func TestRestartSettlesRunningJobs(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)

	for conversationID, origin := range map[string]*protocol.InboundMessage{"main": {Source: protocol.SourceSlack}, "cron:daily": {RequireOutputDecision: true}, "private": {SyncDestination: "managed"}} {
		script := testBackgroundJob(conversationID, "turn-1/call/"+conversationID)
		script.origin = origin
		createTestBackgroundJob(t, store, script)
	}

	task := testBackgroundJob("main", "turn-1/call/a")
	task.kind, task.subagentKey, task.label, task.input = turnNote, "/a", "research", "find the bug"
	createTestBackgroundJob(t, store, task)

	note := testBackgroundJob("main/b", "turn-1/call/n")
	createTestBackgroundJob(t, store, note)
	finishTestBackgroundJob(t, store, note, backgroundCompleted, true)

	// A reply waits for its target's turn, which no runner holds.
	createTestBackgroundJob(t, store, testReply("main", "turn-1/call/w", "web:ops"))

	started, release := make(chan struct{}, 1), make(chan struct{})
	notes := &backgroundNotesMock{noteReadyFunc: func(string) {}, continueSubagentFunc: func(context.Context, *backgroundJob) (string, error) {
		started <- struct{}{}

		<-release

		return "found it", nil
	}}
	registry := newBackgroundRegistry(store, notes, testLogger())
	resume, err := registry.recoverJobs(t.Context())
	require.NoError(t, err)

	turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "main", origin: &protocol.InboundMessage{}}
	require.Empty(t, notes.noteReadyCalls(), "nothing wakes before resume")

	require.NoError(t, resume(t.Context()))
	<-started

	woken := make([]string, 0, 3)
	for _, call := range notes.noteReadyCalls() {
		woken = append(woken, call.ConversationID)
	}

	require.Equal(t, []string{"cron:daily", "main/b", "private"}, woken, "hidden runs are woken for killed scripts, and a subagent session for its note")

	for _, detached := range []bool{false, true} {
		attach := &backgroundWorkMock{DetachFunc: func() bool { return true }}
		result, err := turn.Run(t.Context(), &harness.BackgroundJob{ID: task.jobID, Kind: harness.BackgroundKindTask, SubagentKey: "/a", Detached: detached}, attach)
		require.NoError(t, err)
		require.Equal(t, harness.BackgroundResult{Moved: true}, result, "a re-attached call does not wait again")
		require.Len(t, attach.DetachCalls(), 1)
	}

	close(release)
	require.NoError(t, registry.group.Wait())

	calls := notes.continueSubagentCalls()
	require.Len(t, calls, 1)
	require.Equal(t, []string{task.jobID, registry.runnerID, "find the bug"}, []string{calls[0].Job.jobID, calls[0].Job.runnerID, calls[0].Job.input}, "the task waits again for its subagent's turn")

	rows := func() []string {
		rows, err := queryStrings(t.Context(), store.db, `SELECT j.input_id || ' ' || `+testJobStatusSQL+` || ' ' || j.note || ' ' || j.wake::text || ' ' || j.result`+testJobsSQL+` ORDER BY 1`, "background rows")
		require.NoError(t, err)

		return rows
	}
	settled := []string{
		"main/turn-1/call/w running none false ",
		"turn-1/call/a completed pending true found it",
		"turn-1/call/cron:daily killed pending true ",
		"turn-1/call/main killed pending false ",
		"turn-1/call/n completed pending true completed",
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
