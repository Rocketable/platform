package backend

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func TestSideAskDoesNotTakeSourceOccupancy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		yID := protocol.SlackThreadConversationID("C123", "111.222")
		y := newBlockingTurnBridge()
		rt := testSideAskRuntime(t, func(cfg Config) directBridge {
			if cfg.ConversationID == yID {
				return y
			}

			return new(completingTurnBridge)
		})
		require.NoError(t, rt.CreateConversation(yID, []string{"planner"}, nil))

		yDone := make(chan error, 1)
		go func() {
			yDone <- rt.RunTurn(t.Context(), &protocol.TurnRequest{ID: yID, Kind: protocol.TurnPrompt, Text: "busy"})
		}()

		<-y.started
		synctest.Wait()

		require.NoError(t, (frontend.SideAsk{Backend: rt}).Run(t.Context(), protocol.SideAskRequest{
			ConversationID: yID,
			Agent:          "planner",
			Question:       "What broke?",
			Thinking:       sideAskNoop,
			Message:        sideAskNoop,
		}))

		require.True(t, y.Handling())
		require.False(t, rt.threads.conversationBusy(createdSideAskID(t, rt, yID)))

		y.complete()
		synctest.Wait()
		require.NoError(t, <-yDone)
	})
}

func TestSideAskCancelEndsSOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		yID := protocol.SlackThreadConversationID("C123", "111.222")
		y := newBlockingTurnBridge()

		var mu sync.Mutex

		bridges := map[string]*blockingTurnBridge{yID: y}
		rt := testSideAskRuntime(t, func(cfg Config) directBridge {
			mu.Lock()
			defer mu.Unlock()

			if b := bridges[cfg.ConversationID]; b != nil {
				return b
			}

			b := newBlockingTurnBridge()
			bridges[cfg.ConversationID] = b

			return b
		})
		require.NoError(t, rt.CreateConversation(yID, []string{"planner"}, nil))

		yDone := make(chan error, 1)
		go func() {
			yDone <- rt.RunTurn(t.Context(), &protocol.TurnRequest{ID: yID, Kind: protocol.TurnPrompt, Text: "busy"})
		}()

		<-y.started
		synctest.Wait()

		askCtx, askCancel := context.WithCancel(t.Context())

		askDone := make(chan error, 1)
		go func() {
			askDone <- (frontend.SideAsk{Backend: rt}).Run(askCtx, protocol.SideAskRequest{
				ConversationID: yID,
				Agent:          "planner",
				Question:       "What broke?",
				Thinking:       sideAskNoop,
				Message:        sideAskNoop,
			})
		}()

		synctest.Wait()

		sID := createdSideAskID(t, rt, yID)

		mu.Lock()
		sBridge := bridges[sID]
		mu.Unlock()
		require.NotNil(t, sBridge)
		<-sBridge.started
		synctest.Wait()

		askCancel()
		synctest.Wait()
		require.ErrorIs(t, <-askDone, context.Canceled)
		require.True(t, y.Handling())
		require.False(t, sBridge.Handling())

		y.complete()
		synctest.Wait()
		require.NoError(t, <-yDone)
	})
}

func TestSideAskSyncCopiesFullHistory(t *testing.T) {
	yID := protocol.SlackThreadConversationID("C123", "111.222")
	rt := testSideAskRuntime(t, func(Config) directBridge { return new(completingTurnBridge) })
	require.NoError(t, rt.CreateConversation(yID, []string{"planner"}, nil))
	id1, err := rt.Sessions.AppendEntryID(t.Context(), yID, testSessionEntry("card-one-user", "card-one-assistant"))
	require.NoError(t, err)
	_, err = rt.Sessions.AppendEntryID(t.Context(), yID, testSessionEntry("card-two-user", "card-two-assistant"))
	require.NoError(t, err)
	before, err := rt.Sessions.ObserveEntries(t.Context(), yID, 0)
	require.NoError(t, err)

	require.NoError(t, (frontend.SideAsk{Backend: rt}).Run(t.Context(), protocol.SideAskRequest{
		ConversationID: yID,
		SessionEntryID: id1,
		Agent:          "planner",
		Question:       "What broke?",
		Thinking:       sideAskNoop,
		Message:        sideAskNoop,
	}))

	sID := createdSideAskID(t, rt, yID)
	copied, err := rt.Sessions.ObserveEntries(t.Context(), sID, 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(copied), 2)
	assert.Contains(t, sessionEntryTexts(copied), "card-one-user")
	assert.Contains(t, sessionEntryTexts(copied), "card-two-user")

	after, err := rt.Sessions.ObserveEntries(t.Context(), yID, 0)
	require.NoError(t, err)
	require.Len(t, after, len(before))

	listed, err := rt.ListConversations()
	require.NoError(t, err)

	for _, rec := range listed {
		if rec.ID == sID {
			assert.NotContains(t, rec.Tags, protocol.ConversationUserFacing)
		}
	}
}

func TestSideAskUsesChosenAgentIdentity(t *testing.T) {
	yID := protocol.SlackThreadConversationID("C123", "111.222")
	rt := testSideAskRuntime(t, func(Config) directBridge { return new(completingTurnBridge) })
	require.NoError(t, rt.CreateConversation(yID, []string{"social"}, nil))
	_, err := rt.Sessions.AppendEntryID(t.Context(), yID, testSessionEntry("prior-user", "prior-assistant"))
	require.NoError(t, err)

	require.NoError(t, (frontend.SideAsk{Backend: rt}).Run(t.Context(), protocol.SideAskRequest{
		ConversationID: yID,
		Agent:          "planner",
		Question:       "status?",
		Thinking:       sideAskNoop,
		Message:        sideAskNoop,
	}))

	sID := createdSideAskID(t, rt, yID)
	agent, err := rt.ConversationAgent(sID)
	require.NoError(t, err)
	assert.Equal(t, "planner", agent)

	yAgent, err := rt.ConversationAgent(yID)
	require.NoError(t, err)
	assert.Equal(t, "social", yAgent)
}

func testSideAskRuntime(t *testing.T, factory func(Config) directBridge) *Runtime {
	t.Helper()

	store := newWorkspaceSessionService(t)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), factory)

	return &Runtime{Sessions: store, threads: manager, Channels: protocol.NewChannels()}
}

func createdSideAskID(t *testing.T, rt *Runtime, source string) string {
	t.Helper()

	listed, err := rt.ListConversations()
	require.NoError(t, err)

	for _, rec := range listed {
		if rec.ID != source && strings.HasPrefix(rec.ID, "side-ask:") {
			return rec.ID
		}
	}

	t.Fatal("side ask conversation not listed")

	return ""
}

func sessionEntryTexts(entries []ObservedSessionEntry) string {
	var b strings.Builder

	for i := range entries {
		for _, raw := range entries[i].Entry.ReplayInput {
			b.Write(raw)
			b.WriteByte('\n')
		}
	}

	return b.String()
}

func sideAskNoop(context.Context, string) error { return nil }
