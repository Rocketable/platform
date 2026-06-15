package webui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/voice"
)

const (
	voiceSocketPath      = VoiceModePath + "/ws"
	playbackPathPrefix   = VoiceModePath + "/playback/"
	utteranceCheckEvery  = 100 * time.Millisecond
	defaultChunkPreroll  = 4
	maxAudioMessageSize  = 512 * 1024
	maxAudioSendQueue    = 32
	browserCaptureMIME   = "audio/webm;codecs=opus"
	transportVersion     = "ws_opus_v1"
	webMClusterElementID = "\x1f\x43\xb6\x75"
)

type transcriber interface {
	Transcribe(context.Context, string) (string, error)
}

type synthesizer interface {
	Synthesize(context.Context, string) (io.ReadCloser, error)
	SynthesizeFormat(context.Context, string, string) (io.ReadCloser, error)
}

type clientMessage struct {
	Type      string `json:"type"`
	Transport string `json:"transport,omitempty"`
	MIMEType  string `json:"mime_type,omitempty"`
	Sequence  uint64 `json:"sequence,omitempty"`
	Speaking  bool   `json:"speaking,omitempty"`
	Muted     bool   `json:"muted,omitempty"`
	Data      string `json:"data,omitempty"`
}

type serverMessage struct {
	Type        string `json:"type"`
	SessionID   string `json:"session_id,omitempty"`
	Transport   string `json:"transport,omitempty"`
	Status      string `json:"status,omitempty"`
	Message     string `json:"message,omitempty"`
	PlaybackURL string `json:"playback_url,omitempty"`
	MIMEType    string `json:"mime_type,omitempty"`
	Text        string `json:"text,omitempty"`
}

type playbackAsset struct {
	MIMEType string
	Data     []byte
}

type playbackFormat struct {
	ResponseFormat string
	MIMEType       string
}

type bufferedChunk struct {
	Sequence   uint64
	ReceivedAt time.Time
	Speaking   bool
	Data       []byte
}

type utteranceBuffer struct {
	HeaderData              []byte
	MIMEType                string
	StartedAt, LastSpeechAt time.Time
	Chunks                  []bufferedChunk
}

type voiceHub struct {
	log           *slog.Logger
	cancel        context.CancelFunc
	done          <-chan struct{}
	wg            sync.WaitGroup
	transcriber   transcriber
	tts           synthesizer
	publisher     *voice.TranscriptionPublisher
	silenceWindow time.Duration
	prerollChunks int
	upgrader      websocket.Upgrader

	mu        sync.Mutex
	sessions  map[string]*voiceSession
	playbacks map[string]playbackAsset
	assetIDs  map[string][]string
	closed    bool
}

type voiceSession struct {
	id   string
	hub  *voiceHub
	conn *websocket.Conn

	send chan *serverMessage

	mu           sync.Mutex
	closed       bool
	mimeType     string
	muted        bool
	webmHeader   []byte
	preroll      []bufferedChunk
	current      *utteranceBuffer
	lastSequence uint64
	turnText     map[string]string
}

func newVoiceHub(ctx context.Context, logger *slog.Logger, transcriber transcriber, tts synthesizer, publisher *voice.TranscriptionPublisher) *voiceHub {
	hubCtx, cancel := context.WithCancel(ctx)
	hub := &voiceHub{
		log: logger.With("component", "web_ui_voice"), cancel: cancel, done: hubCtx.Done(),
		transcriber: transcriber, tts: tts, publisher: publisher,
		silenceWindow: voice.UtteranceSilenceWindow, prerollChunks: defaultChunkPreroll,
		upgrader: websocket.Upgrader{CheckOrigin: allowWebsocketOrigin}, sessions: map[string]*voiceSession{}, playbacks: map[string]playbackAsset{}, assetIDs: map[string][]string{},
	}

	go hub.monitorUtterances(hubCtx)

	return hub
}

//nolint:funcorder // Kept near hub construction because it is part of startup lifecycle.
func (h *voiceHub) monitorUtterances(ctx context.Context) {
	ticker := time.NewTicker(utteranceCheckEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.finalizeSilentUtterances(time.Now())
		}
	}
}

//nolint:funcorder // Kept near the monitor loop because it is part of utterance completion.
func (h *voiceHub) finalizeSilentUtterances(now time.Time) {
	h.mu.Lock()

	sessions := make([]*voiceSession, 0, len(h.sessions))
	for _, session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.mu.Unlock()

	for _, session := range sessions {
		session.mu.Lock()
		if session.closed || session.current == nil {
			session.mu.Unlock()
			continue
		}

		if now.Sub(session.current.LastSpeechAt) >= h.silenceWindow {
			session.finalizeCurrentLocked()
		}
		session.mu.Unlock()
	}
}

func allowWebsocketOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}

	u, err := url.ParseRequestURI(origin)
	if err != nil {
		return false
	}

	return strings.EqualFold(u.Host, r.Host)
}

func stateServerMessage(status, text string) *serverMessage {
	return &serverMessage{Type: "state", Status: status, Message: text}
}

func (h *voiceHub) SendResponse(ctx context.Context, msg *events.OutboundMessage) error {
	sessionID := strings.TrimSpace(msg.WebSessionID)
	if sessionID == "" {
		return nil
	}

	session := h.session(sessionID)
	if session == nil {
		return nil
	}

	if msg.ProgressText != "" && strings.TrimSpace(msg.Text) == "" {
		session.enqueue(stateServerMessage("thinking", "Assistant is thinking."))
		return nil
	}

	text := strings.TrimSpace(msg.Text)
	if msg.Complete {
		text = strings.TrimSpace(text + "\n\n" + events.AttachmentNamesSpeech(msg.Attachments))
	}

	if text == "" {
		return nil
	}

	turnID := strings.TrimSpace(msg.TurnID)
	suffix := text

	if turnID != "" {
		session.mu.Lock()
		previous := session.turnText[turnID]
		session.mu.Unlock()

		if previous != "" && strings.HasPrefix(text, previous) {
			suffix = strings.TrimSpace(text[len(previous):])
		}
	}

	if suffix == "" {
		if msg.Complete && turnID != "" {
			session.mu.Lock()
			delete(session.turnText, turnID)
			session.mu.Unlock()
		}

		return nil
	}

	if h.tts == nil {
		session.enqueue(stateServerMessage("error", "Speech playback is unavailable."))
		return nil
	}

	assetID := rand.Text()

	asset, err := h.synthesizeBrowserPlayback(ctx, suffix)
	if err != nil {
		session.enqueue(stateServerMessage("error", "Assistant playback synthesis failed."))
		return fmt.Errorf("synthesize browser playback asset: %w", err)
	}

	if !h.enqueuePlayback(sessionID, assetID, asset, text) {
		return nil
	}

	if turnID != "" {
		session.mu.Lock()
		if msg.Complete {
			delete(session.turnText, turnID)
		} else {
			session.turnText[turnID] = text
		}
		session.mu.Unlock()
	}

	return nil
}

func (h *voiceHub) synthesizeBrowserPlayback(ctx context.Context, text string) (playbackAsset, error) {
	formats := []playbackFormat{{ResponseFormat: "mp3", MIMEType: "audio/mpeg"}, {ResponseFormat: "opus", MIMEType: "audio/ogg"}}

	var errCombined error

	for _, format := range formats {
		stream, err := h.tts.SynthesizeFormat(ctx, text, format.ResponseFormat)
		if err != nil {
			errCombined = errors.Join(errCombined, fmt.Errorf("tts format %s: %w", format.ResponseFormat, err))
			continue
		}

		data, errRead := io.ReadAll(stream)
		errClose := stream.Close()

		if errRead != nil {
			errCombined = errors.Join(errCombined, fmt.Errorf("read synthesized %s audio: %w", format.ResponseFormat, errRead))
			continue
		}

		if errClose != nil {
			errCombined = errors.Join(errCombined, fmt.Errorf("close synthesized %s audio: %w", format.ResponseFormat, errClose))
			continue
		}

		return playbackAsset{MIMEType: format.MIMEType, Data: data}, nil
	}

	return playbackAsset{}, errCombined
}

func (h *voiceHub) handleWebsocket(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Error("upgrade browser voice websocket", "error", err)
		return
	}

	session := new(voiceSession)

	session.id = rand.Text()
	session.hub = h
	session.conn = conn
	session.send = make(chan *serverMessage, maxAudioSendQueue)
	session.turnText = map[string]string{}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()

		_ = conn.Close()

		return
	}

	h.sessions[session.id] = session
	h.mu.Unlock()

	session.enqueue(&serverMessage{Type: "ready", SessionID: session.id, Transport: transportVersion, Status: "connected", Message: "Browser voice session connected."})

	go session.writeLoop()

	session.readLoop()
}

func (h *voiceHub) servePlayback(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, playbackPathPrefix)
	if strings.TrimSpace(id) == "" {
		http.NotFound(w, r)
		return
	}

	h.mu.Lock()

	asset, ok := h.playbacks[id]
	if ok {
		delete(h.playbacks, id)
		h.removeAssetIDLocked(id)
	}
	h.mu.Unlock()

	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", asset.MIMEType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(asset.Data)
}

func (h *voiceHub) session(id string) *voiceSession {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.sessions[id]
}

func (h *voiceHub) close(ctx context.Context) error {
	h.mu.Lock()
	sessions := []*voiceSession(nil)

	if !h.closed {
		h.closed = true
		if h.cancel != nil {
			h.cancel()
		}

		sessions = make([]*voiceSession, 0, len(h.sessions))
		for _, session := range h.sessions {
			sessions = append(sessions, session)
		}

		h.playbacks = map[string]playbackAsset{}
		h.assetIDs = map[string][]string{}
	}
	h.mu.Unlock()

	for _, session := range sessions {
		session.close()
	}

	done := make(chan struct{})

	go func() {
		h.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for browser voice shutdown: %w", ctx.Err())
	}
}

func (s *voiceSession) readLoop() {
	defer s.close()

	s.conn.SetReadLimit(maxAudioMessageSize)

	for {
		messageType, payload, err := s.conn.ReadMessage()
		if err != nil {
			return
		}

		if messageType != websocket.TextMessage {
			s.enqueue(stateServerMessage("error", "Unsupported browser voice message type."))
			return
		}

		message := new(clientMessage)
		if err := json.Unmarshal(payload, message); err != nil {
			s.enqueue(stateServerMessage("error", "Invalid browser voice message."))
			return
		}

		if err := s.handleClientMessage(message); err != nil {
			s.hub.log.Error("handle browser voice message", "error", err, "session_id", s.id, "type", message.Type)
			s.enqueue(stateServerMessage("error", err.Error()))

			return
		}
	}
}

func (s *voiceSession) writeLoop() {
	for message := range s.send {
		if err := s.conn.WriteJSON(message); err != nil {
			return
		}
	}
}

func (s *voiceSession) handleClientMessage(message *clientMessage) error {
	switch message.Type {
	case "hello":
		return s.handleHello(message)
	case "audio":
		return s.handleAudio(message)
	case "mute":
		s.mu.Lock()
		s.muted = message.Muted
		s.mu.Unlock()

		if message.Muted {
			s.enqueue(stateServerMessage("muted", "Microphone muted."))
		} else {
			s.enqueue(stateServerMessage("listening", "Listening."))
		}

		return nil
	default:
		return fmt.Errorf("unsupported browser voice message type %q", message.Type)
	}
}

func (s *voiceSession) handleHello(message *clientMessage) error {
	if s.hub.transcriber == nil || s.hub.publisher == nil {
		return errors.New("browser voice loop is not configured")
	}

	if strings.TrimSpace(message.Transport) != transportVersion {
		return fmt.Errorf("unsupported browser voice transport %q", message.Transport)
	}

	mimeType := normalizeBrowserOpusMIMEType(message.MIMEType)
	if mimeType == "" {
		return fmt.Errorf("unsupported browser Opus MIME type %q", message.MIMEType)
	}

	s.mu.Lock()
	s.mimeType = mimeType
	s.mu.Unlock()

	s.enqueue(stateServerMessage("listening", "Connected and listening."))

	return nil
}

func (s *voiceSession) handleAudio(message *clientMessage) error {
	s.mu.Lock()
	mimeType := s.mimeType
	muted := s.muted
	lastSequence := s.lastSequence
	s.mu.Unlock()

	if mimeType == "" {
		return errors.New("browser voice session is not initialized")
	}

	if muted {
		return nil
	}

	if message.Sequence <= lastSequence {
		return nil
	}

	data, err := base64.StdEncoding.DecodeString(message.Data)
	if err != nil {
		return fmt.Errorf("decode browser voice chunk: %w", err)
	}

	if mimeType == browserCaptureMIME {
		headerData, bodyData := splitWebMHeader(data)
		if len(headerData) > 0 && len(s.webmHeader) == 0 {
			s.webmHeader = append([]byte(nil), headerData...)
		}

		if len(bodyData) > 0 {
			data = bodyData
		} else if len(headerData) > 0 {
			return nil
		}
	}

	chunk := bufferedChunk{
		Sequence:   message.Sequence,
		ReceivedAt: time.Now(),
		Speaking:   message.Speaking,
		Data:       data,
	}

	s.mu.Lock()
	s.lastSequence = message.Sequence
	s.handleBufferedChunkLocked(mimeType, chunk)
	s.mu.Unlock()

	return nil
}

func (s *voiceSession) handleBufferedChunkLocked(mimeType string, chunk bufferedChunk) {
	if len(chunk.Data) == 0 {
		return
	}

	s.preroll = append(s.preroll, cloneBufferedChunk(chunk))
	if len(s.preroll) > s.hub.prerollChunks {
		s.preroll = s.preroll[len(s.preroll)-s.hub.prerollChunks:]
	}

	if s.current == nil && chunk.Speaking {
		current := &utteranceBuffer{HeaderData: append([]byte(nil), s.webmHeader...), MIMEType: mimeType, StartedAt: chunk.ReceivedAt, LastSpeechAt: chunk.ReceivedAt, Chunks: cloneBufferedChunks(s.preroll)}
		s.current = current
		s.preroll = nil
		s.hub.log.Info("started browser voice utterance capture", "session_id", s.id, "chunks", len(current.Chunks))

		return
	}

	if s.current == nil {
		return
	}

	s.current.Chunks = append(s.current.Chunks, cloneBufferedChunk(chunk))
	if chunk.Speaking {
		s.current.LastSpeechAt = chunk.ReceivedAt
		return
	}

	if chunk.ReceivedAt.Sub(s.current.LastSpeechAt) < s.hub.silenceWindow {
		return
	}

	s.finalizeCurrentLocked()
}

func (s *voiceSession) finalizeCurrentLocked() {
	current := s.current
	s.current = nil
	s.hub.startUtteranceWork(s.id, current)
}

func (s *voiceSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}

	s.closed = true
	current := s.current
	s.current = nil
	s.mu.Unlock()

	if current != nil {
		s.hub.startUtteranceWork(s.id, current)
	}

	s.hub.mu.Lock()
	for _, assetID := range s.hub.assetIDs[s.id] {
		delete(s.hub.playbacks, assetID)
	}

	delete(s.hub.sessions, s.id)
	delete(s.hub.assetIDs, s.id)
	s.hub.mu.Unlock()

	close(s.send)
	_ = s.conn.Close()
}

func (h *voiceHub) startUtteranceWork(sessionID string, utterance *utteranceBuffer) {
	h.mu.Lock()

	closed := h.closed
	if !closed {
		h.wg.Add(1)
	}
	h.mu.Unlock()

	if closed {
		return
	}

	go func() {
		defer h.wg.Done()

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		go func() {
			select {
			case <-h.done:
				cancel()
			case <-ctx.Done():
			}
		}()

		h.processUtterance(ctx, sessionID, utterance)
	}()
}

func (s *voiceSession) enqueue(message *serverMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return false
	}

	select {
	case s.send <- message:
		return true
	default:
		s.hub.log.Warn("dropping browser voice websocket message", "session_id", s.id, "type", message.Type)

		return false
	}
}

func (h *voiceHub) processUtterance(ctx context.Context, sessionID string, utterance *utteranceBuffer) {
	data := flattenUtteranceChunks(utterance.Chunks)
	if utterance.MIMEType == browserCaptureMIME {
		data = assembleWebMUtterance(utterance.HeaderData, utterance.Chunks)
	}

	if len(data) == 0 {
		return
	}

	tempFile, err := os.CreateTemp("", "rocketclaw-web-voice-*.webm")
	if err != nil {
		h.log.Error("create browser voice temp file", "error", err, "session_id", sessionID)
		return
	}

	filename := tempFile.Name()
	if _, err := tempFile.Write(data); err != nil {
		_ = tempFile.Close()
		_ = os.Remove(filename)

		h.log.Error("write browser voice temp file", "error", err, "session_id", sessionID)

		return
	}

	if err := tempFile.Close(); err != nil {
		_ = os.Remove(filename)

		h.log.Error("close browser voice temp file", "error", err, "session_id", sessionID)

		return
	}

	defer func() { _ = os.Remove(filename) }()

	h.log.Info(
		"transcribing browser voice utterance",
		"session_id", sessionID,
		"mime_type", utterance.MIMEType,
		"chunks", len(utterance.Chunks),
		"bytes", len(data),
	)

	text, err := h.transcriber.Transcribe(ctx, filename)
	if err != nil {
		h.log.Error("transcribe browser voice utterance", "error", err, "session_id", sessionID)
		return
	}

	text = strings.TrimSpace(text)
	if text == "" {
		h.log.Info("browser voice transcription returned empty text", "session_id", sessionID)
		return
	}

	if _, err := h.publisher.PublishTranscription(ctx, text, sessionID); err != nil {
		h.log.Error("publish browser voice transcription", "error", err, "session_id", sessionID)
		return
	}

	h.log.Info("published browser voice transcription", "session_id", sessionID, "text_len", len(text), "text", text)
}

func (h *voiceHub) removeAssetIDLocked(id string) {
	for sessionID, assetIDs := range h.assetIDs {
		filtered := slices.DeleteFunc(assetIDs, func(assetID string) bool { return assetID == id })

		if len(filtered) == 0 {
			delete(h.assetIDs, sessionID)
			continue
		}

		h.assetIDs[sessionID] = append([]string(nil), filtered...)
	}
}

func (h *voiceHub) enqueuePlayback(sessionID, assetID string, asset playbackAsset, text string) bool {
	h.mu.Lock()

	if h.closed {
		h.mu.Unlock()
		return false
	}

	session := h.sessions[sessionID]
	if session == nil {
		h.mu.Unlock()
		return false
	}

	h.playbacks[assetID] = asset
	h.assetIDs[sessionID] = append(h.assetIDs[sessionID], assetID)
	h.mu.Unlock()

	if session.enqueue(&serverMessage{Type: "playback", Status: "playing", Message: "Playing assistant response.", PlaybackURL: playbackPathPrefix + assetID, MIMEType: asset.MIMEType, Text: text}) {
		return true
	}

	h.mu.Lock()
	delete(h.playbacks, assetID)
	h.removeAssetIDLocked(assetID)
	h.mu.Unlock()

	return false
}

func normalizeBrowserOpusMIMEType(mimeType string) string {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))

	mimeType = strings.ReplaceAll(mimeType, " ", "")
	if mimeType == browserCaptureMIME {
		return browserCaptureMIME
	}

	return ""
}

func splitWebMHeader(data []byte) (headerData, bodyData []byte) {
	clusterOffset := bytes.Index(data, []byte(webMClusterElementID))
	switch {
	case clusterOffset > 0:
		return append([]byte(nil), data[:clusterOffset]...), append([]byte(nil), data[clusterOffset:]...)
	case clusterOffset == 0:
		return nil, append([]byte(nil), data...)
	default:
		return append([]byte(nil), data...), nil
	}
}

func assembleWebMUtterance(headerData []byte, chunks []bufferedChunk) []byte {
	bodyData := flattenUtteranceChunks(chunks)
	if len(headerData) == 0 {
		return bodyData
	}

	assembled := make([]byte, 0, len(headerData)+len(bodyData))
	assembled = append(assembled, headerData...)
	assembled = append(assembled, bodyData...)

	return assembled
}

func flattenUtteranceChunks(chunks []bufferedChunk) []byte {
	total := 0
	for _, chunk := range chunks {
		total += len(chunk.Data)
	}

	if total == 0 {
		return nil
	}

	data := make([]byte, 0, total)
	for _, chunk := range chunks {
		data = append(data, chunk.Data...)
	}

	return data
}

func cloneBufferedChunks(chunks []bufferedChunk) []bufferedChunk {
	cloned := make([]bufferedChunk, 0, len(chunks))
	for _, chunk := range chunks {
		cloned = append(cloned, cloneBufferedChunk(chunk))
	}

	return cloned
}

func cloneBufferedChunk(chunk bufferedChunk) bufferedChunk {
	chunk.Data = append([]byte(nil), chunk.Data...)
	return chunk
}
