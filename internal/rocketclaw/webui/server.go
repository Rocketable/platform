// Package webui serves the minimal browser voice-mode interface.
package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/voice"
)

const fallbackCertFilename = "web-ui.crt"
const fallbackKeyFilename = "web-ui.key"

const (
	// VoiceModePath is the browser voice-mode page route.
	VoiceModePath = "/voice-mode"
	// KeepalivePath is the lightweight browser keepalive route.
	KeepalivePath = "/keepalive"
)

// Server is the dedicated browser web UI HTTP server.
type Server struct {
	certFile string
	urls     []string
	mu       sync.Mutex
	closed   bool
	closeFn  func(context.Context) error
	hub      *voiceHub
}

// Start launches the browser-facing web UI listener.
func Start(
	ctx context.Context,
	logger *slog.Logger,
	workspace, listenAddr, certFile, keyFile string,
	transcriber transcriber,
	tts synthesizer,
	publisher *voice.TranscriptionPublisher,
) (*Server, error) {
	return StartIn(ctx, logger, workspace, config.DefaultWorkDir, listenAddr, certFile, keyFile, transcriber, tts, publisher)
}

// StartIn launches the browser-facing web UI listener using workDir for fallback TLS assets.
func StartIn(
	ctx context.Context,
	logger *slog.Logger,
	workspace, workDir, listenAddr, certFile, keyFile string,
	transcriber transcriber,
	tts synthesizer,
	publisher *voice.TranscriptionPublisher,
) (*Server, error) {
	assets, err := prepareTLSAssetsIn(workspace, workDir, listenAddr, certFile, keyFile, collectSystemInterfaceIPv4Addrs)
	if err != nil {
		return nil, fmt.Errorf("prepare web UI TLS assets: %w", err)
	}

	mux := http.NewServeMux()
	page := []byte(voiceModePage())
	hub := newVoiceHub(ctx, logger, transcriber, tts, publisher)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		http.Redirect(w, r, VoiceModePath, http.StatusTemporaryRedirect)
	})

	mux.HandleFunc(VoiceModePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)

			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(page)
	})

	mux.HandleFunc(KeepalivePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)

			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(struct {
			OK        bool   `json:"ok"`
			Reason    string `json:"reason,omitempty"`
			Timestamp string `json:"timestamp"`
		}{OK: true, Reason: r.Header.Get("X-RocketClaw-Reason"), Timestamp: time.Now().UTC().Format(time.RFC3339)})
	})

	mux.HandleFunc(voiceSocketPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)

			return
		}

		hub.handleWebsocket(w, r)
	})

	mux.HandleFunc(playbackPathPrefix, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)

			return
		}

		hub.servePlayback(w, r)
	})

	listener, err := net.Listen("tcp4", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen for web UI HTTPS server: %w", err)
	}

	httpServer := &http.Server{Handler: mux}

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return nil, fmt.Errorf("parse web UI listener address: %w", err)
	}

	urls := voiceModeURLs(assets.ips, port)
	server := &Server{urls: urls, certFile: assets.certFile, hub: hub}
	server.closeFn = func(closeCtx context.Context) error {
		var err error

		err = errors.Join(err, hub.close(closeCtx))
		err = errors.Join(err, httpServer.Shutdown(closeCtx))

		return err
	}

	go func() {
		err := httpServer.ServeTLS(listener, assets.certFile, assets.keyFile)
		if err != nil && err != http.ErrServerClosed && ctx.Err() == nil {
			logger.Error("web UI HTTPS server stopped", "error", err)
		}
	}()

	return server, nil
}

// SendResponse synthesizes and queues browser playback for a web voice session.
func (s *Server) SendResponse(ctx context.Context, msg *events.OutboundMessage) error {
	return s.hub.SendResponse(ctx, msg)
}

// URL returns the voice-mode page URL.
func (s *Server) URL() string { return s.urls[0] }

// URLs returns numeric IPv4 voice-mode page URLs served by this listener.
func (s *Server) URLs() []string { return append([]string(nil), s.urls...) }

// Name returns the server identifier used in logs.
func (s *Server) Name() string { return "web_ui" }

// Stop stops the HTTP server and waits for it to exit.
func (s *Server) Stop(ctx context.Context) error { return s.Close(ctx) }

// Close stops the HTTP server and waits for it to exit.
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	err := s.closeFn(ctx)
	if err == nil {
		s.closed = true
	}

	return err
}
