package events

import (
	"context"
	"fmt"
	"mime"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// MaxInboundTextAttachmentBytes is the per-file size limit for attachments converted to prompt text.
const MaxInboundTextAttachmentBytes = 256 << 10

const mainConversationID = "main"

// TerminalCLIClientIDMetadataKey identifies the attached terminal client for terminal-originated turns.
const TerminalCLIClientIDMetadataKey = "terminal_cli_client_id"

// TerminalCLIEmbeddedClientID marks direct in-process CLI input that has no server-owned control client.
const TerminalCLIEmbeddedClientID = "embedded"

const (
	// InboundOriginMetadataKey overrides the trusted prompt provenance origin.
	InboundOriginMetadataKey = "rocketclaw_origin"
	// InboundMediaMetadataKey overrides the trusted prompt provenance media.
	InboundMediaMetadataKey = "rocketclaw_media"
	// InboundPrincipalMetadataKey identifies the trusted human principal for prompt provenance.
	InboundPrincipalMetadataKey = "rocketclaw_principal"
	// InboundAllowedAgentsMetadataKey lists source-surface allowed agents for model-created child conversations.
	InboundAllowedAgentsMetadataKey = "rocketclaw_allowed_agents"
	// InboundStartNewThreadDisabledMetadataKey suppresses model-created child conversation tooling for this turn.
	InboundStartNewThreadDisabledMetadataKey = "rocketclaw_start_new_thread_disabled"
)

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
	SourceTerminalCLI  Source = "terminal_cli"
	SourceWebVoice     Source = "web_voice"
	SourceSystem       Source = "system"
)

// InboundResponse is the final plain-text result for a queued inbound turn.
type InboundResponse struct {
	Text        string
	Attachments []OutboundAttachment
	Err         error
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
	// OutputTargetTerminal delivers a response to the invoking terminal CLI.
	OutputTargetTerminal OutputTarget = "terminal"
)

// ObservedMessage is a non-consuming bus event tap record.
type ObservedMessage struct {
	Inbound  *InboundMessage
	Outbound *OutboundMessage
}

// InboundAttachment carries an inline attachment into the shared main-session prompt.
type InboundAttachment struct {
	Name, MIMEType string
	Data           []byte
}

// InboundContent carries source-acquired inbound text and attachments before message routing details are applied.
type InboundContent struct {
	Text                   string
	TextAttachments        []string
	Attachments            []InboundAttachment
	HadAttachments         bool
	HadNonImageAttachments bool
	AttachmentWarnings     []string
}

// OutboundAttachment carries a human-visible file attachment to output sinks.
type OutboundAttachment struct {
	Name, MIMEType string
	Data           []byte
}

// InboundMessage is a message headed into the shared main-session prompt queue.
type InboundMessage struct {
	Source                                                  Source
	Label, Text                                             string
	VerbatimMessage                                         string
	VerbatimAttachments                                     []OutboundAttachment
	Attachments                                             []InboundAttachment
	SlackReply                                              *SlackReplyTarget
	DiscordReply                                            *DiscordReplyTarget
	HadAttachments, HadNonImageAttachments, Human, GoalTurn bool
	AttachmentWarnings                                      []string
	Kind                                                    InboundKind
	ConversationID, WebSessionID                            string
	Metadata                                                map[string]string

	responseInit, responseOnce sync.Once
	responseCh                 chan InboundResponse
}

// SlackReplyTarget identifies the Slack message that owns a streamed reply.
type SlackReplyTarget struct{ ChannelID, MessageTS, ThreadTS string }

// DiscordReplyTarget identifies the Discord message or thread that owns a streamed reply.
type DiscordReplyTarget struct{ ChannelID, MessageID, ThreadID string }

// TextConversationTarget identifies a conversation/message in the configured primary text connector.
type TextConversationTarget struct{ ChannelID, MessageID, ThreadID string }

// AskUserQuestionOption is one native UI choice for ask_user_question.
type AskUserQuestionOption struct{ Label, Value, Description string }

// AskUserQuestionRequest asks the originating text connector human for input.
type AskUserQuestionRequest struct {
	Source                Source
	ID, Question, Details string
	ConversationID        string
	TerminalClientID      string
	Options               []AskUserQuestionOption
	Multiple              bool
	SlackReply            *SlackReplyTarget
	DiscordReply          *DiscordReplyTarget
}

// AskUserQuestionAnswer is returned to RocketCode after a human answers.
type AskUserQuestionAnswer struct {
	Selected []string `json:"selected"`
	Custom   string   `json:"custom"`
	Source   Source   `json:"source"`
}

// StartNewThreadRequest asks RocketClaw to create a new managed conversation from the current turn.
type StartNewThreadRequest struct {
	Source                                                   Source
	SourceConversationID, CurrentAgent, Agent, Title, Prompt string
	TerminalClientID                                         string
	AllowedAgents                                            []string
	SlackReply                                               *SlackReplyTarget
	DiscordReply                                             *DiscordReplyTarget
}

// StartNewThreadResult reports the created conversation and openable surface.
type StartNewThreadResult struct {
	ConversationID string `json:"conversation_id"`
	URL            string `json:"url,omitempty"`
	AttachCommand  string `json:"attach_command,omitempty"`
	CMUXOpened     bool   `json:"cmux_opened,omitempty"`
}

// StartNewThreadRootResult reports the native root surface created by a text connector.
type StartNewThreadRootResult struct {
	Target TextConversationTarget
	URL    string
}

// ResponseCheckpoint identifies a persisted main-session turn that can seed a Slack thread.
type ResponseCheckpoint struct {
	ConversationID, ResponseID, Model, AssistantText string
	SessionEntryID                                   int64
}

// OutboundMessage is a text message headed to enabled connectors.
type OutboundMessage struct {
	Text, ProgressText                   string
	Source                               Source
	Targets                              []OutputTarget
	ConversationID, TurnID, WebSessionID string
	Sequence                             int
	PostProgressText, Complete           bool
	SlackReply                           *SlackReplyTarget
	DiscordReply                         *DiscordReplyTarget
	Checkpoint                           *ResponseCheckpoint
	Attachments                          []OutboundAttachment
	GoalTurn, GoalComplete               bool
	GoalTurnNumber, GoalMaxTurns         int

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

// StartNewThreadRootText returns the human-visible root text for tool-created text conversations.
func StartNewThreadRootText(title, prompt string) string {
	return "New thread: " + strings.TrimSpace(title) + "\n\nStarted by RocketClaw from this conversation.\n\nTask:\n" + prompt
}

// StartNewThreadFirstPrompt returns the first model-visible task prompt body for tool-created conversations.
func StartNewThreadFirstPrompt(req *StartNewThreadRequest, targetAgent string) string {
	source := string(req.Source)
	if req.SourceConversationID != "" {
		source += " " + strings.TrimSpace(req.SourceConversationID)
	}

	return "A RocketClaw agent started this new thread from an existing conversation.\n\n" +
		"Title: " + strings.TrimSpace(req.Title) + "\n" +
		"Started from: " + strings.TrimSpace(source) + "\n" +
		"Source conversation ID: " + strings.TrimSpace(req.SourceConversationID) + "\n" +
		"Requesting agent: " + strings.TrimSpace(req.CurrentAgent) + "\n" +
		"Target agent: " + strings.TrimSpace(targetAgent) + "\n\n" +
		"Task:\n" + req.Prompt
}

// SetInboundAllowedAgents records surface-constrained agents on an inbound message.
func SetInboundAllowedAgents(inbound *InboundMessage, agents []string) {
	if inbound.Metadata == nil {
		inbound.Metadata = map[string]string{}
	}

	inbound.Metadata[InboundAllowedAgentsMetadataKey] = strings.Join(agents, ",")
}

// NewMainInboundMessageFromContent constructs a main inbound message from normalized source content.
func NewMainInboundMessageFromContent(source Source, kind InboundKind, label string, content *InboundContent, human bool) *InboundMessage {
	text := content.Text
	if len(content.TextAttachments) > 0 {
		attachmentText := strings.Join(content.TextAttachments, "\n\n")
		if strings.TrimSpace(text) == "" {
			text = attachmentText
		} else {
			text += "\n\n" + attachmentText
		}
	}

	inbound := NewMainInboundMessage(source, kind, label, text, human)
	if len(content.Attachments) > 0 {
		inbound.Attachments = make([]InboundAttachment, 0, len(content.Attachments))
		for i := range content.Attachments {
			inbound.Attachments = append(inbound.Attachments, InboundAttachment{
				Name:     content.Attachments[i].Name,
				MIMEType: content.Attachments[i].MIMEType,
				Data:     append([]byte(nil), content.Attachments[i].Data...),
			})
		}
	}

	inbound.HadAttachments = content.HadAttachments || len(content.Attachments) > 0
	inbound.HadNonImageAttachments = content.HadNonImageAttachments && len(content.TextAttachments) == 0
	inbound.AttachmentWarnings = append([]string(nil), content.AttachmentWarnings...)

	return inbound
}

// IsTextAttachment reports whether an attachment should be included as literal prompt text.
func IsTextAttachment(name, mimeType string) bool {
	mediaType := mimeType
	if parsed, _, err := mime.ParseMediaType(mediaType); err == nil {
		mediaType = parsed
	}

	mediaType = strings.ToLower(strings.TrimSpace(mediaType))

	return strings.HasPrefix(mediaType, "text/") || slices.Contains([]string{"application/json", "application/jsonl", "application/ld+json", "application/xml", "application/yaml", "application/x-yaml", "application/toml", "application/x-toml", "application/csv", "application/x-ndjson"}, mediaType) || slices.Contains([]string{".txt", ".md", ".markdown", ".csv", ".tsv", ".json", ".jsonl", ".ndjson", ".yaml", ".yml", ".toml", ".xml", ".ini", ".log"}, strings.ToLower(filepath.Ext(strings.TrimSpace(name))))
}

// EnableResponseWait returns a channel that receives the final result for this inbound turn.
func (m *InboundMessage) EnableResponseWait() <-chan InboundResponse { return m.responseChannel() }

// CompleteResponse marks this inbound turn result ready.
func (m *InboundMessage) CompleteResponse(text string, err error) {
	ch := m.responseChannel()
	m.responseOnce.Do(func() {
		ch <- InboundResponse{Text: text, Err: err}

		close(ch)
	})
}

// CompleteResponseWithAttachments marks this inbound turn result ready with response attachments.
func (m *InboundMessage) CompleteResponseWithAttachments(text string, attachments []OutboundAttachment, err error) {
	ch := m.responseChannel()
	m.responseOnce.Do(func() {
		ch <- InboundResponse{Text: text, Attachments: CloneOutboundAttachments(attachments), Err: err}

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
	SessionID, SpeakerID, Format string
	Source                       Source
	RTPSequence                  uint16
	Timestamp, SSRC              uint32
	SampleRate, Channels         int
	Data                         []byte
}
