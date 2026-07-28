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
	"testing/fstest"
	"testing/synctest"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

type mockResponsesAPI struct {
	mu               sync.Mutex
	calls            []responses.ResponseNewParams
	compactCalls     []responses.ResponseCompactParams
	responses        []*responses.Response
	compactResponses []*responses.CompactedResponse
	err              error
	compactErr       error
	newFunc          func(context.Context, *responses.ResponseNewParams) (*responses.Response, error)
}

func (m *mockResponsesAPI) New(ctx context.Context, params *responses.ResponseNewParams, _ ...option.RequestOption) (*responses.Response, error) {
	m.mu.Lock()
	m.calls = append(m.calls, *params)
	m.mu.Unlock()

	if m.newFunc != nil {
		return m.newFunc(ctx, params)
	}

	if m.err != nil {
		return nil, m.err
	}

	if len(m.responses) == 0 {
		return nil, errors.New("no mock response configured")
	}

	resp := m.responses[0]
	m.responses = m.responses[1:]

	return resp, nil
}

func (m *mockResponsesAPI) Compact(_ context.Context, params *responses.ResponseCompactParams, _ ...option.RequestOption) (*responses.CompactedResponse, error) {
	m.mu.Lock()
	m.compactCalls = append(m.compactCalls, *params)
	m.mu.Unlock()

	if m.compactErr != nil {
		return nil, m.compactErr
	}

	if len(m.compactResponses) == 0 {
		return nil, errors.New("no mock compact response configured")
	}

	resp := m.compactResponses[0]
	m.compactResponses = m.compactResponses[1:]

	return resp, nil
}

type mockSessionStore struct {
	mu      sync.Mutex
	saves   [][]SessionEntry
	entries []SessionEntry
}

type checkpointCall struct {
	name       string
	checkpoint ActiveTurnCheckpoint
	turnID     string
}

type mockCheckpointSink struct {
	mu          sync.Mutex
	calls       []checkpointCall
	providerErr error
}

func (m *mockCheckpointSink) StartActiveTurn(_ context.Context, checkpoint *ActiveTurnCheckpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, checkpointCall{name: "start", checkpoint: *checkpoint})

	return nil
}

func (m *mockCheckpointSink) RecordProviderResponse(_ context.Context, checkpoint *ActiveTurnCheckpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, checkpointCall{name: "provider", checkpoint: *checkpoint})

	return m.providerErr
}

func (m *mockCheckpointSink) RecordCompletedToolOutput(_ context.Context, checkpoint *ActiveTurnCheckpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, checkpointCall{name: "tool", checkpoint: *checkpoint})

	return nil
}

func (m *mockCheckpointSink) RecordRecoveredReplay(_ context.Context, checkpoint *ActiveTurnCheckpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, checkpointCall{name: "recovered", checkpoint: *checkpoint})

	return nil
}

func (m *mockCheckpointSink) ClearCompletedTurn(_ context.Context, turnID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, checkpointCall{name: "clear", turnID: turnID})

	return nil
}

func (m *mockCheckpointSink) snapshot() []checkpointCall {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]checkpointCall{}, m.calls...)
}

type mockPermissionReviewer struct {
	decision permissionReviewDecision
	requests []permissionReviewRequest
}

func (m *mockPermissionReviewer) reviewPermission(_ context.Context, request *permissionReviewRequest, output chan<- ChatResponse) permissionReviewDecision {
	m.requests = append(m.requests, *request)
	emitPermissionReviewResult(output, "guardian", m.decision)

	return m.decision
}

func mockResponses(responseItems ...*responses.Response) *mockResponsesAPI {
	var mock mockResponsesAPI

	mock.responses = responseItems

	return &mock
}

func mockResponseError(err error) *mockResponsesAPI {
	var mock mockResponsesAPI

	mock.err = err

	return &mock
}

func mockResponseFunc(newFunc func(context.Context, *responses.ResponseNewParams) (*responses.Response, error)) *mockResponsesAPI {
	var mock mockResponsesAPI

	mock.newFunc = newFunc

	return &mock
}

func contextLengthExceededError() error {
	req := httptest.NewRequest(http.MethodPost, "https://api.openai.test/v1/responses", http.NoBody)
	resp := &http.Response{StatusCode: http.StatusBadRequest, Status: "400 Bad Request", Request: req}

	return &openai.Error{Code: "context_length_exceeded", Message: "too large", StatusCode: http.StatusBadRequest, Request: req, Response: resp}
}

func testLooper(client responsesAPI) *looper {
	var l looper

	l.modelRef = defaultModelRef()
	l.Client = client
	l.Origin = ProviderOrigin{ProviderID: "openai", Route: "responses:https://api.openai.com/v1", ModelID: openai.ChatModelGPT5, AuthenticationEpoch: "epoch-openai"}
	l.Model = openai.ChatModelGPT5
	l.PermissionReviewer = inertPermissionReviewer{}
	l.CheckpointSink = InertCheckpointSink{}

	return &l
}

func emptyTestLooper() *looper {
	var l looper

	l.modelRef = defaultModelRef()
	l.PermissionReviewer = inertPermissionReviewer{}
	l.CheckpointSink = InertCheckpointSink{}

	return &l
}

func testSessionStore() *mockSessionStore {
	var store mockSessionStore

	return &store
}

func testPromptInput(role PromptInputRole, text string, responseCh chan<- ChatResponse) PromptInput {
	var input PromptInput

	input.Role = role
	input.Text = text
	input.Responses = responseCh

	return input
}

func testPromptInputWithAttachments(role PromptInputRole, text string, attachments []Attachment, responseCh chan<- ChatResponse) PromptInput {
	var input PromptInput

	input.Role = role
	input.Text = text
	input.Attachments = attachments
	input.Responses = responseCh

	return input
}

func assistantMessage(text string) ChatResponse {
	var response ChatResponse

	response.Kind = ChatResponseAssistantMessage
	response.Text = text

	return response
}

func assistantCommentary(text string) ChatResponse {
	var response ChatResponse

	response.Kind = ChatResponseAssistantCommentary
	response.Text = text

	return response
}

func reasoningSummary(text string) ChatResponse {
	var response ChatResponse

	response.Kind = ChatResponseReasoningSummary
	response.Text = text

	return response
}

func toolDiagnosticResponse(diagnostic *ToolDiagnostic) ChatResponse {
	var response ChatResponse

	response.Kind = ChatResponseAssistantTool
	response.Tool = diagnostic

	return response
}

func subagentDiagnosticResponse(diagnostic *SubagentDiagnostic) ChatResponse {
	var response ChatResponse

	response.Kind = ChatResponseAssistantTool
	response.Subagent = diagnostic

	return response
}

func providerDiagnosticResponse(diagnostic *ProviderDiagnostic) ChatResponse {
	if diagnostic.ProviderID == "" {
		diagnostic.ProviderID = "openai"
	}

	var response ChatResponse

	response.Kind = ChatResponseAssistantTool
	response.Provider = diagnostic

	return response
}

func testToolDiagnostic(phase, name string) *ToolDiagnostic {
	var diagnostic ToolDiagnostic

	diagnostic.Phase = phase
	diagnostic.Name = name

	return &diagnostic
}

func testReviewSubagentDiagnostic(label string, index, total int, text string) *SubagentDiagnostic {
	var diagnostic SubagentDiagnostic

	diagnostic.Name = "review"
	diagnostic.Label = label
	diagnostic.Index = index
	diagnostic.Total = total
	diagnostic.Text = text

	return &diagnostic
}

func testFunctionToolParam(name string) responses.FunctionToolParam {
	var definition responses.FunctionToolParam

	definition.Name = name
	definition.Parameters = map[string]any{"type": "object"}
	definition.Strict = openai.Bool(true)

	return definition
}

func testLooperTool(name string) looperTool {
	var tool looperTool

	tool.Definition = testFunctionToolParam(name)

	return tool
}

func testFunctionCall(id, callID, name, arguments string) responses.ResponseFunctionToolCall {
	var call responses.ResponseFunctionToolCall

	call.ID = id
	call.CallID = callID
	call.Name = name
	call.Arguments = arguments

	return call
}

func emptyToolCallMetadata() toolCallMetadata {
	var metadata toolCallMetadata

	return metadata
}

func testInputMessage(role responses.EasyInputMessageRole, text, phase string) responses.ResponseInputItemUnionParam {
	var content responses.EasyInputMessageContentUnionParam

	content.OfString = openai.String(text)

	var message responses.EasyInputMessageParam

	message.Role = role
	message.Content = content
	message.Phase = responses.EasyInputMessagePhase(phase)
	message.Type = "message"

	var item responses.ResponseInputItemUnionParam

	item.OfMessage = &message

	return item
}

func testInputReasoning(id, summary, encryptedContent string) responses.ResponseInputItemUnionParam {
	var summaryParam responses.ResponseReasoningItemSummaryParam

	summaryParam.Text = summary
	summaryParam.Type = "summary_text"

	var reasoning responses.ResponseReasoningItemParam

	reasoning.ID = id
	reasoning.Summary = []responses.ResponseReasoningItemSummaryParam{summaryParam}
	reasoning.EncryptedContent = openai.String(encryptedContent)
	reasoning.Type = "reasoning"

	var item responses.ResponseInputItemUnionParam

	item.OfReasoning = &reasoning

	return item
}

func (m *mockSessionStore) appendEntry(entry *SessionEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.entries = append(m.entries, *entry)

	_, turns, err := loadSession(sessionEntries(m.entries), ProviderOrigin{})
	if err != nil {
		return fmt.Errorf("reload session: %w", err)
	}

	clone := make([]SessionEntry, len(turns))
	copy(clone, turns)
	m.saves = append(m.saves, clone)

	return nil
}

func emptySession() func(func(SessionEntry, error) bool) {
	return func(func(SessionEntry, error) bool) {}
}

func sessionEntries(entries []SessionEntry) func(func(SessionEntry, error) bool) {
	return func(yield func(SessionEntry, error) bool) {
		for i := range entries {
			if !yield(entries[i], nil) {
				return
			}
		}
	}
}

func TestLoadSessionPreservesOpaqueForExactOrigin(t *testing.T) {
	origin := ProviderOrigin{ProviderID: "openai", Route: "responses:https://api.openai.com/v1", ModelID: "gpt-5.5", AuthenticationEpoch: "epoch-1"}
	replay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-1", "summary", "sealed")})
	require.NoError(t, err)

	history, turns, err := loadSession(sessionEntries([]SessionEntry{{Origin: &origin, ReplayInput: replay}}), origin)
	require.NoError(t, err)
	require.Len(t, turns, 1)
	require.JSONEq(t, string(replay[0]), marshalJSON(t, history.replay[0]))
	require.JSONEq(t, `{"content":"summary","role":"assistant","type":"message"}`, marshalJSON(t, history.portable[0]))
	require.False(t, history.legacyOpaque)
	require.NoError(t, history.portableError)
}

func TestLoadSessionProjectsPortableForProviderMismatch(t *testing.T) {
	origin := ProviderOrigin{ProviderID: "openai", Route: "responses:https://api.openai.com/v1", ModelID: "gpt-5.5", AuthenticationEpoch: "epoch-1"}
	destination := origin
	destination.ProviderID = "other"
	replay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputReasoning("reasoning-1", "summary", "sealed"),
		functionCallReplayInput("provider-call", "call-1", "read", `{}`),
	})
	require.NoError(t, err)

	history, _, err := loadSession(sessionEntries([]SessionEntry{{Origin: &origin, ReplayInput: replay}}), destination)
	require.NoError(t, err)
	require.NotContains(t, marshalJSON(t, history.replay), "sealed")
	require.NotContains(t, marshalJSON(t, history.replay), "provider-call")
	require.Contains(t, marshalJSON(t, history.replay), "summary")
	require.Contains(t, marshalJSON(t, history.replay), `"call_id":"call-1"`)
}

func TestLoadSessionProjectsPortableForRouteModelAndEpochMismatch(t *testing.T) {
	origin := ProviderOrigin{ProviderID: "openai", Route: "responses:https://api.openai.com/v1", ModelID: "gpt-5.5", AuthenticationEpoch: "epoch-1"}
	replay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-1", "summary", "sealed")})
	require.NoError(t, err)

	for _, destination := range []ProviderOrigin{
		{ProviderID: origin.ProviderID, Route: "responses:https://other.example/v1", ModelID: origin.ModelID, AuthenticationEpoch: origin.AuthenticationEpoch},
		{ProviderID: origin.ProviderID, Route: origin.Route, ModelID: "gpt-5.6", AuthenticationEpoch: origin.AuthenticationEpoch},
		{ProviderID: origin.ProviderID, Route: origin.Route, ModelID: origin.ModelID, AuthenticationEpoch: "epoch-2"},
	} {
		history, _, err := loadSession(sessionEntries([]SessionEntry{{Origin: &origin, ReplayInput: replay}}), destination)
		require.NoError(t, err)
		require.NotContains(t, marshalJSON(t, history.replay), "sealed")
	}
}

func TestLoadSessionMarksOnlyOpaqueOriginlessEntriesLegacy(t *testing.T) {
	portable, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputMessage(responses.EasyInputMessageRoleUser, "portable", "")})
	require.NoError(t, err)
	opaque, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-1", "summary", "sealed")})
	require.NoError(t, err)

	destination := ProviderOrigin{ProviderID: "openai", Route: "route", ModelID: "model", AuthenticationEpoch: "epoch"}

	history, _, err := loadSession(sessionEntries([]SessionEntry{{ReplayInput: portable}, {ReplayInput: opaque}}), destination)
	require.NoError(t, err)
	require.True(t, history.legacyOpaque)
	require.Len(t, history.replay, 2)
	require.Len(t, history.portable, 2)
	require.NotContains(t, marshalJSON(t, history.portable), "sealed")
}

func TestLoadSessionRetainsLegacyEncryptedCompactionForFirstAttempt(t *testing.T) {
	replay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{compactionReplayInput("compaction-1", "sealed", "")})
	require.NoError(t, err)

	history, _, err := loadSession(sessionEntries([]SessionEntry{{ReplayInput: replay}}), ProviderOrigin{})
	require.NoError(t, err)
	require.True(t, history.legacyOpaque)
	require.Contains(t, marshalJSON(t, history.replay), "sealed")

	var missing *MissingPortableContextError
	require.ErrorAs(t, history.portableError, &missing)
}

func TestLooperLegacyOpaqueSuccessBindsOrigin(t *testing.T) {
	origin := ProviderOrigin{ProviderID: "work", Route: "responses:https://work.example/v1", ModelID: "worker", AuthenticationEpoch: "epoch-work"}
	legacyReplay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-legacy", "readable legacy context", "sealed-legacy")})
	require.NoError(t, err)

	mock := mockResponses(responseWithMessage("resp-bound", "done"))
	loop := testLooper(mock)
	loop.Origin = origin
	checkpointSink := new(mockCheckpointSink)
	loop.CheckpointSink = checkpointSink

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "current prompt", make(chan ChatResponse, 1))

	close(input)

	var saved SessionEntry

	require.NoError(t, loop.Loop(t.Context(), input, sessionEntries([]SessionEntry{{Version: 1, Type: "turn", ReplayInput: legacyReplay}}), func(entry SessionEntry) error {
		saved = entry
		return nil
	}, make(chan os.Signal, 1)))
	require.Len(t, mock.calls, 1)
	require.Contains(t, marshalJSON(t, mock.calls[0].Input.OfInputItemList), "sealed-legacy")
	require.Equal(t, legacyReplayOpaqueBound, saved.LegacyReplay)
	require.Equal(t, &origin, saved.Origin)

	checkpointCalls := checkpointSink.snapshot()
	for _, call := range checkpointCalls {
		if call.name == "provider" {
			require.Equal(t, legacyReplayOpaqueBound, call.checkpoint.LegacyReplay)
		}
	}
}

func TestLooperLegacyOpaqueTooManyRequestsIsOneShot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		legacyReplay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-legacy", "readable", "sealed")})
		require.NoError(t, err)

		mock := mockResponseError(openAIError("too_many_requests", "slow down", nil))
		loop := testLooper(mock)

		input := make(chan PromptInput, 1)
		input <- testPromptInput(PromptInputRoleUser, "current", make(chan ChatResponse, 1))

		close(input)

		require.Error(t, loop.Loop(t.Context(), input, sessionEntries([]SessionEntry{{ReplayInput: legacyReplay}}), discardSession, make(chan os.Signal, 1)))
		require.Len(t, mock.calls, 1)
	})
}

func TestLooperLegacyMigrationDisablesSDKRetries(t *testing.T) {
	var (
		mu       sync.Mutex
		requests int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After-Ms", "0")
		w.WriteHeader(http.StatusTooManyRequests)

		_, err := io.WriteString(w, `{"error":{"message":"slow down","type":"too_many_requests","param":null,"code":"too_many_requests"}}`)
		if err != nil {
			t.Errorf("write retryable response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(server.URL))
	loop := testOpenAILoop(t, &client)
	legacyReplay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-legacy", "readable", "sealed")})
	require.NoError(t, err)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "current", make(chan ChatResponse, 1))

	close(input)

	require.Error(t, loop.Loop(t.Context(), input, sessionEntries([]SessionEntry{{ReplayInput: legacyReplay}}), discardSession, make(chan os.Signal, 1)))
	mu.Lock()
	defer mu.Unlock()

	require.Equal(t, 1, requests)
}

func TestLooperLegacyOpaqueContextLengthIsOneShot(t *testing.T) {
	legacyReplay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-legacy", "readable", "sealed")})
	require.NoError(t, err)

	mock := mockResponseError(contextLengthExceededError())
	loop := testLooper(mock)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "current", make(chan ChatResponse, 1))

	close(input)

	require.Error(t, loop.Loop(t.Context(), input, sessionEntries([]SessionEntry{{ReplayInput: legacyReplay}}), discardSession, make(chan os.Signal, 1)))
	require.Len(t, mock.calls, 1)
	require.Empty(t, mock.compactCalls)
}

func TestLooperLegacyPortableFallbackTooManyRequestsIsOneShot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		legacyReplay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-legacy", "readable", "sealed")})
		require.NoError(t, err)

		calls := 0
		mock := mockResponseFunc(func(context.Context, *responses.ResponseNewParams) (*responses.Response, error) {
			calls++
			if calls == 1 {
				return nil, &openai.Error{StatusCode: http.StatusBadRequest, Message: "invalid encrypted content"}
			}

			return nil, openAIError("too_many_requests", "slow down", nil)
		})
		loop := testLooper(mock)

		input := make(chan PromptInput, 1)
		input <- testPromptInput(PromptInputRoleUser, "current", make(chan ChatResponse, 1))

		close(input)

		require.Error(t, loop.Loop(t.Context(), input, sessionEntries([]SessionEntry{{ReplayInput: legacyReplay}}), discardSession, make(chan os.Signal, 1)))
		require.Equal(t, 2, calls)
	})
}

func TestLooperLegacyFailedResponsesAreOneShot(t *testing.T) {
	for _, phase := range []string{"opaque", "portable"} {
		for _, code := range []responses.ResponseErrorCode{responses.ResponseErrorCodeRateLimitExceeded, responses.ResponseErrorCode("context_length_exceeded")} {
			t.Run(phase+"/"+string(code), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					legacyReplay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-legacy", "readable", "sealed")})
					require.NoError(t, err)

					failed := failedResponseWithCode("resp-failed", code, "failed")
					calls := 0
					mock := mockResponseFunc(func(context.Context, *responses.ResponseNewParams) (*responses.Response, error) {
						calls++
						if phase == "portable" && calls == 1 {
							return nil, &openai.Error{StatusCode: http.StatusBadRequest, Message: "invalid encrypted content"}
						}

						return failed, nil
					})
					loop := testLooper(mock)

					input := make(chan PromptInput, 1)
					input <- testPromptInput(PromptInputRoleUser, "current", make(chan ChatResponse, 1))

					close(input)

					require.Error(t, loop.Loop(t.Context(), input, sessionEntries([]SessionEntry{{ReplayInput: legacyReplay}}), discardSession, make(chan os.Signal, 1)))

					if phase == "opaque" {
						require.Equal(t, 1, calls)
					} else {
						require.Equal(t, 2, calls)
					}

					require.Empty(t, mock.compactCalls)
				})
			})
		}
	}
}

func TestLooperLegacyOpaqueRejectionRetriesPortableOnce(t *testing.T) {
	origin := ProviderOrigin{ProviderID: "work", Route: "responses:https://work.example/v1", ModelID: "worker", AuthenticationEpoch: "epoch-work"}
	legacyReplay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-legacy", "readable legacy context", "sealed-legacy")})
	require.NoError(t, err)

	calls := 0
	mock := mockResponseFunc(func(context.Context, *responses.ResponseNewParams) (*responses.Response, error) {
		calls++
		if calls == 1 {
			return nil, &openai.Error{StatusCode: http.StatusBadRequest, Message: "invalid encrypted content"}
		}

		if calls == 2 {
			return responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "read", `{}`)}), nil
		}

		return responseWithMessage("resp-portable", "done"), nil
	})
	loop := testLooper(mock)
	loop.Origin = origin
	loop.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "read", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}
	tool := testLooperTool("read")
	tool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
		return TextToolResult("read result"), nil
	}
	loop.Tools = map[string]looperTool{"read": tool}
	checkpointSink := new(mockCheckpointSink)
	loop.CheckpointSink = checkpointSink

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "current prompt", make(chan ChatResponse, 1))

	close(input)

	var saved SessionEntry

	require.NoError(t, loop.Loop(t.Context(), input, sessionEntries([]SessionEntry{{Version: 1, Type: "turn", ReplayInput: legacyReplay}}), func(entry SessionEntry) error {
		saved = entry
		return nil
	}, make(chan os.Signal, 1)))
	require.Len(t, mock.calls, 3)
	first := marshalJSON(t, mock.calls[0].Input.OfInputItemList)
	second := marshalJSON(t, mock.calls[1].Input.OfInputItemList)
	third := marshalJSON(t, mock.calls[2].Input.OfInputItemList)
	require.Contains(t, first, "sealed-legacy")
	require.NotContains(t, second, "sealed-legacy")
	require.NotContains(t, second, "reasoning-legacy")
	require.Contains(t, second, "readable legacy context")
	require.Equal(t, 1, strings.Count(second, "current prompt"))
	require.NotContains(t, third, "sealed-legacy")
	require.NotContains(t, third, "reasoning-legacy")
	require.Equal(t, 1, strings.Count(third, "current prompt"))
	require.Equal(t, legacyReplayPortable, saved.LegacyReplay)
	require.Equal(t, &origin, saved.Origin)

	checkpointCalls := checkpointSink.snapshot()
	require.Equal(t, legacyReplayPortable, checkpointCalls[1].checkpoint.LegacyReplay)
	require.Equal(t, legacyReplayPortable, checkpointCalls[len(checkpointCalls)-2].checkpoint.LegacyReplay)
}

func TestLooperLegacyOpaqueRejectionDoesNotRetryTwice(t *testing.T) {
	legacyReplay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-legacy", "readable", "sealed")})
	require.NoError(t, err)

	calls := 0
	mock := mockResponseFunc(func(context.Context, *responses.ResponseNewParams) (*responses.Response, error) {
		calls++
		return nil, &openai.Error{StatusCode: http.StatusBadRequest, Message: "invalid encrypted content"}
	})
	loop := testLooper(mock)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "current", make(chan ChatResponse, 1))

	close(input)

	err = loop.Loop(t.Context(), input, sessionEntries([]SessionEntry{{ReplayInput: legacyReplay}}), discardSession, make(chan os.Signal, 1))
	require.Error(t, err)
	require.Equal(t, 2, calls)
}

func testLooperLegacyOpaqueDoesNotRetry(t *testing.T, errProvider error) {
	t.Helper()

	legacyReplay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-legacy", "readable", "sealed")})
	require.NoError(t, err)

	mock := mockResponseError(errProvider)
	loop := testLooper(mock)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "current", make(chan ChatResponse, 1))

	close(input)

	require.Error(t, loop.Loop(t.Context(), input, sessionEntries([]SessionEntry{{ReplayInput: legacyReplay}}), discardSession, make(chan os.Signal, 1)))
	require.Len(t, mock.calls, 1)
}

func TestLooperLegacyGenericInvalidRequestDoesNotRetryPortable(t *testing.T) {
	testLooperLegacyOpaqueDoesNotRetry(t, &openai.Error{StatusCode: http.StatusBadRequest, Type: "invalid_request_error", Message: "encrypted input was invalid"})
}

func TestLooperLegacyRateLimitDoesNotRetryPortable(t *testing.T) {
	testLooperLegacyOpaqueDoesNotRetry(t, &openai.Error{StatusCode: http.StatusTooManyRequests, Code: "rate_limit_exceeded", Message: "slow down"})
}

func TestLooperLegacyAuthenticationFailureDoesNotRetryPortable(t *testing.T) {
	testLooperLegacyOpaqueDoesNotRetry(t, &openai.Error{StatusCode: http.StatusUnauthorized, Message: "unauthorized"})
}

func TestLooperLegacyServerFailureDoesNotRetryPortable(t *testing.T) {
	testLooperLegacyOpaqueDoesNotRetry(t, &openai.Error{StatusCode: http.StatusInternalServerError, Message: "server failure"})
}

func TestLooperLegacyOpaqueRejectionRequiresPortableContext(t *testing.T) {
	legacyReplay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{compactionReplayInput("compaction-legacy", "sealed", "")})
	require.NoError(t, err)

	mock := mockResponseError(&openai.Error{StatusCode: http.StatusBadRequest, Param: "input.encrypted_content"})
	loop := testLooper(mock)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "current", make(chan ChatResponse, 1))

	close(input)

	err = loop.Loop(t.Context(), input, sessionEntries([]SessionEntry{{ReplayInput: legacyReplay}}), discardSession, make(chan os.Signal, 1))

	var missing *MissingPortableContextError
	require.ErrorAs(t, err, &missing)
	require.Len(t, mock.calls, 1)
}

func TestLoadSessionKeepsPortableEntriesAfterFirstPortableError(t *testing.T) {
	origin := ProviderOrigin{ProviderID: "openai", Route: "route", ModelID: "model", AuthenticationEpoch: "epoch"}
	bad, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{compactionReplayInput("compaction-1", "sealed", "")})
	require.NoError(t, err)
	good, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputMessage(responses.EasyInputMessageRoleUser, "after error", "")})
	require.NoError(t, err)

	history, _, err := loadSession(sessionEntries([]SessionEntry{{Origin: &origin, ReplayInput: bad}, {Origin: &origin, ReplayInput: good}}), origin)
	require.NoError(t, err)
	require.Error(t, history.portableError)
	require.Len(t, history.portable, 1)
	require.JSONEq(t, `{"content":"after error","role":"user","type":"message"}`, marshalJSON(t, history.portable[0]))
}

func TestLoadSessionReadableCompactionReplacesCrossEntryPortablePrefix(t *testing.T) {
	source := ProviderOrigin{ProviderID: "source", Route: "source-route", ModelID: "source-model", AuthenticationEpoch: "source-epoch"}
	destination := ProviderOrigin{ProviderID: "destination", Route: "destination-route", ModelID: "destination-model", AuthenticationEpoch: "destination-epoch"}
	oldPrefix, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputMessage(responses.EasyInputMessageRoleUser, "old prefix", "")})
	require.NoError(t, err)
	checkpoint, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{compactionReplayInput("compaction-1", "sealed", "checkpoint with retained recent context")})
	require.NoError(t, err)
	tail, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputMessage(responses.EasyInputMessageRoleAssistant, "post-compaction tail", "final_answer")})
	require.NoError(t, err)

	history, _, err := loadSession(sessionEntries([]SessionEntry{
		{Origin: &source, ReplayInput: oldPrefix},
		{Origin: &source, ReplayInput: checkpoint},
		{Origin: &source, ReplayInput: tail},
	}), destination)
	require.NoError(t, err)

	portableJSON := marshalJSON(t, history.portable)

	replayJSON := marshalJSON(t, history.replay)
	for _, serialized := range []string{portableJSON, replayJSON} {
		require.NotContains(t, serialized, "old prefix")
		require.Equal(t, 1, strings.Count(serialized, "checkpoint with retained recent context"))
		require.Less(t, strings.Index(serialized, "checkpoint with retained recent context"), strings.Index(serialized, "post-compaction tail"))
	}

	exact, _, err := loadSession(sessionEntries([]SessionEntry{
		{Origin: &source, ReplayInput: oldPrefix},
		{Origin: &source, ReplayInput: checkpoint},
		{Origin: &source, ReplayInput: tail},
	}), source)
	require.NoError(t, err)
	require.Contains(t, marshalJSON(t, exact.replay), "old prefix")
	require.Contains(t, marshalJSON(t, exact.replay), "sealed")
}

func TestLoadSessionReadableCompactionSupersedesOlderMissingContext(t *testing.T) {
	source := ProviderOrigin{ProviderID: "source", Route: "source-route", ModelID: "source-model", AuthenticationEpoch: "source-epoch"}
	destination := ProviderOrigin{ProviderID: "destination", Route: "destination-route", ModelID: "destination-model", AuthenticationEpoch: "destination-epoch"}
	oldPrefix, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputMessage(responses.EasyInputMessageRoleUser, "old prefix", "")})
	require.NoError(t, err)
	encryptedOnly, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{compactionReplayInput("encrypted-only", "sealed-old", "")})
	require.NoError(t, err)
	checkpoint, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{compactionReplayInput("readable", "sealed-new", "checkpoint with retained recent context")})
	require.NoError(t, err)
	tail, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputMessage(responses.EasyInputMessageRoleAssistant, "post-compaction tail", "final_answer")})
	require.NoError(t, err)

	history, _, err := loadSession(sessionEntries([]SessionEntry{
		{Origin: &source, ReplayInput: oldPrefix},
		{Origin: &source, ReplayInput: encryptedOnly},
		{Origin: &source, ReplayInput: checkpoint},
		{Origin: &source, ReplayInput: tail},
	}), destination)
	require.NoError(t, err)
	serialized := marshalJSON(t, history.replay)
	require.NotContains(t, serialized, "old prefix")
	require.NotContains(t, serialized, "encrypted-only")
	require.Equal(t, 1, strings.Count(serialized, "checkpoint with retained recent context"))
	require.Equal(t, 1, strings.Count(serialized, "post-compaction tail"))
	require.Less(t, strings.Index(serialized, "checkpoint with retained recent context"), strings.Index(serialized, "post-compaction tail"))
}

func TestLoadSessionRejectsUnsupersededLatestMissingContext(t *testing.T) {
	source := ProviderOrigin{ProviderID: "source", Route: "source-route", ModelID: "source-model", AuthenticationEpoch: "source-epoch"}
	destination := ProviderOrigin{ProviderID: "destination", Route: "destination-route", ModelID: "destination-model", AuthenticationEpoch: "destination-epoch"}
	replay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{compactionReplayInput("latest-encrypted-only", "sealed", "")})
	require.NoError(t, err)

	_, _, err = loadSession(sessionEntries([]SessionEntry{{Origin: &source, ReplayInput: replay}}), destination)

	var missing *MissingPortableContextError
	require.ErrorAs(t, err, &missing)
	require.Equal(t, "latest-encrypted-only", missing.CompactionID)
	require.ErrorContains(t, err, "project session entry 1 portable replay")
}

func TestLoadSessionUsesBoundLegacyOpaqueOnlyForExactOrigin(t *testing.T) {
	origin := ProviderOrigin{ProviderID: "work", Route: "route", ModelID: "model", AuthenticationEpoch: "epoch"}
	legacyReplay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-legacy", "readable", "sealed")})
	require.NoError(t, err)
	dispositionReplay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputMessage(responses.EasyInputMessageRoleAssistant, "migration complete", "final_answer")})
	require.NoError(t, err)

	entries := []SessionEntry{
		{ReplayInput: legacyReplay},
		{Origin: &origin, LegacyReplay: legacyReplayOpaqueBound, ReplayInput: dispositionReplay},
	}

	history, _, err := loadSession(sessionEntries(entries), origin)
	require.NoError(t, err)
	require.Contains(t, marshalJSON(t, history.replay), "sealed")
	require.False(t, history.legacyOpaque)

	destination := origin
	destination.AuthenticationEpoch = "other-epoch"
	history, _, err = loadSession(sessionEntries(entries), destination)
	require.NoError(t, err)
	require.NotContains(t, marshalJSON(t, history.replay), "sealed")
	require.Contains(t, marshalJSON(t, history.replay), "readable")
	require.False(t, history.legacyOpaque)
}

func TestLoadSessionUsesDurablePortableLegacyDisposition(t *testing.T) {
	origin := ProviderOrigin{ProviderID: "work", Route: "route", ModelID: "model", AuthenticationEpoch: "epoch"}
	legacyReplay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-legacy", "readable", "sealed")})
	require.NoError(t, err)

	history, _, err := loadSession(sessionEntries([]SessionEntry{
		{ReplayInput: legacyReplay},
		{Origin: &origin, LegacyReplay: legacyReplayPortable},
	}), origin)
	require.NoError(t, err)
	require.NotContains(t, marshalJSON(t, history.replay), "sealed")
	require.Contains(t, marshalJSON(t, history.replay), "readable")
	require.False(t, history.legacyOpaque)
}

func TestLoadSessionRejectsUnknownLegacyReplayDisposition(t *testing.T) {
	_, _, err := loadSession(sessionEntries([]SessionEntry{{LegacyReplay: "unknown"}}), ProviderOrigin{})
	require.ErrorContains(t, err, "legacy replay disposition")
}

func TestLoadSessionLaterOriginlessOpaqueEntryStartsNewMigration(t *testing.T) {
	origin := ProviderOrigin{ProviderID: "work", Route: "route", ModelID: "model", AuthenticationEpoch: "epoch"}
	earlier, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-earlier", "earlier readable", "earlier-sealed")})
	require.NoError(t, err)
	later, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-later", "later readable", "later-sealed")})
	require.NoError(t, err)

	history, _, err := loadSession(sessionEntries([]SessionEntry{
		{ReplayInput: earlier},
		{Origin: &origin, LegacyReplay: legacyReplayPortable},
		{ReplayInput: later},
	}), origin)
	require.NoError(t, err)
	require.True(t, history.legacyOpaque)
	require.NotContains(t, marshalJSON(t, history.replay), "earlier-sealed")
	require.Contains(t, marshalJSON(t, history.replay), "later-sealed")
}

func TestLoadSessionKeepsPartialPortableEntryAroundCompactionError(t *testing.T) {
	origin := ProviderOrigin{ProviderID: "openai", Route: "route", ModelID: "model", AuthenticationEpoch: "epoch"}
	replay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRoleUser, "before", ""),
		compactionReplayInput("compaction-1", "sealed", ""),
		testInputMessage(responses.EasyInputMessageRoleAssistant, "tail", "final_answer"),
	})
	require.NoError(t, err)

	history, _, err := loadSession(sessionEntries([]SessionEntry{{Origin: &origin, ReplayInput: replay}}), origin)
	require.NoError(t, err)
	require.Error(t, history.portableError)
	require.Len(t, history.portable, 2)
	require.Contains(t, marshalJSON(t, history.portable), "before")
	require.Contains(t, marshalJSON(t, history.portable), "tail")
}

func TestSessionHistoryKeepsCurrentTurnsAfterFirstPortableError(t *testing.T) {
	var history sessionHistory
	history.appendPortable(nil, errors.New("first error"))
	history.appendPortable([]responses.ResponseInputItemUnionParam{testInputMessage(responses.EasyInputMessageRoleAssistant, "later turn", "final_answer")}, nil)

	require.EqualError(t, history.portableError, "first error")
	require.Len(t, history.portable, 1)
	require.JSONEq(t, `{"content":"later turn","phase":"final_answer","role":"assistant","type":"message"}`, marshalJSON(t, history.portable[0]))
}

func discardSession(SessionEntry) error { return nil }

func TestLooperReloadsSessionWithCurrentRuntimeConfig(t *testing.T) {
	replayInput, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputMessage("user", "earlier question", ""),
		testInputMessage("assistant", "old answer", "final_answer"),
		testInputReasoning("rsn-old", "old thought", "encrypted-old"),
	})
	require.NoError(t, err)

	turn := SessionEntry{
		Version:     1,
		Type:        "turn",
		Timestamp:   time.Unix(1, 0).UTC(),
		ResponseID:  "",
		Model:       "old-model",
		ReplayInput: replayInput,
		OutputTrace: nil,
	}

	mock := mockResponses(responseWithMessage("resp-new", "new answer"))
	looper := testLooper(mock)
	looper.SystemPrompt = "current system prompt"
	looper.ReasoningEffort = shared.ReasoningEffort("high")

	var saved []SessionEntry

	output := make(chan ChatResponse, 10)

	interrupts := make(chan os.Signal, 1)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "next question", output)

	close(input)

	err = looper.Loop(context.Background(), input, sessionEntries([]SessionEntry{turn}), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, interrupts)
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("new answer")}, collectResponses(output))
	require.Len(t, mock.calls, 1)

	call := mock.calls[0]
	require.Equal(t, openai.ChatModelGPT5, call.Model)
	require.Equal(t, "current system prompt", call.Instructions.Value)
	require.False(t, call.Store.Value)
	require.True(t, call.ParallelToolCalls.Value)
	require.Len(t, call.ContextManagement, 1)
	require.Equal(t, "compaction", call.ContextManagement[0].Type)
	require.Equal(t, defaultCompactThreshold, call.ContextManagement[0].CompactThreshold.Value)
	require.Equal(t, []responses.ResponseIncludable{reasoningEncryptedContent}, call.Include)
	require.Equal(t, shared.ReasoningEffort("high"), call.Reasoning.Effort)

	items := call.Input.OfInputItemList
	require.Len(t, items, 4)
	require.Contains(t, marshalJSON(t, items[0]), `"content":"earlier question"`)
	require.Contains(t, marshalJSON(t, items[0]), `"role":"user"`)
	require.JSONEq(t, `{"content":"old answer","phase":"final_answer","role":"assistant","type":"message"}`, marshalJSON(t, items[1]))
	require.JSONEq(t, `{"encrypted_content":"encrypted-old","id":"rsn-old","summary":[{"text":"old thought","type":"summary_text"}],"type":"reasoning"}`, marshalJSON(t, items[2]))
	require.Contains(t, marshalJSON(t, items[3]), `"content":"next question"`)
	require.Contains(t, marshalJSON(t, items[3]), `"role":"user"`)

	_, savedTurns, err := loadSession(sessionEntries(saved), looper.Origin)
	require.NoError(t, err)
	require.Len(t, savedTurns, 1)
	require.Equal(t, "turn", savedTurns[0].Type)
	decoded, err := ReplayInputToParams(savedTurns[0].ReplayInput)
	require.NoError(t, err)
	require.Contains(t, marshalJSON(t, decoded[0]), `"content":"next question"`)
	require.Contains(t, marshalJSON(t, decoded[0]), `"role":"user"`)
}

func TestLooperSendsAndReplaysDeveloperPromptInput(t *testing.T) {
	mock := mockResponses(responseWithMessage("resp-final", "done"))
	looper := testLooper(mock)
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleDeveloper, "keep this rule", output)

	close(input)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.JSONEq(t, `{"content":"keep this rule","role":"developer","type":"message"}`, marshalJSON(t, mock.calls[0].Input.OfInputItemList[0]))
	require.Len(t, saved, 1)
	require.JSONEq(t, `{"content":"keep this rule","role":"developer","type":"message"}`, string(saved[0].ReplayInput[0]))

	history, _, err := loadSession(sessionEntries(saved), looper.Origin)
	require.NoError(t, err)
	require.JSONEq(t, `{"content":"keep this rule","role":"developer","type":"message"}`, marshalJSON(t, history.replay[0]))
}

func TestLooperPromptInputShellCommandExpansion(t *testing.T) {
	for _, tc := range []struct {
		enabled bool
		want    string
	}{
		{enabled: false, want: "before !`printf hello` after"},
		{enabled: true, want: "before hello after"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			mock := mockResponses(responseWithMessage("resp-final", "done"))
			looper := testLooper(mock)
			looper.expandInputPrompts = tc.enabled
			looper.promptExpansion = testPromptExpansionEnvironment(t)
			output := make(chan ChatResponse, 10)
			turn, _, interrupted, err := looper.runTurn(context.Background(), output, nil, nil, nil, false, nil, false, false, testPromptInput(PromptInputRoleUser, "before !`printf hello` after", nil))

			require.NoError(t, err)
			require.False(t, interrupted)

			wantJSON := fmt.Sprintf(`{"content":%q,"role":"user","type":"message"}`, tc.want)
			require.JSONEq(t, wantJSON, marshalJSON(t, mock.calls[0].Input.OfInputItemList[0]))
			require.JSONEq(t, wantJSON, string(turn.ReplayInput[0]))
		})
	}
}

func TestLooperClosesPromptResponseChannelAfterTurn(t *testing.T) {
	mock := mockResponses(responseWithMessage("resp-final", "done"))
	looper := testLooper(mock)
	output := make(chan ChatResponse, 1)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("done")}, collectResponses(output))
}

func TestLooperAppendsOneSessionEntryPerCompletedTurn(t *testing.T) {
	mock := mockResponses(
		responseWithMessage("resp-one", "first answer"),
		responseWithMessage("resp-two", "second answer"),
	)
	looper := testLooper(mock)
	store := testSessionStore()
	firstOutput := make(chan ChatResponse, 10)
	secondOutput := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 2)
	input <- testPromptInput(PromptInputRoleUser, "first question", firstOutput)

	input <- testPromptInput(PromptInputRoleUser, "second question", secondOutput)

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		return store.appendEntry(&entry)
	}, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("first answer")}, collectResponses(firstOutput))
	require.Equal(t, []ChatResponse{assistantMessage("second answer")}, collectResponses(secondOutput))
	require.Len(t, store.entries, 2)
	require.Equal(t, "resp-one", store.entries[0].ResponseID)
	require.Equal(t, "resp-two", store.entries[1].ResponseID)
	require.Len(t, store.saves, 2)
	require.Len(t, store.saves[0], 1)
	require.Len(t, store.saves[1], 2)
}

func TestSessionEntryRecordsResolvedOrigin(t *testing.T) {
	loop := testLooper(mockResponses(responseWithMessage("resp", "done")))
	loop.Origin = ProviderOrigin{ProviderID: "work", Route: "responses:https://work.example/v1", ModelID: "worker", AuthenticationEpoch: "epoch-work"}
	output := make(chan ChatResponse, 1)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)

	var entry SessionEntry

	require.NoError(t, loop.Loop(t.Context(), input, emptySession(), func(got SessionEntry) error { entry = got; return nil }, make(chan os.Signal, 1)))
	require.Equal(t, &loop.Origin, entry.Origin)
	require.NotSame(t, &loop.Origin, entry.Origin)
}

func TestActiveTurnCheckpointRecordsResolvedOrigin(t *testing.T) {
	sink := new(mockCheckpointSink)
	loop := testLooper(mockResponses(responseWithMessage("resp", "done")))
	loop.Origin = ProviderOrigin{ProviderID: "work", Route: "responses:https://work.example/v1", ModelID: "worker", AuthenticationEpoch: "epoch-work"}
	loop.CheckpointSink = sink
	output := make(chan ChatResponse, 1)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)

	require.NoError(t, loop.Loop(t.Context(), input, emptySession(), discardSession, make(chan os.Signal, 1)))

	calls := sink.snapshot()
	require.NotEmpty(t, calls)
	require.Equal(t, &loop.Origin, calls[0].checkpoint.Origin)
	require.NotSame(t, &loop.Origin, calls[0].checkpoint.Origin)
}

func TestLoopClosesPromptResponsesWhenSessionLoadFails(t *testing.T) {
	looper := testLooper(mockResponses())
	output := make(chan ChatResponse, 1)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)

	badSession := func(yield func(SessionEntry, error) bool) {
		yield(SessionEntry{Version: 1, Type: "turn", Timestamp: time.Time{}, ResponseID: "", Model: "", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":""}`)}, OutputTrace: nil}, nil)
	}

	err := looper.Loop(context.Background(), input, badSession, discardSession, make(chan os.Signal, 1))

	require.Error(t, err)

	select {
	case _, ok := <-output:
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("prompt response channel was not closed")
	}
}

func TestLooperBuildParamsUsesConfiguredCompactThreshold(t *testing.T) {
	looper := emptyTestLooper()
	looper.CompactThreshold = 12345

	params := looper.buildParams(nil)

	require.Len(t, params.ContextManagement, 1)
	require.Equal(t, "compaction", params.ContextManagement[0].Type)
	require.Equal(t, int64(12345), params.ContextManagement[0].CompactThreshold.Value)
	require.False(t, params.Store.Value)
}

func TestLooperBuildParamsIncludesHostedWebSearchTool(t *testing.T) {
	looper := emptyTestLooper()
	looper.Tools = map[string]looperTool{"websearch": webSearchTool()}

	params := looper.buildParams(nil)

	require.Len(t, params.Tools, 1)
	require.Contains(t, marshalJSON(t, params.Tools), `"type":"web_search"`)
}

func TestLooperBuildParamsSortsToolsByName(t *testing.T) {
	looper := emptyTestLooper()
	looper.Tools = map[string]looperTool{
		"read":        testLooperTool("read"),
		"apply_patch": testLooperTool("apply_patch"),
		"bash":        testLooperTool("bash"),
	}

	params := looper.buildParams(nil)

	require.Len(t, params.Tools, 3)
	require.Equal(t, "apply_patch", params.Tools[0].OfFunction.Name)
	require.Equal(t, "bash", params.Tools[1].OfFunction.Name)
	require.Equal(t, "read", params.Tools[2].OfFunction.Name)
}

func TestLooperBuildParamsIncludesConfiguredVerbosity(t *testing.T) {
	looper := emptyTestLooper()
	looper.Verbosity = "low"

	params := looper.buildParams(nil)

	require.Equal(t, responses.ResponseTextConfigVerbosityLow, params.Text.Verbosity)
}

func TestLooperPersistsAndReplaysCompactionItems(t *testing.T) {
	mock := mockResponses(
		responseWithCompactionAndMessage("resp-compact", "encrypted-compact", "answer"),
		completedBackupResponse("generated readable backup"),
	)
	looper := testLooper(mock)
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)

	interrupts := make(chan os.Signal, 1)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, interrupts)
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("answer")}, collectResponses(output))

	history, turns, err := loadSession(sessionEntries(saved), looper.Origin)
	require.NoError(t, err)
	require.Len(t, turns, 1)
	require.Len(t, turns[0].ReplayInput, 3)
	require.Len(t, history.replay, 3)
	require.JSONEq(t, `{"content":"generated readable backup","encrypted_content":"encrypted-compact","id":"resp-compact-compaction","type":"compaction"}`, marshalJSON(t, history.replay[1]))
}

func TestLooperPersistsReadableBackupBeforeAutomaticCompactionPrune(t *testing.T) {
	sink := &mockCheckpointSink{}
	compaction := testCompactionOutputItem("resp-compact-compaction", "encrypted-compact")
	compaction.Summary = []responses.ResponseReasoningItemSummary{testReasoningSummary("summary of prior context")}

	mock := mockResponses()
	mock.newFunc = func(_ context.Context, params *responses.ResponseNewParams) (*responses.Response, error) {
		switch len(mock.calls) {
		case 1:
			return testResponse("resp-compact", []responses.ResponseOutputItemUnion{compaction}), nil
		case 2:
			return completedBackupResponse("generated readable backup"), nil
		case 3:
			calls := sink.snapshot()
			require.Len(t, calls, 2)
			require.Equal(t, "provider", calls[1].name)
			require.Contains(t, string(calls[1].checkpoint.ReplayInput[1]), `"encrypted_content":"encrypted-compact"`)
			require.Contains(t, string(calls[1].checkpoint.ReplayInput[1]), `"content":"generated readable backup"`)
			require.JSONEq(t, `{"encrypted_content":"encrypted-compact","id":"resp-compact-compaction","type":"compaction"}`, marshalJSON(t, params.Input.OfInputItemList[0]))

			return responseWithMessage("resp-final", "answer"), nil
		default:
			return nil, fmt.Errorf("unexpected response call %d", len(mock.calls))
		}
	}
	looper := testLooper(mock)
	looper.CheckpointSink = sink
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("answer")}, collectResponses(output))
	require.Len(t, mock.calls, 3)
	require.Len(t, saved, 1)
	require.Len(t, saved[0].ReplayInput, 3)
	require.JSONEq(t, `{"content":"generated readable backup","encrypted_content":"encrypted-compact","id":"resp-compact-compaction","type":"compaction"}`, string(saved[0].ReplayInput[1]))
}

func TestLooperReadableBackupFailureRetainsPortableHistory(t *testing.T) {
	sink := &mockCheckpointSink{}
	mock := mockResponses()
	errContext := contextLengthExceededError()
	mock.newFunc = func(_ context.Context, _ *responses.ResponseNewParams) (*responses.Response, error) {
		if len(mock.calls) == 1 {
			return nil, errContext
		}

		return nil, errors.New("summary unavailable")
	}
	mock.compactResponses = []*responses.CompactedResponse{compactedResponse("cmp-old", "encrypted-old")}
	looper := testLooper(mock)
	looper.CheckpointSink = sink
	output := make(chan ChatResponse, 10)
	replayInput, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRoleUser, "durable prompt", ""),
	})
	require.NoError(t, err)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "current prompt", output)

	close(input)

	var saved []SessionEntry

	err = looper.Loop(context.Background(), input, sessionEntries([]SessionEntry{{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), ReplayInput: replayInput}}), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, make(chan os.Signal, 1))

	require.NotContains(t, err.Error(), "summary unavailable")
	require.ErrorContains(t, err, `provider "openai" request failed`)
	require.Empty(t, collectResponses(output))
	require.Len(t, mock.calls, 2)
	require.Contains(t, marshalJSON(t, mock.calls[0].Input.OfInputItemList), "durable prompt")
	require.Contains(t, marshalJSON(t, mock.calls[0].Input.OfInputItemList), "current prompt")
	require.Contains(t, marshalJSON(t, mock.calls[1].Input.OfInputItemList), "durable prompt")
	require.Len(t, mock.compactCalls, 1)
	require.Empty(t, saved)
	require.Len(t, sink.snapshot(), 1)
}

func TestReadableCompactionBackupUsesPortableNoToolsRequest(t *testing.T) {
	mock := mockResponses(completedBackupResponse("first"))
	mock.responses[0].Output = append(mock.responses[0].Output,
		testMessageOutputItem("resp-backup-second", "", " second"),
		testReasoningOutputItem("resp-backup-reasoning", "sealed-result", "ignored"),
	)
	looper := testLooper(mock)
	looper.ReasoningEffort = shared.ReasoningEffortHigh
	looper.ResponseFormat = responses.ResponseFormatTextConfigParamOfJSONSchema("result", map[string]any{"type": "object"})
	looper.Tools = map[string]looperTool{"read": testLooperTool("read")}
	history := []responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRoleUser, "question", ""),
		testInputReasoning("reasoning-1", "portable reasoning", "sealed-input"),
	}

	backup, err := looper.readableCompactionBackup(context.Background(), history)

	require.NoError(t, err)
	require.Equal(t, "first second", backup)
	require.Len(t, mock.calls, 1)
	call := mock.calls[0]
	require.Equal(t, looper.Model, call.Model)
	require.False(t, call.Store.Value)
	require.Equal(t, readableCompactionBackupInstruction, call.Instructions.Value)
	require.Empty(t, call.Tools)
	require.Empty(t, call.ContextManagement)
	require.Empty(t, call.Include)
	require.Equal(t, shared.ReasoningParam{}, call.Reasoning)
	require.Equal(t, responses.ResponseTextConfigParam{}, call.Text)
	requestJSON := marshalJSON(t, call)
	require.NotContains(t, requestJSON, "sealed-input")
	require.Contains(t, requestJSON, "portable reasoning")
}

func TestReadableCompactionBackupRejectsInvalidResponse(t *testing.T) {
	tests := []struct {
		name     string
		response *responses.Response
		want     string
	}{
		{
			name: "incomplete",
			response: func() *responses.Response {
				response := responseWithMessage("resp-backup", "partial")
				response.Status = responses.ResponseStatusIncomplete

				return response
			}(),
			want: `readable compaction backup response status "incomplete"`,
		},
		{
			name:     "no assistant output text",
			response: testResponse("resp-backup", []responses.ResponseOutputItemUnion{testReasoningOutputItem("reasoning", "sealed", "summary")}),
			want:     "readable compaction backup response has no assistant output text",
		},
	}
	tests[1].response.Status = responses.ResponseStatusCompleted

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			looper := testLooper(mockResponses(tt.response))

			_, err := looper.readableCompactionBackup(t.Context(), []responses.ResponseInputItemUnionParam{
				testInputMessage(responses.EasyInputMessageRoleUser, "question", ""),
			})

			require.EqualError(t, err, tt.want)
		})
	}
}

func TestLooperAutomaticCompactionKindsReceiveReadableBackup(t *testing.T) {
	compactionSummary := testCompactionOutputItem("cmp-summary", "encrypted-summary")
	compactionSummary.Type = "compaction_summary"

	tests := []struct {
		name   string
		output []responses.ResponseOutputItemUnion
	}{
		{name: "compaction summary", output: []responses.ResponseOutputItemUnion{compactionSummary}},
		{name: "multiple compactions", output: []responses.ResponseOutputItemUnion{
			testCompactionOutputItem("cmp-1", "encrypted-1"),
			testCompactionOutputItem("cmp-2", "encrypted-2"),
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := mockResponses(completedBackupResponse("generated readable backup"))
			looper := testLooper(mock)

			var (
				record SessionEntry
				replay []responses.ResponseInputItemUnionParam
			)

			compactionOnly, err := looper.appendProviderReplay(t.Context(), []responses.ResponseInputItemUnionParam{
				testInputMessage(responses.EasyInputMessageRoleUser, "question", ""),
			}, &record, &replay, testResponse("resp-compact", tt.output))

			require.NoError(t, err)
			require.True(t, compactionOnly)
			require.Len(t, mock.calls, 1)
			require.Len(t, replay, len(tt.output))

			for i := range replay {
				require.Contains(t, marshalJSON(t, replay[i]), `"content":"generated readable backup"`)
			}
		})
	}
}

func TestLooperCompactsAndRetriesContextLengthExceeded(t *testing.T) {
	replayInput, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		compactionReplayInput("cmp-prior", "sealed-prior", "private-content-value"),
		testInputMessage(responses.EasyInputMessageRole("user"), "old question", ""),
		testInputMessage(responses.EasyInputMessageRole("assistant"), "old answer", ""),
	})
	require.NoError(t, err)

	mock := mockResponses()
	mock.compactResponses = []*responses.CompactedResponse{compactedResponse("cmp-old", "encrypted-old")}
	contextErr := contextLengthExceededError()
	mock.newFunc = func(_ context.Context, _ *responses.ResponseNewParams) (*responses.Response, error) {
		switch len(mock.calls) {
		case 1:
			return nil, contextErr
		case 2:
			return completedBackupResponse("generated readable backup"), nil
		}

		return responseWithMessage("resp-final", "answer"), nil
	}
	looper := testLooper(mock)
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "new question", output)

	close(input)

	var saved []SessionEntry

	err = looper.Loop(context.Background(), input, sessionEntries([]SessionEntry{{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), Origin: new(looper.Origin), ReplayInput: replayInput}}), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("answer")}, collectResponses(output))
	require.Len(t, mock.compactCalls, 1)
	compactInput := marshalJSON(t, mock.compactCalls[0].Input.OfResponseInputItemArray)
	require.Contains(t, compactInput, "sealed-prior")
	require.NotContains(t, compactInput, "private-content-value")
	require.NotContains(t, compactInput, "new question")
	require.Len(t, mock.calls, 3)
	retryInput := marshalJSON(t, mock.calls[2].Input.OfInputItemList)
	require.Contains(t, retryInput, `"type":"compaction"`)
	require.Contains(t, retryInput, "new question")
	require.Len(t, saved, 1)
	require.Contains(t, string(saved[0].ReplayInput[0]), `"type":"compaction"`)
}

func TestLooperProgressiveCompactionKeepsToolCallWithOutput(t *testing.T) {
	toolCall := functionCallReplayInput("tool-1", "call-1", "lookup", `{"q":"x"}`)
	toolOutputParam := toolCallOutput("call-1", TextToolResult("result"))
	toolOutput := responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &toolOutputParam}
	replayInput, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRole("user"), "old question", ""),
		toolCall,
		toolOutput,
	})
	require.NoError(t, err)

	mock := mockResponses()
	mock.compactResponses = []*responses.CompactedResponse{
		compactedResponse("cmp-one", "encrypted-one"),
		compactedResponse("cmp-two", "encrypted-two"),
	}
	contextErr := contextLengthExceededError()
	mock.newFunc = func(_ context.Context, _ *responses.ResponseNewParams) (*responses.Response, error) {
		switch len(mock.calls) {
		case 1, 3:
			return nil, contextErr
		case 2, 4:
			return completedBackupResponse("generated readable backup"), nil
		}

		return responseWithMessage("resp-final", "answer"), nil
	}
	looper := testLooper(mock)
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "new question", output)

	close(input)

	err = looper.Loop(context.Background(), input, sessionEntries([]SessionEntry{{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), Origin: new(looper.Origin), ReplayInput: replayInput}}), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("answer")}, collectResponses(output))
	require.Len(t, mock.compactCalls, 2)
	secondCompactInput := marshalJSON(t, mock.compactCalls[1].Input.OfResponseInputItemArray)
	require.Contains(t, secondCompactInput, `"type":"function_call"`)
	require.Contains(t, secondCompactInput, `"type":"function_call_output"`)
	require.Contains(t, secondCompactInput, `"call_id":"call-1"`)
}

func TestLooperDoesNotCompactUnansweredToolCall(t *testing.T) {
	toolCall := functionCallReplayInput("tool-1", "call-1", "lookup", `{"q":"x"}`)
	replayInput, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRole("user"), "old question", ""),
		toolCall,
	})
	require.NoError(t, err)

	mock := mockResponses()
	mock.compactResponses = []*responses.CompactedResponse{compactedResponse("cmp-old", "encrypted-old")}
	contextErr := contextLengthExceededError()
	mock.newFunc = func(_ context.Context, _ *responses.ResponseNewParams) (*responses.Response, error) {
		switch len(mock.calls) {
		case 1:
			return nil, contextErr
		case 2:
			return completedBackupResponse("generated readable backup"), nil
		}

		return responseWithMessage("resp-final", "answer"), nil
	}
	looper := testLooper(mock)
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "new question", output)

	close(input)

	err = looper.Loop(context.Background(), input, sessionEntries([]SessionEntry{{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), Origin: new(looper.Origin), ReplayInput: replayInput}}), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("answer")}, collectResponses(output))
	require.Len(t, mock.compactCalls, 1)
	compactInput := marshalJSON(t, mock.compactCalls[0].Input.OfResponseInputItemArray)
	require.Contains(t, compactInput, "old question")
	require.NotContains(t, compactInput, `"type":"function_call"`)
}

func TestLooperDoesNotCompactOtherProviderErrors(t *testing.T) {
	mock := mockResponseError(errors.New("provider unavailable"))
	looper := testLooper(mock)
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.Error(t, err)
	require.Empty(t, mock.compactCalls)
}

func TestLooperPersistsAndReplaysWebSearchCalls(t *testing.T) {
	mock := mockResponses(
		responseWithWebSearchAndMessage("resp-search", "golang release", "answer with citation"),
		responseWithMessage("resp-next", "next answer"),
	)
	looper := testLooper(mock)
	looper.Tools = map[string]looperTool{"websearch": webSearchTool()}
	output := make(chan ChatResponse, 10)
	nextOutput := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 2)
	input <- testPromptInput(PromptInputRoleUser, "search", output)

	input <- testPromptInput(PromptInputRoleUser, "continue", nextOutput)

	close(input)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("answer with citation")}, collectResponses(output))
	require.Equal(t, []ChatResponse{assistantMessage("next answer")}, collectResponses(nextOutput))
	require.Len(t, saved, 2)
	require.Len(t, saved[0].ReplayInput, 3)
	require.JSONEq(t, `{"action":{"queries":["golang release"],"query":"golang release","type":"search"},"id":"resp-search-web","status":"completed","type":"web_search_call"}`, string(saved[0].ReplayInput[1]))

	require.Len(t, mock.calls, 2)
	require.Contains(t, marshalJSON(t, mock.calls[1].Input.OfInputItemList), `"type":"web_search_call"`)
	require.Contains(t, marshalJSON(t, mock.calls[1].Input.OfInputItemList), `"query":"golang release"`)
}

func TestWebSearchOutputWithEmptyActionTypeIsTraceOnly(t *testing.T) {
	var action responses.ResponseOutputItemUnionAction

	var webSearch responses.ResponseOutputItemUnion

	webSearch.ID = "resp-search-web"
	webSearch.Type = "web_search_call"
	webSearch.Status = "completed"
	webSearch.Action = action
	mock := mockResponses(testResponse("resp-search", []responses.ResponseOutputItemUnion{
		webSearch,
		testMessageOutputItem("resp-search-msg", "", "done"),
	}))
	looper := testLooper(mock)
	looper.Tools = map[string]looperTool{"websearch": webSearchTool()}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "search", output)

	close(input)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("done")}, collectResponses(output))
	require.Len(t, saved, 1)
	require.Len(t, saved[0].ReplayInput, 2)
	require.NotContains(t, string(saved[0].ReplayInput[1]), "web_search_call")
	require.Len(t, saved[0].OutputTrace, 1)
	require.Contains(t, string(saved[0].OutputTrace[0]), `"web_search_call"`)
}

func TestLooperInjectsCompactionSteering(t *testing.T) {
	mock := mockResponses(
		responseWithCompactionAndMessage("resp-compact", "encrypted-compact", "answer"),
		completedBackupResponse("generated readable backup"),
		responseWithMessage("resp-next", "next answer"),
	)
	looper := testLooper(mock)
	looper.CompactionSteering = "Use the compacted context carefully."
	output := make(chan ChatResponse, 10)
	nextOutput := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 2)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	input <- testPromptInput(PromptInputRoleUser, "next question", nextOutput)

	close(input)

	interrupts := make(chan os.Signal, 1)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, interrupts)
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("answer")}, collectResponses(output))
	require.Equal(t, []ChatResponse{assistantMessage("next answer")}, collectResponses(nextOutput))
	require.Len(t, saved, 2)
	require.Len(t, saved[0].ReplayInput, 4)
	require.JSONEq(t, `{"content":"Use the compacted context carefully.","role":"developer","type":"message"}`, string(saved[0].ReplayInput[3]))

	require.Len(t, mock.calls, 3)
	items := mock.calls[2].Input.OfInputItemList
	require.Len(t, items, 4)
	require.JSONEq(t, `{"encrypted_content":"encrypted-compact","id":"resp-compact-compaction","type":"compaction"}`, marshalJSON(t, items[0]))
	require.Contains(t, marshalJSON(t, items[1]), `"role":"assistant"`)
	require.JSONEq(t, `{"content":"Use the compacted context carefully.","role":"developer","type":"message"}`, marshalJSON(t, items[2]))
	require.Contains(t, marshalJSON(t, items[3]), `"content":"next question"`)
}

func TestPruneHistoryBeforeLatestCompaction(t *testing.T) {
	items := []responses.ResponseInputItemUnionParam{
		responses.ResponseInputItemParamOfMessage("old", responses.EasyInputMessageRole("user")),
		responses.ResponseInputItemParamOfCompaction("first"),
		responses.ResponseInputItemParamOfMessage("middle", responses.EasyInputMessageRole("user")),
		responses.ResponseInputItemParamOfCompaction("second"),
		responses.ResponseInputItemParamOfMessage("new", responses.EasyInputMessageRole("user")),
	}

	pruned := pruneHistoryBeforeLatestCompaction(items)

	require.Len(t, pruned, 2)
	require.JSONEq(t, `{"encrypted_content":"second","type":"compaction"}`, marshalJSON(t, pruned[0]))
	require.Contains(t, marshalJSON(t, pruned[1]), `"content":"new"`)
}

func TestCheckpointBeforeFirstProviderCall(t *testing.T) {
	sink := &mockCheckpointSink{}
	mock := mockResponseFunc(func(_ context.Context, _ *responses.ResponseNewParams) (*responses.Response, error) {
		calls := sink.snapshot()
		require.Len(t, calls, 1)
		require.Equal(t, "start", calls[0].name)
		require.NotEmpty(t, calls[0].checkpoint.TurnID)
		require.JSONEq(t, `{"content":"hello","role":"user","type":"message"}`, string(calls[0].checkpoint.ReplayInput[0]))

		return responseWithMessage("resp-final", "done"), nil
	})
	looper := testLooper(mock)
	looper.CheckpointSink = sink
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "hello", output)

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("done")}, collectResponses(output))
}

func TestCheckpointProviderResponseOpenCallsBeforeToolDispatch(t *testing.T) {
	sink := &mockCheckpointSink{}
	mock := mockResponses(
		responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "read", `{"filePath":"README.md"}`)}),
		responseWithMessage("resp-final", "done"),
	)
	looper := testLooper(mock)
	looper.CheckpointSink = sink
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "read", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}
	tool := testLooperTool("read")
	tool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
		calls := sink.snapshot()
		require.GreaterOrEqual(t, len(calls), 2)
		provider := calls[1]
		require.Equal(t, "provider", provider.name)
		require.Equal(t, "resp-tool", provider.checkpoint.ResponseID)
		require.Len(t, provider.checkpoint.OpenFunctionCalls, 1)
		require.Equal(t, "call-1", provider.checkpoint.OpenFunctionCalls[0].CallID)
		require.Equal(t, "read", provider.checkpoint.OpenFunctionCalls[0].Name)
		require.JSONEq(t, `{"filePath":"README.md"}`, string(provider.checkpoint.OpenFunctionCalls[0].Arguments))

		return TextToolResult("contents"), nil
	}
	looper.Tools = map[string]looperTool{"read": tool}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "read", output)

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("done")}, collectResponses(output))
}

func TestLooperPersistsReadableBackupBeforeContextLengthRetry(t *testing.T) {
	sink := &mockCheckpointSink{}
	providerCalls := 0
	mock := mockResponseFunc(func(_ context.Context, params *responses.ResponseNewParams) (*responses.Response, error) {
		providerCalls++
		switch providerCalls {
		case 1:
			return nil, contextLengthExceededError()
		case 2:
			return completedBackupResponse("generated readable backup"), nil
		case 3:
			calls := sink.snapshot()
			require.Len(t, calls, 2)
			compacted := calls[1]
			require.Equal(t, "provider", compacted.name)
			require.Len(t, compacted.checkpoint.ReplayInput, 2)
			require.Contains(t, string(compacted.checkpoint.ReplayInput[0]), `"type":"compaction"`)
			require.Contains(t, string(compacted.checkpoint.ReplayInput[0]), `"encrypted_content":"encrypted-old"`)
			require.Contains(t, string(compacted.checkpoint.ReplayInput[0]), `"content":"generated readable backup"`)
			require.Contains(t, string(compacted.checkpoint.ReplayInput[1]), `"content":"new prompt"`)
			require.NotContains(t, marshalJSON(t, params.Input.OfInputItemList), "generated readable backup")

			return responseWithMessage("resp-final", "done"), nil
		default:
			return nil, fmt.Errorf("unexpected response call %d", providerCalls)
		}
	})
	mock.compactResponses = []*responses.CompactedResponse{compactedResponse("cmp-old", "encrypted-old")}
	looper := testLooper(mock)
	looper.CheckpointSink = sink
	output := make(chan ChatResponse, 10)
	replayInput, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRole("user"), "old prompt", ""),
	})
	require.NoError(t, err)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "new prompt", output)

	close(input)

	err = looper.Loop(context.Background(), input, sessionEntries([]SessionEntry{{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), ReplayInput: replayInput}}), discardSession, make(chan os.Signal, 1))
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("done")}, collectResponses(output))
	require.Equal(t, 3, providerCalls)
}

func TestLooperAutomaticBackupAfterContextRecoveryUsesRecoveredHistory(t *testing.T) {
	mock := mockResponses()
	mock.compactResponses = []*responses.CompactedResponse{compactedResponse("cmp-old", "encrypted-old")}
	mock.newFunc = func(_ context.Context, params *responses.ResponseNewParams) (*responses.Response, error) {
		switch len(mock.calls) {
		case 1:
			return nil, contextLengthExceededError()
		case 2:
			return completedBackupResponse("manual readable backup"), nil
		case 3:
			return responseWithCompactionAndMessage("resp-auto", "encrypted-auto", "answer"), nil
		case 4:
			input := marshalJSON(t, params.Input.OfInputItemList)
			require.Contains(t, input, "manual readable backup")
			require.Contains(t, input, "new prompt")
			require.NotContains(t, input, "oversized old prompt")

			return completedBackupResponse("automatic readable backup"), nil
		default:
			return nil, fmt.Errorf("unexpected response call %d", len(mock.calls))
		}
	}
	looper := testLooper(mock)
	output := make(chan ChatResponse, 10)
	replayInput, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRoleUser, "oversized old prompt", ""),
	})
	require.NoError(t, err)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "new prompt", output)

	close(input)

	err = looper.Loop(context.Background(), input, sessionEntries([]SessionEntry{{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), ReplayInput: replayInput}}), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("answer")}, collectResponses(output))
	require.Len(t, mock.calls, 4)
}

func TestLooperCompactionCheckpointFailureStopsContinuation(t *testing.T) {
	errCheckpoint := errors.New("checkpoint unavailable")

	t.Run("automatic continuation", func(t *testing.T) {
		mock := mockResponses(
			testResponse("resp-compact", []responses.ResponseOutputItemUnion{testCompactionOutputItem("cmp-auto", "encrypted-auto")}),
			completedBackupResponse("automatic readable backup"),
		)
		looper := testLooper(mock)
		looper.CheckpointSink = &mockCheckpointSink{providerErr: errCheckpoint}
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- testPromptInput(PromptInputRoleUser, "question", output)

		close(input)

		err := looper.Loop(t.Context(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.ErrorIs(t, err, errCheckpoint)
		require.Len(t, mock.calls, 2)
		require.Empty(t, collectResponses(output))
	})

	t.Run("context recovery retry", func(t *testing.T) {
		mock := mockResponses()
		mock.compactResponses = []*responses.CompactedResponse{compactedResponse("cmp-old", "encrypted-old")}
		mock.newFunc = func(_ context.Context, _ *responses.ResponseNewParams) (*responses.Response, error) {
			switch len(mock.calls) {
			case 1:
				return nil, contextLengthExceededError()
			case 2:
				return completedBackupResponse("manual readable backup"), nil
			default:
				return nil, fmt.Errorf("unexpected compacted retry call %d", len(mock.calls))
			}
		}
		looper := testLooper(mock)
		looper.CheckpointSink = &mockCheckpointSink{providerErr: errCheckpoint}
		output := make(chan ChatResponse, 10)
		replayInput, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
			testInputMessage(responses.EasyInputMessageRoleUser, "old prompt", ""),
		})
		require.NoError(t, err)

		input := make(chan PromptInput, 1)
		input <- testPromptInput(PromptInputRoleUser, "new prompt", output)

		close(input)

		err = looper.Loop(t.Context(), input, sessionEntries([]SessionEntry{{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), ReplayInput: replayInput}}), discardSession, make(chan os.Signal, 1))

		require.ErrorIs(t, err, errCheckpoint)
		require.Len(t, mock.calls, 2)
		require.Len(t, mock.compactCalls, 1)
		require.Empty(t, collectResponses(output))
	})
}

func TestLooperDispatchesToolCalls(t *testing.T) {
	mock := mockResponses(
		responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{
			testFunctionCall("tool-1", "call-1", "first", `{"step":1}`),
			testFunctionCall("tool-2", "call-2", "second", `{"step":2}`),
		}),
		responseWithMessage("resp-final", "done"),
	)

	var (
		callsMu sync.Mutex
		calls   []string
	)

	looper := testLooper(mock)
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{
		{Name: "first", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}},
		{Name: "second", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}},
	}}
	firstTool := testLooperTool("first")
	firstTool.CallReplay = func(_ context.Context, args json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, []responses.ResponseInputItemUnionParam, error) {
		callsMu.Lock()
		defer callsMu.Unlock()

		calls = append(calls, "first:"+string(args))
		developerInput := testInputMessage(responses.EasyInputMessageRoleDeveloper, "first instructions", "")

		return TextToolResult("first-result"), []responses.ResponseInputItemUnionParam{developerInput}, nil
	}
	secondTool := testLooperTool("second")
	secondTool.Call = func(_ context.Context, args json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
		callsMu.Lock()
		defer callsMu.Unlock()

		calls = append(calls, "second:"+string(args))

		return TextToolResult("second-result"), nil
	}
	looper.Tools = map[string]looperTool{
		"first":  firstTool,
		"second": secondTool,
	}

	interrupts := make(chan os.Signal, 1)
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "run tools", output)

	close(input)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, interrupts)
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("done")}, collectResponses(output))
	callsMu.Lock()

	gotCalls := append([]string{}, calls...)
	callsMu.Unlock()
	require.ElementsMatch(t, []string{"first:{\"step\":1}", "second:{\"step\":2}"}, gotCalls)
	require.Len(t, mock.calls, 2)

	second := mock.calls[1].Input.OfInputItemList
	require.Len(t, second, 6)
	require.Equal(t, "function_call_output", *second[3].GetType())
	require.Equal(t, "call-1", *second[3].GetCallID())
	require.Contains(t, marshalJSON(t, second[3]), `"first-result"`)
	require.Equal(t, "function_call_output", *second[4].GetType())
	require.Equal(t, "call-2", *second[4].GetCallID())
	require.Contains(t, marshalJSON(t, second[4]), `"second-result"`)
	require.Equal(t, "message", *second[5].GetType())
	require.Equal(t, "developer", *second[5].GetRole())
	require.Contains(t, marshalJSON(t, second[5]), "first instructions")

	history, _, err := loadSession(sessionEntries(saved), looper.Origin)
	require.NoError(t, err)
	require.Len(t, history.replay, 7)
	require.Equal(t, "function_call", *history.replay[1].GetType())
	require.Equal(t, "function_call", *history.replay[2].GetType())
	require.Equal(t, "function_call_output", *history.replay[3].GetType())
	require.Equal(t, "function_call_output", *history.replay[4].GetType())
	require.Equal(t, "message", *history.replay[5].GetType())
	require.Equal(t, "message", *history.replay[6].GetType())
}

func TestLooperCheckpointsCompletedToolOutputBeforeContinuation(t *testing.T) {
	sink := &mockCheckpointSink{}
	mock := mockResponses()
	mock.newFunc = func(_ context.Context, _ *responses.ResponseNewParams) (*responses.Response, error) {
		if len(mock.calls) == 1 {
			return responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "first", `{"step":1}`)}), nil
		}

		toolCheckpoints := 0

		for _, call := range sink.snapshot() {
			if call.name == "tool" {
				toolCheckpoints++
			}
		}

		require.Positive(t, toolCheckpoints, "tool output checkpoint should be durable before continuation request")

		return responseWithMessage("resp-final", "done"), nil
	}
	looper := testLooper(mock)
	looper.CheckpointSink = sink
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "first", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}
	tool := testLooperTool("first")
	tool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
		return TextToolResult("first-result"), nil
	}
	looper.Tools = map[string]looperTool{"first": tool}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "run tool", output)

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)

	var toolCheckpoints []ActiveTurnCheckpoint

	for _, call := range sink.snapshot() {
		if call.name == "tool" {
			toolCheckpoints = append(toolCheckpoints, call.checkpoint)
		}
	}

	require.NotEmpty(t, toolCheckpoints)
	last := toolCheckpoints[len(toolCheckpoints)-1]
	require.Empty(t, last.OpenFunctionCalls)
	require.Len(t, last.CompletedFunctionOutputs, 1)
	require.Equal(t, "call-1", last.CompletedFunctionOutputs[0].CallID)
	require.Equal(t, "first", last.CompletedFunctionOutputs[0].Name)
	require.Contains(t, marshalJSON(t, last.ReplayInput), `"function_call_output"`)
}

func TestLooperClearsCheckpointAfterCompletedSessionEntry(t *testing.T) {
	sink := &mockCheckpointSink{}
	mock := mockResponses(responseWithMessage("resp-final", "done"))
	looper := testLooper(mock)
	looper.CheckpointSink = sink
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		for _, call := range sink.snapshot() {
			require.NotEqual(t, "clear", call.name, "checkpoint should not clear before session entry is durable")
		}

		saved = append(saved, entry)

		return nil
	}, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Len(t, saved, 1)

	var cleared []string

	for _, call := range sink.snapshot() {
		if call.name == "clear" {
			cleared = append(cleared, call.turnID)
		}
	}

	require.Len(t, cleared, 1)
	require.Equal(t, activeTurnID(&saved[0]), cleared[0])
}

func TestLooperReportsToolErrorsInBand(t *testing.T) {
	run := func(t *testing.T, tools map[string]looperTool, call responses.ResponseFunctionToolCall, want string) {
		t.Helper()

		mock := mockResponses(
			responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{call}),
			responseWithMessage("resp-final", "recovered"),
		)
		looper := testLooper(mock)
		looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: call.Name, Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}
		looper.Tools = tools
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- testPromptInput(PromptInputRoleUser, "run tool", output)

		close(input)
		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		require.Equal(t, []ChatResponse{assistantMessage("recovered")}, collectResponses(output))
		require.Len(t, mock.calls, 2)
		require.Contains(t, marshalJSON(t, mock.calls[1].Input.OfInputItemList), want)
	}

	t.Run("tool call error", func(t *testing.T) {
		tool := testLooperTool("fail")
		tool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
			var result ToolResult

			return result, errors.New("boom")
		}
		run(t, map[string]looperTool{"fail": tool}, testFunctionCall("tool-1", "call-1", "fail", `{}`), "tool call failed: fail: boom")
	})

	t.Run("permission subject error", func(t *testing.T) {
		tool := testLooperTool("subject_fail")
		tool.Subjects = func(json.RawMessage) ([]string, error) {
			return nil, errors.New("bad subject")
		}
		tool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
			t.Error("tool with subject error should not execute")

			var result ToolResult

			return result, nil
		}
		run(t, map[string]looperTool{"subject_fail": tool}, testFunctionCall("tool-1", "call-1", "subject_fail", `{}`), "tool call failed: subject_fail: check permission: bad subject")
	})

	t.Run("unknown tool", func(t *testing.T) {
		run(t, nil, testFunctionCall("tool-1", "call-1", "missing", `{}`), "tool call failed: missing: tool not found")
	})

	t.Run("webfetch HTTP error", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(server.Close)

		tool := testLooperTool("webfetch")
		tool.Subjects = func(raw json.RawMessage) ([]string, error) {
			var params webFetchToolParams
			if err := decodeToolParams(raw, &params); err != nil {
				return nil, err
			}

			return []string{params.URL}, nil
		}
		tool.Call = func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
			var params webFetchToolParams
			if err := decodeToolParams(raw, &params); err != nil {
				var result ToolResult

				return result, err
			}

			return webFetch(ctx, params)
		}
		run(t, map[string]looperTool{"webfetch": tool}, testFunctionCall("tool-1", "call-1", "webfetch", fmt.Sprintf(`{"url":%q}`, server.URL)), "tool call failed: webfetch: request failed with status 404")
	})
}

func TestLooperKeepsContextCancellationFatalForToolCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	looper := emptyTestLooper()
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "slow", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}
	tool := testLooperTool("slow")
	tool.Call = func(ctx context.Context, _ json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
		<-ctx.Done()

		var result ToolResult

		return result, ctx.Err()
	}
	looper.Tools = map[string]looperTool{"slow": tool}

	_, hadToolCalls, err := looper.dispatchToolCalls(ctx, responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "slow", `{}`)}), nil, nil)

	require.Error(t, err)
	require.True(t, hadToolCalls)
	require.Contains(t, err.Error(), "run tool calls")
}

func TestLooperSendsAndReplaysUserAttachments(t *testing.T) {
	attachments := []Attachment{
		{MIME: "image/png", Filename: "image.png", URL: "data:image/png;base64,aW1hZ2U="},
		{MIME: "application/pdf", Filename: "doc.pdf", URL: "data:application/pdf;base64,cGRm"},
	}
	mock := mockResponses(responseWithMessage("resp-final", "done"))
	looper := testLooper(mock)
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInputWithAttachments("", "see attached", attachments, output)

	close(input)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Contains(t, marshalJSON(t, mock.calls[0].Input.OfInputItemList), `"role":"user"`)
	require.Contains(t, marshalJSON(t, mock.calls[0].Input.OfInputItemList), `"type":"input_image"`)
	require.Contains(t, marshalJSON(t, mock.calls[0].Input.OfInputItemList), `"type":"input_file"`)

	history, _, err := loadSession(sessionEntries(saved), looper.Origin)
	require.NoError(t, err)
	serialized := marshalJSON(t, history.replay)
	require.Contains(t, serialized, `"image_url":"data:image/png;base64,aW1hZ2U="`)
	require.Contains(t, serialized, `"file_data":"data:application/pdf;base64,cGRm"`)
}

func TestLooperSendsAndReplaysDeveloperAttachments(t *testing.T) {
	attachments := []Attachment{{MIME: "image/png", Filename: "image.png", URL: "data:image/png;base64,aW1hZ2U="}}
	mock := mockResponses(responseWithMessage("resp-final", "done"))
	looper := testLooper(mock)
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInputWithAttachments(PromptInputRoleDeveloper, "see attached", attachments, output)

	close(input)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, make(chan os.Signal, 1))

	require.NoError(t, err)
	serialized := marshalJSON(t, mock.calls[0].Input.OfInputItemList)
	require.Contains(t, serialized, `"role":"developer"`)
	require.Contains(t, serialized, `"type":"input_image"`)

	history, _, err := loadSession(sessionEntries(saved), looper.Origin)
	require.NoError(t, err)
	serialized = marshalJSON(t, history.replay)
	require.Contains(t, serialized, `"role":"developer"`)
	require.Contains(t, serialized, `"image_url":"data:image/png;base64,aW1hZ2U="`)
}

func TestLooperSendsToolOutputAttachments(t *testing.T) {
	mock := mockResponses(
		responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "read", `{}`)}),
		responseWithMessage("resp-final", "done"),
	)
	looper := testLooper(mock)
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "read", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}
	tool := testLooperTool("read")
	tool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
		return ToolResult{Output: "Image read successfully", Attachments: []Attachment{{MIME: "image/png", Filename: "image.png", URL: "data:image/png;base64,aW1hZ2U="}}}, nil
	}
	looper.Tools = map[string]looperTool{"read": tool}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "read image", output)

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	serialized := marshalJSON(t, mock.calls[1].Input.OfInputItemList)
	require.Contains(t, serialized, `"type":"input_text"`)
	require.Contains(t, serialized, `"type":"input_image"`)
}

func TestLooperDeniesToolCallsInBand(t *testing.T) {
	mock := mockResponses(
		responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "bash", `{"command":"rm -rf tmp","description":"remove tmp"}`)}),
		responseWithMessage("resp-final", "recovered"),
	)
	looper := testLooper(mock)
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "bash", Rules: []PermissionRule{
		{Pattern: "*", Action: permissionDeny},
		{Pattern: "git status *", Action: permissionAllow},
	}}}}
	bashTool := testLooperTool("bash")
	bashTool.Permission = "bash"
	bashTool.Subjects = func(raw json.RawMessage) ([]string, error) {
		var params bashParams
		require.NoError(t, json.Unmarshal(raw, &params))

		return BashPermissionSubjects(params.Command), nil
	}
	bashTool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
		t.Error("denied tool should not execute")

		var result ToolResult

		return result, nil
	}
	looper.Tools = map[string]looperTool{"bash": bashTool}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "run denied tool", output)

	close(input)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("recovered")}, collectResponses(output))
	require.Len(t, mock.calls, 2)
	require.Contains(t, marshalJSON(t, mock.calls[1].Input.OfInputItemList), "tool call denied")
}

func TestLooperAutoPermissionReview(t *testing.T) {
	t.Run("disabled denies without executing", func(t *testing.T) {
		called := false
		looper := testLooper(mockResponses())
		looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "bash", Rules: []PermissionRule{{Pattern: "deploy *", Action: permissionAuto}}}}}
		tool := testLooperTool("bash")
		tool.Permission = "bash"
		tool.Subjects = func(json.RawMessage) ([]string, error) { return []string{"deploy prod"}, nil }
		tool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
			called = true
			return TextToolResult("called"), nil
		}
		looper.Tools = map[string]looperTool{"bash": tool}

		outputs, hadToolCalls, err := looper.dispatchToolCalls(context.Background(), responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "bash", `{}`)}), nil, nil)

		require.NoError(t, err)
		require.True(t, hadToolCalls)
		require.False(t, called)
		require.Contains(t, outputs[0].Result.Output, `requires automatic approval, but automatic permission approval is disabled`)
	})

	t.Run("approval executes", func(t *testing.T) {
		called := false
		reviewer := &mockPermissionReviewer{decision: permissionReviewDecision{RiskLevel: permissionReviewRiskLevelLow, UserAuthorization: permissionReviewUserAuthorizationUnknown, Outcome: permissionReviewOutcomeAllow, Rationale: "Low-risk action."}}
		looper := testLooper(mockResponses())
		looper.AutoApprovePermissions = true
		looper.PermissionReviewer = reviewer
		looper.agent = Agent{Name: "main"}
		looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "webfetch", Rules: []PermissionRule{{Pattern: "https://allowed.example/*", Action: permissionAuto}}}}}
		tool := testLooperTool("webfetch")
		tool.Permission = "webfetch"
		tool.Subjects = func(json.RawMessage) ([]string, error) { return []string{"https://allowed.example/page"}, nil }
		tool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
			called = true
			return TextToolResult("fetched"), nil
		}
		looper.Tools = map[string]looperTool{"webfetch": tool}

		reviewContext := []responses.ResponseInputItemUnionParam{testInputMessage(responses.EasyInputMessageRoleUser, "fetch the allowed page", "")}
		looper.permissionReviewInput = reviewContext
		output := make(chan ChatResponse, 10)
		outputs, _, err := looper.dispatchToolCalls(context.Background(), responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "webfetch", `{"url":"https://allowed.example/page"}`)}), nil, output)

		require.NoError(t, err)
		require.True(t, called)
		require.Equal(t, "fetched", outputs[0].Result.Output)
		require.Equal(t, []ChatResponse{subagentDiagnosticResponse(&SubagentDiagnostic{Name: "guardian", Label: "auto-approver", Text: "allow: Low-risk action.", Subagent: &SubagentDiagnostic{Label: "result"}})}, drainBufferedResponses(output))
		require.Len(t, reviewer.requests, 1)
		require.True(t, reviewer.requests[0].ReviewerEmbedded)
		require.Equal(t, "main", reviewer.requests[0].ActiveAgent)
		require.Equal(t, "webfetch", reviewer.requests[0].Permission)
		require.Equal(t, []permissionReviewSubject{{Subject: "https://allowed.example/page", RulePattern: "https://allowed.example/*"}}, reviewer.requests[0].AutoSubjects)
		require.Equal(t, reviewContext, reviewer.requests[0].ReviewContext)
	})

	t.Run("denial does not execute custom reviewer", func(t *testing.T) {
		called := false
		reviewer := &mockPermissionReviewer{decision: permissionReviewDecision{RiskLevel: permissionReviewRiskLevelHigh, UserAuthorization: permissionReviewUserAuthorizationUnknown, Outcome: permissionReviewOutcomeDeny, Rationale: "Not authorized."}}
		looper := testLooper(mockResponses())
		looper.AutoApprovePermissions = true
		looper.PermissionReviewer = reviewer
		looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "tools", Rules: []PermissionRule{{Pattern: "github_private_repo", Action: permissionAuto, Reviewer: "release-guardian"}}}}}
		tool := testLooperTool("github_create_issue")
		tool.Permission = "tools"
		tool.Subjects = func(json.RawMessage) ([]string, error) { return []string{"github_private_repo"}, nil }
		tool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
			called = true
			return TextToolResult("created"), nil
		}
		looper.Tools = map[string]looperTool{"github_create_issue": tool}

		output := make(chan ChatResponse, 10)
		outputs, _, err := looper.dispatchToolCalls(context.Background(), responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "github_create_issue", `{}`)}), nil, output)

		require.NoError(t, err)
		require.False(t, called)
		require.Contains(t, outputs[0].Result.Output, "Not authorized")
		require.Equal(t, []ChatResponse{subagentDiagnosticResponse(&SubagentDiagnostic{Name: "guardian", Label: "auto-approver", Text: "deny: Not authorized.", Subagent: &SubagentDiagnostic{Label: "result"}})}, drainBufferedResponses(output))
		require.Len(t, reviewer.requests, 1)
		require.False(t, reviewer.requests[0].ReviewerEmbedded)
		require.Equal(t, "release-guardian", reviewer.requests[0].Reviewer)
	})

	t.Run("deny short circuits auto", func(t *testing.T) {
		reviewer := &mockPermissionReviewer{decision: permissionReviewDecision{RiskLevel: permissionReviewRiskLevelLow, UserAuthorization: permissionReviewUserAuthorizationUnknown, Outcome: permissionReviewOutcomeAllow, Rationale: "Low-risk action."}}
		looper := testLooper(mockResponses())
		looper.AutoApprovePermissions = true
		looper.PermissionReviewer = reviewer
		looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "bash", Rules: []PermissionRule{{Pattern: "deploy *", Action: permissionAuto}, {Pattern: "rm -rf *", Action: permissionDeny}}}}}
		tool := testLooperTool("bash")
		tool.Permission = "bash"
		tool.Subjects = func(json.RawMessage) ([]string, error) { return []string{"deploy prod", "rm -rf prod"}, nil }
		looper.Tools = map[string]looperTool{"bash": tool}

		outputs, _, err := looper.dispatchToolCalls(context.Background(), responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "bash", `{}`)}), nil, nil)

		require.NoError(t, err)
		require.Contains(t, outputs[0].Result.Output, `=> deny`)
		require.Empty(t, reviewer.requests)
	})

	t.Run("mixed reviewers deny without review", func(t *testing.T) {
		reviewer := &mockPermissionReviewer{decision: permissionReviewDecision{RiskLevel: permissionReviewRiskLevelLow, UserAuthorization: permissionReviewUserAuthorizationUnknown, Outcome: permissionReviewOutcomeAllow, Rationale: "Low-risk action."}}
		looper := testLooper(mockResponses())
		looper.AutoApprovePermissions = true
		looper.PermissionReviewer = reviewer
		looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "bash", Rules: []PermissionRule{{Pattern: "deploy *", Action: permissionAuto}, {Pattern: "release *", Action: permissionAuto, Reviewer: "release-guardian"}}}}}
		tool := testLooperTool("bash")
		tool.Permission = "bash"
		tool.Subjects = func(json.RawMessage) ([]string, error) { return []string{"deploy prod", "release prod"}, nil }
		looper.Tools = map[string]looperTool{"bash": tool}

		outputs, _, err := looper.dispatchToolCalls(context.Background(), responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "bash", `{}`)}), nil, nil)

		require.NoError(t, err)
		require.Contains(t, outputs[0].Result.Output, `matched multiple automatic reviewers`)
		require.Empty(t, reviewer.requests)
	})
}

func TestPermissionReviewFailsClosedOnInvalidReviewerOutput(t *testing.T) {
	modelRef, err := parseModelRef(openai.ChatModelGPT5)
	require.NoError(t, err)

	factory := &toolFactory{
		providers:            Providers{"openai": {client: mockResponses(responseWithMessage("review", "not json")), route: "route", authenticationEpoch: "epoch"}},
		defaultModelRef:      modelRef,
		autoApproverModelRef: modelRef,
		modelRef:             modelRef,
		agents:               Agents{Items: map[string]Agent{}},
		skills:               Skills{Items: map[string]Skill{}},
		baseTools:            map[string]looperTool{},
		shellOutput:          shellOutputConfig{},
		childRunLogger:       DiscardChildRunLog,
	}

	decision := factory.reviewPermission(context.Background(), &permissionReviewRequest{ToolName: "bash", Permission: "bash", RawArguments: `{}`, Subjects: []string{"deploy prod"}, AutoSubjects: []permissionReviewSubject{{Subject: "deploy prod", RulePattern: "deploy *"}}, ReviewerEmbedded: true}, make(chan ChatResponse, 10))

	require.Equal(t, permissionReviewRiskLevelHigh, decision.RiskLevel)
	require.Equal(t, permissionReviewUserAuthorizationUnknown, decision.UserAuthorization)
	require.Equal(t, permissionReviewOutcomeDeny, decision.Outcome)
	require.Contains(t, decision.Rationale, "invalid JSON")
}

func TestLooperAppliesWebFetchURLPermissions(t *testing.T) {
	t.Run("denies non matching URL", func(t *testing.T) {
		mock := mockResponses(
			responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "webfetch", `{"url":"https://blocked.example/page"}`)}),
			responseWithMessage("resp-final", "recovered"),
		)
		looper := testLooper(mock)
		looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "webfetch", Rules: []PermissionRule{
			{Pattern: "*", Action: permissionDeny},
			{Pattern: "https://allowed.example/*", Action: permissionAllow},
		}}}}
		webfetchTool := testLooperTool("webfetch")
		webfetchTool.Permission = "webfetch"
		webfetchTool.Subjects = func(raw json.RawMessage) ([]string, error) {
			var params webFetchToolParams
			require.NoError(t, decodeToolParams(raw, &params))

			return []string{params.URL}, nil
		}
		webfetchTool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
			t.Error("denied webfetch should not execute")

			var result ToolResult

			return result, nil
		}
		looper.Tools = map[string]looperTool{"webfetch": webfetchTool}
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- testPromptInput(PromptInputRoleUser, "fetch docs", output)

		close(input)

		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		serialized := marshalJSON(t, mock.calls[1].Input.OfInputItemList)
		require.Contains(t, serialized, `permission \"webfetch\" rejected subject \"https://blocked.example/page\"`)
	})

	t.Run("allows matching URL", func(t *testing.T) {
		mock := mockResponses(
			responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "webfetch", `{"url":"https://allowed.example/page"}`)}),
			responseWithMessage("resp-final", "done"),
		)
		called := false
		looper := testLooper(mock)
		looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "webfetch", Rules: []PermissionRule{
			{Pattern: "*", Action: permissionDeny},
			{Pattern: "https://allowed.example/*", Action: permissionAllow},
		}}}}
		webfetchTool := testLooperTool("webfetch")
		webfetchTool.Permission = "webfetch"
		webfetchTool.Subjects = func(raw json.RawMessage) ([]string, error) {
			var params webFetchToolParams
			require.NoError(t, decodeToolParams(raw, &params))

			return []string{params.URL}, nil
		}
		webfetchTool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
			called = true
			return TextToolResult("fetched"), nil
		}
		looper.Tools = map[string]looperTool{"webfetch": webfetchTool}
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- testPromptInput(PromptInputRoleUser, "fetch docs", output)

		close(input)

		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		require.True(t, called)
		require.Contains(t, marshalJSON(t, mock.calls[1].Input.OfInputItemList), "fetched")
	})
}

func TestLooperGatesSkillByName(t *testing.T) {
	mock := mockResponses(
		responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{
			testFunctionCall("tool-1", "call-1", "skill", `{"name":"git-review"}`),
			testFunctionCall("tool-2", "call-2", "skill", `{"name":"docs-helper"}`),
		}),
		responseWithMessage("resp-final", "done"),
	)
	calls := []string{}
	looper := testLooper(mock)
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "skill", Rules: []PermissionRule{
		{Pattern: "*", Action: permissionDeny},
		{Pattern: "docs-helper", Action: permissionAllow},
	}}}}
	skillTool := testLooperTool("skill")
	skillTool.Permission = "skill"
	skillTool.Subjects = func(raw json.RawMessage) ([]string, error) {
		var params struct {
			Name string `json:"name"`
		}
		require.NoError(t, json.Unmarshal(raw, &params))

		return []string{params.Name}, nil
	}
	skillTool.Call = func(_ context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
		var params struct {
			Name string `json:"name"`
		}
		require.NoError(t, json.Unmarshal(raw, &params))
		calls = append(calls, params.Name)

		return TextToolResult("loaded " + params.Name), nil
	}
	looper.Tools = map[string]looperTool{"skill": skillTool}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "load skills", output)

	close(input)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []string{"docs-helper"}, calls)
	require.Equal(t, []ChatResponse{assistantMessage("done")}, collectResponses(output))
	serialized := marshalJSON(t, mock.calls[1].Input.OfInputItemList)
	require.Contains(t, serialized, "tool call denied")
	require.Contains(t, serialized, "loaded docs-helper")
}

func TestLooperDirectSkillInjectsDeveloperInput(t *testing.T) {
	loaded := LoadSkills(fstest.MapFS{"docs-helper/SKILL.md": mapFile(`---
name: docs-helper
description: Write docs
---

Use this skill for docs.
`)}, "/virtual/skills").Skills
	agent := agentWithSkillPermission()
	factory := testSkillFactory(t, loaded, agent)
	factory.experimentalStrongerSkills = true

	mock := mockResponses(responseWithMessage("resp-final", "done"))
	looper := testLooper(mock)
	looper.Permissions = agent.Permission
	looper.Tools = map[string]looperTool{"skill": factory.skillTool()}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Text: "apply it", DirectSkill: &PromptInputDirectSkill{Name: "docs-helper", Arguments: "write the API guide"}, Responses: output}

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantMessage("done")}, collectResponses(output))
	require.Len(t, mock.calls, 1)
	serialized := marshalJSON(t, mock.calls[0].Input.OfInputItemList)
	require.Contains(t, serialized, `"role":"developer"`)
	require.Contains(t, serialized, "Use this skill for docs.")
	require.Contains(t, serialized, "write the API guide")
	require.Contains(t, serialized, `"role":"user"`)
	require.Contains(t, serialized, "apply it")
}

func TestLooperDirectSkillRejectsBeforeModelRequest(t *testing.T) {
	mock := mockResponses(responseWithMessage("resp-final", "should not run"))
	looper := testLooper(mock)
	looper.Tools = map[string]looperTool{"skill": testLooperTool("skill")}
	output := make(chan ChatResponse, 10)
	saves := 0

	input := make(chan PromptInput, 1)
	input <- PromptInput{DirectSkill: &PromptInputDirectSkill{}, Responses: output}

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), func(SessionEntry) error {
		saves++

		return nil
	}, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Empty(t, mock.calls)
	require.Zero(t, saves)
	require.Equal(t, []ChatResponse{assistantMessage("direct skill invocation requires a skill name")}, collectResponses(output))
}

func TestLooperDirectSkillRejectsUnknownAndDeniedSkillsBeforeModelRequest(t *testing.T) {
	loaded := LoadSkills(fstest.MapFS{"docs-helper/SKILL.md": mapFile(`---
name: docs-helper
description: Write docs
---

Use this skill for docs.
`)}, "/virtual/skills").Skills

	run := func(t *testing.T, agent *Agent, skillName string) []ChatResponse {
		t.Helper()

		mock := mockResponses(responseWithMessage("resp-final", "should not run"))
		looper := testLooper(mock)
		looper.Permissions = agent.Permission
		looper.Tools = map[string]looperTool{"skill": testSkillFactory(t, loaded, agent).skillTool()}
		output := make(chan ChatResponse, 10)
		saves := 0

		input := make(chan PromptInput, 1)
		input <- PromptInput{DirectSkill: &PromptInputDirectSkill{Name: skillName}, Responses: output}

		close(input)

		err := looper.Loop(context.Background(), input, emptySession(), func(SessionEntry) error {
			saves++

			return nil
		}, make(chan os.Signal, 1))

		require.NoError(t, err)
		require.Empty(t, mock.calls)
		require.Zero(t, saves)

		return collectResponses(output)
	}

	t.Run("unknown skill", func(t *testing.T) {
		got := run(t, agentWithSkillPermission(), "missing-skill")

		require.Len(t, got, 1)
		require.Equal(t, ChatResponseAssistantMessage, got[0].Kind)
		require.Contains(t, got[0].Text, `skill "missing-skill" not found`)
		require.Contains(t, got[0].Text, "Available skills: docs-helper")
	})

	t.Run("denied skill", func(t *testing.T) {
		got := run(t, agentWithSkillRules(PermissionRule{Pattern: "docs-helper", Action: permissionDeny}), "docs-helper")

		require.Len(t, got, 1)
		require.Equal(t, ChatResponseAssistantMessage, got[0].Kind)
		require.Contains(t, got[0].Text, "tool call denied")
		require.Contains(t, got[0].Text, `permission "skill"`)
		require.Contains(t, got[0].Text, `subject "docs-helper"`)
	})
}

func TestLooperEmitsToolDiagnosticsWhenEnabled(t *testing.T) {
	mock := mockResponses(
		responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "skill", `{"name":"current-time"}`)}),
		responseWithMessage("resp-final", "done"),
	)
	looper := testLooper(mock)
	looper.Diagnostics = true
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "skill", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}
	skillTool := testLooperTool("skill")
	skillTool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
		return TextToolResult("loaded current-time"), nil
	}
	looper.Tools = map[string]looperTool{"skill": skillTool}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "what time is it?", output)

	close(input)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)

	callDiagnostic := testToolDiagnostic(toolDiagnosticPhaseCall, "skill")
	callDiagnostic.Arguments = json.RawMessage(`{"name":"current-time"}`)
	resultDiagnostic := testToolDiagnostic(toolDiagnosticPhaseResult, "skill")
	resultDiagnostic.Result = "loaded current-time"
	require.Equal(t, []ChatResponse{
		toolDiagnosticResponse(callDiagnostic),
		toolDiagnosticResponse(resultDiagnostic),
		assistantMessage("done"),
	}, collectResponses(output))
}

func TestLooperEmitsHostedWebSearchDiagnosticsWhenEnabled(t *testing.T) {
	mock := mockResponses(responseWithWebSearchAndMessage("resp-search", "opencode", "found it"))
	looper := testLooper(mock)
	looper.Diagnostics = true
	looper.Tools = map[string]looperTool{"websearch": webSearchTool()}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "search web", output)

	close(input)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)

	callDiagnostic := testToolDiagnostic(toolDiagnosticPhaseCall, "websearch")
	callDiagnostic.Status = "completed"
	callDiagnostic.Action = json.RawMessage(`{"queries":["opencode"],"query":"opencode","type":"search"}`)
	require.Equal(t, []ChatResponse{
		toolDiagnosticResponse(callDiagnostic),
		assistantMessage("found it"),
	}, collectResponses(output))
}

func TestEmitDiagnosticChatResponseDropsWhenUnavailable(t *testing.T) {
	emitDiagnosticChatResponse(nil, toolDiagnosticResponse(testToolDiagnostic("", "nil")))

	unbuffered := make(chan ChatResponse)
	emitDiagnosticChatResponse(unbuffered, toolDiagnosticResponse(testToolDiagnostic("", "blocked")))

	select {
	case item := <-unbuffered:
		t.Fatalf("unexpected diagnostic delivered on blocked channel: %#v", item)
	default:
	}

	buffered := make(chan ChatResponse, 1)
	buffered <- assistantMessage("existing")

	emitDiagnosticChatResponse(buffered, toolDiagnosticResponse(testToolDiagnostic("", "full")))
	require.Equal(t, assistantMessage("existing"), <-buffered)
}

func TestLooperTrapsDoomLoopInBand(t *testing.T) {
	mock := mockResponses(
		responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{
			testFunctionCall("tool-1", "call-1", "repeat", `{"b":2,"a":1}`),
			testFunctionCall("tool-2", "call-2", "repeat", `{"a":1,"b":2}`),
			testFunctionCall("tool-3", "call-3", "repeat", `{"a":1,"b":2}`),
		}),
		responseWithMessage("resp-final", "done"),
	)

	var (
		callsMu sync.Mutex
		calls   int
	)

	looper := testLooper(mock)
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "repeat", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}
	repeatTool := testLooperTool("repeat")
	repeatTool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
		callsMu.Lock()
		defer callsMu.Unlock()

		calls++

		return TextToolResult("ok"), nil
	}
	looper.Tools = map[string]looperTool{"repeat": repeatTool}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "repeat", output)

	close(input)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	callsMu.Lock()
	gotCalls := calls
	callsMu.Unlock()
	require.Equal(t, 2, gotCalls)
	require.Equal(t, []ChatResponse{assistantMessage("done")}, collectResponses(output))
	require.Contains(t, marshalJSON(t, mock.calls[1].Input.OfInputItemList), "repeated identical")
}

func TestLooperPrintsReasoningSummary(t *testing.T) {
	mock := mockResponses(responseWithReasoningAndMessage("resp-reason", "think briefly", "final answer"))
	looper := testLooper(mock)

	output := make(chan ChatResponse, 10)
	interrupts := make(chan os.Signal, 1)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession, interrupts)
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{reasoningSummary("think briefly"), assistantMessage("final answer")}, collectResponses(output))
}

func TestLooperUpdatesSessionStoreAfterCompletedTurn(t *testing.T) {
	mock := mockResponses(responseWithUsage(responseWithMessage("resp-save", "saved answer"), `{"input_tokens":12,"input_tokens_details":{"cached_tokens":3},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":17}`))
	store := testSessionStore()
	looper := testLooper(mock)
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)

	interrupts := make(chan os.Signal, 1)
	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		return store.appendEntry(&entry)
	}, interrupts)
	require.NoError(t, err)
	require.Len(t, store.saves, 1)
	require.Len(t, store.saves[0], 1)
	require.Equal(t, &TokenUsage{PromptTokens: 12, CompletionTokens: 5, TotalTokens: 17, PromptCacheReadTokens: 3, CompletionReasoningTokens: 2}, store.saves[0][0].TokenUsage)
	require.Equal(t, []ChatResponse{assistantMessage("saved answer")}, collectResponses(output))
}

func TestLooperOmitsInterruptedTurnsFromSession(t *testing.T) {
	started := make(chan struct{})
	mock := mockResponseFunc(func(ctx context.Context, _ *responses.ResponseNewParams) (*responses.Response, error) {
		close(started)
		<-ctx.Done()

		return nil, ctx.Err()
	})
	looper := testLooper(mock)
	sink := &mockCheckpointSink{}
	looper.CheckpointSink = sink
	interrupts := make(chan os.Signal, 1)

	var saved []SessionEntry

	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "will interrupt", output)

	close(input)

	var group errgroup.Group

	group.Go(func() error {
		return looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
			saved = append(saved, entry)

			return nil
		}, interrupts)
	})

	<-started

	interrupts <- os.Interrupt

	require.NoError(t, group.Wait())
	require.Equal(t, []ChatResponse{assistantCommentary("(interrupted)")}, collectResponses(output))

	_, turns, err := loadSession(sessionEntries(saved), looper.Origin)
	require.NoError(t, err)
	require.Empty(t, turns)

	calls := sink.snapshot()
	require.NotEmpty(t, calls)
	interrupted := calls[len(calls)-1]
	require.Equal(t, "recovered", interrupted.name)
	require.Len(t, interrupted.checkpoint.ReplayInput, 2)
	require.Contains(t, string(interrupted.checkpoint.ReplayInput[1]), recoveryReplayMessageText)
}

func TestLooperContextCancellationDuringProviderCallMarksInterruptedCheckpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	mock := mockResponseFunc(func(ctx context.Context, _ *responses.ResponseNewParams) (*responses.Response, error) {
		close(started)
		<-ctx.Done()

		return nil, ctx.Err()
	})
	looper := testLooper(mock)
	sink := &mockCheckpointSink{}
	looper.CheckpointSink = sink
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "will cancel", output)

	close(input)

	var group errgroup.Group
	group.Go(func() error {
		return looper.Loop(ctx, input, emptySession(), discardSession, make(chan os.Signal, 1))
	})
	<-started
	cancel()

	err := group.Wait()
	require.Error(t, err)
	require.Contains(t, err.Error(), "request response")

	calls := sink.snapshot()
	require.NotEmpty(t, calls)
	interrupted := calls[len(calls)-1]
	require.Equal(t, "recovered", interrupted.name)
	require.Contains(t, marshalJSON(t, interrupted.checkpoint.ReplayInput), recoveryReplayMessageText)
}

func TestLooperCancellationDuringToolDispatchMarksInterruptedCheckpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mock := mockResponses(responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "task", `{"description":"work"}`)}))
	looper := testLooper(mock)
	sink := &mockCheckpointSink{}
	looper.CheckpointSink = sink
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "task", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}
	tool := testLooperTool("task")
	tool.Call = func(ctx context.Context, _ json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
		cancel()
		<-ctx.Done()

		return ToolResult{}, ctx.Err()
	}
	looper.Tools = map[string]looperTool{"task": tool}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "delegate", output)

	close(input)

	saved := []SessionEntry{}
	err := looper.Loop(ctx, input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, make(chan os.Signal, 1))

	require.Error(t, err)
	require.Contains(t, err.Error(), "dispatch tool calls")
	require.Empty(t, saved)

	calls := sink.snapshot()
	require.NotEmpty(t, calls)
	interrupted := calls[len(calls)-1]
	require.Equal(t, "recovered", interrupted.name)
	require.Len(t, interrupted.checkpoint.OpenFunctionCalls, 1)
	require.Equal(t, "call-1", interrupted.checkpoint.OpenFunctionCalls[0].CallID)
	require.Contains(t, marshalJSON(t, interrupted.checkpoint.ReplayInput), taskAbortedToolOutputText)
	require.Contains(t, marshalJSON(t, interrupted.checkpoint.ReplayInput), recoveryReplayMessageText)
}

func TestLooperPrintsCommentaryResponses(t *testing.T) {
	mock := mockResponses(responseWithCommentaryAndMessage("resp-commentary", "working", "final"))
	looper := testLooper(mock)
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)

	interrupts := make(chan os.Signal, 1)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, interrupts)
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{assistantCommentary("working"), assistantMessage("final")}, collectResponses(output))
}

func TestLooperRetriesRateLimitExceededFailedResponse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mock := mockResponses(
			failedResponseWithCode("resp-rate", responses.ResponseErrorCodeRateLimitExceeded, "too many requests"),
			responseWithMessage("resp-ok", "done"),
		)
		looper := testLooper(mock)
		looper.Diagnostics = true
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- testPromptInput(PromptInputRoleUser, "question", output)

		close(input)

		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		require.Len(t, mock.calls, 2)

		diagnostic := &ProviderDiagnostic{Phase: providerDiagnosticRetry, HTTPStatus: 0, ResponseStatus: string(responses.ResponseStatusFailed), Code: string(responses.ResponseErrorCodeRateLimitExceeded), Attempt: 1, RetryAfter: "1s", ResponseID: "resp-rate"}
		require.Equal(t, []ChatResponse{
			providerDiagnosticResponse(diagnostic),
			assistantMessage("done"),
		}, collectResponses(output))
	})
}

func TestLooperReportsFailedResponsesInDiagnostics(t *testing.T) {
	mock := mockResponses(
		failedResponseWithCode("resp-invalid", responses.ResponseErrorCodeInvalidPrompt, "bad prompt"),
	)
	looper := testLooper(mock)
	looper.Diagnostics = true
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.EqualError(t, err, "run turn: request response: response failed: invalid_prompt")

	diagnostic := &ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: 0, ResponseStatus: string(responses.ResponseStatusFailed), Code: string(responses.ResponseErrorCodeInvalidPrompt), Attempt: 0, RetryAfter: "", ResponseID: "resp-invalid"}
	require.Equal(t, []ChatResponse{providerDiagnosticResponse(diagnostic)}, collectResponses(output))
}

func TestLooperReportsOpenAIRequestErrorsInDiagnostics(t *testing.T) {
	mock := mockResponseError(openAIError("rate_limit_exceeded", "slow down", nil))
	looper := testLooper(mock)
	looper.Diagnostics = true
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.EqualError(t, err, `run turn: request response: new response: provider "openai" retry limit: status 429`)
	require.Len(t, mock.calls, 1)

	_, ok := errors.AsType[*providerRetryLimitError](err)
	require.True(t, ok)

	diagnostic := &ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: http.StatusTooManyRequests, ResponseStatus: "", Code: string(responses.ResponseErrorCodeRateLimitExceeded), Attempt: 0, RetryAfter: "", ResponseID: ""}
	require.Equal(t, []ChatResponse{providerDiagnosticResponse(diagnostic)}, collectResponses(output))
}

func TestLooperClassifiesTerminalTooManyRequestsErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
		want func(error) bool
	}{
		{name: "usage limit", code: "usage_limit_reached", want: func(err error) bool {
			_, ok := errors.AsType[*providerUsageLimitError](err)
			return ok
		}},
		{name: "usage not included", code: "usage_not_included", want: func(err error) bool {
			_, ok := errors.AsType[*providerUsageNotIncludedError](err)
			return ok
		}},
		{name: "generic retry limit", code: "rate_limit_exceeded", want: func(err error) bool {
			_, ok := errors.AsType[*providerRetryLimitError](err)
			return ok
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := mockResponseError(openAIError(tc.code, "blocked", nil))
			looper := testLooper(mock)
			output := make(chan ChatResponse, 10)

			input := make(chan PromptInput, 1)
			input <- testPromptInput(PromptInputRoleUser, "question", output)

			close(input)

			err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

			require.Error(t, err)
			require.True(t, tc.want(err))
			require.Len(t, mock.calls, 1)
		})
	}
}

func TestLooperRetriesTooManyRequestsRequestError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		headers := http.Header{"Retry-After-Ms": []string{"2000"}, "X-Request-ID": []string{"req-rate"}}
		calls := 0
		mock := mockResponseFunc(func(context.Context, *responses.ResponseNewParams) (*responses.Response, error) {
			calls++
			if calls == 1 {
				return nil, openAIError("too_many_requests", "Too Many Requests", headers)
			}

			return responseWithMessage("resp-ok", "done"), nil
		})
		looper := testLooper(mock)
		looper.Diagnostics = true
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- testPromptInput(PromptInputRoleUser, "question", output)

		close(input)

		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		require.Len(t, mock.calls, 2)

		diagnostic := &ProviderDiagnostic{Phase: providerDiagnosticRetry, HTTPStatus: http.StatusTooManyRequests, Code: "too_many_requests", Attempt: 1, RetryAfter: "2s", Headers: map[string]string{"retry-after-ms": "2000", "x-request-id": "req-rate"}}
		require.Equal(t, []ChatResponse{
			providerDiagnosticResponse(diagnostic),
			assistantMessage("done"),
		}, collectResponses(output))
	})
}

func TestLooperReportsRequestErrorsInDiagnostics(t *testing.T) {
	mock := mockResponseError(errors.New("network exploded"))
	looper := testLooper(mock)
	looper.Diagnostics = true
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.EqualError(t, err, `run turn: request response: new response: provider "openai" request failed`)

	diagnostic := &ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: 0, ResponseStatus: "", Code: "", Attempt: 0, RetryAfter: "", ResponseID: ""}
	require.Equal(t, []ChatResponse{providerDiagnosticResponse(diagnostic)}, collectResponses(output))
}

func TestLooperLoopRequiresDependencies(t *testing.T) {
	looper := emptyTestLooper()
	input := make(chan PromptInput)
	interrupts := make(chan os.Signal, 1)

	var nilCtx context.Context
	require.EqualError(t, looper.Loop(nilCtx, input, emptySession(), discardSession, interrupts), "context is required")
	require.EqualError(t, looper.Loop(context.Background(), nil, emptySession(), discardSession, interrupts), "input channel is required")
	require.EqualError(t, looper.Loop(context.Background(), input, nil, discardSession, interrupts), "sessionIn is required")
	require.EqualError(t, looper.Loop(context.Background(), input, emptySession(), nil, interrupts), "sessionOut is required")
	require.EqualError(t, looper.Loop(context.Background(), input, emptySession(), discardSession, nil), "interrupts channel is required")

	close(input)
}

func TestLooperLoopRequiresPromptResponseChannel(t *testing.T) {
	looper := emptyTestLooper()

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "question"}

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.EqualError(t, err, "prompt response channel is required")
}

func responseWithMessage(id, text string) *responses.Response {
	return testResponse(id, []responses.ResponseOutputItemUnion{testMessageOutputItem(id+"-msg", "", text)})
}

func completedBackupResponse(text string) *responses.Response {
	response := responseWithMessage("resp-backup", text)
	response.Status = responses.ResponseStatusCompleted

	return response
}

func responseWithUsage(resp *responses.Response, usageJSON string) *responses.Response {
	var usageResponse responses.Response
	if err := json.Unmarshal([]byte(`{"usage":`+usageJSON+`}`), &usageResponse); err != nil {
		panic(err)
	}

	resp.Usage = usageResponse.Usage
	resp.JSON.Usage = usageResponse.JSON.Usage

	return resp
}

func failedResponseWithCode(id string, code responses.ResponseErrorCode, message string) *responses.Response {
	var response responses.Response

	response.ID = id
	response.Status = responses.ResponseStatusFailed
	response.Error.Code = code
	response.Error.Message = message

	return &response
}

func openAIError(code, message string, headers http.Header) *openai.Error {
	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", http.NoBody)
	if err != nil {
		panic(err)
	}

	var response http.Response

	response.StatusCode = http.StatusTooManyRequests
	response.Header = headers
	response.Request = req

	var errOpenAI openai.Error

	errOpenAI.Code = code
	errOpenAI.Message = message
	errOpenAI.StatusCode = http.StatusTooManyRequests
	errOpenAI.Request = req
	errOpenAI.Response = &response

	return &errOpenAI
}

func responseWithFunctionCalls(id string, calls []responses.ResponseFunctionToolCall) *responses.Response {
	output := make([]responses.ResponseOutputItemUnion, 0, len(calls))
	for i := range calls {
		call := &calls[i]

		var arguments responses.ResponseOutputItemUnionArguments

		arguments.OfString = call.Arguments

		var item responses.ResponseOutputItemUnion

		item.ID = call.ID
		item.Type = "function_call"
		item.CallID = call.CallID
		item.Name = call.Name
		item.Arguments = arguments
		item.Status = "completed"

		output = append(output, item)
	}

	return testResponse(id, output)
}

func responseWithReasoningAndMessage(id, reasoning, text string) *responses.Response {
	return testResponse(id, []responses.ResponseOutputItemUnion{
		testReasoningOutputItem(id+"-reasoning", "encrypted", reasoning),
		testMessageOutputItem(id+"-msg", "", text),
	})
}

func responseWithCompactionAndMessage(id, encryptedContent, text string) *responses.Response {
	return testResponse(id, []responses.ResponseOutputItemUnion{
		testCompactionOutputItem(id+"-compaction", encryptedContent),
		testMessageOutputItem(id+"-msg", "", text),
	})
}

func responseWithWebSearchAndMessage(id, query, text string) *responses.Response {
	return testResponse(id, []responses.ResponseOutputItemUnion{
		testWebSearchOutputItem(id+"-web", query),
		testMessageOutputItem(id+"-msg", "", text),
	})
}

func responseWithCommentaryAndMessage(id, commentary, text string) *responses.Response {
	return testResponse(id, []responses.ResponseOutputItemUnion{
		testMessageOutputItem(id+"-commentary", "commentary", commentary),
		testMessageOutputItem(id+"-msg", "final_answer", text),
	})
}

func compactedResponse(id, encryptedContent string) *responses.CompactedResponse {
	var compacted responses.CompactedResponse

	compacted.ID = id
	compacted.Output = []responses.ResponseOutputItemUnion{testCompactionOutputItem(id, encryptedContent)}

	return &compacted
}

func testResponse(id string, output []responses.ResponseOutputItemUnion) *responses.Response {
	var response responses.Response

	response.ID = id
	response.Output = output

	return &response
}

func testOutputText(text string) responses.ResponseOutputMessageContentUnion {
	var content responses.ResponseOutputMessageContentUnion

	content.Type = "output_text"
	content.Text = text

	return content
}

func testMessageOutputItem(id, phase, text string) responses.ResponseOutputItemUnion {
	var item responses.ResponseOutputItemUnion

	item.ID = id
	item.Type = "message"
	item.Role = "assistant"
	item.Status = "completed"
	item.Phase = responses.ResponseOutputMessagePhase(phase)
	item.Content = []responses.ResponseOutputMessageContentUnion{testOutputText(text)}

	return item
}

func testReasoningSummary(text string) responses.ResponseReasoningItemSummary {
	var summary responses.ResponseReasoningItemSummary

	summary.Text = text
	summary.Type = "summary_text"

	return summary
}

func testReasoningOutputItem(id, encryptedContent, text string) responses.ResponseOutputItemUnion {
	var item responses.ResponseOutputItemUnion

	item.ID = id
	item.Type = "reasoning"
	item.EncryptedContent = encryptedContent
	item.Summary = []responses.ResponseReasoningItemSummary{testReasoningSummary(text)}

	return item
}

func testCompactionOutputItem(id, encryptedContent string) responses.ResponseOutputItemUnion {
	var item responses.ResponseOutputItemUnion

	item.ID = id
	item.Type = "compaction"
	item.EncryptedContent = encryptedContent

	return item
}

func testWebSearchOutputItem(id, query string) responses.ResponseOutputItemUnion {
	var action responses.ResponseOutputItemUnionAction

	action.Type = "search"
	action.Query = query
	action.Queries = []string{query}

	var item responses.ResponseOutputItemUnion

	item.ID = id
	item.Type = "web_search_call"
	item.Status = "completed"
	item.Action = action

	return item
}

func collectResponses(ch <-chan ChatResponse) []ChatResponse {
	result := []ChatResponse{}
	for item := range ch {
		result = append(result, item)
	}

	return result
}

func marshalJSON(t *testing.T, value any) string {
	t.Helper()

	raw, err := json.Marshal(value)
	require.NoError(t, err)

	return string(raw)
}
