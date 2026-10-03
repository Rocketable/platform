package protocol

// CronRunResult captures the observable result of one cronjob run.
type CronRunResult struct {
	ConversationID string
}

// OneOffCronjob captures a live one-off cronjob prompt loaded from disk.
type OneOffCronjob struct {
	Agent, Prompt, RelativePath, TextChannel string
	ConversationID                           string
}
