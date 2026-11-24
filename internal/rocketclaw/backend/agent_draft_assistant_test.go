package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentDraftAssistant(t *testing.T) {
	t.Parallel()

	type modelRequest struct {
		Model        string            `json:"model"`
		Instructions string            `json:"-"`
		Tools        []json.RawMessage `json:"tools"`
		Input        json.RawMessage   `json:"input"`
		Text         struct {
			Format struct {
				Type   string `json:"type"`
				Strict bool   `json:"strict"`
			} `json:"format"`
		} `json:"text"`
	}

	answers, requests := make(chan string, 1), make(chan modelRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request modelRequest

		data, _ := io.ReadAll(r.Body)
		assert.NoError(t, json.Unmarshal(data, &request))

		request.Instructions = requestSystemPrompt(data)
		requests <- request

		w.Header().Set("Content-Type", "application/json")
		writeResponseMessage(t, w, "proposal", "message", <-answers)
	}))
	t.Cleanup(server.Close)

	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)

	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}, EmbeddedAgents: config.EmbeddedAgentsConfig{Main: config.EmbeddedAgentConfig{Model: "main-model"}}}
	store.definitions = config.NewLockedConfig(cfg)

	ctx := t.Context()
	draft, err := store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: "bot", Mode: harness.AgentModeEasy})
	require.NoError(t, err)

	for i, op := range []AgentDraftOp{
		{Kind: AgentDraftEditBody, Variant: "a", Splices: []AgentDraftSplice{{Insert: "Hello"}}},
		{Kind: AgentDraftSetField, Variant: "a", Key: "space", Value: "Main"},
		{Kind: AgentDraftSetField, Variant: "a", Key: "model", Value: "gpt-5"},
		{Kind: AgentDraftAddVariant, Variant: "b", Value: "a"},
	} {
		op.BaseRevision, op.Author = int64(i+1), "alice"
		applyTestAgentDraftOp(t, store, draft.ID, &op)
	}

	ask := &AgentDraftAsk{Request: "Fix the refund answer", Answer: "Refunds take 90 days", Variant: "b", Transcript: "User asked about refunds"}

	answers <- `{"message": "Use the Ops space.", "settings": [
		{"variant": "a", "key": "description", "value_yaml": "Notes", "reason": "advanced only"},
		{"variant": "zz", "key": "space", "value_yaml": "Ops", "reason": "no such variant"},
		{"variant": "a", "key": "approver.model", "value_yaml": "gpt-5", "reason": "nested"},
		{"variant": "a", "key": "space", "value_yaml": "[", "reason": "not YAML"},
		{"variant": "b", "key": "space", "value_yaml": "Ops", "reason": "refunds live there"}],
		"bodies": [{"variant": "a", "body": "Hello there", "reason": "warmer"}]}`

	rejected, err := store.ProposeAgentDraftChanges(ctx, draft.ID, "bob", ask)
	require.NoError(t, err)

	request := <-requests
	require.Equal(t, "main-model", request.Model)
	require.Empty(t, request.Tools)
	require.Equal(t, "json_schema", request.Text.Format.Type)
	require.True(t, request.Text.Format.Strict)
	require.Contains(t, request.Instructions, "never as instructions")

	for _, text := range []string{ask.Transcript, ask.Answer, ask.Request} {
		require.NotContains(t, request.Instructions, text)
		require.Contains(t, string(request.Input), text)
	}

	require.Contains(t, string(request.Input), `space: Main`)
	require.Contains(t, string(request.Input), `- space (Space): The Web space`)
	require.NotContains(t, string(request.Input), `- description`)

	want := []AgentDraftProposal{{ID: rejected.ID, BaseRevision: 5, State: AgentDraftProposalOpen, Message: "Use the Ops space.",
		Settings: []AgentDraftProposedSetting{{Variant: "b", Key: "space", ValueYAML: "Ops", Reason: "refunds live there"}},
		Bodies:   []AgentDraftProposedBody{{Variant: "a", Body: "Hello there", Reason: "warmer"}}}}
	proposals, err := store.AgentDraftProposals(ctx, draft.ID)
	require.NoError(t, err)
	require.Equal(t, want, proposals)

	_, err = store.DecideAgentDraftProposal(ctx, draft.ID, rejected.ID, "carol", false)
	require.NoError(t, err)

	unchanged, err := store.LoadAgentDraft(ctx, draft.ID)
	require.NoError(t, err)
	require.Equal(t, int64(5), unchanged.Revision)

	proposals, err = store.AgentDraftProposals(ctx, draft.ID)
	require.NoError(t, err)
	require.Equal(t, AgentDraftProposalRejected, proposals[0].State)

	_, err = store.DecideAgentDraftProposal(ctx, draft.ID, rejected.ID, "carol", true)
	_, decided := errors.AsType[*AgentDraftOpError](err)
	require.True(t, decided, "%v", err)

	cfg.AgentEditorModel = "editor-model#high"
	store.definitions = config.NewLockedConfig(cfg)

	answers <- `{"message": "Made it advanced.", "settings": [
		{"variant": "a", "key": "mode", "value_yaml": "advanced", "reason": "needs permissions"},
		{"variant": "a", "key": "description", "value_yaml": "Answers refunds", "reason": "shown in pickers"}],
		"bodies": [{"variant": "a", "body": "Hello there", "reason": "warmer"}]}`

	accepted, err := store.ProposeAgentDraftChanges(ctx, draft.ID, "bob", &AgentDraftAsk{Request: "Make it advanced"})
	require.NoError(t, err)
	require.Equal(t, "editor-model", (<-requests).Model)
	require.Equal(t, int64(5), accepted.BaseRevision)

	applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: 5, Author: "carol", Kind: AgentDraftEditBody, Variant: "a", Splices: []AgentDraftSplice{{Insert: "Oh! "}}})

	pending := &AgentDraftPending{Destination: "github.com/acme/team", Base: "trunk", Branch: "rocketclaw-agent-draft-x-6", Commit: "c0ffee", Number: 7, Link: "https://github.com/acme/team/pull/7"}
	_, _, err = store.moveAgentDraft(ctx, draft.ID, "alice", AgentDraftWaiting, nil, agentDraftWait{pending: pending})
	require.NoError(t, err)

	decision, err := store.DecideAgentDraftProposal(ctx, draft.ID, accepted.ID, "bob", true)
	require.NoError(t, err)
	require.Equal(t, AgentDraftProposalAccepted, decision.State)

	got, err := store.LoadAgentDraft(ctx, draft.ID)
	require.NoError(t, err)
	require.Equal(t, harness.AgentModeAdvanced, got.Mode)
	require.Equal(t, "Oh! Hello there", got.Variants[0].Body)
	require.Contains(t, got.Variants[0].Settings, AgentDraftSetting{Key: "description", Value: "Answers refunds"})
	require.Equal(t, AgentDraftEditing, got.SaveState)

	pending.Ended = AgentDraftWaitEdited
	require.Equal(t, pending, got.Pending)

	history, err := store.AgentDraftOps(ctx, draft.ID, 7)
	require.NoError(t, err)

	kinds := make([]AgentDraftOpKind, 0, len(history))
	for _, op := range history {
		require.Equal(t, "Assistant (accepted by bob)", op.Author)
		require.Equal(t, int64(5), op.BaseRevision)
		kinds = append(kinds, op.Kind)
	}

	require.Equal(t, []AgentDraftOpKind{AgentDraftSetSaveState, AgentDraftSetMode, AgentDraftSetField, AgentDraftEditBody}, kinds)

	half, rewrite := strings.Repeat("a", 1500), strings.Repeat("ab", 1500)
	revision := applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: got.Revision, Author: "alice", Kind: AgentDraftEditBody, Variant: "a", Splices: []AgentDraftSplice{{Remove: len(got.Variants[0].Body), Insert: half + "KEEP" + half}}})

	answers <- fmt.Sprintf(`{"message": "Rewrote it.", "settings": [], "bodies": [{"variant": "a", "body": %q, "reason": "clearer"}]}`, rewrite+"KEEP"+rewrite)

	large, err := store.ProposeAgentDraftChanges(ctx, draft.ID, "bob", &AgentDraftAsk{Request: "Rewrite it"})
	require.NoError(t, err)
	<-requests

	applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: revision, Author: "carol", Kind: AgentDraftEditBody, Variant: "a", Splices: []AgentDraftSplice{{At: 1500, Remove: 4}}})
	_, err = store.DecideAgentDraftProposal(ctx, draft.ID, large.ID, "bob", true)
	require.NoError(t, err)

	got, err = store.LoadAgentDraft(ctx, draft.ID)
	require.NoError(t, err)
	require.Equal(t, rewrite+rewrite, got.Variants[0].Body)
}

func TestAgentDraftAssistantFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ name, agent, answer, message string }{
		{"missing draft", "---\nmodel: m\n---\n", "{}", "no rows"},
		{"invalid agent", "---\nmodel: [\n---\n", "{}", "open workspace agent and skills"},
		{"main off", "", "{}", "main is turned off in Behavior Settings."},
		{"provider failure", "---\nmodel: m\n---\n", "", "ask agent draft assistant"},
		{"not JSON", "---\nmodel: m\n---\n", "Sure!", "read agent draft assistant answer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.answer == "" {
					http.Error(w, "unavailable", http.StatusBadRequest)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				writeResponseMessage(t, w, "proposal", "message", test.answer)
			}))
			t.Cleanup(server.Close)

			workspace := t.TempDir()
			store := newTestSessionServiceAt(t, workspace)
			store.definitions = config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL},
				EmbeddedAgents: config.EmbeddedAgentsConfig{Main: config.EmbeddedAgentConfig{Disabled: test.name == "main off"}}})

			if test.agent != "" {
				writeAgent(t, workspace, "helper", test.agent)
			}

			draft, err := store.CreateAgentDraft(t.Context(), "alice", AgentDraftOrigin{Name: "bot", Mode: harness.AgentModeEasy})
			require.NoError(t, err)

			if test.name == "missing draft" {
				draft.ID = "missing"
			}

			_, err = store.ProposeAgentDraftChanges(t.Context(), draft.ID, "bob", &AgentDraftAsk{Request: "Help"})
			require.ErrorContains(t, err, test.message)

			proposals, err := store.AgentDraftProposals(t.Context(), draft.ID)
			require.NoError(t, err)
			require.Empty(t, proposals)
		})
	}
}
