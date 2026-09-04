package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
	slack   backend.SlackFrontend
}

func (r *cronRunner) Run(ctx context.Context, agent, prompt string, _ *slog.Logger, progress *backend.RawRunProgress) (protocol.CronRunResult, error) {
	selected := ""
	for _, channel := range r.config.Slack.Channels {
		if channel.Channel == progress.TextChannel && len(channel.Agents) > 0 {
			selected = channel.Agents[0]
			break
		}
	}
	if selected == "" {
		return protocol.CronRunResult{}, fmt.Errorf("cron destination %q has no configured agents", progress.TextChannel)
	}
	destination := progress.SyncDestination
	if destination == "" {
		root, err := r.slack.StartNewThreadRoot(ctx, &protocol.StartNewThreadRequest{Title: "Cronjob", Prompt: "running…", SlackReply: &protocol.SlackReplyTarget{ChannelID: progress.TextChannel}})
		if err != nil {
			return protocol.CronRunResult{}, err
		}
		destination = protocol.SlackThreadConversationID(root.Target.ChannelID, root.Target.ThreadID)
	}
	if err := r.backend.CreateConversation(ctx, protocol.Conversation{ID: destination, Agent: selected, CreatedBy: "cron"}); err != nil {
		return protocol.CronRunResult{}, err
	}
	if err := r.backend.CreateConversation(ctx, protocol.Conversation{ID: progress.ConversationID, Agent: agent, CreatedBy: "cron"}); err != nil {
		return protocol.CronRunResult{}, err
	}
	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "", prompt, false)
	inbound.ConversationID, inbound.SyncDestination = progress.ConversationID, destination
	inbound.RequireOutputDecision = true
	inbound.Cronjob = progress.Cronjob
	errRun := r.backend.RunTurn(ctx, inbound)
	errSync := r.backend.SyncConversation(context.WithoutCancel(ctx), inbound.ConversationID, destination)
	return protocol.CronRunResult{ConversationID: destination}, errors.Join(errRun, errSync)
}
