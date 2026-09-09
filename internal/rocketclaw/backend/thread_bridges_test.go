package backend

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/Rocketable/platform/internal/rocketcode"
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

func TestThreadBridgeManagerSkillDescriptions(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(".rocketclaw/agents", 0o755))

	for _, name := range []string{"review", "stop", "denied", "ask"} {
		require.NoError(t, root.MkdirAll(".rocketclaw/skills/"+name, 0o755))
		require.NoError(t, root.WriteFile(".rocketclaw/skills/"+name+"/SKILL.md", []byte("---\nname: "+name+"\ndescription: About "+name+"\n---\nInstructions\n"), 0o600))
	}

	require.NoError(t, root.WriteFile(".rocketclaw/agents/main.md", []byte("---\ndescription: Main\nmodel: test\npermission:\n  skill:\n    '*': allow\n    denied: deny\n    ask: auto(guardian)\n---\nPrompt\n"), 0o600))
	require.NoError(t, root.WriteFile(".rocketclaw/agents/planner.md", []byte("---\ndescription: Planner\nmodel: test\npermission:\n  skill: deny\n---\nPrompt\n"), 0o600))

	manager := &threadBridgeManager{runtime: &config.Config{Workspace: workspace}}
	descriptions, err := manager.SkillDescriptions("main")
	require.NoError(t, err)
	assert.Equal(t, []protocol.SkillDescription{{Name: "review", Description: "About review"}, {Name: "stop", Description: "About stop"}}, descriptions)
	descriptions, err = manager.SkillDescriptions("planner")
	require.NoError(t, err)
	assert.Empty(t, descriptions)

	_, err = manager.SkillDescriptions("missing")
	require.Error(t, err)
	require.NoError(t, root.WriteFile(".rocketclaw/agents/main.md", []byte("---\npermission: [invalid\n---\n"), 0o600))

	_, err = manager.SkillDescriptions("main")
	require.Error(t, err)
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

	bridge := newDirectBridgeMock()
	startedAgent := ""
	manager := newThreadBridgeManager(&config.Config{Workspace: workspace}, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge { startedAgent = cfg.Agent; return bridge })

	descriptions, err := manager.WorkflowDescriptions()
	require.NoError(t, err)
	assert.Equal(t, []protocol.WorkflowDescription{{Name: "audit", Description: "Audit routes"}}, descriptions)

	inbound := newThreadInboundMessage("$workflow audit src", "222.333", "111.222")
	require.NoError(t, manager.StartWorkflowInThread(t.Context(), "main", "audit", "src", slackTarget("C123", "111.222"), inbound))
	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, "planner", startedAgent)
	assert.Equal(t, "audit", submittedMessages(bridge)[0].Workflow.Name)
	assert.Equal(t, "src", submittedMessages(bridge)[0].Workflow.Args)
	err = manager.StartWorkflowInThread(t.Context(), "main", "missing", "", slackTarget("C123", "111.222"), inbound)
	require.ErrorContains(t, err, `workflow "missing" is not configured`)
	require.Len(t, submittedMessages(bridge), 1)
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
		return newDirectBridgeMock()
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
		return newDirectBridgeMock()
	})

	require.NoError(t, manager.StartPendingScheduledMessages(map[string]bool{}))
	require.Len(t, created, 2)
	assert.ElementsMatch(t, []Config{
		{ConversationID: protocol.SlackThreadConversationID("D123", "111.222"), Agent: "planner", UserQuestionAsker: protocol.NoUserQuestionAsker()},
		{ConversationID: protocol.SlackThreadConversationID("D123", "333.444"), Agent: "helper", UserQuestionAsker: protocol.NoUserQuestionAsker()},
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

		return newDirectBridgeMock()
	})

	require.NoError(t, manager.StartPendingScheduledMessages(map[string]bool{conversationID: true}))
	assert.Zero(t, created)
}

func TestThreadBridgeManagerDoesNotStartThreadWhenStoreIsUnavailable(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	store, err := NewSessionServiceIn(t.Context(), dsn, testLogger())
	require.NoError(t, err)
	require.NoError(t, store.Stop())

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge {
		return bridge
	})

	err = manager.StartThread(t.Context(), "main", slackTarget("D123", "111.222"), newThreadInboundMessage("first", "111.222", "111.222"))
	require.ErrorContains(t, err, "read managed conversation")
	assert.Empty(t, bridge.StopCalls())
	assert.Empty(t, submittedMessages(bridge))
}

func TestThreadBridgeManagerSwitchesThreadAgent(t *testing.T) {
	t.Run("opaque live conversation", func(t *testing.T) {
		store := newWorkspaceSessionService(t)

		const id = "opaque-web-id"
		require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "main", CreatedBy: "alice"}))
		bridge := &Bridge{config: Config{ConversationID: id, Agent: "main"}}
		rt := &Runtime{threads: &threadBridgeManager{store: store, bridges: map[string]directBridge{id: bridge}}}
		switched, err := rt.SwitchConversationAgent(id, "planner")
		require.NoError(t, err)
		require.True(t, switched)
		require.Equal(t, "planner", bridge.agentSnapshot())

		thread, found, err := store.Thread(id)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, ThreadState{Agent: "planner", CreatedBy: "alice"}, thread)
	})

	store := newWorkspaceSessionService(t)
	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	replyTarget := slackTarget("D123", "111.222")
	require.NoError(t, manager.StartThread(t.Context(), "main", replyTarget, newThreadInboundMessage("first", "111.222", "111.222")))

	handled, err := manager.SwitchThreadAgent(replyTarget, "planner")
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, "first", submittedMessages(bridge)[0].Text)
	require.Len(t, bridge.SwitchAgentCalls(), 1)
	assert.Equal(t, "planner", bridge.SwitchAgentCalls()[0].Agent)

	thread, ok, err := store.Thread(protocol.SlackThreadConversationID("D123", "111.222"))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "planner", thread.Agent)
}

func TestThreadBridgeManagerReadsThreadAgent(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: " planner "}))

	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return newDirectBridgeMock() })

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

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		assert.Equal(t, Config{ConversationID: conversationID, Agent: "planner", UserQuestionAsker: protocol.NoUserQuestionAsker()}, cfg)

		return bridge
	})

	inbound := newThreadInboundMessage("ship it", "222.333", "111.222")
	inbound.SlackReply.RecipientTeamID = "T123"
	inbound.SlackReply.RecipientUserID = "U456"
	require.NoError(t, manager.StartGoalInThread(t.Context(), "", "ship it", "", 5, slackTarget("D123", "111.222"), inbound))
	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, "goal", submittedMessages(bridge)[0].Label)

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

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		assert.Equal(t, Config{ConversationID: conversationID, Agent: "planner", UserQuestionAsker: protocol.NoUserQuestionAsker()}, cfg)

		return bridge
	})

	require.NoError(t, manager.StartActiveGoals(map[string]bool{}))
	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, "goal_continuation", submittedMessages(bridge)[0].Label)
	assert.Equal(t, "Continue the active goal loop.", submittedMessages(bridge)[0].Text)
	assert.Equal(t, conversationID, submittedMessages(bridge)[0].ConversationID)
	assert.Equal(t, &protocol.SlackReplyTarget{RecipientTeamID: "T123", RecipientUserID: "U456"}, submittedMessages(bridge)[0].SlackReply)
}

func TestThreadBridgeManagerSkipsActiveGoalContinuationDuringActiveTurnRecovery(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner"}))
	require.NoError(t, store.BeginGoal(conversationID, "ship it", "", 5, "", ""))

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	require.NoError(t, manager.StartActiveGoals(map[string]bool{conversationID: true}))
	assert.Empty(t, submittedMessages(bridge))
}

func TestThreadBridgeManagerRejectsDuplicateActiveGoal(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "first", "", 5, "", ""))

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	err := manager.StartGoalInThread(t.Context(), "main", "second", "", 5, slackTarget("D123", "111.222"), newThreadInboundMessage("second", "222.333", "111.222"))
	require.ErrorIs(t, err, protocol.ErrGoalAlreadyActive)
	assert.Empty(t, submittedMessages(bridge))
}

func TestThreadBridgeManagerAllowsGoalAfterCompletedGoal(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "first", "", 5, "", ""))
	_, err := store.UpdateGoalStatus(conversationID, GoalStatusComplete, "done")
	require.NoError(t, err)

	bridge := newDirectBridgeMock()
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

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	require.NoError(t, manager.PickLaterWork(t.Context(), conversationID))
	require.Len(t, bridge.PickLaterWorkCalls(), 1)
	require.NoError(t, manager.PickLaterWork(t.Context(), ""))
	require.NoError(t, manager.PickLaterWork(t.Context(), protocol.SlackThreadConversationID("D999", "9.9")))
	require.Len(t, bridge.PickLaterWorkCalls(), 1)
}

func TestThreadBridgeManagerInterruptSlackThreadInterruptsActiveTurn(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "first", "", 5, "", ""))

	marker := &protocol.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333", ThreadTS: "111.222"}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	bridge := &Bridge{activeReply: &protocol.InboundMessage{SlackReply: marker}, activeTurnCancel: cancel, activeTurnInterrupts: make(chan os.Signal, 1)}
	manager := &threadBridgeManager{store: store, bridges: map[string]directBridge{conversationID: bridge}}

	result, err := manager.InterruptThread(slackTarget("D123", "111.222"))
	require.NoError(t, err)
	assert.Equal(t, marker, result.SlackReply)
	assert.Same(t, bridge.activeReply, result)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	assert.Equal(t, os.Interrupt, <-bridge.activeTurnInterrupts)

	goal, ok, err := store.Goal(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, GoalStatusStopped, goal.Status)
}

func TestThreadBridgeManagerRegistersThreadWithoutSubmitting(t *testing.T) {
	store := newWorkspaceSessionService(t)
	bridge := newDirectBridgeMock()
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
	assert.Empty(t, submittedMessages(bridge))
	entries, err := store.ObserveEntries(t.Context(), conversationID)
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
	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, inbound, submittedMessages(bridge)[0])
}

func TestThreadBridgeManagerStashListAndDeleteQueue(t *testing.T) {
	store := newWorkspaceSessionService(t)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return newDirectBridgeMock() })
	target := slackTarget("C123", "111.222")
	other := slackTarget("C123", "333.444")

	require.NoError(t, manager.StashThreadQueueItem(t.Context(), target, &protocol.ThreadQueueItem{ID: "q1", Message: "first", Principal: "U1", StashAt: time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC), SlackChannel: "C123", SlackTS: "1"}))
	require.NoError(t, manager.StashThreadQueueItem(t.Context(), target, &protocol.ThreadQueueItem{ID: "q2", Message: "second", Principal: "U1", StashAt: time.Date(2026, 8, 24, 14, 0, 0, 0, time.UTC), SlackChannel: "C123", SlackTS: "2"}))
	require.NoError(t, manager.StashThreadQueueItem(t.Context(), other, &protocol.ThreadQueueItem{ID: "other", Message: "keep", Principal: "U2", StashAt: time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)}))

	items, err := manager.ThreadQueueItems(target)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, []string{"q1", "q2"}, []string{items[0].ID, items[1].ID})

	removed, err := manager.DeleteThreadQueueItem(t.Context(), target, "other")
	require.NoError(t, err)
	require.False(t, removed)
	removed, err = manager.DeleteThreadQueueItem(t.Context(), target, "q2")
	require.NoError(t, err)
	require.True(t, removed)

	items, err = manager.ThreadQueueItems(target)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "q1", items[0].ID)

	otherItems, err := manager.ThreadQueueItems(other)
	require.NoError(t, err)
	require.Len(t, otherItems, 1)
	assert.Equal(t, "other", otherItems[0].ID)
}

func TestThreadBridgeManagerRejectsMissingSlackThreadTarget(t *testing.T) {
	manager := newThreadBridgeManager(nil, newWorkspaceSessionService(t), slog.New(slog.DiscardHandler), func(Config) directBridge { return newDirectBridgeMock() })
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

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	_, handled, err := manager.ThreadAgent(slackTarget("D123", "111.222"))
	require.NoError(t, err)
	assert.True(t, handled)

	inbound := newThreadInboundMessage("follow up", "222.333", "")
	handled, err = manager.SubmitThreadReply(context.Background(), slackTarget("D123", "111.222"), inbound)
	require.NoError(t, err)
	assert.True(t, handled)

	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, conversationID, submittedMessages(bridge)[0].ConversationID)
	assert.Equal(t, "111.222", submittedMessages(bridge)[0].SlackReply.ThreadTS)
}

func TestThreadBridgeManagerDisablesStartNewThreadForCronThreadReply(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	inbound := newThreadInboundMessage("follow up", "222.333", "")
	handled, err := manager.SubmitThreadReply(context.Background(), slackTarget("D123", "111.222"), inbound)
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, "true", submittedMessages(bridge)[0].Metadata[protocol.InboundStartNewThreadDisabledMetadataKey])
}

func TestThreadBridgeManagerDisablesStartNewThreadForCronThreadGoalStart(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner", CreatedBy: ThreadCreatedByCron}))

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	inbound := newThreadInboundMessage("goal", "222.333", "")
	err := manager.StartGoalInThread(context.Background(), "planner", "goal", "", 3, slackTarget("D123", "111.222"), inbound)
	require.NoError(t, err)
	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, "true", submittedMessages(bridge)[0].Metadata[protocol.InboundStartNewThreadDisabledMetadataKey])
}

func TestThreadBridgeManagerStartNewThreadUsesFreshThreadLocalConversation(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, "skills"), 0o755))
	writeAppTestAgent(t, workspace, "main", "---\ndescription: Test agent\nmodel: gpt-5.5\n---\nPrompt\n")

	store := newWorkspaceSessionService(t)
	bridge := newDirectBridgeMock()

	var created Config

	manager := newThreadBridgeManager(&config.Config{Workspace: workspace}, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		created = cfg
		return bridge
	})

	rootCalls := 0
	result, err := manager.StartNewThread(t.Context(), &protocol.StartNewThreadRequest{Source: protocol.SourceSlack, CurrentAgent: "main", Title: "Child", Prompt: " literal $(date) ", SlackReply: &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"}}, func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error) {
		rootCalls++
		return protocol.StartNewThreadRootResult{Target: protocol.TextConversationTarget{ChannelID: "C2", MessageID: "2", ThreadID: "2"}, URL: "https://example.invalid/thread"}, nil
	})
	require.NoError(t, err)

	conversationID := protocol.SlackThreadConversationID("C2", "2")

	assert.Equal(t, 1, rootCalls)
	assert.Equal(t, protocol.StartNewThreadResult{ConversationID: conversationID, URL: "https://example.invalid/thread"}, result)
	assert.Equal(t, Config{ConversationID: conversationID, Agent: "main", UserQuestionAsker: protocol.NoUserQuestionAsker()}, created)
	require.Len(t, submittedMessages(bridge), 1)
	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, " literal $(date) ", submittedMessages(bridge)[0].Text)
	assert.Equal(t, conversationID, submittedMessages(bridge)[0].ConversationID)
	assert.Equal(t, "System", submittedMessages(bridge)[0].Metadata[protocol.InboundOriginMetadataKey])
	assert.Equal(t, "Text", submittedMessages(bridge)[0].Metadata[protocol.InboundMediaMetadataKey])
	require.NotNil(t, submittedMessages(bridge)[0].SlackReply)
	assert.Equal(t, protocol.SlackReplyTarget{ChannelID: "C2", MessageTS: "2", ThreadTS: "2"}, *submittedMessages(bridge)[0].SlackReply)
}

func TestThreadBridgeManagerStartNewThreadAcceptsSystemSourceWithChannel(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, "skills"), 0o755))
	writeAppTestAgent(t, workspace, "main", "---\ndescription: Test agent\nmodel: gpt-5.5\n---\nPrompt\n")

	store := newWorkspaceSessionService(t)
	bridge := newDirectBridgeMock()
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
	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, "run suite", submittedMessages(bridge)[0].Text)
	assert.Equal(t, conversationID, submittedMessages(bridge)[0].ConversationID)

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
	manager := newThreadBridgeManager(&config.Config{Workspace: workspace}, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return newDirectBridgeMock() })

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

		return newDirectBridgeMock()
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

	assert.Zero(t, created)
}

func TestThreadBridgeManagerRecoversActiveTurnInThreadLocalConversation(t *testing.T) {
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, newWorkspaceSessionService(t), slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		assert.Equal(t, Config{ConversationID: conversationID, Agent: "planner", RecoveringActiveTurn: true, UserQuestionAsker: protocol.NoUserQuestionAsker()}, cfg)
		return bridge
	})
	turn := &ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{ConversationKey: conversationID, TurnID: "turn-1", Agent: "planner"}}

	require.NoError(t, manager.RecoverActiveTurn(t.Context(), turn))
	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, "turn-1", submittedMessages(bridge)[0].Text)
}

func TestThreadBridgeManagerRecoversPrivateExternalMCPTurn(t *testing.T) {
	store := newWorkspaceSessionService(t)
	managedConversationID := protocol.SlackThreadConversationID("C123", "111.222")
	privateConversationID := "external_mcp:planner:private"
	require.NoError(t, store.RegisterExternalMCPConversation("public-1", "main", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: privateConversationID, ManagedConversationID: managedConversationID, SlackChannel: "#ops"}))

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		assert.Equal(t, Config{ConversationID: privateConversationID, Agent: "planner", RecoveringActiveTurn: true, UserQuestionAsker: protocol.NoUserQuestionAsker()}, cfg)
		return bridge
	})
	turn := &ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{ConversationKey: privateConversationID, TurnID: "turn-mcp", Agent: "planner"}}

	require.NoError(t, manager.RecoverActiveTurn(t.Context(), turn))
	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, "turn-mcp", submittedMessages(bridge)[0].Text)
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
		assert.Equal(t, Config{ConversationID: managedConversationID, Agent: "alpha", AgentAfterRecovery: "supercow", RecoveringActiveTurn: true, UserQuestionAsker: protocol.NoUserQuestionAsker()}, cfg)
		return newDirectBridgeMock()
	})
	turn := &ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{ConversationKey: managedConversationID, TurnID: "turn-managed", Agent: "alpha"}}

	require.NoError(t, manager.RecoverActiveTurn(t.Context(), turn))
}

func TestThreadBridgeManagerStopStopsActiveBridges(t *testing.T) {
	store := newWorkspaceSessionService(t)
	bridges := make([]*directBridgeMock, 0, 2)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge {
		bridge := newDirectBridgeMock()
		bridges = append(bridges, bridge)

		return bridge
	})

	require.NoError(t, manager.StartThread(context.Background(), "main", slackTarget("D123", "111.222"), newThreadInboundMessage("first", "111.222", "111.222")))
	require.NoError(t, manager.StartThread(context.Background(), "main", slackTarget("D123", "333.444"), newThreadInboundMessage("second", "333.444", "333.444")))
	require.NoError(t, manager.Stop())

	require.Len(t, bridges, 2)
	require.Len(t, bridges[0].StopCalls(), 1)
	require.Len(t, bridges[1].StopCalls(), 1)
}

func newDirectBridgeMock() *directBridgeMock {
	mock := &directBridgeMock{}

	var startedDone <-chan struct{}

	mock.StartFunc = func(ctx context.Context) error {
		startedDone = ctx.Done()
		return nil
	}
	mock.StopFunc = func() error { return nil }
	mock.SubmitFunc = func(_ context.Context, _ *protocol.InboundMessage) error {
		select {
		case <-startedDone:
			return context.Canceled
		default:
		}

		return nil
	}
	mock.RecoverActiveTurnFunc = func(ctx context.Context, turn *ActiveTurnState) error {
		inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "recovered_turn", turn.Checkpoint.TurnID, false)
		return mock.Submit(ctx, inbound)
	}
	mock.InterruptActiveTurnFunc = func() *protocol.InboundMessage { return nil }
	mock.SwitchAgentFunc = func(string) {}
	mock.PickLaterWorkFunc = func(context.Context) error { return nil }

	return mock
}

func submittedMessages(bridge *directBridgeMock) []*protocol.InboundMessage {
	calls := bridge.SubmitCalls()

	out := make([]*protocol.InboundMessage, len(calls))
	for i, call := range calls {
		out[i] = call.Msg
	}

	return out
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
	service, err := NewSessionServiceIn(t.Context(), dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

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
