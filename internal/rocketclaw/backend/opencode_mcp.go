package backend

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
)

// openCodeMCPInstructions tell OpenCode agents the bridge's rules, which no tool description states.
const openCodeMCPInstructions = `These tools act on your RocketClaw session: its tags and the Hosted Pages it publishes. A subagent acts for the session that started it, and subagents share that session's page paths, so they can overwrite each other's files. Pages are deleted when your session, or its space, goes away. Publishing pages needs a RocketClaw space, which only sessions created or forked from the RocketClaw Web have; without one you can still tag your session and read, list, or delete existing pages.`

// OpenCodeMCP serves, at /opencode/{name}/mcp, the MCP tools coding server name offers its OpenCode
// sessions: each call acts on the RocketClaw session of the OpenCode session its _meta names, so an
// agent tags its own session and publishes Hosted Pages that go with it. Callers authenticate as
// that coding server, with its configured password.
func (r *Runtime) OpenCodeMCP() http.Handler {
	servers := r.Cfg.Clone().OpenCode
	mcpServers := make(map[string]*mcp.Server, len(servers))

	for name, server := range servers {
		mcpServer := mcp.NewServer(&mcp.Implementation{Name: "rocketclaw", Version: "1.0.0"}, &mcp.ServerOptions{Instructions: openCodeMCPInstructions})

		// Bridge pages hold their content: the coding server's files are not on this machine.
		for _, tool := range r.openCodeTools("", "") {
			tool.Parameters["type"] = "object"
			delete(tool.Parameters["properties"].(map[string]any), "source")
			description := strings.Replace(tool.Description, "content (text), content_base64 (binary), or source (a workspace file)", "content (text) or content_base64 (binary)", 1)

			mcpServer.AddTool(&mcp.Tool{Name: strings.TrimPrefix(tool.Name, "rocketclaw_"), Description: description, InputSchema: tool.Parameters}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				out, err := r.callOpenCodeTool(ctx, name, server, tool.Name, request)
				if err != nil {
					out = err.Error()
				}

				return &mcp.CallToolResult{IsError: err != nil, Content: []mcp.Content{&mcp.TextContent{Text: out}}}, nil
			})
		}

		mcpServers[name] = mcpServer
	}

	// A 10 MiB page arrives base64-encoded inside JSON-RPC, over go-sdk's 4 MiB default.
	handler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server { return mcpServers[request.PathValue("name")] }, &mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true, MaxRequestBodyBytes: 16 << 20})

	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		server, ok := servers[request.PathValue("name")]
		user, password, _ := request.BasicAuth()

		if !ok || subtle.ConstantTimeCompare([]byte(user+":"+password), []byte("opencode:"+server.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="rocketclaw"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)

			return
		}

		handler.ServeHTTP(w, request)
	})
}

// callOpenCodeTool runs tool for the RocketClaw session of the calling OpenCode session, or of its
// nearest ancestor RocketClaw lists, as a subagent acts for its parent.
func (r *Runtime) callOpenCodeTool(ctx context.Context, name string, server config.OpenCodeConfig, tool string, request *mcp.CallToolRequest) (string, error) {
	id, _ := request.Params.Meta["ai.opencode/sessionID"].(string)
	if id == "" {
		return "", errors.New(`this call names no OpenCode session; RocketClaw reads it from _meta["ai.opencode/sessionID"]`)
	}

	var input struct {
		Source string `json:"source"`
	}
	if json.Unmarshal(request.Params.Arguments, &input) == nil && input.Source != "" {
		return "", errors.New("source is not available here: give the file's content or content_base64")
	}

	var conversation, space string

	for {
		conversation = "opencode:" + name + ":" + id

		err := r.Sessions.db.QueryRowContext(ctx, `SELECT COALESCE(s.name, '') FROM conversations c LEFT JOIN web_spaces s ON s.id = c.space_id WHERE c.conversation_id = $1`, conversation).Scan(&space)
		if err == nil {
			break
		}

		if !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("read OpenCode session %s: %w", id, err)
		}

		var parent struct {
			Data struct {
				ParentID string `json:"parentID"`
			} `json:"data"`
		}
		if err := openCodeCall(ctx, name, server, http.MethodGet, "/api/session/"+url.PathEscape(id), nil, &parent); err != nil {
			return "", fmt.Errorf("find OpenCode session %s: %w", id, err)
		}

		if parent.Data.ParentID == "" {
			return "", fmt.Errorf("RocketClaw does not list OpenCode session %s yet", id)
		}

		id = parent.Data.ParentID
	}

	if space == "" && (tool == "rocketclaw_store" || tool == "rocketclaw_kv_store") {
		return "", errors.New("this session has no RocketClaw space, so it cannot publish pages; only sessions created or forked from the RocketClaw Web have one")
	}

	tools := r.openCodeTools(conversation, space)

	result, err := tools[slices.IndexFunc(tools, func(candidate harness.Tool) bool { return candidate.Name == tool })].Call(ctx, request.Params.Arguments, nil)
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimPrefix(tool, "rocketclaw_"), err)
	}

	return result.Output, nil
}

func (r *Runtime) openCodeTools(conversation, space string) []harness.Tool {
	reply := func(tags []string, err error) (harness.ToolResult, error) {
		data, _ := json.Marshal(struct {
			Tags []string `json:"tags"`
		}{tags}) // Encoding a string slice cannot fail.

		return harness.TextToolResult(string(data)), err
	}

	return append(pageTools(r.Sessions, nil, &harness.CustomShell{}, r.threads, conversation, space, conversation),
		harness.Tool{Name: "get_tags", Description: `Return this session's RocketClaw tags as {"tags":[...]}.`, Parameters: map[string]any{"properties": map[string]any{}}, Call: func(ctx context.Context, _ json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
			return reply(sessionTags(ctx, r.Sessions.db, conversation))
		}},
		harness.Tool{Name: "toggle_tag", Description: `Add tag to this session's RocketClaw tags, or remove it when present, and return them as {"tags":[...]}. Any tag is allowed.`, Parameters: map[string]any{"properties": map[string]any{"tag": map[string]any{"type": "string"}}, "required": []string{"tag"}}, Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
			var input struct {
				Tag string `json:"tag"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return harness.ToolResult{}, fmt.Errorf("parse toggle_tag: %w", err)
			}

			tag := strings.TrimSpace(input.Tag)
			if tag == "" || strings.ContainsRune(tag, 0) {
				return harness.ToolResult{}, errors.New("a tag cannot be blank or contain a null character")
			}

			return reply(r.Sessions.toggleSessionTag(ctx, conversation, tag, []string{tag}))
		}})
}
