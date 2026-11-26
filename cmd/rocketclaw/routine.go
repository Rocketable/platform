package main

import (
	"context"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type routineRunner struct {
	backend frontend.Backend
}

func (r *routineRunner) Run(ctx context.Context, agent, prompt string, progress *backend.RoutineRunProgress) (protocol.RoutineRunResult, error) {
	destination := progress.SyncDestination
	if destination != "" {
		if err := r.backend.CreateConversation(ctx, protocol.Conversation{ID: destination, Agent: agent, CreatedBy: string(backend.ThreadCreatedByRoutine)}); err != nil {
			return protocol.RoutineRunResult{}, err
		}
	}
	if err := r.backend.CreateConversation(ctx, protocol.Conversation{ID: progress.ConversationID, Agent: agent, CreatedBy: string(backend.ThreadCreatedByRoutine), Run: progress.Run}); err != nil {
		return protocol.RoutineRunResult{}, err
	}
	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, prompt, false)
	inbound.ConversationID, inbound.SyncDestination = progress.ConversationID, destination
	inbound.RequireOutputDecision = true
	inbound.Routine = progress.Routine
	reply := inbound.EnableResponseWait()
	err := r.backend.RunTurn(ctx, inbound)
	result := protocol.RoutineRunResult{ConversationID: destination}
	select {
	case response := <-reply:
		result.Reported = strings.TrimSpace(response.Text) != "" || len(response.Attachments) > 0
	default:
	}
	return result, err
}
