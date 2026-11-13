package rpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
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

func (s *Server) getAgentDraft(ctx context.Context, request *GetAgentDraftRequest) (*GetAgentDraftResponse, error) {
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

		view := &AgentDraftOp{Revision: op.Revision, At: op.At.Format(time.RFC3339Nano), BaseRevision: op.BaseRevision, OpId: op.OpID, Author: op.Author, Kind: string(op.Kind), Variant: op.Variant, Key: op.Key, Value: op.Value, From: op.From,
			ExternalMcp: &AgentDraftExternalMCP{On: op.ExternalMCP.On, Caller: op.ExternalMCP.Caller, Metadata: op.ExternalMCP.Metadata}}

		for _, splice := range op.Splices {
			view.Splices = append(view.Splices, &AgentDraftSplice{At: int32(splice.At), Remove: int32(splice.Remove), Insert: splice.Insert})
		}

		response.Ops = append(response.Ops, view)
	}

	for _, presence := range presences {
		response.Presence = append(response.Presence, &AgentDraftPresence{Client: presence.Client, Username: presence.Username, Variant: presence.Variant, Place: presence.Place})
	}

	return response, nil
}

// applyAgentDraftOp commits request's op as username's and moves the caller's cursor past every op
// committed since its base, in order. A file or setting that does not parse yet stores nothing and answers
// with its line.
func (s *Server) applyAgentDraftOp(ctx context.Context, username string, request *ApplyAgentDraftOpRequest) (*ApplyAgentDraftOpResponse, error) {
	in := request.GetOp()
	op := backend.AgentDraftOp{BaseRevision: in.GetBaseRevision(), OpID: in.GetOpId(), Author: username, Kind: backend.AgentDraftOpKind(in.GetKind()), Variant: in.GetVariant(), Key: in.GetKey(), Value: in.GetValue(), From: in.GetFrom(),
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

// agentSettings returns the setting help for the requested mode, in agent-file order.
func agentSettings(request *AgentSettingsRequest) *AgentSettingsResponse {
	response := &AgentSettingsResponse{}

	settings := harness.AgentSettings()
	for i := range settings {
		if setting := &settings[i]; slices.Contains(setting.Modes, harness.AgentMode(request.Mode)) {
			response.Settings = append(response.Settings, &AgentSettingHelp{Key: setting.Key, Group: setting.Group, Label: setting.Label, Explanation: setting.Explanation, DefaultText: setting.Default,
				Allowed: setting.Allowed, Control: string(setting.Control), Choices: setting.Choices, Required: setting.Required, Common: setting.Common})
		}
	}

	return response
}

func agentDraftView(draft *backend.AgentDraft) *AgentDraft {
	view := &AgentDraft{Id: draft.ID, Revision: draft.Revision, Name: draft.Name, Mode: string(draft.Mode), TestChat: draft.TestChat, SaveState: string(draft.SaveState),
		ExternalMcp: &AgentDraftExternalMCP{On: draft.ExternalMCP.On, Caller: draft.ExternalMCP.Caller, Metadata: draft.ExternalMCP.Metadata}}

	for _, base := range draft.Bases {
		view.Existing = view.Existing || base != nil
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
