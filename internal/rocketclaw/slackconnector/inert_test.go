package slackconnector

import (
	"context"
	"errors"

	"github.com/Rocketable/platform/internal/rocketclaw/cronjob"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
)

type inertThreadRouter struct{}

func (inertThreadRouter) StartThread(_ context.Context, _ string, _ events.TextConversationTarget, _ *events.InboundMessage) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) StartGoalInThread(_ context.Context, _, _, _ string, _ int, _ events.TextConversationTarget, _ *events.InboundMessage) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) StartWorkflowInThread(context.Context, string, string, string, events.TextConversationTarget, *events.InboundMessage) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) WorkflowDescriptions() ([]workflow.Description, error) {
	return nil, errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) ReserveWorkflowTurn(events.TextConversationTarget) (release func(), reserved bool, err error) {
	return func() {}, true, nil
}
func (inertThreadRouter) InterruptThread(target events.TextConversationTarget) (*events.InboundMessage, error) {
	_ = target
	return nil, nil
}
func (inertThreadRouter) InterruptConversation(string) *events.InboundMessage {
	return nil
}
func (inertThreadRouter) RegisterCronThread(_ context.Context, _ events.TextConversationTarget, _ string) error {
	return nil
}
func (inertThreadRouter) RegisterThread(_ events.TextConversationTarget, _ string) (bool, error) {
	return true, nil
}

func (inertThreadRouter) SwitchThreadAgent(_ events.TextConversationTarget, _ string) (bool, error) {
	return false, nil
}
func (inertThreadRouter) ThreadAgent(_ events.TextConversationTarget) (agent string, handled bool, err error) {
	return "", false, nil
}
func (inertThreadRouter) SubmitThreadReply(_ context.Context, _ events.TextConversationTarget, _ *events.InboundMessage) (bool, error) {
	return false, nil
}
func (inertThreadRouter) SubmitWhenActive(_ context.Context, _ events.TextConversationTarget, _ *events.InboundMessage, _ harnessbridge.ActivationHook) (bool, error) {
	return false, nil
}
func (inertThreadRouter) StashThreadQueueItem(context.Context, events.TextConversationTarget, *harnessbridge.ThreadQueueItem) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) ThreadQueueItems(context.Context, events.TextConversationTarget) ([]harnessbridge.ThreadQueueItem, error) {
	return nil, errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) ReorderThreadQueue(context.Context, events.TextConversationTarget, []string) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) DeleteThreadQueueItem(context.Context, events.TextConversationTarget, string) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) ScheduledMessages(context.Context, events.TextConversationTarget) (map[string]harnessbridge.ScheduledMessageState, error) {
	return nil, errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) DeleteScheduledMessage(context.Context, events.TextConversationTarget, string) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) ResetScheduledMessages(context.Context, events.TextConversationTarget) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) TurnPhase(events.TextConversationTarget) harnessbridge.ThreadTurnPhase {
	return harnessbridge.ThreadTurnUnclassified
}

type inertOneOffCronjobs struct{}

func (inertOneOffCronjobs) LoadOneOffCronjob(string) (cronjob.OneOffCronjob, error) {
	return cronjob.OneOffCronjob{}, errors.New("on-demand cronjobs are not configured")
}

func (inertOneOffCronjobs) RunOneOffCronjob(ctx context.Context, _ cronjob.OneOffCronjob, _ *harnessbridge.RawRunProgress, finish func(context.Context, cronjob.RunResult, error)) {
	finish(ctx, cronjob.RunResult{}, errors.New("on-demand cronjobs are not configured"))
}
