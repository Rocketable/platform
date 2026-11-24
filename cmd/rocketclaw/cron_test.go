package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

func TestCronProducerRunsWithDestination(t *testing.T) {
	progressCronjob := &protocol.CronjobMessage{RelativePath: "cron/daily.md", Agent: "job-agent", RanAt: "2000-01-02T03:04:05Z"}
	for _, outcome := range []string{"normal", "run-error", "interrupted"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var operations []string
			errRun := errors.New("producer run failed")
			core := &BackendMock{
				CreateConversationFunc: func(_ context.Context, conversation protocol.Conversation) error {
					operations = append(operations, "create:"+conversation.ID+":"+conversation.Agent)
					return nil
				},
				RunTurnFunc: func(_ context.Context, inbound *protocol.InboundMessage) error {
					operations = append(operations, "run:"+inbound.ConversationID)
					require.Equal(t, "web-chat", inbound.SyncDestination)
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
			}
			runner := &cronRunner{backend: core}
			progress := &backend.CronRunProgress{ConversationID: "X", Cronjob: progressCronjob}
			progress.SyncDestination = "web-chat"
			result, err := runner.Run(ctx, "job-agent", "job prompt", progress)
			require.Equal(t, "web-chat", result.ConversationID)
			switch outcome {
			case "run-error":
				require.ErrorIs(t, err, errRun)
			case "interrupted":
				require.ErrorIs(t, err, context.Canceled)
			default:
				require.NoError(t, err)
			}
			want := []string{"create:web-chat:job-agent", "create:X:job-agent", "run:X"}
			require.Equal(t, want, operations)
		})
	}
}

func TestScheduledCronOnlyRunsTurn(t *testing.T) {
	var operations []string
	errRun := errors.New("run failed")
	core := &BackendMock{
		CreateConversationFunc: func(_ context.Context, conversation protocol.Conversation) error {
			operations = append(operations, "create:"+conversation.ID)
			return nil
		},
		RunTurnFunc: func(_ context.Context, inbound *protocol.InboundMessage) error {
			require.Empty(t, inbound.SyncDestination)
			require.True(t, inbound.RequireOutputDecision)
			require.Nil(t, inbound.SlackReply, "a cron report never goes to Slack")
			operations = append(operations, "run")
			return errRun
		},
	}
	runner := &cronRunner{backend: core}
	result, err := runner.Run(t.Context(), "job", "prompt", &backend.CronRunProgress{ConversationID: "X"})
	require.ErrorIs(t, err, errRun)
	require.Empty(t, result.ConversationID)
	require.Equal(t, []string{"create:X", "run"}, operations)
}

func TestCronCompletionLogNamesWhetherTheRunReported(t *testing.T) {
	store := webSessions(t)

	for _, tc := range []struct {
		destination, reply, outcome string
		attachments                 []protocol.OutboundAttachment
	}{
		{reply: "report", outcome: "reported"},
		{attachments: []protocol.OutboundAttachment{{Name: "chart.png"}}, outcome: "reported"},
		{reply: " \n", outcome: "intentional_silence"},
		{destination: "web-chat", reply: "report", outcome: "reported"},
		{destination: "web-chat", outcome: "intentional_silence"},
	} {
		var logs bytes.Buffer

		core := &BackendMock{
			CreateConversationFunc: func(context.Context, protocol.Conversation) error { return nil },
			RunTurnFunc: func(_ context.Context, inbound *protocol.InboundMessage) error {
				inbound.CompleteResponseWithAttachments(tc.reply, tc.attachments, nil)
				return nil
			},
		}
		cronjobs := cronfrontend.New(config.NewLockedConfig(&config.Config{Workspace: t.TempDir()}), ".", store, &cronRunner{backend: core}, slog.New(slog.NewJSONHandler(&logs, nil)))

		_, err := cronjobs.RunOneOffCronjob(t.Context(), &protocol.OneOffCronjob{Agent: "job", Prompt: "prompt", RelativePath: "cron/daily.md", ConversationID: tc.destination})
		require.NoError(t, err)
		require.Contains(t, logs.String(), `"outcome":"`+tc.outcome+`"`, tc)
	}
}

func TestCronRunStopsWhenDestinationFails(t *testing.T) {
	errCreate := errors.New("create failed")
	core := &BackendMock{CreateConversationFunc: func(context.Context, protocol.Conversation) error { return errCreate }}
	_, err := (&cronRunner{backend: core}).Run(t.Context(), "job", "prompt", &backend.CronRunProgress{ConversationID: "X", SyncDestination: "web-chat"})
	require.ErrorIs(t, err, errCreate)
	require.Empty(t, core.RunTurnCalls(), "no turn runs")
}
