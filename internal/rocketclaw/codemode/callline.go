package codemode

import (
	"context"
	"strings"
)

type (
	sourceContext   struct{}
	callLineContext struct{}
)

func CallLine(ctx context.Context) (number int, code string) {
	number, _ = ctx.Value(callLineContext{}).(int)
	if number == 0 {
		return 0, ""
	}

	source, _ := ctx.Value(sourceContext{}).(string)
	for range number - 1 {
		end := strings.IndexAny(source, "\r\n")
		ending := source[end]

		source = source[end+1:]
		if ending == '\r' {
			source = strings.TrimPrefix(source, "\n")
		}
	}

	if end := strings.IndexAny(source, "\r\n"); end >= 0 {
		source = source[:end]
	}

	return number, strings.TrimSpace(source)
}
