package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
)

// ErrControlUnavailable reports that no RocketClaw server control socket accepted the CLI connection.
var ErrControlUnavailable = errors.New("rocketclaw control socket unavailable")

type controlRequest struct {
	Type           string                       `json:"type"`
	ConversationID string                       `json:"conversation_id,omitempty"`
	QuestionID     string                       `json:"question_id,omitempty"`
	Agent          string                       `json:"agent,omitempty"`
	Text           string                       `json:"text,omitempty"`
	Answer         events.AskUserQuestionAnswer `json:"answer,omitempty"`
	Summarize      bool                         `json:"summarize,omitempty"`
}
type controlMessage struct {
	Type           string                         `json:"type"`
	ConversationID string                         `json:"conversation_id,omitempty"`
	Text           string                         `json:"text,omitempty"`
	Question       *events.AskUserQuestionRequest `json:"question,omitempty"`
}
type controlServer struct {
	net.Listener

	path string
}

type controlQuestionHub struct {
	mu      sync.Mutex
	clients map[string]controlQuestionClient
	pending map[string]controlQuestionPending
}

type controlQuestionClient struct {
	conversationID string
	send           func(controlMessage)
}

type controlQuestionPending struct {
	clientID string
	ch       chan events.AskUserQuestionAnswer
}

func newControlQuestionHub() *controlQuestionHub {
	return &controlQuestionHub{clients: map[string]controlQuestionClient{}, pending: map[string]controlQuestionPending{}}
}

func startControlServer(ctx context.Context, cfg *config.Config, bus *events.Bus, sessions *harnessbridge.SessionService, threads *threadBridgeManager, questions *controlQuestionHub, logger *slog.Logger) (*controlServer, error) {
	path := ControlSocketPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create rocketclaw control socket dir: %w", err)
	}

	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("chmod rocketclaw control socket dir: %w", err)
	}

	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("rocketclaw control path exists and is not a socket: %s", path)
		}

		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale rocketclaw control socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat rocketclaw control socket: %w", err)
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on rocketclaw control socket: %w", err)
	}

	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("chmod rocketclaw control socket: %w", err)
	}

	server := &controlServer{Listener: listener, path: path}

	go func() { <-ctx.Done(); _ = server.Close(context.Background()) }()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				if ctx.Err() == nil {
					logger.Error("accept rocketclaw control client", "error", err)
				}

				return
			}

			go serveControlClient(ctx, conn, bus, sessions, threads, questions)
		}
	}()

	return server, nil
}

func (h *controlQuestionHub) ask(ctx context.Context, req *events.AskUserQuestionRequest) (events.AskUserQuestionAnswer, error) {
	clientID := strings.TrimSpace(req.TerminalClientID)
	h.mu.Lock()
	client, ok := h.clients[clientID]
	if !ok || client.conversationID != strings.TrimSpace(req.ConversationID) {
		h.mu.Unlock()
		return events.AskUserQuestionAnswer{}, errors.New("ask_user_question requires an attached terminal CLI client")
	}

	ch := make(chan events.AskUserQuestionAnswer, 1)
	h.pending[req.ID] = controlQuestionPending{clientID: clientID, ch: ch}
	h.mu.Unlock()

	client.send(controlMessage{Type: "question", Question: req})

	select {
	case answer, ok := <-ch:
		if !ok {
			return events.AskUserQuestionAnswer{}, errors.New("attached terminal CLI client disconnected before answering")
		}

		return answer, nil
	case <-ctx.Done():
		h.mu.Lock()
		delete(h.pending, req.ID)
		h.mu.Unlock()

		return events.AskUserQuestionAnswer{}, fmt.Errorf("wait for terminal answer: %w", ctx.Err())
	}
}

func (h *controlQuestionHub) register(clientID, conversationID string, send func(controlMessage)) {
	h.mu.Lock()
	h.clients[clientID] = controlQuestionClient{conversationID: conversationID, send: send}
	h.mu.Unlock()
}

func (h *controlQuestionHub) unregister(clientID string) {
	h.mu.Lock()
	delete(h.clients, clientID)
	for id, pending := range h.pending {
		if pending.clientID == clientID {
			delete(h.pending, id)
			close(pending.ch)
		}
	}
	h.mu.Unlock()
}

func (h *controlQuestionHub) answer(clientID, questionID string, answer events.AskUserQuestionAnswer) bool {
	h.mu.Lock()
	pending := h.pending[questionID]
	if pending.clientID != clientID || pending.ch == nil {
		h.mu.Unlock()
		return false
	}

	delete(h.pending, questionID)
	h.mu.Unlock()

	answer.Source = events.SourceTerminalCLI
	pending.ch <- answer

	return true
}

// ControlSocketPath returns the selected runtime's local Unix control socket path.
func ControlSocketPath(cfg *config.Config) string {
	return filepath.Join(cfg.Workspace, cfg.WorkDirName(), "control", "control.sock")
}

func (s *controlServer) Close(context.Context) error {
	errClose := s.Listener.Close()
	if info, err := os.Lstat(s.path); err == nil && info.Mode()&os.ModeSocket != 0 {
		errClose = errors.Join(errClose, os.Remove(s.path))
	}

	if errClose != nil {
		return fmt.Errorf("close rocketclaw control socket: %w", errClose)
	}

	return nil
}

func serveControlClient(ctx context.Context, conn net.Conn, bus *events.Bus, sessions *harnessbridge.SessionService, threads *threadBridgeManager, questions *controlQuestionHub) {
	defer func() { _ = conn.Close() }()

	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)

	var sendMu sync.Mutex

	send := func(msg controlMessage) { sendMu.Lock(); defer sendMu.Unlock(); _ = enc.Encode(msg) }

	clientCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	clientID := rand.Text()
	defer questions.unregister(clientID)

	var req controlRequest
	if err := dec.Decode(&req); err != nil {
		return
	}
	conversationID, private := strings.TrimSpace(req.ConversationID), false
	switch req.Type {
	case "attach":
		send(controlMessage{Type: "attached", ConversationID: conversationID})
		controlHistory(ctx, sessions, conversationID, send)
	case "new":
		agent := strings.TrimSpace(req.Agent)
		if agent == "" {
			agent = "main"
		}

		conversationID, private = "cli:"+rand.Text(), true
		managed, created, err := threads.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: agent}, []events.OutputTarget{events.OutputTargetTerminal})
		if err != nil {
			send(controlMessage{Type: "error", Text: err.Error()})
			return
		}
		if created {
			if err := threads.store.UpsertThread(conversationID, agent); err != nil {
				threads.dropCreatedBridge(conversationID, managed)
				send(controlMessage{Type: "error", Text: fmt.Errorf("persist terminal CLI thread bridge: %w", err).Error()})
				return
			}
		}

		send(controlMessage{Type: "attached", ConversationID: conversationID})
	}
	questions.register(clientID, conversationID, send)

	go func() {
		for observed := range bus.Observe(clientCtx) {
			if observed.Inbound != nil && observed.Inbound.ConversationID == conversationID {
				send(controlMessage{Type: "event", Text: formatInbound(observed.Inbound)})
			}

			if observed.Outbound != nil && observed.Outbound.ConversationID == conversationID {
				send(controlMessage{Type: "event", Text: formatOutbound(observed.Outbound)})
			}
		}
	}()

	for {
		if err := dec.Decode(&req); err != nil {
			return
		}

		switch req.Type {
		case "prompt":
			inbound := events.NewMainInboundMessage(events.SourceTerminalCLI, events.InboundKindPrompt, "terminal", strings.TrimSpace(req.Text), true)
			inbound.ConversationID = conversationID
			inbound.Metadata = map[string]string{events.TerminalCLIClientIDMetadataKey: clientID}

			if conversationID == events.MainConversationID() {
				if err := bus.PublishInbound(ctx, inbound); err != nil {
					send(controlMessage{Type: "error", Text: err.Error()})
				}
			} else {
				state, err := threads.store.Load()
				if err != nil {
					send(controlMessage{Type: "error", Text: err.Error()})
					continue
				}

				managed, _, err := threads.ensureThreadBridge(conversationID, state.Threads[conversationID], []events.OutputTarget{events.OutputTargetTerminal})
				if err != nil {
					send(controlMessage{Type: "error", Text: err.Error()})
					continue
				}

				if err := managed.bridge.Submit(ctx, inbound); err != nil {
					send(controlMessage{Type: "error", Text: err.Error()})
				}
			}
		case "exit":
			if private && req.Summarize {
				controlSummarize(ctx, bus, threads, conversationID, send)
			}

			send(controlMessage{Type: "closed"})

			return
		case "question_answer":
			if !questions.answer(clientID, req.QuestionID, req.Answer) {
				send(controlMessage{Type: "error", Text: "question is not pending for this terminal CLI client"})
			}
		}
	}
}

func controlHistory(ctx context.Context, sessions *harnessbridge.SessionService, conversationID string, send func(controlMessage)) {
	entries, err := sessions.ObserveEntries(ctx, conversationID, 0)
	if err != nil {
		send(controlMessage{Type: "error", Text: "load history: " + err.Error()})
		return
	}

	messages := []string{}

	for i := range entries {
		for _, raw := range entries[i].Entry.ReplayInput {
			if role, text := replayMessagePreview(raw); role == "user" || role == "assistant" {
				messages = append(messages, role+": "+text)
			}
		}
	}

	for _, message := range messages[max(0, len(messages)-8):] {
		send(controlMessage{Type: "history", Text: message})
	}
}

func controlSummarize(ctx context.Context, bus *events.Bus, threads *threadBridgeManager, conversationID string, send func(controlMessage)) {
	threads.mu.Lock()
	managed := threads.bridges[conversationID]
	threads.mu.Unlock()

	summary, err := managed.bridge.Summarize(ctx, "Summarize this terminal CLI session for the main RocketClaw conversation. Include decisions, useful context, and unresolved follow-ups.")
	if err != nil {
		send(controlMessage{Type: "error", Text: "summary failed: " + err.Error()})
		return
	}

	if err := bus.PublishInbound(ctx, events.NewMainInboundMessage(events.SourceTerminalCLI, events.InboundKindInternalize, "terminal_cli_summary", summary, false)); err != nil {
		send(controlMessage{Type: "error", Text: "append summary failed: " + err.Error()})
	}
}

// RunControlClient attaches terminal CLI I/O to a running RocketClaw server control socket.
func RunControlClient(ctx context.Context, cfg *config.Config, options CLIOptions) error {
	conn, err := net.DialTimeout("unix", ControlSocketPath(cfg), time.Second)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrControlUnavailable, err)
	}

	defer func() { _ = conn.Close() }()

	renderer := &terminalRenderer{out: options.Out}
	enc, dec, reader := json.NewEncoder(conn), json.NewDecoder(conn), bufio.NewReader(options.In)

	request := controlRequest{Type: "attach", ConversationID: strings.TrimSpace(options.ConversationID)}
	if request.ConversationID == "" {
		request.ConversationID = events.MainConversationID()
	}

	if options.NewConversation {
		request = controlRequest{Type: "new", Agent: options.Agent}
	}

	if err := enc.Encode(request); err != nil {
		return fmt.Errorf("send control attach request: %w", err)
	}

	attached := make(chan struct{})
	closed := make(chan struct{})
	questionReady := make(chan struct{}, 1)
	var pendingMu sync.Mutex
	var pendingQuestion *events.AskUserQuestionRequest

	go func() {
		defer close(closed)

		for {
			var msg controlMessage
			if err := dec.Decode(&msg); err != nil {
				return
			}

			switch msg.Type {
			case "attached":
				renderer.printLine("terminal CLI attached to " + msg.ConversationID)
				close(attached)
			case "history", "event":
				renderer.printEvent(msg.Text)
			case "error":
				renderer.printEvent("error: " + msg.Text)
			case "question":
				renderer.printQuestion(msg.Question)
				pendingMu.Lock()
				pendingQuestion = msg.Question
				pendingMu.Unlock()
				select {
				case questionReady <- struct{}{}:
				default:
				}
			case "closed":
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for control attach: %w", ctx.Err())
	case <-attached:
	case <-closed:
		return fmt.Errorf("wait for control attach: %w", ErrControlUnavailable)
	}

	exited := false

	err = (&terminalCLI{renderer: renderer, in: options.In, reader: reader}).readLines(ctx, func(line string) error {
		line = strings.TrimSpace(line)
		pendingMu.Lock()
		question := pendingQuestion
		if question != nil {
			pendingQuestion = nil
		}
		pendingMu.Unlock()
		if question == nil {
			select {
			case <-questionReady:
				pendingMu.Lock()
				question = pendingQuestion
				pendingQuestion = nil
				pendingMu.Unlock()
			case <-time.After(10 * time.Millisecond):
			}
		}
		if question != nil {
			answer, err := terminalQuestionAnswer(question, line)
			if err != nil {
				renderer.printLine(err.Error())
				pendingMu.Lock()
				pendingQuestion = question
				pendingMu.Unlock()
				return nil
			}

			if err := enc.Encode(controlRequest{Type: "question_answer", QuestionID: question.ID, Answer: answer}); err != nil {
				return fmt.Errorf("send control question answer: %w", err)
			}

			return nil
		}

		if line == "/exit" {
			summarize := options.NewConversation && (&terminalCLI{renderer: renderer, in: options.In, reader: reader}).askYesNo("Append a summary of this CLI session to main? [y/N] ")
			exited = true

			if err := enc.Encode(controlRequest{Type: "exit", Summarize: summarize}); err != nil {
				return fmt.Errorf("send control exit request: %w", err)
			}

			return io.EOF
		}

		if err := enc.Encode(controlRequest{Type: "prompt", Text: line}); err != nil {
			return fmt.Errorf("send control prompt request: %w", err)
		}

		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	if !exited {
		_ = enc.Encode(controlRequest{Type: "exit"})
	}

	select {
	case <-closed:
	case <-time.After(50 * time.Millisecond):
	}

	return nil
}
