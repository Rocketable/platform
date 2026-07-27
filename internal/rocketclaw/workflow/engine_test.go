package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func TestRunValuesAndWorkers(t *testing.T) {
	t.Run("text agent receives worker and arguments", func(t *testing.T) {
		definition := engineDefinition(t, `
researcher = worker(name="researcher", instructions="Find facts", model="fast", tools=["read", "grep"])
def main(args):
    return agent(args, worker=researcher, label="first")
`)

		var request AgentRequest

		result, err := Run(t.Context(), definition, RunRequest{RunID: "run-1", Args: "question"}, func(_ context.Context, got AgentRequest, _ AgentThinkingFunc) (json.RawMessage, error) {
			request = got
			return json.RawMessage(`"answer"`), nil
		}, discardProgress, discardAgentProgress)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if result.Text != "answer" || result.Silent {
			t.Fatalf("Run() result = %+v, want text answer", result)
		}

		wantWorker := Worker{Name: "researcher", Instructions: "Find facts", Model: "fast", Tools: []string{"read", "grep"}}
		if request.Prompt != "question" || request.Worker.Name != wantWorker.Name || request.Worker.Instructions != wantWorker.Instructions || request.Worker.Model != wantWorker.Model || !slices.Equal(request.Worker.Tools, wantWorker.Tools) {
			t.Fatalf("agent request = %+v, want prompt, implicit phase, and worker", request)
		}
	})

	t.Run("structured agent result is native Starlark", func(t *testing.T) {
		definition := engineDefinition(t, `
def main(args):
    value = agent("structured", schema={"type": "object"})
    return [value["ok"], value["count"] + 1]
`)

		result, err := Run(t.Context(), definition, RunRequest{RunID: "structured"}, func(_ context.Context, request AgentRequest, _ AgentThinkingFunc) (json.RawMessage, error) {
			if request.Schema["type"] != "object" {
				t.Fatalf("agent schema = %v, want object schema", request.Schema)
			}

			return json.RawMessage(`{"ok":true,"count":2}`), nil
		}, discardProgress, discardAgentProgress)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if result.Text != `[true,3]` {
			t.Fatalf("Run() result = %+v, want [true,3]", result)
		}
	})

	t.Run("structured agent result must match schema", func(t *testing.T) {
		definition := engineDefinition(t, `
def main(args):
    return agent("structured", schema={"type": "object", "properties": {"count": {"type": "integer"}}, "required": ["count"]})
`)

		_, err := Run(t.Context(), definition, RunRequest{RunID: "mismatch"}, func(context.Context, AgentRequest, AgentThinkingFunc) (json.RawMessage, error) {
			return json.RawMessage(`{"count":"two"}`), nil
		}, discardProgress, discardAgentProgress)
		if err == nil || !strings.Contains(err.Error(), "count") {
			t.Fatalf("Run() error = %v, want schema mismatch", err)
		}
	})

	t.Run("worker value does not expose mutable tools", func(t *testing.T) {
		definition := engineDefinition(t, `
w = worker(name="worker", instructions="work", tools=["read"])
def main(args):
    agent("first", worker=w)
    return agent("second", worker=w)
`)
		calls := 0

		_, err := Run(t.Context(), definition, RunRequest{RunID: "immutable"}, func(_ context.Context, request AgentRequest, _ AgentThinkingFunc) (json.RawMessage, error) {
			calls++
			if request.Worker.Tools[0] != "read" {
				t.Fatalf("agent %d tools = %v, want immutable read tool", calls, request.Worker.Tools)
			}

			request.Worker.Tools[0] = "changed"

			return json.RawMessage(`"ok"`), nil
		}, discardProgress, discardAgentProgress)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	})

	t.Run("explicit empty worker tools stay non-nil", func(t *testing.T) {
		definition := engineDefinition(t, `
w = worker(name="worker", instructions="work", tools=[])
def main(args): return agent("prompt", worker=w)
`)

		_, err := Run(t.Context(), definition, RunRequest{RunID: "empty-tools"}, func(_ context.Context, request AgentRequest, _ AgentThinkingFunc) (json.RawMessage, error) {
			if request.Worker.Tools == nil || len(request.Worker.Tools) != 0 {
				t.Fatalf("worker tools = %#v, want non-nil empty slice", request.Worker.Tools)
			}

			return json.RawMessage(`"ok"`), nil
		}, discardProgress, discardAgentProgress)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	})

	for _, tt := range []struct {
		name, expression, wantText string
		silent                     bool
	}{
		{name: "none", expression: "None", silent: true},
		{name: "empty string", expression: `""`, silent: true},
		{name: "string", expression: `"hello"`, wantText: "hello"},
		{name: "bool", expression: "True", wantText: "true"},
		{name: "integer", expression: "42", wantText: "42"},
		{name: "finite float", expression: "1.5", wantText: "1.5"},
		{name: "list tuple and dict", expression: `{"b": (2, None), "a": [True]}`, wantText: `{"a":[true],"b":[2,null]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Run(t.Context(), engineDefinition(t, "def main(args):\n    return "+tt.expression+"\n"), RunRequest{RunID: tt.name}, inertAgent, discardProgress, discardAgentProgress)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			if result.Text != tt.wantText || result.Silent != tt.silent {
				t.Fatalf("Run() result = %+v, want text %q, silent %t", result, tt.wantText, tt.silent)
			}
		})
	}

	t.Run("declared implicit run starts on first agent call", func(t *testing.T) {
		var updates []PhaseUpdate

		_, err := Run(t.Context(), engineDefinitionWithPhases(t, []string{"run"}, `def main(args): return agent("prompt")`), RunRequest{RunID: "declared-run"}, inertAgent, func(_ context.Context, update PhaseUpdate) error {
			updates = append(updates, update)
			return nil
		}, discardAgentProgress)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		statuses := make([]PhaseStatus, 0, len(updates))
		for _, update := range updates {
			statuses = append(statuses, update.Status)
		}

		want := []PhaseStatus{PhasePending, PhaseInProgress, PhaseInProgress, PhaseInProgress, PhaseComplete}
		if !slices.Equal(statuses, want) {
			t.Fatalf("run phase statuses = %v, want %v", statuses, want)
		}
	})
}

func TestRunRejectsInvalidValues(t *testing.T) {
	for _, tt := range []struct {
		name, body, want string
	}{
		{name: "non-string dict key", body: `def main(args): return {1: "x"}`, want: "string"},
		{name: "non-finite float", body: `def main(args): return 1e308 * 1e308`, want: "finite"},
		{name: "worker result", body: "w = worker(name=\"w\", instructions=\"i\")\ndef main(args): return w", want: "worker"},
		{name: "callable result", body: `def main(args): return main`, want: "function"},
		{name: "set result", body: `def main(args): return set([1])`, want: "set"},
		{name: "cyclic result", body: `def main(args):
    x = []
    x.append(x)
    return x`, want: "cycle"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Run(t.Context(), engineDefinition(t, tt.body), RunRequest{RunID: tt.name}, inertAgent, discardProgress, discardAgentProgress)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestRunReportsStarlarkErrorLocations(t *testing.T) {
	for _, tt := range []struct {
		name, body, location, function string
	}{
		{name: "main", body: `def main(args):
    return "%s" % ()`, location: "test.star:3:17", function: "in main"},
		{name: "callback", body: `def audit(item):
    return "%s" % ()
def main(args):
    return pipeline(["one"], audit)`, location: "test.star:3:17", function: "in audit"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Run(t.Context(), engineDefinition(t, tt.body), RunRequest{RunID: tt.name}, inertAgent, discardProgress, discardAgentProgress)
			if err == nil {
				t.Fatal("Run() error = nil, want Starlark evaluation error")
			}

			if _, ok := errors.AsType[*starlark.EvalError](err); !ok {
				t.Fatalf("Run() error = %v, want wrapped *starlark.EvalError", err)
			}

			for _, want := range []string{tt.location, tt.function, "not enough arguments for format string"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Run() error = %v, want containing %q", err, want)
				}
			}
		})
	}
}

func TestRunFreezesModuleGlobals(t *testing.T) {
	t.Run("concurrent callbacks cannot mutate a module global", func(t *testing.T) {
		definition := engineDefinition(t, `
shared = []
def mutate():
    shared.append(1)
def main(args):
    return parallel([mutate, mutate])
`)

		_, err := Run(t.Context(), definition, RunRequest{RunID: "frozen-global"}, inertAgent, discardProgress, discardAgentProgress)
		if err == nil || !strings.Contains(err.Error(), "frozen") {
			t.Fatalf("Run() error = %v, want frozen module-global mutation rejection", err)
		}
	})

	t.Run("main may mutate a local value", func(t *testing.T) {
		definition := engineDefinition(t, `def main(args):
    local = []
    local.append(args)
    return local
`)

		result, err := Run(t.Context(), definition, RunRequest{RunID: "local", Args: "kept"}, inertAgent, discardProgress, discardAgentProgress)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if result.Text != `["kept"]` {
			t.Fatalf("Run().Text = %s, want local mutation result", result.Text)
		}
	})
}

func TestRunPhases(t *testing.T) {
	definition := engineDefinitionWithPhases(t, []string{"discover", "verify"}, `
def main(args):
    phase("discover", lambda: agent("one"))
    return phase("verify", lambda: agent("two"))
`)

	var updates []PhaseUpdate

	result, err := Run(t.Context(), definition, RunRequest{RunID: "phase-run"}, func(_ context.Context, request AgentRequest, _ AgentThinkingFunc) (json.RawMessage, error) {
		return json.RawMessage(fmt.Sprintf("%q", request.Prompt)), nil
	}, func(_ context.Context, update PhaseUpdate) error {
		updates = append(updates, update)
		return nil
	}, discardAgentProgress)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Text != "two" {
		t.Fatalf("Run().Text = %q, want two", result.Text)
	}

	if got := phaseStatuses(result.Phases); !slices.Equal(got, []PhaseStatus{PhaseComplete, PhaseComplete}) {
		t.Fatalf("Run().Phases statuses = %v, want both complete", got)
	}

	if len(updates) < 8 || updates[0].Status != "pending" || updates[1].Status != "pending" {
		t.Fatalf("phase updates = %+v, want declared pending updates first", updates)
	}

	ids := make(map[string]string)
	terminal := make(map[string]PhaseUpdate)

	for _, update := range updates {
		if previous := ids[update.Name]; previous != "" && previous != update.PhaseID {
			t.Fatalf("phase %q IDs changed from %q to %q", update.Name, previous, update.PhaseID)
		}

		ids[update.Name] = update.PhaseID
		if update.Status == "complete" {
			terminal[update.Name] = update
		}
	}

	for _, name := range definition.Phases {
		update := terminal[name]
		if update.Scheduled != 1 || update.Running != 0 || update.Complete != 1 {
			t.Fatalf("terminal phase %q = %+v, want one complete call", name, update)
		}
	}

	if ids["discover"] != "phase-run/phase/000000/discover" || ids["verify"] != "phase-run/phase/000001/verify" {
		t.Fatalf("declared phase IDs = %v, want declaration indexes", ids)
	}

	for _, tt := range []struct {
		name       string
		runner     AgentRunFunc
		wantStatus PhaseStatus
	}{
		{name: "implicit complete", runner: inertAgent, wantStatus: PhaseComplete},
		{name: "implicit error", runner: func(context.Context, AgentRequest, AgentThinkingFunc) (json.RawMessage, error) {
			return nil, errors.New("agent failed")
		}, wantStatus: PhaseError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var updates []PhaseUpdate

			_, _ = Run(t.Context(), engineDefinition(t, `def main(args): return agent("prompt")`), RunRequest{RunID: tt.name}, tt.runner, func(_ context.Context, update PhaseUpdate) error {
				updates = append(updates, update)
				return nil
			}, discardAgentProgress)

			last := updates[len(updates)-1]
			if last.Name != "run" || last.Status != tt.wantStatus || last.Running != 0 {
				t.Fatalf("last implicit phase update = %+v, want %s terminal update", last, tt.wantStatus)
			}
		})
	}

	t.Run("explicit error clears running calls", func(t *testing.T) {
		var updates []PhaseUpdate

		_, _ = Run(t.Context(), engineDefinitionWithPhases(t, []string{"work"}, `def main(args): return phase("work", lambda: agent("prompt"))`), RunRequest{RunID: "phase-error"}, func(context.Context, AgentRequest, AgentThinkingFunc) (json.RawMessage, error) {
			return nil, errors.New("agent failed")
		}, func(_ context.Context, update PhaseUpdate) error {
			updates = append(updates, update)
			return nil
		}, discardAgentProgress)

		last := updates[len(updates)-1]
		if last.Status != "error" || last.Running != 0 {
			t.Fatalf("last explicit phase update = %+v, want error with no running calls", last)
		}
	})

	t.Run("later failure does not overwrite completed run phase", func(t *testing.T) {
		result, err := Run(t.Context(), engineDefinitionWithPhases(t, []string{"run", "work"}, `def main(args):
    phase("run", lambda: None)
    return phase("work", lambda: agent("fail"))`), RunRequest{RunID: "completed-run"}, func(context.Context, AgentRequest, AgentThinkingFunc) (json.RawMessage, error) {
			return nil, errors.New("failed")
		}, discardProgress, discardAgentProgress)
		if err == nil {
			t.Fatal("Run() error = nil, want later phase failure")
		}

		if got := phaseStatuses(result.Phases); !slices.Equal(got, []PhaseStatus{PhaseComplete, PhaseError}) {
			t.Fatalf("Run().Phases statuses = %v, want completed run then failed work", got)
		}
	})

	for _, tt := range []struct {
		name, body string
		runner     AgentRunFunc
	}{
		{name: "success", body: `def main(args): return None`, runner: inertAgent},
		{name: "failure", body: `def main(args): return phase("work", lambda: agent("fail"))`, runner: func(context.Context, AgentRequest, AgentThinkingFunc) (json.RawMessage, error) {
			return nil, errors.New("failed")
		}},
	} {
		t.Run("untouched declared phases on "+tt.name, func(t *testing.T) {
			var updates []PhaseUpdate

			result, _ := Run(t.Context(), engineDefinitionWithPhases(t, []string{"run", "work", "other"}, tt.body), RunRequest{RunID: "pending-" + tt.name}, tt.runner, func(_ context.Context, update PhaseUpdate) error {
				updates = append(updates, update)
				return nil
			}, discardAgentProgress)

			for _, name := range []string{"run", "other"} {
				last := PhaseUpdate{}

				for _, update := range updates {
					if update.Name == name {
						last = update
					}
				}

				if last.Status != PhaseSkipped || last.Details != "" {
					t.Fatalf("untouched phase %q = %+v, want skipped", name, last)
				}
			}

			if got := phaseStatuses(result.Phases); !slices.Equal(got, []PhaseStatus{PhaseSkipped, map[string]PhaseStatus{"success": PhaseSkipped, "failure": PhaseError}[tt.name], PhaseSkipped}) {
				t.Fatalf("Run().Phases statuses = %v, want final declared statuses", got)
			}
		})
	}

	t.Run("dynamic phases preserve encounter order", func(t *testing.T) {
		var updates []PhaseUpdate

		result, err := Run(t.Context(), engineDefinition(t, `
def main(args):
    phase("verify", lambda: None)
    return phase("audit", lambda: None)
`), RunRequest{RunID: "dynamic-order"}, inertAgent, func(_ context.Context, update PhaseUpdate) error {
			updates = append(updates, update)
			return nil
		}, discardAgentProgress)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		var ids []string

		for _, update := range updates {
			if update.Status == PhaseInProgress {
				ids = append(ids, update.PhaseID)
			}
		}

		want := []string{"dynamic-order/phase/000000/verify", "dynamic-order/phase/000001/audit"}
		if !slices.Equal(ids, want) {
			t.Fatalf("dynamic phase IDs = %v, want %v", ids, want)
		}

		resultIDs := make([]string, 0, len(result.Phases))
		for _, phase := range result.Phases {
			resultIDs = append(resultIDs, phase.PhaseID)
		}

		if !slices.Equal(resultIDs, want) {
			t.Fatalf("Run().Phases IDs = %v, want %v", resultIDs, want)
		}
	})

	t.Run("implicit phase follows declared phases", func(t *testing.T) {
		var ids []string

		_, err := Run(t.Context(), engineDefinitionWithPhases(t, []string{"discover", "audit"}, `def main(args): return agent("prompt")`), RunRequest{RunID: "declared-order"}, inertAgent, func(_ context.Context, update PhaseUpdate) error {
			if !slices.Contains(ids, update.PhaseID) {
				ids = append(ids, update.PhaseID)
			}

			return nil
		}, discardAgentProgress)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		want := []string{"declared-order/phase/000000/discover", "declared-order/phase/000001/audit", "declared-order/phase/000002/run"}
		if !slices.Equal(ids, want) {
			t.Fatalf("phase IDs = %v, want %v", ids, want)
		}
	})

	t.Run("allows exactly one hundred dynamic phases", func(t *testing.T) {
		var source strings.Builder
		source.WriteString("def main(args):\n")

		for i := range 100 {
			fmt.Fprintf(&source, "    phase(\"phase-%d\", lambda: None)\n", i)
		}

		source.WriteString("    return None\n")

		_, err := Run(t.Context(), engineDefinition(t, source.String()), RunRequest{RunID: "dynamic-100"}, inertAgent, discardProgress, discardAgentProgress)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	})

	t.Run("rejects dynamic phase one hundred one", func(t *testing.T) {
		var source strings.Builder
		source.WriteString("def main(args):\n")

		for i := range 101 {
			fmt.Fprintf(&source, "    phase(\"phase-%d\", lambda: None)\n", i)
		}

		source.WriteString("    return None\n")

		_, err := Run(t.Context(), engineDefinition(t, source.String()), RunRequest{RunID: "dynamic-101"}, inertAgent, discardProgress, discardAgentProgress)
		if err == nil || !strings.Contains(err.Error(), "at most 100 phases") {
			t.Fatalf("Run() error = %v, want 100-phase limit", err)
		}
	})

	t.Run("declared phases count toward implicit phase limit", func(t *testing.T) {
		phases := make([]string, 100)
		for i := range phases {
			phases[i] = fmt.Sprintf("phase-%d", i)
		}

		runnerCalls, progressCalls := 0, 0

		_, err := Run(t.Context(), engineDefinitionWithPhases(t, phases, `def main(args): return agent("prompt")`), RunRequest{RunID: "declared-100"}, func(context.Context, AgentRequest, AgentThinkingFunc) (json.RawMessage, error) {
			runnerCalls++
			return json.RawMessage(`"unexpected"`), nil
		}, func(context.Context, PhaseUpdate) error {
			progressCalls++
			return nil
		}, discardAgentProgress)
		if err == nil || !strings.Contains(err.Error(), "at most 100 phases") {
			t.Fatalf("Run() error = %v, want 100-phase limit", err)
		}

		if runnerCalls != 0 || progressCalls != 200 {
			t.Fatalf("runner calls = %d, progress calls = %d, want 0 and 200", runnerCalls, progressCalls)
		}
	})

	t.Run("tuple formatting accepts empty and multiple fan-out results", func(t *testing.T) {
		for _, items := range []string{"[]", "[\"one\", \"two\"]"} {
			definition := engineDefinition(t, `
def audit(item): return item
def main(args):
    audits = pipeline(`+items+`, audit)
    return "Verify and synthesize these audits:\\n%s" % (audits,)
`)
			if _, err := Run(t.Context(), definition, RunRequest{RunID: "format"}, inertAgent, discardProgress, discardAgentProgress); err != nil {
				t.Fatalf("Run() with items %s error = %v", items, err)
			}
		}
	})

	for _, tt := range []struct{ name, phases, body, want string }{
		{name: "undeclared", phases: "[\"one\"]", body: `phase("two", lambda: None)`, want: "declared"},
		{name: "duplicate", phases: "[\"one\"]", body: `phase("one", lambda: None); phase("one", lambda: None)`, want: "once"},
		{name: "nested", phases: "[\"one\", \"two\"]", body: `phase("one", lambda: phase("two", lambda: None))`, want: "nested"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := "meta = {\"name\": \"test\", \"description\": \"test\", \"phases\": " + tt.phases + "}\ndef main(args):\n    " + tt.body + "\n"

			definition, errCompile := compileDefinition("test.star", "test", []byte(source))
			if errCompile != nil {
				t.Fatalf("compileDefinition() error = %v", errCompile)
			}

			_, err := Run(t.Context(), definition, RunRequest{RunID: tt.name}, inertAgent, discardProgress, discardAgentProgress)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestRunFanout(t *testing.T) {
	t.Run("parallel accepts a tuple", func(t *testing.T) {
		result, err := Run(t.Context(), engineDefinition(t, `def main(args): return parallel((lambda: 1, lambda: 2))`), RunRequest{RunID: "tuple"}, inertAgent, discardProgress, discardAgentProgress)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if result.Text != `[1,2]` {
			t.Fatalf("Run().Text = %s, want [1,2]", result.Text)
		}
	})

	t.Run("parallel and pipeline preserve order", func(t *testing.T) {
		definition := engineDefinition(t, `
def main(args):
    a = parallel([lambda: agent("3"), lambda: agent("1"), lambda: agent("2")])
    return pipeline(a, lambda item: agent(item))
`)

		result, err := Run(t.Context(), definition, RunRequest{RunID: "order"}, func(ctx context.Context, request AgentRequest, _ AgentThinkingFunc) (json.RawMessage, error) {
			delay := map[string]time.Duration{"3": 3 * time.Millisecond, "2": 2 * time.Millisecond, "1": time.Millisecond}[request.Prompt]
			select {
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			case <-time.After(delay):
				return json.RawMessage(fmt.Sprintf("%q", request.Prompt)), nil
			}
		}, discardProgress, discardAgentProgress)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if result.Text != `["3","1","2"]` {
			t.Fatalf("Run().Text = %s, want ordered values", result.Text)
		}
	})

	t.Run("parallel agent labels update serialized phase details", func(t *testing.T) {
		definition := engineDefinition(t, `
def main(args):
    return parallel([
        lambda: agent("first", label="alpha"),
        lambda: agent("second", label="beta"),
    ])
`)

		var updates []PhaseUpdate

		_, err := Run(t.Context(), definition, RunRequest{RunID: "labels"}, func(_ context.Context, request AgentRequest, _ AgentThinkingFunc) (json.RawMessage, error) {
			return json.RawMessage(fmt.Sprintf("%q", "private result "+request.Prompt)), nil
		}, func(_ context.Context, update PhaseUpdate) error {
			updates = append(updates, update)
			return nil
		}, discardAgentProgress)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		var scheduled []PhaseUpdate

		maximum := 0

		for _, update := range updates {
			if strings.Contains(update.Details, "private result") {
				t.Fatalf("phase details exposed worker result: %+v", update)
			}

			if update.Scheduled > maximum {
				scheduled = append(scheduled, update)
				maximum = update.Scheduled
			}
		}

		if len(scheduled) != 2 || scheduled[0].Scheduled != 1 || scheduled[1].Scheduled != 2 {
			t.Fatalf("scheduled phase updates = %+v, want exact counts 1 then 2", scheduled)
		}

		if got := []string{scheduled[0].Details, scheduled[1].Details}; !slices.Equal(slices.Sorted(slices.Values(got)), []string{"alpha", "beta"}) {
			t.Fatalf("scheduled phase details = %v, want alpha and beta", got)
		}

		terminal := updates[len(updates)-1]
		if terminal.Status != PhaseComplete || terminal.Scheduled != 2 || terminal.Running != 0 || terminal.Complete != 2 || !slices.Contains([]string{"alpha", "beta"}, terminal.Details) {
			t.Fatalf("terminal phase update = %+v, want complete 2/0/2 with latest label", terminal)
		}
	})

	t.Run("limits callback concurrency to sixteen", func(t *testing.T) {
		definition := engineDefinition(t, `def main(args): return pipeline(range(32), lambda item: agent(str(item)))`)

		var mu sync.Mutex

		active, maximum := 0, 0
		release := make(chan struct{})
		started := make(chan struct{}, 32)
		runner := func(ctx context.Context, _ AgentRequest, _ AgentThinkingFunc) (json.RawMessage, error) {
			mu.Lock()
			active++
			maximum = max(maximum, active)
			mu.Unlock()

			started <- struct{}{}

			select {
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			case <-release:
			}

			mu.Lock()
			active--
			mu.Unlock()

			return json.RawMessage(`""`), nil
		}
		done := make(chan error, 1)

		go func() {
			_, err := Run(t.Context(), definition, RunRequest{RunID: "concurrency"}, runner, discardProgress, discardAgentProgress)
			done <- err
		}()

		for range 16 {
			<-started
		}

		select {
		case <-started:
			t.Fatal("seventeenth callback started before one of sixteen completed")
		case <-time.After(10 * time.Millisecond):
		}

		close(release)

		if err := <-done; err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if maximum != 16 {
			t.Fatalf("maximum callback concurrency = %d, want 16", maximum)
		}
	})

	for _, tt := range []struct{ name, source, want string }{
		{name: "callback limit", source: `def main(args): return pipeline(range(1001), lambda item: item)`, want: "callback limit"},
		{name: "agent limit", source: `def main(args):
    for item in range(1001): agent(str(item))
    return None`, want: "agent limit"},
		{name: "frozen capture", source: `def main(args):
    values = []
    return parallel([lambda: values.append(1)])`, want: "frozen"},
		{name: "runtime nested fanout", source: `def main(args):
    fan = parallel
    return parallel([lambda: fan([])])`, want: "nested"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Run(t.Context(), engineDefinition(t, tt.source), RunRequest{RunID: tt.name}, inertAgent, discardProgress, discardAgentProgress)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() error = %v, want containing %q", err, tt.want)
			}
		})
	}

	t.Run("bounds unknown length iterable", func(t *testing.T) {
		value, err := starlark.EvalOptions(&syntax.FileOptions{}, &starlark.Thread{Name: "unknown-length"}, "test.star", `b"`+strings.Repeat("x", 1002)+`".elems()`, nil)
		if err != nil {
			t.Fatal(err)
		}

		_, err = pipelineItems(t.Context(), value.(starlark.Iterable))
		if err == nil || !strings.Contains(err.Error(), "callback limit exceeded") {
			t.Fatalf("pipelineItems() error = %v, want callback limit", err)
		}
	})

	t.Run("cancellation stops unknown length collection", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		value, err := starlark.EvalOptions(&syntax.FileOptions{}, &starlark.Thread{Name: "canceled-collection"}, "test.star", `b"x".elems()`, nil)
		if err != nil {
			t.Fatal(err)
		}

		_, err = pipelineItems(ctx, value.(starlark.Iterable))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pipelineItems() error = %v, want context canceled", err)
		}
	})

	t.Run("rejects huge range without runner calls", func(t *testing.T) {
		runnerCalls := 0

		_, err := Run(t.Context(), engineDefinition(t, `def main(args): return pipeline(range(1000000000000), lambda item: agent(str(item)))`), RunRequest{RunID: "huge-range"}, func(context.Context, AgentRequest, AgentThinkingFunc) (json.RawMessage, error) {
			runnerCalls++
			return json.RawMessage(`"unexpected"`), nil
		}, discardProgress, discardAgentProgress)
		if err == nil || !strings.Contains(err.Error(), "callback limit exceeded") {
			t.Fatalf("Run() error = %v, want callback limit", err)
		}

		if runnerCalls != 0 {
			t.Fatalf("runner calls = %d, want 0", runnerCalls)
		}
	})
}

func TestRunCancellationAndInfrastructureErrors(t *testing.T) {
	t.Run("initial progress failure skips every declared phase", func(t *testing.T) {
		errProgress := errors.New("pending progress broke")
		failed := false

		result, err := Run(t.Context(), engineDefinitionWithPhases(t, []string{"one", "two"}, `def main(args): return None`), RunRequest{RunID: "pending-progress"}, inertAgent, func(_ context.Context, update PhaseUpdate) error {
			if !failed && update.Status == PhasePending {
				failed = true
				return errProgress
			}

			return nil
		}, discardAgentProgress)
		if !errors.Is(err, errProgress) {
			t.Fatalf("Run() error = %v, want pending progress failure", err)
		}

		if got := phaseStatuses(result.Phases); !slices.Equal(got, []PhaseStatus{PhaseSkipped, PhaseSkipped}) {
			t.Fatalf("Run().Phases statuses = %v, want both skipped", got)
		}
	})

	t.Run("phase entry progress failure still emits terminal error", func(t *testing.T) {
		errEntry := errors.New("entry progress broke")
		errTerminal := errors.New("terminal progress broke")

		var updates []PhaseUpdate

		_, err := Run(t.Context(), engineDefinitionWithPhases(t, []string{"work"}, `def main(args): return phase("work", lambda: None)`), RunRequest{RunID: "entry-progress"}, inertAgent, func(ctx context.Context, update PhaseUpdate) error {
			updates = append(updates, update)
			switch update.Status {
			case PhaseInProgress:
				return errEntry
			case PhaseError:
				if ctx.Err() != nil {
					t.Fatalf("terminal progress context error = %v, want uncanceled", ctx.Err())
				}

				return errTerminal
			case PhasePending, PhaseComplete, PhaseSkipped:
				return nil
			default:
				return nil
			}
		}, discardAgentProgress)
		if !errors.Is(err, errEntry) || !errors.Is(err, errTerminal) {
			t.Fatalf("Run() error = %v, want entry and terminal progress failures", err)
		}

		if updates[len(updates)-1].Status != "error" {
			t.Fatalf("last phase update = %+v, want terminal error", updates[len(updates)-1])
		}
	})

	t.Run("runner failure emits terminal phase with uncanceled context", func(t *testing.T) {
		errRunner := errors.New("runner broke")
		errCanceledTerminal := errors.New("terminal received canceled context")
		blocked := make(chan struct{})
		canceled := make(chan struct{})
		terminal := false
		canceledTerminal := false
		definition := engineDefinitionWithPhases(t, []string{"work"}, `def main(args):
    return phase("work", lambda: parallel([lambda: agent("block"), lambda: agent("fail")]))
`)

		_, err := Run(t.Context(), definition, RunRequest{RunID: "runner-phase"}, func(ctx context.Context, request AgentRequest, _ AgentThinkingFunc) (json.RawMessage, error) {
			if request.Prompt == "fail" {
				<-blocked
				return nil, errRunner
			}

			close(blocked)
			<-ctx.Done()
			close(canceled)

			return nil, context.Cause(ctx)
		}, func(ctx context.Context, update PhaseUpdate) error {
			if update.Status == "error" {
				terminal = true

				if ctx.Err() != nil {
					canceledTerminal = true
					return errCanceledTerminal
				}
			}

			return nil
		}, discardAgentProgress)
		if !errors.Is(err, errRunner) || errors.Is(err, errCanceledTerminal) {
			t.Fatalf("Run() error = %v, want original runner failure without canceled terminal context", err)
		}

		if !terminal {
			t.Fatal("runner failure emitted no terminal phase update")
		}

		if canceledTerminal {
			t.Fatal("runner failure passed canceled context to terminal phase update")
		}

		select {
		case <-canceled:
		default:
			t.Fatal("runner failure did not cancel sibling")
		}
	})

	t.Run("cancels pure Starlark", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := Run(ctx, engineDefinition(t, `def main(args):
    while True: pass`), RunRequest{RunID: "cancel-pure"}, inertAgent, discardProgress, discardAgentProgress)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context canceled", err)
		}
	})

	t.Run("cancels blocking runner", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		started := make(chan struct{})
		done := make(chan error, 1)

		go func() {
			_, err := Run(ctx, engineDefinition(t, `def main(args): return agent("wait")`), RunRequest{RunID: "cancel-runner"}, func(ctx context.Context, _ AgentRequest, _ AgentThinkingFunc) (json.RawMessage, error) {
				close(started)
				<-ctx.Done()

				return nil, context.Cause(ctx)
			}, discardProgress, discardAgentProgress)
			done <- err
		}()

		<-started
		cancel()

		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context canceled", err)
		}
	})

	t.Run("shared step budget", func(t *testing.T) {
		_, err := Run(t.Context(), engineDefinition(t, `def spin():
    while True: pass
def main(args): return parallel([spin, spin])`), RunRequest{RunID: "steps"}, inertAgent, discardProgress, discardAgentProgress)
		if err == nil || !strings.Contains(err.Error(), "step") {
			t.Fatalf("Run() error = %v, want shared step exhaustion", err)
		}
	})

	t.Run("exhausted shared step budget cannot underflow", func(t *testing.T) {
		e := &engine{active: make(map[*starlark.Thread]uint64)}
		thread, stop := e.thread(t.Context(), "exhausted", "", false)
		_, _ = engineDefinition(t, `def main(args): return None`).program.Init(thread, e.builtins())

		stop()

		if e.remaining != 0 {
			t.Fatalf("remaining steps = %d, want 0", e.remaining)
		}
	})

	t.Run("progress failure cancels siblings", func(t *testing.T) {
		blocked := make(chan struct{})
		canceled := make(chan struct{})

		var mu sync.Mutex

		calls := 0

		_, err := Run(t.Context(), engineDefinition(t, `def main(args): return parallel([lambda: agent("a"), lambda: agent("b")])`), RunRequest{RunID: "progress-failure"}, func(ctx context.Context, _ AgentRequest, _ AgentThinkingFunc) (json.RawMessage, error) {
			select {
			case blocked <- struct{}{}:
			case <-ctx.Done():
				close(canceled)
				return nil, context.Cause(ctx)
			}

			<-ctx.Done()
			close(canceled)

			return nil, context.Cause(ctx)
		}, func(context.Context, PhaseUpdate) error {
			mu.Lock()
			defer mu.Unlock()

			calls++
			if calls >= 3 {
				return errors.New("progress broke")
			}

			return nil
		}, discardAgentProgress)
		if err == nil || !strings.Contains(err.Error(), "progress broke") {
			t.Fatalf("Run() error = %v, want progress failure", err)
		}

		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("progress failure did not cancel runners")
		}
	})
}

func TestAgentActivityLifecycle(t *testing.T) {
	for _, tt := range []struct {
		name, label, workerName, wantLabel string
	}{
		{name: "explicit label", label: "failure-trace", workerName: "trace-investigator", wantLabel: "failure-trace"},
		{name: "worker fallback", workerName: "trace-investigator", wantLabel: "trace-investigator"},
		{name: "phase call fallback", wantLabel: "investigate call 1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workerDeclaration, workerArgument := "", ""
			if tt.workerName != "" {
				workerDeclaration = fmt.Sprintf("w = worker(name=%q, instructions=\"work\")\n", tt.workerName)
				workerArgument = ", worker=w"
			}

			labelArgument := ""
			if tt.label != "" {
				labelArgument = fmt.Sprintf(", label=%q", tt.label)
			}

			definition := engineDefinitionWithPhases(t, []string{"investigate"}, workerDeclaration+`def main(args):
	return phase("investigate", lambda: agent("prompt"`+workerArgument+labelArgument+`))`)

			var updates []AgentUpdate

			result, err := Run(t.Context(), definition, RunRequest{RunID: "run"}, func(ctx context.Context, _ AgentRequest, thinking AgentThinkingFunc) (json.RawMessage, error) {
				if err := thinking(ctx, "read: prompt.md"); err != nil {
					return nil, err
				}

				if err := thinking(ctx, "grep: turn limit"); err != nil {
					return nil, err
				}

				return json.RawMessage(`"result"`), nil
			}, discardProgress, func(_ context.Context, update AgentUpdate) error {
				updates = append(updates, update)
				return nil
			})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			if result.Text != "result" {
				t.Fatalf("Run().Text = %q, want result", result.Text)
			}

			want := []AgentUpdate{
				{CallID: "run/agent/000000", PhaseID: "run/phase/000000/investigate", Label: tt.wantLabel, Activity: "read: prompt.md"},
				{CallID: "run/agent/000000", PhaseID: "run/phase/000000/investigate", Label: tt.wantLabel, Activity: "grep: turn limit"},
			}
			if !slices.Equal(updates, want) {
				t.Fatalf("agent updates = %+v, want %+v", updates, want)
			}
		})
	}
}

func TestAgentActivityParallel(t *testing.T) {
	definition := engineDefinition(t, `
def main(args):
    return parallel([
        lambda: agent("first", label="alpha"),
        lambda: agent("second", label="beta"),
    ])
`)

	var updates []AgentUpdate

	started := make(chan struct{}, 2)
	release := make(chan struct{})

	go func() {
		<-started
		<-started
		close(release)
	}()

	var completedMu sync.Mutex

	completedAgents := 0

	_, err := Run(t.Context(), definition, RunRequest{RunID: "run"}, func(ctx context.Context, request AgentRequest, thinking AgentThinkingFunc) (json.RawMessage, error) {
		started <- struct{}{}

		<-release

		if err := thinking(ctx, "working "+request.Prompt); err != nil {
			return nil, err
		}

		completedMu.Lock()
		completedAgents++
		completedMu.Unlock()

		return json.RawMessage(fmt.Sprintf("%q", request.Prompt)), nil
	}, func(_ context.Context, update PhaseUpdate) error {
		completedMu.Lock()
		defer completedMu.Unlock()

		if update.Complete > completedAgents {
			return fmt.Errorf("phase completed %d calls after only %d agent completions", update.Complete, completedAgents)
		}

		return nil
	}, func(_ context.Context, update AgentUpdate) error {
		updates = append(updates, update)
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	latest := make(map[string]AgentUpdate)
	for _, update := range updates {
		latest[update.Label] = update
	}

	if got := latest["alpha"]; got.Activity != "working first" || got.PhaseID != "run/phase/000000/run" {
		t.Fatalf("alpha update = %+v, want completed first worker", got)
	}

	if got := latest["beta"]; got.Activity != "working second" || got.PhaseID != "run/phase/000000/run" {
		t.Fatalf("beta update = %+v, want completed second worker", got)
	}

	ids := []string{latest["alpha"].CallID, latest["beta"].CallID}
	slices.Sort(ids)

	if !slices.Equal(ids, []string{"run/agent/000000", "run/agent/000001"}) {
		t.Fatalf("parallel call IDs = %q, want stable run IDs", ids)
	}
}

func TestAgentActivityProgressFailure(t *testing.T) {
	errActivity := errors.New("activity unavailable")
	runnerCalled := false

	_, err := Run(t.Context(), engineDefinition(t, `def main(args): return agent("prompt")`), RunRequest{RunID: "run"}, func(ctx context.Context, _ AgentRequest, thinking AgentThinkingFunc) (json.RawMessage, error) {
		runnerCalled = true

		if err := thinking(ctx, "working"); err != nil {
			return nil, err
		}

		return json.RawMessage(`"result"`), nil
	}, discardProgress, func(context.Context, AgentUpdate) error {
		return errActivity
	})
	if !errors.Is(err, errActivity) {
		t.Fatalf("Run() error = %v, want activity failure", err)
	}

	if !runnerCalled {
		t.Fatal("agent runner did not publish observable activity")
	}
}

func TestAgentActivityValidationFailure(t *testing.T) {
	definition := engineDefinitionWithPhases(t, []string{"verify"}, `
def main(args):
    return phase("verify", lambda: agent("prompt", label="validator", schema={"type": "object", "properties": {"count": {"type": "integer"}}, "required": ["count"]}))
`)

	var (
		agentUpdates []AgentUpdate
		phaseUpdates []PhaseUpdate
	)

	_, err := Run(t.Context(), definition, RunRequest{RunID: "run"}, func(ctx context.Context, _ AgentRequest, thinking AgentThinkingFunc) (json.RawMessage, error) {
		if err := thinking(ctx, "checking result"); err != nil {
			return nil, err
		}

		return json.RawMessage(`{"count":"invalid"}`), nil
	}, func(_ context.Context, update PhaseUpdate) error {
		phaseUpdates = append(phaseUpdates, update)
		return nil
	}, func(_ context.Context, update AgentUpdate) error {
		agentUpdates = append(agentUpdates, update)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "validate agent result") {
		t.Fatalf("Run() error = %v, want validation failure", err)
	}

	wantAgentUpdates := []AgentUpdate{
		{CallID: "run/agent/000000", PhaseID: "run/phase/000000/verify", Label: "validator", Activity: "checking result"},
	}
	if !slices.Equal(agentUpdates, wantAgentUpdates) {
		t.Fatalf("agent updates = %+v, want no post-validation lifecycle update", agentUpdates)
	}

	if len(phaseUpdates) == 0 || phaseUpdates[len(phaseUpdates)-1].Status != PhaseError || phaseUpdates[len(phaseUpdates)-1].Complete != 0 {
		t.Fatalf("phase updates = %+v, want error with zero complete", phaseUpdates)
	}
}

func engineDefinition(t *testing.T, body string) *Definition {
	t.Helper()
	return engineDefinitionWithPhases(t, nil, body)
}

func engineDefinitionWithPhases(t *testing.T, phases []string, body string) *Definition {
	t.Helper()

	phaseSource := ""

	if phases != nil {
		encoded, err := json.Marshal(phases)
		if err != nil {
			t.Fatal(err)
		}

		phaseSource = `, "phases": ` + string(encoded)
	}

	source := `meta = {"name": "test", "description": "test"` + phaseSource + "}\n" + strings.TrimSpace(body) + "\n"

	definition, err := compileDefinition("test.star", "test", []byte(source))
	if err != nil {
		t.Fatalf("compileDefinition() error = %v\n%s", err, source)
	}

	return definition
}

func inertAgent(context.Context, AgentRequest, AgentThinkingFunc) (json.RawMessage, error) {
	return json.RawMessage(`""`), nil
}

func discardProgress(context.Context, PhaseUpdate) error {
	return nil
}

func discardAgentProgress(context.Context, AgentUpdate) error {
	return nil
}

func phaseStatuses(phases []PhaseUpdate) []PhaseStatus {
	statuses := make([]PhaseStatus, 0, len(phases))
	for _, phase := range phases {
		statuses = append(statuses, phase.Status)
	}

	return statuses
}
