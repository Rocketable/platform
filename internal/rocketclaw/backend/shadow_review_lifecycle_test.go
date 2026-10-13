package backend

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestShadowReviewClearProducerHistory(t *testing.T) {
	store, _ := newShadowTestStore(t)
	require.NoError(t, store.RegisterExternalMCPConversation("external", "main", &ExternalMCPSessionState{ManagedConversationID: "visible", PrivateConversationID: "private", Agent: "main"}))

	for _, id := range []string{"private", "projected-private", "unrelated/producer"} {
		require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "main"}))
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: id, Agent: "main", SessionService: store}}
		request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "original", false)}

		request.inbound.SyncDestination = "visible"
		if id == "unrelated/producer" {
			request.inbound.SyncDestination = "other"
		}

		admitted, err := bridge.activateInbound(t.Context(), &request)
		require.NoError(t, err)
		require.True(t, admitted)
		require.NoError(t, store.captureTurnInputs(t.Context(), id, request.turnID, &turnInputSnapshot{Inputs: []rocketcode.PromptInput{{ID: "input", Texts: []string{"prepared"}}}, Closed: true}))
		endTestTurn(t, store, id, request.turnID, "")
		children, err := queryStrings(t.Context(), store.db, `SELECT child_conversation_id FROM shadow_bindings WHERE parent_conversation_id = $1`, "producer children", id)
		require.NoError(t, err)

		for _, child := range children {
			turn, claimed, err := store.claimShadowTurn(t.Context(), child)
			require.NoError(t, err)
			require.True(t, claimed)

			entry := testSessionEntry("prepared", "child answer")
			entry.TurnID = turn.id

			_, err = store.finishTurn(t.Context(), turn.id, &turnFinish{store: newSessionStore(child, store), entries: []rocketcode.SessionEntry{*entry}, outbound: protocol.NewOutboundMessage(child, "answer")})
			require.NoError(t, err)
			require.NoError(t, store.closeTurn(t.Context(), turn.id))
			require.NoError(t, store.SaveAttachment(t.Context(), child, "", &protocol.OutboundAttachment{ID: child, Name: "result", Data: []byte("bytes")}, false))
		}
	}

	_, err := store.DeleteSession(t.Context(), "visible")
	require.NoError(t, err)

	for _, table := range []string{"session_entries", "attachments", "active_turns"} {
		var count int

		require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table+` WHERE conversation_id IN ('private', 'projected-private') OR conversation_id IN (SELECT child_conversation_id FROM shadow_bindings WHERE parent_conversation_id IN ('private', 'projected-private'))`).Scan(&count))
		require.Zero(t, count, table)
	}

	bindings, err := shadowHistoryDB(t.Context(), store.db, "visible", "", 0, 0)
	require.NoError(t, err)

	for _, binding := range bindings {
		require.Empty(t, binding.Turns)
	}

	var count int

	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns WHERE parent_source_conversation_id IN ('private', 'projected-private')`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns WHERE parent_source_conversation_id = 'unrelated/producer'`).Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_bindings WHERE parent_conversation_id = 'private'`).Scan(&count))
	require.Equal(t, 2, count, "bindings survive history clear")

	bridge := &Bridge{log: store.log, config: Config{ConversationID: "private", Agent: "main", SessionService: store}}
	request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "future", false)}
	admitted, err := bridge.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns WHERE parent_source_conversation_id = 'private'`).Scan(&count))
	require.Equal(t, 2, count, "future turns reuse the surviving bindings")
}

func TestShadowReviewClearRetainsPendingAttachments(t *testing.T) {
	newBridge, store, _, finals := newResumeTestBridges(t, false)
	crashed := newBridge()
	id := crashed.config.ConversationID
	files := []protocol.OutboundAttachment{{ID: "stashed-upload", Name: "upload.txt", Data: []byte("upload")}, {ID: "journal-result", Name: "result.txt", Data: []byte("result")}, {ID: "old-history", Name: "old.txt", Data: []byte("old")}}

	for i, turnID := range []string{"", "unfinished", ""} {
		require.NoError(t, store.SaveAttachment(t.Context(), id, turnID, &files[i], i == 0))
	}

	require.NoError(t, store.PutThreadQueueItem("stash", &protocol.ThreadQueueItem{ID: "stash", ConversationID: id, Kind: protocol.InboundKindHeld, Source: protocol.SourceWeb, Content: protocol.InboundContent{TextAttachments: []string{"attachment:stashed-upload"}}}))
	require.NoError(t, startTurnDB(t.Context(), store.db, "unfinished", id, protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "work", true)))

	_, err := store.DeleteSession(t.Context(), id)
	require.NoError(t, err)
	upload, err := store.LoadAttachment(t.Context(), id, files[0].ID, true)
	require.NoError(t, err)
	require.Equal(t, files[0].Data, upload.Data)

	queue, err := store.ThreadQueueForConversation(id)
	require.NoError(t, err)
	require.Len(t, queue, 1)
	_, uploaded, err := store.InputAttachments(t.Context(), id, queue[0].Content.TextAttachments[0])
	require.NoError(t, err)
	require.Len(t, uploaded, 1)
	_, err = store.LoadAttachment(t.Context(), id, files[2].ID, false)
	require.Error(t, err, "unreferenced history originals are removed")
	runTestBridge(t, newBridge())
	final := readFinal(t, finals)
	files[1].Size = int64(len(files[1].Data))
	require.Equal(t, files[1:2], final.Attachments, "restart reloads the original the unfinished turn's reply sends")
}

func TestShadowReviewPayloadRelease(t *testing.T) {
	for _, order := range []string{"parent finish first", "parent capture first", "child first", "concurrent", "failed", "stopped"} {
		t.Run(order, func(t *testing.T) {
			store, _ := newShadowTestStore(t)
			require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}))
			bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "parent", Agent: "main", SessionService: store}}
			request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "admitted bytes", true)}
			admitted, err := bridge.activateInbound(t.Context(), &request)
			require.NoError(t, err)
			require.True(t, admitted)

			snapshot := turnInputSnapshot{Inputs: []rocketcode.PromptInput{{ID: "image-input", Texts: []string{"prepared bytes"}, Attachments: []rocketcode.Attachment{{MIME: "image/png", URL: "data:image/png;base64,aW1hZ2U="}}}}, Environment: map[string]string{"source": "captured bytes"}, WorkflowSource: "workflow bytes"}
			require.NoError(t, store.captureTurnInputs(t.Context(), "parent", request.turnID, &snapshot))

			var child string

			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT child_conversation_id FROM shadow_bindings WHERE shadow_agent = 'main/a'`).Scan(&child))
			turn, claimed, err := store.claimShadowTurn(t.Context(), child)
			require.NoError(t, err)
			require.True(t, claimed)

			entry := testSessionEntry("prepared bytes", "checkpoint")
			entry.TurnID = turn.id

			_, err = store.AppendEntryID(t.Context(), child, entry)
			require.NoError(t, err)

			if order != "child first" && order != "concurrent" {
				if order == "parent capture first" {
					snapshot.Closed = true
					require.NoError(t, store.captureTurnInputs(t.Context(), "parent", request.turnID, &snapshot))
				} else {
					endTestTurn(t, store, "parent", request.turnID, "")
				}
			}

			mirrored, found, err := store.shadowInputs(t.Context(), child, turn.id)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, snapshot.Inputs, mirrored.Inputs, "a persisted checkpoint is not completed execution; draining still needs inputs")

			var terminal protocol.Terminal

			switch order {
			case "failed":
				terminal = protocol.TerminalFailed
			case "stopped":
				terminal = protocol.TerminalStopped
			}

			finish := &turnFinish{store: newSessionStore(child, store), entries: []rocketcode.SessionEntry{*entry}, terminal: terminal, outbound: protocol.NewOutboundMessage(child, "answer")}

			if order == "concurrent" {
				var group errgroup.Group

				group.Go(func() error {
					_, err := store.finishTurn(t.Context(), request.turnID, &turnFinish{store: newSessionStore("parent", store), outbound: protocol.NewOutboundMessage("parent", "answer")})

					return err
				})
				group.Go(func() error {
					_, err := store.finishTurn(t.Context(), turn.id, finish)

					return err
				})
				require.NoError(t, group.Wait())
			} else {
				_, err = store.finishTurn(t.Context(), turn.id, finish)
				require.NoError(t, err)
			}

			require.NoError(t, store.closeTurn(t.Context(), turn.id))

			if order == "child first" {
				mirrored, _, err = store.shadowInputs(t.Context(), child, turn.id)
				require.NoError(t, err)
				require.Equal(t, snapshot.Inputs, mirrored.Inputs, "open parent still owns recovery inputs")
				endTestTurn(t, store, "parent", request.turnID, "")
			}

			var inputs, context, environment, inbound []byte

			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT inputs, context_entries, environment, inbound_json FROM shadow_turns WHERE child_turn_id = $1`, turn.id).Scan(&inputs, &context, &environment, &inbound))

			if terminal != "" {
				require.Contains(t, string(inputs), "aW1hZ2U=")
				require.Contains(t, string(inbound), "admitted bytes")
			} else {
				require.JSONEq(t, `[]`, string(inputs))
				require.JSONEq(t, `{}`, string(context))
				require.JSONEq(t, `{}`, string(environment))
				require.JSONEq(t, `{}`, string(inbound))
			}

			var parentTurn string

			require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT parent_turn_id FROM shadow_turns WHERE child_turn_id = $1`, turn.id).Scan(&parentTurn))
			require.Equal(t, request.turnID, parentTurn)
		})
	}
}

func TestShadowReviewClearLiveProducer(t *testing.T) {
	store, root := newShadowTestStore(t)
	require.NoError(t, root.Remove(".rocketclaw/agents/main/z.md"))
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o700))

	for _, item := range []struct{ name, model string }{{"main", "gpt-parent"}, {"main/a", "gpt-child"}} {
		require.NoError(t, root.WriteFile(".rocketclaw/agents/"+item.name+".md", fmt.Appendf(nil, "---\nmodel: %s\npermission: {}\n---\nSystem %s", item.model, item.name), 0o600))
	}

	requests, cancelled := make(chan string, 4), make(chan bool, 2)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}

		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}

		requests <- request.Model

		<-r.Context().Done()

		cancelled <- true
	}))
	t.Cleanup(server.Close)
	cfg := config.NewLockedConfig(&config.Config{Workspace: root.Name(), OpenAI: config.OpenAIConfig{APIBaseURL: server.URL, APIKey: "test"}})
	store.definitions = cfg
	require.NoError(t, store.RegisterExternalMCPConversation("external", "main", &ExternalMCPSessionState{ManagedConversationID: "0visible", PrivateConversationID: "private", Agent: "main"}))
	require.NoError(t, store.UpsertThread("private", ThreadState{Agent: "main"}))
	manager, shutdown := newTestBridgeManager(t, cfg, store, make(chan *protocol.OutboundMessage, 4))
	t.Cleanup(func() { require.NoError(t, shutdown()) })

	store.historyWorkers = manager
	parent, err := manager.recordedBridge(t.Context(), "0visible")
	require.NoError(t, err)
	producer, err := manager.recordedBridge(t.Context(), "private")
	require.NoError(t, err)

	inbound := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "original", false)
	inbound.SyncDestination = "0visible"
	require.NoError(t, parent.enqueue(t.Context(), &bridgeRequest{inbound: inbound, producer: producer}, "test producer"))

	for range 2 {
		select {
		case <-requests:
		case <-time.After(3 * time.Second):
			t.Fatal("parent and child did not start")
		}
	}

	_, err = store.DeleteSession(t.Context(), "0visible")
	require.NoError(t, err)

	for range 2 {
		select {
		case <-cancelled:
		case <-time.After(3 * time.Second):
			t.Fatal("clear did not settle producer and child")
		}
	}

	var count int

	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM shadow_turns`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, manager.StartActiveTurns(t.Context()))
	require.Empty(t, runningTestTurns(t, store), "discarded work cannot resume")
}
