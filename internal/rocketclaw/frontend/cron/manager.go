// Package cronfrontend schedules cron producers and their conversation Sync.
package cronfrontend

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

// Runner executes a producer through terminal delivery.
type Runner interface {
	Run(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error)
}

// Manager loads and runs workspace cron definitions.
type Manager struct {
	cfg                   *config.LockedConfig
	workspace, runtimeDir string
	store                 cronScheduleStore
	run                   Runner
	log                   *slog.Logger
	now                   func() time.Time
	tickerInterval        time.Duration

	mu     sync.Mutex
	stop   context.CancelFunc
	closed bool
	// armed holds the schedule IDs of the one-offs whose timers started, so each starts once.
	armed map[string]bool
	wg    sync.WaitGroup
}

type cronScheduleStore interface {
	ResetCronSchedules() error
	SyncCronSchedules([]backend.CronScheduleState) error
	DueCronSchedules(time.Time) ([]backend.CronScheduleState, error)
	ClaimCronSchedule(backend.CronScheduleState, time.Time, time.Time) (bool, error)
	CronRunRecorded(string) (bool, error)
}

type definition struct {
	relativePath, agent, body, space string
	schedules                        []config.CronSchedule
}

// Job is the read-only presentation of a loaded definition and its next triggers.
type Job struct {
	RelativePath, Agent, Body, Space string
	Schedules                        []string
	Upcoming                         []time.Time
}

const (
	cronTracePrefix       = "cron:"
	oneOffCronTracePrefix = "one-off-cron:"
)

// New constructs a cronjob manager using runtimeDir for effective runtime cron definitions and agents' routines.
func New(cfg *config.LockedConfig, runtimeDir string, store cronScheduleStore, run Runner, logger *slog.Logger) *Manager {
	return &Manager{cfg: cfg, workspace: cfg.Clone().Workspace, store: store, run: run, log: logger.With("component", "cronjob"), now: time.Now, tickerInterval: time.Minute, runtimeDir: runtimeDir, armed: map[string]bool{}}
}

// ValidateRuntimeDefinitions loads cron definitions and routines from runtimeDir without mutating scheduler state.
func ValidateRuntimeDefinitions(cfg *config.LockedConfig, runtimeDir string) error {
	_, err := loadDefinitionsIn(cfg, runtimeDir)

	return err
}

// Start loads cron definitions and starts scheduling them.
func (m *Manager) Start(ctx context.Context) error {
	definitions, err := loadDefinitionsIn(m.cfg, m.runtimeDir)
	if err != nil {
		return err
	}

	now := m.now()
	if err := m.store.ResetCronSchedules(); err != nil {
		return fmt.Errorf("reset cron schedules: %w", err)
	}

	if err := m.store.SyncCronSchedules(m.scheduledStates(definitions, now)); err != nil {
		return fmt.Errorf("sync cron schedules: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.stop = cancel

	m.armOneOffs(runCtx, definitions, now)

	m.wg.Add(1)
	go m.runTickerLoop(runCtx)

	m.log.Info("loaded cronjobs", "count", len(definitions))

	for _, definition := range definitions {
		for range definition.schedules {
			m.log.Info(
				"loaded cronjob schedule",
				"file", definition.relativePath,
				"agent", definition.agent,
			)
		}
	}

	return nil
}

// Stop shuts the cron manager down.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	stop := m.stop
	m.mu.Unlock()

	stop()

	done := make(chan struct{})

	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stop cron jobs: %w", ctx.Err())
	}
}

// Stem is the name the cron job at path is listed and loaded by: a cron file's stem, or
// "<agent> · <schedule>" for the routine at agents/<agent>.md#<schedule>.
func Stem(path string) string {
	if agent, schedule, ok := strings.Cut(strings.TrimPrefix(path, "agents/"), ".md#"); ok {
		return agent + " · " + schedule
	}

	return strings.TrimSuffix(strings.TrimPrefix(path, "cron/"), ".md")
}

// LoadOneOffCronjob resolves and loads one live cronjob, a cron file or a routine, for a run from Web.
func (m *Manager) LoadOneOffCronjob(target string) (protocol.OneOffCronjob, error) {
	if strings.Contains(target, " · ") {
		definitions, err := loadDefinitionsIn(m.cfg, m.runtimeDir)
		if err != nil {
			return protocol.OneOffCronjob{}, fmt.Errorf("load one-off cronjob: %w", err)
		}

		i := slices.IndexFunc(definitions, func(d definition) bool { return Stem(d.relativePath) == target })
		if i < 0 {
			return protocol.OneOffCronjob{}, fmt.Errorf("load one-off cronjob: no routine %q", target)
		}

		return protocol.OneOffCronjob{RelativePath: definitions[i].relativePath, Agent: definitions[i].agent, Space: definitions[i].space, Prompt: m.preparePrompt(definitions[i].body)}, nil
	}

	job, err := config.LoadOneOffCronjob(m.workspace, m.runtimeDir, target)
	if err != nil {
		return protocol.OneOffCronjob{}, fmt.Errorf("load one-off cronjob: %w", err)
	}

	job.Prompt = m.preparePrompt(job.Prompt)

	return job, nil
}

// Jobs returns loaded definitions and their next triggers in the next day.
// It neither refreshes scheduler state nor claims any work.
func (m *Manager) Jobs() ([]Job, error) {
	definitions, err := loadDefinitionsIn(m.cfg, m.runtimeDir)
	if err != nil {
		return nil, err
	}

	now := m.now()

	triggers, err := m.store.DueCronSchedules(now.Add(24 * time.Hour))
	if err != nil {
		return nil, fmt.Errorf("read cron next triggers: %w", err)
	}

	jobs := make([]Job, 0, len(definitions))
	for _, definition := range definitions {
		job := Job{RelativePath: definition.relativePath, Agent: definition.agent, Body: definition.body, Space: definition.space}
		for i, schedule := range definition.schedules {
			job.Schedules = append(job.Schedules, schedule.Raw)
			if !schedule.DueAt.IsZero() && !schedule.DueAt.Before(now) && !schedule.DueAt.After(now.Add(24*time.Hour)) {
				job.Upcoming = append(job.Upcoming, schedule.DueAt)
			}

			for _, trigger := range triggers {
				if trigger.ScheduleID == scheduleID(definition.relativePath, i, schedule.Raw) && !trigger.NextDue.Before(now) {
					job.Upcoming = append(job.Upcoming, trigger.NextDue)
				}
			}
		}

		slices.SortFunc(job.Upcoming, time.Time.Compare)
		jobs = append(jobs, job)
	}

	return jobs, nil
}

// RunOneOffCronjob waits for a loaded producer and its required Sync.
func (m *Manager) RunOneOffCronjob(ctx context.Context, job *protocol.OneOffCronjob) (protocol.CronRunResult, error) {
	raw := new(backend.RawRunProgress)

	raw.Run = &protocol.CronRun{Path: job.RelativePath, At: m.now(), OneOff: true}
	raw.ConversationID = cronTraceConversationID(oneOffCronTracePrefix, job.RelativePath, raw.Run.At)
	raw.SyncDestination = job.ConversationID
	raw.Cronjob = &protocol.CronjobMessage{RelativePath: job.RelativePath, Agent: job.Agent, RanAt: m.now().Format(time.RFC3339)}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return protocol.CronRunResult{}, errors.New("cronjob manager is stopped")
	}

	m.wg.Add(1)

	m.mu.Unlock()
	defer m.wg.Done()

	startedAt := time.Now()
	result, err := m.run.Run(context.WithoutCancel(ctx), job.Agent, job.Prompt, raw)

	outcome := "completed"
	if err != nil {
		outcome = "failed"
	} else if result.ConversationID == "" {
		outcome = "intentional_silence"
	}

	m.log.Info("one-off cronjob returned", "event", "cron_completed", "outcome", outcome, "conversation_id", raw.ConversationID, "destination_conversation_id", result.ConversationID, "duration_ms", time.Since(startedAt).Milliseconds(), "error_type", fmt.Sprintf("%T", err))

	if err != nil {
		return result, fmt.Errorf("run one-off cronjob: %w", err)
	}

	return result, nil
}

// armOneOffs starts the timer of each one-off in definitions not armed before. m.mu must be held.
func (m *Manager) armOneOffs(ctx context.Context, definitions []definition, now time.Time) {
	for _, definition := range definitions {
		if len(definition.schedules) != 1 || definition.schedules[0].DueAt.IsZero() {
			continue
		}

		id := scheduleID(definition.relativePath, 0, definition.schedules[0].Raw)
		if m.armed[id] {
			continue
		}

		m.armed[id] = true

		m.wg.Add(1)
		go m.runOneOffTimer(ctx, id, max(definition.schedules[0].DueAt.Sub(now), 0))
	}
}

func (m *Manager) runOneOffTimer(ctx context.Context, id string, interval time.Duration) {
	defer m.wg.Done()

	timer := time.NewTimer(interval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}

	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()

	if closed {
		return
	}

	// Only a one-off still defined runs: an edited time or a removed one-off leaves this timer stale. One that
	// fails to load arms again at the next scan that loads it, which reports the error.
	definitions, _ := loadDefinitionsIn(m.cfg, m.runtimeDir)

	i := slices.IndexFunc(definitions, func(d definition) bool {
		return len(d.schedules) == 1 && scheduleID(d.relativePath, 0, d.schedules[0].Raw) == id
	})
	if i < 0 {
		m.mu.Lock()
		delete(m.armed, id)
		m.mu.Unlock()

		return
	}

	definition := &definitions[i]

	// A routine lives in its agent's file, which is never deleted: one with a scheduled run recorded is done.
	routine := strings.HasPrefix(definition.relativePath, "agents/")
	if routine {
		ran, err := m.store.CronRunRecorded(definition.relativePath)
		if err != nil {
			m.log.Error("read one-off routine runs", "file", definition.relativePath, "error", err)
			return
		}

		if ran {
			return
		}
	}

	m.executeJob(ctx, definition)

	if routine {
		return
	}

	relativePath := filepath.ToSlash(filepath.Join(m.runtimeDir, "cron", filepath.Base(definition.relativePath)))
	if err := os.Remove(filepath.Join(m.workspace, relativePath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.log.Warn("delete one-off cronjob", "file", definition.relativePath, "error", err)
	}

	if m.runtimeDir != "." {
		if err := os.Remove(filepath.Join(m.workspace, definition.relativePath)); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.log.Warn("delete local one-off cronjob", "file", definition.relativePath, "error", err)
		}
	}
}

func (m *Manager) runTickerLoop(ctx context.Context) {
	defer m.wg.Done()

	ticker := time.NewTicker(m.tickerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if err := m.scanScheduled(ctx); err != nil {
			if ctx.Err() == nil {
				m.log.Error("scan scheduled cronjobs", "error", err)
			}
		}
	}
}

func (m *Manager) scanScheduled(ctx context.Context) error {
	now := m.now()

	definitions, err := loadDefinitionsIn(m.cfg, m.runtimeDir)
	if err != nil {
		return err
	}

	states := m.scheduledStates(definitions, now)
	if err := m.store.SyncCronSchedules(states); err != nil {
		return fmt.Errorf("sync cron schedules: %w", err)
	}

	m.mu.Lock()

	closed := m.closed
	if !closed {
		m.armOneOffs(ctx, definitions, now)
	}

	m.mu.Unlock()

	if closed {
		return nil
	}

	due, err := m.store.DueCronSchedules(now)
	if err != nil {
		return fmt.Errorf("load due cron schedules: %w", err)
	}

	if len(due) == 0 {
		return nil
	}

	statesByID := map[string]backend.CronScheduleState{}
	for _, state := range states {
		statesByID[state.ScheduleID] = state
	}

	definitionsByPath := map[string]definition{}
	for _, definition := range definitions {
		definitionsByPath[definition.relativePath] = definition
	}

	for _, state := range due {
		scheduled, ok := statesByID[state.ScheduleID]
		if !ok {
			continue
		}

		definition, ok := definitionsByPath[scheduled.RelativePath]
		if !ok {
			continue
		}

		ok, err := m.store.ClaimCronSchedule(state, scheduled.NextDue, now)
		if err != nil {
			return fmt.Errorf("claim cron schedule: %w", err)
		}

		if !ok {
			continue
		}

		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			continue
		}

		m.wg.Add(1)
		m.mu.Unlock()

		delete(definitionsByPath, definition.relativePath)

		go func() {
			defer m.wg.Done()

			m.executeJob(ctx, &definition)
		}()
	}

	return nil
}

func (m *Manager) scheduledStates(definitions []definition, now time.Time) []backend.CronScheduleState {
	var states []backend.CronScheduleState

	for _, definition := range definitions {
		for index, schedule := range definition.schedules {
			if !schedule.DueAt.IsZero() {
				continue
			}

			states = append(states, backend.CronScheduleState{
				ScheduleID:   scheduleID(definition.relativePath, index, schedule.Raw),
				RelativePath: definition.relativePath,
				NextDue:      schedule.Next(now),
			})
		}
	}

	return states
}

const cronReplyInstruction = "Your reply is posted as a report in a new Web chat exactly as written. If there is nothing worth reporting, reply with nothing."

func (m *Manager) executeJob(ctx context.Context, definition *definition) {
	startedAt := m.now()
	ranAt := startedAt.Format(time.RFC3339)
	prompt := m.preparePrompt(definition.body)

	log := m.log.With("file", definition.relativePath, "agent", definition.agent, "ran_at", ranAt)

	progress := &backend.RawRunProgress{
		ConversationID: cronTraceConversationID(cronTracePrefix, definition.relativePath, startedAt),
		Run:            &protocol.CronRun{Path: definition.relativePath, At: startedAt},
		Cronjob:        &protocol.CronjobMessage{RelativePath: definition.relativePath, Agent: definition.agent, RanAt: ranAt, Space: definition.space},
	}
	log = log.With("conversation_id", progress.ConversationID)
	log.Info("starting cronjob", "event", "cron_started", "prompt_len", len(prompt))

	started := time.Now()
	result, err := m.run.Run(context.WithoutCancel(ctx), definition.agent, prompt, progress)

	outcome := "completed"
	if err != nil {
		outcome = "failed"
	} else if result.ConversationID == "" {
		outcome = "intentional_silence"
	}

	log.Info("completed cronjob", "event", "cron_completed", "outcome", outcome, "destination_conversation_id", result.ConversationID, "duration_ms", time.Since(started).Milliseconds(), "error_type", fmt.Sprintf("%T", err))

	if err != nil && ctx.Err() == nil {
		log.Error("cronjob failed", "human_visible", false, "error_type", fmt.Sprintf("%T", err))
	}
}

func cronTraceConversationID(prefix, relativePath string, ts time.Time) string {
	return prefix + relativePath + ":" + ts.UTC().Format("20060102T150405.000000000Z") + ":" + rand.Text()
}

func (m *Manager) preparePrompt(body string) string {
	if body == "" {
		return cronReplyInstruction
	}

	return strings.TrimSuffix(body, "\n") + "\n\n" + cronReplyInstruction
}

// loadDefinitionsIn loads every cron definition: one per top-level agent routine, at
// agents/<agent>.md#<schedule>, then the cron files, which may not target easy agents.
func loadDefinitionsIn(cfg *config.LockedConfig, runtimeDir string) ([]definition, error) {
	agents, _, err := backend.LoadRuntimeDefinitions(cfg, runtimeDir)
	if err != nil {
		return nil, fmt.Errorf("load agents: %w", err)
	}

	var definitions []definition

	for _, name := range agents.PrimaryNames() {
		agent := agents.Items[name]
		for _, routine := range agent.Routines {
			schedule, err := config.ParseCronSchedule(routine.Schedule)
			if err != nil {
				return nil, fmt.Errorf("agents/%s: routines: schedule %q: %w", agent.Location, routine.Schedule, err)
			}

			relativePath := "agents/" + agent.Location + "#" + schedule.Raw
			if slices.ContainsFunc(definitions, func(d definition) bool { return d.relativePath == relativePath }) {
				return nil, fmt.Errorf("agents/%s: routines: schedule %q is listed twice", agent.Location, schedule.Raw)
			}

			definitions = append(definitions, definition{relativePath: relativePath, agent: name, body: cmp.Or(routine.Instruction, "Scheduled run ("+schedule.Raw+")."), space: agent.Space, schedules: []config.CronSchedule{schedule}})
		}
	}

	root, err := os.OpenRoot(cfg.Clone().Workspace)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	cronPath := filepath.ToSlash(filepath.Join(runtimeDir, "cron"))

	cronRoot, err := root.OpenRoot(cronPath)
	if errors.Is(err, os.ErrNotExist) {
		return definitions, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read cronjob directory: %w", err)
	}

	defer func() { _ = cronRoot.Close() }()

	entries, err := fs.ReadDir(cronRoot.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("read cronjob directory: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".example.md") {
			continue
		}

		relativePath := filepath.ToSlash(filepath.Join("cron", name))

		data, err := cronRoot.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read cronjob %s: %w", relativePath, err)
		}

		definition, err := loadDefinition(data, relativePath)
		if err != nil {
			return nil, err
		}

		if agents.Items[definition.agent].Mode == harness.AgentModeEasy {
			return nil, fmt.Errorf("%s: agent %s is an easy agent; schedule it with its routines", relativePath, definition.agent)
		}

		definitions = append(definitions, definition)
	}

	return definitions, nil
}

func loadDefinition(data []byte, relativePath string) (definition, error) {
	job, schedules, err := config.ParseCronDefinition(data, relativePath)
	if err != nil {
		return definition{}, fmt.Errorf("load cron definition: %w", err)
	}

	return definition{
		relativePath: job.RelativePath,
		agent:        job.Agent,
		body:         job.Prompt,
		space:        job.Space,
		schedules:    schedules,
	}, nil
}

func scheduleID(relativePath string, index int, raw string) string {
	return relativePath + "#" + strconv.Itoa(index) + "#" + raw
}
