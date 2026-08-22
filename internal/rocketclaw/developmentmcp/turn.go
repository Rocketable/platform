package developmentmcp

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

// RunTryTurn stages a try tree and runs one Development MCP chat turn.
func RunTryTurn(ctx context.Context, workspace, runtimeDir string, overlays []string, cfg *config.Config, logger *slog.Logger, chat *harnessbridge.DevelopmentChat, baseOverlay string, files []skel.OverlayFile, agent, prompt string) (thinking, answer string, err error) {
	stage, err := skel.StageLiveRuntime(workspace, runtimeDir, overlays, baseOverlay, files, logger)
	if err != nil {
		return "", "", fmt.Errorf("stage live runtime: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	result, err := harnessbridge.RunDevelopmentTurn(ctx, cfg, stage, agent, prompt, logger, chat)
	if err != nil {
		return "", "", fmt.Errorf("run development turn: %w", err)
	}

	return result.Thinking, result.Answer, nil
}
