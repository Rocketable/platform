package main

import (
	"context"
	"fmt"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func (processAssembler) ValidateAssets(cfg *config.Config, runtimeDir string) error {
	if err := cronfrontend.ValidateRuntimeDefinitions(cfg.Workspace, runtimeDir); err != nil {
		return fmt.Errorf("validate cron definitions: %w", err)
	}
	return nil
}

type cronRunner struct {
	backend frontend.Backend
}

// Run stores the cron request and waits for it while alive. Without a destination
// the backend's delivery reports a reply in a new Web chat.
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
	return protocol.CronRunResult{ConversationID: destination}, r.backend.RunTurn(ctx, inbound)
}
