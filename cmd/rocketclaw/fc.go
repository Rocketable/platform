package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
)

const fcHelpText = `rocketclaw fc

Usage:
  rocketclaw fc list [--since 24h|RFC3339] [--until RFC3339] [--limit N] [--no-message-preview]
  rocketclaw fc observe [--follow|-f] <conversation-id>
  rocketclaw fc delete <conversation-id>

Commands:
  list     List stored rocketcode sessions.
  observe  Print one conversation's stored rocketcode session entries as JSONL.
  delete   Delete one rocketcode session.
`

func runFC(args []string) error {
	if len(args) == 0 {
		return printStdout(fcHelpText, "rocketcode help")
	}

	var secretsARN string
	switch args[0] {
	case "list", "observe", "delete":
		var err error
		secretsARN, err = parseFCSecretsARN(args[1:])
		if err != nil {
			return err
		}
	case "help", "-h", "--help":
		if _, _, err := loadRuntimeConfig(""); err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		return printStdout(fcHelpText, "rocketcode help")
	default:
		return fmt.Errorf("unknown rocketcode command %q", args[0])
	}

	_, cfg, err := loadRuntimeConfig(secretsARN)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	switch args[0] {
	case "list":
		return runFCListIn(cfg.DatabaseURL, args[1:], os.Stdout)
	case "observe":
		return runFCObserveIn(cfg.DatabaseURL, args[1:], os.Stdout)
	default:
		return runFCDeleteIn(cfg.DatabaseURL, args[1:], os.Stdout)
	}
}

func parseFCSecretsARN(args []string) (string, error) {
	flagSet := flag.NewFlagSet("rocketclaw fc", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	secretsARN := flagSet.String(secretsARNFlag, "", secretsARNUsage)
	flagSet.String("since", "", "")
	flagSet.String("until", "", "")
	flagSet.Int("limit", 0, "")
	flagSet.Bool("no-message-preview", false, "")
	flagSet.Bool("follow", false, "")
	flagSet.Bool("f", false, "")
	if err := flagSet.Parse(args); err != nil {
		return "", fmt.Errorf("parse rocketclaw fc flags: %w", err)
	}
	return *secretsARN, nil
}

func runFCDeleteIn(databaseURL string, args []string, out io.Writer) error {
	flagSet := flag.NewFlagSet("rocketclaw fc delete", flag.ContinueOnError)
	flagSet.String(secretsARNFlag, "", secretsARNUsage)

	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse rocketcode delete flags: %w", err)
	}

	remaining := flagSet.Args()
	if len(remaining) != 1 || strings.TrimSpace(remaining[0]) == "" {
		return errors.New("delete requires exactly one conversation-id")
	}

	conversationID := strings.TrimSpace(remaining[0])

	deleted, err := backend.DeleteSessionIn(context.Background(), databaseURL, conversationID)
	if err != nil {
		return fmt.Errorf("delete rocketcode session: %w", err)
	}

	if _, err := fmt.Fprintf(out, "deleted %d turns\n", deleted); err != nil {
		return fmt.Errorf("write rocketcode delete result: %w", err)
	}

	return nil
}

func runFCListIn(databaseURL string, args []string, out io.Writer) error {
	flagSet := flag.NewFlagSet("rocketclaw fc list", flag.ContinueOnError)
	flagSet.String(secretsARNFlag, "", secretsARNUsage)
	sinceText := flagSet.String("since", "", "show sessions updated since duration or RFC3339 time")
	untilText := flagSet.String("until", "", "show sessions updated before RFC3339 time")
	limit := flagSet.Int("limit", 0, "maximum sessions to list")
	noMessagePreview := flagSet.Bool("no-message-preview", false, "omit last-message preview columns")

	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse rocketcode list flags: %w", err)
	}

	if len(flagSet.Args()) != 0 {
		return errors.New("list does not accept arguments")
	}

	if *limit < 0 {
		return errors.New("list limit must be non-negative")
	}

	var options backend.SessionListOptions
	options.Limit = *limit

	if strings.TrimSpace(*sinceText) != "" {
		sinceValue := strings.TrimSpace(*sinceText)
		duration, err := time.ParseDuration(sinceValue)
		if err == nil {
			options.Since = time.Now().UTC().Add(-duration)
		} else {
			since, err := time.Parse(time.RFC3339Nano, sinceValue)
			if err != nil {
				return fmt.Errorf("parse rocketcode list since: %w", err)
			}

			options.Since = since
		}
	}

	if strings.TrimSpace(*untilText) != "" {
		until, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*untilText))
		if err != nil {
			return fmt.Errorf("parse rocketcode list until: %w", err)
		}

		options.Until = until
	}

	return writeFCListInOptions(context.Background(), databaseURL, options, !*noMessagePreview, out)
}

func writeFCListInOptions(ctx context.Context, databaseURL string, options backend.SessionListOptions, includeMessagePreview bool, out io.Writer) error {
	summaries, err := backend.ListSessionsInOptions(ctx, databaseURL, options)
	if err != nil {
		return fmt.Errorf("list rocketcode sessions: %w", err)
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if !includeMessagePreview {
		if _, err := fmt.Fprintln(tw, "CONVERSATION_ID\tTURNS\tLAST_UPDATED"); err != nil {
			return fmt.Errorf("write rocketcode session list: %w", err)
		}
	} else if _, err := fmt.Fprintln(tw, "CONVERSATION_ID\tTURNS\tLAST_UPDATED\tLAST_USER_MESSAGE\tLAST_ASSISTANT_MESSAGE"); err != nil {
		return fmt.Errorf("write rocketcode session list: %w", err)
	}

	for i := range summaries {
		summary := summaries[i]

		updated := ""
		if !summary.LastUpdated.IsZero() {
			updated = summary.LastUpdated.Format(time.RFC3339)
		}

		if !includeMessagePreview {
			if _, err := fmt.Fprintf(tw, "%s\t%d\t%s\n", summary.ConversationID, summary.Turns, updated); err != nil {
				return fmt.Errorf("write rocketcode session list: %w", err)
			}

			continue
		}

		lastUserMessage := strings.Join(strings.Fields(summary.LastUserMessage), " ")
		lastAssistantMessage := strings.Join(strings.Fields(summary.LastAssistantMessage), " ")
		if _, err := fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", summary.ConversationID, summary.Turns, updated, lastUserMessage, lastAssistantMessage); err != nil {
			return fmt.Errorf("write rocketcode session list: %w", err)
		}
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush rocketcode session list: %w", err)
	}

	return nil
}

func runFCObserveIn(databaseURL string, args []string, out io.Writer) error {
	flagSet := flag.NewFlagSet("rocketclaw fc observe", flag.ContinueOnError)
	flagSet.String(secretsARNFlag, "", secretsARNUsage)
	follow := flagSet.Bool("follow", false, "follow session entries")
	flagSet.BoolVar(follow, "f", false, "follow session entries")

	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse rocketcode observe flags: %w", err)
	}

	remaining := flagSet.Args()
	if len(remaining) != 1 || strings.TrimSpace(remaining[0]) == "" {
		return errors.New("observe requires exactly one conversation-id")
	}

	conversationID := strings.TrimSpace(remaining[0])

	return writeFCObserveIn(context.Background(), databaseURL, conversationID, *follow, time.Second, out)
}

func writeFCObserveIn(ctx context.Context, databaseURL, conversationID string, follow bool, pollInterval time.Duration, out io.Writer) error {
	if strings.TrimSpace(conversationID) == "" {
		return errors.New("conversation ID is required")
	}

	service, err := backend.NewSessionServiceIn(databaseURL, slog.New(slog.DiscardHandler))
	if err != nil {
		return fmt.Errorf("observe rocketcode session entries: %w", err)
	}
	defer func() { _ = service.Stop(ctx) }()

	var lastID int64
	for {
		entries, err := service.ObserveEntries(ctx, conversationID, lastID)
		if err != nil {
			return fmt.Errorf("observe rocketcode session entries: %w", err)
		}

		for i := range entries {
			data, err := json.Marshal(entries[i].Entry)
			if err != nil {
				return fmt.Errorf("marshal rocketcode session entry: %w", err)
			}

			if _, err := fmt.Fprintf(out, "%s\n", data); err != nil {
				return fmt.Errorf("write rocketcode session entry: %w", err)
			}

			lastID = entries[i].ID
		}

		if !follow {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("observe rocketcode session: %w", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}
