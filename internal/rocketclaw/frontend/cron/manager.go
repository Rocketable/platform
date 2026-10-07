// Package cronfrontend schedules cron producers and their conversation Sync.
package cronfrontend

import (
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
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

// Runner executes a producer through terminal delivery.
type Runner interface {
	Run(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error)
}

// Manager loads and runs workspace cron definitions.
type Manager struct {
	workspace, runtimeDir string
	channels              []string
	store                 cronScheduleStore
	run                   Runner
	log                   *slog.Logger
	now                   func() time.Time
	tickerInterval        time.Duration

	mu            sync.Mutex
	stop          context.CancelFunc
	start, closed bool
	wg            sync.WaitGroup
}

type cronScheduleStore interface {
	ResetCronSchedules() error
	SyncCronSchedules([]backend.CronScheduleState, time.Time) error
	DueCronSchedules(time.Time) ([]backend.CronScheduleState, error)
	ClaimCronSchedule(backend.CronScheduleState, time.Time, time.Time) (string, bool, error)
	CompleteCronRun(string, time.Time) error
}

type definition struct {
	relativePath, agent, textChannel, body string
	schedules                              []config.CronSchedule
}

// Job is the read-only presentation of a loaded definition and its next triggers.
type Job struct {
	RelativePath, Agent, TextChannel, Body string
	Schedules                              []string
	Upcoming                               []time.Time
}

const (
	cronTracePrefix       = "cron:"
	oneOffCronTracePrefix = "one-off-cron:"
)

// New constructs a cronjob manager using runtimeDir for effective runtime cron definitions.
func New(workspace, runtimeDir string, channels []string, store cronScheduleStore, run Runner, logger *slog.Logger) *Manager {
	channels = slices.Clone(channels)
	for i := range channels {
		channels[i] = strings.TrimSpace(channels[i])
	}

	slices.Sort(channels)
	channels = slices.Compact(channels)

	return &Manager{workspace: workspace, channels: channels, store: store, run: run, log: logger.With("component", "cronjob"), now: time.Now, tickerInterval: time.Minute, runtimeDir: runtimeDir}
}

// ValidateRuntimeDefinitions loads cron definitions from runtimeDir without mutating scheduler state.
func ValidateRuntimeDefinitions(workspace, runtimeDir string, channels []string) error {
	definitions, err := loadDefinitionsIn(workspace, runtimeDir)
	if err != nil {
		return err
	}

	return validateDefinitionChannels(definitions, channels)
}

// Start loads cron definitions and starts scheduling them.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.start {
		m.mu.Unlock()
		return errors.New("cronjob manager already started")
	}
	m.mu.Unlock()

	definitions, err := loadDefinitionsIn(m.workspace, m.runtimeDir)
	if err != nil {
		return err
	}

	if err := validateDefinitionChannels(definitions, m.channels); err != nil {
		return err
	}

	now := m.now()
	if err := m.store.ResetCronSchedules(); err != nil {
		return fmt.Errorf("reset cron schedules: %w", err)
	}

	if err := m.store.SyncCronSchedules(m.scheduledStates(definitions, now), now); err != nil {
		return fmt.Errorf("sync cron schedules: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.start {
		cancel()
		return errors.New("cronjob manager already started")
	}

	m.stop = cancel
	m.start = true
	m.closed = false

	for i := range definitions {
		if len(definitions[i].schedules) == 1 && !definitions[i].schedules[0].DueAt.IsZero() {
			definition := definitions[i]

			m.wg.Add(1)
			go m.runOneOffTimer(runCtx, &definition, max(definition.schedules[0].DueAt.Sub(now), 0))
		}
	}

	m.wg.Add(1)
	go m.runTickerLoop(runCtx)

	m.logLoadedDefinitions(definitions)

	return nil
}

// Stop shuts the cron manager down.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if !m.start {
		m.mu.Unlock()
		return nil
	}

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

// LoadOneOffCronjob resolves and loads one live cronjob for a managed Slack thread run.
func (m *Manager) LoadOneOffCronjob(target string) (protocol.OneOffCronjob, error) {
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
	definitions, err := loadDefinitionsIn(m.workspace, m.runtimeDir)
	if err != nil {
		return nil, err
	}

	if err := validateDefinitionChannels(definitions, m.channels); err != nil {
		return nil, err
	}

	now := m.now()

	triggers, err := m.store.DueCronSchedules(now.Add(24 * time.Hour))
	if err != nil {
		return nil, fmt.Errorf("read cron next triggers: %w", err)
	}

	jobs := make([]Job, 0, len(definitions))
	for _, definition := range definitions {
		job := Job{RelativePath: definition.relativePath, Agent: definition.agent, TextChannel: definition.textChannel, Body: definition.body}
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

	raw.ConversationID = cronTraceConversationID(oneOffCronTracePrefix, job.RelativePath, m.now())
	raw.SyncDestination = job.ConversationID
	raw.Cronjob = &protocol.CronjobMessage{RelativePath: job.RelativePath, Agent: job.Agent, RanAt: m.now().Format(time.RFC3339)}
	raw.TextChannel = job.TextChannel

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return protocol.CronRunResult{}, errors.New("cronjob manager is stopped")
	}

	m.wg.Add(1)

	m.mu.Unlock()
	defer m.wg.Done()

	runCtx := context.WithoutCancel(ctx)

	startedAt := time.Now()
	result, err := m.run.Run(runCtx, job.Agent, job.Prompt, raw)

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

func (m *Manager) runOneOffTimer(ctx context.Context, definition *definition, interval time.Duration) {
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

	m.executeJob(ctx, definition)
	m.deleteOneOffCronjob(definition)
}

func (m *Manager) deleteOneOffCronjob(definition *definition) {
	if err := os.Remove(filepath.Join(m.workspace, m.cronRelativePath(filepath.Base(definition.relativePath)))); err != nil && !errors.Is(err, os.ErrNotExist) {
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

	definitions, err := loadDefinitionsIn(m.workspace, m.runtimeDir)
	if err != nil {
		return err
	}

	if err := validateDefinitionChannels(definitions, m.channels); err != nil {
		return err
	}

	states := m.scheduledStates(definitions, now)
	if err := m.store.SyncCronSchedules(states, now); err != nil {
		return fmt.Errorf("sync cron schedules: %w", err)
	}

	m.mu.Lock()
	closed := m.closed
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

	type scheduledDefinition struct {
		definition definition
		schedule   config.CronSchedule
	}

	scheduledDefinitions := map[string]scheduledDefinition{}

	for i := range definitions {
		definition := definitions[i]
		for index, schedule := range definition.schedules {
			if !schedule.DueAt.IsZero() {
				continue
			}

			id := scheduleID(definition.relativePath, index, schedule.Raw)
			scheduledDefinitions[id] = scheduledDefinition{definition: definition, schedule: schedule}
		}
	}

	startedFiles := map[string]struct{}{}

	for _, state := range due {
		scheduled, ok := scheduledDefinitions[state.ScheduleID]
		if !ok {
			continue
		}

		definition := scheduled.definition

		if _, ok := startedFiles[definition.relativePath]; ok {
			continue
		}

		relativePath, ok, err := m.store.ClaimCronSchedule(state, scheduled.schedule.Next(now), now)
		if err != nil {
			return fmt.Errorf("claim cron schedule: %w", err)
		}

		if !ok {
			continue
		}

		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()

			if err := m.store.CompleteCronRun(relativePath, m.now()); err != nil {
				return fmt.Errorf("complete cron run after stopped claim: %w", err)
			}

			continue
		}

		m.wg.Add(1)
		m.mu.Unlock()

		startedFiles[definition.relativePath] = struct{}{}
		go m.runScheduled(ctx, relativePath, &definition)
	}

	return nil
}

func validateDefinitionChannels(definitions []definition, channels []string) error {
	for _, definition := range definitions {
		if !slices.Contains(channels, definition.textChannel) {
			return fmt.Errorf("cronjob %s channel %q is not configured", definition.relativePath, definition.textChannel)
		}
	}

	return nil
}

func (m *Manager) runScheduled(ctx context.Context, relativePath string, definition *definition) {
	defer m.wg.Done()
	defer func() {
		if err := m.store.CompleteCronRun(relativePath, m.now()); err != nil {
			m.log.Error("complete cron run", "file", relativePath, "error", err)
		}
	}()

	m.executeJob(ctx, definition)
}

func (m *Manager) scheduledStates(definitions []definition, now time.Time) []backend.CronScheduleState {
	var states []backend.CronScheduleState

	for i := range definitions {
		definition := definitions[i]
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

const cronReplyInstruction = "Your reply is posted to the channel exactly as written. If there is nothing worth posting, reply with nothing."

func (m *Manager) executeJob(ctx context.Context, definition *definition) {
	startedAt := m.now()
	ranAt := startedAt.Format(time.RFC3339)
	prompt := m.preparePrompt(definition.body)
	log := m.log.With("file", definition.relativePath, "agent", definition.agent, "ran_at", ranAt)

	progress := &backend.RawRunProgress{
		ConversationID: cronTraceConversationID(cronTracePrefix, definition.relativePath, startedAt),
		TextChannel:    definition.textChannel,
		Cronjob:        &protocol.CronjobMessage{RelativePath: definition.relativePath, Agent: definition.agent, RanAt: ranAt},
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
	prompt := body
	if prompt == "" {
		return cronReplyInstruction
	}

	if strings.HasSuffix(prompt, "\n") {
		return prompt + "\n" + cronReplyInstruction
	}

	return prompt + "\n\n" + cronReplyInstruction
}

func (m *Manager) logLoadedDefinitions(definitions []definition) {
	m.log.Info("loaded cronjobs", "count", len(definitions))

	for i := range definitions {
		definition := definitions[i]
		for range definition.schedules {
			m.log.Info(
				"loaded cronjob schedule",
				"file", definition.relativePath,
				"agent", definition.agent,
			)
		}
	}
}

func loadDefinitionsIn(workspace, runtimeDir string) ([]definition, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	cronPath := filepath.ToSlash(filepath.Join(runtimeDir, "cron"))

	cronRoot, err := root.OpenRoot(cronPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read cronjob directory: %w", err)
	}

	defer func() { _ = cronRoot.Close() }()

	entries, err := fs.ReadDir(cronRoot.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("read cronjob directory: %w", err)
	}

	definitions := make([]definition, 0, len(entries))
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

		definitions = append(definitions, definition)
	}

	return definitions, nil
}

func (m *Manager) cronRelativePath(name string) string {
	return filepath.ToSlash(filepath.Join(m.runtimeDir, "cron", name))
}

func loadDefinition(data []byte, relativePath string) (definition, error) {
	job, schedules, err := config.ParseCronDefinition(data, relativePath)
	if err != nil {
		return definition{}, fmt.Errorf("load cron definition: %w", err)
	}

	return definition{
		relativePath: job.RelativePath,
		agent:        job.Agent,
		textChannel:  job.TextChannel,
		body:         job.Prompt,
		schedules:    schedules,
	}, nil
}

func scheduleID(relativePath string, index int, raw string) string {
	return relativePath + "#" + strconv.Itoa(index) + "#" + raw
}
