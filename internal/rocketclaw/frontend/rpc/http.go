package rpc

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/internal/web"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"tailscale.com/client/tailscale/apitype"
)

// NewHTTPHandler bridges the browser API to the local Web service, and each remote OpenCode
// server's API to the browsers the Web admits under /oc/{server}/api/. serverName prefixes the page
// title when cfg has no server_display_name.
func NewHTTPHandler(connection *grpc.ClientConn, cfg *config.LockedConfig, serverName string) http.Handler {
	mux := http.NewServeMux()
	protection := http.NewCrossOriginProtection()
	app := web.Handler(cfg, serverName)

	var gzipWriters sync.Pool

	mux.Handle("GET /", app)
	mux.Handle("GET /api/", http.NotFoundHandler())
	mux.Handle("POST /api/", http.NotFoundHandler())

	handleOpenCode(mux, connection, cfg.Clone().OpenCode)

	for _, route := range []string{"GET /pages/{space}/{agent}/{path...}", "GET /pages-raw/{agent}/{path...}"} {
		mux.HandleFunc(route, servePage(connection, app))
	}

	for method := range strings.FieldsSeq("Protocol Identity Reload Prompt History ForkSession StageRevert ClearRevert SearchMessages SearchSessions Board SlackNames Handoff ListAgents CreateSession ListConfig SaveBehaviorSettings InspectAgents ListSkills ListWorkflows UpdateSession ListCronJobs RunCronJob GetPage ListPages ListSessionEntries LoadSessionEntries DeleteSessionEntries ListQueue SteerQueueItem PopQueueItem RemoveQueueItem EditQueueItem ReorderQueue MoveToBackground StopBackgroundJob AnswerQuestion VoiceSettings Tabs") {
		descriptor := File_web_proto.Services().ByName("Web").Methods().ByName(protoreflect.Name(method))
		requestType, _ := protoregistry.GlobalTypes.FindMessageByName(descriptor.Input().FullName())
		responseType, _ := protoregistry.GlobalTypes.FindMessageByName(descriptor.Output().FullName())

		mux.HandleFunc("POST /api/"+method, func(w http.ResponseWriter, r *http.Request) {
			request, response := requestType.New().Interface(), responseType.New().Interface()

			if err := httpInput(w, r, request, method); err != nil {
				httpRPCError(w, status.Error(codes.InvalidArgument, err.Error()))
				return
			}

			// Match the in-process server's send limit; history pages count entries, not bytes.
			if err := connection.Invoke(r.Context(), "/rpc.Web/"+method, request, response, grpc.MaxCallRecvMsgSize(math.MaxInt32)); err != nil {
				httpRPCError(w, err)
				return
			}

			body, err := (protojson.MarshalOptions{EmitDefaultValues: true}).Marshal(response)
			if err != nil {
				httpRPCError(w, err)
				return
			}

			httpJSON(w, r, body, &gzipWriters)
		})
	}

	for _, route := range []string{"GET /api/ListSessions", "GET /stream", "GET /api/DownloadAttachment", "POST /api/UploadAttachment", "POST /api/Voice"} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			method := strings.TrimPrefix(r.URL.Path, "/api/")

			var (
				request  proto.Message = &ListSessionsRequest{}
				response proto.Message = &ListSessionsResponse{}
				required string
			)

			switch method {
			case "ListSessions":
				// Ending views and clearing marks takes a same-origin poll, by the rule protection holds
				// unsafe methods to; it lets every GET through.
				post := *r
				post.Method = http.MethodPost

				list := &ListSessionsRequest{Since: r.URL.Query().Get("since")}
				if protection.Check(&post) == nil {
					list.Viewing = r.URL.Query().Get("viewing")
				}

				request = list
			case "/stream":
				method, request, response, required = "Join", &JoinRequest{Id: r.URL.Query().Get("id")}, &ConversationChange{}, "id"
			case "DownloadAttachment", "UploadAttachment":
				request = &Attachment{ConversationId: r.URL.Query().Get("conversationId"), Id: r.URL.Query().Get("id"), Name: r.URL.Query().Get("name")}
				response, required = &Attachment{}, map[string]string{"DownloadAttachment": "conversationId id", "UploadAttachment": "conversationId name"}[method]
			case "Voice":
				request, response = &VoiceRequest{}, &VoiceEvent{}
				if err := httpInput(w, r, request, method); err != nil {
					httpRPCError(w, status.Error(codes.InvalidArgument, err.Error()))
					return
				}
			}

			for name := range strings.FieldsSeq(required) {
				if !r.URL.Query().Has(name) {
					httpRPCError(w, status.Errorf(codes.InvalidArgument, "%s is required", name))
					return
				}
			}

			stream, err := connection.NewStream(r.Context(), &grpc.StreamDesc{ClientStreams: method == "UploadAttachment", ServerStreams: method != "UploadAttachment"}, "/rpc.Web/"+method)
			if err == nil {
				err = stream.SendMsg(request)
			}

			if method == "UploadAttachment" && err == nil {
				err = httpUpload(r.Body, stream)
			}

			if err == nil {
				err = stream.CloseSend()
			}

			if err == nil || errors.Is(err, io.EOF) {
				err = stream.RecvMsg(response)
			}

			if method == "UploadAttachment" {
				if err == nil {
					err = stream.RecvMsg(&Attachment{})
					if errors.Is(err, io.EOF) {
						body, errMarshal := (protojson.MarshalOptions{EmitDefaultValues: true}).Marshal(response)
						if errMarshal == nil {
							httpJSON(w, r, body, &gzipWriters)
							return
						}

						err = errMarshal
					} else if err == nil {
						err = status.Error(codes.Internal, "multiple upload receipts")
					}
				}

				httpRPCError(w, err)

				return
			}

			if err != nil && method != "ListSessions" {
				httpRPCError(w, err)
				return
			}

			if method == "DownloadAttachment" {
				httpDownload(w, r, stream, response.(*Attachment))
				return
			}

			httpEvents(w, r, stream, response, err, &gzipWriters)
		})
	}

	return protection.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		address, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil {
			httpRPCError(w, status.Error(codes.Unauthenticated, "invalid remote address"))
			return
		}

		ip := address.Addr().Unmap()

		ctx, cancel := context.WithCancel(metadata.AppendToOutgoingContext(r.Context(), "rocketclaw-principal", ip.String(), "rocketclaw-direct-local", strconv.FormatBool(directLocal(r, ip))))
		defer cancel()

		mux.ServeHTTP(w, r.WithContext(ctx))
	}))
}

// directLocal reports whether r came straight from this host: from loopback ip, without a header
// that a relay such as tailscale serve adds.
func directLocal(r *http.Request, ip netip.Addr) bool {
	for name := range r.Header {
		if name == "X-Forwarded-For" || name == "Forwarded" || strings.HasPrefix(name, "Tailscale-") {
			return false
		}
	}

	return ip.IsLoopback()
}

// servePage answers /pages-raw/ with the file, sandboxed into an opaque origin, and /pages/ with the Web app,
// after redirecting a link that names a space other than the agent's.
func servePage(connection *grpc.ClientConn, app http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := &Page{}
		if err := connection.Invoke(r.Context(), "/rpc.Web/GetPage", &PageRequest{Agent: r.PathValue("agent"), Path: r.PathValue("path")}, page, grpc.MaxCallRecvMsgSize(math.MaxInt32)); err != nil {
			httpRPCError(w, err)
			return
		}

		switch {
		case strings.HasPrefix(r.URL.Path, "/pages-raw/"):
			w.Header().Set("Content-Type", page.ContentType)
			w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-popups allow-forms allow-modals allow-downloads")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(page.Body)
		case page.Space != "" && !strings.EqualFold(page.Space, r.PathValue("space")):
			// Keep the request's escaped agent, path, and query; only the space segment moves.
			http.Redirect(w, r, "/pages/"+url.PathEscape(page.Space)+"/"+strings.SplitN(r.URL.RequestURI(), "/", 4)[3], http.StatusPermanentRedirect)
		default:
			app.ServeHTTP(w, r)
		}
	}
}

// tailnetWhoIs is satisfied by *local.Client.
type tailnetWhoIs interface {
	WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error)
}

// NewTailnetHandler signs each request in by its tailnet connection and refuses tagged or unknown callers,
// except at coding servers' MCP endpoints, which check their callers' passwords themselves.
// next must serve NewHTTPHandler, which appends the IP principal to this identity.
func NewTailnetHandler(client tailnetWhoIs, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bridge, _ := path.Match("/opencode/*/mcp", r.URL.Path); bridge {
			next.ServeHTTP(w, r)
			return
		}

		who, err := client.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil || who.Node.IsTagged() || who.UserProfile.LoginName == "" {
			httpRPCError(w, status.Error(codes.Unauthenticated, "tailnet caller has no Tailscale user"))
			return
		}

		// Names may be non-ASCII, which only -bin metadata carries.
		next.ServeHTTP(w, r.WithContext(metadata.AppendToOutgoingContext(r.Context(), "rocketclaw-tailnet-login-bin", who.UserProfile.LoginName, "rocketclaw-tailnet-name-bin", who.UserProfile.DisplayName)))
	})
}

// httpJSON writes a JSON body, gzip-compressed when the client accepts it.
func httpJSON(w http.ResponseWriter, r *http.Request, body []byte, writers *sync.Pool) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Vary", "Accept-Encoding")

	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		_, _ = w.Write(body)
		return
	}

	w.Header().Set("Content-Encoding", "gzip")

	compressed, ok := writers.Get().(*gzip.Writer)
	if !ok {
		compressed = gzip.NewWriter(io.Discard)
	}

	compressed.Reset(w)
	_, _ = compressed.Write(body)
	_ = compressed.Close()
	compressed.Reset(io.Discard)
	writers.Put(compressed)
}

func httpUpload(body io.Reader, stream grpc.ClientStream) error {
	buffer := make([]byte, attachmentChunkBytes)
	for {
		n, errRead := body.Read(buffer)
		if n > 0 {
			if err := stream.SendMsg(&Attachment{Data: buffer[:n]}); err != nil {
				return fmt.Errorf("send upload chunk: %w", err)
			}
		}

		if errors.Is(errRead, io.EOF) {
			return nil
		}

		if errRead != nil {
			return fmt.Errorf("read upload body: %w", status.Error(codes.InvalidArgument, errRead.Error()))
		}
	}
}

func httpDownload(w http.ResponseWriter, r *http.Request, stream grpc.ClientStream, file *Attachment) {
	contentType, _, errMIME := mime.ParseMediaType(file.MimeType)
	if errMIME != nil {
		contentType = "application/octet-stream"
	}

	disposition := "attachment"
	if r.URL.Query().Get("download") != "1" && slices.Contains([]string{"image/jpeg", "image/png", "image/gif", "image/webp", "image/avif"}, contentType) {
		disposition = "inline"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": file.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))

	for {
		if _, err := w.Write(file.Data); err != nil {
			return
		}

		err := stream.RecvMsg(file)
		if errors.Is(err, io.EOF) {
			return
		}

		if err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

// httpEvents writes a stream as server-sent events. The session list, which the Web reloads every few seconds, goes
// gzip-compressed and flushed per event to browsers that accept it.
func httpEvents(w http.ResponseWriter, r *http.Request, stream grpc.ClientStream, response proto.Message, err error, writers *sync.Pool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")

	controller := http.NewResponseController(w)
	out, flush := io.Writer(w), controller.Flush

	if r.URL.Path == "/api/ListSessions" && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")

		compressed, ok := writers.Get().(*gzip.Writer)
		if !ok {
			compressed = gzip.NewWriter(io.Discard)
		}

		compressed.Reset(w)
		defer func() {
			_ = compressed.Close()
			compressed.Reset(io.Discard)
			writers.Put(compressed)
		}()

		out, flush = compressed, func() error {
			if err := compressed.Flush(); err != nil {
				return fmt.Errorf("flush gzip: %w", err)
			}

			return controller.Flush()
		}
	}

	var body []byte

	for {
		if errors.Is(err, io.EOF) {
			_, _ = io.WriteString(out, "event: complete\ndata: {}\n\n")
			return
		}

		if err != nil {
			body, _ := json.Marshal(struct {
				Code    codes.Code `json:"code"`
				Message string     `json:"message"`
			}{status.Code(err), status.Convert(err).Message()})
			_, _ = fmt.Fprintf(out, "event: error\ndata: %s\n\n", body)

			return
		}

		var errMarshal error

		body, errMarshal = (protojson.MarshalOptions{EmitDefaultValues: true}).MarshalAppend(append(body[:0], "data: "...), response)
		if errMarshal != nil {
			err = errMarshal
			continue
		}

		body = append(body, '\n', '\n')
		if _, err := out.Write(body); err != nil {
			return
		}

		if err := flush(); err != nil {
			return
		}

		// A listing ends after its inventory; long-lived streams must not retain a large frame while idle.
		if r.URL.Path != "/api/ListSessions" {
			body = nil
		}

		err = stream.RecvMsg(response)
	}
}

// httpInput decodes a JSON request body of at most 4 MiB.
func httpInput(w http.ResponseWriter, r *http.Request, request proto.Message, method string) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return fmt.Errorf("decode request fields: %w", err)
	}

	if fields == nil {
		return errors.New("request must be an object")
	}

	required := ""

	switch method {
	case "Prompt":
		required = "id text"
	case "History", "ForkSession", "StageRevert", "ClearRevert", "Handoff", "UpdateSession", "ListSessionEntries", "LoadSessionEntries", "DeleteSessionEntries", "ListQueue":
		required = "id"
	case "SearchMessages", "SearchSessions":
		required = "query"
	case "SlackNames":
		required = "ids"
	case "RunCronJob":
		required = "stem"
	case "MoveToBackground":
		required = "conversationId"
	case "SteerQueueItem", "PopQueueItem", "RemoveQueueItem", "EditQueueItem":
		required = "id itemId"
	case "ReorderQueue":
		required = "id itemIds"
	case "StopBackgroundJob":
		required = "conversationId jobId"
	case "AnswerQuestion":
		required = "conversationId askId"
	case "Voice":
		required = "conversationId sdp"
	case "SaveBehaviorSettings":
		required = "documentJson"
	}

	for name := range strings.FieldsSeq(required) {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("%s is required", name)
		}
	}

	for name, value := range fields {
		field := request.ProtoReflect().Descriptor().Fields().ByJSONName(name)
		if field == nil || string(value) == "null" {
			return fmt.Errorf("invalid field %s", name)
		}

		if field.Kind() == protoreflect.EnumKind {
			var symbol string
			if err := json.Unmarshal(value, &symbol); err != nil || field.Enum().Values().ByName(protoreflect.Name(symbol)) == nil {
				return fmt.Errorf("invalid %s: expected a %s name", name, field.Enum().Name())
			}
		}

		if method == "ListSkills" && name == "agent" && string(value) == `""` {
			return errors.New("agent must not be empty")
		}
	}

	if err := protojson.Unmarshal(body, request); err != nil {
		return fmt.Errorf("decode protobuf request: %w", err)
	}

	return nil
}

func httpRPCError(w http.ResponseWriter, err error) {
	code := status.Code(err)
	httpCode := http.StatusInternalServerError

	switch code {
	case codes.OK, codes.Unknown, codes.Internal, codes.DataLoss:
		httpCode = http.StatusInternalServerError
	case codes.InvalidArgument, codes.OutOfRange, codes.FailedPrecondition:
		httpCode = http.StatusBadRequest
	case codes.Unauthenticated:
		httpCode = http.StatusUnauthorized
	case codes.PermissionDenied:
		httpCode = http.StatusForbidden
	case codes.NotFound:
		httpCode = http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted:
		httpCode = http.StatusConflict
	case codes.ResourceExhausted:
		httpCode = http.StatusTooManyRequests
	case codes.Unavailable:
		httpCode = http.StatusServiceUnavailable
	case codes.DeadlineExceeded:
		httpCode = http.StatusGatewayTimeout
	case codes.Canceled:
		httpCode = http.StatusRequestTimeout
	case codes.Unimplemented:
		httpCode = http.StatusNotImplemented
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpCode)
	_ = json.NewEncoder(w).Encode(struct {
		Code    codes.Code `json:"code"`
		Message string     `json:"message"`
	}{code, status.Convert(err).Message()})
}
