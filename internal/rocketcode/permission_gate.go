package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/Rocketable/platform/internal/rocketcode/codemode"
	"github.com/openai/openai-go/v3/responses"
)

type toolCallContextKey struct{}

// toolCallContext snapshots the turn state a call needs, because a Background Job keeps
// running after the looper moves on to later tool batches and turns.
type toolCallContext struct {
	looper       *looper
	observations *turnObservations
	reviewInput  []responses.ResponseInputItemUnionParam
	output       chan<- ChatResponse
	callID       string
	sink         *backgroundSink // The call's own Background Job, if it runs as one.
}

func withToolCallContext(ctx context.Context, l *looper, output chan<- ChatResponse, callID string) context.Context {
	return context.WithValue(ctx, toolCallContextKey{}, toolCallContext{looper: l, observations: l.observations, reviewInput: l.permissionReviewInput, output: output, callID: callID})
}

func toolCallContextFrom(ctx context.Context) (toolCallContext, bool) {
	value := ctx.Value(toolCallContextKey{})
	tc, ok := value.(toolCallContext)

	return tc, ok && tc.looper != nil
}

// ToolCallKey returns the current tool call's journal key, stable across restarts; empty outside a call.
// Inside a Code Mode script each host call has its own key under the script's call.
func ToolCallKey(ctx context.Context) string {
	tc, ok := toolCallContextFrom(ctx)
	if !ok {
		return ""
	}

	key := tc.observations.turnID + "/call/" + tc.callID
	if host := codemode.CallKey(ctx); host != "" {
		key += "/host/" + host
	}

	return key
}

// ToolCallAgent returns the active caller's definition inside a tool call.
// Its maps and slices are read-only and must not be mutated by consumers.
func ToolCallAgent(ctx context.Context) (Agent, bool) {
	tc, ok := toolCallContextFrom(ctx)
	if !ok {
		return Agent{}, false
	}

	return tc.looper.agent, true
}

// CheckNestedToolCall runs the same allow/deny/auto(+reviewer) gate as a top-level tool call,
// including multi-subject tools (e.g. apply_patch). Must run inside a looper tool Call context.
func CheckNestedToolCall(ctx context.Context, toolName string, tool *looperTool, args json.RawMessage) error {
	tc, ok := toolCallContextFrom(ctx)
	if !ok {
		return errors.New("nested permission check unavailable outside a tool call")
	}

	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}

	decision, err := tc.looper.permissionDecision(toolName, tool, args)
	if err != nil {
		return fmt.Errorf("check permission: %w", err)
	}

	if decision.denied {
		return errors.New(decision.message)
	}

	if decision.review == nil {
		return nil
	}

	decision.review.ReviewContext = slices.Clone(tc.reviewInput)
	decision.review.Review = ReviewKey(tc.observations.turnID, tc.callID)

	reviewDecision := tc.looper.PermissionReviewer.reviewPermission(ctx, decision.review, tc.output)
	if reviewDecision.Outcome != permissionReviewOutcomeAllow {
		return errors.New(formatPermissionReviewDenied(reviewDecision))
	}

	return nil
}

// CheckNestedPermission is a single-subject helper used by MCP builtins.
func CheckNestedPermission(ctx context.Context, toolName, permission, subject string, args map[string]any) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("marshal nested permission args: %w", err)
	}

	tool := looperTool{
		Permission: permission,
		Subjects: func(json.RawMessage) ([]string, error) {
			return []string{subject}, nil
		},
	}

	return CheckNestedToolCall(ctx, toolName, &tool, raw)
}
