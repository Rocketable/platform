package routinefrontend

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type Runner interface {
	Run(context.Context, string, string, *backend.RoutineRunProgress) (protocol.RoutineRunResult, error)
}

type Manager struct {
	cfg            *config.LockedConfig
	runtimeDir     string
	store          routineScheduleStore
	run            Runner
	log            *slog.Logger
	now            func() time.Time
	tickerInterval time.Duration

	mu          sync.Mutex
	stop        context.CancelFunc
	closed      bool
	definitions []config.RoutineDefinition
	wg          sync.WaitGroup
}

type routineScheduleStore interface {
	ResetRoutineSchedules() error
	SyncRoutineSchedules([]backend.RoutineScheduleState) error
	ScheduleAgents([]string)
	DueRoutineSchedules(time.Time) ([]backend.RoutineScheduleState, error)
	ClaimRoutineSchedule(backend.RoutineScheduleState, time.Time, time.Time) (bool, error)
}

type Job struct {
	RelativePath, Agent, Body, Space, Schedule string
	Upcoming                                   []time.Time
}

const (
	routineTracePrefix       = "routine:"
	oneOffRoutineTracePrefix = "one-off-routine:"
)

func New(cfg *config.LockedConfig, runtimeDir string, store routineScheduleStore, run Runner, logger *slog.Logger) *Manager {
	return &Manager{cfg: cfg, store: store, run: run, log: logger.With("component", "routine"), now: time.Now, tickerInterval: time.Minute, runtimeDir: runtimeDir}
}

func (m *Manager) Start(ctx context.Context) error {
	definitions, err := loadDefinitionsIn(m.cfg, m.runtimeDir)
	if err != nil {
		return err
	}

	if err := m.store.ResetRoutineSchedules(); err != nil {
		return fmt.Errorf("reset routine schedules: %w", err)
	}

	if err := m.store.SyncRoutineSchedules(m.scheduledStates(definitions, m.now())); err != nil {
		return fmt.Errorf("sync routine schedules: %w", err)
	}

	m.store.ScheduleAgents(definitionAgents(definitions))

	runCtx, cancel := context.WithCancel(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.stop, m.definitions = cancel, definitions

	m.wg.Add(1)
	go m.runTickerLoop(runCtx)

	m.log.Info("loaded routines", "count", len(definitions))

	for _, definition := range definitions {
		m.log.Info("loaded routine schedule", "file", definition.Job.RelativePath, "agent", definition.Job.Agent)
	}

	return nil
}

func (m *Manager) Refresh() error {
	definitions, err := loadDefinitionsIn(m.cfg, m.runtimeDir)
	if err != nil {
		return err
	}

	if err := m.store.SyncRoutineSchedules(m.scheduledStates(definitions, m.now())); err != nil {
		return fmt.Errorf("sync routine schedules: %w", err)
	}

	m.store.ScheduleAgents(definitionAgents(definitions))

	m.mu.Lock()
	m.definitions = definitions
	m.mu.Unlock()

	return nil
}

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
		return fmt.Errorf("stop routines: %w", ctx.Err())
	}
}

func (m *Manager) LoadOneOffRoutine(target string) (protocol.OneOffRoutine, error) {
	definitions, err := loadDefinitionsIn(m.cfg, m.runtimeDir)
	if err != nil {
		return protocol.OneOffRoutine{}, fmt.Errorf("load one-off routine: %w", err)
	}

	i := slices.IndexFunc(definitions, func(d config.RoutineDefinition) bool { return backend.RoutineStem(d.Job.RelativePath) == target })
	if i < 0 {
		return protocol.OneOffRoutine{}, fmt.Errorf("load one-off routine: no routine %q", target)
	}

	job := definitions[i].Job
	job.Prompt = m.preparePrompt(job.Prompt)

	return job, nil
}

func (m *Manager) Jobs() ([]Job, error) {
	definitions, err := loadDefinitionsIn(m.cfg, m.runtimeDir)
	if err != nil {
		return nil, err
	}

	now := m.now()

	triggers, err := m.store.DueRoutineSchedules(now.Add(24 * time.Hour))
	if err != nil {
		return nil, fmt.Errorf("read routine next triggers: %w", err)
	}

	jobs := make([]Job, 0, len(definitions))
	for _, definition := range definitions {
		job := Job{RelativePath: definition.Job.RelativePath, Agent: definition.Job.Agent, Body: definition.Job.Prompt, Space: definition.Job.Space, Schedule: definition.Schedule.Raw}
		for _, trigger := range triggers {
			if trigger.ScheduleID == definition.Job.RelativePath && !trigger.NextDue.Before(now) {
				job.Upcoming = append(job.Upcoming, trigger.NextDue)
			}
		}

		jobs = append(jobs, job)
	}

	return jobs, nil
}

func (m *Manager) RunOneOffRoutine(ctx context.Context, job *protocol.OneOffRoutine) (protocol.RoutineRunResult, error) {
	progress := new(backend.RoutineRunProgress)

	progress.Run = &protocol.RoutineRun{Path: job.RelativePath, At: m.now(), OneOff: true}
	progress.ConversationID = routineTraceConversationID(oneOffRoutineTracePrefix, job.RelativePath, progress.Run.At)
	progress.SyncDestination = job.ConversationID
	progress.Routine = &protocol.RoutineMessage{Agent: job.Agent, Space: job.Space}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return protocol.RoutineRunResult{}, errors.New("routine manager is stopped")
	}

	m.wg.Add(1)

	m.mu.Unlock()
	defer m.wg.Done()

	startedAt := time.Now()
	result, err := m.run.Run(context.WithoutCancel(ctx), job.Agent, job.Prompt, progress)

	m.log.Info("one-off routine returned", "event", "routine_completed", "outcome", runOutcome(result, err), "conversation_id", progress.ConversationID, "destination_conversation_id", result.ConversationID, "duration_ms", time.Since(startedAt).Milliseconds(), "error_type", fmt.Sprintf("%T", err))

	if err != nil {
		return result, fmt.Errorf("run one-off routine: %w", err)
	}

	return result, nil
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
				m.log.Error("scan scheduled routines", "error", err)
			}
		}
	}
}

func (m *Manager) scanScheduled(ctx context.Context) error {
	now := m.now()

	m.mu.Lock()
	closed, definitions := m.closed, m.definitions
	m.mu.Unlock()

	if closed {
		return nil
	}

	due, err := m.store.DueRoutineSchedules(now)
	if err != nil {
		return fmt.Errorf("load due routine schedules: %w", err)
	}

	if len(due) == 0 {
		return nil
	}

	definitionsByPath := map[string]config.RoutineDefinition{}
	for _, definition := range definitions {
		definitionsByPath[definition.Job.RelativePath] = definition
	}

	for _, state := range due {
		definition, ok := definitionsByPath[state.ScheduleID]
		if !ok {
			continue
		}

		ok, err := m.store.ClaimRoutineSchedule(state, definition.Schedule.Next(now), now)
		if err != nil {
			return fmt.Errorf("claim routine schedule: %w", err)
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

		go func() {
			defer m.wg.Done()

			m.executeJob(ctx, &definition)
		}()
	}

	return nil
}

func definitionAgents(definitions []config.RoutineDefinition) []string {
	agents := make([]string, len(definitions))
	for i := range definitions {
		agents[i] = definitions[i].Job.Agent
	}

	return agents
}

func (m *Manager) scheduledStates(definitions []config.RoutineDefinition, now time.Time) []backend.RoutineScheduleState {
	states := make([]backend.RoutineScheduleState, len(definitions))
	for i, definition := range definitions {
		states[i] = backend.RoutineScheduleState{ScheduleID: definition.Job.RelativePath, RelativePath: definition.Job.RelativePath, NextDue: definition.Schedule.Next(now)}
	}

	return states
}

const routineReplyInstruction = "Your reply is posted as a report in a new Web chat exactly as written. If there is nothing worth reporting, reply with nothing."

func (m *Manager) executeJob(ctx context.Context, definition *config.RoutineDefinition) {
	startedAt := m.now()
	ranAt := startedAt.Format(time.RFC3339)
	prompt := m.preparePrompt(definition.Job.Prompt)

	log := m.log.With("file", definition.Job.RelativePath, "agent", definition.Job.Agent, "ran_at", ranAt)

	progress := &backend.RoutineRunProgress{
		ConversationID: routineTraceConversationID(routineTracePrefix, definition.Job.RelativePath, startedAt),
		Run:            &protocol.RoutineRun{Path: definition.Job.RelativePath, At: startedAt},
		Routine:        &protocol.RoutineMessage{Agent: definition.Job.Agent, Space: definition.Job.Space},
	}
	log = log.With("conversation_id", progress.ConversationID)
	log.Info("starting routine", "event", "routine_started", "prompt_len", len(prompt))

	started := time.Now()
	result, err := m.run.Run(context.WithoutCancel(ctx), definition.Job.Agent, prompt, progress)

	log.Info("completed routine", "event", "routine_completed", "outcome", runOutcome(result, err), "destination_conversation_id", result.ConversationID, "duration_ms", time.Since(started).Milliseconds(), "error_type", fmt.Sprintf("%T", err))

	if err != nil && ctx.Err() == nil {
		log.Error("routine failed", "human_visible", false, "error_type", fmt.Sprintf("%T", err))
	}
}

func routineTraceConversationID(prefix, relativePath string, ts time.Time) string {
	return prefix + relativePath + ":" + ts.UTC().Format("20060102T150405.000000000Z") + ":" + rand.Text()
}

func runOutcome(result protocol.RoutineRunResult, err error) string {
	if err != nil {
		return "failed"
	}

	if !result.Reported {
		return "intentional_silence"
	}

	return "reported"
}

func (m *Manager) preparePrompt(body string) string {
	if body == "" {
		return routineReplyInstruction
	}

	return strings.TrimSuffix(body, "\n") + "\n\n" + routineReplyInstruction
}

func loadDefinitionsIn(cfg *config.LockedConfig, runtimeDir string) ([]config.RoutineDefinition, error) {
	agents, err := backend.LoadRuntimeAgents(cfg, runtimeDir)
	if err != nil {
		return nil, fmt.Errorf("load agents: %w", err)
	}

	var definitions []config.RoutineDefinition

	for _, name := range agents.PrimaryNames() {
		agent := agents.Items[name]
		for _, routine := range agent.Routines {
			definitions = append(definitions, config.RoutineDefinition{Job: protocol.OneOffRoutine{RelativePath: "agents/" + agent.Location + "#" + routine.Schedule.Raw, Agent: name, Prompt: cmp.Or(routine.Instruction, "Scheduled run ("+routine.Schedule.Raw+")."), Space: agent.Space}, Schedule: routine.Schedule})
		}
	}

	return definitions, nil
}
