package cronjob

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge/harnessbridgetest"
	"github.com/stretchr/testify/require"
)

func newCronScheduleStore(t *testing.T) *harnessbridge.SessionService {
	t.Helper()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	if err != nil {
		t.Fatal(err)
	}

	store, err := harnessbridge.NewSessionServiceIn(t.TempDir(), ".", dsn, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		if err := store.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	})

	return store
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

	if _, err := os.Stat(filepath.Join(workspace, ".rocketclaw-stage", "state.sqlite3")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ValidateRuntimeDefinitions created state file err=%v; want not exist", err)
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

	broadcasts := make(chan events.Broadcast, 1)

	m := New(workspace, ".rocketclaw", nil, broadcasts, newCronScheduleStore(t), func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		t.Fatal("cronjob manager ran during load test")

		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))

	job, err := m.LoadOneOffCronjob("daily")
	if err != nil {
		t.Fatal(err)
	}

	if job.Agent != "helper" || job.RelativePath != "cron/daily.md" || job.TextChannel != "#ops" || !strings.Contains(job.Prompt, "Runtime cron") {
		t.Fatalf("LoadOneOffCronjob = %#v; want effective runtime cron", job)
	}
}

func TestRunOneOffCronjobSetsTraceConversationID(t *testing.T) {
	broadcasts := make(chan events.Broadcast, 1)

	m := New(t.TempDir(), ".", nil, broadcasts, newCronScheduleStore(t), func(_ context.Context, _, _ string, _ *slog.Logger, progress *harnessbridge.RawRunProgress) (RunResult, error) {
		if !strings.HasPrefix(progress.ConversationID, "one-off-cron:cron/daily.md:20000102T030405.000000006Z:") {
			t.Fatalf("ConversationID = %q; want one-off trace ID", progress.ConversationID)
		}

		if progress.TextChannel != "#ops" {
			t.Fatalf("TextChannel = %q; want #ops", progress.TextChannel)
		}

		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 6, time.UTC) }

	m.RunOneOffCronjob(t.Context(), OneOffCronjob{Agent: "helper", Prompt: "Body", RelativePath: "cron/daily.md", TextChannel: "#ops"}, nil, func(context.Context, RunResult, error) {})
}

func TestRunOneOffCronjobRejectsStoppedManager(t *testing.T) {
	broadcasts := make(chan events.Broadcast, 1)

	m := New(t.TempDir(), ".", nil, broadcasts, newCronScheduleStore(t), func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		t.Fatal("stopped cronjob manager ran one-off cronjob")

		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))

	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	stopCronManager(t, m)

	finished := false

	m.RunOneOffCronjob(t.Context(), OneOffCronjob{Agent: "helper", Prompt: "Body", RelativePath: "cron/daily.md"}, nil, func(_ context.Context, _ RunResult, err error) {
		finished = true

		if err == nil || !strings.Contains(err.Error(), "cronjob manager is stopped") {
			t.Fatalf("finish error = %v; want stopped manager", err)
		}
	})

	if !finished {
		t.Fatal("finish was not called")
	}
}

func TestExecuteJobSetsTraceConversationID(t *testing.T) {
	broadcasts := make(chan events.Broadcast, 1)

	m := New(t.TempDir(), ".", nil, broadcasts, newCronScheduleStore(t), func(_ context.Context, _, _ string, _ *slog.Logger, progress *harnessbridge.RawRunProgress) (RunResult, error) {
		if !strings.HasPrefix(progress.ConversationID, "cron:cron/daily.md:20000102T030405.000000006Z:") {
			t.Fatalf("ConversationID = %q; want scheduled trace ID", progress.ConversationID)
		}

		if progress.TextChannel != "#ops" {
			t.Fatalf("TextChannel = %q; want #ops", progress.TextChannel)
		}

		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 6, time.UTC) }

	m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", textChannel: "#ops", body: "Body"})
}

func TestExecuteJobDeliversVisibleOutputToRequiredChannel(t *testing.T) {
	broadcasts := make(chan events.Broadcast, 1)

	m := New(t.TempDir(), ".", nil, broadcasts, newCronScheduleStore(t), func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		return RunResult{
			VerbatimMessage: "visible answer",
			Attachments:     []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}},
		}, nil
	}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC) }

	done := make(chan struct{})

	go func() {
		m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", textChannel: "#ops", body: "Body"})
		close(done)
	}()

	broadcast := <-broadcasts
	broadcast.Delivery.MarkDelivered(nil)
	<-done
	require.Equal(t, "#ops", broadcast.Message.SlackReply.ChannelID)
	require.Equal(t, "cron/daily.md", broadcast.Message.Cronjob.RelativePath)
	require.Equal(t, "helper", broadcast.Message.Cronjob.Agent)
	require.Equal(t, "2000-01-02T03:04:05Z", broadcast.Message.Cronjob.RanAt)
	require.Equal(t, "visible answer", broadcast.Message.Text)
	require.Len(t, broadcast.Message.Attachments, 1)
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

	broadcasts := make(chan events.Broadcast, 1)

	m := New(workspace, ".", []string{"#ops"}, broadcasts, newCronScheduleStore(t), func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		t.Fatal("future duration cronjob ran during start/stop test")
		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))

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

	broadcasts := make(chan events.Broadcast, 1)

	m := New(workspace, ".", []string{"#ops"}, broadcasts, newCronScheduleStore(t), func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		t.Fatal("future duration cronjob ran during duplicate start test")
		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))

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

	broadcasts := make(chan events.Broadcast, 1)

	runDone := make(chan struct{})
	m := New(workspace, ".", []string{"#ops", "#triage"}, broadcasts, newCronScheduleStore(t), func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		close(runDone)

		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))
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

	broadcasts := make(chan events.Broadcast, 1)

	runDone := make(chan struct{})
	m := New(workspace, ".", []string{"#ops", "#triage"}, broadcasts, newCronScheduleStore(t), func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		close(runDone)

		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))
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

	broadcasts := make(chan events.Broadcast, 1)

	runDone := make(chan struct{})
	m := New(workspace, ".", []string{"#ops", "#triage"}, broadcasts, newCronScheduleStore(t), func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		close(runDone)

		return RunResult{}, errors.New("boom")
	}, slog.New(slog.DiscardHandler))
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

	broadcasts := make(chan events.Broadcast, 1)

	store := newCronScheduleStore(t)
	runPrompt := make(chan string, 1)
	m := New(workspace, ".", []string{"#ops"}, broadcasts, store, func(_ context.Context, _ string, prompt string, _ *slog.Logger, _ *harnessbridge.RawRunProgress) (RunResult, error) {
		runPrompt <- prompt
		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))

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

	broadcasts := make(chan events.Broadcast, 1)

	store := newCronScheduleStore(t)
	runs := make(chan struct{}, 1)
	m := New(workspace, ".", []string{"#ops"}, broadcasts, store, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		runs <- struct{}{}
		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))
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

	broadcasts := make(chan events.Broadcast, 1)

	store := newCronScheduleStore(t)
	runStarted := make(chan struct{}, 2)
	release := make(chan struct{})
	m := New(workspace, ".", []string{"#ops"}, broadcasts, store, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		runStarted <- struct{}{}

		<-release

		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))

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
	broadcasts := make(chan events.Broadcast, 1)

	m := New(t.TempDir(), ".", nil, broadcasts, newCronScheduleStore(t), func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "already mentions tool", body: "Call " + harnessbridge.RawRunExposedToolName, want: "Call " + harnessbridge.RawRunExposedToolName},
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

func TestExecuteJobWithTextChannelSendsThreadOnlyFinalPayload(t *testing.T) {
	broadcasts := make(chan events.Broadcast, 1)

	m := New(t.TempDir(), ".", nil, broadcasts, newCronScheduleStore(t), func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		return RunResult{Text: "internal note", VerbatimMessage: " final payload ", Attachments: []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}}, nil
	}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC) }

	done := make(chan struct{})

	go func() {
		m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", textChannel: "#triage", body: "Body"})
		close(done)
	}()

	broadcast := <-broadcasts
	broadcast.Delivery.MarkDelivered(nil)
	<-done
	require.Equal(t, "#triage", broadcast.Message.SlackReply.ChannelID)
	require.Equal(t, "final payload", broadcast.Message.Text)
	require.Len(t, broadcast.Message.Attachments, 1)
	require.Equal(t, "report.txt", broadcast.Message.Attachments[0].Name)
}

func TestExecuteJobWithTextChannelSkipsEmptyFinalPayload(t *testing.T) {
	broadcasts := make(chan events.Broadcast, 1)

	m := New(t.TempDir(), ".", nil, broadcasts, newCronScheduleStore(t), func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		return RunResult{Text: "internal note"}, nil
	}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC) }

	m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", textChannel: "#triage", body: "Body"})

	select {
	case <-broadcasts:
		t.Fatal("broadcast sent for empty final payload")
	default:
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

	broadcasts := make(chan events.Broadcast, 1)

	m := New(workspace, ".", []string{"#ops", "#triage"}, broadcasts, newCronScheduleStore(t), func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))

	job, err := m.LoadOneOffCronjob("daily.md")
	if err != nil {
		t.Fatal(err)
	}

	if job.Agent != "helper" || job.RelativePath != "cron/daily.md" || job.TextChannel != "#triage" {
		t.Fatalf("job = %#v; want helper cron/daily.md #triage", job)
	}

	if !strings.Contains(job.Prompt, "Body") || !strings.Contains(job.Prompt, harnessbridge.RawRunExposedToolName) {
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

	broadcasts := make(chan events.Broadcast, 1)

	m := New(workspace, ".", []string{"#ops", "#triage"}, broadcasts, newCronScheduleStore(t), func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))

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

	broadcasts := make(chan events.Broadcast, 1)

	m := New(workspace, ".", []string{"#ops", "#triage"}, broadcasts, newCronScheduleStore(t), func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))

	if _, err := m.LoadOneOffCronjob("daily"); err == nil || !strings.Contains(err.Error(), "open workspace root") {
		t.Fatalf("LoadOneOffCronjob(daily) error = %v; want workspace open error", err)
	}
}
