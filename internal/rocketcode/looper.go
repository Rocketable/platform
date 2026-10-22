package rocketcode

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	semconv "github.com/Arize-ai/openinference/go/openinference-semantic-conventions"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	openairespjson "github.com/openai/openai-go/v3/packages/respjson"
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

// ErrShutdown is the cancellation cause that stops a turn after its running calls finish and
// are journaled. Resumable calls, including task and execute, stop through their own steps
// instead, and resume when the turn runs again.
var ErrShutdown = errors.New("shutdown")

// callContext returns the context one call runs in. A resumable call stops with ctx; any other
// call ignores ErrShutdown, so it finishes and is journaled, but stops on any other cancellation.
func callContext(ctx context.Context, resumable bool) (context.Context, context.CancelFunc) {
	if resumable {
		return ctx, func() {}
	}

	call, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	cancelUnlessShutdown := func() {
		if cause := context.Cause(ctx); !errors.Is(cause, ErrShutdown) {
			cancel(cause)
		}
	}

	stop := context.AfterFunc(ctx, cancelUnlessShutdown)
	if ctx.Err() != nil {
		cancelUnlessShutdown()
	}

	return call, func() {
		stop()
		cancel(nil)
	}
}

// TurnPhase is whether the live turn is still in the tool loop.
type TurnPhase int

const (
	// TurnPhaseUnclassified means no provider response has classified the turn yet.
	TurnPhaseUnclassified TurnPhase = iota
	// TurnPhaseToolLoop means the last provider response requested tools.
	TurnPhaseToolLoop
	// TurnPhaseFinalAnswer means the last provider response had no tools.
	TurnPhaseFinalAnswer
)

// SteerDrain collects waiting steers and Completion Notes after a tool batch, or when a no-tool answer would otherwise end the turn.
// Each drain follows one provider response, named by responseID; a turn resumed
// after a restart drains its journaled response again under the same ID.
// Applied identities include replayed and compacted inputs. A final drain may
// wait for new input; errors must prevent accepting the pending final answer.
type SteerDrain interface {
	Drain(ctx context.Context, responseID string, phase TurnPhase, applied []string) ([]PromptInput, error)
}

// InertSteerDrain never supplies additional input.
type InertSteerDrain struct{}

// Drain does not wait or supply input.
func (InertSteerDrain) Drain(context.Context, string, TurnPhase, []string) ([]PromptInput, error) {
	return nil, nil
}

type responsesAPI interface {
	New(context.Context, *responses.ResponseNewParams, responseObserver, ...option.RequestOption) (*responses.Response, error)
	Compact(context.Context, *responses.ResponseCompactParams, ...option.RequestOption) (*responses.CompactedResponse, error)
}

type responseServiceClient struct {
	service *responses.ResponseService
	doer    *responsesWebsocketDoer
}

func (c responseServiceClient) New(ctx context.Context, params *responses.ResponseNewParams, observer responseObserver, opts ...option.RequestOption) (*responses.Response, error) {
	var errLocal error

	middleware := func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		resp, err := c.doer.middleware(req, next, observer)
		if _, ok := errors.AsType[progressPersistenceError](err); ok {
			errLocal = err
			// SDK v3.69.0 internal/requestconfig/requestconfig.go: shouldRetry
			// honors this header before returning the original transport error.
			resp = &http.Response{Header: http.Header{"X-Should-Retry": []string{"false"}}, Body: http.NoBody, Request: req}
		}

		return resp, err
	}

	resp, err := c.service.New(ctx, *params, append(slices.Clone(opts), option.WithMiddleware(middleware))...)
	if errLocal != nil {
		err = errLocal // SDK context cancellation must not erase a fatal local write error.
	}

	if err != nil {
		return nil, fmt.Errorf("create response: %w", err)
	}

	return resp, nil
}

func (c responseServiceClient) Compact(ctx context.Context, params *responses.ResponseCompactParams, opts ...option.RequestOption) (*responses.CompactedResponse, error) {
	middleware := func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		return c.doer.middleware(req, next, inertResponseObserver{})
	}

	resp, err := c.service.Compact(ctx, *params, append(slices.Clone(opts), option.WithMiddleware(middleware))...)
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
	// codeModeOnly tools are available only inside execute (not model-facing).
	codeModeOnly bool
	// resumable calls pause through their own steps at shutdown and run again on resume; other
	// calls finish at shutdown, and are reported interrupted only when cut off by a crash.
	resumable bool
}

type toolCallMetadata struct {
	callID        string
	subagentIndex int
	subagentTotal int
	observations  *turnObservations
	progress      *PublicProgress
}

// looper runs conversational turns against the configured model and tools.
type looper struct {
	agent              Agent
	ProviderOrigin     ProviderOrigin
	Client             responsesAPI
	SystemPrompt       string
	Model              shared.ResponsesModel
	DisplayModel       string
	ReasoningEffort    shared.ReasoningEffort
	Verbosity          string
	CompactThreshold   int64
	CompactionSteering string
	ParallelToolCalls  int
	ResponseFormat     responses.ResponseFormatTextConfigUnionParam
	Permissions        PermissionSet
	Tools              map[string]looperTool
	// CodeModeHosts are tools available only inside execute (not model-facing):
	// sandbox host tools plus embedder custom tools (e.g. rocketclaw_*).
	CodeModeHosts          map[string]looperTool
	permissionReviewInput  []responses.ResponseInputItemUnionParam
	Diagnostics            bool
	AutoApprovePermissions bool
	PermissionReviewer     permissionReviewer
	InPermissionReview     bool
	Observability          ObservabilityConfig
	Journal                Journal
	observations           *turnObservations // Root-owned lifetime; workers receive the turn-local pointer.
	SteerDrain             SteerDrain
	expandInputPrompts     bool
	promptExpansion        promptExpansionEnvironment
	spillRel               string

	spillMu      sync.Mutex
	spillTurnID  string
	spillResults map[string]string

	phaseMu   sync.Mutex
	turnPhase TurnPhase
}

func (l *looper) Phase() TurnPhase {
	l.phaseMu.Lock()
	defer l.phaseMu.Unlock()

	return l.turnPhase
}

type permissionReviewer interface {
	reviewPermission(context.Context, *permissionReviewRequest, chan<- ChatResponse) permissionReviewDecision
}

type inertPermissionReviewer struct{}

func (inertPermissionReviewer) reviewPermission(context.Context, *permissionReviewRequest, chan<- ChatResponse) permissionReviewDecision {
	return permissionReviewFailure("automatic permission reviewer is unavailable")
}

type toolPermissionDecision struct {
	denied  bool
	message string
	review  *permissionReviewRequest
}

type directSkillInputError struct {
	message string
}

func (e directSkillInputError) Error() string {
	return e.message
}

type permissionReviewSubject struct {
	Subject     string `json:"subject"`
	RulePattern string `json:"rule_pattern"`
}

type permissionReviewRequest struct {
	ActiveAgent      string                                  `json:"active_agent"`
	ToolName         string                                  `json:"tool_name"`
	Permission       string                                  `json:"permission"`
	RawArguments     string                                  `json:"raw_arguments"`
	Subjects         []string                                `json:"subjects"`
	AutoSubjects     []permissionReviewSubject               `json:"auto_subjects"`
	Reviewer         string                                  `json:"reviewer"`
	ReviewerEmbedded bool                                    `json:"reviewer_embedded"`
	ReviewContext    []responses.ResponseInputItemUnionParam `json:"-"`
	// Review is the review's child-session key under the reviewing session, which the reviewed call starts.
	Review ChildKey `json:"-"`
	// Turn is the review's turn in that history, from the key of the call it checks (see ReviewTurn).
	Turn string `json:"-"`
}

type permissionReviewRiskLevel string

const (
	permissionReviewRiskLevelLow      permissionReviewRiskLevel = "low"
	permissionReviewRiskLevelMedium   permissionReviewRiskLevel = "medium"
	permissionReviewRiskLevelHigh     permissionReviewRiskLevel = "high"
	permissionReviewRiskLevelCritical permissionReviewRiskLevel = "critical"
)

type permissionReviewUserAuthorization string

const (
	permissionReviewUserAuthorizationUnknown permissionReviewUserAuthorization = "unknown"
	permissionReviewUserAuthorizationLow     permissionReviewUserAuthorization = "low"
	permissionReviewUserAuthorizationMedium  permissionReviewUserAuthorization = "medium"
	permissionReviewUserAuthorizationHigh    permissionReviewUserAuthorization = "high"
)

type permissionReviewOutcome string

const (
	permissionReviewOutcomeAllow permissionReviewOutcome = "allow"
	permissionReviewOutcomeDeny  permissionReviewOutcome = "deny"
)

type permissionReviewDecision struct {
	RiskLevel         permissionReviewRiskLevel         `json:"risk_level"`
	UserAuthorization permissionReviewUserAuthorization `json:"user_authorization"`
	Outcome           permissionReviewOutcome           `json:"outcome"`
	Rationale         string                            `json:"rationale"`
}

// Runtime is the concrete RocketCode loop runtime returned by New.
type Runtime = looper

// Looper processes prompt input streams with a configured runtime.
type Looper interface {
	Loop(ctx context.Context, input <-chan PromptInput, sessionIn iter.Seq2[SessionEntry, error], sessionOut sessionWriter, interrupts <-chan os.Signal) error
}

type sessionWriter interface {
	Append(context.Context, *SessionEntry) error
}

type toolCallSignature struct {
	name string
	args string
}

type dispatchedToolOutput struct {
	Name        string
	Param       responses.ResponseInputItemFunctionCallOutputParam
	Result      ToolResult
	ReplayInput []responses.ResponseInputItemUnionParam
}

type compactionBlock struct {
	end      int
	complete bool
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
	Provider       string            `json:"provider"`
	Model          string            `json:"model"`
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
	Version           int                 `json:"version"`
	Type              string              `json:"type"`
	Timestamp         time.Time           `json:"timestamp"`
	TurnID            string              `json:"turn_id,omitempty"`
	ResponseID        string              `json:"response_id,omitempty"`
	Model             string              `json:"model,omitempty"`
	Agent             string              `json:"agent,omitempty"`
	ReasoningEffort   *string             `json:"reasoning_effort,omitempty"`
	ReplayAttribution []ReplayAttribution `json:"replay_attribution,omitempty"`
	TokenUsage        *TokenUsage         `json:"token_usage,omitempty"`
	ReplayInput       []json.RawMessage   `json:"replay_input,omitempty"`
	OutputTrace       []json.RawMessage   `json:"output_trace,omitempty"`
}

// TokenUsage records replay-neutral provider token counts for a completed turn.
type TokenUsage struct {
	PromptTokens              int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens          int64 `json:"completion_tokens,omitempty"`
	TotalTokens               int64 `json:"total_tokens,omitempty"`
	PromptCacheReadTokens     int64 `json:"prompt_cache_read_tokens,omitempty"`
	PromptCacheWriteTokens    int64 `json:"prompt_cache_write_tokens,omitempty"`
	CompletionReasoningTokens int64 `json:"completion_reasoning_tokens,omitempty"`
}

func (u *TokenUsage) add(other TokenUsage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
	u.PromptCacheReadTokens += other.PromptCacheReadTokens
	u.PromptCacheWriteTokens += other.PromptCacheWriteTokens
	u.CompletionReasoningTokens += other.CompletionReasoningTokens
}

func (e *SessionEntry) addTokenUsageFromResponse(resp *responses.Response) {
	usage, ok := tokenUsageFromResponse(resp)
	if !ok {
		return
	}

	if e.TokenUsage == nil {
		e.TokenUsage = &TokenUsage{}
	}

	e.TokenUsage.add(usage)
}

func (u TokenUsage) attributes() []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.Int64(semconv.LLMTokenCountPrompt, u.PromptTokens),
		attribute.Int64(semconv.LLMTokenCountCompletion, u.CompletionTokens),
		attribute.Int64(semconv.LLMTokenCountTotal, u.TotalTokens),
		attribute.Int64(semconv.LLMTokenCountPromptDetailsCacheRead, u.PromptCacheReadTokens),
		attribute.Int64(semconv.LLMTokenCountPromptDetailsCacheWrite, u.PromptCacheWriteTokens),
		attribute.Int64(semconv.LLMTokenCountCompletionDetailsReasoning, u.CompletionReasoningTokens),
	}

	return slices.DeleteFunc(attrs, func(attr attribute.KeyValue) bool {
		return attr.Value.AsInt64() == 0
	})
}

func tokenUsageFromResponse(resp *responses.Response) (TokenUsage, bool) {
	if resp == nil || !presentOpenAIField(resp.JSON.Usage) {
		return TokenUsage{}, false
	}

	var usage TokenUsage
	if presentOpenAIField(resp.Usage.JSON.InputTokens) {
		usage.PromptTokens = resp.Usage.InputTokens
	}

	if presentOpenAIField(resp.Usage.JSON.OutputTokens) {
		usage.CompletionTokens = resp.Usage.OutputTokens
	}

	if presentOpenAIField(resp.Usage.JSON.TotalTokens) {
		usage.TotalTokens = resp.Usage.TotalTokens
	}

	if presentOpenAIField(resp.Usage.InputTokensDetails.JSON.CachedTokens) {
		usage.PromptCacheReadTokens = resp.Usage.InputTokensDetails.CachedTokens
	}

	if field, ok := resp.Usage.JSON.ExtraFields["cache_creation_input_tokens"]; ok && presentOpenAIField(field) {
		if tokens, errParse := strconv.ParseInt(field.Raw(), 10, 64); errParse == nil {
			usage.PromptCacheWriteTokens = tokens
		}
	}

	if presentOpenAIField(resp.Usage.OutputTokensDetails.JSON.ReasoningTokens) {
		usage.CompletionReasoningTokens = resp.Usage.OutputTokensDetails.ReasoningTokens
	}

	return usage, true
}

func presentOpenAIField(field openairespjson.Field) bool {
	return field.Raw() != openairespjson.Omitted && field.Valid()
}

// Loop processes input lines until input closes or a runtime error occurs.
func (l *looper) Loop(
	ctx context.Context,
	input <-chan PromptInput,
	sessionIn iter.Seq2[SessionEntry, error],
	sessionOut sessionWriter,
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

	if interrupts == nil {
		return errors.New("interrupts channel is required")
	}

	var (
		history            []responses.ResponseInputItemUnionParam
		historyAttribution []ReplayAttribution
	)

	loaded := false

	for line := range input {
		if line.Responses == nil {
			return errors.New("prompt response channel is required")
		}

		turnOutput := line.Responses

		if strings.Join(line.Texts, "") == "" && len(line.Attachments) == 0 && line.DirectSkill == nil {
			close(turnOutput)

			continue
		}

		if !loaded {
			var err error

			var entries []SessionEntry

			history, entries, err = loadSession(sessionIn, providerForModel(l.DisplayModel))
			if err != nil {
				close(turnOutput)

				return err
			}

			loaded = true

			offset := 0

			for i := range entries {
				entry := &entries[i]
				historyAttribution = append(historyAttribution, entry.attributionRanges(offset)...)
				offset += len(entry.ReplayInput)
			}
		}

		turn, rendered, interrupted, err := l.runTurn(ctx, turnOutput, interrupts, history, historyAttribution, &line)
		observations := l.observations

		if err != nil {
			observations.close()

			if errDirectSkill, ok := errors.AsType[directSkillInputError](err); ok {
				emitChatResponse(turnOutput, ChatResponse{Kind: ChatResponseAssistantMessage, Text: errDirectSkill.Error()})
				close(turnOutput)

				continue
			}

			close(turnOutput)

			return fmt.Errorf("run turn: %w", err)
		}

		if interrupted {
			observations.close()
			close(turnOutput)

			continue
		}

		observations.mu.Lock()
		observations.closed = true

		trace := slices.Clone(observations.trace)
		for i := range observations.calls {
			// Records contain only strings; JSON encoding cannot fail.
			raw, _ := json.Marshal(&observations.calls[i])
			trace = append(trace, raw)
		}

		turn.OutputTrace = append(trace, turn.OutputTrace...)

		errSave := observations.err
		if errSave == nil {
			if errAppend := sessionOut.Append(ctx, &turn); errAppend != nil {
				errSave = fmt.Errorf("append session turn: %w", errAppend)
			}
		}
		observations.mu.Unlock()

		if errSave != nil {
			close(turnOutput)

			return errSave
		}

		items, err := ReplayInputToParams(turn.ReplayInput)
		if err != nil {
			close(turnOutput)

			return err
		}

		historyAttribution = append(historyAttribution, turn.attributionRanges(len(history))...)
		history = append(history, items...)

		for _, item := range rendered {
			emitChatResponse(turnOutput, item)
		}

		close(turnOutput)
	}

	return nil
}

func (l *looper) setPhase(phase TurnPhase) {
	l.phaseMu.Lock()
	l.turnPhase = phase
	l.phaseMu.Unlock()
}

func (l *looper) runTurn(
	ctx context.Context,
	output chan<- ChatResponse,
	interrupts <-chan os.Signal,
	baseHistory []responses.ResponseInputItemUnionParam,
	baseAttribution []ReplayAttribution,
	input *PromptInput,
) (record SessionEntry, rendered []ChatResponse, interrupted bool, err error) {
	var emptyRecord SessionEntry

	observations := &turnObservations{journal: l.Journal}
	l.observations = observations

	step, turnItems, pending, errInput, err := l.openTurn(ctx, input)
	if err != nil {
		return emptyRecord, nil, false, err
	}

	record = step.Record
	applied := step.AppliedInputIDs
	observations.turnID = record.TurnID
	observations.trace = slices.Clone(step.Trace)

	l.restoreTurnExecuteResults(record.TurnID)

	defer func() {
		if ctx.Err() == nil {
			l.deleteTurnExecuteResults()
		}
	}()

	if errInput != nil {
		if err := observations.observe(ctx, &PublicProgress{ID: "input-error", Kind: PublicProgressText, State: PublicProgressFailed, Text: errInput.Error(), Agent: record.Agent, Model: record.Model}); err != nil {
			return emptyRecord, nil, false, err
		}

		return emptyRecord, nil, false, directSkillInputError{message: errInput.Error()}
	}

	turnCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	turnCtx, span := l.Observability.startSpan(turnCtx, "rocketcode.turn", semconv.SpanKindAgent,
		attribute.String(semconv.AgentName, l.agent.Name),
		l.Observability.inputValue(strings.Join(input.Texts, "")),
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
		// Each step, a resumed turn's first too, runs as the type the last steer naming one switched the turn to.
		if l, err = l.asRecordAgent(turnCtx, &record, &baseHistory, &baseAttribution, &turnItems); err != nil {
			return emptyRecord, nil, false, err
		}

		resp := pending
		pending = nil

		if resp == nil {
			history := append(append([]responses.ResponseInputItemUnionParam{}, baseHistory...), turnItems...)
			history = pruneHistoryBeforeLatestCompaction(history)

			params := l.buildParams(history)

			attribution := append(slices.Clone(baseAttribution), record.attributionRanges(len(baseHistory))...)
			attributionEnd := len(baseHistory) + len(turnItems)

			observer := &responseObservations{owner: observations, agent: record.Agent, model: record.Model}

			var errResponse error

			resp, errResponse = l.newProviderResponse(turnCtx, &params, observer, output, func(recovered []responses.ResponseInputItemUnionParam, retained int) error {
				// Retained items come from provider input, which omits local metadata.
				recovered = slices.Clone(recovered)
				copy(recovered[len(recovered)-retained:], history[len(history)-retained:])
				recovered = pruneHistoryBeforeLatestCompaction(recovered)

				replayInput, errReplay := ReplayInputFromParams(recovered)
				if errReplay != nil {
					return errReplay
				}

				record.ReplayInput = replayInput
				record.ReplayAttribution = make([]ReplayAttribution, 0, len(attribution))

				start := attributionEnd - retained
				for _, snapshot := range attribution {
					snapshot.Start = max(snapshot.Start, start) + len(recovered) - retained - start

					snapshot.End = max(min(snapshot.End, attributionEnd), start) + len(recovered) - retained - start
					if snapshot.Start != snapshot.End {
						record.ReplayAttribution = append(record.ReplayAttribution, snapshot)
					}
				}

				turnItems = append([]responses.ResponseInputItemUnionParam(nil), recovered...)

				return l.saveTurn(turnCtx, &record, nil, applied)
			})
			if errResponse != nil {
				if errors.Is(context.Cause(turnCtx), errTurnInterrupted) {
					return emptyRecord, nil, true, nil
				}

				return emptyRecord, nil, false, fmt.Errorf("request response: %w", errResponse)
			}

			l.emitHostedToolDiagnostics(output, resp.Output)

			if err := l.appendProviderReplay(&record, &turnItems, resp); err != nil {
				return emptyRecord, nil, false, err
			}

			if err := l.saveTurn(turnCtx, &record, resp, applied); err != nil {
				return emptyRecord, nil, false, fmt.Errorf("record provider response: %w", err)
			}
		}

		// Stream reasoning/commentary live; defer final assistant messages to turn end.
		rendered = appendLiveTurnResponses(output, rendered, responseChatResponses(resp.Output))
		reviewContext := pruneHistoryBeforeLatestCompaction(append(append([]responses.ResponseInputItemUnionParam{}, baseHistory...), turnItems...))

		hadToolCalls, compactionOnly, toolOutputs, interrupted, err := l.dispatchProviderTools(turnCtx, resp, reviewContext, &doomLoop, output)
		if err != nil || interrupted {
			return emptyRecord, nil, interrupted, err
		}

		if !hadToolCalls {
			if compactionOnly {
				continue
			}

			injected, errSteers := l.appendSteers(turnCtx, &record, &turnItems, TurnPhaseFinalAnswer, &applied)
			if errSteers != nil {
				return emptyRecord, nil, false, errSteers
			}

			if !injected {
				return record, rendered, false, nil
			}

			if err := l.saveTurn(turnCtx, &record, nil, applied); err != nil {
				return emptyRecord, nil, false, err
			}

			continue
		}

		if err := l.appendToolOutputReplay(&record, &turnItems, toolOutputs); err != nil {
			return emptyRecord, nil, false, err
		}

		if _, err := l.appendSteers(turnCtx, &record, &turnItems, TurnPhaseToolLoop, &applied); err != nil {
			return emptyRecord, nil, false, err
		}

		if err := l.saveTurn(turnCtx, &record, nil, applied); err != nil {
			return emptyRecord, nil, false, fmt.Errorf("record tool outputs: %w", err)
		}
	}
}

// openTurn resumes the journaled turn for input's TurnID, or starts a new one from the prompt.
func (l *looper) openTurn(ctx context.Context, input *PromptInput) (step turnStep, turnItems []responses.ResponseInputItemUnionParam, pending *responses.Response, errInput, err error) {
	resumed := false
	if input.TurnID != "" {
		if resumed, err = loadStep(ctx, l.Journal, input.TurnID, &step); err != nil {
			return step, nil, nil, nil, err
		}
	}

	if !resumed {
		turnItems, errInput = l.promptTurnItems(ctx, input)

		replayInput, err := ReplayInputFromParams(turnItems)
		if err != nil {
			return step, nil, nil, nil, err
		}

		step.Record = SessionEntry{
			Version:         1,
			Type:            "turn",
			Timestamp:       time.Now().UTC(),
			Model:           l.DisplayModel,
			Agent:           l.agent.Name,
			ReasoningEffort: new(string(l.ReasoningEffort)),
			ReplayInput:     replayInput,
		}

		step.Record.TurnID = cmp.Or(input.TurnID, strconv.FormatInt(step.Record.Timestamp.UnixNano(), 10))
		if input.ID != "" {
			step.AppliedInputIDs = []string{input.ID}
		}

		if err := l.saveTurn(ctx, &step.Record, nil, step.AppliedInputIDs); err != nil {
			return step, nil, nil, nil, err
		}

		return step, turnItems, nil, errInput, nil
	}

	// A resumed turn's lists and Code Mode records were saved on their own, not in its step.
	for list, items := range map[TurnList]*[]json.RawMessage{TurnReplayInput: &step.Record.ReplayInput, TurnOutputTrace: &step.Record.OutputTrace, TurnTrace: &step.Trace} {
		if *items, err = l.Journal.Items(context.WithoutCancel(ctx), input.TurnID, list); err != nil {
			return step, nil, nil, nil, fmt.Errorf("load journaled %s: %w", list, err)
		}
	}

	l.observations.saved = step

	calls, err := l.Journal.CodeModeCalls(context.WithoutCancel(ctx), input.TurnID)
	if err != nil {
		return step, nil, nil, nil, fmt.Errorf("load code mode calls: %w", err)
	}

	l.observations.calls = CodeModeCallsFromTrace(calls)

	if err := step.Record.forProvider(providerForModel(l.DisplayModel)); err != nil {
		return step, nil, nil, nil, fmt.Errorf("convert journaled turn for provider: %w", err)
	}

	if turnItems, err = ReplayInputToParams(step.Record.ReplayInput); err != nil {
		return step, nil, nil, nil, err
	}
	// Older checkpoints still have identities in replay; retain those on the next
	// save so later compaction cannot erase their deduplication evidence.
	for i := range turnItems {
		item := &turnItems[i]
		if item.OfMessage != nil {
			if id, ok := item.OfMessage.ExtraFields()["input_id"].(string); ok && id != "" && !slices.Contains(step.AppliedInputIDs, id) {
				step.AppliedInputIDs = append(step.AppliedInputIDs, id)
			}
		}
	}

	if len(step.Response) > 0 {
		pending = new(responses.Response)
		if err := pending.UnmarshalJSON(step.Response); err != nil {
			return step, nil, nil, nil, fmt.Errorf("decode journaled response: %w", err)
		}
	}

	return step, turnItems, pending, nil, nil
}

// saveTurn journals the turn's step, with the provider response whose calls are not yet appended,
// and of its record's items and trace only those that changed since its last save.
func (l *looper) saveTurn(ctx context.Context, record *SessionEntry, resp *responses.Response, applied []string) error {
	o := l.observations
	step := turnStep{Record: *record, AppliedInputIDs: applied, Revision: o.saved.Revision + 1}

	o.mu.Lock()
	step.Trace = slices.Clone(o.trace)
	o.mu.Unlock()

	if resp != nil {
		step.Response = json.RawMessage(resp.RawJSON())
	}

	head := step
	head.Record.ReplayInput, head.Record.OutputTrace, head.Trace = nil, nil, nil

	raw, err := json.Marshal(&head)
	if err != nil {
		return fmt.Errorf("encode journal turn %q: %w", record.TurnID, err)
	}

	lists := []TurnItems{{List: TurnReplayInput, Items: step.Record.ReplayInput}, {List: TurnOutputTrace, Items: step.Record.OutputTrace}, {List: TurnTrace, Items: step.Trace}}
	for i, saved := range [][]json.RawMessage{o.saved.Record.ReplayInput, o.saved.Record.OutputTrace, o.saved.Trace} {
		list := &lists[i]
		for list.From < min(len(saved), len(list.Items)) && bytes.Equal(saved[list.From], list.Items[list.From]) {
			list.From++
		}

		// Each item is encoded as it was within its encoded step, so it reads back as it always has.
		changed := make([]json.RawMessage, 0, len(list.Items)-list.From)
		for _, item := range list.Items[list.From:] {
			encoded, err := json.Marshal(item)
			if err != nil {
				return fmt.Errorf("encode journal turn %q %s item: %w", record.TurnID, list.List, err)
			}

			changed = append(changed, encoded)
		}

		list.Items = changed
	}

	if err := l.Journal.SaveTurn(context.WithoutCancel(ctx), record.TurnID, raw, lists); err != nil {
		return fmt.Errorf("save journal turn %q: %w", record.TurnID, err)
	}

	o.saved = step

	return nil
}

func (l *looper) dispatchProviderTools(ctx context.Context, resp *responses.Response, reviewContext []responses.ResponseInputItemUnionParam, doomLoop *doomLoopTrap, output chan<- ChatResponse) (hadToolCalls, compactionOnly bool, toolOutputs []dispatchedToolOutput, interrupted bool, err error) {
	hadToolCalls = slices.ContainsFunc(resp.Output, func(item responses.ResponseOutputItemUnion) bool { return item.Type == "function_call" })

	compactionOnly = len(resp.Output) > 0 && !slices.ContainsFunc(resp.Output, func(item responses.ResponseOutputItemUnion) bool { return item.Type != "compaction" })
	if hadToolCalls {
		l.setPhase(TurnPhaseToolLoop)
	} else if !compactionOnly {
		l.setPhase(TurnPhaseFinalAnswer)
	}

	previousReviewInput := l.permissionReviewInput
	l.permissionReviewInput = reviewContext
	toolOutputs, _, err = l.dispatchToolCalls(ctx, resp, doomLoop, output)
	l.permissionReviewInput = previousReviewInput

	if err == nil {
		return hadToolCalls, compactionOnly, toolOutputs, false, nil
	}

	if errors.Is(context.Cause(ctx), errTurnInterrupted) {
		return false, false, nil, true, nil
	}

	return false, false, nil, false, fmt.Errorf("dispatch tool calls: %w", err)
}

// appendSteers skips a drained input whose ID is already in the turn, since a
// resumed turn's embedder re-offers every steer it accepted.
func (l *looper) appendSteers(ctx context.Context, record *SessionEntry, turnItems *[]responses.ResponseInputItemUnionParam, phase TurnPhase, applied *[]string) (bool, error) {
	inputs, err := l.SteerDrain.Drain(ctx, record.ResponseID, phase, slices.Clone(*applied))
	if err != nil {
		return false, fmt.Errorf("drain turn input: %w", err)
	}

	injected := false

	for i := range inputs {
		input := &inputs[i]
		if input.ID != "" && slices.Contains(*applied, input.ID) {
			continue
		}

		// A steer naming another agent type switches the turn to it from its next step; the steps so far keep theirs.
		if input.Agent != "" && input.Agent != record.Agent {
			record.ReplayAttribution = append(record.ReplayAttribution, ReplayAttribution{End: len(record.ReplayInput), Agent: record.Agent, Model: record.Model, ReasoningEffort: record.ReasoningEffort})
			record.Agent = input.Agent
		}

		if input.DirectSkill != nil {
			item, err := l.directSkillInput(ctx, input.DirectSkill)
			if err != nil {
				return false, err
			}

			if err := appendReplayInput(record, &item); err != nil {
				return false, err
			}

			*turnItems = append(*turnItems, item)
		}

		steer := promptInputMessage(input)
		if err := appendReplayInput(record, &steer); err != nil {
			return false, err
		}

		*turnItems = append(*turnItems, steer)
		if input.ID != "" {
			*applied = append(*applied, input.ID)
		}

		injected = true
	}

	return injected, nil
}

func (l *looper) appendProviderReplay(record *SessionEntry, turnItems *[]responses.ResponseInputItemUnionParam, resp *responses.Response) error {
	record.ResponseID = resp.ID
	record.addTokenUsageFromResponse(resp)
	hadCompaction := slices.ContainsFunc(resp.Output, func(item responses.ResponseOutputItemUnion) bool { return item.Type == "compaction" })

	for i := range resp.Output {
		asInput, ok := responseOutputToReplayInput(&resp.Output[i])
		if !ok {
			if trace, err := json.Marshal(resp.Output[i]); err == nil {
				record.OutputTrace = append(record.OutputTrace, trace)
			}

			continue
		}

		if err := appendReplayInput(record, &asInput); err != nil {
			return err
		}

		*turnItems = append(*turnItems, asInput)
	}

	if hadCompaction && l.CompactionSteering != "" {
		steeringInput := inputMessageParam(responses.EasyInputMessageRole("developer"), easyInputStringContent(l.CompactionSteering))

		if err := appendReplayInput(record, &steeringInput); err != nil {
			return err
		}

		*turnItems = append(*turnItems, steeringInput)
	}

	return nil
}

func (l *looper) appendToolOutputReplay(record *SessionEntry, turnItems *[]responses.ResponseInputItemUnionParam, toolOutputs []dispatchedToolOutput) error {
	for i := range toolOutputs {
		toolInput := responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &toolOutputs[i].Param}

		if err := appendReplayInput(record, &toolInput); err != nil {
			return err
		}

		*turnItems = append(*turnItems, toolInput)
	}

	for i := range toolOutputs {
		for j := range toolOutputs[i].ReplayInput {
			replayInput := &toolOutputs[i].ReplayInput[j]
			if err := appendReplayInput(record, replayInput); err != nil {
				return err
			}

			*turnItems = append(*turnItems, *replayInput)
		}
	}

	return nil
}

func (l *looper) promptTurnItems(ctx context.Context, input *PromptInput) ([]responses.ResponseInputItemUnionParam, error) {
	if l.expandInputPrompts && input.DirectSkill == nil {
		texts := make([]string, len(input.Texts))
		for i, text := range input.Texts {
			texts[i] = l.promptExpansion.expandShellCommands(ctx, text)
		}

		input.Texts = texts
	}

	turnItems := []responses.ResponseInputItemUnionParam{promptInputMessage(input)}

	if input.DirectSkill != nil {
		directSkillItem, err := l.directSkillInput(ctx, input.DirectSkill)
		if err != nil {
			return turnItems, err
		}

		turnItems = slices.Insert(turnItems, 0, directSkillItem)
	}

	return turnItems, nil
}

func (l *looper) directSkillInput(ctx context.Context, input *PromptInputDirectSkill) (responses.ResponseInputItemUnionParam, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return responses.ResponseInputItemUnionParam{}, errors.New("direct skill invocation requires a skill name")
	}

	tool, ok := l.Tools["skill"]
	if !ok {
		return responses.ResponseInputItemUnionParam{}, fmt.Errorf("skill %q is not available to the active agent", name)
	}

	raw, err := json.Marshal(skillToolParams{Name: name, Arguments: input.Arguments, Direct: true})
	if err != nil {
		return responses.ResponseInputItemUnionParam{}, fmt.Errorf("marshal direct skill input: %w", err)
	}

	decision, err := l.permissionDecision("skill", &tool, raw)
	if err != nil {
		return responses.ResponseInputItemUnionParam{}, err
	}

	if decision.denied {
		return responses.ResponseInputItemUnionParam{}, errors.New(decision.message)
	}

	if decision.review != nil {
		return responses.ResponseInputItemUnionParam{}, fmt.Errorf("skill %q is not visible to the active agent", name)
	}

	result, replayInput, err := tool.CallReplay(ctx, raw, nil, toolCallMetadata{})
	if err != nil {
		return responses.ResponseInputItemUnionParam{}, err
	}

	if len(replayInput) > 0 {
		return replayInput[0], nil
	}

	return inputMessageParam(responses.EasyInputMessageRoleDeveloper, easyInputStringContent(result.Output)), nil
}

func appendReplayInput(record *SessionEntry, item *responses.ResponseInputItemUnionParam) error {
	raw, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{*item})
	if err != nil {
		return err
	}

	record.ReplayInput = append(record.ReplayInput, raw...)

	return nil
}

func (l *looper) newProviderResponse(
	ctx context.Context,
	params *responses.ResponseNewParams,
	observer responseObserver,
	output chan<- ChatResponse,
	checkpointCompacted func([]responses.ResponseInputItemUnionParam, int) error,
) (resp *responses.Response, err error) {
	provider := l.ProviderOrigin.Provider

	ctx, span := l.Observability.startSpan(ctx, "rocketcode.provider", semconv.SpanKindLLM,
		attribute.String(semconv.LLMModelName, l.ProviderOrigin.Model),
		attribute.String(semconv.LLMProvider, provider),
	)
	defer func() {
		recordSpanError(span, err)

		if resp != nil {
			span.span.SetAttributes(attribute.String("rocketcode.response_id", resp.ID))

			if usage, ok := tokenUsageFromResponse(resp); ok {
				span.span.SetAttributes(usage.attributes()...)
			}
		}

		span.span.End()
	}()

	return l.newResponseWithProviderRetry(ctx, params, observer, output, checkpointCompacted)
}

func (l *looper) newResponseWithProviderRetry(ctx context.Context, params *responses.ResponseNewParams, observer responseObserver, output chan<- ChatResponse, checkpointCompacted func([]responses.ResponseInputItemUnionParam, int) error) (*responses.Response, error) {
	attempt := 0
	provider := l.ProviderOrigin.Provider

	for {
		var raw *http.Response

		resp, err := l.Client.New(ctx, params, observer, option.WithResponseInto(&raw))
		if err != nil {
			if _, ok := errors.AsType[progressPersistenceError](err); ok {
				return nil, fmt.Errorf("observe provider response: %w", err)
			}

			if ctx.Err() == nil && isContextLengthExceeded(err) {
				resp, err := l.newResponseAfterContextCompaction(ctx, params, observer, err, output, checkpointCompacted)
				if err != nil {
					return nil, fmt.Errorf("new response: %w", err)
				}

				return resp, nil
			}

			errReturn := err
			if ctx.Err() == nil {
				if errAPI, ok := errors.AsType[*openai.Error](err); ok {
					if errAPI.StatusCode == http.StatusTooManyRequests && (errAPI.Code == "too_many_requests" || errAPI.Type == "too_many_requests") {
						attempt++
						wait := providerRetryDelay(errAPI.Response, attempt)
						errRateLimit := &providerRateLimitError{provider: provider, code: errAPI.Code, typeName: errAPI.Type, message: errAPI.Message, retryAfter: wait, requestID: providerRequestID(errAPI.Response), cause: err}

						if attempt > providerRateLimitMaxRetries {
							diagnostic := ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: http.StatusTooManyRequests, Code: errRateLimit.code, Type: errRateLimit.typeName, Message: errRateLimit.message, RetryAfter: errRateLimit.retryAfter.String(), Headers: providerDiagnosticHeaders(errAPI.Response)}
							l.emitProviderDiagnostic(ctx, output, &diagnostic)

							return nil, fmt.Errorf("new response: %w", errRateLimit)
						}

						diagnostic := ProviderDiagnostic{Phase: providerDiagnosticRetry, HTTPStatus: http.StatusTooManyRequests, Code: errRateLimit.code, Type: errRateLimit.typeName, Message: errRateLimit.message, Attempt: attempt, RetryAfter: errRateLimit.retryAfter.String(), Headers: providerDiagnosticHeaders(errAPI.Response)}
						l.emitProviderDiagnostic(ctx, output, &diagnostic)

						if err := waitProviderRetry(ctx, wait); err != nil {
							return nil, fmt.Errorf("wait for provider retry: %w", err)
						}

						continue
					}

					if errAPI.StatusCode == http.StatusTooManyRequests {
						switch {
						case errAPI.Code == "usage_limit_reached" || errAPI.Type == "usage_limit_reached":
							errReturn = &providerUsageLimitError{provider: provider, code: errAPI.Code, typeName: errAPI.Type, message: errAPI.Message, requestID: providerRequestID(errAPI.Response), cause: err}
						case errAPI.Code == "usage_not_included" || errAPI.Type == "usage_not_included":
							errReturn = &providerUsageNotIncludedError{provider: provider, code: errAPI.Code, typeName: errAPI.Type, message: errAPI.Message, requestID: providerRequestID(errAPI.Response), cause: err}
						default:
							errReturn = &providerRetryLimitError{provider: provider, httpStatus: errAPI.StatusCode, requestID: providerRequestID(errAPI.Response), cause: err}
						}
					}

					diagnostic := ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: errAPI.StatusCode, Code: errAPI.Code, Type: errAPI.Type, Message: errAPI.Message, Headers: providerDiagnosticHeaders(errAPI.Response)}
					l.emitProviderDiagnostic(ctx, output, &diagnostic)
				} else {
					diagnostic := ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: 0, ResponseStatus: "", Code: "", Message: err.Error(), Attempt: 0, RetryAfter: "", ResponseID: ""}
					l.emitProviderDiagnostic(ctx, output, &diagnostic)
				}
			}

			return nil, fmt.Errorf("new response: %w", errReturn)
		}

		if resp == nil {
			err := errors.New("missing response")
			l.emitProviderDiagnostic(ctx, output, &ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: 0, ResponseStatus: "", Code: "", Message: err.Error(), Attempt: 0, RetryAfter: "", ResponseID: ""})

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

		if isContextLengthExceeded(err) {
			resp, err := l.newResponseAfterContextCompaction(ctx, params, observer, err, output, checkpointCompacted)
			if err != nil {
				return nil, err
			}

			return resp, nil
		}

		if resp.Error.Code != responses.ResponseErrorCodeRateLimitExceeded {
			diagnostic := providerDiagnosticFromFailedResponse(resp, providerDiagnosticError, 0, 0, nil)
			l.emitProviderDiagnostic(ctx, output, &diagnostic)

			return nil, err
		}

		attempt++
		wait := providerRetryDelay(raw, attempt)
		err = &providerRateLimitError{provider: provider, code: string(resp.Error.Code), message: resp.Error.Message, retryAfter: wait, responseID: resp.ID, requestID: providerRequestID(raw), cause: err}

		if attempt > providerRateLimitMaxRetries {
			diagnostic := providerDiagnosticFromFailedResponse(resp, providerDiagnosticError, 0, 0, raw)
			l.emitProviderDiagnostic(ctx, output, &diagnostic)

			return nil, err
		}

		diagnostic := providerDiagnosticFromFailedResponse(resp, providerDiagnosticRetry, attempt, wait, raw)
		l.emitProviderDiagnostic(ctx, output, &diagnostic)

		if err := waitProviderRetry(ctx, wait); err != nil {
			return nil, fmt.Errorf("wait for provider retry: %w", err)
		}
	}
}

func (l *looper) newResponseAfterContextCompaction(ctx context.Context, params *responses.ResponseNewParams, observer responseObserver, errOriginal error, output chan<- ChatResponse, checkpointCompacted func([]responses.ResponseInputItemUnionParam, int) error) (*responses.Response, error) {
	original := params.Input.OfInputItemList

	blocks := compactionBlocks(original)
	if len(blocks) < 2 {
		return nil, errOriginal
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
			return nil, fmt.Errorf("compact context after context_length_exceeded: %w", err)
		}

		compactedInput, err := compactedOutputToReplayParams(compacted.Output)
		if err != nil {
			return nil, fmt.Errorf("convert compacted response: %w", err)
		}

		recoveredHistory := append(append([]responses.ResponseInputItemUnionParam{}, compactedInput...), original[end:]...)
		retryParams := *params

		retryParams.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: recoveredHistory}
		if err := checkpointCompacted(recoveredHistory, len(original)-end); err != nil {
			return nil, fmt.Errorf("checkpoint compacted response input: %w", err)
		}

		var raw *http.Response

		resp, err := l.Client.New(ctx, &retryParams, observer, option.WithResponseInto(&raw))
		if err != nil {
			if _, ok := errors.AsType[progressPersistenceError](err); ok {
				return nil, fmt.Errorf("observe compacted provider response: %w", err)
			}

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

			return nil, fmt.Errorf("retry compacted response: %w", err)
		}

		if resp == nil {
			err := errors.New("missing response")
			l.emitProviderDiagnostic(ctx, output, &ProviderDiagnostic{Phase: providerDiagnosticError, HTTPStatus: 0, ResponseStatus: "", Code: "", Message: err.Error(), Attempt: 0, RetryAfter: "", ResponseID: ""})

			return nil, err
		}

		if resp.Status != responses.ResponseStatusFailed {
			return resp, nil
		}

		errLast = &responseFailureError{responseID: resp.ID, status: resp.Status, code: resp.Error.Code, message: resp.Error.Message}
		if isContextLengthExceeded(errLast) {
			continue
		}

		diagnostic := providerDiagnosticFromFailedResponse(resp, providerDiagnosticError, 0, 0, nil)
		l.emitProviderDiagnostic(ctx, output, &diagnostic)

		return nil, errLast
	}

	return nil, errLast
}

func compactionBlocks(items []responses.ResponseInputItemUnionParam) []compactionBlock {
	blocks := make([]compactionBlock, 0, len(items))
	for i := 0; i < len(items); {
		if items[i].OfFunctionCall == nil {
			blocks = append(blocks, compactionBlock{end: i + 1, complete: true})
			i++

			continue
		}

		pending := map[string]bool{}

		end := len(items)
		complete := false

		for j := i; j < len(items); j++ {
			if call := items[j].OfFunctionCall; call != nil {
				pending[call.CallID] = true
			}

			if output := items[j].OfFunctionCallOutput; output != nil {
				delete(pending, output.CallID.Or(""))
			}

			if j > i && len(pending) == 0 {
				end = j + 1
				complete = true

				break
			}
		}

		blocks = append(blocks, compactionBlock{end: end, complete: complete})
		i = end
	}

	return blocks
}

func compactedOutputToReplayParams(items []responses.ResponseOutputItemUnion) ([]responses.ResponseInputItemUnionParam, error) {
	input := make([]responses.ResponseInputItemUnionParam, 0, len(items))
	for i := range items {
		switch items[i].Type {
		case "message":
			role := strings.TrimSpace(items[i].Role)
			if role == "" {
				role = "user"
			}

			message := responses.EasyInputMessageParam{Role: responses.EasyInputMessageRole(role), Content: easyInputStringContent(responseItemText(&items[i])), Type: "message"}
			if items[i].Phase != "" {
				message.Phase = responses.EasyInputMessagePhase(items[i].Phase)
			}

			input = append(input, responses.ResponseInputItemUnionParam{OfMessage: &message})
		case "compaction", "compaction_summary":
			input = append(input, compactionReplayInput(items[i].ID, items[i].EncryptedContent, responseItemText(&items[i])))
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

func waitProviderRetry(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("provider retry canceled: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

func providerRetryDelay(resp *http.Response, attempt int) time.Duration {
	wait := providerRetryBackoff(attempt)
	if resp == nil {
		return wait
	}

	found := false
	headerWait := time.Duration(0)

	if millis, errParse := strconv.ParseFloat(resp.Header.Get("Retry-After-Ms"), 64); errParse == nil && millis >= 0 && millis == millis {
		if delay := time.Duration(millis * float64(time.Millisecond)); delay >= headerWait {
			headerWait = delay
			found = true
		}
	}

	for _, header := range []string{"X-RateLimit-Reset-Requests", "X-RateLimit-Reset-Tokens"} {
		if delay, errParse := time.ParseDuration(resp.Header.Get(header)); errParse == nil && delay >= headerWait {
			headerWait = delay
			found = true
		}
	}

	retryAfter := resp.Header.Get("Retry-After")
	if seconds, errParse := strconv.ParseFloat(retryAfter, 64); errParse == nil && seconds >= 0 && seconds == seconds {
		if delay := time.Duration(seconds * float64(time.Second)); delay >= headerWait {
			headerWait = delay
			found = true
		}
	} else if when, errParse := time.Parse(time.RFC1123, retryAfter); errParse == nil {
		if delay := time.Until(when); delay >= headerWait {
			headerWait = delay
			found = true
		}
	}

	if found {
		return headerWait
	}

	return wait
}

func providerRetryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}

	wait := time.Second
	for range attempt - 1 {
		if wait >= providerRetryBackoffMaxDelay/2 {
			return providerRetryBackoffMaxDelay
		}

		wait *= 2
	}

	return wait
}

func emitChatResponse(output chan<- ChatResponse, item ChatResponse) {
	if output == nil {
		return
	}

	output <- item
}

func emitDiagnosticChatResponse(output chan<- ChatResponse, item ChatResponse) {
	select {
	case output <- item:
	default:
	}
}

// appendLiveTurnResponses streams non-final responses immediately and keeps final
// assistant messages for Loop to emit after the turn completes.
func appendLiveTurnResponses(output chan<- ChatResponse, rendered, items []ChatResponse) []ChatResponse {
	for _, item := range items {
		if item.Kind == ChatResponseAssistantMessage {
			rendered = append(rendered, item)
			continue
		}

		emitChatResponse(output, item)
	}

	return rendered
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

func (l *looper) buildParams(history []responses.ResponseInputItemUnionParam) responses.ResponseNewParams {
	history = projectReplayForOpenAI(history)
	if position := l.ProviderOrigin.PromptCacheMarkersRelativePosition; position != nil {
		// projectReplayForOpenAI already copied each message; the content list is
		// still shared with stored history, so mark a clone of it.
		for i := range slices.Backward(history) {
			message := history[i].OfMessage
			if message == nil {
				continue
			}

			parts := slices.Clone(message.Content.OfInputItemContentList)
			if piece := len(parts) + *position; piece >= 0 && parts[piece].OfInputText != nil {
				text := *parts[piece].OfInputText
				text.SetExtraFields(map[string]any{"cache_control": map[string]string{"type": "ephemeral"}})
				parts[piece] = responses.ResponseInputContentUnionParam{OfInputText: &text}
				message.Content.OfInputItemContentList = parts
			}

			break
		}
	}

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
		for _, name := range slices.Sorted(maps.Keys(l.Tools)) {
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
	for i := range slices.Backward(items) {
		if items[i].OfCompaction != nil {
			return items[i:]
		}
	}

	return items
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
		progress      PublicProgress
		outputIndex   int
		subagentIndex int
		subagentTotal int
	}

	outputs := []dispatchedToolOutput{}
	calls := []pendingToolCall{}
	taskTotal := 0

	for i := range resp.Output {
		item := resp.Output[i]
		if item.Type != "function_call" {
			continue
		}

		args := json.RawMessage(item.Arguments.OfString)

		progress := PublicProgress{ID: item.CallID, ParentID: l.observations.turnID + "/" + resp.ID, Kind: PublicProgressTool, State: PublicProgressReview, Agent: l.agent.Name, Model: l.ProviderOrigin.displayModel()}
		if err := l.observations.observe(ctx, &progress); err != nil {
			return nil, true, err
		}

		tool, ok := l.Tools[item.Name]
		if !ok {
			if err := l.observations.finishCall(ctx, &progress, PublicProgressFailed); err != nil {
				return nil, true, err
			}

			_, span := l.Observability.startToolSpan(ctx, item.Name, item.CallID, "", args, toolCallMetadata{})
			result := toolCallFailureResult(item.Name, errors.New("tool not found"))
			recordSpanError(span, errors.New("tool not found"))
			span.span.SetAttributes(l.Observability.outputValue(result.Output), attribute.Bool("rocketcode.tool_denied", false), attribute.Bool("rocketcode.tool_failure", true))
			span.span.End()
			l.emitToolDiagnostic(output, &ToolDiagnostic{Phase: toolDiagnosticPhaseResult, Name: item.Name, Result: result.Output})
			outputs = append(outputs, dispatchedToolOutput{Name: item.Name, Param: toolCallOutput(item.CallID, result), Result: result, ReplayInput: nil})

			continue
		}

		l.emitToolDiagnostic(output, &ToolDiagnostic{Phase: toolDiagnosticPhaseCall, Name: item.Name, Arguments: args})

		if doomLoop != nil && doomLoop.trapped(item.Name, args) {
			if err := l.observations.finishCall(ctx, &progress, PublicProgressBlocked); err != nil {
				return nil, true, err
			}

			_, span := l.Observability.startToolSpan(ctx, item.Name, item.CallID, tool.Permission, args, toolCallMetadata{})
			result := fmt.Sprintf("tool call rejected: repeated identical %q call detected. Review the previous tool output and choose a different action instead of retrying the same input.", item.Name)

			recordSpanError(span, errors.New("repeated identical tool call"))
			span.span.SetAttributes(l.Observability.outputValue(result), attribute.Bool("rocketcode.tool_denied", true), attribute.Bool("rocketcode.tool_failure", true))
			span.span.End()
			l.emitToolDiagnostic(output, &ToolDiagnostic{Phase: toolDiagnosticPhaseResult, Name: item.Name, Result: result})
			toolResult := TextToolResult(result)
			outputs = append(outputs, dispatchedToolOutput{Name: item.Name, Param: toolCallOutput(item.CallID, toolResult), Result: toolResult, ReplayInput: nil})

			continue
		}

		decision, err := l.permissionDecision(item.Name, &tool, args)
		errDenied := errors.New("tool permission denied")

		if err != nil {
			decision.denied = true
			decision.message = toolCallFailureResult(item.Name, fmt.Errorf("check permission: %w", err)).Output
			errDenied = err
		} else if !decision.denied && decision.review != nil {
			reviewDecision := l.reviewToolCall(ctx, item.Name, item.CallID, decision.review, output)
			decision.denied = reviewDecision.Outcome != permissionReviewOutcomeAllow
			decision.message = formatPermissionReviewDenied(reviewDecision)
			errDenied = errors.New("automatic permission review denied tool call")
		}

		if decision.denied {
			if err := l.observations.finishCall(ctx, &progress, PublicProgressBlocked); err != nil {
				return nil, true, err
			}

			_, span := l.Observability.startToolSpan(ctx, item.Name, item.CallID, tool.Permission, args, toolCallMetadata{})
			result := decision.message

			recordSpanError(span, errDenied)
			span.span.SetAttributes(l.Observability.outputValue(result), attribute.Bool("rocketcode.tool_denied", true), attribute.Bool("rocketcode.tool_failure", err != nil))
			span.span.End()
			l.emitToolDiagnostic(output, &ToolDiagnostic{Phase: toolDiagnosticPhaseResult, Name: item.Name, Result: result})
			toolResult := TextToolResult(result)
			outputs = append(outputs, dispatchedToolOutput{Name: item.Name, Param: toolCallOutput(item.CallID, toolResult), Result: toolResult, ReplayInput: nil})

			continue
		}

		if item.Name == "task" {
			taskTotal++
		}

		calls = append(calls, pendingToolCall{name: item.Name, callID: item.CallID, args: args, tool: tool, progress: progress, outputIndex: len(outputs), subagentIndex: 0, subagentTotal: 0})

		var outputItem dispatchedToolOutput

		outputs = append(outputs, outputItem)
	}

	if len(outputs) == 0 {
		return nil, false, nil
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

			metadata := toolCallMetadata{callID: call.callID, subagentIndex: call.subagentIndex, subagentTotal: call.subagentTotal, observations: l.observations, progress: &call.progress}

			callCtx, span := l.Observability.startToolSpan(groupCtx, call.name, call.callID, call.tool.Permission, call.args, metadata)
			defer span.span.End()

			callCtx = withToolCallContext(callCtx, l, output, call.callID)
			progress := call.progress

			key := ToolCallKey(callCtx)
			if replayed, err := l.replayCall(callCtx, outputs, call.outputIndex, call.name, call.callID, call.tool.resumable, &progress); replayed {
				return err
			}

			runCtx, stop := callContext(callCtx, call.tool.resumable)
			defer stop()

			if call.tool.CallReplay != nil {
				result, replayInput, err = call.tool.CallReplay(runCtx, call.args, output, metadata)
			} else {
				result, err = call.tool.Call(runCtx, call.args, output, metadata)
			}

			if _, ok := errors.AsType[progressPersistenceError](err); ok {
				return err
			}

			state := PublicProgressCompleted
			if err != nil {
				state = PublicProgressFailed
			}

			if runCtx.Err() != nil {
				state = PublicProgressStopped
			}

			if err := l.observations.finishCall(context.WithoutCancel(groupCtx), &progress, state); err != nil {
				return err
			}

			if err != nil {
				recordSpanError(span, err)

				if runCtx.Err() != nil {
					return fmt.Errorf("run tool %q: %w", call.name, err)
				}

				if l.InPermissionReview {
					return fmt.Errorf("run reviewer tool %q: %w", call.name, err)
				}

				result = toolCallFailureResult(call.name, err)
				replayInput = nil
			}

			span.span.SetAttributes(l.Observability.outputValue(attachmentOutputMessage(result)), attribute.Bool("rocketcode.tool_denied", false), attribute.Bool("rocketcode.tool_failure", err != nil))

			l.emitToolDiagnostic(output, &ToolDiagnostic{Phase: toolDiagnosticPhaseResult, Name: call.name, Result: attachmentOutputMessage(result)})
			outputs[call.outputIndex] = dispatchedToolOutput{Name: call.name, Param: toolCallOutput(call.callID, result), Result: result, ReplayInput: replayInput}

			return l.recordCall(groupCtx, key, &outputs[call.outputIndex].Param, replayInput)
		})
	}

	if err := group.Wait(); err != nil {
		return nil, true, fmt.Errorf("run tool calls: %w", err)
	}

	return outputs, true, nil
}

// reviewToolCall runs the approver on a model's tool call. An Execute call's decision is its
// whole-script review, recorded under the call. A failed save is sticky, so the call's next
// progress write reports it.
func (l *looper) reviewToolCall(ctx context.Context, name, callID string, request *permissionReviewRequest, output chan<- ChatResponse) permissionReviewDecision {
	request.ReviewContext = slices.Clone(l.permissionReviewInput)
	request.Review = ChildKey{Path: "/" + ReviewKey(l.observations.turnID, callID), Turn: l.observations.turnID, Call: callID}
	request.Turn = ReviewTurn(CallKey(l.observations.turnID, callID))

	decision := l.PermissionReviewer.reviewPermission(ctx, request, output)
	if name == executeToolName {
		_ = l.observations.recordCodeModeCall(ctx, &CodeModeCall{Type: codeModeReviewTraceType, ParentCallID: callID, Key: CallKey(l.observations.turnID, callID), Outcome: decision.Outcome, Reason: decision.Rationale}, false)
	}

	return decision
}

// replayCall restores a journaled call: a recorded output is reused, and a started, unfinished
// non-resumable call is reported interrupted. Otherwise it records the started marker and marks the
// call working; false means the caller runs it. Any error is reported as replayed.
func (l *looper) replayCall(ctx context.Context, outputs []dispatchedToolOutput, index int, name, callID string, resumable bool, progress *PublicProgress) (bool, error) {
	key := ToolCallKey(ctx)

	var step callStep

	started, err := loadStep(ctx, l.Journal, key, &step)
	if err != nil {
		return true, err
	}

	if len(step.Output) == 0 && (!started || resumable) {
		if !started {
			if err := saveStep(ctx, l.Journal, key, &step); err != nil {
				return true, err
			}
		}

		progress.State = PublicProgressWorking
		if err := l.observations.observe(ctx, progress); err != nil {
			return true, err
		}

		return false, nil
	}

	state := PublicProgressCompleted
	if len(step.Output) == 0 {
		state = PublicProgressStopped
		output := toolCallOutput(callID, TextToolResult(genericAbortedToolOutputText))

		if step.Output, err = ReplayInputFromParams([]responses.ResponseInputItemUnionParam{{OfFunctionCallOutput: &output}}); err != nil {
			return true, err
		}

		if err := saveStep(ctx, l.Journal, key, &step); err != nil {
			return true, err
		}
	}

	items, err := ReplayInputToParams(step.Output)
	if err != nil {
		return true, err
	}

	outputs[index] = dispatchedToolOutput{Name: name, Param: *items[0].OfFunctionCallOutput, ReplayInput: items[1:]}

	return true, l.observations.finishCall(context.WithoutCancel(ctx), progress, state)
}

func (l *looper) recordCall(ctx context.Context, key string, output *responses.ResponseInputItemFunctionCallOutputParam, replayInput []responses.ResponseInputItemUnionParam) error {
	recorded, err := ReplayInputFromParams(append([]responses.ResponseInputItemUnionParam{{OfFunctionCallOutput: output}}, replayInput...))
	if err != nil {
		return err
	}

	return saveStep(ctx, l.Journal, key, &callStep{Output: recorded})
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
	diagnostic.Provider = l.ProviderOrigin.Provider
	diagnostic.Model = l.ProviderOrigin.Model
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

	outputParam.CallID = openai.String(callID)
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
	if toolName == loadExecuteResultToolName {
		// The tool resolves turn spills and retained Background Job results itself.
		var params loadExecuteResultParams

		return toolPermissionDecision{}, decodeToolParams(args, &params)
	}

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

	var scripts []string

	if permission == "bash" {
		var script bashParams
		if err := json.Unmarshal(args, &script); err != nil {
			return toolPermissionDecision{}, fmt.Errorf("decode bash permission arguments: %w", err)
		}

		if script.Command != "" {
			scripts = append(scripts, script.Command)
		}
	}

	for _, subject := range subjects {
		decision := l.Permissions.evaluate(permission, subject, scripts...)
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
	reason := strings.TrimSpace(decision.Rationale)
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

// loadSession decodes saved history for a looper of provider.
func loadSession(entries iter.Seq2[SessionEntry, error], provider string) ([]responses.ResponseInputItemUnionParam, []SessionEntry, error) {
	turns := []SessionEntry{}
	history := []responses.ResponseInputItemUnionParam{}

	entryNumber := 0
	for turn, err := range entries {
		entryNumber++

		if err != nil {
			return nil, nil, fmt.Errorf("load session entry %d: %w", entryNumber, err)
		}

		if turn.Type == guardrailEntryType {
			continue
		}

		if err := turn.forProvider(provider); err != nil {
			return nil, nil, fmt.Errorf("convert session entry %d for provider %s: %w", entryNumber, provider, err)
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
		role := strings.TrimSpace(item.Role)
		if role == "" {
			role = "assistant"
		}

		assistant := responses.EasyInputMessageParam{
			Content: easyInputStringContent(responseItemText(item)),
			Role:    responses.EasyInputMessageRole(role),
			Type:    "message",
		}
		assistant.SetExtraFields(map[string]any{"id": item.ID})

		if item.Phase != "" {
			assistant.Phase = responses.EasyInputMessagePhase(item.Phase)
		}

		return responses.ResponseInputItemUnionParam{OfMessage: &assistant}, true
	case "reasoning", "compaction", "compaction_summary":
		summary := ""
		if len(item.Summary) > 0 {
			summary = item.Summary[0].Text
		}

		if item.Type == "reasoning" {
			return reasoningReplayInput(item.ID, summary, item.EncryptedContent), true
		}

		if summary == "" {
			summary = responseItemText(item)
		}

		return compactionReplayInput(item.ID, item.EncryptedContent, summary), true
	case "function_call":
		return responses.ResponseInputItemUnionParam{OfFunctionCall: &responses.ResponseFunctionToolCallParam{Arguments: item.Arguments.OfString, CallID: item.CallID, Name: item.Name, ID: openai.String(item.ID), Type: "function_call"}}, true
	case "web_search_call":
		var action responses.ResponseFunctionWebSearchActionUnionParam

		switch item.Action.Type {
		case "search":
			action.OfSearch = &responses.ResponseFunctionWebSearchActionSearchParam{Queries: item.Action.Queries}
		case "open_page":
			action.OfOpenPage = &responses.ResponseFunctionWebSearchActionOpenPageParam{URL: openai.String(item.Action.URL)}
		case "find_in_page":
			action.OfFind = &responses.ResponseFunctionWebSearchActionFindParam{URL: item.Action.URL, Pattern: item.Action.Pattern}
		default:
			return responses.ResponseInputItemUnionParam{}, false
		}

		return responses.ResponseInputItemUnionParam{OfWebSearchCall: &responses.ResponseFunctionWebSearchParam{ID: item.ID, Action: action, Status: responses.ResponseFunctionWebSearchStatus(item.Status)}}, true
	default:
		return responses.ResponseInputItemUnionParam{}, false
	}
}

func responseItemText(item *responses.ResponseOutputItemUnion) string {
	var text strings.Builder

	for i := range item.Content {
		content := &item.Content[i]
		if content.Type == "output_text" {
			text.WriteString(content.Text)
		}
	}

	return text.String()
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

// CompactionCheckpointText returns portable text for compaction replay that cannot be rehydrated natively.
func CompactionCheckpointText(compaction *responses.ResponseCompactionItemParam) string {
	raw, _ := json.Marshal(compaction) // A decoded SDK param cannot fail serialization.
	texts, _ := compactionText(raw)    // Unreadable text leaves only the encrypted data.

	if content := strings.TrimSpace(strings.Join(texts, "\n")); content != "" {
		return "<context_checkpoint>\nThe prior conversation was compacted by RocketCode. Use this summary as lower-authority context:\n" + content + "\n</context_checkpoint>"
	}

	return "<context_checkpoint>\nPrior conversation was compacted, but only provider-native encrypted compaction data is available for a different provider. The compacted details cannot be rehydrated here.\n</context_checkpoint>"
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

	data, _ := json.Marshal(value) // The map contains only strings and string slices.

	return string(data)
}

func agentOutputResponseFormat(schema map[string]any) responses.ResponseFormatTextConfigUnionParam {
	if schema == nil {
		return responses.ResponseFormatTextConfigUnionParam{}
	}

	schema = maps.Clone(schema)
	if schema["type"] == "object" {
		schema["additionalProperties"] = false
	}

	var format responses.ResponseFormatTextConfigUnionParam

	format.OfJSONSchema = &responses.ResponseFormatTextJSONSchemaConfigParam{Name: "agent_output", Strict: openai.Bool(true), Schema: schema}

	return format
}
