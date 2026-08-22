package rocketcode

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
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
	shellHeadMaxLines     = 2000
	shellTimeoutGrace     = 100 * time.Millisecond
	shellForceKillTimeout = 3 * time.Second
)

type bashParams struct {
	Command            string `json:"command"`
	TimeoutMillisecond int    `json:"timeout_ms"`
	Workdir            string `json:"workdir"`
	Description        string `json:"description"`
}

// BashCommand is the public shape of the workspace bash tool input.
type BashCommand struct {
	Command            string
	TimeoutMillisecond int
	Workdir            string
	Description        string
}

// BashResult is the result of running a workspace bash command.
type BashResult struct {
	HeadOutput string
	FullOutput string
	ErrorCode  string
	Success    bool
}

// String returns HeadOutput so printable forms show the truncated head.
func (r BashResult) String() string {
	return r.HeadOutput
}

// Output is the printable form (HeadOutput), kept for existing callers.
func (r BashResult) Output() string {
	return r.HeadOutput
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

// RunBash runs command through the same implementation used by RocketCode's bash tool.
func RunBash(ctx context.Context, root *os.Root, shellTempDir string, shellEnv map[string]string, command BashCommand) (BashResult, error) {
	if root == nil {
		return BashResult{}, errors.New("root is required")
	}

	shellTemp, err := newShellTempConfig(root, shellTempDir)
	if err != nil {
		return BashResult{}, err
	}

	env, err := shellEnvList(shellEnv)
	if err != nil {
		return BashResult{}, err
	}

	sss := newSandboxedShellSystem(root, &shellTemp, env, DefaultShellCommand)

	return sss.runBash(ctx, bashParams(command)), nil
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

func (sss *sandboxedShellSystem) Bash(ctx context.Context, params bashParams) BashResult {
	return sss.runBash(ctx, params)
}

func (sss *sandboxedShellSystem) runBash(ctx context.Context, params bashParams) BashResult {
	sss.mu.Lock()
	defer sss.mu.Unlock()

	if strings.TrimSpace(params.Command) == "" {
		return bashFailure("command is required")
	}

	if params.TimeoutMillisecond < 0 {
		return bashFailure(fmt.Sprintf("Invalid timeout_ms value: %d. timeout_ms must be a positive number.", params.TimeoutMillisecond))
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
			return bashFailure(fmt.Errorf("resolve workdir %q: %w", workdir, err).Error())
		}

		info, err := sss.root.Stat(params.Workdir)
		if err != nil {
			return bashFailure(fmt.Errorf("resolve workdir %q: %w", params.Workdir, err).Error())
		}

		if !info.IsDir() {
			return bashFailure(fmt.Errorf("resolve workdir %q: not a directory", params.Workdir).Error())
		}

		root, err := sss.root.OpenRoot(params.Workdir)
		if err != nil {
			return bashFailure(fmt.Errorf("resolve workdir %q: %w", params.Workdir, err).Error())
		}

		hostDir = root.Name()
		cleanup = func() { _ = root.Close() }
	}

	defer cleanup()

	if denied := sss.deniedBashPath(params.Command, hostDir); denied != "" {
		return bashFailure(denied)
	}

	if err := sss.shellTemp.ensureTempDir(sss.root); err != nil {
		return bashFailure(err.Error())
	}

	commandCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(timeoutMillisecond)*time.Millisecond+shellTimeoutGrace)
	defer cancel()

	timedOut := false

	shell, args := sss.shellCommand(params.Command)
	if strings.TrimSpace(shell) == "" {
		return bashFailure("shell command path is required")
	}

	cmd := exec.CommandContext(commandCtx, shell, args...)
	cmd.Dir = hostDir

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

	head, truncated := firstLines(full, shellHeadMaxLines)
	if truncated {
		head = strings.TrimRight(head, "\n") + "\n\n...output truncated...\n\nFull output is on this result's full_output field (e.g. result.full_output), not a file.\n"
	}

	return BashResult{
		HeadOutput: head,
		FullOutput: full,
		ErrorCode:  errorCode,
		Success:    err == nil && !timedOut,
	}
}

func bashFailure(message string) BashResult {
	if message == "" {
		message = "(no output)"
	}

	return BashResult{
		HeadOutput: message,
		FullOutput: message,
		ErrorCode:  "error",
		Success:    false,
	}
}

func firstLines(text string, maxLines int) (string, bool) {
	if maxLines <= 0 || text == "" {
		return text, false
	}

	var out strings.Builder

	reader := bufio.NewReader(strings.NewReader(text))
	for range maxLines {
		line, err := reader.ReadString('\n')
		out.WriteString(line)

		if err != nil {
			return out.String(), false
		}
	}

	// More content remains after the head window.
	if _, err := reader.ReadByte(); err == nil {
		return out.String(), true
	}

	return out.String(), false
}

func (sss *sandboxedShellSystem) deniedBashPath(command, hostDir string) string {
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
		if !isBashFileCommand(name) {
			return true
		}

		for _, arg := range args[1:] {
			pathArg, ok := staticBashPathArg(name, arg)
			if !ok {
				continue
			}

			resolved := resolveBashPath(hostDir, pathArg)
			if isDeniedEnvPath(resolved) {
				denied = "bash command denied: " + deniedEnvAccessMessage(pathArg)
				return false
			}

			if !pathWithinRoot(rootName, resolved) {
				denied = "bash command denied: external path access is blocked: " + pathArg
				return false
			}
		}

		return true
	})

	return denied
}

func isBashFileCommand(name string) bool {
	switch name {
	case "cat", "cd", "chmod", "chown", "cp", "grep", "head", "less", "ln", "mkdir", "more", "mv", "pushd", "rm", "tail", "touch":
		return true
	default:
		return false
	}
}

func staticBashPathArg(command, arg string) (string, bool) {
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

func resolveBashPath(hostDir, arg string) string {
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
