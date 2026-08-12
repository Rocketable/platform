// Package quickbench implements BAR-based RocketCode benchmarks.
package quickbench

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
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

	if errors.Is(err, flag.ErrHelp) {
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
  dump PATH [-names]             Print BAR members (or names only)
  run PATH                       Run variation×matrix cells and ELO rank
  capture -conversation ID ...   Build a BAR from state.sqlite3

BAR layout (pack/dump order — edit bench.yaml first):
  bench.yaml                       (name, root, matrix, elo.model/criteria)
  mocks/tools.json                 (static tools; task is never mocked)
  mocks/bash.json                  (shell command doubles for gh/etc.)
  variations/<id>/transcript.json  (required; final message is user)
  variations/<id>/agents/<name>/model.txt|system.txt  (optional overlays)
  agents/<name>.md                 (full RocketCode agent tree; required)

Subject models come from bench.yaml matrix (not CLI). Omit matrix for one
default cell using agent models as written in agents/*.md.

Providers load from ./quickbench.json (env templates {{env.NAME}}).

Examples:
  %[1]s pack ./bench -o bench.bar
  %[1]s dump bench.bar
  %[1]s run ./bench
  %[1]s capture -conversation slack-thread:C1:1.2 -agents ./agents -o ./captured
`, cmd)
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	return fs
}

func runPack(args []string) error {
	fs := newFlagSet("pack")
	out := fs.String("o", "", "output .bar path")
	fs.StringVar(out, "out", "", "output .bar path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("usage: pack DIR [-o OUT.bar]")
	}

	dir := fs.Arg(0)
	if *out == "" {
		base := filepath.Base(filepath.Clean(dir))
		if base == "." || base == string(filepath.Separator) {
			base = "bench"
		}

		*out = base + ".bar"
	}

	return Pack(dir, *out)
}

func runUnpack(args []string) error {
	fs := newFlagSet("unpack")
	out := fs.String("o", "", "output directory")
	fs.StringVar(out, "out", "", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("usage: unpack FILE.bar [-o DIR]")
	}

	barPath := fs.Arg(0)
	if *out == "" {
		base := strings.TrimSuffix(filepath.Base(barPath), filepath.Ext(barPath))
		if base == "" {
			base = "bar"
		}

		*out = base
	}

	return Unpack(barPath, *out)
}

func runDump(args []string) error {
	fs := newFlagSet("dump")
	namesOnly := fs.Bool("names", false, "list member names only")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("usage: dump PATH [-names]")
	}

	bar, err := Open(fs.Arg(0))
	if err != nil {
		return err
	}

	return Dump(os.Stdout, bar, *namesOnly)
}

func runRun(ctx context.Context, args []string) error {
	fs := newFlagSet("run")
	jsonOut := fs.Bool("json", false, "print JSON report")
	timeout := fs.Duration("timeout", 0, "per-cell timeout (0 = none)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("usage: run PATH [-timeout DUR] [-json]")
	}

	bar, err := Open(fs.Arg(0))
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	var selectors []modelSelector
	// Models from agent files and matrix rows for provider load.
	agents, _, err := buildAgents(bar, Variation{}, nil)
	if err != nil {
		return err
	}

	for _, agent := range agents.Items {
		if sel, err := parseModelSelector(agent.Model); err == nil {
			selectors = append(selectors, sel)
		}
	}

	matrix := bar.Matrix
	if len(matrix) == 0 {
		matrix = []MatrixEntry{{ID: "default"}}
	}

	for _, entry := range matrix {
		for _, ov := range entry.Agents {
			if ov.Model.Raw != "" {
				selectors = append(selectors, ov.Model)
			}
		}
	}

	judgeSel, err := parseModelSelector(bar.Judge)
	if err != nil {
		return fmt.Errorf("bench.yaml elo.model: %w", err)
	}

	selectors = append(selectors, judgeSel)

	providers, err := loadProviderConfig(filepath.Join(cwd, "quickbench.json"), selectors)
	if err != nil {
		return err
	}

	report, err := runBAR(ctx, providers, bar, runOptions{
		json:    *jsonOut,
		timeout: *timeout,
	})
	if err != nil {
		return err
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")

		return enc.Encode(report)
	}

	return writeHumanReport(os.Stdout, report)
}

func runCapture(ctx context.Context, args []string) error {
	fs := newFlagSet("capture")
	conversation := fs.String("conversation", "", "conversation id")
	dbPath := fs.String("db", "", "state.sqlite3 path")
	agentsDir := fs.String("agents", "", "workspace agents directory")
	root := fs.String("root", "", "root agent name")
	out := fs.String("o", "", "output path")
	fs.StringVar(out, "out", "", "output path")
	name := fs.String("name", "", "variation id")
	pack := fs.Bool("pack", false, "write a .bar archive")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	if strings.TrimSpace(*conversation) == "" {
		return errors.New("usage: capture -conversation ID [-agents DIR] [-root NAME] [-db PATH] [-o OUT] [-name VAR] [-pack]")
	}

	if *dbPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}

		*dbPath = filepath.Join(cwd, ".rocketclaw", "state.sqlite3")
	}

	if *agentsDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}

		*agentsDir = filepath.Join(cwd, "agents")
	}

	if *out == "" {
		if *pack {
			*out = "captured.bar"
		} else {
			*out = "captured"
		}
	}

	return Capture(ctx, CaptureOptions{
		DBPath:         *dbPath,
		ConversationID: *conversation,
		AgentsDir:      *agentsDir,
		Root:           *root,
		Out:            *out,
		Variation:      *name,
		Pack:           *pack || strings.HasSuffix(strings.ToLower(*out), ".bar"),
	})
}
