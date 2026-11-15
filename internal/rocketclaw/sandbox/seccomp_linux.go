//go:build amd64 || arm64

package sandbox

import (
	"encoding/binary"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

const (
	seccompNr   = 0
	seccompArch = 4
	seccompArg0 = 16
)

const (
	seccompRetKillProcess = 0x80000000
	seccompRetErrno       = 0x00050000
	seccompRetAllow       = 0x7fff0000
)

func seccompProgram() []byte {
	denied := append([]uintptr{
		unix.SYS_ACCT, unix.SYS_ADD_KEY, unix.SYS_BPF, unix.SYS_CHROOT, unix.SYS_CLOCK_ADJTIME,
		unix.SYS_CLOCK_SETTIME, unix.SYS_DELETE_MODULE, unix.SYS_FINIT_MODULE, unix.SYS_FSCONFIG,
		unix.SYS_FSMOUNT, unix.SYS_FSOPEN, unix.SYS_FSPICK, unix.SYS_INIT_MODULE, unix.SYS_IO_URING_ENTER,
		unix.SYS_IO_URING_REGISTER, unix.SYS_IO_URING_SETUP, unix.SYS_KEXEC_FILE_LOAD, unix.SYS_KEXEC_LOAD,
		unix.SYS_KEYCTL, unix.SYS_LOOKUP_DCOOKIE, unix.SYS_MOUNT, unix.SYS_MOUNT_SETATTR, unix.SYS_MOVE_MOUNT,
		unix.SYS_NAME_TO_HANDLE_AT, unix.SYS_OPEN_BY_HANDLE_AT, unix.SYS_OPEN_TREE, unix.SYS_PERF_EVENT_OPEN,
		unix.SYS_PIDFD_GETFD, unix.SYS_PIVOT_ROOT, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV,
		unix.SYS_PTRACE, unix.SYS_QUOTACTL, unix.SYS_QUOTACTL_FD, unix.SYS_REBOOT, unix.SYS_REQUEST_KEY,
		unix.SYS_SETDOMAINNAME, unix.SYS_SETHOSTNAME, unix.SYS_SETNS, unix.SYS_SETTIMEOFDAY, unix.SYS_SWAPOFF,
		unix.SYS_SWAPON, unix.SYS_SYSLOG, unix.SYS_UMOUNT2, unix.SYS_UNSHARE, unix.SYS_USERFAULTFD,
		unix.SYS_VHANGUP,
	}, archDeniedSyscalls()...)
	sockets := []uintptr{unix.SYS_SOCKET, unix.SYS_SOCKETPAIR}

	kill := bpf.RetConstant{Val: seccompRetKillProcess}
	eperm := bpf.RetConstant{Val: seccompRetErrno | uint32(unix.EPERM)}
	allow := bpf.RetConstant{Val: seccompRetAllow}

	prog := make([]bpf.Instruction, 0, 6+2*len(denied)+5*len(sockets))
	prog = append(prog,
		bpf.LoadAbsolute{Off: seccompArch, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: auditArch, SkipTrue: 1},
		kill,
		bpf.LoadAbsolute{Off: seccompNr, Size: 4},
	)
	prog = append(prog, archPrologue()...)

	for _, nr := range denied {
		prog = append(prog, bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(nr), SkipFalse: 1}, eperm)
	}

	for _, nr := range sockets {
		prog = append(prog,
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(nr), SkipFalse: 4},
			bpf.LoadAbsolute{Off: seccompArg0, Size: 4},
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.AF_UNIX, SkipTrue: 1},
			bpf.RetConstant{Val: seccompRetErrno | uint32(unix.EAFNOSUPPORT)},
			allow,
		)
	}

	prog = append(prog, allow)

	raw, _ := bpf.Assemble(prog)

	out := make([]byte, 0, 8*len(raw))
	for _, ins := range raw {
		out = binary.NativeEndian.AppendUint16(out, ins.Op)
		out = append(out, ins.Jt, ins.Jf)
		out = binary.NativeEndian.AppendUint32(out, ins.K)
	}

	return out
}
