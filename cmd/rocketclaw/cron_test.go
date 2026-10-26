package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

// A run with a destination records it for the cron's agent and runs; the destination shows the run without a copy.
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
			progress := &backend.RawRunProgress{ConversationID: "X", Cronjob: progressCronjob}
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

// A scheduled run without a destination only stores and waits; the backend's
// delivery reports any reply in a Web chat.
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
	result, err := runner.Run(t.Context(), "job", "prompt", &backend.RawRunProgress{ConversationID: "X"})
	require.ErrorIs(t, err, errRun)
	require.Empty(t, result.ConversationID)
	require.Equal(t, []string{"create:X", "run"}, operations)
}

// A destination that cannot be created stops the run before it starts.
func TestCronRunStopsWhenDestinationFails(t *testing.T) {
	errCreate := errors.New("create failed")
	core := &BackendMock{CreateConversationFunc: func(context.Context, protocol.Conversation) error { return errCreate }}
	_, err := (&cronRunner{backend: core}).Run(t.Context(), "job", "prompt", &backend.RawRunProgress{ConversationID: "X", SyncDestination: "web-chat"})
	require.ErrorIs(t, err, errCreate)
	require.Empty(t, core.RunTurnCalls(), "no turn runs")
}
