package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Rocketable/platform/internal/harnessfeedback"
)

func main() {
	if err := harnessfeedback.Run(context.Background(), os.Getenv); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		os.Exit(1)
	}
}
