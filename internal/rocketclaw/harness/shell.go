package harness

import (
	"bytes"
	"cmp"
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

	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

const (
	defaultShellTimeout   = 2 * 60 * 1000
	shellTimeoutGrace     = 100 * time.Millisecond
	shellForceKillTimeout = 3 * time.Second

	deniedBashOutsidePath    = "bash command denied: path is outside this agent's working directory"
	deniedBashWorkdir        = "bash command denied: workdir is outside this agent's working directory"
	deniedBashMissingWorkdir = "bash command denied: workdir is not an existing folder"
	deniedBashEnvFile        = "bash command denied: .env files are blocked"
)

type bashParams struct {
	Command            string `json:"command"`
	TimeoutMillisecond int    `json:"timeout_ms"`
	Workdir            string `json:"workdir"`
	Description        string `json:"description"`
}

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
	sandbox          *sandbox.Runtime
	forceKillTimeout time.Duration
}

func newSandboxedShellSystem(root *os.Root, shellTemp *shellTempConfig, env []string, rt *sandbox.Runtime) *sandboxedShellSystem {
	return &sandboxedShellSystem{
		mu:               sync.Mutex{},
		root:             root,
		shellTemp:        *shellTemp,
		env:              slices.Clone(env),
		sandbox:          rt,
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

	dir := filepath.Clean(cmp.Or(customShell.Dir, sss.root.Name()))
	folders := append([]string{dir}, customShell.ReadOnly...)
	hostDir := resolveShellPath(customShell, dir, params.Workdir)
	inFolders := slices.ContainsFunc(folders, func(folder string) bool { return pathWithinRoot(folder, hostDir) })

	switch {
	case !inFolders && customShell.Emulated && pathWithinRoot(emulatedHome, hostDir):
	case !inFolders:
		return bashFailure(deniedBashWorkdir)
	case isExecuteSpillPath(sss.root, sss.spillRel, hostDir):
		return bashFailure(deniedSpillAccess)
	default:
		if info, err := os.Stat(hostDir); err != nil || !info.IsDir() {
			return bashFailure(deniedBashMissingWorkdir)
		}

		resolved, ok := guestWorkdir(folders, hostDir)
		if !ok {
			return bashFailure(deniedBashWorkdir)
		}

		hostDir = resolved
	}

	if denied := sss.deniedBashPath(customShell, dir, params.Command, hostDir); denied != "" {
		return bashFailure(denied)
	}

	if err := sss.shellTemp.ensureTempDir(sss.root); err != nil {
		return bashFailure(err.Error())
	}

	timeout := time.Duration(timeoutMillisecond) * time.Millisecond

	env := append(os.Environ(), sss.env...)
	env = append(env, "TMPDIR="+sss.shellTemp.tmpDir)

	if customShell.Emulated {
		return emulatedBashResult(runEmulated(ctx, sss.sandbox, customShell, params.Command, hostDir, sss.env, env, timeout))
	}

	commandCtx, cancel := context.WithTimeout(ctx, timeout+shellTimeoutGrace)
	defer cancel()

	timedOut := false

	env = append(env, customShell.Env...)
	shell, args := withCustomShell(customShell, params.Command)
	cmd := exec.CommandContext(commandCtx, shell, args...)
	cmd.Dir, cmd.Env = hostDir, env

	outputReader, outputWriter, err := os.Pipe()
	if err != nil {
		return bashFailure(fmt.Errorf("create output pipe: %w", err).Error())
	}
	defer func() { _ = outputReader.Close() }()

	var full string

	copied := make(chan struct{})

	go func() {
		full = readShellOutput(outputReader)

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

func readShellOutput(pipe *os.File) string {
	var (
		output strings.Builder
		chunk  [8192]byte
	)
	for {
		n, errRead := pipe.Read(chunk[:])
		output.Grow(n)
		_, _ = output.Write(chunk[:n])

		if errRead != nil {
			return output.String()
		}
	}
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

func (sss *sandboxedShellSystem) deniedBashPath(customShell *CustomShell, dir, command, hostDir string) string {
	parser := syntax.NewParser()

	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return ""
	}

	printer := syntax.NewPrinter()
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

				if isExecuteSpillPath(sss.root, sss.spillRel, resolveShellPath(customShell, hostDir, arg)) {
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

		var paths []string

		for _, arg := range args[1:] {
			if pathArg, ok := staticBashPathArg(name, arg); ok {
				paths = append(paths, pathArg)
			}
		}

		reads := 0

		switch name {
		case "cat", "cd", "grep", "head", "less", "more", "pushd", "tail":
			reads = len(paths)
		case "cp":
			reads = len(paths) - 1
		case "chmod", "chown", "ln", "mkdir", "mv", "rm", "touch":
		default:
			return true
		}

		for i, pathArg := range paths {
			resolved := resolveShellPath(customShell, hostDir, pathArg)
			if isExecuteSpillPath(sss.root, sss.spillRel, resolved) {
				denied = deniedSpillAccess
				return false
			}

			if isDeniedEnvPath(resolved) {
				denied = deniedBashEnvFile
				return false
			}

			readable := i < reads && slices.ContainsFunc(customShell.ReadOnly, func(folder string) bool { return pathWithinRoot(folder, resolved) })
			if !pathWithinRoot(dir, resolved) && !readable && (!customShell.Emulated || !pathWithinRoot(emulatedHome, resolved)) {
				denied = deniedBashOutsidePath
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

func resolveShellPath(customShell *CustomShell, hostDir, arg string) string {
	if customShell.Emulated && (arg == "~" || strings.HasPrefix(arg, "~/")) {
		return filepath.Join(emulatedHome, arg[1:])
	}

	return resolveBashPath(hostDir, arg)
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
