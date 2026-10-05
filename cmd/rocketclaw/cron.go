package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func (processAssembler) ValidateAssets(cfg *config.Config, runtimeDir string, channels []string) error {
	if err := cronfrontend.ValidateRuntimeDefinitions(cfg.Workspace, runtimeDir, channels); err != nil {
		return fmt.Errorf("validate cron definitions: %w", err)
	}
	return nil
}

type cronRunner struct {
	backend frontend.Backend
	config  *config.Config
}

// Run stores the cron request and waits for it while alive. Without a destination
// the backend's delivery posts a visible report as a new Slack thread.
func (r *cronRunner) Run(ctx context.Context, agent, prompt string, progress *backend.RawRunProgress) (protocol.CronRunResult, error) {
	channel, ok := r.config.Slack.Channel(progress.TextChannel)
	if !ok || len(channel.Agents) == 0 {
		return protocol.CronRunResult{}, fmt.Errorf("cron destination %q has no configured agents", progress.TextChannel)
	}
	destination := progress.SyncDestination
	if destination != "" {
		if err := r.backend.CreateConversation(ctx, protocol.Conversation{ID: destination, Agent: channel.Agents[0], CreatedBy: "cron"}); err != nil {
			return protocol.CronRunResult{}, err
		}
	}
	if err := r.backend.CreateConversation(ctx, protocol.Conversation{ID: progress.ConversationID, Agent: agent, CreatedBy: "cron"}); err != nil {
		return protocol.CronRunResult{}, err
	}
	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, prompt, false)
	inbound.ConversationID, inbound.SyncDestination = progress.ConversationID, destination
	inbound.RequireOutputDecision = true
	inbound.Cronjob = progress.Cronjob
	if destination == "" {
		inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: progress.TextChannel}
		return protocol.CronRunResult{}, r.backend.RunTurn(ctx, inbound)
	}
	errRun := r.backend.RunTurn(ctx, inbound)
	errSync := r.backend.SyncConversation(context.WithoutCancel(ctx), inbound.ConversationID, destination)
	return protocol.CronRunResult{ConversationID: destination}, errors.Join(errRun, errSync)
}
