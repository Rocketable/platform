package slackconnector

import (
	"context"
	"errors"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type inertConversationBackend struct{}

func (inertConversationBackend) Subscribe(context.Context) <-chan protocol.ConversationEvent {
	ch := make(chan protocol.ConversationEvent)
	close(ch)

	return ch
}

func (inertConversationBackend) CreateConversation(string, []string, []protocol.ConversationTag) error {
	return protocol.ErrUnknownConversation
}

func (inertConversationBackend) RunTurn(context.Context, *protocol.TurnRequest) error {
	return protocol.ErrUnknownConversation
}

func (inertConversationBackend) SyncConversation(context.Context, string, string) error {
	return protocol.ErrUnknownConversation
}

func (inertConversationBackend) ListConversations() ([]protocol.ConversationRecord, error) {
	return nil, nil
}

func (inertConversationBackend) ConversationAgent(string) (string, error) {
	return "", protocol.ErrUnknownConversation
}

func (inertConversationBackend) SwitchAgent(string, string) error {
	return protocol.ErrUnknownConversation
}

func (inertConversationBackend) ListLaterWork(context.Context, string) ([]protocol.ThreadQueueItem, error) {
	return nil, nil
}

func (inertConversationBackend) DeleteLaterWork(context.Context, string, string) error {
	return nil
}

func (inertConversationBackend) ReorderLaterWork(context.Context, string, []string) error {
	return nil
}

func (inertConversationBackend) ConversationBusy(string) bool {
	return false
}

func (inertConversationBackend) ScheduledMessages(string) (map[string]protocol.ScheduledMessageState, error) {
	return nil, errors.New("scheduled messages are not configured")
}

func (inertConversationBackend) WorkflowDescriptions() ([]protocol.WorkflowDescription, error) {
	return nil, errors.New("workflows are not configured")
}

type inertOneOffCronjobs struct{}

func (inertOneOffCronjobs) LoadOneOffCronjob(string) (protocol.OneOffCronjob, error) {
	return protocol.OneOffCronjob{}, errors.New("on-demand cronjobs are not configured")
}

func (inertOneOffCronjobs) ListCronjobs(string) ([]string, error) {
	return nil, errors.New("on-demand cronjobs are not configured")
}

func (inertOneOffCronjobs) RunOneOffCronjob(ctx context.Context, _ *protocol.OneOffCronjob, _ *protocol.CronProgress, finish func(context.Context, protocol.CronRunResult, error)) {
	finish(ctx, protocol.CronRunResult{}, errors.New("on-demand cronjobs are not configured"))
}

type inertSideAskRunner struct{}

func (inertSideAskRunner) RunSideAsk(context.Context, *sideAskRequest) {}

type inertSideAskHost struct{}

func (inertSideAskHost) Run(context.Context, protocol.SideAskRequest) error { return nil }
