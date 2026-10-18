package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"

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
	web := rpc.New(rt, rt.Sessions, rt.Cfg, channels, cronjobs)
	web.Register(server)

	var serving errgroup.Group

	closeTailnet := func() error { return nil }

	relays := make(map[int]http.Handler, len(cfg.OpenCode))
	for _, server := range cfg.OpenCode {
		relays[server.Port] = rpc.NewOpenCodeRelay(server)
	}

	if tailnet := cfg.Web.Tsnet; tailnet != nil {
		if closeTailnet, err = startTailnet(rt, tailnet, httpServer.Handler, relays, &serving); err != nil {
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

	ctxSync, cancelSync := context.WithCancel(context.Background())
	if len(cfg.OpenCode) > 0 {
		serving.Go(func() error {
			rt.Sessions.SyncOpenCode(ctxSync, cfg.OpenCode)
			return nil
		})
	}

	var relayServers []*http.Server

	stop := func(context.Context) error {
		errs := []error{httpServer.Close()}
		_ = httpListener.Close()

		for _, relay := range relayServers {
			errs = append(errs, relay.Close())
		}

		cancelSync()
		errs = append(errs, closeTailnet())

		server.Stop()

		if err := errors.Join(append(errs, serving.Wait(), connection.Close())...); err != nil {
			return fmt.Errorf("stop Web: %w", err)
		}

		return nil
	}

	host, _, _ := net.SplitHostPort(cfg.Web.ListenAddress) // Listening on it above proved it parses.
	for port, relay := range relays {
		listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			return nil, errors.Join(fmt.Errorf("start OpenCode relay: %w", err), stop(context.Background()))
		}

		relayServer := &http.Server{Handler: web.AdmitHTTP(relay)}
		relayServers = append(relayServers, relayServer)

		serving.Go(func() error {
			if err := relayServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("serve OpenCode relay: %w", err)
			}

			return nil
		})
	}

	return stop, nil
}
