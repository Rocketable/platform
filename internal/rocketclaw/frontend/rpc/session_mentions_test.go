package rpc

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestNativeSessionMentionAttemptsDeliveryWhenQueuedButNotStashed(t *testing.T) {
	h := startWebHarness(t)
	cfg := h.rt.Cfg.Clone()
	cfg.Web.Tsnet = &config.TsnetConfig{Origin: "https://claw.example.test"}
	h.rt.Cfg.Store(cfg)

	ctx := metadata.NewOutgoingContext(t.Context(), tailnetUser("alice"))
	channels := &mockChannels{
		SendSessionMentionFunc: func(_ context.Context, id, link string) error {
			assert.Equal(t, "U42", id)
			assert.Equal(t, "https://claw.example.test/s/cHJpbWFyeQ", link)

			items, err := h.rt.QueueItems("primary")
			if err != nil {
				return fmt.Errorf("read admitted queue: %w", err)
			}

			if assert.Len(t, items, 2) {
				assert.Equal(t, "sent", items[1].ID)
			}

			return errors.New("test direct notification failed")
		},
		SlackMentionEmailFunc: func(context.Context, string) string { return "" },
	}
	h.server.channels = channels
	require.NoError(t, h.rt.Sessions.BeginGoal("primary", &backend.GoalState{Objective: "Hold queued work"}))

	for _, item := range []struct {
		id       string
		delivery PromptDelivery
		warning  string
	}{
		{"held", PromptDelivery_STASH, ""},
		{"sent", PromptDelivery_QUEUE, "One or more teammate notifications could not be sent."},
	} {
		reply, err := invoke[PromptResponse](ctx, h.conn, "Prompt", &PromptRequest{
			Id: "primary", MessageId: item.id, Text: "Ask Riley", Delivery: item.delivery, MentionIds: []string{"U42"},
		})
		require.NoError(t, err)
		require.Equal(t, item.warning, reply.GetMentionWarning())
	}

	require.Len(t, channels.SendSessionMentionCalls(), 1)
}

func TestRemoteSessionMentionsRejectTooManyRecipients(t *testing.T) {
	h := startWebHarness(t)
	cfg := h.rt.Cfg.Clone()
	cfg.Web.Tsnet = &config.TsnetConfig{Origin: "https://claw.example.test"}
	h.rt.Cfg.Store(cfg)

	channels := &mockChannels{
		SendSessionMentionFunc: func(context.Context, string, string) error { return nil },
		SlackMentionEmailFunc:  func(context.Context, string) string { return "" },
	}
	h.server.channels = channels

	ctx := metadata.NewOutgoingContext(t.Context(), tailnetUser("alice"))
	_, err := invoke[PromptResponse](ctx, h.conn, "NotifySessionMentions", &PromptRequest{
		Id: "opencode:box:s1", MentionIds: []string{"U1", "U2", "U3", "U4", "U5", "U6"},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Empty(t, channels.SendSessionMentionCalls())
}

func TestAcceptedSessionMentionReportsTurnFailure(t *testing.T) {
	notice := sessionMentionNotice{
		ids:     []string{"U42"},
		warning: new("One or more teammate notifications could not be sent."),
	}
	server := &Server{backend: &mockBackend{RunTurnFunc: func(context.Context, *protocol.InboundMessage) error {
		notice.accepted = true
		return errors.New("the agent was stopped")
	}}}

	reply, err := server.submitPrompt(t.Context(), &protocol.InboundMessage{}, protocol.GoalRequest{}, false, &notice)
	require.NoError(t, err)
	require.Contains(t, reply.GetMentionWarning(), "the agent was stopped")
	require.Contains(t, reply.GetMentionWarning(), "One or more teammate notifications could not be sent.")
}
