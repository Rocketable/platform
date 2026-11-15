package protocol

import "time"

type SessionSummary struct {
	ConversationID, LastMessage string
	LastUpdated                 time.Time
}
