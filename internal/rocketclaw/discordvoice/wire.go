package discordvoice

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/hpke"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/discordvoice/internal/aesgcm8"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/chacha20poly1305"
)

// identify holds the Gateway Identify properties we send when opening the main Discord Gateway.
// https://docs.discord.com/developers/events/gateway-events#identify-identify-structure
type identify struct {
	Intents int
}

// user is the subset of Discord's User object needed for the READY event's current user.
// https://docs.discord.com/developers/resources/user#user-object
type user struct {
	ID string `json:"id"`
}

// voiceStateUpdate is the subset of Discord's Voice State object used to track user voice sessions.
// https://docs.discord.com/developers/resources/voice#voice-state-object
type voiceStateUpdate struct {
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
}

// channel is the subset of Discord's Channel object needed to resolve a configured guild voice channel.
// https://docs.discord.com/developers/resources/channel#channel-object
type channel struct {
	ID      string `json:"id"`
	GuildID string `json:"guild_id"`
}

// voiceServerUpdate carries the gateway VOICE_SERVER_UPDATE event used to open a voice WebSocket.
// https://docs.discord.com/developers/events/gateway-events#voice-server-update
type voiceServerUpdate struct {
	GuildID  string  `json:"guild_id"`
	Token    string  `json:"token"`
	Endpoint *string `json:"endpoint"`
}

type voiceConnectionStatus int

// voiceConnectionStatus tracks the local lifecycle around Discord's voice connection flow.
// https://docs.discord.com/developers/topics/voice-connections#connecting-to-voice
const (
	// New means the local voice connection exists but has not completed Discord's voice handshake.
	voiceConnectionStatusNew voiceConnectionStatus = 0
	// Connecting means Discord voice session/server details are being combined to open the voice WebSocket.
	voiceConnectionStatusConnecting voiceConnectionStatus = 1
	// Ready means Discord voice Ready and Session Description have completed and media can flow.
	voiceConnectionStatusReady voiceConnectionStatus = 2
	// Dead means the local voice connection has been closed or failed.
	voiceConnectionStatusDead voiceConnectionStatus = 3
)

// opusPacket carries the RTP header fields and Opus payload received from Discord voice UDP.
// https://docs.discord.com/developers/topics/voice-connections#transport-encryption-and-sending-voice
// https://www.rfc-editor.org/rfc/rfc3550.html#section-5.1
type opusPacket struct {
	Flags       byte
	PayloadType byte
	Sequence    uint16
	Timestamp   uint32
	SSRC        uint32
	SpeakerID   string
	Opus        []byte
}

type voiceEvent struct {
	packet opusPacket
	err    error
}

type wireConfig struct {
	token          string
	voiceChannelID string
	humanUserID    string
	log            *slog.Logger
}

// voiceSpeakingUpdate is Discord voice opcode 5's Speaking payload for SSRC to user mapping.
// https://docs.discord.com/developers/topics/voice-connections#speaking
type voiceSpeakingUpdate struct {
	UserID   string `json:"user_id"`
	SSRC     int    `json:"ssrc"`
	Speaking int    `json:"speaking"`
}

type voiceOpcode int

// Voice opcodes are named by Discord at:
// https://docs.discord.com/developers/topics/opcodes-and-status-codes#voice-voice-opcodes
const (
	// Identify starts a voice WebSocket session with server, channel, user, session, token, and DAVE support.
	voiceOpIdentify voiceOpcode = 0
	// Select Protocol tells Discord the discovered UDP address, port, encryption mode, and codec.
	voiceOpSelectProtocol voiceOpcode = 1
	// Ready completes the voice WebSocket handshake and provides UDP connection details.
	voiceOpReady voiceOpcode = 2
	// Heartbeat keeps the voice WebSocket session alive and acknowledges the latest sequence.
	voiceOpHeartbeat voiceOpcode = 3
	// Session Description provides the selected encryption mode, secret key, and DAVE version.
	voiceOpSessionDescription voiceOpcode = 4
	// Speaking updates a user's speaking state and SSRC mapping.
	voiceOpSpeaking voiceOpcode = 5
	// Hello provides the voice heartbeat interval.
	voiceOpHello voiceOpcode = 8
	// DAVE Prepare Epoch announces an upcoming DAVE protocol or MLS group transition.
	voiceOpDavePrepareEpoch voiceOpcode = 24
)

const (
	discordAPI        = "https://discord.com/api/v10"
	discordGatewayURL = "wss://gateway.discord.gg/?v=10&encoding=json"
)

type gatewayOpcode int

// Gateway opcodes are named by Discord at:
// https://docs.discord.com/developers/topics/opcodes-and-status-codes#gateway-gateway-opcodes
const (
	// Dispatch delivers gateway events such as READY, VOICE_STATE_UPDATE, and VOICE_SERVER_UPDATE.
	gatewayOpDispatch gatewayOpcode = 0
	// Heartbeat keeps the main gateway session alive.
	gatewayOpHeartbeat gatewayOpcode = 1
	// Identify starts the main gateway session and declares requested intents.
	gatewayOpIdentify gatewayOpcode = 2
	// Voice State Update asks Discord to join, move, or leave a guild voice channel.
	gatewayOpVoiceStateUpdate gatewayOpcode = 4
	// Hello provides the main gateway heartbeat interval.
	gatewayOpHello gatewayOpcode = 10
)

type wire struct {
	identify identify
	dialer   *websocket.Dialer
	client   *http.Client
	log      *slog.Logger

	token          string
	voiceChannelID string
	humanUserID    string
	events         chan voiceEvent
	voiceConn      *voiceConnection

	mu                 sync.Mutex
	user               *user
	voiceStates        map[string][]*voiceStateUpdate
	wsConn             *websocket.Conn
	closed             bool
	voiceSessionID     map[string]string
	voiceServerUpdates map[string]*voiceServerUpdate
	voiceConnections   map[string]*voiceConnection
	ready              chan struct{}
}

func newWire(ctx context.Context, cfg wireConfig) (*wire, error) {
	const (
		intentGuilds           = 1 << 0
		intentGuildVoiceStates = 1 << 7
	)

	wire := &wire{
		identify:           identify{Intents: intentGuilds | intentGuildVoiceStates},
		dialer:             websocket.DefaultDialer,
		client:             &http.Client{Timeout: 15 * time.Second},
		log:                cfg.log,
		token:              cfg.token,
		voiceChannelID:     cfg.voiceChannelID,
		humanUserID:        cfg.humanUserID,
		events:             make(chan voiceEvent, 16),
		voiceStates:        make(map[string][]*voiceStateUpdate),
		voiceSessionID:     make(map[string]string),
		voiceServerUpdates: make(map[string]*voiceServerUpdate),
		voiceConnections:   make(map[string]*voiceConnection),
		ready:              make(chan struct{}),
	}

	if err := wire.openGateway(); err != nil {
		return nil, fmt.Errorf("open Discord session: %w", err)
	}

	voiceChannel, err := wire.channel(cfg.voiceChannelID)
	if err != nil {
		_ = wire.close()
		return nil, fmt.Errorf("resolve Discord voice channel: %w", err)
	}

	if voiceChannel == nil || voiceChannel.GuildID == "" {
		_ = wire.close()
		return nil, errors.New("configured Discord channel is not a guild voice channel")
	}

	voiceConn, err := wire.joinVoice(ctx, voiceChannel.GuildID, voiceChannel.ID, false, false)
	if err != nil {
		_ = wire.close()
		return nil, fmt.Errorf("join Discord voice channel: %w", err)
	}

	wire.mu.Lock()
	wire.voiceConn = voiceConn
	wire.mu.Unlock()

	go wire.watchVoiceConnection(voiceConn)
	go wire.forwardPackets(ctx, voiceConn, voiceChannel.GuildID)
	go wire.sendWakeupFrames(ctx, voiceConn)

	return wire, nil
}

func (s *wire) watchVoiceConnection(vc *voiceConnection) {
	<-vc.dead

	if vc.err == nil {
		return
	}

	select {
	case s.events <- voiceEvent{err: vc.err}:
	default:
	}
}

func (s *wire) forwardPackets(ctx context.Context, vc *voiceConnection, guildID string) {
	for {
		select {
		case <-ctx.Done():
			return
		case packet, ok := <-vc.opusRecv:
			if !ok {
				return
			}

			if packet == nil || len(packet.Opus) == 0 {
				continue
			}

			if len(packet.Opus) == 3 && packet.Opus[0] == daveOpusSilence0 && packet.Opus[1] == daveOpusSilence1 && packet.Opus[2] == daveOpusSilence2 {
				continue
			}

			userID := vc.speakerID(packet.SSRC)
			if userID == "" {
				userID = s.assumeConfiguredHumanUserID(guildID, packet.SSRC)
			}

			if userID != s.humanUserID {
				continue
			}

			event := voiceEvent{packet: opusPacket{
				Flags:       packet.Flags,
				PayloadType: packet.PayloadType,
				Sequence:    packet.Sequence,
				Timestamp:   packet.Timestamp,
				SSRC:        packet.SSRC,
				SpeakerID:   userID,
				Opus:        slices.Clone(packet.Opus),
			}}
			select {
			case s.events <- event:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (s *wire) assumeConfiguredHumanUserID(guildID string, ssrc uint32) string {
	if s.humanUserID == "" {
		return ""
	}

	s.mu.Lock()
	states := slices.Clone(s.voiceStates[guildID])
	user := s.user
	s.mu.Unlock()

	botUserID := ""
	if user != nil {
		botUserID = user.ID
	}

	inVoiceChannel := func(state *voiceStateUpdate) bool {
		if state == nil || state.ChannelID != s.voiceChannelID {
			return false
		}

		return state.UserID != botUserID
	}

	humanPresent := slices.ContainsFunc(states, func(state *voiceStateUpdate) bool {
		return inVoiceChannel(state) && state.UserID == s.humanUserID
	})
	othersPresent := slices.ContainsFunc(states, func(state *voiceStateUpdate) bool {
		return inVoiceChannel(state) && state.UserID != s.humanUserID
	})

	if !humanPresent || othersPresent {
		return ""
	}

	s.mu.Lock()
	vc := s.voiceConn
	s.mu.Unlock()

	if vc != nil {
		vc.setSpeakerID(ssrc, s.humanUserID)
	}

	return s.humanUserID
}

func (s *wire) play(ctx context.Context, frames opusFrames) (int, error) {
	s.mu.Lock()
	vc := s.voiceConn
	s.mu.Unlock()

	if err := vc.validate(); err != nil {
		return 0, err
	}

	if err := vc.setSpeaking(true); err != nil {
		s.log.Error("start Discord speaking", "error", err)
	}

	defer func() {
		if err := vc.setSpeaking(false); err != nil {
			s.log.Error("stop Discord speaking", "error", err)
		}
	}()

	sent := 0

	err := frames(func(frame []byte) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-vc.dead:
			return errors.New("discord voice connection unavailable")
		case vc.opusSend <- frame:
			sent++
			return nil
		}
	})
	if err != nil {
		return sent, fmt.Errorf("decode Ogg Opus stream: %w", err)
	}

	return sent, nil
}

func (s *wire) sendWakeupFrames(ctx context.Context, vc *voiceConnection) {
	if err := vc.setSpeaking(true); err != nil {
		s.log.Error("start Discord wakeup speaking", "error", err)
	}

	defer func() {
		if err := vc.setSpeaking(false); err != nil {
			s.log.Error("stop Discord wakeup speaking", "error", err)
		}
	}()

	silenceFrame := []byte{daveOpusSilence0, daveOpusSilence1, daveOpusSilence2}

	for range 5 {
		select {
		case <-ctx.Done():
			return
		case vc.opusSend <- silenceFrame:
		}

		time.Sleep(20 * time.Millisecond)
	}
}

func (s *wire) openGateway() error {
	conn, resp, err := s.dialer.Dial(discordGatewayURL, nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	if err != nil {
		return fmt.Errorf("dial Discord gateway: %w", err)
	}

	s.mu.Lock()
	s.wsConn = conn
	s.closed = false
	s.mu.Unlock()

	go s.gatewayReadLoop(conn)

	select {
	case <-s.ready:
		return nil
	case <-time.After(10 * time.Second):
		_ = conn.Close()
		return errors.New("discord gateway READY timeout")
	}
}

func (s *wire) close() error {
	s.mu.Lock()
	s.closed = true
	conn := s.wsConn
	s.wsConn = nil

	connections := make([]*voiceConnection, 0, len(s.voiceConnections))
	for _, vc := range s.voiceConnections {
		connections = append(connections, vc)
	}
	s.mu.Unlock()

	for _, vc := range connections {
		vc.die(nil)
	}

	if conn != nil {
		if err := conn.Close(); err != nil {
			return fmt.Errorf("close Discord gateway websocket: %w", err)
		}
	}

	return nil
}

func (s *wire) channel(channelID string) (*channel, error) {
	var channel channel
	if err := s.restJSON(http.MethodGet, "/channels/"+channelID, nil, &channel); err != nil {
		return nil, err
	}

	return &channel, nil
}

func (s *wire) joinVoice(ctx context.Context, guildID, channelID string, mute, deaf bool) (*voiceConnection, error) {
	if err := s.sendVoiceStateUpdate(guildID, channelID, mute, deaf); err != nil {
		return nil, err
	}

	sessionID, update, err := s.waitVoiceJoin(ctx, guildID)
	if err != nil {
		return nil, err
	}

	vc := newVoiceConnection(s, guildID, channelID, mute, deaf)

	s.mu.Lock()
	user := s.user
	s.mu.Unlock()

	if user == nil || user.ID == "" {
		return nil, errors.New("discord current user missing")
	}

	if err := vc.connect(ctx, user.ID, sessionID, update); err != nil {
		vc.die(nil)
		return nil, err
	}

	s.mu.Lock()
	s.voiceConnections[guildID] = vc
	s.mu.Unlock()

	return vc, nil
}

func (s *wire) sendVoiceStateUpdate(guildID, channelID string, mute, deaf bool) error {
	type voiceStateUpdateData struct {
		GuildID   string  `json:"guild_id"`
		ChannelID *string `json:"channel_id"`
		SelfMute  bool    `json:"self_mute"`
		SelfDeaf  bool    `json:"self_deaf"`
	}

	type voiceStateUpdatePayload struct {
		Op gatewayOpcode        `json:"op"`
		D  voiceStateUpdateData `json:"d"`
	}

	var channel *string
	if channelID != "" {
		channel = &channelID
	}

	// Discord documents this as Opcode 4 Gateway Voice State Update and says:
	// "To inform the gateway of our intent to establish voice connectivity" send
	// `guild_id`, `channel_id`, `self_mute`, and `self_deaf`.
	// https://docs.discord.com/developers/topics/voice-connections#retrieving-voice-server-information
	return s.writeGatewayJSON(voiceStateUpdatePayload{Op: gatewayOpVoiceStateUpdate, D: voiceStateUpdateData{GuildID: guildID, ChannelID: channel, SelfMute: mute, SelfDeaf: deaf}})
}

func (s *wire) restJSON(method, path string, body, out any) error {
	var r io.Reader

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode Discord API request: %w", err)
		}

		r = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, discordAPI+path, r)
	if err != nil {
		return fmt.Errorf("create Discord API request: %w", err)
	}

	req.Header.Set("Authorization", s.token)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("call Discord API: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("discord API %s %s failed: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}

	if out == nil {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode Discord API response: %w", err)
	}

	return nil
}

func (s *wire) gatewayReadLoop(conn *websocket.Conn) {
	type gatewayEvent struct {
		Op gatewayOpcode   `json:"op"`
		T  string          `json:"t"`
		D  json.RawMessage `json:"d"`
	}

	// Discord gateway payloads carry `op` for the opcode, `t` for the event name
	// on Dispatch payloads, and `d` for event data.
	// https://docs.discord.com/developers/events/gateway-events#payload-structure
	for {
		var event gatewayEvent
		if err := conn.ReadJSON(&event); err != nil {
			return
		}

		s.handleGatewayEvent(conn, event.Op, event.T, event.D)
	}
}

func (s *wire) handleGatewayEvent(conn *websocket.Conn, op gatewayOpcode, eventType string, data json.RawMessage) {
	switch op {
	case gatewayOpHello:
		var hello struct {
			HeartbeatInterval time.Duration `json:"heartbeat_interval"`
		}
		if json.Unmarshal(data, &hello) == nil {
			go s.gatewayHeartbeat(conn, hello.HeartbeatInterval*time.Millisecond)
		}

		s.sendIdentify()
	case gatewayOpDispatch:
		s.handleDispatch(eventType, data)
	case gatewayOpHeartbeat, gatewayOpIdentify, gatewayOpVoiceStateUpdate:
		return
	}
}

func (s *wire) sendIdentify() {
	type identifyData struct {
		Token      string            `json:"token"`
		Intents    int               `json:"intents"`
		Properties map[string]string `json:"properties"`
	}

	type identifyPayload struct {
		Op gatewayOpcode `json:"op"`
		D  identifyData  `json:"d"`
	}

	// Discord Gateway Opcode 2 Identify starts a new gateway session and carries
	// the bot token, requested intents, and client properties.
	// https://docs.discord.com/developers/events/gateway#identifying
	if err := s.writeGatewayJSON(identifyPayload{Op: gatewayOpIdentify, D: identifyData{Token: strings.TrimPrefix(s.token, "Bot "), Intents: s.identify.Intents, Properties: map[string]string{"os": "darwin", "browser": "rocketclaw", "device": "rocketclaw"}}}); err != nil {
		s.log.Error("identify Discord gateway", "error", err)
	}
}

func (s *wire) gatewayHeartbeat(conn *websocket.Conn, interval time.Duration) {
	type heartbeatPayload struct {
		Op gatewayOpcode `json:"op"`
		D  any           `json:"d"`
	}

	if interval <= 0 {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		closed := s.closed || s.wsConn != conn
		s.mu.Unlock()

		if closed {
			return
		}

		// Discord Gateway Opcode 1 Heartbeat is sent at the Hello interval; this
		// implementation does not track gateway sequence numbers, so the body is nil.
		// https://docs.discord.com/developers/events/gateway#sending-heartbeats
		if err := s.writeGatewayJSON(heartbeatPayload{Op: gatewayOpHeartbeat, D: nil}); err != nil {
			s.log.Error("heartbeat Discord gateway", "error", err)
		}
	}
}

func (s *wire) handleDispatch(eventType string, data json.RawMessage) {
	switch eventType {
	case "READY":
		var ready struct {
			User *user `json:"user"`
		}
		if json.Unmarshal(data, &ready) == nil && ready.User != nil {
			s.mu.Lock()
			s.user = ready.User
			s.mu.Unlock()

			select {
			case <-s.ready:
			default:
				close(s.ready)
			}
		}
	case "VOICE_STATE_UPDATE":
		var update voiceStateUpdate
		if json.Unmarshal(data, &update) != nil {
			return
		}

		s.mu.Lock()

		states := s.voiceStates[update.GuildID]
		if i := slices.IndexFunc(states, func(state *voiceStateUpdate) bool { return state.UserID == update.UserID }); i >= 0 {
			states[i] = &update
			s.voiceStates[update.GuildID] = states
		} else {
			s.voiceStates[update.GuildID] = append(states, &update)
		}

		user := s.user
		s.mu.Unlock()

		if user != nil && update.UserID == user.ID {
			s.mu.Lock()
			s.voiceSessionID[update.GuildID] = update.SessionID
			s.mu.Unlock()
		}
	case "VOICE_SERVER_UPDATE":
		var update voiceServerUpdate
		if json.Unmarshal(data, &update) != nil {
			return
		}

		s.mu.Lock()
		s.voiceServerUpdates[update.GuildID] = &update
		s.mu.Unlock()
	}
}

func (s *wire) waitVoiceJoin(ctx context.Context, guildID string) (string, *voiceServerUpdate, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		s.mu.Lock()
		sessionID := s.voiceSessionID[guildID]
		update := s.voiceServerUpdates[guildID]
		s.mu.Unlock()

		if sessionID != "" && update != nil {
			return sessionID, update, nil
		}

		select {
		case <-ctx.Done():
			return "", nil, fmt.Errorf("wait Discord voice join: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (s *wire) writeGatewayJSON(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	conn := s.wsConn
	if conn == nil {
		return errors.New("discord gateway is not connected")
	}

	if err := conn.WriteJSON(v); err != nil {
		return fmt.Errorf("write Discord gateway JSON: %w", err)
	}

	return nil
}

const (
	// RTP fixed headers are 12 bytes before CSRC or extension data.
	// https://www.rfc-editor.org/rfc/rfc3550.html#section-5.1
	rtpHeaderSize = 12
	// Discord voice Opus packets use RTP payload type 120 (0x78).
	// https://docs.discord.com/developers/topics/voice-connections#transport-encryption-and-sending-voice
	rtpPayloadTypeOpus = 0x78
	// Discord RTP-size AEAD modes append a 32-bit nonce suffix to each encrypted RTP payload.
	// https://docs.discord.com/developers/topics/voice-connections#transport-encryption-modes
	rtpNonceSuffixSize = 4
	// Discord voice sends Opus frames at the 20 ms RTP audio cadence.
	// https://docs.discord.com/developers/topics/voice-connections#voice-data-interpolation
	rtpPacketInterval = 20 * time.Millisecond
	// Opus audio is encoded at 48 kHz, so one 20 ms frame advances the RTP timestamp by 960 samples.
	// https://docs.discord.com/developers/topics/voice-connections#transport-encryption-and-sending-voice
	rtpTimestampStep = 960
	// Discord UDP keepalives are sent every 5 seconds using little-endian nonces.
	// https://docs.discord.com/developers/topics/voice-connections#heartbeating
	udpKeepAliveInterval = 5 * time.Second
	// udpPacketBufferSize is an Ethernet MTU-sized scratch buffer for Discord voice UDP reads.
	udpPacketBufferSize = 1500
)

type rtpEncryptionMode string

// Discord voice transport encryption modes are documented at:
// https://docs.discord.com/developers/topics/voice-connections#transport-encryption-modes
const (
	// AES-256-GCM RTP-size is Discord's preferred available mode when supported.
	rtpEncryptionModeAES256GCMRTPSize rtpEncryptionMode = "aead_aes256_gcm_rtpsize"
	// XChaCha20-Poly1305 RTP-size is the required available mode for voice gateway compatibility.
	rtpEncryptionModeXChaCha20Poly1305RTPSize rtpEncryptionMode = "aead_xchacha20_poly1305_rtpsize"
)

type voiceConnection struct {
	cond *sync.Cond

	status voiceConnectionStatus
	dead   chan struct{}
	err    error

	guildID   string
	channelID string

	opusSend chan []byte
	opusRecv chan *opusPacket

	session   *wire
	wsConn    *websocket.Conn
	udpConn   *net.UDPConn
	userID    string
	deaf      bool
	mute      bool
	speaking  bool
	ready     chan struct{}
	dave      *daveSession
	seqAck    uint16
	ssrc      uint32
	mode      rtpEncryptionMode
	secret    [32]byte
	sequence  uint16
	timestamp uint32
	nonce     uint32
	ssrcUsers map[uint32]string
}

func newVoiceConnection(s *wire, guildID, channelID string, mute, deaf bool) *voiceConnection {
	return &voiceConnection{
		cond:      sync.NewCond(&sync.Mutex{}),
		status:    voiceConnectionStatusNew,
		dead:      make(chan struct{}),
		guildID:   guildID,
		channelID: channelID,

		session: s,
		mute:    mute,
		deaf:    deaf,

		opusSend: make(chan []byte),
		opusRecv: make(chan *opusPacket),

		ready:     make(chan struct{}),
		ssrcUsers: map[uint32]string{},
	}
}

func (v *voiceConnection) validate() error {
	if v == nil {
		return errors.New("discord voice connection unavailable")
	}

	if v.err != nil {
		return fmt.Errorf("discord voice connection unavailable: %w", v.err)
	}

	if v.status == voiceConnectionStatusDead {
		return errors.New("discord voice connection unavailable")
	}

	if v.opusSend == nil {
		return errors.New("discord voice connection unavailable")
	}

	select {
	case <-v.dead:
		return errors.New("discord voice connection unavailable")
	default:
		return nil
	}
}

func (v *voiceConnection) connect(ctx context.Context, userID, sessionID string, update *voiceServerUpdate) error {
	// voiceIdentifyData is Discord Voice Opcode 0's Identify payload body.
	// https://docs.discord.com/developers/topics/voice-connections#establishing-a-voice-websocket-connection
	type voiceIdentifyData struct {
		ServerID               string `json:"server_id"`
		ChannelID              string `json:"channel_id"`
		UserID                 string `json:"user_id"`
		SessionID              string `json:"session_id"`
		Token                  string `json:"token"`
		MaxDaveProtocolVersion int    `json:"max_dave_protocol_version"`
	}

	// voiceIdentifyPayload wraps the Identify body with Discord voice opcode 0.
	// https://docs.discord.com/developers/topics/opcodes-and-status-codes#voice-voice-opcodes
	type voiceIdentifyPayload struct {
		Op voiceOpcode       `json:"op"`
		D  voiceIdentifyData `json:"d"`
	}

	if update == nil || update.Endpoint == nil || *update.Endpoint == "" {
		return errors.New("discord voice server endpoint missing")
	}

	endpoint := strings.TrimPrefix(*update.Endpoint, "wss://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimSuffix(endpoint, "/")

	conn, resp, err := v.session.dialer.Dial("wss://"+endpoint+"/?v=8", nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	if err != nil {
		return fmt.Errorf("dial Discord voice gateway: %w", err)
	}

	v.cond.L.Lock()
	v.wsConn = conn
	v.userID = userID
	v.status = voiceConnectionStatusConnecting
	v.cond.L.Unlock()

	// Discord Voice Opcode 0 Identify carries server, channel, user, session,
	// token, and `max_dave_protocol_version` for DAVE support negotiation.
	// https://docs.discord.com/developers/topics/voice-connections#establishing-a-voice-websocket-connection
	if err := conn.WriteJSON(voiceIdentifyPayload{Op: voiceOpIdentify, D: voiceIdentifyData{ServerID: v.guildID, ChannelID: v.channelID, UserID: userID, SessionID: sessionID, Token: update.Token, MaxDaveProtocolVersion: 1}}); err != nil {
		_ = conn.Close()
		return fmt.Errorf("identify Discord voice gateway: %w", err)
	}

	go v.voiceReadLoop(conn)

	select {
	case <-v.ready:
		return nil
	case <-v.dead:
		if v.err != nil {
			return v.err
		}

		return errors.New("discord voice gateway closed")
	case <-ctx.Done():
		_ = conn.Close()
		return fmt.Errorf("connect Discord voice gateway: %w", ctx.Err())
	case <-time.After(10 * time.Second):
		_ = conn.Close()
		return errors.New("discord voice gateway READY timeout")
	}
}

func (v *voiceConnection) voiceReadLoop(conn *websocket.Conn) {
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			v.die(fmt.Errorf("read Discord voice gateway: %w", err))
			return
		}

		if messageType != websocket.TextMessage {
			v.handleVoiceBinary(data)
			continue
		}

		var event struct {
			Op voiceOpcode     `json:"op"`
			D  json.RawMessage `json:"d"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}

		v.handleVoiceEvent(conn, event.Op, event.D)
	}
}

func (v *voiceConnection) handleVoiceEvent(conn *websocket.Conn, op voiceOpcode, data json.RawMessage) {
	switch op {
	case voiceOpHello:
		var hello struct {
			HeartbeatInterval time.Duration `json:"heartbeat_interval"`
		}
		if json.Unmarshal(data, &hello) == nil {
			go v.voiceHeartbeat(conn, hello.HeartbeatInterval*time.Millisecond)
		}
	case voiceOpReady:
		var ready struct {
			SSRC        uint32   `json:"ssrc"`
			IP          string   `json:"ip"`
			Port        int      `json:"port"`
			Modes       []string `json:"modes"`
			Experiments []string `json:"experiments"`
		}
		if json.Unmarshal(data, &ready) == nil {
			if err := v.selectUDPProtocol(conn, ready.SSRC, ready.IP, ready.Port, ready.Modes, ready.Experiments); err != nil {
				v.die(err)
			}
		}
	case voiceOpSessionDescription:
		var session struct {
			Mode                string `json:"mode"`
			SecretKey           []byte `json:"secret_key"`
			DaveProtocolVersion int    `json:"dave_protocol_version"`
		}
		if json.Unmarshal(data, &session) == nil {
			if session.DaveProtocolVersion > 0 {
				if err := v.startDAVE(conn, session.DaveProtocolVersion); err != nil {
					v.die(err)
					return
				}
			}

			v.cond.L.Lock()

			v.mode = rtpEncryptionMode(session.Mode)
			if len(session.SecretKey) == len(v.secret) {
				copy(v.secret[:], session.SecretKey)
			}

			v.status = voiceConnectionStatusReady
			go v.udpReceiveLoop()
			go v.udpSendLoop()
			go v.udpKeepAliveLoop()

			select {
			case <-v.ready:
			default:
				close(v.ready)
			}

			v.cond.Broadcast()
			v.cond.L.Unlock()
		}
	case voiceOpHeartbeat:
		if err := v.writeVoiceHeartbeat(conn); err != nil {
			v.session.log.Error("heartbeat Discord voice gateway", "error", err)
		}
	case voiceOpSpeaking:
		var speaking voiceSpeakingUpdate
		if json.Unmarshal(data, &speaking) == nil {
			v.cond.L.Lock()
			v.ssrcUsers[uint32(speaking.SSRC)] = speaking.UserID
			v.cond.L.Unlock()
		}
	case voiceOpDavePrepareEpoch:
		var epoch struct {
			ProtocolVersion int `json:"protocol_version"`
			Epoch           int `json:"epoch"`
		}
		if json.Unmarshal(data, &epoch) == nil && epoch.Epoch == 1 && epoch.ProtocolVersion > 0 {
			if err := v.startDAVE(conn, epoch.ProtocolVersion); err != nil {
				v.die(err)
				return
			}
		}
	case voiceOpIdentify, voiceOpSelectProtocol:
		return
	}
}

func (v *voiceConnection) startDAVE(conn *websocket.Conn, _ int) error {
	v.cond.L.Lock()
	userID := v.userID
	channelID := v.channelID

	var externalSender []byte
	if v.dave != nil {
		externalSender = append(externalSender, v.dave.externalSender...)
	}
	v.cond.L.Unlock()

	dave, err := newDaveSession(userID, channelID)
	if err != nil {
		return err
	}

	if len(externalSender) > 0 {
		dave.setExternalSender(externalSender)
	}

	keyPackage, err := dave.keyPackageMessage()
	if err != nil {
		return err
	}

	v.cond.L.Lock()
	v.dave = dave
	v.cond.L.Unlock()

	return v.writeVoiceBinary(conn, daveMlsKeyPackageOp, keyPackage)
}

func (v *voiceConnection) handleVoiceBinary(data []byte) {
	if len(data) < 3 {
		return
	}

	op := data[2]
	seqAck := uint16(data[0])<<8 | uint16(data[1])
	payload := data[3:]

	v.cond.L.Lock()
	v.seqAck = seqAck
	v.cond.L.Unlock()

	switch op {
	case daveMlsExternalSenderOp:
		v.handleDaveExternalSender(payload)
	case daveMlsProposalsOp:
		v.handleDaveProposals(payload)
	case daveMlsAnnounceCommitOp, daveMlsWelcomeOp:
		v.handleDaveTransition(op, payload)
	}
}

func (v *voiceConnection) handleDaveExternalSender(payload []byte) {
	v.cond.L.Lock()
	if v.dave != nil {
		v.dave.setExternalSender(payload)
	}
	v.cond.L.Unlock()
}

func (v *voiceConnection) handleDaveProposals(payload []byte) {
	v.cond.L.Lock()
	dave := v.dave
	conn := v.wsConn
	v.cond.L.Unlock()

	if dave == nil {
		return
	}

	commitWelcome, err := dave.processProposals(payload)
	if err != nil {
		return
	}

	if len(commitWelcome) > 0 {
		if err := v.writeVoiceBinary(conn, daveMlsCommitWelcomeOp, commitWelcome); err != nil {
			v.die(fmt.Errorf("send DAVE MLS commit welcome: %w", err))
		}
	}
}

func (v *voiceConnection) handleDaveTransition(op byte, payload []byte) {
	if len(payload) < 2 {
		return
	}

	if op != daveMlsWelcomeOp {
		return
	}

	v.cond.L.Lock()
	dave := v.dave
	v.cond.L.Unlock()

	if dave == nil {
		return
	}

	if err := dave.processWelcome(payload[2:]); err != nil {
		return
	}
}

func (v *voiceConnection) voiceHeartbeat(conn *websocket.Conn, interval time.Duration) {
	if interval <= 0 {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		v.cond.L.Lock()
		dead := v.status == voiceConnectionStatusDead || v.wsConn != conn
		v.cond.L.Unlock()

		if dead {
			return
		}

		if err := v.writeVoiceHeartbeat(conn); err != nil {
			v.die(fmt.Errorf("heartbeat Discord voice gateway: %w", err))
			return
		}
	}
}

func (v *voiceConnection) writeVoiceHeartbeat(conn *websocket.Conn) error {
	// voiceHeartbeatData is Discord Voice Opcode 3's heartbeat payload body.
	// https://docs.discord.com/developers/topics/voice-connections#heartbeating
	type voiceHeartbeatData struct {
		T      int64  `json:"t"`
		SeqAck uint16 `json:"seq_ack"`
	}

	// voiceHeartbeatPayload wraps the heartbeat body with Discord voice opcode 3.
	// https://docs.discord.com/developers/topics/opcodes-and-status-codes#voice-voice-opcodes
	type voiceHeartbeatPayload struct {
		Op voiceOpcode        `json:"op"`
		D  voiceHeartbeatData `json:"d"`
	}

	v.cond.L.Lock()
	seqAck := v.seqAck
	v.cond.L.Unlock()

	// Discord Voice Opcode 3 Heartbeat for voice gateway v8 includes the
	// timestamp nonce and `seq_ack`, the last received numbered voice message.
	// https://docs.discord.com/developers/topics/voice-connections#heartbeating
	return v.writeVoiceJSON(conn, voiceHeartbeatPayload{Op: voiceOpHeartbeat, D: voiceHeartbeatData{T: time.Now().UnixMilli(), SeqAck: seqAck}})
}

func (v *voiceConnection) selectUDPProtocol(conn *websocket.Conn, ssrc uint32, ip string, port int, modes, experiments []string) error {
	// selectProtocolData is Discord Voice Opcode 1's UDP address, port, and encryption mode object.
	// https://docs.discord.com/developers/topics/voice-connections#select-protocol
	type selectProtocolData struct {
		Address string `json:"address"`
		Port    uint16 `json:"port"`
		Mode    string `json:"mode"`
	}

	// selectProtocolCodec identifies the Opus audio codec in Discord Voice Opcode 1.
	// https://docs.discord.com/developers/topics/voice-connections#select-protocol
	type selectProtocolCodec struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		Priority    int    `json:"priority"`
		PayloadType int    `json:"payload_type"`
	}

	// selectProtocolBody is Discord Voice Opcode 1's Select Protocol payload body.
	// https://docs.discord.com/developers/topics/voice-connections#select-protocol
	type selectProtocolBody struct {
		Protocol    string                `json:"protocol"`
		Data        selectProtocolData    `json:"data"`
		Codecs      []selectProtocolCodec `json:"codecs"`
		Experiments []string              `json:"experiments,omitempty"`
	}

	// selectProtocolPayload wraps the Select Protocol body with Discord voice opcode 1.
	// https://docs.discord.com/developers/topics/opcodes-and-status-codes#voice-voice-opcodes
	type selectProtocolPayload struct {
		Op voiceOpcode        `json:"op"`
		D  selectProtocolBody `json:"d"`
	}

	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", ip, port))
	if err != nil {
		return fmt.Errorf("resolve Discord voice UDP address: %w", err)
	}

	udpConn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return fmt.Errorf("dial Discord voice UDP: %w", err)
	}

	// Discord UDP discovery sends a fixed 74-byte probe:
	// bytes 0-1 are request type 1, bytes 2-3 are packet length 70,
	// bytes 4-7 are our SSRC, and bytes 8-73 are zero padding.
	packet := make([]byte, 74)
	binary.BigEndian.PutUint16(packet[0:2], 1)
	binary.BigEndian.PutUint16(packet[2:4], 70)
	binary.BigEndian.PutUint32(packet[4:8], ssrc)

	if _, err := udpConn.Write(packet); err != nil {
		_ = udpConn.Close()
		return fmt.Errorf("write Discord voice UDP discovery: %w", err)
	}

	if err := udpConn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		_ = udpConn.Close()
		return fmt.Errorf("set Discord voice UDP discovery deadline: %w", err)
	}

	n, err := udpConn.Read(packet)
	if err != nil {
		_ = udpConn.Close()
		return fmt.Errorf("read Discord voice UDP discovery: %w", err)
	}

	if err := udpConn.SetReadDeadline(time.Time{}); err != nil {
		_ = udpConn.Close()
		return fmt.Errorf("clear Discord voice UDP discovery deadline: %w", err)
	}

	if n < len(packet) || binary.BigEndian.Uint16(packet[0:2]) != 2 {
		_ = udpConn.Close()
		return errors.New("invalid Discord voice UDP discovery response")
	}

	// The response reuses the packet buffer: bytes 8-71 contain the
	// discovered external IP as a NUL-terminated string, bytes 72-73 the port.
	addressBytes := packet[8:72]

	addressLen := len(addressBytes)
	if zero := slices.Index(addressBytes, 0); zero >= 0 {
		addressLen = zero
	}

	mode := rtpEncryptionModeXChaCha20Poly1305RTPSize
	if slices.Contains(modes, string(rtpEncryptionModeAES256GCMRTPSize)) {
		mode = rtpEncryptionModeAES256GCMRTPSize
	}

	v.cond.L.Lock()
	v.udpConn = udpConn
	v.ssrc = ssrc
	v.timestamp = uint32(time.Now().UnixNano())
	v.cond.L.Unlock()

	// Discord Voice Opcode 1 Select Protocol sends the discovered UDP address,
	// UDP port, chosen encryption mode, and Opus codec before media starts.
	// https://docs.discord.com/developers/topics/voice-connections#establishing-a-voice-udp-connection
	return v.writeVoiceJSON(conn, selectProtocolPayload{Op: voiceOpSelectProtocol, D: selectProtocolBody{Protocol: "udp", Data: selectProtocolData{Address: string(addressBytes[:addressLen]), Port: binary.BigEndian.Uint16(packet[72:74]), Mode: string(mode)}, Codecs: []selectProtocolCodec{{Name: "opus", Type: "audio", Priority: 1000, PayloadType: 120}}, Experiments: experiments}})
}

func (v *voiceConnection) udpReceiveLoop() {
	buffer := make([]byte, udpPacketBufferSize)

	for {
		v.cond.L.Lock()
		udpConn := v.udpConn
		v.cond.L.Unlock()

		if udpConn == nil {
			return
		}

		n, err := udpConn.Read(buffer)
		if err != nil {
			if errNet, ok := err.(net.Error); ok && errNet.Timeout() {
				_ = udpConn.SetReadDeadline(time.Time{})
				continue
			}

			v.die(fmt.Errorf("read Discord voice UDP: %w", err))

			return
		}

		isKeepalive := n == 8
		isRTPVersion2 := buffer[0]>>6 == 2
		isOpusPayload := buffer[1]&0x7f == rtpPayloadTypeOpus

		if isKeepalive || !isRTPVersion2 || !isOpusPayload {
			continue
		}

		packet, err := v.decryptRTPPacket(buffer[:n])
		if err != nil {
			continue
		}

		select {
		case v.opusRecv <- packet:
		case <-v.dead:
			return
		}
	}
}

func (v *voiceConnection) udpSendLoop() {
	for {
		select {
		case opus := <-v.opusSend:
			packet, err := v.encryptRTPPacket(opus)
			if err != nil {
				v.die(err)
				return
			}

			v.cond.L.Lock()
			udpConn := v.udpConn
			v.cond.L.Unlock()

			if udpConn == nil {
				return
			}

			if _, err := udpConn.Write(packet); err != nil {
				v.die(fmt.Errorf("write Discord voice UDP: %w", err))
				return
			}

			select {
			case <-time.After(rtpPacketInterval):
			case <-v.dead:
				return
			}
		case <-v.dead:
			return
		}
	}
}

func (v *voiceConnection) udpKeepAliveLoop() {
	ticker := time.NewTicker(udpKeepAliveInterval)
	defer ticker.Stop()

	var counter uint32

	for {
		select {
		case <-ticker.C:
			v.cond.L.Lock()
			udpConn := v.udpConn
			v.cond.L.Unlock()

			if udpConn == nil {
				return
			}

			packet := make([]byte, 8)
			binary.LittleEndian.PutUint32(packet, counter)
			counter++

			if _, err := udpConn.Write(packet); err != nil {
				v.die(fmt.Errorf("write Discord voice UDP keepalive: %w", err))
				return
			}
		case <-v.dead:
			return
		}
	}
}

func (v *voiceConnection) encryptRTPPacket(opus []byte) ([]byte, error) {
	if len(opus) == 0 {
		return nil, errors.New("discord voice Opus packet is empty")
	}

	v.cond.L.Lock()
	mode := v.mode
	secret := v.secret
	dave := v.dave
	userID := v.userID
	ssrc := v.ssrc
	sequence := v.sequence
	timestamp := v.timestamp
	nonceCounter := v.nonce
	v.sequence++
	v.timestamp += rtpTimestampStep
	v.nonce++
	v.cond.L.Unlock()

	header := make([]byte, rtpHeaderSize)
	// Discord voice RTP packets use the RFC 3550 fixed header form documented in
	// https://docs.discord.com/developers/topics/voice-connections#transport-encryption-modes-voice-packet-structure.
	// Version 2, no padding, no extension, no CSRCs.
	header[0] = 0x80
	// Discord assigns payload type 0x78 to Opus audio.
	header[1] = rtpPayloadTypeOpus
	// RTP sequence number increments once per Opus packet.
	binary.BigEndian.PutUint16(header[2:4], sequence)
	// RTP timestamp advances by 960 samples for each 20 ms frame at 48 kHz.
	binary.BigEndian.PutUint32(header[4:8], timestamp)
	// SSRC is the Discord voice server-assigned synchronization source.
	binary.BigEndian.PutUint32(header[8:12], ssrc)

	if dave != nil {
		var err error

		opus, err = dave.encryptOpus(userID, opus)
		if err != nil {
			return nil, err
		}
	}

	sealed, err := sealRTPPayload(mode, secret[:], header, opus, nonceCounter+1)
	if err != nil {
		return nil, err
	}

	return slices.Concat(header, sealed), nil
}

func (v *voiceConnection) decryptRTPPacket(packet []byte) (*opusPacket, error) {
	headerLen, err := rtpAEADHeaderLen(packet)
	if err != nil {
		return nil, err
	}

	if len(packet) < headerLen+rtpNonceSuffixSize {
		return nil, errors.New("discord voice RTP packet is too short")
	}

	v.cond.L.Lock()
	mode := v.mode
	secret := v.secret
	dave := v.dave
	userID := v.ssrcUsers[binary.BigEndian.Uint32(packet[8:12])]
	v.cond.L.Unlock()

	opus, err := openRTPPayload(mode, secret[:], packet[:headerLen], packet[headerLen:])
	if err != nil {
		return nil, err
	}

	opus = stripRTPPayloadExtension(packet, opus)
	if dave != nil && userID != "" {
		opus, err = dave.decryptOpus(userID, opus)
		if err != nil {
			return nil, err
		}
	}

	return &opusPacket{Flags: packet[0], PayloadType: packet[1], Sequence: binary.BigEndian.Uint16(packet[2:4]), Timestamp: binary.BigEndian.Uint32(packet[4:8]), SSRC: binary.BigEndian.Uint32(packet[8:12]), Opus: opus}, nil
}

func rtpAEADHeaderLen(packet []byte) (int, error) {
	if len(packet) < rtpHeaderSize {
		return 0, errors.New("discord voice RTP packet is too short")
	}

	headerLen := rtpHeaderSize
	if packet[0]&0x10 != 0 {
		headerLen += 4
	}

	if len(packet) < headerLen {
		return 0, errors.New("discord voice RTP extension header is truncated")
	}

	return headerLen, nil
}

func stripRTPPayloadExtension(packet, opus []byte) []byte {
	if len(packet) < rtpHeaderSize+4 || packet[0]&0x10 == 0 || binary.BigEndian.Uint16(packet[12:14]) != 0xbede {
		return opus
	}

	extBytes := int(binary.BigEndian.Uint16(packet[14:16])) * 4
	if extBytes >= len(opus) {
		return opus
	}

	return opus[extBytes:]
}

func sealRTPPayload(mode rtpEncryptionMode, secret, header, opus []byte, nonceCounter uint32) ([]byte, error) {
	nonceSuffix := make([]byte, rtpNonceSuffixSize)
	binary.BigEndian.PutUint32(nonceSuffix, nonceCounter)

	switch mode {
	case rtpEncryptionModeAES256GCMRTPSize:
		// Discord's preferred RTP-size AEAD mode uses a 32-byte AES-256-GCM key
		// and appends the 32-bit nonce suffix to the encrypted payload.
		// https://docs.discord.com/developers/topics/voice-connections#transport-encryption-modes
		block, err := aes.NewCipher(secret)
		if err != nil {
			return nil, fmt.Errorf("create Discord voice AES cipher: %w", err)
		}

		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("create Discord voice AES-GCM: %w", err)
		}

		nonce := make([]byte, aead.NonceSize())
		copy(nonce, nonceSuffix)

		return append(aead.Seal(nil, nonce, opus, header), nonceSuffix...), nil
	case rtpEncryptionModeXChaCha20Poly1305RTPSize:
		// Discord requires RTP-size XChaCha20-Poly1305 support; it uses the same
		// appended 32-bit nonce suffix, zero-extended to the AEAD nonce size.
		// https://docs.discord.com/developers/topics/voice-connections#transport-encryption-modes
		aead, err := chacha20poly1305.NewX(secret)
		if err != nil {
			return nil, fmt.Errorf("create Discord voice XChaCha20-Poly1305: %w", err)
		}

		nonce := make([]byte, aead.NonceSize())
		copy(nonce, nonceSuffix)

		return append(aead.Seal(nil, nonce, opus, header), nonceSuffix...), nil
	default:
		return nil, fmt.Errorf("unsupported Discord voice encryption mode %q", mode)
	}
}

func openRTPPayload(mode rtpEncryptionMode, secret, header, ciphertext []byte) ([]byte, error) {
	// Discord appends the 32-bit nonce suffix to RTP-size encrypted payloads;
	// receivers strip it before opening the AEAD payload.
	// https://docs.discord.com/developers/topics/voice-connections#transport-encryption-modes
	nonceSuffix := ciphertext[len(ciphertext)-rtpNonceSuffixSize:]
	ciphertext = ciphertext[:len(ciphertext)-rtpNonceSuffixSize]

	switch mode {
	case rtpEncryptionModeAES256GCMRTPSize:
		// AES256-GCM RTP-size packets authenticate the unencrypted RTP header as
		// associated data and decrypt only the encrypted audio payload.
		block, err := aes.NewCipher(secret)
		if err != nil {
			return nil, fmt.Errorf("create Discord voice AES cipher: %w", err)
		}

		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("create Discord voice AES-GCM: %w", err)
		}

		nonce := make([]byte, aead.NonceSize())
		copy(nonce, nonceSuffix)

		plaintext, err := aead.Open(nil, nonce, ciphertext, header)
		if err != nil {
			return nil, fmt.Errorf("open Discord voice AES RTP payload: %w", err)
		}

		return plaintext, nil
	case rtpEncryptionModeXChaCha20Poly1305RTPSize:
		// XChaCha20-Poly1305 RTP-size packets use the same header-associated-data
		// framing with a 24-byte nonce formed from the appended suffix.
		aead, err := chacha20poly1305.NewX(secret)
		if err != nil {
			return nil, fmt.Errorf("create Discord voice XChaCha20-Poly1305: %w", err)
		}

		nonce := make([]byte, aead.NonceSize())
		copy(nonce, nonceSuffix)

		plaintext, err := aead.Open(nil, nonce, ciphertext, header)
		if err != nil {
			return nil, fmt.Errorf("open Discord voice XChaCha RTP payload: %w", err)
		}

		return plaintext, nil
	default:
		return nil, fmt.Errorf("unsupported Discord voice encryption mode %q", mode)
	}
}

func (v *voiceConnection) writeVoiceJSON(conn *websocket.Conn, value any) error {
	v.cond.L.Lock()
	defer v.cond.L.Unlock()

	if v.status == voiceConnectionStatusDead || v.wsConn != conn {
		return errors.New("discord voice gateway is not connected")
	}

	if err := conn.WriteJSON(value); err != nil {
		return fmt.Errorf("write Discord voice gateway JSON: %w", err)
	}

	return nil
}

func (v *voiceConnection) writeVoiceBinary(conn *websocket.Conn, op byte, payload []byte) error {
	v.cond.L.Lock()
	defer v.cond.L.Unlock()

	if v.status == voiceConnectionStatusDead || v.wsConn != conn {
		return errors.New("discord voice gateway is not connected")
	}

	message := make([]byte, 0, len(payload)+1)
	message = append(message, op)
	message = append(message, payload...)

	if err := conn.WriteMessage(websocket.BinaryMessage, message); err != nil {
		return fmt.Errorf("write Discord voice gateway binary message: %w", err)
	}

	return nil
}

func (v *voiceConnection) speakerID(ssrc uint32) string {
	v.cond.L.Lock()
	defer v.cond.L.Unlock()

	return v.ssrcUsers[ssrc]
}

func (v *voiceConnection) setSpeakerID(ssrc uint32, userID string) {
	v.cond.L.Lock()
	defer v.cond.L.Unlock()

	v.ssrcUsers[ssrc] = userID
}

func (v *voiceConnection) setSpeaking(speaking bool) error {
	// speakingData is Discord Voice Opcode 5's Speaking payload body.
	// https://docs.discord.com/developers/topics/voice-connections#speaking
	type speakingData struct {
		Speaking int    `json:"speaking"`
		Delay    int    `json:"delay"`
		SSRC     uint32 `json:"ssrc"`
	}

	// speakingPayload wraps the speaking body with Discord voice opcode 5.
	// https://docs.discord.com/developers/topics/opcodes-and-status-codes#voice-voice-opcodes
	type speakingPayload struct {
		Op voiceOpcode  `json:"op"`
		D  speakingData `json:"d"`
	}

	v.cond.L.Lock()
	v.speaking = speaking
	conn := v.wsConn
	ssrc := v.ssrc
	v.cond.L.Unlock()

	if conn == nil || ssrc == 0 {
		return nil
	}

	value := 0
	if speaking {
		value = 1
	}

	// Discord Voice Opcode 5 Speaking sets the current speaking bitmask and SSRC;
	// docs require sending it before audio, with `delay` set to 0 for bots.
	// https://docs.discord.com/developers/topics/voice-connections#speaking
	return v.writeVoiceJSON(conn, speakingPayload{Op: voiceOpSpeaking, D: speakingData{Speaking: value, Delay: 0, SSRC: ssrc}})
}

func (v *voiceConnection) die(err error) {
	v.cond.L.Lock()
	if v.status == voiceConnectionStatusDead {
		v.cond.L.Unlock()
		return
	}

	v.status = voiceConnectionStatusDead
	if err != nil {
		v.err = err
	}

	conn := v.wsConn
	udpConn := v.udpConn
	v.wsConn = nil
	v.udpConn = nil
	close(v.dead)
	v.cond.Broadcast()
	v.cond.L.Unlock()

	if conn != nil {
		_ = conn.Close()
	}

	if udpConn != nil {
		_ = udpConn.Close()
	}
}

const (
	// DAVE MLS External Sender Package carries the MLS external sender extension material.
	// https://daveprotocol.com/#dave_mls_external_sender_package-25
	daveMlsExternalSenderOp = 25
	// DAVE MLS Key Package sends this client's raw MLS KeyPackage bytes.
	// https://daveprotocol.com/#dave_mls_key_package-26
	daveMlsKeyPackageOp = 26
	// DAVE MLS Proposals carries append/revoke proposal operations.
	// https://daveprotocol.com/#dave_mls_proposals-27
	daveMlsProposalsOp = 27
	// DAVE MLS Commit Welcome sends the generated MLS Commit and optional Welcome.
	// https://daveprotocol.com/#dave_mls_commit_welcome-28
	daveMlsCommitWelcomeOp = 28
	// DAVE MLS Announce Commit Transition carries an accepted commit transition.
	// https://daveprotocol.com/#dave_mls_announce_commit_transition-29
	daveMlsAnnounceCommitOp = 29
	// DAVE MLS Welcome carries Welcome data for joining an MLS group.
	// https://daveprotocol.com/#dave_mls_welcome-30
	daveMlsWelcomeOp = 30

	// MLS protocol version 1.0.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-17.1
	mlsVersion10 = 0x0001
	// PublicMessage wire format.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-6
	mlsWireFormatPublic = 0x0001
	// Ratchet Tree extension type.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-17.4
	mlsExtensionRatchetTree = 0x0002
	// External Senders extension type.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-17.4
	mlsExtensionExternalSend = 0x0005
	// DAVE v1 uses MLS_128_DHKEMP256_AES128GCM_SHA256_P256.
	// https://daveprotocol.com/#cryptographic-specification
	mlsCipherSuiteDAVEV1 = 0x0002
	// Basic credential type.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-17.5
	mlsCredentialTypeBasic = 0x0001
	// Member sender type.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-6.1
	mlsSenderMember = 0x01
	// External sender type.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-6.1
	mlsSenderExternal = 0x02
	// Proposal content type.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-6.1
	mlsContentTypeProposal = 0x02
	// Commit content type.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-6.1
	mlsContentTypeCommit = 0x03
	// Add proposal type.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-17.3
	mlsProposalAdd = 0x0001
	// Remove proposal type.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-17.3
	mlsProposalRemove = 0x0003
	// ProposalOrRef reference variant.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-12.4.1
	mlsProposalOrRefRef = 0x02
	// MLS labeled signatures and KDF labels use this fixed prefix.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-5.1.2
	mlsSignLabelPrefix = "MLS 1.0 "
	// Proposal reference hash label.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-12.4.1
	mlsProposalRefLabel = "Proposal Reference"
	// KeyPackage reference hash label.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-10
	mlsKeyPackageRefLabel = "KeyPackage Reference"
	// LeafNode signature label.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-7.2
	mlsLeafNodeTBSLabel = "LeafNodeTBS"
	// KeyPackage signature label.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-10
	mlsKeyPackageTBSLabel = "KeyPackageTBS"
	// FramedContent signature label.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-6.1
	mlsFramedContentTBSLabel = "FramedContentTBS"
	// GroupInfo signature label.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-11.1
	mlsGroupInfoTBSLabel = "GroupInfoTBS"
	// DAVE sender media keys are exported from MLS with this label.
	// https://daveprotocol.com/#sender-key-derivation
	daveMediaExporterLabel = "Discord Secure Frames v0"
	// P-256 uncompressed public keys are 65 bytes in TLS/MLS wire form.
	// https://www.rfc-editor.org/rfc/rfc8446.html#section-4.2.8.2
	mlsP256PublicKeyWireSize = 65
	// DAVE v1's MLS ciphersuite uses SHA-256.
	mlsHashSize = sha256.Size
	// DAVE media encryption uses 128-bit AES-GCM sender keys.
	// https://daveprotocol.com/#sender-key-derivation
	daveMediaKeySize = 16
	// DAVE media AES-GCM nonces are 96 bits.
	// https://daveprotocol.com/#cryptographic-specification
	daveMediaNonceSize = 12
	// DAVE media frames truncate AES-GCM tags to 8 bytes.
	// https://daveprotocol.com/#media-encryption
	daveMediaTagSize = 8
	// DAVE media supplemental data ends with the 0xfafa magic marker.
	// https://daveprotocol.com/#media-encryption
	daveMagicMarker = 0xfafa
	// Discord's Opus silence frame is f8 ff fe and is passed through outside DAVE media encryption.
	// https://docs.discord.com/developers/topics/voice-connections#voice-data-interpolation
	daveOpusSilence0 = 0xf8
	daveOpusSilence1 = 0xff
	daveOpusSilence2 = 0xfe
)

type mlsLeafNodeSource byte

// LeafNodeSource is defined by MLS 1.0 for the LeafNode source field.
// https://www.rfc-editor.org/rfc/rfc9420.html#section-7.3
const (
	// KeyPackage means the leaf node is used in a KeyPackage and is followed by lifetime fields.
	mlsLeafNodeSourceKeyPackage mlsLeafNodeSource = 1
	// Update means the leaf node is used in an Update proposal.
	mlsLeafNodeSourceUpdate mlsLeafNodeSource = 2
	// Commit means the leaf node is used in a Commit path and is followed by the parent hash.
	mlsLeafNodeSourceCommit mlsLeafNodeSource = 3
)

type daveProposalsOperation byte

// DAVE MLS proposals opcode 27 tells clients to append or revoke proposals.
// https://daveprotocol.com/#proposal-handling
const (
	// Append carries MLS proposal messages to add to the local pending proposal set.
	daveProposalsAppend daveProposalsOperation = 0
	// Revoke carries proposal references to remove from the local pending proposal set.
	daveProposalsRevoke daveProposalsOperation = 1
)

type daveSession struct {
	userID         string
	channelID      string
	initKey        *ecdh.PrivateKey
	leafKey        *ecdh.PrivateKey
	signingKey     *ecdsa.PrivateKey
	externalSender []byte
	selfLeafNode   []byte
	keyPackage     []byte
	epochSecret    []byte
	mediaSecret    []byte
	initSecret     []byte
	interimHash    []byte
	mediaNonce     uint32
}

func newDaveSession(userID, channelID string) (*daveSession, error) {
	initKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate DAVE MLS init key: %w", err)
	}

	leafKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate DAVE MLS leaf key: %w", err)
	}

	signingKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate DAVE MLS signing key: %w", err)
	}

	return &daveSession{userID: userID, channelID: channelID, initKey: initKey, leafKey: leafKey, signingKey: signingKey}, nil
}

func (d *daveSession) setExternalSender(payload []byte) {
	d.externalSender = append(d.externalSender[:0], payload...)
}

func (d *daveSession) processProposals(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, errors.New("DAVE MLS proposals payload is empty")
	}

	operation := daveProposalsOperation(payload[0])
	reader := mlsReader{data: payload[1:]}

	vector, err := reader.readVector()
	if err != nil {
		return nil, err
	}

	if reader.remaining() != 0 {
		return nil, fmt.Errorf("DAVE MLS proposals has %d trailing bytes", reader.remaining())
	}

	switch operation {
	case daveProposalsAppend:
		return d.parseAppendedProposals(vector)
	case daveProposalsRevoke:
		reader := mlsReader{data: vector}
		for reader.remaining() > 0 {
			if _, err := reader.readVector(); err != nil {
				return nil, err
			}
		}
	}

	return nil, nil
}

func (d *daveSession) parseAppendedProposals(vector []byte) ([]byte, error) {
	reader := mlsReader{data: vector}
	for reader.remaining() > 0 {
		proposal, err := reader.readProposalMessage()
		if err != nil {
			return nil, err
		}

		if proposal.proposalType == mlsProposalAdd {
			commitWelcome, err := d.commitAddProposal(&proposal, mlsHashWithLabel(mlsProposalRefLabel, proposal.contentAuth))
			if err != nil {
				return nil, err
			}

			return commitWelcome, nil
		}
	}

	return nil, nil
}

type daveProposalMessageInfo struct {
	proposalType int
	groupID      []byte
	contentAuth  []byte
	keyPackage   []byte
	initKey      []byte
	leafNode     []byte
}

func (d *daveSession) keyPackageMessage() ([]byte, error) {
	userID, err := strconv.ParseUint(d.userID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse Discord user ID for DAVE MLS credential: %w", err)
	}

	leafNode, err := d.buildLeafNode(userID)
	if err != nil {
		return nil, err
	}

	initKeyVector := mlsVector(d.initKey.PublicKey().Bytes())
	keyPackageTBS := make([]byte, 0, 4+len(initKeyVector)+len(leafNode)+1)
	// MLS KeyPackage.version, fixed to MLS 1.0 for DAVE v1.
	keyPackageTBS = binary.BigEndian.AppendUint16(keyPackageTBS, mlsVersion10)
	// MLS KeyPackage.cipher_suite, Discord DAVE v1's MLS ciphersuite.
	keyPackageTBS = binary.BigEndian.AppendUint16(keyPackageTBS, mlsCipherSuiteDAVEV1)
	// MLS KeyPackage.init_key, vector-encoded HPKE init public key.
	keyPackageTBS = append(keyPackageTBS, initKeyVector...)
	// MLS KeyPackage.leaf_node, already encoded by buildLeafNode.
	keyPackageTBS = append(keyPackageTBS, leafNode...)
	// MLS KeyPackage.extensions, empty vector.
	keyPackageTBS = append(keyPackageTBS, 0)

	signature, err := d.signMLS(mlsKeyPackageTBSLabel, keyPackageTBS)
	if err != nil {
		return nil, err
	}

	keyPackage := slices.Concat(keyPackageTBS, mlsVector(signature))

	d.selfLeafNode = append(d.selfLeafNode[:0], leafNode...)
	d.keyPackage = append(d.keyPackage[:0], keyPackage...)

	return keyPackage, nil
}

func (d *daveSession) buildLeafNode(userID uint64) ([]byte, error) {
	signingPublicKey, err := d.signingKey.PublicKey.Bytes()
	if err != nil {
		return nil, fmt.Errorf("encode DAVE MLS signing public key: %w", err)
	}

	if len(d.leafKey.PublicKey().Bytes()) != mlsP256PublicKeyWireSize || len(signingPublicKey) != mlsP256PublicKeyWireSize {
		return nil, errors.New("DAVE MLS P-256 public key has unexpected wire size")
	}

	leafPublicKey := mlsVector(d.leafKey.PublicKey().Bytes())
	signaturePublicKey := mlsVector(signingPublicKey)
	credential := basicCredential(userID)
	capabilities := daveCapabilities()
	leafNodeTBS := make([]byte, 0, len(leafPublicKey)+len(signaturePublicKey)+len(credential)+len(capabilities)+18)
	// MLS LeafNode.encryption_key, the vector-encoded HPKE leaf public key.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-7.2
	leafNodeTBS = append(leafNodeTBS, leafPublicKey...)
	// MLS LeafNode.signature_key, the vector-encoded signature public key.
	leafNodeTBS = append(leafNodeTBS, signaturePublicKey...)
	// MLS LeafNode.credential, a Basic credential carrying the Discord user ID.
	leafNodeTBS = append(leafNodeTBS, credential...)
	// MLS LeafNode.capabilities advertises DAVE's supported MLS version, ciphersuite, and credential type.
	leafNodeTBS = append(leafNodeTBS, capabilities...)
	// MLS LeafNode.leaf_node_source is KeyPackage for DAVE's outbound KeyPackage.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-7.3
	leafNodeTBS = append(leafNodeTBS, byte(mlsLeafNodeSourceKeyPackage))
	// KeyPackage leaf nodes carry a Lifetime; DAVE uses [0, max uint64].
	leafNodeTBS = binary.BigEndian.AppendUint64(leafNodeTBS, 0)
	leafNodeTBS = binary.BigEndian.AppendUint64(leafNodeTBS, ^uint64(0))
	// MLS LeafNode.extensions, empty vector.
	leafNodeTBS = append(leafNodeTBS, 0)

	signature, err := d.signMLS(mlsLeafNodeTBSLabel, leafNodeTBS)
	if err != nil {
		return nil, err
	}

	// MLS LeafNode.signature signs the LeafNodeTBS using the LeafNodeTBS label.
	return slices.Concat(leafNodeTBS, mlsVector(signature)), nil
}

func (d *daveSession) signMLS(label string, content []byte) ([]byte, error) {
	signedContent := slices.Concat(mlsVector([]byte(mlsSignLabelPrefix+label)), mlsVector(content))
	digest := sha256.Sum256(signedContent)

	signature, err := ecdsa.SignASN1(rand.Reader, d.signingKey, digest[:])
	if err != nil {
		return nil, fmt.Errorf("sign DAVE MLS %s: %w", label, err)
	}

	return signature, nil
}

func (d *daveSession) commitAddProposal(proposal *daveProposalMessageInfo, proposalRef []byte) ([]byte, error) {
	oldTreeHash := mlsTreeHashLeaf(0, d.selfLeafNode)

	oldContext := d.groupContext(proposal.groupID, 0, oldTreeHash, nil)
	if len(d.epochSecret) == 0 {
		initialSecret := make([]byte, mlsHashSize)
		if _, err := rand.Read(initialSecret); err != nil {
			return nil, fmt.Errorf("generate DAVE MLS initial secret: %w", err)
		}

		zero := make([]byte, mlsHashSize)

		preJoinerSecret, err := hkdf.Extract(sha256.New, zero, initialSecret)
		if err != nil {
			return nil, fmt.Errorf("extract DAVE MLS pre-joiner secret: %w", err)
		}

		joinerSecret, err := mlsExpandWithLabel(preJoinerSecret, "joiner", oldContext, mlsHashSize)
		if err != nil {
			return nil, err
		}

		preEpochSecret, err := hkdf.Extract(sha256.New, zero, joinerSecret)
		if err != nil {
			return nil, fmt.Errorf("extract DAVE MLS pre-epoch secret: %w", err)
		}

		d.epochSecret, err = mlsExpandWithLabel(preEpochSecret, "epoch", oldContext, mlsHashSize)
		if err != nil {
			return nil, err
		}

		d.initSecret, err = mlsDeriveSecret(d.epochSecret, "init")
		if err != nil {
			return nil, err
		}
	}

	confirmationKey0, err := mlsDeriveSecret(d.epochSecret, "confirm")
	if err != nil {
		return nil, err
	}

	confirmationTag0 := mlsMAC(confirmationKey0, nil)
	d.interimHash = mlsHash(slices.Clone(mlsVector(confirmationTag0)))

	newTreeHash := mlsTreeHashParent(mlsTreeHashLeaf(0, d.selfLeafNode), mlsTreeHashLeaf(1, proposal.leafNode))
	proposalReference := slices.Concat([]byte{mlsProposalOrRefRef}, mlsVector(proposalRef))
	// MLS Commit.proposals, containing one ProposalOrRef reference to Discord's Add proposal.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-12.4.1
	commit := mlsVector(proposalReference)
	// MLS Commit.path, empty optional path.
	commit = append(commit, 0)

	groupID := mlsVector(proposal.groupID)
	framedContent := make([]byte, 0, len(groupID)+8+1+4+2+len(commit))
	// MLS FramedContent.group_id, the proposal's MLS group ID.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-6.1
	framedContent = append(framedContent, groupID...)
	// MLS FramedContent.epoch, committing the transition from epoch 0.
	framedContent = binary.BigEndian.AppendUint64(framedContent, 0)
	// MLS FramedContent.sender, a member sender at leaf index 0.
	framedContent = append(framedContent, mlsSenderMember)
	framedContent = binary.BigEndian.AppendUint32(framedContent, 0)
	// MLS FramedContent.authenticated_data, empty vector, followed by content_type Commit.
	framedContent = append(framedContent, 0, mlsContentTypeCommit)
	// MLS FramedContent.content, a Commit payload.
	framedContent = append(framedContent, commit...)

	framedContentTBS := make([]byte, 0, 4+len(framedContent)+len(oldContext))
	// MLS FramedContentTBS.version and wire_format for a PublicMessage.
	framedContentTBS = binary.BigEndian.AppendUint16(framedContentTBS, mlsVersion10)
	framedContentTBS = binary.BigEndian.AppendUint16(framedContentTBS, mlsWireFormatPublic)
	// MLS FramedContentTBS.content and group_context bind the commit to epoch 0.
	framedContentTBS = append(framedContentTBS, framedContent...)
	framedContentTBS = append(framedContentTBS, oldContext...)

	signature, err := d.signMLS(mlsFramedContentTBSLabel, framedContentTBS)
	if err != nil {
		return nil, err
	}

	signatureVector := mlsVector(signature)
	confirmedInput := make([]byte, 0, 2+len(framedContent)+len(signatureVector))
	// MLS confirmed_transcript_hash input is wire_format, FramedContent, and signature.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-8.2
	confirmedInput = binary.BigEndian.AppendUint16(confirmedInput, mlsWireFormatPublic)
	confirmedInput = append(confirmedInput, framedContent...)
	confirmedInput = append(confirmedInput, signatureVector...)
	confirmedHash := mlsHash(slices.Concat(d.interimHash, confirmedInput))
	newContext := d.groupContext(proposal.groupID, 1, newTreeHash, confirmedHash)

	secrets, err := d.nextEpochSecrets(newContext)
	if err != nil {
		return nil, err
	}

	confirmationTag := mlsMAC(secrets.confirmationKey, confirmedHash)
	confirmationTagVector := mlsVector(confirmationTag)
	auth := make([]byte, 0, len(signatureVector)+len(confirmationTagVector))
	// MLS FramedContentAuthData.signature followed by the Commit confirmation tag.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-6.1
	auth = append(auth, signatureVector...)
	auth = append(auth, confirmationTagVector...)

	membershipKey, err := mlsDeriveSecret(d.epochSecret, "membership")
	if err != nil {
		return nil, err
	}

	membershipTag := mlsMAC(membershipKey, slices.Concat(framedContentTBS, auth))
	membershipTagVector := mlsVector(membershipTag)
	commitMessage := make([]byte, 0, 4+len(framedContent)+len(auth)+len(membershipTagVector))
	// MLS PublicMessage.version and wire_format.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-6
	commitMessage = binary.BigEndian.AppendUint16(commitMessage, mlsVersion10)
	commitMessage = binary.BigEndian.AppendUint16(commitMessage, mlsWireFormatPublic)
	// MLS PublicMessage.content.
	commitMessage = append(commitMessage, framedContent...)
	// MLS PublicMessage.auth contains signature and confirmation_tag for Commits.
	commitMessage = append(commitMessage, auth...)
	// MLS PublicMessage.membership_tag authenticates the member sender.
	commitMessage = append(commitMessage, membershipTagVector...)

	welcome, err := d.welcomeMessage(proposal, newContext, confirmationTag, &secrets)
	if err != nil {
		return nil, err
	}

	d.epochSecret = append(d.epochSecret[:0], secrets.epochSecret...)
	d.mediaSecret = append(d.mediaSecret[:0], secrets.epochSecret...)
	d.initSecret = append(d.initSecret[:0], secrets.initSecret...)

	return slices.Concat(commitMessage, welcome), nil
}

type daveEpochSecrets struct {
	joinerSecret    []byte
	welcomeSecret   []byte
	epochSecret     []byte
	initSecret      []byte
	confirmationKey []byte
}

func (d *daveSession) nextEpochSecrets(groupContext []byte) (daveEpochSecrets, error) {
	zero := make([]byte, mlsHashSize)

	secret, err := hkdf.Extract(sha256.New, zero, d.initSecret)
	if err != nil {
		return daveEpochSecrets{}, fmt.Errorf("extract DAVE MLS epoch input secret: %w", err)
	}

	joinerSecret, err := mlsExpandWithLabel(secret, "joiner", groupContext, mlsHashSize)
	if err != nil {
		return daveEpochSecrets{}, err
	}

	preEpochSecret, err := hkdf.Extract(sha256.New, zero, joinerSecret)
	if err != nil {
		return daveEpochSecrets{}, fmt.Errorf("extract DAVE MLS pre-epoch secret: %w", err)
	}

	welcomeSecret, err := mlsDeriveSecret(preEpochSecret, "welcome")
	if err != nil {
		return daveEpochSecrets{}, err
	}

	epochSecret, err := mlsExpandWithLabel(preEpochSecret, "epoch", groupContext, mlsHashSize)
	if err != nil {
		return daveEpochSecrets{}, err
	}

	confirmationKey, err := mlsDeriveSecret(epochSecret, "confirm")
	if err != nil {
		return daveEpochSecrets{}, err
	}

	initSecret, err := mlsDeriveSecret(epochSecret, "init")
	if err != nil {
		return daveEpochSecrets{}, err
	}

	return daveEpochSecrets{joinerSecret: joinerSecret, welcomeSecret: welcomeSecret, epochSecret: epochSecret, initSecret: initSecret, confirmationKey: confirmationKey}, nil
}

func (d *daveSession) processWelcome(payload []byte) error {
	reader := mlsReader{data: payload}

	cipherSuite, err := reader.readUint16()
	if err != nil {
		return err
	}

	if cipherSuite != mlsCipherSuiteDAVEV1 {
		return fmt.Errorf("DAVE MLS welcome cipher suite %d", cipherSuite)
	}

	encryptedSecrets, err := reader.readVector()
	if err != nil {
		return err
	}

	encryptedGroupInfo, err := reader.readVector()
	if err != nil {
		return err
	}

	if reader.remaining() != 0 {
		return fmt.Errorf("DAVE MLS welcome has %d trailing bytes", reader.remaining())
	}

	groupSecrets, err := d.decryptWelcomeGroupSecrets(encryptedSecrets, encryptedGroupInfo)
	if err != nil {
		return err
	}

	if len(groupSecrets) == 0 {
		return errors.New("DAVE MLS welcome did not include this key package")
	}

	joinerSecret, err := readWelcomeJoinerSecret(groupSecrets)
	if err != nil {
		return err
	}

	return d.applyWelcomeSecrets(joinerSecret, encryptedGroupInfo)
}

func (d *daveSession) decryptWelcomeGroupSecrets(encryptedSecrets, encryptedGroupInfo []byte) ([]byte, error) {
	keyPackageRef := mlsHashWithLabel(mlsKeyPackageRefLabel, d.keyPackage)
	secretsReader := mlsReader{data: encryptedSecrets}

	for secretsReader.remaining() > 0 {
		newMemberRef, err := secretsReader.readVector()
		if err != nil {
			return nil, err
		}

		enc, err := secretsReader.readVector()
		if err != nil {
			return nil, err
		}

		ciphertext, err := secretsReader.readVector()
		if err != nil {
			return nil, err
		}

		if !bytes.Equal(newMemberRef, keyPackageRef) {
			continue
		}

		privateKey, err := hpke.NewDHKEMPrivateKey(d.initKey)
		if err != nil {
			return nil, fmt.Errorf("create DAVE MLS HPKE private key: %w", err)
		}

		recipient, err := hpke.NewRecipient(enc, privateKey, hpke.HKDFSHA256(), hpke.AES128GCM(), mlsEncryptContext("Welcome", encryptedGroupInfo))
		if err != nil {
			return nil, fmt.Errorf("create DAVE MLS HPKE recipient: %w", err)
		}

		groupSecrets, err := recipient.Open(nil, ciphertext)
		if err != nil {
			return nil, fmt.Errorf("decrypt DAVE MLS group secrets: %w", err)
		}

		return groupSecrets, nil
	}

	return nil, nil
}

func readWelcomeJoinerSecret(groupSecrets []byte) ([]byte, error) {
	secrets := mlsReader{data: groupSecrets}

	joinerSecret, err := secrets.readVector()
	if err != nil {
		return nil, err
	}

	pathSecretPresent, err := secrets.readUint8()
	if err != nil {
		return nil, err
	}

	if pathSecretPresent == 1 {
		if _, err := secrets.readVector(); err != nil {
			return nil, err
		}
	} else if pathSecretPresent != 0 {
		return nil, fmt.Errorf("DAVE MLS welcome path secret presence %d", pathSecretPresent)
	}

	psks, err := secrets.readVector()
	if err != nil {
		return nil, err
	}

	if len(psks) != 0 {
		return nil, errors.New("DAVE MLS welcome PSKs are not supported")
	}

	if secrets.remaining() != 0 {
		return nil, fmt.Errorf("DAVE MLS welcome group secrets has %d trailing bytes", secrets.remaining())
	}

	return joinerSecret, nil
}

func (d *daveSession) applyWelcomeSecrets(joinerSecret, encryptedGroupInfo []byte) error {
	zero := make([]byte, mlsHashSize)

	preEpochSecret, err := hkdf.Extract(sha256.New, zero, joinerSecret)
	if err != nil {
		return fmt.Errorf("extract DAVE MLS welcome pre-epoch secret: %w", err)
	}

	welcomeSecret, err := mlsDeriveSecret(preEpochSecret, "welcome")
	if err != nil {
		return err
	}

	welcomeKey, err := mlsExpandWithLabel(welcomeSecret, "key", nil, 16)
	if err != nil {
		return err
	}

	welcomeNonce, err := mlsExpandWithLabel(welcomeSecret, "nonce", nil, 12)
	if err != nil {
		return err
	}

	block, err := aes.NewCipher(welcomeKey)
	if err != nil {
		return fmt.Errorf("create DAVE MLS welcome AES cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("create DAVE MLS welcome AES-GCM: %w", err)
	}

	groupInfo, err := aead.Open(nil, welcomeNonce, encryptedGroupInfo, nil)
	if err != nil {
		return fmt.Errorf("decrypt DAVE MLS group info: %w", err)
	}

	groupContext, err := readMLSGroupContext(groupInfo)
	if err != nil {
		return err
	}

	epochSecret, err := mlsExpandWithLabel(preEpochSecret, "epoch", groupContext, mlsHashSize)
	if err != nil {
		return err
	}

	initSecret, err := mlsDeriveSecret(epochSecret, "init")
	if err != nil {
		return err
	}

	d.epochSecret = append(d.epochSecret[:0], epochSecret...)
	d.mediaSecret = append(d.mediaSecret[:0], epochSecret...)
	d.initSecret = append(d.initSecret[:0], initSecret...)

	return nil
}

func readMLSGroupContext(groupInfo []byte) ([]byte, error) {
	reader := mlsReader{data: groupInfo}

	start := reader.off
	if _, err := reader.readUint16(); err != nil {
		return nil, err
	}

	if _, err := reader.readUint16(); err != nil {
		return nil, err
	}

	if _, err := reader.readVector(); err != nil {
		return nil, err
	}

	if err := reader.skipUint64(); err != nil {
		return nil, err
	}

	if _, err := reader.readVector(); err != nil {
		return nil, err
	}

	if _, err := reader.readVector(); err != nil {
		return nil, err
	}

	if _, err := reader.readVector(); err != nil {
		return nil, err
	}

	return slices.Clone(groupInfo[start:reader.off]), nil
}

func (d *daveSession) decryptOpus(userID string, frame []byte) ([]byte, error) {
	if len(frame) == 3 && frame[0] == daveOpusSilence0 && frame[1] == daveOpusSilence1 && frame[2] == daveOpusSilence2 {
		return frame, nil
	}

	if len(d.mediaSecret) == 0 {
		return frame, nil
	}

	parsed, err := parseDaveMediaFrame(frame)
	if err != nil {
		return nil, err
	}

	key, err := d.mediaKey(userID, parsed.nonce>>24)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create DAVE media AES cipher: %w", err)
	}

	aead, err := aesgcm8.New(block)
	if err != nil {
		return nil, fmt.Errorf("create DAVE media AES-GCM-8: %w", err)
	}

	nonce := make([]byte, daveMediaNonceSize)
	binary.LittleEndian.PutUint32(nonce[8:], parsed.nonce)
	sealed := slices.Concat(parsed.ciphertext, parsed.tag)

	plaintext, err := aead.Open(nil, nonce, sealed, parsed.authenticated)
	if err != nil {
		return nil, fmt.Errorf("open DAVE media frame: %w", err)
	}

	return reconstructDaveMediaFrame(parsed.ranges, parsed.authenticated, plaintext, len(frame)), nil
}

func (d *daveSession) encryptOpus(userID string, frame []byte) ([]byte, error) {
	// Discord's Opus silence frame is not DAVE-encrypted.
	// https://docs.discord.com/developers/topics/voice-connections#voice-data-interpolation
	if len(frame) == 3 && frame[0] == daveOpusSilence0 && frame[1] == daveOpusSilence1 && frame[2] == daveOpusSilence2 {
		return frame, nil
	}

	if len(d.mediaSecret) == 0 {
		return frame, nil
	}

	nonceValue := d.mediaNonce
	d.mediaNonce++

	key, err := d.mediaKey(userID, nonceValue>>24)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create DAVE media AES cipher: %w", err)
	}

	aead, err := aesgcm8.New(block)
	if err != nil {
		return nil, fmt.Errorf("create DAVE media AES-GCM-8: %w", err)
	}

	nonce := make([]byte, daveMediaNonceSize)
	// DAVE media uses a 96-bit AES-GCM nonce with this sender's 32-bit nonce in the final bytes.
	// https://daveprotocol.com/#media-encryption
	binary.LittleEndian.PutUint32(nonce[8:], nonceValue)
	sealed := aead.Seal(nil, nonce, frame, nil)
	// DAVE media ciphertext keeps the Opus payload length; the 8-byte GCM tag moves into supplemental data.
	ciphertext := sealed[:len(frame)]
	tag := sealed[len(frame):]
	// DAVE supplemental data is tag, LEB128 nonce, one-byte supplemental length, then 0xfafa magic.
	supplemental := slices.Clone(tag)
	supplemental = appendDaveLEB128(supplemental, nonceValue)
	supplemental = append(supplemental, byte(len(supplemental)+3), 0xfa, 0xfa)

	// DAVE media frames are encrypted payload followed by supplemental data.
	return slices.Concat(ciphertext, supplemental), nil
}

func (d *daveSession) mediaKey(userID string, generation uint32) ([]byte, error) {
	user, err := strconv.ParseUint(userID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse Discord user ID for DAVE media key: %w", err)
	}

	userIDBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(userIDBytes, user)

	exporterSecret, err := mlsDeriveSecret(d.mediaSecret, "exporter")
	if err != nil {
		return nil, err
	}

	secret, err := mlsDeriveSecret(exporterSecret, daveMediaExporterLabel)
	if err != nil {
		return nil, err
	}

	contextHash := mlsHash(userIDBytes)

	baseSecret, err := mlsExpandWithLabel(secret, "exported", contextHash, daveMediaKeySize)
	if err != nil {
		return nil, err
	}

	for gen := uint32(0); ; gen++ {
		key, err := mlsExpandWithLabel(baseSecret, "key", binary.BigEndian.AppendUint32(nil, gen), daveMediaKeySize)
		if err != nil {
			return nil, err
		}

		if gen == generation {
			return key, nil
		}

		baseSecret, err = mlsExpandWithLabel(baseSecret, "secret", binary.BigEndian.AppendUint32(nil, gen), mlsHashSize)
		if err != nil {
			return nil, err
		}
	}
}

type daveMediaFrame struct {
	tag, authenticated, ciphertext []byte
	ranges                         []daveMediaRange
	nonce                          uint32
}

type daveMediaRange struct {
	offset, size int
}

func parseDaveMediaFrame(frame []byte) (daveMediaFrame, error) {
	if len(frame) < daveMediaTagSize+3 {
		return daveMediaFrame{}, errors.New("DAVE media frame is too short")
	}

	if binary.LittleEndian.Uint16(frame[len(frame)-2:]) != daveMagicMarker {
		return daveMediaFrame{}, errors.New("DAVE media frame marker missing")
	}

	supplementalSize := int(frame[len(frame)-3])
	if supplementalSize < daveMediaTagSize+3 || supplementalSize > len(frame) {
		return daveMediaFrame{}, fmt.Errorf("DAVE media supplemental size %d", supplementalSize)
	}

	supplemental := frame[len(frame)-supplementalSize:]

	nonce, nonceBytes, err := readDaveLEB128(supplemental[daveMediaTagSize : len(supplemental)-3])
	if err != nil {
		return daveMediaFrame{}, err
	}

	rangeBytes := supplemental[daveMediaTagSize+nonceBytes : len(supplemental)-3]

	ranges, err := parseDaveMediaRanges(rangeBytes)
	if err != nil {
		return daveMediaFrame{}, err
	}

	actualSize := len(frame) - supplementalSize
	parsed := daveMediaFrame{tag: supplemental[:daveMediaTagSize], nonce: nonce, ranges: ranges}

	frameIndex := 0
	for _, r := range ranges {
		if r.offset > actualSize || r.offset+r.size > actualSize || r.offset < frameIndex {
			return daveMediaFrame{}, errors.New("DAVE media range outside frame")
		}

		parsed.ciphertext = append(parsed.ciphertext, frame[frameIndex:r.offset]...)
		parsed.authenticated = append(parsed.authenticated, frame[r.offset:r.offset+r.size]...)
		frameIndex = r.offset + r.size
	}

	parsed.ciphertext = append(parsed.ciphertext, frame[frameIndex:actualSize]...)

	return parsed, nil
}

func parseDaveMediaRanges(data []byte) ([]daveMediaRange, error) {
	var ranges []daveMediaRange

	for len(data) > 0 {
		offset, n, err := readDaveLEB128(data)
		if err != nil {
			return nil, err
		}

		data = data[n:]

		size, n, err := readDaveLEB128(data)
		if err != nil {
			return nil, err
		}

		data = data[n:]

		ranges = append(ranges, daveMediaRange{offset: int(offset), size: int(size)})
	}

	return ranges, nil
}

func reconstructDaveMediaFrame(ranges []daveMediaRange, authenticated, plaintext []byte, frameSize int) []byte {
	frame := make([]byte, 0, frameSize)
	authIndex := 0
	plainIndex := 0

	for _, r := range ranges {
		plainBytes := r.offset - len(frame)
		frame = append(frame, plaintext[plainIndex:plainIndex+plainBytes]...)
		plainIndex += plainBytes

		frame = append(frame, authenticated[authIndex:authIndex+r.size]...)
		authIndex += r.size
	}

	return append(frame, plaintext[plainIndex:]...)
}

func readDaveLEB128(data []byte) (value uint32, n int, err error) {
	for i, b := range data {
		if i == 5 {
			return 0, 0, errors.New("DAVE media LEB128 is too long")
		}

		value |= uint32(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return value, i + 1, nil
		}
	}

	return 0, 0, errors.New("DAVE media LEB128 is truncated")
}

func appendDaveLEB128(data []byte, value uint32) []byte {
	for value >= 0x80 {
		data = append(data, byte(value&0x7f)|0x80)
		value >>= 7
	}

	return append(data, byte(value))
}

func mlsHashWithLabel(label string, content []byte) []byte {
	hashedContent := slices.Concat(mlsVector([]byte(mlsSignLabelPrefix+label)), mlsVector(content))
	digest := sha256.Sum256(hashedContent)

	return digest[:]
}

func (d *daveSession) groupContext(groupID []byte, epoch uint64, treeHash, confirmedHash []byte) []byte {
	groupIDVector := mlsVector(groupID)
	treeHashVector := mlsVector(treeHash)
	confirmedHashVector := mlsVector(confirmedHash)
	extensions := d.groupExtensions()
	groupContextBytes := make([]byte, 0, 4+len(groupIDVector)+8+len(treeHashVector)+len(confirmedHashVector)+len(extensions))
	// MLS GroupContext.version and cipher_suite.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-8
	groupContextBytes = binary.BigEndian.AppendUint16(groupContextBytes, mlsVersion10)
	groupContextBytes = binary.BigEndian.AppendUint16(groupContextBytes, mlsCipherSuiteDAVEV1)
	// MLS GroupContext.group_id.
	groupContextBytes = append(groupContextBytes, groupIDVector...)
	// MLS GroupContext.epoch.
	groupContextBytes = binary.BigEndian.AppendUint64(groupContextBytes, epoch)
	// MLS GroupContext.tree_hash.
	groupContextBytes = append(groupContextBytes, treeHashVector...)
	// MLS GroupContext.confirmed_transcript_hash.
	groupContextBytes = append(groupContextBytes, confirmedHashVector...)
	// MLS GroupContext.extensions, carrying DAVE's External Senders extension.
	groupContextBytes = append(groupContextBytes, extensions...)

	return groupContextBytes
}

func (d *daveSession) groupExtensions() []byte {
	// DAVE stores its external sender material inside the MLS External Senders extension_data vector.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-11.1.1
	extensionData := mlsVector(d.externalSender)
	extensionDataVector := mlsVector(extensionData)
	extension := make([]byte, 0, 2+len(extensionDataVector))
	// MLS Extension.extension_type, Discord DAVE's External Senders extension.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-17.4
	extension = binary.BigEndian.AppendUint16(extension, mlsExtensionExternalSend)
	// MLS Extension.extension_data.
	extension = append(extension, extensionDataVector...)

	// MLS GroupContext.extensions is a vector of Extension values.
	return mlsVector(extension)
}

func (d *daveSession) welcomeMessage(proposal *daveProposalMessageInfo, groupContext, confirmationTag []byte, secrets *daveEpochSecrets) ([]byte, error) {
	rTree := make([]byte, 0, 5+len(d.selfLeafNode)+len(proposal.leafNode))
	// MLS RatchetTree extension data contains the current self leaf and the added member leaf.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-11.1.1
	rTree = append(rTree, 1, 1)
	rTree = append(rTree, d.selfLeafNode...)
	rTree = append(rTree, 0, 1, 1)
	rTree = append(rTree, proposal.leafNode...)
	rTreeVector := mlsVector(mlsVector(rTree))
	rTreeExtension := make([]byte, 0, 2+len(rTreeVector))
	// MLS Extension.extension_type, Ratchet Tree.
	rTreeExtension = binary.BigEndian.AppendUint16(rTreeExtension, mlsExtensionRatchetTree)
	// MLS Extension.extension_data, the vector-encoded ratchet tree.
	rTreeExtension = append(rTreeExtension, rTreeVector...)
	rTreeExtensionVector := mlsVector(rTreeExtension)
	confirmationTagVector := mlsVector(confirmationTag)
	groupInfoTBS := make([]byte, 0, len(groupContext)+len(rTreeExtensionVector)+len(confirmationTagVector)+4)
	// MLS GroupInfoTBS.group_context.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-11.1
	groupInfoTBS = append(groupInfoTBS, groupContext...)
	// MLS GroupInfoTBS.extensions, carrying the Ratchet Tree extension.
	groupInfoTBS = append(groupInfoTBS, rTreeExtensionVector...)
	// MLS GroupInfoTBS.confirmation_tag.
	groupInfoTBS = append(groupInfoTBS, confirmationTagVector...)
	// MLS GroupInfoTBS.signer, member sender index 0.
	groupInfoTBS = binary.BigEndian.AppendUint32(groupInfoTBS, 0)

	signature, err := d.signMLS(mlsGroupInfoTBSLabel, groupInfoTBS)
	if err != nil {
		return nil, err
	}

	// MLS GroupInfo.signature signs GroupInfoTBS.
	groupInfoTBS = append(groupInfoTBS, mlsVector(signature)...)
	groupInfo := groupInfoTBS

	welcomeKey, err := mlsExpandWithLabel(secrets.welcomeSecret, "key", nil, 16)
	if err != nil {
		return nil, err
	}

	welcomeNonce, err := mlsExpandWithLabel(secrets.welcomeSecret, "nonce", nil, 12)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(welcomeKey)
	if err != nil {
		return nil, fmt.Errorf("create DAVE welcome AES cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create DAVE welcome AES-GCM: %w", err)
	}

	encryptedGroupInfo := aead.Seal(nil, welcomeNonce, groupInfo, nil)
	joinerSecretVector := mlsVector(secrets.joinerSecret)
	groupSecrets := make([]byte, 0, len(joinerSecretVector)+2)
	// MLS GroupSecrets.joiner_secret.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-12.4.3.1
	groupSecrets = append(groupSecrets, joinerSecretVector...)
	// MLS GroupSecrets.path_secret and PSKs, both empty for DAVE's Add-only Welcome.
	groupSecrets = append(groupSecrets, 0, 0)
	info := mlsEncryptContext("Welcome", encryptedGroupInfo)

	publicKey, err := ecdh.P256().NewPublicKey(proposal.initKey)
	if err != nil {
		return nil, fmt.Errorf("parse DAVE MLS add init key: %w", err)
	}

	hpkePublicKey, err := hpke.NewDHKEMPublicKey(publicKey)
	if err != nil {
		return nil, fmt.Errorf("create DAVE MLS HPKE public key: %w", err)
	}

	enc, sender, err := hpke.NewSender(hpkePublicKey, hpke.HKDFSHA256(), hpke.AES128GCM(), info)
	if err != nil {
		return nil, fmt.Errorf("create DAVE MLS HPKE sender: %w", err)
	}

	ciphertext, err := sender.Seal(nil, groupSecrets)
	if err != nil {
		return nil, fmt.Errorf("encrypt DAVE MLS group secrets: %w", err)
	}

	encVector := mlsVector(enc)
	ciphertextVector := mlsVector(ciphertext)
	hpkeCiphertext := make([]byte, 0, len(encVector)+len(ciphertextVector))
	// MLS HPKECiphertext.kem_output.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-12.4.3.1
	hpkeCiphertext = append(hpkeCiphertext, encVector...)
	// MLS HPKECiphertext.ciphertext, encrypting GroupSecrets.
	hpkeCiphertext = append(hpkeCiphertext, ciphertextVector...)
	newMemberRef := mlsHashWithLabel(mlsKeyPackageRefLabel, proposal.keyPackage)
	newMemberRefVector := mlsVector(newMemberRef)
	encryptedSecrets := make([]byte, 0, len(newMemberRefVector)+len(hpkeCiphertext))
	// MLS EncryptedGroupSecrets.new_member, the KeyPackageRef.
	encryptedSecrets = append(encryptedSecrets, newMemberRefVector...)
	// MLS EncryptedGroupSecrets.encrypted_group_secrets.
	encryptedSecrets = append(encryptedSecrets, hpkeCiphertext...)
	encryptedSecretsVector := mlsVector(encryptedSecrets)
	encryptedGroupInfoVector := mlsVector(encryptedGroupInfo)
	welcome := make([]byte, 0, 2+len(encryptedSecretsVector)+len(encryptedGroupInfoVector))
	// MLS Welcome.cipher_suite.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-12.4.3
	welcome = binary.BigEndian.AppendUint16(welcome, mlsCipherSuiteDAVEV1)
	// MLS Welcome.secrets.
	welcome = append(welcome, encryptedSecretsVector...)
	// MLS Welcome.encrypted_group_info.
	welcome = append(welcome, encryptedGroupInfoVector...)

	return welcome, nil
}

func mlsEncryptContext(label string, mlsContext []byte) []byte {
	return slices.Concat(mlsVector([]byte(mlsSignLabelPrefix+label)), mlsVector(mlsContext))
}

func mlsExpandWithLabel(secret []byte, label string, mlsContext []byte, length int) ([]byte, error) {
	labelVector := mlsVector([]byte(mlsSignLabelPrefix + label))
	contextVector := mlsVector(mlsContext)
	kdfLabel := make([]byte, 0, 2+len(labelVector)+len(contextVector))
	// MLS KDFLabel.length.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-5.1.2
	kdfLabel = binary.BigEndian.AppendUint16(kdfLabel, uint16(length))
	// MLS KDFLabel.label, prefixed with "MLS 1.0 ".
	kdfLabel = append(kdfLabel, labelVector...)
	// MLS KDFLabel.context.
	kdfLabel = append(kdfLabel, contextVector...)

	key, err := hkdf.Expand(sha256.New, secret, string(kdfLabel), length)
	if err != nil {
		return nil, fmt.Errorf("expand MLS %s secret: %w", label, err)
	}

	return key, nil
}

func mlsDeriveSecret(secret []byte, label string) ([]byte, error) {
	return mlsExpandWithLabel(secret, label, nil, mlsHashSize)
}

func mlsMAC(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)

	return mac.Sum(nil)
}

func mlsHash(data []byte) []byte {
	digest := sha256.Sum256(data)
	return digest[:]
}

func mlsTreeHashLeaf(index uint32, leafNode []byte) []byte {
	// MLS TreeHashInput.leaf node_type.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-7.8
	input := []byte{1}
	// MLS TreeHashInput.leaf.leaf_index.
	input = binary.BigEndian.AppendUint32(input, index)
	// MLS TreeHashInput.leaf.leaf_node_source, present leaf node.
	input = append(input, 1)
	// MLS TreeHashInput.leaf.leaf_node.
	input = append(input, leafNode...)

	return mlsHash(input)
}

func mlsTreeHashParent(leftHash, rightHash []byte) []byte {
	input := make([]byte, 1, 2+len(leftHash)+len(rightHash)+4)
	// MLS TreeHashInput.parent node_type.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-7.8
	input[0] = 2
	// MLS TreeHashInput.parent.parent_hash, absent for this DAVE parent node.
	input = append(input, 0)
	// MLS TreeHashInput.parent.left_hash.
	input = append(input, mlsVector(leftHash)...)
	// MLS TreeHashInput.parent.right_hash.
	input = append(input, mlsVector(rightHash)...)

	return mlsHash(input)
}

func basicCredential(userID uint64) []byte {
	credential := make([]byte, 0, 11)
	// MLS Credential.credential_type.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-5.3
	credential = binary.BigEndian.AppendUint16(credential, mlsCredentialTypeBasic)
	identity := make([]byte, 8)
	// MLS BasicCredential.identity value, Discord user ID in network byte order.
	binary.BigEndian.PutUint64(identity, userID)
	// MLS BasicCredential.identity vector.
	credential = append(credential, mlsVector(identity)...)

	return credential
}

func daveCapabilities() []byte {
	capabilities := make([]byte, 0, 12)
	version := binary.BigEndian.AppendUint16(nil, mlsVersion10)
	// MLS Capabilities.versions, DAVE v1 supports MLS 1.0.
	// https://www.rfc-editor.org/rfc/rfc9420.html#section-7.2
	capabilities = append(capabilities, mlsVector(version)...)
	cipherSuite := binary.BigEndian.AppendUint16(nil, mlsCipherSuiteDAVEV1)
	// MLS Capabilities.cipher_suites, DAVE v1's ciphersuite.
	capabilities = append(capabilities, mlsVector(cipherSuite)...)
	// MLS Capabilities.extensions and proposals, both empty for DAVE's KeyPackage.
	capabilities = append(capabilities, 0, 0)
	credentialType := binary.BigEndian.AppendUint16(nil, mlsCredentialTypeBasic)
	// MLS Capabilities.credentials, Basic credentials.
	capabilities = append(capabilities, mlsVector(credentialType)...)

	return capabilities
}

func mlsVector(data []byte) []byte {
	vector := mlsVarint(len(data))
	return append(vector, data...)
}

type mlsReader struct {
	data []byte
	off  int
}

func (r *mlsReader) remaining() int {
	return len(r.data) - r.off
}

func (r *mlsReader) readUint16() (int, error) {
	if r.remaining() < 2 {
		return 0, errors.New("MLS uint16 is truncated")
	}

	value := int(binary.BigEndian.Uint16(r.data[r.off : r.off+2]))
	r.off += 2

	return value, nil
}

func (r *mlsReader) readUint8() (int, error) {
	if r.remaining() < 1 {
		return 0, errors.New("MLS uint8 is truncated")
	}

	value := int(r.data[r.off])
	r.off++

	return value, nil
}

func (r *mlsReader) skipUint32() error {
	if r.remaining() < 4 {
		return errors.New("MLS uint32 is truncated")
	}

	r.off += 4

	return nil
}

func (r *mlsReader) skipUint64() error {
	if r.remaining() < 8 {
		return errors.New("MLS uint64 is truncated")
	}

	r.off += 8

	return nil
}

func (r *mlsReader) readProposalMessage() (daveProposalMessageInfo, error) {
	version, err := r.readUint16()
	if err != nil {
		return daveProposalMessageInfo{}, err
	}

	if version != mlsVersion10 {
		return daveProposalMessageInfo{}, fmt.Errorf("MLS proposal message version %d", version)
	}

	wireFormat, err := r.readUint16()
	if err != nil {
		return daveProposalMessageInfo{}, err
	}

	if wireFormat != mlsWireFormatPublic {
		return daveProposalMessageInfo{}, fmt.Errorf("MLS proposal message wire format %d", wireFormat)
	}

	contentAuthStart := r.off - 2

	groupID, err := r.readVector()
	if err != nil {
		return daveProposalMessageInfo{}, err
	}

	if err := r.skipUint64(); err != nil {
		return daveProposalMessageInfo{}, err
	}

	senderType, err := r.readUint8()
	if err != nil {
		return daveProposalMessageInfo{}, err
	}

	if senderType != mlsSenderExternal {
		return daveProposalMessageInfo{}, fmt.Errorf("MLS proposal sender type %d", senderType)
	}

	if err := r.skipUint32(); err != nil {
		return daveProposalMessageInfo{}, err
	}

	if _, err := r.readVector(); err != nil {
		return daveProposalMessageInfo{}, err
	}

	contentType, err := r.readUint8()
	if err != nil {
		return daveProposalMessageInfo{}, err
	}

	if contentType != mlsContentTypeProposal {
		return daveProposalMessageInfo{}, fmt.Errorf("MLS proposal content type %d", contentType)
	}

	proposalType, err := r.readUint16()
	if err != nil {
		return daveProposalMessageInfo{}, err
	}

	info := daveProposalMessageInfo{proposalType: proposalType, groupID: slices.Clone(groupID)}
	switch proposalType {
	case mlsProposalAdd:
		keyPackage, initKey, leafNode, err := r.readKeyPackage()
		if err != nil {
			return daveProposalMessageInfo{}, err
		}

		info.keyPackage = keyPackage
		info.initKey = initKey
		info.leafNode = leafNode
	case mlsProposalRemove:
		if err := r.skipUint32(); err != nil {
			return daveProposalMessageInfo{}, err
		}
	default:
		return daveProposalMessageInfo{}, fmt.Errorf("MLS proposal type %d is not supported", proposalType)
	}

	if _, err := r.readVector(); err != nil {
		return daveProposalMessageInfo{}, err
	}

	info.contentAuth = append(info.contentAuth, r.data[contentAuthStart:r.off]...)

	return info, nil
}

func (r *mlsReader) readKeyPackage() (keyPackage, initKey, leafNode []byte, err error) {
	start := r.off
	if _, err := r.readUint16(); err != nil {
		return nil, nil, nil, err
	}

	if _, err := r.readUint16(); err != nil {
		return nil, nil, nil, err
	}

	initKey, err = r.readVector()
	if err != nil {
		return nil, nil, nil, err
	}

	leafStart := r.off

	if err := r.skipLeafNode(); err != nil {
		return nil, nil, nil, err
	}

	leafNode = slices.Clone(r.data[leafStart:r.off])
	if _, err := r.readVector(); err != nil {
		return nil, nil, nil, err
	}

	if _, err := r.readVector(); err != nil {
		return nil, nil, nil, err
	}

	keyPackage = slices.Clone(r.data[start:r.off])

	return keyPackage, slices.Clone(initKey), leafNode, nil
}

func (r *mlsReader) skipLeafNode() error {
	if _, err := r.readVector(); err != nil {
		return err
	}

	if _, err := r.readVector(); err != nil {
		return err
	}

	credentialType, err := r.readUint16()
	if err != nil {
		return err
	}

	if _, err := r.readVector(); err != nil {
		return err
	}

	if credentialType != mlsCredentialTypeBasic {
		return fmt.Errorf("MLS leaf credential type %d", credentialType)
	}

	for range 5 {
		if _, err := r.readVector(); err != nil {
			return err
		}
	}

	sourceValue, err := r.readUint8()
	if err != nil {
		return err
	}

	source := mlsLeafNodeSource(sourceValue)

	switch source {
	case mlsLeafNodeSourceKeyPackage:
		if err := r.skipUint64(); err != nil {
			return err
		}

		if err := r.skipUint64(); err != nil {
			return err
		}
	case mlsLeafNodeSourceUpdate:
		// RFC 9420 defines LeafNodeSource update as struct{}, so there is no source-specific field to skip here.
	case mlsLeafNodeSourceCommit:
		if _, err := r.readVector(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("MLS leaf node source %d", source)
	}

	if _, err := r.readVector(); err != nil {
		return err
	}

	if _, err := r.readVector(); err != nil {
		return err
	}

	return nil
}

func (r *mlsReader) readVector() ([]byte, error) {
	length, err := r.readVarint()
	if err != nil {
		return nil, err
	}

	if r.remaining() < length {
		return nil, fmt.Errorf("MLS vector length %d exceeds remaining %d", length, r.remaining())
	}

	value := r.data[r.off : r.off+length]
	r.off += length

	return value, nil
}

func (r *mlsReader) readVarint() (int, error) {
	if r.remaining() == 0 {
		return 0, errors.New("MLS varint is truncated")
	}

	first := r.data[r.off]
	switch first >> 6 {
	case 0:
		r.off++
		return int(first), nil
	case 1:
		if r.remaining() < 2 {
			return 0, errors.New("MLS two-byte varint is truncated")
		}

		value := int(first&0x3f)<<8 | int(r.data[r.off+1])
		r.off += 2

		return value, nil
	case 2:
		if r.remaining() < 4 {
			return 0, errors.New("MLS four-byte varint is truncated")
		}

		value := int(first&0x3f)<<24 | int(r.data[r.off+1])<<16 | int(r.data[r.off+2])<<8 | int(r.data[r.off+3])
		r.off += 4

		return value, nil
	default:
		return 0, errors.New("MLS eight-byte varint is not valid for MLS vectors")
	}
}

func mlsVarint(n int) []byte {
	if n < 64 {
		return []byte{byte(n)}
	}

	if n < 16384 {
		return []byte{byte(n>>8) | 0x40, byte(n)}
	}

	return []byte{byte(n>>24) | 0x80, byte(n >> 16), byte(n >> 8), byte(n)}
}
