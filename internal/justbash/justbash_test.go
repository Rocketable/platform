package justbash

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type bashCall struct {
	t        *testing.T
	session  *Session
	launcher string
	dir      string
	tmp      string
}

// newBashCall registers turn and returns a way to run bash calls the way RocketCode runs a customShell.
func newBashCall(t *testing.T, server *Server, turn Turn) *bashCall {
	t.Helper()

	_, err := exec.LookPath("node")
	require.NoError(t, err, "just-bash tests need node on PATH")

	session, err := server.Register(turn)
	require.NoError(t, err)

	launcher, err := server.Launcher("main")
	require.NoError(t, err)

	tmp := filepath.Join(turn.RuntimeDir, ".rocketcode", "tmp", "conv1")
	require.NoError(t, os.MkdirAll(tmp, 0o700))

	return &bashCall{t: t, session: session, launcher: launcher, dir: turn.Workspace, tmp: tmp}
}

func (c *bashCall) run(command string, env ...string) (stdout, stderr string, code int) {
	c.t.Helper()

	cmd := exec.CommandContext(c.t.Context(), c.launcher, command)
	cmd.Dir = c.dir
	cmd.Env = append(os.Environ(), "TMPDIR="+c.tmp)

	for k, v := range maps.All(c.session.Env()) {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	cmd.Env = append(cmd.Env, env...)

	var out, errOut bytes.Buffer

	cmd.Stdout, cmd.Stderr = &out, &errOut

	err := cmd.Run()
	if _, isExit := errors.AsType[*exec.ExitError](err); err != nil && !isExit {
		require.NoError(c.t, err)
	}

	return out.String(), errOut.String(), cmd.ProcessState.ExitCode()
}

func TestBashCalls(t *testing.T) {
	t.Parallel()

	listed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("from listed")) }))
	t.Cleanup(listed.Close)

	unlisted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("from unlisted")) }))
	t.Cleanup(unlisted.Close)

	server, _ := testServer(t, map[string]string{"arn:1": `{"KEY":"s3cret"}`})
	turn := testTurn(t, map[string]Settings{"main": settings(t, `secretsARN: [arn:1]
commands:
  hello: "printf '%s|' pre"
  show-x: "printf '%s' \"$X\""
  big: "head -c 6291456 /dev/zero | tr '\\0' x"
allowedNetworkTargets: [`+strings.TrimPrefix(listed.URL, "http://")+`]
`)})
	require.NoError(t, os.WriteFile(filepath.Join(turn.Workspace, "outside.txt"), []byte("outside"), 0o644))

	call := newBashCall(t, server, turn)
	folder := filepath.Join(turn.Workspace, "justbash", "conv1")

	cases := []struct {
		name, command, stdout string
		failed                bool
	}{
		{"writes land on disk", "echo hi > out.txt; cat out.txt; pwd", "hi\n" + folder + "\n", false},
		{"outside the folder is invisible", "cat " + filepath.Join(turn.Workspace, "outside.txt"), "", true},
		{"tmp and dev null", "f=$(mktemp) && echo t > $f && cat $f && echo gone > /dev/null", "t\n", false},
		{"python", `python3 -c "print(6*7)"`, "42\n", false},
		{"javascript", `js-exec -c "console.log(6*7)"`, "42\n", false},
		{"sqlite", `sqlite3 :memory: "select 6*7"`, "42\n", false},
		{"secret", `printf %s "$KEY"`, "s3cret", false},
		{"host command", "hello 'a b' c", "pre|a b|c|", false},
		{"host command sees exported vars", "export X=1; show-x", "1", false},
		{"host output above gRPC default", "big | wc -c | tr -d ' '", "6291456\n", false},
		{"listed network", "curl -sS " + listed.URL + "/", "from listed", false},
		{"unlisted network", "curl -sS " + unlisted.URL + "/", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := call.run(tc.command)
			require.Equal(t, tc.stdout, stdout, stderr)
			require.Equal(t, tc.failed, code != 0, stderr)
		})
	}

	data, err := os.ReadFile(filepath.Join(folder, "out.txt"))
	require.NoError(t, err)
	require.Equal(t, "hi\n", string(data))

	// The agent's own file lies outside the mounts: a write lands in the call's in-memory filesystem.
	agentFile := filepath.Join(turn.RuntimeDir, "agents", "main.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(agentFile), 0o755))
	require.NoError(t, os.WriteFile(agentFile, []byte("---\nshell: just-bash\n---\n"), 0o644))
	call.run("echo 'customShell: /bin/sh' >> " + agentFile)

	data, err = os.ReadFile(agentFile)
	require.NoError(t, err)
	require.Equal(t, "---\nshell: just-bash\n---\n", string(data))
}

func TestBashCallTmpInsideFolder(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t, nil)
	turn := testTurn(t, map[string]Settings{"main": settings(t, "workingDirectory: data\n")})
	call := newBashCall(t, server, turn)
	call.tmp = filepath.Join(turn.Workspace, "data", "tmp")
	require.NoError(t, os.MkdirAll(call.tmp, 0o700))

	stdout, stderr, code := call.run("f=$(mktemp) && echo nested > $f && cat $f")
	require.Equal(t, 0, code, stderr)
	require.Equal(t, "nested\n", stdout)
}

func TestBashCallFailures(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t, nil)
	call := newBashCall(t, server, testTurn(t, map[string]Settings{"main": settings(t, "")}))

	_, stderr, code := call.run("echo hi", "ROCKETCLAW_JUSTBASH_TOKEN=wrong")
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr, "unknown or ended just-bash session")

	_, stderr, code = call.run("echo hi", "PATH=/nonexistent")
	require.Equal(t, 127, code)
	require.Contains(t, stderr, "just-bash needs node 20.19 or later on PATH")
}

func TestBashCallInterpretersOutliveDefaultLimit(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t, nil)
	call := newBashCall(t, server, testTurn(t, map[string]Settings{"main": settings(t, "")}))

	stdout, stderr, code := call.run(`python3 -c "import time; time.sleep(35); print(1)"`)
	require.Equal(t, 0, code, stderr)
	require.Equal(t, "1\n", stdout)
}

func TestBashCallKilledStopsHostCommand(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t, nil)
	turn := testTurn(t, map[string]Settings{"main": settings(t, `commands: {spawn: "bash -c 'sleep 30 & echo $! > \"$0\"; wait'"}`+"\n")})
	call := newBashCall(t, server, turn)
	pidFile := filepath.Join(turn.Workspace, "pid")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Like RocketCode's bash, run the launcher in its own process group and stop the group.
	cmd := exec.CommandContext(ctx, call.launcher, "spawn "+pidFile)
	cmd.Dir = call.dir
	cmd.Env = append(os.Environ(), "TMPDIR="+call.tmp)

	for k, v := range maps.All(call.session.Env()) {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())

	var pid int

	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))

		return err == nil && pid > 0
	}, 20*time.Second, 10*time.Millisecond)

	require.NoError(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM))

	_ = cmd.Wait()

	require.Eventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }, 10*time.Second, 10*time.Millisecond, "the host command's child is gone")
}
