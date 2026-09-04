package slackconnector

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type recordingConversationBackend struct {
	mu         sync.Mutex
	agent      string
	agentErr   error
	known      map[string]string
	agentReads []string
	turns      chan protocol.TurnRequest
	recorded   []protocol.TurnRequest
	switches   []string
	created    []string
	queue      []protocol.ThreadQueueItem
	busy       bool
	scheduled  map[string]protocol.ScheduledMessageState
	workflows  []protocol.WorkflowDescription
}

func (r *recordingConversationBackend) Subscribe(context.Context) <-chan protocol.ConversationEvent {
	ch := make(chan protocol.ConversationEvent)
	close(ch)

	return ch
}

func (r *recordingConversationBackend) CreateConversation(id string, _ []string, _ []protocol.ConversationTag) error {
	r.created = append(r.created, id)

	return nil
}

func (r *recordingConversationBackend) RunTurn(_ context.Context, req *protocol.TurnRequest) error {
	r.mu.Lock()
	r.recorded = append(r.recorded, *req)
	r.mu.Unlock()

	if r.turns != nil {
		r.turns <- *req
	}

	return nil
}

func (r *recordingConversationBackend) SyncConversation(context.Context, string, string) error {
	return nil
}

func (r *recordingConversationBackend) ListConversations() ([]protocol.ConversationRecord, error) {
	return nil, nil
}

func (r *recordingConversationBackend) ConversationAgent(id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.agentReads = append(r.agentReads, id)
	if r.agentErr != nil {
		return "", r.agentErr
	}

	if agent, ok := r.known[id]; ok {
		return agent, nil
	}

	if r.agent == "" {
		return "", protocol.ErrUnknownConversation
	}

	return r.agent, nil
}

func (r *recordingConversationBackend) SwitchAgent(id, agent string) error {
	r.switches = append(r.switches, id+":"+agent)

	return nil
}

func (r *recordingConversationBackend) ListLaterWork(context.Context, string) ([]protocol.ThreadQueueItem, error) {
	return r.queue, nil
}

func (r *recordingConversationBackend) DeleteLaterWork(_ context.Context, _, itemID string) error {
	kept := r.queue[:0]
	for i := range r.queue {
		if r.queue[i].ID != itemID {
			kept = append(kept, r.queue[i])
		}
	}

	r.queue = kept

	return nil
}

func (r *recordingConversationBackend) ReorderLaterWork(_ context.Context, _ string, itemIDs []string) error {
	byID := make(map[string]protocol.ThreadQueueItem, len(r.queue))
	for i := range r.queue {
		byID[r.queue[i].ID] = r.queue[i]
	}

	next := make([]protocol.ThreadQueueItem, 0, len(itemIDs))
	for i, id := range itemIDs {
		item := byID[id]
		item.Position = i
		next = append(next, item)
	}

	r.queue = next

	return nil
}

func (r *recordingConversationBackend) ConversationBusy(string) bool {
	return r.busy
}

func (r *recordingConversationBackend) ScheduledMessages(string) (map[string]protocol.ScheduledMessageState, error) {
	return r.scheduled, nil
}

func (r *recordingConversationBackend) WorkflowDescriptions() ([]protocol.WorkflowDescription, error) {
	return r.workflows, nil
}

func (r *recordingConversationBackend) turnSnapshot() []protocol.TurnRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.recorded)
}

func (r *recordingConversationBackend) agentReadSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.agentReads)
}

func TestRunConversationTurnUsesSlackThreadY(t *testing.T) {
	rec := &recordingConversationBackend{agent: "main", turns: make(chan protocol.TurnRequest, 1)}
	c := new(Connector)
	c.conv = rec
	c.log = testLogger()
	id := protocol.SlackThreadConversationID("C1", "1.1")
	require.NoError(t, c.runConversationTurn(t.Context(), &protocol.TurnRequest{ID: id, Kind: protocol.TurnPrompt, Text: "hello"}))

	req := <-rec.turns
	require.Equal(t, id, req.ID)
	require.NotContains(t, req.ID, "external_mcp:")
	require.Equal(t, protocol.TurnPrompt, req.Kind)
	require.Equal(t, "hello", req.Text)
}

func TestRunConversationTurnUnknownCreatesThenRuns(t *testing.T) {
	rec := &recordingConversationBackend{turns: make(chan protocol.TurnRequest, 1)}
	c := new(Connector)
	c.conv = rec
	c.log = testLogger()
	id := protocol.SlackThreadConversationID("C1", "1.1")
	require.NoError(t, c.runConversationTurn(t.Context(), &protocol.TurnRequest{ID: id, Kind: protocol.TurnPrompt, Text: "hello", Agent: "social"}))
	require.Equal(t, []string{id}, rec.created)
	req := <-rec.turns
	require.Equal(t, id, req.ID)
	require.Equal(t, protocol.TurnPrompt, req.Kind)
	require.Equal(t, "hello", req.Text)
}

func TestRunConversationTurnUnknownWithoutAgentFails(t *testing.T) {
	c := new(Connector)
	c.conv = inertConversationBackend{}
	require.ErrorIs(t, c.runConversationTurn(t.Context(), &protocol.TurnRequest{ID: protocol.SlackThreadConversationID("C1", "1.1"), Kind: protocol.TurnPrompt, Text: "hello"}), protocol.ErrUnknownConversation)
}

func TestRunConversationTurnKindsStayOnY(t *testing.T) {
	rec := &recordingConversationBackend{agent: "main", turns: make(chan protocol.TurnRequest, 5)}
	c := new(Connector)
	c.conv = rec
	c.log = testLogger()
	id := protocol.SlackThreadConversationID("C1", "1.1")
	require.NoError(t, c.runConversationTurn(t.Context(), &protocol.TurnRequest{ID: id, Kind: protocol.TurnCancel}))
	require.NoError(t, c.runConversationTurn(t.Context(), &protocol.TurnRequest{ID: id, Kind: protocol.TurnEnqueue, Text: "later"}))
	require.NoError(t, c.runConversationTurn(t.Context(), &protocol.TurnRequest{ID: id, Kind: protocol.TurnGoal, Objective: "ship"}))
	require.NoError(t, c.runConversationTurn(t.Context(), &protocol.TurnRequest{ID: id, Kind: protocol.TurnWorkflow, Workflow: "review"}))
	require.NoError(t, c.runConversationTurn(t.Context(), &protocol.TurnRequest{ID: id, Kind: protocol.TurnSteer, Text: "also"}))

	got := map[protocol.TurnKind]protocol.TurnRequest{}

	for range 5 {
		req := <-rec.turns
		require.Equal(t, id, req.ID)
		got[req.Kind] = req
	}

	require.Equal(t, protocol.TurnCancel, got[protocol.TurnCancel].Kind)
	require.Equal(t, "later", got[protocol.TurnEnqueue].Text)
	require.Equal(t, "ship", got[protocol.TurnGoal].Objective)
	require.Equal(t, "review", got[protocol.TurnWorkflow].Workflow)
	require.Equal(t, "also", got[protocol.TurnSteer].Text)
}

func TestSwitchAgentStaysOnY(t *testing.T) {
	rec := &recordingConversationBackend{agent: "main"}
	id := protocol.SlackThreadConversationID("C1", "1.1")
	c := new(Connector)
	c.conv = rec
	require.NoError(t, c.conv.SwitchAgent(id, "review"))
	require.Equal(t, []string{id + ":review"}, rec.switches)
	require.NotContains(t, rec.switches[0], "external_mcp:")
}

func TestDeliverConversationEventIgnoresProducerX(t *testing.T) {
	c := new(Connector)
	c.deliverConversationEvent(t.Context(), protocol.ConversationEvent{ConversationID: "external_mcp:planner:x", Text: "secret", Role: "assistant", Complete: true})
}

func TestDeliverConversationEventAcceptsSlackThreadY(t *testing.T) {
	id := protocol.SlackThreadConversationID("C1", "1.1")
	channelID, threadTS, ok := protocol.SlackThreadTarget(id)
	require.True(t, ok)
	require.Equal(t, "C1", channelID)
	require.Equal(t, "1.1", threadTS)

	_, _, xOK := protocol.SlackThreadTarget("external_mcp:planner:x")
	require.False(t, xOK)
}
