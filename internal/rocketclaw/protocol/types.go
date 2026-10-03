package protocol

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// MaxInboundTextAttachmentBytes is the per-file size limit for attachments converted to prompt text.
const MaxInboundTextAttachmentBytes = 256 << 10

const (
	// InboundRawTextMetadataKey retains typed text before attachment text is merged.
	InboundRawTextMetadataKey = "rocketclaw_raw_text"
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
	// InboundKindSteer joins an open human turn or runs next after input closes.
	InboundKindSteer InboundKind = "steer"
	// InboundKindEnqueue waits for its own turn.
	InboundKindEnqueue InboundKind = "enqueue"
	// InboundKindHeld waits for manual release into the queue.
	InboundKindHeld InboundKind = "held"
	// InboundKindCancel interrupts the active conversation owner.
	InboundKindCancel InboundKind = "cancel"
)

// AttachmentPresence records which attachments the source message carried, whether or not they reached the model.
type AttachmentPresence string

// Attachment presences; images win when a message carries both images and unsupported files.
const (
	// AttachmentPresenceNone means the source message carried no attachments the model could miss.
	AttachmentPresenceNone AttachmentPresence = ""
	// AttachmentPresenceUnsupported means the source message carried only files the model cannot read as images or text.
	AttachmentPresenceUnsupported AttachmentPresence = "unsupported"
	// AttachmentPresenceImages means the source message carried images, even when none survived download.
	AttachmentPresenceImages AttachmentPresence = "images"
)

// GoalAction identifies goal-loop work that consumes the goal's turn budget.
// Its zero value denotes ordinary work, including human re-steering.
type GoalAction string

// Goal actions distinguish budget-neutral input from kickoff and continuation work.
const (
	GoalActionNone     GoalAction = ""
	GoalActionKickoff  GoalAction = "goal"
	GoalActionContinue GoalAction = "goal_continuation"
)

// Source identifies where an inbound or outbound message originated.
type Source string

// Known inbound and outbound message source labels.
const (
	SourceSlack       Source = "slack"
	SourceWeb         Source = "web"
	SourceExternalMCP Source = "external_mcp"
	SourceSystem      Source = "system"
)

// InboundResponse is the final plain-text result for a queued inbound turn.
type InboundResponse struct {
	Text        string
	Attachments []OutboundAttachment
	Err         error
}

// InboundAttachment carries an inline attachment into a conversation prompt.
type InboundAttachment struct {
	Name, MIMEType string
	Data           []byte
}

// InboundContent carries source-acquired inbound text and attachments before message routing details are applied.
type InboundContent struct {
	Text               string
	TextAttachments    []string
	Attachments        []InboundAttachment
	AttachmentPresence AttachmentPresence
	AttachmentWarnings []string
}

// OutboundAttachment carries a human-visible file attachment to output sinks.
type OutboundAttachment struct {
	ID                 string
	Name, MIMEType     string
	Data               []byte
	OriginalUnverified bool
	Size               int64 // Stored byte length for metadata-only reads.
}

// CronjobMessage identifies a human-visible cronjob result.
type CronjobMessage struct {
	RelativePath, Agent, RanAt string
}

// ExternalMCPRelay carries one MCP request to its Slack surface.
type ExternalMCPRelay struct {
	ConversationID, ExternalConversationID, Agent, Text string
	Attachments                                         []OutboundAttachment
}

// InboundMessage is a message headed into its conversation prompt queue.
type InboundMessage struct {
	// Source is the surface that produced the message.
	Source Source
	// Text is the prompt body, with any text attachments appended after the typed text.
	Text string
	// Attachments are inline files, such as images, sent to the model alongside Text.
	Attachments []InboundAttachment
	// SlackReply is the Slack message that receives the reply; nil when the reply has no Slack target.
	SlackReply *SlackReplyTarget
	// AttachmentPresence records which attachments the source message carried, so the model can be told about ones it did not receive.
	AttachmentPresence AttachmentPresence
	// Human reports that a person wrote the message, rather than RocketClaw itself.
	Human bool
	// GoalTurn marks a turn that runs while a goal is active, including human input that does not spend the goal's turn budget.
	GoalTurn bool
	// AttachmentWarnings explain to the model, in plain text, why attachments were skipped.
	AttachmentWarnings []string
	// Kind says how the message enters the conversation's input queue.
	Kind InboundKind
	// GoalAction marks goal kickoff or continuation work that spends the goal's turn budget.
	GoalAction GoalAction
	// PreserveWhitespace keeps Text unchanged when framing the prompt instead of trimming it.
	PreserveWhitespace bool
	// ConversationID is the conversation that runs the turn; empty until the message is routed.
	ConversationID string
	// Metadata carries source-supplied context; the Inbound*MetadataKey keys are RocketClaw's trusted provenance and routing hints.
	Metadata map[string]string
	// Workflow, when set, starts the named workflow instead of an ordinary model turn.
	Workflow *WorkflowInvocation
	// SyncDestination, when set, is a conversation that receives this turn: the turn waits in its queue,
	// runs in ConversationID, and then its new history and final reply are copied into SyncDestination.
	SyncDestination string
	// RequireOutputDecision runs the turn with cron tools and repeats it until the model decides whether to publish its output.
	RequireOutputDecision bool
	// Cronjob identifies the cron job that produced the message; nil for other messages.
	Cronjob *CronjobMessage

	responseInit, responseOnce sync.Once
	responseCh                 chan InboundResponse
}

// SlackReplyTarget identifies the Slack message that owns a streamed reply.
type SlackReplyTarget struct {
	ChannelID, MessageTS, ThreadTS   string
	RecipientTeamID, RecipientUserID string
}

// TextConversationTarget identifies a conversation/message in the configured primary text connector.
type TextConversationTarget struct{ ChannelID, MessageID, ThreadID string }

// AskUserQuestionOption is one native UI choice for ask_user_question.
type AskUserQuestionOption struct{ Label, Value, Description string }

// AskUserQuestionRequest asks the originating text connector human for input.
type AskUserQuestionRequest struct {
	Source                Source
	ID, Question, Details string
	ConversationID        string
	Options               []AskUserQuestionOption
	Multiple              bool
	SlackReply            *SlackReplyTarget
}

// AskUserQuestionAnswer is returned to RocketCode after a human answers.
type AskUserQuestionAnswer struct {
	Selected []string `json:"selected"`
	Custom   string   `json:"custom"`
	Source   Source   `json:"source"`
}

// UserQuestionAsker is the origin-owned ask_user_question capability for one turn path.
// The zero value is inert (ExposeTool is false).
type UserQuestionAsker struct {
	expose bool
	ask    func(context.Context, *AskUserQuestionRequest) (AskUserQuestionAnswer, error)
}

// NoUserQuestionAsker returns the inert asker that omits the tool from the model list.
func NoUserQuestionAsker() UserQuestionAsker { return UserQuestionAsker{} }

// InteractiveUserQuestionAsker returns an asker that exposes the tool and delegates to ask.
func InteractiveUserQuestionAsker(ask func(context.Context, *AskUserQuestionRequest) (AskUserQuestionAnswer, error)) UserQuestionAsker {
	return UserQuestionAsker{expose: true, ask: ask}
}

// ExposeTool reports whether ask_user_question belongs in the model tool list.
func (a UserQuestionAsker) ExposeTool() bool { return a.expose }

// AskUserQuestion runs the origin ask path, or rejects when the tool is not exposed.
func (a UserQuestionAsker) AskUserQuestion(ctx context.Context, req *AskUserQuestionRequest) (AskUserQuestionAnswer, error) {
	if !a.expose {
		return AskUserQuestionAnswer{}, errors.New("ask_user_question is not available")
	}

	return a.ask(ctx, req)
}

// StartNewThreadRequest asks RocketClaw to create a new managed conversation from the current turn.
type StartNewThreadRequest struct {
	Source                             Source
	CurrentAgent, Agent, Title, Prompt string
	AllowedAgents                      []string
	SlackReply                         *SlackReplyTarget
}

// StartNewThreadResult reports the created conversation and openable surface.
type StartNewThreadResult struct {
	ConversationID string `json:"conversation_id"`
	URL            string `json:"url,omitempty"`
}

// StartNewThreadRootResult reports the native root surface created by a text connector.
type StartNewThreadRootResult struct {
	Target TextConversationTarget
	URL    string
}

// OutboundMessage is a text message headed to enabled connectors.
type OutboundMessage struct {
	ConsumedID, ConsumedText           string
	ConsumedHeader                     string
	ConsumedRawText                    string
	ConsumedSource                     Source
	Text                               string
	ConversationID, TurnID             string
	ExternalConversationID             string
	Agent                              string
	Model, SourceConversationID        string
	ReasoningEffort                    *string
	Cronjob                            *CronjobMessage
	Complete                           bool
	SlackReply                         *SlackReplyTarget
	Attachments                        []OutboundAttachment
	GoalTurn, GoalComplete, GoalActive bool
	GoalTurnNumber, GoalMaxTurns       int
	WorkflowTerminal                   Terminal

	deliveryInit, deliveredOnce sync.Once
	delivered                   chan struct{}
	deliveryErr                 error
}

// NewInboundMessage constructs an unrouted inbound message.
func NewInboundMessage(source Source, kind InboundKind, text string, human bool) *InboundMessage {
	return &InboundMessage{
		Source: source, Text: text, Human: human, Kind: kind,
	}
}

// SetInboundAllowedAgents records surface-constrained agents on an inbound message.
func SetInboundAllowedAgents(inbound *InboundMessage, agents []string) {
	if inbound.Metadata == nil {
		inbound.Metadata = map[string]string{}
	}

	inbound.Metadata[InboundAllowedAgentsMetadataKey] = strings.Join(agents, ",")
}

// NewInboundMessageFromContent constructs an unrouted inbound message from normalized source content.
func NewInboundMessageFromContent(source Source, kind InboundKind, content *InboundContent, human bool) *InboundMessage {
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

	inbound.AttachmentPresence = content.AttachmentPresence
	if len(content.Attachments) > 0 {
		inbound.AttachmentPresence = AttachmentPresenceImages
	} else if inbound.AttachmentPresence == AttachmentPresenceUnsupported && len(content.TextAttachments) > 0 {
		inbound.AttachmentPresence = AttachmentPresenceNone
	}

	inbound.AttachmentWarnings = append([]string(nil), content.AttachmentWarnings...)

	return inbound
}

// NormalizeMIMEType returns a lowercase media type, or the trimmed lowercase input if parsing fails.
func NormalizeMIMEType(mimeType string) string {
	if mediaType, _, err := mime.ParseMediaType(mimeType); err == nil {
		mimeType = mediaType
	}

	return strings.ToLower(strings.TrimSpace(mimeType))
}

// IsTextAttachment reports whether an attachment should be included as literal prompt text.
func IsTextAttachment(name, mimeType string) bool {
	mediaType := NormalizeMIMEType(mimeType)

	return strings.HasPrefix(mediaType, "text/") || slices.Contains([]string{"application/json", "application/jsonl", "application/ld+json", "application/xml", "application/yaml", "application/x-yaml", "application/toml", "application/x-toml", "application/csv", "application/x-ndjson"}, mediaType) || slices.Contains([]string{".txt", ".md", ".markdown", ".csv", ".tsv", ".json", ".jsonl", ".ndjson", ".yaml", ".yml", ".toml", ".xml", ".ini", ".log"}, strings.ToLower(filepath.Ext(strings.TrimSpace(name))))
}

// EnableResponseWait returns a channel that receives the final result for this inbound turn.
func (m *InboundMessage) EnableResponseWait() <-chan InboundResponse { return m.responseChannel() }

// CompleteResponseWithAttachments marks this inbound turn result ready with response attachments.
func (m *InboundMessage) CompleteResponseWithAttachments(text string, attachments []OutboundAttachment, err error) {
	ch := m.responseChannel()
	m.responseOnce.Do(func() {
		ch <- InboundResponse{Text: text, Attachments: CloneOutboundAttachments(attachments), Err: err}

		close(ch)
	})
}

// NewOutboundMessage constructs an outbound message for one explicit conversation.
func NewOutboundMessage(conversationID, text string) *OutboundMessage {
	return &OutboundMessage{Text: text, ConversationID: strings.TrimSpace(conversationID)}
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
