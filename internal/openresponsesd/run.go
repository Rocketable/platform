// Package openresponsesd implements the OpenResponses protocol daemon.
package openresponsesd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const defaultAddr = "127.0.0.1:8798"

// Run executes openresponsesd with argv0 and args.
func Run(ctx context.Context, argv0 string, args []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	cfg, err := loadConfigFromArgs(args, os.Getenv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(helpText(invocationCommand(argv0)))
			return nil
		}

		return err
	}

	server := newServer(cfg, http.DefaultClient)
	httpServer := &http.Server{Addr: cfg.Addr, Handler: http.HandlerFunc(server.serveHTTP), ReadHeaderTimeout: 15 * time.Second}
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr, err)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}

		return nil
	case <-runCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}

		return nil
	}
}

func loadConfigFromArgs(args []string, getenv func(string) string) (config, error) {
	configPath := getenv("OPENRESPONSESD_CONFIG")
	if configPath == "" {
		configPath = "./openresponsesd.json"
	}

	authToken := getenv("OPENRESPONSESD_AUTH_TOKEN")
	addr := ""
	provider := ""
	fs := flag.NewFlagSet("openresponsesd", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&configPath, "config", configPath, "JSON config file path")
	fs.StringVar(&addr, "addr", "", "bind address override")
	fs.StringVar(&authToken, "auth-token", authToken, "single bearer auth token override")
	fs.StringVar(&provider, "provider", "", "default provider override")

	if err := fs.Parse(args); err != nil {
		return config{}, err
	}

	if len(fs.Args()) != 0 {
		return config{}, fmt.Errorf("openresponsesd takes flags only, got extra arguments: %v", fs.Args())
	}

	cfg, err := loadConfigFile(configPath)
	if err != nil {
		return config{}, err
	}

	if addr != "" {
		cfg.Addr = addr
	}

	if authToken != "" {
		cfg.Auth.Tokens = []string{authToken}
	}

	if provider != "" {
		cfg.DefaultProvider = provider
	}

	if err := cfg.validate(getenv); err != nil {
		return config{}, err
	}

	return cfg, nil
}

func invocationCommand(argv0 string) string {
	if argv0 == "" {
		return "openresponsesd"
	}

	return filepath.Clean(argv0)
}

func helpText(cmd string) string {
	return fmt.Sprintf(`openresponsesd exposes a Responses/OpenResponses-shaped API and routes to configured upstream providers.

Usage:

  %[1]s [--config ./openresponsesd.json] [--addr 127.0.0.1:8798] [--auth-token token] [--provider name]

Flags:

  --config      JSON config file path. Default: ./openresponsesd.json or OPENRESPONSESD_CONFIG.
  --addr        Bind address override.
  --auth-token  Single bearer auth token override for local development.
  --provider    Default upstream provider override.
`, cmd)
}
