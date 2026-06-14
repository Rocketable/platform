package slackconnector

import (
	"context"
	"errors"

	"github.com/Rocketable/platform/internal/rocketclaw/cronjob"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
)

type inertThreadRouter struct{}

func (inertThreadRouter) StartThread(_ context.Context, _ string, _ bool, _ events.TextConversationTarget, _ *events.InboundMessage) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) StartGoalInThread(_ context.Context, _, _, _ string, _ int, _ events.TextConversationTarget, _ *events.InboundMessage) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) InterruptThread(target events.TextConversationTarget) (*events.InboundMessage, error) {
	_ = target
	return nil, nil
}
func (inertThreadRouter) RegisterCronThread(_ context.Context, _, _, _, _ string) error {
	return nil
}
func (inertThreadRouter) PrepareThreadReply(target events.TextConversationTarget) (bool, error) {
	_ = target
	return false, nil
}
func (inertThreadRouter) PrepareResponseThreadReply(target events.TextConversationTarget) (bool, error) {
	_ = target
	return false, nil
}
func (inertThreadRouter) SubmitThreadReply(_ context.Context, _ events.TextConversationTarget, _ *events.InboundMessage) (bool, error) {
	return false, nil
}
func (inertThreadRouter) SubmitResponseThreadReply(_ context.Context, _ events.TextConversationTarget, _ *events.InboundMessage) (bool, error) {
	return false, nil
}
func (inertThreadRouter) SummarizeThread(ctx context.Context, target events.TextConversationTarget) (bool, error) {
	_, _ = ctx, target
	return false, nil
}
func (inertThreadRouter) RecordResponseCheckpoint(target events.TextConversationTarget, checkpoint events.ResponseCheckpoint) error {
	_, _ = target, checkpoint
	return nil
}

type inertOneOffCronjobs struct{}

func (inertOneOffCronjobs) LoadOneOffCronjob(string) (cronjob.OneOffCronjob, error) {
	return cronjob.OneOffCronjob{}, errors.New("on-demand cronjobs are not configured")
}

func (inertOneOffCronjobs) RunOneOffCronjob(ctx context.Context, _ cronjob.OneOffCronjob, _ *harnessbridge.RawRunProgress, finish func(context.Context, cronjob.RunResult, error)) {
	finish(ctx, cronjob.RunResult{}, errors.New("on-demand cronjobs are not configured"))
}
