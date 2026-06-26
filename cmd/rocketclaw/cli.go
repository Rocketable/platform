package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Rocketable/platform/internal/rocketclaw/app"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
)

const cliHelpText = `rocketclaw cli

Usage:
  rocketclaw cli
  rocketclaw cli --new [agent]
  rocketclaw cli --attach <conversation-id>

Options:
  --new                    Start a fresh private persisted terminal conversation. The optional positional argument is the agent name and defaults to main.
  --attach <conversation>  Attach to an existing server-owned conversation. Defaults to main when omitted.
`

func runCLI(args []string) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		return printStdout(cliHelpText, "CLI help")
	}
	flagSet := flag.NewFlagSet("rocketclaw cli", flag.ContinueOnError)
	newConversation := flagSet.Bool("new", false, "start a fresh private terminal conversation")
	attachConversationID := flagSet.String("attach", "", "attach to an existing conversation")
	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse CLI flags: %w", err)
	}
	remaining := flagSet.Args()
	if *newConversation && *attachConversationID != "" {
		return errors.New("cli accepts only one of --new and --attach")
	}
	if !*newConversation && len(remaining) > 0 {
		return errors.New("cli accepts an agent argument only with --new")
	}
	if len(remaining) > 1 {
		return errors.New("cli --new accepts at most one agent")
	}
	agent := "main"
	if len(remaining) == 1 {
		agent = remaining[0]
	}
	selected, cfg, err := loadRuntimeConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := newLogger(cfg.Logging.Level)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	options := app.CLIOptions{In: os.Stdin, Out: os.Stdout, Agent: agent, ConversationID: *attachConversationID, NewConversation: *newConversation}
	if err := app.RunControlClient(ctx, cfg, options); err == nil {
		return nil
	} else if !errors.Is(err, app.ErrControlUnavailable) {
		return fmt.Errorf("run rocketclaw CLI control client: %w", err)
	}
	lock, err := harnessbridge.AcquireStateStoreLock(cfg.Workspace, cfg.WorkDirName())
	if err != nil {
		if errors.Is(err, harnessbridge.ErrStateStoreLocked) {
			return fmt.Errorf("rocketclaw control socket is unavailable and another process owns the state store; start or restart the server before using CLI attach: %w", err)
		}
		return fmt.Errorf("check rocketclaw state-store lock for CLI fallback: %w", err)
	}
	if err := lock.Close(); err != nil {
		return fmt.Errorf("release rocketclaw state-store lock probe: %w", err)
	}
	configPath, err := filepath.Abs(selected.Path)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	err = app.RunCLI(ctx, cfg, configPath, logger, options)
	if err != nil {
		if errors.Is(err, app.ErrRestartRequested) {
			logger.Info("rocketclaw restart requested; exiting with code 255 for supervisor restart")
			return exitCodeError(255)
		}
		logger.Error("rocketclaw CLI exited with error", "error", err)
		return fmt.Errorf("run rocketclaw CLI: %w", err)
	}
	return nil
}
