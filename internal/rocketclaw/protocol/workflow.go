package protocol

type WorkflowInvocation struct {
	Name, Args string
}

type WorkflowDescription struct{ Name, Description string }

type Terminal string

const (
	TerminalComplete Terminal = "complete"
	TerminalFailed   Terminal = "failed"
	TerminalStopped  Terminal = "stopped"
)

type PhaseStatus string

const (
	PhasePending    PhaseStatus = "pending"
	PhaseInProgress PhaseStatus = "in-progress"
	PhaseComplete   PhaseStatus = "complete"
	PhaseError      PhaseStatus = "error"
	PhaseSkipped    PhaseStatus = "skipped"
)

type PhaseUpdate struct {
	PhaseID   string      `json:"-"`
	Name      string      `json:"name"`
	Status    PhaseStatus `json:"status"`
	Scheduled int         `json:"scheduled"`
	Complete  int         `json:"complete"`
}
