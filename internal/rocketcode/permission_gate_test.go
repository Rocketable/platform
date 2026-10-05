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

func TestCheckNestedPermissionAutoWithReviewerAllow(t *testing.T) {
	t.Parallel()

	var permissions PermissionSet
	require.NoError(t, permissions.Set("mcp", "demo.echo", PermissionAuto))

	looper := &looper{
		Journal:                InertJournal{},
		observations:           &turnObservations{journal: InertJournal{}},
		Permissions:            permissions,
		AutoApprovePermissions: true,
		PermissionReviewer: &mockPermissionReviewer{reviewPermissionFunc: func(context.Context, *permissionReviewRequest, chan<- ChatResponse) permissionReviewDecision {
			return permissionReviewDecision{Outcome: permissionReviewOutcomeAllow, Rationale: "ok"}
		}},
		agent: Agent{Name: "main"},
	}
	ctx := withToolCallContext(t.Context(), looper, nil, "")

	err := CheckNestedPermission(ctx, "execute", "mcp", "demo.echo", map[string]any{"message": "hi"})
	require.NoError(t, err)
}
