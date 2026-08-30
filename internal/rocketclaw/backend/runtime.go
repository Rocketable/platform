package backend

import (
	"context"
	"log/slog"
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
	ConfigPath               string
	Log                      *slog.Logger
	RunCtx                   context.Context
	Channels                 protocol.Channels
	Sessions                 *SessionService
	Cron                     *Manager
	OverlayMu                *sync.Mutex
	Reload                   func(context.Context, string) (string, error)
	Restart                  func(context.Context, string) (string, error)
	RecoveredTurns           []ActiveTurnState
	CannotResume             []cannotResumeItem
	ExternalMCPUsers         map[string]string
	RefreshExternalMCPAgents *func() error

	TextRouter      protocol.PrimaryTextRouter
	threads         *threadBridgeManager
	startThreadRoot *func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error)
	slackAsker      *protocol.UserQuestionAsker
	drainSlack      *func(context.Context, string, rocketcode.TurnPhase) []string
}

// AttachSlack hooks originator Slack methods into backend thread state.
func (r *Runtime) AttachSlack(slack SlackFrontend) {
	r.threads.output = slack.SendResponse
	r.threads.abort = slack.AbortResponse
	r.threads.root = slack.StartNewThreadRoot
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
