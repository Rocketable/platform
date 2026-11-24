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
	cfg := newCronConfig(t, workspace, "runtime")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "runtime", "agents", "worker.md"), []byte("---\nmodel: gpt-6.1-sol\nroutines:\n  - 30m: Exact body\n  - '0 * * * *'\n---\nWork.\n"), 0o600))
	store := newCronScheduleStore(t)
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
		{RelativePath: "agents/worker.md#30m", Agent: "worker", Body: "Exact body", Schedule: "30m", Upcoming: []time.Time{start.Add(30 * time.Minute)}},
		{RelativePath: "agents/worker.md#0 * * * *", Agent: "worker", Body: "Scheduled run (0 * * * *).", Schedule: "0 * * * *", Upcoming: []time.Time{start.Add(time.Hour)}},
	}, jobs)
	remaining, err := store.DueCronSchedules(start.Add(24 * time.Hour))
	require.NoError(t, err)
	require.ElementsMatch(t, states, remaining)
}

func TestStartAndRefreshReportBadRoutines(t *testing.T) {
	workspace := t.TempDir()
	cfg := newCronConfig(t, workspace, ".rocketclaw-stage")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".rocketclaw-stage", "agents", "broken.md"), []byte("---\nmodel: gpt-6.1-sol\nroutines: [every 30min]\n---\nPrompt\n"), 0o644))

	m := New(cfg, ".rocketclaw-stage", newCronScheduleStore(t), &runnerMock{}, slog.New(slog.DiscardHandler))
	require.ErrorContains(t, m.Start(t.Context()), "invalid cron expression")
	require.ErrorContains(t, m.Refresh(), "invalid cron expression")
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

	m := New(newCronConfig(t, t.TempDir(), "."), ".", newCronScheduleStore(t), &runnerMock{RunFunc: func(_ context.Context, agent, prompt string, progress *backend.CronRunProgress) (protocol.CronRunResult, error) {
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
	m := New(newCronConfig(t, t.TempDir(), "."), ".", newCronScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.CronRunProgress) (protocol.CronRunResult, error) {
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
	m := New(newCronConfig(t, workspace, "."), ".", newCronScheduleStore(t), &runnerMock{RunFunc: func(_ context.Context, _, _ string, progress *backend.CronRunProgress) (protocol.CronRunResult, error) {
		if !strings.HasPrefix(progress.ConversationID, "cron:cron/daily.md:20000102T030405.000000006Z:") {
			t.Fatalf("ConversationID = %q; want scheduled trace ID", progress.ConversationID)
		}

		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.NewJSONHandler(&logs, nil)))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 6, time.UTC) }

	m.executeJob(t.Context(), &config.CronDefinition{Job: protocol.OneOffCronjob{RelativePath: "cron/daily.md", Agent: "helper", Prompt: "Body"}})
	require.Contains(t, logs.String(), `"event":"cron_completed"`)
	require.Contains(t, logs.String(), `"outcome":"intentional_silence"`)
	require.Contains(t, logs.String(), `"duration_ms":`)
	logs.Reset()

	m.run = &runnerMock{RunFunc: func(context.Context, string, string, *backend.CronRunProgress) (protocol.CronRunResult, error) {
		return protocol.CronRunResult{Reported: true}, nil
	}}
	m.executeJob(t.Context(), &config.CronDefinition{Job: protocol.OneOffCronjob{RelativePath: "cron/daily.md", Agent: "helper", Prompt: "Body"}})
	require.Contains(t, logs.String(), `"outcome":"reported"`)
	logs.Reset()

	m.run = &runnerMock{RunFunc: func(context.Context, string, string, *backend.CronRunProgress) (protocol.CronRunResult, error) {
		return protocol.CronRunResult{}, errors.New("secret-bearing-error")
	}}
	m.executeJob(t.Context(), &config.CronDefinition{Job: protocol.OneOffCronjob{RelativePath: "cron/daily.md", Agent: "helper", Prompt: "Body"}})
	require.Contains(t, logs.String(), `"outcome":"failed"`)
	require.NotContains(t, logs.String(), "secret-bearing-error")
}

func TestLoadDefinitionsReportsAgentLoadErrors(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.WriteFile(workspace, []byte("not a directory"), 0o644))

	cfg := config.NewLockedConfig(&config.Config{Workspace: workspace})
	_, err := loadDefinitionsIn(cfg, ".")
	require.ErrorContains(t, err, "load agents")

	_, err = New(cfg, ".", newCronScheduleStore(t), &runnerMock{}, slog.New(slog.DiscardHandler)).LoadOneOffCronjob("helper · 1h")
	require.ErrorContains(t, err, "load agents")
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
	cfg := newCronConfig(t, workspace, ".")
	agentPath := filepath.Join(workspace, "agents", "helper.md")
	require.NoError(t, os.WriteFile(agentPath, []byte("---\nmodel: gpt-6.1-sol\nroutines:\n  - 1s: old body\n---\nHelp.\n"), 0o644))

	store, db := newCronScheduleStoreDB(t)
	runPrompt := make(chan string, 1)
	m := New(cfg, ".", store, &runnerMock{RunFunc: func(_ context.Context, _ string, prompt string, _ *backend.CronRunProgress) (protocol.CronRunResult, error) {
		runPrompt <- prompt
		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

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
			t.Fatal("scheduled routine did not run")
		}
	}

	require.NoError(t, os.WriteFile(agentPath, []byte("---\nmodel: gpt-6.1-sol\nroutines:\n  - 2s: new body\n---\nHelp.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "broken.md"), []byte("---\nmodel: gpt-6.1-sol\nroutines: [every 30min]\n---\nPrompt\n"), 0o644))
	scan(10*time.Second, "old body")
	require.ErrorContains(t, m.Refresh(), "invalid cron expression")
	scan(20*time.Second, "old body")

	require.NoError(t, os.Remove(filepath.Join(workspace, "agents", "broken.md")))
	_, err := db.ExecContext(t.Context(), `ALTER TABLE schedules ADD CONSTRAINT refuse_edit CHECK (id <> 'agents/helper.md#2s')`)
	require.NoError(t, err)
	require.ErrorContains(t, m.Refresh(), "sync cron schedules")
	scan(30*time.Second, "old body")

	_, err = db.ExecContext(t.Context(), `ALTER TABLE schedules DROP CONSTRAINT refuse_edit`)
	require.NoError(t, err)
	require.NoError(t, m.Refresh())
	scan(40*time.Second, "new body")

	triggers, err := store.DueCronSchedules(start.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, []backend.CronScheduleState{{ScheduleID: "agents/helper.md#2s", RelativePath: "agents/helper.md#2s", NextDue: start.Add(42 * time.Second)}}, triggers)
}

func TestScanScheduledWaitsForTheRunningRoutineWithoutBacklog(t *testing.T) {
	workspace := t.TempDir()
	cfg := newCronConfig(t, workspace, ".")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "helper.md"), []byte("---\nmodel: gpt-6.1-sol\nroutines: [1s]\n---\nHelp.\n"), 0o644))

	store, db := newCronScheduleStoreDB(t)
	runStarted := make(chan error, 2)
	release := make(chan struct{})
	m := New(cfg, ".", store, &runnerMock{RunFunc: func(ctx context.Context, _, _ string, progress *backend.CronRunProgress) (protocol.CronRunResult, error) {
		_, errConversation := db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, cron_path) VALUES ($1, 'helper', 'cron', 'cron_run', FALSE, $2)`, progress.ConversationID, progress.Run.Path)

		_, errTurn := db.ExecContext(ctx, `INSERT INTO turns (id, conversation_id, created_at_unix_ns, updated_at_unix_ns) VALUES ($1, $1, 0, 0)`, progress.ConversationID)
		runStarted <- errors.Join(errConversation, errTurn)

		<-release

		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	start := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	m.now = func() time.Time { return start }
	require.NoError(t, m.Refresh())

	m.now = func() time.Time { return start.Add(time.Second) }
	require.NoError(t, m.scanScheduled(t.Context()))

	select {
	case err := <-runStarted:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("scheduled routine did not run")
	}

	m.now = func() time.Time { return start.Add(5 * time.Second) }
	require.NoError(t, m.scanScheduled(t.Context()))

	select {
	case err := <-runStarted:
		t.Fatal("a routine overlapped its own running run or replayed its backlog", err)
	default:
	}

	close(release)
	m.wg.Wait()
}

func TestParseCronScheduleRejectsInvalidSchedules(t *testing.T) {
	for raw, want := range map[string]string{
		"":           "schedule must not be blank",
		"0s":         "duration schedules must be greater than zero",
		"@every 1h":  "@every is not supported",
		"not a cron": "invalid cron expression",
	} {
		_, err := config.ParseCronSchedule(raw)
		require.ErrorContains(t, err, want, raw)
	}
}

func TestPreparePromptAddsTheReplyInstruction(t *testing.T) {
	m := New(newCronConfig(t, t.TempDir(), "."), ".", newCronScheduleStore(t), &runnerMock{}, slog.New(slog.DiscardHandler))

	for body, want := range map[string]string{
		"":           cronReplyInstruction,
		"Body\n":     "Body\n\n" + cronReplyInstruction,
		"Body\n\n\n": "Body\n\n\n\n" + cronReplyInstruction,
		"Body\r\n":   "Body\r\n\n" + cronReplyInstruction,
		"Body":       "Body\n\n" + cronReplyInstruction,
	} {
		require.Equal(t, want, m.preparePrompt(body), body)
	}
}

func newCronConfig(t *testing.T, workspace, runtimeDir string) *config.LockedConfig {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, runtimeDir, "agents"), 0o755))

	return config.NewLockedConfig(&config.Config{Workspace: workspace})
}

const easyRoutineAgent = "---\nmode: easy\nmodel: gpt-6.1-sol\nspace: Ops\nworkingDirectory: data\nroutines:\n"

func TestRoutinesRunAsCronDefinitions(t *testing.T) {
	workspace := t.TempDir()
	cfg := newCronConfig(t, workspace, ".")
	agentPath := filepath.Join(workspace, "agents", "marketing.md")
	require.NoError(t, os.WriteFile(agentPath, []byte(easyRoutineAgent+"  - '*/5 * * * *': Check X.\n  - 30m\n---\nPrompt\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "agents", "marketing"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "marketing", "twin.md"), []byte(easyRoutineAgent+"  - 1h\n---\nPrompt\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "ops.md"), []byte("---\nmodel: gpt-6.1-sol\nroutines:\n  - 1h: Check Y.\n---\nPrompt\n"), 0o644))

	definitions, err := loadDefinitionsIn(cfg, ".")
	require.NoError(t, err)
	require.Len(t, definitions, 3)

	var runs []string

	store := newCronScheduleStore(t)
	m := New(cfg, ".", store, &runnerMock{RunFunc: func(_ context.Context, agent, prompt string, progress *backend.CronRunProgress) (protocol.CronRunResult, error) {
		runs = append(runs, agent+"|"+prompt+"|"+progress.Run.Path+"|"+progress.Cronjob.Space)
		return protocol.CronRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	for i := range definitions {
		m.executeJob(t.Context(), &definitions[i])
	}

	require.Equal(t, []string{
		"marketing|Check X.\n\n" + cronReplyInstruction + "|agents/marketing.md#*/5 * * * *|Ops",
		"marketing|Scheduled run (30m).\n\n" + cronReplyInstruction + "|agents/marketing.md#30m|Ops",
		"ops|Check Y.\n\n" + cronReplyInstruction + "|agents/ops.md#1h|",
	}, runs)

	job, err := m.LoadOneOffCronjob("marketing · 30m")
	require.NoError(t, err)
	require.Equal(t, protocol.OneOffCronjob{RelativePath: "agents/marketing.md#30m", Agent: "marketing", Space: "Ops", Prompt: "Scheduled run (30m).\n\n" + cronReplyInstruction}, job)

	_, err = m.LoadOneOffCronjob("marketing · 2h")
	require.ErrorContains(t, err, `no routine "marketing · 2h"`)

	start := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, store.SyncCronSchedules(m.scheduledStates(definitions, start)))
	before, err := store.DueCronSchedules(start.Add(24 * time.Hour))
	require.NoError(t, err)
	require.Len(t, before, 3)
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
