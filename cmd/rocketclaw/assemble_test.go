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
