// Package cron is the Cron Frontend: disk load, clock, and a new X+Y pair per fire.
package cron

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/robfig/cron/v3"
	"sigs.k8s.io/yaml"
)

const (
	lockedPrefix     = "cronjob:"
	userFacingPrefix = "cronjob-user:"
)

// ThreadRoot starts a Slack thread and returns that conversation's id.
type ThreadRoot interface {
	StartThread(ctx context.Context, channel, title, prompt string) (string, error)
}

// ScheduleState records one observed scheduled cron trigger.
type ScheduleState struct {
	ScheduleID   string
	RelativePath string
	NextDue      time.Time
}

// ScheduleRun is a claimed scheduled cron run.
type ScheduleRun struct {
	ScheduleID   string
	RelativePath string
	DueAt        time.Time
}

// ScheduleStore is Cron Frontend state in Postgres cron_schedules.
type ScheduleStore interface {
	ResetSchedules() error
	SyncSchedules([]ScheduleState, time.Time) error
	DueSchedules(time.Time, int) ([]ScheduleState, error)
	ClaimSchedule(ScheduleState, time.Time, time.Time) (ScheduleRun, bool, error)
	CompleteRun(string, time.Time) error
}

// Frontend loads job files, runs the clock, and starts a new X+Y pair per fire.
type Frontend struct {
	workspace, runtimeDir string
	channels              []string
	agents                map[string][]string
	backend               frontend.Backend
	store                 ScheduleStore
	roots                 ThreadRoot
	log                   *slog.Logger
	now                   func() time.Time
	tickerInterval        time.Duration

	mu            sync.Mutex
	stop          context.CancelFunc
	start, closed bool
	wg            sync.WaitGroup
}

type definition struct {
	relativePath, agent, textChannel, body string
	schedules                              []schedule
}

type schedule struct {
	raw      string
	dueAt    time.Time
	duration time.Duration
	parsed   cron.Schedule
}

// New constructs a Cron Frontend using runtimeDir for effective runtime cron definitions.
func New(workspace, runtimeDir string, channels []string, agents map[string][]string, conv frontend.Backend, store ScheduleStore, roots ThreadRoot, logger *slog.Logger) *Frontend {
	channels = slices.Clone(channels)
	for i := range channels {
		channels[i] = strings.TrimSpace(channels[i])
	}

	slices.Sort(channels)
	channels = slices.Compact(channels)

	copied := make(map[string][]string, len(agents))
	for channel, list := range agents {
		copied[channel] = slices.Clone(list)
	}

	return &Frontend{workspace: workspace, channels: channels, agents: copied, backend: conv, store: store, roots: roots, log: logger.With("component", "cronjob"), now: time.Now, tickerInterval: time.Minute, runtimeDir: runtimeDir}
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
func (m *Frontend) Start(ctx context.Context) error {
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
	if err := m.store.ResetSchedules(); err != nil {
		return fmt.Errorf("reset cron schedules: %w", err)
	}

	if err := m.store.SyncSchedules(m.scheduledStates(definitions, now), now); err != nil {
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
		if len(definitions[i].schedules) == 1 && !definitions[i].schedules[0].dueAt.IsZero() {
			definition := definitions[i]

			m.wg.Add(1)
			go m.runOneOffTimer(runCtx, &definition, max(definition.schedules[0].dueAt.Sub(now), 0))
		}
	}

	m.wg.Add(1)
	go m.runTickerLoop(runCtx)

	m.logLoadedDefinitions(definitions)

	return nil
}

// Stop shuts the cron manager down.
func (m *Frontend) Stop(ctx context.Context) error {
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
func (m *Frontend) LoadOneOffCronjob(target string) (protocol.OneOffCronjob, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return protocol.OneOffCronjob{}, errors.New("cron target must be a top-level cron stem like daily or daily.md")
	}

	if strings.Contains(target, "/") || strings.Contains(target, `\`) || strings.Contains(target, string(filepath.Separator)) {
		return protocol.OneOffCronjob{}, errors.New("cron target must be a top-level cron stem; nested paths are not allowed")
	}

	name := target
	if before, ok := strings.CutSuffix(name, ".md"); ok {
		name = before
	} else if filepath.Ext(name) != "" {
		return protocol.OneOffCronjob{}, errors.New("cron target must omit extensions other than .md")
	}

	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return protocol.OneOffCronjob{}, errors.New("cron target must be a top-level cron stem like daily or daily.md")
	}

	if strings.HasSuffix(name, ".example") {
		return protocol.OneOffCronjob{}, errors.New("cron target must reference a real cron file, not an example template")
	}

	relativePath := filepath.ToSlash(filepath.Join("cron", name+".md"))

	root, err := os.OpenRoot(m.workspace)
	if err != nil {
		return protocol.OneOffCronjob{}, fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	data, err := root.ReadFile(m.cronRelativePath(name + ".md"))
	if err != nil {
		return protocol.OneOffCronjob{}, fmt.Errorf("read cronjob %s: %w", relativePath, err)
	}

	definition, err := loadDefinition(data, relativePath)
	if err != nil {
		return protocol.OneOffCronjob{}, err
	}

	return protocol.OneOffCronjob{Agent: definition.agent, Prompt: definition.body, RelativePath: relativePath, TextChannel: definition.textChannel}, nil
}

// ListCronjobs returns top-level cron stems whose configured channel matches channel.
func (m *Frontend) ListCronjobs(channel string) ([]string, error) {
	channel = strings.TrimSpace(channel)

	definitions, err := loadDefinitionsIn(m.workspace, m.runtimeDir)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0)

	for _, definition := range definitions {
		if definition.textChannel != channel {
			continue
		}

		names = append(names, strings.TrimSuffix(strings.TrimPrefix(definition.relativePath, "cron/"), ".md"))
	}

	return names, nil
}

// ListWebCronjobs returns stems whose channel is a web session name.
func (m *Frontend) ListWebCronjobs() ([]string, error) {
	definitions, err := loadDefinitionsIn(m.workspace, m.runtimeDir)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0)

	for _, definition := range definitions {
		if strings.HasPrefix(definition.textChannel, "#") {
			continue
		}

		names = append(names, strings.TrimSuffix(strings.TrimPrefix(definition.relativePath, "cron/"), ".md"))
	}

	return names, nil
}

// CronjobDetail is one cron definition for the web cron page.
type CronjobDetail struct {
	Stem, Agent, Channel, Body, Schedule string
	Upcoming                             []time.Time
}

// ListWebCronjobDetails returns cron definitions with upcoming fires.
func (m *Frontend) ListWebCronjobDetails() ([]CronjobDetail, error) {
	definitions, err := loadDefinitionsIn(m.workspace, m.runtimeDir)
	if err != nil {
		return nil, err
	}

	now := m.now()
	until := now.Add(24 * time.Hour)
	out := make([]CronjobDetail, 0)

	for _, definition := range definitions {
		raws := make([]string, 0, len(definition.schedules))
		upcoming := make([]time.Time, 0)

		for _, item := range definition.schedules {
			raws = append(raws, item.raw)
			if !item.dueAt.IsZero() {
				if !item.dueAt.Before(now) && !item.dueAt.After(until) {
					upcoming = append(upcoming, item.dueAt)
				}

				continue
			}

			at := now
			for {
				at = item.next(at)
				if at.IsZero() || at.After(until) {
					break
				}

				upcoming = append(upcoming, at)
			}
		}

		slices.SortFunc(upcoming, func(a, b time.Time) int { return a.Compare(b) })
		out = append(out, CronjobDetail{
			Stem:     strings.TrimSuffix(strings.TrimPrefix(definition.relativePath, "cron/"), ".md"),
			Agent:    definition.agent,
			Channel:  definition.textChannel,
			Body:     definition.body,
			Schedule: strings.Join(raws, ", "),
			Upcoming: upcoming,
		})
	}

	return out, nil
}

// RunOneOffCronjob starts a new X+Y pair for `$cron` / web run. Progress is unused; humans see Y after Sync.
func (m *Frontend) RunOneOffCronjob(ctx context.Context, job *protocol.OneOffCronjob, _ *protocol.CronProgress, finish func(context.Context, protocol.CronRunResult, error)) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		finish(ctx, protocol.CronRunResult{}, errors.New("cronjob manager is stopped"))

		return
	}

	m.wg.Add(1)

	m.mu.Unlock()
	defer m.wg.Done()

	runCtx := context.WithoutCancel(ctx)
	err := m.fire(runCtx, job.Agent, job.Prompt, job.RelativePath, job.TextChannel)
	finish(runCtx, protocol.CronRunResult{}, err)
}

func (m *Frontend) runOneOffTimer(ctx context.Context, definition *definition, interval time.Duration) {
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

func (m *Frontend) deleteOneOffCronjob(definition *definition) {
	if err := os.Remove(filepath.Join(m.workspace, m.cronRelativePath(filepath.Base(definition.relativePath)))); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.log.Warn("delete one-off cronjob", "file", definition.relativePath, "error", err)
	}

	if m.runtimeDir != "." {
		if err := os.Remove(filepath.Join(m.workspace, definition.relativePath)); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.log.Warn("delete local one-off cronjob", "file", definition.relativePath, "error", err)
		}
	}
}

func (m *Frontend) runTickerLoop(ctx context.Context) {
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

func (m *Frontend) scanScheduled(ctx context.Context) error {
	now := m.now()

	definitions, err := loadDefinitionsIn(m.workspace, m.runtimeDir)
	if err != nil {
		return err
	}

	if err := validateDefinitionChannels(definitions, m.channels); err != nil {
		return err
	}

	states := m.scheduledStates(definitions, now)
	if err := m.store.SyncSchedules(states, now); err != nil {
		return fmt.Errorf("sync cron schedules: %w", err)
	}

	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()

	if closed {
		return nil
	}

	due, err := m.store.DueSchedules(now, 0)
	if err != nil {
		return fmt.Errorf("load due cron schedules: %w", err)
	}

	if len(due) == 0 {
		return nil
	}

	type scheduledDefinition struct {
		definition definition
		schedule   schedule
	}

	scheduledDefinitions := map[string]scheduledDefinition{}

	for i := range definitions {
		definition := definitions[i]
		for index, schedule := range definition.schedules {
			if !schedule.dueAt.IsZero() {
				continue
			}

			id := scheduleID(definition.relativePath, index, schedule.raw)
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

		run, ok, err := m.store.ClaimSchedule(state, scheduled.schedule.next(now), now)
		if err != nil {
			return fmt.Errorf("claim cron schedule: %w", err)
		}

		if !ok {
			continue
		}

		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()

			if err := m.store.CompleteRun(run.RelativePath, m.now()); err != nil {
				return fmt.Errorf("complete cron run after stopped claim: %w", err)
			}

			continue
		}

		m.wg.Add(1)
		m.mu.Unlock()

		startedFiles[definition.relativePath] = struct{}{}
		go m.runScheduled(ctx, run, &definition)
	}

	return nil
}

func validateDefinitionChannels(definitions []definition, channels []string) error {
	for _, definition := range definitions {
		if strings.HasPrefix(definition.textChannel, "#") && !slices.Contains(channels, definition.textChannel) {
			return fmt.Errorf("cronjob %s channel %q is not configured", definition.relativePath, definition.textChannel)
		}
	}

	return nil
}

func (m *Frontend) runScheduled(ctx context.Context, run ScheduleRun, definition *definition) {
	defer m.wg.Done()
	defer func() {
		if err := m.store.CompleteRun(run.RelativePath, m.now()); err != nil {
			m.log.Error("complete cron run", "file", run.RelativePath, "error", err)
		}
	}()

	m.executeJob(ctx, definition)
}

func (m *Frontend) scheduledStates(definitions []definition, now time.Time) []ScheduleState {
	var states []ScheduleState

	for i := range definitions {
		definition := definitions[i]
		for index, schedule := range definition.schedules {
			if !schedule.dueAt.IsZero() {
				continue
			}

			states = append(states, ScheduleState{
				ScheduleID:   scheduleID(definition.relativePath, index, schedule.raw),
				RelativePath: definition.relativePath,
				NextDue:      schedule.next(now),
			})
		}
	}

	return states
}

func (m *Frontend) executeJob(ctx context.Context, definition *definition) {
	startedAt := m.now()
	ranAt := startedAt.Format(time.RFC3339)
	log := m.log.With("file", definition.relativePath, "agent", definition.agent, "ran_at", ranAt)
	log.Info("starting cronjob", "prompt_len", len(definition.body))

	if err := m.fire(context.WithoutCancel(ctx), definition.agent, definition.body, definition.relativePath, definition.textChannel); err != nil {
		if ctx.Err() == nil {
			log.Error("cronjob failed", "error", err)
		}

		return
	}

	log.Info("completed cronjob")
}

func (m *Frontend) fire(ctx context.Context, agent, prompt, relativePath, textChannel string) error {
	lockedID := lockedPrefix + rand.Text()
	userFacingID := userFacingPrefix + rand.Text()

	if strings.HasPrefix(textChannel, "#") {
		var err error

		userFacingID, err = m.roots.StartThread(ctx, textChannel, path.Base(relativePath), prompt)
		if err != nil {
			return fmt.Errorf("start cron thread: %w", err)
		}
	}

	if err := m.backend.CreateConversation(lockedID, []string{agent}, nil); err != nil {
		return fmt.Errorf("create locked cron conversation: %w", err)
	}

	if err := m.backend.CreateConversation(userFacingID, m.agentsForChannel(textChannel, agent), []protocol.ConversationTag{protocol.ConversationUserFacing, protocol.ConversationCron}); err != nil {
		return fmt.Errorf("create user-facing cron conversation: %w", err)
	}

	if err := m.backend.RunTurn(ctx, &protocol.TurnRequest{ID: lockedID, Kind: protocol.TurnPrompt, Text: prompt, Agent: agent, UserFacingID: userFacingID}); err != nil {
		return fmt.Errorf("run cron turn: %w", err)
	}

	if err := m.backend.SyncConversation(ctx, lockedID, userFacingID); err != nil {
		return fmt.Errorf("sync cron conversation: %w", err)
	}

	return nil
}

func (m *Frontend) agentsForChannel(textChannel, jobAgent string) []string {
	agents := slices.Clone(m.agents[textChannel])
	if len(agents) == 0 {
		agents = []string{jobAgent}
	}

	if !slices.Contains(agents, jobAgent) {
		agents = append([]string{jobAgent}, agents...)
	}

	return agents
}

func (m *Frontend) logLoadedDefinitions(definitions []definition) {
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
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".example.md") {
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

func (m *Frontend) cronRelativePath(name string) string {
	return filepath.ToSlash(filepath.Join(m.runtimeDir, "cron", name))
}

func loadDefinition(data []byte, relativePath string) (definition, error) {
	frontmatterBytes, body, err := splitFrontmatter(data)
	if err != nil {
		return definition{}, fmt.Errorf("parse cronjob %s: %w", relativePath, err)
	}

	scheduleValues, agent, textChannel, err := parseFrontmatter(frontmatterBytes)
	if err != nil {
		return definition{}, fmt.Errorf("parse cronjob %s frontmatter: %w", relativePath, err)
	}

	if textChannel == "" {
		return definition{}, fmt.Errorf("parse cronjob %s frontmatter: channel is required", relativePath)
	}

	schedules := make([]schedule, 0, len(scheduleValues))
	oneOff := false

	for _, raw := range scheduleValues {
		schedule, err := parseSchedule(raw)
		if err != nil {
			return definition{}, fmt.Errorf("parse cronjob %s schedule %q: %w", relativePath, raw, err)
		}

		schedule.raw = strings.TrimSpace(raw)

		oneOff = oneOff || !schedule.dueAt.IsZero()
		schedules = append(schedules, schedule)
	}

	if oneOff && len(schedules) != 1 {
		return definition{}, fmt.Errorf("parse cronjob %s schedules: timestamp schedules cannot be combined with other schedules", relativePath)
	}

	return definition{
		relativePath: relativePath,
		agent:        agent,
		textChannel:  textChannel,
		body:         body,
		schedules:    schedules,
	}, nil
}

func parseFrontmatter(data []byte) (scheduleValues []string, agent, textChannel string, err error) {
	var raw frontmatter
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, "", "", fmt.Errorf("unmarshal frontmatter yaml: %w", err)
	}

	if !raw.Schedule.present {
		return nil, "", "", errors.New("schedule is required")
	}

	agent = string(raw.Agent)
	if agent == "" {
		agent = "main"
	}

	return slices.Clone(raw.Schedule.values), agent, string(raw.Channel), nil
}

type frontmatter struct {
	Schedule frontmatterSchedule `json:"schedule"`
	Agent    frontmatterAgent    `json:"agent"`
	Channel  frontmatterChannel  `json:"channel"`
}

type frontmatterSchedule struct {
	present bool
	values  []string
}

func (s *frontmatterSchedule) UnmarshalJSON(data []byte) error {
	if strings.TrimSpace(string(data)) == "null" {
		return nil
	}

	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		s.present = true
		s.values = []string{single}

		return nil
	}

	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		s.present = true
		s.values = slices.Clone(list)

		return nil
	}

	return errors.New("schedule must be a string or list of strings")
}

type frontmatterAgent string

func (a *frontmatterAgent) UnmarshalJSON(data []byte) error {
	if strings.TrimSpace(string(data)) == "null" {
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*a = frontmatterAgent(strings.TrimSpace(text))

		return nil
	}

	*a = frontmatterAgent(strings.TrimSpace(string(data)))

	return nil
}

type frontmatterChannel string

func (c *frontmatterChannel) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		text = strings.TrimSpace(text)
		*c = frontmatterChannel(text)
	}

	return nil
}

func splitFrontmatter(data []byte) (frontmatter []byte, body string, err error) {
	source := string(data)

	line, next, ok := readLine(source, 0)
	if !ok || strings.TrimSuffix(line, "\r") != "---" {
		return nil, "", errors.New("yaml frontmatter is required")
	}

	frontmatterStart := next
	for offset := next; offset <= len(source); {
		line, next, ok = readLine(source, offset)
		if !ok {
			break
		}

		if strings.TrimSuffix(line, "\r") == "---" {
			return []byte(source[frontmatterStart:offset]), source[next:], nil
		}

		offset = next
	}

	return nil, "", errors.New("yaml frontmatter closing delimiter is required")
}

func readLine(source string, start int) (line string, next int, ok bool) {
	if start >= len(source) {
		return "", len(source), false
	}

	if index := strings.IndexByte(source[start:], '\n'); index >= 0 {
		index += start
		return source[start:index], index + 1, true
	}

	return source[start:], len(source), true
}

func parseSchedule(raw string) (schedule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return schedule{}, errors.New("schedule must not be blank")
	}

	if dueAt, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return schedule{dueAt: dueAt}, nil
	}

	if duration, err := time.ParseDuration(raw); err == nil {
		if duration <= 0 {
			return schedule{}, errors.New("duration schedules must be greater than zero")
		}

		return schedule{duration: duration, parsed: nil}, nil
	}

	if strings.HasPrefix(raw, "@every") {
		return schedule{}, errors.New("@every is not supported")
	}

	parsed, err := cron.ParseStandard(raw)
	if err != nil {
		return schedule{}, fmt.Errorf("invalid cron expression: %w", err)
	}

	return schedule{duration: 0, parsed: parsed}, nil
}

func (s schedule) next(now time.Time) time.Time {
	if s.duration > 0 {
		return now.Add(s.duration)
	}

	return s.parsed.Next(now)
}

func scheduleID(relativePath string, index int, raw string) string {
	return relativePath + "#" + strconv.Itoa(index) + "#" + raw
}
