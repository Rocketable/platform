// Package frontend is the conversation Backend Frontends compose.
package frontend

import (
	"context"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

// Backend is the conversation building-block surface Frontends compose.
type Backend interface {
	Subscribe(ctx context.Context) <-chan protocol.ConversationEvent
	CreateConversation(id string, agents []string, tags []protocol.ConversationTag) error
	RunTurn(ctx context.Context, req *protocol.TurnRequest) error
	SyncConversation(ctx context.Context, src, dst string) error
	ListConversations() ([]protocol.ConversationRecord, error)
	ConversationAgent(id string) (string, error)
	SwitchAgent(id, agent string) error
	ListLaterWork(ctx context.Context, id string) ([]protocol.ThreadQueueItem, error)
	DeleteLaterWork(ctx context.Context, id, itemID string) error
	ReorderLaterWork(ctx context.Context, id string, itemIDs []string) error
	ConversationBusy(id string) bool
	ScheduledMessages(id string) (map[string]protocol.ScheduledMessageState, error)
	WorkflowDescriptions() ([]protocol.WorkflowDescription, error)
}
