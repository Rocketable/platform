// Package main implements the interviewd binary.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Rocketable/platform/internal/interviewd"
)

func main() {
	if err := interviewd.Run(context.Background(), os.Args[0], os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)

		os.Exit(1)
	}
}
