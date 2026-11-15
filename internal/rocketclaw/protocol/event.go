package protocol

type Event struct {
	Message         *OutboundMessage
	Acknowledgement chan error
}
