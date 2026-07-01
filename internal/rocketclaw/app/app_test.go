package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
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

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
