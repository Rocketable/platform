//go:build amd64 || arm64

package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Spec §10.1 limits.
const (
	memoryBytes     = 256 << 20 // cgroup memory.max
	tasks           = 64        // cgroup pids.max, bwrap's own processes included
	cpuMillis       = 1000      // cgroup cpu.max: one CPU
	outputBytes     = 16 << 20  // stdout and stderr together, host command output separately
	fileBytes       = 256 << 20 // RLIMIT_FSIZE: the largest file the guest can write
	tmpBytes        = 256 << 20 // the per-command /tmp
	scriptBytes     = 8 << 20
	hostInputBytes  = 16 << 20 // stdin per host command
	hostInvocations = 128      // host commands per shell command
	hostConcurrency = 16
	guestNoFile     = 1024
)

// Guest paths of the per-command files.
const (
	guestRunDir = "/run/rocketclaw"
	guestScript = guestRunDir + "/script"
	guestExecID = guestRunDir + "/exec-id"
	guestSocket = guestRunDir + "/command.sock"
	guestExtDir = "/extensions"
	guestRelay  = guestExtDir + "/.rocketclaw/rocketclaw"
	// bootstrap makes every Bash, nested ones too, call a host command for a bare name even when a builtin
	// has it (spec §6.2).
	bootstrapPath = guestExtDir + "/.rocketclaw/bootstrap"
	readyFD       = 5 // ExtraFiles: 3 bwrap arguments, 4 seccomp, 5 ready
)

// shellIdentifier names get a Bash function, so they win over builtins; others are reached through PATH.
var shellIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Runtime runs emulated shell commands. The zero value is unavailable: every Run fails, never falling back to
// host Bash.
type Runtime struct {
	err     error  // why the sandbox is unavailable
	rootfs  string // unpacked rootfs directory
	self    string // this executable, the guest relay
	bwrap   string
	cgroup  string // delegated cgroup parent
	state   string // per-command files and sockets
	seccomp []byte
}

// Run runs cmd in a fresh sandbox. A shell that started always yields a Result, whatever its exit; an error
// means no guest code ran.
func (rt *Runtime) Run(ctx context.Context, cmd *Command, host Host) (Result, error) {
	if err := rt.Err(); err != nil {
		return Result{}, err
	}

	if len(cmd.Script) > scriptBytes {
		return Result{}, fmt.Errorf("command of %d bytes exceeds %d", len(cmd.Script), scriptBytes)
	}

	for _, name := range cmd.Commands {
		if err := CheckCommandName(name); err != nil {
			return Result{}, fmt.Errorf("command %w", err)
		}
	}

	names := append([]string{Curl}, cmd.Commands...)

	execID := "x-" + rand.Text()

	dir := filepath.Join(rt.state, execID)
	if err := writeExecFiles(dir, execID, cmd.Script, names); err != nil {
		return Result{}, fmt.Errorf("write command files: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	ln, err := listenIn(filepath.Join(dir, "run"), "command.sock")
	if err != nil {
		return Result{}, fmt.Errorf("listen: %w", err)
	}

	cg, err := newExecCgroup(filepath.Join(rt.cgroup, execID))
	if err != nil {
		_ = ln.Close()
		return Result{}, err
	}
	defer func() { _ = cg.remove() }()

	rel, err := cgroupRelative(cg.path)
	if err != nil {
		_ = ln.Close()
		return Result{}, err
	}

	b := &broker{
		execID: execID,
		cgroup: rel,
		names:  names,
		host:   host,
		out:    newBudget(outputBytes),
		ln:     ln,
		slots:  make(chan struct{}, hostConcurrency),
		conns:  map[*net.UnixConn]struct{}{},
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go b.serve(ctx)
	defer b.close()

	return rt.sandbox(ctx, cmd, dir, cg, b.out)
}

// writeExecFiles writes the read-only files one sandbox sees: the script, its ID, the command shims and the
// Bash bootstrap.
func writeExecFiles(dir, execID, script string, names []string) error {
	run := filepath.Join(dir, "run")
	ext := filepath.Join(dir, "extensions")
	helper := filepath.Join(ext, ".rocketclaw")

	for _, d := range []string{dir, run, ext, helper} {
		if err := os.Mkdir(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	var bootstrap strings.Builder

	for _, name := range names {
		if err := os.Symlink(".rocketclaw/rocketclaw", filepath.Join(ext, name)); err != nil {
			return fmt.Errorf("shim %s: %w", name, err)
		}

		if shellIdentifier.MatchString(name) {
			fmt.Fprintf(&bootstrap, "%s() { %s/%s \"$@\"; }\n", name, guestExtDir, name)
		}
	}

	for name, data := range map[string]string{
		filepath.Join(run, "script"):        script,
		filepath.Join(run, "exec-id"):       execID,
		filepath.Join(helper, "bootstrap"):  bootstrap.String(),
		filepath.Join(helper, "rocketclaw"): "", // mount point for the relay
	} {
		if err := os.WriteFile(name, []byte(data), 0o444); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	return nil
}

// bwrapArgs builds the sandbox (docs/explainers/2026-11-01-emulated-shell-sandbox.md). They reach bwrap through --args, so the guest's
// /proc/1/cmdline shows no host paths.
func (rt *Runtime) bwrapArgs(cmd *Command, dir string) []string {
	args := []string{
		"--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup",
		"--disable-userns", "--die-with-parent", "--new-session", "--cap-drop", "ALL",
		"--uid", "1000", "--gid", "1000", "--hostname", "rocketclaw",
		"--tmpfs", "/",
	}

	entries, _ := os.ReadDir(rt.rootfs) // checked by Prepare
	for _, e := range entries {
		switch name := e.Name(); {
		case name == "proc" || name == "dev" || name == "sys" || name == "tmp" || name == "run" || name == "home":
		case e.Type()&os.ModeSymlink != 0:
			target, _ := os.Readlink(filepath.Join(rt.rootfs, name))
			args = append(args, "--symlink", target, "/"+name)
		case e.IsDir():
			args = append(args, "--ro-bind", filepath.Join(rt.rootfs, name), "/"+name)
		}
	}

	args = append(args, "--proc", "/proc", "--dev", "/dev", "--size", strconv.Itoa(tmpBytes), "--tmpfs", "/tmp", "--bind", cmd.Dir, cmd.Dir)
	for _, ro := range cmd.ReadOnly {
		args = append(args, "--ro-bind", ro, ro)
	}

	args = append(args,
		"--ro-bind", filepath.Join(dir, "run"), guestRunDir,
		"--ro-bind", filepath.Join(dir, "extensions"), guestExtDir,
		"--ro-bind", rt.self, guestRelay,
		"--remount-ro", "/",
		"--seccomp", "4",
		"--chdir", cmd.Cwd,
		"--clearenv",
	)

	for _, kv := range append(cmd.Env, "BASH_ENV="+bootstrapPath) {
		k, v, _ := strings.Cut(kv, "=")
		args = append(args, "--setenv", k, v)
	}

	return args
}

// listenIn binds a Unix socket named name in dir through /proc/self/fd, so the 108-byte socket path limit
// never depends on how deep dir is.
func listenIn(dir, name string) (*net.UnixListener, error) {
	fd, err := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	defer func() { _ = unix.Close(fd) }()

	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: fmt.Sprintf("/proc/self/fd/%d/%s", fd, name), Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", name, err)
	}

	// The /proc path stops naming the socket once fd closes; removing the command's directory deletes it.
	ln.SetUnlinkOnClose(false)

	return ln, nil
}

// memfd returns a file holding data, for bwrap to read through an inherited descriptor.
func memfd(name string, data []byte) (*os.File, error) {
	fd, err := unix.MemfdCreate(name, unix.MFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("memfd %s: %w", name, err)
	}

	f := os.NewFile(uintptr(fd), name)
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("memfd %s: %w", name, err)
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("memfd %s: %w", name, err)
	}

	return f, nil
}

// sandbox starts bwrap in cg and collects the result.
func (rt *Runtime) sandbox(ctx context.Context, cmd *Command, dir string, cg *execCgroup, hostOut *budget) (Result, error) {
	argsFile, err := memfd("rocketclaw-bwrap-args", []byte(strings.Join(rt.bwrapArgs(cmd, dir), "\x00")+"\x00"))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = argsFile.Close() }()

	seccompFile, err := memfd("rocketclaw-seccomp", rt.seccomp)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = seccompFile.Close() }()

	readyR, readyW, err := os.Pipe()
	if err != nil {
		return Result{}, fmt.Errorf("pipe: %w", err)
	}
	defer func() { _ = readyR.Close() }()

	outR, outW, err := os.Pipe()
	if err != nil {
		_ = readyW.Close()
		return Result{}, fmt.Errorf("pipe: %w", err)
	}
	defer func() { _ = outR.Close() }()

	c := &exec.Cmd{
		Path: rt.bwrap,
		Args: []string{
			"bwrap", "--args", "3", "--",
			guestRelay, "init", strconv.Itoa(fileBytes), strconv.Itoa(guestNoFile), strconv.Itoa(readyFD), "--",
			"/bin/bash", "--noprofile", "--norc", guestScript,
		},
		Env:        []string{}, // the guest reads bwrap's own environment through /proc/1/environ
		Stdout:     outW,
		Stderr:     outW,
		ExtraFiles: []*os.File{argsFile, seccompFile, readyW},
		SysProcAttr: &syscall.SysProcAttr{
			UseCgroupFD: true, // CLONE_INTO_CGROUP: the limits hold from the first instruction
			CgroupFD:    cg.fd,
			Pdeathsig:   syscall.SIGKILL,
		},
	}

	errStart := c.Start()
	_ = outW.Close()
	_ = readyW.Close()

	if errStart != nil {
		return Result{}, fmt.Errorf("start bwrap: %w", errStart)
	}

	out := newBudget(outputBytes)

	var output bytes.Buffer

	copied := make(chan struct{})

	go func() {
		defer close(copied)

		chunk := make([]byte, 32<<10)
		for {
			n, err := outR.Read(chunk)
			output.Write(chunk[:out.take(n)])

			if err != nil {
				return
			}
		}
	}()

	// "1" when init ran; "1E" when it ran but could not exec Bash; nothing when bwrap failed.
	ready := make(chan bool, 1)

	go func() {
		b, _ := io.ReadAll(io.LimitReader(readyR, 2))
		ready <- string(b) == "1"
	}()

	exited := make(chan struct{})

	go func() {
		_ = c.Wait()

		close(exited)
	}()

	timer := time.NewTimer(cmd.Timeout)
	defer timer.Stop()

	timedOut := false

	select {
	case <-exited:
	case <-timer.C:
		timedOut = true
	case <-ctx.Done():
		timedOut = true
	case <-out.over:
	case <-hostOut.over:
	}

	// Kill whatever is left: the shell after a limit, background jobs after a normal exit.
	errKill := killCgroup(cg.path)

	<-exited
	<-copied

	if errKill != nil {
		return Result{}, errKill
	}

	if !<-ready {
		return Result{}, fmt.Errorf("the sandbox did not start: %s", strings.TrimSpace(output.String()))
	}

	res := Result{Output: output.Bytes(), TimedOut: timedOut, Truncated: out.spent() || hostOut.spent()}

	// The agent sees only the output, so it learns here why a command stopped early.
	switch {
	case res.Truncated:
		res.Output = fmt.Appendf(res.Output, "\n[emulated shell: output stopped at %d MiB]\n", outputBytes>>20)
	case cg.hitResourceLimit():
		res.Output = fmt.Appendf(res.Output, "\n[emulated shell: memory (%d MiB) or process (%d) limit reached]\n", memoryBytes>>20, tasks)
	}

	res.ExitCode = c.ProcessState.ExitCode()
	if ws, ok := c.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		res.ExitCode = 128 + int(ws.Signal())
	}

	if timedOut {
		res.ExitCode = 124
	}

	return res, nil
}
