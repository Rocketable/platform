package harnessbridge

import (
	"context"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
)

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
}
