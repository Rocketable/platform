package quickbench

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type benchmarkFile struct {
	Path      string
	Benchmark benchmark
}

type benchmark struct {
	Name        string          `yaml:"name"`
	Description string          `yaml:"description"`
	Tags        []string        `yaml:"tags"`
	Runs        int             `yaml:"runs"`
	Timeout     string          `yaml:"timeout"`
	Tools       []benchmarkTool `yaml:"tools"`
	Inference   []message       `yaml:"inference"`
	Expected    expected        `yaml:"expected"`

	duration time.Duration
}

type benchmarkTool struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Parameters  map[string]any `yaml:"parameters"`
	Static      *staticTool    `yaml:"static"`
	CLI         map[string]any `yaml:"cli"`
	HTTP        map[string]any `yaml:"http"`
	MCP         map[string]any `yaml:"mcp"`
}

type staticTool struct {
	Response string `yaml:"response"`
}

type message struct {
	Role string
	Text string
}

type expected struct {
	Text  []textAssertion `yaml:"text"`
	Tools toolExpected    `yaml:"tools"`
}

type textAssertion struct {
	Regexp string `yaml:"regexp"`
}

type toolExpected struct {
	Ordered bool               `yaml:"ordered"`
	Calls   []toolCallExpected `yaml:"calls"`
}

type toolCallExpected struct {
	Name      string         `yaml:"name"`
	Arguments map[string]any `yaml:"arguments"`
}

func loadBenchmarkFile(path string) (benchmarkFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return benchmarkFile{}, fmt.Errorf("%s: read benchmark: %w", path, err)
	}

	var bench benchmark
	if err := yaml.Unmarshal(data, &bench); err != nil {
		return benchmarkFile{}, fmt.Errorf("%s: parse YAML: %w", path, err)
	}

	if err := validateBenchmark(&bench); err != nil {
		return benchmarkFile{}, fmt.Errorf("%s: %w", path, err)
	}

	return benchmarkFile{Path: path, Benchmark: bench}, nil
}

func (m *message) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode || len(value.Content) != 2 {
		return errors.New("inference item must be a mapping with one role")
	}

	role := value.Content[0]
	text := value.Content[1]
	if role.Kind != yaml.ScalarNode || text.Kind != yaml.ScalarNode {
		return errors.New("inference item role and text must be scalars")
	}

	m.Role = role.Value
	m.Text = text.Value

	return nil
}

func validateBenchmark(bench *benchmark) error {
	if strings.TrimSpace(bench.Name) == "" {
		return errors.New("name is required")
	}

	if bench.Runs < 0 {
		return errors.New("runs must be positive")
	}

	if strings.TrimSpace(bench.Timeout) != "" {
		duration, err := time.ParseDuration(bench.Timeout)
		if err != nil || duration <= 0 {
			return errors.New("timeout must be a positive Go duration")
		}

		bench.duration = duration
	}

	if len(bench.Inference) == 0 {
		return errors.New("inference is required")
	}

	systemCount := 0
	for i, msg := range bench.Inference {
		switch msg.Role {
		case "system":
			systemCount++
			if i != 0 {
				return errors.New("system inference message must be first")
			}
		case "user", "assistant":
		default:
			return fmt.Errorf("inference role %q is not supported", msg.Role)
		}
	}

	if systemCount > 1 {
		return errors.New("inference may contain at most one system message")
	}

	last := bench.Inference[len(bench.Inference)-1]
	if last.Role != "user" || strings.TrimSpace(last.Text) == "" {
		return errors.New("inference must end with a non-empty user message")
	}

	seenTools := map[string]bool{}
	for _, tool := range bench.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			return errors.New("tool name is required")
		}

		if seenTools[tool.Name] {
			return fmt.Errorf("tool %q is duplicated", tool.Name)
		}

		seenTools[tool.Name] = true
		backends := 0
		if tool.Static != nil {
			backends++
		}

		if tool.CLI != nil {
			backends++
		}

		if tool.HTTP != nil {
			backends++
		}

		if tool.MCP != nil {
			backends++
		}

		if backends != 1 {
			return fmt.Errorf("tool %q must define exactly one backend", tool.Name)
		}

		if tool.Static == nil {
			return fmt.Errorf("tool %q uses an unsupported backend; only static is implemented", tool.Name)
		}
	}

	for _, assertion := range bench.Expected.Text {
		if strings.TrimSpace(assertion.Regexp) == "" {
			return errors.New("expected.text regexp is required")
		}

		if _, err := regexp.Compile(assertion.Regexp); err != nil {
			return fmt.Errorf("expected.text regexp %q: %w", assertion.Regexp, err)
		}
	}

	for _, call := range bench.Expected.Tools.Calls {
		if strings.TrimSpace(call.Name) == "" {
			return errors.New("expected.tools.calls name is required")
		}

		if !seenTools[call.Name] {
			return fmt.Errorf("expected tool %q is not declared", call.Name)
		}
	}

	return nil
}
