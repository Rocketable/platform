package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
	"github.com/openai/openai-go/v3/responses"
)

type toolCallContextKey struct{}

type toolCallContext struct {
	looper            *looper
	observations      *turnObservations
	reviewInput       []responses.ResponseInputItemUnionParam
	currentTurnInputs []PromptInput
	output            chan<- ChatResponse
	callID            string
	sink              *backgroundSink
}

func toolCallContextFrom(ctx context.Context) (toolCallContext, bool) {
	value := ctx.Value(toolCallContextKey{})
	tc, ok := value.(toolCallContext)

	return tc, ok && tc.looper != nil
}

func CallKey(turnID, callID string) string {
	return turnID + "/call/" + callID
}

func ToolCallKey(ctx context.Context) string {
	tc, ok := toolCallContextFrom(ctx)
	if !ok {
		return ""
	}

	key := CallKey(tc.observations.turnID, tc.callID)
	if host := codemode.CallKey(ctx); host != "" {
		key += "/host/" + host
	}

	return key
}

func ToolCallAgent(ctx context.Context) (Agent, bool) {
	tc, ok := toolCallContextFrom(ctx)
	if !ok {
		return Agent{}, false
	}

	return tc.looper.agent, true
}

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
		return permissionDeniedError(decision.message)
	}

	var bash bashParams
	if decision.review != nil && decision.review.Permission == "bash" {
		_ = json.Unmarshal(args, &bash)
	}

	switch {
	case decision.review == nil, tc.looper.Permissions.Easy && bash.Stdin.text == "":
		return nil
	case len(bash.Stdin.text) > 64<<10:
		return permissionDeniedError("stdin too large to review (max 64 KiB); write it to a file first")
	}

	return reviewNestedCall(ctx, &tc, decision.review, ReviewTurn(ToolCallKey(ctx)))
}

func reviewNestedCall(ctx context.Context, tc *toolCallContext, review *permissionReviewRequest, turn string) error {
	review.ReviewContext = slices.Clone(tc.reviewInput)
	review.CurrentTurnInputs = slices.Clone(tc.currentTurnInputs)
	review.Review = ChildKey{Path: "/" + ReviewKey(tc.observations.turnID, tc.callID), Turn: tc.observations.turnID, Call: tc.callID}
	review.Turn = turn

	reviewDecision := tc.looper.PermissionReviewer.reviewPermission(ctx, review)
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("permission review: %w", err)
	}

	tc.observations.mu.Lock()
	if tc.observations.reviews == nil {
		tc.observations.reviews = map[string]permissionReviewDecision{}
	}

	tc.observations.reviews[ToolCallKey(ctx)] = reviewDecision
	tc.observations.mu.Unlock()

	if reviewDecision.Outcome != permissionReviewOutcomeAllow {
		return permissionDeniedError(formatPermissionReviewDenied(reviewDecision))
	}

	return nil
}

type permissionDeniedError string

func (e permissionDeniedError) Error() string {
	return string(e)
}

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
