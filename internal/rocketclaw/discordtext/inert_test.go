package discordtext

import (
	"context"
	"errors"

	"github.com/Rocketable/platform/internal/rocketclaw/cronjob"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
)

type inertOneOffCronjobs struct{}

func (inertOneOffCronjobs) LoadOneOffCronjob(string) (cronjob.OneOffCronjob, error) {
	return cronjob.OneOffCronjob{}, errors.New("on-demand cronjobs are not configured")
}

func (inertOneOffCronjobs) RunOneOffCronjob(ctx context.Context, _ cronjob.OneOffCronjob, _ *harnessbridge.RawRunProgress, finish func(context.Context, cronjob.RunResult, error)) {
	finish(ctx, cronjob.RunResult{}, errors.New("on-demand cronjobs are not configured"))
}
