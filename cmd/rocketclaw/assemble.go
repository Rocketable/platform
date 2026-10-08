package main

import (
	"context"
	"fmt"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	slackconnector "github.com/Rocketable/platform/internal/rocketclaw/frontend/slack"
)

type processAssembler struct{}

func (processAssembler) Assemble(rt *backend.Runtime) (backend.SlackFrontend, <-chan struct{}, []func(context.Context) error, error) {
	cfg := rt.Cfg.Clone()
	var stops []func(context.Context) error

	rt.Log.Info("starting Slack connector")

	channels := cfg.Slack.MappedChannels()
	runner := &cronRunner{backend: rt, config: cfg}
	cronjobs := cronfrontend.New(cfg.Workspace, cfg.RuntimeDirName(), channels, rt.Sessions, runner, rt.Log)
	slack := slackconnector.New(&cfg.Slack, rt.TextRouter, rt.Sessions, rt.Log)

	if err := slack.Authenticate(); err != nil {
		return nil, nil, nil, fmt.Errorf("start Slack connector: %w", err)
	}

	stops = append(stops, slack.Stop)
	done := slack.StartEvents(rt.RunCtx, rt)
	if err := cronjobs.Start(rt.RunCtx); err != nil {
		return nil, nil, nil, err
	}
	stops = append(stops, cronjobs.Stop)

	if cfg.MCPExternal.Enabled {
		agents := &mcpAgentIndex{cfg: rt.Cfg}
		*rt.RefreshExternalMCPAgents = agents.Refresh
		if err := agents.Refresh(); err != nil {
			return nil, nil, nil, err
		}

		externalMCP, err := startExternalMCPServer(rt.RunCtx, cfg, slack, rt.ExternalMCPUsers, agents, rt.Sessions, rt, rt.Log)
		if err != nil {
			return nil, nil, nil, err
		}

		stops = append(stops, externalMCP.Close)
	}

	stopWeb, err := startWebRPC(rt, slack, cronjobs)
	if err != nil {
		return nil, nil, nil, err
	}
	stops = append(stops, stopWeb)

	return slack, done, stops, nil
}
