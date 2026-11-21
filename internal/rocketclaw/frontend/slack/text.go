package slackconnector

import (
	"cmp"
	"fmt"
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
		size, hardEnd := 0, 0
		paragraph, line, space := 0, 0, 0

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

			switch {
			case r == '\n':
				line = hardEnd
				if i > 0 && runes[i-1] == '\n' {
					paragraph = hardEnd
				}
			case unicode.IsSpace(r):
				space = hardEnd
			}
		}

		if hardEnd == len(runes) && size < limit {
			chunks = append(chunks, string(runes))
			break
		}

		end := cmp.Or(paragraph, line, space, hardEnd)
		chunks = append(chunks, string(runes[:end]))
		runes = runes[end:]
	}

	return chunks
}
