package rocketcode

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"testing/synctest"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/stretchr/testify/require"
)

func TestOpenShellCommandLifecycle(t *testing.T) {
	env := testPromptExpansionEnvironment(t)
	sss := env.shell
	sss.openshellImage = "agent-image"
	sss.env = []string{"ROCKETCLAW_CONVERSATION_ID=configured", "TMPDIR=/wrong"}
	var names []string
	for _, tc := range []struct {
		name      string
		exit      int
		errStream error
		errExit   error
		outcome   v1.DeletionOutcome
		errDelete error
	}{
		{name: "success", outcome: v1.DeletionCompleted},
		{name: "exit", exit: 7, outcome: v1.DeletionCompleted},
		{name: "partial transport", errStream: errors.New("lost transport"), outcome: v1.DeletionCompleted},
		{name: "missing exit", errExit: errors.New("stream ended without exit event"), outcome: v1.DeletionCompleted},
		{name: "incomplete cleanup", outcome: v1.DeletionAccepted},
		{name: "failed cleanup", errDelete: errors.New("gateway unreachable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			var output bytes.Buffer
			exit, err := sss.executeOpenShell(t.Context(), sandboxes, executor, "printf test", env.hostDir, &output, &output)
			require.Equal(t, "stdoutstderr", output.String())
			require.Equal(t, tc.exit, exit)
			if tc.errStream != nil || tc.errExit != nil || tc.errDelete != nil || tc.outcome == v1.DeletionAccepted {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestOpenShellFailuresCleanUpOwnedName(t *testing.T) {
	for _, stage := range []string{"create", "ready", "stream", "deadline"} {
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
					return nil, errBackend
				}}
				_, err := env.shell.executeOpenShell(ctx, sandboxes, executor, "touch marker", env.hostDir, io.Discard, io.Discard)
				if stage == "deadline" {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				} else {
					require.ErrorIs(t, err, errBackend)
				}
				require.True(t, deleted)
				if stage != "stream" {
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
