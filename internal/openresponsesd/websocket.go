package openresponsesd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type wsEvent struct {
	Type string `json:"type"`
}

func (s *server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	out := make(chan any, 16)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case value := <-out:
				_ = conn.WriteJSON(value)
			case <-done:
				return
			}
		}
	}()
	defer close(done)

	var mu sync.Mutex
	inFlight := false
	var local memoryEntry
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var event wsEvent
		if err := json.Unmarshal(data, &event); err != nil {
			writeWSError(out, done, "invalid_json", "malformed JSON websocket event")
			continue
		}
		if event.Type != "response.create" {
			writeWSError(out, done, "invalid_event", "websocket turn must start with response.create")
			continue
		}
		mu.Lock()
		busy := inFlight
		if !busy {
			inFlight = true
		}
		localCopy := local
		mu.Unlock()
		if busy {
			writeWSError(out, done, "response_in_flight", "websocket connection already has a response in flight")
			continue
		}

		req, err := unmarshalWebSocketRequest(data)
		if err != nil {
			mu.Lock()
			inFlight = false
			mu.Unlock()
			writeWSErrorForErr(out, done, err)
			continue
		}

		go func() {
			if err := validateToolSchemas(req.Tools); err != nil {
				finishWebSocketError(out, done, &mu, &inFlight, &local, localCopy, req, err)
				return
			}
			providerName, model, err := s.selectProvider(req.Model)
			if err != nil {
				finishWebSocketError(out, done, &mu, &inFlight, &local, localCopy, req, err)
				return
			}
			req.Model = model
			provider := s.cfg.Providers[providerName]
			if provider.Type == "openai_responses" {
				previousResponseID := strings.TrimSpace(req.PreviousResponseID)
				if previousResponseID != "" {
					entry, ok := s.state.get(previousResponseID)
					if !ok && localCopy.id == previousResponseID {
						entry = localCopy
						ok = true
					}
					if ok {
						history := mergeInput(entry.input, entry.output)
						req.Input = mergeInput(history, req.Input)
						req.PreviousResponseID = ""
					}
				}
				upstream, err := dialOpenAIResponsesWebSocket(r.Context(), provider)
				if err != nil {
					finishWebSocketError(out, done, &mu, &inFlight, &local, localCopy, req, err)
					return
				}
				defer func() { _ = upstream.Close() }()
				entry, ok, err := s.forwardOpenAIResponsesWebSocket(r.Context(), upstream, out, done, req)
				if err != nil {
					finishWebSocketError(out, done, &mu, &inFlight, &local, localCopy, req, err)
					return
				}
				if ok {
					localCopy = entry
				} else if previousResponseID == localCopy.id {
					localCopy = memoryEntry{}
				}
				mu.Lock()
				local = localCopy
				inFlight = false
				mu.Unlock()
				return
			}

			if req.Generate != nil && !*req.Generate {
				finishWebSocketError(out, done, &mu, &inFlight, &local, localCopy, req, &adapterError{status: http.StatusBadRequest, code: "unsupported_request_field", message: "websocket generate:false requires a native websocket provider"})
				return
			}
			resp, err := s.createResponse(r.Context(), req, &localCopy)
			if err != nil {
				finishWebSocketError(out, done, &mu, &inFlight, &local, localCopy, req, err)
				return
			}

			writeWSJSON(out, done, responseEvent{Type: "response.created", Response: resp.summary("queued")})
			writeWSJSON(out, done, responseEvent{Type: "response.in_progress", Response: resp.summary("in_progress")})
			for i, item := range resp.Output {
				writeWSJSON(out, done, responseEvent{Type: "response.output_item.done", OutputIndex: new(i), Item: &item})
			}
			writeWSJSON(out, done, responseEvent{Type: "response.completed", Response: resp.object()})
			mu.Lock()
			local = localCopy
			inFlight = false
			mu.Unlock()
		}()
	}
}

func unmarshalWebSocketRequest(data json.RawMessage) (responseRequest, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return responseRequest{}, &requestError{code: "invalid_json", message: "malformed JSON websocket event"}
	}
	for _, name := range []string{"stream", "stream_options", "background", "body"} {
		if _, ok := fields[name]; ok {
			return responseRequest{}, &requestError{code: "unsupported_request_field", message: "unsupported request field " + name}
		}
	}
	delete(fields, "type")
	raw, err := json.Marshal(fields)
	if err != nil {
		return responseRequest{}, err
	}

	return unmarshalResponseRequest(raw)
}

func dialOpenAIResponsesWebSocket(ctx context.Context, provider providerConfig) (*websocket.Conn, error) {
	urlWebSocket, err := openAIResponsesWebSocketURL(provider.BaseURL)
	if err != nil {
		return nil, err
	}
	header := http.Header{"Authorization": []string{"Bearer " + provider.APIKey}}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, urlWebSocket, header)
	if err != nil {
		if resp != nil {
			return nil, &adapterError{status: http.StatusBadGateway, code: "upstream_error", message: fmt.Sprintf("upstream websocket handshake failed with status %d", resp.StatusCode)}
		}
		return nil, &adapterError{status: http.StatusBadGateway, code: "upstream_error", message: "upstream websocket connection failed"}
	}

	return conn, nil
}

func openAIResponsesWebSocketURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", &adapterError{status: http.StatusBadGateway, code: "invalid_provider_url", message: "configured provider base_url is invalid"}
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", &adapterError{status: http.StatusBadGateway, code: "invalid_provider_url", message: "configured provider base_url must use http, https, ws, or wss"}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/responses"

	return parsed.String(), nil
}

func (s *server) forwardOpenAIResponsesWebSocket(ctx context.Context, upstream *websocket.Conn, out chan<- any, done <-chan struct{}, req responseRequest) (memoryEntry, bool, error) {
	if err := upstream.WriteJSON(webSocketCreateEvent{Type: "response.create", Body: req}); err != nil {
		return memoryEntry{}, false, &adapterError{status: http.StatusBadGateway, code: "upstream_error", message: "upstream websocket write failed"}
	}
	for {
		select {
		case <-ctx.Done():
			return memoryEntry{}, false, ctx.Err()
		default:
		}
		_, data, err := upstream.ReadMessage()
		if err != nil {
			return memoryEntry{}, false, &adapterError{status: http.StatusBadGateway, code: "upstream_error", message: "upstream websocket read failed"}
		}
		writeWSJSON(out, done, json.RawMessage(data))
		entry, terminal, ok, err := s.memoryEntryFromWebSocketEvent(data, req)
		if err != nil {
			return memoryEntry{}, false, err
		}
		if terminal {
			return entry, ok, nil
		}
	}
}

type webSocketCreateEvent struct {
	Type string
	Body responseRequest
}

func (e webSocketCreateEvent) MarshalJSON() ([]byte, error) {
	data, err := e.Body.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	fields["type"] = mustMarshal(e.Type)
	if e.Body.Generate != nil {
		fields["generate"] = mustMarshal(*e.Body.Generate)
	}

	return json.Marshal(fields)
}

func (s *server) memoryEntryFromWebSocketEvent(data json.RawMessage, req responseRequest) (memoryEntry, bool, bool, error) {
	var event struct {
		Type     string            `json:"type"`
		Response canonicalResponse `json:"response"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return memoryEntry{}, false, false, &adapterError{status: http.StatusBadGateway, code: "upstream_error", message: "upstream websocket event was invalid"}
	}
	switch event.Type {
	case "response.completed", "response.done":
		if event.Response.ID == "" {
			return memoryEntry{}, false, false, &adapterError{status: http.StatusBadGateway, code: "upstream_error", message: "upstream websocket completion did not include a response id"}
		}
		entry := memoryEntry{id: event.Response.ID, input: req.Input, output: mustMarshal(event.Response.Output), expires: s.state.expires()}
		if req.Store == nil || *req.Store {
			s.state.put(entry)
		}
		return entry, true, true, nil
	case "response.failed", "response.incomplete", "error":
		return memoryEntry{}, true, false, nil
	default:
		return memoryEntry{}, false, false, nil
	}
}

func finishWebSocketError(out chan<- any, done <-chan struct{}, mu *sync.Mutex, inFlight *bool, local *memoryEntry, localCopy memoryEntry, req responseRequest, err error) {
	if strings.TrimSpace(req.PreviousResponseID) == localCopy.id {
		localCopy = memoryEntry{}
	}
	writeWSErrorForErr(out, done, err)
	mu.Lock()
	*local = localCopy
	*inFlight = false
	mu.Unlock()
}

func writeWSErrorForErr(out chan<- any, done <-chan struct{}, err error) {
	if errRequest, ok := errors.AsType[*requestError](err); ok {
		writeWSError(out, done, errRequest.code, errRequest.message)
		return
	}
	if errAdapter, ok := errors.AsType[*adapterError](err); ok {
		writeWSJSON(out, done, struct {
			Type  string        `json:"type"`
			Error responseError `json:"error"`
		}{Type: "error", Error: responseError{Type: errorType(errAdapter.status), Code: errAdapter.code, Message: errAdapter.message}})
		return
	}

	writeWSError(out, done, "response_failed", err.Error())
}

func writeWSError(out chan<- any, done <-chan struct{}, code, message string) {
	writeWSJSON(out, done, struct {
		Type  string        `json:"type"`
		Error responseError `json:"error"`
	}{Type: "error", Error: responseError{Type: "invalid_request_error", Code: code, Message: message}})
}

func writeWSJSON(out chan<- any, done <-chan struct{}, value any) {
	select {
	case out <- value:
	case <-done:
	}
}
