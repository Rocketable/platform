package developmentmcp

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/Rocketable/platform/internal/rocketclaw/agentlint"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

// LintTry stages a try tree and lints it.
func LintTry(workspace, runtimeDir string, overlays []string, baseOverlay string, files []skel.OverlayFile, cfg *config.Config, logger *slog.Logger) (agentlint.Result, error) {
	stage, err := skel.StageLiveRuntime(workspace, runtimeDir, overlays, baseOverlay, files, logger)
	if err != nil {
		return agentlint.Result{}, fmt.Errorf("stage live runtime: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	result, err := agentlint.Lint(stage, cfg)
	if err != nil {
		return agentlint.Result{}, fmt.Errorf("lint staged runtime: %w", err)
	}

	return result, nil
}
