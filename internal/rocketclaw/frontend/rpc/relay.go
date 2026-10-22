package rpc

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"path"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// NewOpenCodeRelay serves one remote OpenCode server at the root of its own port with the
// server's password. It refuses any request, WebSocket upgrades included, whose Origin is
// another site, and sends the upstream its own origin so OpenCode's PTY check still holds.
// Only pages on the same host may frame it. Callers add admission.
func NewOpenCodeRelay(server config.OpenCodeConfig) http.Handler {
	upstream, _ := url.Parse(server.URL) // Config validation already parsed it.
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.SetBasicAuth("opencode", server.Password)

			if r.In.Header.Get("Origin") != "" {
				r.Out.Header.Set("Origin", upstream.Scheme+"://"+upstream.Host)
			}

			if prompt, _ := path.Match("/api/session/*/prompt", r.In.URL.Path); !prompt || r.In.Method != http.MethodPost || r.Out.Body == nil {
				return
			}

			// OpenCode stores a prompt's metadata on its user message; the sender rides there.
			// A failed read forwards what arrived, which OpenCode rejects as malformed.
			raw, _ := io.ReadAll(r.Out.Body)

			var body, stamped map[string]json.RawMessage
			if json.Unmarshal(raw, &body) == nil && body != nil {
				_ = json.Unmarshal(body["metadata"], &stamped)
				if stamped == nil {
					stamped = map[string]json.RawMessage{}
				}

				md, _ := metadata.FromOutgoingContext(r.In.Context())
				stamped["rocketclaw_username"], _ = json.Marshal(md.Get("rocketclaw-tailnet-login-bin")[0])
				body["metadata"], _ = json.Marshal(stamped)
				raw, _ = json.Marshal(body)
			}

			r.Out.Body = io.NopCloser(bytes.NewReader(raw))
			r.Out.ContentLength = int64(len(raw))
		},
		FlushInterval: -1,
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		if origin := r.Header.Get("Origin"); origin != "" && origin != scheme+"://"+r.Host {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}

		host := (&url.URL{Host: r.Host}).Hostname()
		w.Header().Set("Content-Security-Policy", "frame-ancestors http://"+net.JoinHostPort(host, "*")+" https://"+net.JoinHostPort(host, "*"))
		proxy.ServeHTTP(w, r)
	})
}

// AdmitHTTP serves next only to browsers the Web admits by their IP: web_users, then Tailscale.
// The tailnet listener admits with NewTailnetHandler instead.
func (s *Server) AdmitHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		address, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil {
			httpRPCError(w, status.Error(codes.Unauthenticated, "invalid remote address"))
			return
		}

		username, _, err := s.principal(metadata.NewIncomingContext(r.Context(), metadata.Pairs("rocketclaw-principal", address.Addr().Unmap().String())))
		if err != nil {
			httpRPCError(w, err)
			return
		}

		// The relay reads the sender where NewTailnetHandler leaves the tailnet login.
		next.ServeHTTP(w, r.WithContext(metadata.AppendToOutgoingContext(r.Context(), "rocketclaw-tailnet-login-bin", username)))
	})
}
