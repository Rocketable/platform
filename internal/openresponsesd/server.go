package openresponsesd

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const maxRequestBytes = 10 * 1024 * 1024

type server struct {
	cfg    config
	client *http.Client
	state  *memoryState
}

func newServer(cfg config, client *http.Client) *server {
	return &server{cfg: cfg, client: client, state: newMemoryState(cfg.State)}
}

func (s *server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		s.handleHealth(w, r)
	case "/v1/responses":
		if !s.authorize(w, r) {
			return
		}
		if websocket.IsWebSocketUpgrade(r) {
			s.handleWebSocket(w, r)
			return
		}
		s.handleResponses(w, r)
	case "/v1/responses/compact":
		if !s.authorize(w, r) {
			return
		}
		s.handleCompact(w, r)
	default:
		http.NotFound(w, r)
	}
}

type healthProvider struct {
	Type   string   `json:"type"`
	Models []string `json:"models,omitempty"`
}

type healthResponse struct {
	OK              bool                      `json:"ok"`
	Addr            string                    `json:"addr"`
	DefaultProvider string                    `json:"default_provider,omitempty"`
	Providers       map[string]healthProvider `json:"providers"`
	StateMode       string                    `json:"state_mode"`
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}

	providers := make(map[string]healthProvider, len(s.cfg.Providers))
	for name, provider := range s.cfg.Providers {
		providers[name] = healthProvider{Type: provider.Type, Models: provider.Models}
	}

	writeJSON(w, http.StatusOK, healthResponse{OK: true, Addr: s.cfg.Addr, DefaultProvider: s.cfg.DefaultProvider, Providers: providers, StateMode: s.cfg.State.Mode})
}

func (s *server) authorize(w http.ResponseWriter, r *http.Request) bool {
	if len(s.cfg.Auth.Tokens) == 0 {
		return true
	}

	value := strings.TrimSpace(r.Header.Get("Authorization"))
	token, ok := strings.CutPrefix(value, "Bearer ")
	if ok {
		for _, configured := range s.cfg.Auth.Tokens {
			if subtle.ConstantTimeCompare([]byte(token), []byte(configured)) == 1 {
				return true
			}
		}
	}

	w.Header().Set("WWW-Authenticate", "Bearer")
	writeJSON(w, http.StatusUnauthorized, errorEnvelope{Error: responseError{Type: "authentication_error", Code: "invalid_auth_token", Message: "missing or invalid bearer token"}})

	return false
}

func (s *server) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}

	req, err := decodeResponseRequest(r)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	if req.Generate != nil {
		writeRequestError(w, &requestError{code: "unsupported_request_field", message: "unsupported request field generate"})
		return
	}

	resp, err := s.createResponse(r.Context(), req, nil)
	if err != nil {
		if req.Stream {
			writeSSEError(w, err)
			return
		}
		writeAdapterError(w, err)
		return
	}

	if req.Stream {
		writeSSE(w, resp)
		return
	}

	writeRawJSON(w, http.StatusOK, resp.raw)
}

func (s *server) handleCompact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}

	req, err := decodeResponseRequest(r)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	if req.Generate != nil {
		writeRequestError(w, &requestError{code: "unsupported_request_field", message: "unsupported request field generate"})
		return
	}

	if len(req.Input) == 0 && req.PreviousResponseID == "" {
		writeRequestError(w, &requestError{code: "missing_compaction_input", message: "input or previous_response_id is required"})
		return
	}
	providerName, model, err := s.selectProvider(req.Model)
	if err != nil {
		writeAdapterError(w, err)
		return
	}
	req.Model = model
	provider := s.cfg.Providers[providerName]
	if provider.Type == "openai_responses" {
		req.raw = mustMarshal(req)
		resp, err := s.openAIResponsesCompact(r.Context(), provider, req)
		if err != nil {
			writeAdapterError(w, err)
			return
		}
		writeRawJSON(w, http.StatusOK, resp.raw)
		return
	}

	if req.PreviousResponseID != "" {
		entry, ok := s.state.get(req.PreviousResponseID)
		if !ok {
			writeAdapterError(w, &adapterError{status: http.StatusBadRequest, code: "previous_response_not_found", message: "previous_response_id was not found"})
			return
		}
		history := mergeInput(entry.input, entry.output)
		req.Input = mergeInput(history, req.Input)
		req.PreviousResponseID = ""
	}

	input, err := compactionInput(req.Input)
	if err != nil {
		writeAdapterError(w, err)
		return
	}
	req.Input = input
	req.raw = mustMarshal(req)

	resp, err := s.createResponse(r.Context(), req, nil)
	if err != nil {
		writeAdapterError(w, err)
		return
	}

	writeRawJSON(w, http.StatusOK, resp.raw)
}

func (s *server) createResponse(ctx context.Context, req responseRequest, local *memoryEntry) (canonicalResponse, error) {
	if err := validateToolSchemas(req.Tools); err != nil {
		return canonicalResponse{}, err
	}

	providerName, model, err := s.selectProvider(req.Model)
	if err != nil {
		return canonicalResponse{}, err
	}

	if strings.TrimSpace(req.PreviousResponseID) != "" {
		entry, ok := s.state.get(req.PreviousResponseID)
		if !ok && local != nil && local.id == req.PreviousResponseID {
			entry = *local
			ok = true
		}
		if !ok {
			return canonicalResponse{}, &adapterError{status: http.StatusBadRequest, code: "previous_response_not_found", message: "previous_response_id was not found"}
		}

		history := mergeInput(entry.input, entry.output)
		req.Input = mergeInput(history, req.Input)
	}

	req.Model = model
	req.raw = mustMarshal(req)
	provider := s.cfg.Providers[providerName]
	var resp canonicalResponse
	switch provider.Type {
	case "openai_responses":
		resp, err = s.openAIResponses(ctx, provider, req)
	case "openai_chat_completions":
		resp, err = s.chatCompletions(ctx, provider, req)
	case "anthropic_messages":
		resp, err = s.anthropicMessages(ctx, provider, req)
	default:
		err = &adapterError{status: http.StatusBadGateway, code: "unsupported_provider", message: "unsupported provider type"}
	}
	if err != nil {
		if local != nil && strings.TrimSpace(req.PreviousResponseID) == local.id {
			*local = memoryEntry{}
		}
		return canonicalResponse{}, err
	}

	store := req.Store == nil || *req.Store
	entry := memoryEntry{id: resp.ID, input: req.Input, output: mustMarshal(resp.Output), expires: s.state.expires()}
	if store {
		s.state.put(entry)
	} else if local != nil {
		*local = entry
	}

	return resp, nil
}

func (s *server) selectProvider(model string) (string, string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", "", &adapterError{status: http.StatusBadRequest, code: "missing_model", message: "model is required"}
	}

	for _, route := range s.cfg.ModelRoutes {
		matched, err := path.Match(route.Match, model)
		if err != nil {
			return "", "", &adapterError{status: http.StatusInternalServerError, code: "invalid_model_route", message: "configured model route is invalid"}
		}
		if matched {
			if route.StripPrefix != "" {
				model = strings.TrimPrefix(model, route.StripPrefix)
			}
			return route.Provider, model, nil
		}
	}

	if s.cfg.DefaultProvider != "" {
		return s.cfg.DefaultProvider, model, nil
	}

	return "", "", &adapterError{status: http.StatusBadRequest, code: "provider_not_found", message: "no provider route matched model"}
}

func decodeResponseRequest(r *http.Request) (responseRequest, error) {
	raw, err := readJSONBody(r)
	if err != nil {
		return responseRequest{}, err
	}

	req, err := unmarshalResponseRequest(raw)
	if err != nil {
		return responseRequest{}, err
	}

	return req, nil
}

func unmarshalResponseRequest(raw json.RawMessage) (responseRequest, error) {
	var req responseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return responseRequest{}, &requestError{code: "invalid_json", message: "malformed JSON request body"}
	}
	if len(req.Unsupported) != 0 {
		field := "request body"
		if len(req.unsupportedFields) > 0 {
			field = req.unsupportedFields[0]
		}
		return responseRequest{}, &requestError{code: "unsupported_request_field", message: "unsupported request field " + field}
	}
	req.raw = raw

	return req, nil
}

func readJSONBody(r *http.Request) (json.RawMessage, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, &requestError{code: "unsupported_content_type", message: "content-type must be application/json"}
	}

	data, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if len(data) > maxRequestBytes {
		return nil, &requestError{code: "request_too_large", message: "request body exceeds limit"}
	}

	return data, nil
}

func compactionInput(raw json.RawMessage) (json.RawMessage, error) {
	items, err := inputItems(raw)
	if err != nil {
		return nil, err
	}
	pendingCalls := make(map[string]bool)
	parts := []responseContentPart{{Type: "input_text", Text: "Summarize this conversation into a compact checkpoint. Preserve each item boundary in the summary."}}
	for _, item := range items {
		if item.Type == "function_call" {
			pendingCalls[item.CallID] = true
		}
		if item.Type == "function_call_output" {
			delete(pendingCalls, item.CallID)
		}
		var prompt strings.Builder
		prompt.WriteString("checkpoint item\n")
		prompt.WriteString("type: ")
		prompt.WriteString(item.Type)
		prompt.WriteString("\nrole: ")
		prompt.WriteString(item.Role)
		prompt.WriteString("\ncontent: ")
		prompt.Write(item.Content)
		prompt.WriteString("\ncall_id: ")
		prompt.WriteString(item.CallID)
		prompt.WriteString("\nname: ")
		prompt.WriteString(item.Name)
		prompt.WriteString("\narguments: ")
		prompt.WriteString(item.Arguments)
		prompt.WriteString("\noutput: ")
		prompt.WriteString(item.Output)
		parts = append(parts, responseContentPart{Type: "input_text", Text: prompt.String()})
	}
	if len(pendingCalls) > 0 {
		return nil, &adapterError{status: http.StatusBadRequest, code: "unanswered_function_call", message: "compaction cannot summarize unanswered function calls"}
	}

	return mustMarshal([]inputItem{{Type: "message", Role: "user", Content: mustMarshal(parts)}}), nil
}

func mergeInput(previous, next json.RawMessage) json.RawMessage {
	var merged []json.RawMessage
	merged = appendInput(merged, previous)
	merged = appendInput(merged, next)

	return mustMarshal(merged)
}

func appendInput(merged []json.RawMessage, raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return merged
	}
	if raw[0] == '[' {
		var items []json.RawMessage
		_ = json.Unmarshal(raw, &items)
		return append(merged, items...)
	}
	if raw[0] == '"' {
		return append(merged, mustMarshal(inputItem{Type: "message", Role: "user", Content: raw}))
	}

	return append(merged, raw)
}

type memoryState struct {
	max int
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]memoryEntry
	order   []string
}

type memoryEntry struct {
	id      string
	input   json.RawMessage
	output  json.RawMessage
	expires time.Time
}

func newMemoryState(cfg stateConfig) *memoryState {
	maxResponses := cfg.MaxResponses
	if maxResponses == 0 {
		maxResponses = 1024
	}

	return &memoryState{max: maxResponses, ttl: time.Duration(cfg.TTLSeconds) * time.Second, entries: make(map[string]memoryEntry)}
}

func (m *memoryState) expires() time.Time {
	if m.ttl == 0 {
		return time.Time{}
	}

	return time.Now().Add(m.ttl)
}

func (m *memoryState) put(entry memoryEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.entries[entry.id]; !ok {
		m.order = append(m.order, entry.id)
	}
	m.entries[entry.id] = entry
	m.evictLocked()
}

func (m *memoryState) get(id string) (memoryEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.entries[id]
	if !ok {
		return memoryEntry{}, false
	}
	if !entry.expires.IsZero() && time.Now().After(entry.expires) {
		delete(m.entries, id)
		return memoryEntry{}, false
	}

	return entry, true
}

func (m *memoryState) evictLocked() {
	for len(m.order) > m.max {
		delete(m.entries, m.order[0])
		m.order = m.order[1:]
	}
}

type requestError struct{ code, message string }

func (e *requestError) Error() string { return e.message }

type adapterError struct {
	status        int
	code, message string
}

func (e *adapterError) Error() string { return e.message }

func writeRequestError(w http.ResponseWriter, err error) {
	if errRequest, ok := errors.AsType[*requestError](err); ok {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: responseError{Type: "invalid_request_error", Code: errRequest.code, Message: errRequest.message}})
		return
	}

	writeJSON(w, http.StatusInternalServerError, errorEnvelope{Error: responseError{Type: "server_error", Code: "internal_error", Message: "internal server error"}})
}

func writeAdapterError(w http.ResponseWriter, err error) {
	if errAdapter, ok := errors.AsType[*adapterError](err); ok {
		writeJSON(w, errAdapter.status, errorEnvelope{Error: responseError{Type: errorType(errAdapter.status), Code: errAdapter.code, Message: errAdapter.message}})
		return
	}

	writeJSON(w, http.StatusBadGateway, errorEnvelope{Error: responseError{Type: "server_error", Code: "upstream_error", Message: "upstream provider failed"}})
}

func methodNotAllowed(w http.ResponseWriter, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	writeJSON(w, http.StatusMethodNotAllowed, errorEnvelope{Error: responseError{Type: "invalid_request_error", Code: "method_not_allowed", Message: "method not allowed"}})
}

func errorType(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "authentication_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "server_error"
	default:
		return "invalid_request_error"
	}
}

type errorEnvelope struct {
	Error responseError `json:"error"`
}

type responseError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeRawJSON(w http.ResponseWriter, status int, data json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeSSE(w http.ResponseWriter, response canonicalResponse) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	seq := 0
	writeEvent := func(event responseEvent) {
		event.SequenceNumber = seq
		seq++
		data := mustMarshal(event)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
	}

	writeEvent(responseEvent{Type: "response.created", Response: response.summary("queued")})
	writeEvent(responseEvent{Type: "response.in_progress", Response: response.summary("in_progress")})
	for i, item := range response.Output {
		writeEvent(responseEvent{Type: "response.output_item.done", OutputIndex: new(i), Item: &item})
	}
	writeEvent(responseEvent{Type: "response.completed", Response: response.object()})
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func writeSSEError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	errResp := responseError{Type: "server_error", Code: "upstream_error", Message: "upstream provider failed"}
	if errAdapter, ok := errors.AsType[*adapterError](err); ok {
		errResp = responseError{Type: errorType(errAdapter.status), Code: errAdapter.code, Message: errAdapter.message}
	}
	data := mustMarshal(struct {
		Type           string        `json:"type"`
		SequenceNumber int           `json:"sequence_number"`
		Error          responseError `json:"error"`
	}{Type: "error", SequenceNumber: 0, Error: errResp})
	_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

type responseEvent struct {
	Type           string              `json:"type"`
	SequenceNumber int                 `json:"sequence_number"`
	Response       responseObject      `json:"response,omitempty"`
	OutputIndex    *int                `json:"output_index,omitempty"`
	Item           *responseOutputItem `json:"item,omitempty"`
}

func mustMarshal(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}

	return data
}
