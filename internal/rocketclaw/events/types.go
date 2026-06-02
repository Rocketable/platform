package events

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

const mainConversationID = "main"

// InboundKind describes how an inbound message should be handled.
type InboundKind string

const (
	// InboundKindPrompt is a normal conversational prompt.
	InboundKindPrompt InboundKind = "prompt"
	// InboundKindInternalize is a note the session should absorb without replying.
	InboundKindInternalize InboundKind = "internalize"
)

// Source identifies where an inbound or outbound message originated.
type Source string

// Known inbound and outbound message source labels.
const (
	SourceSlack        Source = "slack"
	SourceDiscordText  Source = "discord_text"
	SourceDiscordVoice Source = "discord_voice"
	SourceExternalMCP  Source = "external_mcp"
	SourceWebVoice     Source = "web_voice"
	SourceSystem       Source = "system"
)

// InboundResponse is the final plain-text result for a queued inbound turn.
type InboundResponse struct {
	Text string
	Err  error
}

// OutputTarget identifies which connector should receive an outbound message.
type OutputTarget string

const (
	// OutputTargetSlackMain delivers a response to the main Slack DM.
	OutputTargetSlackMain OutputTarget = "slack_main"
	// OutputTargetDiscordText delivers a response to Discord text.
	OutputTargetDiscordText OutputTarget = "discord_text"
	// OutputTargetDiscord delivers a response to Discord voice.
	OutputTargetDiscord OutputTarget = "discord"
	// OutputTargetWebUI delivers a response to the browser voice-mode client.
	OutputTargetWebUI OutputTarget = "web_ui"
)

// InboundAttachment carries an inline attachment into the shared main-session prompt.
type InboundAttachment struct {
	Name     string
	MIMEType string
	Data     []byte
}

// OutboundAttachment carries a human-visible file attachment to output sinks.
type OutboundAttachment struct {
	Name     string
	MIMEType string
	Data     []byte
}

// InboundMessage is a message headed into the shared main-session prompt queue.
type InboundMessage struct {
	Source                       Source
	Label, Text                  string
	VerbatimMessage              string
	VerbatimAttachments          []OutboundAttachment
	Attachments                  []InboundAttachment
	SlackReply                   *SlackReplyTarget
	DiscordReply                 *DiscordReplyTarget
	HadAttachments               bool
	HadNonImageAttachments       bool
	AttachmentWarnings           []string
	Human                        bool
	Kind                         InboundKind
	ConversationID, WebSessionID string
	Metadata                     map[string]string

	responseInit, responseOnce sync.Once
	responseCh                 chan InboundResponse
}

// SlackReplyTarget identifies the Slack message that owns a streamed reply.
type SlackReplyTarget struct {
	ChannelID, MessageTS, ThreadTS string
}

// DiscordReplyTarget identifies the Discord message or thread that owns a streamed reply.
type DiscordReplyTarget struct {
	ChannelID, MessageID, ThreadID string
}

// ResponseCheckpoint identifies a persisted main-session turn that can seed a Slack thread.
type ResponseCheckpoint struct {
	ConversationID string
	SessionEntryID int64
	ResponseID     string
	Model          string
	AssistantText  string
}

// OutboundMessage is a text message headed to enabled connectors.
type OutboundMessage struct {
	Text, SlackThinking                  string
	SlackPostText                        bool
	Source                               Source
	Targets                              []OutputTarget
	ConversationID, TurnID, WebSessionID string
	Sequence                             int
	Complete                             bool
	SlackReply                           *SlackReplyTarget
	DiscordReply                         *DiscordReplyTarget
	Checkpoint                           *ResponseCheckpoint
	Attachments                          []OutboundAttachment

	deliveryInit, deliveredOnce sync.Once
	delivered                   chan struct{}
	deliveryErr                 error
	deliveryNotify              func(error)
}

// MainConversationID returns the stable key for the shared main session.
func MainConversationID() string { return mainConversationID }

// MainOutputTargets returns the default targets for main-session replies.
func MainOutputTargets() []OutputTarget {
	return []OutputTarget{OutputTargetSlackMain, OutputTargetDiscord}
}

// NewMainInboundMessage constructs a message for the shared main session.
func NewMainInboundMessage(source Source, kind InboundKind, label, text string, human bool) *InboundMessage {
	return &InboundMessage{
		Source: source, Label: label, Text: text, Human: human, Kind: kind,
		ConversationID: MainConversationID(),
	}
}

// EnableResponseWait returns a channel that receives the final result for this inbound turn.
func (m *InboundMessage) EnableResponseWait() <-chan InboundResponse {
	return m.responseChannel()
}

// CompleteResponse marks this inbound turn result ready.
func (m *InboundMessage) CompleteResponse(text string, err error) {
	ch := m.responseChannel()
	m.responseOnce.Do(func() {
		ch <- InboundResponse{Text: text, Err: err}

		close(ch)
	})
}

// NewMainOutboundMessage constructs an outbound message for the shared main session.
func NewMainOutboundMessage(source Source, text string, targets ...OutputTarget) *OutboundMessage {
	message := OutboundMessage{
		Text: text, Source: source, Targets: MainOutputTargets(), ConversationID: MainConversationID(),
	}
	if len(targets) > 0 {
		message.Targets = append([]OutputTarget(nil), targets...)
	}

	return &message
}

// CloneOutboundAttachments returns a deep copy of attachments.
func CloneOutboundAttachments(attachments []OutboundAttachment) []OutboundAttachment {
	if len(attachments) == 0 {
		return nil
	}

	cloned := make([]OutboundAttachment, 0, len(attachments))
	for i := range attachments {
		attachment := attachments[i]
		attachment.Data = append([]byte(nil), attachment.Data...)
		cloned = append(cloned, attachment)
	}

	return cloned
}

// AttachmentNamesSpeech returns a short spoken description of attachment names.
func AttachmentNamesSpeech(attachments []OutboundAttachment) string {
	names := make([]string, 0, len(attachments))
	for i := range attachments {
		if name := strings.TrimSpace(attachments[i].Name); name != "" {
			names = append(names, name)
		}
	}

	if len(names) == 0 {
		return ""
	}

	return "Attached files: " + strings.Join(names, ", ") + "."
}

// WaitDelivered waits until outbound delivery for this message finishes.
func (m *OutboundMessage) WaitDelivered(ctx context.Context) error {
	ch := m.deliveryChannel()
	select {
	case <-ch:
		return m.deliveryErr
	case <-ctx.Done():
		return fmt.Errorf("wait for outbound delivery: %w", ctx.Err())
	}
}

// MarkDelivered marks outbound delivery for this message complete.
func (m *OutboundMessage) MarkDelivered(err error) {
	ch := m.deliveryChannel()
	m.deliveredOnce.Do(func() {
		m.deliveryErr = err
		if m.deliveryNotify != nil {
			m.deliveryNotify(err)
		}

		close(ch)
	})
}

func (m *OutboundMessage) deliveryChannel() chan struct{} {
	m.deliveryInit.Do(func() {
		m.delivered = make(chan struct{})
	})

	return m.delivered
}

func (m *InboundMessage) responseChannel() chan InboundResponse {
	m.responseInit.Do(func() {
		m.responseCh = make(chan InboundResponse, 1)
	})

	return m.responseCh
}

// AudioChunk carries a connector audio frame into the transcription pipeline.
type AudioChunk struct {
	SessionID, SpeakerID string
	Source               Source
	RTPSequence          uint16
	Timestamp, SSRC      uint32
	SampleRate, Channels int
	Format               string
	Data                 []byte
}
