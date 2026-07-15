// Package primarytext holds shared primary text connector helpers.
package primarytext

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/cronjob"
	"github.com/Rocketable/platform/internal/rocketclaw/emoji"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
)

const (
	socialAgentSwitchPrefix, socialAgentSwitchPrefixVariant = "🎛", "🎛️"
)

// OneOffCronjobRunner runs one loaded cronjob with progress callbacks.
type OneOffCronjobRunner interface {
	LoadOneOffCronjob(string) (cronjob.OneOffCronjob, error)
	RunOneOffCronjob(context.Context, cronjob.OneOffCronjob, *harnessbridge.RawRunProgress, func(context.Context, cronjob.RunResult, error))
}

// SplitSlackText splits Slack responses on the same preferred boundaries as the connector.
func SplitSlackText(text string, preferredLimit, hardLimit int) []string {
	return splitText(text, preferredLimit, hardLimit, slackChunkEnd)
}

// ParseSocialAgentSwitch returns the managed-conversation agent switch target, if text is a switch control message.
func ParseSocialAgentSwitch(text string) (string, bool) {
	text = emoji.CanonicalizeLeadingAlias(text)
	text = strings.TrimSpace(text)

	after, ok := strings.CutPrefix(text, socialAgentSwitchPrefixVariant)
	if !ok {
		after, ok = strings.CutPrefix(text, socialAgentSwitchPrefix)
		if !ok {
			return "", false
		}
	}

	if after != "" {
		r, size := utf8.DecodeRuneInString(after)
		if !unicode.IsSpace(r) {
			return "", false
		}

		after = after[size:]
	}

	return strings.TrimSpace(after), true
}

// GoalProgressText returns the shared visible goal progress prefix.
func GoalProgressText(turnNumber, maxTurns int) string {
	if maxTurns > 0 && turnNumber > 0 {
		return fmt.Sprintf("_Pursuing Goal (%d/%d)..._", turnNumber, maxTurns)
	}

	return "_Pursuing Goal..._"
}

// RunOneOffCronjob wires shared on-demand cron progress and final output handling.
func RunOneOffCronjob(ctx context.Context, runner OneOffCronjobRunner, loaded cronjob.OneOffCronjob, publish func(context.Context, string, string, bool, bool, []events.OutboundAttachment) error, afterFinalPublish func(context.Context, cronjob.RunResult), onPublishError func(error)) {
	thinking := ""
	progress := &harnessbridge.RawRunProgress{
		Thinking: func(ctx context.Context, text string) error {
			text = strings.TrimSpace(text)
			if text == "" {
				return nil
			}

			if thinking != "" {
				thinking += "\n"
			}

			thinking += text

			return publish(ctx, "", thinking, false, false, nil)
		},
		Message: func(ctx context.Context, text string) error {
			text = strings.TrimSpace(text)
			if text == "" {
				return nil
			}

			return publish(ctx, text, "", false, true, nil)
		},
	}

	runner.RunOneOffCronjob(ctx, loaded, progress, func(ctx context.Context, result cronjob.RunResult, err error) {
		if err != nil {
			if errPublish := publish(ctx, "I couldn't run that on-demand cron right now.", "", true, false, nil); errPublish != nil {
				onPublishError(errPublish)
			}

			return
		}

		payload := strings.TrimSpace(result.VerbatimMessage)
		if payload == "" && len(result.Attachments) == 0 {
			payload = "Cronjob completed and decided to emit no human-visible output."
		}

		if err := publish(ctx, payload, "", true, false, result.Attachments); err != nil {
			onPublishError(err)
			return
		}

		afterFinalPublish(ctx, result)
	})
}

func splitText(text string, preferredLimit, hardLimit int, chunkEnd func([]rune, int, int) int) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}

	chunks := make([]string, 0, len(runes)/preferredLimit+1)
	for len(runes) > 0 {
		if len(runes) < hardLimit {
			chunks = append(chunks, string(runes))
			break
		}

		end := chunkEnd(runes, preferredLimit, hardLimit)
		chunks = append(chunks, string(runes[:end]))
		runes = runes[end:]
	}

	return chunks
}

func slackChunkEnd(runes []rune, preferredLimit, hardLimit int) int {
	if end := slackBoundary(runes[:min(len(runes), preferredLimit)]); end > 0 {
		return end
	}

	if end := slackBoundary(runes[:min(len(runes), hardLimit)]); end > 0 {
		return end
	}

	return min(len(runes), hardLimit)
}

func slackBoundary(runes []rune) int {
	for i := range slices.Backward(runes) {
		if i > 0 && runes[i-1] == '\n' && runes[i] == '\n' {
			return i + 1
		}
	}

	for i := range slices.Backward(runes) {
		if runes[i] == '\n' {
			return i + 1
		}
	}

	for i := range slices.Backward(runes) {
		if unicode.IsSpace(runes[i]) && runes[i] != '\n' {
			return i + 1
		}
	}

	return 0
}
