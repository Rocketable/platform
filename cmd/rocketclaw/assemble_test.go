package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func TestAssembleFrontendsReportsSlackStartError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	rt := backend.RuntimeFor()
	rt.Cfg = &config.Config{}
	rt.Log = slog.New(slog.DiscardHandler)
	rt.RunCtx = ctx
	rt.Channels = protocol.NewChannels()
	_, err := processAssembler{}.Assemble(rt)
	require.Error(t, err)
	require.ErrorContains(t, err, "start Slack connector")
}
