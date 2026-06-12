package quickbench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseOptionsAllowsInterspersedFlags(t *testing.T) {
	opt, err := parseOptions([]string{"./benches", "--model", "openai/gpt-5.5?reasoningEffort=high&verbosity=low", "--runs=2", "--json", "--timeout", "30s"})
	if err != nil {
		t.Fatalf("parseOptions returned error: %v", err)
	}

	if !opt.json {
		t.Fatalf("json = false, want true")
	}

	if opt.runs != 2 {
		t.Fatalf("runs = %d, want 2", opt.runs)
	}

	if opt.timeout != 30*time.Second || !opt.timeoutOK {
		t.Fatalf("timeout = %s ok=%v, want 30s true", opt.timeout, opt.timeoutOK)
	}

	if len(opt.models) != 1 || opt.models[0].Provider != "openai" || opt.models[0].Model != "gpt-5.5" {
		t.Fatalf("models = %+v, want openai/gpt-5.5", opt.models)
	}
}

func TestParseModelSelectorRejectsUnknownOption(t *testing.T) {
	_, err := parseModelSelector("openai/gpt-5.5?temperature=0")
	if err == nil || !strings.Contains(err.Error(), `unknown option "temperature"`) {
		t.Fatalf("parseModelSelector error = %v, want unknown option", err)
	}
}

func TestLoadProviderConfigInterpolatesEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quickbench.json")
	if err := os.WriteFile(path, []byte(`{"providers":{"openai":{"apiKey":"{{ env.QUICKBENCH_TEST_OPENAI_KEY }}"}}}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("QUICKBENCH_TEST_OPENAI_KEY", "test-key")
	providers, err := loadProviderConfig(path, []modelSelector{{Provider: "openai"}})
	if err != nil {
		t.Fatalf("loadProviderConfig returned error: %v", err)
	}

	if providers.OpenAI == nil {
		t.Fatalf("OpenAI provider is nil")
	}
}

func TestLoadProviderConfigFailsOnMissingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quickbench.json")
	if err := os.WriteFile(path, []byte(`{"providers":{"openai":{"apiKey":"{{ env.QUICKBENCH_TEST_MISSING_KEY }}"}}}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := loadProviderConfig(path, []modelSelector{{Provider: "openai"}})
	if err == nil || !strings.Contains(err.Error(), "QUICKBENCH_TEST_MISSING_KEY") {
		t.Fatalf("loadProviderConfig error = %v, want missing env", err)
	}
}

func TestLoadProviderConfigIgnoresUnselectedProviderEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quickbench.json")
	if err := os.WriteFile(path, []byte(`{"providers":{"openai":{"apiKey":"{{ env.QUICKBENCH_TEST_OPENAI_KEY }}"},"anthropic":{"apiKey":"{{ env.QUICKBENCH_TEST_MISSING_ANTHROPIC_KEY }}"}}}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("QUICKBENCH_TEST_OPENAI_KEY", "test-key")
	providers, err := loadProviderConfig(path, []modelSelector{{Provider: "openai"}})
	if err != nil {
		t.Fatalf("loadProviderConfig returned error: %v", err)
	}

	if providers.OpenAI == nil {
		t.Fatalf("OpenAI provider is nil")
	}
}

func TestScanBenchmarkFilesRecursesAndSortsYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	for _, path := range []string{"b.yml", "nested/a.yaml", "ignored.txt"} {
		fullPath := filepath.Join(dir, path)
		if err := os.WriteFile(fullPath, []byte("name: x\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	paths, err := scanBenchmarkFiles(dir)
	if err != nil {
		t.Fatalf("scanBenchmarkFiles returned error: %v", err)
	}

	want := []string{filepath.Join(dir, "b.yml"), filepath.Join(dir, "nested/a.yaml")}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestLoadBenchmarkFileValidatesStaticBenchmark(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bench.yaml")
	data := []byte(`name: enum tool choice
runs: 3
timeout: 2m
tools:
  - name: choose_route
    description: Pick the route.
    parameters:
      type: object
      required: [route]
      properties:
        route:
          type: string
          enum: [fast, cheap, safe]
    static:
      response: '{"ok": true}'
inference:
  - system: You must call choose_route.
  - user: Pick the safest route.
expected:
  text:
    - regexp: '.*'
  tools:
    calls:
      - name: choose_route
        arguments:
          route: safe
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write benchmark: %v", err)
	}

	file, err := loadBenchmarkFile(path)
	if err != nil {
		t.Fatalf("loadBenchmarkFile returned error: %v", err)
	}

	if file.Benchmark.duration != 2*time.Minute {
		t.Fatalf("duration = %s, want 2m", file.Benchmark.duration)
	}
}

func TestLoadBenchmarkFileRejectsUnsupportedBackend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bench.yaml")
	data := []byte(`name: cli tool
tools:
  - name: get
    cli:
      command: get
inference:
  - user: run
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write benchmark: %v", err)
	}

	_, err := loadBenchmarkFile(path)
	if err == nil || !strings.Contains(err.Error(), "only static is implemented") {
		t.Fatalf("loadBenchmarkFile error = %v, want unsupported backend", err)
	}
}

func TestEvaluateAssertionsMatchesSubsetArguments(t *testing.T) {
	failures := evaluateAssertions(expected{Tools: toolExpected{Calls: []toolCallExpected{{Name: "choose_route", Arguments: map[string]any{"route": "safe", "nested": map[string]any{"count": 1}}}}}}, "", []observedToolCall{{Name: "choose_route", Arguments: map[string]any{"route": "safe", "extra": true, "nested": map[string]any{"count": float64(1), "ignored": "yes"}}}})
	if len(failures) != 0 {
		t.Fatalf("failures = %v, want none", failures)
	}
}

func TestEvaluateAssertionsChecksOrderedToolCalls(t *testing.T) {
	failures := evaluateAssertions(expected{Tools: toolExpected{Ordered: true, Calls: []toolCallExpected{{Name: "second"}, {Name: "first"}}}}, "", []observedToolCall{{Name: "first"}, {Name: "second"}})
	if len(failures) == 0 {
		t.Fatalf("failures = none, want ordered failure")
	}
}
