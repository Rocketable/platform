package protocol

import (
	"context"
	"errors"
	"strings"
)

// ErrGoalAlreadyActive reports that a conversation already has an active goal.
var ErrGoalAlreadyActive = errors.New("goal already active")

// Conversation is an explicitly recorded conversation and its selected agent.
// IDs are opaque to the Backend; frontends resolve presentation and policy.
type Conversation struct {
	ID, Agent, CreatedBy string
	Settled              bool
}

// PendingSteer is one uninjected Slack Steer copied onto the active-turn row.
type PendingSteer struct {
	Text, Principal, SlackChannel, SlackTS, SlackThreadTS string
}

// PendingSteersSink copies pending Slack Steers onto the active-turn row.
// The zero value is inert.
type PendingSteersSink struct {
	Set func(conversationID string, steers []PendingSteer) error
}

// Persist copies steers onto the active-turn row, or does nothing when unset.
func (s PendingSteersSink) Persist(conversationID string, steers []PendingSteer) {
	if s.Set == nil {
		return
	}

	_ = s.Set(conversationID, steers)
}

// SlackThreadConversationID returns the stable conversation ID for a Slack thread.
func SlackThreadConversationID(channelID, threadTS string) string {
	channelID, threadTS = strings.TrimSpace(channelID), strings.TrimSpace(threadTS)
	if channelID == "" || threadTS == "" {
		return ""
	}

	return "slack-thread:" + channelID + ":" + threadTS
}

// SlackThreadTarget returns the Slack channel and thread timestamp for a Slack thread conversation ID.
func SlackThreadTarget(conversationID string) (channelID, threadTS string, ok bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(conversationID), "slack-thread:")
	if !ok {
		return "", "", false
	}

	channelID, threadTS, ok = strings.Cut(rest, ":")
	channelID, threadTS = strings.TrimSpace(channelID), strings.TrimSpace(threadTS)

	return channelID, threadTS, ok && channelID != "" && threadTS != ""
}

// PrimaryTextRouter routes primary text connector conversations.
type PrimaryTextRouter interface {
	StartThread(ctx context.Context, agent string, target TextConversationTarget, inbound *InboundMessage) error
	StartGoalInThread(ctx context.Context, agent, objective, checkScript string, maxTurns int, target TextConversationTarget, inbound *InboundMessage) error
	StartWorkflowInThread(ctx context.Context, agent, name, args string, target TextConversationTarget, inbound *InboundMessage) error
	ReserveWorkflowTurn(target TextConversationTarget) (release func(), reserved bool, err error)
	WorkflowDescriptions() ([]WorkflowDescription, error)
	InterruptConversation(conversationID string) *InboundMessage
	InterruptThread(target TextConversationTarget) (*InboundMessage, error)
	RegisterThread(target TextConversationTarget, agent string) (created bool, err error)
	ThreadAgent(target TextConversationTarget) (agent string, handled bool, err error)
	SwitchThreadAgent(target TextConversationTarget, agent string) (bool, error)
	SubmitThreadReply(ctx context.Context, target TextConversationTarget, inbound *InboundMessage) (bool, error)
	StashThreadQueueItem(ctx context.Context, target TextConversationTarget, item *ThreadQueueItem) error
	ThreadQueueItems(target TextConversationTarget) ([]ThreadQueueItem, error)
	DeleteThreadQueueItem(ctx context.Context, target TextConversationTarget, id string) (bool, error)
	PromoteThreadQueueItem(ctx context.Context, target TextConversationTarget, id string) (bool, error)
	ScheduledMessages(target TextConversationTarget) (map[string]ScheduledMessageState, error)
	ThreadBusy(target TextConversationTarget) bool
}
