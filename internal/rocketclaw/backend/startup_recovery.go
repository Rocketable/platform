package backend

import (
	"context"
	"errors"
	"fmt"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"log/slog"
	"strings"

	"github.com/Rocketable/platform/internal/rocketcode"
)

type startupRecoveryStore interface {
	RecoverableActiveTurns(context.Context) ([]ActiveTurnState, error)
	ClearActiveTurn(context.Context, string) error
	StopGoal(string) error
	Thread(string) (ThreadState, bool, error)
	ExternalMCPSessionByConversationID(string) (string, ExternalMCPSessionState, bool, error)
}

type startupRecoveryHandoff func(context.Context, *ActiveTurnState) error

type cannotResumeFunc func(conversationID string, steers []protocol.PendingSteer)

type cannotResumeItem struct {
	conversationID string
	steers         []protocol.PendingSteer
}

func recoverStartupActiveTurns(ctx context.Context, store startupRecoveryStore, handoff startupRecoveryHandoff, cannotResume cannotResumeFunc, log *slog.Logger) error {
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
			if _, _, external, err := store.ExternalMCPSessionByConversationID(conversationID); err != nil {
				return fmt.Errorf("validate startup active turn external conversation: %w", err)
			} else if !external {
				if errClear := store.ClearActiveTurn(ctx, turn.Checkpoint.TurnID); errClear != nil {
					return fmt.Errorf("delete unknown startup active turn: %w", errClear)
				}

				log.Warn("deleted unknown startup active turn", "conversation_id", conversationID, "turn_id", turn.Checkpoint.TurnID)

				continue
			}
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
			if errClear := cannotResumeActiveTurn(ctx, store, turn, cannotResume); errClear != nil {
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

			if errClear := cannotResumeActiveTurn(ctx, store, turn, cannotResume); errClear != nil {
				return fmt.Errorf("delete failed startup active turn handoff: %w", errClear)
			}

			log.Warn("deleted failed startup active turn after handoff error", "conversation_id", conversationID, "turn_id", turn.Checkpoint.TurnID, "error", err)

			continue
		}
	}

	return nil
}

type startupSteerSurface interface {
	RestorePendingSteers(string, []protocol.PendingSteer)
	DiscardPendingSteers(context.Context, []protocol.PendingSteer)
}

func applyStartupSteerRecovery(ctx context.Context, slack startupSteerSurface, pick func(context.Context, string) error, recovered []ActiveTurnState, cannotResume []cannotResumeItem) error {
	for i := range recovered {
		slack.RestorePendingSteers(recovered[i].Checkpoint.ConversationKey, recovered[i].PendingSteers)
	}

	for i := range cannotResume {
		slack.DiscardPendingSteers(ctx, cannotResume[i].steers)

		if err := pick(ctx, cannotResume[i].conversationID); err != nil {
			return fmt.Errorf("pick later work after unresumable turn: %w", err)
		}
	}

	return nil
}

func cannotResumeActiveTurn(ctx context.Context, store startupRecoveryStore, turn *ActiveTurnState, cannotResume cannotResumeFunc) error {
	if err := store.ClearActiveTurn(ctx, turn.Checkpoint.TurnID); err != nil {
		return fmt.Errorf("clear unresumable active turn: %w", err)
	}

	if err := store.StopGoal(turn.Checkpoint.ConversationKey); err != nil {
		return fmt.Errorf("stop goal after unresumable turn: %w", err)
	}

	cannotResume(turn.Checkpoint.ConversationKey, turn.PendingSteers)

	return nil
}

func isStartupRecoveryShutdownError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || IsBridgeStopped(err)
}
