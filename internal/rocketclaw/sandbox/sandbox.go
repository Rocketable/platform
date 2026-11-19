package sandbox

//go:generate go run ./rootfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"
)

type Host interface {
	Command(ctx context.Context, log *slog.Logger, name string, args []string, cwd string, stdin io.Reader, stdout, stderr io.Writer) int
}

type Command struct {
	Script   string
	Cwd      string
	Dir      string
	ReadOnly []string
	Env      []string
	Commands []string
	Timeout  time.Duration
}

type Result struct {
	Output    []byte
	ExitCode  int
	TimedOut  bool
	Truncated bool
}

const Curl = "curl"

var commandName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+-]{0,63}$`)

func CheckCommandName(name string) error {
	switch name {
	case Curl:
		return errors.New("curl is the emulated shell's own")
	case "if", "then", "else", "elif", "fi", "case", "esac", "for", "select", "while", "until", "do", "done", "in", "function", "time", "coproc":
		return fmt.Errorf("%s is a Bash reserved word", name)
	}

	if !commandName.MatchString(name) || strings.HasPrefix(name, "rocketclaw") {
		return fmt.Errorf("%q: names are 1-64 of A-Z a-z 0-9 _ . + -, start with a letter, digit or _, and do not start with rocketclaw", name)
	}

	return nil
}

var errNotReady = errors.New("sandbox not prepared")

type None struct{}

func (None) Prepare(context.Context, string, *slog.Logger) *Runtime { return &Runtime{} }

var ErrPreparing = errors.New("the first start builds the sandbox's Ubuntu image, which takes a minute or two; commands wait for it")

func (rt *Runtime) Err() error {
	if rt.p == nil {
		return errNotReady
	}

	select {
	case <-rt.p.ready:
		return rt.p.err
	default:
	}

	rt.p.mu.Lock()
	defer rt.p.mu.Unlock()

	if rt.p.retry != nil {
		return rt.p.retry
	}

	return ErrPreparing
}

func (rt *Runtime) Wait(ctx context.Context) error {
	if rt.p == nil {
		return errNotReady
	}

	select {
	case <-rt.p.ready:
		return rt.p.err
	case <-ctx.Done():
		return fmt.Errorf("waiting for the sandbox: %w", ctx.Err())
	}
}

type probeHost struct{}

func (probeHost) Command(context.Context, *slog.Logger, string, []string, string, io.Reader, io.Writer, io.Writer) int {
	return 127
}
