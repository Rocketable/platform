package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/oai"
)

const oaiHelpText = `rocketclaw oai

Usage:
  rocketclaw oai login [--headless]

Commands:
  login  Authenticate with ChatGPT for RocketCode model requests.
`

func runOAI(args []string) error {
	if len(args) == 0 {
		return printStdout(oaiHelpText, "oai help")
	}

	switch args[0] {
	case "login":
		return runOAILogin(args[1:])
	case "help", "-h", "--help":
		return printStdout(oaiHelpText, "oai help")
	default:
		return fmt.Errorf("unknown oai command %q", args[0])
	}
}

func runOAILogin(args []string) error {
	headless := false

	for _, arg := range args {
		switch arg {
		case "--headless":
			headless = true
		case "help", "-h", "--help":
			return printStdout(oaiHelpText, "oai help")
		default:
			return fmt.Errorf("unknown oai login argument %q", arg)
		}
	}

	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve current directory: %w", err)
	}

	login := oai.LoginBrowserIn
	if headless {
		login = oai.LoginDeviceIn
	}

	workDir := config.DefaultWorkDir
	if selected, err := selectRuntimeConfigFile(); err != nil {
		return fmt.Errorf("stat config path: %w", err)
	} else if selected.Found {
		workDir = selected.WorkDir
	}

	path, err := login(context.Background(), workspace, workDir, os.Stdout)
	if err != nil {
		return fmt.Errorf("login with ChatGPT OAuth: %w", err)
	}

	if path == "" {
		return errors.New("OAuth login did not return a token path")
	}

	if _, err = fmt.Fprintf(os.Stdout, "Saved OpenAI ChatGPT OAuth token to %s\n", path); err != nil {
		return fmt.Errorf("write oai login result: %w", err)
	}

	return nil
}
