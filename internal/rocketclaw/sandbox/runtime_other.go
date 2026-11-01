//go:build !linux || !(amd64 || arm64)

package sandbox

import (
	"context"
	"errors"
)

var errNeedsLinux = errors.New("needs Linux on amd64 or arm64; run RocketClaw in a Linux container")

// Runtime here only carries why the sandbox is unavailable. The zero value is unavailable too.
type Runtime struct {
	err    error
	rootfs string // never set: Err reports the zero value as not prepared
}

// Prepare reports that the sandbox needs Linux; emulated shells refuse here, never running host Bash.
func Prepare(context.Context, []byte, string) *Runtime {
	return &Runtime{err: errNeedsLinux}
}

// Run always fails here.
func (rt *Runtime) Run(context.Context, *Command, Host) (Result, error) {
	return Result{}, rt.Err()
}

// Guest never runs here.
func Guest([]string) (int, bool) { return 0, false }
