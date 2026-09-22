package rpc

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func startHTTPTestServer(t *testing.T, connection *grpc.ClientConn) *httptest.Server {
	t.Helper()

	server := httptest.NewUnstartedServer(NewHTTPHandler(connection))
	require.NoError(t, server.Listener.Close())

	listener, err := net.Listen("tcp", ":0")
	require.NoError(t, err)

	server.Listener = listener
	server.Start()
	server.URL = "http://127.0.0.1:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	t.Cleanup(server.Close)

	return server
}

func TestHTTPBoundary(t *testing.T) {
	connection, err := grpc.NewClient("unix:"+testSocketPath(t), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	handler := NewHTTPHandler(connection)

	for _, tc := range []struct{ method, body, message string }{
		{"RunCronJob", `{}`, "stem is required"},
		{"SettleSession", `{"id":"chat"}`, "settled is required"},
		{"SteerQueueItem", `{"id":"chat"}`, "itemId is required"},
		{"PopQueueItem", `{"id":"chat"}`, "itemId is required"},
		{"PopQueueItem", `{"itemId":"held"}`, "id is required"},
		{"RemoveQueueItem", `{"id":"chat"}`, "itemId is required"},
		{"ReorderQueue", `{"id":"chat"}`, "itemIds is required"},
		{"ListSkills", `{"agent":""}`, "agent must not be empty"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/"+tc.method, strings.NewReader(tc.body)))
		require.Equal(t, http.StatusBadRequest, response.Code, tc.method)
		require.Contains(t, response.Body.String(), tc.message)
	}

	requestInvalidAddress := httptest.NewRequest(http.MethodPost, "/api/Identity", strings.NewReader(`{}`))
	requestInvalidAddress.RemoteAddr = "not-an-address"
	responseInvalidAddress := httptest.NewRecorder()
	handler.ServeHTTP(responseInvalidAddress, requestInvalidAddress)
	require.Equal(t, http.StatusUnauthorized, responseInvalidAddress.Code)

	for _, path := range []string{"/api/Prompt", "/api/UploadAttachment?conversationId=x&name=file.txt"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"id":"x","text":"run"}`))
		request.Header.Set("Content-Type", "text/plain")
		request.Header.Set("Origin", "https://untrusted.example")

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, http.StatusForbidden, response.Code, path)
	}

	for _, body := range []string{`{}`, `{`, `null`, `[]`, `{"id":"x","text":null}`, `{"id":"x","text":[]}`, `{"id":"x","text":"","unknown":true}`, `{"id":"x","text":"","delivery":7}`, `{"id":"x","text":"","delivery":"later"}`, `{"id":"x","text":"` + strings.Repeat("x", 4<<20) + `"}`} {
		request := httptest.NewRequest(http.MethodPost, "/api/Prompt", strings.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, http.StatusBadRequest, response.Code)
		require.Contains(t, response.Body.String(), `"code":3`)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/AnswerQuestion", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusNotFound, response.Code)
}

func TestHTTPStreams(t *testing.T) {
	for _, test := range []struct {
		name     string
		code     codes.Code
		httpCode int
	}{
		{name: "unary"}, {name: "stash"}, {name: "pop"}, {name: "complete"}, {name: "post-terminal-error"}, {name: "wirecut"}, {name: "oversized"}, {name: "invalid-text"},
		{name: "cancel"}, {name: "idle-cancel"}, {name: "upload"}, {name: "download"}, {name: "inline"}, {name: "forced-download"}, {name: "truncated"},
		{"auth", codes.Unauthenticated, http.StatusUnauthorized},
		{"permission", codes.PermissionDenied, http.StatusForbidden},
		{"not-found", codes.NotFound, http.StatusNotFound},
		{"conflict", codes.AlreadyExists, http.StatusConflict},
		{"aborted", codes.Aborted, http.StatusConflict},
		{"quota", codes.ResourceExhausted, http.StatusTooManyRequests},
		{"unavailable", codes.Unavailable, http.StatusServiceUnavailable},
		{"deadline", codes.DeadlineExceeded, http.StatusGatewayTimeout},
		{"invalid", codes.InvalidArgument, http.StatusBadRequest},
		{"internal", codes.Internal, http.StatusInternalServerError},
		{"unimplemented", codes.Unimplemented, http.StatusNotImplemented},
		{"upload-rejected", codes.PermissionDenied, http.StatusForbidden},
		{"upload-metadata", codes.InvalidArgument, http.StatusBadRequest},
		{"upload-final-error", codes.DataLoss, http.StatusInternalServerError},
	} {
		scenario := test.name
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			data := bytes.Repeat([]byte("original\x00bytes\xff"), 400000)
			canceled := make(chan struct{})
			transport := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
				require.Equal(t, []string{"127.0.0.1"}, metadata.ValueFromIncomingContext(stream.Context(), "rocketclaw-principal"))

				if test.code != codes.OK && scenario != "upload-metadata" && scenario != "upload-final-error" {
					return status.Error(test.code, "denied")
				}

				switch scenario {
				case "invalid-text":
					return nil
				case "pop":
					request := &QueueItemRequest{}
					require.NoError(t, stream.RecvMsg(request))
					require.Equal(t, "visible", request.Id)
					require.Equal(t, "held", request.ItemId)

					return stream.SendMsg(&QueueItemResponse{})
				case "unary", "stash":
					request := &PromptRequest{}
					require.NoError(t, stream.RecvMsg(request))
					require.Equal(t, "visible", request.Id)

					if scenario == "stash" {
						require.Equal(t, PromptDelivery_STASH, request.Delivery)
					} else {
						require.Equal(t, PromptDelivery_QUEUE, request.Delivery)
					}

					return stream.SendMsg(&PromptResponse{})
				case "upload", "upload-metadata", "upload-final-error":
					first := &Attachment{}
					require.NoError(t, stream.RecvMsg(first))
					require.Equal(t, "visible", first.ConversationId)
					require.Equal(t, "original.bin", first.Name)

					if scenario == "upload-metadata" {
						return status.Error(test.code, "denied")
					}

					var received []byte

					for {
						frame := &Attachment{}

						err := stream.RecvMsg(frame)
						if err == io.EOF {
							break
						}

						require.NoError(t, err)
						require.LessOrEqual(t, len(frame.Data), attachmentChunkBytes)
						require.Empty(t, frame.ConversationId)
						received = append(received, frame.Data...)
					}

					require.Equal(t, data, received)

					if err := stream.SendMsg(&Attachment{Id: "receipt", Size: int64(len(received))}); err != nil {
						return fmt.Errorf("send upload receipt: %w", err)
					}

					if scenario == "upload-final-error" {
						return status.Error(test.code, "denied")
					}

					return nil
				case "download", "inline", "forced-download", "truncated":
					request := &Attachment{}
					require.NoError(t, stream.RecvMsg(request))
					require.Equal(t, "visible", request.ConversationId)

					mimeType := "image/svg+xml"
					if scenario == "inline" || scenario == "forced-download" {
						mimeType = "image/png"
					}

					for offset := 0; offset < len(data); offset += attachmentChunkBytes {
						if err := stream.SendMsg(&Attachment{ConversationId: "private-producer", Name: "résumé.svg", MimeType: mimeType, Size: int64(len(data)), Data: data[offset:min(offset+attachmentChunkBytes, len(data))]}); err != nil {
							return fmt.Errorf("send download chunk: %w", err)
						}

						if scenario == "truncated" {
							return status.Error(codes.DataLoss, "broken file")
						}
					}

					return nil
				case "cancel", "idle-cancel":
					require.NoError(t, stream.RecvMsg(&JoinRequest{}))

					if scenario == "idle-cancel" {
						cancel()
					} else {
						require.NoError(t, stream.SendMsg(&TranscriptEvent{Text: "ready"}))
					}

					<-stream.Context().Done()
					close(canceled)

					return stream.Context().Err()
				default:
					require.NoError(t, stream.RecvMsg(&ListSessionsRequest{}))

					if scenario == "oversized" {
						return stream.SendMsg(&ListSessionsResponse{Sessions: []*Session{{Title: strings.Repeat("x", len(data))}}})
					}

					require.NoError(t, stream.SendMsg(&ListSessionsResponse{Sessions: []*Session{{Title: strings.Repeat("x", 100000)}}}))
					require.NoError(t, stream.SendMsg(&ListSessionsResponse{SummariesComplete: true}))

					if scenario == "wirecut" {
						<-stream.Context().Done()
						return stream.Context().Err()
					}

					if scenario == "post-terminal-error" {
						return status.Error(codes.Unavailable, "after terminal")
					}

					return nil
				}
			}))
			listener, err := Listen(testSocketPath(t))
			require.NoError(t, err)

			var serving errgroup.Group
			serving.Go(func() error { return transport.Serve(listener) })
			t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

			connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, connection.Close()) })

			if scenario == "invalid-text" {
				stream, err := connection.NewStream(metadata.NewOutgoingContext(ctx, metadata.Pairs("rocketclaw-principal", "127.0.0.1")), &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/ListSessions")
				require.NoError(t, err)

				response := httptest.NewRecorder()
				httpEvents(response, stream, &ListSessionsResponse{Sessions: []*Session{{Title: "\xff"}}}, nil)
				require.Equal(t, "text/event-stream", response.Header().Get("Content-Type"))
				require.Contains(t, response.Body.String(), "event: error\n")
				require.Contains(t, response.Body.String(), "invalid UTF-8")
				require.NotContains(t, response.Body.String(), "event: complete")

				return
			}

			server := httptest.NewServer(NewHTTPHandler(connection))
			t.Cleanup(server.Close)

			path, method := "/api/ListSessions", http.MethodGet

			switch scenario {
			case "cancel", "idle-cancel", "auth":
				path = "/stream?id=visible"
			case "upload", "upload-rejected", "upload-metadata", "upload-final-error":
				path, method = "/api/UploadAttachment?conversationId=visible&name=original.bin", http.MethodPost
			case "download", "inline", "forced-download", "truncated":
				path = "/api/DownloadAttachment?conversationId=visible&id=file"
				if scenario == "forced-download" {
					path += "&download=1"
				}
			}

			var requestBody io.Reader = http.NoBody
			if strings.HasPrefix(scenario, "upload") {
				requestBody = bytes.NewReader(data)
			}

			if scenario == "unary" || (test.code != codes.OK && scenario != "auth" && !strings.HasPrefix(scenario, "upload")) {
				path, method = "/api/Prompt", http.MethodPost
				requestBody = strings.NewReader(`{"id":"visible","text":"hello","delivery":"QUEUE"}`)
			}

			if scenario == "stash" {
				path, method = "/api/Prompt", http.MethodPost
				requestBody = strings.NewReader(`{"id":"visible","text":"$stop","delivery":"STASH"}`)
			}

			if scenario == "pop" {
				path, method = "/api/PopQueueItem", http.MethodPost
				requestBody = strings.NewReader(`{"id":"visible","itemId":"held"}`)
			}

			request, err := http.NewRequestWithContext(ctx, method, server.URL+path, requestBody)
			require.NoError(t, err)
			request.Header.Set("X-Forwarded-For", "192.0.2.99")
			request.Header.Set("Rocketclaw-Principal", "192.0.2.99")

			response, err := server.Client().Do(request)
			if scenario == "idle-cancel" {
				require.ErrorIs(t, err, context.Canceled)

				select {
				case <-canceled:
				case <-t.Context().Done():
					t.Fatal("idle gRPC stream was not canceled")
				}

				return
			}

			require.NoError(t, err)

			if scenario == "wirecut" {
				reader := bufio.NewReader(response.Body)
				for {
					line, err := reader.ReadString('\n')
					require.NoError(t, err)

					if strings.Contains(line, `"summariesComplete":true`) {
						break
					}
				}

				transport.Stop()

				body, err := io.ReadAll(reader)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Contains(t, string(body), "event: error\n")
				require.NotContains(t, string(body), "event: complete")

				return
			}

			if scenario == "cancel" {
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.NoError(t, response.Body.Close())

				select {
				case <-canceled:
				case <-t.Context().Done():
					t.Fatal("gRPC stream was not canceled")
				}

				return
			}

			body, err := io.ReadAll(response.Body)
			require.NoError(t, response.Body.Close())

			if scenario == "truncated" {
				require.Error(t, err)
				require.Less(t, len(body), len(data))

				return
			}

			require.NoError(t, err)

			if test.code != codes.OK {
				require.Equal(t, test.httpCode, response.StatusCode)
				require.Equal(t, "application/json", response.Header.Get("Content-Type"))
				require.JSONEq(t, fmt.Sprintf(`{"code":%d,"message":"denied"}`, test.code), string(body))

				return
			}

			switch scenario {
			case "pop":
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.JSONEq(t, `{}`, string(body))
			case "unary", "stash":
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.JSONEq(t, `{"privateText":""}`, string(body))
			case "oversized":
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.Contains(t, string(body), `"code":8`)
				require.Contains(t, string(body), "event: error\n")
				require.NotContains(t, string(body), "event: complete")
			case "upload":
				require.Contains(t, string(body), `"id":"receipt"`)
			case "download", "inline", "forced-download":
				require.Equal(t, data, body)
				require.Equal(t, "nosniff", response.Header.Get("X-Content-Type-Options"))

				if scenario == "inline" {
					require.Contains(t, response.Header.Get("Content-Disposition"), "inline;")
				} else {
					require.Contains(t, response.Header.Get("Content-Disposition"), "attachment;")
				}

				require.Contains(t, response.Header.Get("Content-Disposition"), "filename*=utf-8''")
			default:
				require.Contains(t, string(body), strings.Repeat("x", 100000))
				require.Contains(t, string(body), `"sessions":[]`)
				require.Contains(t, string(body), `"summariesComplete":true`)

				if scenario == "complete" {
					require.True(t, strings.HasSuffix(string(body), "event: complete\ndata: {}\n\n"))
				} else {
					require.Contains(t, string(body), "event: error\n")
					require.NotContains(t, string(body), "event: complete")
				}
			}
		})
	}
}
