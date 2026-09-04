package cron

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type memoryStore struct {
	mu      sync.Mutex
	states  map[string]ScheduleState
	running map[string]bool
}

func newMemoryStore() *memoryStore {
	return &memoryStore{states: map[string]ScheduleState{}, running: map[string]bool{}}
}

func (s *memoryStore) ResetSchedules() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.states = map[string]ScheduleState{}
	s.running = map[string]bool{}

	return nil
}

func (s *memoryStore) SyncSchedules(states []ScheduleState, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := map[string]ScheduleState{}

	for _, state := range states {
		if existing, ok := s.states[state.ScheduleID]; ok {
			state.NextDue = existing.NextDue
		}

		next[state.ScheduleID] = state
	}

	s.states = next

	return nil
}

func (s *memoryStore) DueSchedules(now time.Time, limit int) ([]ScheduleState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var due []ScheduleState

	for _, state := range s.states {
		if state.NextDue.After(now) {
			continue
		}

		due = append(due, state)
		if limit > 0 && len(due) == limit {
			break
		}
	}

	return due, nil
}

func (s *memoryStore) ClaimSchedule(due ScheduleState, nextDue, _ time.Time) (ScheduleRun, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[due.ScheduleID]
	if !ok {
		return ScheduleRun{}, false, nil
	}

	if s.running[state.RelativePath] {
		return ScheduleRun{}, false, nil
	}

	s.running[state.RelativePath] = true
	state.NextDue = nextDue
	s.states[due.ScheduleID] = state

	return ScheduleRun{ScheduleID: state.ScheduleID, RelativePath: state.RelativePath, DueAt: due.NextDue}, true, nil
}

func (s *memoryStore) CompleteRun(relativePath string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.running, relativePath)

	return nil
}

type recordingRoot struct {
	mu        sync.Mutex
	n         int
	threadIDs []string
}

func (r *recordingRoot) StartThread(_ context.Context, _, _, _ string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.n++
	id := protocol.SlackThreadConversationID("C1", strconv.Itoa(r.n)+".0")
	r.threadIDs = append(r.threadIDs, id)

	return id, nil
}

type inertRoot struct{}

func (inertRoot) StartThread(context.Context, string, string, string) (string, error) {
	return "", errors.New("slack thread root is inert")
}

type createdConv struct {
	id     string
	agents []string
	tags   []protocol.ConversationTag
}

type recordingBackend struct {
	mu      sync.Mutex
	created []createdConv
	turns   []protocol.TurnRequest
	syncs   [][2]string
	runHook func(protocol.TurnRequest)
	runErr  error
}

func (r *recordingBackend) Subscribe(context.Context) <-chan protocol.ConversationEvent {
	ch := make(chan protocol.ConversationEvent)
	close(ch)

	return ch
}

func (r *recordingBackend) CreateConversation(id string, agents []string, tags []protocol.ConversationTag) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.created = append(r.created, createdConv{id: id, agents: slices.Clone(agents), tags: slices.Clone(tags)})

	return nil
}

func (r *recordingBackend) RunTurn(_ context.Context, req *protocol.TurnRequest) error {
	r.mu.Lock()
	hook := r.runHook
	r.turns = append(r.turns, *req)
	r.mu.Unlock()

	if hook != nil {
		hook(*req)
	}

	return r.runErr
}

func (r *recordingBackend) SyncConversation(_ context.Context, src, dst string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.syncs = append(r.syncs, [2]string{src, dst})

	return nil
}

func (r *recordingBackend) ListConversations() ([]protocol.ConversationRecord, error) {
	return nil, nil
}

func (r *recordingBackend) ConversationAgent(string) (string, error) {
	return "", protocol.ErrUnknownConversation
}

func (r *recordingBackend) SwitchAgent(string, string) error {
	return nil
}

func (*recordingBackend) ListLaterWork(context.Context, string) ([]protocol.ThreadQueueItem, error) {
	return nil, nil
}

func (*recordingBackend) DeleteLaterWork(context.Context, string, string) error { return nil }

func (*recordingBackend) ReorderLaterWork(context.Context, string, []string) error { return nil }

func (*recordingBackend) ConversationBusy(string) bool { return false }

func (*recordingBackend) ScheduledMessages(string) (map[string]protocol.ScheduledMessageState, error) {
	return nil, nil
}

func (*recordingBackend) WorkflowDescriptions() ([]protocol.WorkflowDescription, error) {
	return nil, nil
}

func (r *recordingBackend) snapshot() (created []createdConv, turns []protocol.TurnRequest, syncs [][2]string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]createdConv(nil), r.created...), append([]protocol.TurnRequest(nil), r.turns...), append([][2]string(nil), r.syncs...)
}

func newTestFrontend(workspace string, channels []string, conv frontend.Backend, roots ThreadRoot) *Frontend {
	return New(workspace, ".", channels, map[string][]string{"#ops": {"main", "helper"}}, conv, newMemoryStore(), roots, slog.New(slog.DiscardHandler))
}

func TestLoadDefinitionsLoadsMarkdownAndSkipsTemplates(t *testing.T) {
	workspace := t.TempDir()
	cronDir := filepath.Join(workspace, "cron")
	require.NoError(t, os.Mkdir(cronDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule:\n  - 15m\n  - '0 8 * * *'\nagent: worker\nchannel: '#triage'\n---\nRun daily\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(cronDir, "archive"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "notes.txt"), []byte("not a cronjob"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "daily.example.md"), []byte("---\nschedule: 1h\n---\nexample\n"), 0o644))

	defs, err := loadDefinitionsIn(workspace, ".")
	require.NoError(t, err)
	require.Len(t, defs, 1)

	def := defs[0]
	require.Equal(t, "cron/daily.md", def.relativePath)
	require.Equal(t, "worker", def.agent)
	require.Equal(t, "#triage", def.textChannel)
	require.Equal(t, "Run daily\n", def.body)
	require.Len(t, def.schedules, 2)
	require.Equal(t, "15m0s", def.schedules[0].duration.String())
	require.NotNil(t, def.schedules[1].parsed)
}

func TestLoadDefinitionsInUsesEffectiveRuntimeCron(t *testing.T) {
	workspace := t.TempDir()
	cronDir := filepath.Join(workspace, ".rocketclaw", "cron")
	require.NoError(t, os.MkdirAll(cronDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\nagent: worker\nchannel: '#ops'\n---\nRuntime cron\n"), 0o644))

	defs, err := loadDefinitionsIn(workspace, ".rocketclaw")
	require.NoError(t, err)
	require.Len(t, defs, 1)
	require.Equal(t, "cron/daily.md", defs[0].relativePath)
	require.Equal(t, "Runtime cron\n", defs[0].body)
}

func TestValidateRuntimeDefinitionsReportsStagedCronParseErrors(t *testing.T) {
	workspace := t.TempDir()
	cronDir := filepath.Join(workspace, ".rocketclaw-stage", "cron")
	require.NoError(t, os.MkdirAll(cronDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "broken.md"), []byte("not frontmatter"), 0o644))

	err := ValidateRuntimeDefinitions(workspace, ".rocketclaw-stage", []string{"#ops"})
	require.ErrorContains(t, err, "yaml frontmatter is required")
}

func TestLoadOneOffCronjobUsesEffectiveRuntimeCron(t *testing.T) {
	workspace := t.TempDir()
	cronDir := filepath.Join(workspace, ".rocketclaw", "cron")
	require.NoError(t, os.MkdirAll(cronDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\nagent: helper\nchannel: '#ops'\n---\nRuntime cron"), 0o644))

	m := New(workspace, ".rocketclaw", nil, nil, &recordingBackend{}, newMemoryStore(), inertRoot{}, slog.New(slog.DiscardHandler))
	job, err := m.LoadOneOffCronjob("daily")
	require.NoError(t, err)
	require.Equal(t, "helper", job.Agent)
	require.Equal(t, "cron/daily.md", job.RelativePath)
	require.Equal(t, "#ops", job.TextChannel)
	require.Contains(t, job.Prompt, "Runtime cron")
	require.Empty(t, job.ConversationID)
	require.NotContains(t, job.ConversationID, "one-off-cron:")
}

func TestListCronjobsFiltersByChannel(t *testing.T) {
	workspace := t.TempDir()
	cronDir := filepath.Join(workspace, ".rocketclaw", "cron")
	require.NoError(t, os.MkdirAll(cronDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\nagent: helper\nchannel: '#ops'\n---\nOps cron"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "weekly.md"), []byte("---\nschedule: 1h\nagent: helper\nchannel: '#triage'\n---\nTriage cron"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "heartbeat.md"), []byte("---\nschedule: 10m\nagent: helper\nchannel: '#ops'\n---\nOps heartbeat"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "webops.md"), []byte("---\nschedule: 1h\nagent: helper\nchannel: ops\n---\nWeb ops cron"), 0o644))

	m := New(workspace, ".rocketclaw", nil, nil, &recordingBackend{}, newMemoryStore(), inertRoot{}, slog.New(slog.DiscardHandler))

	jobs, err := m.ListCronjobs("#ops")
	require.NoError(t, err)
	require.Equal(t, []string{"daily", "heartbeat"}, jobs)

	jobs, err = m.ListCronjobs("#missing")
	require.NoError(t, err)
	require.Empty(t, jobs)

	jobs, err = m.ListCronjobs("ops")
	require.NoError(t, err)
	require.Equal(t, []string{"webops"}, jobs)

	details, err := m.ListWebCronjobDetails()
	require.NoError(t, err)
	require.Len(t, details, 4)
	require.NotEmpty(t, details[0].Upcoming)
	require.NotEmpty(t, details[0].Schedule)

	fires := 0

	for _, detail := range details {
		if detail.Stem == "heartbeat" {
			fires = len(detail.Upcoming)
		}
	}

	require.Greater(t, fires, 48)
}

func TestRunOneOffCronjobRejectsStoppedManager(t *testing.T) {
	conv := &recordingBackend{}
	m := newTestFrontend(t.TempDir(), nil, conv, inertRoot{})
	require.NoError(t, m.Start(t.Context()))
	stopCronFrontend(t, m)

	finished := false

	m.RunOneOffCronjob(t.Context(), &protocol.OneOffCronjob{Agent: "helper", Prompt: "Body", RelativePath: "cron/daily.md", TextChannel: "ops"}, nil, func(_ context.Context, _ protocol.CronRunResult, err error) {
		finished = true

		require.ErrorContains(t, err, "cronjob manager is stopped")
	})
	require.True(t, finished)

	_, turns, _ := conv.snapshot()
	require.Empty(t, turns)
}

func TestLoadDefinitionsWithoutCronDirectory(t *testing.T) {
	defs, err := loadDefinitionsIn(t.TempDir(), ".")
	require.NoError(t, err)
	require.Empty(t, defs)
}

func TestLoadDefinitionsReportsDirectoryErrors(t *testing.T) {
	workspaceFile := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.WriteFile(workspaceFile, []byte("not a directory"), 0o644))

	_, err := loadDefinitionsIn(workspaceFile, ".")
	require.ErrorContains(t, err, "open workspace root")

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "cron"), []byte("not a directory"), 0o644))

	_, err = loadDefinitionsIn(workspace, ".")
	require.ErrorContains(t, err, "read cronjob directory")
}

func TestStartStopLoadsCronjobsWithoutRunningFutureDuration(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "cron"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "cron", "daily.md"), []byte("---\nschedule: 1h\nchannel: '#ops'\n---\nBody"), 0o644))

	conv := &recordingBackend{runHook: func(protocol.TurnRequest) {
		t.Fatal("future duration cronjob ran during start/stop test")
	}}
	m := newTestFrontend(workspace, []string{"#ops"}, conv, &recordingRoot{})
	require.NoError(t, m.Start(t.Context()))
	stopCronFrontend(t, m)
}

func TestStartRejectsAlreadyStartedManager(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "cron"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "cron", "daily.md"), []byte("---\nschedule: 1h\nchannel: '#ops'\n---\nBody"), 0o644))

	m := newTestFrontend(workspace, []string{"#ops"}, &recordingBackend{}, &recordingRoot{})
	require.NoError(t, m.Start(t.Context()))
	err := m.Start(t.Context())
	require.ErrorContains(t, err, "cronjob manager already started")
	stopCronFrontend(t, m)
}

func TestParseScheduleTimestamp(t *testing.T) {
	dueAt := "2026-05-21T15:04:05.123456789Z"
	schedule, err := parseSchedule(dueAt)
	require.NoError(t, err)
	require.Equal(t, dueAt, schedule.dueAt.Format(time.RFC3339Nano))
	require.Zero(t, schedule.duration)
	require.Nil(t, schedule.parsed)
}

func TestOneOffCronjobRunsImmediatelyAndDeletesFile(t *testing.T) {
	workspace := t.TempDir()
	cronDir := filepath.Join(workspace, "cron")
	require.NoError(t, os.Mkdir(cronDir, 0o755))
	cronPath := filepath.Join(cronDir, "due.md")
	require.NoError(t, os.WriteFile(cronPath, []byte("---\nschedule: \"2000-01-01T00:00:00Z\"\nchannel: '#ops'\n---\nBody"), 0o644))

	runDone := make(chan struct{})
	conv := &recordingBackend{runHook: func(protocol.TurnRequest) { close(runDone) }}
	m := newTestFrontend(workspace, []string{"#ops", "#triage"}, conv, &recordingRoot{})
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC) }
	require.NoError(t, m.Start(t.Context()))

	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("one-off cronjob did not run")
	}

	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(cronPath); errors.Is(err, os.ErrNotExist) {
			stopCronFrontend(t, m)
			return
		}
	}

	t.Fatal("one-off cronjob file was not deleted")
}

func TestOneOffCronjobRunsAfterFutureDueTime(t *testing.T) {
	workspace := t.TempDir()
	cronDir := filepath.Join(workspace, "cron")
	require.NoError(t, os.Mkdir(cronDir, 0o755))

	now := time.Now().UTC()
	cronPath := filepath.Join(cronDir, "future.md")
	require.NoError(t, os.WriteFile(cronPath, []byte("---\nschedule: \""+now.Add(30*time.Millisecond).Format(time.RFC3339Nano)+"\"\nchannel: '#ops'\n---\nBody"), 0o644))

	runDone := make(chan struct{})
	conv := &recordingBackend{runHook: func(protocol.TurnRequest) { close(runDone) }}
	m := newTestFrontend(workspace, []string{"#ops", "#triage"}, conv, &recordingRoot{})
	m.now = func() time.Time { return now }
	require.NoError(t, m.Start(t.Context()))

	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("future one-off cronjob did not run")
	}

	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(cronPath); errors.Is(err, os.ErrNotExist) {
			stopCronFrontend(t, m)
			return
		}
	}

	t.Fatal("future one-off cronjob file was not deleted")
}

func TestOneOffCronjobDeletesFileAfterRunError(t *testing.T) {
	workspace := t.TempDir()
	cronDir := filepath.Join(workspace, "cron")
	require.NoError(t, os.Mkdir(cronDir, 0o755))
	cronPath := filepath.Join(cronDir, "error.md")
	require.NoError(t, os.WriteFile(cronPath, []byte("---\nschedule: \"2000-01-01T00:00:00Z\"\nchannel: '#ops'\n---\nBody"), 0o644))

	runDone := make(chan struct{})
	conv := &recordingBackend{runErr: errors.New("cron run failed"), runHook: func(protocol.TurnRequest) { close(runDone) }}
	m := newTestFrontend(workspace, []string{"#ops", "#triage"}, conv, &recordingRoot{})
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC) }
	require.NoError(t, m.Start(t.Context()))

	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("one-off cronjob did not run")
	}

	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(cronPath); errors.Is(err, os.ErrNotExist) {
			stopCronFrontend(t, m)
			return
		}
	}

	t.Fatal("one-off cronjob file was not deleted after run error")
}

func stopCronFrontend(t *testing.T, m *Frontend) {
	t.Helper()

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	require.NoError(t, m.Stop(stopCtx))
}

func TestScanScheduledUsesLatestDefinitionAndPersistsState(t *testing.T) {
	workspace := t.TempDir()
	cronDir := filepath.Join(workspace, "cron")
	require.NoError(t, os.Mkdir(cronDir, 0o755))
	cronPath := filepath.Join(cronDir, "daily.md")
	require.NoError(t, os.WriteFile(cronPath, []byte("---\nschedule: 1s\nagent: helper\nchannel: '#ops'\n---\nold body"), 0o644))

	runPrompt := make(chan string, 1)
	conv := &recordingBackend{runHook: func(req protocol.TurnRequest) { runPrompt <- req.Text }}
	store := newMemoryStore()
	m := New(workspace, ".", []string{"#ops"}, map[string][]string{"#ops": {"helper"}}, conv, store, &recordingRoot{}, slog.New(slog.DiscardHandler))
	start := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	m.now = func() time.Time { return start }

	defs, err := loadDefinitionsIn(workspace, ".")
	require.NoError(t, err)
	require.NoError(t, store.SyncSchedules(m.scheduledStates(defs, start), start))
	require.NoError(t, os.WriteFile(cronPath, []byte("---\nschedule: 1s\nagent: helper\nchannel: '#ops'\n---\nnew body"), 0o644))

	m.now = func() time.Time { return start.Add(time.Second) }
	require.NoError(t, m.scanScheduled(t.Context()))

	select {
	case prompt := <-runPrompt:
		require.Contains(t, prompt, "new body")
	case <-time.After(time.Second):
		t.Fatal("scheduled cronjob did not run")
	}
}

func TestScanScheduledInvalidLiveChannelRunsNothingUntilRepaired(t *testing.T) {
	workspace := t.TempDir()
	cronDir := filepath.Join(workspace, "cron")
	require.NoError(t, os.Mkdir(cronDir, 0o755))
	cronPath := filepath.Join(cronDir, "daily.md")
	require.NoError(t, os.WriteFile(cronPath, []byte("---\nschedule: 1s\nchannel: '#ops'\n---\nbody"), 0o644))

	runs := make(chan struct{}, 1)
	conv := &recordingBackend{runHook: func(protocol.TurnRequest) { runs <- struct{}{} }}
	store := newMemoryStore()
	m := New(workspace, ".", []string{"#ops"}, map[string][]string{"#ops": {"main"}}, conv, store, &recordingRoot{}, slog.New(slog.DiscardHandler))
	start := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	m.now = func() time.Time { return start }
	definitions, err := loadDefinitionsIn(workspace, ".")
	require.NoError(t, err)
	require.NoError(t, store.SyncSchedules(m.scheduledStates(definitions, start), start))

	require.NoError(t, os.WriteFile(cronPath, []byte("---\nschedule: 1s\nchannel: '#private'\n---\ninvalid"), 0o644))

	m.now = func() time.Time { return start.Add(time.Second) }
	require.ErrorContains(t, m.scanScheduled(t.Context()), "channel \"#private\" is not configured")

	select {
	case <-runs:
		t.Fatal("invalid live channel ran")
	default:
	}

	require.NoError(t, os.WriteFile(cronPath, []byte("---\nschedule: 1s\nchannel: '#ops'\n---\nrepaired"), 0o644))
	require.NoError(t, m.scanScheduled(t.Context()))

	select {
	case <-runs:
	case <-time.After(time.Second):
		t.Fatal("repaired cron did not retain due state")
	}
}

func TestScanScheduledCoalescesSameFileAndNoBacklog(t *testing.T) {
	workspace := t.TempDir()
	cronDir := filepath.Join(workspace, "cron")
	require.NoError(t, os.Mkdir(cronDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule:\n  - 1s\n  - 1s\nagent: helper\nchannel: '#ops'\n---\nBody"), 0o644))

	runStarted := make(chan struct{}, 2)
	release := make(chan struct{})
	conv := &recordingBackend{runHook: func(protocol.TurnRequest) {
		runStarted <- struct{}{}

		<-release
	}}
	store := newMemoryStore()
	m := New(workspace, ".", []string{"#ops"}, map[string][]string{"#ops": {"helper"}}, conv, store, &recordingRoot{}, slog.New(slog.DiscardHandler))
	start := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	m.now = func() time.Time { return start }

	defs, err := loadDefinitionsIn(workspace, ".")
	require.NoError(t, err)
	require.NoError(t, store.SyncSchedules(m.scheduledStates(defs, start), start))
	m.now = func() time.Time { return start.Add(time.Second) }
	require.NoError(t, m.scanScheduled(t.Context()))

	select {
	case <-runStarted:
	case <-time.After(time.Second):
		t.Fatal("scheduled cronjob did not run")
	}

	require.NoError(t, m.scanScheduled(t.Context()))

	select {
	case <-runStarted:
		t.Fatal("same-file scheduled cronjob overlapped or replayed backlog")
	default:
	}

	close(release)
	m.wg.Wait()
}

func TestLoadDefinitionRejectsInvalidFrontmatter(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "missing frontmatter", data: "body", want: "yaml frontmatter is required"},
		{name: "missing closing delimiter", data: "---\nschedule: 1h\nbody", want: "yaml frontmatter closing delimiter is required"},
		{name: "invalid yaml", data: "---\nschedule: [\n---\nbody", want: "unmarshal frontmatter yaml"},
		{name: "missing schedule", data: "---\nagent: main\nchannel: '#ops'\n---\nbody", want: "schedule is required"},
		{name: "invalid schedule list item", data: "---\nschedule:\n  - 1h\n  - 3\nchannel: '#ops'\n---\nbody", want: "schedule must be a string or list of strings"},
		{name: "invalid schedule scalar", data: "---\nschedule: 123\nchannel: '#ops'\n---\nbody", want: "schedule must be a string or list of strings"},
		{name: "blank duration", data: "---\nschedule: ''\nchannel: '#ops'\n---\nbody", want: "schedule must not be blank"},
		{name: "zero duration", data: "---\nschedule: 0s\nchannel: '#ops'\n---\nbody", want: "duration schedules must be greater than zero"},
		{name: "every unsupported", data: "---\nschedule: '@every 1h'\nchannel: '#ops'\n---\nbody", want: "@every is not supported"},
		{name: "invalid cron", data: "---\nschedule: not a cron\nchannel: '#ops'\n---\nbody", want: "invalid cron expression"},
		{name: "mixed timestamp", data: "---\nschedule:\n  - '2000-01-01T00:00:00Z'\n  - 1h\nchannel: '#ops'\n---\nbody", want: "timestamp schedules cannot be combined"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadDefinition([]byte(tt.data), "cron/test.md")
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestLoadDefinitionLeavesNonStringAgentForValidation(t *testing.T) {
	def, err := loadDefinition([]byte("---\nschedule: 1h\nagent: 7\nchannel: '#ops'\n---\nBody"), "cron/test.md")
	require.NoError(t, err)
	require.Equal(t, "7", def.agent)
}

func TestLoadDefinitionDefaultsBlankAgent(t *testing.T) {
	def, err := loadDefinition([]byte("---\nschedule: 1h\nagent: '  \t  '\nchannel: '  #ops  '\n---\nBody"), "cron/test.md")
	require.NoError(t, err)
	require.Equal(t, "main", def.agent)
}

func TestLoadOneOffCronjobValidatesTargetsAndPreparesPrompt(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "cron"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "cron", "daily.md"), []byte("---\nschedule: 1h\nagent: helper\nchannel: '#triage'\n---\nBody"), 0o644))

	m := newTestFrontend(workspace, []string{"#ops", "#triage"}, &recordingBackend{}, &recordingRoot{})
	job, err := m.LoadOneOffCronjob("daily.md")
	require.NoError(t, err)
	require.Equal(t, "helper", job.Agent)
	require.Equal(t, "cron/daily.md", job.RelativePath)
	require.Equal(t, "#triage", job.TextChannel)
	require.Contains(t, job.Prompt, "Body")

	for _, target := range []string{"", "nested/daily", "daily.txt", "daily.example", "."} {
		_, err := m.LoadOneOffCronjob(target)
		require.Error(t, err, target)
	}
}

func TestLoadOneOffCronjobReportsReadAndDefinitionErrors(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "cron"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "cron", "broken.md"), []byte("not frontmatter"), 0o644))

	m := newTestFrontend(workspace, []string{"#ops", "#triage"}, &recordingBackend{}, &recordingRoot{})
	_, err := m.LoadOneOffCronjob("missing")
	require.ErrorContains(t, err, "read cronjob cron/missing.md")
	_, err = m.LoadOneOffCronjob("broken")
	require.ErrorContains(t, err, "yaml frontmatter is required")
}

func TestLoadOneOffCronjobReportsWorkspaceOpenError(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.WriteFile(workspace, []byte("not a directory"), 0o644))

	m := newTestFrontend(workspace, []string{"#ops", "#triage"}, &recordingBackend{}, inertRoot{})
	_, err := m.LoadOneOffCronjob("daily")
	require.ErrorContains(t, err, "open workspace root")
}

func TestTwoFiresStartTwoThreadsAndSyncAfterRunTurn(t *testing.T) {
	conv := &recordingBackend{}
	roots := &recordingRoot{}
	m := newTestFrontend(t.TempDir(), []string{"#ops"}, conv, roots)

	m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", textChannel: "#ops", body: "Body"})
	m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", textChannel: "#ops", body: "Body"})

	created, turns, syncs := conv.snapshot()
	require.Len(t, created, 4)
	require.Len(t, turns, 2)
	require.Len(t, syncs, 2)
	require.Len(t, roots.threadIDs, 2)
	require.NotEqual(t, roots.threadIDs[0], roots.threadIDs[1])

	for i, turn := range turns {
		require.True(t, strings.HasPrefix(turn.ID, lockedPrefix))
		require.False(t, strings.HasPrefix(turn.ID, "cron:"))
		require.False(t, strings.HasPrefix(turn.ID, "one-off-cron:"))
		require.NotContains(t, turn.ID, "slack-thread:")
		require.Equal(t, protocol.TurnPrompt, turn.Kind)
		require.Equal(t, "Body", turn.Text)
		require.Equal(t, syncs[i][0], turn.ID)
		require.Equal(t, roots.threadIDs[i], syncs[i][1])
		require.Equal(t, roots.threadIDs[i], turn.UserFacingID)
	}

	var lockedTags, userFacingTags [][]protocol.ConversationTag

	for _, item := range created {
		if strings.HasPrefix(item.id, lockedPrefix) {
			require.Equal(t, []string{"helper"}, item.agents)
			require.Empty(t, item.tags)
			lockedTags = append(lockedTags, item.tags)
		}

		if strings.HasPrefix(item.id, "slack-thread:") {
			require.Contains(t, item.tags, protocol.ConversationUserFacing)
			require.Contains(t, item.tags, protocol.ConversationCron)
			userFacingTags = append(userFacingTags, item.tags)
		}
	}

	require.Len(t, lockedTags, 2)
	require.Len(t, userFacingTags, 2)
}

func TestFireWithoutSlackChannelIsOpaqueAndUserFacing(t *testing.T) {
	conv := &recordingBackend{}
	m := newTestFrontend(t.TempDir(), nil, conv, inertRoot{})
	m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", textChannel: "ops", body: "Body"})

	created, turns, syncs := conv.snapshot()
	require.Len(t, turns, 1)
	require.Len(t, syncs, 1)
	require.True(t, strings.HasPrefix(turns[0].ID, lockedPrefix))
	require.Equal(t, syncs[0][1], turns[0].UserFacingID)
	require.True(t, strings.HasPrefix(syncs[0][1], userFacingPrefix))
	require.NotContains(t, syncs[0][1], "slack-thread:")
	require.NotContains(t, syncs[0][1], "web-session:")
	require.NotContains(t, syncs[0][1], "cron:")

	var sawUserFacing bool

	for _, item := range created {
		if item.id != syncs[0][1] {
			continue
		}

		sawUserFacing = true

		require.Contains(t, item.tags, protocol.ConversationUserFacing)
		require.Contains(t, item.tags, protocol.ConversationCron)
	}

	require.True(t, sawUserFacing)
}

func TestCronFromHumanThreadDoesNotRunTurnThatThread(t *testing.T) {
	humanThread := protocol.SlackThreadConversationID("C9", "99.1")
	conv := &recordingBackend{}
	m := newTestFrontend(t.TempDir(), []string{"#ops"}, conv, &recordingRoot{})

	var finished error

	m.RunOneOffCronjob(t.Context(), &protocol.OneOffCronjob{Agent: "helper", Prompt: "Body", RelativePath: "cron/daily.md", TextChannel: "#ops"}, nil, func(_ context.Context, _ protocol.CronRunResult, err error) {
		finished = err
	})
	require.NoError(t, finished)

	_, turns, syncs := conv.snapshot()
	require.Len(t, turns, 1)
	require.NotEqual(t, humanThread, turns[0].ID)
	require.NotEqual(t, humanThread, syncs[0][1])
	require.True(t, strings.HasPrefix(turns[0].ID, lockedPrefix))
}
