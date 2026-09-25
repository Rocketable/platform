// Package protocol defines shared RocketClaw event contracts.
package protocol

import (
	"context"
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
		ConsumedID: message.ConsumedID, ConsumedText: message.ConsumedText,
		Text: message.Text, ProgressText: message.ProgressText,
		ConversationID: message.ConversationID, TurnID: message.TurnID,
		ExternalConversationID: message.ExternalConversationID, Agent: message.Agent,
		Model: message.Model, SourceConversationID: message.SourceConversationID, ReasoningEffort: Clone(message.ReasoningEffort),
		Complete:   message.Complete,
		SlackReply: Clone(message.SlackReply), Attachments: CloneOutboundAttachments(message.Attachments),
		GoalTurn: message.GoalTurn, GoalComplete: message.GoalComplete, GoalActive: message.GoalActive,
		GoalTurnNumber: message.GoalTurnNumber, GoalMaxTurns: message.GoalMaxTurns, WorkflowTerminal: message.WorkflowTerminal,
		Cronjob: Clone(message.Cronjob), WorkflowAgent: Clone(message.WorkflowAgent), WorkflowPhase: Clone(message.WorkflowPhase),
	}
}
