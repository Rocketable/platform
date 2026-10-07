package backend

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
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

func TestRuntimeWorkflowDescriptionsListsSavedWorkflows(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	require.NoError(t, root.MkdirAll(".rocketclaw/workflows", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/workflows/audit.star", []byte("meta = {\"name\": \"audit\", \"description\": \"Audit routes\"}\ndef main(args): return args\n"), 0o600))
	require.NoError(t, root.Close())

	descriptions, err := (&Runtime{Cfg: &config.Config{Workspace: workspace}}).WorkflowDescriptions()
	require.NoError(t, err)
	assert.Equal(t, []protocol.WorkflowDescription{{Name: "audit", Description: "Audit routes"}}, descriptions)

	_, err = (&Runtime{Cfg: &config.Config{Workspace: filepath.Join(workspace, "missing")}}).WorkflowDescriptions()
	require.ErrorContains(t, err, "open workflow root")
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
	runTestManager(t, manager)

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
		bridge := NewConversation(&config.Config{Workspace: workspace}, discardPublisher{}, &cfg, slog.New(slog.DiscardHandler))
		require.NoError(t, store.UpsertThread(cfg.ConversationID, ThreadState{Agent: cfg.Agent}))
		require.NoError(t, startTestBridge(t.Context(), bridge))
		require.NoError(t, bridge.ScheduleMessage(time.Hour, "later", false))
		require.NoError(t, bridge.Stop())
	}

	require.NoError(t, store.UpsertThread(protocol.SlackThreadConversationID("D123", "111.222"), ThreadState{Agent: "selected"}))

	created := make([]Config, 0, 2)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		created = append(created, cfg)
		return newDirectBridgeMock()
	})
	runTestManager(t, manager)

	require.NoError(t, manager.StartPendingScheduledMessages())
	require.Len(t, created, 2)
	assert.ElementsMatch(t, []Config{
		{ConversationID: protocol.SlackThreadConversationID("D123", "111.222"), Agent: "selected", UserQuestionAsker: protocol.NoUserQuestionAsker()},
		{ConversationID: protocol.SlackThreadConversationID("D123", "333.444"), Agent: "helper", UserQuestionAsker: protocol.NoUserQuestionAsker()},
	}, created)
}

func TestThreadBridgeManagerDoesNotStartThreadWhenStoreIsUnavailable(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	store, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, testLogger())
	require.NoError(t, err)
	require.NoError(t, store.Stop())

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge {
		return bridge
	})
	runTestManager(t, manager)

	err = manager.StartThread(t.Context(), "main", slackTarget("D123", "111.222"), newThreadInboundMessage("first", "111.222", "111.222"))
	require.ErrorContains(t, err, "read managed conversation")
	assert.Empty(t, bridge.StopCalls())
	assert.Empty(t, submittedMessages(bridge))
}

// A thread whose record cannot be persisted keeps no bridge: the new bridge is
// stopped and forgotten, so a retry starts a fresh one.
func TestThreadBridgeManagerDropsBridgeWhenThreadIsNotPersisted(t *testing.T) {
	store := newWorkspaceSessionService(t)
	bridges := make([]*directBridgeMock, 0, 2)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge {
		bridge := newDirectBridgeMock()
		bridges = append(bridges, bridge)

		return bridge
	})
	runTestManager(t, manager)

	_, err := store.db.ExecContext(t.Context(), `ALTER TABLE managed_conversations ADD CONSTRAINT reject_thread CHECK (conversation_id <> 'slack-thread:D123:111.222') NOT VALID`)
	require.NoError(t, err)
	err = manager.StartThread(t.Context(), "main", slackTarget("D123", "111.222"), newThreadInboundMessage("first", "111.222", "111.222"))
	require.ErrorContains(t, err, "persist Slack thread bridge")
	require.Len(t, bridges, 1)
	assert.Len(t, bridges[0].StopCalls(), 1)
	assert.Empty(t, submittedMessages(bridges[0]))

	_, err = store.db.ExecContext(t.Context(), `ALTER TABLE managed_conversations DROP CONSTRAINT reject_thread`)
	require.NoError(t, err)
	require.NoError(t, manager.StartThread(t.Context(), "main", slackTarget("D123", "111.222"), newThreadInboundMessage("retry", "111.222", "111.222")))
	require.Len(t, bridges, 2, "the retry starts a fresh bridge")
	assert.Len(t, submittedMessages(bridges[1]), 1)
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
	runTestManager(t, manager)

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
	runTestManager(t, manager)

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
	runTestManager(t, manager)

	inbound := newThreadInboundMessage("ship it", "222.333", "111.222")
	inbound.SlackReply.RecipientTeamID = "T123"
	inbound.SlackReply.RecipientUserID = "U456"
	require.NoError(t, manager.StartGoalInThread(t.Context(), "", "ship it", "", 5, slackTarget("D123", "111.222"), inbound))
	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, protocol.GoalActionKickoff, submittedMessages(bridge)[0].GoalAction)

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
	runTestManager(t, manager)

	require.NoError(t, manager.StartActiveGoals())
	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, protocol.GoalActionContinue, submittedMessages(bridge)[0].GoalAction)
	assert.Equal(t, "Continue the active goal loop.", submittedMessages(bridge)[0].Text)
	assert.Equal(t, conversationID, submittedMessages(bridge)[0].ConversationID)
	assert.Equal(t, &protocol.SlackReplyTarget{RecipientTeamID: "T123", RecipientUserID: "U456"}, submittedMessages(bridge)[0].SlackReply)
}

// A resumed turn continues its goal itself when it finishes; a startup kick would double it.
func TestThreadBridgeManagerSkipsActiveGoalContinuationForResumedTurn(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner"}))
	require.NoError(t, store.BeginGoal(conversationID, "ship it", "", 5, "", ""))
	seedActiveTurn(t, store, conversationID, "turn-resumed", nil)

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	runTestManager(t, manager)

	require.NoError(t, manager.StartActiveGoals())
	assert.Empty(t, submittedMessages(bridge))
}

func TestThreadBridgeManagerRejectsDuplicateActiveGoal(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "first", "", 5, "", ""))

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	runTestManager(t, manager)

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
	runTestManager(t, manager)
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
	runTestManager(t, manager)
	require.NoError(t, manager.PickLaterWork(t.Context(), conversationID))
	require.Len(t, bridge.PickLaterWorkCalls(), 1)
	require.NoError(t, manager.PickLaterWork(t.Context(), ""))
	require.NoError(t, manager.PickLaterWork(t.Context(), protocol.SlackThreadConversationID("D999", "9.9")))
	require.Len(t, bridge.PickLaterWorkCalls(), 1)
}

func TestStartQueuedConversationsReportsLaterWorkErrors(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{}))
	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "one"}))
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge {
		return newDirectBridgeMock()
	})
	runTestManager(t, manager)
	require.ErrorContains(t, manager.StartQueuedConversations(), "text thread agent is required")
}

func TestStartQueuedConversationsWakesEachRecordedConversationOnce(t *testing.T) {
	store := newWorkspaceSessionService(t)
	first := protocol.SlackThreadConversationID("D123", "111.222")
	second := protocol.SlackThreadConversationID("D124", "222.333")

	require.NoError(t, store.UpsertThread(first, ThreadState{Agent: "main"}))
	require.NoError(t, store.UpsertThread(second, ThreadState{Agent: "main"}))
	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: first, Message: "one"}))
	require.NoError(t, store.PutThreadQueueItem("q2", &protocol.ThreadQueueItem{ID: "q2", ConversationID: first, Message: "two"}))
	require.NoError(t, store.PutThreadQueueItem("q3", &protocol.ThreadQueueItem{ID: "q3", ConversationID: second, Message: "three"}))

	woke := map[string]int{}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		bridge := newDirectBridgeMock()
		conversationID := cfg.ConversationID
		bridge.PickLaterWorkFunc = func(context.Context) error {
			woke[conversationID]++
			return nil
		}

		return bridge
	})
	runTestManager(t, manager)

	require.NoError(t, manager.StartQueuedConversations())
	require.Equal(t, map[string]int{first: 1, second: 1}, woke)
	require.Len(t, manager.bridges, 2)
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
	runTestManager(t, manager)

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
	runTestManager(t, manager)

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
	runTestManager(t, manager)
	_, err := manager.RegisterThread(slackTarget("", ""), "main")
	require.ErrorContains(t, err, "text thread target is required")

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
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
	runTestManager(t, manager)

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

func TestThreadBridgeManagerStartNewThreadCreatesWebSession(t *testing.T) {
	// The fake CLI stands in for the host tailscale binary that RocketClaw shells out to.
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tailscale"), []byte("#!/bin/sh\n[ \"$*\" = \"ip -4\" ] && echo 100.95.197.99\n"), 0o755))
	t.Setenv("PATH", bin)

	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, "skills"), 0o755))
	writeAppTestAgent(t, workspace, "main", "---\ndescription: Test agent\nmodel: gpt-5.5\n---\nPrompt\n")

	store := newWorkspaceSessionService(t)
	bridge := newDirectBridgeMock()

	var created Config

	manager := newThreadBridgeManager(&config.Config{Workspace: workspace, Web: config.WebConfig{ListenAddress: "127.0.0.1:8080"}}, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		created = cfg
		return bridge
	})
	runTestManager(t, manager)

	result, err := manager.StartNewThread(t.Context(), &protocol.StartNewThreadRequest{CurrentAgent: "main", Title: "Child", Prompt: " literal $(date) ", CreatedBy: "Alice"})
	require.NoError(t, err)

	conversationID := result.ConversationID
	_, _, slack := protocol.SlackThreadTarget(conversationID)

	require.NotEmpty(t, conversationID)
	assert.False(t, slack)
	assert.Equal(t, "http://100.95.197.99:8080/s/"+base64.RawURLEncoding.EncodeToString([]byte(conversationID)), result.URL)
	assert.Equal(t, Config{ConversationID: conversationID, Agent: "main", UserQuestionAsker: protocol.NoUserQuestionAsker()}, created)
	require.Len(t, submittedMessages(bridge), 1)

	first := submittedMessages(bridge)[0]
	assert.Equal(t, " literal $(date) ", first.Text)
	assert.Contains(t, buildPrompt(first, nil), "\n\n literal $(date) ")
	assert.Equal(t, conversationID, first.ConversationID)
	assert.Equal(t, "System", first.Metadata[protocol.InboundOriginMetadataKey])
	assert.Equal(t, "Text", first.Metadata[protocol.InboundMediaMetadataKey])
	assert.Nil(t, first.SlackReply)

	thread, ok, err := store.Thread(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ThreadState{Agent: "main", CreatedBy: "Alice"}, thread)

	var name string
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT name FROM managed_conversations WHERE conversation_id = $1`, conversationID).Scan(&name))
	assert.Equal(t, "Child", name)

	second, err := manager.StartNewThread(t.Context(), &protocol.StartNewThreadRequest{CurrentAgent: "main", Title: "Child", Prompt: "again"})
	require.NoError(t, err)
	assert.NotEqual(t, conversationID, second.ConversationID)
}

func TestThreadBridgeManagerStartNewThreadRejectsUnavailableAgents(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, "skills"), 0o755))
	writeAppTestAgent(t, workspace, "main", "---\ndescription: Test agent\nmodel: gpt-5.5\n---\nPrompt\n")

	store := newWorkspaceSessionService(t)
	manager := newThreadBridgeManager(&config.Config{Workspace: workspace, Web: config.WebConfig{ListenAddress: "127.0.0.1:8080"}}, store, slog.New(slog.DiscardHandler), func(Config) directBridge {
		t.Fatal("no conversation should start")
		return nil
	})
	runTestManager(t, manager)

	_, err := manager.StartNewThread(t.Context(), &protocol.StartNewThreadRequest{CurrentAgent: "main", AllowedAgents: []string{"main"}, Agent: "other", Title: "Nightly", Prompt: "run suite"})
	require.ErrorContains(t, err, `agent "other" is not allowed on this source surface`)

	_, err = manager.StartNewThread(t.Context(), &protocol.StartNewThreadRequest{CurrentAgent: "missing", Title: "Nightly", Prompt: "run suite"})
	require.ErrorContains(t, err, `agent "missing" is not configured`)

	t.Setenv("PATH", t.TempDir())

	_, err = manager.StartNewThread(t.Context(), &protocol.StartNewThreadRequest{CurrentAgent: "main", Title: "Nightly", Prompt: "run suite"})
	require.ErrorContains(t, err, "tailscale")
}

func TestThreadBridgeManagerIgnoresUnmanagedThreadTargets(t *testing.T) {
	store := newWorkspaceSessionService(t)
	created := 0
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge {
		created++

		return newDirectBridgeMock()
	})
	runTestManager(t, manager)

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

func TestThreadBridgeManagerStopStopsActiveBridges(t *testing.T) {
	store := newWorkspaceSessionService(t)
	bridges := make([]*directBridgeMock, 0, 2)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge {
		bridge := newDirectBridgeMock()
		bridges = append(bridges, bridge)

		return bridge
	})
	shutdown := runTestManager(t, manager)

	require.NoError(t, manager.StartThread(context.Background(), "main", slackTarget("D123", "111.222"), newThreadInboundMessage("first", "111.222", "111.222")))
	require.NoError(t, manager.StartThread(context.Background(), "main", slackTarget("D123", "333.444"), newThreadInboundMessage("second", "333.444", "333.444")))
	require.NoError(t, shutdown())

	require.Len(t, bridges, 2)
	require.Len(t, bridges[0].StopCalls(), 1)
	require.Len(t, bridges[1].StopCalls(), 1)

	require.NoError(t, manager.StartThread(context.Background(), "main", slackTarget("D123", "555.666"), newThreadInboundMessage("late", "555.666", "555.666")))
	require.Len(t, bridges, 3)
	assert.Len(t, bridges[2].StopCalls(), 1)
	assert.Empty(t, bridges[2].RunCalls())
	assert.Len(t, bridges[2].SubmitCalls(), 1)
}

// runTestManager runs manager's bridge loops the way app.go does and returns the
// shutdown that cancels them and waits for every loop; test cleanup also runs it.
func runTestManager(t *testing.T, manager *threadBridgeManager) func() error {
	t.Helper()

	ctx, cancel := context.WithCancelCause(context.Background())

	var run errgroup.Group

	run.Go(func() error { return manager.Run(ctx) })

	shutdown := sync.OnceValue(func() error {
		cancel(errShutdown)

		return run.Wait()
	})

	t.Cleanup(func() { assert.NoError(t, shutdown()) })

	return shutdown
}

func newDirectBridgeMock() *directBridgeMock {
	mock := &directBridgeMock{}

	mock.RunFunc = func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	}
	mock.StopFunc = func() error { return nil }
	mock.SubmitFunc = func(context.Context, *protocol.InboundMessage) error { return nil }
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
	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, text, true)
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
	service, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
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
