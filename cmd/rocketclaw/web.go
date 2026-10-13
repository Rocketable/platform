package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/rpc"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func startWebRPC(rt *backend.Runtime, channels rpc.ChannelAgentChoices, cronjobs rpc.CronJobs) (func(context.Context) error, error) {
	cfg := rt.Cfg.Clone()
	// Only HTTP is network-accessible; principal metadata stays inside the process.
	listener := bufconn.Listen(1 << 20)
	httpListener, err := net.Listen("tcp", cfg.Web.ListenAddress)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("start Web HTTP: %w", err)
	}

	// This fixed passthrough target and insecure credentials cannot fail setup; dialing happens on RPC calls.
	connection, _ := grpc.NewClient("passthrough:///web", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}))
	httpServer := &http.Server{Handler: rpc.NewHTTPHandler(connection, cfg.Web.Sentry)}
	server := grpc.NewServer()
	rpc.New(rt, rt.Sessions, rt.Cfg, channels, cronjobs).Register(server)

	var serving errgroup.Group

	closeTailnet := func() error { return nil }

	if tailnet := cfg.Web.Tsnet; tailnet != nil {
		if closeTailnet, err = startTailnet(rt, tailnet, httpServer.Handler, &serving); err != nil {
			_ = httpListener.Close()
			_ = listener.Close()
			_ = connection.Close()

			return nil, fmt.Errorf("start Web Tailscale node: %w", err)
		}
	}

	serving.Go(func() error {
		if err := server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("serve web RPC: %w", err)
		}

		return nil
	})
	serving.Go(func() error {
		if err := httpServer.Serve(httpListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve Web HTTP: %w", err)
		}

		return nil
	})
	rt.Log.Info("started Web HTTP", "address", httpListener.Addr().String())

	return func(context.Context) error {
		errHTTP := httpServer.Close()
		_ = httpListener.Close()
		errTailnet := closeTailnet()

		server.Stop()

		if err := errors.Join(errHTTP, errTailnet, serving.Wait(), connection.Close()); err != nil {
			return fmt.Errorf("stop Web: %w", err)
		}

		return nil
	}, nil
}
