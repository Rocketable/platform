package slackconnector

import (
	"cmp"
	"fmt"
	"slices"
	"unicode"
)

func slackGoalHeaderText(turnNumber, maxTurns int, complete bool) string {
	if complete {
		return "✅ Goal complete"
	}

	if maxTurns > 0 && turnNumber > 0 {
		return fmt.Sprintf("🏁 Pursuing Goal (%d/%d)...", turnNumber, maxTurns)
	}

	return "🏁 Pursuing Goal..."
}

func splitSlackText(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}

	chunks := make([]string, 0, len(runes)/limit+1)
	for len(runes) > 0 {
		// Slack escapes these characters when returning blocks; budget that form so a footer edit still fits.
		size, hardEnd := 0, 0

		for i, r := range runes {
			width := 1

			switch r {
			case '&':
				width = len("&amp;")
			case '<', '>':
				width = len("&lt;")
			}

			if size+width > limit {
				break
			}

			size += width

			hardEnd = i + 1
		}

		if hardEnd == len(runes) && size < limit {
			chunks = append(chunks, string(runes))
			break
		}

		end := hardEnd
		if bound := slackBoundary(runes[:hardEnd]); bound > 0 {
			end = bound
		}

		chunks = append(chunks, string(runes[:end]))
		runes = runes[end:]
	}

	return chunks
}

func slackBoundary(runes []rune) int {
	line, space := 0, 0

	for i := range slices.Backward(runes) {
		if i > 0 && runes[i-1] == '\n' && runes[i] == '\n' {
			return i + 1
		}

		if line == 0 && runes[i] == '\n' {
			line = i + 1
		}

		if space == 0 && unicode.IsSpace(runes[i]) && runes[i] != '\n' {
			space = i + 1
		}
	}

	return cmp.Or(line, space)
}
