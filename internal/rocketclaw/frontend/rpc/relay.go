package rpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func handleOpenCode(mux *http.ServeMux, connection *grpc.ClientConn, cfg *config.LockedConfig) {
	servers := cfg.Clone().OpenCode

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

			ctx := metadata.AppendToOutgoingContext(r.Context(), "rocketclaw-username", identity.Username)

			parts := strings.Split(r.PathValue("path"), "/")
			if r.Method != http.MethodPost || len(parts) != 3 || parts[0] != "session" || parts[2] != "prompt" {
				proxy.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			_ = backend.AddOpenCodeSessionBridge(r.Context(), cfg, r.PathValue("server"), parts[1])

			id := "opencode:" + r.PathValue("server") + ":" + parts[1]
			admission := openCodeAdmission{connection: connection, id: id}
			proxy.ServeHTTP(w, r.WithContext(context.WithValue(ctx, admissionKey{}, &admission)))

			if admission.created > 0 {
				tab := "/oc/" + base64.RawURLEncoding.EncodeToString([]byte(id))
				_ = connection.Invoke(context.WithoutCancel(r.Context()), "/rpc.Web/ChangeTabs", &ChangeTabsRequest{Attention: &AttentionChange{Paths: []string{tab}, Action: AttentionAction_SENT, Admitted: admission.created}}, &TabsResponse{})
			}
		})
	}
}

type admissionKey struct{}

type openCodeAdmission struct {
	connection *grpc.ClientConn
	id         string
	created    int64
	mentions   json.RawMessage
}

const mentionWarningHeader = "X-Rocketclaw-Mention-Warning"

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

			r.Out.Header.Del("Accept-Encoding")

			raw, _ := io.ReadAll(r.Out.Body)

			var body, stamped map[string]json.RawMessage
			if json.Unmarshal(raw, &body) == nil && body != nil {
				_ = json.Unmarshal(body["metadata"], &stamped)
				if stamped == nil {
					stamped = map[string]json.RawMessage{}
				}

				if admission, ok := r.In.Context().Value(admissionKey{}).(*openCodeAdmission); ok {
					admission.mentions = stamped["rocketclaw_mention_ids"]
					delete(stamped, "rocketclaw_mention_ids")
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
			ctx := response.Request.Context()
			admission, admitted := ctx.Value(admissionKey{}).(*openCodeAdmission)

			admitted = admitted && response.StatusCode/100 == 2
			if admitted {
				raw, err := io.ReadAll(response.Body)
				if err != nil {
					return fmt.Errorf("read OpenCode prompt answer: %w", err)
				}

				var accepted struct {
					Data struct {
						Time struct {
							Created int64 `json:"created"`
						} `json:"time"`
					} `json:"data"`
				}
				if json.Unmarshal(raw, &accepted) == nil {
					admission.created = accepted.Data.Time.Created
				}

				response.Body = io.NopCloser(bytes.NewReader(raw))
			}

			response.Header.Del("Set-Cookie")
			response.Header.Del("WWW-Authenticate")
			response.Header.Del("Access-Control-Allow-Origin")
			response.Header.Set("Content-Security-Policy", "sandbox")
			response.Header.Set("X-Content-Type-Options", "nosniff")
			response.Header.Del(mentionWarningHeader)

			if admitted && len(admission.mentions) > 0 {
				var ids []string
				if err := json.Unmarshal(admission.mentions, &ids); err != nil {
					response.Header.Set(mentionWarningHeader, "Invalid teammate tags.")
					return nil
				}

				reply := &PromptResponse{}
				if err := admission.connection.Invoke(ctx, "/rpc.Web/NotifySessionMentions", &PromptRequest{Id: admission.id, MentionIds: ids}, reply); err != nil {
					slog.Warn("relay teammate notifications failed", "session", admission.id, "error", err)
					response.Header.Set(mentionWarningHeader, "Teammate notifications could not be sent.")
				} else if reply.MentionWarning != nil {
					response.Header.Set(mentionWarningHeader, *reply.MentionWarning)
				}
			}

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
