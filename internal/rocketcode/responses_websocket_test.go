package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestWebsocketPersistsTextBeforeTerminal(t *testing.T) {
	for _, texts := range []struct{ done, final string }{{"partial", "p"}, {"different", "final"}, {"", "replaced"}, {"nonempty", ""}} {
		t.Run(texts.final, func(t *testing.T) { testWebsocketTextBeforeTerminal(t, texts.done, texts.final) })
	}
}

func testWebsocketTextBeforeTerminal(t *testing.T, done, final string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	release := make(chan struct{})
	defer close(release)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := testWebsocketUpgrader()

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()

		if _, _, err := conn.ReadMessage(); err != nil {
			t.Error(err)
			return
		}

		for _, event := range []string{
			`{"type":"response.created","response":{"id":"resp_live"}}`,
			`{"type":"response.reasoning_text.delta","item_id":"private","delta":"PRIVATE_REASONING"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"tool","delta":"PRIVATE_ARGS"}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","id":"tool","call_id":"call","name":"Execute","arguments":"{}"}}`,
			`{"type":"response.output_item.added","item":{"type":"reasoning","id":"reason","encrypted_content":"PRIVATE_ENCRYPTED"}}`,
			`{"type":"response.output_text.delta","item_id":"msg_live","content_index":0,"delta":"partial"}`,
			`{"type":"response.output_text.delta","item_id":"msg_live","content_index":0,"delta":" suffix"}`,
		} {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(event)); err != nil {
				t.Error(err)
				return
			}
		}

		select {
		case <-release:
		case <-ctx.Done():
			return
		}

		if err := conn.WriteJSON(responses.ResponseTextDoneEvent{Type: "response.output_text.done", ItemID: "msg_live", Text: done}); err != nil {
			t.Error(err)
			return
		}

		select {
		case <-release:
		case <-ctx.Done():
			return
		}

		terminal := struct {
			Type     string          `json:"type"`
			Response testSDKResponse `json:"response"`
		}{Type: "response.completed", Response: testSDKResponse{ID: "resp_live", Status: "completed", Output: []testSDKOutput{{Type: "message", ID: "msg_live", Role: "assistant", Phase: "final_answer", Content: []testSDKContent{{Type: "output_text", Text: final}}}}}}
		if err := conn.WriteJSON(terminal); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(websocketAPIBaseURL(server.URL)))
	loop := testLooper(mockResponses())
	loop.agent.Name, loop.DisplayModel = "main", "openai/gpt-5"
	tool := testLooperTool("Execute")
	tool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
		t.Error("provisional function call dispatched")
		return TextToolResult("unexpected"), nil
	}
	loop.Tools = map[string]looperTool{"Execute": tool}
	loop.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "Execute", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}
	loop.Client = newResponsesAPI(&client)
	sink := recordingJournal()
	persisted := make(chan []PublicProgress, 10)
	sink.SaveTraceFunc = func(_ context.Context, _ string, trace []json.RawMessage) error {
		persisted <- PublicProgressFromTrace(trace)
		return nil
	}
	loop.Journal = sink

	var (
		record  SessionEntry
		workers errgroup.Group
	)
	workers.Go(func() error {
		var err error

		record, _, _, err = loop.runTurn(ctx, make(chan ChatResponse, 10), nil, nil, nil, &PromptInput{Text: "hello", Role: PromptInputRoleUser})

		return err
	})

	defer func() {
		cancel()

		_ = workers.Wait()
	}()

	for _, text := range []string{"partial", "partial suffix"} {
		select {
		case progress := <-persisted:
			require.Len(t, progress, 1)
			require.Equal(t, text, progress[0].Text)
			require.Equal(t, PublicProgressWorking, progress[0].State)
			require.Equal(t, "msg_live/0", progress[0].ID)
			require.True(t, strings.HasSuffix(progress[0].ParentID, "/resp_live"))
			require.Equal(t, "main", progress[0].Agent)
			require.Equal(t, "openai/gpt-5", progress[0].Model)
			require.Len(t, turnSaves(t, sink), 1, "terminal replay cannot exist while held")
		case <-ctx.Done():
			t.Fatal("no public checkpoint while terminal response was held")
		}
	}

	release <- struct{}{}

	select {
	case progress := <-persisted:
		require.Len(t, progress, 1)
		require.Equal(t, done, progress[0].Text, "done is an authoritative replacement, including empty")
		require.Equal(t, PublicProgressWorking, progress[0].State, "text done is not request completion")
		require.Len(t, turnSaves(t, sink), 1)
	case <-ctx.Done():
		t.Fatal("done text was not persisted before terminal release")
	}

	release <- struct{}{}

	require.NoError(t, workers.Wait())
	require.Equal(t, "resp_live", record.ResponseID)
	require.Contains(t, string(record.ReplayInput[len(record.ReplayInput)-1]), `"phase":"final_answer"`)

	progress := PublicProgressFromTrace(turnSaves(t, sink)[1].Trace)
	require.Len(t, progress, 1)
	require.Equal(t, final, progress[0].Text)
	require.Equal(t, PublicProgressCompleted, progress[0].State)

	_ = loop.Client.(responseServiceClient).doer.conn.Close()
}

func TestWebsocketStorageFailureDoesNotRetryOrReuseUnreadEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	sent, dropped := make(chan struct{}), make(chan struct{})
	creates := make(chan struct{}, 10)

	var mu sync.Mutex

	connections := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := testWebsocketUpgrader()

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()

		mu.Lock()
		connections++
		first := connections == 1
		mu.Unlock()

		if _, _, err := conn.ReadMessage(); err != nil {
			t.Error(err)
			return
		}

		creates <- struct{}{}

		if !first {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_fresh","status":"completed","output":[]}}`)); err != nil {
				t.Error(err)
			}

			return
		}

		for _, event := range []string{
			`{"type":"response.created","response":{"id":"resp_abandoned"}}`,
			`{"type":"response.output_text.delta","item_id":"msg","delta":"partial"}`,
			`{"type":"response.completed","response":{"id":"resp_unread","status":"completed","output":[]}}`,
		} {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(event)); err != nil {
				t.Error(err)
				return
			}
		}

		close(sent)

		if _, _, err := conn.ReadMessage(); err == nil {
			t.Error("abandoned connection received a second create")
		}

		close(dropped)
	}))
	defer server.Close()

	client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(websocketAPIBaseURL(server.URL)))
	api := newResponsesAPI(&client)
	loop := testLooper(api)
	sink := recordingJournal()
	errPersist := contextLengthExceededError() // A wrapped provider-looking cause must still be fatal local storage.
	sink.SaveTraceFunc = func(context.Context, string, []json.RawMessage) error {
		<-sent // Ensure the terminal event is already unread on the socket.
		return errPersist
	}
	loop.Journal = sink
	input := make(chan PromptInput, 1)

	output := make(chan ChatResponse, 10)
	input <- testPromptInput(PromptInputRoleUser, "hello", output)

	close(input)
	err := loop.Loop(ctx, input, emptySession(), discardSession, make(chan os.Signal, 1))
	require.ErrorIs(t, err, errPersist)
	_, ok := errors.AsType[progressPersistenceError](err)
	require.True(t, ok)
	require.Len(t, creates, 1, "SDK and looper must not issue a second create")
	require.Empty(t, collectResponses(output), "storage errors are not public tool or provider diagnostic text")
	require.Nil(t, api.doer.conn)

	select {
	case <-dropped:
	case <-ctx.Done():
		t.Fatal("unread socket was not dropped")
	}

	resp, err := api.New(ctx, &responses.ResponseNewParams{Model: "gpt-5"}, inertResponseObserver{})
	require.NoError(t, err)
	require.Equal(t, "resp_fresh", resp.ID, "later request cannot consume abandoned terminal events")
	require.Len(t, creates, 2)

	_ = api.doer.conn.Close()
}

func TestWebsocketObservationTerminalAndReceiveBoundaries(t *testing.T) {
	for _, boundary := range []string{"failed", "incomplete", "empty", "eof", "cancel", "storage_cancel", "retry"} {
		t.Run(boundary, func(t *testing.T) {
			watchdog, stop := context.WithTimeout(t.Context(), 3*time.Second)
			defer stop()

			ctx, cancel := context.WithCancel(t.Context()) // No deadline: cancellation must wake the socket read.
			defer cancel()

			var mu sync.Mutex

			attempt := 0

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upgrader := testWebsocketUpgrader()

				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.Close() }()

				if _, _, err := conn.ReadMessage(); err != nil {
					t.Error(err)
					return
				}

				mu.Lock()
				attempt++
				n := attempt
				mu.Unlock()

				id := fmt.Sprintf("resp_%d", n)
				for _, event := range []string{
					fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, id),
					fmt.Sprintf(`{"type":"response.output_text.delta","item_id":"msg","delta":%q}`, id),
				} {
					if err := conn.WriteMessage(websocket.TextMessage, []byte(event)); err != nil {
						t.Error(err)
						return
					}
				}

				if boundary == "cancel" || boundary == "storage_cancel" {
					_, _, _ = conn.ReadMessage() // Client cancellation must close this connection.
					return
				}

				if boundary == "eof" || boundary == "retry" && n == 1 {
					return
				}

				status := boundary
				if boundary == "empty" || boundary == "retry" {
					status = "completed"
				}

				terminal := fmt.Sprintf(`{"type":"response.%s","response":{"id":%q,"status":%q,"output":[]}}`, status, id, status)
				if err := conn.WriteMessage(websocket.TextMessage, []byte(terminal)); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()

			opts := []option.RequestOption{option.WithAPIKey("test-key"), option.WithBaseURL(websocketAPIBaseURL(server.URL))}
			if boundary != "retry" {
				opts = append(opts, option.WithMaxRetries(0))
			}

			client := openai.NewClient(opts...)
			api := newResponsesAPI(&client)
			sink := recordingJournal()
			errPersist := errors.New("storage failed during cancellation")
			persisted := make(chan struct{}, 10)
			sink.SaveTraceFunc = func(context.Context, string, []json.RawMessage) error {
				persisted <- struct{}{}

				if boundary == "storage_cancel" {
					cancel()
					return errPersist
				}

				return nil
			}
			owner := turnObservations{journal: sink, turnID: "turn"}
			observer := &responseObservations{owner: &owner, agent: "main", model: "gpt-5"}

			var (
				resp    *responses.Response
				workers errgroup.Group
			)
			workers.Go(func() error {
				var err error

				resp, err = api.New(ctx, &responses.ResponseNewParams{Model: "gpt-5"}, observer)

				return err
			})

			defer func() { cancel(); _ = workers.Wait() }()

			select {
			case <-persisted:
			case <-watchdog.Done():
				t.Fatal("public text was not persisted")
			}

			if boundary == "cancel" {
				cancel()
			}

			err := workers.Wait()
			writes := sink.SaveTraceCalls()
			progress := PublicProgressFromTrace(writes[len(writes)-1].Trace)

			switch boundary {
			case "storage_cancel":
				require.ErrorIs(t, err, errPersist)
				_, ok := errors.AsType[progressPersistenceError](err)
				require.True(t, ok, "SDK cancellation must not mask the fatal local error")
				require.Nil(t, api.doer.conn)
			case "cancel":
				require.ErrorIs(t, err, context.Canceled)
				require.Nil(t, api.doer.conn)
				require.Equal(t, "resp_1", progress[0].Text)
			case "eof":
				require.ErrorContains(t, err, "read responses websocket event")
				require.Nil(t, api.doer.conn)
				require.Equal(t, "resp_1", progress[0].Text)
			case "failed", "incomplete":
				require.NoError(t, err)
				require.Equal(t, responses.ResponseStatus(boundary), resp.Status)
				require.Equal(t, "resp_1", progress[0].Text, "non-success retains last public observation")

				state := PublicProgressFailed
				if boundary == "incomplete" {
					state = PublicProgressStopped
				}

				require.Equal(t, state, progress[0].State)
			case "empty":
				require.NoError(t, err)
				require.Empty(t, progress, "empty authoritative output removes the partial")
			case "retry":
				require.NoError(t, err)
				require.Equal(t, "resp_2", resp.ID)
				require.Len(t, writes, 4)
				require.Empty(t, PublicProgressFromTrace(writes[1].Trace), "remove abandoned attempt before publishing retry")
				require.Equal(t, "resp_2", PublicProgressFromTrace(writes[2].Trace)[0].Text)
				require.Empty(t, progress)
			}

			if api.doer.conn != nil {
				_ = api.doer.conn.Close()
			}
		})
	}
}

func TestNewResponsesAPIUsesWebsocketWhenURLIsWebsocket(t *testing.T) {
	var create json.RawMessage

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("request path = %q; want /v1/responses", r.URL.Path)
			http.NotFound(w, r)

			return
		}

		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q; want Bearer test-key", got)
		}

		upgrader := testWebsocketUpgrader()

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}

		defer func() { _ = conn.Close() }()

		_, create, err = conn.ReadMessage()
		if err != nil {
			t.Errorf("read create: %v", err)
			return
		}

		if err := conn.WriteJSON(map[string]any{
			"type":            "response.completed",
			"sequence_number": 1,
			"response":        map[string]any{"id": "resp_ws", "status": "completed", "output": []any{}},
		}); err != nil {
			t.Errorf("write completed: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(websocketAPIBaseURL(server.URL)))
	api := newResponsesAPI(&client)
	resp, err := api.New(t.Context(), &responses.ResponseNewParams{Model: "gpt-5.5"}, inertResponseObserver{})
	require.NoError(t, err)
	require.Equal(t, "resp_ws", resp.ID)

	var event map[string]any
	require.NoError(t, json.Unmarshal(create, &event))
	require.Equal(t, "response.create", event["type"])
	require.Equal(t, "gpt-5.5", event["model"])
}

func TestNewResponsesAPIUsesClientHTTPClientWhenURLIsHTTPS(t *testing.T) {
	// openai-go sends authenticated HTTP through a dedicated loopback transport
	// and ignores custom clients. HTTPS is what still uses the configured client.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer real-token" {
			t.Errorf("Authorization = %q; want Bearer real-token", got)
		}

		w.Header().Set("Content-Type", "application/json")

		if _, err := w.Write([]byte(`{"id":"resp_auth","status":"completed","output":[]}`)); err != nil {
			t.Errorf("write response body: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	transport := &authRewriteTransport{base: server.Client().Transport}
	client := openai.NewClient(
		option.WithAPIKey("dummy-key"),
		option.WithBaseURL(server.URL+"/v1"),
		option.WithHTTPClient(&http.Client{Transport: transport}),
	)
	api := newResponsesAPI(&client)
	resp, err := api.New(t.Context(), &responses.ResponseNewParams{Model: "gpt-5.5"}, inertResponseObserver{})
	require.NoError(t, err)
	require.Equal(t, "resp_auth", resp.ID)
	require.True(t, transport.used)
}

func TestNewResponsesAPIKeepsHTTPWhenURLIsNotWebsocket(t *testing.T) {
	var sawWebsocket bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			sawWebsocket = true

			http.Error(w, "websocket not supported", http.StatusBadRequest)

			return
		}

		if r.Method != http.MethodPost {
			t.Errorf("method = %q; want POST", r.Method)
		}

		if r.URL.Path != "/v1/responses" {
			t.Errorf("request path = %q; want /v1/responses", r.URL.Path)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}

		if strings.Contains(string(body), `"type":"response.create"`) {
			t.Errorf("http body included websocket create type: %s", body)
		}

		w.Header().Set("Content-Type", "application/json")

		if _, err := w.Write([]byte(`{"id":"resp_http","status":"completed","output":[]}`)); err != nil {
			t.Errorf("write response body: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(server.URL+"/v1"), option.WithUnsafeAllowHTTP())
	api := newResponsesAPI(&client)
	resp, err := api.New(t.Context(), &responses.ResponseNewParams{Model: "gpt-5.5"}, inertResponseObserver{})
	require.NoError(t, err)
	require.Equal(t, "resp_http", resp.ID)
	require.False(t, sawWebsocket)
}

func TestNewResponsesAPICompactsOverHTTPWhenURLIsWebsocket(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q; want POST", r.Method)
		}

		if r.URL.Path != "/v1/responses/compact" {
			t.Errorf("request path = %q; want /v1/responses/compact", r.URL.Path)
		}

		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			t.Error("compact used websocket upgrade")
		}

		w.Header().Set("Content-Type", "application/json")

		if _, err := w.Write([]byte(`{"id":"cmp_1","object":"response.compaction","created_at":1,"output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)); err != nil {
			t.Errorf("write compact body: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(websocketAPIBaseURL(server.URL)))
	api := newResponsesAPI(&client)
	resp, err := api.Compact(t.Context(), &responses.ResponseCompactParams{Model: "gpt-5.5"})
	require.NoError(t, err)
	require.Equal(t, "cmp_1", resp.ID)
}

func TestNewResponsesAPIMapsWebsocketError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := testWebsocketUpgrader()

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}

		defer func() { _ = conn.Close() }()

		if _, _, err := conn.ReadMessage(); err != nil {
			t.Errorf("read create: %v", err)
			return
		}

		if err := conn.WriteJSON(map[string]any{
			"type":   "error",
			"status": http.StatusTooManyRequests,
			"error": map[string]any{
				"type":    "too_many_requests",
				"code":    "too_many_requests",
				"message": "rate limited",
			},
		}); err != nil {
			t.Errorf("write error: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(websocketAPIBaseURL(server.URL)))
	api := newResponsesAPI(&client)
	_, err := api.New(t.Context(), &responses.ResponseNewParams{Model: "gpt-5.5"}, inertResponseObserver{})
	require.Error(t, err)
	errAPI, ok := errors.AsType[*openai.Error](err)
	require.True(t, ok)
	require.Equal(t, http.StatusTooManyRequests, errAPI.StatusCode)
	require.Equal(t, "too_many_requests", errAPI.Code)
}

type authRewriteTransport struct {
	base http.RoundTripper
	used bool
}

func (t *authRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.used = true
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer real-token")

	resp, errRound := t.base.RoundTrip(cloned)
	if errRound != nil {
		return nil, fmt.Errorf("round trip rewritten auth request: %w", errRound)
	}

	return resp, nil
}

func testWebsocketUpgrader() websocket.Upgrader {
	return websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
}

func websocketAPIBaseURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http") + "/v1"
}
