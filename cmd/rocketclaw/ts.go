package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"tailscale.com/ipn"
)

const tsHelpText = `rocketclaw ts

Usage:
  rocketclaw ts login

Commands:
  login  Log the Web's Tailscale node (web.tsnet) in to the tailnet, then exit.
         It uses web.tsnet.auth_key when set; otherwise it prints a login link.
         Stop RocketClaw first, and start it again afterwards.
`

var errTailnetNotConfigured = errors.New("web.tsnet is not set in the configuration")

func runTS(args []string) error {
	if len(args) == 0 {
		return printStdout(tsHelpText, "ts help")
	}

	switch args[0] {
	case "login":
		return runTSLogin(args[1:])
	case "help", "-h", "--help":
		return printStdout(tsHelpText, "ts help")
	default:
		return fmt.Errorf("unknown ts command %q", args[0])
	}
}

func runTSLogin(args []string) error {
	flagSet := flag.NewFlagSet("ts login", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	secretsARN := flagSet.String(secretsARNFlag, "", secretsARNUsage)

	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse ts login flags: %w", err)
	}

	if len(flagSet.Args()) != 0 {
		return errors.New("ts login takes no arguments")
	}

	_, cfg, err := loadRuntimeConfig(*secretsARN)
	if err != nil {
		return fmt.Errorf("load runtime config: %w", err)
	}

	if cfg.Web.Tsnet == nil {
		return errTailnetNotConfigured
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	node, err := newTailnetNode(cfg.Web.Tsnet)
	if err != nil {
		return err
	}

	defer func() { _ = node.server.Close() }()

	watcher, err := node.client.WatchIPNBus(ctx, ipn.NotifyInitialState)
	if err != nil {
		return fmt.Errorf("watch Tailscale node: %w", err)
	}

	defer func() { _ = watcher.Close() }()

	for {
		step, url, err := tailnetWait(watcher)
		if err != nil {
			return err
		}

		if step == tailnetRunning {
			break
		}

		message := "Approve " + cfg.Web.Tsnet.Hostname + " in the Tailscale admin console (Machines).\n"
		if step == tailnetLogin {
			message = "To log " + cfg.Web.Tsnet.Hostname + " in to your tailnet, open:\n\n  " + url + "\n\n"
		}

		if err := printStdout(message, "ts login prompt"); err != nil {
			return err
		}
	}

	status, err := node.server.Up(ctx)
	if err != nil {
		return fmt.Errorf("bring up Tailscale node: %w", err)
	}

	return printStdout(fmt.Sprintf("Logged in as %s. Start RocketClaw to serve the Web there.\n", strings.TrimSuffix(status.Self.DNSName, ".")), "ts login result")
}
