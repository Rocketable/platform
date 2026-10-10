package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// codexVoice is Codex's internal GPT-Live route, for chatgpt providers. sidebandURL is
// a fmt pattern taking the call id.
type codexVoice struct {
	createURL, sidebandURL string
}

// newCodexVoice is never derived from APIBaseURL, so configuration cannot send a ChatGPT
// token elsewhere.
func newCodexVoice() *codexVoice {
	return &codexVoice{
		createURL:   "https://chatgpt.com/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas",
		sidebandURL: "wss://api.openai.com/v1/live/%s",
	}
}

func (*codexVoice) route() voiceRoute { return voiceRouteCodex }

// voices are Codex's realtime voices (codex-rs protocol RealtimeVoicesList).
func (*codexVoice) voices() []string {
	return []string{"cove", "juniper", "maple", "spruce", "ember", "vale", "breeze", "arbor", "sol"}
}

func (*codexVoice) header(credentials voiceCredentials) http.Header {
	return http.Header{
		"Authorization": {"Bearer " + credentials.bearer}, "Chatgpt-Account-Id": {credentials.accountID},
		"Openai-Alpha": {"quicksilver=v2"}, "Originator": {"codex_cli_rs"}, "X-Session-Id": {uuid.NewString()},
	}
}

func (v *codexVoice) create(ctx context.Context, header http.Header, sdp, agent, voice string, history []*TranscriptEvent) (answer, id string, err error) {
	body := struct {
		SDP     string       `json:"sdp"`
		Session voiceSession `json:"session"`
	}{SDP: sdp, Session: newVoiceSession("gpt-live-1-codex", agent, voice)}
	body.Session.InitialItems = voiceSeed(history)

	data, _ := json.Marshal(body) // Strings and string items always encode.

	resp, err := voicePost(ctx, v.createURL, header, data)
	if err != nil {
		return "", "", err
	}

	defer func() { _ = resp.Body.Close() }()

	id = path.Base(resp.Header.Get("Location"))
	if !strings.HasPrefix(id, "rtc_") && uuid.Validate(id) != nil {
		return "", "", errVoiceCallID
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("read voice answer: %w", err)
	}

	return string(raw), id, nil
}

func (v *codexVoice) sideband(id string) string { return fmt.Sprintf(v.sidebandURL, id) }

// hangup sends session.close over a fresh sideband: the route has no hangup endpoint.
func (v *codexVoice) hangup(ctx context.Context, header http.Header, id string) error {
	conn, err := voiceDial(ctx, v.sideband(id), header)
	if err != nil {
		return err
	}

	defer func() { _ = conn.Close() }()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(voiceCloseMessage)); err != nil {
		return fmt.Errorf("close voice call: %w", err)
	}

	return nil
}

func (*codexVoice) kind(eventType string) voiceEventKind {
	switch eventType {
	case "input_transcript.added":
		return voiceEventInput
	case "output_transcript.added":
		return voiceEventOutput
	case "turn.done":
		return voiceEventTurnDone
	case "delegation.created":
		return voiceEventHandOff
	case "session.closed":
		return voiceEventClosed
	case "error":
		return voiceEventError
	default:
		return voiceEventIgnored
	}
}

func (*codexVoice) appends(id, text string) [][]byte {
	message := struct {
		Type             string         `json:"type"`
		DelegationItemID string         `json:"delegation_item_id,omitempty"`
		Channel          string         `json:"channel"`
		Content          []voiceContent `json:"content"`
	}{Type: "delegation.context.append", DelegationItemID: id, Channel: "speakable"}

	if id == "" {
		message.Type = "session.context.append"
	}

	chunks := voiceChunks(text, 500)
	messages := make([][]byte, 0, len(chunks))

	for _, chunk := range chunks {
		message.Content = []voiceContent{{Type: "input_text", Text: chunk}}
		data, _ := json.Marshal(message) // Strings and string slices always encode.
		messages = append(messages, data)
	}

	return messages
}

// line is the speaker's latest line, as Codex update_active_transcript: both speakers'
// transcripts interleave on this route.
func (*codexVoice) line(said []voiceEvent, role string) int {
	i := len(said) - 1
	for i >= 0 && said[i].role != role {
		i--
	}

	return i
}

func (*codexVoice) reconnects() bool { return true }
