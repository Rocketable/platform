package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestSandboxedShellSystemBash(t *testing.T) {
	dir := t.TempDir()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-tmp")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-tmp"), 0o755))

	require.NoError(t, root.MkdirAll("nested", 0o755))
	require.NoError(t, root.WriteFile("root.txt", []byte("root\n"), 0o644))
	require.NoError(t, root.WriteFile("nested/file.txt", []byte("nested\n"), 0o644))
	require.NoError(t, root.WriteFile(".env", []byte("SECRET=value\n"), 0o644))
	require.NoError(t, root.WriteFile(".env.example", []byte("SECRET=example\n"), 0o644))

	shellTemp := testShellTempConfig(t, root, outputDir)
	sss := newSandboxedShellSystem(root, &shellTemp, nil, DefaultShellCommand{})
	sss.forceKillTimeout = 10 * time.Millisecond

	t.Run("rejects invalid commands before execution", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			command bashParams
			want    string
		}{
			{"blank command", bashParams{Command: " \t\n"}, "command is required"},
			{"negative timeout", bashParams{Command: "touch rejected-command", TimeoutMillisecond: -1}, "Invalid timeout_ms value: -1. timeout_ms must be a positive number."},
			{"missing workdir", bashParams{Command: "touch rejected-command", Workdir: "missing"}, `resolve workdir "missing":`},
			{"file workdir", bashParams{Command: "touch rejected-command", Workdir: "root.txt"}, `resolve workdir "root.txt": not a directory`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got := sss.Bash(t.Context(), &CustomShell{}, tc.command)
				require.False(t, got.Success)
				require.Equal(t, "error", got.ErrorCode)
				require.Contains(t, got.Output, tc.want)

				_, err := root.Stat("rejected-command")
				require.ErrorIs(t, err, fs.ErrNotExist)
			})
		}
	})
	t.Run("shell environment cannot reinterpret checked commands", func(t *testing.T) {
		require.NoError(t, root.WriteFile("sh", []byte("#!/bin/sh\nprintf unexpected"), 0o755))
		require.NoError(t, root.WriteFile("startup.sh", []byte("printf unexpected"), 0o644))

		for _, env := range []struct{ name, value string }{
			{"SHELL", filepath.Join(dir, "sh")},
			{"BASH_ENV", filepath.Join(dir, "startup.sh")},
			{"BASH_FUNC_printf%%", "() { builtin printf unexpected; }"},
		} {
			t.Run(env.name, func(t *testing.T) {
				t.Setenv(env.name, env.value)
				got := sss.Bash(t.Context(), &CustomShell{}, bashParams{Command: "printf checked"})
				require.True(t, got.Success, got.Output)
				require.Equal(t, "checked", got.Output)
			})
		}
	})
	t.Run("interrupt stops a running command", func(t *testing.T) {
		ctx, interrupt := context.WithCancelCause(t.Context())

		time.AfterFunc(200*time.Millisecond, func() { interrupt(errTurnInterrupted) })

		got := sss.Bash(ctx, &CustomShell{}, bashParams{Command: "echo started; sleep 30; echo finished"})
		require.False(t, got.Success)
		require.Contains(t, got.Output, "started")
		require.NotContains(t, got.Output, "finished")
	})
	t.Run("captures stderr", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: "echo stdout_msg && echo stderr_msg >&2", TimeoutMillisecond: 0, Workdir: "", Description: "stderr"}).String()
		require.Contains(t, got, "stdout_msg")
		require.Contains(t, got, "stderr_msg")
	})
	t.Run("empty output", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: "true", TimeoutMillisecond: 0, Workdir: "", Description: "No output"}).String()
		require.Equal(t, "(no output)", got)
	})
	t.Run("non zero exit sets error code", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: "exit 42", TimeoutMillisecond: 0, Workdir: "", Description: "Non zero"})
		require.Equal(t, "(no output)", got.String())
		require.Equal(t, "42", got.ErrorCode)
		require.False(t, got.Success)
	})
	t.Run("default workdir is sandbox root", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: "pwd", TimeoutMillisecond: 0, Workdir: "", Description: "pwd"}).String()
		require.Contains(t, got, dir)
	})
	t.Run("nested workdir is honored", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: "pwd && ls", TimeoutMillisecond: 0, Workdir: filepath.Join(dir, "nested"), Description: "nested pwd"}).String()
		require.Contains(t, got, "file.txt")
	})

	t.Run("external workdir is rejected", func(t *testing.T) {
		workdir := t.TempDir()
		got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: "pwd", TimeoutMillisecond: 0, Workdir: workdir, Description: "external pwd"}).String()
		require.Equal(t, fmt.Sprintf("resolve workdir %q: path escapes root: %s", workdir, workdir), got)
	})

	t.Run("direct external file access is denied", func(t *testing.T) {
		externalDir := t.TempDir()
		externalFile := filepath.Join(externalDir, "secret.txt")
		require.NoError(t, os.WriteFile(externalFile, []byte("secret\n"), 0o644))

		got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: "cat " + externalFile, TimeoutMillisecond: 0, Workdir: "", Description: "external cat"}).String()
		require.Contains(t, got, "bash command denied: external path access is blocked")
		require.Contains(t, got, externalFile)
	})

	t.Run("relative external file access is denied", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: "cat ../outside.txt", TimeoutMillisecond: 0, Workdir: "", Description: "relative external cat"}).String()
		require.Equal(t, "bash command denied: external path access is blocked: ../outside.txt", got)
	})

	t.Run("external cd is denied", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: "cd /tmp", TimeoutMillisecond: 0, Workdir: "", Description: "external cd"}).String()
		require.Equal(t, "bash command denied: external path access is blocked: /tmp", got)
	})

	t.Run("direct env file access is denied", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: "cat .env", TimeoutMillisecond: 0, Workdir: "", Description: "env cat"}).String()
		require.Equal(t, "bash command denied: "+deniedEnvAccessMessage(".env"), got)
	})

	t.Run("env example file access is allowed", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: "cat .env.example", TimeoutMillisecond: 0, Workdir: "", Description: "env example cat"}).String()
		require.Contains(t, got, "SECRET=example")
	})

	t.Run("timeout sets error code and preserves output", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: "echo started && sleep 10", TimeoutMillisecond: 100, Workdir: "", Description: "timeout"})
		require.Contains(t, got.String(), "started")
		require.Equal(t, "timeout", got.ErrorCode)
		require.False(t, got.Success)
	})

	t.Run("cancellation ends the call when an escaped child keeps the output open", func(t *testing.T) {
		tests := []struct {
			name    string
			command string
		}{
			{name: "shell already exited", command: "set -m; sleep 30 & echo $! $$ > child.pid"},
			{name: "shell still running", command: "set -m; sleep 30 & echo $! $$ > child.pid; sleep 10"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				require.NoError(t, root.WriteFile("child.pid", nil, 0o600))

				ctx, cancel := context.WithCancel(t.Context())

				var (
					running errgroup.Group
					got     BashResult
				)

				running.Go(func() error {
					got = sss.Bash(ctx, &CustomShell{}, bashParams{Command: tt.command, Description: "escaped child"})
					return nil
				})
				t.Cleanup(func() {
					cancel()
					require.NoError(t, running.Wait())
				})

				// Keep job-control diagnostics separate and cancel only after the child exists.
				var pid, shellPID int

				require.EventuallyWithT(t, func(check *assert.CollectT) {
					data, err := root.ReadFile("child.pid")
					assert.NoError(check, err)
					_, err = fmt.Sscanf(string(data), "%d %d", &pid, &shellPID)
					assert.NoError(check, err)
				}, 10*time.Second, time.Millisecond)
				t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

				if tt.name == "shell already exited" {
					require.Eventually(t, func() bool {
						return syscall.Kill(shellPID, 0) == syscall.ESRCH
					}, 10*time.Second, time.Millisecond)
				}

				start := time.Now()

				cancel()
				require.NoError(t, running.Wait())

				elapsed := time.Since(start)

				require.Less(t, elapsed, 10*time.Second)
				require.Equal(t, "timeout", got.ErrorCode)
				require.NoError(t, syscall.Kill(pid, syscall.SIGKILL))
			})
		}
	})

	t.Run("environment precedence and private temp dir", func(t *testing.T) {
		t.Setenv("ROCKETCLAW_CONVERSATION_ID", "old")

		got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: `printf %s "$TMPDIR"`, TimeoutMillisecond: 0, Workdir: "", Description: "tmpdir"}).String()
		require.Equal(t, outputDir, got)

		info, err := root.Stat(filepath.Join(".tmp", "shell-tmp"))
		require.NoError(t, err)
		require.Equal(t, fs.FileMode(0o700), info.Mode().Perm())

		configured := newSandboxedShellSystem(root, &shellTemp, []string{"ROCKETCLAW_CONVERSATION_ID=new", "TMPDIR=/not/rocketcode"}, DefaultShellCommand{})

		result := configured.Bash(t.Context(), &CustomShell{}, bashParams{Command: `printf '%s\n%s' "$ROCKETCLAW_CONVERSATION_ID" "$TMPDIR"`, Description: "env precedence"})
		require.True(t, result.Success, result.Output)
		require.Equal(t, "new\n"+outputDir, result.String())
	})

	t.Run("returns full multi-line output", func(t *testing.T) {
		cmd := "i=1; while [ $i -le 2100 ]; do echo $i; i=$((i+1)); done"
		got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: cmd, TimeoutMillisecond: 0, Workdir: "", Description: "many lines"})
		require.Equal(t, got.Output, got.String())
		require.Contains(t, got.Output, "1\n2\n3")
		require.Contains(t, got.Output, "2099\n2100")
		require.NotContains(t, got.Output, "...output truncated...")
		require.NotContains(t, got.Output, "full_output")
		require.Empty(t, got.ErrorCode)
		require.True(t, got.Success)
	})

	t.Run("custom shell", func(t *testing.T) {
		require.NoError(t, root.WriteFile("wrap.sh", []byte("#!/bin/sh\nprintf 'argc=%s arg=%s pwd=%s tmp=%s' \"$#\" \"$1\" \"$(pwd -P)\" \"$TMPDIR\""), 0o755))

		realDir, err := filepath.EvalSymlinks(dir)
		require.NoError(t, err)

		t.Run("receives the full command as its only argument", func(t *testing.T) {
			command := `printf "%s\n" 'two words' | tr a-z A-Z`
			got := sss.Bash(t.Context(), &CustomShell{Path: filepath.Join(dir, "wrap.sh")}, bashParams{Command: command})
			require.True(t, got.Success, got.Output)
			require.Equal(t, fmt.Sprintf("argc=1 arg=%s pwd=%s tmp=%s", command, realDir, outputDir), got.Output)
		})
		t.Run("runs in the nested workdir with the session TMPDIR", func(t *testing.T) {
			got := sss.Bash(t.Context(), &CustomShell{Path: filepath.Join(dir, "wrap.sh")}, bashParams{Command: "ls", Workdir: filepath.Join(dir, "nested")})
			require.True(t, got.Success, got.Output)
			require.Equal(t, fmt.Sprintf("argc=1 arg=ls pwd=%s tmp=%s", filepath.Join(realDir, "nested"), outputDir), got.Output)
		})
		t.Run("with a setup reads the setup line and the command on stdin", func(t *testing.T) {
			require.NoError(t, root.WriteFile("setup.sh", []byte("#!/bin/sh\nprintf 'argc=%s\\n' \"$#\"; cat"), 0o755))

			sss := newSandboxedShellSystem(root, &shellTemp, []string{"X=turn"}, DefaultShellCommand{})

			got := sss.Bash(t.Context(), &CustomShell{Path: filepath.Join(dir, "setup.sh"), Setup: json.RawMessage(`{"a":1}`)}, bashParams{Command: "echo $X"})
			require.True(t, got.Success, got.Output)

			argc, rest, _ := strings.Cut(got.Output, "\n")
			line, command, _ := strings.Cut(rest, "\n")

			require.Equal(t, "argc=0", argc)
			require.Equal(t, "echo $X", command)

			var setup struct {
				Config json.RawMessage
				Env    map[string]string
			}
			require.NoError(t, json.Unmarshal([]byte(line), &setup))
			require.JSONEq(t, `{"a":1}`, string(setup.Config))
			require.Equal(t, "turn", setup.Env["X"], "the env holds the turn's variables")
			require.Equal(t, outputDir, setup.Env["TMPDIR"])
			require.Equal(t, os.Getenv("HOME"), setup.Env["HOME"], "and RocketClaw's own")
		})
		t.Run("a shell confined to its own folder takes workdir there and is not path-checked", func(t *testing.T) {
			require.NoError(t, root.WriteFile("confined.sh", []byte("#!/bin/sh\nread -r setup\nexec /bin/bash"), 0o755))

			folder, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)

			// The folder lies outside the sandbox root, as an easy agent's workingDirectory may.
			require.NoError(t, os.WriteFile(filepath.Join(folder, "file.txt"), []byte("outside\n"), 0o600))
			require.NoError(t, os.Mkdir(filepath.Join(folder, "sub"), 0o700))

			shell := CustomShell{Path: filepath.Join(dir, "confined.sh"), Setup: json.RawMessage(`{}`), Emulated: true, Dir: folder}

			got := sss.Bash(t.Context(), &shell, bashParams{Command: "cat " + filepath.Join(folder, "file.txt") + " && pwd -P", Workdir: folder})
			require.True(t, got.Success, got.Output)
			require.Equal(t, "outside\n"+folder+"\n", got.Output)

			got = sss.Bash(t.Context(), &shell, bashParams{Command: "pwd -P", Workdir: "sub"})
			require.True(t, got.Success, got.Output)
			require.Equal(t, filepath.Join(folder, "sub")+"\n", got.Output, "a relative workdir starts from the folder")

			got = sss.Bash(t.Context(), &shell, bashParams{Command: "pwd -P"})
			require.Equal(t, folder+"\n", got.Output, "the folder is the default workdir")
		})
		t.Run("missing program reports the start error without falling back to bash", func(t *testing.T) {
			got := sss.Bash(t.Context(), &CustomShell{Path: filepath.Join(dir, "missing-shell")}, bashParams{Command: "touch ran-under-bash"})
			require.Contains(t, got.Output, "no such file or directory")
			require.Equal(t, "error", got.ErrorCode)
			require.False(t, got.Success)

			_, err := root.Stat("ran-under-bash")
			require.ErrorIs(t, err, fs.ErrNotExist)
		})
	})
}

func testShellTempConfig(t *testing.T, root *os.Root, outputDir string) shellTempConfig {
	t.Helper()

	config, err := newShellTempConfig(root, outputDir)
	require.NoError(t, err)

	return config
}

func TestPathWithinRoot(t *testing.T) {
	for _, tt := range []struct {
		root, path string
		want       bool
	}{
		{"/workspace", "/workspace", true},
		{"/workspace", "/workspace/nested/file", true},
		{"/workspace", "/workspace/nested/../file", true},
		{"/workspace", "/workspace/..hidden", true},
		{"/workspace", "/workspace-other/file", false},
		{"/workspace", "/workspace/../outside", false},
		{"/workspace", "/", false},
		{"/", "/workspace", true},
		{"spill", "spill/result.txt", true},
		{"spill", "spill-other/result.txt", false},
		{"spill", "../spill/result.txt", false},
		{"/workspace", "relative", false},
	} {
		t.Run(tt.root+"/"+tt.path, func(t *testing.T) {
			require.Equal(t, tt.want, pathWithinRoot(tt.root, tt.path))
		})
	}
}

func TestResolveBashPath(t *testing.T) {
	for _, tt := range []struct{ home, arg, want string }{
		{"/home/person", "~", "/home/person"},
		{"/home/person", "~/file", "/home/person/file"},
		{"/home/person", "~//file", "/home/person/file"},
		{"/home/person", "~/../file", "/home/file"},
		{"/home/person", "~person/file", "/workspace/~person/file"},
		{"/", "~", "/"},
		{"/", "~/file", "/file"},
		{"relative/home", "~", "relative/home"},
		{"relative/home", "~/file", "relative/home/file"},
		{"", "~", "/workspace/~"},
		{"", "~/file", "/workspace/~/file"},
		{"", "file", "/workspace/file"},
		{"", "/file", "/file"},
		{"", "", "/workspace"},
	} {
		t.Run(tt.home+"/"+tt.arg, func(t *testing.T) {
			t.Setenv("HOME", tt.home)
			require.Equal(t, tt.want, resolveBashPath("/workspace", tt.arg))
		})
	}
}

func TestShellCommandOverride(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-tmp")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-tmp"), 0o755))
	shellTemp := testShellTempConfig(t, root, outputDir)

	shell := &mockShellCommander{CommandFunc: func(string) (string, []string) {
		return "/bin/sh", []string{"-c", "printf mocked"}
	}}
	sss := newSandboxedShellSystem(root, &shellTemp, nil, shell)
	got := sss.Bash(context.Background(), &CustomShell{}, bashParams{Command: "gh pr view 1", TimeoutMillisecond: 0})

	require.Equal(t, "gh pr view 1", shell.CommandCalls()[0].Command)
	require.Contains(t, got.String(), "mocked")
}
