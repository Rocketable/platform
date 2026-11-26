package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestHTTPEventFramingAllocationBudget(t *testing.T) {
	if os.Getenv("ROCKETCLAW_TEST_EVENT_FRAMING_ALLOCATIONS") != "1" {
		t.Parallel()

		executable := filepath.Join(t.TempDir(), "rpc.test")
		build := exec.CommandContext(t.Context(), "go", "test", "-c", "-race=false", "-cover=false", "-o", executable, ".")

		build.Env = append(os.Environ(), "GOGC=100", "GOMEMLIMIT=off")
		output, err := build.CombinedOutput()
		require.NoError(t, err, "%s", output)

		cmd := exec.CommandContext(t.Context(), executable, "-test.run=^"+t.Name()+"$", "-test.cpu="+strconv.Itoa(runtime.GOMAXPROCS(0)), "-test.benchtime=32x")

		cmd.Env = append(os.Environ(), "ROCKETCLAW_TEST_EVENT_FRAMING_ALLOCATIONS=1", "GOGC=100", "GOMEMLIMIT=off")
		output, err = cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)

		return
	}

	result := testing.Benchmark(BenchmarkHTTPEventFraming)
	require.Positive(t, result.N)
	require.Less(t, result.AllocedBytesPerOp(), int64(3<<20), "N=%d T=%s MemBytes=%d MemAllocs=%d GOMAXPROCS=%d", result.N, result.T, result.MemBytes, result.MemAllocs, runtime.GOMAXPROCS(0))
}

func BenchmarkHTTPEventFraming(b *testing.B) {
	message := &ListSessionsResponse{Sessions: []*Session{{Title: strings.Repeat("example", 32768), Tags: []string{"quote\" 日本語\n"}}}}
	transport := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		for range 7 {
			if err := stream.SendMsg(message); err != nil {
				return fmt.Errorf("send session frame: %w", err)
			}
		}

		return nil
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(b, err)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	b.Cleanup(func() { transport.Stop(); require.NoError(b, serving.Wait()) })

	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, connection.Close()) })

	request := httptest.NewRequest(http.MethodGet, "/api/ListSessions", http.NoBody)
	body, err := protojson.Marshal(message)
	require.NoError(b, err)

	want := []byte(strings.Repeat("data: "+string(body)+"\n\n", 8) + "event: complete\ndata: {}\n\n")
	b.SetBytes(int64(len(want)))
	b.ReportAllocs()

	var writers sync.Pool

	response := httptest.NewRecorder()
	response.Body.Grow(len(want))

	for b.Loop() {
		stream, err := connection.NewStream(b.Context(), &grpc.StreamDesc{ServerStreams: true}, "/example/Events")
		require.NoError(b, err)
		require.NoError(b, stream.CloseSend())

		response.Body.Reset()
		httpEvents(response, request, stream, proto.Clone(message), nil, &writers)

		if !bytes.Equal(want, response.Body.Bytes()) {
			b.Fatal("stream event framing changed")
		}
	}
}

func startHTTPTestServer(t *testing.T, connection *grpc.ClientConn) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(signedIn("alice", NewHTTPHandler(connection, new(config.LockedConfig), "")))
	t.Cleanup(server.Close)

	return server
}

func TestHTTPBoundary(t *testing.T) {
	t.Parallel()

	connection, err := grpc.NewClient("unix:"+testSocketPath(t), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	handler := NewHTTPHandler(connection, new(config.LockedConfig), "")

	for _, tc := range []struct{ method, body, message string }{
		{"RunRoutine", `{}`, "stem is required"},
		{"SteerQueueItem", `{}`, "id is required"},
		{"SteerQueueItem", `{"id":"chat"}`, "itemId is required"},
		{"PopQueueItem", `{"id":"chat"}`, "itemId is required"},
		{"PopQueueItem", `{"itemId":"held"}`, "id is required"},
		{"RemoveQueueItem", `{"id":"chat"}`, "itemId is required"},
		{"ReorderQueue", `{"id":"chat"}`, "itemIds is required"},
		{"ReorderQueue", `{"itemIds":[]}`, "id is required"},
		{"SearchSessions", `{"messages":true}`, "query is required"},
		{"ListSkills", `{"agent":""}`, "agent must not be empty"},
		{"MoveToBackground", `{}`, "conversationId is required"},
		{"MoveToBackground", `{"conversationId":"chat","jobId":"turn-1/call/a"}`, "invalid field jobId"},
		{"StopBackgroundJob", `{"conversationId":"chat"}`, "jobId is required"},
		{"StopBackgroundJob", `{"jobId":"job"}`, "conversationId is required"},
		{"StopBackgroundJob", `{"conversationId":"chat","jobId":"turn-1/call/a","reason":"user"}`, "invalid field reason"},
		{"AnswerQuestion", `{"askId":"turn-1/call/q"}`, "conversationId is required"},
		{"AnswerQuestion", `{"conversationId":"chat"}`, "askId is required"},
		{"ReportTyping", `{"typing":true}`, "id is required"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/"+tc.method, strings.NewReader(tc.body)))
		require.Equal(t, http.StatusBadRequest, response.Code, tc.method)
		require.JSONEq(t, fmt.Sprintf(`{"code":3,"message":%q}`, tc.message), response.Body.String(), tc.method)
	}

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

	for _, tc := range []struct{ method, path, message string }{
		{http.MethodGet, "/stream", "id is required"},
		{http.MethodGet, "/typing", "id is required"},
		{http.MethodPost, "/api/UploadAttachment?conversationId=x", "name is required"},
		{http.MethodGet, "/api/DownloadAttachment?id=file", "conversationId is required"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, http.NoBody))
		require.Equal(t, http.StatusBadRequest, response.Code, tc.path)
		require.JSONEq(t, fmt.Sprintf(`{"code":3,"message":%q}`, tc.message), response.Body.String())
	}
}

func TestHTTPFindPullRequests(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	New(&mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning}, nil, config.NewLockedConfig(&config.Config{}), &mockChannels{}, &mockRoutines{}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() {
		transport.Stop()
		require.NoError(t, serving.Wait())
	})

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	handler := NewHTTPHandler(connection, new(config.LockedConfig), "")
	body := `{"runs":[
		{"command":"GIT_DIR=$(jj git root) gh pr list --head a --json number; GIT_DIR=$(jj git root) gh pr create --head a --fill","output":"https://github.com/o/r/pull/7"},
		{"command":"gh pr view 5","output":"url:\thttps://github.com/o/r/pull/5"}
	]}`

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/FindPullRequests", strings.NewReader(body)))
	require.Equal(t, http.StatusUnauthorized, response.Code)

	response = httptest.NewRecorder()
	signedIn("alice", handler).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/FindPullRequests", strings.NewReader(body)))
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"created":["https://github.com/o/r/pull/7"],"mentioned":["https://github.com/o/r/pull/5"]}`, response.Body.String())
}

func TestHTTPRefusesOtherSites(t *testing.T) {
	t.Parallel()

	connection, err := grpc.NewClient("unix:"+testSocketPath(t), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	handler := NewHTTPHandler(connection, new(config.LockedConfig), "")

	const download = "/api/DownloadAttachment?id=file"

	fetched := func(site, mode, dest, origin string) http.Header {
		header := http.Header{"Sec-Fetch-Site": {site}, "Sec-Fetch-Mode": {mode}, "Sec-Fetch-Dest": {dest}}
		if origin != "" {
			header.Set("Origin", origin)
		}

		return header
	}

	for _, tt := range []struct {
		name, method, path string
		header             http.Header
		code               int
	}{
		{"cross-site fetch", http.MethodGet, "/api/ListSessions", fetched("cross-site", "cors", "empty", "https://evil.example"), http.StatusForbidden},
		{"cross-site event stream", http.MethodGet, "/stream?id=chat", fetched("cross-site", "cors", "empty", "https://evil.example"), http.StatusForbidden},
		{"cross-site image", http.MethodGet, download, fetched("cross-site", "no-cors", "image", ""), http.StatusForbidden},
		{"same-site image", http.MethodGet, download, fetched("same-site", "no-cors", "image", ""), http.StatusForbidden},
		{"opaque origin fetch", http.MethodGet, download, fetched("cross-site", "cors", "empty", "null"), http.StatusForbidden},
		{"other origin without fetch metadata", http.MethodGet, download, http.Header{"Origin": {"https://evil.example"}}, http.StatusForbidden},
		{"cross-site form post", http.MethodPost, "/api/Prompt", fetched("cross-site", "navigate", "document", "https://evil.example"), http.StatusForbidden},
		{"same-origin image", http.MethodGet, download, fetched("same-origin", "no-cors", "image", ""), http.StatusBadRequest},
		{"same origin without fetch metadata", http.MethodGet, download, http.Header{"Origin": {"http://example.com"}}, http.StatusBadRequest},
		{"typed address", http.MethodGet, download, fetched("none", "navigate", "document", ""), http.StatusBadRequest},
		{"cross-site link", http.MethodGet, download, fetched("cross-site", "navigate", "document", ""), http.StatusBadRequest},
		{"no fetch metadata", http.MethodGet, download, http.Header{}, http.StatusBadRequest},
		{"hosted page asset", http.MethodGet, "/pages-raw/main/app.js", fetched("cross-site", "no-cors", "script", ""), http.StatusServiceUnavailable},
	} {
		request := httptest.NewRequest(tt.method, tt.path, http.NoBody)
		request.Header = tt.header

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, tt.code, response.Code, tt.name)
	}
}

func TestHTTPReload(t *testing.T) {
	t.Parallel()

	core := &mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning, ReloadFunc: func(reason string) (string, error) {
		require.Equal(t, "updated assets", reason)
		return "rocketclaw runtime assets reloaded", nil
	}}
	server := New(core, nil, config.NewLockedConfig(&config.Config{}), &mockChannels{}, &mockRoutines{})
	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	server.Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() {
		transport.Stop()
		require.NoError(t, serving.Wait())
	})

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	handler := NewHTTPHandler(connection, new(config.LockedConfig), "")

	for _, tc := range []struct {
		handler http.Handler
		code    int
	}{
		{handler, http.StatusUnauthorized},
		{signedIn("alice", handler), http.StatusOK},
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/Reload", strings.NewReader(`{"reason":"updated assets"}`))
		response := httptest.NewRecorder()
		tc.handler.ServeHTTP(response, request)
		require.Equal(t, tc.code, response.Code)

		if tc.code == http.StatusOK {
			require.JSONEq(t, `{"message":"rocketclaw runtime assets reloaded"}`, response.Body.String())
		} else {
			require.Empty(t, core.ReloadCalls())
		}
	}

	_, err = invoke[ReloadResponse](t.Context(), connection, "Reload", &ReloadRequest{Reason: "updated assets"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	require.Len(t, core.ReloadCalls(), 1)
	core.ReloadFunc = func(string) (string, error) { return "", errors.New("invalid staged assets") }
	request := httptest.NewRequest(http.MethodPost, "/api/Reload", strings.NewReader(`{"reason":"updated assets"}`))
	response := httptest.NewRecorder()
	signedIn("alice", handler).ServeHTTP(response, request)
	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.JSONEq(t, `{"code":2,"message":"web reload: invalid staged assets"}`, response.Body.String())
	require.Empty(t, core.RunTurnCalls())
}

func TestHTTPRPCMCP(t *testing.T) {
	t.Parallel()

	core := &mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning, ReloadFunc: func(reason string) (string, error) {
		if reason == "broken" {
			return "", errors.New("invalid staged assets")
		}

		return "reloaded for " + reason, nil
	}}
	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	New(core, nil, config.NewLockedConfig(&config.Config{}), &mockChannels{}, &mockRoutines{}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() {
		transport.Stop()
		require.NoError(t, serving.Wait())
	})

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	handler := NewHTTPHandler(connection, new(config.LockedConfig), "")
	connect := func(login string) (*mcp.ClientSession, string) {
		server := httptest.NewServer(signedIn(login, handler))
		t.Cleanup(server.Close)

		session, err := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "test"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL + "/rpc_mcp", DisableStandaloneSSE: true}, nil)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, session.Close()) })

		return session, server.URL
	}
	call := func(session *mcp.ClientSession, tool string, arguments any) (string, bool) {
		t.Helper()

		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: arguments})
		require.NoError(t, err)
		require.Len(t, result.Content, 1)

		return result.Content[0].(*mcp.TextContent).Text, result.IsError
	}

	alice, aliceURL := connect("alice")
	bob, _ := connect("bob")

	listed, err := alice.ListTools(t.Context(), nil)
	require.NoError(t, err)

	type inputSchema struct {
		Type                 json.RawMessage         `json:"type"`
		Description          string                  `json:"description"`
		Enum                 []string                `json:"enum"`
		Items                *inputSchema            `json:"items"`
		Properties           map[string]*inputSchema `json:"properties"`
		AdditionalProperties *inputSchema            `json:"additionalProperties"`
	}

	names, schemas, descriptions, readOnly := make([]string, 0, len(listed.Tools)), map[string]inputSchema{}, map[string]string{}, map[string]bool{}

	for _, tool := range listed.Tools {
		raw, err := json.Marshal(tool.InputSchema)
		require.NoError(t, err)

		var schema inputSchema
		require.NoError(t, json.Unmarshal(raw, &schema))
		require.NotEmpty(t, tool.Description, "%s needs a comment in web.proto", tool.Name)

		names, schemas[tool.Name], descriptions[tool.Name], readOnly[tool.Name] = append(names, tool.Name), schema, tool.Description, tool.Annotations.ReadOnlyHint
	}

	require.ElementsMatch(t, append(strings.Fields(webUnaryMethods+" "+editorMethods), "ListSessions", "DownloadAttachment", "UploadAttachment"), names)
	require.Contains(t, descriptions["Prompt"], "read it with History")
	require.Contains(t, schemas["Prompt"].Properties["messageId"].Description, "resend the same one when retrying")
	require.Equal(t, []string{"STEER", "QUEUE", "STASH"}, schemas["Prompt"].Properties["delivery"].Enum)
	require.JSONEq(t, `"string"`, string(schemas["Prompt"].Properties["attachmentIds"].Items.Type))

	op := schemas["ApplyAgentDraftOp"].Properties["op"]
	require.JSONEq(t, `["integer", "string"]`, string(op.Properties["revision"].Type))
	require.JSONEq(t, `"integer"`, string(op.Properties["splices"].Items.Properties["remove"].Type))
	require.JSONEq(t, `"string"`, string(op.Properties["externalMcp"].Properties["metadata"].AdditionalProperties.Type))
	require.Equal(t, map[string]bool{"History": true, "Prompt": false}, map[string]bool{"History": readOnly["History"], "Prompt": readOnly["Prompt"]})

	recursive, err := json.Marshal(messageSchema((&descriptorpb.DescriptorProto{}).ProtoReflect().Descriptor(), map[protoreflect.FullName]bool{}))
	require.NoError(t, err)

	var descriptorSchema inputSchema
	require.NoError(t, json.Unmarshal(recursive, &descriptorSchema))
	require.Empty(t, descriptorSchema.Properties["nestedType"].Items.Properties, "a message that contains itself stops at the repeat")

	webMethods := File_web_proto.Services().ByName("Web").Methods()

	for _, method := range names {
		fields, want := webMethods.ByName(protoreflect.Name(method)).Input().Fields(), []string{"messages"}
		if !webMethods.ByName(protoreflect.Name(method)).IsStreamingClient() {
			want = make([]string, fields.Len())
			for i := range want {
				want[i] = fields.Get(i).JSONName()
			}
		}

		require.ElementsMatch(t, want, slices.Collect(maps.Keys(schemas[method].Properties)), method)
	}

	text, failed := call(alice, "Reload", &ReloadRequest{Reason: "updated assets"})
	require.False(t, failed, text)
	require.JSONEq(t, `{"message":"reloaded for updated assets"}`, text)
	require.Len(t, core.ReloadCalls(), 1)

	for login, session := range map[string]*mcp.ClientSession{"alice": alice, "bob": bob} {
		text, failed := call(session, "Identity", nil)
		require.False(t, failed, text)

		var identity IdentityResponse
		require.NoError(t, protojson.Unmarshal([]byte(text), &identity))
		require.Equal(t, login, identity.Username)
	}

	text, failed = call(alice, "RunRoutine", &RunRoutineRequest{})
	require.True(t, failed)
	require.Equal(t, "rpc error: code = InvalidArgument desc = stem is required", text)

	text, failed = call(alice, "Reload", &ReloadRequest{Reason: "broken"})
	require.True(t, failed)
	require.Equal(t, "rpc error: code = Unknown desc = web reload: invalid staged assets", text)

	response, err := http.Get(aliceURL + "/rpc_mcp")
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusMethodNotAllowed, response.StatusCode)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, aliceURL+"/rpc_mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"Identity"}}`))
	require.NoError(t, err)

	request.Header = http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"}}

	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)

	without, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Contains(t, string(without), `\"username\":\"alice\"`)
}

func TestHTTPStreams(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		code     codes.Code
		httpCode int
	}{
		{name: "unary"}, {name: "large-history"}, {name: "stash"}, {name: "pop"}, {name: "complete"}, {name: "post-terminal-error"}, {name: "wirecut"}, {name: "oversized"}, {name: "invalid-text"},
		{name: "cancel"}, {name: "idle-cancel"}, {name: "typing"}, {name: "upload"}, {name: "upload-read-error"}, {name: "download"}, {name: "invalid-mime"}, {name: "inline"}, {name: "forced-download"}, {name: "truncated"},
		{"auth", codes.Unauthenticated, http.StatusUnauthorized},
		{"permission", codes.PermissionDenied, http.StatusForbidden},
		{"not-found", codes.NotFound, http.StatusNotFound},
		{"conflict", codes.AlreadyExists, http.StatusConflict},
		{"quota", codes.ResourceExhausted, http.StatusTooManyRequests},
		{"unavailable", codes.Unavailable, http.StatusServiceUnavailable},
		{"deadline", codes.DeadlineExceeded, http.StatusGatewayTimeout},
		{"invalid", codes.InvalidArgument, http.StatusBadRequest},
		{"internal", codes.Internal, http.StatusInternalServerError},
		{"unimplemented", codes.Unimplemented, http.StatusNotImplemented},
		{"upload-rejected", codes.PermissionDenied, http.StatusForbidden},
		{"upload-final-error", codes.DataLoss, http.StatusInternalServerError},
	} {
		scenario := test.name
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			data := bytes.Repeat([]byte("original\x00bytes\xff"), 400000)
			canceled := make(chan struct{})
			transport := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
				require.Equal(t, []string{"alice"}, metadata.ValueFromIncomingContext(stream.Context(), "rocketclaw-tailnet-login-bin"))

				if test.code != codes.OK && scenario != "upload-final-error" {
					return status.Error(test.code, "denied")
				}

				switch scenario {
				case "upload-read-error":
					return stream.RecvMsg(&Attachment{})
				case "invalid-text":
					return nil
				case "large-history":
					request := &HistoryRequest{}
					require.NoError(t, stream.RecvMsg(request))
					require.Equal(t, "visible", request.Id)

					return stream.SendMsg(&HistoryResponse{Messages: []*TranscriptEvent{{Text: strings.Repeat("x", 5<<20)}}})
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
				case "upload", "upload-final-error":
					first := &Attachment{}
					require.NoError(t, stream.RecvMsg(first))
					require.Equal(t, "visible", first.ConversationId)
					require.Equal(t, "original.bin", first.Name)

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
				case "download", "invalid-mime", "inline", "forced-download", "truncated":
					request := &Attachment{}
					require.NoError(t, stream.RecvMsg(request))
					require.Equal(t, "visible", request.ConversationId)

					mimeType := "image/svg+xml"
					if scenario == "invalid-mime" {
						mimeType = "invalid mime"
					}

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
				case "typing":
					method, _ := grpc.MethodFromServerStream(stream)
					require.Equal(t, "/rpc.Web/WatchTyping", method)

					request := &WatchTypingRequest{}
					require.NoError(t, stream.RecvMsg(request))
					require.Equal(t, "visible", request.Id)
					require.NoError(t, stream.SendMsg(&TypingEvent{}))

					return stream.SendMsg(&TypingEvent{Principal: "Ana", Typing: true})
				default:
					require.NoError(t, stream.RecvMsg(&ListSessionsRequest{}))

					if scenario == "oversized" {
						return stream.SendMsg(&ListSessionsResponse{Sessions: []*Session{{Title: strings.Repeat("x", len(data))}}})
					}

					require.NoError(t, stream.SendMsg(&ListSessionsResponse{Sessions: []*Session{{Title: strings.Repeat("x", 100000), Tags: []string{"customer", "quote\" 日本語"}}}}))
					require.NoError(t, stream.SendMsg(&ListSessionsResponse{Owner: "second frame"}))

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
			listener, err := net.Listen("unix", testSocketPath(t))
			require.NoError(t, err)

			var serving errgroup.Group
			serving.Go(func() error { return transport.Serve(listener) })
			t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

			connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, connection.Close()) })

			if scenario == "invalid-text" {
				stream, err := connection.NewStream(metadata.NewOutgoingContext(ctx, tailnetUser("alice")), &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/ListSessions")
				require.NoError(t, err)

				response := httptest.NewRecorder()
				httpEvents(response, httptest.NewRequest(http.MethodGet, "/api/ListSessions", http.NoBody), stream, &ListSessionsResponse{Sessions: []*Session{{Title: "\xff"}}}, nil, new(sync.Pool))
				require.Equal(t, "text/event-stream", response.Header().Get("Content-Type"))
				require.Contains(t, response.Body.String(), "event: error\n")
				require.Contains(t, response.Body.String(), "invalid UTF-8")
				require.NotContains(t, response.Body.String(), "event: complete")

				return
			}

			if scenario == "upload-read-error" {
				request := httptest.NewRequest(http.MethodPost, "/api/UploadAttachment?conversationId=visible&name=original.bin", iotest.ErrReader(errors.New("broken body")))
				response := httptest.NewRecorder()
				signedIn("alice", NewHTTPHandler(connection, new(config.LockedConfig), "")).ServeHTTP(response, request)
				require.Equal(t, http.StatusBadRequest, response.Code)
				require.JSONEq(t, `{"code":3,"message":"read upload body: rpc error: code = InvalidArgument desc = broken body"}`, response.Body.String())

				return
			}

			server := httptest.NewServer(signedIn("alice", NewHTTPHandler(connection, new(config.LockedConfig), "")))
			t.Cleanup(server.Close)

			path, method := "/api/ListSessions", http.MethodGet

			switch scenario {
			case "cancel", "idle-cancel", "auth":
				path = "/stream?id=visible"
			case "typing":
				path = "/typing?id=visible"
			case "upload", "upload-rejected", "upload-final-error":
				path, method = "/api/UploadAttachment?conversationId=visible&name=original.bin", http.MethodPost
			case "download", "invalid-mime", "inline", "forced-download", "truncated":
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

			if scenario == "large-history" {
				path, method = "/api/History", http.MethodPost
				requestBody = strings.NewReader(`{"id":"visible","limit":1}`)
			}

			if scenario == "stash" {
				path, method = "/api/Prompt", http.MethodPost
				requestBody = strings.NewReader(`{"id":"visible","text":"/stop","delivery":"STASH"}`)
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

					if strings.Contains(line, `"owner":"second frame"`) {
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
			case "large-history":
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.True(t, response.Uncompressed, "a large history crosses the network compressed")

				var history HistoryResponse
				require.NoError(t, protojson.Unmarshal(body, &history))
				require.Len(t, history.Messages, 1)
				require.Equal(t, strings.Repeat("x", 5<<20), history.Messages[0].Text)
			case "pop":
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.JSONEq(t, `{}`, string(body))
			case "typing":
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.Equal(t, "text/event-stream", response.Header.Get("Content-Type"))

				frames, complete := strings.CutSuffix(string(body), "event: complete\ndata: {}\n\n")
				require.True(t, complete, "%s", body)

				var events []string
				for frame := range strings.SplitSeq(strings.TrimSuffix(frames, "\n\n"), "\n\n") {
					events = append(events, strings.TrimPrefix(frame, "data: "))
				}

				require.Len(t, events, 2)
				require.JSONEq(t, `{"principal":"","typing":false}`, events[0])
				require.JSONEq(t, `{"principal":"Ana","typing":true}`, events[1])
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
			case "download", "invalid-mime", "inline", "forced-download":
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.Equal(t, data, body)
				require.Equal(t, "nosniff", response.Header.Get("X-Content-Type-Options"))

				if scenario == "invalid-mime" {
					require.Equal(t, "application/octet-stream", response.Header.Get("Content-Type"))
				}

				if scenario == "inline" {
					require.Contains(t, response.Header.Get("Content-Disposition"), "inline;")
				} else {
					require.Contains(t, response.Header.Get("Content-Disposition"), "attachment;")
				}

				require.Contains(t, response.Header.Get("Content-Disposition"), "filename*=utf-8''")
			default:
				require.True(t, response.Uncompressed, "the session list crosses the network compressed")
				require.Contains(t, string(body), strings.Repeat("x", 100000))
				require.NotContains(t, string(body), `"upstreamSuccess"`, "a listing leaves out default values")
				first, _, _ := strings.Cut(string(body), "\n")

				var batch struct {
					Sessions []struct{ Tags []string }
				}
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(first, "data: ")), &batch))
				require.Equal(t, []string{"customer", "quote\" 日本語"}, batch.Sessions[0].Tags)
				require.Contains(t, string(body), `"owner":"second frame"`)

				if scenario == "complete" {
					require.True(t, strings.HasSuffix(string(body), "event: complete\ndata: {}\n\n"))

					for range 2 {
						next, err := server.Client().Do(request.Clone(ctx))
						require.NoError(t, err)
						unpacked, err := io.ReadAll(next.Body)
						require.NoError(t, err)
						require.NoError(t, next.Body.Close())
						require.True(t, next.Uncompressed)
						require.Equal(t, body, unpacked)
					}
				} else {
					require.Contains(t, string(body), "event: error\n")
					require.NotContains(t, string(body), "event: complete")
				}
			}
		})
	}
}
