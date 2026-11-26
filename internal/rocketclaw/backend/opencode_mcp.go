package backend

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
)

const openCodeMCPInstructions = `These tools act on your RocketClaw session's tags and on the Hosted Pages of its RocketClaw space. A subagent acts for the session that started it. Pages belong to the space, not to you: every chat working in that space can read, replace, or delete them, and they stay after your session ends. Only sessions created or forked from a RocketClaw Web space have a space; without one you can still tag your session. navigate_for_me takes the person who sent your latest prompt from the RocketClaw Web to another page of it.`

func (r *Runtime) OpenCodeMCP() http.Handler {
	servers := r.Cfg.Clone().OpenCode
	mcpServers := make(map[string]*mcp.Server, len(servers))

	for name, server := range servers {
		mcpServer := mcp.NewServer(&mcp.Implementation{Name: "rocketclaw", Version: "1.0.0"}, &mcp.ServerOptions{Instructions: openCodeMCPInstructions})

		for _, tool := range r.openCodeTools("", "") {
			tool.Parameters["type"] = "object"

			mcpServer.AddTool(&mcp.Tool{Name: strings.TrimPrefix(tool.Name, "rocketclaw_"), Description: tool.Description, InputSchema: tool.Parameters}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				out, err := r.callOpenCodeTool(ctx, name, server, tool.Name, request)
				if err != nil {
					out = err.Error()
				}

				return &mcp.CallToolResult{IsError: err != nil, Content: []mcp.Content{&mcp.TextContent{Text: out}}}, nil
			})
		}

		mcpServers[name] = mcpServer
	}

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

func (r *Runtime) callOpenCodeTool(ctx context.Context, name string, server config.OpenCodeConfig, tool string, request *mcp.CallToolRequest) (string, error) {
	id, _ := request.Params.Meta["ai.opencode/sessionID"].(string)
	if id == "" {
		return "", errors.New(`this call names no OpenCode session; RocketClaw reads it from _meta["ai.opencode/sessionID"]`)
	}

	var conversation string

	for {
		conversation = "opencode:" + name + ":" + id

		found, err := scanFound("read OpenCode session "+id, r.Sessions.db.QueryRowContext(ctx, `SELECT conversation_id FROM conversations WHERE conversation_id = $1`, conversation).Scan(&conversation))
		if err != nil {
			return "", err
		}

		if found {
			break
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

	var username string

	if tool == navigateToolName {
		senders, err := openCodeSenders(ctx, name, server, id, math.MinInt64, math.MaxInt64)
		if err != nil {
			return "", fmt.Errorf("read who prompted OpenCode session %s: %w", id, err)
		}

		if len(senders) == 0 || senders[0] == "" {
			return "", errors.New("nobody sent this session's latest prompt from the RocketClaw Web, so there is nobody to take anywhere")
		}

		username = senders[0]
	}

	tools := r.openCodeTools(conversation, username)

	result, err := tools[slices.IndexFunc(tools, func(candidate harness.Tool) bool { return candidate.Name == tool })].Call(ctx, request.Params.Arguments, nil)
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimPrefix(tool, "rocketclaw_"), err)
	}

	return result.Output, nil
}

const openCodeBridgeName = "rocketclaw"

func addOpenCodeBridge(ctx context.Context, origin, name string, server config.OpenCodeConfig, directory string) error {
	var bridge struct {
		Config struct {
			Type    string `json:"type"`
			URL     string `json:"url"`
			OAuth   bool   `json:"oauth"`
			Headers struct {
				Authorization string `json:"Authorization"`
			} `json:"headers"`
		} `json:"config"`
	}

	bridge.Config.Type, bridge.Config.URL = "remote", origin+"/opencode/"+url.PathEscape(name)+"/mcp"
	bridge.Config.Headers.Authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:"+server.Password))

	return openCodeCall(ctx, name, server, http.MethodPut, "/api/experimental/mcp/"+openCodeBridgeName+"?"+url.Values{"location[directory]": {directory}}.Encode(), &bridge, nil)
}

func AddOpenCodeSessionBridge(ctx context.Context, cfg *config.LockedConfig, name, session string) error {
	current := cfg.Clone()
	server := current.OpenCode[name]

	var found struct {
		Data openCodeSession `json:"data"`
	}
	if err := openCodeCall(ctx, name, server, http.MethodGet, "/api/session/"+url.PathEscape(session), nil, &found); err != nil {
		return err
	}

	return addOpenCodeBridge(ctx, current.Web.Tsnet.Origin, name, server, found.Data.Location.Directory)
}

func addOpenCodeBridges(ctx context.Context, origin, name string, server config.OpenCodeConfig) {
	type mcpServer struct {
		Name string `json:"name"`
	}

	var locations []openCodeLocation
	if openCodeCall(ctx, name, server, http.MethodGet, "/api/debug/location", nil, &locations) != nil {
		return
	}

	for _, location := range locations {
		var listed struct {
			Data []mcpServer `json:"data"`
		}

		if openCodeCall(ctx, name, server, http.MethodGet, "/api/mcp?"+url.Values{"location[directory]": {location.Directory}}.Encode(), nil, &listed) == nil &&
			!slices.ContainsFunc(listed.Data, func(mcp mcpServer) bool { return mcp.Name == openCodeBridgeName }) {
			_ = addOpenCodeBridge(ctx, origin, name, server, location.Directory)
		}
	}
}

func (r *Runtime) openCodeTools(conversation, username string) []harness.Tool {
	reply := func(tags []string, err error) (harness.ToolResult, error) {
		data, _ := json.Marshal(struct {
			Tags []string `json:"tags"`
		}{tags})

		return harness.TextToolResult(string(data)), err
	}

	return append(pageSpace{sessions: r.Sessions, threads: r.threads, conversation: conversation, noSpace: openCodePageNoSpace}.tools(), navigateTool(r.threads, username),
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
