package backend

import (
	"context"
	"crypto/rand"
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

// backgroundNotes delivers Completion Notes outside the registry: a main agent's through its
// conversation's bridge, and a sleeping subagent's as a new turn of that subagent.
type backgroundNotes interface {
	// noteReady learns that conversationID has a new Completion Note for its main agent.
	noteReady(conversationID string)
	// continueSubagent runs input as a new turn of the subagent a subagent_wake job runs; without
	// input it resumes the turn a task job's subagent journaled before a restart.
	continueSubagent(ctx context.Context, job *backgroundJob, input string) (string, error)
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
	header := provenanceHeader(promptProvenance{origin: "System"})
	return rocketcode.PromptInput{ID: id, Text: header + "\n\n" + text, Header: header}
}

// conversationJobs kills a conversation's running jobs before its rows are deleted.
type conversationJobs interface {
	abandon(conversationID string)
}

// noConversationJobs is the session service's conversationJobs before a run attaches its registry.
type noConversationJobs struct{}

func (noConversationJobs) abandon(string) {}

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

	mu      sync.Mutex
	runs    map[backgroundKey]*backgroundRun
	stopped bool // Set by shutdown; no subagent wakes after it.
}

func newBackgroundRegistry(store *SessionService, notes backgroundNotes, log *slog.Logger) *backgroundRegistry {
	return &backgroundRegistry{store: store, notes: notes, log: log, runnerID: rand.Text(), runs: map[backgroundKey]*backgroundRun{}}
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
	// turnID is the turn's active-turn row; it claims the notes of the subagents it runs attached.
	turnID string
}

func (backgroundTurn) Enabled() bool { return true }

// DrainNotes claims the Completion Notes waiting for the running subagent at subagentKey for the
// nearest detached job among it and its ancestors, which outlives the subagent's turn, or else for the turn.
func (t backgroundTurn) DrainNotes(ctx context.Context, subagentKey string) []rocketcode.PromptInput {
	claim, nearest := t.turnID, ""

	t.registry.mu.Lock()
	for key, run := range t.registry.runs {
		owner := run.row.subagentKey
		if key.conversationID == t.conversationID && !run.attached && owner != "" && len(owner) > len(nearest) && (owner == subagentKey || strings.HasPrefix(subagentKey, owner+"/")) {
			claim, nearest = key.jobID, owner
		}
	}
	t.registry.mu.Unlock()

	notes, err := claimBackgroundNotes(context.WithoutCancel(ctx), t.registry.store.db, t.conversationID, subagentKey, claim, false)
	if err != nil {
		t.registry.log.Error("claim subagent notes", "conversation_id", t.conversationID, "subagent_key", subagentKey, "error", err)
	}

	inputs := make([]rocketcode.PromptInput, len(notes))
	for i := range notes {
		inputs[i] = systemPromptInput(notes[i].jobID, backgroundNote(&notes[i]))
	}

	return inputs
}

// Run runs work on a context detached from ctx. Until the job moves, ctx's cancellation
// reaches the work with its cause, and Run waits for the work to finish.
func (t backgroundTurn) Run(ctx context.Context, job *rocketcode.BackgroundJob, work rocketcode.BackgroundWork) (rocketcode.BackgroundResult, error) {
	r, key := t.registry, backgroundKey{t.conversationID, job.ID}

	exists, err := r.store.hasBackgroundJob(ctx, key.conversationID, key.jobID)
	if err != nil {
		return rocketcode.BackgroundResult{}, err
	}

	if exists { // A resumed turn's call whose job already left it.
		work.Detach()
		return rocketcode.BackgroundResult{Moved: true}, nil
	}

	r.mu.Lock()
	for _, other := range r.runs {
		if job.Kind == rocketcode.BackgroundKindTask && other.row.conversationID == key.conversationID && other.row.subagentKey == job.SubagentKey {
			// Only a background task reports with a note; any other run is a turn to wait out.
			busy := "subagent %s is busy with another turn; continue it again later"
			if other.row.kind == backgroundTask && !other.attached {
				busy = "subagent %s is still running; wait for its note before continuing it"
			}

			r.mu.Unlock()

			return rocketcode.BackgroundResult{}, fmt.Errorf(busy, job.SubagentKey)
		}
	}

	workCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	run := &backgroundRun{row: &backgroundJob{conversationID: key.conversationID, jobID: job.ID, childKey: job.ChildKey, kind: backgroundJobKind(job.Kind), agent: job.Agent, label: job.Label, callID: job.CallID, subagentKey: job.SubagentKey, origin: t.origin, runnerID: r.runnerID}, cancel: cancel, recorded: make(chan struct{}), done: make(chan struct{}), attached: !job.Detached}
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

	if run.err != nil {
		return rocketcode.BackgroundResult{}, fmt.Errorf("run background work: %w", run.err)
	}

	return rocketcode.BackgroundResult{Output: run.output}, nil
}

// work runs one job. A job that ended detached records its final state with a pending,
// wakeable Completion Note for its owner, unless it was abandoned; a subagent wake records
// none. A subagent that stops running is woken for the notes that reached it after its last step.
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
	if errors.Is(cause, errBackgroundAbandoned) {
		return
	}

	stop, stopped := errors.AsType[backgroundStopError](cause)
	if run.row.kind != backgroundExecute && !stopped {
		defer r.wakeSubagent(key.conversationID, run.row.subagentKey)
	}

	if attached {
		return
	}

	<-run.recorded

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

	// Ponytail: a failed write leaves the job running without its note, so it is retried briefly;
	// an outage outlasting the retries leaves the job to the next start's recovery.
	finished, err := r.store.finishBackgroundJob(context.WithoutCancel(ctx), &end)
	for attempt := 1; err != nil && attempt < 3; attempt++ {
		time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)

		finished, err = r.store.finishBackgroundJob(context.WithoutCancel(ctx), &end)
	}

	if err != nil {
		r.log.Error("finish background job", "conversation_id", key.conversationID, "job_id", key.jobID, "error", err)
		return
	}

	if finished && end.noteState != noteNone {
		r.wakeOwner(key.conversationID, end.childKey)
	}
}

// wakeOwner delivers the pending notes of the owner at childKey: its conversation's bridge takes a
// main agent's, and a subagent_wake job a subagent's.
func (r *backgroundRegistry) wakeOwner(conversationID, childKey string) {
	if childKey == "" {
		r.notes.noteReady(conversationID)
		return
	}

	r.wakeSubagent(conversationID, childKey)
}

// recoverJobs settles, before any frontend can start a turn, the jobs a stopped process left running.
// A script is killed, and its note waits for its owner's next turn or, in a hidden run, wakes the run.
// A task's subagent resumes its journaled turn; a subagent wake ends and its note wakes the subagent again.
// Resuming subagents hold their keys at once but run, like the wakes, only once resume is called.
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

		owners, err := r.store.backgroundWakeOwners(ctx)
		if err != nil {
			return err
		}

		for i := range owners {
			r.wakeOwner(owners[i].conversationID, owners[i].childKey)
		}

		return nil
	}, nil
}

// subagentTurn is a subagent job's work: a wake's new turn of its subagent with the notes it
// claimed, or, without input, the resumed turn of a task's subagent.
type subagentTurn struct {
	notes backgroundNotes
	job   *backgroundJob
	input string
}

func (w subagentTurn) RunBackground(ctx context.Context) (string, error) {
	return w.notes.continueSubagent(ctx, w.job, w.input)
}

func (subagentTurn) Detach() bool { return true }

// wakeSubagent delivers the pending notes of the subagent at subagentKey as a subagent_wake job that
// runs one new turn of it, unless the subagent runs: then its next step takes them, or its
// end wakes it again. The turn's output stays in the subagent's history; its parent is not told.
func (r *backgroundRegistry) wakeSubagent(conversationID, subagentKey string) {
	// Only a pending note may hold the subagent busy for a following task continue.
	if _, wake, err := r.store.backgroundWake(context.Background(), conversationID, subagentKey); err != nil || !wake {
		if err != nil {
			r.log.Error("look up subagent notes", "conversation_id", conversationID, "subagent_key", subagentKey, "error", err)
		}

		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.stopped {
		return
	}

	for key, run := range r.runs {
		if key.conversationID == conversationID && run.row.subagentKey == subagentKey {
			return
		}
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	key := backgroundKey{conversationID, subagentKey + "/wake/" + rand.Text()}
	run := &backgroundRun{row: &backgroundJob{conversationID: conversationID, jobID: key.jobID, childKey: subagentKey, kind: backgroundSubagentWake, subagentKey: subagentKey, runnerID: r.runnerID}, cancel: cancel, recorded: make(chan struct{}), done: make(chan struct{})}
	r.runs[key] = run

	r.group.Go(func() error {
		notes, err := r.store.startSubagentWake(ctx, run.row)
		if err == nil && len(notes) > 0 {
			close(run.recorded)
			r.work(ctx, key, run, subagentTurn{notes: r.notes, job: run.row, input: systemPromptInput("", backgroundNotesText(notes)).Text})

			return nil
		}

		if err != nil {
			r.log.Error("start subagent wake", "conversation_id", conversationID, "subagent_key", subagentKey, "error", err)
		}

		r.mu.Lock()
		delete(r.runs, key)
		r.mu.Unlock()
		close(run.done)

		return nil
	})
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

// stop stops a running job listed at conversationID, its own or one of a hidden run whose
// destination it is, and waits until its stopped note is recorded. It reports false when
// the job had already finished.
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
		if running && run.row.subagentKey != "" && key.conversationID == jobs[i].conversationID && !owned.attached && (owned.row.childKey == run.row.subagentKey || strings.HasPrefix(owned.row.childKey, run.row.subagentKey+"/")) {
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

	r.stopped = true
	for _, run := range r.runs {
		if !run.attached {
			run.cancel(errBackgroundAbandoned)
		}
	}
	r.mu.Unlock()

	_ = r.group.Wait() // Jobs return no errors.
}
