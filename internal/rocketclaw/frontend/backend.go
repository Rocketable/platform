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
	StageRevert(context.Context, string, string) (string, string, error)
	ClearRevert(context.Context, string) error
	SyncConversation(context.Context, string, string) error
	QueueItems(string) ([]protocol.ThreadQueueItem, error)
	PromoteQueueItem(context.Context, string, string) (bool, error)
	PopQueueItem(context.Context, string, string) (bool, error)
	DeleteQueueItem(context.Context, string, string) (bool, error)
	ReorderQueueItems(string, []string) error
	StashQueueItem(context.Context, string, *protocol.ThreadQueueItem) error
	WorkflowDescriptions() ([]protocol.WorkflowDescription, error)
	StartGoal(context.Context, *protocol.InboundMessage, protocol.GoalRequest) error
	MoveToBackground(string) (bool, error)
	StopBackgroundJob(context.Context, string, string) (bool, error)
	BackgroundJobs(context.Context, string) ([]protocol.BackgroundJob, bool, error)
	CompletionNotes(context.Context, string, []string) ([]protocol.BackgroundJob, error)
	PendingQuestions(context.Context, string) ([]protocol.AskUserQuestionRequest, error)
	AnswerQuestion(context.Context, string, string, protocol.AskUserQuestionAnswer) error
}
