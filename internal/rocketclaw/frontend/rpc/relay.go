package rpc

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func handleOpenCode(mux *http.ServeMux, connection *grpc.ClientConn, servers map[string]config.OpenCodeConfig) {
	proxies := make(map[string]http.Handler, len(servers))
	for name, server := range servers {
		proxies[name] = http.StripPrefix("/oc/"+name, openCodeProxy(server))
	}

	// Registered per method, as "GET /" leaves no room for a method-less pattern.
	for method := range strings.FieldsSeq("GET POST PUT PATCH DELETE") {
		mux.HandleFunc(method+" /oc/{server}/api/{path...}", func(w http.ResponseWriter, r *http.Request) {
			proxy, ok := proxies[r.PathValue("server")]
			if !ok {
				http.NotFound(w, r)
				return
			}

			identity := &IdentityResponse{}
			if err := connection.Invoke(r.Context(), "/rpc.Web/Identity", &IdentityRequest{}, identity); err != nil {
				httpRPCError(w, err)
				return
			}

			// The proxy stamps prompts with the person Identity admitted.
			proxy.ServeHTTP(w, r.WithContext(metadata.AppendToOutgoingContext(r.Context(), "rocketclaw-username", identity.Username)))
		})
	}
}

// openCodeProxy serves one remote OpenCode server's API under /oc/{server}/ on the Web's own
// origin, with the server's password. It refuses any request, WebSocket upgrades included,
// whose Origin is another site, and sends the upstream its own origin so OpenCode's PTY check
// still holds. Cookies cross in neither direction, the browser is never asked to sign in, and a
// response opened directly runs sandboxed rather than as RocketClaw. Prompts carry the sender
// as metadata.rocketclaw_username. Callers add admission and the admitted username.
func openCodeProxy(server config.OpenCodeConfig) http.Handler {
	upstream, _ := url.Parse(server.URL) // Config validation already parsed it.
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.SetBasicAuth("opencode", server.Password)
			r.Out.Header.Del("Cookie")

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
				stamped["rocketclaw_username"], _ = json.Marshal(md.Get("rocketclaw-username")[0])
				body["metadata"], _ = json.Marshal(stamped)
				raw, _ = json.Marshal(body)
			}

			r.Out.Body = io.NopCloser(bytes.NewReader(raw))
			r.Out.ContentLength = int64(len(raw))
		},
		ModifyResponse: func(response *http.Response) error {
			response.Header.Del("Set-Cookie")
			response.Header.Del("WWW-Authenticate")
			response.Header.Set("Content-Security-Policy", "sandbox")
			response.Header.Set("X-Content-Type-Options", "nosniff")

			return nil
		},
		// A browser that left takes no reply, and its cancellation is not a failure worth logging.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() == nil {
				log.Printf("http: proxy error: %v", err)
				w.WriteHeader(http.StatusBadGateway)
			}
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

		proxy.ServeHTTP(w, r)
	})
}
