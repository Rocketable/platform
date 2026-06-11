package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"golang.org/x/sync/errgroup"
)

const reasoningEncryptedContent responses.ResponseIncludable = "reasoning.encrypted_content"
const defaultCompactThreshold int64 = 200000
const providerRateLimitRetryMinDelay = time.Minute

var errTurnInterrupted = errors.New("turn interrupted")

type responsesAPI interface {
	New(context.Context, *responses.ResponseNewParams, ...option.RequestOption) (*responses.Response, error)
}

type responseServiceClient struct {
	service *responses.ResponseService
}

func (c responseServiceClient) New(ctx context.Context, params *responses.ResponseNewParams, opts ...option.RequestOption) (*responses.Response, error) {
	resp, err := c.service.New(ctx, *params, opts...)
	if err != nil {
		return nil, fmt.Errorf("create response: %w", err)
	}

	return resp, nil
}

// looperTool describes one callable tool available to the runtime.
type looperTool struct {
	Definition         responses.FunctionToolParam
	Hosted             responses.ToolUnionParam
	Call               func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error)
	CallReplay         func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, []responses.ResponseInputItemUnionParam, error)
	Permission         string
	Subjects           func(json.RawMessage) ([]string, error)
	VisibilitySubjects []string
}

type toolCallMetadata struct {
	subagentIndex int
	subagentTotal int
}

// looper runs conversational turns against the configured model and tools.
type looper struct {
	agent              Agent
	Client             responsesAPI
	SystemPrompt       string
	Model              shared.ResponsesModel
	ReasoningEffort    shared.ReasoningEffort
	Verbosity          string
	CompactThreshold   int64
	CompactionSteering string
	ParallelToolCalls  int
	ResponseFormat     responses.ResponseFormatTextConfigUnionParam
	Permissions        PermissionSet
	Tools              map[string]looperTool
	RewriteHistory     func([]responses.ResponseInputItemUnionParam) []responses.ResponseInputItemUnionParam
	Diagnostics        bool
	expandInputPrompts bool
	promptExpansion    promptExpansionEnvironment
}

// Runtime is the concrete RocketCode loop runtime returned by New.
type Runtime = looper

// Looper processes prompt input streams with a configured runtime.
type Looper interface {
	Loop(ctx context.Context, input <-chan PromptInput, sessionIn iter.Seq2[SessionEntry, error], sessionOut func(SessionEntry) error, interrupts <-chan os.Signal) error
}

type toolCallSignature struct {
	name string
	args string
}

type dispatchedToolOutput struct {
	Param       responses.ResponseInputItemFunctionCallOutputParam
	Result      ToolResult
	ReplayInput []responses.ResponseInputItemUnionParam
}

type doomLoopTrap struct {
	recent []toolCallSignature
}

// ChatResponse is one user-visible response item emitted by the runtime.
type ChatResponse struct {
	Kind     string
	Text     string
	Tool     *ToolDiagnostic
	Subagent *SubagentDiagnostic
	Provider *ProviderDiagnostic
}

// ToolDiagnostic describes a tool runtime diagnostic emitted when diagnostics are enabled.
type ToolDiagnostic struct {
	Phase     string          `json:"phase"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Result    string          `json:"result,omitempty"`
	Status    string          `json:"status,omitempty"`
	Action    json.RawMessage `json:"action,omitempty"`
}

// SubagentDiagnostic describes a subagent runtime diagnostic emitted when diagnostics are enabled.
type SubagentDiagnostic struct {
	Name     string              `json:"name"`
	Label    string              `json:"label"`
	Index    int                 `json:"index,omitempty"`
	Total    int                 `json:"total,omitempty"`
	Text     string              `json:"text,omitempty"`
	Tool     *ToolDiagnostic     `json:"tool,omitempty"`
	Subagent *SubagentDiagnostic `json:"subagent,omitempty"`
	Provider *ProviderDiagnostic `json:"provider,omitempty"`
}

// ProviderDiagnostic describes provider request diagnostics emitted when diagnostics are enabled.
type ProviderDiagnostic struct {
	Phase          string `json:"phase"`
	HTTPStatus     int    `json:"http_status,omitempty"`
	ResponseStatus string `json:"response_status,omitempty"`
	Code           string `json:"code,omitempty"`
	Message        string `json:"message,omitempty"`
	Attempt        int    `json:"attempt,omitempty"`
	RetryAfter     string `json:"retry_after,omitempty"`
	ResponseID     string `json:"response_id,omitempty"`
}

const (
	// ChatResponseAssistantMessage identifies final assistant message output.
	ChatResponseAssistantMessage = "assistant_message"
	// ChatResponseAssistantCommentary identifies assistant progress/commentary output.
	ChatResponseAssistantCommentary = "assistant_commentary"
	// ChatResponseAssistantTool identifies structured tool and subagent diagnostics.
	ChatResponseAssistantTool = "assistant_tool"
	// ChatResponseReasoningSummary identifies reasoning summary output.
	ChatResponseReasoningSummary = "reasoning_summary"
)

const (
	toolDiagnosticPhaseCall   = "call"
	toolDiagnosticPhaseResult = "result"
	providerDiagnosticRetry   = "retry"
	providerDiagnosticError   = "error"
)

type responseFailureError struct {
	responseID string
	status     responses.ResponseStatus
	code       responses.ResponseErrorCode
	message    string
}

func (e *responseFailureError) Error() string {
	if e == nil {
		return "response failed"
	}

	if e.code != "" && e.message != "" {
		return fmt.Sprintf("response failed: %s: %s", e.code, e.message)
	}

	if e.code != "" {
		return fmt.Sprintf("response failed: %s", e.code)
	}

	if e.message != "" {
		return "response failed: " + e.message
	}

	return fmt.Sprintf("response failed with status %q", e.status)
}

// SessionEntry is one denormalized persisted session record.
type SessionEntry struct {
	Version     int               `json:"version"`
	Type        string            `json:"type"`
	Timestamp   time.Time         `json:"timestamp"`
	ResponseID  string            `json:"response_id,omitempty"`
	Model       string            `json:"model,omitempty"`
	ReplayInput []json.RawMessage `json:"replay_input,omitempty"`
	OutputTrace []json.RawMessage `json:"output_trace,omitempty"`
}

// Loop processes input lines until input closes or a runtime error occurs.
func (l *looper) Loop(
	ctx context.Context,
	input <-chan PromptInput,
	sessionIn iter.Seq2[SessionEntry, error],
	sessionOut func(SessionEntry) error,
	interrupts <-chan os.Signal,
) (err error) {
	if ctx == nil {
		return errors.New("context is required")
	}

	if input == nil {
		return errors.New("input channel is required")
	}

	if sessionIn == nil {
		return errors.New("sessionIn is required")
	}

	if sessionOut == nil {
		return errors.New("sessionOut is required")
	}

	if interrupts == nil {
		return errors.New("interrupts channel is required")
	}

	var history []responses.ResponseInputItemUnionParam

	loaded := false

	for line := range input {
		if line.Responses == nil {
			return errors.New("prompt response channel is required")
		}

		turnOutput := line.Responses

		if line.Text == "" && len(line.Attachments) == 0 {
			close(turnOutput)

			continue
		}

		if !loaded {
			var err error

			history, _, err = loadSession(sessionIn)
			if err != nil {
				close(turnOutput)

				return err
			}

			loaded = true
		}

		turn, rendered, interrupted, err := l.runTurn(ctx, turnOutput, interrupts, history, line)
		if err != nil {
			close(turnOutput)

			return fmt.Errorf("run turn: %w", err)
		}

		if interrupted {
			close(turnOutput)

			continue
		}

		if err := sessionOut(turn); err != nil {
			close(turnOutput)

			return fmt.Errorf("append session turn: %w", err)
		}

		items, err := ReplayInputToParams(turn.ReplayInput)
		if err != nil {
			close(turnOutput)

			return err
		}

		history = append(history, items...)

		for _, item := range rendered {
			emitChatResponse(turnOutput, item)
		}

		close(turnOutput)
	}

	return nil
}

func (l *looper) runTurn(
	ctx context.Context,
	output chan<- ChatResponse,
	interrupts <-chan os.Signal,
	baseHistory []responses.ResponseInputItemUnionParam,
	input PromptInput,
) (SessionEntry, []ChatResponse, bool, error) {
	var emptyRecord SessionEntry

	if l.expandInputPrompts {
		input.Text = l.promptExpansion.expandShellCommands(ctx, input.Text)
	}

	promptItem := promptInputMessage(input)
	turnItems := []responses.ResponseInputItemUnionParam{promptItem}

	replayInput, err := ReplayInputFromParams(turnItems)
	if err != nil {
		return emptyRecord, nil, false, err
	}

	record := SessionEntry{
		Version:     1,
		Type:        "turn",
		Timestamp:   time.Now().UTC(),
		Model:       l.Model,
		ReplayInput: replayInput,
	}

	turnCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var group errgroup.Group

	defer func() {
		cancel(nil)

		if errWait := group.Wait(); errWait != nil {
			cancel(errWait)
		}
	}()

	if interrupts != nil {
		group.Go(func() error {
			select {
			case <-turnCtx.Done():
				return nil
			case <-interrupts:
				cancel(errTurnInterrupted)

				emitChatResponse(output, ChatResponse{Kind: ChatResponseAssistantCommentary, Text: "(interrupted)"})

				return nil
			}
		})
	}

	rendered := []ChatResponse{}
	doomLoop := doomLoopTrap{recent: nil}

	for {
		history := append(append([]responses.ResponseInputItemUnionParam{}, baseHistory...), turnItems...)
		history = l.rewriteHistory(history)
		history = pruneHistoryBeforeLatestCompaction(history)

		params := l.buildParams(history)

		resp, err := l.newResponseWithProviderRetry(turnCtx, &params, output)
		if err != nil {
			if errors.Is(context.Cause(turnCtx), errTurnInterrupted) {
				return emptyRecord, nil, true, nil
			}

			return emptyRecord, nil, false, fmt.Errorf("request response: %w", err)
		}

		record.ResponseID = resp.ID
		l.emitHostedToolDiagnostics(output, resp.Output)
		rendered = append(rendered, responseChatResponses(resp.Output)...)

		hadCompaction := false

		for i := range resp.Output {
			if resp.Output[i].Type == "compaction" {
				hadCompaction = true
			}

			asInput, ok := responseOutputToReplayInput(&resp.Output[i])
			if !ok {
				if trace, err := json.Marshal(resp.Output[i]); err == nil {
					record.OutputTrace = append(record.OutputTrace, trace)
				}

				continue
			}

			if err := appendReplayInput(&record, &asInput); err != nil {
				return emptyRecord, nil, false, err
			}

			turnItems = append(turnItems, asInput)
		}

		if hadCompaction && l.CompactionSteering != "" {
			steeringInput := inputMessageParam(responses.EasyInputMessageRole("developer"), easyInputStringContent(l.CompactionSteering))

			if err := appendReplayInput(&record, &steeringInput); err != nil {
				return emptyRecord, nil, false, err
			}

			turnItems = append(turnItems, steeringInput)
		}

		toolOutputs, hadToolCalls, err := l.dispatchToolCalls(turnCtx, resp, &doomLoop, output)
		if err != nil {
			if errors.Is(context.Cause(turnCtx), errTurnInterrupted) {
				return emptyRecord, nil, true, nil
			}

			return emptyRecord, nil, false, fmt.Errorf("dispatch tool calls: %w", err)
		}

		if !hadToolCalls {
			return record, rendered, false, nil
		}

		for i := range toolOutputs {
			toolInput := functionCallOutputInputItem(&toolOutputs[i].Param)

			if err := appendReplayInput(&record, &toolInput); err != nil {
				return emptyRecord, nil, false, err
			}

			turnItems = append(turnItems, toolInput)
		}

		for i := range toolOutputs {
			for j := range toolOutputs[i].ReplayInput {
				replayInput := &toolOutputs[i].ReplayInput[j]
				if err := appendReplayInput(&record, replayInput); err != nil {
					return emptyRecord, nil, false, err
				}

				turnItems = append(turnItems, *replayInput)
			}
		}
	}
}

func appendReplayInput(record *SessionEntry, item *responses.ResponseInputItemUnionParam) error {
	raw, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{*item})
	if err != nil {
		return err
	}

	record.ReplayInput = append(record.ReplayInput, raw...)

	return nil
}

func (l *looper) newResponseWithProviderRetry(ctx context.Context, params *responses.ResponseNewParams, output chan<- ChatResponse) (*responses.Response, error) {
	attempt := 0

	for {
		var raw *http.Response

		resp, err := l.Client.New(ctx, params, option.WithResponseInto(&raw))
		if err != nil {
			if ctx.Err() == nil {
				diagnostic := ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: 0, ResponseStatus: "", Code: "", Message: err.Error(), Attempt: 0, RetryAfter: "", ResponseID: ""}
				if errAPI, ok := errors.AsType[*openai.Error](err); ok {
					diagnostic.Code = errAPI.Code
					diagnostic.Message = errAPI.Message
					diagnostic.HTTPStatus = errAPI.StatusCode
				}

				l.emitProviderDiagnostic(output, &diagnostic)
			}

			return nil, fmt.Errorf("new response: %w", err)
		}

		if resp == nil {
			err := errors.New("missing response")
			l.emitProviderDiagnostic(output, &ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: 0, ResponseStatus: "", Code: "", Message: err.Error(), Attempt: 0, RetryAfter: "", ResponseID: ""})

			return nil, err
		}

		if resp.Status != responses.ResponseStatusFailed {
			return resp, nil
		}

		err = &responseFailureError{
			responseID: resp.ID,
			status:     resp.Status,
			code:       resp.Error.Code,
			message:    resp.Error.Message,
		}

		if resp.Error.Code != responses.ResponseErrorCodeRateLimitExceeded {
			diagnostic := providerDiagnosticFromFailedResponse(resp, providerDiagnosticError, 0, 0)
			l.emitProviderDiagnostic(output, &diagnostic)

			return nil, err
		}

		attempt++
		wait := providerRateLimitRetryMinDelay

		if raw != nil {
			headers := raw.Header
			if value := headers.Get("Retry-After-Ms"); value != "" {
				parsed, errParse := strconv.ParseFloat(value, 64)
				if errParse == nil && parsed >= 0 && parsed == parsed {
					if parsed > float64(1<<63-1)/float64(time.Millisecond) {
						wait = time.Duration(1<<63 - 1)
					} else if delay := time.Duration(parsed * float64(time.Millisecond)); delay > wait {
						wait = delay
					}
				}
			}

			for _, header := range []string{"X-RateLimit-Reset-Requests", "X-RateLimit-Reset-Tokens"} {
				if delay, err := time.ParseDuration(headers.Get(header)); err == nil && delay > wait {
					wait = delay
				}
			}

			retryAfter := headers.Get("Retry-After")

			parsed, errParse := strconv.ParseFloat(retryAfter, 64)
			if errParse == nil && parsed >= 0 && parsed == parsed {
				if parsed > float64(1<<63-1)/float64(time.Second) {
					wait = time.Duration(1<<63 - 1)
				} else if delay := time.Duration(parsed * float64(time.Second)); delay > wait {
					wait = delay
				}
			} else if when, err := time.Parse(time.RFC1123, retryAfter); err == nil {
				if delay := time.Until(when); delay > wait {
					wait = delay
				}
			}
		}

		diagnostic := providerDiagnosticFromFailedResponse(resp, providerDiagnosticRetry, attempt, wait)
		l.emitProviderDiagnostic(output, &diagnostic)

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}

			return nil, fmt.Errorf("wait for provider retry: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func providerDiagnosticFromFailedResponse(resp *responses.Response, phase string, attempt int, retryAfter time.Duration) ProviderDiagnostic {
	diagnostic := ProviderDiagnostic{
		Phase:          phase,
		HTTPStatus:     0,
		ResponseStatus: string(resp.Status),
		Code:           string(resp.Error.Code),
		Message:        resp.Error.Message,
		Attempt:        attempt,
		RetryAfter:     "",
		ResponseID:     resp.ID,
	}

	if retryAfter > 0 {
		diagnostic.RetryAfter = retryAfter.String()
	}

	return diagnostic
}

func emitChatResponse(output chan<- ChatResponse, item ChatResponse) {
	if output == nil {
		return
	}

	select {
	case output <- item:
		return
	default:
		output <- item
		return
	}
}

func emitDiagnosticChatResponse(output chan<- ChatResponse, item ChatResponse) {
	select {
	case output <- item:
	default:
	}
}

func responseChatResponses(items []responses.ResponseOutputItemUnion) []ChatResponse {
	result := []ChatResponse{}

	for i := range items {
		item := &items[i]
		switch item.Type {
		case "reasoning":
			for j := range item.Summary {
				summary := item.Summary[j]
				if summary.Text == "" {
					continue
				}

				result = append(result, ChatResponse{Kind: ChatResponseReasoningSummary, Text: summary.Text})
			}
		case "message":
			kind := ChatResponseAssistantMessage
			if item.Phase == "commentary" {
				kind = ChatResponseAssistantCommentary
			}

			for j := range item.Content {
				content := item.Content[j]
				if content.Type != "output_text" || content.Text == "" {
					continue
				}

				result = append(result, ChatResponse{Kind: kind, Text: content.Text})
			}
		}
	}

	return result
}

func (l *looper) rewriteHistory(items []responses.ResponseInputItemUnionParam) []responses.ResponseInputItemUnionParam {
	if l.RewriteHistory == nil {
		return items
	}

	return l.RewriteHistory(items)
}

func (l *looper) buildParams(history []responses.ResponseInputItemUnionParam) responses.ResponseNewParams {
	var input responses.ResponseNewParamsInputUnion

	input.OfInputItemList = history

	var params responses.ResponseNewParams

	params.Input = input
	params.Model = l.Model
	params.Store = openai.Bool(false)
	params.ContextManagement = []responses.ResponseNewParamsContextManagement{{
		Type:             "compaction",
		CompactThreshold: openai.Int(l.compactThreshold()),
	}}
	params.Include = []responses.ResponseIncludable{reasoningEncryptedContent}

	params.ParallelToolCalls = openai.Bool(true)
	if l.SystemPrompt != "" {
		params.Instructions = openai.String(l.SystemPrompt)
	}

	if l.ReasoningEffort != "" {
		var reasoning shared.ReasoningParam

		reasoning.Effort = l.ReasoningEffort
		reasoning.Summary = shared.ReasoningSummaryAuto
		params.Reasoning = reasoning
	}

	if l.Verbosity != "" || l.ResponseFormat.GetType() != nil {
		params.Text = responses.ResponseTextConfigParam{Verbosity: responses.ResponseTextConfigVerbosity(l.Verbosity), Format: l.ResponseFormat}
	}

	if len(l.Tools) > 0 {
		params.Tools = make([]responses.ToolUnionParam, 0, len(l.Tools))
		for name := range l.Tools {
			tool := l.Tools[name]
			if tool.Hosted.GetType() != nil {
				params.Tools = append(params.Tools, tool.Hosted)

				continue
			}

			definition := tool.Definition
			if param.IsOmitted(definition.Strict) {
				definition.Strict = openai.Bool(true)
			}

			var toolParam responses.ToolUnionParam

			toolParam.OfFunction = &definition
			params.Tools = append(params.Tools, toolParam)
		}
	}

	return params
}

func (l *looper) compactThreshold() int64 {
	if l.CompactThreshold > 0 {
		return l.CompactThreshold
	}

	return defaultCompactThreshold
}

func pruneHistoryBeforeLatestCompaction(items []responses.ResponseInputItemUnionParam) []responses.ResponseInputItemUnionParam {
	latest := -1

	for i := range items {
		if items[i].OfCompaction != nil {
			latest = i
		}
	}

	if latest <= 0 {
		return items
	}

	return items[latest:]
}

func (l *looper) dispatchToolCalls(
	ctx context.Context,
	resp *responses.Response,
	doomLoop *doomLoopTrap,
	output chan<- ChatResponse,
) ([]dispatchedToolOutput, bool, error) {
	type pendingToolCall struct {
		name          string
		callID        string
		args          json.RawMessage
		tool          looperTool
		outputIndex   int
		subagentIndex int
		subagentTotal int
	}

	outputs := []dispatchedToolOutput{}
	calls := []pendingToolCall{}

	for i := range resp.Output {
		item := resp.Output[i]
		if item.Type != "function_call" {
			continue
		}

		tool, ok := l.Tools[item.Name]
		if !ok {
			result := toolCallFailureResult(item.Name, errors.New("tool not found"))
			l.emitToolDiagnostic(output, toolResultDiagnostic(item.Name, result.Output))
			outputs = append(outputs, dispatchedToolOutput{Param: toolCallOutput(item.CallID, result), Result: result, ReplayInput: nil})

			continue
		}

		args := json.RawMessage(item.Arguments.OfString)
		l.emitToolDiagnostic(output, toolCallDiagnostic(item.Name, args))

		if doomLoop != nil && doomLoop.trapped(item.Name, args) {
			result := fmt.Sprintf("tool call rejected: repeated identical %q call detected. Review the previous tool output and choose a different action instead of retrying the same input.", item.Name)
			l.emitToolDiagnostic(output, toolResultDiagnostic(item.Name, result))
			toolResult := TextToolResult(result)
			outputs = append(outputs, dispatchedToolOutput{Param: toolCallOutput(item.CallID, toolResult), Result: toolResult, ReplayInput: nil})

			continue
		}

		if decision, denied, err := l.permissionDecision(item.Name, &tool, args); err != nil {
			result := toolCallFailureResult(item.Name, fmt.Errorf("check permission: %w", err))
			l.emitToolDiagnostic(output, toolResultDiagnostic(item.Name, result.Output))
			outputs = append(outputs, dispatchedToolOutput{Param: toolCallOutput(item.CallID, result), Result: result, ReplayInput: nil})

			continue
		} else if denied {
			result := formatPermissionDenied(&decision)
			l.emitToolDiagnostic(output, toolResultDiagnostic(item.Name, result))
			toolResult := TextToolResult(result)
			outputs = append(outputs, dispatchedToolOutput{Param: toolCallOutput(item.CallID, toolResult), Result: toolResult, ReplayInput: nil})

			continue
		}

		calls = append(calls, pendingToolCall{name: item.Name, callID: item.CallID, args: args, tool: tool, outputIndex: len(outputs), subagentIndex: 0, subagentTotal: 0})

		var outputItem dispatchedToolOutput

		outputs = append(outputs, outputItem)
	}

	if len(outputs) == 0 {
		return nil, false, nil
	}

	taskTotal := 0

	for i := range calls {
		if calls[i].name == "task" {
			taskTotal++
		}
	}

	taskIndex := 0

	for i := range calls {
		if calls[i].name != "task" {
			continue
		}

		taskIndex++
		calls[i].subagentIndex = taskIndex
		calls[i].subagentTotal = taskTotal
	}

	group, groupCtx := errgroup.WithContext(ctx)
	if l.ParallelToolCalls > 0 {
		group.SetLimit(l.ParallelToolCalls)
	}

	for i := range calls {
		call := &calls[i]

		group.Go(func() error {
			var (
				result      ToolResult
				replayInput []responses.ResponseInputItemUnionParam
				err         error
			)

			metadata := toolCallMetadata{subagentIndex: call.subagentIndex, subagentTotal: call.subagentTotal}

			if call.tool.CallReplay != nil {
				result, replayInput, err = call.tool.CallReplay(groupCtx, call.args, output, metadata)
			} else {
				result, err = call.tool.Call(groupCtx, call.args, output, metadata)
			}

			if err != nil {
				if ctx.Err() != nil {
					return fmt.Errorf("run tool %q: %w", call.name, err)
				}

				result = toolCallFailureResult(call.name, err)
				replayInput = nil
			}

			l.emitToolDiagnostic(output, toolResultDiagnostic(call.name, attachmentOutputMessage(result)))
			outputs[call.outputIndex] = dispatchedToolOutput{Param: toolCallOutput(call.callID, result), Result: result, ReplayInput: replayInput}

			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return nil, true, fmt.Errorf("run tool calls: %w", err)
	}

	return outputs, true, nil
}

func toolCallFailureResult(name string, err error) ToolResult {
	return TextToolResult(fmt.Sprintf("tool call failed: %s: %v. Choose a different action.", name, err))
}

func (l *looper) emitToolDiagnostic(output chan<- ChatResponse, diagnostic *ToolDiagnostic) {
	if !l.Diagnostics {
		return
	}

	emitDiagnosticChatResponse(output, ChatResponse{Kind: ChatResponseAssistantTool, Tool: diagnostic})
}

func (l *looper) emitProviderDiagnostic(output chan<- ChatResponse, diagnostic *ProviderDiagnostic) {
	if !l.Diagnostics {
		return
	}

	emitDiagnosticChatResponse(output, ChatResponse{Kind: ChatResponseAssistantTool, Provider: diagnostic})
}

func (l *looper) emitHostedToolDiagnostics(output chan<- ChatResponse, items []responses.ResponseOutputItemUnion) {
	if !l.Diagnostics {
		return
	}

	for i := range items {
		item := items[i]
		if item.Type != "web_search_call" {
			continue
		}

		l.emitToolDiagnostic(output, toolHostedDiagnostic("websearch", item.Status, json.RawMessage(webSearchOutputActionJSON(&item.Action))))
	}
}

func toolCallDiagnostic(name string, args json.RawMessage) *ToolDiagnostic {
	diagnostic := &ToolDiagnostic{Phase: toolDiagnosticPhaseCall, Name: name, Arguments: args, Result: "", Status: "", Action: nil}

	return diagnostic
}

func toolResultDiagnostic(name, result string) *ToolDiagnostic {
	diagnostic := &ToolDiagnostic{Phase: toolDiagnosticPhaseResult, Name: name, Arguments: nil, Result: result, Status: "", Action: nil}

	return diagnostic
}

func toolHostedDiagnostic(name, status string, action json.RawMessage) *ToolDiagnostic {
	diagnostic := &ToolDiagnostic{Phase: toolDiagnosticPhaseCall, Name: name, Arguments: nil, Result: "", Status: status, Action: action}

	return diagnostic
}

func toolCallOutput(callID string, result ToolResult) responses.ResponseInputItemFunctionCallOutputParam {
	var output responses.ResponseInputItemFunctionCallOutputOutputUnionParam
	if len(result.Attachments) > 0 {
		output.OfResponseFunctionCallOutputItemArray = functionCallOutputContent(result)
	} else {
		output.OfString = openai.String(result.Output)
	}

	var outputParam responses.ResponseInputItemFunctionCallOutputParam

	outputParam.CallID = callID
	outputParam.Output = output
	outputParam.Type = "function_call_output"

	return outputParam
}

func (d *doomLoopTrap) trapped(name string, args json.RawMessage) bool {
	sig := toolCallSignature{name: name, args: canonicalToolArguments(args)}

	d.recent = append(d.recent, sig)
	if len(d.recent) > 3 {
		d.recent = d.recent[len(d.recent)-3:]
	}

	if len(d.recent) < 3 {
		return false
	}

	for _, recent := range d.recent {
		if recent != sig {
			return false
		}
	}

	return true
}

func (l *looper) permissionDecision(toolName string, tool *looperTool, args json.RawMessage) (permissionDecision, bool, error) {
	permission := tool.Permission
	if permission == "" {
		permission = toolName
	}

	subjects := []string{"*"}

	if tool.Subjects != nil {
		var err error

		subjects, err = tool.Subjects(args)
		if err != nil {
			return permissionDecision{}, false, err
		}
	}

	if len(subjects) == 0 {
		decision := permissionDecision{Action: permissionDeny, Bucket: "", Rule: PermissionRule{Pattern: "", Action: ""}, Matched: false, Permission: permission, Subject: ""}
		return decision, true, nil
	}

	for _, subject := range subjects {
		decision := l.Permissions.evaluate(permission, subject)
		if decision.Action == permissionDeny {
			return decision, true, nil
		}
	}

	var decision permissionDecision

	return decision, false, nil
}

func formatPermissionDenied(decision *permissionDecision) string {
	if decision.Matched {
		return fmt.Sprintf("tool call denied: permission %q rejected subject %q by rule %q => %s. Choose a different action.", decision.Permission, decision.Subject, decision.Rule.Pattern, decision.Rule.Action)
	}

	return fmt.Sprintf("tool call denied: permission %q has no matching allow rule for subject %q. Choose a different action.", decision.Permission, decision.Subject)
}

func loadSession(entries iter.Seq2[SessionEntry, error]) ([]responses.ResponseInputItemUnionParam, []SessionEntry, error) {
	turns := []SessionEntry{}
	history := []responses.ResponseInputItemUnionParam{}

	entryNumber := 0
	for turn, err := range entries {
		entryNumber++

		if err != nil {
			return nil, nil, fmt.Errorf("load session entry %d: %w", entryNumber, err)
		}

		items, err := ReplayInputToParams(turn.ReplayInput)
		if err != nil {
			if replayErr, ok := errors.AsType[*ReplayDecodeError](err); ok {
				replayErr.EntryIndex = entryNumber
			}

			return nil, nil, fmt.Errorf("decode session entry %d replay input: %w", entryNumber, err)
		}

		turns = append(turns, turn)
		history = append(history, items...)
	}

	return history, turns, nil
}

func responseOutputToReplayInput(item *responses.ResponseOutputItemUnion) (responses.ResponseInputItemUnionParam, bool) {
	switch item.Type {
	case "message":
		msg := item.AsMessage()

		parts := make([]string, 0, len(msg.Content))
		for i := range msg.Content {
			content := msg.Content[i]
			if content.Type == "output_text" {
				parts = append(parts, content.Text)
			}
		}

		assistant := responses.EasyInputMessageParam{
			Content: easyInputStringContent(strings.Join(parts, "")),
			Role:    responses.EasyInputMessageRole("assistant"),
			Type:    "message",
		}
		if msg.Phase != "" {
			assistant.Phase = responses.EasyInputMessagePhase(msg.Phase)
		}

		return responses.ResponseInputItemUnionParam{OfMessage: &assistant}, true
	case "reasoning":
		reasoning := item.AsReasoning()

		summary := ""
		if len(reasoning.Summary) > 0 {
			summary = reasoning.Summary[0].Text
		}

		return reasoningReplayInput(reasoning.ID, summary, reasoning.EncryptedContent), true
	case "compaction":
		return compactionReplayInput(item.ID, item.EncryptedContent), true
	case "function_call":
		return functionCallReplayInput(item.ID, item.CallID, item.Name, item.Arguments.OfString), true
	case "web_search_call":
		action, ok := webSearchOutputActionParam(&item.Action)
		if !ok {
			return emptyResponseInputItem(), false
		}

		return webSearchReplayInput(item.ID, item.Status, action), true
	default:
		return emptyResponseInputItem(), false
	}
}

func emptyResponseInputItem() responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{}
}

func functionCallOutputInputItem(output *responses.ResponseInputItemFunctionCallOutputParam) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{OfFunctionCallOutput: output}
}

func reasoningReplayInput(id, summary, encryptedContent string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{OfReasoning: &responses.ResponseReasoningItemParam{
		ID:               id,
		Summary:          []responses.ResponseReasoningItemSummaryParam{{Text: summary}},
		EncryptedContent: openai.String(encryptedContent),
		Type:             "reasoning",
	}}
}

func compactionReplayInput(id, encryptedContent string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{OfCompaction: &responses.ResponseCompactionItemParam{ID: openai.String(id), EncryptedContent: encryptedContent, Type: "compaction"}}
}

func functionCallReplayInput(id, callID, name, arguments string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{OfFunctionCall: &responses.ResponseFunctionToolCallParam{Arguments: arguments, CallID: callID, Name: name, ID: openai.String(id), Type: "function_call"}}
}

func webSearchReplayInput(id, status string, action responses.ResponseFunctionWebSearchActionUnionParam) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{OfWebSearchCall: &responses.ResponseFunctionWebSearchParam{ID: id, Action: action, Status: responses.ResponseFunctionWebSearchStatus(status)}}
}

func webSearchOutputActionParam(action *responses.ResponseOutputItemUnionAction) (responses.ResponseFunctionWebSearchActionUnionParam, bool) {
	switch action.Type {
	case "search":
		return responses.ResponseFunctionWebSearchActionUnionParam{OfSearch: &responses.ResponseFunctionWebSearchActionSearchParam{Query: action.Query, Queries: action.Queries}}, true
	case "open_page":
		return responses.ResponseFunctionWebSearchActionUnionParam{OfOpenPage: &responses.ResponseFunctionWebSearchActionOpenPageParam{URL: openai.String(action.URL)}}, true
	case "find_in_page":
		return responses.ResponseFunctionWebSearchActionUnionParam{OfFind: &responses.ResponseFunctionWebSearchActionFindParam{URL: action.URL, Pattern: action.Pattern}}, true
	default:
		return responses.ResponseFunctionWebSearchActionUnionParam{}, false
	}
}

func webSearchOutputActionJSON(action *responses.ResponseOutputItemUnionAction) string {
	value := map[string]any{"type": action.Type}
	switch action.Type {
	case "search":
		value["query"] = action.Query
		if len(action.Queries) > 0 {
			value["queries"] = action.Queries
		}
	case "open_page":
		value["url"] = action.URL
	case "find_in_page":
		value["url"] = action.URL
		value["pattern"] = action.Pattern
	}

	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}

	return string(data)
}
