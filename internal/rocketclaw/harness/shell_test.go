package harness

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
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
	sss := newSandboxedShellSystem(root, &shellTemp, nil, &sandbox.Runtime{}, InertTurnSecrets{})
	sss.forceKillTimeout = 10 * time.Millisecond

	t.Run("rejects invalid commands before execution", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			command bashParams
			want    string
		}{
			{"blank command", bashParams{Command: " \t\n"}, "command is required"},
			{"negative timeout", bashParams{Command: "touch rejected-command", TimeoutMillisecond: -1}, "Invalid timeout_ms value: -1. timeout_ms must be a positive number."},
			{"missing workdir", bashParams{Command: "touch rejected-command", Workdir: "missing"}, deniedBashMissingWorkdir},
			{"file workdir", bashParams{Command: "touch rejected-command", Workdir: "root.txt"}, deniedBashMissingWorkdir},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got := sss.Bash(t.Context(), &CustomShell{}, &tc.command)
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
				got := sss.Bash(t.Context(), &CustomShell{}, &bashParams{Command: "printf checked"})
				require.True(t, got.Success, got.Output)
				require.Equal(t, "checked", got.Output)
			})
		}
	})
	t.Run("interrupt stops a running command", func(t *testing.T) {
		ctx, interrupt := context.WithCancelCause(t.Context())

		time.AfterFunc(200*time.Millisecond, func() { interrupt(errTurnInterrupted) })

		got := sss.Bash(ctx, &CustomShell{}, &bashParams{Command: "echo started; sleep 30; echo finished"})
		require.False(t, got.Success)
		require.Contains(t, got.Output, "started")
		require.NotContains(t, got.Output, "finished")
	})
	t.Run("calls run side by side", func(t *testing.T) {
		results := make(chan BashResult, 2)

		for _, command := range []string{
			"touch side-a; for i in $(seq 200); do [ -e side-b ] && exit 0; sleep 0.01; done; exit 1",
			"touch side-b; for i in $(seq 200); do [ -e side-a ] && exit 0; sleep 0.01; done; exit 1",
		} {
			go func() { results <- sss.Bash(t.Context(), &CustomShell{}, &bashParams{Command: command}) }()
		}

		for range 2 {
			require.True(t, (<-results).Success, "each command sees the other one running")
		}
	})
	t.Run("captures stderr", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, &bashParams{Command: "echo stdout_msg && echo stderr_msg >&2", TimeoutMillisecond: 0, Workdir: "", Description: "stderr"}).String()
		require.Contains(t, got, "stdout_msg")
		require.Contains(t, got, "stderr_msg")
	})
	t.Run("empty output", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, &bashParams{Command: "true", TimeoutMillisecond: 0, Workdir: "", Description: "No output"}).String()
		require.Equal(t, "(no output)", got)
	})
	t.Run("non zero exit sets error code", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, &bashParams{Command: "exit 42", TimeoutMillisecond: 0, Workdir: "", Description: "Non zero"})
		require.Equal(t, "(no output)", got.String())
		require.Equal(t, "42", got.ErrorCode)
		require.False(t, got.Success)
	})
	t.Run("default workdir is sandbox root", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, &bashParams{Command: "pwd", TimeoutMillisecond: 0, Workdir: "", Description: "pwd"}).String()
		require.Contains(t, got, dir)
	})
	t.Run("nested workdir is honored", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, &bashParams{Command: "pwd && ls", TimeoutMillisecond: 0, Workdir: filepath.Join(dir, "nested"), Description: "nested pwd"}).String()
		require.Contains(t, got, "file.txt")
	})

	t.Run("external workdir is rejected", func(t *testing.T) {
		workdir := t.TempDir()
		got := sss.Bash(context.Background(), &CustomShell{}, &bashParams{Command: "pwd", TimeoutMillisecond: 0, Workdir: workdir, Description: "external pwd"}).String()
		require.Equal(t, deniedBashWorkdir, got)
	})

	t.Run("direct external file access is denied", func(t *testing.T) {
		externalDir := t.TempDir()
		externalFile := filepath.Join(externalDir, "secret.txt")
		require.NoError(t, os.WriteFile(externalFile, []byte("secret\n"), 0o644))

		got := sss.Bash(context.Background(), &CustomShell{}, &bashParams{Command: "cat " + externalFile, TimeoutMillisecond: 0, Workdir: "", Description: "external cat"}).String()
		require.Equal(t, deniedBashOutsidePath, got)
	})

	t.Run("relative external file access is denied", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, &bashParams{Command: "cat ../outside.txt", TimeoutMillisecond: 0, Workdir: "", Description: "relative external cat"}).String()
		require.Equal(t, deniedBashOutsidePath, got)
	})

	t.Run("external cd is denied", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, &bashParams{Command: "cd /tmp", TimeoutMillisecond: 0, Workdir: "", Description: "external cd"}).String()
		require.Equal(t, deniedBashOutsidePath, got)
	})

	t.Run("direct env file access is denied", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, &bashParams{Command: "cat .env", TimeoutMillisecond: 0, Workdir: "", Description: "env cat"}).String()
		require.Equal(t, deniedBashEnvFile, got)
	})

	t.Run("env example file access is allowed", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, &bashParams{Command: "cat .env.example", TimeoutMillisecond: 0, Workdir: "", Description: "env example cat"}).String()
		require.Contains(t, got, "SECRET=example")
	})

	t.Run("timeout sets error code and preserves output", func(t *testing.T) {
		got := sss.Bash(context.Background(), &CustomShell{}, &bashParams{Command: "echo started && sleep 10", TimeoutMillisecond: 100, Workdir: "", Description: "timeout"})
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
					got = sss.Bash(ctx, &CustomShell{}, &bashParams{Command: tt.command, Description: "escaped child"})
					return nil
				})
				t.Cleanup(func() {
					cancel()
					require.NoError(t, running.Wait())
				})

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

		got := sss.Bash(context.Background(), &CustomShell{}, &bashParams{Command: `printf %s "$TMPDIR"`, TimeoutMillisecond: 0, Workdir: "", Description: "tmpdir"}).String()
		require.Equal(t, outputDir, got)

		info, err := root.Stat(filepath.Join(".tmp", "shell-tmp"))
		require.NoError(t, err)
		require.Equal(t, fs.FileMode(0o700), info.Mode().Perm())

		secrets := NewTurnSecretStore()
		require.NoError(t, secrets.Save("GITHUB_TOKEN", []byte("ghp-turn-value")))
		configured := newSandboxedShellSystem(root, &shellTemp, []string{"ROCKETCLAW_CONVERSATION_ID=new", "TMPDIR=/not/rocketcode"}, &sandbox.Runtime{}, secrets)

		result := configured.Bash(t.Context(), &CustomShell{}, &bashParams{Command: `printf '%s\n%s\n%s' "$ROCKETCLAW_CONVERSATION_ID" "$TMPDIR" "$GITHUB_TOKEN"`, Description: "env precedence"})
		require.True(t, result.Success, result.Output)
		require.Equal(t, "new\n"+outputDir+"\nghp-turn-value", result.String())

		require.NoError(t, root.WriteFile("token.sh", []byte("#!/bin/sh\nprintf %s \"$GITHUB_TOKEN\""), 0o755))
		result = configured.Bash(t.Context(), &CustomShell{Path: filepath.Join(dir, "token.sh")}, &bashParams{Command: "ls"})
		require.Equal(t, BashResult{Output: "ghp-turn-value", Success: true}, result, "a wrapper program sees the turn secret")
		require.NotContains(t, strings.Join(configured.env, "\n"), "ghp-turn-value", "the persisted turn environment never holds a turn secret")
	})

	t.Run("returns full multi-line output", func(t *testing.T) {
		cmd := "i=1; while [ $i -le 2100 ]; do echo $i; i=$((i+1)); done"
		got := sss.Bash(context.Background(), &CustomShell{}, &bashParams{Command: cmd, TimeoutMillisecond: 0, Workdir: "", Description: "many lines"})
		require.Equal(t, got.Output, got.String())
		require.Contains(t, got.Output, "1\n2\n3")
		require.Contains(t, got.Output, "2099\n2100")
		require.NotContains(t, got.Output, "...output truncated...")
		require.NotContains(t, got.Output, "full_output")
		require.Empty(t, got.ErrorCode)
		require.True(t, got.Success)
	})

	t.Run("stdin", func(t *testing.T) {
		binary := "\x00\x01\xfe\xff" + strings.Repeat("payload ", 1<<17)

		got := sss.Bash(t.Context(), &CustomShell{}, &bashParams{Command: "cat > piped.bin", Stdin: bashStdin{text: binary}})
		require.True(t, got.Success, got.Output)

		data, err := root.ReadFile("piped.bin")
		require.NoError(t, err)
		require.Equal(t, binary, string(data))

		require.NoError(t, root.WriteFile("upper.sh", []byte("#!/bin/sh\ntr a-z A-Z"), 0o755))
		got = sss.Bash(t.Context(), &CustomShell{Path: filepath.Join(dir, "upper.sh")}, &bashParams{Command: "ignored", Stdin: bashStdin{text: "wrapped"}})
		require.Equal(t, BashResult{Output: "WRAPPED", Success: true}, got)

		start := time.Now()
		got = sss.Bash(t.Context(), &CustomShell{}, &bashParams{Command: "true", Stdin: bashStdin{text: binary}})
		require.Equal(t, BashResult{Success: true}, got)

		got = sss.Bash(t.Context(), &CustomShell{}, &bashParams{Command: "sleep 30 <&0 >/dev/null 2>&1 & echo $! > stdin-holder.pid", Stdin: bashStdin{text: binary}})
		require.True(t, got.Success, got.Output)

		pid, err := root.ReadFile("stdin-holder.pid")
		require.NoError(t, err)
		holder, err := strconv.Atoi(strings.TrimSpace(string(pid)))
		require.NoError(t, err)
		require.NoError(t, syscall.Kill(holder, syscall.SIGKILL))

		got = sss.Bash(t.Context(), &CustomShell{}, &bashParams{Command: "sleep 30", TimeoutMillisecond: 100, Stdin: bashStdin{text: binary}})
		require.Equal(t, "timeout", got.ErrorCode)
		require.Less(t, time.Since(start), 10*time.Second, "unread stdin never holds the call open")
	})

	t.Run("custom shell", func(t *testing.T) {
		require.NoError(t, root.WriteFile("wrap.sh", []byte("#!/bin/sh\nprintf 'argc=%s arg=%s pwd=%s tmp=%s' \"$#\" \"$1\" \"$(pwd -P)\" \"$TMPDIR\""), 0o755))

		realDir, err := filepath.EvalSymlinks(dir)
		require.NoError(t, err)

		t.Run("receives the full command as its only argument", func(t *testing.T) {
			command := `printf "%s\n" 'two words' | tr a-z A-Z`
			got := sss.Bash(t.Context(), &CustomShell{Path: filepath.Join(dir, "wrap.sh")}, &bashParams{Command: command})
			require.True(t, got.Success, got.Output)
			require.Equal(t, fmt.Sprintf("argc=1 arg=%s pwd=%s tmp=%s", command, realDir, outputDir), got.Output)
		})
		t.Run("runs in the nested workdir with the session TMPDIR", func(t *testing.T) {
			got := sss.Bash(t.Context(), &CustomShell{Path: filepath.Join(dir, "wrap.sh")}, &bashParams{Command: "ls", Workdir: filepath.Join(dir, "nested")})
			require.True(t, got.Success, got.Output)
			require.Equal(t, fmt.Sprintf("argc=1 arg=ls pwd=%s tmp=%s", filepath.Join(realDir, "nested"), outputDir), got.Output)
		})
		t.Run("runs in the agent's folder with the shell's variables", func(t *testing.T) {
			require.NoError(t, root.WriteFile("env.sh", []byte("#!/bin/sh\nprintf 'pwd=%s environment=%s' \"$(pwd -P)\" \"$ROCKETCLAW_ENVIRONMENT\""), 0o755))

			folder, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)

			got := sss.Bash(t.Context(), &CustomShell{Path: filepath.Join(dir, "env.sh"), Dir: folder, Env: []string{`ROCKETCLAW_ENVIRONMENT={"commands":{"gh":"gh"}}`}}, &bashParams{Command: "ls"})
			require.True(t, got.Success, got.Output)
			require.Equal(t, "pwd="+folder+` environment={"commands":{"gh":"gh"}}`, got.Output)
		})
		t.Run("an emulated shell without a sandbox refuses and never runs host bash", func(t *testing.T) {
			folder, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)

			got := sss.Bash(t.Context(), &CustomShell{Emulated: true, Dir: folder}, &bashParams{Command: "touch ran-under-bash", Workdir: folder})
			require.Contains(t, got.Output, "emulated shell")
			require.Equal(t, "error", got.ErrorCode)
			require.False(t, got.Success)

			_, err = os.Stat(filepath.Join(folder, "ran-under-bash"))
			require.ErrorIs(t, err, fs.ErrNotExist)
		})
		t.Run("missing program reports the start error without falling back to bash", func(t *testing.T) {
			got := sss.Bash(t.Context(), &CustomShell{Path: filepath.Join(dir, "missing-shell")}, &bashParams{Command: "touch ran-under-bash"})
			require.Contains(t, got.Output, "no such file or directory")
			require.Equal(t, "error", got.ErrorCode)
			require.False(t, got.Success)

			_, err := root.Stat("ran-under-bash")
			require.ErrorIs(t, err, fs.ErrNotExist)
		})
	})
}

func TestLiteralWordConfig(t *testing.T) {
	file, err := syntax.NewParser().Parse(strings.NewReader(`echo <(ls) $(ls) $'a\tb' $"c d"`), "")
	require.NoError(t, err)

	call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr)
	require.True(t, ok)

	_, err = expand.Literal(literalWordConfig(), call.Args[1])
	require.ErrorIs(t, err, errProcessSubstitution)

	_, err = expand.Literal(literalWordConfig(), call.Args[2])
	_, isUnexpectedCommand := errors.AsType[expand.UnexpectedCommandError](err)
	require.True(t, isUnexpectedCommand)

	fields, err := expand.Fields(literalWordConfig(), call.Args[3:]...)
	require.NoError(t, err)
	require.Equal(t, []string{"a\tb", "c d"}, fields)
}

func TestBashStaysInTheAgentFolder(t *testing.T) {
	workspace, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, workspace.Close()) })

	require.NoError(t, workspace.Mkdir(".tmp", 0o700))
	require.NoError(t, workspace.WriteFile("wrap.sh", []byte("#!/bin/sh\nexec /bin/bash -c \"$1\""), 0o755))

	shellTemp := testShellTempConfig(t, workspace, filepath.Join(workspace.Name(), ".tmp"))
	sss := newSandboxedShellSystem(workspace, &shellTemp, nil, &sandbox.Runtime{}, InertTurnSecrets{})

	folders := map[string]*os.Root{}

	for _, name := range []string{"folder", "uploads", "outside"} {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		require.NoError(t, err)

		folders[name], err = os.OpenRoot(dir)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, folders[name].Close()) })
	}

	folder, uploads, outside := folders["folder"].Name(), folders["uploads"].Name(), folders["outside"].Name()
	t.Setenv("HOME", outside)
	require.NoError(t, folders["uploads"].WriteFile("report.txt", []byte("upload"), 0o644))
	require.NoError(t, folders["outside"].WriteFile("secret.txt", []byte("secret"), 0o644))

	wrapper := CustomShell{Path: filepath.Join(workspace.Name(), "wrap.sh"), Dir: folder, ReadOnly: []string{uploads}}
	emulated := CustomShell{Emulated: true, Dir: folder, ReadOnly: []string{uploads}}

	for _, tc := range []struct {
		name    string
		shell   CustomShell
		params  bashParams
		want    string
		success bool
	}{
		{"wrapper cat outside", wrapper, bashParams{Command: "cat " + outside + "/secret.txt"}, deniedBashOutsidePath, false},
		{"wrapper cp from outside", wrapper, bashParams{Command: "cp " + outside + "/secret.txt stolen.txt"}, deniedBashOutsidePath, false},
		{"wrapper cp to outside", wrapper, bashParams{Command: "cp " + uploads + "/report.txt " + outside + "/leak.txt"}, deniedBashOutsidePath, false},
		{"wrapper write outside", wrapper, bashParams{Command: "touch " + outside + "/new.txt"}, deniedBashOutsidePath, false},
		{"wrapper relative escape", wrapper, bashParams{Command: "cat ../" + filepath.Base(outside) + "/secret.txt"}, deniedBashOutsidePath, false},
		{"wrapper write into uploads", wrapper, bashParams{Command: "touch " + uploads + "/new.txt"}, deniedBashOutsidePath, false},
		{"wrapper cp into uploads", wrapper, bashParams{Command: "cp report.txt " + uploads + "/copy.txt", Workdir: uploads}, deniedBashOutsidePath, false},
		{"wrapper workdir outside", wrapper, bashParams{Command: "ls", Workdir: outside}, deniedBashWorkdir, false},
		{"wrapper relative workdir outside", wrapper, bashParams{Command: "ls", Workdir: "../" + filepath.Base(outside)}, deniedBashWorkdir, false},
		{"wrapper workspace path", wrapper, bashParams{Command: "cat " + workspace.Name() + "/wrap.sh"}, deniedBashOutsidePath, false},
		{"emulated cat outside", emulated, bashParams{Command: "cat /etc/hostname"}, deniedBashOutsidePath, false},
		{"emulated workdir outside", emulated, bashParams{Command: "ls", Workdir: "/etc"}, deniedBashWorkdir, false},
		{"emulated env file in tmp", emulated, bashParams{Command: "cat /tmp/.env"}, deniedBashEnvFile, false},
		{"wrapper cat tmp", wrapper, bashParams{Command: "cat /tmp/x"}, deniedBashOutsidePath, false},
		{"wrapper cd tmp", wrapper, bashParams{Command: "cd /tmp"}, deniedBashOutsidePath, false},
		{"wrapper write tmp", wrapper, bashParams{Command: "touch /tmp/x"}, deniedBashOutsidePath, false},
		{"wrapper workdir tmp", wrapper, bashParams{Command: "ls", Workdir: "/tmp"}, deniedBashWorkdir, false},
		{"wrapper read dev", wrapper, bashParams{Command: "head -c 4 /dev/urandom"}, deniedBashOutsidePath, false},
		{"wrapper read proc", wrapper, bashParams{Command: "grep Pid /proc/self/status"}, deniedBashOutsidePath, false},
		{"default bash read proc", CustomShell{Dir: folder}, bashParams{Command: "cat /proc/self/status"}, deniedBashOutsidePath, false},
		{"emulated read dev", emulated, bashParams{Command: "head -c 4 /dev/urandom"}, deniedBashOutsidePath, false},
		{"emulated read proc", emulated, bashParams{Command: "grep Pid /proc/self/status"}, deniedBashOutsidePath, false},
		{"emulated workdir under tmp outside", emulated, bashParams{Command: "ls", Workdir: "/tmp/../etc"}, deniedBashWorkdir, false},
		{"emulated home escape", emulated, bashParams{Command: "cat ~/../etc/passwd"}, deniedBashOutsidePath, false},
		{"wrapper home", wrapper, bashParams{Command: "cat ~/secret.txt"}, deniedBashOutsidePath, false},
		{"wrapper home write", wrapper, bashParams{Command: "touch ~/new.txt"}, deniedBashOutsidePath, false},
		{"wrapper home workdir", wrapper, bashParams{Command: "ls", Workdir: "~"}, deniedBashWorkdir, false},
		{"default bash home", CustomShell{Dir: folder}, bashParams{Command: "cat ~/secret.txt"}, deniedBashOutsidePath, false},
		{"wrapper reads uploads", wrapper, bashParams{Command: "cat " + uploads + "/report.txt"}, "upload", true},
		{"wrapper copies an upload in", wrapper, bashParams{Command: "cp " + uploads + "/report.txt mine.txt && cat mine.txt"}, "upload", true},
		{"wrapper works in uploads", wrapper, bashParams{Command: "cat report.txt", Workdir: uploads}, "upload", true},
		{"wrapper works in its folder", wrapper, bashParams{Command: "mkdir sub && touch sub/new.txt && echo hi > own.txt && cat own.txt && pwd", Workdir: folder}, "hi\n" + folder + "\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sss.Bash(t.Context(), &tc.shell, &tc.params)
			require.Equal(t, tc.want, got.Output)
			require.Equal(t, tc.success, got.Success)
		})
	}

	for _, params := range []bashParams{{Command: "cat /tmp/x"}, {Command: "cd /tmp"}, {Command: "touch /tmp/x"}, {Command: "mkdir /tmp/d && cp /tmp/x /tmp/d/y"}, {Command: "pwd", Workdir: "/tmp"}, {Command: "cat ~/secret.txt"}, {Command: "touch ~/x"}, {Command: "cd ~"}, {Command: "pwd", Workdir: "~"}} {
		require.NotContains(t, sss.Bash(t.Context(), &emulated, &params).Output, "bash command denied", params)
	}

	for _, name := range []string{"stolen.txt", "leak.txt", "new.txt", "copy.txt"} {
		for _, root := range folders {
			_, err := root.Stat(name)
			require.ErrorIs(t, err, fs.ErrNotExist, "%s in %s", name, root.Name())
		}
	}

	_, err = folders["folder"].Stat("sub/new.txt")
	require.NoError(t, err)
}

func TestBashRefusalsAreTheSameForEveryShell(t *testing.T) {
	workspace, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, workspace.Close()) })

	require.NoError(t, workspace.MkdirAll(".rocketcode/spill", 0o700))
	require.NoError(t, workspace.Mkdir(".tmp", 0o700))
	require.NoError(t, workspace.WriteFile("wrap.sh", []byte("#!/bin/sh\nexec /bin/bash -c \"$1\""), 0o755))

	shellTemp := testShellTempConfig(t, workspace, filepath.Join(workspace.Name(), ".tmp"))
	sss := newSandboxedShellSystem(workspace, &shellTemp, nil, &sandbox.Runtime{}, InertTurnSecrets{})
	sss.spillRel = ".rocketcode/spill"

	folder, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	spill := filepath.Join(workspace.Name(), ".rocketcode", "spill", "result.txt")

	shells := map[string]CustomShell{
		"default":  {},
		"wrapper":  {Path: filepath.Join(workspace.Name(), "wrap.sh"), Dir: folder},
		"emulated": {Emulated: true, Dir: folder},
	}

	for _, tc := range []struct {
		params bashParams
		want   string
	}{
		{bashParams{Command: "cat /etc/hostname"}, deniedBashOutsidePath},
		{bashParams{Command: "touch /etc/rocketclaw-new.txt"}, deniedBashOutsidePath},
		{bashParams{Command: "ls", Workdir: "/var"}, deniedBashWorkdir},
		{bashParams{Command: "ls", Workdir: "/etc"}, deniedBashWorkdir},
		{bashParams{Command: "cat .env"}, deniedBashEnvFile},
		{bashParams{Command: "cat " + spill}, deniedSpillAccess},
		{bashParams{Command: "ls", Workdir: "missing"}, deniedBashMissingWorkdir},
	} {
		for name, shell := range shells {
			got := sss.Bash(t.Context(), &shell, &tc.params)
			require.Equal(t, tc.want, got.Output, "%s: %+v", name, tc.params)
			require.False(t, got.Success)
		}
	}
}

func TestBashOutputAllocationBudget(t *testing.T) {
	cost := testing.Benchmark(BenchmarkBashOutput)
	require.Positive(t, cost.N)
	require.Less(t, cost.AllocedBytesPerOp(), int64(9<<20))
}

func BenchmarkBashOutput(b *testing.B) {
	root, err := os.OpenRoot(b.TempDir())
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, root.Close()) })
	require.NoError(b, root.Mkdir("shell-tmp", 0o700))

	shellTemp := testShellTempConfig(b, root, filepath.Join(root.Name(), "shell-tmp"))
	sss := newSandboxedShellSystem(root, &shellTemp, nil, &sandbox.Runtime{}, InertTurnSecrets{})
	want := strings.Repeat("sample\n\x00", 1<<18)
	require.NoError(b, root.WriteFile("input", []byte(want), 0o600))

	shell := &CustomShell{}
	params := bashParams{Command: "printf 'before\\n'; cat input >&2; printf 'after\\n'; exit 7"}
	want = "before\n" + want + "after\n"

	for b.Loop() {
		got := sss.Bash(b.Context(), shell, &params)
		if got.Output != want || got.ErrorCode != "7" || got.Success {
			b.Fatal("shell changed the output or exit status")
		}
	}
}

func testShellTempConfig(tb testing.TB, root *os.Root, outputDir string) shellTempConfig {
	tb.Helper()

	config, err := newShellTempConfig(root, outputDir)
	require.NoError(tb, err)

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
