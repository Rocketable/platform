package backend

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/agentlint"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

func lintResultFromAgentlint(result agentlint.Result) protocol.LintResult {
	out := protocol.LintResult{Findings: make([]protocol.LintFinding, len(result.Findings))}
	for i := range result.Findings {
		out.Findings[i] = protocol.LintFinding{Code: result.Findings[i].Code, Severity: result.Findings[i].Severity, Path: result.Findings[i].Path, Message: result.Findings[i].Message}
	}

	return out
}

// LintTry stages a try tree and lints it.
func LintTry(workspace, runtimeDir string, overlays []string, baseOverlay string, files []protocol.OverlayFile, cfg *config.Config, logger *slog.Logger) (protocol.LintResult, error) {
	stage, err := skel.StageLiveRuntime(workspace, runtimeDir, overlays, baseOverlay, files, logger)
	if err != nil {
		return protocol.LintResult{}, fmt.Errorf("stage live runtime: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	result, err := agentlint.Lint(stage, cfg)
	if err != nil {
		return protocol.LintResult{}, fmt.Errorf("lint staged runtime: %w", err)
	}

	return lintResultFromAgentlint(result), nil
}

// KeyedConversationLocks serializes work per conversation id.
type KeyedConversationLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedConversationLock
}

type keyedConversationLock struct {
	refs int
	mu   sync.Mutex
}

// NewKeyedConversationLocks constructs an empty lock set.
func NewKeyedConversationLocks() *KeyedConversationLocks {
	return &KeyedConversationLocks{locks: map[string]*keyedConversationLock{}}
}

// Lock serializes one conversation id and returns the unlock function.
func (l *KeyedConversationLocks) Lock(key string) func() {
	l.mu.Lock()

	entry := l.locks[key]
	if entry == nil {
		entry = new(keyedConversationLock)
		l.locks[key] = entry
	}

	entry.refs++
	l.mu.Unlock()

	entry.mu.Lock()

	return func() {
		entry.mu.Unlock()
		l.mu.Lock()

		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}

// RunTryTurn stages a try tree and runs one Development MCP chat turn.
func RunTryTurn(ctx context.Context, workspace, runtimeDir string, overlays []string, cfg *config.Config, logger *slog.Logger, chat *DevelopmentChat, baseOverlay string, files []protocol.OverlayFile, agent, prompt string) (thinking, answer string, err error) {
	stage, err := skel.StageLiveRuntime(workspace, runtimeDir, overlays, baseOverlay, files, logger)
	if err != nil {
		return "", "", fmt.Errorf("stage live runtime: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	result, err := RunDevelopmentTurn(ctx, cfg, stage, agent, prompt, logger, chat)
	if err != nil {
		return "", "", fmt.Errorf("run development turn: %w", err)
	}

	return result.Thinking, result.Answer, nil
}
