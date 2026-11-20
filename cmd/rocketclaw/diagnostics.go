package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"strconv"
	"sync"
)

const defaultPprofAddress = "127.0.0.1:6060"

var errPprofValue = errors.New("want true, false, or host:port such as 127.0.0.1:6061")

type pprofAddress string

func (a *pprofAddress) String() string { return string(*a) }

func (a *pprofAddress) IsBoolFlag() bool { return true }

func (a *pprofAddress) Set(value string) error {
	if enabled, err := strconv.ParseBool(value); err == nil {
		*a = ""
		if enabled {
			*a = defaultPprofAddress
		}

		return nil
	}

	_, port, errSplit := net.SplitHostPort(value)
	if _, errPort := strconv.ParseUint(port, 10, 16); errSplit != nil || errPort != nil {
		return errPprofValue
	}

	*a = pprofAddress(value)

	return nil
}

func newDiagnosticsServer(ctx context.Context, connections *sync.WaitGroup) *http.Server {
	runtime.SetBlockProfileRate(10_000_000)
	runtime.SetMutexProfileFraction(100)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)

	return &http.Server{
		BaseContext: func(net.Listener) context.Context { return ctx },
		Handler:     mux,
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				connections.Add(1)
			}

			if state == http.StateClosed {
				connections.Done()
			}
		},
	}
}

func serveDiagnostics(server *http.Server, listener net.Listener, logger *slog.Logger) {
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("private pprof HTTP serving failed", "error_type", fmt.Sprintf("%T", err))
	}
}
