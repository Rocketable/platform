package rpc

import (
	"bufio"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
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

	mux.Handle("GET /", app)
	mux.Handle("GET /api/", http.NotFoundHandler())
	mux.Handle("POST /api/", http.NotFoundHandler())

	handleOpenCode(mux, connection, cfg)

	for _, route := range []string{"GET /pages/{space}/{path...}", "GET /pages-raw/{space}/{path...}"} {
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

	for _, route := range []string{"GET /api/ListSessions", "GET /stream", "GET /typing", "GET /listing", "GET /api/DownloadAttachment", "POST /api/UploadAttachment"} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			method, query, required := strings.TrimPrefix(r.URL.Path, "/api/"), r.URL.Query(), ""

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
			case "/listing":
				method, request, response = "WatchListing", &WatchListingRequest{}, &ListingChanged{}
			case "DownloadAttachment", "UploadAttachment":
				request = &Attachment{ConversationId: query.Get("conversationId"), Id: query.Get("id"), Name: query.Get("name")}
				response, required = &Attachment{}, map[string]string{"DownloadAttachment": "conversationId id", "UploadAttachment": "conversationId name"}[method]
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
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func servePage(connection *grpc.ClientConn, app http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := &Page{}
		if err := connection.Invoke(r.Context(), "/rpc.Web/GetPage", &PageRequest{Space: r.PathValue("space"), Path: r.PathValue("path")}, page, grpc.MaxCallRecvMsgSize(math.MaxInt32)); err != nil {
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
		default:
			app.ServeHTTP(w, r)
		}
	}
}

type tailnetWhoIs interface {
	WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error)
}

type accessRecorder struct {
	writer http.ResponseWriter
	status int
	bytes  int
}

func (a *accessRecorder) Header() http.Header {
	return a.writer.Header()
}

func (a *accessRecorder) WriteHeader(code int) {
	if code >= http.StatusOK || code == http.StatusSwitchingProtocols {
		a.status = cmp.Or(a.status, code)
	}

	a.writer.WriteHeader(code)
}

func (a *accessRecorder) Write(body []byte) (int, error) {
	a.status = cmp.Or(a.status, http.StatusOK)
	n, err := a.writer.Write(body)
	a.bytes += n

	if err != nil {
		return n, fmt.Errorf("write response: %w", err)
	}

	return n, nil
}

func (a *accessRecorder) Flush() {
	_ = a.FlushError()
}

func (a *accessRecorder) FlushError() error {
	if err := http.NewResponseController(a.writer).Flush(); err != nil {
		return fmt.Errorf("flush response: %w", err)
	}

	return nil
}

func (a *accessRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	connection, buffered, err := http.NewResponseController(a.writer).Hijack()
	if err != nil {
		return nil, nil, fmt.Errorf("hijack response: %w", err)
	}

	a.status = cmp.Or(a.status, http.StatusSwitchingProtocols)

	return connection, buffered, nil
}

func (a *accessRecorder) Unwrap() http.ResponseWriter {
	return a.writer
}

func NewTailnetHandler(client tailnetWhoIs, trusted []string, accessLog io.Writer, next http.Handler) http.Handler {
	ancestors := strings.Join(append([]string{"frame-ancestors 'self'"}, trusted...), " ")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, login, recorded := time.Now(), "-", &accessRecorder{writer: w}
		w = recorded

		defer func(r *http.Request) {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			_, _ = fmt.Fprintf(accessLog, "%s - %s [%s] %s %d %d %s %s %dms\n", host, login, start.Format("02/Jan/2006:15:04:05 -0700"),
				strconv.Quote(r.Method+" "+r.URL.EscapedPath()+" "+r.Proto), cmp.Or(recorded.status, http.StatusOK), recorded.bytes,
				strconv.Quote(cmp.Or(r.Referer(), "-")), strconv.Quote(cmp.Or(r.UserAgent(), "-")), time.Since(start).Milliseconds())
		}(r)

		if bridge, _ := path.Match("/opencode/*/mcp", r.URL.Path); bridge {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Content-Security-Policy", ancestors)
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")

		origin := r.Header.Get("Origin")

		page := origin != "" && slices.Contains(trusted, origin)
		if page {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Expose-Headers", mentionWarningHeader)
			w.Header().Add("Vary", "Origin")
		}

		who, err := client.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil || who.Node.IsTagged() || who.UserProfile.LoginName == "" {
			httpRPCError(w, status.Error(codes.Unauthenticated, "tailnet caller has no Tailscale user"))
			return
		}

		login = who.UserProfile.LoginName
		r = r.Clone(metadata.AppendToOutgoingContext(r.Context(), "rocketclaw-tailnet-login-bin", login, "rocketclaw-tailnet-name-bin", who.UserProfile.DisplayName))

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
	case "History", "ForkSession", "StageRevert", "MessageTree", "Handoff", "UpdateSession", "ListSessionEntries", "LoadSessionEntries", "DeleteSessionEntries", "DeleteSession", "ListQueue", "ReportTyping":
		required = "id"
	case "SearchMessages", "SearchSessions":
		required = "query"
	case "RunCronJob":
		required = "stem"
	case "MoveToBackground":
		required = "conversationId"
	case "SteerQueueItem", "PopQueueItem", "RemoveQueueItem", "EditQueueItem":
		required = "id itemId"
	case "DeleteBranch":
		required = "id messageId"
	case "SummarizeBranch":
		required = "id branchId"
	case "ReorderQueue":
		required = "id itemIds"
	case "StopBackgroundJob":
		required = "conversationId jobId"
	case "AnswerQuestion":
		required = "conversationId askId"
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
