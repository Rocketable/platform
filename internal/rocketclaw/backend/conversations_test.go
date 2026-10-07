package backend

import (
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

// Notes never come from the input's text, stay readable once consumed, and resolve in Delegation Histories.
func TestRuntimeCompletionNotesReadStoredJobs(t *testing.T) {
	store := newTestSessionService(t)
	rt := &Runtime{Sessions: store}
	conversationID := "one-off-cron:cron/report.md:20260905t020000z:run"

	script := testBackgroundJob(conversationID, "turn-1/call/a")
	subagent := testBackgroundJob(conversationID, "turn-1/call/t-1/task/call/b")
	subagent.childKey, subagent.kind, subagent.label, subagent.subagentKey = "/t-1", backgroundTask, "research", "/t-1/b-2"

	for _, job := range []*backgroundJob{script, subagent} {
		createTestBackgroundJob(t, store, job)
		finishTestBackgroundJob(t, store, job, backgroundCompleted, false)
	}

	_, err := store.db.ExecContext(t.Context(), `UPDATE background_jobs SET note_state = $1`, noteConsumed)
	require.NoError(t, err)

	notes, err := rt.CompletionNotes(t.Context(), conversationID, []string{script.jobID, subagent.jobID, "turn-1/call/unknown"})
	require.NoError(t, err)
	require.Equal(t, []protocol.BackgroundJob{{ID: script.jobID, Kind: "execute", State: "completed", Label: "tests", ToolCallID: "call",
		Note: `<execute id="turn-1/call/a" state="completed" description="tests">` + "\ncompleted\n</execute>"}}, notes, "only the conversation's own notes")

	notes, err = rt.CompletionNotes(t.Context(), conversationID+"/t-1", []string{subagent.jobID})
	require.NoError(t, err)
	require.Equal(t, []protocol.BackgroundJob{{ID: subagent.jobID, Kind: "task", State: "completed", Label: "research", ToolCallID: "call", SubagentKey: "/t-1/b-2",
		Note: `<subagent id="turn-1/call/t-1/task/call/b" state="completed" description="research" continue="/t-1/b-2">` + "\ncompleted\n</subagent>"}}, notes)

	notes, err = rt.CompletionNotes(t.Context(), conversationID+"/t-1/b-2", []string{subagent.jobID})
	require.NoError(t, err)
	require.Empty(t, notes, "a note belongs only to the history of the subagent that owns it")
}
