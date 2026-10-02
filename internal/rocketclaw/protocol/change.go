package protocol

// ConversationChange signals committed transcript changes without carrying content.
type ConversationChange struct {
	ConversationID string `json:"conversationId"`
	Revision       string `json:"revision"`
}
