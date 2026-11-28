package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	routinefrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/routine"
	slackconnector "github.com/Rocketable/platform/internal/rocketclaw/frontend/slack"
)

type processAssembler struct {
	tailnet *tailnetNode
}

type slackFrontend interface {
	ChannelAgentChoices(context.Context, string) ([]string, error)
	SidebarChannelAgentChoices(context.Context, string) (string, []string, error)
	SlackNames(context.Context, []string) map[string]string
	SlackTagsMatching(context.Context, string) []string
	SendSessionMention(ctx context.Context, userID, link string) error
	SlackMentionEmail(ctx context.Context, userID string) string
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

func (inertSlack) SendSessionMention(context.Context, string, string) error {
	return errors.New("direct notifications are unavailable")
}

func (inertSlack) SlackMentionEmail(context.Context, string) string { return "" }

func (inertSlack) RenameThreadSession(context.Context, string, string) {}

func (inertSlack) StartEvents(ctx context.Context, _ frontend.Backend) <-chan struct{} {
	return ctx.Done()
}

func (inertSlack) Start(context.Context) {}

func (inertSlack) Stop(context.Context) error {
	return nil
}

func (a *processAssembler) Gate(ctx context.Context, cfg *config.Config, log *slog.Logger) (func() error, error) {
	node, err := startTailnet(ctx, cfg.Web.Tsnet, log)
	if err != nil {
		return nil, err
	}

	a.tailnet = node

	return node.server.Close, nil
}

func (a *processAssembler) Assemble(rt *backend.Runtime) (done <-chan struct{}, stops []func(context.Context) error, err error) {
	cfg := rt.Cfg.Clone()

	runner := &routineRunner{backend: rt}
	routines := routinefrontend.New(rt.Cfg, cfg.RuntimeDirName(), rt.Sessions, runner, rt.Log)

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

	if err := routines.Start(rt.RunCtx); err != nil {
		return nil, nil, fmt.Errorf("start routines: %w", err)
	}

	stops = append(stops, routines.Stop)
	rt.Routines = routines
	done = slack.StartEvents(rt.RunCtx, rt)

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

		stops = append(stops, startExternalMCPServer(rt.RunCtx, listener, cfg.MCPExternal.Users, agents, rt.Sessions, rt, rt.Log).Close)
	}

	stops = append(stops, startWebRPC(rt, a.tailnet, slack, routines))

	rt.AttachSlack(slack)
	slack.Start(rt.RunCtx)

	return done, stops, nil
}
