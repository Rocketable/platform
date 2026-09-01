package backend

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketcode"
	"golang.org/x/sync/errgroup"
)

// DevelopmentTurnResult is one Development MCP chat turn.
type DevelopmentTurnResult struct {
	Thinking, Answer string
}

// DevelopmentChat is in-memory session history for Development MCP turns.
type DevelopmentChat struct {
	memory memoryStore
}

// RunDevelopmentTurn runs one live-tools chat turn against a try tree.
func RunDevelopmentTurn(ctx context.Context, cfg *config.Config, runtimeDir, agent, prompt string, logger *slog.Logger, chat *DevelopmentChat) (DevelopmentTurnResult, error) {
	copied := *cfg
	if filepath.IsAbs(runtimeDir) {
		rel, errRel := filepath.Rel(copied.Workspace, runtimeDir)
		if errRel != nil {
			return DevelopmentTurnResult{}, fmt.Errorf("resolve development runtime dir: %w", errRel)
		}

		runtimeDir = rel
	}

	copied.WorkDir = runtimeDir

	root, agents, skills, resolver, err := prepareRocketCode(&copied, agent, logger, toolModePersistent)
	if err != nil {
		return DevelopmentTurnResult{}, err
	}
	defer func() { _ = root.Close() }()

	parent := filepath.ToSlash(filepath.Join(copied.RuntimeDirName(), ".rocketcode"))
	if err := root.MkdirAll(parent, 0o755); err != nil {
		return DevelopmentTurnResult{}, fmt.Errorf("create development shell temp parent dir: %w", err)
	}

	shellTempRel := filepath.ToSlash(filepath.Join(parent, "devmcp-"+rand.Text()))
	if err := root.Mkdir(shellTempRel, 0o700); err != nil {
		return DevelopmentTurnResult{}, fmt.Errorf("create development shell temp dir: %w", err)
	}
	defer func() { _ = root.RemoveAll(shellTempRel) }()

	rocketcodeConfig := isolatedRocketCodeConfig(&copied, filepath.Join(copied.Workspace, filepath.FromSlash(shellTempRel)), []rocketcode.Tool{
		reloadTool(func(context.Context, string) (string, error) { return "rocketclaw runtime assets reloaded", nil }),
		restartTool(func(context.Context, string) (string, error) {
			return "restart requested; runtime cancellation started", nil
		}, func(context.Context) error { return nil }),
	})

	looper, err := rocketcode.NewWithModelResolver(resolver, &rocketcodeConfig, root, agents, skills, agent, io.Discard)
	if err != nil {
		return DevelopmentTurnResult{}, fmt.Errorf("prepare development rocketcode run: %w", err)
	}

	input := make(chan rocketcode.PromptInput, 1)

	output := make(chan rocketcode.ChatResponse, 128)
	input <- rocketcode.PromptInput{Text: prompt, Responses: output}

	close(input)

	var group errgroup.Group
	group.Go(func() error {
		return looper.Loop(ctx, input, chat.memory.in(), chat.memory.out, make(chan os.Signal, 1))
	})

	var result DevelopmentTurnResult

	for item := range output {
		switch item.Kind {
		case rocketcode.ChatResponseAssistantCommentary, rocketcode.ChatResponseAssistantTool, rocketcode.ChatResponseReasoningSummary:
			result.Thinking = appendText(result.Thinking, rocketcodeThinkingText(item))
		case rocketcode.ChatResponseAssistantMessage:
			result.Answer = appendText(result.Answer, item.Text)
		}
	}

	if errGroup := group.Wait(); errGroup != nil {
		return DevelopmentTurnResult{}, fmt.Errorf("run development rocketcode turn: %w", errGroup)
	}

	return result, nil
}
