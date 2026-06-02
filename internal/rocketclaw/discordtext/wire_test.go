package discordtext

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
)

func TestWireRESTRequests(t *testing.T) {
	transport := &fakeTextTransport{responses: []fakeTextResponse{
		{status: http.StatusOK, body: `{"id":"C123","guild_id":"G123","type":0}`},
		{status: http.StatusNoContent},
		{status: http.StatusOK, body: `{"id":"M1","channel_id":"C123","content":"hello"}`},
		{status: http.StatusOK, body: `{"id":"T1","parent_id":"C123","type":11}`},
		{status: http.StatusOK, body: `{"id":"A1","channel_id":"C123"}`},
	}}
	w := &wire{client: &http.Client{Transport: transport}, token: "Bot token", log: slog.New(slog.DiscardHandler)}

	channel, err := w.channel("C123")
	require.NoError(t, err)
	assert.Equal(t, "G123", channel.GuildID)

	require.NoError(t, w.typing("C123"))

	posted, err := w.sendMessage("C123", messageSend{Content: "hello", Reference: &messageReference{MessageID: "U1"}})
	require.NoError(t, err)
	assert.Equal(t, "M1", posted.ID)

	thread, err := w.createThread("C123", "M1", threadStart{Name: "thread"})
	require.NoError(t, err)
	assert.Equal(t, "T1", thread.ID)

	require.NoError(t, w.sendAttachments("C123", []events.OutboundAttachment{{Name: "note.txt", Data: []byte("body")}}))

	require.Len(t, transport.requests, 5)
	assert.Equal(t, "GET", transport.requests[0].Method)
	assert.Equal(t, "/api/v10/channels/C123", transport.requests[0].URL.Path)
	assert.Equal(t, "Bot token", transport.requests[0].Header.Get("Authorization"))
	assert.Equal(t, "POST", transport.requests[1].Method)
	assert.Equal(t, "/api/v10/channels/C123/typing", transport.requests[1].URL.Path)
	assert.Equal(t, "POST", transport.requests[2].Method)
	assert.Equal(t, "/api/v10/channels/C123/messages", transport.requests[2].URL.Path)
	assert.Contains(t, transport.bodies[2], `"content":"hello"`)
	assert.Equal(t, "POST", transport.requests[3].Method)
	assert.Equal(t, "/api/v10/channels/C123/messages/M1/threads", transport.requests[3].URL.Path)
	assert.Contains(t, transport.bodies[3], `"name":"thread"`)
	assert.Equal(t, "POST", transport.requests[4].Method)
	assert.Equal(t, "/api/v10/channels/C123/messages", transport.requests[4].URL.Path)
	assert.Contains(t, transport.bodies[4], "note.txt")
	assert.Contains(t, transport.bodies[4], "body")
}

func TestWireRESTStatusError(t *testing.T) {
	transport := &fakeTextTransport{responses: []fakeTextResponse{{status: http.StatusBadRequest, body: `{"message":"bad"}`}}}
	w := &wire{client: &http.Client{Transport: transport}, token: "Bot token", log: slog.New(slog.DiscardHandler)}

	_, err := w.channel("C123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discord API GET /channels/C123 failed")
}

func TestWireRESTDecodeError(t *testing.T) {
	transport := &fakeTextTransport{responses: []fakeTextResponse{{status: http.StatusOK, body: `{`}}}
	w := &wire{client: &http.Client{Transport: transport}, token: "Bot token", log: slog.New(slog.DiscardHandler)}

	_, err := w.channel("C123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode Discord text API response")
}

func TestWireDispatchEvents(t *testing.T) {
	w := &wire{events: make(chan textEvent, 2), ready: make(chan struct{}), log: slog.New(slog.DiscardHandler)}
	w.handleDispatch("READY", rawJSON(t, map[string]any{"user": map[string]any{"id": "BOT"}}))

	select {
	case <-w.ready:
	default:
		t.Fatal("READY did not close ready channel")
	}

	assert.Equal(t, "BOT", w.userID())

	w.handleDispatch("MESSAGE_CREATE", rawJSON(t, map[string]any{"id": "M1", "channel_id": "C123", "content": "hello", "author": map[string]any{"id": "human"}}))
	w.handleDispatch("MESSAGE_REACTION_ADD", rawJSON(t, map[string]any{"user_id": "human", "channel_id": "T1", "message_id": "M1", "emoji": map[string]any{"name": discordSummaryEmoji}}))

	messageEvent := <-w.events
	require.NotNil(t, messageEvent.message)
	assert.Equal(t, "hello", messageEvent.message.Message.Content)

	reactionEvent := <-w.events
	require.NotNil(t, reactionEvent.reaction)
	assert.Equal(t, discordSummaryEmoji, reactionEvent.reaction.Emoji.Name)
}

func TestWireGatewayHelpers(t *testing.T) {
	w := &wire{events: make(chan textEvent, 1), ready: make(chan struct{}), log: slog.New(slog.DiscardHandler)}

	assert.Empty(t, w.userID())
	require.NoError(t, w.Close())
	require.Error(t, w.writeGatewayJSON(map[string]string{"type": "test"}))
	w.gatewayHeartbeat(nil, 0)
	w.handleGatewayEvent(nil, gatewayOpDispatch, "READY", rawJSON(t, map[string]any{"user": map[string]any{"id": "BOT"}}))
	w.handleGatewayEvent(nil, gatewayOpHeartbeat, "", nil)

	select {
	case <-w.ready:
	default:
		t.Fatal("gateway dispatch did not close ready channel")
	}
}

func rawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return data
}

type fakeTextResponse struct {
	status int
	body   string
}

type fakeTextTransport struct {
	requests  []*http.Request
	bodies    []string
	responses []fakeTextResponse
}

func (f *fakeTextTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte

	if req.Body != nil {
		data, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("read fake Discord text request body: %w", err)
		}

		body = data
	}

	f.requests = append(f.requests, req)
	f.bodies = append(f.bodies, string(body))
	response := f.responses[0]
	f.responses = f.responses[1:]

	status := response.status
	if status == 0 {
		status = http.StatusOK
	}

	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(response.body)), Header: make(http.Header), Request: req}, nil
}
