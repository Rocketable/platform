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

// PrimaryTextRouter routes primary text connector conversations.
type PrimaryTextRouter interface {
	// MentionThread reports whether a conversation records the thread and whether it is a
	// read-only report thread: a cron report or an External MCP thread.
	MentionThread(target TextConversationTarget) (recorded, report bool, err error)
	// SubmitMention records the thread's conversation with agent when absent and queues inbound
	// as its next turn, at most once per mention; it reports false for a mention already accepted,
	// and true with an error when the mention was queued but its pickup failed.
	SubmitMention(ctx context.Context, agent string, target TextConversationTarget, inbound *InboundMessage) (bool, error)
	// InterruptThread stops the thread's goal and interrupts its active turn without waiting for
	// a running turn to end.
	InterruptThread(target TextConversationTarget) (*InboundMessage, error)
	// WebURL links to the conversation in the Web Interface.
	WebURL(ctx context.Context, conversationID string) (string, error)
}
