package backend

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"golang.org/x/sync/errgroup"
)

// errBackgroundAbandoned kills detached jobs at shutdown and before their conversation is
// deleted. Unlike rocketcode.ErrShutdown it also stops bash, and the job records nothing:
// shutdown leaves its row running for the next start, and deletion removes the row.
var errBackgroundAbandoned = fmt.Errorf("background job abandoned: %w", rocketcode.ErrBackgroundKilled)

// backgroundStopError is why a job was stopped; it becomes the stopped job's result.
type backgroundStopError string

const (
	errStoppedByUser  backgroundStopError = "stopped by user"
	errStoppedByAgent backgroundStopError = "stopped by agent"
	// errStoppedWithOwner stops a subagent's own jobs with it; they leave no Completion Note.
	errStoppedWithOwner backgroundStopError = "stopped with its subagent"
)

func (e backgroundStopError) Error() string { return string(e) }

// Unwrap lets a stop kill the job's running bash.
func (backgroundStopError) Unwrap() error { return rocketcode.ErrBackgroundKilled }

// errMovedToBackground ends a moved job's turn-bound waits, such as a script's pending question.
var errMovedToBackground = errors.New("the user moved the work to the background")

// backgroundMovedKey holds, in an attached job's work context, the context its move cancels.
type backgroundMovedKey struct{}

// movedNote follows OpenCode's Session.background note (packages/core/src/session.ts);
// questionWithdrawn is what a moved script's pending ask_user_question returns, so the script keeps running.
const (
	movedNote         = "User requested that active blocking work be moved to the background.\n\nBackgrounded work:\n%s\nThe backgrounded work is still unfinished. Move on to other work if you can. If there is nothing else useful to do, finish your response. Do not wait, sleep, poll, or report the backgrounded work as complete until its result arrives as a note."
	questionWithdrawn = "No answer: the user moved this script to the background while ask_user_question waited, so the question was withdrawn and nobody will answer it. This text is not the user's answer. Decide without it, or ask in the turn that receives this job's result."
)

// backgroundNotes reaches the sessions jobs report to and wait for: it delivers Completion Notes through
// a session's bridge, and stops the turn of a subagent session whose task call stopped.
type backgroundNotes interface {
	// noteReady learns that conversationID has a new Completion Note.
	noteReady(conversationID string)
	// continueSubagent waits again, after a restart, for the subagent turn a task job awaited.
	continueSubagent(ctx context.Context, job *backgroundJob) (string, error)
	InterruptConversation(conversationID string) *protocol.InboundMessage
}

// backgroundNotesMetadataKey lists, in a wake turn's inbound, the jobs whose notes the turn delivers.
const backgroundNotesMetadataKey = "background_job_ids"

// backgroundNote follows OpenCode's shell notification (packages/core/src/shell/result.ts) and
// subagent result (packages/core/src/tool/plugin/subagent.ts), and says why stopped or killed work
// ended so the model does not simply run it again.
func backgroundNote(job *backgroundJob) string {
	body := job.result

	switch job.status {
	case backgroundStopped:
		body = "The job was " + job.result + ". Do not run it again unless asked."
	case backgroundKilled:
		body = "The job was killed because the server restarted."
	case backgroundRunning, backgroundCompleted, backgroundFailed:
	}

	if job.kind == backgroundExecute {
		return fmt.Sprintf("<execute id=%q state=%q description=%q>\n%s\n</execute>", job.jobID, job.status, job.label, body)
	}

	return fmt.Sprintf("<subagent id=%q state=%q description=%q continue=%q>\n%s\n</subagent>", job.jobID, job.status, job.label, job.subagentKey, body)
}

func backgroundNotesText(notes []backgroundJob) string {
	texts := make([]string, len(notes))
	for i := range notes {
		texts[i] = backgroundNote(&notes[i])
	}

	return strings.Join(texts, "\n\n")
}

// systemPromptInput is text entering a turn with a system origin, never a human one. Each
// Completion Note uses its job ID as input ID, so a resumed turn offered it again injects it once.
func systemPromptInput(id, text string) rocketcode.PromptInput {
	header := provenanceHeader(&promptProvenance{origin: "System"})
	return rocketcode.PromptInput{ID: id, Texts: []string{header + "\n\n" + text}, Header: header}
}

// conversationJobs kills a conversation's running jobs before its rows are deleted, and wakes the task
// calls waiting for subagent turns once a turn finishes.
type conversationJobs interface {
	abandon(conversationID string)
	turnFinished()
}

// noConversationJobs is the session service's conversationJobs before a run attaches its registry.
type noConversationJobs struct{}

func (noConversationJobs) abandon(string) {}

func (noConversationJobs) turnFinished() {}

type backgroundKey struct{ conversationID, jobID string }

// backgroundRun is one job's in-memory state. An attached run's call waits for done, when it
// finished attached, or moved, when a move reaches it first.
type backgroundRun struct {
	row    *backgroundJob
	cancel context.CancelCauseFunc
	// unlink stops forwarding the call's cancellation to an attached run.
	unlink func() bool
	// markMoved ends the attached run's moved context, which its call and turn-bound waits watch.
	markMoved context.CancelCauseFunc
	// recorded closes once the moved call wrote the row, failed to, or kept the work attached.
	recorded, done chan struct{}

	// Guarded by backgroundRegistry.mu.
	attached bool
	output   string
	err      error
}

// backgroundRegistry runs this process's execute and task calls of agents allowed to use
// background mode. Jobs run on one plain errgroup, so no job's error cancels another.
type backgroundRegistry struct {
	store    *SessionService
	notes    backgroundNotes
	log      *slog.Logger
	runnerID string
	group    errgroup.Group

	mu   sync.Mutex
	runs map[backgroundKey]*backgroundRun
	// finished closes, and is replaced, whenever a turn finishes, for the task calls awaiting subagent turns.
	finished chan struct{}
}

func newBackgroundRegistry(store *SessionService, notes backgroundNotes, log *slog.Logger) *backgroundRegistry {
	return &backgroundRegistry{store: store, notes: notes, log: log, runnerID: rand.Text(), runs: map[backgroundKey]*backgroundRun{}, finished: make(chan struct{})}
}

// turnRoot is a turn's workspace root. The jobs the turn starts keep using it through their
// tools after the turn ends, so its last user, the turn or one of its jobs, closes it.
type turnRoot struct {
	root *os.Root

	mu    sync.Mutex
	users int
}

func (r *turnRoot) hold() {
	r.mu.Lock()
	r.users++
	r.mu.Unlock()
}

func (r *turnRoot) release() {
	r.mu.Lock()
	r.users--
	last := r.users == 0
	r.mu.Unlock()

	if last {
		_ = r.root.Close()
	}
}

// backgroundTurn is the registry as one conversation turn's rocketcode.BackgroundJobs.
type backgroundTurn struct {
	registry       *backgroundRegistry
	root           *turnRoot
	conversationID string
	// origin is the turn's inbound; each job's row freezes it when the row is created.
	origin *protocol.InboundMessage
}

func (backgroundTurn) Enabled() bool { return true }

// Run runs work on a context detached from ctx. Until the job moves, ctx's cancellation
// reaches the work with its cause, and Run waits for the work to finish.
func (t backgroundTurn) Run(ctx context.Context, job *rocketcode.BackgroundJob, work rocketcode.BackgroundWork) (rocketcode.BackgroundResult, error) {
	r, key := t.registry, backgroundKey{t.conversationID, job.ID}

	rows, err := r.store.notedBackgroundJobs(ctx, key.conversationID, []string{key.jobID})
	if err != nil {
		return rocketcode.BackgroundResult{}, err
	}

	if len(rows) > 0 { // A resumed turn's call whose job already left it.
		work.Detach()

		return rocketcode.BackgroundResult{Moved: true}, nil
	}

	workCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))

	run := &backgroundRun{row: &backgroundJob{conversationID: key.conversationID, jobID: job.ID, kind: backgroundJobKind(job.Kind), agent: job.Agent, label: job.Label, callID: job.CallID, subagentKey: job.SubagentKey, origin: t.origin, runnerID: r.runnerID, input: job.Prompt}, cancel: cancel, recorded: make(chan struct{}), done: make(chan struct{}), attached: !job.Detached}

	r.mu.Lock()
	flipped := run.attached && !r.attachedLocked(key.conversationID)

	var moved context.Context

	if run.attached {
		run.unlink = context.AfterFunc(ctx, func() { cancel(context.Cause(ctx)) })
		moved, run.markMoved = context.WithCancelCause(context.Background())
		workCtx = context.WithValue(workCtx, backgroundMovedKey{}, moved)
	}

	r.runs[key] = run
	r.mu.Unlock()

	if flipped {
		r.movableChanged(ctx, key.conversationID)
	}

	if job.Detached {
		if _, _, err := createBackgroundJob(ctx, r.store.db, run.row); err != nil {
			r.mu.Lock()
			delete(r.runs, key)
			r.mu.Unlock()

			return rocketcode.BackgroundResult{}, err
		}

		close(run.recorded)
		work.Detach()
	}

	t.root.hold()
	r.group.Go(func() error {
		defer t.root.release()

		r.work(workCtx, key, run, work)

		return nil
	})

	if job.Detached {
		return rocketcode.BackgroundResult{Moved: true}, nil
	}

	select {
	case <-run.done:
	case <-moved.Done():
		if work.Detach() {
			defer close(run.recorded)

			if _, _, err := createBackgroundJob(context.WithoutCancel(ctx), r.store.db, run.row); err != nil {
				// Unrecorded work could neither report nor be stopped, so it ends with the call.
				run.cancel(errBackgroundAbandoned)
				<-run.done

				return rocketcode.BackgroundResult{}, err
			}

			return rocketcode.BackgroundResult{Moved: true}, nil
		}

		// The work stored its result for the turn just before the move, so the call keeps it.
		close(run.recorded)
		<-run.done
	}

	if stop, ok := errors.AsType[backgroundStopError](context.Cause(workCtx)); ok && run.err != nil {
		return rocketcode.BackgroundResult{}, stop
	}

	if run.err != nil {
		return rocketcode.BackgroundResult{}, fmt.Errorf("run background work: %w", run.err)
	}

	return rocketcode.BackgroundResult{Output: run.output}, nil
}

// work runs one job. A job that ended detached records its final state with a pending, wakeable
// Completion Note for its owner, unless it was abandoned.
func (r *backgroundRegistry) work(ctx context.Context, key backgroundKey, run *backgroundRun, work rocketcode.BackgroundWork) {
	defer close(run.done)

	output, errWork := work.RunBackground(ctx)

	r.mu.Lock()
	delete(r.runs, key)

	run.output, run.err = output, errWork
	attached := run.attached
	flipped := attached && !r.attachedLocked(key.conversationID)
	r.mu.Unlock()

	if flipped {
		r.movableChanged(ctx, key.conversationID)
	}

	cause := context.Cause(ctx)
	if attached || errors.Is(cause, errBackgroundAbandoned) { // An attached call has no row, and an abandoned job records nothing.
		return
	}

	<-run.recorded

	stop, stopped := errors.AsType[backgroundStopError](cause)

	end := *run.row
	end.status, end.noteState, end.wake, end.result = backgroundCompleted, notePending, true, output

	switch {
	case stop == errStoppedWithOwner:
		end.status, end.result, end.noteState, end.wake = backgroundStopped, stop.Error(), noteNone, false
	case stopped:
		end.status, end.result = backgroundStopped, stop.Error()
	case errWork != nil:
		end.status, end.result = backgroundFailed, errWork.Error()
	}

	finished, err := r.store.finishBackgroundJob(context.WithoutCancel(ctx), &end)
	for attempt := 1; err != nil && attempt < 3; attempt++ {
		time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)

		finished, err = r.store.finishBackgroundJob(context.WithoutCancel(ctx), &end)
	}

	if err != nil {
		r.log.Error("finish background job", "conversation_id", key.conversationID, "job_id", key.jobID, "error", err)
		return
	}

	if finished && end.noteState == notePending {
		r.notes.noteReady(key.conversationID)
	}
}

// recoverJobs settles, before any frontend can start a turn, the jobs a stopped process left running.
// A script is killed, and its note waits for its owner's next turn or, in a hidden run, wakes the run.
// A task waits again for its subagent session's turn, which resumes on that session's bridge. Its run
// holds its row at once but waits only once resume is called, which also wakes the owners of notes.
func (r *backgroundRegistry) recoverJobs(ctx context.Context) (resume func(context.Context) error, err error) {
	if err := r.store.releaseStaleBackgroundClaims(ctx); err != nil {
		return nil, err
	}

	jobs, err := r.store.runningBackgroundJobs(ctx)
	if err != nil {
		return nil, err
	}

	var resumed []func()

	for i := range jobs {
		job := &jobs[i]

		if job.kind != backgroundTask {
			end := *job
			end.status, end.noteState, end.wake = backgroundKilled, notePending, hiddenRun(job.origin)

			if _, err := r.store.finishBackgroundJob(ctx, &end); err != nil {
				return nil, err
			}

			continue
		}

		restamped, err := r.store.restampBackgroundJob(ctx, job, r.runnerID)
		if err != nil {
			return nil, err
		}

		if !restamped {
			continue
		}

		job.runnerID = r.runnerID
		workCtx, cancel := context.WithCancelCause(context.Background())
		key := backgroundKey{job.conversationID, job.jobID}
		run := &backgroundRun{row: job, cancel: cancel, recorded: make(chan struct{}), done: make(chan struct{})}
		close(run.recorded)

		r.mu.Lock()
		r.runs[key] = run
		r.mu.Unlock()

		resumed = append(resumed, func() { r.work(workCtx, key, run, subagentTurn{notes: r.notes, job: job}) })
	}

	return func(ctx context.Context) error {
		for _, work := range resumed {
			r.group.Go(func() error {
				work()
				return nil
			})
		}

		owners, err := queryStrings(ctx, r.store.db, `SELECT DISTINCT conversation_id FROM background_jobs WHERE note_state = $1 AND wake AND claimed_turn_id = '' ORDER BY 1`, "background wake owners", notePending)
		if err != nil {
			return err
		}

		for _, owner := range owners {
			r.notes.noteReady(owner)
		}

		return nil
	}, nil
}

// subagentTurn is a recovered task job's work: it waits again for its subagent session's turn.
type subagentTurn struct {
	notes backgroundNotes
	job   *backgroundJob
}

func (w subagentTurn) RunBackground(ctx context.Context) (string, error) {
	return w.notes.continueSubagent(ctx, w.job)
}

func (subagentTurn) Detach() bool { return true }

// subagentTurnSQL finds, in session $1, the turn running its input $2: saved with its entries, ended
// stopped or failed, running, or not yet started. It also returns the session's parent and the input of
// the turn the session runs.
const subagentTurnSQL = `WITH saved AS (
    SELECT e.entry_json FROM session_entries e
    CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
    WHERE e.conversation_id = $1 AND item->>'input_id' = $2 ORDER BY e.id DESC LIMIT 1
), turns AS (
    SELECT terminal, COALESCE(outbound_json->>'Text', '') AS text FROM active_turns
    WHERE conversation_id = $1 AND inbound_json->'Metadata'->>'web_message_id' = $2 ORDER BY created_at_unix_ns DESC LIMIT 1
), queued AS (
    SELECT count(*) AS rows FROM thread_queue WHERE conversation_id = $1 AND queue_item_id = $2
), running AS (
    SELECT c.parent_conversation_id AS parent, a.inbound_json->'Metadata'->>'web_message_id' AS input
    FROM active_turns a JOIN managed_conversations c USING (conversation_id) WHERE a.conversation_id = $1 AND a.phase = $3
)
SELECT s.entry_json, t.terminal, t.text, q.rows, r.parent, r.input FROM queued q LEFT JOIN saved s ON true LEFT JOIN turns t ON true LEFT JOIN running r ON true`

// awaitSubagent waits until the turn of the subagent session conversationID that runs its input inputID
// ends, and returns that turn's final answer, as a top-level turn's (runTurn), and attribution. A call that
// stops waiting, other than for a shutdown, stops that turn once it runs. A stopped parent's call, unlike a
// stopped job's, also stops the turn running ahead of its input, unless a background job waits for that
// turn (OpenCode packages/core/src/tool/plugin/subagent.ts). Its input is not withdrawn but held, and it
// leaves no note.
func (r *backgroundRegistry) awaitSubagent(ctx context.Context, conversationID, inputID string) (string, rocketcode.ReplayAttribution, error) {
	for {
		r.mu.Lock()
		finished := r.finished
		r.mu.Unlock()

		var (
			saved, terminal, text, parent, running sql.NullString
			queued                                 int
		)
		if err := r.store.db.QueryRowContext(context.WithoutCancel(ctx), subagentTurnSQL, conversationID, inputID, turnRunning).Scan(&saved, &terminal, &text, &queued, &parent, &running); err != nil {
			return "", rocketcode.ReplayAttribution{}, fmt.Errorf("read subagent turn: %w", err)
		}

		switch {
		case saved.Valid:
			var entry rocketcode.SessionEntry
			if err := json.Unmarshal([]byte(saved.String), &entry); err != nil {
				return "", rocketcode.ReplayAttribution{}, fmt.Errorf("decode subagent turn: %w", err)
			}

			items, err := rocketcode.ReplayInputToParams(entry.ReplayInput)
			if err != nil {
				return "", rocketcode.ReplayAttribution{}, fmt.Errorf("decode subagent turn: %w", err)
			}

			answer := ""

			for i := range items {
				role, text, ok, err := ReplayInputMessageRoleText(&items[i], entry.ReplayInput[i])
				if err != nil {
					return "", rocketcode.ReplayAttribution{}, err
				}

				if commentary := items[i].OfMessage != nil && items[i].OfMessage.Phase == "commentary" || items[i].OfOutputMessage != nil && items[i].OfOutputMessage.Phase == "commentary"; ok && role == "assistant" && !commentary {
					answer = appendText(answer, text)
				}
			}

			return answer, rocketcode.ReplayAttribution{Agent: entry.Agent, Model: entry.Model, ReasoningEffort: entry.ReasoningEffort}, nil
		case terminal.String != "":
			return "", rocketcode.ReplayAttribution{}, fmt.Errorf("subagent turn %s: %s", terminal.String, text.String)
		case !terminal.Valid && queued == 0:
			return "", rocketcode.ReplayAttribution{}, fmt.Errorf("the prompt to subagent %q was removed before it ran", conversationID)
		}

		select {
		case <-finished:
		case <-ctx.Done():
			cause := context.Cause(ctx)
			_, jobStopped := errors.AsType[backgroundStopError](cause)
			stopped := !errors.Is(cause, rocketcode.ErrShutdown) && !errors.Is(cause, errBackgroundAbandoned)

			// A stopped parent's queued input is held, before its turn ahead stops, until the next write
			// releases it (releaseHeldInputs). Unlike a stash's, its inbound stays an enqueue.
			if stopped && !jobStopped {
				if _, err := execState(context.WithoutCancel(ctx), r.store.db, "hold subagent input", `UPDATE thread_queue SET kind = $3 WHERE conversation_id = $1 AND queue_item_id = $2 AND kind = $4`, conversationID, inputID, protocol.InboundKindHeld, protocol.InboundKindEnqueue); err != nil {
					return "", rocketcode.ReplayAttribution{}, err
				}
			}

			ahead := !jobStopped && !r.detached(backgroundKey{parent.String, strings.TrimPrefix(running.String, parent.String+"/")})
			if stopped && (terminal.Valid || ahead) {
				r.notes.InterruptConversation(conversationID)
			}

			return "", rocketcode.ReplayAttribution{}, fmt.Errorf("wait for subagent %q: %w", conversationID, cause)
		}
	}
}

// turnFinished wakes the task calls awaiting subagent turns once a turn finished.
func (r *backgroundRegistry) turnFinished() {
	r.mu.Lock()
	close(r.finished)
	r.finished = make(chan struct{})
	r.mu.Unlock()
}

// move moves every attached job of conversationID into the background and returns them in job
// ID order. A job whose work already finished is no longer listed, so its call keeps its real result.
func (r *backgroundRegistry) move(conversationID string) []*backgroundRun {
	r.mu.Lock()
	defer r.mu.Unlock()

	var moved []*backgroundRun

	for key, run := range r.runs {
		if key.conversationID == conversationID && run.attached {
			run.attached = false
			run.unlink()
			run.markMoved(errMovedToBackground)

			moved = append(moved, run)
		}
	}

	slices.SortFunc(moved, func(a, b *backgroundRun) int { return strings.Compare(a.row.jobID, b.row.jobID) })

	return moved
}

func (r *backgroundRegistry) movable(conversationID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.attachedLocked(conversationID)
}

// detached reports whether the job at key runs in the background: started there, moved, or recovered.
func (r *backgroundRegistry) detached(key backgroundKey) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	run, ok := r.runs[key]

	return ok && !run.attached
}

// attachedLocked reports whether conversationID has an attached job. Callers hold r.mu.
func (r *backgroundRegistry) attachedLocked(conversationID string) bool {
	for key, run := range r.runs {
		if key.conversationID == conversationID && run.attached {
			return true
		}
	}

	return false
}

// movableChanged wakes conversationID's Web views when movable flips, which no row change reports.
func (r *backgroundRegistry) movableChanged(ctx context.Context, conversationID string) {
	if _, err := r.store.db.ExecContext(context.WithoutCancel(ctx), `SELECT pg_notify('rocketclaw_transcript_' || md5(current_schema()), json_build_object('conversationId', $1::text, 'revision', pg_current_xact_id()::text)::text)`, conversationID); err != nil {
		r.log.Error("notify movable change", "conversation_id", conversationID, "error", err)
	}
}

// stop stops a running job listed at conversationID, its own or one of a hidden run whose destination
// it is, and waits until its stopped note is recorded. It reports false when the job had already finished.
func (r *backgroundRegistry) stop(ctx context.Context, conversationID, jobID string, reason backgroundStopError) (bool, error) {
	jobs, err := r.store.backgroundJobs(ctx, conversationID)
	if err != nil {
		return false, err
	}

	i := slices.IndexFunc(jobs, func(job backgroundJob) bool { return job.jobID == jobID })
	if i < 0 {
		return false, fmt.Errorf("background job %q is neither running nor waiting to report in this conversation", jobID)
	}

	r.mu.Lock()
	run, running := r.runs[backgroundKey{jobs[i].conversationID, jobID}]

	// A stopped subagent's own detached jobs stop with it, so none of them wakes it again.
	for key, owned := range r.runs {
		if subagent := jobs[i].conversationID + jobs[i].subagentKey; running && jobs[i].subagentKey != "" && !owned.attached && (key.conversationID == subagent || strings.HasPrefix(key.conversationID, subagent+"/")) {
			owned.cancel(errStoppedWithOwner)
		}
	}
	r.mu.Unlock()

	if !running {
		return false, nil
	}

	run.cancel(reason)

	select {
	case <-run.done:
		return true, nil
	case <-ctx.Done():
		return false, fmt.Errorf("wait for stopped job: %w", context.Cause(ctx))
	}
}

// abandon kills conversationID's detached jobs without recording them and waits for them.
func (r *backgroundRegistry) abandon(conversationID string) {
	r.mu.Lock()

	var runs []*backgroundRun

	for key, run := range r.runs {
		if key.conversationID == conversationID && !run.attached {
			run.cancel(errBackgroundAbandoned)
			runs = append(runs, run)
		}
	}
	r.mu.Unlock()

	for _, run := range runs {
		<-run.done
	}
}

// shutdown kills the detached jobs and waits for every job; attached calls follow their turns.
func (r *backgroundRegistry) shutdown() {
	r.mu.Lock()
	for _, run := range r.runs {
		if !run.attached {
			run.cancel(errBackgroundAbandoned)
		}
	}
	r.mu.Unlock()

	_ = r.group.Wait() // Jobs return no errors.
}
