package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime/pprof"
	"runtime/trace"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestDiagnosticsRoutes(t *testing.T) {
	var connections sync.WaitGroup

	server := newDiagnosticsServer(t.Context(), &connections)
	paths := []string{"", "cmdline", "symbol"}
	for _, profile := range pprof.Profiles() {
		paths = append(paths, profile.Name())
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/debug/pprof/"+path, http.NoBody))
			require.Equal(t, http.StatusOK, response.Code)

			switch path {
			case "":
				require.Contains(t, response.Body.String(), "Types of profiles available")
			case "cmdline":
				require.Equal(t, strings.Join(os.Args, "\x00"), response.Body.String())
			case "symbol":
				require.Equal(t, "num_symbols: 1\n", response.Body.String())
			default:
				require.Equal(t, "application/octet-stream", response.Header().Get("Content-Type"))
				reader, err := gzip.NewReader(response.Body)
				require.NoError(t, err)
				data, err := io.ReadAll(reader)
				require.NoError(t, err)
				require.NoError(t, reader.Close())
				require.NotEmpty(t, data)
			}
		})
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/debug/requests", http.NoBody)
	request.RemoteAddr = "127.0.0.1:12345"
	server.Handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code, "gRPC debug pages are not pprof routes")
}

func TestDiagnosticsServeFailureIsLoggedImmediately(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	var connections sync.WaitGroup

	server := newDiagnosticsServer(t.Context(), &connections)

	var logs bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&logs, nil)).With("process_start", "test-start")

	var serving errgroup.Group

	serving.Go(func() error {
		serveDiagnostics(server, listener, logger)
		return nil
	})
	t.Cleanup(func() {
		require.NoError(t, server.Close())
		_ = listener.Close()

		require.NoError(t, serving.Wait())
		connections.Wait()
	})
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listener.Addr().String()+"/debug/pprof/", http.NoBody)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)
	// The listener fails after a successful request, while the command could run on.
	require.NoError(t, listener.Close())
	require.NoError(t, serving.Wait())
	require.Contains(t, logs.String(), `msg="private pprof HTTP serving failed"`)
	require.Contains(t, logs.String(), "error_type=*net.OpError")
	require.Contains(t, logs.String(), "process_start=test-start")
	require.NotContains(t, logs.String(), listener.Addr().String())
	require.Equal(t, 1, strings.Count(logs.String(), "\n"))
}

func TestDiagnosticsImmediateClose(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	var connections sync.WaitGroup

	server := newDiagnosticsServer(t.Context(), &connections)
	// Close before Serve runs: Serve still owns and closes the bound listener.
	require.NoError(t, server.Close())

	var logs bytes.Buffer

	serveDiagnostics(server, listener, slog.New(slog.NewTextHandler(&logs, nil)))
	connections.Wait()
	require.Empty(t, logs.String())
	rebound, err := net.Listen("tcp", listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, rebound.Close())
}

// Runtime recording is process-wide: these real-network tests must stay serial.
func TestDiagnosticsLiveCapturesAndCancellation(t *testing.T) {
	for _, name := range []string{"profile", "trace"} {
		for _, cancelBy := range []string{"request", "server"} {
			t.Run(name+" cancel by "+cancelBy, func(t *testing.T) {
				// No capture may create application files; all responses stay in memory.
				workspace := t.TempDir()
				t.Chdir(workspace)

				listener, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)

				var connections sync.WaitGroup

				ctxServer, cancelServer := context.WithCancel(t.Context())
				server := newDiagnosticsServer(ctxServer, &connections)

				var serving errgroup.Group

				serving.Go(func() error {
					serveDiagnostics(server, listener, slog.New(slog.DiscardHandler))
					return nil
				})
				t.Cleanup(func() {
					cancelServer()
					require.NoError(t, server.Close())
					_ = listener.Close()

					require.NoError(t, serving.Wait())
					connections.Wait()
				})

				client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
				t.Cleanup(client.CloseIdleConnections)

				base := "http://" + listener.Addr().String() + "/debug/pprof/"

				seconds := "1"
				if name == "trace" {
					seconds = "0.05"
				}

				request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+name+"?seconds="+seconds, http.NoBody)
				require.NoError(t, err)
				response, err := client.Do(request)
				require.NoError(t, err)
				data, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.Equal(t, "application/octet-stream", response.Header.Get("Content-Type"))

				if name == "profile" {
					reader, err := gzip.NewReader(bytes.NewReader(data))
					require.NoError(t, err)
					profile, err := io.ReadAll(reader)
					require.NoError(t, err)
					require.NoError(t, reader.Close())
					require.NotEmpty(t, profile)
				} else {
					require.True(t, bytes.HasPrefix(data, []byte("go 1.")))
				}

				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()

				var body io.Reader = http.NoBody
				if cancelBy == "server" {
					// An unread GET body prevents net/http's background disconnect read.
					body = strings.NewReader("unread request body")
				}

				request, err = http.NewRequestWithContext(ctx, http.MethodGet, base+name+"?seconds=60", body)
				require.NoError(t, err)

				var requesting errgroup.Group

				requesting.Go(func() error {
					response, errRequest := client.Do(request)
					if errRequest != nil {
						return fmt.Errorf("request live %s capture: %w", name, errRequest)
					}

					_, errRead := io.Copy(io.Discard, response.Body)

					return errors.Join(errRead, response.Body.Close())
				})
				t.Cleanup(func() {
					cancel()
					cancelServer()

					_ = requesting.Wait()
				})
				// Inspect the native handler's sleep through the goroutine endpoint.
				// That frame follows StartCPUProfile/trace.Start, unlike connection acceptance.
				require.EventuallyWithT(t, func(check *assert.CollectT) {
					request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"goroutine?debug=2", http.NoBody)
					if !assert.NoError(check, err) {
						return
					}

					response, err := client.Do(request)
					if !assert.NoError(check, err) {
						return
					}

					data, err := io.ReadAll(response.Body)
					assert.NoError(check, err)
					assert.NoError(check, response.Body.Close())
					assert.Contains(check, string(data), "net/http/pprof.sleep(")
				}, 5*time.Second, 5*time.Millisecond)
				requestConflict, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+name+"?seconds="+seconds, http.NoBody)
				require.NoError(t, err)
				response, err = client.Do(requestConflict)
				require.NoError(t, err)
				_, err = io.Copy(io.Discard, response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Equal(t, http.StatusInternalServerError, response.StatusCode)

				cancelStarted := time.Now()
				if cancelBy == "request" {
					cancel()
				} else {
					cancelServer()
					require.NoError(t, server.Close())
					require.NoError(t, serving.Wait())
				}

				errRequest := requesting.Wait()
				// Server cancellation can finish the response before Close reaches the socket.
				if cancelBy == "request" {
					require.ErrorIs(t, errRequest, context.Canceled)
				}

				connections.Wait()
				require.Less(t, time.Since(cancelStarted), 5*time.Second, "cancellation must end the 60-second capture early")
				// Joining must finish native Stop, not leave a recording alive after close.
				if name == "profile" {
					require.NoError(t, pprof.StartCPUProfile(io.Discard))
					pprof.StopCPUProfile()
				} else {
					require.NoError(t, trace.Start(io.Discard))
					trace.Stop()
				}

				files, err := os.ReadDir(workspace)
				require.NoError(t, err)
				require.Empty(t, files)
			})
		}
	}
}

// This CPU-bound comparison measures live recording cost, not agent throughput.
func BenchmarkDiagnosticsCaptureOverhead(b *testing.B) {
	for _, mode := range []string{"off", "cpu", "trace"} {
		b.Run(mode, func(b *testing.B) {
			payload := make([]byte, 4096)

			switch mode {
			case "cpu":
				require.NoError(b, pprof.StartCPUProfile(io.Discard))
				b.Cleanup(pprof.StopCPUProfile)
			case "trace":
				require.NoError(b, trace.Start(io.Discard))
				b.Cleanup(trace.Stop)
			}

			b.ReportAllocs()

			for b.Loop() {
				sha256.Sum256(payload)
			}
		})
	}
}
