package protocol

// OverlayFile is one overlay file on the development-flow protocol.
type OverlayFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// OverlayContext is a named base overlay plus request-carried file deltas.
type OverlayContext struct {
	BaseOverlay string        `json:"base_overlay,omitempty"`
	Files       []OverlayFile `json:"files"`
}

// LintFinding is one overlay lint finding.
type LintFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

// LintRequest is one overlay lint call.
type LintRequest struct {
	Context OverlayContext `json:"context"`
}

// LintResult is the overlay lint outcome.
type LintResult struct {
	Findings []LintFinding
}

// TryTurnRequest is one Development MCP try-turn.
type TryTurnRequest struct {
	Context        OverlayContext `json:"context"`
	Agent          string         `json:"agent"`
	Prompt         string         `json:"prompt"`
	ConversationID string         `json:"conversation_id"`
}

// TryTurnResult is one Development MCP try-turn outcome.
type TryTurnResult struct {
	ConversationID string `json:"conversation_id"`
	Thinking       string `json:"thinking"`
	Answer         string `json:"answer"`
}
