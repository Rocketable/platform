package rpc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/oai"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/gorilla/websocket"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// voiceEndError ends a call; its value is the ended frame's reason.
type voiceEndError string

func (e voiceEndError) Error() string {
	return "voice call ended: " + string(e)
}

// voiceCall is one live call. Only its call loop writes to conn or touches the fields after it.
type voiceCall struct {
	server                    *Server
	dialect                   *voiceDialect
	header                    http.Header
	id                        string // the OpenAI session or call
	conn                      *websocket.Conn
	conversationID, principal string

	pending          *voiceEvent // a public hand-off waiting for late words
	quiet            <-chan time.Time
	said             []voiceEvent // what both speakers said since the previous hand-off
	questions        []protocol.AskUserQuestionRequest
	seen, answerable map[string]bool // hand-offs; questions read out, true while answerable
	reported         map[string]bool // turns whose reply was sent
	latest, progress string
	progressAt       time.Time
	busy             bool // a turn runs without waiting on a question
}

// voiceTurn is one transcript turn reduced to the text a call may send (R19).
type voiceTurn struct {
	key             string
	active          bool
	inputs          []string
	reply, progress string
}

type voiceSnapshot struct {
	turns     []voiceTurn
	questions []protocol.AskUserQuestionRequest
}

// voice runs one call for exactly as long as the request lives.
func (s *Server) voice(request *VoiceRequest, stream grpc.ServerStream) error {
	ctx, id := stream.Context(), request.ConversationId

	_, principal, err := s.principal(ctx)
	if err != nil {
		return err
	}

	if err := s.visibleConversation(ctx, id); err != nil {
		return err
	}

	cfg := s.cfg.Clone()

	thread, recorded, err := s.sessions.Thread(id)
	if err != nil {
		return fmt.Errorf("read voice conversation: %w", err)
	}

	if !recorded {
		return fmt.Errorf("web voice: %w", status.Error(codes.NotFound, "conversation is not recorded"))
	}

	agents, _, err := backend.LoadRuntimeDefinitions(s.cfg, cfg.RuntimeDirName())
	if err != nil {
		return fmt.Errorf("load voice agent: %w", err)
	}

	name, _, named := strings.Cut(agents.Items[thread.Agent].Model, "/")
	if !named {
		name = "openai"
	}

	provider, _ := cfg.Provider(name)

	credentials := voiceCredentials{bearer: provider.APIKey}
	if provider.RocketCodeAuth == "chatgpt" {
		// A missing login leaves no bearer; a failed refresh names its login command.
		token, err := oai.FreshTokenIn(ctx, cfg.Workspace, cfg.RuntimeDirName(), name, oai.Token{})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("refresh voice credentials", "conversation", id, "provider", name, "error", err)
			return fmt.Errorf("web voice: %w", status.Error(codes.FailedPrecondition, err.Error()))
		}

		credentials = voiceCredentials{bearer: token.Access, accountID: token.AccountID}
	}

	if credentials.bearer == "" {
		return fmt.Errorf("web voice: %w", status.Errorf(codes.FailedPrecondition, "provider %q has no usable voice credentials", name))
	}

	callCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	own := &cancel

	s.voiceMu.Lock()
	if older := s.voiceCalls[id]; older != nil {
		(*older)(voiceEndError("replaced"))
	}

	s.voiceCalls[id] = own
	s.voiceMu.Unlock()

	defer func() {
		s.voiceMu.Lock()
		if s.voiceCalls[id] == own {
			delete(s.voiceCalls, id)
		}
		s.voiceMu.Unlock()
	}()

	seed, err := s.history(ctx, &HistoryRequest{Id: id, Limit: 40})
	if err != nil {
		return err
	}

	dialect := newVoiceDialect(provider)
	header := dialect.header(credentials)
	answer, callID, err := dialect.create(callCtx, header, request.Sdp, thread.Agent, seed.Messages)

	var conn *websocket.Conn
	if err == nil {
		if conn, err = dialect.dial(callCtx, header, callID); err != nil {
			// The call was created and billed; end it even though this request is over.
			hangup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			_ = dialect.hangup(hangup, header, callID)

			cancel()
		}
	}

	if upstream, ok := errors.AsType[*voiceUpstreamError](err); ok {
		return fmt.Errorf("web voice: %w", status.Error(codes.FailedPrecondition, upstream.Error()))
	}

	if err != nil {
		return fmt.Errorf("web voice: %w", err)
	}

	if err := stream.SendMsg(&VoiceEvent{Event: &VoiceEvent_Answer{Answer: &VoiceAnswer{Sdp: answer, Route: string(dialect.route)}}}); err != nil {
		cancel(err)
	}

	call := &voiceCall{server: s, dialect: dialect, header: header, id: callID, conn: conn, conversationID: id, principal: principal}
	events, snapshots, done := make(chan voiceEvent), make(chan voiceSnapshot), make(chan struct{})

	var handoffs errgroup.Group

	calls, groupCtx := errgroup.WithContext(callCtx)
	calls.Go(func() error { return call.read(events, done) })
	calls.Go(func() error { return call.loop(groupCtx, events, snapshots, done, &handoffs) })
	calls.Go(func() error { return call.forward(groupCtx, snapshots) })

	errCalls := calls.Wait()

	reason, ended := errors.AsType[voiceEndError](errCalls)
	if !ended {
		reason = "error"

		slog.Error("web voice call", "conversation", id, "error", errCalls)
	}

	_ = stream.SendMsg(&VoiceEvent{Event: &VoiceEvent_Ended{Ended: &VoiceEnded{Reason: string(reason)}}})
	_ = handoffs.Wait() // Hand-offs log their own failures.

	return nil
}

// read decodes sideband messages until the socket closes; done closes with the socket.
func (c *voiceCall) read(events chan<- voiceEvent, done <-chan struct{}) error {
	defer close(events)

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return nil
		}

		event, err := c.dialect.decode(data)
		if err != nil {
			slog.Warn("decode voice sideband event", "conversation", c.conversationID, "error", err)
			continue
		}

		if event.kind == voiceEventIgnored {
			continue
		}

		select {
		case events <- event:
		case <-done:
			return nil
		}
	}
}

// loop owns the socket's writes and all call state until the call ends, then closes the call.
func (c *voiceCall) loop(ctx context.Context, events <-chan voiceEvent, snapshots <-chan voiceSnapshot, done chan<- struct{}, handoffs *errgroup.Group) error {
	noMedia, idle := time.NewTimer(time.Minute), time.NewTimer(5*time.Minute)
	c.seen, c.answerable = map[string]bool{}, map[string]bool{}

	var (
		reason voiceEndError
		closed bool
	)

	for reason == "" {
		select {
		case <-ctx.Done():
			reason = "error"
			if end, ok := errors.AsType[voiceEndError](context.Cause(ctx)); ok {
				reason = end
			} else if errors.Is(context.Cause(ctx), context.Canceled) {
				reason = "stopped"
			}
		case <-noMedia.C:
			reason = "no_media"
		case <-idle.C:
			if !c.busy {
				reason = "idle"
			}

			idle.Reset(5 * time.Minute)
		case <-c.quiet:
			c.handOff(ctx, handoffs)
		case snapshot := <-snapshots:
			c.update(snapshot)
		case event, ok := <-events:
			if !ok {
				reason = "closed"
				break
			}

			// session.started comes before media flows; only heard speech proves it does.
			if event.kind == voiceEventInput {
				noMedia.Stop()
			}

			if event.kind == voiceEventInput || event.kind == voiceEventHandOff {
				idle.Reset(5 * time.Minute)
			}

			if event.kind == voiceEventClosed {
				closed, reason = true, cmp.Or(map[string]voiceEndError{"expired": "expired", "content": "safety"}[event.text], "closed")
			}

			c.hear(ctx, &event, handoffs)
		}
	}

	if c.pending != nil {
		c.handOff(ctx, handoffs)
	}

	c.end(ctx, events, closed)
	close(done)

	return reason
}

// hear records what was said and starts each new hand-off once.
func (c *voiceCall) hear(ctx context.Context, event *voiceEvent, handoffs *errgroup.Group) {
	codex := c.dialect.route == voiceRouteCodex

	switch event.kind {
	case voiceEventIgnored, voiceEventStarted, voiceEventClosed:
	case voiceEventInput, voiceEventOutput, voiceEventTurnDone:
		if codex == (event.kind == voiceEventTurnDone) {
			// Hand-offs use only the newest few kilobytes, so a long call without one stays bounded.
			c.said = append(c.said, *event)
			c.said = c.said[max(0, len(c.said)-2000):]
		}

		if c.pending != nil && event.endMS >= c.pending.offsetMS {
			c.handOff(ctx, handoffs)
		}
	case voiceEventHandOff:
		if c.seen[event.id] {
			return
		}

		if c.pending != nil {
			c.handOff(ctx, handoffs)
		}

		c.seen[event.id], c.latest = true, event.id
		c.pending, c.quiet = event, time.After(2*time.Second)

		if codex {
			c.handOff(ctx, handoffs)
		}
	case voiceEventError:
		slog.Warn("voice sideband rejected an event", "conversation", c.conversationID)
	}
}

// handOff turns the pending hand-off into an answer to a question this call read
// out, or into a voice steer: the request plus what was said since the previous one (R9).
func (c *voiceCall) handOff(ctx context.Context, handoffs *errgroup.Group) {
	event, codex := *c.pending, c.dialect.route == voiceRouteCodex
	c.pending, c.quiet = nil, nil

	slices.SortStableFunc(c.said, func(a, b voiceEvent) int { return cmp.Compare(a.startMS, b.startMS) })

	split := 0
	for split < len(c.said) && (codex || c.said[split].startMS < event.offsetMS) {
		split++
	}

	before := c.said[:split]
	c.said = slices.Clone(c.said[split:])

	// Public hand-offs carry no text: the request is the person's last words before it.
	words := []string{event.text}

	if !codex {
		start := len(before)
		for start > 0 && before[start-1].kind == voiceEventInput {
			start--
		}

		for _, item := range before[start:] {
			words = append(words, item.text)
		}
	}

	var lines []string

	for i, item := range before {
		if i > 0 && item.kind != voiceEventTurnDone && item.kind == before[i-1].kind {
			lines[len(lines)-1] += item.text
			continue
		}

		lines = append(lines, "> "+cmp.Or(item.role, map[voiceEventKind]string{voiceEventInput: "user", voiceEventOutput: "assistant"}[item.kind])+": "+item.text)
	}

	request := strings.TrimSpace(strings.Join(words, ""))

	message := voiceCut(request, 4096)
	if len(lines) > 0 {
		message = strings.TrimSpace(message + "\n\nVoice conversation:\n" + voiceTail(strings.Join(lines, "\n"), 4096))
	}

	ask := ""
	if i := slices.IndexFunc(c.questions, func(q protocol.AskUserQuestionRequest) bool { return c.answerable[q.ID] }); i >= 0 && request != "" {
		ask = c.questions[i].ID
		c.answerable[ask] = false
	}

	handoffs.Go(func() error {
		ctx := context.WithoutCancel(ctx) // Ending the call never stops the turn (R17).
		if ask != "" {
			err := c.server.backend.AnswerQuestion(ctx, c.conversationID, ask, protocol.AskUserQuestionAnswer{Custom: request})
			if _, notPending := errors.AsType[*backend.QuestionNotPendingError](err); !notPending {
				if err != nil {
					slog.Error("answer voice question", "conversation", c.conversationID, "error", err)
				}

				return nil
			}
		}

		inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: message}, true)
		inbound.ConversationID = c.conversationID
		inbound.Metadata[protocol.InboundPrincipalMetadataKey] = c.principal
		inbound.Metadata[protocol.InboundMediaMetadataKey] = "Voice"

		inbound.Metadata["web_message_id"] = event.id
		if err := c.server.backend.RunTurn(ctx, inbound); err != nil {
			slog.Error("run voice hand-off", "conversation", c.conversationID, "error", err)
		}

		return nil
	})
}

// update reads out new questions, sends throttled progress, and reports each turn
// that settles during the call once: spoken if this call handed it off (R11), else silent.
func (c *voiceCall) update(snapshot voiceSnapshot) {
	c.questions, c.busy = snapshot.questions, false

	for _, question := range c.questions {
		if _, read := c.answerable[question.ID]; read {
			continue
		}

		c.answerable[question.ID] = true
		labels := make([]string, 0, len(question.Options))

		for _, option := range question.Options {
			labels = append(labels, option.Label)
		}

		text := strings.TrimSpace("The agent asks: " + question.Question + "\n" + question.Details)
		if len(labels) > 0 {
			text += "\nOptions: " + strings.Join(labels, ", ")
		}

		c.send(c.latest, text, true)
	}

	if c.reported == nil {
		c.reported = map[string]bool{}
		for _, turn := range snapshot.turns {
			c.reported[turn.key] = !turn.active // Replies settled before the call are not news.
		}
	}

	for _, turn := range snapshot.turns {
		owner := c.latest

		mine := slices.IndexFunc(turn.inputs, func(id string) bool { return c.seen[id] })
		if mine >= 0 {
			owner = turn.inputs[mine]
		}

		switch {
		case turn.active:
			c.busy = c.busy || len(c.questions) == 0

			if turn.progress != "" && turn.progress != c.progress && time.Since(c.progressAt) >= 5*time.Second {
				c.progress, c.progressAt = turn.progress, time.Now()
				c.send(owner, turn.progress, false)
			}
		case !c.reported[turn.key] && turn.reply != "":
			c.reported[turn.key] = true
			c.send(owner, turn.reply, mine >= 0)
		}
	}
}

func (c *voiceCall) send(id, text string, spoken bool) {
	for _, message := range c.dialect.appends(id, text, spoken) {
		_ = c.conn.WriteMessage(websocket.TextMessage, message) // A broken socket also ends the reader.
	}
}

// end asks OpenAI to close the session, waits up to 3 s for it, hangs up a
// public session that did not confirm, and closes the socket, which ends the reader.
func (c *voiceCall) end(ctx context.Context, events <-chan voiceEvent, closed bool) {
	if !closed {
		_ = c.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		_ = c.conn.WriteMessage(websocket.TextMessage, []byte(voiceCloseMessage))
		budget := time.After(3 * time.Second)

	wait:
		for !closed {
			select {
			case event, ok := <-events:
				if !ok {
					break wait
				}

				closed = event.kind == voiceEventClosed
			case <-budget:
				break wait
			}
		}
	}

	// A Codex call already got session.close on this sideband; only public sessions have a separate hangup.
	if !closed && c.dialect.route == voiceRoutePublic {
		hangup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		if err := c.dialect.hangup(hangup, c.header, c.id); err != nil {
			slog.Warn("hang up voice call", "conversation", c.conversationID, "error", err)
		}

		cancel()
	}

	_ = c.conn.Close()
}

// forward sends the loop a snapshot of the conversation after each change.
func (c *voiceCall) forward(ctx context.Context, snapshots chan<- voiceSnapshot) error {
	root, err := os.OpenRoot(c.server.cfg.Clone().Workspace)
	if err != nil {
		return fmt.Errorf("open voice workspace: %w", err)
	}

	defer func() { _ = root.Close() }()

	for _, err := range c.server.sessions.Changes(ctx, c.conversationID) {
		var snapshot voiceSnapshot
		if err == nil {
			snapshot, err = c.snapshot(ctx, root)
		}

		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			return err
		}

		select {
		case snapshots <- snapshot:
		case <-ctx.Done():
			return nil
		}
	}

	return nil
}

func (c *voiceCall) snapshot(ctx context.Context, root *os.Root) (voiceSnapshot, error) {
	var snapshot voiceSnapshot

	view, err := c.server.sessions.ObserveHistory(ctx, c.conversationID, "", 0, 0, 10, backend.HistoryInventory{})
	if err != nil {
		return snapshot, fmt.Errorf("read voice history: %w", err)
	}

	for i := range view.Entries {
		entry := &view.Entries[i]
		if entry.Synced && entry.SourceConversationID == "" {
			continue
		}

		messages, err := c.server.transcriptEntry(ctx, root, entry, c.conversationID, map[string]string{})
		if err != nil {
			return snapshot, err
		}

		turn, tool, partial := voiceTurn{key: entry.Key, active: entry.Active}, "", ""

		// Tool rows give only their name and public state; their text holds arguments and results.
		for _, message := range messages {
			switch {
			case message.Role == "user" && message.InputId != "":
				turn.inputs = append(turn.inputs, message.InputId)
			case message.Role == "assistant" && message.Complete:
				turn.reply = message.Text
			case message.Role == "assistant":
				partial = message.Text
			case message.ToolName != "":
				tool = message.ToolName + " " + message.State
			}
		}

		turn.progress = strings.TrimSpace(tool + "\n" + partial)
		snapshot.turns = append(snapshot.turns, turn)
	}

	snapshot.questions, err = c.server.backend.PendingQuestions(ctx, c.conversationID)
	if err != nil {
		return snapshot, fmt.Errorf("read voice questions: %w", err)
	}

	return snapshot, nil
}

// voiceTail returns the longest suffix of text within limit bytes that starts on a rune boundary.
func voiceTail(text string, limit int) string {
	start := max(0, len(text)-limit)
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}

	return text[start:]
}
