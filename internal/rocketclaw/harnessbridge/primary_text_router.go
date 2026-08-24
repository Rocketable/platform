package harnessbridge

import (
	"context"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/Rocketable/platform/internal/rocketcode"
)

// ThreadTurnPhase is whether a managed thread is still in the tool loop.
type ThreadTurnPhase int

const (
	// ThreadTurnUnclassified means Slack should treat a mid-turn send as a pending steer.
	ThreadTurnUnclassified ThreadTurnPhase = iota
	// ThreadTurnToolLoop means a mid-turn send is a Slack Steer.
	ThreadTurnToolLoop
	// ThreadTurnFinalAnswer means a mid-turn send is too late and becomes an Enqueued Slack Message.
	ThreadTurnFinalAnswer
)

// ThreadTurnPhaseFrom maps a RocketCode looper phase onto Slack's turn phase.
func ThreadTurnPhaseFrom(phase rocketcode.TurnPhase) ThreadTurnPhase {
	switch phase {
	case rocketcode.TurnPhaseToolLoop:
		return ThreadTurnToolLoop
	case rocketcode.TurnPhaseFinalAnswer:
		return ThreadTurnFinalAnswer
	case rocketcode.TurnPhaseUnclassified:
		return ThreadTurnUnclassified
	default:
		return ThreadTurnUnclassified
	}
}

// PrimaryTextRouter routes primary text connector conversations to app-owned bridges.
type PrimaryTextRouter interface {
	StartThread(ctx context.Context, agent string, target events.TextConversationTarget, inbound *events.InboundMessage) error
	StartGoalInThread(ctx context.Context, agent, objective, checkScript string, maxTurns int, target events.TextConversationTarget, inbound *events.InboundMessage) error
	StartWorkflowInThread(ctx context.Context, agent, name, args string, target events.TextConversationTarget, inbound *events.InboundMessage) error
	ReserveWorkflowTurn(target events.TextConversationTarget) (release func(), reserved bool, err error)
	WorkflowDescriptions() ([]workflow.Description, error)
	InterruptConversation(conversationID string) *events.InboundMessage
	InterruptThread(target events.TextConversationTarget) (*events.InboundMessage, error)
	RegisterThread(target events.TextConversationTarget, agent string) (created bool, err error)
	RegisterCronThread(ctx context.Context, target events.TextConversationTarget, agent string) error
	ThreadAgent(target events.TextConversationTarget) (agent string, handled bool, err error)
	SwitchThreadAgent(target events.TextConversationTarget, agent string) (bool, error)
	SubmitThreadReply(ctx context.Context, target events.TextConversationTarget, inbound *events.InboundMessage) (bool, error)
	SubmitWhenActive(ctx context.Context, target events.TextConversationTarget, inbound *events.InboundMessage, activation ActivationHook) (bool, error)
	StashThreadQueueItem(ctx context.Context, target events.TextConversationTarget, item *ThreadQueueItem) error
	ThreadQueueItems(ctx context.Context, target events.TextConversationTarget) ([]ThreadQueueItem, error)
	ReorderThreadQueue(ctx context.Context, target events.TextConversationTarget, ids []string) error
	DeleteThreadQueueItem(ctx context.Context, target events.TextConversationTarget, id string) error
	ScheduledMessages(ctx context.Context, target events.TextConversationTarget) (map[string]ScheduledMessageState, error)
	DeleteScheduledMessage(ctx context.Context, target events.TextConversationTarget, id string) error
	ResetScheduledMessages(ctx context.Context, target events.TextConversationTarget) error
	TurnPhase(target events.TextConversationTarget) ThreadTurnPhase
}
