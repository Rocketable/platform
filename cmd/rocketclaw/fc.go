package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
)

const fcHelpText = `rocketclaw fc

Usage:
  rocketclaw fc list [--since 24h|RFC3339] [--until RFC3339] [--limit N] [--no-message-preview]
  rocketclaw fc observe [--follow|-f] [conversation-id]
  rocketclaw fc delete [--no-vacuum] <conversation-id>
  rocketclaw fc vacuum

Commands:
  list     List stored rocketcode sessions.
  observe  Print stored rocketcode session entries as JSONL. Defaults to main.
  delete   Delete one rocketcode session and vacuum by default.
  vacuum   Vacuum the rocketcode session DB. May block if rocketclaw is running.
`

func runFC(args []string) error {
	if len(args) == 0 {
		return printStdout(fcHelpText, "rocketcode help")
	}

	_, cfg, err := loadRuntimeConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	switch args[0] {
	case "list":
		return runFCListIn(cfg.Workspace, cfg.WorkDirName(), args[1:], os.Stdout)
	case "observe":
		return runFCObserveIn(cfg.Workspace, cfg.WorkDirName(), args[1:], os.Stdout)
	case "delete":
		return runFCDeleteIn(cfg.Workspace, cfg.WorkDirName(), args[1:], os.Stdout)
	case "vacuum":
		return runFCVacuumIn(cfg.Workspace, cfg.WorkDirName(), args[1:], os.Stdout)
	case "help", "-h", "--help":
		return printStdout(fcHelpText, "rocketcode help")
	default:
		return fmt.Errorf("unknown rocketcode command %q", args[0])
	}
}

func runFCDeleteIn(workspace, workDir string, args []string, out io.Writer) error {
	flagSet := flag.NewFlagSet("rocketclaw fc delete", flag.ContinueOnError)
	noVacuum := flagSet.Bool("no-vacuum", false, "skip vacuum after delete")

	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse rocketcode delete flags: %w", err)
	}

	remaining := flagSet.Args()
	if len(remaining) != 1 || strings.TrimSpace(remaining[0]) == "" {
		return errors.New("delete requires exactly one conversation-id")
	}

	conversationID := strings.TrimSpace(remaining[0])

	lock, err := acquireFCMutationLock(workspace, workDir, "delete")
	if err != nil {
		return fmt.Errorf("delete rocketcode session: %w", err)
	}

	defer func() { _ = lock.Close() }()

	deleted, err := harnessbridge.DeleteSessionIn(context.Background(), workspace, workDir, conversationID)
	if err != nil {
		return fmt.Errorf("delete rocketcode session: %w", err)
	}

	if *noVacuum {
		_, err := fmt.Fprintf(out, "deleted %d turns; skipped vacuum\n", deleted)
		if err != nil {
			return fmt.Errorf("write rocketcode delete result: %w", err)
		}

		if deleted > 0 {
			_, err = fmt.Fprintln(out, "run rocketclaw fc vacuum to reclaim disk space")
			if err != nil {
				return fmt.Errorf("write rocketcode delete hint: %w", err)
			}
		}

		return nil
	}

	stats, vacuumErr := harnessbridge.VacuumSessionsIn(context.Background(), workspace, workDir)

	if _, err := fmt.Fprintf(out, "deleted %d turns\n", deleted); err != nil {
		return fmt.Errorf("write rocketcode delete result: %w", err)
	}

	if vacuumErr != nil {
		return fmt.Errorf("deleted %d turns; vacuum failed: %w", deleted, vacuumErr)
	}

	return writeVacuumStats(out, stats)
}

func runFCVacuumIn(workspace, workDir string, args []string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("vacuum does not accept arguments")
	}

	lock, err := acquireFCMutationLock(workspace, workDir, "vacuum")
	if err != nil {
		return fmt.Errorf("vacuum rocketcode sessions: %w", err)
	}

	defer func() { _ = lock.Close() }()

	stats, err := harnessbridge.VacuumSessionsIn(context.Background(), workspace, workDir)
	if err != nil {
		return fmt.Errorf("vacuum rocketcode sessions: %w", err)
	}

	return writeVacuumStats(out, stats)
}

func acquireFCMutationLock(workspace, workDir, command string) (*harnessbridge.StateStoreLock, error) {
	lock, err := harnessbridge.AcquireStateStoreLock(workspace, workDir)
	if errors.Is(err, harnessbridge.ErrStateStoreLocked) {
		return nil, fmt.Errorf("rocketclaw daemon is running; stop it before running fc %s: %w", command, err)
	}

	if err != nil {
		return nil, fmt.Errorf("lock rocketcode session db for fc %s: %w", command, err)
	}

	return lock, nil
}

func writeVacuumStats(out io.Writer, stats harnessbridge.VacuumStats) error {
	if !stats.DBExists {
		if _, err := fmt.Fprintln(out, "nothing to vacuum"); err != nil {
			return fmt.Errorf("write rocketcode vacuum result: %w", err)
		}

		return nil
	}

	if _, err := fmt.Fprintf(out, "vacuumed sessions: pages %d -> %d, free pages %d -> %d\n", stats.BeforePageCount, stats.AfterPageCount, stats.BeforeFreePages, stats.AfterFreePages); err != nil {
		return fmt.Errorf("write rocketcode vacuum result: %w", err)
	}

	return nil
}

func runFCListIn(workspace, workDir string, args []string, out io.Writer) error {
	flagSet := flag.NewFlagSet("rocketclaw fc list", flag.ContinueOnError)
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

	var options harnessbridge.SessionListOptions
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

	return writeFCListInOptions(context.Background(), workspace, workDir, options, !*noMessagePreview, out)
}

func writeFCListInOptions(ctx context.Context, workspace, workDir string, options harnessbridge.SessionListOptions, includeMessagePreview bool, out io.Writer) error {
	summaries, err := harnessbridge.ListSessionsInOptions(ctx, workspace, workDir, options)
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

func runFCObserveIn(workspace, workDir string, args []string, out io.Writer) error {
	flagSet := flag.NewFlagSet("rocketclaw fc observe", flag.ContinueOnError)
	follow := flagSet.Bool("follow", false, "follow session entries")
	flagSet.BoolVar(follow, "f", false, "follow session entries")

	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse rocketcode observe flags: %w", err)
	}

	remaining := flagSet.Args()
	if len(remaining) > 1 {
		return errors.New("observe accepts at most one conversation-id")
	}

	conversationID := events.MainConversationID()
	if len(remaining) == 1 {
		conversationID = strings.TrimSpace(remaining[0])
	}

	return writeFCObserveIn(context.Background(), workspace, workDir, conversationID, *follow, time.Second, out)
}

func writeFCObserveIn(ctx context.Context, workspace, workDir, conversationID string, follow bool, pollInterval time.Duration, out io.Writer) error {
	if strings.TrimSpace(conversationID) == "" {
		conversationID = events.MainConversationID()
	}

	var lastID int64
	for {
		entries, err := harnessbridge.ObserveSessionEntries(ctx, harnessbridge.SessionDBPathIn(workspace, workDir), conversationID, lastID)
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
