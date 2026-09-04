package backend

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"maps"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
)

// SlackFrontend is the Slack surface cmd constructs.
type SlackFrontend interface {
	HandleBroadcast(context.Context, *protocol.Broadcast) protocol.BroadcastAcknowledgement
	Start(context.Context) error
	Stop(context.Context) error
	SendResponse(context.Context, *protocol.OutboundMessage) error
	AbortResponse(*protocol.OutboundMessage)
	StartNewThreadRoot(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error)
	AskUserQuestion(context.Context, *protocol.AskUserQuestionRequest) (protocol.AskUserQuestionAnswer, error)
	DrainSteers(context.Context, string) []string
	ActivateEnqueue(context.Context, *protocol.ThreadQueueItem, *protocol.InboundMessage) error
	SetPendingSteersSink(protocol.PendingSteersSink)
	RestorePendingSteers(string, []protocol.PendingSteer)
	DiscardPendingSteers(context.Context, []protocol.PendingSteer)
}

// Runtime is the backend after construction, before frontends.
type Runtime struct {
	Cfg                      *config.Config
	Log                      *slog.Logger
	RunCtx                   context.Context
	Channels                 protocol.Channels
	Sessions                 *SessionService
	RecoveredTurns           []ActiveTurnState
	CannotResume             []cannotResumeItem
	ExternalMCPUsers         map[string]string
	RefreshExternalMCPAgents *func() error

	TextRouter      protocol.PrimaryTextRouter
	threads         *threadBridgeManager
	startThreadRoot *func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error)
	slackAsker      *protocol.UserQuestionAsker
	drainSlack      *func(context.Context, string, rocketcode.TurnPhase) []string

	eventsMu    sync.Mutex
	subscribers map[chan protocol.Event]<-chan struct{}
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

	message.MarkDelivered(errDelivery)

	return errDelivery
}

// AttachSlack hooks originator Slack methods into backend thread state.
func (r *Runtime) AttachSlack(slack SlackFrontend) {
	*r.slackAsker = protocol.InteractiveUserQuestionAsker(slack.AskUserQuestion)
	*r.drainSlack = func(ctx context.Context, conversationID string, _ rocketcode.TurnPhase) []string {
		return slack.DrainSteers(ctx, conversationID)
	}
	*r.startThreadRoot = slack.StartNewThreadRoot
}

// SubmitExternalMCP submits one External MCP inbound.
func (r *Runtime) SubmitExternalMCP(ctx context.Context, agent, conversationID string, inbound *protocol.InboundMessage, activation protocol.ActivationHook) error {
	return r.threads.SubmitExternalMCP(ctx, agent, conversationID, inbound, activation)
}
