package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
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
	mu        sync.Mutex
	calls     []responses.ResponseNewParams
	responses []*responses.Response
	err       error
	newFunc   func(context.Context, *responses.ResponseNewParams) (*responses.Response, error)
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

type mockSessionStore struct {
	mu      sync.Mutex
	saves   [][]SessionEntry
	entries []SessionEntry
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

func testLooper(client responsesAPI) *looper {
	var l looper

	l.Client = client
	l.Model = openai.ChatModelGPT5

	return &l
}

func emptyTestLooper() *looper {
	var l looper

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

	_, turns, err := loadSession(sessionEntries(m.entries))
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

	_, savedTurns, err := loadSession(sessionEntries(saved))
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

	history, _, err := loadSession(sessionEntries(saved))
	require.NoError(t, err)
	require.JSONEq(t, `{"content":"keep this rule","role":"developer","type":"message"}`, marshalJSON(t, history[0]))
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
			turn, _, interrupted, err := looper.runTurn(context.Background(), output, nil, nil, testPromptInput(PromptInputRoleUser, "before !`printf hello` after", nil))

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

func TestLooperBuildParamsIncludesConfiguredVerbosity(t *testing.T) {
	looper := emptyTestLooper()
	looper.Verbosity = "low"

	params := looper.buildParams(nil)

	require.Equal(t, responses.ResponseTextConfigVerbosityLow, params.Text.Verbosity)
}

func TestLooperPersistsAndReplaysCompactionItems(t *testing.T) {
	mock := mockResponses(responseWithCompactionAndMessage("resp-compact", "encrypted-compact", "answer"))
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

	history, turns, err := loadSession(sessionEntries(saved))
	require.NoError(t, err)
	require.Len(t, turns, 1)
	require.Len(t, turns[0].ReplayInput, 3)
	require.Len(t, history, 3)
	require.JSONEq(t, `{"encrypted_content":"encrypted-compact","id":"resp-compact-compaction","type":"compaction"}`, marshalJSON(t, history[1]))
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

	require.Len(t, mock.calls, 2)
	items := mock.calls[1].Input.OfInputItemList
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

	history, _, err := loadSession(sessionEntries(saved))
	require.NoError(t, err)
	require.Len(t, history, 7)
	require.Equal(t, "function_call", *history[1].GetType())
	require.Equal(t, "function_call", *history[2].GetType())
	require.Equal(t, "function_call_output", *history[3].GetType())
	require.Equal(t, "function_call_output", *history[4].GetType())
	require.Equal(t, "message", *history[5].GetType())
	require.Equal(t, "message", *history[6].GetType())
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

	history, _, err := loadSession(sessionEntries(saved))
	require.NoError(t, err)
	serialized := marshalJSON(t, history)
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

	history, _, err := loadSession(sessionEntries(saved))
	require.NoError(t, err)
	serialized = marshalJSON(t, history)
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

		return bashPermissionSubjects(params.Command), nil
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
	mock := mockResponses(responseWithMessage("resp-save", "saved answer"))
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

	_, turns, err := loadSession(sessionEntries(saved))
	require.NoError(t, err)
	require.Empty(t, turns)
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

		diagnostic := &ProviderDiagnostic{Phase: providerDiagnosticRetry, HTTPStatus: 0, ResponseStatus: string(responses.ResponseStatusFailed), Code: string(responses.ResponseErrorCodeRateLimitExceeded), Message: "too many requests", Attempt: 1, RetryAfter: "1m0s", ResponseID: "resp-rate"}
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

	require.EqualError(t, err, "run turn: request response: response failed: invalid_prompt: bad prompt")

	diagnostic := &ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: 0, ResponseStatus: string(responses.ResponseStatusFailed), Code: string(responses.ResponseErrorCodeInvalidPrompt), Message: "bad prompt", Attempt: 0, RetryAfter: "", ResponseID: "resp-invalid"}
	require.Equal(t, []ChatResponse{providerDiagnosticResponse(diagnostic)}, collectResponses(output))
}

func TestLooperReportsOpenAIRequestErrorsInDiagnostics(t *testing.T) {
	mock := mockResponseError(openAIError("rate_limit_exceeded", "slow down", http.StatusTooManyRequests, nil))
	looper := testLooper(mock)
	looper.Diagnostics = true
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.EqualError(t, err, "run turn: request response: new response: POST \"https://api.openai.com/v1/responses\": 429 Too Many Requests ")
	require.Len(t, mock.calls, 1)

	diagnostic := &ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: http.StatusTooManyRequests, ResponseStatus: "", Code: string(responses.ResponseErrorCodeRateLimitExceeded), Message: "slow down", Attempt: 0, RetryAfter: "", ResponseID: ""}
	require.Equal(t, []ChatResponse{providerDiagnosticResponse(diagnostic)}, collectResponses(output))
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

	require.EqualError(t, err, "run turn: request response: new response: network exploded")

	diagnostic := &ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: 0, ResponseStatus: "", Code: "", Message: "network exploded", Attempt: 0, RetryAfter: "", ResponseID: ""}
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

func failedResponseWithCode(id string, code responses.ResponseErrorCode, message string) *responses.Response {
	var response responses.Response

	response.ID = id
	response.Status = responses.ResponseStatusFailed
	response.Error.Code = code
	response.Error.Message = message

	return &response
}

func openAIError(code, message string, status int, headers http.Header) *openai.Error {
	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", http.NoBody)
	if err != nil {
		panic(err)
	}

	var response http.Response

	response.StatusCode = status
	response.Header = headers
	response.Request = req

	var errOpenAI openai.Error

	errOpenAI.Code = code
	errOpenAI.Message = message
	errOpenAI.StatusCode = status
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
