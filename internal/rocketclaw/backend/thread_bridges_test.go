package backend

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunRejectsUnresolvedAgentModelAtStartup(t *testing.T) {
	workspace := shortTempDir(t)
	agentsRoot := filepath.Join(workspace, "agents")
	require.NoError(t, os.MkdirAll(agentsRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsRoot, "main.md"), []byte("---\ndescription: Main\nmodel: '{{ model \"missing\" }}'\n---\nPrompt\n"), 0o600))

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	err = Run(t.Context(), &config.Config{Workspace: workspace, DatabaseURL: dsn}, "", slog.New(slog.DiscardHandler), nil)
	require.ErrorContains(t, err, "validate rocketcode definitions")
	require.ErrorContains(t, err, `model "missing" is not configured`)
}

func TestRunRejectsInvalidWorkflowAtStartup(t *testing.T) {
	for _, tt := range []struct{ name, source, want string }{
		{name: "syntax", source: "not valid starlark", want: "validate workflow definitions"},
		{name: "worker model", source: "meta = {\"name\": \"bad\", \"description\": \"Bad\"}\nw = worker(name=\"w\", instructions=\"work\", model=\"missing\")\ndef main(args): return None\n", want: `workflow worker model "missing" is not configured`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace := shortTempDir(t)
			root, err := os.OpenRoot(workspace)
			require.NoError(t, err)
			require.NoError(t, root.Mkdir("workflows", 0o755))
			require.NoError(t, root.WriteFile("workflows/bad.star", []byte(tt.source), 0o600))
			t.Cleanup(func() { require.NoError(t, root.Close()) })

			dsn, errDSN := harnessbridgetest.IsolatedTestDatabaseURL()
			require.NoError(t, errDSN)
			err = Run(t.Context(), &config.Config{Workspace: workspace, DatabaseURL: dsn}, "", slog.New(slog.DiscardHandler), nil)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestThreadBridgeManagerListsAndStartsWorkflowWithPersistedAgent(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	require.NoError(t, root.MkdirAll(".rocketclaw/workflows", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/workflows/audit.star", []byte("meta = {\"name\": \"audit\", \"description\": \"Audit routes\"}\ndef main(args): return args\n"), 0o600))
	require.NoError(t, root.Close())

	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner"}))

	bridge := new(fakeDirectBridge)
	startedAgent := ""
	manager := newThreadBridgeManager(&config.Config{Workspace: workspace}, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge { startedAgent = cfg.Agent; return bridge })

	descriptions, err := manager.WorkflowDescriptions()
	require.NoError(t, err)
	assert.Equal(t, []protocol.WorkflowDescription{{Name: "audit", Description: "Audit routes"}}, descriptions)

	inbound := newThreadInboundMessage("$workflow audit src", "222.333", "111.222")
	require.NoError(t, manager.StartWorkflowInThread(t.Context(), "main", "audit", "src", slackTarget("C123", "111.222"), inbound))
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "planner", startedAgent)
	assert.Equal(t, "audit", bridge.submits[0].Workflow.Name)
	assert.Equal(t, "src", bridge.submits[0].Workflow.Args)
	err = manager.StartWorkflowInThread(t.Context(), "main", "missing", "", slackTarget("C123", "111.222"), inbound)
	require.ErrorContains(t, err, `workflow "missing" is not configured`)
	require.Len(t, bridge.submits, 1)
}

func TestWorkflowValidationKeepsLiveAssetsOnInvalidReload(t *testing.T) {
	for _, tt := range []struct{ name, source, want string }{
		{name: "invalid syntax", source: "not valid starlark", want: "validate workflow definitions"},
		{name: "unknown worker model", source: "meta = {\"name\": \"bad\", \"description\": \"Bad\"}\nw = worker(name=\"w\", instructions=\"work\", model=\"missing\")\ndef main(args): return None\n", want: `workflow worker model "missing" is not configured`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			root, err := os.OpenRoot(workspace)
			require.NoError(t, err)
			require.NoError(t, root.MkdirAll(".rocketclaw/workflows", 0o755))
			require.NoError(t, root.WriteFile(".rocketclaw/workflows/live.star", []byte("live"), 0o600))
			require.NoError(t, root.Mkdir("workflows", 0o755))
			require.NoError(t, root.WriteFile("workflows/bad.star", []byte(tt.source), 0o600))
			t.Cleanup(func() { require.NoError(t, root.Close()) })

			cfg := &config.Config{Workspace: workspace}
			err = skel.ReplaceRuntimeAssetsAfterValidation(workspace, cfg.RuntimeDirName(), nil, slog.New(slog.DiscardHandler), func(runtimeDir string) error {
				return validateWorkflowDefinitions(cfg, runtimeDir)
			})
			require.ErrorContains(t, err, tt.want)
			data, err := root.ReadFile(".rocketclaw/workflows/live.star")
			require.NoError(t, err)
			assert.Equal(t, "live", string(data))
		})
	}
}

func TestThreadBridgeManagerCreatesSeparateBridgesPerThreadAndPersistsThem(t *testing.T) {
	store := newWorkspaceSessionService(t)
	created := make([]Config, 0, 2)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		created = append(created, cfg)
		return new(fakeDirectBridge)
	})

	require.NoError(t, manager.StartThread(t.Context(), "main", slackTarget("D123", "111.222"), newThreadInboundMessage("first", "111.222", "111.222")))
	require.NoError(t, manager.StartThread(t.Context(), "factory", slackTarget("D123", "333.444"), newThreadInboundMessage("second", "333.444", "333.444")))

	require.Len(t, created, 2)

	thread, ok, err := store.Thread(created[0].ConversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ThreadState{Agent: "main"}, thread)
}

func TestThreadBridgeManagerStartsPendingScheduledMessageBridges(t *testing.T) {
	workspace := t.TempDir()

	store := newWorkspaceSessionService(t)
	for _, cfg := range []Config{
		{ConversationID: protocol.SlackThreadConversationID("D123", "111.222"), Agent: "planner", StartNewThread: inertStartNewThread, SessionService: store},
		{ConversationID: protocol.SlackThreadConversationID("D123", "333.444"), Agent: "helper", StartNewThread: inertStartNewThread, SessionService: store},
	} {
		bridge := NewConversation(&config.Config{Workspace: workspace}, nil, &cfg, slog.New(slog.DiscardHandler))
		require.NoError(t, bridge.Start(t.Context()))
		require.NoError(t, bridge.ScheduleMessage(time.Hour, "later", false))
		require.NoError(t, bridge.Stop())
	}

	created := make([]Config, 0, 2)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		created = append(created, cfg)
		return new(fakeDirectBridge)
	})

	require.NoError(t, manager.StartPendingScheduledMessages(map[string]bool{}))
	require.Len(t, created, 2)
	assert.ElementsMatch(t, []Config{
		{ConversationID: protocol.SlackThreadConversationID("D123", "111.222"), Agent: "planner", OutputTargets: []protocol.OutputTarget{protocol.OutputTargetSlack}, UserQuestionAsker: protocol.NoUserQuestionAsker()},
		{ConversationID: protocol.SlackThreadConversationID("D123", "333.444"), Agent: "helper", OutputTargets: []protocol.OutputTarget{protocol.OutputTargetSlack}, UserQuestionAsker: protocol.NoUserQuestionAsker()},
	}, created)
}

func TestThreadBridgeManagerSkipsScheduledMessageBridgeDuringActiveTurnRecovery(t *testing.T) {
	workspace := t.TempDir()
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	bridge := NewConversation(&config.Config{Workspace: workspace}, nil, &Config{ConversationID: conversationID, Agent: "planner", StartNewThread: inertStartNewThread, SessionService: store}, slog.New(slog.DiscardHandler))
	require.NoError(t, bridge.Start(t.Context()))
	require.NoError(t, bridge.ScheduleMessage(time.Hour, "later", false))
	require.NoError(t, bridge.Stop())

	created := 0
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge {
		created++

		return new(fakeDirectBridge)
	})

	require.NoError(t, manager.StartPendingScheduledMessages(map[string]bool{conversationID: true}))
	assert.Zero(t, created)
}

func TestThreadBridgeManagerDoesNotStartThreadWhenStoreIsUnavailable(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	store, err := NewSessionServiceIn(dsn, testLogger())
	require.NoError(t, err)
	require.NoError(t, store.Stop(context.Background()))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge {
		return bridge
	})

	err = manager.StartThread(t.Context(), "main", slackTarget("D123", "111.222"), newThreadInboundMessage("first", "111.222", "111.222"))
	require.ErrorContains(t, err, "load external MCP paired conversation")
	assert.Zero(t, bridge.stops)
	assert.Empty(t, bridge.submits)
}

func TestThreadBridgeManagerSwitchesThreadAgent(t *testing.T) {
	store := newWorkspaceSessionService(t)
	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	replyTarget := slackTarget("D123", "111.222")
	require.NoError(t, manager.StartThread(t.Context(), "main", replyTarget, newThreadInboundMessage("first", "111.222", "111.222")))

	handled, err := manager.SwitchThreadAgent(replyTarget, "planner")
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, []string{"submit:first", "switch:planner"}, bridge.ops)

	thread, ok, err := store.Thread(protocol.SlackThreadConversationID("D123", "111.222"))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "planner", thread.Agent)
}

func TestThreadBridgeManagerReadsThreadAgent(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: " planner "}))

	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return new(fakeDirectBridge) })

	agent, handled, err := manager.ThreadAgent(slackTarget("D123", "111.222"))
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, "planner", agent)

	conversationID = protocol.SlackThreadConversationID("D123", "333.444")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: " "}))

	agent, handled, err = manager.ThreadAgent(slackTarget("D123", "333.444"))
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Empty(t, agent)

	agent, handled, err = manager.ThreadAgent(slackTarget("D123", "222.333"))
	require.NoError(t, err)
	assert.False(t, handled)
	assert.Empty(t, agent)
}

func TestThreadBridgeManagerStartsGoalInExistingThreadWithPersistedAgent(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner"}))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		assert.Equal(t, Config{ConversationID: conversationID, Agent: "planner", OutputTargets: []protocol.OutputTarget{protocol.OutputTargetSlack}, UserQuestionAsker: protocol.NoUserQuestionAsker()}, cfg)

		return bridge
	})

	inbound := newThreadInboundMessage("ship it", "222.333", "111.222")
	inbound.SlackReply.RecipientTeamID = "T123"
	inbound.SlackReply.RecipientUserID = "U456"
	require.NoError(t, manager.StartGoalInThread(t.Context(), "", "ship it", "", 5, slackTarget("D123", "111.222"), inbound))
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "goal", bridge.submits[0].Label)

	goal, ok, err := store.Goal(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "ship it", goal.Objective)
	assert.Equal(t, "T123", goal.SlackRecipientTeamID)
	assert.Equal(t, "U456", goal.SlackRecipientUserID)
}

func TestThreadBridgeManagerStartsActiveGoalAfterRestart(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner"}))
	require.NoError(t, store.BeginGoal(conversationID, "ship it", "", 5, "T123", "U456"))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		assert.Equal(t, Config{ConversationID: conversationID, Agent: "planner", OutputTargets: []protocol.OutputTarget{protocol.OutputTargetSlack}, UserQuestionAsker: protocol.NoUserQuestionAsker()}, cfg)

		return bridge
	})

	require.NoError(t, manager.StartActiveGoals(map[string]bool{}))
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "goal_continuation", bridge.submits[0].Label)
	assert.Equal(t, "Continue the active goal loop.", bridge.submits[0].Text)
	assert.Equal(t, conversationID, bridge.submits[0].ConversationID)
	assert.Equal(t, &protocol.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222", RecipientTeamID: "T123", RecipientUserID: "U456"}, bridge.submits[0].SlackReply)
}

func TestThreadBridgeManagerSkipsActiveGoalContinuationDuringActiveTurnRecovery(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner"}))
	require.NoError(t, store.BeginGoal(conversationID, "ship it", "", 5, "", ""))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	require.NoError(t, manager.StartActiveGoals(map[string]bool{conversationID: true}))
	assert.Empty(t, bridge.submits)
}

func TestThreadBridgeManagerRejectsDuplicateActiveGoal(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "first", "", 5, "", ""))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	err := manager.StartGoalInThread(t.Context(), "main", "second", "", 5, slackTarget("D123", "111.222"), newThreadInboundMessage("second", "222.333", "111.222"))
	require.ErrorIs(t, err, protocol.ErrGoalAlreadyActive)
	assert.Empty(t, bridge.submits)
}

func TestThreadBridgeManagerAllowsGoalAfterCompletedGoal(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "first", "", 5, "", ""))
	_, err := store.UpdateGoalStatus(conversationID, GoalStatusComplete, "done")
	require.NoError(t, err)

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	require.NoError(t, manager.StartGoalInThread(t.Context(), "main", "second", "", 5, slackTarget("D123", "111.222"), newThreadInboundMessage("second", "222.333", "111.222")))

	goal, ok, err := store.Goal(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "second", goal.Objective)
	assert.Equal(t, GoalStatusActive, goal.Status)
}

func TestThreadBridgeManagerPickLaterWorkUsesLiveBridge(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	require.NoError(t, manager.PickLaterWork(t.Context(), conversationID))
	assert.Equal(t, []string{"pick"}, bridge.ops)
	require.NoError(t, manager.PickLaterWork(t.Context(), ""))
	require.NoError(t, manager.PickLaterWork(t.Context(), protocol.SlackThreadConversationID("D999", "9.9")))
	assert.Equal(t, []string{"pick"}, bridge.ops)
}

func TestThreadBridgeManagerInterruptSlackThreadInterruptsActiveTurn(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "first", "", 5, "", ""))

	marker := &protocol.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333", ThreadTS: "111.222"}
	bridge := &fakeDirectBridge{interruptResult: &protocol.InboundMessage{SlackReply: marker}}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	require.NoError(t, manager.StartThread(t.Context(), "main", slackTarget("D123", "111.222"), newThreadInboundMessage("start", "111.222", "111.222")))

	result, err := manager.InterruptThread(slackTarget("D123", "111.222"))
	require.NoError(t, err)
	assert.Equal(t, marker, result.SlackReply)
	assert.Equal(t, 1, bridge.interrupts)

	goal, ok, err := store.Goal(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, GoalStatusStopped, goal.Status)
}

func TestThreadBridgeManagerRegistersCronThreadWithoutSubmitting(t *testing.T) {
	store := newWorkspaceSessionService(t)
	bridge := new(fakeDirectBridge)

	var created Config

	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		created = cfg
		return bridge
	})

	require.NoError(t, manager.RegisterCronThread(t.Context(), slackTarget("C123", "111.222"), "planner"))

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	assert.Equal(t, Config{ConversationID: conversationID, Agent: "planner", OutputTargets: []protocol.OutputTarget{protocol.OutputTargetSlack}, UserQuestionAsker: protocol.NoUserQuestionAsker()}, created)
	assert.Empty(t, bridge.submits)

	thread, ok, err := store.Thread(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}, thread)
}

func TestThreadBridgeManagerRegistersThreadWithoutSubmitting(t *testing.T) {
	store := newWorkspaceSessionService(t)
	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	target := slackTarget("C123", "111.222")

	created, err := manager.RegisterThread(target, "planner")
	require.NoError(t, err)
	require.True(t, created)

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	thread, ok, err := store.Thread(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ThreadState{Agent: "planner"}, thread)
	assert.Empty(t, bridge.submits)
	entries, err := store.ObserveEntries(t.Context(), conversationID, 0)
	require.NoError(t, err)
	assert.Empty(t, entries)

	created, err = manager.RegisterThread(target, "other")
	require.NoError(t, err)
	assert.False(t, created)

	thread, ok, err = store.Thread(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ThreadState{Agent: "planner"}, thread)

	inbound := newThreadInboundMessage("first agentic turn", "222.333", "111.222")
	handled, err := manager.SubmitThreadReply(t.Context(), target, inbound)
	require.NoError(t, err)
	require.True(t, handled)
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, inbound, bridge.submits[0])
}

func TestThreadBridgeManagerStashListAndDeleteQueue(t *testing.T) {
	store := newWorkspaceSessionService(t)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return new(fakeDirectBridge) })
	target := slackTarget("C123", "111.222")
	other := slackTarget("C123", "333.444")

	require.NoError(t, manager.StashThreadQueueItem(t.Context(), target, &protocol.ThreadQueueItem{ID: "q1", Message: "first", Principal: "U1", StashAt: time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC), SlackChannel: "C123", SlackTS: "1"}))
	require.NoError(t, manager.StashThreadQueueItem(t.Context(), target, &protocol.ThreadQueueItem{ID: "q2", Message: "second", Principal: "U1", StashAt: time.Date(2026, 8, 24, 14, 0, 0, 0, time.UTC), SlackChannel: "C123", SlackTS: "2"}))
	require.NoError(t, manager.StashThreadQueueItem(t.Context(), other, &protocol.ThreadQueueItem{ID: "other", Message: "keep", Principal: "U2", StashAt: time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)}))

	items, err := manager.ThreadQueueItems(t.Context(), target)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, []string{"q1", "q2"}, []string{items[0].ID, items[1].ID})

	require.NoError(t, manager.DeleteThreadQueueItem(t.Context(), target, "other"))
	require.NoError(t, manager.DeleteThreadQueueItem(t.Context(), target, "q2"))
	items, err = manager.ThreadQueueItems(t.Context(), target)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "q1", items[0].ID)

	otherItems, err := manager.ThreadQueueItems(t.Context(), other)
	require.NoError(t, err)
	require.Len(t, otherItems, 1)
	assert.Equal(t, "other", otherItems[0].ID)
}

func TestThreadBridgeManagerRejectsMissingSlackThreadTarget(t *testing.T) {
	manager := newThreadBridgeManager(nil, newWorkspaceSessionService(t), slog.New(slog.DiscardHandler), func(Config) directBridge { return new(fakeDirectBridge) })
	_, err := manager.RegisterThread(slackTarget("", ""), "main")
	require.ErrorContains(t, err, "text thread target is required")

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "", "hello", true)
	err = manager.StartThread(t.Context(), "main", slackTarget("", ""), inbound)
	require.ErrorContains(t, err, "slack thread target is required")

	inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: " ", ThreadTS: " "}
	err = manager.StartThread(t.Context(), "main", slackTarget(" ", " "), inbound)
	require.ErrorContains(t, err, "slack thread target is required")
}

func TestThreadBridgeManagerSubmitsPersistedThreadReply(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "factory"}))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	_, handled, err := manager.ThreadAgent(slackTarget("D123", "111.222"))
	require.NoError(t, err)
	assert.True(t, handled)

	inbound := newThreadInboundMessage("follow up", "222.333", "")
	handled, err = manager.SubmitThreadReply(context.Background(), slackTarget("D123", "111.222"), inbound)
	require.NoError(t, err)
	assert.True(t, handled)

	require.Len(t, bridge.submits, 1)
	assert.Equal(t, conversationID, bridge.submits[0].ConversationID)
	assert.Equal(t, "111.222", bridge.submits[0].SlackReply.ThreadTS)
}

func TestThreadBridgeManagerDisablesStartNewThreadForCronThreadReply(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	inbound := newThreadInboundMessage("follow up", "222.333", "")
	handled, err := manager.SubmitThreadReply(context.Background(), slackTarget("D123", "111.222"), inbound)
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "true", bridge.submits[0].Metadata[protocol.InboundStartNewThreadDisabledMetadataKey])
}

func TestThreadBridgeManagerDisablesStartNewThreadForCronThreadGoalStart(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	inbound := newThreadInboundMessage("goal", "222.333", "")
	err := manager.StartGoalInThread(context.Background(), "planner", "goal", "", 3, slackTarget("D123", "111.222"), inbound)
	require.NoError(t, err)
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "true", bridge.submits[0].Metadata[protocol.InboundStartNewThreadDisabledMetadataKey])
}

func TestThreadBridgeManagerStartNewThreadUsesFreshThreadLocalConversation(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, "skills"), 0o755))
	writeAppTestAgent(t, workspace, "main", "---\ndescription: Test agent\nmodel: gpt-5.5\n---\nPrompt\n")

	store := newWorkspaceSessionService(t)
	bridge := new(fakeDirectBridge)

	var created Config

	manager := newThreadBridgeManager(&config.Config{Workspace: workspace}, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		created = cfg
		return bridge
	})

	rootCalls := 0
	result, err := manager.StartNewThread(t.Context(), &protocol.StartNewThreadRequest{Source: protocol.SourceSlack, SourceConversationID: "source-1", CurrentAgent: "main", Title: "Child", Prompt: " literal $(date) ", SlackReply: &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"}}, func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error) {
		rootCalls++
		return protocol.StartNewThreadRootResult{Target: protocol.TextConversationTarget{ChannelID: "C2", MessageID: "2", ThreadID: "2"}, URL: "https://example.invalid/thread"}, nil
	})
	require.NoError(t, err)

	conversationID := protocol.SlackThreadConversationID("C2", "2")

	assert.Equal(t, 1, rootCalls)
	assert.Equal(t, protocol.StartNewThreadResult{ConversationID: conversationID, URL: "https://example.invalid/thread"}, result)
	assert.Equal(t, Config{ConversationID: conversationID, Agent: "main", OutputTargets: []protocol.OutputTarget{protocol.OutputTargetSlack}, UserQuestionAsker: protocol.NoUserQuestionAsker()}, created)
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, []string{"submit: literal $(date) "}, bridge.ops)
	assert.Equal(t, " literal $(date) ", bridge.submits[0].Text)
	assert.Equal(t, conversationID, bridge.submits[0].ConversationID)
	assert.Equal(t, "System", bridge.submits[0].Metadata[protocol.InboundOriginMetadataKey])
	assert.Equal(t, "Text", bridge.submits[0].Metadata[protocol.InboundMediaMetadataKey])
	require.NotNil(t, bridge.submits[0].SlackReply)
	assert.Equal(t, protocol.SlackReplyTarget{ChannelID: "C2", MessageTS: "2", ThreadTS: "2"}, *bridge.submits[0].SlackReply)
}

func TestThreadBridgeManagerStartNewThreadAcceptsSystemSourceWithChannel(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, "skills"), 0o755))
	writeAppTestAgent(t, workspace, "main", "---\ndescription: Test agent\nmodel: gpt-5.5\n---\nPrompt\n")

	store := newWorkspaceSessionService(t)
	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(&config.Config{Workspace: workspace}, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	rootCalls := 0
	result, err := manager.StartNewThread(t.Context(), &protocol.StartNewThreadRequest{Source: protocol.SourceSystem, CurrentAgent: "main", AllowedAgents: []string{"main"}, Title: "Nightly", Prompt: "run suite", SlackReply: &protocol.SlackReplyTarget{ChannelID: "#ops"}}, func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error) {
		rootCalls++
		return protocol.StartNewThreadRootResult{Target: protocol.TextConversationTarget{ChannelID: "C2", MessageID: "2", ThreadID: "2"}, URL: "https://example.invalid/thread"}, nil
	})
	require.NoError(t, err)

	conversationID := protocol.SlackThreadConversationID("C2", "2")

	assert.Equal(t, 1, rootCalls)
	assert.Equal(t, protocol.StartNewThreadResult{ConversationID: conversationID, URL: "https://example.invalid/thread"}, result)
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "run suite", bridge.submits[0].Text)
	assert.Equal(t, conversationID, bridge.submits[0].ConversationID)

	thread, ok, err := store.Thread(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "main", thread.Agent)
	assert.NotEqual(t, ThreadCreatedByCron, thread.CreatedBy)
}

func TestThreadBridgeManagerStartNewThreadRejectsLockedAgentAndUnavailableSources(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, "skills"), 0o755))
	writeAppTestAgent(t, workspace, "main", "---\ndescription: Test agent\nmodel: gpt-5.5\n---\nPrompt\n")

	store := newWorkspaceSessionService(t)
	manager := newThreadBridgeManager(&config.Config{Workspace: workspace}, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return new(fakeDirectBridge) })

	root := func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error) {
		t.Fatal("createRoot should not run")
		return protocol.StartNewThreadRootResult{}, nil
	}

	_, err := manager.StartNewThread(t.Context(), &protocol.StartNewThreadRequest{Source: protocol.SourceSystem, CurrentAgent: "main", AllowedAgents: []string{"main"}, Agent: "other", Title: "Nightly", Prompt: "run suite", SlackReply: &protocol.SlackReplyTarget{ChannelID: "#ops"}}, root)
	require.ErrorContains(t, err, `agent "other" is not allowed on this source surface`)

	_, err = manager.StartNewThread(t.Context(), &protocol.StartNewThreadRequest{Source: protocol.SourceExternalMCP, CurrentAgent: "main", Title: "Nightly", Prompt: "run suite", SlackReply: &protocol.SlackReplyTarget{ChannelID: "#ops"}}, root)
	require.ErrorContains(t, err, "rocketclaw_start_new_thread is not available for external_mcp turns")

	_, err = manager.StartNewThread(t.Context(), &protocol.StartNewThreadRequest{Source: protocol.SourceSystem, CurrentAgent: "main", Title: "Nightly", Prompt: "run suite"}, root)
	require.ErrorContains(t, err, "rocketclaw_start_new_thread is not available for system turns")
}

func TestThreadBridgeManagerIgnoresUnmanagedThreadTargets(t *testing.T) {
	store := newWorkspaceSessionService(t)
	created := 0
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge {
		created++

		return new(fakeDirectBridge)
	})

	for _, tt := range []struct {
		name string
		call func() (bool, error)
	}{
		{
			name: "blank thread reply",
			call: func() (bool, error) {
				return manager.SubmitThreadReply(context.Background(), slackTarget(" ", " "), newThreadInboundMessage("reply", "222.333", " "))
			},
		},
		{
			name: "unknown thread reply",
			call: func() (bool, error) {
				return manager.SubmitThreadReply(context.Background(), slackTarget("D123", "111.222"), newThreadInboundMessage("reply", "222.333", "111.222"))
			},
		},
		{
			name: "blank prepare",
			call: func() (bool, error) {
				_, handled, err := manager.ThreadAgent(slackTarget(" ", " "))
				return handled, err
			},
		},
		{
			name: "missing prepare",
			call: func() (bool, error) {
				_, handled, err := manager.ThreadAgent(slackTarget("D123", "111.222"))
				return handled, err
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handled, err := tt.call()
			require.NoError(t, err)
			assert.False(t, handled)
		})
	}

	err := manager.SubmitExternalMCP(context.Background(), "main", " ", newThreadInboundMessage("reply", "222.333", "111.222"), NoopActivationHook)
	require.ErrorContains(t, err, "text thread conversation ID is required")
	assert.Zero(t, created)
}

func TestThreadBridgeManagerExternalMCPAndSlackUsePairedSeparateBridges(t *testing.T) {
	store := newWorkspaceSessionService(t)
	managedConversationID := protocol.SlackThreadConversationID("D123", "111.222")
	privateConversationID := "external_mcp:customer:private"

	require.NoError(t, store.RegisterExternalMCPConversation("public-1", "alpha", &ExternalMCPSessionState{Agent: "customer", PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID, SlackChannel: "#alpha"}))

	bridges := map[string]*fakeDirectBridge{}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		if cfg.ConversationID == privateConversationID {
			assert.Equal(t, Config{ConversationID: privateConversationID, Agent: "customer", ManagedConversationID: managedConversationID, ExternalConversationID: "public-1", OutputTargets: []protocol.OutputTarget{protocol.OutputTargetSlack}, UserQuestionAsker: protocol.NoUserQuestionAsker()}, cfg)
		} else {
			assert.Equal(t, Config{ConversationID: managedConversationID, Agent: "alpha", ManagedConversationID: managedConversationID, OutputTargets: []protocol.OutputTarget{protocol.OutputTargetSlack}, UserQuestionAsker: protocol.NoUserQuestionAsker()}, cfg)
		}

		bridge := new(fakeDirectBridge)
		bridges[cfg.ConversationID] = bridge

		return bridge
	})

	requestCtx, cancel := context.WithCancel(context.Background())
	require.NoError(t, manager.SubmitExternalMCP(requestCtx, "customer", privateConversationID, newThreadInboundMessage("initial", "123.456", "111.222"), NoopActivationHook))
	cancel()

	handled, err := manager.SubmitThreadReply(context.Background(), slackTarget("D123", "111.222"), newThreadInboundMessage("follow up", "222.333", "111.222"))
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, bridges, 2)
	assert.Equal(t, "initial", bridges[privateConversationID].submits[0].Text)
	assert.Equal(t, "follow up", bridges[managedConversationID].submits[0].Text)

	handled, err = manager.SwitchThreadAgent(slackTarget("D123", "111.222"), "supercow")
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, []string{"submit:follow up", "switch:supercow"}, bridges[managedConversationID].ops)
	assert.Equal(t, []string{"switch:customer", "submit:initial"}, bridges[privateConversationID].ops)

	_, err = manager.InterruptThread(slackTarget("D123", "111.222"))
	require.NoError(t, err)
	assert.Equal(t, 1, bridges[managedConversationID].interrupts)
	assert.Zero(t, bridges[privateConversationID].interrupts)

	manager.InterruptConversation(privateConversationID)
	assert.Equal(t, 1, bridges[privateConversationID].interrupts)
}

func TestThreadBridgeManagerSubmitsSameExternalMCPConversationToOneBridge(t *testing.T) {
	store := newWorkspaceSessionService(t)
	managedConversationID := protocol.SlackThreadConversationID("D123", "111.222")
	privateConversationID := "external_mcp:planner:private"
	require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID, SlackChannel: "#ops"}))

	bridge := new(fakeDirectBridge)
	created := 0
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		created++

		assert.Equal(t, Config{ConversationID: privateConversationID, Agent: "planner", ManagedConversationID: managedConversationID, ExternalConversationID: "public-1", OutputTargets: []protocol.OutputTarget{protocol.OutputTargetSlack}, UserQuestionAsker: protocol.NoUserQuestionAsker()}, cfg)

		return bridge
	})

	require.NoError(t, manager.SubmitExternalMCP(context.Background(), "planner", privateConversationID, newThreadInboundMessage("recovered", "123.456", "111.222"), NoopActivationHook))
	require.NoError(t, manager.SubmitExternalMCP(context.Background(), "planner", privateConversationID, newThreadInboundMessage("follow up", "222.333", "111.222"), NoopActivationHook))

	assert.Equal(t, 1, created)
	require.Len(t, bridge.submits, 2)
	assert.Equal(t, "recovered", bridge.submits[0].Text)
	assert.Equal(t, "follow up", bridge.submits[1].Text)
}

func TestThreadBridgeManagerLegacyExternalMCPUsesReportedStickyAgent(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "supercow"}))
	require.NoError(t, store.UpsertExternalMCPSession("public-1", &ExternalMCPSessionState{Agent: "planner", ManagedConversationID: conversationID, SlackChannel: "#ops"}))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	require.NoError(t, manager.SubmitExternalMCP(t.Context(), "planner", conversationID, newThreadInboundMessage("follow up", "222.333", "111.222"), NoopActivationHook))

	assert.Equal(t, []string{"switch:planner", "submit:follow up"}, bridge.ops)
}

func TestThreadBridgeManagerExternalMCPConversationsUseIndependentBridges(t *testing.T) {
	store := newWorkspaceSessionService(t)
	bridges := map[string]*fakeDirectBridge{}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		bridge := new(fakeDirectBridge)
		bridges[cfg.ConversationID] = bridge

		return bridge
	})

	firstConversationID := protocol.SlackThreadConversationID("D123", "111.222")
	secondConversationID := protocol.SlackThreadConversationID("D123", "333.444")
	recovered := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "recovered_turn", "recovered", false)
	require.NoError(t, manager.SubmitExternalMCP(context.Background(), "planner", firstConversationID, recovered, NoopActivationHook))

	followup := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "", "independent", true)
	require.NoError(t, manager.SubmitExternalMCP(context.Background(), "planner", secondConversationID, followup, NoopActivationHook))

	require.Len(t, bridges, 2)
	assert.Equal(t, "recovered", bridges[firstConversationID].submits[0].Text)
	assert.Equal(t, "independent", bridges[secondConversationID].submits[0].Text)
}

func TestThreadBridgeManagerRecoversActiveTurnInThreadLocalConversation(t *testing.T) {
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, newWorkspaceSessionService(t), slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		assert.Equal(t, Config{ConversationID: conversationID, Agent: "planner", OutputTargets: []protocol.OutputTarget{protocol.OutputTargetSlack}, RecoveringActiveTurn: true, UserQuestionAsker: protocol.NoUserQuestionAsker()}, cfg)
		return bridge
	})
	turn := &ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{ConversationKey: conversationID, TurnID: "turn-1", Agent: "planner"}}

	require.NoError(t, manager.RecoverActiveTurn(t.Context(), turn))
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "turn-1", bridge.submits[0].Text)
}

func TestThreadBridgeManagerRecoversPrivateExternalMCPTurn(t *testing.T) {
	store := newWorkspaceSessionService(t)
	managedConversationID := protocol.SlackThreadConversationID("C123", "111.222")
	privateConversationID := "external_mcp:planner:private"
	require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID, SlackChannel: "#ops"}))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		assert.Equal(t, Config{ConversationID: privateConversationID, Agent: "planner", ManagedConversationID: managedConversationID, ExternalConversationID: "public-1", OutputTargets: []protocol.OutputTarget{protocol.OutputTargetSlack}, RecoveringActiveTurn: true, UserQuestionAsker: protocol.NoUserQuestionAsker()}, cfg)
		return bridge
	})
	turn := &ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{ConversationKey: privateConversationID, TurnID: "turn-mcp", Agent: "planner"}}

	require.NoError(t, manager.RecoverActiveTurn(t.Context(), turn))
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "turn-mcp", bridge.submits[0].Text)
}

func TestThreadBridgeManagerRestoresManagedAgentAfterRecovery(t *testing.T) {
	store := newWorkspaceSessionService(t)
	managedConversationID := protocol.SlackThreadConversationID("C123", "111.222")
	privateConversationID := "external_mcp:planner:private"
	require.NoError(t, store.RegisterExternalMCPConversation("public-1", "alpha", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID, SlackChannel: "#ops"}))
	updated, err := store.SetThreadAgentIfExists(managedConversationID, "supercow")
	require.NoError(t, err)
	require.True(t, updated)

	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		assert.Equal(t, Config{ConversationID: managedConversationID, Agent: "alpha", AgentAfterRecovery: "supercow", ManagedConversationID: managedConversationID, OutputTargets: []protocol.OutputTarget{protocol.OutputTargetSlack}, RecoveringActiveTurn: true, UserQuestionAsker: protocol.NoUserQuestionAsker()}, cfg)
		return new(fakeDirectBridge)
	})
	turn := &ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{ConversationKey: managedConversationID, TurnID: "turn-managed", Agent: "alpha"}}

	require.NoError(t, manager.RecoverActiveTurn(t.Context(), turn))
}

func TestThreadBridgeManagerStopStopsActiveBridges(t *testing.T) {
	store := newWorkspaceSessionService(t)
	bridges := make([]*fakeDirectBridge, 0, 2)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge {
		bridge := new(fakeDirectBridge)
		bridges = append(bridges, bridge)

		return bridge
	})

	require.NoError(t, manager.StartThread(context.Background(), "main", slackTarget("D123", "111.222"), newThreadInboundMessage("first", "111.222", "111.222")))
	require.NoError(t, manager.StartThread(context.Background(), "main", slackTarget("D123", "333.444"), newThreadInboundMessage("second", "333.444", "333.444")))
	require.NoError(t, manager.Stop())

	require.Len(t, bridges, 2)
	assert.Equal(t, 1, bridges[0].stops)
	assert.Equal(t, 1, bridges[1].stops)
}

func TestThreadBridgeManagerConsumesSlackOriginatorOutput(t *testing.T) {
	store := newWorkspaceSessionService(t)
	require.NoError(t, store.UpsertThread(protocol.SlackThreadConversationID("C123", "111.222"), ThreadState{Agent: "main"}))

	outputs := make(chan *protocol.OutboundMessage, 2)
	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	manager.output = func(_ context.Context, message *protocol.OutboundMessage) error {
		outputs <- message

		return nil
	}

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "", "hello", true)
	handled, err := manager.SubmitThreadReply(t.Context(), protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.222"}, inbound)
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, protocol.BridgeSlack, inbound.Bridge)
	require.NotNil(t, inbound.Response)

	progress := protocol.NewOutboundMessage(protocol.SourceSlack, inbound.ConversationID, "progress")
	final := protocol.NewOutboundMessage(protocol.SourceSlack, inbound.ConversationID, "final")
	final.Complete = true

	inbound.Response <- protocol.Response{Payload: &protocol.TextResponse{Kind: protocol.ResponseProgress, Message: progress}}

	inbound.Response <- protocol.Response{Payload: &protocol.TextResponse{Kind: protocol.ResponseResult, Message: final}}

	require.Equal(t, "progress", (<-outputs).Text)
	require.Equal(t, "final", (<-outputs).Text)
}

func TestThreadBridgeManagerHandlesChildThreadInteraction(t *testing.T) {
	manager := newThreadBridgeManager(nil, newWorkspaceSessionService(t), slog.New(slog.DiscardHandler), func(Config) directBridge { return new(fakeDirectBridge) })
	want := protocol.StartNewThreadRootResult{Target: protocol.TextConversationTarget{ChannelID: "C1", MessageID: "1.2", ThreadID: "1.2"}, URL: "https://slack.example/thread"}
	manager.root = func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error) {
		return want, nil
	}
	root := make(chan protocol.StartNewThreadRootResult, 1)
	errCh := make(chan error, 1)
	handled, err := manager.handleInteraction(t.Context(), protocol.StartNewThreadResponse{Request: &protocol.StartNewThreadRequest{}, Root: root, Err: errCh})
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, want, <-root)
}

func TestThreadBridgeManagerBusyExternalMCPStashesOnManagedQueue(t *testing.T) {
	store := newWorkspaceSessionService(t)
	managedID := protocol.SlackThreadConversationID("C123", "111.222")
	privateID := "external_mcp:main:private"
	require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Agent: "main", PrivateConversationID: privateID, ManagedConversationID: managedID, SlackChannel: "#ops"}))
	require.NoError(t, store.ReleaseExternalMCPRecovery(privateID))
	require.NoError(t, store.ReserveExternalMCPRecovery(managedID))
	t.Cleanup(func() { _ = store.ReleaseExternalMCPRecovery(managedID) })

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	inbound := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, privateID, "later from mcp", true)
	resultCh := inbound.EnableResponseWait()
	require.NoError(t, manager.SubmitExternalMCP(t.Context(), "main", privateID, inbound, NoopActivationHook))
	assert.Empty(t, bridge.submits)

	items, err := manager.ThreadQueueItems(t.Context(), protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.222"})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "later from mcp", items[0].Message)
	assert.Empty(t, items[0].SlackTS)

	require.NoError(t, manager.DeleteThreadQueueItem(t.Context(), protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.222"}, items[0].ID))

	result := <-resultCh
	require.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "removed")
}

func TestThreadBridgeManagerSubmitExternalMCPDoesNotAttachSlackResponse(t *testing.T) {
	store := newWorkspaceSessionService(t)
	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	conversationID := "external_mcp:main:private"
	inbound := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, conversationID, "hi", true)
	require.NoError(t, manager.SubmitExternalMCP(t.Context(), "main", conversationID, inbound, NoopActivationHook))
	require.Equal(t, protocol.BridgeExternalMCP, inbound.Bridge)
	require.Nil(t, inbound.Response)
}

type fakeDirectBridge struct {
	submits         []*protocol.InboundMessage
	stops           int
	ops             []string
	startedDone     <-chan struct{}
	interrupts      int
	interruptResult *protocol.InboundMessage
}

func (f *fakeDirectBridge) Start(ctx context.Context) error {
	f.startedDone = ctx.Done()

	return nil
}

func (f *fakeDirectBridge) Stop() error {
	f.stops++

	return nil
}

func (f *fakeDirectBridge) Submit(_ context.Context, msg *protocol.InboundMessage) error {
	select {
	case <-f.startedDone:
		return context.Canceled
	default:
	}

	f.submits = append(f.submits, msg)
	f.ops = append(f.ops, "submit:"+msg.Text)

	return nil
}

func (f *fakeDirectBridge) SubmitWhenActive(ctx context.Context, msg *protocol.InboundMessage, activation protocol.ActivationHook) error {
	if err := activation(ctx, msg); err != nil {
		return err
	}

	return f.Submit(ctx, msg)
}

func (f *fakeDirectBridge) RecoverActiveTurn(ctx context.Context, turn *ActiveTurnState) error {
	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "recovered_turn", turn.Checkpoint.TurnID, false)

	return f.Submit(ctx, inbound)
}

func (f *fakeDirectBridge) InterruptActiveTurn() *protocol.InboundMessage {
	f.interrupts++

	return f.interruptResult
}

func (f *fakeDirectBridge) SwitchAgent(agent string) {
	f.ops = append(f.ops, "switch:"+agent)
}

func (f *fakeDirectBridge) PickLaterWork(context.Context) error {
	f.ops = append(f.ops, "pick")

	return nil
}

func newThreadInboundMessage(text, messageTS, threadTS string) *protocol.InboundMessage {
	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "", text, true)
	inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: "D123", MessageTS: messageTS, ThreadTS: threadTS}

	return inbound
}

func slackTarget(channelID, threadTS string) protocol.TextConversationTarget {
	return protocol.TextConversationTarget{ChannelID: channelID, ThreadID: threadTS}
}

func newWorkspaceSessionService(t *testing.T) *SessionService {
	t.Helper()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	service, err := NewSessionServiceIn(dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	return service
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func writeAppTestAgent(t *testing.T, workspace, name, content string) {
	t.Helper()

	dir := filepath.Join(workspace, config.DefaultRuntimeDir, "agents")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o600))
}

func inertStartNewThread(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadResult, error) {
	return protocol.StartNewThreadResult{}, errors.New("start new thread is inert in this test")
}
