package rpc

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
)

// publicVoice is OpenAI's public GPT-Live API, for api_key providers. Its URLs follow the
// provider's API base; sidebandURL and hangupURL are fmt patterns taking the session id.
type publicVoice struct {
	createURL, sidebandURL, hangupURL string
}

// newPublicVoice follows the provider's API base, sending the API key over plain HTTP only
// to localhost or a loopback IP.
func newPublicVoice(provider config.OpenAIConfig) (*publicVoice, error) {
	scheme, rest, _ := strings.Cut(strings.TrimSuffix(cmp.Or(provider.APIBaseURL, "https://api.openai.com/v1"), "/"), "://")
	secure := strings.TrimPrefix(strings.TrimPrefix(scheme, "http"), "ws")

	hostport, _, _ := strings.Cut(rest, "/")
	host := (&url.URL{Host: hostport}).Hostname()

	if ip, err := netip.ParseAddr(host); secure == "" && host != "localhost" && (err != nil || !ip.IsLoopback()) {
		return nil, errVoicePlainHTTP
	}

	return &publicVoice{
		createURL: "http" + secure + "://" + rest + "/live/sessions", sidebandURL: "ws" + secure + "://" + rest + "/live/sessions/%s/attach",
		hangupURL: "http" + secure + "://" + rest + "/live/sessions/%s/hangup",
	}, nil
}

func (*publicVoice) route() voiceRoute { return voiceRoutePublic }

func (*publicVoice) voices() []string {
	return []string{"marin", "cedar", "alloy", "ash", "ballad", "coral", "echo", "sage", "shimmer", "verse"}
}

func (*publicVoice) header(credentials voiceCredentials) http.Header {
	return http.Header{"Authorization": {"Bearer " + credentials.bearer}}
}

func (v *publicVoice) create(ctx context.Context, header http.Header, sdp, agent, voice string, history []*TranscriptEvent) (answer, id string, err error) {
	var body struct {
		Session   voiceSession `json:"session"`
		Transport struct {
			Type string `json:"type"`
			SDP  string `json:"sdp"`
		} `json:"transport"`
	}

	body.Session = newVoiceSession("gpt-live-1", agent, voice)
	body.Session.Input, body.Transport.Type, body.Transport.SDP = voiceSeed(history), "webrtc", sdp
	// The browser may only end or mute the call; results come from the server's sideband.
	body.Session.Client = json.RawMessage(`{"data_channel":{"allowed_client_events":["session.close","session.input_audio.mute","session.input_audio.unmute"]}}`)

	data, _ := json.Marshal(body) // Strings, string items and a fixed raw message always encode.

	resp, err := voicePost(ctx, v.createURL, header, data)
	if err != nil {
		return "", "", err
	}

	defer func() { _ = resp.Body.Close() }()

	var reply struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		Transport struct {
			SDP string `json:"sdp"`
		} `json:"transport"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return "", "", fmt.Errorf("decode voice answer: %w", err)
	}

	return reply.Transport.SDP, reply.Session.ID, nil
}

func (v *publicVoice) sideband(id string) string { return fmt.Sprintf(v.sidebandURL, id) }

func (v *publicVoice) hangup(ctx context.Context, header http.Header, id string) error {
	resp, err := voicePost(ctx, fmt.Sprintf(v.hangupURL, id), header, nil)
	if err != nil {
		return err
	}

	_ = resp.Body.Close()

	return nil
}

func (*publicVoice) kind(eventType string) voiceEventKind {
	switch eventType {
	case "session.input_transcript.delta":
		return voiceEventInput
	case "session.output_transcript.delta":
		return voiceEventOutput
	case "session.delegation.created":
		return voiceEventHandOff
	case "session.closed":
		return voiceEventClosed
	case "error":
		return voiceEventError
	default:
		return voiceEventIgnored
	}
}

func (*publicVoice) appends(id, text string) [][]byte {
	message := struct {
		Type         string  `json:"type"`
		DelegationID *string `json:"delegation_id"` // Required; null addresses the whole session.
		Content      string  `json:"content"`
	}{Type: "session.commentary.append"}

	if id != "" {
		message.DelegationID = &id
	}

	chunks := voiceChunks(text, 1500)
	messages := make([][]byte, 0, len(chunks))

	for _, chunk := range chunks {
		message.Content = chunk
		data, _ := json.Marshal(message) // Strings always encode.
		messages = append(messages, data)
	}

	return messages
}

// line is the last line: a speaker's speech extends it only while that speaker continues.
func (*publicVoice) line(said []voiceEvent, _ string) int { return len(said) - 1 }

func (*publicVoice) reconnects() bool { return false }
