package protocol

import (
	"strings"
	"unicode"
)

// ParseDollarCommand splits canonical `$command args` text.
func ParseDollarCommand(text string) (command, args string, ok bool) {
	after, ok := strings.CutPrefix(strings.TrimSpace(text), "$")
	if !ok {
		return "", "", false
	}

	after = strings.TrimSpace(after)

	separator := strings.IndexFunc(after, unicode.IsSpace)
	if separator < 0 {
		return strings.ToLower(after), "", true
	}

	return strings.ToLower(after[:separator]), strings.TrimSpace(after[separator:]), true
}
