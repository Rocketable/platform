package main

import (
	"context"
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

// startTailnet starts the Web's tsnet node and serves handler over HTTPS on the tailnet in
// serving, and returns what closes it. It never waits on a person: a logged-out node without
// an auth key is closed at once, and the Web serves only its plain listener.
func startTailnet(rt *backend.Runtime, tailnet *config.TsnetConfig, handler http.Handler, serving *errgroup.Group) (func() error, error) {
	node, err := newTailnetNode(tailnet)
	if err != nil {
		return nil, err
	}

	// The node's login state is known once it started.
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
	tailnetServer := &http.Server{Handler: rpc.NewTailnetHandler(node.client, handler)}

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

// tailnetLoginNotice is what the server logs when its tsnet node is logged out.
const tailnetLoginNotice = "Tailscale authentication unavailable, so Web HTTPS on the tailnet is off: run `rocketclaw ts login`, then restart RocketClaw"

var errTailnetLogin = errors.New("tailscale node needs a login")

// tailnetBackendError is an error the node's backend reported.
type tailnetBackendError string

func (e tailnetBackendError) Error() string { return "tailscale node: " + string(e) }

// tailnetNode is the Web's tsnet node and its local API client.
type tailnetNode struct {
	server *tsnet.Server
	client *local.Client
}

// newTailnetNode starts the Web's tsnet node. Its state lives outside the workspace, it
// never prints (so no login link appears in logs), and Tailscale's log upload is off.
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

	client, err := server.LocalClient() // Starts the node.
	if err != nil {
		return nil, fmt.Errorf("start Tailscale node: %w", err)
	}

	return &tailnetNode{server: server, client: client}, nil
}

type tailnetStep int

const (
	tailnetRunning  tailnetStep = iota
	tailnetLogin                // needs a person to open a login link
	tailnetApproval             // needs the device approved in the admin console
)

// tailnetWait reports the node's next step: running, a login link to open, or device approval.
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

// serveTailnet serves the Web over HTTPS once the node runs. A node that loses its
// login stops with errTailnetLogin instead of waiting on a link nobody sees.
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

	// ListenTLS needs a running node; Up finishes tsnet's setup and returns at once now.
	if _, err := node.server.Up(ctx); err != nil {
		return fmt.Errorf("bring up Tailscale node: %w", err)
	}

	listener, err := node.server.ListenTLS("tcp", ":443")
	if err != nil {
		return fmt.Errorf("listen for Web HTTPS on the tailnet: %w", err)
	}

	domains := node.server.CertDomains()
	rt.Log.Info("started Web HTTPS on the tailnet", "domains", domains)

	_ = rt.Cfg.Do(func(cfg *config.Config) error {
		cfg.Web.Tsnet.Origin = "https://" + domains[0]
		return nil
	})

	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve Web HTTPS on the tailnet: %w", err)
	}

	return nil
}
