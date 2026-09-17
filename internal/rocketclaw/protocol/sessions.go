package protocol

import "time"

// SessionSummary is one durable conversation's list row.
type SessionSummary struct {
	ConversationID, LastMessage string
	LastUpdated                 time.Time
}
