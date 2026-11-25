package backend

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

type runtimeReloader interface {
	Reload(string) (string, error)
}

type runtimeRefresher interface {
	Refresh() error
}

type inertRefresher struct{}

func (inertRefresher) Refresh() error { return nil }

type sandboxPreparer interface {
	Prepare(ctx context.Context, dir string, log *slog.Logger) *sandbox.Runtime
}

type Runtime struct {
	Cfg               *config.LockedConfig
	Log               *slog.Logger
	logLevel          *slog.LevelVar
	RunCtx            context.Context
	Sessions          *SessionService
	ExternalMCPAgents runtimeRefresher
	Routines          runtimeRefresher
	Assemble          FrontendAssembler
	sandboxes         sandboxPreparer

	reloadMu sync.Mutex

	slackWarningMu   sync.Mutex
	slackWarningText string

	TextRouter protocol.PrimaryTextRouter
	threads    *threadBridgeManager
	background *backgroundRegistry
	questions  *webQuestions
	shutdown   *runShutdown

	eventsMu    sync.Mutex
	subscribers map[chan protocol.Event]<-chan struct{}
}

func (r *Runtime) Restart(reason string) string {
	if !r.shutdown.start(reason, true) {
		r.Log.Warn("restart requested while shutdown already in progress", "reason", reason)
	}

	return "restart requested; runtime cancellation started"
}

func (r *Runtime) Reload(reason string) (string, error) {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	cfg := r.Cfg.Clone()
	r.Log.Info("reload requested", "reason", reason)

	var warnings string

	if err := skel.ReplaceRuntimeAssetsAfterValidation(cfg.Workspace, cfg.RuntimeDirName(), new(cfg.Behavior()), r.Log, r.Cfg, func(runtimeDir string) error {
		var err error
		if warnings, err = validateRuntimeAssets(r.Cfg, runtimeDir, r.Log); err != nil {
			return err
		}

		return r.Assemble.ValidateAssets(cfg, runtimeDir)
	}, func() error { return nil }); err != nil {
		return "", fmt.Errorf("reload runtime assets: %w", err)
	}

	r.slackWarningMu.Lock()
	r.slackWarningText = warnings
	r.slackWarningMu.Unlock()

	if err := r.ExternalMCPAgents.Refresh(); err != nil {
		return "", fmt.Errorf("refresh external MCP agents: %w", err)
	}

	if err := r.Routines.Refresh(); err != nil {
		return "", fmt.Errorf("refresh routines: %w", err)
	}

	if err := r.resumeScheduledSessions(r.RunCtx); err != nil {
		return "", err
	}

	r.prepareSandbox()

	if warnings != "" {
		return "rocketclaw runtime assets reloaded\n\n" + warnings, nil
	}

	return "rocketclaw runtime assets reloaded", nil
}

func (r *Runtime) SaveBehavior(behavior *config.Behavior) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	candidate := r.Cfg.Clone()
	previous := candidate.Behavior()

	candidate.SetBehavior(behavior)

	if err := candidate.ValidateBehavior(); err != nil {
		return fmt.Errorf("validate behavior settings: %w", err)
	}

	for _, embedded := range [...]struct{ field, model string }{{"embedded_agents.main.model", candidate.EmbeddedAgents.Main.Model}, {"title_model", candidate.TitleModel}} {
		if provider, _, qualified := strings.Cut(embedded.model, "/"); qualified && !slices.Contains(candidate.ProviderNames(), provider) {
			return fmt.Errorf("validate behavior settings: %s: provider %q is not configured", embedded.field, provider)
		}
	}

	normalized := candidate.Behavior()

	tx, err := r.Sessions.db.BeginTx(r.RunCtx, nil)
	if err != nil {
		return fmt.Errorf("begin behavior settings save: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := putBehaviorSettings(r.RunCtx, tx, &normalized); err != nil {
		return err
	}

	var warnings string

	validate := func(runtimeDir string) error {
		var err error
		if warnings, err = validateRuntimeAssets(config.NewLockedConfig(candidate), runtimeDir, r.Log); err != nil {
			return err
		}

		if err := r.Assemble.ValidateAssets(candidate, runtimeDir); err != nil {
			return fmt.Errorf("validate frontend assets: %w", err)
		}

		return nil
	}
	applied := false
	apply := func() error {
		applied = true

		SetLogLevel(r.logLevel, normalized.Logging.Level)

		if err := r.ExternalMCPAgents.Refresh(); err != nil {
			return fmt.Errorf("refresh external MCP agents: %w", err)
		}

		if err := r.Routines.Refresh(); err != nil {
			return fmt.Errorf("refresh routines: %w", err)
		}

		return commitState(tx, "behavior settings")
	}
	setBehavior := func(behavior *config.Behavior) {
		_ = r.Cfg.Do(func(cfg *config.Config) error {
			cfg.SetBehavior(behavior)

			return nil
		})
	}

	if !slices.Equal(previous.Overlays, normalized.Overlays) {
		err = skel.ReplaceRuntimeAssetsAfterValidation(candidate.Workspace, candidate.RuntimeDirName(), &normalized, r.Log, r.Cfg, validate, apply)
	} else if err = validate(candidate.RuntimeDirName()); err == nil {
		setBehavior(&normalized)

		if err = apply(); err != nil {
			setBehavior(&previous)
		}
	}

	if err == nil {
		r.slackWarningMu.Lock()
		r.slackWarningText = warnings
		r.slackWarningMu.Unlock()

		r.prepareSandbox()

		return nil
	}

	if applied {
		SetLogLevel(r.logLevel, previous.Logging.Level)

		if errRefresh := r.ExternalMCPAgents.Refresh(); errRefresh != nil {
			err = errors.Join(err, fmt.Errorf("refresh external MCP agents after restoring: %w", errRefresh))
		}

		if errRefresh := r.Routines.Refresh(); errRefresh != nil {
			err = errors.Join(err, fmt.Errorf("refresh routines after restoring: %w", errRefresh))
		}
	}

	return fmt.Errorf("behavior settings rejected, nothing changed: %w", err)
}

func (r *Runtime) SlackWarning() string {
	r.slackWarningMu.Lock()
	defer r.slackWarningMu.Unlock()

	return r.slackWarningText
}

func (r *Runtime) Subscribe(ctx context.Context) iter.Seq[protocol.Event] {
	ctx, cancel := context.WithCancel(ctx)
	events := make(chan protocol.Event)

	r.eventsMu.Lock()
	if r.subscribers == nil {
		r.subscribers = make(map[chan protocol.Event]<-chan struct{})
	}

	r.subscribers[events] = ctx.Done()
	r.eventsMu.Unlock()

	return func(yield func(protocol.Event) bool) {
		defer func() {
			cancel()
			r.eventsMu.Lock()
			delete(r.subscribers, events)
			r.eventsMu.Unlock()
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case event := <-events:
				if !yield(event) {
					return
				}
			}
		}
	}
}

func (r *Runtime) PublishOutbound(ctx context.Context, message *protocol.OutboundMessage) error {
	r.eventsMu.Lock()
	subscribers := maps.Clone(r.subscribers)
	r.eventsMu.Unlock()

	var errDelivery error

	for events, stopped := range subscribers {
		event := protocol.Event{Message: protocol.CloneOutboundMessage(message), Acknowledgement: make(chan error, 1)}
		select {
		case <-stopped:
			continue
		case <-ctx.Done():
			errDelivery = errors.Join(errDelivery, ctx.Err())
		case events <- event:
			select {
			case err := <-event.Acknowledgement:
				errDelivery = errors.Join(errDelivery, err)
			case <-stopped:
				select {
				case err := <-event.Acknowledgement:
					errDelivery = errors.Join(errDelivery, err)
				default:
					errDelivery = errors.Join(errDelivery, context.Canceled)
				}
			case <-ctx.Done():
				errDelivery = errors.Join(errDelivery, ctx.Err())
			}
		}
	}

	return errDelivery
}

func (r *Runtime) AttachSlack(slack slackLookup) {
	r.threads.mu.Lock()
	r.threads.slackLookups = slack
	r.threads.mu.Unlock()
}

func (r *Runtime) resumeScheduledSessions(ctx context.Context) error {
	ids, err := queryStrings(ctx, r.Sessions.db, `SELECT conversation_id FROM conversations WHERE paused AND sticky = 'global' AND agent = ANY($1) ORDER BY 1`, "scheduled paused sessions", r.Sessions.scheduledAgents())
	if err != nil {
		return err
	}

	for _, id := range ids {
		if err := r.SetAgentPaused(ctx, id, "RocketClaw", false); err != nil {
			return err
		}
	}

	return nil
}
