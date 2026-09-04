// Package rpc is the gRPC Frontend for the TypeScript web home.
package rpc

//go:generate protoc -I ../../../../proto --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative web.proto

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	clawcron "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

const (
	principalKey = "rocketclaw-principal"
	dollarHelp   = "$goal <objective> - start a goal\n$stop - end the turn\n$cron [job] - list or run cron\n$workflow <name> [args]\n$agent [name] - list or switch\n$enqueue <text> - stash later work\n$queue - list later work"
)

// Server is the web RPC Frontend.
type Server struct {
	UnimplementedWebServer

	rt       *backend.Runtime
	conv     frontend.Backend
	cron     *clawcron.Frontend
	list     func(context.Context) ([]*Session, error)
	observe  func(context.Context, string) ([]*TranscriptEvent, error)
	listCron func(context.Context) ([]*CronJob, error)
	runCron  func(context.Context, string) (string, error)
	sideAsk  func(context.Context, protocol.SideAskRequest) error
	agents   func() []*Agent
	skills   func() []*Skill
	config   func() *ConfigView
	settle   func(context.Context, string, bool) error
	agent    string

	mu      sync.Mutex
	created map[string]struct{}
	asks    map[string]context.CancelFunc
}

// New constructs a web RPC Frontend around the backend.
func New(rt *backend.Runtime) *Server {
	s := &Server{rt: rt, conv: rt, agent: "main", created: map[string]struct{}{}, asks: map[string]context.CancelFunc{}}
	s.list = s.listManagedSessions
	s.observe = s.observeTranscript
	s.listCron = s.listCronJobs
	s.runCron = s.runCronJob
	s.sideAsk = s.runSideAsk
	s.agents = s.listAgents
	s.skills = s.listSkills
	s.config = s.configView
	s.settle = s.settleSession

	return s
}

// SetCron injects the Cron Frontend constructed at assemble.
func (s *Server) SetCron(c *clawcron.Frontend) {
	s.cron = c
}

func wrapRPC(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("web rpc: %w", err)
}

func textTarget(id string) protocol.TextConversationTarget {
	if channel, thread, ok := protocol.SlackThreadTarget(id); ok {
		return protocol.TextConversationTarget{ChannelID: channel, MessageID: thread, ThreadID: thread}
	}

	return protocol.TextConversationTarget{ChannelID: id}
}

func webUserFacing(id string) bool {
	if _, ok := protocol.WebSessionName(id); ok {
		return true
	}

	_, _, ok := protocol.SlackThreadTarget(id)

	return ok
}

// HandleBroadcast fans out live output to Join subscribers. Slack delivery stays on Slack.
func (s *Server) HandleBroadcast(ctx context.Context, broadcast *protocol.Broadcast) protocol.BroadcastAcknowledgement {
	return s.rt.HandleBroadcast(ctx, broadcast)
}

// ListSessions returns web and Slack conversation ids.
func (s *Server) ListSessions(ctx context.Context, _ *ListSessionsRequest) (*ListSessionsResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	sessions, err := s.list(ctx)
	if err != nil {
		return nil, wrapRPC(err)
	}

	seen := map[string]struct{}{}
	out := &ListSessionsResponse{}
	facing := s.userFacingIDs()

	for _, session := range sessions {
		id := session.GetId()
		if !webUserFacing(id) {
			if _, ok := facing[id]; !ok {
				continue
			}
		}

		if _, ok := seen[id]; ok {
			continue
		}

		seen[id] = struct{}{}

		out.Sessions = append(out.Sessions, session)
	}

	s.mu.Lock()

	created := make([]string, 0, len(s.created))
	for id := range s.created {
		created = append(created, id)
	}
	s.mu.Unlock()
	slices.Sort(created)

	for _, id := range created {
		if _, ok := seen[id]; ok {
			continue
		}

		name, ok := protocol.WebSessionName(id)
		if !ok {
			continue
		}

		out.Sessions = append(out.Sessions, &Session{Id: id, Title: name})
	}

	return out, nil
}

// History returns stored transcript text for a conversation.
func (s *Server) History(ctx context.Context, req *HistoryRequest) (*HistoryResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	events, err := s.observe(ctx, strings.TrimSpace(req.GetId()))
	if err != nil {
		return nil, wrapRPC(err)
	}

	texts := make([]string, 0, len(events))
	for _, event := range events {
		texts = append(texts, event.GetText())
	}

	return &HistoryResponse{Texts: texts}, nil
}

// ListAgents returns configured agents.
func (s *Server) ListAgents(ctx context.Context, _ *ListAgentsRequest) (*ListAgentsResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	return &ListAgentsResponse{Agents: s.agents()}, nil
}

// ListSkills returns configured skills.
func (s *Server) ListSkills(ctx context.Context, _ *ListSkillsRequest) (*ListSkillsResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	return &ListSkillsResponse{Skills: s.skills()}, nil
}

// ListConfig returns a redacted runtime configuration view.
func (s *Server) ListConfig(ctx context.Context, _ *ListConfigRequest) (*ListConfigResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	return &ListConfigResponse{Config: s.config()}, nil
}

// SettleSession records a manual settle or unsettle override.
func (s *Server) SettleSession(ctx context.Context, req *SettleSessionRequest) (*SettleSessionResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	id := strings.TrimSpace(req.GetId())
	if req.GetSettled() && s.conv.ConversationBusy(id) {
		return nil, wrapRPC(status.Error(codes.FailedPrecondition, "session is running"))
	}

	return &SettleSessionResponse{}, wrapRPC(s.settle(ctx, id, req.GetSettled()))
}

// Protocol returns the SHA-256 of the embedded web.proto.
func (s *Server) Protocol(ctx context.Context, _ *ProtocolRequest) (*ProtocolResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	return &ProtocolResponse{ProtoSha256: protoSHA256()}, nil
}

// CreateSession registers a web-session conversation.
func (s *Server) CreateSession(ctx context.Context, req *CreateSessionRequest) (*CreateSessionResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	name := strings.TrimSpace(req.GetName())
	if name == "" {
		name = rand.Text()
	}

	agent := strings.TrimSpace(req.GetAgent())
	if agent == "" {
		agent = s.agent
	}

	id := protocol.WebSessionConversationID(name)
	if err := s.conv.CreateConversation(id, []string{agent}, []protocol.ConversationTag{protocol.ConversationUserFacing}); err != nil {
		return nil, wrapRPC(err)
	}

	s.mu.Lock()
	s.created[id] = struct{}{}
	s.mu.Unlock()

	return &CreateSessionResponse{Id: id}, nil
}

// Join sends an Observe snapshot then live web transcript events.
func (s *Server) Join(req *JoinRequest, stream grpc.ServerStreamingServer[TranscriptEvent]) error {
	if err := s.principalFrom(stream.Context()); err != nil {
		return wrapRPC(err)
	}

	id := strings.TrimSpace(req.GetId())
	if !s.allowY(id) {
		return wrapRPC(status.Error(codes.FailedPrecondition, "conversation is not user-facing"))
	}

	events, err := s.observe(stream.Context(), id)
	if err != nil {
		return wrapRPC(err)
	}

	for _, event := range events {
		event.Snapshot = true
		if err := stream.Send(event); err != nil {
			return wrapRPC(err)
		}
	}

	live := s.conv.Subscribe(stream.Context())

	for {
		select {
		case event, ok := <-live:
			if !ok {
				return nil
			}

			if event.ConversationID != id {
				continue
			}

			if err := stream.Send(&TranscriptEvent{Text: event.Text, Role: event.Role, Complete: event.Complete}); err != nil {
				return wrapRPC(err)
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

// Prompt submits originator text or a dollar command as a web turn.
func (s *Server) Prompt(ctx context.Context, req *PromptRequest) (*PromptResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	id := strings.TrimSpace(req.GetId())
	if !s.allowY(id) {
		return nil, wrapRPC(status.Error(codes.FailedPrecondition, "conversation is not user-facing"))
	}

	text := req.GetText()
	if command, args, ok := protocol.ParseDollarCommand(text); ok {
		return s.dispatchCommand(ctx, id, command, args)
	}

	kind := protocol.TurnPrompt
	if req.GetDelivery() == PromptDelivery_QUEUE {
		kind = protocol.TurnEnqueue
	}

	return &PromptResponse{}, wrapRPC(s.conv.RunTurn(ctx, &protocol.TurnRequest{ID: id, Kind: kind, Text: text, Agent: s.agent}))
}

// ListQueue returns later-work rows for a conversation.
func (s *Server) ListQueue(ctx context.Context, req *ListQueueRequest) (*ListQueueResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	items, err := s.conv.ListLaterWork(ctx, strings.TrimSpace(req.GetId()))
	if err != nil {
		return nil, wrapRPC(err)
	}

	out := make([]*QueueItem, 0, len(items))
	for i := range items {
		out = append(out, &QueueItem{Id: items[i].ID, Text: items[i].Message})
	}

	return &ListQueueResponse{Items: out}, nil
}

// RemoveQueueItem drops one later-work row.
func (s *Server) RemoveQueueItem(ctx context.Context, req *QueueItemRequest) (*QueueItemResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	return &QueueItemResponse{}, wrapRPC(s.conv.DeleteLaterWork(ctx, strings.TrimSpace(req.GetId()), strings.TrimSpace(req.GetItemId())))
}

// ReorderQueue writes later-work row order for a conversation.
func (s *Server) ReorderQueue(ctx context.Context, req *ReorderQueueRequest) (*QueueItemResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	return &QueueItemResponse{}, wrapRPC(s.conv.ReorderLaterWork(ctx, strings.TrimSpace(req.GetId()), req.GetItemIds()))
}

// SteerQueueItem promotes one later-work row into the active turn, or starts it when idle.
func (s *Server) SteerQueueItem(ctx context.Context, req *QueueItemRequest) (*QueueItemResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	id := strings.TrimSpace(req.GetId())
	itemID := strings.TrimSpace(req.GetItemId())

	items, err := s.conv.ListLaterWork(ctx, id)
	if err != nil {
		return nil, wrapRPC(err)
	}

	i := slices.IndexFunc(items, func(item protocol.ThreadQueueItem) bool { return item.ID == itemID })
	if i < 0 {
		return nil, wrapRPC(status.Error(codes.NotFound, "queue item not found"))
	}

	item := items[i]
	if err := s.conv.RunTurn(ctx, &protocol.TurnRequest{ID: id, Kind: protocol.TurnPrompt, Text: item.Message, Agent: s.agent}); err != nil {
		return nil, wrapRPC(err)
	}

	return &QueueItemResponse{}, wrapRPC(s.conv.DeleteLaterWork(ctx, id, itemID))
}

// ListCronJobs returns web-targeted cron jobs.
func (s *Server) ListCronJobs(ctx context.Context, _ *ListCronJobsRequest) (*ListCronJobsResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	jobs, err := s.listCron(ctx)
	if err != nil {
		return nil, wrapRPC(err)
	}

	return &ListCronJobsResponse{Jobs: jobs}, nil
}

// RunCronJob triggers a web-targeted cron job.
func (s *Server) RunCronJob(ctx context.Context, req *RunCronJobRequest) (*RunCronJobResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	id, err := s.runCron(ctx, req.GetStem())
	if err != nil {
		return nil, wrapRPC(err)
	}

	return &RunCronJobResponse{Id: id}, nil
}

// SideAsk runs a private question against history through one entry.
func (s *Server) SideAsk(req *SideAskRequest, stream grpc.ServerStreamingServer[TranscriptEvent]) error {
	if err := s.principalFrom(stream.Context()); err != nil {
		return wrapRPC(err)
	}

	askID := rand.Text()
	ctx, cancel := context.WithCancel(stream.Context())

	s.mu.Lock()
	s.asks[askID] = cancel
	s.mu.Unlock()

	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.asks, askID)
		s.mu.Unlock()
	}()

	return wrapRPC(s.sideAsk(ctx, protocol.SideAskRequest{
		ConversationID: req.GetSessionId(), SessionEntryID: req.GetEntryId(), Agent: s.agent, Question: req.GetQuestion(),
		Thinking: func(_ context.Context, text string) error {
			return wrapRPC(stream.Send(&TranscriptEvent{Text: text}))
		},
		Message: func(_ context.Context, text string) error {
			return wrapRPC(stream.Send(&TranscriptEvent{Text: text}))
		},
	}))
}

// AnswerQuestion completes or dismisses a live Side Ask.
func (s *Server) AnswerQuestion(ctx context.Context, req *AnswerQuestionRequest) (*AnswerQuestionResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	s.mu.Lock()
	cancel := s.asks[req.GetAskId()]
	s.mu.Unlock()

	if req.GetDismiss() && cancel != nil {
		cancel()
	}

	return &AnswerQuestionResponse{}, nil
}

// ListSessionEntries lists stored session_entries for a conversation id.
func (s *Server) ListSessionEntries(ctx context.Context, req *SessionEntriesRequest) (*ListSessionEntriesResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	entries, err := s.rt.Sessions.ObserveEntries(ctx, strings.TrimSpace(req.GetId()), 0)
	if err != nil {
		return nil, wrapRPC(err)
	}

	out := &ListSessionEntriesResponse{}
	for i := range entries {
		out.Entries = append(out.Entries, &SessionEntryMeta{
			Id:        entries[i].ID,
			Type:      entries[i].Entry.Type,
			Timestamp: entries[i].Entry.Timestamp.UTC().Format(time.RFC3339Nano),
		})
	}

	return out, nil
}

// LoadSessionEntries returns stored session_entries JSON for a conversation id.
func (s *Server) LoadSessionEntries(ctx context.Context, req *SessionEntriesRequest) (*LoadSessionEntriesResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	entries, err := s.rt.Sessions.ObserveEntries(ctx, strings.TrimSpace(req.GetId()), 0)
	if err != nil {
		return nil, wrapRPC(err)
	}

	out := &LoadSessionEntriesResponse{}

	for i := range entries {
		raw, err := json.Marshal(entries[i].Entry)
		if err != nil {
			return nil, wrapRPC(err)
		}

		out.Entries = append(out.Entries, &SessionEntryData{Id: entries[i].ID, Json: string(raw)})
	}

	return out, nil
}

// DeleteSessionEntries deletes session_entries for a conversation id. It does not wipe managed_conversations or goals.
func (s *Server) DeleteSessionEntries(ctx context.Context, req *SessionEntriesRequest) (*DeleteSessionEntriesResponse, error) {
	if err := s.principalFrom(ctx); err != nil {
		return nil, wrapRPC(err)
	}

	deleted, err := s.rt.Sessions.DeleteSession(ctx, strings.TrimSpace(req.GetId()))
	if err != nil {
		return nil, wrapRPC(err)
	}

	return &DeleteSessionEntriesResponse{Deleted: deleted}, nil
}

func (s *Server) principalFrom(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return wrapRPC(status.Error(codes.Unauthenticated, "principal is required"))
	}

	values := md.Get(principalKey)
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return wrapRPC(status.Error(codes.Unauthenticated, "principal is required"))
	}

	if _, ok := s.rt.Cfg.UsernameForIP(strings.TrimSpace(values[0])); !ok {
		return wrapRPC(status.Error(codes.Unauthenticated, "principal is required"))
	}

	return nil
}

func (s *Server) userFacingIDs() map[string]struct{} {
	out := map[string]struct{}{}

	records, err := s.conv.ListConversations()
	if err != nil {
		return out
	}

	for _, rec := range records {
		if slices.Contains(rec.Tags, protocol.ConversationUserFacing) {
			out[rec.ID] = struct{}{}
		}
	}

	return out
}

func (s *Server) allowY(id string) bool {
	if webUserFacing(id) {
		return true
	}

	_, ok := s.userFacingIDs()[id]

	return ok
}

func (s *Server) dispatchCommand(ctx context.Context, id, command, args string) (*PromptResponse, error) {
	target := textTarget(id)

	switch command {
	case "stop":
		return &PromptResponse{}, wrapRPC(s.conv.RunTurn(ctx, &protocol.TurnRequest{ID: id, Kind: protocol.TurnCancel}))
	case "enqueue":
		if args == "" {
			return &PromptResponse{PrivateText: dollarHelp}, nil
		}

		return &PromptResponse{}, wrapRPC(s.conv.RunTurn(ctx, &protocol.TurnRequest{ID: id, Kind: protocol.TurnEnqueue, Text: args, Agent: s.agent}))
	case "queue":
		items, err := s.conv.ListLaterWork(ctx, id)
		if err != nil {
			return nil, wrapRPC(err)
		}

		lines := make([]string, 0, len(items))
		for i := range items {
			lines = append(lines, items[i].Message)
		}

		return &PromptResponse{PrivateText: strings.Join(lines, "\n")}, nil
	case "goal":
		goal, rejection := protocol.ParseGoalRequest(args)
		if rejection != "" {
			return &PromptResponse{PrivateText: rejection}, nil
		}

		return &PromptResponse{}, wrapRPC(s.conv.RunTurn(ctx, &protocol.TurnRequest{ID: id, Kind: protocol.TurnGoal, Agent: s.agent, Objective: goal.Objective, CheckScript: goal.CheckScript, MaxTurns: goal.MaxTurns, Text: args}))
	case "workflow":
		name, rest, _ := strings.Cut(args, " ")

		return &PromptResponse{}, wrapRPC(s.conv.RunTurn(ctx, &protocol.TurnRequest{ID: id, Kind: protocol.TurnWorkflow, Agent: s.agent, Workflow: name, WorkflowArgs: strings.TrimSpace(rest)}))
	case "agent":
		if target.ThreadID != "" {
			allowed, errAgents := s.slackAllowedAgents(ctx, target)
			if errAgents != nil {
				return nil, wrapRPC(errAgents)
			}

			if args == "" {
				return &PromptResponse{PrivateText: strings.Join(allowed, "\n")}, nil
			}

			if !slices.Contains(allowed, args) {
				return &PromptResponse{PrivateText: "agent is not allowed in this channel"}, nil
			}
		} else if args == "" {
			listed := s.agents()

			names := make([]string, 0, len(listed))
			for _, agent := range listed {
				names = append(names, agent.GetName())
			}

			return &PromptResponse{PrivateText: strings.Join(names, "\n")}, nil
		}

		return &PromptResponse{}, wrapRPC(s.conv.SwitchAgent(id, args))
	case "cron":
		if args == "" {
			jobs, err := s.listCron(ctx)
			if err != nil {
				return nil, wrapRPC(err)
			}

			stems := make([]string, 0, len(jobs))
			for _, job := range jobs {
				stems = append(stems, job.GetStem())
			}

			return &PromptResponse{PrivateText: strings.Join(stems, "\n")}, nil
		}

		_, err := s.runCron(ctx, args)

		return &PromptResponse{}, wrapRPC(err)
	default:
		return &PromptResponse{PrivateText: dollarHelp}, nil
	}
}

func (s *Server) slackAllowedAgents(ctx context.Context, target protocol.TextConversationTarget) ([]string, error) {
	id := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)

	sessions, err := s.list(ctx)
	if err != nil {
		return nil, err
	}

	title := ""

	for _, session := range sessions {
		if session.GetId() == id {
			title = session.GetTitle()
			break
		}
	}

	channels := s.config().GetSlackChannels()
	for _, channel := range channels {
		if channel.GetChannel() == title && len(channel.GetAgents()) > 0 {
			return channel.GetAgents(), nil
		}
	}

	for _, channel := range channels {
		if channel.GetChannel() == "@" && len(channel.GetAgents()) > 0 {
			return channel.GetAgents(), nil
		}
	}

	return nil, nil
}
