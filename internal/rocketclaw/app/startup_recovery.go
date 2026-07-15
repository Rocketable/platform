package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/Rocketable/platform/internal/rocketcode"
)

type startupRecoveryStore interface {
	RecoverableActiveTurns(context.Context) ([]harnessbridge.ActiveTurnState, error)
	ClearActiveTurn(context.Context, string) error
	Thread(string) (harnessbridge.ThreadState, bool, error)
}

type startupRecoveryHandoff func(context.Context, *harnessbridge.ActiveTurnState) error

func recoverStartupActiveTurns(ctx context.Context, store startupRecoveryStore, handoff startupRecoveryHandoff, log *slog.Logger) error {
	turns, err := store.RecoverableActiveTurns(ctx)
	if err != nil {
		return fmt.Errorf("load startup active-turn recovery rows: %w", err)
	}

	seen := map[string]bool{}

	for i := range turns {
		turn := &turns[i]

		conversationID := strings.TrimSpace(turn.Checkpoint.ConversationKey)
		if conversationID == "" {
			if errClear := store.ClearActiveTurn(ctx, turn.Checkpoint.TurnID); errClear != nil {
				return fmt.Errorf("delete invalid startup active turn: %w", errClear)
			}

			log.Warn("deleted invalid startup active turn", "turn_id", turn.Checkpoint.TurnID)

			continue
		}

		if strings.HasPrefix(conversationID, "cron:") || strings.HasPrefix(conversationID, "one-off-cron:") {
			if errClear := store.ClearActiveTurn(ctx, turn.Checkpoint.TurnID); errClear != nil {
				return fmt.Errorf("delete raw cron startup active turn: %w", errClear)
			}

			log.Warn("deleted raw cron startup active turn", "conversation_id", conversationID, "turn_id", turn.Checkpoint.TurnID)

			continue
		}

		if _, ok, err := store.Thread(conversationID); err != nil {
			return fmt.Errorf("validate startup active turn conversation: %w", err)
		} else if !ok {
			if errClear := store.ClearActiveTurn(ctx, turn.Checkpoint.TurnID); errClear != nil {
				return fmt.Errorf("delete unknown startup active turn: %w", errClear)
			}

			log.Warn("deleted unknown startup active turn", "conversation_id", conversationID, "turn_id", turn.Checkpoint.TurnID)

			continue
		}

		if seen[conversationID] {
			if errClear := store.ClearActiveTurn(ctx, turn.Checkpoint.TurnID); errClear != nil {
				return fmt.Errorf("delete skipped competing startup active turn: %w", errClear)
			}

			log.Info("deleted duplicate startup active-turn recovery row", "conversation_id", conversationID, "turn_id", turn.Checkpoint.TurnID)

			continue
		}

		recoveredReplay, err := rocketcode.RecoveredReplayInput(&turn.Checkpoint)
		if err != nil {
			if errClear := store.ClearActiveTurn(ctx, turn.Checkpoint.TurnID); errClear != nil {
				return fmt.Errorf("delete unrecoverable startup active turn: %w", errClear)
			}

			log.Warn("deleted unrecoverable startup active turn", "conversation_id", conversationID, "turn_id", turn.Checkpoint.TurnID, "error", err)

			continue
		}

		seen[conversationID] = true

		turn.Checkpoint.ReplayInput = recoveredReplay
		if err := handoff(ctx, turn); err != nil {
			if isStartupRecoveryShutdownError(err) {
				return fmt.Errorf("handoff startup active turn recovery: %w", err)
			}

			if errClear := store.ClearActiveTurn(ctx, turn.Checkpoint.TurnID); errClear != nil {
				return fmt.Errorf("delete failed startup active turn handoff: %w", errClear)
			}

			log.Warn("deleted failed startup active turn after handoff error", "conversation_id", conversationID, "turn_id", turn.Checkpoint.TurnID, "error", err)

			continue
		}
	}

	return nil
}

func isStartupRecoveryShutdownError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || harnessbridge.IsBridgeStopped(err)
}
