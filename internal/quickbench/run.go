// Package quickbench implements the quickbench benchmark runner.
package quickbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

type options struct {
	dir       string
	models    []modelSelector
	json      bool
	runs      int
	timeout   time.Duration
	timeoutOK bool
}

// Run executes quickbench with argv0 and args.
func Run(ctx context.Context, argv0 string, args []string) error {
	cmd := argv0
	if cmd == "" {
		cmd = "quickbench"
	} else if filepath.Base(filepath.Clean(cmd)) == "quickbench" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Path != "" {
			cmd = "go run " + info.Path
		}
	}

	opt, err := parseOptions(args)
	if err != nil {
		if errors.Is(err, errHelp) {
			fmt.Print(helpText(cmd))
			return nil
		}

		return fmt.Errorf("parse flags: %w\n\n%s", err, helpText(cmd))
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	providers, err := loadProviderConfig(filepath.Join(cwd, "quickbench.json"), opt.models)
	if err != nil {
		return err
	}

	paths, err := scanBenchmarkFiles(opt.dir)
	if err != nil {
		return err
	}

	benches := make([]benchmarkFile, 0, len(paths))
	for _, path := range paths {
		bench, err := loadBenchmarkFile(path)
		if err != nil {
			return err
		}

		benches = append(benches, bench)
	}

	results := make([]fileResult, len(benches))
	group, groupCtx := errgroup.WithContext(ctx)
	for i := range benches {
		i := i
		group.Go(func() error {
			fileResults, err := runBenchmarkFile(groupCtx, providers, opt, benches[i])
			if err != nil {
				return err
			}

			results[i] = fileResults

			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return err
	}

	report := report{Files: results}
	if opt.json {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return fmt.Errorf("write JSON report: %w", err)
		}

		return nil
	}

	if err := writeHumanReport(os.Stdout, report); err != nil {
		return err
	}

	return nil
}

var errHelp = errors.New("help requested")

func parseOptions(args []string) (options, error) {
	var opt options
	positionals := []string{}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return options{}, errHelp
		case arg == "--json":
			opt.json = true
		case arg == "--model":
			i++
			if i >= len(args) {
				return options{}, errors.New("--model requires a value")
			}

			model, err := parseModelSelector(args[i])
			if err != nil {
				return options{}, fmt.Errorf("--model: %w", err)
			}

			opt.models = append(opt.models, model)
		case strings.HasPrefix(arg, "--model="):
			model, err := parseModelSelector(strings.TrimPrefix(arg, "--model="))
			if err != nil {
				return options{}, fmt.Errorf("--model: %w", err)
			}

			opt.models = append(opt.models, model)
		case arg == "--runs":
			i++
			if i >= len(args) {
				return options{}, errors.New("--runs requires a value")
			}

			runs, err := strconv.Atoi(args[i])
			if err != nil || runs <= 0 {
				return options{}, errors.New("--runs must be a positive integer")
			}

			opt.runs = runs
		case strings.HasPrefix(arg, "--runs="):
			runs, err := strconv.Atoi(strings.TrimPrefix(arg, "--runs="))
			if err != nil || runs <= 0 {
				return options{}, errors.New("--runs must be a positive integer")
			}

			opt.runs = runs
		case arg == "--timeout":
			i++
			if i >= len(args) {
				return options{}, errors.New("--timeout requires a value")
			}

			timeout, err := time.ParseDuration(args[i])
			if err != nil || timeout <= 0 {
				return options{}, errors.New("--timeout must be a positive duration")
			}

			opt.timeout = timeout
			opt.timeoutOK = true
		case strings.HasPrefix(arg, "--timeout="):
			timeout, err := time.ParseDuration(strings.TrimPrefix(arg, "--timeout="))
			if err != nil || timeout <= 0 {
				return options{}, errors.New("--timeout must be a positive duration")
			}

			opt.timeout = timeout
			opt.timeoutOK = true
		case strings.HasPrefix(arg, "-"):
			return options{}, fmt.Errorf("unknown flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}

	if len(positionals) != 1 {
		return options{}, errors.New("usage requires exactly one benchmark directory")
	}

	if len(opt.models) == 0 {
		return options{}, errors.New("at least one --model is required")
	}

	dir, err := filepath.Abs(positionals[0])
	if err != nil {
		return options{}, fmt.Errorf("resolve benchmark directory: %w", err)
	}

	opt.dir = dir

	return opt, nil
}

func scanBenchmarkFiles(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat benchmark directory: %w", err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("benchmark path %q is not a directory", dir)
	}

	paths := []string{}
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".yaml" || ext == ".yml" {
			paths = append(paths, path)
		}

		return nil
	}); err != nil {
		return nil, fmt.Errorf("scan benchmark directory: %w", err)
	}

	slices.Sort(paths)

	return paths, nil
}

func helpText(cmd string) string {
	return fmt.Sprintf(`quickbench runs YAML LLM benchmarks through RocketCode.

Usage:

  %[1]s [--json] [--runs N] [--timeout 2m] --model provider/model[?option=value] FULL_PATH_TO_DIR

Flags:

  --model    Model selector. Repeat for multiple models. Example: openai/gpt-5.5?reasoningEffort=high&verbosity=low
  --json     Write JSON report instead of human-readable output.
  --runs     Override YAML runs with a positive integer.
  --timeout  Override YAML timeout with a positive Go duration.

Configuration:

  quickbench loads provider config from ./quickbench.json only.
`, cmd)
}

func writeHumanReport(w io.Writer, result report) error {
	totalRuns := 0
	passedRuns := 0

	for _, file := range result.Files {
		if _, err := fmt.Fprintf(w, "%s\n", file.Path); err != nil {
			return fmt.Errorf("write report: %w", err)
		}

		for _, model := range file.Models {
			for _, run := range model.Runs {
				totalRuns++
				if run.Passed {
					passedRuns++
				}

				status := "FAIL"
				if run.Passed {
					status = "PASS"
				}

				if _, err := fmt.Fprintf(w, "  %s run %d: %s latency=%s\n", model.Model, run.Run, status, run.Latency); err != nil {
					return fmt.Errorf("write report: %w", err)
				}

				for _, failure := range run.Failures {
					if _, err := fmt.Fprintf(w, "    - %s\n", failure); err != nil {
						return fmt.Errorf("write report: %w", err)
					}
				}

				if run.Error != "" {
					if _, err := fmt.Fprintf(w, "    - error: %s\n", run.Error); err != nil {
						return fmt.Errorf("write report: %w", err)
					}
				}
			}
		}
	}

	if _, err := fmt.Fprintf(w, "\nSummary: %d/%d runs passed\n", passedRuns, totalRuns); err != nil {
		return fmt.Errorf("write report: %w", err)
	}

	return nil
}
