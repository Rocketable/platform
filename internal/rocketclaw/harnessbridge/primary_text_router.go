package harnessbridge

import (
	"context"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
)

// PrimaryTextRouter routes primary text connector conversations to app-owned bridges.
type PrimaryTextRouter interface {
	StartThread(ctx context.Context, agent string, preSeed bool, target events.TextConversationTarget, inbound *events.InboundMessage) error
	StartGoalInThread(ctx context.Context, agent, objective, checkScript string, maxTurns int, target events.TextConversationTarget, inbound *events.InboundMessage) error
	InterruptThread(target events.TextConversationTarget) (*events.InboundMessage, error)
	RegisterCronThread(ctx context.Context, target events.TextConversationTarget, agent, seedText string) error
	PrepareThreadReply(target events.TextConversationTarget) (bool, error)
	PrepareResponseThreadReply(target events.TextConversationTarget) (bool, error)
	SubmitThreadReply(ctx context.Context, target events.TextConversationTarget, inbound *events.InboundMessage) (bool, error)
	SubmitResponseThreadReply(ctx context.Context, target events.TextConversationTarget, inbound *events.InboundMessage) (bool, error)
	SummarizeThread(ctx context.Context, target events.TextConversationTarget) (bool, error)
	RecordResponseCheckpoint(target events.TextConversationTarget, checkpoint events.ResponseCheckpoint) error
}
