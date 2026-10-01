package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestHTTPReadableSnapshotOverReceiveLimit(t *testing.T) {
	text := strings.Repeat("full tool output 世界 \"quoted\"\n", 200000)
	require.Greater(t, len(text), 4<<20)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	output, err := json.Marshal(struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output string `json:"output"`
	}{"function_call_output", "large", text})
	require.NoError(t, err)

	const id = "slack-thread:C1:1.1"

	checkpoint := &rocketcode.ActiveTurnCheckpoint{TurnID: "storage", ConversationKey: id, ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"large","name":"execute","arguments":"{\"code\":\"full script\"}"}`), output}}
	require.NoError(t, sessions.UpsertActiveTurn(t.Context(), checkpoint, map[string]string{"execution_turn_id": "large-live"}))
	runtime := &backend.Runtime{Sessions: sessions}
	core := &mockBackend{SubscribeFunc: runtime.Subscribe, ListConversationsFunc: runtime.ListConversations, QueueItemsFunc: func(string) ([]protocol.ThreadQueueItem, error) { return nil, nil }}
	channels := &mockChannels{
		ChannelAgentChoicesFunc:        func(context.Context, string) ([]string, error) { return []string{"main"}, nil },
		SidebarChannelAgentChoicesFunc: func(context.Context, string) (string, []string, error) { return "Large result", []string{"main"}, nil },
	}
	jobs := &mockCronJobs{JobsFunc: func() ([]cronfrontend.Job, error) { return nil, nil }}
	connection := liveTestConnection(t, New(core, sessions, cfg, channels, jobs))
	server := startHTTPTestServer(t, connection)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/stream?id="+id, http.NoBody)
	require.NoError(t, err)
	response, err := server.Client().Do(request)

	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()

	require.Equal(t, http.StatusOK, response.StatusCode)

	var assembled strings.Builder

	index := uint32(0)

	var snapshot TranscriptEvent

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)

	for scanner.Scan() {
		line := scanner.Text()
		require.NotEqual(t, "event: error", line)

		if !strings.HasPrefix(line, "data: ") || line == "data: {}" {
			continue
		}

		var frame TranscriptEvent
		require.NoError(t, protojson.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame))
		require.Contains(t, []string{"1", "2"}, frame.SnapshotId)
		require.Equal(t, index, frame.FragmentIndex)
		index++

		assembled.WriteString(frame.Fragment)

		if frame.SnapshotEnd {
			require.NoError(t, protojson.Unmarshal([]byte(assembled.String()), &snapshot))

			if !snapshot.Seed {
				break
			}

			assembled.Reset()

			index = 0
		}
	}

	require.NoError(t, scanner.Err())
	require.Greater(t, index, uint32(1))
	require.Equal(t, "large-live", snapshot.TurnId)
	require.Len(t, snapshot.Items, 2)
	require.Equal(t, "large", snapshot.Items[1].ToolCallId)
	require.Equal(t, text, snapshot.Items[1].Text, "a single full readable result survives projection, gRPC, and SSE without clipping")
	_, err = sessions.AppendEntryID(t.Context(), id, &rocketcode.SessionEntry{Version: 1, Type: "turn", ReplayInput: checkpoint.ReplayInput})
	require.NoError(t, err)
	require.NoError(t, sessions.ClearActiveTurn(t.Context(), checkpoint.TurnID))
	historyRequest, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/History", strings.NewReader(`{"id":"`+id+`"}`))
	require.NoError(t, err)
	history, err := server.Client().Do(historyRequest)
	require.NoError(t, err)
	body, err := io.ReadAll(history.Body)
	require.NoError(t, err)
	require.NoError(t, history.Body.Close())
	require.Equal(t, http.StatusOK, history.StatusCode, string(body))

	var saved HistoryResponse
	require.NoError(t, protojson.Unmarshal(body, &saved))
	require.Equal(t, text, saved.Messages[1].Text, "Web's opening History prerequisite must also preserve full saved results")

	if os.Getenv("ROCKETCLAW_SLACK_BROWSER") == "1" {
		browser := exec.CommandContext(t.Context(), "bun", "--eval", `
const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE);
const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true, args: ["--no-sandbox"] });
try {
  const page = await browser.newPage();
  await page.goto(process.env.ROCKETCLAW_TEST_HTTP_URL + "/s/" + Buffer.from("slack-thread:C1:1.1").toString("base64url"));
  for (let i = 0; i < 2; i++) {
    await page.waitForFunction(() => Array.from(document.querySelectorAll("main pre")).some(node => node.textContent.endsWith('full tool output 世界 "quoted"\n'.repeat(200000))), null, { timeout: 20000 });
    if (i === 0) await page.reload();
  }
} finally { await browser.close(); }
`)

		browser.Env = append(os.Environ(), "ROCKETCLAW_TEST_HTTP_URL="+server.URL)
		output, err := browser.CombinedOutput()
		require.NoError(t, err, string(output))
	}
}

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
