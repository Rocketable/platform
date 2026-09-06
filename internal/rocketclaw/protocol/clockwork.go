// Package protocol defines shared RocketClaw event contracts.
package protocol

import (
	"context"
)

// OutboundPublisher sends one outbound message into connector delivery.
type OutboundPublisher interface {
	PublishOutbound(context.Context, *OutboundMessage) error
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}

	clone := *p

	return &clone
}

// CloneOutboundMessage returns a deep copy suitable for an independent connector delivery.
func CloneOutboundMessage(message *OutboundMessage) *OutboundMessage {
	return &OutboundMessage{
		Text: message.Text, ProgressText: message.ProgressText,
		ConversationID: message.ConversationID, TurnID: message.TurnID,
		ExternalConversationID: message.ExternalConversationID, Agent: message.Agent,
		Complete:   message.Complete,
		SlackReply: clonePtr(message.SlackReply), Attachments: CloneOutboundAttachments(message.Attachments),
		GoalTurn: message.GoalTurn, GoalComplete: message.GoalComplete, GoalActive: message.GoalActive,
		GoalTurnNumber: message.GoalTurnNumber, GoalMaxTurns: message.GoalMaxTurns, WorkflowTerminal: message.WorkflowTerminal,
		Cronjob: clonePtr(message.Cronjob), WorkflowAgent: clonePtr(message.WorkflowAgent), WorkflowPhase: clonePtr(message.WorkflowPhase),
	}
}
