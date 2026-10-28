package justbash

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Rocketable/platform/internal/justbash/justbashpb"
)

// testServer starts a server whose cache dir has a short path, because macOS limits Unix socket paths
// to 104 bytes.
func testServer(t *testing.T, secrets map[string]string) (*Server, *secretFetcherMock) {
	t.Helper()

	cache, err := os.MkdirTemp("", "jb")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(cache) })

	fetcher := &secretFetcherMock{SecretStringFunc: func(arn string) (string, error) {
		body, ok := secrets[arn]
		if !ok {
			return "", errors.New("no such secret")
		}

		return body, nil
	}}

	server := NewServer(cache, filepath.Join(cache, "rocketclaw.json"), fetcher)

	t.Cleanup(func() { require.NoError(t, server.Close()) })

	return server, fetcher
}

func testTurn(t *testing.T, agents map[string]Settings) Turn {
	t.Helper()

	workspace, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw"), 0o755))

	return Turn{
		Workspace:  workspace,
		RuntimeDir: filepath.Join(workspace, ".rocketclaw"),
		Agents:     agents,
		Env:        map[string]string{"ROCKETCLAW_CONVERSATION_ID": "conv1"},
	}
}

func settings(t *testing.T, doc string) Settings {
	t.Helper()

	got, enabled, err := ParseSettings(frontmatter(t, "shell: just-bash\n"+doc))
	require.NoError(t, err)
	require.True(t, enabled)

	return got
}

func TestSetup(t *testing.T) {
	t.Parallel()

	server, fetcher := testServer(t, map[string]string{"arn:1": `{"A":"1"}`, "arn:2": `{"B":"2"}`, "arn:bad": "not json"})
	turn := testTurn(t, map[string]Settings{
		"main": settings(t, "secretsARN: [arn:1, arn:2]\ncommands: {zeta: x, alpha: y}\n"),
		"bad":  settings(t, "secretsARN: [arn:bad]\n"),
	})

	session, err := server.Register(turn)
	require.NoError(t, err)

	sb := sandbox{server}

	for range 2 {
		resp, err := sb.Setup(t.Context(), &justbashpb.SetupRequest{Token: session.token, Agent: "main"})
		require.NoError(t, err)
		require.Equal(t, filepath.Join(turn.Workspace, "justbash", "conv1"), resp.GetWorkingDirectory())
		require.DirExists(t, resp.GetWorkingDirectory())
		require.Equal(t, map[string]string{"A": "1", "B": "2"}, resp.GetEnv())
		require.Equal(t, []string{"alpha", "zeta"}, resp.GetCommands())
	}

	require.Len(t, fetcher.SecretStringCalls(), 2, "secrets load once per session")

	_, err = sb.Setup(t.Context(), &justbashpb.SetupRequest{Token: session.token, Agent: "bad"})
	require.ErrorContains(t, err, "secret arn:bad is not a JSON object of strings")
	require.NotContains(t, err.Error(), "not json")

	_, err = sb.Setup(t.Context(), &justbashpb.SetupRequest{Token: session.token, Agent: "other"})
	require.Equal(t, codes.NotFound, status.Code(err))

	session.Release()

	_, err = sb.Setup(t.Context(), &justbashpb.SetupRequest{Token: session.token, Agent: "main"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestWorkingDirectoryRules(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t, nil)

	cases := []struct {
		dir, wantErr string
	}{
		{"data", ""},
		{"$PWD/somewhere/$ROCKETCLAW_CONVERSATION_ID", ""},
		{".", "contains"},
		{".rocketclaw", "lies inside"},
		{".rocketclaw/x", "lies inside"},
		{"/", "contains"},
		{filepath.Dir(server.cacheDir), "contains"},
	}
	for _, tc := range cases {
		turn := testTurn(t, map[string]Settings{"main": settings(t, "workingDirectory: "+strconv.Quote(tc.dir)+"\n")})

		session, err := server.Register(turn)
		require.NoError(t, err)

		_, err = sandbox{server}.Setup(t.Context(), &justbashpb.SetupRequest{Token: session.token, Agent: "main"})
		if tc.wantErr == "" {
			require.NoError(t, err, tc.dir)
		} else {
			require.ErrorContains(t, err, tc.wantErr, tc.dir)
			require.ErrorContains(t, err, "workingDirectory")
		}
	}
}

func TestRunCommand(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t, nil)
	turn := testTurn(t, map[string]Settings{"main": settings(t, `commands:
  hello: "printf '%s|' pre"
  where: "pwd; printf '%s' \"$X\""
  echo-stdin: cat
`)})
	session, err := server.Register(turn)
	require.NoError(t, err)

	sb := sandbox{server}
	dir := filepath.Join(turn.Workspace, "justbash", "conv1")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))

	run := func(name string, args []string, env map[string]string, cwd, stdin string) *justbashpb.RunCommandResponse {
		t.Helper()

		resp, err := sb.RunCommand(t.Context(), &justbashpb.RunCommandRequest{Token: session.token, Agent: "main", Name: name, Args: args, Env: env, Cwd: cwd, Stdin: []byte(stdin)})
		require.NoError(t, err)

		return resp
	}

	resp := run("hello", []string{"a b", "c"}, nil, "/", "")
	require.Equal(t, "pre|a b|c|", string(resp.GetStdout()))
	require.Equal(t, int32(0), resp.GetExitCode())

	resp = run("where", nil, map[string]string{"X": "1"}, filepath.Join(dir, "sub"), "")
	require.Equal(t, filepath.Join(dir, "sub")+"\n1", string(resp.GetStdout()), "cwd inside the folder is kept and the script's variables reach the command")

	resp = run("where", nil, nil, "/elsewhere", "")
	require.Equal(t, dir+"\n", string(resp.GetStdout()), "cwd outside the folder falls back to the folder")

	require.Equal(t, "in", string(run("echo-stdin", nil, nil, dir, "in").GetStdout()))

	_, err = sb.RunCommand(t.Context(), &justbashpb.RunCommandRequest{Token: session.token, Agent: "main", Name: "rm"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestRunCommandIgnoresSandboxShellHooks(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t, nil)
	turn := testTurn(t, map[string]Settings{"main": settings(t, "commands: {hello: 'printf ok'}\n")})
	session, err := server.Register(turn)
	require.NoError(t, err)

	dir := filepath.Join(turn.Workspace, "justbash", "conv1")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "bin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hook"), []byte("echo HOOKED\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bin", "printf"), []byte("#!/bin/sh\necho HIJACKED\n"), 0o755))

	resp, err := sandbox{server}.RunCommand(t.Context(), &justbashpb.RunCommandRequest{
		Token: session.token, Agent: "main", Name: "hello", Cwd: dir,
		Env: map[string]string{"BASH_ENV": filepath.Join(dir, "hook"), "PATH": filepath.Join(dir, "bin"), "LD_PRELOAD": "x"},
	})
	require.NoError(t, err)
	require.Equal(t, "ok", string(resp.GetStdout()))
}

func TestRunCommandCancelStopsProcessGroup(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t, nil)
	turn := testTurn(t, map[string]Settings{"main": settings(t, `commands: {spawn: "bash -c 'sleep 30 & echo $! > \"$0\"; wait'"}`+"\n")})
	session, err := server.Register(turn)
	require.NoError(t, err)

	pidFile := filepath.Join(turn.Workspace, "pid")
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)

	go func() {
		_, err := sandbox{server}.RunCommand(ctx, &justbashpb.RunCommandRequest{Token: session.token, Agent: "main", Name: "spawn", Args: []string{pidFile}})
		done <- err
	}()

	var pid int

	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))

		return err == nil && pid > 0
	}, 10*time.Second, 10*time.Millisecond)

	cancel()

	select {
	case errRun := <-done:
		t.Log("RunCommand after cancel:", errRun)
	case <-time.After(10 * time.Second):
		t.Fatal("RunCommand did not return after cancel")
	}

	require.Eventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }, 5*time.Second, 10*time.Millisecond, "the host command's child is gone")
}

func TestFetchRefusalIsPermissionDenied(t *testing.T) {
	t.Parallel()

	server, _ := testServer(t, nil)
	session, err := server.Register(testTurn(t, map[string]Settings{"main": settings(t, "allowedNetworkTargets: [api.example.com]\n")}))
	require.NoError(t, err)

	_, err = sandbox{server}.Fetch(t.Context(), &justbashpb.FetchRequest{Token: session.token, Agent: "main", Url: "http://127.0.0.1:1/"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
