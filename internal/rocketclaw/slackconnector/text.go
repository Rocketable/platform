package slackconnector

import (
	"fmt"
	"slices"
	"unicode"
)

func slackGoalProgressText(turnNumber, maxTurns int) string {
	if maxTurns > 0 && turnNumber > 0 {
		return fmt.Sprintf("_Pursuing Goal (%d/%d)..._", turnNumber, maxTurns)
	}

	return "_Pursuing Goal..._"
}

func slackGoalHeaderText(turnNumber, maxTurns int, complete bool) string {
	if complete {
		return "✅ Goal complete"
	}

	if maxTurns > 0 && turnNumber > 0 {
		return fmt.Sprintf("🏁 Pursuing Goal (%d/%d)...", turnNumber, maxTurns)
	}

	return "🏁 Pursuing Goal..."
}

func splitSlackText(text string, preferredLimit, hardLimit int) []string {
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

		end := slackChunkEnd(runes, preferredLimit, hardLimit)
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
