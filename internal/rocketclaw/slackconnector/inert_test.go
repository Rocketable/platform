package slackconnector

import (
	"context"
	"errors"

	"github.com/Rocketable/platform/internal/rocketclaw/cronjob"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
)

type inertThreadRouter struct{}

func (inertThreadRouter) StartThread(context.Context, string, bool, *events.InboundMessage) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) StartSlackGoalInThread(context.Context, string, string, string, int, *events.InboundMessage) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) InterruptSlackThread(context.Context, string, string) (*events.SlackReplyTarget, error) {
	return nil, nil
}
func (inertThreadRouter) RegisterCronThread(context.Context, string, string, string, string) error {
	return nil
}
func (inertThreadRouter) PrepareThreadReply(context.Context, string, string) (bool, error) {
	return false, nil
}
func (inertThreadRouter) PrepareResponseThreadReply(context.Context, string, string) (bool, error) {
	return false, nil
}
func (inertThreadRouter) SubmitThreadReply(context.Context, string, string, *events.InboundMessage) (bool, error) {
	return false, nil
}
func (inertThreadRouter) SubmitResponseThreadReply(context.Context, string, string, *events.InboundMessage) (bool, error) {
	return false, nil
}
func (inertThreadRouter) SummarizeThread(context.Context, string, string) (bool, error) {
	return false, nil
}
func (inertThreadRouter) RecordResponseCheckpoint(context.Context, string, string, events.ResponseCheckpoint) error {
	return nil
}

type inertOneOffCronjobs struct{}

func (inertOneOffCronjobs) LoadOneOffCronjob(string) (cronjob.OneOffCronjob, error) {
	return cronjob.OneOffCronjob{}, errors.New("on-demand cronjobs are not configured")
}

func (inertOneOffCronjobs) RunOneOffCronjob(ctx context.Context, _ cronjob.OneOffCronjob, _ *harnessbridge.RawRunProgress, finish func(context.Context, cronjob.RunResult, error)) {
	finish(ctx, cronjob.RunResult{}, errors.New("on-demand cronjobs are not configured"))
}
