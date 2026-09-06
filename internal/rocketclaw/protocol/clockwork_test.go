package protocol

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestAndResponseKinds(t *testing.T) {
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

func TestCloneOutboundMessageDeepCopiesDeliveryData(t *testing.T) {
	workflowAgent := AgentUpdate{Activity: "working"}
	workflowPhase := PhaseUpdate{Name: "phase"}
	message := NewOutboundMessage("conversation", "reply")
	message.SlackReply = &SlackReplyTarget{ChannelID: "C1", ThreadTS: "1.2"}
	message.Cronjob = &CronjobMessage{RelativePath: "job.md"}
	message.WorkflowAgent = &workflowAgent
	message.WorkflowPhase = &workflowPhase
	message.Attachments = []OutboundAttachment{{Name: "report.txt", Data: []byte("report")}}

	clone := CloneOutboundMessage(message)

	require.NotSame(t, message, clone)
	require.NotSame(t, message.SlackReply, clone.SlackReply)
	require.NotSame(t, message.Cronjob, clone.Cronjob)
	require.NotSame(t, message.WorkflowAgent, clone.WorkflowAgent)
	require.NotSame(t, message.WorkflowPhase, clone.WorkflowPhase)
	require.NotSame(t, &message.Attachments[0], &clone.Attachments[0])
	require.NotSame(t, &message.Attachments[0].Data[0], &clone.Attachments[0].Data[0])

	clone.SlackReply.ThreadTS = "changed"
	clone.Cronjob.RelativePath = "changed"
	clone.WorkflowAgent.Activity = "changed"
	clone.WorkflowPhase.Name = "changed"
	clone.Attachments[0].Data[0] = 'X'

	require.Equal(t, "1.2", message.SlackReply.ThreadTS)
	require.Equal(t, "job.md", message.Cronjob.RelativePath)
	require.Equal(t, workflowAgent.Activity, message.WorkflowAgent.Activity)
	require.Equal(t, workflowPhase.Name, message.WorkflowPhase.Name)
	require.Equal(t, byte('r'), message.Attachments[0].Data[0])
}
