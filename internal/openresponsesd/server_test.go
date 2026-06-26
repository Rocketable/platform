package openresponsesd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestHealthDoesNotExposeSecrets(t *testing.T) {
	srv := newServer(testConfig("openai_responses", "http://provider.test/v1"), http.DefaultClient)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	http.HandlerFunc(srv.serveHTTP).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "provider-key")
	require.NotContains(t, rec.Body.String(), "auth-token")
	require.Contains(t, rec.Body.String(), `"type":"openai_responses"`)
}

func TestResponsesRequiresBearerToken(t *testing.T) {
	srv := newServer(testConfig("openai_responses", "http://provider.test/v1"), http.DefaultClient)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")

	http.HandlerFunc(srv.serveHTTP).ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"invalid_auth_token"`)
}

func TestOpenAIResponsesForwardsCanonicalRequest(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/responses", r.URL.Path)
		require.Equal(t, "Bearer provider-key", r.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "gpt-5", body["model"])
		writeTestJSON(w, map[string]any{"id": "resp_1", "object": "response", "status": "completed", "model": "gpt-5", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "hi"}}}}})
	}))
	t.Cleanup(provider.Close)
	srv := newServer(testConfig("openai_responses", provider.URL+"/v1"), provider.Client())

	rec := postPath(t, srv, "/v1/responses", `{"model":"gpt-5","input":"hi"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"id":"resp_1"`)
	require.Contains(t, rec.Body.String(), `"object":"response"`)
}

func TestChatCompletionsTranslationAndContinuation(t *testing.T) {
	var requests []map[string]any
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		requests = append(requests, body)
		id := "chatcmpl_1"
		if len(requests) == 2 {
			id = "chatcmpl_2"
		}
		writeTestJSON(w, map[string]any{"id": id, "model": body["model"], "choices": []any{map[string]any{"message": map[string]any{"content": "answer"}}}})
	}))
	t.Cleanup(provider.Close)
	cfg := testConfig("openai_chat_completions", provider.URL+"/v1")
	cfg.ModelRoutes = []modelRoute{{Match: "local/*", Provider: "provider", StripPrefix: "local/"}}
	cfg.DefaultProvider = ""
	srv := newServer(cfg, provider.Client())

	first := postPath(t, srv, "/v1/responses", `{"model":"local/model-a","input":[{"type":"message","role":"user","content":"hello"}]}`)
	second := postPath(t, srv, "/v1/responses", `{"model":"local/model-a","previous_response_id":"chatcmpl_1","input":[{"type":"message","role":"user","content":"again"}]}`)

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Len(t, requests, 2)
	require.Equal(t, "model-a", requests[0]["model"])
	secondMessages := requests[1]["messages"].([]any)
	require.Len(t, secondMessages, 3)
	require.Equal(t, "hello", secondMessages[0].(map[string]any)["content"])
	require.Equal(t, "answer", secondMessages[1].(map[string]any)["content"])
	require.Equal(t, "again", secondMessages[2].(map[string]any)["content"])
}

func TestAnthropicMessagesTranslation(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/messages", r.URL.Path)
		require.Equal(t, "provider-key", r.Header.Get("x-api-key"))
		require.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "claude-sonnet", body["model"])
		require.Equal(t, []any{map[string]any{"text": "be brief", "type": "text"}}, body["system"])
		writeTestJSON(w, map[string]any{"id": "msg_1", "model": "claude-sonnet", "content": []any{map[string]any{"type": "text", "text": "ok"}}, "usage": map[string]any{"input_tokens": 2, "output_tokens": 1}})
	}))
	t.Cleanup(provider.Close)
	srv := newServer(testConfig("anthropic_messages", provider.URL), provider.Client())

	rec := postPath(t, srv, "/v1/responses", `{"model":"claude-sonnet","instructions":"be brief","input":"hello"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"id":"msg_1"`)
	require.Contains(t, rec.Body.String(), `"text":"ok"`)
}

func TestSSEEmitsSemanticEventsInOrder(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"id": "chatcmpl_sse", "model": "model", "choices": []any{map[string]any{"message": map[string]any{"content": "answer"}}}})
	}))
	t.Cleanup(provider.Close)
	srv := newServer(testConfig("openai_chat_completions", provider.URL+"/v1"), provider.Client())

	rec := postPath(t, srv, "/v1/responses", `{"model":"model","stream":true,"input":"hello"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "event: response.created")
	require.Contains(t, body, "event: response.in_progress")
	require.Contains(t, body, "event: response.output_item.done")
	require.Contains(t, body, "event: response.completed")
	require.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"))
	require.Contains(t, body, `"sequence_number":0`)
	require.Contains(t, body, `"sequence_number":3`)
}

func TestSSEProviderErrorStaysEventStream(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusBadGateway, errorEnvelope{Error: responseError{Type: "server_error", Code: "upstream_error", Message: "provider failed"}})
	}))
	t.Cleanup(provider.Close)
	srv := newServer(testConfig("openai_chat_completions", provider.URL+"/v1"), provider.Client())

	rec := postPath(t, srv, "/v1/responses", `{"model":"model","stream":true,"input":"hello"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	body := rec.Body.String()
	require.Contains(t, body, "event: error")
	require.Contains(t, body, `"type":"error"`)
	require.Contains(t, body, `"sequence_number":0`)
	require.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"))
}

func TestResponsesRejectUnknownRequestField(t *testing.T) {
	srv := newServer(testConfig("openai_chat_completions", "http://provider.test/v1"), http.DefaultClient)

	rec := postPath(t, srv, "/v1/responses", `{"model":"model","temperature":0.2,"input":"hello"}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"unsupported_request_field"`)
	require.Contains(t, rec.Body.String(), "temperature")
}

func TestResponsesRejectInvalidToolSchema(t *testing.T) {
	srv := newServer(testConfig("openai_responses", "http://provider.test/v1"), http.DefaultClient)

	rec := postPath(t, srv, "/v1/responses", `{"model":"model","input":"hello","tools":[{"type":"function","name":"lookup","parameters":[]}]}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"invalid_tool_schema"`)
}

func TestResponseRequestMarshalPreservesRawJSON(t *testing.T) {
	raw, err := responseRequest{
		Model:     "model",
		Input:     json.RawMessage(`{"n":9007199254740993}`),
		Text:      json.RawMessage(`{"format":{"threshold":9007199254740993}}`),
		Stream:    true,
		Reasoning: json.RawMessage(`null`),
	}.MarshalJSON()

	require.NoError(t, err)
	require.Contains(t, string(raw), `9007199254740993`)
	require.NotContains(t, string(raw), `9.007199254740992e+15`)
	require.NotContains(t, string(raw), `"stream"`)
	require.Contains(t, string(raw), `"reasoning":null`)

	_, err = responseRequest{Model: "model", Text: json.RawMessage(`{`)}.MarshalJSON()
	require.Error(t, err)
}

func TestTranslatedProvidersRejectUnsupportedMessageRoles(t *testing.T) {
	for _, providerType := range []string{"openai_chat_completions", "anthropic_messages"} {
		t.Run(providerType, func(t *testing.T) {
			srv := newServer(testConfig(providerType, "http://provider.test/v1"), http.DefaultClient)

			rec := postPath(t, srv, "/v1/responses", `{"model":"model","input":[{"type":"message","role":"bogus","content":"hello"}]}`)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), `"code":"unsupported_input_role"`)
		})
	}
}

func TestCompactFallbackRejectsUnansweredFunctionCall(t *testing.T) {
	srv := newServer(testConfig("openai_chat_completions", "http://provider.test/v1"), http.DefaultClient)
	rec := postPath(t, srv, "/v1/responses/compact", `{"model":"model","input":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}]}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"unanswered_function_call"`)
}

func TestOpenAIResponsesCompactUsesNativeEndpoint(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/responses/compact", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "gpt-5", body["model"])
		require.Equal(t, "resp_upstream", body["previous_response_id"])
		writeTestJSON(w, map[string]any{"id": "cmp_1", "object": "response.compaction", "output": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hello"}}}, map[string]any{"type": "compaction", "summary": "compact"}}, "usage": map[string]any{}})
	}))
	t.Cleanup(provider.Close)
	srv := newServer(testConfig("openai_responses", provider.URL+"/v1"), provider.Client())

	rec := postPath(t, srv, "/v1/responses/compact", `{"model":"gpt-5","previous_response_id":"resp_upstream"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"id":"cmp_1"`)
	require.Contains(t, rec.Body.String(), `"object":"response.compaction"`)
}

func TestCompactFallbackPreservesBoundariesAndAnsweredFunctionCalls(t *testing.T) {
	var content string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		messages := body["messages"].([]any)
		content = messages[0].(map[string]any)["content"].(string)
		writeTestJSON(w, map[string]any{"id": "chatcmpl_compact", "model": body["model"], "choices": []any{map[string]any{"message": map[string]any{"content": "summary"}}}})
	}))
	t.Cleanup(provider.Close)
	srv := newServer(testConfig("openai_chat_completions", provider.URL+"/v1"), provider.Client())

	rec := postPath(t, srv, "/v1/responses/compact", `{"model":"model","input":[{"type":"message","role":"user","content":"hello"},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"result"}]}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, content, "checkpoint item")
	require.Contains(t, content, "type: message")
	require.Contains(t, content, "type: function_call")
	require.Contains(t, content, "type: function_call_output")
	require.Contains(t, content, "output: result")
}

func TestCompactPreviousResponseDoesNotDuplicateHistory(t *testing.T) {
	var requests []map[string]any
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		requests = append(requests, body)
		content := "answer"
		id := "chatcmpl_prev"
		if len(requests) == 2 {
			content = "summary"
			id = "chatcmpl_compact"
		}
		writeTestJSON(w, map[string]any{"id": id, "model": body["model"], "choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	t.Cleanup(provider.Close)
	srv := newServer(testConfig("openai_chat_completions", provider.URL+"/v1"), provider.Client())

	first := postPath(t, srv, "/v1/responses", `{"model":"model","input":"hello"}`)
	compact := postPath(t, srv, "/v1/responses/compact", `{"model":"model","previous_response_id":"chatcmpl_prev"}`)

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, compact.Code)
	require.Len(t, requests, 2)
	messages := requests[1]["messages"].([]any)
	content := messages[0].(map[string]any)["content"].(string)
	require.Equal(t, 1, strings.Count(content, "hello"))
	require.Equal(t, 1, strings.Count(content, "answer"))
}

func TestWebSocketResponseCreate(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"id": "chatcmpl_ws", "model": "model", "choices": []any{map[string]any{"message": map[string]any{"content": "answer"}}}})
	}))
	t.Cleanup(provider.Close)
	srv := newServer(testConfig("openai_chat_completions", provider.URL+"/v1"), provider.Client())
	httpServer := httptest.NewServer(http.HandlerFunc(srv.serveHTTP))
	t.Cleanup(httpServer.Close)

	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/v1/responses"
	conn, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": []string{"Bearer auth-token"}})
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()

	require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.create", "model": "model", "input": "hello"}))

	events := readWebSocketTypes(t, conn, 4)
	require.Equal(t, []string{"response.created", "response.in_progress", "response.output_item.done", "response.completed"}, events)
}

func TestWebSocketRejectsNestedBodyAndTransportFields(t *testing.T) {
	srv := newServer(testConfig("openai_chat_completions", "http://provider.test/v1"), http.DefaultClient)
	httpServer := httptest.NewServer(http.HandlerFunc(srv.serveHTTP))
	t.Cleanup(httpServer.Close)

	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/v1/responses"
	conn, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": []string{"Bearer auth-token"}})
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))

	for _, field := range []string{"body", "stream", "stream_options", "background"} {
		require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.create", "model": "model", "input": "hello", field: true}))
		errEvent := readWebSocketError(t, conn)
		require.Equal(t, "unsupported_request_field", errEvent["code"])
		require.Contains(t, errEvent["message"], field)
	}
}

func TestWebSocketOpenAIResponsesUsesNativeUpstream(t *testing.T) {
	upgrader := websocket.Upgrader{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/responses", r.URL.Path)
		require.Equal(t, "Bearer provider-key", r.Header.Get("Authorization"))
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer func() { require.NoError(t, conn.Close()) }()
		var event map[string]any
		require.NoError(t, conn.ReadJSON(&event))
		require.Equal(t, "response.create", event["type"])
		require.Equal(t, "model-a", event["model"])
		require.Equal(t, false, event["generate"])
		require.Equal(t, "resp_previous", event["previous_response_id"])
		require.NotContains(t, event, "body")
		require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_ws", "object": "response", "status": "queued", "model": "model-a"}}))
		require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_ws", "object": "response", "status": "completed", "model": "model-a", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer"}}}}, "usage": map[string]any{}}}))
	}))
	t.Cleanup(upstream.Close)
	cfg := testConfig("openai_responses", upstream.URL+"/v1")
	cfg.ModelRoutes = []modelRoute{{Match: "local/*", Provider: "provider", StripPrefix: "local/"}}
	cfg.DefaultProvider = ""
	srv := newServer(cfg, upstream.Client())
	httpServer := httptest.NewServer(http.HandlerFunc(srv.serveHTTP))
	t.Cleanup(httpServer.Close)

	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/v1/responses"
	conn, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": []string{"Bearer auth-token"}})
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))

	require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.create", "model": "local/model-a", "store": false, "generate": false, "previous_response_id": "resp_previous", "input": "hello"}))

	events := readWebSocketTypes(t, conn, 2)
	require.Equal(t, []string{"response.created", "response.completed"}, events)
}

func TestWebSocketGenerateFalseRequiresNativeProvider(t *testing.T) {
	srv := newServer(testConfig("openai_chat_completions", "http://provider.test/v1"), http.DefaultClient)
	httpServer := httptest.NewServer(http.HandlerFunc(srv.serveHTTP))
	t.Cleanup(httpServer.Close)

	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/v1/responses"
	conn, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": []string{"Bearer auth-token"}})
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))

	require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.create", "model": "model", "generate": false, "input": "hello"}))
	errEvent := readWebSocketError(t, conn)
	require.Equal(t, "unsupported_request_field", errEvent["code"])
	require.Contains(t, errEvent["message"], "generate:false")
}

func TestWebSocketRejectsConcurrentResponseCreate(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		writeTestJSON(w, map[string]any{"id": "chatcmpl_ws", "model": "model", "choices": []any{map[string]any{"message": map[string]any{"content": "answer"}}}})
	}))
	t.Cleanup(provider.Close)
	srv := newServer(testConfig("openai_chat_completions", provider.URL+"/v1"), provider.Client())
	httpServer := httptest.NewServer(http.HandlerFunc(srv.serveHTTP))
	t.Cleanup(httpServer.Close)

	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/v1/responses"
	conn, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": []string{"Bearer auth-token"}})
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))

	require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.create", "model": "model", "input": "hello"}))
	<-entered
	require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.create", "model": "model", "input": "again"}))

	var event map[string]any
	require.NoError(t, conn.ReadJSON(&event))
	require.Equal(t, "error", event["type"])
	require.Equal(t, "response_in_flight", event["error"].(map[string]any)["code"])
	close(release)
}

func TestWebSocketFailedLocalContinuationEvictsStoreFalseState(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"id": "chatcmpl_local", "model": "model", "choices": []any{map[string]any{"message": map[string]any{"content": "answer"}}}})
	}))
	t.Cleanup(provider.Close)
	srv := newServer(testConfig("openai_chat_completions", provider.URL+"/v1"), provider.Client())
	httpServer := httptest.NewServer(http.HandlerFunc(srv.serveHTTP))
	t.Cleanup(httpServer.Close)

	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/v1/responses"
	conn, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": []string{"Bearer auth-token"}})
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))

	require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.create", "model": "model", "store": false, "input": "hello"}))
	readWebSocketTypes(t, conn, 4)
	require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.create", "model": "model", "previous_response_id": "chatcmpl_local", "input": "again", "tools": []any{map[string]any{"type": "function", "name": "lookup", "parameters": []any{}}}}))
	firstError := readWebSocketError(t, conn)
	require.Equal(t, "invalid_tool_schema", firstError["code"])
	require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.create", "model": "model", "previous_response_id": "chatcmpl_local", "input": "again"}))
	secondError := readWebSocketError(t, conn)
	require.Equal(t, "previous_response_not_found", secondError["code"])
	require.Contains(t, secondError["message"], "previous_response_id was not found")
}

func TestWebSocketNativeFailedLocalContinuationEvictsStoreFalseState(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var mu sync.Mutex
	var requests []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer func() { require.NoError(t, conn.Close()) }()
		var event map[string]any
		require.NoError(t, conn.ReadJSON(&event))
		mu.Lock()
		requests = append(requests, event)
		count := len(requests)
		mu.Unlock()
		switch count {
		case 1:
			require.Equal(t, false, event["store"])
			require.NotContains(t, event, "previous_response_id")
			require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_local", "object": "response", "status": "completed", "model": "model", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer"}}}}, "usage": map[string]any{}}}))
		case 2:
			require.NotContains(t, event, "previous_response_id")
			require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.failed", "response": map[string]any{"id": "resp_failed", "object": "response", "status": "failed", "model": "model"}}))
		case 3:
			require.Equal(t, "resp_local", event["previous_response_id"])
			require.NoError(t, conn.WriteJSON(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "code": "previous_response_not_found", "message": "previous_response_id was not found"}}))
		}
	}))
	t.Cleanup(upstream.Close)
	srv := newServer(testConfig("openai_responses", upstream.URL+"/v1"), upstream.Client())
	httpServer := httptest.NewServer(http.HandlerFunc(srv.serveHTTP))
	t.Cleanup(httpServer.Close)

	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/v1/responses"
	conn, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": []string{"Bearer auth-token"}})
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))

	require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.create", "model": "model", "store": false, "input": "hello"}))
	require.Equal(t, []string{"response.completed"}, readWebSocketTypes(t, conn, 1))
	require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.create", "model": "model", "previous_response_id": "resp_local", "input": "again"}))
	require.Equal(t, []string{"response.failed"}, readWebSocketTypes(t, conn, 1))
	require.NoError(t, conn.WriteJSON(map[string]any{"type": "response.create", "model": "model", "previous_response_id": "resp_local", "input": "again"}))
	errEvent := readWebSocketError(t, conn)
	require.Equal(t, "previous_response_not_found", errEvent["code"])
}

func readWebSocketTypes(t *testing.T, conn *websocket.Conn, count int) []string {
	t.Helper()
	events := make([]string, 0, count)
	for len(events) < count {
		var event map[string]any
		require.NoError(t, conn.ReadJSON(&event))
		events = append(events, event["type"].(string))
	}

	return events
}

func readWebSocketError(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	var event map[string]any
	require.NoError(t, conn.ReadJSON(&event))
	require.Equal(t, "error", event["type"])

	return event["error"].(map[string]any)
}

func postPath(t *testing.T, srv *server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer auth-token")
	http.HandlerFunc(srv.serveHTTP).ServeHTTP(rec, req)

	return rec
}

func testConfig(providerType, baseURL string) config {
	return config{
		Addr:            defaultAddr,
		Auth:            authConfig{Tokens: []string{"auth-token"}},
		DefaultProvider: "provider",
		Providers: map[string]providerConfig{
			"provider": {Type: providerType, APIKey: "provider-key", BaseURL: baseURL, AnthropicVersion: "2023-06-01"},
		},
		State: stateConfig{Mode: "memory"},
	}
}

func writeTestJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
