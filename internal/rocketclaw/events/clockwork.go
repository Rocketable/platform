// Package events defines shared RocketClaw event contracts.
package events

import (
	"context"
	"fmt"
	"slices"

	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
)

// BridgeID identifies a connector participating in RocketClaw communication.
type BridgeID string

const (
	// BridgeSlack identifies the Slack connector.
	BridgeSlack BridgeID = "slack"
	// BridgeExternalMCP identifies the External MCP connector.
	BridgeExternalMCP BridgeID = "external_mcp"
)

// RequestKind identifies the operation carried by a Request.
type RequestKind string

// Text request operation kinds.
const (
	RequestTextStartThread           RequestKind = "text_start_thread"
	RequestTextStartGoal             RequestKind = "text_start_goal"
	RequestTextStartWorkflow         RequestKind = "text_start_workflow"
	RequestTextReserveWorkflowTurn   RequestKind = "text_reserve_workflow_turn"
	RequestTextWorkflowDescriptions  RequestKind = "text_workflow_descriptions"
	RequestTextInterruptConversation RequestKind = "text_interrupt_conversation"
	RequestTextInterruptThread       RequestKind = "text_interrupt_thread"
	RequestTextRegisterThread        RequestKind = "text_register_thread"
	RequestTextRegisterCronThread    RequestKind = "text_register_cron_thread"
	RequestTextThreadAgent           RequestKind = "text_thread_agent"
	RequestTextSwitchThreadAgent     RequestKind = "text_switch_thread_agent"
	RequestTextSubmitThreadReply     RequestKind = "text_submit_thread_reply"
	RequestTextSubmitExternalMCP     RequestKind = "text_submit_external_mcp"
)

// TextRequest carries one operation formerly sent through the primary text router.
type TextRequest struct {
	Kind                                      RequestKind
	Target                                    TextConversationTarget
	Agent, Objective, CheckScript, Name, Args string
	ConversationID                            string
	MaxTurns                                  int
	Inbound                                   *InboundMessage
}

// RequestKind identifies the TextRequest operation.
func (r *TextRequest) RequestKind() RequestKind { return r.Kind }

// RequestOperation is the typed operation payload carried by a Request.
// Concrete operation values are defined alongside the shared event data they use.
type RequestOperation interface {
	RequestKind() RequestKind
}

// Request carries one connector operation into RocketClaw.
type Request struct {
	Sender    BridgeID
	Operation RequestOperation
	Response  chan Response
}

// ResponseKind identifies the kind of value carried by a Response.
type ResponseKind string

const (
	// ResponseProgress carries observable operation progress.
	ResponseProgress ResponseKind = "progress"
	// ResponseInteraction carries an interaction for a connector to handle.
	ResponseInteraction ResponseKind = "interaction"
	// ResponseResult carries a completed operation result.
	ResponseResult ResponseKind = "result"
)

// ResponsePayload is the typed value carried by a Response.
type ResponsePayload interface {
	ResponseKind() ResponseKind
}

// TextResponse carries the result of one TextRequest operation.
type TextResponse struct {
	Kind         ResponseKind
	Message      *OutboundMessage
	Handled      bool
	Created      bool
	Reserved     bool
	Agent        string
	Descriptions []workflow.Description
	Inbound      *InboundMessage
	Release      chan struct{}
}

// ResponseKind identifies the TextResponse result.
func (r *TextResponse) ResponseKind() ResponseKind { return r.Kind }

// StartNewThreadResponse asks a connector to create a native thread root.
type StartNewThreadResponse struct {
	Request *StartNewThreadRequest
	Root    chan StartNewThreadRootResult
	Err     chan error
}

// ResponseKind identifies the child-thread interaction.
func (StartNewThreadResponse) ResponseKind() ResponseKind { return ResponseInteraction }

// Response carries progress, interaction, result, or error information for a Request.
type Response struct {
	Payload ResponsePayload
	Err     error
}

// BroadcastStatus describes how a Bridge handled a Broadcast.
type BroadcastStatus string

const (
	// BroadcastHandled reports that a Bridge delivered or acted on a Broadcast.
	BroadcastHandled BroadcastStatus = "handled"
	// BroadcastDropped reports that a Bridge intentionally ignored a Broadcast.
	BroadcastDropped BroadcastStatus = "dropped"
	// BroadcastFailed reports that a Bridge could not handle a Broadcast.
	BroadcastFailed BroadcastStatus = "failed"
)

// BroadcastAcknowledgement reports the outcome of one Bridge delivery.
type BroadcastAcknowledgement struct {
	Status BroadcastStatus
	Err    error
}

// BroadcastReply carries the result of a Bridge-specific Broadcast operation.
type BroadcastReply struct {
	Message *InboundMessage
	Err     error
}

// Broadcast carries live output to Bridges other than its sender.
type Broadcast struct {
	Sender          BridgeID
	Message         *OutboundMessage
	Delivery        *OutboundMessage
	Relay           *ExternalMCPRelay
	RelayReply      *InboundMessage
	RelayChannel    string
	RelayCleanup    *InboundMessage
	RelayResponse   chan BroadcastReply
	Acknowledgement chan BroadcastAcknowledgement
}

// Channels are the two unbuffered channels used for connector communication.
type Channels struct {
	Requests   chan Request
	Broadcasts chan Broadcast
}

// OutboundPublisher sends one outbound message into connector delivery.
type OutboundPublisher interface {
	PublishOutbound(context.Context, *OutboundMessage) error
}

// BroadcastPublisher adapts the Broadcasts channel to the outbound publisher contract.
type BroadcastPublisher chan<- Broadcast

// PublishOutbound publishes one live output Broadcast.
func (p BroadcastPublisher) PublishOutbound(ctx context.Context, message *OutboundMessage) error {
	if message.Response == nil && message.Cronjob == nil && !slices.Contains(message.Targets, OutputTargetSlack) {
		message.MarkDelivered(nil)

		return nil
	}

	if message.Response != nil {
		kind := ResponseProgress
		if message.Complete {
			kind = ResponseResult
		}

		select {
		case message.Response <- Response{Payload: &TextResponse{Kind: kind, Message: message}}:
		case <-ctx.Done():
			return fmt.Errorf("publish outbound response: %w", ctx.Err())
		}
	}

	sender := message.Bridge
	if sender == "" {
		switch message.Source {
		case SourceSlack:
			sender = BridgeSlack
		case SourceExternalMCP:
			sender = BridgeExternalMCP
		case SourceSystem:
		}
	}

	select {
	case p <- Broadcast{Sender: sender, Message: message, Delivery: message}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("publish outbound broadcast: %w", ctx.Err())
	}
}

// NewChannels constructs the unbuffered connector channels.
func NewChannels() Channels {
	return Channels{
		Requests:   make(chan Request),
		Broadcasts: make(chan Broadcast),
	}
}

// Clone returns an independent delivery copy of b with its own acknowledgement channel.
func (b *Broadcast) Clone() Broadcast {
	var message *OutboundMessage
	if b.Message != nil {
		message = CloneOutboundMessage(b.Message)
	}

	return Broadcast{
		Sender:          b.Sender,
		Message:         message,
		Delivery:        b.Delivery,
		Relay:           cloneExternalMCPRelay(b.Relay),
		RelayReply:      b.RelayReply,
		RelayChannel:    b.RelayChannel,
		RelayCleanup:    b.RelayCleanup,
		RelayResponse:   b.RelayResponse,
		Acknowledgement: make(chan BroadcastAcknowledgement, 1),
	}
}

func cloneExternalMCPRelay(relay *ExternalMCPRelay) *ExternalMCPRelay {
	if relay == nil {
		return nil
	}

	clone := *relay
	clone.Attachments = CloneOutboundAttachments(relay.Attachments)

	return &clone
}

// CloneOutboundMessage returns a deep copy suitable for an independent connector delivery.
func CloneOutboundMessage(message *OutboundMessage) *OutboundMessage {
	clone := &OutboundMessage{
		Text:                   message.Text,
		ProgressText:           message.ProgressText,
		Source:                 message.Source,
		Bridge:                 message.Bridge,
		Targets:                slices.Clone(message.Targets),
		ConversationID:         message.ConversationID,
		TurnID:                 message.TurnID,
		ExternalConversationID: message.ExternalConversationID,
		Agent:                  message.Agent,
		Sequence:               message.Sequence,
		PostProgressText:       message.PostProgressText,
		Complete:               message.Complete,
		GoalTurn:               message.GoalTurn,
		GoalComplete:           message.GoalComplete,
		GoalTurnNumber:         message.GoalTurnNumber,
		GoalMaxTurns:           message.GoalMaxTurns,
		WorkflowTerminal:       message.WorkflowTerminal,
		Attachments:            CloneOutboundAttachments(message.Attachments),
	}

	if message.SlackReply != nil {
		reply := *message.SlackReply
		clone.SlackReply = &reply
	}

	if message.Cronjob != nil {
		cronjob := *message.Cronjob
		clone.Cronjob = &cronjob
	}

	if message.WorkflowAgent != nil {
		workflowAgent := *message.WorkflowAgent
		clone.WorkflowAgent = &workflowAgent
	}

	if message.WorkflowPhase != nil {
		workflowPhase := *message.WorkflowPhase
		clone.WorkflowPhase = &workflowPhase
	}

	return clone
}
