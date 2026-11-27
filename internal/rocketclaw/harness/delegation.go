package harness

import "context"

type DelegationOp string

const (
	DelegationStart DelegationOp = "start"
	DelegationWrite DelegationOp = "write"
	DelegationRead  DelegationOp = "read"
)

func (a Agents) delegates(caller *Agent, op DelegationOp, target, owner string) bool {
	top, ok := a.Items[owner]
	denied := caller.Permission.evaluate("task", target).Action == permissionDeny

	if caller.Permission.Sudo {
		return (!denied || target == caller.Name) && (!ok || !top.Permission.Sudo || owner == caller.Name || op == DelegationRead)
	}

	_, loaded := a.Items[target]

	return ok && !top.Permission.Sudo && loaded && !denied
}

func ToolCallDelegates(ctx context.Context, op DelegationOp, target, owner string) bool {
	tc, _ := toolCallContextFrom(ctx)

	return !ToolCallPrivate(ctx, owner) && tc.looper.agents.delegates(&tc.looper.agent, op, target, owner)
}

func ToolCallPrivate(ctx context.Context, owner string) bool {
	tc, _ := toolCallContextFrom(ctx)

	return owner != tc.looper.agent.Name && tc.looper.agents.Items[owner].Identity != ""
}
