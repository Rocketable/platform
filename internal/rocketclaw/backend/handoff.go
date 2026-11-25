package backend

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"golang.org/x/sync/errgroup"
)

func GenerateOneShot(ctx context.Context, locked *config.LockedConfig, agent, instructions, transcript string) (reply string, err error) {
	root, agents, skills, resolver, err := prepareRocketCode(locked, agent)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, root.Close()) }()

	active := agents.Items[agent]
	active.Prompt = instructions
	agents.Items[agent] = active

	cfg := locked.Clone()

	scratch := filepath.Join(cfg.RuntimeDirName(), ".rocketcode", "one-shot-"+rand.Text())
	if err := root.MkdirAll(scratch, 0o700); err != nil {
		return "", fmt.Errorf("create one-shot scratch: %w", err)
	}

	defer func() { err = errors.Join(err, root.RemoveAll(scratch)) }()

	runtimeConfig := harness.Config{ShellTempDir: filepath.Join(cfg.Workspace, scratch), SpillDir: rocketcodeSpillDir(cfg), RetainedResultDir: filepath.Join("retained", "unused"), ChildSessions: harness.InertChildSessions{}, Journal: harness.InertJournal{}, BackgroundJobs: harness.InertBackgroundJobs{}, TurnSecrets: harness.InertTurnSecrets{}}

	runtime, err := harness.NewWithModelResolver(resolver, &runtimeConfig, root, agents, skills, agent)
	if err != nil {
		return "", fmt.Errorf("prepare one-shot run: %w", err)
	}

	clear(runtime.Tools)

	memory := new(memoryStore)
	input := make(chan harness.PromptInput, 1)

	output := make(chan harness.ChatResponse)
	input <- harness.PromptInput{Role: harness.PromptInputRoleUser, Texts: []string{transcript}, Responses: output}

	close(input)

	var group errgroup.Group
	group.Go(func() error { return runtime.Loop(ctx, input, memory.in(), memory, make(chan os.Signal, 1)) })

	var text strings.Builder

	for response := range output {
		if response.Kind == harness.ChatResponseAssistantMessage {
			text.WriteString(response.Text)
		}
	}

	if err := group.Wait(); err != nil {
		return "", fmt.Errorf("generate one-shot reply: %w", err)
	}

	reply = strings.TrimSpace(text.String())
	if reply == "" {
		return "", errors.New("model returned an empty reply")
	}

	return reply, nil
}
