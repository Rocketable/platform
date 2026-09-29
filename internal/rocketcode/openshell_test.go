package rocketcode

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/gateway"
	"github.com/stretchr/testify/require"
)

func TestOpenShellCommandLifecycle(t *testing.T) {
	env := testPromptExpansionEnvironment(t)
	sss := env.shell
	sss.openshellImage = "agent-image"
	sss.env = []string{"ROCKETCLAW_CONVERSATION_ID=configured", "TMPDIR=/wrong"}

	var names []string

	for _, tc := range []struct {
		name       string
		exit       int
		errStream  error
		errExit    error
		outcome    v1.DeletionOutcome
		errDelete  error
		stdoutOnly bool
	}{
		{name: "success", outcome: v1.DeletionCompleted},
		{name: "exit", exit: 7, outcome: v1.DeletionCompleted},
		{name: "partial transport", errStream: errors.New("lost transport"), outcome: v1.DeletionCompleted},
		{name: "missing exit", errExit: errors.New("stream ended without exit event"), outcome: v1.DeletionCompleted},
		{name: "incomplete cleanup", outcome: v1.DeletionAccepted},
		{name: "failed cleanup", errDelete: errors.New("gateway unreachable")},
		{name: "prompt output", outcome: v1.DeletionCompleted, stdoutOnly: true},
		{name: "partial prompt incomplete cleanup", errStream: errors.New("lost transport"), outcome: v1.DeletionAccepted, stdoutOnly: true},
		{name: "partial prompt failed cleanup", errStream: errors.New("lost transport"), errDelete: errors.New("gateway unreachable"), stdoutOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var diagnostic bytes.Buffer

			logOutput := log.Writer()

			log.SetOutput(&diagnostic)
			t.Cleanup(func() { log.SetOutput(logOutput) })

			var name string

			closed := false
			sandboxes := &mockSandboxInterface{
				CreateFunc: func(_ context.Context, workspace, n string, spec *v1.SandboxSpec, _ map[string]string, _ ...v1.CreateOptions) (*v1.Sandbox, error) {
					require.Empty(t, workspace)

					name = n
					require.NotContains(t, names, name)
					names = append(names, name)

					require.Equal(t, "agent-image", spec.Template.Image)
					require.Equal(t, map[string]any{"mounts": []any{map[string]any{"type": "bind", "source": env.hostDir, "target": env.hostDir, "read_only": false}}}, spec.Template.DriverConfig)
					require.Contains(t, spec.Policy.Filesystem.ReadWrite, env.hostDir)
					require.Empty(t, spec.Policy.NetworkPolicies)

					return &v1.Sandbox{Name: name}, nil
				},
				WaitReadyFunc: func(_ context.Context, workspace, n string, _ ...v1.WaitOptions) (*v1.Sandbox, error) {
					require.Empty(t, workspace)
					require.Equal(t, name, n)

					return &v1.Sandbox{Name: name}, nil
				},
				DeleteFunc: func(ctx context.Context, workspace, n string, opts ...v1.DeleteOptions) (*v1.DeletionResult, error) {
					require.True(t, closed)
					require.NoError(t, ctx.Err())
					require.Empty(t, workspace)
					require.Equal(t, name, n)
					require.True(t, opts[0].AllowMissing)

					return &v1.DeletionResult{Outcome: tc.outcome}, tc.errDelete
				},
			}
			chunks := []*v1.ExecChunk{{Data: []byte("stdout"), Stream: v1.StreamStdout}, {Data: []byte("stderr"), Stream: v1.StreamStderr}}
			stream := &mockExecStream{
				NextFunc: func() (*v1.ExecChunk, error) {
					if len(chunks) > 0 {
						chunk := chunks[0]
						chunks = chunks[1:]

						return chunk, nil
					}

					if tc.errStream != nil {
						return nil, tc.errStream
					}

					return nil, io.EOF
				},
				ExitCodeFunc: func() (int, error) { return tc.exit, tc.errExit },
				CloseFunc:    func() error { closed = true; return nil },
			}
			executor := &mockExecInterface{StreamFunc: func(_ context.Context, workspace, n string, argv []string, opts ...v1.ExecOptions) (v1.ExecStream, error) {
				require.Empty(t, workspace)
				require.Equal(t, name, n)
				require.Equal(t, []string{"/bin/bash", "--noprofile", "--norc", "-p", "-c", "printf test"}, argv)
				require.Equal(t, env.hostDir, opts[0].WorkDir)
				require.Equal(t, sss.shellTemp.tmpDir, opts[0].Env["TMPDIR"])
				require.Equal(t, "configured", opts[0].Env["ROCKETCLAW_CONVERSATION_ID"])
				require.Len(t, opts[0].Env, 2)
				require.True(t, opts[0].NoLoginShell)

				return stream, nil
			}}

			var (
				output bytes.Buffer
				stderr io.Writer = &output
			)
			if tc.stdoutOnly {
				stderr = io.Discard
			}

			exit, err := sss.executeOpenShell(t.Context(), sandboxes, executor, "printf test", env.hostDir, &output, stderr)
			if tc.stdoutOnly {
				require.Equal(t, "stdout", output.String())
			} else {
				require.Equal(t, "stdoutstderr", output.String())
			}

			require.Equal(t, tc.exit, exit)

			if tc.errStream != nil {
				require.ErrorIs(t, err, tc.errStream)
			}

			switch {
			case tc.errDelete != nil:
				require.ErrorIs(t, err, tc.errDelete)
				require.Contains(t, diagnostic.String(), "openshell cleanup "+name+": "+tc.errDelete.Error())
			case tc.outcome == v1.DeletionAccepted:
				require.Contains(t, diagnostic.String(), "openshell cleanup "+name+": deletion incomplete")
			default:
				require.Empty(t, diagnostic.String())
			}

			if tc.errStream != nil || tc.errExit != nil || tc.errDelete != nil || tc.outcome == v1.DeletionAccepted {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestOpenShellFailuresCleanUpOwnedName(t *testing.T) {
	for _, stage := range []string{"create", "ready", "stream", "deadline", "exec deadline"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				env := testPromptExpansionEnvironment(t)
				env.shell.openshellImage = "image"

				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()

				var name string

				errBackend := errors.New(stage + " failed")
				deleted := false
				sandboxes := &mockSandboxInterface{
					CreateFunc: func(_ context.Context, _ string, n string, _ *v1.SandboxSpec, _ map[string]string, _ ...v1.CreateOptions) (*v1.Sandbox, error) {
						name = n

						if stage == "create" {
							return nil, errBackend
						}

						return &v1.Sandbox{Name: name}, nil
					},
					WaitReadyFunc: func(ctx context.Context, _, _ string, _ ...v1.WaitOptions) (*v1.Sandbox, error) {
						if stage == "deadline" {
							<-ctx.Done()
							return nil, ctx.Err()
						}

						if stage == "ready" {
							return nil, errBackend
						}

						return &v1.Sandbox{Name: name}, nil
					},
					DeleteFunc: func(ctx context.Context, _, n string, opts ...v1.DeleteOptions) (*v1.DeletionResult, error) {
						require.NoError(t, ctx.Err())
						require.Equal(t, name, n)
						require.True(t, opts[0].AllowMissing)

						deleted = true

						return &v1.DeletionResult{Outcome: v1.DeletionAlreadyAbsent}, nil
					},
				}
				executor := &mockExecInterface{StreamFunc: func(context.Context, string, string, []string, ...v1.ExecOptions) (v1.ExecStream, error) {
					if stage == "exec deadline" {
						return &mockExecStream{NextFunc: func() (*v1.ExecChunk, error) {
							<-ctx.Done()
							return nil, &v1.StatusError{Code: v1.ErrorDeadlineExceeded, Message: "deadline"}
						}, CloseFunc: func() error { return nil }}, nil
					}

					return nil, errBackend
				}}

				_, err := env.shell.executeOpenShell(ctx, sandboxes, executor, "touch marker", env.hostDir, io.Discard, io.Discard)
				if stage == "deadline" || stage == "exec deadline" {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				} else {
					require.ErrorIs(t, err, errBackend)
				}

				require.True(t, deleted)

				if stage != "stream" && stage != "exec deadline" {
					require.Empty(t, executor.StreamCalls())
				}

				_, err = env.root.Stat("marker")
				require.ErrorIs(t, err, os.ErrNotExist)
			})
		})
	}
}

func TestOpenShellUnavailableDoesNotRunLocally(t *testing.T) {
	// Gateway discovery reads host configuration, not workspace files.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	env := testPromptExpansionEnvironment(t)
	env.shell.openshellImage = "agent-image"
	got := env.shell.Bash(t.Context(), bashParams{Command: "printf local > marker"})
	require.False(t, got.Success)
	require.Equal(t, "error", got.ErrorCode)
	require.Contains(t, got.Output, "openshell")

	_, err := env.root.Stat("marker")
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Equal(t, "before  after", env.expandShellCommands(t.Context(), "before !`printf local > marker` after"))
	_, err = env.root.Stat("marker")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestOpenShellCleanupCanOutliveCommandDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		env := testPromptExpansionEnvironment(t)
		env.shell.openshellImage = "image"

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		sandboxes := &mockSandboxInterface{
			CreateFunc: func(context.Context, string, string, *v1.SandboxSpec, map[string]string, ...v1.CreateOptions) (*v1.Sandbox, error) {
				return &v1.Sandbox{}, nil
			},
			WaitReadyFunc: func(context.Context, string, string, ...v1.WaitOptions) (*v1.Sandbox, error) {
				return &v1.Sandbox{}, nil
			},
			DeleteFunc: func(cleanup context.Context, _, _ string, _ ...v1.DeleteOptions) (*v1.DeletionResult, error) {
				time.Sleep(2 * time.Second)
				require.NoError(t, cleanup.Err())
				require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)

				return &v1.DeletionResult{Outcome: v1.DeletionCompleted}, nil
			},
		}
		stream := &mockExecStream{NextFunc: func() (*v1.ExecChunk, error) { return nil, io.EOF }, ExitCodeFunc: func() (int, error) { return 0, nil }, CloseFunc: func() error { return nil }}
		executor := &mockExecInterface{StreamFunc: func(context.Context, string, string, []string, ...v1.ExecOptions) (v1.ExecStream, error) {
			return stream, nil
		}}
		exit, err := env.shell.executeOpenShell(ctx, sandboxes, executor, "true", env.hostDir, io.Discard, io.Discard)
		require.NoError(t, err)
		require.Zero(t, exit)
	})
}

func TestOpenShellGatewayIntegration(t *testing.T) {
	image := os.Getenv("ROCKETCODE_OPENSHELL_TEST_IMAGE")
	if image == "" {
		t.Skip("set ROCKETCODE_OPENSHELL_TEST_IMAGE to test the active local gateway")
	}

	engine := os.Getenv("ROCKETCODE_OPENSHELL_TEST_ENGINE")
	require.Contains(t, []string{"docker", "podman"}, engine)
	t.Logf("OpenShell v0.1.2 (6648bd0c290e), OS=%s, engine=%s, image=%s", runtime.GOOS, engine, image)

	repo, err := os.OpenRoot(filepath.Join("..", ".."))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, repo.Close()) })
	require.NoError(t, repo.MkdirAll(".tmp", 0o700))
	// Host API creates the fixture root; all fixtures below use *os.Root.
	dir, err := os.MkdirTemp(filepath.Join(repo.Name(), ".tmp"), "openshell-integration-")
	require.NoError(t, err)

	rel := filepath.Join(".tmp", filepath.Base(dir))

	t.Cleanup(func() { require.NoError(t, repo.RemoveAll(rel)) })

	root, err := repo.OpenRoot(rel)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("nested", 0o755))
	abs, err := filepath.Abs(root.Name())
	require.NoError(t, err)
	shellTemp := testPromptShellTempConfig(t, root, abs)
	env, err := newPromptExpansionEnvironment(root, shellTemp, []string{"ROCKETCLAW_CONVERSATION_ID=integration"}, DefaultShellCommand)
	require.NoError(t, err)

	env.shell.openshellImage = image
	client, err := gateway.NewClient("")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	before, err := client.Sandboxes().ListAll(t.Context(), "")
	require.NoError(t, err)
	got := env.shell.Bash(t.Context(), bashParams{Workdir: "nested", Command: `pwd; printf shared > shared; printf private > "$TMPDIR/private"; printf '%s' "$ROCKETCLAW_CONVERSATION_ID"`})
	require.True(t, got.Success, got.Output)
	require.Equal(t, filepath.Join(abs, "nested")+"\nintegration", got.Output)

	data, err := root.ReadFile("nested/shared")
	require.NoError(t, err)
	require.Equal(t, "shared", string(data))
	data, err = root.ReadFile(filepath.Join(shellTemp.tmpRelDir, "private"))
	require.NoError(t, err)
	require.Equal(t, "private", string(data))

	info, err := root.Stat(shellTemp.tmpRelDir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	got = env.shell.Bash(t.Context(), bashParams{Command: "printf stdout; printf stderr >&2; exit 7"})
	require.Equal(t, BashResult{Output: "stdoutstderr", ErrorCode: "7"}, got)
	require.Equal(t, "prefix stdout suffix", env.expandShellCommands(t.Context(), "prefix !`printf stdout; printf stderr >&2; exit 7` suffix"))
	got = env.shell.Bash(t.Context(), bashParams{Command: "printf transient > /sandbox/rocketcode-container-only"})
	require.True(t, got.Success, got.Output)
	got = env.shell.Bash(t.Context(), bashParams{Command: "test ! -e /sandbox/rocketcode-container-only && test -f nested/shared"})
	require.True(t, got.Success, got.Output)
	// External processes need wall-clock observation, not synctest fake time.
	got = env.shell.Bash(t.Context(), bashParams{TimeoutMillisecond: 15000, Command: `printf started; (while true; do printf x >> descendant; sleep 0.1; done) & wait`})
	require.False(t, got.Success)
	require.Equal(t, "timeout", got.ErrorCode)
	require.Contains(t, got.Output, "started")
	require.NotContains(t, got.Output, "cleanup")

	data, err = root.ReadFile("descendant")
	require.NoError(t, err)
	time.Sleep(300 * time.Millisecond)

	stopped, err := root.ReadFile("descendant")
	require.NoError(t, err)
	require.Equal(t, data, stopped, "descendant must stop before cleanup returns")
	got = env.shell.Bash(t.Context(), bashParams{Command: "printf independent"})
	require.Equal(t, BashResult{Output: "independent", Success: true}, got)
	after, err := client.Sandboxes().ListAll(t.Context(), "")
	require.NoError(t, err)

	for _, sandbox := range after {
		if strings.HasPrefix(sandbox.Name, "rocketcode-") {
			require.True(t, slices.ContainsFunc(before, func(existing *v1.Sandbox) bool { return existing.ID == sandbox.ID }), "owned sandbox %s remains after cleanup", sandbox.Name)
		}
	}

	t.Log("same-path writes, private temp, output/exit, ephemeral state, descendant termination, and cleanup passed")
}
