package main

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	slackconnector "github.com/Rocketable/platform/internal/rocketclaw/frontend/slack"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type processAssembler struct{}

func (processAssembler) Assemble(rt *backend.Runtime) (backend.SlackFrontend, <-chan struct{}, []func(context.Context) error, error) {
	copyLoop := newClockwork(rt.Channels)
	var stops []func(context.Context) error

	go copyLoop.run(rt.RunCtx)

	rt.Log.Info("starting Slack connector")

	slack := slackconnector.New(&rt.Cfg.Slack, protocol.BroadcastPublisher(rt.Channels.Broadcasts), rt.TextRouter, rt.Cron, &backend.SideAskRunner{Config: rt.Cfg, Sessions: rt.Sessions, Logger: rt.Log}, rt.Log)
	removeSlack, err := copyLoop.registerBridge(protocol.BridgeSlack, slack)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("register Slack bridge: %w", err)
	}

	stops = append(stops, func(context.Context) error {
		removeSlack()
		return nil
	})

	if err := slack.Start(rt.RunCtx); err != nil {
		return nil, nil, nil, fmt.Errorf("start Slack connector: %w", err)
	}

	stops = append(stops, slack.Stop)

	if rt.Cfg.MCPExternal.Enabled {
		removeExternal, err := copyLoop.registerBridge(protocol.BridgeExternalMCP, dropBroadcastBridge{})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("register External MCP bridge: %w", err)
		}

		stops = append(stops, func(context.Context) error {
			removeExternal()
			return nil
		})

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
			result, err := waitBroadcastReply(relayCtx, rt.Channels.Broadcasts, &protocol.Broadcast{Sender: protocol.BridgeExternalMCP, Relay: relay, RelayReply: reply, RelayChannel: channelName})
			return result.Message, err
		}
		cleanupTextRelay := func(cleanupCtx context.Context, reply *protocol.InboundMessage) {
			if reply == nil {
				return
			}

			_, _ = waitBroadcastReply(cleanupCtx, rt.Channels.Broadcasts, &protocol.Broadcast{Sender: protocol.BridgeExternalMCP, RelayCleanup: reply})
		}

		externalMCP, err := startExternalMCPServer(rt.RunCtx, rt.Cfg, textRelay, cleanupTextRelay, rt.ExternalMCPUsers, func(agent string) bool {
			externalMCPAgentsMu.Lock()
			defer externalMCPAgentsMu.Unlock()

			return slices.Contains(externalMCPAgents, agent)
		}, rt.Sessions, func(submitCtx context.Context, agent, conversationID string, inbound *protocol.InboundMessage, activation protocol.ActivationHook) error {
			if err := activation(submitCtx, inbound); err != nil {
				return err
			}

			return rt.SubmitExternalMCP(submitCtx, agent, conversationID, inbound, backend.NoopActivationHook)
		}, rt.Log)
		if err != nil {
			return nil, nil, nil, err
		}

		stops = append(stops, externalMCP.Close)
	}

	developmentMCP, err := startDevelopmentMCP(rt.RunCtx, rt.Cfg, rt.ConfigPath, rt.OverlayMu, func(reason string) (string, error) {
		return rt.Reload(rt.RunCtx, reason)
	}, func(reason string) (string, error) {
		return rt.Restart(rt.RunCtx, reason)
	}, rt.Log, rt.Sessions)
	if err != nil {
		return nil, nil, nil, err
	}

	if developmentMCP != nil {
		stops = append(stops, developmentMCP.Close)
	}

	return slack, copyLoop.done, stops, nil
}

func waitBroadcastReply(ctx context.Context, ch chan<- protocol.Broadcast, broadcast *protocol.Broadcast) (protocol.BroadcastReply, error) {
	response := make(chan protocol.BroadcastReply, 1)
	sent := *broadcast
	sent.RelayResponse = response

	select {
	case ch <- sent:
	case <-ctx.Done():
		return protocol.BroadcastReply{}, ctx.Err()
	}

	select {
	case result := <-response:
		return result, result.Err
	case <-ctx.Done():
		return protocol.BroadcastReply{}, ctx.Err()
	}
}
