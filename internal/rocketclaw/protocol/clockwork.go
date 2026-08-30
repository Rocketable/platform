// Package protocol defines shared RocketClaw event contracts.
package protocol

import (
	"context"
	"fmt"
	"slices"
)

// BridgeID identifies a connector participating in RocketClaw communication.
type BridgeID string

const (
	// BridgeSlack identifies the Slack connector.
	BridgeSlack BridgeID = "slack"
	// BridgeExternalMCP identifies the External MCP connector.
	BridgeExternalMCP BridgeID = "external_mcp"
)

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

// TextResponse carries originator output for one inbound.
type TextResponse struct {
	Kind    ResponseKind
	Message *OutboundMessage
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

// Channels are the unbuffered channels used for connector communication.
type Channels struct {
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
		Broadcasts: make(chan Broadcast),
	}
}

// Clone returns an independent delivery copy of b with its own acknowledgement channel.
func (b *Broadcast) Clone() Broadcast {
	var message *OutboundMessage
	if b.Message != nil {
		message = CloneOutboundMessage(b.Message)
	}

	relay := clonePtr(b.Relay)
	if relay != nil {
		relay.Attachments = CloneOutboundAttachments(b.Relay.Attachments)
	}

	return Broadcast{
		Sender:          b.Sender,
		Message:         message,
		Delivery:        b.Delivery,
		Relay:           relay,
		RelayReply:      b.RelayReply,
		RelayChannel:    b.RelayChannel,
		RelayCleanup:    b.RelayCleanup,
		RelayResponse:   b.RelayResponse,
		Acknowledgement: make(chan BroadcastAcknowledgement, 1),
	}
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}

	clone := *p

	return &clone
}

// CloneOutboundMessage returns a deep copy suitable for an independent connector delivery.
func CloneOutboundMessage(message *OutboundMessage) *OutboundMessage {
	return &OutboundMessage{
		Text: message.Text, ProgressText: message.ProgressText, Source: message.Source, Bridge: message.Bridge,
		Targets: slices.Clone(message.Targets), ConversationID: message.ConversationID, TurnID: message.TurnID,
		SessionEntryID: message.SessionEntryID, ExternalConversationID: message.ExternalConversationID, Agent: message.Agent,
		Sequence: message.Sequence, PostProgressText: message.PostProgressText, Complete: message.Complete,
		SlackReply: clonePtr(message.SlackReply), Attachments: CloneOutboundAttachments(message.Attachments),
		GoalTurn: message.GoalTurn, GoalComplete: message.GoalComplete, GoalActive: message.GoalActive,
		GoalTurnNumber: message.GoalTurnNumber, GoalMaxTurns: message.GoalMaxTurns, WorkflowTerminal: message.WorkflowTerminal,
		Cronjob: clonePtr(message.Cronjob), WorkflowAgent: clonePtr(message.WorkflowAgent), WorkflowPhase: clonePtr(message.WorkflowPhase),
	}
}
