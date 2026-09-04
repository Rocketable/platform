package rpc

import (
	"context"
	"log/slog"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
)

type stubRouter struct {
	busy                                            bool
	started, stopped, enqueued, switched, workflows []string
	queue                                           []protocol.ThreadQueueItem
	target                                          protocol.TextConversationTarget
}

func (r *stubRouter) StartThread(_ context.Context, _ string, target protocol.TextConversationTarget, inbound *protocol.InboundMessage) error {
	r.started = append(r.started, inbound.Text)
	r.target = target

	return nil
}
func (stubRouter) StartGoalInThread(context.Context, string, string, string, int, protocol.TextConversationTarget, *protocol.InboundMessage) error {
	return nil
}
func (r *stubRouter) StartWorkflowInThread(_ context.Context, _, name, args string, _ protocol.TextConversationTarget, _ *protocol.InboundMessage) error {
	r.workflows = append(r.workflows, name+" "+args)
	return nil
}
func (stubRouter) ReserveWorkflowTurn(protocol.TextConversationTarget) (release func(), reserved bool, err error) {
	return func() {}, true, nil
}
func (stubRouter) WorkflowDescriptions() ([]protocol.WorkflowDescription, error) {
	return nil, nil
}
func (stubRouter) InterruptConversation(string) *protocol.InboundMessage { return nil }
func (r *stubRouter) InterruptThread(protocol.TextConversationTarget) (*protocol.InboundMessage, error) {
	r.stopped = append(r.stopped, "stop")
	return nil, nil
}
func (stubRouter) RegisterThread(protocol.TextConversationTarget, string) (bool, error) {
	return true, nil
}
func (stubRouter) ThreadAgent(protocol.TextConversationTarget) (agent string, handled bool, err error) {
	return "", false, nil
}
func (r *stubRouter) SwitchThreadAgent(_ protocol.TextConversationTarget, agent string) (bool, error) {
	r.switched = append(r.switched, agent)
	return true, nil
}
func (stubRouter) SubmitThreadReply(context.Context, protocol.TextConversationTarget, *protocol.InboundMessage) (bool, error) {
	return false, nil
}
func (stubRouter) SubmitWhenActive(context.Context, protocol.TextConversationTarget, *protocol.InboundMessage, protocol.ActivationHook) (bool, error) {
	return false, nil
}
func (r *stubRouter) StashThreadQueueItem(_ context.Context, _ protocol.TextConversationTarget, item *protocol.ThreadQueueItem) error {
	r.enqueued = append(r.enqueued, item.Message)
	return nil
}
func (r *stubRouter) ThreadQueueItems(context.Context, protocol.TextConversationTarget) ([]protocol.ThreadQueueItem, error) {
	return r.queue, nil
}
func (r *stubRouter) DeleteThreadQueueItem(_ context.Context, _ protocol.TextConversationTarget, id string) error {
	r.queue = slices.DeleteFunc(r.queue, func(item protocol.ThreadQueueItem) bool { return item.ID == id })
	return nil
}
func (r *stubRouter) ReorderThreadQueueItems(_ context.Context, _ protocol.TextConversationTarget, ids []string) error {
	byID := make(map[string]protocol.ThreadQueueItem, len(r.queue))
	for i := range r.queue {
		byID[r.queue[i].ID] = r.queue[i]
	}

	next := make([]protocol.ThreadQueueItem, 0, len(ids))
	for i, id := range ids {
		item := byID[id]
		item.Position = i
		next = append(next, item)
	}

	r.queue = next

	return nil
}
func (stubRouter) ScheduledMessages(context.Context, protocol.TextConversationTarget) (map[string]protocol.ScheduledMessageState, error) {
	return nil, nil
}
func (r *stubRouter) ThreadBusy(protocol.TextConversationTarget) bool { return r.busy }
func (stubRouter) PickQueuedWork(context.Context, protocol.TextConversationTarget) error {
	return nil
}

func inertCronList(context.Context) ([]*CronJob, error)    { return nil, nil }
func inertCronRun(context.Context, string) (string, error) { return "", nil }
func inertSideAsk(context.Context, protocol.SideAskRequest) error {
	return nil
}
func inertAgents() []*Agent    { return nil }
func inertSkills() []*Skill    { return nil }
func inertConfig() *ConfigView { return &ConfigView{} }
func inertSettle(context.Context, string, bool) error {
	return nil
}

type recordingBackend struct {
	subscribe func(context.Context) <-chan protocol.ConversationEvent
	turns     []protocol.TurnRequest
	created   []string
	switched  []string
	records   []protocol.ConversationRecord
	queue     []protocol.ThreadQueueItem
	busy      bool
}

func (r *recordingBackend) Subscribe(ctx context.Context) <-chan protocol.ConversationEvent {
	if r.subscribe != nil {
		return r.subscribe(ctx)
	}

	return make(chan protocol.ConversationEvent)
}

func (r *recordingBackend) CreateConversation(id string, _ []string, _ []protocol.ConversationTag) error {
	r.created = append(r.created, id)

	return nil
}

func (r *recordingBackend) RunTurn(_ context.Context, req *protocol.TurnRequest) error {
	r.turns = append(r.turns, *req)

	return nil
}

func (recordingBackend) SyncConversation(context.Context, string, string) error { return nil }

func (r *recordingBackend) ListConversations() ([]protocol.ConversationRecord, error) {
	return r.records, nil
}

func (recordingBackend) ConversationAgent(string) (string, error) { return "", nil }

func (r *recordingBackend) SwitchAgent(_, agent string) error {
	r.switched = append(r.switched, agent)

	return nil
}

func (r *recordingBackend) ListLaterWork(context.Context, string) ([]protocol.ThreadQueueItem, error) {
	return r.queue, nil
}

func (r *recordingBackend) DeleteLaterWork(_ context.Context, _, itemID string) error {
	r.queue = slices.DeleteFunc(r.queue, func(item protocol.ThreadQueueItem) bool { return item.ID == itemID })

	return nil
}

func (r *recordingBackend) ReorderLaterWork(_ context.Context, _ string, ids []string) error {
	byID := make(map[string]protocol.ThreadQueueItem, len(r.queue))
	for i := range r.queue {
		byID[r.queue[i].ID] = r.queue[i]
	}

	next := make([]protocol.ThreadQueueItem, 0, len(ids))
	for i, id := range ids {
		item := byID[id]
		item.Position = i
		next = append(next, item)
	}

	r.queue = next

	return nil
}

func (r *recordingBackend) ConversationBusy(string) bool {
	return r.busy
}

func (recordingBackend) ScheduledMessages(string) (map[string]protocol.ScheduledMessageState, error) {
	return nil, nil
}

func (recordingBackend) WorkflowDescriptions() ([]protocol.WorkflowDescription, error) {
	return nil, nil
}

func listIDs(ids ...string) func(context.Context) ([]*Session, error) {
	return func(context.Context) ([]*Session, error) {
		sessions := make([]*Session, 0, len(ids))
		for _, id := range ids {
			sessions = append(sessions, &Session{Id: id})
		}

		return sessions, nil
	}
}

func testServer(_ *stubRouter, list func(context.Context) ([]*Session, error), observe func(context.Context, string) ([]*TranscriptEvent, error)) *Server {
	rt := backend.RuntimeFor()
	rt.Cfg.Users = map[string]string{"alice": "alice", "bob": "bob"}
	server := New(rt)
	server.conv = &recordingBackend{subscribe: rt.Subscribe}
	server.list = list
	server.observe = observe
	server.listCron = inertCronList
	server.runCron = inertCronRun
	server.sideAsk = inertSideAsk
	server.agents = inertAgents
	server.skills = inertSkills
	server.config = inertConfig
	server.settle = inertSettle

	return server
}

func recorded(server *Server) *recordingBackend {
	return server.conv.(*recordingBackend)
}

func turnKinds(r *recordingBackend) []protocol.TurnKind {
	kinds := make([]protocol.TurnKind, 0, len(r.turns))
	for i := range r.turns {
		kinds = append(kinds, r.turns[i].Kind)
	}

	return kinds
}

func testClient(t *testing.T, server *Server) webClient {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	grpcServer := grpc.NewServer()

	RegisterWebServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(lis) }()

	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return webClient{cc: conn}
}

func principalCtx(ctx context.Context, name string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs(principalKey, name))
}

func TestListSessionsRejectsMissingPrincipal(t *testing.T) {
	client := testClient(t, testServer(&stubRouter{}, listIDs(), func(context.Context, string) ([]*TranscriptEvent, error) { return nil, nil }))
	_, err := client.ListSessions(t.Context(), &ListSessionsRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestPrincipalUsesConfigUsers(t *testing.T) {
	server := testServer(&stubRouter{}, listIDs(), func(context.Context, string) ([]*TranscriptEvent, error) { return nil, nil })
	server.rt.Cfg.Users = map[string]string{"alice": "100.64.0.1"}
	client := testClient(t, server)

	_, err := client.Protocol(principalCtx(t.Context(), "100.64.0.1"), &ProtocolRequest{})
	require.NoError(t, err)

	_, err = client.Protocol(principalCtx(t.Context(), "8.8.8.8"), &ProtocolRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	_, err = client.Protocol(principalCtx(t.Context(), "alice"), &ProtocolRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestListSessionsIncludesSlackAndWeb(t *testing.T) {
	web := protocol.WebSessionConversationID("ops")
	slack := protocol.SlackThreadConversationID("C1", "1.2")
	client := testClient(t, testServer(&stubRouter{}, func(context.Context) ([]*Session, error) {
		return []*Session{
			{Id: web, Title: "ops", Preview: "hi", UpdatedAt: "2026-09-02T00:00:00Z", Agent: "main", Settled: true},
			{Id: slack, Title: "#ops"},
			{Id: "cron:cron/daily.md:x:y"},
		}, nil
	}, func(context.Context, string) ([]*TranscriptEvent, error) { return nil, nil }))
	alice := principalCtx(t.Context(), "alice")
	listed, err := client.ListSessions(alice, &ListSessionsRequest{})
	require.NoError(t, err)
	require.Len(t, listed.GetSessions(), 2)
	require.Equal(t, []string{web, slack}, []string{listed.GetSessions()[0].GetId(), listed.GetSessions()[1].GetId()})
	require.Equal(t, "ops", listed.GetSessions()[0].GetTitle())
	require.Equal(t, "hi", listed.GetSessions()[0].GetPreview())
	require.Equal(t, "2026-09-02T00:00:00Z", listed.GetSessions()[0].GetUpdatedAt())
	require.Equal(t, "main", listed.GetSessions()[0].GetAgent())
	require.True(t, listed.GetSessions()[0].GetSettled())
	require.Equal(t, "#ops", listed.GetSessions()[1].GetTitle())
}

func TestSettleSessionRecordsOverride(t *testing.T) {
	var (
		gotID      string
		gotSettled bool
	)

	server := testServer(&stubRouter{}, listIDs(), func(context.Context, string) ([]*TranscriptEvent, error) { return nil, nil })
	server.settle = func(_ context.Context, id string, settled bool) error {
		gotID, gotSettled = id, settled
		return nil
	}
	client := testClient(t, server)
	_, err := client.SettleSession(principalCtx(t.Context(), "alice"), &SettleSessionRequest{Id: "web-session:ops", Settled: true})
	require.NoError(t, err)
	require.Equal(t, "web-session:ops", gotID)
	require.True(t, gotSettled)
}

func TestSettleSessionRejectsBusy(t *testing.T) {
	server := testServer(&stubRouter{}, listIDs(), func(context.Context, string) ([]*TranscriptEvent, error) { return nil, nil })
	recorded(server).busy = true
	client := testClient(t, server)
	_, err := client.SettleSession(principalCtx(t.Context(), "alice"), &SettleSessionRequest{Id: "web-session:ops", Settled: true})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestHistoryReturnsObservedTexts(t *testing.T) {
	id := protocol.SlackThreadConversationID("C1", "1.2")
	client := testClient(t, testServer(&stubRouter{}, listIDs(id), func(_ context.Context, got string) ([]*TranscriptEvent, error) {
		require.Equal(t, id, got)
		return []*TranscriptEvent{{Text: "hello from slack", Role: "user"}}, nil
	}))
	got, err := client.History(principalCtx(t.Context(), "alice"), &HistoryRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, []string{"hello from slack"}, got.GetTexts())
}

func TestJoinStreamsSnapshotThenLiveWebBroadcast(t *testing.T) {
	id := protocol.WebSessionConversationID("ops")
	server := testServer(&stubRouter{}, listIDs(id), func(context.Context, string) ([]*TranscriptEvent, error) {
		return []*TranscriptEvent{{Text: "hello", Role: "user"}}, nil
	})
	client := testClient(t, server)
	alice := principalCtx(t.Context(), "alice")
	bob := principalCtx(t.Context(), "bob")
	stream, err := client.Join(alice, &JoinRequest{Id: id})
	require.NoError(t, err)
	snap, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "hello", snap.GetText())
	require.True(t, snap.GetSnapshot())

	bobStream, err := client.Join(bob, &JoinRequest{Id: id})
	require.NoError(t, err)
	_, err = bobStream.Recv()
	require.NoError(t, err)

	message := protocol.NewOutboundMessage(protocol.SourceWeb, id, "live", protocol.OutputTargetWeb)
	message.Complete = true
	require.Equal(t, protocol.BroadcastHandled, server.HandleBroadcast(t.Context(), &protocol.Broadcast{Message: message, Delivery: message}).Status)

	got, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "live", got.GetText())
	require.Equal(t, "assistant", got.GetRole())
	require.False(t, got.GetSnapshot())
	require.True(t, got.GetComplete())
	got, err = bobStream.Recv()
	require.NoError(t, err)
	require.Equal(t, "live", got.GetText())
	require.False(t, got.GetSnapshot())

	slackID := protocol.SlackThreadConversationID("C1", "1.2")
	slackStream, err := client.Join(alice, &JoinRequest{Id: slackID})
	require.NoError(t, err)
	snap, err = slackStream.Recv()
	require.NoError(t, err)
	require.True(t, snap.GetSnapshot())

	slack := protocol.NewOutboundMessage(protocol.SourceSlack, slackID, "from slack", protocol.OutputTargetSlack)
	require.Equal(t, protocol.BroadcastDropped, server.HandleBroadcast(t.Context(), &protocol.Broadcast{Message: slack, Delivery: slack}).Status)

	live, err := slackStream.Recv()
	require.NoError(t, err)
	require.Equal(t, "from slack", live.GetText())

	progress := protocol.NewOutboundMessage(protocol.SourceSlack, slackID, "", protocol.OutputTargetSlack)
	progress.ProgressText = "Read\nGlob"
	require.Equal(t, protocol.BroadcastDropped, server.HandleBroadcast(t.Context(), &protocol.Broadcast{Message: progress, Delivery: progress}).Status)

	thought, err := slackStream.Recv()
	require.NoError(t, err)
	require.Equal(t, "thinking", thought.GetRole())
	require.Equal(t, "Read\nGlob", thought.GetText())

	require.Equal(t, protocol.BroadcastDropped, server.HandleBroadcast(t.Context(), &protocol.Broadcast{Relay: &protocol.ExternalMCPRelay{Text: "x"}}).Status)

	origin := protocol.NewOutboundMessage(protocol.SourceSystem, id, "popped later", protocol.OutputTargetWeb)
	origin.Originator = true
	broadcast := protocol.Broadcast{Message: origin, Delivery: origin}
	cloned := broadcast.Clone()
	require.Equal(t, protocol.BroadcastHandled, server.HandleBroadcast(t.Context(), &cloned).Status)

	user, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "popped later", user.GetText())
	require.Equal(t, "user", user.GetRole())
	require.False(t, user.GetSnapshot())
}

func TestPromptSlackSessionStartsThreadAndEchoes(t *testing.T) {
	id := protocol.SlackThreadConversationID("C1", "1.2")
	rt := backend.RuntimeFor()
	rt.Cfg.Users = map[string]string{"alice": "alice"}
	server := New(rt)
	server.conv = &recordingBackend{subscribe: rt.Subscribe}
	server.list = listIDs()
	server.observe = func(context.Context, string) ([]*TranscriptEvent, error) {
		return []*TranscriptEvent{{Text: "prior", Role: "user"}}, nil
	}
	server.listCron = inertCronList
	server.runCron = inertCronRun
	server.sideAsk = inertSideAsk
	server.agents = inertAgents
	server.skills = inertSkills
	server.config = inertConfig
	server.settle = inertSettle
	client := testClient(t, server)
	alice := principalCtx(t.Context(), "alice")
	stream, err := client.Join(alice, &JoinRequest{Id: id})
	require.NoError(t, err)
	snap, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "prior", snap.GetText())
	require.True(t, snap.GetSnapshot())

	got, err := client.Prompt(alice, &PromptRequest{Id: id, Text: "hello"})
	require.NoError(t, err)
	require.Empty(t, got.GetPrivateText())
	require.Equal(t, []protocol.TurnRequest{{ID: id, Kind: protocol.TurnPrompt, Text: "hello", Agent: "main"}}, recorded(server).turns)
}

func TestProtocolReturnsProtoSHA256(t *testing.T) {
	require.Len(t, protoSHA256(), 64)
	client := testClient(t, testServer(&stubRouter{}, listIDs(), func(context.Context, string) ([]*TranscriptEvent, error) { return nil, nil }))
	got, err := client.Protocol(principalCtx(t.Context(), "alice"), &ProtocolRequest{})
	require.NoError(t, err)
	require.Equal(t, protoSHA256(), got.GetProtoSha256())
}

func TestPromptCommandsAndSteer(t *testing.T) {
	id := protocol.WebSessionConversationID("ops")
	router := &stubRouter{busy: true}
	server := testServer(router, listIDs(), func(context.Context, string) ([]*TranscriptEvent, error) { return nil, nil })
	recorded(server).queue = []protocol.ThreadQueueItem{{Message: "later"}}
	client := testClient(t, server)
	ctx := principalCtx(t.Context(), "alice")
	_, err := client.Prompt(ctx, &PromptRequest{Id: id, Text: "steer me"})
	require.NoError(t, err)
	_, err = client.Prompt(ctx, &PromptRequest{Id: id, Text: "later work", Delivery: PromptDelivery_QUEUE})
	require.NoError(t, err)

	_, err = client.Prompt(ctx, &PromptRequest{Id: id, Text: "$enqueue later work"})
	require.NoError(t, err)

	_, err = client.Prompt(ctx, &PromptRequest{Id: id, Text: "$stop"})
	require.NoError(t, err)
	require.Equal(t, []protocol.TurnKind{protocol.TurnPrompt, protocol.TurnEnqueue, protocol.TurnEnqueue, protocol.TurnCancel}, turnKinds(recorded(server)))

	queued, err := client.Prompt(ctx, &PromptRequest{Id: id, Text: "$queue"})
	require.NoError(t, err)
	require.Equal(t, "later", queued.GetPrivateText())

	help, err := client.Prompt(ctx, &PromptRequest{Id: id, Text: "$wat"})
	require.NoError(t, err)
	require.Contains(t, help.GetPrivateText(), "$goal")

	router.busy = false
	_, err = client.Prompt(ctx, &PromptRequest{Id: id, Text: "parked", Delivery: PromptDelivery_QUEUE})
	require.NoError(t, err)
	require.Empty(t, router.started)

	_, err = client.Prompt(ctx, &PromptRequest{Id: id, Text: "hello"})
	require.NoError(t, err)

	created, err := client.CreateSession(ctx, &CreateSessionRequest{Name: "ops"})
	require.NoError(t, err)
	require.Equal(t, protocol.WebSessionConversationID("ops"), created.GetId())
	require.Equal(t, []string{protocol.WebSessionConversationID("ops")}, recorded(server).created)

	_, err = client.Prompt(ctx, &PromptRequest{Id: id, Text: "$agent helper"})
	require.NoError(t, err)
	require.Equal(t, []string{"helper"}, recorded(server).switched)
	require.Equal(t, []protocol.TurnKind{protocol.TurnPrompt, protocol.TurnEnqueue, protocol.TurnEnqueue, protocol.TurnCancel, protocol.TurnEnqueue, protocol.TurnPrompt}, turnKinds(recorded(server)))
}

func TestQueueListRemoveAndSteer(t *testing.T) {
	id := protocol.WebSessionConversationID("ops")
	router := &stubRouter{busy: true}
	server := testServer(router, listIDs(), func(context.Context, string) ([]*TranscriptEvent, error) {
		return []*TranscriptEvent{{Text: "prior", Role: "user"}}, nil
	})
	recorded(server).queue = []protocol.ThreadQueueItem{{ID: "q1", Message: "later"}, {ID: "q2", Message: "also"}}
	client := testClient(t, server)
	ctx := principalCtx(t.Context(), "alice")

	stream, err := client.Join(ctx, &JoinRequest{Id: id})
	require.NoError(t, err)
	snap, err := stream.Recv()
	require.NoError(t, err)
	require.True(t, snap.GetSnapshot())

	listed, err := client.ListQueue(ctx, &ListQueueRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, []*QueueItem{{Id: "q1", Text: "later"}, {Id: "q2", Text: "also"}}, listed.GetItems())

	_, err = client.ReorderQueue(ctx, &ReorderQueueRequest{Id: id, ItemIds: []string{"q2", "q1"}})
	require.NoError(t, err)
	listed, err = client.ListQueue(ctx, &ListQueueRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, []*QueueItem{{Id: "q2", Text: "also"}, {Id: "q1", Text: "later"}}, listed.GetItems())

	_, err = client.RemoveQueueItem(ctx, &QueueItemRequest{Id: id, ItemId: "q2"})
	require.NoError(t, err)
	listed, err = client.ListQueue(ctx, &ListQueueRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, []*QueueItem{{Id: "q1", Text: "later"}}, listed.GetItems())

	_, err = client.SteerQueueItem(ctx, &QueueItemRequest{Id: id, ItemId: "q1"})
	require.NoError(t, err)
	require.Equal(t, []protocol.TurnRequest{{ID: id, Kind: protocol.TurnPrompt, Text: "later", Agent: "main"}}, recorded(server).turns)
	listed, err = client.ListQueue(ctx, &ListQueueRequest{Id: id})
	require.NoError(t, err)
	require.Empty(t, listed.GetItems())

	recorded(server).queue = []protocol.ThreadQueueItem{{ID: "q3", Message: "next"}}
	router.busy = false
	_, err = client.SteerQueueItem(ctx, &QueueItemRequest{Id: id, ItemId: "q3"})
	require.NoError(t, err)
	require.Equal(t, []protocol.TurnKind{protocol.TurnPrompt, protocol.TurnPrompt}, turnKinds(recorded(server)))
	require.Empty(t, recorded(server).queue)

	_, err = client.SteerQueueItem(ctx, &QueueItemRequest{Id: id, ItemId: "missing"})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestPromptSlackAgentSwitchUsesChannelAgents(t *testing.T) {
	id := protocol.SlackThreadConversationID("C1", "1.2")
	router := &stubRouter{}
	server := testServer(router, func(context.Context) ([]*Session, error) {
		return []*Session{{Id: id, Title: "#ops"}}, nil
	}, func(context.Context, string) ([]*TranscriptEvent, error) { return nil, nil })
	server.agents = func() []*Agent {
		return []*Agent{{Name: "ops"}, {Name: "planner"}, {Name: "helper"}}
	}
	server.config = func() *ConfigView {
		return &ConfigView{SlackChannels: []*ConfigChannel{
			{Channel: "#ops", Agents: []string{"ops", "planner"}},
			{Channel: "@", Agents: []string{"adhoc"}},
		}}
	}
	client := testClient(t, server)
	ctx := principalCtx(t.Context(), "alice")

	listed, err := client.Prompt(ctx, &PromptRequest{Id: id, Text: "$agent"})
	require.NoError(t, err)
	require.Equal(t, "ops\nplanner", listed.GetPrivateText())

	denied, err := client.Prompt(ctx, &PromptRequest{Id: id, Text: "$agent helper"})
	require.NoError(t, err)
	require.Equal(t, "agent is not allowed in this channel", denied.GetPrivateText())
	require.Empty(t, recorded(server).switched)

	_, err = client.Prompt(ctx, &PromptRequest{Id: id, Text: "$agent planner"})
	require.NoError(t, err)
	require.Equal(t, []string{"planner"}, recorded(server).switched)
}

func TestSideAskAndAnswerQuestion(t *testing.T) {
	var asked string

	server := testServer(&stubRouter{}, listIDs(), func(context.Context, string) ([]*TranscriptEvent, error) { return nil, nil })
	server.sideAsk = func(_ context.Context, req protocol.SideAskRequest) error {
		asked = req.Question
		return nil
	}
	client := testClient(t, server)
	ctx := principalCtx(t.Context(), "alice")
	stream, err := client.SideAsk(ctx, &SideAskRequest{SessionId: protocol.WebSessionConversationID("ops"), Question: "why?"})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Error(t, err)
	require.Equal(t, "why?", asked)

	_, err = client.AnswerQuestion(ctx, &AnswerQuestionRequest{AskId: "ask", Answer: "yes", Dismiss: true})
	require.NoError(t, err)
}

func TestListAndRunCronJobs(t *testing.T) {
	var ran string

	server := testServer(&stubRouter{}, listIDs(), func(context.Context, string) ([]*TranscriptEvent, error) { return nil, nil })
	server.listCron = func(context.Context) ([]*CronJob, error) {
		return []*CronJob{{
			Stem: "daily", Status: "idle", LastRun: "2026-09-01T00:00:00Z", NextRun: "run-1",
			Schedule: "0 0 * * *", Body: "do it", Agent: "cron", Channel: "#ops",
			Upcoming: []string{"2026-09-03T00:00:00Z"}, Origin: "cron/daily.md",
		}, {Stem: "ops", Status: "idle"}}, nil
	}
	server.runCron = func(_ context.Context, stem string) (string, error) {
		ran = stem
		return "one-off-cron:cron/daily.md:x:y", nil
	}
	server.agents = func() []*Agent {
		return []*Agent{{Name: "ops", Model: "gpt", Reasoning: "low", Description: "d", Verbosity: "low", Prompt: "p", Permissions: "perm", Origin: "agents/ops.md"}}
	}
	server.skills = func() []*Skill {
		return []*Skill{{Name: "bro", Description: "d", License: "MIT", Compatibility: "unix", Content: "body", Origin: "skills/bro"}}
	}
	server.config = func() *ConfigView {
		return &ConfigView{
			Workspace: "/ws", Overlays: []string{"github.com/rocketable/overlay@main"},
			Models:        []*ConfigModel{{Name: "main", Model: "gpt"}},
			SlackChannels: []*ConfigChannel{{Channel: "#ops", Agents: []string{"ops"}}},
			McpServers:    []string{"demo"}, LoggingLevel: "info", AutoApproverModel: "gpt",
			InstrumentationEnabled: true, McpExternal: true,
		}
	}
	client := testClient(t, server)
	ctx := principalCtx(t.Context(), "alice")
	listed, err := client.ListCronJobs(ctx, &ListCronJobsRequest{})
	require.NoError(t, err)
	require.Equal(t, "daily", listed.GetJobs()[0].GetStem())
	require.Equal(t, "idle", listed.GetJobs()[0].GetStatus())
	require.Equal(t, "2026-09-01T00:00:00Z", listed.GetJobs()[0].GetLastRun())
	require.Equal(t, "run-1", listed.GetJobs()[0].GetNextRun())
	require.Equal(t, "0 0 * * *", listed.GetJobs()[0].GetSchedule())
	require.Equal(t, "do it", listed.GetJobs()[0].GetBody())
	require.Equal(t, "cron", listed.GetJobs()[0].GetAgent())
	require.Equal(t, "#ops", listed.GetJobs()[0].GetChannel())
	require.Equal(t, []string{"2026-09-03T00:00:00Z"}, listed.GetJobs()[0].GetUpcoming())
	require.Equal(t, "cron/daily.md", listed.GetJobs()[0].GetOrigin())

	agents, err := client.ListAgents(ctx, &ListAgentsRequest{})
	require.NoError(t, err)
	require.Equal(t, "ops", agents.GetAgents()[0].GetName())
	require.Equal(t, "gpt", agents.GetAgents()[0].GetModel())
	require.Equal(t, "low", agents.GetAgents()[0].GetReasoning())
	require.Equal(t, "d", agents.GetAgents()[0].GetDescription())
	require.Equal(t, "low", agents.GetAgents()[0].GetVerbosity())
	require.Equal(t, "p", agents.GetAgents()[0].GetPrompt())
	require.Equal(t, "perm", agents.GetAgents()[0].GetPermissions())
	require.Equal(t, "agents/ops.md", agents.GetAgents()[0].GetOrigin())

	skills, err := client.ListSkills(ctx, &ListSkillsRequest{})
	require.NoError(t, err)
	require.Equal(t, "bro", skills.GetSkills()[0].GetName())
	require.Equal(t, "d", skills.GetSkills()[0].GetDescription())
	require.Equal(t, "MIT", skills.GetSkills()[0].GetLicense())
	require.Equal(t, "unix", skills.GetSkills()[0].GetCompatibility())
	require.Equal(t, "body", skills.GetSkills()[0].GetContent())
	require.Equal(t, "skills/bro", skills.GetSkills()[0].GetOrigin())

	cfg, err := client.ListConfig(ctx, &ListConfigRequest{})
	require.NoError(t, err)
	require.Equal(t, "/ws", cfg.GetConfig().GetWorkspace())
	require.Equal(t, []string{"github.com/rocketable/overlay@main"}, cfg.GetConfig().GetOverlays())
	require.Equal(t, "main", cfg.GetConfig().GetModels()[0].GetName())
	require.Equal(t, "gpt", cfg.GetConfig().GetModels()[0].GetModel())
	require.Equal(t, "#ops", cfg.GetConfig().GetSlackChannels()[0].GetChannel())
	require.Equal(t, []string{"ops"}, cfg.GetConfig().GetSlackChannels()[0].GetAgents())
	require.Equal(t, []string{"demo"}, cfg.GetConfig().GetMcpServers())
	require.Equal(t, "info", cfg.GetConfig().GetLoggingLevel())
	require.Equal(t, "gpt", cfg.GetConfig().GetAutoApproverModel())
	require.True(t, cfg.GetConfig().GetInstrumentationEnabled())
	require.True(t, cfg.GetConfig().GetMcpExternal())

	started, err := client.RunCronJob(ctx, &RunCronJobRequest{Stem: "daily"})
	require.NoError(t, err)
	require.Equal(t, "daily", ran)
	require.Equal(t, "one-off-cron:cron/daily.md:x:y", started.GetId())
}

func TestServerHomeUsesRuntimeStore(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	log := slog.New(slog.DiscardHandler)
	sessions, err := backend.NewSessionServiceIn(dsn, log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop(context.Background())) })

	rt := backend.RuntimeFor()
	rt.Sessions = sessions
	rt.Log = log
	rt.Cfg = &config.Config{Workspace: t.TempDir(), Models: map[string]string{"main": "gpt"}, Users: map[string]string{"alice": "alice"}}
	server := New(rt)
	client := testClient(t, server)
	ctx := principalCtx(t.Context(), "alice")

	listed, err := client.ListSessions(ctx, &ListSessionsRequest{})
	require.NoError(t, err)
	require.Empty(t, listed.GetSessions())

	hist, err := client.History(ctx, &HistoryRequest{Id: protocol.WebSessionConversationID("ops")})
	require.NoError(t, err)
	require.Empty(t, hist.GetTexts())

	cfg, err := client.ListConfig(ctx, &ListConfigRequest{})
	require.NoError(t, err)
	require.Equal(t, "main", cfg.GetConfig().GetModels()[0].GetName())

	agents, err := client.ListAgents(ctx, &ListAgentsRequest{})
	require.NoError(t, err)
	require.Empty(t, agents.GetAgents())

	skills, err := client.ListSkills(ctx, &ListSkillsRequest{})
	require.NoError(t, err)
	require.Empty(t, skills.GetSkills())
}

func TestPromptStopCancelsSlackStartedTurn(t *testing.T) {
	id := protocol.SlackThreadConversationID("C1", "1.2")
	server := testServer(&stubRouter{}, listIDs(id), func(context.Context, string) ([]*TranscriptEvent, error) { return nil, nil })
	client := testClient(t, server)
	_, err := client.Prompt(principalCtx(t.Context(), "alice"), &PromptRequest{Id: id, Text: "$stop"})
	require.NoError(t, err)
	require.Equal(t, []protocol.TurnRequest{{ID: id, Kind: protocol.TurnCancel}}, recorded(server).turns)
}

func TestJoinUsesObserveThenSubscribeNotReplay(t *testing.T) {
	id := protocol.WebSessionConversationID("ops")
	other := protocol.WebSessionConversationID("other")
	server := testServer(&stubRouter{}, listIDs(id), func(context.Context, string) ([]*TranscriptEvent, error) {
		return []*TranscriptEvent{{Text: "stored", Role: "user"}}, nil
	})
	client := testClient(t, server)
	stream, err := client.Join(principalCtx(t.Context(), "alice"), &JoinRequest{Id: id})
	require.NoError(t, err)
	snap, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "stored", snap.GetText())
	require.True(t, snap.GetSnapshot())

	dropped := protocol.NewOutboundMessage(protocol.SourceWeb, other, "secret", protocol.OutputTargetWeb)
	dropped.Complete = true
	require.Equal(t, protocol.BroadcastHandled, server.HandleBroadcast(t.Context(), &protocol.Broadcast{Message: dropped, Delivery: dropped}).Status)

	live := protocol.NewOutboundMessage(protocol.SourceWeb, id, "live", protocol.OutputTargetWeb)
	live.Complete = true
	require.Equal(t, protocol.BroadcastHandled, server.HandleBroadcast(t.Context(), &protocol.Broadcast{Message: live, Delivery: live}).Status)

	got, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "live", got.GetText())
	require.False(t, got.GetSnapshot())
}

func TestPromptRejectsLockedX(t *testing.T) {
	server := testServer(&stubRouter{}, listIDs(), func(context.Context, string) ([]*TranscriptEvent, error) { return nil, nil })
	client := testClient(t, server)
	_, err := client.Prompt(principalCtx(t.Context(), "alice"), &PromptRequest{Id: "external_mcp:private", Text: "hello"})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Empty(t, recorded(server).turns)
}

func TestSessionEntriesListLoadDelete(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	log := slog.New(slog.DiscardHandler)
	sessions, err := backend.NewSessionServiceIn(dsn, log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop(context.Background())) })

	id := protocol.WebSessionConversationID("ops")
	require.NoError(t, sessions.UpsertThread(id, backend.ThreadState{Agent: "main"}))
	require.NoError(t, sessions.BeginGoal(id, "ship", "", 3, "", ""))
	_, err = sessions.AppendEntryID(t.Context(), id, &harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC()})
	require.NoError(t, err)

	rt := backend.RuntimeFor()
	rt.Sessions = sessions
	rt.Log = log
	rt.Cfg = &config.Config{Workspace: t.TempDir(), Users: map[string]string{"alice": "alice"}}
	server := New(rt)
	server.listCron = inertCronList
	server.runCron = inertCronRun
	server.sideAsk = inertSideAsk
	server.agents = inertAgents
	server.skills = inertSkills
	server.config = inertConfig
	server.settle = inertSettle
	client := testClient(t, server)
	ctx := principalCtx(t.Context(), "alice")

	listed, err := client.ListSessionEntries(ctx, &SessionEntriesRequest{Id: id})
	require.NoError(t, err)
	require.Len(t, listed.GetEntries(), 1)
	require.Equal(t, "turn", listed.GetEntries()[0].GetType())

	loaded, err := client.LoadSessionEntries(ctx, &SessionEntriesRequest{Id: id})
	require.NoError(t, err)
	require.Len(t, loaded.GetEntries(), 1)
	require.Contains(t, loaded.GetEntries()[0].GetJson(), `"type":"turn"`)

	deleted, err := client.DeleteSessionEntries(ctx, &SessionEntriesRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted.GetDeleted())

	loaded, err = client.LoadSessionEntries(ctx, &SessionEntriesRequest{Id: id})
	require.NoError(t, err)
	require.Empty(t, loaded.GetEntries())

	thread, ok, err := sessions.Thread(id)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "main", thread.Agent)

	goal, ok, err := sessions.Goal(id)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ship", goal.Objective)
}
