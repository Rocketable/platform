package protocol

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

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
	message := NewOutboundMessage("conversation", "reply")
	message.ConsumedID, message.ConsumedText, message.ConsumedRawText = "web-input", "same text attachment:ref", "same text"
	message.ConsumedSource = SourceWeb
	message.ConsumedHeader = `[Web principal="alice"]`
	message.Agent, message.Model, message.SourceConversationID, message.ReasoningEffort = "planner", "work/model-a", "producer-x", new("high")
	message.SlackReply = &SlackReplyTarget{ChannelID: "C1", ThreadTS: "1.2"}
	message.Cronjob = &CronjobMessage{RelativePath: "job.md"}
	message.Attachments = []OutboundAttachment{
		{ID: "report", Name: "report.txt", MIMEType: "text/plain", Data: []byte("report"), OriginalUnverified: true, Size: 6},
		{ID: "image", Name: "image.png", MIMEType: "image/png", Data: []byte("image"), Size: 5},
	}

	clone := CloneOutboundMessage(message)

	require.NotSame(t, message, clone)
	require.Equal(t, message.Attachments, clone.Attachments)
	require.Equal(t, "web-input", clone.ConsumedID)
	require.Equal(t, "same text attachment:ref", clone.ConsumedText)
	require.Equal(t, "same text", clone.ConsumedRawText)
	require.Equal(t, SourceWeb, clone.ConsumedSource)
	require.Equal(t, message.ConsumedHeader, clone.ConsumedHeader)
	require.Equal(t, "planner", clone.Agent)
	require.Equal(t, "work/model-a", clone.Model)
	require.Equal(t, "producer-x", clone.SourceConversationID)
	require.Equal(t, message.ReasoningEffort, clone.ReasoningEffort)
	require.NotSame(t, message.ReasoningEffort, clone.ReasoningEffort)
	require.NotSame(t, message.SlackReply, clone.SlackReply)
	require.NotSame(t, message.Cronjob, clone.Cronjob)
	require.NotSame(t, &message.Attachments[0], &clone.Attachments[0])
	require.NotSame(t, &message.Attachments[0].Data[0], &clone.Attachments[0].Data[0])

	clone.SlackReply.ThreadTS = "changed"
	clone.Cronjob.RelativePath = "changed"
	clone.Attachments[0].Name = "changed"
	clone.Attachments[0].Data[0] = 'X'
	clone.Attachments[1].Data[0] = 'Y'

	require.Equal(t, "1.2", message.SlackReply.ThreadTS)
	require.Equal(t, "job.md", message.Cronjob.RelativePath)
	require.Equal(t, "report.txt", message.Attachments[0].Name)
	require.Equal(t, byte('r'), message.Attachments[0].Data[0])
	require.Equal(t, byte('i'), message.Attachments[1].Data[0])
}
