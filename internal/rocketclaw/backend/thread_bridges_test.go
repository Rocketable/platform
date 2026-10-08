package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

func TestRuntimeWorkflowDescriptionsListsSavedWorkflows(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	require.NoError(t, root.MkdirAll(".rocketclaw/workflows", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/workflows/audit.star", []byte("meta = {\"name\": \"audit\", \"description\": \"Audit routes\"}\ndef main(args): return args\n"), 0o600))
	require.NoError(t, root.Close())

	descriptions, err := (&Runtime{Cfg: config.NewLockedConfig(&config.Config{Workspace: workspace})}).WorkflowDescriptions()
	require.NoError(t, err)
	assert.Equal(t, []protocol.WorkflowDescription{{Name: "audit", Description: "Audit routes"}}, descriptions)

	_, err = (&Runtime{Cfg: config.NewLockedConfig(&config.Config{Workspace: filepath.Join(workspace, "missing")})}).WorkflowDescriptions()
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
			err = skel.ReplaceRuntimeAssetsAfterValidation(workspace, cfg.RuntimeDirName(), nil, slog.New(slog.DiscardHandler), config.NewLockedConfig(cfg), func(runtimeDir string) error {
				return validateWorkflowDefinitions(cfg, runtimeDir)
			})
			require.ErrorContains(t, err, tt.want)
			data, err := root.ReadFile(".rocketclaw/workflows/live.star")
			require.NoError(t, err)
			assert.Equal(t, "live", string(data))
		})
	}
}

func TestThreadBridgeManagerStartsPendingScheduledMessageBridges(t *testing.T) {
	workspace := t.TempDir()

	store := newWorkspaceSessionService(t)
	for _, cfg := range []Config{
		{ConversationID: protocol.SlackThreadConversationID("D123", "111.222"), Agent: "planner", StartNewThread: inertStartNewThread, SessionService: store},
		{ConversationID: protocol.SlackThreadConversationID("D123", "333.444"), Agent: "helper", StartNewThread: inertStartNewThread, SessionService: store},
	} {
		bridge := NewConversation(config.NewLockedConfig(&config.Config{Workspace: workspace}), discardPublisher{}, &cfg, slog.New(slog.DiscardHandler))
		require.NoError(t, store.UpsertThread(cfg.ConversationID, ThreadState{Agent: cfg.Agent}))
		require.NoError(t, startTestBridge(t.Context(), bridge))
		require.NoError(t, bridge.ScheduleMessage(new(protocol.InboundMessage), time.Hour, "later", false))
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
		{ConversationID: protocol.SlackThreadConversationID("D123", "111.222"), Agent: "selected"},
		{ConversationID: protocol.SlackThreadConversationID("D123", "333.444"), Agent: "helper"},
	}, created)
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

	start := &threadStart{conversationID: "slack-thread:D123:111.222", agent: "main"}

	_, err := store.db.ExecContext(t.Context(), `ALTER TABLE managed_conversations ADD CONSTRAINT reject_thread CHECK (conversation_id <> 'slack-thread:D123:111.222') NOT VALID`)
	require.NoError(t, err)
	_, err = manager.ensureStartedThread(start)
	require.ErrorContains(t, err, "persist new Web session")
	require.Len(t, bridges, 1)
	assert.Len(t, bridges[0].StopCalls(), 1)

	_, err = store.db.ExecContext(t.Context(), `ALTER TABLE managed_conversations DROP CONSTRAINT reject_thread`)
	require.NoError(t, err)
	_, err = manager.ensureStartedThread(start)
	require.NoError(t, err)
	require.Len(t, bridges, 2, "the retry starts a fresh bridge")
}

func TestRuntimeSwitchesConversationAgent(t *testing.T) {
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
}

func TestThreadBridgeManagerMentionThreadRecognizesReportThreads(t *testing.T) {
	store := newWorkspaceSessionService(t)
	require.NoError(t, store.UpsertThread(protocol.SlackThreadConversationID("C123", "1.1"), ThreadState{Agent: "main", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, store.RegisterExternalMCPConversation("external", "main", &ExternalMCPSessionState{PrivateConversationID: "external_mcp:private", ManagedConversationID: protocol.SlackThreadConversationID("C123", "2.2"), Agent: "main"}))
	require.NoError(t, store.UpsertThread(protocol.SlackThreadConversationID("C123", "3.3"), ThreadState{Agent: "main"}))

	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return newDirectBridgeMock() })

	for _, tt := range []struct {
		name, threadTS   string
		recorded, report bool
	}{
		{name: "cron report", threadTS: "1.1", recorded: true, report: true},
		{name: "External MCP thread", threadTS: "2.2", recorded: true, report: true},
		{name: "mention thread", threadTS: "3.3", recorded: true},
		{name: "unknown thread", threadTS: "4.4"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorded, report, err := manager.MentionThread(slackTarget("C123", tt.threadTS))
			require.NoError(t, err)
			assert.Equal(t, tt.recorded, recorded)
			assert.Equal(t, tt.report, report)
		})
	}
}

// A mention is accepted once per conversation: a redelivery while it waits, mid-turn, after a
// restart, or after its turn finished starts nothing, and a mention never steers the running turn.
func TestThreadBridgeManagerSubmitMentionQueuesEachMentionOnce(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	target := slackTarget("D123", "111.222")
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 4), stopCh: make(chan struct{}), handling: true, inputOpen: true}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	submitTo := func(manager *threadBridgeManager, text, messageTS string) bool {
		t.Helper()

		inbound := newThreadInboundMessage(text, messageTS, "111.222")
		protocol.SetInboundAllowedAgents(inbound, []string{"main", "planner"})
		accepted, err := manager.SubmitMention(t.Context(), "main", target, inbound)
		require.NoError(t, err)

		return accepted
	}
	submit := func(text, messageTS string) bool { return submitTo(manager, text, messageTS) }

	require.True(t, submit("first", "111.222"))
	require.True(t, submit("second", "222.333"))
	require.False(t, submit("first", "111.222"), "a redelivery while the mention waits")

	thread, recorded, err := store.Thread(conversationID)
	require.NoError(t, err)
	require.True(t, recorded)
	assert.Equal(t, ThreadState{Agent: "main"}, thread)

	queue, err := store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, queue, 2)
	assert.Equal(t, []string{"slack:D123:111.222", "slack:D123:222.333"}, []string{queue[0].ID, queue[1].ID})
	assert.Equal(t, "first", queue[0].Inbound.Text)
	assert.Equal(t, protocol.InboundKindEnqueue, queue[1].Kind, "a waiting mention lists as QUEUE")

	assert.Empty(t, bridge.steers, "a mention never steers the running turn")
	require.Len(t, bridge.requestCh, 1, "the first mention waits for the running turn to finish")

	request := <-bridge.requestCh
	assert.Equal(t, "slack:D123:111.222", request.queueItemID)
	assert.Equal(t, protocol.InboundKindPrompt, request.inbound.Kind)
	assert.Equal(t, "main,planner", request.inbound.Metadata[protocol.InboundAllowedAgentsMetadataKey])
	assert.Equal(t, &protocol.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}, request.inbound.SlackReply)

	activated, err := bridge.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, activated)
	require.False(t, submit("first", "111.222"), "a redelivery mid-turn")

	restarted := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return newDirectBridgeMock() })
	require.False(t, submitTo(restarted, "first", "111.222"), "a redelivery after a restart, while the active turn waits to resume")

	entry := testSessionEntry("first", "answer")
	entry.TurnID = request.turnID
	entry.ReplayInput[0] = json.RawMessage(`{"type":"message","role":"user","input_id":"slack:D123:111.222","content":"first"}`)
	_, err = store.finishTurn(t.Context(), request.turnID, &turnFinish{store: newSessionStore(conversationID, store), entries: []rocketcode.SessionEntry{*entry}, outbound: protocol.NewOutboundMessage(conversationID, "answer")})
	require.NoError(t, err)
	require.NoError(t, store.closeTurn(t.Context(), request.turnID))
	require.False(t, submit("first", "111.222"), "a redelivery after its turn finished")

	queue, err = store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, queue, 1)
	assert.Equal(t, "slack:D123:222.333", queue[0].ID)
}

// A mention committed while its bridge is stopped stays accepted and waits for the next start.
func TestThreadBridgeManagerSubmitMentionKeepsMentionForStoppedBridge(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, stopped: true}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })

	inbound := newThreadInboundMessage("first", "111.222", "111.222")
	protocol.SetInboundAllowedAgents(inbound, []string{"main"})
	accepted, err := manager.SubmitMention(t.Context(), "main", slackTarget("D123", "111.222"), inbound)
	require.ErrorIs(t, err, protocol.ErrBridgeStopped)
	assert.True(t, accepted)

	queue, err := store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	assert.Len(t, queue, 1)
}

func TestThreadBridgeManagerStartsActiveGoalAfterRestart(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner"}))
	require.NoError(t, beginGoalDB(t.Context(), store.db, conversationID, &GoalState{Objective: "ship it", MaxTurns: 5, SlackRecipientTeamID: "T123", SlackRecipientUserID: "U456"}))

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
		assert.Equal(t, Config{ConversationID: conversationID, Agent: "planner"}, cfg)

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
	require.NoError(t, store.BeginGoal(conversationID, "ship it", "", 5))
	seedActiveTurn(t, store, conversationID, "turn-resumed", nil)

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return bridge })
	runTestManager(t, manager)

	require.NoError(t, manager.StartActiveGoals())
	assert.Empty(t, submittedMessages(bridge))
}

func TestThreadBridgeManagerAllowsGoalAfterCompletedGoal(t *testing.T) {
	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "first", "", 5))
	_, err := store.UpdateGoalStatus(conversationID, GoalStatusComplete, "done")
	require.NoError(t, err)
	require.NoError(t, store.BeginGoal(conversationID, "second", "", 5))

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
	require.NoError(t, store.BeginGoal(conversationID, "first", "", 5))

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

func TestThreadBridgeManagerStashListAndDeleteQueue(t *testing.T) {
	store := newWorkspaceSessionService(t)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge { return newDirectBridgeMock() })
	runTestManager(t, manager)

	target := protocol.SlackThreadConversationID("C123", "111.222")
	other := protocol.SlackThreadConversationID("C123", "333.444")

	require.NoError(t, manager.stashQueueItem(t.Context(), target, &protocol.ThreadQueueItem{ID: "q1", Message: "first", Principal: "U1", StashAt: time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC), SlackChannel: "C123", SlackTS: "1"}))
	require.NoError(t, manager.stashQueueItem(t.Context(), target, &protocol.ThreadQueueItem{ID: "q2", Message: "second", Principal: "U1", StashAt: time.Date(2026, 8, 24, 14, 0, 0, 0, time.UTC), SlackChannel: "C123", SlackTS: "2"}))
	require.NoError(t, manager.stashQueueItem(t.Context(), other, &protocol.ThreadQueueItem{ID: "other", Message: "keep", Principal: "U2", StashAt: time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)}))

	items, err := manager.queueItems(target)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, []string{"q1", "q2"}, []string{items[0].ID, items[1].ID})

	removed, err := manager.deleteQueueItem(t.Context(), target, "other")
	require.NoError(t, err)
	require.False(t, removed)
	removed, err = manager.deleteQueueItem(t.Context(), target, "q2")
	require.NoError(t, err)
	require.True(t, removed)

	items, err = manager.queueItems(target)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "q1", items[0].ID)

	otherItems, err := manager.queueItems(other)
	require.NoError(t, err)
	require.Len(t, otherItems, 1)
	assert.Equal(t, "other", otherItems[0].ID)
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

	manager := newThreadBridgeManager(config.NewLockedConfig(&config.Config{Workspace: workspace, Web: config.WebConfig{ListenAddress: "127.0.0.1:8080"}}), store, slog.New(slog.DiscardHandler), func(cfg Config) directBridge {
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
	assert.Equal(t, Config{ConversationID: conversationID, Agent: "main"}, created)
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
	manager := newThreadBridgeManager(config.NewLockedConfig(&config.Config{Workspace: workspace, Web: config.WebConfig{ListenAddress: "127.0.0.1:8080"}}), store, slog.New(slog.DiscardHandler), func(Config) directBridge {
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

func TestThreadBridgeManagerStopStopsActiveBridges(t *testing.T) {
	store := newWorkspaceSessionService(t)
	bridges := make([]*directBridgeMock, 0, 2)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), func(Config) directBridge {
		bridge := newDirectBridgeMock()
		bridges = append(bridges, bridge)

		return bridge
	})
	shutdown := runTestManager(t, manager)

	for _, id := range []string{"first", "second"} {
		_, err := manager.ensureStartedThread(&threadStart{conversationID: id, agent: "main"})
		require.NoError(t, err)
	}

	require.NoError(t, shutdown())

	require.Len(t, bridges, 2)
	require.Len(t, bridges[0].StopCalls(), 1)
	require.Len(t, bridges[1].StopCalls(), 1)

	_, err := manager.ensureStartedThread(&threadStart{conversationID: "late", agent: "main"})
	require.NoError(t, err)
	require.Len(t, bridges, 3)
	assert.Len(t, bridges[2].StopCalls(), 1)
	assert.Empty(t, bridges[2].RunCalls())
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
