package protocol

import (
	"encoding/json"
	"time"
)

// ListSessionsRequest bounds a durable-session list call.
type ListSessionsRequest struct {
	Since, Until time.Time
	Limit        int
	OmitPreview  bool
}

// SessionSummary is one durable conversation's list row.
type SessionSummary struct {
	ConversationID, LastUserMessage, LastAssistantMessage string
	Turns                                                 int
	LastUpdated                                           time.Time
}

// ListSessionsResult is the durable-session list outcome.
type ListSessionsResult struct {
	Sessions []SessionSummary
}

// ObserveSessionRequest identifies one durable conversation to snapshot.
type ObserveSessionRequest struct {
	ConversationID string
}

// ObserveSessionResult is a one-shot snapshot of stored entry JSON.
type ObserveSessionResult struct {
	Entries []json.RawMessage
}

// DeleteSessionRequest identifies one durable conversation whose turns to delete.
type DeleteSessionRequest struct {
	ConversationID string
}

// DeleteSessionResult is the number of stored turns removed.
type DeleteSessionResult struct {
	Deleted int64
}
