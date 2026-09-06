package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	slackconnector "github.com/Rocketable/platform/internal/rocketclaw/frontend/slack"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type processAssembler struct{}

func (processAssembler) Assemble(rt *backend.Runtime) (backend.SlackFrontend, <-chan struct{}, []func(context.Context) error, error) {
	var stops []func(context.Context) error

	rt.Log.Info("starting Slack connector")

	channels := make([]string, 0, len(rt.Cfg.Slack.Channels))
	for _, channel := range rt.Cfg.Slack.Channels {
		if channel.Channel != "@" {
			channels = append(channels, channel.Channel)
		}
	}
	runner := &cronRunner{backend: rt, config: rt.Cfg}
	cronjobs := cronfrontend.New(rt.Cfg.Workspace, rt.Cfg.RuntimeDirName(), channels, rt.Sessions, runner, rt.Log)
	slack := slackconnector.New(&rt.Cfg.Slack, rt, rt.TextRouter, cronjobs, rt.Log)
	runner.slack = slack

	if err := slack.Start(rt.RunCtx); err != nil {
		return nil, nil, nil, fmt.Errorf("start Slack connector: %w", err)
	}

	stops = append(stops, slack.Stop)
	done := slack.StartEvents(rt.RunCtx, rt)
	if err := cronjobs.Start(rt.RunCtx); err != nil {
		return nil, nil, nil, err
	}
	stops = append(stops, cronjobs.Stop)

	if rt.Cfg.MCPExternal.Enabled {
		var (
			externalMCPAgentsMu sync.Mutex
			externalMCPAgents   = []string{}
		)

		*rt.RefreshExternalMCPAgents = func() error {
			agents, err := backend.ExternalMCPAgentsIn(rt.Cfg, rt.Cfg.RuntimeDirName())
			if err != nil {
				return fmt.Errorf("load external MCP agents: %w", err)
			}

			externalMCPAgentsMu.Lock()
			externalMCPAgents = agents
			externalMCPAgentsMu.Unlock()

			return nil
		}

		if err := (*rt.RefreshExternalMCPAgents)(); err != nil {
			return nil, nil, nil, err
		}

		textRelay := func(relayCtx context.Context, relay *protocol.ExternalMCPRelay, reply *protocol.InboundMessage, channelName string) (*protocol.InboundMessage, error) {
			channelID, threadTS := channelName, ""
			if reply != nil {
				channelID, threadTS = reply.SlackReply.ChannelID, reply.SlackReply.ThreadTS
			}
			target, err := slack.SendExternalMCPRelay(relayCtx, channelID, threadTS, relay)
			if err != nil {
				return nil, err
			}
			return &protocol.InboundMessage{SlackReply: target}, nil
		}
		cleanupTextRelay := func(cleanupCtx context.Context, reply *protocol.InboundMessage) {
			if reply == nil {
				return
			}

			slack.CleanupExternalMCPRelay(cleanupCtx, reply.SlackReply)
		}

		externalMCP, err := startExternalMCPServer(rt.RunCtx, rt.Cfg, textRelay, cleanupTextRelay, rt.ExternalMCPUsers, func(agent string) bool {
			externalMCPAgentsMu.Lock()
			defer externalMCPAgentsMu.Unlock()

			return slices.Contains(externalMCPAgents, agent)
		}, rt.Sessions, func(submitCtx context.Context, agent, conversationID string, inbound *protocol.InboundMessage) error {
			if err := rt.CreateConversation(submitCtx, protocol.Conversation{ID: conversationID, Agent: agent}); err != nil {
				return err
			}

			errRun := rt.RunTurn(submitCtx, inbound)
			errSync := rt.SyncConversation(context.WithoutCancel(submitCtx), conversationID, inbound.SyncDestination)
			return errors.Join(errRun, errSync)
		}, rt.Log)
		if err != nil {
			return nil, nil, nil, err
		}

		stops = append(stops, externalMCP.Close)
	}

	if os.Getenv("ROCKETCLAW_WEB_GRPC") != "" {
		stopWeb, err := startWebRPC(rt, slack, cronjobs)
		if err != nil {
			return nil, nil, nil, err
		}

		stops = append(stops, stopWeb)
	}

	return slack, done, stops, nil
}
