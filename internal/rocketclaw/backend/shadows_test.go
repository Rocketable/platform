package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func testDrainInputs(t *testing.T, drain *Bridge, phase rocketcode.TurnPhase) []rocketcode.PromptInput {
	t.Helper()
	// These tests simulate the model applying the preceding drain's payload.
	snapshot, _, err := loadTurnInputs(t.Context(), drain.config.SessionService.db, drain.config.ConversationID, drain.activeTurnID)
	require.NoError(t, err)

	applied := make([]string, 0, len(snapshot.Inputs))
	for i := range snapshot.Inputs {
		applied = append(applied, snapshot.Inputs[i].ID)
	}

	inputs, err := drain.Drain(t.Context(), phase, applied)
	require.NoError(t, err)

	return inputs
}

func newShadowTestStore(t *testing.T) (*SessionService, *os.Root) {
	t.Helper()

	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	for _, name := range []string{"main", "main/a", "main/z"} {
		file := filepath.Join(config.DefaultRuntimeDir, "agents", name+".md")
		require.NoError(t, root.MkdirAll(filepath.Dir(file), 0o700))
		require.NoError(t, root.WriteFile(file, []byte("---\nmodel: gpt-5.5\n---\nPrompt "+name), 0o600))
	}

	return newTestSessionServiceAt(t, workspace), root
}

func TestShadowBindingsCreation(t *testing.T) {
	store, _ := newShadowTestStore(t)
	runtime := &Runtime{Sessions: store}

	var group errgroup.Group
	for range 8 {
		group.Go(func() error {
			return runtime.CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main", CreatedBy: "alice"})
		})
	}

	require.NoError(t, group.Wait())
	require.NoError(t, runtime.CreateConversation(t.Context(), protocol.Conversation{ID: "other", Agent: "main"}))
	bindings, err := queryStrings(t.Context(), store.db, `SELECT parent_conversation_id || ':' || parent_agent || ':' || shadow_agent FROM shadow_bindings ORDER BY 1`, "test shadow bindings")
	require.NoError(t, err)
	require.Equal(t, []string{"other:main:main/a", "other:main:main/z", "parent:main:main/a", "parent:main:main/z"}, bindings)
	children, err := queryStrings(t.Context(), store.db, `SELECT child_conversation_id FROM shadow_bindings ORDER BY 1`, "test shadow children")
	require.NoError(t, err)
	require.Len(t, children, 4)

	for _, child := range children {
		thread, found, err := store.Thread(child)
		require.NoError(t, err)
		require.True(t, found)
		require.Contains(t, []string{"main/a", "main/z"}, thread.Agent)
		require.NoError(t, runtime.CreateConversation(t.Context(), protocol.Conversation{ID: child, Agent: thread.Agent}))
		require.NoError(t, store.UpsertThread(child, ThreadState{Agent: "main"}), "explicit ownership, not the agent name, prevents recursion")
	}

	var count int
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_bindings`).Scan(&count))
	require.Equal(t, 4, count, "a bound child never creates another shadow family")
}

func TestShadowActivationRollback(t *testing.T) {
	for _, kind := range []string{"queue", "schedule", "note"} {
		t.Run(kind, func(t *testing.T) {
			store, _ := newShadowTestStore(t)
			_, err := (stateDAO{db: store.db}).createConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"})
			require.NoError(t, err)

			bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "parent", Agent: "main", SessionService: store}}
			request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "new input", false)}

			switch kind {
			case "queue":
				request.queueItemID = "queued"

				require.NoError(t, store.PutThreadQueueItem("queued", &protocol.ThreadQueueItem{ConversationID: "parent", Message: "new input"}))
			case "schedule":
				request.scheduledMessageID = "scheduled"

				require.NoError(t, store.PutScheduledMessage("scheduled", &protocol.ScheduledMessageState{ConversationID: "parent", Agent: "main", Message: "new input", DueAt: time.Now()}))
			case "note":
				request.backgroundWake = true
				job := testBackgroundJob("parent", "finished")
				createTestBackgroundJob(t, store, job)
				job.status, job.noteState, job.wake = backgroundCompleted, notePending, true
				_, err := store.finishBackgroundJob(t.Context(), job)
				require.NoError(t, err)
			}

			_, err = store.db.ExecContext(t.Context(), `ALTER TABLE active_turns ADD CONSTRAINT reject_activation CHECK (conversation_id <> 'parent')`)
			require.NoError(t, err)
			admitted, err := bridge.activateInbound(t.Context(), &request)
			require.ErrorContains(t, err, "start active turn")
			require.False(t, admitted)
			require.Empty(t, request.turnID)

			for _, table := range []string{"shadow_bindings", "shadow_turns", "active_turns"} {
				var count int
				require.NoError(t, store.db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count))
				require.Zero(t, count, table)
			}

			var count int
			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM managed_conversations`).Scan(&count))
			require.Equal(t, 1, count, "rolled-back attachment leaves no child sessions")

			switch kind {
			case "queue":
				items, err := store.ThreadQueueForConversation("parent")
				require.NoError(t, err)
				require.Len(t, items, 1)
			case "schedule":
				items, err := store.ScheduledMessagesForConversation("parent")
				require.NoError(t, err)
				require.Len(t, items, 1)
			case "note":
				var claim string
				require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT claimed_turn_id FROM background_jobs WHERE conversation_id = 'parent'`).Scan(&claim))
				require.Empty(t, claim)
			}

			_, err = store.db.ExecContext(t.Context(), `ALTER TABLE active_turns DROP CONSTRAINT reject_activation`)
			require.NoError(t, err)
			admitted, err = bridge.activateInbound(t.Context(), &request)
			require.NoError(t, err)
			require.True(t, admitted)
			admitted, err = bridge.activateInbound(t.Context(), &request)
			require.NoError(t, err)
			require.True(t, admitted, "retry of admitted work reuses its manifests")
			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns`).Scan(&count))
			require.Equal(t, 2, count)
		})
	}
}

func TestShadowSeedAndFork(t *testing.T) {
	store, root := newShadowTestStore(t)
	_, err := (stateDAO{db: store.db}).createConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"})
	require.NoError(t, err)

	attachment := protocol.OutboundAttachment{ID: "original", Name: "seed.txt", MIMEType: "text/plain", Data: []byte("seed bytes")}
	require.NoError(t, store.SaveAttachment(t.Context(), "parent", "", &attachment, true))

	entry := testSessionEntry("attachment:original", "seed answer")
	entry.ReplayInput = append(entry.ReplayInput, json.RawMessage(`{"type":"message","role":"user","prompt_header":"[System]","input_id":"finished running","content":"Completion Note"}`))
	_, err = store.AppendEntryID(t.Context(), "parent", entry)
	require.NoError(t, err)
	_, err = store.AppendEntryID(t.Context(), "parent/task", testSessionEntry("delegated", "seed child"))
	require.NoError(t, err)

	for _, id := range []string{"finished", "running"} {
		job := testBackgroundJob("parent", id)
		createTestBackgroundJob(t, store, job)

		if id == "finished" {
			job.status, job.noteState, job.wake, job.result = backgroundCompleted, notePending, true, "seed result"
			_, err = store.finishBackgroundJob(t.Context(), job)
			require.NoError(t, err)
		}
	}

	spill := openTestSpill(t, store)
	file := filepath.Join(rocketcodeRetainedDir("parent"), "result.txt")
	require.NoError(t, spill.MkdirAll(filepath.Dir(file), 0o700))
	require.NoError(t, spill.WriteFile(file, []byte("retained seed"), 0o600))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "parent", Agent: "main", SessionService: store}}
	request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "future", true)}
	admitted, err := bridge.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)
	_, err = store.ForkConversation(t.Context(), "parent", protocol.Conversation{ID: "fork", Agent: "main"}, "")
	require.NoError(t, err)
	children, err := queryStrings(t.Context(), store.db, `SELECT child_conversation_id FROM shadow_bindings ORDER BY 1`, "seed children")
	require.NoError(t, err)
	require.Len(t, children, 4, "fork attaches its own children, not copies of source comparisons")

	for _, child := range children {
		entries, err := store.ObserveEntries(t.Context(), child)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Contains(t, string(entries[0].Entry.ReplayInput[1]), "seed answer")
		files, err := queryStrings(t.Context(), store.db, `SELECT id FROM attachments WHERE conversation_id = $1`, "seed files", child)
		require.NoError(t, err)
		require.Len(t, files, 1)
		require.NotEqual(t, "original", files[0])
		require.Contains(t, string(entries[0].Entry.ReplayInput[0]), files[0])
		copied, err := store.LoadAttachment(t.Context(), child, files[0], true)
		require.NoError(t, err)
		require.Equal(t, attachment.Data, copied.Data)
		delegations, err := store.ObserveEntries(t.Context(), child+"/task")
		require.NoError(t, err)
		require.Len(t, delegations, 1)

		data, err := spill.ReadFile(filepath.Join(rocketcodeRetainedDir(child), "result.txt"))
		require.NoError(t, err)
		require.Equal(t, "retained seed", string(data))

		var count int
		require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM background_jobs WHERE conversation_id = $1 AND note_state = $2 AND NOT wake AND sync_destination = ''`, child, noteConsumed).Scan(&count))
		require.Equal(t, 1, count)
		require.Empty(t, backgroundJobIDs(t, store, child))
		require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM active_turns WHERE conversation_id = $1`, child).Scan(&count))
		require.Zero(t, count, "copying seeds performs no execution")
	}
	// Removed definitions keep existing ownership but do not get new manifests.
	require.NoError(t, root.Remove(filepath.Join(config.DefaultRuntimeDir, "agents", "main", "z.md")))
	endTestTurn(t, store, "parent", request.turnID, "")
	request = bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "next", true)}
	admitted, err = bridge.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)

	var count int
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns WHERE parent_turn_id = $1`, request.turnID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestShadowClaimHead(t *testing.T) {
	store, _ := newShadowTestStore(t)
	require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}))
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "parent", Agent: "main", SessionService: store}}

	for range 2 {
		request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "same text", false)}
		request.inbound.SyncDestination, request.inbound.GoalAction, request.inbound.RequireOutputDecision = "visible", protocol.GoalActionKickoff, true
		request.inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C1", ThreadTS: "1"}
		request.inbound.Metadata = map[string]string{"web_message_id": "parent-input", backgroundNotesMetadataKey: "parent-job", "channel_id": "C1"}
		admitted, err := bridge.activateInbound(t.Context(), &request)
		require.NoError(t, err)
		require.True(t, admitted)
		require.NoError(t, store.captureTurnInputs(t.Context(), "parent", request.turnID, &turnInputSnapshot{Inputs: []rocketcode.PromptInput{{ID: "input", Texts: []string{"canonical input"}}}}))
		endTestTurn(t, store, "parent", request.turnID, "")
	}

	_, err := store.PruneStateBefore(t.Context(), time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, retained, err := store.Thread("parent")
	require.NoError(t, err)
	require.True(t, retained, "pending manifests protect their historyless parent as well as their children")

	var child string
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT child_conversation_id FROM shadow_bindings WHERE shadow_agent = 'main/a'`).Scan(&child))

	inputs, err := json.Marshal([]rocketcode.PromptInput{{ID: "input", Texts: []string{"canonical input"}}})
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `UPDATE shadow_turns SET inputs = '[]' WHERE child_conversation_id = $1`, child)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `UPDATE shadow_turns SET inputs = $2 WHERE child_conversation_id = $1 AND id = (SELECT max(id) FROM shadow_turns WHERE child_conversation_id = $1)`, child, string(inputs))
	require.NoError(t, err)
	_, claimed, err := store.claimShadowTurn(t.Context(), child)
	require.NoError(t, err)
	require.False(t, claimed, "a ready later manifest cannot skip an unprepared head")
	_, err = store.db.ExecContext(t.Context(), `UPDATE shadow_turns SET inputs = $2 WHERE child_conversation_id = $1`, child, string(inputs))
	require.NoError(t, err)

	_, err = store.db.ExecContext(t.Context(), `ALTER TABLE shadow_turns ADD CONSTRAINT reject_claim CHECK (child_turn_id = '')`)
	require.NoError(t, err)
	_, claimed, err = store.claimShadowTurn(t.Context(), child)
	require.ErrorContains(t, err, "claim shadow turn")
	require.False(t, claimed)

	var uncommitted int
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM active_turns WHERE conversation_id = $1`, child).Scan(&uncommitted))
	require.Zero(t, uncommitted, "failed claim rolls back the child history anchor")
	_, err = store.db.ExecContext(t.Context(), `ALTER TABLE shadow_turns DROP CONSTRAINT reject_claim`)
	require.NoError(t, err)

	turns, claims := make([]activeTurn, 8), make([]bool, 8)

	var group errgroup.Group
	for i := range turns {
		group.Go(func() error {
			var err error

			turns[i], claims[i], err = store.claimShadowTurn(t.Context(), child)

			return err
		})
	}

	require.NoError(t, group.Wait())

	var (
		first activeTurn
		count int
	)

	for i, claimed := range claims {
		if claimed {
			first = turns[i]
			count++
		}
	}

	require.Equal(t, 1, count)

	var expected string
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT 'shadow-turn-' || min(id)::text FROM shadow_turns WHERE child_conversation_id = $1`, child).Scan(&expected))
	require.Equal(t, expected, first.id)
	require.Empty(t, first.inbound.SyncDestination)
	require.Empty(t, first.inbound.GoalAction)
	require.Nil(t, first.inbound.SlackReply)
	require.False(t, first.inbound.RequireOutputDecision)
	require.Empty(t, first.inbound.Metadata, "connector routing and Completion Note claims belong only to the parent")
	require.Equal(t, protocol.SourceSystem, first.inbound.Source)

	entry := testSessionEntry("canonical input", "first independent answer")
	entry.TurnID = first.id
	_, err = store.finishTurn(t.Context(), first.id, &turnFinish{store: newSessionStore(child, store), outbound: protocol.NewOutboundMessage(child, "answer"), entries: []rocketcode.SessionEntry{*entry}})
	require.NoError(t, err)
	_, claimed, err = store.claimShadowTurn(t.Context(), child)
	require.NoError(t, err)
	require.False(t, claimed, "delivery is part of settlement")
	require.NoError(t, store.closeTurn(t.Context(), first.id))
	second, claimed, err := store.claimShadowTurn(t.Context(), child)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotEqual(t, first.id, second.id)

	var anchor, last int64
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT history_anchor_id, (SELECT max(id) FROM session_entries WHERE conversation_id = $2) FROM active_turns WHERE id = $1`, second.id, child).Scan(&anchor, &last))
	require.Positive(t, anchor)
	require.Equal(t, last, anchor, "the second anchor includes the settled first child response")

	var saved []byte
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT inputs FROM shadow_turns WHERE child_turn_id = $1`, first.id).Scan(&saved))
	require.JSONEq(t, `[]`, string(saved), "completed payload is released after durable closure and child saving")
}

func TestShadowImplicitCreation(t *testing.T) {
	store, _ := newShadowTestStore(t)
	require.NoError(t, store.UpsertThread("cron:private", ThreadState{Agent: "main", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, store.RegisterExternalMCPConversation("external", "main", &ExternalMCPSessionState{ManagedConversationID: "visible", PrivateConversationID: "mcp:private", Agent: "main"}))

	conversation := protocol.SlackThreadConversationID("C1", "1.1")
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversation, Agent: "main", SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{}), handling: true}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	inbound := newThreadInboundMessage("mention", "1.1", "1.1")
	protocol.SetInboundAllowedAgents(inbound, []string{"main"})
	inbound.SlackReply.ChannelID = "C1"
	accepted, err := manager.SubmitMention(t.Context(), "main", slackTarget("C1", "1.1"), inbound)
	require.NoError(t, err)
	require.True(t, accepted)
	parents, err := queryStrings(t.Context(), store.db, `SELECT parent_conversation_id FROM shadow_bindings GROUP BY 1 HAVING count(*) = 2 ORDER BY 1`, "implicit shadow parents")
	require.NoError(t, err)
	require.Equal(t, []string{"cron:private", conversation, "visible"}, parents)

	var count int
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns`).Scan(&count))
	require.Zero(t, count, "creation and queued admission do not consume model inputs")
}

func TestShadowExecutionOwnerEntrypoints(t *testing.T) {
	for _, entrypoint := range []string{"web", "slack", "cron", "external MCP", "scheduled", "goal continuation", "background wake"} {
		t.Run(entrypoint, func(t *testing.T) {
			store, _ := newShadowTestStore(t)
			visible := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "visible", Agent: "main", SessionService: store}}
			owner := visible
			inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "input", true)
			inbound.Metadata = map[string]string{"web_message_id": "id", "channel_id": "C1"}
			inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C1", ThreadTS: "1"}
			request := bridgeRequest{inbound: inbound}

			switch entrypoint {
			case "slack":
				inbound.Source = protocol.SourceSlack
			case "cron", "external MCP":
				owner = &Bridge{log: visible.log, config: Config{ConversationID: "private", Agent: "main", SessionService: store}}
				request.producer, inbound.SyncDestination = owner, "visible"

				inbound.Source, inbound.RequireOutputDecision = protocol.SourceSystem, true
				if entrypoint == "external MCP" {
					inbound.Source = protocol.SourceExternalMCP
				}
			case "scheduled":
				inbound.Source, inbound.Human, request.scheduledMessageID = protocol.SourceSystem, false, "scheduled"

				require.NoError(t, store.PutScheduledMessage("scheduled", &protocol.ScheduledMessageState{ConversationID: "visible", Agent: "main", Message: "input", DueAt: time.Now()}))
			case "goal continuation":
				inbound.Source, inbound.Human, inbound.GoalAction = protocol.SourceSystem, false, protocol.GoalActionContinue
			case "background wake":
				inbound.Source, inbound.Human, request.backgroundWake = protocol.SourceSystem, false, true
				job := testBackgroundJob("visible", "parent-job")
				createTestBackgroundJob(t, store, job)
				job.status, job.noteState, job.wake = backgroundCompleted, notePending, true
				_, err := store.finishBackgroundJob(t.Context(), job)
				require.NoError(t, err)
			}

			admitted, err := visible.activateInbound(t.Context(), &request)
			require.NoError(t, err)
			require.True(t, admitted)
			admitted, err = visible.activateInbound(t.Context(), &request)
			require.NoError(t, err)
			require.True(t, admitted)
			children, err := queryStrings(t.Context(), store.db, `SELECT child_conversation_id FROM shadow_turns WHERE parent_source_conversation_id = $1 AND parent_turn_id = $2`, "owner manifests", owner.config.ConversationID, request.turnID)
			require.NoError(t, err)
			require.Len(t, children, 2, "the outer execution owner creates exactly one manifest per child")
			require.NoError(t, store.captureTurnInputs(t.Context(), owner.config.ConversationID, request.turnID, &turnInputSnapshot{Inputs: []rocketcode.PromptInput{{ID: "input", Texts: []string{"prepared"}}}}))

			for _, id := range children {
				turn, claimed, err := store.claimShadowTurn(t.Context(), id)
				require.NoError(t, err)
				require.True(t, claimed)
				require.Empty(t, turn.inbound.Metadata)
				require.Empty(t, turn.inbound.SyncDestination)

				publisher := &outboundPublisherMock{PublishOutboundFunc: func(context.Context, *protocol.OutboundMessage) error { return errors.New("connector unavailable") }}
				child := &Bridge{shadow: true, config: Config{ConversationID: id, Agent: "main/a", SessionService: store}, bus: publisher}
				child.publishConsumed(t.Context(), inbound, "parent header")
				require.NoError(t, child.finish(t.Context(), &bridgeRequest{inbound: turn.inbound, turnID: turn.id}, &turnFinish{store: newSessionStore(id, store)}, &runResult{turnID: turn.id, text: "local only"}, ""))
				require.Empty(t, publisher.PublishOutboundCalls(), "connector failures cannot affect local child completion")
				active, err := store.HasActiveTurn(t.Context(), id)
				require.NoError(t, err)
				require.False(t, active)
			}
		})
	}
}

func TestShadowToolConstructionAndRealEffects(t *testing.T) {
	for _, permission := range []string{"allow", "deny"} {
		t.Run(permission, func(t *testing.T) {
			store, root := newShadowTestStore(t)
			require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o700))
			require.NoError(t, root.WriteFile(".rocketclaw/agents/main/a.md", fmt.Appendf(nil, "---\nmodel: gpt-child\npermission:\n  bash: %s\n  rocketclaw: {\"*\": allow}\n---\nChild prompt", permission), 0o600))
			require.NoError(t, root.WriteFile(".rocketclaw/agents/main.md", []byte("---\nmodel: gpt-parent\npermission: {bash: allow}\n---\nParent prompt"), 0o600))

			toolResults := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Input []struct{ Type, Output string } `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}

				w.Header().Set("Content-Type", "application/json")

				for _, input := range request.Input {
					if input.Type == "function_call_output" {
						toolResults <- input.Output

						writeRawRunMessage(t, w, "done", "message", "answer")

						return
					}
				}

				arguments, _ := json.Marshal(struct{ Code, Description string }{`def main():
    return bash(command="printf '%s' \"$ROCKETCLAW_SHADOW\" > effect.txt")
`, "write a real file"})
				_, _ = fmt.Fprintf(w, `{"id":"call","status":"completed","output":[{"id":"fc","type":"function_call","status":"completed","call_id":"write","name":"execute","arguments":%q}]}`, arguments)
			}))
			t.Cleanup(server.Close)
			cfg := config.NewLockedConfig(&config.Config{Workspace: root.Name(), OpenAI: config.OpenAIConfig{APIBaseURL: server.URL, APIKey: "test"}})
			store.definitions = cfg
			parent := &Bridge{runtime: cfg, config: Config{ConversationID: "parent", Agent: "main", SessionService: store}, log: slog.New(slog.DiscardHandler)}
			request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "write", true)}
			admitted, err := parent.activateInbound(t.Context(), &request)
			require.NoError(t, err)
			require.True(t, admitted)
			require.NoError(t, store.captureTurnInputs(t.Context(), "parent", request.turnID, &turnInputSnapshot{Inputs: []rocketcode.PromptInput{{ID: "initial", Texts: []string{"write"}}}, Closed: true}))

			var childID string
			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT child_conversation_id FROM shadow_bindings WHERE shadow_agent = 'main/a'`).Scan(&childID))
			turn, claimed, err := store.claimShadowTurn(t.Context(), childID)
			require.NoError(t, err)
			require.True(t, claimed)

			publisher := &outboundPublisherMock{PublishOutboundFunc: func(context.Context, *protocol.OutboundMessage) error { return errors.New("connector unavailable") }}
			child := NewConversation(cfg, publisher, &Config{ConversationID: childID, Agent: "main/a", SessionService: store}, slog.New(slog.DiscardHandler))
			child.shadow = true

			prohibited := make([]rocketcode.Tool, 0, 7)
			for _, name := range []string{askUserQuestionToolName, startNewThreadToolName, scheduleMessageToolName, resetScheduledMessagesToolName, updateGoalToolName, reloadToolName, restartToolName} {
				prohibited = append(prohibited, rocketcode.Tool{Name: name})
			}

			parentConfig := parent.rocketcodeConfig(root.Name(), nil, prohibited...)
			require.NotContains(t, parentConfig.ShellEnv, "ROCKETCLAW_SHADOW")

			childConfig := child.rocketcodeConfig(root.Name(), nil, prohibited...)
			require.Equal(t, "main/a", childConfig.ShellEnv["ROCKETCLAW_SHADOW"])

			capturedEnv := map[string]string{rocketclawConversationIDEnv: "parent", "ROCKETCLAW_METADATA_FRUIT": "banana"}
			childConfig = child.rocketcodeConfig(root.Name(), capturedEnv, prohibited...)
			require.Equal(t, "main/a", childConfig.ShellEnv["ROCKETCLAW_SHADOW"])
			require.Equal(t, childID, childConfig.ShellEnv[rocketclawConversationIDEnv])
			require.Equal(t, "banana", childConfig.ShellEnv["ROCKETCLAW_METADATA_FRUIT"])
			require.Equal(t, "parent", capturedEnv[rocketclawConversationIDEnv])
			require.NotContains(t, capturedEnv, "ROCKETCLAW_SHADOW", "shadow environment must not mutate captured parent inputs")

			for _, tool := range prohibited {
				require.True(t, slices.ContainsFunc(parentConfig.CustomTools, func(item rocketcode.Tool) bool { return item.Name == tool.Name }))
				require.False(t, slices.ContainsFunc(childConfig.CustomTools, func(item rocketcode.Tool) bool { return item.Name == tool.Name }))
			}

			require.IsType(t, rocketcode.InertBackgroundJobs{}, childConfig.BackgroundJobs)
			result, err := runTestTurn(t.Context(), child, turn.inbound, turn.id)
			require.NoError(t, err)
			require.Equal(t, "answer", result.text)
			require.Empty(t, publisher.PublishOutboundCalls())

			output := <-toolResults

			data, err := root.ReadFile("effect.txt")
			if permission == "allow" {
				require.NoError(t, err)
				require.Equal(t, "main/a", string(data))
			} else {
				require.ErrorIs(t, err, os.ErrNotExist)
				require.Contains(t, output, "bash")
			}
		})
	}
}

func TestShadowConsumedBatchIsAtomic(t *testing.T) {
	store, _ := newShadowTestStore(t)
	parent := &Bridge{bus: discardPublisher{}, log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "parent", Agent: "main", SessionService: store}}
	request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "start", true)}
	admitted, err := parent.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)

	parent.activeTurnID, parent.inputOpen, parent.notesOpen = request.turnID, true, true
	job := testBackgroundJob("parent", "note")
	createTestBackgroundJob(t, store, job)
	job.status, job.noteState, job.result = backgroundCompleted, notePending, "finished result"
	_, err = store.finishBackgroundJob(t.Context(), job)
	require.NoError(t, err)

	parent.movedNotes = []rocketcode.PromptInput{systemPromptInput("moved", "moved notice")}
	steer := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindSteer, "$docs-helper \"typed args\"  Next\t", true)
	steer.PreserveWhitespace = true
	completion := &turnCompletion{done: make(chan struct{})}
	parent.steers = []bridgeRequest{{queueItemID: "s1", inbound: steer, completion: completion}}
	_, err = store.db.ExecContext(t.Context(), `ALTER TABLE shadow_turns ADD CONSTRAINT reject_fanout CHECK (inputs = '[]'::jsonb)`)
	require.NoError(t, err)
	_, err = parent.Drain(t.Context(), rocketcode.TurnPhaseFinalAnswer, nil)
	require.Error(t, err, "fan-out failure must stop parent consumption")
	require.Zero(t, parent.steersRead)
	require.Empty(t, parent.notesOffered)
	require.Len(t, parent.movedNotes, 1)
	require.True(t, parent.inputOpen)
	require.Error(t, parent.settleSteers(t.Context(), err))

	select {
	case <-completion.done:
		t.Fatal("failed consumption completed an accepted steer")
	default:
	}

	require.Len(t, parent.steers, 1, "retry retains the live steer observer")
	parent.activeTurnID, parent.inputOpen, parent.notesOpen = request.turnID, true, true

	var claim string
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT claimed_turn_id FROM background_jobs WHERE job_id = 'note'`).Scan(&claim))
	require.Empty(t, claim)
	_, err = store.db.ExecContext(t.Context(), `ALTER TABLE shadow_turns DROP CONSTRAINT reject_fanout`)
	require.NoError(t, err)

	parent.threads = &threadBridgeManager{bridges: map[string]directBridge{}}
	inputs, err := parent.Drain(t.Context(), rocketcode.TurnPhaseFinalAnswer, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"note", "moved", "s1"}, []string{inputs[0].ID, inputs[1].ID, inputs[2].ID})
	require.Equal(t, buildPrompt(steer, nil), inputs[2].Texts[0])
	require.Equal(t, &rocketcode.PromptInputDirectSkill{Name: "docs-helper", Arguments: "\"typed args\"  Next\t"}, inputs[2].DirectSkill)

	var mirrored []byte
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT inputs FROM shadow_turns LIMIT 1`).Scan(&mirrored))

	data, err := json.Marshal(inputs)
	require.NoError(t, err)
	require.JSONEq(t, string(data), string(mirrored))
	inputs, err = parent.Drain(t.Context(), rocketcode.TurnPhaseFinalAnswer, []string{"note", "moved", "s1"})
	require.NoError(t, err)
	require.Empty(t, inputs)
	require.False(t, parent.inputOpen)

	var closed bool
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT bool_and(input_closed) FROM shadow_turns`).Scan(&closed))
	require.True(t, closed)
	// Overflow compaction can remove S1 from replay. Queue ownership must be
	// released from journaled application, not from surviving prompt text.
	for _, id := range []string{"s1", "waiting"} {
		require.NoError(t, store.PutThreadQueueItem(id, &protocol.ThreadQueueItem{ConversationID: "parent", Message: id}))
	}

	require.NoError(t, store.SaveTurnStep(t.Context(), "parent", request.turnID, json.RawMessage(`{"applied_input_ids":["s1"],"record":{"version":1,"type":"turn","replay_input":[{"type":"compaction","id":"cmp","encrypted_content":"summary"}]}}`)))
	entry := rocketcode.SessionEntry{Version: 1, Type: "turn", TurnID: request.turnID, ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"compaction","id":"cmp","encrypted_content":"summary"}`)}}
	_, err = store.finishTurn(t.Context(), request.turnID, &turnFinish{store: newSessionStore("parent", store), outbound: protocol.NewOutboundMessage("parent", "answer"), entries: []rocketcode.SessionEntry{entry}})
	require.NoError(t, err)
	waiting, err := store.ThreadQueueForConversation("parent")
	require.NoError(t, err)
	require.Len(t, waiting, 1)
	require.Equal(t, "waiting", waiting[0].ID)
}

func TestShadowLiveFinalDrain(t *testing.T) {
	store, root := newShadowTestStore(t)
	require.NoError(t, root.Remove(filepath.Join(config.DefaultRuntimeDir, "agents", "main", "z.md")))
	require.NoError(t, root.MkdirAll(filepath.Join(config.DefaultRuntimeDir, "skills"), 0o700))

	for _, name := range []string{"main", "main/a"} {
		frame := "Parent frame."
		model := "gpt-parent"

		if name != "main" {
			frame = "Child frame must not replace parent."
			model = "gpt-child"
		}

		require.NoError(t, root.WriteFile(filepath.Join(config.DefaultRuntimeDir, "agents", name+".md"), fmt.Appendf(nil, "---\nmodel: %s\nadditionalInstructions: %s\npermission: {}\n---\nSystem %s", model, frame, name), 0o600))
	}

	type providerRequest struct {
		Model        string            `json:"model"`
		Input        []json.RawMessage `json:"input"`
		Instructions string            `json:"instructions"`
	}

	requests := make(chan providerRequest, 8)
	parentRelease, childRelease := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request providerRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}

		requests <- request

		if request.Model == "gpt-parent" && len(request.Input) == 1 {
			select {
			case <-parentRelease:
			case <-r.Context().Done():
				return
			}
		}

		if request.Model == "gpt-child" && len(request.Input) > 1 {
			select {
			case <-childRelease:
			case <-r.Context().Done():
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp-%s","status":"completed","output":[{"type":"message","id":"msg","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer %s"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, request.Model, request.Model)
	}))
	t.Cleanup(server.Close)
	cfg := config.NewLockedConfig(&config.Config{Workspace: root.Name(), OpenAI: config.OpenAIConfig{APIBaseURL: server.URL, APIKey: "test"}})
	store.definitions = cfg
	finals := make(chan *protocol.OutboundMessage, 8)
	events := make(chan *protocol.OutboundMessage, 32)
	publisher := &outboundPublisherMock{PublishOutboundFunc: func(_ context.Context, message *protocol.OutboundMessage) error {
		events <- message

		if message.ConversationID != "parent" {
			return errors.New("connector refuses child output")
		}

		message.MarkDelivered(nil)

		if message.Complete {
			finals <- message
		}

		return nil
	}}
	manager := newThreadBridgeManager(cfg, store, slog.New(slog.DiscardHandler), func(bridgeCfg Config) directBridge {
		bridgeCfg.SessionService = store
		return NewConversation(cfg, publisher, &bridgeCfg, slog.New(slog.DiscardHandler))
	})
	shutdown := runTestManager(t, manager)
	t.Cleanup(func() { require.NoError(t, shutdown()) })
	require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}))

	parent, err := manager.recordedBridge(t.Context(), "parent")
	require.NoError(t, err)

	initial := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "  same text  ", true)
	initial.PreserveWhitespace, initial.Metadata = true, map[string]string{"web_message_id": "initial"}
	initial.Metadata[protocol.InboundPrincipalMetadataKey] = "Alice"
	initial.AttachmentWarnings = []string{"skipped attachment"}
	initial.Attachments = []protocol.InboundAttachment{{Name: "image.png", MIMEType: "image/png", Data: tinyPNG()}}
	require.NoError(t, parent.Submit(t.Context(), initial))

	var first []providerRequest
	for len(first) < 2 {
		select {
		case request := <-requests:
			first = append(first, request)
		case <-time.After(3 * time.Second):
			t.Fatal("parent and child did not both start from captured input")
		}
	}

	require.JSONEq(t, string(first[0].Input[0]), string(first[1].Input[0]))
	require.Contains(t, string(first[1].Input[0]), "Parent frame.")
	require.Contains(t, string(first[1].Input[0]), "Alice")
	require.Contains(t, string(first[1].Input[0]), "skipped attachment")
	require.Contains(t, string(first[1].Input[0]), "data:image/png;base64,")
	require.NotEqual(t, first[0].Instructions, first[1].Instructions)

	var childID string
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT child_conversation_id FROM shadow_bindings`).Scan(&childID))
	manager.mu.Lock()
	child := manager.bridges[childID].(*Bridge)
	manager.mu.Unlock()
	require.Eventually(t, func() bool { child.mu.Lock(); defer child.mu.Unlock(); return child.shadowInputWake != nil }, 3*time.Second, time.Millisecond)

	steer := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindSteer, "  same text  ", true)
	steer.PreserveWhitespace, steer.Metadata = true, map[string]string{"web_message_id": "s1"}
	require.NoError(t, parent.Submit(t.Context(), steer))
	close(parentRelease)

	var late []providerRequest
	for len(late) < 2 {
		late = append(late, <-requests)
	}

	require.Len(t, late[0].Input, 3)
	require.Len(t, late[1].Input, 3)
	require.JSONEq(t, string(late[0].Input[2]), string(late[1].Input[2]))

	select {
	case outbound := <-finals:
		require.Equal(t, "parent", outbound.ConversationID)
	case <-time.After(3 * time.Second):
		t.Fatal("parent delivery waited for the child's response")
	}

	close(childRelease)
	require.Eventually(t, func() bool {
		active, err := store.HasActiveTurn(context.Background(), childID)
		return err == nil && !active
	}, 3*time.Second, time.Millisecond)
	require.Empty(t, finals, "child finals must stay local")

	for len(events) > 0 {
		require.Equal(t, "parent", (<-events).ConversationID, "all child events, not just finals, must stay local")
	}

	entries, err := store.ObserveEntries(t.Context(), childID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Contains(t, string(entries[0].Entry.ReplayInput[0]), `"input_id":"initial"`)
	require.Contains(t, string(entries[0].Entry.ReplayInput[2]), `"input_id":"s1"`)
}

func TestShadowFirstReconciliationClear(t *testing.T) {
	store, _ := newShadowTestStore(t)
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	require.NoError(t, lockSessionHistory(t.Context(), tx, "parent"))

	inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "first", true)
	require.NoError(t, store.startShadowManifests(t.Context(), tx, "parent", "main", "first-turn", inbound))
	require.NoError(t, saveTurnInputs(t.Context(), tx, "parent", "first-turn", &turnInputSnapshot{Inputs: []rocketcode.PromptInput{{ID: "initial", Texts: []string{"first"}}}, Closed: true}))

	var holder int
	require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT pg_backend_pid()`).Scan(&holder))

	var clearing errgroup.Group
	clearing.Go(func() error { _, err := store.DeleteSession(t.Context(), "parent"); return err })

	defer func() { _ = tx.Rollback(); require.NoError(t, clearing.Wait()) }()

	require.Eventually(t, func() bool {
		var blocked bool
		require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, holder).Scan(&blocked))

		return blocked
	}, 3*time.Second, time.Millisecond, "clear waits behind the actual first admission transaction")
	require.NoError(t, tx.Commit())
	require.NoError(t, clearing.Wait())

	var count int
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns`).Scan(&count))
	require.Zero(t, count, "newly committed children must not escape history clear")
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_bindings`).Scan(&count))
	require.Equal(t, 2, count, "history clear retains the first admitted bindings")

	bridge := &Bridge{config: Config{ConversationID: "parent", Agent: "main", SessionService: store}, log: store.log}
	request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "next", true)}
	admitted, err := bridge.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns WHERE parent_turn_id = $1`, request.turnID).Scan(&count))
	require.Equal(t, 2, count, "future input reaches the retained first bindings")
}

func TestShadowLiveHistoryMutation(t *testing.T) {
	for _, mutation := range []string{"revert", "history clear", "external MCP removal"} {
		t.Run(mutation, func(t *testing.T) {
			store, root := newShadowTestStore(t)
			require.NoError(t, root.Remove(".rocketclaw/agents/main/z.md"))
			require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o700))

			for _, item := range []struct{ name, model string }{{"main", "gpt-parent"}, {"main/a", "gpt-child"}} {
				require.NoError(t, root.WriteFile(".rocketclaw/agents/"+item.name+".md", fmt.Appendf(nil, "---\nmodel: %s\npermission: {}\n---\nSystem %s", item.model, item.name), 0o600))
			}

			requests := make(chan string, 8)

			firstChild := make(chan struct{}, 1)
			firstChild <- struct{}{}

			cancelled := make(chan bool, 2)

			firstParent := make(chan struct{}, 1)
			if mutation == "external MCP removal" {
				firstParent <- struct{}{}
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}

				requests <- request.Model

				if request.Model == "gpt-parent" {
					select {
					case <-firstParent:
						<-r.Context().Done()

						cancelled <- true

						return
					default:
					}
				}

				if request.Model == "gpt-child" {
					select {
					case <-firstChild:
						<-r.Context().Done()

						var stopped bool

						err := store.db.QueryRowContext(t.Context(), `SELECT COALESCE(bool_and(terminal = $1 AND input_closed), TRUE) FROM shadow_turns`, protocol.TerminalStopped).Scan(&stopped)
						cancelled <- err == nil && stopped

						return
					default:
					}
				}

				w.Header().Set("Content-Type", "application/json")
				writeRawRunMessage(t, w, "response", "message", "answer")
			}))
			t.Cleanup(server.Close)
			cfg := config.NewLockedConfig(&config.Config{Workspace: root.Name(), OpenAI: config.OpenAIConfig{APIBaseURL: server.URL, APIKey: "test"}})
			store.definitions = cfg

			parentID, ownerID := "parent", "parent"
			if mutation == "external MCP removal" {
				parentID = "0parent" // The destination sorts before the producer: interruption must not wait serially.
				ownerID = "external_mcp:private"
				require.NoError(t, store.RegisterExternalMCPConversation("external", "main", &ExternalMCPSessionState{ManagedConversationID: parentID, PrivateConversationID: ownerID, Agent: "main"}))
				require.NoError(t, store.UpsertThread(ownerID, ThreadState{Agent: "main"}))
			} else {
				require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}))
			}

			finals := make(chan *protocol.OutboundMessage, 8)
			manager, shutdown := newTestBridgeManager(t, cfg, store, finals)
			t.Cleanup(func() { require.NoError(t, shutdown()) })

			store.historyWorkers = manager
			parent, err := manager.recordedBridge(t.Context(), parentID)
			require.NoError(t, err)

			if mutation == "external MCP removal" {
				producer, err := manager.recordedBridge(t.Context(), ownerID)
				require.NoError(t, err)

				inbound := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "original", false)
				inbound.SyncDestination = parentID
				require.NoError(t, parent.enqueue(t.Context(), &bridgeRequest{inbound: inbound, producer: producer}, "test external MCP producer"))
			} else {
				require.NoError(t, parent.Submit(t.Context(), protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "original", true)))
			}

			for range 2 {
				select {
				case <-requests:
				case <-time.After(3 * time.Second):
					t.Fatal("parent and child did not start")
				}
			}

			if mutation != "external MCP removal" {
				readFinal(t, finals)
			}

			var childID string
			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT child_conversation_id FROM shadow_bindings WHERE parent_conversation_id = $1`, ownerID).Scan(&childID))

			if mutation == "external MCP removal" {
				require.NoError(t, store.SaveAttachment(t.Context(), childID, "", &protocol.OutboundAttachment{ID: "live-child-blob", Name: "result", Data: []byte("bytes")}, false))
				require.NoError(t, store.RemoveExternalMCPConversation("external"))

				for range 2 {
					require.True(t, <-cancelled)
				}

				for _, table := range []string{"managed_conversations", "active_turns", "shadow_turns", "turn_steps", "session_entries", "attachments", "external_mcp_sessions", "pending_attachment_deletions"} {
					var count int
					require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&count))
					require.Zero(t, count, table+" must not survive live family removal")
				}

				_, err = store.attachments.Get(t.Context(), "live-child-blob")
				require.ErrorIs(t, err, fs.ErrNotExist)

				for _, id := range []string{ownerID, parentID} {
					final := readFinal(t, finals)
					require.Equal(t, id, final.ConversationID, "only the producer stop and its normal destination projection are delivered")
					require.Equal(t, protocol.TerminalStopped, final.Terminal)
					require.Empty(t, final.Text)
					require.Empty(t, final.Attachments)
				}

				require.NoError(t, shutdown())
				manager, shutdown = newTestBridgeManager(t, cfg, store, finals)
				store.historyWorkers = manager
				require.NoError(t, manager.StartActiveTurns(t.Context()))
				require.Empty(t, requests, "removed producers and children must not restart")
				require.Empty(t, finals, "stopped work must not deliver after removal")

				return
			}

			entries, err := store.ObserveEntries(t.Context(), "parent")
			require.NoError(t, err)
			require.Len(t, entries, 1)

			rt := &Runtime{Sessions: store, threads: manager}
			if mutation == "revert" {
				_, _, err = rt.StageRevert(t.Context(), "parent", fmt.Sprintf("%d:0", entries[0].ID))
			} else {
				_, err = store.DeleteSession(t.Context(), "parent")
			}

			require.NoError(t, err)
			require.True(t, <-cancelled, "interrupted manifests are stopped or already cleared")
			active, err := store.HasActiveTurn(t.Context(), childID)
			require.NoError(t, err)
			require.False(t, active, "mutation returns only after the live child settles")
			entries, err = store.ObserveEntries(t.Context(), childID)
			require.NoError(t, err)
			require.Empty(t, entries, "settlement must not write a visible discarded suffix")

			if mutation == "revert" {
				require.NoError(t, rt.ClearRevert(t.Context(), "parent"))
			}

			require.NoError(t, shutdown())
			manager, shutdown = newTestBridgeManager(t, cfg, store, finals)
			store.historyWorkers = manager
			require.NoError(t, manager.StartActiveTurns(t.Context()))
			require.Empty(t, requests, "redo and restart must not repeat stopped work")

			parent, err = manager.recordedBridge(t.Context(), "parent")
			require.NoError(t, err)
			require.NoError(t, parent.Submit(t.Context(), protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "next", true)))
			readFinal(t, finals)

			for range 2 {
				select {
				case <-requests:
				case <-time.After(3 * time.Second):
					t.Fatal("retained binding did not receive future input")
				}
			}

			require.Eventually(t, func() bool {
				active, err := store.HasActiveTurn(t.Context(), childID)
				return err == nil && !active
			}, 3*time.Second, time.Millisecond)
			require.Empty(t, finals, "child settlement stays local")
		})
	}
}

func TestShadowWorkflowCapturedSource(t *testing.T) {
	for _, mutation := range []string{"change", "delete"} {
		t.Run(mutation, func(t *testing.T) {
			store, root := newShadowTestStore(t)
			require.NoError(t, root.Remove(".rocketclaw/agents/main/z.md"))
			require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o700))
			require.NoError(t, root.MkdirAll(".rocketclaw/workflows", 0o700))

			source := `meta = {"name": "audit", "description": "Captured audit"}
review = worker(name = "review", instructions = "Explicit review", model = "review", tools = ["execute"])
def main(args):
    base = agent(args)
    checked = agent(args, worker = review, schema = {"type": "object", "properties": {"ok": {"type": "boolean"}}, "required": ["ok"]})
    return base + str(checked["ok"])
`
			require.NoError(t, root.WriteFile(".rocketclaw/workflows/audit.star", []byte(source), 0o600))

			for _, name := range []string{"main", "main/a"} {
				model := "gpt-main"
				if name != "main" {
					model = "gpt-shadow-a"
				}

				require.NoError(t, root.WriteFile(".rocketclaw/agents/"+name+".md", fmt.Appendf(nil, "---\nmodel: %s\npermission: {bash: allow}\n---\nSystem %s", model, name), 0o600))
			}

			type providerRequest struct {
				Model        string            `json:"model"`
				Instructions string            `json:"instructions"`
				Input        []json.RawMessage `json:"input"`
				Text         json.RawMessage   `json:"text"`
			}

			requests := make(chan providerRequest, 8)
			shellOutputs := make(chan string, 4)

			interruptParent := make(chan struct{}, 1)
			interruptParent <- struct{}{}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request providerRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}

				for _, input := range request.Input {
					var entry struct{ Type, Output string }
					if err := json.Unmarshal(input, &entry); err != nil {
						t.Error(err)
						return
					}

					if entry.Type == "function_call_output" {
						shellOutputs <- entry.Output

						text := "base"
						if request.Model == "gpt-review" {
							text = `{"ok":true}`
						}

						w.Header().Set("Content-Type", "application/json")
						writeRawRunMessage(t, w, "response", "message", text)

						return
					}
				}

				requests <- request

				if request.Model == "gpt-main" {
					select {
					case <-interruptParent:
						<-r.Context().Done()
						return
					default:
					}
				}

				w.Header().Set("Content-Type", "application/json")

				arguments, _ := json.Marshal(struct{ Code, Description string }{`def main():
    return bash(command="printf 'shadow=%s' \"$ROCKETCLAW_SHADOW\"")
`, "read shadow identity"})
				_, _ = fmt.Fprintf(w, `{"id":"call","status":"completed","output":[{"id":"fc","type":"function_call","status":"completed","call_id":"environment","name":"execute","arguments":%q}]}`, arguments)
			}))
			t.Cleanup(server.Close)
			cfg := config.NewLockedConfig(&config.Config{Workspace: root.Name(), Models: map[string]string{"review": "gpt-review"}, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL, APIKey: "test"}})
			store.definitions = cfg
			require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}))

			var childID string
			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT child_conversation_id FROM shadow_bindings`).Scan(&childID))
			// An earlier unprepared head makes this child lag without delaying the parent.
			_, err := store.db.ExecContext(t.Context(), `INSERT INTO shadow_turns (child_conversation_id, parent_source_conversation_id, parent_turn_id, inbound_json) VALUES ($1, 'parent', 'lag', '{}')`, childID)
			require.NoError(t, err)

			finals := make(chan *protocol.OutboundMessage, 8)
			manager, shutdown := newTestBridgeManager(t, cfg, store, finals)
			t.Cleanup(func() { require.NoError(t, shutdown()) })

			parent, err := manager.recordedBridge(t.Context(), "parent")
			require.NoError(t, err)

			inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "$workflow audit original args", true)
			inbound.Workflow = protocol.WorkflowInvocation{Name: "audit", Args: "original args"}
			require.NoError(t, parent.Submit(t.Context(), inbound))
			require.Equal(t, "gpt-main", (<-requests).Model)
			require.NoError(t, shutdown())
			require.Empty(t, finals, "shutdown does not deliver unfinished workflow output")

			if mutation == "delete" {
				require.NoError(t, root.Remove(".rocketclaw/workflows/audit.star"))
			} else {
				require.NoError(t, root.WriteFile(".rocketclaw/workflows/audit.star", []byte("meta = {\"name\": \"audit\", \"description\": \"Changed\"}\ndef main(args): return \"WRONG\"\n"), 0o600))
			}

			manager, shutdown = newTestBridgeManager(t, cfg, store, finals)
			require.NoError(t, manager.StartActiveTurns(t.Context()))
			final := readFinal(t, finals)
			require.Equal(t, "baseTrue", final.Text, "parent recovery recompiles captured source too")

			for range 2 {
				<-requests
				require.Equal(t, "shadow=", <-shellOutputs, "parent workflow has no shadow identity")
			}

			require.Eventually(t, func() bool {
				active, err := store.HasActiveTurn(t.Context(), "parent")
				return err == nil && !active
			}, 3*time.Second, time.Millisecond)
			require.NoError(t, shutdown())
			_, err = store.db.ExecContext(t.Context(), `DELETE FROM shadow_turns WHERE parent_turn_id = 'lag'`)
			require.NoError(t, err)
			manager, shutdown = newTestBridgeManager(t, cfg, store, finals)
			require.NoError(t, manager.StartActiveTurns(t.Context()))

			var base providerRequest
			select {
			case base = <-requests:
			case <-time.After(3 * time.Second):
				t.Fatal("saved workflow did not start the lagging child")
			}

			require.Equal(t, "gpt-shadow-a", base.Model)
			require.Contains(t, base.Instructions, "System main/a")
			require.Contains(t, string(base.Input[0]), "original args")

			review := <-requests
			require.Equal(t, "gpt-review", review.Model)
			require.Contains(t, review.Instructions, "Explicit review")
			require.Contains(t, string(review.Text), "json_schema")

			for range 2 {
				require.Equal(t, "shadow=main/a", <-shellOutputs, "base and explicit workflow workers retain the owning shadow identity")
			}

			require.Eventually(t, func() bool {
				entries, err := store.ObserveEntries(t.Context(), childID)
				return err == nil && len(entries) == 1
			}, 3*time.Second, time.Millisecond)
			require.Empty(t, finals)

			var count int
			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns`).Scan(&count))
			require.Equal(t, 1, count, "workflow worker calls do not create outer manifests")
		})
	}
}

func TestShadowAdmissionPinsAgent(t *testing.T) {
	store, root := newShadowTestStore(t)
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o700))
	require.NoError(t, root.WriteFile(".rocketclaw/agents/other.md", []byte("---\nmodel: gpt-other\npermission: {}\n---\nOther prompt"), 0o600))

	requests := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}

		requests <- request.Model

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", "answer")
	}))
	t.Cleanup(server.Close)
	cfg := config.NewLockedConfig(&config.Config{Workspace: root.Name(), OpenAI: config.OpenAIConfig{APIBaseURL: server.URL, APIKey: "test"}})
	store.definitions = cfg
	require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}))
	manager, shutdown := newTestBridgeManager(t, cfg, store, make(chan *protocol.OutboundMessage, 8))
	t.Cleanup(func() { require.NoError(t, shutdown()) })

	bridge, err := manager.recordedBridge(t.Context(), "parent")
	require.NoError(t, err)

	request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "admitted", true)}
	admitted, err := bridge.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)
	bridge.SwitchAgent("other")
	// Recovery reconstructs the request from durable state, not the admitted Go value.
	head, found, err := store.headTurn(t.Context(), "parent")
	require.NoError(t, err)
	require.True(t, found)
	result, err := runTestTurn(t.Context(), bridge, head.inbound, head.id)
	require.NoError(t, err)
	require.Equal(t, "main", result.attribution.Agent)
	require.Equal(t, "gpt-5.5", <-requests)
}

func TestShadowSiblingFailureIsIndependent(t *testing.T) {
	store, root := newShadowTestStore(t)
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o700))

	for _, item := range []struct{ name, model string }{{"main", "gpt-parent"}, {"main/a", "gpt-good"}, {"main/z", "gpt-fail"}} {
		require.NoError(t, root.WriteFile(".rocketclaw/agents/"+item.name+".md", fmt.Appendf(nil, "---\nmodel: %s\npermission: {}\n---\nSystem %s", item.model, item.name), 0o600))
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}

		if request.Model == "gpt-fail" {
			http.Error(w, "provider failure", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", "answer "+request.Model)
	}))
	t.Cleanup(server.Close)
	cfg := config.NewLockedConfig(&config.Config{Workspace: root.Name(), OpenAI: config.OpenAIConfig{APIBaseURL: server.URL, APIKey: "test"}})
	store.definitions = cfg
	require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}))

	finals := make(chan *protocol.OutboundMessage, 8)
	manager, shutdown := newTestBridgeManager(t, cfg, store, finals)
	t.Cleanup(func() { require.NoError(t, shutdown()) })

	parent, err := manager.recordedBridge(t.Context(), "parent")
	require.NoError(t, err)
	require.NoError(t, parent.Submit(t.Context(), protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "same", true)))
	require.Equal(t, "answer gpt-parent", readFinal(t, finals).Text)

	var good, failed string
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT child_conversation_id FROM shadow_bindings WHERE shadow_agent = 'main/a'`).Scan(&good))
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT child_conversation_id FROM shadow_bindings WHERE shadow_agent = 'main/z'`).Scan(&failed))
	require.Eventually(t, func() bool {
		var settled int

		err := store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns s WHERE child_conversation_id IN ($1, $2) AND child_turn_id <> '' AND NOT EXISTS (SELECT 1 FROM active_turns a WHERE a.id = s.child_turn_id AND a.phase <> $3)`, good, failed, turnDone).Scan(&settled)

		return err == nil && settled == 2
	}, 3*time.Second, time.Millisecond)
	entries, err := store.ObserveEntries(t.Context(), good)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Contains(t, string(entries[0].Entry.ReplayInput[len(entries[0].Entry.ReplayInput)-1]), "answer gpt-good")

	var terminal protocol.Terminal
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT terminal FROM active_turns WHERE conversation_id = $1`, failed).Scan(&terminal))
	require.Equal(t, protocol.TerminalFailed, terminal)
	require.Empty(t, finals, "the failed child's error is local too")
}

func TestShadowCanonicalCaptureRecovery(t *testing.T) {
	store, root := newShadowTestStore(t)
	require.NoError(t, root.Remove(filepath.Join(config.DefaultRuntimeDir, "agents", "main", "z.md")))
	require.NoError(t, root.MkdirAll(filepath.Join(config.DefaultRuntimeDir, "skills"), 0o700))

	requests := make(chan []json.RawMessage, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}

		requests <- request.Input

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp","status":"completed","output":[{"type":"message","id":"msg","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(server.Close)
	cfg := config.NewLockedConfig(&config.Config{Workspace: root.Name(), OpenAI: config.OpenAIConfig{APIBaseURL: server.URL, APIKey: "test"}})
	store.definitions = cfg
	finals := make(chan *protocol.OutboundMessage, 4)
	manager := newThreadBridgeManager(cfg, store, slog.New(slog.DiscardHandler), func(bridgeCfg Config) directBridge {
		bridgeCfg.SessionService, bridgeCfg.RequestRestart, bridgeCfg.StartNewThread = store, testNoopRestart, testNoopStartNewThread
		return NewConversation(cfg, finalsPublisher{finals: finals}, &bridgeCfg, slog.New(slog.DiscardHandler))
	})
	parent, err := manager.ensureStartedThread(t.Context(), &threadStart{conversationID: "parent", agent: "main"})
	require.NoError(t, err)

	request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "raw admission must not be used", true)}
	admitted, err := parent.(*Bridge).activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)

	prepared := rocketcode.PromptInput{ID: "initial", TurnID: request.turnID, Texts: []string{"[Web principal=\"Alice\" additional_instructions=\"old frame\"]\n\n  exact captured text  "}, Header: "old header", Attachments: []rocketcode.Attachment{{MIME: "image/png", Filename: "image.png", URL: "data:image/png;base64,aW1hZ2U="}}}
	note := replayInputForMessage("developer", "captured goal note")

	environment := externalMCPMetadataEnv("parent", map[string]string{"ticket": "original"})
	base := replayInputForMessage("developer", externalMCPMetadataDeveloperMessage("This external MCP thread has metadata:", environment))

	snapshot := turnInputSnapshot{Inputs: []rocketcode.PromptInput{prepared}, ContextEntries: []rocketcode.SessionEntry{{Version: 1, Type: "goal_state", ReplayInput: note}}, ChildContext: []rocketcode.SessionEntry{{Version: 1, ReplayInput: base}}, Environment: environment}
	_, err = store.db.ExecContext(t.Context(), `ALTER TABLE shadow_turns ADD CONSTRAINT reject_capture CHECK (inputs = '[]'::jsonb)`)
	require.NoError(t, err)
	err = store.captureTurnInputs(t.Context(), "parent", request.turnID, &snapshot)
	_, persistence := errors.AsType[inputPersistenceError](err)
	require.True(t, persistence)
	_, captured, err := loadTurnInputs(t.Context(), store.db, "parent", request.turnID)
	require.NoError(t, err)
	require.False(t, captured, "fan-out rollback includes the parent journal")
	_, err = store.db.ExecContext(t.Context(), `ALTER TABLE shadow_turns DROP CONSTRAINT reject_capture`)
	require.NoError(t, err)
	require.NoError(t, store.captureTurnInputs(t.Context(), "parent", request.turnID, &snapshot))
	// Change the definition after capture, before either loop journals the input.
	require.NoError(t, root.WriteFile(filepath.Join(config.DefaultRuntimeDir, "agents", "main.md"), []byte("---\nmodel: gpt-5.5\nadditionalInstructions: new frame\npermission: {}\n---\nnew system"), 0o600))

	changed := replayInputForMessage("developer", "changed metadata must not replace captured metadata")
	_, err = store.AppendEntryID(t.Context(), "parent", &rocketcode.SessionEntry{Version: 1, Type: externalMCPMetadataEntryType, ReplayInput: changed})
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `ALTER TABLE shadow_turns ADD CONSTRAINT reject_close CHECK (NOT input_closed)`)
	require.NoError(t, err)

	request.completion = &turnCompletion{done: make(chan struct{})}
	parent.(*Bridge).handle(t.Context(), &request)
	require.Empty(t, finals, "failed closure cannot deliver the parent final")

	select {
	case <-request.completion.done:
		t.Fatal("failed closure completed the caller")
	default:
	}

	turn, found, err := store.headTurn(t.Context(), "parent")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, turnRunning, turn.phase)
	_, journaled, err := store.LoadTurnStep(t.Context(), "parent", request.turnID)
	require.NoError(t, err)
	require.True(t, journaled, "unaccepted final response survives failed closure")
	_, err = store.db.ExecContext(t.Context(), `ALTER TABLE shadow_turns DROP CONSTRAINT reject_close`)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `ALTER TABLE active_turns ADD CONSTRAINT reject_finish CHECK (phase <> 'delivering')`)
	require.NoError(t, err)
	parent.(*Bridge).handle(t.Context(), &request)
	require.Empty(t, finals, "failed settlement cannot deliver after durable closure")
	closed, captured, err := loadTurnInputs(t.Context(), store.db, "parent", request.turnID)
	require.NoError(t, err)
	require.True(t, captured)
	require.True(t, closed.Closed)
	_, err = parent.(*Bridge).openInputWindow(t.Context(), &request)
	require.NoError(t, err)
	require.False(t, parent.(*Bridge).inputOpen, "resume must not reopen durable closure")

	late := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindSteer, "later turn", true)}
	accepted, err := parent.(*Bridge).slipSteer(&late)
	require.NoError(t, err)
	require.False(t, accepted)
	_, err = store.db.ExecContext(t.Context(), `ALTER TABLE active_turns DROP CONSTRAINT reject_finish`)
	require.NoError(t, err)
	parent.(*Bridge).handle(t.Context(), &request)

	parentInput := <-requests
	require.Empty(t, requests, "retry must use the journaled provider response")
	_, captured, err = loadTurnInputs(t.Context(), store.db, "parent", request.turnID)
	require.NoError(t, err)
	require.False(t, captured, "successful parent settlement removed its canonical journal")
	// Do not start the first manager's pending children. A fresh manager must
	// discover the ready historyless child without any incoming message.
	_, err = store.PruneStateBefore(t.Context(), time.Now().Add(time.Hour))
	require.NoError(t, err)
	restarted, shutdown := newTestBridgeManager(t, cfg, store, finals)
	t.Cleanup(func() { require.NoError(t, shutdown()) })
	require.NoError(t, restarted.StartActiveTurns(t.Context()))

	var childInput []json.RawMessage
	select {
	case childInput = <-requests:
	case <-time.After(3 * time.Second):
		t.Fatal("ready child did not resume after parent journal cleanup")
	}

	require.Equal(t, parentInput, childInput)
	require.Len(t, childInput, 3)
	require.Contains(t, string(childInput[0]), "original")
	require.Contains(t, string(childInput[1]), "captured goal note")
	require.Contains(t, string(childInput[2]), "old frame")
	require.NotContains(t, string(childInput[2]), "raw admission")

	var childID string
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT child_conversation_id FROM shadow_bindings`).Scan(&childID))
	require.Eventually(t, func() bool { active, err := store.HasActiveTurn(t.Context(), childID); return err == nil && !active }, 3*time.Second, time.Millisecond)
	require.Len(t, finals, 1, "only parent completion was externally published")
	require.NoError(t, shutdown())
	_, err = store.PruneStateBefore(t.Context(), time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, found, err = store.Thread(childID)
	require.NoError(t, err)
	require.True(t, found, "a retained parent retains its bound child after completion")
}

func TestShadowTerminalClosure(t *testing.T) {
	for _, scenario := range []string{"attachment fallback", "preparation error", "obsolete goal", "stop"} {
		t.Run(scenario, func(t *testing.T) {
			store, root := newShadowTestStore(t)
			parent := NewConversation(store.definitions, discardPublisher{}, &Config{ConversationID: "parent", Agent: "main", SessionService: store}, slog.New(slog.DiscardHandler))
			parent.threads = &threadBridgeManager{bridges: map[string]directBridge{}}
			request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "start", true)}
			admitted, err := parent.activateInbound(t.Context(), &request)
			require.NoError(t, err)
			require.True(t, admitted)

			terminal := protocol.TerminalComplete

			switch scenario {
			case "attachment fallback":
				request.inbound.Attachments = []protocol.InboundAttachment{{Name: "bad.bin", MIMEType: "application/octet-stream", Data: []byte("unsupported")}}
			case "preparation error":
				require.NoError(t, root.Remove(".rocketclaw/agents/main.md"))

				terminal = protocol.TerminalFailed
			case "obsolete goal":
				request.inbound.GoalAction = protocol.GoalActionContinue
			case "stop":
				terminal = protocol.TerminalStopped

				require.NoError(t, store.captureTurnInputs(t.Context(), "parent", request.turnID, &turnInputSnapshot{Inputs: []rocketcode.PromptInput{{ID: "initial", Texts: []string{"saved"}}}}))
			}

			if scenario == "stop" {
				err = parent.finish(t.Context(), &request, &turnFinish{store: newSessionStore("parent", store)}, &runResult{turnID: request.turnID}, terminal)
			} else {
				err = parent.handleInbound(t.Context(), &request)
			}

			if scenario == "preparation error" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			var (
				closed        bool
				savedTerminal protocol.Terminal
			)
			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT bool_and(input_closed), min(terminal) FROM shadow_turns`).Scan(&closed, &savedTerminal))
			require.True(t, closed)
			require.Equal(t, terminal, savedTerminal)
		})
	}
}

func TestShadowFinalWaitCancellationAndClosure(t *testing.T) {
	store, _ := newShadowTestStore(t)
	parent := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "parent", Agent: "main", SessionService: store}}
	request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "start", true)}
	admitted, err := parent.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)

	snapshot := turnInputSnapshot{Inputs: []rocketcode.PromptInput{{ID: "initial", Texts: []string{"start"}}, {ID: "s1", Texts: []string{"same text"}}}}
	require.NoError(t, store.captureTurnInputs(t.Context(), "parent", request.turnID, &snapshot))

	var childID string
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT child_conversation_id FROM shadow_bindings ORDER BY shadow_agent LIMIT 1`).Scan(&childID))
	turn, claimed, err := store.claimShadowTurn(t.Context(), childID)
	require.NoError(t, err)
	require.True(t, claimed)

	child := &Bridge{shadow: true, config: Config{ConversationID: childID, SessionService: store}, activeTurnID: turn.id, requestCh: make(chan bridgeRequest, 1)}
	parent.threads = &threadBridgeManager{bridges: map[string]directBridge{childID: child}}
	ctx, cancel := context.WithCancel(t.Context())

	var group errgroup.Group
	group.Go(func() error {
		inputs, err := child.Drain(ctx, rocketcode.TurnPhaseFinalAnswer, []string{"initial", "s1"})
		require.Empty(t, inputs)

		return err
	})
	require.Eventually(t, func() bool { child.mu.Lock(); defer child.mu.Unlock(); return child.shadowInputWake != nil }, 3*time.Second, time.Millisecond)
	// Registration cannot hold the bridge lock, and duplicate-only state must
	// keep the existing child turn open until closure or cancellation.
	cancel()
	require.ErrorIs(t, group.Wait(), context.Canceled)
	active, err := store.HasActiveTurn(t.Context(), childID)
	require.NoError(t, err)
	require.True(t, active)

	group = errgroup.Group{}
	group.Go(func() error {
		inputs, err := child.Drain(t.Context(), rocketcode.TurnPhaseFinalAnswer, []string{"initial", "s1"})
		require.Equal(t, []rocketcode.PromptInput{{ID: "s2", Texts: []string{"same text"}}}, inputs)

		return err
	})
	require.Eventually(t, func() bool { child.mu.Lock(); defer child.mu.Unlock(); return child.shadowInputWake != nil }, 3*time.Second, time.Millisecond)

	snapshot.Inputs = append(snapshot.Inputs, rocketcode.PromptInput{ID: "s2", Texts: []string{"same text"}})
	snapshot.Closed = true
	require.NoError(t, store.captureTurnInputs(t.Context(), "parent", request.turnID, &snapshot))
	require.NoError(t, parent.notifyShadowInputs(t.Context(), request.turnID))
	require.NoError(t, group.Wait())
	inputs, err := child.Drain(t.Context(), rocketcode.TurnPhaseFinalAnswer, []string{"initial", "s1", "s2"})
	require.NoError(t, err)
	require.Empty(t, inputs, "a preexisting closed state is observed after wait registration")

	child.activeTurnID = "earlier-child-turn"
	child.activeTurnCancel = func() { t.Error("stop interrupted an unrelated child turn") }
	_, err = store.db.ExecContext(t.Context(), `UPDATE shadow_turns SET terminal = $2 WHERE parent_turn_id = $1`, request.turnID, protocol.TerminalStopped)
	require.NoError(t, err)
	require.NoError(t, parent.notifyShadowInputs(t.Context(), request.turnID))
}

// Deleting a Web copy while its producer's family holds history locks waits for
// them instead of deadlocking: both sides lock the family in the same order.
func TestHistoryMutationSharesLockOrder(t *testing.T) {
	for _, tc := range []struct {
		name, producer, child string
		lockRest              func(context.Context, *sql.Tx, string) error
	}{
		{"producer handoff", "slack-thread:LOCKORDER:1", "A2HANDOFF", func(ctx context.Context, tx *sql.Tx, producer string) error {
			return lockHistories(ctx, tx, []string{producer, "web:" + producer})
		}},
		{"shadow parent", "slack-thread:LOCKORDER:2", "A2SHADOW", func(ctx context.Context, tx *sql.Tx, producer string) error {
			return lockShadowHistory(ctx, tx, producer)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSessionService(t)
			ctx := t.Context()
			web := "web:" + tc.producer

			for _, id := range []string{tc.producer, web, tc.child} {
				_, err := (stateDAO{db: s.db}).createConversation(ctx, protocol.Conversation{ID: id, Agent: "main"})
				require.NoError(t, err)
			}

			_, err := s.db.ExecContext(ctx, `UPDATE managed_conversations SET producer_inbound_json = json_build_object('SyncDestination', $2::text) WHERE conversation_id = $1`, tc.producer, web)
			require.NoError(t, err)
			_, err = s.db.ExecContext(ctx, `INSERT INTO shadow_bindings (child_conversation_id, parent_conversation_id, parent_agent, shadow_agent) VALUES ($1, $2, 'main', 'shadow')`, tc.child, tc.producer)
			require.NoError(t, err)

			holder, err := s.db.BeginTx(ctx, nil)
			require.NoError(t, err)

			defer func() { _ = holder.Rollback() }()

			require.NoError(t, lockSessionHistory(ctx, holder, tc.producer))

			var pid int
			require.NoError(t, holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))

			deleted := make(chan error, 1)

			go func() {
				_, err := s.DeleteSession(ctx, web)
				deleted <- err
			}()

			require.Eventually(t, func() bool {
				var blocked bool

				err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked)

				return err == nil && blocked
			}, 10*time.Second, 10*time.Millisecond)

			require.NoError(t, tc.lockRest(ctx, holder, tc.producer))
			require.NoError(t, holder.Commit())
			require.NoError(t, <-deleted)
		})
	}
}
