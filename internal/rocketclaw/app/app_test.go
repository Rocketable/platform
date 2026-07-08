package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/externalmcp"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfiguredMainOutputTargetsSelectsSlack(t *testing.T) {
	assert.Equal(t, []events.OutputTarget{events.OutputTargetSlackMain}, configuredMainOutputTargets(&config.Config{Slack: config.SlackConfig{Enabled: true}}))
	assert.Empty(t, configuredMainOutputTargets(&config.Config{}))
}

func TestOutboundLoopDeliversSlackMessagesInOrder(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	seen := make(chan int, 2)

	done := make(chan error, 1)
	go func() {
		done <- outboundLoop(ctx, bus, func(_ context.Context, msg *events.OutboundMessage) error {
			seen <- msg.Sequence
			return nil
		}, testLogger())
	}()

	first := events.NewMainOutboundMessage(events.SourceSystem, "first")
	first.Sequence = 1
	first.Targets = []events.OutputTarget{events.OutputTargetSlackMain}
	second := events.NewMainOutboundMessage(events.SourceSystem, "second")
	second.Sequence = 2
	second.Targets = []events.OutputTarget{events.OutputTargetSlackMain}

	require.NoError(t, bus.PublishOutbound(context.Background(), first))
	require.NoError(t, bus.PublishOutbound(context.Background(), second))

	require.Equal(t, 1, <-seen)
	require.Equal(t, 2, <-seen)
	require.NoError(t, first.WaitDelivered(context.Background()))
	require.NoError(t, second.WaitDelivered(context.Background()))
	cancel()
	require.NoError(t, <-done)
}

func TestOutboundLoopPropagatesSlackDeliveryErrorsToWaitDelivered(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errDelivery := errors.New("send failed")
	attempted := make(chan struct{}, 1)

	done := make(chan error, 1)
	go func() {
		done <- outboundLoop(ctx, bus, func(context.Context, *events.OutboundMessage) error {
			attempted <- struct{}{}
			return errDelivery
		}, testLogger())
	}()

	msg := events.NewMainOutboundMessage(events.SourceSystem, "hello")
	msg.Targets = []events.OutputTarget{events.OutputTargetSlackMain}
	require.NoError(t, bus.PublishOutbound(context.Background(), msg))
	<-attempted
	cancel()

	err := msg.WaitDelivered(context.Background())
	require.ErrorContains(t, err, "send failed")
	require.NoError(t, <-done)
}

func TestOutboundLoopMarksNoTargetMessagesDelivered(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- outboundLoop(ctx, bus, func(context.Context, *events.OutboundMessage) error { return nil }, testLogger())
	}()

	msg := events.NewMainOutboundMessage(events.SourceSystem, "hello")
	require.NoError(t, bus.PublishOutbound(context.Background(), msg))
	require.NoError(t, msg.WaitDelivered(context.Background()))
	cancel()
	require.NoError(t, <-done)
}

func TestOutboundLoopReturnsDeadlineError(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()

	err := outboundLoop(ctx, bus, func(context.Context, *events.OutboundMessage) error { return nil }, testLogger())
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestSubmitExternalMCPInputPreservesPublicConversationMetadata(t *testing.T) {
	var captured *events.InboundMessage

	submit := func(_ context.Context, agent, conversationID string, inbound *events.InboundMessage, activation harnessbridge.ActivationHook) error {
		assert.Equal(t, "planner", agent)
		assert.Equal(t, "external_mcp:planner:private", conversationID)

		captured = inbound
		require.NoError(t, activation(context.Background(), inbound))
		inbound.CompleteResponse("answer", nil)

		return nil
	}

	content := &events.InboundContent{Text: "prompt"}
	result, err := submitExternalMCPInput(context.Background(), submit, "planner", "external_mcp:planner:private", content, map[string]string{"ticket": "123"}, "alice", nil, "public-1", harnessbridge.NoopActivationHook)

	require.NoError(t, err)
	require.NotNil(t, captured)
	assert.Equal(t, events.SourceExternalMCP, captured.Source)
	assert.Equal(t, "public-1", captured.Metadata["external_conversation_id"])
	assert.Equal(t, "123", captured.Metadata["ticket"])
	assert.Equal(t, "alice", captured.Metadata[events.InboundPrincipalMetadataKey])
	assert.Nil(t, captured.SlackReply)
	assert.Equal(t, externalmcp.SessionResult{ExternalConversationID: "public-1", Agent: "planner", Answer: "answer", Attachments: []externalmcp.SessionAttachment{}}, result)
}

func TestSubmitExternalMCPInputWaitsForOwnQueuedTurnResult(t *testing.T) {
	type queuedTurn struct {
		inbound    *events.InboundMessage
		activation harnessbridge.ActivationHook
	}

	queue := make(chan queuedTurn, 2)
	started := make(chan string, 2)
	releaseRecovered := make(chan struct{})
	relayed := make(chan struct{}, 1)

	go func() {
		for turn := range queue {
			inbound := turn.inbound
			if err := turn.activation(context.Background(), inbound); err != nil {
				inbound.CompleteResponse("", err)

				continue
			}

			started <- inbound.Text

			if inbound.Text == "recovered" {
				<-releaseRecovered
				inbound.CompleteResponse("recovered answer", nil)

				continue
			}

			inbound.CompleteResponse("follow-up answer", nil)
		}
	}()

	recovered := events.NewMainInboundMessage(events.SourceExternalMCP, events.InboundKindPrompt, "", "recovered", false)
	queue <- queuedTurn{inbound: recovered, activation: harnessbridge.NoopActivationHook}

	require.Equal(t, "recovered", <-started)

	resultCh := make(chan externalmcp.SessionResult, 1)
	errCh := make(chan error, 1)
	submit := func(ctx context.Context, _ string, _ string, inbound *events.InboundMessage, activation harnessbridge.ActivationHook) error {
		select {
		case queue <- queuedTurn{inbound: inbound, activation: activation}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	go func() {
		content := &events.InboundContent{Text: "follow-up"}
		activation := func(context.Context, *events.InboundMessage) error {
			relayed <- struct{}{}

			return nil
		}

		result, err := submitExternalMCPInput(context.Background(), submit, "planner", "external_mcp:planner:private", content, nil, "", nil, "public-1", activation)
		if err != nil {
			errCh <- err

			return
		}

		resultCh <- result
	}()

	select {
	case <-relayed:
		t.Fatal("follow-up relay posted before recovered turn released")
	case result := <-resultCh:
		t.Fatalf("follow-up completed before recovered turn released: %#v", result)
	case err := <-errCh:
		t.Fatalf("follow-up failed before recovered turn released: %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	close(releaseRecovered)
	require.Equal(t, struct{}{}, <-relayed)
	require.Equal(t, "follow-up", <-started)

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case result := <-resultCh:
		assert.Equal(t, externalmcp.SessionResult{ExternalConversationID: "public-1", Agent: "planner", Answer: "follow-up answer", Attachments: []externalmcp.SessionAttachment{}}, result)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for follow-up result")
	}

	close(queue)
}

func TestExternalMCPInboundContentProvidesRelayAttachments(t *testing.T) {
	content, outbound, err := externalMCPInboundContent([]externalmcp.SessionPromptAttachment{{
		Name:       "report.txt",
		MIMEType:   "text/plain",
		DataBase64: "cmVwb3J0",
	}})

	require.NoError(t, err)
	assert.Equal(t, []string{"External MCP text file attachment report.txt (text/plain):\nreport"}, content.TextAttachments)
	assert.Equal(t, []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}, outbound)
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
