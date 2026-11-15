package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/rpc"
	"golang.org/x/sync/errgroup"
	"tailscale.com/client/local"
	"tailscale.com/envknob"
	"tailscale.com/ipn"
	"tailscale.com/tsnet"
)

func startTailnet(rt *backend.Runtime, tailnet *config.TsnetConfig, handler http.Handler, serving *errgroup.Group) (func() error, error) {
	node, err := newTailnetNode(tailnet)
	if err != nil {
		return nil, err
	}

	status, err := node.client.StatusWithoutPeers(context.Background())
	if err != nil {
		_ = node.server.Close()
		return nil, fmt.Errorf("read Tailscale node status: %w", err)
	}

	if status.BackendState == ipn.NeedsLogin.String() && tailnet.AuthKey == "" {
		rt.Log.Warn(tailnetLoginNotice)
		_ = node.server.Close()

		return func() error { return nil }, nil
	}

	ctxUp, cancelUp := context.WithCancel(context.Background())
	tailnetServer := &http.Server{Handler: handler, TLSConfig: &tls.Config{GetCertificate: node.client.GetCertificate}}

	serving.Go(func() error {
		err := serveTailnet(ctxUp, rt, node, tailnetServer)

		switch {
		case ctxUp.Err() != nil:
		case errors.Is(err, errTailnetLogin):
			rt.Log.Warn(tailnetLoginNotice)
		default:
			rt.Log.Warn("Web HTTPS on the tailnet stopped", "error", err)
		}

		return nil
	})

	return func() error {
		cancelUp()
		return errors.Join(tailnetServer.Close(), node.server.Close())
	}, nil
}

const tailnetLoginNotice = "Tailscale authentication unavailable, so Web HTTPS on the tailnet is off: run `rocketclaw ts login`, then restart RocketClaw"

var errTailnetLogin = errors.New("tailscale node needs a login")

type tailnetBackendError string

func (e tailnetBackendError) Error() string { return "tailscale node: " + string(e) }

type tailnetNode struct {
	server *tsnet.Server
	client *local.Client
}

func newTailnetNode(tailnet *config.TsnetConfig) (*tailnetNode, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("find the Tailscale state directory: %w", err)
	}

	envknob.SetNoLogsNoSupport()

	server := &tsnet.Server{
		Dir:           filepath.Join(dir, "rocketclaw", "tsnet-"+tailnet.Hostname),
		Hostname:      tailnet.Hostname,
		AuthKey:       tailnet.AuthKey,
		AdvertiseTags: tailnet.Tags,
		UserLogf:      func(string, ...any) {},
	}

	client, err := server.LocalClient()
	if err != nil {
		return nil, fmt.Errorf("start Tailscale node: %w", err)
	}

	return &tailnetNode{server: server, client: client}, nil
}

type tailnetStep int

const (
	tailnetRunning tailnetStep = iota
	tailnetLogin
	tailnetApproval
)

func tailnetWait(watcher *local.IPNBusWatcher) (tailnetStep, string, error) {
	for {
		notify, err := watcher.Next()
		if err != nil {
			return 0, "", fmt.Errorf("watch Tailscale node: %w", err)
		}

		switch {
		case notify.ErrMessage != nil:
			return 0, "", tailnetBackendError(*notify.ErrMessage)
		case notify.BrowseToURL != nil && *notify.BrowseToURL != "":
			return tailnetLogin, *notify.BrowseToURL, nil
		case notify.State != nil && *notify.State == ipn.Running:
			return tailnetRunning, "", nil
		case notify.State != nil && *notify.State == ipn.NeedsMachineAuth:
			return tailnetApproval, "", nil
		}
	}
}

func serveTailnet(ctx context.Context, rt *backend.Runtime, node *tailnetNode, server *http.Server) error {
	watcher, err := node.client.WatchIPNBus(ctx, ipn.NotifyInitialState)
	if err != nil {
		return fmt.Errorf("watch Tailscale node: %w", err)
	}

	defer func() { _ = watcher.Close() }()

	for {
		step, _, err := tailnetWait(watcher)
		if err != nil {
			return err
		}

		if step == tailnetRunning {
			break
		}

		if step == tailnetLogin {
			return errTailnetLogin
		}

		rt.Log.Warn("the Tailscale node waits for device approval in the admin console")
	}

	if _, err := node.server.Up(ctx); err != nil {
		return fmt.Errorf("bring up Tailscale node: %w", err)
	}

	domains := node.server.CertDomains()
	if len(domains) == 0 {
		return errors.New("tailscale node has no HTTPS name: enable MagicDNS and HTTPS in the admin console")
	}

	server.Handler = rpc.NewTailnetHandler(node.client, rt.Cfg.Clone().TrustedRocketClaws, server.Handler)

	listener, err := node.server.Listen("tcp", ":443")
	if err != nil {
		return fmt.Errorf("listen for Web HTTPS on the tailnet: %w", err)
	}

	rt.Log.Info("started Web HTTPS on the tailnet", "domains", domains)

	_ = rt.Cfg.Do(func(cfg *config.Config) error {
		cfg.Web.Tsnet.Origin = "https://" + domains[0]
		return nil
	})

	if err := server.ServeTLS(listener, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve Web HTTPS on the tailnet: %w", err)
	}

	return nil
}
