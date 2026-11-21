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

	return number, strings.TrimSpace(strings.Split(source, "\n")[number-1])
}
