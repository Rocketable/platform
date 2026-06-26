// Package main implements the openresponsesd binary.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Rocketable/platform/internal/openresponsesd"
)

func main() {
	if err := openresponsesd.Run(context.Background(), os.Args[0], os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		os.Exit(1)
	}
}
