package rpc

import (
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

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

func NewHTTPHandler(connection *grpc.ClientConn, cfg *config.LockedConfig, serverName string) http.Handler {
	mux := http.NewServeMux()
	protection := http.NewCrossOriginProtection()
	protection.AddInsecureBypassPattern("/pages-raw/")

	app := web.Handler(cfg, serverName)

	var gzipWriters sync.Pool

	unknown := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accessOf(w).reason = "unknown method"
		http.NotFound(w, r)
	})

	mux.Handle("GET /", app)
	mux.Handle("GET /api/", unknown)
	mux.Handle("POST /api/", unknown)

	handleOpenCode(mux, connection, cfg)

	for _, route := range []string{"GET /pages/{space}/{agent}/{path...}", "GET /pages-raw/{agent}/{path...}"} {
		mux.HandleFunc(route, servePage(connection, app))
	}

	for method := range strings.FieldsSeq(webUnaryMethods + " " + editorMethods) {
		descriptor := File_web_proto.Services().ByName("Web").Methods().ByName(protoreflect.Name(method))
		requestType, _ := protoregistry.GlobalTypes.FindMessageByName(descriptor.Input().FullName())
		responseType, _ := protoregistry.GlobalTypes.FindMessageByName(descriptor.Output().FullName())

		mux.HandleFunc("POST /api/"+method, func(w http.ResponseWriter, r *http.Request) {
			request, response := requestType.New().Interface(), responseType.New().Interface()

			if err := httpInput(w, r, request, method); err != nil {
				httpRPCError(w, status.Error(codes.InvalidArgument, err.Error()))
				return
			}

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

	for _, route := range []string{"GET /api/ListSessions", "GET /stream", "GET /typing", "GET /api/DownloadAttachment", "POST /api/UploadAttachment", "POST /api/Voice"} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			method, query, required := strings.TrimPrefix(r.URL.Path, "/api/"), url.Values(nil), ""
			if method != "Voice" {
				query = r.URL.Query()
			}

			var request, response proto.Message

			switch method {
			default:
				post := *r
				post.Method = http.MethodPost

				list := &ListSessionsRequest{Since: query.Get("since")}
				if protection.Check(&post) == nil {
					list.Viewing, list.Hold, list.SameOrigin = query.Get("viewing"), query.Has("hold"), true
				}

				request, response = list, &ListSessionsResponse{}
			case "/stream":
				method, request, response, required = "Join", &JoinRequest{Id: query.Get("id")}, &ConversationChange{}, "id"
			case "/typing":
				method, request, response, required = "WatchTyping", &WatchTypingRequest{Id: query.Get("id")}, &TypingEvent{}, "id"
			case "DownloadAttachment", "UploadAttachment":
				request = &Attachment{ConversationId: query.Get("conversationId"), Id: query.Get("id"), Name: query.Get("name")}
				response, required = &Attachment{}, map[string]string{"DownloadAttachment": "conversationId id", "UploadAttachment": "conversationId name"}[method]
			case "Voice":
				request, response = &VoiceRequest{}, &VoiceEvent{}
				if err := httpInput(w, r, request, method); err != nil {
					httpRPCError(w, status.Error(codes.InvalidArgument, err.Error()))
					return
				}
			}

			for name := range strings.FieldsSeq(required) {
				if !query.Has(name) {
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
				httpDownload(w, stream, response.(*Attachment), query.Get("download") == "1")
				return
			}

			httpEvents(w, r, stream, response, err, &gzipWriters)
		})
	}

	return refuseOtherSites(protection, mux)
}

func refuseOtherSites(protection *http.CrossOriginProtection, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checked := *r
		if r.Header.Get("Sec-Fetch-Mode") != "navigate" {
			checked.Method = http.MethodPost
		}

		if err := protection.Check(&checked); err != nil {
			accessOf(w).reason = err.Error()
			http.Error(w, err.Error(), http.StatusForbidden)

			return
		}

		next.ServeHTTP(w, r)
	})
}

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
			w.Header().Add("Content-Security-Policy", "sandbox allow-scripts allow-popups allow-forms allow-modals allow-downloads")
			w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(page.Body)
		case page.Space != "" && !strings.EqualFold(page.Space, r.PathValue("space")):
			http.Redirect(w, r, "/pages/"+url.PathEscape(page.Space)+"/"+strings.SplitN(r.URL.RequestURI(), "/", 4)[3], http.StatusPermanentRedirect)
		default:
			app.ServeHTTP(w, r)
		}
	}
}

type tailnetWhoIs interface {
	WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error)
}

func NewTailnetHandler(client tailnetWhoIs, trusted []string, next http.Handler) http.Handler {
	ancestors := strings.Join(append([]string{"frame-ancestors 'self'"}, trusted...), " ")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bridge, _ := path.Match("/opencode/*/mcp", r.URL.Path); bridge {
			next.ServeHTTP(w, r)
			return
		}

		if r.URL.Path == "/stream" || r.URL.Path == "/typing" || strings.HasPrefix(r.URL.Path, "/api/") {
			access := &rpcAccess{writer: w}
			defer access.log(r, time.Now())

			w = access
		}

		w.Header().Set("Content-Security-Policy", ancestors)
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")

		origin := r.Header.Get("Origin")

		page := origin != "" && slices.Contains(trusted, origin)
		if page {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
		}

		who, err := client.WhoIs(r.Context(), r.RemoteAddr)
		if err == nil {
			accessOf(w).login = who.UserProfile.LoginName
		}

		if err != nil || who.Node.IsTagged() || who.UserProfile.LoginName == "" {
			const refusal = "tailnet caller has no Tailscale user"

			accessOf(w).reason = refusal
			httpRPCError(w, status.Error(codes.Unauthenticated, refusal))

			return
		}

		r = r.Clone(metadata.AppendToOutgoingContext(r.Context(), "rocketclaw-tailnet-login-bin", who.UserProfile.LoginName, "rocketclaw-tailnet-name-bin", who.UserProfile.DisplayName))

		if page {
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", r.Header.Get("Access-Control-Request-Method"))
				w.Header().Set("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
				w.Header().Set("Access-Control-Max-Age", "7200")
				w.WriteHeader(http.StatusNoContent)

				return
			}

			r.Header.Set("Origin", "https://"+r.Host)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
		}

		next.ServeHTTP(w, r)
	})
}

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

func httpDownload(w http.ResponseWriter, stream grpc.ClientStream, file *Attachment, forced bool) {
	contentType, _, errMIME := mime.ParseMediaType(file.MimeType)
	if errMIME != nil {
		contentType = "application/octet-stream"
	}

	disposition := "attachment"
	if !forced && slices.Contains([]string{"image/jpeg", "image/png", "image/gif", "image/webp", "image/avif"}, contentType) {
		disposition = "inline"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": file.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Add("Content-Security-Policy", "sandbox; default-src 'none'")
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
			accessOf(w).code = status.Code(err).String()

			panic(http.ErrAbortHandler)
		}
	}
}

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

	encoding := protojson.MarshalOptions{EmitDefaultValues: r.URL.Path != "/api/ListSessions"}

	for {
		if errors.Is(err, io.EOF) {
			_, _ = io.WriteString(out, "event: complete\ndata: {}\n\n")
			return
		}

		if err != nil {
			accessOf(w).code = status.Code(err).String()
			body, _ := json.Marshal(struct {
				Code    codes.Code `json:"code"`
				Message string     `json:"message"`
			}{status.Code(err), status.Convert(err).Message()})
			_, _ = fmt.Fprintf(out, "event: error\ndata: %s\n\n", body)

			return
		}

		body, err = encoding.MarshalAppend(append(body[:0], "data: "...), response)
		if err != nil {
			continue
		}

		body = append(body, '\n', '\n')
		if _, err := out.Write(body); err != nil {
			return
		}

		if err := flush(); err != nil {
			return
		}

		if r.URL.Path != "/api/ListSessions" {
			body = nil
		}

		err = stream.RecvMsg(response)
	}
}

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
	case "History", "ForkSession", "StageRevert", "ClearRevert", "Handoff", "UpdateSession", "ListSessionEntries", "LoadSessionEntries", "DeleteSessionEntries", "DeleteSession", "ListQueue", "ReportTyping":
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

	accessOf(w).code = code.String()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpCode)
	_ = json.NewEncoder(w).Encode(struct {
		Code    codes.Code `json:"code"`
		Message string     `json:"message"`
	}{code, status.Convert(err).Message()})
}

const webBackgroundMethods = "Protocol Identity ListSessions ReportTyping"

type rpcAccess struct {
	writer http.ResponseWriter
	status int
	code   string
	login  string
	reason string
}

func accessOf(w http.ResponseWriter) *rpcAccess {
	if access, ok := w.(*rpcAccess); ok {
		return access
	}

	return &rpcAccess{}
}

func (a *rpcAccess) Header() http.Header {
	return a.writer.Header()
}

func (a *rpcAccess) WriteHeader(statusCode int) {
	a.status = cmp.Or(a.status, statusCode)
	a.writer.WriteHeader(statusCode)
}

func (a *rpcAccess) Write(body []byte) (int, error) {
	a.status = cmp.Or(a.status, http.StatusOK)

	n, err := a.writer.Write(body)
	if err != nil {
		return n, fmt.Errorf("write web response: %w", err)
	}

	return n, nil
}

func (a *rpcAccess) Unwrap() http.ResponseWriter {
	return a.writer
}

func (a *rpcAccess) log(r *http.Request, start time.Time) {
	answered, code := cmp.Or(a.status, http.StatusOK), a.code
	if code == "" && answered < http.StatusBadRequest {
		code = codes.OK.String()
	}

	method, level := cmp.Or(map[string]string{"/stream": "Join", "/typing": "WatchTyping"}[r.URL.Path], strings.TrimPrefix(r.URL.Path, "/api/")), slog.LevelInfo
	if slices.Contains(strings.Fields(webBackgroundMethods), method) {
		level = slog.LevelDebug
	}

	slog.Log(r.Context(), level, "rpc request", "method", method, "login", a.login, "http_method", r.Method, "content_type", r.Header.Get("Content-Type"), "user_agent", r.UserAgent(), "origin", r.Header.Get("Origin"),
		"remote_addr", r.RemoteAddr, "status", answered, "code", code, "reason", a.reason, "duration_ms", time.Since(start).Milliseconds())
}
