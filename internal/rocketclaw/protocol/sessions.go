package protocol

import "time"

// SessionSummary is one durable conversation's list row.
type SessionSummary struct {
	ConversationID, LastUserMessage string
	LastUpdated                     time.Time
}
