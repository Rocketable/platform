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
	Run                         *RoutineRun
}

type RoutineRun struct {
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

func SlackThreadSessionID(channelID, threadTS, agent string) string {
	return SlackThreadConversationID(channelID, threadTS) + ":" + agent
}

type AmbientRecord struct {
	Author  string `json:"author"`
	IsAgent bool   `json:"is_agent"`
	Allowed bool   `json:"allowed"`
	Text    string `json:"text"`
}

type AmbientScreen struct {
	WakeWhen          string          `json:"-"`
	IncompleteContext bool            `json:"-"`
	Earlier           []AmbientRecord `json:"earlier_messages"`
	Message           AmbientRecord   `json:"message"`
}

type PrimaryTextRouter interface {
	ScreenAmbient(ctx context.Context, target TextConversationTarget, screen *AmbientScreen) (pass bool)
	MentionThread(target TextConversationTarget) (recorded bool, err error)
	SubmitMention(ctx context.Context, agent string, target TextConversationTarget, inbound *InboundMessage) (bool, error)
	InterruptTurn(ctx context.Context, turnID string) error
	WebURL(conversationID string) (string, error)
}
