package rpc

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestSpacePermissions(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	cfg := &config.Config{
		DatabaseURL: dsn, Workspace: t.TempDir(), Permissions: map[string][]string{"alice@example.com": {"Ops"}},
		OpenCode: map[string]config.OpenCodeConfig{"box": {URL: "https://box.example"}},
		Slack:    config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"main-agent", "ops-agent", "loose"}}}},
	}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	runtime := cfg.RuntimeDirName()
	require.NoError(t, root.MkdirAll(runtime+"/agents", 0o700))
	require.NoError(t, root.MkdirAll(runtime+"/skills/notes", 0o700))
	require.NoError(t, root.WriteFile(runtime+"/skills/notes/SKILL.md", []byte("---\nname: notes\ndescription: Take notes\n---\nNotes.\n"), 0o600))

	for name, space := range map[string]string{"ops-agent": "space: Ops\n", "main-agent": "space: Main\n", "loose": ""} {
		require.NoError(t, root.WriteFile(runtime+"/agents/"+name+".md", []byte("---\nmodel: gpt-6.1-sol\n"+space+"permission:\n  skill: {notes: allow}\n  rocketclaw:\n    rocketclaw_set_tag: [[x]]\n---\nHelp."), 0o600))
		require.NoError(t, sessions.PutPage(t.Context(), &backend.Page{Agent: name, Kind: backend.PageFile, Name: "index.html", ContentType: "text/html", Body: []byte("<p>page</p>")}))
		require.NoError(t, sessions.PutPage(t.Context(), &backend.Page{Agent: name, Kind: backend.PageData, Name: "rows", ContentType: "application/json", Body: []byte("[1]")}))
	}

	rt := &backend.Runtime{Sessions: sessions}
	at := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)

	for _, chat := range []protocol.Conversation{
		{ID: "ops-born", Agent: "ops-agent", Space: "Ops"},
		{ID: "main-born", Agent: "main-agent", Space: "Main"},
		{ID: "ops-unborn", Agent: "ops-agent"},
		{ID: "loose-unborn", Agent: "loose"},
		{ID: "switched", Agent: "main-agent", Space: "Ops"},
	} {
		require.NoError(t, rt.CreateConversation(t.Context(), chat))

		content := "needle"
		if chat.ID == "main-born" {
			content += " <@U05MD4PEVHN>"
		}

		_, err := sessions.AppendEntryID(t.Context(), chat.ID, &harness.SessionEntry{Version: 1, Type: "turn", Timestamp: at, ReplayInput: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","content":"` + content + `"}`),
		}})
		require.NoError(t, err)
	}

	for id, path := range map[string]string{"cron:ops": "cron/ops-report.md", "cron:main": "cron/main-report.md", "cron:gone": "cron/gone.md"} {
		require.NoError(t, rt.CreateConversation(t.Context(), protocol.Conversation{ID: id, Agent: "loose", CreatedBy: "cron", Run: &protocol.CronRun{Path: path, At: at}}))
		_, err := sessions.AppendEntryID(t.Context(), id, &harness.SessionEntry{Version: 1, Type: "turn", Timestamp: at})
		require.NoError(t, err)
	}

	crons := map[string]protocol.OneOffCronjob{"ops-report": {Agent: "loose", Space: "Ops"}, "main-report": {Agent: "main-agent"}, "ops-agent-report": {Agent: "ops-agent", Space: "Ops"}}
	ran := make(chan string, 2)
	release := make(chan struct{})
	core := withoutBackgroundJobs()
	core.QueueItemsFunc = func(string) ([]protocol.ThreadQueueItem, error) { return nil, nil }
	core.ReloadFunc = func(string) (string, error) { return "reloaded", nil }
	core.CreateConversationFunc = func(context.Context, protocol.Conversation) error { return nil }
	core.ContinueRunFunc = func(_ context.Context, _, id string, _ bool) (string, error) { return id, nil }
	server := New(core, sessions, config.NewLockedConfig(cfg), &mockChannels{SlackTagsMatchingFunc: func(context.Context, string) []string {
		<-release
		return []string{"U05MD4PEVHN"}
	}}, &mockCronJobs{LoadOneOffCronjobFunc: func(stem string) (protocol.OneOffCronjob, error) {
		job, ok := crons[stem]
		if !ok {
			return job, os.ErrNotExist
		}

		return job, nil
	}, RunOneOffCronjobFunc: func(_ context.Context, job *protocol.OneOffCronjob) (protocol.CronRunResult, error) {
		ran <- job.Agent
		return protocol.CronRunResult{}, nil
	}, JobsFunc: func() ([]cronfrontend.Job, error) {
		return []cronfrontend.Job{{RelativePath: "cron/ops-report.md", Agent: "loose", Space: "Ops"}, {RelativePath: "cron/main-report.md", Agent: "main-agent"}}, nil
	}})

	alice := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-tailnet-login-bin", "alice@example.com", "rocketclaw-tailnet-name-bin", "Alice"))
	bob := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-tailnet-login-bin", "bob@example.com", "rocketclaw-tailnet-name-bin", "Bob"))
	theirs, every := []string{"ops-born", "ops-unborn", "switched"}, []string{"loose-unborn", "main-born", "ops-born", "ops-unborn", "switched"}

	listSessions := func(ctx context.Context) (first *ListSessionsResponse, ids []string) {
		require.NoError(t, server.listSessions(&ListSessionsRequest{}, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(message any) error {
			response := message.(*ListSessionsResponse)
			if first == nil {
				first = response
			}

			for _, session := range response.Sessions {
				ids = append(ids, session.Id)
			}

			return nil
		}}))
		slices.Sort(ids)

		return first, ids
	}

	first, ids := listSessions(alice)
	require.Equal(t, theirs, ids)
	require.True(t, proto.Equal(&ListSessionsResponse{Owner: "alice@example.com", AgentTags: map[string]*TagChoices{"ops-agent": {Tags: []string{"x"}}}}, first), "%v", first)

	first, ids = listSessions(bob)
	require.Equal(t, every, ids)
	require.True(t, proto.Equal(&ListSessionsResponse{Owner: "bob@example.com", OpencodeServers: map[string]string{"box": "https://box.example"}, AgentTags: map[string]*TagChoices{
		"ops-agent": {Tags: []string{"x"}}, "main-agent": {Tags: []string{"x"}}, "loose": {Tags: []string{"x"}},
	}}, first), "%v", first)

	for _, tc := range []struct {
		name    string
		read    func(ctx context.Context, id string) error
		allowed codes.Code
	}{
		{"History", func(ctx context.Context, id string) error {
			_, err := server.historyRPC(ctx, &HistoryRequest{Id: id})
			return err
		}, codes.OK},
		{"ListQueue", func(ctx context.Context, id string) error {
			_, err := server.listQueue(ctx, &ListQueueRequest{Id: id})
			return err
		}, codes.OK},
		{"LoadSessionEntries", func(ctx context.Context, id string) error {
			_, err := server.LoadSessionEntries(ctx, &SessionEntriesRequest{Id: id})
			return err
		}, codes.OK},
		{"Join", func(ctx context.Context, id string) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			return server.join(&JoinRequest{Id: id}, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(any) error {
				cancel()
				return nil
			}})
		}, codes.OK},
		{"WatchTyping", func(ctx context.Context, id string) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			return server.watchTyping(&WatchTypingRequest{Id: id}, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(any) error {
				cancel()
				return nil
			}})
		}, codes.OK},
		{"ReportTyping", func(ctx context.Context, id string) error {
			_, err := server.reportTyping(ctx, &ReportTypingRequest{Id: id, Typing: true})
			return err
		}, codes.OK},
		{"DownloadAttachment", func(ctx context.Context, id string) error {
			return server.downloadAttachment(&mockServerStream{ContextFunc: func() context.Context { return ctx }, RecvMsgFunc: func(message any) error {
				proto.Merge(message.(*Attachment), &Attachment{ConversationId: id, Id: "file"})
				return nil
			}})
		}, codes.NotFound},
	} {
		require.Equal(t, codes.PermissionDenied, status.Code(tc.read(alice, "main-born")), tc.name)
		require.Equal(t, tc.allowed, status.Code(tc.read(alice, "switched")), tc.name)
		require.Equal(t, tc.allowed, status.Code(tc.read(bob, "main-born")), tc.name)
	}

	// Typing keeps to the rules for viewing a chat, which close shadow sessions and private conversations, here a
	// cron run, even to people with every space.
	require.NoError(t, rt.CreateConversation(t.Context(), protocol.Conversation{ID: "ops-shadow", Agent: "ops-agent", Space: "Ops"}))
	_, err = openTestTurns(t, dsn).db.ExecContext(t.Context(), `UPDATE conversations SET shadow_parent = 'ops-born', parent_agent = 'ops-agent', shadow_agent = 'ops-agent/a' WHERE conversation_id = 'ops-shadow'`)
	require.NoError(t, err)

	for _, id := range []string{"ops-shadow", "cron:ops"} {
		_, err := server.reportTyping(bob, &ReportTypingRequest{Id: id, Typing: true})
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%s: %v", id, err)

		err = server.watchTyping(&WatchTypingRequest{Id: id}, &mockServerStream{ContextFunc: func() context.Context { return bob }})
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%s: %v", id, err)
	}

	for _, tc := range []struct {
		ctx  context.Context
		id   string
		want codes.Code
	}{
		{alice, "cron:ops", codes.OK},
		{alice, "cron:main", codes.PermissionDenied},
		{alice, "cron:gone", codes.PermissionDenied},
		{bob, "cron:main", codes.OK},
		{bob, "cron:gone", codes.OK},
	} {
		_, err := server.historyRPC(tc.ctx, &HistoryRequest{Id: tc.id})
		require.Equal(t, tc.want, status.Code(err), "%s: %v", tc.id, err)
	}

	for _, tc := range []struct {
		ctx     context.Context
		id      string
		want    []string
		current string
	}{
		{alice, "", []string{"ops-agent"}, ""},
		{alice, "switched", []string{"ops-agent"}, "main-agent"},
		{bob, "", []string{"loose", "main-agent", "ops-agent"}, ""},
	} {
		listed, err := server.listAgents(tc.ctx, tc.id)
		require.NoError(t, err)

		var names []string
		for _, agent := range listed.Agents {
			names = append(names, agent.Name)
		}

		require.Equal(t, tc.want, names, tc.id)
		require.Equal(t, tc.current, listed.CurrentAgent, tc.id)
	}

	// Alice's search waits in its Slack tag lookup while Bob's starts, so a flight shared across them
	// would hand one of them the other's hits.
	found := make([]*SearchMessagesResponse, 2)

	var searches errgroup.Group

	for i, ctx := range []context.Context{alice, bob} {
		searches.Go(func() (err error) {
			found[i], err = server.searchMessages(ctx, &SearchMessagesRequest{Query: "needle"})
			return err
		})
		require.Eventually(t, func() bool { return searchWaiters(server) == i+1 }, time.Second, time.Millisecond)
	}

	close(release)
	require.NoError(t, searches.Wait())

	for i, want := range []struct{ hits, tags []string }{{theirs, nil}, {every, []string{"U05MD4PEVHN"}}} {
		var hits []string
		for _, match := range found[i].Matches {
			hits = append(hits, match.ConversationId)
		}

		require.Equal(t, want.hits, hits)
		require.Equal(t, want.tags, found[i].TagIds)
	}

	pages, err := server.listPages(alice)
	require.NoError(t, err)
	require.True(t, proto.Equal(&ListPagesResponse{Pages: []*PageEntry{{Agent: "ops-agent", Space: "Ops", Path: "index.html"}}}, pages), "%v", pages)

	pages, err = server.listPages(bob)
	require.NoError(t, err)
	require.True(t, proto.Equal(&ListPagesResponse{Pages: []*PageEntry{
		{Agent: "loose", Path: "index.html"}, {Agent: "main-agent", Space: "Main", Path: "index.html"}, {Agent: "ops-agent", Space: "Ops", Path: "index.html"},
	}}, pages), "%v", pages)

	for _, request := range []*PageRequest{{Path: "index.html"}, {Path: "rows", Data: true}} {
		for agent, want := range map[string]codes.Code{"ops-agent": codes.OK, "main-agent": codes.NotFound, "loose": codes.NotFound} {
			request.Agent = agent
			_, err := server.getPage(alice, request)
			require.Equal(t, want, status.Code(err), "%s %s", agent, request.Path)
			_, err = server.getPage(bob, request)
			require.NoError(t, err, "%s %s", agent, request.Path)
		}
	}

	for _, tc := range []struct {
		ctx   context.Context
		agent string
		want  []string
	}{
		{alice, "main-agent", nil},
		{alice, "ops-agent", []string{"notes"}},
		{bob, "main-agent", []string{"notes"}},
	} {
		skills, err := server.listSkills(tc.ctx, &ListSkillsRequest{Agent: tc.agent})
		require.NoError(t, err)

		var names []string
		for _, skill := range skills.Skills {
			names = append(names, skill.Name)
		}

		require.Equal(t, tc.want, names, tc.agent)
	}

	for ctx, want := range map[context.Context][]string{alice: {"ops-agent"}, bob: {"main-agent", "ops-agent", "loose"}} {
		view, err := server.listConfig(ctx)
		require.NoError(t, err)
		require.True(t, proto.Equal(&ConfigChannel{Channel: "#ops", Agents: want}, view.Config.SlackChannels[0]), "%v", view.Config.SlackChannels)
		require.Len(t, view.Config.SlackChannels, 1)
	}

	_, err = server.reload(alice, &ReloadRequest{Reason: "assets"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	reloaded, err := server.reload(bob, &ReloadRequest{Reason: "assets"})
	require.NoError(t, err)
	require.Equal(t, "reloaded", reloaded.Message)
	require.Len(t, core.ReloadCalls(), 1)

	for ctx, want := range map[context.Context][]string{alice: {"cron/ops-report.md", "cron:ops"}, bob: {"cron/ops-report.md", "cron/main-report.md", "cron:gone", "cron:main", "cron:ops"}} {
		listed, err := server.listCronJobs(ctx)
		require.NoError(t, err)

		var origins []string
		for _, job := range listed.Jobs {
			origins = append(origins, job.Origin)
		}

		require.Equal(t, want, origins)
	}

	for _, stem := range []string{"main-report", "ops-report"} {
		_, err := server.runCronJob(alice, &RunCronJobRequest{Stem: stem})
		require.Equal(t, codes.PermissionDenied, status.Code(err), stem)
	}

	for ctx, stem := range map[context.Context]string{alice: "ops-agent-report", bob: "main-report"} {
		_, err := server.runCronJob(ctx, &RunCronJobRequest{Stem: stem})
		require.NoError(t, err, stem)
		require.Equal(t, crons[stem].Agent, <-ran, stem)
	}

	for _, tc := range []struct {
		ctx     context.Context
		request *CreateSessionRequest
		want    codes.Code
		space   string
	}{
		{alice, &CreateSessionRequest{Agent: "main-agent"}, codes.InvalidArgument, ""},
		{alice, &CreateSessionRequest{Agent: "loose"}, codes.InvalidArgument, ""},
		{alice, &CreateSessionRequest{Agent: "ops-agent"}, codes.OK, "Ops"},
		{alice, &CreateSessionRequest{SourceConversationId: "cron:main"}, codes.PermissionDenied, ""},
		{alice, &CreateSessionRequest{SourceConversationId: "cron:ops"}, codes.OK, "Ops"},
		{bob, &CreateSessionRequest{Agent: "main-agent"}, codes.OK, "Main"},
		{bob, &CreateSessionRequest{SourceConversationId: "cron:main"}, codes.OK, "Main"},
	} {
		calls := len(core.CreateConversationCalls())
		_, err := server.createSession(tc.ctx, tc.request)
		require.Equal(t, tc.want, status.Code(err), "%v: %v", tc.request, err)

		if tc.want == codes.OK {
			require.Equal(t, tc.space, core.CreateConversationCalls()[calls].Conversation.Space, "%v", tc.request)
		}
	}

	for _, tc := range []struct {
		ctx  context.Context
		id   string
		want codes.Code
	}{
		{alice, "switched", codes.FailedPrecondition},
		{alice, "ops-born", codes.OK},
		{bob, "switched", codes.OK},
	} {
		_, err := server.forkSession(tc.ctx, &ForkSessionRequest{Id: tc.id})
		require.Equal(t, tc.want, status.Code(err), "%s: %v", tc.id, err)
	}

	_, err = server.createSession(alice, &CreateSessionRequest{OpencodeServer: "box", Directory: "/srv/app"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = server.forkSession(alice, &ForkSessionRequest{Id: "opencode:box:ses_a"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	for ctx, want := range map[context.Context]*IdentityResponse{
		alice: {Username: "alice@example.com", Principal: "Alice", Limited: new(true)},
		bob:   {Username: "bob@example.com", Principal: "Bob"},
	} {
		identity, err := server.identity(ctx)
		require.NoError(t, err)
		require.True(t, proto.Equal(want, identity), "%v", identity)
	}
}

func TestSpacePermissionTurns(t *testing.T) {
	h := startVoiceHarness(t)
	cfg := h.server.cfg.Clone()

	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	for name, space := range map[string]string{"ops-agent": "Ops", "main-agent": "Main"} {
		require.NoError(t, root.WriteFile(cfg.RuntimeDirName()+"/agents/"+name+".md", []byte("---\nmodel: gpt-6.1-sol\nspace: "+space+"\npermission: {}\n---\nYou are "+name+".\n"), 0o600))
	}

	cfg.Permissions = map[string][]string{"alice": {"Ops"}}
	h.server.cfg.Store(cfg)

	for id, agent := range map[string]string{"ops-chat": "ops-agent", "switched": "main-agent"} {
		require.NoError(t, h.rt.CreateConversation(t.Context(), protocol.Conversation{ID: id, Agent: agent, CreatedBy: "bob", Space: "Ops"}))
	}

	alice := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
	bob := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "192.0.2.2"))
	prompt := func(ctx context.Context, id, text string, delivery PromptDelivery) (*PromptResponse, error) {
		return h.server.prompt(ctx, &PromptRequest{Id: id, Text: text, Delivery: delivery})
	}
	started := func(ctx context.Context, id string) (string, *errgroup.Group) {
		var turn errgroup.Group

		turn.Go(func() error {
			_, err := prompt(ctx, id, "work", PromptDelivery_STEER)
			return err
		})

		return <-h.requests, &turn
	}
	finish := func(turn *errgroup.Group) {
		h.replies <- voiceModelText(rand.Text(), "Done.")

		require.NoError(t, turn.Wait())
	}

	_, err = prompt(bob, "switched", "held", PromptDelivery_STASH)
	require.NoError(t, err)

	listed, err := h.server.listQueue(bob, &ListQueueRequest{Id: "switched"})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)

	held := listed.Items[0].Id

	for _, tc := range []struct {
		name string
		feed func(ctx context.Context) error
		bob  *codes.Code // what Bob gets, checked when set
	}{
		{"EditQueueItem", func(ctx context.Context) error {
			_, err := h.server.queueItem(ctx, "EditQueueItem", &QueueItemRequest{Id: "switched", ItemId: held, Text: "edited"})
			return err
		}, new(codes.OK)},
		{"SteerQueueItem", func(ctx context.Context) error {
			_, err := h.server.queueItem(ctx, "SteerQueueItem", &QueueItemRequest{Id: "switched", ItemId: held})
			return err
		}, nil},
		{"PopQueueItem", func(ctx context.Context) error {
			_, err := h.server.queueItem(ctx, "PopQueueItem", &QueueItemRequest{Id: "switched", ItemId: held})
			return err
		}, nil},
		{"AnswerQuestion", func(ctx context.Context) error {
			_, err := h.server.answerQuestion(ctx, &AnswerQuestionRequest{ConversationId: "switched", AskId: "ask"})
			return err
		}, new(codes.NotFound)},
		{"Handoff", func(ctx context.Context) error {
			_, err := h.server.handoff(ctx, &HandoffRequest{Id: "switched"})
			return err
		}, nil},
		{"Prompt", func(ctx context.Context) error {
			_, err := prompt(ctx, "switched", "hello", PromptDelivery_QUEUE)
			return err
		}, nil},
		{"Voice", func(ctx context.Context) error {
			return h.server.voice(&VoiceRequest{ConversationId: "switched", Sdp: "offer-x"}, &mockServerStream{ContextFunc: func() context.Context { return ctx }})
		}, nil},
		{"UpdateSession", func(ctx context.Context) error {
			_, err := h.server.updateSession(ctx, &UpdateSessionRequest{Id: "switched", Paused: new(true)})
			return err
		}, nil},
	} {
		err := tc.feed(alice)
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%s: %v", tc.name, err)
		require.Contains(t, status.Convert(err).Message(), "$agent", tc.name)

		if tc.bob != nil {
			require.Equal(t, *tc.bob, status.Code(tc.feed(bob)), tc.name)
		}
	}

	// Pausing follows prompting: alice pauses and resumes the chat, with history, whose agent is in her spaces.
	_, err = h.rt.Sessions.AppendEntryID(t.Context(), "ops-chat", &harness.SessionEntry{Timestamp: time.Now().UTC()})
	require.NoError(t, err)

	for _, paused := range []bool{true, false} {
		_, err = h.server.updateSession(alice, &UpdateSessionRequest{Id: "ops-chat", Paused: &paused})
		require.NoError(t, err)

		for chat, err := range h.rt.Sessions.SidebarChats(t.Context(), "alice", h.server.channels, []string{"ops-chat"}) {
			require.NoError(t, err)
			require.Equal(t, paused, chat.Session.Paused)
		}
	}

	_, err = h.server.reorderQueue(alice, &ReorderQueueRequest{Id: "switched", ItemIds: []string{held}})
	require.NoError(t, err)
	_, err = h.server.queueItem(alice, "RemoveQueueItem", &QueueItemRequest{Id: "switched", ItemId: held})
	require.NoError(t, err)

	_, err = prompt(alice, "switched", "$stop", PromptDelivery_STEER)
	require.NoError(t, err)
	_, err = prompt(alice, "switched", "$agent main-agent", PromptDelivery_STEER)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	offered, err := prompt(alice, "switched", "$agent", PromptDelivery_STEER)
	require.NoError(t, err)
	require.Equal(t, "ops-agent", offered.PrivateText)

	_, err = h.server.voiceSettings(alice, &VoiceSettingsRequest{Action: VoiceSettingsAction_UPDATE, Agent: new("main-agent")})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	request, turn := started(alice, "ops-chat")
	require.Contains(t, request, "You are ops-agent.")
	finish(turn)

	request, turn = started(bob, "switched")
	require.Contains(t, request, "You are main-agent.")

	switched, err := prompt(alice, "switched", "$agent ops-agent", PromptDelivery_STEER)
	require.NoError(t, err)
	require.Equal(t, "Agent: ops-agent", switched.PrivateText)

	steered := make(chan error, 1)

	go func() {
		_, err := prompt(alice, "switched", "alice steer", PromptDelivery_STEER)
		steered <- err
	}()

	var errSteer error
	select {
	case errSteer = <-steered:
	case <-time.After(10 * time.Second):
	}

	require.Equal(t, codes.PermissionDenied, status.Code(errSteer), "%v", errSteer)
	require.Contains(t, status.Convert(errSteer).Message(), "$stop")

	_, err = prompt(alice, "switched", "alice queued", PromptDelivery_QUEUE)
	require.NoError(t, err)

	_, err = h.server.prompt(alice, &PromptRequest{Id: "switched", Text: "alice held", Delivery: PromptDelivery_STASH, MessageId: "alice-held"})
	require.NoError(t, err)
	_, err = h.server.queueItem(alice, "EditQueueItem", &QueueItemRequest{Id: "switched", ItemId: "alice-held", Text: "alice edited"})
	require.NoError(t, err)
	_, err = h.server.queueItem(alice, "SteerQueueItem", &QueueItemRequest{Id: "switched", ItemId: "alice-held"})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), "$stop")
	_, err = h.server.queueItem(alice, "PopQueueItem", &QueueItemRequest{Id: "switched", ItemId: "alice-held"})
	require.NoError(t, err)
	finish(turn)

	request = <-h.requests
	require.Contains(t, request, "You are ops-agent.")
	require.Contains(t, request, "alice queued")
	require.NotContains(t, request, "alice steer")

	h.replies <- voiceModelText(rand.Text(), "Done.")

	request = <-h.requests
	require.Contains(t, request, "You are ops-agent.")
	require.Contains(t, request, "alice edited")

	h.replies <- voiceModelText(rand.Text(), "Done.")

	require.Eventually(t, func() bool {
		active, err := h.rt.Sessions.HasActiveTurn(t.Context(), "switched")
		return err == nil && !active
	}, 10*time.Second, 10*time.Millisecond)

	reader, cancel, peer := h.call(t, "ops-chat", "offer-ops")
	_, err = h.rt.SwitchConversationAgent("ops-chat", "main-agent")
	require.NoError(t, err)
	handOff(t, peer, "refused", "please continue")
	notice := frame(t, reader)
	require.Contains(t, notice, `"notice":`)
	require.Contains(t, notice, "agent main-agent is outside your spaces")

	items, err := h.rt.QueueItems("ops-chat")
	require.NoError(t, err)
	require.Empty(t, items)

	active, err := h.rt.Sessions.HasActiveTurn(t.Context(), "ops-chat")
	require.NoError(t, err)
	require.False(t, active)

	_, err = h.rt.SwitchConversationAgent("ops-chat", "ops-agent")
	require.NoError(t, err)
	handOff(t, peer, "admitted", "go ahead")

	request = <-h.requests
	require.Contains(t, request, "You are ops-agent.")
	require.Contains(t, request, "please continue")

	h.replies <- voiceModelText(rand.Text(), "Continued.")

	peer.expect(t, "Continued.")
	cancel()
	h.ended(t)
}

// Run now follows the cron's agent: a sticky: global agent's run replies in its sticky session, and a
// sticky: user agent, which a cron cannot run, is refused.
func TestRunCronJobFollowsStickyAgent(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(cfg.RuntimeDirName()+"/agents", 0o700))

	for name, sticky := range map[string]string{"room": "global", "mine": "user", "plain": "off"} {
		require.NoError(t, root.WriteFile(cfg.RuntimeDirName()+"/agents/"+name+".md", []byte("---\nmodel: gpt-6.1-sol\nsticky: "+sticky+"\npermission: {}\n---\nHelp."), 0o600))
	}

	_, _, err = sessions.CreateStickyConversation(t.Context(), protocol.Conversation{ID: "lobby", Agent: "room", CreatedBy: "bob@example.com"}, harness.AgentStickyGlobal)
	require.NoError(t, err)

	ran := make(chan string, 3)
	core := withoutBackgroundJobs()
	core.CreateConversationFunc = func(context.Context, protocol.Conversation) error { return nil }
	server := New(core, sessions, config.NewLockedConfig(cfg), &mockChannels{}, &mockCronJobs{LoadOneOffCronjobFunc: func(stem string) (protocol.OneOffCronjob, error) {
		return protocol.OneOffCronjob{Agent: stem}, nil
	}, RunOneOffCronjobFunc: func(_ context.Context, job *protocol.OneOffCronjob) (protocol.CronRunResult, error) {
		ran <- job.ConversationID
		return protocol.CronRunResult{}, nil
	}})
	alice := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-tailnet-login-bin", "alice@example.com", "rocketclaw-tailnet-name-bin", "Alice"))

	started, err := server.runCronJob(alice, &RunCronJobRequest{Stem: "room"})
	require.NoError(t, err)
	require.Equal(t, "lobby", started.Id)
	require.Equal(t, "lobby", <-ran, "the run replies in the agent's sticky session")

	started, err = server.runCronJob(alice, &RunCronJobRequest{Stem: "plain"})
	require.NoError(t, err)
	require.NotEqual(t, "lobby", started.Id)
	require.Equal(t, started.Id, <-ran)

	_, err = server.runCronJob(alice, &RunCronJobRequest{Stem: "mine"})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}
