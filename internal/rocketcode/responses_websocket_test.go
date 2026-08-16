package rocketcode

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

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
	resp, err := api.New(t.Context(), &responses.ResponseNewParams{Model: "gpt-5.5"})
	require.NoError(t, err)
	require.Equal(t, "resp_ws", resp.ID)

	var event map[string]any
	require.NoError(t, json.Unmarshal(create, &event))
	require.Equal(t, "response.create", event["type"])
	require.Equal(t, "gpt-5.5", event["model"])
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

	client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(server.URL+"/v1"))
	api := newResponsesAPI(&client)
	resp, err := api.New(t.Context(), &responses.ResponseNewParams{Model: "gpt-5.5"})
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
	_, err := api.New(t.Context(), &responses.ResponseNewParams{Model: "gpt-5.5"})
	require.Error(t, err)
	errAPI, ok := errors.AsType[*openai.Error](err)
	require.True(t, ok)
	require.Equal(t, http.StatusTooManyRequests, errAPI.StatusCode)
	require.Equal(t, "too_many_requests", errAPI.Code)
}

func testWebsocketUpgrader() websocket.Upgrader {
	return websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
}

func websocketAPIBaseURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http") + "/v1"
}
