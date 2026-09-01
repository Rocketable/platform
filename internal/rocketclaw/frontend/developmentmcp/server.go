// Package developmentmcp hosts the Development MCP HTTP server.
package developmentmcp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

const (
	developmentMCPPath             = "/mcp"
	listOverlayToolName            = "rocketclaw_development_list_overlay"
	readContextFromOverlayToolName = "rocketclaw_development_read_context_from_overlay"
	lintToolName                   = "rocketclaw_development_lint"
	runTurnToolName                = "rocketclaw_development_run_turn"
	reloadToolName                 = "rocketclaw_development_reload"
	restartToolName                = "rocketclaw_development_restart"
)

type readContextFromOverlayInput struct {
	Overlay string `json:"overlay"`
}

type overlayContext struct {
	BaseOverlay string        `json:"base_overlay,omitempty"`
	Files       []overlayFile `json:"files"`
}

type lintInput struct {
	Context overlayContext `json:"context"`
}

type runTurnInput struct {
	Context        overlayContext `json:"context"`
	Agent          string         `json:"agent"`
	Prompt         string         `json:"prompt"`
	ConversationID string         `json:"conversation_id"`
}

type runTurnOutput struct {
	ConversationID string `json:"conversation_id"`
	Thinking       string `json:"thinking"`
	Answer         string `json:"answer"`
}

type reasonInput struct {
	Reason  string          `json:"reason"`
	Context json.RawMessage `json:"context,omitempty"`
}

type lintFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

type overlayFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Server is an HTTP MCP server.
type Server struct {
	url       string
	closeOnce sync.Once
	closeFn   func(context.Context) error
}

// Start starts the Development MCP HTTP server.
func Start(ctx context.Context, logger *slog.Logger, listenAddr string, users map[string]string, overlaySpecs []string, readOverlayContext func(spec string) (protocol.OverlayContext, error), lint func(baseOverlay string, files []protocol.OverlayFile) (protocol.LintResult, error), runTurn func(ctx context.Context, baseOverlay string, files []protocol.OverlayFile, agent, prompt, conversationID string) (thinking, answer string, err error), reload, restart func(reason string) (string, error)) (*Server, error) {
	if len(users) == 0 {
		return nil, errors.New("development MCP users are required")
	}

	reasonOnlyToolSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reason": map[string]any{"type": "string"},
		},
	}

	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "rocketclaw-development-mcp", Version: "1.0.0"}, nil)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: listOverlayToolName, Description: "List configured git overlay spec strings in config order."}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, []string, error) {
		return nil, overlaySpecs, nil
	})
	mcp.AddTool(mcpServer, &mcp.Tool{Name: readContextFromOverlayToolName, Description: "Return a context printout for one configured overlay spec."}, func(_ context.Context, _ *mcp.CallToolRequest, input readContextFromOverlayInput) (*mcp.CallToolResult, overlayContext, error) {
		got, err := readOverlayContext(input.Overlay)
		if err != nil {
			return nil, overlayContext{}, err
		}

		out := overlayContext{BaseOverlay: got.BaseOverlay, Files: make([]overlayFile, len(got.Files))}
		for i, file := range got.Files {
			out.Files[i] = overlayFile{Path: file.Path, Content: file.Content}
		}

		return nil, out, nil
	})
	mcp.AddTool(mcpServer, &mcp.Tool{Name: lintToolName, Description: "Lint the overlay represented by the given context."}, func(_ context.Context, _ *mcp.CallToolRequest, input lintInput) (*mcp.CallToolResult, []lintFinding, error) {
		files := make([]protocol.OverlayFile, len(input.Context.Files))
		for i, file := range input.Context.Files {
			files[i] = protocol.OverlayFile{Path: file.Path, Content: file.Content}
		}

		result, err := lint(input.Context.BaseOverlay, files)
		if err != nil {
			return nil, nil, err
		}

		findings := make([]lintFinding, len(result.Findings))
		for i, finding := range result.Findings {
			findings[i] = lintFinding{Code: finding.Code, Severity: finding.Severity, Path: finding.Path, Message: finding.Message}
		}

		return nil, findings, nil
	})
	mcp.AddTool(mcpServer, &mcp.Tool{Name: runTurnToolName, Description: "Run one chat turn against the overlay represented by the given context."}, func(ctx context.Context, _ *mcp.CallToolRequest, input runTurnInput) (*mcp.CallToolResult, runTurnOutput, error) {
		conversationID := input.ConversationID
		if conversationID == "" {
			conversationID = "devmcp-" + rand.Text()
		}

		files := make([]protocol.OverlayFile, len(input.Context.Files))
		for i, file := range input.Context.Files {
			files[i] = protocol.OverlayFile{Path: file.Path, Content: file.Content}
		}

		thinking, answer, err := runTurn(ctx, input.Context.BaseOverlay, files, input.Agent, input.Prompt, conversationID)
		if err != nil {
			return nil, runTurnOutput{}, err
		}

		return nil, runTurnOutput{ConversationID: conversationID, Thinking: thinking, Answer: answer}, nil
	})
	mcp.AddTool(mcpServer, &mcp.Tool{Name: reloadToolName, Description: "Reload published overlay files the live daemon can hot-load.", InputSchema: reasonOnlyToolSchema}, reasonToolHandler("reload", reload))
	mcp.AddTool(mcpServer, &mcp.Tool{Name: restartToolName, Description: "Restart for overlay-list or runtime-config changes. Not interchangeable with reload.", InputSchema: reasonOnlyToolSchema}, reasonToolHandler("restart", restart))

	httpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true},
	)

	mux := http.NewServeMux()
	mux.Handle(developmentMCPPath, withBasicAuth(httpHandler, users))

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen for development MCP HTTP server: %w", err)
	}

	httpServer := &http.Server{Handler: mux}
	server := &Server{url: "http://" + listener.Addr().String() + developmentMCPPath, closeFn: httpServer.Shutdown}

	go func() { <-ctx.Done(); _ = server.Close(context.Background()) }()

	go func() {
		err := httpServer.Serve(listener)
		if err != nil && err != http.ErrServerClosed && ctx.Err() == nil {
			logger.Error("development MCP HTTP server stopped", "error", err)
		}
	}()

	return server, nil
}

// URL returns the server base URL.
func (s *Server) URL() string { return s.url }

// Close stops the HTTP server and waits for it to exit.
func (s *Server) Close(ctx context.Context) error {
	var err error

	s.closeOnce.Do(func() {
		err = s.closeFn(ctx)
	})

	return err
}

func reasonToolHandler(name string, run func(string) (string, error)) func(context.Context, *mcp.CallToolRequest, reasonInput) (*mcp.CallToolResult, string, error) {
	return func(_ context.Context, _ *mcp.CallToolRequest, input reasonInput) (*mcp.CallToolResult, string, error) {
		if len(input.Context) > 0 {
			return nil, "", fmt.Errorf("%s does not take context", name)
		}

		out, err := run(input.Reason)

		return nil, out, err
	}
}

func withBasicAuth(next http.Handler, users map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()

		want, found := users[username]
		if !ok || !found || want != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="rocketclaw development mcp"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(w, r)
	})
}
