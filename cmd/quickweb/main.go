// Package main implements the quickweb binary.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Rocketable/platform/internal/quickweb"
)

func main() {
	if err := quickweb.Run(context.Background(), os.Args[0], os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		os.Exit(1)
	}
}
