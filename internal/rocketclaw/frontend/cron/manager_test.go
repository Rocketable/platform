package cronfrontend

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

func newCronScheduleStore(t *testing.T) *backend.SessionService {
	t.Helper()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	if err != nil {
		t.Fatal(err)
	}

	store, err := backend.NewSessionServiceIn(dsn, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := store.Stop(); err != nil {
			t.Fatal(err)
		}
	})

	return store
}

func TestJobsProjectsDefinitionsAndPersistedTriggers(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll("runtime/cron", 0o700))
	require.NoError(t, root.WriteFile("runtime/cron/alpha.md", []byte("---\nschedule: [30m, '0 * * * *']\nagent: worker\nchannel: '#ops'\n---\nExact body\n"), 0o600))
	require.NoError(t, root.WriteFile("runtime/cron/zeta.md", []byte("---\nschedule: '2000-01-01T00:45:00Z'\nagent: worker\nchannel: '#ops'\n---\nOnce\n"), 0o600))
	store := newCronScheduleStore(t)
	manager := New(workspace, "runtime", []string{"#ops"}, store, &runnerMock{}, slog.New(slog.DiscardHandler))
	start := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return start.Add(5 * time.Minute) }
	definitions, err := loadDefinitionsIn(workspace, "runtime")
	require.NoError(t, err)

	states := manager.scheduledStates(definitions, start)
	require.NoError(t, store.SyncCronSchedules(states, start))

	jobs, err := manager.Jobs()
	require.NoError(t, err)
	require.Equal(t, []Job{
		{RelativePath: "cron/alpha.md", Agent: "worker", TextChannel: "#ops", Body: "Exact body\n", Schedules: []string{"30m", "0 * * * *"}, Upcoming: []time.Time{start.Add(30 * time.Minute), start.Add(time.Hour)}},
		{RelativePath: "cron/zeta.md", Agent: "worker", TextChannel: "#ops", Body: "Once\n", Schedules: []string{"2000-01-01T00:45:00Z"}, Upcoming: []time.Time{start.Add(45 * time.Minute)}},
	}, jobs)
	remaining, err := store.DueCronSchedules(start.Add(24 * time.Hour))
	require.NoError(t, err)
	require.ElementsMatch(t, states, remaining)
	require.NoError(t, root.WriteFile("outside.md", []byte("outside Cron root"), 0o600))
	require.NoError(t, root.Symlink("../../outside.md", "runtime/cron/escape.md"))

	_, err = manager.Jobs()
	require.ErrorContains(t, err, "escape.md")
}

func TestLoadDefinitionsLoadsMarkdownAndSkipsTemplates(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, "cron")
	if err := os.Mkdir(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule:\n  - 15m\n  - '0 8 * * *'\nagent: worker\nchannel: '#triage'\n---\nRun daily\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(filepath.Join(cronDir, "archive"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "notes.txt"), []byte("not a cronjob"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "daily.example.md"), []byte("---\nschedule: 1h\n---\nexample\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := loadDefinitionsIn(workspace, ".")
	if err != nil {
		t.Fatal(err)
	}

	if len(defs) != 1 {
		t.Fatalf("loadDefinitions loaded %d definitions; want 1", len(defs))
	}

	def := defs[0]
	if def.relativePath != "cron/daily.md" || def.agent != "worker" || def.textChannel != "#triage" || def.body != "Run daily\n" {
		t.Fatalf("definition = %#v; want daily worker body", def)
	}

	if len(def.schedules) != 2 || def.schedules[0].duration.String() != "15m0s" || def.schedules[1].parsed == nil {
		t.Fatalf("schedules = %#v; want duration and cron", def.schedules)
	}
}

func TestLoadDefinitionsInUsesEffectiveRuntimeCron(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, ".rocketclaw", "cron")
	if err := os.MkdirAll(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\nagent: worker\nchannel: '#ops'\n---\nRuntime cron\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := loadDefinitionsIn(workspace, ".rocketclaw")
	if err != nil {
		t.Fatal(err)
	}

	if len(defs) != 1 || defs[0].relativePath != "cron/daily.md" || defs[0].body != "Runtime cron\n" {
		t.Fatalf("loadDefinitionsIn = %#v; want effective runtime cron", defs)
	}
}

func TestValidateRuntimeDefinitionsReportsStagedCronParseErrors(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, ".rocketclaw-stage", "cron")
	if err := os.MkdirAll(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "broken.md"), []byte("not frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ValidateRuntimeDefinitions(workspace, ".rocketclaw-stage", []string{"#ops"}); err == nil || !strings.Contains(err.Error(), "yaml frontmatter is required") {
		t.Fatalf("ValidateRuntimeDefinitions() error = %v; want frontmatter error", err)
	}
}

func TestLoadOneOffCronjobUsesEffectiveRuntimeCron(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, ".rocketclaw", "cron")
	if err := os.MkdirAll(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\nagent: helper\nchannel: '#ops'\n---\nRuntime cron"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(workspace, ".rocketclaw", nil, newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		t.Fatal("cronjob manager ran during load test")

		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	job, err := m.LoadOneOffCronjob("daily")
	if err != nil {
		t.Fatal(err)
	}

	if job.Agent != "helper" || job.RelativePath != "cron/daily.md" || job.TextChannel != "#ops" || !strings.Contains(job.Prompt, "Runtime cron") {
		t.Fatalf("LoadOneOffCronjob = %#v; want effective runtime cron", job)
	}
}

func TestListCronjobsFiltersByChannel(t *testing.T) {
	workspace := t.TempDir()
	cronDir := filepath.Join(workspace, ".rocketclaw", "cron")
	require.NoError(t, os.MkdirAll(cronDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\nagent: helper\nchannel: '#ops'\n---\nOps cron"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "weekly.md"), []byte("---\nschedule: 1h\nagent: helper\nchannel: '#triage'\n---\nTriage cron"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "heartbeat.md"), []byte("---\nschedule: 1h\nagent: helper\nchannel: '#ops'\n---\nOps heartbeat"), 0o644))

	m := New(workspace, ".rocketclaw", nil, newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		t.Fatal("cronjob manager ran during list test")

		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	jobs, err := m.ListCronjobs("#ops")
	require.NoError(t, err)
	require.Equal(t, []string{"daily", "heartbeat"}, jobs)

	jobs, err = m.ListCronjobs("#missing")
	require.NoError(t, err)
	require.Empty(t, jobs)
}

func TestCronTraceConversationIDPreservesRelativePath(t *testing.T) {
	ts := time.Date(2000, 1, 2, 3, 4, 5, 6, time.UTC)

	for _, prefix := range []string{cronTracePrefix, oneOffCronTracePrefix} {
		for _, path := range []string{"cron/report:daily.md", "cron/report_daily.md"} {
			id := cronTraceConversationID(prefix, path, ts)
			require.Equal(t, prefix+path+":20000102T030405.000000006Z", id[:strings.LastIndex(id, ":")])
		}
	}
}

func TestRunOneOffCronjobSetsTraceConversationID(t *testing.T) {
	var sources []string

	m := New(t.TempDir(), ".", nil, newCronScheduleStore(t), &runnerMock{RunFunc: func(_ context.Context, agent, prompt string, progress *backend.RawRunProgress) (protocol.CronRunResult, error) {
		require.Equal(t, "helper", agent)
		require.Equal(t, "Body", prompt)
		require.Equal(t, "slack-thread:C1:1.2", progress.SyncDestination)
		require.Equal(t, &protocol.CronjobMessage{RelativePath: "cron/daily.md", Agent: "helper", RanAt: "2000-01-02T03:04:05Z"}, progress.Cronjob)

		sources = append(sources, progress.ConversationID)
		if !strings.HasPrefix(progress.ConversationID, "one-off-cron:cron/daily.md:20000102T030405.000000006Z:") {
			t.Fatalf("ConversationID = %q; want one-off trace ID", progress.ConversationID)
		}

		if progress.TextChannel != "#ops" {
			t.Fatalf("TextChannel = %q; want #ops", progress.TextChannel)
		}

		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 6, time.UTC) }

	for range 2 {
		_, err := m.RunOneOffCronjob(t.Context(), &protocol.OneOffCronjob{Agent: "helper", Prompt: "Body", RelativePath: "cron/daily.md", TextChannel: "#ops", ConversationID: "slack-thread:C1:1.2"})
		require.NoError(t, err)
	}

	require.Len(t, sources, 2)
	require.NotEqual(t, sources[0], sources[1])
}

func TestRunOneOffCronjobRejectsStoppedManager(t *testing.T) {
	m := New(t.TempDir(), ".", nil, newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		t.Fatal("stopped cronjob manager ran one-off cronjob")

		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	stopCronManager(t, m)

	_, err := m.RunOneOffCronjob(t.Context(), &protocol.OneOffCronjob{Agent: "helper", Prompt: "Body", RelativePath: "cron/daily.md"})
	require.ErrorContains(t, err, "cronjob manager is stopped")
}

func TestExecuteJobSetsTraceConversationID(t *testing.T) {
	m := New(t.TempDir(), ".", nil, newCronScheduleStore(t), &runnerMock{RunFunc: func(_ context.Context, _, _ string, progress *backend.RawRunProgress) (protocol.CronRunResult, error) {
		if !strings.HasPrefix(progress.ConversationID, "cron:cron/daily.md:20000102T030405.000000006Z:") {
			t.Fatalf("ConversationID = %q; want scheduled trace ID", progress.ConversationID)
		}

		if progress.TextChannel != "#ops" {
			t.Fatalf("TextChannel = %q; want #ops", progress.TextChannel)
		}

		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 6, time.UTC) }

	m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", textChannel: "#ops", body: "Body"})
}

func TestLoadDefinitionsWithoutCronDirectory(t *testing.T) {
	defs, err := loadDefinitionsIn(t.TempDir(), ".")
	if err != nil {
		t.Fatal(err)
	}

	if len(defs) != 0 {
		t.Fatalf("loadDefinitions loaded %d definitions; want 0", len(defs))
	}
}

func TestLoadDefinitionsReportsDirectoryErrors(t *testing.T) {
	workspaceFile := filepath.Join(t.TempDir(), "workspace")
	if err := os.WriteFile(workspaceFile, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := loadDefinitionsIn(workspaceFile, "."); err == nil || !strings.Contains(err.Error(), "open workspace root") {
		t.Fatalf("loadDefinitionsIn(workspace file, .) error = %v; want workspace open error", err)
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "cron"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := loadDefinitionsIn(workspace, "."); err == nil || !strings.Contains(err.Error(), "read cronjob directory") {
		t.Fatalf("loadDefinitionsIn(cron file, .) error = %v; want cron directory error", err)
	}
}

func TestStartStopLoadsCronjobsWithoutRunningFutureDuration(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, "cron")
	if err := os.Mkdir(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\nchannel: '#ops'\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(workspace, ".", []string{"#ops"}, newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		t.Fatal("future duration cronjob ran during start/stop test")
		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := m.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestStartRejectsAlreadyStartedManager(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, "cron")
	if err := os.Mkdir(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\nchannel: '#ops'\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(workspace, ".", []string{"#ops"}, newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		t.Fatal("future duration cronjob ran during duplicate start test")
		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := m.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "cronjob manager already started") {
		t.Fatalf("Start() error = %v; want already-started error", err)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := m.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestParseScheduleTimestamp(t *testing.T) {
	dueAt := "2026-05-21T15:04:05.123456789Z"

	schedule, err := parseSchedule(dueAt)
	if err != nil {
		t.Fatal(err)
	}

	if schedule.dueAt.Format(time.RFC3339Nano) != dueAt || schedule.duration != 0 || schedule.parsed != nil {
		t.Fatalf("schedule = %#v; want timestamp-only schedule", schedule)
	}
}

func TestOneOffCronjobRunsImmediatelyAndDeletesFile(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, "cron")
	if err := os.Mkdir(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cronPath := filepath.Join(cronDir, "due.md")
	if err := os.WriteFile(cronPath, []byte("---\nschedule: \"2000-01-01T00:00:00Z\"\nchannel: '#ops'\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	runDone := make(chan struct{})
	m := New(workspace, ".", []string{"#ops", "#triage"}, newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		close(runDone)

		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC) }

	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("one-off cronjob did not run")
	}

	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(cronPath); errors.Is(err, os.ErrNotExist) {
			stopCronManager(t, m)
			return
		}
	}

	t.Fatal("one-off cronjob file was not deleted")
}

func TestOneOffCronjobRunsAfterFutureDueTime(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, "cron")
	if err := os.Mkdir(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()

	cronPath := filepath.Join(cronDir, "future.md")
	if err := os.WriteFile(cronPath, []byte("---\nschedule: \""+now.Add(30*time.Millisecond).Format(time.RFC3339Nano)+"\"\nchannel: '#ops'\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	runDone := make(chan struct{})
	m := New(workspace, ".", []string{"#ops", "#triage"}, newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		close(runDone)

		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return now }

	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("future one-off cronjob did not run")
	}

	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(cronPath); errors.Is(err, os.ErrNotExist) {
			stopCronManager(t, m)
			return
		}
	}

	t.Fatal("future one-off cronjob file was not deleted")
}

func TestOneOffCronjobDeletesFileAfterRunError(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, "cron")
	if err := os.Mkdir(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cronPath := filepath.Join(cronDir, "error.md")
	if err := os.WriteFile(cronPath, []byte("---\nschedule: \"2000-01-01T00:00:00Z\"\nchannel: '#ops'\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	runDone := make(chan struct{})
	m := New(workspace, ".", []string{"#ops", "#triage"}, newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		close(runDone)

		return protocol.CronRunResult{}, errors.New("boom")
	}}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC) }

	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("one-off cronjob did not run")
	}

	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(cronPath); errors.Is(err, os.ErrNotExist) {
			stopCronManager(t, m)
			return
		}
	}

	t.Fatal("one-off cronjob file was not deleted after run error")
}

func stopCronManager(t *testing.T, m *Manager) {
	t.Helper()

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := m.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestScanScheduledUsesLatestDefinitionAndPersistsState(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, "cron")
	if err := os.Mkdir(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cronPath := filepath.Join(cronDir, "daily.md")
	if err := os.WriteFile(cronPath, []byte("---\nschedule: 1s\nagent: helper\nchannel: '#ops'\n---\nold body"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newCronScheduleStore(t)
	runPrompt := make(chan string, 1)
	m := New(workspace, ".", []string{"#ops"}, store, &runnerMock{RunFunc: func(_ context.Context, _ string, prompt string, _ *backend.RawRunProgress) (protocol.CronRunResult, error) {
		runPrompt <- prompt
		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	start := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	m.now = func() time.Time { return start }

	defs, err := loadDefinitionsIn(workspace, ".")
	if err != nil {
		t.Fatal(err)
	}

	if err := store.SyncCronSchedules(m.scheduledStates(defs, start), start); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(cronPath, []byte("---\nschedule: 1s\nagent: helper\nchannel: '#ops'\n---\nnew body"), 0o644); err != nil {
		t.Fatal(err)
	}

	m.now = func() time.Time { return start.Add(time.Second) }

	if err := m.scanScheduled(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case prompt := <-runPrompt:
		if !strings.Contains(prompt, "new body") {
			t.Fatalf("scheduled prompt = %q; want latest body", prompt)
		}
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

	store := newCronScheduleStore(t)
	runs := make(chan struct{}, 1)
	m := New(workspace, ".", []string{"#ops"}, store, &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		runs <- struct{}{}
		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))
	start := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	m.now = func() time.Time { return start }
	definitions, err := loadDefinitionsIn(workspace, ".")
	require.NoError(t, err)
	require.NoError(t, store.SyncCronSchedules(m.scheduledStates(definitions, start), start))

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
	if err := os.Mkdir(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule:\n  - 1s\n  - 1s\nagent: helper\nchannel: '#ops'\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newCronScheduleStore(t)
	runStarted := make(chan struct{}, 2)
	release := make(chan struct{})
	m := New(workspace, ".", []string{"#ops"}, store, &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		runStarted <- struct{}{}

		<-release

		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	start := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	m.now = func() time.Time { return start }

	defs, err := loadDefinitionsIn(workspace, ".")
	if err != nil {
		t.Fatal(err)
	}

	if err := store.SyncCronSchedules(m.scheduledStates(defs, start), start); err != nil {
		t.Fatal(err)
	}

	m.now = func() time.Time { return start.Add(time.Second) }
	if err := m.scanScheduled(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-runStarted:
	case <-time.After(time.Second):
		t.Fatal("scheduled cronjob did not run")
	}

	if err := m.scanScheduled(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-runStarted:
		t.Fatal("same-file scheduled cronjob overlapped or replayed backlog")
	default:
	}

	close(release)
	m.wg.Wait()
}

func TestPreparePromptInstructionCases(t *testing.T) {
	m := New(t.TempDir(), ".", nil, newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "already mentions tool", body: "Call " + backend.RawRunExposedToolName, want: "Call " + backend.RawRunExposedToolName},
		{name: "empty", body: "", want: humanVisibleEmptyCallInstruction},
		{name: "trailing newline", body: "Body\n", want: "Body\n\n" + humanVisibleEmptyCallInstruction},
		{name: "plain", body: "Body", want: "Body\n\n" + humanVisibleEmptyCallInstruction},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.preparePrompt(tt.body); got != tt.want {
				t.Fatalf("preparePrompt(%q) = %q; want %q", tt.body, got, tt.want)
			}
		})
	}
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
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("loadDefinition error = %v; want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadDefinitionLeavesNonStringAgentForValidation(t *testing.T) {
	def, err := loadDefinition([]byte("---\nschedule: 1h\nagent: 7\nchannel: '#ops'\n---\nBody"), "cron/test.md")
	if err != nil {
		t.Fatal(err)
	}

	if def.agent != "7" {
		t.Fatalf("definition agent = %q; want 7", def.agent)
	}
}

func TestLoadDefinitionDefaultsBlankAgent(t *testing.T) {
	def, err := loadDefinition([]byte("---\nschedule: 1h\nagent: '  \t  '\nchannel: '  #ops  '\n---\nBody"), "cron/test.md")
	if err != nil {
		t.Fatal(err)
	}

	if def.agent != "main" {
		t.Fatalf("definition agent = %q; want main", def.agent)
	}
}

func TestLoadOneOffCronjobValidatesTargetsAndPreparesPrompt(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, "cron")
	if err := os.Mkdir(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\nagent: helper\nchannel: '#triage'\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(workspace, ".", []string{"#ops", "#triage"}, newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	job, err := m.LoadOneOffCronjob("daily.md")
	if err != nil {
		t.Fatal(err)
	}

	if job.Agent != "helper" || job.RelativePath != "cron/daily.md" || job.TextChannel != "#triage" {
		t.Fatalf("job = %#v; want helper cron/daily.md #triage", job)
	}

	if !strings.Contains(job.Prompt, "Body") || !strings.Contains(job.Prompt, backend.RawRunExposedToolName) {
		t.Fatalf("prompt = %q; want body plus exposed tool instruction", job.Prompt)
	}

	for _, target := range []string{"", "nested/daily", "daily.txt", "daily.example", "."} {
		if _, err := m.LoadOneOffCronjob(target); err == nil {
			t.Fatalf("LoadOneOffCronjob(%q) succeeded; want error", target)
		}
	}
}

func TestLoadOneOffCronjobReportsReadAndDefinitionErrors(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, "cron")
	if err := os.Mkdir(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "broken.md"), []byte("not frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(workspace, ".", []string{"#ops", "#triage"}, newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	if _, err := m.LoadOneOffCronjob("missing"); err == nil || !strings.Contains(err.Error(), "read cronjob cron/missing.md") {
		t.Fatalf("LoadOneOffCronjob(missing) error = %v; want read cronjob error", err)
	}

	if _, err := m.LoadOneOffCronjob("broken"); err == nil || !strings.Contains(err.Error(), "yaml frontmatter is required") {
		t.Fatalf("LoadOneOffCronjob(broken) error = %v; want frontmatter error", err)
	}
}

func TestLoadOneOffCronjobReportsWorkspaceOpenError(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.WriteFile(workspace, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(workspace, ".", []string{"#ops", "#triage"}, newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	if _, err := m.LoadOneOffCronjob("daily"); err == nil || !strings.Contains(err.Error(), "open workspace root") {
		t.Fatalf("LoadOneOffCronjob(daily) error = %v; want workspace open error", err)
	}
}
