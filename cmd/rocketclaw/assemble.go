package main

import (
	"context"
	"fmt"
	"net"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	slackconnector "github.com/Rocketable/platform/internal/rocketclaw/frontend/slack"
)

type processAssembler struct{}

type slackFrontend interface {
	ChannelAgentChoices(context.Context, string) ([]string, error)
	SidebarChannelAgentChoices(context.Context, string) (string, []string, error)
	SlackNames(context.Context, []string) map[string]string
	SlackTagsMatching(context.Context, string) []string
	RenameThreadSession(ctx context.Context, conversationID, name string)
	StartEvents(context.Context, frontend.Backend) <-chan struct{}
	Start(context.Context)
	Stop(context.Context) error
}

type inertSlack struct{}

func (inertSlack) ChannelAgentChoices(context.Context, string) ([]string, error) {
	return nil, nil
}

func (inertSlack) SidebarChannelAgentChoices(_ context.Context, channelID string) (name string, choices []string, err error) {
	return channelID, nil, nil
}

func (inertSlack) SlackNames(context.Context, []string) map[string]string {
	return nil
}

func (inertSlack) SlackTagsMatching(context.Context, string) []string {
	return nil
}

func (inertSlack) RenameThreadSession(context.Context, string, string) {}

func (inertSlack) StartEvents(ctx context.Context, _ frontend.Backend) <-chan struct{} {
	return ctx.Done()
}

func (inertSlack) Start(context.Context) {}

func (inertSlack) Stop(context.Context) error {
	return nil
}

func (processAssembler) Assemble(rt *backend.Runtime) (<-chan struct{}, []func(context.Context) error, error) {
	cfg := rt.Cfg.Clone()
	var stops []func(context.Context) error

	runner := &cronRunner{backend: rt}
	cronjobs := cronfrontend.New(rt.Cfg, cfg.RuntimeDirName(), rt.Sessions, runner, rt.Log)

	var slack slackFrontend = inertSlack{}

	if cfg.Slack.BotToken != "" {
		rt.Log.Info("starting Slack connector")

		connector := slackconnector.New(rt.Cfg, rt.TextRouter, rt.Sessions, rt.Log)
		if err := connector.Authenticate(); err != nil {
			return nil, nil, fmt.Errorf("start Slack connector: %w", err)
		}

		slack = connector
	}

	stops = append(stops, slack.Stop)
	if err := cronjobs.Start(rt.RunCtx); err != nil {
		return nil, nil, err
	}
	stops = append(stops, cronjobs.Stop)
	rt.CronJobs = cronjobs
	done := slack.StartEvents(rt.RunCtx, rt)

	if cfg.MCPExternal.Enabled {
		agents := &mcpAgentIndex{cfg: rt.Cfg}
		rt.ExternalMCPAgents = agents
		if err := agents.Refresh(); err != nil {
			return nil, nil, err
		}

		listener, err := net.Listen("tcp", cfg.MCPExternal.ListenAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("start external MCP HTTP server: listen for external MCP HTTP server: %w", err)
		}

		stops = append(stops, startExternalMCPServer(rt.RunCtx, listener, rt.ExternalMCPUsers, agents, rt.Sessions, rt, rt.Log).Close)
	}

	stopWeb, err := startWebRPC(rt, slack, cronjobs)
	if err != nil {
		return nil, nil, err
	}
	stops = append(stops, stopWeb)

	rt.AttachSlack(slack)
	slack.Start(rt.RunCtx)

	return done, stops, nil
}
