package backend

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
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

// Runtime is the backend after construction, before frontends.
type Runtime struct {
	Cfg               *config.LockedConfig
	Log               *slog.Logger
	RunCtx            context.Context
	Sessions          *SessionService
	ExternalMCPUsers  map[string]string
	ExternalMCPAgents runtimeRefresher
	CronJobs          runtimeRefresher
	assemble          FrontendAssembler

	reloadMu sync.Mutex

	TextRouter protocol.PrimaryTextRouter
	threads    *threadBridgeManager
	background *backgroundRegistry
	questions  *webQuestions

	eventsMu    sync.Mutex
	subscribers map[chan protocol.Event]<-chan struct{}

	// sandboxMu is apart from reloadMu: the Web reads sandboxErr while a reload prepares the sandbox.
	sandboxMu  sync.Mutex
	sandboxErr error
}

// Reload validates and replaces runtime assets, then refreshes External MCP agents and the cron jobs.
// It uses the startup configuration, never rereading the configuration file.
func (r *Runtime) Reload(reason string) (string, error) {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	cfg := r.Cfg.Clone()
	r.Log.Info("reload requested", "reason", reason)

	if err := skel.ReplaceRuntimeAssetsAfterValidation(cfg.Workspace, cfg.RuntimeDirName(), cfg.Overlays, r.Log, r.Cfg, func(runtimeDir string) error {
		if err := validateRuntimeAssets(r.Cfg, runtimeDir); err != nil {
			return err
		}

		return r.assemble.ValidateAssets(cfg, runtimeDir)
	}); err != nil {
		return "", fmt.Errorf("reload runtime assets: %w", err)
	}

	if err := r.ExternalMCPAgents.Refresh(); err != nil {
		return "", fmt.Errorf("refresh external MCP agents: %w", err)
	}

	if err := r.CronJobs.Refresh(); err != nil {
		return "", fmt.Errorf("refresh cron jobs: %w", err)
	}

	r.prepareSandbox()

	return "rocketclaw runtime assets reloaded", nil
}

// Subscribe registers a live-only event consumer. History is read separately.
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

// PublishOutbound completes delivery through live consumers, never a private
// request response stream. Each consumer receives an independent rendering copy.
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

// AttachSlack makes Slack the session search's Slack lookup.
func (r *Runtime) AttachSlack(slack slackLookup) {
	r.threads.mu.Lock()
	r.threads.slackLookups = slack
	r.threads.mu.Unlock()
}
