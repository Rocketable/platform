package rpc

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) pageCall(ctx context.Context, request any) (any, error) {
	switch request := request.(type) {
	case *PageRequest:
		return s.getPage(ctx, request)
	case *ListPagesRequest:
		return s.listPages(ctx, request)
	case *TabsRequest:
		return s.tabs(ctx, request)
	case *ReportTypingRequest:
		return s.reportTyping(ctx, request)
	case *FindPullRequestsRequest:
		return s.findPullRequests(ctx, request)
	case *MessageTreeRequest:
		return s.messageTree(ctx, request)
	case *SummarizeBranchRequest:
		return s.summarizeBranch(ctx, request)
	default:
		return &ProtocolResponse{ProtoSha256: protoSHA256}, nil
	}
}

func (s *Server) getPage(ctx context.Context, request *PageRequest) (*Page, error) {
	_, limit, err := s.spaceLimit(ctx)
	if err != nil {
		return nil, err
	}

	agents, err := backend.LoadRuntimeAgents(s.cfg, s.cfg.Clone().RuntimeDirName())
	if err != nil {
		return nil, fmt.Errorf("load web page agents: %w", err)
	}

	kind, name := backend.PageFile, request.Path
	if request.Data {
		kind = backend.PageData
	}

	if name == "" || strings.HasSuffix(name, "/") {
		name += "index.html"
	}

	page, found, err := s.sessions.Page(ctx, request.Agent, kind, name)
	if err != nil {
		return nil, fmt.Errorf("read web page: %w", err)
	}

	agent, ok := agents.Items[request.Agent]
	if !found || !ok && page.Conversation == "" || !limit.AllowsSpaceName(cmp.Or(agent.Space, page.Space)) {
		return nil, status.Errorf(codes.NotFound, "page %q not found", name)
	}

	return &Page{Space: cmp.Or(agent.Space, page.Space), ContentType: page.ContentType, Body: page.Body}, nil
}

func (s *Server) listPages(ctx context.Context, request *ListPagesRequest) (*ListPagesResponse, error) {
	_, limit, err := s.spaceLimit(ctx)
	if err != nil {
		return nil, err
	}

	agents, err := backend.LoadRuntimeAgents(s.cfg, s.cfg.Clone().RuntimeDirName())
	if err != nil {
		return nil, fmt.Errorf("load web page agents: %w", err)
	}

	pages, err := s.sessions.Pages(ctx, request.Agent)
	if err != nil {
		return nil, fmt.Errorf("list web pages: %w", err)
	}

	response := &ListPagesResponse{}

	for i := range pages {
		page := &pages[i]
		if request.Path != "" && page.Name != request.Path {
			continue
		}

		if agent, ok := agents.Items[page.Agent]; (ok || page.Space != "") && page.Kind == backend.PageFile && page.ContentType == "text/html" && limit.AllowsSpaceName(cmp.Or(agent.Space, page.Space)) {
			entry := &PageEntry{Agent: page.Agent, Space: cmp.Or(agent.Space, page.Space), Path: page.Name}
			if request.Path != "" {
				entry.UpdatedAt = new(page.UpdatedAt.Format(time.RFC3339Nano))
			}

			response.Pages = append(response.Pages, entry)
		}
	}

	return response, nil
}
