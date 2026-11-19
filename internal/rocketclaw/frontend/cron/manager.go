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

type Runner interface {
	Run(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error)
}

type Manager struct {
	cfg                   *config.LockedConfig
	workspace, runtimeDir string
	store                 cronScheduleStore
	run                   Runner
	log                   *slog.Logger
	now                   func() time.Time
	tickerInterval        time.Duration

	mu          sync.Mutex
	stop        context.CancelFunc
	closed      bool
	definitions []definition
	wg          sync.WaitGroup
}

type cronScheduleStore interface {
	ResetCronSchedules() error
	SyncCronSchedules([]backend.CronScheduleState) error
	ScheduleAgents([]string)
	DueCronSchedules(time.Time) ([]backend.CronScheduleState, error)
	ClaimCronSchedule(backend.CronScheduleState, time.Time, time.Time) (bool, error)
}

type definition struct {
	job       protocol.OneOffCronjob
	schedules []config.CronSchedule
}

type Job struct {
	RelativePath, Agent, Body, Space string
	Schedules                        []string
	Upcoming                         []time.Time
}

const (
	cronTracePrefix       = "cron:"
	oneOffCronTracePrefix = "one-off-cron:"
)

func New(cfg *config.LockedConfig, runtimeDir string, store cronScheduleStore, run Runner, logger *slog.Logger) *Manager {
	return &Manager{cfg: cfg, workspace: cfg.Clone().Workspace, store: store, run: run, log: logger.With("component", "cronjob"), now: time.Now, tickerInterval: time.Minute, runtimeDir: runtimeDir}
}

func ValidateRuntimeDefinitions(cfg *config.LockedConfig, runtimeDir string) error {
	_, err := loadDefinitionsIn(cfg, runtimeDir)

	return err
}

func (m *Manager) Start(ctx context.Context) error {
	definitions, err := loadDefinitionsIn(m.cfg, m.runtimeDir)
	if err != nil {
		return err
	}

	if err := m.store.ResetCronSchedules(); err != nil {
		return fmt.Errorf("reset cron schedules: %w", err)
	}

	if err := m.store.SyncCronSchedules(m.scheduledStates(definitions, m.now())); err != nil {
		return fmt.Errorf("sync cron schedules: %w", err)
	}

	m.store.ScheduleAgents(definitionAgents(definitions))

	runCtx, cancel := context.WithCancel(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.stop, m.definitions = cancel, definitions

	m.wg.Add(1)
	go m.runTickerLoop(runCtx)

	m.log.Info("loaded cronjobs", "count", len(definitions))

	for _, definition := range definitions {
		for range definition.schedules {
			m.log.Info(
				"loaded cronjob schedule",
				"file", definition.job.RelativePath,
				"agent", definition.job.Agent,
			)
		}
	}

	return nil
}

func (m *Manager) Refresh() error {
	definitions, err := loadDefinitionsIn(m.cfg, m.runtimeDir)
	if err != nil {
		return err
	}

	if err := m.store.SyncCronSchedules(m.scheduledStates(definitions, m.now())); err != nil {
		return fmt.Errorf("sync cron schedules: %w", err)
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
		return fmt.Errorf("stop cron jobs: %w", ctx.Err())
	}
}

func (m *Manager) LoadOneOffCronjob(target string) (protocol.OneOffCronjob, error) {
	var (
		job protocol.OneOffCronjob
		err error
	)

	if strings.Contains(target, " · ") {
		definitions, err := loadDefinitionsIn(m.cfg, m.runtimeDir)
		if err != nil {
			return protocol.OneOffCronjob{}, fmt.Errorf("load one-off cronjob: %w", err)
		}

		i := slices.IndexFunc(definitions, func(d definition) bool { return backend.CronStem(d.job.RelativePath) == target })
		if i < 0 {
			return protocol.OneOffCronjob{}, fmt.Errorf("load one-off cronjob: no routine %q", target)
		}

		job = definitions[i].job
	} else if job, err = config.LoadOneOffCronjob(m.workspace, m.runtimeDir, target); err != nil {
		return protocol.OneOffCronjob{}, fmt.Errorf("load one-off cronjob: %w", err)
	}

	job.Prompt = m.preparePrompt(job.Prompt)

	return job, nil
}

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
		job := Job{RelativePath: definition.job.RelativePath, Agent: definition.job.Agent, Body: definition.job.Prompt, Space: definition.job.Space}
		for i, schedule := range definition.schedules {
			job.Schedules = append(job.Schedules, schedule.Raw)

			for _, trigger := range triggers {
				if trigger.ScheduleID == scheduleID(definition.job.RelativePath, i, schedule.Raw) && !trigger.NextDue.Before(now) {
					job.Upcoming = append(job.Upcoming, trigger.NextDue)
				}
			}
		}

		slices.SortFunc(job.Upcoming, time.Time.Compare)
		jobs = append(jobs, job)
	}

	return jobs, nil
}

func (m *Manager) RunOneOffCronjob(ctx context.Context, job *protocol.OneOffCronjob) (protocol.CronRunResult, error) {
	raw := new(backend.RawRunProgress)

	raw.Run = &protocol.CronRun{Path: job.RelativePath, At: m.now(), OneOff: true}
	raw.ConversationID = cronTraceConversationID(oneOffCronTracePrefix, job.RelativePath, raw.Run.At)
	raw.SyncDestination = job.ConversationID
	raw.Cronjob = &protocol.CronjobMessage{RelativePath: job.RelativePath, Agent: job.Agent, RanAt: m.now().Format(time.RFC3339), Space: job.Space}

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

	outcome := "reported"
	if err != nil {
		outcome = "failed"
	} else if !result.Reported {
		outcome = "intentional_silence"
	}

	m.log.Info("one-off cronjob returned", "event", "cron_completed", "outcome", outcome, "conversation_id", raw.ConversationID, "destination_conversation_id", result.ConversationID, "duration_ms", time.Since(startedAt).Milliseconds(), "error_type", fmt.Sprintf("%T", err))

	if err != nil {
		return result, fmt.Errorf("run one-off cronjob: %w", err)
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
				m.log.Error("scan scheduled cronjobs", "error", err)
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

	due, err := m.store.DueCronSchedules(now)
	if err != nil {
		return fmt.Errorf("load due cron schedules: %w", err)
	}

	if len(due) == 0 {
		return nil
	}

	statesByID := map[string]backend.CronScheduleState{}
	for _, state := range m.scheduledStates(definitions, now) {
		statesByID[state.ScheduleID] = state
	}

	definitionsByPath := map[string]definition{}
	for _, definition := range definitions {
		definitionsByPath[definition.job.RelativePath] = definition
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

		delete(definitionsByPath, definition.job.RelativePath)

		go func() {
			defer m.wg.Done()

			m.executeJob(ctx, &definition)
		}()
	}

	return nil
}

func definitionAgents(definitions []definition) []string {
	agents := make([]string, len(definitions))
	for i := range definitions {
		agents[i] = definitions[i].job.Agent
	}

	return agents
}

func (m *Manager) scheduledStates(definitions []definition, now time.Time) []backend.CronScheduleState {
	var states []backend.CronScheduleState

	for _, definition := range definitions {
		for index, schedule := range definition.schedules {
			states = append(states, backend.CronScheduleState{
				ScheduleID:   scheduleID(definition.job.RelativePath, index, schedule.Raw),
				RelativePath: definition.job.RelativePath,
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
	prompt := m.preparePrompt(definition.job.Prompt)

	log := m.log.With("file", definition.job.RelativePath, "agent", definition.job.Agent, "ran_at", ranAt)

	progress := &backend.RawRunProgress{
		ConversationID: cronTraceConversationID(cronTracePrefix, definition.job.RelativePath, startedAt),
		Run:            &protocol.CronRun{Path: definition.job.RelativePath, At: startedAt},
		Cronjob:        &protocol.CronjobMessage{RelativePath: definition.job.RelativePath, Agent: definition.job.Agent, RanAt: ranAt, Space: definition.job.Space},
	}
	log = log.With("conversation_id", progress.ConversationID)
	log.Info("starting cronjob", "event", "cron_started", "prompt_len", len(prompt))

	started := time.Now()
	result, err := m.run.Run(context.WithoutCancel(ctx), definition.job.Agent, prompt, progress)

	outcome := "reported"
	if err != nil {
		outcome = "failed"
	} else if !result.Reported {
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
			if slices.ContainsFunc(definitions, func(d definition) bool { return d.job.RelativePath == relativePath }) {
				return nil, fmt.Errorf("agents/%s: routines: schedule %q is listed twice", agent.Location, schedule.Raw)
			}

			definitions = append(definitions, definition{job: protocol.OneOffCronjob{RelativePath: relativePath, Agent: name, Prompt: cmp.Or(routine.Instruction, "Scheduled run ("+schedule.Raw+")."), Space: agent.Space}, schedules: []config.CronSchedule{schedule}})
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

		switch agent := agents.Items[definition.job.Agent]; {
		case agent.Mode == harness.AgentModeEasy:
			return nil, fmt.Errorf("%s: agent %s is an easy agent; schedule it with its routines", relativePath, definition.job.Agent)
		case agent.Sticky == harness.AgentStickyUser || agent.Sticky == harness.AgentStickySlack:
			return nil, fmt.Errorf("%s: agent %s is sticky: %s; a cron cannot run it", relativePath, definition.job.Agent, agent.Sticky)
		case agent.Sticky == harness.AgentStickyGlobal && definition.job.Space != "" && !strings.EqualFold(definition.job.Space, agent.Space):
			return nil, fmt.Errorf("%s: sticky: global agent %s requires the cron to use its space %s", relativePath, definition.job.Agent, agent.Space)
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

	return definition{job: job, schedules: schedules}, nil
}

func scheduleID(relativePath string, index int, raw string) string {
	return relativePath + "#" + strconv.Itoa(index) + "#" + raw
}
