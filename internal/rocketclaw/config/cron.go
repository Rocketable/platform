package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/robfig/cron/v3"
)

type CronDefinition struct {
	Job      protocol.OneOffCronjob
	Schedule CronSchedule
}

type CronSchedule struct {
	Raw      string
	duration time.Duration
	parsed   cron.Schedule
}

func ParseCronSchedule(raw string) (CronSchedule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CronSchedule{}, errors.New("schedule must not be blank")
	}

	if duration, err := time.ParseDuration(raw); err == nil {
		if duration <= 0 {
			return CronSchedule{}, errors.New("duration schedules must be greater than zero")
		}

		return CronSchedule{Raw: raw, duration: duration}, nil
	}

	if strings.HasPrefix(raw, "@every") {
		return CronSchedule{}, errors.New("@every is not supported")
	}

	parsed, err := cron.ParseStandard(raw)
	if err != nil {
		return CronSchedule{}, fmt.Errorf("invalid cron expression: %w", err)
	}

	return CronSchedule{Raw: raw, parsed: parsed}, nil
}

func (s CronSchedule) Next(now time.Time) time.Time {
	if s.duration > 0 {
		return now.Add(s.duration)
	}

	return s.parsed.Next(now)
}
