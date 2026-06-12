package rocketcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

const (
	defaultShellTimeout   = 2 * 60 * 1000
	shellMaxLines         = 2000
	shellTempFileTTL      = 6 * time.Hour
	shellTimeoutGrace     = 100 * time.Millisecond
	shellForceKillTimeout = 3 * time.Second
)

type bashParams struct {
	Command     string
	Timeout     int
	Workdir     string
	Description string
}

// BashCommand is the public shape of the workspace bash tool input.
type BashCommand struct {
	Command     string
	Timeout     int
	Workdir     string
	Description string
}

// BashResult is the result of running a workspace bash command.
type BashResult struct {
	Output  string
	Success bool
}

type temporaryFile string

type creationTime time.Time

type sandboxedShellSystem struct {
	mu          sync.Mutex
	root        *os.Root
	shellOutput shellOutputConfig
	env         []string
	tempFiles   map[temporaryFile]creationTime
	bash        sandboxedBash
	useSandbox  bool
}

func newSandboxedShellSystem(root *os.Root, shellOutput *shellOutputConfig, env []string, useSandbox bool) *sandboxedShellSystem {
	return &sandboxedShellSystem{
		mu:          sync.Mutex{},
		root:        root,
		shellOutput: *shellOutput,
		env:         slices.Clone(env),
		tempFiles:   map[temporaryFile]creationTime{},
		bash:        newSandboxedBash(root, *shellOutput, env),
		useSandbox:  useSandbox,
	}
}

// RunBash runs command through the same implementation used by RocketCode's bash tool.
func RunBash(ctx context.Context, root *os.Root, shellOutputDir string, shellEnv map[string]string, useSandbox bool, command BashCommand) (BashResult, error) {
	if root == nil {
		return BashResult{}, errors.New("root is required")
	}

	shellOutput, err := newShellOutputConfig(root, shellOutputDir)
	if err != nil {
		return BashResult{}, err
	}

	env, err := shellEnvList(shellEnv)
	if err != nil {
		return BashResult{}, err
	}

	sss := newSandboxedShellSystem(root, &shellOutput, env, useSandbox)
	output, success := sss.runBash(ctx, bashParams(command))

	return BashResult{Output: output, Success: success}, nil
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

func (sss *sandboxedShellSystem) Bash(ctx context.Context, params bashParams) string {
	output, _ := sss.runBash(ctx, params)
	return output
}

func (sss *sandboxedShellSystem) runBash(ctx context.Context, params bashParams) (string, bool) {
	sss.mu.Lock()
	defer sss.mu.Unlock()

	if strings.TrimSpace(params.Command) == "" {
		return "command is required", false
	}

	if params.Timeout < 0 {
		return fmt.Sprintf("Invalid timeout value: %d. Timeout must be a positive number.", params.Timeout), false
	}

	for file, created := range sss.tempFiles {
		if time.Since(time.Time(created)) <= shellTempFileTTL {
			continue
		}

		_ = sss.root.Remove(string(file))
		delete(sss.tempFiles, file)
	}

	timeout := params.Timeout
	if timeout == 0 {
		timeout = defaultShellTimeout
	}

	hostDir := sss.root.Name()
	cleanup := func() {}

	if params.Workdir != "" {
		workdir := params.Workdir

		var err error

		params.Workdir, err = normalizeRootName(sss.root, workdir)
		if err != nil {
			return fmt.Errorf("resolve workdir %q: %w", workdir, err).Error(), false
		}

		info, err := sss.root.Stat(params.Workdir)
		if err != nil {
			return fmt.Errorf("resolve workdir %q: %w", params.Workdir, err).Error(), false
		}

		if !info.IsDir() {
			return fmt.Errorf("resolve workdir %q: not a directory", params.Workdir).Error(), false
		}

		root, err := sss.root.OpenRoot(params.Workdir)
		if err != nil {
			return fmt.Errorf("resolve workdir %q: %w", params.Workdir, err).Error(), false
		}

		hostDir = root.Name()
		cleanup = func() { _ = root.Close() }
	}

	defer cleanup()

	if denied := sss.deniedBashPath(params.Command, hostDir); denied != "" {
		return denied, false
	}

	if err := sss.shellOutput.ensureTempDir(sss.root); err != nil {
		return err.Error(), false
	}

	commandCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(timeout)*time.Millisecond+shellTimeoutGrace)
	defer cancel()

	timedOut := false
	shell, args := shellCommand(params.Command)

	var cmd *exec.Cmd

	if sss.useSandbox {
		var err error

		cmd, err = sss.bash.command(commandCtx, sandboxedBashCommand{shell: shell, args: args, workdir: params.Workdir})
		if err != nil {
			return err.Error(), false
		}
	} else {
		cmd = exec.CommandContext(commandCtx, shell, args...)
		cmd.Dir = hostDir

		cmd.Env = append(os.Environ(), sss.env...)
		cmd.Env = append(cmd.Env, "TMPDIR="+sss.shellOutput.tmpDir)
	}

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
	output := stdoutBuf.String()
	visible, truncated, tempPath := sss.visibleShellOutput(output)

	metadata := []string{}

	if errStatus, ok := errors.AsType[*exec.ExitError](err); ok && errStatus.ExitCode() > 0 {
		exitCode := errStatus.ExitCode()
		metadata = append(metadata, fmt.Sprintf("Command exited with code %d", exitCode))
	}

	if timedOut {
		metadata = append(metadata, fmt.Sprintf("bash tool terminated command after exceeding timeout %d ms. If this command is expected to take longer and is not waiting for interactive input, retry with a larger timeout value in milliseconds.", timeout))
	}

	if visible == "" {
		visible = "(no output)"
	}

	if truncated && tempPath != "" {
		visible = "...output truncated...\n\nFull output saved to: " + filepath.ToSlash(tempPath) + "\n\n" + visible
	}

	if len(metadata) > 0 {
		visible += "\n\n<bash_metadata>\n" + strings.Join(metadata, "\n") + "\n</bash_metadata>"
	}

	return visible, err == nil && !timedOut
}

func (sss *sandboxedShellSystem) visibleShellOutput(output string) (visible string, truncated bool, tempPath string) {
	lines := strings.Split(output, "\n")
	if len(lines) <= shellMaxLines && len([]byte(output)) <= maxBytes {
		return output, false, ""
	}

	out := make([]string, 0, min(len(lines), shellMaxLines))
	bytesUsed := 0

	for i := len(lines) - 1; i >= 0 && len(out) < shellMaxLines; i-- {
		line := lines[i]

		size := len([]byte(line))
		if len(out) > 0 {
			size++
		}

		if bytesUsed+size > maxBytes {
			if len(out) == 0 {
				buf := []byte(line)

				start := max(len(buf)-maxBytes, 0)
				for start < len(buf) && (buf[start]&0xc0) == 0x80 {
					start++
				}

				out = append([]string{string(buf[start:])}, out...)
			}

			break
		}

		out = append([]string{line}, out...)
		bytesUsed += size
	}

	visible = strings.Join(out, "\n")

	tempPath = filepath.ToSlash(filepath.Join(sss.shellOutput.outputRelDir, fmt.Sprintf("rocketcode-bash-%d", time.Now().UnixNano())))
	if err := sss.root.WriteFile(tempPath, []byte(output), 0o600); err != nil {
		if visible == "" {
			visible = output
		}

		return visible, false, ""
	}

	sss.tempFiles[temporaryFile(tempPath)] = creationTime(time.Now())

	return visible, true, tempPath
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
