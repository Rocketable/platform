// Package funneld implements a small HTTPS reverse-proxy funnel daemon.
package funneld

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

const (
	defaultAddr      = ":https"
	defaultHTTPAddr  = ":http"
	defaultConfig    = "./funneld.json"
	defaultCertCache = "./funneld-certs"
)

type config struct {
	Addr      string        `json:"addr"`
	HTTPAddr  string        `json:"http_addr"`
	Host      string        `json:"host"`
	CertCache string        `json:"cert_cache"`
	Routes    []routeConfig `json:"routes"`
}

type routeConfig struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

// Run executes funneld with argv0 and args.
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

	handler, err := newHandler(cfg, slog.Default())
	if err != nil {
		return err
	}

	certManager := &autocert.Manager{
		Cache:      autocert.DirCache(cfg.CertCache),
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(cfg.Host),
	}

	server := &http.Server{Addr: cfg.Addr, Handler: handler, TLSConfig: certManager.TLSConfig(), ReadHeaderTimeout: 15 * time.Second}
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr, err)
	}

	serveErr := make(chan error, 2)
	go func() { serveErr <- server.ServeTLS(listener, "", "") }()

	var httpServer *http.Server
	if cfg.HTTPAddr != "" {
		httpServer = &http.Server{Addr: cfg.HTTPAddr, Handler: certManager.HTTPHandler(nil), ReadHeaderTimeout: 15 * time.Second}
		httpListener, err := new(net.ListenConfig).Listen(ctx, "tcp", cfg.HTTPAddr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", cfg.HTTPAddr, err)
		}

		go func() { serveErr <- httpServer.Serve(httpListener) }()
	}
	logConfiguredRoutes(ctx, slog.Default(), cfg.Routes)
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "funneld started", configAttrs(cfg)...)

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}

		return nil
	case <-runCtx.Done():
		slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "funneld shutting down", configAttrs(cfg)...)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown tls server: %w", err)
		}

		if httpServer != nil {
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				return fmt.Errorf("shutdown http server: %w", err)
			}
		}

		slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "funneld shut down", configAttrs(cfg)...)
		return nil
	}
}

func configAttrs(cfg config) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("addr", cfg.Addr),
		slog.String("http_addr", cfg.HTTPAddr),
		slog.String("host", cfg.Host),
		slog.String("cert_cache", cfg.CertCache),
		slog.Int("route_count", len(cfg.Routes)),
	}

	for i, route := range cfg.Routes {
		attrs = append(attrs, slog.GroupAttrs(fmt.Sprintf("route_%d", i),
			slog.String("path", route.Path),
			slog.String("target", route.Target),
		))
	}

	return attrs
}

func logConfiguredRoutes(ctx context.Context, logger *slog.Logger, routes []routeConfig) {
	for i, route := range routes {
		logger.LogAttrs(ctx, slog.LevelInfo, "funneld route configured",
			slog.Int("route", i),
			slog.String("path", route.Path),
			slog.String("target", route.Target),
		)
	}
}

func loadConfigFromArgs(args []string, getenv func(string) string) (config, error) {
	configPath := getenv("FUNNELD_CONFIG")
	if configPath == "" {
		configPath = defaultConfig
	}

	addr := ""
	httpAddr := ""
	host := ""
	fs := flag.NewFlagSet("funneld", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&configPath, "config", configPath, "JSON config file path")
	fs.StringVar(&addr, "addr", "", "TLS bind address override")
	fs.StringVar(&httpAddr, "http-addr", "", "HTTP ACME challenge bind address override; set to none to disable")
	fs.StringVar(&host, "host", "", "certificate host override")

	if err := fs.Parse(args); err != nil {
		return config{}, err
	}

	if len(fs.Args()) != 0 {
		return config{}, fmt.Errorf("funneld takes flags only, got extra arguments: %v", fs.Args())
	}

	cfg, err := loadConfigFile(configPath)
	if err != nil {
		return config{}, err
	}

	if addr != "" {
		cfg.Addr = addr
	}

	if httpAddr != "" {
		if httpAddr == "none" {
			cfg.HTTPAddr = ""
		} else {
			cfg.HTTPAddr = httpAddr
		}
	}

	if host != "" {
		cfg.Host = host
	}

	if err := cfg.validate(); err != nil {
		return config{}, err
	}

	return cfg, nil
}

func loadConfigFile(path string) (config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := config{Addr: defaultAddr, HTTPAddr: defaultHTTPAddr, CertCache: defaultCertCache}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return config{}, fmt.Errorf("parse config %s: %w", path, err)
	}

	return cfg, nil
}

func (cfg *config) validate() error {
	host, err := normalizeHost(cfg.Host)
	if err != nil {
		return err
	}
	cfg.Host = host

	if cfg.Addr == "" {
		return errors.New("addr is required")
	}

	if cfg.CertCache == "" {
		return errors.New("cert_cache is required")
	}

	if len(cfg.Routes) == 0 {
		return errors.New("at least one route is required")
	}

	seen := make(map[string]bool, len(cfg.Routes))
	for _, route := range cfg.Routes {
		if route.Path == "" || route.Path[0] != '/' {
			return fmt.Errorf("route path %q must start with /", route.Path)
		}

		if seen[route.Path] {
			return fmt.Errorf("duplicate route path %q", route.Path)
		}
		seen[route.Path] = true

		target, err := url.Parse(route.Target)
		if err != nil {
			return fmt.Errorf("parse target for %s: %w", route.Path, err)
		}

		if target.Scheme != "http" && target.Scheme != "https" {
			return fmt.Errorf("route %s target must use http or https", route.Path)
		}

		if target.Host == "" || target.Path == "" {
			return fmt.Errorf("route %s target must include host and path", route.Path)
		}
	}

	return nil
}

func newHandler(cfg config, logger *slog.Logger) (http.Handler, error) {
	mux := http.NewServeMux()
	for _, route := range cfg.Routes {
		target, err := url.Parse(route.Target)
		if err != nil {
			return nil, fmt.Errorf("parse target for %s: %w", route.Path, err)
		}

		targetURL := *target
		mountPath := route.Path
		proxy := &httputil.ReverseProxy{
			Rewrite: func(req *httputil.ProxyRequest) {
				suffix := req.In.URL.Path
				if mountPath != "/" {
					suffix = strings.TrimPrefix(req.In.URL.Path, mountPath)
				}

				req.Out.URL.Scheme = targetURL.Scheme
				req.Out.URL.Host = targetURL.Host
				req.Out.URL.Path = strings.TrimRight(targetURL.Path, "/") + suffix
				req.Out.URL.RawPath = ""
				req.Out.URL.RawQuery = req.In.URL.RawQuery
				req.Out.Host = targetURL.Host
				req.SetXForwarded()
			},
		}

		mux.Handle(mountPath, proxy)
		if mountPath != "/" {
			mux.Handle(mountPath+"/", proxy)
		}
	}

	return logRequests(logger, mux), nil
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *loggingResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *loggingResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}

	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

func (w *loggingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &loggingResponseWriter{ResponseWriter: w}
		next.ServeHTTP(lw, r)

		status := lw.status
		if status == 0 {
			status = http.StatusOK
		}

		logger.LogAttrs(r.Context(), slog.LevelInfo, "http request",
			slog.String("method", r.Method),
			slog.String("host", r.Host),
			slog.String("path", r.URL.Path),
			slog.String("query", r.URL.RawQuery),
			slog.String("remote_addr", r.RemoteAddr),
			slog.String("user_agent", r.UserAgent()),
			slog.Int("status", status),
			slog.Int("bytes", lw.bytes),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

func normalizeHost(host string) (string, error) {
	if host == "" {
		return "", errors.New("host is required")
	}

	if strings.Contains(host, "://") {
		u, err := url.Parse(host)
		if err != nil {
			return "", fmt.Errorf("parse host %q: %w", host, err)
		}

		host = u.Hostname()
	}

	if strings.Contains(host, "/") {
		return "", fmt.Errorf("host %q must not include a path", host)
	}

	if host == "" {
		return "", errors.New("host is required")
	}

	return host, nil
}

func invocationCommand(argv0 string) string {
	if argv0 == "" {
		return "funneld"
	}

	return filepath.Clean(argv0)
}

func helpText(cmd string) string {
	return fmt.Sprintf(`funneld serves a small HTTPS reverse proxy from JSON route mappings.

Usage:

  %[1]s [--config ./funneld.json] [--addr :https] [--http-addr :http] [--host example.com]

Flags:

  --config     JSON config file path. Default: ./funneld.json or FUNNELD_CONFIG.
  --addr       TLS bind address override.
  --http-addr  HTTP ACME challenge address override. Use "none" to disable.
  --host       Certificate host override.

Example config:

  {
    "host": "example.ts.net",
    "routes": [
      {"path": "/service/webhook", "target": "http://localhost:7070/service/webhook"},
      {"path": "/other/webhook", "target": "http://localhost:7071/other/webhook"}
    ]
  }
`, cmd)
}
