package protocol

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ErrGoalAlreadyActive reports that a conversation already has an active goal.
var ErrGoalAlreadyActive = errors.New("goal already active")

// ErrBridgeStopped reports work left for the next start: it is the cause of
// shutdown cancellation and the error of requests a stopped bridge refuses.
var ErrBridgeStopped = errors.New("bridge stopped")

// Conversation is an explicitly recorded conversation and its selected agent.
// IDs are opaque to the Backend; frontends resolve presentation and policy.
// Space names the Web space it is born in, created when missing; empty for none.
// Run is set only for a cron run, which is created hidden; any other is a Web chat.
type Conversation struct {
	ID, Agent, CreatedBy, Space string
	Run                         *CronRun
}

// CronRun locates a cron run: its definition's path, start time, and whether it ran once from Web.
type CronRun struct {
	Path   string
	At     time.Time
	OneOff bool
}

// ShadowBinding identifies a parent's independent comparison session. Turns
// contain only correlations in the requested parent history page, not seed history.
type ShadowBinding struct {
	ParentConversationID, ParentAgent, ShadowAgent, ChildConversationID string
	Turns                                                               []ShadowTurn
}

// ShadowTurn pairs one outer turn by source and identity, never prompt text.
// An empty ChildTurnID and Terminal means waiting. A completed turn without a
// reply is silent; its position is retained regardless of transcript content.
type ShadowTurn struct {
	ParentSourceConversationID, ParentTurnID, ChildTurnID string
	Order                                                 int64
	Running                                               bool
	Terminal                                              Terminal
	Revision                                              string
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
