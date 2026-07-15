package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunReportsPendingRestartNotificationStartupErrors(t *testing.T) {
	workspace := shortTempDir(t)
	service, err := harnessbridge.NewSessionServiceIn(workspace, config.DefaultRuntimeDir, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NoError(t, service.MarkRestartRequester(context.Background(), "main"))
	require.NoError(t, service.Stop(context.Background()))

	db, err := sql.Open("sqlite", filepath.Join(workspace, config.DefaultRuntimeDir, "state.sqlite3"))
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `CREATE TRIGGER fail_session_entries_insert BEFORE INSERT ON session_entries BEGIN SELECT RAISE(FAIL, 'no append'); END`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	err = Run(context.Background(), &config.Config{Workspace: workspace}, "", slog.New(slog.DiscardHandler))
	require.ErrorContains(t, err, "apply pending restart notifications")
}

func TestRunRejectsUnresolvedAgentModelAtStartup(t *testing.T) {
	workspace := shortTempDir(t)
	agentsRoot := filepath.Join(workspace, "agents")
	require.NoError(t, os.MkdirAll(agentsRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsRoot, "main.md"), []byte("---\ndescription: Main\nmodel: '{{ model \"missing\" }}'\n---\nPrompt\n"), 0o600))

	err := Run(t.Context(), &config.Config{Workspace: workspace}, "", slog.New(slog.DiscardHandler))
	require.ErrorContains(t, err, "validate rocketcode definitions")
	require.ErrorContains(t, err, `model "missing" is not configured`)
}

func TestThreadBridgeManagerCreatesSeparateBridgesPerThreadAndPersistsThem(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionService(t, workspace)
	created := make([]bridgeConfig, 0, 2)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg bridgeConfig) directBridge {
		created = append(created, cfg)
		return new(fakeDirectBridge)
	})

	require.NoError(t, manager.StartThread(t.Context(), "main", slackTarget("D123", "111.222"), newThreadInboundMessage("first", "111.222", "111.222")))
	require.NoError(t, manager.StartThread(t.Context(), "factory", slackTarget("D123", "333.444"), newThreadInboundMessage("second", "333.444", "333.444")))

	require.Len(t, created, 2)

	thread, ok, err := store.Thread(created[0].ConversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, harnessbridge.ThreadState{Agent: "main"}, thread)
}

func TestThreadBridgeManagerStartsPendingScheduledMessageBridges(t *testing.T) {
	workspace := t.TempDir()

	store := newTestSessionService(t, workspace)
	for _, cfg := range []harnessbridge.Config{
		{ConversationID: harnessbridge.SlackThreadConversationID("D123", "111.222"), Agent: "planner", StartNewThread: inertStartNewThread, SessionService: store},
		{ConversationID: harnessbridge.SlackThreadConversationID("D123", "333.444"), Agent: "helper", StartNewThread: inertStartNewThread, SessionService: store},
	} {
		bridge := harnessbridge.NewConversation(&config.Config{Workspace: workspace}, nil, &cfg, slog.New(slog.DiscardHandler))
		require.NoError(t, bridge.Start(t.Context()))
		require.NoError(t, bridge.ScheduleMessage(time.Hour, "later", false))
		require.NoError(t, bridge.Stop())
	}

	created := make([]bridgeConfig, 0, 2)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg bridgeConfig) directBridge {
		created = append(created, cfg)
		return new(fakeDirectBridge)
	})

	require.NoError(t, manager.StartPendingScheduledMessages(map[string]bool{}))
	require.Len(t, created, 2)
	assert.ElementsMatch(t, []bridgeConfig{
		{ConversationID: harnessbridge.SlackThreadConversationID("D123", "111.222"), Agent: "planner", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}},
		{ConversationID: harnessbridge.SlackThreadConversationID("D123", "333.444"), Agent: "helper", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}},
	}, created)
}

func TestThreadBridgeManagerSkipsScheduledMessageBridgeDuringActiveTurnRecovery(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionService(t, workspace)
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	bridge := harnessbridge.NewConversation(&config.Config{Workspace: workspace}, nil, &harnessbridge.Config{ConversationID: conversationID, Agent: "planner", StartNewThread: inertStartNewThread, SessionService: store}, slog.New(slog.DiscardHandler))
	require.NoError(t, bridge.Start(t.Context()))
	require.NoError(t, bridge.ScheduleMessage(time.Hour, "later", false))
	require.NoError(t, bridge.Stop())

	created := 0
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge {
		created++

		return new(fakeDirectBridge)
	})

	require.NoError(t, manager.StartPendingScheduledMessages(map[string]bool{conversationID: true}))
	assert.Zero(t, created)
}

func TestThreadBridgeManagerStopsStartedThreadWhenPersistFails(t *testing.T) {
	store, err := harnessbridge.NewSessionServiceIn(t.TempDir(), config.DefaultRuntimeDir, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NoError(t, store.Stop(context.Background()))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge {
		return bridge
	})

	err = manager.StartThread(t.Context(), "main", slackTarget("D123", "111.222"), newThreadInboundMessage("first", "111.222", "111.222"))
	require.ErrorContains(t, err, "persist Slack thread bridge")
	assert.Equal(t, 1, bridge.stops)
	assert.Empty(t, bridge.submits)
}

func TestThreadBridgeManagerSwitchesThreadAgent(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionService(t, workspace)
	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })

	replyTarget := slackTarget("D123", "111.222")
	require.NoError(t, manager.StartThread(t.Context(), "main", replyTarget, newThreadInboundMessage("first", "111.222", "111.222")))

	handled, err := manager.SwitchThreadAgent(replyTarget, "planner")
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, []string{"submit:first", "switch:planner"}, bridge.ops)

	thread, ok, err := store.Thread(harnessbridge.SlackThreadConversationID("D123", "111.222"))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "planner", thread.Agent)
}

func TestThreadBridgeManagerReadsThreadAgent(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, harnessbridge.ThreadState{Agent: " planner "}))

	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return new(fakeDirectBridge) })

	agent, handled, err := manager.ThreadAgent(slackTarget("D123", "111.222"))
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, "planner", agent)

	conversationID = harnessbridge.SlackThreadConversationID("D123", "333.444")
	require.NoError(t, store.UpsertThread(conversationID, harnessbridge.ThreadState{Agent: " "}))

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
	store := newTestSessionService(t, t.TempDir())
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, harnessbridge.ThreadState{Agent: "planner"}))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg bridgeConfig) directBridge {
		assert.Equal(t, bridgeConfig{ConversationID: conversationID, Agent: "planner", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}}, cfg)

		return bridge
	})

	require.NoError(t, manager.StartGoalInThread(t.Context(), "", "ship it", "", 5, slackTarget("D123", "111.222"), newThreadInboundMessage("ship it", "222.333", "111.222")))
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "goal", bridge.submits[0].Label)

	goal, ok, err := store.Goal(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "ship it", goal.Objective)
}

func TestThreadBridgeManagerStartsActiveGoalAfterRestart(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, harnessbridge.ThreadState{Agent: "planner"}))
	require.NoError(t, store.BeginGoal(conversationID, "ship it", "", 5))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg bridgeConfig) directBridge {
		assert.Equal(t, bridgeConfig{ConversationID: conversationID, Agent: "planner", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}}, cfg)

		return bridge
	})

	require.NoError(t, manager.StartActiveGoals(map[string]bool{}))
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "goal_continuation", bridge.submits[0].Label)
	assert.Equal(t, "Continue the active goal loop.", bridge.submits[0].Text)
	assert.Equal(t, conversationID, bridge.submits[0].ConversationID)
	assert.Equal(t, &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}, bridge.submits[0].SlackReply)
}

func TestThreadBridgeManagerSkipsActiveGoalContinuationDuringActiveTurnRecovery(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, harnessbridge.ThreadState{Agent: "planner"}))
	require.NoError(t, store.BeginGoal(conversationID, "ship it", "", 5))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })

	require.NoError(t, manager.StartActiveGoals(map[string]bool{conversationID: true}))
	assert.Empty(t, bridge.submits)
}

func TestThreadBridgeManagerRejectsDuplicateActiveGoal(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "first", "", 5))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })

	err := manager.StartGoalInThread(t.Context(), "main", "second", "", 5, slackTarget("D123", "111.222"), newThreadInboundMessage("second", "222.333", "111.222"))
	require.ErrorIs(t, err, harnessbridge.ErrGoalAlreadyActive)
	assert.Empty(t, bridge.submits)
}

func TestThreadBridgeManagerAllowsGoalAfterCompletedGoal(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "first", "", 5))
	_, err := store.UpdateGoalStatus(conversationID, harnessbridge.GoalStatusComplete, "done")
	require.NoError(t, err)

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })
	require.NoError(t, manager.StartGoalInThread(t.Context(), "main", "second", "", 5, slackTarget("D123", "111.222"), newThreadInboundMessage("second", "222.333", "111.222")))

	goal, ok, err := store.Goal(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "second", goal.Objective)
	assert.Equal(t, harnessbridge.GoalStatusActive, goal.Status)
}

func TestThreadBridgeManagerInterruptSlackThreadInterruptsActiveTurn(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "first", "", 5))

	marker := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333", ThreadTS: "111.222"}
	bridge := &fakeDirectBridge{interruptResult: &events.InboundMessage{SlackReply: marker}}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })
	require.NoError(t, manager.StartThread(t.Context(), "main", slackTarget("D123", "111.222"), newThreadInboundMessage("start", "111.222", "111.222")))

	result, err := manager.InterruptThread(slackTarget("D123", "111.222"))
	require.NoError(t, err)
	assert.Equal(t, marker, result.SlackReply)
	assert.Equal(t, 1, bridge.interrupts)

	goal, ok, err := store.Goal(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, harnessbridge.GoalStatusStopped, goal.Status)
}

func TestThreadBridgeManagerRegistersCronThreadWithoutSubmitting(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionService(t, workspace)
	bridge := new(fakeDirectBridge)

	var created bridgeConfig

	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg bridgeConfig) directBridge {
		created = cfg
		return bridge
	})

	require.NoError(t, manager.RegisterCronThread(t.Context(), slackTarget("C123", "111.222"), "planner"))

	conversationID := harnessbridge.SlackThreadConversationID("C123", "111.222")
	assert.Equal(t, bridgeConfig{ConversationID: conversationID, Agent: "planner", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}}, created)
	assert.Empty(t, bridge.submits)

	thread, ok, err := store.Thread(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, harnessbridge.ThreadState{Agent: "planner", CreatedBy: harnessbridge.ThreadCreatedByCron}, thread)
}

func TestThreadBridgeManagerRejectsMissingSlackThreadTarget(t *testing.T) {
	manager := newThreadBridgeManager(nil, newTestSessionService(t, t.TempDir()), slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return new(fakeDirectBridge) })

	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	err := manager.StartThread(t.Context(), "main", slackTarget("", ""), inbound)
	require.ErrorContains(t, err, "slack thread target is required")

	inbound.SlackReply = &events.SlackReplyTarget{ChannelID: " ", ThreadTS: " "}
	err = manager.StartThread(t.Context(), "main", slackTarget(" ", " "), inbound)
	require.ErrorContains(t, err, "slack thread target is required")
}

func TestThreadBridgeManagerSubmitsPersistedThreadReply(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, harnessbridge.ThreadState{Agent: "factory"}))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })

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
	store := newTestSessionService(t, t.TempDir())
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, harnessbridge.ThreadState{Agent: "planner", CreatedBy: harnessbridge.ThreadCreatedByCron}))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })

	inbound := newThreadInboundMessage("follow up", "222.333", "")
	handled, err := manager.SubmitThreadReply(context.Background(), slackTarget("D123", "111.222"), inbound)
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "true", bridge.submits[0].Metadata[events.InboundStartNewThreadDisabledMetadataKey])
}

func TestThreadBridgeManagerDisablesStartNewThreadForCronThreadGoalStart(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, harnessbridge.ThreadState{Agent: "planner", CreatedBy: harnessbridge.ThreadCreatedByCron}))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge { return bridge })

	inbound := newThreadInboundMessage("goal", "222.333", "")
	err := manager.StartGoalInThread(context.Background(), "planner", "goal", "", 3, slackTarget("D123", "111.222"), inbound)
	require.NoError(t, err)
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "true", bridge.submits[0].Metadata[events.InboundStartNewThreadDisabledMetadataKey])
}

func TestThreadBridgeManagerStartNewThreadUsesFreshThreadLocalConversation(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, "skills"), 0o755))
	writeAppTestAgent(t, workspace, "main", "---\ndescription: Test agent\nmodel: gpt-5.5\n---\nPrompt\n")

	store := newTestSessionService(t, workspace)
	bridge := new(fakeDirectBridge)

	var created bridgeConfig

	manager := newThreadBridgeManager(&config.Config{Workspace: workspace}, store, slog.New(slog.DiscardHandler), func(cfg bridgeConfig) directBridge {
		created = cfg
		return bridge
	})

	rootCalls := 0
	result, err := manager.StartNewThread(t.Context(), &events.StartNewThreadRequest{Source: events.SourceSlack, SourceConversationID: "source-1", CurrentAgent: "main", Title: "Child", Prompt: " literal $(date) ", SlackReply: &events.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"}}, func(context.Context, *events.StartNewThreadRequest) (events.StartNewThreadRootResult, error) {
		rootCalls++
		return events.StartNewThreadRootResult{Target: events.TextConversationTarget{ChannelID: "C2", MessageID: "2", ThreadID: "2"}, URL: "https://example.invalid/thread"}, nil
	})
	require.NoError(t, err)

	conversationID := harnessbridge.SlackThreadConversationID("C2", "2")

	assert.Equal(t, 1, rootCalls)
	assert.Equal(t, events.StartNewThreadResult{ConversationID: conversationID, URL: "https://example.invalid/thread"}, result)
	assert.Equal(t, bridgeConfig{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}}, created)
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, []string{"submit: literal $(date) "}, bridge.ops)
	assert.Equal(t, " literal $(date) ", bridge.submits[0].Text)
	assert.Equal(t, conversationID, bridge.submits[0].ConversationID)
	assert.Equal(t, "System", bridge.submits[0].Metadata[events.InboundOriginMetadataKey])
	assert.Equal(t, "Text", bridge.submits[0].Metadata[events.InboundMediaMetadataKey])
	require.NotNil(t, bridge.submits[0].SlackReply)
	assert.Equal(t, events.SlackReplyTarget{ChannelID: "C2", MessageTS: "2", ThreadTS: "2"}, *bridge.submits[0].SlackReply)
}

func TestThreadBridgeManagerIgnoresUnmanagedThreadTargets(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	created := 0
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge {
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

	err := manager.SubmitExternalMCP(context.Background(), "main", " ", newThreadInboundMessage("reply", "222.333", "111.222"), harnessbridge.NoopActivationHook)
	require.ErrorContains(t, err, "text thread conversation ID is required")
	assert.Zero(t, created)
}

func TestThreadBridgeManagerExternalMCPAndSlackShareBoundThreadHistory(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionService(t, workspace)
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")

	require.NoError(t, store.UpsertThread(conversationID, harnessbridge.ThreadState{Agent: "planner"}))

	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg bridgeConfig) directBridge {
		assert.Equal(t, bridgeConfig{ConversationID: conversationID, Agent: "planner", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}}, cfg)
		return bridge
	})

	requestCtx, cancel := context.WithCancel(context.Background())
	require.NoError(t, manager.SubmitExternalMCP(requestCtx, "planner", conversationID, newThreadInboundMessage("initial", "123.456", "111.222"), harnessbridge.NoopActivationHook))
	cancel()

	handled, err := manager.SubmitThreadReply(context.Background(), slackTarget("D123", "111.222"), newThreadInboundMessage("follow up", "222.333", "111.222"))
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, bridge.submits, 2)
	assert.Equal(t, "follow up", bridge.submits[1].Text)
}

func TestThreadBridgeManagerSubmitsSameExternalMCPConversationToOneBridge(t *testing.T) {
	workspace := t.TempDir()
	store := newTestSessionService(t, workspace)
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")

	bridge := new(fakeDirectBridge)
	created := 0
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg bridgeConfig) directBridge {
		created++

		assert.Equal(t, bridgeConfig{ConversationID: conversationID, Agent: "planner", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}}, cfg)

		return bridge
	})

	require.NoError(t, manager.SubmitExternalMCP(context.Background(), "planner", conversationID, newThreadInboundMessage("recovered", "123.456", "111.222"), harnessbridge.NoopActivationHook))
	require.NoError(t, manager.SubmitExternalMCP(context.Background(), "planner", conversationID, newThreadInboundMessage("follow up", "222.333", "111.222"), harnessbridge.NoopActivationHook))

	assert.Equal(t, 1, created)
	require.Len(t, bridge.submits, 2)
	assert.Equal(t, "recovered", bridge.submits[0].Text)
	assert.Equal(t, "follow up", bridge.submits[1].Text)
}

func TestThreadBridgeManagerExternalMCPConversationsUseIndependentBridges(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	bridges := map[string]*fakeDirectBridge{}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg bridgeConfig) directBridge {
		bridge := new(fakeDirectBridge)
		bridges[cfg.ConversationID] = bridge

		return bridge
	})

	firstConversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	secondConversationID := harnessbridge.SlackThreadConversationID("D123", "333.444")
	recovered := events.NewInboundMessage(events.SourceSystem, events.InboundKindPrompt, "recovered_turn", "recovered", false)
	require.NoError(t, manager.SubmitExternalMCP(context.Background(), "planner", firstConversationID, recovered, harnessbridge.NoopActivationHook))

	followup := events.NewInboundMessage(events.SourceExternalMCP, events.InboundKindPrompt, "", "independent", true)
	require.NoError(t, manager.SubmitExternalMCP(context.Background(), "planner", secondConversationID, followup, harnessbridge.NoopActivationHook))

	require.Len(t, bridges, 2)
	assert.Equal(t, "recovered", bridges[firstConversationID].submits[0].Text)
	assert.Equal(t, "independent", bridges[secondConversationID].submits[0].Text)
}

func TestThreadBridgeManagerRecoversActiveTurnInThreadLocalConversation(t *testing.T) {
	conversationID := harnessbridge.SlackThreadConversationID("D123", "111.222")
	bridge := new(fakeDirectBridge)
	manager := newThreadBridgeManager(nil, newTestSessionService(t, t.TempDir()), slog.New(slog.DiscardHandler), func(cfg bridgeConfig) directBridge {
		assert.Equal(t, bridgeConfig{ConversationID: conversationID, Agent: "planner", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RecoveringActiveTurn: true}, cfg)
		return bridge
	})
	turn := &harnessbridge.ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{ConversationKey: conversationID, TurnID: "turn-1", Agent: "planner"}}

	require.NoError(t, manager.RecoverActiveTurn(t.Context(), turn))
	require.Len(t, bridge.submits, 1)
	assert.Equal(t, "turn-1", bridge.submits[0].Text)
}

func TestThreadBridgeManagerStopStopsActiveBridges(t *testing.T) {
	store := newTestSessionService(t, t.TempDir())
	bridges := make([]*fakeDirectBridge, 0, 2)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(bridgeConfig) directBridge {
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

type fakeDirectBridge struct {
	submits         []*events.InboundMessage
	stops           int
	ops             []string
	startedDone     <-chan struct{}
	interrupts      int
	interruptResult *events.InboundMessage
}

func (f *fakeDirectBridge) Start(ctx context.Context) error {
	f.startedDone = ctx.Done()

	return nil
}

func (f *fakeDirectBridge) Stop() error {
	f.stops++

	return nil
}

func (f *fakeDirectBridge) Submit(_ context.Context, msg *events.InboundMessage) error {
	select {
	case <-f.startedDone:
		return context.Canceled
	default:
	}

	f.submits = append(f.submits, msg)
	f.ops = append(f.ops, "submit:"+msg.Text)

	return nil
}

func (f *fakeDirectBridge) SubmitWhenActive(ctx context.Context, msg *events.InboundMessage, activation harnessbridge.ActivationHook) error {
	if err := activation(ctx, msg); err != nil {
		return err
	}

	return f.Submit(ctx, msg)
}

func (f *fakeDirectBridge) RecoverActiveTurn(ctx context.Context, turn *harnessbridge.ActiveTurnState) error {
	inbound := events.NewInboundMessage(events.SourceSystem, events.InboundKindPrompt, "recovered_turn", turn.Checkpoint.TurnID, false)

	return f.Submit(ctx, inbound)
}

func (f *fakeDirectBridge) InterruptActiveTurn() *events.InboundMessage {
	f.interrupts++

	return f.interruptResult
}

func (f *fakeDirectBridge) SwitchAgent(agent string) {
	f.ops = append(f.ops, "switch:"+agent)
}

func newThreadInboundMessage(text, messageTS, threadTS string) *events.InboundMessage {
	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", text, true)
	inbound.SlackReply = &events.SlackReplyTarget{ChannelID: "D123", MessageTS: messageTS, ThreadTS: threadTS}

	return inbound
}

func slackTarget(channelID, threadTS string) events.TextConversationTarget {
	return events.TextConversationTarget{ChannelID: channelID, ThreadID: threadTS}
}

func newTestSessionService(t *testing.T, workspace string) *harnessbridge.SessionService {
	t.Helper()

	service, err := harnessbridge.NewSessionServiceIn(workspace, config.DefaultRuntimeDir, slog.New(slog.DiscardHandler))
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

func inertStartNewThread(context.Context, *events.StartNewThreadRequest) (events.StartNewThreadResult, error) {
	return events.StartNewThreadResult{}, errors.New("start new thread is inert in this test")
}
