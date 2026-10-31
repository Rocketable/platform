package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/agentlint"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

type runtimeReloader interface {
	Reload(string) (string, error)
}

type externalMCPAgents interface {
	Refresh() error
}

type inertExternalMCPAgents struct{}

func (inertExternalMCPAgents) Refresh() error { return nil }

// Runtime is the backend after construction, before frontends.
type Runtime struct {
	Cfg               *config.LockedConfig
	Log               *slog.Logger
	logLevel          *slog.LevelVar
	RunCtx            context.Context
	Sessions          *SessionService
	ExternalMCPUsers  map[string]string
	ExternalMCPAgents externalMCPAgents
	assemble          FrontendAssembler

	reloadMu sync.Mutex

	TextRouter protocol.PrimaryTextRouter
	threads    *threadBridgeManager
	background *backgroundRegistry
	questions  *webQuestions

	eventsMu    sync.Mutex
	subscribers map[chan protocol.Event]<-chan struct{}

	// justBashMu is apart from reloadMu: the Web reads justBashErr while a reload installs.
	justBashMu  sync.Mutex
	justBashErr error
}

// Reload validates and replaces runtime assets, then refreshes External MCP agents.
// It uses the live configuration, never rereading the configuration file.
func (r *Runtime) Reload(reason string) (string, error) {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	cfg := r.Cfg.Clone()
	r.Log.Info("reload requested", "reason", reason)

	if err := skel.ReplaceRuntimeAssetsAfterValidation(cfg.Workspace, cfg.RuntimeDirName(), new(cfg.Behavior()), r.Log, r.Cfg, func(runtimeDir string) error {
		if err := validateRuntimeAssets(r.Cfg, runtimeDir); err != nil {
			return err
		}

		return r.assemble.ValidateAssets(cfg, runtimeDir)
	}, func() error { return nil }); err != nil {
		return "", fmt.Errorf("reload runtime assets: %w", err)
	}

	if err := r.ExternalMCPAgents.Refresh(); err != nil {
		return "", fmt.Errorf("refresh external MCP agents: %w", err)
	}

	r.installJustBash()

	return "rocketclaw runtime assets reloaded", nil
}

// SaveBehavior validates behavior against the setup and runtime assets and applies it atomically: it
// writes the document in a transaction, swaps the runtime assets (only when the overlay list changes,
// publishing workspace edits) and behavior fields, sets the log level, refreshes External MCP agents,
// and commits last. Any failure restores the previous state and rolls the document back. The live
// config keeps the maps and slices of behavior, so the caller must not modify them afterward.
func (r *Runtime) SaveBehavior(behavior *config.Behavior) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	candidate := r.Cfg.Clone()
	previous := candidate.Behavior()

	candidate.SetBehavior(behavior)

	if err := candidate.ValidateBehavior(); err != nil {
		return fmt.Errorf("validate behavior settings: %w", err)
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

	validate := func(runtimeDir string) error {
		if err := validateRuntimeAssets(config.NewLockedConfig(candidate), runtimeDir); err != nil {
			return err
		}

		if err := r.assemble.ValidateAssets(candidate, runtimeDir); err != nil {
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

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit behavior settings: %w", err)
		}

		return nil
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
		return nil
	}

	if applied {
		SetLogLevel(r.logLevel, previous.Logging.Level)

		if errRefresh := r.ExternalMCPAgents.Refresh(); errRefresh != nil {
			err = errors.Join(err, fmt.Errorf("refresh external MCP agents after restoring: %w", errRefresh))
		}
	}

	return fmt.Errorf("behavior settings rejected, nothing changed: %w", err)
}

// InspectAgents lints, or draws the task delegation graph of, the agents of target against the
// live config, or reports the live Behavior Settings without who may use the server. Target
// "current" is the live runtime directory; "next" stages the workspace and the live overlays as the
// next start would. It returns the text rocketclaw lint, agent-graph, or settings prints and the
// number of lint findings.
func (r *Runtime) InspectAgents(target protocol.InspectTarget, kind protocol.InspectKind) (output string, findings int, err error) {
	cfg := r.Cfg.Clone()
	runtimeRoot := filepath.Join(cfg.Workspace, cfg.RuntimeDirName())

	switch target {
	case protocol.InspectCurrent:
	case protocol.InspectNext:
		stage, err := os.MkdirTemp(cfg.Workspace, ".rocketclaw-inspect-*")
		if err != nil {
			return "", 0, fmt.Errorf("create inspection stage: %w", err)
		}

		defer func() { _ = os.RemoveAll(stage) }()

		if err := skel.SyncEffectiveRuntimeAssets(cfg.Workspace, stage, cfg.Overlays, r.Log); err != nil {
			return "", 0, fmt.Errorf("build next target: %w", err)
		}

		runtimeRoot = stage
	default:
		return "", 0, fmt.Errorf("unknown inspection target %q", target)
	}

	switch kind {
	case protocol.InspectGraph:
		dot, err := agentlint.AgentGraphDOT(runtimeRoot, cfg)
		if err != nil {
			return "", 0, fmt.Errorf("draw agent graph: %w", err)
		}

		return dot, 0, nil
	case protocol.InspectLint:
		result, err := agentlint.Lint(runtimeRoot, cfg)
		if err != nil {
			return "", 0, fmt.Errorf("lint agents: %w", err)
		}

		if len(result.Findings) == 0 {
			return fmt.Sprintf("rocketclaw lint %s: OK\n", target), 0, nil
		}

		lines := []string{fmt.Sprintf("rocketclaw lint %s: found %d findings", target, len(result.Findings))}
		for _, finding := range result.Findings {
			lines = append(lines, fmt.Sprintf("%s %s %s: %s", finding.Code, finding.Severity, finding.Path, finding.Message))
		}

		return strings.Join(lines, "\n") + "\n", len(result.Findings), nil
	case protocol.InspectSettings:
		behavior := cfg.Behavior()
		behavior.RedactUsers()

		document, err := json.MarshalIndent(behavior, "", "  ")
		if err != nil {
			return "", 0, fmt.Errorf("encode behavior settings: %w", err)
		}

		return string(document) + "\n", 0, nil
	}

	return "", 0, fmt.Errorf("unknown inspection kind %q", kind)
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
