package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type RoutineRunProgress struct {
	ConversationID  string
	Run             *protocol.RoutineRun
	SyncDestination string
	Routine         *protocol.RoutineMessage
}

func prepareRocketCode(locked *config.LockedConfig, agent string) (*os.Root, harness.Agents, harness.Skills, *modelResolver, error) {
	cfg := locked.Clone()

	root, err := os.OpenRoot(cfg.Workspace)
	if err != nil {
		return nil, harness.Agents{}, harness.Skills{}, nil, fmt.Errorf("open workspace root: %w", err)
	}

	agents, skills, err := loadRocketCodeDefinitionsIn(root, locked, cfg.RuntimeDirName(), nil)
	if err != nil {
		_ = root.Close()
		return nil, harness.Agents{}, harness.Skills{}, nil, fmt.Errorf("open workspace agent and skills: %w", err)
	}

	appendOverlayPromptToAgent(agents, agent, cfg)

	return root, agents, skills, newModelResolver(cfg, slog.New(slog.DiscardHandler)), nil
}

func rocketcodeSpillDir(cfg *config.Config) string {
	return filepath.Join(cfg.Workspace, cfg.RuntimeDirName(), ".rocketcode", "spill")
}

func rocketcodeRetainedDir(conversationID string) string {
	sum := sha256.Sum256([]byte(conversationID))
	return filepath.Join("retained", hex.EncodeToString(sum[:]))
}
