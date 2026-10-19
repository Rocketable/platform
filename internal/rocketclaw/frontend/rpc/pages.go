package rpc

import (
	"context"
	"fmt"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// pageCall answers GetPage and ListPages by their request, and Protocol, which negotiates the schema, not a browser principal.
func (s *Server) pageCall(ctx context.Context, request any) (any, error) {
	switch request := request.(type) {
	case *PageRequest:
		return s.getPage(ctx, request)
	case *ListPagesRequest:
		return s.listPages(ctx)
	case *TabsRequest:
		return s.tabs(ctx, request)
	default:
		return &ProtocolResponse{ProtoSha256: protoSHA256}, nil
	}
}

func (s *Server) getPage(ctx context.Context, request *PageRequest) (*Page, error) {
	if _, _, err := s.principal(ctx); err != nil {
		return nil, err
	}

	agents, _, err := backend.LoadRuntimeDefinitions(s.cfg, s.cfg.Clone().RuntimeDirName())
	if err != nil {
		return nil, fmt.Errorf("load web page agents: %w", err)
	}

	agent, ok := agents.Items[request.Agent]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "agent %q not found", request.Agent)
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

	if !found {
		return nil, status.Errorf(codes.NotFound, "page %q not found", name)
	}

	return &Page{Space: agent.Space, ContentType: page.ContentType, Body: page.Body}, nil
}

func (s *Server) listPages(ctx context.Context) (*ListPagesResponse, error) {
	if _, _, err := s.principal(ctx); err != nil {
		return nil, err
	}

	agents, _, err := backend.LoadRuntimeDefinitions(s.cfg, s.cfg.Clone().RuntimeDirName())
	if err != nil {
		return nil, fmt.Errorf("load web page agents: %w", err)
	}

	pages, err := s.sessions.Pages(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list web pages: %w", err)
	}

	response := &ListPagesResponse{}

	for _, page := range pages {
		if agent, ok := agents.Items[page.Agent]; ok && page.Kind == backend.PageFile && page.ContentType == "text/html" {
			response.Pages = append(response.Pages, &PageEntry{Agent: page.Agent, Space: agent.Space, Path: page.Name})
		}
	}

	return response, nil
}
