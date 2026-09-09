// Command rocketclaw bridges Slack and external MCP inputs to RocketCode.
package main

//go:generate find ../../cmd/rocketclaw ../../internal/rocketclaw ../../internal/rocketcode ( -name mocks_test.go -o -name *_mocks_test.go ) -delete
//go:generate go run -C ../.. -mod=mod github.com/vektra/mockery/v3@v3.7.4

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
	secretsARNFlag    = "aws-secrets-manager-arn"
	secretsARNUsage   = "Secrets Manager ARN of a JSON secret to merge last"
)

type runtimeConfigFile struct {
	Path, WorkDir string
	Found         bool
}

const helpText = "rocketclaw\n\nUsage:\n  rocketclaw run\n  rocketclaw lint [next|current]\n  rocketclaw agent-graph [next|current]\n  rocketclaw oai login [provider] [--headless]\n  rocketclaw oai list\n  rocketclaw oai logout [provider]\n  rocketclaw help\n\nCommands:\n  run          Start rocketclaw and fail if the configuration file is missing or invalid.\n  lint         Check effective RocketCode agent-system safety.\n  agent-graph  Print the effective RocketCode task delegation graph as DOT.\n  oai          Manage provider-specific ChatGPT credentials for RocketCode model requests.\n  help         Show this help screen.\n\nRunning `rocketclaw` without a subcommand starts the server when femtoclaw.json or rocketclaw.json is present.\nIf both files are missing, this help screen is shown instead.\n"

type exitCoder interface {
	Error() string
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
	if coded, ok := errors.AsType[exitCoder](err); ok {
		return coded.ExitCode()
	}
	return 1
}
func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "run":
			return runServe(args[1:])
		case "lint":
			return runLint(args[1:])
		case "agent-graph":
			return runAgentGraph(args[1:])
		case "oai":
			return runOAI(args[1:])
		case "help", "-h", "--help":
			return printStdout(helpText, "help")
		}
	}
	selected, err := selectRuntimeConfigFile()
	if err != nil {
		return fmt.Errorf("stat config path: %w", err)
	}
	if !selected.Found {
		return printStdout(helpText, "help")
	}
	return runServe(args)
}

var secretFetcher config.SecretFetcher = config.AWSFetcher{}

func loadRuntimeConfig(secretsARN string) (runtimeConfigFile, *config.Config, error) {
	selected, err := selectRuntimeConfigFile()
	if err != nil {
		return runtimeConfigFile{}, nil, err
	}
	if !selected.Found {
		return runtimeConfigFile{}, nil, os.ErrNotExist
	}

	cfg, err := config.Load(selected.Path, secretsARN, secretFetcher)
	if err != nil {
		return runtimeConfigFile{}, nil, err
	}
	cfg.WorkDir = selected.WorkDir
	return selected, cfg, nil
}
func selectRuntimeConfigFile() (runtimeConfigFile, error) {
	for _, candidate := range []runtimeConfigFile{
		{Path: legacyConfigPath, WorkDir: legacyWorkDir},
		{Path: defaultConfigPath, WorkDir: config.DefaultRuntimeDir},
	} {
		_, err := os.Stat(candidate.Path)
		if err == nil {
			candidate.Found = true
			return candidate, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return runtimeConfigFile{}, fmt.Errorf("stat %s: %w", candidate.Path, err)
		}
	}
	return runtimeConfigFile{}, nil
}
func printStdout(text, name string) error {
	_, err := fmt.Fprint(os.Stdout, text)
	if err != nil {
		return fmt.Errorf("print %s: %w", name, err)
	}
	return nil
}
