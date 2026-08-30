package protocol

// OverlayFile is one overlay file on the development-flow protocol.
type OverlayFile struct {
	Path, Content string
}

// OverlayContext is a named base overlay plus request-carried file deltas.
type OverlayContext struct {
	BaseOverlay string
	Files       []OverlayFile
}

// OverlaySpec is one configured overlay name.
type OverlaySpec struct {
	Spec string
}

// LintFinding is one overlay lint finding.
type LintFinding struct {
	Code, Severity, Path, Message string
}

// LintRequest is one overlay lint call.
type LintRequest struct {
	Context OverlayContext
}

// LintResult is the overlay lint outcome.
type LintResult struct {
	Findings []LintFinding
}

// TryTurnRequest is one Development MCP try-turn.
type TryTurnRequest struct {
	Context        OverlayContext
	Agent, Prompt  string
	ConversationID string
}

// TryTurnResult is one Development MCP try-turn outcome.
type TryTurnResult struct {
	ConversationID, Thinking, Answer string
}

// ReloadRequest is one Reload protocol message.
type ReloadRequest struct {
	Reason string
}

// RestartRequest is one Restart protocol message.
type RestartRequest struct {
	Reason string
}
