package protocol

type CronRunResult struct {
	ConversationID string
}

type OneOffCronjob struct {
	Agent, Prompt, RelativePath, Space string
	ConversationID                     string
}
