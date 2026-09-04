package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

type cronRootSender interface {
	SendCronjobRoot(context.Context, *protocol.OutboundMessage) (protocol.TextConversationTarget, error)
}

type cronRunner struct {
	backend frontend.Backend
	config  *config.Config
	slack   cronRootSender
}

func (r *cronRunner) Run(ctx context.Context, agent, prompt string, progress *backend.RawRunProgress) (protocol.CronRunResult, error) {
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
	if destination != "" {
		if err := r.backend.CreateConversation(ctx, protocol.Conversation{ID: destination, Agent: selected, CreatedBy: "cron"}); err != nil {
			return protocol.CronRunResult{}, err
		}
	}
	if err := r.backend.CreateConversation(ctx, protocol.Conversation{ID: progress.ConversationID, Agent: agent, CreatedBy: "cron"}); err != nil {
		return protocol.CronRunResult{}, err
	}
	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "", prompt, false)
	inbound.ConversationID, inbound.SyncDestination = progress.ConversationID, destination
	inbound.RequireOutputDecision = true
	inbound.Cronjob = progress.Cronjob
	response := inbound.EnableResponseWait()
	errRun := r.backend.RunTurn(ctx, inbound)
	if destination == "" {
		if errRun != nil {
			return protocol.CronRunResult{}, errRun
		}
		result := <-response
		if strings.TrimSpace(result.Text) == "" && len(result.Attachments) == 0 {
			return protocol.CronRunResult{}, nil
		}
		message := protocol.NewOutboundMessage("", result.Text)
		message.Complete, message.Cronjob, message.Attachments = true, progress.Cronjob, result.Attachments
		message.SlackReply = &protocol.SlackReplyTarget{ChannelID: progress.TextChannel}
		root, err := r.slack.SendCronjobRoot(ctx, message)
		if err != nil {
			return protocol.CronRunResult{}, err
		}
		destination = protocol.SlackThreadConversationID(root.ChannelID, root.ThreadID)
		if err := r.backend.CreateConversation(ctx, protocol.Conversation{ID: destination, Agent: selected, CreatedBy: "cron"}); err != nil {
			return protocol.CronRunResult{}, err
		}
	}
	errSync := r.backend.SyncConversation(context.WithoutCancel(ctx), inbound.ConversationID, destination)
	return protocol.CronRunResult{ConversationID: destination}, errors.Join(errRun, errSync)
}
