package slackconnector

import (
	"context"
	"errors"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type inertThreadRouter struct{}

func (inertThreadRouter) ScreenAmbient(context.Context, protocol.TextConversationTarget, *protocol.AmbientScreen) bool {
	return false
}
func (inertThreadRouter) MentionThread(protocol.TextConversationTarget) (recorded bool, err error) {
	return false, nil
}
func (inertThreadRouter) SubmitMention(context.Context, string, protocol.TextConversationTarget, *protocol.InboundMessage) (bool, error) {
	return false, errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) InterruptTurn(context.Context, string) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) WebURL(string) (string, error) {
	return "", errors.New("web links are not configured")
}
