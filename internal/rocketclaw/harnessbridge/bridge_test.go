package harnessbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/oai"
	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

type bridgeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f bridgeRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRestartToolScopesDescriptionToRuntimeConfig(t *testing.T) {
	tool := restartTool(testNoopRestart, testNoopRestartRecorder)

	assert.Contains(t, tool.Description, "explicitly requested runtime configuration change")
	assert.Contains(t, tool.Description, "rocketclaw.json")
	assert.Contains(t, tool.Description, "agents/")
	assert.Contains(t, tool.Description, "skills/")
	assert.Contains(t, tool.Description, "cron/")
	assert.Contains(t, tool.Description, "reason field")
	assert.Contains(t, tool.Description, "memory, ledger, audit, report")
	assert.Contains(t, tool.Description, "source-code")
	assert.Contains(t, tool.Description, "data-file edits")
	assert.NotContains(t, tool.Description, "file changes")
	assert.Equal(t, []string{"reason"}, tool.Parameters["required"])
}

func TestRestartToolCallsConfiguredRestart(t *testing.T) {
	order := []string{}

	tool := restartTool(func(_ context.Context, reason string) (string, error) {
		order = append(order, "restart:"+reason)
		return "custom restart output", nil
	}, func(context.Context) error {
		order = append(order, "record")

		return nil
	})

	result, err := tool.Call(t.Context(), []byte(`{"reason":"rocketclaw.json changed and runtime config must reload"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"record", "restart:rocketclaw.json changed and runtime config must reload"}, order)
	assert.Equal(t, "custom restart output", result.Output)
}

func TestRestartToolAcceptsEmptyOutputAndPropagatesErrors(t *testing.T) {
	tool := restartTool(testNoopRestart, testNoopRestartRecorder)
	result, err := tool.Call(t.Context(), []byte(`{"reason":"cron changed"}`), nil)
	require.NoError(t, err)
	assert.Empty(t, result.Output)

	_, err = tool.Call(t.Context(), []byte(`{"reason":" "}`), nil)
	require.EqualError(t, err, "reason is required")

	_, err = tool.Call(t.Context(), []byte(`{`), nil)
	require.ErrorContains(t, err, "parse restart request")

	tool = restartTool(testNoopRestart, func(context.Context) error { return assert.AnError })
	_, err = tool.Call(t.Context(), []byte(`{"reason":"cron changed"}`), nil)
	require.ErrorIs(t, err, assert.AnError)

	tool = restartTool(func(context.Context, string) (string, error) { return "", assert.AnError }, testNoopRestartRecorder)
	_, err = tool.Call(t.Context(), []byte(`{"reason":"cron changed"}`), nil)
	assert.ErrorIs(t, err, assert.AnError)
}

func testNoopRestart(context.Context, string) (string, error) { return "", nil }

func testNoopRestartRecorder(context.Context) error { return nil }

func TestNormalizeConfigDefaultsAndCopiesTargets(t *testing.T) {
	defaulted := normalizeConfig(nil)
	assert.Equal(t, events.MainConversationID(), defaulted.ConversationID)
	assert.Equal(t, "main", defaulted.Agent)
	assert.True(t, defaulted.ConsumeSharedInbound)
	assert.Equal(t, events.MainOutputTargets(), defaulted.OutputTargets)

	targets := []events.OutputTarget{events.OutputTargetSlackMain}
	configured := normalizeConfig(&Config{ConversationID: "thread", Agent: "helper", OutputTargets: targets})
	targets[0] = events.OutputTargetDiscord

	assert.Equal(t, "thread", configured.ConversationID)
	assert.Equal(t, "helper", configured.Agent)
	assert.False(t, configured.ConsumeSharedInbound)
	assert.Equal(t, []events.OutputTarget{events.OutputTargetSlackMain}, configured.OutputTargets)
}

func TestProcessResponseAndFinalShareTurnID(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), RequestRestart: testNoopRestart, SessionService: nil}
	inbound := events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	result := runResult{turnID: "turn-1", text: "", thinking: "", sequence: 0, sessionEntryID: 0, responseID: "", model: ""}

	var reply rocketcode.ChatResponse

	reply.Kind = rocketcode.ChatResponseAssistantMessage
	reply.Text = "hello back"
	require.NoError(t, bridge.processResponse(context.Background(), inbound, &result, reply))
	partial := readRocketCodeOutbound(t, bus)
	assert.Equal(t, "turn-1", partial.TurnID)
	assert.False(t, partial.Complete)

	var group errgroup.Group

	group.Go(func() error { return bridge.publishFinal(context.Background(), inbound, result, true) })

	final := readRocketCodeOutbound(t, bus)
	assert.Equal(t, "turn-1", final.TurnID)
	assert.True(t, final.Complete)
	final.MarkDelivered(nil)
	require.NoError(t, group.Wait())
}

func TestPublishFinalAttachesMainResponseCheckpoint(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), RequestRestart: testNoopRestart, SessionService: nil}
	inbound := events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	result := runResult{turnID: "turn-1", text: "answer", thinking: "", sequence: 0, sessionEntryID: 7, responseID: "resp-1", model: "gpt-5.4"}

	var group errgroup.Group
	group.Go(func() error { return bridge.publishFinal(context.Background(), inbound, result, true) })

	outbound := readRocketCodeOutbound(t, bus)
	require.NotNil(t, outbound.Checkpoint)
	assert.Equal(t, events.ResponseCheckpoint{ConversationID: "main", SessionEntryID: 7, ResponseID: "resp-1", Model: "gpt-5.4", AssistantText: "answer"}, *outbound.Checkpoint)
	outbound.MarkDelivered(nil)
	require.NoError(t, group.Wait())
}

func TestPublishFinalCarriesMainResponseAttachments(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), RequestRestart: testNoopRestart, SessionService: nil}
	inbound := events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	result := runResult{turnID: "turn-1", text: "", thinking: "", sequence: 0, sessionEntryID: 0, responseID: "", model: "", attachments: []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}}

	var group errgroup.Group
	group.Go(func() error { return bridge.publishFinal(context.Background(), inbound, result, true) })

	outbound := readRocketCodeOutbound(t, bus)
	assert.True(t, outbound.Complete)
	assert.Empty(t, outbound.Text)
	assert.Equal(t, []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}, outbound.Attachments)
	outbound.MarkDelivered(nil)
	require.NoError(t, group.Wait())
}

func TestPublishFinalAttachesCheckpointToInternalizedVerbatimMessage(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.config = Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), RequestRestart: testNoopRestart, SessionService: nil}
	inbound := events.NewMainInboundMessage(events.SourceSystem, events.InboundKindInternalize, "cron", "internal note", false)
	inbound.VerbatimMessage = "cron output"
	inbound.VerbatimAttachments = []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}
	result := runResult{turnID: "turn-1", text: "internalized", thinking: "", sequence: 0, sessionEntryID: 7, responseID: "resp-1", model: "gpt-5.4"}

	var group errgroup.Group
	group.Go(func() error { return bridge.publishFinal(context.Background(), inbound, result, false) })

	outbound := readRocketCodeOutbound(t, bus)
	assert.Equal(t, "cron output", outbound.Text)
	assert.Equal(t, []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}, outbound.Attachments)
	assert.True(t, outbound.Complete)
	require.NotNil(t, outbound.Checkpoint)
	assert.Equal(t, events.ResponseCheckpoint{ConversationID: "main", SessionEntryID: 7, ResponseID: "resp-1", Model: "gpt-5.4", AssistantText: "cron output"}, *outbound.Checkpoint)
	outbound.MarkDelivered(nil)
	require.NoError(t, group.Wait())
}

func TestPublishFinalReportsVerbatimOutboundErrors(t *testing.T) {
	t.Run("publish", func(t *testing.T) {
		bus := events.New()
		bus.Close()

		bridge := new(Bridge)
		bridge.bus = bus
		bridge.config = Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), RequestRestart: testNoopRestart, SessionService: nil}
		inbound := events.NewMainInboundMessage(events.SourceSystem, events.InboundKindInternalize, "cron", "internal note", false)
		inbound.VerbatimMessage = "cron output"
		response := inbound.EnableResponseWait()

		err := bridge.publishFinal(context.Background(), inbound, runResult{turnID: "turn-1"}, false)
		require.ErrorContains(t, err, "publish verbatim outbound message")

		got := <-response
		require.ErrorIs(t, got.Err, events.ErrBusClosed)
		assert.Empty(t, got.Text)
	})

	t.Run("delivery", func(t *testing.T) {
		bus := events.New()
		defer bus.Close()

		bridge := new(Bridge)
		bridge.bus = bus
		bridge.config = Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), RequestRestart: testNoopRestart, SessionService: nil}
		inbound := events.NewMainInboundMessage(events.SourceSystem, events.InboundKindInternalize, "cron", "internal note", false)
		inbound.VerbatimMessage = "cron output"
		response := inbound.EnableResponseWait()

		var group errgroup.Group
		group.Go(func() error {
			return bridge.publishFinal(context.Background(), inbound, runResult{turnID: "turn-1"}, false)
		})

		outbound := readRocketCodeOutbound(t, bus)
		outbound.MarkDelivered(assert.AnError)

		require.ErrorContains(t, group.Wait(), "wait for verbatim outbound delivery")

		got := <-response
		require.ErrorIs(t, got.Err, assert.AnError)
		assert.Empty(t, got.Text)
	})
}

func TestHandleInboundReportsRocketCodeErrorDetail(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll(".rocketclaw/agents", 0o755))
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))

	bus := events.New()
	defer bus.Close()

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: []events.OutputTarget{events.OutputTargetSlackMain}, RequestRestart: testNoopRestart, SessionService: service}, slog.New(slog.DiscardHandler))
	inbound := events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)

	var group errgroup.Group
	group.Go(func() error { return bridge.handleInbound(context.Background(), inbound) })

	outbound := readRocketCodeOutbound(t, bus)
	assert.True(t, outbound.Complete)
	assert.Contains(t, outbound.Text, internalErrorResponse)
	assert.Contains(t, outbound.Text, `missing required default agent "main"`)
	outbound.MarkDelivered(nil)
	require.NoError(t, group.Wait())

	response := <-inbound.EnableResponseWait()
	assert.Equal(t, outbound.Text, response.Text)
	require.NoError(t, response.Err)
}

func TestHandleInboundInternalizeCompletesResponseWithRocketCodeError(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll(".rocketclaw/agents", 0o755))
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))

	bus := events.New()
	defer bus.Close()

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: []events.OutputTarget{events.OutputTargetSlackMain}, RequestRestart: testNoopRestart, SessionService: service}, slog.New(slog.DiscardHandler))
	inbound := events.NewMainInboundMessage(events.SourceSystem, events.InboundKindInternalize, "cron", "internal note", false)
	responseCh := inbound.EnableResponseWait()

	err = bridge.handleInbound(context.Background(), inbound)
	require.ErrorContains(t, err, `missing required default agent "main"`)

	response := <-responseCh
	assert.Empty(t, response.Text)
	require.ErrorContains(t, response.Err, `missing required default agent "main"`)
}

func TestRocketCodeConfigEnablesDiagnosticsForThinkingUpdates(t *testing.T) {
	bridge := new(Bridge)
	cfg := bridge.rocketcodeConfig(t.TempDir(), nil, rocketcode.Tool{Name: attachFilesToolName})

	toolNames := make([]string, 0, len(cfg.CustomTools))
	for i := range cfg.CustomTools {
		toolNames = append(toolNames, cfg.CustomTools[i].Name)
	}

	assert.True(t, cfg.Diagnostics)
	assert.True(t, cfg.ExperimentalStrongerSkills)
	assert.Equal(t, 16, cfg.ParallelToolCalls)
	assert.Equal(t, rocketcode.PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: true, SkillPrompts: true, InputPrompts: false}, cfg.ExpandPromptShellCommands)
	assert.Contains(t, toolNames, scheduleMessageToolName)
	assert.Contains(t, toolNames, resetScheduledMessagesToolName)
	assert.Contains(t, toolNames, attachFilesToolName)
	assert.Equal(t, map[string]string{"A": "B"}, bridge.rocketcodeConfig(t.TempDir(), map[string]string{"A": "B"}).ShellEnv)
}

func TestNewConversationKeepsInjectedSessionService(t *testing.T) {
	service, err := NewSessionService(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	bus := events.New()
	defer bus.Close()

	bridge := NewConversation(new(config.Config), bus, &Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), RequestRestart: testNoopRestart, SessionService: service}, slog.New(slog.DiscardHandler))
	assert.Same(t, service, bridge.config.SessionService)
}

func TestBridgeSubmitReturnsErrorAfterStop(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	bridge := NewConversation(&config.Config{Workspace: t.TempDir()}, bus, &Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), SessionService: newTestSessionService(t)}, slog.New(slog.DiscardHandler))
	require.NoError(t, bridge.Start(context.Background()))
	require.NoError(t, bridge.Stop())

	err := bridge.Submit(context.Background(), events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true))
	require.ErrorIs(t, err, errBridgeStopped)
}

func TestBridgeStartReportsStateLoadError(t *testing.T) {
	service := newTestSessionService(t)
	require.NoError(t, service.Stop(context.Background()))

	bus := events.New()
	defer bus.Close()

	bridge := NewConversation(&config.Config{Workspace: t.TempDir()}, bus, &Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), SessionService: service}, slog.New(slog.DiscardHandler))
	err := bridge.Start(context.Background())
	require.ErrorContains(t, err, "load scheduled messages")
}

func TestBridgeSummarizeValidationAndResult(t *testing.T) {
	bridge := &Bridge{requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	_, err := bridge.Summarize(context.Background(), " ")
	require.EqualError(t, err, "summary prompt is required")

	done := make(chan summaryResult, 1)

	go func() {
		text, err := bridge.Summarize(context.Background(), " summarize this ")
		done <- summaryResult{text: text, err: err}
	}()

	request := <-bridge.requestCh
	require.NotNil(t, request.summary)
	assert.Equal(t, "summarize this", request.summary.prompt)

	request.summary.resultCh <- summaryResult{text: "short summary"}

	got := <-done
	require.NoError(t, got.err)
	assert.Equal(t, "short summary", got.text)
}

func TestBridgeSummarizeReturnsContextErrorAfterEnqueue(t *testing.T) {
	bridge := &Bridge{requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := bridge.Summarize(ctx, "summarize this")
		done <- err
	}()

	request := <-bridge.requestCh
	require.NotNil(t, request.summary)
	cancel()

	err := <-done
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "summarize thread")
}

func TestBridgeSummarizeReturnsStoppedErrorAfterEnqueue(t *testing.T) {
	bridge := &Bridge{requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
	done := make(chan error, 1)

	go func() {
		_, err := bridge.Summarize(context.Background(), "summarize this")
		done <- err
	}()

	request := <-bridge.requestCh
	require.NotNil(t, request.summary)
	close(bridge.stopCh)

	err := <-done
	require.ErrorIs(t, err, errBridgeStopped)
	require.ErrorContains(t, err, "summarize thread")
}

func TestBridgeHandleSummaryReportsRunError(t *testing.T) {
	request := &summaryRequest{ctx: context.Background(), prompt: "summarize this", resultCh: make(chan summaryResult, 1)}
	bridge := &Bridge{runtime: &config.Config{Workspace: t.TempDir()}, config: Config{ConversationID: events.MainConversationID(), Agent: "main", SessionService: newTestSessionService(t)}, log: slog.New(slog.DiscardHandler)}

	bridge.handleSummary(context.Background(), request)

	result := <-request.resultCh
	require.ErrorContains(t, result.err, "summarize thread")
	assert.Empty(t, result.text)
}

func TestBridgeEnqueueReturnsContextOrStopErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	bridge := &Bridge{requestCh: make(chan bridgeRequest), stopCh: make(chan struct{})}
	err := bridge.enqueue(ctx, bridgeRequest{}, "submit test")
	require.ErrorIs(t, err, context.Canceled)

	bridge = &Bridge{requestCh: make(chan bridgeRequest), stopCh: make(chan struct{})}
	close(bridge.stopCh)
	err = bridge.enqueue(context.Background(), bridgeRequest{}, "submit test")
	require.ErrorIs(t, err, errBridgeStopped)
}

func TestBridgeSummarizeRunsQueuedSummary(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: openai/gpt-5.5\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var requestBody struct {
		Input []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("summary request method = %q; want %q", r.Method, http.MethodPost)
		}

		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode summary request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		w.Header().Set("Content-Type", "application/json")

		if _, err := w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"short summary","annotations":[]}]}]}`)); err != nil {
			t.Errorf("write summary response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	bus := events.New()
	defer bus.Close()

	bridge := NewConversation(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, bus, &Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), SessionService: service}, slog.New(slog.DiscardHandler))
	require.NoError(t, bridge.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	text, err := bridge.Summarize(ctx, "summarize this")
	require.NoError(t, err)
	assert.Equal(t, "short summary", text)
	require.Len(t, requestBody.Input, 1)
	assert.Equal(t, "user", requestBody.Input[0].Role)
	assert.Contains(t, requestBody.Input[0].Content, "summarize this")
}

func TestBridgeStopAfterStartContextCanceledIsIdempotent(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	bridge := NewConversation(&config.Config{Workspace: t.TempDir()}, bus, &Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), SessionService: newTestSessionService(t)}, slog.New(slog.DiscardHandler))
	require.NoError(t, bridge.Start(ctx))

	cancel()

	select {
	case <-bridge.stopCh:
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop after context cancellation")
	}

	require.NoError(t, bridge.Stop())
}

func TestBridgeWaitIdle(t *testing.T) {
	bridge := &Bridge{requestCh: make(chan bridgeRequest, 1)}
	require.NoError(t, bridge.WaitIdle(t.Context()))

	bridge.setHandling(true)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()

	require.Error(t, bridge.WaitIdle(ctx))
}

func TestBridgeForwardInboundFiltersConversation(t *testing.T) {
	ctx := t.Context()

	bus := events.New()
	defer bus.Close()

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "target"}, bus: bus, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
	go bridge.forwardInbound(ctx)

	require.NoError(t, bus.PublishInbound(ctx, &events.InboundMessage{ConversationID: "other", Text: "skip"}))
	require.NoError(t, bus.PublishInbound(ctx, &events.InboundMessage{ConversationID: "target", Text: "keep"}))

	select {
	case request := <-bridge.requestCh:
		require.NotNil(t, request.inbound)
		assert.Equal(t, "keep", request.inbound.Text)
	case <-time.After(time.Second):
		t.Fatal("matching inbound was not forwarded")
	}
}

func TestScheduleMessageToolValidatesAndPreservesMessage(t *testing.T) {
	var (
		delay     time.Duration
		message   string
		recurring bool
		logs      bytes.Buffer
	)

	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	tool := scheduleMessageTool(func(d time.Duration, msg string, repeat bool) error {
		delay = d
		message = msg
		recurring = repeat

		return nil
	}, logger)
	assert.ElementsMatch(t, []string{"message", "send_this_in", "recurring"}, tool.Parameters["required"])

	result, err := tool.Call(context.Background(), []byte(`{"message":"  keep spaces  ","send_this_in":"5m"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "scheduled message in 5m0s", result.Output)
	assert.Equal(t, 5*time.Minute, delay)
	assert.Equal(t, "  keep spaces  ", message)
	assert.False(t, recurring)
	assert.Contains(t, logs.String(), "rocketclaw schedule message tool called")

	result, err = tool.Call(context.Background(), []byte(`{"message":"again","send_this_in":"1m","recurring":true}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "scheduled recurring message every 1m0s", result.Output)
	assert.True(t, recurring)

	for _, raw := range []string{
		`{`,
		`{"message":"  ","send_this_in":"5m"}`,
		`{"message":"hello","send_this_in":"nope"}`,
		`{"message":"hello","send_this_in":"0s"}`,
		`{"message":"hello","send_this_in":"2h"}`,
		`{"message":"hello","send_this_in":"30s","recurring":true}`,
	} {
		_, err := tool.Call(context.Background(), []byte(raw), nil)
		require.Error(t, err, raw)
	}

	tool = scheduleMessageTool(func(time.Duration, string, bool) error { return assert.AnError }, logger)
	_, err = tool.Call(context.Background(), []byte(`{"message":"hello","send_this_in":"5m"}`), nil)
	require.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, logs.String(), "rocketclaw schedule message tool failed")
}

func TestResetScheduledMessagesToolUsesScheduleSubject(t *testing.T) {
	reset := false
	tool := resetScheduledMessagesTool(func() error {
		reset = true

		return nil
	})

	assert.Equal(t, resetScheduledMessagesToolName, tool.Name)
	assert.Equal(t, []string{scheduleMessageToolName}, tool.VisibilitySubjects)
	subjects, err := tool.Subjects(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{scheduleMessageToolName}, subjects)

	result, err := tool.Call(context.Background(), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "scheduled messages reset", result.Output)
	assert.True(t, reset)

	tool = resetScheduledMessagesTool(func() error { return assert.AnError })
	_, err = tool.Call(context.Background(), nil, nil)
	require.ErrorIs(t, err, assert.AnError)
}

func TestAttachFilesToolReadsWorkspacePath(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.Mkdir("reports", 0o755))
	require.NoError(t, root.WriteFile("reports/latest.txt", []byte("report body"), 0o644))

	attachments := new(outboundAttachmentCollector)
	tool := attachments.Tool(root)
	parameters := tool.Parameters
	properties := parameters["properties"].(map[string]any)
	attachmentsSchema := properties["attachments"].(map[string]any)
	items := attachmentsSchema["items"].(map[string]any)
	assert.ElementsMatch(t, []string{"path", "name", "mime_type", "content", "content_base64"}, items["required"])

	_, err = tool.Call(t.Context(), []byte(`{"attachments":[{"path":"reports/latest.txt","name":"","mime_type":"","content":"","content_base64":""}]}`), nil)
	require.NoError(t, err)

	assert.Equal(t, []events.OutboundAttachment{{Name: "latest.txt", MIMEType: "text/plain", Data: []byte("report body")}}, attachments.Attachments())
}

func TestOutboundAttachmentSources(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	got, err := outboundAttachment(root, &outboundAttachmentInput{Name: "note.txt", Content: "hello"})
	require.NoError(t, err)
	assert.Equal(t, events.OutboundAttachment{Name: "note.txt", MIMEType: "text/plain", Data: []byte("hello")}, got)

	got, err = outboundAttachment(root, &outboundAttachmentInput{Name: "", MIMEType: "Text/Plain; Charset=UTF-8", ContentBase64: "aGVsbG8="})
	require.NoError(t, err)
	assert.Equal(t, events.OutboundAttachment{Name: "attachment", MIMEType: "text/plain", Data: []byte("hello")}, got)

	got, err = outboundAttachment(root, &outboundAttachmentInput{Name: "blob", Content: "hello"})
	require.NoError(t, err)
	assert.Equal(t, events.OutboundAttachment{Name: "blob", MIMEType: "text/plain", Data: []byte("hello")}, got)

	_, err = outboundAttachment(root, &outboundAttachmentInput{Name: "bad", ContentBase64: "%%%"})
	require.ErrorContains(t, err, `decode attachment "bad"`)

	_, err = outboundAttachment(root, &outboundAttachmentInput{Path: "missing.txt"})
	require.ErrorContains(t, err, `read attachment "missing.txt"`)

	_, err = outboundAttachment(root, &outboundAttachmentInput{Name: "empty"})
	require.ErrorContains(t, err, `attachment "empty" has no content or path`)
}

func TestAttachFilesToolReportsInvalidInput(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	tool := new(outboundAttachmentCollector).Tool(root)
	_, err = tool.Call(t.Context(), []byte(`{`), nil)
	require.ErrorContains(t, err, "parse response attachments")

	raw := []byte(`{"attachments":[{"path":"missing.txt","name":"","mime_type":"","content":"","content_base64":""}]}`)
	_, err = tool.Call(t.Context(), raw, nil)
	require.ErrorContains(t, err, `read attachment "missing.txt"`)
}

func TestAttachmentFallbackAndImageAttachments(t *testing.T) {
	assert.Empty(t, attachmentFallback(&events.InboundMessage{Attachments: []events.InboundAttachment{{Name: "image.png"}}}))
	assert.Equal(t, unsupportedFileFallback, attachmentFallback(&events.InboundMessage{HadNonImageAttachments: true}))
	assert.Contains(t, attachmentFallback(&events.InboundMessage{HadAttachments: true, AttachmentWarnings: []string{" first ", " ", "second"}}), "- first\n- second")

	attachments := attachmentsFromInbound([]events.InboundAttachment{
		{Name: "photo.jpg", MIMEType: "image/jpeg", Data: []byte("jpg")},
		{Name: "unknown", MIMEType: "image/webp", Data: []byte("webp")},
	})
	require.Len(t, attachments, 2)
	assert.Equal(t, "image/jpeg", attachments[0].MIME)
	assert.Equal(t, "data:image/jpeg;base64,anBn", attachments[0].URL)
	assert.Equal(t, "image/webp", attachments[1].MIME)
	assert.Equal(t, "data:image/webp;base64,d2VicA==", attachments[1].URL)
	assert.Equal(t, "new", appendText("", " new "))
	assert.Equal(t, "old\nnew", appendText("old", " new "))
	assert.Equal(t, "old", appendText("old", " "))
}

func TestNormalizeInboundAttachmentsCentralizesModelPolicy(t *testing.T) {
	msg := &events.InboundMessage{Attachments: []events.InboundAttachment{
		{Name: "tiny.png", MIMEType: "application/octet-stream", Data: tinyPNG()},
		{Name: "not-image.png", MIMEType: "image/png", Data: []byte("not an image")},
		{Name: "empty.png", MIMEType: "image/png"},
	}}

	normalizeInboundAttachments(msg)

	require.Len(t, msg.Attachments, 1)
	assert.True(t, msg.HadAttachments)
	assert.Equal(t, "tiny.png", msg.Attachments[0].Name)
	assert.Equal(t, "image/png", msg.Attachments[0].MIMEType)
	assert.Equal(t, tinyPNG(), msg.Attachments[0].Data)
	assert.Equal(t, []string{
		"Skipped attachment not-image.png because text/plain is not supported.",
		"Skipped attachment empty.png because it was empty.",
	}, msg.AttachmentWarnings)
}

func TestModelAttachmentMIMETypePrecedence(t *testing.T) {
	assert.Equal(t, "text/plain", modelAttachmentMIMEType([]byte("plain text"), "image/png", "photo.png"))
	assert.Equal(t, "image/png", modelAttachmentMIMEType(nil, " Image/PNG; Charset=UTF-8 ", "photo.jpg"))
	assert.Equal(t, "image/jpeg", modelAttachmentMIMEType(nil, " ", "photo.jpg"))
	assert.Empty(t, modelAttachmentMIMEType(nil, " ", "photo"))
}

func TestNormalizeInboundAttachmentsRejectsAttachmentsTooLargeToReduce(t *testing.T) {
	msg := &events.InboundMessage{Attachments: []events.InboundAttachment{
		{Name: "large.png", MIMEType: "image/png", Data: append(tinyPNG(), make([]byte, maxInboundAttachmentResizeInput)...)},
	}}

	normalizeInboundAttachments(msg)

	assert.Empty(t, msg.Attachments)
	assert.True(t, msg.HadAttachments)
	assert.Equal(t, []string{"Skipped attachment large.png because it was too large to attempt size reduction."}, msg.AttachmentWarnings)
}

func TestReduceResizedImageWithinLimitReportsEncoderErrorsAndExhaustion(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))

	_, err := reduceResizedImageWithinLimit(img, 1000, 1, func(image.Image, int) ([]byte, int, error) {
		return nil, 0, assert.AnError
	})
	require.ErrorIs(t, err, assert.AnError)

	_, err = reduceResizedImageWithinLimit(img, 1000, 1, func(image.Image, int) ([]byte, int, error) {
		return nil, 1000, nil
	})
	require.ErrorIs(t, err, errInboundAttachmentReductionNotEnough)
}

func TestFitInboundImageWithinLimitLeavesSmallImageUnchanged(t *testing.T) {
	data := encodeAttachmentTestPNG(t, newAttachmentTestImage(1, 1), png.BestCompression)

	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit(" Image/PNG; Charset=UTF-8 ", data, len(data))
	require.NoError(t, err)
	assert.Equal(t, data, transformed)
	assert.Equal(t, "image/png", transformedMIMEType)
	assert.False(t, changed)
}

func TestFitInboundImageWithinLimitUsesLosslessPNGFirst(t *testing.T) {
	img := newAttachmentTestImage(160, 160)
	original := encodeAttachmentTestPNG(t, img, png.NoCompression)
	target := len(encodeAttachmentTestPNG(t, img, png.BestCompression))
	require.Greater(t, len(original), target)

	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/png", original, target)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "image/png", transformedMIMEType)
	assert.LessOrEqual(t, len(transformed), target)

	originalConfig := decodeAttachmentTestImageConfig(t, original)
	transformedConfig := decodeAttachmentTestImageConfig(t, transformed)
	assert.Equal(t, originalConfig.Width, transformedConfig.Width)
	assert.Equal(t, originalConfig.Height, transformedConfig.Height)
}

func TestFitInboundImageWithinLimitUsesSmallestSuccessfulJPEGChangeFirst(t *testing.T) {
	img := newAttachmentTestImage(256, 256)
	original := encodeAttachmentTestJPEG(t, img, 100)
	target := 0

	for quality := 95; quality >= 50; quality -= 5 {
		candidateSize := len(encodeAttachmentTestJPEG(t, img, quality))
		if candidateSize < len(original) {
			target = candidateSize
			break
		}
	}

	require.NotZero(t, target)

	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/jpeg", original, target)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "image/jpeg", transformedMIMEType)
	assert.LessOrEqual(t, len(transformed), target)

	originalConfig := decodeAttachmentTestImageConfig(t, original)
	transformedConfig := decodeAttachmentTestImageConfig(t, transformed)
	assert.Equal(t, originalConfig.Width, transformedConfig.Width)
	assert.Equal(t, originalConfig.Height, transformedConfig.Height)
}

func TestFitInboundImageWithinLimitResizesPNG(t *testing.T) {
	original := encodeAttachmentTestPNG(t, newAttachmentTestImage(400, 400), png.NoCompression)
	target := 4096
	require.Greater(t, len(original), target)

	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/png", original, target)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "image/png", transformedMIMEType)
	assert.LessOrEqual(t, len(transformed), target)

	originalConfig := decodeAttachmentTestImageConfig(t, original)
	transformedConfig := decodeAttachmentTestImageConfig(t, transformed)
	assert.Less(t, transformedConfig.Width, originalConfig.Width)
	assert.Less(t, transformedConfig.Height, originalConfig.Height)
}

func TestFitInboundImageWithinLimitFallsBackToJPEG(t *testing.T) {
	original := encodeAttachmentTestPNG(t, newAttachmentTestImage(80, 80), png.NoCompression)
	target := 1024
	require.Greater(t, len(original), target)

	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/webp", original, target)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "image/jpeg", transformedMIMEType)
	assert.LessOrEqual(t, len(transformed), target)

	cfg := decodeAttachmentTestImageConfig(t, transformed)
	assert.NotZero(t, cfg.Width)
	assert.NotZero(t, cfg.Height)
}

func TestFitInboundImageWithinLimitReportsDecodeFailure(t *testing.T) {
	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/webp", []byte("not an image"), 1)
	assert.Nil(t, transformed)
	assert.Empty(t, transformedMIMEType)
	assert.False(t, changed)
	assert.ErrorIs(t, err, errInboundAttachmentReductionFailed)
}

func TestFitInboundImageWithinLimitRejectsImpossibleTarget(t *testing.T) {
	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/png", []byte("x"), 0)
	assert.Nil(t, transformed)
	assert.Empty(t, transformedMIMEType)
	assert.False(t, changed)
	require.ErrorIs(t, err, errInboundAttachmentReductionNotEnough)

	transformed, changed, err = resizePNGWithinLimit([]byte("x"), 0)
	assert.Nil(t, transformed)
	assert.False(t, changed)
	assert.ErrorIs(t, err, errInboundAttachmentReductionNotEnough)
}

func TestFitInboundImageWithinLimitReportsPNGDecodeFailure(t *testing.T) {
	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/png", []byte("not a png"), 1)
	assert.Nil(t, transformed)
	assert.Empty(t, transformedMIMEType)
	assert.False(t, changed)
	assert.ErrorIs(t, err, errInboundAttachmentReductionFailed)
}

func TestResizePNGWithinLimitRejectsSinglePixelImageTooLargeForTarget(t *testing.T) {
	original := encodeAttachmentTestPNG(t, newAttachmentTestImage(1, 1), png.NoCompression)

	transformed, changed, err := resizePNGWithinLimit(original, 1)
	assert.Nil(t, transformed)
	assert.False(t, changed)
	assert.ErrorIs(t, err, errInboundAttachmentReductionNotEnough)
}

func TestNextImageResizeDimensionsStillShrinksWhenEstimateWouldGrow(t *testing.T) {
	nextWidth, nextHeight := nextImageResizeDimensions(2, 2, 100, 10000)

	assert.Equal(t, 1, nextWidth)
	assert.Equal(t, 1, nextHeight)
}

func TestInboundAttachmentReductionFailureReason(t *testing.T) {
	assert.Equal(t, "image reduction failed", inboundAttachmentReductionFailureReason(errInboundAttachmentReductionFailed, maxInboundAttachmentBytes))
	assert.Equal(t, "it still exceeded the remaining attachment budget after reduction", inboundAttachmentReductionFailureReason(errInboundAttachmentReductionNotEnough, 1))
	assert.Equal(t, "it still exceeded the per-file size limit after reduction", inboundAttachmentReductionFailureReason(errInboundAttachmentReductionNotEnough, maxInboundAttachmentBytes))
}

func TestNormalizeInboundAttachmentsRejectsTotalBudgetOverflow(t *testing.T) {
	data := append(tinyPNG(), make([]byte, maxInboundAttachmentBytes-len(tinyPNG()))...)
	msg := &events.InboundMessage{Attachments: []events.InboundAttachment{
		{Name: "one.png", MIMEType: "image/png", Data: data},
		{Name: "two.png", MIMEType: "image/png", Data: data},
		{Name: "three.png", MIMEType: "image/png", Data: data},
		{Name: "four.png", MIMEType: "image/png", Data: data},
		{MIMEType: "image/png", Data: data},
	}}

	normalizeInboundAttachments(msg)

	require.Len(t, msg.Attachments, 4)
	assert.Equal(t, []string{"Skipped attachment attachment-5 because the message exceeded the attachment size budget."}, msg.AttachmentWarnings)
}

func tinyPNG() []byte {
	return []byte("\x89PNG\r\n\x1a\n")
}

func newAttachmentTestImage(width, height int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.NRGBA{R: uint8((x*31 + y*17) % 256), G: uint8((x*13 + y*29) % 256), B: uint8((x*7 + y*19) % 256), A: 0xff})
		}
	}

	return img
}

func encodeAttachmentTestPNG(t *testing.T, img image.Image, level png.CompressionLevel) []byte {
	t.Helper()

	var buffer bytes.Buffer

	encoder := png.Encoder{CompressionLevel: level, BufferPool: nil}
	require.NoError(t, encoder.Encode(&buffer, img))

	return buffer.Bytes()
}

func encodeAttachmentTestJPEG(t *testing.T, img image.Image, quality int) []byte {
	t.Helper()

	var buffer bytes.Buffer

	options := jpeg.Options{Quality: quality}
	require.NoError(t, jpeg.Encode(&buffer, img, &options))

	return buffer.Bytes()
}

func decodeAttachmentTestImageConfig(t *testing.T, data []byte) image.Config {
	t.Helper()

	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err)

	return cfg
}

func TestCompactedOutputToReplayInputPreservesSupportedItems(t *testing.T) {
	items := []responses.ResponseOutputItemUnion{
		{
			Type: "message",
			ID:   "msg_1",
			Content: []responses.ResponseOutputMessageContentUnion{
				{Type: "output_text", Text: "hello "},
				{Type: "refusal", Refusal: "no"},
				{Type: "output_text", Text: "world"},
			},
			Phase: responses.ResponseOutputMessagePhase("final_answer"),
		},
		{
			Type:    "message",
			ID:      "msg_2",
			Role:    "assistant",
			Content: []responses.ResponseOutputMessageContentUnion{{Type: "output_text", Text: "assistant"}},
		},
		{Type: "compaction", ID: "cmp_1", EncryptedContent: "sealed"},
		{Type: "compaction_summary", ID: "cmp_2", EncryptedContent: "chatgpt-sealed"},
		{Type: "reasoning", ID: "rsn_1", Summary: []responses.ResponseReasoningItemSummary{{Text: "summary"}}, EncryptedContent: "reasoning-sealed"},
		{Type: "reasoning", ID: "rsn_2"},
	}

	got, err := compactedOutputToReplayInput(items)
	require.NoError(t, err)
	params, err := rocketcode.ReplayInputToParams(got)
	require.NoError(t, err)
	require.Len(t, params, len(items))

	assert.Equal(t, "hello world", params[0].OfMessage.Content.OfString.Value)
	assert.Equal(t, responses.EasyInputMessagePhase("final_answer"), params[0].OfMessage.Phase)
	assert.Equal(t, "assistant", params[1].OfMessage.Content.OfString.Value)
	assert.Equal(t, "sealed", params[2].OfCompaction.EncryptedContent)
	assert.Equal(t, "chatgpt-sealed", params[3].OfCompaction.EncryptedContent)
	assert.Equal(t, "summary", params[4].OfReasoning.Summary[0].Text)
	assert.Equal(t, "rsn_2", params[5].OfReasoning.ID)
}

func TestCompactedOutputToReplayInputRejectsUnsupportedKind(t *testing.T) {
	_, err := compactedOutputToReplayInput([]responses.ResponseOutputItemUnion{{Type: "tool_search_call"}})
	require.ErrorContains(t, err, `unsupported compacted output item kind "tool_search_call"`)
}

func TestCompactSeedReplayReportsInvalidReplayInput(t *testing.T) {
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: events.MainConversationID()}}

	_, err := bridge.compactSeedReplay(context.Background(), []rocketcode.SessionEntry{{ReplayInput: []json.RawMessage{json.RawMessage("{")}}}, "")
	require.ErrorContains(t, err, "build compaction input: entry 1")
}

func TestCompactSeedReplayUsesDefaultModelAndReportsProviderError(t *testing.T) {
	var requestBody struct {
		Model string `json:"model"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses/compact" {
			http.NotFound(w, r)

			return
		}

		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		http.Error(w, `{"error":{"message":"blocked"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)

	bridge := &Bridge{runtime: &config.Config{Workspace: t.TempDir(), OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, log: slog.New(slog.DiscardHandler), config: Config{ConversationID: events.MainConversationID()}}
	_, err := bridge.compactSeedReplay(context.Background(), []rocketcode.SessionEntry{{ReplayInput: testReplayInput(replayInputMessage{role: "user", text: "main question"})}}, "")
	require.ErrorContains(t, err, "compact seed replay")
	assert.Equal(t, string(responses.ResponseCompactParamsModelGPT5_4), requestBody.Model)
}

func TestReplayInputMessageRoleTextCoversMessageShapes(t *testing.T) {
	plain := responses.ResponseInputItemUnionParam{OfMessage: &responses.EasyInputMessageParam{Role: "assistant", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("plain")}, Type: "message"}}
	role, text, ok := replayInputMessageRoleText(&plain)
	require.True(t, ok)
	assert.Equal(t, "assistant", role)
	assert.Equal(t, "plain", text)

	withContent := responses.ResponseInputItemUnionParam{OfInputMessage: &responses.ResponseInputItemMessageParam{Role: "user", Content: responses.ResponseInputMessageContentListParam{responses.ResponseInputContentParamOfInputText("look")}, Type: "message"}}
	role, text, ok = replayInputMessageRoleText(&withContent)
	require.True(t, ok)
	assert.Equal(t, "user", role)
	assert.Equal(t, "look", text)

	_, _, ok = replayInputMessageRoleText(&responses.ResponseInputItemUnionParam{})
	assert.False(t, ok)
}

func TestReplayInputMessagesFiltersBlankMessages(t *testing.T) {
	raw, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		{OfMessage: &responses.EasyInputMessageParam{Role: "user", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(" ")}, Type: "message"}},
		{OfMessage: &responses.EasyInputMessageParam{Role: "assistant", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("answer")}, Type: "message"}},
	})
	require.NoError(t, err)

	messages, err := replayInputMessages(raw)
	require.NoError(t, err)
	assert.Equal(t, []replayInputMessage{{role: "assistant", text: "answer"}}, messages)
}

func TestReplayInputMessagesReportsBadJSON(t *testing.T) {
	_, err := replayInputMessages([]json.RawMessage{json.RawMessage("{")})
	require.ErrorContains(t, err, "decode replay input messages")
}

func TestReplayInputRawKindReportsInvalidJSON(t *testing.T) {
	assert.Empty(t, replayInputRawKind(json.RawMessage("{")))
}

func TestBuildPromptCoversAttachmentsAndInternalNotes(t *testing.T) {
	prompt := buildPrompt(&events.InboundMessage{Text: "  hello  ", AttachmentWarnings: []string{" skipped image ", " "}})
	assert.Contains(t, prompt, "Reply in plain text")
	assert.Contains(t, prompt, "User message:\nhello\n\nAttachment notes:\n- skipped image")

	prompt = buildPrompt(&events.InboundMessage{Attachments: []events.InboundAttachment{{Name: "one.png"}, {Name: "two.png"}}})
	assert.Contains(t, prompt, "User attached 2 files with no accompanying text.")

	prompt = buildPrompt(&events.InboundMessage{AttachmentWarnings: []string{" unsupported PDF "}})
	assert.Contains(t, prompt, "User message:\nAttachment notes:\n- unsupported PDF")

	prompt = buildPrompt(&events.InboundMessage{Kind: events.InboundKindInternalize, Text: "  keep\nspaces  "})
	assert.Contains(t, prompt, "Internalize the following note")
	assert.Contains(t, prompt, "Internal note:\n  keep\nspaces  ")
}

func TestBridgeScheduleMessageSubmitsAfterDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: SlackThreadConversationID("D123", "111.222"), SessionService: newTestSessionService(t)}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		require.NoError(t, bridge.ScheduleMessage(5*time.Second, "later", false))
		synctest.Wait()

		select {
		case <-bridge.requestCh:
			t.Fatal("scheduled message submitted before delay")
		default:
		}

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case request := <-bridge.requestCh:
			require.NotNil(t, request.inbound)
			assert.NotEmpty(t, request.scheduledMessageID)
			assert.Equal(t, "later", request.inbound.Text)
			assert.Equal(t, bridge.config.ConversationID, request.inbound.ConversationID)
			require.NotNil(t, request.inbound.SlackReply)
			assert.Equal(t, events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}, *request.inbound.SlackReply)
		case <-time.After(time.Nanosecond):
			t.Fatal("scheduled message was not submitted")
		}

		state, err := bridge.config.SessionService.Load()
		require.NoError(t, err)
		require.Len(t, state.ScheduledMessages, 1)
	})
}

func TestBridgeScheduleMessagePersistsRecurringMetadata(t *testing.T) {
	service := newTestSessionService(t)
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: events.MainConversationID(), Agent: "main", SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.ScheduleMessage(time.Minute, "again", true))

	state, err := service.Load()
	require.NoError(t, err)
	require.Len(t, state.ScheduledMessages, 1)

	for _, scheduled := range state.ScheduledMessages {
		assert.True(t, scheduled.Recurring)
		assert.Equal(t, time.Minute, scheduled.Interval)
		assert.Equal(t, "again", scheduled.Message)
	}
}

func TestBridgeScheduleMessageLogsPersistFailure(t *testing.T) {
	store := newTestSessionService(t)
	require.NoError(t, store.Stop(context.Background()))

	var logs bytes.Buffer

	bridge := &Bridge{log: slog.New(slog.NewJSONHandler(&logs, nil)), config: Config{ConversationID: events.MainConversationID(), Agent: "main", SessionService: store}}

	require.Error(t, bridge.ScheduleMessage(time.Minute, "later", false))
	assert.Contains(t, logs.String(), "scheduled message persist failed")
}

func TestBridgeScheduleMessageSubmitsExternalMCPInPersistedSlackThread(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		conversationID := "external_mcp:planner:abc"
		threadKey := SlackThreadConversationID("D123", "111.222")

		require.NoError(t, service.updateState(func(state *State) {
			state.Threads = map[string]ThreadState{threadKey: {Agent: "planner", SeededFromResponse: conversationID}}
		}))

		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, Agent: "planner", SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		require.NoError(t, bridge.ScheduleMessage(5*time.Second, "later", false))

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case request := <-bridge.requestCh:
			require.NotNil(t, request.inbound)
			assert.Equal(t, "later", request.inbound.Text)
			assert.Equal(t, conversationID, request.inbound.ConversationID)
			require.NotNil(t, request.inbound.SlackReply)
			assert.Equal(t, events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}, *request.inbound.SlackReply)
		case <-time.After(time.Nanosecond):
			t.Fatal("scheduled external MCP message was not submitted")
		}
	})
}

func TestBridgeScheduleMessageExternalMCPDoesNotUseUnrelatedSlackThread(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		conversationID := "external_mcp:planner:abc"
		threadKey := SlackThreadConversationID("D123", "111.222")

		require.NoError(t, service.updateState(func(state *State) {
			state.Threads = map[string]ThreadState{threadKey: {Agent: "planner", SeededFromResponse: "external_mcp:planner:other"}}
		}))

		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, Agent: "planner", SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		require.NoError(t, bridge.ScheduleMessage(5*time.Second, "later", false))

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case request := <-bridge.requestCh:
			require.NotNil(t, request.inbound)
			assert.Equal(t, "later", request.inbound.Text)
			assert.Equal(t, conversationID, request.inbound.ConversationID)
			assert.Nil(t, request.inbound.SlackReply)
		case <-time.After(time.Nanosecond):
			t.Fatal("scheduled external MCP message was not submitted")
		}
	})
}

func TestBridgeDeletesScheduledMessageAfterSuccessfulHandling(t *testing.T) {
	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	require.NoError(t, service.updateState(func(state *State) {
		state.ScheduledMessages = map[string]ScheduledMessageState{"schedule-1": {ConversationID: events.MainConversationID(), Agent: "main", Message: "later", DueAt: time.Now().UTC()}}
	}))

	bus := events.New()
	defer bus.Close()

	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), RequestRestart: testNoopRestart, SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.requestCh = make(chan bridgeRequest, 1)
	bridge.stopCh = make(chan struct{})

	go bridge.loop(t.Context())

	inbound := events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "scheduled_message", "later", false)
	inbound.HadNonImageAttachments = true
	require.NoError(t, bridge.enqueue(context.Background(), bridgeRequest{inbound: inbound, scheduledMessageID: "schedule-1"}, "submit scheduled message"))

	outbound := readRocketCodeOutbound(t, bus)
	assert.Equal(t, unsupportedFileFallback, outbound.Text)
	outbound.MarkDelivered(nil)

	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	require.NoError(t, bridge.WaitIdle(waitCtx))

	state, err := service.Load()
	require.NoError(t, err)
	assert.Empty(t, state.ScheduledMessages)
}

func TestBridgeKeepsScheduledMessageAfterHandlingError(t *testing.T) {
	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	require.NoError(t, service.updateState(func(state *State) {
		state.ScheduledMessages = map[string]ScheduledMessageState{"schedule-1": {ConversationID: events.MainConversationID(), Agent: "main", Message: "later", DueAt: time.Now().UTC()}}
	}))

	bus := events.New()
	bus.Close()

	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), RequestRestart: testNoopRestart, SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.requestCh = make(chan bridgeRequest, 1)
	bridge.stopCh = make(chan struct{})

	go bridge.loop(t.Context())

	inbound := events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "scheduled_message", "later", false)
	inbound.HadNonImageAttachments = true
	require.NoError(t, bridge.enqueue(context.Background(), bridgeRequest{inbound: inbound, scheduledMessageID: "schedule-1"}, "submit scheduled message"))

	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	require.NoError(t, bridge.WaitIdle(waitCtx))

	state, err := service.Load()
	require.NoError(t, err)
	require.Len(t, state.ScheduledMessages, 1)
}

func TestBridgeKeepsRecurringScheduledMessageAfterSuccessfulHandling(t *testing.T) {
	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	require.NoError(t, service.updateState(func(state *State) {
		state.ScheduledMessages = map[string]ScheduledMessageState{"schedule-1": {ConversationID: events.MainConversationID(), Agent: "main", Message: "later", DueAt: time.Now().UTC(), Recurring: true, Interval: time.Minute}}
	}))

	bus := events.New()
	defer bus.Close()

	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), RequestRestart: testNoopRestart, SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.requestCh = make(chan bridgeRequest, 1)
	bridge.stopCh = make(chan struct{})

	go bridge.loop(t.Context())

	inbound := events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "scheduled_message", "later", false)
	inbound.HadNonImageAttachments = true
	require.NoError(t, bridge.enqueue(context.Background(), bridgeRequest{inbound: inbound, scheduledMessageID: "schedule-1", scheduledMessageRecurring: true}, "submit scheduled message"))

	outbound := readRocketCodeOutbound(t, bus)
	assert.Equal(t, unsupportedFileFallback, outbound.Text)
	outbound.MarkDelivered(nil)

	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()

	require.NoError(t, bridge.WaitIdle(waitCtx))

	state, err := service.Load()
	require.NoError(t, err)
	require.Contains(t, state.ScheduledMessages, "schedule-1")
}

func TestBridgeStopDisarmsScheduledMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer

		bridge := &Bridge{log: slog.New(slog.NewJSONHandler(&logs, nil)), config: Config{ConversationID: events.MainConversationID(), SessionService: newTestSessionService(t)}, inputStop: func() {}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		require.NoError(t, bridge.ScheduleMessage(5*time.Second, "later", false))
		require.NoError(t, bridge.Stop())

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case <-bridge.requestCh:
			t.Fatal("scheduled message submitted after bridge stop")
		default:
		}

		assert.Contains(t, logs.String(), "scheduled message enqueue failed")
	})
}

func TestBridgeResetScheduledMessagesDeletesPersistedAndCancelsArmed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		store, err := NewSessionService(workspace)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Stop(context.Background())) })

		var logs bytes.Buffer

		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		conversationID := events.MainConversationID()
		bridge := NewConversation(&config.Config{Workspace: workspace}, nil, &Config{ConversationID: conversationID, Agent: "main", SessionService: store}, logger)
		bridge.requestCh = make(chan bridgeRequest, 1)
		bridge.stopCh = make(chan struct{})

		require.NoError(t, bridge.ScheduleMessage(5*time.Second, "later", false))
		require.NoError(t, store.updateState(func(state *State) {
			state.ScheduledMessages["other"] = ScheduledMessageState{ConversationID: "other", Agent: "main", Message: "keep", DueAt: time.Now().UTC().Add(time.Hour)}
		}))

		require.NoError(t, bridge.ResetScheduledMessages())
		synctest.Wait()
		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case <-bridge.requestCh:
			t.Fatal("scheduled message submitted after reset")
		default:
		}

		state, err := store.Load()
		require.NoError(t, err)
		require.Len(t, state.ScheduledMessages, 1)
		assert.Equal(t, "other", state.ScheduledMessages["other"].ConversationID)
		assert.Equal(t, "keep", state.ScheduledMessages["other"].Message)
		assert.Contains(t, logs.String(), "scheduled message persisted")
		assert.Contains(t, logs.String(), "scheduled messages reset")
		assert.Contains(t, logs.String(), "scheduled message missing or stale at due time")
	})
}

func TestBridgeResetScheduledMessagesReportsStoreError(t *testing.T) {
	store := newTestSessionService(t)
	require.NoError(t, store.Stop(context.Background()))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: events.MainConversationID(), SessionService: store}}
	require.Error(t, bridge.ResetScheduledMessages())
}

func TestBridgeArmsOverdueScheduledMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: events.MainConversationID(), SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		due := ScheduledMessageState{ConversationID: events.MainConversationID(), Agent: "main", Message: "now", DueAt: time.Now().UTC().Add(-time.Second)}

		require.NoError(t, service.updateState(func(state *State) { state.ScheduledMessages = map[string]ScheduledMessageState{"due": due} }))
		bridge.armScheduledMessage("due", &due)
		synctest.Wait()

		select {
		case request := <-bridge.requestCh:
			require.NotNil(t, request.inbound)
			assert.NotEmpty(t, request.scheduledMessageID)
			assert.Equal(t, "now", request.inbound.Text)
		case <-time.After(time.Nanosecond):
			t.Fatal("overdue scheduled message was not submitted")
		}
	})
}

func TestBridgeRecurringScheduledMessageAdvancesAndRearms(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: events.MainConversationID(), SessionService: service}, requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
		due := ScheduledMessageState{ConversationID: events.MainConversationID(), Agent: "main", Message: "again", DueAt: time.Now().UTC().Add(5 * time.Second), Recurring: true, Interval: time.Minute}

		require.NoError(t, service.updateState(func(state *State) { state.ScheduledMessages = map[string]ScheduledMessageState{"repeat": due} }))
		bridge.armScheduledMessage("repeat", &due)

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case request := <-bridge.requestCh:
			require.NotNil(t, request.inbound)
			assert.Equal(t, "again", request.inbound.Text)
			assert.True(t, request.scheduledMessageRecurring)
		case <-time.After(time.Nanosecond):
			t.Fatal("recurring scheduled message was not submitted")
		}

		state, err := service.Load()
		require.NoError(t, err)

		advanced := state.ScheduledMessages["repeat"]
		assert.True(t, advanced.Recurring)
		assert.Equal(t, time.Minute, advanced.Interval)
		assert.True(t, advanced.DueAt.After(due.DueAt))

		time.Sleep(time.Minute)
		synctest.Wait()

		select {
		case request := <-bridge.requestCh:
			require.NotNil(t, request.inbound)
			assert.Equal(t, "again", request.inbound.Text)
			assert.True(t, request.scheduledMessageRecurring)
		case <-time.After(time.Nanosecond):
			t.Fatal("recurring scheduled message was not rearmed")
		}
	})
}

func TestBridgeStaleScheduledMessageTimerDoesNotSubmit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: events.MainConversationID(), SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		oldDue := ScheduledMessageState{ConversationID: events.MainConversationID(), Agent: "main", Message: "old", DueAt: time.Now().UTC().Add(5 * time.Second)}
		newDue := oldDue
		newDue.DueAt = newDue.DueAt.Add(time.Minute)

		require.NoError(t, service.updateState(func(state *State) { state.ScheduledMessages = map[string]ScheduledMessageState{"stale": newDue} }))
		bridge.armScheduledMessage("stale", &oldDue)

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case <-bridge.requestCh:
			t.Fatal("stale scheduled message was submitted")
		default:
		}
	})
}

func TestBridgeScheduledMessageTimerStopsOnStoreError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: events.MainConversationID(), SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		due := ScheduledMessageState{ConversationID: events.MainConversationID(), Agent: "main", Message: "later", DueAt: time.Now().UTC().Add(5 * time.Second)}

		require.NoError(t, service.updateState(func(state *State) { state.ScheduledMessages = map[string]ScheduledMessageState{"broken": due} }))
		require.NoError(t, service.Stop(context.Background()))
		bridge.armScheduledMessage("broken", &due)

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case <-bridge.requestCh:
			t.Fatal("scheduled message submitted after store error")
		default:
		}
	})
}

func TestBridgeRestoresScheduledMessageAfterRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		store, err := NewSessionService(workspace)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Stop(context.Background())) })

		first := NewConversation(&config.Config{Workspace: workspace}, nil, &Config{ConversationID: events.MainConversationID(), Agent: "main", SessionService: store}, slog.New(slog.DiscardHandler))
		require.NoError(t, first.Start(t.Context()))
		require.NoError(t, first.ScheduleMessage(5*time.Second, "later", false))
		require.NoError(t, first.Stop())

		var logs bytes.Buffer

		second := NewConversation(&config.Config{Workspace: workspace}, nil, &Config{ConversationID: events.MainConversationID(), Agent: "main", SessionService: store}, slog.New(slog.NewJSONHandler(&logs, nil)))
		second.requestCh = make(chan bridgeRequest, 1)
		second.stopCh = make(chan struct{})
		state, err := store.Load()
		require.NoError(t, err)

		for id, message := range state.ScheduledMessages {
			second.armScheduledMessage(id, &message)
		}

		synctest.Wait()

		select {
		case <-second.requestCh:
			t.Fatal("scheduled message submitted before restored delay")
		default:
		}

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case request := <-second.requestCh:
			require.NotNil(t, request.inbound)
			assert.NotEmpty(t, request.scheduledMessageID)
			assert.Equal(t, "later", request.inbound.Text)
		case <-time.After(time.Nanosecond):
			t.Fatal("restored scheduled message was not submitted")
		}

		state, err = store.Load()
		require.NoError(t, err)
		require.Len(t, state.ScheduledMessages, 1)
		assert.Contains(t, logs.String(), "scheduled message enqueued")
	})
}

func TestBridgeStartLogsRestoredScheduledMessage(t *testing.T) {
	workspace := t.TempDir()
	store, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(context.Background())) })

	require.NoError(t, store.updateState(func(state *State) {
		state.ScheduledMessages = map[string]ScheduledMessageState{"schedule-1": {ConversationID: events.MainConversationID(), Agent: "main", Message: "later", DueAt: time.Now().UTC().Add(time.Hour)}}
	}))

	var logs bytes.Buffer

	bus := events.New()
	t.Cleanup(bus.Close)
	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: events.MainConversationID(), Agent: "main", SessionService: store}, slog.New(slog.NewJSONHandler(&logs, nil)))
	require.NoError(t, bridge.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

	assert.Contains(t, logs.String(), "scheduled message restored")
}

func TestSeedThreadFromMainCompactsMainSessionOnce(t *testing.T) {
	workspace := t.TempDir()
	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	_, err = service.AppendEntryID(context.Background(), events.MainConversationID(), &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), Model: "gpt-5.5", ReplayInput: testReplayInput(replayInputMessage{role: "user", text: "main question"}, replayInputMessage{role: "assistant", text: "main answer"})})
	require.NoError(t, err)

	var (
		mu       sync.Mutex
		compacts int
		model    string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses/compact" {
			http.NotFound(w, r)

			return
		}

		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		mu.Lock()
		compacts++
		model = request.Model
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"cmp_1","type":"compaction","encrypted_content":"sealed"}]}`))
	}))
	t.Cleanup(server.Close)

	bridge := new(Bridge)
	bridge.runtime = &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}
	bridge.config = Config{ConversationID: SlackThreadConversationID("D123", "111.222"), Agent: "main", OutputTargets: events.MainOutputTargets(), SessionService: service}
	bridge.log = slog.New(slog.DiscardHandler)

	require.NoError(t, bridge.SeedThreadFromMain(context.Background()))
	require.NoError(t, bridge.SeedThreadFromMain(context.Background()))

	mu.Lock()
	assert.Equal(t, 1, compacts)
	assert.Equal(t, "gpt-5.5", model)
	mu.Unlock()

	entries, err := service.ObserveEntries(context.Background(), bridge.config.ConversationID, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "main_thread_seed", entries[0].Entry.Type)
	assert.Equal(t, "gpt-5.5", entries[0].Entry.Model)
	params, err := rocketcode.ReplayInputToParams(entries[0].Entry.ReplayInput)
	require.NoError(t, err)
	require.Len(t, params, 1)
	assert.Equal(t, "sealed", params[0].OfCompaction.EncryptedContent)
}

func TestSeedThreadFromMainReturnsWhenMainSessionEmpty(t *testing.T) {
	service := newTestSessionService(t)
	conversationID := SlackThreadConversationID("D123", "111.222")

	bridge := new(Bridge)
	bridge.runtime = &config.Config{Workspace: t.TempDir()}
	bridge.config = Config{ConversationID: conversationID, Agent: "main", OutputTargets: events.MainOutputTargets(), SessionService: service}
	bridge.log = slog.New(slog.DiscardHandler)

	require.NoError(t, bridge.SeedThreadFromMain(context.Background()))

	entries, err := service.ObserveEntries(context.Background(), conversationID, 0)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestSeedThreadFromCronPersistsAssistantSeedOnce(t *testing.T) {
	service := newTestSessionService(t)
	conversationID := SlackThreadConversationID("C123", "111.222")

	bridge := new(Bridge)
	bridge.config = Config{ConversationID: conversationID, Agent: "planner", OutputTargets: events.MainOutputTargets(), SessionService: service}

	require.NoError(t, bridge.SeedThreadFromCron(context.Background(), "cron output"))
	require.NoError(t, bridge.SeedThreadFromCron(context.Background(), "new output ignored"))

	entries, err := service.ObserveEntries(context.Background(), conversationID, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "cron_thread_seed", entries[0].Entry.Type)

	messages, err := replayInputMessages(entries[0].Entry.ReplayInput)
	require.NoError(t, err)
	assert.Equal(t, []replayInputMessage{{role: "assistant", text: "cron output"}}, messages)
}

func TestSeedThreadFromMainReportsThreadSessionLoadFailure(t *testing.T) {
	service, err := NewSessionService(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, service.Stop(context.Background()))

	bridge := new(Bridge)
	bridge.runtime = &config.Config{Workspace: t.TempDir()}
	bridge.config = Config{ConversationID: SlackThreadConversationID("D123", "111.222"), Agent: "main", OutputTargets: events.MainOutputTargets(), SessionService: service}
	bridge.log = slog.New(slog.DiscardHandler)

	err = bridge.SeedThreadFromMain(context.Background())
	require.ErrorContains(t, err, "load Slack thread session")
}

func TestSeedResponseThreadCompactsMainCheckpoint(t *testing.T) {
	workspace := t.TempDir()
	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	id, err := service.AppendEntryID(context.Background(), events.MainConversationID(), &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), ResponseID: "resp-1", Model: "gpt-5.5", ReplayInput: testReplayInput(replayInputMessage{role: "user", text: "main question"}, replayInputMessage{role: "assistant", text: "main answer"})})
	require.NoError(t, err)

	var (
		errRequest  error
		requestBody struct {
			Input []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"input"`
		}
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses/compact" {
			http.NotFound(w, r)

			return
		}

		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			errRequest = err
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"cmp_1","type":"compaction","encrypted_content":"sealed"}]}`))
	}))
	t.Cleanup(server.Close)

	bridge := new(Bridge)
	bridge.runtime = &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}
	bridge.config = Config{ConversationID: SlackThreadConversationID("D123", "111.222"), Agent: "main", OutputTargets: events.MainOutputTargets(), SessionService: service}
	bridge.log = slog.New(slog.DiscardHandler)

	require.NoError(t, bridge.SeedResponseThread(context.Background(), events.ResponseCheckpoint{ConversationID: events.MainConversationID(), SessionEntryID: id, ResponseID: "resp-1", Model: "gpt-5.5", AssistantText: "thread root answer"}, "seed-key"))
	require.NoError(t, errRequest)
	require.Len(t, requestBody.Input, 1)
	assert.Equal(t, "user", requestBody.Input[0].Role)
	assert.Equal(t, "main question", requestBody.Input[0].Content)

	entries, err := service.ObserveEntries(context.Background(), bridge.config.ConversationID, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "response_thread_seed", entries[0].Entry.Type)
	params, err := rocketcode.ReplayInputToParams(entries[0].Entry.ReplayInput)
	require.NoError(t, err)
	require.Len(t, params, 2)
	assert.Equal(t, "sealed", params[0].OfCompaction.EncryptedContent)
	assert.Equal(t, "thread root answer", params[1].OfMessage.Content.OfString.Value)
}

func TestSeedResponseThreadCompactsPriorMainEntriesWithChatGPTInstructions(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: openai/gpt-5.5\n---\nAgent instructions\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	require.NoError(t, oai.SaveToken(workspace, oai.Token{Refresh: "refresh", Access: "access", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acct"}))

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	_, err = service.AppendEntryID(context.Background(), events.MainConversationID(), &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), ResponseID: "resp-1", Model: "gpt-5.5", ReplayInput: testReplayInput(replayInputMessage{role: "user", text: "earlier question"}, replayInputMessage{role: "assistant", text: "earlier answer"})})
	require.NoError(t, err)
	id, err := service.AppendEntryID(context.Background(), events.MainConversationID(), &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(2, 0).UTC(), ResponseID: "resp-2", Model: "gpt-5.5", ReplayInput: testReplayInput(replayInputMessage{role: "user", text: "checkpoint question"}, replayInputMessage{role: "assistant", text: "checkpoint answer"})})
	require.NoError(t, err)

	var requestBody struct {
		Instructions string `json:"instructions"`
		Input        []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
	}

	oldTransport := http.DefaultTransport
	http.DefaultTransport = bridgeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, "/backend-api/codex/responses/compact", req.URL.Path)
		require.NoError(t, json.NewDecoder(req.Body).Decode(&requestBody))

		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"cmp_1","type":"compaction","encrypted_content":"sealed"}]}`)), Request: req}, nil
	})

	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	bridge := new(Bridge)
	bridge.runtime = &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{RocketCodeAuth: "chatgpt"}}
	bridge.config = Config{ConversationID: SlackThreadConversationID("D123", "111.222"), Agent: "main", OutputTargets: events.MainOutputTargets(), SessionService: service}
	bridge.log = slog.New(slog.DiscardHandler)

	require.NoError(t, bridge.SeedResponseThread(context.Background(), events.ResponseCheckpoint{SessionEntryID: id, ResponseID: "resp-2", Model: "gpt-5.5", AssistantText: "thread root answer"}, "seed-key"))
	assert.Equal(t, "Agent instructions", requestBody.Instructions)
	require.Len(t, requestBody.Input, 3)
	assert.Equal(t, "earlier question", requestBody.Input[0].Content)
	assert.Equal(t, "earlier answer", requestBody.Input[1].Content)
	assert.Equal(t, "checkpoint question", requestBody.Input[2].Content)
}

func TestSeedResponseThreadRejectsInvalidCheckpoint(t *testing.T) {
	workspace := t.TempDir()
	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	bridge := new(Bridge)
	bridge.runtime = &config.Config{Workspace: workspace}
	bridge.config = Config{ConversationID: SlackThreadConversationID("D123", "111.222"), Agent: "main", OutputTargets: events.MainOutputTargets(), SessionService: service}
	bridge.log = slog.New(slog.DiscardHandler)

	for _, tt := range []struct {
		name       string
		checkpoint events.ResponseCheckpoint
		wantErr    string
	}{
		{
			name:       "missing session entry",
			checkpoint: events.ResponseCheckpoint{AssistantText: "answer"},
			wantErr:    "response checkpoint session entry ID is required",
		},
		{
			name:       "missing assistant text",
			checkpoint: events.ResponseCheckpoint{SessionEntryID: 1, AssistantText: " "},
			wantErr:    "response checkpoint assistant text is required",
		},
		{
			name:       "missing checkpoint row",
			checkpoint: events.ResponseCheckpoint{ConversationID: events.MainConversationID(), SessionEntryID: 1, AssistantText: "answer"},
			wantErr:    "main session checkpoint entry 1 was not found",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := bridge.SeedResponseThread(context.Background(), tt.checkpoint, "seed-key")
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSeedResponseThreadReturnsWhenThreadAlreadySeeded(t *testing.T) {
	service := newTestSessionService(t)
	conversationID := SlackThreadConversationID("D123", "111.222")
	seed := &rocketcode.SessionEntry{Version: 1, Type: "response_thread_seed", Timestamp: time.Unix(1, 0).UTC(), Model: "gpt-5.5", ReplayInput: testReplayInput(replayInputMessage{role: "assistant", text: "existing seed"})}
	_, err := service.AppendEntryID(context.Background(), conversationID, seed)
	require.NoError(t, err)

	bridge := new(Bridge)
	bridge.runtime = &config.Config{Workspace: t.TempDir()}
	bridge.config = Config{ConversationID: conversationID, Agent: "main", OutputTargets: events.MainOutputTargets(), SessionService: service}
	bridge.log = slog.New(slog.DiscardHandler)

	err = bridge.SeedResponseThread(context.Background(), events.ResponseCheckpoint{SessionEntryID: 1, AssistantText: "thread root answer"}, "seed-key")
	require.NoError(t, err)

	entries, err := service.ObserveEntries(context.Background(), conversationID, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "response_thread_seed", entries[0].Entry.Type)
}

func TestSeedResponseThreadReportsThreadSessionLoadFailure(t *testing.T) {
	service, err := NewSessionService(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, service.Stop(context.Background()))

	bridge := new(Bridge)
	bridge.runtime = &config.Config{Workspace: t.TempDir()}
	bridge.config = Config{ConversationID: SlackThreadConversationID("D123", "111.222"), Agent: "main", OutputTargets: events.MainOutputTargets(), SessionService: service}
	bridge.log = slog.New(slog.DiscardHandler)

	err = bridge.SeedResponseThread(context.Background(), events.ResponseCheckpoint{SessionEntryID: 1, AssistantText: "thread root answer"}, "seed-key")
	require.ErrorContains(t, err, "load response-rooted thread session")
}

func TestOpenAIClientLogsProviderRequestsOnError(t *testing.T) {
	status := http.StatusBadRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status == http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))

			return
		}

		http.Error(w, `{"error":{"message":"blocked"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)

	var logs bytes.Buffer

	cfg := new(config.Config)
	cfg.OpenAI.APIBaseURL = server.URL

	bridge := new(Bridge)
	bridge.runtime = cfg
	bridge.log = slog.New(slog.NewJSONHandler(&logs, nil))

	var params responses.ResponseNewParams

	client, err := bridge.openAIClient()
	require.NoError(t, err)

	_, err = client.Responses.New(context.Background(), params)
	require.Error(t, err)
	assert.Contains(t, logs.String(), "provider request failed")
	assert.Contains(t, logs.String(), `"path":"/responses"`)
	assert.Contains(t, logs.String(), `"status":400`)
	assert.Contains(t, logs.String(), `"error":"provider returned status 400"`)
	logs.Reset()

	status = http.StatusOK
	client, err = bridge.openAIClient()
	require.NoError(t, err)

	_, _ = client.Responses.New(context.Background(), params)

	assert.NotContains(t, logs.String(), "provider request completed")
}

func TestRocketCodeThinkingTextHandlesStructuredToolDiagnostics(t *testing.T) {
	var call, status, hosted, hostedQueries, raw, result rocketcode.ToolDiagnostic

	call.Phase = "call"
	call.Name = "bash"
	call.Arguments = []byte(`{"command":"cat /tmp/file","description":"Read the file"}`)
	status.Phase = "call"
	status.Name = "bash"
	hosted.Phase = "call"
	hosted.Name = "websearch"
	hosted.Status = "started"
	hosted.Action = []byte(`{"type":"search","query":"Google DeepMind blog"}`)
	hostedQueries.Phase = "call"
	hostedQueries.Name = "websearch"
	hostedQueries.Status = "started"
	hostedQueries.Action = []byte(`{"type":"search","queries":["Anthropic news","Google AI blog"]}`)
	raw.Phase = "call"
	raw.Name = "custom"
	raw.Status = "started"
	raw.Arguments = []byte(`plain text`)
	result.Phase = "result"
	result.Name = "bash"
	result.Result = "file contents"

	assert.Equal(t, "Read the file", rocketcodeThinkingText(toolResponse(&call)))
	assert.Equal(t, "bash started", rocketcodeThinkingText(toolResponse(&status)))
	assert.Equal(t, "websearch started: Google DeepMind blog", rocketcodeThinkingText(toolResponse(&hosted)))
	assert.Equal(t, "websearch started: Anthropic news, Google AI blog", rocketcodeThinkingText(toolResponse(&hostedQueries)))
	assert.Equal(t, "custom started: plain text", rocketcodeThinkingText(toolResponse(&raw)))
	assert.Empty(t, rocketcodeThinkingText(toolResponse(&result)))

	result.Result = `tool call denied: permission "bash" has no matching allow rule for subject "pwd". Choose a different action.`
	assert.Equal(t, result.Result, rocketcodeThinkingText(toolResponse(&result)))
}

func TestRocketCodeThinkingTextHandlesToolDiagnosticFallbacks(t *testing.T) {
	assert.Equal(t, "tool started", rocketcodeThinkingText(toolResponse(&rocketcode.ToolDiagnostic{Phase: "call"})))
	assert.Equal(t, "custom", rocketcodeThinkingText(toolResponse(&rocketcode.ToolDiagnostic{Phase: "unknown", Name: " custom "})))
	assert.Equal(t, "tool queued", rocketcodeThinkingText(toolResponse(&rocketcode.ToolDiagnostic{Phase: "call", Status: "queued"})))
	assert.Equal(t, "plain thought", rocketcodeThinkingText(rocketcode.ChatResponse{Text: " plain thought "}))
}

func TestRocketCodeThinkingTextHandlesSubagentToolDiagnostics(t *testing.T) {
	call := rocketcode.ToolDiagnostic{Phase: "call", Name: "bash", Arguments: []byte(`{"command":"cat /tmp/file","description":"Read the file"}`)}
	result := rocketcode.ToolDiagnostic{Phase: "result", Name: "bash", Result: "file contents"}

	assert.Equal(t, "subagent hally-google-workspace assistant tool: bash: Read the file", rocketcodeThinkingText(subagentToolResponse(&call)))
	assert.Empty(t, rocketcodeThinkingText(subagentToolResponse(&result)))
}

func TestRocketCodeThinkingTextSuppressesEmptyNestedSubagentDiagnostics(t *testing.T) {
	response := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:  "alitu-scenario-manager",
			Label: "assistant tool",
			Subagent: &rocketcode.SubagentDiagnostic{
				Name:  "alitu-scenario-manager",
				Label: "assistant tool",
				Tool:  &rocketcode.ToolDiagnostic{Phase: "result", Name: "bash", Result: "file contents"},
			},
		},
	}

	assert.Empty(t, rocketcodeThinkingText(response))
}

func TestRocketCodeThinkingTextSuppressesProviderOnlySubagentDiagnostics(t *testing.T) {
	response := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:  "alitu-scenario-manager",
			Label: "assistant tool",
			Provider: &rocketcode.ProviderDiagnostic{
				Phase:   "retry",
				Attempt: 2,
			},
		},
	}

	assert.Empty(t, rocketcodeThinkingText(response))
}

func TestRocketCodeThinkingTextKeepsExplicitSubagentProviderText(t *testing.T) {
	response := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:     "alitu-scenario-manager",
			Label:    "assistant tool",
			Text:     "provider retrying",
			Provider: &rocketcode.ProviderDiagnostic{Phase: "retry", Attempt: 2},
		},
	}

	assert.Equal(t, "subagent alitu-scenario-manager assistant tool: provider retrying", rocketcodeThinkingText(response))
}

func toolResponse(tool *rocketcode.ToolDiagnostic) rocketcode.ChatResponse {
	var response rocketcode.ChatResponse

	response.Kind = rocketcode.ChatResponseAssistantTool
	response.Tool = tool

	return response
}

func subagentToolResponse(tool *rocketcode.ToolDiagnostic) rocketcode.ChatResponse {
	var response rocketcode.ChatResponse

	response.Kind = rocketcode.ChatResponseAssistantTool
	response.Subagent = &rocketcode.SubagentDiagnostic{Name: "hally-google-workspace", Label: "assistant tool", Tool: tool}

	return response
}

func TestProcessResponsePublishesStructuredToolDiagnosticsAsThinking(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), RequestRestart: testNoopRestart, SessionService: nil}
	inbound := events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	result := runResult{turnID: "turn-1", text: "", thinking: "", sequence: 0, sessionEntryID: 0, responseID: "", model: ""}

	var diagnostic rocketcode.ToolDiagnostic

	diagnostic.Phase = "call"
	diagnostic.Name = "bash"
	diagnostic.Arguments = []byte(`{"command":"cat /tmp/file","description":"Read the file"}`)

	require.NoError(t, bridge.processResponse(context.Background(), inbound, &result, toolResponse(&diagnostic)))

	outbound := readRocketCodeOutbound(t, bus)
	assert.Equal(t, "Read the file", outbound.SlackThinking)
	assert.Equal(t, "turn-1", outbound.TurnID)
}

func TestProcessResponseSuppressesProviderOnlySubagentDiagnostics(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.config = Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), RequestRestart: testNoopRestart, SessionService: nil}
	inbound := events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	result := runResult{turnID: "turn-1", text: "", thinking: "", sequence: 0, sessionEntryID: 0, responseID: "", model: ""}
	item := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:     "alitu-scenario-manager",
			Label:    "assistant tool",
			Provider: &rocketcode.ProviderDiagnostic{Phase: "retry", Attempt: 2},
		},
	}

	require.NoError(t, bridge.processResponse(context.Background(), inbound, &result, item))
	assert.Empty(t, result.thinking)
	assert.Zero(t, result.sequence)
}

func TestRunTurnSendsExternalMCPMetadataAsDeveloperMessage(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "planner", "---\ndescription: Planner\nmode: primary\nmodel: openai/gpt-5.5\npermission:\n  bash:\n    \"*\": allow\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var (
		requestBody struct {
			Input []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
				Output  any    `json:"output"`
			} `json:"input"`
		}
		errRequest error
		requests   int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errRequest = assert.AnError

			http.NotFound(w, r)

			return
		}

		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			errRequest = err
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		requests++

		w.Header().Set("Content-Type", "application/json")

		if requests == 2 {
			_, _ = w.Write([]byte(`{"id":"resp_2","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"call_1","type":"function_call","status":"completed","call_id":"call_1","name":"bash","arguments":"{\"command\":\"printf '%s|%s|%s' \\\"$ROCKETCLAW_METADATA_A\\\" \\\"$ROCKETCLAW_METADATA_LATER_KEY\\\" \\\"$ROCKETCLAW_METADATA_Z\\\"\",\"description\":\"check env\"}"}]}`))

			return
		}

		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	bridge := new(Bridge)
	bridge.runtime = &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}
	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	bridge.config = Config{ConversationID: "external_mcp:planner:abc", Agent: "planner", OutputTargets: events.MainOutputTargets(), SessionService: service}
	bridge.log = slog.New(slog.DiscardHandler)

	msg := events.NewMainInboundMessage(events.SourceExternalMCP, events.InboundKindPrompt, "", "hello", true)
	msg.Metadata = map[string]string{"z": "last", "a": "first"}

	result, err := bridge.runTurn(context.Background(), msg, "turn-1", false)
	require.NoError(t, err)
	require.NoError(t, errRequest)
	assert.Equal(t, "ok", result.text)
	require.Len(t, requestBody.Input, 2)
	assert.Equal(t, "developer", requestBody.Input[0].Role)
	assert.Equal(t, "This external MCP thread has metadata:\nROCKETCLAW_CONVERSATION_ID=\"external_mcp:planner:abc\"\nROCKETCLAW_METADATA_A=\"first\"\nROCKETCLAW_METADATA_Z=\"last\"", requestBody.Input[0].Content)
	assert.Equal(t, "user", requestBody.Input[1].Role)

	msg.Metadata = map[string]string{"a": "ignored", "later-key": "fresh"}
	_, err = bridge.runTurn(context.Background(), msg, "turn-2", false)
	require.NoError(t, err)

	developerMessages := []string{}

	for i := range requestBody.Input {
		if requestBody.Input[i].Role == "developer" {
			developerMessages = append(developerMessages, requestBody.Input[i].Content)
		}
	}

	assert.Contains(t, developerMessages, "This external MCP thread has metadata:\nROCKETCLAW_CONVERSATION_ID=\"external_mcp:planner:abc\"\nROCKETCLAW_METADATA_A=\"first\"\nROCKETCLAW_METADATA_Z=\"last\"")
	assert.Contains(t, developerMessages, "This external MCP turn has additional metadata:\nROCKETCLAW_METADATA_LATER_KEY=\"fresh\"")

	for _, content := range developerMessages {
		assert.NotContains(t, content, "ignored")
	}

	require.NotEmpty(t, requestBody.Input)
	assert.Equal(t, "first|fresh|last", requestBody.Input[len(requestBody.Input)-1].Output)

	msg.Metadata = map[string]string{"a": "ignored"}
	_, err = bridge.runTurn(context.Background(), msg, "turn-3", false)
	require.NoError(t, err)

	for i := range requestBody.Input {
		assert.NotContains(t, requestBody.Input[i].Content, "LATER_KEY")
	}

	entries, err := service.ObserveEntries(context.Background(), bridge.config.ConversationID, 0)
	require.NoError(t, err)

	metadataEntries := 0

	for i := range entries {
		if entries[i].Entry.Type == externalMCPMetadataEntryType {
			metadataEntries++
		}
	}

	assert.Equal(t, 1, metadataEntries)
}

func TestExternalMCPMetadataDeveloperMessageSorted(t *testing.T) {
	env := externalMCPMetadataEnv("external_mcp:planner:abc", map[string]string{"ticket-id": "123", "owner": "alice"})
	assert.Equal(t, "This external MCP thread has metadata:\nROCKETCLAW_CONVERSATION_ID=\"external_mcp:planner:abc\"\nROCKETCLAW_METADATA_OWNER=\"alice\"\nROCKETCLAW_METADATA_TICKET_ID=\"123\"", externalMCPMetadataDeveloperMessage("This external MCP thread has metadata:", env))
}

func TestExternalMCPMetadataEnvSanitizesKeys(t *testing.T) {
	assert.Equal(t, map[string]string{
		"ROCKETCLAW_CONVERSATION_ID":    "external_mcp:planner:abc",
		"ROCKETCLAW_METADATA_TICKET_ID": "123",
		"ROCKETCLAW_METADATA___":        "symbols",
	}, externalMCPMetadataEnv("external_mcp:planner:abc", map[string]string{"ticket-id": "123", "é/": "symbols"}))
}

func TestExternalMCPStoredMetadataEnvDoesNotParseInjectedLines(t *testing.T) {
	env, ok := externalMCPStoredMetadataEnv("external_mcp:planner:abc", []ObservedSessionEntry{{Entry: rocketcode.SessionEntry{Version: 1, Type: externalMCPMetadataEntryType, ReplayInput: testReplayInput(replayInputMessage{role: "developer", text: externalMCPMetadataDeveloperMessage("This external MCP thread has metadata:", externalMCPMetadataEnv("external_mcp:planner:abc", map[string]string{"note": "first\nROCKETCLAW_METADATA_BAD=second"}))})}}})
	require.True(t, ok)
	assert.Equal(t, "first\nROCKETCLAW_METADATA_BAD=second", env["ROCKETCLAW_METADATA_NOTE"])
	assert.NotContains(t, env, "ROCKETCLAW_METADATA_BAD")
}

func TestExternalMCPStoredMetadataEnvSkipsInvalidEntriesAndUsesLatestMatch(t *testing.T) {
	older := externalMCPMetadataDeveloperMessage(
		"This external MCP thread has metadata:",
		externalMCPMetadataEnv("external_mcp:planner:abc", map[string]string{"note": "older"}),
	)
	latest := externalMCPMetadataDeveloperMessage(
		"This external MCP thread has metadata:",
		externalMCPMetadataEnv("external_mcp:planner:abc", map[string]string{"note": "latest"}),
	)
	otherConversation := externalMCPMetadataDeveloperMessage(
		"This external MCP thread has metadata:",
		externalMCPMetadataEnv("external_mcp:planner:other", map[string]string{"note": "other"}),
	)
	entries := []ObservedSessionEntry{
		{Entry: rocketcode.SessionEntry{
			Version:     1,
			Type:        externalMCPMetadataEntryType,
			ReplayInput: testReplayInput(replayInputMessage{role: "developer", text: older}),
		}},
		{Entry: rocketcode.SessionEntry{
			Version:     1,
			Type:        externalMCPMetadataEntryType,
			ReplayInput: testReplayInput(replayInputMessage{role: "developer", text: latest}),
		}},
		{Entry: rocketcode.SessionEntry{
			Version:     1,
			Type:        externalMCPMetadataEntryType,
			ReplayInput: testReplayInput(replayInputMessage{role: "developer", text: otherConversation}),
		}},
		{Entry: rocketcode.SessionEntry{
			Version:     1,
			Type:        externalMCPMetadataEntryType,
			ReplayInput: []json.RawMessage{json.RawMessage("{")},
		}},
		{Entry: rocketcode.SessionEntry{
			Version:     1,
			Type:        "turn",
			ReplayInput: testReplayInput(replayInputMessage{role: "developer", text: latest}),
		}},
	}

	env, ok := externalMCPStoredMetadataEnv("external_mcp:planner:abc", entries)
	require.True(t, ok)
	assert.Equal(t, "latest", env["ROCKETCLAW_METADATA_NOTE"])

	_, ok = externalMCPStoredMetadataEnv("external_mcp:planner:missing", entries)
	assert.False(t, ok)
}

func TestNewOutboundMessageRoutesBrowserVoiceToWebUIWithoutDiscord(t *testing.T) {
	bridge := new(Bridge)
	bridge.config = Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), RequestRestart: testNoopRestart, SessionService: nil}

	inbound := events.NewMainInboundMessage(events.SourceWebVoice, events.InboundKindPrompt, "", "hello", true)
	inbound.WebSessionID = "browser-session-1"

	outbound := bridge.newOutboundMessage(inbound, "turn-1", 1, "reply", "", true)

	assert.Equal(t, "browser-session-1", outbound.WebSessionID)
	assert.Contains(t, outbound.Targets, events.OutputTargetSlackMain)
	assert.Contains(t, outbound.Targets, events.OutputTargetWebUI)
	assert.NotContains(t, outbound.Targets, events.OutputTargetDiscord)
}

func TestProcessResponseKeepsWebUITargetForBrowserThinking(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: events.MainConversationID(), Agent: "main", ConsumeSharedInbound: false, OutputTargets: events.MainOutputTargets(), RequestRestart: testNoopRestart, SessionService: nil}
	inbound := events.NewMainInboundMessage(events.SourceWebVoice, events.InboundKindPrompt, "", "hello", true)
	inbound.WebSessionID = "browser-session-1"
	result := runResult{turnID: "turn-1", text: "", thinking: "", sequence: 0}

	var diagnostic rocketcode.ToolDiagnostic

	diagnostic.Phase = "call"
	diagnostic.Name = "bash"
	diagnostic.Arguments = []byte(`{"command":"cat /tmp/file","description":"Read the file"}`)

	var item rocketcode.ChatResponse

	item.Kind = rocketcode.ChatResponseAssistantTool
	item.Tool = &diagnostic

	require.NoError(t, bridge.processResponse(context.Background(), inbound, &result, item))
	outbound := readRocketCodeOutbound(t, bus)
	assert.Contains(t, outbound.Targets, events.OutputTargetSlackMain)
	assert.Contains(t, outbound.Targets, events.OutputTargetWebUI)
	assert.Equal(t, "browser-session-1", outbound.WebSessionID)
}
func readRocketCodeOutbound(t *testing.T, bus *events.Bus) *events.OutboundMessage {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	for msg := range bus.Outbound(ctx) {
		return msg
	}

	t.Fatal("timed out waiting for outbound message")

	return nil
}
