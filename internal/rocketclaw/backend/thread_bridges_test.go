package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	workspace := t.TempDir()

	store := newWorkspaceSessionService(t)
	for _, cfg := range []Config{
		{ConversationID: protocol.SlackThreadConversationID("D123", "111.222"), Agent: "planner", SessionService: store},
		{ConversationID: protocol.SlackThreadConversationID("D123", "333.444"), Agent: "helper", SessionService: store},
	} {
		bridge := NewConversation(config.NewLockedConfig(&config.Config{Workspace: workspace}), discardPublisher{}, &cfg, slog.New(slog.DiscardHandler))
		require.NoError(t, store.UpsertThread(cfg.ConversationID, ThreadState{Agent: cfg.Agent}))
		require.NoError(t, startTestBridge(t.Context(), bridge))
		require.NoError(t, bridge.ScheduleMessage(new(protocol.InboundMessage), time.Hour, "later", false))
		require.NoError(t, bridge.Stop())
	}

	require.NoError(t, store.UpsertThread(protocol.SlackThreadConversationID("D123", "111.222"), ThreadState{Agent: "selected"}))

	created := make([]Config, 0, 2)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(cfg *Config) directBridge {
		created = append(created, *cfg)
		return newDirectBridgeMock()
	}})
	runTestManager(t, manager)

	require.NoError(t, manager.StartPendingScheduledMessages(t.Context()))
	require.Len(t, created, 2)
	assert.ElementsMatch(t, []Config{
		{ConversationID: protocol.SlackThreadConversationID("D123", "111.222"), Agent: "selected"},
		{ConversationID: protocol.SlackThreadConversationID("D123", "333.444"), Agent: "helper"},
	}, created)
}

// A thread whose record cannot be persisted keeps no bridge: the new bridge is
// stopped and forgotten, so a retry starts a fresh one.
func TestThreadBridgeManagerDropsBridgeWhenThreadIsNotPersisted(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, "skills"), 0o755))
	writeAppTestAgent(t, workspace, "main", "---\ndescription: Test agent\nmodel: gpt-6.1-sol\n---\nPrompt\n")

	store := newWorkspaceSessionService(t)
	bridges := make([]*directBridgeMock, 0, 2)
	cfg := &config.Config{Workspace: workspace, Web: config.WebConfig{ListenAddress: "127.0.0.1:8080", Tsnet: &config.TsnetConfig{Hostname: "rocketclaw", Origin: "https://rocketclaw.example.ts.net"}}}
	manager := newThreadBridgeManager(config.NewLockedConfig(cfg), store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge {
		bridge := newDirectBridgeMock()
		bridges = append(bridges, bridge)

		return bridge
	}})
	runTestManager(t, manager)

	start := &threadStart{agent: "main"}

	_, err := store.db.ExecContext(t.Context(), `ALTER TABLE managed_conversations ADD CONSTRAINT reject_thread CHECK (false) NOT VALID`)
	require.NoError(t, err)
	_, err = manager.createSession(t.Context(), start, nil, "", nil)
	require.ErrorContains(t, err, "persist new Web session")
	require.Len(t, bridges, 1)
	assert.Len(t, bridges[0].StopCalls(), 1)

	_, err = manager.ensureThreadBridge(t.Context(), start.conversationID, ThreadState{Agent: "main"})
	require.NoError(t, err)
	require.Len(t, bridges, 2, "the retry starts a fresh bridge")
}

func TestStartedThreadKeepsSpawnedBy(t *testing.T) {
	store := newWorkspaceSessionService(t)

	require.NoError(t, store.UpsertThread("child", ThreadState{Agent: "main", spawnedBy: "parent"}))
	require.NoError(t, store.UpsertThread("child", ThreadState{Agent: "planner", spawnedBy: "other"}))
	_, err := store.ForkConversation(t.Context(), "child", protocol.Conversation{ID: "fork", Agent: "main"}, "")
	require.NoError(t, err)

	for id, want := range map[string]ThreadState{"child": {Agent: "planner", spawnedBy: "parent"}, "fork": {Agent: "main"}} {
		thread, recorded, err := store.Thread(id)
		require.NoError(t, err)
		require.True(t, recorded)
		require.Equal(t, want, thread, "only creation records the creator; a fork has none")
	}
}

func TestRuntimeSwitchesConversationAgent(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	store := newWorkspaceSessionService(t)
	require.NoError(t, store.UpsertThread(protocol.SlackThreadConversationID("C123", "1.1"), ThreadState{Agent: "main", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, store.RegisterExternalMCPConversation("external", "main", &ExternalMCPSessionState{PrivateConversationID: "external_mcp:private", ManagedConversationID: protocol.SlackThreadConversationID("C123", "2.2"), Agent: "main"}))
	require.NoError(t, store.UpsertThread(protocol.SlackThreadConversationID("C123", "3.3"), ThreadState{Agent: "main"}))

	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return newDirectBridgeMock() }})

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
	t.Parallel()

	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	target := slackTarget("D123", "111.222")
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 4), stopCh: make(chan struct{}), handling: true, inputOpen: true}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return bridge }})

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

	restarted := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return newDirectBridgeMock() }})
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
	t.Parallel()

	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, stopped: true}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return bridge }})

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
	t.Parallel()

	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner"}))
	require.NoError(t, beginGoalDB(t.Context(), store.db, conversationID, &GoalState{Objective: "ship it", MaxTurns: 5, PrincipalName: "Alice", PrincipalEmail: "alice@example.com"}))

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(cfg *Config) directBridge {
		assert.Equal(t, Config{ConversationID: conversationID, Agent: "planner"}, *cfg)

		return bridge
	}})
	runTestManager(t, manager)

	require.NoError(t, manager.StartActiveGoals(t.Context()))
	require.Len(t, submittedMessages(bridge), 1)
	assert.Equal(t, protocol.GoalActionContinue, submittedMessages(bridge)[0].GoalAction)
	assert.Equal(t, map[string]string{protocol.InboundPrincipalNameMetadataKey: "Alice", protocol.InboundPrincipalEmailMetadataKey: "alice@example.com"}, submittedMessages(bridge)[0].Metadata, "the restart continuation keeps the goal's setter")
	assert.Equal(t, "Continue the active goal loop.", submittedMessages(bridge)[0].Text)
	assert.Equal(t, conversationID, submittedMessages(bridge)[0].ConversationID)
	assert.Equal(t, &protocol.SlackReplyTarget{}, submittedMessages(bridge)[0].SlackReply)
}

// A resumed turn continues its goal itself when it finishes; a startup kick would double it.
func TestThreadBridgeManagerSkipsActiveGoalContinuationForResumedTurn(t *testing.T) {
	t.Parallel()

	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "planner"}))
	require.NoError(t, store.BeginGoal(conversationID, &GoalState{Objective: "ship it", MaxTurns: 5}))
	seedActiveTurn(t, store, conversationID, "turn-resumed", nil)

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return bridge }})
	runTestManager(t, manager)

	require.NoError(t, manager.StartActiveGoals(t.Context()))
	assert.Empty(t, submittedMessages(bridge))
}

func TestThreadBridgeManagerAllowsGoalAfterCompletedGoal(t *testing.T) {
	t.Parallel()

	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, &GoalState{Objective: "first", MaxTurns: 5}))
	_, err := store.UpdateGoalStatus(conversationID, GoalStatusComplete, "done")
	require.NoError(t, err)
	require.NoError(t, store.BeginGoal(conversationID, &GoalState{Objective: "second", MaxTurns: 5}))

	goal, ok, err := store.Goal(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "second", goal.Objective)
	assert.Equal(t, GoalStatusActive, goal.Status)
}

func TestThreadBridgeManagerPickLaterWorkUsesLiveBridge(t *testing.T) {
	t.Parallel()

	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	bridge := newDirectBridgeMock()
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return bridge }})
	runTestManager(t, manager)
	require.NoError(t, manager.PickLaterWork(t.Context(), conversationID))
	require.Len(t, bridge.PickLaterWorkCalls(), 1)
	require.NoError(t, manager.PickLaterWork(t.Context(), ""))
	require.NoError(t, manager.PickLaterWork(t.Context(), protocol.SlackThreadConversationID("D999", "9.9")))
	require.Len(t, bridge.PickLaterWorkCalls(), 1)
}

func TestStartQueuedConversationsReportsLaterWorkErrors(t *testing.T) {
	t.Parallel()

	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{}))
	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "one"}))
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge {
		return newDirectBridgeMock()
	}})
	runTestManager(t, manager)
	require.ErrorContains(t, manager.StartQueuedConversations(t.Context()), "text thread agent is required")
}

func TestStartQueuedConversationsWakesEachRecordedConversationOnce(t *testing.T) {
	t.Parallel()

	store := newWorkspaceSessionService(t)
	first := protocol.SlackThreadConversationID("D123", "111.222")
	second := protocol.SlackThreadConversationID("D124", "222.333")

	require.NoError(t, store.UpsertThread(first, ThreadState{Agent: "main"}))
	require.NoError(t, store.UpsertThread(second, ThreadState{Agent: "main"}))
	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: first, Message: "one"}))
	require.NoError(t, store.PutThreadQueueItem("q2", &protocol.ThreadQueueItem{ID: "q2", ConversationID: first, Message: "two"}))
	require.NoError(t, store.PutThreadQueueItem("q3", &protocol.ThreadQueueItem{ID: "q3", ConversationID: second, Message: "three"}))

	woke := map[string]int{}
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(cfg *Config) directBridge {
		bridge := newDirectBridgeMock()
		conversationID := cfg.ConversationID
		bridge.PickLaterWorkFunc = func(context.Context) error {
			woke[conversationID]++
			return nil
		}

		return bridge
	}})
	runTestManager(t, manager)

	require.NoError(t, manager.StartQueuedConversations(t.Context()))
	require.Equal(t, map[string]int{first: 1, second: 1}, woke)
	require.Len(t, manager.bridges, 2)
}

func TestThreadBridgeManagerInterruptSlackThreadInterruptsActiveTurn(t *testing.T) {
	t.Parallel()

	store := newWorkspaceSessionService(t)
	conversationID := protocol.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, &GoalState{Objective: "first", MaxTurns: 5}))

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
	t.Parallel()

	store := newWorkspaceSessionService(t)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return newDirectBridgeMock() }})
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

func TestSessionsWriteCreatesWebSession(t *testing.T) {
	// The fake CLI stands in for the host tailscale binary that RocketClaw shells out to.
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tailscale"), []byte("#!/bin/sh\n[ \"$*\" = \"ip -4\" ] && echo 100.95.197.99\n"), 0o755))
	t.Setenv("PATH", bin)

	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, "skills"), 0o755))
	writeAppTestAgent(t, workspace, "main", "---\ndescription: Test agent\nmodel: gpt-6.1-sol\n---\nPrompt\n")

	store := newWorkspaceSessionService(t)
	bridge := newDirectBridgeMock()

	var created Config

	manager := newThreadBridgeManager(config.NewLockedConfig(&config.Config{Workspace: workspace, Web: config.WebConfig{ListenAddress: "127.0.0.1:8080"}}), store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(cfg *Config) directBridge {
		created = *cfg
		return bridge
	}})
	runTestManager(t, manager)

	caller := &Bridge{config: Config{ConversationID: "web:caller", SessionService: store}, threads: manager}
	origin := &protocol.InboundMessage{Source: protocol.SourceWeb, Human: true, Metadata: map[string]string{protocol.InboundPrincipalMetadataKey: "Alice"}}
	write := sessionsWriteTool(caller, origin, "main", caller.config.ConversationID)

	output, err := write.Call(t.Context(), []byte(`{"session_id":"","mode":"steer","title":" Child ","prompt":" literal $(date) ","agent":""}`), nil)
	require.NoError(t, err)

	var result struct {
		SessionID string `json:"session_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(output.Output), &result))

	conversationID := result.SessionID
	_, _, slack := protocol.SlackThreadTarget(conversationID)

	require.NotEmpty(t, conversationID)
	assert.False(t, slack)
	assert.JSONEq(t, fmt.Sprintf(`{"outcome":"created","session_id":%q,"url":%q}`, conversationID, "http://100.95.197.99:8080/s/"+base64.RawURLEncoding.EncodeToString([]byte(conversationID))), output.Output)
	assert.Equal(t, Config{ConversationID: conversationID, Agent: "main"}, created)
	require.Len(t, submittedMessages(bridge), 1)

	first := submittedMessages(bridge)[0]
	assert.Equal(t, " literal $(date) ", first.Text)
	assert.False(t, first.Human)
	assert.Equal(t, protocol.SourceSystem, first.Source)
	assert.Contains(t, buildPrompt(first, nil, false), `sender="message from session web:caller (agent )"`)
	assert.Contains(t, buildPrompt(first, nil, false), "\n\n literal $(date) ")
	assert.Equal(t, "System", first.Metadata[protocol.InboundOriginMetadataKey])
	assert.Nil(t, first.SlackReply)

	thread, ok, err := store.Thread(conversationID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ThreadState{Agent: "main", CreatedBy: "Alice", spawnedBy: "web:caller"}, thread, "the creator is recorded")

	var name string
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT name FROM managed_conversations WHERE conversation_id = $1`, conversationID).Scan(&name))
	assert.Equal(t, "Child", name)

	_, err = write.Call(t.Context(), []byte(`{"session_id":"","mode":"steer","title":"","prompt":"again","agent":""}`), nil)
	require.ErrorContains(t, err, "title is required")
}

func TestThreadBridgeManagerWebURLUsesTsnetOrigin(t *testing.T) {
	// No tailscale binary on PATH: the tsnet link must not ask for the IP.
	t.Setenv("PATH", t.TempDir())

	cfg := &config.Config{Web: config.WebConfig{ListenAddress: "127.0.0.1:8080", Tsnet: &config.TsnetConfig{Hostname: "rocketclaw", Origin: "https://rocketclaw.example.ts.net"}}}
	manager := newThreadBridgeManager(config.NewLockedConfig(cfg), nil, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge {
		t.Fatal("no conversation should start")
		return nil
	}})

	url, err := manager.WebURL(t.Context(), "web:1")
	require.NoError(t, err)
	assert.Equal(t, "https://rocketclaw.example.ts.net/s/"+base64.RawURLEncoding.EncodeToString([]byte("web:1")), url)
}

func TestSessionsWriteRejectsUnavailableAgents(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, "skills"), 0o755))
	writeAppTestAgent(t, workspace, "main", "---\ndescription: Test agent\nmodel: gpt-6.1-sol\n---\nPrompt\n")

	store := newWorkspaceSessionService(t)
	manager := newThreadBridgeManager(config.NewLockedConfig(&config.Config{Workspace: workspace, Web: config.WebConfig{ListenAddress: "127.0.0.1:8080"}}), store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge {
		t.Fatal("no conversation should start")
		return nil
	}})
	runTestManager(t, manager)

	write := func(origin *protocol.InboundMessage, agentName, agent, want string) {
		t.Helper()

		_, err := sessionsWriteTool(&Bridge{config: Config{ConversationID: "caller", SessionService: store}, threads: manager}, origin, agentName, "caller").Call(t.Context(), fmt.Appendf(nil, `{"session_id":"","mode":"enqueue","title":"Nightly","prompt":"run suite","agent":%q}`, agent), nil)
		require.ErrorContains(t, err, want)
	}

	slack := newThreadInboundMessage("go", "1.1", "1.1")
	protocol.SetInboundAllowedAgents(slack, []string{"main"})
	write(slack, "main", "other", `agent "other" is not allowed on this source surface`)

	cron := &protocol.InboundMessage{Source: protocol.SourceSystem, Cronjob: &protocol.CronjobMessage{RelativePath: "cron/daily.md", Agent: "job"}}
	write(cron, "job", "main", `agent "main" is not allowed on this source surface`)
	write(cron, "job", "", `agent "job" is not configured`)
	write(new(protocol.InboundMessage), "missing", "", `agent "missing" is not configured`)

	t.Setenv("PATH", t.TempDir())
	write(new(protocol.InboundMessage), "main", "", "tailscale")

	conversations, err := queryStrings(t.Context(), store.db, `SELECT conversation_id FROM managed_conversations`, "conversations")
	require.NoError(t, err)
	assert.Empty(t, conversations, "a failed URL lookup creates no session")
}

func TestThreadBridgeManagerStopStopsActiveBridges(t *testing.T) {
	t.Parallel()

	store := newWorkspaceSessionService(t)
	bridges := make([]*directBridgeMock, 0, 2)
	manager := newThreadBridgeManager(nil, store, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge {
		bridge := newDirectBridgeMock()
		bridges = append(bridges, bridge)

		return bridge
	}})
	shutdown := runTestManager(t, manager)

	for _, id := range []string{"first", "second"} {
		_, err := manager.ensureThreadBridge(t.Context(), id, ThreadState{Agent: "main"})
		require.NoError(t, err)
	}

	require.NoError(t, shutdown())

	require.Len(t, bridges, 2)
	require.Len(t, bridges[0].StopCalls(), 1)
	require.Len(t, bridges[1].StopCalls(), 1)

	_, err := manager.ensureThreadBridge(t.Context(), "late", ThreadState{Agent: "main"})
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

// R18: promoting an agent-written queue item keeps it non-human, whether it joins the open
// turn as a steer or runs as the next turn.
func TestPromotedAgentQueueItemStaysNonHuman(t *testing.T) {
	store := newTestSessionService(t)

	const conversationID = "web:cron:digest"
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	bus := newTestBus()
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), bus: bus, config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1)}
	manager := &threadBridgeManager{log: slog.New(slog.DiscardHandler), store: store, bridges: map[string]directBridge{conversationID: bridge}}

	for _, open := range []bool{true, false} {
		inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindEnqueue, "$docs-helper write docs", false)
		inbound.Metadata = map[string]string{protocol.InboundSenderSessionMetadataKey: "web:sender", protocol.InboundSenderAgentMetadataKey: "planner"}
		require.NoError(t, store.PutThreadQueueItem("agent", &protocol.ThreadQueueItem{ConversationID: conversationID, Source: protocol.SourceSystem, Kind: protocol.InboundKindEnqueue, Message: inbound.Text, Inbound: inbound}))

		bridge.inputOpen = open
		promoted, err := manager.promoteQueueItem(t.Context(), conversationID, "agent")
		require.NoError(t, err)
		require.True(t, promoted)

		if !open {
			assert.False(t, (<-bridge.requestCh).inbound.Human, "a promoted agent item runs as a non-human turn")
			continue
		}

		require.Len(t, bridge.steers, 1)
		assert.False(t, bridge.steers[0].inbound.Human)

		inputs, err := bridge.Drain(t.Context(), "resp-1", rocketcode.TurnPhaseToolLoop, nil)
		require.NoError(t, err)
		require.Len(t, inputs, 1)
		assert.Nil(t, inputs[0].DirectSkill)
		assert.True(t, strings.HasPrefix(inputs[0].Header, `[System sender="steer from session web:sender (agent planner)" additional_instructions=`), inputs[0].Header)
	}
}

// agentWrite is a write as rocketclaw_sessions_write builds it for agent planner of session web:sender.
func agentWrite(text string) *protocol.InboundMessage {
	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, text, false)
	inbound.Metadata = map[string]string{protocol.InboundSenderSessionMetadataKey: "web:sender", protocol.InboundSenderAgentMetadataKey: "planner"}

	return inbound
}

// runningTestBridge is a recorded conversation's bridge whose turn runs with its input open.
func runningTestBridge(t *testing.T, store *SessionService, manager *threadBridgeManager, conversationID string) *Bridge {
	t.Helper()
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "main", spawnedBy: "web:sender"}))

	_, cancel := context.WithCancel(t.Context())
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), bus: newTestBus(), config: Config{ConversationID: conversationID, SessionService: store}, threads: manager, requestCh: make(chan bridgeRequest, 8), stopCh: make(chan struct{}),
		handling: true, inputOpen: true, activeReply: new(protocol.InboundMessage), activeTurnCancel: cancel, activeTurnInterrupts: make(chan os.Signal, 1)}
	manager.bridges[conversationID] = bridge

	return bridge
}

func queueIDs(t *testing.T, store *SessionService, conversationID string) []string {
	t.Helper()

	queue, err := store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)

	rows := protocol.MixedLaterWork(queue, nil)
	ids := make([]string, len(rows))

	for i := range rows {
		ids[i] = rows[i].Queue.ID
	}

	return ids
}

// AE1, AE3, AE4: an agent's write is a Thread Queue row first; a steer joins only an open turn, a
// stash waits for a person, both show in Web with their sender, and a replay adds nothing.
func TestWriteSessionDeliversByMode(t *testing.T) {
	store := newTestSessionService(t)
	manager := &threadBridgeManager{log: slog.New(slog.DiscardHandler), store: store, bridges: map[string]directBridge{}}

	for _, tc := range []struct {
		name    string
		mode    sessionWriteMode
		open    bool
		outcome sessionWriteOutcome
		kind    protocol.InboundKind
	}{
		{"steer into an open turn", sessionWriteSteer, true, sessionSteered, protocol.InboundKindSteer},
		{"steer past the final answer or idle", sessionWriteSteer, false, sessionQueued, protocol.InboundKindEnqueue},
		{"enqueue", sessionWriteEnqueue, true, sessionQueued, protocol.InboundKindEnqueue},
		{"stash", sessionWriteStash, true, sessionStashed, protocol.InboundKindHeld},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := "web:" + tc.name
			bridge := runningTestBridge(t, store, manager, target)
			bridge.inputOpen = tc.open

			outcome, running, err := manager.writeSession(t.Context(), target, "write "+tc.name, tc.mode, agentWrite("also check 2025"))
			require.NoError(t, err)
			assert.Equal(t, tc.outcome, outcome)
			assert.False(t, running)

			_, _, err = manager.writeSession(t.Context(), target, "write "+tc.name, tc.mode, agentWrite("also check 2025"))
			require.ErrorContains(t, err, "already delivered", "a replayed write adds nothing")

			queue, err := store.ThreadQueueForConversation(target)
			require.NoError(t, err)
			require.Len(t, queue, 1)
			assert.Equal(t, tc.kind, queue[0].Kind)
			assert.Equal(t, protocol.SourceSystem, queue[0].Source)
			assert.Empty(t, queue[0].Principal, "an agent's write has no human principal")

			items, err := manager.queueItems(target)
			require.NoError(t, err)
			require.Len(t, items, 1, "Web lists the write once")
			assert.Equal(t, "web:sender", items[0].Inbound.Metadata[protocol.InboundSenderSessionMetadataKey])

			switch tc.outcome {
			case sessionSteered:
				require.NoError(t, manager.PickLaterWork(t.Context(), target))
				require.Len(t, bridge.steers, 1, "later work does not admit the steer's row again")
				assert.Empty(t, bridge.requestCh)

				inputs, err := bridge.Drain(t.Context(), "resp-1", rocketcode.TurnPhaseToolLoop, nil)
				require.NoError(t, err)
				require.Len(t, inputs, 1)
				assert.True(t, strings.HasPrefix(inputs[0].Header, `[System sender="steer from session web:sender (agent planner)"`), inputs[0].Header)
			case sessionQueued:
				require.Len(t, bridge.requestCh, 1, "the write waits for the running turn")

				request := <-bridge.requestCh
				assert.Equal(t, "write "+tc.name, request.queueItemID)
				assert.False(t, request.inbound.Human)
				assert.True(t, strings.HasPrefix(buildPrompt(request.inbound, nil, false), `[System sender="message from session web:sender (agent planner)"`))

				for _, runs := range []bool{true, false} {
					activated, err := bridge.activateInbound(t.Context(), &bridgeRequest{inbound: request.inbound, queueItemID: request.queueItemID})
					require.NoError(t, err)
					assert.Equal(t, runs, activated, "the write runs once")
				}
			case sessionStashed:
				assert.Empty(t, bridge.requestCh, "a stash waits until a person pops it")
				assert.Empty(t, bridge.steers)

				popped, err := (&Runtime{Sessions: store, threads: manager}).PopQueueItem(t.Context(), target, "write "+tc.name)
				require.NoError(t, err)
				require.True(t, popped)
				assert.False(t, (<-bridge.requestCh).inbound.Human, "a popped write stays non-human")
			case sessionCreated, sessionStopped: // writeSession never creates; TestWriteSessionStopRunsFirst covers stops.
			}
		})
	}

	t.Run("behind a goal and a due scheduled message", func(t *testing.T) {
		const target = "web:goal"

		bridge := runningTestBridge(t, store, manager, target)
		bridge.inputOpen = false

		require.NoError(t, store.BeginGoal(target, &GoalState{Objective: "ship it", MaxTurns: 5}))
		require.NoError(t, store.PutScheduledMessage("due", &protocol.ScheduledMessageState{ConversationID: target, Message: "scheduled", DueAt: time.Now().Add(-time.Minute)}))

		outcome, _, err := manager.writeSession(t.Context(), target, "after goal", sessionWriteEnqueue, agentWrite("later"))
		require.NoError(t, err)
		assert.Equal(t, sessionQueued, outcome)
		assert.Empty(t, bridge.requestCh, "the active goal holds the write")

		_, err = store.UpdateGoalStatus(target, GoalStatusComplete, "done")
		require.NoError(t, err)
		require.NoError(t, manager.PickLaterWork(t.Context(), target))
		assert.Equal(t, "after goal", (<-bridge.requestCh).queueItemID, "the write runs where PickLaterWork puts a queued row: before the due scheduled message")
	})

	t.Run("history busy", func(t *testing.T) {
		bridge := runningTestBridge(t, store, manager, "web:busy")
		bridge.historyMutation = true

		_, _, err := manager.writeSession(t.Context(), "web:busy", "busy", sessionWriteEnqueue, agentWrite("later"))
		require.ErrorContains(t, err, "history is busy")
		assert.Empty(t, queueIDs(t, store, "web:busy"))
	})

	t.Run("staged revert", func(t *testing.T) {
		const target = "web:reverted"

		bridge := runningTestBridge(t, store, manager, target)
		bridge.inputOpen = false
		_, err := store.db.ExecContext(t.Context(), `UPDATE managed_conversations SET revert_message_id = 'staged' WHERE conversation_id = $1`, target)
		require.NoError(t, err)

		for _, mode := range []sessionWriteMode{sessionWriteEnqueue, sessionWriteSteer} {
			outcome, _, err := manager.writeSession(t.Context(), target, string(mode), mode, agentWrite(string(mode)))
			require.NoError(t, err)
			assert.Equal(t, sessionQueued, outcome)
		}

		var marker string
		require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT revert_message_id FROM managed_conversations WHERE conversation_id = $1`, target).Scan(&marker))
		assert.Equal(t, "staged", marker, "a write never commits a staged revert")
		assert.Empty(t, bridge.requestCh)
		assert.Equal(t, []string{"enqueue", "steer"}, queueIDs(t, store, target))

		_, err = store.db.ExecContext(t.Context(), `UPDATE managed_conversations SET revert_message_id = '' WHERE conversation_id = $1`, target)
		require.NoError(t, err)
		require.NoError(t, manager.PickLaterWork(t.Context(), target))
		assert.Equal(t, "enqueue", (<-bridge.requestCh).queueItemID, "the writes run once the revert is cancelled")
	})
}

// AE5: a stop write stops the turn and goal, then runs before every earlier queued item, including one
// whose request the loop already holds; the loop's other requests keep their place.
func TestWriteSessionStopRunsFirst(t *testing.T) {
	store := newTestSessionService(t)
	manager := &threadBridgeManager{log: slog.New(slog.DiscardHandler), store: store, bridges: map[string]directBridge{}}

	const target = "web:target"

	bridge := runningTestBridge(t, store, manager, target)
	bridge.inputOpen = false

	for _, id := range []string{"first", "second"} {
		_, _, err := manager.writeSession(t.Context(), target, id, sessionWriteEnqueue, agentWrite(id))
		require.NoError(t, err)
	}

	require.Len(t, bridge.requestCh, 1, "the first queued write was handed to the loop during the turn")

	bridge.requestCh <- bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "sync", false)}

	require.NoError(t, store.BeginGoal(target, &GoalState{Objective: "ship it", MaxTurns: 5}))

	outcome, running, err := manager.writeSession(t.Context(), target, "stop", sessionWriteStop, agentWrite("do this instead"))
	require.NoError(t, err)
	assert.Equal(t, sessionStopped, outcome)
	assert.True(t, running)
	assert.Equal(t, os.Interrupt, <-bridge.activeTurnInterrupts)

	goal, _, err := store.Goal(target)
	require.NoError(t, err)
	assert.Equal(t, GoalStatusStopped, goal.Status)

	assert.Equal(t, []string{"stop", "first", "second"}, queueIDs(t, store, target))
	require.Len(t, bridge.waiting, 1)
	assert.Equal(t, "first", bridge.waiting[0].queueItemID)
	require.Len(t, bridge.requestCh, 1)
	assert.Equal(t, "sync", (<-bridge.requestCh).inbound.Text)

	require.NoError(t, manager.PickLaterWork(t.Context(), target))

	stop := <-bridge.requestCh
	assert.Equal(t, "stop", stop.queueItemID)
	assert.True(t, strings.HasPrefix(buildPrompt(stop.inbound, nil, false), `[System sender="session web:sender (agent planner) stopped your previous turn and sent this"`))

	idle := runningTestBridge(t, store, manager, "web:idle")
	idle.handling, idle.activeReply, idle.activeTurnCancel, idle.activeTurnInterrupts = false, nil, nil, nil

	outcome, running, err = manager.writeSession(t.Context(), "web:idle", "idle-stop", sessionWriteStop, agentWrite("go"))
	require.NoError(t, err)
	assert.Equal(t, sessionStopped, outcome)
	assert.False(t, running, "no turn was running")
	assert.Equal(t, "idle-stop", (<-idle.requestCh).queueItemID)
}

// A request a stopped parent left held in its subagent's queue does not run, even from the loop that already
// holds it, until an agent or a person queues the next write there; then it runs just before that write. A
// stash, by hand or by an agent, stays held.
func TestWriteSessionReleasesHeldRequest(t *testing.T) {
	for _, writer := range []string{"agent", "person"} {
		t.Run(writer, func(t *testing.T) {
			store := newTestSessionService(t)
			manager := &threadBridgeManager{log: slog.New(slog.DiscardHandler), store: store, bridges: map[string]directBridge{}}

			const target = "web:main/a"

			bridge := runningTestBridge(t, store, manager, target)
			bridge.inputOpen = false

			for _, id := range []string{"held", "earlier"} {
				_, _, err := manager.writeSession(t.Context(), target, id, sessionWriteEnqueue, agentWrite(id))
				require.NoError(t, err)
			}

			_, err := store.db.ExecContext(t.Context(), `UPDATE thread_queue SET kind = 'held' WHERE queue_item_id = 'held'`) // As a stopped parent's wait does.
			require.NoError(t, err)

			request := <-bridge.requestCh
			require.Equal(t, "held", request.queueItemID)
			activated, err := bridge.activateInbound(t.Context(), &request)
			require.NoError(t, err)
			require.False(t, activated, "the held request does not run")

			require.NoError(t, manager.stashQueueItem(t.Context(), target, &protocol.ThreadQueueItem{ID: "by hand", Kind: protocol.InboundKindHeld, Source: protocol.SourceWeb, Principal: "alice", Message: "later"}))
			_, _, err = manager.writeSession(t.Context(), target, "agent stash", sessionWriteStash, agentWrite("later"))
			require.NoError(t, err)
			require.Equal(t, []string{"earlier"}, queueIDs(t, store, target), "stashes release nothing")

			if writer == "agent" {
				_, _, err = manager.writeSession(t.Context(), target, "next", sessionWriteEnqueue, agentWrite("next"))
			} else {
				err = manager.stashQueueItem(t.Context(), target, &protocol.ThreadQueueItem{ID: "next", Kind: protocol.InboundKindEnqueue, Source: protocol.SourceWeb, Principal: "alice", Message: "next", StashAt: time.Now().UTC()})
			}

			require.NoError(t, err)
			assert.Equal(t, []string{"earlier", "held", "next"}, queueIDs(t, store, target))

			held, err := queryStrings(t.Context(), store.db, `SELECT queue_item_id FROM thread_queue WHERE conversation_id = $1 AND kind = 'held' ORDER BY 1`, "held rows", target)
			require.NoError(t, err)
			assert.Equal(t, []string{"agent stash", "by hand"}, held)
		})
	}
}

// A person's Web message or $goal releases the request a stopped parent held in a subagent session, and
// the loop runs it before that write, even behind requests it already lined up, and before the goal.
func TestWebWriteRunsReleasedRequestFirst(t *testing.T) {
	for _, write := range []string{"message", "goal"} {
		t.Run(write, func(t *testing.T) {
			store := newTestSessionService(t)
			manager := &threadBridgeManager{log: slog.New(slog.DiscardHandler), store: store, bridges: map[string]directBridge{}}
			runtime := &Runtime{Sessions: store, threads: manager}

			const target = "web:main/a"

			bridge := runningTestBridge(t, store, manager, target)
			bridge.inputOpen = false

			_, _, err := manager.writeSession(t.Context(), target, "held", sessionWriteEnqueue, agentWrite("parent work"))
			require.NoError(t, err)
			require.Equal(t, "held", (<-bridge.requestCh).queueItemID)
			_, err = store.db.ExecContext(t.Context(), `UPDATE thread_queue SET kind = 'held' WHERE queue_item_id = 'held'`) // As a stopped parent's wait does.
			require.NoError(t, err)

			bridge.requestCh <- bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindSteer, "lined up", true)}

			inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindSteer, "next work", true)
			inbound.ConversationID, inbound.Metadata = target, map[string]string{protocol.InboundPrincipalMetadataKey: "alice"}

			var writes errgroup.Group

			writes.Go(func() error {
				if write == "goal" {
					return runtime.StartGoal(t.Context(), inbound, protocol.GoalRequest{Objective: "ship it"})
				}

				return runtime.RunTurn(t.Context(), inbound)
			})

			require.Eventually(t, func() bool { return len(bridge.requestCh) == 3 }, 5*time.Second, time.Millisecond, "the write releases the held request to the loop")
			assert.Equal(t, "lined up", (<-bridge.requestCh).inbound.Text)

			held := <-bridge.requestCh
			assert.Equal(t, "held", held.queueItemID, "the released request runs before the write that released it")

			next := <-bridge.requestCh
			assert.Equal(t, "next work", next.inbound.Text)

			if write == "goal" {
				require.NoError(t, writes.Wait())
				assert.Equal(t, protocol.GoalActionKickoff, next.inbound.GoalAction)

				activated, err := bridge.activateInbound(t.Context(), &held)
				require.NoError(t, err)
				assert.True(t, activated, "the goal does not hold the request released before it")
			} else {
				close(next.completion.done)
				require.NoError(t, writes.Wait())
			}
		})
	}
}

// Agents writing to and stopping each other never wait on one another's turns.
func TestWriteSessionMutualWritesDoNotBlock(t *testing.T) {
	store := newTestSessionService(t)
	manager := &threadBridgeManager{log: slog.New(slog.DiscardHandler), store: store, bridges: map[string]directBridge{}}

	for _, id := range []string{"web:a", "web:b"} {
		runningTestBridge(t, store, manager, id)
	}

	var writes errgroup.Group

	for _, pair := range [][2]string{{"web:a", "web:b"}, {"web:b", "web:a"}} {
		for _, mode := range []sessionWriteMode{sessionWriteSteer, sessionWriteStop} {
			writes.Go(func() error {
				_, _, err := manager.writeSession(t.Context(), pair[1], pair[0]+string(mode), mode, agentWrite(string(mode)))
				return err
			})
		}
	}

	require.NoError(t, writes.Wait())
}
