package rpc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
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

// voiceCall is one live call. Only its call loop writes to or replaces conn, or touches the fields after it.
// The loop also writes notices to the browser's stream, which the handler writes only before and after the loop.
type voiceCall struct {
	server                              *Server
	dialect                             voiceDialect
	header                              http.Header
	id                                  string // the OpenAI session or call
	conn                                *websocket.Conn
	stream                              grpc.ServerStream
	conversationID, principal, username string

	said                  []voiceEvent    // the lines, by speaker, said since the previous hand-off
	final                 map[string]bool // speakers whose latest line has its final transcript
	seen, asked, reported map[string]bool // this call's hand-offs; questions noticed; turns spoken
	latest                string          // the newest hand-off
	unsent                [][]byte        // appends from the first failed write on, for the next sideband
	inputs                map[string]bool // the conversation's input ids; nil until the first snapshot
}

// voiceTurn is one transcript turn reduced to the text a call may send.
type voiceTurn struct {
	key    string
	inputs []string // the input id of each user message, in order
	reply  string
}

type voiceSnapshot struct {
	turns     []voiceTurn
	questions []protocol.AskUserQuestionRequest
	voice     map[string]bool // inputs any voice call handed off; another person's never supersede this call
}

// voiceLongReply is what the voice model hears instead of a reply too long to speak.
const voiceLongReply = "That answer is too long to read aloud. It is shown on the screen."

// voiceSettings reads or changes the caller's voice settings and returns them.
func (s *Server) voiceSettings(ctx context.Context, request *VoiceSettingsRequest) (*VoiceSettingsResponse, error) {
	username, _, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}

	var settings backend.VoiceSettings

	switch request.Action {
	case VoiceSettingsAction_READ:
	case VoiceSettingsAction_START:
		// The conversation START finds is its answer, though another device resets it a moment later.
		if settings, err = s.sessions.VoiceSettings(ctx, username); err != nil || settings.ConversationID != "" {
			break
		}

		var created *CreateSessionResponse
		if created, err = s.createSession(ctx, &CreateSessionRequest{Agent: request.GetAgent()}); err == nil {
			err = s.sessions.StartVoiceConversation(ctx, username, created.Id, request.GetAgent())
		}
	case VoiceSettingsAction_RESET:
		err = s.sessions.ResetVoiceConversation(ctx, username, request.ConversationId)
	case VoiceSettingsAction_UPDATE:
		var choices []string
		if choices, err = s.agentChoices(ctx, ""); err == nil {
			if request.Agent != nil && !slices.Contains(choices, *request.Agent) || request.Voice != nil && !slices.Contains(append(newCodexVoice().voices(), (&publicVoice{}).voices()...), *request.Voice) {
				return nil, fmt.Errorf("web voice settings: %w", status.Error(codes.InvalidArgument, "select a loaded agent and an offered voice"))
			}

			err = s.sessions.UpdateVoiceSettings(ctx, username, request.Agent, request.Voice, request.Transcript)
		}
	}

	if err == nil && settings.ConversationID == "" {
		settings, err = s.sessions.VoiceSettings(ctx, username)
	}

	if err != nil {
		return nil, fmt.Errorf("web voice settings: %w", err)
	}

	return &VoiceSettingsResponse{ConversationId: settings.ConversationID, Agent: settings.Agent, Voice: settings.Voice, Transcript: settings.Transcript}, nil
}

// voice runs one call for exactly as long as the request lives.
func (s *Server) voice(request *VoiceRequest, stream grpc.ServerStream) error {
	ctx, id := stream.Context(), request.ConversationId

	username, principal, err := s.principal(ctx)
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

	const name = "openai"

	provider := cfg.OpenAI

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

	var dialect voiceDialect = newCodexVoice()

	if provider.RocketCodeAuth != "chatgpt" {
		public, err := newPublicVoice(provider)
		if err != nil {
			return fmt.Errorf("web voice: %w", status.Error(codes.FailedPrecondition, err.Error()))
		}

		dialect = public
	}

	if request.Voice != "" && !slices.Contains(dialect.voices(), request.Voice) {
		return fmt.Errorf("web voice: %w", status.Errorf(codes.InvalidArgument, "provider %q offers no voice %q", name, request.Voice))
	}

	callCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	own := &cancel

	s.voiceMu.Lock()
	if older := s.voiceCalls[username]; older != nil {
		(*older)(voiceEndError("replaced"))
	}

	s.voiceCalls[username] = own
	s.voiceMu.Unlock()

	defer func() {
		s.voiceMu.Lock()
		if s.voiceCalls[username] == own {
			delete(s.voiceCalls, username)
		}
		s.voiceMu.Unlock()
	}()

	seed, err := s.history(ctx, &HistoryRequest{Id: id, Limit: 40})
	if err != nil {
		return err
	}

	header := dialect.header(credentials)
	answer, callID, err := dialect.create(callCtx, header, request.Sdp, thread.Agent, cmp.Or(request.Voice, dialect.voices()[0]), seed.Messages)

	var conn *websocket.Conn
	if err == nil {
		if conn, err = voiceDial(callCtx, dialect.sideband(callID), header); err != nil {
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

	if err := stream.SendMsg(&VoiceEvent{Event: &VoiceEvent_Answer{Answer: &VoiceAnswer{Sdp: answer, Route: string(dialect.route())}}}); err != nil {
		cancel(err)
	}

	call := &voiceCall{server: s, dialect: dialect, header: header, id: callID, conn: conn, stream: stream, conversationID: id, principal: principal, username: username}
	snapshots := make(chan voiceSnapshot)

	calls, groupCtx := errgroup.WithContext(callCtx)
	calls.Go(func() error { return call.loop(groupCtx, calls, snapshots) })
	calls.Go(func() error { return call.forward(groupCtx, snapshots) })

	errCalls := calls.Wait()

	reason, ended := errors.AsType[voiceEndError](errCalls)
	if !ended {
		reason = "error"

		slog.Error("web voice call", "conversation", id, "error", errCalls)
	}

	_ = stream.SendMsg(&VoiceEvent{Event: &VoiceEvent_Ended{Ended: &VoiceEnded{Reason: string(reason)}}})

	return nil
}

// listen starts the current socket's reader in the call's group. It decodes messages until
// the socket fails or closes, then closes events, its only end signal; done stops it early.
// As Codex, a normal close of a sideband that reconnects is OpenAI ending the call, not a drop.
func (c *voiceCall) listen(calls *errgroup.Group, done <-chan struct{}) <-chan voiceEvent {
	conn, events := c.conn, make(chan voiceEvent)

	calls.Go(func() error {
		defer close(events)

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				if c.dialect.reconnects() && websocket.IsCloseError(err, websocket.CloseNormalClosure) {
					select {
					case events <- voiceEvent{kind: voiceEventClosed}:
					case <-done:
					}
				}

				return nil
			}

			event, err := voiceDecode(data, c.dialect)
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
	})

	return events
}

// loop owns the socket's writes and all call state until the call ends, then closes the call.
// A lost sideband ends a public call; a Codex call reconnects as Codex sideband.rs does:
// it hears every event its old reader read, waits 200 ms doubling to 5 s, reset after 30 s
// connected, then dials up to 5 times as connect_sideband, and sends what failed first.
func (c *voiceCall) loop(ctx context.Context, calls *errgroup.Group, snapshots <-chan voiceSnapshot) error {
	c.final, c.seen, c.asked, c.reported = map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	done := make(chan struct{})
	events, connected := c.listen(calls, done), time.Now()

	var (
		reason       voiceEndError
		closed       bool
		redial       <-chan time.Time // set, with events nil, while the sideband is down
		drops, tries int
	)

	for reason == "" {
		live := snapshots
		if redial != nil {
			live = nil // The forwarder waits; its next snapshot is whole.
		}

		select {
		case <-ctx.Done():
			reason = "error"
			if end, ok := errors.AsType[voiceEndError](context.Cause(ctx)); ok {
				reason = end
			} else if errors.Is(context.Cause(ctx), context.Canceled) {
				reason = "stopped"
			}
		case snapshot := <-live:
			c.update(snapshot)
		case event, ok := <-events:
			if !ok {
				_ = c.conn.Close()
				events = nil

				if !c.dialect.reconnects() {
					reason = "lost"
					break
				}

				if time.Since(connected) >= 30*time.Second {
					drops = 0
				}

				drops, tries = drops+1, 0
				redial = time.After(min(200*time.Millisecond<<min(drops-1, 5), 5*time.Second))

				_ = c.stream.SendMsg(&VoiceEvent{Event: &VoiceEvent_Reconnecting{Reconnecting: true}})

				break
			}

			if event.kind == voiceEventClosed {
				closed, reason = true, cmp.Or(map[string]voiceEndError{"expired": "expired", "content": "safety"}[event.text], "closed")
			}

			if errors.Is(c.hear(ctx, event), protocol.ErrBridgeStopped) {
				reason = "error"
			}
		case <-redial:
			conn, err := voiceDial(ctx, c.dialect.sideband(c.id), c.header)
			upstream, _ := errors.AsType[*voiceUpstreamError](err)
			tries++

			switch {
			case err == nil:
				c.conn, redial = conn, nil
				events, connected = c.listen(calls, done), time.Now()
				c.flush()

				_ = c.stream.SendMsg(&VoiceEvent{Event: &VoiceEvent_Reconnecting{Reconnecting: false}})
			case ctx.Err() != nil: // The spent timer leaves only the call's end ready.
			case upstream != nil && (upstream.status == http.StatusNotFound || upstream.status == http.StatusGone):
				reason = "closed"
			case tries == 5:
				reason = "lost"
			default:
				redial = time.After(time.Duration(float64(200*time.Millisecond<<(tries-1)) * (0.9 + rand.Float64()/5)))
			}
		}
	}

	c.end(ctx, events, closed)
	close(done)

	return reason
}

// hear records what was said and admits each hand-off.
func (c *voiceCall) hear(ctx context.Context, event voiceEvent) error {
	switch event.kind {
	case voiceEventInput, voiceEventOutput, voiceEventTurnDone:
		// Lines grow as in Codex update_active_transcript: speech extends the route's line for
		// its speaker, which a final transcript then replaces if it extends it, and the next
		// speech starts a new line.
		role, done := cmp.Or(event.role, map[voiceEventKind]string{voiceEventInput: "user", voiceEventOutput: "assistant"}[event.kind]), event.kind == voiceEventTurnDone
		i := c.dialect.line(c.said, role)

		switch {
		case event.text == "":
		case i < 0 || c.said[i].role != role || c.final[role]:
			// Hand-offs use only the newest few kilobytes, so a long call without one stays bounded.
			c.said = append(c.said, voiceEvent{role: role, text: event.text})
			c.said = c.said[max(0, len(c.said)-2000):]
		case !done:
			c.said[i].text += event.text
		case strings.HasPrefix(event.text, c.said[i].text):
			c.said[i].text = event.text
		}

		if done || event.text != "" {
			c.final[role] = done
		}
	case voiceEventHandOff:
		if c.seen[event.id] {
			return nil // A re-delivered hand-off would carry rebuilt text that admission refuses as different.
		}

		return c.handOff(ctx, event)
	case voiceEventError:
		slog.Warn("voice sideband rejected an event", "conversation", c.conversationID)
	case voiceEventIgnored, voiceEventClosed: // The loop ends the call on a close.
	}

	return nil
}

// handOff steers a hand-off into the conversation exactly as Codex wraps it
// (codex-rs core/src/context/realtime_delegation.rs): the request in <input>, and the
// lines said since the previous hand-off in <transcript_delta>. Public hand-offs carry
// no text, so their lines are the request too. A hand-off with nothing to send is ignored.
// It is admitted in arrival order without waiting for its turn; a stopping bridge
// ends the call, and any other refusal sends the browser a notice.
func (c *voiceCall) handOff(ctx context.Context, event voiceEvent) error {
	// As Codex's append_handoff_input, the request joins the lines unless the person already said it.
	if request := strings.TrimSpace(event.text); request != "" && !slices.ContainsFunc(c.said, func(line voiceEvent) bool {
		return line.role == "user" && strings.TrimSpace(line.text) == request
	}) {
		c.said = append(c.said, voiceEvent{role: "user", text: request})
	}

	lines := make([]string, 0, len(c.said))
	for _, line := range c.said {
		lines = append(lines, line.role+": "+line.text)
	}

	said := strings.Join(lines, "\n")

	input := cmp.Or(event.text, said)
	if input == "" {
		return nil
	}

	message := "<realtime_delegation>\n  <input>" + voiceDelegationField(input, false) + "</input>\n"
	if said != "" {
		message += "  <transcript_delta>" + voiceDelegationField(said, true) + "</transcript_delta>\n"
	}

	message += "</realtime_delegation>"

	c.said, c.seen[event.id], c.latest = nil, true, event.id

	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: message}, true)
	inbound.ConversationID = c.conversationID
	inbound.Metadata[protocol.InboundPrincipalMetadataKey], inbound.Metadata[protocol.InboundPrincipalNameMetadataKey], inbound.Metadata[protocol.InboundPrincipalEmailMetadataKey] = c.principal, c.principal, c.username
	inbound.Metadata[protocol.InboundMediaMetadataKey] = "Voice"
	inbound.Metadata["web_message_id"] = event.id

	item := &protocol.ThreadQueueItem{ID: event.id, Source: protocol.SourceWeb, Kind: protocol.InboundKindSteer, Principal: c.principal, Message: message, Inbound: inbound}
	// Ending the call never cancels an admission, so no stored row misses its enqueue.
	if err := c.server.backend.StashQueueItem(context.WithoutCancel(ctx), c.conversationID, item); err != nil {
		slog.Error("admit voice hand-off", "conversation", c.conversationID, "error", err)

		if errors.Is(err, protocol.ErrBridgeStopped) {
			return fmt.Errorf("admit voice hand-off: %w", err)
		}

		_ = c.stream.SendMsg(&VoiceEvent{Event: &VoiceEvent_Notice{Notice: "The agent could not accept that request. Try again."}})
	}

	return nil
}

// update notices each new question once, under the newest hand-off, and speaks the
// final reply of each finished turn this call handed off, once. Typing supersedes voice,
// as Codex invalidate_realtime_voice_input: a typed input silences every earlier hand-off,
// and only a later hand-off makes its turn speak again. An input typed during the call
// also tells the browser to mute the voice model at once, as Codex suppresses its speaker.
func (c *voiceCall) update(snapshot voiceSnapshot) {
	for _, question := range snapshot.questions {
		if c.asked[question.ID] {
			continue
		}

		c.asked[question.ID] = true
		labels := make([]string, 0, len(question.Options))

		for _, option := range question.Options {
			labels = append(labels, option.Label)
		}

		text := strings.TrimSpace("I need your input. Please respond in the app.\n\n" + question.Question + "\n" + question.Details)
		if len(labels) > 0 {
			text += "\nOptions: " + strings.Join(labels, ", ")
		}

		c.send(c.latest, text)
	}

	// The first snapshot holds what was typed before the call; only later inputs are typed during it.
	during, typed := c.inputs != nil, false
	if !during {
		c.inputs = map[string]bool{}
	}

	live := map[string]bool{} // this call's hand-offs that no typed input has followed

	for _, turn := range snapshot.turns {
		for _, id := range turn.inputs {
			if !c.seen[id] && !snapshot.voice[id] {
				clear(live)

				typed = typed || during && !c.inputs[id]
			}

			live[id], c.inputs[id] = c.seen[id], true
		}
	}

	if typed {
		_ = c.stream.SendMsg(&VoiceEvent{Event: &VoiceEvent_Typed{Typed: true}})
	}

	for _, turn := range snapshot.turns {
		if len(turn.inputs) == 0 || !live[turn.inputs[len(turn.inputs)-1]] || turn.reply == "" || c.reported[turn.key] {
			continue
		}

		mine := slices.IndexFunc(turn.inputs, func(id string) bool { return c.seen[id] })

		c.reported[turn.key] = true
		if len(turn.reply) <= 3960 { // Codex speaks at most 990 tokens of 4 bytes.
			c.send(turn.inputs[mine], turn.reply)
			continue
		}

		// As Codex shows a reply too long to speak on screen, the browser shows it as written,
		// and the voice model says where it is.
		_ = c.stream.SendMsg(&VoiceEvent{Event: &VoiceEvent_Reply{Reply: turn.reply}})
		c.send(turn.inputs[mine], voiceLongReply)
	}
}

func (c *voiceCall) send(id, text string) {
	c.unsent = append(c.unsent, c.dialect.appends(id, text)...)
	c.flush()
}

// flush writes the unsent appends in order. A failed write keeps that append and the rest,
// and closes the socket, so later writes fail at once and the reader ends.
func (c *voiceCall) flush() {
	for len(c.unsent) > 0 {
		if err := c.conn.WriteMessage(websocket.TextMessage, c.unsent[0]); err != nil {
			_ = c.conn.Close()
			return
		}

		c.unsent = c.unsent[1:]
	}
}

// end asks OpenAI to close the session over a live sideband (events is nil once it is
// down), waits up to 3 s for it, hangs up a public session that did not confirm, and
// closes the socket, which ends the reader.
func (c *voiceCall) end(ctx context.Context, events <-chan voiceEvent, closed bool) {
	if !closed && events != nil {
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

	// A sideband that reconnects sends nothing after session.close, as Codex; others hang up.
	if !closed && !c.dialect.reconnects() {
		hangup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		if err := c.dialect.hangup(hangup, c.header, c.id); err != nil {
			slog.Warn("hang up voice call", "conversation", c.conversationID, "error", err)
		}

		cancel()
	}

	_ = c.conn.Close()
}

// forward sends the loop a snapshot of the conversation after each change. The first read
// takes the latest 10 entries and later reads keep its start, so every turn this call hands
// off stays in view however many turns finish between two reads, as Codex hears each turn end.
func (c *voiceCall) forward(ctx context.Context, snapshots chan<- voiceSnapshot) error {
	root, err := os.OpenRoot(c.server.cfg.Clone().Workspace)
	if err != nil {
		return fmt.Errorf("open voice workspace: %w", err)
	}

	defer func() { _ = root.Close() }()

	from, limit := int64(0), 10

	for _, err := range c.server.sessions.Changes(ctx, c.conversationID) {
		var snapshot voiceSnapshot
		if err == nil {
			snapshot, from, err = c.snapshot(ctx, root, from, limit)
			limit = 0
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

// snapshot reads the entries from entry from on, or the latest limit entries, and returns
// where the read started.
func (c *voiceCall) snapshot(ctx context.Context, root *os.Root, from int64, limit int) (voiceSnapshot, int64, error) {
	snapshot := voiceSnapshot{voice: map[string]bool{}}

	view, err := c.server.sessions.ObserveHistory(ctx, c.conversationID, "", from, 0, limit, &backend.HistoryInventory{})
	if err != nil {
		return snapshot, 0, fmt.Errorf("read voice history: %w", err)
	}

	for i := range view.Entries {
		entry := &view.Entries[i]
		if entry.Synced && entry.SourceConversationID == "" {
			continue
		}

		messages, err := c.server.transcriptEntry(ctx, root, entry, c.conversationID, false, map[string]string{})
		if err != nil {
			return snapshot, 0, err
		}

		turn := voiceTurn{key: entry.Key}

		for _, message := range messages {
			switch {
			case message.Role == "user" && message.Principal != "": // System notes carry no principal and never supersede voice.
				turn.inputs = append(turn.inputs, message.InputId)
				snapshot.voice[message.InputId] = strings.Contains(message.Header, " media=Voice")
			case message.Role == "assistant" && message.Complete && !entry.Active: // A running turn's inputs still count.
				turn.reply = message.Text
			}
		}

		snapshot.turns = append(snapshot.turns, turn)
	}

	snapshot.questions, err = c.server.backend.PendingQuestions(ctx, c.conversationID)
	if err != nil {
		return snapshot, 0, fmt.Errorf("read voice questions: %w", err)
	}

	return snapshot, view.Inventory.From, nil
}

// voiceTail returns the longest suffix of text within limit bytes that starts on a rune boundary.
// voiceDelegationField escapes text for a realtime_delegation and bounds it to 4 KB as
// Codex does, keeping its start, or its end when tail is set, beside a "…".
func voiceDelegationField(text string, tail bool) string {
	const limit, marker = 4 * 1024, "…"

	text = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
	switch {
	case len(text) <= limit:
		return text
	case tail:
		return marker + voiceTail(text, limit-len(marker))
	default:
		return voiceCut(text, limit-len(marker)) + marker
	}
}

func voiceTail(text string, limit int) string {
	start := max(0, len(text)-limit)
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}

	return text[start:]
}
