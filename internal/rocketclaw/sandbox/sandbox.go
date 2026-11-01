// Package sandbox runs RocketClaw's emulated shell: real Bash and Linux programs in a bubblewrap sandbox
// with no network, no host files but the agent's folders, cgroup limits and seccomp
// (docs/specs/2026-11-01-linux-native-just-bash-spec.md). Commands an agent declares, and curl, run on the
// host through Host; the RocketClaw binary, mounted read-only, relays them out of the sandbox.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Host runs, outside the sandbox, what an emulated shell may ask of the host: the agent's commands and curl.
// Everything it receives comes from the guest and is untrusted.
type Host interface {
	// Command runs name with args and stdin, writing its output, and returns its exit status. cwd is the
	// guest's working directory, a guest path.
	Command(ctx context.Context, name string, args []string, cwd string, stdin io.Reader, stdout, stderr io.Writer) int
}

// Command is one emulated shell command line.
type Command struct {
	Script   string
	Cwd      string        // guest working directory; folders show at their host paths
	Dir      string        // the agent's writable folder
	ReadOnly []string      // folders shown read-only
	Env      []string      // the guest's whole environment; never RocketClaw's
	Commands []string      // names Host runs; the guest reaches only these and curl
	Timeout  time.Duration // whole command
}

// Result is the outcome of a shell that started: stdout and stderr together, as host bash gives them.
type Result struct {
	Output   []byte
	ExitCode int
	TimedOut bool
	// Truncated means the output limit stopped the command.
	Truncated bool
}

// Curl is the managed HTTP client every emulated shell has; Host runs it like a command.
const Curl = "curl"

// commandName is a name a guest can call: a shell word that is not a path, option, or assignment.
var commandName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+-]{0,63}$`)

// CheckCommandName reports why an agent cannot declare a command named name: it becomes a file and a Bash
// function in the sandbox.
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

// errNotReady is the zero Runtime's error: RocketClaw never prepared a sandbox.
var errNotReady = errors.New("sandbox not prepared")

// Runtime runs emulated shell commands; each platform defines its fields. The zero value is unavailable:
// every Run fails, never falling back to host Bash.

// Err reports why commands cannot run, or nil when the sandbox is ready.
func (rt *Runtime) Err() error {
	switch {
	case rt.err != nil:
		return rt.err
	case rt.rootfs == "":
		return errNotReady
	}

	return nil
}

// probeHost runs no host commands.
type probeHost struct{}

func (probeHost) Command(context.Context, string, []string, string, io.Reader, io.Writer, io.Writer) int {
	return 127
}
