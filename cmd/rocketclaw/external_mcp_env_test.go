package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/externalmcp"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

type externalMCPAssembler struct {
	started chan externalMCPStarted
}

type externalMCPStarted struct {
	runtime  *backend.Runtime
	endpoint string
}

func (externalMCPAssembler) Gate(context.Context, *config.Config, *slog.Logger) (func() error, error) {
	return func() error { return nil }, nil
}

func (a externalMCPAssembler) Assemble(rt *backend.Runtime) (<-chan struct{}, []func(context.Context) error, error) {
	events := rt.Subscribe(rt.RunCtx)
	go func() {
		for event := range events {
			event.Acknowledgement <- nil
		}
	}()

	listener, err := net.Listen("tcp", rt.Cfg.Clone().MCPExternal.ListenAddr)
	if err != nil {
		return nil, nil, err
	}

	server := startExternalMCPServer(rt.RunCtx, listener, nil, &mcpAgentIndex{names: []string{"planner"}}, rt.Sessions, rt, rt.Log)
	a.started <- externalMCPStarted{runtime: rt, endpoint: "http://" + listener.Addr().String() + externalmcp.ExternalMCPPath}

	return make(chan struct{}), []func(context.Context) error{server.Close}, nil
}

func TestExternalMCPMetadataReachesWebChatTurns(t *testing.T) {
	workspace := t.TempDir()

	var (
		mu      sync.Mutex
		outputs []string
	)

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []struct {
				Type    string          `json:"type"`
				Output  string          `json:"output"`
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}

		w.Header().Set("Content-Type", "application/json")

		mu.Lock()
		defer mu.Unlock()

		var instructions string
		if len(body.Input) > 0 && body.Input[0].Role == "developer" {
			_ = json.Unmarshal(body.Input[0].Content, &instructions)
		}

		if !strings.Contains(instructions, "Planner prompt") || strings.Contains(instructions, "Intruder prompt") {
			t.Errorf("the turn did not run as planner: %s", instructions)
		}

		response := `{"id":"resp_done","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_done","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"done","annotations":[]}]}]}`
		if last := body.Input[len(body.Input)-1]; last.Type == "function_call_output" {
			outputs = append(outputs, last.Output)
		} else {
			code := "def main():\n    return bash(command=" + strconv.Quote(`printf '%s|%s|%s|%s|%s' "${ROCKETCLAW_METADATA_INTERCOM_CONVERSATION_ID-unset}" "${ROCKETCLAW_CONVERSATION_ID-unset}" "${ROCKETCLAW_METADATA_EXTERNAL_CONVERSATION_ID-unset}" "${ROCKETCLAW_METADATA_ROCKETCLAW_RAW_TEXT-unset}" "${ROCKETCLAW_METADATA_WEB_MESSAGE_ID-unset}"`) + ")\n"
			args, err := json.Marshal(map[string]string{"code": code})
			if err != nil {
				t.Error(err)
			}

			response = fmt.Sprintf(`{"id":"resp_call","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"fc_call","type":"function_call","status":"completed","call_id":"call_%d","name":"execute","arguments":%q}]}`, len(outputs), string(args))
		}

		if _, err := w.Write([]byte(response)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(model.Close)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	cfg := &config.Config{
		Workspace:   workspace,
		DatabaseURL: dsn,
		OpenAI:      config.OpenAIConfig{APIKey: "test", APIBaseURL: model.URL},
		Slack:       config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "@", Agents: []string{"managed"}, AllowedUserIDs: []string{"U1"}}}},
		MCPExternal: config.MCPExternalConfig{Enabled: true, ListenAddr: "127.0.0.1:0"},
	}
	assembler := externalMCPAssembler{started: make(chan externalMCPStarted, 1)}

	ctx, cancel := context.WithCancel(t.Context())
	errRun := make(chan error, 1)

	go func() {
		errRun <- backend.Run(ctx, cfg, testLogger(), new(slog.LevelVar), assembler, sandbox.None{})
	}()

	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-errRun)
	})

	started := <-assembler.started

	for name, prompt := range map[string]string{"planner": "Planner prompt", "intruder": "Intruder prompt"} {
		require.NoError(t, os.WriteFile(filepath.Join(workspace, ".rocketclaw", "agents", name+".md"), []byte("---\ndescription: Agent\nmodel: gpt-6.1-sol\npermission:\n  bash: allow\n---\n"+prompt+"\n"), 0o600))
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: started.endpoint, HTTPClient: http.DefaultClient, DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: externalmcp.SessionPromptToolName, Arguments: map[string]any{
		"external_conversation_id": "ticket-1", "agent": "planner", "input": "reply to the customer",
		"metadata": map[string]string{"intercom_conversation_id": "ic-42", "rocketclaw_agent": "intruder"},
	}})
	require.NoError(t, err)
	require.False(t, result.IsError, "%s", result.Content[0].(*mcp.TextContent).Text)

	binding, ok, err := started.runtime.Sessions.ExternalMCPSession("ticket-1")
	require.NoError(t, err)
	require.True(t, ok)

	followUp := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "reply again", true)
	followUp.ConversationID, followUp.Metadata = binding.ManagedConversationID, map[string]string{"web_message_id": "web-1"}
	done := followUp.EnableResponseWait()
	require.NoError(t, started.runtime.RunTurn(t.Context(), followUp))

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the Web chat turn")
	}

	mu.Lock()
	defer mu.Unlock()

	require.Equal(t, []string{"ic-42|" + binding.PrivateConversationID + "|ticket-1|unset|unset", "ic-42|" + binding.ManagedConversationID + "|ticket-1|unset|unset"}, outputs)
}
