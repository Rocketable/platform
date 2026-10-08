package slackconnector

import (
	"context"
	"errors"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type inertThreadRouter struct{}

func (inertThreadRouter) MentionThread(protocol.TextConversationTarget) (recorded, report bool, err error) {
	return false, false, nil
}
func (inertThreadRouter) SubmitMention(context.Context, string, protocol.TextConversationTarget, *protocol.InboundMessage) (bool, error) {
	return false, errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) InterruptThread(protocol.TextConversationTarget) (*protocol.InboundMessage, error) {
	return nil, errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) WebURL(context.Context, string) (string, error) {
	return "", errors.New("web links are not configured")
}
