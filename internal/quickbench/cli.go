// Package quickbench implements BAR-based RocketCode benchmarks.
package quickbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

// Run is the quickbench CLI entrypoint.
func Run(ctx context.Context, argv0 string, args []string) error {
	cmd := commandName(argv0)
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(helpText(cmd))
		return nil
	}

	var err error

	switch args[0] {
	case "pack":
		err = runPack(args[1:])
	case "unpack":
		err = runUnpack(args[1:])
	case "dump":
		err = runDump(args[1:])
	case "run":
		err = runRun(ctx, args[1:])
	case "capture":
		err = runCapture(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], helpText(cmd))
	}

	if errors.Is(err, errHelp) {
		fmt.Print(helpText(cmd))
		return nil
	}

	return err
}

func commandName(argv0 string) string {
	cmd := argv0
	if cmd == "" {
		return "quickbench"
	}

	if filepath.Base(filepath.Clean(cmd)) == "quickbench" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Path != "" {
			return "go run " + info.Path
		}
	}

	return cmd
}

func helpText(cmd string) string {
	return fmt.Sprintf(`%[1]s — BAR benchmarks for RocketCode

Subcommands:
  pack DIR [-o OUT.bar]          Pack a BAR directory into a .bar (txtar)
  unpack FILE.bar [-o DIR]       Unpack a .bar into a directory
  dump PATH [--names]            Print BAR members (or names only)
  run PATH --model SEL [...]     Run variation×model matrix and ELO rank
  capture --conversation ID ...  Build a BAR from state.sqlite3

BAR layout:
  meta.txt                         (name, root agent, ...)
  agents/<name>.md                 (full RocketCode agent tree; required)
  variations/<id>/transcript.json  (required; final message is user)
  variations/<id>/agents/<name>/model.txt|system.txt  (optional overlays)
  mocks/tools.json                 (static tools; task is never mocked)
  mocks/bash.json                  (shell command doubles for gh/etc.)
  elo/criteria.txt                 (required)
  elo/judge.txt                    (required model selector)

Providers load from ./quickbench.json (env templates {{env.NAME}}).

Examples:
  %[1]s pack ./bench -o bench.bar
  %[1]s dump bench.bar
  %[1]s run ./bench --model gpt-5.4 --model gpt-5.4-mini
  %[1]s run ./bench --model worker=gpt-5.4-mini
  %[1]s capture --conversation slack-thread:C1:1.2 --agents ./agents -o ./captured
`, cmd)
}

func runPack(args []string) error {
	dir, out, err := parseOnePathOut(args, "", "pack")
	if err != nil {
		return err
	}

	if out == "" {
		base := filepath.Base(filepath.Clean(dir))
		if base == "." || base == string(filepath.Separator) {
			base = "bench"
		}

		out = base + ".bar"
	}

	return Pack(dir, out)
}

func runUnpack(args []string) error {
	barPath, out, err := parseOnePathOut(args, "", "unpack")
	if err != nil {
		return err
	}

	if out == "" {
		base := strings.TrimSuffix(filepath.Base(barPath), filepath.Ext(barPath))
		if base == "" {
			base = "bar"
		}

		out = base
	}

	return Unpack(barPath, out)
}

func runDump(args []string) error {
	namesOnly := false

	var path string

	for i := range args {
		switch args[i] {
		case "-h", "--help":
			return errHelp
		case "--names":
			namesOnly = true
		case "-o", "--out":
			return errors.New("dump does not take -o")
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("unknown flag %s", args[i])
			}

			if path != "" {
				return errors.New("dump accepts one path")
			}

			path = args[i]
		}
	}

	if path == "" {
		return errors.New("usage: dump PATH [--names]")
	}

	bar, err := Open(path)
	if err != nil {
		return err
	}

	return Dump(os.Stdout, bar, namesOnly)
}

func runRun(ctx context.Context, args []string) error {
	opt := runOptions{namedModels: map[string]modelSelector{}}

	var path string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return errHelp
		case arg == "--json":
			opt.json = true
		case arg == "--model" || strings.HasPrefix(arg, "--model="):
			value, next, err := flagValue(args, i, "--model")
			if err != nil {
				return err
			}

			i = next

			name, selRaw, hasName := strings.Cut(value, "=")
			if hasName && name != "" && !strings.Contains(name, "?") {
				sel, err := parseModelSelector(selRaw)
				if err != nil {
					return fmt.Errorf("--model %s=: %w", name, err)
				}

				opt.namedModels[name] = sel
			} else {
				sel, err := parseModelSelector(value)
				if err != nil {
					return fmt.Errorf("--model: %w", err)
				}

				opt.rootModels = append(opt.rootModels, sel)
			}
		case arg == "--judge" || strings.HasPrefix(arg, "--judge="):
			value, next, err := flagValue(args, i, "--judge")
			if err != nil {
				return err
			}

			i = next

			sel, err := parseModelSelector(value)
			if err != nil {
				return fmt.Errorf("--judge: %w", err)
			}

			opt.judge = &sel
		case arg == "--timeout" || strings.HasPrefix(arg, "--timeout="):
			value, next, err := flagValue(args, i, "--timeout")
			if err != nil {
				return err
			}

			i = next

			d, err := time.ParseDuration(value)
			if err != nil {
				return fmt.Errorf("--timeout: %w", err)
			}

			if d <= 0 {
				return errors.New("--timeout must be positive")
			}

			opt.timeout = d
			opt.timeoutOK = true
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("unknown flag %s", arg)
		default:
			if path != "" {
				return errors.New("run accepts one BAR path")
			}

			path = arg
		}
	}

	if path == "" {
		return errors.New("usage: run PATH [--model SEL|--model agent=SEL]")
	}

	bar, err := Open(path)
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	selectors := append([]modelSelector(nil), opt.rootModels...)
	for _, sel := range opt.namedModels {
		selectors = append(selectors, sel)
	}
	// Collect models declared on agents for provider load.
	agents, rootName, err := buildAgents(bar, Variation{}, nil, nil)
	if err != nil {
		return err
	}

	for _, agent := range agents.Items {
		if sel, err := parseModelSelector(agent.Model); err == nil {
			selectors = append(selectors, sel)
		}
	}

	_ = rootName

	if opt.judge != nil {
		selectors = append(selectors, *opt.judge)
	} else {
		judgeSel, err := parseModelSelector(bar.Judge)
		if err != nil {
			return fmt.Errorf("elo/judge.txt: %w", err)
		}

		selectors = append(selectors, judgeSel)
	}

	providers, err := loadProviderConfig(filepath.Join(cwd, "quickbench.json"), selectors)
	if err != nil {
		return err
	}

	report, err := runBAR(ctx, providers, bar, opt)
	if err != nil {
		return err
	}

	if opt.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")

		return enc.Encode(report)
	}

	return writeHumanReport(os.Stdout, report)
}

func runCapture(ctx context.Context, args []string) error {
	var (
		dbPath, conversation, out, name, agentsDir, root string
		pack                                             bool
	)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return errHelp
		case arg == "--pack":
			pack = true
		case arg == "--db" || strings.HasPrefix(arg, "--db="):
			value, next, err := flagValue(args, i, "--db")
			if err != nil {
				return err
			}

			i = next
			dbPath = value
		case arg == "--conversation" || strings.HasPrefix(arg, "--conversation="):
			value, next, err := flagValue(args, i, "--conversation")
			if err != nil {
				return err
			}

			i = next
			conversation = value
		case arg == "--agents" || strings.HasPrefix(arg, "--agents="):
			value, next, err := flagValue(args, i, "--agents")
			if err != nil {
				return err
			}

			i = next
			agentsDir = value
		case arg == "--root" || strings.HasPrefix(arg, "--root="):
			value, next, err := flagValue(args, i, "--root")
			if err != nil {
				return err
			}

			i = next
			root = value
		case arg == "-o" || arg == "--out" || strings.HasPrefix(arg, "--out="):
			flag := "--out"
			if arg == "-o" {
				flag = "-o"
			}

			value, next, err := flagValue(args, i, flag)
			if err != nil {
				return err
			}

			i = next
			out = value
		case arg == "--name" || strings.HasPrefix(arg, "--name="):
			value, next, err := flagValue(args, i, "--name")
			if err != nil {
				return err
			}

			i = next
			name = value
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("unknown flag %s", arg)
		default:
			return fmt.Errorf("unexpected argument %q", arg)
		}
	}

	if conversation == "" {
		return errors.New("usage: capture --conversation ID [--agents DIR] [--root NAME] [--db PATH] [-o OUT] [--name VAR] [--pack]")
	}

	if dbPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}

		dbPath = filepath.Join(cwd, ".rocketclaw", "state.sqlite3")
	}

	if agentsDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}

		agentsDir = filepath.Join(cwd, "agents")
	}

	if out == "" {
		if pack {
			out = "captured.bar"
		} else {
			out = "captured"
		}
	}

	return Capture(ctx, CaptureOptions{
		DBPath:         dbPath,
		ConversationID: conversation,
		AgentsDir:      agentsDir,
		Root:           root,
		Out:            out,
		Variation:      name,
		Pack:           pack || strings.HasSuffix(strings.ToLower(out), ".bar"),
	})
}

var errHelp = errors.New("help requested")

func parseOnePathOut(args []string, defaultOut, usage string) (path, out string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return "", "", errHelp
		case arg == "-o" || arg == "--out" || strings.HasPrefix(arg, "--out="):
			flag := "--out"
			if arg == "-o" {
				flag = "-o"
			}

			value, next, err := flagValue(args, i, flag)
			if err != nil {
				return "", "", err
			}

			i = next
			out = value
		case strings.HasPrefix(arg, "-"):
			return "", "", fmt.Errorf("unknown flag %s", arg)
		default:
			if path != "" {
				return "", "", fmt.Errorf("%s accepts one path", usage)
			}

			path = arg
		}
	}

	if path == "" {
		return "", "", fmt.Errorf("usage: %s PATH [-o OUT]", usage)
	}

	if out == "" {
		out = defaultOut
	}

	return path, out, nil
}

func flagValue(args []string, i int, name string) (string, int, error) {
	arg := args[i]
	if after, ok := strings.CutPrefix(arg, name+"="); ok {
		return after, i, nil
	}

	if arg != name {
		return "", i, fmt.Errorf("internal flag parse for %s", name)
	}

	if i+1 >= len(args) {
		return "", i, fmt.Errorf("%s requires a value", name)
	}

	return args[i+1], i + 1, nil
}
