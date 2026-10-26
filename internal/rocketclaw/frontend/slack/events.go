package slackconnector

import (
	"context"
	"fmt"
	"strings"

	"github.com/slack-go/slack"

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

			channelID, threadTS, err := c.facts.SlackThread(ctx, message.ConversationID)
			if err != nil {
				event.Acknowledgement <- err
				continue
			}

			if message.ConsumedID != "" {
				if threadTS != "" && message.ConsumedSource == protocol.SourceWeb && message.ConsumedText != "" {
					text := message.ConsumedRawText
					if strings.TrimSpace(text) == "" && message.ConsumedText != text {
						text = "(attachments)"
					}

					for _, chunk := range splitSlackText(text, slackBlockTextLimit, slackBlockTextLimit) {
						blocks := []slack.Block{
							slack.NewHeaderBlock(slack.NewTextBlockObject(slack.PlainTextType, "📡 web", false, false)),
							slack.NewDividerBlock(),
							slack.NewSectionBlock(slack.NewTextBlockObject(slack.PlainTextType, chunk, false, false), nil, nil),
						}

						_, _, err = c.api.PostMessageContext(ctx, channelID, slack.MsgOptionText("📡 web\n"+chunk, false), slack.MsgOptionTS(threadTS), slack.MsgOptionBlocks(blocks...), slack.MsgOptionDisableMarkdown(), slack.MsgOptionParse(false))
						if err != nil {
							err = fmt.Errorf("send Slack web input: %w", err)
							break
						}
					}

					event.Acknowledgement <- err
				} else {
					event.Acknowledgement <- nil
				}

				continue
			}

			switch {
			case message.ConversationID == "":
			case threadTS != "":
				if message.SlackReply == nil {
					message.SlackReply = &protocol.SlackReplyTarget{MessageTS: threadTS}
				}

				message.SlackReply.ChannelID, message.SlackReply.ThreadTS = channelID, threadTS
			default:
				event.Acknowledgement <- nil
				continue
			}

			err = c.SendResponse(ctx, message)
			if err != nil && message.Complete && ctx.Err() == nil {
				c.AbortResponse(message)
			}

			event.Acknowledgement <- err
		}
	}()

	return done
}
