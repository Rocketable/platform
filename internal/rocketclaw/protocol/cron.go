package protocol

type CronRunResult struct {
	ConversationID string
	Reported       bool
}

type OneOffCronjob struct {
	Agent, Prompt, RelativePath, Space string
	ConversationID                     string
}
