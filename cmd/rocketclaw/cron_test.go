package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

func TestCronRunnerRejectsChannelWithoutAgents(t *testing.T) {
	runner := &cronRunner{config: &config.Config{Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops"}}}}}
	_, err := runner.Run(t.Context(), "job", "prompt", slog.New(slog.DiscardHandler), &backend.RawRunProgress{TextChannel: "#ops"})
	require.ErrorContains(t, err, "has no configured agents")
}

func TestCronProducerAlwaysSyncsBeforeReturning(t *testing.T) {
	progressCronjob := &protocol.CronjobMessage{RelativePath: "cron/daily.md", Agent: "job-agent", RanAt: "2000-01-02T03:04:05Z"}
	for _, existing := range []bool{false, true} {
		for _, outcome := range []string{"normal", "run-error", "interrupted", "sync-error"} {
			t.Run(fmt.Sprintf("existing=%t/%s", existing, outcome), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var operations []string
				errRun := errors.New("producer run failed")
				errSync := errors.New("producer sync failed")
				core := &BackendMock{
					CreateConversationFunc: func(_ context.Context, conversation protocol.Conversation) error {
						operations = append(operations, "create:"+conversation.ID+":"+conversation.Agent)
						return nil
					},
					RunTurnFunc: func(_ context.Context, inbound *protocol.InboundMessage) error {
						operations = append(operations, "run:"+inbound.ConversationID)
						require.Equal(t, "slack-thread:C1:1.2", inbound.SyncDestination)
						require.Equal(t, "job prompt", inbound.Text)
						require.True(t, inbound.RequireOutputDecision)
						require.Equal(t, progressCronjob, inbound.Cronjob)
						if outcome == "interrupted" {
							cancel()
							return context.Canceled
						}
						if outcome == "run-error" {
							return errRun
						}
						return nil
					},
					SyncConversationFunc: func(syncCtx context.Context, source, destination string) error {
						require.NoError(t, syncCtx.Err())
						operations = append(operations, "sync:"+source+":"+destination)
						if outcome == "sync-error" {
							return errSync
						}
						return nil
					},
				}
				slack := &SlackFrontendMock{StartNewThreadRootFunc: func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error) {
					operations = append(operations, "root")
					return protocol.StartNewThreadRootResult{Target: protocol.TextConversationTarget{ChannelID: "C1", ThreadID: "1.2"}}, nil
				}}
				runner := &cronRunner{backend: core, slack: slack, config: &config.Config{Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"channel-agent"}}}}}}
				progress := &backend.RawRunProgress{ConversationID: "X", TextChannel: "#ops", Cronjob: progressCronjob}
				if existing {
					progress.SyncDestination = "slack-thread:C1:1.2"
				}
				result, err := runner.Run(ctx, "job-agent", "job prompt", slog.New(slog.DiscardHandler), progress)
				require.Equal(t, "slack-thread:C1:1.2", result.ConversationID)
				switch outcome {
				case "run-error":
					require.ErrorIs(t, err, errRun)
				case "interrupted":
					require.ErrorIs(t, err, context.Canceled)
				case "sync-error":
					require.ErrorIs(t, err, errSync)
				default:
					require.NoError(t, err)
				}
				want := []string{"create:slack-thread:C1:1.2:channel-agent", "create:X:job-agent", "run:X", "sync:X:slack-thread:C1:1.2"}
				if !existing {
					want = append([]string{"root"}, want...)
				}
				require.Equal(t, want, operations)
			})
		}
	}
}
