package protocol

import (
	"context"
	"mime"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const MaxInboundTextAttachmentBytes = 256 << 10

const (
	InboundRawTextMetadataKey        = "rocketclaw_raw_text"
	InboundOriginMetadataKey         = "rocketclaw_origin"
	InboundMediaMetadataKey          = "rocketclaw_media"
	InboundPrincipalMetadataKey      = "rocketclaw_principal"
	InboundPrincipalNameMetadataKey  = "rocketclaw_principal_name"
	InboundPrincipalEmailMetadataKey = "rocketclaw_principal_email"
	InboundAllowedAgentsMetadataKey  = "rocketclaw_allowed_agents"
	InboundSenderSessionMetadataKey  = "rocketclaw_sender_session"
	InboundSenderAgentMetadataKey    = "rocketclaw_sender_agent"
	InboundSenderActionMetadataKey   = "rocketclaw_sender_action"
	SenderActionStopped              = "stopped"
)

type InboundKind string

const (
	InboundKindPrompt  InboundKind = "prompt"
	InboundKindSteer   InboundKind = "steer"
	InboundKindEnqueue InboundKind = "enqueue"
	InboundKindHeld    InboundKind = "held"
	InboundKindCancel  InboundKind = "cancel"
)

type AttachmentPresence string

const (
	AttachmentPresenceNone        AttachmentPresence = ""
	AttachmentPresenceUnsupported AttachmentPresence = "unsupported"
	AttachmentPresenceImages      AttachmentPresence = "images"
)

type GoalAction string

const (
	GoalActionNone     GoalAction = ""
	GoalActionKickoff  GoalAction = "goal"
	GoalActionContinue GoalAction = "goal_continuation"
)

type Source string

const (
	SourceSlack       Source = "slack"
	SourceWeb         Source = "web"
	SourceExternalMCP Source = "external_mcp"
	SourceSystem      Source = "system"
)

type InboundResponse struct {
	Text        string
	Attachments []OutboundAttachment
	Err         error
}

type InboundAttachment struct {
	Name, MIMEType string
	Data           []byte
}

type InboundContent struct {
	Text               string
	TextAttachments    []string
	Attachments        []InboundAttachment
	AttachmentPresence AttachmentPresence
	AttachmentWarnings []string
}

type OutboundAttachment struct {
	ID                 string
	ConversationID     string `json:"conversation_id,omitempty"`
	Name, MIMEType     string
	Data               []byte `json:"-"`
	OriginalUnverified bool
	Size               int64
}

type CronjobMessage struct {
	RelativePath, Agent, RanAt string
	Space                      string
}

type InboundMessage struct {
	Source                Source
	Text                  string
	Attachments           []InboundAttachment
	SlackReply            *SlackReplyTarget
	AttachmentPresence    AttachmentPresence
	Human                 bool
	GoalTurn              bool
	AttachmentWarnings    []string
	Kind                  InboundKind
	GoalAction            GoalAction
	PreserveWhitespace    bool
	ConversationID        string
	Metadata              map[string]string
	ExternalMCPMetadata   map[string]string
	SyncDestination       string
	RequireOutputDecision bool
	Cronjob               *CronjobMessage
	OriginHeader          string
	SlackRoot             *InboundMessage `json:"-"`

	responseInit, responseOnce sync.Once
	responseCh                 chan InboundResponse
}

type SlackReplyTarget struct{ ChannelID, MessageTS, ThreadTS string }

type TextConversationTarget struct{ ChannelID, MessageID, ThreadID, Agent string }

type TurnProgress struct {
	Running, Asking bool
	Steps           []TurnStep
}

type TurnStep struct {
	ID, Title string
	State     TurnStepState
}

type TurnStepState string

const (
	TurnStepRunning TurnStepState = "running"
	TurnStepDone    TurnStepState = "done"
	TurnStepFailed  TurnStepState = "failed"
)

type AskUserQuestionOption struct{ Label, Value, Description string }

type AskUserQuestionRequest struct {
	ID, Question, Details string
	ConversationID        string
	Options               []AskUserQuestionOption
	Multiple              bool
}

type AskUserQuestionAnswer struct {
	Selected []string `json:"selected"`
	Custom   string   `json:"custom"`
	Source   Source   `json:"source"`
}

type UserQuestionAsker interface {
	AskUserQuestion(context.Context, *AskUserQuestionRequest) (AskUserQuestionAnswer, error)
}

type Terminal string

const (
	TerminalComplete Terminal = "complete"
	TerminalFailed   Terminal = "failed"
	TerminalStopped  Terminal = "stopped"
)

type OutboundMessage struct {
	ConsumedID, ConsumedText           string
	ConsumedHeader                     string
	ConsumedRawText                    string
	ConsumedSource                     Source
	Text                               string
	ConversationID, TurnID             string
	Agent                              string
	Model, SourceConversationID        string
	ReasoningEffort                    *string
	Cronjob                            *CronjobMessage
	Complete                           bool
	SlackReply                         *SlackReplyTarget
	Attachments                        []OutboundAttachment
	GoalTurn, GoalComplete, GoalActive bool
	GoalTurnNumber, GoalMaxTurns       int
	Terminal                           Terminal
	ChannelQuiet                       bool
}

func NewInboundMessage(source Source, kind InboundKind, text string, human bool) *InboundMessage {
	return &InboundMessage{
		Source: source, Text: text, Human: human, Kind: kind,
	}
}

func SetInboundAllowedAgents(inbound *InboundMessage, agents []string) {
	if inbound.Metadata == nil {
		inbound.Metadata = map[string]string{}
	}

	inbound.Metadata[InboundAllowedAgentsMetadataKey] = strings.Join(agents, ",")
}

func NewInboundMessageFromContent(source Source, kind InboundKind, content *InboundContent, human bool) *InboundMessage {
	normalizeInboundAttachments(content)

	text := content.Text
	if len(content.TextAttachments) > 0 {
		attachmentText := strings.Join(content.TextAttachments, "\n\n")
		if strings.TrimSpace(text) == "" {
			text = attachmentText
		} else {
			text += "\n\n" + attachmentText
		}
	}

	inbound := NewInboundMessage(source, kind, text, human)

	inbound.Metadata = map[string]string{InboundRawTextMetadataKey: content.Text}

	inbound.Attachments = slices.Clip(append([]InboundAttachment(nil), content.Attachments...))
	for i := range inbound.Attachments {
		inbound.Attachments[i].Data = append([]byte(nil), inbound.Attachments[i].Data...)
	}

	inbound.AttachmentPresence = content.AttachmentPresence
	if inbound.AttachmentPresence == AttachmentPresenceUnsupported && len(content.TextAttachments) > 0 {
		inbound.AttachmentPresence = AttachmentPresenceNone
	}

	inbound.AttachmentWarnings = append([]string(nil), content.AttachmentWarnings...)

	return inbound
}

func NormalizeMIMEType(mimeType string) string {
	if mediaType, _, err := mime.ParseMediaType(mimeType); err == nil {
		mimeType = mediaType
	}

	return strings.ToLower(strings.TrimSpace(mimeType))
}

func IsTextAttachment(name, normalizedMIME string) bool {
	return strings.HasPrefix(normalizedMIME, "text/") || slices.Contains([]string{"application/json", "application/jsonl", "application/ld+json", "application/xml", "application/yaml", "application/x-yaml", "application/toml", "application/x-toml", "application/csv", "application/x-ndjson"}, normalizedMIME) || slices.Contains([]string{".txt", ".md", ".markdown", ".csv", ".tsv", ".json", ".jsonl", ".ndjson", ".yaml", ".yml", ".toml", ".xml", ".ini", ".log"}, strings.ToLower(filepath.Ext(strings.TrimSpace(name))))
}

func (m *InboundMessage) EnableResponseWait() <-chan InboundResponse {
	m.responseInit.Do(func() {
		m.responseCh = make(chan InboundResponse, 1)
	})

	return m.responseCh
}

func (m *InboundMessage) CompleteResponseWithAttachments(text string, attachments []OutboundAttachment, err error) {
	m.EnableResponseWait()
	m.responseOnce.Do(func() {
		m.responseCh <- InboundResponse{Text: text, Attachments: CloneOutboundAttachments(attachments), Err: err}

		close(m.responseCh)
	})
}

func NewOutboundMessage(conversationID, text string) *OutboundMessage {
	return &OutboundMessage{Text: text, ConversationID: strings.TrimSpace(conversationID)}
}

func CloneOutboundAttachments(attachments []OutboundAttachment) []OutboundAttachment {
	cloned := slices.Clip(append([]OutboundAttachment(nil), attachments...))
	for i := range cloned {
		cloned[i].Data = append([]byte(nil), cloned[i].Data...)
	}

	return cloned
}
