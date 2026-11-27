package routinefrontend

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

func newRoutineScheduleStore(t *testing.T) *backend.SessionService {
	t.Helper()

	store, _ := newRoutineScheduleStoreDB(t)

	return store
}

func newRoutineScheduleStoreDB(t *testing.T) (*backend.SessionService, *sql.DB) {
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
	cfg := newRoutineConfig(t, workspace, "runtime")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "runtime", "agents", "worker.md"), []byte("---\nmodel: gpt-6.1-sol\nroutines:\n  - 30m: Exact body\n  - '0 * * * *'\n---\nWork.\n"), 0o600))
	store := newRoutineScheduleStore(t)
	manager := New(cfg, "runtime", store, &runnerMock{}, slog.New(slog.DiscardHandler))
	start := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return start.Add(5 * time.Minute) }
	definitions, err := loadDefinitionsIn(cfg, "runtime")
	require.NoError(t, err)

	states := manager.scheduledStates(definitions, start)
	require.NoError(t, store.SyncRoutineSchedules(states))

	jobs, err := manager.Jobs()
	require.NoError(t, err)
	require.Equal(t, []Job{
		{RelativePath: "agents/worker.md#30m", Agent: "worker", Body: "Exact body", Schedule: "30m", Upcoming: []time.Time{start.Add(30 * time.Minute)}},
		{RelativePath: "agents/worker.md#0 * * * *", Agent: "worker", Body: "Scheduled run (0 * * * *).", Schedule: "0 * * * *", Upcoming: []time.Time{start.Add(time.Hour)}},
	}, jobs)
	remaining, err := store.DueRoutineSchedules(start.Add(24 * time.Hour))
	require.NoError(t, err)
	require.ElementsMatch(t, states, remaining)
}

func TestBadRoutinesFailOnlyTheirAgents(t *testing.T) {
	workspace := t.TempDir()
	cfg := newRoutineConfig(t, workspace, ".rocketclaw-stage")
	agents := filepath.Join(workspace, ".rocketclaw-stage", "agents")
	require.NoError(t, os.WriteFile(filepath.Join(agents, "helper.md"), []byte("---\nmodel: gpt-6.1-sol\nroutines: [1h]\n---\nPrompt\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(agents, "phrase.md"), []byte("---\nmodel: gpt-6.1-sol\nroutines: [every 30min]\n---\nPrompt\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(agents, "twice.md"), []byte(easyRoutineAgent+"  - 30m\n  - 30m: Again.\n---\nPrompt\n"), 0o644))

	store := newRoutineScheduleStore(t)
	start := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	m := New(cfg, ".rocketclaw-stage", store, &runnerMock{}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return start }

	require.NoError(t, m.Start(t.Context()))
	t.Cleanup(func() { stopRoutineManager(t, m) })
	require.NoError(t, m.Refresh())

	scheduled, err := store.DueRoutineSchedules(start.Add(24 * time.Hour))
	require.NoError(t, err)
	require.Equal(t, []backend.RoutineScheduleState{{ScheduleID: "agents/helper.md#1h", RelativePath: "agents/helper.md#1h", NextDue: start.Add(time.Hour)}}, scheduled)
}

func TestRoutineTraceConversationIDPreservesRelativePath(t *testing.T) {
	ts := time.Date(2000, 1, 2, 3, 4, 5, 6, time.UTC)

	for _, prefix := range []string{routineTracePrefix, oneOffRoutineTracePrefix} {
		for _, path := range []string{"cron/report:daily.md", "cron/report_daily.md"} {
			id := routineTraceConversationID(prefix, path, ts)
			require.Equal(t, prefix+path+":20000102T030405.000000006Z", id[:strings.LastIndex(id, ":")])
		}
	}
}

func TestRunOneOffRoutineSetsTraceConversationID(t *testing.T) {
	var (
		sources     []string
		logs        bytes.Buffer
		destination string
	)

	m := New(newRoutineConfig(t, t.TempDir(), "."), ".", newRoutineScheduleStore(t), &runnerMock{RunFunc: func(_ context.Context, agent, prompt string, progress *backend.RoutineRunProgress) (protocol.RoutineRunResult, error) {
		require.Equal(t, "helper", agent)
		require.Equal(t, "Body", prompt)
		require.Equal(t, destination, progress.SyncDestination)
		require.Equal(t, &protocol.RoutineMessage{Agent: "helper", Space: "Ops"}, progress.Routine, "a run from Web carries the cron's space")

		sources = append(sources, progress.ConversationID)
		if !strings.HasPrefix(progress.ConversationID, "one-off-routine:cron/daily.md:20000102T030405.000000006Z:") {
			t.Fatalf("ConversationID = %q; want one-off trace ID", progress.ConversationID)
		}

		return protocol.RoutineRunResult{ConversationID: progress.SyncDestination, Reported: progress.SyncDestination != ""}, nil
	}}, slog.New(slog.NewJSONHandler(&logs, nil)))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 6, time.UTC) }

	for _, destination = range []string{"slack-thread:C1:1.2", ""} {
		_, err := m.RunOneOffRoutine(t.Context(), &protocol.OneOffRoutine{Agent: "helper", Space: "Ops", Prompt: "Body", RelativePath: "cron/daily.md", ConversationID: destination})
		require.NoError(t, err)
	}

	require.Len(t, sources, 2)
	require.NotEqual(t, sources[0], sources[1])
	require.Equal(t, 2, strings.Count(logs.String(), `"event":"routine_completed"`))
	require.Equal(t, 1, strings.Count(logs.String(), `"outcome":"reported"`))
	require.Equal(t, 1, strings.Count(logs.String(), `"outcome":"intentional_silence"`))
	require.Contains(t, logs.String(), `"duration_ms":`)
}

func TestRunOneOffRoutineRejectsStoppedManager(t *testing.T) {
	m := New(newRoutineConfig(t, t.TempDir(), "."), ".", newRoutineScheduleStore(t), &runnerMock{RunFunc: func(context.Context, string, string, *backend.RoutineRunProgress) (protocol.RoutineRunResult, error) {
		t.Fatal("stopped routine manager ran one-off routine")

		return protocol.RoutineRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	stopRoutineManager(t, m)

	_, err := m.RunOneOffRoutine(t.Context(), &protocol.OneOffRoutine{Agent: "helper", Prompt: "Body", RelativePath: "cron/daily.md"})
	require.ErrorContains(t, err, "routine manager is stopped")
}

func TestExecuteJobSetsTraceConversationID(t *testing.T) {
	var logs bytes.Buffer

	workspace := t.TempDir()
	m := New(newRoutineConfig(t, workspace, "."), ".", newRoutineScheduleStore(t), &runnerMock{RunFunc: func(_ context.Context, _, _ string, progress *backend.RoutineRunProgress) (protocol.RoutineRunResult, error) {
		if !strings.HasPrefix(progress.ConversationID, "routine:cron/daily.md:20000102T030405.000000006Z:") {
			t.Fatalf("ConversationID = %q; want scheduled trace ID", progress.ConversationID)
		}

		return protocol.RoutineRunResult{}, nil
	}}, slog.New(slog.NewJSONHandler(&logs, nil)))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 6, time.UTC) }

	m.executeJob(t.Context(), &config.RoutineDefinition{Job: protocol.OneOffRoutine{RelativePath: "cron/daily.md", Agent: "helper", Prompt: "Body"}})
	require.Contains(t, logs.String(), `"event":"routine_started"`)
	require.Contains(t, logs.String(), `"event":"routine_completed"`)
	require.Contains(t, logs.String(), `"outcome":"intentional_silence"`)
	require.Contains(t, logs.String(), `"duration_ms":`)
	logs.Reset()

	m.run = &runnerMock{RunFunc: func(context.Context, string, string, *backend.RoutineRunProgress) (protocol.RoutineRunResult, error) {
		return protocol.RoutineRunResult{Reported: true}, nil
	}}
	m.executeJob(t.Context(), &config.RoutineDefinition{Job: protocol.OneOffRoutine{RelativePath: "cron/daily.md", Agent: "helper", Prompt: "Body"}})
	require.Contains(t, logs.String(), `"outcome":"reported"`)
	logs.Reset()

	m.run = &runnerMock{RunFunc: func(context.Context, string, string, *backend.RoutineRunProgress) (protocol.RoutineRunResult, error) {
		return protocol.RoutineRunResult{}, errors.New("secret-bearing-error")
	}}
	m.executeJob(t.Context(), &config.RoutineDefinition{Job: protocol.OneOffRoutine{RelativePath: "cron/daily.md", Agent: "helper", Prompt: "Body"}})
	require.Contains(t, logs.String(), `"outcome":"failed"`)
	require.NotContains(t, logs.String(), "secret-bearing-error")
}

func TestLoadDefinitionsReportsAgentLoadErrors(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.WriteFile(workspace, []byte("not a directory"), 0o644))

	cfg := config.NewLockedConfig(&config.Config{Workspace: workspace})
	_, err := loadDefinitionsIn(cfg, ".")
	require.ErrorContains(t, err, "load agents")

	_, err = New(cfg, ".", newRoutineScheduleStore(t), &runnerMock{}, slog.New(slog.DiscardHandler)).LoadOneOffRoutine("helper · 1h")
	require.ErrorContains(t, err, "load agents")
}

func stopRoutineManager(t *testing.T, m *Manager) {
	t.Helper()

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := m.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestScanScheduledRunsLoadedDefinitionUntilRefresh(t *testing.T) {
	workspace := t.TempDir()
	cfg := newRoutineConfig(t, workspace, ".")
	agentPath := filepath.Join(workspace, "agents", "helper.md")
	require.NoError(t, os.WriteFile(agentPath, []byte("---\nmodel: gpt-6.1-sol\nroutines:\n  - 1s: old body\n---\nHelp.\n"), 0o644))

	store, db := newRoutineScheduleStoreDB(t)
	runPrompt := make(chan string, 1)
	m := New(cfg, ".", store, &runnerMock{RunFunc: func(_ context.Context, _ string, prompt string, _ *backend.RoutineRunProgress) (protocol.RoutineRunResult, error) {
		runPrompt <- prompt
		return protocol.RoutineRunResult{}, nil
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
	t.Cleanup(func() { stopRoutineManager(t, m) })

	scan := func(at time.Duration, want string) {
		t.Helper()

		clockMu.Lock()
		elapsed = at
		clockMu.Unlock()

		require.NoError(t, m.scanScheduled(t.Context()))

		select {
		case prompt := <-runPrompt:
			require.Equal(t, want+"\n\n"+routineReplyInstruction, prompt)
		case <-time.After(time.Second):
			t.Fatal("scheduled routine did not run")
		}
	}

	require.NoError(t, os.WriteFile(agentPath, []byte("---\nmodel: gpt-6.1-sol\nroutines:\n  - 2s: new body\n---\nHelp.\n"), 0o644))
	scan(10*time.Second, "old body")

	_, err := db.ExecContext(t.Context(), `ALTER TABLE schedules ADD CONSTRAINT refuse_edit CHECK (id <> 'agents/helper.md#2s')`)
	require.NoError(t, err)
	require.ErrorContains(t, m.Refresh(), "sync routine schedules")
	scan(30*time.Second, "old body")

	_, err = db.ExecContext(t.Context(), `ALTER TABLE schedules DROP CONSTRAINT refuse_edit`)
	require.NoError(t, err)
	require.NoError(t, m.Refresh())
	scan(40*time.Second, "new body")

	triggers, err := store.DueRoutineSchedules(start.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, []backend.RoutineScheduleState{{ScheduleID: "agents/helper.md#2s", RelativePath: "agents/helper.md#2s", NextDue: start.Add(42 * time.Second)}}, triggers)
}

func TestScanScheduledWaitsForTheRunningRoutineWithoutBacklog(t *testing.T) {
	workspace := t.TempDir()
	cfg := newRoutineConfig(t, workspace, ".")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "helper.md"), []byte("---\nmodel: gpt-6.1-sol\nroutines: [1s]\n---\nHelp.\n"), 0o644))

	store, db := newRoutineScheduleStoreDB(t)
	runStarted := make(chan error, 2)
	release := make(chan struct{})
	m := New(cfg, ".", store, &runnerMock{RunFunc: func(ctx context.Context, _, _ string, progress *backend.RoutineRunProgress) (protocol.RoutineRunResult, error) {
		_, errConversation := db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, routine_path) VALUES ($1, 'helper', 'routine', 'routine_run', FALSE, $2)`, progress.ConversationID, progress.Run.Path)

		_, errTurn := db.ExecContext(ctx, `INSERT INTO turns (id, conversation_id, created_at_unix_ns, updated_at_unix_ns) VALUES ($1, $1, 0, 0)`, progress.ConversationID)
		runStarted <- errors.Join(errConversation, errTurn)

		<-release

		return protocol.RoutineRunResult{}, nil
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

func TestParseRoutineScheduleRejectsInvalidSchedules(t *testing.T) {
	for raw, want := range map[string]string{
		"":           "schedule must not be blank",
		"0s":         "duration schedules must be greater than zero",
		"@every 1h":  "@every is not supported",
		"not a cron": "invalid cron expression",
	} {
		_, err := config.ParseRoutineSchedule(raw)
		require.ErrorContains(t, err, want, raw)
	}
}

func TestPreparePromptAddsTheReplyInstruction(t *testing.T) {
	m := New(newRoutineConfig(t, t.TempDir(), "."), ".", newRoutineScheduleStore(t), &runnerMock{}, slog.New(slog.DiscardHandler))

	for body, want := range map[string]string{
		"":           routineReplyInstruction,
		"Body\n":     "Body\n\n" + routineReplyInstruction,
		"Body\n\n\n": "Body\n\n\n\n" + routineReplyInstruction,
		"Body\r\n":   "Body\r\n\n" + routineReplyInstruction,
		"Body":       "Body\n\n" + routineReplyInstruction,
	} {
		require.Equal(t, want, m.preparePrompt(body), body)
	}
}

func newRoutineConfig(t *testing.T, workspace, runtimeDir string) *config.LockedConfig {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, runtimeDir, "agents"), 0o755))

	return config.NewLockedConfig(&config.Config{Workspace: workspace})
}

const easyRoutineAgent = "---\nmode: easy\nmodel: gpt-6.1-sol\nspace: Ops\nworkingDirectory: data\nroutines:\n"

func TestRoutinesRunAsRoutineDefinitions(t *testing.T) {
	workspace := t.TempDir()
	cfg := newRoutineConfig(t, workspace, ".")
	agentPath := filepath.Join(workspace, "agents", "marketing.md")
	require.NoError(t, os.WriteFile(agentPath, []byte(easyRoutineAgent+"  - '*/5 * * * *': Check X.\n  - 30m\n---\nPrompt\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "agents", "marketing"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "marketing", "twin.md"), []byte(easyRoutineAgent+"  - 1h\n---\nPrompt\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "ops.md"), []byte("---\nmodel: gpt-6.1-sol\nroutines:\n  - 1h: Check Y.\n---\nPrompt\n"), 0o644))

	definitions, err := loadDefinitionsIn(cfg, ".")
	require.NoError(t, err)
	require.Len(t, definitions, 3)

	var runs []string

	store := newRoutineScheduleStore(t)
	m := New(cfg, ".", store, &runnerMock{RunFunc: func(_ context.Context, agent, prompt string, progress *backend.RoutineRunProgress) (protocol.RoutineRunResult, error) {
		runs = append(runs, agent+"|"+prompt+"|"+progress.Run.Path+"|"+progress.Routine.Space)
		return protocol.RoutineRunResult{}, nil
	}}, slog.New(slog.DiscardHandler))

	for i := range definitions {
		m.executeJob(t.Context(), &definitions[i])
	}

	require.Equal(t, []string{
		"marketing|Check X.\n\n" + routineReplyInstruction + "|agents/marketing.md#*/5 * * * *|Ops",
		"marketing|Scheduled run (30m).\n\n" + routineReplyInstruction + "|agents/marketing.md#30m|Ops",
		"ops|Check Y.\n\n" + routineReplyInstruction + "|agents/ops.md#1h|",
	}, runs)

	job, err := m.LoadOneOffRoutine("marketing · 30m")
	require.NoError(t, err)
	require.Equal(t, protocol.OneOffRoutine{RelativePath: "agents/marketing.md#30m", Agent: "marketing", Space: "Ops", Prompt: "Scheduled run (30m).\n\n" + routineReplyInstruction}, job)

	_, err = m.LoadOneOffRoutine("marketing · 2h")
	require.ErrorContains(t, err, `no routine "marketing · 2h"`)

	start := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, store.SyncRoutineSchedules(m.scheduledStates(definitions, start)))
	before, err := store.DueRoutineSchedules(start.Add(24 * time.Hour))
	require.NoError(t, err)
	require.Len(t, before, 3)
	require.NoError(t, os.WriteFile(agentPath, []byte(easyRoutineAgent+"  - 30m\n  - '*/5 * * * *': Check X.\n---\nPrompt\n"), 0o644))

	reordered, err := loadDefinitionsIn(cfg, ".")
	require.NoError(t, err)
	require.NoError(t, store.SyncRoutineSchedules(m.scheduledStates(reordered, start.Add(time.Minute))))
	after, err := store.DueRoutineSchedules(start.Add(24 * time.Hour))
	require.NoError(t, err)
	require.Equal(t, before, after)
}
