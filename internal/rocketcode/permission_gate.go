package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

type toolCallContextKey struct{}

type toolCallContext struct {
	looper *looper
	output chan<- ChatResponse
}

func withToolCallContext(ctx context.Context, l *looper, output chan<- ChatResponse) context.Context {
	return context.WithValue(ctx, toolCallContextKey{}, toolCallContext{looper: l, output: output})
}

func toolCallContextFrom(ctx context.Context) (toolCallContext, bool) {
	value := ctx.Value(toolCallContextKey{})
	tc, ok := value.(toolCallContext)

	return tc, ok && tc.looper != nil
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

	decision.review.ReviewContext = slices.Clone(tc.looper.permissionReviewInput)

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
