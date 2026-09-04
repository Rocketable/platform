package protocol

import (
	"context"
	"errors"
	"strings"
)

// ErrGoalAlreadyActive reports that a conversation already has an active goal.
var ErrGoalAlreadyActive = errors.New("goal already active")

// ErrUnknownConversation reports RunTurn or Sync on an id that was never created.
var ErrUnknownConversation = errors.New("unknown conversation")

// ConversationTag is an interpret-only conversation tag.
type ConversationTag string

const (
	// ConversationUserFacing marks a conversation Slack and Web should render.
	ConversationUserFacing ConversationTag = "user-facing"
	// ConversationCron marks Cron presentation.
	ConversationCron ConversationTag = "cron"
)

// TurnKind is one RunTurn request kind.
type TurnKind string

const (
	// TurnPrompt is a normal conversational prompt.
	TurnPrompt TurnKind = "prompt"
	// TurnSteer injects follow-up text.
	TurnSteer TurnKind = "steer"
	// TurnEnqueue stashes later work and returns after that turn finishes.
	TurnEnqueue TurnKind = "enqueue"
	// TurnCancel ends the active turn on a conversation.
	TurnCancel TurnKind = "cancel"
	// TurnGoal starts a goal loop.
	TurnGoal TurnKind = "goal"
	// TurnWorkflow starts a workflow.
	TurnWorkflow TurnKind = "workflow"
)

// TurnRequest is one blocking RunTurn.
type TurnRequest struct {
	ID, Text, Agent, Objective, CheckScript, Workflow, WorkflowArgs, UserFacingID string
	Kind                                                                          TurnKind
	MaxTurns                                                                      int
}

// ConversationRecord is one ListConversations row.
type ConversationRecord struct {
	ID     string
	Agents []string
	Tags   []ConversationTag
}

// ConversationEvent is one live Subscribe event. Missed events are not replayed.
type ConversationEvent struct {
	ConversationID, Text, Role string
	Complete                   bool
}

// ActivationHook runs when a text connector activates an idle conversation.
type ActivationHook func(context.Context, *InboundMessage) error

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

// SideAskRequest is one isolated Slack Side Ask turn.
type SideAskRequest struct {
	ConversationID    string
	SessionEntryID    int64
	Agent, Question   string
	Thinking, Message func(context.Context, string) error
}

// WebSessionConversationID returns the stable conversation ID for a web session.
func WebSessionConversationID(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	return "web-session:" + name
}

// WebSessionName returns the web session name for a web session conversation ID.
func WebSessionName(conversationID string) (name string, ok bool) {
	name, ok = strings.CutPrefix(strings.TrimSpace(conversationID), "web-session:")
	name = strings.TrimSpace(name)

	return name, ok && name != ""
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
