// Package main implements the quickbench binary.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Rocketable/platform/internal/quickbench"
)

func main() {
	if err := quickbench.Run(context.Background(), os.Args[0], os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		os.Exit(1)
	}
}
