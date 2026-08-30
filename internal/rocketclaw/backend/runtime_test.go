package backend

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
)

type stubSlack struct{}

func (stubSlack) HandleBroadcast(context.Context, *protocol.Broadcast) protocol.BroadcastAcknowledgement {
	return protocol.BroadcastAcknowledgement{Status: protocol.BroadcastDropped}
}
func (stubSlack) Start(context.Context) error { return nil }
func (stubSlack) Stop(context.Context) error  { return nil }
func (stubSlack) SendResponse(context.Context, *protocol.OutboundMessage) error {
	return nil
}
func (stubSlack) AbortResponse(*protocol.OutboundMessage) {}
func (stubSlack) StartNewThreadRoot(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error) {
	return protocol.StartNewThreadRootResult{}, nil
}
func (stubSlack) AskUserQuestion(context.Context, *protocol.AskUserQuestionRequest) (protocol.AskUserQuestionAnswer, error) {
	return protocol.AskUserQuestionAnswer{}, nil
}
func (stubSlack) DrainSteers(context.Context, string) []string { return nil }
func (stubSlack) ActivateEnqueue(context.Context, *protocol.ThreadQueueItem, *protocol.InboundMessage) error {
	return nil
}
func (stubSlack) SetPendingSteersSink(protocol.PendingSteersSink)      {}
func (stubSlack) RestorePendingSteers(string, []protocol.PendingSteer) {}
func (stubSlack) DiscardPendingSteers(context.Context, []protocol.PendingSteer) {
}

func TestAttachSlackAndSubmitExternalMCP(t *testing.T) {
	manager := newThreadBridgeManager(new(config.Config), nil, slog.New(slog.DiscardHandler), func(Config) directBridge {
		return nil
	})

	var (
		asker protocol.UserQuestionAsker
		drain func(context.Context, string, rocketcode.TurnPhase) []string
		root  func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error)
	)

	rt := &Runtime{threads: manager, slackAsker: &asker, drainSlack: &drain, startThreadRoot: &root}
	rt.AttachSlack(stubSlack{})
	require.True(t, asker.ExposeTool())
	require.Empty(t, drain(t.Context(), "c", 0))
	got, err := root(t.Context(), &protocol.StartNewThreadRequest{})
	require.NoError(t, err)
	require.Equal(t, protocol.StartNewThreadRootResult{}, got)
	require.NoError(t, manager.output(t.Context(), protocol.NewOutboundMessage(protocol.SourceSlack, "c", "x")))
}
