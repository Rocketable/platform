// Package frontend defines the conversation operations consumed by frontends.
package frontend

import (
	"context"
	"iter"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

// Backend owns conversation execution, recording, ordering, and live output.
type Backend interface {
	Subscribe(context.Context) iter.Seq[protocol.Event]
	CreateConversation(context.Context, protocol.Conversation) error
	ListConversations(context.Context) ([]protocol.Conversation, error)
	SwitchConversationAgent(string, string) (bool, error)
	RunTurn(context.Context, *protocol.InboundMessage) error
	SyncConversation(context.Context, string, string) error
	QueueItems(context.Context, string) ([]protocol.ThreadQueueItem, error)
	PromoteQueueItem(context.Context, string, string) (bool, error)
	DeleteQueueItem(context.Context, string, string) (bool, error)
	ReorderQueueItems(context.Context, string, []string) error
	StashQueueItem(context.Context, string, *protocol.ThreadQueueItem) error
}
