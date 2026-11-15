package protocol

type ConversationChange struct {
	ConversationID string `json:"conversationId"`
	Revision       string `json:"revision"`
}
