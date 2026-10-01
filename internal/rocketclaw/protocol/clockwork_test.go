package protocol

import (
	"context"
	"encoding/json"
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
	workflowAgent := AgentUpdate{Activity: "working"}
	workflowPhase := PhaseUpdate{Name: "phase"}
	message := NewOutboundMessage("conversation", "reply")
	message.ConsumedID, message.ConsumedText, message.ConsumedRawText = "web-input", "same text attachment:ref", "same text"
	message.ConsumedSource = SourceWeb
	message.ConsumedHeader = `[Web principal="alice"]`
	message.Agent, message.Model, message.SourceConversationID, message.ReasoningEffort = "planner", "work/model-a", "producer-x", new("high")
	message.SlackReply = &SlackReplyTarget{ChannelID: "C1", ThreadTS: "1.2"}
	message.Cronjob = &CronjobMessage{RelativePath: "job.md"}
	message.WorkflowAgent = &workflowAgent
	message.WorkflowPhase = &workflowPhase
	message.Attachments = []OutboundAttachment{{Name: "report.txt", Data: []byte("report")}}
	message.TranscriptCheckpoint = json.RawMessage(`{"replay_input":[{"content":"full result"}]}`)
	message.TranscriptEntry = json.RawMessage(`{"replay_input_ids":{"web-input":0}}`)
	message.TranscriptEntryID, message.TranscriptTerminal = 42, TerminalComplete

	clone := CloneOutboundMessage(message)

	require.NotSame(t, message, clone)
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
	require.NotSame(t, message.WorkflowAgent, clone.WorkflowAgent)
	require.NotSame(t, message.WorkflowPhase, clone.WorkflowPhase)
	require.NotSame(t, &message.Attachments[0], &clone.Attachments[0])
	require.NotSame(t, &message.Attachments[0].Data[0], &clone.Attachments[0].Data[0])
	require.Equal(t, message.TranscriptCheckpoint, clone.TranscriptCheckpoint)
	require.Equal(t, message.TranscriptEntry, clone.TranscriptEntry)
	require.Equal(t, int64(42), clone.TranscriptEntryID)
	require.Equal(t, TerminalComplete, clone.TranscriptTerminal)
	clone.TranscriptCheckpoint[0], clone.TranscriptEntry[0] = 'X', 'X'
	require.Equal(t, byte('{'), message.TranscriptCheckpoint[0])
	require.Equal(t, byte('{'), message.TranscriptEntry[0])

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
