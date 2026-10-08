// Package protocol defines shared RocketClaw event contracts.
package protocol

import (
	"context"
	"slices"
)

// OutboundPublisher sends one outbound message into connector delivery.
type OutboundPublisher interface {
	PublishOutbound(context.Context, *OutboundMessage) error
}

// Clone returns a shallow copy of p, or nil when p is nil.
func Clone[T any](p *T) *T {
	if p == nil {
		return nil
	}

	clone := *p

	return &clone
}

// CloneOutboundMessage returns a deep copy suitable for an independent connector delivery.
func CloneOutboundMessage(message *OutboundMessage) *OutboundMessage {
	return &OutboundMessage{
		ConsumedID: message.ConsumedID, ConsumedText: message.ConsumedText, ConsumedRawText: message.ConsumedRawText, ConsumedSource: message.ConsumedSource, ConsumedHeader: message.ConsumedHeader,
		Text:           message.Text,
		ConversationID: message.ConversationID, TurnID: message.TurnID,
		ExternalConversationID: message.ExternalConversationID, Agent: message.Agent,
		Model: message.Model, SourceConversationID: message.SourceConversationID, ReasoningEffort: Clone(message.ReasoningEffort),
		Complete:   message.Complete,
		SlackReply: Clone(message.SlackReply), Attachments: CloneOutboundAttachments(message.Attachments),
		GoalTurn: message.GoalTurn, GoalComplete: message.GoalComplete, GoalActive: message.GoalActive,
		GoalTurnNumber: message.GoalTurnNumber, GoalMaxTurns: message.GoalMaxTurns, Source: message.Source, Terminal: message.Terminal,
		Cronjob: Clone(message.Cronjob), ReplyState: slices.Clone(message.ReplyState),
	}
}
