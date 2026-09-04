package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

func TestCronRunnerRejectsChannelWithoutAgents(t *testing.T) {
	runner := &cronRunner{config: &config.Config{Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops"}}}}}
	_, err := runner.Run(t.Context(), "job", "prompt", &backend.RawRunProgress{TextChannel: "#ops"})
	require.ErrorContains(t, err, "has no configured agents")
}

func TestCronProducerAlwaysSyncsBeforeReturning(t *testing.T) {
	progressCronjob := &protocol.CronjobMessage{RelativePath: "cron/daily.md", Agent: "job-agent", RanAt: "2000-01-02T03:04:05Z"}
	for _, outcome := range []string{"normal", "run-error", "interrupted", "sync-error"} {
		t.Run(outcome, func(t *testing.T) {
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
					require.Nil(t, inbound.SlackReply)
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
			slack := &cronRootSenderMock{}
			runner := &cronRunner{backend: core, slack: slack, config: &config.Config{Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"channel-agent"}}}}}}
			progress := &backend.RawRunProgress{ConversationID: "X", TextChannel: "#ops", Cronjob: progressCronjob}
			progress.SyncDestination = "slack-thread:C1:1.2"
			result, err := runner.Run(ctx, "job-agent", "job prompt", progress)
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
			require.Equal(t, want, operations)
		})
	}
}

func TestScheduledCronPostsOnlyCompletedReport(t *testing.T) {
	for _, outcome := range []string{"visible", "silent", "error"} {
		t.Run(outcome, func(t *testing.T) {
			var operations []string
			core := &BackendMock{
				CreateConversationFunc: func(_ context.Context, conversation protocol.Conversation) error {
					operations = append(operations, "create:"+conversation.ID)
					return nil
				},
				RunTurnFunc: func(_ context.Context, inbound *protocol.InboundMessage) error {
					require.Equal(t, []string{"create:X"}, operations, "Slack must stay untouched during execution")
					require.Empty(t, inbound.SyncDestination)
					require.True(t, inbound.RequireOutputDecision)
					operations = append(operations, "run")
					if outcome == "error" {
						return errors.New("run failed")
					}
					text := ""
					if outcome == "visible" {
						text = "exact report"
					}
					inbound.CompleteResponseWithAttachments(text, nil, nil)
					return nil
				},
				SyncConversationFunc: func(_ context.Context, source, destination string) error {
					require.Equal(t, "X", source)
					require.Equal(t, "slack-thread:C1:1.2", destination)
					operations = append(operations, "sync")
					return nil
				},
			}
			slack := &cronRootSenderMock{SendCronjobRootFunc: func(_ context.Context, message *protocol.OutboundMessage) (protocol.TextConversationTarget, error) {
				require.Equal(t, "exact report", message.Text)
				require.True(t, message.Complete)
				operations = append(operations, "report")
				return protocol.TextConversationTarget{ChannelID: "C1", MessageID: "1.2", ThreadID: "1.2"}, nil
			}}
			runner := &cronRunner{backend: core, slack: slack, config: &config.Config{Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"main"}}}}}}
			result, err := runner.Run(t.Context(), "job", "prompt", &backend.RawRunProgress{ConversationID: "X", TextChannel: "#ops"})
			if outcome == "error" {
				require.ErrorContains(t, err, "run failed")
			} else {
				require.NoError(t, err)
			}
			if outcome == "visible" {
				require.Equal(t, "slack-thread:C1:1.2", result.ConversationID)
				require.Equal(t, []string{"create:X", "run", "report", "create:slack-thread:C1:1.2", "sync"}, operations)
			} else {
				require.Empty(t, result.ConversationID)
				require.Equal(t, []string{"create:X", "run"}, operations)
			}
		})
	}
}
