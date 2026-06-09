// Package quickweb implements the Quickweb internal applet server.
package quickweb

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
)

const (
	defaultAddr      = "0.0.0.0:8797"
	defaultDBName    = "quickweb.sqlite"
	hardMaxJSONBytes = 10 * 1024 * 1024
)

// Config describes a Quickweb server instance.
type Config struct {
	ContentRoot string
	DBPath      string
	Addr        string
	ServiceName string
	BaseURL     string
}

// Run executes quickweb with argv0 and args.
func Run(ctx context.Context, argv0 string, args []string) error {
	invocation := invocationCommand(argv0)
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	cfg := Config{ContentRoot: cwd, DBPath: filepath.Join(cwd, defaultDBName), Addr: defaultAddr}
	fs := flag.NewFlagSet(invocation, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "bind address")
	fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "SQLite state database path")
	fs.StringVar(&cfg.ServiceName, "service-name", "", "human-readable service name for logs")
	fs.StringVar(&cfg.BaseURL, "base-url", "", "externally preferred base URL to advertise")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(helpText(invocation))
			return nil
		}

		return fmt.Errorf("parse flags: %w\n\n%s", err, helpText(invocation))
	}

	if len(fs.Args()) != 0 {
		return fmt.Errorf("quickweb takes flags only, got extra arguments: %s\n\n%s", strings.Join(fs.Args(), " "), helpText(invocation))
	}

	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.DBPath, err = filepath.Abs(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("resolve database path: %w", err)
	}

	root, err := os.OpenRoot(cfg.ContentRoot)
	if err != nil {
		return fmt.Errorf("open content root: %w", err)
	}
	defer root.Close()

	db, err := openDatabase(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := migrateDatabase(db); err != nil {
		return err
	}

	candidates := candidateURLs(cfg.Addr, cfg.BaseURL)
	server := NewServer(cfg, root, db, candidates, true)
	httpServer := &http.Server{Addr: cfg.Addr, Handler: server.Handler(), ReadHeaderTimeout: 15 * time.Second}
	logger := log.New(os.Stdout, "quickweb: ", log.LstdFlags)
	logStartup(logger, cfg, candidates)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.ListenAndServe()
	}()

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

		logger.Printf("shutting down")
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}

		return nil
	}
}

func logStartup(logger *log.Logger, cfg Config, candidates []string) {
	logger.Printf("version=%s", buildVersion())
	if cfg.ServiceName != "" {
		logger.Printf("service_name=%s", cfg.ServiceName)
	}
	logger.Printf("content_root=%s", cfg.ContentRoot)
	logger.Printf("db_path=%s", cfg.DBPath)
	logger.Printf("addr=%s", cfg.Addr)
	logger.Printf("data_migration=ok")
	logger.Printf("candidate_urls in preference order:")
	for _, url := range candidates {
		logger.Printf("- %s", url)
	}
}

func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			if len(setting.Value) > 12 {
				return setting.Value[:12]
			}

			return setting.Value
		}
	}

	return "devel"
}

func invocationCommand(argv0 string) string {
	if argv0 == "" {
		return "quickweb"
	}

	path := filepath.Clean(argv0)
	if filepath.Base(path) == "quickweb" {
		for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
			if strings.HasPrefix(filepath.Base(dir), "go-build") {
				if info, ok := debug.ReadBuildInfo(); ok && info.Path != "" {
					return "go run " + info.Path
				}

				break
			}

			if parent := filepath.Dir(dir); parent == dir {
				break
			}
		}
	}

	return argv0
}

func helpText(cmd string) string {
	return fmt.Sprintf(`quickweb serves static applets from the current working directory and gives each page one persistent JSON document.

Usage:

  %[1]s [--addr 0.0.0.0:8797] [--db ./quickweb.sqlite] [--service-name name] [--base-url https://host]

Flags:

  --addr          Bind address. Default: 0.0.0.0:8797
  --db            SQLite state database path. Default: ./quickweb.sqlite
  --service-name  Human-readable process/service name used in logs and health output.
  --base-url      Externally preferred base URL to advertise first.

Content root:

  Quickweb always serves files from the current working directory. Start it from the applet content root.

Example:

  $ cd ./alitu-quickweb
  $ %[1]s --db ./alitu-quickweb.sqlite --addr 0.0.0.0:8797 --service-name alitu-quickweb

Systemd example:

  [Unit]
  Description=Quickweb instance
  After=network-online.target
  Wants=network-online.target

  [Service]
  Type=simple
  WorkingDirectory=/home/wallace/alitu-quickweb
  ExecStart=/usr/bin/go run github.com/Rocketable/platform/cmd/quickweb@main --db /home/wallace/alitu-quickweb/alitu-quickweb.sqlite --addr 0.0.0.0:8797 --service-name alitu-quickweb
  Restart=on-failure
  RestartSec=5s

  [Install]
  WantedBy=multi-user.target

Endpoints:

  GET /healthz
  GET /skills
  GET /data?path=/applet/
  PUT /data?path=/applet/   full JSON overwrite
  POST /data?path=/applet/  full JSON overwrite

Writes are always full overwrites. Quickweb does not implement PATCH, merge, append, or per-key updates.
`, cmd)
}
