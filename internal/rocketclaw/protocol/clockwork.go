package protocol

import "context"

type OutboundPublisher interface {
	PublishOutbound(context.Context, *OutboundMessage) error
}

func Clone[T any](p *T) *T {
	if p == nil {
		return nil
	}

	return new(*p)
}

func CloneOutboundMessage(message *OutboundMessage) *OutboundMessage {
	return &OutboundMessage{
		ConsumedID: message.ConsumedID, ConsumedText: message.ConsumedText, ConsumedRawText: message.ConsumedRawText, ConsumedSource: message.ConsumedSource, ConsumedHeader: message.ConsumedHeader,
		Text:           message.Text,
		ConversationID: message.ConversationID, TurnID: message.TurnID,
		Agent: message.Agent,
		Model: message.Model, SourceConversationID: message.SourceConversationID, ReasoningEffort: Clone(message.ReasoningEffort),
		Complete:   message.Complete,
		SlackReply: Clone(message.SlackReply), Attachments: CloneOutboundAttachments(message.Attachments),
		GoalTurn: message.GoalTurn, GoalComplete: message.GoalComplete, GoalActive: message.GoalActive,
		GoalTurnNumber: message.GoalTurnNumber, GoalMaxTurns: message.GoalMaxTurns, Terminal: message.Terminal,
		Cronjob: Clone(message.Cronjob),
	}
}
