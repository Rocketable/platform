package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func (processAssembler) ValidateAssets(cfg *config.Config, runtimeDir string) error {
	if err := cronfrontend.ValidateRuntimeDefinitions(config.NewLockedConfig(cfg), runtimeDir); err != nil {
		return fmt.Errorf("validate cron definitions: %w", err)
	}
	return nil
}

type cronRunner struct {
	backend frontend.Backend
}

func (r *cronRunner) Run(ctx context.Context, agent, prompt string, progress *backend.RawRunProgress) (protocol.CronRunResult, error) {
	destination := progress.SyncDestination
	if destination != "" {
		if err := r.backend.CreateConversation(ctx, protocol.Conversation{ID: destination, Agent: agent, CreatedBy: "cron"}); err != nil {
			return protocol.CronRunResult{}, err
		}
	}
	if err := r.backend.CreateConversation(ctx, protocol.Conversation{ID: progress.ConversationID, Agent: agent, CreatedBy: "cron", Run: progress.Run}); err != nil {
		return protocol.CronRunResult{}, err
	}
	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, prompt, false)
	inbound.ConversationID, inbound.SyncDestination = progress.ConversationID, destination
	inbound.RequireOutputDecision = true
	inbound.Cronjob = progress.Cronjob
	reply := inbound.EnableResponseWait()
	err := r.backend.RunTurn(ctx, inbound)
	result := protocol.CronRunResult{ConversationID: destination}
	select {
	case response := <-reply:
		result.Reported = strings.TrimSpace(response.Text) != "" || len(response.Attachments) > 0
	default:
	}
	return result, err
}
