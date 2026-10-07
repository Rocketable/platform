package protocol

import (
	"context"
	"errors"
	"strings"
)

// ErrGoalAlreadyActive reports that a conversation already has an active goal.
var ErrGoalAlreadyActive = errors.New("goal already active")

// ErrBridgeStopped reports work left for the next start: it is the cause of
// shutdown cancellation and the error of requests a stopped bridge refuses.
var ErrBridgeStopped = errors.New("bridge stopped")

// Conversation is an explicitly recorded conversation and its selected agent.
// IDs are opaque to the Backend; frontends resolve presentation and policy.
type Conversation struct {
	ID, Agent, CreatedBy string
}

// BackgroundJob is a Background Job a conversation lists: running, or finished with its
// Completion Note still pending. Hidden marks a job of a hidden cron or External MCP run whose
// destination is the conversation; StoppedBy is "user" or "agent" for a stopped job. Note is
// the job's Completion Note text, set only where a transcript shows the note.
type BackgroundJob struct {
	ID, Kind, State, Label, ToolCallID, SubagentKey, StoppedBy, Note string
	Hidden                                                           bool
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

// SkillDescription describes an available skill for human discovery.
type SkillDescription struct {
	Name, Description string
}

// PrimaryTextRouter routes primary text connector conversations.
type PrimaryTextRouter interface {
	StartThread(ctx context.Context, agent string, target TextConversationTarget, inbound *InboundMessage) error
	StartGoalInThread(ctx context.Context, agent, objective, checkScript string, maxTurns int, target TextConversationTarget, inbound *InboundMessage) error
	SkillDescriptions(agent string) ([]SkillDescription, error)
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
