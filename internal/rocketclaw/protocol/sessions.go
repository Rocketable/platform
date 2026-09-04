package protocol

import "time"

// SessionSummary is one durable conversation's list row.
type SessionSummary struct {
	ConversationID, LastUserMessage, LastAssistantMessage string
	Turns                                                 int
	LastUpdated                                           time.Time
}
