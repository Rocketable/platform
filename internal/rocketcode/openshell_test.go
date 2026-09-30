package rocketcode

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
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
	openshellv1 "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
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
	for _, state := range []string{"missing config", "stopped gateway"} {
		t.Run(state, func(t *testing.T) {
			// SDK discovery uses host configuration, outside the workspace abstraction.
			configDir := t.TempDir()

			t.Setenv("XDG_CONFIG_HOME", configDir)

			configRoot, err := os.OpenRoot(configDir)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, configRoot.Close()) })

			if state == "stopped gateway" {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)

				endpoint := "http://" + listener.Addr().String()

				require.NoError(t, listener.Close())

				// NVIDIA/OpenShell@6648bd0c290e sdk/go/openshell/v1/gateway/paths.go.
				require.NoError(t, configRoot.MkdirAll("openshell/gateways/stopped", 0o700))
				require.NoError(t, configRoot.WriteFile("openshell/active_gateway", []byte("stopped\n"), 0o600))

				data, err := json.Marshal(struct {
					Endpoint string `json:"gateway_endpoint"`
					AuthMode string `json:"auth_mode"`
				}{Endpoint: endpoint, AuthMode: "plaintext"})
				require.NoError(t, err)
				require.NoError(t, configRoot.WriteFile("openshell/gateways/stopped/metadata.json", data, 0o600))

				config, err := gateway.LoadConfig("")
				require.NoError(t, err)
				require.Equal(t, endpoint, config.Endpoint)
				require.Equal(t, gateway.AuthModePlaintext, config.AuthMode)
			}

			env := testPromptExpansionEnvironment(t)
			got := env.shell.Bash(t.Context(), bashParams{TimeoutMillisecond: 1000, Command: "printf standard > standard-marker; printf standard"})
			require.Equal(t, BashResult{Output: "standard", Success: true}, got)

			data, err := env.root.ReadFile("standard-marker")
			require.NoError(t, err)
			require.Equal(t, "standard", string(data))

			env.shell.openshellImage = "agent-image"
			got = env.shell.Bash(t.Context(), bashParams{TimeoutMillisecond: 1000, Command: "printf local > marker"})
			require.False(t, got.Success)
			require.Equal(t, "error", got.ErrorCode)
			require.Contains(t, got.Output, "openshell")

			if state == "stopped gateway" {
				require.Contains(t, got.Output, "connection refused")
			}

			_, err = env.root.Stat("marker")
			require.ErrorIs(t, err, os.ErrNotExist)
			require.Equal(t, "before  after", env.expandShellCommands(t.Context(), "before !`printf local > marker` after"))
			_, err = env.root.Stat("marker")
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestOpenShellGatewayMTLS(t *testing.T) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "gateway CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	ca, err = x509.ParseCertificate(caDER)
	require.NoError(t, err)

	pool := x509.NewCertPool()
	pool.AddCert(ca)

	certs := make(map[string]tls.Certificate)

	for i, name := range []string{"server", "client", "untrusted CA"} {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)

		template := &x509.Certificate{
			SerialNumber: big.NewInt(int64(i + 2)), Subject: pkix.Name{CommonName: name},
			NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
			DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		}
		parent, signer := ca, caKey

		if name == "untrusted CA" {
			template.IsCA, template.BasicConstraintsValid = true, true
			template.KeyUsage = x509.KeyUsageCertSign
			parent, signer = template, key
		}

		der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, signer)
		require.NoError(t, err)

		certs[name] = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}

	clientKey, err := x509.MarshalPKCS8PrivateKey(certs["client"].PrivateKey)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certs["server"]},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool,
	})), grpc.UnaryInterceptor(func(ctx context.Context, _ any, info *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
		remote, _ := peer.FromContext(ctx)
		auth := remote.AuthInfo.(credentials.TLSInfo)

		md, _ := metadata.FromIncomingContext(ctx)
		if !bytes.Equal(auth.State.VerifiedChains[0][0].Raw, certs["client"].Certificate[0]) || auth.State.PeerCertificates[0].Subject.CommonName != "client" || len(md.Get("authorization")) != 0 {
			return nil, status.Error(codes.PermissionDenied, "wrong client credentials")
		}

		switch info.FullMethod {
		case openshellv1.OpenShell_CreateSandbox_FullMethodName:
			return nil, status.Error(codes.FailedPrecondition, "authenticated create")
		case openshellv1.OpenShell_DeleteSandbox_FullMethodName:
			return nil, status.Error(codes.FailedPrecondition, "authenticated delete")
		default:
			return nil, status.Error(codes.PermissionDenied, "unexpected RPC")
		}
	}))
	openshellv1.RegisterOpenShellServer(server, openshellv1.UnimplementedOpenShellServer{})

	var serving errgroup.Group
	serving.Go(func() error { return server.Serve(listener) })
	t.Cleanup(func() {
		server.Stop() // Stops the transport and closes the listener before joining.
		require.Contains(t, []error{nil, grpc.ErrServerStopped}, serving.Wait())
	})

	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "trusted", want: "authenticated create"},
		{name: "missing certificate", want: "load client cert"},
		{name: "untrusted CA", want: "certificate signed by unknown authority"},
		{name: "stopped gateway", want: "connection refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// SDK configuration is host-scoped; fixture writes still use *os.Root.
			configDir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configDir)
			configRoot, err := os.OpenRoot(configDir)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, configRoot.Close()) })
			// NVIDIA/OpenShell@6648bd0c290e sdk/go/openshell/v1/gateway/{paths,gateway}.go.
			require.NoError(t, configRoot.MkdirAll("openshell/gateways/local/mtls", 0o700))
			require.NoError(t, configRoot.WriteFile("openshell/active_gateway", []byte("local\n"), 0o600))

			endpoint := listener.Addr().(*net.TCPAddr).Port

			if tc.name == "stopped gateway" {
				stopped, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)

				endpoint = stopped.Addr().(*net.TCPAddr).Port
				require.NoError(t, stopped.Close())
			}

			data, err := json.Marshal(struct {
				Endpoint string `json:"gateway_endpoint"`
				AuthMode string `json:"auth_mode"`
			}{Endpoint: fmt.Sprintf("https://localhost:%d", endpoint), AuthMode: "mtls"})
			require.NoError(t, err)
			require.NoError(t, configRoot.WriteFile("openshell/gateways/local/metadata.json", data, 0o600))

			rootCA := caDER
			if tc.name == "untrusted CA" {
				rootCA = certs["untrusted CA"].Certificate[0]
			}

			require.NoError(t, configRoot.WriteFile("openshell/gateways/local/mtls/ca.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootCA}), 0o600))

			if tc.name != "missing certificate" {
				require.NoError(t, configRoot.WriteFile("openshell/gateways/local/mtls/tls.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certs["client"].Certificate[0]}), 0o600))
			}

			require.NoError(t, configRoot.WriteFile("openshell/gateways/local/mtls/tls.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: clientKey}), 0o600))
			env := testPromptExpansionEnvironment(t)
			env.shell.openshellImage = "agent-image"
			_, err = env.shell.connectOpenShell(t.Context(), "printf local > marker", env.hostDir, io.Discard, io.Discard)
			require.ErrorContains(t, err, tc.want)
			_, err = env.root.Stat("marker")
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestOpenShellCleanupCanOutliveCommandDeadline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stalled bool
		timeout bool
		exit    int
	}{
		{name: "finite cleanup"},
		{name: "stalled cleanup", stalled: true},
		{name: "execution timeout and stalled cleanup", stalled: true, timeout: true},
		{name: "exit 7 and stalled cleanup", stalled: true, exit: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				env := testPromptExpansionEnvironment(t)
				env.shell.openshellImage = "image"

				var diagnostic bytes.Buffer

				logOutput := log.Writer()

				log.SetOutput(&diagnostic)
				t.Cleanup(func() { log.SetOutput(logOutput) })

				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()

				var name string

				sandboxes := &mockSandboxInterface{
					CreateFunc: func(_ context.Context, _, n string, _ *v1.SandboxSpec, _ map[string]string, _ ...v1.CreateOptions) (*v1.Sandbox, error) {
						name = n

						return &v1.Sandbox{Name: name}, nil
					},
					WaitReadyFunc: func(context.Context, string, string, ...v1.WaitOptions) (*v1.Sandbox, error) {
						return &v1.Sandbox{Name: name}, nil
					},
					DeleteFunc: func(cleanup context.Context, _, n string, opts ...v1.DeleteOptions) (*v1.DeletionResult, error) {
						require.Equal(t, name, n)
						require.True(t, opts[0].AllowMissing)
						require.NoError(t, cleanup.Err())

						deadline, ok := cleanup.Deadline()
						require.True(t, ok, "cleanup must have its own deadline")
						require.Equal(t, 2*time.Minute, time.Until(deadline))

						if tc.stalled {
							started := time.Now()

							<-cleanup.Done()
							require.Equal(t, 2*time.Minute, time.Since(started))

							return nil, &v1.StatusError{Code: v1.ErrorDeadlineExceeded, Message: "deadline"}
						}

						time.Sleep(2 * time.Second)
						require.NoError(t, cleanup.Err())
						require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)

						return &v1.DeletionResult{Outcome: v1.DeletionCompleted}, nil
					},
				}
				chunks := []*v1.ExecChunk{{Data: []byte("partial stdout"), Stream: v1.StreamStdout}, {Data: []byte("stderr"), Stream: v1.StreamStderr}}
				stream := &mockExecStream{
					NextFunc: func() (*v1.ExecChunk, error) {
						if len(chunks) > 0 {
							chunk := chunks[0]
							chunks = chunks[1:]

							return chunk, nil
						}

						time.Sleep(500 * time.Millisecond)

						if tc.timeout {
							<-ctx.Done()

							return nil, &v1.StatusError{Code: v1.ErrorDeadlineExceeded, Message: "deadline"}
						}

						return nil, io.EOF
					},
					ExitCodeFunc: func() (int, error) { return tc.exit, nil },
					CloseFunc:    func() error { return nil },
				}
				executor := &mockExecInterface{StreamFunc: func(context.Context, string, string, []string, ...v1.ExecOptions) (v1.ExecStream, error) {
					return stream, nil
				}}

				var output bytes.Buffer

				exit, err := env.shell.executeOpenShell(ctx, sandboxes, executor, "printf test", env.hostDir, &output, io.Discard)
				require.Equal(t, "partial stdout", output.String())
				require.Equal(t, tc.exit, exit)

				if tc.timeout {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				} else {
					require.NotErrorIs(t, err, context.DeadlineExceeded)
				}

				if tc.stalled {
					require.ErrorContains(t, err, "openshell cleanup "+name+": cleanup deadline exceeded; deletion unconfirmed")
					require.Contains(t, diagnostic.String(), "openshell cleanup "+name+": cleanup deadline exceeded; deletion unconfirmed")
				} else {
					require.NoError(t, err)
					require.Empty(t, diagnostic.String())
				}
			})
		})
	}
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
	cfg, err := gateway.LoadConfig("")
	require.NoError(t, err)
	t.Logf("active gateway auth mode=%s", cfg.AuthMode)

	var opts []gateway.ClientOption
	if cfg.AuthMode == gateway.AuthModeMTLS {
		// The inventory client needs the same pinned-SDK mTLS override as the adapter.
		opts = append(opts, gateway.WithAuth(v1.NoAuth()), gateway.WithTLS(&v1.TLSConfig{
			CAFile:   filepath.Join(cfg.Dir, "mtls", "ca.crt"),
			CertFile: filepath.Join(cfg.Dir, "mtls", "tls.crt"),
			KeyFile:  filepath.Join(cfg.Dir, "mtls", "tls.key"),
		}))
	}

	client, err := gateway.NewClient(cfg.Name, opts...)
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
