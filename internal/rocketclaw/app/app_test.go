package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func outboundLoop(
	ctx context.Context,
	bus *events.Bus,
	slackSend func(context.Context, *events.OutboundMessage) error,
	discordSend func(context.Context, *events.OutboundMessage) error,
	webSend func(context.Context, *events.OutboundMessage) error,
	logger *slog.Logger,
) error {
	return outboundLoopWithDiscordText(ctx, bus, slackSend, discardOutboundSend, discordSend, webSend, logger)
}

func discardOutboundSend(context.Context, *events.OutboundMessage) error {
	return nil
}

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

func TestConfiguredMainOutputTargetsSelectsPrimaryTextConnector(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want []events.OutputTarget
	}{
		{name: "slack", cfg: config.Config{Slack: config.SlackConfig{Enabled: true}}, want: []events.OutputTarget{events.OutputTargetSlackMain}},
		{name: "discord text", cfg: config.Config{DiscordText: config.DiscordTextConfig{Enabled: true}}, want: []events.OutputTarget{events.OutputTargetDiscordText}},
		{name: "discord text and voice", cfg: config.Config{DiscordText: config.DiscordTextConfig{Enabled: true}, DiscordVoice: config.DiscordVoiceConfig{Enabled: true}}, want: []events.OutputTarget{events.OutputTargetDiscordText, events.OutputTargetDiscord}},
		{name: "voice only", cfg: config.Config{DiscordVoice: config.DiscordVoiceConfig{Enabled: true}}, want: []events.OutputTarget{events.OutputTargetDiscord}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, configuredMainOutputTargets(&tt.cfg))
		})
	}
}

func TestTerminalRendererPrintsEvent(t *testing.T) {
	var out bytes.Buffer

	renderer := terminalRenderer{out: &out}

	renderer.printLine("event")

	assert.Equal(t, "event\n", out.String())
}

func TestTerminalRendererSuppressesConsecutiveDuplicateAssistantLine(t *testing.T) {
	var out bytes.Buffer

	renderer := terminalRenderer{out: &out}

	renderer.printLine("[assistant] hello")
	renderer.printLine("[assistant] hello")
	renderer.printLine("[you] ok")
	renderer.printLine("[assistant] hello")

	assert.Equal(t, "[assistant] hello\n[you] ok\n[assistant] hello\n", out.String())
}

func TestTerminalCLISummaryPromptDefaultsNo(t *testing.T) {
	var out bytes.Buffer

	input := strings.NewReader("\n")
	called := false
	cli := terminalCLI{renderer: &terminalRenderer{out: &out}, reader: bufio.NewReader(input), summarize: func(context.Context, string) (string, error) {
		called = true
		return "", nil
	}}

	cli.offerSummary()

	assert.False(t, called)
}

func TestTerminalCLISummaryPromptPublishesInternalizedMainNote(t *testing.T) {
	var out bytes.Buffer

	input := strings.NewReader("y\n")

	var published *events.InboundMessage

	cli := terminalCLI{
		renderer: &terminalRenderer{out: &out},
		reader:   bufio.NewReader(input),
		summarize: func(context.Context, string) (string, error) {
			return "session summary", nil
		},
		publishMain: func(_ context.Context, msg *events.InboundMessage) error {
			published = msg
			return nil
		},
	}

	cli.offerSummary()

	require.NotNil(t, published)
	assert.Equal(t, events.SourceTerminalCLI, published.Source)
	assert.Equal(t, events.InboundKindInternalize, published.Kind)
	assert.Equal(t, events.MainConversationID(), published.ConversationID)
	assert.Equal(t, "session summary", published.Text)
}

func TestTerminalCLISummaryPromptDoesNotPublishEmptySummary(t *testing.T) {
	var out bytes.Buffer

	input := strings.NewReader("y\n")
	called := false

	cli := terminalCLI{
		renderer: &terminalRenderer{out: &out},
		reader:   bufio.NewReader(input),
		summarize: func(context.Context, string) (string, error) {
			return " \n ", nil
		},
		publishMain: func(context.Context, *events.InboundMessage) error {
			called = true
			return nil
		},
	}

	cli.offerSummary()

	assert.False(t, called)
	assert.Contains(t, out.String(), "summary was empty")
}

func TestTerminalCLISummaryPromptDoesNotPublishOnSummaryError(t *testing.T) {
	var out bytes.Buffer

	input := strings.NewReader("y\n")
	called := false

	cli := terminalCLI{
		renderer: &terminalRenderer{out: &out},
		reader:   bufio.NewReader(input),
		summarize: func(context.Context, string) (string, error) {
			return "", assert.AnError
		},
		publishMain: func(context.Context, *events.InboundMessage) error {
			called = true
			return nil
		},
	}

	cli.offerSummary()

	assert.False(t, called)
	assert.Contains(t, out.String(), "summary failed")
}

func TestTerminalCLIExitCanReadPipedSummaryAnswer(t *testing.T) {
	var out bytes.Buffer

	input := strings.NewReader("/exit\ny\n")

	var published *events.InboundMessage

	cli := terminalCLI{
		renderer:        &terminalRenderer{out: &out},
		reader:          bufio.NewReader(input),
		newConversation: true,
		onExit:          func() {},
		submit:          func(context.Context, *events.InboundMessage) error { return nil },
		summarize: func(context.Context, string) (string, error) {
			return "session summary", nil
		},
		publishMain: func(_ context.Context, msg *events.InboundMessage) error {
			published = msg
			return nil
		},
	}

	cli.readInput(context.Background())

	require.NotNil(t, published)
	assert.Equal(t, "session summary", published.Text)
}

func TestControlServerSocketModesAndCleanup(t *testing.T) {
	workspace := shortTempDir(t)
	cfg := &config.Config{Workspace: workspace}

	bus := events.New()
	defer bus.Close()

	store := newAppTestSessionService(t, workspace)
	manager := newThreadBridgeManager(bus, cfg, store, testLogger(), func(bridgeConfig) directBridge { return new(fakeDirectBridge) })

	server, err := startControlServer(t.Context(), cfg, bus, store, manager, newControlQuestionHub(), testLogger())
	require.NoError(t, err)

	socketPath := ControlSocketPath(cfg)
	parentInfo, err := os.Stat(filepath.Dir(socketPath))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), parentInfo.Mode().Perm())

	socketInfo, err := os.Lstat(socketPath)
	require.NoError(t, err)
	assert.NotZero(t, socketInfo.Mode()&os.ModeSocket)
	assert.Equal(t, os.FileMode(0o600), socketInfo.Mode().Perm())

	require.NoError(t, server.Close(t.Context()))

	_, err = os.Lstat(socketPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestControlServerDoesNotRemoveNonSocketPath(t *testing.T) {
	workspace := shortTempDir(t)
	cfg := &config.Config{Workspace: workspace}
	socketPath := ControlSocketPath(cfg)
	require.NoError(t, os.MkdirAll(filepath.Dir(socketPath), 0o700))
	require.NoError(t, os.WriteFile(socketPath, []byte("not a socket"), 0o600))

	bus := events.New()
	defer bus.Close()

	store := newAppTestSessionService(t, workspace)
	manager := newThreadBridgeManager(bus, cfg, store, testLogger(), func(bridgeConfig) directBridge { return new(fakeDirectBridge) })

	_, err := startControlServer(t.Context(), cfg, bus, store, manager, newControlQuestionHub(), testLogger())
	require.ErrorContains(t, err, "not a socket")
	data, err := os.ReadFile(socketPath)
	require.NoError(t, err)
	assert.Equal(t, "not a socket", string(data))
}

func TestControlServerNewConversationPersistsAgent(t *testing.T) {
	workspace := shortTempDir(t)
	cfg := &config.Config{Workspace: workspace}

	bus := events.New()
	defer bus.Close()

	store := newAppTestSessionService(t, workspace)
	manager := newThreadBridgeManager(bus, cfg, store, testLogger(), func(bridgeConfig) directBridge { return new(fakeDirectBridge) })
	server, err := startControlServer(t.Context(), cfg, bus, store, manager, newControlQuestionHub(), testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(t.Context())) }()

	conn, err := net.Dial("unix", ControlSocketPath(cfg))
	require.NoError(t, err)

	defer func() { require.NoError(t, conn.Close()) }()

	require.NoError(t, json.NewEncoder(conn).Encode(controlRequest{Type: "new", Agent: "planner"}))

	var msg controlMessage
	require.NoError(t, json.NewDecoder(conn).Decode(&msg))
	require.Equal(t, "attached", msg.Type)

	state, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, harnessbridge.ThreadState{Agent: "planner"}, state.Threads[msg.ConversationID])
}

func TestControlServerAttachRejectsUnknownConversation(t *testing.T) {
	workspace := shortTempDir(t)
	cfg := &config.Config{Workspace: workspace}

	bus := events.New()
	defer bus.Close()

	store := newAppTestSessionService(t, workspace)
	manager := newThreadBridgeManager(bus, cfg, store, testLogger(), func(bridgeConfig) directBridge { return new(fakeDirectBridge) })
	server, err := startControlServer(t.Context(), cfg, bus, store, manager, newControlQuestionHub(), testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(t.Context())) }()

	conn, err := net.Dial("unix", ControlSocketPath(cfg))
	require.NoError(t, err)

	defer func() { require.NoError(t, conn.Close()) }()

	require.NoError(t, json.NewEncoder(conn).Encode(controlRequest{Type: "attach", ConversationID: "cli:missing"}))

	var msg controlMessage
	require.NoError(t, json.NewDecoder(conn).Decode(&msg))
	assert.Equal(t, "error", msg.Type)
	assert.Contains(t, msg.Text, "not an existing server-owned conversation")
}

func TestRunControlClientExitStopsReadingPipedInput(t *testing.T) {
	workspace := shortTempDir(t)
	cfg := &config.Config{Workspace: workspace}
	socketPath := ControlSocketPath(cfg)
	require.NoError(t, os.MkdirAll(filepath.Dir(socketPath), 0o700))

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	defer func() { _ = listener.Close() }()

	requests := make(chan controlRequest, 3)
	done := make(chan error, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- fmt.Errorf("accept control client: %w", err)
			return
		}

		defer func() { _ = conn.Close() }()

		enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)

		var req controlRequest
		if err := dec.Decode(&req); err != nil {
			done <- fmt.Errorf("decode attach: %w", err)
			return
		}

		requests <- req

		if err := enc.Encode(controlMessage{Type: "attached", ConversationID: req.ConversationID}); err != nil {
			done <- fmt.Errorf("encode attached: %w", err)
			return
		}

		if err := dec.Decode(&req); err != nil {
			done <- fmt.Errorf("decode exit: %w", err)
			return
		}

		requests <- req

		_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))

		if err := dec.Decode(&req); err == nil {
			requests <- req

			done <- fmt.Errorf("unexpected request after exit: %s", req.Type)

			return
		}

		done <- nil
	}()

	err = RunControlClient(t.Context(), cfg, CLIOptions{In: strings.NewReader("/exit\nignored\n"), Out: new(bytes.Buffer)})
	require.NoError(t, err)
	require.NoError(t, <-done)
	assert.Equal(t, "attach", (<-requests).Type)
	assert.Equal(t, "exit", (<-requests).Type)
}

func TestRunControlClientEOFPresentsSummaryPromptForNewConversation(t *testing.T) {
	workspace := shortTempDir(t)
	cfg := &config.Config{Workspace: workspace}
	socketPath := ControlSocketPath(cfg)
	require.NoError(t, os.MkdirAll(filepath.Dir(socketPath), 0o700))

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	defer func() { _ = listener.Close() }()

	requests := make(chan controlRequest, 3)
	done := make(chan error, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- fmt.Errorf("accept control client: %w", err)
			return
		}

		defer func() { _ = conn.Close() }()

		enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)

		var req controlRequest
		if err := dec.Decode(&req); err != nil {
			done <- fmt.Errorf("decode new: %w", err)
			return
		}

		requests <- req

		if err := enc.Encode(controlMessage{Type: "attached", ConversationID: "cli:new"}); err != nil {
			done <- fmt.Errorf("encode attached: %w", err)
			return
		}

		if err := dec.Decode(&req); err != nil {
			done <- fmt.Errorf("decode exit: %w", err)
			return
		}

		requests <- req

		done <- nil
	}()

	out := new(bytes.Buffer)
	err = RunControlClient(t.Context(), cfg, CLIOptions{In: strings.NewReader(""), Out: out, NewConversation: true})
	require.NoError(t, err)
	require.NoError(t, <-done)
	assert.Equal(t, "new", (<-requests).Type)
	exit := <-requests
	assert.Equal(t, "exit", exit.Type)
	assert.False(t, exit.Summarize)
	assert.Contains(t, out.String(), "Append a summary")
}

func TestRunControlClientAnswersQuestion(t *testing.T) {
	workspace := shortTempDir(t)
	cfg := &config.Config{Workspace: workspace}
	socketPath := ControlSocketPath(cfg)
	require.NoError(t, os.MkdirAll(filepath.Dir(socketPath), 0o700))

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	defer func() { _ = listener.Close() }()

	requests := make(chan controlRequest, 3)
	done := make(chan error, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}

		defer func() { _ = conn.Close() }()

		enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)

		var req controlRequest
		if err := dec.Decode(&req); err != nil {
			done <- err
			return
		}

		requests <- req

		if err := enc.Encode(controlMessage{Type: "attached", ConversationID: req.ConversationID}); err != nil {
			done <- err
			return
		}

		if err := enc.Encode(controlMessage{Type: "question", Question: &events.AskUserQuestionRequest{ID: "q1", Question: "Deploy?", Details: "Production", Options: []events.AskUserQuestionOption{{Label: "Yes", Value: "yes", Description: "Ship it"}}}}); err != nil {
			done <- err
			return
		}

		if err := dec.Decode(&req); err != nil {
			done <- err
			return
		}

		requests <- req

		done <- nil
	}()

	out := new(bytes.Buffer)
	err = RunControlClient(t.Context(), cfg, CLIOptions{In: strings.NewReader("1\n"), Out: out})
	require.NoError(t, err)
	require.NoError(t, <-done)
	assert.Equal(t, "attach", (<-requests).Type)
	answer := <-requests
	assert.Equal(t, "question_answer", answer.Type)
	assert.Equal(t, "q1", answer.QuestionID)
	assert.Equal(t, []string{"yes"}, answer.Answer.Selected)
	assert.Contains(t, out.String(), "Deploy?")
}

func TestTerminalCLINewOpensCMUXSurfaceWithServerConversation(t *testing.T) {
	t.Setenv("CMUX_WORKSPACE_ID", "caller-workspace")
	t.Setenv("CMUX_SURFACE_ID", "caller-surface")
	t.Setenv("CMUX_WORKING_DIRECTORY", "/work/caller")

	runner := &fakeCMUXRunner{outputs: []string{"", "new-surface-1"}}
	cli := terminalCLI{
		renderer: &terminalRenderer{out: new(bytes.Buffer)},
		cmux:     runner.Run,
		newConversationID: func(_ context.Context, agent string) (string, error) {
			assert.Equal(t, "planner", agent)
			return "cli:server-created", nil
		},
	}

	err := cli.openCMUXConversation(t.Context(), "planner")
	require.NoError(t, err)
	require.Len(t, runner.calls, 3)
	assert.Equal(t, []string{"identify"}, runner.calls[0])
	assert.Equal(t, []string{"new-surface", "--type", "terminal", "--working-directory", "/work/caller", "--focus", "true", "--workspace", "caller-workspace"}, runner.calls[1])
	assert.Equal(t, []string{"send", "--surface", "new-surface-1", "rocketclaw cli --attach cli:server-created\n"}, runner.calls[2])
}

func TestTerminalCLINewReportsNonCMUXLocalError(t *testing.T) {
	out := new(bytes.Buffer)
	cli := terminalCLI{
		renderer: &terminalRenderer{out: out},
		cmux:     (&fakeCMUXRunner{err: errors.New("cmux not found")}).Run,
		newConversationID: func(context.Context, string) (string, error) {
			t.Fatal("non-cmux /new requested a conversation")
			return "", nil
		},
	}

	handled, err := cli.handleSlashCommand(t.Context(), "/new")
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Contains(t, out.String(), "/new only opens a new cmux terminal")
	assert.Contains(t, out.String(), "rocketclaw cli --new [agent]")
}

func TestTerminalCLIUnknownSlashCommandIsLocalError(t *testing.T) {
	out := new(bytes.Buffer)
	cli := terminalCLI{
		renderer: &terminalRenderer{out: out},
		cmux:     (&fakeCMUXRunner{}).Run,
		newConversationID: func(context.Context, string) (string, error) {
			t.Fatal("unknown slash command requested a conversation")
			return "", nil
		},
	}

	handled, err := cli.handleSlashCommand(t.Context(), "/fork")
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Contains(t, out.String(), "unknown command /fork")
}

func TestTerminalCLIHelpSlashCommandIsLocal(t *testing.T) {
	out := new(bytes.Buffer)
	cli := terminalCLI{
		renderer: &terminalRenderer{out: out},
		cmux:     (&fakeCMUXRunner{}).Run,
		newConversationID: func(context.Context, string) (string, error) {
			t.Fatal("help slash command requested a conversation")
			return "", nil
		},
	}

	handled, err := cli.handleSlashCommand(t.Context(), "/help")
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Contains(t, out.String(), "/exit")
	assert.Contains(t, out.String(), "/new [agent]")
}

type fakeCMUXRunner struct {
	outputs []string
	err     error
	calls   [][]string
}

func (r *fakeCMUXRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if r.err != nil {
		return "", r.err
	}

	output := ""
	if len(r.outputs) > 0 {
		output = r.outputs[0]
		r.outputs = r.outputs[1:]
	}

	return output, nil
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
	msg.ProgressText = "first thought"
	msg.TurnID = "turn-1"
	require.NoError(t, bus.PublishOutbound(context.Background(), msg))
	assert.Equal(t, "first thought", (<-slackSeen).ProgressText)
	assert.Equal(t, "first thought", (<-discordSeen).ProgressText)
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
	workspace := shortTempDir(t)

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

func TestRunContextCancellationWaitsForActiveMainBridge(t *testing.T) {
	workspace := shortTempDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "main.md"), []byte("---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\n---\nPrompt\n"), 0o644))

	requestStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	providerCanceled := make(chan error, 1)

	var requestOnce sync.Once

	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		requestOnce.Do(func() { close(requestStarted) })

		select {
		case <-releaseProvider:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q,"annotations":[]}]}]}`, "after shutdown requested")
		case <-r.Context().Done():
			providerCanceled <- r.Context().Err()
		}
	}))
	t.Cleanup(openai.Close)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	listenAddr := listener.Addr().String()
	require.NoError(t, listener.Close())

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: openai.URL}}
	cfg.MCPExternal.Enabled = true
	cfg.MCPExternal.ListenAddr = listenAddr

	done := make(chan error, 1)

	go func() {
		done <- Run(ctx, cfg, filepath.Join(workspace, "rocketclaw.json"), testLogger())
	}()

	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", listenAddr, 10*time.Millisecond)
		if err != nil {
			return false
		}

		_ = conn.Close()

		return true
	}, 5*time.Second, 10*time.Millisecond)

	mcpDone := make(chan error, 1)

	go func() {
		reply, err := callSessionPromptForAgent(t.Context(), "http://"+listenAddr+externalmcp.ExternalMCPPath, "main", "hello", nil)
		if err == nil {
			assert.Equal(t, "after shutdown requested", reply.answer)
		}

		mcpDone <- err
	}()

	select {
	case <-requestStarted:
	case err := <-done:
		require.Failf(t, "runtime stopped before provider request started", "err=%v", err)
	case err := <-mcpDone:
		require.Failf(t, "MCP request failed before provider request started", "err=%v", err)
	case <-time.After(5 * time.Second):
		require.Fail(t, "provider request did not start")
	}

	cancel()

	select {
	case err := <-done:
		require.Failf(t, "shutdown returned before active bridge completed", "err=%v", err)
	case err := <-providerCanceled:
		require.Failf(t, "active provider request was canceled", "err=%v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseProvider)

	select {
	case err := <-mcpDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.Fail(t, "MCP request did not finish")
	}

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.Fail(t, "runtime did not stop")
	}
}

func TestRunReturnsErrRestartRequestedAfterCronRestartTool(t *testing.T) {
	workspace := shortTempDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "main.md"), []byte("---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission:\n  rocketclaw:\n    '*': allow\n---\nPrompt\n"), 0o644))
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
		case 4:
			_, err := w.Write([]byte(`{"id":"resp_4","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"","annotations":[]}]}]}`))
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

	assert.Equal(t, 4, requests)
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
	slackRelay := func(relayCtx context.Context, text string, attachments []events.OutboundAttachment, replyTarget *events.InboundMessage, channel string) (*events.InboundMessage, error) {
		_ = relayCtx
		_ = attachments
		_ = replyTarget
		_ = channel

		relayText = text

		return appTestSlackReply(&events.SlackReplyTarget{ChannelID: "D123", MessageTS: "123.456", ThreadTS: ""}), nil
	}

	cfg := new(config.Config)
	cfg.ThreadAgents = config.ThreadAgents{":z:": {Agent: "planner", PreSeed: true}, ":factory:": {Agent: "planner", PreSeed: true}}
	cfg.MCPExternal.Enabled = true
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	store := newAppTestSessionService(t, t.TempDir())

	server, err := startExternalMCPServer(ctx, cfg, slackRelay, inertExternalMCPCleanup, nil, []string{"planner"}, "planner", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	reply, err := callSessionPromptForAgent(ctx, server.URL(), "", "hello", map[string]string{"ticket": "123", "owner": "alice"})
	require.NoError(t, err)
	assert.Equal(t, "planner answer", reply.answer)
	assert.Equal(t, "planner", reply.usedAgent)
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

	reply, err := callMCPTool(ctx, server.URL(), map[string]any{
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
	assert.Equal(t, "planner", reply.usedAgent)

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

	_, err = callMCPTool(ctx, server.URL(), map[string]any{
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

	reply, err := callMCPTool(ctx, server.URL(), map[string]any{"external_conversation_id": externalConversationID, "input": "follow up", "metadata": map[string]string{"ticket": "123"}})
	require.NoError(t, err)
	assert.Equal(t, "follow-up answer", reply.answer)
	assert.Equal(t, "planner", reply.usedAgent)
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

	reply, err := callMCPTool(ctx, server.URL(), map[string]any{"external_conversation_id": externalConversationID, "input": "start"})
	require.NoError(t, err)
	assert.Equal(t, "created answer", reply.answer)
	assert.Equal(t, "main", reply.usedAgent)
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
	writeAppTestAgent(t, workspace, "planner", "---\ndescription: Planner\nmode: primary\nmodel: gpt-5.5\n---\nPrompt\n")
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
	threadBridges := newThreadBridgeManager(bus, nil, rocketcodeSessions, testLogger(), func(bridgeConfig bridgeConfig) directBridge {
		return harnessbridge.NewConversation(cfg, bus, &harnessbridge.Config{ConversationID: bridgeConfig.ConversationID, Agent: bridgeConfig.Agent, ConsumeSharedInbound: false, OutputTargets: bridgeConfig.OutputTargets, StartNewThread: inertStartNewThread, SessionService: rocketcodeSessions}, testLogger())
	})

	defer func() { require.NoError(t, threadBridges.Stop()) }()

	server, err := startExternalMCPServer(ctx, cfg, func(relayCtx context.Context, text string, attachments []events.OutboundAttachment, replyTarget *events.InboundMessage, channel string) (*events.InboundMessage, error) {
		_ = relayCtx
		_ = text
		_ = attachments
		_ = channel

		if replyTarget != nil {
			return appTestSlackReply(&events.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333", ThreadTS: "123.456"}), nil
		}

		return appTestSlackReply(&events.SlackReplyTarget{ChannelID: "D123", MessageTS: "123.456", ThreadTS: ""}), nil
	}, inertExternalMCPCleanup, nil, []string{"planner"}, "planner", rocketcodeSessions, threadBridges.SubmitExternalMCP, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	first, err := callSessionPromptForAgent(ctx, server.URL(), "", "first", map[string]string{"ticket": "123"})
	require.NoError(t, err)
	assert.Equal(t, "first answer", first.answer)
	assert.Equal(t, "planner", first.usedAgent)

	firstSlack := <-slackSeen
	require.NotNil(t, firstSlack.SlackReply)
	assert.Equal(t, events.SlackReplyTarget{ChannelID: "D123", MessageTS: "123.456", ThreadTS: "123.456"}, *firstSlack.SlackReply)

	second, err := callMCPTool(ctx, server.URL(), map[string]any{"external_conversation_id": first.externalConversationID, "input": "second", "metadata": map[string]string{"ticket": "456"}})
	require.NoError(t, err)
	assert.Equal(t, "second answer", second.answer)
	assert.Equal(t, "planner", second.usedAgent)
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

	server, err := startExternalMCPServer(ctx, cfg, func(relayCtx context.Context, text string, attachments []events.OutboundAttachment, replyTarget *events.InboundMessage, channel string) (*events.InboundMessage, error) {
		_ = relayCtx
		_ = attachments
		_ = replyTarget
		_ = channel

		relayText = text

		return appTestSlackReply(&events.SlackReplyTarget{ChannelID: "D123", MessageTS: "123.456", ThreadTS: ""}), nil
	}, inertExternalMCPCleanup, nil, []string{"main"}, "main", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	reply, err := callSessionPromptForAgent(ctx, server.URL(), "", "hello", map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, "main answer", reply.answer)
	assert.Equal(t, "main", reply.usedAgent)
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

	server, err := startExternalMCPServer(ctx, cfg, func(relayCtx context.Context, text string, attachments []events.OutboundAttachment, replyTarget *events.InboundMessage, channel string) (*events.InboundMessage, error) {
		_ = relayCtx
		_ = text
		_ = replyTarget

		relayChannel = channel
		relayAttachments = events.CloneOutboundAttachments(attachments)

		return appTestSlackReply(&events.SlackReplyTarget{ChannelID: channel, MessageTS: "123.456", ThreadTS: ""}), nil
	}, inertExternalMCPCleanup, nil, []string{"main"}, "main", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	reply, err := callMCPTool(ctx, server.URL(), map[string]any{"input": "hello", "slack_channel": "#triage", "attachments": []map[string]string{{"name": "red.png", "mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString([]byte("png"))}}})
	require.NoError(t, err)
	assert.Equal(t, "main answer", reply.answer)
	assert.Equal(t, "main", reply.usedAgent)
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

	cleanup := func(cleanupCtx context.Context, replyTarget *events.InboundMessage) {
		_ = cleanupCtx

		cleaned = append(cleaned, cloneAppTestSlackReplyTarget(replyTarget.SlackReply))
	}

	submitAgent := func(context.Context, string, string, *events.InboundMessage) error {
		t.Fatal("submitAgent called after thread alias failure")

		return nil
	}

	cfg := new(config.Config)
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	store := newAppTestSessionService(t, t.TempDir())
	server, err := startExternalMCPServer(ctx, cfg, func(context.Context, string, []events.OutboundAttachment, *events.InboundMessage, string) (*events.InboundMessage, error) {
		return appTestSlackReply(&events.SlackReplyTarget{MessageTS: "123.456"}), nil
	}, cleanup, nil, []string{"main"}, "main", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	_, err = callMCPTool(ctx, server.URL(), map[string]any{"input": "hello"})
	require.ErrorContains(t, err, "persist external MCP text thread alias")
	require.Len(t, cleaned, 1)
	assert.Equal(t, &events.SlackReplyTarget{MessageTS: "123.456", ThreadTS: "123.456"}, cleaned[0])
}

func TestStartExternalMCPServerCleansExternalMCPRelayWhenSubmitFails(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var cleaned []*events.SlackReplyTarget

	cleanup := func(cleanupCtx context.Context, replyTarget *events.InboundMessage) {
		_ = cleanupCtx

		cleaned = append(cleaned, cloneAppTestSlackReplyTarget(replyTarget.SlackReply))
	}

	submitAgent := func(context.Context, string, string, *events.InboundMessage) error {
		return errors.New("submit failed")
	}

	cfg := new(config.Config)
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	store := newAppTestSessionService(t, t.TempDir())
	server, err := startExternalMCPServer(ctx, cfg, func(context.Context, string, []events.OutboundAttachment, *events.InboundMessage, string) (*events.InboundMessage, error) {
		return appTestSlackReply(&events.SlackReplyTarget{ChannelID: "D123", MessageTS: "123.456"}), nil
	}, cleanup, nil, []string{"main"}, "main", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	_, err = callMCPTool(ctx, server.URL(), map[string]any{"input": "hello"})
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

	cleanup := func(cleanupCtx context.Context, replyTarget *events.InboundMessage) {
		_ = cleanupCtx

		cleaned = append(cleaned, cloneAppTestSlackReplyTarget(replyTarget.SlackReply))
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
	server, err := startExternalMCPServer(ctx, cfg, func(relayCtx context.Context, text string, attachments []events.OutboundAttachment, replyTarget *events.InboundMessage, channel string) (*events.InboundMessage, error) {
		_ = relayCtx
		_ = text
		_ = attachments
		_ = channel

		require.NotNil(t, replyTarget)

		return appTestSlackReply(&events.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333", ThreadTS: "111.222"}), nil
	}, cleanup, nil, []string{"planner"}, "planner", store, submitAgent, testLogger())
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	_, err = callMCPTool(ctx, server.URL(), map[string]any{"external_conversation_id": externalConversationID, "input": "hello"})
	require.ErrorContains(t, err, "submit external MCP input to agent")
	require.Len(t, cleaned, 1)
	assert.Equal(t, &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333", ThreadTS: "111.222"}, cleaned[0])
}

func TestStartExternalMCPServerIgnoresMismatchedRequestedAgentForExistingSession(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	const (
		externalConversationID = "caller-conversation-1"
		conversationID         = "external_mcp:planner:abc"
	)

	store := newAppTestSessionService(t, t.TempDir())
	session := harnessbridge.ExternalMCPSessionState{Agent: "planner", ConversationID: conversationID}
	require.NoError(t, store.UpsertExternalMCPSession(externalConversationID, session))

	selectedAgent := make(chan string, 1)
	selectedConversationID := make(chan string, 1)
	submitAgent := func(submitCtx context.Context, agent, conversationID string, inbound *events.InboundMessage) error {
		_ = submitCtx

		selectedAgent <- agent

		selectedConversationID <- conversationID

		inbound.CompleteResponse("planner answer", nil)

		return nil
	}

	var logs bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&logs, nil))

	cfg := new(config.Config)
	cfg.MCPExternal.ListenAddr = "127.0.0.1:0"
	server, err := startExternalMCPServer(ctx, cfg, inertExternalMCPRelay, inertExternalMCPCleanup, nil, []string{"main", "planner"}, "main", store, submitAgent, logger)
	require.NoError(t, err)

	defer func() { require.NoError(t, server.Close(context.Background())) }()

	reply, err := callMCPTool(ctx, server.URL(), map[string]any{"external_conversation_id": externalConversationID, "agent": "main", "input": "hello"})
	require.NoError(t, err)
	assert.Equal(t, "planner answer", reply.answer)
	assert.Equal(t, "planner", reply.usedAgent)
	assert.Equal(t, externalConversationID, reply.externalConversationID)
	assert.Equal(t, "planner", <-selectedAgent)
	assert.Equal(t, conversationID, <-selectedConversationID)

	state, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, session, state.ExternalMCPSessions[externalConversationID])

	logText := logs.String()
	assert.Contains(t, logText, "mismatched persisted session agent")
	assert.Contains(t, logText, "external_conversation_id=caller-conversation-1")
	assert.Contains(t, logText, "requested_agent=main")
	assert.Contains(t, logText, "used_agent=planner")
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

			_, err = callMCPTool(ctx, server.URL(), map[string]any{"external_conversation_id": externalConversationID, "agent": tt.requestedAgent, "input": "hello"})
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

	_, err = callMCPTool(ctx, server.URL(), map[string]any{"agent": "planner", "input": "hello"})
	require.ErrorContains(t, err, `external MCP agent "planner" is not exposed`)
}

func TestSubmitExternalMCPInputReportsErrors(t *testing.T) {
	errSubmit := errors.New("thread bridge unavailable")
	_, err := submitExternalMCPInput(t.Context(), func(context.Context, string, string, *events.InboundMessage) error {
		return errSubmit
	}, "planner", "external_mcp:planner:123", &events.InboundContent{Text: "hello"}, nil, "", nil, "ticket-123")
	require.ErrorIs(t, err, errSubmit)
	require.ErrorContains(t, err, `submit external MCP input to agent "planner"`)

	errResponse := errors.New("assistant failed")
	replyTarget := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}
	attachments := []events.InboundAttachment{{Name: "scorecard.png", MIMEType: "image/png", Data: []byte("png")}}
	metadata := map[string]string{"ticket": "123", events.InboundPrincipalMetadataKey: "mallory", events.InboundOriginMetadataKey: "Slack", events.InboundMediaMetadataKey: "Voice"}
	_, err = submitExternalMCPInput(t.Context(), func(_ context.Context, agent, conversationID string, inbound *events.InboundMessage) error {
		assert.Equal(t, "planner", agent)
		assert.Equal(t, "external_mcp:planner:123", conversationID)
		assert.Equal(t, "hello", inbound.Text)
		assert.Equal(t, map[string]string{"ticket": "123", events.InboundPrincipalMetadataKey: "alice"}, inbound.Metadata)
		assert.Equal(t, attachments, inbound.Attachments)
		assert.True(t, inbound.HadAttachments)
		assert.Equal(t, replyTarget, inbound.SlackReply)
		inbound.CompleteResponse("", errResponse)

		return nil
	}, "planner", "external_mcp:planner:123", &events.InboundContent{Text: "hello", Attachments: attachments}, metadata, "alice", appTestSlackReply(replyTarget), "ticket-123")
	require.ErrorIs(t, err, errResponse)
	require.ErrorContains(t, err, "wait for external MCP reply")

	ctx, cancel := context.WithCancel(t.Context())
	_, err = submitExternalMCPInput(ctx, func(context.Context, string, string, *events.InboundMessage) error {
		cancel()

		return nil
	}, "planner", "external_mcp:planner:123", &events.InboundContent{Text: "hello"}, nil, "", nil, "ticket-123")
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "wait for external MCP reply")
}

func TestSubmitExternalMCPInputReturnsAttachments(t *testing.T) {
	outboundAttachments := []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}, {MIMEType: "application/octet-stream", Data: []byte("data")}}
	reply, err := submitExternalMCPInput(t.Context(), func(_ context.Context, _, _ string, inbound *events.InboundMessage) error {
		inbound.CompleteResponseWithAttachments("answer", outboundAttachments, nil)

		return nil
	}, "planner", "external_mcp:planner:123", &events.InboundContent{Text: "hello"}, nil, "", nil, "ticket-123")
	require.NoError(t, err)
	assert.Equal(t, "answer", reply.Answer)
	assert.Equal(t, []externalmcp.SessionAttachment{
		{Name: "report.txt", MIMEType: "text/plain", DataBase64: base64.StdEncoding.EncodeToString([]byte("report"))},
		{Name: "attachment-2", MIMEType: "application/octet-stream", DataBase64: base64.StdEncoding.EncodeToString([]byte("data"))},
	}, reply.Attachments)
}

func TestExternalMCPInboundContentConvertsTextAttachments(t *testing.T) {
	content, outbound, err := externalMCPInboundContent([]externalmcp.SessionPromptAttachment{
		{Name: "notes.md", MIMEType: "text/markdown; charset=utf-8", DataBase64: base64.StdEncoding.EncodeToString([]byte("# Notes\nhello"))},
		{Name: "photo.png", MIMEType: "image/png", DataBase64: base64.StdEncoding.EncodeToString([]byte("png"))},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"External MCP text file attachment notes.md (text/markdown):\n# Notes\nhello"}, content.TextAttachments)
	assert.Equal(t, []events.InboundAttachment{{Name: "photo.png", MIMEType: "image/png", Data: []byte("png")}}, content.Attachments)
	assert.Equal(t, []events.OutboundAttachment{{Name: "notes.md", MIMEType: "text/markdown; charset=utf-8", Data: []byte("# Notes\nhello")}, {Name: "photo.png", MIMEType: "image/png", Data: []byte("png")}}, outbound)
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

func shortTempDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "rc-*")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })

	return dir
}

func inertExternalMCPRelay(context.Context, string, []events.OutboundAttachment, *events.InboundMessage, string) (*events.InboundMessage, error) {
	return nil, nil
}

func inertExternalMCPCleanup(context.Context, *events.InboundMessage) {}

func inertStartNewThread(context.Context, *events.StartNewThreadRequest) (events.StartNewThreadResult, error) {
	return events.StartNewThreadResult{}, errors.New("start new thread is inert in this test")
}

func appTestSlackReply(replyTarget *events.SlackReplyTarget) *events.InboundMessage {
	return &events.InboundMessage{SlackReply: replyTarget}
}

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

	service, err := harnessbridge.NewSessionServiceIn(workspace, config.DefaultWorkDir)
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
	usedAgent              string
	externalConversationID string
}

func callSessionPromptForAgent(ctx context.Context, endpoint, agent, input string, metadata map[string]string) (mcpToolReply, error) {
	args := map[string]any{"input": input}
	if agent != "" {
		args["agent"] = agent
	}

	if metadata != nil {
		args["metadata"] = metadata
	}

	return callMCPTool(ctx, endpoint, args)
}

func callMCPTool(ctx context.Context, endpoint string, args map[string]any) (mcpToolReply, error) {
	implementation := new(mcp.Implementation)
	implementation.Name = "test-client"
	implementation.Version = "1.0.0"

	client := mcp.NewClient(implementation, nil)
	transport := new(mcp.StreamableClientTransport)
	transport.Endpoint = endpoint
	transport.DisableStandaloneSSE = true
	transport.HTTPClient = new(http.Client)

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
		Agent                  string `json:"agent"`
		ExternalConversationID string `json:"external_conversation_id"`
	}

	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return mcpToolReply{}, fmt.Errorf("marshal structured %s content: %w", externalmcp.SessionPromptToolName, err)
	}

	if err := json.Unmarshal(data, &structured); err != nil {
		return mcpToolReply{}, fmt.Errorf("parse structured %s content: %w", externalmcp.SessionPromptToolName, err)
	}

	return mcpToolReply{answer: structured.Answer, usedAgent: structured.Agent, externalConversationID: structured.ExternalConversationID}, nil
}
