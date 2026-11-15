package rpc

import (
	"bytes"
	"encoding/base64"
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
	"google.golang.org/grpc/status"
)

func handleOpenCode(mux *http.ServeMux, connection *grpc.ClientConn, servers map[string]config.OpenCodeConfig) {
	proxies := make(map[string]http.Handler, len(servers))
	for name, server := range servers {
		proxies[name] = http.StripPrefix("/oc/"+name, openCodeProxy(server))
	}

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

			if identity.GetLimited() {
				http.Error(w, status.Convert(errOpenCodeClosed).Message(), http.StatusForbidden)
				return
			}

			if parts := strings.Split(r.PathValue("path"), "/"); r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "session" && parts[2] == "prompt" {
				tab := "/oc/" + base64.RawURLEncoding.EncodeToString([]byte("opencode:"+r.PathValue("server")+":"+parts[1]))
				_ = connection.Invoke(r.Context(), "/rpc.Web/Tabs", &TabsRequest{Attention: &AttentionChange{Paths: []string{tab}, Action: AttentionAction_SENT}}, &TabsResponse{})
			}

			proxy.ServeHTTP(w, r.WithContext(metadata.AppendToOutgoingContext(r.Context(), "rocketclaw-username", identity.Username)))
		})
	}
}

func openCodeProxy(server config.OpenCodeConfig) http.Handler {
	upstream, _ := url.Parse(server.URL)
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
			response.Header.Del("Access-Control-Allow-Origin")
			response.Header.Set("Content-Security-Policy", "sandbox")
			response.Header.Set("X-Content-Type-Options", "nosniff")

			return nil
		},
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
