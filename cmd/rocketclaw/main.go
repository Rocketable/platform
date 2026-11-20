package main

//go:generate find ../../cmd/rocketclaw ../../internal/rocketclaw ( -name mocks_test.go -o -name *_mocks_test.go ) -delete
//go:generate go run -C ../.. -mod=mod -modfile=tools/go.mod github.com/vektra/mockery/v3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
)

const (
	defaultConfigPath = "rocketclaw.json"
	legacyConfigPath  = "femtoclaw.json"
	legacyWorkDir     = ".femtoclaw"
	secretsARNFlag    = "aws-secrets-manager-arn"
	secretsARNUsage   = "Secrets Manager ARN of a JSON secret to merge last"
	usersARNFlag      = "aws-secrets-manager-users-arn"
)

type runtimeConfigFile struct {
	Path, WorkDir string
}

const helpText = "rocketclaw\n\nUsage:\n  rocketclaw run [--pprof=false|HOST:PORT] [--aws-secrets-manager-arn ARN] [--aws-secrets-manager-users-arn ARN]\n  rocketclaw oai login [provider] [--headless]\n  rocketclaw oai list\n  rocketclaw oai logout [provider]\n  rocketclaw ts login\n  rocketclaw help\n\nCommands:\n  run          Start rocketclaw and fail if the configuration is missing or invalid.\n  oai          Manage provider-specific ChatGPT credentials for model requests.\n  ts           Log the Web's Tailscale node (web.tsnet) in to the tailnet.\n  help         Show this help screen.\n\nRun options:\n  --pprof      Serve private Go profiles at 127.0.0.1:6060; enabled by default, not saved in config.\n               Use --pprof=HOST:PORT to choose another address, or --pprof=false to turn profiling off.\n  --aws-secrets-manager-arn\n               Merge this JSON secret over rocketclaw.json; with it, rocketclaw.json is optional.\n  --aws-secrets-manager-users-arn\n               Use this JSON secret of login to password as mcp_external.users, replacing the configured logins.\n\nRunning `rocketclaw` without a subcommand starts the server when femtoclaw.json or rocketclaw.json is present.\nIf both files are missing, this help screen is shown instead.\n"

type exitCoder interface {
	Error() string
	ExitCode() int
}
type exitCodeError int

func (e exitCodeError) Error() string { return "" }
func (e exitCodeError) ExitCode() int { return int(e) }
func main() {
	if code, ok := sandbox.Guest(os.Args); ok {
		os.Exit(code)
	}

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
		case "oai":
			return runOAI(args[1:])
		case "ts":
			return runTS(args[1:])
		case "help", "-h", "--help":
			return printStdout(helpText, "help")
		}
	}
	selected, err := selectRuntimeConfigFile()
	if err != nil {
		return fmt.Errorf("stat config path: %w", err)
	}
	if selected.Path == "" {
		return printStdout(helpText, "help")
	}
	return runServe(args)
}

var (
	secretFetcher config.SecretFetcher = &config.AWSFetcher{}

	errUsersSecretShape = errors.New("secret must be a JSON object of login to password strings")
)

func loadRuntimeConfig(secretsARN, usersARN string) (runtimeConfigFile, *config.Config, error) {
	selected, err := selectRuntimeConfigFile()
	if err != nil {
		return runtimeConfigFile{}, nil, err
	}
	if selected.Path == "" {
		selected = runtimeConfigFile{Path: defaultConfigPath, WorkDir: config.DefaultRuntimeDir}
	}

	cfg, err := config.Load(selected.Path, secretsARN, secretFetcher)
	if err != nil {
		return runtimeConfigFile{}, nil, err
	}
	cfg.WorkDir = selected.WorkDir

	if usersARN != "" {
		body, err := secretFetcher.SecretString(context.Background(), usersARN)
		if err != nil {
			return runtimeConfigFile{}, nil, fmt.Errorf("read --%s: %w", usersARNFlag, err)
		}

		var users map[string]string
		if err := json.Unmarshal([]byte(body), &users); err != nil || users == nil {
			return runtimeConfigFile{}, nil, fmt.Errorf("read --%s %s: %w", usersARNFlag, usersARN, errUsersSecretShape)
		}

		cfg.MCPExternal.Users = users
	}

	return selected, cfg, nil
}
func selectRuntimeConfigFile() (runtimeConfigFile, error) {
	for _, candidate := range []runtimeConfigFile{
		{Path: legacyConfigPath, WorkDir: legacyWorkDir},
		{Path: defaultConfigPath, WorkDir: config.DefaultRuntimeDir},
	} {
		_, err := os.Stat(candidate.Path)
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return runtimeConfigFile{}, fmt.Errorf("stat %s: %w", candidate.Path, err)
		}
	}
	return runtimeConfigFile{}, nil
}
func printStdout(text, name string) error {
	if _, err := fmt.Fprint(os.Stdout, text); err != nil {
		return fmt.Errorf("print %s: %w", name, err)
	}
	return nil
}
