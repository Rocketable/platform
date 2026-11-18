package config

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/robfig/cron/v3"
	"sigs.k8s.io/yaml"
)

func LoadOneOffCronjob(workspace, runtimeDir, target string) (protocol.OneOffCronjob, error) {
	target = strings.TrimSpace(target)
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

func ParseCronDefinition(data []byte, relativePath string) (protocol.OneOffCronjob, []CronSchedule, error) {
	frontmatterBytes, body, err := splitCronFrontmatter(data)
	if err != nil {
		return protocol.OneOffCronjob{}, nil, fmt.Errorf("parse cronjob %s: %w", relativePath, err)
	}

	var raw cronFrontmatter
	if err := yaml.Unmarshal(frontmatterBytes, &raw); err != nil {
		return protocol.OneOffCronjob{}, nil, fmt.Errorf("parse cronjob %s frontmatter: unmarshal frontmatter yaml: %w", relativePath, err)
	}

	if raw.Schedule == nil {
		return protocol.OneOffCronjob{}, nil, fmt.Errorf("parse cronjob %s frontmatter: schedule is required", relativePath)
	}

	schedules := make([]CronSchedule, 0, len(raw.Schedule))
	for _, value := range raw.Schedule {
		schedule, err := ParseCronSchedule(value)
		if err != nil {
			return protocol.OneOffCronjob{}, nil, fmt.Errorf("parse cronjob %s schedule %q: %w", relativePath, value, err)
		}

		schedules = append(schedules, schedule)
	}

	return protocol.OneOffCronjob{RelativePath: relativePath, Agent: cmp.Or(string(raw.Agent), "main"), Space: raw.Space, Prompt: body}, schedules, nil
}

type cronFrontmatter struct {
	Schedule cronFrontmatterSchedule `json:"schedule"`
	Agent    cronFrontmatterAgent    `json:"agent"`
	Space    string                  `json:"space"`
}

type cronFrontmatterSchedule []string

func (s *cronFrontmatterSchedule) UnmarshalJSON(data []byte) error {
	if strings.TrimSpace(string(data)) == "null" {
		return nil
	}

	if data[0] == '"' {
		single := new(string)
		_ = json.Unmarshal(data, single)
		*s = cronFrontmatterSchedule{*single}

		return nil
	}

	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*s = list
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
	if err := json.Unmarshal(data, &text); err != nil {
		text = string(data)
	}

	*a = cronFrontmatterAgent(strings.TrimSpace(text))

	return nil
}

func splitCronFrontmatter(data []byte) (frontmatter []byte, body string, err error) {
	line, rest, ok := bytes.Cut(data, []byte("\n"))
	if !ok || !bytes.Equal(bytes.TrimSuffix(line, []byte("\r")), []byte("---")) {
		return nil, "", errors.New("yaml frontmatter is required")
	}

	for offset := 0; offset < len(rest); {
		line, after, _ := bytes.Cut(rest[offset:], []byte("\n"))
		if bytes.Equal(bytes.TrimSuffix(line, []byte("\r")), []byte("---")) {
			return rest[:offset], string(after), nil
		}

		offset = len(rest) - len(after)
	}

	return nil, "", errors.New("yaml frontmatter closing delimiter is required")
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
