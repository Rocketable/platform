//go:build amd64 || arm64

package sandbox

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/Rocketable/platform/internal/rocketclaw/sandbox/wire"
)

// Guest is RocketClaw's entry point inside a sandbox, where the binary is mounted read-only at guestRelay.
// It reports whether this process runs there and, if so, its exit status: "init" starts the sandbox's Bash,
// and the /extensions/<name> links relay <name> to the host.
func Guest(args []string) (code int, ok bool) {
	if exe, err := os.Executable(); err != nil || exe != guestRelay {
		return 0, false
	}

	if len(args) > 1 && args[1] == "init" {
		fmt.Fprintln(os.Stderr, "rocketclaw:", guestInit(args[2:]))
		return 126, true
	}

	return relay(filepath.Base(args[0]), args[1:]), true
}

// guestInit sets resource limits, tells the host the sandbox is up through the ready descriptor, marks every
// inherited descriptor close-on-exec, and execs the command. args: FSIZE NOFILE READYFD -- argv...
func guestInit(args []string) error {
	if len(args) < 5 || args[3] != "--" {
		return errors.New("usage: rocketclaw init FSIZE NOFILE READYFD -- COMMAND [ARG...]")
	}

	var n [3]uint64
	for i := range n {
		v, err := strconv.ParseUint(args[i], 10, 63)
		if err != nil {
			return fmt.Errorf("init: %w", err)
		}

		n[i] = v
	}

	for _, l := range []struct {
		res int
		max uint64
	}{{unix.RLIMIT_FSIZE, n[0]}, {unix.RLIMIT_NOFILE, n[1]}, {unix.RLIMIT_CORE, 0}} {
		if err := unix.Setrlimit(l.res, &unix.Rlimit{Cur: l.max, Max: l.max}); err != nil {
			return fmt.Errorf("setrlimit: %w", err)
		}
	}

	unix.Umask(0o022)

	if _, err := unix.Write(int(n[2]), []byte{'1'}); err != nil {
		return fmt.Errorf("ready: %w", err)
	}

	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return fmt.Errorf("close_range: %w", err)
	}

	argv := args[4:]
	err := unix.Exec(argv[0], argv, os.Environ())

	// The ready descriptor closes on a successful exec; it is still open here, so the host learns Bash never ran.
	_, _ = unix.Write(int(n[2]), []byte{'E'})

	return fmt.Errorf("exec %s: %w", argv[0], err)
}

// relay runs host command name with args and returns its exit status; 126 when it cannot reach the host.
func relay(name string, args []string) int {
	code, err := relayCommand(name, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: host command bridge failed\n", name)
		return 126
	}

	return code
}

func relayCommand(name string, args []string) (int, error) {
	conn, err := net.Dial("unix", guestSocket)
	if err != nil {
		return 0, fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close() }()

	execID, err := os.ReadFile(guestExecID)
	if err != nil {
		return 0, fmt.Errorf("read exec id: %w", err)
	}

	cwd, _ := os.Getwd()

	start := &wire.InvokeStart{ExecId: execID, Name: []byte(name), Cwd: []byte(cwd)}
	for _, a := range args {
		start.Args = append(start.Args, []byte(a))
	}

	w := wire.NewWriter(conn)
	if err := w.Write(&wire.Frame{Body: &wire.Frame_InvokeStart{InvokeStart: start}}); err != nil {
		return 0, fmt.Errorf("send start: %w", err)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		sig := <-signals
		_ = w.Write(&wire.Frame{Body: &wire.Frame_Cancel{Cancel: &wire.Cancel{Reason: []byte(sig.String())}}})
		os.Exit(128 + int(sig.(syscall.Signal)))
	}()

	go pumpStdin(w)

	r := wire.NewReader(conn)
	for {
		f, err := r.Read()
		if err != nil {
			return 0, fmt.Errorf("read frame: %w", err)
		}

		switch body := f.GetBody().(type) {
		case *wire.Frame_StdoutChunk:
			// A closed stdout pipe raises SIGPIPE here, ending the relay as it would any command.
			if _, err := os.Stdout.Write(body.StdoutChunk); err != nil {
				return 0, fmt.Errorf("stdout: %w", err)
			}
		case *wire.Frame_StderrChunk:
			_, _ = os.Stderr.Write(body.StderrChunk)
		case *wire.Frame_InvokeExit:
			_, _ = os.Stderr.Write(body.InvokeExit.GetMessage())
			return int(body.InvokeExit.GetCode()), nil
		default:
			return 0, fmt.Errorf("unexpected frame %T", body)
		}
	}
}

// pumpStdin streams stdin as chunks, then StdinEOF. Each write blocks while the host is not reading.
func pumpStdin(w *wire.Writer) {
	buf := make([]byte, wire.ChunkSize)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 && w.Write(&wire.Frame{Body: &wire.Frame_StdinChunk{StdinChunk: buf[:n]}}) != nil {
			return
		}

		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.EBADF) {
				_ = w.Write(&wire.Frame{Body: &wire.Frame_StdinEof{StdinEof: &wire.StdinEOF{}}})
			}

			return
		}
	}
}
