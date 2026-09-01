package slackconnector

import (
	"context"
	"errors"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type inertThreadRouter struct{}

func (inertThreadRouter) StartThread(_ context.Context, _ string, _ protocol.TextConversationTarget, _ *protocol.InboundMessage) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) StartGoalInThread(_ context.Context, _, _, _ string, _ int, _ protocol.TextConversationTarget, _ *protocol.InboundMessage) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) StartWorkflowInThread(context.Context, string, string, string, protocol.TextConversationTarget, *protocol.InboundMessage) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) WorkflowDescriptions() ([]protocol.WorkflowDescription, error) {
	return nil, errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) ReserveWorkflowTurn(protocol.TextConversationTarget) (release func(), reserved bool, err error) {
	return func() {}, true, nil
}
func (inertThreadRouter) InterruptThread(target protocol.TextConversationTarget) (*protocol.InboundMessage, error) {
	_ = target
	return nil, nil
}
func (inertThreadRouter) InterruptConversation(string) *protocol.InboundMessage {
	return nil
}
func (inertThreadRouter) RegisterCronThread(_ context.Context, _ protocol.TextConversationTarget, _ string) error {
	return nil
}
func (inertThreadRouter) RegisterThread(_ protocol.TextConversationTarget, _ string) (bool, error) {
	return true, nil
}

func (inertThreadRouter) SwitchThreadAgent(_ protocol.TextConversationTarget, _ string) (bool, error) {
	return false, nil
}
func (inertThreadRouter) ThreadAgent(_ protocol.TextConversationTarget) (agent string, handled bool, err error) {
	return "", false, nil
}
func (inertThreadRouter) SubmitThreadReply(_ context.Context, _ protocol.TextConversationTarget, _ *protocol.InboundMessage) (bool, error) {
	return false, nil
}
func (inertThreadRouter) SubmitWhenActive(_ context.Context, _ protocol.TextConversationTarget, _ *protocol.InboundMessage, _ protocol.ActivationHook) (bool, error) {
	return false, nil
}
func (inertThreadRouter) StashThreadQueueItem(context.Context, protocol.TextConversationTarget, *protocol.ThreadQueueItem) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) ThreadQueueItems(context.Context, protocol.TextConversationTarget) ([]protocol.ThreadQueueItem, error) {
	return nil, errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) DeleteThreadQueueItem(context.Context, protocol.TextConversationTarget, string) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) ScheduledMessages(context.Context, protocol.TextConversationTarget) (map[string]protocol.ScheduledMessageState, error) {
	return nil, errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) ThreadBusy(protocol.TextConversationTarget) bool {
	return false
}

type inertOneOffCronjobs struct{}

func (inertOneOffCronjobs) LoadOneOffCronjob(string) (protocol.OneOffCronjob, error) {
	return protocol.OneOffCronjob{}, errors.New("on-demand cronjobs are not configured")
}

func (inertOneOffCronjobs) ListCronjobs(string) ([]string, error) {
	return nil, errors.New("on-demand cronjobs are not configured")
}

func (inertOneOffCronjobs) RunOneOffCronjob(ctx context.Context, _ protocol.OneOffCronjob, _ *protocol.CronProgress, finish func(context.Context, protocol.CronRunResult, error)) {
	finish(ctx, protocol.CronRunResult{}, errors.New("on-demand cronjobs are not configured"))
}

type inertSideAskRunner struct{}

func (inertSideAskRunner) RunSideAsk(context.Context, *sideAskRequest) {}

type inertSideAskHost struct{}

func (inertSideAskHost) Run(context.Context, protocol.SideAskRequest) error { return nil }
