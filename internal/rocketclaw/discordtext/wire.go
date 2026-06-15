package discordtext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
)

const (
	discordAPI        = "https://discord.com/api/v10"
	discordGatewayURL = "wss://gateway.discord.gg/?v=10&encoding=json"
)

const (
	// Guild Text is the configured primary Discord text channel type.
	// https://docs.discord.com/developers/resources/channel#channel-object-channel-types
	channelTypeGuildText = 0
	channelTypeDM        = 1
	// Guild News Thread, Public Thread, and Private Thread are the Discord thread channel types we route managed replies through.
	// https://docs.discord.com/developers/resources/channel#channel-object-channel-types
	channelTypeGuildNewsThread    = 10
	channelTypeGuildPublicThread  = 11
	channelTypeGuildPrivateThread = 12
)

type gatewayOpcode int

type gatewayHeartbeatPayload struct {
	Op gatewayOpcode `json:"op"`
	D  any           `json:"d"`
}

// Gateway opcodes are named by Discord at:
// https://docs.discord.com/developers/topics/opcodes-and-status-codes#gateway-gateway-opcodes
const (
	// Dispatch delivers gateway events such as READY, MESSAGE_CREATE, and MESSAGE_REACTION_ADD.
	gatewayOpDispatch gatewayOpcode = 0
	// Heartbeat keeps the main gateway session alive.
	gatewayOpHeartbeat gatewayOpcode = 1
	// Identify starts the main gateway session and declares requested intents.
	gatewayOpIdentify gatewayOpcode = 2
	// Hello provides the main gateway heartbeat interval.
	gatewayOpHello gatewayOpcode = 10
)

type wireConfig struct {
	token string
	log   *slog.Logger
}

// textUser is the subset of Discord's User object needed for authors and the READY event's current user.
// https://docs.discord.com/developers/resources/user#user-object
type textUser struct {
	ID  string `json:"id"`
	Bot bool   `json:"bot"`
}

// textChannel is the subset of Discord's Channel object needed to resolve configured text channels and threads.
// https://docs.discord.com/developers/resources/channel#channel-object
type textChannel struct {
	ID       string `json:"id"`
	GuildID  string `json:"guild_id"`
	ParentID string `json:"parent_id"`
	Type     int    `json:"type"`
}

// messageReference is Discord's reply reference payload used to reply to a message and detect response-rooted threads.
// https://docs.discord.com/developers/resources/message#message-reference-object-message-reference-structure
type messageReference struct {
	MessageID       string `json:"message_id"`
	ChannelID       string `json:"channel_id,omitempty"`
	FailIfNotExists *bool  `json:"fail_if_not_exists,omitempty"`
}

// textMessage is the subset of Discord's Message object used by MESSAGE_CREATE and REST message responses.
// https://docs.discord.com/developers/resources/message#message-object
type textMessage struct {
	ID               string            `json:"id"`
	ChannelID        string            `json:"channel_id"`
	GuildID          string            `json:"guild_id"`
	Content          string            `json:"content"`
	Author           *textUser         `json:"author"`
	MessageReference *messageReference `json:"message_reference"`
	Attachments      []textAttachment  `json:"attachments"`
	Thread           *textChannel      `json:"thread"`
}

type textAttachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	URL         string `json:"url"`
	Size        int    `json:"size"`
}

// messageCreate carries the MESSAGE_CREATE dispatch payload.
// https://docs.discord.com/developers/events/gateway-events#message-create
type messageCreate struct {
	Message *textMessage
}

// textEmoji is the subset of Discord's Emoji object carried by MESSAGE_REACTION_ADD.
// https://docs.discord.com/developers/resources/emoji#emoji-object
type textEmoji struct {
	Name string `json:"name"`
}

// reactionAdd carries the MESSAGE_REACTION_ADD dispatch payload used for managed-thread summaries.
// https://docs.discord.com/developers/events/gateway-events#message-reaction-add
type reactionAdd struct {
	UserID    string    `json:"user_id"`
	ChannelID string    `json:"channel_id"`
	MessageID string    `json:"message_id"`
	Emoji     textEmoji `json:"emoji"`
}

// messageSend is the REST Create Message payload subset used for text replies.
// https://docs.discord.com/developers/resources/message#create-message-jsonform-params
type messageSend struct {
	Content   string            `json:"content,omitempty"`
	Reference *messageReference `json:"message_reference,omitempty"`
}

// postedMessage is the REST Create Message response subset used to record response checkpoints.
// https://docs.discord.com/developers/resources/message#message-object
type postedMessage struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	Content   string `json:"content"`
}

// threadStart is the REST Start Thread from Message payload subset used for managed and response-rooted threads.
// https://docs.discord.com/developers/resources/channel#start-thread-from-message-json-params
type threadStart struct {
	Name                string `json:"name"`
	AutoArchiveDuration int    `json:"auto_archive_duration,omitempty"`
}

// textEvent is the internal event shape decoded from Discord Gateway dispatches.
type textEvent struct {
	message  *messageCreate
	reaction *reactionAdd
}

type wire struct {
	dialer *websocket.Dialer
	client *http.Client
	log    *slog.Logger
	token  string
	events chan textEvent
	ready  chan struct{}

	mu     sync.Mutex
	conn   *websocket.Conn
	closed bool
	user   *textUser
}

func newWire(cfg wireConfig) (*wire, error) {
	wire := &wire{dialer: websocket.DefaultDialer, client: &http.Client{Timeout: 15 * time.Second}, log: cfg.log, token: cfg.token, events: make(chan textEvent, 32), ready: make(chan struct{})}
	if err := wire.openGateway(); err != nil {
		return nil, err
	}

	return wire, nil
}

func (w *wire) Close() error {
	w.mu.Lock()
	w.closed = true
	conn := w.conn
	w.conn = nil
	w.mu.Unlock()

	if conn != nil {
		if err := conn.Close(); err != nil {
			return fmt.Errorf("close Discord text gateway websocket: %w", err)
		}
	}

	return nil
}

func (w *wire) openGateway() error {
	conn, resp, err := w.dialer.Dial(discordGatewayURL, nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	if err != nil {
		return fmt.Errorf("dial Discord text gateway: %w", err)
	}

	w.mu.Lock()
	w.conn = conn
	w.closed = false
	w.mu.Unlock()

	go w.gatewayReadLoop(conn)

	select {
	case <-w.ready:
		return nil
	case <-time.After(10 * time.Second):
		_ = conn.Close()
		return errors.New("discord text gateway READY timeout")
	}
}

func (w *wire) userID() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.user == nil {
		return ""
	}

	return w.user.ID
}

func (w *wire) channel(channelID string) (*textChannel, error) {
	var channel textChannel
	if err := w.restJSON(http.MethodGet, "/channels/"+channelID, nil, &channel); err != nil {
		return nil, err
	}

	return &channel, nil
}

func (w *wire) message(channelID, messageID string) (*textMessage, error) {
	var message textMessage
	if err := w.restJSON(http.MethodGet, "/channels/"+channelID+"/messages/"+messageID, nil, &message); err != nil {
		return nil, err
	}

	return &message, nil
}

func (w *wire) downloadAttachment(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create Discord attachment download request: %w", err)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download Discord attachment: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download Discord attachment: %s", resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read Discord attachment: %w", err)
	}

	if int64(len(data)) > limit {
		return nil, errors.New("discord attachment download exceeded size limit")
	}

	return data, nil
}

func (w *wire) typing(channelID string) error {
	return w.restJSON(http.MethodPost, "/channels/"+channelID+"/typing", nil, nil)
}

func (w *wire) sendMessage(channelID string, message messageSend) (*postedMessage, error) {
	var posted postedMessage
	if err := w.restJSON(http.MethodPost, "/channels/"+channelID+"/messages", message, &posted); err != nil {
		return nil, err
	}

	return &posted, nil
}

func (w *wire) editMessage(channelID, messageID string, message messageSend) error {
	return w.restJSON(http.MethodPatch, "/channels/"+channelID+"/messages/"+messageID, message, nil)
}

func (w *wire) deleteMessage(channelID, messageID string) error {
	return w.restJSON(http.MethodDelete, "/channels/"+channelID+"/messages/"+messageID, nil, nil)
}

func (w *wire) addReaction(channelID, messageID, emoji string) error {
	path := "/channels/" + channelID + "/messages/" + messageID + "/reactions/" + url.PathEscape(emoji) + "/@me"
	return w.restJSON(http.MethodPut, path, nil, nil)
}

func (w *wire) createThread(channelID, messageID string, start threadStart) (*textChannel, error) {
	var thread textChannel

	path := "/channels/" + channelID + "/messages/" + messageID + "/threads"
	if err := w.restJSON(http.MethodPost, path, start, &thread); err != nil {
		return nil, err
	}

	return &thread, nil
}

func (w *wire) sendAttachments(channelID string, attachments []events.OutboundAttachment) error {
	for i := range attachments {
		if err := w.sendAttachment(channelID, attachments[i]); err != nil {
			return err
		}
	}

	return nil
}

func (w *wire) sendAttachment(channelID string, attachment events.OutboundAttachment) error {
	var body bytes.Buffer

	writer := multipart.NewWriter(&body)

	payload, err := writer.CreateFormField("payload_json")
	if err != nil {
		return fmt.Errorf("create Discord attachment payload field: %w", err)
	}

	if _, err := payload.Write([]byte(`{"content":""}`)); err != nil {
		return fmt.Errorf("write Discord attachment payload field: %w", err)
	}

	name := strings.TrimSpace(attachment.Name)
	if name == "" {
		name = "attachment"
	}

	part, err := writer.CreateFormFile("files[0]", name)
	if err != nil {
		return fmt.Errorf("create Discord attachment file field: %w", err)
	}

	if _, err := part.Write(attachment.Data); err != nil {
		return fmt.Errorf("write Discord attachment file field: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish Discord attachment request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, discordAPI+"/channels/"+channelID+"/messages", &body)
	if err != nil {
		return fmt.Errorf("create Discord attachment request: %w", err)
	}

	req.Header.Set("Authorization", w.token)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("call Discord attachment API: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("discord API POST attachment failed: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}

	return nil
}

func (w *wire) restJSON(method, path string, body, out any) error {
	var reader io.Reader

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode Discord text API request: %w", err)
		}

		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, discordAPI+path, reader)
	if err != nil {
		return fmt.Errorf("create Discord text API request: %w", err)
	}

	req.Header.Set("Authorization", w.token)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("call Discord text API: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("discord API %s %s failed: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}

	if out == nil {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode Discord text API response: %w", err)
	}

	return nil
}

func (w *wire) gatewayReadLoop(conn *websocket.Conn) {
	// gatewayEvent is the Discord Gateway payload envelope.
	// https://docs.discord.com/developers/topics/gateway-events#payload-structure
	type gatewayEvent struct {
		Op gatewayOpcode   `json:"op"`
		T  string          `json:"t"`
		D  json.RawMessage `json:"d"`
	}

	for {
		var event gatewayEvent
		if err := conn.ReadJSON(&event); err != nil {
			return
		}

		w.handleGatewayEvent(conn, event.Op, event.T, event.D)
	}
}

func (w *wire) handleGatewayEvent(conn *websocket.Conn, op gatewayOpcode, eventType string, data json.RawMessage) {
	switch op {
	case gatewayOpHello:
		// hello is the Discord Gateway Hello payload that sets heartbeat cadence.
		// https://docs.discord.com/developers/events/gateway-events#hello
		var hello struct {
			HeartbeatInterval time.Duration `json:"heartbeat_interval"`
		}
		if json.Unmarshal(data, &hello) == nil {
			go w.gatewayHeartbeat(conn, hello.HeartbeatInterval*time.Millisecond)
		}

		w.sendIdentify()
	case gatewayOpDispatch:
		w.handleDispatch(eventType, data)
	case gatewayOpHeartbeat:
		if err := w.writeGatewayJSON(gatewayHeartbeatPayload{Op: gatewayOpHeartbeat, D: nil}); err != nil {
			w.log.Error("heartbeat Discord text gateway", "error", err)
		}
	case gatewayOpIdentify:
	}
}

func (w *wire) sendIdentify() {
	// identifyData is the Discord Gateway Identify payload subset used to subscribe to text events.
	// https://docs.discord.com/developers/events/gateway-events#identify-identify-structure
	type identifyData struct {
		Token      string            `json:"token"`
		Intents    int               `json:"intents"`
		Properties map[string]string `json:"properties"`
	}

	// identifyPayload wraps Identify data in the Discord Gateway payload envelope.
	type identifyPayload struct {
		Op gatewayOpcode `json:"op"`
		D  identifyData  `json:"d"`
	}

	const (
		// Guilds is needed for guild channel context.
		intentGuilds = 1 << 0
		// Guild Messages delivers MESSAGE_CREATE in configured guild text channels and threads.
		intentGuildMessages = 1 << 9
		// Guild Message Reactions delivers MESSAGE_REACTION_ADD for summary emoji handling.
		intentGuildMessageReactions = 1 << 10
		// Direct Messages delivers MESSAGE_CREATE in DMs with the configured human.
		intentDirectMessages = 1 << 12
		// Message Content exposes message text for human prompts.
		intentMessageContent = 1 << 15
	)

	data := identifyData{Token: strings.TrimPrefix(w.token, "Bot "), Intents: intentGuilds | intentGuildMessages | intentGuildMessageReactions | intentDirectMessages | intentMessageContent, Properties: map[string]string{"os": "darwin", "browser": "rocketclaw", "device": "rocketclaw"}}
	if err := w.writeGatewayJSON(identifyPayload{Op: gatewayOpIdentify, D: data}); err != nil {
		w.log.Error("identify Discord text gateway", "error", err)
	}
}

func (w *wire) gatewayHeartbeat(conn *websocket.Conn, interval time.Duration) {
	if interval <= 0 {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		w.mu.Lock()
		closed := w.closed || w.conn != conn
		w.mu.Unlock()

		if closed {
			return
		}

		if err := w.writeGatewayJSON(gatewayHeartbeatPayload{Op: gatewayOpHeartbeat, D: nil}); err != nil {
			w.log.Error("heartbeat Discord text gateway", "error", err)
		}
	}
}

func (w *wire) handleDispatch(eventType string, data json.RawMessage) {
	switch eventType {
	case "READY":
		// ready is the Discord Gateway Ready payload subset used to learn the current bot user.
		// https://docs.discord.com/developers/events/gateway-events#ready
		var ready struct {
			User *textUser `json:"user"`
		}
		if json.Unmarshal(data, &ready) == nil && ready.User != nil {
			w.mu.Lock()
			w.user = ready.User
			w.mu.Unlock()

			select {
			case <-w.ready:
			default:
				close(w.ready)
			}
		}
	case "MESSAGE_CREATE":
		var message textMessage
		if json.Unmarshal(data, &message) == nil {
			w.events <- textEvent{message: &messageCreate{Message: &message}}
		}
	case "MESSAGE_REACTION_ADD":
		var reaction reactionAdd
		if json.Unmarshal(data, &reaction) == nil {
			w.events <- textEvent{reaction: &reaction}
		}
	}
}

func (w *wire) writeGatewayJSON(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.conn == nil {
		return errors.New("discord text gateway is not connected")
	}

	if err := w.conn.WriteJSON(v); err != nil {
		return fmt.Errorf("write Discord text gateway JSON: %w", err)
	}

	return nil
}
