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
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"golang.org/x/sync/errgroup"
)

// SideAskRunner runs one isolated Slack Side Ask against prefixed thread history.
type SideAskRunner struct {
	Config   *config.Config
	Sessions *SessionService
	Logger   *slog.Logger
}

// Run loads history through the stamped entry and executes one private RocketCode turn.
func (r SideAskRunner) Run(ctx context.Context, req protocol.SideAskRequest) error {
	observed, err := r.Sessions.ObserveEntries(ctx, req.ConversationID, 0)
	if err != nil {
		return fmt.Errorf("load side ask history: %w", err)
	}

	root, agents, skills, resolver, err := prepareRocketCode(r.Config, req.Agent, r.Logger, toolModePersistent)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	parent := filepath.ToSlash(filepath.Join(r.Config.RuntimeDirName(), ".rocketcode"))
	if err := root.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create side ask shell temp parent dir: %w", err)
	}

	shellTempRel := filepath.ToSlash(filepath.Join(parent, "side-ask-"+rand.Text()))
	if err := root.Mkdir(shellTempRel, 0o700); err != nil {
		return fmt.Errorf("create side ask shell temp dir: %w", err)
	}
	defer func() { _ = root.RemoveAll(shellTempRel) }()

	attachments := new(outboundAttachmentCollector)
	rocketcodeConfig := isolatedRocketCodeConfig(r.Config, filepath.Join(r.Config.Workspace, filepath.FromSlash(shellTempRel)), []rocketcode.Tool{attachments.Tool(root), reloadTool(func(context.Context, string) (string, error) { return "rocketclaw runtime assets reloaded", nil })})

	looper, err := rocketcode.NewWithModelResolver(resolver, &rocketcodeConfig, root, agents, skills, req.Agent, io.Discard)
	if err != nil {
		return fmt.Errorf("prepare side ask rocketcode run: %w", err)
	}

	sessionIn := sessionEntriesForProvider(func(yield func(rocketcode.SessionEntry, error) bool) {
		for i := range observed {
			if observed[i].ID > req.SessionEntryID {
				continue
			}

			if !yield(observed[i].Entry, nil) {
				return
			}
		}
	}, providerForModel(looper.DisplayModel))
	memory := new(memoryStore)
	input := make(chan rocketcode.PromptInput, 1)

	output := make(chan rocketcode.ChatResponse, 128)
	input <- rocketcode.PromptInput{Role: "", Text: provenanceHeader(promptProvenance{origin: "Slack", media: "Text"}) + "\n\n" + req.Question, Responses: output}

	close(input)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var group errgroup.Group
	group.Go(func() error {
		return looper.Loop(runCtx, input, sessionIn, memory.out, make(chan os.Signal, 1))
	})

	var errProgress error

	last := ""

	for item := range output {
		if errProgress != nil || runCtx.Err() != nil {
			continue
		}

		switch item.Kind {
		case rocketcode.ChatResponseAssistantCommentary, rocketcode.ChatResponseAssistantTool, rocketcode.ChatResponseReasoningSummary:
			thinking := rocketcodeThinkingText(item)
			if thinking == "" {
				continue
			}

			if err := req.Thinking(runCtx, thinking); err != nil {
				errProgress = fmt.Errorf("publish side ask thinking: %w", err)

				cancel()
			}
		case rocketcode.ChatResponseAssistantMessage:
			last = appendText(last, item.Text)
			if err := req.Message(runCtx, last); err != nil {
				errProgress = fmt.Errorf("publish side ask message: %w", err)

				cancel()
			}
		}
	}

	if err := ctx.Err(); err != nil {
		_ = group.Wait()
		return fmt.Errorf("run side ask: %w", err)
	}

	if errProgress != nil {
		_ = group.Wait()
		return errProgress
	}

	if errGroup := group.Wait(); errGroup != nil {
		return fmt.Errorf("run side ask: %w", errGroup)
	}

	return nil
}
