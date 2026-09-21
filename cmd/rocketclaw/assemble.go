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
	var stops []func(context.Context) error

	rt.Log.Info("starting Slack connector")

	channels := rt.Cfg.Slack.MappedChannels()
	runner := &cronRunner{backend: rt, config: rt.Cfg}
	cronjobs := cronfrontend.New(rt.Cfg.Workspace, rt.Cfg.RuntimeDirName(), channels, rt.Sessions, runner, rt.Log)
	slack := slackconnector.New(&rt.Cfg.Slack, rt, rt.TextRouter, cronjobs, rt.Sessions, rt.Log)
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
		agents := &mcpAgentIndex{cfg: rt.Cfg}
		*rt.RefreshExternalMCPAgents = agents.Refresh
		if err := agents.Refresh(); err != nil {
			return nil, nil, nil, err
		}

		externalMCP, err := startExternalMCPServer(rt.RunCtx, rt.Cfg, slack, rt.ExternalMCPUsers, agents, rt.Sessions, rt, rt.Log)
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
