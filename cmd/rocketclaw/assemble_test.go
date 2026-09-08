package main

import (
	"context"
	"log/slog"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func TestWaitBroadcastReply(t *testing.T) {
	t.Run("returns reply", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ch := make(chan protocol.Broadcast)
			reply := &protocol.InboundMessage{Text: "ok"}

			go func() {
				b := <-ch
				b.RelayResponse <- protocol.BroadcastReply{Message: reply}
			}()

			got, err := waitBroadcastReply(t.Context(), ch, &protocol.Broadcast{})
			require.NoError(t, err)
			require.Equal(t, reply, got.Message)
		})
	})
	t.Run("returns reply error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ch := make(chan protocol.Broadcast)
			replyErr := context.Canceled

			go func() {
				b := <-ch
				b.RelayResponse <- protocol.BroadcastReply{Err: replyErr}
			}()

			got, err := waitBroadcastReply(t.Context(), ch, &protocol.Broadcast{})
			require.ErrorIs(t, err, replyErr)
			require.Nil(t, got.Message)
		})
	})
	t.Run("cancels before send", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := waitBroadcastReply(ctx, make(chan protocol.Broadcast), &protocol.Broadcast{})
		require.ErrorIs(t, err, context.Canceled)
	})
	t.Run("cancels while waiting", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ch := make(chan protocol.Broadcast)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)

			go func() {
				_, err := waitBroadcastReply(ctx, ch, &protocol.Broadcast{})
				done <- err
			}()

			<-ch
			cancel()
			require.ErrorIs(t, <-done, context.Canceled)
		})
	})
}

func TestAssembleFrontendsReportsSlackStartError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	refresh := func() error { return nil }
	rt := &backend.Runtime{
		Cfg:                      &config.Config{},
		Log:                      slog.New(slog.DiscardHandler),
		RunCtx:                   ctx,
		Channels:                 protocol.NewChannels(),
		RefreshExternalMCPAgents: &refresh,
	}
	_, _, _, err := processAssembler{}.Assemble(rt)
	require.Error(t, err)
	require.ErrorContains(t, err, "start Slack connector")
}
