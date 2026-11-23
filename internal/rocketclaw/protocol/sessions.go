package protocol

import "time"

type SessionSummary struct {
	ConversationID, LastMessage string
	LastUpdated                 time.Time
}

type BranchDeletion struct {
	Messages, NestedBranches, SubagentSessions, QueuedMessages int
	Current                                                    bool
}
