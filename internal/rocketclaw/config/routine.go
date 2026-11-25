package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/robfig/cron/v3"
)

type RoutineDefinition struct {
	Job      protocol.OneOffRoutine
	Schedule RoutineSchedule
}

type RoutineSchedule struct {
	Raw      string
	duration time.Duration
	parsed   cron.Schedule
}

func ParseRoutineSchedule(raw string) (RoutineSchedule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RoutineSchedule{}, errors.New("schedule must not be blank")
	}

	if duration, err := time.ParseDuration(raw); err == nil {
		if duration <= 0 {
			return RoutineSchedule{}, errors.New("duration schedules must be greater than zero")
		}

		return RoutineSchedule{Raw: raw, duration: duration}, nil
	}

	if strings.HasPrefix(raw, "@every") {
		return RoutineSchedule{}, errors.New("@every is not supported")
	}

	parsed, err := cron.ParseStandard(raw)
	if err != nil {
		return RoutineSchedule{}, fmt.Errorf("invalid cron expression: %w", err)
	}

	return RoutineSchedule{Raw: raw, parsed: parsed}, nil
}

func (s RoutineSchedule) Next(now time.Time) time.Time {
	if s.duration > 0 {
		return now.Add(s.duration)
	}

	return s.parsed.Next(now)
}
