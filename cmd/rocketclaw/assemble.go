package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	clawcron "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	slackconnector "github.com/Rocketable/platform/internal/rocketclaw/frontend/slack"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

var (
	_ frontend.Backend    = (*backend.Runtime)(nil)
	_ clawcron.ThreadRoot = (*slackconnector.Connector)(nil)
)

type processAssembler struct{}

func (processAssembler) Assemble(rt *backend.Runtime) ([]func(context.Context) error, error) {
	var stops []func(context.Context) error

	rt.Log.Info("starting Slack connector")

	channels := make([]string, 0, len(rt.Cfg.Slack.Channels))
	agents := make(map[string][]string, len(rt.Cfg.Slack.Channels))
	for _, channel := range rt.Cfg.Slack.Channels {
		if channel.Channel == "@" {
			continue
		}

		agents[channel.Channel] = channel.Agents
		if strings.HasPrefix(channel.Channel, "#") {
			channels = append(channels, channel.Channel)
		}
	}

	slack := slackconnector.New(&rt.Cfg.Slack, protocol.BroadcastPublisher(rt.Channels.Broadcasts), rt.Channels.Broadcasts, rt, frontend.SideAsk{Backend: rt}, rt.Log)
	cronFront := clawcron.New(rt.Cfg.Workspace, rt.Cfg.RuntimeDirName(), channels, agents, rt, sessionScheduleStore{sessions: rt.Sessions}, slack, rt.Log)
	slack.SetCron(cronFront)

	if err := slack.Start(rt.RunCtx); err != nil {
		return nil, fmt.Errorf("start Slack connector: %w", err)
	}

	stops = append(stops, slack.Stop)

	if rt.Sessions != nil {
		slack.SetPendingSteersSink(protocol.PendingSteersSink{Set: rt.Sessions.SetPendingSteers})
	}

	for i := range rt.RecoveredTurns {
		slack.RestorePendingSteers(rt.RecoveredTurns[i].Checkpoint.ConversationKey, rt.RecoveredTurns[i].PendingSteers)
	}

	if rt.Cfg.MCPExternal.Enabled {
		if _, err := backend.ExternalMCPAgentsIn(rt.Cfg, rt.Cfg.RuntimeDirName()); err != nil {
			return nil, fmt.Errorf("load external MCP agents: %w", err)
		}

		users, errUsers := config.LoadExternalMCPUsers(rt.ConfigPath)
		if errUsers != nil {
			return nil, fmt.Errorf("load external MCP auth users: %w", errUsers)
		}

		agentExposed := func(agent string) bool {
			names, errAgents := backend.ExternalMCPAgentsIn(rt.Cfg, rt.Cfg.RuntimeDirName())
			return errAgents == nil && slices.Contains(names, agent)
		}
		externalMCP, err := startExternalMCPServer(rt.RunCtx, rt.Cfg, slack.StartThread, users, agentExposed, rt.Sessions, rt, rt.Log)
		if err != nil {
			return nil, err
		}

		stops = append(stops, externalMCP.Close)
	}

	if err := cronFront.Start(rt.RunCtx); err != nil {
		return nil, fmt.Errorf("start cronjobs: %w", err)
	}

	stops = append(stops, cronFront.Stop)

	return stops, nil
}

type sessionScheduleStore struct {
	sessions *backend.SessionService
}

func (s sessionScheduleStore) ResetSchedules() error {
	return s.sessions.ResetCronSchedules()
}

func (s sessionScheduleStore) SyncSchedules(states []clawcron.ScheduleState, now time.Time) error {
	converted := make([]backend.CronScheduleState, len(states))
	for i, state := range states {
		converted[i] = backend.CronScheduleState{ScheduleID: state.ScheduleID, RelativePath: state.RelativePath, NextDue: state.NextDue}
	}

	return s.sessions.SyncCronSchedules(converted, now)
}

func (s sessionScheduleStore) DueSchedules(now time.Time, limit int) ([]clawcron.ScheduleState, error) {
	due, err := s.sessions.DueCronSchedules(now, limit)
	if err != nil {
		return nil, err
	}

	out := make([]clawcron.ScheduleState, len(due))
	for i, state := range due {
		out[i] = clawcron.ScheduleState{ScheduleID: state.ScheduleID, RelativePath: state.RelativePath, NextDue: state.NextDue}
	}

	return out, nil
}

func (s sessionScheduleStore) ClaimSchedule(due clawcron.ScheduleState, nextDue, now time.Time) (clawcron.ScheduleRun, bool, error) {
	run, ok, err := s.sessions.ClaimCronSchedule(backend.CronScheduleState{ScheduleID: due.ScheduleID, RelativePath: due.RelativePath, NextDue: due.NextDue}, nextDue, now)
	if err != nil {
		return clawcron.ScheduleRun{}, false, err
	}

	return clawcron.ScheduleRun{ScheduleID: run.ScheduleID, RelativePath: run.RelativePath, DueAt: run.DueAt}, ok, nil
}

func (s sessionScheduleStore) CompleteRun(relativePath string, now time.Time) error {
	return s.sessions.CompleteCronRun(relativePath, now)
}
