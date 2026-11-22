//go:build amd64 || arm64

package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	memoryBytes     = 256 << 20
	tasks           = 64
	cpuMillis       = 1000
	outputBytes     = 16 << 20
	fileBytes       = 256 << 20
	tmpBytes        = 256 << 20
	scriptBytes     = 8 << 20
	hostInvocations = 128
	hostConcurrency = 16
	guestNoFile     = 1024
)

const (
	guestRunDir   = "/run/rocketclaw"
	guestScript   = guestRunDir + "/script"
	guestExecID   = guestRunDir + "/exec-id"
	guestSocket   = guestRunDir + "/command.sock"
	guestExtDir   = "/extensions"
	guestRelay    = guestExtDir + "/.rocketclaw/rocketclaw"
	bootstrapPath = guestExtDir + "/.rocketclaw/bootstrap"
	readyFD       = 5
)

var shellIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Runtime struct {
	p *prepared
}

type prepared struct {
	ready   chan struct{}
	err     error
	mu      sync.Mutex
	retry   error
	rootfs  string
	self    string
	bwrap   string
	cgroup  string
	state   string
	seccomp []byte
	log     *slog.Logger
}

func (rt *Runtime) Run(ctx context.Context, cmd *Command, host Host) (Result, error) {
	if err := rt.Wait(ctx); err != nil {
		return Result{}, err
	}

	return rt.run(ctx, cmd, host)
}

const limitReached = "emulated shell limit reached"

func (rt *Runtime) run(ctx context.Context, cmd *Command, host Host) (Result, error) {
	conversation := ""

	for _, kv := range cmd.Env {
		if id, ok := strings.CutPrefix(kv, "ROCKETCLAW_CONVERSATION_ID="); ok {
			conversation = id
		}
	}

	log := rt.p.log.With("conversation", conversation, "folder", cmd.Dir, "cwd", cmd.Cwd, "script_bytes", len(cmd.Script))

	if len(cmd.Script) > scriptBytes {
		log.Warn(limitReached, "limit", "script", "limit_bytes", scriptBytes)

		return Result{}, fmt.Errorf("command of %d bytes exceeds %d", len(cmd.Script), scriptBytes)
	}

	for _, name := range cmd.Commands {
		if err := CheckCommandName(name); err != nil {
			return Result{}, fmt.Errorf("command %w", err)
		}
	}

	names := append([]string{Curl}, cmd.Commands...)

	execID := "x-" + rand.Text()
	log = log.With("exec", execID)

	dir := filepath.Join(rt.p.state, execID)
	if err := writeExecFiles(dir, execID, cmd.Script, names); err != nil {
		return Result{}, fmt.Errorf("write command files: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	cg, err := newExecCgroup(filepath.Join(rt.p.cgroup, execID))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = cg.remove() }()

	ln, err := listenIn(filepath.Join(dir, "run"), "command.sock")
	if err != nil {
		return Result{}, fmt.Errorf("listen: %w", err)
	}

	b := &broker{
		execID: execID,
		cgroup: strings.TrimPrefix(cg.path, cgroupRoot),
		names:  names,
		host:   host,
		log:    log,
		out:    newBudget(outputBytes),
		ln:     ln,
		slots:  make(chan struct{}, hostConcurrency),
		conns:  map[*net.UnixConn]struct{}{},
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go b.serve(ctx)
	defer b.close()

	return rt.sandbox(ctx, cmd, dir, cg, b.out, log)
}

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
		filepath.Join(helper, "rocketclaw"): "",
	} {
		if err := os.WriteFile(name, []byte(data), 0o444); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	return nil
}

func (rt *Runtime) imageArgs(args []string, e os.DirEntry, name string, binds []string) []string {
	host, guest := filepath.Join(rt.p.rootfs, name), "/"+name

	switch {
	case e.Type()&os.ModeSymlink != 0:
		target, _ := os.Readlink(host)
		return append(args, "--symlink", target, guest)
	case !e.IsDir() || !slices.ContainsFunc(binds, func(b string) bool { return strings.HasPrefix(b, guest+"/") }):
		return append(args, "--ro-bind", host, guest)
	}

	entries, _ := os.ReadDir(host)
	for _, child := range entries {
		args = rt.imageArgs(args, child, filepath.Join(name, child.Name()), binds)
	}

	return args
}

func (rt *Runtime) bwrapArgs(cmd *Command, dir string) []string {
	args := []string{
		"--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup",
		"--disable-userns", "--die-with-parent", "--new-session", "--cap-drop", "ALL",
		"--uid", "1000", "--gid", "1000", "--hostname", "rocketclaw",
		"--tmpfs", "/",
	}

	binds := append([]string{cmd.Dir}, cmd.ReadOnly...)

	entries, _ := os.ReadDir(rt.p.rootfs)
	for _, e := range entries {
		switch e.Name() {
		case "proc", "dev", "sys", "tmp", "run", "home", ".complete":
		default:
			args = rt.imageArgs(args, e, e.Name(), binds)
		}
	}

	args = append(args, "--proc", "/proc", "--dev", "/dev", "--size", strconv.Itoa(tmpBytes), "--tmpfs", "/tmp", "--bind", cmd.Dir, cmd.Dir)
	for _, ro := range cmd.ReadOnly {
		args = append(args, "--ro-bind", ro, ro)
	}

	args = append(args,
		"--ro-bind", filepath.Join(dir, "run"), guestRunDir,
		"--ro-bind", filepath.Join(dir, "extensions"), guestExtDir,
		"--ro-bind", rt.p.self, guestRelay,
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

	ln.SetUnlinkOnClose(false)

	return ln, nil
}

func memfd(name string, data []byte) (*os.File, error) {
	fd, err := unix.MemfdCreate(name, unix.MFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("memfd %s: %w", name, err)
	}

	f := os.NewFile(uintptr(fd), name)
	if _, err := f.WriteAt(data, 0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("memfd %s: %w", name, err)
	}

	return f, nil
}

func (rt *Runtime) sandbox(ctx context.Context, cmd *Command, dir string, cg *execCgroup, hostOut *budget, log *slog.Logger) (Result, error) {
	argsFile, err := memfd("rocketclaw-bwrap-args", []byte(strings.Join(rt.bwrapArgs(cmd, dir), "\x00")+"\x00"))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = argsFile.Close() }()

	seccompFile, err := memfd("rocketclaw-seccomp", rt.p.seccomp)
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
		Path: rt.p.bwrap,
		Args: []string{
			"bwrap", "--args", "3", "--",
			guestRelay, "init", strconv.Itoa(fileBytes), strconv.Itoa(guestNoFile), strconv.Itoa(readyFD), "--",
			"/bin/bash", "--noprofile", "--norc", guestScript,
		},
		Env:        []string{},
		Stdout:     outW,
		Stderr:     outW,
		ExtraFiles: []*os.File{argsFile, seccompFile, readyW},
		SysProcAttr: &syscall.SysProcAttr{
			UseCgroupFD: true,
			CgroupFD:    cg.fd,
			Pdeathsig:   syscall.SIGKILL,
		},
	}

	if cmd.Stdin != "" {
		c.Stdin = strings.NewReader(cmd.Stdin)
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

			got, crossed := out.take(n)
			if crossed {
				log.Warn(limitReached, "limit", "output", "limit_bytes", outputBytes)
				close(out.over)
			}

			output.Write(chunk[:got])

			if err != nil {
				return
			}
		}
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

		log.Warn(limitReached, "limit", "timeout", "timeout", cmd.Timeout)
	case <-ctx.Done():
		timedOut = true
	case <-out.over:
	case <-hostOut.over:
	}

	errKill := killCgroup(cg.path)

	<-exited
	<-copied

	if errKill != nil {
		return Result{}, errKill
	}

	ready, _ := io.ReadAll(io.LimitReader(readyR, 2))
	if string(ready) != "1" {
		return Result{}, fmt.Errorf("the sandbox did not start: %s", strings.TrimSpace(output.String()))
	}

	res := Result{Output: output.Bytes(), TimedOut: timedOut, Truncated: out.spent() || hostOut.spent()}

	oomKills, _ := cgroupKey(cg.path, "memory.events", "oom_kill")
	processLimitHits, _ := cgroupKey(cg.path, "pids.events", "max")

	if oomKills > 0 {
		log.Warn(limitReached, "limit", "memory", "limit_bytes", memoryBytes, "oom_kills", oomKills)
	}

	if processLimitHits > 0 {
		log.Warn(limitReached, "limit", "processes", "limit_processes", tasks, "refused_forks", processLimitHits)
	}

	switch {
	case res.Truncated:
		res.Output = fmt.Appendf(res.Output, "\n[emulated shell: output stopped at %d MiB]\n", outputBytes>>20)
	case oomKills > 0 || processLimitHits > 0:
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
