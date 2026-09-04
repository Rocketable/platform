package frontend

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

// SideAsk is Create S (not user-facing), Sync(source, S), RunTurn S.
type SideAsk struct {
	Backend Backend
}

// Run copies source into a private conversation and RunTurns that conversation.
func (s SideAsk) Run(ctx context.Context, req protocol.SideAskRequest) error {
	sID := "side-ask:" + rand.Text()
	if err := s.Backend.CreateConversation(sID, []string{req.Agent}, nil); err != nil {
		return fmt.Errorf("create side ask conversation: %w", err)
	}

	events := s.Backend.Subscribe(ctx)
	if err := s.Backend.SyncConversation(ctx, req.ConversationID, sID); err != nil {
		return fmt.Errorf("sync side ask conversation: %w", err)
	}

	errc := make(chan error, 1)
	go func() {
		errc <- s.Backend.RunTurn(ctx, &protocol.TurnRequest{ID: sID, Kind: protocol.TurnPrompt, Text: req.Question, Agent: req.Agent})
	}()

	for {
		select {
		case err := <-errc:
			if ctx.Err() != nil {
				_ = s.Backend.RunTurn(context.WithoutCancel(ctx), &protocol.TurnRequest{ID: sID, Kind: protocol.TurnCancel})

				if err != nil {
					return fmt.Errorf("run side ask: %w", err)
				}

				return fmt.Errorf("run side ask: %w", ctx.Err())
			}

			if err != nil {
				return fmt.Errorf("run side ask: %w", err)
			}

			return nil
		case ev, ok := <-events:
			if !ok {
				return fmt.Errorf("run side ask: %w", <-errc)
			}

			if ev.ConversationID != sID || ev.Text == "" {
				continue
			}

			var err error

			switch ev.Role {
			case "thinking":
				err = req.Thinking(ctx, ev.Text)
			case "assistant":
				err = req.Message(ctx, ev.Text)
			}

			if err != nil {
				_ = s.Backend.RunTurn(context.WithoutCancel(ctx), &protocol.TurnRequest{ID: sID, Kind: protocol.TurnCancel})

				<-errc

				return fmt.Errorf("side ask callback: %w", err)
			}
		case <-ctx.Done():
			_ = s.Backend.RunTurn(context.WithoutCancel(ctx), &protocol.TurnRequest{ID: sID, Kind: protocol.TurnCancel})

			err := <-errc
			if err != nil {
				return fmt.Errorf("run side ask: %w", err)
			}

			return fmt.Errorf("run side ask: %w", ctx.Err())
		}
	}
}
