package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/rpc"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
)

func startWebRPC(rt *backend.Runtime, channels rpc.ChannelAgentChoices, cronjobs rpc.CronJobs) (func(context.Context) error, error) {
	address := os.Getenv("ROCKETCLAW_WEB_GRPC")

	socketPath, ok := strings.CutPrefix(address, "unix:")
	if !ok {
		return nil, errors.New("ROCKETCLAW_WEB_GRPC must be unix:/absolute/private-directory/web.sock")
	}

	listener, err := rpc.Listen(socketPath)
	if err != nil {
		return nil, fmt.Errorf("start web RPC: %w", err)
	}

	server := grpc.NewServer()
	rpc.New(rt, rt.Sessions, rt.Cfg, channels, cronjobs).Register(server)

	var serving errgroup.Group
	serving.Go(func() error {
		if err := server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("serve web RPC: %w", err)
		}

		return nil
	})
	rt.Log.Info("started Web entry RPC", "address", address)

	return func(context.Context) error {
		server.Stop()

		if err := serving.Wait(); err != nil {
			return fmt.Errorf("stop web RPC: %w", err)
		}

		return nil
	}, nil
}
