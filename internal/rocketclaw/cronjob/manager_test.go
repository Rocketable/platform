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
)

func TestNewInstallsTextChannelNoop(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	m := New(t.TempDir(), ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		t.Fatal("cronjob manager ran during construction")

		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))

	if err := m.SendTextChannel(t.Context(), "C123", "cron/daily.md", "main", "now", "done", nil); err != nil {
		t.Fatalf("SendTextChannel() = %v; want nil", err)
	}
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

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\nagent: worker\n---\nRuntime cron\n"), 0o644); err != nil {
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

func TestLoadOneOffCronjobUsesEffectiveRuntimeCron(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, ".rocketclaw", "cron")
	if err := os.MkdirAll(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\nagent: helper\n---\nRuntime cron"), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := events.New()
	defer bus.Close()

	m := New(workspace, ".rocketclaw", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		t.Fatal("cronjob manager ran during load test")

		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))

	job, err := m.LoadOneOffCronjob("daily")
	if err != nil {
		t.Fatal(err)
	}

	if job.Agent != "helper" || job.RelativePath != "cron/daily.md" || !strings.Contains(job.Prompt, "Runtime cron") {
		t.Fatalf("LoadOneOffCronjob = %#v; want effective runtime cron", job)
	}
}

func TestRunOneOffCronjobSetsTraceConversationID(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	m := New(t.TempDir(), ".", bus, func(_ context.Context, _, _ string, _ *slog.Logger, progress *harnessbridge.RawRunProgress) (RunResult, error) {
		if !strings.HasPrefix(progress.ConversationID, "one-off-cron:cron/daily.md:20000102T030405.000000006Z:") {
			t.Fatalf("ConversationID = %q; want one-off trace ID", progress.ConversationID)
		}

		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 6, time.UTC) }

	m.RunOneOffCronjob(t.Context(), OneOffCronjob{Agent: "helper", Prompt: "Body", RelativePath: "cron/daily.md"}, nil, func(context.Context, RunResult, error) {})
}

func TestRunOneOffCronjobRejectsStoppedManager(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	m := New(t.TempDir(), ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
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
	bus := events.New()
	defer bus.Close()

	m := New(t.TempDir(), ".", bus, func(_ context.Context, _, _ string, _ *slog.Logger, progress *harnessbridge.RawRunProgress) (RunResult, error) {
		if !strings.HasPrefix(progress.ConversationID, "cron:cron/daily.md:20000102T030405.000000006Z:") {
			t.Fatalf("ConversationID = %q; want scheduled trace ID", progress.ConversationID)
		}

		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 6, time.UTC) }

	m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", body: "Body"})
}

func TestExecuteJobPublishesVisibleAndInternalMessages(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	m := New(t.TempDir(), ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		return RunResult{
			Text:            "internal summary",
			VerbatimMessage: "visible answer",
			Attachments:     []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}},
		}, nil
	}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC) }

	m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", body: "Body"})

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	var messages []*events.InboundMessage
	for msg := range bus.Inbound(ctx) {
		messages = append(messages, msg)
		if len(messages) == 2 {
			break
		}
	}

	if len(messages) != 2 {
		t.Fatalf("executeJob published %d messages; want 2", len(messages))
	}

	visible := messages[0]
	if visible.Label != "cronjob human_visible file=cron/daily.md ran_at=2000-01-02T03:04:05Z" || !strings.Contains(visible.Text, "visible answer") {
		t.Fatalf("visible message = %#v; want labeled verbatim delivery", visible)
	}

	if visible.VerbatimMessage != "visible answer" || len(visible.VerbatimAttachments) != 1 || visible.VerbatimAttachments[0].Name != "report.txt" || string(visible.VerbatimAttachments[0].Data) != "report" {
		t.Fatalf("visible verbatim payload = (%q, %#v); want answer with report attachment", visible.VerbatimMessage, visible.VerbatimAttachments)
	}

	internal := messages[1]
	if internal.Label != "cronjob file=cron/daily.md ran_at=2000-01-02T03:04:05Z" || !strings.Contains(internal.Text, "internal summary") {
		t.Fatalf("internal message = %#v; want labeled internal summary", internal)
	}

	if internal.VerbatimMessage != "" || len(internal.VerbatimAttachments) != 0 {
		t.Fatalf("internal verbatim payload = (%q, %#v); want none", internal.VerbatimMessage, internal.VerbatimAttachments)
	}
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

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := events.New()
	defer bus.Close()

	m := New(workspace, ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
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

func TestRunJobLoopDoesNotStartQueuedJobAfterStopAccepting(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	runStarted := make(chan struct{}, 1)
	m := New(t.TempDir(), ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		runStarted <- struct{}{}

		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))
	j := &job{definition: definition{relativePath: "cron/daily.md"}, wakeCh: make(chan struct{}, 1)}
	j.trigger()
	m.StopAccepting()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan struct{})

	m.wg.Add(1)

	go func() {
		m.runJobLoop(ctx, j)
		close(done)
	}()

	select {
	case <-done:
	case <-runStarted:
		cancel()
		<-done
		t.Fatal("queued cronjob started after manager stopped")
	case <-time.After(time.Second):
		t.Fatal("cronjob loop did not stop")
	}
}

func TestStopAfterStopAcceptingStillStopsManager(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	m := New(t.TempDir(), ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))
	stopCalled := false
	m.start = true
	m.stop = func() { stopCalled = true }
	m.StopAccepting()

	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if !stopCalled {
		t.Fatal("Stop did not stop manager after StopAccepting")
	}
}

func TestStartRejectsAlreadyStartedManager(t *testing.T) {
	workspace := t.TempDir()

	cronDir := filepath.Join(workspace, "cron")
	if err := os.Mkdir(cronDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cronDir, "daily.md"), []byte("---\nschedule: 1h\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := events.New()
	defer bus.Close()

	m := New(workspace, ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
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
	if err := os.WriteFile(cronPath, []byte("---\nschedule: \"2000-01-01T00:00:00Z\"\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := events.New()
	defer bus.Close()

	runDone := make(chan struct{})
	m := New(workspace, ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
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
	if err := os.WriteFile(cronPath, []byte("---\nschedule: \""+now.Add(30*time.Millisecond).Format(time.RFC3339Nano)+"\"\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := events.New()
	defer bus.Close()

	runDone := make(chan struct{})
	m := New(workspace, ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
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
	if err := os.WriteFile(cronPath, []byte("---\nschedule: \"2000-01-01T00:00:00Z\"\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := events.New()
	defer bus.Close()

	runDone := make(chan struct{})
	m := New(workspace, ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
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

func TestPreparePromptInstructionCases(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	m := New(t.TempDir(), ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
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

func TestJobTriggerCoalescesPendingWakeups(t *testing.T) {
	j := &job{wakeCh: make(chan struct{}, 1)}

	j.trigger()

	if j.pending != 1 {
		t.Fatalf("pending after first trigger = %d; want 1", j.pending)
	}

	select {
	case <-j.wakeCh:
	default:
		t.Fatal("trigger did not send initial wakeup")
	}

	j.trigger()

	if j.pending != 2 {
		t.Fatalf("pending after second trigger = %d; want 2", j.pending)
	}

	select {
	case <-j.wakeCh:
		t.Fatal("second trigger sent duplicate wakeup")
	default:
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
		{name: "missing schedule", data: "---\nagent: main\n---\nbody", want: "schedule is required"},
		{name: "invalid schedule list item", data: "---\nschedule:\n  - 1h\n  - 3\n---\nbody", want: "schedule must be a string or list of strings"},
		{name: "invalid schedule scalar", data: "---\nschedule: 123\n---\nbody", want: "schedule must be a string or list of strings"},
		{name: "blank duration", data: "---\nschedule: ''\n---\nbody", want: "schedule must not be blank"},
		{name: "zero duration", data: "---\nschedule: 0s\n---\nbody", want: "duration schedules must be greater than zero"},
		{name: "every unsupported", data: "---\nschedule: '@every 1h'\n---\nbody", want: "@every is not supported"},
		{name: "invalid cron", data: "---\nschedule: not a cron\n---\nbody", want: "invalid cron expression"},
		{name: "mixed timestamp", data: "---\nschedule:\n  - '2000-01-01T00:00:00Z'\n  - 1h\n---\nbody", want: "timestamp schedules cannot be combined"},
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
	def, err := loadDefinition([]byte("---\nschedule: 1h\nagent: 7\n---\nBody"), "cron/test.md")
	if err != nil {
		t.Fatal(err)
	}

	if def.agent != "7" {
		t.Fatalf("definition agent = %q; want 7", def.agent)
	}
}

func TestLoadDefinitionDefaultsBlankAgentAndTrimsChannel(t *testing.T) {
	def, err := loadDefinition([]byte("---\nschedule: 1h\nagent: '  \t  '\nchannel: '  #ops  '\n---\nBody"), "cron/test.md")
	if err != nil {
		t.Fatal(err)
	}

	if def.agent != "main" || def.textChannel != "#ops" {
		t.Fatalf("definition agent/textChannel = %q/%q; want main/#ops", def.agent, def.textChannel)
	}
}

func TestLoadDefinitionIgnoresLegacyTextChannelAlias(t *testing.T) {
	def, err := loadDefinition([]byte("---\nschedule: 1h\nslack-channel: '  #legacy  '\n---\nBody"), "cron/test.md")
	if err != nil {
		t.Fatal(err)
	}

	if def.textChannel != "" {
		t.Fatalf("definition textChannel = %q; want empty", def.textChannel)
	}
}

func TestExecuteJobWithTextChannelSendsThreadOnlyFinalPayload(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	m := New(t.TempDir(), ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		return RunResult{Text: "internal note", VerbatimMessage: " final payload ", Attachments: []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}}, nil
	}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC) }

	var (
		gotChannel, gotPath, gotAgent, gotRanAt, gotText string
		gotAttachments                                   []events.OutboundAttachment
	)

	m.SendTextChannel = func(_ context.Context, channel, path, agent, ranAt, text string, attachments []events.OutboundAttachment) error {
		gotChannel, gotPath, gotAgent, gotRanAt, gotText = channel, path, agent, ranAt, text
		gotAttachments = attachments

		return nil
	}

	m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", textChannel: "#triage", body: "Body"})

	if gotChannel != "#triage" || gotPath != "cron/daily.md" || gotAgent != "helper" || gotRanAt != "2000-01-02T03:04:05Z" || gotText != "final payload" {
		t.Fatalf("text delivery = (%q, %q, %q, %q, %q); want channel/path/agent/time/final payload", gotChannel, gotPath, gotAgent, gotRanAt, gotText)
	}

	if len(gotAttachments) != 1 || gotAttachments[0].Name != "report.txt" || gotAttachments[0].MIMEType != "text/plain" || string(gotAttachments[0].Data) != "report" {
		t.Fatalf("text attachments = %#v; want report.txt text/plain report", gotAttachments)
	}

	assertNoInbound(t, bus)
}

func TestExecuteJobWithTextChannelSkipsEmptyFinalPayload(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	m := New(t.TempDir(), ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		return RunResult{Text: "internal note"}, nil
	}, slog.New(slog.DiscardHandler))
	m.SendTextChannel = func(context.Context, string, string, string, string, string, []events.OutboundAttachment) error {
		t.Fatal("text delivery called for empty final payload")
		return nil
	}

	m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", textChannel: "#triage", body: "Body"})

	assertNoInbound(t, bus)
}

func TestExecuteJobWithoutChannelSendsDefaultTextThread(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	m := New(t.TempDir(), ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		return RunResult{Text: "internal note", VerbatimMessage: " final payload ", Attachments: []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}}, nil
	}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC) }

	var (
		gotChannel, gotPath, gotAgent, gotRanAt, gotText string
		gotAttachments                                   []events.OutboundAttachment
	)

	m.SendTextChannel = func(_ context.Context, channel, path, agent, ranAt, text string, attachments []events.OutboundAttachment) error {
		gotChannel, gotPath, gotAgent, gotRanAt, gotText = channel, path, agent, ranAt, text
		gotAttachments = attachments

		return nil
	}

	m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", body: "Body"})

	if gotChannel != "" || gotPath != "cron/daily.md" || gotAgent != "helper" || gotRanAt != "2000-01-02T03:04:05Z" || gotText != "final payload" {
		t.Fatalf("default text delivery = (%q, %q, %q, %q, %q); want empty channel/path/agent/time/final payload", gotChannel, gotPath, gotAgent, gotRanAt, gotText)
	}

	if len(gotAttachments) != 1 || gotAttachments[0].Name != "report.txt" || gotAttachments[0].MIMEType != "text/plain" || string(gotAttachments[0].Data) != "report" {
		t.Fatalf("default text attachments = %#v; want report.txt text/plain report", gotAttachments)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	var messages []*events.InboundMessage
	for msg := range bus.Inbound(ctx) {
		messages = append(messages, msg)
		if len(messages) == 2 {
			break
		}
	}

	if len(messages) != 2 {
		t.Fatalf("executeJob published %d messages; want visible and internal main notes", len(messages))
	}

	if messages[0].Label != "cronjob human_visible file=cron/daily.md ran_at=2000-01-02T03:04:05Z" || messages[0].VerbatimMessage != " final payload " {
		t.Fatalf("visible main note = %#v; want verbatim main note", messages[0])
	}

	if messages[1].Label != "cronjob file=cron/daily.md ran_at=2000-01-02T03:04:05Z" || !strings.Contains(messages[1].Text, "internal note") {
		t.Fatalf("internal main note = %#v; want internal note", messages[1])
	}
}

func TestExecuteJobDefaultTextDeliveryKeepsInternalOnlyOutputInMain(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	m := New(t.TempDir(), ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		return RunResult{Text: "internal note"}, nil
	}, slog.New(slog.DiscardHandler))
	m.now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC) }
	m.SendTextChannel = func(context.Context, string, string, string, string, string, []events.OutboundAttachment) error {
		t.Fatal("default text delivery called for empty final payload")
		return nil
	}

	m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", body: "Body"})

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	var msg *events.InboundMessage
	for inbound := range bus.Inbound(ctx) {
		msg = inbound
		break
	}

	if msg == nil {
		t.Fatal("executeJob did not publish internal-only message")
	}

	if msg.Label != "cronjob file=cron/daily.md ran_at=2000-01-02T03:04:05Z" || !strings.Contains(msg.Text, "internal note") {
		t.Fatalf("internal-only message = %#v; want labeled internal note", msg)
	}
}

func assertNoInbound(t *testing.T, bus *events.Bus) {
	t.Helper()

	bus.StopInbound()

	for range bus.Inbound(context.Background()) {
		t.Fatal("unexpected inbound message")
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

	bus := events.New()
	defer bus.Close()

	m := New(workspace, ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
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

	bus := events.New()
	defer bus.Close()

	m := New(workspace, ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
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

	bus := events.New()
	defer bus.Close()

	m := New(workspace, ".", bus, func(context.Context, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (RunResult, error) {
		return RunResult{}, nil
	}, slog.New(slog.DiscardHandler))

	if _, err := m.LoadOneOffCronjob("daily"); err == nil || !strings.Contains(err.Error(), "open workspace root") {
		t.Fatalf("LoadOneOffCronjob(daily) error = %v; want workspace open error", err)
	}
}
