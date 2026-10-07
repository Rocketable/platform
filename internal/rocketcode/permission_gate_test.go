package rocketcode

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckNestedPermissionOutsideToolCall(t *testing.T) {
	t.Parallel()

	err := CheckNestedPermission(t.Context(), "execute", "mcp", "demo.echo", map[string]any{"message": "hi"})
	require.ErrorContains(t, err, "outside a tool call")
}

func TestToolCallAgent(t *testing.T) {
	t.Parallel()
	_, ok := ToolCallAgent(t.Context())
	require.False(t, ok)

	agent := Agent{Name: "child", Frontmatter: map[string]any{"permissions": "retained"}}
	ctx := withToolCallContext(t.Context(), &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, agent: agent}, nil, "call")
	got, ok := ToolCallAgent(ctx)
	require.True(t, ok)
	require.Equal(t, agent, got)
}

func TestCheckNestedPermissionAllow(t *testing.T) {
	t.Parallel()

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("mcp", "demo.echo"))

	looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions}
	ctx := withToolCallContext(t.Context(), looper, nil, "")

	err := CheckNestedPermission(ctx, "execute", "mcp", "demo.echo", map[string]any{"message": "hi"})
	require.NoError(t, err)
}

func TestCheckNestedPermissionAutoWithoutAutoApproveDenies(t *testing.T) {
	t.Parallel()

	var permissions PermissionSet
	require.NoError(t, permissions.Set("mcp", "demo.echo", PermissionAuto))

	looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions, AutoApprovePermissions: false}
	ctx := withToolCallContext(t.Context(), looper, nil, "")

	err := CheckNestedPermission(ctx, "execute", "mcp", "demo.echo", map[string]any{"message": "hi"})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "automatic approval") || strings.Contains(err.Error(), "denied"), "error = %v", err)
}

func TestCheckNestedPermissionDeny(t *testing.T) {
	t.Parallel()

	var permissions PermissionSet
	require.NoError(t, permissions.Deny("mcp", "demo.danger"))

	looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions}
	ctx := withToolCallContext(t.Context(), looper, nil, "")

	err := CheckNestedPermission(ctx, "execute", "mcp", "demo.danger", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "denied")
}

// A review during a call joins the call's review history: unique to its turn when a provider
// reuses the call ID, and the same when the turn resumes.
func TestCheckNestedPermissionAutoWithReviewerAllow(t *testing.T) {
	t.Parallel()

	var permissions PermissionSet
	require.NoError(t, permissions.Set("mcp", "demo.echo", PermissionAuto))

	reviewer := &mockPermissionReviewer{reviewPermissionFunc: func(context.Context, *permissionReviewRequest, chan<- ChatResponse) permissionReviewDecision {
		return permissionReviewDecision{Outcome: permissionReviewOutcomeAllow, Rationale: "ok"}
	}}

	for _, turnID := range []string{"turn-1", "turn-1", "turn-2"} {
		looper := &looper{
			Journal:                InertJournal{},
			observations:           &turnObservations{journal: InertJournal{}, turnID: turnID},
			Permissions:            permissions,
			AutoApprovePermissions: true,
			PermissionReviewer:     reviewer,
			agent:                  Agent{Name: "main"},
		}
		ctx := withToolCallContext(t.Context(), looper, nil, "call_0")

		err := CheckNestedPermission(ctx, "execute", "mcp", "demo.echo", map[string]any{"message": "hi"})
		require.NoError(t, err)
	}

	requests := reviewedRequests(reviewer)
	require.Equal(t, []string{reviewKey("turn-1", "call_0"), reviewKey("turn-1", "call_0"), reviewKey("turn-2", "call_0")}, []string{requests[0].Review, requests[1].Review, requests[2].Review})
}
