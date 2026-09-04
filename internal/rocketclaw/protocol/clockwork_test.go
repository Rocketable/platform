package protocol

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewChannelsAreUnbuffered(t *testing.T) {
	channels := NewChannels()

	require.Equal(t, 0, cap(channels.Broadcasts))
}

func TestRequestAndResponseKinds(t *testing.T) {
	require.Equal(t, ResponseResult, (&TextResponse{Kind: ResponseResult}).ResponseKind())
	require.Equal(t, ResponseInteraction, (StartNewThreadResponse{}).ResponseKind())
	require.Contains(t, StartNewThreadRootText("Title", "Do work"), "Title")
	require.Contains(t, StartNewThreadRootText("Title", "Do work"), "Do work")
}

func TestNoUserQuestionAsker(t *testing.T) {
	require.False(t, NoUserQuestionAsker().ExposeTool())
	_, err := NoUserQuestionAsker().AskUserQuestion(t.Context(), &AskUserQuestionRequest{Question: "q"})
	require.Error(t, err)

	interactive := InteractiveUserQuestionAsker(func(context.Context, *AskUserQuestionRequest) (AskUserQuestionAnswer, error) {
		return AskUserQuestionAnswer{Custom: "ok"}, nil
	})
	require.True(t, interactive.ExposeTool())
	answer, err := interactive.AskUserQuestion(t.Context(), &AskUserQuestionRequest{Question: "q"})
	require.NoError(t, err)
	require.Equal(t, "ok", answer.Custom)
}

func TestBroadcastPublisherSendsResponseAndPreservesDelivery(t *testing.T) {
	broadcasts := make(chan Broadcast, 1)
	response := make(chan Response, 1)
	message := NewOutboundMessage(SourceSlack, "conversation", "reply")
	message.Complete = true
	message.Response = response
	publisher := BroadcastPublisher(broadcasts)

	require.NoError(t, publisher.PublishOutbound(t.Context(), message))

	broadcast := <-broadcasts
	result := (<-response).Payload.(*TextResponse)
	require.Equal(t, ResponseResult, result.Kind)
	require.Same(t, message, result.Message)
	require.Equal(t, BridgeSlack, broadcast.Sender)
	require.Same(t, message, broadcast.Delivery)
}

func TestBroadcastPublisherSkipsUnaddressedOutput(t *testing.T) {
	broadcasts := make(chan Broadcast, 1)
	message := NewOutboundMessage(SourceSystem, "conversation", "internal")

	require.NoError(t, BroadcastPublisher(broadcasts).PublishOutbound(t.Context(), message))
	require.NoError(t, message.WaitDelivered(t.Context()))

	select {
	case <-broadcasts:
		t.Fatal("unaddressed output was broadcast")
	default:
	}
}

func TestBroadcastPublisherSendsWebTargetedOutput(t *testing.T) {
	broadcasts := make(chan Broadcast, 1)
	message := NewOutboundMessage(SourceWeb, WebSessionConversationID("ops"), "reply", OutputTargetWeb)

	require.NoError(t, BroadcastPublisher(broadcasts).PublishOutbound(t.Context(), message))

	broadcast := <-broadcasts
	require.Equal(t, BridgeWeb, broadcast.Sender)
	require.Same(t, message, broadcast.Delivery)
}

func TestBroadcastCloneDeepCopiesDeliveryData(t *testing.T) {
	workflowAgent := AgentUpdate{Activity: "working"}
	workflowPhase := PhaseUpdate{Details: "phase details"}
	message := NewOutboundMessage(SourceSlack, "conversation", "reply")
	message.Originator = true
	message.Targets = []OutputTarget{OutputTargetSlack}
	message.SlackReply = &SlackReplyTarget{ChannelID: "C1", ThreadTS: "1.2"}
	message.Cronjob = &CronjobMessage{RelativePath: "job.md"}
	message.WorkflowAgent = &workflowAgent
	message.WorkflowPhase = &workflowPhase
	message.Attachments = []OutboundAttachment{{Name: "report.txt", Data: []byte("report")}}

	original := Broadcast{
		Sender:       BridgeSlack,
		Message:      message,
		Relay:        &ExternalMCPRelay{Text: "relay", Attachments: []OutboundAttachment{{Name: "a.txt", Data: []byte("a")}}},
		RelayChannel: "#ops",
	}
	clone := original.Clone()

	require.NotSame(t, original.Message, clone.Message)
	require.NotSame(t, original.Message.SlackReply, clone.Message.SlackReply)
	require.NotSame(t, original.Message.Cronjob, clone.Message.Cronjob)
	require.NotSame(t, original.Message.WorkflowAgent, clone.Message.WorkflowAgent)
	require.NotSame(t, original.Message.WorkflowPhase, clone.Message.WorkflowPhase)
	require.NotSame(t, &original.Message.Attachments[0], &clone.Message.Attachments[0])
	require.NotSame(t, &original.Message.Attachments[0].Data[0], &clone.Message.Attachments[0].Data[0])
	require.NotSame(t, original.Relay, clone.Relay)
	require.NotSame(t, &original.Relay.Attachments[0].Data[0], &clone.Relay.Attachments[0].Data[0])
	require.Equal(t, "#ops", clone.RelayChannel)
	require.True(t, clone.Message.Originator)

	clone.Message.Targets[0] = "changed"
	clone.Message.SlackReply.ThreadTS = "changed"
	clone.Message.Cronjob.RelativePath = "changed"
	clone.Message.WorkflowAgent.Activity = "changed"
	clone.Message.WorkflowPhase.Details = "changed"
	clone.Message.Attachments[0].Data[0] = 'X'
	clone.Relay.Attachments[0].Data[0] = 'Z'

	require.Equal(t, OutputTargetSlack, original.Message.Targets[0])
	require.Equal(t, "1.2", original.Message.SlackReply.ThreadTS)
	require.Equal(t, byte('a'), original.Relay.Attachments[0].Data[0])
	require.Equal(t, "job.md", original.Message.Cronjob.RelativePath)
	require.Equal(t, workflowAgent.Activity, original.Message.WorkflowAgent.Activity)
	require.Equal(t, workflowPhase.Details, original.Message.WorkflowPhase.Details)
	require.Equal(t, byte('r'), original.Message.Attachments[0].Data[0])
	require.NotNil(t, clone.Acknowledgement)
	require.Equal(t, 1, cap(clone.Acknowledgement))
}
