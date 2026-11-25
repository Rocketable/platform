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
	clone := *message
	clone.ReasoningEffort = Clone(message.ReasoningEffort)
	clone.SlackReply = Clone(message.SlackReply)
	clone.Attachments = CloneOutboundAttachments(message.Attachments)
	clone.Routine = Clone(message.Routine)

	return &clone
}
