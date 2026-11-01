package harness

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

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

const (
	defaultShellTimeout   = 2 * 60 * 1000
	shellTimeoutGrace     = 100 * time.Millisecond
	shellForceKillTimeout = 3 * time.Second
)

type bashParams struct {
	Command            string `json:"command"`
	TimeoutMillisecond int    `json:"timeout_ms"`
	Workdir            string `json:"workdir"`
	Description        string `json:"description"`
}

// BashResult is the result of running a workspace bash command.
type BashResult struct {
	Output    string
	ErrorCode string
	Success   bool
}

func (r BashResult) String() string {
	return r.Output
}

type sandboxedShellSystem struct {
	mu               sync.Mutex
	root             *os.Root
	spillRel         string
	shellTemp        shellTempConfig
	env              []string
	shellCommand     shellCommander
	forceKillTimeout time.Duration
}

func newSandboxedShellSystem(root *os.Root, shellTemp *shellTempConfig, env []string, shellCommand shellCommander) *sandboxedShellSystem {
	return &sandboxedShellSystem{
		mu:               sync.Mutex{},
		root:             root,
		shellTemp:        *shellTemp,
		env:              slices.Clone(env),
		shellCommand:     shellCommand,
		forceKillTimeout: shellForceKillTimeout,
	}
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

func (sss *sandboxedShellSystem) Bash(ctx context.Context, customShell *CustomShell, params bashParams) BashResult {
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

	switch {
	case customShell.Dir != "" && filepath.IsAbs(params.Workdir):
		hostDir = params.Workdir
	case customShell.Dir != "":
		hostDir = filepath.Join(customShell.Dir, params.Workdir)
	case params.Workdir != "":
		root, err := sss.workdirRoot(params.Workdir)
		if err != nil {
			return bashFailure(err.Error())
		}

		defer func() { _ = root.Close() }()

		hostDir = root.Name()
	}

	if denied := sss.deniedBashPath(params.Command, hostDir); customShell.Dir == "" && denied != "" {
		return bashFailure(denied)
	}

	if err := sss.shellTemp.ensureTempDir(sss.root); err != nil {
		return bashFailure(err.Error())
	}

	commandCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMillisecond)*time.Millisecond+shellTimeoutGrace)
	defer cancel()

	timedOut := false

	env := append(os.Environ(), sss.env...)
	env = append(env, "TMPDIR="+sss.shellTemp.tmpDir)

	shell, args, stdin := withCustomShell(sss.shellCommand, customShell, params.Command, env)
	if strings.TrimSpace(shell) == "" {
		return bashFailure("shell command path is required")
	}

	cmd := exec.CommandContext(commandCtx, shell, args...)

	cmd.Dir, cmd.Env = hostDir, env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	// The command writes to a pipe this call owns, so a child that leaves the
	// process group and keeps the output open cannot hold the call past its timeout.
	outputReader, outputWriter, err := os.Pipe()
	if err != nil {
		return bashFailure(fmt.Errorf("create output pipe: %w", err).Error())
	}
	defer func() { _ = outputReader.Close() }()

	var output bytes.Buffer

	copied := make(chan struct{})

	go func() {
		_, _ = io.Copy(&output, outputReader)

		close(copied)
	}()

	cmd.Stdout = outputWriter
	cmd.Stderr = outputWriter
	cmd.Cancel = func() error {
		timedOut = true

		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("terminate command group: %w", err)
		}

		time.AfterFunc(sss.forceKillTimeout, func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })

		return nil
	}

	var sysProcAttr syscall.SysProcAttr

	sysProcAttr.Setpgid = true
	cmd.SysProcAttr = &sysProcAttr

	err = cmd.Run()
	_ = outputWriter.Close()

	if cmd.Process == nil {
		return bashFailure(err.Error())
	}

	select {
	case <-copied:
	case <-commandCtx.Done():
		if !timedOut {
			_ = cmd.Cancel()
		}

		select {
		case <-copied:
		case <-time.After(sss.forceKillTimeout):
			_ = outputReader.Close()

			<-copied
		}
	}

	full := output.String()
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

	return BashResult{
		Output:    full,
		ErrorCode: errorCode,
		Success:   err == nil && !timedOut,
	}
}

// workdirRoot opens workdir, a directory in the workspace outside the spill directory.
func (sss *sandboxedShellSystem) workdirRoot(workdir string) (*os.Root, error) {
	name, err := normalizeRootName(sss.root, workdir)
	if err != nil {
		return nil, fmt.Errorf("resolve workdir %q: %w", workdir, err)
	}

	if isExecuteSpillPath(sss.root, sss.spillRel, name) {
		return nil, errors.New(deniedSpillAccess)
	}

	info, err := sss.root.Stat(name)
	if err != nil {
		return nil, fmt.Errorf("resolve workdir %q: %w", name, err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("resolve workdir %q: not a directory", name)
	}

	root, err := sss.root.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("resolve workdir %q: %w", name, err)
	}

	return root, nil
}

func bashFailure(message string) BashResult {
	if message == "" {
		message = "(no output)"
	}

	return BashResult{
		Output:    message,
		ErrorCode: "error",
		Success:   false,
	}
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

		if word, ok := node.(*syntax.Word); ok {
			for part := range syntax.Preorder(word) {
				switch part.(type) {
				case *syntax.Word, *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted:
				default:
					return true
				}
			}

			fields, err := expand.Fields(&expand.Config{}, word)
			if err != nil {
				denied = err.Error()
				return false
			}

			for _, arg := range fields {
				if strings.HasPrefix(arg, "-") && strings.Contains(arg, "=") {
					_, arg, _ = strings.Cut(arg, "=")
				}

				if isExecuteSpillPath(sss.root, sss.spillRel, resolveBashPath(hostDir, arg)) {
					denied = deniedSpillAccess
					return false
				}
			}
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
		switch name {
		case "cat", "cd", "chmod", "chown", "cp", "grep", "head", "less", "ln", "mkdir", "more", "mv", "pushd", "rm", "tail", "touch":
		default:
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
	if arg == "~" || strings.HasPrefix(arg, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(arg, "~"))
		}
	}

	if filepath.IsAbs(arg) {
		return filepath.Clean(arg)
	}

	return filepath.Clean(filepath.Join(hostDir, arg))
}

func pathWithinRoot(rootName, path string) bool {
	rel, err := filepath.Rel(rootName, path)

	return err == nil && filepath.IsLocal(rel)
}
