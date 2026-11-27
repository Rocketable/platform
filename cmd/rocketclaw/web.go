package main

import (
	"cmp"
	"context"
	"crypto/tls"
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

func startWebRPC(rt *backend.Runtime, tailnet *tailnetNode, channels rpc.ChannelAgentChoices, routines rpc.Routines) func(context.Context) error {
	cfg := rt.Cfg.Clone()

	listener := bufconn.Listen(1 << 20)
	connection, _ := grpc.NewClient("passthrough:///web", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}))
	handler := http.NewServeMux()
	handler.Handle("/", rpc.NewHTTPHandler(connection, rt.Cfg, cmp.Or(cfg.ServerDisplayName, cfg.Web.Tsnet.Hostname)))
	handler.Handle("/opencode/{name}/mcp", http.NewCrossOriginProtection().Handler(rt.OpenCodeMCP()))
	server := grpc.NewServer()
	web := rpc.New(rt, rt.Sessions, rt.Cfg, channels, routines)
	web.Register(server)

	var serving errgroup.Group

	https := &http.Server{
		Handler:   rpc.NewTailnetHandler(tailnet.client, rt.Cfg.Clone().TrustedRocketClaws, handler),
		TLSConfig: &tls.Config{GetCertificate: tailnet.client.GetCertificate},
	}

	rt.Log.Info("started Web HTTPS on the tailnet", "domains", tailnet.server.CertDomains())

	serving.Go(func() error {
		if err := https.ServeTLS(tailnet.https, "", ""); !errors.Is(err, http.ErrServerClosed) {
			rt.Restart(fmt.Sprintf("Web HTTPS on the tailnet stopped: %v", err))
		}

		return nil
	})

	serving.Go(func() error {
		if err := server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("serve web RPC: %w", err)
		}

		return nil
	})

	ctxSync, cancelSync := context.WithCancel(context.Background())

	serving.Go(func() error {
		rt.Sessions.WakeParks(ctxSync)
		return nil
	})

	if len(cfg.OpenCode) > 0 {
		serving.Go(func() error {
			rt.Sessions.SyncOpenCode(ctxSync, rt.Cfg)
			return nil
		})
	}

	return func(context.Context) error {
		cancelSync()

		errTailnet := https.Close()

		server.Stop()
		web.StopSearches()

		if err := errors.Join(errTailnet, serving.Wait(), connection.Close()); err != nil {
			return fmt.Errorf("stop Web: %w", err)
		}

		return nil
	}
}
