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

func TestCheckNestedPermissionAllow(t *testing.T) {
	t.Parallel()

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("mcp", "demo.echo"))

	looper := &looper{Permissions: permissions}
	ctx := withToolCallContext(t.Context(), looper, nil)

	err := CheckNestedPermission(ctx, "execute", "mcp", "demo.echo", map[string]any{"message": "hi"})
	require.NoError(t, err)
}

func TestCheckNestedPermissionAutoWithoutAutoApproveDenies(t *testing.T) {
	t.Parallel()

	var permissions PermissionSet
	require.NoError(t, permissions.Set("mcp", "demo.echo", PermissionAuto))

	looper := &looper{Permissions: permissions, AutoApprovePermissions: false}
	ctx := withToolCallContext(t.Context(), looper, nil)

	err := CheckNestedPermission(ctx, "execute", "mcp", "demo.echo", map[string]any{"message": "hi"})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "automatic approval") || strings.Contains(err.Error(), "denied"), "error = %v", err)
}

func TestCheckNestedPermissionDeny(t *testing.T) {
	t.Parallel()

	var permissions PermissionSet
	require.NoError(t, permissions.Deny("mcp", "demo.danger"))

	looper := &looper{Permissions: permissions}
	ctx := withToolCallContext(t.Context(), looper, nil)

	err := CheckNestedPermission(ctx, "execute", "mcp", "demo.danger", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "denied")
}

func TestCheckNestedPermissionAutoWithReviewerAllow(t *testing.T) {
	t.Parallel()

	var permissions PermissionSet
	require.NoError(t, permissions.Set("mcp", "demo.echo", PermissionAuto))

	looper := &looper{
		Permissions:            permissions,
		AutoApprovePermissions: true,
		PermissionReviewer: permissionReviewerFunc(func() permissionReviewDecision {
			return permissionReviewDecision{Outcome: permissionReviewOutcomeAllow, Rationale: "ok"}
		}),
		agent: Agent{Name: "main"},
	}
	ctx := withToolCallContext(t.Context(), looper, nil)

	err := CheckNestedPermission(ctx, "execute", "mcp", "demo.echo", map[string]any{"message": "hi"})
	require.NoError(t, err)
}

type permissionReviewerFunc func() permissionReviewDecision

func (f permissionReviewerFunc) reviewPermission(context.Context, *permissionReviewRequest, chan<- ChatResponse) permissionReviewDecision {
	return f()
}
