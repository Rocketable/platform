//go:build linux

package sandbox

import (
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

const auditArch = unix.AUDIT_ARCH_X86_64

// archPrologue kills x32 system calls (bit 30 set), which would slip past every number check after it.
func archPrologue() []bpf.Instruction {
	return []bpf.Instruction{
		bpf.JumpIf{Cond: bpf.JumpGreaterOrEqual, Val: 0x40000000, SkipFalse: 1},
		bpf.RetConstant{Val: seccompRetKillProcess},
	}
}

// archDeniedSyscalls are amd64's extra legacy and port-I/O system calls.
func archDeniedSyscalls() []uintptr {
	return []uintptr{unix.SYS_IOPL, unix.SYS_IOPERM, unix.SYS_USELIB, unix.SYS_USTAT, unix.SYS_SYSFS, unix.SYS__SYSCTL}
}
