package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/discordvoice"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/externalmcp"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutboundLoopDeliversChannelsInParallelAndPreservesPerChannelOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	bus := events.New()
	defer bus.Close()

	slackFirstRelease := make(chan struct{})
	slackFirstSeen := make(chan struct{}, 1)
	discordSeen := make(chan struct{}, 2)
	order := make(map[string][]int)

	var mu sync.Mutex

	record := func(target string, sequence int) {
		mu.Lock()
		defer mu.Unlock()

		order[target] = append(order[target], sequence)
	}

	slack := outboundOK(func(deliveryCtx context.Context, msg *events.OutboundMessage) {
		_ = deliveryCtx

		record("slack", msg.Sequence)

		if msg.Sequence == 1 {
			slackFirstSeen <- struct{}{}

			<-slackFirstRelease
		}
	})
	discord := outboundOK(func(deliveryCtx context.Context, msg *events.OutboundMessage) {
		_ = deliveryCtx

		record("discord", msg.Sequence)

		discordSeen <- struct{}{}
	})

	done := make(chan error, 1)

	go func() {
		done <- outboundLoop(ctx, bus, slack, discord, discardOutboundSend, testLogger())
	}()

	first := testOutboundMessage(1, false)
	second := testOutboundMessage(2, true)

	require.NoError(t, bus.PublishOutbound(context.Background(), first))
	require.NoError(t, bus.PublishOutbound(context.Background(), second))

	require.Eventually(t, func() bool {
		return len(slackFirstSeen) == 1 && len(discordSeen) == 2
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, []int{1, 2}, recordedOrder(order, &mu, "discord"))
	assert.Equal(t, []int{1}, recordedOrder(order, &mu, "slack"))

	assertDeliveryBlocked(t, first, 100*time.Millisecond)
	assertDeliveryBlocked(t, second, 100*time.Millisecond)

	waitCtx, stopWait := context.WithTimeout(context.Background(), 100*time.Millisecond)
	errWait := bus.WaitOutboundIdle(waitCtx)

	stopWait()
	require.ErrorContains(t, errWait, "wait for outbound idle")

	close(slackFirstRelease)
	require.NoError(t, second.WaitDelivered(context.Background()))
	require.NoError(t, bus.WaitOutboundIdle(context.Background()))
	assert.Equal(t, []int{1, 2}, recordedOrder(order, &mu, "slack"))
	assert.Equal(t, []int{1, 2}, recordedOrder(order, &mu, "discord"))

	cancel()
	require.NoError(t, <-done)
}

func TestOutboundLoopPropagatesDeliveryErrorsToWaitDelivered(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	bus := events.New()
	defer bus.Close()

	discord := func(context.Context, *events.OutboundMessage) error {
		return assert.AnError
	}

	done := make(chan error, 1)

	go func() {
		done <- outboundLoop(ctx, bus, discardOutboundSend, discord, discardOutboundSend, testLogger())
	}()

	msg := testOutboundMessage(9, true)
	msg.Targets = []events.OutputTarget{events.OutputTargetDiscord}
	require.NoError(t, bus.PublishOutbound(context.Background(), msg))
	err := msg.WaitDelivered(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, assert.AnError.Error())

	cancel()
	require.NoError(t, <-done)
}

func TestOutboundLoopTreatsInterruptedDiscordPlaybackAsDelivered(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	bus := events.New()
	defer bus.Close()

	discord := func(context.Context, *events.OutboundMessage) error {
		return discordvoice.ErrPlaybackInterrupted
	}

	done := make(chan error, 1)

	go func() {
		done <- outboundLoop(ctx, bus, discardOutboundSend, discord, discardOutboundSend, testLogger())
	}()

	msg := testOutboundMessage(10, true)
	msg.Targets = []events.OutputTarget{events.OutputTargetDiscord}
	require.NoError(t, bus.PublishOutbound(context.Background(), msg))
	require.NoError(t, msg.WaitDelivered(context.Background()))
	require.NoError(t, bus.WaitOutboundIdle(context.Background()))

	cancel()
	require.NoError(t, <-done)
}

func TestOutboundLoopRoutesDiscordVoiceThinkingToDiscord(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	bus := events.New()
	defer bus.Close()

	slackSeen := make(chan *events.OutboundMessage, 1)
	discordSeen := make(chan *events.OutboundMessage, 1)
	done := make(chan error, 1)

	go func() {
		done <- outboundLoop(ctx, bus, outboundOK(func(deliveryCtx context.Context, msg *events.OutboundMessage) {
			_ = deliveryCtx

			slackSeen <- msg
		}), outboundOK(func(deliveryCtx context.Context, msg *events.OutboundMessage) {
			_ = deliveryCtx

			discordSeen <- msg
		}), discardOutboundSend, testLogger())
	}()

	msg := events.NewMainOutboundMessage(events.SourceDiscordVoice, "", events.OutputTargetSlackMain)
	msg.SlackThinking = "first thought"
	msg.TurnID = "turn-1"
	require.NoError(t, bus.PublishOutbound(context.Background(), msg))
	assert.Equal(t, "first thought", (<-slackSeen).SlackThinking)
	assert.Equal(t, "first thought", (<-discordSeen).SlackThinking)
	cancel()
	require.NoError(t, <-done)
}

func TestOutboundLoopRoutesBrowserVoiceResponsesToWebUI(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	bus := events.New()
	defer bus.Close()

	webSeen := make(chan *events.OutboundMessage, 1)
	done := make(chan error, 1)

	go func() {
		done <- outboundLoop(ctx, bus, discardOutboundSend, discardOutboundSend, outboundOK(func(deliveryCtx context.Context, msg *events.OutboundMessage) {
			_ = deliveryCtx

			webSeen <- msg
		}), testLogger())
	}()

	msg := events.NewMainOutboundMessage(events.SourceWebVoice, "hello", events.OutputTargetWebUI)
	msg.WebSessionID = "browser-session-1"
	msg.Complete = true
	require.NoError(t, bus.PublishOutbound(context.Background(), msg))

	seen := <-webSeen
	assert.Equal(t, "hello", seen.Text)
	assert.Equal(t, "browser-session-1", seen.WebSessionID)

	cancel()
	require.NoError(t, <-done)
}

func TestOutboundLoopWebAndShutdownEdges(t *testing.T) {
	t.Run("web delivery error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		bus := events.New()
		defer bus.Close()

		errWeb := errors.New("web unavailable")
		done := make(chan error, 1)

		go func() {
			done <- outboundLoop(ctx, bus, discardOutboundSend, discardOutboundSend, func(context.Context, *events.OutboundMessage) error {
				return errWeb
			}, testLogger())
		}()

		msg := events.NewMainOutboundMessage(events.SourceWebVoice, "hello", events.OutputTargetWebUI)
		require.NoError(t, bus.PublishOutbound(context.Background(), msg))
		err := msg.WaitDelivered(context.Background())
		require.Error(t, err)
		require.ErrorContains(t, err, "send web UI response")
		require.ErrorContains(t, err, errWeb.Error())

		cancel()
		require.NoError(t, <-done)
	})

	t.Run("closed bus", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		bus := events.New()
		done := make(chan error, 1)

		go func() {
			done <- outboundLoop(ctx, bus, discardOutboundSend, discardOutboundSend, discardOutboundSend, testLogger())
		}()

		bus.Close()
		require.NoError(t, <-done)
	})

	t.Run("deadline context", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()

		bus := events.New()
		defer bus.Close()

		err := outboundLoop(ctx, bus, discardOutboundSend, discardOutboundSend, discardOutboundSend, testLogger())
		require.Error(t, err)
		require.ErrorContains(t, err, "outbound loop canceled")
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestOutboundLoopMarksMessagesWithoutTargetsDelivered(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	bus := events.New()
	defer bus.Close()

	done := make(chan error, 1)

	go func() {
		done <- outboundLoop(ctx, bus, discardOutboundSend, discardOutboundSend, discardOutboundSend, testLogger())
	}()

	msg := events.NewMainOutboundMessage(events.SourceSystem, "internal")
	msg.Targets = nil
	require.NoError(t, bus.PublishOutbound(context.Background(), msg))
	require.NoError(t, msg.WaitDelivered(context.Background()))
	require.NoError(t, bus.WaitOutboundIdle(context.Background()))

	cancel()
	require.NoError(t, <-done)
}

func TestRunStartsRuntimeAndStopsOnCanceledContext(t *testing.T) {
	workspace := t.TempDir()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	certPath := filepath.Join(workspace, ".rocketclaw", "web-ui.crt")

	cfg := &config.Config{Workspace: workspace}
	cfg.MCPExternal.Enabled = true
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	cfg.WebUI.Enabled = true
	cfg.WebUI.ListenAddr = "127.0.0.1:0"

	done := make(chan error, 1)

	go func() {
		done <- Run(ctx, cfg, filepath.Join(workspace, "rocketclaw.json"), testLogger())
	}()

	require.Eventually(t, func() bool {
		_, err := os.Stat(certPath)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	for _, name := range []string{
		"AGENTS.md",
		filepath.Join(".rocketclaw", "state.sqlite3"),
		filepath.Join(".rocketclaw", "agents", "main.md"),
		filepath.Join(".rocketclaw", "web-ui.crt"),
		filepath.Join(".rocketclaw", "web-ui.key"),
	} {
		_, err := os.Stat(filepath.Join(workspace, name))
		require.NoError(t, err, name)
	}
}

func TestRunReturnsErrRestartRequestedAfterCronRestartTool(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "main.md"), []byte("---\ndescription: Main\nmode: primary\nmodel: openai/gpt-5.5\npermission:\n  rocketclaw:\n    '*': allow\n---\nPrompt\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "cron"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "cron", "restart.md"), []byte("---\nschedule: \"2000-01-01T00:00:00Z\"\nagent: main\n---\nRestart now\n"), 0o644))

	var (
		requestMu sync.Mutex
		requests  int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		requestMu.Lock()
		requests++
		request := requests
		requestMu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		switch request {
		case 1:
			writeAppRawRunFunctionCall(t, w, "resp_1", "call_1", "rocketclaw_restart", map[string]string{"reason": "cron changed runtime config"})
		case 2:
			writeAppRawRunFunctionCall(t, w, "resp_2", "call_2", harnessbridge.RawRunExposedToolName, map[string]string{"payload": ""})
		case 3:
			_, err := w.Write([]byte(`{"id":"resp_3","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"","annotations":[]}]}]}`))
			assert.NoError(t, err)
		default:
			t.Fatalf("unexpected response request %d", request)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}
	err := Run(ctx, cfg, filepath.Join(workspace, "rocketclaw.json"), testLogger())
	require.ErrorIs(t, err, ErrRestartRequested)

	requestMu.Lock()
	defer requestMu.Unlock()

	assert.Equal(t, 3, requests)
}

func TestStartExternalMCPServerRoutesSelectedAgentDirectly(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	bus := events.New()
	defer bus.Close()

	mainInboundSeen := make(chan *events.InboundMessage, 1)

	go func() {
		for inbound := range bus.Inbound(ctx) {
			mainInboundSeen <- inbound
			return
		}
	}()

	selectedAgent := make(chan string, 1)
	selectedConversationID := make(chan string, 1)

	var relayText string

	threadTarget := make(chan *events.InboundMessage, 1)
	submitAgent := func(submitCtx context.Context, agent, conversationID string, inbound *events.InboundMessage) error {
		_ = submitCtx

		selectedAgent <- agent

		selectedConversationID <- conversationID

		threadTarget <- inbound

		inbound.CompleteResponse("planner answer", nil)

		return nil
	}
	slackRelay := func(relayCtx context.Context, text string, attachments []events.OutboundAttachment, replyTarget *events.SlackReplyTarget, channel string) (*events.SlackReplyTarget, error) {
		_ = relayCtx
		_ = attachments
		_ = replyTarget
		_ = channel

		relayText = text

		return &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "123.456", ThreadTS: ""}, nil
	}

	cfg := new(config.Config)
	cfg.ThreadAgents = config.ThreadAgents{":z:": {Agent: "planner", PreSeed: true}, ":factory:": {Agent: "planner", PreSeed: true}}
	cfg.MCPExternal.Enabled = true
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	store := newAppTestSessionService(t, t.TempDir())

	server, err := startExternalMCPServer(ctx, cfg, slackRelay, inertExternalMCPCleanup, nil, []string{"planner"}, "planner", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	reply, err := callSessionPromptForAgent(ctx, server.URL(), "", "", "", "hello", map[string]string{"ticket": "123", "owner": "alice"})
	require.NoError(t, err)
	assert.Equal(t, "planner answer", reply.answer)
	assert.NotEmpty(t, reply.externalConversationID)
	assert.Equal(t, "planner", <-selectedAgent)

	internalConversationID := <-selectedConversationID
	assert.Contains(t, internalConversationID, "external_mcp:planner:")
	assert.Equal(t, ":factory: hello", relayText)

	inbound := <-threadTarget
	assert.Equal(t, "hello", inbound.Text)
	assert.Equal(t, map[string]string{"ticket": "123", "owner": "alice"}, inbound.Metadata)
	replyTarget := inbound.SlackReply
	require.NotNil(t, replyTarget)
	assert.Equal(t, replyTarget.MessageTS, replyTarget.ThreadTS)

	state, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, harnessbridge.ThreadState{Agent: "planner", SeededFromResponse: internalConversationID}, state.Threads[harnessbridge.SlackThreadConversationID(replyTarget.ChannelID, replyTarget.ThreadTS)])
	assert.Equal(t, harnessbridge.ExternalMCPSessionState{Agent: "planner", ConversationID: internalConversationID}, state.ExternalMCPSessions[reply.externalConversationID])

	select {
	case inbound := <-mainInboundSeen:
		t.Fatalf("selected external MCP agent was also published to main inbound: %+v", inbound)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestStartExternalMCPServerRoutesAttachments(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	threadTarget := make(chan *events.InboundMessage, 1)
	submitAgent := func(submitCtx context.Context, agent, conversationID string, inbound *events.InboundMessage) error {
		_ = submitCtx

		assert.Equal(t, "planner", agent)
		assert.Contains(t, conversationID, "external_mcp:planner:")

		threadTarget <- inbound

		inbound.CompleteResponse("planner answer", nil)

		return nil
	}

	cfg := new(config.Config)
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	store := newAppTestSessionService(t, t.TempDir())

	server, err := startExternalMCPServer(ctx, cfg, inertExternalMCPRelay, inertExternalMCPCleanup, nil, []string{"planner"}, "planner", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	reply, err := callMCPTool(ctx, server.URL(), "", "", map[string]any{
		"agent": "planner",
		"input": "look at this",
		"attachments": []map[string]any{{
			"name":        "scorecard.png",
			"mime_type":   "image/png",
			"data_base64": base64.StdEncoding.EncodeToString([]byte("png-data")),
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "planner answer", reply.answer)

	inbound := <-threadTarget
	require.Len(t, inbound.Attachments, 1)
	assert.True(t, inbound.HadAttachments)
	assert.Equal(t, "scorecard.png", inbound.Attachments[0].Name)
	assert.Equal(t, "image/png", inbound.Attachments[0].MIMEType)
	assert.Equal(t, []byte("png-data"), inbound.Attachments[0].Data)
}

func TestStartExternalMCPServerRejectsMalformedAttachmentBase64(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cfg := new(config.Config)
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	store := newAppTestSessionService(t, t.TempDir())
	submitAgent := func(context.Context, string, string, *events.InboundMessage) error {
		t.Fatal("submitAgent called for malformed attachment")
		return nil
	}

	server, err := startExternalMCPServer(ctx, cfg, inertExternalMCPRelay, inertExternalMCPCleanup, nil, []string{"planner"}, "planner", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	_, err = callMCPTool(ctx, server.URL(), "", "", map[string]any{
		"agent": "planner",
		"input": "look at this",
		"attachments": []map[string]any{{
			"name":        "bad.png",
			"data_base64": "%%%",
		}},
	})
	require.ErrorContains(t, err, "decode external MCP attachment 1")
}

func TestStartExternalMCPServerRoutesExistingExternalConversationIDToSeededSlackThread(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	const (
		externalConversationID = "ticket-123"
		conversationID         = "external_mcp:planner:abc"
		threadTS               = "999.000"
	)

	store := newAppTestSessionService(t, t.TempDir())
	require.NoError(t, store.UpsertExternalMCPSession(externalConversationID, harnessbridge.ExternalMCPSessionState{Agent: "planner", ConversationID: conversationID}))

	threadKey := harnessbridge.SlackThreadConversationID("D123", threadTS)
	require.NoError(t, store.UpsertThread(threadKey, "planner"))
	require.NoError(t, store.MarkThreadSeeded(threadKey, conversationID))

	threadTarget := make(chan *events.InboundMessage, 1)
	selectedConversationID := make(chan string, 1)
	submitAgent := func(submitCtx context.Context, agent, conversationID string, inbound *events.InboundMessage) error {
		_ = submitCtx

		assert.Equal(t, "planner", agent)

		selectedConversationID <- conversationID

		threadTarget <- inbound

		inbound.CompleteResponse("follow-up answer", nil)

		return nil
	}

	cfg := new(config.Config)
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	server, err := startExternalMCPServer(ctx, cfg, inertExternalMCPRelay, inertExternalMCPCleanup, nil, []string{"planner"}, "planner", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	reply, err := callMCPTool(ctx, server.URL(), "", "", map[string]any{"external_conversation_id": externalConversationID, "input": "follow up", "metadata": map[string]string{"ticket": "123"}})
	require.NoError(t, err)
	assert.Equal(t, "follow-up answer", reply.answer)
	assert.Equal(t, externalConversationID, reply.externalConversationID)
	assert.Equal(t, conversationID, <-selectedConversationID)

	inbound := <-threadTarget
	assert.Equal(t, "follow up", inbound.Text)
	assert.Equal(t, map[string]string{"ticket": "123"}, inbound.Metadata)
	require.NotNil(t, inbound.SlackReply)
	assert.Equal(t, events.SlackReplyTarget{ChannelID: "D123", MessageTS: threadTS, ThreadTS: threadTS}, *inbound.SlackReply)
}

func TestStartExternalMCPServerCreatesConversationForUnknownExternalConversationID(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	const externalConversationID = "caller-conversation-1"

	store := newAppTestSessionService(t, t.TempDir())
	selectedConversationID := make(chan string, 1)
	submitAgent := func(submitCtx context.Context, agent, conversationID string, inbound *events.InboundMessage) error {
		_ = submitCtx

		assert.Equal(t, "main", agent)

		selectedConversationID <- conversationID

		inbound.CompleteResponse("created answer", nil)

		return nil
	}

	cfg := new(config.Config)
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	server, err := startExternalMCPServer(ctx, cfg, inertExternalMCPRelay, inertExternalMCPCleanup, nil, []string{"main"}, "main", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	reply, err := callMCPTool(ctx, server.URL(), "", "", map[string]any{"external_conversation_id": externalConversationID, "input": "start"})
	require.NoError(t, err)
	assert.Equal(t, "created answer", reply.answer)
	assert.Equal(t, externalConversationID, reply.externalConversationID)

	conversationID := <-selectedConversationID
	state, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, harnessbridge.ExternalMCPSessionState{Agent: "main", ConversationID: conversationID}, state.ExternalMCPSessions[externalConversationID])
}

func TestExternalMCPExistingExternalConversationIDRunsAgentAndRepliesInSeededSlackThread(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	workspace := t.TempDir()
	writeAppTestAgent(t, workspace, "planner", "---\ndescription: Planner\nmode: primary\nmodel: openai/gpt-5.5\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	answers := make(chan string, 2)
	answers <- "first answer"

	answers <- "second answer"

	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q,"annotations":[]}]}]}`, <-answers)
	}))
	t.Cleanup(openai.Close)

	bus := events.New()
	defer bus.Close()

	slackSeen := make(chan *events.OutboundMessage, 2)
	outboundDone := make(chan error, 1)

	go func() {
		outboundDone <- outboundLoop(ctx, bus, outboundOK(func(deliveryCtx context.Context, msg *events.OutboundMessage) {
			_ = deliveryCtx

			if msg.Complete && msg.Text != "" {
				slackSeen <- msg
			}
		}), discardOutboundSend, discardOutboundSend, testLogger())
	}()

	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: openai.URL}}
	cfg.ThreadAgents = config.ThreadAgents{":factory:": {Agent: "planner", PreSeed: true}}
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	rocketcodeSessions := newAppTestSessionService(t, workspace)
	threadBridges := newThreadBridgeManager(bus, rocketcodeSessions, testLogger(), func(bridgeConfig bridgeConfig) directBridge {
		return harnessbridge.NewConversation(cfg, bus, &harnessbridge.Config{ConversationID: bridgeConfig.ConversationID, Agent: bridgeConfig.Agent, ConsumeSharedInbound: false, OutputTargets: bridgeConfig.OutputTargets, SessionService: rocketcodeSessions}, testLogger())
	})

	defer func() { require.NoError(t, threadBridges.Stop(context.Background())) }()

	server, err := startExternalMCPServer(ctx, cfg, func(relayCtx context.Context, text string, attachments []events.OutboundAttachment, replyTarget *events.SlackReplyTarget, channel string) (*events.SlackReplyTarget, error) {
		_ = relayCtx
		_ = text
		_ = attachments
		_ = channel

		if replyTarget != nil {
			return &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333", ThreadTS: "123.456"}, nil
		}

		return &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "123.456", ThreadTS: ""}, nil
	}, inertExternalMCPCleanup, nil, []string{"planner"}, "planner", rocketcodeSessions, threadBridges.SubmitExternalMCP, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	first, err := callSessionPromptForAgent(ctx, server.URL(), "", "", "", "first", map[string]string{"ticket": "123"})
	require.NoError(t, err)
	assert.Equal(t, "first answer", first.answer)

	firstSlack := <-slackSeen
	require.NotNil(t, firstSlack.SlackReply)
	assert.Equal(t, events.SlackReplyTarget{ChannelID: "D123", MessageTS: "123.456", ThreadTS: "123.456"}, *firstSlack.SlackReply)

	second, err := callMCPTool(ctx, server.URL(), "", "", map[string]any{"external_conversation_id": first.externalConversationID, "input": "second", "metadata": map[string]string{"ticket": "456"}})
	require.NoError(t, err)
	assert.Equal(t, "second answer", second.answer)
	assert.Equal(t, first.externalConversationID, second.externalConversationID)

	secondSlack := <-slackSeen
	require.NotNil(t, secondSlack.SlackReply)
	assert.Equal(t, "second answer", secondSlack.Text)
	assert.Equal(t, events.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333", ThreadTS: "123.456"}, *secondSlack.SlackReply)

	cancel()
	require.NoError(t, <-outboundDone)
}

func TestStartExternalMCPServerRoutesDefaultAgentToIsolatedSession(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	bus := events.New()
	defer bus.Close()

	var relayText string

	selectedAgent := make(chan string, 1)
	selectedConversationID := make(chan string, 1)
	threadTarget := make(chan *events.InboundMessage, 1)

	submitAgent := func(submitCtx context.Context, agent, conversationID string, inbound *events.InboundMessage) error {
		_ = submitCtx

		selectedAgent <- agent

		selectedConversationID <- conversationID

		threadTarget <- inbound

		inbound.CompleteResponse("main answer", nil)

		return nil
	}

	cfg := new(config.Config)
	cfg.MCPExternal.Enabled = true
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	store := newAppTestSessionService(t, t.TempDir())

	server, err := startExternalMCPServer(ctx, cfg, func(relayCtx context.Context, text string, attachments []events.OutboundAttachment, replyTarget *events.SlackReplyTarget, channel string) (*events.SlackReplyTarget, error) {
		_ = relayCtx
		_ = attachments
		_ = replyTarget
		_ = channel

		relayText = text

		return &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "123.456", ThreadTS: ""}, nil
	}, inertExternalMCPCleanup, nil, []string{"main"}, "main", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	reply, err := callSessionPromptForAgent(ctx, server.URL(), "", "", "", "hello", map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, "main answer", reply.answer)
	assert.NotEmpty(t, reply.externalConversationID)
	assert.Equal(t, "main", <-selectedAgent)
	assert.Contains(t, <-selectedConversationID, "external_mcp:main:")
	assert.Equal(t, "hello", relayText)

	inbound := <-threadTarget
	require.NotNil(t, inbound.SlackReply)
	assert.Equal(t, inbound.SlackReply.MessageTS, inbound.SlackReply.ThreadTS)

	state, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, "main", state.ExternalMCPSessions[reply.externalConversationID].Agent)
}

func TestStartExternalMCPServerRelaysPromptToRequestedSlackChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var (
		relayChannel     string
		relayAttachments []events.OutboundAttachment
	)

	threadTarget := make(chan *events.InboundMessage, 1)
	submitAgent := func(submitCtx context.Context, agent, conversationID string, inbound *events.InboundMessage) error {
		_ = submitCtx

		assert.Equal(t, "main", agent)
		assert.Contains(t, conversationID, "external_mcp:main:")

		threadTarget <- inbound

		inbound.CompleteResponse("main answer", nil)

		return nil
	}

	cfg := new(config.Config)
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	store := newAppTestSessionService(t, t.TempDir())

	server, err := startExternalMCPServer(ctx, cfg, func(relayCtx context.Context, text string, attachments []events.OutboundAttachment, replyTarget *events.SlackReplyTarget, channel string) (*events.SlackReplyTarget, error) {
		_ = relayCtx
		_ = text
		_ = replyTarget

		relayChannel = channel
		relayAttachments = events.CloneOutboundAttachments(attachments)

		return &events.SlackReplyTarget{ChannelID: channel, MessageTS: "123.456", ThreadTS: ""}, nil
	}, inertExternalMCPCleanup, nil, []string{"main"}, "main", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	reply, err := callMCPTool(ctx, server.URL(), "", "", map[string]any{"input": "hello", "slack_channel": "#triage", "attachments": []map[string]string{{"name": "red.png", "mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString([]byte("png"))}}})
	require.NoError(t, err)
	assert.Equal(t, "main answer", reply.answer)
	assert.Equal(t, "#triage", relayChannel)
	assert.Equal(t, []events.OutboundAttachment{{Name: "red.png", MIMEType: "image/png", Data: []byte("png")}}, relayAttachments)

	inbound := <-threadTarget
	require.NotNil(t, inbound.SlackReply)
	assert.Equal(t, "#triage", inbound.SlackReply.ChannelID)
	assert.Equal(t, inbound.SlackReply.MessageTS, inbound.SlackReply.ThreadTS)
	assert.Equal(t, []events.InboundAttachment{{Name: "red.png", MIMEType: "image/png", Data: []byte("png")}}, inbound.Attachments)
}

func TestStartExternalMCPServerCleansExternalMCPRelayWhenThreadAliasFails(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var cleaned []*events.SlackReplyTarget

	cleanup := func(cleanupCtx context.Context, replyTarget *events.SlackReplyTarget) {
		_ = cleanupCtx

		cleaned = append(cleaned, cloneAppTestSlackReplyTarget(replyTarget))
	}

	submitAgent := func(context.Context, string, string, *events.InboundMessage) error {
		t.Fatal("submitAgent called after thread alias failure")

		return nil
	}

	cfg := new(config.Config)
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	store := newAppTestSessionService(t, t.TempDir())
	server, err := startExternalMCPServer(ctx, cfg, func(context.Context, string, []events.OutboundAttachment, *events.SlackReplyTarget, string) (*events.SlackReplyTarget, error) {
		return &events.SlackReplyTarget{MessageTS: "123.456"}, nil
	}, cleanup, nil, []string{"main"}, "main", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	_, err = callMCPTool(ctx, server.URL(), "", "", map[string]any{"input": "hello"})
	require.ErrorContains(t, err, "persist external MCP Slack thread alias")
	require.Len(t, cleaned, 1)
	assert.Equal(t, &events.SlackReplyTarget{MessageTS: "123.456", ThreadTS: "123.456"}, cleaned[0])
}

func TestStartExternalMCPServerCleansExternalMCPRelayWhenSubmitFails(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var cleaned []*events.SlackReplyTarget

	cleanup := func(cleanupCtx context.Context, replyTarget *events.SlackReplyTarget) {
		_ = cleanupCtx

		cleaned = append(cleaned, cloneAppTestSlackReplyTarget(replyTarget))
	}

	submitAgent := func(context.Context, string, string, *events.InboundMessage) error {
		return errors.New("submit failed")
	}

	cfg := new(config.Config)
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	store := newAppTestSessionService(t, t.TempDir())
	server, err := startExternalMCPServer(ctx, cfg, func(context.Context, string, []events.OutboundAttachment, *events.SlackReplyTarget, string) (*events.SlackReplyTarget, error) {
		return &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "123.456"}, nil
	}, cleanup, nil, []string{"main"}, "main", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	_, err = callMCPTool(ctx, server.URL(), "", "", map[string]any{"input": "hello"})
	require.ErrorContains(t, err, "submit external MCP input to agent")
	require.Len(t, cleaned, 1)
	assert.Equal(t, &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "123.456", ThreadTS: "123.456"}, cleaned[0])
}

func TestStartExternalMCPServerCleansExistingExternalMCPRelayWhenSubmitFails(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	const (
		externalConversationID = "caller-conversation-1"
		conversationID         = "external_mcp:planner:abc"
	)

	var cleaned []*events.SlackReplyTarget

	cleanup := func(cleanupCtx context.Context, replyTarget *events.SlackReplyTarget) {
		_ = cleanupCtx

		cleaned = append(cleaned, cloneAppTestSlackReplyTarget(replyTarget))
	}

	submitAgent := func(context.Context, string, string, *events.InboundMessage) error {
		return errors.New("submit failed")
	}

	store := newAppTestSessionService(t, t.TempDir())
	require.NoError(t, store.UpsertExternalMCPSession(externalConversationID, harnessbridge.ExternalMCPSessionState{Agent: "planner", ConversationID: conversationID}))

	threadKey := harnessbridge.SlackThreadConversationID("D123", "111.222")
	require.NoError(t, store.UpsertThread(threadKey, "planner"))
	require.NoError(t, store.MarkThreadSeeded(threadKey, conversationID))

	cfg := new(config.Config)
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	server, err := startExternalMCPServer(ctx, cfg, func(relayCtx context.Context, text string, attachments []events.OutboundAttachment, replyTarget *events.SlackReplyTarget, channel string) (*events.SlackReplyTarget, error) {
		_ = relayCtx
		_ = text
		_ = attachments
		_ = channel

		require.NotNil(t, replyTarget)

		return &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333", ThreadTS: "111.222"}, nil
	}, cleanup, nil, []string{"planner"}, "planner", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	_, err = callMCPTool(ctx, server.URL(), "", "", map[string]any{"external_conversation_id": externalConversationID, "input": "hello"})
	require.ErrorContains(t, err, "submit external MCP input to agent")
	require.Len(t, cleaned, 1)
	assert.Equal(t, &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333", ThreadTS: "111.222"}, cleaned[0])
}

func TestStartExternalMCPServerRejectsInvalidExternalMCPConversationState(t *testing.T) {
	tests := []struct {
		name           string
		agents         []string
		requestedAgent string
		session        harnessbridge.ExternalMCPSessionState
		wantErr        string
	}{
		{
			name:           "agent mismatch",
			agents:         []string{"main", "planner"},
			requestedAgent: "main",
			session:        harnessbridge.ExternalMCPSessionState{Agent: "planner", ConversationID: "external_mcp:planner:abc"},
			wantErr:        `belongs to agent "planner", not "main"`,
		},
		{
			name:    "incomplete state",
			agents:  []string{"planner"},
			session: harnessbridge.ExternalMCPSessionState{Agent: "planner"},
			wantErr: "has incomplete persisted state",
		},
		{
			name:    "unexposed persisted agent",
			agents:  []string{"main"},
			session: harnessbridge.ExternalMCPSessionState{Agent: "planner", ConversationID: "external_mcp:planner:abc"},
			wantErr: `external MCP agent "planner" is not exposed`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			const externalConversationID = "caller-conversation-1"

			store := newAppTestSessionService(t, t.TempDir())
			require.NoError(t, store.UpsertExternalMCPSession(externalConversationID, tt.session))

			cfg := new(config.Config)
			cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
			server, err := startExternalMCPServer(ctx, cfg, inertExternalMCPRelay, inertExternalMCPCleanup, nil, tt.agents, "main", store, func(context.Context, string, string, *events.InboundMessage) error {
				t.Fatal("submitAgent called for invalid external MCP session")

				return nil
			}, testLogger())
			require.NoError(t, err)

			defer func() { require.NoError(t, server.Close(context.Background())) }()

			_, err = callMCPTool(ctx, server.URL(), "", "", map[string]any{"external_conversation_id": externalConversationID, "agent": tt.requestedAgent, "input": "hello"})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestStartExternalMCPServerRejectsUnexposedRequestedAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cfg := new(config.Config)
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	store := newAppTestSessionService(t, t.TempDir())
	server, err := startExternalMCPServer(ctx, cfg, inertExternalMCPRelay, inertExternalMCPCleanup, nil, []string{"main"}, "main", store, func(context.Context, string, string, *events.InboundMessage) error {
		t.Fatal("submitAgent called for unexposed external MCP agent")

		return nil
	}, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	_, err = callMCPTool(ctx, server.URL(), "", "", map[string]any{"agent": "planner", "input": "hello"})
	require.ErrorContains(t, err, `external MCP agent "planner" is not exposed`)
}

func TestSubmitExternalMCPInputReportsErrors(t *testing.T) {
	errSubmit := errors.New("thread bridge unavailable")
	_, err := submitExternalMCPInput(t.Context(), func(context.Context, string, string, *events.InboundMessage) error {
		return errSubmit
	}, "planner", "external_mcp:planner:123", "hello", nil, nil, nil, "ticket-123")
	require.ErrorIs(t, err, errSubmit)
	require.ErrorContains(t, err, `submit external MCP input to agent "planner"`)

	errResponse := errors.New("assistant failed")
	replyTarget := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}
	attachments := []events.InboundAttachment{{Name: "scorecard.png", MIMEType: "image/png", Data: []byte("png")}}
	metadata := map[string]string{"ticket": "123"}
	_, err = submitExternalMCPInput(t.Context(), func(_ context.Context, agent, conversationID string, inbound *events.InboundMessage) error {
		assert.Equal(t, "planner", agent)
		assert.Equal(t, "external_mcp:planner:123", conversationID)
		assert.Equal(t, "hello", inbound.Text)
		assert.Equal(t, metadata, inbound.Metadata)
		assert.Equal(t, attachments, inbound.Attachments)
		assert.True(t, inbound.HadAttachments)
		assert.Equal(t, replyTarget, inbound.SlackReply)
		inbound.CompleteResponse("", errResponse)

		return nil
	}, "planner", "external_mcp:planner:123", "hello", metadata, attachments, replyTarget, "ticket-123")
	require.ErrorIs(t, err, errResponse)
	require.ErrorContains(t, err, "wait for external MCP reply")

	ctx, cancel := context.WithCancel(t.Context())
	_, err = submitExternalMCPInput(ctx, func(context.Context, string, string, *events.InboundMessage) error {
		cancel()

		return nil
	}, "planner", "external_mcp:planner:123", "hello", nil, nil, nil, "ticket-123")
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "wait for external MCP reply")
}

func TestRetrySlackDeliveryReturnsCanceledError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	errSend := errors.New("slack unavailable")
	err := retrySlackDelivery(ctx, testLogger(), "test delivery", func(context.Context) error {
		return errSend
	})

	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	assert.ErrorContains(t, err, "slack delivery canceled while retrying test delivery after slack unavailable")
}

func TestRetrySlackDeliverySucceedsAfterRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		attempts := 0
		errSend := errors.New("slack unavailable")
		err := retrySlackDelivery(t.Context(), testLogger(), "test delivery", func(context.Context) error {
			attempts++
			if attempts == 1 {
				return errSend
			}

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 2, attempts)
	})
}

func TestRetrySlackDeliveryStopsWhileWaitingToRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		errSend := errors.New("slack unavailable")
		errCh := make(chan error, 1)
		attempts := 0

		go func() {
			errCh <- retrySlackDelivery(ctx, testLogger(), "test delivery", func(context.Context) error {
				attempts++

				return errSend
			})
		}()

		synctest.Wait()
		cancel()

		err := <-errCh
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "slack delivery canceled while retrying test delivery after slack unavailable")
		assert.Equal(t, 1, attempts)
	})
}

func testOutboundMessage(sequence int, complete bool) *events.OutboundMessage {
	msg := events.NewMainOutboundMessage(events.SourceSlack, "hello", events.MainOutputTargets()...)
	msg.TurnID = "turn-1"
	msg.Sequence = sequence
	msg.Complete = complete
	msg.SlackReply = &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: ""}

	return msg
}

func recordedOrder(order map[string][]int, mu *sync.Mutex, target string) []int {
	mu.Lock()
	defer mu.Unlock()

	return append([]int(nil), order[target]...)
}

func assertDeliveryBlocked(t *testing.T, msg *events.OutboundMessage, duration time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	err := msg.WaitDelivered(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func outboundOK(fn func(context.Context, *events.OutboundMessage)) func(context.Context, *events.OutboundMessage) error {
	return func(ctx context.Context, msg *events.OutboundMessage) error {
		fn(ctx, msg)
		return nil
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func inertExternalMCPRelay(context.Context, string, []events.OutboundAttachment, *events.SlackReplyTarget, string) (*events.SlackReplyTarget, error) {
	return nil, nil
}

func inertExternalMCPCleanup(context.Context, *events.SlackReplyTarget) {}

func cloneAppTestSlackReplyTarget(replyTarget *events.SlackReplyTarget) *events.SlackReplyTarget {
	if replyTarget == nil {
		return nil
	}

	return &events.SlackReplyTarget{ChannelID: replyTarget.ChannelID, MessageTS: replyTarget.MessageTS, ThreadTS: replyTarget.ThreadTS}
}

func writeAppTestAgent(t *testing.T, workspace, name, content string) {
	t.Helper()

	dir := filepath.Join(workspace, ".rocketclaw", "agents")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644))
}

func newAppTestSessionService(t *testing.T, workspace string) *harnessbridge.SessionService {
	t.Helper()

	service, err := harnessbridge.NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	return service
}

func writeAppRawRunFunctionCall(t *testing.T, w http.ResponseWriter, responseID, callID, name string, args any) {
	t.Helper()

	argsData, err := json.Marshal(args)
	require.NoError(t, err)

	data, err := json.Marshal(map[string]any{
		"id":         responseID,
		"object":     "response",
		"created_at": 0,
		"status":     "requires_action",
		"model":      "gpt-5.5",
		"output": []map[string]any{{
			"id":        callID,
			"type":      "function_call",
			"call_id":   callID,
			"name":      name,
			"arguments": string(argsData),
			"status":    "completed",
		}},
	})
	require.NoError(t, err)

	_, err = w.Write(data)
	assert.NoError(t, err)
}

type mcpToolReply struct {
	answer                 string
	externalConversationID string
}

func callSessionPromptForAgent(ctx context.Context, endpoint, username, password, agent, input string, metadata map[string]string) (mcpToolReply, error) {
	args := map[string]any{"input": input}
	if agent != "" {
		args["agent"] = agent
	}

	if metadata != nil {
		args["metadata"] = metadata
	}

	return callMCPTool(ctx, endpoint, username, password, args)
}
func callMCPTool(ctx context.Context, endpoint, username, password string, args map[string]any) (mcpToolReply, error) {
	implementation := new(mcp.Implementation)
	implementation.Name = "test-client"
	implementation.Version = "1.0.0"

	client := mcp.NewClient(implementation, nil)
	transport := new(mcp.StreamableClientTransport)
	transport.Endpoint = endpoint
	transport.DisableStandaloneSSE = true
	transport.HTTPClient = new(http.Client)
	transport.HTTPClient.Transport = basicAuthRoundTripper{base: http.DefaultTransport, username: username, password: password}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return mcpToolReply{}, fmt.Errorf("connect MCP client: %w", err)
	}

	defer func() { _ = session.Close() }()

	params := new(mcp.CallToolParams)
	params.Name = externalmcp.SessionPromptToolName
	params.Arguments = args

	result, err := session.CallTool(ctx, params)
	if err != nil {
		return mcpToolReply{}, fmt.Errorf("call %s: %w", externalmcp.SessionPromptToolName, err)
	}

	if len(result.Content) != 1 {
		return mcpToolReply{}, fmt.Errorf("%s content count = %d; want 1", externalmcp.SessionPromptToolName, len(result.Content))
	}

	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return mcpToolReply{}, fmt.Errorf("%s content type = %T; want *mcp.TextContent", externalmcp.SessionPromptToolName, result.Content[0])
	}

	if result.IsError {
		return mcpToolReply{}, errors.New(content.Text)
	}

	var structured struct {
		Answer                 string `json:"answer"`
		ExternalConversationID string `json:"external_conversation_id"`
	}

	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return mcpToolReply{}, fmt.Errorf("marshal structured %s content: %w", externalmcp.SessionPromptToolName, err)
	}

	if err := json.Unmarshal(data, &structured); err != nil {
		return mcpToolReply{}, fmt.Errorf("parse structured %s content: %w", externalmcp.SessionPromptToolName, err)
	}

	return mcpToolReply{answer: structured.Answer, externalConversationID: structured.ExternalConversationID}, nil
}

type basicAuthRoundTripper struct {
	base     http.RoundTripper
	username string
	password string
}

func (r basicAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if r.username != "" || r.password != "" {
		clone.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(r.username+":"+r.password)))
	}

	base := r.base
	if base == nil {
		base = http.DefaultTransport
	}

	resp, err := base.RoundTrip(clone)
	if err != nil {
		return nil, fmt.Errorf("round trip MCP request: %w", err)
	}

	return resp, nil
}
