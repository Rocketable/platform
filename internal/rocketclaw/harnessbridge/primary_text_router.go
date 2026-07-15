package harnessbridge

import (
	"context"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
)

// PrimaryTextRouter routes primary text connector conversations to app-owned bridges.
type PrimaryTextRouter interface {
	StartThread(ctx context.Context, agent string, target events.TextConversationTarget, inbound *events.InboundMessage) error
	StartGoalInThread(ctx context.Context, agent, objective, checkScript string, maxTurns int, target events.TextConversationTarget, inbound *events.InboundMessage) error
	InterruptThread(target events.TextConversationTarget) (*events.InboundMessage, error)
	RegisterCronThread(ctx context.Context, target events.TextConversationTarget, agent string) error
	ThreadAgent(target events.TextConversationTarget) (agent string, handled bool, err error)
	SwitchThreadAgent(target events.TextConversationTarget, agent string) (bool, error)
	SubmitThreadReply(ctx context.Context, target events.TextConversationTarget, inbound *events.InboundMessage) (bool, error)
}
