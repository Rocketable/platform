package protocol

// WorkflowInvocation is one inbound workflow start without a compiled program.
type WorkflowInvocation struct {
	Name, Args string
}

// WorkflowDescription identifies one available workflow.
type WorkflowDescription struct{ Name, Description string }

// Terminal identifies how a workflow run ended.
type Terminal string

const (
	// TerminalComplete reports a successful workflow.
	TerminalComplete Terminal = "complete"
	// TerminalFailed reports a workflow infrastructure failure.
	TerminalFailed Terminal = "failed"
	// TerminalStopped reports a human interruption.
	TerminalStopped Terminal = "stopped"
)

// PhaseStatus identifies one workflow phase state.
type PhaseStatus string

const (
	// PhasePending has not started.
	PhasePending PhaseStatus = "pending"
	// PhaseInProgress is executing.
	PhaseInProgress PhaseStatus = "in-progress"
	// PhaseComplete finished successfully.
	PhaseComplete PhaseStatus = "complete"
	// PhaseError failed.
	PhaseError PhaseStatus = "error"
	// PhaseSkipped was not entered before the workflow terminated.
	PhaseSkipped PhaseStatus = "skipped"
)

// PhaseUpdate reports connector-neutral workflow progress.
type PhaseUpdate struct {
	PhaseID, Name                string
	Status                       PhaseStatus
	Scheduled, Running, Complete int
}

// AgentUpdate reports one workflow agent call's latest observable activity.
type AgentUpdate struct {
	PhaseID, Label, Activity string
}
