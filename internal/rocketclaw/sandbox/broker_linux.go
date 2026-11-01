//go:build amd64 || arm64

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Rocketable/platform/internal/rocketclaw/sandbox/wire"
)

// startTimeout bounds how long a connection may wait before sending InvokeStart.
const startTimeout = 10 * time.Second

// broker serves one command's socket. Every connection is untrusted: it must come from a process in the
// command's cgroup, name one of names, and stay within the budgets.
type broker struct {
	execID string
	cgroup string // the command's cgroup as /proc/<pid>/cgroup shows it
	names  []string
	host   Host
	out    *budget // host command output, against outputBytes
	ln     *net.UnixListener
	slots  chan struct{}

	mu          sync.Mutex
	invocations int
	conns       map[*net.UnixConn]struct{}
	closed      bool
	wg          sync.WaitGroup
}

// serve accepts connections until the listener closes. ctx ends with the command.
func (b *broker) serve(ctx context.Context) {
	for {
		conn, err := b.ln.AcceptUnix()
		if err != nil {
			return
		}

		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()

			_ = conn.Close()

			return
		}

		b.conns[conn] = struct{}{}
		b.wg.Add(1)
		b.mu.Unlock()

		go func() {
			defer b.wg.Done()

			b.handle(ctx, conn)

			b.mu.Lock()
			delete(b.conns, conn)
			b.mu.Unlock()

			_ = conn.Close()
		}()
	}
}

// close stops accepting, closes every connection, and waits for the connection goroutines. A host command
// still running is abandoned with its context cancelled (spec §6.5).
func (b *broker) close() {
	b.mu.Lock()
	b.closed = true
	_ = b.ln.Close()

	for c := range b.conns {
		_ = c.Close()
	}
	b.mu.Unlock()

	b.wg.Wait()
}

// peerInExec checks the connecting process against the command's cgroup: reaching the socket is not proof.
func (b *broker) peerInExec(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("peer: %w", err)
	}

	var cred *unix.Ucred

	errCred := raw.Control(func(fd uintptr) { cred, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err := errors.Join(errCred, err); err != nil {
		return fmt.Errorf("peer credentials: %w", err)
	}

	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", cred.Pid))
	if err != nil {
		return fmt.Errorf("peer cgroup: %w", err)
	}

	if strings.TrimSpace(string(data)) != "0::"+b.cgroup {
		return fmt.Errorf("peer pid %d is outside the command's cgroup", cred.Pid)
	}

	return nil
}

func (b *broker) handle(ctx context.Context, conn *net.UnixConn) {
	w, r := wire.NewWriter(conn), wire.NewReader(conn)

	if b.peerInExec(conn) != nil {
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(startTimeout))

	f, err := r.Read()
	if err != nil || f.GetInvokeStart() == nil {
		exit(w, 126, "rocketclaw: malformed host command request")
		return
	}

	_ = conn.SetReadDeadline(time.Time{})

	start := f.GetInvokeStart()
	name := string(start.GetName())

	switch {
	case string(start.GetExecId()) != b.execID:
		exit(w, 126, "rocketclaw: host command request for another command")
		return
	case !slices.Contains(b.names, name):
		exit(w, 127, safeName(name)+": command not found")
		return
	}

	b.mu.Lock()
	b.invocations++
	n := b.invocations
	b.mu.Unlock()

	if n > hostInvocations {
		exit(w, 126, name+": too many host commands in one shell command")
		return
	}

	select {
	case b.slots <- struct{}{}:
		defer func() { <-b.slots }()
	case <-ctx.Done():
		return
	}

	b.invoke(ctx, conn, r, w, name, start)
}

// safeName echoes a guest-supplied name only when it is a plausible command name.
func safeName(name string) string {
	if commandName.MatchString(name) {
		return name
	}

	return "rocketclaw"
}

func exit(w *wire.Writer, code int, msg string) {
	e := &wire.InvokeExit{Code: uint32(code)}
	if msg != "" {
		e.Message = []byte(msg + "\n")
	}

	_ = w.Write(&wire.Frame{Body: &wire.Frame_InvokeExit{InvokeExit: e}})
}

// errRevoked fails writes of a host command that outlived its invocation.
var errRevoked = errors.New("host command ended")

// invocation is one running host command; its output stops when it ends.
type invocation struct {
	mu      sync.Mutex
	revoked bool
}

func (inv *invocation) live() error {
	inv.mu.Lock()
	defer inv.mu.Unlock()

	if inv.revoked {
		return errRevoked
	}

	return nil
}

func (inv *invocation) revoke() {
	inv.mu.Lock()
	inv.revoked = true
	inv.mu.Unlock()
}

func (b *broker) invoke(ctx context.Context, conn *net.UnixConn, r *wire.Reader, w *wire.Writer, name string, start *wire.InvokeStart) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	inv := &invocation{}
	defer inv.revoke()

	stdinR, stdinW := io.Pipe()
	defer func() { _ = stdinR.Close() }()

	// Guest frames become a stdin stream. A pipe write blocks until the host command reads, which stops
	// reading the socket, which blocks the guest relay: backpressure end to end.
	go func() {
		var total int

		for {
			f, err := r.Read()
			if err != nil {
				stdinW.CloseWithError(io.ErrUnexpectedEOF)
				cancel()

				return
			}

			switch body := f.GetBody().(type) {
			case *wire.Frame_StdinChunk:
				if total += len(body.StdinChunk); total > hostInputBytes {
					stdinW.CloseWithError(errors.New("host command input limit exceeded"))
					cancel()

					return
				}

				// Once the host command stops reading, input is dropped, so the guest never blocks writing.
				_, _ = stdinW.Write(body.StdinChunk)
			case *wire.Frame_StdinEof:
				_ = stdinW.Close()
			default: // Cancel, or a frame a guest may not send
				stdinW.CloseWithError(context.Canceled)
				cancel()

				return
			}
		}
	}()

	args := make([]string, len(start.GetArgs()))
	for i, a := range start.GetArgs() {
		args[i] = string(a)
	}

	stdout := &frameWriter{w: w, inv: inv, out: b.out}
	stderr := &frameWriter{w: w, stderr: true, inv: inv, out: b.out}
	result := make(chan int, 1)

	go func() { result <- b.host.Command(ctx, name, args, string(start.GetCwd()), stdinR, stdout, stderr) }()

	select {
	case code := <-result:
		inv.revoke()

		if code < 0 || code > 255 {
			code = 126
		}

		exit(w, code, "")

		_ = conn.CloseWrite()
	case <-ctx.Done():
		// Go cannot stop a host command that ignores its context (spec §6.5); its output is revoked now.
		inv.revoke()
	}
}

// frameWriter sends host command output as bounded frames. The socket blocks when the guest stops reading.
type frameWriter struct {
	w      *wire.Writer
	stderr bool
	inv    *invocation
	out    *budget
}

func (f *frameWriter) Write(p []byte) (int, error) {
	written := 0

	for len(p) > 0 {
		if err := f.inv.live(); err != nil {
			return written, err
		}

		chunk := p[:f.out.take(min(len(p), wire.ChunkSize))]
		if len(chunk) == 0 {
			return written, errors.New("output limit exceeded")
		}

		frame := &wire.Frame{Body: &wire.Frame_StdoutChunk{StdoutChunk: chunk}}
		if f.stderr {
			frame.Body = &wire.Frame_StderrChunk{StderrChunk: chunk}
		}

		if err := f.w.Write(frame); err != nil {
			return written, fmt.Errorf("send output: %w", err)
		}

		written += len(chunk)
		p = p[len(chunk):]
	}

	return written, nil
}

// budget is a byte allowance shared by concurrent writers; the first overrun closes over.
type budget struct {
	mu        sync.Mutex
	remaining int
	exceeded  bool
	over      chan struct{}
}

func newBudget(n int) *budget { return &budget{remaining: n, over: make(chan struct{})} }

// take grants as much of n as remains; granting less marks the budget exceeded.
func (b *budget) take(n int) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	got := min(n, b.remaining)
	b.remaining -= got

	if got < n && !b.exceeded {
		b.exceeded = true
		close(b.over)
	}

	return got
}

func (b *budget) spent() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.exceeded
}
