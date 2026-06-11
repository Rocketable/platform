// Package cronjob loads and runs workspace cronjob prompts.
package cronjob

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
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/robfig/cron/v3"
	"sigs.k8s.io/yaml"
)

// RunResult captures the observable result of one cronjob run.
type RunResult struct {
	Text, VerbatimMessage string
	Attachments           []events.OutboundAttachment
}

// OneOffCronjob captures a live one-off cronjob prompt loaded from disk.
type OneOffCronjob struct {
	Agent, Prompt, RelativePath, SlackChannel string
}

// OnDemandCronTarget extracts one deterministic top-level cron target from connector text.
func OnDemandCronTarget(text string, prefixes ...string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}

	candidates := []string{}
	if target, ok := singleOnDemandCronTarget(text, prefixes); ok {
		candidates = append(candidates, target)
	}

	for _, prefix := range prefixes {
		if after, ok := strings.CutPrefix(text, prefix); ok {
			if target, ok := singleOnDemandCronTarget(after, prefixes); ok {
				candidates = append(candidates, target)
			}
		}
	}

	for field := range strings.FieldsSeq(text) {
		field = strings.Trim(field, "`.,;:()[]<>")
		if target, ok := onDemandCronPathTarget(field); ok {
			candidates = append(candidates, target)
		}
	}

	if len(candidates) == 0 {
		return "", false
	}

	target := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate != target {
			return "", false
		}
	}

	return target, true
}

func singleOnDemandCronTarget(text string, prefixes []string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) != 1 {
		return "", false
	}

	if slices.Contains(prefixes, fields[0]) {
		return "", false
	}

	if target, ok := onDemandCronPathTarget(fields[0]); ok {
		return target, true
	}

	return fields[0], true
}

func onDemandCronPathTarget(text string) (string, bool) {
	if !strings.HasPrefix(text, "cron/") || !strings.HasSuffix(text, ".md") {
		return "", false
	}

	target := strings.TrimSuffix(strings.TrimPrefix(text, "cron/"), ".md")
	if target == "" || strings.ContainsAny(target, `/\`) {
		return "", false
	}

	return target, true
}

// RunFunc executes one cronjob prompt and returns the cronjob result.
type RunFunc func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error)

// Manager loads cron definitions once at startup and schedules them.
type Manager struct {
	workspace        string
	workDir          string
	bus              *events.Bus
	run              RunFunc
	log              *slog.Logger
	now              func() time.Time
	SendSlackChannel func(context.Context, string, string, string, string, string, []events.OutboundAttachment) error

	mu     sync.Mutex
	stop   context.CancelFunc
	cron   *cron.Cron
	jobs   []*job
	start  bool
	closed bool
	wg     sync.WaitGroup
}

type definition struct {
	relativePath, agent, slackChannel, body string
	schedules                               []schedule
}

type schedule struct {
	dueAt    time.Time
	duration time.Duration
	parsed   cron.Schedule
}

const (
	cronTracePrefix       = "cron:"
	oneOffCronTracePrefix = "one-off-cron:"
)

type job struct {
	definition definition
	wakeCh     chan struct{}

	mu      sync.Mutex
	pending int
}

// New constructs a cronjob manager using workDir for effective runtime cron definitions.
func New(workspace, workDir string, bus *events.Bus, run RunFunc, logger *slog.Logger) *Manager {
	return &Manager{workspace: workspace, bus: bus, run: run, log: logger.With("component", "cronjob"), now: time.Now, SendSlackChannel: func(context.Context, string, string, string, string, string, []events.OutboundAttachment) error {
		return nil
	}, workDir: workDir}
}

// Start loads cron definitions and starts scheduling them.
func (m *Manager) Start(ctx context.Context) error {
	definitions, err := loadDefinitionsIn(m.workspace, m.workDir)
	if err != nil {
		return err
	}

	scheduler := cron.New(cron.WithLocation(time.Local))

	jobs := make([]*job, 0, len(definitions))

	for i := range definitions {
		current := &job{definition: definitions[i], wakeCh: make(chan struct{}, 1)}
		for _, schedule := range definitions[i].schedules {
			if schedule.duration > 0 || !schedule.dueAt.IsZero() {
				continue
			}

			scheduler.Schedule(schedule.parsed, cron.FuncJob(current.trigger))
		}

		jobs = append(jobs, current)
	}

	runCtx, cancel := context.WithCancel(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.start {
		cancel()
		return errors.New("cronjob manager already started")
	}

	m.stop = cancel
	m.cron = scheduler
	m.jobs = jobs
	m.start = true
	m.closed = false

	for i := range jobs {
		m.wg.Add(1)

		go m.runJobLoop(runCtx, jobs[i])

		for _, schedule := range jobs[i].definition.schedules {
			interval := schedule.duration
			if !schedule.dueAt.IsZero() {
				interval = max(schedule.dueAt.Sub(m.now()), 0)
			}

			if interval <= 0 && schedule.dueAt.IsZero() {
				continue
			}

			m.wg.Add(1)

			go m.runDurationLoop(runCtx, jobs[i], interval, !schedule.dueAt.IsZero())
		}
	}

	m.cron.Start()
	m.logLoadedDefinitions(definitions)

	return nil
}

// Stop shuts the cron manager down.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if !m.start || m.closed {
		m.mu.Unlock()
		return nil
	}

	m.closed = true
	stop := m.stop
	scheduler := m.cron
	m.mu.Unlock()

	if stop != nil {
		stop()
	}

	if scheduler != nil {
		stopped := scheduler.Stop()
		select {
		case <-stopped.Done():
		case <-ctx.Done():
			return fmt.Errorf("stop cron scheduler: %w", ctx.Err())
		}
	}

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
func (m *Manager) LoadOneOffCronjob(target string) (OneOffCronjob, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return OneOffCronjob{}, errors.New("cron target must be a top-level cron stem like daily or daily.md")
	}

	if strings.Contains(target, "/") || strings.Contains(target, `\`) || strings.Contains(target, string(filepath.Separator)) {
		return OneOffCronjob{}, errors.New("cron target must be a top-level cron stem; nested paths are not allowed")
	}

	name := target
	if before, ok := strings.CutSuffix(name, ".md"); ok {
		name = before
	} else if filepath.Ext(name) != "" {
		return OneOffCronjob{}, errors.New("cron target must omit extensions other than .md")
	}

	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return OneOffCronjob{}, errors.New("cron target must be a top-level cron stem like daily or daily.md")
	}

	if strings.HasSuffix(name, ".example") {
		return OneOffCronjob{}, errors.New("cron target must reference a real cron file, not an example template")
	}

	relativePath := filepath.ToSlash(filepath.Join("cron", name+".md"))

	root, err := os.OpenRoot(m.workspace)
	if err != nil {
		return OneOffCronjob{}, fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	data, err := root.ReadFile(m.cronRelativePath(name + ".md"))
	if err != nil {
		return OneOffCronjob{}, fmt.Errorf("read cronjob %s: %w", relativePath, err)
	}

	definition, err := loadDefinition(data, relativePath)
	if err != nil {
		return OneOffCronjob{}, err
	}

	return OneOffCronjob{Agent: definition.agent, Prompt: m.preparePrompt(definition.body), RelativePath: relativePath, SlackChannel: definition.slackChannel}, nil
}

// RunOneOffCronjob executes a loaded cronjob once with optional progress delivery.
func (m *Manager) RunOneOffCronjob(ctx context.Context, job OneOffCronjob, progress *harnessbridge.RawRunProgress, finish func(context.Context, RunResult, error)) {
	log := m.log.With("file", job.RelativePath, "agent", job.Agent, "one_off", true)

	if progress == nil {
		progress = new(harnessbridge.RawRunProgress)
	}

	progress.ConversationID = cronTraceConversationID(oneOffCronTracePrefix, job.RelativePath, m.now())

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		finish(ctx, RunResult{}, errors.New("cronjob manager is stopped"))

		return
	}

	m.wg.Add(1)

	m.mu.Unlock()
	defer m.wg.Done()

	runCtx := context.WithoutCancel(ctx)

	result, err := m.run(runCtx, job.Agent, job.Prompt, log, progress)
	finish(runCtx, result, err)
}

func (m *Manager) runDurationLoop(ctx context.Context, job *job, interval time.Duration, once bool) {
	defer m.wg.Done()

	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			job.trigger()
		}

		if once {
			return
		}

		timer.Reset(interval)
	}
}

func (m *Manager) runJobLoop(ctx context.Context, job *job) {
	defer m.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-job.wakeCh:
		}

		for {
			if ctx.Err() != nil {
				return
			}

			job.mu.Lock()
			if job.pending == 0 {
				job.mu.Unlock()
				break
			}

			job.pending--
			job.mu.Unlock()

			m.executeJob(context.WithoutCancel(ctx), &job.definition)

			if len(job.definition.schedules) == 1 && !job.definition.schedules[0].dueAt.IsZero() {
				if err := os.Remove(filepath.Join(m.workspace, m.cronRelativePath(filepath.Base(job.definition.relativePath)))); err != nil && !errors.Is(err, os.ErrNotExist) {
					m.log.Warn("delete one-off cronjob", "file", job.definition.relativePath, "error", err)
				}

				if m.workDir != "." {
					if err := os.Remove(filepath.Join(m.workspace, job.definition.relativePath)); err != nil && !errors.Is(err, os.ErrNotExist) {
						m.log.Warn("delete local one-off cronjob", "file", job.definition.relativePath, "error", err)
					}
				}

				return
			}
		}
	}
}

const humanVisibleEmptyCallInstruction = `When you are done YOU MUST CALL ` + harnessbridge.RawRunExposedToolName + `("") (empty string)`

func (m *Manager) executeJob(ctx context.Context, definition *definition) {
	startedAt := m.now()
	ranAt := startedAt.Format(time.RFC3339)
	prompt := m.preparePrompt(definition.body)
	log := m.log.With("file", definition.relativePath, "agent", definition.agent, "ran_at", ranAt)
	log.Info("starting cronjob", "prompt_len", len(prompt))

	progress := &harnessbridge.RawRunProgress{Thinking: func(_ context.Context, text string) error {
		log.Debug("cronjob progress", "text", strings.TrimSpace(text))
		return nil
	}, Message: func(context.Context, string) error { return nil }}
	progress.ConversationID = cronTraceConversationID(cronTracePrefix, definition.relativePath, startedAt)

	result, err := m.run(ctx, definition.agent, prompt, log, progress)
	if err != nil {
		if ctx.Err() == nil {
			log.Error("cronjob failed", "human_visible", false, "error", err)
		}

		return
	}

	humanVisible := result.VerbatimMessage != "" || len(result.Attachments) > 0
	log.Info("completed cronjob", "text", result.Text, "verbatim_message", result.VerbatimMessage, "human_visible", humanVisible)

	if definition.slackChannel != "" {
		if strings.TrimSpace(result.VerbatimMessage) != "" || len(result.Attachments) > 0 {
			if err := m.SendSlackChannel(ctx, definition.slackChannel, definition.relativePath, definition.agent, ranAt, strings.TrimSpace(result.VerbatimMessage), result.Attachments); err != nil {
				log.Warn("send cronjob Slack channel delivery", "slack_channel", definition.slackChannel, "error", err)
			}
		}

		return
	}

	publish := func(label, body string, visible bool) bool {
		inbound := events.NewMainInboundMessage(events.SourceSystem, events.InboundKindInternalize, label, body, false)
		if visible {
			inbound.VerbatimMessage = result.VerbatimMessage
			inbound.VerbatimAttachments = events.CloneOutboundAttachments(result.Attachments)
		}

		if err := m.bus.PublishInbound(ctx, inbound); err != nil {
			if ctx.Err() == nil {
				log.Error("publish cronjob result to main session inbound queue", "label", label, "body", body, "human_visible", visible, "error", err)
			}

			return false
		}

		log.Info("published cronjob result to main session inbound queue", "label", label, "body", body, "human_visible", visible)

		return true
	}
	if humanVisible && !publish("cronjob human_visible file="+definition.relativePath+" ran_at="+ranAt, "Cronjob "+definition.relativePath+" ran at "+ranAt+" requested verbatim delivery to the main session outbound targets:\n\n"+result.VerbatimMessage, true) {
		return
	}

	if strings.TrimSpace(result.Text) == "" {
		return
	}

	publish("cronjob file="+definition.relativePath+" ran_at="+ranAt, "Cronjob "+definition.relativePath+" ran at "+ranAt+":\n\n"+result.Text, false)
}

func cronTraceConversationID(prefix, relativePath string, ts time.Time) string {
	return prefix + strings.ReplaceAll(relativePath, ":", "_") + ":" + ts.UTC().Format("20060102T150405.000000000Z") + ":" + rand.Text()
}

func (m *Manager) preparePrompt(body string) string {
	prompt := body
	if strings.Contains(prompt, harnessbridge.RawRunExposedToolName) {
		return prompt
	}

	if prompt == "" {
		return humanVisibleEmptyCallInstruction
	}

	if strings.HasSuffix(prompt, "\n") {
		return prompt + "\n" + humanVisibleEmptyCallInstruction
	}

	return prompt + "\n\n" + humanVisibleEmptyCallInstruction
}

func (j *job) trigger() {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.pending++
	if j.pending != 1 {
		return
	}

	select {
	case j.wakeCh <- struct{}{}:
	default:
	}
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

func loadDefinitionsIn(workspace, workDir string) ([]definition, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	cronPath := filepath.ToSlash(filepath.Join(workDir, "cron"))

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

func (m *Manager) cronRelativePath(name string) string {
	return filepath.ToSlash(filepath.Join(m.workDir, "cron", name))
}

func loadDefinition(data []byte, relativePath string) (definition, error) {
	frontmatterBytes, body, err := splitFrontmatter(data)
	if err != nil {
		return definition{}, fmt.Errorf("parse cronjob %s: %w", relativePath, err)
	}

	scheduleValues, agent, slackChannel, err := parseFrontmatter(frontmatterBytes)
	if err != nil {
		return definition{}, fmt.Errorf("parse cronjob %s frontmatter: %w", relativePath, err)
	}

	schedules := make([]schedule, 0, len(scheduleValues))
	oneOff := false

	for _, raw := range scheduleValues {
		schedule, err := parseSchedule(raw)
		if err != nil {
			return definition{}, fmt.Errorf("parse cronjob %s schedule %q: %w", relativePath, raw, err)
		}

		oneOff = oneOff || !schedule.dueAt.IsZero()
		schedules = append(schedules, schedule)
	}

	if oneOff && len(schedules) != 1 {
		return definition{}, fmt.Errorf("parse cronjob %s schedules: timestamp schedules cannot be combined with other schedules", relativePath)
	}

	return definition{
		relativePath: relativePath,
		agent:        agent,
		slackChannel: slackChannel,
		body:         body,
		schedules:    schedules,
	}, nil
}

func parseFrontmatter(data []byte) (scheduleValues []string, agent, slackChannel string, err error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, "", "", fmt.Errorf("unmarshal frontmatter yaml: %w", err)
	}

	scheduleValues, err = parseFrontmatterSchedule(raw["schedule"])
	if err != nil {
		return nil, "", "", err
	}

	agent, err = parseFrontmatterAgent(raw["agent"])
	if err != nil {
		return nil, "", "", err
	}

	if text, ok := raw["channel"].(string); ok {
		slackChannel = strings.TrimSpace(text)
	} else if text, ok := raw["slack-channel"].(string); ok {
		slackChannel = strings.TrimSpace(text)
	}

	return scheduleValues, agent, slackChannel, nil
}

func parseFrontmatterSchedule(value any) ([]string, error) {
	switch value := value.(type) {
	case nil:
		return nil, errors.New("schedule is required")
	case string:
		return []string{value}, nil
	case []string:
		return append([]string(nil), value...), nil
	case []any:
		schedules := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, errors.New("schedule must be a string or list of strings")
			}

			schedules = append(schedules, text)
		}

		return schedules, nil
	default:
		return nil, errors.New("schedule must be a string or list of strings")
	}
}

func parseFrontmatterAgent(value any) (string, error) {
	if value == nil {
		return "main", nil
	}

	text, ok := value.(string)
	if !ok {
		return "", errors.New("agent must be a string")
	}

	if text = strings.TrimSpace(text); text != "" {
		return text, nil
	}

	return "main", nil
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
