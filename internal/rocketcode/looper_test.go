//nolint:exhaustruct,gocritic // Test fixtures intentionally use sparse SDK literals and value slices.
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
	newFunc   func(context.Context, responses.ResponseNewParams) (*responses.Response, error)
}

func (m *mockResponsesAPI) New(ctx context.Context, params responses.ResponseNewParams, _ ...option.RequestOption) (*responses.Response, error) {
	m.mu.Lock()
	m.calls = append(m.calls, params)
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

func (m *mockSessionStore) append(entry SessionEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.entries = append(m.entries, entry)

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
		for _, entry := range entries {
			if !yield(entry, nil) {
				return
			}
		}
	}
}

func discardSession(SessionEntry) error { return nil }

func TestLooperReloadsSessionWithCurrentRuntimeConfig(t *testing.T) {
	replayInput, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		{OfMessage: &responses.EasyInputMessageParam{Role: "user", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("earlier question")}, Type: "message"}},
		{OfMessage: &responses.EasyInputMessageParam{Role: "assistant", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("old answer")}, Phase: "final_answer", Type: "message"}},
		{OfReasoning: &responses.ResponseReasoningItemParam{ID: "rsn-old", Summary: []responses.ResponseReasoningItemSummaryParam{{Text: "old thought", Type: "summary_text"}}, EncryptedContent: openai.String("encrypted-old"), Type: "reasoning"}},
	})
	require.NoError(t, err)

	turn := SessionEntry{
		Version:     1,
		Type:        "turn",
		Timestamp:   time.Unix(1, 0).UTC(),
		Model:       "old-model",
		ReplayInput: replayInput,
	}

	mock := &mockResponsesAPI{responses: []*responses.Response{responseWithMessage("resp-new", "new answer")}}
	looper := &looper{
		Client:          mock,
		SystemPrompt:    "current system prompt",
		Model:           openai.ChatModelGPT5,
		ReasoningEffort: shared.ReasoningEffort("high"),
	}

	var saved []SessionEntry

	output := make(chan ChatResponse, 10)

	interrupts := make(chan os.Signal, 1)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "next question", Responses: output}

	close(input)

	err = looper.Loop(context.Background(), input, sessionEntries([]SessionEntry{turn}), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, interrupts)
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "new answer"}}, collectResponses(output))
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
	mock := &mockResponsesAPI{responses: []*responses.Response{responseWithMessage("resp-final", "done")}}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleDeveloper, Text: "keep this rule", Responses: output}

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
			mock := &mockResponsesAPI{responses: []*responses.Response{responseWithMessage("resp-final", "done")}}
			looper := &looper{Client: mock, Model: openai.ChatModelGPT5, expandInputPrompts: tc.enabled, promptExpansion: testPromptExpansionEnvironment(t)}
			output := make(chan ChatResponse, 10)
			turn, _, interrupted, err := looper.runTurn(context.Background(), output, nil, nil, PromptInput{Role: PromptInputRoleUser, Text: "before !`printf hello` after"})

			require.NoError(t, err)
			require.False(t, interrupted)

			wantJSON := fmt.Sprintf(`{"content":%q,"role":"user","type":"message"}`, tc.want)
			require.JSONEq(t, wantJSON, marshalJSON(t, mock.calls[0].Input.OfInputItemList[0]))
			require.JSONEq(t, wantJSON, string(turn.ReplayInput[0]))
		})
	}
}

func TestLooperClosesPromptResponseChannelAfterTurn(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{responseWithMessage("resp-final", "done")}}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5}
	responses := make(chan ChatResponse, 1)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "question", Responses: responses}

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "done"}}, collectResponses(responses))
}

func TestLoopClosesPromptResponsesWhenSessionLoadFails(t *testing.T) {
	looper := &looper{Client: &mockResponsesAPI{}, Model: openai.ChatModelGPT5}
	responses := make(chan ChatResponse, 1)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "question", Responses: responses}

	close(input)

	badSession := func(yield func(SessionEntry, error) bool) {
		yield(SessionEntry{Version: 1, Type: "turn", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":""}`)}}, nil)
	}

	err := looper.Loop(context.Background(), input, badSession, discardSession, make(chan os.Signal, 1))

	require.Error(t, err)

	select {
	case _, ok := <-responses:
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("prompt response channel was not closed")
	}
}

func TestLooperBuildParamsUsesConfiguredCompactThreshold(t *testing.T) {
	looper := &looper{CompactThreshold: 12345}

	params := looper.buildParams(nil)

	require.Len(t, params.ContextManagement, 1)
	require.Equal(t, "compaction", params.ContextManagement[0].Type)
	require.Equal(t, int64(12345), params.ContextManagement[0].CompactThreshold.Value)
	require.False(t, params.Store.Value)
}

func TestLooperBuildParamsIncludesHostedWebSearchTool(t *testing.T) {
	looper := &looper{Tools: map[string]looperTool{"websearch": webSearchTool()}}

	params := looper.buildParams(nil)

	require.Len(t, params.Tools, 1)
	require.Contains(t, marshalJSON(t, params.Tools), `"type":"web_search"`)
}

func TestLooperBuildParamsIncludesConfiguredVerbosity(t *testing.T) {
	looper := &looper{Verbosity: "low"}

	params := looper.buildParams(nil)

	require.Equal(t, responses.ResponseTextConfigVerbosityLow, params.Text.Verbosity)
}

func TestLooperPersistsAndReplaysCompactionItems(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{responseWithCompactionAndMessage("resp-compact", "encrypted-compact", "answer")}}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "question", Responses: output}

	close(input)

	interrupts := make(chan os.Signal, 1)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, interrupts)
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "answer"}}, collectResponses(output))

	history, turns, err := loadSession(sessionEntries(saved))
	require.NoError(t, err)
	require.Len(t, turns, 1)
	require.Len(t, turns[0].ReplayInput, 3)
	require.Len(t, history, 3)
	require.JSONEq(t, `{"encrypted_content":"encrypted-compact","id":"resp-compact-compaction","type":"compaction"}`, marshalJSON(t, history[1]))
}

func TestLooperPersistsAndReplaysWebSearchCalls(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{
		responseWithWebSearchAndMessage("resp-search", "golang release", "answer with citation"),
		responseWithMessage("resp-next", "next answer"),
	}}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5, Tools: map[string]looperTool{"websearch": webSearchTool()}}
	output := make(chan ChatResponse, 10)
	nextOutput := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 2)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "search", Responses: output}

	input <- PromptInput{Role: PromptInputRoleUser, Text: "continue", Responses: nextOutput}

	close(input)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "answer with citation"}}, collectResponses(output))
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "next answer"}}, collectResponses(nextOutput))
	require.Len(t, saved, 2)
	require.Len(t, saved[0].ReplayInput, 3)
	require.JSONEq(t, `{"action":{"queries":["golang release"],"query":"golang release","type":"search"},"id":"resp-search-web","status":"completed","type":"web_search_call"}`, string(saved[0].ReplayInput[1]))

	require.Len(t, mock.calls, 2)
	require.Contains(t, marshalJSON(t, mock.calls[1].Input.OfInputItemList), `"type":"web_search_call"`)
	require.Contains(t, marshalJSON(t, mock.calls[1].Input.OfInputItemList), `"query":"golang release"`)
}

func TestWebSearchOutputWithEmptyActionTypeIsTraceOnly(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{{
		ID: "resp-search",
		Output: []responses.ResponseOutputItemUnion{
			{ID: "resp-search-web", Type: "web_search_call", Status: "completed", Action: responses.ResponseOutputItemUnionAction{Type: ""}},
			{ID: "resp-search-msg", Type: "message", Role: "assistant", Status: "completed", Content: []responses.ResponseOutputMessageContentUnion{{Type: "output_text", Text: "done"}}},
		},
	}}}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5, Tools: map[string]looperTool{"websearch": webSearchTool()}}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "search", Responses: output}

	close(input)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "done"}}, collectResponses(output))
	require.Len(t, saved, 1)
	require.Len(t, saved[0].ReplayInput, 2)
	require.NotContains(t, string(saved[0].ReplayInput[1]), "web_search_call")
	require.Len(t, saved[0].OutputTrace, 1)
	require.Contains(t, string(saved[0].OutputTrace[0]), `"web_search_call"`)
}

func TestLooperInjectsCompactionSteering(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{
		responseWithCompactionAndMessage("resp-compact", "encrypted-compact", "answer"),
		responseWithMessage("resp-next", "next answer"),
	}}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5, CompactionSteering: "Use the compacted context carefully."}
	output := make(chan ChatResponse, 10)
	nextOutput := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 2)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "question", Responses: output}

	input <- PromptInput{Role: PromptInputRoleUser, Text: "next question", Responses: nextOutput}

	close(input)

	interrupts := make(chan os.Signal, 1)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, interrupts)
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "answer"}}, collectResponses(output))
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "next answer"}}, collectResponses(nextOutput))
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
	mock := &mockResponsesAPI{responses: []*responses.Response{
		responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{
			{ID: "tool-1", CallID: "call-1", Name: "first", Arguments: `{"step":1}`},
			{ID: "tool-2", CallID: "call-2", Name: "second", Arguments: `{"step":2}`},
		}),
		responseWithMessage("resp-final", "done"),
	}}

	var (
		callsMu sync.Mutex
		calls   []string
	)

	looper := &looper{
		Client: mock,
		Model:  openai.ChatModelGPT5,
		Permissions: PermissionSet{Buckets: []PermissionBucket{
			{Name: "first", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}},
			{Name: "second", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}},
		}},
		Tools: map[string]looperTool{
			"first": {
				Definition: responses.FunctionToolParam{Name: "first", Parameters: map[string]any{"type": "object"}, Strict: openai.Bool(true)},
				CallReplay: func(_ context.Context, args json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, []responses.ResponseInputItemUnionParam, error) {
					callsMu.Lock()
					defer callsMu.Unlock()

					calls = append(calls, "first:"+string(args))
					developerInput := responses.ResponseInputItemUnionParam{OfMessage: &responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleDeveloper, Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("first instructions")}, Type: "message"}}

					return textToolResult("first-result"), []responses.ResponseInputItemUnionParam{developerInput}, nil
				},
			},
			"second": {
				Definition: responses.FunctionToolParam{Name: "second", Parameters: map[string]any{"type": "object"}, Strict: openai.Bool(true)},
				Call: func(_ context.Context, args json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
					callsMu.Lock()
					defer callsMu.Unlock()

					calls = append(calls, "second:"+string(args))

					return textToolResult("second-result"), nil
				},
			},
		},
	}

	interrupts := make(chan os.Signal, 1)
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "run tools", Responses: output}

	close(input)

	var saved []SessionEntry

	err := looper.Loop(context.Background(), input, emptySession(), func(entry SessionEntry) error {
		saved = append(saved, entry)

		return nil
	}, interrupts)
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "done"}}, collectResponses(output))
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

		mock := &mockResponsesAPI{responses: []*responses.Response{
			responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{call}),
			responseWithMessage("resp-final", "recovered"),
		}}
		looper := &looper{
			Client:      mock,
			Model:       openai.ChatModelGPT5,
			Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: call.Name, Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}},
			Tools:       tools,
		}
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- PromptInput{Role: PromptInputRoleUser, Text: "run tool", Responses: output}

		close(input)
		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "recovered"}}, collectResponses(output))
		require.Len(t, mock.calls, 2)
		require.Contains(t, marshalJSON(t, mock.calls[1].Input.OfInputItemList), want)
	}

	t.Run("tool call error", func(t *testing.T) {
		run(t, map[string]looperTool{"fail": {
			Definition: responses.FunctionToolParam{Name: "fail", Parameters: map[string]any{"type": "object"}, Strict: openai.Bool(true)},
			Call: func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
				return ToolResult{}, errors.New("boom")
			},
		}}, responses.ResponseFunctionToolCall{ID: "tool-1", CallID: "call-1", Name: "fail", Arguments: `{}`}, "tool call failed: fail: boom")
	})

	t.Run("permission subject error", func(t *testing.T) {
		run(t, map[string]looperTool{"subject_fail": {
			Definition: responses.FunctionToolParam{Name: "subject_fail", Parameters: map[string]any{"type": "object"}, Strict: openai.Bool(true)},
			Subjects: func(json.RawMessage) ([]string, error) {
				return nil, errors.New("bad subject")
			},
			Call: func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
				t.Fatal("tool with subject error should not execute")
				return ToolResult{}, nil
			},
		}}, responses.ResponseFunctionToolCall{ID: "tool-1", CallID: "call-1", Name: "subject_fail", Arguments: `{}`}, "tool call failed: subject_fail: check permission: bad subject")
	})

	t.Run("unknown tool", func(t *testing.T) {
		run(t, nil, responses.ResponseFunctionToolCall{ID: "tool-1", CallID: "call-1", Name: "missing", Arguments: `{}`}, "tool call failed: missing: tool not found")
	})

	t.Run("webfetch HTTP error", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(server.Close)

		run(t, map[string]looperTool{"webfetch": {
			Definition: responses.FunctionToolParam{Name: "webfetch", Parameters: map[string]any{"type": "object"}, Strict: openai.Bool(true)},
			Subjects: func(raw json.RawMessage) ([]string, error) {
				params, err := decodeToolParams[webFetchToolParams](raw)
				if err != nil {
					return nil, err
				}

				return []string{params.URL}, nil
			},
			Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				params, err := decodeToolParams[webFetchToolParams](raw)
				if err != nil {
					return ToolResult{}, err
				}

				return webFetch(ctx, params)
			},
		}}, responses.ResponseFunctionToolCall{ID: "tool-1", CallID: "call-1", Name: "webfetch", Arguments: fmt.Sprintf(`{"url":%q}`, server.URL)}, "tool call failed: webfetch: request failed with status 404")
	})
}

func TestLooperKeepsContextCancellationFatalForToolCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	looper := &looper{
		Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: "slow", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}},
		Tools: map[string]looperTool{"slow": {
			Definition: responses.FunctionToolParam{Name: "slow", Parameters: map[string]any{"type": "object"}, Strict: openai.Bool(true)},
			Call: func(ctx context.Context, _ json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				<-ctx.Done()

				return ToolResult{}, ctx.Err()
			},
		}},
	}

	_, hadToolCalls, err := looper.dispatchToolCalls(ctx, responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{{
		ID:        "tool-1",
		CallID:    "call-1",
		Name:      "slow",
		Arguments: `{}`,
	}}), nil, nil)

	require.Error(t, err)
	require.True(t, hadToolCalls)
	require.Contains(t, err.Error(), "run tool calls")
}

func TestLooperSendsAndReplaysUserAttachments(t *testing.T) {
	attachments := []Attachment{
		{MIME: "image/png", Filename: "image.png", URL: "data:image/png;base64,aW1hZ2U="},
		{MIME: "application/pdf", Filename: "doc.pdf", URL: "data:application/pdf;base64,cGRm"},
	}
	mock := &mockResponsesAPI{responses: []*responses.Response{responseWithMessage("resp-final", "done")}}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Text: "see attached", Attachments: attachments, Responses: output}

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
	mock := &mockResponsesAPI{responses: []*responses.Response{responseWithMessage("resp-final", "done")}}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleDeveloper, Text: "see attached", Attachments: attachments, Responses: output}

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
	mock := &mockResponsesAPI{responses: []*responses.Response{
		responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{{ID: "tool-1", CallID: "call-1", Name: "read", Arguments: `{}`}}),
		responseWithMessage("resp-final", "done"),
	}}
	looper := &looper{
		Client:      mock,
		Model:       openai.ChatModelGPT5,
		Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: "read", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}},
		Tools: map[string]looperTool{"read": {
			Definition: responses.FunctionToolParam{Name: "read", Parameters: map[string]any{"type": "object"}, Strict: openai.Bool(true)},
			Call: func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
				return ToolResult{Output: "Image read successfully", Attachments: []Attachment{{MIME: "image/png", Filename: "image.png", URL: "data:image/png;base64,aW1hZ2U="}}}, nil
			},
		}},
	}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "read image", Responses: output}

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	serialized := marshalJSON(t, mock.calls[1].Input.OfInputItemList)
	require.Contains(t, serialized, `"type":"input_text"`)
	require.Contains(t, serialized, `"type":"input_image"`)
}

func TestLooperDeniesToolCallsInBand(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{
		responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{{ID: "tool-1", CallID: "call-1", Name: "bash", Arguments: `{"command":"rm -rf tmp","description":"remove tmp"}`}}),
		responseWithMessage("resp-final", "recovered"),
	}}
	looper := &looper{
		Client: mock,
		Model:  openai.ChatModelGPT5,
		Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: "bash", Rules: []PermissionRule{
			{Pattern: "*", Action: permissionDeny},
			{Pattern: "git status *", Action: permissionAllow},
		}}}},
		Tools: map[string]looperTool{
			"bash": {
				Definition: responses.FunctionToolParam{Name: "bash", Parameters: map[string]any{"type": "object"}, Strict: openai.Bool(true)},
				Permission: "bash",
				Subjects: func(raw json.RawMessage) ([]string, error) {
					var params bashParams
					require.NoError(t, json.Unmarshal(raw, &params))

					return bashPermissionSubjects(params.Command), nil
				},
				Call: func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
					t.Fatal("denied tool should not execute")
					return ToolResult{}, nil
				},
			},
		},
	}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "run denied tool", Responses: output}

	close(input)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "recovered"}}, collectResponses(output))
	require.Len(t, mock.calls, 2)
	require.Contains(t, marshalJSON(t, mock.calls[1].Input.OfInputItemList), "tool call denied")
}

func TestLooperAppliesWebFetchURLPermissions(t *testing.T) {
	t.Run("denies non matching URL", func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{
			responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{{ID: "tool-1", CallID: "call-1", Name: "webfetch", Arguments: `{"url":"https://blocked.example/page"}`}}),
			responseWithMessage("resp-final", "recovered"),
		}}
		looper := &looper{
			Client: mock,
			Model:  openai.ChatModelGPT5,
			Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: "webfetch", Rules: []PermissionRule{
				{Pattern: "*", Action: permissionDeny},
				{Pattern: "https://allowed.example/*", Action: permissionAllow},
			}}}},
			Tools: map[string]looperTool{"webfetch": {
				Definition: responses.FunctionToolParam{Name: "webfetch", Parameters: map[string]any{"type": "object"}, Strict: openai.Bool(true)},
				Permission: "webfetch",
				Subjects: func(raw json.RawMessage) ([]string, error) {
					params, err := decodeToolParams[webFetchToolParams](raw)
					require.NoError(t, err)

					return []string{params.URL}, nil
				},
				Call: func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
					t.Fatal("denied webfetch should not execute")
					return ToolResult{}, nil
				},
			}},
		}
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- PromptInput{Role: PromptInputRoleUser, Text: "fetch docs", Responses: output}

		close(input)

		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		serialized := marshalJSON(t, mock.calls[1].Input.OfInputItemList)
		require.Contains(t, serialized, `permission \"webfetch\" rejected subject \"https://blocked.example/page\"`)
	})

	t.Run("allows matching URL", func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{
			responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{{ID: "tool-1", CallID: "call-1", Name: "webfetch", Arguments: `{"url":"https://allowed.example/page"}`}}),
			responseWithMessage("resp-final", "done"),
		}}
		called := false
		looper := &looper{
			Client: mock,
			Model:  openai.ChatModelGPT5,
			Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: "webfetch", Rules: []PermissionRule{
				{Pattern: "*", Action: permissionDeny},
				{Pattern: "https://allowed.example/*", Action: permissionAllow},
			}}}},
			Tools: map[string]looperTool{"webfetch": {
				Definition: responses.FunctionToolParam{Name: "webfetch", Parameters: map[string]any{"type": "object"}, Strict: openai.Bool(true)},
				Permission: "webfetch",
				Subjects: func(raw json.RawMessage) ([]string, error) {
					params, err := decodeToolParams[webFetchToolParams](raw)
					require.NoError(t, err)

					return []string{params.URL}, nil
				},
				Call: func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
					called = true
					return textToolResult("fetched"), nil
				},
			}},
		}
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- PromptInput{Role: PromptInputRoleUser, Text: "fetch docs", Responses: output}

		close(input)

		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		require.True(t, called)
		require.Contains(t, marshalJSON(t, mock.calls[1].Input.OfInputItemList), "fetched")
	})
}

func TestLooperGatesSkillByName(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{
		responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{
			{ID: "tool-1", CallID: "call-1", Name: "skill", Arguments: `{"name":"git-review"}`},
			{ID: "tool-2", CallID: "call-2", Name: "skill", Arguments: `{"name":"docs-helper"}`},
		}),
		responseWithMessage("resp-final", "done"),
	}}
	calls := []string{}
	looper := &looper{
		Client: mock,
		Model:  openai.ChatModelGPT5,
		Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: "skill", Rules: []PermissionRule{
			{Pattern: "*", Action: permissionDeny},
			{Pattern: "docs-helper", Action: permissionAllow},
		}}}},
		Tools: map[string]looperTool{
			"skill": {
				Definition: responses.FunctionToolParam{Name: "skill", Parameters: map[string]any{"type": "object"}, Strict: openai.Bool(true)},
				Permission: "skill",
				Subjects: func(raw json.RawMessage) ([]string, error) {
					var params struct {
						Name string `json:"name"`
					}
					require.NoError(t, json.Unmarshal(raw, &params))

					return []string{params.Name}, nil
				},
				Call: func(_ context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
					var params struct {
						Name string `json:"name"`
					}
					require.NoError(t, json.Unmarshal(raw, &params))
					calls = append(calls, params.Name)

					return textToolResult("loaded " + params.Name), nil
				},
			},
		},
	}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "load skills", Responses: output}

	close(input)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []string{"docs-helper"}, calls)
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "done"}}, collectResponses(output))
	serialized := marshalJSON(t, mock.calls[1].Input.OfInputItemList)
	require.Contains(t, serialized, "tool call denied")
	require.Contains(t, serialized, "loaded docs-helper")
}

func TestLooperEmitsToolDiagnosticsWhenEnabled(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{
		responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{{ID: "tool-1", CallID: "call-1", Name: "skill", Arguments: `{"name":"current-time"}`}}),
		responseWithMessage("resp-final", "done"),
	}}
	looper := &looper{
		Client:      mock,
		Model:       openai.ChatModelGPT5,
		Diagnostics: true,
		Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: "skill", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}},
		Tools: map[string]looperTool{
			"skill": {
				Definition: responses.FunctionToolParam{Name: "skill", Parameters: map[string]any{"type": "object"}, Strict: openai.Bool(true)},
				Call: func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
					return textToolResult("loaded current-time"), nil
				},
			},
		},
	}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "what time is it?", Responses: output}

	close(input)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{
		{Kind: ChatResponseAssistantTool, Tool: &ToolDiagnostic{Phase: toolDiagnosticPhaseCall, Name: "skill", Arguments: json.RawMessage(`{"name":"current-time"}`)}},
		{Kind: ChatResponseAssistantTool, Tool: &ToolDiagnostic{Phase: toolDiagnosticPhaseResult, Name: "skill", Result: "loaded current-time"}},
		{Kind: ChatResponseAssistantMessage, Text: "done"},
	}, collectResponses(output))
}

func TestLooperEmitsHostedWebSearchDiagnosticsWhenEnabled(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{responseWithWebSearchAndMessage("resp-search", "opencode", "found it")}}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5, Diagnostics: true, Tools: map[string]looperTool{"websearch": webSearchTool()}}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "search web", Responses: output}

	close(input)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	require.Equal(t, []ChatResponse{
		{Kind: ChatResponseAssistantTool, Tool: &ToolDiagnostic{Phase: toolDiagnosticPhaseCall, Name: "websearch", Status: "completed", Action: json.RawMessage(`{"queries":["opencode"],"query":"opencode","type":"search"}`)}},
		{Kind: ChatResponseAssistantMessage, Text: "found it"},
	}, collectResponses(output))
}

func TestEmitDiagnosticChatResponseDropsWhenUnavailable(t *testing.T) {
	emitDiagnosticChatResponse(nil, ChatResponse{Kind: ChatResponseAssistantTool, Tool: &ToolDiagnostic{Name: "nil"}})

	unbuffered := make(chan ChatResponse)
	emitDiagnosticChatResponse(unbuffered, ChatResponse{Kind: ChatResponseAssistantTool, Tool: &ToolDiagnostic{Name: "blocked"}})

	select {
	case item := <-unbuffered:
		t.Fatalf("unexpected diagnostic delivered on blocked channel: %#v", item)
	default:
	}

	buffered := make(chan ChatResponse, 1)
	buffered <- ChatResponse{Kind: ChatResponseAssistantMessage, Text: "existing"}

	emitDiagnosticChatResponse(buffered, ChatResponse{Kind: ChatResponseAssistantTool, Tool: &ToolDiagnostic{Name: "full"}})
	require.Equal(t, ChatResponse{Kind: ChatResponseAssistantMessage, Text: "existing"}, <-buffered)
}

func TestLooperTrapsDoomLoopInBand(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{
		responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{
			{ID: "tool-1", CallID: "call-1", Name: "repeat", Arguments: `{"b":2,"a":1}`},
			{ID: "tool-2", CallID: "call-2", Name: "repeat", Arguments: `{"a":1,"b":2}`},
			{ID: "tool-3", CallID: "call-3", Name: "repeat", Arguments: `{"a":1,"b":2}`},
		}),
		responseWithMessage("resp-final", "done"),
	}}

	var (
		callsMu sync.Mutex
		calls   int
	)

	looper := &looper{
		Client:      mock,
		Model:       openai.ChatModelGPT5,
		Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: "repeat", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}},
		Tools: map[string]looperTool{
			"repeat": {
				Definition: responses.FunctionToolParam{Name: "repeat", Parameters: map[string]any{"type": "object"}, Strict: openai.Bool(true)},
				Call: func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
					callsMu.Lock()
					defer callsMu.Unlock()

					calls++

					return textToolResult("ok"), nil
				},
			},
		},
	}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "repeat", Responses: output}

	close(input)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.NoError(t, err)
	callsMu.Lock()
	gotCalls := calls
	callsMu.Unlock()
	require.Equal(t, 2, gotCalls)
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "done"}}, collectResponses(output))
	require.Contains(t, marshalJSON(t, mock.calls[1].Input.OfInputItemList), "repeated identical")
}

func TestLooperPrintsReasoningSummary(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{responseWithReasoningAndMessage("resp-reason", "think briefly", "final answer")}}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5}

	output := make(chan ChatResponse, 10)
	interrupts := make(chan os.Signal, 1)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "question", Responses: output}

	close(input)
	err := looper.Loop(context.Background(), input, emptySession(), discardSession, interrupts)
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{{Kind: ChatResponseReasoningSummary, Text: "think briefly"}, {Kind: ChatResponseAssistantMessage, Text: "final answer"}}, collectResponses(output))
}

func TestLooperUpdatesSessionStoreAfterCompletedTurn(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{responseWithMessage("resp-save", "saved answer")}}
	store := &mockSessionStore{}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "question", Responses: output}

	close(input)

	interrupts := make(chan os.Signal, 1)
	err := looper.Loop(context.Background(), input, emptySession(), store.append, interrupts)
	require.NoError(t, err)
	require.Len(t, store.saves, 1)
	require.Len(t, store.saves[0], 1)
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantMessage, Text: "saved answer"}}, collectResponses(output))
}

func TestLooperOmitsInterruptedTurnsFromSession(t *testing.T) {
	started := make(chan struct{})
	mock := &mockResponsesAPI{newFunc: func(ctx context.Context, _ responses.ResponseNewParams) (*responses.Response, error) {
		close(started)
		<-ctx.Done()

		return nil, ctx.Err()
	}}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5}
	interrupts := make(chan os.Signal, 1)

	var saved []SessionEntry

	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "will interrupt", Responses: output}

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
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantCommentary, Text: "(interrupted)"}}, collectResponses(output))

	_, turns, err := loadSession(sessionEntries(saved))
	require.NoError(t, err)
	require.Empty(t, turns)
}

func TestLooperPrintsCommentaryResponses(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{responseWithCommentaryAndMessage("resp-commentary", "working", "final")}}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "question", Responses: output}

	close(input)

	interrupts := make(chan os.Signal, 1)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, interrupts)
	require.NoError(t, err)
	require.Equal(t, []ChatResponse{{Kind: ChatResponseAssistantCommentary, Text: "working"}, {Kind: ChatResponseAssistantMessage, Text: "final"}}, collectResponses(output))
}

func TestLooperRetriesRateLimitExceededFailedResponse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mock := &mockResponsesAPI{responses: []*responses.Response{
			failedResponseWithCode("resp-rate", responses.ResponseErrorCodeRateLimitExceeded, "too many requests"),
			responseWithMessage("resp-ok", "done"),
		}}
		looper := &looper{Client: mock, Model: openai.ChatModelGPT5, Diagnostics: true}
		output := make(chan ChatResponse, 10)

		input := make(chan PromptInput, 1)
		input <- PromptInput{Role: PromptInputRoleUser, Text: "question", Responses: output}

		close(input)

		err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

		require.NoError(t, err)
		require.Len(t, mock.calls, 2)
		require.Equal(t, []ChatResponse{
			{
				Kind: ChatResponseAssistantTool,
				Provider: &ProviderDiagnostic{
					Phase:          providerDiagnosticRetry,
					ResponseStatus: string(responses.ResponseStatusFailed),
					Code:           string(responses.ResponseErrorCodeRateLimitExceeded),
					Message:        "too many requests",
					Attempt:        1,
					RetryAfter:     "1m0s",
					ResponseID:     "resp-rate",
				},
			},
			{Kind: ChatResponseAssistantMessage, Text: "done"},
		}, collectResponses(output))
	})
}

func TestLooperReportsFailedResponsesInDiagnostics(t *testing.T) {
	mock := &mockResponsesAPI{responses: []*responses.Response{
		failedResponseWithCode("resp-invalid", responses.ResponseErrorCodeInvalidPrompt, "bad prompt"),
	}}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5, Diagnostics: true}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "question", Responses: output}

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.EqualError(t, err, "run turn: request response: response failed: invalid_prompt: bad prompt")
	require.Equal(t, []ChatResponse{{
		Kind: ChatResponseAssistantTool,
		Provider: &ProviderDiagnostic{
			Phase:          providerDiagnosticError,
			ResponseStatus: string(responses.ResponseStatusFailed),
			Code:           string(responses.ResponseErrorCodeInvalidPrompt),
			Message:        "bad prompt",
			ResponseID:     "resp-invalid",
		},
	}}, collectResponses(output))
}

func TestLooperReportsOpenAIRequestErrorsInDiagnostics(t *testing.T) {
	mock := &mockResponsesAPI{err: openAIError("rate_limit_exceeded", "slow down", http.StatusTooManyRequests, nil)}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5, Diagnostics: true}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "question", Responses: output}

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.EqualError(t, err, "run turn: request response: new response: POST \"https://api.openai.com/v1/responses\": 429 Too Many Requests ")
	require.Len(t, mock.calls, 1)
	require.Equal(t, []ChatResponse{{
		Kind: ChatResponseAssistantTool,
		Provider: &ProviderDiagnostic{
			Phase:      providerDiagnosticError,
			HTTPStatus: http.StatusTooManyRequests,
			Code:       string(responses.ResponseErrorCodeRateLimitExceeded),
			Message:    "slow down",
		},
	}}, collectResponses(output))
}

func TestLooperReportsRequestErrorsInDiagnostics(t *testing.T) {
	mock := &mockResponsesAPI{err: errors.New("network exploded")}
	looper := &looper{Client: mock, Model: openai.ChatModelGPT5, Diagnostics: true}
	output := make(chan ChatResponse, 10)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "question", Responses: output}

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.EqualError(t, err, "run turn: request response: new response: network exploded")
	require.Equal(t, []ChatResponse{{
		Kind:     ChatResponseAssistantTool,
		Provider: &ProviderDiagnostic{Phase: providerDiagnosticError, Message: "network exploded"},
	}}, collectResponses(output))
}

func TestLooperLoopRequiresDependencies(t *testing.T) {
	looper := &looper{}
	input := make(chan PromptInput)
	interrupts := make(chan os.Signal, 1)

	require.EqualError(t, looper.Loop(nil, input, emptySession(), discardSession, interrupts), "context is required") //nolint:staticcheck // Exercises nil context validation.
	require.EqualError(t, looper.Loop(context.Background(), nil, emptySession(), discardSession, interrupts), "input channel is required")
	require.EqualError(t, looper.Loop(context.Background(), input, nil, discardSession, interrupts), "sessionIn is required")
	require.EqualError(t, looper.Loop(context.Background(), input, emptySession(), nil, interrupts), "sessionOut is required")
	require.EqualError(t, looper.Loop(context.Background(), input, emptySession(), discardSession, nil), "interrupts channel is required")

	close(input)
}

func TestLooperLoopRequiresPromptResponseChannel(t *testing.T) {
	looper := &looper{}

	input := make(chan PromptInput, 1)
	input <- textPromptInput("question")

	close(input)

	err := looper.Loop(context.Background(), input, emptySession(), discardSession, make(chan os.Signal, 1))

	require.EqualError(t, err, "prompt response channel is required")
}

func responseWithMessage(id, text string) *responses.Response {
	return &responses.Response{
		ID: id,
		Output: []responses.ResponseOutputItemUnion{{
			ID:     id + "-msg",
			Type:   "message",
			Role:   "assistant",
			Status: "completed",
			Content: []responses.ResponseOutputMessageContentUnion{{
				Type: "output_text",
				Text: text,
			}},
		}},
	}
}

func failedResponseWithCode(id string, code responses.ResponseErrorCode, message string) *responses.Response {
	return &responses.Response{
		ID:     id,
		Status: responses.ResponseStatusFailed,
		Error: responses.ResponseError{
			Code:    code,
			Message: message,
		},
	}
}

func openAIError(code, message string, status int, headers http.Header) *openai.Error {
	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", nil)
	if err != nil {
		panic(err)
	}

	return &openai.Error{
		Code:       code,
		Message:    message,
		StatusCode: status,
		Request:    req,
		Response: &http.Response{
			StatusCode: status,
			Header:     headers,
			Request:    req,
		},
	}
}

func responseWithFunctionCalls(id string, calls []responses.ResponseFunctionToolCall) *responses.Response {
	output := make([]responses.ResponseOutputItemUnion, 0, len(calls))
	for _, call := range calls {
		output = append(output, responses.ResponseOutputItemUnion{
			ID:        call.ID,
			Type:      "function_call",
			CallID:    call.CallID,
			Name:      call.Name,
			Arguments: responses.ResponseOutputItemUnionArguments{OfString: call.Arguments},
			Status:    "completed",
		})
	}

	return &responses.Response{ID: id, Output: output}
}

func responseWithReasoningAndMessage(id, reasoning, text string) *responses.Response {
	return &responses.Response{
		ID: id,
		Output: []responses.ResponseOutputItemUnion{
			{
				ID:               id + "-reasoning",
				Type:             "reasoning",
				EncryptedContent: "encrypted",
				Summary: []responses.ResponseReasoningItemSummary{{
					Text: reasoning,
					Type: "summary_text",
				}},
			},
			{
				ID:     id + "-msg",
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Content: []responses.ResponseOutputMessageContentUnion{{
					Type: "output_text",
					Text: text,
				}},
			},
		},
	}
}

func responseWithCompactionAndMessage(id, encryptedContent, text string) *responses.Response {
	return &responses.Response{
		ID: id,
		Output: []responses.ResponseOutputItemUnion{
			{
				ID:               id + "-compaction",
				Type:             "compaction",
				EncryptedContent: encryptedContent,
			},
			{
				ID:     id + "-msg",
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Content: []responses.ResponseOutputMessageContentUnion{{
					Type: "output_text",
					Text: text,
				}},
			},
		},
	}
}

func responseWithWebSearchAndMessage(id, query, text string) *responses.Response {
	return &responses.Response{
		ID: id,
		Output: []responses.ResponseOutputItemUnion{
			{
				ID:     id + "-web",
				Type:   "web_search_call",
				Status: "completed",
				Action: responses.ResponseOutputItemUnionAction{
					Type:    "search",
					Query:   query,
					Queries: []string{query},
				},
			},
			{
				ID:     id + "-msg",
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Content: []responses.ResponseOutputMessageContentUnion{{
					Type: "output_text",
					Text: text,
				}},
			},
		},
	}
}

func responseWithCommentaryAndMessage(id, commentary, text string) *responses.Response {
	return &responses.Response{
		ID: id,
		Output: []responses.ResponseOutputItemUnion{
			{
				ID:     id + "-commentary",
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Phase:  "commentary",
				Content: []responses.ResponseOutputMessageContentUnion{{
					Type: "output_text",
					Text: commentary,
				}},
			},
			{
				ID:     id + "-msg",
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Phase:  "final_answer",
				Content: []responses.ResponseOutputMessageContentUnion{{
					Type: "output_text",
					Text: text,
				}},
			},
		},
	}
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
