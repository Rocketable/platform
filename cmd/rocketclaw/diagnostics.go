package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"sync"
)

func newDiagnosticsServer(ctx context.Context, connections *sync.WaitGroup) *http.Server {
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
				// Context cancellation stops recording; Close does not join handlers.
				connections.Done()
			}
		},
	}
}

// serveDiagnostics reports failures immediately without interrupting backend work.
func serveDiagnostics(server *http.Server, listener net.Listener, logger *slog.Logger) {
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("private pprof HTTP serving failed", "error_type", fmt.Sprintf("%T", err))
	}
}
