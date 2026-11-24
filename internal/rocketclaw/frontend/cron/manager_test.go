package cronfrontend

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

func newCronScheduleStore(t *testing.T) *backend.SessionService {
	t.Helper()

	store, _ := newCronScheduleStoreDB(t)

	return store
}

func newCronScheduleStoreDB(t *testing.T) (*backend.SessionService, *sql.DB) {
	t.Helper()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	if err != nil {
		t.Fatal(err)
	}

	store, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := store.Stop(); err != nil {
			t.Fatal(err)
		}
	})

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	return store, db
}

func TestJobsProjectsDefinitionsAndPersistedTriggers(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll("runtime/cron", 0o700))
	require.NoError(t, root.WriteFile("runtime/cron/alpha.md", []byte("---\nschedule: [30m, '0 * * * *']\nagent: worker\n---\nExact body\n"), 0o600))
	store := newCronScheduleStore(t)
	cfg := newCronConfig(t, workspace, "runtime")
	manager := New(cfg, "runtime", store, &runnerMock{}, slog.New(slog.DiscardHandler))
	start := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return start.Add(5 * time.Minute) }
	definitions, err := loadDefinitionsIn(cfg, "runtime")
	require.NoError(t, err)

	states := manager.scheduledStates(definitions, start)
	require.NoError(t, store.SyncCronSchedules(states))

	jobs, err := manager.Jobs()
	require.NoError(t, err)
	require.Equal(t, []Job{
		{RelativePath: "cron/alpha.md", Agent: "worker", Body: "Exact body\n", Schedules: []string{"30m", "0 * * * *"}, Upcoming: []time.Time{start.Add(30 * time.Minute), start.Add(time.Hour)}},
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

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule:\n  - 15m\n  - '0 8 * * *'\nagent: worker\n---\nRun daily\n"), 0o644); err != nil {
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

	defs, err := loadDefinitionsIn(newCronConfig(t, workspace, "."), ".")
	if err != nil {
		t.Fatal(err)
	}

	if len(defs) != 1 {
		t.Fatalf("loadDefinitions loaded %d definitions; want 1", len(defs))
	}

	def := defs[0]
	if def.Job.RelativePath != "cron/daily.md" || def.Job.Agent != "worker" || def.Job.Prompt != "Run daily\n" {
		t.Fatalf("definition = %#v; want daily worker body", def)
	}

	now := time.Date(2026, time.May, 21, 0, 0, 0, 0, time.UTC)
	if len(def.Schedules) != 2 || !def.Schedules[0].Next(now).Equal(now.Add(15*time.Minute)) || !def.Schedules[1].Next(now).Equal(now.Add(8*time.Hour)) {
		t.Fatalf("schedules = %#v; want duration and cron", def.Schedules)
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

	if err := ValidateRuntimeDefinitions(newCronConfig(t, workspace, ".rocketclaw-stage"), ".rocketclaw-stage"); err == nil || !strings.Contains(err.Error(), "yaml frontmatter is required") {
		t.Fatalf("ValidateRuntimeDefinitions() error = %v; want frontmatter error", err)
	}

	m := New(newCronConfig(t, workspace, ".rocketclaw-stage"), ".rocketclaw-stage", newCronScheduleStore(t), &runnerMock{}, slog.New(slog.DiscardHandler))
	require.ErrorContains(t, m.Start(t.Context()), "yaml frontmatter is required")
	require.ErrorContains(t, m.Refresh(), "yaml frontmatter is required")
}

func TestLoadOneOffCronjobUsesEffectiveRuntimeCron(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, ".rocketclaw", "cron")
	if err := os.MkdirAll(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\nagent: helper\n---\nRuntime cron"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(newCronConfig(t, workspace, ".rocketclaw"), ".rocketclaw", newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		t.Fatal("cronjob manager ran during load test")

		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	job, err := m.LoadOneOffCronjob("daily")
	if err != nil {
		t.Fatal(err)
	}

	if job.Agent != "helper" || job.RelativePath != "cron/daily.md" || !strings.Contains(job.Prompt, "Runtime cron") {
		t.Fatalf("LoadOneOffCronjob = %#v; want effective runtime cron", job)
	}
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
	var (
		sources     []string
		logs        bytes.Buffer
		destination string
	)

	m := New(newCronConfig(t, t.TempDir(), "."), ".", newCronScheduleStore(t), &runnerMock{RunFunc: func(_ context.Context, agent, prompt string, progress *backend.RawRunProgress) (protocol.CronRunResult, error) {
		require.Equal(t, "helper", agent)
		require.Equal(t, "Body", prompt)
		require.Equal(t, destination, progress.SyncDestination)
		require.Equal(t, &protocol.CronjobMessage{RelativePath: "cron/daily.md", Agent: "helper", RanAt: "2000-01-02T03:04:05Z", Space: "Ops"}, progress.Cronjob, "a run from Web carries the cron's space")

		sources = append(sources, progress.ConversationID)
		if !strings.HasPrefix(progress.ConversationID, "one-off-cron:cron/daily.md:20000102T030405.000000006Z:") {
			t.Fatalf("ConversationID = %q; want one-off trace ID", progress.ConversationID)
		}

		return protocol.CronRunResult{ConversationID: progress.SyncDestination, Reported: progress.SyncDestination != ""}, nil
	}}, slog.New(slog.NewJSONHandler(&logs, nil)))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 6, time.UTC) }

	for _, destination = range []string{"slack-thread:C1:1.2", ""} {
		_, err := m.RunOneOffCronjob(t.Context(), &protocol.OneOffCronjob{Agent: "helper", Space: "Ops", Prompt: "Body", RelativePath: "cron/daily.md", ConversationID: destination})
		require.NoError(t, err)
	}

	require.Len(t, sources, 2)
	require.NotEqual(t, sources[0], sources[1])
	require.Equal(t, 2, strings.Count(logs.String(), `"event":"cron_completed"`))
	require.Equal(t, 1, strings.Count(logs.String(), `"outcome":"reported"`))
	require.Equal(t, 1, strings.Count(logs.String(), `"outcome":"intentional_silence"`))
	require.Contains(t, logs.String(), `"duration_ms":`)
}

func TestRunOneOffCronjobRejectsStoppedManager(t *testing.T) {
	m := New(newCronConfig(t, t.TempDir(), "."), ".", newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
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
	var logs bytes.Buffer

	workspace := t.TempDir()
	m := New(newCronConfig(t, workspace, "."), ".", newCronScheduleStore(t), &runnerMock{RunFunc: func(_ context.Context, _, _ string, progress *backend.RawRunProgress) (protocol.CronRunResult, error) {
		if !strings.HasPrefix(progress.ConversationID, "cron:cron/daily.md:20000102T030405.000000006Z:") {
			t.Fatalf("ConversationID = %q; want scheduled trace ID", progress.ConversationID)
		}

		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.NewJSONHandler(&logs, nil)))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 6, time.UTC) }

	writeHelperAgent(t, workspace)

	m.executeJob(t.Context(), &config.CronDefinition{Job: protocol.OneOffCronjob{RelativePath: "cron/daily.md", Agent: "helper", Prompt: "Body"}})
	require.Contains(t, logs.String(), `"event":"cron_completed"`)
	require.Contains(t, logs.String(), `"outcome":"intentional_silence"`)
	require.Contains(t, logs.String(), `"duration_ms":`)
	logs.Reset()

	m.run = &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		return protocol.CronRunResult{Reported: true}, nil
	}}
	m.executeJob(t.Context(), &config.CronDefinition{Job: protocol.OneOffCronjob{RelativePath: "cron/daily.md", Agent: "helper", Prompt: "Body"}})
	require.Contains(t, logs.String(), `"outcome":"reported"`)
	logs.Reset()

	m.run = &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		return protocol.CronRunResult{}, errors.New("secret-bearing-error")
	}}
	m.executeJob(t.Context(), &config.CronDefinition{Job: protocol.OneOffCronjob{RelativePath: "cron/daily.md", Agent: "helper", Prompt: "Body"}})
	require.Contains(t, logs.String(), `"outcome":"failed"`)
	require.NotContains(t, logs.String(), "secret-bearing-error")
}

func TestLoadDefinitionsWithoutCronDirectory(t *testing.T) {
	defs, err := loadDefinitionsIn(newCronConfig(t, t.TempDir(), "."), ".")
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

	if _, err := loadDefinitionsIn(config.NewLockedConfig(&config.Config{Workspace: workspaceFile}), "."); err == nil || !strings.Contains(err.Error(), "open workspace root") {
		t.Fatalf("loadDefinitionsIn(workspace file, .) error = %v; want workspace open error", err)
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "cron"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := loadDefinitionsIn(newCronConfig(t, workspace, "."), "."); err == nil || !strings.Contains(err.Error(), "read cronjob directory") {
		t.Fatalf("loadDefinitionsIn(cron file, .) error = %v; want cron directory error", err)
	}
}

func stopCronManager(t *testing.T, m *Manager) {
	t.Helper()

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := m.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestScanScheduledRunsLoadedDefinitionUntilRefresh(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, "cron")
	if err := os.Mkdir(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cronPath := filepath.Join(cronDir, "daily.md")
	if err := os.WriteFile(cronPath, []byte("---\nschedule: 1s\nagent: helper\n---\nold body"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, db := newCronScheduleStoreDB(t)
	runPrompt := make(chan string, 1)
	m := New(newCronConfig(t, workspace, "."), ".", store, &runnerMock{RunFunc: func(_ context.Context, _ string, prompt string, _ *backend.RawRunProgress) (protocol.CronRunResult, error) {
		runPrompt <- prompt
		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))
	writeHelperAgent(t, workspace)

	var (
		clockMu sync.Mutex
		elapsed time.Duration
	)

	start := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	m.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()

		return start.Add(elapsed)
	}
	require.NoError(t, m.Start(t.Context()))
	t.Cleanup(func() { stopCronManager(t, m) })

	scan := func(at time.Duration, want string) {
		t.Helper()

		clockMu.Lock()
		elapsed = at
		clockMu.Unlock()

		require.NoError(t, m.scanScheduled(t.Context()))

		select {
		case prompt := <-runPrompt:
			require.Equal(t, want+"\n\n"+cronReplyInstruction, prompt)
		case <-time.After(time.Second):
			t.Fatal("scheduled cronjob did not run")
		}
	}

	require.NoError(t, os.WriteFile(cronPath, []byte("---\nschedule: 2s\nagent: helper\n---\nnew body"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cronDir, "broken.md"), []byte("not frontmatter"), 0o644))
	scan(10*time.Second, "old body")
	require.ErrorContains(t, m.Refresh(), "yaml frontmatter is required")
	scan(20*time.Second, "old body")

	require.NoError(t, os.Remove(filepath.Join(cronDir, "broken.md")))
	_, err := db.ExecContext(t.Context(), `ALTER TABLE schedules ADD CONSTRAINT refuse_edit CHECK (id <> 'cron/daily.md#0#2s')`)
	require.NoError(t, err)
	require.ErrorContains(t, m.Refresh(), "sync cron schedules")
	scan(30*time.Second, "old body")

	_, err = db.ExecContext(t.Context(), `ALTER TABLE schedules DROP CONSTRAINT refuse_edit`)
	require.NoError(t, err)
	require.NoError(t, m.Refresh())
	scan(40*time.Second, "new body")

	triggers, err := store.DueCronSchedules(start.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, []backend.CronScheduleState{{ScheduleID: "cron/daily.md#0#2s", RelativePath: "cron/daily.md", NextDue: start.Add(42 * time.Second)}}, triggers)
}

func TestScanScheduledCoalescesSameFileAndNoBacklog(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, "cron")
	if err := os.Mkdir(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule:\n  - 1s\n  - 1s\nagent: helper\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, db := newCronScheduleStoreDB(t)
	runStarted := make(chan error, 2)
	release := make(chan struct{})
	m := New(newCronConfig(t, workspace, "."), ".", store, &runnerMock{RunFunc: func(ctx context.Context, _, _ string, progress *backend.RawRunProgress) (protocol.CronRunResult, error) {
		_, errConversation := db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, cron_path) VALUES ($1, 'helper', 'cron', 'cron_run', FALSE, $2)`, progress.ConversationID, progress.Run.Path)

		_, errTurn := db.ExecContext(ctx, `INSERT INTO turns (id, conversation_id, created_at_unix_ns, updated_at_unix_ns) VALUES ($1, $1, 0, 0)`, progress.ConversationID)
		runStarted <- errors.Join(errConversation, errTurn)

		<-release

		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))
	writeHelperAgent(t, workspace)

	start := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	m.now = func() time.Time { return start }
	require.NoError(t, m.Refresh())

	m.now = func() time.Time { return start.Add(time.Second) }
	if err := m.scanScheduled(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-runStarted:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("scheduled cronjob did not run")
	}

	if err := m.scanScheduled(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-runStarted:
		t.Fatal("same-file scheduled cronjob overlapped or replayed backlog", err)
	default:
	}

	close(release)
	m.wg.Wait()
}

func TestParseCronDefinitionRejectsInvalidFrontmatter(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "missing frontmatter", data: "body", want: "yaml frontmatter is required"},
		{name: "missing closing delimiter", data: "---\nschedule: 1h\nbody", want: "yaml frontmatter closing delimiter is required"},
		{name: "invalid yaml", data: "---\nschedule: [\n---\nbody", want: "unmarshal frontmatter yaml"},
		{name: "missing schedule", data: "---\nagent: main\n---\nbody", want: "schedule is required"},
		{name: "null schedule", data: "---\nschedule: null\n---\nbody", want: "schedule is required"},
		{name: "invalid schedule list item", data: "---\nschedule:\n  - 1h\n  - 3\n---\nbody", want: "schedule must be a string or list of strings"},
		{name: "invalid schedule scalar", data: "---\nschedule: 123\n---\nbody", want: "schedule must be a string or list of strings"},
		{name: "blank duration", data: "---\nschedule: ''\n---\nbody", want: "schedule must not be blank"},
		{name: "zero duration", data: "---\nschedule: 0s\n---\nbody", want: "duration schedules must be greater than zero"},
		{name: "every unsupported", data: "---\nschedule: '@every 1h'\n---\nbody", want: "@every is not supported"},
		{name: "invalid cron", data: "---\nschedule: not a cron\n---\nbody", want: "invalid cron expression"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := config.ParseCronDefinition([]byte(tt.data), "cron/test.md")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseCronDefinition error = %v; want containing %q", err, tt.want)
			}
		})
	}
}

func TestParseCronDefinitionLeavesNonStringAgentForValidation(t *testing.T) {
	job, _, err := config.ParseCronDefinition([]byte("---\nschedule: 1h\nagent: 7\n---\nBody"), "cron/test.md")
	if err != nil {
		t.Fatal(err)
	}

	if job.Agent != "7" {
		t.Fatalf("definition agent = %q; want 7", job.Agent)
	}
}

func TestParseCronDefinitionLeavesBlankAgentEmpty(t *testing.T) {
	_, schedules, err := config.ParseCronDefinition([]byte("---\nschedule: []\n---\nBody"), "cron/test.md")
	require.NoError(t, err)
	require.NotNil(t, schedules)
	require.Empty(t, schedules)

	for _, frontmatter := range []string{"schedule: 1h", "schedule: 1h\nagent: '  \t  '", "schedule: 1h\nagent: null"} {
		job, _, err := config.ParseCronDefinition([]byte("---\n"+frontmatter+"\n---\nBody"), "cron/test.md")
		require.NoError(t, err)
		require.Empty(t, job.Agent, frontmatter)
	}
}

func TestExecuteJobRefusesCronsItCannotRun(t *testing.T) {
	workspace := t.TempDir()
	cfg := newCronConfig(t, workspace, ".")
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "cron"), 0o755))

	for name, agent := range map[string]string{
		"easy":    "---\nmode: easy\nmodel: gpt-6.1-sol\nspace: Ops\nworkingDirectory: data\n---\nPrompt\n",
		"mine":    "---\nmodel: gpt-6.1-sol\nsticky: user\n---\nPrompt\n",
		"channel": "---\nmodel: gpt-6.1-sol\nsticky: slack\n---\nPrompt\n",
		"shared":  "---\nmodel: gpt-6.1-sol\nsticky: global\nspace: Marketing\n---\nPrompt\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", name+".md"), []byte(agent), 0o644))
	}

	for name, frontmatter := range map[string]string{
		"blank": "", "gone": "agent: gone\n", "easy": "agent: easy\n", "mine": "agent: mine\n", "channel": "agent: channel\n", "elsewhere": "agent: shared\nspace: Finance\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(workspace, "cron", name+".md"), []byte("---\nschedule: 1h\n"+frontmatter+"---\nBody"), 0o644))
	}

	require.NoError(t, ValidateRuntimeDefinitions(cfg, "."))

	definitions, err := loadDefinitionsIn(cfg, ".")
	require.NoError(t, err)
	require.Len(t, definitions, 6, "a cron whose agent it cannot run still loads")

	var logs bytes.Buffer

	m := New(cfg, ".", newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		t.Fatal("a cron whose agent it cannot run ran")

		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.NewJSONHandler(&logs, nil)))

	for i := range definitions {
		m.executeJob(t.Context(), &definitions[i])
	}

	for _, refusal := range []string{
		"cron/blank.md names no agent",
		"cron/gone.md uses agent gone, which is not loaded",
		"cron/easy.md uses agent easy, which is an easy agent scheduled with its routines",
		"cron/mine.md uses agent mine, which is sticky: user",
		"cron/channel.md uses agent channel, which is sticky: slack",
		"cron/elsewhere.md uses agent shared, which is sticky: global in space Marketing, not Finance",
	} {
		require.Contains(t, logs.String(), `"error":"`+refusal+`"`)
	}
}

func TestLoadOneOffCronjobValidatesTargetsAndPreparesPrompt(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, "cron")
	if err := os.Mkdir(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\nagent: helper\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(newCronConfig(t, workspace, "."), ".", newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	job, err := m.LoadOneOffCronjob("daily.md")
	if err != nil {
		t.Fatal(err)
	}

	if job.Agent != "helper" || job.RelativePath != "cron/daily.md" {
		t.Fatalf("job = %#v; want helper cron/daily.md", job)
	}

	if !strings.Contains(job.Prompt, "Body") || !strings.Contains(job.Prompt, cronReplyInstruction) {
		t.Fatalf("prompt = %q; want body plus exposed tool instruction", job.Prompt)
	}

	for _, tt := range []struct {
		name, body, want string
	}{
		{name: "empty", body: "", want: cronReplyInstruction},
		{name: "trailing newline", body: "Body\n", want: "Body\n\n" + cronReplyInstruction},
		{name: "several trailing newlines", body: "Body\n\n\n", want: "Body\n\n\n\n" + cronReplyInstruction},
		{name: "CRLF", body: "Body\r\n", want: "Body\r\n\n" + cronReplyInstruction},
		{name: "plain", body: "Body", want: "Body\n\n" + cronReplyInstruction},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.preparePrompt(tt.body); got != tt.want {
				t.Fatalf("preparePrompt(%q) = %q; want %q", tt.body, got, tt.want)
			}
		})
	}

	for _, target := range []string{"", "nested/daily", "daily.txt", "daily.example", "daily.example.md", "."} {
		if _, err := m.LoadOneOffCronjob(target); err == nil {
			t.Fatalf("LoadOneOffCronjob(%q) succeeded; want error", target)
		}
	}

	if err := os.WriteFile(filepath.Join(cronDir, "broken.md"), []byte("not frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}

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

	m := New(config.NewLockedConfig(&config.Config{Workspace: workspace}), ".", newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	if _, err := m.LoadOneOffCronjob("daily"); err == nil || !strings.Contains(err.Error(), "open workspace root") {
		t.Fatalf("LoadOneOffCronjob(daily) error = %v; want workspace open error", err)
	}
}

func newCronConfig(t *testing.T, workspace, runtimeDir string) *config.LockedConfig {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, runtimeDir, "agents"), 0o755))

	return config.NewLockedConfig(&config.Config{Workspace: workspace})
}

func writeHelperAgent(t *testing.T, workspace string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "helper.md"), []byte("---\nmodel: gpt-6.1-sol\n---\nHelp.\n"), 0o644))
}

const easyRoutineAgent = "---\nmode: easy\nmodel: gpt-6.1-sol\nspace: Ops\nworkingDirectory: data\nroutines:\n"

func TestRoutinesRunAsCronDefinitions(t *testing.T) {
	workspace := t.TempDir()
	cfg := newCronConfig(t, workspace, ".")
	agentPath := filepath.Join(workspace, "agents", "marketing.md")
	require.NoError(t, os.WriteFile(agentPath, []byte(easyRoutineAgent+"  - '*/5 * * * *': Check X.\n  - 30m\n---\nPrompt\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "agents", "marketing"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "marketing", "twin.md"), []byte(easyRoutineAgent+"  - 1h\n---\nPrompt\n"), 0o644))

	definitions, err := loadDefinitionsIn(cfg, ".")
	require.NoError(t, err)
	require.Len(t, definitions, 2)

	var runs []string

	store := newCronScheduleStore(t)
	m := New(cfg, ".", store, &runnerMock{RunFunc: func(_ context.Context, agent, prompt string, progress *backend.RawRunProgress) (protocol.CronRunResult, error) {
		runs = append(runs, agent+"|"+prompt+"|"+progress.Run.Path+"|"+progress.Cronjob.Space)
		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	for i := range definitions {
		m.executeJob(t.Context(), &definitions[i])
	}

	require.Equal(t, []string{
		"marketing|Check X.\n\n" + cronReplyInstruction + "|agents/marketing.md#*/5 * * * *|Ops",
		"marketing|Scheduled run (30m).\n\n" + cronReplyInstruction + "|agents/marketing.md#30m|Ops",
	}, runs)

	job, err := m.LoadOneOffCronjob("marketing · 30m")
	require.NoError(t, err)
	require.Equal(t, protocol.OneOffCronjob{RelativePath: "agents/marketing.md#30m", Agent: "marketing", Space: "Ops", Prompt: "Scheduled run (30m).\n\n" + cronReplyInstruction}, job)

	start := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, store.SyncCronSchedules(m.scheduledStates(definitions, start)))
	before, err := store.DueCronSchedules(start.Add(24 * time.Hour))
	require.NoError(t, err)
	require.Len(t, before, 2)
	require.NoError(t, os.WriteFile(agentPath, []byte(easyRoutineAgent+"  - 30m\n  - '*/5 * * * *': Check X.\n---\nPrompt\n"), 0o644))

	reordered, err := loadDefinitionsIn(cfg, ".")
	require.NoError(t, err)
	require.NoError(t, store.SyncCronSchedules(m.scheduledStates(reordered, start.Add(time.Minute))))
	after, err := store.DueCronSchedules(start.Add(24 * time.Hour))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestValidateRuntimeDefinitionsRefusesBadRoutines(t *testing.T) {
	for _, tc := range []struct {
		name, agent, want string
	}{
		{"duplicate schedule", easyRoutineAgent + "  - 30m\n  - 30m: Again.\n---\nPrompt\n", `agents/marketing.md: routines: schedule "30m" is listed twice`},
		{"phrase", easyRoutineAgent + "  - every 30min\n---\nPrompt\n", `agents/marketing.md: routines: schedule "every 30min": invalid cron expression`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			cfg := newCronConfig(t, workspace, ".")
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "marketing.md"), []byte(tc.agent), 0o644))

			require.ErrorContains(t, ValidateRuntimeDefinitions(cfg, "."), tc.want)
		})
	}
}
