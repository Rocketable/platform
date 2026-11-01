//go:build linux

package sandbox

import (
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

const auditArch = unix.AUDIT_ARCH_AARCH64

// archPrologue has nothing to add on arm64: it has no second system call table.
func archPrologue() []bpf.Instruction { return nil }

// archDeniedSyscalls has nothing to add on arm64.
func archDeniedSyscalls() []uintptr { return nil }
