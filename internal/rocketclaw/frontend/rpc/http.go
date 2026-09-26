package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/internal/web"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// NewHTTPHandler bridges the browser API to the local Web service.
func NewHTTPHandler(connection *grpc.ClientConn) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", web.Handler())
	mux.Handle("GET /api/", http.NotFoundHandler())
	mux.Handle("POST /api/", http.NotFoundHandler())

	for method := range strings.FieldsSeq("Protocol Identity Prompt History ForkSession SearchMessages Handoff ListAgents CreateSession ListConfig ListSkills SettleSession UpdateSession ListCronJobs RunCronJob ListSessionEntries LoadSessionEntries DeleteSessionEntries ListQueue SteerQueueItem PopQueueItem RemoveQueueItem ReorderQueue") {
		descriptor := File_web_proto.Services().ByName("Web").Methods().ByName(protoreflect.Name(method))
		requestType, _ := protoregistry.GlobalTypes.FindMessageByName(descriptor.Input().FullName())
		responseType, _ := protoregistry.GlobalTypes.FindMessageByName(descriptor.Output().FullName())

		mux.HandleFunc("POST /api/"+method, func(w http.ResponseWriter, r *http.Request) {
			request, response := requestType.New().Interface(), responseType.New().Interface()

			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
			if err == nil {
				err = httpInput(body, request, method)
			}

			if err != nil {
				httpRPCError(w, status.Error(codes.InvalidArgument, err.Error()))
				return
			}

			if err := connection.Invoke(r.Context(), "/rpc.Web/"+method, request, response); err != nil {
				httpRPCError(w, err)
				return
			}

			body, err = (protojson.MarshalOptions{EmitDefaultValues: true}).Marshal(response)
			if err != nil {
				httpRPCError(w, err)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		})
	}

	for _, route := range []string{"GET /api/ListSessions", "GET /stream", "GET /api/DownloadAttachment", "POST /api/UploadAttachment"} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			method := strings.TrimPrefix(r.URL.Path, "/api/")

			var (
				request  proto.Message = &ListSessionsRequest{}
				response proto.Message = &ListSessionsResponse{}
			)

			switch method {
			case "/stream":
				method, request, response = "Join", &JoinRequest{Id: r.URL.Query().Get("id")}, &TranscriptEvent{}
			case "DownloadAttachment", "UploadAttachment":
				request = &Attachment{ConversationId: r.URL.Query().Get("conversationId"), Id: r.URL.Query().Get("id"), Name: r.URL.Query().Get("name")}
				response = &Attachment{}
			}

			required := ""

			switch method {
			case "Join":
				required = "id"
			case "UploadAttachment":
				required = "conversationId name"
			case "DownloadAttachment":
				required = "conversationId id"
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
							w.Header().Set("Content-Type", "application/json")
							_, _ = w.Write(body)

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

			httpEvents(w, stream, response, err)
		})
	}

	return http.NewCrossOriginProtection().Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		address, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil {
			httpRPCError(w, status.Error(codes.Unauthenticated, "invalid remote address"))
			return
		}

		ctx, cancel := context.WithCancel(metadata.NewOutgoingContext(r.Context(), metadata.Pairs("rocketclaw-principal", address.Addr().Unmap().String())))
		defer cancel()

		mux.ServeHTTP(w, r.WithContext(ctx))
	}))
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

func httpEvents(w http.ResponseWriter, stream grpc.ClientStream, response proto.Message, err error) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")

	for {
		if errors.Is(err, io.EOF) {
			_, _ = io.WriteString(w, "event: complete\ndata: {}\n\n")
			return
		}

		if err != nil {
			body, _ := json.Marshal(struct {
				Code    codes.Code `json:"code"`
				Message string     `json:"message"`
			}{status.Code(err), status.Convert(err).Message()})
			_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", body)

			return
		}

		body, errMarshal := (protojson.MarshalOptions{EmitDefaultValues: true}).Marshal(response)
		if errMarshal != nil {
			err = errMarshal
			continue
		}

		if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
			return
		}

		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}

		err = stream.RecvMsg(response)
	}
}

func httpInput(body []byte, request proto.Message, method string) error {
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
	case "History", "ForkSession", "Handoff", "SettleSession", "UpdateSession", "ListSessionEntries", "LoadSessionEntries", "DeleteSessionEntries", "ListQueue", "SteerQueueItem", "PopQueueItem", "RemoveQueueItem", "ReorderQueue":
		required = "id"
	case "SearchMessages":
		required = "query"
	case "RunCronJob":
		required = "stem"
	}

	switch method {
	case "SettleSession":
		required += " settled"
	case "SteerQueueItem", "PopQueueItem", "RemoveQueueItem":
		required += " itemId"
	case "ReorderQueue":
		required += " itemIds"
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
			var delivery string
			if err := json.Unmarshal(value, &delivery); err != nil || (delivery != "STEER" && delivery != "QUEUE" && delivery != "STASH") {
				return fmt.Errorf("invalid %s: expected STEER, QUEUE or STASH", name)
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
