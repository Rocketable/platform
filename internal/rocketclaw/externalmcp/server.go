// Package externalmcp hosts MCP servers for rocketclaw integrations.
package externalmcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SessionPromptToolName and ExternalMCPPath define the public external MCP surface.
const (
	SessionPromptToolName = "session_prompt"
	ExternalMCPPath       = "/mcp"
)

type sessionPromptInput struct {
	ExternalConversationID string                    `json:"external_conversation_id,omitempty" jsonschema:"external MCP conversation ID; omitted starts a new conversation and returns its ID"`
	Input                  string                    `json:"input" jsonschema:"plain-text input to send to the selected rocketclaw session"`
	Agent                  string                    `json:"agent,omitempty" jsonschema:"agent to send the input to in the external MCP conversation; defaults to main for new conversations"`
	SlackChannel           string                    `json:"slack_channel,omitempty" jsonschema:"optional Slack channel name for the relay message, for example #triage"`
	Metadata               map[string]string         `json:"metadata,omitempty" jsonschema:"metadata for external MCP conversations"`
	Attachments            []SessionPromptAttachment `json:"attachments,omitempty" jsonschema:"optional attachments to send with this turn"`
}

// SessionAttachment carries an attachment across the public session_prompt boundary.
type SessionAttachment struct {
	Name       string `json:"name,omitempty" jsonschema:"display filename for the attachment"`
	MIMEType   string `json:"mime_type,omitempty" jsonschema:"attachment MIME type, for example image/png"`
	DataBase64 string `json:"data_base64" jsonschema:"base64-encoded attachment bytes"`
}

// SessionPromptAttachment carries an externally supplied attachment into the session_prompt tool.
type SessionPromptAttachment = SessionAttachment

// SessionResult is the app-level result for an external MCP session tool call.
type SessionResult struct {
	ExternalConversationID string              `json:"external_conversation_id,omitempty" jsonschema:"external conversation ID for this MCP conversation"`
	Answer                 string              `json:"answer" jsonschema:"plain-text answer from rocketclaw"`
	Attachments            []SessionAttachment `json:"attachments,omitempty" jsonschema:"attachments returned by rocketclaw"`
}

type usernameContextKey struct{}

// Server is an HTTP MCP server.
type Server struct {
	url       string
	closeOnce sync.Once
	closeFn   func(context.Context) error
}

// StartSessionPromptServer starts the persistent external MCP HTTP server.
func StartSessionPromptServer(ctx context.Context, logger *slog.Logger, listenAddr string, users map[string]string, defaultAgent string, sessionPromptHandler func(context.Context, string, string, string, string, map[string]string, []SessionPromptAttachment, string) (SessionResult, error)) (*Server, error) {
	if defaultAgent = strings.TrimSpace(defaultAgent); defaultAgent == "" {
		defaultAgent = "main"
	}

	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "rocketclaw-external-mcp", Version: "1.0.0"}, nil)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: SessionPromptToolName, Description: "Queue blocking input for a selectable rocketclaw session and return the final plain-text reply.", InputSchema: map[string]any{"type": "object", "required": []string{"input"}, "properties": map[string]any{"external_conversation_id": map[string]any{"type": "string", "description": "external MCP conversation ID; omitted starts a new conversation and returns its ID"}, "input": map[string]any{"type": "string", "description": "plain-text input to send to the selected rocketclaw session"}, "agent": map[string]any{"type": "string", "description": "agent to send the input to in the external MCP conversation; defaults to main for new conversations"}, "slack_channel": map[string]any{"type": "string", "description": "optional Slack channel name for the relay message, for example #triage"}, "metadata": map[string]any{"type": "object", "description": "metadata for external MCP conversations", "additionalProperties": map[string]any{"type": "string"}}, "attachments": map[string]any{"type": "array", "description": "optional attachments for this turn", "items": map[string]any{"type": "object", "required": []string{"data_base64"}, "properties": map[string]any{"name": map[string]any{"type": "string", "description": "display filename for the attachment"}, "mime_type": map[string]any{"type": "string", "description": "attachment MIME type, for example image/png"}, "data_base64": map[string]any{"type": "string", "description": "base64-encoded attachment bytes"}}, "additionalProperties": false}}}, "additionalProperties": false}}, func(callCtx context.Context, request *mcp.CallToolRequest, input sessionPromptInput) (*mcp.CallToolResult, SessionResult, error) {
		_ = request

		username, _ := callCtx.Value(usernameContextKey{}).(string)

		agent := strings.TrimSpace(input.Agent)
		if agent == "" && strings.TrimSpace(input.ExternalConversationID) == "" {
			agent = defaultAgent
		}

		reply, err := sessionPromptHandler(callCtx, username, strings.TrimSpace(input.ExternalConversationID), agent, input.Input, input.Metadata, input.Attachments, strings.TrimSpace(input.SlackChannel))
		if err != nil {
			return nil, SessionResult{}, err
		}

		content := []mcp.Content{&mcp.TextContent{Text: reply.Answer}}
		for i := range reply.Attachments {
			attachment := reply.Attachments[i]

			data, err := base64.StdEncoding.DecodeString(attachment.DataBase64)
			if err != nil {
				return nil, SessionResult{}, fmt.Errorf("decode session result attachment %d: %w", i+1, err)
			}

			mimeType := strings.TrimSpace(attachment.MIMEType)
			if parsed, _, err := mime.ParseMediaType(mimeType); err == nil {
				mimeType = parsed
			}

			mimeType = strings.ToLower(mimeType)

			if strings.HasPrefix(mimeType, "image/") {
				content = append(content, &mcp.ImageContent{Data: data, MIMEType: mimeType})
				continue
			}

			name := strings.TrimSpace(attachment.Name)
			if name == "" {
				name = fmt.Sprintf("attachment-%d", i+1)
			}

			content = append(content, &mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: fmt.Sprintf("attachment://%d/%s", i+1, url.PathEscape(name)), MIMEType: mimeType, Blob: data}})
		}

		return &mcp.CallToolResult{Content: content}, reply, nil
	})

	httpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil)

	mux := http.NewServeMux()
	mux.Handle(ExternalMCPPath, withBasicAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { httpHandler.ServeHTTP(w, r) }), users))

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen for external MCP HTTP server: %w", err)
	}

	httpServer := &http.Server{Handler: mux}
	server := &Server{url: "http://" + listener.Addr().String() + ExternalMCPPath, closeFn: httpServer.Shutdown}

	go func() { <-ctx.Done(); _ = server.Close(context.Background()) }()

	go func() {
		err := httpServer.Serve(listener)
		if err != nil && err != http.ErrServerClosed && ctx.Err() == nil {
			logger.Error("external MCP HTTP server stopped", "error", err)
		}
	}()

	return server, nil
}

// URL returns the server base URL.
func (s *Server) URL() string { return s.url }

// Name returns the server identifier used in logs.
func (s *Server) Name() string { return "external_mcp" }

// Stop stops the HTTP server and waits for it to exit.
func (s *Server) Stop(ctx context.Context) error { return s.Close(ctx) }

// Close stops the HTTP server and waits for it to exit.
func (s *Server) Close(ctx context.Context) error {
	var err error

	s.closeOnce.Do(func() {
		err = s.closeFn(ctx)
	})

	return err
}

func withBasicAuth(next http.Handler, users map[string]string) http.Handler {
	if next == nil || users == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()

		want, found := users[username]
		if !ok || !found || want != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="rocketclaw external mcp"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), usernameContextKey{}, strings.TrimSpace(username))))
	})
}
