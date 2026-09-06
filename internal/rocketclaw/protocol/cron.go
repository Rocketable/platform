package protocol

import (
	"strings"
)

// CronRunResult captures the observable result of one cronjob run.
type CronRunResult struct {
	ConversationID string
}

// OneOffCronjob captures a live one-off cronjob prompt loaded from disk.
type OneOffCronjob struct {
	Agent, Prompt, RelativePath, TextChannel string
	ConversationID                           string
}

// OnDemandCronTarget extracts one deterministic top-level cron target from connector text.
func OnDemandCronTarget(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}

	var candidates []string

	if fields := strings.Fields(text); len(fields) == 1 {
		if target, ok := onDemandCronPathTarget(fields[0]); ok {
			candidates = append(candidates, target)
		} else {
			candidates = append(candidates, fields[0])
		}
	}

	for field := range strings.FieldsSeq(text) {
		field = strings.Trim(field, "`.,;:()[]<>")
		if target, ok := onDemandCronPathTarget(field); ok {
			candidates = append(candidates, target)
		}
	}

	if len(candidates) == 0 {
		return "", false
	}

	target := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate != target {
			return "", false
		}
	}

	return target, true
}

func onDemandCronPathTarget(text string) (string, bool) {
	if !strings.HasPrefix(text, "cron/") || !strings.HasSuffix(text, ".md") {
		return "", false
	}

	target := strings.TrimSuffix(strings.TrimPrefix(text, "cron/"), ".md")
	if target == "" || strings.ContainsAny(target, `/\`) {
		return "", false
	}

	return target, true
}
