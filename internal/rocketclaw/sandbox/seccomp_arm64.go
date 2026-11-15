//go:build linux

package sandbox

import (
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

const auditArch = unix.AUDIT_ARCH_AARCH64

func archPrologue() []bpf.Instruction { return nil }

func archDeniedSyscalls() []uintptr { return nil }
