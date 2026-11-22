package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"tailscale.com/client/local"
	"tailscale.com/envknob"
	"tailscale.com/ipn"
	"tailscale.com/tsnet"
)

func startTailnet(ctx context.Context, tailnet *config.TsnetConfig, log *slog.Logger) (*tailnetNode, error) {
	node, err := newTailnetNode(tailnet)
	if err != nil {
		return nil, err
	}

	if node.https, err = listenTailnet(ctx, node, tailnet.AuthKey, log); err != nil {
		return nil, errors.Join(err, node.server.Close())
	}

	tailnet.Origin = "https://" + node.server.CertDomains()[0]

	return node, nil
}

var errTailnetLogin = errors.New("the Tailscale node needs a login: run `rocketclaw ts login`, or set web.tsnet.auth_key to a valid key")

type tailnetBackendError string

func (e tailnetBackendError) Error() string { return "tailscale node: " + string(e) }

type tailnetNode struct {
	server *tsnet.Server
	client *local.Client
	https  net.Listener
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

func listenTailnet(ctx context.Context, node *tailnetNode, authKey string, log *slog.Logger) (net.Listener, error) {
	status, err := node.client.StatusWithoutPeers(ctx)
	if err != nil {
		return nil, fmt.Errorf("read Tailscale node status: %w", err)
	}

	if status.BackendState == ipn.NeedsLogin.String() && authKey == "" {
		return nil, errTailnetLogin
	}

	watcher, err := node.client.WatchIPNBus(ctx, ipn.NotifyInitialState)
	if err != nil {
		return nil, fmt.Errorf("watch Tailscale node: %w", err)
	}

	defer func() { _ = watcher.Close() }()

	for {
		step, _, err := tailnetWait(watcher)
		if err != nil {
			return nil, err
		}

		if step == tailnetRunning {
			break
		}

		if step == tailnetLogin {
			return nil, errTailnetLogin
		}

		log.Warn("the Tailscale node waits for device approval in the admin console")
	}

	if _, err := node.server.Up(ctx); err != nil {
		return nil, fmt.Errorf("bring up Tailscale node: %w", err)
	}

	if len(node.server.CertDomains()) == 0 {
		return nil, errors.New("the tailnet gives the Tailscale node no HTTPS name: enable MagicDNS and HTTPS certificates in the Tailscale admin console")
	}

	listener, err := node.server.Listen("tcp", ":443")
	if err != nil {
		return nil, fmt.Errorf("listen for Web HTTPS on the tailnet: %w", err)
	}

	return listener, nil
}
