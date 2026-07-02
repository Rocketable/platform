// Package main implements the funneld binary.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Rocketable/platform/internal/funneld"
)

func main() {
	if err := funneld.Run(context.Background(), os.Args[0], os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		os.Exit(1)
	}
}
