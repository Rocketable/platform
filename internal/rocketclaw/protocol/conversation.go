package protocol

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrGoalAlreadyActive = errors.New("goal already active")

var ErrBridgeStopped = errors.New("bridge stopped")

type Conversation struct {
	ID, Agent, CreatedBy, Space string
	Run                         *CronRun
}

type CronRun struct {
	Path   string
	At     time.Time
	OneOff bool
}

type ShadowBinding struct {
	ParentConversationID, ParentAgent, ShadowAgent, ChildConversationID string
	Turns                                                               []ShadowTurn
}

type ShadowTurn struct {
	ParentSourceConversationID, ParentTurnID, ChildTurnID string
	Order                                                 int64
	Running                                               bool
	Terminal                                              Terminal
	Revision                                              string
}

type BackgroundJob struct {
	ID, Kind, State, Label, ToolCallID, SubagentKey, StoppedBy, Note string
	Hidden                                                           bool
}

func SlackThreadConversationID(channelID, threadTS string) string {
	channelID, threadTS = strings.TrimSpace(channelID), strings.TrimSpace(threadTS)
	if channelID == "" || threadTS == "" {
		return ""
	}

	return "slack-thread:" + channelID + ":" + threadTS
}

type PrimaryTextRouter interface {
	MentionThread(target TextConversationTarget) (recorded, report bool, err error)
	SubmitMention(ctx context.Context, agent string, target TextConversationTarget, inbound *InboundMessage) (bool, error)
	InterruptThread(target TextConversationTarget) (*InboundMessage, error)
	WebURL(ctx context.Context, conversationID string) (string, error)
}
