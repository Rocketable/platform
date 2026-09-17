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
	// Only HTTP is network-accessible; principal metadata stays inside the process.
	listener := bufconn.Listen(1 << 20)
	httpListener, err := net.Listen("tcp", rt.Cfg.Web.ListenAddress)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("start Web HTTP: %w", err)
	}

	connection, err := grpc.NewClient("passthrough:///web", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}))
	if err != nil {
		_ = httpListener.Close()
		_ = listener.Close()

		return nil, fmt.Errorf("connect Web HTTP to RPC: %w", err)
	}

	httpServer := &http.Server{Handler: rpc.NewHTTPHandler(connection)}
	server := grpc.NewServer()
	rpc.New(rt, rt.Sessions, rt.Cfg, channels, cronjobs).Register(server)

	var serving errgroup.Group
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

		server.Stop()

		if err := errors.Join(errHTTP, serving.Wait(), connection.Close()); err != nil {
			return fmt.Errorf("stop Web: %w", err)
		}

		return nil
	}, nil
}
