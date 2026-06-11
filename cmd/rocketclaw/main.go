// Command rocketclaw bridges Slack and Discord connectors to rocketcode.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
)

const (
	defaultConfigPath = "rocketclaw.json"
	legacyConfigPath  = "femtoclaw.json"
	legacyWorkDir     = ".femtoclaw"
)

type runtimeConfigFile struct {
	Path    string
	WorkDir string
	Found   bool
}

const helpText = "rocketclaw\n\nUsage:\n  rocketclaw run\n  rocketclaw setup\n  rocketclaw setup files list\n  rocketclaw setup files get <path>\n  rocketclaw doctor\n  rocketclaw lint [next|current]\n  rocketclaw agent-graph [next|current]\n  rocketclaw oai login [--headless]\n  rocketclaw fc list\n  rocketclaw fc observe [--follow|-f] [conversation-id]\n  rocketclaw fc delete [--no-vacuum] <conversation-id>\n  rocketclaw fc vacuum\n  rocketclaw help\n\nCommands:\n  run          Start rocketclaw and fail if the configuration file is missing or invalid.\n  setup        Interactively create rocketclaw.json, prepare root setup files, seed workspace overlays, and prepare .rocketclaw.\n  doctor       Validate configuration.\n  lint         Check effective RocketCode agent-system safety.\n  agent-graph  Print the effective RocketCode task delegation graph as DOT.\n  oai          Authenticate rocketclaw to ChatGPT for RocketCode model requests.\n  fc           Inspect rocketcode sessions.\n  help         Show this help screen.\n\nRunning `rocketclaw` without a subcommand starts the server when femtoclaw.json or rocketclaw.json is present.\nIf both files are missing, this help screen is shown instead.\n"

type exitCoder interface {
	ExitCode() int
}

type exitCodeError int

func (e exitCodeError) Error() string { return "" }

func (e exitCodeError) ExitCode() int { return int(e) }

func main() {
	if err := run(os.Args[1:]); err != nil {
		if message := strings.TrimSpace(err.Error()); message != "" {
			fmt.Fprintf(os.Stderr, "%s\n", message)
		}

		os.Exit(exitCodeForError(err))
	}
}

func exitCodeForError(err error) int {
	var coded exitCoder
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}

	return 1
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "run":
			return runServe(args[1:])
		case "setup":
			return runSetup(args[1:])
		case "doctor":
			return runDoctor(args[1:])
		case "lint":
			return runLint(args[1:])
		case "agent-graph":
			return runAgentGraph(args[1:])
		case "oai":
			return runOAI(args[1:])
		case "fc":
			return runFC(args[1:])
		case "help", "-h", "--help":
			return printHelp()
		}
	}

	selected, err := selectRuntimeConfigFile()
	if err != nil {
		return fmt.Errorf("stat config path: %w", err)
	}

	if !selected.Found {
		return printHelp()
	}

	return runServe(args)
}

func loadRuntimeConfig() (runtimeConfigFile, *config.Config, error) {
	selected, err := selectRuntimeConfigFile()
	if err != nil {
		return runtimeConfigFile{}, nil, err
	}

	if !selected.Found {
		return runtimeConfigFile{}, nil, os.ErrNotExist
	}

	cfg, err := config.Load(selected.Path)
	if err != nil {
		return runtimeConfigFile{}, nil, err
	}

	cfg.WorkDir = selected.WorkDir

	return selected, cfg, nil
}

func selectRuntimeConfigFile() (runtimeConfigFile, error) {
	missing, err := missingFile(legacyConfigPath)
	if err != nil {
		return runtimeConfigFile{}, err
	}

	if !missing {
		return runtimeConfigFile{Path: legacyConfigPath, WorkDir: legacyWorkDir, Found: true}, nil
	}

	missing, err = missingFile(defaultConfigPath)
	if err != nil {
		return runtimeConfigFile{}, err
	}

	if !missing {
		return runtimeConfigFile{Path: defaultConfigPath, WorkDir: config.DefaultWorkDir, Found: true}, nil
	}

	return runtimeConfigFile{}, nil
}

func missingFile(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return false, nil
	}

	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}

	return false, fmt.Errorf("stat %s: %w", path, err)
}

func printHelp() error {
	return printStdout(helpText, "help")
}

func printStdout(text, name string) error {
	_, err := fmt.Fprint(os.Stdout, text)
	if err != nil {
		return fmt.Errorf("print %s: %w", name, err)
	}

	return nil
}
