package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	semconv "github.com/Arize-ai/openinference/go/openinference-semantic-conventions"
	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/sync/errgroup"
)

const reasoningEncryptedContent responses.ResponseIncludable = "reasoning.encrypted_content"
const defaultCompactThreshold int64 = 200000
const providerRateLimitMaxRetries = 5
const providerRetryBackoffMaxDelay = time.Minute

var errTurnInterrupted = errors.New("turn interrupted")

type responsesAPI interface {
	New(context.Context, *responses.ResponseNewParams, ...option.RequestOption) (*responses.Response, error)
	Compact(context.Context, *responses.ResponseCompactParams, ...option.RequestOption) (*responses.CompactedResponse, error)
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

func (c responseServiceClient) Compact(ctx context.Context, params *responses.ResponseCompactParams, opts ...option.RequestOption) (*responses.CompactedResponse, error) {
	resp, err := c.service.Compact(ctx, *params, opts...)
	if err != nil {
		return nil, fmt.Errorf("compact response: %w", err)
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
	agent                  Agent
	Client                 responsesAPI
	AnthropicClient        *anthropic.Client
	SystemPrompt           string
	Model                  shared.ResponsesModel
	DisplayModel           string
	ReasoningEffort        shared.ReasoningEffort
	Verbosity              string
	CompactThreshold       int64
	CompactionSteering     string
	ParallelToolCalls      int
	ResponseFormat         responses.ResponseFormatTextConfigUnionParam
	Permissions            PermissionSet
	Tools                  map[string]looperTool
	RewriteHistory         func([]responses.ResponseInputItemUnionParam) []responses.ResponseInputItemUnionParam
	Diagnostics            bool
	AutoApprovePermissions bool
	PermissionReviewer     permissionReviewer
	InPermissionReview     bool
	Observability          ObservabilityConfig
	expandInputPrompts     bool
	promptExpansion        promptExpansionEnvironment
}

type permissionReviewer interface {
	reviewPermission(context.Context, *permissionReviewRequest) permissionReviewDecision
}

type inertPermissionReviewer struct{}

func (inertPermissionReviewer) reviewPermission(context.Context, *permissionReviewRequest) permissionReviewDecision {
	return permissionReviewDecision{Approved: false, Reason: "automatic permission reviewer is unavailable"}
}

type toolPermissionDecision struct {
	denied  bool
	message string
	review  *permissionReviewRequest
}

type permissionReviewSubject struct {
	Subject     string `json:"subject"`
	RulePattern string `json:"rule_pattern"`
}

type permissionReviewRequest struct {
	ActiveAgent      string                    `json:"active_agent"`
	ToolName         string                    `json:"tool_name"`
	Permission       string                    `json:"permission"`
	RawArguments     string                    `json:"raw_arguments"`
	Subjects         []string                  `json:"subjects"`
	AutoSubjects     []permissionReviewSubject `json:"auto_subjects"`
	Reviewer         string                    `json:"reviewer"`
	ReviewerEmbedded bool                      `json:"reviewer_embedded"`
}

type permissionReviewDecision struct {
	Approved      bool   `json:"approved"`
	Risk          string `json:"risk"`
	Authorization string `json:"authorization"`
	Reason        string `json:"reason"`
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

type compactionBlock struct {
	end int
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
	Phase          string            `json:"phase"`
	HTTPStatus     int               `json:"http_status,omitempty"`
	ResponseStatus string            `json:"response_status,omitempty"`
	Code           string            `json:"code,omitempty"`
	Type           string            `json:"type,omitempty"`
	Message        string            `json:"message,omitempty"`
	Attempt        int               `json:"attempt,omitempty"`
	RetryAfter     string            `json:"retry_after,omitempty"`
	ResponseID     string            `json:"response_id,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
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

type providerRateLimitError struct {
	provider   string
	code       string
	typeName   string
	message    string
	retryAfter time.Duration
	responseID string
	requestID  string
	cause      error
}

func (e *providerRateLimitError) Error() string {
	if e.code != "" && e.message != "" {
		return fmt.Sprintf("provider rate limit: %s: %s", e.code, e.message)
	}

	if e.code != "" {
		return "provider rate limit: " + e.code
	}

	if e.message != "" {
		return "provider rate limit: " + e.message
	}

	return "provider rate limit"
}

func (e *providerRateLimitError) Unwrap() error {
	return e.cause
}

type providerRetryLimitError struct {
	provider   string
	httpStatus int
	requestID  string
	cause      error
}

type providerUsageLimitError struct {
	provider  string
	code      string
	typeName  string
	message   string
	requestID string
	cause     error
}

func (e *providerUsageLimitError) Error() string {
	if e.message != "" {
		return "provider usage limit: " + e.message
	}

	return "provider usage limit"
}

func (e *providerUsageLimitError) Unwrap() error {
	return e.cause
}

type providerUsageNotIncludedError struct {
	provider  string
	code      string
	typeName  string
	message   string
	requestID string
	cause     error
}

func (e *providerUsageNotIncludedError) Error() string {
	if e.message != "" {
		return "provider usage not included: " + e.message
	}

	return "provider usage not included"
}

func (e *providerUsageNotIncludedError) Unwrap() error {
	return e.cause
}

func (e *providerRetryLimitError) Error() string {
	if e.requestID != "" {
		return fmt.Sprintf("provider retry limit: status %d, request id: %s", e.httpStatus, e.requestID)
	}

	return fmt.Sprintf("provider retry limit: status %d", e.httpStatus)
}

func (e *providerRetryLimitError) Unwrap() error {
	return e.cause
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
) (record SessionEntry, rendered []ChatResponse, interrupted bool, err error) {
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

	record = SessionEntry{
		Version:     1,
		Type:        "turn",
		Timestamp:   time.Now().UTC(),
		Model:       l.DisplayModel,
		ReplayInput: replayInput,
	}

	turnCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	turnCtx, span := l.Observability.startSpan(turnCtx, "rocketcode.turn", semconv.SpanKindAgent,
		attribute.String(semconv.AgentName, l.agent.Name),
		l.Observability.inputValue(input.Text),
		attribute.String("rocketcode.model", l.DisplayModel),
		attribute.Int("rocketcode.tool_count", len(l.Tools)),
		attribute.Int64("rocketcode.compact_threshold", l.compactThreshold()),
		attribute.Int("rocketcode.parallel_tool_calls", l.ParallelToolCalls),
	)
	defer func() {
		recordSpanError(span, err)
		span.span.SetAttributes(attribute.String("rocketcode.response_id", record.ResponseID))

		if outputText := assistantOutputText(rendered); outputText != "" {
			span.span.SetAttributes(l.Observability.outputValue(outputText))
		}

		span.span.End()
	}()

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

	rendered = []ChatResponse{}
	doomLoop := doomLoopTrap{recent: nil}

	for {
		history := append(append([]responses.ResponseInputItemUnionParam{}, baseHistory...), turnItems...)
		history = l.rewriteHistory(history)
		history = pruneHistoryBeforeLatestCompaction(history)

		params := l.buildParams(history)

		resp, recoveredHistory, err := l.newProviderResponse(turnCtx, &params, output)
		if err != nil {
			if errors.Is(context.Cause(turnCtx), errTurnInterrupted) {
				return emptyRecord, nil, true, nil
			}

			return emptyRecord, nil, false, fmt.Errorf("request response: %w", err)
		}

		if len(recoveredHistory) > 0 {
			recoveredHistory = pruneHistoryBeforeLatestCompaction(recoveredHistory)

			replayInput, err := ReplayInputFromParams(recoveredHistory)
			if err != nil {
				return emptyRecord, nil, false, err
			}

			record.ReplayInput = replayInput

			turnItems = append([]responses.ResponseInputItemUnionParam(nil), recoveredHistory...)
		}

		record.ResponseID = resp.ID
		l.emitHostedToolDiagnostics(output, resp.Output)
		rendered = append(rendered, responseChatResponses(resp.Output)...)

		hadCompaction := slices.ContainsFunc(resp.Output, func(item responses.ResponseOutputItemUnion) bool { return item.Type == "compaction" })

		for i := range resp.Output {
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
			if len(resp.Output) > 0 && !slices.ContainsFunc(resp.Output, func(item responses.ResponseOutputItemUnion) bool { return item.Type != "compaction" }) {
				continue
			}

			return record, rendered, false, nil
		}

		for i := range toolOutputs {
			toolInput := responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &toolOutputs[i].Param}

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

func (l *looper) newProviderResponse(ctx context.Context, params *responses.ResponseNewParams, output chan<- ChatResponse) (resp *responses.Response, recoveredHistory []responses.ResponseInputItemUnionParam, err error) {
	provider := modelProviderOpenAI
	if strings.HasPrefix(l.DisplayModel, modelProviderAnthropic+"/") {
		provider = modelProviderAnthropic
	}

	ctx, span := l.Observability.startSpan(ctx, "rocketcode.provider", semconv.SpanKindLLM,
		attribute.String(semconv.LLMModelName, l.DisplayModel),
		attribute.String(semconv.LLMProvider, provider),
	)
	defer func() {
		recordSpanError(span, err)

		if resp != nil {
			span.span.SetAttributes(attribute.String("rocketcode.response_id", resp.ID))
		}

		span.span.End()
	}()

	if strings.HasPrefix(l.DisplayModel, modelProviderAnthropic+"/") {
		resp, err := l.newAnthropicResponse(ctx, params, output)
		return resp, nil, err
	}

	if l.Client == nil {
		return nil, nil, errors.New("openai provider is required")
	}

	return l.newResponseWithProviderRetry(ctx, params, output)
}

func (l *looper) newResponseWithProviderRetry(ctx context.Context, params *responses.ResponseNewParams, output chan<- ChatResponse) (*responses.Response, []responses.ResponseInputItemUnionParam, error) {
	attempt := 0

	for {
		var raw *http.Response

		resp, err := l.Client.New(ctx, params, option.WithResponseInto(&raw))
		if err != nil {
			if ctx.Err() == nil && isContextLengthExceeded(err) {
				resp, recoveredHistory, err := l.newResponseAfterContextCompaction(ctx, params, err, output)
				if err != nil {
					return nil, nil, fmt.Errorf("new response: %w", err)
				}

				return resp, recoveredHistory, nil
			}

			errReturn := err
			if ctx.Err() == nil {
				if errAPI, ok := errors.AsType[*openai.Error](err); ok {
					if errAPI.StatusCode == http.StatusTooManyRequests && (errAPI.Code == "too_many_requests" || errAPI.Type == "too_many_requests") {
						attempt++
						wait := providerRetryDelay(errAPI.Response, attempt)
						errRateLimit := &providerRateLimitError{provider: modelProviderOpenAI, code: errAPI.Code, typeName: errAPI.Type, message: errAPI.Message, retryAfter: wait, requestID: providerRequestID(errAPI.Response), cause: err}

						if attempt > providerRateLimitMaxRetries {
							diagnostic := ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: http.StatusTooManyRequests, Code: errRateLimit.code, Type: errRateLimit.typeName, Message: errRateLimit.message, RetryAfter: errRateLimit.retryAfter.String(), Headers: providerDiagnosticHeaders(errAPI.Response)}
							l.emitProviderDiagnostic(ctx, output, &diagnostic)

							return nil, nil, fmt.Errorf("new response: %w", errRateLimit)
						}

						diagnostic := ProviderDiagnostic{Phase: providerDiagnosticRetry, HTTPStatus: http.StatusTooManyRequests, Code: errRateLimit.code, Type: errRateLimit.typeName, Message: errRateLimit.message, Attempt: attempt, RetryAfter: errRateLimit.retryAfter.String(), Headers: providerDiagnosticHeaders(errAPI.Response)}
						l.emitProviderDiagnostic(ctx, output, &diagnostic)

						if err := waitProviderRetry(ctx, wait); err != nil {
							return nil, nil, fmt.Errorf("wait for provider retry: %w", err)
						}

						continue
					}

					if errAPI.StatusCode == http.StatusTooManyRequests {
						switch {
						case errAPI.Code == "usage_limit_reached" || errAPI.Type == "usage_limit_reached":
							errReturn = &providerUsageLimitError{provider: modelProviderOpenAI, code: errAPI.Code, typeName: errAPI.Type, message: errAPI.Message, requestID: providerRequestID(errAPI.Response), cause: err}
						case errAPI.Code == "usage_not_included" || errAPI.Type == "usage_not_included":
							errReturn = &providerUsageNotIncludedError{provider: modelProviderOpenAI, code: errAPI.Code, typeName: errAPI.Type, message: errAPI.Message, requestID: providerRequestID(errAPI.Response), cause: err}
						default:
							errReturn = &providerRetryLimitError{provider: modelProviderOpenAI, httpStatus: errAPI.StatusCode, requestID: providerRequestID(errAPI.Response), cause: err}
						}
					}

					diagnostic := ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: errAPI.StatusCode, Code: errAPI.Code, Type: errAPI.Type, Message: errAPI.Message, Headers: providerDiagnosticHeaders(errAPI.Response)}
					l.emitProviderDiagnostic(ctx, output, &diagnostic)
				} else {
					diagnostic := ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: 0, ResponseStatus: "", Code: "", Message: err.Error(), Attempt: 0, RetryAfter: "", ResponseID: ""}
					l.emitProviderDiagnostic(ctx, output, &diagnostic)
				}
			}

			return nil, nil, fmt.Errorf("new response: %w", errReturn)
		}

		if resp == nil {
			err := errors.New("missing response")
			l.emitProviderDiagnostic(ctx, output, &ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: 0, ResponseStatus: "", Code: "", Message: err.Error(), Attempt: 0, RetryAfter: "", ResponseID: ""})

			return nil, nil, err
		}

		if resp.Status != responses.ResponseStatusFailed {
			return resp, nil, nil
		}

		err = &responseFailureError{
			responseID: resp.ID,
			status:     resp.Status,
			code:       resp.Error.Code,
			message:    resp.Error.Message,
		}

		if isResponseContextLengthExceeded(resp) {
			resp, recoveredHistory, err := l.newResponseAfterContextCompaction(ctx, params, err, output)
			if err != nil {
				return nil, nil, err
			}

			return resp, recoveredHistory, nil
		}

		if resp.Error.Code != responses.ResponseErrorCodeRateLimitExceeded {
			diagnostic := providerDiagnosticFromFailedResponse(resp, providerDiagnosticError, 0, 0, nil)
			l.emitProviderDiagnostic(ctx, output, &diagnostic)

			return nil, nil, err
		}

		attempt++
		wait := providerRetryDelay(raw, attempt)
		err = &providerRateLimitError{provider: modelProviderOpenAI, code: string(resp.Error.Code), message: resp.Error.Message, retryAfter: wait, responseID: resp.ID, requestID: providerRequestID(raw), cause: err}

		if attempt > providerRateLimitMaxRetries {
			diagnostic := providerDiagnosticFromFailedResponse(resp, providerDiagnosticError, 0, 0, raw)
			l.emitProviderDiagnostic(ctx, output, &diagnostic)

			return nil, nil, err
		}

		diagnostic := providerDiagnosticFromFailedResponse(resp, providerDiagnosticRetry, attempt, wait, raw)
		l.emitProviderDiagnostic(ctx, output, &diagnostic)

		if err := waitProviderRetry(ctx, wait); err != nil {
			return nil, nil, fmt.Errorf("wait for provider retry: %w", err)
		}
	}
}

func (l *looper) newResponseAfterContextCompaction(ctx context.Context, params *responses.ResponseNewParams, errOriginal error, output chan<- ChatResponse) (*responses.Response, []responses.ResponseInputItemUnionParam, error) {
	original := params.Input.OfInputItemList

	blocks := compactionBlocks(original)
	if len(blocks) < 2 {
		return nil, nil, errOriginal
	}

	eligible := len(blocks) - 1
	chunk := (eligible + 9) / 10
	errLast := errOriginal

	for compactedBlocks := chunk; compactedBlocks <= eligible; compactedBlocks += chunk {
		end := blocks[compactedBlocks-1].end
		compactParams := responses.ResponseCompactParams{
			Model:        responses.ResponseCompactParamsModel(params.Model),
			Instructions: params.Instructions,
			Input:        responses.ResponseCompactParamsInputUnion{OfResponseInputItemArray: original[:end]},
		}

		compacted, err := l.Client.Compact(ctx, &compactParams)
		if err != nil {
			return nil, nil, fmt.Errorf("compact context after context_length_exceeded: %w", err)
		}

		compactedInput, err := compactedOutputToReplayParams(compacted.Output)
		if err != nil {
			return nil, nil, fmt.Errorf("convert compacted response: %w", err)
		}

		recoveredHistory := append(append([]responses.ResponseInputItemUnionParam{}, compactedInput...), original[end:]...)
		retryParams := *params
		retryParams.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: recoveredHistory}

		var raw *http.Response

		resp, err := l.Client.New(ctx, &retryParams, option.WithResponseInto(&raw))
		if err != nil {
			errLast = err
			if ctx.Err() == nil && isContextLengthExceeded(err) {
				continue
			}

			if ctx.Err() == nil {
				diagnostic := ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: 0, ResponseStatus: "", Code: "", Message: err.Error(), Attempt: 0, RetryAfter: "", ResponseID: ""}
				if errAPI, ok := errors.AsType[*openai.Error](err); ok {
					diagnostic.Code = errAPI.Code
					diagnostic.Message = errAPI.Message
					diagnostic.HTTPStatus = errAPI.StatusCode
				}

				l.emitProviderDiagnostic(ctx, output, &diagnostic)
			}

			return nil, nil, fmt.Errorf("retry compacted response: %w", err)
		}

		if resp == nil {
			err := errors.New("missing response")
			l.emitProviderDiagnostic(ctx, output, &ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: 0, ResponseStatus: "", Code: "", Message: err.Error(), Attempt: 0, RetryAfter: "", ResponseID: ""})

			return nil, nil, err
		}

		if resp.Status != responses.ResponseStatusFailed {
			return resp, recoveredHistory, nil
		}

		errLast = &responseFailureError{responseID: resp.ID, status: resp.Status, code: resp.Error.Code, message: resp.Error.Message}
		if isResponseContextLengthExceeded(resp) {
			continue
		}

		diagnostic := providerDiagnosticFromFailedResponse(resp, providerDiagnosticError, 0, 0, nil)
		l.emitProviderDiagnostic(ctx, output, &diagnostic)

		return nil, nil, errLast
	}

	return nil, nil, errLast
}

func compactionBlocks(items []responses.ResponseInputItemUnionParam) []compactionBlock {
	blocks := make([]compactionBlock, 0, len(items))
	for i := 0; i < len(items); {
		if items[i].OfFunctionCall == nil {
			blocks = append(blocks, compactionBlock{end: i + 1})
			i++

			continue
		}

		pending := map[string]bool{}

		end := len(items)
		for j := i; j < len(items); j++ {
			if call := items[j].OfFunctionCall; call != nil {
				pending[call.CallID] = true
			}

			if output := items[j].OfFunctionCallOutput; output != nil {
				delete(pending, output.CallID)
			}

			if j > i && len(pending) == 0 {
				end = j + 1

				break
			}
		}

		blocks = append(blocks, compactionBlock{end: end})
		i = end
	}

	return blocks
}

func compactedOutputToReplayParams(items []responses.ResponseOutputItemUnion) ([]responses.ResponseInputItemUnionParam, error) {
	input := make([]responses.ResponseInputItemUnionParam, 0, len(items))
	for i := range items {
		switch items[i].Type {
		case "message":
			parts := make([]string, 0, len(items[i].Content))
			for j := range items[i].Content {
				if items[i].Content[j].Type == "output_text" {
					parts = append(parts, items[i].Content[j].Text)
				}
			}

			role := strings.TrimSpace(string(items[i].Role))
			if role == "" {
				role = "user"
			}

			message := responses.EasyInputMessageParam{Role: responses.EasyInputMessageRole(role), Content: easyInputStringContent(strings.Join(parts, "")), Type: "message"}
			if items[i].Phase != "" {
				message.Phase = responses.EasyInputMessagePhase(items[i].Phase)
			}

			input = append(input, responses.ResponseInputItemUnionParam{OfMessage: &message})
		case "compaction", "compaction_summary":
			input = append(input, compactionReplayInput(items[i].ID, items[i].EncryptedContent, ""))
		case "reasoning":
			summary := ""
			if len(items[i].Summary) > 0 {
				summary = items[i].Summary[0].Text
			}

			input = append(input, reasoningReplayInput(items[i].ID, summary, items[i].EncryptedContent))
		default:
			return nil, fmt.Errorf("unsupported compacted output item kind %q", items[i].Type)
		}
	}

	return input, nil
}

func isContextLengthExceeded(err error) bool {
	if errAPI, ok := errors.AsType[*openai.Error](err); ok {
		return errAPI.Code == "context_length_exceeded"
	}

	if errResponse, ok := errors.AsType[*responseFailureError](err); ok {
		return errResponse.code == responses.ResponseErrorCode("context_length_exceeded")
	}

	return false
}

func isResponseContextLengthExceeded(resp *responses.Response) bool {
	return resp.Error.Code == responses.ResponseErrorCode("context_length_exceeded")
}

func providerDiagnosticFromFailedResponse(resp *responses.Response, phase string, attempt int, retryAfter time.Duration, raw *http.Response) ProviderDiagnostic {
	diagnostic := ProviderDiagnostic{
		Phase:          phase,
		ResponseStatus: string(resp.Status),
		Code:           string(resp.Error.Code),
		Message:        resp.Error.Message,
		Attempt:        attempt,
		ResponseID:     resp.ID,
		Headers:        providerDiagnosticHeaders(raw),
	}

	if retryAfter > 0 {
		diagnostic.RetryAfter = retryAfter.String()
	}

	return diagnostic
}

func providerRequestID(resp *http.Response) string {
	if resp == nil {
		return ""
	}

	if requestID := resp.Header.Get("X-Request-ID"); requestID != "" {
		return requestID
	}

	if requestID := resp.Header.Get("X-Oai-Request-Id"); requestID != "" {
		return requestID
	}

	return resp.Header.Get("Cf-Ray")
}

func providerDiagnosticHeaders(resp *http.Response) map[string]string {
	if resp == nil {
		return nil
	}

	headers := map[string]string{}

	for name, values := range resp.Header {
		if len(values) == 0 {
			continue
		}

		nameLower := strings.ToLower(name)
		switch {
		case nameLower == "retry-after-ms",
			nameLower == "retry-after",
			nameLower == "x-ratelimit-reset-requests",
			nameLower == "x-ratelimit-reset-tokens",
			nameLower == "x-request-id",
			nameLower == "x-oai-request-id",
			nameLower == "cf-ray",
			nameLower == "x-openai-authorization-error",
			nameLower == "x-error-json",
			strings.HasPrefix(nameLower, "x-codex-"):
			headers[nameLower] = values[0]
		}
	}

	if len(headers) == 0 {
		return nil
	}

	return headers
}

func emitChatResponse(output chan<- ChatResponse, item ChatResponse) {
	if output == nil {
		return
	}

	select {
	case output <- item:
	default:
		output <- item
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
				if strings.HasPrefix(l.DisplayModel, modelProviderAnthropic+"/") {
					continue
				}

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

		args := json.RawMessage(item.Arguments.OfString)

		tool, ok := l.Tools[item.Name]
		if !ok {
			_, span := l.Observability.startToolSpan(ctx, item.Name, item.CallID, "", args, toolCallMetadata{})
			result := toolCallFailureResult(item.Name, errors.New("tool not found"))
			recordSpanError(span, errors.New("tool not found"))
			span.span.SetAttributes(l.Observability.outputValue(result.Output), attribute.Bool("rocketcode.tool_denied", false), attribute.Bool("rocketcode.tool_failure", true))
			span.span.End()
			l.emitToolDiagnostic(output, &ToolDiagnostic{Phase: toolDiagnosticPhaseResult, Name: item.Name, Result: result.Output})
			outputs = append(outputs, dispatchedToolOutput{Param: toolCallOutput(item.CallID, result), Result: result, ReplayInput: nil})

			continue
		}

		l.emitToolDiagnostic(output, &ToolDiagnostic{Phase: toolDiagnosticPhaseCall, Name: item.Name, Arguments: args})

		if doomLoop != nil && doomLoop.trapped(item.Name, args) {
			_, span := l.Observability.startToolSpan(ctx, item.Name, item.CallID, tool.Permission, args, toolCallMetadata{})
			result := fmt.Sprintf("tool call rejected: repeated identical %q call detected. Review the previous tool output and choose a different action instead of retrying the same input.", item.Name)

			recordSpanError(span, errors.New("repeated identical tool call"))
			span.span.SetAttributes(l.Observability.outputValue(result), attribute.Bool("rocketcode.tool_denied", true), attribute.Bool("rocketcode.tool_failure", true))
			span.span.End()
			l.emitToolDiagnostic(output, &ToolDiagnostic{Phase: toolDiagnosticPhaseResult, Name: item.Name, Result: result})
			toolResult := TextToolResult(result)
			outputs = append(outputs, dispatchedToolOutput{Param: toolCallOutput(item.CallID, toolResult), Result: toolResult, ReplayInput: nil})

			continue
		}

		decision, err := l.permissionDecision(item.Name, &tool, args)
		if err != nil {
			_, span := l.Observability.startToolSpan(ctx, item.Name, item.CallID, tool.Permission, args, toolCallMetadata{})
			result := toolCallFailureResult(item.Name, fmt.Errorf("check permission: %w", err))
			recordSpanError(span, err)
			span.span.SetAttributes(l.Observability.outputValue(result.Output), attribute.Bool("rocketcode.tool_denied", true), attribute.Bool("rocketcode.tool_failure", true))
			span.span.End()
			l.emitToolDiagnostic(output, &ToolDiagnostic{Phase: toolDiagnosticPhaseResult, Name: item.Name, Result: result.Output})
			outputs = append(outputs, dispatchedToolOutput{Param: toolCallOutput(item.CallID, result), Result: result, ReplayInput: nil})

			continue
		}

		if decision.denied {
			_, span := l.Observability.startToolSpan(ctx, item.Name, item.CallID, tool.Permission, args, toolCallMetadata{})
			result := decision.message

			recordSpanError(span, errors.New("tool permission denied"))
			span.span.SetAttributes(l.Observability.outputValue(result), attribute.Bool("rocketcode.tool_denied", true), attribute.Bool("rocketcode.tool_failure", false))
			span.span.End()
			l.emitToolDiagnostic(output, &ToolDiagnostic{Phase: toolDiagnosticPhaseResult, Name: item.Name, Result: result})
			toolResult := TextToolResult(result)
			outputs = append(outputs, dispatchedToolOutput{Param: toolCallOutput(item.CallID, toolResult), Result: toolResult, ReplayInput: nil})

			continue
		}

		if decision.review != nil {
			reviewDecision := l.PermissionReviewer.reviewPermission(ctx, decision.review)
			if !reviewDecision.Approved {
				_, span := l.Observability.startToolSpan(ctx, item.Name, item.CallID, tool.Permission, args, toolCallMetadata{})
				result := formatPermissionReviewDenied(reviewDecision)

				recordSpanError(span, errors.New("automatic permission review denied tool call"))
				span.span.SetAttributes(l.Observability.outputValue(result), attribute.Bool("rocketcode.tool_denied", true), attribute.Bool("rocketcode.tool_failure", false))
				span.span.End()
				l.emitToolDiagnostic(output, &ToolDiagnostic{Phase: toolDiagnosticPhaseResult, Name: item.Name, Result: result})
				toolResult := TextToolResult(result)
				outputs = append(outputs, dispatchedToolOutput{Param: toolCallOutput(item.CallID, toolResult), Result: toolResult, ReplayInput: nil})

				continue
			}
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
			callCtx, span := l.Observability.startToolSpan(groupCtx, call.name, call.callID, call.tool.Permission, call.args, metadata)

			if call.tool.CallReplay != nil {
				result, replayInput, err = call.tool.CallReplay(callCtx, call.args, output, metadata)
			} else {
				result, err = call.tool.Call(callCtx, call.args, output, metadata)
			}

			if err != nil {
				recordSpanError(span, err)

				if ctx.Err() != nil {
					span.span.End()

					return fmt.Errorf("run tool %q: %w", call.name, err)
				}

				result = toolCallFailureResult(call.name, err)
				replayInput = nil
			}

			span.span.SetAttributes(l.Observability.outputValue(attachmentOutputMessage(result)), attribute.Bool("rocketcode.tool_denied", false), attribute.Bool("rocketcode.tool_failure", err != nil))
			span.span.End()

			l.emitToolDiagnostic(output, &ToolDiagnostic{Phase: toolDiagnosticPhaseResult, Name: call.name, Result: attachmentOutputMessage(result)})
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

func (l *looper) emitProviderDiagnostic(ctx context.Context, output chan<- ChatResponse, diagnostic *ProviderDiagnostic) {
	recordProviderDiagnosticEvent(ctx, diagnostic)

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

		l.emitToolDiagnostic(output, &ToolDiagnostic{Phase: toolDiagnosticPhaseCall, Name: "websearch", Status: item.Status, Action: json.RawMessage(webSearchOutputActionJSON(&item.Action))})
	}
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

func (l *looper) permissionDecision(toolName string, tool *looperTool, args json.RawMessage) (toolPermissionDecision, error) {
	permission := tool.Permission
	if permission == "" {
		permission = toolName
	}

	subjects := []string{"*"}

	if tool.Subjects != nil {
		var err error

		subjects, err = tool.Subjects(args)
		if err != nil {
			return toolPermissionDecision{}, err
		}
	}

	if len(subjects) == 0 {
		decision := permissionDecision{Action: permissionDeny, Bucket: "", Rule: PermissionRule{Pattern: "", Action: ""}, Matched: false, Permission: permission, Subject: ""}
		return toolPermissionDecision{denied: true, message: formatPermissionDenied(&decision)}, nil
	}

	autoSubjects := []permissionReviewSubject{}
	reviewer := ""
	reviewerEmbedded := true
	reviewerSet := false

	for _, subject := range subjects {
		decision := l.Permissions.evaluate(permission, subject)
		if decision.Action == permissionDeny {
			return toolPermissionDecision{denied: true, message: formatPermissionDenied(&decision)}, nil
		}

		if decision.Action != permissionAuto {
			continue
		}

		if l.InPermissionReview {
			return toolPermissionDecision{denied: true, message: "tool call denied: automatic permission review cannot recursively require automatic approval. Choose a different action."}, nil
		}

		if !l.AutoApprovePermissions {
			return toolPermissionDecision{denied: true, message: fmt.Sprintf("tool call denied: permission %q subject %q requires automatic approval, but automatic permission approval is disabled. Choose a different action.", permission, subject)}, nil
		}

		if !reviewerSet {
			reviewer = decision.Rule.Reviewer
			reviewerEmbedded = reviewer == ""
			reviewerSet = true
		} else if reviewer != decision.Rule.Reviewer {
			return toolPermissionDecision{denied: true, message: fmt.Sprintf("tool call denied: permission %q matched multiple automatic reviewers. Choose a different action.", permission)}, nil
		}

		autoSubjects = append(autoSubjects, permissionReviewSubject{Subject: subject, RulePattern: decision.Rule.Pattern})
	}

	if len(autoSubjects) == 0 {
		return toolPermissionDecision{}, nil
	}

	activeAgent := l.agent.Name

	return toolPermissionDecision{review: &permissionReviewRequest{
		ActiveAgent:      activeAgent,
		ToolName:         toolName,
		Permission:       permission,
		RawArguments:     canonicalToolArguments(args),
		Subjects:         subjects,
		AutoSubjects:     autoSubjects,
		Reviewer:         reviewer,
		ReviewerEmbedded: reviewerEmbedded,
	}}, nil
}

func formatPermissionReviewDenied(decision permissionReviewDecision) string {
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		reason = "automatic permission review denied or failed"
	}

	return "tool call denied: automatic permission review rejected the action: " + reason + ". Choose a different action."
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
		summary := ""
		if len(item.Summary) > 0 {
			summary = item.Summary[0].Text
		}

		return compactionReplayInput(item.ID, item.EncryptedContent, summary), true
	case "function_call":
		return functionCallReplayInput(item.ID, item.CallID, item.Name, item.Arguments.OfString), true
	case "web_search_call":
		action, ok := webSearchOutputActionParam(&item.Action)
		if !ok {
			return responses.ResponseInputItemUnionParam{}, false
		}

		return webSearchReplayInput(item.ID, item.Status, action), true
	default:
		return responses.ResponseInputItemUnionParam{}, false
	}
}

func reasoningReplayInput(id, summary, encryptedContent string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{OfReasoning: &responses.ResponseReasoningItemParam{
		ID:               id,
		Summary:          []responses.ResponseReasoningItemSummaryParam{{Text: summary}},
		EncryptedContent: openai.String(encryptedContent),
		Type:             "reasoning",
	}}
}

func compactionReplayInput(id, encryptedContent, content string) responses.ResponseInputItemUnionParam {
	compaction := responses.ResponseCompactionItemParam{ID: openai.String(id), EncryptedContent: encryptedContent, Type: "compaction"}
	if content != "" {
		compaction.SetExtraFields(map[string]any{"content": content})
	}

	return responses.ResponseInputItemUnionParam{OfCompaction: &compaction}
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
