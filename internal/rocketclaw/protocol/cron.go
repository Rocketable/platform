package protocol

import (
	"context"
	"slices"
	"strings"
)

// CronRunResult captures the observable result of one cronjob run.
type CronRunResult struct {
	Text, VerbatimMessage string
	ConversationID        string
	Attachments           []OutboundAttachment
}

// OneOffCronjob captures a live one-off cronjob prompt loaded from disk.
type OneOffCronjob struct {
	Agent, Prompt, RelativePath, TextChannel string
	ConversationID                           string
}

// CronProgress receives one-off cron thinking and message callbacks.
type CronProgress struct {
	Thinking, Message func(context.Context, string) error
}

// OnDemandCronTarget extracts one deterministic top-level cron target from connector text.
func OnDemandCronTarget(text string, prefixes ...string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}

	candidates := []string{}
	if target, ok := singleOnDemandCronTarget(text, prefixes); ok {
		candidates = append(candidates, target)
	}

	for _, prefix := range prefixes {
		if after, ok := strings.CutPrefix(text, prefix); ok {
			if target, ok := singleOnDemandCronTarget(after, prefixes); ok {
				candidates = append(candidates, target)
			}
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

func singleOnDemandCronTarget(text string, prefixes []string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) != 1 {
		return "", false
	}

	if slices.Contains(prefixes, fields[0]) {
		return "", false
	}

	if target, ok := onDemandCronPathTarget(fields[0]); ok {
		return target, true
	}

	return fields[0], true
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
