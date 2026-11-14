package rpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) createAgentDraft(ctx context.Context, username string, request *CreateAgentDraftRequest) (*AgentDraft, error) {
	draft, err := s.sessions.CreateAgentDraft(ctx, username, backend.AgentDraftOrigin{Name: request.Name, Mode: harness.AgentMode(request.Mode), Template: request.Template, Copy: request.Copy})
	if err != nil {
		return nil, agentDraftError("CreateAgentDraft", err)
	}

	return agentDraftView(draft), nil
}

func (s *Server) listAgentDrafts(ctx context.Context) (*ListAgentDraftsResponse, error) {
	drafts, err := s.sessions.ListAgentDrafts(ctx)
	if err != nil {
		return nil, fmt.Errorf("web agent drafts: %w", err)
	}

	response := &ListAgentDraftsResponse{Templates: backend.AgentDraftTemplates()}
	for i := range drafts {
		response.Drafts = append(response.Drafts, agentDraftView(&drafts[i]))
	}

	return response, nil
}

// getAgentDraft answers request's draft, its ops after since and the places seen. Opening the draft, with
// since unset, first checks whether the save it waits for was merged, as username; a check that fails
// leaves the draft waiting and is logged.
func (s *Server) getAgentDraft(ctx context.Context, username string, request *GetAgentDraftRequest) (*GetAgentDraftResponse, error) {
	if request.Since == 0 {
		if _, err := s.sessions.CheckAgentDraftMerge(ctx, s.backend, request.Id, username); err != nil {
			slog.Warn("check agent draft merge", "draft", request.Id, "error", err)
		}
	}

	draft, err := s.sessions.LoadAgentDraft(ctx, request.Id)
	if err != nil {
		return nil, agentDraftError("GetAgentDraft", err)
	}

	ops, err := s.sessions.AgentDraftOps(ctx, request.Id, request.Since)
	if err != nil {
		return nil, fmt.Errorf("web agent draft ops: %w", err)
	}

	presences, err := s.sessions.AgentDraftPresences(ctx, request.Id)
	if err != nil {
		return nil, fmt.Errorf("web agent draft presence: %w", err)
	}

	response := &GetAgentDraftResponse{Draft: agentDraftView(draft)}

	// Ops committed after the draft was read wait for the next read, so the answer is one revision's.
	for i := range ops {
		op := &ops[i]
		if op.Revision > draft.Revision {
			break
		}

		response.Ops = append(response.Ops, &AgentDraftOp{Revision: op.Revision, At: op.At.Format(time.RFC3339Nano), BaseRevision: op.BaseRevision, OpId: op.OpID, Author: op.Author, Kind: string(op.Kind), Variant: op.Variant, Key: op.Key, Value: op.Value, From: op.From,
			ExternalMcp: externalMCPView(op.ExternalMCP), Splices: spliceViews(op.Splices)})
	}

	for _, presence := range presences {
		response.Presence = append(response.Presence, &AgentDraftPresence{Client: presence.Client, Username: presence.Username, Variant: presence.Variant, Place: presence.Place})
	}

	proposals, err := s.sessions.AgentDraftProposals(ctx, request.Id)
	if err != nil {
		return nil, fmt.Errorf("web agent draft proposals: %w", err)
	}

	for i := range proposals {
		response.Proposals = append(response.Proposals, agentDraftProposalView(&proposals[i]))
	}

	messages, err := s.sessions.AgentDraftMessages(ctx, draft.TestChat)
	if err != nil {
		return nil, fmt.Errorf("web agent draft messages: %w", err)
	}

	for _, message := range messages {
		response.Messages = append(response.Messages, &AgentDraftMessage{InputId: message.InputID, Labels: message.Labels, Live: message.Live, ExternalMcp: externalMCPView(message.ExternalMCP), NotRun: problemViews(message.NotRun)})
	}

	return response, nil
}

func (s *Server) proposeDraftChanges(ctx context.Context, username string, request *ProposeDraftChangesRequest) (*AgentDraftProposal, error) {
	draft, err := s.sessions.LoadAgentDraft(ctx, request.Id)
	if err != nil {
		return nil, agentDraftError("ProposeDraftChanges", err)
	}

	ask := backend.AgentDraftAsk{Request: request.Request, Answer: request.Answer, Variant: request.Variant}

	if draft.TestChat != "" {
		history, err := s.history(ctx, &HistoryRequest{Id: draft.TestChat})
		if err != nil {
			return nil, err
		}

		for _, message := range history.Messages {
			ask.Transcript += fmt.Sprintf("## %s\n%s\n\n", message.Role, message.Text)
		}
	}

	proposal, err := s.sessions.ProposeAgentDraftChanges(ctx, request.Id, username, &ask)
	if err != nil {
		return nil, agentDraftError("ProposeDraftChanges", err)
	}

	return agentDraftProposalView(proposal), nil
}

// applyAgentDraftOp commits request's op as username's and moves the caller's cursor past every op
// committed since its base, in order. A file or setting that does not parse yet stores nothing and answers
// with its line. The save state and the test chat are the server's, set only by saving and the test chat.
func (s *Server) applyAgentDraftOp(ctx context.Context, username string, request *ApplyAgentDraftOpRequest) (*ApplyAgentDraftOpResponse, error) {
	in := request.GetOp()
	if slices.Contains([]backend.AgentDraftOpKind{backend.AgentDraftSetSaveState, backend.AgentDraftSetTestChat}, backend.AgentDraftOpKind(in.GetKind())) {
		return nil, agentDraftError("ApplyAgentDraftOp", &backend.AgentDraftOpError{Problem: "Saving and the test chat make that change themselves."})
	}

	op := backend.AgentDraftOp{BaseRevision: in.GetBaseRevision(), OpID: in.GetOpId(), Author: username, Kind: backend.AgentDraftOpKind(in.GetKind()), Variant: in.GetVariant(), Key: in.GetKey(), Value: in.GetValue(), From: in.GetFrom(), FromFile: in.GetFromFile(),
		ExternalMCP: backend.AgentDraftExternalMCP{On: in.GetExternalMcp().GetOn(), Caller: in.GetExternalMcp().GetCaller(), Metadata: in.GetExternalMcp().GetMetadata()}}

	for _, splice := range in.GetSplices() {
		op.Splices = append(op.Splices, backend.AgentDraftSplice{At: int(splice.At), Remove: int(splice.Remove), Insert: splice.Insert})
	}

	revision, err := s.sessions.ApplyAgentDraftOp(ctx, request.Id, &op)
	if errNotValid, ok := errors.AsType[*backend.AgentDraftNotValidError](err); ok {
		return &ApplyAgentDraftOpResponse{InvalidLine: int32(errNotValid.Line)}, nil
	}

	if err != nil {
		return nil, agentDraftError("ApplyAgentDraftOp", err)
	}

	committed, err := s.sessions.AgentDraftOps(ctx, request.Id, op.BaseRevision)
	if err != nil {
		return nil, fmt.Errorf("web agent draft ops: %w", err)
	}

	cursor, variant := int(request.Cursor), op.Variant

	for i := range committed {
		switch later := &committed[i]; {
		case later.Revision > revision || later.Variant != variant:
		case later.Kind == backend.AgentDraftRenameVariant:
			variant = later.Value
		case later.Kind == backend.AgentDraftEditBody:
			for _, splice := range later.Splices {
				cursor -= min(splice.Remove, max(0, cursor-splice.At))
				if splice.At <= cursor {
					cursor += utf8.RuneCountInString(splice.Insert)
				}
			}
		}
	}

	return &ApplyAgentDraftOpResponse{Revision: revision, Cursor: int32(cursor)}, nil
}

func (s *Server) setAgentDraftPresence(ctx context.Context, username string, request *SetAgentDraftPresenceRequest) (*SetAgentDraftPresenceResponse, error) {
	presence := request.GetPresence()
	if err := s.sessions.SetAgentDraftPresence(ctx, request.Id, backend.AgentDraftPresence{Client: presence.GetClient(), Username: username, Variant: presence.GetVariant(), Place: presence.GetPlace()}); err != nil {
		return nil, fmt.Errorf("web agent draft presence: %w", err)
	}

	return &SetAgentDraftPresenceResponse{}, nil
}

func (s *Server) sendAgentDraft(ctx context.Context, send *backend.AgentDraftSend) (*DraftPromptResponse, error) {
	_, principal, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}

	send.Principal = principal
	response := &DraftPromptResponse{}

	var problems []backend.AgentDraftProblem

	response.TestChat, problems, err = s.sessions.SendAgentDraft(ctx, s.backend, send)
	if errRefused, ok := errors.AsType[*backend.AgentDraftRefusedError](err); ok {
		response.Refused, response.Restart, problems, err = errRefused.Problem, errRefused.Restart, errRefused.Problems, nil
	}

	if err != nil {
		return nil, agentDraftError("DraftPrompt", err)
	}

	response.Problems = problemViews(problems)

	return response, nil
}

func (s *Server) draftLayers(ctx context.Context, request *DraftLayersRequest) (*DraftLayersResponse, error) {
	layers, err := s.sessions.AgentDraftLayers(ctx, request.Id, request.Variant, request.Destination)
	if err != nil {
		return nil, agentDraftError("DraftLayers", err)
	}

	response := &DraftLayersResponse{HiddenBy: layers.HiddenBy, Replaces: layers.Replaces, Recreated: layers.Recreated, Next: layers.Next, Branch: layers.Branch, Unpushable: layers.Unpushable}
	for _, layer := range layers.Layers {
		response.Layers = append(response.Layers, &AgentLayer{Layer: layer.Layer, Ref: layer.Ref, File: layer.File})
	}

	return response, nil
}

func (s *Server) saveDraft(ctx context.Context, username string, request *SaveDraftRequest) (*SaveDraftResponse, error) {
	state, err := s.sessions.SaveAgentDraft(ctx, s.backend, &backend.AgentDraftSave{ID: request.Id, Variant: request.Variant, Destination: request.Destination, Author: username,
		Reviewed: request.Reviewed, ReviewedFile: request.ReviewedFile, PullRequest: request.PullRequest})

	errSave, refused := errors.AsType[*backend.AgentDraftSaveError](err)
	if !refused && err != nil {
		return nil, agentDraftError("SaveDraft", err)
	}

	if !refused {
		return &SaveDraftResponse{SaveState: string(state)}, nil
	}

	response := &SaveDraftResponse{Refused: errSave.Problem, Problems: problemViews(errSave.Problems)}

	if review := errSave.Review; review != nil {
		response.Review = &AgentDraftReview{Revision: review.Revision, Base: review.Base, Live: review.Live, Draft: review.Draft, Splices: spliceViews(review.Splices)}
		for _, setting := range review.Settings {
			response.Review.Settings = append(response.Review.Settings, &AgentDraftReviewSetting{Key: setting.Key, Base: setting.Base, Live: setting.Live, Draft: setting.Draft, Theirs: setting.Theirs, Both: setting.Both})
		}
	}

	return response, nil
}

func (s *Server) stopWaiting(ctx context.Context, username string, request *StopWaitingRequest) (*SaveDraftResponse, error) {
	state, err := s.sessions.StopAgentDraftWaiting(ctx, request.Id, username, backend.AgentDraftWaitStopped)
	if err != nil {
		return nil, agentDraftError("StopWaiting", err)
	}

	return &SaveDraftResponse{SaveState: string(state)}, nil
}

func agentSettings(request *AgentSettingsRequest) *AgentSettingsResponse {
	response := &AgentSettingsResponse{}

	settings := harness.AgentSettingsFor(harness.AgentMode(request.Mode))
	for i := range settings {
		setting := &settings[i]
		response.Settings = append(response.Settings, &AgentSettingHelp{Key: setting.Key, Group: setting.Group, Label: setting.Label, Explanation: setting.Explanation, DefaultText: setting.Default,
			Allowed: setting.Allowed, Control: string(setting.Control), Choices: setting.Choices, Required: setting.Required, Common: setting.Common})
	}

	return response
}

func agentDraftView(draft *backend.AgentDraft) *AgentDraft {
	view := &AgentDraft{Id: draft.ID, Revision: draft.Revision, Name: draft.Name, Mode: string(draft.Mode), TestChat: draft.TestChat, SaveState: string(draft.SaveState), ExternalMcp: externalMCPView(draft.ExternalMCP)}

	for _, base := range draft.Bases {
		view.Existing = view.Existing || base != nil
	}

	if pending := draft.Pending; pending != nil {
		view.Pending = &AgentDraftPending{Destination: pending.Destination, Base: pending.Base, Branch: pending.Branch, Commit: pending.Commit, Number: int32(pending.Number), Link: pending.Link, Ended: string(pending.Ended)}
	}

	for _, variant := range draft.Variants {
		shown := &AgentDraftVariant{Name: variant.Name, Body: variant.Body}
		for _, setting := range variant.Settings {
			shown.Settings = append(shown.Settings, &AgentDraftSetting{Key: setting.Key, Value: setting.Value})
		}

		view.Variants = append(view.Variants, shown)
	}

	return view
}

func agentDraftProposalView(proposal *backend.AgentDraftProposal) *AgentDraftProposal {
	view := &AgentDraftProposal{Id: proposal.ID, BaseRevision: proposal.BaseRevision, State: string(proposal.State), Message: proposal.Message}
	for _, change := range proposal.Settings {
		view.Settings = append(view.Settings, &AgentDraftProposedSetting{Variant: change.Variant, Key: change.Key, ValueYaml: change.ValueYAML, Reason: change.Reason})
	}

	for _, body := range proposal.Bodies {
		view.Bodies = append(view.Bodies, &AgentDraftProposedBody{Variant: body.Variant, Body: body.Body, Reason: body.Reason})
	}

	return view
}

func externalMCPView(mcp backend.AgentDraftExternalMCP) *AgentDraftExternalMCP {
	return &AgentDraftExternalMCP{On: mcp.On, Caller: mcp.Caller, Metadata: mcp.Metadata}
}

func problemViews(problems []backend.AgentDraftProblem) []*AgentDraftProblem {
	views := make([]*AgentDraftProblem, 0, len(problems))
	for _, problem := range problems {
		views = append(views, &AgentDraftProblem{Variant: problem.Variant, Key: problem.Key, Sentence: problem.Sentence})
	}

	return views
}

func spliceViews(splices []backend.AgentDraftSplice) []*AgentDraftSplice {
	views := make([]*AgentDraftSplice, 0, len(splices))
	for _, splice := range splices {
		views = append(views, &AgentDraftSplice{At: int32(splice.At), Remove: int32(splice.Remove), Insert: splice.Insert})
	}

	return views
}

// agentDraftError gives a draft method's refusal its status and plain sentence: an unknown draft, agent or
// starter agent is NotFound, and a name, change or agent file the draft cannot take InvalidArgument.
func agentDraftError(method string, err error) error {
	_, name := errors.AsType[*backend.AgentDraftNameError](err)
	_, change := errors.AsType[*backend.AgentDraftOpError](err)
	_, file := errors.AsType[*backend.AgentDraftNotValidError](err)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		err = status.Error(codes.NotFound, "That draft does not exist.")
	case errors.Is(err, fs.ErrNotExist):
		err = status.Error(codes.NotFound, "That agent or starter agent does not exist.")
	case name || change || file:
		err = status.Error(codes.InvalidArgument, err.Error())
	}

	return fmt.Errorf("web %s: %w", method, err)
}
