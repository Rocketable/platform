package slackconnector

import (
	"context"

	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

// StartEvents subscribes before starting live output consumption.
func (c *Connector) StartEvents(ctx context.Context, backend frontend.Backend) <-chan struct{} {
	events := backend.Subscribe(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)

		for event := range events {
			message := event.Message
			if message.ConversationID != "" {
				channelID, threadTS, ok := protocol.SlackThreadTarget(message.ConversationID)
				if !ok {
					event.Acknowledgement <- nil
					continue
				}

				if message.SlackReply == nil {
					message.SlackReply = &protocol.SlackReplyTarget{MessageTS: threadTS}
				}

				message.SlackReply.ChannelID, message.SlackReply.ThreadTS = channelID, threadTS
			}

			err := c.SendResponse(ctx, message)
			if err != nil && message.Complete && ctx.Err() == nil {
				c.AbortResponse(message)
			}

			event.Acknowledgement <- err
		}
	}()

	return done
}
