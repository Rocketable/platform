package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/rpc"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"tailscale.com/ipn/ipnstate"
)

// serverName names this server in browser tabs: server_display_name, else its Tailscale
// node name (the tsnet hostname, or this machine's node), else nothing.
func serverName(cfg *config.Config) string {
	if cfg.ServerDisplayName != "" {
		return cfg.ServerDisplayName
	}

	if cfg.Web.Tsnet != nil {
		return cfg.Web.Tsnet.Hostname
	}

	output, err := exec.Command("tailscale", "status", "--json", "--peers=false").Output()

	var status ipnstate.Status
	if err != nil || json.Unmarshal(output, &status) != nil || status.Self == nil {
		return ""
	}

	name, _, _ := strings.Cut(status.Self.DNSName, ".")

	return name
}

func startWebRPC(rt *backend.Runtime, channels rpc.ChannelAgentChoices, cronjobs rpc.CronJobs) (func(context.Context) error, error) {
	cfg := rt.Cfg.Clone()
	// Only HTTP is network-accessible; principal metadata stays inside the process.
	listener := bufconn.Listen(1 << 20)
	certificate, err := webCertificate(cfg.Web)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}

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
	httpServer := &http.Server{Handler: rpc.NewHTTPHandler(connection, cfg.Web.Sentry, cfg.OpenCode, serverName(cfg)), TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}}
	server := grpc.NewServer()
	web := rpc.New(rt, rt.Sessions, rt.Cfg, channels, cronjobs)
	web.Register(server)

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
		// HTTPS lets browsers carry every tab over one HTTP/2 connection instead of six HTTP/1.1 ones.
		if err := httpServer.ServeTLS(httpListener, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve Web HTTP: %w", err)
		}

		return nil
	})
	rt.Log.Info("started Web HTTPS", "address", httpListener.Addr().String(), "self_signed", cfg.Web.TLSCertFile == "")

	ctxSync, cancelSync := context.WithCancel(context.Background())
	if len(cfg.OpenCode) > 0 {
		serving.Go(func() error {
			rt.Sessions.SyncOpenCode(ctxSync, cfg.OpenCode)
			return nil
		})
	}

	return func(context.Context) error {
		errs := []error{httpServer.Close()}
		_ = httpListener.Close()

		cancelSync()
		errs = append(errs, closeTailnet())

		server.Stop()

		if err := errors.Join(append(errs, serving.Wait(), connection.Close())...); err != nil {
			return fmt.Errorf("stop Web: %w", err)
		}

		return nil
	}, nil
}

// webCertificate loads web's configured certificate, or makes a self-signed one for this start.
func webCertificate(web config.WebConfig) (tls.Certificate, error) {
	if web.TLSCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(web.TLSCertFile, web.TLSKeyFile)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("load Web certificate: %w", err)
		}

		return certificate, nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("make Web certificate key: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: "RocketClaw"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("make Web certificate: %w", err)
	}

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
