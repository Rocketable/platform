package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/robfig/cron/v3"
	"sigs.k8s.io/yaml"
)

// LoadOneOffCronjob reads a current runtime definition without running it.
// The cron frontend adds its output-decision instruction to Prompt.
func LoadOneOffCronjob(workspace, runtimeDir, target string) (protocol.OneOffCronjob, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return protocol.OneOffCronjob{}, errors.New("cron target must be a top-level cron stem like daily or daily.md")
	}

	if strings.Contains(target, "/") || strings.Contains(target, `\`) {
		return protocol.OneOffCronjob{}, errors.New("cron target must be a top-level cron stem; nested paths are not allowed")
	}

	name := target
	if before, ok := strings.CutSuffix(name, ".md"); ok {
		name = before
	} else if filepath.Ext(name) != "" {
		return protocol.OneOffCronjob{}, errors.New("cron target must omit extensions other than .md")
	}

	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return protocol.OneOffCronjob{}, errors.New("cron target must be a top-level cron stem like daily or daily.md")
	}

	if strings.HasSuffix(name, ".example") {
		return protocol.OneOffCronjob{}, errors.New("cron target must reference a real cron file, not an example template")
	}

	root, err := os.OpenRoot(workspace)
	if err != nil {
		return protocol.OneOffCronjob{}, fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	relativePath := "cron/" + name + ".md"

	data, err := root.ReadFile(filepath.ToSlash(filepath.Join(runtimeDir, relativePath)))
	if err != nil {
		return protocol.OneOffCronjob{}, fmt.Errorf("read cronjob %s: %w", relativePath, err)
	}

	job, _, err := ParseCronDefinition(data, relativePath)

	return job, err
}

// CronWebAgentChoices keeps configured channel order among loaded agents, with
// the caller's sorted loaded-agent list as the fallback.
func (c *Config) CronWebAgentChoices(loaded []string, job *protocol.OneOffCronjob) []string {
	for _, channel := range c.Slack.Channels {
		if channel.Channel != job.TextChannel {
			continue
		}

		allowed := slices.DeleteFunc(slices.Clone(channel.Agents), func(name string) bool { return !slices.Contains(loaded, name) })
		if len(allowed) > 0 {
			return allowed
		}
	}

	return loaded
}

// ParseCronDefinition validates a cron asset and returns its prompt and schedules.
func ParseCronDefinition(data []byte, relativePath string) (protocol.OneOffCronjob, []CronSchedule, error) {
	frontmatterBytes, body, err := splitCronFrontmatter(data)
	if err != nil {
		return protocol.OneOffCronjob{}, nil, fmt.Errorf("parse cronjob %s: %w", relativePath, err)
	}

	var raw cronFrontmatter
	if err := yaml.Unmarshal(frontmatterBytes, &raw); err != nil {
		return protocol.OneOffCronjob{}, nil, fmt.Errorf("parse cronjob %s frontmatter: unmarshal frontmatter yaml: %w", relativePath, err)
	}

	if !raw.Schedule.present {
		return protocol.OneOffCronjob{}, nil, fmt.Errorf("parse cronjob %s frontmatter: schedule is required", relativePath)
	}

	if raw.Channel == "" {
		return protocol.OneOffCronjob{}, nil, fmt.Errorf("parse cronjob %s frontmatter: channel is required", relativePath)
	}

	oneOff := false

	schedules := make([]CronSchedule, 0, len(raw.Schedule.values))
	for _, value := range raw.Schedule.values {
		schedule, err := parseCronSchedule(value)
		if err != nil {
			return protocol.OneOffCronjob{}, nil, fmt.Errorf("parse cronjob %s schedule %q: %w", relativePath, value, err)
		}

		oneOff = oneOff || !schedule.DueAt.IsZero()
		schedules = append(schedules, schedule)
	}

	if oneOff && len(raw.Schedule.values) != 1 {
		return protocol.OneOffCronjob{}, nil, fmt.Errorf("parse cronjob %s schedules: timestamp schedules cannot be combined with other schedules", relativePath)
	}

	agent := string(raw.Agent)
	if agent == "" {
		agent = "main"
	}

	return protocol.OneOffCronjob{RelativePath: relativePath, Agent: agent, TextChannel: string(raw.Channel), Prompt: body}, schedules, nil
}

type cronFrontmatter struct {
	Schedule cronFrontmatterSchedule `json:"schedule"`
	Agent    cronFrontmatterAgent    `json:"agent"`
	Channel  cronFrontmatterChannel  `json:"channel"`
}

type cronFrontmatterSchedule struct {
	present bool
	values  []string
}

func (s *cronFrontmatterSchedule) UnmarshalJSON(data []byte) error {
	if strings.TrimSpace(string(data)) == "null" {
		return nil
	}

	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		s.present, s.values = true, []string{single}
		return nil
	}

	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		s.present, s.values = true, slices.Clone(list)
		return nil
	}

	return errors.New("schedule must be a string or list of strings")
}

type cronFrontmatterAgent string

func (a *cronFrontmatterAgent) UnmarshalJSON(data []byte) error {
	if strings.TrimSpace(string(data)) == "null" {
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*a = cronFrontmatterAgent(strings.TrimSpace(text))
		return nil
	}

	*a = cronFrontmatterAgent(strings.TrimSpace(string(data)))

	return nil
}

type cronFrontmatterChannel string

func (c *cronFrontmatterChannel) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		text = strings.TrimSpace(text)
		if text != "" && !strings.HasPrefix(text, "#") {
			text = "#" + text
		}

		*c = cronFrontmatterChannel(text)
	}

	return nil
}

func splitCronFrontmatter(data []byte) (frontmatter []byte, body string, err error) {
	source := string(data)

	line, rest, ok := strings.Cut(source, "\n")
	if !ok || strings.TrimSuffix(line, "\r") != "---" {
		return nil, "", errors.New("yaml frontmatter is required")
	}

	start := len(source) - len(rest)
	for offset := start; offset < len(source); {
		line, rest, _ = strings.Cut(source[offset:], "\n")
		if strings.TrimSuffix(line, "\r") == "---" {
			return []byte(source[start:offset]), rest, nil
		}

		offset = len(source) - len(rest)
	}

	return nil, "", errors.New("yaml frontmatter closing delimiter is required")
}

// CronSchedule is a timestamp, duration, or cron cadence from a runtime asset.
type CronSchedule struct {
	Raw      string
	DueAt    time.Time
	duration time.Duration
	parsed   cron.Schedule
}

func parseCronSchedule(raw string) (CronSchedule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CronSchedule{}, errors.New("schedule must not be blank")
	}

	if dueAt, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return CronSchedule{Raw: raw, DueAt: dueAt}, nil
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

// Next returns the next occurrence of a recurring schedule after now.
func (s CronSchedule) Next(now time.Time) time.Time {
	if s.duration > 0 {
		return now.Add(s.duration)
	}

	return s.parsed.Next(now)
}
