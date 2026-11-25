package protocol

type RoutineRunResult struct {
	ConversationID string
	Reported       bool
}

type OneOffRoutine struct {
	Agent, Prompt, RelativePath, Space string
	ConversationID                     string
}
