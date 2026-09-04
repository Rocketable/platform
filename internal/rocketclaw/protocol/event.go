package protocol

// Event is live conversation output. Consumers acknowledge after final handling,
// including when the output does not apply to their surface.
type Event struct {
	Message         *OutboundMessage
	Acknowledgement chan error
}
