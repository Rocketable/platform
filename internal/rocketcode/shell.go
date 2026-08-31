package rocketcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

const (
	defaultShellTimeout   = 2 * 60 * 1000
	shellTimeoutGrace     = 100 * time.Millisecond
	shellForceKillTimeout = 3 * time.Second
)

// ShellParams is the workspace shell and python3 host-tool input.
type ShellParams struct {
	Command            string `json:"command"`
	TimeoutMillisecond int    `json:"timeout_ms"`
	// Workdir matches OpenCode v2 ShellTool.Input.workdir
	// (packages/core/src/tool/plugin/shell.ts). Env is inherited, not a param.
	Workdir     string `json:"workdir"`
	Description string `json:"description"`
}

// ShellResult is the result of running a workspace shell command.
type ShellResult struct {
	Output    string
	ErrorCode string
	Success   bool
}

func (r ShellResult) String() string {
	return r.Output
}

type sandboxedShellSystem struct {
	mu           sync.Mutex
	root         *os.Root
	shellTemp    shellTempConfig
	env          []string
	shellCommand ShellCommandFunc
}

func newSandboxedShellSystem(root *os.Root, shellTemp *shellTempConfig, env []string, shellCommand ShellCommandFunc) *sandboxedShellSystem {
	return &sandboxedShellSystem{
		mu:           sync.Mutex{},
		root:         root,
		shellTemp:    *shellTemp,
		env:          slices.Clone(env),
		shellCommand: shellCommand,
	}
}

// RunShell runs command through the same implementation used by RocketCode's shell tool.
func RunShell(ctx context.Context, root *os.Root, shellTempDir string, shellEnv map[string]string, command ShellParams) (ShellResult, error) {
	if root == nil {
		return ShellResult{}, errors.New("root is required")
	}

	shellTemp, err := newShellTempConfig(root, shellTempDir)
	if err != nil {
		return ShellResult{}, err
	}

	env, err := shellEnvList(shellEnv)
	if err != nil {
		return ShellResult{}, err
	}

	sss := newSandboxedShellSystem(root, &shellTemp, env, DefaultShellCommand)

	return sss.shell(ctx, command), nil
}

func shellEnvList(shellEnv map[string]string) ([]string, error) {
	shellEnvKeys := slices.Sorted(maps.Keys(shellEnv))

	env := make([]string, 0, len(shellEnvKeys))
	for _, key := range shellEnvKeys {
		if key == "TMPDIR" {
			continue
		}

		value := shellEnv[key]
		if key == "" {
			return nil, errors.New("shell env key is required")
		}

		if strings.Contains(key, "=") {
			return nil, fmt.Errorf("shell env key %q must not contain =", key)
		}

		if strings.Contains(key, "\x00") || strings.Contains(value, "\x00") {
			return nil, fmt.Errorf("shell env %q must not contain NUL", key)
		}

		env = append(env, key+"="+value)
	}

	return env, nil
}

func (sss *sandboxedShellSystem) shell(ctx context.Context, params ShellParams) ShellResult {
	program, args := sss.shellCommand(params.Command)

	return sss.run(ctx, params, program, args, nil, true)
}

func (sss *sandboxedShellSystem) python3(ctx context.Context, params ShellParams) ShellResult {
	stdin := strings.NewReader(params.Command)

	return sss.run(ctx, params, "python3", []string{"-"}, stdin, false)
}

func (sss *sandboxedShellSystem) run(ctx context.Context, params ShellParams, program string, args []string, stdin io.Reader, scanPaths bool) ShellResult {
	sss.mu.Lock()
	defer sss.mu.Unlock()

	if strings.TrimSpace(params.Command) == "" {
		return shellFailure("command is required")
	}

	if params.TimeoutMillisecond < 0 {
		return shellFailure(fmt.Sprintf("Invalid timeout_ms value: %d. timeout_ms must be a positive number.", params.TimeoutMillisecond))
	}

	timeoutMillisecond := params.TimeoutMillisecond
	if timeoutMillisecond == 0 {
		timeoutMillisecond = defaultShellTimeout
	}

	hostDir := sss.root.Name()
	cleanup := func() {}

	if params.Workdir != "" {
		workdir := params.Workdir

		var err error

		params.Workdir, err = normalizeRootName(sss.root, workdir)
		if err != nil {
			return shellFailure(fmt.Errorf("resolve workdir %q: %w", workdir, err).Error())
		}

		info, err := sss.root.Stat(params.Workdir)
		if err != nil {
			return shellFailure(fmt.Errorf("resolve workdir %q: %w", params.Workdir, err).Error())
		}

		if !info.IsDir() {
			return shellFailure(fmt.Errorf("resolve workdir %q: not a directory", params.Workdir).Error())
		}

		root, err := sss.root.OpenRoot(params.Workdir)
		if err != nil {
			return shellFailure(fmt.Errorf("resolve workdir %q: %w", params.Workdir, err).Error())
		}

		hostDir = root.Name()
		cleanup = func() { _ = root.Close() }
	}

	defer cleanup()

	if scanPaths {
		if denied := sss.deniedShellPath(params.Command, hostDir); denied != "" {
			return shellFailure(denied)
		}
	}

	if err := sss.shellTemp.ensureTempDir(sss.root); err != nil {
		return shellFailure(err.Error())
	}

	commandCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(timeoutMillisecond)*time.Millisecond+shellTimeoutGrace)
	defer cancel()

	timedOut := false

	if strings.TrimSpace(program) == "" {
		return shellFailure("shell command path is required")
	}

	cmd := exec.CommandContext(commandCtx, program, args...)
	cmd.Dir = hostDir
	cmd.Stdin = stdin

	cmd.Env = append(os.Environ(), sss.env...)
	cmd.Env = append(cmd.Env, "TMPDIR="+sss.shellTemp.tmpDir)

	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = cmd.Stdout
	cmd.Cancel = func() error {
		timedOut = true

		if cmd.Process == nil {
			return nil
		}

		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("terminate command group: %w", err)
		}

		time.AfterFunc(shellForceKillTimeout, func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })

		return nil
	}

	var sysProcAttr syscall.SysProcAttr

	sysProcAttr.Setpgid = true
	cmd.SysProcAttr = &sysProcAttr

	stdoutBuf, _ := cmd.Stdout.(*bytes.Buffer)
	err := cmd.Run()

	full := stdoutBuf.String()
	if full == "" {
		full = "(no output)"
	}

	errorCode := ""
	if timedOut {
		errorCode = "timeout"
	} else if errStatus, ok := errors.AsType[*exec.ExitError](err); ok && errStatus.ExitCode() > 0 {
		errorCode = strconv.Itoa(errStatus.ExitCode())
	} else if err != nil {
		errorCode = "error"
	}

	return ShellResult{
		Output:    full,
		ErrorCode: errorCode,
		Success:   err == nil && !timedOut,
	}
}

func shellFailure(message string) ShellResult {
	if message == "" {
		message = "(no output)"
	}

	return ShellResult{
		Output:    message,
		ErrorCode: "error",
		Success:   false,
	}
}

func (sss *sandboxedShellSystem) deniedShellPath(command, hostDir string) string {
	parser := syntax.NewParser()

	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return ""
	}

	printer := syntax.NewPrinter()
	rootName := filepath.Clean(sss.root.Name())
	denied := ""

	syntax.Walk(file, func(node syntax.Node) bool {
		if denied != "" {
			return false
		}

		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}

		args := make([]string, 0, len(call.Args))
		for _, arg := range call.Args {
			var buf bytes.Buffer
			if err := printer.Print(&buf, arg); err != nil {
				continue
			}

			args = append(args, strings.TrimSpace(buf.String()))
		}

		name := filepath.Base(unquoteShellArg(args[0]))
		if !isShellFileCommand(name) {
			return true
		}

		for _, arg := range args[1:] {
			pathArg, ok := staticShellPathArg(name, arg)
			if !ok {
				continue
			}

			resolved := resolveShellPath(hostDir, pathArg)
			if isDeniedEnvPath(resolved) {
				denied = "shell command denied: " + deniedEnvAccessMessage(pathArg)
				return false
			}

			if !pathWithinRoot(rootName, resolved) {
				denied = "shell command denied: external path access is blocked: " + pathArg
				return false
			}
		}

		return true
	})

	return denied
}

func isShellFileCommand(name string) bool {
	switch name {
	case "cat", "cd", "chmod", "chown", "cp", "grep", "head", "less", "ln", "mkdir", "more", "mv", "pushd", "rm", "tail", "touch":
		return true
	default:
		return false
	}
}

func staticShellPathArg(command, arg string) (string, bool) {
	arg = unquoteShellArg(arg)
	if arg == "" || arg == "--" {
		return "", false
	}

	if strings.HasPrefix(arg, "-") || command == "chmod" && strings.HasPrefix(arg, "+") {
		return "", false
	}

	if strings.ContainsAny(arg, "$`(){};|&<>") {
		return "", false
	}

	return arg, true
}

func unquoteShellArg(arg string) string {
	if len(arg) < 2 {
		return arg
	}

	first := arg[0]

	last := arg[len(arg)-1]
	if (first == '\'' || first == '"') && first == last {
		return arg[1 : len(arg)-1]
	}

	return arg
}

func resolveShellPath(hostDir, arg string) string {
	if strings.HasPrefix(arg, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Clean(filepath.Join(home, arg[2:]))
		}
	}

	if arg == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Clean(home)
		}
	}

	if filepath.IsAbs(arg) {
		return filepath.Clean(arg)
	}

	return filepath.Clean(filepath.Join(hostDir, arg))
}

func pathWithinRoot(rootName, path string) bool {
	rel, err := filepath.Rel(rootName, path)
	if err != nil {
		return false
	}

	return rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
